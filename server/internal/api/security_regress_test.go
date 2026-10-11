package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Аудит 7 сентября 2026 воспроизвёл двенадцать дыр; эти тесты держат их
// закрытыми. Каждый — отдельный случай, у каждого своё «почему».

func doRequest(s *Server, method, path, body, remote, token string) *httptest.ResponseRecorder {
	if strings.HasPrefix(path, "/") {
		path = "http://localhost:8765" + path
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// Выключенный локальный вход закрывает и loopback.
func TestPrivateNetworkClosedWhenLoginRequired(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"10.23.45.67:1234", "172.20.1.2:1234", "192.168.1.99:1234", "127.0.0.1:1234"} {
		if w := doRequest(s, "GET", "/api/ui?screen=settings:access", "", addr, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: при выключенном local_open ответ %d, ждали 401", addr, w.Code)
		}
	}
}

// Форма с чужого сайта шлёт text/plain и Sec-Fetch-Site: cross-site —
// такое действие не выполняется, даже когда сеть открыта без входа.
func TestForeignSiteDoesNotRunAction(t *testing.T) {
	s, st := testServer(t)
	body := `{"action":"save_auth","screen":"settings:access","params":{"auth.rp_id":"audit.invalid"}}`
	r := httptest.NewRequest("POST", "http://localhost:8765/api/action", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Origin", "https://attacker.invalid")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusOK || st.Setting(auth.SettingRPID, "") == "audit.invalid" {
		t.Fatalf("действие с чужого сайта прошло: %d", w.Code)
	}
	// тот же запрос как JSON, но с пометкой cross-site — тоже нет
	r = httptest.NewRequest("POST", "http://localhost:8765/api/action", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site JSON: %d", w.Code)
	}
}

// Токен с правом «действия» не выписывает себе токен с правом «всё».
func TestActionsTokenDoesNotChangeAccess(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	limited, err := st.AddToken("ограниченный", []string{"actions"})
	if err != nil {
		t.Fatal(err)
	}
	w := doRequest(s, "POST", "/api/action", `{"action":"add_token","screen":"settings:access","params":{"name":"побег","scope_all":"true"}}`, "203.0.113.10:1234", limited)
	if w.Code != http.StatusForbidden || s.lastToken != "" {
		t.Fatalf("токен «действия» выписал токен: %d", w.Code)
	}
	// а токен «всё» — может
	admin, err := st.AddToken("всё", []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if w := doRequest(s, "POST", "/api/action", `{"action":"add_token","screen":"settings:access","params":{"name":"новый","scope_ui":"true"}}`, "203.0.113.10:1234", admin); w.Code != http.StatusOK {
		t.Fatalf("токен «всё» не смог выписать токен: %d %s", w.Code, w.Body.String())
	}
}

// Новый токен показывается недолго: три минуты, потом экран его не несёт.
func TestNewTokenShownBriefly(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accessActions(actionRequest{Action: "add_token", Params: map[string]string{"name": "админ", "scope_all": "true"}}); err != nil {
		t.Fatal(err)
	}
	admin := s.lastToken
	reader, err := st.AddToken("читатель", []string{"ui"})
	if err != nil {
		t.Fatal(err)
	}
	if w := doRequest(s, "GET", "/api/ui?screen=settings:access~tokens", "", "203.0.113.10:1234", reader); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), admin) {
		t.Fatal("ограниченный токен прочитал экран доступа")
	}
	if w := doRequest(s, "GET", "/api/ui?screen=settings:access~tokens", "", "203.0.113.10:1234", admin); !strings.Contains(w.Body.String(), admin) {
		t.Fatal("свежий токен не показан — человек не сможет его скопировать")
	}
	s.mu.Lock()
	s.lastTokenAt = time.Now().Add(-4 * time.Minute)
	s.mu.Unlock()
	if w := doRequest(s, "GET", "/api/ui?screen=settings:access~tokens", "", "203.0.113.10:1234", admin); strings.Contains(w.Body.String(), admin) {
		t.Fatal("токен виден спустя четыре минуты")
	}
	if w := doRequest(s, "GET", "/api/ui?screen=settings:access~tokens", "", "203.0.113.10:1234", admin); strings.Contains(w.Body.String(), admin) {
		t.Fatal("токен вернулся на экран")
	}
}
func TestSecurityHeaders(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/api/health", "/api/ui?screen=inbox", "/login"} {
		w := doRequest(s, "GET", path, "", "127.0.0.1:1234", "")
		for k, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff",
			"X-Frame-Options": "DENY", "Referrer-Policy": "same-origin"} {
			if got := w.Header().Get(k); got != want {
				t.Fatalf("%s: %s = %q, ждали %q", path, k, got, want)
			}
		}
	}
}

// Тело обычного запроса ограничено: экран и действие в мегабайты не
// укладываются только у того, кто хочет занять память.
func TestRequestBodyIsLimited(t *testing.T) {
	s, _ := testServer(t)
	body := `{"action":"refresh","target":"","params":{"мусор":"` + strings.Repeat("щ", 5<<20) + `"}}`
	w := doRequest(s, "POST", "/api/action", body, "127.0.0.1:1234", "")
	if w.Code == http.StatusOK {
		t.Fatalf("пятимегабайтное действие прошло: %d", w.Code)
	}
}

// HTML из письма в адресе Сёмы исполнялся бы с её cookie: отдаём на
// скачивание, в песочнице, без угадывания типа.
func TestHTMLAttachmentNotExecuted(t *testing.T) {
	s, _ := testServer(t)
	payload := `<!doctype html><script>document.title="x"</script>`
	a, _, err := s.eng.AddFile(context.Background(), "письмо.html", "text/html", []byte(payload), engine.FileSource{Kind: "mail"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := doRequest(s, "GET", fmt.Sprintf("/api/files/%d", a.ID), "", "127.0.0.1:1234", "")
	h := w.Header()
	if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") {
		t.Errorf("html отдан inline: %q", h.Get("Content-Disposition"))
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("нет песочницы: CSP=%q nosniff=%q", h.Get("Content-Security-Policy"), h.Get("X-Content-Type-Options"))
	}
	// обычная картинка по-прежнему показывается на месте
	b, _, err := s.eng.AddFile(context.Background(), "чек.png", "image/png", []byte("png"), engine.FileSource{Kind: "mail"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w := doRequest(s, "GET", fmt.Sprintf("/api/files/%d", b.ID), "", "127.0.0.1:1234", ""); !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline") {
		t.Errorf("картинка перестала показываться на месте: %q", w.Header().Get("Content-Disposition"))
	}
}

func TestFileNameWithoutPath(t *testing.T) {
	s, _ := testServer(t)
	a, _, err := s.eng.AddFile(context.Background(), "../../метка.txt", "text/plain", []byte("проба"), engine.FileSource{Kind: "upload"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := doRequest(s, "GET", fmt.Sprintf("/api/files/%d", a.ID), "", "127.0.0.1:1234", "")
	if d := w.Header().Get("Content-Disposition"); strings.Contains(d, "..") || !strings.Contains(d, urlEscape("метка.txt")) {
		t.Fatalf("имя файла с путём: %q", d)
	}
}

// Удалили ключ — умерли и его сессии: cookie не переживает отзыв.
func TestSessionDiesWithKey(t *testing.T) {
	s, st := testServer(t)
	pid, err := st.AddPasskey("проба", `{}`, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.auth.OpenSession(pid, "браузер", "203.0.113.2", "проба")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.accessActions(actionRequest{Action: "delete_passkey", Target: fmt.Sprint(pid)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.SessionByToken(token, auth.SessionTTL); ok {
		t.Fatal("сессия пережила удаление ключа")
	}
}

func TestCookieSecureOverHTTPS(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://sema.local/login", nil)
	r.TLS = &tls.ConnectionState{}
	setSessionCookie(w, r, "проба")
	if c := w.Result().Cookies(); len(c) != 1 || !c[0].Secure || !c[0].HttpOnly {
		t.Fatal("по https cookie должна быть Secure и HttpOnly")
	}
	w = httptest.NewRecorder()
	setSessionCookie(w, httptest.NewRequest("GET", "http://localhost/login", nil), "проба")
	if c := w.Result().Cookies(); len(c) != 1 || c[0].Secure {
		t.Fatal("по http Secure сломал бы вход на localhost")
	}
}
func TestForeignSiteWritesNowhere(t *testing.T) {
	s, _ := testServer(t)
	for _, c := range []struct{ path, ct, body string }{
		{"/api/push/subscribe", "text/plain", `{"endpoint":"https://evil.invalid/x","keys":{"p256dh":"a","auth":"b"}}`},
		{"/api/action", "text/plain", `{"action":"delete_mcp","target":"агент"}`},
	} {
		r := httptest.NewRequest("POST", "http://localhost:8765"+c.path, strings.NewReader(c.body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", "https://evil.invalid")
		r.Header.Set("Content-Type", c.ct)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s с чужого сайта: %d", c.path, w.Code)
		}
	}
	// свой источник проходит
	r := httptest.NewRequest("POST", "http://localhost:8765/api/action", strings.NewReader(`{"action":"нет","screen":"health","params":{}}`))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Origin", "http://localhost:8765")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusForbidden {
		t.Fatal("свой источник отвергнут")
	}
}

// Снятые ручки не отвечают оболочкой: ленты уведомлений и передачи
// сессии приложению больше нет, и клиент должен видеть отказ, а не
// страницу.
func TestRemovedEndpointsAnswerDenial(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/api/notifications/feed?after=0", "/api/auth/claim?nonce=abc", "/api/auth/handoff?nonce=abc"} {
		w := doRequest(s, "GET", path, "", "127.0.0.1:1234", "")
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s ответил %d: %s", path, w.Code, strings.TrimSpace(w.Body.String())[:60])
		}
	}
}

// Safari при политике referrer «no-referrer» обнуляет Origin и у
// собственных запросов страницы: человек видел «запрос с чужого сайта не
// выполняется» на каждой попытке сохранить. Браузер говорит, откуда
// запрос, полем Sec-Fetch-Site, и подделать его со страницы нельзя —
// «same-origin» весомее обнулённого Origin. Чужой сайт по-прежнему нет.
func TestOwnRequestWithNulledOrigin(t *testing.T) {
	свой := httptest.NewRequest("POST", "http://sema.local:8765/do", strings.NewReader("action=noop"))
	свой.Header.Set("Origin", "null")
	свой.Header.Set("Sec-Fetch-Site", "same-origin")
	if crossSite(свой) {
		t.Error("свой запрос с обнулённым Origin принят за чужой")
	}
	чужой := httptest.NewRequest("POST", "http://sema.local:8765/do", strings.NewReader("action=noop"))
	чужой.Header.Set("Origin", "null")
	if !crossSite(чужой) {
		t.Error("Origin «null» без пометки браузера должен считаться чужим")
	}
	подделка := httptest.NewRequest("POST", "http://sema.local:8765/do", strings.NewReader("action=noop"))
	подделка.Header.Set("Origin", "https://attacker.invalid")
	подделка.Header.Set("Sec-Fetch-Site", "cross-site")
	if !crossSite(подделка) {
		t.Error("cross-site пропущен")
	}
}
