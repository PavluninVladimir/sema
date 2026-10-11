package mcp

// Вход OAuth 2.1 для серверов MCP (спецификация MCP Authorization,
// редакция 2025-11-25): сервер отвечает 401 и называет, где лежат метаданные
// защищаемого ресурса (RFC 9728) → по ним находим сервер авторизации
// (RFC 8414) → называем себя серверу входа: либо адресом документа клиента
// (Client ID Metadata Document — предпочтительный способ, `ClientDocument`),
// либо динамической регистрацией (RFC 7591) → идём за кодом с PKCE (S256) и
// параметром resource (RFC 8707) → меняем код на токены и потом продлеваем
// их по refresh_token.
//
// Здесь только протокол: пакет ничего не хранит и никуда, кроме
// переданного ему сетевого клиента, не ходит. Где лежат токены, чей это
// вход и кому показать кнопку — дело движка.
//
// Адреса, которые сервер сам называет в ответах (метаданные, регистрация,
// выдача токенов), — чужие данные: сервер, которому мы поверили на слово,
// мог бы отправить нас на внутренний адрес. Поэтому каждый такой адрес
// проходит `Net.Check` до запроса.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Challenge — что сервер сказал заголовком WWW-Authenticate на 401.
type Challenge struct {
	Realm            string
	ResourceMetadata string // адрес метаданных ресурса (RFC 9728)
	Scope            string // права через пробел
	Error            string // invalid_token, insufficient_scope…
	Description      string
}

var reBearer = regexp.MustCompile(`(?i)(?:^|,)\s*bearer(?:\s+|$)`)

// ParseChallenge разбирает значения заголовков WWW-Authenticate и
// возвращает вызов схемы Bearer; nil — Bearer не предложен.
func ParseChallenge(values []string) *Challenge {
	for _, v := range values {
		loc := reBearer.FindStringIndex(v)
		if loc == nil {
			continue
		}
		p := parseAuthParams(v[loc[1]:])
		return &Challenge{Realm: p["realm"], ResourceMetadata: p["resource_metadata"], Scope: p["scope"],
			Error: p["error"], Description: p["error_description"]}
	}
	return nil
}

// parseAuthParams — `key=value, key="quoted \"value\""`. Кончается на
// первом слове без «=»: дальше идёт следующая схема.
func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',' || s[i] == '\t') {
			i++
		}
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ' ' && s[i] != ',' {
			i++
		}
		key := strings.ToLower(s[start:i])
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if key == "" || i >= len(s) || s[i] != '=' {
			break
		}
		i++
		for i < len(s) && s[i] == ' ' {
			i++
		}
		var val strings.Builder
		if i < len(s) && s[i] == '"' {
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
			i++
		} else {
			for i < len(s) && s[i] != ',' && s[i] != ' ' {
				val.WriteByte(s[i])
				i++
			}
		}
		out[key] = val.String()
	}
	return out
}

// Net — сетевые правила запросов входа.
type Net struct {
	HTTP *http.Client
	// Check решает, можно ли ходить по найденному адресу; nil — можно
	// куда угодно. Зовётся перед каждым запросом по адресу, который назвал
	// сервер, а не человек.
	Check func(*url.URL) error
	// TokenRetries — сколько раз повторить обмен кода и продление, если
	// сервер выдач ответил invalid_client (0 — не повторять). Сервер входа
	// за балансировщиком отвечает на такой запрос по-разному: клиента,
	// которого зарегистрировали на одном его узле, другой узел не знает, и
	// повтор попадает уже на другой. Остальные ошибки не повторяются.
	TokenRetries int
	// TokenRetryPause — пауза между повторами (0 — без паузы).
	TokenRetryPause time.Duration
	// Note принимает одну строку итога, если пришлось повторять; в неё не
	// попадает ни токен, ни код. nil — молчим.
	Note func(string)
}

func (n Net) allow(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("адрес %q не разбирается: нужен вид https://…", clip(raw, 100))
	}
	if n.Check != nil {
		if err := n.Check(u); err != nil {
			return nil, fmt.Errorf("адрес %s не годится для входа: %w", u.Host, err)
		}
	}
	return u, nil
}

func (n Net) client() *http.Client {
	if n.HTTP != nil {
		return n.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// getJSON — GET с разбором JSON. Ответ не 200 — ошибка с кодом.
func (n Net) getJSON(ctx context.Context, raw string, out any) error {
	u, err := n.allow(raw)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := n.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s ответил HTTP %d", u.Host, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: ответ не разобран: %s", u.Host, clip(string(body), 120))
	}
	return nil
}

// ResourceMeta — метаданные защищаемого ресурса (RFC 9728).
type ResourceMeta struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// ServerMeta — метаданные сервера авторизации (RFC 8414 / OIDC).
type ServerMeta struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	GrantTypes            []string `json:"grant_types_supported"`
	AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported       []string `json:"scopes_supported"`
	// ClientMetadataDocument — сервер принимает клиента по адресу его
	// документа (`client_id_metadata_document_supported`): регистрироваться
	// у него не нужно.
	ClientMetadataDocument bool `json:"client_id_metadata_document_supported"`
}

// Discovery — всё, что нужно знать, чтобы начать вход.
type Discovery struct {
	Resource    string // что писать в параметр resource (RFC 8707)
	Scopes      []string
	Server      ServerMeta
	Fallback    bool   // метаданных ресурса не нашлось: сервер авторизации — сам сервер MCP
	MetadataURL string // откуда взяли метаданные ресурса
}

// Discover идёт по цепочке обнаружения от адреса сервера MCP и его вызова
// 401. ch может быть nil: тогда метаданные ищутся по известным путям.
func (n Net) Discover(ctx context.Context, mcpURL string, ch *Challenge) (*Discovery, error) {
	mu, err := url.Parse(mcpURL)
	if err != nil || mu.Host == "" {
		return nil, fmt.Errorf("адрес сервера %q не разбирается", mcpURL)
	}
	origin := mu.Scheme + "://" + mu.Host
	path := strings.TrimRight(mu.Path, "/")

	var candidates []string
	if ch != nil && ch.ResourceMetadata != "" {
		// адрес назвал сам сервер: не годится — это не повод молча искать
		// в другом месте, а повод сказать об этом
		if _, err := n.allow(ch.ResourceMetadata); err != nil {
			return nil, fmt.Errorf("сервер указал метаданные входа по адресу, куда Сёма не ходит: %w", err)
		}
		candidates = append(candidates, ch.ResourceMetadata)
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+path)
	if path != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")
	}
	var rm ResourceMeta
	found, metaURL := false, ""
	var lastErr error
	for _, c := range candidates {
		var m ResourceMeta
		if err := n.getJSON(ctx, c, &m); err != nil {
			lastErr = err
			continue
		}
		rm, found, metaURL = m, true, c
		break
	}
	d := &Discovery{MetadataURL: metaURL}
	issuer := ""
	if found && len(rm.AuthorizationServers) > 0 {
		issuer = rm.AuthorizationServers[0]
	} else {
		// прежняя редакция протокола: сервер авторизации — тот же адрес
		issuer, d.Fallback = origin, true
		if ch != nil && ch.ResourceMetadata != "" && !found {
			return nil, fmt.Errorf("сервер указал метаданные входа, но прочитать их не вышло: %v", lastErr)
		}
	}
	// Resource: то, что назвал сам сервер, если это тот же адрес; иначе — адрес сервера.
	d.Resource = canonicalResource(mcpURL)
	if rm.Resource != "" {
		if ru, err := url.Parse(rm.Resource); err == nil && ru.Host != "" &&
			strings.EqualFold(ru.Scheme, mu.Scheme) && strings.EqualFold(ru.Host, mu.Host) {
			d.Resource = strings.TrimRight(ru.String(), "?#")
		}
	}
	d.Scopes = rm.ScopesSupported

	meta, err := n.serverMeta(ctx, issuer)
	if err != nil {
		return nil, err
	}
	d.Server = *meta
	if len(d.Scopes) == 0 {
		d.Scopes = meta.ScopesSupported
	}
	return d, nil
}

// canonicalResource — адрес сервера MCP в виде, который RFC 8707 просит
// писать в resource: схема и хост в нижнем регистре, без фрагмента.
func canonicalResource(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme, u.Host, u.Fragment = strings.ToLower(u.Scheme), strings.ToLower(u.Host), ""
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String()
}

// serverMeta ищет метаданные сервера авторизации: сначала RFC 8414, потом
// OpenID Connect.
func (n Net) serverMeta(ctx context.Context, issuer string) (*ServerMeta, error) {
	iu, err := url.Parse(issuer)
	if err != nil || iu.Host == "" {
		return nil, fmt.Errorf("адрес сервера входа %q не разбирается", clip(issuer, 100))
	}
	origin := iu.Scheme + "://" + iu.Host
	path := strings.TrimRight(iu.Path, "/")
	candidates := []string{
		origin + "/.well-known/oauth-authorization-server" + path,
		origin + "/.well-known/openid-configuration" + path,
	}
	if path != "" {
		candidates = append(candidates, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration")
	}
	var lastErr error
	for _, c := range candidates {
		var m ServerMeta
		if err := n.getJSON(ctx, c, &m); err != nil {
			lastErr = err
			continue
		}
		if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
			lastErr = fmt.Errorf("%s: в метаданных нет authorization_endpoint или token_endpoint", iu.Host)
			continue
		}
		if m.Issuer != "" && strings.TrimRight(m.Issuer, "/") != strings.TrimRight(issuer, "/") {
			lastErr = fmt.Errorf("%s: метаданные принадлежат другому серверу (%s)", iu.Host, clip(m.Issuer, 80))
			continue
		}
		if len(m.CodeChallengeMethods) > 0 && !contains(m.CodeChallengeMethods, "S256") {
			return nil, fmt.Errorf("сервер входа %s не умеет PKCE S256, а без него Сёма не входит", iu.Host)
		}
		for _, ep := range []string{m.AuthorizationEndpoint, m.TokenEndpoint, m.RegistrationEndpoint, m.RevocationEndpoint} {
			if ep == "" {
				continue
			}
			if _, err := n.allow(ep); err != nil {
				return nil, err
			}
		}
		return &m, nil
	}
	return nil, fmt.Errorf("метаданные сервера входа %s не найдены: %v", iu.Host, lastErr)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Register регистрирует Сёму как публичного клиента (RFC 7591) и
// возвращает client_id.
func (n Net) Register(ctx context.Context, endpoint, redirectURI, clientName string) (string, error) {
	u, err := n.allow(endpoint)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := n.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		ClientID    string `json:"client_id"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.ClientID == "" {
		why := out.Description
		if why == "" {
			why = out.Error
		}
		if why == "" {
			why = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return "", fmt.Errorf("сервер входа не принял регистрацию Сёмы: %s", clip(why, 200))
	}
	return out.ClientID, nil
}

// ClientDocument — документ клиента (OAuth Client ID Metadata Document,
// draft-ietf-oauth-client-id-metadata-document): `client_id` — это адрес
// самого документа, а сервер входа скачивает его сам и сверяет адрес
// возврата. Ничего не хранится ни у него, ни у нас, поэтому любой узел
// сервера входа знает клиента, — в отличие от регистрации, где клиента
// помнит только тот узел, куда она попала.
//
// Документ публичный и отдаётся без входа, поэтому в нём только то, что
// сервер входа показывает человеку на странице согласия: имя, адрес Сёмы и
// адрес возврата. Секретов и внутренних адресов нет.
type ClientDocument struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// NewClientDocument собирает документ. clientID — адрес, по которому он
// будет лежать (совпадение байт в байт — главное требование), clientURI —
// адрес самой Сёмы.
func NewClientDocument(clientID, clientURI, name string, redirects []string) ClientDocument {
	return ClientDocument{
		ClientID: clientID, ClientName: name, ClientURI: clientURI, RedirectURIs: redirects,
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none", // публичный клиент: секрета у Сёмы нет
	}
}

// CheckClientIDURL — годится ли адрес в client_id по черновику: только
// https, есть путь, нет учётных данных, фрагмента, запроса и `.`/`..` в пути.
// Запрос черновик лишь не советует, но нам он ни к чему, а строже — надёжнее.
func CheckClientIDURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("адрес %q не разбирается", clip(raw, 100))
	}
	switch {
	case u.Scheme != "https":
		return fmt.Errorf("в client_id годится только https")
	case u.User != nil:
		return fmt.Errorf("в client_id не бывает имени и пароля")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return fmt.Errorf("в client_id не бывает фрагмента")
	case u.RawQuery != "" || strings.Contains(raw, "?"):
		return fmt.Errorf("в client_id Сёма не пишет запрос")
	case u.Path == "" || u.Path == "/":
		return fmt.Errorf("в client_id нужен путь к документу")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("в пути client_id не бывает «.» и «..»")
		}
	}
	return nil
}

// PKCE — пара проверочного кода: verifier остаётся у нас, challenge уходит
// на сервер входа.
func NewPKCE() (verifier, challenge string) {
	verifier = randomString(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomState — одноразовый state для одной попытки входа.
func RandomState() string { return randomString(24) }

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("нет случайных чисел: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthorizeParams — что идёт в адрес страницы согласия.
type AuthorizeParams struct {
	ClientID    string
	RedirectURI string
	State       string
	Challenge   string
	Resource    string
	Scope       string
}

// AuthorizeURL строит адрес, который человек открывает в браузере.
func AuthorizeURL(endpoint string, p AuthorizeParams) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("адрес страницы входа %q не разбирается", clip(endpoint, 100))
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("state", p.State)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	if p.Resource != "" {
		q.Set("resource", p.Resource)
	}
	if p.Scope != "" {
		q.Set("scope", p.Scope)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Token — выданные токены.
type Token struct {
	Access    string
	Refresh   string
	Scope     string
	ExpiresAt time.Time // нулевое — срок не назван
}

// TokenError — отказ сервера выдач токенов. Текст ответа целиком не
// хранится: в нём могло оказаться чужое значение, а нужны только код и
// пояснение самого сервера.
type TokenError struct {
	Status      int
	Code        string
	Description string
	Tries       int // сколько раз спрашивали, пока не сдались (0 и 1 — один)
}

func (e *TokenError) Error() string {
	why := e.Code
	if e.Description != "" {
		why += ": " + e.Description
	}
	if why == "" {
		why = "HTTP " + strconv.Itoa(e.Status)
	}
	if e.Tries > 1 {
		why += fmt.Sprintf(" (попыток подряд: %d)", e.Tries)
	}
	return "сервер входа отказал: " + clip(why, 200)
}

// Definitive — отказ окончательный: токен мёртв или клиент неизвестен.
// Всё остальное (сеть, 5xx, «попробуйте позже») вход не отменяет.
func (e *TokenError) Definitive() bool {
	switch e.Code {
	case "invalid_grant", "invalid_client", "unauthorized_client", "invalid_token", "access_denied":
		return true
	}
	return false
}

// ExchangeParams — обмен кода на токены.
type ExchangeParams struct {
	ClientID    string
	Code        string
	Verifier    string
	RedirectURI string
	Resource    string
}

// Exchange меняет код на токены.
func (n Net) Exchange(ctx context.Context, tokenEndpoint string, p ExchangeParams, now time.Time) (*Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {p.Code},
		"redirect_uri":  {p.RedirectURI},
		"client_id":     {p.ClientID},
		"code_verifier": {p.Verifier},
	}
	if p.Resource != "" {
		form.Set("resource", p.Resource)
	}
	return n.tokenRetrying(ctx, "обмен кода", tokenEndpoint, form, now)
}

// Refresh продлевает вход. Сервер может выдать новый refresh_token
// (ротация) — тогда старый умирает; если не выдал, остаётся прежний.
func (n Net) Refresh(ctx context.Context, tokenEndpoint, clientID, refresh, resource string, now time.Time) (*Token, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {clientID},
	}
	if resource != "" {
		form.Set("resource", resource)
	}
	t, err := n.tokenRetrying(ctx, "продление входа", tokenEndpoint, form, now)
	if err != nil {
		return nil, err
	}
	if t.Refresh == "" {
		t.Refresh = refresh
	}
	return t, nil
}

// tokenRetrying — запрос к серверу выдач токенов с повтором на invalid_client.
//
// Сервер входа за балансировщиком отвечает на один и тот же запрос то
// выдачей, то «клиент неизвестен»: динамическую регистрацию помнит только
// тот узел, куда она попала. Повтор через секунду чаще всего попадает уже на
// другой узел. Повторяется только invalid_client — он не тратит ни код, ни
// refresh_token; остальные отказы (в том числе «код уже использован») и сетевые
// ошибки возвращаются сразу. Итог, если повторы были, — одной строкой в Note.
func (n Net) tokenRetrying(ctx context.Context, what, endpoint string, form url.Values, now time.Time) (*Token, error) {
	attempts := 1 + max(n.TokenRetries, 0)
	for i := 1; ; i++ {
		tok, err := n.token(ctx, endpoint, form, now)
		var te *TokenError
		if err != nil && (!errors.As(err, &te) || te.Code != "invalid_client") {
			return nil, err
		}
		if err == nil {
			if i > 1 {
				n.note("%s: сервер входа отвечал invalid_client %d раз подряд, попытка %d прошла", what, i-1, i)
			}
			return tok, nil
		}
		if i >= attempts {
			te.Tries = i
			if i > 1 {
				n.note("%s: invalid_client на всех %d попытках, сдаёмся", what, i)
			}
			return nil, te
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(n.TokenRetryPause):
		}
	}
}

func (n Net) note(format string, args ...any) {
	if n.Note != nil {
		n.Note(fmt.Sprintf(format, args...))
	}
}

func (n Net) token(ctx context.Context, endpoint string, form url.Values, now time.Time) (*Token, error) {
	u, err := n.allow(endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := n.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Access      string      `json:"access_token"`
		Type        string      `json:"token_type"`
		ExpiresIn   json.Number `json:"expires_in"`
		Refresh     string      `json:"refresh_token"`
		Scope       string      `json:"scope"`
		Error       string      `json:"error"`
		Description string      `json:"error_description"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	_ = dec.Decode(&out)
	if resp.StatusCode/100 != 2 {
		return nil, &TokenError{Status: resp.StatusCode, Code: out.Error, Description: out.Description}
	}
	if out.Access == "" {
		return nil, &TokenError{Status: resp.StatusCode, Code: "no_access_token", Description: "в ответе нет access_token"}
	}
	if out.Type != "" && !strings.EqualFold(out.Type, "bearer") {
		return nil, &TokenError{Status: resp.StatusCode, Code: "unsupported_token_type", Description: "тип токена " + clip(out.Type, 30) + ", Сёма понимает только Bearer"}
	}
	t := &Token{Access: out.Access, Refresh: out.Refresh, Scope: out.Scope}
	if secs, err := out.ExpiresIn.Int64(); err == nil && secs > 0 {
		t.ExpiresAt = now.Add(time.Duration(secs) * time.Second)
	}
	return t, nil
}

// Revoke просит сервер входа отозвать токен (RFC 7009). Ответ не важен:
// вход у нас снимается в любом случае.
func (n Net) Revoke(ctx context.Context, endpoint, clientID, token, hint string) error {
	u, err := n.allow(endpoint)
	if err != nil {
		return err
	}
	form := url.Values{"token": {token}, "client_id": {clientID}, "token_type_hint": {hint}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := n.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// Probe спрашивает сервер без входа и возвращает его вызов: 401 с
// WWW-Authenticate — значит, вход нужен и вот откуда его начинать.
// Второе значение — код ответа (0, если ответа не было).
func Probe(ctx context.Context, hc *http.Client, mcpURL string, headers map[string]string) (*Challenge, int, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + ProtocolVersion +
		`","capabilities":{},"clientInfo":{"name":"sema","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, strings.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusUnauthorized {
		return ParseChallenge(resp.Header.Values("WWW-Authenticate")), resp.StatusCode, nil
	}
	return nil, resp.StatusCode, nil
}
