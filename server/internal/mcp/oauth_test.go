package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
)

// Обнаружение по цепочке «401 → метаданные ресурса → сервер входа»: как у
// Planner, где ресурс и сервер входа стоят на разных адресах.
func TestDiscoverChain(t *testing.T) {
	fake := mcptest.New(t, false)
	n := Net{}
	ch := &Challenge{ResourceMetadata: fake.MCP.URL + "/.well-known/oauth-protected-resource/mcp"}
	d, err := n.Discover(context.Background(), fake.URL(), ch)
	if err != nil {
		t.Fatal(err)
	}
	if d.Resource != fake.URL() {
		t.Errorf("resource %q", d.Resource)
	}
	if d.Server.AuthorizationEndpoint != fake.Auth.URL+"/oauth/authorize" || d.Server.TokenEndpoint != fake.Auth.URL+"/oauth/token" ||
		d.Server.RegistrationEndpoint != fake.Auth.URL+"/oauth/register" {
		t.Errorf("метаданные сервера входа: %+v", d.Server)
	}
	if strings.Join(d.Scopes, " ") != "tasks:read tasks:write mcp:read" {
		t.Errorf("права: %v", d.Scopes)
	}
	// без заголовка вызова — по известным путям
	if d2, err := n.Discover(context.Background(), fake.URL(), nil); err != nil || d2.Server.TokenEndpoint != d.Server.TokenEndpoint {
		t.Errorf("обнаружение без заголовка: %v", err)
	}
}

// Запасные пути: openid-configuration, если oauth-authorization-server нет;
// сервер входа — сам сервер MCP, если метаданных ресурса нет.
func TestDiscoverFallbacks(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.OpenIDOnly = true
	fake.NoResourceMeta = true
	d, err := Net{}.Discover(context.Background(), fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Fallback || d.Server.TokenEndpoint != fake.Auth.URL+"/oauth/token" {
		t.Errorf("запасной путь не сработал: %+v", d)
	}
}

// Сервер без S256 — отказ: без PKCE Сёма не входит.
func TestDiscoverRefusesWithoutS256(t *testing.T) {
	fake := mcptest.New(t, true)
	fake.NoPKCE = true
	_, err := Net{}.Discover(context.Background(), fake.URL(), nil)
	if err == nil || !strings.Contains(err.Error(), "S256") {
		t.Fatalf("ждали отказ про S256, получили %v", err)
	}
}

// Найденные адреса — чужие данные: проходят Check до запроса. Метаданные
// ресурса, отправляющие на внутренний адрес, не читаются.
func TestDiscoverChecksFoundAddresses(t *testing.T) {
	var hit atomic.Bool
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer inner.Close()
	fake := mcptest.New(t, true)
	home, _ := url.Parse(fake.MCP.URL)
	n := Net{Check: func(u *url.URL) error {
		if u.Host != home.Host {
			return fmt.Errorf("частный адрес")
		}
		return nil
	}}
	ch := &Challenge{ResourceMetadata: inner.URL + "/.well-known/oauth-protected-resource"}
	if _, err := n.Discover(context.Background(), fake.URL(), ch); err == nil {
		t.Error("метаданные с внутреннего адреса прочитаны")
	}
	if hit.Load() {
		t.Error("запрос дошёл до внутреннего адреса")
	}
}

func TestRegisterBodyAndErrors(t *testing.T) {
	fake := mcptest.New(t, true)
	id, err := Net{}.Register(context.Background(), fake.Auth.URL+"/oauth/register", "https://sema.test/mcp/oauth/callback", "Сёма")
	if err != nil || id != "cid-1" {
		t.Fatalf("регистрация: %q, %v", id, err)
	}
	body := fake.Registrations()[0]
	if body["client_name"] != "Сёма" || body["token_endpoint_auth_method"] != "none" {
		t.Errorf("тело регистрации: %v", body)
	}
	if uris, _ := body["redirect_uris"].([]any); len(uris) != 1 || uris[0] != "https://sema.test/mcp/oauth/callback" {
		t.Errorf("redirect_uris: %v", body["redirect_uris"])
	}
	if gt, _ := body["grant_types"].([]any); len(gt) != 2 {
		t.Errorf("grant_types: %v", body["grant_types"])
	}
	if rt, _ := body["response_types"].([]any); len(rt) != 1 || rt[0] != "code" {
		t.Errorf("response_types: %v", body["response_types"])
	}

	// отказ сервера называется по-человечески, а не молчанием
	fake.RegisterError = "redirect_uri должен быть https"
	_, err = Net{}.Register(context.Background(), fake.Auth.URL+"/oauth/register", "http://sema.local/mcp/oauth/callback", "Сёма")
	if err == nil || !strings.Contains(err.Error(), "redirect_uri должен быть https") {
		t.Errorf("отказ регистрации: %v", err)
	}
}

func TestAuthorizeURL(t *testing.T) {
	v, c := NewPKCE()
	if len(v) < 43 || v == c {
		t.Errorf("verifier %q, challenge %q", v, c)
	}
	v2, _ := NewPKCE()
	if v == v2 || RandomState() == RandomState() {
		t.Error("случайные значения повторились")
	}
	link, err := AuthorizeURL("https://as.test/oauth/authorize?tenant=1", AuthorizeParams{
		ClientID: "cid", RedirectURI: "https://sema.test/mcp/oauth/callback", State: "st", Challenge: c,
		Resource: "https://mcp.test/mcp", Scope: "a b"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	for k, want := range map[string]string{"response_type": "code", "client_id": "cid", "state": "st", "code_challenge": c,
		"code_challenge_method": "S256", "resource": "https://mcp.test/mcp", "scope": "a b", "tenant": "1",
		"redirect_uri": "https://sema.test/mcp/oauth/callback"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, ждали %q", k, q.Get(k), want)
		}
	}
}

// Обмен кода, обновление с ротацией и виды отказов.
func TestTokenExchangeAndRefresh(t *testing.T) {
	fake := mcptest.New(t, true)
	ctx := context.Background()
	n := Net{}
	regID, _ := n.Register(ctx, fake.Auth.URL+"/oauth/register", "https://sema.test/cb", "Сёма")
	v, ch := NewPKCE()
	link, _ := AuthorizeURL(fake.Auth.URL+"/oauth/authorize", AuthorizeParams{ClientID: regID, RedirectURI: "https://sema.test/cb",
		State: "s", Challenge: ch, Resource: fake.URL(), Scope: "tasks:read"})
	code, _, err := fake.Approve(link)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok, err := n.Exchange(ctx, fake.Auth.URL+"/oauth/token", ExchangeParams{ClientID: regID, Code: code, Verifier: v,
		RedirectURI: "https://sema.test/cb", Resource: fake.URL()}, now)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "at-1" || tok.Refresh != "rt-1" || tok.ExpiresAt.Sub(now) != time.Hour {
		t.Errorf("токены: %+v", tok)
	}
	// код одноразовый
	if _, err := n.Exchange(ctx, fake.Auth.URL+"/oauth/token", ExchangeParams{ClientID: regID, Code: code, Verifier: v,
		RedirectURI: "https://sema.test/cb", Resource: fake.URL()}, now); err == nil {
		t.Error("код принят второй раз")
	}
	// чужой verifier не проходит PKCE
	code2, _, _ := fake.Approve(link)
	_, err = n.Exchange(ctx, fake.Auth.URL+"/oauth/token", ExchangeParams{ClientID: regID, Code: code2, Verifier: "чужой",
		RedirectURI: "https://sema.test/cb", Resource: fake.URL()}, now)
	var te *TokenError
	if !errors.As(err, &te) || te.Code != "invalid_grant" || !te.Definitive() {
		t.Errorf("PKCE не сработал: %v", err)
	}

	// обновление: новый refresh_token заменяет старый
	fresh, err := n.Refresh(ctx, fake.Auth.URL+"/oauth/token", regID, tok.Refresh, fake.URL(), now)
	if err != nil || fresh.Access == tok.Access || fresh.Refresh == tok.Refresh {
		t.Fatalf("обновление: %+v, %v", fresh, err)
	}
	if _, err := n.Refresh(ctx, fake.Auth.URL+"/oauth/token", regID, tok.Refresh, fake.URL(), now); !errors.As(err, &te) || !te.Definitive() {
		t.Errorf("старый refresh_token жив после ротации: %v", err)
	}
	// временная беда сервера — не окончательный отказ
	fake.RefreshTransient = true
	if _, err := n.Refresh(ctx, fake.Auth.URL+"/oauth/token", regID, fresh.Refresh, fake.URL(), now); !errors.As(err, &te) || te.Definitive() {
		t.Errorf("500 принят за окончательный отказ: %v", err)
	}
	// текст отказа не несёт значений из ответа
	if strings.Contains(te.Error(), fresh.Refresh) {
		t.Error("refresh_token попал в текст ошибки")
	}
}

// Сервер, не называющий срок, — токен без срока; ответ без refresh_token при
// обновлении оставляет прежний.
func TestRefreshKeepsOldRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-at","token_type":"bearer","expires_in":"120"}`))
	}))
	defer srv.Close()
	tok, err := Net{}.Refresh(context.Background(), srv.URL, "c", "old-rt", "", time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "new-at" || tok.Refresh != "old-rt" || tok.ExpiresAt != time.Unix(1120, 0) {
		t.Errorf("токен: %+v", tok)
	}
}

// Повтор на invalid_client: сервер за балансировщиком отвечает через раз, и
// повтор попадает на другой узел. Повторяется только он; пауза учитывает
// отмену; итог — одна строка без значений.
func TestTokenRetriesInvalidClient(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	failFirst := int32(2)
	code, status := "invalid_client", http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) <= failFirst {
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error":%q,"error_description":"Invalid client configuration"}`, code)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer","refresh_token":"rt"}`))
	}))
	defer srv.Close()
	var notes []string
	n := Net{TokenRetries: 3, TokenRetryPause: time.Millisecond, Note: func(s string) { notes = append(notes, s) }}
	p := ExchangeParams{ClientID: "c", Code: "secret-code", Verifier: "v", RedirectURI: "https://s/cb"}

	// два отказа подряд — и выдача; в журнале одна строка без кода и токенов
	if tok, err := n.Exchange(ctx, srv.URL, p, time.Now()); err != nil || tok.Access != "at" || calls.Load() != 3 {
		t.Fatalf("после двух отказов: %+v, %v, запросов %d", tok, err, calls.Load())
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "invalid_client") || strings.Contains(notes[0], "secret-code") || strings.Contains(notes[0], "access_token") {
		t.Errorf("строка итога: %q", notes)
	}

	// четыре подряд — ошибка с числом попыток, ровно 1+3 запроса
	calls.Store(0)
	failFirst, notes = 100, nil
	_, err := n.Exchange(ctx, srv.URL, p, time.Now())
	var te *TokenError
	if !errors.As(err, &te) || te.Code != "invalid_client" || te.Tries != 4 || calls.Load() != 4 || !strings.Contains(err.Error(), "попыток подряд: 4") {
		t.Fatalf("четвёртый отказ подряд: %v, запросов %d", err, calls.Load())
	}
	if !te.Definitive() || len(notes) != 1 {
		t.Errorf("окончательность %v, строк итога %d", te.Definitive(), len(notes))
	}

	// другой отказ — сразу, без повторов
	calls.Store(0)
	code, status = "invalid_grant", http.StatusBadRequest
	if _, err := n.Exchange(ctx, srv.URL, p, time.Now()); err == nil || calls.Load() != 1 {
		t.Errorf("invalid_grant повторялся: %v, запросов %d", err, calls.Load())
	}

	// повторы выключены
	calls.Store(0)
	code, status = "invalid_client", http.StatusUnauthorized
	if _, err := (Net{}).Refresh(ctx, srv.URL, "c", "rt", "", time.Now()); err == nil || calls.Load() != 1 {
		t.Errorf("без повторов: %v, запросов %d", err, calls.Load())
	}

	// отмена во время паузы не ждёт конца паузы
	calls.Store(0)
	slow := Net{TokenRetries: 3, TokenRetryPause: time.Minute}
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := slow.Exchange(cctx, srv.URL, p, time.Now()); err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("пауза не прервалась отменой: %v за %v", err, time.Since(start))
	}
}

// Документ клиента: то, что просит черновик и MCP Authorization 2025-11-25.
func TestClientDocumentShape(t *testing.T) {
	doc := NewClientDocument("https://sema.example.org/mcp/oauth/client.json", "https://sema.example.org", "Сёма",
		[]string{"https://sema.example.org/mcp/oauth/callback"})
	if doc.ClientID == "" || doc.ClientName == "" || len(doc.RedirectURIs) == 0 {
		t.Fatalf("нет обязательных полей: %+v", doc)
	}
	if doc.TokenEndpointAuthMethod != "none" || strings.Join(doc.GrantTypes, " ") != "authorization_code refresh_token" ||
		strings.Join(doc.ResponseTypes, " ") != "code" {
		t.Errorf("документ: %+v", doc)
	}
	// сервер входа сообщает о поддержке документов этим полем метаданных
	fake := mcptest.New(t, true)
	fake.CIMD = true
	d, err := Net{}.Discover(context.Background(), fake.URL(), nil)
	if err != nil || !d.Server.ClientMetadataDocument {
		t.Errorf("client_id_metadata_document_supported не прочитан: %+v, %v", d, err)
	}
}
