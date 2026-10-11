package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Скрипт «Письма за сутки» отвечал «писем за сутки нет», хотя за сутки
// пришло 27 писем: sema.messages без явного status берёт только inbox_ok,
// а свежее лежит под server_spam, pending и archived. Проверяем оба
// поведения — умолчание и status: "any", — и то, что описание инструмента
// об этом говорит: модель пишет код по описанию, а не по исходникам.
func TestMessagesForDayVisibleOnlyWithStatusAny(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)

	yesterday := time.Now().Add(-6 * time.Hour)
	messages := []struct {
		id     string
		status string
	}{
		{"<spam@x>", store.StatusServerSpam},
		{"<wait@x>", store.StatusPending},
		{"<arch@x>", store.StatusArchived},
	}
	for _, p := range messages {
		m := &store.Message{MessageID: p.id, Account: "тест", FromAddr: "кто@то",
			Subject: "тема", Date: yesterday, Status: p.status}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	args := map[string]any{"since": yesterday.Add(-time.Hour).Format(time.RFC3339), "limit": 200}

	// как звал скрипт: без status — и потому пусто
	res, err := e.toolMessages(args)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if got := len(res.([]map[string]any)); got != 0 {
		t.Fatalf("без status ожидались только inbox_ok, а пришло %d", got)
	}

	// как надо: status «any» показывает всё, что пришло за сутки
	args["status"] = "any"
	res, err = e.toolMessages(args)
	if err != nil {
		t.Fatalf("messages any: %v", err)
	}
	if got := len(res.([]map[string]any)); got != len(messages) {
		t.Fatalf("со status «any» ожидались %d писем, пришло %d", len(messages), got)
	}
}

// Описание инструмента обязано называть и умолчание, и способ его обойти:
// именно по нему модель пишет модули.
func TestDescriptionMessagesNamesDefault(t *testing.T) {
	e, _ := newTestEngine(t, "модель", nil)
	for _, tool := range e.builtinTools() {
		if tool.Name != "messages" {
			continue
		}
		for _, must := range []string{"inbox_ok", "any", "since"} {
			if !strings.Contains(tool.Desc+tool.Args, must) {
				t.Fatalf("в описании messages нет %q: %s %s", must, tool.Args, tool.Desc)
			}
		}
		return
	}
	t.Fatal("инструмент messages не найден")
}
