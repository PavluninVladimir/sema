package script

// Периоды по-русски для скриптов и модулей: «за август», «в июле 2026»,
// «этот месяц», «прошлый месяц», «этот год». Доступно всем как
// sema.period(text) — оно ничего не открывает наружу.

import (
	"fmt"
	"strings"
	"time"
)

var monthStems = []struct {
	Stem  string
	Month time.Month
}{
	{"январ", time.January}, {"феврал", time.February}, {"март", time.March}, {"апрел", time.April},
	{"мая", time.May}, {"май", time.May}, {"июн", time.June}, {"июл", time.July}, {"август", time.August},
	{"сентябр", time.September}, {"октябр", time.October}, {"ноябр", time.November}, {"декабр", time.December},
}

// ParsePeriod возвращает границы периода и метку (2026-08); пустая метка —
// периода в тексте нет.
func ParsePeriod(query string) (from, to time.Time, label string) {
	q := strings.ToLower(query)
	now := time.Now()
	year := now.Year()
	for i := 2020; i <= now.Year()+1; i++ {
		if strings.Contains(q, fmt.Sprint(i)) {
			year = i
		}
	}
	monthStart := func(y int, m time.Month) time.Time {
		return time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	}
	switch {
	case strings.Contains(q, "этот месяц"), strings.Contains(q, "текущий месяц"), strings.Contains(q, "этом месяце"):
		from = monthStart(now.Year(), now.Month())
		return from, from.AddDate(0, 1, 0), from.Format("2006-01")
	case strings.Contains(q, "прошлый месяц"), strings.Contains(q, "прошлом месяце"):
		from = monthStart(now.Year(), now.Month()-1)
		return from, from.AddDate(0, 1, 0), from.Format("2006-01")
	case strings.Contains(q, "этот год"), strings.Contains(q, "этом году"), strings.Contains(q, "за год"):
		from = monthStart(now.Year(), time.January)
		return from, from.AddDate(1, 0, 0), from.Format("2006")
	case strings.Contains(q, "прошлый год"), strings.Contains(q, "прошлом году"):
		from = monthStart(now.Year()-1, time.January)
		return from, from.AddDate(1, 0, 0), from.Format("2006")
	}
	for _, m := range monthStems {
		if strings.Contains(q, m.Stem) {
			from = monthStart(year, m.Month)
			return from, from.AddDate(0, 1, 0), from.Format("2006-01")
		}
	}
	return time.Time{}, time.Time{}, ""
}
