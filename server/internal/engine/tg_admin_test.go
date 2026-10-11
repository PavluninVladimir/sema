package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ответ в Telegram — текст Сёмы, а не карточка команды, которая ложится
// после него; и не прошлый ответ, если на этот вопрос был только скрипт.
// Порядок тот же, что отдаёт ChatMsgs: от старых к новым, свежие в конце.
func TestTextReplyForTelegram(t *testing.T) {
	msgs := []*store.ChatMsg{
		{Role: "user", Content: "Старый вопрос"},
		{Role: "assistant", Content: "Старый ответ."},
		{Role: "user", Content: "Скажи температуру"},
		{Role: "assistant", Content: "Sema: На сервере датчика нет."},
		{Role: "tool", Content: `{"title":"Команда"}`},
	}
	if got := lastAnswerText(msgs); got != "На сервере датчика нет." {
		t.Fatalf("ответ: %q", got)
	}
	// после ответа могут лечь и заметка, и несколько карточек
	withNotes := append(append([]*store.ChatMsg{}, msgs...), &store.ChatMsg{Role: "note", Content: "заметка"}, &store.ChatMsg{Role: "tool", Content: "{}"})
	if got := lastAnswerText(withNotes); got != "На сервере датчика нет." {
		t.Fatalf("ответ за карточками и заметками: %q", got)
	}
	// на этот вопрос ответа текстом не было, только карточка: прошлый ответ
	// за новый принимать нельзя
	only := []*store.ChatMsg{
		{Role: "user", Content: "Старый вопрос"},
		{Role: "assistant", Content: "старый ответ"},
		{Role: "user", Content: "вопрос"},
		{Role: "tool", Content: "{}"},
	}
	if got := lastAnswerText(only); got != "" {
		t.Fatalf("прошлый ответ принят за новый: %q", got)
	}
	// последней стоит реплика человека — ответа ещё нет
	asked := append(append([]*store.ChatMsg{}, msgs[:2]...), &store.ChatMsg{Role: "user", Content: "новый вопрос"})
	if got := lastAnswerText(asked); got != "" {
		t.Fatalf("ответ на прежний вопрос принят за новый: %q", got)
	}
	if got := lastAnswerText(nil); got != "" {
		t.Fatalf("пустая переписка дала ответ: %q", got)
	}
}

// То же через настоящее хранилище: ChatMsgs берёт последние реплики и
// разворачивает их в хронологический порядок, и lastAnswerText обязан
// увидеть свежий ответ, а не самую старую из выбранных реплик. Реплик
// кладём больше двенадцати, чтобы выборка резала историю, как в работе.
func TestLastAnswerFromStore(t *testing.T) {
	_, st := newTestEngine(t, "", nil)
	chat, err := st.CreateChat("Админ")
	if err != nil {
		t.Fatal(err)
	}
	add := func(role, text string) {
		t.Helper()
		if err := st.AddChatMsg(chat, role, text); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		add("user", "вопрос "+strings.Repeat("я", i))
		add("assistant", "ответ "+strings.Repeat("я", i))
	}
	add("user", "Скажи температуру")
	add("assistant", "Sema: На сервере датчика нет.")
	add("tool", `{"title":"Команда"}`)

	msgs, err := st.ChatMsgs(chat, 12)
	if err != nil {
		t.Fatal(err)
	}
	if got := lastAnswerText(msgs); got != "На сервере датчика нет." {
		t.Fatalf("ответ из хранилища: %q", got)
	}

	// новый вопрос, на который текстом не ответили: старый ответ не годится
	add("user", "А влажность?")
	add("tool", "{}")
	msgs, err = st.ChatMsgs(chat, 12)
	if err != nil {
		t.Fatal(err)
	}
	if got := lastAnswerText(msgs); got != "" {
		t.Fatalf("прошлый ответ принят за новый: %q", got)
	}
}

// В админ-чате право «Отвечать» не нужно: ответ хозяину разрешён самим
// правом «Админ», иначе Сёма отвечает в веб-чате, а в Telegram молчит.
func TestAdminChatWritesWithoutGrantsReply(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	createTGAccounts(t, st)
	if err := st.SaveTGChat(&store.TGChat{Account: testTGAccount, ID: 5, Title: "Вадим", Kind: "user", Admin: true}); err != nil {
		t.Fatal(err)
	}
	// tgSendChecked не должен упереться в право «Отвечать»: проверяем до
	// клиента — клиента в тесте нет, значит ошибка будет про клиента, а не
	// про право
	err := e.tgSendChecked(context.Background(), testTGAccount, 5, 0, "ответ", false)
	if err != nil && strings.Contains(err.Error(), "писать не разрешено") {
		t.Fatalf("админ-чат упёрся в право «Отвечать»: %v", err)
	}
}
