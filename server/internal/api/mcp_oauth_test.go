package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вход на сервер MCP через экраны: «Войти» на «Интеграциях», страница
// согласия, возврат на /mcp/oauth/callback. Сервер MCP и сервер входа здесь
// на одном адресе, поэтому рабочая охрана адресов ничем не подменена.

const oauthHost = "sema.local:8443"

// asBrowser — запрос из браузера владельца по https с его сессией.
func asBrowser(method, path, body, token string) *http.Request {
	r := httptest.NewRequest(method, "https://"+oauthHost+path, strings.NewReader(body))
	r.RemoteAddr = "203.0.113.5:4000"
	r.Header.Set("Origin", "https://"+oauthHost)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Current-Screen", url.PathEscape(mcpScreen))
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "sema_session", Value: token})
	}
	return r
}

// fromProvider — браузер владельца приходит на возврат со страницы сервера
// входа: переход по ссылке, без заголовков оболочки, из чужого источника.
func fromProvider(path, token string) *http.Request {
	r := httptest.NewRequest("GET", "https://"+oauthHost+path, nil)
	r.RemoteAddr = "203.0.113.5:4000"
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Accept", "text/html")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "sema_session", Value: token})
	}
	return r
}

func serve(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func mcpSetup(t *testing.T) (*Server, *store.Store, *mcptest.Server, string) {
	t.Helper()
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	fake := mcptest.New(t, true)
	if err := st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: fake.URL(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	token, err := s.auth.OpenSession(1, "проба", "203.0.113.5", "проба")
	if err != nil {
		t.Fatal(err)
	}
	return s, st, fake, token
}

// pendingLink — адрес страницы согласия из начатого входа.
func pendingLink(t *testing.T, s *Server) (link, state string) {
	t.Helper()
	link, _, ok := s.eng.MCPLoginPending("sing")
	if !ok {
		t.Fatal("вход не начат")
	}
	u, _ := url.Parse(link)
	return link, u.Query().Get("state")
}

func TestMCPLoginThroughScreens(t *testing.T) {
	s, st, fake, token := mcpSetup(t)

	// сервер ждёт входа: строка говорит об этом и предлагает «Войти»
	_ = s.eng.CheckMCP(t.Context(), "sing")
	w := serve(s, asBrowser("GET", "/ui?screen="+url.QueryEscape(mcpScreen), "", token))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "нужен вход") || !strings.Contains(body, "Войти") {
		t.Fatalf("экран до входа: %d\n%.600s", w.Code, body)
	}

	// «Войти»: адрес возврата — тот, с которого нажали
	w = serve(s, asBrowser("POST", "/do", "action=mcp_login&target=sing&screen="+url.QueryEscape(mcpScreen), token))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Открыть страницу входа") {
		t.Fatalf("после «Войти»: %d\n%.600s", w.Code, w.Body.String())
	}
	link, state := pendingLink(t, s)
	if got := mustParse(t, link).Query().Get("redirect_uri"); got != "https://"+oauthHost+"/mcp/oauth/callback" {
		t.Errorf("redirect_uri %q", got)
	}
	code, _, err := fake.Approve(link)
	if err != nil {
		t.Fatalf("страница согласия: %v", err)
	}

	// возврат без сессии не завершает вход
	callback := "/mcp/oauth/callback?code=" + code + "&state=" + state
	if w := serve(s, fromProvider(callback, "")); w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
		t.Errorf("возврат без сессии: %d %q", w.Code, w.Header().Get("Location"))
	}
	noPage := fromProvider(callback, "")
	noPage.Header.Del("Accept")
	if w := serve(s, noPage); w.Code != http.StatusUnauthorized {
		t.Errorf("возврат без сессии и без страницы: %d", w.Code)
	}
	if len(fake.TokenForms()) != 0 {
		t.Fatal("код ушёл на обмен без сессии")
	}
	// возврат под сессией другого человека тоже не завершает
	other, _ := s.auth.OpenSession(7, "чужой", "203.0.113.6", "чужой")
	if w := serve(s, fromProvider(callback, other)); w.Code != http.StatusBadRequest {
		t.Errorf("возврат чужой сессией: %d", w.Code)
	}
	// чужой state
	if w := serve(s, fromProvider("/mcp/oauth/callback?code="+code+"&state=чужой", token)); w.Code != http.StatusBadRequest {
		t.Errorf("чужой state: %d", w.Code)
	}
	if len(fake.TokenForms()) != 0 {
		t.Fatal("код ушёл на обмен с чужим state")
	}

	// настоящий возврат: на экран сервера, а не в общий список
	w = serve(s, fromProvider(callback, token))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/#screen=settings%3Aintegrations%40sing" {
		t.Fatalf("возврат: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	// повторный — нет
	if w := serve(s, fromProvider(callback, token)); w.Code != http.StatusBadRequest {
		t.Errorf("повторный возврат: %d", w.Code)
	}

	// экран после входа: факты, «Выйти», ни одного токена
	tokens := tokensOf(t, s)
	for _, screen := range []string{mcpScreen, entityAddr(mcpScreen, "sing", ""), entityAddr(mcpScreen, "sing", mcpTabRules),
		entityAddr(mcpScreen, "sing", mcpTabLog), entityAddr(mcpScreen, mcpNew, ""),
		secretsScreen, "sent", "notifications", "settings:secrets@mcp:sing:oauth"} {
		w := serve(s, asBrowser("GET", "/ui?screen="+url.QueryEscape(screen), "", token))
		if w.Code != 200 {
			t.Errorf("экран %s: %d", screen, w.Code)
		}
		for _, secret := range tokens {
			if strings.Contains(w.Body.String(), secret) {
				t.Errorf("токен %q виден на экране %s", secret, screen)
			}
		}
	}
	// вход выполнен: факты и «Выйти» — на экране сервера, в «Опасном»
	w = serve(s, asBrowser("GET", "/ui?screen="+url.QueryEscape(entityAddr(mcpScreen, "sing", "")), "", token))
	body = w.Body.String()
	for _, want := range []string{"выполнен, действует до", "права: tasks:read tasks:write mcp:read", "вход выполнен, инструментов: 2", "вход OAuth выполнен"} {
		if !strings.Contains(body, want) {
			t.Errorf("на экране нет %q\n%.1500s", want, body)
		}
	}
	if !strings.Contains(body, "mcp_logout") {
		t.Errorf("нет «Выйти»")
	}
	// в хранилище секретов запись видна, но заменить её значением нельзя
	w = serve(s, asBrowser("GET", "/ui?screen="+url.QueryEscape(secretsScreen), "", token))
	if b := w.Body.String(); !strings.Contains(b, "mcp:sing:oauth") || !strings.Contains(b, "вход OAuth") || strings.Contains(b, "replace_secret\" data-target=\"mcp:sing:oauth") {
		t.Errorf("экран секретов: запись входа показана неверно")
	}

	// «Выйти» из меню: подтверждение и итог
	w = serve(s, asBrowser("POST", "/do", "action=mcp_logout&target=sing&screen="+url.QueryEscape(mcpScreen), token))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "нужен вход") {
		t.Errorf("после выхода: %d", w.Code)
	}
	if _, has := st.Secret("mcp:sing:oauth"); has {
		t.Error("токены остались после выхода")
	}
}

func tokensOf(t *testing.T, s *Server) []string {
	t.Helper()
	v, ok := s.eng.Store().SecretValue("mcp:sing:oauth")
	if !ok {
		t.Fatal("токенов нет")
	}
	var out []string
	for _, key := range []string{"access_token", "refresh_token"} {
		i := strings.Index(v, `"`+key+`":"`)
		if i < 0 {
			continue
		}
		rest := v[i+len(key)+4:]
		out = append(out, rest[:strings.Index(rest, `"`)])
	}
	if len(out) != 2 {
		t.Fatalf("токены не разобрались: %d", len(out))
	}
	return out
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Адрес возврата берётся из запроса, а не из тела: клиент не может подсунуть
// свой адрес полем действия.
func TestMCPLoginOriginComesFromRequest(t *testing.T) {
	s, _, _, token := mcpSetup(t)
	body := `{"action":"mcp_login","target":"sing","screen":"settings:integrations","origin":"https://evil.test","who":"session:1"}`
	r := asBrowser("POST", "/api/action", body, token)
	r.Header.Set("Content-Type", "application/json")
	if w := serve(s, r); w.Code != 200 {
		t.Fatalf("действие: %d %s", w.Code, w.Body.String())
	}
	link, _ := pendingLink(t, s)
	if got := mustParse(t, link).Query().Get("redirect_uri"); got != "https://"+oauthHost+"/mcp/oauth/callback" {
		t.Errorf("redirect_uri %q — адрес подсунут телом запроса", got)
	}
}

// Действие входа доступно хозяину, а не любому токену с узким правом.
func TestMCPLoginNeedsOwner(t *testing.T) {
	s, st, _, _ := mcpSetup(t)
	tok, err := st.AddToken("узкий", []string{"actions"})
	if err != nil {
		t.Fatal(err)
	}
	r := asBrowser("POST", "/api/action", `{"action":"mcp_login","target":"sing","screen":"settings:integrations"}`, "")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	if w := serve(s, r); w.Code != http.StatusForbidden {
		t.Errorf("токен с правом «действия» начал вход: %d", w.Code)
	}
	if _, _, ok := s.eng.MCPLoginPending("sing"); ok {
		t.Error("вход начат")
	}
}

// requestOrigin: откуда Сёма открыт — оттуда и возвращаем.
func TestRequestOrigin(t *testing.T) {
	s, st := testServer(t)
	mk := func(scheme, host string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("GET", scheme+"://"+host+"/", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		what string
		req  *http.Request
		want string
	}{
		{"https на своём порту", mk("https", "sema.local:8443", nil), "https://sema.local:8443"},
		{"http в своей сети", mk("http", "sema.local:8765", nil), "http://sema.local:8765"},
		{"публичный адрес по https", mk("https", "sema.example.org", nil), "https://sema.example.org"},
	}
	for _, c := range cases {
		if got := s.requestOrigin(c.req); got != c.want {
			t.Errorf("%s: %q, ждали %q", c.what, got, c.want)
		}
	}
	// TLS кончается на чужом прокси: схему без доверия к нему знает только auth.origin
	if err := st.SetSetting(auth.SettingOrigin, "https://sema.example.org"); err != nil {
		t.Fatal(err)
	}
	if got := s.requestOrigin(mk("http", "sema.example.org", nil)); got != "https://sema.example.org" {
		t.Errorf("за прокси при названном auth.origin: %q", got)
	}
	// другой хост не наследует схему чужого адреса входа
	if got := s.requestOrigin(mk("http", "sema.local:8765", nil)); got != "http://sema.local:8765" {
		t.Errorf("другой хост: %q", got)
	}
	// при доверии к прокси схему говорит он
	trustProxy.Store(true)
	t.Cleanup(func() { trustProxy.Store(false) })
	if got := s.requestOrigin(mk("http", "sema.example.org", map[string]string{"X-Forwarded-Proto": "https"})); got != "https://sema.example.org" {
		t.Errorf("X-Forwarded-Proto: %q", got)
	}
	// непонятный Host — запасной адрес входа, а не мусор в redirect_uri
	bad := mk("http", "sema.local", nil)
	bad.Host = "sema.local/../x"
	if got := s.requestOrigin(bad); got != s.auth.Origin() {
		t.Errorf("мусорный Host: %q", got)
	}
}

// Экран «Интеграции» после настройки сервера без ключа принимает его без
// секрета; форма «Добавить сервер MCP» несёт поле «Права входа».
func TestAddMCPWithoutKey(t *testing.T) {
	s, st := testServer(t)
	fake := mcptest.New(t, true)
	req := actionRequest{Action: "add_mcp", Params: map[string]string{
		"name": "sing", "url": fake.URL(), "login_scope": "  tasks:read   tasks:write "}}
	if _, err := s.integrationsActions(req); err != nil {
		t.Fatal(err)
	}
	m, ok := st.MCPServer("sing")
	if !ok || m.Secret != "" || m.LoginScope != "tasks:read tasks:write" {
		t.Fatalf("сервер: %+v", m)
	}
	if m.State != "login" || !strings.Contains(m.Note, "нужен вход") {
		t.Errorf("сервер без ключа, требующий вход: %q / %q", m.State, m.Note)
	}
	// «Права входа…» из меню строки
	if _, err := s.integrationsActions(actionRequest{Action: "mcp_login_scope", Target: "sing", Params: map[string]string{"value": "mcp:read"}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MCPServer("sing"); m.LoginScope != "mcp:read" {
		t.Errorf("права входа: %q", m.LoginScope)
	}
	// права входа уходят в страницу согласия
	if _, err := s.integrationsActions(actionRequest{Action: "mcp_login", Target: "sing", origin: "https://sema.local:8443", who: "session:1"}); err != nil {
		t.Fatal(err)
	}
	link, _ := pendingLink(t, s)
	if got := mustParse(t, link).Query().Get("scope"); got != "mcp:read" {
		t.Errorf("scope %q", got)
	}
	_ = engine.MCPCallbackPath
}

// Кнопки строки сервера в трёх состояниях: «Войти» — главное действие, пока
// вход нужен; после «Войти» — «Открыть страницу входа» ведёт на страницу
// сервера входа; после входа «Войти» исчезает, а «Выйти» в строку не
// возвращается: оно в «Опасном» на экране сервера (одно действие в строке).
func TestMCPRowButtons(t *testing.T) {
	s, _, fake, token := mcpSetup(t)
	_ = s.eng.CheckMCP(t.Context(), "sing")
	page := func() string {
		return serve(s, asBrowser("GET", "/ui?screen="+url.QueryEscape(mcpScreen), "", token)).Body.String()
	}
	row := func(html string) string {
		i := strings.Index(html, `data-goto="settings:integrations@sing"`)
		j := strings.Index(html, `Как это устроено`)
		if i < 0 || j < i {
			t.Fatalf("строки сервера нет на экране")
		}
		return html[i:j]
	}
	menuOf := func(r string) string {
		i := strings.Index(r, `<div class="dropdown">`)
		if i < 0 {
			return ""
		}
		return r[i:]
	}

	before := row(page())
	if !strings.Contains(before, `class="action primary"  hx-post="/do" hx-target="#window" hx-swap="innerHTML" hx-vals="{&#34;action&#34;:&#34;mcp_login&#34;,&#34;target&#34;:&#34;sing&#34;}">Войти</button>`) {
		t.Error("до входа «Войти» не главная кнопка строки")
	}
	if strings.Contains(before, "mcp_logout") || !strings.Contains(before, "нужен вход") {
		t.Error("до входа виден «Выйти» или нет слов «нужен вход»")
	}

	serve(s, asBrowser("POST", "/do", "action=mcp_login&target=sing&screen="+url.QueryEscape(mcpScreen), token))
	link, state := pendingLink(t, s)
	pending := row(page())
	if !strings.Contains(pending, `data-action="$open" data-target="`+strings.ReplaceAll(link, "&", "&amp;")+`">Открыть страницу входа</button>`) {
		t.Errorf("после «Войти» нет кнопки на страницу входа:\n%.1200s", pending)
	}
	if !strings.Contains(menuOf(pending), "mcp_login_cancel") {
		t.Error("«Отменить вход» не в меню")
	}

	code, _, err := fake.Approve(link)
	if err != nil {
		t.Fatal(err)
	}
	serve(s, fromProvider("/mcp/oauth/callback?code="+code+"&state="+state, token))
	after := row(page())
	if strings.Contains(after, "mcp_logout") {
		t.Error("«Выйти» вернулось в строку списка: оно в «Опасном» на экране сервера")
	}
	if strings.Contains(after, `>Войти</button>`) || !strings.Contains(after, "вход OAuth выполнен") {
		t.Error("после входа предлагается «Войти» или строка не говорит про вход")
	}
	// «Выйти» — в «Опасном» экрана сервера, под вопросом
	overview := serve(s, asBrowser("GET", "/ui?screen="+url.QueryEscape(entityAddr(mcpScreen, "sing", "")), "", token)).Body.String()
	i := strings.Index(overview, "Выйти из сервера")
	if i < 0 || !strings.Contains(overview[i:], "mcp_logout") || !strings.Contains(overview[i:], "data-confirm=") {
		t.Errorf("«Выйти» не в «Опасном» или без подтверждения (позиция %d)", i)
	}
	// и обе кнопки входа живут на «Обзоре» до входа: сначала «Войти», потом
	// «Открыть страницу входа»
	if strings.Contains(overview, `>Войти</button>`) {
		t.Error("на «Обзоре» после входа предлагается «Войти»")
	}
}

// Вход со страницы сервера: «Войти» готовит вход и остаётся на экране сервера,
// второй щелчок «Открыть страницу входа» ведёт на страницу сервера входа —
// настоящий щелчок по кнопке, а не открытие вкладки из ответа (Safari не даёт).
func TestMCPLoginButtonsOnServerScreen(t *testing.T) {
	s, _, fake, token := mcpSetup(t)
	_ = s.eng.CheckMCP(t.Context(), "sing")
	here := entityAddr(mcpScreen, "sing", "")
	view := func() string {
		return serve(s, asBrowser("GET", "/ui?screen="+url.QueryEscape(here), "", token)).Body.String()
	}
	before := view()
	if !strings.Contains(before, `>Войти</button>`) || strings.Contains(before, "Открыть страницу входа") {
		t.Fatalf("до входа на «Обзоре» нет «Войти» или уже есть страница входа:\n%.1500s", before)
	}
	// нажали «Войти» с экрана сервера: ответ — тот же экран, уже с «Открыть страницу входа»
	w := serve(s, asBrowser("POST", "/do", "action=mcp_login&target=sing&screen="+url.QueryEscape(here), token))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Открыть страницу входа") || !strings.Contains(w.Body.String(), `data-screen="`+here+`"`) {
		t.Fatalf("после «Войти» на экране сервера: %d\n%.1500s", w.Code, w.Body.String())
	}
	link, _ := pendingLink(t, s)
	if !strings.Contains(w.Body.String(), `data-action="$open" data-target="`+strings.ReplaceAll(link, "&", "&amp;")+`"`) {
		t.Errorf("«Открыть страницу входа» не ведёт на страницу сервера входа")
	}
	if !strings.Contains(w.Body.String(), "mcp_login_cancel") {
		t.Error("«Отменить вход» не доступен на «Обзоре»")
	}
	// отменили — вернулось «Войти»
	w = serve(s, asBrowser("POST", "/do", "action=mcp_login_cancel&target=sing&screen="+url.QueryEscape(here), token))
	if w.Code != 200 || strings.Contains(w.Body.String(), "Открыть страницу входа") || !strings.Contains(w.Body.String(), `>Войти</button>`) {
		t.Errorf("после «Отменить вход»: %d", w.Code)
	}
	_ = fake
}

// Пределы входа правятся на экране, а не в коде; непонятное значение не
// записывается.
func TestMCPLoginLimitsFromScreen(t *testing.T) {
	s, st := testServer(t)
	ok := actionRequest{Action: "set_mcp_login_limits", Params: map[string]string{
		"login_ttl_min": "15", "refresh_skew_s": "120", "net_timeout_s": "30"}}
	if _, err := s.integrationsActions(ok); err != nil {
		t.Fatal(err)
	}
	if got := st.Setting("mcp.login_ttl_min", ""); got != "15" || st.Setting("mcp.refresh_skew_s", "") != "120" || st.Setting("mcp.net_timeout_s", "") != "30" {
		t.Errorf("настройки: %q", got)
	}
	for _, bad := range []map[string]string{{"login_ttl_min": "0"}, {"refresh_skew_s": "сто"}, {"net_timeout_s": "5000"}} {
		if _, err := s.integrationsActions(actionRequest{Action: "set_mcp_login_limits", Params: bad}); err == nil {
			t.Errorf("значение %v принято", bad)
		}
	}
	doc, err := s.buildScreen(mcpScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range append(append([]Component{}, doc.Components...), rareBody(t, s, mcpScreen)...) { // «Тонкая настройка»
		if c.ID == "mcp-login-limits" {
			found = true
			// три срока входа, потом повторы, пауза и переключатель документа клиента
			if len(c.Fields) != 6 || c.Fields[0].Value != "15" || c.Fields[1].Value != "120" || c.Fields[2].Value != "30" ||
				c.Fields[3].Name != "token_retries" || c.Fields[4].Name != "token_retry_ms" || c.Fields[5].Name != "client_metadata" {
				t.Errorf("поля пределов: %+v", c.Fields)
			}
		}
	}
	if !found {
		t.Error("формы пределов входа нет на экране")
	}
}
