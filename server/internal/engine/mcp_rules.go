package engine

// Правила серверов MCP: у каждого инструмента свой ответ — «Разрешить»,
// «Спросить» или «Запретить», как у команд на серверах. Инструменты
// перечислимы (сервер сам называет их), поэтому правило здесь — не образец
// имени, а строка «инструмент → ответ». Образцы `*_list`, `*delete*`, по
// которым круги угадывали изменяющее, больше не нужны: человек решает за
// каждое имя, а угадывание осталось только в переезде.
//
// Не подошло ни одно правило — запрещено. Инструмент, которого в правилах
// нет, «новый»: решения человека про него ещё нет, и отвечает «ответ для
// нового инструмента» сервера (по умолчанию «Запретить»). Такой инструмент
// модель не видит вовсе: в промпте чата его нет, как поиска, которого нет
// (`SearchReady`).

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// mcpRuleKey — ключ правила: имя инструмента в змеином виде. Сервер вправе
// назвать `listTasks`, `get-task` и `tasks.list` иначе, чем мы их записали, и
// правило должно найтись по любому написанию одного и того же имени.
func mcpRuleKey(tool string) string { return strings.ToLower(mcpSnake(tool)) }

// mcpRuleFor — решение человека про инструмент. false — инструмент новый.
func mcpRuleFor(m *store.MCPServer, tool string) (string, bool) {
	if p, ok := m.Rules[tool]; ok && store.ValidRuleKind(p) {
		return p, true
	}
	key := mcpRuleKey(tool)
	for name, p := range m.Rules {
		if store.ValidRuleKind(p) && mcpRuleKey(name) == key {
			return p, true
		}
	}
	return "", false
}

// MCPRuleOf — решение человека про инструмент для экрана. decided=false —
// инструмент новый.
func MCPRuleOf(m *store.MCPServer, tool string) (policy string, decided bool) {
	return mcpRuleFor(m, tool)
}

// NoteNewMCPTools — сверить уведомление «новые инструменты» с правилами:
// открыть, если есть нерешённые, и закрыть, если решено всё.
func (e *Engine) NoteNewMCPTools(m *store.MCPServer) { e.noteNewMCPTools(m) }

// mcpDefault — ответ для нового инструмента: запрещено, пока человек не
// решил иначе.
func mcpDefault(m *store.MCPServer) string {
	if store.ValidRuleKind(m.RuleDefault) {
		return m.RuleDefault
	}
	return PolicyDeny
}

// ClassifyMCP — ответ правила на вызов инструмента. Новый инструмент
// получает ответ по умолчанию, и это сказано в причине.
func ClassifyMCP(m *store.MCPServer, tool string) (policy, why string) {
	if p, ok := mcpRuleFor(m, tool); ok {
		return p, "правило сервера: «" + PolicyTitle(p) + "»"
	}
	p := mcpDefault(m)
	return p, "инструмент ещё не решён — по умолчанию «" + PolicyTitle(p) + "»: откройте «Интеграции → Правила» и решите за него"
}

// MCPToolState — инструмент сервера с ответом, для экрана и промпта.
type MCPToolState struct {
	Name   string
	Desc   string
	Policy string // ответ: решённый или по умолчанию
	New    bool   // решения человека про него ещё нет
}

// MCPToolStates — инструменты сервера в порядке, как сервер их назвал.
func MCPToolStates(m *store.MCPServer) []MCPToolState {
	desc := map[string]string{}
	for _, t := range m.ToolInfo {
		desc[t.Name] = t.Desc
	}
	names := m.Tools
	if len(names) == 0 {
		for _, t := range m.ToolInfo {
			names = append(names, t.Name)
		}
	}
	out := make([]MCPToolState, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		st := MCPToolState{Name: name, Desc: desc[name]}
		if p, ok := mcpRuleFor(m, name); ok {
			st.Policy = p
		} else {
			st.Policy, st.New = mcpDefault(m), true
		}
		out = append(out, st)
	}
	return out
}

// MCPRuleCounts — сколько инструментов с каким ответом и сколько новых.
func MCPRuleCounts(states []MCPToolState) (allow, ask, deny, fresh int) {
	for _, st := range states {
		switch st.Policy {
		case PolicyAllow:
			allow++
		case PolicyAsk:
			ask++
		default:
			deny++
		}
		if st.New {
			fresh++
		}
	}
	return
}

// visibleMCP — сервер глазами модели: только инструменты, которые ей
// позволено звать. Запрещённые — в том числе новые, пока они запрещены по
// умолчанию, — не показываются вовсе: модель не просит того, чего не видит,
// и не тратит ход на отказ. Копия, а не правка записи.
func visibleMCP(m *store.MCPServer) *store.MCPServer {
	cp := *m
	cp.Tools, cp.ToolInfo = nil, nil
	for _, name := range m.Tools {
		if p, _ := ClassifyMCP(m, name); p != PolicyDeny {
			cp.Tools = append(cp.Tools, name)
		}
	}
	for _, t := range m.ToolInfo {
		if p, _ := ClassifyMCP(m, t.Name); p != PolicyDeny {
			cp.ToolInfo = append(cp.ToolInfo, t)
		}
	}
	return &cp
}

// mcpNewTools — инструменты, которых в правилах ещё нет.
func mcpNewTools(m *store.MCPServer) []string {
	var out []string
	for _, st := range MCPToolStates(m) {
		if st.New {
			out = append(out, st.Name)
		}
	}
	return out
}

// MCPServerScreen — адрес экрана сервера MCP для уведомлений:
// `settings:integrations@имя` и вкладка через «~» («rules», «log»; пусто —
// «Обзор»). У каждого сервера свой экран, и уведомление про него ведёт туда, а
// не в общий список. Так же, как ServerScreen у серверов по SSH.
func MCPServerScreen(server, tab string) string {
	addr := "settings:integrations@" + server
	if tab != "" {
		addr += "~" + tab
	}
	return addr
}

// mcpNewNoticeKey — ключ уведомления «новые инструменты».
func mcpNewNoticeKey(server string) string { return "mcp-new:" + server }

// noteNewMCPTools — человеку сказано, что у сервера появились инструменты,
// про которые он ещё не решал. Без этого они тихо лежали бы запрещёнными, и
// причину «почему Сёма не умеет того, что умеет сервер» никто бы не нашёл.
// Решил за все — запись закрывается сама.
func (e *Engine) noteNewMCPTools(m *store.MCPServer) {
	fresh := mcpNewTools(m)
	key := mcpNewNoticeKey(m.Name)
	if len(fresh) == 0 {
		e.CloseNotification(key, store.NotifyDone, "решено по всем инструментам")
		return
	}
	def := mcpDefault(m)
	sort.Strings(fresh)
	_, _ = e.Notify(&store.Notification{
		Key: key, Source: "core", Level: "info",
		Title:   fmt.Sprintf("Сервер MCP «%s»: новых инструментов %d — решите, что им можно", m.Name, len(fresh)),
		Summary: "пока для них действует «" + PolicyTitle(def) + "»: модель их не видит",
		Fields: []store.NotifyField{
			{Label: "Сервер", Value: m.Name},
			{Label: "Новые", Value: clip(strings.Join(fresh, ", "), 600)},
			{Label: "Ответ по умолчанию", Value: PolicyTitle(def)},
		},
		Actions: []store.NotifyAction{
			{Label: "Решить", Kind: "goto", Screen: MCPServerScreen(m.Name, "rules"), Style: "primary"},
			{Label: "Позже", Kind: "dismiss"},
		},
	})
}

// legacyClassifyMCP — как прежние круги отвечали на имя инструмента: «никогда»
// сильнее «чтения»; образец сравнивался и с именем как есть, и с его
// змеиной записью. Остался только для переезда: по нему известным
// инструментам переносится ровно то поведение, что было.
func legacyClassifyMCP(read, never []string, tool string) string {
	raw := strings.ToLower(strings.TrimSpace(tool))
	forms := []string{raw}
	if snake := mcpSnake(tool); snake != raw {
		forms = append(forms, snake)
	}
	hit := func(pattern string) bool {
		p := strings.ToLower(strings.TrimSpace(pattern))
		for _, f := range forms {
			if globMatch(p, f) {
				return true
			}
		}
		return false
	}
	for _, p := range never {
		if hit(p) {
			return PolicyDeny
		}
	}
	for _, p := range read {
		if hit(p) {
			return PolicyAllow
		}
	}
	return PolicyAsk
}

// migrateMCPRules — разовый переезд кругов вызовов на правила по
// инструментам. Каждому ИЗВЕСТНОМУ инструменту сервера ответ выводится из
// прежних кругов: подошёл под «читают» — «Разрешить», под «никогда» —
// «Запретить», иначе — «Спросить». Сегодняшнее поведение не меняется.
// Запрет по умолчанию касается только новых инструментов: тех, что сервер
// назовёт после переезда.
//
// Серверы, у которых правила уже есть (`RulesSet`), не трогаются: то, что
// человек решил на экране, нельзя переписать старыми образцами. Прежние
// колонки остаются в базе нетронутыми — сборка, которую придётся откатить,
// найдёт свои круги на месте.
func (e *Engine) migrateMCPRules() {
	list, err := e.store.MCPServers()
	if err != nil {
		return
	}
	for _, m := range list {
		if m.RulesSet {
			continue
		}
		rules := map[string]string{}
		for _, st := range MCPToolStates(&store.MCPServer{Tools: m.Tools, ToolInfo: m.ToolInfo}) {
			rules[st.Name] = legacyClassifyMCP(m.LegacyRead, m.LegacyNever, st.Name)
		}
		if err := e.store.SetMCPRules(m.Name, rules, store.RuleDeny); err != nil {
			log.Printf("правила сервера MCP «%s»: %v", m.Name, err)
			continue // метки нет — повторится на следующем старте
		}
		var allow, ask, deny int
		for _, p := range rules {
			switch p {
			case PolicyAllow:
				allow++
			case PolicyAsk:
				ask++
			default:
				deny++
			}
		}
		log.Printf("правила сервера MCP «%s»: инструментов %d — разрешить %d, спросить %d, запретить %d; новые инструменты — запрещены",
			m.Name, len(rules), allow, ask, deny)
	}
}
