package store

import (
	"path/filepath"
	"testing"
)

// Папка — четвёртый уровень привязки. Знание папки приходит любому файлу
// внутри неё, но только от ближайшей заведённой папки.
func TestDirPlaceNearest(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "память.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	save := func(text string, pl Place) int64 {
		t.Helper()
		id, err := st.SaveMemory(&Memory{Kind: MemFact, Area: "код", Text: text})
		if err != nil {
			t.Fatal(err)
		}
		pl.MemoryID = id
		if err := st.BindToPlace(pl); err != nil {
			t.Fatal(err)
		}
		return id
	}
	api := save("принцип api", Place{Repo: "server", Dir: "internal/api/"})
	internal := save("принцип internal", Place{Repo: "server", Dir: "internal"})
	fileID := save("урок про файл", Place{Repo: "server", File: "internal/api/inbox.go"})

	known, err := st.PlacesKnowledge("server", []PlaceQuery{{File: "internal/api/inbox.go"}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(known) != 2 || known[0].ID != fileID || known[1].ID != api || known[1].Level != LevelDir {
		t.Fatalf("ждали файл, затем ближайшую папку api: %+v", known)
	}
	known, _ = st.PlacesKnowledge("server", []PlaceQuery{{File: "internal/engine/queue.go"}}, 10)
	if len(known) != 1 || known[0].ID != internal {
		t.Fatalf("для internal/engine ближайшая папка — internal: %+v", known)
	}
	known, _ = st.PlacesKnowledge("server", []PlaceQuery{{File: "cmd/sema/main.go"}}, 10)
	if len(known) != 0 {
		t.Fatalf("у cmd/ заведённых папок нет: %+v", known)
	}
	// хвостовой слэш не делает другую папку
	if places := st.MemoryPlaces(api); len(places) != 1 || places[0].Dir != "internal/api" {
		t.Fatalf("папка не очищена от слэша: %+v", places)
	}
	// одна запись на две папки — две привязки, ключ различает папки
	if err := st.BindToPlace(Place{MemoryID: api, Repo: "server", Dir: "internal/web"}); err != nil {
		t.Fatal(err)
	}
	if places := st.MemoryPlaces(api); len(places) != 2 {
		t.Fatalf("вторая папка затёрла первую: %+v", places)
	}
}

// База прежней схемы — без колонки dir — открывается и пересобирает
// привязки без потерь. 7 октября 2026 выкат упал на старте: индекс по
// dir создавался раньше миграции, на свежей базе это не воспроизводилось.
func TestOpenOldPlacesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "старая.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.SaveMemory(&Memory{Kind: MemLesson, Area: "код", Text: "урок"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindToPlace(Place{MemoryID: id, Repo: "server", File: "a.go", Symbol: "f"}); err != nil {
		t.Fatal(err)
	}
	// возвращаем прежнюю схему: таблица без dir и без индекса по папке
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_memory_places_dir`,
		`CREATE TABLE memory_places_old (memory_id INTEGER NOT NULL, repo TEXT NOT NULL DEFAULT '', file TEXT NOT NULL DEFAULT '',
			symbol TEXT NOT NULL DEFAULT '', line INTEGER NOT NULL DEFAULT 0, added TEXT NOT NULL, source TEXT NOT NULL DEFAULT '',
			doubtful INTEGER NOT NULL DEFAULT 0, checked TEXT NOT NULL DEFAULT '', PRIMARY KEY (memory_id, repo, file, symbol))`,
		`INSERT INTO memory_places_old SELECT memory_id, repo, file, symbol, line, added, source, doubtful, checked FROM memory_places`,
		`DROP TABLE memory_places`,
		`ALTER TABLE memory_places_old RENAME TO memory_places`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q[:30], err)
		}
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("старая база не открылась: %v", err)
	}
	defer st.Close()
	places := st.MemoryPlaces(id)
	if len(places) != 1 || places[0].Symbol != "f" || places[0].Dir != "" {
		t.Fatalf("привязка потеряна при пересборке: %+v", places)
	}
	if err := st.BindToPlace(Place{MemoryID: id, Repo: "server", Dir: "internal"}); err != nil {
		t.Fatal(err)
	}
}
