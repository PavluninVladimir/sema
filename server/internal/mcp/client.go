package mcp

// Клиент MCP: Сёма ходит на чужие серверы (трекер задач, вики) тем же
// протоколом, которым сам отвечает внешним агентам. Транспорт — Streamable
// HTTP (редакция 2025-06-18), как у Claude Code и Codex: JSON-RPC поверх
// POST, ответ приходит либо телом JSON, либо потоком событий. Без
// сторонних библиотек — чистый Go.
//
// Клиент держит сессию: `Mcp-Session-Id` из ответа на initialize идёт во
// все следующие запросы, согласованная версия — заголовком
// `MCP-Protocol-Version`. Сессия живёт, пока живёт сам клиент, то есть
// переживает несколько вызовов, но не перезапуск Сёмы.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/netguard"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion — версия протокола, которую Сёма предлагает. Сервер вправе
// ответить другой: принимается то, что он назвал.
const ProtocolVersion = "2025-06-18"

// maxReply — сколько байт ответа читается: чужой сервер не должен занимать
// память бесконечным потоком.
const maxReply = 8 << 20

// Tool — инструмент чужого сервера, как он сам себя описал.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"inputSchema,omitempty"`
}

// Authorizer — откуда клиент берёт заголовки входа и что делает, когда
// сервер ответил 401. Клиент про OAuth ничего не знает: токены, их срок и
// обновление — дело того, кто его создал.
type Authorizer interface {
	// Headers — заголовки для очередного запроса (пусто — без входа).
	Headers(ctx context.Context) (map[string]string, error)
	// Unauthorized зовётся на 401. sent — значение Authorization, с которым
	// ушёл отвергнутый запрос: по нему видно, обновил ли токен кто-то другой.
	// true — токен обновлён, запрос стоит повторить (один раз).
	Unauthorized(ctx context.Context, sent string, ch *Challenge) (retry bool, err error)
}

// AuthError — сервер ответил 401 и вход не помог. Несёт вызов сервера
// (`WWW-Authenticate`): по нему движок находит, куда идти за входом.
type AuthError struct {
	Challenge *Challenge
}

func (e *AuthError) Error() string {
	msg := "HTTP 401: сервер требует вход"
	if e.Challenge != nil && e.Challenge.Description != "" {
		msg += " (" + e.Challenge.Description + ")"
	}
	return msg
}

// ToolError — инструмент выполнился и вернул ошибку (`isError` в ответе).
// Текст хранится целиком: суть ошибки — в его глубине (у Planner это
// `details.raw.message[0]` внутри JSON), и обрезка до нескольких сотен знаков
// уносила именно её — модель видела «{ "code": … "message": [ "V» и не могла
// исправить вызов. Обрезает тот, кто показывает, по рунам и с названным
// остатком, а не клиент на входе.
type ToolError struct {
	Text string
}

func (e *ToolError) Error() string {
	return "инструмент вернул ошибку: " + e.Text
}

// RPCError — сервер ответил ошибкой JSON-RPC (не инструмент, а сам запрос):
// -32602 значит «параметры не подошли».
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("%s (код %d)", e.Message, e.Code) }

// HTTPError — сервер ответил HTTP-статусом 400 и выше. Тело сохранено как
// пришло (до 4 КБ): часть серверов отдаёт отказ по аргументам именно так, с
// JSON внутри.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, clip(e.Body, 200)) }

// Client — соединение с одним сервером MCP. Безопасен для нескольких
// горутин: сессия одна на всех.
type Client struct {
	URL     string
	Headers map[string]string // постоянные заголовки (ключ, заданный человеком)
	Auth    Authorizer        // вход OAuth; nil — только постоянные заголовки
	HTTP    *http.Client

	id atomic.Int64

	initMu sync.Mutex // рукопожатие одно за раз
	mu     sync.Mutex // поля ниже
	ready  bool
	sess   string // Mcp-Session-Id; пусто — сервер без сессий
	proto  string // согласованная версия протокола
	server string // имя сервера из initialize
}

func NewClient(url string, headers map[string]string) *Client {
	return &Client{URL: url, Headers: headers, HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: netguard.SameOriginRedirect}}
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"` // у уведомления id нет
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// isReplyTo — это ответ на наш запрос: у сообщения нет method (иначе это
// уведомление или просьба сервера), а id совпадает.
func (m *rpcMsg) isReplyTo(id int64) bool {
	if m.Method != "" || len(m.ID) == 0 {
		return false
	}
	raw := strings.Trim(strings.TrimSpace(string(m.ID)), `"`)
	return raw == strconv.FormatInt(id, 10)
}

// isAnonymousError — ответ-ошибка без id: так отвечают на запрос, который
// сервер не смог даже разобрать. Берём его, только если настоящего ответа
// не нашлось.
func (m *rpcMsg) isAnonymousError() bool {
	return m.Method == "" && m.Error != nil && (len(m.ID) == 0 || string(m.ID) == "null")
}

type reply struct {
	msg    *rpcMsg
	header http.Header
}

// exchange — один обмен с сервером: запрос, повтор после обновления токена
// на 401, повтор после потерянной сессии на 404. notify — уведомление:
// ответа не ждём, успех — любой 2xx (обычно 202).
func (c *Client) exchange(ctx context.Context, method string, params any, notify bool) (*reply, error) {
	req := rpcReq{JSONRPC: "2.0", Method: method, Params: params}
	var id int64
	if !notify {
		id = c.id.Add(1)
		req.ID = &id
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	authTried, sessionTried := false, false
	for {
		sess := c.session()
		resp, sent, err := c.post(ctx, body)
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			ch := ParseChallenge(resp.Header.Values("WWW-Authenticate"))
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			if c.Auth != nil && !authTried {
				authTried = true
				retry, aerr := c.Auth.Unauthorized(ctx, sent, ch)
				if aerr != nil {
					return nil, aerr
				}
				if retry {
					continue
				}
			}
			return nil, &AuthError{Challenge: ch}
		case sess != "" && !notify && method != "initialize" && !sessionTried && lostSession(resp):
			// сессия потеряна (сервер перезапустили, срок вышел): по
			// спецификации 404 с сессией — повод начать заново
			_ = resp.Body.Close()
			sessionTried = true
			c.forget(sess)
			if err := c.ensure(ctx); err != nil {
				return nil, err
			}
			continue
		}
		return c.read(resp, id, notify)
	}
}

// lostSession — ответ говорит, что сервер не знает нашу сессию: 404 по
// спецификации, а часть серверов отвечает 400 со словом «session» в теле.
// Тело при 400 читается и возвращается на место, чтобы обычная ошибка
// 400 (плохие аргументы) осталась обычной.
func lostSession(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusNotFound:
		return true
	case http.StatusBadRequest:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		return strings.Contains(strings.ToLower(string(raw)), "session")
	}
	return false
}

// post — отправка одного запроса. Второе значение — Authorization, с которым
// он ушёл.
func (c *Client) post(ctx context.Context, body []byte) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	if c.Auth != nil {
		h, err := c.Auth.Headers(ctx)
		if err != nil {
			return nil, "", err
		}
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	c.mu.Lock()
	sess, proto := c.sess, c.proto
	c.mu.Unlock()
	if sess != "" {
		req.Header.Set("Mcp-Session-Id", sess)
	}
	if proto != "" {
		req.Header.Set("MCP-Protocol-Version", proto)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	return resp, req.Header.Get("Authorization"), nil
}

// read разбирает ответ. Тело закрывается здесь.
func (c *Client) read(resp *http.Response, id int64, notify bool) (*reply, error) {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	if notify {
		// 202 Accepted без тела — обычный успех уведомления
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &reply{header: resp.Header}, nil
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, fmt.Errorf("сервер принял запрос, но ответа не прислал (HTTP %d)", resp.StatusCode)
	}
	msg, err := readMessage(io.LimitReader(resp.Body, maxReply), resp.Header.Get("Content-Type"), id)
	if err != nil {
		return nil, err
	}
	return &reply{msg: msg, header: resp.Header}, nil
}

// readMessage достаёт из ответа сообщение с нашим id. Ответ бывает телом
// JSON (объект или пакет) или потоком событий, где рядом с ответом идут
// уведомления о ходе работы — нужное выбирается по id, а не «последнее».
func readMessage(r io.Reader, contentType string, id int64) (*rpcMsg, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(8)
	sse := strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") ||
		bytes.HasPrefix(bytes.TrimSpace(head), []byte("event:")) || bytes.HasPrefix(bytes.TrimSpace(head), []byte("data:"))
	if !sse {
		raw, err := io.ReadAll(br)
		if err != nil {
			return nil, err
		}
		return pickMessage(bytes.TrimSpace(raw), id)
	}
	var data []string
	var fallback *rpcMsg
	dispatch := func() *rpcMsg {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		m, err := pickMessage([]byte(payload), id)
		if err != nil {
			return nil
		}
		if m.isReplyTo(id) {
			return m
		}
		if m.isAnonymousError() {
			fallback = m
		}
		return nil
	}
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if m := dispatch(); m != nil {
				return m, nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if err != nil {
			break
		}
	}
	if m := dispatch(); m != nil {
		return m, nil
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("в потоке событий нет ответа на запрос %d", id)
}

// pickMessage — из JSON (объект или пакет) выбрать ответ на наш id.
func pickMessage(raw []byte, id int64) (*rpcMsg, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("ответ пустой")
	}
	var list []rpcMsg
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("ответ не разобран: %s", clip(string(raw), 200))
		}
	} else {
		var one rpcMsg
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, fmt.Errorf("ответ не разобран: %s", clip(string(raw), 200))
		}
		list = []rpcMsg{one}
	}
	var anon *rpcMsg
	for i := range list {
		if list[i].isReplyTo(id) {
			return &list[i], nil
		}
		if list[i].isAnonymousError() {
			anon = &list[i]
		}
	}
	if anon != nil {
		return anon, nil
	}
	if len(list) == 1 && list[0].Method == "" && len(list[0].ID) == 0 && list[0].Result != nil {
		// старый сервер, забывший вернуть id: ответ один, других быть не может
		return &list[0], nil
	}
	return nil, fmt.Errorf("в ответе нет сообщения на запрос %d: %s", id, clip(string(raw), 200))
}

func (c *Client) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

// forget — сессия sess мертва. Сбрасывается только она: если рядом уже
// начали новую, чужое рукопожатие не отменяется.
func (c *Client) forget(sess string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == sess {
		c.sess, c.ready = "", false
	}
}

// ensure — рукопожатие, если его ещё не было. Одно за раз: остальные
// ждут и видят готовую сессию.
func (c *Client) ensure(ctx context.Context) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if ready {
		return nil
	}
	return c.handshake(ctx)
}

func (c *Client) handshake(ctx context.Context) error {
	c.mu.Lock()
	c.sess, c.proto = "", ""
	c.mu.Unlock()
	rep, err := c.exchange(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "sema", "version": "1"},
	}, false)
	if err != nil {
		return err
	}
	if rep.msg.Error != nil {
		return &RPCError{Code: rep.msg.Error.Code, Message: rep.msg.Error.Message}
	}
	var out struct {
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(rep.msg.Result, &out); err != nil {
		return fmt.Errorf("ответ на initialize не разобран: %w", err)
	}
	proto := out.ProtocolVersion
	if proto == "" {
		proto = ProtocolVersion
	}
	c.mu.Lock()
	c.sess = rep.header.Get("Mcp-Session-Id")
	c.proto = proto
	c.server = out.ServerInfo.Name
	c.ready = true
	c.mu.Unlock()
	// сервер ждёт уведомления о готовности; часть серверов на него не
	// отвечает как надо, и падать из-за этого незачем — сессия уже есть
	_, _ = c.exchange(ctx, "notifications/initialized", map[string]any{}, true)
	return nil
}

// Reset — забыть сессию: следующий вызов начнёт рукопожатие заново.
func (c *Client) Reset() {
	c.mu.Lock()
	c.sess, c.proto, c.ready = "", "", false
	c.mu.Unlock()
}

// call — вызов метода с рукопожатием при необходимости.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	if err := c.ensure(ctx); err != nil {
		return err
	}
	rep, err := c.exchange(ctx, method, params, false)
	if err != nil {
		return err
	}
	if rep.msg.Error != nil {
		return &RPCError{Code: rep.msg.Error.Code, Message: rep.msg.Error.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rep.msg.Result, out)
}

// Hello — представление и проверка связи; возвращает имя сервера. Повторный
// вызов на живой сессии ничего не спрашивает у сервера.
func (c *Client) Hello(ctx context.Context) (string, error) {
	if err := c.ensure(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	name := c.server
	c.mu.Unlock()
	if name == "" {
		name = "сервер MCP"
	}
	return name, nil
}

// Protocol — какую версию протокола согласовали с сервером (пусто — ещё
// не здоровались).
func (c *Client) Protocol() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proto
}

// Tools — что сервер умеет. Длинный список приходит страницами.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var out struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Tools...)
		if out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
	}
	return all, nil
}

// Call — вызов инструмента; ответ приводится к тексту.
func (c *Client) Call(ctx context.Context, tool string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &out); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range out.Content {
		if c.Text != "" {
			b.WriteString(c.Text)
			b.WriteString("\n")
		}
	}
	text := strings.TrimSpace(b.String())
	if out.IsError {
		return text, &ToolError{Text: text}
	}
	return text, nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
