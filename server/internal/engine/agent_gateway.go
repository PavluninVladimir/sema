package engine

// Шлюз: соединение открывает Сёма, а не он.
//
// Правило «звонит агент» держалось не на том, что агент — агент, а на том,
// что ноутбук спит, меняет сеть и стоит за домашним роутером: звонит тот,
// кого не найти по адресу. У шлюза адрес постоянный, прячется Сёма —
// значит звонит он, и дома не открывается ни один порт.
//
// Переворачивается только направление TCP. Роли шифрованного разговора те
// же: Сёма предъявляет серверный сертификат, шлюз — выданный его
// центром. TLS безразлично, кто открыл сокет, поэтому выдача сертификатов,
// вход по одноразовому коду и разрешение способностей остаются как были.
//
// Управление и потоки — разные соединения, но **один порт**: и провод, и
// каждый браузерный поток приходят на тот же адрес шлюза, а кто зачем
// пришёл, Сёма говорит первым кадром. Порт один нарочно: дома Сёму
// изолируют на роутере, и наружу разрешается ровно одно направление —
// диапазон портов пришлось бы открывать целиком.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	gatewayAddrKey    = "agents.gateway"          // публичный адрес шлюза: куда звонит Сёма
	gatewayUpKey      = "agents.gateway_upstream" // куда шлюз вносит запросы внутри Сёмы
	gatewayStreamsKey = "agents.gateway_streams"  // сколько потоков держим разом
)

// Первый кадр соединения, которое открыла Сёма: зачем оно. Без него
// шлюзу пришлось бы гадать по молчанию, а гадать на одном порту
// нельзя — управление и поток выглядят одинаково.
const (
	ConnControl = "управление"
	ConnStream  = "поток"
)

// connHello — что Сёма говорит первым кадром.
type connHello struct {
	Kind   string `json:"вид"`
	Stream string `json:"поток,omitempty"`
}

// gatewayStats — что показать человеку про свой конец провода. Счётчики
// шлюза (сколько отсеяно) живут у него и спрашиваются по каналу:
// отсев делает он, и считать за него значило бы выдумывать.
type gatewayStats struct {
	Streams int       // открыто прямо сейчас
	Total   int       // за жизнь процесса
	Denials int       // потоков, которые не открылись
	Last    time.Time // когда последний раз открывали поток
	ErrText string    // почему не открылся последний
}

// GatewayAddr — публичный адрес шлюза: тот единственный, куда Сёме
// разрешено звонить наружу. Пусто — шлюза нет, и всё работает как
// прежде: это не обязательное звено.
func (e *Engine) GatewayAddr() string {
	return strings.TrimSpace(e.store.SettingText(gatewayAddrDef))
}

// gatewayName — под каким именем писать в журнал попытки дозвониться.
// Шлюза может ещё не быть вовсе (адрес вписали раньше агента), и тогда
// запись всё равно нужна — под общим именем, а не потерянной.
func (e *Engine) gatewayName() string {
	if a, ok := e.GatewayAgent(); ok {
		return a.Name
	}
	return "шлюз"
}

// GatewayUpstream — куда шлюз вносит запросы внутри Сёмы. По
// умолчанию — его же порт по петле: наружу этот адрес не ходит никогда.
func (e *Engine) GatewayUpstream() string {
	if v := strings.TrimSpace(e.store.SettingText(gatewayUpDef)); v != "" {
		return v
	}
	host, port, err := net.SplitHostPort(e.conf.Listen)
	if err != nil || port == "" {
		return "127.0.0.1:8765"
	}
	// «0.0.0.0» и «::» значат «на всех адресах», и подключаться по ним
	// нельзя: внутрь себя ходим по петле
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// GatewayMaxStreams — потолок одновременных потоков. Браузер открывает их
// пачкой, а каждый — своё соединение до шлюза: без потолка страница с
// десятком картинок выедала бы дескрипторы.
func (e *Engine) GatewayMaxStreams() int { return e.settingInt(gatewayStreamsKey, 32) }

// GatewayStats — снимок счётчиков для экрана.
func (e *Engine) GatewayStats() gatewayStats {
	e.gateMu.Lock()
	defer e.gateMu.Unlock()
	return e.gate
}

// DialGateway держит провод к шлюзу: Сёма звонит сам и
// переподключается, пока адрес задан. Адреса нет — ждёт, вдруг человек его
// впишет; появился другой — перезванивает по новому.
func (e *Engine) DialGateway(ctx context.Context) {
	wait := time.Second
	for ctx.Err() == nil {
		addr := e.GatewayAddr()
		if addr == "" {
			if !e.waitGatewayChange(ctx, addr) {
				return
			}
			wait = time.Second
			continue
		}
		// копия после -restore шлюзу не звонит, пока человек не подтвердил
		// привязку, как не забирает почту: с одним удостоверением оригинал
		// и копия выбивали бы друг друга из шлюза каждую секунду — новый
		// звонок закрывает прежний (agent-gateway 8.2)
		if e.NeedsBinding() {
			if !e.waitBound(ctx) {
				return
			}
			wait = time.Second
			continue
		}
		started := time.Now()
		if err := e.dialGatewayOnce(ctx, addr); err != nil && ctx.Err() == nil {
			log.Printf("шлюз: %v (попробую через %s)", err, wait.Round(time.Second))
		}
		// Пауза растёт, пока шлюза нет, но соединение, которое
		// пожило, сбрасывает её. Любая растущая пауза должна где-то
		// обнуляться — иначе она измеряет не нынешнюю беду, а всю историю:
		// те же грабли уже были у жителя машины.
		if time.Since(started) > time.Minute {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < time.Minute {
			wait *= 2
		}
	}
}

// waitGatewayChange ждёт, пока адрес шлюза станет другим. false —
// сервер останавливается.
// gatewayBindingPoll — как часто проверять, подтверждена ли привязка.
var gatewayBindingPoll = 5 * time.Second

// waitBound — ждать подтверждения привязки; false — Сёма останавливается.
func (e *Engine) waitBound(ctx context.Context) bool {
	log.Printf("шлюз: Сёма поднят из копии — звоню шлюзу после подтверждения привязки на «Здоровье»")
	t := time.NewTicker(gatewayBindingPoll)
	defer t.Stop()
	for e.NeedsBinding() {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
	return true
}

func (e *Engine) waitGatewayChange(ctx context.Context, was string) bool {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if e.GatewayAddr() != was {
				return true
			}
		}
	}
}

// gatewayTLS — та же конфигурация, с которой Сёма принимает агентов: его
// серверный сертификат и обязательная проверка сертификата на том конце.
// Тот, кто открыл сокет, на это не влияет.
func (e *Engine) gatewayTLS() (*tls.Config, error) {
	root, err := e.EnsureRoot()
	if err != nil {
		return nil, err
	}
	srv, err := e.EnsureServerCert()
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair([]byte(srv.CertPEM), []byte(srv.KeyPEM))
	if err != nil {
		return nil, fmt.Errorf("сертификат Сёмы не годится: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(root.CertPEM)) {
		return nil, fmt.Errorf("корень не читается")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert, // пропуск — сертификат, а не пароль
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// callGateway — открыть сокет наружу и стать на нём сервером TLS.
// Возвращает готовое соединение и того, кто на другом конце.
func (e *Engine) callGateway(ctx context.Context, addr string, kind connHello) (*tls.Conn, *store.Agent, error) {
	conf, err := e.gatewayTLS()
	if err != nil {
		return nil, nil, err
	}
	d := &net.Dialer{Timeout: 20 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("до шлюза %s не дозвониться: %w", addr, err)
	}
	tc := tls.Server(raw, conf)
	hctx, cancel := context.WithTimeout(ctx, linkHandshakeWait)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		_ = tc.Close()
		// Подменили адрес — сюда и придём: сертификата от нашего центра на
		// том конце нет, и разговора не будет. Поэтому адрес шлюза не
		// секрет. В журнал это идёт обязательно: молча упираться в чужую
		// машину и перезванивать туда же — худший способ узнать о подмене.
		e.store.LogAgent(store.AgentUse{Agent: e.gatewayName(), What: "звонок Сёмы", OK: false,
			Detail: "рукопожатие с " + addr + " не сошлось: " + err.Error()})
		return nil, nil, fmt.Errorf("рукопожатие с %s не сошлось: %w", addr, err)
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		_ = tc.Close()
		return nil, nil, fmt.Errorf("на том конце нет сертификата")
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs[0].Raw}))
	a, err := e.checkCaller(certPEM)
	if err != nil {
		_ = tc.Close()
		return nil, nil, err
	}
	if AgentRole(a) != RoleGateway {
		_ = tc.Close()
		e.store.LogAgent(store.AgentUse{Agent: a.Name, What: "звонок Сёмы", OK: false,
			Detail: "по адресу шлюза отвечает агент роли «" + AgentRole(a) + "»"})
		return nil, nil, fmt.Errorf("по адресу шлюза отвечает «%s» с ролью «%s», а не шлюз",
			a.Name, AgentRole(a))
	}
	// первым кадром говорим, зачем это соединение: порт один на всё
	if err := writeFrame(tc, kind); err != nil {
		_ = tc.Close()
		return nil, nil, fmt.Errorf("приветствие не ушло: %w", err)
	}
	return tc, a, nil
}

// dialGatewayOnce — один управляющий провод от начала до обрыва.
func (e *Engine) dialGatewayOnce(ctx context.Context, addr string) error {
	tc, a, err := e.callGateway(ctx, addr, connHello{Kind: ConnControl})
	if err != nil {
		return err
	}
	log.Printf("шлюз: «%s» на связи, звонили мы (%s)", a.Name, addr)
	// правила доступа проверяем на каждом соединении: человек мог включить
	// вход без пароля из своей сети уже после того, как шлюз встал
	e.checkRulesGateway()
	// дальше — тот же приём, что у звонящего агента: имя, способности,
	// кадры JSON-RPC в обе стороны. Рукопожатие уже сделано, и повторный
	// HandshakeContext внутри ничего не делает
	e.handleAgentReady(ctx, tc, func() {})
	return fmt.Errorf("провод к шлюзу «%s» закрылся", a.Name)
}

// gatewayAsk — что шлюз вправе попросить у Сёмы. Список закрытый:
// открыть поток и ничего больше.
func (e *Engine) gatewayAsk(ctx context.Context, agent, what string, params json.RawMessage) (any, error) {
	if what != "поток" {
		return nil, fmt.Errorf("Сёма не отвечает на «%s.%s»: шлюзу доступен только поток", CanGateway, what)
	}
	var p struct {
		Stream string `json:"поток"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("аргументы не разобраны: %w", err)
		}
	}
	if strings.TrimSpace(p.Stream) == "" {
		return nil, fmt.Errorf("поток без номера: шлюз обязан назвать, какой запрос ждёт соединения")
	}
	addr := e.GatewayAddr()
	if addr == "" {
		return nil, fmt.Errorf("адрес шлюза у Сёмы не задан — звонить некуда")
	}
	if !e.takeStream() {
		return nil, fmt.Errorf("потоков уже %d — больше Сёма разом не держит", e.GatewayMaxStreams())
	}
	// Поток открываем в своей горутине и отвечаем сразу: шлюз ждёт
	// ответа по управляющему проводу, а соединение придёт к нему отдельно.
	// Ответить после — значит держать читателя провода, пока идёт байтовая
	// перекачка, то есть заткнуть им же и пинги.
	go func() {
		defer e.freeStream()
		if err := e.openGatewayStream(context.WithoutCancel(ctx), addr, agent, p.Stream); err != nil {
			log.Printf("шлюз: поток %s не открылся: %v", p.Stream, err)
			e.gateMu.Lock()
			e.gate.Denials++
			e.gate.ErrText = err.Error()
			e.gateMu.Unlock()
			e.store.LogAgent(store.AgentUse{Agent: agent, What: "поток", OK: false, Detail: err.Error()})
		}
	}()
	return map[string]any{"ок": true}, nil
}

func (e *Engine) takeStream() bool {
	e.gateMu.Lock()
	defer e.gateMu.Unlock()
	if e.gate.Streams >= e.GatewayMaxStreams() {
		return false
	}
	e.gate.Streams++
	e.gate.Total++
	e.gate.Last = time.Now()
	return true
}

func (e *Engine) freeStream() {
	e.gateMu.Lock()
	if e.gate.Streams > 0 {
		e.gate.Streams--
	}
	e.gateMu.Unlock()
}

// openGatewayStream — соединение под один браузерный поток: наружу к
// шлюзу, внутрь к своему же HTTP, и байты между ними напрямую.
//
// Своим соединением на поток, а не общим проводом: провод пишет кадры в
// один поток под замком, и одно скачивание вложения заткнуло бы и пинги, и
// все остальные запросы. А живое обновление и отдача файла в «запрос —
// ответ» не укладываются вовсе.
func (e *Engine) openGatewayStream(ctx context.Context, addr, agent, token string) error {
	outside, _, err := e.callGateway(ctx, addr, connHello{Kind: ConnStream, Stream: token})
	if err != nil {
		return err
	}
	defer outside.Close()
	inside, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", e.GatewayUpstream())
	if err != nil {
		return fmt.Errorf("свой же порт %s не отвечает: %w", e.GatewayUpstream(), err)
	}
	defer inside.Close()
	pipe(outside, inside)
	return nil
}

// pipe — байты в обе стороны, пока одна из сторон не закончила. Ничего
// не накапливаем целиком: сотня мегабайт вложения не должна оказаться в
// памяти ни здесь, ни у шлюза.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	oneSide := func(from, v net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(v, from)
		// конец в одну сторону не всегда конец разговора (клиент дослал
		// запрос и ждёт ответ), поэтому закрываем только запись
		if cw, ok := v.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
			return
		}
		_ = v.Close()
	}
	go oneSide(a, b)
	go oneSide(b, a)
	wg.Wait()
}

// writeFrame — сообщение с заголовком длины, как во всём этом проводе.
func writeFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

// NewStreamNumber — номер, по которому шлюз узнаёт своё соединение.
// Угадать его нечего: попасть в приём потоков можно только пройдя взаимную
// проверку сертификатов. Случайность здесь — от путаницы, не от врага.
func NewStreamNumber() string {
	raw := make([]byte, 9)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

// GatewayWarning — опасное сочетание: шлюз есть, а правила доступа
// остались домашними. Пусто — сочетания нет.
//
// Молча терпеть это нельзя: запросы из интернета приходят к Сёме по петле, и
// отличает их от домашних ровно один заголовок — тот, которым шлюз
// подписывает настоящего обратившегося.
func (e *Engine) GatewayWarning() string {
	if e.GatewayAddr() == "" {
		return ""
	}
	var trouble []string
	if e.store.SettingFlag(store.AuthLocalOpen) {
		// Сказать «в дом войдёт кто угодно» было бы неправдой: запрос с
		// заголовком настоящего адреса своим не считается, и вход ему нужен
		// (TestЗапросЧерезШлюзаНеСвой). Но вся эта защита держится на
		// одном заголовке, и говорить надо именно это.
		trouble = append(trouble, "вход без пароля из своей сети всё ещё включён («Вход и доступ» → «С этой машины без входа»): "+
			"запрос через шлюза своим не считается — его выдаёт заголовок с настоящим адресом, — "+
			"но вся защита держится на этом одном заголовке. Шлюз без него или порт Сёмы, "+
			"случайно открытый наружу, пустят в дом без входа")
	}
	if e.store.Setting("auth.trust_proxy", "false") != "true" {
		trouble = append(trouble, "доверие заголовкам прокси выключено («Вход и доступ» → «За своим прокси»): "+
			"в журнале доступа будет один и тот же адрес у всех, а шлюз не сможет назвать настоящего обратившегося")
	}
	return strings.Join(trouble, " · ")
}

// checkRulesGateway — сказать вслух про опасное сочетание и
// закрыть запись, когда человек его исправил.
func (e *Engine) checkRulesGateway() {
	trouble := e.GatewayWarning()
	const key = "gateway:доступ"
	if trouble == "" {
		e.CloseNotification(key, store.NotifyDone, "ядро")
		return
	}
	log.Printf("ВНИМАНИЕ шлюз: %s", trouble)
	_, _ = e.Notify(&store.Notification{
		Key: key, Source: "core", Level: "warn",
		Title:   "Шлюз есть, а правила доступа прежние",
		Summary: trouble,
		Actions: []store.NotifyAction{
			{Label: "Открыть «Вход и доступ»", Kind: "goto", Screen: "settings:access", Style: "primary"},
			{Label: "Понял", Kind: "dismiss"},
		},
	})
}

// GatewayLook — что шлюз рассказывает о себе: домен, край, сертификат,
// сколько отсеял. Спрашиваем по каналу, а не выдумываем: отсев делает он.
type GatewayLook struct {
	Domain   string `json:"домен"`
	Edge     string `json:"край"`       // кто держит публичный сертификат
	Cert     string `json:"сертификат"` // срок или «за nginx»
	Level    string `json:"уровень"`    // ok | warn | alert — про сертификат
	Browsers string `json:"браузерам"`  // адрес, где он принимает браузеры
	Sema     string `json:"сёме"`       // адрес, куда звонит Сёма
	Skipped  int    `json:"пропущено"`
	Excluded int    `json:"отсеяно"`
	Closed   int    `json:"закрыто"` // адресов под временным баном
	Streams  int    `json:"потоков"`
	// НеВнесено — запросы, которые край пропустил, а внести не смог. Число
	// отдельное от отсеянного нарочно: сложить их значило бы показать «отсеяно
	// 500» в тот день, когда край не отклонил ничего, а чинить надо дома.
	NotRecorded int `json:"не внесено"`
}

// GatewayAsk — спросить шлюза о себе. Ошибка значима: «шлюз не
// отвечает» и «шлюза нет» — разные вещи, и человеку важно, какая.
func (e *Engine) GatewayAsk(ctx context.Context) (GatewayLook, error) {
	a, ok := e.GatewayAgent()
	if !ok {
		return GatewayLook{}, fmt.Errorf("шлюз не заведён")
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := e.AskAgent(cctx, a.Name, CanGateway, CanGateway+".состояние", map[string]any{})
	if err != nil {
		return GatewayLook{}, err
	}
	var out GatewayLook
	if err := json.Unmarshal(raw, &out); err != nil {
		return GatewayLook{}, fmt.Errorf("состояние шлюза не разобрано: %w", err)
	}
	return out, nil
}
