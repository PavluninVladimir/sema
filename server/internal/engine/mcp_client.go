package engine

// Сёма ходит на чужие серверы MCP: вики, чужие сервисы. Ядро хранит
// справочник и правила по инструментам (mcp_rules.go), модуль понимает диалект. Ключ подставляет
// ядро по имени секрета — в текст модели токен не попадает.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// mcpHeaders — заголовки постоянного ключа, заданного человеком: ключ
// подставляется ядром. Вход OAuth идёт другой дорогой (`mcpAuth`).
func (e *Engine) mcpHeaders(m *store.MCPServer) (map[string]string, error) {
	if m.Secret == "" {
		return nil, nil
	}
	head := m.Header
	if head == "" {
		head = "Authorization"
	}
	prefix := m.Prefix
	if prefix == "" && strings.EqualFold(head, "Authorization") {
		prefix = "Bearer "
	}
	v, err := e.coreSecretFiller("ядро · сервер MCP «" + m.Name + "»")(m.Secret)
	if err != nil {
		return nil, err
	}
	return map[string]string{head: prefix + v}, nil
}

// CheckMCP — проверка связи и вычитывание инструментов. Итог остаётся в
// справочнике: человек видит, жив ли сервер, не открывая журнал.
func (e *Engine) CheckMCP(ctx context.Context, name string) error {
	m, ok := e.store.MCPServer(name)
	if !ok {
		return fmt.Errorf("сервера %q нет", name)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var names []string
	var info []store.MCPTool
	who := ""
	if m.Agent != "" {
		list, err := e.agentTools(cctx, m)
		if err != nil {
			return e.noteMCP(m, "fail", clip(err.Error(), 200))
		}
		// имя агента уже стоит в подписи строки: повторять его в примечании
		// незачем — примечание нужно для ошибок
		names, who = list, ""
	} else {
		// проверка — это настоящее рукопожатие, а не разговор по старой
		// сессии: живой клиент выбрасываем
		e.mcpDrop(m.Name)
		c, err := e.mcpClient(m)
		if err != nil {
			return e.noteMCP(m, "fail", err.Error())
		}
		who, err = c.Hello(cctx)
		if err != nil {
			return e.checkFailed(m, err)
		}
		tools, err := c.Tools(cctx)
		if err != nil {
			return e.checkFailed(m, err)
		}
		names = make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t.Name)
			var schema json.RawMessage
			if len(t.Schema) > 0 {
				schema, _ = json.Marshal(t.Schema)
			}
			info = append(info, store.MCPTool{Name: t.Name, Desc: t.Description, Schema: schema})
		}
	}
	m.Tools, m.ToolInfo, m.State, m.Note = names, info, "ok", who
	// правила здесь не выводятся и не засеваются: инструмент, которого в
	// правилах нет, — «новый», и отвечает ему ответ по умолчанию (запретить),
	// пока человек не решит. Раньше новому серверу ядро само ставило круги
	// по угаданным образцам имён — угадывание ушло вместе с кругами
	m.Checked = time.Now().Format(time.RFC3339)
	log.Printf("MCP «%s»: на связи, инструментов %d", name, len(names))
	if err := e.store.SaveMCPServer(m); err != nil {
		return err
	}
	// правила читаются заново: человек мог решить, пока шла проверка
	if cur, ok := e.store.MCPServer(name); ok {
		e.noteNewMCPTools(cur)
	}
	return nil
}

// checkFailed — проверка не прошла. Если сервер ждёт входа, это не поломка, а
// состояние: строка говорит «нужен вход» и показывает кнопку.
func (e *Engine) checkFailed(m *store.MCPServer, err error) error {
	if mapped := e.mcpFailure(m, err, true); mapped != err {
		return mapped
	}
	return e.noteMCP(m, "fail", clip(err.Error(), 200))
}

func (e *Engine) noteMCP(m *store.MCPServer, state, note string) error {
	m.State, m.Note, m.Checked = state, note, time.Now().Format(time.RFC3339)
	_ = e.store.SaveMCPServer(m)
	if state == "fail" {
		return fmt.Errorf("%s", note)
	}
	return nil
}

// mcpSnake — имя инструмента в змеином виде: `listTasks`, `get-task` и
// `tasks.list` становятся `list_tasks`, `get_task`, `tasks_list`. Образцы
// кругов написаны в змеином виде (так называют инструменты чаще всего), а
// сервер вправе назвать иначе.
func mcpSnake(name string) string {
	rs := []rune(strings.TrimSpace(name))
	var b strings.Builder
	for i, r := range rs {
		switch {
		case r == '-' || r == '.' || r == ' ' || r == '/':
			b.WriteRune('_')
		case unicode.IsUpper(r):
			if i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1])) {
				b.WriteRune('_')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// MCPResult — что вышло из вызова.
type MCPResult struct {
	Text      string
	Policy    string
	Why       string
	PendingID int64
	Err       string
	FailKind  string // mcpFailArgs: сервер не принял аргументы — вызов можно исправить
	// FromServer — текст ошибки написал сам сервер (а не Сёма: круг, предел, вход)
	FromServer bool
}

// RequestMCP — вызов инструмента чужого сервера с правилами и журналом.
// Разрешённое выполняется сразу, «спросить» ждёт человека, запрещённое (и
// новое, пока про него не решили) отклоняется. Без чата: модуль и скрипт.
func (e *Engine) RequestMCP(ctx context.Context, who, server, tool string, args map[string]any) MCPResult {
	return e.RequestMCPFrom(ctx, MCPOrigin{Who: who}, server, tool, args)
}

// RequestMCPFrom — то же, но с указанием, откуда вызов: из чата ответ
// человека («Выполнить») вернётся в этот чат, и Сёма продолжит (`ApproveMCP`).
func (e *Engine) RequestMCPFrom(ctx context.Context, o MCPOrigin, server, tool string, args map[string]any) MCPResult {
	who := o.Who
	m, ok := e.store.MCPServer(server)
	if !ok || !m.Enabled {
		return MCPResult{Err: fmt.Sprintf("сервер MCP %q не подключён", server)}
	}
	policy, why := ClassifyMCP(m, tool)
	raw, _ := json.Marshal(args)
	use := store.MCPUse{Server: server, Tool: tool, Who: who, Circle: policy, Args: string(raw)}
	switch policy {
	case PolicyDeny:
		use.OK, use.Result = false, why
		e.store.LogMCP(use)
		return MCPResult{Policy: policy, Why: why, Err: "такие вызовы запрещены: " + why}
	case PolicyAsk:
		id, err := e.store.AddMCPPending(&store.MCPPending{Server: server, Tool: tool, Args: string(raw), Who: who, ChatID: o.ChatID})
		if err != nil {
			return MCPResult{Err: err.Error()}
		}
		// это не успех: вызов только записан и ждёт человека. В журнале
		// «выполнено» и «ждёт кнопки» должны различаться
		use.OK, use.Result = false, store.MCPWaitingResult
		e.store.LogMCP(use)
		e.notifyMCPPending(id, m, tool, string(raw), o)
		return MCPResult{Policy: policy, Why: why, PendingID: id,
			Text: "Вызов записан и ждёт вашего подтверждения в «Уведомлениях»."}
	}
	text, err := e.runMCP(ctx, m, tool, args)
	use.OK, use.Result = err == nil, text
	if err != nil {
		err = e.mcpFailure(m, err, false)
		// суть ошибки первой строкой, а не обрезок JSON: по нему модель
		// исправляет вызов, а человек понимает, что не так
		fail := e.describeMCPError(m, err)
		e.noteToolHint(m, tool, fail)
		use.Result = fail.Text(0) // журнал режет по своему пределу, суть в начале
		e.store.LogMCP(use)
		return MCPResult{Policy: policy, Why: why, Err: fail.Text(e.MCPChatLimits().Card), FailKind: fail.Kind, FromServer: fail.FromServer}
	}
	e.store.LogMCP(use)
	return MCPResult{Policy: policy, Why: why, Text: text}
}

// runMCP — сам вызов. Ответ чужого сервера проходит маски секретов сразу:
// он идёт и в журнал, и в карточку, и в промпт модели, и ни одно из этих мест
// не должно получить то, что маска прячет.
func (e *Engine) runMCP(ctx context.Context, m *store.MCPServer, tool string, args map[string]any) (string, error) {
	if m.Agent != "" {
		text, err := e.runMCPOverLink(ctx, m, tool, args)
		return e.maskOutput(text), err
	}
	c, err := e.mcpClient(m)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := c.Hello(cctx); err != nil {
		return "", err
	}
	text, err := c.Call(cctx, tool, args)
	if err == nil && m.State != "ok" {
		// сервер ответил — значит «не отвечает» или «нужен вход» устарели
		_ = e.store.SetMCPState(m.Name, "ok", "")
	}
	return e.maskOutput(text), err
}

// ApproveMCP — человек нажал «Выполнить» (или «Отклонить») на карточке
// вызова. Вызов пришёл из чата — результат ложится в него карточкой, а Сёма
// продолжает по нему, как после подтверждённой команды на сервере.
func (e *Engine) ApproveMCP(ctx context.Context, id int64, approve bool) (string, error) {
	p, ok := e.store.MCPPendingByID(id)
	if !ok {
		return "", fmt.Errorf("вызова %d нет", id)
	}
	if p.State != "waiting" {
		return "", fmt.Errorf("этот вызов уже решён")
	}
	card := ToolCard{Title: p.Server + " · " + p.Tool,
		MCP: &MCPCard{Server: p.Server, Tool: p.Tool, Args: p.Args, PendingID: id}}
	if !approve {
		_ = e.store.DecideMCPPending(id, "rejected")
		e.CloseNotification(fmt.Sprintf("mcp:%d", id), store.NotifyDismissed, "отклонено")
		e.store.LogMCP(store.MCPUse{Server: p.Server, Tool: p.Tool, Who: p.Who, Circle: PolicyAsk,
			OK: false, Args: p.Args, Result: store.MCPRejectedResult})
		if p.ChatID > 0 {
			card.MCP.Status, card.Result = "rejected", "отклонено человеком"
			if !e.replaceMCPCard(p.ChatID, id, card) {
				_ = e.saveToolCard(p.ChatID, card)
			}
		}
		return "Вызов отклонён.", nil
	}
	m, ok := e.store.MCPServer(p.Server)
	if !ok {
		return "", fmt.Errorf("сервера %q больше нет", p.Server)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(p.Args), &args)
	started := time.Now()
	text, err := e.runMCP(ctx, m, p.Tool, args)
	err = e.mcpFailure(m, err, false)
	var fail mcpFail
	if err != nil {
		// то же, что у читающего вызова: суть первой строкой и подсказка на будущее
		fail = e.describeMCPError(m, err)
		e.noteToolHint(m, p.Tool, fail)
	}
	_ = e.store.DecideMCPPending(id, "done")
	e.store.LogMCP(store.MCPUse{Server: p.Server, Tool: p.Tool, Who: p.Who, Circle: PolicyAsk,
		OK: err == nil, Args: p.Args, Result: firstNonEmpty(text, fail.Text(0))})
	if err == nil {
		// вызов трекера от поручения: номер созданной задачи — из ответа
		e.assignTrackerApproved(ctx, id, text)
	}
	level, value := "ok", clip(text, 1500)
	if err != nil {
		level, value = "alert", clipNamed(fail.Line, 500) // в уведомлении — суть, сырой ответ лежит в журнале и карточке
	}
	_ = e.setNotificationResultByKey(fmt.Sprintf("mcp:%d", id), store.NotifyField{
		Label: "Результат", Value: value, Level: level})
	if p.ChatID > 0 {
		card.Seconds = time.Since(started).Seconds()
		if err != nil {
			card.MCP.Status, card.Err, card.MCP.FailKind, card.MCP.FromServer = "error", fail.Text(e.MCPChatLimits().Card), fail.Kind, fail.FromServer
		} else {
			card.MCP.Status = "executed"
			card.Code = clipNamed(text, e.MCPChatLimits().Card)
			card.Result = fmt.Sprintf("%d знаков · %.1f с", len([]rune(text)), card.Seconds)
		}
		// одна команда — одна карточка: та, что ждала кнопки, становится
		// выполненной, а не появляется второй строкой ниже
		if !e.replaceMCPCard(p.ChatID, id, card) {
			_ = e.saveToolCard(p.ChatID, card)
		}
		if e.store.Setting(fmt.Sprintf("chat.expires.%d", p.ChatID), "") != "" {
			e.touchChatExpiry(p.ChatID) // чат команды живёт от последнего действия
		}
		e.continueChat(p.ChatID)
	}
	if err != nil {
		return "", errors.New(fail.Line)
	}
	return text, nil
}

// ApproveMCPAlways — «Выполнить и разрешить впредь»: этому инструменту на
// этом сервере правило «Разрешить», и ждущий вызов выполняется. Как у
// команд SSH: впредь без вопроса — решение человека, а не модели, и только
// для одного инструмента одного сервера.
func (e *Engine) ApproveMCPAlways(ctx context.Context, id int64) (string, error) {
	p, ok := e.store.MCPPendingByID(id)
	if !ok {
		return "", fmt.Errorf("вызова %d нет", id)
	}
	if p.State != "waiting" {
		return "", fmt.Errorf("этот вызов уже решён")
	}
	m, ok := e.store.MCPServer(p.Server)
	if !ok {
		return "", fmt.Errorf("сервера %q больше нет", p.Server)
	}
	rules := make(map[string]string, len(m.Rules)+1)
	for tool, answer := range m.Rules {
		rules[tool] = answer
	}
	rules[p.Tool] = store.RuleAllow
	if err := e.store.SetMCPRules(m.Name, rules, m.RuleDefault); err != nil {
		return "", err
	}
	log.Printf("MCP «%s»: %s — впредь без вопроса (решение человека)", m.Name, p.Tool)
	return e.ApproveMCP(ctx, id, true)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// mcpTools — инструменты песочницы: модуль ходит только на выданные серверы.
func (e *Engine) mcpTools(granted []string, who string) []script.Tool {
	return e.mcpToolsFor(granted, MCPOrigin{Who: who}, func(server string) string {
		return fmt.Sprintf("сервер MCP %q модулю не выдан — выдать можно на экране «Интеграции»", server)
	})
}

// runMCPOverLink — сервер живёт у агента: зовём по каналу агентов, где
// обе стороны показали сертификаты. HTTP здесь не запасной путь: если
// агента нет на связи, вызов не состоится — иначе шифрование и
// разрешения обходились бы стороной.
func (e *Engine) runMCPOverLink(ctx context.Context, m *store.MCPServer, tool string, args map[string]any) (string, error) {
	can := m.Can
	if can == "" {
		can = "код"
	}
	if args == nil {
		args = map[string]any{}
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	raw, err := e.AskAgent(cctx, m.Agent, can, can+"."+tool, args)
	if err != nil {
		return "", err
	}
	var out struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &out) == nil && out.Text != "" {
		return out.Text, nil
	}
	return string(raw), nil
}

// agentTools — список умений агента по каналу: тем же ответом, что и
// вызов, только вопрос про сам список.
func (e *Engine) agentTools(ctx context.Context, m *store.MCPServer) ([]string, error) {
	can := m.Can
	if can == "" {
		can = "код"
	}
	raw, err := e.AskAgent(ctx, m.Agent, can, can+".умения", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("список умений не разобран: %w", err)
	}
	return out.Tools, nil
}
