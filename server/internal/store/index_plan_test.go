package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Индексы под экраны «Отправители» и чат. Проверяем ровно то, ради чего
// они заведены: план запроса без полного скана таблицы — и то, ради чего
// их можно было бы завести зря: ответ не меняется ни на строку.
//
// План читается через EXPLAIN QUERY PLAN тем же соединением и по той же
// строке SQL, что уходит в базу (sqlSenders, sqlMessagesFrom,
// sqlChatMsgs), поэтому разойтись запросу и проверке негде.

// queryPlan — строки EXPLAIN QUERY PLAN одной строкой на каждую.
func queryPlan(t *testing.T, st *Store, query string, args ...any) []string {
	t.Helper()
	rows, err := st.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("план: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("разбор плана: %v", err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("план: %v", err)
	}
	return out
}

// planScansTable — есть ли в плане полный проход по названной таблице.
func planScansTable(plan []string, table string) bool {
	for _, s := range plan {
		// «SCAN messages», «SCAN m» — SQLite пишет псевдоним, если он есть,
		// поэтому смотрим и на «USING INDEX»: скан по индексу — не то же,
		// что скан таблицы, но нам и он не нужен
		if strings.HasPrefix(s, "SCAN ") && strings.Contains(s, table) {
			return true
		}
	}
	return false
}

// fillMail — правила и письма в количестве, при котором полный скан
// заметен: на живой Сёме это 204 правила на 1700 писем.
func fillMail(t *testing.T, st *Store, rules, messages int) {
	t.Helper()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < rules; i++ {
		action := "ham"
		if i%3 == 0 {
			action = "spam"
		}
		if err := st.AddRule("sender", fmt.Sprintf("отправитель%03d@пример.рф", i), action); err != nil {
			t.Fatalf("правило: %v", err)
		}
	}
	for i := 0; i < messages; i++ {
		m := &Message{
			Account:   "ящик@пример.рф",
			MessageID: fmt.Sprintf("<письмо-%d@пример.рф>", i),
			UID:       uint32(i + 1),
			FromAddr:  fmt.Sprintf("отправитель%03d@пример.рф", i%rules),
			FromName:  fmt.Sprintf("Отправитель %03d", i%rules),
			Subject:   fmt.Sprintf("Тема письма номер %d", i),
			Date:      base.Add(-time.Duration(i) * time.Minute),
			Snippet:   "Короткая выдержка из письма",
			Status:    StatusHam,
		}
		if err := st.InsertMessage(m); err != nil {
			t.Fatalf("письмо: %v", err)
		}
	}
}

func sendersPrint(sn []*Sender) string {
	var b strings.Builder
	for _, s := range sn {
		fmt.Fprintf(&b, "%s|%s|%d|%s|%s|%s\n", s.Addr, s.Action, s.Count,
			s.LastDate.UTC().Format(time.RFC3339), s.Name, s.SinceRaw)
	}
	return b.String()
}

func messagesPrint(ms []*Message) string {
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "%d|%s|%s|%s|%s\n", m.ID, m.MessageID, m.FromAddr,
			m.Date.UTC().Format(time.RFC3339), m.Subject)
	}
	return b.String()
}

// TestSendersIndexWithoutScan — «Отправители» больше не читают все
// письма на каждое правило, а ответ остаётся прежним.
func TestSendersIndexWithoutScan(t *testing.T) {
	st := testStore(t)
	fillMail(t, st, 204, 1700)

	// с индексом: план и ответ
	planWith := queryPlan(t, st, sqlSenders, "ham")
	if planScansTable(planWith, "messages") || planScansTable(planWith, " m") {
		t.Errorf("с индексом в плане остался полный скан писем: %v", planWith)
	}
	if !strings.Contains(strings.Join(planWith, "\n"), "idx_messages_from_date") {
		t.Errorf("индекс idx_messages_from_date не используется: %v", planWith)
	}
	startWith := time.Now()
	withIndex, err := st.Senders("ham")
	if err != nil {
		t.Fatalf("отправители: %v", err)
	}
	timeWith := time.Since(startWith)

	// без индекса: тот же запрос, тот же ответ, но полный скан
	if _, err := st.db.Exec(`DROP INDEX idx_messages_from_date`); err != nil {
		t.Fatalf("снять индекс: %v", err)
	}
	planWithout := queryPlan(t, st, sqlSenders, "ham")
	startWithout := time.Now()
	withoutIndex, err := st.Senders("ham")
	if err != nil {
		t.Fatalf("отправители без индекса: %v", err)
	}
	timeWithout := time.Since(startWithout)
	if _, err := st.db.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_from_date ON messages(from_addr, date DESC)`); err != nil {
		t.Fatalf("вернуть индекс: %v", err)
	}

	t.Logf("план без индекса: %v", planWithout)
	t.Logf("план с индексом:  %v", planWith)
	t.Logf("время: без индекса %v, с индексом %v", timeWithout, timeWith)

	if !planScansTable(planWithout, "messages") && !planScansTable(planWithout, " m") {
		t.Logf("без индекса скана нет — значит проверка плана ничего не доказывает: %v", planWithout)
	}
	if len(withIndex) == 0 {
		t.Fatal("отправители пусты — проверять нечего")
	}
	if a, b := sendersPrint(withIndex), sendersPrint(withoutIndex); a != b {
		t.Errorf("индекс изменил ответ «Отправителей»:\nс индексом:\n%s\nбез индекса:\n%s", a, b)
	}
}

// TestSenderHistoryIndex — MessagesFrom идёт по индексу и без
// сортировки во временном дереве, ответ не меняется.
func TestSenderHistoryIndex(t *testing.T) {
	st := testStore(t)
	fillMail(t, st, 204, 1700)
	addr := "отправитель007@пример.рф"

	planWith := queryPlan(t, st, sqlMessagesFrom, addr, 50)
	if planScansTable(planWith, "messages") {
		t.Errorf("с индексом остался полный скан писем: %v", planWith)
	}
	if strings.Contains(strings.Join(planWith, "\n"), "TEMP B-TREE FOR ORDER BY") {
		t.Errorf("сортировка всё ещё во временном дереве: %v", planWith)
	}
	withIndex, err := st.MessagesFrom(addr, nil, 50)
	if err != nil {
		t.Fatalf("история: %v", err)
	}

	if _, err := st.db.Exec(`DROP INDEX idx_messages_from_date`); err != nil {
		t.Fatalf("снять индекс: %v", err)
	}
	planWithout := queryPlan(t, st, sqlMessagesFrom, addr, 50)
	withoutIndex, err := st.MessagesFrom(addr, nil, 50)
	if err != nil {
		t.Fatalf("история без индекса: %v", err)
	}
	if _, err := st.db.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_from_date ON messages(from_addr, date DESC)`); err != nil {
		t.Fatalf("вернуть индекс: %v", err)
	}

	t.Logf("план без индекса: %v", planWithout)
	t.Logf("план с индексом:  %v", planWith)

	if len(withIndex) == 0 {
		t.Fatal("история пуста — проверять нечего")
	}
	if a, b := messagesPrint(withIndex), messagesPrint(withoutIndex); a != b {
		t.Errorf("индекс изменил историю отправителя:\nс индексом:\n%s\nбез индекса:\n%s", a, b)
	}
}

// TestChatFeedIndex — открытие чата не читает всю переписку.
func TestChatFeedIndex(t *testing.T) {
	st := testStore(t)
	chat, err := st.CreateChat("Разговор")
	if err != nil {
		t.Fatalf("чат: %v", err)
	}
	other, err := st.CreateChat("Соседний")
	if err != nil {
		t.Fatalf("чат: %v", err)
	}
	for i := 0; i < 2000; i++ {
		id := chat
		if i%2 == 0 {
			id = other
		}
		if err := st.AddChatMsg(id, "user", fmt.Sprintf("реплика %d", i)); err != nil {
			t.Fatalf("реплика: %v", err)
		}
	}

	planWith := queryPlan(t, st, sqlChatMsgs, chat, 50)
	if planScansTable(planWith, "chat_msgs") {
		t.Errorf("с индексом остался полный скан переписки: %v", planWith)
	}
	withIndex, err := st.ChatMsgs(chat, 50)
	if err != nil {
		t.Fatalf("лента: %v", err)
	}

	if _, err := st.db.Exec(`DROP INDEX idx_chat_msgs_chat`); err != nil {
		t.Fatalf("снять индекс: %v", err)
	}
	planWithout := queryPlan(t, st, sqlChatMsgs, chat, 50)
	withoutIndex, err := st.ChatMsgs(chat, 50)
	if err != nil {
		t.Fatalf("лента без индекса: %v", err)
	}
	if _, err := st.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chat_msgs_chat ON chat_msgs(chat_id, id DESC)`); err != nil {
		t.Fatalf("вернуть индекс: %v", err)
	}

	t.Logf("план без индекса: %v", planWithout)
	t.Logf("план с индексом:  %v", planWith)

	if len(withIndex) != 50 {
		t.Fatalf("в ленте %d реплик, ждали 50", len(withIndex))
	}
	var a, b strings.Builder
	for i := range withIndex {
		fmt.Fprintf(&a, "%d|%s|%s\n", withIndex[i].ID, withIndex[i].Role, withIndex[i].Content)
	}
	for i := range withoutIndex {
		fmt.Fprintf(&b, "%d|%s|%s\n", withoutIndex[i].ID, withoutIndex[i].Role, withoutIndex[i].Content)
	}
	if a.String() != b.String() {
		t.Errorf("индекс изменил ленту чата:\nс индексом:\n%s\nбез индекса:\n%s", a.String(), b.String())
	}
}
