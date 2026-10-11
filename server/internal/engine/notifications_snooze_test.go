package engine

import (
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Отложенное не висит в счётчике и возвращается по сроку: иначе кнопка
// «Отложить» была бы вежливым синонимом «Прочитано».
func TestPostponedReturnsByDue(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	id, err := e.Notify(&store.Notification{Key: "проба:1", Source: "core", Level: "alert",
		Title: "Обновить сервер", Summary: "вышло обновление"})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.NotifyCounter(); n != 1 {
		t.Fatalf("открытая запись должна считаться: %d", n)
	}
	when, err := e.SnoozeNotify(id, "завтра")
	if err != nil {
		t.Fatal(err)
	}
	if !when.After(time.Now()) {
		t.Fatalf("отложено в прошлое: %s", when)
	}
	if n := e.NotifyCounter(); n != 0 {
		t.Errorf("отложенное не должно висеть в счётчике: %d", n)
	}
	got, ok := st.Notification(id)
	if !ok || got.State != store.NotifyOpen {
		t.Fatalf("запись должна остаться открытой: %+v", got)
	}
	if got.Level == "alert" {
		t.Error("тревогу надо опустить, иначе «Сроки» её не поднимут обратно")
	}
	if got.Summary != "вышло обновление" {
		t.Errorf("итог переписывать нельзя: %q", got.Summary)
	}

	// срок наступил — задача «Сроки» поднимает обратно
	if err := st.SnoozeNotification(id, time.Now().Add(-time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	e.notifyDuePass(t.Context())
	got, _ = st.Notification(id)
	if got.Level != "alert" {
		t.Errorf("по сроку запись должна вернуться тревогой, а не %q", got.Level)
	}
	if n := e.NotifyCounter(); n != 1 {
		t.Errorf("вернувшееся снова считается: %d", n)
	}
}

// Срок в прошлом — отказ, а не тихое «отложил на вчера».
func TestPostponeInPastCannot(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	id, _ := e.Notify(&store.Notification{Key: "проба:2", Source: "core", Level: "info", Title: "Что-то"})
	if _, err := e.SnoozeNotify(id, "вчера"); err == nil {
		t.Error("отложить в прошлое должно быть отказом")
	}
}
