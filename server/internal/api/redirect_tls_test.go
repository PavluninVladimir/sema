package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Открытый порт ведёт страницу на защищённый, когда тот работает (perf-pass
// 7.4), и не трогает ни API, ни запросы по адресу, ни прокси.
func TestOpenPortRedirectsPagesToTLS(t *testing.T) {
	s, st := testServer(t)
	try := func(method, target, host string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r.Host = host
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		if !s.redirectToTLS(w, r) {
			w.Code = 0
		}
		return w
	}
	if w := try("GET", "/", "sema.local:8765", nil); w.Code != 0 {
		t.Fatal("защищённого порта нет, а страница переадресована")
	}
	tlsPort.Store("8443")
	defer tlsPort.Store("")
	w := try("GET", "/?x=1", "sema.local:8765", nil)
	if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "https://sema.local:8443/?x=1" {
		t.Fatalf("страница: %d %q", w.Code, w.Header().Get("Location"))
	}
	// постоянную переадресацию браузер запомнил бы навсегда: выключенная
	// настройка и упавший TLS не вернули бы http-закладку к жизни
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("переадресация кэшируется браузером: Cache-Control %q", cc)
	}
	for name, c := range map[string]struct {
		method, target, host string
		hdr                  map[string]string
	}{
		"API":          {"GET", "/api/health", "sema.local:8765", nil},
		"экран":        {"GET", "/ui?screen=files", "sema.local:8765", nil},
		"действие":     {"POST", "/", "sema.local:8765", nil},
		"по адресу":    {"GET", "/", "192.168.1.20:8765", nil},
		"localhost":    {"GET", "/", "localhost:8765", nil},
		"через прокси": {"GET", "/", "sema.local:8765", map[string]string{"X-Forwarded-For": "10.0.0.2"}},
		"ссылка файла": {"GET", "/share/1.2.x", "sema.local:8765", nil},
	} {
		if w := try(c.method, c.target, c.host, c.hdr); w.Code != 0 {
			t.Errorf("%s: переадресован (%d), а не должен", name, w.Code)
		}
	}
	if err := st.SetSetting(engine.RedirectTLSKey, "0"); err != nil {
		t.Fatal(err)
	}
	if w := try("GET", "/", "sema.local:8765", nil); w.Code != 0 {
		t.Error("настройка выключена, а страница переадресована")
	}
}
