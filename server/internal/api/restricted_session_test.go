package api

// Сессия, открытая фразой снаружи, и значки оболочки (1 октября 2026).
//
// Живой случай: iPhone через шлюз вошёл фразой и получал 403 на каждый /ui и
// /api/ws — отказ верный (фраза снаружи только заводит ключ), но человек
// видел неработающий экран, а не дорогу к /login. Здесь проверяется и то,
// что дорога есть, и то, что сама защита не ослабла: ни экран, ни действие
// такой сессией не выполняются.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const снаружиАдрес = "203.0.113.10:1234"

// restrictedToken — урезанная сессия, открытая настоящим входом по фразе
// из недоверенной сети; возвращает токен из cookie и хэш строки в базе.
func restrictedToken(t *testing.T, s *Server, st *store.Store) (token, hash string) {
	t.Helper()
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	фраза, err := s.auth.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	w, token := loginByPhrase(t, s, фраза, снаружиАдрес)
	if w.Code != http.StatusOK || token == "" {
		t.Fatalf("фраза не пустила снаружи: %d %s", w.Code, w.Body.String())
	}
	hash = store.HashSecret(token)
	if se := sessionByHash(t, st, hash); se == nil || !se.Restricted {
		t.Fatal("сессия снаружи не помечена урезанной")
	}
	return token, hash
}

// fromBrowser — запрос так, как его шлёт браузер хозяина: htmx-запрос
// клиента или переход по ссылке.
func fromBrowser(method, path, body, token string, htmx bool) *http.Request {
	r := httptest.NewRequest(method, "http://localhost:8765"+path, strings.NewReader(body))
	r.RemoteAddr = снаружиАдрес
	r.Header.Set("Cookie", "sema_session="+token)
	if htmx {
		r.Header.Set("HX-Request", "true")
	} else {
		r.Header.Set("Accept", "text/html,application/xhtml+xml")
	}
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return r
}

// loginFor — куда ведёт отказ странице, открытой по этому пути: адрес
// возврата уходит в виде, в каком его видит сервер (кириллица — процентами).
func loginFor(path string) string {
	u := url.URL{Path: path}
	return "/login?next=" + url.QueryEscape(u.RequestURI())
}

// saveBar — тот же запрос, но уходящий из нижней полосы сохранения: она
// просит короткий ответ и разбирает код сама.
func saveBar(r *http.Request) *http.Request {
	r.Header.Set("X-Sema-Save", "1")
	return r
}

// Клиент уводит на вход по 401, и урезанная сессия получает именно его:
// те же три места клиента (подмена окна, очередь исходящих, сохранение
// форм) и служебный работник, что и на истёкшей сессии. Страница, открытая
// переходом, уходит на /login сразу. Всё прочее — отказ с кодом, как был.
func TestRestrictedSessionLeadsToLogin(t *testing.T) {
	s, st := testServer(t)
	token, _ := restrictedToken(t, s, st)

	for _, c := range []struct {
		что  string
		r    *http.Request
		code int
		// куда ведёт: переадресация или ссылка в теле отказа
		location string
	}{
		{"экран клиента (/ui)", fromBrowser("GET", "/ui?screen=inbox", "", token, true), http.StatusUnauthorized, ""},
		{"действие клиента (/do)", fromBrowser("POST", "/do", "action=refresh", token, true), http.StatusUnauthorized, ""},
		{"сохранение формы полосой", saveBar(fromBrowser("POST", "/do", "action=refresh", token, true)), http.StatusUnauthorized, ""},
		{"письмо переходом", fromBrowser("GET", messagePath+"/1", "", token, false), http.StatusSeeOther, loginFor(messagePath + "/1")},
		{"файл переходом", fromBrowser("GET", "/api/files/1", "", token, false), http.StatusSeeOther, loginFor("/api/files/1")},
	} {
		w := serve(s, c.r)
		if w.Code != c.code {
			t.Errorf("%s: ответ %d, ждали %d\n%s", c.что, w.Code, c.code, w.Body.String())
			continue
		}
		if c.location != "" && w.Header().Get("Location") != c.location {
			t.Errorf("%s: ведёт на %q, ждали %q", c.что, w.Header().Get("Location"), c.location)
		}
		if c.location == "" && !strings.Contains(w.Body.String(), `href="/login"`) {
			t.Errorf("%s: в отказе нет пути к входу: %q", c.что, w.Body.String())
		}
		if w.Body.Len() > 400 {
			t.Errorf("%s: тело отказа %d байт — похоже, вместе с отказом ушло что-то лишнее", c.что, w.Body.Len())
		}
	}

	// приложениям, проводу и всему, что не клиент и не переход, — прежний
	// честный отказ с кодом
	for _, дверь := range []struct{ что, метод, путь string }{
		{"экран приложения", "GET", "/api/ui?screen=inbox"},
		{"провод", "GET", "/api/ws"},
		{"действие приложения", "POST", "/api/action"},
	} {
		got := sessionWithCookie(s, дверь.метод, дверь.путь, token, снаружиАдрес)
		if got.Code != http.StatusForbidden {
			t.Errorf("%s: ответ %d, ждали 403", дверь.что, got.Code)
		}
	}

	// причина отказа остаётся в журнале доступа — по ней видно, почему
	// человек не попал внутрь
	журнал, err := st.AccessLog(100)
	if err != nil {
		t.Fatal(err)
	}
	причин := map[string]bool{}
	for _, e := range журнал {
		if strings.Contains(e.Note, "фраза снаружи даёт только завести ключ") {
			причин[e.Path] = true
		}
	}
	for _, путь := range []string{"/ui", "/do", messagePath + "/1", "/api/files/1", "/api/ui", "/api/ws", "/api/action"} {
		if !причин[путь] {
			t.Errorf("в журнале доступа нет причины отказа для %s", путь)
		}
	}

	// оболочка открыта без входа и переадресации не получает нарочно: её
	// кладёт в кэш служебный работник, и страница входа под именем «/»
	// ломала бы открытие без сети. На вход её уводит первый запрос экрана
	if got := serve(s, fromBrowser("GET", "/", "", token, false)); got.Code != http.StatusOK ||
		!strings.Contains(got.Body.String(), `id="window"`) {
		t.Errorf("оболочка: ответ %d, ждали 200 со страницей", got.Code)
	}
	// а страница входа открыта и отдаёт то, что нужно: состояние сессии
	state := serve(s, fromBrowser("GET", "/api/auth/state", "", token, true))
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), `"restricted":true`) ||
		!strings.Contains(state.Body.String(), `"canRegister":true`) {
		t.Errorf("страница входа не узнает урезанную сессию: %d %s", state.Code, state.Body.String())
	}
}

// Защита не ослабла: действие урезанной сессией не выполняется ни по
// какому пути, а сессия по ключу ту же самую правку выполняет — иначе тест
// проходил бы и на ровном месте, по чужой причине.
func TestRestrictedSessionExecutesNothing(t *testing.T) {
	s, st := testServer(t)
	token, hash := restrictedToken(t, s, st)
	form := "action=mark_own_device&target=" + hash

	if se := sessionByHash(t, st, hash); se == nil || se.Own {
		t.Fatal("исходное состояние не чистое")
	}
	// клиентом, переходом и приложением; /do без заголовка htmx отбивается
	// и без всякой сессии, поэтому заголовок ставим
	for _, r := range []*http.Request{
		fromBrowser("POST", "/do", form, token, true),
		fromBrowser("POST", "/do", form, token, false),
		fromBrowser("POST", "/api/action", `{"action":"mark_own_device","target":"`+hash+`"}`, token, true),
		fromBrowser("POST", "/api/action", `{"action":"mark_own_device","target":"`+hash+`"}`, token, false),
	} {
		w := serve(s, r)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden && w.Code != http.StatusSeeOther {
			t.Errorf("%s %s: ответ %d — отказа нет", r.Method, r.URL.Path, w.Code)
		}
	}
	if se := sessionByHash(t, st, hash); se == nil || se.Own {
		t.Fatal("урезанная сессия выполнила действие")
	}

	// контроль: сессия по ключу, с того же адреса, тем же запросом
	pid, err := st.AddPasskey("телефон", `{}`, s.auth.RPID())
	if err != nil {
		t.Fatal(err)
	}
	ключевой, err := s.auth.OpenSession(pid, "проба", "203.0.113.10", "вход по passkey")
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(s, fromBrowser("POST", "/do", form, ключевой, true)); w.Code != http.StatusOK {
		t.Fatalf("сессия по ключу: ответ %d\n%s", w.Code, w.Body.String())
	}
	if se := sessionByHash(t, st, hash); se == nil || !se.Own {
		t.Fatal("контроль не сработал: действие сессией по ключу не выполнилось, и проверка выше ничего не доказывала")
	}
	// и экран у неё открывается: обычная сессия работает, как раньше
	if w := serve(s, fromBrowser("GET", "/ui?screen=inbox", "", ключевой, true)); w.Code != http.StatusOK {
		t.Fatalf("экран сессией по ключу: ответ %d", w.Code)
	}
}

// Провод урезанной сессии не открывается: рукопожатие отклонено, кадра
// нет. Что это отказ по входу, страница узнаёт сама (см. браузерный тест).
func TestRestrictedSessionNoWire(t *testing.T) {
	s, st := testServer(t)
	token, _ := restrictedToken(t, s, st)
	// в тесте клиент приходит с loopback, а урезанная сессия считается
	// такой при выключенном «без входа» с любого адреса
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {"sema_session=" + token}}})
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("провод открылся урезанной сессии")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("рукопожатие: %v, ждали отказ 403", resp)
	}
}

// Значки приложения открыты без входа, а соседнее — нет: iPhone просит их
// без cookie, а за 401 прячется безликая буква вместо значка.
func TestShellIconsOpenWithoutLogin(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	for path, typ := range map[string]string{
		"/icon-180.png": "image/png", "/icon-192.png": "image/png", "/icon-512.png": "image/png",
		"/icon-maskable-512.png": "image/png", "/icon-maskable.svg": "image/svg+xml", "/icon.svg": "image/svg+xml",
		"/apple-touch-icon.png": "image/png", "/apple-touch-icon-precomposed.png": "image/png", "/favicon.ico": "image/png",
		"/manifest.webmanifest": "application/manifest+json",
	} {
		w := serve(s, stranger("GET", "https://"+publicSema+path))
		if w.Code != http.StatusOK {
			t.Errorf("%s без входа: ответ %d, ждали 200", path, w.Code)
			continue
		}
		if got := w.Header().Get("Content-Type"); got != typ {
			t.Errorf("%s: тип %q, ждали %q", path, got, typ)
		}
		if w.Header().Get("ETag") == "" {
			t.Errorf("%s: без отпечатка сборки браузеру нечего спросить", path)
		}
	}
	// закрыто по-прежнему: экраны, действия, провод и соседние имена
	for _, path := range []string{"/ui?screen=inbox", "/api/ws", "/icon-secret.png", "/apple-touch-icon-x.png", "/favicon.icox"} {
		if w := serve(s, stranger("GET", "https://"+publicSema+path)); w.Code != http.StatusUnauthorized {
			t.Errorf("%s без входа: ответ %d, ждали 401", path, w.Code)
		}
	}
}

// Страница входа показывает значок Сёмы, а не эмодзи-конверт, и берёт его
// у оболочки: человек без сессии (а на странице входа её по определению нет)
// получает тот же файл, что и оболочка, — иначе вместо значка была бы
// пустая рамка.
func TestLoginPageShowsAppIcon(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	page := serve(s, stranger("GET", "https://"+publicSema+"/login"))
	if page.Code != http.StatusOK {
		t.Fatalf("страница входа без сессии: %d", page.Code)
	}
	body := page.Body.String()
	if !strings.Contains(body, `<img src="/icon.svg" alt="" width="56" height="56">`) {
		t.Error("на странице входа нет значка Сёмы")
	}
	if strings.Contains(body, "✉") {
		t.Error("на странице входа остался эмодзи-конверт вместо значка")
	}
	icon := serve(s, stranger("GET", "https://"+publicSema+"/icon.svg"))
	if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/svg+xml" || icon.Body.Len() == 0 {
		t.Errorf("значок без сессии: ответ %d, тип %q, %d байт", icon.Code, icon.Header().Get("Content-Type"), icon.Body.Len())
	}
}
