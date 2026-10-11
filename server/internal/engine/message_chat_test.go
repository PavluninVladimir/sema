package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// waitSilence — «Разобраться» продолжает разговор в отдельной горутине
// (continueChat), и без ожидания она доживает до конца теста и пишет в
// уже закрытую базу, а заодно роняет уборку временного каталога.
func waitSilence(t *testing.T, e *Engine) {
	t.Helper()
	to := time.Now().Add(5 * time.Second)
	for time.Now().Before(to) {
		e.thinkingMu.Lock()
		thinking := len(e.thinking)
		for _, goes := range e.thinking {
			if !goes {
				thinking--
			}
		}
		e.thinkingMu.Unlock()
		if thinking == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("разговор не затих за пять секунд")
}

func messageForChat(t *testing.T, st *store.Store, subject, from string) *store.Message {
	t.Helper()
	m := &store.Message{Account: "я@пример.рф", MessageID: subject + "@пример", FromAddr: from,
		Subject: subject, Date: time.Now(), Snippet: "текст письма", Status: store.StatusInboxOK}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Разговор о письме один: второе нажатие открывает тот же чат, а не
// плодит «Письмо · …» на каждое любопытство.
func TestConversationAboutMessageOne(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := messageForChat(t, st, "Счёт за август", "bot@delivery.example")
	if id := e.MessageChatID(m.ID); id != 0 {
		t.Fatalf("до нажатия чата быть не должно: %d", id)
	}
	id, err := e.MessageChat(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.MessageChat(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Errorf("второе нажатие завело новый чат: %d вместо %d", again, id)
	}
	waitSilence(t, e)
	msgs, err := st.ChatMsgs(id, 10)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("вводной нет: %v %d", err, len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "Счёт за август") {
		t.Errorf("тема обычного письма должна быть во вводной: %q", msgs[0].Content)
	}
	if ref := e.chatMsgRefs(id); !strings.Contains(ref, "Счёт за август") {
		t.Errorf("тема должна уйти подсказкой в поиск контекста: %q", ref)
	}
}

// У секретного письма тема не попадает в реплику: реплики идут модели
// как есть, а класс проверяет дверь позже — здесь его ещё никто не
// проверял.
func TestSubjectSecretMessagesNotInReply(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := messageForChat(t, st, "Паспорт: серия и номер", "kadry@пример.рф")
	if err := st.SetContentClass(store.ClassMsg, m.ID, store.ClassSecret, "Вадим", ""); err != nil {
		t.Fatal(err)
	}
	id, err := e.MessageChat(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitSilence(t, e)
	msgs, _ := st.ChatMsgs(id, 10)
	if len(msgs) == 0 {
		t.Fatal("вводной нет")
	}
	if strings.Contains(msgs[0].Content, "Паспорт") {
		t.Errorf("тема секретного письма ушла в реплику: %q", msgs[0].Content)
	}
}
