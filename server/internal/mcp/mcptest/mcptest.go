// Пакет mcptest — поддельный сервер MCP вместе с сервером авторизации OAuth
// для тестов клиента, входа и экранов «Интеграции». Настоящей сети не
// касается: всё на httptest. Подключается только из тестов, в бинарник не
// попадает.
//
// Сервер честный там, где тест обязан ловить ошибки клиента: код входа
// проверяется настоящим PKCE (S256), redirect_uri и client_id должны
// совпасть с зарегистрированными, resource — с адресом сервера MCP, токены
// после обновления ротируются (старый refresh_token умирает).
//
// Клиента можно назвать двумя способами. Регистрацией (`/oauth/register`) —
// как у серверов, что её принимают. Адресом документа клиента (CIMD, при
// `CIMD = true`): сервер сам скачивает документ по client_id, сверяет
// client_id в нём с адресом, а redirect_uri — с перечисленными, как это
// делают настоящие серверы входа. Для сервера за балансировщиком есть
// режим «через раз invalid_client» у выдачи токенов.
package mcptest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Req — один запрос, дошедший до сервера MCP.
type Req struct {
	Method   string // JSON-RPC: initialize, tools/list…; пусто, если тела не было
	HasID    bool   // у запроса был id (у уведомления его нет)
	Auth     string // заголовок Authorization
	Session  string // Mcp-Session-Id
	Protocol string // MCP-Protocol-Version
	Status   int    // чем ответили
}

// ToolDef — инструмент с описанием и схемой аргументов. Пусто — сервер
// называет только `ToolNames`, с пустой схемой.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // inputSchema как есть
}

// Call — один вызов инструмента, дошедший до сервера.
type Call struct {
	Name string
	Args map[string]any
}

// Server — сервер MCP (`MCP.URL + "/mcp"`) и сервер авторизации (`Auth.URL`).
// При SameOrigin это один и тот же httptest.Server.
type Server struct {
	MCP  *httptest.Server
	Auth *httptest.Server

	mu sync.Mutex

	// ---- ручки теста ----
	AuthRequired     bool // без токена — 401 (по умолчанию да)
	Scopes           []string
	AccessTTL        int    // секунд жизни токена (expires_in); 0 — 3600; <0 — не называть срок
	RejectRefresh    bool   // обновление отвечает invalid_grant
	RefreshTransient bool   // обновление отвечает 500
	NoRegistration   bool   // у сервера авторизации нет registration_endpoint
	RegisterError    string // регистрация отказывает с этим пояснением
	ProtocolReply    string // какую версию протокола называет initialize (пусто — ту, что просили)
	Stateless        bool   // сервер без Mcp-Session-Id
	SSE              bool   // отвечать потоком событий с посторонними сообщениями
	NoPKCE           bool   // в метаданных нет S256
	OpenIDOnly       bool   // метаданные только по /.well-known/openid-configuration
	NoResourceMeta   bool   // метаданных ресурса нет (прежняя редакция протокола)
	ToolNames        []string
	ToolDefs         []ToolDef                                     // если заданы, сервер называет их (с описаниями и схемами), а не ToolNames
	CallReply        func(name string, args map[string]any) string // что отвечает tools/call; nil — «вызван <имя>»
	// CallResult — ответ tools/call с признаком ошибки инструмента (`isError`):
	// так отвечают серверы, которые пересылают отказ своего REST как есть. Если
	// задан, CallReply не спрашивается.
	CallResult func(name string, args map[string]any) (text string, isError bool)
	// CallStatus — сервер отвечает на tools/call не JSON-RPC, а HTTP-статусом
	// (5xx, 4xx) с телом; 0 — обычный ответ.
	CallStatus  func(name string, args map[string]any) (status int, body string)
	InitOpen    bool   // рукопожатие пускает без входа, инструменты — нет
	AcceptToken string // постоянный ключ, который сервер принимает как Bearer наравне с выданными

	CIMD                   bool // объявляет client_id_metadata_document_supported и принимает клиента по адресу документа
	InvalidClientNext      int  // следующие столько запросов к выдаче токенов отвечают invalid_client (код и токен не тратятся)
	InvalidClientAlternate bool // через раз: первый, третий… запрос к выдаче токенов отвечает invalid_client
	// FetchDoc — как сервер скачивает документ клиента. nil — обычным GET по
	// адресу (годится, когда документ стоит на httptest); тесты Сёмы подставляют
	// свою функцию: адрес документа в них «публичный» (https://sema.example.org),
	// а отдаёт его настоящая ручка или движок напрямую.
	FetchDoc func(clientID string) (body []byte, status int, err error)

	requests   []Req
	calls      []Call
	sessions   map[string]bool
	sessionSeq int
	regs       []map[string]any
	clients    map[string]map[string]any
	codes      map[string]codeInfo
	access     map[string]bool // действующие access_token
	refresh    map[string]bool // действующие refresh_token
	revoked    []string
	tokenSeq   int
	codeSeq    int
	tokenForms []url.Values
	tokenCalls int
	docFetches []string
}

type codeInfo struct {
	challenge, redirect, client, resource string
}

// New поднимает сервер. sameOrigin — MCP и сервер авторизации на одном адресе.
func New(t testing.TB, sameOrigin bool) *Server {
	t.Helper()
	s := &Server{
		AuthRequired: true,
		Scopes:       []string{"tasks:read", "tasks:write", "mcp:read"},
		ToolNames:    []string{"task_list", "task_create"},
		sessions:     map[string]bool{}, clients: map[string]map[string]any{},
		codes: map[string]codeInfo{}, access: map[string]bool{}, refresh: map[string]bool{},
	}
	mcpMux, authMux := http.NewServeMux(), http.NewServeMux()
	s.MCP = httptest.NewServer(mcpMux)
	t.Cleanup(s.MCP.Close)
	if sameOrigin {
		s.Auth = s.MCP
		authMux = mcpMux
	} else {
		s.Auth = httptest.NewServer(authMux)
		t.Cleanup(s.Auth.Close)
	}
	mcpMux.HandleFunc("/mcp", s.handleMCP)
	mcpMux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.handleResourceMeta)
	mcpMux.HandleFunc("/.well-known/oauth-protected-resource", s.handleResourceMeta)
	authMux.HandleFunc("/.well-known/oauth-authorization-server", s.handleAuthMeta)
	authMux.HandleFunc("/.well-known/openid-configuration", s.handleAuthMeta)
	authMux.HandleFunc("/oauth/register", s.handleRegister)
	authMux.HandleFunc("/oauth/token", s.handleToken)
	authMux.HandleFunc("/oauth/revoke", s.handleRevoke)
	return s
}

// URL — адрес, который записывают в справочник серверов MCP.
func (s *Server) URL() string { return s.MCP.URL + "/mcp" }

// Requests — что дошло до /mcp (копия).
func (s *Server) Requests() []Req {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Req(nil), s.requests...)
}

// Calls — какие инструменты вызывали (копия).
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Count — сколько запросов с таким методом JSON-RPC пришло.
func (s *Server) Count(method string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.Method == method {
			n++
		}
	}
	return n
}

// Registrations — тела запросов на регистрацию клиента.
func (s *Server) Registrations() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.regs...)
}

// TokenForms — формы, дошедшие до выдачи токенов.
func (s *Server) TokenForms() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.tokenForms...)
}

// TokenCalls — сколько запросов дошло до выдачи токенов, включая отвеченные
// invalid_client.
func (s *Server) TokenCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenCalls
}

// DocFetches — адреса документов клиента, которые сервер скачивал.
func (s *Server) DocFetches() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.docFetches...)
}

// Revoked — токены, которые у сервера просили отозвать.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

// ExpireSessions — сервер «перезапустился»: все сессии MCP забыты.
func (s *Server) ExpireSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]bool{}
}

// KillAccess — сервер отвергает все выданные access_token (отозваны).
func (s *Server) KillAccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = map[string]bool{}
}

// KillRefresh — все refresh_token мертвы.
func (s *Server) KillRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh = map[string]bool{}
}

// Approve — «человек нажал Разрешить на странице сервера»: проверяет адрес
// страницы согласия и возвращает code и state, которые сервер входа отдал бы
// браузеру. Ошибка — клиент построил адрес неверно.
func (s *Server) Approve(authorizeURL string) (code, state string, err error) {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(authorizeURL, s.Auth.URL+"/oauth/authorize") {
		return "", "", fmt.Errorf("страница входа не там: %s", authorizeURL)
	}
	q := u.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	if isDocumentID(q.Get("client_id")) {
		// клиент назван адресом документа: сервер скачивает его сам
		if !s.CIMD {
			return "", "", fmt.Errorf("client_id %q — адрес, а сервер клиентов по документу не принимает", q.Get("client_id"))
		}
		if err := s.checkDocument(q.Get("client_id"), q.Get("redirect_uri")); err != nil {
			return "", "", err
		}
	} else {
		reg, ok := s.clients[q.Get("client_id")]
		if !ok {
			return "", "", fmt.Errorf("client_id %q не зарегистрирован", q.Get("client_id"))
		}
		if uris, _ := reg["redirect_uris"].([]any); len(uris) == 0 || uris[0] != q.Get("redirect_uri") {
			return "", "", fmt.Errorf("redirect_uri %q не совпал с зарегистрированным", q.Get("redirect_uri"))
		}
	}
	switch {
	case q.Get("response_type") != "code":
		return "", "", fmt.Errorf("response_type %q", q.Get("response_type"))
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		return "", "", fmt.Errorf("нет PKCE S256")
	case q.Get("state") == "":
		return "", "", fmt.Errorf("нет state")
	}
	s.codeSeq++
	code = fmt.Sprintf("code-%d", s.codeSeq)
	s.codes[code] = codeInfo{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"),
		client: q.Get("client_id"), resource: q.Get("resource")}
	return code, q.Get("state"), nil
}

// isDocumentID — client_id похож на адрес документа: у CIMD это https-адрес.
func isDocumentID(id string) bool { return strings.HasPrefix(id, "https://") }

// checkDocument — то, что делает настоящий сервер входа, встретив адрес в
// client_id: скачивает документ (без переходов и не больше пяти килобайт, как
// советует черновик), сверяет client_id в нём с адресом байт в байт и
// проверяет, что redirect_uri назван в документе (пустой — не проверяется:
// при продлении токена адреса возврата нет). Зовётся под s.mu.
func (s *Server) checkDocument(clientID, redirect string) error {
	s.docFetches = append(s.docFetches, clientID)
	var (
		body   []byte
		status int
		err    error
	)
	if s.FetchDoc != nil {
		body, status, err = s.FetchDoc(clientID)
	} else {
		hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		var resp *http.Response
		if resp, err = hc.Get(clientID); err == nil {
			status = resp.StatusCode
			body, _ = io.ReadAll(io.LimitReader(resp.Body, 6<<10))
			resp.Body.Close()
		}
	}
	switch {
	case err != nil:
		return fmt.Errorf("документ клиента не скачался: %w", err)
	case status != http.StatusOK:
		return fmt.Errorf("документ клиента: HTTP %d", status)
	case len(body) > 5<<10:
		return fmt.Errorf("документ клиента больше пяти килобайт")
	}
	var doc struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		Secret       string   `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("документ клиента не разобран: %w", err)
	}
	switch {
	case doc.ClientID != clientID:
		return fmt.Errorf("client_id в документе %q не совпал с адресом %q", doc.ClientID, clientID)
	case doc.ClientName == "" || len(doc.RedirectURIs) == 0:
		return fmt.Errorf("в документе нет client_name или redirect_uris")
	case strings.HasPrefix(doc.AuthMethod, "client_secret") || doc.Secret != "":
		return fmt.Errorf("в документе клиента не бывает общего секрета")
	}
	if redirect != "" {
		for _, u := range doc.RedirectURIs {
			if u == redirect {
				return nil
			}
		}
		return fmt.Errorf("redirect_uri %q не назван в документе клиента (%v)", redirect, doc.RedirectURIs)
	}
	return nil
}

// ---- сервер MCP ----

type rpc struct {
	ID     *json.Number    `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req rpc
	_ = json.Unmarshal(body, &req)
	rec := Req{Method: req.Method, HasID: req.ID != nil, Auth: r.Header.Get("Authorization"),
		Session: r.Header.Get("Mcp-Session-Id"), Protocol: r.Header.Get("MCP-Protocol-Version")}
	reply := func(status int) {
		rec.Status = status
		s.mu.Lock()
		s.requests = append(s.requests, rec)
		s.mu.Unlock()
	}
	s.mu.Lock()
	needAuth := s.AuthRequired
	okToken := strings.HasPrefix(rec.Auth, "Bearer ") && (s.access[strings.TrimPrefix(rec.Auth, "Bearer ")] ||
		(s.AcceptToken != "" && strings.TrimPrefix(rec.Auth, "Bearer ") == s.AcceptToken))
	sessionOK := rec.Session == "" || s.sessions[rec.Session]
	stateless, initOpen := s.Stateless, s.InitOpen
	s.mu.Unlock()

	openHandshake := initOpen && (req.Method == "initialize" || req.ID == nil)
	if needAuth && !okToken && !openHandshake {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="mcp", resource_metadata="%s/.well-known/oauth-protected-resource/mcp", scope="%s"`,
			s.MCP.URL, strings.Join(s.Scopes, " ")))
		reply(http.StatusUnauthorized)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if rec.Session != "" && !sessionOK && req.Method != "initialize" {
		reply(http.StatusNotFound)
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if req.ID == nil { // уведомление
		reply(http.StatusAccepted)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	id := req.ID.String()
	var result any
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		proto := p.ProtocolVersion
		s.mu.Lock()
		if s.ProtocolReply != "" {
			proto = s.ProtocolReply
		}
		if !stateless {
			s.sessionSeq++
			sid := fmt.Sprintf("sess-%d", s.sessionSeq)
			s.sessions[sid] = true
			w.Header().Set("Mcp-Session-Id", sid)
		}
		s.mu.Unlock()
		result = map[string]any{"protocolVersion": proto, "capabilities": map[string]any{},
			"serverInfo": map[string]any{"name": "fake-mcp", "version": "1"}}
	case "tools/list":
		var tools []map[string]any
		s.mu.Lock()
		defs, names := s.ToolDefs, s.ToolNames
		s.mu.Unlock()
		if len(defs) > 0 {
			for _, d := range defs {
				schema := d.Schema
				if schema == nil {
					schema = map[string]any{"type": "object"}
				}
				tools = append(tools, map[string]any{"name": d.Name, "description": d.Description, "inputSchema": schema})
			}
		} else {
			for _, n := range names {
				tools = append(tools, map[string]any{"name": n, "description": n, "inputSchema": map[string]any{"type": "object"}})
			}
		}
		result = map[string]any{"tools": tools}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		s.calls = append(s.calls, Call{Name: p.Name, Args: p.Arguments})
		replyText, replyResult, replyStatus := s.CallReply, s.CallResult, s.CallStatus
		s.mu.Unlock()
		if replyStatus != nil {
			if code, body := replyStatus(p.Name, p.Arguments); code != 0 {
				reply(code)
				http.Error(w, body, code)
				return
			}
		}
		text, isErr := "вызван "+p.Name, false
		switch {
		case replyResult != nil:
			text, isErr = replyResult(p.Name, p.Arguments)
		case replyText != nil:
			text = replyText(p.Name, p.Arguments)
		}
		out := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		if isErr {
			out["isError"] = true
		}
		result = out
	default:
		result = map[string]any{}
	}
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	s.mu.Lock()
	sse := s.SSE
	s.mu.Unlock()
	if sse {
		// рядом с ответом идут уведомление о ходе, чужой ответ и просьба сервера
		w.Header().Set("Content-Type", "text/event-stream")
		reply(http.StatusOK)
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":9999,\"result\":{\"чужой\":true}}\n\n")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"ping\"}\n\n")
		// ответ в два data: — они склеиваются переводом строки
		half := len(msg) / 2
		for half < len(msg) && msg[half] != ',' {
			half++
		}
		fmt.Fprintf(w, "event: message\ndata: %s\ndata: %s\n\n", msg[:half+1], msg[half+1:])
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	reply(http.StatusOK)
	_, _ = w.Write(msg)
}

func (s *Server) handleResourceMeta(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	no := s.NoResourceMeta
	s.mu.Unlock()
	if no {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"resource": s.URL(), "authorization_servers": []string{s.Auth.URL},
		"scopes_supported": s.Scopes, "bearer_methods_supported": []string{"header"}})
}

// ---- сервер авторизации ----

func (s *Server) handleAuthMeta(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	openIDOnly, noReg, noPKCE := s.OpenIDOnly, s.NoRegistration, s.NoPKCE
	cimd := s.CIMD
	s.mu.Unlock()
	if openIDOnly && strings.Contains(r.URL.Path, "oauth-authorization-server") {
		http.NotFound(w, r)
		return
	}
	meta := map[string]any{
		"issuer": s.Auth.URL, "authorization_endpoint": s.Auth.URL + "/oauth/authorize",
		"token_endpoint": s.Auth.URL + "/oauth/token", "revocation_endpoint": s.Auth.URL + "/oauth/revoke",
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      s.Scopes,
	}
	if !noReg {
		meta["registration_endpoint"] = s.Auth.URL + "/oauth/register"
	}
	if cimd {
		meta["client_id_metadata_document_supported"] = true
	}
	if noPKCE {
		meta["code_challenge_methods_supported"] = []string{"plain"}
	}
	writeJSON(w, meta)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regs = append(s.regs, body)
	if s.RegisterError != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_redirect_uri", "error_description": s.RegisterError})
		return
	}
	id := fmt.Sprintf("cid-%d", len(s.regs))
	s.clients[id] = body
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"client_id": id, "redirect_uris": body["redirect_uris"]})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenCalls++
	s.tokenForms = append(s.tokenForms, r.PostForm)
	fail := func(status int, code, why string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": code, "error_description": why})
	}
	issue := func(clientID string) {
		s.tokenSeq++
		at, rt := fmt.Sprintf("at-%d", s.tokenSeq), fmt.Sprintf("rt-%d", s.tokenSeq)
		s.access[at], s.refresh[rt] = true, true
		out := map[string]any{"access_token": at, "token_type": "Bearer", "refresh_token": rt, "scope": strings.Join(s.Scopes, " ")}
		switch {
		case s.AccessTTL == 0:
			out["expires_in"] = 3600
		case s.AccessTTL > 0:
			out["expires_in"] = s.AccessTTL
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
	// сервер за балансировщиком: часть узлов клиента не знает. Код и токен при
	// этом не тратятся — отказ приходит до любой проверки
	if s.InvalidClientNext > 0 {
		s.InvalidClientNext--
		fail(http.StatusUnauthorized, "invalid_client", "Invalid client configuration")
		return
	}
	if s.InvalidClientAlternate && s.tokenCalls%2 == 1 {
		fail(http.StatusUnauthorized, "invalid_client", "Invalid client configuration")
		return
	}
	// клиент, названный адресом, проверяется по документу и здесь: сервер не
	// хранит его и скачивает заново (или берёт из кэша, а мы кэш не держим)
	if id := r.PostForm.Get("client_id"); isDocumentID(id) {
		if !s.CIMD {
			fail(http.StatusUnauthorized, "invalid_client", "клиентов по документу здесь нет")
			return
		}
		if err := s.checkDocument(id, r.PostForm.Get("redirect_uri")); err != nil {
			fail(http.StatusUnauthorized, "invalid_client", err.Error())
			return
		}
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		ci, ok := s.codes[r.PostForm.Get("code")]
		if !ok {
			fail(400, "invalid_grant", "код неизвестен или уже использован")
			return
		}
		delete(s.codes, r.PostForm.Get("code")) // код одноразовый
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		switch {
		case base64.RawURLEncoding.EncodeToString(sum[:]) != ci.challenge:
			fail(400, "invalid_grant", "PKCE: code_verifier не подходит")
		case r.PostForm.Get("redirect_uri") != ci.redirect || r.PostForm.Get("client_id") != ci.client:
			fail(400, "invalid_grant", "redirect_uri или client_id не совпали")
		case r.PostForm.Get("resource") != ci.resource:
			fail(400, "invalid_target", "resource не совпал")
		default:
			issue(ci.client)
		}
	case "refresh_token":
		rt := r.PostForm.Get("refresh_token")
		switch {
		case s.RefreshTransient:
			fail(500, "server_error", "временная беда")
		case s.RejectRefresh || !s.refresh[rt]:
			fail(400, "invalid_grant", "refresh_token отозван или уже использован")
		default:
			delete(s.refresh, rt) // ротация: старый умер
			issue(r.PostForm.Get("client_id"))
		}
	default:
		fail(400, "unsupported_grant_type", "")
	}
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	s.revoked = append(s.revoked, r.PostForm.Get("token"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
