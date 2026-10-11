package script

import (
	"testing"
	"time"
)

// «Напомни сегодня в 16:30» — 16:30, а не 16:00 и не 09:00: срок с
// минутами и словом «сегодня» раньше терялся.
func TestDueAtWithMinutes(t *testing.T) {
	now := time.Date(2026, 9, 7, 13, 37, 0, 0, time.Local)
	cases := map[string]time.Time{
		"сегодня в 16:30": time.Date(2026, 9, 7, 16, 30, 0, 0, time.Local),
		"в 16:30":         time.Date(2026, 9, 7, 16, 30, 0, 0, time.Local),
		"в 9":             time.Date(2026, 9, 8, 9, 0, 0, 0, time.Local),
		"завтра в 7:05":   time.Date(2026, 9, 8, 7, 5, 0, 0, time.Local),
		"сегодня":         time.Date(2026, 9, 7, 18, 0, 0, 0, time.Local),
		"сегодня вечером": time.Date(2026, 9, 7, 18, 0, 0, 0, time.Local),
	}
	for in, want := range cases {
		got, ok := parseFutureDate(in, now)
		if !ok || !got.Equal(want) {
			t.Errorf("%q: %v (ok=%v), ждали %v", in, got, ok, want)
		}
	}
}
