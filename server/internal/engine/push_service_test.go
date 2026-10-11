package engine

// Отправка пушей против поддельной службы пушей. Служба здесь — настоящий
// HTTPS-сервер, который делает то, что делают Apple, Google и Mozilla:
// проверяет подпись VAPID (адрес службы, срок, адрес для связи), читает
// заголовки TTL, Urgency, Topic и расшифровывает тело по RFC 8291 ключом
// «устройства». Без этого тесты проверяли бы только, что мы что-то
// отправили, а не что отправленное принял бы настоящий получатель.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// pushRecord — один принятый запрос.
type pushRecord struct {
	Path   string
	Header http.Header
	Body   []byte
}

// pushReply — что служба ответит на этот путь.
type pushReply struct {
	Status int
	Body   string
	Header map[string]string
}

type fakePushService struct {
	srv   *httptest.Server
	mu    sync.Mutex
	got   []pushRecord
	reply map[string]pushReply
}

// newFakePushService поднимает службу и подменяет клиента, которым Сёма ходит
// к службам пушей (как web_page_test подменяет scriptHTTP).
func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	f := &fakePushService{reply: map[string]pushReply{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, pushRecord{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
		rep, ok := f.reply[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			rep = pushReply{Status: http.StatusCreated}
		}
		for k, v := range rep.Header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rep.Status)
		_, _ = w.Write([]byte(rep.Body))
	}))
	was := scriptHTTP
	scriptHTTP = f.srv.Client()
	t.Cleanup(func() { scriptHTTP = was; f.srv.Close() })
	return f
}

func (f *fakePushService) records(path string) []pushRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pushRecord
	for _, r := range f.got {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakePushService) answer(path string, rep pushReply) {
	f.mu.Lock()
	f.reply[path] = rep
	f.mu.Unlock()
}

// pushDevice — устройство глазами теста: у него есть закрытый ключ, которым
// расшифровывается всё, что на него отправили.
type pushDevice struct {
	Sub  *store.PushSubscription
	priv *ecdh.PrivateKey
	auth []byte
}

func (f *fakePushService) device(t *testing.T, e *Engine, path string) *pushDevice {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	pub, _, err := e.VAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	sub := &store.PushSubscription{Endpoint: f.srv.URL + path,
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(auth), VAPIDKey: pub, Agent: "Проба " + strings.TrimPrefix(path, "/push/")}
	if err := e.store.SavePushSubscription(sub); err != nil {
		t.Fatal(err)
	}
	return &pushDevice{Sub: sub, priv: priv, auth: auth}
}

// decrypt — расшифровка тела по RFC 8291 (aes128gcm) закрытым ключом устройства.
func (d *pushDevice) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("тело короче заголовка: %d", len(body))
	}
	salt := body[:16]
	idlen := int(body[20])
	keyID := body[21 : 21+idlen]
	cipherText := body[21+idlen:]
	asPub, err := ecdh.P256().NewPublicKey(keyID)
	if err != nil {
		t.Fatalf("ключ отправителя в заголовке: %v", err)
	}
	secret, err := d.priv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), d.priv.PublicKey().Bytes()...)
	info = append(info, keyID...)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, d.auth, info), ikm); err != nil {
		t.Fatal(err)
	}
	cek := make([]byte, 16)
	_, _ = io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")), cek)
	nonce := make([]byte, 12)
	_, _ = io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")), nonce)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		t.Fatalf("тело не расшифровалось ключом устройства: %v", err)
	}
	// дополнение: данные, байт 0x02, нули до конца записи
	end := len(plain)
	for end > 0 && plain[end-1] == 0 {
		end--
	}
	if end == 0 || plain[end-1] != 0x02 {
		t.Fatalf("нет разделителя конца записи в расшифрованном теле")
	}
	return plain[:end-1]
}

// payload — расшифрованное тело как pushPayload.
func (d *pushDevice) payload(t *testing.T, r pushRecord) pushPayload {
	t.Helper()
	var p pushPayload
	if err := json.Unmarshal(d.decrypt(t, r.Body), &p); err != nil {
		t.Fatalf("тело не JSON: %v", err)
	}
	return p
}

// checkVAPID проверяет подпись так, как проверяет служба: подпись верна
// открытым ключом из заголовка, ключ — наш, `aud` — происхождение службы,
// срок не дальше суток, `sub` — ровно заданный. Возвращает claims.
func checkVAPID(t *testing.T, header, wantAud, wantSub, wantKey string) map[string]any {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid ")
	if !ok {
		t.Fatalf("Authorization не vapid: %q", header)
	}
	var jwt, key string
	for _, part := range strings.Split(rest, ", ") {
		switch {
		case strings.HasPrefix(part, "t="):
			jwt = part[2:]
		case strings.HasPrefix(part, "k="):
			key = part[2:]
		}
	}
	if key != wantKey {
		t.Fatalf("в подписи не наш открытый ключ: %q", key)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT из %d частей", len(parts))
	}
	raw, _ := base64.RawURLEncoding.DecodeString(key)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		t.Fatalf("открытый ключ VAPID: %v", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("подпись ES256 должна быть 64 байта: %v %d", err, len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("подпись JWT не сходится с ключом из заголовка")
	}
	claimsRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != wantAud {
		t.Errorf("aud = %v, ждали происхождение службы %q", claims["aud"], wantAud)
	}
	if claims["sub"] != wantSub {
		t.Errorf("sub = %v, ждали %q", claims["sub"], wantSub)
	}
	exp, _ := claims["exp"].(float64)
	if left := time.Until(time.Unix(int64(exp), 0)); left <= 0 || left > 24*time.Hour {
		t.Errorf("срок подписи через %v: Apple требует не дальше суток", left)
	}
	return claims
}

// pushTestChannel — канал `web`, каким он живёт у Сёмы: от «внимание», без
// тихих часов.
func pushTestChannel() *store.NotifyChannel {
	return &store.NotifyChannel{Kind: "web", Title: "Пуши", Enabled: true, MinLevel: "warn"}
}

func putNote(t *testing.T, st *store.Store, n *store.Notification) *store.Notification {
	t.Helper()
	if n.Source == "" {
		n.Source = "core"
	}
	if _, err := st.PutNotification(n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Служба принимает то, что мы шлём: подпись, заголовки, шифрование и состав.
func TestPushAcceptedByService(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	dev := f.device(t, e, "/push/a")
	_ = st.SetSetting("auth.origin", "https://sema.example.org")
	pub, _, _ := e.VAPIDKeys()

	alert := putNote(t, st, &store.Notification{Key: "server:диск", Level: "alert", Title: "Диск на srv-bots", Summary: "занято 95%"})
	e.deliverWeb(t.Context(), alert, pushTestChannel())

	got := f.records("/push/a")
	if len(got) != 1 {
		t.Fatalf("на устройство ушло %d пушей, ждали 1", len(got))
	}
	r := got[0]
	if r.Header.Get("Content-Encoding") != "aes128gcm" {
		t.Errorf("Content-Encoding = %q, ждали aes128gcm", r.Header.Get("Content-Encoding"))
	}
	if len(r.Body) > 4096 {
		t.Errorf("тело %d байт: больше 4096, Apple отвечает 413", len(r.Body))
	}
	if r.Header.Get("TTL") != "86400" {
		t.Errorf("TTL тревоги = %q, ждали сутки (86400)", r.Header.Get("TTL"))
	}
	if r.Header.Get("Urgency") != "high" {
		t.Errorf("Urgency тревоги = %q, ждали high", r.Header.Get("Urgency"))
	}
	topic := r.Header.Get("Topic")
	if topic != pushTopic(alert) || len(topic) == 0 || len(topic) > 32 {
		t.Errorf("Topic = %q: нужен от ключа записи и не длиннее 32 знаков", topic)
	}
	for _, ch := range topic {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", ch) {
			t.Errorf("Topic %q: знак %q вне url-безопасного base64", topic, ch)
		}
	}
	// aud — то, что у Apple называется «происхождение службы»; sub — адрес входа
	checkVAPID(t, r.Header.Get("Authorization"), f.srv.URL, "https://sema.example.org", pub)

	p := dev.payload(t, r)
	if p.Title != "Диск на srv-bots" || p.Body != "занято 95%" || p.Level != "alert" {
		t.Errorf("состав пуша: %+v", p)
	}
	if p.URL != "/#screen=notifications" || p.Tag != pushTag(alert) || !p.Renotify || p.Waiting != 1 {
		t.Errorf("адрес, тег, повтор и счётчик: %+v", p)
	}

	// запись доставлена, устройство отмечено живым с ответом службы
	if n, _ := st.Notification(alert.ID); n.Undelivered() {
		t.Error("запись не отмечена доставленной")
	}
	d, _ := st.PushSubscriptionByID(dev.Sub.ID)
	if d.LastOK == "" || d.LastErr != "" || d.LastStatus != 201 {
		t.Errorf("исход не записан: %+v", d)
	}
}

// Срочность и срок хранения — по уровню и настройкам, а не одни на всех.
func TestPushUrgencyAndTTLByLevel(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	f.device(t, e, "/push/a")

	warn := putNote(t, st, &store.Notification{Key: "w", Level: "warn", Title: "Внимание"})
	e.deliverWeb(t.Context(), warn, pushTestChannel())
	r := f.records("/push/a")[0]
	if r.Header.Get("Urgency") != "normal" || r.Header.Get("TTL") != "3600" {
		t.Errorf("«внимание»: Urgency %q TTL %q, ждали normal и 3600", r.Header.Get("Urgency"), r.Header.Get("TTL"))
	}

	// настройки меняют, а непонятное значение молча возвращает обычное
	_ = st.SetSetting("push.urgency.warn", "low")
	_ = st.SetSetting("push.ttl_min", "5")
	_ = st.SetSetting("push.urgency.alert", "very-low")
	_ = st.SetSetting("push.ttl_alert_min", "мусор")
	warn2 := putNote(t, st, &store.Notification{Key: "w2", Level: "warn", Title: "Ещё"})
	alert := putNote(t, st, &store.Notification{Key: "a", Level: "alert", Title: "Тревога"})
	e.deliverWeb(t.Context(), warn2, pushTestChannel())
	e.deliverWeb(t.Context(), alert, pushTestChannel())
	rs := f.records("/push/a")
	if rs[1].Header.Get("Urgency") != "low" || rs[1].Header.Get("TTL") != "300" {
		t.Errorf("по настройкам: Urgency %q TTL %q, ждали low и 300", rs[1].Header.Get("Urgency"), rs[1].Header.Get("TTL"))
	}
	if rs[2].Header.Get("Urgency") != "very-low" || rs[2].Header.Get("TTL") != "86400" {
		t.Errorf("тревога: Urgency %q TTL %q, ждали very-low и 86400 при испорченной настройке", rs[2].Header.Get("Urgency"), rs[2].Header.Get("TTL"))
	}
}

// Topic — от ключа записи: то же дело заменяет прежнее у службы и на
// устройстве, разные дела друг друга не вытесняют.
func TestPushTopicByKey(t *testing.T) {
	a := &store.Notification{ID: 1, Key: "server:диск"}
	b := &store.Notification{ID: 2, Key: "server:диск"} // тот же ключ, новая запись
	c := &store.Notification{ID: 3, Key: "server:память"}
	if pushTopic(a) != pushTopic(b) || pushTag(a) != pushTag(b) {
		t.Error("тот же ключ должен давать тот же Topic и тег")
	}
	if pushTopic(a) == pushTopic(c) || pushTag(a) == pushTag(c) {
		t.Error("разные ключи не должны вытеснять друг друга")
	}
	if len(pushTopic(a)) > 32 {
		t.Errorf("Topic %q длиннее 32 знаков", pushTopic(a))
	}
	// без ключа — по номеру записи, а не один общий на все
	if pushTopic(&store.Notification{ID: 5}) == pushTopic(&store.Notification{ID: 6}) {
		t.Error("записи без ключа слиплись в один Topic")
	}
	// сам ключ в Topic не просвечивает: он может нести номер письма
	if strings.Contains(pushTopic(a), "диск") || strings.Contains(pushTag(a), "диск") {
		t.Error("ключ записи виден в Topic или теге")
	}
}

// Адрес для связи в подписи: «mailto:» не удваивается (так библиотека
// портила прежнее значение по умолчанию), https остаётся как есть, а
// негодное заменяется запасным с пометкой «ненастоящий».
func TestPushSubjectForms(t *testing.T) {
	cases := []struct {
		name, setting, origin string
		want                  string
		real                  bool
	}{
		{"почта без схемы", "ivan@example.com", "", "mailto:ivan@example.com", true},
		{"почта со схемой", "mailto:ivan@example.com", "", "mailto:ivan@example.com", true},
		{"адрес https", "https://sema.example.org", "", "https://sema.example.org", true},
		{"из адреса входа", "", "https://sema.example.org", "https://sema.example.org", true},
		{"настройка главнее адреса входа", "ivan@example.com", "https://sema.example.org", "mailto:ivan@example.com", true},
		{"ничего не задано", "", "", pushFallbackSubject, false},
		{"localhost не годится", "mailto:sema@localhost", "", pushFallbackSubject, false},
		{"имя из своей сети", "https://sema.local", "", pushFallbackSubject, false},
		{"мусор", "не адрес", "", pushFallbackSubject, false},
		{"адрес входа из своей сети", "", "https://sema.local:8443", pushFallbackSubject, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, st := newTestEngine(t, "тест", nil)
			f := newFakePushService(t)
			f.device(t, e, "/push/a")
			_ = st.SetSetting("push.subject", c.setting)
			_ = st.SetSetting("auth.origin", c.origin)
			got, real := e.PushSubject()
			if got != c.want || real != c.real {
				t.Fatalf("PushSubject() = %q, %v; ждали %q, %v", got, real, c.want, c.real)
			}
			// и то, что видит служба: ровно этот sub, без удвоенного «mailto:»
			n := putNote(t, st, &store.Notification{Key: "k", Level: "warn", Title: "Проба"})
			e.deliverWeb(t.Context(), n, pushTestChannel())
			pub, _, _ := e.VAPIDKeys()
			checkVAPID(t, f.records("/push/a")[0].Header.Get("Authorization"), f.srv.URL, c.want, pub)
		})
	}
}

// Подпись не обновляется чаще раза в час (Apple: «Don't refresh your JWT
// more frequently than once per hour»), а по истечении часа — свежая.
func TestPushAuthorizationReused(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	f.device(t, e, "/push/a")
	f.device(t, e, "/push/b")
	now := time.Now()
	was := pushNow
	pushNow = func() time.Time { return now }
	t.Cleanup(func() { pushNow = was })

	send := func(key string) {
		n := putNote(t, st, &store.Notification{Key: key, Level: "warn", Title: "Проба"})
		e.deliverWeb(t.Context(), n, pushTestChannel())
	}
	send("one")
	send("two")
	a, b := f.records("/push/a"), f.records("/push/b")
	first := a[0].Header.Get("Authorization")
	for _, r := range []pushRecord{a[1], b[0], b[1]} {
		if r.Header.Get("Authorization") != first {
			t.Fatal("подпись обновилась раньше, чем через час")
		}
	}
	now = now.Add(61 * time.Minute)
	send("three")
	if f.records("/push/a")[2].Header.Get("Authorization") == first {
		t.Error("через час подпись всё ещё прежняя")
	}
}

// Ответ службы человеческими словами; 404 и 410 убирают устройство, а не
// копят отказы.
func TestPushServiceAnswers(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	bad := f.device(t, e, "/push/jwt")
	gone := f.device(t, e, "/push/gone")
	notfound := f.device(t, e, "/push/missing")
	mismatch := f.device(t, e, "/push/mismatch")
	busy := f.device(t, e, "/push/busy")
	down := f.device(t, e, "/push/down")
	f.answer("/push/jwt", pushReply{Status: 403, Body: `{"reason":"BadJwtToken"}`})
	f.answer("/push/gone", pushReply{Status: 410, Body: `{"reason":"Unsubscribed"}`})
	f.answer("/push/missing", pushReply{Status: 404})
	f.answer("/push/mismatch", pushReply{Status: 400, Body: `{"reason":"VapidPkHashMismatch"}`})
	f.answer("/push/busy", pushReply{Status: 429, Header: map[string]string{"Retry-After": "120"}})
	f.answer("/push/down", pushReply{Status: 503})

	n := putNote(t, st, &store.Notification{Key: "k", Level: "warn", Title: "Проба"})
	e.deliverWeb(t.Context(), n, pushTestChannel())

	// 403 BadJwtToken: устройство остаётся, а строка называет причину и что делать
	d, _ := st.PushSubscriptionByID(bad.Sub.ID)
	if d.LastStatus != 403 || !strings.Contains(d.LastErr, "BadJwtToken") || !strings.Contains(d.LastErr, "для связи") {
		t.Errorf("403 BadJwtToken объяснён плохо: %+v", d)
	}
	// 404 и 410: подписки больше нет — запись убрана, человеку сказано
	for _, g := range []*pushDevice{gone, notfound} {
		if _, ok := st.PushSubscriptionByID(g.Sub.ID); ok {
			t.Errorf("устройство %s осталось после ответа «подписки нет»", g.Sub.Endpoint)
		}
		if _, ok := st.OpenNotificationByKey(fmt.Sprintf("push:gone:%d", g.Sub.ID)); !ok {
			t.Errorf("об ушедшем устройстве %d человеку не сказано", g.Sub.ID)
		}
	}
	// другой ключ: понятно, что делать, и есть запись «Переподпишите»
	d, _ = st.PushSubscriptionByID(mismatch.Sub.ID)
	if !strings.Contains(d.LastErr, "другим ключом") || d.LastStatus != 400 {
		t.Errorf("VapidPkHashMismatch: %+v", d)
	}
	if _, ok := st.OpenNotificationByKey(fmt.Sprintf("push:resub:%d", mismatch.Sub.ID)); !ok {
		t.Error("про смену ключа человеку не сказано")
	}
	d, _ = st.PushSubscriptionByID(busy.Sub.ID)
	if !strings.Contains(d.LastErr, "429") || !strings.Contains(d.LastErr, "120") {
		t.Errorf("429 без срока повтора: %q", d.LastErr)
	}
	d, _ = st.PushSubscriptionByID(down.Sub.ID)
	if !strings.Contains(d.LastErr, "недоступна") || d.ErrAt == "" {
		t.Errorf("5xx: %+v", d)
	}
	// из шести устройств доставлено ни одно, и запись осталась недоставленной
	if got, _ := st.Notification(n.ID); !got.Undelivered() {
		t.Error("запись отмечена доставленной, хотя никто не принял")
	}
}

// «Проверить» на устройстве шлёт только на него, а ответ службы приходит
// словами; не принявшее — ошибка действия.
func TestPushTestOnlyThatDevice(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	a := f.device(t, e, "/push/a")
	b := f.device(t, e, "/push/b")

	line, err := e.TestPushDevice(b.Sub.ID)
	if err != nil {
		t.Fatalf("проба принятая, а ошибка: %v", err)
	}
	if !strings.Contains(line, "принято, ответ") || !strings.Contains(line, "201") {
		t.Errorf("строка ответа: %q", line)
	}
	if len(f.records("/push/a")) != 0 {
		t.Error("проба для одного устройства ушла и на другое")
	}
	rs := f.records("/push/b")
	if len(rs) != 1 {
		t.Fatalf("на устройство ушло %d проб, ждали 1", len(rs))
	}
	p := b.payload(t, rs[0])
	if !strings.Contains(p.Title, "Проба") || p.URL != "/#screen=settings:notify" {
		t.Errorf("проба: %+v", p)
	}
	// Topic пробы — настоящий base64, а не слово: Apple его раскодирует и
	// на «sema-test» отвечала 400 BadWebPushTopic
	if topic := rs[0].Header.Get("Topic"); topic != pushTopicOf(pushTestTopic) || !validPushTopic(topic) {
		t.Errorf("Topic пробы = %q", topic)
	}

	f.answer("/push/a", pushReply{Status: 403, Body: `{"reason":"BadJwtToken"}`})
	line, err = e.TestPushDevice(a.Sub.ID)
	if err == nil || !strings.Contains(line, "BadJwtToken") {
		t.Errorf("отказ службы должен быть ошибкой с причиной: %q %v", line, err)
	}
	if d, _ := st.PushSubscriptionByID(a.Sub.ID); d.LastErr == "" {
		t.Error("отказ не записан в строку устройства")
	}
	if _, err := e.TestPushDevice(9999); err == nil {
		t.Error("проба несуществующему устройству прошла")
	}
}

// Порог и тихие часы у устройства свои: телефону ночью тишина, Mac — как был.
// Что не ушло из-за тихих часов, ждёт их конца и уходит одним пушем.
func TestPushDevicePolicy(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	// повтор после тихих часов берёт канал из базы, а не из вызова
	if err := st.SaveNotifyChannel(pushTestChannel()); err != nil {
		t.Fatal(err)
	}
	mac := f.device(t, e, "/push/mac")     // как у канала: от «внимание», без тихих часов
	phone := f.device(t, e, "/push/phone") // свои тихие часы
	low := f.device(t, e, "/push/low")     // свой порог «инфо»
	now := time.Now()
	quiet := fmt.Sprintf("%s-%s", now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04"))
	if err := st.SetPushQuiet(phone.Sub.ID, quiet); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPushLevel(low.Sub.ID, "info"); err != nil {
		t.Fatal(err)
	}

	warn := putNote(t, st, &store.Notification{Key: "w", Level: "warn", Title: "Внимание"})
	info := putNote(t, st, &store.Notification{Key: "i", Level: "info", Title: "К сведению"})
	e.deliverWeb(t.Context(), warn, pushTestChannel())
	e.deliverWeb(t.Context(), info, pushTestChannel())

	if n := len(f.records("/push/mac")); n != 1 {
		t.Errorf("Mac получил %d, ждали 1 («внимание», а «инфо» ниже порога)", n)
	}
	if n := len(f.records("/push/low")); n != 2 {
		t.Errorf("устройство с порогом «инфо» получило %d, ждали 2", n)
	}
	if n := len(f.records("/push/phone")); n != 0 {
		t.Errorf("телефон получил %d в тихие часы", n)
	}
	// «инфо» телефону в тихие часы не откладывается: порог его и так не пускал
	if pending := st.DeferredPush(phone.Sub.ID); len(pending) != 1 || pending[0] != warn.ID {
		t.Errorf("телефону отложено %v, ждали одну запись «внимание»", pending)
	}

	// тихие часы кончились — одна запись уходит как есть
	if err := st.SetPushQuiet(phone.Sub.ID, "-"); err != nil {
		t.Fatal(err)
	}
	if sent := e.resendPushPending(time.Now()); sent != 1 {
		t.Errorf("повтор после тихих часов отправил %d, ждали 1", sent)
	}
	rs := f.records("/push/phone")
	if len(rs) != 1 || phone.payload(t, rs[0]).Title != "Внимание" {
		t.Fatalf("после тихих часов: %d пушей", len(rs))
	}
	if len(st.DeferredPush(phone.Sub.ID)) != 0 {
		t.Error("ожидание не снято после отправки")
	}

	// несколько за ночь — один общий пуш без содержимого
	if err := st.SetPushQuiet(phone.Sub.ID, quiet); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"x", "y", "z"} {
		e.deliverWeb(t.Context(), putNote(t, st, &store.Notification{Key: k, Level: "warn", Title: "Секретное дело " + k}), pushTestChannel())
	}
	if err := st.SetPushQuiet(phone.Sub.ID, ""); err != nil { // как у канала: тихих часов нет
		t.Fatal(err)
	}
	if sent := e.resendPushPending(time.Now()); sent != 3 {
		t.Errorf("повтор отправил %d, ждали 3", sent)
	}
	rs = f.records("/push/phone")
	if len(rs) != 2 {
		t.Fatalf("устройство получило %d пушей, ждали 2 (один и один общий)", len(rs))
	}
	sum := phone.payload(t, rs[1])
	if !strings.Contains(sum.Title, "есть новое — 3") || sum.Body != "" || strings.Contains(sum.Title, "Секретное") {
		t.Errorf("общий пуш: %+v", sum)
	}

	// разобранное за ночь будить не нужно
	if err := st.SetPushQuiet(phone.Sub.ID, quiet); err != nil {
		t.Fatal(err)
	}
	done := putNote(t, st, &store.Notification{Key: "d", Level: "warn", Title: "Сделанное"})
	e.deliverWeb(t.Context(), done, pushTestChannel())
	_ = st.SetNotificationState(done.ID, store.NotifyDone, "тест")
	_ = st.SetPushQuiet(phone.Sub.ID, "-")
	if sent := e.resendPushPending(time.Now()); sent != 0 {
		t.Errorf("повтор отправил %d про уже закрытую запись", sent)
	}
	if len(f.records("/push/phone")) != 2 {
		t.Error("закрытая запись разбудила телефон")
	}
	_ = mac
}

// Состав не ослаблен новыми полями: закрытый ящик и секретное — «есть новое»
// везде, где бывает содержимое: заголовок, тело, тег, Topic, адрес.
func TestPushMuteEverywhere(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	dev := f.device(t, e, "/push/a")

	if err := st.SaveMailConf("work@example.com", &store.MailConf{Purpose: store.PurposeWork}); err != nil {
		t.Fatal(err)
	}
	closed := &store.Message{MessageID: "<z@y>", Account: "work@example.com", FromAddr: "boss@example.com",
		Subject: "Договор № 7", Snippet: "во вложении"}
	open := &store.Message{MessageID: "<o@y>", Account: "home@example.com", FromAddr: "friend@example.com",
		Subject: "скан паспорта для визы", Snippet: "жена просила"}
	plain := &store.Message{MessageID: "<p@y>", Account: "home@example.com", FromAddr: "shop@example.com",
		Subject: "Ваш заказ собран", Snippet: "курьер завтра"}
	for _, m := range []*store.Message{closed, open, plain} {
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	mk := func(m *store.Message, key string) *store.Notification {
		return putNote(t, st, &store.Notification{Key: key, Level: "alert", Title: "Ответить: " + m.Subject,
			Summary: m.Snippet + " от " + m.FromAddr, Ref: "msg:" + itoa(int(m.ID)),
			Fields: []store.NotifyField{{Label: "От кого", Value: m.FromAddr}}})
	}
	for _, m := range []*store.Message{closed, open} {
		n := mk(m, "mail:"+m.MessageID)
		e.deliverWeb(t.Context(), n, pushTestChannel())
		r := f.records("/push/a")[len(f.records("/push/a"))-1]
		p := dev.payload(t, r)
		if p.Title != "Сёма: есть новое" || p.Body != "" {
			t.Errorf("%s: содержимое в пуше: %+v", m.Account, p)
		}
		// и нигде в запросе — ни в заголовках, ни в расшифрованном теле
		whole := string(dev.decrypt(t, r.Body)) + fmt.Sprint(r.Header)
		for _, secret := range []string{"паспорт", "Договор", "boss@", "friend@", "жена", "во вложении", n.Key} {
			if strings.Contains(whole, secret) {
				t.Errorf("%s: в пуше просвечивает %q", m.Account, secret)
			}
		}
	}
	// обычное письмо открытого ящика показывается как было
	e.deliverWeb(t.Context(), mk(plain, "mail:plain"), pushTestChannel())
	rs := f.records("/push/a")
	p := dev.payload(t, rs[len(rs)-1])
	if p.Title != "Ответить: Ваш заказ собран" || p.Body == "" {
		t.Errorf("обычное письмо урезано зря: %+v", p)
	}
	// общий пуш после тихих часов тоже без содержимого — проверено выше
	// (TestPushDevicePolicy): в нём только счёт
}

// Нечитаемый ключ пушей не заводится заново и не уходит в службу; подписка
// под другим ключом — «переподпишите», а не молчание.
func TestPushResubscribeOnKeyChange(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	dev := f.device(t, e, "/push/a")
	if err := st.SavePushSubscription(&store.PushSubscription{Endpoint: dev.Sub.Endpoint, P256dh: dev.Sub.P256dh,
		Auth: dev.Sub.Auth, VAPIDKey: "старый-ключ", Agent: "Проба"}); err != nil {
		t.Fatal(err)
	}
	n := putNote(t, st, &store.Notification{Key: "k", Level: "warn", Title: "Проба"})
	e.deliverWeb(t.Context(), n, pushTestChannel())
	if len(f.records("/push/a")) != 0 {
		t.Error("в службу ушёл пуш по подписке под другим ключом")
	}
	d, _ := st.PushSubscriptionByID(dev.Sub.ID)
	if !strings.Contains(d.LastErr, "переподпишите") {
		t.Errorf("строка устройства: %q", d.LastErr)
	}
	if _, ok := st.OpenNotificationByKey(fmt.Sprintf("push:resub:%d", dev.Sub.ID)); !ok {
		t.Error("человеку не сказано переподписаться")
	}
	known, id, _, same := e.PushDeviceStatus(dev.Sub.Endpoint)
	if !known || id != dev.Sub.ID || same {
		t.Errorf("статус: known=%v id=%d same=%v, ждали подписку под чужим ключом", known, id, same)
	}
	if k, _, id2, _ := e.PushDeviceStatus("https://нет.example/x"); k || id2 != "" {
		t.Error("незнакомый адрес оказался известным")
	}
}

// Устройства без канала и с выключенным каналом пуши не получают, а канал
// «все источники / источники из списка» — общий.
func TestPushChannelGates(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	f.device(t, e, "/push/a")

	off := pushTestChannel()
	off.Enabled = false
	n := putNote(t, st, &store.Notification{Key: "a", Level: "warn", Title: "Проба"})
	e.deliverWeb(t.Context(), n, off)

	onlyMail := pushTestChannel()
	onlyMail.Sources = "mail"
	n2 := putNote(t, st, &store.Notification{Key: "b", Level: "warn", Title: "Сервер", Source: "core"})
	e.deliverWeb(t.Context(), n2, onlyMail)
	if len(f.records("/push/a")) != 0 {
		t.Error("пуш ушёл мимо выключенного канала или источника не из списка")
	}
	e.deliverWeb(t.Context(), n2, pushTestChannel())
	if len(f.records("/push/a")) != 1 {
		t.Error("пуш не ушёл по включённому каналу")
	}
}

// Подписка с телефона: имя берётся из строки браузера и режима окна.
func TestPushSubscribeNamesDevice(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"
	sub, err := e.PushSubscribe("https://web.push.apple.com/QAbc", "p", "a", iphone, true, 5)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ID == 0 || sub.Agent != "iPhone · Сёма с экрана «Домой»" {
		t.Errorf("подписка: %+v", sub)
	}
	// имя от человека главнее и переживает повторную подписку
	if err := st.SetPushTitle(sub.ID, "Телефон Вадима"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPushLevel(sub.ID, "alert"); err != nil {
		t.Fatal(err)
	}
	again, err := e.PushSubscribe("https://web.push.apple.com/QAbc", "p2", "a2", iphone, true, 5)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != sub.ID {
		t.Errorf("повторная подписка завела двойника: %d и %d", sub.ID, again.ID)
	}
	got, _ := st.PushSubscriptionByID(sub.ID)
	if PushDeviceLabel(got) != "Телефон Вадима" || got.MinLevel != "alert" {
		t.Errorf("своё имя или порог затёрты повторной подпиской: %+v", got)
	}
	// не https и служебные адреса не принимаются
	for _, bad := range []string{"http://fcm.googleapis.com/x", "https://127.0.0.1/x", "https://localhost/x", "https://nas.local/x"} {
		if _, err := e.PushSubscribe(bad, "p", "a", iphone, true, 5); err == nil {
			t.Errorf("адрес %q принят", bad)
		}
	}
}

// Устройство называется по строке браузера и режиму окна.
func TestPushDeviceName(t *testing.T) {
	const (
		iphoneSafari = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
		iphoneHome   = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"
		ipadDesktop  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"
		androidChrom = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
		androidFox   = "Mozilla/5.0 (Android 14; Mobile; rv:127.0) Gecko/127.0 Firefox/127.0"
		androidSams  = "Mozilla/5.0 (Linux; Android 14; SM-S911B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36"
		macChrome    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
		macSafari    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"
		macEdge      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0"
		macFirefox   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:127.0) Gecko/20100101 Firefox/127.0"
		winChrome    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
		linuxFox     = "Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0"
	)
	cases := []struct {
		name       string
		ua         string
		standalone bool
		touch      int
		endpoint   string
		want       string
	}{
		{"iPhone с экрана «Домой»", iphoneHome, true, 5, "https://web.push.apple.com/x", "iPhone · Сёма с экрана «Домой»"},
		{"iPhone в Safari", iphoneSafari, false, 5, "", "iPhone · Safari"},
		{"iPad в режиме «как компьютер»", ipadDesktop, true, 5, "", "iPad · Сёма с экрана «Домой»"},
		{"iPad Safari вкладкой", ipadDesktop, false, 5, "", "iPad · Safari"},
		{"Chrome на Android", androidChrom, false, 5, "https://fcm.googleapis.com/fcm/send/x", "Chrome на Android"},
		{"Chrome на Android, установленная", androidChrom, true, 5, "", "Chrome на Android · приложение"},
		{"Firefox на Android", androidFox, false, 5, "", "Firefox на Android"},
		{"Samsung Internet", androidSams, false, 5, "", "Samsung Internet на Android"},
		{"Chrome на маке", macChrome, false, 0, "https://fcm.googleapis.com/fcm/send/x", "Chrome на маке"},
		{"Safari на маке", macSafari, false, 0, "https://web.push.apple.com/x", "Safari на маке"},
		{"Edge на маке", macEdge, false, 0, "", "Edge на маке"},
		{"Firefox на маке", macFirefox, false, 0, "", "Firefox на маке"},
		{"Chrome на Windows", winChrome, false, 0, "", "Chrome на Windows"},
		{"Firefox на Linux", linuxFox, false, 0, "", "Firefox на Linux"},
		// подписка, заведённая до имён: в строке только кусок про систему
		{"старая запись, Google", "Macintosh; Intel Mac OS X 10_15_7", false, 0, "https://fcm.googleapis.com/fcm/send/x", "Chrome на маке"},
		{"совсем без сведений, Apple", "", false, 0, "https://web.push.apple.com/x", "Safari"},
		{"совсем без сведений", "", false, 0, "", "Браузер"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PushDeviceName(c.ua, c.standalone, c.touch, c.endpoint); got != c.want {
				t.Errorf("имя %q, ждали %q", got, c.want)
			}
		})
	}
	// готовое имя не пересчитывается, старый кусок — выводится заново
	old := &store.PushSubscription{Agent: "Macintosh; Intel Mac OS X 10_15_7", Endpoint: "https://fcm.googleapis.com/fcm/send/x"}
	if got := PushDeviceLabel(old); got != "Chrome на маке" {
		t.Errorf("старая запись: %q", got)
	}
	if got := PushDeviceLabel(&store.PushSubscription{Agent: "iPhone · Safari", Title: "Мой"}); got != "Мой" {
		t.Errorf("имя человека главнее: %q", got)
	}
	if got := PushDeviceLabel(&store.PushSubscription{Agent: "iPhone · Safari"}); got != "iPhone · Safari" {
		t.Errorf("готовое имя изменено: %q", got)
	}
	for in, want := range map[string]string{
		"https://web.push.apple.com/x":                         "Apple",
		"https://fcm.googleapis.com/fcm/x":                     "Google",
		"https://updates.push.services.mozilla.com/wpush/v2/x": "Mozilla",
		"https://wns2-par02p.notify.windows.com/x":             "Microsoft",
		"https://push.example.net/x":                           "push.example.net",
	} {
		if got := PushServiceName(in); got != want {
			t.Errorf("служба %q названа %q, ждали %q", in, got, want)
		}
	}
}

// Адрес для связи принимается только настоящий.
func TestNormalizePushSubject(t *testing.T) {
	for in, want := range map[string]string{
		"ivan@example.com":          "mailto:ivan@example.com",
		"MAILTO:ivan@example.com":   "mailto:ivan@example.com",
		"https://sema.example.org/": "https://sema.example.org",
		" mailto:a@b.ru ":           "mailto:a@b.ru",
	} {
		if got, ok := NormalizePushSubject(in); !ok || got != want {
			t.Errorf("%q → %q, %v; ждали %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "sema", "a@localhost", "https://localhost", "https://sema.local", "http://sema.example.org",
		"mailto:", "a b@example.com", "https://user:pw@sema.example.org", "@example.com"} {
		if got, ok := NormalizePushSubject(in); ok {
			t.Errorf("%q принят как %q", in, got)
		}
	}
}

// После тихих часов устройство получает отложенное один раз. Раньше
// отправленное не помечалось доставленным каналу, и следом в том же проходе
// повтор недоставленного слал запись второй раз, а вместе с общим пушем
// «есть новое — N» ещё и N одиночных — ровно то, от чего общий пуш избавляет.
func TestPushNoDoublesAfterQuiet(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(fmt.Sprintf("записей %d", count), func(t *testing.T) {
			e, st := newTestEngine(t, "тест", nil)
			f := newFakePushService(t)
			// канала mac нет: недоставленное никто, кроме web, не заберёт
			if err := st.SaveNotifyChannel(pushTestChannel()); err != nil {
				t.Fatal(err)
			}
			phone := f.device(t, e, "/push/phone")
			now := time.Now()
			quiet := fmt.Sprintf("%s-%s", now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04"))
			if err := st.SetPushQuiet(phone.Sub.ID, quiet); err != nil {
				t.Fatal(err)
			}
			var notes []*store.Notification
			for i := 0; i < count; i++ {
				n := putNote(t, st, &store.Notification{Key: fmt.Sprintf("k%d", i), Level: "warn", Title: fmt.Sprintf("Дело %d", i)})
				notes = append(notes, n)
				e.deliverWeb(t.Context(), n, pushTestChannel())
			}
			if got := len(f.records("/push/phone")); got != 0 {
				t.Fatalf("в тихие часы ушло %d пушей", got)
			}
			// тихие часы кончились, идёт тот самый час «Сроки»
			if err := st.SetPushQuiet(phone.Sub.ID, "-"); err != nil {
				t.Fatal(err)
			}
			settle := func() { time.Sleep(400 * time.Millisecond) } // повтор недоставленного шлёт в своей нитке
			e.resendUndelivered(time.Now())
			settle()
			got := f.records("/push/phone")
			if len(got) != 1 {
				t.Fatalf("после тихих часов ушло %d пушей, ждали ровно один", len(got))
			}
			p := phone.payload(t, got[0])
			if count == 1 && p.Title != "Дело 0" {
				t.Errorf("одна запись должна уйти как есть: %+v", p)
			}
			if count == 3 && (!strings.Contains(p.Title, "есть новое — 3") || p.Body != "") {
				t.Errorf("несколько записей — один общий пуш без содержимого: %+v", p)
			}
			for _, n := range notes {
				if fresh, _ := st.Notification(n.ID); fresh.Undelivered() {
					t.Errorf("запись %d осталась недоставленной после отправки: повтор пошлёт её второй раз", n.ID)
				}
			}
			if left, _ := st.UndeliveredOpen(time.Now().Add(-24*time.Hour).Format(time.RFC3339), "info", 50); len(left) != 0 {
				t.Errorf("повтор недоставленного всё ещё видит %d записей", len(left))
			}
			// и следующий час не шлёт ничего
			e.resendUndelivered(time.Now())
			settle()
			if got := len(f.records("/push/phone")); got != 1 {
				t.Errorf("повторный проход прислал ещё %d пушей", got-1)
			}
		})
	}
}

// Запись отложена одному устройству (тихие часы), а другому ушла сразу: после
// тихих часов первое получает своё, второе — ничего нового.
func TestPushDeferredOneDeviceSentToOther(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	if err := st.SaveNotifyChannel(pushTestChannel()); err != nil {
		t.Fatal(err)
	}
	f.device(t, e, "/push/mac")
	phone := f.device(t, e, "/push/phone")
	now := time.Now()
	quiet := fmt.Sprintf("%s-%s", now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04"))
	if err := st.SetPushQuiet(phone.Sub.ID, quiet); err != nil {
		t.Fatal(err)
	}
	n := putNote(t, st, &store.Notification{Key: "k", Level: "warn", Title: "Общее дело"})
	e.deliverWeb(t.Context(), n, pushTestChannel())
	if len(f.records("/push/mac")) != 1 || len(f.records("/push/phone")) != 0 {
		t.Fatalf("сначала: Mac %d, телефон %d, ждали 1 и 0", len(f.records("/push/mac")), len(f.records("/push/phone")))
	}
	if err := st.SetPushQuiet(phone.Sub.ID, "-"); err != nil {
		t.Fatal(err)
	}
	e.resendUndelivered(time.Now())
	time.Sleep(400 * time.Millisecond)
	e.resendUndelivered(time.Now())
	time.Sleep(400 * time.Millisecond)
	if got := len(f.records("/push/phone")); got != 1 {
		t.Errorf("телефон получил %d, ждали ровно один", got)
	}
	if got := len(f.records("/push/mac")); got != 1 {
		t.Errorf("Mac получил %d, ждали один (тот, что ушёл сразу)", got)
	}
}

// Отложенное не уходит дважды, если устройство получило его другим путём:
// ушло сразу, пока шёл повтор, — ожидание снимается.
func TestPushSentNowClearsDeferred(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	phone := f.device(t, e, "/push/phone")
	n := putNote(t, st, &store.Notification{Key: "k", Level: "warn", Title: "Дело"})
	st.DeferPush(phone.Sub.ID, n.ID)
	e.deliverWeb(t.Context(), n, pushTestChannel())
	if len(st.DeferredPush(phone.Sub.ID)) != 0 {
		t.Error("ушедшее сейчас осталось в ожидании: следующий час послал бы его второй раз")
	}
}

// «Тревога висит, пока не закроют» — настройка, по умолчанию выключена. Сервер
// кладёт в пуш `sticky` (истина только у тревоги и только когда включено), а
// работник по уровню сам ничего не решает. Общий пуш после тихих часов и проба
// никогда не висят.
func TestPushStickyAlertSetting(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	f := newFakePushService(t)
	dev := f.device(t, e, "/push/a")
	if err := st.SaveNotifyChannel(pushTestChannel()); err != nil {
		t.Fatal(err)
	}
	last := func() (pushPayload, string) {
		t.Helper()
		rs := f.records("/push/a")
		r := rs[len(rs)-1]
		return dev.payload(t, r), string(dev.decrypt(t, r.Body))
	}
	send := func(key, level string) {
		n := putNote(t, st, &store.Notification{Key: key, Level: level, Title: "Дело " + key})
		e.deliverWeb(t.Context(), n, pushTestChannel())
	}

	// по умолчанию тревога не липнет, и в теле поля нет вовсе
	send("a1", "alert")
	if p, raw := last(); p.Sticky || strings.Contains(raw, "sticky") {
		t.Errorf("по умолчанию тревога липнет: %s", raw)
	}
	if e.PushStickyAlert() {
		t.Error("по умолчанию настройка включена")
	}

	// включили: липнет только тревога
	_ = st.SetSetting("push.sticky_alert", "1")
	send("a2", "alert")
	if p, _ := last(); !p.Sticky || p.Level != "alert" {
		t.Errorf("тревога при включённой настройке не липнет: %+v", p)
	}
	send("w1", "warn")
	if p, raw := last(); p.Sticky || strings.Contains(raw, "sticky") {
		t.Errorf("«внимание» липнет: %s", raw)
	}
	_ = st.SetSetting("push.sticky_alert", "true")
	send("a3", "alert")
	if p, _ := last(); !p.Sticky {
		t.Error("значение «true» не принято")
	}
	_ = st.SetSetting("push.sticky_alert", "мусор")
	send("a4", "alert")
	if p, _ := last(); p.Sticky {
		t.Error("непонятное значение включило липкость")
	}

	// настройка включена, а проба и общий пуш всё равно не липнут
	_ = st.SetSetting("push.sticky_alert", "1")
	if _, err := e.TestPushDevice(dev.Sub.ID); err != nil {
		t.Fatal(err)
	}
	if p, raw := last(); p.Sticky || strings.Contains(raw, "sticky") {
		t.Errorf("проба липнет: %s", raw)
	}
	now := time.Now()
	quiet := fmt.Sprintf("%s-%s", now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04"))
	if err := st.SetPushQuiet(dev.Sub.ID, quiet); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"s1", "s2"} {
		send(k, "alert") // тревоги, отложенные тихими часами
	}
	if err := st.SetPushQuiet(dev.Sub.ID, "-"); err != nil {
		t.Fatal(err)
	}
	before := len(f.records("/push/a"))
	e.resendPushPending(time.Now())
	if len(f.records("/push/a")) != before+1 {
		t.Fatalf("общий пуш не ушёл")
	}
	if p, raw := last(); !strings.Contains(p.Title, "есть новое — 2") || p.Sticky || strings.Contains(raw, "sticky") {
		t.Errorf("общий пуш после тихих часов липнет или не общий: %s", raw)
	}
}

// validPushTopic — то, что проверяет служба Apple: не длиннее 32 знаков и
// раскодируется как url-безопасный base64 без дополнения.
func validPushTopic(topic string) bool {
	if topic == "" || len(topic) > 32 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(topic)
	return err == nil
}

// Topic у любого пуша — раскодируемый base64. 1 октября 2026 проба на iPhone
// получила от Apple 400 BadWebPushTopic: «sema-test» состоит из годных букв,
// но девять знаков не бывают base64. Google тот же заголовок принимал, и на
// маке беды не было видно.
func TestPushTopicsAreRealBase64(t *testing.T) {
	if validPushTopic("sema-test") {
		t.Fatal("проверка не ловит «sema-test» — она ничего не проверяет")
	}
	for name, topic := range map[string]string{
		"проба":            pushTopic(&store.Notification{}),
		"запись с ключом":  pushTopic(&store.Notification{ID: 7, Key: "server:диск"}),
		"запись без ключа": pushTopic(&store.Notification{ID: 7}),
		"сводка":           pushTopicOf(pushSummaryTopic),
	} {
		if !validPushTopic(topic) {
			t.Errorf("%s: Topic %q служба Apple не примет", name, topic)
		}
	}
	if pushTopic(&store.Notification{}) == pushTopicOf(pushSummaryTopic) {
		t.Error("проба и сводка делят один Topic: одно вытеснит другое у службы")
	}
}
