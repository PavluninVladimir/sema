// Package mcp — инструменты памяти Сёмы для внешних агентов разработки.
//
// Сети здесь больше нет. Раньше пакет был сервером Model Context Protocol
// по HTTP, и агент разработки ходил к Сёме токеном в заголовке; Сёма
// живёт в контейнере и пересоздаётся при каждом выкате, так что сессия
// умирала вместе с ним, а токен лежал открытой строкой в настройках
// Claude Code. Теперь дверь одна — житель машины: он держит опознанный
// провод, а здесь остались описания инструментов и вызовы в движок.
package mcp

import (
	"context"
	"encoding/json"
	"strings"
)

// Memory — то, что серверу нужно от движка.
type Memory interface {
	SessionStart(ctx context.Context, agent, path, model, hint string, resume bool) (int64, string, any)
	SessionEnd(ctx context.Context, agent, path string, sessionID int64, summary string, observations []string) error
	Recall(ctx context.Context, agent, path, model, query string) (text string, closed []string)
	Remember(ctx context.Context, agent, path, area, kind, text, file, symbol, source string) (string, error)
	CodeKnowledge(ctx context.Context, agent, path, file, symbol string) string
	// CodeKnowledgePlaces — то же по списку мест карточки одним ответом:
	// папка, файл, символы; repo — имя репозитория от git-корня
	CodeKnowledgePlaces(ctx context.Context, agent, path, repo string, places []PlaceArg) string
	// KnowledgeGranted — выданы ли этому агенту в этой папке области памяти:
	// «записей нет» и «вам не выдано» — разные ответы, и карточка должна
	// назвать, где выдать
	KnowledgeGranted(agent, path string) bool
}

type Server struct{ mem Memory }

func New(mem Memory) *Server { return &Server{mem: mem} }

// tools — описание инструментов для tools/list.
var tools = []map[string]any{
	{
		"name":        "memory_session_start",
		"description": "Начать сессию в памяти Сёмы и получить контекст: уроки, факты и сводку прошлой сессии для этой папки.",
		"inputSchema": schema(map[string]string{"path": "рабочая папка агента", "model": "модель агента (для правил «наружу»)", "hint": "о чём сессия, если известно"}, "path"),
	},
	{
		"name":        "memory_session_end",
		"description": "Закончить сессию: передать сводку (что делали, что выяснили, какие решения) и наблюдения. Сёма сделает из них сводку сессии, факты и граф.",
		"inputSchema": schema(map[string]string{"path": "рабочая папка агента", "session_id": "id сессии из session_start (необязательно)", "summary": "сводка сессии, 5–20 строк", "observations": "список коротких наблюдений (необязательно)"}, "path", "summary"),
	},
	{
		"name":        "memory_recall",
		"description": "Вспомнить из памяти Сёмы по вопросу: уроки, факты, сводки и связи из выданных областей.",
		"inputSchema": schema(map[string]string{"path": "рабочая папка агента", "query": "вопрос или тема", "model": "модель агента"}, "path", "query"),
	},
	{
		"name": "code_knowledge",
		"description": "Что мы знаем про это место в коде: грабли, уроки прошлых работ, требования и разговоры, привязанные к файлу или символу. " +
			"Спрашивать ПЕРЕД правкой чужого места: агент разработки видит, что в коде сейчас, а это — что здесь уже ломалось.",
		"inputSchema": schema(map[string]string{"path": "рабочая папка агента", "file": "путь к файлу от корня репозитория", "symbol": "имя функции или типа (необязательно)"}, "path"),
	},
	{
		"name": "memory_remember",
		"description": "Записать в память Сёмы факт или урок. Повтор известного закрепляет урок. Не класть секреты. " +
			"Если урок про конкретное место в коде — назовите файл и символ: тогда он вернётся тому, кто придёт править это место (code_knowledge).",
		"inputSchema": schema(map[string]string{"path": "рабочая папка агента", "area": "область памяти (пусто — первая выданная)", "kind": "fact | lesson | observation", "text": "одно предложение по-русски", "file": "файл, к которому относится урок (необязательно)", "symbol": "функция или тип (необязательно)", "source": "откуда знание: требование спеки, грабли, разговор (необязательно)"}, "path", "text"),
	},
}

func schema(props map[string]string, required ...string) map[string]any {
	p := map[string]any{}
	for name, desc := range props {
		typ := "string"
		if name == "observations" {
			p[name] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
			continue
		}
		if name == "session_id" {
			typ = "integer"
		}
		p[name] = map[string]any{"type": typ, "description": desc}
	}
	return map[string]any{"type": "object", "properties": p, "required": required}
}

// PlaceArg — место карточки в запросе code_knowledge: папка, файл или
// символ в файле. Тот же вид, что у store.PlaceQuery, но без зависимости
// от хранилища: это договор провода.
type PlaceArg struct {
	Dir    string `json:"dir,omitempty"`
	File   string `json:"file,omitempty"`
	Symbol string `json:"symbol,omitempty"`
}

type callArgs struct {
	Path         string     `json:"path"`
	Repo         string     `json:"repo"`
	Places       []PlaceArg `json:"places"`
	Model        string     `json:"model"`
	Hint         string     `json:"hint"`
	Resume       bool       `json:"resume"`
	SessionID    int64      `json:"session_id"`
	Summary      string     `json:"summary"`
	Observations []string   `json:"observations"`
	Query        string     `json:"query"`
	Area         string     `json:"area"`
	Kind         string     `json:"kind"`
	Text         string     `json:"text"`
	File         string     `json:"file"`
	Symbol       string     `json:"symbol"`
	Source       string     `json:"source"`
}

// Call — выполнить инструмент памяти. Зовётся и разбором запроса MCP, и
// приводом от жителя машины: инструменты одни и те же, дороги разные.
func (s *Server) Call(ctx context.Context, agent, tool string, raw json.RawMessage) (string, error) {
	return s.call(ctx, agent, tool, raw)
}

// Tools — имена инструментов памяти: по ним житель машины строит свой
// список, чтобы он не разъезжался с тем, что Сёма умеет.
func Tools() []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if name, ok := t["name"].(string); ok {
			out = append(out, name)
		}
	}
	return out
}

func (s *Server) call(ctx context.Context, agent, tool string, raw json.RawMessage) (string, error) {
	var a callArgs
	_ = json.Unmarshal(raw, &a)
	switch tool {
	case "memory_session_start":
		id, text, _ := s.mem.SessionStart(ctx, agent, a.Path, a.Model, a.Hint, a.Resume)
		if text == "" {
			text = "Память по этой папке пока пуста."
		}
		return "session_id: " + itoa(id) + "\n" + text, nil
	case "memory_session_end":
		if err := s.mem.SessionEnd(ctx, agent, a.Path, a.SessionID, a.Summary, a.Observations); err != nil {
			return "", err
		}
		return "Сессия закрыта: сводка принята, кристалл и факты сделает сведение памяти.", nil
	case "memory_recall":
		text, closed := s.mem.Recall(ctx, agent, a.Path, a.Model, a.Query)
		if text == "" {
			text = "В памяти ничего подходящего (или области не выданы)."
			if len(closed) > 0 {
				text = "Области " + strings.Join(closed, ", ") + " закрыты для облачной модели: откройте им «наружу» на экране «Память»."
			}
		}
		return text, nil
	case "code_knowledge":
		if a.File == "" && a.Symbol == "" && len(a.Places) == 0 {
			return "", &toolError{"нужен файл или символ: без места искать нечего"}
		}
		var text string
		if len(a.Places) > 0 {
			// карточка называет все свои места разом: один запрос, а не сорок
			text = s.mem.CodeKnowledgePlaces(ctx, agent, a.Path, a.Repo, a.Places)
		} else {
			text = s.mem.CodeKnowledge(ctx, agent, a.Path, a.File, a.Symbol)
		}
		if text == "" && !s.mem.KnowledgeGranted(agent, a.Path) {
			return "Области памяти этому агенту в этой папке не выданы: выдать можно на экране «Память» (memory).", nil
		}
		if text == "" {
			// молчание тут опаснее всего: агент решит, что знания нет, а
			// оно может быть просто не привязано
			return "Про это место записей нет. Это не значит, что тут безопасно: знание могло не получить привязки — спросите memory_recall по теме.", nil
		}
		return text, nil
	case "memory_remember":
		return s.mem.Remember(ctx, agent, a.Path, a.Area, a.Kind, a.Text, a.File, a.Symbol, a.Source)
	default:
		return "", &toolError{"инструмента нет: " + tool}
	}
}

type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

func itoa(n int64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}
