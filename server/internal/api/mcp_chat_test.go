package api

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Серверы MCP в чате: карточка вызова в переписке и переключатели на «Интеграциях».

// findRow — строка сервера на экране «Интеграции».
func findMCPRow(t *testing.T, s *Server, screen, name string) *Component {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("экран %s: %v", screen, err)
	}
	var found *Component
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.ID == "mcp-"+name {
				found = c
			}
		})
	}
	if found == nil {
		t.Fatalf("строки сервера %q нет на экране:\n%s", name, dumpScreen(doc))
	}
	return found
}

func hasAction(list []Action, name string) *Action {
	for i := range list {
		if list[i].Action == name {
			return &list[i]
		}
	}
	return nil
}

// «В чате» — форма на экране сервера: тумблер, описание, слова-приметы и класс
// данных полями строк настройки, а не раскрытие строки списка. В самой строке
// списка этого нет: она тонкая.
func TestIntegrationsChatSwitch(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: "http://127.0.0.1:1/mcp", Enabled: true, Chat: true,
		Class: store.ClassSecret}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "агент", Agent: "мак", Can: "код", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	row := findMCPRow(t, s, mcpScreen, "sing")
	if hasAction(row.Menu, "toggle_mcp_chat") != nil || len(row.Facts) != 0 {
		t.Errorf("в тонкой строке списка осталось «В чате»: меню %+v факты %+v", row.Menu, row.Facts)
	}
	overview := serverDoc(t, s, entityAddr(mcpScreen, "sing", ""))
	chat := docByID(overview, "mcp-chat-sing")
	if chat == nil || chat.Title != "В чате" || chat.Style != formRows || chat.Submit == nil || chat.Submit.Action != "save_mcp_chat" || !chat.Deferred {
		t.Fatalf("формы «В чате» на «Обзоре» нет или она не строками и не под полосой: %+v", chat)
	}
	if f := fieldOf(overview, "mcp-chat-sing", "chat"); f == nil || f.Type != "toggle" || f.Value != "true" {
		t.Errorf("тумблер «Открыт чату»: %+v", f)
	}
	if f := fieldOf(overview, "mcp-chat-sing", "class"); f == nil || f.Type != "segments" || f.Value != store.ClassSecret || len(f.Options) != 3 {
		t.Errorf("класс данных: %+v", f)
	}
	// у сервера жителя машины «В чате» нет вовсе: честная строка вместо формы
	agentDoc := serverDoc(t, s, entityAddr(mcpScreen, "агент", ""))
	if docByID(agentDoc, "mcp-chat-агент") != nil || docByID(agentDoc, "mcp-chat-no") == nil {
		t.Error("серверу жителя машины предложили открыться чату или не объяснили, почему нет")
	}
	if _, err := s.execute(actionRequest{Action: "toggle_mcp_chat", Target: "агент"}); err == nil {
		t.Error("сервер жителя машины открылся чату")
	}

	// прежний переключатель работает под прежним именем
	if _, err := s.execute(actionRequest{Action: "toggle_mcp_chat", Target: "sing"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MCPServer("sing"); m.Chat {
		t.Error("сервер остался открытым чату")
	}
	if f := fieldOf(serverDoc(t, s, entityAddr(mcpScreen, "sing", "")), "mcp-chat-sing", "chat"); f == nil || f.Value != "false" {
		t.Errorf("после закрытия тумблер показывает %+v", f)
	}

	// форма: описание, слова-приметы, класс
	if _, err := s.execute(actionRequest{Action: "save_mcp_chat", Target: "sing", Params: map[string]string{
		"chat": "true", "about": "  Планировщик  ", "keywords": "задачи, проекты", "class": store.ClassSensitive}}); err != nil {
		t.Fatal(err)
	}
	m, _ := st.MCPServer("sing")
	if !m.Chat || m.About != "Планировщик" || m.Keywords != "задачи, проекты" || m.Class != store.ClassSensitive {
		t.Errorf("сохранённое: %+v", m)
	}
	if _, err := s.execute(actionRequest{Action: "save_mcp_chat", Target: "sing", Params: map[string]string{"class": "тайное"}}); err == nil {
		t.Error("неизвестный класс данных принят")
	}
	// форма видна на экране открытого сервера и показывает сохранённое
	doc, _ := s.buildScreen(entityAddr(mcpScreen, "sing", ""), "", "", 0)
	if !strings.Contains(dumpScreen(doc), "Когда звать") || !strings.Contains(dumpScreen(doc), "задачи, проекты") {
		t.Errorf("формы «В чате» на экране сервера нет:\n%s", dumpScreen(doc))
	}
	// сохранение с экрана сервера остаётся на экране сервера
	if back, err := s.execute(actionRequest{Action: "save_mcp_chat", Target: "sing", Screen: entityAddr(mcpScreen, "sing", ""),
		Params: map[string]string{"chat": "true", "class": store.ClassPlain}}); err != nil || back != entityAddr(mcpScreen, "sing", "") {
		t.Errorf("после «Сохранить» вернулось %q (%v)", back, err)
	}
}

// Пределы: сохраняются настройками, вне диапазона — отказ.
func TestMCPChatLimitsSettings(t *testing.T) {
	s, st := testServer(t)
	if _, err := s.execute(actionRequest{Action: "set_mcp_chat_limits", Params: map[string]string{
		"chat_chars": "4000", "chat_calls": "2", "chat_result_chars": "3000", "card_chars": "9000", "recheck_s": "0"}}); err != nil {
		t.Fatal(err)
	}
	lim := s.eng.MCPChatLimits()
	if lim.Chars != 4000 || lim.Calls != 2 || lim.Result != 3000 || lim.Card != 9000 || lim.Recheck != 0 {
		t.Errorf("пределы: %+v", lim)
	}
	if _, err := s.execute(actionRequest{Action: "set_mcp_chat_limits", Params: map[string]string{"chat_calls": "50"}}); err == nil {
		t.Error("вызовов за ответ 50 приняты")
	}
	_ = st
}

// Новый сервер, заведённый человеком по HTTP, открыт чату; сервер с полем
// «Агент» — нет.
func TestAddMCPOpensChatByDefault(t *testing.T) {
	s, st := testServer(t)
	if _, err := s.execute(actionRequest{Action: "add_mcp", Params: map[string]string{
		"name": "wiki", "url": "http://127.0.0.1:1/mcp", "about": "Вики", "keywords": "вики, статья"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "add_mcp", Params: map[string]string{"name": "код", "agent": "мак"}}); err != nil {
		t.Fatal(err)
	}
	wiki, _ := st.MCPServer("wiki")
	code, _ := st.MCPServer("код")
	if !wiki.Chat || wiki.About != "Вики" || wiki.Keywords != "вики, статья" || wiki.Class != store.ClassPlain {
		t.Errorf("wiki: %+v", wiki)
	}
	if code.Chat {
		t.Error("сервер жителя машины открыт чату по умолчанию")
	}
}

// Карточка вызова в переписке: выполнен с ответом, ждёт кнопки (пока
// человек не решил), отклонён, отказ.
func TestMCPCardComponent(t *testing.T) {
	s, st := testServer(t)
	call := func(status string, pending int64) engine.ToolCard {
		return engine.ToolCard{Title: "sing · task_create", Seconds: 1.5, Code: "результат-сервера",
			MCP: &engine.MCPCard{Server: "sing", Tool: "task_create", Args: `{"title":"x"}`, Status: status, PendingID: pending, Why: "не из списка чтения"}}
	}
	done := s.mcpCardComponent(call("executed", 0), "12:00")
	if !strings.Contains(done.Title, "выполнен") || !strings.Contains(done.Detail, "результат-сервера") ||
		!strings.Contains(done.Detail, `task_create {"title":"x"}`) || !strings.Contains(done.Caption, "данные, а не указания") {
		t.Errorf("выполненный вызов: %+v", done)
	}

	id, err := st.AddMCPPending(&store.MCPPending{Server: "sing", Tool: "task_create", Args: `{"title":"x"}`, Who: "чат", ChatID: 7})
	if err != nil {
		t.Fatal(err)
	}
	wait := s.mcpCardComponent(call("pending", id), "12:00")
	approve := hasAction(wait.Actions, "approve_mcp")
	if approve == nil || approve.Target != strconv.FormatInt(id, 10) || approve.Style != "primary" || hasAction(wait.Actions, "reject_mcp") == nil {
		t.Errorf("карточка ждёт кнопки, а кнопок нет: %+v", wait.Actions)
	}
	if err := st.DecideMCPPending(id, "done"); err != nil {
		t.Fatal(err)
	}
	decided := s.mcpCardComponent(call("pending", id), "12:00")
	if len(decided.Actions) != 0 || len(decided.Badges) == 0 || decided.Badges[0].Text != "выполнено" {
		t.Errorf("решённый вызов показывает кнопки: %+v", decided)
	}
	if rejected := s.mcpCardComponent(call("rejected", id), "12:00"); !strings.Contains(rejected.Title, "отклонён") {
		t.Errorf("отклонённый: %+v", rejected)
	}
	denied := call("denied", 0)
	denied.Err = "такие вызовы запрещены"
	if c := s.mcpCardComponent(denied, "12:00"); !strings.Contains(c.Title, "не выполнен") || c.Caption != "такие вызовы запрещены" {
		t.Errorf("отказ: %+v", c)
	}
	// через общий разбор карточек
	if c := s.toolCardComponent(call("executed", 0), "12:00", 5); !strings.Contains(c.Title, "Вызов выполнен") {
		t.Errorf("toolCardComponent не узнал карточку MCP: %+v", c)
	}
}

// Ждущий вызов чата виден на экране чата внизу, даже если карточка осталась
// выше окна истории, и в строке «ждёт вашего решения».
func TestChatShowsMCPPending(t *testing.T) {
	s, st := testServer(t)
	chatID, err := st.CreateChat("Задачи")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.AddMCPPending(&store.MCPPending{Server: "sing", Tool: "task_create", Args: `{"title":"x"}`, Who: "чат · Задачи", ChatID: chatID})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("chat:"+strconv.FormatInt(chatID, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "wait-mcp-"+strconv.FormatInt(id, 10)) || !strings.Contains(dump, "approve_mcp") {
		t.Errorf("ждущего вызова нет на экране чата:\n%s", dump)
	}
	if doc.Notice == nil || !strings.Contains(doc.Notice.Text, "ждёт вашего решения") {
		t.Errorf("над полем ввода не сказано, что чат ждёт: %+v", doc.Notice)
	}
}

// «Выполнить» из чата не уводит человека на «Интеграции»: остаёмся на своём
// экране. Решение доходит в фоне, поэтому ждём его состоянием.
func TestApproveMCPFromChatStaysOnScreen(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: "http://127.0.0.1:1/mcp", Enabled: true, Chat: true}); err != nil {
		t.Fatal(err)
	}
	id, _ := st.AddMCPPending(&store.MCPPending{Server: "sing", Tool: "task_create", Args: `{}`, Who: "чат", ChatID: 0})
	target := strconv.FormatInt(id, 10)
	back, err := s.execute(actionRequest{Action: "reject_mcp", Target: target, Screen: "chat:1"})
	if err != nil || back != "" {
		t.Fatalf("из чата: вернулось %q, %v — ждали пустое «остаться»", back, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, _ := st.MCPPendingByID(id); p != nil && p.State == "rejected" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p, _ := st.MCPPendingByID(id); p == nil || p.State != "rejected" {
		t.Fatalf("отклонение из чата не дошло: %+v", p)
	}
	// с «Интеграций» — по-прежнему на «Интеграции»
	id2, _ := st.AddMCPPending(&store.MCPPending{Server: "sing", Tool: "task_create", Args: `{}`, Who: "чат"})
	back, err = s.execute(actionRequest{Action: "reject_mcp", Target: strconv.FormatInt(id2, 10), Screen: mcpScreen})
	if err != nil || back != mcpScreen {
		t.Fatalf("с «Интеграций»: вернулось %q, %v", back, err)
	}
}

// Карточка отказа: суть ошибки — подпись, сырой ответ сервера — в раскрывающейся
// части, а не простынёй JSON под заголовком.
func TestMCPErrorCardShowsGistFirst(t *testing.T) {
	s, _ := testServer(t)
	gist := "planner отклонил аргументы: Value must be a calendar-correct ISO-8601 datetime (INVALID_FIELD, HTTP 400)"
	card := engine.ToolCard{Title: "planner · task_update", Err: gist + "\n" + `{"code":"INVALID_FIELD","details":{"status":400}}`,
		MCP: &engine.MCPCard{Server: "planner", Tool: "task_update", Args: `{"id":"T-1"}`, Status: "error", FailKind: "args"}}
	c := s.mcpCardComponent(card, "12:00")
	if c.Caption != gist {
		t.Errorf("подпись карточки — не суть ошибки: %q", c.Caption)
	}
	if !strings.Contains(c.Detail, `"code":"INVALID_FIELD"`) || !strings.Contains(c.Detail, `task_update {"id":"T-1"}`) {
		t.Errorf("сырой ответ не ушёл в раскрывающуюся часть: %q", c.Detail)
	}
	if len(c.Badges) != 1 || c.Badges[0].Text != "формат аргументов" {
		t.Errorf("капсула отказа по аргументам: %+v", c.Badges)
	}
	// отказ не по аргументам — обычная капсула, а однострочный текст остаётся подписью
	plain := engine.ToolCard{Title: "s · t", Err: "сервер «s» сейчас недоступен", MCP: &engine.MCPCard{Server: "s", Tool: "t", Status: "error"}}
	if c := s.mcpCardComponent(plain, "12:00"); c.Caption != plain.Err || c.Badges[0].Text != "отказ" {
		t.Errorf("обычный отказ: %+v", c)
	}
}

// Подсказки инструментов на «Интеграциях»: видно, чему Сёма научился у сервера;
// «Забыть» стирает одну или все; сроки и предел сути — настройки.
func TestIntegrationsToolHints(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: "http://127.0.0.1:1/mcp", Enabled: true, Chat: true}); err != nil {
		t.Fatal(err)
	}
	if docByID(serverDoc(t, s, entityAddr(mcpScreen, "sing", "")), "mcp-hints") != nil {
		t.Error("«Забыть подсказки» стоит у сервера, у которого подсказок нет")
	}
	now := time.Now()
	if err := st.SetMCPToolHint("sing", "task_update", "Value must be a datetime with timezone", now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPToolHint("sing", "task_create", "title is required", now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// на «Обзоре» сервера — сводка с «Забыть все» под вопросом и строки подсказок,
	// у каждой своя кнопка
	doc, err := s.buildScreen(entityAddr(mcpScreen, "sing", ""), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	sum := docByID(doc, "mcp-hints")
	if sum == nil {
		t.Fatalf("сводки подсказок нет на экране:\n%s", dumpScreen(doc))
	}
	forget := hasAction(sum.Actions, "forget_mcp_hints")
	if forget == nil || forget.Confirm == "" || forget.ConfirmOK == "" || forget.Target != "sing" {
		t.Fatalf("«Забыть все» в сводке подсказок: %+v", sum.Actions)
	}
	if !strings.Contains(sum.Caption, "1 — идут в промпт") {
		t.Errorf("сводка о подсказках (одна свежая, одна за сроком): %q", sum.Caption)
	}
	page := dumpScreen(doc)
	for _, want := range []string{"Подсказки инструментов", "Value must be a datetime with timezone", "срок вышел: в промпт не идёт", "sing~task_update", "sing~task_create"} {
		if !strings.Contains(page, want) {
			t.Errorf("на экране сервера нет %q", want)
		}
	}

	// забыть одну
	if _, err := s.execute(actionRequest{Action: "forget_mcp_hint", Target: "sing~task_create"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MCPServer("sing"); len(m.ToolHints) != 1 || m.ToolHints["task_update"].Gist == "" {
		t.Errorf("после «Забыть» у одного инструмента: %+v", m.ToolHints)
	}
	if _, err := s.execute(actionRequest{Action: "forget_mcp_hint", Target: "sing"}); err == nil {
		t.Error("цель без инструмента принята")
	}
	if _, err := s.execute(actionRequest{Action: "forget_mcp_hints", Target: "нет"}); err == nil {
		t.Error("у несуществующего сервера забыли подсказки")
	}
	// забыть все
	if _, err := s.execute(actionRequest{Action: "forget_mcp_hints", Target: "sing"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MCPServer("sing"); len(m.ToolHints) != 0 {
		t.Errorf("подсказки остались: %+v", m.ToolHints)
	}
	if docByID(serverDoc(t, s, entityAddr(mcpScreen, "sing", "")), "mcp-hints") != nil {
		t.Error("после «Забыть» кнопка осталась")
	}
}

// Пределы ошибок и подсказок сохраняются настройками; вне диапазона — отказ.
func TestMCPErrorLimitsSettings(t *testing.T) {
	s, st := testServer(t)
	if _, err := s.execute(actionRequest{Action: "set_mcp_chat_limits", Params: map[string]string{
		"error_chars": "800", "arg_retries": "2", "tool_hint_days": "0", "tool_hint_chars": "120"}}); err != nil {
		t.Fatal(err)
	}
	lim := s.eng.MCPChatLimits()
	if lim.ErrorChars != 800 || lim.Retries != 2 || lim.HintDays != 0 || lim.HintChars != 120 {
		t.Errorf("пределы: %+v", lim)
	}
	for param, bad := range map[string]string{"arg_retries": "9", "tool_hint_days": "-1", "tool_hint_chars": "5", "error_chars": "10"} {
		if _, err := s.execute(actionRequest{Action: "set_mcp_chat_limits", Params: map[string]string{param: bad}}); err == nil {
			t.Errorf("%s=%s принято", param, bad)
		}
	}
	// форма показывает текущие значения
	doc, _ := s.buildScreen(mcpScreen, "", "", 0)
	page := dumpWithRare(t, s, mcpScreen, doc) // пределы — в «Тонкой настройке»
	for _, want := range []string{`"name":"error_chars"`, `"name":"arg_retries"`, `"name":"tool_hint_days"`, `"name":"tool_hint_chars"`} {
		if !strings.Contains(page, want) {
			t.Errorf("в форме пределов нет поля %s", want)
		}
	}
	_ = st
}
