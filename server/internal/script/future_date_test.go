package script

import (
	"testing"
	"time"
)

// Сроки словами, которыми откладывают дело: раньше «через 30 минут»
// становилось «через 30 дней», а «вечером», «на выходных», «после 15-го» и
// точное время от модели не понимались вовсе.
func TestParseFutureDatePhrases(t *testing.T) {
	now := time.Date(2026, 10, 4, 17, 0, 0, 0, time.Local) // воскресенье, 17:00
	cases := []struct {
		in   string
		want time.Time
	}{
		{"через 30 минут", now.Add(30 * time.Minute)},
		{"через полчаса", now.Add(30 * time.Minute)},
		{"через 2 часа", now.Add(2 * time.Hour)},
		{"через пару часов", now.Add(2 * time.Hour)},
		{"вечером", time.Date(2026, 10, 4, 18, 0, 0, 0, time.Local)},
		{"утром", time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)},
		{"на выходных", time.Date(2026, 10, 10, 10, 0, 0, 0, time.Local)},
		{"на следующей неделе", time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)},
		{"после 15-го", time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)},
		{"после 2 числа", time.Date(2026, 11, 3, 9, 0, 0, 0, time.Local)},
		{"2026-10-05 10:00", time.Date(2026, 10, 5, 10, 0, 0, 0, time.Local)},
		{"завтра", time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)},
		{"в понедельник вечером", time.Date(2026, 10, 5, 18, 0, 0, 0, time.Local)},
	}
	for _, c := range cases {
		got, ok := parseFutureDate(c.in, now)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%q: %v (%v), ждали %v", c.in, got, ok, c.want)
		}
	}
	if _, ok := parseFutureDate("потом как-нибудь", now); ok {
		t.Error("«потом как-нибудь» понято как срок")
	}
}
