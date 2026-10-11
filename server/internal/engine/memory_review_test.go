package engine

import (
	"context"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Координату записи сверяет житель машины: файлы репозитория видит
// только он. Ответ «не знаю» ничего не меняет — иначе спящий ноутбук или
// ненастроенный языковой сервер стёрли бы всю разметку памяти.
func TestPlaceVerifiesAgentMachine(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	a := dialAgent(t, e, "мак", []string{"код"})
	waitLink(t, e, "мак")
	if err := e.AllowAgent("мак", "код", true); err != nil {
		t.Fatal(err)
	}
	// потребитель памяти — по нему Сёма знает рабочую папку
	if _, err := e.store.MemoryConsumer("mcp", "Claude Code", "/home/user/repo/sema"); err != nil {
		t.Fatal(err)
	}
	id, err := e.store.SaveMemory(&store.Memory{Kind: store.MemLesson, Area: "код", Text: "урок про значки"})
	if err != nil {
		t.Fatal(err)
	}
	place := store.Place{MemoryID: id, Repo: "server", File: "internal/web/icons.go", Symbol: "Значок"}
	if err := e.store.BindToPlace(place); err != nil {
		t.Fatal(err)
	}
	age := func() {
		if err := e.store.AgePlace(id, time.Now().AddDate(0, 0, -30)); err != nil {
			t.Fatal(err)
		}
	}

	// «не знаю» — координату не трогаем
	age()
	a.reply.Store(map[string]any{"файл": false, "есть": false, "знаем": false})
	e.verifyPlacesWithCode(context.Background())
	a.wait(t)
	if place := e.store.MemoryPlaces(id); len(place) != 1 || place[0].Doubtful {
		t.Fatalf("по ответу «не знаю» координату тронули: %+v", place)
	}

	// «символа нет» — координата становится сомнительной
	age()
	a.reply.Store(map[string]any{"файл": true, "есть": false, "знаем": true, "ярус": "lsp"})
	e.verifyPlacesWithCode(context.Background())
	a.wait(t)
	places := e.store.MemoryPlaces(id)
	if len(places) != 1 || !places[0].Doubtful {
		t.Fatalf("координата не помечена сомнительной: %+v", places)
	}
	// само знание при этом остаётся: устаревает координата, а не урок
	if _, err := e.store.MemoryByID(id); err != nil {
		t.Fatalf("запись пропала вместе с координатой: %v", err)
	}
}
