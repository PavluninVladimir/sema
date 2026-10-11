package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Документ клиента для серверов входа OAuth (`/mcp/oauth/client.json`):
// единственная открытая без входа ручка раздела «Интеграции».

const publicSema = "sema.example.org"

// stranger — запрос чужого сервера входа: публичный адрес, ни сессии, ни
// токена, ни заголовков оболочки.
func stranger(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "198.51.100.7:5555"
	return r
}

func TestMCPClientDocumentIsPublic(t *testing.T) {
	s, st := testServer(t)
	// вход обязателен всем: «своя сеть без входа» здесь не помогает
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	docURL := "https://" + publicSema + engine.MCPClientDocPath

	// публичного адреса нет — документа нет, а не документ с внутренним адресом
	for _, origin := range []string{"", "http://localhost:8765", "https://sema.local:8443"} {
		_ = st.SetSetting("auth.origin", origin)
		if w := serve(s, stranger("GET", docURL)); w.Code != http.StatusNotFound {
			t.Errorf("auth.origin=%q: %d, ждали 404", origin, w.Code)
		}
	}

	_ = st.SetSetting("auth.origin", "https://"+publicSema)
	w := serve(s, stranger("GET", docURL))
	if w.Code != http.StatusOK {
		t.Fatalf("документ без сессии и без токена: %d\n%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") || strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control %q: сервер входа вправе кэшировать документ", cc)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("заголовки безопасности protect не дошли до открытой ручки")
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("не JSON: %v", err)
	}
	// главное требование: client_id совпадает с адресом документа байт в байт
	if doc["client_id"] != docURL {
		t.Errorf("client_id %v ≠ адрес, по которому документ скачали, %q", doc["client_id"], docURL)
	}
	if uris, _ := json.Marshal(doc["redirect_uris"]); string(uris) != `["https://`+publicSema+`/mcp/oauth/callback"]` {
		t.Errorf("redirect_uris %s", uris)
	}
	if doc["client_name"] != "Сёма" || doc["client_uri"] != "https://"+publicSema || doc["token_endpoint_auth_method"] != "none" {
		t.Errorf("документ: %v", doc)
	}
	for _, key := range []string{"client_secret", "jwks", "jwks_uri"} {
		if _, has := doc[key]; has {
			t.Errorf("в публичном документе есть %s", key)
		}
	}

	// через ручку нельзя вытянуть ничего, кроме этого документа: запрос,
	// параметры, чужое имя хоста и заголовки на ответ не влияют
	same := w.Body.Bytes()
	for _, r := range []*http.Request{
		stranger("GET", docURL+"?client_id=https://evil.test/x&redirect_uris=https://evil.test/cb&format=debug"),
		stranger("GET", "https://attacker.invalid"+engine.MCPClientDocPath),
		func() *http.Request {
			r := stranger("GET", docURL)
			r.Header.Set("X-Forwarded-Host", "attacker.invalid")
			r.Header.Set("Origin", "https://attacker.invalid")
			return r
		}(),
	} {
		got := serve(s, r)
		if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), same) {
			t.Errorf("%s: ответ зависит от запроса: %d\n%s", r.URL.String(), got.Code, got.Body.String())
		}
	}
	// писать в ручку нельзя
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		if w := serve(s, stranger(method, docURL)); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d, ждали 405", method, w.Code)
		}
	}
	if w := serve(s, stranger("HEAD", docURL)); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d, тело %d байт", w.Code, w.Body.Len())
	}
	// и ничего рядом не открыто
	for _, p := range []string{"/mcp/oauth/client.json/", "/mcp/oauth/client.jsonx", "/mcp/oauth/", "/mcp/oauth/client.json/.."} {
		if w := serve(s, stranger("GET", "https://"+publicSema+p)); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, ждали 401", p, w.Code)
		}
	}
}

// Открытых без входа путей — ровно перечисленные. Всё, что вешается на mux,
// проходит через `protect`, и открыть его можно только строкой в `openPath`;
// этот тест перечисляет такие строки и падает, когда появилась новая: каждая
// открытая дверь — решение, а не побочный эффект.
func TestOpenPathsAreListed(t *testing.T) {
	open := map[string]bool{
		"/api/health": true, "/login": true, "/api/agents/enroll": true, "/api/auth/state": true,
		"/api/auth/register/begin": true, engine.MCPClientDocPath: true,
		engine.SharePath + "1": true, // ссылка на файл: доступ даёт подпись
	}
	for p := range open {
		if !openPath(p) {
			t.Errorf("%s должен быть открыт", p)
		}
	}
	closed := []string{"/api/ui", "/api/action", "/api/files", "/api/files/1", "/api/ws", "/api/push/key",
		"/ui", "/do", "/upload", messagePath + "/1", engine.MCPCallbackPath, "/mcp", "/mcp/oauth", "/mcp/oauth/",
		engine.MCPClientDocPath + "/", engine.MCPClientDocPath + "x", "/api/agents", "/api/memory/session_start",
		engine.SharePath, "/share"}
	for _, p := range closed {
		if openPath(p) {
			t.Errorf("%s открыт без входа", p)
		}
	}

	// все пути, которые сервер вешает на mux, — из исходников: новая ручка, у
	// которой в openPath появилась строка, обязана попасть и в список выше
	re := regexp.MustCompile(`Handle(?:Func)?\("(?:GET |POST )?(/[^"]*)"`)
	reConst := regexp.MustCompile(`Handle(?:Func)?\((?:"(?:GET |POST )"\+)?engine\.(\w+)`)
	consts := map[string]string{"MCPCallbackPath": engine.MCPCallbackPath, "MCPClientDocPath": engine.MCPClientDocPath,
		"SharePath": engine.SharePath + "1"}
	wild := regexp.MustCompile(`\{[^}]*\}`)
	seen := 0
	for _, file := range []string{"server.go", "auth.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			paths = append(paths, wild.ReplaceAllString(m[1], "1"))
		}
		for _, m := range reConst.FindAllStringSubmatch(string(src), -1) {
			if p, ok := consts[m[1]]; ok {
				paths = append(paths, p)
			}
		}
		for _, p := range paths {
			seen++
			if openPath(p) && !open[p] && !strings.HasPrefix(p, "/api/auth/") {
				t.Errorf("%s: путь открыт без входа, но в перечне открытых его нет", p)
			}
			if strings.HasPrefix(p, "/api/auth/") && !openPath(p) {
				t.Errorf("%s: церемонии входа должны быть открыты", p)
			}
		}
	}
	if seen < 20 {
		t.Fatalf("из исходников разобрано %d путей — разбор сломался и тест ничего не проверяет", seen)
	}

	// то же на живом обработчике: чужой без входа получает 401 на закрытых
	s, st := testServer(t)
	_ = st.SetSetting(auth.SettingLocalOpen, "false")
	for _, p := range closed {
		target := "https://" + publicSema + p
		if p == engine.MCPCallbackPath {
			target += "?code=x&state=y"
		}
		if w := serve(s, stranger("GET", target)); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s без входа: %d, ждали 401", p, w.Code)
		}
	}
}

// asBrowserAt — запрос из браузера владельца по https с указанного адреса.
func asBrowserAt(host, method, path, body, token string) *http.Request {
	r := asBrowser(method, path, body, token)
	r.URL.Host, r.Host = host, host
	r.Header.Set("Origin", "https://"+host)
	return r
}

// Вход документом клиента через экраны, с настоящей ручкой документа: сервер
// входа скачивает его без сессии, регистрации нет, адрес возврата — публичный.
func TestMCPLoginByClientDocumentThroughScreens(t *testing.T) {
	s, st, fake, token := mcpSetup(t)
	_ = st.SetSetting("auth.origin", "https://"+publicSema)
	_ = st.SetSetting("mcp.token_retry_ms", "1")
	fake.CIMD = true
	fake.FetchDoc = func(id string) ([]byte, int, error) {
		u, err := url.Parse(id)
		if err != nil {
			return nil, 0, err
		}
		w := serve(s, stranger("GET", u.String()))
		return w.Body.Bytes(), w.Code, nil
	}
	docURL := "https://" + publicSema + engine.MCPClientDocPath

	// «Войти» с публичного адреса
	if w := serve(s, asBrowserAt(publicSema, "POST", "/do", "action=mcp_login&target=sing&screen="+url.QueryEscape(mcpScreen), token)); w.Code != 200 {
		t.Fatalf("«Войти»: %d\n%.500s", w.Code, w.Body.String())
	}
	link, state := pendingLink(t, s)
	q := mustParse(t, link).Query()
	if q.Get("client_id") != docURL || q.Get("redirect_uri") != "https://"+publicSema+"/mcp/oauth/callback" {
		t.Fatalf("адрес страницы согласия: client_id %q, redirect_uri %q", q.Get("client_id"), q.Get("redirect_uri"))
	}
	code, _, err := fake.Approve(link)
	if err != nil {
		t.Fatalf("страница согласия (документ скачан настоящей ручкой): %v", err)
	}
	r := fromProvider("/mcp/oauth/callback?code="+code+"&state="+state, token)
	r.URL.Host, r.Host = publicSema, publicSema
	if w := serve(s, r); w.Code != http.StatusSeeOther {
		t.Fatalf("возврат: %d\n%s", w.Code, w.Body.String())
	}
	if n := len(fake.Registrations()); n != 0 {
		t.Errorf("регистраций %d, а сервер принимает документ", n)
	}
	m, _ := st.MCPServer("sing")
	if m.State != "ok" || len(m.Tools) == 0 {
		t.Errorf("после входа: %q / %q", m.State, m.Note)
	}
	if got := fake.DocFetches(); len(got) < 2 {
		t.Errorf("документ скачивали %d раз, ждали согласие и обмен кода", len(got))
	}
	// форма пределов показывает переключатель и адрес документа
	// пределы лежат в «Тонкой настройке»: тело приезжает по раскрытию, частью экрана
	w := serve(s, asBrowserAt(publicSema, "GET", "/ui?screen="+url.QueryEscape(mcpScreen)+"&part="+rareID, "", token))
	body := w.Body.String()
	for _, want := range []string{"Называть себя документом клиента", docURL, "client_metadata", "token_retries", "token_retry_ms"} {
		if !strings.Contains(body, want) {
			t.Errorf("на экране «Интеграции» нет %q", want)
		}
	}
}

// Пределы входа правятся с экрана: повторы, пауза и переключатель документа.
func TestMCPRetryLimitsFromScreen(t *testing.T) {
	s, st := testServer(t)
	ok := actionRequest{Action: "set_mcp_login_limits", Params: map[string]string{
		"token_retries": "5", "token_retry_ms": "250", "client_metadata": "false"}}
	if _, err := s.integrationsActions(ok); err != nil {
		t.Fatal(err)
	}
	if st.Setting("mcp.token_retries", "") != "5" || st.Setting("mcp.token_retry_ms", "") != "250" || st.Setting("mcp.client_metadata", "") != "0" {
		t.Errorf("не сохранилось: %q %q %q", st.Setting("mcp.token_retries", ""), st.Setting("mcp.token_retry_ms", ""), st.Setting("mcp.client_metadata", ""))
	}
	if tries, pause := s.eng.MCPRetryLimits(); tries != 5 || pause.Milliseconds() != 250 || s.eng.MCPClientMetadataOn() {
		t.Errorf("движок читает %d / %v / %v", tries, pause, s.eng.MCPClientMetadataOn())
	}
	// ноль повторов — осмысленное значение, а не «не задано»
	if _, err := s.integrationsActions(actionRequest{Action: "set_mcp_login_limits", Params: map[string]string{"token_retries": "0", "client_metadata": "true"}}); err != nil {
		t.Fatal(err)
	}
	if tries, _ := s.eng.MCPRetryLimits(); tries != 0 || !s.eng.MCPClientMetadataOn() {
		t.Errorf("ноль повторов или включение не сохранились: %d %v", tries, s.eng.MCPClientMetadataOn())
	}
	for _, bad := range []map[string]string{{"token_retries": "6"}, {"token_retries": "-1"}, {"token_retry_ms": "10001"}, {"token_retry_ms": "много"}} {
		if _, err := s.integrationsActions(actionRequest{Action: "set_mcp_login_limits", Params: bad}); err == nil {
			t.Errorf("%v принято", bad)
		}
	}
}
