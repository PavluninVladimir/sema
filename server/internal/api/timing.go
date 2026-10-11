package api

// Server-Timing у экранов и действий (perf-pass 1.2).
//
// 3 октября 2026 «Здоровье» строилось 2,1 с, и узнать, где именно, было
// нечем: ни разбивки, ни профилировщика. Теперь ответ `/ui` и `/do` несёт
// заголовок, который браузер показывает на вкладке «Сеть»:
//
//	menu;dur=…, action;dur=…, build;dur=…, render;dur=…,
//	sql;dur=…;desc="N queries", sqlmax;dur=…, total;dur=…
//
// sqlmax — самый долгий запрос экрана. Сам запрос (шаблон без значений и кто
// его позвал) в заголовок не идёт, а пишется строкой в журнал сервера, когда
// он дольше порога `debug.slow_sql_ms` (perf-pass 9.4).
//
// Только числа и названия частей: ни адреса, ни данных. Названия
// латиницей — заголовок HTTP кириллицу не переносит. Сюда доходят только
// запросы, прошедшие `protect`, то есть вошедшие.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const serverTimingHeader = "Server-Timing"

type screenTiming struct {
	start   time.Time
	menu    time.Duration // меню и подвал: одинаковы для всех экранов
	menuSQL int           // запросов к базе на меню
	action  time.Duration // само действие (у /do)
	build   time.Duration // сборка документа, вместе с меню
	render  time.Duration // разметка
	sql     *store.SQLTrace
}

type timingKey struct{}

// startTiming заводит счёт на запрос и кладёт его в контекст: сборка
// экрана и writeWeb находят его там, а не в параметрах каждой функции.
// Stop — в defer обработчика.
func startTiming(r *http.Request) (*screenTiming, *http.Request) {
	t := &screenTiming{start: time.Now(), sql: store.TraceSQL()}
	return t, r.WithContext(context.WithValue(r.Context(), timingKey{}, t))
}

func (t *screenTiming) stop() { t.sql.Stop() }

// slowLimit — порог медленного запроса с запомненным значением.
type slowLimit struct {
	mu    sync.Mutex
	limit time.Duration
	at    time.Time
}

// slowLimitTTL — сколько помнится порог: правка на экране доходит за это время.
const slowLimitTTL = 10 * time.Second

func (s *Server) slowSQLLimit() time.Duration {
	s.slowSQL.mu.Lock()
	defer s.slowSQL.mu.Unlock()
	if s.slowSQL.at.IsZero() || time.Since(s.slowSQL.at) > slowLimitTTL {
		s.slowSQL.limit = time.Duration(s.eng.SlowSQLMs()) * time.Millisecond
		s.slowSQL.at = time.Now()
	}
	return s.slowSQL.limit
}

// stopTiming — конец счёта запроса и строка в журнал сервера про самый
// долгий запрос к базе, если он дольше порога. В строке — шаблон запроса
// (значений в нём нет) и кто его позвал: этого хватает, чтобы найти место.
func (s *Server) stopTiming(t *screenTiming, r *http.Request) {
	t.stop()
	slow := t.sql.Slowest()
	if slow.D <= 0 {
		return
	}
	// экран и действие — из запроса человека, поэтому без переводов строки:
	// иначе адрес с %0A дописал бы в журнал поддельную строку
	where := r.URL.Path
	if screen := r.URL.Query().Get("screen"); screen != "" {
		where += " " + screen
	}
	if action := r.Form.Get("action"); action != "" {
		where += " " + action
	}
	where = clipRunes(strings.Join(strings.Fields(where), " "), 120)
	if line, ok := slowSQLLine(slow, s.slowSQLLimit(), where); ok {
		log.Print(line)
	}
}

// slowSQLLine — строка журнала про самый долгий запрос; false — писать
// нечего: порог 0 (выключено) или запрос короче порога.
func slowSQLLine(slow store.SQLSlow, limit time.Duration, where string) (string, bool) {
	if limit <= 0 || slow.D < limit {
		return "", false
	}
	return fmt.Sprintf("медленный запрос %s мс (%s): %s — %s", ms(slow.D), where, slow.Caller, slow.Query), true
}

func timingOf(r *http.Request) *screenTiming {
	t, _ := r.Context().Value(timingKey{}).(*screenTiming)
	return t
}

// timedBy — параметр сборки экрана: отметить, когда собралось меню.
func timedBy(t *screenTiming) screenOpt { return func(o *screenOptions) { o.timing = t } }

func ms(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond)) }

// header — значение заголовка. Меню входит в сборку, но показывается
// отдельно и из неё вычитается: «build» — то, что своё у экрана.
func (t *screenTiming) header() string {
	var parts []string
	add := func(name string, d time.Duration) {
		if d > 0 {
			parts = append(parts, name+";dur="+ms(d))
		}
	}
	build := t.build - t.menu
	if t.menu > 0 {
		parts = append(parts, fmt.Sprintf(`menu;dur=%s;desc="%d queries"`, ms(t.menu), t.menuSQL))
	}
	add("action", t.action)
	add("build", build)
	add("render", t.render)
	st := t.sql.Stats()
	parts = append(parts, fmt.Sprintf(`sql;dur=%s;desc="%d queries"`, ms(st.D), st.N))
	if slow := t.sql.Slowest(); slow.D > 0 {
		parts = append(parts, "sqlmax;dur="+ms(slow.D))
	}
	parts = append(parts, "total;dur="+ms(time.Since(t.start)))
	return strings.Join(parts, ", ")
}
