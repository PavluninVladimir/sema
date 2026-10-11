package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/mcp"
	"github.com/PavluninVladimir/sema/server/internal/mcp/mcptest"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вход документом клиента (Client ID Metadata Document) и повторы обмена
// токенов на invalid_client: сервер входа за балансировщиком (Planner,
// 30 сентября 2026) отвечал на один и тот же запрос через раз «клиент
// неизвестен», потому что зарегистрированного клиента помнил не каждый его
// узел. Документ клиента не хранится нигде, и его знает любой узел.

const publicOrigin = "https://sema.example.org"

// cimdSetup — движок с публичным адресом и сервер входа, принимающий клиента
// по документу. Документ «скачивается» у самого движка: так сервер входа
// увидел бы то, что отдаёт ручка. Пауза между повторами — миллисекунда,
// чтобы тест не ждал секунду на каждый invalid_client.
func cimdSetup(t *testing.T) (*Engine, *store.Store, *mcptest.Server) {
	t.Helper()
	openLoginNet(t)
	e, st, _ := mcpTestEngine(t)
	_ = st.SetSetting("auth.origin", publicOrigin)
	_ = st.SetSetting("mcp.token_retry_ms", "1")
	fake := mcptest.New(t, false)
	fake.CIMD = true
	fake.FetchDoc = func(id string) ([]byte, int, error) {
		want, ok := e.MCPClientDocumentURL()
		body, docOK := e.MCPClientDocument()
		if !ok || !docOK || id != want {
			return nil, 404, nil
		}
		return body, 200, nil
	}
	addMCP(t, st, "sing", fake.URL())
	return e, st, fake
}

// begin — «Войти» с указанного адреса и страница согласия.
func begin(t *testing.T, e *Engine, fake *mcptest.Server, origin string) (link, code, state string) {
	t.Helper()
	link, err := e.MCPLoginBegin(context.Background(), "sing", origin, testWho)
	if err != nil {
		t.Fatalf("начало входа: %v", err)
	}
	code, state, err = fake.Approve(link)
	if err != nil {
		t.Fatalf("страница согласия: %v", err)
	}
	return link, code, state
}

func finish(e *Engine, state, code string) error {
	_, err := e.MCPLoginFinish(context.Background(), state, code, "", "", testWho)
	return err
}

// Полный вход по документу: регистрации нет, client_id — адрес документа,
// сервер входа сам скачал документ и сверил адрес возврата, продление идёт с
// тем же client_id, в таблицу зарегистрированных клиентов ничего не пишется.
func TestMCPLoginByClientDocument(t *testing.T) {
	e, st, fake := cimdSetup(t)
	docURL := publicOrigin + "/mcp/oauth/client.json"

	link, code, state := begin(t, e, fake, publicOrigin)
	q := mustQuery(t, link, "client_id")
	if q != docURL {
		t.Fatalf("client_id = %q, ждали адрес документа %q", q, docURL)
	}
	if got := mustQuery(t, link, "redirect_uri"); got != publicOrigin+"/mcp/oauth/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if len(fake.Registrations()) != 0 {
		t.Fatalf("Сёма регистрировалась, хотя сервер принимает документ: %v", fake.Registrations())
	}
	if err := finish(e, state, code); err != nil {
		t.Fatalf("возврат: %v", err)
	}
	if len(fake.Registrations()) != 0 {
		t.Error("регистрация появилась при обмене кода")
	}
	// документ скачивали дважды: на странице согласия и при обмене кода
	if got := fake.DocFetches(); len(got) != 2 || got[0] != docURL {
		t.Errorf("скачивания документа: %v", got)
	}
	if _, known := st.MCPClient("sing", publicOrigin+MCPCallbackPath, fake.Auth.URL); known {
		t.Error("в mcp_clients лежит клиент, которого не регистрировали")
	}
	m, _ := st.MCPServer("sing")
	if m.State != "ok" || len(m.Tools) != 2 {
		t.Fatalf("после входа: %q / %q", m.State, m.Note)
	}
	plain, _ := st.SecretValue("mcp:sing:oauth")
	if !strings.Contains(plain, `"client_id":"`+docURL+`"`) {
		t.Errorf("в записи входа нет client_id-документа: %s", plain)
	}

	// продление: тот же client_id, сервер входа снова скачивает документ
	fake.KillAccess()
	res := e.RequestMCP(context.Background(), "тест", "sing", "task_list", nil)
	if res.Err != "" {
		t.Fatalf("вызов после отзыва токена: %+v", res)
	}
	forms := fake.TokenForms()
	if f := forms[len(forms)-1]; f.Get("grant_type") != "refresh_token" || f.Get("client_id") != docURL {
		t.Errorf("форма продления: %v", f)
	}
	if got := len(fake.DocFetches()); got != 3 {
		t.Errorf("документ скачан %d раз, ждали 3 (согласие, обмен, продление)", got)
	}
}

// Документ, как его видит сервер входа: адрес в нём совпадает с адресом
// документа байт в байт, адрес возврата один и на том же хосте, секретов нет,
// а сам адрес годится в client_id по черновику.
func TestMCPClientDocumentContent(t *testing.T) {
	e, st, _ := cimdSetup(t)
	raw, ok := e.MCPClientDocument()
	docURL, _ := e.MCPClientDocumentURL()
	if !ok || docURL != publicOrigin+MCPClientDocPath {
		t.Fatalf("документ: %v %q", ok, docURL)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("не JSON: %v\n%s", err, raw)
	}
	if doc["client_id"] != docURL {
		t.Errorf("client_id %v ≠ адрес документа %q", doc["client_id"], docURL)
	}
	if err := mcp.CheckClientIDURL(doc["client_id"].(string)); err != nil {
		t.Errorf("client_id не годится по черновику: %v", err)
	}
	for k, want := range map[string]any{"client_name": "Сёма", "client_uri": publicOrigin, "token_endpoint_auth_method": "none"} {
		if doc[k] != want {
			t.Errorf("%s = %v, ждали %v", k, doc[k], want)
		}
	}
	for k, want := range map[string][]string{
		"redirect_uris":  {publicOrigin + "/mcp/oauth/callback"},
		"grant_types":    {"authorization_code", "refresh_token"},
		"response_types": {"code"},
	} {
		got, _ := json.Marshal(doc[k])
		exp, _ := json.Marshal(want)
		if string(got) != string(exp) {
			t.Errorf("%s = %s, ждали %s", k, got, exp)
		}
	}
	for _, banned := range []string{"secret", "sema.local", "localhost", "127.0.0.1", "jwks"} {
		if bytes.Contains(bytes.ToLower(raw), []byte(banned)) {
			t.Errorf("в публичном документе есть %q:\n%s", banned, raw)
		}
	}
	if len(raw) > 2<<10 {
		t.Errorf("документ на %d байт: сервер входа вправе читать не больше пяти килобайт", len(raw))
	}

	// правка адреса входа — и документ следует за ней сам, из настроек
	_ = st.SetSetting("auth.origin", "https://other.example.org/")
	raw, _ = e.MCPClientDocument()
	if !bytes.Contains(raw, []byte(`"client_id":"https://other.example.org/mcp/oauth/client.json"`)) {
		t.Errorf("документ не последовал за настройкой:\n%s", raw)
	}
}

// Документа нет и вход идёт регистрацией, когда публичного https-адреса у
// Сёмы нет: сервер входа его не скачает, а внутренний адрес наружу не нужен.
func TestMCPClientDocumentNeedsPublicOrigin(t *testing.T) {
	for _, origin := range []string{
		"", "http://localhost:8765", "https://localhost", "https://sema.local:8443", "https://sema",
		"http://sema.example.org", "https://192.168.88.10:8443", "https://10.0.0.5", "https://sema.home.arpa",
		"https://sema.example.org/путь", "https://user:pw@sema.example.org",
	} {
		e, st, fake := cimdSetup(t)
		_ = st.SetSetting("auth.origin", origin)
		if raw, ok := e.MCPClientDocument(); ok {
			t.Errorf("auth.origin=%q: документ отдаётся:\n%s", origin, raw)
		}
		link, code, state := begin(t, e, fake, testOrigin)
		if got := mustQuery(t, link, "client_id"); got != "cid-1" {
			t.Errorf("auth.origin=%q: client_id = %q, ждали регистрацию cid-1", origin, got)
		}
		if err := finish(e, state, code); err != nil {
			t.Errorf("auth.origin=%q: вход регистрацией не прошёл: %v", origin, err)
		}
		if n := len(fake.Registrations()); n != 1 || len(fake.DocFetches()) != 0 {
			t.Errorf("auth.origin=%q: регистраций %d, скачиваний документа %d", origin, n, len(fake.DocFetches()))
		}
	}
}

// Настройка `mcp.client_metadata` = 0: регистрация, как раньше, даже когда
// документ был бы возможен. Сам документ при этом отдаётся: вход, начатый им
// раньше, продлевается им же.
func TestMCPClientMetadataSwitch(t *testing.T) {
	e, st, fake := cimdSetup(t)
	if !e.MCPClientMetadataOn() {
		t.Fatal("по умолчанию документ клиента должен быть включён")
	}
	_ = st.SetSetting("mcp.client_metadata", "0")
	if e.MCPClientMetadataOn() {
		t.Fatal("настройка 0 не выключила документ")
	}
	link, code, state := begin(t, e, fake, publicOrigin)
	if got := mustQuery(t, link, "client_id"); got != "cid-1" || len(fake.Registrations()) != 1 {
		t.Errorf("при mcp.client_metadata=0 client_id %q, регистраций %d", got, len(fake.Registrations()))
	}
	if err := finish(e, state, code); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.MCPClientDocument(); !ok {
		t.Error("выключенная настройка сняла и саму ручку: продление входа по документу осталось бы без документа")
	}
}

// Вход начат с другого адреса (sema.local), чем тот, где живёт документ:
// адрес возврата в документе не покрыл бы этот вход, поэтому — регистрация.
// А если регистрации сервер не даёт, ошибка говорит, откуда начинать.
func TestMCPLoginFromOtherOriginUsesRegistration(t *testing.T) {
	e, _, fake := cimdSetup(t)
	link, code, state := begin(t, e, fake, "https://sema.local:8443")
	if got := mustQuery(t, link, "client_id"); got != "cid-1" {
		t.Errorf("client_id = %q, ждали регистрацию", got)
	}
	if got := mustQuery(t, link, "redirect_uri"); got != "https://sema.local:8443/mcp/oauth/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if err := finish(e, state, code); err != nil {
		t.Fatal(err)
	}

	e2, _, fake2 := cimdSetup(t)
	fake2.NoRegistration = true
	_, err := e2.MCPLoginBegin(context.Background(), "sing", "https://sema.local:8443", testWho)
	if err == nil || !strings.Contains(err.Error(), "только по документу") || !strings.Contains(err.Error(), publicOrigin) {
		t.Errorf("без регистрации и с чужого адреса: %v", err)
	}
	if _, err := e2.MCPLoginBegin(context.Background(), "sing", publicOrigin, testWho); err != nil {
		t.Errorf("с публичного адреса вход по документу должен идти и без регистрации: %v", err)
	}
}

// Сервер, который документов не принимает, получает регистрацию, как раньше.
func TestMCPLoginRegistersWhenServerHasNoDocuments(t *testing.T) {
	e, _, fake := cimdSetup(t)
	fake.CIMD = false
	link, code, state := begin(t, e, fake, publicOrigin)
	if got := mustQuery(t, link, "client_id"); got != "cid-1" || len(fake.Registrations()) != 1 {
		t.Errorf("client_id %q, регистраций %d", got, len(fake.Registrations()))
	}
	if err := finish(e, state, code); err != nil {
		t.Fatal(err)
	}
}

// Сервер отвечает invalid_client через раз: обмен кода повторяется и проходит.
// Код при неудачных попытках не тратится (сервер отказывает до проверок), так
// что повтор — это тот же код.
func TestMCPExchangeSurvivesInvalidClient(t *testing.T) {
	for _, fails := range []int{1, 2, 3} {
		e, st, fake := cimdSetup(t)
		fake.InvalidClientNext = fails
		var logged bytes.Buffer
		log.SetOutput(&logged)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })

		_, code, state := begin(t, e, fake, publicOrigin)
		if err := finish(e, state, code); err != nil {
			t.Fatalf("%d invalid_client подряд: обмен не пережил: %v", fails, err)
		}
		log.SetOutput(os.Stderr)
		if got := fake.TokenCalls(); got != fails+1 {
			t.Errorf("%d отказов: запросов к серверу выдач %d, ждали %d", fails, got, fails+1)
		}
		m, _ := st.MCPServer("sing")
		if m.State != "ok" {
			t.Errorf("%d отказов: %q / %q", fails, m.State, m.Note)
		}
		// одна строка итога, без кода и токенов
		lines := 0
		for _, l := range strings.Split(logged.String(), "\n") {
			if strings.Contains(l, "обмен кода") {
				lines++
				if !strings.Contains(l, "invalid_client") || strings.Contains(l, code) || strings.Contains(l, "at-") || strings.Contains(l, "rt-") {
					t.Errorf("строка журнала: %q", l)
				}
			}
		}
		if lines != 1 {
			t.Errorf("%d отказов: строк об обмене в журнале %d, ждали одну\n%s", fails, lines, logged.String())
		}
	}
}

// Режим «через раз» целиком: первый запрос отказ, второй проходит.
func TestMCPExchangeAlternatingInvalidClient(t *testing.T) {
	e, _, fake := cimdSetup(t)
	fake.InvalidClientAlternate = true
	_, code, state := begin(t, e, fake, publicOrigin)
	if err := finish(e, state, code); err != nil {
		t.Fatalf("обмен через раз: %v", err)
	}
	if got := fake.TokenCalls(); got != 2 {
		t.Errorf("запросов к серверу выдач %d, ждали 2", got)
	}
}

// Четвёртый invalid_client подряд — честная ошибка: причина, число попыток,
// совет. Клиент, названный регистрацией, забывается: в следующий раз Сёма
// зарегистрируется заново, а не будет держаться за неизвестного серверу.
func TestMCPExchangeGivesUpAfterRetries(t *testing.T) {
	e, st, fake := cimdSetup(t)
	_ = st.SetSetting("mcp.client_metadata", "0")
	fake.InvalidClientNext = 4
	_, code, state := begin(t, e, fake, publicOrigin)
	err := finish(e, state, code)
	if err == nil {
		t.Fatal("четвёртый invalid_client подряд принят за успех")
	}
	for _, want := range []string{"invalid_client", "попыток подряд: 4", "нажмите «Войти» ещё раз"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	if got := fake.TokenCalls(); got != 4 {
		t.Errorf("запросов к серверу выдач %d, ждали ровно 4 (первый и три повтора)", got)
	}
	m, _ := st.MCPServer("sing")
	if m.State != "login" || !strings.Contains(m.Note, "нажмите «Войти» ещё раз") || m.Login != nil {
		t.Errorf("строка сервера: %q / %q", m.State, m.Note)
	}
	if _, known := st.MCPClient("sing", publicOrigin+MCPCallbackPath, fake.Auth.URL); known {
		t.Error("регистрация, которую сервер не узнаёт, осталась в справочнике")
	}
	// следующий вход регистрирует заново и проходит
	_, code, state = begin(t, e, fake, publicOrigin)
	if err := finish(e, state, code); err != nil {
		t.Fatalf("повторный вход: %v", err)
	}
	if len(fake.Registrations()) != 2 {
		t.Errorf("регистраций %d, ждали 2 (заново после отказа)", len(fake.Registrations()))
	}
}

// То же по документу: в строке сервера — адрес документа, который должен
// открываться из интернета, а регистрацию никто не забывает: её нет.
func TestMCPExchangeGivesUpNamesTheDocument(t *testing.T) {
	e, st, fake := cimdSetup(t)
	fake.InvalidClientNext = 4
	_, code, state := begin(t, e, fake, publicOrigin)
	err := finish(e, state, code)
	if err == nil {
		t.Fatal("отказ принят за успех")
	}
	docURL := publicOrigin + MCPClientDocPath
	m, _ := st.MCPServer("sing")
	for _, want := range []string{docURL, "из интернета", "нажмите «Войти» ещё раз"} {
		if !strings.Contains(m.Note, want) {
			t.Errorf("в строке сервера нет %q: %s", want, m.Note)
		}
	}
}

// Ошибки, кроме invalid_client, не повторяются; и повторы можно выключить.
func TestMCPRetriesOnlyInvalidClient(t *testing.T) {
	e, st, fake := cimdSetup(t)
	// «код уже использован» — один запрос, без повторов
	_, code, state := begin(t, e, fake, publicOrigin)
	if err := finish(e, state, code); err != nil {
		t.Fatal(err)
	}
	fake.KillAccess()
	fake.RejectRefresh = true
	before := fake.TokenCalls()
	res := e.RequestMCP(context.Background(), "тест", "sing", "task_list", nil)
	if !strings.Contains(res.Err, "истёк") {
		t.Fatalf("вызов с мёртвым входом: %+v", res)
	}
	if got := fake.TokenCalls() - before; got != 1 {
		t.Errorf("invalid_grant повторялся: %d запросов к серверу выдач", got)
	}

	// повторы выключены — один запрос, ошибка сразу
	_ = st.SetSetting("mcp.token_retries", "0")
	fake.RejectRefresh = false
	fake.InvalidClientNext = 1
	_, code, state = begin(t, e, fake, publicOrigin)
	before = fake.TokenCalls()
	if err := finish(e, state, code); err == nil {
		t.Fatal("при mcp.token_retries=0 отказ пережит")
	}
	if got := fake.TokenCalls() - before; got != 1 {
		t.Errorf("при mcp.token_retries=0 запросов %d", got)
	}
	if tries, _ := e.MCPRetryLimits(); tries != 0 {
		t.Errorf("mcp.token_retries=0 прочитан как %d", tries)
	}
}

// Продление тоже переживает invalid_client, а после четвёртого подряд вход
// признаётся потерянным — с честной причиной.
func TestMCPRefreshRetriesInvalidClient(t *testing.T) {
	e, st, fake := cimdSetup(t)
	_, code, state := begin(t, e, fake, publicOrigin)
	if err := finish(e, state, code); err != nil {
		t.Fatal(err)
	}
	fake.KillAccess()
	fake.InvalidClientNext = 2
	res := e.RequestMCP(context.Background(), "тест", "sing", "task_list", nil)
	if res.Err != "" {
		t.Fatalf("продление не пережило два invalid_client: %+v", res)
	}
	fake.KillAccess()
	fake.InvalidClientNext = 4
	res = e.RequestMCP(context.Background(), "тест", "sing", "task_list", nil)
	if !strings.Contains(res.Err, "вход в сервер MCP «sing» истёк") {
		t.Fatalf("четыре отказа подряд: %+v", res)
	}
	m, _ := st.MCPServer("sing")
	if m.State != "login" || !strings.Contains(m.Note, "invalid_client") || !strings.Contains(m.Note, "попыток подряд: 4") {
		t.Errorf("строка сервера после потери входа: %q / %q", m.State, m.Note)
	}
}

// Человека вернули на Сёму с error=invalid_client: причина и совет «нажмите
// “Войти” ещё раз» — в строке сервера. Зарегистрированный клиент забывается.
func TestMCPReturnedWithInvalidClient(t *testing.T) {
	e, st, fake := cimdSetup(t)
	_ = st.SetSetting("mcp.client_metadata", "0")
	_, _, state := begin(t, e, fake, publicOrigin)
	_, err := e.MCPLoginFinish(context.Background(), state, "", "invalid_client", "Invalid client configuration", testWho)
	if err == nil {
		t.Fatal("возврат с ошибкой принят за успех")
	}
	m, _ := st.MCPServer("sing")
	for _, want := range []string{"invalid_client", "Invalid client configuration", "нажмите «Войти» ещё раз", "балансировщиком"} {
		if !strings.Contains(m.Note, want) {
			t.Errorf("в строке сервера нет %q: %s", want, m.Note)
		}
	}
	if _, known := st.MCPClient("sing", publicOrigin+MCPCallbackPath, fake.Auth.URL); known {
		t.Error("клиент, которого сервер не узнал, остался в справочнике")
	}

	// с другого адреса, когда документ был возможен, совет говорит, откуда начинать
	_ = st.SetSetting("mcp.client_metadata", "1")
	_, _, state = begin(t, e, fake, "https://sema.local:8443")
	_, _ = e.MCPLoginFinish(context.Background(), state, "", "invalid_client", "", testWho)
	m, _ = st.MCPServer("sing")
	if !strings.Contains(m.Note, "надёжнее: начните вход с "+publicOrigin) || !strings.Contains(m.Note, "нажмите «Войти» ещё раз") {
		t.Errorf("совет с другого адреса: %s", m.Note)
	}
}

// Адрес документа проверяется по черновику: только https, путь есть, ни
// запроса, ни фрагмента, ни учётных данных, ни «..».
func TestCheckClientIDURL(t *testing.T) {
	ok := []string{"https://sema.example.org/mcp/oauth/client.json", "https://sema.example.org:8443/client.json"}
	bad := []string{"http://sema.example.org/client.json", "https://sema.example.org", "https://sema.example.org/",
		"https://sema.example.org/a?b=1", "https://sema.example.org/a#f", "https://u:p@sema.example.org/a",
		"https://sema.example.org/a/../b", "https://sema.example.org/./a", "", "сёма"}
	for _, u := range ok {
		if err := mcp.CheckClientIDURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := mcp.CheckClientIDURL(u); err == nil {
			t.Errorf("%q принят", u)
		}
	}
	if _, err := url.Parse(publicOrigin + MCPClientDocPath); err != nil {
		t.Fatal(err)
	}
}
