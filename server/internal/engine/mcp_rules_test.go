package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Правила серверов MCP по инструментам: «Запретить» скрывает инструмент от
// модели и отклоняет вызов, новый инструмент запрещён и помечен новым, пока
// человек не решил.

const callArchive = "Архивирую.\n```sema-mcp:planner\n{\"tool\": \"task_archive\", \"args\": {\"id\": \"t1\"}}\n```"

// Инструмент с «Запретить» модель не видит: его нет ни в описи, ни в полном
// списке сигнатур. «Спросить» и «Разрешить» остаются.
func TestMCPDeniedToolNotInPrompt(t *testing.T) {
	f := newMCPChatFix(t, true, "ок")
	decideMCP(t, f.st, "planner", map[string]string{"task_archive": PolicyDeny})
	f.ask(t, "какие задачи на сегодня в Planner")
	prompt := f.model.Prompts()[0]
	if strings.Contains(prompt, "task_archive") {
		t.Errorf("запрещённый инструмент попал в промпт:\n%s", prompt)
	}
	for _, want := range []string{"task_list_today", "task_create(title: string", "Tools (6):"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("в промпте нет %q", want)
		}
	}
}

// Если модель всё же зовёт запрещённый инструмент, вызов отклоняется и на
// сервер не уходит; карточка говорит «запрещён», а не «не вышло».
func TestMCPDeniedToolCallRejected(t *testing.T) {
	f := newMCPChatFix(t, true, callArchive, "Не вышло: архивировать нельзя.")
	decideMCP(t, f.st, "planner", map[string]string{"task_archive": PolicyDeny})
	f.ask(t, "заархивируй задачу t1 в Planner")
	if calls := f.srv.Calls(); len(calls) != 0 {
		t.Fatalf("запрещённый вызов ушёл на сервер: %+v", calls)
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards[0].MCP.Status != "denied" || !strings.Contains(cards[0].Err, "запрещ") {
		t.Fatalf("карточка запрещённого вызова: %+v", cards)
	}
	if pending, _ := f.st.MCPPendingList(); len(pending) != 0 {
		t.Fatalf("у запрещённого вызова есть кнопка «Выполнить»: %+v", pending)
	}
}

// Новый инструмент — то, что сервер назвал после решения человека: запрещён
// по умолчанию, помечен новым, модели не виден, а человеку приходит
// уведомление. Решил — запись закрылась, инструмент виден.
func TestMCPNewToolDeniedMarkedAndNotified(t *testing.T) {
	f := newMCPChatFix(t, true, "ок")
	f.srv.ToolDefs = append(plannerTools(), mcptest.ToolDef{Name: "task_brand_new", Description: "Brand new tool"})
	if err := f.e.CheckMCP(context.Background(), "planner"); err != nil {
		t.Fatal(err)
	}
	m, _ := f.st.MCPServer("planner")
	var found *MCPToolState
	for _, st := range MCPToolStates(m) {
		st := st
		if st.Name == "task_brand_new" {
			found = &st
		}
	}
	if found == nil || !found.New || found.Policy != PolicyDeny {
		t.Fatalf("новый инструмент: %+v", found)
	}
	if fresh := mcpNewTools(m); len(fresh) != 1 || fresh[0] != "task_brand_new" {
		t.Fatalf("новых инструментов: %v", fresh)
	}
	res := f.e.RequestMCP(context.Background(), "тест", "planner", "task_brand_new", nil)
	if res.Policy != PolicyDeny || res.Err == "" || len(f.srv.Calls()) != 0 {
		t.Fatalf("новый инструмент вызвался: %+v", res)
	}
	if !strings.Contains(res.Why, "ещё не решён") {
		t.Errorf("отказ не говорит, что инструмент новый: %q", res.Why)
	}
	if _, open := f.st.OpenNotificationByKey(mcpNewNoticeKey("planner")); !open {
		t.Fatal("человек не узнал про новый инструмент")
	}
	f.ask(t, "что в Planner")
	if strings.Contains(f.model.Prompts()[0], "task_brand_new") {
		t.Error("новый инструмент виден модели до решения человека")
	}
	// решил — инструмент виден, уведомление закрылось
	decideMCP(t, f.st, "planner", map[string]string{"task_brand_new": PolicyAllow})
	m, _ = f.st.MCPServer("planner")
	f.e.noteNewMCPTools(m)
	if _, open := f.st.OpenNotificationByKey(mcpNewNoticeKey("planner")); open {
		t.Error("уведомление про новые инструменты не закрылось после решения")
	}
	if got, _ := ClassifyMCP(m, "task_brand_new"); got != PolicyAllow {
		t.Errorf("решённый инструмент получил %q", got)
	}
}

// Ответ для нового инструмента — настройка сервера: «Спросить» значит, что
// новое ждёт кнопки, а не запрещено.
func TestMCPDefaultAnswerForNewTool(t *testing.T) {
	f := newMCPChatFix(t, true, "ок")
	m, _ := f.st.MCPServer("planner")
	if m.RuleDefault != store.RuleDeny {
		t.Fatalf("по умолчанию для нового — %q, а должно быть «Запретить»", m.RuleDefault)
	}
	if err := f.st.SetMCPRules("planner", m.Rules, store.RuleAsk); err != nil {
		t.Fatal(err)
	}
	m, _ = f.st.MCPServer("planner")
	if got, _ := ClassifyMCP(m, "никогда_не_слышал"); got != PolicyAsk {
		t.Errorf("новый инструмент при ответе «Спросить» получил %q", got)
	}
	// неверный ответ не записывается: половина списка хуже отказа
	if err := f.st.SetMCPRules("planner", map[string]string{"task_list": "можно"}, store.RuleDeny); err == nil {
		t.Error("ответ не из трёх принят")
	}
	if err := f.st.SetMCPRules("planner", m.Rules, "можно"); err == nil {
		t.Error("ответ для нового не из трёх принят")
	}
}

// Сервер, добавленный после переезда, начинает без правил: все его
// инструменты новые. Перезапуск не принимает его за «прежний» и не выводит
// правил из круга, которого у него не было.
func TestNewServerStartsWithoutRules(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveMCPServer(&store.MCPServer{Name: "свежий", URL: "http://x/mcp", Enabled: true,
		Tools: []string{"task_list", "task_create"}}); err != nil {
		t.Fatal(err)
	}
	e.migrateMCPRules() // старт после добавления
	m, _ := st.MCPServer("свежий")
	for _, tool := range []string{"task_list", "task_create"} {
		if got, _ := ClassifyMCP(m, tool); got != PolicyDeny {
			t.Errorf("%s у нового сервера получил %q", tool, got)
		}
	}
	if fresh := mcpNewTools(m); len(fresh) != 2 {
		t.Errorf("новых инструментов %v, а их два", fresh)
	}
}
