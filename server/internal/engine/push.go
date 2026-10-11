package engine

// Пуши в браузер — ещё один канал доставки уведомлений, на общих правилах:
// порог уровня, источники, тихие часы. Своё у него: ключи VAPID, состав
// пуша и устройства — у каждого своё имя, а при желании и свои порог с
// тихими часами (store.PushSubscription).
//
// Состав — правило из спеки, и оно жёстче, чем у остальных каналов: пуш
// проходит через чужую службу (Apple, Google, Mozilla) и лежит в системном
// центре уведомлений телефона, где его видит кто угодно. Поэтому обычное
// уведомление — заголовок и итог; закрытый ящик или «секретное» — только
// «есть новое»; полей и содержимого писем нет никогда. Число ждущих записей
// (для значка) — не содержимое и едет всегда.
//
// Что требует служба Apple (developer.apple.com, «Sending web push
// notifications in web apps and browsers»), и как это выполнено здесь:
//   - подпись VAPID: `sub` — настоящий mailto: или https:-адрес (иначе 403
//     BadJwtToken), `aud` — происхождение службы, срок не больше суток;
//   - подпись не обновлять чаще раза в час — pushAuthClient;
//   - тело — aes128gcm до 4 КБ, заголовки TTL, Urgency, Topic (до 32 знаков
//     из url-безопасного base64) — pushOne;
//   - пуш обязан показать уведомление сразу, иначе Safari отзывает подписку:
//     это делает sw.js, а здесь нечего скрывать от показа.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/PavluninVladimir/sema/server/internal/netguard"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	pushPublicSecret  = "push:vapid-public"
	pushPrivateSecret = "push:vapid-private"
	pushSubjectKey    = "push.subject"       // mailto: или https:-адрес, которым подписываемся у службы
	pushTTLKey        = "push.ttl_min"       // сколько минут служба хранит пуш для спящего устройства
	pushTTLAlertKey   = "push.ttl_alert_min" // то же для тревоги: она не должна протухать за ночь
	pushUrgencyKey    = "push.urgency."      // + уровень: very-low | low | normal | high
	pushStickyKey     = "push.sticky_alert"  // 1 — тревога на компьютере висит, пока её не закроют; по умолчанию 0

	// запасной адрес для связи: подпись с ним принимают Google и Mozilla,
	// а Apple — нет, поэтому он всегда считается «ненастоящим» и экран
	// говорит об этом вслух
	pushFallbackSubject = "mailto:sema@example.com"
)

// VAPIDKeys — пара ключей, рождается один раз и живёт в хранилище секретов:
// шифруется SEMA_KEY и едет с резервной копией. Потеря ключей — потеря всех
// подписок разом, и об этом говорится вслух (см. проверку в pushOne).
func (e *Engine) VAPIDKeys() (pub, priv string, err error) {
	pub, okPub := e.store.SecretValue(pushPublicSecret)
	priv, okPriv := e.store.SecretValue(pushPrivateSecret)
	if okPub && okPriv && pub != "" && priv != "" {
		return pub, priv, nil
	}
	if problem := e.store.SecretProblem(); problem != "" {
		// ключи есть, но не читаются: чужой SEMA_KEY. Заводить новые поверх
		// нельзя — это молча обнулило бы все подписки
		return "", "", fmt.Errorf("ключи пушей не читаются: %s", problem)
	}
	priv, pub, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	if err := e.store.SaveSecret(&store.Secret{Name: pushPublicSecret, Kind: store.SecretOther,
		Note: "открытый ключ пушей (VAPID)"}, pub); err != nil {
		return "", "", err
	}
	if err := e.store.SaveSecret(&store.Secret{Name: pushPrivateSecret, Kind: store.SecretOther,
		Note: "закрытый ключ пушей (VAPID), наружу не выдаётся"}, priv); err != nil {
		return "", "", err
	}
	log.Printf("пуши: ключи VAPID заведены")
	return pub, priv, nil
}

// PushPublicKey — то, что отдаётся браузеру при подписке.
func (e *Engine) PushPublicKey() (string, error) {
	pub, _, err := e.VAPIDKeys()
	return pub, err
}

// PushSubject — адрес для связи в подписи VAPID (поле `sub`), в том виде,
// как его ждёт служба: «mailto:…» или «https://…». real — адрес настоящий;
// без него запасной, и Apple такие пуши отвергает (403 BadJwtToken).
//
// Порядок: настройка `push.subject`; иначе адрес входа `auth.origin`, если
// он https и с настоящим именем; иначе запасной.
func (e *Engine) PushSubject() (subject string, real bool) {
	if v := strings.TrimSpace(e.store.SettingText(pushSubjectDef)); v != "" {
		if s, ok := NormalizePushSubject(v); ok {
			return s, true
		}
		// испорченная настройка не должна молча ронять пуши: уходим на запасной
		// адрес, а экран покажет, что адрес не настоящий
		return pushFallbackSubject, false
	}
	if origin, ok := e.mcpPublicOrigin(); ok {
		return origin, true
	}
	return pushFallbackSubject, false
}

// NormalizePushSubject — «ivan@example.com» → «mailto:ivan@example.com»;
// https-адрес остаётся как есть; всё иное, а также адрес с «localhost»,
// который никому не принадлежит, — не подходит.
func NormalizePushSubject(v string) (string, bool) {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(v, "https://"):
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" || u.User != nil || !pushContactHost(u.Hostname()) {
			return "", false
		}
		return strings.TrimRight(v, "/"), true
	case strings.HasPrefix(strings.ToLower(v), "mailto:"):
		return NormalizePushSubject(v[len("mailto:"):])
	}
	at := strings.LastIndex(v, "@")
	if at <= 0 || strings.ContainsAny(v, " \t\r\n<>,;") || !pushContactHost(v[at+1:]) {
		return "", false
	}
	return "mailto:" + v, true
}

// pushContactHost — у адреса для связи должно быть имя, по которому можно
// кого-то найти: «localhost» и имена своей сети не годятся.
func pushContactHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.Contains(host, ".") {
		return false
	}
	for _, own := range []string{".localhost", ".local", ".lan", ".internal", ".home.arpa"} {
		if strings.HasSuffix(host, own) {
			return false
		}
	}
	return true
}

// HTTPSOK — Сёма открывается по защищённому адресу: либо сам слушает с
// сертификатом, либо адрес входа — https за своим прокси (так и живёт Сёма
// за шлюзом). Без https браузер не даст ни подписку, ни значок.
func (e *Engine) HTTPSOK() bool {
	return e.TLSOn() || strings.HasPrefix(strings.TrimSpace(e.store.SettingText(store.AuthOrigin)), "https://")
}

// libSubject — адрес для библиотеки. Она дописывает «mailto:» ко всему, что
// не начинается с «https:», и готовое «mailto:sema@localhost» превращала в
// «mailto:mailto:sema@localhost» — подпись, которую Apple не принимает.
func libSubject(subject string) string {
	return strings.TrimPrefix(subject, "mailto:")
}

// PushSubscribe — устройство подписалось. Запоминаем, под каким ключом.
// ua — строка браузера из заголовка запроса, standalone и touch говорит сама
// страница: по ним выводится имя.
func (e *Engine) PushSubscribe(endpoint, p256dh, auth, ua string, standalone bool, touch int) (*store.PushSubscription, error) {
	// сервер потом делает POST на этот адрес: браузер присылает только
	// https-точки служб пушей, всё иное — ошибка, а не будущий запрос куда попало
	if !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("адрес подписки должен быть https")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("неверный адрес подписки")
	}
	if err := netguard.ValidateURL(u); err != nil {
		return nil, err
	}
	pub, _, err := e.VAPIDKeys()
	if err != nil {
		return nil, err
	}
	sub := &store.PushSubscription{
		Endpoint: endpoint, P256dh: p256dh, Auth: auth, VAPIDKey: pub,
		Agent: clip(PushDeviceName(ua, standalone, touch, endpoint), 80),
	}
	if err := e.store.SavePushSubscription(sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// PushUnsubscribe — убрать устройство по адресу у службы пушей.
func (e *Engine) PushUnsubscribe(endpoint string) error {
	return e.store.DeletePushSubscription(endpoint)
}

// PushDeviceStatus — что сервер знает про устройство с этим адресом: страница
// спрашивает при каждом открытии настроек. Не знает — значит подписку
// забыли (убрали, служба ответила 410, база восстановлена из копии), и
// страница подпишет заново сама; ключ другой — переподписать надо жестом.
func (e *Engine) PushDeviceStatus(endpoint string) (known bool, id int64, name string, sameKey bool) {
	p, ok := e.store.PushSubscriptionByEndpoint(endpoint)
	if !ok {
		return false, 0, "", false
	}
	pub, _, err := e.VAPIDKeys()
	return true, p.ID, PushDeviceLabel(p), err == nil && (p.VAPIDKey == "" || p.VAPIDKey == pub)
}

// ---- что уезжает ----

// pushPayload — что уедет в чужую службу. Правило спеки: содержимого писем
// нет никогда; закрытый ящик или «секретное» — только «есть новое».
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
	Level string `json:"level"`
	// Renotify — заменить прежнее уведомление с тем же тегом заново, со
	// звуком. Только для тревоги: ради «внимания» второй раз не будят
	Renotify bool `json:"renotify,omitempty"`
	// Sticky — уведомление висит в центре уведомлений компьютера, пока его
	// не закроют (`requireInteraction`). Истина только у тревоги и только
	// когда человек включил `push.sticky_alert`; работник сам по уровню не
	// решает. Общий пуш после тихих часов и проба — без него
	Sticky bool `json:"sticky,omitempty"`
	// Waiting — сколько записей ждёт человека: служебный работник ставит это
	// число на значок приложения, когда страница закрыта
	Waiting int `json:"waiting"`
}

const (
	pushScreen     = "/#screen=notifications"
	pushTestScreen = "/#screen=settings:notify"
)

func (e *Engine) pushPayload(n *store.Notification) pushPayload {
	p := pushPayload{Title: clip(n.Title, 120), Body: clip(n.Summary, 160), URL: pushScreen,
		Tag: pushTag(n), Level: n.Level, Renotify: n.Level == "alert", Sticky: e.pushSticky(n.Level), Waiting: e.NotifyCounter()}
	if e.pushMustBeMute(n) {
		p.Title, p.Body = "Сёма: есть новое", ""
	}
	return p
}

// pushTopic — признак, по которому служба заменяет не доставленный пуш
// новым (RFC 8030, заголовок Topic: до 32 знаков url-безопасного base64).
// Берётся от ключа записи: «диск почти полон» → «диск полон» заменяет
// прежнее, а два разных дела друг друга не вытесняют. Раньше стоял один
// общий «sema», и из трёх пушей, пришедших спящему телефону, оставался
// последний.
func pushTopic(n *store.Notification) string {
	if n.ID == 0 && n.Key == "" {
		return pushTopicOf(pushTestTopic)
	}
	key := n.Key
	if key == "" {
		key = "id:" + strconv.FormatInt(n.ID, 10)
	}
	return pushTopicOf(key)
}

// у пробы и у общего пуша после тихих часов записи нет — у них свои ключи
const (
	pushTestTopic    = "sema:проба"
	pushSummaryTopic = "sema:сводка"
)

// pushTopicOf — Topic из ключа: 24 знака url-безопасного base64, то есть
// ровно 18 байт. Apple заголовок **раскодирует**, а не только смотрит на
// буквы: 1 октября 2026 проба уходила с Topic «sema-test» — буквы годные,
// но девять знаков не бывают base64 (длина по модулю 4 равна единице), и
// iPhone получал 400 BadWebPushTopic, а Google тот же пуш принимал. Поэтому
// Topic словом не пишется никогда — только через эту функцию.
func pushTopicOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:24]
}

// pushTag — по нему устройство заменяет уже показанное уведомление: тот же
// ключ — то же уведомление в центре уведомлений, а не очередная строка.
func pushTag(n *store.Notification) string {
	topic := pushTopic(n)
	if len(topic) > 12 {
		topic = topic[:12]
	}
	return "sema-" + topic
}

// pushMustBeMute — нельзя показывать ничего, кроме факта. Письмо из
// закрытого ящика и «секретное» — да; уведомление про почту, у которого
// письма не найти, — тоже да: не знаем, значит не показываем.
func (e *Engine) pushMustBeMute(n *store.Notification) bool {
	ref, ok := strings.CutPrefix(n.Ref, "msg:")
	if !ok {
		return false
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return true
	}
	m, err := e.store.Message(id)
	if err != nil {
		return true
	}
	return e.mailboxClosedForCloud(m.Account) || e.ClassOfMessage(m) == store.ClassSecret
}

// pushSticky — висит ли уведомление, пока его не закроют: только тревога и
// только по настройке `push.sticky_alert` (по умолчанию выключена). Каждая
// тревога, остающаяся в центре уведомлений мака до ручного закрытия, — не
// то, что стоит включать без просьбы.
func (e *Engine) pushSticky(level string) bool {
	if level != "alert" {
		return false
	}
	return e.store.SettingFlag(pushStickyDef)
}

// PushStickyAlert — включено ли «тревога висит, пока не закроют»: для формы.
func (e *Engine) PushStickyAlert() bool { return e.pushSticky("alert") }

// pushTTL — сколько секунд служба хранит пуш, пока устройство спит или без
// сети. Тревога живёт сутки: ночью выключенный телефон должен её получить
// утром, а не потерять.
func (e *Engine) pushTTL(level string) int {
	def := pushTTLDef
	if level == "alert" {
		def = pushTTLAlertDef
	}
	v := e.store.SettingInt(def)
	if v > 60*24*28 {
		v = 60 * 24 * 28 // Apple хранит не дольше 30 дней, а 4 недели — предел и у других
	}
	return v * 60
}

// pushUrgency — срочность (RFC 8030): служба решает, будить ли устройство
// сейчас или отложить до зарядки и Wi-Fi. Тревога — high, остальное — normal;
// настройка `push.urgency.<уровень>` меняет.
func (e *Engine) pushUrgency(level string) webpush.Urgency {
	def := webpush.UrgencyNormal
	if level == "alert" {
		def = webpush.UrgencyHigh
	}
	// настройка есть у трёх уровней, у остальных (ok) читать нечего
	reg, known := store.LookupSetting(pushUrgencyKey + level)
	if !known {
		return def
	}
	switch u := webpush.Urgency(strings.TrimSpace(e.store.SettingText(reg))); u {
	case webpush.UrgencyVeryLow, webpush.UrgencyLow, webpush.UrgencyNormal, webpush.UrgencyHigh:
		return u
	}
	return def
}

// ---- подпись, которую служба не просит обновлять чаще раза в час ----

// pushAuthCache хранит готовую подпись VAPID для службы. Apple: «Don't
// refresh your JWT more frequently than once per hour». Библиотека же
// подписывает заново на каждую отправку; подмена заголовка в клиенте — самый
// короткий способ уложиться, не переписывая шифрование.
type pushAuthCache struct {
	mu   sync.Mutex
	byAu map[string]pushAuthEntry
}

type pushAuthEntry struct {
	header string
	at     time.Time
}

// pushNow — часы подписи; переменная ради теста, как scriptHTTP.
var pushNow = time.Now

const pushAuthLife = time.Hour

// pushAuthClient подставляет сохранённую подпись вместо свежей, пока той
// нет часа. Ключом служит служба, адрес для связи и открытый ключ: смена
// любого из них сразу даёт новую подпись.
type pushAuthClient struct {
	inner   webpush.HTTPClient
	cache   *pushAuthCache
	subject string
}

func (c *pushAuthClient) Do(req *http.Request) (*http.Response, error) {
	fresh := req.Header.Get("Authorization")
	key := req.URL.Scheme + "://" + req.URL.Host + "|" + c.subject + "|" + vapidKeyOf(fresh)
	c.cache.mu.Lock()
	if c.cache.byAu == nil {
		c.cache.byAu = map[string]pushAuthEntry{}
	}
	if was, ok := c.cache.byAu[key]; ok && pushNow().Sub(was.at) < pushAuthLife {
		req.Header.Set("Authorization", was.header)
	} else {
		c.cache.byAu[key] = pushAuthEntry{header: fresh, at: pushNow()}
	}
	c.cache.mu.Unlock()
	return c.inner.Do(req)
}

// vapidKeyOf — открытый ключ из заголовка «vapid t=…, k=…».
func vapidKeyOf(header string) string {
	if i := strings.LastIndex(header, "k="); i >= 0 {
		return header[i+2:]
	}
	return ""
}

// ---- отправка одному устройству ----

// pushResult — чем кончилась отправка одному устройству.
type pushResult struct {
	Status int    // ответ службы; 0 — до службы не дошли
	Err    string // человеческий текст; пусто — служба приняла
	Gone   bool   // служба сказала «такой подписки нет»
	Resign bool   // подписка сделана под другим ключом Сёмы
}

func (r pushResult) OK() bool { return r.Err == "" && !r.Gone }

// pushOne отправляет один пуш на одно устройство и записывает исход в его
// строку. Ничего не решает про порог и тихие часы — это дело вызывающего.
func (e *Engine) pushOne(ctx context.Context, s *store.PushSubscription, p pushPayload, topic, level string) pushResult {
	res := e.pushDo(ctx, s, p, topic, level)
	switch {
	case res.Gone:
		_ = e.store.DeletePushSubscription(s.Endpoint)
		log.Printf("пуши: устройство «%s» отписалось (ответ %d) — запись убрана", PushDeviceLabel(s), res.Status)
		e.pushGoneNotice(s)
	case res.Resign:
		e.store.MarkPushDelivery(s.Endpoint, res.Err, res.Status)
		e.requireResign(s)
	default:
		e.store.MarkPushDelivery(s.Endpoint, res.Err, res.Status)
	}
	return res
}

func (e *Engine) pushDo(ctx context.Context, s *store.PushSubscription, p pushPayload, topic, level string) pushResult {
	pub, priv, err := e.VAPIDKeys()
	if err != nil {
		return pushResult{Err: shortErr(err.Error())}
	}
	if s.VAPIDKey != "" && s.VAPIDKey != pub {
		// подписка сделана под другим ключом: служба её отвергнет, а
		// человек не поймёт, почему пуши пропали
		return pushResult{Err: "ключ пушей сменился — переподпишите устройство", Resign: true}
	}
	body, _ := json.Marshal(p)
	subject, _ := e.PushSubject()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	resp, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{
		Endpoint: s.Endpoint, Keys: webpush.Keys{Auth: s.Auth, P256dh: s.P256dh},
	}, &webpush.Options{
		HTTPClient:     &pushAuthClient{inner: scriptHTTP, cache: &e.pushAuth, subject: subject},
		Subscriber:     libSubject(subject),
		VAPIDPublicKey: pub, VAPIDPrivateKey: priv,
		TTL: e.pushTTL(level), Topic: topic, Urgency: e.pushUrgency(level),
	})
	if err != nil {
		return pushResult{Err: "до службы пушей не дошли: " + shortErr(err.Error())}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		return pushResult{Status: resp.StatusCode}
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	reason := pushReason(raw)
	res := pushResult{Status: resp.StatusCode}
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		res.Gone = true
		res.Err = pushFailure(s.Endpoint, resp.StatusCode, reason, "")
	default:
		res.Err = pushFailure(s.Endpoint, resp.StatusCode, reason, resp.Header.Get("Retry-After"))
		res.Resign = resp.StatusCode == http.StatusBadRequest && reason == "VapidPkHashMismatch"
	}
	return res
}

// pushReason — причина отказа из тела ответа: у Apple это {"reason":
// "BadJwtToken"}, у Mozilla {"message": …}, у Google обычный текст. Только
// коротко и одной строкой: тело чужое и в журнал уходит как есть.
func pushReason(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	var js map[string]any
	if json.Unmarshal(raw, &js) == nil {
		for _, k := range []string{"reason", "message", "error"} {
			if v, ok := js[k].(string); ok && strings.TrimSpace(v) != "" {
				return shortErr(v)
			}
		}
		return ""
	}
	return shortErr(text)
}

// pushFailure — что случилось, словами. Коды Apple перечислены в её
// документации и узнаются по `reason`; у остальных служб разбор по статусу.
func pushFailure(endpoint string, status int, reason, retryAfter string) string {
	svc := PushServiceName(endpoint)
	with := func(text string) string {
		if reason != "" {
			text += " [" + reason + "]"
		}
		return text
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		return with(svc + " ответила, что подписки больше нет (" + strconv.Itoa(status) + ")")
	case reason == "VapidPkHashMismatch" || (status == http.StatusForbidden && reason != "" &&
		strings.Contains(strings.ToLower(reason), "mismatch")):
		return with(svc + ": подписка сделана под другим ключом Сёмы (" + strconv.Itoa(status) + ") — уберите устройство и подпишите заново")
	case reason == "BadJwtToken", reason == "BadAuthorizationHeader", reason == "BadVapidPublicKey":
		return with(svc + " не приняла подпись Сёмы (" + strconv.Itoa(status) +
			"): чаще всего дело в адресе для связи — нужен настоящий https:// или mailto: (поле «Адрес для связи» ниже)")
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return with(svc + " не приняла подпись Сёмы (" + strconv.Itoa(status) +
			"): либо неверный адрес для связи, либо подписка сделана под другим ключом — задайте адрес ниже или подпишите устройство заново")
	case status == http.StatusRequestEntityTooLarge:
		return with(svc + ": сообщение слишком длинное (413)")
	case status == http.StatusTooManyRequests:
		text := svc + " просит не торопиться (429)"
		if retryAfter != "" {
			text += ", повтор не раньше чем через " + retryAfter + " с"
		}
		return with(text)
	case status >= 500:
		return with(svc + " сейчас недоступна (" + strconv.Itoa(status) + "), попробуем позже")
	case status == http.StatusBadRequest:
		return with(svc + " не приняла запрос (400)")
	}
	return with(svc + ": ответ " + strconv.Itoa(status))
}

// pushGoneNotice — устройство убрано по слову службы. Молча ронять
// подписку нельзя: человек решит, что пуши «просто перестали ходить».
func (e *Engine) pushGoneNotice(s *store.PushSubscription) {
	name := PushDeviceLabel(s)
	_, _ = e.Notify(&store.Notification{
		Key: "push:gone:" + strconv.FormatInt(s.ID, 10), Source: "core", Level: "info",
		Title: "Устройство отписалось от пушей: " + name,
		Summary: "Служба пушей ответила, что подписки больше нет: на iPhone так бывает, когда разрешение выключили в настройках " +
			"или несколько пушей подряд не показали уведомления. Откройте «Ждут вас» на этом устройстве и нажмите «Получать пуши» — " +
			"кнопка стоит наверху списка. Какие устройства подписаны — в «Настройки → Уведомления».",
		// переход ведёт к списку устройств: сама кнопка подписки лежит на
		// «Ждут вас», где эта запись и находится
		Actions: []store.NotifyAction{{Kind: "goto", Label: "Устройства", Screen: "settings:notify"}},
	})
}

// requireResign — ключ сменился, подписка мертва. Молчать нельзя:
// человек решит, что пуши просто перестали ходить.
func (e *Engine) requireResign(s *store.PushSubscription) {
	_, _ = e.Notify(&store.Notification{
		Key: "push:resub:" + strconv.FormatInt(s.ID, 10), Source: "core", Level: "warn",
		Title: "Переподпишите устройство на пуши: " + PushDeviceLabel(s),
		Summary: "Ключи пушей сменились (перенос или потеря SEMA_KEY), и прежняя подписка больше не работает. Откройте «Ждут вас» на этом устройстве " +
			"и нажмите «Переподписать» — кнопка стоит наверху списка. Какие устройства подписаны — в «Настройки → Уведомления».",
		Actions: []store.NotifyAction{{Kind: "goto", Label: "Устройства", Screen: "settings:notify"}},
	})
}

// ---- доставка записи ----

// pushEffective — канал глазами устройства: свои порог и тихие часы,
// где они заданы, иначе канальные. Источники и «включён» остаются
// канальными — они про канал, а не про устройство.
func pushEffective(c *store.NotifyChannel, d *store.PushSubscription) *store.NotifyChannel {
	eff := *c
	if d.MinLevel != "" {
		eff.MinLevel = d.MinLevel
	}
	switch d.Quiet {
	case "":
	case "-":
		eff.QuietFrom, eff.QuietTo = "", ""
	default:
		eff.QuietFrom, eff.QuietTo, _ = strings.Cut(d.Quiet, "-")
	}
	return &eff
}

// deliverWeb — отправка записи на устройства канала `web`. Каждое решает по
// своим порогу и тихим часам: телефону ночью тишина, Mac — как был. Что
// устройству не ушло из-за тихих часов, ждёт их конца (resendPushPending).
//
// 404 и 410 от службы означают «подписки больше нет» — запись удаляется
// сразу, а не копится отказами. Сменившийся ключ — не отказ службы, а наша
// беда, и о ней говорится человеку.
func (e *Engine) deliverWeb(ctx context.Context, n *store.Notification, c *store.NotifyChannel) {
	if _, _, err := e.VAPIDKeys(); err != nil {
		if channelTakes(c, n, NotifySourceGroup(n), time.Now()) == "" {
			e.store.MarkDelivered(n.ID, c.Kind, err.Error())
		}
		return
	}
	subs, err := e.store.PushSubscriptions()
	group, now := NotifySourceGroup(n), time.Now()
	if err != nil || len(subs) == 0 {
		// пустой список — беда только если канал вообще взял бы запись
		if channelTakes(c, n, group, now) == "" {
			e.store.MarkDelivered(n.ID, c.Kind, "подписанных устройств нет")
		}
		return
	}
	if !c.Enabled || (c.Sources != "" && !hasString(strings.Split(strings.ReplaceAll(c.Sources, " ", ""), ","), group)) {
		return
	}
	payload, topic := e.pushPayload(n), pushTopic(n)

	type sent struct {
		s *store.PushSubscription
		r pushResult
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var done []sent
	for _, s := range subs {
		switch why := channelTakes(pushEffective(c, s), n, group, now); {
		case why == "":
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := e.pushOne(ctx, s, payload, topic, n.Level)
				if r.OK() {
					// ушло сейчас — ждать конца тихих часов нечего: иначе
					// отложенное ранее ушло бы устройству ещё раз
					e.store.ForgetDeferredPush(s.ID, n.ID)
				}
				mu.Lock()
				done = append(done, sent{s, r})
				mu.Unlock()
			}()
		case strings.HasPrefix(why, "тихие часы"):
			e.store.DeferPush(s.ID, n.ID)
		}
	}
	wg.Wait()
	if len(done) == 0 {
		return
	}
	delivered := false
	var errs []string
	for _, d := range done {
		if d.r.OK() {
			delivered = true
		} else if d.r.Err != "" {
			errs = append(errs, PushDeviceLabel(d.s)+": "+d.r.Err)
		}
	}
	// хоть одно устройство получило — запись доставлена: повторять её на
	// все устройства значило бы дважды будить то, что уже получило
	if delivered {
		e.store.MarkDelivered(n.ID, c.Kind, "")
	}
	if len(errs) > 0 {
		e.store.MarkDelivered(n.ID, c.Kind, strings.Join(errs, "; "))
	}
}

// pushSummary — один пуш вместо десятка: за тихие часы набралось несколько
// записей, и будить ими устройство по одной утром незачем. Содержимого
// записей в нём нет, только счёт и ссылка на экран.
func (e *Engine) pushSummary(list []*store.Notification) pushPayload {
	level := "info"
	for _, n := range list {
		if store.LevelRank(n.Level) > store.LevelRank(level) {
			level = n.Level
		}
	}
	return pushPayload{Title: "Сёма: есть новое — " + strconv.Itoa(len(list)), URL: pushScreen,
		Tag: "sema-summary", Level: level, Waiting: e.NotifyCounter()}
}

// resendPushPending — после тихих часов устройство получает то, что за них
// накопилось: одну запись пушем, несколько — одним общим пушем. Зовётся
// тем же часовым проходом, что и повтор недоставленного.
func (e *Engine) resendPushPending(now time.Time) int {
	chans, _ := e.store.NotifyChannels()
	var web *store.NotifyChannel
	for _, c := range chans {
		if c.Kind == "web" && c.Enabled {
			web = c
			break
		}
	}
	if web == nil {
		return 0
	}
	subs, _ := e.store.PushSubscriptions()
	oldest := now.Add(-time.Duration(e.notifyRetryHours()) * time.Hour)
	sent := 0
	for _, s := range subs {
		ids := e.store.DeferredPush(s.ID)
		if len(ids) == 0 || inQuietHours(pushEffective(web, s), now) {
			continue
		}
		var live []*store.Notification
		var drop []int64
		for _, id := range ids {
			n, ok := e.store.Notification(id)
			if !ok {
				drop = append(drop, id)
				continue
			}
			created, _ := time.Parse(time.RFC3339, n.Created)
			if n.State != store.NotifyOpen || created.Before(oldest) {
				drop = append(drop, id) // разобрано или устарело: будить незачем
				continue
			}
			live = append(live, n)
		}
		e.store.ForgetDeferredPush(s.ID, drop...)
		if len(live) == 0 {
			continue
		}
		var res pushResult
		var done []int64
		for _, n := range live {
			done = append(done, n.ID)
		}
		if len(live) == 1 {
			res = e.pushOne(context.Background(), s, e.pushPayload(live[0]), pushTopic(live[0]), live[0].Level)
		} else {
			p := e.pushSummary(live)
			res = e.pushOne(context.Background(), s, p, pushTopicOf(pushSummaryTopic), p.Level)
		}
		if res.OK() {
			e.store.ForgetDeferredPush(s.ID, done...)
			// отправленное помечается доставленным каналу: следом в том же
			// проходе идёт повтор недоставленного, и запись, которую не
			// забрал другой канал, иначе ушла бы устройству второй раз — а
			// вместе с общим пушем «есть новое — N» ещё и N одиночных
			for _, id := range done {
				e.store.MarkDelivered(id, web.Kind, "")
			}
			sent += len(done)
		}
	}
	return sent
}

// ---- проба ----

// pushTest — пробное уведомление: ни порогов, ни тихих часов, оно же
// проверяет, что всё доходит.
func (e *Engine) pushTest(s *store.PushSubscription) pushResult {
	n := &store.Notification{ID: 0, Title: "Проба пушей Сёмы", Summary: "Если вы это видите — пуши на «" + PushDeviceLabel(s) + "» работают.",
		Level: "info"}
	p := e.pushPayload(n)
	p.URL = pushTestScreen
	p.Sticky = false // проба не должна зависать в центре уведомлений
	return e.pushOne(context.Background(), s, p, pushTopic(n), "info")
}

// pushResultLine — строка о том, что вышло, человеческими словами.
func pushResultLine(s *store.PushSubscription, r pushResult) string {
	name := PushDeviceLabel(s)
	switch {
	case r.OK():
		return name + ": принято, ответ " + PushServiceName(s.Endpoint) + " " + strconv.Itoa(r.Status)
	case r.Err != "":
		return name + ": " + r.Err
	}
	return name + ": ничего не вышло"
}

// TestPushDevice — «Проверить» на одном устройстве: пробный пуш именно ему и
// ответ службы человеческой строкой. Ошибка — когда не принято.
func (e *Engine) TestPushDevice(id int64) (string, error) {
	s, ok := e.store.PushSubscriptionByID(id)
	if !ok {
		return "", fmt.Errorf("такого устройства нет")
	}
	r := e.pushTest(s)
	line := pushResultLine(s, r)
	if !r.OK() {
		return line, fmt.Errorf("%s", line)
	}
	return line, nil
}

// TestPush — «Проверить» на канале: пробное сообщение на все устройства.
// Строка — что вышло словами по каждому; ошибка — если хоть одно не приняло
// или устройств нет вовсе.
func (e *Engine) TestPush(ctx context.Context) (string, error) {
	subs, _ := e.store.PushSubscriptions()
	if len(subs) == 0 {
		return "", fmt.Errorf("подписанных устройств нет: откройте Сёму в браузере по https и нажмите «Получать пуши»")
	}
	var out []string
	failed := false
	for _, s := range subs {
		r := e.pushTest(s)
		failed = failed || !r.OK()
		out = append(out, pushResultLine(s, r))
	}
	line := strings.Join(out, "; ")
	if failed {
		return line, fmt.Errorf("%s", line)
	}
	return line, nil
}
