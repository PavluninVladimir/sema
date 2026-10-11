package engine

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вопрос к Сёме из ленты собеседника уходит в тред «Сёма», а не в ленту
// и не собеседнику: в ленте только то, что собеседник видел.
func TestWordCallLeavesInThread(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	c := &store.TGChat{ID: 5, Title: "Вадим", Kind: "user", Collect: true}
	if err := st.SaveTGChat(c); err != nil {
		t.Fatal(err)
	}
	lane, err := e.tgChatFor(c)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddChatMsg(lane, "peer", "посмотри чек")

	landed, err := e.SendMessageTo(lane, "Сёма, что в чеке?")
	if err != nil {
		t.Fatal(err)
	}
	if landed == lane {
		t.Fatal("вопрос Сёме остался в ленте собеседника")
	}
	thread, err := st.Chat(landed)
	if err != nil || thread.Kind != store.ChatKindSema || thread.Parent != lane {
		t.Fatalf("ветка не та: %+v", thread)
	}
	laneMsgs, _ := st.ChatMsgs(lane, 10)
	for _, m := range laneMsgs {
		if m.Role == "user" {
			t.Fatalf("в ленте собеседника появилась реплика для Сёмы: %q", m.Content)
		}
	}
	threadMsgs, _ := st.ChatMsgs(landed, 10)
	if len(threadMsgs) == 0 || threadMsgs[0].Role != "user" {
		t.Fatalf("вопрос не в треде: %+v", threadMsgs)
	}
	// второй вызов — тот же тред, не новый
	again, _ := e.SendMessageTo(lane, "Сёма, а дата?")
	if again != landed {
		t.Fatalf("на каждый вопрос новый тред: %d и %d", landed, again)
	}
	// карточка, адресованная ленте, тоже уходит в тред
	if err := e.writeToolCard(lane, ToolCard{Title: "Проба"}); err != nil {
		t.Fatal(err)
	}
	laneMsgs, _ = st.ChatMsgs(lane, 10)
	for _, m := range laneMsgs {
		if m.Role == "tool" {
			t.Fatal("карточка легла в ленту собеседника")
		}
	}
	// в самом треде Сёма знает, с кем разговор, а поле не пишет собеседнику
	if _, ok := e.tgPeerForContext(landed); !ok {
		t.Fatal("тред не знает собеседника родителя")
	}
	if _, ok := e.tgPeerOfChat(landed); ok {
		t.Fatal("тред считается прямой перепиской: написанное ушло бы собеседнику")
	}
}

// Тред по сообщению: один якорь — один тред, Сёма видит якорь и хвост
// родителя, подписанные как родитель.
func TestThreadByMessage(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	chat, _ := st.CreateChat("Про отпуск")
	_ = st.AddChatMsg(chat, "user", "поедем в сентябре")
	_ = st.AddChatMsg(chat, "assistant", "хорошо")
	msgs, _ := st.ChatMsgs(chat, 10)
	anchor := msgs[0].ID

	th, err := e.ThreadFromMsg(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if th2, _ := e.ThreadFromMsg(anchor); th2 != th {
		t.Fatalf("второй тред по тому же сообщению: %d и %d", th, th2)
	}
	c, _ := st.Chat(th)
	if c.Kind != store.ChatKindMsg || c.Anchor != anchor || c.Parent != chat {
		t.Fatalf("тред не тот: %+v", c)
	}
	ctx := e.threadContext(th)
	if !strings.Contains(ctx, "поедем в сентябре") || !strings.Contains(ctx, "Anchor message") {
		t.Fatalf("в контексте нет якоря:\n%s", ctx)
	}
	if !strings.Contains(ctx, "parent chat") {
		t.Fatalf("хвост родителя не подписан:\n%s", ctx)
	}
	// из треда — к тому же разговору, глубже не растёт
	tmsgs, _ := st.ChatMsgs(th, 5)
	deeper, _ := e.ThreadFromMsg(tmsgs[0].ID)
	d, _ := st.Chat(deeper)
	if d.Parent != chat {
		t.Fatalf("ветка от ветки ушла вглубь: родитель %d", d.Parent)
	}
	// удаление родителя уносит ветки
	if err := e.DeleteChatTree(chat); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Chat(th); err == nil {
		t.Fatal("ветка пережила удаление родителя")
	}
	_ = context.Background()
}

// Слова и дела Сёмы в чат идут через chatForOutput: новое место записи,
// которое кладёт ответ, карточку или заметку по номеру чата напрямую,
// однажды напишет в ленту собеседника. Реплики собеседника (peer) и
// ушедшее ему (sent, user) — ленте по праву.
func TestNoWriteBypassesThread(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`AddChatMsg\(([^,]+), "(assistant|note|tool)"`)
	var bad []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, _ := os.ReadFile(f)
		for i, line := range strings.Split(string(raw), "\n") {
			m := re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if strings.Contains(m[1], "chatForOutput(") || strings.Contains(line, "// в тред уже выбран") {
				continue
			}
			bad = append(bad, f+":"+itoaInt(i+1)+": "+strings.TrimSpace(line))
		}
	}
	if len(bad) > 0 {
		t.Fatalf("запись в чат мимо chatForOutput (или пометьте `// в тред уже выбран`):\n%s", strings.Join(bad, "\n"))
	}
}

func itoaInt(n int) string { return strconv.Itoa(n) }
