package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Расписания задач пишутся по-человечески: «каждые 5 сек», «каждые 30 мин»,
// «каждый час», «9:00», «каждое вс 20:00», «30.09 10:00» (один раз) и
// «после «Другая задача»» — такие запускает не время, а предыдущая задача.

// CreateTask разбирает человеческое описание задачи.
func (e *Engine) CreateTask(what, when string) error {
	what, when = strings.TrimSpace(what), strings.TrimSpace(when)
	if what == "" || when == "" {
		return fmt.Errorf("нужны и описание, и расписание")
	}
	afterID, schedule, err := e.ParseAfter(when)
	if err != nil {
		return err
	}
	if afterID == 0 {
		if _, err := nextRun(schedule, time.Time{}, time.Now()); err != nil {
			return fmt.Errorf("не понял расписание %q — примеры: «9:00», «9:00, 19:00», «каждые 30 мин», «каждый час», «30.09 10:00», «после «Архив писем»»", when)
		}
	}
	title := what
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60]) + "…"
	}
	return e.store.CreateTask(&store.Task{
		Title:    title,
		Prompt:   what,
		Kind:     "prompt",
		Schedule: schedule,
		AfterID:  afterID,
		Enabled:  true,
		Sort:     100,
		Once:     reOnceDate.MatchString(schedule),
	})
}

var (
	// «03:30» и «каждый день в 03:30» — одно и то же расписание.
	//
	// Понималась только голая пара часов и минут, а заводились задачи
	// строкой «каждый день в 03:30»: так их сеет сам же heartbeat.go, так
	// они стоят на экране «Задачи» и так их читает человек. Планировщик
	// на такую строку отвечал «unknown schedule», taskDue молча возвращал
	// false — и задача не запускалась НИКОГДА. Молча: ни ошибки, ни
	// строки в журнале, на экране «включена» и красивое расписание.
	//
	// Так «Копия наружу» не снялась ни разу за всё время своего
	// существования, а «Цены у провайдеров» не
	// работали с того дня, как их завели.
	reDaily    = regexp.MustCompile(`^(?:кажд\S*\s+день\s+в\s+)?(\d{1,2}):(\d{2})$`)
	reEverySec = regexp.MustCompile(`^кажд\S*\s+(\d+)\s*сек`)
	reEveryMin = regexp.MustCompile(`^кажд\S*\s+(\d+)\s*мин`)
	reEveryHr  = regexp.MustCompile(`^кажд\S*\s+(\d+)\s*ч`)
	reHourly   = regexp.MustCompile(`^каждый час$`)
	reWeekly   = regexp.MustCompile(`^кажд\S*\s+(пн|вт|ср|чт|пт|сб|вс)\S*\s+(\d{1,2}):(\d{2})$`)
	reOnceDate = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.?(\d{4})?\s+(\d{1,2}):(\d{2})$`)
)

var weekdays = map[string]time.Weekday{
	"пн": time.Monday, "вт": time.Tuesday, "ср": time.Wednesday,
	"чт": time.Thursday, "пт": time.Friday, "сб": time.Saturday, "вс": time.Sunday,
}

// nextRun решает, пора ли выполнять задачу (lastRun — прошлый запуск).
// Несколько расписаний пишутся через запятую или точку с запятой —
// «9:00, 19:00»: у одной задачи бывает утренний и вечерний заход, и две
// одинаковые задачи ради второго времени путали экран (17 сентября 2026
// «Утренняя» и «Вечерняя подборка» показывали один и тот же расход).
// Задача пора, если пора хоть одному; непонятная часть — ошибка целиком,
// иначе половина расписания молча не срабатывала бы.
func nextRun(schedule string, lastRun, now time.Time) (bool, error) {
	if parts := strings.FieldsFunc(schedule, func(r rune) bool { return r == ',' || r == ';' }); len(parts) > 1 {
		due := false
		for _, part := range parts {
			ok, err := nextRun(part, lastRun, now)
			if err != nil {
				return false, err
			}
			due = due || ok
		}
		return due, nil
	}
	s := strings.ToLower(strings.TrimSpace(schedule))
	switch {
	case reDaily.MatchString(s):
		m := reDaily.FindStringSubmatch(s)
		at := timeAt(now, m[1], m[2])
		return now.After(at) && lastRun.Before(at), nil
	case reEverySec.MatchString(s):
		n, _ := strconv.Atoi(reEverySec.FindStringSubmatch(s)[1])
		return now.Sub(lastRun) >= time.Duration(n)*time.Second, nil
	case reEveryMin.MatchString(s):
		n, _ := strconv.Atoi(reEveryMin.FindStringSubmatch(s)[1])
		return now.Sub(lastRun) >= time.Duration(n)*time.Minute, nil
	case reEveryHr.MatchString(s):
		n, _ := strconv.Atoi(reEveryHr.FindStringSubmatch(s)[1])
		return now.Sub(lastRun) >= time.Duration(n)*time.Hour, nil
	case reHourly.MatchString(s):
		return now.Sub(lastRun) >= time.Hour, nil
	case reWeekly.MatchString(s):
		m := reWeekly.FindStringSubmatch(s)
		if now.Weekday() != weekdays[m[1]] {
			return false, nil
		}
		at := timeAt(now, m[2], m[3])
		return now.After(at) && lastRun.Before(at), nil
	case reOnceDate.MatchString(s):
		m := reOnceDate.FindStringSubmatch(s)
		day, _ := strconv.Atoi(m[1])
		mon, _ := strconv.Atoi(m[2])
		year := now.Year()
		if m[3] != "" {
			year, _ = strconv.Atoi(m[3])
		}
		hh, _ := strconv.Atoi(m[4])
		mm, _ := strconv.Atoi(m[5])
		at := time.Date(year, time.Month(mon), day, hh, mm, 0, 0, now.Location())
		return now.After(at) && lastRun.Before(at), nil
	case strings.HasPrefix(s, "после"):
		return false, nil // такую задачу запускает предыдущая, а не время
	default:
		return false, fmt.Errorf("unknown schedule %q", schedule)
	}
}

func timeAt(now time.Time, hh, mm string) time.Time {
	h, _ := strconv.Atoi(hh)
	m, _ := strconv.Atoi(mm)
	return time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
}
