package engine

import (
	"testing"
	"time"
)

// У выгрузки копии должен быть свой срок. 11 сентября 2026 две выгрузки
// подряд провисели по четверти часа без единой строки в журнале: у
// задачи срока нет, а ResponseHeaderTimeout к телу запроса не
// относится — он ждёт заголовков ПОСЛЕ того, как тело дописано.
// Вставшая посреди тела выгрузка не кончалась никогда, а человек всё
// это время думал, что копия делается.
func TestDueUploadCountsBySizeAndSpeed(t *testing.T) {
	// 39 МБ при 20 КБ/с — это чуть больше получаса
	s := uploadDeadline(39<<20, 20<<10)
	if s < 30*time.Minute || s > 35*time.Minute {
		t.Errorf("срок для 39 МБ при 20 КБ/с: %s", s)
	}
	// быстрый канал — короткий срок, но не короче пяти минут
	if got := uploadDeadline(1<<20, 1<<20); got != 5*time.Minute {
		t.Errorf("маленький файл на быстром канале: %s, а нижний предел — 5 минут", got)
	}
	// скорость не названа — берём умолчание, а не делим на ноль
	if got := uploadDeadline(100<<20, 0); got <= 0 {
		t.Errorf("без скорости срок посчитан неверно: %s", got)
	}
}

// Порог «медленно» — настройка: у кого-то канал в мегабит, у кого-то в
// сто, и «медленно» у них разное.
func TestThresholdSpeedConfigurable(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if got := e.MinSpeed(); got != 20<<10 {
		t.Errorf("умолчание порога: %d", got)
	}
	if err := st.SetSetting("backup.min_bps", "4096"); err != nil {
		t.Fatal(err)
	}
	if got := e.MinSpeed(); got != 4096 {
		t.Errorf("порог из настройки не читается: %d", got)
	}
	// мусор в настройке не должен обнулять порог
	if err := st.SetSetting("backup.min_bps", "ерунда"); err != nil {
		t.Fatal(err)
	}
	if got := e.MinSpeed(); got != 20<<10 {
		t.Errorf("мусор в настройке принят за порог: %d", got)
	}
}
