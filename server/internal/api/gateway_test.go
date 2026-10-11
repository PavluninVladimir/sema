package api

// Выход наружу меняет правила доступа. Здесь проверяется связка, которую
// легко понять наоборот: шлюз всегда ставит заголовок с настоящим
// адресом обратившегося, а `IsLocal` при виде этого заголовка перестаёт
// считать запрос своим. То есть заголовок — не только подпись в журнале, но
// и то, что не даёт интернету пройти как «своя сеть».

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
)

// requestWith — запрос с одним добавленным заголовком: ровно тем, которым
// шлюз подписывает настоящего обратившегося.
func requestWith(s *Server, method, path, remote, title, значение string) *httptest.ResponseRecorder {
	if strings.HasPrefix(path, "/") {
		path = "http://localhost:8765" + path
	}
	r := httptest.NewRequest(method, path, strings.NewReader(""))
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(title, значение)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// Запрос, пришедший через шлюза, «своим» не считается никогда — даже
// когда вход из своей сети открыт и соединение по петле. Иначе публичный
// адрес пускал бы кого угодно без входа.
func TestRequestThroughGatewayIsNotLocal(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "true"); err != nil {
		t.Fatal(err)
	}
	// без заголовка петля своя: так ходит человек дома
	if w := doRequest(s, "GET", "/api/ui?screen=settings:access", "", "127.0.0.1:1234", ""); w.Code != http.StatusOK {
		t.Fatalf("дома по петле без входа ответ %d, ждали 200 — иначе проверка ниже ничего не значит", w.Code)
	}
	// тот же адрес, но с подписью шлюза — уже не свой
	for _, title := range []string{"X-Forwarded-For", "X-Real-IP"} {
		r := requestWith(s, "GET", "/api/ui?screen=settings:access", "127.0.0.1:1234", title, "203.0.113.7")
		if r.Code != http.StatusUnauthorized {
			t.Errorf("%s: запрос из интернета через шлюза получил %d, ждали 401", title, r.Code)
		}
	}
}

// Настоящий адрес обратившегося доходит до журнала доступа, а не адрес
// шлюза. Без `auth.trust_proxy` — адрес соединения: заголовку без
// своего прокси верить нельзя, им подделывают адрес.
func TestRealAddrInAccessLog(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingTrustProxy, "true"); err != nil {
		t.Fatal(err)
	}
	// перечитываем настройку так же, как это делает старт сервера
	_ = s.Handler()
	r := requestWith(s, "GET", "/api/ui?screen=settings:access", "127.0.0.1:1234", "X-Forwarded-For", "203.0.113.7")
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("ждали отказ (заголовок делает запрос чужим), получили %d", r.Code)
	}
	записи, err := st.AccessLog(20)
	if err != nil {
		t.Fatal(err)
	}
	нашли := ""
	for _, з := range записи {
		if з.Status == http.StatusUnauthorized {
			нашли = з.IP
			break
		}
	}
	if нашли != "203.0.113.7" {
		t.Fatalf("в журнале доступа адрес %q, а обращался 203.0.113.7 — шлюз подписал, а Сёма не прочитал", нашли)
	}
}

// Без доверия прокси заголовок в журнал не попадает: за Сёмой может не быть
// никакого прокси, и тогда этот заголовок принадлежит тому, кто его прислал.
func TestWithoutProxyTrustAddrFromHeaderIgnored(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingTrustProxy, "false"); err != nil {
		t.Fatal(err)
	}
	_ = s.Handler()
	r := requestWith(s, "GET", "/api/ui?screen=settings:access", "198.51.100.4:1234", "X-Forwarded-For", "203.0.113.7")
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("ждали отказ, получили %d", r.Code)
	}
	записи, err := st.AccessLog(20)
	if err != nil {
		t.Fatal(err)
	}
	for _, з := range записи {
		if з.Status == http.StatusUnauthorized {
			if з.IP != "198.51.100.4" {
				t.Fatalf("без доверия прокси в журнал попал адрес из заголовка (%q), а должен был адрес соединения", з.IP)
			}
			return
		}
	}
	t.Fatal("отказ не записан в журнал доступа")
}
