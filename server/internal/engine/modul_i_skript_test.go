package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/llm"
)

// nextModel — отвечает по очереди: первый ход, второй ход.
type nextModel struct {
	mu      sync.Mutex
	replies []string
	calls   int
}

func (m *nextModel) Name() string { return "тестовая" }

func (m *nextModel) Complete(ctx context.Context, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reply := m.replies[len(m.replies)-1]
	if m.calls < len(m.replies) {
		reply = m.replies[m.calls]
	}
	m.calls++
	return reply, nil
}

var _ llm.Provider = (*nextModel)(nil)

// Модуль и скрипт в одном ответе: модуль ставится, и скрипт рядом с ним
// тоже выполняется, а не остаётся сырым кодом в переписке. 17 сентября
// 2026 Сёма дважды присылал обновление модуля вместе со скриптом превью
// статьи — модуль обновлялся, превью не запускалось вовсе.
func TestScriptNearbyWithModuleRuns(t *testing.T) {
	model := &nextModel{replies: []string{
		"Обновил модуль и запускаю превью.\n\n" +
			"```sema-module:проба-превью\n```json\n{\"name\": \"проба-превью\", \"title\": \"Проба превью\"}\n```\n```javascript\nfunction run() { return 1; }\n```\n```\n\n" +
			"```sema:Превью статьи\nreturn 'текст статьи';\n```\n\nКак вернутся результаты — покажу.",
		"Превью готово: текст статьи.",
	}}
	e, st := newTestEngine(t, "модель", model)
	chatID, err := st.CreateChat("проба")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.ChatReply(context.Background(), chatID, "обнови модуль и покажи превью"); err != nil {
		t.Fatal(err)
	}

	if _, ok := e.Module("проба-превью"); !ok {
		t.Fatal("модуль не поставлен")
	}
	msgs, _ := st.ChatMsgs(chatID, 20)
	script, reply := false, ""
	for _, m := range msgs {
		var card ToolCard
		if m.Role == "tool" && json.Unmarshal([]byte(m.Content), &card) == nil && card.Title == "Превью статьи" {
			script = card.Result == "текст статьи"
		}
		if m.Role == "assistant" {
			reply = m.Content
		}
	}
	if !script {
		t.Fatal("скрипт рядом с модулем не выполнился")
	}
	if strings.Contains(reply, "```sema:") || !strings.Contains(reply, "Превью готово") {
		t.Errorf("второго хода по результату скрипта нет или код остался в ответе: %q", reply)
	}
}
