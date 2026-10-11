package engine

import (
	"strings"
	"testing"
	"time"
)

// Расписание пика — настройка, а не код: провайдер меняет часы когда
// захочет. Значит разбор строки должен быть надёжным, а непонятная строка —
// означать «пика нет», а не выдуманные часы.
func TestSchedulePeak(t *testing.T) {
	sched := "пн-пт 01:00-04:00,06:00-10:00 UTC"
	cases := []struct {
		when string
		peak bool
		what string
	}{
		{"2026-09-07T02:30:00Z", true, "понедельник, середина первого окна"},
		{"2026-09-07T04:00:00Z", false, "ровно конец окна — уже не пик"},
		{"2026-09-07T01:00:00Z", true, "ровно начало окна — уже пик"},
		{"2026-09-07T07:15:00Z", true, "второе окно"},
		{"2026-09-07T05:00:00Z", false, "между окнами"},
		{"2026-09-07T23:00:00Z", false, "ночь"},
		{"2026-09-04T02:30:00Z", true, "пятница"},
		{"2026-09-05T02:30:00Z", false, "суббота — выходной"},
		{"2026-09-13T07:00:00Z", false, "воскресенье"},
	}
	for _, c := range cases {
		when, err := time.Parse(time.RFC3339, c.when)
		if err != nil {
			t.Fatal(err)
		}
		if got := inPeak(sched, when); got != c.peak {
			t.Errorf("%s (%s): вышло %v, ждали %v", c.when, c.what, got, c.peak)
		}
	}
}

// Непонятное расписание — это «пика нет»: считать по выдуманным часам
// хуже, чем по обычной цене.
func TestUnclearScheduleNotPeak(t *testing.T) {
	cases := []string{"", "каждый день", "пн-пт", "abc 01:00-04:00", "пн-пт вечером"}
	when := time.Date(2026, 9, 7, 2, 30, 0, 0, time.UTC)
	for _, r := range cases {
		if inPeak(r, when) {
			t.Errorf("расписание %q приняли за пик", r)
		}
	}
}

// Окно через полночь: «22:00-02:00» — это два куска суток, а не пустота.
func TestWindowThroughMidnight(t *testing.T) {
	sched := "пн-вс 22:00-02:00"
	if !inPeak(sched, time.Date(2026, 9, 7, 23, 0, 0, 0, time.UTC)) {
		t.Error("23:00 не попало в окно 22:00-02:00")
	}
	if !inPeak(sched, time.Date(2026, 9, 7, 1, 0, 0, 0, time.UTC)) {
		t.Error("01:00 не попало в окно 22:00-02:00")
	}
	if inPeak(sched, time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)) {
		t.Error("полдень попал в ночное окно")
	}
}

// Пиковая цена без расписания бессмысленна и потому не принимается: иначе
// человек задал бы дорогой тариф, который никогда не применится, и удивился
// бы счёту. Разобранное расписание проверяется тут же.
func TestAssignmentPeakPrice(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	if err := e.saveUserModels([]ModelConf{{Name: "дипсик", Type: "openai",
		BaseURL: "https://api.deepseek.com/v1"}}); err != nil {
		t.Fatal(err)
	}
	full := "$0.22 / $0.66 / $0.007 / 1M | $0.44 / $1.32 / $0.014 / 1M | пн-пт 01:00-04:00,06:00-10:00 UTC"
	if err := e.SetModelPrice("дипсик", full); err != nil {
		t.Fatal(err)
	}
	m := e.userModels()[0]
	if m.Price == "" || m.PricePeak == "" || m.Peak == "" {
		t.Fatalf("части не разошлись: %+v", m)
	}
	if !strings.Contains(m.Peak, "пн-пт") {
		t.Fatalf("расписание: %q", m.Peak)
	}
	// пиковая цена без расписания — отказ, а не тихое сохранение
	if err := e.SetModelPrice("дипсик", "$0.22 / 1M | $0.44 / 1M"); err == nil {
		t.Fatal("пиковая цена без расписания принята")
	}
	// непонятное расписание — тоже отказ: молчаливое «никогда не пик» хуже
	if err := e.SetModelPrice("дипсик", "$0.22 / 1M | $0.44 / 1M | по вторникам вечером"); err == nil {
		t.Fatal("непонятное расписание принято")
	}
}
