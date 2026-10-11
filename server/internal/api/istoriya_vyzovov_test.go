package api

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Последний запуск» отвечает, что было сейчас; по нему одному скрипт,
// переставший работать месяц назад, выглядит живым — нужна история
// вызовов (13 сентября 2026).
func TestScriptCallHistory(t *testing.T) {
	s, st := testServer(t)
	id, err := st.CreateScript("Письма за сутки", "return 1")
	if err != nil {
		t.Fatal(err)
	}
	screen := func() string {
		doc, err := s.buildScreen("script:"+itoa(id), "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		return dumpScreen(doc)
	}
	// вызовов не было — так и сказано
	if д := screen(); !strings.Contains(д, "Вызовов пока не было") {
		t.Errorf("нет честного пустого места:\n%s", д)
	}
	// два вызова: удачный и с ошибкой
	потребитель := "script:" + itoa(id)
	if err := st.AddUsage(&store.UsageRow{At: time.Now().Add(-time.Hour), Consumer: потребитель, Kind: "run",
		Seconds: 0.12, OK: true, Note: "строк вывода 3"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddUsage(&store.UsageRow{At: time.Now(), Consumer: потребитель, Kind: "run",
		Seconds: 0.04, OK: false, Note: "TypeError: undefined"}); err != nil {
		t.Fatal(err)
	}
	д := screen()
	if !strings.Contains(д, "История вызовов") {
		t.Fatalf("истории нет:\n%s", д)
	}
	if !strings.Contains(д, "TypeError: undefined") || !strings.Contains(д, "строк вывода 3") {
		t.Errorf("в истории нет обоих вызовов:\n%s", д)
	}
	if !strings.Contains(д, "ошибка") {
		t.Error("неудачный вызов не помечен ошибкой")
	}
	if !strings.Contains(д, "последний — ") {
		t.Error("не сказано, когда звали последний раз")
	}
}
