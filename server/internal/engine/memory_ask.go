package engine

// Память по проводу: житель машины спрашивает — Сёма отвечает теми же
// инструментами, что раньше отдавала по сети. Дорога другая, память та же.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/PavluninVladimir/sema/server/internal/mcp"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

var (
	memoryNamesOnce sync.Once
	memoryToolNames map[string]bool
)

// memoryServer — те же инструменты памяти, что видел клиент MCP. Список
// один на всю Сёму: два разных списка разъехались бы в первый же месяц.
//
// Держится он на самом движке, а не рядом в пакете: движок в проверках
// поднимается не один раз, и общий на всех сервер отвечал бы из чужой
// базы — молча и правдоподобно.
func (e *Engine) memoryServer() *mcp.Server {
	memoryNamesOnce.Do(func() {
		memoryToolNames = map[string]bool{}
		for _, name := range mcp.Tools() {
			memoryToolNames[name] = true
		}
	})
	e.memMu.Lock()
	defer e.memMu.Unlock()
	if e.memSrv == nil {
		e.memSrv = mcp.New(memoryFor{e})
	}
	return e.memSrv
}

// memoryNote — имя для заметки в открытую сессию. Инструментом MCP она
// никогда не была: её шлёт хук после каждого ответа, а не модель.
const memoryNote = "note"

// memoryPrinciples — пачка принципов из CLAUDE.md от жителя машины. Тоже
// не инструмент модели: файл инструкций разбирает житель, который его
// видит, а Сёма только хранит ссылки (engine/principles.go).
const memoryPrinciples = "principles"

// memoryWho — от чьего имени спрашивают память. Житель машины держит
// провод, но за ним сидят разные агенты разработки, и области выданы
// именно им: подставить сюда имя жителя значило бы слить Claude Code и
// Codex в одного потребителя с одной сессией на двоих.
//
// Имя приходит от жителя машины, а он на этой машине свой: проверять его
// нечем и незачем — раньше клиент MCP называл себя точно так же.
func memoryWho(agent string, params json.RawMessage) string {
	var head struct {
		Client string `json:"client"`
	}
	if json.Unmarshal(params, &head) == nil {
		if c := strings.TrimSpace(head.Client); c != "" {
			return c
		}
	}
	return agent
}

// memoryAsk — вызов инструмента памяти от имени агента. Рабочая папка
// приходит в параметрах каждого запроса, а не хранится состоянием
// соединения: агент один, а проектов много, и области выданы паре
// «агент плюс папка».
func (e *Engine) memoryAsk(ctx context.Context, agent, tool string, params json.RawMessage) (any, error) {
	srv := e.memoryServer()
	if tool != memoryNote && tool != memoryPrinciples && !memoryToolNames[tool] {
		return nil, fmt.Errorf("инструмента памяти «%s» нет", tool)
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var head struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(params, &head)
	if head.Path == "" {
		return nil, fmt.Errorf("нужна рабочая папка: по ней выданы области памяти")
	}
	if tool == memoryNote {
		if err := e.SessionNote(agent, head.Path, head.Text); err != nil {
			return nil, err
		}
		return map[string]any{"text": "Заметка принята."}, nil
	}
	if tool == memoryPrinciples {
		text, err := e.AcceptPrinciples(ctx, agent, head.Path, params)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text}, nil
	}
	text, err := srv.Call(ctx, agent, tool, params)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": text}, nil
}

// memoryFor — переходник к движку: тот же, что стоял за сетевой ручкой.
type memoryFor struct{ eng *Engine }

func (m memoryFor) SessionStart(ctx context.Context, agent, path, model, hint string, resume bool) (int64, string, any) {
	return m.eng.SessionStart(ctx, agent, path, model, hint, resume)
}

func (m memoryFor) SessionEnd(ctx context.Context, agent, path string, sessionID int64, summary string, observations []string) error {
	return m.eng.SessionEnd(ctx, agent, path, sessionID, summary, observations)
}

func (m memoryFor) Recall(ctx context.Context, agent, path, model, query string) (string, []string) {
	r := m.eng.ExternalRecall(ctx, agent, path, model, query)
	return r.Text, r.Closed
}

func (m memoryFor) Remember(ctx context.Context, agent, path, area, kind, text, file, symbol, source string) (string, error) {
	rec, err := m.eng.ExternalRemember(ctx, agent, path, area, kind, text, file, symbol, source)
	if err != nil {
		return "", err
	}
	if file != "" || symbol != "" {
		where := symbol
		if where == "" {
			where = file
		}
		return fmt.Sprintf("Записано (%s, область %s, id %d) и привязано к %s: вернётся тому, кто придёт править это место.",
			rec.Kind, rec.Area, rec.ID, where), nil
	}
	if rec.Status == store.MemPending {
		return "Принято, ждёт подтверждения человека: область в режиме «спросить».", nil
	}
	return fmt.Sprintf("Записано (%s, область %s, id %d).", rec.Kind, rec.Area, rec.ID), nil
}

func (m memoryFor) CodeKnowledge(ctx context.Context, agent, path, file, symbol string) string {
	return m.eng.CodeKnowledge(ctx, agent, path, file, symbol)
}

func (m memoryFor) KnowledgeGranted(agent, path string) bool {
	o := m.eng.ExternalOrigin(agent, path)
	return o.Consumer != nil && len(o.Consumer.Areas) > 0
}

func (m memoryFor) CodeKnowledgePlaces(ctx context.Context, agent, path, repo string, places []mcp.PlaceArg) string {
	qs := make([]store.PlaceQuery, 0, len(places))
	for _, p := range places {
		qs = append(qs, store.PlaceQuery{Dir: p.Dir, File: p.File, Symbol: p.Symbol})
	}
	return m.eng.AttachToPlaces(ctx, agent, path, repo, qs)
}
