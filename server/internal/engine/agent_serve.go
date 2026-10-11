package engine

// Приём агентов: TLS со взаимной проверкой и JSON-RPC поверх соединения.
// Звонит агент, а не Сёма: ноутбук засыпает, меняет сеть и стоит за
// домашним роутером, а у Сёмы адрес постоянный.
//
// Разбор сообщений тот же, что у языковых серверов: заголовок с длиной и
// тело. Второй раз изобретать незачем.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/ca"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// link — живое соединение с агентом.
type link struct {
	agent       string
	fingerprint string
	conn        net.Conn
	out         *bufio.Writer
	mu          sync.Mutex
	waitMu      sync.Mutex
	waiting     map[int]chan json.RawMessage
	id          int
	since       time.Time
}

const (
	linkHeaderLimit   = 8 << 10
	linkRequestLimit  = 1 << 20
	linkMaxRequests   = 8
	linkMaxHandshakes = 32
	linkHandshakeWait = 10 * time.Second
	linkReadWait      = 5 * time.Minute // агент посылает ping раз в две минуты
)

// AgentLinks — живые соединения; экран показывает их честно, а не
// притворяется, что агент всегда рядом.
func (e *Engine) AgentLinks() []string {
	e.linkMu.Lock()
	defer e.linkMu.Unlock()
	var out []string
	for name, l := range e.links {
		out = append(out, fmt.Sprintf("%s · на связи %s", name, time.Since(l.since).Round(time.Second)))
	}
	return out
}

// ServeAgents слушает агентов, если адрес задан. Без адреса Сёма
// соединений из сети не принимает вовсе.
// ServeAgents слушает агентов и переслушивает, когда человек поменял
// адрес на экране: раньше настройка применялась только после перезапуска,
// а человек видел «сохранено» и ждал звонка, которого не будет.
func (e *Engine) ServeAgents(ctx context.Context) error {
	for {
		addr := e.AgentListen()
		if addr == "" {
			// адреса нет — ждём, вдруг человек его впишет
			if !e.waitListenChange(ctx, addr) {
				return nil
			}
			continue
		}
		if err := e.serveAgentsOn(ctx, addr); err != nil {
			log.Printf("агенты: %v", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		if !e.waitListenChange(ctx, addr) {
			return nil
		}
	}
}

// waitListenChange ждёт, пока адрес в настройках станет другим. false —
// сервер останавливается.
func (e *Engine) waitListenChange(ctx context.Context, was string) bool {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if e.AgentListen() != was {
				return true
			}
		}
	}
}

func (e *Engine) serveAgentsOn(ctx context.Context, addr string) error {
	root, err := e.EnsureRoot()
	if err != nil {
		return err
	}
	srv, err := e.EnsureServerCert()
	if err != nil {
		return err
	}
	cert, err := tls.X509KeyPair([]byte(srv.CertPEM), []byte(srv.KeyPEM))
	if err != nil {
		return fmt.Errorf("сертификат Сёмы не годится: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(root.CertPEM)) {
		return fmt.Errorf("корень не читается")
	}
	conf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert, // пропуск — сертификат, а не пароль
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}
	ln, err := tls.Listen("tcp", addr, conf)
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("агенты: слушаю %s (взаимная проверка сертификатов)", addr)
	watch, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go func() {
		// закрываем слушателя и при остановке сервера, и когда человек
		// поменял адрес: старый порт не должен держаться зря
		e.waitListenChange(watch, addr)
		_ = ln.Close()
	}()
	handshakes := make(chan struct{}, linkMaxHandshakes)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("агенты: соединение не принято: %v", err)
			continue
		}
		select {
		case handshakes <- struct{}{}:
			go e.handleAgentReady(ctx, conn, func() { <-handshakes })
		default:
			_ = conn.Close()
		}
	}
}

func (e *Engine) handleAgent(ctx context.Context, conn net.Conn) {
	e.handleAgentReady(ctx, conn, func() {})
}

func (e *Engine) handleAgentReady(ctx context.Context, conn net.Conn, ready func()) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var release sync.Once
	defer release.Do(ready)
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return
	}
	if err := agentHandshake(ctx, tc, linkHandshakeWait); err != nil {
		log.Printf("агенты: рукопожатие не вышло: %v", err)
		return
	}
	release.Do(ready)
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs[0].Raw}))
	a, err := e.checkCaller(certPEM)
	if err != nil {
		log.Printf("агенты: %v", err)
		return
	}
	l := &link{agent: a.Name, fingerprint: a.Fingerprint, conn: conn, out: bufio.NewWriter(conn),
		waiting: map[int]chan json.RawMessage{}, since: time.Now()}
	e.linkMu.Lock()
	// Отзыв или перевыпуск мог произойти между проверкой сертификата и
	// установкой провода. Под тем же замком отзыв закрывает соединение.
	current, exists := e.store.Agent(a.Name)
	if !exists || current.State != store.AgentOK || current.Fingerprint != a.Fingerprint {
		e.linkMu.Unlock()
		return
	}
	if old := e.links[a.Name]; old != nil {
		_ = old.conn.Close() // одно соединение на агента: второе вытесняет первое
	}
	e.links[a.Name] = l
	e.linkMu.Unlock()
	e.store.LogAgent(store.AgentUse{Agent: a.Name, What: "соединение", OK: true, Detail: ca.Fingerprint(certPEM)})
	// вернулся — закрываем уведомление об обрыве. Считаем разлуку по
	// прежней отметке «был на связи»: её TouchAgent обновит уже сейчас
	e.agentBack(a.Name, a.Seen)
	log.Printf("агенты: «%s» на связи", a.Name)

	defer e.linkClosed(a.Name, l)
	l.read(ctx, e, a.Name)
}

func agentHandshake(ctx context.Context, conn *tls.Conn, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	return conn.HandshakeContext(ctx)
}

// linkClosed — соединение закрылось. Вытесненное соединение обрывом не
// считается: агент уже перезвонил, и в карте висит новое. Мак после сна
// оставляет мёртвый TCP жить минутами, так что это не редкий случай, а
// каждое утро — уведомление приходило бы при живом агенте.
func (e *Engine) linkClosed(name string, l *link) {
	e.linkMu.Lock()
	live := e.links[name] == l
	if live {
		delete(e.links, name)
	}
	e.linkMu.Unlock()
	held := time.Since(l.since)
	e.store.LogAgent(store.AgentUse{Agent: name, What: "соединение закрыто", OK: true,
		Detail: "держалось " + held.Round(time.Second).String()})
	if live {
		e.agentGone(name, held)
	}
	log.Printf("агенты: «%s» отключился", name)
}

// read — единственный читатель соединения: ответы раздаёт по номерам,
// а объявления агента о себе применяет.
func (l *link) read(parent context.Context, e *Engine, name string) {
	// связь оборвалась — тем, кто ждёт ответа, надо сказать сразу, а не
	// держать их до таймаута: экран выглядит зависшим на минуты
	ctx, cancel := context.WithCancel(parent)
	var calls sync.WaitGroup
	defer func() {
		cancel()
		_ = l.conn.Close()
		l.failWaiting("связь с агентом оборвалась")
		calls.Wait()
	}()
	requests := make(chan struct{}, linkMaxRequests)
	r := bufio.NewReaderSize(l.conn, linkHeaderLimit)
	for {
		_ = l.conn.SetReadDeadline(time.Now().Add(linkReadWait))
		body, err := readFramed(r)
		if err != nil {
			return
		}
		a, ok := e.store.Agent(name)
		if !ok || a.State != store.AgentOK || a.Fingerprint != l.fingerprint {
			return
		}
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params json.RawMessage `json:"params"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		if msg.Method != "" && len(body) > linkRequestLimit {
			return
		}
		switch {
		case msg.ID != nil && msg.Method == "":
			l.waitMu.Lock()
			ch := l.waiting[*msg.ID]
			delete(l.waiting, *msg.ID)
			l.waitMu.Unlock()
			if ch == nil {
				continue
			}
			if msg.Error != nil {
				ch <- json.RawMessage(`{"__error":` + strconv.Quote(msg.Error.Message) + `}`)
				continue
			}
			ch <- msg.Result
		case msg.ID != nil && msg.Method == "ping":
			// пинг со сроком ответа: так агент проверяет, что провод жив в
			// обе стороны (шлюз — раз в полминуты). Отвечаем сразу и без
			// журнала, иначе журнал агента зарос бы строками «←ping». Сначала
			// ответ, потом запись в базу: занятая база не должна держать понг
			_ = l.write([]byte(`{"jsonrpc":"2.0","id":` + strconv.Itoa(*msg.ID) + `,"result":"pong"}`))
			e.store.TouchAgent(name)
		case msg.ID != nil:
			// агент спрашивает Сёму: то же соединение, те же кадры, но
			// в обратную сторону. Отвечаем в своей горутине, чтобы долгий
			// ответ не держал чтение провода
			select {
			case requests <- struct{}{}:
			default:
				// Переполнение закрывает провод и отменяет уже начатые
				// запросы: очередь не может расти без ограничения.
				return
			}
			e.store.TouchAgent(name)
			calls.Add(1)
			go func() {
				defer calls.Done()
				defer func() { <-requests }()
				l.answerAgent(ctx, e, name, *msg.ID, msg.Method, msg.Params)
			}()
		case msg.Method == "hello":
			// агент рассказывает, что умеет: разрешает человек, поэтому
			// новые способности появляются выключенными
			var p struct {
				Can     []string `json:"can"`
				Role    string   `json:"role"`
				Version string   `json:"version"`
			}
			if json.Unmarshal(msg.Params, &p) == nil {
				// роль называет машина, а помнит Сёма. Расхождение — отказ:
				// иначе житель машины назвался бы шлюзом и получил бы
				// его полномочия одним словом в кадре
				if !e.rolesMatch(name, p.Role) {
					return
				}
				e.noteCapabilities(name, p.Can)
			}
			e.store.TouchAgent(name)
		case msg.Method == "ping":
			e.store.TouchAgent(name)
		}
	}
}

// rolesMatch — та ли роль у агента, которой он себя назвал. Пустое слово
// читается как «житель» — так знакомятся удостоверения, выданные до
// появления ролей; они жителю и принадлежат.
//
// Несовпадение — отказ с названной причиной, а не тихое принятие: роль
// решает, какие способности агенту вообще положены, и её выбрал человек,
// когда выдавал код.
func (e *Engine) rolesMatch(name, said string) bool {
	a, ok := e.store.Agent(name)
	if !ok {
		return false
	}
	remember := AgentRole(a)
	named, _ := KnownRole(said)
	if named == remember {
		return true
	}
	e.store.LogAgent(store.AgentUse{Agent: name, What: "знакомство", OK: false,
		Detail: fmt.Sprintf("назвался ролью «%s», а закреплена «%s» — соединение отклонено", named, remember)})
	log.Printf("агенты: «%s» назвался ролью «%s», а закреплена «%s» — отклоняю", name, named, remember)
	_, _ = e.Notify(&store.Notification{
		Key: "agent-role:" + name, Source: "core", Level: "alert",
		Title:   "Агент «" + name + "» назвался чужой ролью",
		Summary: "закреплена «" + remember + "», назвался «" + named + "» — соединение отклонено",
		Fields: []store.NotifyField{
			{Label: "Закреплена", Value: remember},
			{Label: "Назвался", Value: named, Level: "alert"},
			{Label: "Что делаем", Value: "соединение отклоняется; сменить роль можно только новым приглашением"},
		},
		Actions: []store.NotifyAction{
			{Label: "Открыть агента", Kind: "goto", Screen: AgentScreen(name, ""), Style: "primary"},
			{Label: "Отозвать доступ", Kind: "core", Op: "revoke_agent", Ref: name},
		},
	})
	return false
}

// noteCapabilities — новые способности запоминаются выключенными.
func (e *Engine) noteCapabilities(name string, can []string) {
	e.agentMu.Lock()
	defer e.agentMu.Unlock()
	a, ok := e.store.Agent(name)
	if !ok || a.State != store.AgentOK {
		return
	}
	known := map[string]bool{}
	for _, c := range a.Can {
		known[c] = true
	}
	fresh := false
	for _, c := range can {
		if !known[c] {
			a.Can = append(a.Can, c)
			fresh = true
		}
	}
	if fresh {
		a.Seen = time.Now().Format(time.RFC3339)
		_ = e.store.SaveAgent(a)
		log.Printf("агенты: «%s» объявил новые способности: %s", name, strings.Join(can, ", "))
	}
}

// AskAgent — вызов способности агента. Неразрешённая не вызывается вовсе.
func (e *Engine) AskAgent(ctx context.Context, name, can, method string, params any) (json.RawMessage, error) {
	if !e.AgentAllowed(name, can) {
		return nil, fmt.Errorf("способность «%s» агенту «%s» не разрешена", can, name)
	}
	e.linkMu.Lock()
	l := e.links[name]
	e.linkMu.Unlock()
	if l == nil {
		return nil, fmt.Errorf("агент «%s» сейчас не на связи", name)
	}
	raw, err := l.call(ctx, method, params)
	e.store.LogAgent(store.AgentUse{Agent: name, What: method, OK: err == nil,
		Detail: errText(err)})
	return raw, err
}

// failWaiting будит всех ожидающих ошибкой: канал закрыт, ответа не будет.
func (l *link) failWaiting(why string) {
	l.waitMu.Lock()
	waiting := l.waiting
	l.waiting = map[int]chan json.RawMessage{}
	l.waitMu.Unlock()
	for _, ch := range waiting {
		ch <- json.RawMessage(`{"__error":` + strconv.Quote(why) + `}`)
	}
}

// write — кадр в провод под общим замком и со сроком. Срок обязателен:
// уснувший мак перестаёт вычитывать, буфер наполняется, и запись висит
// вечно — вместе с ней висел бы замок, а на нём весь экран «Агенты».
func (l *link) write(body []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.conn.SetWriteDeadline(time.Now().Add(linkWriteWait))
	defer l.conn.SetWriteDeadline(time.Time{})
	if _, err := fmt.Fprintf(l.out, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		return err
	}
	return l.out.Flush()
}

// linkWriteWait — сколько ждём, пока агент вычитает кадр.
const linkWriteWait = 10 * time.Second

func (l *link) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	l.waitMu.Lock()
	l.id++
	id := l.id
	ch := make(chan json.RawMessage, 1)
	l.waiting[id] = ch
	l.waitMu.Unlock()
	defer func() {
		l.waitMu.Lock()
		delete(l.waiting, id)
		l.waitMu.Unlock()
	}()

	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	werr := l.write(body)
	if werr != nil {
		l.waitMu.Lock()
		delete(l.waiting, id)
		l.waitMu.Unlock()
		return nil, werr
	}
	select {
	case <-ctx.Done():
		l.waitMu.Lock()
		delete(l.waiting, id)
		l.waitMu.Unlock()
		return nil, ctx.Err()
	case raw := <-ch:
		var e struct {
			Err string `json:"__error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Err != "" {
			return nil, fmt.Errorf("%s", e.Err)
		}
		return raw, nil
	}
}

// readFramed — сообщение с заголовком длины.
func readFramed(r *bufio.Reader) ([]byte, error) {
	length := 0
	headerBytes := 0
	seenLength := false
	for {
		var raw []byte
		for {
			if headerBytes >= linkHeaderLimit {
				return nil, fmt.Errorf("слишком большой заголовок сообщения")
			}
			b, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			headerBytes++
			raw = append(raw, b)
			if b == '\n' {
				break
			}
		}
		line := strings.TrimRight(string(raw), "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if seenLength {
				return nil, fmt.Errorf("повторный заголовок длины сообщения")
			}
			seenLength = true
			var err error
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("плохая длина сообщения")
			}
		}
	}
	if length <= 0 || length > 32<<20 {
		return nil, fmt.Errorf("плохая длина сообщения")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// HandleEnroll — вход агента по приглашению. Единственное место, куда
// агент приходит без сертификата, поэтому оно и защищено одноразовым
// кодом.
func (e *Engine) HandleEnroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string   `json:"code"`
		Pub  []byte   `json:"pub"`
		Can  []string `json:"can"`
		Role string   `json:"role"` // какую роль агент просит себе; решает не он
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"тело запроса не разобрано"}`, http.StatusBadRequest)
		return
	}
	certPEM, rootPEM, role, err := e.Enroll(req.Code, req.Pub, req.Can)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusForbidden)
		return
	}
	// роль в ответе — та, что выбрал человек при выдаче кода. Агент сверит
	// её со своей просьбой и скажет вслух, если они разошлись
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"cert": certPEM, "root": rootPEM, "role": role})
}

// CheckAgent — «жив ли ты и что умеешь»: зовёт способность агента по
// живому соединению. Заодно это единственная проверка, которая ходит
// туда же, куда пойдёт настоящая работа.
func (e *Engine) CheckAgent(ctx context.Context, name string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := e.AskAgent(cctx, name, "код", "код.agent_status", map[string]any{})
	if err != nil {
		return "", err
	}
	var out struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return string(raw), nil
	}
	first := strings.SplitN(strings.TrimSpace(out.Text), "\n", 2)[0]
	return "ответил · " + first, nil
}

// AgentSavings — сколько агент сэкономил: строк не прочитано, карточек,
// ответов. Спрашивается по каналу и держится в памяти минуту: экран
// открывается часто, а цифры так быстро не меняются.
type AgentSavings struct {
	Calls     int  `json:"calls"`
	CardLines int  `json:"card_lines"`
	Saved     int  `json:"saved_lines"`
	Ctags     bool `json:"ctags"`
	Grep      bool `json:"grep"`
	// WithKnowledge — карточек с приложением из памяти; Week — за семь
	// дней по дневным строкам агента. По ним видно, стали ли карточки
	// спрашивать после подсказки в хуке и доходит ли знание
	WithKnowledge int `json:"with_knowledge"`
	Week          struct {
		Calls         int `json:"calls"`
		Saved         int `json:"saved_lines"`
		WithKnowledge int `json:"with_knowledge"`
	} `json:"week"`
	At time.Time
	// Tokens — во что это обошлось бы моделью. Строк агент считает точно,
	// а токены прикидываются: длина строки кода в среднем известна, и
	// человеку понятнее «сэкономлено 40 тысяч токенов», чем «3200 строк».
	Tokens int
}

func (e *Engine) AgentSavings(name string) (AgentSavings, bool) {
	e.linkMu.Lock()
	got, ok := e.savings[name]
	e.linkMu.Unlock()
	if ok && time.Since(got.At) < time.Minute {
		return got, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := e.AskAgent(ctx, name, "код", "код.экономия", map[string]any{})
	if err != nil {
		return got, ok // прежние цифры честнее пустоты, если они были
	}
	var out AgentSavings
	if json.Unmarshal(raw, &out) != nil {
		return got, ok
	}
	out.At = time.Now()
	out.Tokens = out.Saved * e.settingInt("code.tokens_per_line", 12)
	// снимок в историю: агент считает нарастающим итогом, а «за неделю»
	// показывается разницей снимков
	_ = e.store.SaveSavings(store.Savings{Agent: name, Calls: out.Calls,
		CardLines: out.CardLines, Saved: out.Saved, Tokens: out.Tokens})
	e.linkMu.Lock()
	if e.savings == nil {
		e.savings = map[string]AgentSavings{}
	}
	e.savings[name] = out
	e.linkMu.Unlock()
	return out, true
}
