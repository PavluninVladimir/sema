package engine

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/mcp"
	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вход OAuth на сервер MCP целиком, на поддельных серверах без сети наружу.

const testOrigin = "https://sema.test:8443"
const testWho = "session:1"

// mcpTestEngine — движок на временной базе; путь к базе нужен, чтобы
// посмотреть, что лежит на диске.
func mcpTestEngine(t *testing.T) (*Engine, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	old, had := os.LookupEnv("SEMA_KEY")
	_ = os.Setenv("SEMA_KEY", "ключ-для-тестов")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEMA_KEY", old)
			return
		}
		_ = os.Unsetenv("SEMA_KEY")
	})
	dbPath := filepath.Join(dir, "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	conf := &config.Config{Data: dir, KeyEnv: "SEMA_KEY"}
	conf.Files.Dir = filepath.Join(dir, "files")
	e, err := New(conf, st)
	if err != nil {
		t.Fatalf("движок: %v", err)
	}
	return e, st, dbPath
}

// openLoginNet — подмена сети входа для тестов, где сервер MCP и сервер
// входа стоят на разных адресах loopback: рабочая охрана такие адреса не
// пускает (так и задумано). Рабочий `netguard` не трогается — подменяется
// только переменная, как `scriptHTTP` и `falHTTP`.
func openLoginNet(t *testing.T) {
	t.Helper()
	was := mcpLoginNet
	mcpLoginNet = func(string, time.Duration) mcp.Net { return mcp.Net{HTTP: &http.Client{Timeout: 10 * time.Second}} }
	t.Cleanup(func() { mcpLoginNet = was })
}

func addMCP(t *testing.T, st *store.Store, name, url string) {
	t.Helper()
	if err := st.SaveMCPServer(&store.MCPServer{Name: name, URL: url, Enabled: true,
		LegacyRead: mcpSeed.read, LegacyNever: mcpSeed.never}); err != nil {
		t.Fatal(err)
	}
	// инструмент, про который человек не решал, запрещён: решено за те, что зовут стенды
	decideMCP(t, st, name, map[string]string{"task_list": PolicyAllow, "task_get": PolicyAllow})
}

func rawSecret(t *testing.T, dbPath, name string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM secrets WHERE name = ?`, name).Scan(&v); err != nil {
		t.Fatalf("запись %s: %v", name, err)
	}
	return v
}

// loginThrough — целый вход: «Войти», страница согласия, возврат.
func loginThrough(t *testing.T, e *Engine, fake *mcptest.Server, name string) {
	t.Helper()
	link, err := e.MCPLoginBegin(context.Background(), name, testOrigin, testWho)
	if err != nil {
		t.Fatalf("начало входа: %v", err)
	}
	code, state, err := fake.Approve(link)
	if err != nil {
		t.Fatalf("страница согласия: %v", err)
	}
	if srv, err := e.MCPLoginFinish(context.Background(), state, code, "", "", testWho); err != nil || srv != name {
		t.Fatalf("возврат: %q, %v", srv, err)
	}
}

func okCount(fake *mcptest.Server, method string) int {
	n := 0
	for _, r := range fake.Requests() {
		if r.Method == method && r.Status == http.StatusOK {
			n++
		}
	}
	return n
}

// Весь путь: 401 → обнаружение → регистрация → адрес согласия с PKCE →
// возврат → токены в хранилище → вызовы с Bearer → обновление с ротацией →
// отказ в обновлении → «нужен вход» и уведомление → повторный вход → «Выйти».
func TestMCPLoginFullCycle(t *testing.T) {
	openLoginNet(t)
	e, st, dbPath := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	ctx := context.Background()

	// 1. без токена: сервер жив, но ждёт входа — это состояние, а не поломка
	if err := e.CheckMCP(ctx, "sing"); err == nil {
		t.Fatal("проверка без входа прошла")
	}
	m, _ := st.MCPServer("sing")
	if m.State != "login" || !strings.Contains(m.Note, "нужен вход") {
		t.Fatalf("состояние после 401: %q / %q", m.State, m.Note)
	}
	if _, open := st.OpenNotificationByKey("mcp_login:sing"); open {
		t.Error("проверка по кнопке подняла уведомление: человек и так смотрит на экран")
	}

	// 2. вызов из чата или модуля — уведомление, один раз
	res := e.RequestMCP(ctx, "тест", "sing", "task_list", nil)
	if !strings.Contains(res.Err, "требует вход") {
		t.Fatalf("вызов без входа: %+v", res)
	}
	n1, open := st.OpenNotificationByKey("mcp_login:sing")
	if !open || n1.Level != "warn" || len(n1.Actions) == 0 || n1.Actions[0].Kind != "goto" || n1.Actions[0].Screen != MCPServerScreen("sing", "") {
		t.Fatalf("уведомление о входе: %+v", n1)
	}
	e.RequestMCP(ctx, "тест", "sing", "task_list", nil)
	if n2, _ := st.OpenNotificationByKey("mcp_login:sing"); n2.ID != n1.ID {
		t.Error("каждый неудачный вызов шлёт новое уведомление")
	}

	// 3. «Войти»
	link, err := e.MCPLoginBegin(ctx, "sing", testOrigin, testWho)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	if !strings.HasPrefix(link, fake.Auth.URL+"/oauth/authorize?") {
		t.Errorf("страница согласия: %s", link)
	}
	wantRedirect := testOrigin + "/mcp/oauth/callback"
	for k, want := range map[string]string{"response_type": "code", "client_id": "cid-1", "redirect_uri": wantRedirect,
		"code_challenge_method": "S256", "resource": fake.URL(), "scope": "tasks:read tasks:write mcp:read"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, ждали %q", k, q.Get(k), want)
		}
	}
	if q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Error("нет state или code_challenge")
	}
	if reg := fake.Registrations(); len(reg) != 1 || reg[0]["client_name"] != "Сёма" {
		t.Errorf("регистрация: %v", reg)
	}
	if got, until, ok := e.MCPLoginPending("sing"); !ok || got != link || time.Until(until) > 10*time.Minute || time.Until(until) < 9*time.Minute {
		t.Errorf("начатый вход: %v %v %v", got, until, ok)
	}

	// 4. чужой и повторный state, чужой человек — отказ; настоящая попытка жива
	code, state, err := fake.Approve(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.MCPLoginFinish(ctx, "чужой-state", code, "", "", testWho); !errors.Is(err, ErrMCPState) {
		t.Errorf("чужой state: %v", err)
	}
	if _, err := e.MCPLoginFinish(ctx, state, code, "", "", "session:2"); !errors.Is(err, ErrMCPState) {
		t.Errorf("state другого человека: %v", err)
	}
	if _, _, ok := e.MCPLoginPending("sing"); !ok {
		t.Fatal("чужие попытки убили настоящую")
	}
	if fake.Registrations() == nil || len(fake.TokenForms()) != 0 {
		t.Error("чужой state дошёл до сервера входа")
	}

	// 5. возврат
	srv, err := e.MCPLoginFinish(ctx, state, code, "", "", testWho)
	if err != nil || srv != "sing" {
		t.Fatalf("возврат: %q, %v", srv, err)
	}
	if _, err := e.MCPLoginFinish(ctx, state, code, "", "", testWho); !errors.Is(err, ErrMCPState) {
		t.Errorf("повторный state принят: %v", err)
	}
	m, _ = st.MCPServer("sing")
	if m.State != "ok" || m.Note != "вход выполнен, инструментов: 2" || len(m.Tools) != 2 {
		t.Fatalf("после входа: %q / %q / %v", m.State, m.Note, m.Tools)
	}
	if _, open := st.OpenNotificationByKey("mcp_login:sing"); open {
		t.Error("уведомление о входе не закрылось")
	}
	if _, _, ok := e.MCPLoginPending("sing"); ok {
		t.Error("использованный вход остался начатым")
	}
	v := e.MCPLoginView(m)
	if !v.Has || v.Scope != "tasks:read tasks:write mcp:read" || !v.Refresh || time.Until(v.Expires) < 59*time.Minute {
		t.Errorf("сведения о входе для экрана: %+v", v)
	}

	// 6. токены — в хранилище и зашифрованы
	sec, ok := st.Secret("mcp:sing:oauth")
	if !ok || sec.Kind != store.SecretOAuth {
		t.Fatalf("запись хранилища: %+v %v", sec, ok)
	}
	raw := rawSecret(t, dbPath, "mcp:sing:oauth")
	if !strings.HasPrefix(raw, "enc:v1:") || strings.Contains(raw, "at-1") || strings.Contains(raw, "rt-1") || strings.Contains(raw, "cid-1") {
		t.Errorf("токены лежат открыто: %.60s", raw)
	}
	plain, _ := st.SecretValue("mcp:sing:oauth")
	if !strings.Contains(plain, `"access_token":"at-1"`) || !strings.Contains(plain, `"refresh_token":"rt-1"`) ||
		!strings.Contains(plain, `"client_id":"cid-1"`) || !strings.Contains(plain, `"resource":"`+fake.URL()+`"`) ||
		!strings.Contains(plain, `"token_endpoint":"`+fake.Auth.URL+`/oauth/token"`) {
		t.Errorf("состав записи: %s", plain)
	}
	if list, _ := st.SecretLog("mcp:sing:oauth", 10); len(list) == 0 || !strings.Contains(list[0].Who, "сервер MCP «sing»") {
		t.Errorf("чтение ядром не видно в журнале секрета: %+v", list)
	}

	// 7. вызов идёт с Bearer, сессия одна на все вызовы
	res = e.RequestMCP(ctx, "тест", "sing", "task_list", nil)
	if res.Err != "" || res.Text != "вызван task_list" {
		t.Fatalf("вызов с входом: %+v", res)
	}
	e.RequestMCP(ctx, "тест", "sing", "task_list", nil)
	reqs := fake.Requests()
	last := reqs[len(reqs)-1]
	if last.Method != "tools/call" || last.Auth != "Bearer at-1" || last.Session == "" || last.Protocol != "2025-06-18" {
		t.Errorf("последний вызов: %+v", last)
	}
	if got := okCount(fake, "initialize"); got != 1 {
		t.Errorf("рукопожатий с сервером: %d — сессия не пережила вызовы", got)
	}
	if okCount(fake, "tools/list") != 1 || okCount(fake, "tools/call") != 2 {
		t.Errorf("tools/list %d, tools/call %d", okCount(fake, "tools/list"), okCount(fake, "tools/call"))
	}

	// 8. срок токена вышел: продление с ротацией, потом вызов
	tokens, _ := e.readMCPTokens("sing", "тест")
	tokens.ExpiresAt = time.Now().Add(20 * time.Second).Format(time.RFC3339) // меньше минуты — уже пора
	if err := e.saveMCPTokens("sing", tokens); err != nil {
		t.Fatal(err)
	}
	res = e.RequestMCP(ctx, "тест", "sing", "task_list", nil)
	if res.Err != "" {
		t.Fatalf("вызов после продления: %+v", res)
	}
	forms := fake.TokenForms()
	if refresh := forms[len(forms)-1]; refresh.Get("grant_type") != "refresh_token" || refresh.Get("refresh_token") != "rt-1" ||
		refresh.Get("client_id") != "cid-1" || refresh.Get("resource") != fake.URL() {
		t.Errorf("форма продления: %v", refresh)
	}
	reqs = fake.Requests()
	if got := reqs[len(reqs)-1].Auth; got != "Bearer at-2" {
		t.Errorf("после продления ушло %q", got)
	}
	tokens, _ = e.readMCPTokens("sing", "тест")
	if tokens.Access != "at-2" || tokens.Refresh != "rt-2" {
		t.Errorf("ротация не записана: %s / %s", tokens.Access, tokens.Refresh)
	}

	// 9. сервер отозвал токен раньше срока: 401 → продление → повтор
	fake.KillAccess()
	res = e.RequestMCP(ctx, "тест", "sing", "task_list", nil)
	if res.Err != "" || res.Text != "вызван task_list" {
		t.Fatalf("вызов после отзыва токена: %+v", res)
	}
	reqs = fake.Requests()
	if got := reqs[len(reqs)-1].Auth; got != "Bearer at-3" {
		t.Errorf("после 401 ушло %q", got)
	}

	// 10. продление отклонено: «нужен вход», токены убраны, уведомление
	fake.KillAccess()
	fake.RejectRefresh = true
	res = e.RequestMCP(ctx, "тест", "sing", "task_list", nil)
	if !strings.Contains(res.Err, "вход в сервер MCP «sing» истёк") {
		t.Fatalf("вызов с мёртвым входом: %+v", res)
	}
	m, _ = st.MCPServer("sing")
	if m.State != "login" || m.Login != nil || !strings.Contains(m.Note, "вход истёк") {
		t.Errorf("после потери входа: %q / %q / %+v", m.State, m.Note, m.Login)
	}
	if _, has := st.Secret("mcp:sing:oauth"); has {
		t.Error("мёртвые токены остались в хранилище")
	}
	if n, open := st.OpenNotificationByKey("mcp_login:sing"); !open || n.Level != "warn" || !strings.Contains(n.Summary, "вход истёк") {
		t.Errorf("уведомление после потери входа: %+v %v", n, open)
	}

	// 11. вошли заново — уведомление закрылось, регистрация та же
	fake.RejectRefresh = false
	loginThrough(t, e, fake, "sing")
	if _, open := st.OpenNotificationByKey("mcp_login:sing"); open {
		t.Error("после нового входа уведомление осталось")
	}
	if len(fake.Registrations()) != 1 {
		t.Errorf("клиент зарегистрирован заново без нужды: %d регистраций", len(fake.Registrations()))
	}

	// 12. «Выйти»
	if err := e.MCPLogout(ctx, "sing"); err != nil {
		t.Fatal(err)
	}
	if _, has := st.Secret("mcp:sing:oauth"); has {
		t.Error("после выхода токены остались")
	}
	if _, known := st.MCPClient("sing", testOrigin+MCPCallbackPath, fake.Auth.URL); known {
		t.Error("после выхода остался client_id")
	}
	m, _ = st.MCPServer("sing")
	if m.State != "login" || m.Login != nil || e.MCPLoginView(m).Has {
		t.Errorf("после выхода: %q %+v", m.State, m.Login)
	}
	if len(fake.Revoked()) != 2 {
		t.Errorf("сервер входа просили отозвать %v", fake.Revoked())
	}
	if res := e.RequestMCP(ctx, "тест", "sing", "task_list", nil); !strings.Contains(res.Err, "требует вход") {
		t.Errorf("вызов после выхода: %+v", res)
	}
}

// Токены не видны нигде, кроме зашифрованной записи: ни в журнале вызовов,
// ни в уведомлениях, ни в сведениях о входе для экрана.
func TestMCPTokensStayOutOfEverything(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	e.RequestMCP(context.Background(), "тест", "sing", "task_list", nil)
	loginThrough(t, e, fake, "sing")
	e.RequestMCP(context.Background(), "тест", "sing", "task_list", map[string]any{"q": "x"})
	tokens, _ := e.readMCPTokens("sing", "тест")
	secrets := []string{tokens.Access, tokens.Refresh}

	var seen []string
	log, _ := st.MCPLog("sing", 100)
	for _, u := range log {
		seen = append(seen, u.Args, u.Result, u.Tool)
	}
	m, _ := st.MCPServer("sing")
	seen = append(seen, m.Note, m.LoginScope, m.State)
	if m.Login != nil {
		seen = append(seen, m.Login.At, m.Login.Expires, m.Login.Scope)
	}
	view := e.MCPLoginView(m)
	seen = append(seen, view.Scope, view.PendingURL)
	nots, _ := st.Notifications(false, 100)
	open, _ := st.Notifications(true, 100)
	for _, n := range append(nots, open...) {
		seen = append(seen, n.Title, n.Summary)
		for _, f := range n.Fields {
			seen = append(seen, f.Label, f.Value)
		}
	}
	for _, s := range seen {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(s, secret) {
				t.Errorf("токен %q попал в %q", secret, s)
			}
		}
	}
	if len(log) == 0 {
		t.Error("журнал вызовов пуст — проверять нечего")
	}
}

// Одновременные вызовы при истёкшем токене продлевают его один раз:
// refresh_token одноразовый, второе продление убило бы вход.
func TestMCPRefreshIsSerialized(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	loginThrough(t, e, fake, "sing")
	tokens, _ := e.readMCPTokens("sing", "тест")
	tokens.ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339)
	if err := e.saveMCPTokens("sing", tokens); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.mcpAccessToken(context.Background(), "sing", ""); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("продление: %v", err)
	}
	refreshes := 0
	for _, f := range fake.TokenForms() {
		if f.Get("grant_type") == "refresh_token" {
			refreshes++
		}
	}
	if refreshes != 1 {
		t.Errorf("продлений %d, ждали одно", refreshes)
	}
}

// Временная беда при продлении (сеть, 500) вход не отменяет.
func TestMCPRefreshTransientKeepsLogin(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	loginThrough(t, e, fake, "sing")
	tokens, _ := e.readMCPTokens("sing", "тест")
	tokens.ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339)
	_ = e.saveMCPTokens("sing", tokens)
	fake.RefreshTransient = true
	res := e.RequestMCP(context.Background(), "тест", "sing", "task_list", nil)
	if res.Err == "" || !strings.Contains(res.Err, "не удалось продлить вход") {
		t.Fatalf("вызов при беде сервера входа: %+v", res)
	}
	if _, has := st.Secret("mcp:sing:oauth"); !has {
		t.Error("временная беда стёрла вход")
	}
	if m, _ := st.MCPServer("sing"); m.State == "login" {
		t.Error("временная беда переведена в «нужен вход»")
	}
	if _, open := st.OpenNotificationByKey("mcp_login:sing"); open {
		t.Error("временная беда подняла уведомление")
	}
	fake.RefreshTransient = false
	if res := e.RequestMCP(context.Background(), "тест", "sing", "task_list", nil); res.Err != "" {
		t.Errorf("после выздоровления: %+v", res)
	}
}

// Честные отказы там, где вход невозможен.
func TestMCPLoginRefusals(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	ctx := context.Background()

	fake := mcptest.New(t, false)
	fake.NoRegistration = true
	addMCP(t, st, "noreg", fake.URL())
	if _, err := e.MCPLoginBegin(ctx, "noreg", testOrigin, testWho); err == nil || !strings.Contains(err.Error(), "registration_endpoint") {
		t.Errorf("сервер без регистрации: %v", err)
	}

	fake2 := mcptest.New(t, false)
	fake2.RegisterError = "redirect_uri должен быть https"
	addMCP(t, st, "strict", fake2.URL())
	_, err := e.MCPLoginBegin(ctx, "strict", "http://sema.local:8765", testWho)
	if err == nil || !strings.Contains(err.Error(), "redirect_uri должен быть https") || !strings.Contains(err.Error(), "http://sema.local:8765/mcp/oauth/callback") {
		t.Errorf("сервер отказал в регистрации: %v", err)
	}
	if _, _, ok := e.MCPLoginPending("strict"); ok {
		t.Error("после отказа осталась начатая попытка")
	}

	fake3 := mcptest.New(t, false)
	fake3.AuthRequired = false
	addMCP(t, st, "open", fake3.URL())
	if _, err := e.MCPLoginBegin(ctx, "open", testOrigin, testWho); err == nil || !strings.Contains(err.Error(), "без входа") {
		t.Errorf("сервер без входа: %v", err)
	}

	if err := st.SaveMCPServer(&store.MCPServer{Name: "keyed", URL: fake.URL(), Secret: "какой-то", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.MCPLoginBegin(ctx, "keyed", testOrigin, testWho); err == nil || !strings.Contains(err.Error(), "постоянный ключ") {
		t.Errorf("сервер с постоянным ключом: %v", err)
	}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "agentic", Agent: "мак", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.MCPLoginBegin(ctx, "agentic", testOrigin, testWho); err == nil {
		t.Error("вход начат на сервере агента")
	}
	if _, err := e.MCPLoginBegin(ctx, "noreg", "", testWho); err == nil {
		t.Error("вход начат без адреса возврата")
	}

	// сервер входа вернул человека с отказом
	fake4 := mcptest.New(t, false)
	addMCP(t, st, "denied", fake4.URL())
	link, err := e.MCPLoginBegin(ctx, "denied", testOrigin, testWho)
	if err != nil {
		t.Fatal(err)
	}
	state := mustQuery(t, link, "state")
	if srv, err := e.MCPLoginFinish(ctx, state, "", "access_denied", "человек нажал «Отмена»", testWho); err == nil || srv != "denied" {
		t.Errorf("отказ на странице согласия: %q, %v", srv, err)
	}
	if m, _ := st.MCPServer("denied"); m.State != "login" || !strings.Contains(m.Note, "человек нажал «Отмена»") {
		t.Errorf("причина отказа не видна в строке: %q / %q", m.State, m.Note)
	}
}

func mustQuery(t *testing.T, link, key string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}

// Начатый вход живёт десять минут.
func TestMCPLoginExpires(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	link, err := e.MCPLoginBegin(context.Background(), "sing", testOrigin, testWho)
	if err != nil {
		t.Fatal(err)
	}
	code, state, _ := fake.Approve(link)
	e.mcpMu.Lock()
	e.mcpX.tries[state].Expires = time.Now().Add(-time.Second)
	e.mcpMu.Unlock()
	if _, _, ok := e.MCPLoginPending("sing"); ok {
		t.Error("просроченный вход показан как начатый")
	}
	if _, err := e.MCPLoginFinish(context.Background(), state, code, "", "", testWho); !errors.Is(err, ErrMCPState) {
		t.Errorf("просроченный state принят: %v", err)
	}
	// новая попытка отменяет прежнюю
	l1, _ := e.MCPLoginBegin(context.Background(), "sing", testOrigin, testWho)
	l2, _ := e.MCPLoginBegin(context.Background(), "sing", testOrigin, testWho)
	c1, s1, _ := fake.Approve(l1)
	if _, err := e.MCPLoginFinish(context.Background(), s1, c1, "", "", testWho); !errors.Is(err, ErrMCPState) {
		t.Errorf("прежняя попытка пережила новую: %v", err)
	}
	if got, _, ok := e.MCPLoginPending("sing"); !ok || got != l2 {
		t.Error("новая попытка потеряна")
	}
	e.MCPLoginCancel("sing")
	if _, _, ok := e.MCPLoginPending("sing"); ok {
		t.Error("отменённый вход остался")
	}
}

// Рабочая охрана адресов (ничего не подменено): сервер входа, названный
// чужим сервером и стоящий в частной сети, не читается, регистрация не
// отправляется.
func TestMCPLoginGuardsFoundAddresses(t *testing.T) {
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false) // MCP и сервер входа — разные адреса loopback
	addMCP(t, st, "sing", fake.URL())
	_, err := e.MCPLoginBegin(context.Background(), "sing", testOrigin, testWho)
	if err == nil || !strings.Contains(err.Error(), "не годится для входа") {
		t.Fatalf("вход на сервере входа из частной сети: %v", err)
	}
	if len(fake.Registrations()) != 0 {
		t.Error("регистрация ушла на адрес, который охрана не пускает")
	}
}

// Сервер входа на том же адресе, что записал человек, доверенный: так стоят
// серверы в своей сети. Рабочая охрана при этом не подменена.
func TestMCPLoginSameOriginAllowedWithRealGuard(t *testing.T) {
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, true)
	addMCP(t, st, "wiki", fake.URL())
	loginThrough(t, e, fake, "wiki")
	if m, _ := st.MCPServer("wiki"); m.State != "ok" || len(m.Tools) != 2 {
		t.Errorf("после входа: %q %v", m.State, m.Tools)
	}
	if res := e.RequestMCP(context.Background(), "тест", "wiki", "task_list", nil); res.Err != "" {
		t.Errorf("вызов: %+v", res)
	}
}

// Старый порядок цел: постоянный ключ в заголовке, без входа; сессия MCP
// при этом тоже одна на несколько вызовов.
func TestMCPStaticKeyStillWorks(t *testing.T) {
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, true)
	fake.AcceptToken = "постоянный-ключ"
	if err := st.SaveSecret(&store.Secret{Name: "wiki-key", Kind: store.SecretToken}, "постоянный-ключ"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "wiki", URL: fake.URL(), Secret: "wiki-key", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckMCP(context.Background(), "wiki"); err != nil {
		t.Fatalf("проверка с ключом: %v", err)
	}
	decideMCP(t, st, "wiki", map[string]string{"task_list": PolicyAllow})
	for i := 0; i < 2; i++ {
		if res := e.RequestMCP(context.Background(), "тест", "wiki", "task_list", nil); res.Err != "" || res.Text != "вызван task_list" {
			t.Fatalf("вызов с ключом: %+v", res)
		}
	}
	reqs := fake.Requests()
	if last := reqs[len(reqs)-1]; last.Auth != "Bearer постоянный-ключ" {
		t.Errorf("заголовок: %q", last.Auth)
	}
	if okCount(fake, "initialize") != 1 {
		t.Errorf("рукопожатий: %d", okCount(fake, "initialize"))
	}
	if m, _ := st.MCPServer("wiki"); m.State != "ok" || e.MCPLoginView(m).Has {
		t.Errorf("сервер с ключом: %q", m.State)
	}
	// ключ отвергнут — это «не отвечает», а не «нужен вход»: вход тут ни при чём
	fake.AcceptToken = "другой"
	if err := e.CheckMCP(context.Background(), "wiki"); err == nil {
		t.Fatal("отвергнутый ключ прошёл")
	}
	if m, _ := st.MCPServer("wiki"); m.State != "fail" {
		t.Errorf("состояние при отвергнутом ключе: %q", m.State)
	}
	if _, open := st.OpenNotificationByKey("mcp_login:wiki"); open {
		t.Error("у сервера с ключом поднято уведомление о входе")
	}
}

// Записи входа не выдаются модулям и не заменяются руками.
func TestMCPTokensNeverGoToModules(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	loginThrough(t, e, fake, "sing")

	if err := e.ReplaceSecret("mcp:sing:oauth", "подмена"); err == nil {
		t.Error("токены заменены значением")
	}
	m := &Module{Name: "разведчик", Granted: []string{"mcp:sing:oauth"}}
	fill := e.moduleSecretFiller(m, "тест")
	if v, err := fill("mcp:sing:oauth"); err == nil || v != "" {
		t.Errorf("подстановка в заголовок отдала токены: %q, %v", v, err)
	}
	tool := e.secretTool(m, "тест")
	if v, err := tool.Fn([]any{"mcp:sing:oauth"}); err == nil || v != nil {
		t.Errorf("sema.secret отдал токены: %v, %v", v, err)
	}
	if places := e.SecretPlaces("mcp:sing:oauth"); len(places) != 1 || !strings.Contains(places[0], "sing") {
		t.Errorf("где используется: %v", places)
	}
	// запись входа — служебный секрет: с экрана секретов её не удалить, выходят
	// с сервера кнопкой «Выйти», и строка сервера после этого не врёт
	if err := e.DeleteSecret("mcp:sing:oauth"); err == nil {
		t.Fatal("служебный секрет удалился с экрана секретов")
	}
	if _, has := st.Secret("mcp:sing:oauth"); !has {
		t.Fatal("отказ в удалении всё же унёс токены")
	}
	if err := e.MCPLogout(context.Background(), "sing"); err != nil {
		t.Fatal(err)
	}
	if _, has := st.Secret("mcp:sing:oauth"); has {
		t.Error("после «Выйти» токены остались")
	}
	if srv, _ := st.MCPServer("sing"); srv.State != "login" || srv.Login != nil {
		t.Errorf("после выхода: %q %+v", srv.State, srv.Login)
	}
}

// Удаление сервера уносит вход.
func TestMCPDeleteServerRemovesLogin(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	loginThrough(t, e, fake, "sing")
	if err := e.DeleteMCPServer(context.Background(), "sing"); err != nil {
		t.Fatal(err)
	}
	if _, has := st.Secret("mcp:sing:oauth"); has {
		t.Error("токены удалённого сервера остались")
	}
	if _, ok := st.MCPServer("sing"); ok {
		t.Error("сервер остался")
	}
	if len(fake.Revoked()) == 0 {
		t.Error("токены не отозваны у сервера входа")
	}
}

// Правила сети входа, как они собраны в работе: найденные сервером адреса —
// только публичные и только https; адрес, который записал сам человек,
// доверенный и в своей сети.
func TestMCPLoginNetPolicy(t *testing.T) {
	n := mcpLoginNet("https://mcp.example.com/mcp", time.Second)
	bad := []string{"https://169.254.169.254/latest/meta-data", "https://127.0.0.1:9000/x", "https://10.0.0.5/x",
		"https://printer.local/x", "http://as.example.com/oauth/token", "https://user:pw@as.example.com/x"}
	for _, raw := range bad {
		u, _ := url.Parse(raw)
		if err := n.Check(u); err == nil {
			t.Errorf("адрес %s разрешён", raw)
		}
	}
	for _, raw := range []string{"https://me.example.com/oauth/token", "https://mcp.example.com/.well-known/oauth-protected-resource/mcp"} {
		u, _ := url.Parse(raw)
		if err := n.Check(u); err != nil {
			t.Errorf("адрес %s запрещён: %v", raw, err)
		}
	}
	home := mcpLoginNet("http://192.168.1.5:9000/mcp", time.Second)
	own, _ := url.Parse("http://192.168.1.5:9000/oauth/token")
	other, _ := url.Parse("http://192.168.1.6:9000/oauth/token")
	if err := home.Check(own); err != nil {
		t.Errorf("сервер в своей сети, записанный человеком: %v", err)
	}
	if err := home.Check(other); err == nil {
		t.Error("сосед по сети принят за сервер человека")
	}

	// сам транспорт: к чужому адресу в частной сети запрос не уходит
	var hit bool
	srv := httptestServer(func() { hit = true })
	defer srv.Close()
	if _, err := n.HTTP.Get(srv.URL); err == nil || hit {
		t.Errorf("запрос ушёл на loopback: err=%v, дошёл=%v", err, hit)
	}
}

func httptestServer(onHit func()) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { onHit() }))
}

// Сроки входа — настройки: начатый вход живёт столько, сколько сказано, и
// токен продлевается за столько, за сколько сказано.
func TestMCPLimitsAreSettings(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	if ttl, skew, wait := e.MCPLoginLimits(); ttl != 10*time.Minute || skew != 60*time.Second || wait != 20*time.Second {
		t.Errorf("первичные значения: %v %v %v", ttl, skew, wait)
	}
	_ = st.SetSetting("mcp.login_ttl_min", "3")
	_ = st.SetSetting("mcp.refresh_skew_s", "600")
	_ = st.SetSetting("mcp.net_timeout_s", "7")
	if ttl, skew, wait := e.MCPLoginLimits(); ttl != 3*time.Minute || skew != 600*time.Second || wait != 7*time.Second {
		t.Errorf("настройки не прочитались: %v %v %v", ttl, skew, wait)
	}
	if _, err := e.MCPLoginBegin(context.Background(), "sing", testOrigin, testWho); err != nil {
		t.Fatal(err)
	}
	if _, until, _ := e.MCPLoginPending("sing"); time.Until(until) > 3*time.Minute || time.Until(until) < 2*time.Minute {
		t.Errorf("срок начатого входа %v", time.Until(until))
	}
	loginThrough(t, e, fake, "sing")
	// токен живёт час, а продлевать велено за десять минут: пока не пора
	if _, err := e.mcpAccessToken(context.Background(), "sing", ""); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.TokenForms()); n != 1 {
		t.Fatalf("обращений к выдаче токенов: %d", n)
	}
	tokens, _ := e.readMCPTokens("sing", "тест")
	tokens.ExpiresAt = time.Now().Add(9 * time.Minute).Format(time.RFC3339)
	_ = e.saveMCPTokens("sing", tokens)
	if _, err := e.mcpAccessToken(context.Background(), "sing", ""); err != nil {
		t.Fatal(err)
	}
	if forms := fake.TokenForms(); len(forms) != 2 || forms[1].Get("grant_type") != "refresh_token" {
		t.Errorf("за девять минут до конца при пределе десять токен не продлён: %v", forms)
	}
}

// Сервер, живущий у агента, вход не затрагивает: ни строки «Вход», ни
// перевода ошибки в «нужен вход».
func TestMCPAgentServerNotTouchedByLogin(t *testing.T) {
	e, st, _ := mcpTestEngine(t)
	if err := st.SaveMCPServer(&store.MCPServer{Name: "мак-код", Agent: "мак", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	m, _ := st.MCPServer("мак-код")
	boom := &mcp.AuthError{Challenge: &mcp.Challenge{Scope: "x"}}
	if got := e.mcpFailure(m, boom, false); got != error(boom) {
		t.Errorf("ошибка сервера агента переведена: %v", got)
	}
	if v := e.MCPLoginView(m); v.Has || v.Pending {
		t.Errorf("у сервера агента есть вход: %+v", v)
	}
	if m2, _ := st.MCPServer("мак-код"); m2.State == "login" {
		t.Error("сервер агента переведён в «нужен вход»")
	}
	if _, open := st.OpenNotificationByKey("mcp_login:мак-код"); open {
		t.Error("у сервера агента поднято уведомление о входе")
	}
}

// Сервер, пускающий рукопожатие без входа и отказывающий на инструментах:
// проверка видит 401 на tools/list, и вход начинается, хотя initialize
// проходит без токена.
func TestMCPLoginWhenOnlyToolsNeedIt(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	fake.InitOpen = true
	addMCP(t, st, "sing", fake.URL())
	if err := e.CheckMCP(context.Background(), "sing"); err == nil {
		t.Fatal("проверка без входа прошла")
	}
	if m, _ := st.MCPServer("sing"); m.State != "login" {
		t.Fatalf("состояние: %q / %q", m.State, m.Note)
	}
	loginThrough(t, e, fake, "sing")
	if m, _ := st.MCPServer("sing"); m.State != "ok" || len(m.Tools) != 2 {
		t.Errorf("после входа: %q %v", m.State, m.Tools)
	}
}

// Неудачная попытка войти заново не отменяет вход, который ещё работает.
func TestMCPFailedRelaunchKeepsWorkingLogin(t *testing.T) {
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	fake := mcptest.New(t, false)
	addMCP(t, st, "sing", fake.URL())
	loginThrough(t, e, fake, "sing")
	link, err := e.MCPLoginBegin(context.Background(), "sing", testOrigin, testWho)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.MCPLoginFinish(context.Background(), mustQuery(t, link, "state"), "", "access_denied", "передумал", testWho); err == nil {
		t.Fatal("отказ принят за успех")
	}
	m, _ := st.MCPServer("sing")
	if m.State != "ok" || !strings.Contains(m.Note, "вход не завершён") || !e.MCPLoginView(m).Has {
		t.Errorf("после неудачной попытки: %q / %q", m.State, m.Note)
	}
	if res := e.RequestMCP(context.Background(), "тест", "sing", "task_list", nil); res.Err != "" {
		t.Errorf("прежний вход перестал работать: %+v", res)
	}
}
