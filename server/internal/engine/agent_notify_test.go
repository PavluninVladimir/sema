package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Житель машины добирает пропущенное сам: лента отдаёт записи после
// названного номера, помечает их доставленными и не отдаёт повторно.
func TestFeedNotificationsForAgentMachine(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	// тихие часы канала по умолчанию 23:00–8:00: без этого проверка
	// проходила днём и падала ночью — тест не должен зависеть от часов
	removeQuietHours(t, st)
	if _, err := e.Notify(&store.Notification{Key: "проба:1", Source: "core", Level: "warn",
		Title: "Сервер", Summary: "обновлений 68"}); err != nil {
		t.Fatal(err)
	}
	cards := e.NotifyFeedFor(0, 10)
	if len(cards) != 1 || cards[0].Title != "Сервер" || cards[0].Screen != "notifications" {
		t.Fatalf("лента не та: %+v", cards)
	}
	n, _ := st.Notification(cards[0].ID)
	if !strings.Contains(n.Delivered, "mac") {
		t.Fatalf("выданное не помечено доставленным: %q", n.Delivered)
	}
	if again := e.NotifyFeedFor(cards[0].ID, 10); len(again) != 0 {
		t.Fatalf("та же запись пришла дважды: %+v", again)
	}
}

// removeQuietHours — канал «Mac» без тихих часов: проверяем ленту, а не
// расписание.
func removeQuietHours(t *testing.T, st *store.Store) {
	t.Helper()
	chans, err := st.NotifyChannels()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chans {
		if c.Kind != "mac" {
			continue
		}
		c.QuietFrom, c.QuietTo = "", ""
		if err := st.SaveNotifyChannel(c); err != nil {
			t.Fatal(err)
		}
	}
}

// Тихие часы канала действуют и на ленту: ночью житель машины ничего не
// получает, и это не ошибка связи.
func TestFeedSilentInQuietHours(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	chans, _ := st.NotifyChannels()
	for _, c := range chans {
		if c.Kind == "mac" {
			c.QuietFrom, c.QuietTo = "00:00", "23:59"
			if err := st.SaveNotifyChannel(c); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := e.Notify(&store.Notification{Key: "тихо:1", Source: "core", Level: "alert",
		Title: "Сервер", Summary: "молчим"}); err != nil {
		t.Fatal(err)
	}
	if cards := e.NotifyFeedFor(0, 10); len(cards) != 0 {
		t.Fatalf("в тихие часы лента отдала записи: %+v", cards)
	}
}

// Ответ Сёмы на вопросы жителя машины: только память и уведомления, и
// только по разрешённой способности.
func TestSemaRepliesOnlyByAllowed(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveAgent(&store.Agent{Name: "мак", State: store.AgentOK,
		Can: []string{CanNotify}, Allowed: nil}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := e.handleAgentAsk(ctx, "мак", "уведомления.лента", json.RawMessage(`{}`)); err == nil {
		t.Fatal("невыданная способность ответила")
	}
	a, _ := st.Agent("мак")
	a.Allowed = []string{CanNotify}
	if err := st.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	res, err := e.handleAgentAsk(ctx, "мак", "уведомления.лента", json.RawMessage(`{"после":0}`))
	if err != nil {
		t.Fatalf("лента не ответила: %v", err)
	}
	if _, ok := res.(map[string]any)["ждут"]; !ok {
		t.Fatalf("в ответе нет счётчика: %+v", res)
	}
	if _, err := e.handleAgentAsk(ctx, "мак", "уведомления.чтотоещё", json.RawMessage(`{}`)); err == nil {
		t.Fatal("неизвестный вызов не отклонён")
	}
	if _, err := e.handleAgentAsk(ctx, "мак", "файлы.дай", json.RawMessage(`{}`)); err == nil {
		t.Fatal("посторонняя способность ответила")
	}
}

// Некому показывать — канал говорит об этом человеку, а не молчит.
func TestChannelMacExplainsSilence(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	note := e.MacChannelNote()
	if !strings.Contains(note, "некому") {
		t.Fatalf("канал молчит без объяснения: %q", note)
	}
}
