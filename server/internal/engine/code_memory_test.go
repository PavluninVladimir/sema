package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Догадка о месте нарочно узкая: лучше не привязать, чем привязать не туда.
// Неверная координата хуже отсутствия — по ней придёт тот, кому она не нужна.
func TestPlaceFromText(t *testing.T) {
	cases := []struct {
		text   string
		файлов int
		первый string
		репо   string
		line   int
	}{
		{"починили server/internal/engine/rag.go:350 — резали по байтам", 1, "internal/engine/rag.go", "server", 350},
		{"правка в app/Sources/Sema/DynamicView.swift", 1, "Sources/Sema/DynamicView.swift", "app", 0},
		{"разобрались с очередью и ничего не трогали", 0, "", "", 0},
		{"два места: internal/api/ui.go и internal/api/web.go", 2, "internal/api/ui.go", "", 0},
	}
	for _, c := range cases {
		place := placesFromText(c.text)
		if len(place) != c.файлов {
			t.Errorf("%q → %d мест, ждали %d", clip(c.text, 40), len(place), c.файлов)
			continue
		}
		if c.файлов == 0 {
			continue
		}
		if place[0].File != c.первый || place[0].Repo != c.репо || place[0].Line != c.line {
			t.Errorf("%q → %+v, ждали файл %q репо %q строка %d",
				clip(c.text, 40), place[0], c.первый, c.репо, c.line)
		}
	}
}

// Переименование символа не должно терять заметку: место могло переехать, а
// знание остаться верным. Поэтому привязка помечается сомнительной, а не
// удаляется.
func TestRenameMarksDoubtful(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	m, err := e.Remember(nil, Origin{Consumer: e.ChatConsumer()}, MemoryRecord{
		Kind: store.MemLesson, Area: "код",
		Text:  "здесь нельзя резать строку по байтам",
		Place: &store.Place{Repo: "server", File: "internal/engine/rag.go", Symbol: "ragContext"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if place := st.MemoryPlaces(m.ID); len(place) != 1 {
		t.Fatalf("привязка не сохранилась: %+v", place)
	}

	// символ переименовали — помечаем сомнительным
	if err := st.MarkDoubtful(m.ID, "ragContext", true); err != nil {
		t.Fatal(err)
	}
	place := st.MemoryPlaces(m.ID)
	if len(place) != 1 {
		t.Fatal("заметка потерялась вместе с символом — так нельзя")
	}
	if !place[0].Doubtful {
		t.Fatal("привязка не помечена сомнительной")
	}
	if len(st.DoubtfulPlaces(10)) != 1 {
		t.Fatal("разбор памяти не увидит сомнительную привязку")
	}
	// сама запись жива и находится по файлу
	if has, _ := st.PlaceKnowledge("server", "internal/engine/rag.go", "", 5); len(has) != 1 {
		t.Fatal("запись перестала находиться по файлу")
	}
}

// Запись без координаты ведёт себя как прежде: привязка необязательна.
func TestRecordWithoutPlace(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	m, err := e.Remember(nil, Origin{Consumer: e.ChatConsumer()}, MemoryRecord{
		Kind: store.MemFact, Area: "код", Text: "Сёма живёт на Raspberry Pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if place := st.MemoryPlaces(m.ID); len(place) != 0 {
		t.Fatalf("у записи без координаты появились места: %+v", place)
	}
	// и она не всплывает в знании о местах
	if has, _ := st.PlaceKnowledge("server", "internal/engine/rag.go", "", 5); len(has) != 0 {
		t.Fatal("запись без координаты попала в знание о месте")
	}
	if !strings.Contains(m.Text, "Raspberry") {
		t.Fatal("запись испортилась")
	}
}

// Знание полезно, пока не вытесняет сам код. Предел — как у скиллов в
// промпте, и обрезка идёт по строкам: обрывок урока хуже его отсутствия,
// по нему делают неверный вывод.
func TestAttachNotExceedsLimit(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	if err := e.store.SetSetting("code.knowledge_chars", "300"); err != nil {
		t.Fatal(err)
	}
	o := Origin{Consumer: e.ChatConsumer()}
	for i := 0; i < 12; i++ {
		if _, err := e.Remember(nil, o, MemoryRecord{
			Kind: store.MemLesson, Area: "код",
			Text: "Урок номер " + itoa(i) + ": " + strings.Repeat("длинная подробность, ", 6),
			Place: &store.Place{Repo: "server", File: "internal/engine/rag.go",
				Symbol: "ragContext" + itoa(i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// потребителю нужно выдать область, иначе знание ему не покажут — и
	// это правильно: права у приложения те же, что у припоминания
	o2 := e.ExternalOrigin("проба", "/Users/x/repo/sema/server")
	if err := e.store.SetConsumerAreas(o2.Consumer.ID, []string{"код"}); err != nil {
		t.Fatal(err)
	}
	text := e.AttachToPlace(nil, "проба", "/Users/x/repo/sema/server", "internal/engine/rag.go", "")
	if text == "" {
		t.Fatal("приложение пустое, а записи есть")
	}
	if n := len([]rune(text)); n > e.CodeKnowledgeLimit() {
		t.Fatalf("приложение %d знаков при пределе %d", n, e.CodeKnowledgeLimit())
	}
	if !strings.Contains(text, "и ещё") {
		t.Fatal("остаток не назван: молча урезанное знание хуже названного остатка")
	}
	// каждая строка приложения — целая строка полного знания, а не обрывок
	full := e.CodeKnowledge(nil, "проба", "/Users/x/repo/sema/server", "internal/engine/rag.go", "")
	ints := map[string]bool{}
	for _, str := range strings.Split(full, "\n") {
		ints[str] = true
	}
	for _, str := range strings.Split(strings.TrimSpace(text), "\n") {
		if str == "" || strings.HasPrefix(str, "…и ещё") {
			continue
		}
		if !ints[str] {
			t.Fatalf("строка приложения не совпадает с полной — значит оборвана: %q", clip(str, 80))
		}
	}
}

// Протухшая координата снимается, а знание остаётся: устаревает привязка,
// а не урок. Потерять урок из-за переименования функции — худший исход.
func TestStaleBindingClearedKnowledgeStays(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.SetSetting("code.place_ttl_days", "30"); err != nil {
		t.Fatal(err)
	}
	m, err := e.Remember(nil, Origin{Consumer: e.ChatConsumer()}, MemoryRecord{
		Kind: store.MemLesson, Area: "код", Text: "тут нельзя резать по байтам",
		Place: &store.Place{Repo: "server", File: "internal/engine/rag.go", Symbol: "старыйСимвол"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDoubtful(m.ID, "старыйСимвол", true); err != nil {
		t.Fatal(err)
	}
	// свежая пометка — не трогаем: место могло переехать, разберёмся позже
	e.parseDoubtfulPlaces()
	if len(st.MemoryPlaces(m.ID)) != 1 {
		t.Fatal("свежую сомнительную привязку сняли слишком рано")
	}
	// состарим пометку
	if err := st.AgePlace(m.ID, time.Now().AddDate(0, 0, -100)); err != nil {
		t.Fatal(err)
	}
	e.parseDoubtfulPlaces()
	if len(st.MemoryPlaces(m.ID)) != 0 {
		t.Fatal("протухшая привязка осталась")
	}
	// а сама запись жива
	if got, err := st.MemoryByID(m.ID); err != nil || got == nil || !strings.Contains(got.Text, "по байтам") {
		t.Fatal("вместе с координатой потеряли урок — это худший исход")
	}
}
