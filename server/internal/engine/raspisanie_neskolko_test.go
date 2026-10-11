package engine

import (
	"testing"
	"time"
)

// Одна задача — несколько времён: «9:00, 19:00» срабатывает утром и
// вечером, по разу, а не заводит вторую такую же задачу.
func TestSeveralSchedulesInOneTask(t *testing.T) {
	day := func(hh, mm int) time.Time { return time.Date(2026, 9, 17, hh, mm, 0, 0, time.Local) }
	cases := []struct {
		name      string
		last, now time.Time
		due       bool
	}{
		{"утро наступило", day(8, 0), day(9, 1), true},
		{"утро уже было", day(9, 1), day(12, 0), false},
		{"вечер наступил", day(9, 1), day(19, 2), true},
		{"вечер уже был", day(19, 2), day(22, 0), false},
	}
	for _, s := range cases {
		due, err := nextRun("9:00, 19:00", s.last, s.now)
		if err != nil || due != s.due {
			t.Errorf("%s: пора=%v, ошибка %v", s.name, due, err)
		}
	}
	if _, err := nextRun("9:00; когда-нибудь", time.Time{}, day(10, 0)); err == nil {
		t.Error("непонятная часть расписания прошла молча")
	}
}
