package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Пуш проходит через чужую службу и лежит в системном центре уведомлений
// телефона, где его видит кто угодно. Поэтому правило жёстче, чем у других
// каналов: содержимого писем нет никогда, а закрытый ящик и «секретное» —
// только «есть новое».
func TestContentPush(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)

	plain := &store.Notification{ID: 1, Title: "Сервер srv-bots: место кончается", Summary: "свободно 4%", Level: "warn"}
	p := e.pushPayload(plain)
	if p.Title != plain.Title || !strings.Contains(p.Body, "4%") {
		t.Fatalf("обычное уведомление урезано зря: %+v", p)
	}

	// письмо из закрытого ящика: заголовок и итог не уходят
	m := &store.Message{MessageID: "<a@b>", Account: "work@example.com",
		FromAddr: "boss@example.com", Subject: "Договор № 7", Snippet: "во вложении"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	// ящик без «отдавать облачной модели» — закрытый, как рабочий по умолчанию
	if err := st.SaveMailConf("work@example.com", &store.MailConf{}); err != nil {
		t.Fatal(err)
	}
	message := &store.Notification{ID: 2, Title: "Ответить boss@example.com", Summary: "Договор № 7", Level: "warn", Ref: "msg:" + itoa(int(m.ID))}
	p = e.pushPayload(message)
	if strings.Contains(p.Title, "boss") || strings.Contains(p.Body, "Договор") || p.Body != "" {
		t.Fatalf("содержимое закрытого ящика ушло в пуш: %+v", p)
	}
	if !strings.Contains(p.Title, "есть новое") {
		t.Fatalf("факт нового не сказан: %+v", p)
	}

	// ссылка на письмо, которого нет: не знаем — значит не показываем
	unknown := &store.Notification{ID: 3, Title: "Что-то", Summary: "с полями", Ref: "msg:999999"}
	if p := e.pushPayload(unknown); p.Body != "" || strings.Contains(p.Title, "Что-то") {
		t.Fatalf("неизвестное письмо показано: %+v", p)
	}
}

// Подписки: одна конечная точка — одна запись, переподписка не двоит.
func TestSubscriptionNotDoubled(t *testing.T) {
	_, st := newTestEngine(t, "тест", nil)
	for i := 0; i < 3; i++ {
		if err := st.SavePushSubscription(&store.PushSubscription{Endpoint: "https://push.example/abc",
			P256dh: "p", Auth: "a", Agent: "телефон"}); err != nil {
			t.Fatal(err)
		}
	}
	subs, err := st.PushSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("подписок %d, ждали 1", len(subs))
	}
	if err := st.SavePushSubscription(&store.PushSubscription{Endpoint: "https://push.example/abc"}); err == nil {
		t.Fatal("неполная подписка принята")
	}
	st.MarkPushDelivery("https://push.example/abc", "ответ 500", 500)
	subs, _ = st.PushSubscriptions()
	if subs[0].LastErr != "ответ 500" {
		t.Fatalf("исход не записан: %+v", subs[0])
	}
	if err := st.DeletePushSubscription("https://push.example/abc"); err != nil {
		t.Fatal(err)
	}
	if subs, _ := st.PushSubscriptions(); len(subs) != 0 {
		t.Fatal("подписка не удалилась")
	}
}
