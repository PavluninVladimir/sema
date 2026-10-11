package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Дверь для агента закрытая: память и уведомления, и ничего больше.
// Неразрешённая способность — отказ с объяснением, а не молчание и не
// разрыв.
func TestAgentAsksOnlyMemory(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	if err := st.SaveAgent(&store.Agent{Name: "мак", Fingerprint: "aa", State: store.AgentOK,
		Can: []string{"код", CanMemory}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	params := json.RawMessage(`{"path":"/tmp/проба"}`)

	if _, err := e.handleAgentAsk(ctx, "мак", "код.symbols", params); err == nil ||
		!strings.Contains(err.Error(), "доступны память и уведомления") {
		t.Fatalf("чужая способность прошла: %v", err)
	}
	if _, err := e.handleAgentAsk(ctx, "мак", "память.memory_recall", params); err == nil ||
		!strings.Contains(err.Error(), "не разрешена") {
		t.Fatalf("память без разрешения человека прошла: %v", err)
	}

	a, _ := st.Agent("мак")
	a.Allowed = []string{CanMemory}
	if err := st.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.handleAgentAsk(ctx, "мак", "память.нет-такого", params); err == nil ||
		!strings.Contains(err.Error(), "инструмента памяти") {
		t.Fatalf("выдуманный инструмент прошёл: %v", err)
	}
	if _, err := e.handleAgentAsk(ctx, "мак", "память.memory_recall", json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "рабочая папка") {
		t.Fatalf("запрос без папки прошёл: %v", err)
	}
}

// Способность и области — два разных замка. Способность включена, а
// областей папке не выдали — из памяти не приходит ничего; выданы одной
// папке — из другой по-прежнему пусто.
func TestAreaGrantedFolder(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	if err := st.SaveAgent(&store.Agent{Name: "мак", Fingerprint: "aa", State: store.AgentOK,
		Can: []string{CanMemory}, Allowed: []string{CanMemory}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	own := e.ExternalOrigin("Claude Code", "/tmp/своя")
	if err := st.SetConsumerAreas(own.Consumer.ID, []string{"код"}); err != nil {
		t.Fatal(err)
	}
	if a, ok := st.MemoryArea("код"); ok {
		a.Outbound = true // проверяем разделение по папкам, а не правило «наружу»
		if err := st.SaveMemoryArea(a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.ExternalRemember(ctx, "Claude Code", "/tmp/своя", "код", "lesson",
		"Провод к жителю машины рвётся при выкате Сёмы.", "", "", ""); err != nil {
		t.Fatal(err)
	}

	ask := func(path string) string {
		params, _ := json.Marshal(map[string]any{"path": path, "client": "Claude Code", "model": "модель", "query": "выкате"})
		res, err := e.handleAgentAsk(ctx, "мак", "память.memory_recall", params)
		if err != nil {
			t.Fatalf("память отказала: %v", err)
		}
		m, ok := res.(map[string]any)
		if !ok {
			t.Fatalf("ответ не разобран: %#v", res)
		}
		text, _ := m["text"].(string)
		return text
	}
	if got := ask("/tmp/своя"); !strings.Contains(got, "выкате") {
		t.Fatalf("своя папка не увидела свою запись: %q", got)
	}
	if got := ask("/tmp/чужая"); strings.Contains(got, "выкате") {
		t.Fatalf("чужая папка увидела чужую память: %q", got)
	}
}

// Обрыв, возвращение, снова обрыв — уведомление одно: ключ по имени
// агента, вторая пропажа заменяет первую.
func TestDropLinkOneNotification(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	if err := st.SaveAgent(&store.Agent{Name: "мак", Fingerprint: "aa", State: store.AgentOK}); err != nil {
		t.Fatal(err)
	}
	open := func() int {
		list, err := st.Notifications(true, 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, x := range list {
			if x.Key == agentOffKey("мак") && x.State == store.NotifyOpen {
				n++
			}
		}
		return n
	}
	e.agentGone("мак", time.Minute)
	if open() != 1 {
		t.Fatalf("обрыв не дал уведомления: %d", open())
	}
	e.agentBack("мак", time.Now().Add(-2*time.Hour).Format(time.RFC3339))
	if open() != 0 {
		t.Fatalf("возвращение не закрыло уведомление: %d", open())
	}
	e.agentGone("мак", time.Minute)
	e.agentGone("мак", time.Minute)
	if open() != 1 {
		t.Fatalf("уведомления копятся: %d", open())
	}

	// у выключенного человеком агента обрыв молчит: он сам его и выключил
	a, _ := st.Agent("мак")
	a.State = store.AgentRevoked
	if err := st.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	e.CloseNotification(agentOffKey("мак"), store.NotifyDone, "проба")
	e.agentGone("мак", time.Minute)
	if open() != 0 {
		t.Fatalf("отозванный агент разбудил человека: %d", open())
	}
}

// Агент перезвонил раньше, чем умерло прежнее соединение: мак после сна
// оставляет мёртвый TCP жить минутами. Закрытие вытесненного соединения
// не должно будить человека — агент на связи.
func TestEvictedConnNotDrop(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	if err := st.SaveAgent(&store.Agent{Name: "мак", Fingerprint: "aa", State: store.AgentOK}); err != nil {
		t.Fatal(err)
	}
	open := func() int {
		list, err := st.Notifications(true, 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, x := range list {
			if x.Key == agentOffKey("мак") && x.State == store.NotifyOpen {
				n++
			}
		}
		return n
	}
	old := &link{agent: "мак", since: time.Now()}
	fresh := &link{agent: "мак", since: time.Now()}
	e.linkMu.Lock()
	e.links = map[string]*link{"мак": fresh}
	e.linkMu.Unlock()

	e.linkClosed("мак", old) // прежнее соединение догорело
	if open() != 0 {
		t.Fatal("вытеснение разбудило человека, хотя агент на связи")
	}
	e.linkClosed("мак", fresh) // а теперь ушёл и живой
	if open() != 1 {
		t.Fatal("настоящий обрыв не дал уведомления")
	}
}
