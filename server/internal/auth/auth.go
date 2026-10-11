// Package auth — вход в Сёму: passkey (WebAuthn) для человека, токены для
// программ, сессии. Один пользователь — хозяин; ключей у него может быть
// несколько. Сами церемонии WebAuthn идут в браузере на странице /login,
// а приложение получает сессию по одноразовому коду (device login).
package auth

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	SettingRPID   = "auth.rp_id"
	SettingOrigin = "auth.origin"
	// SettingLocalOpen — с этой машины без проверки личности. У новой
	// установки выключено (умолчание — в реестре, store.AuthLocalOpen);
	// работающему Сёме прежнее «включено» записала разовая миграция
	SettingLocalOpen  = "auth.local_open"
	SettingTrustProxy = "auth.trust_proxy" // верить X-Forwarded-For — только за своим прокси
	// SettingLocalScope — что считать «этой машиной»: loopback или ещё и
	// частная сеть. Дома мак и телефон ходят к Pi по сети, и без «private»
	// при выключенном passkey снаружи не попасть; но частная сеть — это и
	// гость по Wi-Fi, и соседний контейнер, поэтому у новой установки здесь
	// loopback, а private — только выбор человека (store.AuthLocalScope)
	SettingLocalScope = "auth.local_scope" // loopback (по умолчанию) | private
	// SettingUserVerification — требовать ли от хранителя ключа проверку
	// владельца (Touch ID, лицо, код устройства) при входе. По умолчанию
	// «required», и это сознательное сужение доступа против обычного правила
	// проекта: «preferred» на публичном адресе означает «ключ пускает и без
	// человека», а найденный телефон — это и есть чужой человек с ключом.
	// Отпечаток и лицо в браузере спрашивает только хранитель ключа, другой
	// дороги к биометрии нет. Ключ, заведённый хранителем, который проверку
	// не делает вовсе, после этого перестаёт входить — поэтому смена названа
	// в журнале при старте (UserVerificationNotice), а вернуть прежнее можно
	// тумблером на «Доступе»; запасной путь — вход контрольной фразой.
	SettingUserVerification = "auth.user_verification" // preferred | required
	// SettingLocalHosts — имена, под которыми Сёму зовут из своей сети,
	// кроме адреса входа: защита от DNS rebinding пускает только их
	SettingLocalHosts = "auth.local_hosts" // через запятую
	SessionTTL        = 30 * 24 * time.Hour
	SessionPrefix     = "sema_s_"
	TokenPrefix       = "sema_"
)

// Scopes — права токена: экраны, действия, файлы, память (MCP и хуки), всё.
var Scopes = []struct{ ID, Title string }{
	{"ui", "чтение экранов"}, {"actions", "действия"}, {"files", "файлы"}, {"memory", "память (MCP и хуки)"}, {"all", "всё"},
}

// owner — единственный пользователь Сёмы с набором ключей.
type owner struct {
	creds []webauthn.Credential
	ids   []int64
}

func (o *owner) WebAuthnID() []byte                         { return []byte("sema-owner") }
func (o *owner) WebAuthnName() string                       { return "sema" }
func (o *owner) WebAuthnDisplayName() string                { return "Хозяин Сёмы" }
func (o *owner) WebAuthnIcon() string                       { return "" }
func (o *owner) WebAuthnCredentials() []webauthn.Credential { return o.creds }

// Nonce — код приложения, ждущего эту сессию.

type Ceremony struct {
	data    *webauthn.SessionData
	kind    string // register | login
	name    string // имя ключа при регистрации
	expires time.Time
}

// Auth — состояние входа: конфигурация WebAuthn, церемонии, ожидающие сессии.
type Auth struct {
	listen     string // адрес прослушивания: из него выводится адрес входа по умолчанию
	st         *store.Store
	mu         sync.Mutex
	ceremonies map[string]*Ceremony

	// lastPhrase/lastPhraseAt — только что созданная фраза входа: как и
	// свежий токен на «Доступе», видна недолго и не хранится (phrase.go)
	lastPhrase   string
	lastPhraseAt time.Time
	// guard — предел попыток входа по фразе (phrase.go)
	guard phraseGuard
}

func New(st *store.Store) *Auth {
	return &Auth{st: st, ceremonies: map[string]*Ceremony{}}
}

func (a *Auth) RPID() string { return a.st.SettingText(rpIDDef) }

// Origin — адрес входа. Заданный человеком не переписывается никогда;
// не задан — выводится из адреса прослушивания (порт тот же).
func (a *Auth) Origin() string {
	if v := a.st.SettingText(originDef); v != "" {
		return v
	}
	return DefaultOrigin(a.listen)
}

// OriginDefault — адрес входа, который был бы, пока человек его не назвал: у
// этой настройки умолчание не число, а вывод из адреса прослушивания.
func (a *Auth) OriginDefault() string { return DefaultOrigin(a.listen) }

// SetListen — адрес прослушивания, из которого выводится адрес входа по
// умолчанию. Зовётся при старте сервера.
func (a *Auth) SetListen(addr string) { a.listen = addr }

// DefaultOrigin — адрес входа из адреса прослушивания: localhost и порт.
func DefaultOrigin(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		port = "8765"
	}
	return "http://localhost:" + port
}

// OriginWarning — заданный адрес входа не сходится с тем, где Сёма
// слушает: не переписываем, а предупреждаем. Пусто — всё сходится.
func OriginWarning(origin, listen, tlsListen string) string {
	if origin == "" {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return "адрес входа auth.origin «" + origin + "» не разбирается: нужен вид https://имя:порт"
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	_, lp, _ := net.SplitHostPort(listen)
	_, tp, _ := net.SplitHostPort(tlsListen)
	if port == lp || (tlsListen != "" && port == tp) {
		if u.Scheme == "https" && tlsListen == "" {
			return "адрес входа " + origin + " — https, а защищённый порт не настроен (SEMA_TLS_CERT/KEY): вход по этому адресу не откроется"
		}
		return ""
	}
	return "адрес входа " + origin + " ведёт на порт " + port + ", а Сёма слушает " + listen + strings.TrimSpace(" "+tlsListen) + ": это нормально только за своим прокси, который ведёт на этот порт; иначе поправьте auth.origin на «Доступе» или адрес прослушивания"
}

// LocalOpen — пускать ли «эту машину» без проверки личности. Непонятная
// запись читается умолчанием, а не «да»: молчание настройки не разрешение.
func (a *Auth) LocalOpen() bool {
	return a.st.SettingFlag(localOpenDef)
}

// LocalPrivate — считать ли частную сеть своей. Хранится и в настройке, и
// в флаге процесса: IsLocal зовётся на каждый запрос, ходить за ним в базу
// незачем. Флаг ставится при старте и при сохранении формы.
var LocalPrivate atomic.Bool

// LoadLocalScope — флаг процесса из настройки. Своей частная сеть становится
// только по явной записи «private»: пусто и непонятное — петля.
func (a *Auth) LoadLocalScope() {
	LocalPrivate.Store(a.st.SettingText(localScopeDef) == store.ScopePrivate)
}

// UserVerificationMode — что просить у хранителя ключа: required (по
// умолчанию) или preferred, если человек вернул прежнее поведение.
func (a *Auth) UserVerificationMode() string {
	if a.st.Setting(SettingUserVerification, "required") == "preferred" {
		return "preferred"
	}
	return "required"
}

// userVerification — что просить у хранителя ключа.
func (a *Auth) userVerification() protocol.UserVerificationRequirement {
	if a.UserVerificationMode() == "preferred" {
		return protocol.VerificationPreferred
	}
	return protocol.VerificationRequired
}

// UserVerificationNotice — строка в журнал при старте. Значение по
// умолчанию сменилось, и пройти это молча нельзя: человек с хранителем
// ключа, который проверку владельца не делает, узнал бы о смене только
// тем, что вход перестал работать. Пусто — говорить нечего.
func UserVerificationNotice(setting string) string {
	switch setting {
	case "":
		return "проверка владельца ключа теперь обязательна (auth.user_verification = required по умолчанию): " +
			"хранитель ключа спросит отпечаток, лицо или код устройства при каждом входе. " +
			"Ключ, заведённый хранителем без такой проверки, входить перестанет — тогда войдите контрольной фразой " +
			"и верните «preferred» тумблером «Требовать проверку владельца ключа» на «Доступе»"
	case "preferred":
		return "проверка владельца ключа не обязательна (auth.user_verification = preferred): так решено вами; " +
			"на публичном адресе это значит, что найденный телефон с ключом войдёт без человека"
	}
	return ""
}

// LocalHosts — имена сервера, разрешённые из своей сети, кроме адреса входа.
func (a *Auth) LocalHosts() []string {
	var out []string
	for _, h := range strings.Split(a.st.SettingText(localHostsDef), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func (a *Auth) webauthn() (*webauthn.WebAuthn, error) {
	origins := []string{a.Origin()}
	// приложение и curl ходят на 127.0.0.1 — тот же сервер, что и localhost
	for _, o := range []string{"http://localhost:8765", "http://127.0.0.1:8765"} {
		if o != origins[0] {
			origins = append(origins, o)
		}
	}
	return webauthn.New(&webauthn.Config{RPDisplayName: "Сёма", RPID: a.RPID(), RPOrigins: origins})
}

// loadOwner — ключи владельца целиком, без разбора имени. Годится там, где
// нужен сам факт «ключи есть»; в церемонию так отдавать нельзя — см. ниже.
func (a *Auth) loadOwner() *owner { return a.ownerKeys(false) }

// ownerCeremony — ключи для церемонии, отобранные по нынешнему имени
// сервера (RP ID). Ключ заводится браузером **под конкретное имя**, и чужие
// в церемонии не просто бесполезны, а вредны, причём по-разному:
//
//   - на входе они уезжают списком «предложи из этих», и хранитель честно
//     не находит ни одного — вход падает;
//   - при заведении они уезжают списком «такие уже есть, не заводи
//     повторно», и хранитель, у которого эти ключи лежат (их завели на
//     этой же машине, пусть и под другим именем), отвечает «уже заведён».
//
// Браузер в обоих случаях отдаёт одну безликую ошибку «operation either
// timed out or was not allowed», по которой причину не найти. Так и вышло
// 28 сентября 2026 при переезде на внешний домен: имя сменили, а прежние
// три ключа продолжали уезжать в церемонию и не давали завести новый.
//
// Ключи, заведённые до того, как Сёма стала запоминать имя (RPID пусто),
// на вход допускаются: у большинства имя не менялось ни разу, и отказать
// им значило бы сломать вход на ровном месте. А вот в список «уже есть»
// они не идут — там ошибка в эту сторону лишь заведёт лишний ключ, а в
// обратную запирает дверь.
func (a *Auth) ownerCeremony(forLogin bool) *owner { return a.ownerKeys(true, forLogin) }

func (a *Auth) ownerKeys(byRP bool, forLogin ...bool) *owner {
	o := &owner{}
	rp := strings.ToLower(strings.TrimSpace(a.RPID()))
	вход := len(forLogin) > 0 && forLogin[0]
	keys, _ := a.st.Passkeys()
	for _, k := range keys {
		if byRP {
			своё := strings.ToLower(strings.TrimSpace(k.RPID))
			switch {
			case своё == rp: // имя совпало
			case своё == "" && вход: // старый ключ без имени — на вход пускаем
			default:
				continue
			}
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(k.Credential), &c) == nil {
			o.creds = append(o.creds, c)
			o.ids = append(o.ids, k.ID)
		}
	}
	return o
}

func (a *Auth) gc() {
	now := time.Now()
	for id, c := range a.ceremonies {
		if now.After(c.expires) {
			delete(a.ceremonies, id)
		}
	}
}

// BeginRegistration — параметры для navigator.credentials.create.
func (a *Auth) BeginRegistration(name string) (any, string, error) {
	w, err := a.webauthn()
	if err != nil {
		return nil, "", err
	}
	o := a.ownerCeremony(false)
	var exclude []protocol.CredentialDescriptor
	for _, c := range o.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, sd, err := w.BeginRegistration(o,
		webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementPreferred, UserVerification: a.userVerification()}))
	if err != nil {
		return nil, "", err
	}
	id := store.NewSecret("c_")
	a.mu.Lock()
	a.gc()
	if len(a.ceremonies) >= 128 {
		a.mu.Unlock()
		return nil, "", fmt.Errorf("слишком много попыток входа, повторите позже")
	}
	a.ceremonies[id] = &Ceremony{data: sd, kind: "register", name: name, expires: time.Now().Add(5 * time.Minute)}
	a.mu.Unlock()
	return opts, id, nil
}

// FinishRegistration — ключ сохраняется, и, если есть код приложения, сразу
// открывается сессия.
func (a *Auth) FinishRegistration(id, name string, r *http.Request) (int64, *Ceremony, error) {
	a.mu.Lock()
	c, ok := a.ceremonies[id]
	delete(a.ceremonies, id)
	a.mu.Unlock()
	if !ok || c.kind != "register" || time.Now().After(c.expires) {
		return 0, nil, fmt.Errorf("церемония не найдена или устарела")
	}
	w, err := a.webauthn()
	if err != nil {
		return 0, nil, err
	}
	cred, err := w.FinishRegistration(a.loadOwner(), *c.data, r)
	if err != nil {
		return 0, nil, err
	}
	raw, _ := json.Marshal(cred)
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(c.name)
	}
	if name == "" {
		name = "Ключ от " + time.Now().Format("02.01.2006")
	}
	pid, err := a.st.AddPasskey(name, string(raw), a.RPID())
	return pid, c, err
}

// BeginLogin — параметры для navigator.credentials.get.
func (a *Auth) BeginLogin() (any, string, error) {
	w, err := a.webauthn()
	if err != nil {
		return nil, "", err
	}
	o := a.ownerCeremony(true)
	if len(o.creds) == 0 {
		return nil, "", fmt.Errorf("ключей для «%s» ещё нет", a.RPID())
	}
	opts, sd, err := w.BeginLogin(o, webauthn.WithUserVerification(a.userVerification()))
	if err != nil {
		return nil, "", err
	}
	id := store.NewSecret("c_")
	a.mu.Lock()
	a.gc()
	if len(a.ceremonies) >= 128 {
		a.mu.Unlock()
		return nil, "", fmt.Errorf("слишком много попыток входа, повторите позже")
	}
	a.ceremonies[id] = &Ceremony{data: sd, kind: "login", expires: time.Now().Add(5 * time.Minute)}
	a.mu.Unlock()
	return opts, id, nil
}

// FinishLogin — проверка подписи; возвращает id ключа, которым вошли.
func (a *Auth) FinishLogin(id string, r *http.Request) (int64, *Ceremony, error) {
	a.mu.Lock()
	c, ok := a.ceremonies[id]
	delete(a.ceremonies, id)
	a.mu.Unlock()
	if !ok || c.kind != "login" || time.Now().After(c.expires) {
		return 0, nil, fmt.Errorf("церемония не найдена или устарела")
	}
	w, err := a.webauthn()
	if err != nil {
		return 0, nil, err
	}
	o := a.loadOwner()
	cred, err := w.FinishLogin(o, *c.data, r)
	if err != nil {
		return 0, nil, err
	}
	for i, k := range o.creds {
		if string(k.ID) == string(cred.ID) {
			raw, _ := json.Marshal(cred)
			a.st.UpdatePasskeyCredential(o.ids[i], string(raw))
			return o.ids[i], c, nil
		}
	}
	// подпись сошлась, а ключа в списке нет: его удалили между началом
	// и концом церемонии. Раньше это возвращалось как успешный вход с
	// нулевым ключом — нашёл аудит Codex 7 сентября 2026
	return 0, nil, fmt.Errorf("ключ входа больше не зарегистрирован")
}

// OpenSession — новая сессия после входа.
func (a *Auth) OpenSession(passkeyID int64, agent, ip, note string) (string, error) {
	token := store.NewSecret(SessionPrefix)
	if err := a.st.AddSession(token, passkeyID, agent, ip, note, SessionTTL); err != nil {
		return "", err
	}
	return token, nil
}

// NormalizeIP — один и тот же компьютер под разными именами: «::1» и
// «127.0.0.1», «::ffff:192.168.1.5» и «192.168.1.5» — один адрес.
func NormalizeIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if p := net.ParseIP(ip); p != nil {
		if p.IsLoopback() {
			return "127.0.0.1"
		}
		if v4 := p.To4(); v4 != nil {
			return v4.String()
		}
		return p.String()
	}
	return ip
}

// Identity — кто пришёл: сессия, токен, локальный без входа или никто.
type Identity struct {
	Kind    string // session | token | local | none
	Who     string // для журнала: session:<passkey id>, token:<имя>, local
	Session *store.Session
	Token   *store.APIToken
}

// IsLocal — запрос с этой машины без прокси: loopback, а при
// auth.local_scope = private ещё и частная сеть. Это осознанная
// настройка, а не дыра: дома мак и телефон ходят к Pi по сети; за чужой
// сетью её выключают, и тогда вход нужен всем.
func IsLocal(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	return LocalPrivate.Load() && ip.IsPrivate()
}

// Identify разбирает заголовок Authorization и cookie.
func (a *Auth) Identify(r *http.Request) Identity {
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	candidates := []string{}
	if bearer != "" {
		candidates = append(candidates, bearer)
	}
	if c, err := r.Cookie("sema_session"); err == nil && c.Value != "" {
		candidates = append(candidates, c.Value)
	}
	for _, v := range candidates {
		switch {
		case strings.HasPrefix(v, SessionPrefix):
			if se, ok := a.st.SessionByToken(v, SessionTTL); ok {
				return Identity{Kind: "session", Who: fmt.Sprintf("session:%d", se.PasskeyID), Session: se}
			}
		case strings.HasPrefix(v, TokenPrefix):
			if t, ok := a.st.TokenByValue(v); ok {
				if !t.Revoked.IsZero() {
					return Identity{Kind: "none", Who: "token:" + t.Name + " (отозван)"}
				}
				return Identity{Kind: "token", Who: "token:" + t.Name, Token: t}
			}
			return Identity{Kind: "none", Who: "token:неизвестный"}
		}
	}
	if a.LocalOpen() && IsLocal(r) {
		return Identity{Kind: "local", Who: "local"}
	}
	return Identity{Kind: "none", Who: "anon"}
}

// ScopeFor — какое право нужно маршруту.
func ScopeFor(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/ui"):
		return "ui"
	case strings.HasPrefix(path, "/api/action"):
		return "actions"
	case strings.HasPrefix(path, "/api/files"):
		return "files"
	case strings.HasPrefix(path, "/api/push"), strings.HasPrefix(path, "/api/ws"):
		return "ui" // подписка на пуши и провод живого обновления — часть интерфейса, не отдельное право
	}
	// маршрутов памяти по сети больше нет: и `/mcp`, и `/api/memory/*`
	// сняты, память ходит проводом к жителю машины. Право «память» в
	// справочнике осталось, но открывать ему нечего — разбирать эти пути
	// значило бы обещать дверь, которой нет
	return "all"
}

func HasScope(t *store.APIToken, scope string) bool {
	for _, s := range t.Scopes {
		if s == "all" || s == scope {
			return true
		}
	}
	return false
}
