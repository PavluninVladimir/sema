package api

// Вход ключом поверх урезанной сессии (1 октября 2026).
//
// Живой случай: iPhone, сессия контрольной фразой снаружи («только завести
// ключ»), а ключ Сёмы у владельца уже есть — синхронизированный, приехал
// из связки на устройство сам. Страница показывала одно «Завести passkey»,
// хранитель отвечал «такой уже есть» (InvalidStateError), и войти было
// нечем: кнопки входа под урезанной сессией не было.
//
// Здесь — серверная половина: вход ключом, начатый с урезанной cookie,
// даёт новую полную сессию, урезанная умирает, а неудавшийся вход ничего не
// меняет. Хранитель ключа — программный: настоящая подпись по настоящим
// параметрам церемонии, тот же путь `/api/auth/login/*`, что у браузера.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// softKey — ключ устройства без устройства: пара ES256 и идентификатор.
type softKey struct {
	priv *ecdsa.PrivateKey
	id   []byte
}

func newSoftKey(t *testing.T) *softKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{priv: priv, id: id}
}

// credentialJSON — запись ключа в таком виде, в каком её кладёт в базу
// заведение: синхронизируемый ключ (связка, менеджер паролей), проверка
// владельца пройдена.
func (k *softKey) credentialJSON(t *testing.T) string {
	t.Helper()
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: 2, Algorithm: int64(webauthncose.AlgES256)},
		Curve:         1,
		XCoord:        k.priv.PublicKey.X.FillBytes(make([]byte, 32)),
		YCoord:        k.priv.PublicKey.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(webauthn.Credential{ID: k.id, PublicKey: cose, Flags: webauthn.CredentialFlags{
		UserPresent: true, UserVerified: true, BackupEligible: true, BackupState: true}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// assertion — тело запроса `login/finish`: ответ хранителя на вызов.
// wrongSig портит подпись: так выглядит чужой, не тот ключ.
func (k *softKey) assertion(t *testing.T, rpID, origin, challenge string, wrongSig bool) string {
	t.Helper()
	b64 := base64.RawURLEncoding.EncodeToString
	clientData, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": challenge, "origin": origin, "crossOrigin": false})
	rp := sha256.Sum256([]byte(rpID))
	// UP | UV | BE | BS — как у синхронизируемого ключа с проверкой владельца
	authData := append(append([]byte{}, rp[:]...), 0x01|0x04|0x08|0x10)
	authData = binary.BigEndian.AppendUint32(authData, 1)
	cd := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cd[:]...))
	if wrongSig {
		digest[0] ^= 0xff
	}
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"id": b64(k.id), "rawId": b64(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64(clientData), "authenticatorData": b64(authData), "signature": b64(sig),
		},
	})
	return string(body)
}

// ceremonyRequest — POST из страницы входа: с cookie (может быть пустой) и
// с адреса снаружи, как у iPhone за шлюзом.
func ceremonyRequest(s *Server, path, body, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://localhost:8765"+path, strings.NewReader(body))
	r.RemoteAddr = снаружиАдрес
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.Header.Set("Cookie", "sema_session="+cookie)
	}
	return serve(s, r)
}

// beginLogin — первая половина входа: номер церемонии и вызов хранителю.
func beginLogin(t *testing.T, s *Server, cookie string) (ceremony, challenge string) {
	t.Helper()
	w := ceremonyRequest(s, "/api/auth/login/begin", "", cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("начало входа: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Ceremony string
		Options  struct {
			PublicKey struct{ Challenge string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Ceremony == "" || got.Options.PublicKey.Challenge == "" {
		t.Fatalf("начало входа вернуло не то: %v %s", err, w.Body.String())
	}
	return got.Ceremony, got.Options.PublicKey.Challenge
}

// sessionCookie — токен новой сессии из ответа входа.
func sessionCookie(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == "sema_session" && c.MaxAge >= 0 {
			return c.Value
		}
	}
	return ""
}

// Урезанная сессия начала вход ключом и закончила его: cookie заменена
// полной сессией, урезанная строка из базы ушла, а у ключа проставлен
// последний вход. Экран новой сессии открывается, старой — нет.
func TestLoginByKeyReplacesRestrictedSession(t *testing.T) {
	s, st := testServer(t)
	key := newSoftKey(t)
	pid, err := st.AddPasskey("MacBookEx", key.credentialJSON(t), s.auth.RPID())
	if err != nil {
		t.Fatal(err)
	}
	old, oldHash := restrictedToken(t, s, st)
	ui := func(token string) int {
		return serve(s, fromBrowser("GET", "/ui?screen=inbox", "", token, true)).Code
	}
	if code := ui(old); code != http.StatusUnauthorized {
		t.Fatalf("урезанная сессия до входа: экран отвечает %d, ждали 401", code)
	}

	// страница входа открыта этой самой урезанной cookie: вход начинается с неё
	ceremony, challenge := beginLogin(t, s, old)
	w := ceremonyRequest(s, "/api/auth/login/finish?ceremony="+ceremony, key.assertion(t, s.auth.RPID(), s.auth.Origin(), challenge, false), old)
	if w.Code != http.StatusOK {
		t.Fatalf("вход ключом поверх урезанной сессии: %d %s", w.Code, w.Body.String())
	}
	fresh := sessionCookie(w)
	if fresh == "" || fresh == old {
		t.Fatalf("вход не выдал новую cookie: %q", fresh)
	}
	se := sessionByHash(t, st, store.HashSecret(fresh))
	if se == nil || se.Restricted || se.PasskeyID != pid {
		t.Fatalf("новая сессия: %+v — ждали полную, открытую ключом %d", se, pid)
	}
	if sessionByHash(t, st, oldHash) != nil {
		t.Error("урезанная сессия осталась в базе: с ней можно было бы заводить ключи и после входа")
	}
	if code := ui(fresh); code != http.StatusOK {
		t.Errorf("экран новой сессии: %d, ждали 200", code)
	}
	if code := ui(old); code != http.StatusUnauthorized {
		t.Errorf("экран прежней cookie: %d, ждали 401", code)
	}
	keys, _ := st.Passkeys()
	if len(keys) != 1 || keys[0].LastUsed.IsZero() {
		t.Errorf("у ключа не проставлен последний вход: %+v", keys)
	}
}

// Неудавшийся вход ничего не повышает и ничего не отнимает: подпись не
// сошлась — отказ, урезанная сессия как была (и осталась урезанной), новой
// cookie нет. Иначе «Войти ключом» на странице урезанной сессии было бы
// дырой, а не дверью.
func TestFailedLoginKeepsRestrictedSession(t *testing.T) {
	s, st := testServer(t)
	key := newSoftKey(t)
	if _, err := st.AddPasskey("MacBookEx", key.credentialJSON(t), s.auth.RPID()); err != nil {
		t.Fatal(err)
	}
	old, oldHash := restrictedToken(t, s, st)

	ceremony, challenge := beginLogin(t, s, old)
	w := ceremonyRequest(s, "/api/auth/login/finish?ceremony="+ceremony, key.assertion(t, s.auth.RPID(), s.auth.Origin(), challenge, true), old)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("чужая подпись: ответ %d, ждали 401", w.Code)
	}
	if c := sessionCookie(w); c != "" {
		t.Fatalf("отказ выдал cookie: %q", c)
	}
	if se := sessionByHash(t, st, oldHash); se == nil || !se.Restricted {
		t.Fatalf("урезанная сессия после отказа: %+v — ждали ту же, урезанную", se)
	}
	if code := serve(s, fromBrowser("GET", "/ui?screen=inbox", "", old, true)).Code; code != http.StatusUnauthorized {
		t.Errorf("экран урезанной сессии после отказа: %d, ждали 401", code)
	}
	// и ключ не отмечен как использованный
	if keys, _ := st.Passkeys(); len(keys) != 1 || !keys[0].LastUsed.IsZero() {
		t.Errorf("отказанный вход отметил ключ: %+v", keys)
	}
	// чужой ключ, которого у Сёмы нет, тоже не входит: церемония принимает
	// только свои идентификаторы
	stranger := newSoftKey(t)
	ceremony, challenge = beginLogin(t, s, old)
	w = ceremonyRequest(s, "/api/auth/login/finish?ceremony="+ceremony, stranger.assertion(t, s.auth.RPID(), s.auth.Origin(), challenge, false), old)
	if w.Code != http.StatusUnauthorized || sessionCookie(w) != "" {
		t.Fatalf("чужой ключ: ответ %d, cookie %q — ждали отказ", w.Code, sessionCookie(w))
	}
}

// Вход ключом поверх полной сессии её не трогает: удаляется только
// урезанная, а полную, с которой открыли страницу «завести ещё ключ», не
// унести заодно.
func TestLoginByKeyKeepsFullSession(t *testing.T) {
	s, st := testServer(t)
	key := newSoftKey(t)
	pid, err := st.AddPasskey("MacBookEx", key.credentialJSON(t), s.auth.RPID())
	if err != nil {
		t.Fatal(err)
	}
	full, err := s.auth.OpenSession(pid, "проба", "203.0.113.10", "вход по passkey")
	if err != nil {
		t.Fatal(err)
	}
	ceremony, challenge := beginLogin(t, s, full)
	w := ceremonyRequest(s, "/api/auth/login/finish?ceremony="+ceremony, key.assertion(t, s.auth.RPID(), s.auth.Origin(), challenge, false), full)
	if w.Code != http.StatusOK {
		t.Fatalf("повторный вход: %d %s", w.Code, w.Body.String())
	}
	if sessionByHash(t, st, store.HashSecret(full)) == nil {
		t.Error("вход ключом унёс полную сессию")
	}
}
