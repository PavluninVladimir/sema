package engine

// Вход OAuth на чужие серверы MCP: Planner и все, кто отвечает 401 с
// `WWW-Authenticate: Bearer` и не хочет постоянных ключей. Протокол —
// `internal/mcp/oauth.go`; здесь то, что принадлежит Сёме: где лежат токены,
// кто их продлевает, что видит человек и чем кончается, когда вход умер.
//
// Договорённости:
//   - токены — одна запись хранилища секретов на сервер (`mcp:<имя>:oauth`,
//     вид «oauth»), значение всегда `enc:v1`; каждое чтение ядром видно в
//     журнале секрета. Модулю эта запись не выдаётся, экрану — не отдаётся;
//   - несекретное для экрана (когда вошли, до какого часа, какие права) —
//     в строке сервера (`MCPServer.Login`): чтобы экран, который
//     перерисовывается каждые несколько секунд, не заполнял журнал секрета;
//   - начатые входы живут в памяти десять минут: verifier не должен лежать
//     на диске, а потерянная попытка стоит одного нажатия;
//   - продление — под замком сервера: refresh_token у многих серверов
//     одноразовый, и два одновременных продления убили бы вход.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/mcp"
	"github.com/PavluninVladimir/sema/server/internal/netguard"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// MCPCallbackPath — куда сервер входа возвращает человека. Адрес целиком —
// `<адрес Сёмы, откуда нажали «Войти»>` + этот путь.
const MCPCallbackPath = "/mcp/oauth/callback"

// MCPClientDocPath — где лежит документ клиента (Client ID Metadata
// Document). Единственная открытая без входа ручка этого раздела: сервер
// входа скачивает документ сам, а у него нет ни сессии, ни токена. Адрес
// целиком — `<публичный адрес Сёмы>` + этот путь, и он же — client_id.
const MCPClientDocPath = "/mcp/oauth/client.json"

// Пределы входа — настройки, а не числа в коде (правятся на «Интеграциях»);
// здесь только первичные значения. Начатый вход живёт десять минут, токен
// продлевается за минуту до конца, запрос к серверу входа ждёт двадцать
// секунд — так называют эти сроки спецификация и привычная работа Claude Code.
const (
	mcpLoginTTLKey    = "mcp.login_ttl_min"
	mcpRefreshSkewKey = "mcp.refresh_skew_s"
	mcpNetTimeoutKey  = "mcp.net_timeout_s"
	// повтор обмена кода и продления на invalid_client (сервер входа за
	// балансировщиком отвечает через раз) и пользование документом клиента
	mcpTokenRetriesKey   = "mcp.token_retries"
	mcpTokenPauseKey     = "mcp.token_retry_ms"
	mcpClientMetadataKey = "mcp.client_metadata"

	mcpLoginCap   = 40  // начатых входов в памяти не больше: защита памяти, а не политика
	mcpNoteMax    = 300 // сколько знаков причины неудачного входа кладём в строку сервера
	mcpClientName = "Сёма"
)

// MCPLoginLimits — пределы входа из настроек: сколько живёт начатый вход,
// за сколько до конца продлевать токен, сколько ждать сервер входа.
func (e *Engine) MCPLoginLimits() (ttl, skew, netTimeout time.Duration) {
	return time.Duration(e.store.SettingInt(mcpLoginTTLDef)) * time.Minute,
		time.Duration(e.store.SettingInt(mcpRefreshSkewDef)) * time.Second,
		time.Duration(e.store.SettingInt(mcpNetTimeoutDef)) * time.Second
}

// MCPRetryLimits — сколько раз и с какой паузой повторять обмен кода и
// продление, когда сервер входа отвечает invalid_client. Ноль повторов —
// не повторять вовсе; ноль паузы — без паузы.
func (e *Engine) MCPRetryLimits() (tries int, pause time.Duration) {
	tries, ms := e.store.SettingInt(mcpTokenRetriesDef), e.store.SettingInt(mcpTokenPauseDef)
	return tries, time.Duration(ms) * time.Millisecond
}

// MCPClientMetadataOn — пользоваться ли документом клиента, когда это
// возможно (`mcp.client_metadata`, по умолчанию да). Выключено — Сёма, как
// раньше, регистрируется у сервера входа сам. На саму отдачу документа
// настройка не влияет: вход, начатый документом, продлевается им же.
func (e *Engine) MCPClientMetadataOn() bool { return e.store.SettingFlag(mcpClientMetadataDef) }

// errMCPLoginLost — вход умер (сервер отказал в продлении или отверг токен):
// нужен новый, руками.
var errMCPLoginLost = errors.New("вход истёк")

func mcpOAuthSecret(name string) string { return "mcp:" + name + ":oauth" }

func mcpLoginKey(name string) string { return "mcp_login:" + name }

// mcpTokens — то, что лежит в записи хранилища.
type mcpTokens struct {
	Access        string `json:"access_token"`
	Refresh       string `json:"refresh_token,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	TokenEndpoint string `json:"token_endpoint"`
	Revocation    string `json:"revocation_endpoint,omitempty"`
	ClientID      string `json:"client_id"`
	Scope         string `json:"scope,omitempty"`
	Resource      string `json:"resource,omitempty"`
}

func (t *mcpTokens) expires() time.Time {
	at, err := time.Parse(time.RFC3339, t.ExpiresAt)
	if err != nil {
		return time.Time{}
	}
	return at
}

// needsRefresh — срок называть нечем — считаем вечным: продлим по 401.
func (t *mcpTokens) needsRefresh(now time.Time, skew time.Duration) bool {
	at := t.expires()
	return !at.IsZero() && now.Add(skew).After(at)
}

// mcpRuntime — то, что живёт в памяти процесса: клиенты с сессиями, начатые
// входы, замки продления.
type mcpRuntime struct {
	clients map[string]*mcpEntry
	tries   map[string]*mcpLoginTry // по state
	byName  map[string]string       // сервер → state его начатого входа
	locks   map[string]*sync.Mutex
}

type mcpEntry struct {
	sig string
	c   *mcp.Client
}

// mcpLoginTry — одна начатая попытка входа.
type mcpLoginTry struct {
	State         string
	Server        string
	Who           string // кто нажал «Войти»: вернуться обязан он же
	Verifier      string
	Redirect      string
	ClientID      string
	ByDocument    bool   // клиент назван адресом документа, а не зарегистрирован
	DocSkipped    string // что сделать, чтобы войти документом, хотя сервер входа его принимает: совет человеку
	Issuer        string
	TokenEndpoint string
	Revocation    string
	Resource      string
	Scope         string
	URL           string
	Expires       time.Time
}

func (e *Engine) mcpState() *mcpRuntime {
	// зовётся под mcpMu
	if e.mcpX.clients == nil {
		e.mcpX.clients = map[string]*mcpEntry{}
		e.mcpX.tries = map[string]*mcpLoginTry{}
		e.mcpX.byName = map[string]string{}
		e.mcpX.locks = map[string]*sync.Mutex{}
	}
	return &e.mcpX
}

func (e *Engine) mcpLock(name string) *sync.Mutex {
	e.mcpMu.Lock()
	defer e.mcpMu.Unlock()
	rt := e.mcpState()
	if rt.locks[name] == nil {
		rt.locks[name] = &sync.Mutex{}
	}
	return rt.locks[name]
}

// mcpDrop — забыть клиента с его сессией: новый вход, выход, правка адреса.
func (e *Engine) mcpDrop(name string) {
	e.mcpMu.Lock()
	defer e.mcpMu.Unlock()
	delete(e.mcpState().clients, name)
}

// ---- сеть входа ----

// originRoundTripper — куда ходить с запросом. К адресу, который человек сам
// записал в справочник, — обычным транспортом: это его выбор, и сервер может
// стоять хоть в своей сети. К адресам, которые назвал чужой сервер, — только
// защищённым, где имя разрешается один раз и в частную сеть не пускает.
type originRoundTripper struct {
	home           *url.URL
	plain, guarded http.RoundTripper
}

func (o originRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if sameOrigin(r.URL, o.home) {
		return o.plain.RoundTrip(r)
	}
	return o.guarded.RoundTrip(r)
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// mcpGuardedTransport — транспорт `netguard`: только публичные адреса.
var mcpGuardedTransport = netguard.Client(0).Transport

// mcpLoginNet — сетевые правила входа для сервера с этим адресом. Переменная,
// а не функция: тесты подменяют её на свой httptest-сервер, как `scriptHTTP`
// и `falHTTP`; рабочую охрану никто не трогает.
var mcpLoginNet = func(mcpURL string, timeout time.Duration) mcp.Net {
	home, _ := url.Parse(mcpURL)
	return mcp.Net{
		HTTP: &http.Client{
			Timeout:       timeout,
			Transport:     originRoundTripper{home: home, plain: http.DefaultTransport, guarded: mcpGuardedTransport},
			CheckRedirect: netguard.SameOriginRedirect,
		},
		Check: func(u *url.URL) error {
			if sameOrigin(u, home) {
				return nil
			}
			if u.Scheme != "https" {
				return errors.New("токены по незащищённому http не отдаются")
			}
			return netguard.ValidateURL(u)
		},
	}
}

// mcpProbeClient — клиент для первого вопроса серверу без входа: адрес
// записал человек, охраны сверх обычного не нужно.
var mcpProbeClient = func(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: netguard.SameOriginRedirect}
}

// loginNet — сеть входа для сервера с пределом из настроек. name нужен только
// журналу: одна строка про повторы должна называть сервер.
func (e *Engine) loginNet(name, mcpURL string) mcp.Net {
	_, _, timeout := e.MCPLoginLimits()
	n := mcpLoginNet(hostAlias(mcpURL), timeout)
	n.TokenRetries, n.TokenRetryPause = e.MCPRetryLimits()
	n.Note = func(line string) { log.Printf("MCP «%s»: %s", name, line) }
	return n
}

// ---- клиент с сессией и входом ----

// mcpClient — клиент этого сервера. Он один на сервер и живёт, пока не
// изменились адрес или ключ: сессия MCP переживает вызовы, но не
// перезапуск Сёмы.
func (e *Engine) mcpClient(m *store.MCPServer) (*mcp.Client, error) {
	sig := strings.Join([]string{m.URL, m.Secret, m.Header, m.Prefix}, "\x00")
	e.mcpMu.Lock()
	defer e.mcpMu.Unlock()
	rt := e.mcpState()
	if ent := rt.clients[m.Name]; ent != nil && ent.sig == sig {
		return ent.c, nil
	}
	c := mcp.NewClient(hostAlias(m.URL), nil)
	c.Auth = &mcpAuth{e: e, name: m.Name}
	rt.clients[m.Name] = &mcpEntry{sig: sig, c: c}
	return c, nil
}

// mcpAuth — заголовки входа для одного сервера: постоянный ключ, если он
// задан, иначе токен OAuth, если вход выполнен, иначе ничего.
type mcpAuth struct {
	e    *Engine
	name string
}

func (a *mcpAuth) Headers(ctx context.Context) (map[string]string, error) {
	m, ok := a.e.store.MCPServer(a.name)
	if !ok {
		return nil, fmt.Errorf("сервера %q больше нет", a.name)
	}
	if m.Secret != "" {
		return a.e.mcpHeaders(m)
	}
	if !a.e.mcpHasLogin(a.name) {
		return nil, nil
	}
	tok, err := a.e.mcpAccessToken(ctx, a.name, "")
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

func (a *mcpAuth) Unauthorized(ctx context.Context, sent string, ch *mcp.Challenge) (bool, error) {
	m, ok := a.e.store.MCPServer(a.name)
	if !ok || m.Secret != "" || !a.e.mcpHasLogin(a.name) {
		return false, nil // постоянный ключ или входа нет: обновлять нечего
	}
	// вход умер — ошибка errMCPLoginLost уходит наверх как есть, а состояние
	// «нужен вход» и уведомление уже поставлены
	if _, err := a.e.mcpAccessToken(ctx, a.name, sent); err != nil {
		return false, err
	}
	return true, nil
}

// mcpHasLogin — токены лежат в хранилище (значение не читается и в журнал
// секрета не пишется).
func (e *Engine) mcpHasLogin(name string) bool {
	_, ok := e.store.Secret(mcpOAuthSecret(name))
	return ok
}

func (e *Engine) readMCPTokens(name, why string) (*mcpTokens, error) {
	v, err := e.coreSecretFiller("ядро · сервер MCP «" + name + "» · " + why)(mcpOAuthSecret(name))
	if err != nil {
		return nil, err
	}
	var t mcpTokens
	if err := json.Unmarshal([]byte(v), &t); err != nil || t.Access == "" {
		return nil, fmt.Errorf("запись входа в сервер «%s» повреждена — войдите заново", name)
	}
	return &t, nil
}

func (e *Engine) saveMCPTokens(name string, t *mcpTokens) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := e.store.SaveSecret(&store.Secret{Name: mcpOAuthSecret(name), Kind: store.SecretOAuth,
		Note: "вход OAuth на сервер MCP «" + name + "» — им пользуется только ядро"}, string(raw)); err != nil {
		return err
	}
	l := &store.MCPLogin{At: time.Now().Format(time.RFC3339), Scope: t.Scope, Refresh: t.Refresh != ""}
	if at := t.expires(); !at.IsZero() {
		l.Expires = at.Format(time.RFC3339)
	}
	// «вошли в» — момент первого входа: продление его не сдвигает
	if m, ok := e.store.MCPServer(name); ok && m.Login != nil && m.Login.At != "" {
		l.At = m.Login.At
	}
	return e.store.SetMCPLogin(name, l)
}

// mcpAccessToken — токен для запроса. rejected — Authorization, который
// сервер только что отверг: тогда токен продлевается, если его ещё никто не
// продлил.
func (e *Engine) mcpAccessToken(ctx context.Context, name, rejected string) (string, error) {
	lock := e.mcpLock(name)
	lock.Lock()
	defer lock.Unlock()
	t, err := e.readMCPTokens(name, "токен входа")
	if err != nil {
		return "", err
	}
	now := time.Now()
	_, skew, _ := e.MCPLoginLimits()
	if rejected == "" {
		if !t.needsRefresh(now, skew) {
			return t.Access, nil
		}
	} else if rejected != "Bearer "+t.Access {
		return t.Access, nil // пока мы ждали замок, токен уже сменили
	}
	return e.refreshMCP(ctx, name, t, now)
}

func (e *Engine) refreshMCP(ctx context.Context, name string, t *mcpTokens, now time.Time) (string, error) {
	if t.Refresh == "" {
		e.mcpLoginLost(name, "токен не принят, а обновить его нечем")
		return "", errMCPLoginLost
	}
	m, ok := e.store.MCPServer(name)
	if !ok {
		return "", fmt.Errorf("сервера %q больше нет", name)
	}
	fresh, err := e.loginNet(name, m.URL).Refresh(ctx, t.TokenEndpoint, t.ClientID, t.Refresh, t.Resource, now)
	if err != nil {
		var te *mcp.TokenError
		if errors.As(err, &te) && te.Definitive() {
			e.mcpLoginLost(name, te.Error())
			return "", errMCPLoginLost
		}
		return "", fmt.Errorf("не удалось продлить вход: %w", err)
	}
	t.Access, t.Refresh = fresh.Access, fresh.Refresh
	if fresh.Scope != "" {
		t.Scope = fresh.Scope
	}
	t.ExpiresAt = ""
	if !fresh.ExpiresAt.IsZero() {
		t.ExpiresAt = fresh.ExpiresAt.Format(time.RFC3339)
	}
	// новый refresh_token сервер уже выдал, а старый умер: не записать —
	// значит потерять вход, поэтому о неудаче говорим громко
	if err := e.saveMCPTokens(name, t); err != nil {
		log.Printf("MCP «%s»: продлённый вход не записался: %v", name, err)
		return "", fmt.Errorf("вход продлён, но записать его не удалось: %w", err)
	}
	return t.Access, nil
}

// mcpLoginLost — вход умер окончательно. Токены удаляются (они мертвы), а
// человек получает уведомление: без него сервер молча превратился бы в
// «не отвечает».
func (e *Engine) mcpLoginLost(name, reason string) {
	log.Printf("MCP «%s»: вход потерян: %s", name, clip(reason, 200))
	_ = e.store.DeleteSecret(mcpOAuthSecret(name))
	_ = e.store.SetMCPLogin(name, nil)
	_ = e.store.SetMCPState(name, "login", "вход истёк: "+clip(reason, 160)+" — нужен новый")
	e.mcpDrop(name)
	e.notifyMCPLogin(name, "вход истёк: "+clip(reason, 160))
}

// notifyMCPLogin — уведомление «нужен вход». Открытое с тем же ключом не
// дублируется: каждый неудачный вызов модуля иначе слал бы новый пуш.
func (e *Engine) notifyMCPLogin(name, why string) {
	key := mcpLoginKey(name)
	if _, open := e.store.OpenNotificationByKey(key); open {
		return
	}
	_, _ = e.Notify(&store.Notification{
		Key: key, Source: "core", Level: "warn",
		Title:   "Сервер MCP «" + name + "»: нужен вход",
		Summary: why,
		Fields:  []store.NotifyField{{Label: "Сервер", Value: name}},
		Actions: []store.NotifyAction{{Label: "Открыть сервер", Kind: "goto", Screen: MCPServerScreen(name, ""), Style: "primary"}},
		Ref:     key,
	})
}

// mcpFailure переводит ошибку обмена с сервером в понятную человеку и
// поднимает состояние «нужен вход», когда причина в нём. quiet — человек
// сам смотрит на экран (проверка по кнопке), уведомление ему не нужно.
func (e *Engine) mcpFailure(m *store.MCPServer, err error, quiet bool) error {
	if err == nil || m.Agent != "" {
		return err
	}
	var ae *mcp.AuthError
	switch {
	case errors.Is(err, errMCPLoginLost):
		return fmt.Errorf("вход в сервер MCP «%s» истёк: откройте «Интеграции» и нажмите «Войти»", m.Name)
	case errors.As(err, &ae) && m.Secret == "":
		if e.mcpHasLogin(m.Name) {
			// токен есть, а сервер отвергает и его, и продлённый: вход надо
			// делать заново, иначе строка врала бы «вход выполнен»
			e.mcpLoginLost(m.Name, "сервер не принял токен даже после продления")
			return fmt.Errorf("вход в сервер MCP «%s» не принят: откройте «Интеграции» и нажмите «Войти»", m.Name)
		}
		note := "нужен вход: сервер отвечает 401"
		if ae.Challenge != nil && ae.Challenge.Scope != "" {
			note += ", права: " + clip(ae.Challenge.Scope, 100)
		}
		_ = e.store.SetMCPState(m.Name, "login", note)
		if !quiet {
			e.notifyMCPLogin(m.Name, "сервер отвечает 401 и ждёт входа")
		}
		return fmt.Errorf("сервер MCP «%s» требует вход: откройте «Интеграции» и нажмите «Войти»", m.Name)
	}
	return err
}

// ---- документ клиента ----

// mcpPublicOrigin — адрес Сёмы, по которому его достаёт сервер входа из
// интернета: заданный человеком `auth.origin`, если он подходит. Подходит
// только https-адрес с настоящим именем: не `localhost`, не адрес числом из
// частной сети, не имя из своей сети (`sema.local`, `sema`, `*.lan`). Иначе
// сервер входа документ не скачает, а название внутреннего адреса ушло бы
// наружу ни за чем. Не задан — пусто: выводить публичный адрес из порта
// прослушивания нельзя, он не публичный.
func (e *Engine) mcpPublicOrigin() (string, bool) {
	raw := strings.TrimRight(strings.TrimSpace(e.store.SettingText(store.AuthOrigin)), "/")
	u, err := url.Parse(raw)
	if raw == "" || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || netguard.ValidateURL(u) != nil {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if !strings.Contains(host, ".") {
		return "", false // одно слово — имя из своей сети
	}
	for _, own := range []string{".lan", ".internal", ".home.arpa", ".localdomain", ".intranet", ".corp", ".home"} {
		if strings.HasSuffix(host, own) {
			return "", false
		}
	}
	return "https://" + strings.ToLower(u.Host), true
}

// mcpClientDoc — публичный адрес Сёмы и адрес документа клиента на нём.
func (e *Engine) mcpClientDoc() (origin, docURL string, ok bool) {
	origin, ok = e.mcpPublicOrigin()
	if !ok {
		return "", "", false
	}
	docURL = origin + MCPClientDocPath
	if mcp.CheckClientIDURL(docURL) != nil {
		return "", "", false
	}
	return origin, docURL, true
}

// MCPClientDocumentURL — адрес документа клиента: он же client_id. Нет
// публичного адреса — нет и документа.
func (e *Engine) MCPClientDocumentURL() (string, bool) {
	_, docURL, ok := e.mcpClientDoc()
	return docURL, ok
}

// MCPClientDocument — тело документа клиента для ручки `MCPClientDocPath`.
//
// Адрес возврата в нём один — `<публичный адрес>/mcp/oauth/callback`, и это
// нарочно. Возвращать человека надо туда, где открыта его сессия, а сессия
// у каждого адреса своя, поэтому вход, начатый с другого адреса
// (`sema.local:8443`), документом не идёт — он идёт регистрацией, как раньше.
// Перечислять в публичном документе все адреса, с которых когда-нибудь
// начинали вход, значило бы светить наружу внутренние имена и хранить их
// списком; к тому же черновик разрешает серверу входа требовать, чтобы адреса
// возврата стояли на том же хосте, что и client_id, и такие серверы
// отвергли бы весь документ из-за одного чужого адреса. В документе только
// то, что сервер входа покажет человеку на странице согласия, и всё берётся из
// настроек: запрос ничего в него не подмешивает.
func (e *Engine) MCPClientDocument() ([]byte, bool) {
	origin, docURL, ok := e.mcpClientDoc()
	if !ok {
		return nil, false
	}
	raw, err := json.Marshal(mcp.NewClientDocument(docURL, origin, mcpClientName, []string{origin + MCPCallbackPath}))
	if err != nil {
		return nil, false
	}
	return append(raw, '\n'), true
}

// invalidClientTail — что сказать человеку, когда сервер входа не узнал
// клиента, — и заодно забыть регистрацию, если клиентом была она: в
// следующий раз Сёма зарегистрируется заново, а не будет держаться за
// клиента, которого сервер не знает.
func (e *Engine) invalidClientTail(name string, try *mcpLoginTry) string {
	if try.ByDocument {
		return "сервер входа не принял документ клиента " + try.ClientID +
			": он должен открываться из интернета; проверьте его и нажмите «Войти» ещё раз"
	}
	_ = e.store.DeleteMCPClients(name)
	tail := "сервер входа не узнал клиента Сёмы: за балансировщиком так бывает — нажмите «Войти» ещё раз"
	if try.DocSkipped != "" {
		tail += "; надёжнее: " + try.DocSkipped
	}
	return tail
}

// ---- вход: первая половина ----

// MCPLoginBegin — человек нажал «Войти». Находит сервер входа, регистрирует
// Сёму, запоминает попытку и возвращает адрес страницы согласия. origin —
// адрес Сёмы, из которого человек нажал: туда сервер входа вернёт его с кодом,
// и именно там открыта его сессия. who — чья это попытка.
func (e *Engine) MCPLoginBegin(ctx context.Context, name, origin, who string) (string, error) {
	m, ok := e.store.MCPServer(name)
	if !ok {
		return "", fmt.Errorf("сервера %q нет", name)
	}
	if m.Agent != "" || strings.TrimSpace(m.URL) == "" {
		return "", fmt.Errorf("сервер «%s» живёт у агента: входа на нём нет", name)
	}
	if m.Secret != "" {
		return "", fmt.Errorf("у сервера «%s» задан постоянный ключ «%s»: вход не нужен; уберите ключ, если хотите входить через OAuth", name, m.Secret)
	}
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return "", fmt.Errorf("не понятно, с какого адреса Сёма открыт: некуда возвращать после входа")
	}
	redirect := origin + MCPCallbackPath
	ttl, _, netTimeout := e.MCPLoginLimits()
	// на всю подготовку входа — четыре обращения к сети: вопрос серверу,
	// метаданные ресурса, метаданные сервера входа, регистрация
	cctx, cancel := context.WithTimeout(ctx, 2*netTimeout)
	defer cancel()

	// сервер должен сам сказать, что вход нужен, и откуда его начинать
	ch, status, err := mcp.Probe(cctx, mcpProbeClient(netTimeout), hostAlias(m.URL), nil)
	if err != nil {
		return "", fmt.Errorf("сервер «%s» не отвечает: %w", name, err)
	}
	switch {
	case status == http.StatusUnauthorized && ch == nil:
		return "", fmt.Errorf("сервер «%s» требует вход, но не предлагает Bearer: другой вид входа Сёма не умеет — задайте постоянный ключ", name)
	case status/100 == 2 && m.State != "login":
		return "", fmt.Errorf("сервер «%s» отвечает без входа — входить не нужно", name)
	case status/100 == 2:
		// рукопожатие сервер пускает без входа, а инструменты нет (проверка
		// уже видела 401): адрес метаданных вызовом не назван, ищем по
		// известным путям
		ch = nil
	case status != http.StatusUnauthorized:
		return "", fmt.Errorf("сервер «%s» ответил HTTP %d, а не 401: начинать вход не с чего", name, status)
	}

	net := e.loginNet(m.Name, m.URL)
	d, err := net.Discover(cctx, hostAlias(m.URL), ch)
	if err != nil {
		return "", err
	}
	scope := strings.Join(strings.Fields(m.LoginScope), " ")
	if scope == "" && ch != nil {
		scope = strings.Join(strings.Fields(ch.Scope), " ")
	}
	if scope == "" {
		scope = strings.Join(d.Scopes, " ")
	}

	issuer := d.Server.Issuer
	if issuer == "" {
		if tu, err := url.Parse(d.Server.TokenEndpoint); err == nil {
			issuer = tu.Scheme + "://" + tu.Host
		}
	}
	// Как назвать себя серверу входа. Документ клиента предпочтительнее
	// (MCP Authorization, 2025-11-25): его знает любой узел сервера входа, а
	// зарегистрированного клиента — только тот, куда регистрация попала, и за
	// балансировщиком вход проходил через раз. Документ годится, когда сервер
	// входа его принимает, у Сёмы есть публичный https-адрес и вход начат
	// именно с него (иначе адрес возврата в документе не покрыл бы этот вход).
	var clientID string
	byDoc, skipped := false, ""
	if d.Server.ClientMetadataDocument && e.MCPClientMetadataOn() {
		pub, docURL, ok := e.mcpClientDoc()
		switch {
		case !ok:
			skipped = "впишите публичный https-адрес Сёмы в «Вход и доступ» как адрес страницы входа — тогда Сёма назовёт себя документом, а не регистрацией"
		case !sameOriginText(origin, pub):
			skipped = "начните вход с " + pub + " — там Сёма назовёт себя документом, а не регистрацией"
		default:
			clientID, byDoc = docURL, true
		}
	}
	if !byDoc {
		known := false
		clientID, known = e.store.MCPClient(name, redirect, issuer)
		if !known {
			if d.Server.RegistrationEndpoint == "" {
				if d.Server.ClientMetadataDocument {
					why := skipped
					if why == "" {
						why = "включите «Называть себя документом клиента» в пределах входа на «Интеграциях»"
					}
					return "", fmt.Errorf("сервер входа %s принимает клиентов только по документу, а Сёма сейчас не может им воспользоваться. Что делать: %s", hostOf(d.Server.TokenEndpoint), why)
				}
				return "", fmt.Errorf("сервер входа %s не умеет регистрировать клиентов (в метаданных нет registration_endpoint): вход без регистрации Сёма пока не поддерживает", hostOf(d.Server.TokenEndpoint))
			}
			clientID, err = net.Register(cctx, d.Server.RegistrationEndpoint, redirect, mcpClientName)
			if err != nil {
				return "", fmt.Errorf("%w (адрес возврата: %s)", err, redirect)
			}
			if err := e.store.SaveMCPClient(store.MCPClientReg{Server: name, Redirect: redirect, Issuer: issuer, ClientID: clientID}); err != nil {
				return "", err
			}
		}
	}

	verifier, challenge := mcp.NewPKCE()
	state := mcp.RandomState()
	link, err := mcp.AuthorizeURL(d.Server.AuthorizationEndpoint, mcp.AuthorizeParams{
		ClientID: clientID, RedirectURI: redirect, State: state, Challenge: challenge, Resource: d.Resource, Scope: scope})
	if err != nil {
		return "", err
	}
	try := &mcpLoginTry{State: state, Server: name, Who: who, Verifier: verifier, Redirect: redirect,
		ClientID: clientID, ByDocument: byDoc, DocSkipped: skipped, Issuer: issuer, TokenEndpoint: d.Server.TokenEndpoint, Revocation: d.Server.RevocationEndpoint,
		Resource: d.Resource, Scope: scope, URL: link, Expires: time.Now().Add(ttl)}
	e.mcpMu.Lock()
	rt := e.mcpState()
	if old := rt.byName[name]; old != "" {
		delete(rt.tries, old) // новая попытка отменяет прежнюю
	}
	now := time.Now()
	for k, t := range rt.tries {
		if now.After(t.Expires) {
			delete(rt.tries, k)
			if rt.byName[t.Server] == k {
				delete(rt.byName, t.Server)
			}
		}
	}
	if len(rt.tries) >= mcpLoginCap {
		e.mcpMu.Unlock()
		return "", fmt.Errorf("слишком много начатых входов: подождите десять минут")
	}
	rt.tries[state], rt.byName[name] = try, state
	e.mcpMu.Unlock()
	how := "регистрацией, client_id " + clientID
	if byDoc {
		how = "документом клиента " + clientID
	}
	log.Printf("MCP «%s»: вход начат, возврат на %s, клиент назван %s", name, redirect, how)
	return link, nil
}

// sameOriginText — два адреса вида схема://хост[:порт] совпадают.
func sameOriginText(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && sameOrigin(ua, ub)
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// MCPLoginPending — начатый вход этого сервера, если он ещё действует: адрес
// страницы согласия и срок.
func (e *Engine) MCPLoginPending(name string) (link string, until time.Time, ok bool) {
	e.mcpMu.Lock()
	defer e.mcpMu.Unlock()
	rt := e.mcpState()
	t := rt.tries[rt.byName[name]]
	if t == nil || time.Now().After(t.Expires) {
		return "", time.Time{}, false
	}
	return t.URL, t.Expires, true
}

// MCPLoginCancel — человек передумал.
func (e *Engine) MCPLoginCancel(name string) {
	e.mcpMu.Lock()
	defer e.mcpMu.Unlock()
	rt := e.mcpState()
	if st := rt.byName[name]; st != "" {
		delete(rt.tries, st)
		delete(rt.byName, name)
	}
}

// ---- вход: вторая половина ----

// ErrMCPState — возврат с чужим, повторным или просроченным state.
var ErrMCPState = errors.New("вход не найден или устарел: начните заново кнопкой «Войти»")

// MCPLoginFinish — сервер входа вернул человека с кодом. Возвращает имя
// сервера (пусто, если state не опознан) и ошибку. Итог видно в строке
// сервера, а не только в этом ответе: страница возврата ведёт на «Интеграции».
func (e *Engine) MCPLoginFinish(ctx context.Context, state, code, oauthErr, oauthDesc, who string) (string, error) {
	e.mcpMu.Lock()
	rt := e.mcpState()
	try := rt.tries[state]
	if state == "" || try == nil || time.Now().After(try.Expires) {
		if try != nil {
			delete(rt.tries, state)
			delete(rt.byName, try.Server)
		}
		e.mcpMu.Unlock()
		return "", ErrMCPState
	}
	if try.Who != who {
		// попытку начал другой вход: её не забираем, но и завершить не даём
		e.mcpMu.Unlock()
		return "", ErrMCPState
	}
	delete(rt.tries, state) // одноразовый: дальше он не годится ни для чего
	delete(rt.byName, try.Server)
	e.mcpMu.Unlock()

	name := try.Server
	if _, ok := e.store.MCPServer(name); !ok {
		return "", fmt.Errorf("сервера %q больше нет", name)
	}
	fail := func(err error) (string, error) {
		// неудачная попытка войти заново не отменяет вход, который ещё
		// работает: состояние остаётся прежним, меняется только примечание.
		// Причина вместе с советом должна поместиться в строку сервера, иначе
		// совет обрежется первым — он в хвосте
		state := "login"
		if e.mcpHasLogin(name) {
			if cur, ok := e.store.MCPServer(name); ok {
				state = cur.State
			}
		}
		_ = e.store.SetMCPState(name, state, "вход не завершён: "+clip(err.Error(), mcpNoteMax))
		return name, err
	}
	if oauthErr != "" {
		why := oauthErr
		if oauthDesc != "" {
			why += ": " + oauthDesc
		}
		err := fmt.Errorf("сервер входа отказал: %s", clip(why, 200))
		if oauthErr == "invalid_client" {
			// человека вернули с этой ошибкой (сервер входа иногда возвращает, а
			// иногда показывает её у себя — тогда до нас не доходит вовсе)
			err = fmt.Errorf("%w — %s", err, e.invalidClientTail(name, try))
		}
		return fail(err)
	}
	if strings.TrimSpace(code) == "" {
		return fail(fmt.Errorf("сервер входа вернул человека без кода"))
	}
	m, _ := e.store.MCPServer(name)
	_, _, netTimeout := e.MCPLoginLimits()
	cctx, cancel := context.WithTimeout(ctx, 2*netTimeout)
	defer cancel()
	now := time.Now()
	tok, err := e.loginNet(m.Name, m.URL).Exchange(cctx, try.TokenEndpoint, mcp.ExchangeParams{
		ClientID: try.ClientID, Code: code, Verifier: try.Verifier, RedirectURI: try.Redirect, Resource: try.Resource}, now)
	if err != nil {
		var te *mcp.TokenError
		if errors.As(err, &te) && te.Code == "invalid_client" {
			// повторы уже были (`mcp.Net.TokenRetries`): сервер входа не узнаёт
			// нас и после них
			err = fmt.Errorf("%w — %s", err, e.invalidClientTail(name, try))
		}
		return fail(err)
	}
	rec := &mcpTokens{Access: tok.Access, Refresh: tok.Refresh, TokenEndpoint: try.TokenEndpoint, Revocation: try.Revocation,
		ClientID: try.ClientID, Scope: firstNonEmpty(tok.Scope, try.Scope), Resource: try.Resource}
	if !tok.ExpiresAt.IsZero() {
		rec.ExpiresAt = tok.ExpiresAt.Format(time.RFC3339)
	}
	lock := e.mcpLock(name)
	lock.Lock()
	err = e.saveMCPTokens(name, rec)
	lock.Unlock()
	if err != nil {
		return fail(fmt.Errorf("токены получены, но записать их не удалось: %w", err))
	}
	e.mcpDrop(name) // старая сессия принадлежала прежнему входу
	e.CloseNotification(mcpLoginKey(name), store.NotifyDone, "вход выполнен")
	log.Printf("MCP «%s»: вход выполнен, права: %s", name, clip(rec.Scope, 120))

	// сразу вычитываем инструменты, как при обычном добавлении
	if err := e.CheckMCP(ctx, name); err != nil {
		_ = e.store.SetMCPState(name, "fail", "вход выполнен, но проверка не прошла: "+clip(err.Error(), 200))
		return name, nil
	}
	if m, ok := e.store.MCPServer(name); ok {
		_ = e.store.SetMCPState(name, "ok", fmt.Sprintf("вход выполнен, инструментов: %d", len(m.Tools)))
	}
	return name, nil
}

// ---- выход ----

// MCPLogout — «Выйти»: токены и регистрация клиента удаляются, у сервера
// входа токены отзываются (это лишь вежливость: ответ не важен).
func (e *Engine) MCPLogout(ctx context.Context, name string) error {
	m, ok := e.store.MCPServer(name)
	if !ok {
		return fmt.Errorf("сервера %q нет", name)
	}
	e.forgetMCPLogin(ctx, m, true)
	_ = e.store.SetMCPState(name, "login", "вы вышли: нужен вход")
	return nil
}

// DeleteMCPServer — удалить сервер вместе со входом.
func (e *Engine) DeleteMCPServer(ctx context.Context, name string) error {
	if m, ok := e.store.MCPServer(name); ok {
		e.forgetMCPLogin(ctx, m, true)
	}
	e.mcpDrop(name)
	e.CloseNotification(mcpLoginKey(name), store.NotifyDismissed, "сервер удалён")
	return e.store.DeleteMCPServer(name)
}

func (e *Engine) forgetMCPLogin(ctx context.Context, m *store.MCPServer, revoke bool) {
	name := m.Name
	lock := e.mcpLock(name)
	lock.Lock()
	defer lock.Unlock()
	if revoke && e.mcpHasLogin(name) {
		if t, err := e.readMCPTokens(name, "выход"); err == nil && t.Revocation != "" {
			rctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			net := e.loginNet(m.Name, m.URL)
			if t.Refresh != "" {
				_ = net.Revoke(rctx, t.Revocation, t.ClientID, t.Refresh, "refresh_token")
			}
			_ = net.Revoke(rctx, t.Revocation, t.ClientID, t.Access, "access_token")
			cancel()
		}
	}
	_ = e.store.DeleteSecret(mcpOAuthSecret(name))
	_ = e.store.DeleteMCPClients(name)
	_ = e.store.SetMCPLogin(name, nil)
	e.MCPLoginCancel(name)
	e.mcpDrop(name)
	e.CloseNotification(mcpLoginKey(name), store.NotifyDismissed, "вы вышли")
}

// ---- что видит экран ----

// MCPLoginView — состояние входа для строки сервера. Токены не читаются.
type MCPLoginView struct {
	Has        bool      // токены лежат в хранилище
	Expires    time.Time // когда кончается нынешний токен (нулевое — не названо)
	Scope      string
	Refresh    bool // есть чем продлить
	Pending    bool // вход начат, страница согласия ещё не пройдена
	PendingURL string
	Until      time.Time // до какого часа начатый вход действует
}

func (e *Engine) MCPLoginView(m *store.MCPServer) MCPLoginView {
	var v MCPLoginView
	if m.Agent != "" || m.Secret != "" {
		return v
	}
	v.Has = e.mcpHasLogin(m.Name)
	if v.Has && m.Login != nil {
		v.Scope, v.Refresh = m.Login.Scope, m.Login.Refresh
		if at, err := time.Parse(time.RFC3339, m.Login.Expires); err == nil {
			v.Expires = at
		}
	}
	v.PendingURL, v.Until, v.Pending = e.MCPLoginPending(m.Name)
	return v
}
