package engine

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/ca"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Сквозная проверка канала: настоящий TLS со взаимной проверкой,
// приглашение, вызов инструмента по каналу и запрет чужому.

// fakeAgent — агент с той стороны провода: отвечает на вызовы Сёмы тем же
// кадром, что и настоящий.
type fakeAgent struct {
	conn    net.Conn
	calls   chan string
	silent  atomic.Bool  // принимает вызовы и молчит: так проверяется обрыв
	reply   atomic.Value // что отвечать на «код.место»: map[string]any
	replies chan []byte  // ответы Сёмы на вопросы самого агента
}

// dialAgent поднимает приёмник Сёмы на свободном порту, входит по
// приглашению и соединяется с выданным сертификатом.
func dialAgent(t *testing.T, e *Engine, name string, can []string) *fakeAgent {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	if err := e.store.SetSetting("agents.listen", addr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = e.ServeAgents(ctx) }()

	code, err := e.NewInvite(name, RoleResident)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, pub, err := ca.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, rootPEM, _, err := e.Enroll(code, pub, can)
	if err != nil {
		t.Fatalf("вход по приглашению: %v", err)
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(rootPEM)) {
		t.Fatal("корень не читается")
	}
	var conn net.Conn
	for i := 0; i < 50; i++ { // приёмник поднимается в своей горутине
		conn, err = tls.Dial("tcp", addr, &tls.Config{Certificates: []tls.Certificate{pair},
			RootCAs: pool, ServerName: "sema", MinVersion: tls.VersionTLS13})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("соединение с Сёмой: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	a := &fakeAgent{conn: conn, calls: make(chan string, 8), replies: make(chan []byte, 8)}
	go a.serve()
	return a
}

// serve — агент отвечает на всё, что попросят: имя вызова уходит в canal
// для проверки, ответ — как у настоящего, полем text.
func (a *fakeAgent) serve() {
	r := bufio.NewReaderSize(a.conn, 1<<20)
	for {
		body, err := readFramed(r)
		if err != nil {
			return
		}
		var msg struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		if msg.Method == "" {
			select {
			case a.replies <- body:
			default:
			}
			continue
		}
		a.calls <- msg.Method + " " + string(msg.Params)
		if a.silent.Load() {
			continue
		}
		var result any = map[string]any{"text": "ответ на " + msg.Method}
		if strings.HasSuffix(msg.Method, ".умения") {
			result = map[string]any{"tools": []string{"symbols", "diagnostics"}}
		}
		if v := a.reply.Load(); v != nil && msg.Method == "код.место" {
			result = v
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
		fmt.Fprintf(a.conn, "Content-Length: %d\r\n\r\n%s", len(out), out)
	}
}

func (a *fakeAgent) wait(t *testing.T) string {
	t.Helper()
	select {
	case c := <-a.calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("агента не позвали")
		return ""
	}
}

// Канал целиком: агент вошёл по приглашению, Сёма его опознала по
// сертификату и позвала разрешённую способность.
func TestLinkCallEndToEnd(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	a := dialAgent(t, e, "мак", []string{"код"})
	waitLink(t, e, "мак")

	if _, err := e.AskAgent(context.Background(), "мак", "код", "код.symbols", map[string]any{"path": "x.go"}); err == nil {
		t.Fatal("неразрешённую способность позвали")
	}
	if err := e.AllowAgent("мак", "код", true); err != nil {
		t.Fatal(err)
	}
	raw, err := e.AskAgent(context.Background(), "мак", "код", "код.symbols", map[string]any{"path": "x.go"})
	if err != nil {
		t.Fatalf("вызов по каналу: %v", err)
	}
	if !strings.Contains(string(raw), "ответ на код.symbols") {
		t.Fatalf("ответ агента не дошёл: %s", raw)
	}
	if got := a.wait(t); !strings.Contains(got, "x.go") {
		t.Errorf("аргументы не доехали: %s", got)
	}
}

// Сервер MCP, привязанный к агенту, ходит по каналу, а не по HTTP: адреса
// у него нет вовсе.
func TestMCPOverLink(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	a := dialAgent(t, e, "мак", []string{"код"})
	waitLink(t, e, "мак")
	if err := e.AllowAgent("мак", "код", true); err != nil {
		t.Fatal(err)
	}
	m := &store.MCPServer{Name: "агент", Agent: "мак", Can: "код", Enabled: true}
	if err := st.SaveMCPServer(m); err != nil {
		t.Fatalf("сервер без адреса не сохранился: %v", err)
	}
	if err := e.CheckMCP(context.Background(), "агент"); err != nil {
		t.Fatalf("проверка по каналу: %v", err)
	}
	a.wait(t)
	saved, _ := st.MCPServer("агент")
	if len(saved.Tools) != 2 || saved.State != "ok" {
		t.Fatalf("умения агента не вычитались: %+v", saved)
	}
	// умения нового сервера запрещены, пока человек не решил: способность
	// разрешена на «Агентах», а вызовы инструментов — правилами сервера
	if res := e.RequestMCP(context.Background(), "тест", "агент", "symbols", nil); res.Policy != PolicyDeny {
		t.Fatalf("новое умение агента не запрещено по умолчанию: %+v", res)
	}
	decideMCP(t, st, "агент", map[string]string{"symbols": PolicyAllow})
	res := e.RequestMCP(context.Background(), "тест", "агент", "symbols", map[string]any{"path": "y.go"})
	if res.Err != "" {
		t.Fatalf("вызов по каналу: %s", res.Err)
	}
	if !strings.Contains(res.Text, "ответ на код.symbols") {
		t.Fatalf("ответ не дошёл: %q", res.Text)
	}
	if got := a.wait(t); !strings.Contains(got, "y.go") {
		t.Errorf("аргументы не доехали: %s", got)
	}
}

// Отозванный агент не соединяется вовсе: сертификат ещё годен, а пропуск
// уже нет.
func TestRevokedAgentCannotConnect(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	dialAgent(t, e, "мак", []string{"код"})
	waitLink(t, e, "мак")
	if err := e.RevokeAgent("мак"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.AgentLinks()) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := e.AllowAgent("мак", "код", true); err == nil {
		if _, err := e.AskAgent(context.Background(), "мак", "код", "код.symbols", nil); err == nil {
			t.Fatal("отозванный агент отвечает на вызовы")
		}
	}
}

func waitLink(t *testing.T, e *Engine, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.linkMu.Lock()
		l := e.links[name]
		e.linkMu.Unlock()
		if l != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("агент «%s» не появился на связи", name)
}

// Обрыв связи будит ожидающих сразу: иначе экран висит до таймаута.
func TestLinkBreakWakesCallers(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	a := dialAgent(t, e, "мак", []string{"код"})
	waitLink(t, e, "мак")
	if err := e.AllowAgent("мак", "код", true); err != nil {
		t.Fatal(err)
	}
	// агент замолчал: принимает вызов, но не отвечает
	a.silent.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := e.AskAgent(context.Background(), "мак", "код", "код.symbols", map[string]any{})
		done <- err
	}()
	a.wait(t)                         // вызов дошёл
	time.Sleep(50 * time.Millisecond) // ждущий встал в очередь
	_ = a.conn.Close()                // провод оборвался
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("вызов завершился успехом на оборванной связи")
		}
		if !strings.Contains(err.Error(), "оборвалась") {
			t.Errorf("причина не названа: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ожидающий не разбужен: висит до таймаута")
	}
}

// Пинг с номером — вопрос «ты здесь?»: на него приходит ответ сразу, и в
// журнал агента он не пишется. Шлюз спрашивает так раз в полминуты и по
// молчанию закрывает мёртвый провод (agent-gateway, 8.4); через журнал
// такой вопрос зарос бы строками «←ping» за сутки.
func TestAgentPingAnsweredQuietly(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	a := dialAgent(t, e, "мак", []string{"код"})
	waitLink(t, e, "мак")

	fmt.Fprintf(a.conn, "Content-Length: %d\r\n\r\n%s", len(`{"jsonrpc":"2.0","id":7,"method":"ping"}`),
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	select {
	case body := <-a.replies:
		var got struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.Unmarshal(body, &got); err != nil || got.ID != 7 || got.Error != nil || string(got.Result) != `"pong"` {
			t.Fatalf("на пинг ответ %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("на пинг с номером Сёма не ответил")
	}
	log, err := st.AgentLog("мак", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range log {
		if strings.Contains(u.What, "ping") {
			t.Fatalf("пинг попал в журнал агента: %+v", u)
		}
	}
}
