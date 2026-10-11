package engine

// Проверка 3: счётчик, который должен расти, не растёт.
//
// Зеркало файлов стояло выключенным месяц, и 260 вложений лежали в одном
// экземпляре на карточке Raspberry Pi. Экран показывал строку «зеркало» без
// единого числа, и это выглядело порядком. Векторы в тот же день встали на
// 1707 из 2141 и стояли, пока человек не спросил.
//
// Пара «сделано из всего» есть у пяти воркеров и была всё это время. Никто
// на неё не смотрел.
//
// Мера прежняя: работа осмысленна, если после неё что-то стало другим. Здесь
// «что-то» — само число. Задача отработала, а счётчик не сдвинулся — значит
// проход был впустую.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// workCounter — пара «сделано из всего» у одного дела.
type workCounter struct {
	Name  string // как назвать человеку
	Task  string // какая задача его двигает
	Where string // экран, где это чинят
	Take  func() (done, total int)
}

// counters — все пары «сделано из всего», за которыми стоит смотреть.
//
// Список здесь, а не у каждого воркера: пара уже существует и отдаётся
// наружу, добавлять контракт ради наблюдения незачем. Зато видно все дела,
// которые обязаны доходить до конца, одним списком.
func (e *Engine) counters() []workCounter {
	// подмена для проверок: настоящие пары приходят от живых воркеров, а
	// проверять надо саму мерку — стоит счётчик или идёт
	if e.countersForTest != nil {
		return e.countersForTest()
	}
	return []workCounter{
		{Name: "Зеркало файлов", Task: WorkerFilesMirror, Where: "settings:storages",
			Take: e.MirrorPass},
		{Name: "Вложения из писем", Task: WorkerAttachments, Where: "files",
			Take: e.attachmentProgress},
		{Name: "Архив писем", Task: WorkerArchive, Where: "inbox", Take: func() (int, int) {
			no, total := e.ArchiveProgress()
			return total - no, total
		}},
	}
}

// checkCounters — счётчик стоит, хотя задача, которая его двигает,
// отработала.
//
// Молчим, пока дело сделано целиком: «260 из 260» не растёт и не должно.
// Молчим и когда задача выключена: это решение человека, а не беда, — про
// выключенное говорит другая проверка.
func (e *Engine) checkCounters() int {
	task, err := e.store.Tasks()
	if err != nil {
		return 0
	}
	bySystem := map[string]*store.Task{}
	for _, t := range task {
		if t.System != "" {
			bySystem[t.System] = t
		}
	}
	found := 0
	for _, s := range e.counters() {
		key := "стоит:" + s.Task
		done, total := s.Take()
		t := bySystem[s.Task]
		switch {
		case total == 0 || done >= total:
			// делать нечего или всё сделано — это не беда, а конец работы
			e.closeIfWas(key, "дело доведено до конца")
			continue
		case t == nil || !t.Enabled:
			// выключено человеком: про это говорит проверка объявленного
			e.closeIfWas(key, "задача выключена")
			continue
		}
		// Что было в прошлый раз: само число и момент, на который оно
		// записано. Оба в одной настройке — иначе запись каждый проход
		// зальёт журнал изменений настроек сотнями строк в сутки.
		previous, when, was := e.previousCount(key)
		if !was || previous != done {
			e.rememberCount(key, done, t.LastRun)
			e.closeIfWas(key, "счётчик пошёл")
			continue
		}
		// счётчик стоит. Беда это или нет, решает одно: работала ли с тех
		// пор задача, которая его двигает
		if !t.LastRun.After(when) {
			continue // не работала — ждать нечего
		}
		e.sayAboutFinding(store.Finding{
			Key: key, Kind: "счётчик не растёт", Ref: s.Name,
			Level:   store.FindingWarn,
			Summary: fmt.Sprintf("«%s» отработало, а сделано столько же, сколько было", s.Name),
			Numbers: fmt.Sprintf("%d из %d, не двигается", done, total),
			Measure: store.MeasureAlert, Screen: s.Where,
		})
		found++
	}
	return found
}

// Прошлый счёт и момент, на который он записан, — в одной настройке.
//
// Своей таблицы ради двух чисел на дело заводить незачем, а переживать
// перезапуск они должны: перезапуск раз в час иначе прячет любую остановку.
// Записываем только когда число изменилось — иначе журнал изменений
// настроек получал бы сотни строк в сутки ни о чём.
func (e *Engine) previousCount(key string) (count int, when time.Time, was bool) {
	v := e.store.Setting("watch.count."+key, "")
	number, ts, ok := strings.Cut(v, "|")
	if !ok {
		return 0, time.Time{}, false
	}
	n, err := strconv.Atoi(number)
	if err != nil {
		return 0, time.Time{}, false
	}
	t, _ := time.Parse(time.RFC3339, ts)
	return n, t, true
}

func (e *Engine) rememberCount(key string, n int, run time.Time) {
	_ = e.store.SetSetting("watch.count."+key,
		fmt.Sprintf("%d|%s", n, run.Format(time.RFC3339)))
}
