package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Серверы MCP в чате: модель видит инструменты подключённого сервера и зовёт
// их сама. Поддельный сервер MCP и поддельная модель, без сети наружу.

// scriptedModel — модель, отвечающая по очереди заранее заданными текстами
// (последний повторяется) и запоминающая все промпты.
type scriptedModel struct {
	mu      sync.Mutex
	answers []string
	prompts []string
}

func (m *scriptedModel) Name() string { return "сценарная" }

func (m *scriptedModel) Complete(_ context.Context, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, prompt)
	i := len(m.prompts) - 1
	if i >= len(m.answers) {
		i = len(m.answers) - 1
	}
	if i < 0 {
		return "", nil
	}
	return m.answers[i], nil
}

func (m *scriptedModel) CompleteStream(ctx context.Context, prompt string, _ func(string)) (string, error) {
	return m.Complete(ctx, prompt)
}

func (m *scriptedModel) Prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

// plannerTools — инструменты, похожие на настоящие имена Planner.
func plannerTools() []mcptest.ToolDef {
	obj := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return []mcptest.ToolDef{
		{Name: "task_list_today", Description: "List tasks planned for today", Schema: obj(map[string]any{
			"maxCount": map[string]any{"type": "integer", "description": "How many tasks to return"}})},
		{Name: "task_list", Description: "List tasks with filters", Schema: obj(map[string]any{
			"projectId": str("Project id"), "maxCount": map[string]any{"type": "integer"}})},
		{Name: "task_create", Description: "Create a task", Schema: obj(map[string]any{
			"title":     str("Task title"),
			"priority":  map[string]any{"type": "integer", "enum": []any{0, 1, 2}, "description": "0/1/2 = HIGH/NORMAL/LOW"},
			"projectId": str("Project id")}, "title")},
		{Name: "task_archive", Description: "Archive a task", Schema: obj(map[string]any{"id": str("Task id")}, "id")},
		{Name: "checklist_item_check", Description: "Check a checklist item", Schema: obj(map[string]any{"id": str("Item id")}, "id")},
		{Name: "project_list", Description: "List projects"},
		{Name: "get_my_context", Description: "Who am I and what is around me"},
	}
}

// mcpChatFix — всё для сценариев: движок, поддельный сервер, сценарная модель
// и чат, в котором она отвечает.
type mcpChatFix struct {
	e      *Engine
	st     *store.Store
	srv    *mcptest.Server
	model  *scriptedModel
	chatID int64
}

func newMCPChatFix(t *testing.T, trusted bool, answers ...string) *mcpChatFix {
	t.Helper()
	e, st := newTestEngine(t, "", nil)
	srv := mcptest.New(t, false)
	srv.AuthRequired = false
	srv.ToolDefs = plannerTools()
	srv.CallReply = func(name string, args map[string]any) string {
		return `[{"id":"t1","title":"Оплатить сервер"},{"id":"t2","title":"Позвонить в банк"}]`
	}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "planner", URL: srv.URL(), Enabled: true, Chat: true,
		About:    "Planner — планировщик владельца: задачи, проекты",
		Keywords: "задачи, задача, проект, сегодня, Planner",
		Class:    store.ClassPlain, LegacyRead: mcpSeed.read, LegacyNever: mcpSeed.never}); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckMCP(context.Background(), "planner"); err != nil {
		t.Fatalf("проверка сервера: %v", err)
	}
	// как на живом сервере: прежние круги переехали на правила по инструментам,
	// известным инструментам — то же поведение, что было
	e.migrateMCPRules()
	name := "облако"
	if trusted {
		name = "дом"
	}
	if err := e.AddModel(ModelConf{Name: name, Type: "openai", BaseURL: "http://llm:8080/v1", Model: "q", Trusted: trusted}); err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{answers: answers}
	e.setProvider(name, model)
	chatID, err := st.CreateChat("Задачи")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatModel(chatID, name); err != nil {
		t.Fatal(err)
	}
	return &mcpChatFix{e: e, st: st, srv: srv, model: model, chatID: chatID}
}

// ask — человек пишет в чат, Сёма отвечает синхронно (как задача по расписанию).
func (f *mcpChatFix) ask(t *testing.T, text string) {
	t.Helper()
	ctx := llm.WithConsumer(context.Background(), fmt.Sprintf("chat:%d", f.chatID))
	if err := f.e.ChatReply(ctx, f.chatID, text); err != nil {
		t.Fatalf("ответ в чате: %v", err)
	}
}

// cards — карточки MCP в переписке чата.
func (f *mcpChatFix) cards(t *testing.T) []ToolCard {
	t.Helper()
	msgs, err := f.st.ChatMsgs(f.chatID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []ToolCard
	for _, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		if c, ok := ParseToolCard(m.Content); ok && c.MCP != nil {
			out = append(out, c)
		}
	}
	return out
}

func (f *mcpChatFix) lastAnswer(t *testing.T) string {
	t.Helper()
	msgs, _ := f.st.ChatMsgs(f.chatID, 100)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			return msgs[i].Content
		}
	}
	return ""
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

const callToday = "Смотрю задачи.\n```sema-mcp:planner\n{\"tool\": \"task_list_today\", \"args\": {}}\n```"

// Вопрос про задачи в Planner: в промпте опись серверов и полный список
// инструментов с аргументами — то, чего не было и из-за чего Сёма искал письма.
func TestMCPChatPromptCatalogueAndFullList(t *testing.T) {
	f := newMCPChatFix(t, true, "ок")
	f.ask(t, "какие задачи на сегодня в Planner")
	prompt := f.model.Prompts()[0]
	for _, want := range []string{
		"External MCP servers",
		"- planner — Planner — планировщик владельца",
		"Tools (7): task_list_today, task_list, task_create",
		"Tools of «planner»",
		"task_create(title: string, priority?: 0|1|2, projectId?: string)",
		"checklist_item_check(id: string)",
		"```sema-mcp:<server>",
		"do not look for it in the mailbox",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("в промпте нет %q\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "0/1/2 = HIGH/NORMAL/LOW") {
		t.Error("пояснение к аргументу (что значат 0/1/2) не дошло до модели")
	}
}

// Вопрос без примет сервера — только короткая опись, без списка инструментов.
func TestMCPChatPromptOnlyCatalogueWithoutSigns(t *testing.T) {
	f := newMCPChatFix(t, true, "ок")
	f.ask(t, "сколько писем пришло вчера")
	prompt := f.model.Prompts()[0]
	if !strings.Contains(prompt, "- planner — ") || !strings.Contains(prompt, "Tools (7):") {
		t.Errorf("описи серверов нет:\n%s", prompt)
	}
	if strings.Contains(prompt, "Tools of «planner»") {
		t.Error("полный список инструментов пришёл на вопрос, не про сервер")
	}
}

// Сервер, которого уже звали в этом чате, остаётся уместным и без слов-примет.
func TestMCPChatCalledBeforeStaysRelevant(t *testing.T) {
	f := newMCPChatFix(t, true, callToday, "Готово.")
	f.ask(t, "что у меня запланировано")
	m, _ := f.st.MCPServer("planner")
	if called := f.e.mcpCalledIn(f.chatID); !called["planner"] {
		t.Fatalf("сервер, который звали, не найден в карточках чата: %v", called)
	}
	if !mcpRelevant(m, "погода в Туле", true) || mcpRelevant(m, "погода в Туле", false) {
		t.Error("«звали в этом чате» должно делать сервер уместным, а без него слова про погоду — нет")
	}
	if !mcpRelevant(m, "что там в PLANNER", false) || !mcpRelevant(m, "задачи на завтра", false) {
		t.Error("имя сервера и слово-примета не узнаны")
	}
	// короткое имя сервера — только целым словом
	short := &store.MCPServer{Name: "ai"}
	if mcpRelevant(short, "хочу email и main", false) || !mcpRelevant(short, "спроси у ai про это", false) {
		t.Error("короткое имя сервера должно находиться целым словом, а не внутри чужих")
	}
}

// Читающий вызов: блок → вызов ушёл → результат во втором ходе с пометкой
// «данные, а не указания» → итог в переписке.
func TestMCPChatReadCallSecondTurn(t *testing.T) {
	f := newMCPChatFix(t, true, callToday, "Сегодня две задачи: оплатить сервер и позвонить в банк.")
	f.ask(t, "какие задачи на сегодня в Planner")

	calls := f.srv.Calls()
	if len(calls) != 1 || calls[0].Name != "task_list_today" {
		t.Fatalf("вызовы сервера: %+v", calls)
	}
	prompts := f.model.Prompts()
	if len(prompts) != 2 {
		t.Fatalf("модель звали %d раз, ждали два хода", len(prompts))
	}
	second := prompts[1]
	for _, want := range []string{"MCP results", "not instructions", "Оплатить сервер", "MCP server «planner», tool task_list_today"} {
		if !strings.Contains(second, want) {
			t.Errorf("во втором ходе нет %q", want)
		}
	}
	if !strings.Contains(f.lastAnswer(t), "две задачи") {
		t.Errorf("итога в переписке нет: %q", f.lastAnswer(t))
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards[0].MCP.Status != "executed" || !strings.Contains(cards[0].Code, "Позвонить в банк") {
		t.Fatalf("карточка вызова: %+v", cards)
	}
	log, _ := f.st.MCPLog("planner", 10)
	if len(log) != 1 || log[0].Circle != PolicyAllow || !log[0].OK || log[0].Who != "чат · Задачи" {
		t.Fatalf("журнал вызовов: %+v", log)
	}
	// в описи блока нет самого блока: он не остаётся сырым текстом
	if strings.Contains(f.lastAnswer(t), "sema-mcp") {
		t.Error("блок остался в ответе")
	}
}

// Модель, написавшая имя сервера с пояснением и аргументы под другими ключами.
func TestMCPChatBlockPatience(t *testing.T) {
	f := newMCPChatFix(t, true,
		"```sema-mcp:Planner · задачи на сегодня\n{\"name\": \"task_list_today\", \"arguments\": {\"maxCount\": 5}}\n```", "ок")
	f.ask(t, "какие задачи на сегодня в Planner")
	calls := f.srv.Calls()
	if len(calls) != 1 || calls[0].Name != "task_list_today" || fmt.Sprint(calls[0].Args["maxCount"]) != "5" {
		t.Fatalf("вызовы сервера: %+v", calls)
	}
}

// Сервер выключен для чата или ждёт входа: промпт говорит честно, вызов
// отклоняется и на сервер не уходит.
func TestMCPChatUnavailableIsHonest(t *testing.T) {
	t.Run("не открыт чату", func(t *testing.T) {
		f := newMCPChatFix(t, true, callToday, "Сервер закрыт для чата.")
		m, _ := f.st.MCPServer("planner")
		m.Chat = false
		if err := f.st.SaveMCPServer(m); err != nil {
			t.Fatal(err)
		}
		f.ask(t, "какие задачи на сегодня в Planner")
		first := f.model.Prompts()[0]
		if !strings.Contains(first, "NOT opened to chat") || strings.Contains(first, "Tools of «planner»") {
			t.Errorf("промпт не сказал, что сервер закрыт чату:\n%s", first)
		}
		if n := len(f.srv.Calls()); n != 0 {
			t.Fatalf("на сервер, закрытый чату, ушло %d вызовов", n)
		}
		cards := f.cards(t)
		if len(cards) != 1 || cards[0].MCP.Status != "denied" || !strings.Contains(cards[0].Err, "не открыт чату") {
			t.Fatalf("карточка отказа: %+v", cards)
		}
	})
	t.Run("нужен вход", func(t *testing.T) {
		f := newMCPChatFix(t, true, callToday, "Нужно войти.")
		if err := f.st.SetMCPState("planner", "login", "нужен вход: сервер отвечает 401"); err != nil {
			t.Fatal(err)
		}
		f.ask(t, "какие задачи на сегодня в Planner")
		first := f.model.Prompts()[0]
		for _, want := range []string{"cannot use now", "planner: connected, but unavailable right now", "нужен вход"} {
			if !strings.Contains(first, want) {
				t.Errorf("в промпте нет %q\n%s", want, first)
			}
		}
		if strings.Contains(first, "Tools of «planner»") {
			t.Error("недоступному серверу дали список инструментов")
		}
		if n := len(f.srv.Calls()); n != 0 {
			t.Fatalf("на сервер без входа ушли вызовы: %+v", f.srv.Calls())
		}
		cards := f.cards(t)
		if len(cards) != 1 || cards[0].MCP.Status != "error" || !strings.Contains(cards[0].Err, "недоступен") {
			t.Fatalf("карточка отказа: %+v", cards)
		}
		// вторым ходом модель узнала причину и ответила честно
		if second := f.model.Prompts(); len(second) != 2 || !strings.Contains(second[1], "нужен вход") {
			t.Errorf("вторым ходом модель причины не получила")
		}
	})
}

// Упавший сервер чат перепроверяет сам, не дожидаясь кнопки; недавно
// проверенный (недавно упавший) — не трогает.
func TestMCPChatRechecksFailedServer(t *testing.T) {
	f := newMCPChatFix(t, true, "ок")
	m, _ := f.st.MCPServer("planner")
	m.State, m.Note, m.Checked = "fail", "минутный сбой сети", time.Now().Add(-time.Hour).Format(time.RFC3339)
	if err := f.st.SaveMCPServer(m); err != nil {
		t.Fatal(err)
	}
	inits := f.srv.Count("initialize")
	f.ask(t, "какие задачи на сегодня в Planner")
	if got, _ := f.st.MCPServer("planner"); got.State != "ok" {
		t.Fatalf("после перепроверки состояние %q", got.State)
	}
	if !strings.Contains(f.model.Prompts()[0], "Tools of «planner»") {
		t.Error("сервер ожил, а модель его не увидела")
	}
	if f.srv.Count("initialize") <= inits {
		t.Error("перепроверки не было")
	}

	// свежий отказ не перепроверяется: сервер только что упал
	if err := f.st.SetMCPState("planner", "fail", "только что"); err != nil {
		t.Fatal(err)
	}
	inits = f.srv.Count("initialize")
	f.ask(t, "какие задачи на сегодня в Planner")
	if f.srv.Count("initialize") != inits {
		t.Error("свежий отказ перепроверили сразу же")
	}
	if !strings.Contains(f.model.Prompts()[1], "unavailable right now") {
		t.Error("модель не узнала, что сервер сейчас недоступен")
	}
}

// Изменяющий вызов: карточка «ждёт кнопки» и уведомление; на сервер ничего не
// ушло. После решения человека результат ложится в чат, и Сёма продолжает.
func TestMCPChatWriteWaitsThenContinues(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Заведу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Купить молоко\", \"priority\": 1}}\n```",
		"Задача заведена: «Купить молоко».")
	f.srv.CallReply = func(name string, args map[string]any) string {
		return `{"id":"t9","title":"Купить молоко"}`
	}
	f.ask(t, "заведи задачу в Planner: купить молоко")

	if n := len(f.srv.Calls()); n != 0 {
		t.Fatalf("изменяющий вызов ушёл без кнопки: %+v", f.srv.Calls())
	}
	pend, _ := f.st.MCPPendingList()
	if len(pend) != 1 || pend[0].ChatID != f.chatID || pend[0].Tool != "task_create" {
		t.Fatalf("ожидающие вызовы: %+v", pend)
	}
	id := pend[0].ID
	n, open := f.st.OpenNotificationByKey(fmt.Sprintf("mcp:%d", id))
	if !open || !strings.Contains(n.Summary, "чат · Задачи") {
		t.Fatalf("уведомления о вызове нет: %+v", n)
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards[0].MCP.Status != "pending" || cards[0].MCP.PendingID != id {
		t.Fatalf("карточка ожидания: %+v", cards)
	}
	if len(f.model.Prompts()) != 1 {
		t.Fatalf("на вызов, который ждёт кнопки, второго хода быть не должно")
	}
	if !f.e.waitingForHuman(f.chatID) {
		t.Error("пинатель не знает, что чат ждёт человека")
	}

	// человек нажимает «Выполнить»
	if _, err := f.e.ApproveMCP(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	if calls := f.srv.Calls(); len(calls) != 1 || calls[0].Name != "task_create" || fmt.Sprint(calls[0].Args["title"]) != "Купить молоко" {
		t.Fatalf("после подтверждения вызовы: %+v", calls)
	}
	// одна карточка, а не две: ждавшая стала выполненной
	cards = f.cards(t)
	if len(cards) != 1 || cards[0].MCP.Status != "executed" || !strings.Contains(cards[0].Code, "t9") {
		t.Fatalf("карточка после подтверждения: %+v", cards)
	}
	// Сёма продолжил: модель звали второй раз, и результат был в промпте
	waitFor(t, "продолжение чата после подтверждения", func() bool {
		f.e.thinkingMu.Lock()
		thinking := f.e.thinking[f.chatID]
		f.e.thinkingMu.Unlock()
		return len(f.model.Prompts()) == 2 && !thinking && strings.Contains(f.lastAnswer(t), "заведена")
	})
	if !strings.Contains(f.model.Prompts()[1], "t9") {
		t.Errorf("результат подтверждённого вызова не дошёл до модели:\n%.800s", f.model.Prompts()[1])
	}
	if f.e.waitingForHuman(f.chatID) {
		t.Error("чат всё ещё считается ждущим человека")
	}
	// отклонение: вызова нет, карточка «отклонено»
	id2, _ := f.st.AddMCPPending(&store.MCPPending{Server: "planner", Tool: "task_create", Args: `{"title":"x"}`, Who: "чат · Задачи", ChatID: f.chatID})
	if _, err := f.e.ApproveMCP(context.Background(), id2, false); err != nil {
		t.Fatal(err)
	}
	if n := len(f.srv.Calls()); n != 1 {
		t.Errorf("отклонённый вызов дошёл до сервера: %d вызовов", n)
	}
}

// Ответ одним блоком, ждущим кнопки, не остаётся пустым: человек видит слово, а
// не одну карточку.
func TestMCPChatPendingOnlyAnswerSaysSo(t *testing.T) {
	f := newMCPChatFix(t, true, "```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"x\"}}\n```")
	f.ask(t, "заведи задачу в Planner")
	if got := f.lastAnswer(t); !strings.Contains(got, "ждёт вашей кнопки") {
		t.Errorf("ответ: %q", got)
	}
}

// Больше положенного блоков в ответе — лишние не выполняются, и об этом сказано.
func TestMCPChatExtraBlocksNotRun(t *testing.T) {
	var answer strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&answer, "```sema-mcp:planner\n{\"tool\": \"task_list\", \"args\": {\"maxCount\": %d}}\n```\n", i+1)
	}
	f := newMCPChatFix(t, true, answer.String(), "Готово.")
	f.ask(t, "покажи задачи Planner")
	if n := len(f.srv.Calls()); n != 3 {
		t.Fatalf("выполнено %d вызовов, ждали три", n)
	}
	second := f.model.Prompts()[1]
	if !strings.Contains(second, "не больше 3") {
		t.Errorf("модель не узнала, что лишние не выполнены:\n%.600s", second)
	}
	cards := f.cards(t)
	skipped := 0
	for _, c := range cards {
		if c.MCP.Status == "denied" && strings.Contains(c.Err, "не больше 3") {
			skipped++
		}
	}
	if len(cards) != 5 || skipped != 2 {
		t.Fatalf("карточек %d, отказных %d", len(cards), skipped)
	}
}

// Настройка предела вызовов действует.
func TestMCPChatCallsLimitIsSetting(t *testing.T) {
	f := newMCPChatFix(t, true, strings.Repeat(callToday+"\n", 3), "Готово.")
	if err := f.st.SetSetting(mcpChatCallsKey, "1"); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "покажи задачи Planner")
	if n := len(f.srv.Calls()); n != 1 {
		t.Fatalf("выполнено %d вызовов при пределе 1", n)
	}
}

// Секрет в ответе сервера прячется маской до записи: ни в журнале, ни в
// карточке, ни в промпте модели его нет.
func TestMCPResultSecretsMasked(t *testing.T) {
	f := newMCPChatFix(t, true, callToday, "Готово.")
	if err := f.st.AddMemoryRule("mask", "sk-live-ZZ9", ""); err != nil {
		t.Fatal(err)
	}
	f.srv.CallReply = func(string, map[string]any) string {
		return "Задача: оплатить сервер\napi_key=sk-live-ZZ9-secret\nЗадача: позвонить"
	}
	f.ask(t, "какие задачи на сегодня в Planner")
	second := f.model.Prompts()[1]
	if strings.Contains(second, "sk-live-ZZ9") {
		t.Fatal("секрет из ответа сервера дошёл до модели")
	}
	if !strings.Contains(second, "Оплатить сервер") && !strings.Contains(second, "оплатить сервер") {
		t.Error("вместе с секретом пропал и весь результат")
	}
	for _, c := range f.cards(t) {
		if strings.Contains(c.Code, "sk-live-ZZ9") {
			t.Fatal("секрет остался в карточке чата")
		}
	}
	log, _ := f.st.MCPLog("planner", 5)
	if len(log) == 0 || strings.Contains(log[0].Result, "sk-live-ZZ9") {
		t.Fatalf("секрет попал в журнал вызовов: %+v", log)
	}
}

// Длинный результат обрезается по рунам с названным остатком: и в модели, и
// в карточке пределы свои и настраиваются.
func TestMCPResultClippedWithRemainder(t *testing.T) {
	f := newMCPChatFix(t, true, callToday, "Готово.")
	f.srv.CallReply = func(string, map[string]any) string { return strings.Repeat("Ѫ", 9000) }
	if err := f.st.SetSetting(mcpChatResultKey, "1000"); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "какие задачи на сегодня в Planner")
	second := f.model.Prompts()[1]
	if strings.Count(second, "Ѫ") != 1000 || !strings.Contains(second, "показано 1000 из 9000 знаков") {
		t.Errorf("результат для модели: %d знаков, остаток назван: %v",
			strings.Count(second, "Ѫ"), strings.Contains(second, "показано 1000 из 9000"))
	}
	if cards := f.cards(t); len(cards) != 1 || strings.Count(cards[0].Code, "Ѫ") != 9000 {
		t.Error("в карточке чата результат должен остаться целым: предел карточки другой")
	}
}

// Секретный сервер облачной модели не отдаёт результат: вместо него строка
// `cutNotice`. Доверенная модель тот же ответ получает. Человек видит
// результат карточкой в обоих случаях.
func TestMCPSecretServerCutForCloud(t *testing.T) {
	setSecret := func(f *mcpChatFix) {
		m, _ := f.st.MCPServer("planner")
		m.Class = store.ClassSecret
		if err := f.st.SaveMCPServer(m); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("облачная", func(t *testing.T) {
		f := newMCPChatFix(t, false, callToday, "Данные мне не переданы.")
		setSecret(f)
		f.ask(t, "какие задачи на сегодня в Planner")
		first := f.model.Prompts()[0]
		if strings.Contains(first, "Tools of «planner»") {
			t.Error("облачной модели дали список инструментов закрытого сервера")
		}
		if !strings.Contains(first, "Withheld from you") || !strings.Contains(first, "MCP «planner»") {
			t.Errorf("облачная модель не узнала, что сервер от неё закрыт:\n%s", first)
		}
		// модель всё равно написала блок: вызов идёт (человек увидит ответ),
		// а модели результат не отдаётся
		second := f.model.Prompts()[1]
		if strings.Contains(second, "Оплатить сервер") {
			t.Fatal("секретные данные сервера ушли облачной модели")
		}
		if !strings.Contains(second, "withheld from you") || !strings.Contains(second, "Withheld from you") {
			t.Errorf("второй ход без строки о скрытом:\n%.900s", second)
		}
		cards := f.cards(t)
		if len(cards) != 1 || !strings.Contains(cards[0].Code, "Оплатить сервер") {
			t.Fatalf("человек не видит результата в карточке: %+v", cards)
		}
		// и следующий ход: карточка в истории тоже скрыта
		p, err := f.e.chatPrompt(context.Background(), f.chatID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p, "Оплатить сервер") {
			t.Fatal("результат закрытого сервера ушёл облачной модели вместе с историей")
		}
		if !strings.Contains(p, "результат скрыт от облачной модели") {
			t.Error("в истории нет пометки о скрытом результате")
		}
	})
	t.Run("доверенная", func(t *testing.T) {
		f := newMCPChatFix(t, true, callToday, "Две задачи.")
		setSecret(f)
		f.ask(t, "какие задачи на сегодня в Planner")
		if !strings.Contains(f.model.Prompts()[1], "Оплатить сервер") {
			t.Error("доверенная модель не получила ответ секретного сервера")
		}
	})
}

// Чувствительный сервер и режим «спрашивать»: облачной модели закрыт, пока
// человек не разрешил; вопрос приходит уведомлением. Проверяется сам блок
// промпта: остальной промпт чата в этом режиме дверь облачной модели и так не
// пропускает (в подсказках есть слова-приметы вроде «оплатить» и «личный»).
func TestMCPSensitiveServerAsksConsent(t *testing.T) {
	f := newMCPChatFix(t, false, "ок")
	m, _ := f.st.MCPServer("planner")
	m.Class = store.ClassSensitive
	_ = f.st.SaveMCPServer(m)
	if err := f.e.SetSensitiveMode("ask"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	got := f.e.mcpChatPrompt(ctx, f.chatID, "какие задачи на сегодня в Planner", true)
	if strings.Contains(got, "Tools of «planner»") || strings.Contains(got, "- planner —") {
		t.Errorf("чувствительный сервер открыт облачной модели без согласия:\n%s", got)
	}
	if !strings.Contains(got, "Withheld from you") {
		t.Errorf("модель не узнала, что сервер от неё скрыт:\n%s", got)
	}
	if _, open := f.st.OpenNotificationByKey("consent:mcp:planner"); !open {
		t.Error("вопроса о согласии нет")
	}
	// человек разрешил — сервер виден
	if err := f.e.DecideConsent("mcp:planner", true); err != nil {
		t.Fatal(err)
	}
	got = f.e.mcpChatPrompt(ctx, f.chatID, "какие задачи на сегодня в Planner", true)
	if !strings.Contains(got, "Tools of «planner»") {
		t.Errorf("после согласия сервер остался закрытым:\n%s", got)
	}
	// доверенной модели согласие не нужно
	if err := f.e.DecideConsent("mcp:planner", false); err != nil {
		t.Fatal(err)
	}
	if got = f.e.mcpChatPrompt(ctx, f.chatID, "какие задачи на сегодня в Planner", false); !strings.Contains(got, "Tools of «planner»") {
		t.Error("доверенная модель не получила сервер")
	}
}

// Скрипт чата зовёт sema.mcp.call: круги те же, подпись вызова — чат; скрипт
// не из чата получает объяснение.
func TestChatScriptCallsMCP(t *testing.T) {
	f := newMCPChatFix(t, true,
		"```sema:Задачи на сегодня\nconst r = sema.mcp.call('planner', 'task_list_today', {});\nreturn r.text;\n```",
		"Вот задачи.")
	f.ask(t, "какие задачи на сегодня в Planner")
	calls := f.srv.Calls()
	if len(calls) != 1 || calls[0].Name != "task_list_today" {
		t.Fatalf("вызовы из скрипта: %+v", calls)
	}
	log, _ := f.st.MCPLog("planner", 5)
	if len(log) != 1 || log[0].Who != "чат · Задачи" {
		t.Fatalf("подпись вызова из скрипта: %+v", log)
	}
	if second := f.model.Prompts(); len(second) != 2 || !strings.Contains(second[1], "Оплатить сервер") {
		t.Error("результат скрипта не дошёл до второго хода")
	}

	// не из чата: расписание, кнопка «Запустить»
	res := f.e.RunScript(llm.WithConsumer(context.Background(), "script:5"),
		"return sema.mcp.call('planner', 'task_list_today', {}).text;")
	if res.Err == nil || !strings.Contains(res.ErrText+res.Err.Error(), "для расписания оформите это модулем") {
		t.Fatalf("скрипт не из чата: %+v", res)
	}
	if len(f.srv.Calls()) != 1 {
		t.Error("скрипт не из чата дошёл до сервера")
	}
	// сервер, закрытый чату, из скрипта чата недоступен
	m, _ := f.st.MCPServer("planner")
	m.Chat = false
	_ = f.st.SaveMCPServer(m)
	res = f.e.RunScript(llm.WithConsumer(context.Background(), fmt.Sprintf("chat:%d", f.chatID)),
		"return sema.mcp.call('planner', 'task_list_today', {}).text;")
	if res.Err == nil || !strings.Contains(res.ErrText+res.Err.Error(), "не открыт чату") {
		t.Fatalf("сервер, закрытый чату, из скрипта: %+v", res)
	}
}

// Вторая реплика с блоком после «Do not write another script block» не
// запрещает MCP: подсказка второго хода это говорит прямо.
func TestScriptResultsPromptKeepsBlocksAllowed(t *testing.T) {
	p := scriptResultsPrompt([]ToolCard{{Title: "x", Result: "1"}})
	if !strings.Contains(p, "sema-mcp") || strings.Contains(p, "Do not write another script block.") {
		t.Errorf("второй ход после скрипта запрещает все блоки:\n%s", p)
	}
}

// Второй ход тоже может прислать sema-mcp: чтение выполняется, результат —
// карточкой, третьего хода нет; изменяющее — карточка с кнопкой.
func TestMCPChatSecondTurnBlock(t *testing.T) {
	f := newMCPChatFix(t, true, callToday,
		"Сначала посмотрю проекты.\n```sema-mcp:planner\n{\"tool\": \"project_list\"}\n```\nИ заведу задачу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Позвонить\"}}\n```")
	f.ask(t, "какие задачи на сегодня в Planner")
	names := []string{}
	for _, c := range f.srv.Calls() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "task_list_today,project_list" {
		t.Fatalf("вызовы на двух ходах: %v", names)
	}
	if len(f.model.Prompts()) != 2 {
		t.Fatalf("третий ход случился: моделей звали %d раз", len(f.model.Prompts()))
	}
	statuses := []string{}
	for _, c := range f.cards(t) {
		statuses = append(statuses, c.MCP.Tool+":"+c.MCP.Status)
	}
	if got := strings.Join(statuses, " "); got != "task_list_today:executed project_list:executed task_create:pending" {
		t.Fatalf("карточки: %s", got)
	}
	if strings.Contains(f.lastAnswer(t), "sema-mcp") {
		t.Error("блок второго хода остался в тексте ответа")
	}
}

// ---- правила ----

// Прежние круги Planner, выведенные в ответ по именам, похожим на
// настоящие, — то, по чему известным инструментам переносится поведение:
// читающее идёт сразу, изменяющее ждёт кнопки, разрушающее не выполняется.
func TestMCPSeedCirclesPlanner(t *testing.T) {
	read := []string{
		"task_list", "task_get", "task_list_today", "task_list_overdue", "task_list_inbox", "project_list", "project_get",
		"habit_list", "habit_get", "habit_progress_list", "habit_progress_get", "tag_list", "tag_get",
		"kanban_status_list", "kanban_status_get", "checklist_item_list", "checklist_item_get",
		"time_stat_list", "time_stat_get", "task_group_list", "task_group_get", "get_my_context",
		// другие записи имён у других серверов
		"listTasks", "get-task", "search_docs", "wiki_search", "find_page",
	}
	confirm := []string{
		"task_create", "task_update", "task_complete", "task_complete_today", "task_uncomplete", "task_cancel",
		"task_change_column", "task_move", "task_archive", "task_unarchive", "project_create", "project_update",
		"project_archive", "project_unarchive", "habit_create", "habit_update", "habit_progress_create",
		"habit_progress_update", "tag_create", "tag_update", "kanban_status_create", "kanban_status_update",
		"task_group_create", "task_group_update", "time_stat_create", "time_stat_update",
		// «list» и «get» внутри слова — не чтение: раньше `*list*` пускал эти вызовы молча
		"checklist_item_check", "checklist_item_uncheck", "checklist_item_create", "checklist_item_update",
		"set_context", "mark_read", "budget_set", "createTask",
	}
	never := []string{"task_delete", "project_delete_all", "tag_remove", "workspace_destroy", "deleteTask"}
	for _, tool := range read {
		if p := legacyClassifyMCP(mcpSeed.read, mcpSeed.never, tool); p != PolicyAllow {
			t.Errorf("%q: ответ %q, ждали «Разрешить»", tool, p)
		}
	}
	for _, tool := range confirm {
		if p := legacyClassifyMCP(mcpSeed.read, mcpSeed.never, tool); p != PolicyAsk {
			t.Errorf("%q: ответ %q, ждали «Спросить»", tool, p)
		}
	}
	for _, tool := range never {
		if p := legacyClassifyMCP(mcpSeed.read, mcpSeed.never, tool); p != PolicyDeny {
			t.Errorf("%q: ответ %q, ждали «Запретить»", tool, p)
		}
	}
}

// Круги, поставленные первым заполнением, обновляются, а правленные человеком
// остаются; метка не даёт повторить.
func TestMigrateMCPCircles(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	old := &store.MCPServer{Name: "старый", URL: "http://x/mcp", Enabled: true, LegacyRead: mcpSeedV1.read, LegacyNever: mcpSeedV1.never}
	mine := &store.MCPServer{Name: "правленый", URL: "http://y/mcp", Enabled: true,
		LegacyRead: append([]string{"мой_образец"}, mcpSeedV1.read...), LegacyNever: mcpSeedV1.never}
	for _, m := range []*store.MCPServer{old, mine} {
		if err := st.SaveMCPServer(m); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SetSetting(mcpSeedKey, "")
	e.migrateMCPCircles()
	got, _ := st.MCPServer("старый")
	if !sameList(got.LegacyRead, mcpSeed.read) || !sameList(got.LegacyNever, mcpSeed.never) {
		t.Errorf("круги прежнего заполнения не обновились: %v / %v", got.LegacyRead, got.LegacyNever)
	}
	kept, _ := st.MCPServer("правленый")
	if !sameList(kept.LegacyRead, mine.LegacyRead) {
		t.Errorf("правленые человеком круги тронуты: %v", kept.LegacyRead)
	}
	// после метки — не трогаем, даже если списки снова совпали со старыми
	got.LegacyRead, got.LegacyNever = mcpSeedV1.read, mcpSeedV1.never
	_ = st.SaveMCPServer(got)
	e.migrateMCPCircles()
	again, _ := st.MCPServer("старый")
	if !sameList(again.LegacyRead, mcpSeedV1.read) {
		t.Error("метка не удержала повтор переезда")
	}
}

// Переезд на правила по инструментам: известным инструментам — то же
// поведение, что давали круги (чтение → «Разрешить», «никогда» → «Запретить»,
// остальное → «Спросить»), новый инструмент — «Запретить». Повтор безопасен и
// того, что решил человек, не трогает.
func TestMigrateMCPRulesKeepsBehaviorAndDeniesNew(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	tools := []string{"task_list", "task_create", "task_delete", "task_archive", "get_my_context", "checklist_item_check"}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "planner", URL: "http://x/mcp", Enabled: true,
		Tools: tools, LegacyRead: mcpSeed.read, LegacyNever: mcpSeed.never}); err != nil {
		t.Fatal(err)
	}
	e.migrateMCPRules()
	m, _ := st.MCPServer("planner")
	if !m.RulesSet || m.RuleDefault != store.RuleDeny {
		t.Fatalf("правила не заведены или ответ по умолчанию не «Запретить»: %+v", m)
	}
	want := map[string]string{"task_list": PolicyAllow, "get_my_context": PolicyAllow, "task_create": PolicyAsk,
		"task_archive": PolicyAsk, "checklist_item_check": PolicyAsk, "task_delete": PolicyDeny}
	for tool, policy := range want {
		if got, why := ClassifyMCP(m, tool); got != policy {
			t.Errorf("%s: ответ %q (%s), ждали %q", tool, got, why, policy)
		}
	}
	// инструмент, которого сервер раньше не называл, запрещён и помечен новым
	if got, _ := ClassifyMCP(m, "task_brand_new"); got != PolicyDeny {
		t.Errorf("новый инструмент получил %q", got)
	}
	// человек решил иначе — повтор переезда этого не стирает
	rules := map[string]string{}
	for k, v := range m.Rules {
		rules[k] = v
	}
	rules["task_create"] = PolicyAllow
	if err := st.SetMCPRules("planner", rules, store.RuleDeny); err != nil {
		t.Fatal(err)
	}
	e.migrateMCPRules()
	again, _ := st.MCPServer("planner")
	if got, _ := ClassifyMCP(again, "task_create"); got != PolicyAllow {
		t.Errorf("повторный переезд стёр решение человека: %q", got)
	}
}

// ---- разбор блока ----

func TestParseMCPBlock(t *testing.T) {
	cases := []struct {
		name, head, body string
		server, tool     string
		args             string
		err              bool
	}{
		{"обычный", "planner", `{"tool": "task_list", "args": {"maxCount": 3}}`, "planner", "task_list", `{"maxCount":3}`, false},
		{"без args", "planner", `{"tool": "project_list"}`, "planner", "project_list", `null`, false},
		{"пояснение в ограждении", "planner · задачи на сегодня", `{"tool": "task_list_today"}`, "planner", "task_list_today", `null`, false},
		{"JSON на строке ограждения", `planner {"tool": "task_list"}`, "", "planner", "task_list", `null`, false},
		{"другие ключи", "planner", `{"name": "task_get", "arguments": {"id": "t1"}}`, "planner", "task_get", `{"id":"t1"}`, false},
		{"инструмент в ограждении, тело — аргументы", "planner task_list", `{"maxCount": 2}`, "planner", "task_list", `{"maxCount":2}`, false},
		{"инструмент в ограждении, тела нет", "planner get_my_context", "", "planner", "get_my_context", `null`, false},
		{"аргументы строкой", "planner", `{"tool": "task_get", "args": "{\"id\":\"t1\"}"}`, "planner", "task_get", `{"id":"t1"}`, false},
		{"вложенное ограждение json", "planner", "```json\n{\"tool\": \"task_list\"}\n", "planner", "task_list", `null`, false},
		{"битый JSON", "planner", `{"tool": `, "planner", "", "", true},
		{"нет инструмента", "planner", `{"args": {}}`, "planner", "", "", true},
		{"аргументы не объект", "planner", `{"tool": "x", "args": [1]}`, "planner", "x", "", true},
		{"пусто", "planner", "", "planner", "", "", true},
	}
	for _, c := range cases {
		got := parseMCPBlock(c.head, c.body)
		if got.Server != c.server || got.Tool != c.tool || (got.Err != "") != c.err {
			t.Errorf("%s: сервер %q инструмент %q ошибка %q", c.name, got.Server, got.Tool, got.Err)
			continue
		}
		if !c.err {
			raw, _ := json.Marshal(got.Args)
			if string(raw) != c.args {
				t.Errorf("%s: аргументы %s, ждали %s", c.name, raw, c.args)
			}
		}
	}
}

func TestResolveMCPServer(t *testing.T) {
	all := []*store.MCPServer{{Name: "planner"}, {Name: "wiki"}}
	for raw, want := range map[string]string{"planner": "planner", "Planner": "planner",
		"plan": "planner", "wiki": "wiki", "WIKI": "wiki", "": "", "трекер": ""} {
		got := resolveMCPServer(raw, all)
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != want {
			t.Errorf("resolveMCPServer(%q) = %q, ждали %q", raw, name, want)
		}
	}
}

// ---- сжатие схемы и бюджет ----

func TestMCPArgsCompact(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
		"title":{"type":"string","description":"Task title"},
		"priority":{"type":"integer","enum":[0,1,2],"description":"0/1/2 = HIGH/NORMAL/LOW"},
		"state":{"type":"string","enum":["pinned","unpinned"]},
		"tags":{"type":"array","items":{"type":"string"}},
		"due":{"type":["string","null"]},
		"parent":{"anyOf":[{"type":"string"},{"type":"integer"}]},
		"loose":{}
	},"required":["title"]}`)
	line := mcpToolLine(store.MCPTool{Name: "task_create", Desc: "Create a task", Schema: schema}, mcpLevelTyped)
	want := `  - task_create(title: string, due?: string|null, loose?: any, parent?: string|integer, priority?: 0|1|2, state?: "pinned"|"unpinned", tags?: array<string>) — Create a task` + "\n"
	if line != want {
		t.Errorf("сигнатура:\n%q\nждали:\n%q", line, want)
	}
	// самая скупая ступень — только имена
	if got := mcpToolLine(store.MCPTool{Name: "x", Desc: "d", Schema: schema}, mcpLevelNames); !strings.HasPrefix(got, "  - x(title, due?, loose?, parent?, priority?, state?, tags?)") || strings.Contains(got, "—") {
		t.Errorf("ступень имён: %q", got)
	}
	// подробная — с пояснениями к аргументам
	if got := mcpToolLine(store.MCPTool{Name: "x", Desc: "d", Schema: schema}, mcpLevelFull); !strings.Contains(got, "priority: 0/1/2 = HIGH/NORMAL/LOW") {
		t.Errorf("ступень с пояснениями: %q", got)
	}
	// нет схемы — скобки пустые
	if got := mcpToolLine(store.MCPTool{Name: "project_list", Desc: "List projects"}, mcpLevelTyped); got != "  - project_list() — List projects\n" {
		t.Errorf("без схемы: %q", got)
	}
}

// Бюджет знаков: сперва подробность падает, потом инструменты режутся целиком
// с названным остатком; обрывка сигнатуры не бывает.
func TestMCPToolsBlockBudget(t *testing.T) {
	var info []store.MCPTool
	for i := 0; i < 52; i++ {
		info = append(info, store.MCPTool{
			Name: fmt.Sprintf("entity%02d_operation", i),
			Desc: strings.Repeat("Длинное описание инструмента. ", 8),
			Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"` +
				strings.Repeat("идентификатор ", 6) + `"},"limit":{"type":"integer"}},"required":["id"]}`),
		})
	}
	m := &store.MCPServer{Name: "big", ToolInfo: info}
	size := func(s string) int { return len([]rune(s)) }

	rich := mcpToolsBlock(m, 100000)
	if !strings.Contains(rich, "id: идентификатор") || strings.Contains(rich, "did not fit") {
		t.Errorf("при большом бюджете нужны пояснения к аргументам и все инструменты")
	}
	mid := mcpToolsBlock(m, size(rich)-1)
	if size(mid) >= size(rich) || !strings.Contains(mid, "entity51_operation(") || strings.Contains(mid, "did not fit") {
		t.Errorf("средний бюджет: подробность должна упасть, а инструменты остаться (%d знаков)", size(mid))
	}
	tight := mcpToolsBlock(m, 1800)
	if size(tight) > 1800 {
		t.Errorf("блок вылез за бюджет: %d > 1800", size(tight))
	}
	if !strings.Contains(tight, "more tools did not fit") || strings.Contains(tight, "entity51_operation") {
		t.Errorf("остаток не назван или хвост не отрезан:\n%s", tight)
	}
	for _, line := range strings.Split(strings.TrimSpace(tight), "\n") {
		if strings.HasPrefix(line, "  - ") && !strings.Contains(line, ")") {
			t.Errorf("обрубок сигнатуры: %q", line)
		}
	}
}

// Закрытая карточка в истории: без тела, с пометкой; чужие карточки не трогаются.
func TestHideClosedMCP(t *testing.T) {
	card := ToolCard{Title: "planner · task_list", Code: "секретное тело", MCP: &MCPCard{Server: "planner", Tool: "task_list", Status: "executed"}}
	raw, _ := json.Marshal(card)
	got := hideClosedMCP(string(raw), map[string]string{"planner": "секретное"})
	if strings.Contains(got, "секретное тело") || !strings.Contains(got, "скрыт от облачной модели") {
		t.Errorf("карточка закрытого сервера: %s", got)
	}
	if hideClosedMCP(string(raw), nil) != string(raw) {
		t.Error("карточка тронута без закрытого сервера")
	}
	other, _ := json.Marshal(ToolCard{Title: "ssh", Code: "вывод", SSH: &SSHCard{Host: "x"}})
	if hideClosedMCP(string(other), map[string]string{"planner": "секретное"}) != string(other) {
		t.Error("чужая карточка тронута")
	}
}

// Пометка «часть скрыта от вас» не должна сама поднимать класс промпта: дверь
// читает промпт целиком, и облачная модель получала отказ на собственную пометку.
func TestCutNoticeDoesNotTripGate(t *testing.T) {
	for _, n := range [][2]int{{3, 0}, {0, 2}, {2, 1}} {
		text := cutNotice(n[0], n[1], []string{"MCP «planner»"})
		if class, why := TextClass(text); class != store.ClassPlain {
			t.Errorf("cutNotice(%d, %d) читается как %q (%s):\n%s", n[0], n[1], class, why, text)
		}
	}
}

// «Выполнить и разрешить впредь»: вызов выполняется, инструменту этого
// сервера — правило «Разрешить», и следующий такой вызов идёт без кнопки.
// Соседний изменяющий инструмент по-прежнему ждёт.
func TestMCPApproveAlwaysAllowsTool(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Заведу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Первая\"}}\n```", "ок")
	f.ask(t, "заведи задачу в Planner")
	cards := f.cards(t)
	if len(cards) != 1 || cards[0].MCP.Status != "pending" {
		t.Fatalf("карточки: %+v", cards)
	}
	if _, err := f.e.ApproveMCPAlways(context.Background(), cards[0].MCP.PendingID); err != nil {
		t.Fatal(err)
	}
	m, _ := f.st.MCPServer("planner")
	if m.Rules["task_create"] != store.RuleAllow {
		t.Fatalf("правило task_create: %q", m.Rules["task_create"])
	}
	if m.Rules["task_archive"] == store.RuleAllow {
		t.Error("разрешилось больше, чем один инструмент")
	}
	res := f.e.RequestMCPFrom(context.Background(), MCPOrigin{Who: "проба"}, "planner", "task_create", map[string]any{"title": "Вторая"})
	if res.PendingID != 0 || res.Err != "" {
		t.Fatalf("второй вызов не пошёл сам: %+v", res)
	}
	if res := f.e.RequestMCPFrom(context.Background(), MCPOrigin{Who: "проба"}, "planner", "task_archive", map[string]any{"id": "T-1"}); res.PendingID == 0 {
		t.Errorf("task_archive пошёл без кнопки: %+v", res)
	}
	calls := 0
	for _, c := range f.srv.Calls() {
		if c.Name == "task_create" {
			calls++
		}
	}
	if calls != 2 {
		t.Errorf("task_create на сервере %d раз, ждали два", calls)
	}
}
