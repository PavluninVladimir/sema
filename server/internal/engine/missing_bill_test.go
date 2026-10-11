package engine

import (
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ритм проверяем без базы: правило «говорим о пропаже, только когда
// уверены в ритме» иначе держалось бы на честном слове. Ошибка здесь
// дорога не деньгами, а доверием — ассистент, который раз в месяц зря
// говорит «счёт не пришёл», перестаёт быть ассистентом.
func TestRhythmProvider(t *testing.T) {
	today := time.Date(2026, 9, 12, 10, 0, 0, 0, time.Local)
	v := &store.Vendor{ID: 1, Name: "Горэнерго", Files: 5}

	cases := []struct {
		name  string
		dates []string
		want  bool
		зачем string
	}{
		{"ровный месячный ритм, счёта нет", []string{"2026-05-03", "2026-06-03", "2026-07-04", "2026-08-03"},
			true, "четыре счёта около третьего числа, сентябрьского нет"},
		{"счёт этого месяца пришёл", []string{"2026-06-03", "2026-07-03", "2026-08-03", "2026-09-03"},
			false, "сентябрьский счёт на месте"},
		{"мало счетов", []string{"2026-07-03", "2026-08-03", "2026-09-03"[:10]},
			false, "три счёта — совпадение, а не ритм"},
		{"разброс дня велик", []string{"2026-05-02", "2026-06-14", "2026-07-01", "2026-08-19"},
			false, "день месяца гуляет на две недели — ритма нет"},
		{"не месячный ритм", []string{"2026-08-01", "2026-08-08", "2026-08-15", "2026-08-22"},
			false, "раз в неделю — это не наш случай"},
		{"поставщик закончился", []string{"2026-02-03", "2026-03-03", "2026-04-03", "2026-05-03"},
			false, "молчит четыре месяца: это конец отношений, а не пропажа"},
	}
	for _, s := range cases {
		t.Run(s.name, func(t *testing.T) {
			p, ok := providerRhythm(v, invoicesOnDates(s.dates), today)
			if ok != s.want {
				t.Fatalf("ритм: %v, ждали %v (%s) · %+v", ok, s.want, s.зачем, p)
			}
			if ok && p.UsuallyDay < 1 {
				t.Errorf("день месяца не назван: %+v", p)
			}
		})
	}
}

// Пока срок не вышел, Сёма молчит: сказать «счёт не пришёл» второго числа,
// когда он приходит третьего, — это шум, а не забота.
func TestEarlyNotSay(t *testing.T) {
	v := &store.Vendor{ID: 1, Name: "Горэнерго", Files: 5}
	dates := invoicesOnDates([]string{"2026-05-03", "2026-06-03", "2026-07-04", "2026-08-03"})
	// третье число месяца — счёт мог прийти сегодня вечером
	if _, ok := providerRhythm(v, dates, time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local)); ok {
		t.Error("Сёма забеспокоилась в тот же день, когда счёт обычно приходит")
	}
	// и через пару дней тоже рано
	if _, ok := providerRhythm(v, dates, time.Date(2026, 9, 6, 10, 0, 0, 0, time.Local)); ok {
		t.Error("Сёма забеспокоилась через два дня после обычного срока")
	}
	// а через неделю — пора
	if _, ok := providerRhythm(v, dates, time.Date(2026, 9, 12, 10, 0, 0, 0, time.Local)); !ok {
		t.Error("прошла неделя после обычного срока, а Сёма молчит")
	}
}

// Чеки в ритм не идут: ритм — про то, когда приходит СЧЁТ, а чеки от того
// же поставщика приходят когда попало.
func TestReceiptsNotBreakRhythm(t *testing.T) {
	v := &store.Vendor{ID: 1, Name: "Горэнерго", Files: 8}
	files := invoicesOnDates([]string{"2026-05-03", "2026-06-03", "2026-07-04", "2026-08-03"})
	for _, d := range []string{"2026-08-19", "2026-09-01", "2026-09-08"} {
		files = append(files, &store.Attachment{Kind: store.AttKindReceipt, DocDate: d, ToPay: false})
	}
	if _, ok := providerRhythm(v, files, time.Date(2026, 9, 12, 10, 0, 0, 0, time.Local)); !ok {
		t.Error("чеки сбили ритм счетов")
	}
}

func invoicesOnDates(dates []string) []*store.Attachment {
	var out []*store.Attachment
	for _, d := range dates {
		out = append(out, &store.Attachment{Kind: store.AttKindReceipt, DocDate: d, ToPay: true, Amount: 500})
	}
	return out
}
