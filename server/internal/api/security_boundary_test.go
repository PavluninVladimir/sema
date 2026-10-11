package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrivateNetworkIsNotOwner(t *testing.T) {
	s, st := testServer(t)
	// «своя» только сама машина — так Сёма стоит за чужой сетью
	if err := st.SetSetting("auth.local_scope", "loopback"); err != nil {
		t.Fatal(err)
	}
	s.auth.LoadLocalScope()
	defer func() { _ = st.SetSetting("auth.local_scope", "private"); s.auth.LoadLocalScope() }()
	for _, ip := range []string{"10.0.0.4", "172.17.0.1", "192.168.1.5"} {
		if w := doRequest(s, "GET", "/api/ui", "", ip+":1234", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", ip, w.Code)
		}
		if w := doRequest(s, "POST", "/api/auth/register/begin", "", ip+":1234", ""); w.Code != http.StatusForbidden {
			t.Fatalf("bootstrap %s: %d", ip, w.Code)
		}
	}
}

func TestRebindingAndAuthCSRF(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/api/ui", "/api/auth/state", "/api/auth/register/begin"} {
		r := httptest.NewRequest("GET", "http://attacker.invalid"+path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Errorf("rebind %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/auth/register/begin", "/api/auth/handoff", "/api/auth/logout", "/api/agents/enroll"} {
		for _, origin := range []string{"https://attacker.invalid", "https://localhost:8765", "null"} {
			r := httptest.NewRequest("POST", "http://localhost:8765"+path, nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 403 {
				t.Errorf("CSRF %s %s: %d", path, origin, w.Code)
			}
		}
	}
}

func TestLimitedActionsCannotEscalate(t *testing.T) {
	s, st := testServer(t)
	token, err := st.AddToken("limited", []string{"ui", "actions"})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"add_token", "save_model", "run_script", "save_auth", "approve_ssh", "grant_module", "future_admin_action"} {
		body := `{"action":"` + action + `","screen":"health","params":{}}`
		if w := doRequest(s, "POST", "/api/action", body, "203.0.113.10:1234", token); w.Code != 403 {
			t.Errorf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	// Разрешённое действие не может вернуть административный экран.
	w := doRequest(s, "POST", "/api/action", `{"action":"refresh","screen":"settings:access"}`, "203.0.113.10:1234", token)
	if w.Code != 403 || strings.Contains(w.Body.String(), "sema_") {
		t.Fatal("access through action")
	}
}

// Дома мак и телефон ходят к Сёме по сети: у Сёмы, работавшего до
// open-source, частная сеть своя, пока это не выключили (testServer — такой
// Сёма; прежние значения ему записала миграция). Это осознанная настройка, а
// не дыра — за чужой сетью её выключают, и тогда вход нужен всем. У новой
// установки своя только петля — TestFreshInstallIsClosed.
func TestPrivateNetworkIsOwnByDefault(t *testing.T) {
	s, _ := testServer(t)
	for _, ip := range []string{"192.168.1.5", "10.0.0.4"} {
		if w := doRequest(s, "GET", "/api/ui", "", ip+":1234", ""); w.Code != http.StatusOK {
			t.Fatalf("%s из своей сети: %d", ip, w.Code)
		}
	}
	if w := doRequest(s, "GET", "/api/ui", "", "203.0.113.9:1234", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("чужой адрес прошёл без входа: %d", w.Code)
	}
}

// Под чужим именем Сёма не отвечает даже своей сети (подмена DNS), но
// имена из настройки и адреса числом — можно: rebinding живёт на именах.
func TestHostNamesFromLocalNetwork(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting("auth.local_hosts", "sema.home.arpa, Sema.Home"); err != nil {
		t.Fatal(err)
	}
	try := func(host string) int {
		r := httptest.NewRequest("GET", "http://"+host+"/api/ui", nil)
		r.RemoteAddr = "192.168.1.5:1234"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	for _, host := range []string{"sema.home.arpa:8765", "sema.home", "192.168.88.10:8765", "localhost:8765"} {
		if code := try(host); code != http.StatusOK {
			t.Errorf("%s: %d, ждали 200", host, code)
		}
	}
	if code := try("attacker.invalid:8765"); code != http.StatusForbidden {
		t.Errorf("чужое имя прошло: %d", code)
	}
}
