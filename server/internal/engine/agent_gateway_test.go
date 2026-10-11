package engine

// Шлюз со стороны Сёмы: звонит он, роли TLS прежние, управление и
// потоки — разные соединения на одном порту.
//
// Здесь настоящий TLS и настоящее приглашение: поддельный только тот, кто
// на другом конце. Проверять это вызовом функций мимо сокета было бы
// проверкой намерения, а не поведения.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/ca"
)

// fakeGateway — та сторона, куда звонит Сёма. Принимает TCP,
// становится на нём клиентом TLS, читает приветствие и разводит соединения:
// управление держит, потоки отдаёт тому, кто их просил.
type fakeGateway struct {
	t       *testing.T
	ln      net.Listener
	addr    string
	conf    *tls.Config
	mu      sync.Mutex
	ctl     net.Conn
	ctlOut  *bufio.Writer
	replies map[int]chan json.RawMessage
	nextID  int
	streams map[string]chan net.Conn
	pings   chan struct{}
	kinds   chan string
	closed  chan struct{}
}

// поднятьШлюза заводит агента роли «шлюз», кладёт его адрес в
// настройки Сёмы и начинает принимать его звонки.
func raiseGateway(t *testing.T, e *Engine, name string) *fakeGateway {
	t.Helper()
	code, err := e.NewInvite(name, RoleGateway)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, pub, err := ca.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, rootPEM, role, err := e.Enroll(code, pub, []string{CanGateway})
	if err != nil {
		t.Fatalf("вход шлюза: %v", err)
	}
	if role != RoleGateway {
		t.Fatalf("роль в ответе не шлюз: %q", role)
	}
	if err := e.AllowAgent(name, CanGateway, true); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(rootPEM)) {
		t.Fatal("корень не читается")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGateway{t: t, ln: ln, addr: ln.Addr().String(),
		conf: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool,
			ServerName: "sema", MinVersion: tls.VersionTLS13},
		replies: map[int]chan json.RawMessage{}, streams: map[string]chan net.Conn{},
		pings: make(chan struct{}, 64), kinds: make(chan string, 64), closed: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close(); close(g.closed) })
	go g.accept()
	if err := e.store.SetSetting(gatewayAddrKey, g.addr); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *fakeGateway) accept() {
	for {
		raw, err := g.ln.Accept()
		if err != nil {
			return
		}
		go g.parse(raw)
	}
}

func (g *fakeGateway) parse(raw net.Conn) {
	conn := tls.Client(raw, g.conf)
	if err := conn.Handshake(); err != nil {
		_ = raw.Close()
		return
	}
	kind, stream, err := readHello(conn)
	if err != nil {
		_ = conn.Close()
		return
	}
	select {
	case g.kinds <- kind:
	default:
	}
	switch kind {
	case ConnControl:
		g.holdControl(conn)
	case ConnStream:
		g.mu.Lock()
		ch := g.streams[stream]
		g.mu.Unlock()
		if ch == nil {
			_ = conn.Close()
			return
		}
		ch <- conn
	default:
		_ = conn.Close()
	}
}

// readHello — первый кадр без забегания вперёд. Читать его через
// bufio нельзя: буфер утащил бы начало потока, и первые байты запроса
// пропали бы молча.
func readHello(r io.Reader) (kind, stream string, err error) {
	var head []byte
	one := make([]byte, 1)
	for {
		if len(head) > 512 {
			return "", "", fmt.Errorf("приветствие без конца заголовка")
		}
		if _, err := io.ReadFull(r, one); err != nil {
			return "", "", err
		}
		head = append(head, one[0])
		if strings.HasSuffix(string(head), "\r\n\r\n") {
			break
		}
	}
	var length int
	for _, line := range strings.Split(strings.TrimSpace(string(head)), "\r\n") {
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &length); err != nil {
				return "", "", err
			}
		}
	}
	if length <= 0 || length > 1<<16 {
		return "", "", fmt.Errorf("плохая длина приветствия: %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", "", err
	}
	var p connHello
	if err := json.Unmarshal(body, &p); err != nil {
		return "", "", err
	}
	return p.Kind, p.Stream, nil
}

func (g *fakeGateway) holdControl(conn net.Conn) {
	g.mu.Lock()
	g.ctl, g.ctlOut = conn, bufio.NewWriter(conn)
	g.mu.Unlock()
	// как настоящий: первым делом рассказываем, кто мы, какой роли и что умеем
	g.send(map[string]any{"jsonrpc": "2.0", "method": "hello",
		"params": map[string]any{"can": []string{CanGateway}, "role": RoleGateway, "version": "тест"}})
	r := bufio.NewReaderSize(conn, 1<<20)
	for {
		body, err := readFramed(r)
		if err != nil {
			g.mu.Lock()
			if g.ctl == conn {
				g.ctl, g.ctlOut = nil, nil
			}
			g.mu.Unlock()
			return
		}
		var msg struct {
			ID     *int                      `json:"id"`
			Method string                    `json:"method"`
			Result json.RawMessage           `json:"result"`
			Error  *struct{ Message string } `json:"error"`
		}
		if json.Unmarshal(body, &msg) != nil || msg.ID == nil {
			continue
		}
		if msg.Method == "" {
			g.mu.Lock()
			ch := g.replies[*msg.ID]
			delete(g.replies, *msg.ID)
			g.mu.Unlock()
			if ch != nil {
				if msg.Error != nil {
					ch <- json.RawMessage(`{"__error":"` + msg.Error.Message + `"}`)
				} else {
					ch <- msg.Result
				}
			}
			continue
		}
		// Сёма нас о чём-то спрашивает — отвечаем состоянием, как настоящий
		res := map[string]any{"домен": "sema.example.org", "край": "nginx",
			"сертификат": "за nginx — срок смотрите у certbot", "уровень": "ok",
			"браузерам": "127.0.0.1:8770", "пропущено": 3, "отсеяно": 7, "закрыто": 1,
			"не внесено": 2}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": res})
		g.sendRaw(out)
	}
}

func (g *fakeGateway) send(v any) {
	body, _ := json.Marshal(v)
	g.sendRaw(body)
}

func (g *fakeGateway) sendRaw(body []byte) {
	g.mu.Lock()
	out := g.ctlOut
	g.mu.Unlock()
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(body), body)
	_ = out.Flush()
}

// live — стоит ли управляющий провод.
func (g *fakeGateway) live() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ctl != nil
}

func (g *fakeGateway) waitWire(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if g.live() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Сёма не позвонил шлюзу")
}

// ask — попросить Сёму открыть поток и дождаться соединения под него.
func (g *fakeGateway) ask(t *testing.T) net.Conn {
	t.Helper()
	token := NewStreamNumber()
	ch := make(chan net.Conn, 1)
	want := make(chan json.RawMessage, 1)
	g.mu.Lock()
	g.streams[token] = ch
	g.nextID++
	id := g.nextID
	g.replies[id] = want
	g.mu.Unlock()
	g.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": CanGateway + ".поток",
		"params": map[string]any{"поток": token}})
	select {
	case raw := <-want:
		if strings.Contains(string(raw), "__error") {
			t.Fatalf("Сёма отказался открывать поток: %s", raw)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Сёма не ответил на просьбу о потоке")
	}
	select {
	case c := <-ch:
		t.Cleanup(func() { _ = c.Close() })
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("соединение под поток не пришло")
		return nil
	}
}

// up — куда шлюз вносит запросы: обычный HTTP-сервер вместо самого
// Сёмы. Проверяем перекачку байтов, а не сборку экранов.
func up(t *testing.T, e *Engine, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if err := e.store.SetSetting(gatewayUpKey, strings.TrimPrefix(srv.URL, "http://")); err != nil {
		t.Fatal(err)
	}
	return srv
}

// Соединение открывает Сёма: дома не нужен ни один открытый порт.
func TestGatewayCallsSema(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	g := raiseGateway(t, e, "край")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.DialGateway(ctx)
	g.waitWire(t)
	waitLink(t, e, "край")

	select {
	case kind := <-g.kinds:
		if kind != ConnControl {
			t.Fatalf("первым кадром пришло %q, а ждали «%s»", kind, ConnControl)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Сёма не сказал, зачем соединение")
	}
	if !strings.Contains(strings.Join(e.AgentLinks(), " "), "край") {
		t.Fatalf("шлюза нет среди живых соединений: %v", e.AgentLinks())
	}
	// и он же виден как шлюз, а не как житель
	a, ok := e.GatewayAgent()
	if !ok || a.Name != "край" {
		t.Fatalf("шлюз не найден по роли: %+v", a)
	}
}

// Поток идёт своим соединением на тот же порт, и по нему проходит обычный
// HTTP: и запрос, и ответ.
func TestStreamOwnConn(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	up(t, e, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "экран %s для %s", r.URL.Path, r.Header.Get("X-Forwarded-For"))
	}))
	g := raiseGateway(t, e, "край")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.DialGateway(ctx)
	g.waitWire(t)

	conn := g.ask(t)
	req, _ := http.NewRequest("GET", "http://sema/api/ui?screen=health", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if err := req.Write(conn); err != nil {
		t.Fatalf("запрос не ушёл в поток: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("ответ по потоку не прочитан: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "/api/ui") || !strings.Contains(string(body), "203.0.113.7") {
		t.Fatalf("байты по потоку доехали не те: %q", body)
	}
	// и это было отдельное соединение, а не управляющий провод
	if !g.live() {
		t.Fatal("управляющий провод закрылся вместе с потоком")
	}
}

// Тяжёлый поток не задерживает ни управление, ни другие потоки: замер, а не
// предположение.
func TestHeavyStreamNotHoldsRest(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	slow := make(chan struct{})
	up(t, e, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/файл" {
			// «вложение», которое отдаётся долго: пока его не отпустят,
			// соединение занято
			w.Header().Set("Content-Length", "8")
			_, _ = w.Write([]byte("123"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-slow
			_, _ = w.Write([]byte("45678"))
			return
		}
		fmt.Fprint(w, "быстрый ответ")
	}))
	g := raiseGateway(t, e, "край")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.DialGateway(ctx)
	g.waitWire(t)

	// тяжёлый поток пошёл и встал
	heavy := g.ask(t)
	heavyRequest, _ := http.NewRequest("GET", "http://sema/файл", nil)
	if err := heavyRequest.Write(heavy); err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 3)
	// дождались, что байты пошли: соединение точно занято
	_ = heavy.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(bufio.NewReaderSize(heavy, 1), first); err != nil {
		t.Fatalf("тяжёлый поток не начался: %v", err)
	}

	// пока он стоит, просим второй и меряем
	started := time.Now()
	fast := g.ask(t)
	fastRequest, _ := http.NewRequest("GET", "http://sema/api/ui", nil)
	if err := fastRequest.Write(fast); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(fast), fastRequest)
	if err != nil {
		t.Fatalf("второй поток не ответил, пока шёл первый: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	sent := time.Since(started)
	close(slow)
	if !strings.Contains(string(body), "быстрый ответ") {
		t.Fatalf("второй поток ответил не тем: %q", body)
	}
	if sent > 3*time.Second {
		t.Fatalf("второй поток ждал первого %s — потоки делят соединение", sent.Round(time.Millisecond))
	}
	t.Logf("второй поток при занятом первом ответил за %s", sent.Round(time.Millisecond))
	// и управление всё это время живо
	if !g.live() {
		t.Fatal("управляющий провод не пережил тяжёлого потока")
	}
}

// Обрыв потока не роняет управляющий провод.
func TestDropStreamNotFailsWire(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	up(t, e, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ок")
	}))
	g := raiseGateway(t, e, "край")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.DialGateway(ctx)
	g.waitWire(t)

	conn := g.ask(t)
	_ = conn.Close() // поток оборвался, не дождавшись ответа
	time.Sleep(300 * time.Millisecond)
	if !g.live() {
		t.Fatal("оборванный поток уронил управляющий провод")
	}
	// и следующий поток открывается как ни в чём не бывало
	second := g.ask(t)
	req, _ := http.NewRequest("GET", "http://sema/api/ui", nil)
	if err := req.Write(second); err != nil {
		t.Fatal(err)
	}
	if _, err := http.ReadResponse(bufio.NewReader(second), req); err != nil {
		t.Fatalf("после обрыва потоки не открываются: %v", err)
	}
}

// Сертификат не от Сёмы — соединения нет. Заодно: подменённый адрес ничего
// не даёт, потому что рукопожатие не сходится.
func TestForeignOnAddrGateway(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	// на адресе сидит кто-то со своим центром
	foreign, err := ca.NewRoot("Чужой", 1)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, pub, _ := ca.NewKey()
	certPEM, _ := ca.Sign(foreign, "край", pub, 30)
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				c := tls.Client(raw, &tls.Config{Certificates: []tls.Certificate{pair},
					InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) // #nosec G402 — это подделка в тесте
				_ = c.Handshake()
				_ = c.Close()
			}()
		}
	}()
	if err := e.store.SetSetting(gatewayAddrKey, ln.Addr().String()); err != nil {
		t.Fatal(err)
	}
	err = e.dialGatewayOnce(context.Background(), ln.Addr().String())
	if err == nil {
		t.Fatal("Сёма заговорил с чужим сертификатом на адресе шлюза")
	}
	if !strings.Contains(err.Error(), "рукопожатие") && !strings.Contains(err.Error(), "не выдавал") {
		t.Fatalf("причина отказа не названа: %v", err)
	}
	// и это видно в журнале, а не только в ошибке
	log, _ := e.store.AgentLog("", 20)
	found := false
	for _, u := range log {
		if !u.OK {
			found = true
		}
	}
	if !found {
		t.Error("отказ не записан в журнал агентов")
	}
}

// Шлюз недоступен — Сёма дома работает как прежде. Это требование, и
// его легко потерять: адрес шлюза не должен становиться условием
// работы.
func TestHomeWorksWithoutGateway(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	// адрес есть, а на нём никого: порт закрыт правилом роутера, машина
	// выключена — для Сёмы это одно и то же
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := dead.Addr().String()
	_ = dead.Close()
	if err := e.store.SetSetting(gatewayAddrKey, addr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.DialGateway(ctx)

	// житель машины подключается и отвечает, как будто шлюза и не было
	a := dialAgent(t, e, "мак", []string{"код"})
	waitLink(t, e, "мак")
	if err := e.AllowAgent("мак", "код", true); err != nil {
		t.Fatal(err)
	}
	raw, err := e.AskAgent(context.Background(), "мак", "код", "код.symbols", map[string]any{"path": "x.go"})
	if err != nil {
		t.Fatalf("с мёртвым шлюзом своя сеть перестала работать: %v", err)
	}
	if !strings.Contains(string(raw), "ответ на код.symbols") {
		t.Fatalf("ответ жителя не дошёл: %s", raw)
	}
	a.wait(t)
	// и состояние честное: провода нет
	if len(e.AgentLinks()) != 1 {
		t.Fatalf("живых соединений должно быть одно (житель), а их %v", e.AgentLinks())
	}
	if _, has := e.GatewayAgent(); has {
		t.Fatal("шлюз взялся из воздуха: его не заводили")
	}

	// Мёртвого порта мало: он отказывает мгновенно, и такой проверкой можно
	// проспать замок, удержанный через звонок. Худший случай — чёрная дыра:
	// сокет принимают и молчат, и звонок висит все десять секунд рукопожатия.
	// Дома в это время должно отвечаться так же быстро, как без шлюза.
	hole, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hole.Close() })
	called := make(chan struct{}, 1)
	go func() {
		for {
			c, err := hole.Accept()
			if err != nil {
				return
			}
			// ни байта в ответ: рукопожатие Сёмы упрётся в свой срок
			t.Cleanup(func() { _ = c.Close() })
			select {
			case called <- struct{}{}:
			default:
			}
		}
	}()
	if err := e.store.SetSetting(gatewayAddrKey, hole.Addr().String()); err != nil {
		t.Fatal(err)
	}
	// Мерить, не дождавшись звонка, бессмысленно: переподключение идёт с
	// растущей паузой, и в первые доли секунды Сёма ещё спит, а не висит.
	select {
	case <-called:
	case <-time.After(30 * time.Second):
		t.Fatal("Сёма так и не позвонил на новый адрес: проверять задержку не на чем")
	}
	started := time.Now()
	if _, err := e.AskAgent(context.Background(), "мак", "код", "код.symbols", map[string]any{"path": "y.go"}); err != nil {
		t.Fatalf("Сёма висит на звонке шлюзу, а житель не отвечает: %v", err)
	}
	if sent := time.Since(started); sent > 3*time.Second {
		t.Fatalf("житель ответил за %s: застрявший звонок шлюзу задерживает своих",
			sent.Round(time.Millisecond))
	}
}

// Роль называет машина, а помнит Сёма: житель, назвавшийся шлюзом, не
// становится им — соединение отклоняется.
func TestAgentWontCallItselfGateway(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	code, err := e.NewInvite("мак", RoleResident)
	if err != nil {
		t.Fatal(err)
	}
	_, pub, _ := ca.NewKey()
	if _, _, role, err := e.Enroll(code, pub, []string{"код"}); err != nil || role != RoleResident {
		t.Fatalf("житель не завёлся: роль %q, ошибка %v", role, err)
	}
	if e.rolesMatch("мак", RoleGateway) {
		t.Fatal("житель назвался шлюзом, и Сёма согласился")
	}
	if !e.rolesMatch("мак", RoleResident) {
		t.Fatal("житель назвался жителем, а Сёма не согласился")
	}
	// удостоверение, выданное до появления ролей, знакомится молча — и это
	// по-прежнему житель
	if !e.rolesMatch("мак", "") {
		t.Fatal("старое удостоверение без роли перестало знакомиться")
	}
	// отказ виден человеку, а не только в коде
	if n, ok := st.OpenNotificationByKey("agent-role:мак"); !ok || n.Level != "alert" {
		t.Fatalf("тревоги о чужой роли нет: %+v", n)
	}
}

// Шлюз один: второй звонил бы по тому же адресу, и Сёма не знал бы,
// чей это провод.
func TestGatewayOnlyOne(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	code, err := e.NewInvite("край", RoleGateway)
	if err != nil {
		t.Fatal(err)
	}
	_, pub, _ := ca.NewKey()
	if _, _, _, err := e.Enroll(code, pub, []string{CanGateway}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NewInvite("второй край", RoleGateway); err == nil {
		t.Fatal("завели второго шлюза")
	}
	// а тому же имени приглашение выписывается: это замена, а не второй
	if _, err := e.NewInvite("край", RoleGateway); err != nil {
		t.Fatalf("приглашение прежнему шлюзу не выписалось: %v", err)
	}
	// и роль, которой Сёма не знает, приглашением не становится
	if _, err := e.NewInvite("кто-то", "сторож"); err == nil {
		t.Fatal("выписали приглашение на выдуманную роль")
	}
}

// Опасное сочетание Сёма называет вслух: шлюз есть, а вход без пароля
// из своей сети остался включённым.
func TestDangerousCombinationNamed(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	// у новой установки вход без проверки личности выключен — сочетание
	// возникает у Сёмы, где его включили (или оставила миграция с прежней версии)
	if err := st.SetSetting("auth.local_open", "true"); err != nil {
		t.Fatal(err)
	}
	if w := e.GatewayWarning(); w != "" {
		t.Fatalf("без шлюза предупреждать не о чем, а сказано: %q", w)
	}
	if err := st.SetSetting(gatewayAddrKey, "край:8767"); err != nil {
		t.Fatal(err)
	}
	w := e.GatewayWarning()
	if !strings.Contains(w, "без пароля") {
		t.Fatalf("про вход без пароля из своей сети не сказано: %q", w)
	}
	if !strings.Contains(w, "прокси") {
		t.Fatalf("про доверие заголовкам прокси не сказано: %q", w)
	}
	// человек исправил — предупреждения нет
	if err := st.SetSetting("auth.local_open", "false"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("auth.trust_proxy", "true"); err != nil {
		t.Fatal(err)
	}
	if w := e.GatewayWarning(); w != "" {
		t.Fatalf("правила исправлены, а предупреждение осталось: %q", w)
	}
}

// Поток без номера и поток при незаданном адресе — отказ с причиной, а не
// молчание: шлюз должен понять, что случилось.
func TestStreamWithoutNumberDenial(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	code, _ := e.NewInvite("край", RoleGateway)
	_, pub, _ := ca.NewKey()
	if _, _, _, err := e.Enroll(code, pub, []string{CanGateway}); err != nil {
		t.Fatal(err)
	}
	if err := e.AllowAgent("край", CanGateway, true); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetSetting(gatewayAddrKey, "край:8767"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.handleAgentAsk(context.Background(), "край", CanGateway+".поток", json.RawMessage(`{}`)); err == nil {
		t.Fatal("поток без номера приняли")
	}
	// и способность жителя шлюзу не открывается этим же путём
	if _, err := e.handleAgentAsk(context.Background(), "край", CanMemory+".memory_recall", json.RawMessage(`{}`)); err == nil {
		t.Fatal("шлюз дотянулся до памяти")
	}
	// а житель не дотягивается до потоков
	code2, _ := e.NewInvite("мак", RoleResident)
	_, pub2, _ := ca.NewKey()
	if _, _, _, err := e.Enroll(code2, pub2, []string{"код"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AllowAgent("мак", CanGateway, true); err != nil {
		t.Fatal(err)
	}
	_, err := e.handleAgentAsk(context.Background(), "мак", CanGateway+".поток",
		json.RawMessage(`{"поток":"abc"}`))
	if err == nil || !strings.Contains(err.Error(), "не шлюз") {
		t.Fatalf("житель с выданным тумблером открыл поток: %v", err)
	}
}

// Состояние шлюза разбирается целиком: у двух репозиториев нет кодовой
// зависимости, и договор между ними держится на одних и тех же словах ключей.
// Опечатка в ключе ничего не сломает — графа просто станет нулём, и человек
// решит, что край ничего не отсеял. Поэтому список ключей написан здесь
// дословно: разошлись — падает эта проверка, а не человек упирается в ноль.
//
// Та же проверка со стороны шлюза — TestСостояниеНазываетКрай в
// internal/gate: там она сверяет, что он отдаёт ровно эти слова.
func TestStateGatewayParsed(t *testing.T) {
	const reply = `{"домен":"sema.example.org","край":"nginx",
		"сертификат":"за краем — срок смотрите там, где выписан сертификат","уровень":"warn",
		"браузерам":"127.0.0.1:8770","сёме":"0.0.0.0:8767",
		"пропущено":11,"отсеяно":4,"закрыто":2,"потоков":3,"не внесено":5}`
	var look GatewayLook
	if err := json.Unmarshal([]byte(reply), &look); err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	if look.Domain == "" {
		empty = append(empty, "домен")
	}
	if look.Edge == "" {
		empty = append(empty, "край")
	}
	if look.Cert == "" {
		empty = append(empty, "сертификат")
	}
	if look.Level == "" {
		empty = append(empty, "уровень")
	}
	if look.Browsers == "" {
		empty = append(empty, "браузерам")
	}
	if look.Sema == "" {
		empty = append(empty, "сёме")
	}
	if look.Skipped != 11 {
		empty = append(empty, "пропущено")
	}
	if look.Excluded != 4 {
		empty = append(empty, "отсеяно")
	}
	if look.Closed != 2 {
		empty = append(empty, "закрыто")
	}
	if look.Streams != 3 {
		empty = append(empty, "потоков")
	}
	// ключ с пробелом внутри — самое хрупкое место договора
	if look.NotRecorded != 5 {
		empty = append(empty, "не внесено")
	}
	if len(empty) > 0 {
		t.Fatalf("шлюз это сказал, а Сёма не услышал: %v", empty)
	}
}

// Копия после -restore шлюзу не звонит, пока привязка не подтверждена: у
// неё то же удостоверение, и новый звонок закрывает прежний — оригинал и
// копия выбивали бы друг друга каждую секунду (ревью agent-gateway 8.2).
func TestRestoredCopyWaitsBindingBeforeGateway(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	g := raiseGateway(t, e, "край")
	if err := os.WriteFile(filepath.Join(e.conf.Data, bindingMarker), []byte("копия x · снята 2026-10-05"), 0o644); err != nil {
		t.Fatal(err)
	}
	was := gatewayBindingPoll
	gatewayBindingPoll = 20 * time.Millisecond
	t.Cleanup(func() { gatewayBindingPoll = was })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.DialGateway(ctx)

	// без привязки звонок ушёл бы сразу; полсекунды тишины — не звонит
	select {
	case kind := <-g.kinds:
		t.Fatalf("копия без привязки позвонила шлюзу (%q)", kind)
	case <-time.After(500 * time.Millisecond):
	}
	if err := e.ConfirmBinding(); err != nil {
		t.Fatal(err)
	}
	g.waitWire(t)
}
