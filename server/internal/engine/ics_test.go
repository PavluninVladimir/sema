package engine

import (
	"testing"
	"time"
)

// Приглашение говорит о времени тремя способами, и путать их нельзя.
// «140000Z» — это UTC; Сёма читала его как местное, и напоминание про
// встречу в 14:00 UTC приходило в 14:00 по Самаре — на четыре часа раньше
// самой встречи. Ошибка неочевидная: время выглядит правдоподобно, просто
// оно чужое.
func TestTimeInvite(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip("поясов нет в сборке")
	}
	cases := []struct {
		name, text string
		want       time.Time
	}{
		{"UTC с буквой Z",
			"BEGIN:VEVENT\r\nDTSTART:20260912T140000Z\r\nSUMMARY:Созвон\r\nEND:VEVENT",
			time.Date(2026, 9, 12, 14, 0, 0, 0, time.UTC)},
		{"названный пояс",
			"BEGIN:VEVENT\r\nDTSTART;TZID=Europe/Moscow:20260912T140000\r\nSUMMARY:Созвон\r\nEND:VEVENT",
			time.Date(2026, 9, 12, 14, 0, 0, 0, msk)},
		{"пояс в кавычках",
			"DTSTART;TZID=\"Europe/Moscow\":20260912T140000\r\nSUMMARY:Созвон",
			time.Date(2026, 9, 12, 14, 0, 0, 0, msk)},
		{"плавающее время — местное",
			"DTSTART:20260912T140000\r\nSUMMARY:Созвон",
			time.Date(2026, 9, 12, 14, 0, 0, 0, time.Local)},
		{"неизвестный пояс — считаем местным, а не сдвигаем наугад",
			"DTSTART;TZID=Марс/Олимп:20260912T140000\r\nSUMMARY:Созвон",
			time.Date(2026, 9, 12, 14, 0, 0, 0, time.Local)},
		{"событие на весь день — девять утра",
			"DTSTART;VALUE=DATE:20260912\r\nSUMMARY:Отпуск",
			time.Date(2026, 9, 12, 9, 0, 0, 0, time.Local)},
	}
	for _, s := range cases {
		t.Run(s.name, func(t *testing.T) {
			got, summary, ok := parseIcs(s.text)
			if !ok {
				t.Fatal("время не разобрано")
			}
			if !got.Equal(s.want) {
				t.Errorf("время %s, а надо %s (разница %s)",
					got.Format(time.RFC3339), s.want.Format(time.RFC3339), got.Sub(s.want))
			}
			if summary == "" {
				t.Error("название встречи потеряно")
			}
		})
	}
}

// Класс «не двоеточие» ловит и перевод строки: прежнее выражение могло
// взять слово DTSTART из одной строки, а цифры — из другой.
func TestInviteNotMergesLine(t *testing.T) {
	text := "DTSTART\r\nX-WR-CALNAME:20260912T140000Z\r\n"
	if _, _, ok := parseIcs(text); ok {
		t.Error("время склеено из двух разных строк")
	}
}
