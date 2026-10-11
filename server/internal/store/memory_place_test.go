package store

import (
	"path/filepath"
	"testing"
)

// Знание о коде живёт по символу, а не по строке: строки съезжают от любой
// правки, а имя функции переживает и переносы, и переименования файлов.
func TestKnowledgeBindsToPlace(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "память.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.SaveMemory(&Memory{Kind: MemLesson, Area: "код",
		Text: "clip и любые пределы — по рунам, а не по байтам: русский текст режется вдвое раньше"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindToPlace(Place{MemoryID: id,
		Repo: "server", File: "internal/engine/rag.go", Symbol: "ragContext", Line: 350}); err != nil {
		t.Fatal(err)
	}

	// нашли по файлу
	byFile, err := st.PlaceKnowledge("server", "internal/engine/rag.go", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(byFile) != 1 {
		t.Fatalf("по файлу нашлось %d записей", len(byFile))
	}
	// и по символу — даже если файл переехал
	bySymbol, err := st.PlaceKnowledge("server", "internal/rag/context.go", "ragContext", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(bySymbol) != 1 {
		t.Fatal("после переезда файла знание о функции потерялось")
	}
	// чужое место ничего не возвращает
	other, _ := st.PlaceKnowledge("server", "internal/api/ui.go", "", 5)
	if len(other) != 0 {
		t.Fatalf("нашлось лишнее: %d", len(other))
	}
	// повтор привязки не двоится
	if err := st.BindToPlace(Place{MemoryID: id, Repo: "server",
		File: "internal/engine/rag.go", Symbol: "ragContext", Line: 402}); err != nil {
		t.Fatal(err)
	}
	if places := st.MemoryPlaces(id); len(places) != 1 || places[0].Line != 402 {
		t.Fatalf("привязка задвоилась или не обновила строку: %+v", places)
	}
	// привязка без места — ошибка, а не тихое ничего
	if err := st.BindToPlace(Place{MemoryID: id}); err == nil {
		t.Fatal("пустая привязка принята")
	}
}

// Забытая запись уносит свою координату: разбор памяти забывает мусор
// каждый день, и привязки без записи копились бы в базе молча, а
// МестаЗаписи отдавал бы призраков — координаты знания, которого нет.
func TestForgottenMemoryTakesPlaceAway(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "память.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.SaveMemory(&Memory{Kind: MemObservation, Area: "код", Text: "проба"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindToPlace(Place{MemoryID: id,
		Repo: "server", File: "internal/web/icons.go", Symbol: "Значок"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMemory(id); err != nil {
		t.Fatal(err)
	}
	if places := st.MemoryPlaces(id); len(places) != 0 {
		t.Fatalf("после удаления записи осталось привязок: %d", len(places))
	}
	knowledge, err := st.PlaceKnowledge("server", "internal/web/icons.go", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(knowledge) != 0 {
		t.Fatalf("забытая запись всё ещё отвечает про место: %d", len(knowledge))
	}
}

// Переписанный урок остаётся про то же место: разбор памяти каждый день
// переписывает записи понятнее, и без переноса координаты знание живо,
// а вопрос «что здесь уже ломалось» остаётся без ответа.
func TestNewVersionInheritsPlace(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "память.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	older, err := st.SaveMemory(&Memory{Kind: MemLesson, Area: "код", Text: "пределы по рунам"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindToPlace(Place{MemoryID: older, Repo: "server",
		File: "internal/engine/rag.go", Symbol: "ragContext", Source: "урок из работы"}); err != nil {
		t.Fatal(err)
	}
	newer, err := st.SaveMemory(&Memory{Kind: MemLesson, Area: "код",
		Text: "clip и любые пределы считаются по рунам", Supersedes: older})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MovePlaces(older, newer); err != nil {
		t.Fatal(err)
	}
	knowledge, err := st.PlaceKnowledge("server", "internal/engine/rag.go", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(knowledge) != 1 || knowledge[0].ID != newer {
		t.Fatalf("про место отвечает не новая версия: %d записей", len(knowledge))
	}
	// у старой версии координата остаётся: это её история
	if places := st.MemoryPlaces(older); len(places) != 1 {
		t.Fatalf("у заменённой записи привязок %d", len(places))
	}
	if places := st.MemoryPlaces(newer); len(places) != 1 || places[0].Source != "урок из работы" {
		t.Fatalf("источник привязки не переехал: %+v", places)
	}
}

// Забытый кристалл не оставляет сессию со ссылкой в пустоту: капсула
// «кристалл» без текста врёт сильнее, чем честное «без кристалла».
func TestForgottenCrystalUnbindsFromSession(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "память.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	session, err := st.StartMemorySession(0, "проба", "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.SaveMemory(&Memory{Kind: MemCrystal, Area: "код", Text: "итог сессии"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionCrystal(session, id); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMemory(id); err != nil {
		t.Fatal(err)
	}
	m, err := st.MemorySession(session)
	if err != nil {
		t.Fatal(err)
	}
	if m.Crystal > 0 {
		t.Fatalf("сессия всё ещё ссылается на забытый кристалл: %d", m.Crystal)
	}
}
