package engine

// Проверка 2: растущая пауза, упёршаяся в потолок.
//
// 11–12 сентября 2026 MAX рвал связь 120 раз за тринадцать часов. Пауза
// переподключения выросла до своего потолка в пять минут двадцать секунд и
// стояла там всю ночь, а в журнале ровным шагом шла одна и та же строка.
// Никто не заметил: каждая строка по отдельности — обычная заминка, и только
// сто двадцать строк подряд складываются в «связи нет».
//
// Растущая пауза для того и растёт, чтобы не долбиться в закрытую дверь. Но
// пауза, достигшая потолка и не сброшенная часами, — уже не заминка, а
// состояние, и человек имеет право о нём знать. У проекта уже есть про это
// урок в граблях: «любая растущая пауза должна где-то обнуляться — иначе она
// измеряет не нынешнюю беду, а всю историю». Здесь мы смотрим за самим
// обнулением.
//
// Состояние переподключения живёт в памяти процесса, а не в базе: в базу его
// класть незачем, а после перезапуска связь всё равно начинается заново.

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// linkUnderWatch — что известно про одно переподключение.
type linkUnderWatch struct {
	Pause time.Duration
	Cap   time.Duration
	InCap time.Time // когда упёрлась; нулевое — ещё не упёрлась
	Drops int
	Last  time.Time
	Why   string
}

type linkWatch struct {
	mu   sync.Mutex
	link map[string]*linkUnderWatch
}

// Dropped — связь потеряна, переподключение отложено на паузу.
//
// Зовётся оттуда же, где пишется строка в журнал: одно место, один смысл.
// Наблюдатель не угадывает состояние по журналу — ему говорят.
func (e *Engine) Dropped(name string, pause, ceiling time.Duration, why string) {
	e.link.mu.Lock()
	defer e.link.mu.Unlock()
	if e.link.link == nil {
		e.link.link = map[string]*linkUnderWatch{}
	}
	s, has := e.link.link[name]
	if !has {
		s = &linkUnderWatch{}
		e.link.link[name] = s
	}
	s.Pause, s.Cap, s.Why = pause, ceiling, why
	s.Drops++
	s.Last = time.Now()
	switch {
	case pause < ceiling:
		s.InCap = time.Time{} // ещё растёт — это нормальная заминка
	case s.InCap.IsZero():
		s.InCap = time.Now()
	}
}

// LinkAlive — соединение пожило достаточно, пауза сброшена. Обнуление паузы
// и есть то, за чем наблюдаем: без него пауза меряет всю историю, а не
// нынешнюю беду.
func (e *Engine) LinkAlive(name string) {
	e.link.mu.Lock()
	_, was := e.link.link[name]
	delete(e.link.link, name)
	e.link.mu.Unlock()
	// Находку закрываем здесь, а не ждём прохода наблюдателя: он ходит по
	// тем связям, что помнит, а эту мы только что забыли — и запись
	// висела бы открытой вечно при живой связи.
	if was {
		e.closeIfWas("потолок:"+name, "связь вернулась")
	}
}

// checkPauses — пауза стоит в потолке дольше порога.
func (e *Engine) checkPauses() int {
	// ноль здесь — осмысленное значение («говорить сразу»), поэтому читаем
	// настройку сами: settingInt считает ноль за «не задано»
	threshold := 30 * time.Minute
	if v := strings.TrimSpace(e.store.Setting("watch.ceiling_min", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			threshold = time.Duration(n) * time.Minute
		}
	}
	e.link.mu.Lock()
	snapshot := make(map[string]linkUnderWatch, len(e.link.link))
	for name, s := range e.link.link {
		snapshot[name] = *s
	}
	e.link.mu.Unlock()

	found := 0
	for name, s := range snapshot {
		key := "потолок:" + name
		if s.InCap.IsZero() || time.Since(s.InCap) < threshold {
			e.closeIfWas(key, "связь вернулась")
			continue
		}
		e.sayAboutFinding(store.Finding{
			Key: key, Kind: "пауза в потолке", Ref: name,
			Level:   store.FindingWarn,
			Summary: fmt.Sprintf("«%s» переподключается впустую: пауза упёрлась в потолок и не сбрасывается", name),
			Numbers: fmt.Sprintf("%s в потолке %s, обрывов %d, последняя причина: %s",
				humanDuration(time.Since(s.InCap)), s.Cap, s.Drops, clip(s.Why, 80)),
			Measure: store.MeasureAlert, Screen: "health",
		})
		found++
	}
	return found
}

// LinkUnderWatch — что видно человеку: кто и сколько рвётся.
func (e *Engine) LinkUnderWatch() map[string]time.Duration {
	e.link.mu.Lock()
	defer e.link.mu.Unlock()
	out := map[string]time.Duration{}
	for name, s := range e.link.link {
		if !s.InCap.IsZero() {
			out[name] = time.Since(s.InCap)
		}
	}
	return out
}

// LinkState — что известно про одно живое соединение: на связи ли
// и, если нет, сколько раз рвалось и когда. Нет записи — связь есть:
// возврат связи её забывает (см. СвязьЕсть).
func (e *Engine) LinkState(name string) (onLink bool, drops int, last time.Time, pause time.Duration) {
	e.link.mu.Lock()
	defer e.link.mu.Unlock()
	s, has := e.link.link[name]
	if !has {
		return true, 0, time.Time{}, 0
	}
	return false, s.Drops, s.Last, s.Pause
}
