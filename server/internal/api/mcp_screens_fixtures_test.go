package api

// Данные для экранов «Интеграций»: четыре сервера MCP разного рода — по ключу,
// с входом OAuth и пятьюдесятью инструментами в группах, у агента и такой,
// что ждёт входа, — вызовы на кнопке, подсказки про формат, журнал. Их берут и
// юнит-тесты, и браузерные, и снимки для ревью.
//
// Значения ключа и токенов входа — приметные строки: тест ищет их в разметке,
// и ни одна не должна встретиться ни на одном экране раздела.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	mcpKeySentinel     = "KEY-значение-ключа-77aa"
	mcpAccessSentinel  = "AT-токен-доступа-1234bb"
	mcpRefreshSentinel = "RT-токен-продления-5678cc"
)

// mcpFiftyTools — пятьдесят инструментов, как у Planner: группы по
// приставке имени, плюс два без приставки («Прочее»).
func mcpFiftyTools() []string {
	groups := []struct {
		prefix string
		verbs  []string
	}{
		{"task", []string{"list", "get", "create", "update", "complete", "uncomplete", "cancel", "archive", "unarchive", "move", "change_column", "list_inbox", "list_overdue", "list_today"}},
		{"task_group", []string{"list", "get", "create", "update"}},
		{"project", []string{"list", "get", "create", "update", "archive"}},
		{"habit", []string{"list", "get", "create", "update"}},
		{"habit_progress", []string{"list", "create", "update"}},
		{"checklist_item", []string{"list", "get", "create", "update", "check", "uncheck"}},
		{"kanban_status", []string{"list", "get", "create", "update"}},
		{"tag", []string{"list", "get", "create", "update"}},
		{"time_stat", []string{"list", "get", "create", "update"}},
	}
	var out []string
	for _, g := range groups {
		for _, v := range g.verbs {
			out = append(out, g.prefix+"_"+v)
		}
	}
	return append(out, "get_my_context", "whoami")
}

// mcpToolInfo — описания инструментов, как их назвал сервер.
func mcpToolInfo(names []string) []store.MCPTool {
	info := make([]store.MCPTool, 0, len(names))
	for _, n := range names {
		info = append(info, store.MCPTool{Name: n, Desc: "Описание инструмента " + n + ", как его назвал сервер."})
	}
	return info
}

// mcpScreensFix — сервер с четырьмя серверами MCP:
//
//	sing  — вход OAuth выполнен, 50 инструментов, часть не решена, подсказки про формат
//	ключ  — постоянный ключ из хранилища, заголовок X-Api-Key
//	код   — сервер жителя машины: адреса нет, по каналу агентов
//	wiki  — вход OAuth нужен (настоящий mcptest-сервер: по нему можно начать вход)
func mcpScreensFix(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

	// секреты: ключ и токены входа — приметные значения
	if err := st.SaveSecret(&store.Secret{Name: "mcp-key", Kind: store.SecretToken}, mcpKeySentinel); err != nil {
		t.Fatal(err)
	}
	tokens := fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"token_endpoint":"https://auth.example/token","client_id":"sema","scope":"tasks:read tasks:write"}`,
		mcpAccessSentinel, mcpRefreshSentinel)
	if err := st.SaveSecret(&store.Secret{Name: "mcp:sing:oauth", Kind: store.SecretOAuth}, tokens); err != nil {
		t.Fatal(err)
	}

	sing := mcpFiftyTools()
	if err := st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: "https://mcp.planner-app.example/mcp", Enabled: true,
		State: "ok", Checked: ago(18 * time.Hour), Tools: sing, ToolInfo: mcpToolInfo(sing), Chat: true,
		About: "задачи, привычки и проекты", Keywords: "задача, привычка, проект", Class: store.ClassPlain,
		LoginScope: "tasks:read tasks:write"}); err != nil {
		t.Fatal(err)
	}
	rules := map[string]string{}
	for _, name := range sing {
		switch {
		case name == "tag_update" || name == "time_stat_create" || name == "time_stat_update":
			// три инструмента без решения: новые, запрещены по умолчанию
		case name == "whoami" || name == "get_my_context":
			rules[name] = store.RuleDeny
		case strings.HasSuffix(name, "_list") || strings.HasSuffix(name, "_get"):
			rules[name] = store.RuleAllow
		default:
			rules[name] = store.RuleAsk
		}
	}
	if err := st.SetMCPRules("sing", rules, store.RuleDeny); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPLogin("sing", &store.MCPLogin{At: ago(72 * time.Hour), Expires: now.Add(50 * time.Minute).Format(time.RFC3339),
		Scope: "tasks:read tasks:write", Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPToolHint("sing", "task_update", "Value must be a calendar-correct ISO-8601 datetime with timezone", now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPToolHint("sing", "task_create", "title is required", now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	keyed := []string{"page_get", "page_list", "page_update", "page_delete", "search"}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "ключ", URL: "https://wiki.example/mcp", Secret: "mcp-key", Header: "X-Api-Key",
		Enabled: true, State: "ok", Checked: ago(3 * time.Hour), Tools: keyed, ToolInfo: mcpToolInfo(keyed), Chat: true,
		About: "корпоративная вики", Class: store.ClassSensitive}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPRules("ключ", map[string]string{"page_get": store.RuleAllow, "page_list": store.RuleAllow,
		"search": store.RuleAllow, "page_update": store.RuleAsk, "page_delete": store.RuleDeny}, store.RuleDeny); err != nil {
		t.Fatal(err)
	}

	code := []string{"symbols", "context", "impact", "definition", "diagnostics", "search", "agent_status"}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "код", Agent: "мак", Can: "код", Enabled: true, State: "ok",
		Checked: ago(20 * time.Minute), Tools: code, ToolInfo: mcpToolInfo(code)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPRules("код", map[string]string{"symbols": store.RuleAllow, "context": store.RuleAllow, "impact": store.RuleAllow,
		"definition": store.RuleAllow, "search": store.RuleAllow, "agent_status": store.RuleAllow, "diagnostics": store.RuleAsk}, store.RuleDeny); err != nil {
		t.Fatal(err)
	}

	// wiki: настоящий сервер mcptest, который требует вход
	fake := mcptest.New(t, true)
	if err := st.SaveMCPServer(&store.MCPServer{Name: "wiki", URL: fake.URL(), Enabled: true, Chat: true,
		About: "вики для заметок", Class: store.ClassPlain}); err != nil {
		t.Fatal(err)
	}
	_ = s.eng.CheckMCP(t.Context(), "wiki")

	// вызовы на кнопке
	for _, p := range []*store.MCPPending{
		{Server: "sing", Tool: "task_create", Args: `{"title":"Купить фильтр для кофемашины","project":"Дом"}`, Who: "чат «Сёма · Вадим»"},
		{Server: "ключ", Tool: "page_update", Args: `{"id":"42","body":"Новая редакция"}`, Who: "модуль «Заметки»"},
	} {
		if _, err := st.AddMCPPending(p); err != nil {
			t.Fatal(err)
		}
	}
	// журнал sing: все пять исходов и страница про запас
	for _, u := range []store.MCPUse{
		{Server: "sing", Tool: "task_list_today", Who: "чат «Сёма · Вадим»", Circle: "allow", OK: true, Args: `{}`, Result: "14 задач"},
		{Server: "sing", Tool: "task_create", Who: "чат «Сёма · Вадим»", Circle: "ask", OK: false, Args: `{"title":"Купить фильтр"}`, Result: store.MCPWaitingResult},
		{Server: "sing", Tool: "task_create", Who: "чат «Сёма · Вадим»", Circle: "ask", OK: true, Args: `{"title":"Позвонить"}`, Result: "создана задача T-77"},
		{Server: "sing", Tool: "task_complete", Who: "чат «Сёма · Вадим»", Circle: "ask", OK: false, Args: `{"id":"T-5"}`, Result: store.MCPRejectedResult},
		{Server: "sing", Tool: "whoami", Who: "модуль «Заметки»", Circle: "deny", OK: false, Args: `{}`, Result: "правило сервера: «Запретить»"},
		{Server: "sing", Tool: "task_update", Who: "чат «Задача: Собери новости…»", Circle: "ask", OK: false, Args: `{"id":"T-9","deadline":"2026-10-01"}`,
			Result: "planner отклонил аргументы: Value must be a calendar-correct ISO-8601 datetime (INVALID_FIELD, HTTP 400)"},
		{Server: "код", Tool: "symbols", Who: "агент «мак»", Circle: "allow", OK: true, Args: `{"file":"main.go"}`, Result: "[ярус lsp] 12 объявлений"},
	} {
		st.LogMCP(u)
	}
	for i := 0; i < 30; i++ {
		st.LogMCP(store.MCPUse{Server: "sing", Tool: fmt.Sprintf("task_get_%d", i), Who: "чат «Сёма · Вадим»", Circle: "allow", OK: true,
			Args: fmt.Sprintf(`{"id":"T-%d"}`, i), Result: "задача"})
	}
	return s, st
}
