package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
)

// Сессия и версия протокола: Mcp-Session-Id из ответа на initialize идёт во
// все следующие запросы, версия — ровно та, что назвал сервер.
func TestSessionAndProtocolHeaders(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.AuthRequired = false
	fake.ProtocolReply = "2025-03-26" // сервер старше нас: принимаем его версию
	c := NewClient(fake.URL(), nil)
	ctx := context.Background()

	name, err := c.Hello(ctx)
	if err != nil || name != "fake-mcp" {
		t.Fatalf("Hello: %q, %v", name, err)
	}
	if _, err := c.Hello(ctx); err != nil { // повтор ничего не спрашивает
		t.Fatal(err)
	}
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 2 {
		t.Fatalf("Tools: %v, %v", tools, err)
	}
	if out, err := c.Call(ctx, "task_list", nil); err != nil || out != "вызван task_list" {
		t.Fatalf("Call: %q, %v", out, err)
	}
	if got := c.Protocol(); got != "2025-03-26" {
		t.Errorf("согласованная версия %q", got)
	}

	if fake.Count("initialize") != 1 {
		t.Errorf("initialize отправлен %d раз, ждали один", fake.Count("initialize"))
	}
	reqs := fake.Requests()
	first := reqs[0]
	if first.Method != "initialize" || first.Session != "" || first.Protocol != "" {
		t.Errorf("initialize идёт без сессии и без версии в заголовке: %+v", first)
	}
	sawInitialized := false
	for _, r := range reqs[1:] {
		if r.Session != "sess-1" || r.Protocol != "2025-03-26" {
			t.Errorf("%s: сессия %q, версия %q", r.Method, r.Session, r.Protocol)
		}
		if r.Method == "notifications/initialized" {
			sawInitialized = true
			if r.HasID {
				t.Error("у уведомления не должно быть id")
			}
			if r.Status != http.StatusAccepted {
				t.Errorf("уведомление: статус %d", r.Status)
			}
		}
	}
	if !sawInitialized {
		t.Error("notifications/initialized не отправлен")
	}
}

// Сервер по умолчанию отвечает нашей версией, и MCP-Protocol-Version стоит
// после рукопожатия.
func TestOurVersionOffered(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.AuthRequired = false
	c := NewClient(fake.URL(), nil)
	if _, err := c.Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Protocol() != ProtocolVersion || ProtocolVersion != "2025-06-18" {
		t.Errorf("версия %q", c.Protocol())
	}
	for _, r := range fake.Requests()[1:] {
		if r.Protocol != "2025-06-18" {
			t.Errorf("%s: MCP-Protocol-Version %q", r.Method, r.Protocol)
		}
	}
}

// Сервер забыл сессию: 404 с Mcp-Session-Id — один раз здороваемся заново и
// повторяем запрос уже с новой сессией.
func TestLostSessionReinitializes(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.AuthRequired = false
	c := NewClient(fake.URL(), nil)
	ctx := context.Background()
	if _, err := c.Tools(ctx); err != nil {
		t.Fatal(err)
	}
	fake.ExpireSessions()
	out, err := c.Call(ctx, "task_list", nil)
	if err != nil || out != "вызван task_list" {
		t.Fatalf("вызов после потери сессии: %q, %v", out, err)
	}
	if fake.Count("initialize") != 2 || fake.Count("notifications/initialized") != 2 {
		t.Errorf("рукопожатий: initialize %d, initialized %d", fake.Count("initialize"), fake.Count("notifications/initialized"))
	}
	reqs := fake.Requests()
	last := reqs[len(reqs)-1]
	if last.Method != "tools/call" || last.Session != "sess-2" || last.Status != http.StatusOK {
		t.Errorf("повтор ушёл не с новой сессией: %+v", last)
	}
	// 404 был ровно один: в третий раз клиент не пытается
	n404 := 0
	for _, r := range reqs {
		if r.Status == http.StatusNotFound {
			n404++
		}
	}
	if n404 != 1 {
		t.Errorf("отказов 404: %d", n404)
	}
}

// Сервер без сессий: заголовка нет, и 404 не повод здороваться заново.
func TestStatelessServer(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.AuthRequired = false
	fake.Stateless = true
	c := NewClient(fake.URL(), nil)
	ctx := context.Background()
	if _, err := c.Tools(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(ctx, "task_list", nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.Requests() {
		if r.Session != "" {
			t.Errorf("%s: сессия %q у сервера без сессий", r.Method, r.Session)
		}
	}
	if fake.Count("initialize") != 1 {
		t.Errorf("initialize %d раз", fake.Count("initialize"))
	}
}

// Поток событий с несколькими сообщениями: берётся ответ на наш id, а не
// последнее событие и не первое.
func TestSSEPicksOurReply(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.AuthRequired = false
	fake.SSE = true
	c := NewClient(fake.URL(), nil)
	ctx := context.Background()
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 2 {
		t.Fatalf("Tools по SSE: %v, %v", tools, err)
	}
	out, err := c.Call(ctx, "task_create", map[string]any{"title": "x"})
	if err != nil || out != "вызван task_create" {
		t.Fatalf("Call по SSE: %q, %v", out, err)
	}
}

// Прямая проверка выбора сообщения: ответ бывает в середине потока,
// в нескольких data: и после мусора.
func TestReadMessage(t *testing.T) {
	stream := "id: 1\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
		": комментарий\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"чужой\":1}}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":8,\n" +
		"data: \"result\":{\"ok\":true}}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{}}\n\n"
	m, err := readMessage(strings.NewReader(stream), "text/event-stream", 8)
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Result) != `{"ok":true}` {
		t.Errorf("результат %s", m.Result)
	}
	// ответа нет вовсе — ошибка, а не чужой ответ
	if _, err := readMessage(strings.NewReader(stream), "text/event-stream", 42); err == nil {
		t.Error("чужие ответы приняты за наш")
	}
	// тело JSON и пакет
	if m, err := readMessage(strings.NewReader(`[{"jsonrpc":"2.0","id":3,"result":1},{"jsonrpc":"2.0","id":4,"result":2}]`), "application/json", 4); err != nil || string(m.Result) != "2" {
		t.Errorf("пакет: %v, %v", m, err)
	}
	// без Content-Type, но по виду поток
	if m, err := readMessage(strings.NewReader("event: message\ndata: {\"id\":5,\"result\":true}\n\n"), "", 5); err != nil || string(m.Result) != "true" {
		t.Errorf("поток без типа: %v, %v", m, err)
	}
	// ошибка сервера без id (не смог разобрать запрос) берётся, если своего ответа нет
	if m, err := readMessage(strings.NewReader(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse"}}`), "application/json", 5); err != nil || m.Error == nil {
		t.Errorf("ошибка без id: %v, %v", m, err)
	}
}

// fakeAuth — Authorizer из тестов: токен меняется, когда сервер его отверг.
type fakeAuth struct {
	token    atomic.Value
	next     string
	retries  atomic.Int32
	refuse   bool
	sentSeen []string
}

func (f *fakeAuth) Headers(context.Context) (map[string]string, error) {
	return map[string]string{"Authorization": "Bearer " + f.token.Load().(string)}, nil
}

func (f *fakeAuth) Unauthorized(_ context.Context, sent string, _ *Challenge) (bool, error) {
	f.retries.Add(1)
	f.sentSeen = append(f.sentSeen, sent)
	if f.refuse {
		return false, nil
	}
	f.token.Store(f.next)
	return true, nil
}

// 401 → токен обновляется → запрос повторяется один раз; повторный 401
// возвращается как AuthError с вызовом сервера.
func TestUnauthorizedRefreshRetriesOnce(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer new" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="x", resource_metadata="https://ex.test/.well-known/oauth-protected-resource", scope="a b", error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"s"}}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	a := &fakeAuth{next: "new"}
	a.token.Store("old")
	c.Auth = a
	if _, err := c.Hello(context.Background()); err != nil {
		t.Fatalf("Hello после обновления токена: %v", err)
	}
	if a.retries.Load() != 1 || len(seen) < 2 || seen[0] != "Bearer old" || seen[1] != "Bearer new" {
		t.Errorf("обновлений %d, запросы %v", a.retries.Load(), seen)
	}
	if len(a.sentSeen) != 1 || a.sentSeen[0] != "Bearer old" {
		t.Errorf("Unauthorized не получил отвергнутый заголовок: %v", a.sentSeen)
	}

	// токен так и остался плохим: второй 401 не зацикливается
	c2 := NewClient(srv.URL, nil)
	b := &fakeAuth{next: "still-bad"}
	b.token.Store("old")
	c2.Auth = b
	_, err := c2.Hello(context.Background())
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("ждали AuthError, получили %v", err)
	}
	if b.retries.Load() != 1 {
		t.Errorf("обновлений %d, ждали одно", b.retries.Load())
	}
	if ae.Challenge == nil || ae.Challenge.ResourceMetadata != "https://ex.test/.well-known/oauth-protected-resource" ||
		ae.Challenge.Scope != "a b" || ae.Challenge.Error != "invalid_token" {
		t.Errorf("вызов сервера: %+v", ae.Challenge)
	}
}

func TestParseChallenge(t *testing.T) {
	cases := []struct {
		in   []string
		want Challenge
		nil_ bool
	}{
		{in: []string{`Bearer realm="mcp", resource_metadata="https://a.test/.well-known/oauth-protected-resource/mcp", scope="tasks:read tasks:write mcp:read"`},
			want: Challenge{Realm: "mcp", ResourceMetadata: "https://a.test/.well-known/oauth-protected-resource/mcp", Scope: "tasks:read tasks:write mcp:read"}},
		{in: []string{`Basic realm="x"`, `Bearer error="invalid_token", error_description="expired \"soon\""`},
			want: Challenge{Error: "invalid_token", Description: `expired "soon"`}},
		{in: []string{`bearer`}, want: Challenge{}},
		{in: []string{`Basic realm="x", Bearer realm="y"`}, want: Challenge{Realm: "y"}},
		{in: []string{`Basic realm="x"`}, nil_: true},
		{in: nil, nil_: true},
	}
	for _, c := range cases {
		got := ParseChallenge(c.in)
		if c.nil_ {
			if got != nil {
				t.Errorf("%v: ждали nil, получили %+v", c.in, got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("%v: %+v, ждали %+v", c.in, got, c.want)
		}
	}
}

// Часть серверов на неизвестную сессию отвечает не 404, а 400 со словом
// «session»: клиент здоровается заново. Обычная ошибка 400 сессию не рвёт.
func TestBadRequestAboutSessionReinitializes(t *testing.T) {
	var inits, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			inits.Add(1)
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("s%d", inits.Load()))
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"serverInfo":{"name":"x"}}}`, req.ID)
		case "tools/list":
			if calls.Add(1) == 1 { // первая сессия «умерла»
				http.Error(w, "Bad Request: No valid session ID provided", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"a"}]}}`, req.ID)
		case "tools/call":
			http.Error(w, "Bad Request: аргумент title обязателен", http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	tools, err := c.Tools(context.Background())
	if err != nil || len(tools) != 1 || inits.Load() != 2 {
		t.Fatalf("после 400 про сессию: %v, %v, рукопожатий %d", tools, err, inits.Load())
	}
	if _, err := c.Call(context.Background(), "a", nil); err == nil || !strings.Contains(err.Error(), "аргумент title") {
		t.Errorf("обычный 400: %v", err)
	}
	if inits.Load() != 2 {
		t.Errorf("обычный 400 разорвал сессию: рукопожатий %d", inits.Load())
	}
}

// Ошибка инструмента приходит целиком и типизированной: суть у серверов
// вроде Planner лежит в глубине JSON, а обрезка на входе (раньше —
// двести знаков) уносила именно её.
func TestToolErrorKeepsWholeText(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.AuthRequired = false
	long := `{"code":"INVALID_FIELD","message":"REST request failed with HTTP 400.","details":{"raw":{"message":["` +
		strings.Repeat("Value must be a calendar-correct ISO-8601 datetime. ", 12) + `"]}}}`
	fake.CallResult = func(string, map[string]any) (string, bool) { return long, true }
	c := NewClient(fake.URL(), nil)
	text, err := c.Call(context.Background(), "task_update", map[string]any{"id": "T-1"})
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("ошибка инструмента не типизирована: %T %v", err, err)
	}
	if te.Text != long || text != long {
		t.Errorf("текст ошибки обрезан: %d знаков из %d", len([]rune(te.Text)), len([]rune(long)))
	}
	if !strings.HasPrefix(err.Error(), "инструмент вернул ошибку: {") {
		t.Errorf("Error(): %.80q", err.Error())
	}
}

// Статус 4xx/5xx сервера — HTTPError с телом, а не строка без структуры:
// отказ по аргументам часть серверов отдаёт именно так.
func TestHTTPErrorCarriesStatusAndBody(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.AuthRequired = false
	fake.CallStatus = func(string, map[string]any) (int, string) {
		return 422, `{"detail":[{"loc":["body","deadline"],"msg":"Input should be a valid datetime"}]}`
	}
	c := NewClient(fake.URL(), nil)
	_, err := c.Call(context.Background(), "task_update", nil)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 422 || !strings.Contains(he.Body, "valid datetime") {
		t.Fatalf("ошибка: %T %v", err, err)
	}
	if !strings.HasPrefix(err.Error(), "HTTP 422: ") {
		t.Errorf("Error(): %q", err.Error())
	}
}

// Ошибка JSON-RPC несёт код: -32602 значит «параметры не подошли».
func TestRPCErrorCarriesCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case len(req.ID) == 0:
			w.WriteHeader(http.StatusAccepted)
		case req.Method == "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"x"}}}`, req.ID)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"Invalid params: deadline"}}`, req.ID)
		}
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, nil).Call(context.Background(), "task_update", nil)
	var re *RPCError
	if !errors.As(err, &re) || re.Code != -32602 || !strings.Contains(re.Message, "deadline") {
		t.Fatalf("ошибка: %T %v", err, err)
	}
	if err.Error() != "Invalid params: deadline (код -32602)" {
		t.Errorf("Error(): %q", err.Error())
	}
}
