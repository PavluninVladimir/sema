package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/mcp"
	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ошибки инструментов MCP: суть первой строкой, исправление вызова, подсказка
// про формат. Случай — Planner 30 сентября 2026: схема говорит «ISO 8601
// date string», а сервер требует дату со временем и поясом.

const plannerGist = "Value must be a calendar-correct ISO-8601 datetime with explicit timezone (Z or ±HH:MM, e.g. 2026-07-16T12:00:00.000Z or 2026-07-16T15:00:00+03:00)"

// plannerRejection — ответ сервера как в mcp_log 46–47.
const plannerRejection = `{"code":"INVALID_FIELD","message":"REST request failed with HTTP 400.","details":{"status":400,"raw":{"statusCode":400,"message":["` +
	plannerGist + `"],"error":"Bad Request"}}}`

// ---- разбор ответа и вид ошибки ----

func TestParseMCPErrorText(t *testing.T) {
	cases := []struct {
		name, text string
		gist, code string
		status     int
		structured bool
	}{
		{"Planner: суть на дне", plannerRejection, plannerGist, "INVALID_FIELD", 400, true},
		{"NestJS: общее слово не в счёт", `{"statusCode":400,"message":["deadline must be a valid ISO 8601 date string","title must be a string"],"error":"Bad Request"}`,
			"deadline must be a valid ISO 8601 date string; title must be a string", "", 400, true},
		{"Pydantic: поле из loc", `{"detail":[{"type":"datetime_parsing","loc":["body","deadline"],"msg":"Input should be a valid datetime","input":"2026-10-01"}]}`,
			"body.deadline: Input should be a valid datetime", "", 0, true},
		{"RFC 7807", `{"type":"https://e/x","title":"Bad Request","status":400,"detail":"deadline is malformed"}`, "deadline is malformed", "", 400, true},
		{"Google-стиль: числовой code — статус", `{"error":{"code":400,"message":"Invalid value at 'deadline'","status":"INVALID_ARGUMENT"}}`,
			"Invalid value at 'deadline'", "", 400, true},
		{"поле рядом с сообщением", `{"errors":[{"field":"deadline","message":"must be a datetime"},{"field":"id","message":"must be a uuid"}]}`,
			"deadline: must be a datetime; id: must be a uuid", "", 0, true},
		{"приписка перед JSON", `Error: {"message":"deadline is required","code":"VALIDATION_ERROR"}`, "deadline is required", "VALIDATION_ERROR", 0, true},
		{"простой текст — первая строка", "Task T-9 not found\nat service.get (x.js:1)", "Task T-9 not found", "", 0, false},
		{"JSON без слов", `{"ok":false,"code":42}`, "", "", 0, true},
		{"только общие слова", `{"message":"Bad Request"}`, "Bad Request", "", 0, true},
	}
	for _, c := range cases {
		got := parseMCPErrorText(c.text)
		if got.Gist != c.gist || got.Code != c.code || got.Status != c.status || got.Structured != c.structured {
			t.Errorf("%s:\nсуть %q (ждали %q)\nкод %q (ждали %q)\nстатус %d (ждали %d), JSON %v (ждали %v)",
				c.name, got.Gist, c.gist, got.Code, c.code, got.Status, c.status, got.Structured, c.structured)
		}
	}
}

// Отказ по аргументам узнаётся по статусу, коду и словам; сеть, 5xx, вход,
// «не найдено» и перегрузка — нет.
func TestMCPArgsRejected(t *testing.T) {
	cases := []struct {
		name            string
		status, numCode int
		code, text      string
		want            bool
	}{
		{"400", 400, 0, "", "что угодно", true},
		{"422", 422, 0, "", "что угодно", true},
		{"500 сильнее слов", 500, 0, "", "Value must be a string", false},
		{"502", 502, 0, "", "Bad Gateway", false},
		{"401", 401, 0, "", "invalid token", false},
		{"403", 403, 0, "", "must be admin", false},
		{"404 — задачи нет, а не формат", 404, 0, "", "Task not found", false},
		{"408", 408, 0, "", "timeout", false},
		{"429", 429, 0, "", "rate limit", false},
		{"JSON-RPC Invalid params", 0, -32602, "", "Invalid params", true},
		{"JSON-RPC Method not found", 0, -32601, "", "Method not found", false},
		{"код INVALID_FIELD без статуса", 0, 0, "INVALID_FIELD", "x", true},
		{"код VALIDATION_ERROR", 0, 0, "VALIDATION_ERROR", "x", true},
		{"код AUTH_INVALID", 0, 0, "AUTH_INVALID", "x", false},
		{"код NOT_FOUND", 0, 0, "NOT_FOUND", "must be found", false},
		{"слова: must be", 0, 0, "", "projectId must be a UUID", true},
		{"слова: is required", 0, 0, "", "title is required", true},
		{"слова: pydantic", 0, 0, "", "1 validation error for Args\ndeadline\n  Input should be a valid datetime", true},
		{"слова: Invalid API key — вход", 0, 0, "", "Invalid API key", false},
		{"слова: не найдено", 0, 0, "", "Project not found: must be created first", false},
		{"слова: статус 502 внутри текста", 0, 0, "", "Request failed with status code 502, expected JSON", false},
		{"слова: HTTP 503 внутри текста", 0, 0, "", "upstream responded HTTP 503: invalid response", false},
		{"слова: сессия", 0, 0, "", "Invalid session: expected a fresh login", false},
		{"слова: ничего", 0, 0, "", "Something went wrong", false},
		{"пусто", 0, 0, "", "", false},
	}
	for _, c := range cases {
		if got := mcpArgsRejected(c.status, c.numCode, c.code, c.text); got != c.want {
			t.Errorf("%s: %v, ждали %v", c.name, got, c.want)
		}
	}
}

// Первая строка — суть со словами «отклонил аргументы» и кодом; сырой ответ
// ниже режется по рунам с названным остатком, а суть не режется никогда.
func TestDescribeMCPErrorGistFirst(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &store.MCPServer{Name: "planner"}
	f := e.describeMCPError(m, &mcp.ToolError{Text: plannerRejection})
	want := "planner отклонил аргументы: " + plannerGist + " (INVALID_FIELD, HTTP 400)"
	if f.Kind != mcpFailArgs || f.Line != want {
		t.Fatalf("вид %q, строка:\n%s\nждали:\n%s", f.Kind, f.Line, want)
	}
	text := f.Text(0)
	if first, _, _ := strings.Cut(text, "\n"); first != want {
		t.Errorf("первая строка не суть: %q", first)
	}
	if !strings.Contains(text, `"code":"INVALID_FIELD"`) {
		t.Errorf("сырого ответа под сутью нет:\n%s", text)
	}
	// предел режет только сырой ответ
	tiny := f.Text(40)
	if first, _, _ := strings.Cut(tiny, "\n"); first != want {
		t.Errorf("суть обрезана пределом сырого ответа: %q", first)
	}
	if !strings.Contains(tiny, "обрезано: показано 40 из") {
		t.Errorf("остаток сырого ответа не назван:\n%s", tiny)
	}
	// тот же предел у модели: первая строка цела, сырое под ней режется или уходит
	for _, limit := range []int{50, 200, 10} {
		got := clipKeepingFirstLine(text, limit)
		if first, _, _ := strings.Cut(got, "\n"); first != want {
			t.Errorf("предел %d: суть обрезана: %q", limit, first)
		}
	}
	if got := clipKeepingFirstLine(text, 10); !strings.Contains(got, "сырой ответ не показан") {
		t.Errorf("при нехватке места сырой ответ должен уйти с пометкой:\n%s", got)
	}
	if got := clipKeepingFirstLine(text, 1000000); got != text {
		t.Error("в пределе текст не должен меняться")
	}
}

// Статус 4xx с телом и ошибка JSON-RPC разбираются так же, как ошибка
// инструмента; сеть и вход идут как есть и отказом по аргументам не считаются.
func TestDescribeMCPErrorKinds(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &store.MCPServer{Name: "wiki"}
	cases := []struct {
		name string
		err  error
		kind string
		line string
	}{
		{"HTTP 422 с JSON", &mcp.HTTPError{Status: 422, Body: `{"detail":[{"loc":["body","deadline"],"msg":"Input should be a valid datetime"}]}`},
			mcpFailArgs, "wiki отклонил аргументы: body.deadline: Input should be a valid datetime (HTTP 422)"},
		{"HTTP 502", &mcp.HTTPError{Status: 502, Body: "Bad Gateway"}, "", "wiki ответил HTTP 502: Bad Gateway"},
		{"HTTP 500 без тела", &mcp.HTTPError{Status: 500}, "", "wiki ответил HTTP 500"},
		{"JSON-RPC -32602", &mcp.RPCError{Code: -32602, Message: "Invalid params: deadline"}, mcpFailArgs,
			"wiki отклонил аргументы: Invalid params: deadline (код -32602)"},
		{"JSON-RPC -32601", &mcp.RPCError{Code: -32601, Message: "Method not found"}, "", "wiki вернул ошибку: Method not found (код -32601)"},
		{"ошибка инструмента 404", &mcp.ToolError{Text: `{"code":"NOT_FOUND","message":"Task not found","details":{"status":404}}`}, "",
			"wiki вернул ошибку: Task not found (NOT_FOUND, HTTP 404)"},
		{"ошибка инструмента простым текстом", &mcp.ToolError{Text: "Invalid API key"}, "", "wiki вернул ошибку: Invalid API key"},
		{"сеть", errors.New("dial tcp 127.0.0.1:9: connect: connection refused"), "", "dial tcp 127.0.0.1:9: connect: connection refused"},
		{"вход", fmt.Errorf("сервер MCP «wiki» требует вход: откройте «Интеграции»"), "", "сервер MCP «wiki» требует вход: откройте «Интеграции»"},
	}
	for _, c := range cases {
		got := e.describeMCPError(m, c.err)
		if got.Kind != c.kind || got.Line != c.line {
			t.Errorf("%s:\nвид %q (ждали %q)\nстрока %q\nждали   %q", c.name, got.Kind, c.kind, got.Line, c.line)
		}
	}
}

// Секрет в тексте ошибки прячется маской до карточки, журнала и модели: он
// идёт во все три места.
func TestDescribeMCPErrorMasksSecrets(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.AddMemoryRule("mask", "sk-live-ZZ9", ""); err != nil {
		t.Fatal(err)
	}
	f := e.describeMCPError(&store.MCPServer{Name: "s"}, &mcp.ToolError{Text: `{"message":"key sk-live-ZZ9-secret must be rotated"}`})
	if strings.Contains(f.Text(0), "sk-live-ZZ9") {
		t.Fatalf("секрет дошёл до текста ошибки:\n%s", f.Text(0))
	}
}

// Длинная суть режется по пределу настройки с названным остатком, а не молча.
func TestDescribeMCPErrorGistLimitIsSetting(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting(mcpErrorCharsKey, "200"); err != nil {
		t.Fatal(err)
	}
	f := e.describeMCPError(&store.MCPServer{Name: "s"}, &mcp.ToolError{Text: `{"message":"` + strings.Repeat("Ѫ", 500) + `"}`})
	if strings.Count(f.Line, "Ѫ") != 200 || !strings.Contains(f.Line, "ещё 300 знаков") {
		t.Errorf("суть в пределе настройки: %d знаков, остаток назван: %v", strings.Count(f.Line, "Ѫ"), strings.Contains(f.Line, "ещё 300"))
	}
}

// ---- дата и пояс в подсказке ----

func TestMCPClockText(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		t.Skip("нет базы часовых поясов")
	}
	now := time.Date(2026, 9, 30, 21, 30, 0, 0, loc)
	got := mcpClockText(now, "Europe/Samara")
	for _, want := range []string{"Wednesday 2026-09-30", "21:30", "Europe/Samara (UTC+04:00)", "2026-10-01T09:00:00+04:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("в строке про дату нет %q:\n%s", want, got)
		}
	}
}

// ---- сценарии в чате ----

// taskUpdateTool — инструмент, у которого схема говорит «date string», а сервер
// требует дату со временем и поясом.
func taskUpdateTool() mcptest.ToolDef {
	return mcptest.ToolDef{Name: "task_update", Description: "Update a task", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":       map[string]any{"type": "string", "description": "Task id"},
			"deadline": map[string]any{"type": "string", "description": "Deadline, ISO 8601 date string"},
		},
		"required": []any{"id"},
	}}
}

// newMCPFixFix — стенд Planner со строгим task_update: дата без времени
// отклоняется так же, как на живом сервере, дата с поясом принимается.
func newMCPFixFix(t *testing.T, answers ...string) *mcpChatFix {
	t.Helper()
	return newMCPFixFixTrust(t, true, answers...)
}

// newMCPFixFixTrust — то же, но можно выбрать модель чата: доверенную («дом») или
// облачную, которой закрытое не показывается.
func newMCPFixFixTrust(t *testing.T, trusted bool, answers ...string) *mcpChatFix {
	t.Helper()
	f := newMCPChatFix(t, trusted, answers...)
	f.srv.ToolDefs = append(plannerTools(), taskUpdateTool(),
		mcptest.ToolDef{Name: "task_get", Description: "Get one task", Schema: map[string]any{"type": "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}}}})
	f.srv.CallResult = func(name string, args map[string]any) (string, bool) {
		switch name {
		case "task_update":
			if d, _ := args["deadline"].(string); d != "" && !strings.Contains(d, "T") {
				return plannerRejection, true
			}
			return `{"id":"T-1","deadline":"2026-10-01T09:00:00+04:00"}`, false
		case "task_list":
			if p, _ := args["projectId"].(string); p == "xx" {
				return `{"code":"INVALID_FIELD","message":"Validation failed","details":{"status":400,"raw":{"message":["projectId must be a UUID"],"error":"Bad Request"}}}`, true
			}
			return `[{"id":"t1","title":"Оплатить сервер"}]`, false
		}
		return "вызван " + name, false
	}
	if err := f.e.CheckMCP(context.Background(), "planner"); err != nil {
		t.Fatal(err)
	}
	// то, что сервер назвал после переезда, — «новое» и запрещено; решает человек
	decideMCP(t, f.st, "planner", map[string]string{"task_update": PolicyAsk, "task_get": PolicyAllow})
	if err := f.st.SetSetting("app.timezone", "Europe/Samara"); err != nil {
		t.Fatal(err)
	}
	return f
}

const (
	updateDateOnly = "Ставлю срок.\n```sema-mcp:planner\n{\"tool\": \"task_update\", \"args\": {\"id\": \"T-1\", \"deadline\": \"2026-10-01\"}}\n```"
	updateDateTime = "Исправляю формат.\n```sema-mcp:planner\n{\"tool\": \"task_update\", \"args\": {\"id\": \"T-1\", \"deadline\": \"2026-10-01T09:00:00+04:00\"}}\n```"
	updateAgain    = "Пробую другую дату.\n```sema-mcp:planner\n{\"tool\": \"task_update\", \"args\": {\"id\": \"T-1\", \"deadline\": \"2026-10-02\"}}\n```"
	askDeadline    = "поставь срок задаче T-1 в Planner на завтра"
)

// settle — ждём, пока Сёма продолжит после кнопки: модель позвана нужное число
// раз и ничего не думает.
func (f *mcpChatFix) settle(t *testing.T, calls int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("модель позвана %d раз и закончила", calls), func() bool {
		f.e.thinkingMu.Lock()
		thinking := f.e.thinking[f.chatID]
		f.e.thinkingMu.Unlock()
		return len(f.model.Prompts()) == calls && !thinking
	})
}

// approveOnly — нажать «Выполнить» на единственном ждущем вызове.
func (f *mcpChatFix) approveOnly(t *testing.T) error {
	t.Helper()
	pend, err := f.st.MCPPendingList()
	if err != nil || len(pend) != 1 {
		t.Fatalf("ждущих вызовов %d, ждали один: %v %+v", len(pend), err, pend)
	}
	_, err = f.e.ApproveMCP(context.Background(), pend[0].ID, true)
	return err
}

// lineAfter — строка, следующая за первой, где встретилось слово.
func lineAfter(text, marker string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.Contains(l, marker) && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}

// Живой случай целиком: отказ по формату даты → в карточке и в продолжении
// суть первой строкой → модель просит исправить → исправленный вызов снова
// идёт на кнопку → успех; подсказка остаётся у сервера.
func TestMCPArgRejectedFixedAndRemembered(t *testing.T) {
	f := newMCPFixFix(t, updateDateOnly, updateDateTime, "Срок поставлен: завтра, 9:00.")
	f.ask(t, askDeadline)
	if n := len(f.srv.Calls()); n != 0 {
		t.Fatalf("изменяющий вызов ушёл без кнопки: %d", n)
	}
	// в первом ходе подсказки про формат ещё нет, а дата и пояс уже есть
	first := f.model.Prompts()[0]
	// день недели в строке — сегодняшний: тест, зашитый на «Wednesday»,
	// падал в любой другой день (1 октября 2026)
	for _, want := range []string{"Today is ", "Europe/Samara (UTC+04:00)", "explicit offset", "If a tool rejects your ARGUMENTS"} {
		if !strings.Contains(first, want) {
			t.Errorf("в первом промпте нет %q", want)
		}
	}
	if strings.Contains(first, "⚠ сервер требует:") {
		t.Error("подсказка про формат появилась до первого отказа")
	}

	// «Выполнить» → сервер отказал
	err := f.approveOnly(t)
	if err == nil || !strings.HasPrefix(err.Error(), "planner отклонил аргументы: Value must be a calendar-correct") {
		t.Fatalf("ошибка подтверждённого вызова: %v", err)
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards[0].MCP.Status != "error" || cards[0].MCP.FailKind != mcpFailArgs {
		t.Fatalf("карточка после отказа: %+v", cards)
	}
	if first, _, _ := strings.Cut(cards[0].Err, "\n"); first != "planner отклонил аргументы: "+plannerGist+" (INVALID_FIELD, HTTP 400)" {
		t.Errorf("первая строка карточки — не суть: %q", first)
	}
	if !strings.Contains(cards[0].Err, `"statusCode":400`) {
		t.Errorf("под сутью нет сырого ответа: %q", cards[0].Err)
	}
	f.settle(t, 2)

	// продолжение после кнопки: модель видит суть целиком и просьбу исправить
	second := f.model.Prompts()[1]
	for _, want := range []string{
		"[sema-mcp call «planner» · task_update",
		"failed — planner отклонил аргументы: " + plannerGist + " (INVALID_FIELD, HTTP 400)",
		"rejected the ARGUMENTS of your call to task_update",
		"send the call again as ONE sema-mcp block",
		"Retry 1 of 1",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("в продолжении после отказа нет %q\n…%s", want, promptTail(second, 2500))
		}
	}
	if strings.Contains(second, `"message": [ "V`) || strings.Contains(second, `{"title":"planner · task_update"`) {
		t.Error("модель снова получила обрезок или сырой JSON карточки вместо сути")
	}
	if !strings.HasSuffix(strings.TrimSpace(second), "Sema:") || strings.LastIndex(second, "Retry 1 of 1") < strings.LastIndex(second, "Conversation:") {
		t.Error("просьба исправить вызов должна стоять в конце промпта, после переписки")
	}
	// подсказка под сигнатурой инструмента уже в этом же промпте
	if got := lineAfter(second, "task_update(id: string, deadline?: string)"); !strings.Contains(got, "⚠ сервер требует: Value must be a calendar-correct ISO-8601 datetime") {
		t.Errorf("под сигнатурой task_update нет подсказки, там %q", got)
	}

	// исправленный вызов снова ждёт человека — и проходит
	waitFor(t, "исправленный вызов ждёт кнопки", func() bool {
		p, _ := f.st.MCPPendingList()
		return len(p) == 1
	})
	if err := f.approveOnly(t); err != nil {
		t.Fatalf("исправленный вызов: %v", err)
	}
	f.settle(t, 3)
	calls := f.srv.Calls()
	if len(calls) != 2 || fmt.Sprint(calls[1].Args["deadline"]) != "2026-10-01T09:00:00+04:00" {
		t.Fatalf("вызовы сервера: %+v", calls)
	}
	if !strings.Contains(f.lastAnswer(t), "Срок поставлен") {
		t.Errorf("итог: %q", f.lastAnswer(t))
	}
	if third := f.model.Prompts()[2]; strings.Contains(third, "rejected the ARGUMENTS of your call to") {
		t.Error("после успешного вызова просьба исправить осталась в промпте")
	}

	// подсказка осталась у сервера, и успех её не стёр: формат не изменился
	m, _ := f.st.MCPServer("planner")
	h, ok := m.ToolHints["task_update"]
	if !ok || h.Gist != plannerGist {
		t.Fatalf("подсказка не записана или не та: %+v", m.ToolHints)
	}
	// следующий разговор: сразу верный формат, без отказа
	chat2, _ := f.st.CreateChat("Ещё задачи")
	got := f.e.mcpChatPrompt(context.Background(), chat2, "что у меня в Planner", false)
	if line := lineAfter(got, "task_update(id: string, deadline?: string)"); !strings.Contains(line, "⚠ сервер требует: "+plannerGist) {
		t.Errorf("в следующем разговоре подсказки нет:\n%s", got)
	}
}

// Второй отказ подряд — повтора нет: модель получает просьбу ответить честно,
// а не исправить в третий раз.
func TestMCPArgRejectedTwiceStopsRetrying(t *testing.T) {
	f := newMCPFixFix(t, updateDateOnly, updateAgain, "Сервер просит дату со временем и поясом; две попытки не прошли, поставьте срок руками или скажите время.")
	f.ask(t, askDeadline)
	if err := f.approveOnly(t); err == nil {
		t.Fatal("первый отказ должен вернуть ошибку")
	}
	f.settle(t, 2)
	waitFor(t, "второй вызов ждёт кнопки", func() bool {
		p, _ := f.st.MCPPendingList()
		return len(p) == 1
	})
	if err := f.approveOnly(t); err == nil {
		t.Fatal("второй отказ должен вернуть ошибку")
	}
	f.settle(t, 3)

	prompts := f.model.Prompts()
	if !strings.Contains(prompts[1], "rejected the ARGUMENTS of your call to task_update") {
		t.Error("после первого отказа модель не попросили исправить вызов")
	}
	third := prompts[2]
	if strings.Contains(third, "rejected the ARGUMENTS of your call to") || strings.Contains(third, "send the call again as ONE") {
		t.Errorf("после второго отказа подряд модель снова просят повторить:\n…%s", promptTail(third, 1500))
	}
	if !strings.Contains(third, "has rejected the arguments of task_update 2 times in a row") ||
		!strings.Contains(third, "tell the user honestly what the server requires") {
		t.Errorf("второй отказ: нет просьбы ответить честно:\n…%s", promptTail(third, 1500))
	}
	if !strings.Contains(f.lastAnswer(t), "две попытки не прошли") {
		t.Errorf("честного ответа нет: %q", f.lastAnswer(t))
	}
	if p, _ := f.st.MCPPendingList(); len(p) != 0 {
		t.Errorf("после честного ответа остались ждущие вызовы: %+v", p)
	}
	if n := len(f.srv.Calls()); n != 2 {
		t.Errorf("на сервер ушло %d вызовов, ждали два", n)
	}
}

// Счёт ведётся по карточкам: новая просьба человека начинает его заново, а
// удачный вызов того же инструмента и чужой инструмент его не продолжают.
func TestMCPArgFailStreak(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	chat, _ := st.CreateChat("счёт")
	put := func(role, content string) {
		t.Helper()
		if err := st.AddChatMsg(chat, role, content); err != nil {
			t.Fatal(err)
		}
	}
	card := func(tool, status, kind string) {
		raw := mustJSON(t, ToolCard{Title: "s · " + tool, MCP: &MCPCard{Server: "s", Tool: tool, Status: status, FailKind: kind}})
		put("tool", raw)
	}
	streak := func() int { return e.mcpArgFailStreak(chat, "s", "task_update") }

	put("user", "поставь срок")
	card("task_update", "error", mcpFailArgs)
	if streak() != 1 {
		t.Errorf("один отказ: %d", streak())
	}
	put("assistant", "Не вышло.")
	card("task_get", "executed", "") // чужой инструмент счёт не трогает
	card("task_update", "pending", "")
	card("task_update", "error", mcpFailArgs)
	if streak() != 2 {
		t.Errorf("два отказа подряд: %d", streak())
	}
	put("user", "попробуй ещё раз")
	if streak() != 0 {
		t.Errorf("новая просьба человека должна начинать счёт заново: %d", streak())
	}
	card("task_update", "error", mcpFailArgs)
	card("task_update", "executed", "")
	if streak() != 0 {
		t.Errorf("удачный вызов обрывает счёт: %d", streak())
	}
	card("task_update", "error", "") // отказ другого рода
	card("task_update", "error", mcpFailArgs)
	if streak() != 1 {
		t.Errorf("отказ другого рода обрывает счёт: %d", streak())
	}
}

// Читающий вызов: отказ по аргументам идёт вторым ходом — суть первой строкой и
// просьба исправить; исправленный блок второго хода выполняется.
func TestMCPReadCallArgRejectedSecondTurn(t *testing.T) {
	f := newMCPFixFix(t,
		"Смотрю.\n```sema-mcp:planner\n{\"tool\": \"task_list\", \"args\": {\"projectId\": \"xx\"}}\n```",
		"Исправил.\n```sema-mcp:planner\n{\"tool\": \"task_list\", \"args\": {\"projectId\": \"9b2f6c1e-0000-4000-8000-000000000001\"}}\n```")
	f.ask(t, "какие задачи в проекте xx в Planner")
	prompts := f.model.Prompts()
	// третий ход — по результату исправленного вызова: читающие вызовы идут
	// шагами (chat-tool-steps). Раньше его не было, и модель не видела, что
	// исправленный вызов вернул
	if len(prompts) != 3 {
		t.Fatalf("модель звали %d раз, ждали три хода", len(prompts))
	}
	if !strings.Contains(prompts[2], "MCP results") {
		t.Errorf("третий ход без результата исправленного вызова\n…%s", promptTail(prompts[2], 1200))
	}
	second := prompts[1]
	for _, want := range []string{
		"- MCP server «planner», tool task_list: не вышло — planner отклонил аргументы: projectId must be a UUID (INVALID_FIELD, HTTP 400)",
		"  → The server «planner» rejected the ARGUMENTS of your call to task_list",
		"Retry 1 of 1",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("во втором ходе нет %q\n…%s", want, promptTail(second, 1800))
		}
	}
	if calls := f.srv.Calls(); len(calls) != 2 || fmt.Sprint(calls[1].Args["projectId"]) != "9b2f6c1e-0000-4000-8000-000000000001" {
		t.Fatalf("исправленный вызов второго хода не ушёл: %+v", calls)
	}
	cards := f.cards(t)
	if len(cards) != 2 || cards[0].MCP.Status != "error" || cards[0].MCP.FailKind != mcpFailArgs || cards[1].MCP.Status != "executed" {
		t.Fatalf("карточки: %+v", cards)
	}
	// читающий инструмент тоже учит подсказке
	if m, _ := f.st.MCPServer("planner"); m.ToolHints["task_list"].Gist != "projectId must be a UUID" {
		t.Errorf("подсказка у task_list: %+v", m.ToolHints)
	}
}

// Подсказку заводит только отказ по аргументам: сеть, 5xx, «не найдено», вход и
// отказ по кругу — не особенность инструмента.
func TestMCPToolHintOnlyForArgRejections(t *testing.T) {
	ctx := context.Background()
	type reply struct {
		status int
		body   string
		text   string
		isErr  bool
	}
	notHints := map[string]reply{
		"HTTP 502":          {status: 502, body: "Bad Gateway"},
		"HTTP 503 с JSON":   {status: 503, body: `{"error":"Service Unavailable","message":"must be retried"}`},
		"5xx внутри ошибки": {text: `{"code":"UPSTREAM_ERROR","message":"REST request failed with HTTP 502.","details":{"status":502,"raw":{"message":"Bad gateway"}}}`, isErr: true},
		"404: задачи нет":   {text: `{"code":"NOT_FOUND","message":"Task not found","details":{"status":404}}`, isErr: true},
		"простым текстом: не найдено": {text: "Task T-9 not found", isErr: true},
		"ключ недействителен":         {text: "Invalid API key", isErr: true},
		"перегрузка":                  {status: 429, body: "Too Many Requests: must wait"},
	}
	for name, r := range notHints {
		f := newMCPFixFix(t, "ок")
		r := r
		f.srv.CallStatus = func(string, map[string]any) (int, string) { return r.status, r.body }
		f.srv.CallResult = func(string, map[string]any) (string, bool) { return r.text, r.isErr }
		res := f.e.RequestMCP(ctx, "тест", "planner", "task_get", map[string]any{"id": "T-9"})
		if res.Err == "" || res.FailKind != "" {
			t.Errorf("%s: ошибка %q, вид %q — ждали ошибку не по аргументам", name, res.Err, res.FailKind)
		}
		if m, _ := f.st.MCPServer("planner"); len(m.ToolHints) != 0 {
			t.Errorf("%s: заведена подсказка %+v", name, m.ToolHints)
		}
	}

	// отказ по кругу и неподключённый сервер
	f := newMCPFixFix(t, "ок")
	if res := f.e.RequestMCP(ctx, "тест", "planner", "task_delete", nil); res.Policy != PolicyDeny || res.Err == "" {
		t.Fatalf("удаление должно быть запрещено: %+v", res)
	}
	if res := f.e.RequestMCP(ctx, "тест", "нет-такого", "task_get", nil); res.Err == "" || res.FailKind != "" {
		t.Fatalf("неизвестный сервер: %+v", res)
	}
	// сеть: сервер не отвечает
	f.srv.MCP.Close()
	if res := f.e.RequestMCP(ctx, "тест", "planner", "task_get", nil); res.Err == "" || res.FailKind != "" {
		t.Fatalf("сервер не отвечает: %+v", res)
	}
	if m, _ := f.st.MCPServer("planner"); len(m.ToolHints) != 0 {
		t.Errorf("отказ по кругу или сети завёл подсказку: %+v", m.ToolHints)
	}

	// а отказ по аргументам — заводит, и по статусу 400 с телом тоже
	g := newMCPFixFix(t, "ок")
	g.srv.CallStatus = func(string, map[string]any) (int, string) {
		return 400, `{"message":["deadline must be a valid ISO 8601 date string"],"error":"Bad Request"}`
	}
	res := g.e.RequestMCP(ctx, "тест", "planner", "task_get", map[string]any{"id": "T-9"})
	if res.FailKind != mcpFailArgs || !strings.HasPrefix(res.Err, "planner отклонил аргументы: deadline must be a valid ISO 8601 date string (HTTP 400)") {
		t.Fatalf("HTTP 400: %+v", res)
	}
	if m, _ := g.st.MCPServer("planner"); m.ToolHints["task_get"].Gist != "deadline must be a valid ISO 8601 date string" {
		t.Errorf("подсказка после HTTP 400: %+v", m.ToolHints)
	}
	// журнал вызовов хранит суть в начале строки, а не обрезок JSON
	log, _ := g.st.MCPLog("planner", 1)
	if len(log) != 1 || log[0].OK || !strings.HasPrefix(log[0].Result, "planner отклонил аргументы: deadline must be") {
		t.Errorf("журнал вызовов: %+v", log)
	}
}

// Подсказка живёт `mcp.tool_hint_days` дней, стирается кнопкой, выключается
// нулём; срок считается от последнего отказа.
func TestMCPToolHintExpiresAndForgets(t *testing.T) {
	f := newMCPFixFix(t, "ок")
	ctx := context.Background()
	showing := func() bool {
		return strings.Contains(f.e.mcpChatPrompt(ctx, f.chatID, "что в Planner", false), "⚠ сервер требует: "+plannerGist)
	}
	hint := func(age time.Duration) {
		t.Helper()
		if err := f.st.SetMCPToolHint("planner", "task_update", plannerGist, time.Now().Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	hint(10 * 24 * time.Hour)
	if !showing() {
		t.Fatal("подсказка десятидневной давности по умолчанию (30 дней) должна идти в промпт")
	}
	hint(31 * 24 * time.Hour)
	if showing() {
		t.Error("подсказка за сроком (30 дней) идёт в промпт")
	}
	// срок — настройка
	if err := f.st.SetSetting(mcpToolHintDaysKey, "60"); err != nil {
		t.Fatal(err)
	}
	if !showing() {
		t.Error("срок 60 дней не подействовал на подсказку 31 день назад")
	}
	if err := f.st.SetSetting(mcpToolHintDaysKey, "0"); err != nil {
		t.Fatal(err)
	}
	if showing() {
		t.Error("срок 0 должен выключить подсказки")
	}
	if err := f.st.SetSetting(mcpToolHintDaysKey, "30"); err != nil {
		t.Fatal(err)
	}
	// свежий отказ обновляет срок
	hint(31 * 24 * time.Hour)
	hint(time.Minute)
	if !showing() {
		t.Error("новая подсказка не обновила срок")
	}
	// кнопка «Забыть»
	if n, err := f.st.ForgetMCPToolHints("planner", "task_update"); err != nil || n != 1 {
		t.Fatalf("забыть подсказку: %d, %v", n, err)
	}
	if showing() {
		t.Error("забытая подсказка осталась в промпте")
	}
	if n, _ := f.st.ForgetMCPToolHints("planner", "task_update"); n != 0 {
		t.Errorf("забыть нечего, а счёт %d", n)
	}
}

// Длина сути в подсказке — настройка, остаток назван; при нехватке бюджета
// инструмент с подсказкой остаётся в списке первым.
func TestMCPToolHintLimitAndBudget(t *testing.T) {
	f := newMCPFixFix(t, "ок")
	if err := f.st.SetSetting(mcpToolHintCharsKey, "50"); err != nil {
		t.Fatal(err)
	}
	f.e.noteToolHint(&store.MCPServer{Name: "planner"}, "task_update",
		mcpFail{Kind: mcpFailArgs, Gist: strings.Repeat("Ѫ", 120)})
	m, _ := f.st.MCPServer("planner")
	if g := m.ToolHints["task_update"].Gist; strings.Count(g, "Ѫ") != 50 || !strings.Contains(g, "ещё 70 знаков") {
		t.Errorf("суть в подсказке: %q", g)
	}
	// не отказ по аргументам и сервер агента подсказок не заводят
	f.e.noteToolHint(&store.MCPServer{Name: "planner"}, "task_get", mcpFail{Gist: "x"})
	f.e.noteToolHint(&store.MCPServer{Name: "planner", Agent: "мак"}, "task_list", mcpFail{Kind: mcpFailArgs, Gist: "x"})
	if m, _ = f.st.MCPServer("planner"); len(m.ToolHints) != 1 {
		t.Errorf("лишние подсказки: %+v", m.ToolHints)
	}

	// бюджет тесен: список режется по инструментам, но подсказанный остаётся
	var info []store.MCPTool
	for i := 0; i < 40; i++ {
		info = append(info, store.MCPTool{Name: fmt.Sprintf("entity%02d_operation", i), Desc: "d"})
	}
	big := &store.MCPServer{Name: "big", ToolInfo: info}
	block := mcpToolsBlockHints(big, 900, map[string]string{"entity39_operation": "deadline must be a datetime"})
	if !strings.Contains(block, "entity39_operation()") || !strings.Contains(block, "⚠ сервер требует: deadline must be a datetime") {
		t.Errorf("подсказанный инструмент вытеснен из тесного списка:\n%s", block)
	}
	if len([]rune(block)) > 900 {
		t.Errorf("блок вылез за бюджет: %d", len([]rune(block)))
	}
}

// Просьба исправить вызов в конце промпта — только когда последнее в
// переписке карточка отказа по аргументам и сервер не закрыт модели.
func TestMCPRetryNoteConditions(t *testing.T) {
	f := newMCPFixFix(t, "ок")
	card := func(status, kind string) []*store.ChatMsg {
		raw := mustJSON(t, ToolCard{Title: "planner · task_update", Err: "planner отклонил аргументы: формат",
			MCP: &MCPCard{Server: "planner", Tool: "task_update", Status: status, FailKind: kind}})
		return []*store.ChatMsg{{Role: "tool", Content: raw}}
	}
	if note := f.e.mcpRetryNote(f.chatID, card("error", mcpFailArgs), nil); !strings.Contains(note, "rejected the ARGUMENTS of your call to task_update (planner отклонил аргументы: формат)") {
		t.Errorf("просьбы исправить нет: %q", note)
	}
	for name, h := range map[string][]*store.ChatMsg{
		"пусто": nil,
		"отказ не по аргументам": card("error", ""),
		"выполнено":              card("executed", ""),
		"ждёт кнопки":            card("pending", ""),
		"после карточки ответ":   append(card("error", mcpFailArgs), &store.ChatMsg{Role: "assistant", Content: "Не вышло."}),
	} {
		if note := f.e.mcpRetryNote(f.chatID, h, nil); note != "" {
			t.Errorf("%s: просьба исправить не к месту: %q", name, note)
		}
	}
	if note := f.e.mcpRetryNote(f.chatID, card("error", mcpFailArgs), map[string]string{"planner": "секретное"}); note != "" {
		t.Errorf("закрытому серверу сказана суть ошибки: %q", note)
	}
	// в истории для модели такая карточка — строка, а не JSON
	if got := mcpCardForModel(card("error", mcpFailArgs)[0].Content, 1000); !strings.HasPrefix(got, "[sema-mcp call «planner» · task_update failed — planner отклонил аргументы: формат") {
		t.Errorf("карточка в истории: %q", got)
	}
	executed := card("executed", "")[0].Content
	if mcpCardForModel(executed, 1000) != executed {
		t.Error("выполненная карточка должна остаться как была")
	}
}

// Просьба исправить вызов — настройка: 0 не просит повторять вовсе.
func TestMCPArgRetriesIsSetting(t *testing.T) {
	f := newMCPFixFix(t, "ок")
	if err := f.st.SetSetting(mcpArgRetriesKey, "0"); err != nil {
		t.Fatal(err)
	}
	f.ask(t, askDeadline)
	first := f.model.Prompts()[0]
	if !strings.Contains(first, "do not send the call again: answer honestly") || strings.Contains(first, "send the call again as one block") {
		t.Errorf("при arg_retries=0 модель просят повторять:\n%s", first)
	}
	if got := mcpRetryText("s", "t", "формат", 1, 0); !strings.Contains(got, "has rejected the arguments of t 1 times in a row") {
		t.Errorf("при arg_retries=0 уже первый отказ — честный ответ: %q", got)
	}
}

// Пределы ошибок попадают в общий список пределов сервера MCP в чате.
func TestMCPChatLimitsIncludeErrorSettings(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	lim := e.MCPChatLimits()
	if lim.ErrorChars != 1500 || lim.HintDays != 30 || lim.HintChars != 300 || lim.Retries != 1 {
		t.Errorf("значения по умолчанию: %+v", lim)
	}
	for k, v := range map[string]string{mcpErrorCharsKey: "700", mcpToolHintDaysKey: "7", mcpToolHintCharsKey: "120", mcpArgRetriesKey: "2"} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	lim = e.MCPChatLimits()
	if lim.ErrorChars != 700 || lim.HintDays != 7 || lim.HintChars != 120 || lim.Retries != 2 {
		t.Errorf("настройки не подействовали: %+v", lim)
	}
}

// Закрытый облачной модели сервер не отдаёт ей суть ошибки ни подсказкой, ни
// просьбой исправить: сама ошибка — тоже его данные.
func TestMCPHintsHiddenFromCloudForClosedServer(t *testing.T) {
	f := newMCPFixFix(t, "ок")
	if err := f.st.SetMCPToolHint("planner", "task_update", plannerGist, time.Now()); err != nil {
		t.Fatal(err)
	}
	m, _ := f.st.MCPServer("planner")
	m.Class = store.ClassSecret
	if err := f.st.SaveMCPServer(m); err != nil {
		t.Fatal(err)
	}
	got := f.e.mcpChatPrompt(context.Background(), f.chatID, "что в Planner", true)
	if strings.Contains(got, plannerGist) || strings.Contains(got, "task_update") {
		t.Errorf("закрытый сервер выдал облачной модели подсказку:\n%s", got)
	}
	// SaveMCPServer строку с подсказками не затирает
	if m, _ = f.st.MCPServer("planner"); len(m.ToolHints) != 1 {
		t.Errorf("сохранение строки сервера затёрло подсказки: %+v", m.ToolHints)
	}
}

func promptTail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Слова закрытого сервера — его данные: ошибка второго хода облачной модели не
// показывается (она может повторять то, что человек прислал серверу), а
// человеку карточка говорит всё. Доверенная модель получает ошибку целиком.
func TestMCPServerErrorWithheldFromCloud(t *testing.T) {
	block := "Смотрю.\n```sema-mcp:planner\n{\"tool\": \"task_list\", \"args\": {\"projectId\": \"xx\"}}\n```"
	setSecret := func(f *mcpChatFix) {
		m, _ := f.st.MCPServer("planner")
		m.Class = store.ClassSecret
		if err := f.st.SaveMCPServer(m); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("облачная", func(t *testing.T) {
		f := newMCPFixFixTrust(t, false, block, "Сервер мне закрыт.")
		setSecret(f)
		f.ask(t, "какие задачи в проекте xx в Planner")
		prompts := f.model.Prompts()
		if len(prompts) != 2 {
			t.Fatalf("модель звали %d раз", len(prompts))
		}
		second := prompts[1]
		if strings.Contains(second, "projectId must be a UUID") || strings.Contains(second, "INVALID_FIELD") {
			t.Fatalf("ошибка закрытого сервера ушла облачной модели:\n…%s", promptTail(second, 1200))
		}
		if !strings.Contains(second, "the server's error text is withheld from you (server data class: секретное)") {
			t.Errorf("модель не узнала, что текст ошибки скрыт:\n…%s", promptTail(second, 1200))
		}
		cards := f.cards(t)
		if len(cards) != 1 || !strings.Contains(cards[0].Err, "projectId must be a UUID") {
			t.Errorf("человек не видит причину отказа в карточке: %+v", cards)
		}
		// и история следующего хода: карточка закрытого сервера скрыта целиком
		p, err := f.e.chatPrompt(context.Background(), f.chatID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p, "projectId must be a UUID") || strings.Contains(p, "rejected the ARGUMENTS of your call to") {
			t.Error("закрытая ошибка или просьба её исправить ушли облачной модели вместе с историей")
		}
	})
	t.Run("доверенная", func(t *testing.T) {
		f := newMCPFixFixTrust(t, true, block, "Нужен UUID.")
		setSecret(f)
		f.ask(t, "какие задачи в проекте xx в Planner")
		if second := f.model.Prompts()[1]; !strings.Contains(second, "projectId must be a UUID") {
			t.Errorf("доверенная модель не получила ошибку:\n…%s", promptTail(second, 1200))
		}
	})
}
