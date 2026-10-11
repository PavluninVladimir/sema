package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Отметка о сроке живёт в настройках отдельно от самого чата, и раньше
// пережившая чат отметка застревала навсегда: сводке взяться неоткуда,
// значит чат «не удаляется» — а его уже нет. То же с пустым чатом.
func TestPurgeExpiredChats(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	expired := time.Now().Add(-time.Hour).Format(time.RFC3339)

	// чата нет вовсе — осталась одна отметка
	if err := st.SetSetting("chat.expires.9999", expired); err != nil {
		t.Fatal(err)
	}
	// чат есть, но в нём ни одной реплики
	empty, err := st.CreateChat("Уведомление · пусто")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddChatMsg(empty, "note", "служебная заметка"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(fmt.Sprintf("chat.expires.%d", empty), expired); err != nil {
		t.Fatal(err)
	}
	// чат с разговором — его терять нельзя
	live, err := st.CreateChat("Уведомление · разговор")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddChatMsg(live, "user", "что там с диском"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(fmt.Sprintf("chat.expires.%d", live), expired); err != nil {
		t.Fatal(err)
	}

	e.purgeExpiredChats(time.Now())

	if v := st.Setting("chat.expires.9999", ""); v != "" {
		t.Fatalf("отметка без чата осталась: %q", v)
	}
	if _, err := st.Chat(empty); err == nil {
		t.Fatal("пустой чат не удалился — он застрянет навсегда")
	}
	// разговор уходит только вместе со сводкой: чат удалён, но след остался
	if _, err := st.Chat(live); err == nil {
		t.Fatal("чат со сводкой должен был уйти")
	}
	sessions, err := st.MemorySessions(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sessions {
		if strings.Contains(s.Summary, "что там с диском") {
			found = true
		}
	}
	if !found {
		t.Fatal("разговор пропал: сводки в памяти нет")
	}
}
