// Пакет max — клиент мессенджера MAX (oneme.ru) от вашего имени.
//
// Как и в Telegram, бот видит только написанное ему, поэтому Сёма
// работает клиентом: одно постоянное соединение WebSocket, JSON-кадры
// «ver / cmd / seq / opcode / payload». Вход — телефон, код из SMS,
// дальше токен доступа, который хранится шифрованным в секретах.
//
// Соединение одно на всю работу: сервер сбрасывает токен после
// нескольких десятков входов подряд, поэтому «подключился — сделал
// запрос — отключился» не годится.
package max

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// dialTries — сколько раз пробовать соединение, пока не попадём на узел
// с живым сертификатом.
const dialTries = 5

// Адрес и версия протокола. Меняются вместе с веб-клиентом MAX, поэтому
// вынесены сюда, а не разбросаны по коду.
const (
	wsURL   = "wss://ws-api.oneme.ru/websocket"
	origin  = "https://web.max.ru"
	proto   = 11
	uaValue = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
)

// Опкоды — только те, что нужны Сёме. Остальные есть в протоколе, но
// без надобности не зовутся: чем меньше поверхность, тем меньше поводов
// для блокировки аккаунта.
const (
	opPing      = 1
	opInit      = 6
	opCaptcha   = 224 // проверка VK: ссылку открывает человек в браузере
	opCodeAsk   = 17  // запрос кода на телефон
	opCodeEnter = 18  // ввод кода из SMS
	opLogin     = 19  // вход по токену
	opFileUp    = 87  // адрес для загрузки файла
	opPhotoUp   = 80  // адрес для загрузки фотографии
	opHistory   = 49
	opChats     = 53
	opSend      = 64
)

// Виды кадров: запрос, ответ, ошибка. Push приходит с cmd = 0 и своим
// опкодом — его отличают по отсутствию ожидающего seq.
const (
	cmdRequest = 0
	cmdReply   = 1
	cmdError   = 3
)

// flexID — число, которое MAX присылает то числом, то строкой: у
// сообщений id приходит строкой, у чатов — числом. Разбираем оба вида,
// иначе история молча не читается.
type flexID int64

func (f *flexID) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(string(raw), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("не число: %s", s)
	}
	*f = flexID(n)
	return nil
}

// frame — кадр протокола в обе стороны.
type frame struct {
	Ver     int             `json:"ver"`
	Cmd     int             `json:"cmd"`
	Seq     int             `json:"seq"`
	Opcode  int             `json:"opcode"`
	Payload json.RawMessage `json:"payload"`
}

// Client — соединение с MAX. Живёт долго: одно на всю работу Сёмы.
type Client struct {
	deviceID string
	token    string // токен доступа; пусто — только вход по коду

	// принимать просроченный сертификат узла: человек включает это сам,
	// когда MAX не чинит свои узлы. Подлинность сервера при этом всё
	// равно проверяется — иначе на месте MAX мог бы оказаться кто угодно
	allowExpired bool

	mu        sync.Mutex
	conn      *websocket.Conn
	lastChats []Chat // список из ответа на вход: запрос списка бывает пустым
	me        int64  // свой id: по нему видно, где в истории ваши сообщения
	seq       int
	waiting   map[int]chan frame

	// dead — закрывается, когда читатель кончился. Раньше ошибка чтения
	// выбрасывалась (`_ = c.read(...)`), и закрытое сервером соединение
	// обнаруживалось только следующим keepalive — до минуты спустя. Связь
	// рвётся редко, а вот минута молчания на ровном месте была всегда.
	dead   chan struct{}
	reason error

	// last — когда от MAX пришёл хоть какой-нибудь кадр. Это и есть
	// настоящий признак живой связи: сообщения приходят пушами, и пока они
	// идут, связь работает, что бы ни отвечал сервер на наш keepalive
	last time.Time
	// unknownCount — кадры, которых мы не ждали и не поняли. Считаем и
	// показываем первый: иначе протокол чужой службы остаётся догадкой
	unknownCount int
	firstUnknown string

	onPush func(Update)
}

// New — клиент с сохранённым удостоверением. deviceID держится
// постоянным: со сменой устройства MAX просит вход заново.
// трасса — разбор чужого протокола по кадрам. Включается переменной
// SEMA_MAX_TRACE=1 на время разбирательства и пишет только форму кадра:
// вид, номер, опкод и имена полей. Значений в журнале нет никогда — в них
// переписка, а журнал человек присылает в чат, когда что-то сломалось.
var trace = os.Getenv("SEMA_MAX_TRACE") == "1"

func New(deviceID, token string) *Client {
	return &Client{deviceID: deviceID, token: token, waiting: map[int]chan frame{}}
}

// AllowExpiredCert — принимать узлы MAX с просроченным сертификатом.
// Цепочка и имя сервера проверяются как обычно, не проверяется только
// срок: у ws-api.oneme.ru часть узлов отдаёт сертификат, истёкший
// 4 сентября 2026.
func (c *Client) AllowExpiredCert(on bool) { c.allowExpired = on }

// Token — токен доступа после входа по коду: его хранит вызывающий.
func (c *Client) Token() string { return c.token }

// DeviceID — удостоверение устройства, тоже на хранение.
func (c *Client) DeviceID() string { return c.deviceID }

// dial — соединение и INIT. Без INIT сервер не отвечает ни на что.
//
// Часть узлов MAX отдаёт просроченный сертификат (`CN=oneme.ru`, истёк
// 4 сентября 2026 — примерно каждое третье подключение), поэтому попытка
// повторяется: следующее соединение попадает на исправный узел. Проверку
// сертификата при этом не отключаем — иначе шифрование ничего не значит.
func (c *Client) dial(ctx context.Context) error {
	var conn *websocket.Conn
	var err error
	for try := 0; try < dialTries; try++ {
		opts := &websocket.DialOptions{
			HTTPHeader: map[string][]string{
				"Origin":     {origin},
				"User-Agent": {uaValue},
			},
		}
		if c.allowExpired {
			opts.HTTPClient = relaxedClient()
		}
		conn, _, err = websocket.Dial(ctx, wsURL, opts)
		if err == nil {
			break
		}
		if !badCert(err) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		if badCert(err) {
			return fmt.Errorf("у MAX часть узлов отдаёт просроченный сертификат — попробуйте ещё раз: %w", err)
		}
		return fmt.Errorf("MAX не отвечает: %w", err)
	}
	conn.SetReadLimit(8 << 20) // история и списки чатов бывают крупными
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return nil
}

// relaxedClient — тот же TLS, но без проверки срока: цепочка до корня и
// имя сервера сверяются, просроченный сертификат принимается. Так вход
// проходит через сломанный узел MAX, но подменить сервер всё равно
// нельзя — на это чужого сертификата не хватит.
func relaxedClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // проверку делаем сами, ниже
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			var certs []*x509.Certificate
			for _, der := range raw {
				c, err := x509.ParseCertificate(der)
				if err != nil {
					return err
				}
				certs = append(certs, c)
			}
			if len(certs) == 0 {
				return fmt.Errorf("узел MAX не прислал сертификат")
			}
			pool := x509.NewCertPool()
			for _, c := range certs[1:] {
				pool.AddCert(c)
			}
			leaf := certs[0]
			// момент проверки — внутри срока самого сертификата: так
			// сверяются подписи и имя, а истёкший срок не мешает
			when := leaf.NotAfter.Add(-time.Hour)
			if when.After(time.Now()) {
				when = time.Now()
			}
			_, err := leaf.Verify(x509.VerifyOptions{
				DNSName:       "ws-api.oneme.ru",
				Intermediates: pool,
				CurrentTime:   when,
			})
			return err
		},
	}}}
}

// badCert — отказ из-за сертификата узла, а не из-за сети. Такое лечится
// повтором: у MAX за одним адресом несколько узлов.
func badCert(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "certificate") || strings.Contains(s, "x509")
}

// Hello — соединиться и представиться: проба связи без запроса кода.
func (c *Client) Hello(ctx context.Context) error { return c.open(ctx) }

// hello — INIT: кто мы такие. Ответ несёт настройки сервера, они не нужны.
func (c *Client) hello(ctx context.Context) error {
	_, err := c.call(ctx, opInit, map[string]any{
		"userAgent": map[string]any{
			"deviceType":      "WEB",
			"pushDeviceType":  "WEBPUSH",
			"locale":          "ru",
			"deviceLocale":    "ru",
			"osVersion":       "macOS",
			"deviceName":      "Sema",
			"headerUserAgent": uaValue,
			"appVersion":      "26.6.17",
			"screen":          "1440x900 2.0x",
			"timezone":        time.Local.String(),
		},
		"deviceId": c.deviceID,
	})
	return err
}

// call — запрос и ответ по seq. Push-кадры разбираются читателем и сюда
// не попадают.
func (c *Client) call(ctx context.Context, opcode int, payload any) (json.RawMessage, error) {
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("нет соединения с MAX")
	}
	c.seq++
	seq := c.seq
	ch := make(chan frame, 1)
	c.waiting[seq] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiting, seq)
		c.mu.Unlock()
	}()

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(frame{Ver: proto, Cmd: cmdRequest, Seq: seq, Opcode: opcode, Payload: raw})
	if err != nil {
		return nil, err
	}
	if trace {
		log.Printf("max-трасса: → seq=%d op=%d (жду ответа)", seq, opcode)
	}
	if err := conn.Write(ctx, websocket.MessageText, out); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f := <-ch:
		if f.Cmd == cmdError {
			return nil, errorOf(f.Payload)
		}
		return f.Payload, nil
	}
}

// errorOf — ошибка сервера человеческими словами: MAX кладёт причину в
// три разных поля, и «error» из них самое сухое.
func errorOf(payload json.RawMessage) error {
	var e struct {
		Error     string `json:"error"`
		Localized string `json:"localizedMessage"`
		Message   string `json:"message"`
	}
	_ = json.Unmarshal(payload, &e)
	switch {
	case e.Localized != "":
		return fmt.Errorf("MAX: %s", e.Localized)
	case e.Error != "":
		return fmt.Errorf("MAX: %s", e.Error)
	case e.Message != "":
		return fmt.Errorf("MAX: %s", e.Message)
	}
	return fmt.Errorf("MAX отказал без объяснения")
}

// read — единственный читатель соединения: раздаёт ответы по seq, а
// push отдаёт наверх. Возвращается, когда связь оборвалась.
func (c *Client) read(ctx context.Context) error {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			c.failWaiting(err)
			return err
		}
		c.mu.Lock()
		c.last = time.Now()
		c.mu.Unlock()
		var f frame
		if json.Unmarshal(data, &f) != nil {
			continue // мусор в кадре не повод рвать связь
		}
		if trace {
			log.Printf("max-трасса: ← cmd=%d seq=%d op=%d поля{%s}",
				f.Cmd, f.Seq, f.Opcode, frameFields(f.Payload))
		}
		c.mu.Lock()
		ch, ok := c.waiting[f.Seq]
		c.mu.Unlock()
		if ok && f.Cmd != cmdRequest {
			ch <- f
			continue
		}
		// Пингует сервер, а отвечать должен клиент.
		//
		// Это и была причина, по которой MAX рвался круглые сутки. Мы
		// поняли протокол наоборот: слали ping сами (его MAX не отвечает
		// вовсе) и молчали на его собственный — а он через десять секунд
		// после неотвеченного ping закрывал соединение. В журнале это
		// выглядело как «обрыв каждые полторы минуты без причины».
		if f.Cmd == cmdRequest && f.Opcode == opPing {
			if err := c.reply(f.Seq, opPing); err != nil {
				return err // не смогли ответить — связи уже нет
			}
			continue
		}
		if f.Cmd == cmdRequest && c.onPush != nil {
			if u, ok := updateOf(f.Payload); ok {
				c.onPush(u)
				continue
			}
		}
		// Кадр, которого никто не ждал и который не оказался сообщением,
		// раньше молча выбрасывался. Именно в этой тишине и жила загадка
		// обрывов: чем MAX отвечает на keepalive, узнать было неоткуда.
		c.mu.Lock()
		c.unknownCount++
		if c.firstUnknown == "" {
			// только форма кадра, без значений: в payload бывает переписка,
			// а журнал человек присылает в чат, когда что-то сломалось
			c.firstUnknown = fmt.Sprintf("cmd=%d seq=%d op=%d поля{%s}",
				f.Cmd, f.Seq, f.Opcode, frameFields(f.Payload))
		}
		c.mu.Unlock()
	}
}

// failWaiting — обрыв связи будит всех, кто ждёт ответа: иначе запрос
// висит до таймаута контекста, а человек смотрит на пустой экран.
func (c *Client) failWaiting(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for seq, ch := range c.waiting {
		payload, _ := json.Marshal(map[string]string{"error": "связь с MAX оборвалась: " + err.Error()})
		select {
		case ch <- frame{Cmd: cmdError, Seq: seq, Payload: payload}:
		default:
		}
		delete(c.waiting, seq)
	}
}

// Close — закрыть соединение.
func (c *Client) Close() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn == nil {
		return
	}
	// Вежливое закрытие ждёт ответного кадра от собеседника — до пяти
	// секунд. Когда связь уже оборвалась (EOF, broken pipe), ждать нечего и
	// некого: эти пять секунд просто добавлялись к каждому переподключению.
	// Поэтому вежливо прощаемся только с живой связью.
	if c.alive() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	conn.CloseNow()
}

// alive — цел ли ещё читатель. Его конец означает, что собеседника нет.
func (c *Client) alive() bool {
	c.mu.Lock()
	dead := c.dead
	c.mu.Unlock()
	if dead == nil {
		return false
	}
	select {
	case <-dead:
		return false
	default:
		return true
	}
}

// RawCall — разовый запрос произвольным опкодом: нужен для проб, чтобы
// видеть ответ MAX как есть, а не после разбора.
func (c *Client) RawCall(ctx context.Context, opcode int, payload any) (string, error) {
	if err := c.open(ctx); err != nil {
		return "", err
	}
	raw, err := c.call(ctx, opcode, payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Captcha — ссылка на проверку VK. MAX отвечает «Проверка не пройдена»,
// пока её не прошли: без человека с браузером тут не обойтись.
func (c *Client) Captcha(ctx context.Context, phone string) (string, error) {
	if err := c.open(ctx); err != nil {
		return "", err
	}
	payload, err := c.call(ctx, opCaptcha, map[string]any{"source": "auth", "identifier": phone})
	if err != nil {
		return "", err
	}
	var r struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal(payload, &r); err != nil || r.Link == "" {
		return "", fmt.Errorf("MAX не дал ссылку на проверку")
	}
	return r.Link, nil
}

// AskCode — запрос кода на телефон. captcha — то, что вернула проверка
// VK; пусто — пробуем без неё, вдруг не спросят. Возвращает временный токен, с
// которым потом уходит код из SMS.
func (c *Client) AskCode(ctx context.Context, phone, captcha string) (string, error) {
	if err := c.open(ctx); err != nil {
		return "", err
	}
	req := map[string]any{"phone": phone, "type": "START_AUTH", "language": "ru"}
	if strings.TrimSpace(captcha) != "" {
		req["captchaToken"] = strings.TrimSpace(captcha)
	}
	payload, err := c.call(ctx, opCodeAsk, req)
	if err != nil {
		return "", err
	}
	var r struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(payload, &r); err != nil || r.Token == "" {
		return "", fmt.Errorf("MAX не дал токен подтверждения")
	}
	return r.Token, nil
}

// SubmitCode — код из SMS в обмен на токен доступа. Токен хранится
// шифрованным: он и есть доступ к переписке.
func (c *Client) SubmitCode(ctx context.Context, verifyToken, code string) (string, error) {
	if err := c.open(ctx); err != nil {
		return "", err
	}
	payload, err := c.call(ctx, opCodeEnter, map[string]any{
		"token": verifyToken, "verifyCode": strings.TrimSpace(code), "authTokenType": "CHECK_CODE",
	})
	if err != nil {
		return "", err
	}
	var r struct {
		TokenAttrs struct {
			Login struct {
				Token string `json:"token"`
			} `json:"LOGIN"`
		} `json:"tokenAttrs"`
	}
	if err := json.Unmarshal(payload, &r); err != nil || r.TokenAttrs.Login.Token == "" {
		return "", fmt.Errorf("MAX не дал токен доступа — код не подошёл?")
	}
	c.token = r.TokenAttrs.Login.Token
	return c.token, nil
}

// open — соединение и INIT, если их ещё нет. Вход по токену сюда не
// входит: для запроса кода он не нужен.
func (c *Client) open(ctx context.Context) error {
	c.mu.Lock()
	have := c.conn != nil
	c.mu.Unlock()
	if have {
		return nil
	}
	if err := c.dial(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.dead = make(chan struct{})
	dead := c.dead
	c.mu.Unlock()
	go func() {
		// читатель живёт, пока живо соединение; его конец — и есть конец
		// связи, и узнать об этом надо сразу, а не следующим keepalive
		err := c.read(context.Background())
		c.mu.Lock()
		c.reason = err
		c.mu.Unlock()
		close(dead)
	}()
	return c.hello(ctx)
}

// Login — вход по токену на уже открытом соединении. Возвращает номер
// телефона из профиля: при входе по токену из браузера человек его не
// вводил, а подписать аккаунт чем-то надо.
func (c *Client) Login(ctx context.Context) (string, error) {
	if strings.TrimSpace(c.token) == "" {
		return "", fmt.Errorf("нет токена доступа: пройдите вход по телефону")
	}
	if err := c.open(ctx); err != nil {
		return "", err
	}
	payload, err := c.call(ctx, opLogin, map[string]any{
		"interactive": true, "token": c.token,
		"chatsCount": 100, "chatsSync": 0, "contactsSync": 0,
		"presenceSync": 0, "draftsSync": 0,
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Profile struct {
			Contact struct {
				ID     flexID `json:"id"`
				Phones []struct {
					Number string `json:"number"`
				} `json:"phones"`
			} `json:"contact"`
		} `json:"profile"`
		Chats []chatRow `json:"chats"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return "", nil
	}
	// вход отвечает и списком чатов — запоминаем: отдельный запрос за ними
	// нужен не всегда, а лишний вызов у MAX не бесплатный
	c.mu.Lock()
	c.lastChats = chatsOf(r.Chats)
	c.me = int64(r.Profile.Contact.ID)
	c.mu.Unlock()
	if len(r.Profile.Contact.Phones) > 0 {
		return r.Profile.Contact.Phones[0].Number, nil
	}
	return "", nil
}

// chatRow — чат так, как его описывает MAX и в ответе на вход, и в
// ответе на запрос списка.
type chatRow struct {
	ID    flexID `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Last  struct {
		Text string `json:"text"`
	} `json:"lastMessage"`
}

// chatsOf — из ответа MAX в то, что нужно Сёме.
func chatsOf(rows []chatRow) []Chat {
	out := make([]Chat, 0, len(rows))
	for _, ch := range rows {
		title := strings.TrimSpace(ch.Title)
		if title == "" {
			title = fmt.Sprintf("чат %d", ch.ID)
		}
		out = append(out, Chat{ID: int64(ch.ID), Title: title, Kind: kindOf(ch.Type), Last: ch.Last.Text})
	}
	return out
}

// Chat — диалог или группа в списке.
type Chat struct {
	ID    int64
	Title string
	Kind  string // dialog | chat | channel
	Last  string // последнее сообщение: по нему человек узнаёт чат
}

// Chats — список чатов: из чего выбирать, что собирать.
func (c *Client) Chats(ctx context.Context, limit int) ([]Chat, error) {
	if limit <= 0 {
		limit = 100
	}
	// marker — «изменённые раньше этого времени»: с нулём MAX честно
	// отвечал пустотой, потому что раньше начала эпохи чатов нет
	payload, err := c.call(ctx, opChats, map[string]any{
		"count": limit, "marker": time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Chats []chatRow `json:"chats"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	out := chatsOf(r.Chats)
	if len(out) == 0 {
		// список приходит и вместе со входом; если запрос ничего не дал,
		// берём оттуда, а не показываем человеку пустоту
		c.mu.Lock()
		out = c.lastChats
		c.mu.Unlock()
	}
	return out, nil
}

// kindOf — вид чата словами Сёмы: класс чувствительности выбирается по
// нему, как в Telegram.
func kindOf(t string) string {
	switch strings.ToUpper(t) {
	case "DIALOG":
		return "dialog"
	case "CHANNEL":
		return "channel"
	default:
		return "chat"
	}
}

// Attach — вложение сообщения: имя, вид и адрес, по которому его забрать.
type Attach struct {
	Kind string // PHOTO | VIDEO | FILE | AUDIO | UNSUPPORTED …
	Name string
	URL  string
	Size int64
	// Keys — имена полей кадра вложения, без значений. Голосовое в живом
	// протоколе MAX ни разу не разбирали по трассе, и когда оно придёт без
	// адреса, по этим именам видно, где его искать. Значений здесь нет
	// никогда: в них переписка, а журнал человек присылает в чат.
	Keys []string
}

// Voice — голосовое сообщение. В протоколе веб-клиента это вложение AUDIO;
// UNSUPPORTED с адресом — то, чем прежний разбор называл голосовое, когда
// вид не узнавался: его забираем тоже, а нераспознаваемое отвергнет
// проверка типа при скачивании.
func (a Attach) Voice() bool {
	switch strings.ToUpper(a.Kind) {
	case "AUDIO", "VOICE":
		return true
	case "UNSUPPORTED":
		return a.URL != ""
	}
	return false
}

// Human — как назвать вложение в переписке, когда имени нет.
func (a Attach) Human() string {
	if strings.TrimSpace(a.Name) != "" {
		return a.Name
	}
	switch a.Kind {
	case "PHOTO":
		return "фотография"
	case "VIDEO":
		return "видео"
	case "UNSUPPORTED", "AUDIO", "VOICE":
		return "голосовое сообщение"
	}
	return "вложение"
}

// Update — новое сообщение, пришедшее по живому соединению.
type Update struct {
	ChatID int64
	MsgID  int64
	Text   string
	Sender int64
	Out    bool     // это мы написали, а не нам
	Attach int      // сколько вложений
	Files  []Attach // сами вложения, если их дали
}

// attachesOf — вложения из кадра. Поля у видов разные (fileUrl у файла,
// baseUrl у фотографии), поэтому смотрим все известные имена: пустой
// адрес — вложение просто не заберётся, но останется видно в переписке.
func attachesOf(raw []map[string]any) []Attach {
	var out []Attach
	for _, m := range raw {
		if m == nil {
			continue
		}
		if _, keyboard := m["keyboard"]; keyboard {
			continue // кнопка канала — не вложение
		}
		a := Attach{Kind: str(m["_type"])}
		for key := range m {
			a.Keys = append(a.Keys, key)
		}
		sort.Strings(a.Keys)
		for _, key := range []string{"fileName", "name", "title"} {
			if v := str(m[key]); v != "" {
				a.Name = v
				break
			}
		}
		for _, key := range []string{"fileUrl", "url", "baseUrl", "previewUrl"} {
			if v := str(m[key]); v != "" {
				a.URL = v
				break
			}
		}
		for _, key := range []string{"fileSize", "size"} {
			if v, ok := m[key].(float64); ok && v > 0 {
				a.Size = int64(v)
				break
			}
		}
		out = append(out, a)
	}
	return out
}

// str — строковое поле кадра, если оно строка.
func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// updateOf — из push-кадра в то, что нужно Сёме. Опкод push-сообщения о
// новом сообщении в описании протокола не закреплён, поэтому смотрим на
// содержимое: есть чат и сообщение — значит, сообщение.
func updateOf(payload json.RawMessage) (Update, bool) {
	var r struct {
		ChatID  flexID `json:"chatId"`
		Message struct {
			ID      flexID           `json:"id"`
			Text    string           `json:"text"`
			Sender  flexID           `json:"sender"`
			Attach  []map[string]any `json:"attaches"`
			Deleted string           `json:"deleted"`
		} `json:"message"`
	}
	if json.Unmarshal(payload, &r) != nil {
		return Update{}, false
	}
	if r.ChatID == 0 || r.Message.ID == 0 || r.Message.Deleted != "" {
		return Update{}, false
	}
	files := attachesOf(r.Message.Attach)
	return Update{
		ChatID: int64(r.ChatID), MsgID: int64(r.Message.ID), Text: r.Message.Text,
		Sender: int64(r.Message.Sender), Attach: len(files), Files: files,
	}, true
}

// Listen — держать соединение и отдавать новые сообщения. Возвращается,
// когда связь оборвалась или контекст отменён: переподключение — забота
// вызывающего, как и в Telegram.
func (c *Client) Listen(ctx context.Context, onUpdate func(Update)) error {
	c.onPush = onUpdate
	if _, err := c.Login(ctx); err != nil {
		return err
	}
	// Связь держится не нашим keepalive, а ответом на чужой.
	//
	// Пингует сервер: раз в минуту присылает `cmd=0 op=1` с пустым телом и
	// ждёт ответа тем же номером. Не дождавшись — секунд через десять
	// закрывает соединение. Прежний код понимал протокол наоборот: слал
	// ping сам (MAX на него не отвечает вовсе), на серверный молчал и
	// поэтому жил ровно минуту. Ответ уходит из читателя, там же, где
	// разбирается кадр.
	//
	// Своё движение навстречу тоже нужно. Замер на живом: сервер пингует
	// раз в минуту, и одного нашего ответа на него связи не хватает — она
	// живёт ту же минуту. Стоило слать свой кадр в промежутке, и связь
	// стала жить вчетверо дольше. Похоже на срок простоя, который считается
	// по движению от клиента: ответ приходится ровно на границу, а кадр в
	// середине минуты уводит от неё с запасом.
	//
	// «interactive: true» — отметка «человек у экрана»; MAX отвечает на неё
	// как положено (`cmd=1` тем же номером). Ответа не ждём: ждать значит
	// снова выдумывать себе обрыв.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	c.mu.Lock()
	dead := c.dead
	c.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			c.Close()
			return ctx.Err()
		case <-dead:
			// сервер закрыл соединение — узнаём в тот же миг
			c.mu.Lock()
			reason := c.reason
			c.mu.Unlock()
			c.Close()
			if reason == nil {
				reason = fmt.Errorf("сервер закрыл соединение")
			}
			return fmt.Errorf("%w (кадров нет %s%s)", reason,
				c.silentFor().Round(time.Second), c.unknownFrames())
		case <-ping.C:
			ctxPing, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.sendFrame(ctxPing, opPing, map[string]any{"interactive": true})
			cancel()
			if err != nil {
				// не записалось — сокета уже нет; читатель скажет то же
				// самое, но незачем ждать его, раз мы это уже знаем
				c.Close()
				return fmt.Errorf("%w (кадров нет %s%s)", err,
					c.silentFor().Round(time.Second), c.unknownFrames())
			}
		}
	}
}

// reply — наш ответ на запрос сервера: тот же номер, вид «ответ».
//
// Срок короткий нарочно: сервер ждёт ответа считаные секунды, и запись,
// которая не уложилась, уже не нужна — связь всё равно потеряна.
func (c *Client) reply(seq, opcode int) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("нет соединения с MAX")
	}
	out, err := json.Marshal(frame{Ver: proto, Cmd: cmdReply, Seq: seq,
		Opcode: opcode, Payload: json.RawMessage("{}")})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if trace {
		log.Printf("max-трасса: → seq=%d op=%d (ответ на запрос сервера)", seq, opcode)
	}
	return conn.Write(ctx, websocket.MessageText, out)
}

// sendFrame — кадр без ожидания ответа.
//
// Ответа MAX не обязан давать вовремя, а ждать его значит выдумывать себе
// обрыв: ровно так и жила прежняя беда. Нам достаточно самой отправки —
// она и есть то движение, ради которого кадр посылается.
func (c *Client) sendFrame(ctx context.Context, opcode int, payload any) error {
	c.mu.Lock()
	conn := c.conn
	c.seq++
	seq := c.seq
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("нет соединения с MAX")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	out, err := json.Marshal(frame{Ver: proto, Cmd: cmdRequest, Seq: seq, Opcode: opcode, Payload: raw})
	if err != nil {
		return err
	}
	if trace {
		log.Printf("max-трасса: → seq=%d op=%d (без ожидания ответа)", seq, opcode)
	}
	return conn.Write(ctx, websocket.MessageText, out)
}

// silentFor — сколько от MAX не приходило ни одного кадра.
func (c *Client) silentFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last.IsZero() {
		return 0 // ещё ничего не ждали — не повод считать связь мёртвой
	}
	return time.Since(c.last)
}

// unknownFrames — чем MAX отвечал такое, чего мы не поняли. Без этого правка
// протокола осталась бы угадыванием.
func (c *Client) unknownFrames() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unknownCount == 0 {
		return ""
	}
	return fmt.Sprintf(" · неопознанных кадров %d, первый: %s", c.unknownCount, c.firstUnknown)
}

// frameFields — какие поля есть в кадре, без их значений. Этого хватает,
// чтобы узнать кадр («presence, userId» — это отметка присутствия), и не
// хватает, чтобы выдать содержимое переписки.
func frameFields(payload json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(payload, &m) != nil {
		return "не объект"
	}
	fields := make([]string, 0, len(m))
	for k := range m {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	return strings.Join(fields, ",")
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Send — сообщение от вашего имени.
func (c *Client) Send(ctx context.Context, chatID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("пустое сообщение")
	}
	_, err := c.call(ctx, opSend, map[string]any{
		"chatId": chatID,
		"message": map[string]any{
			"text": text, "cid": time.Now().UnixMilli(),
			"elements": []any{}, "attaches": []any{},
		},
		"notify": true,
	})
	return err
}

// SendFile — файл собеседнику: сначала MAX даёт адрес хранилища, файл
// уходит обычным POST, и только потом сообщение с вложением. Подтверждение
// загрузки приходит push-кадром (opcode 136), но ждать его не обязательно:
// сервер принимает вложение по токену.
func (c *Client) SendFile(ctx context.Context, chatID int64, name string, raw []byte, caption string) error {
	if len(raw) == 0 {
		return fmt.Errorf("пустой файл")
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	payload, err := c.call(ctx, opFileUp, map[string]any{
		"name": name, "size": len(raw), "ext": ext, "count": 1,
	})
	if err != nil {
		return err
	}
	var r struct {
		Info []struct {
			URL    string `json:"url"`
			FileID string `json:"fileId"`
			Token  string `json:"token"`
			Name   string `json:"name"`
		} `json:"info"`
	}
	if err := json.Unmarshal(payload, &r); err != nil || len(r.Info) == 0 || r.Info[0].URL == "" {
		return fmt.Errorf("MAX не дал адрес для загрузки файла")
	}
	info := r.Info[0]
	if err := postFile(ctx, info.URL, name, raw); err != nil {
		return err
	}
	attach := map[string]any{
		"_type": "FILE", "fileId": info.FileID, "token": info.Token,
		"name": name, "size": len(raw),
	}
	_, err = c.call(ctx, opSend, map[string]any{
		"chatId": chatID,
		"message": map[string]any{
			"text": caption, "cid": time.Now().UnixMilli(),
			"elements": []any{}, "attaches": []any{attach},
		},
		"notify": true,
	})
	return err
}

// SendPhoto — картинка фотографией, а не файлом: так её видно прямо в
// ленте, как пост с обложкой. Загрузка фото устроена иначе, чем файла:
// адрес даёт свой опкод, а хранилище в ответ на POST возвращает токен
// фотографии, который и кладётся во вложение PHOTO.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, name string, raw []byte, caption string) error {
	if len(raw) == 0 {
		return fmt.Errorf("пустая картинка")
	}
	payload, err := c.call(ctx, opPhotoUp, map[string]any{"count": 1})
	if err != nil {
		return err
	}
	var r struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(payload, &r); err != nil || r.URL == "" {
		return fmt.Errorf("MAX не дал адрес для загрузки фото (ответ: %s)", replyShape(payload))
	}
	resp, err := postFileBody(ctx, r.URL, name, raw)
	if err != nil {
		return err
	}
	token := photoTokenOf(resp)
	if token == "" {
		return fmt.Errorf("хранилище MAX не вернуло токен фото (ответ: %s)", replyShape(resp))
	}
	_, err = c.call(ctx, opSend, map[string]any{
		"chatId": chatID,
		"message": map[string]any{
			"text": caption, "cid": time.Now().UnixMilli(),
			"elements": []any{}, "attaches": []any{map[string]any{"_type": "PHOTO", "photoToken": token}},
		},
		"notify": true,
	})
	return err
}

// photoTokenOf — токен из ответа хранилища: {"photos": {"<id>": {"token": "…"}}}.
func photoTokenOf(raw []byte) string {
	var r struct {
		Photos map[string]struct {
			Token string `json:"token"`
		} `json:"photos"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	for _, p := range r.Photos {
		if p.Token != "" {
			return p.Token
		}
	}
	return ""
}

// replyShape — только имена полей верхнего уровня: значения в журнал не
// идут (правило разборщика кадров), а форма нужна, чтобы понять протокол.
func replyShape(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return fmt.Sprintf("не JSON, %d байт", len(raw))
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "поля " + strings.Join(keys, ", ")
}

// postFile — сама загрузка: multipart на адрес, который дал MAX.
func postFile(ctx context.Context, url, name string, raw []byte) error {
	_, err := postFileBody(ctx, url, name, raw)
	return err
}

// postFileBody — то же, но с ответом хранилища: у фотографии в нём токен.
func postFileBody(ctx context.Context, url, name string, raw []byte) ([]byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Origin", origin)
	req.Header.Set("User-Agent", uaValue)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("хранилище MAX ответило %s", resp.Status)
	}
	return answer, nil
}

// History — прошлая переписка, от старых к новым: чат в Сёме должен
// открываться с разговором, а не с пустотой.
func (c *Client) History(ctx context.Context, chatID int64, limit int) ([]Update, error) {
	if limit <= 0 {
		limit = 30
	}
	// поля как у веб-клиента: без getMessages MAX отвечает без сообщений,
	// а без backwardTime отсчёт идёт не от «сейчас»
	payload, err := c.call(ctx, opHistory, map[string]any{
		"chatId": chatID, "from": time.Now().UnixMilli(),
		"forward": 0, "backward": limit,
		"forwardTime": 0, "backwardTime": 0,
		"getChat": false, "getMessages": true, "interactive": false,
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Messages []struct {
			ID     flexID           `json:"id"`
			Text   string           `json:"text"`
			Sender flexID           `json:"sender"`
			Attach []map[string]any `json:"attaches"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	out := make([]Update, 0, len(r.Messages))
	for _, m := range r.Messages {
		files := attachesOf(m.Attach)
		out = append(out, Update{ChatID: chatID, MsgID: int64(m.ID), Text: m.Text,
			Sender: int64(m.Sender), Out: c.me != 0 && int64(m.Sender) == c.me,
			Attach: len(files), Files: files})
	}
	return out, nil
}
