package api

// Вход по контрольной фразе (изменение login-phrase). Проверяем не функции,
// а настоящий путь: запрос уходит в Handler, как из браузера, и смотрим,
// что отвечает Сёма.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
)

// loginByPhrase — POST /api/auth/phrase с указанного адреса; возвращает
// ответ и токен сессии из cookie, если Сёма её открыл.
func loginByPhrase(t *testing.T, s *Server, фраза, remote string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	w := doRequest(s, "POST", "/api/auth/phrase", `{"phrase":`+strconv.Quote(фраза)+`}`, remote, "")
	for _, c := range w.Result().Cookies() {
		if c.Name == "sema_session" {
			return w, c.Value
		}
	}
	return w, ""
}

// screenAsText — описание экрана строкой: в тесте важно не устройство
// компонентов, а то, что прочтёт человек.
func screenAsText(t *testing.T, s *Server, screen string) string {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("экран %s: %v", screen, err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Снаружи фраза открывает ровно одно — заведение ключа. Три двери, которые
// человек попробует первыми (почта, файл, память), обязаны ответить
// отказом: иначе одна заученная строка открывала бы всю переписку тому,
// кто выманил её поддельной страницей.
func TestPhraseFromOutsideOnlyEnrollsKey(t *testing.T) {
	s, st := testServer(t)
	// «снаружи» считается той же IsLocal, что и везде; вход без проверки
	// личности выключаем, иначе своя сеть проходит и без всякой фразы
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	фраза, err := s.auth.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	const снаружи = "203.0.113.10:1234"
	w, token := loginByPhrase(t, s, фраза, снаружи)
	if w.Code != http.StatusOK || token == "" {
		t.Fatalf("фраза не пустила снаружи: %d %s", w.Code, w.Body.String())
	}
	var ответ struct{ Restricted bool }
	_ = json.Unmarshal(w.Body.Bytes(), &ответ)
	if !ответ.Restricted {
		t.Fatal("сессия снаружи не помечена урезанной")
	}
	// двери перечислены обеими дорогами: `/api/ui` — это путь приложений, а
	// живой веб-клиент ходит на `/ui` и `/do`. Оптимизация однажды уже
	// стояла на мёртвой дороге — проверять надо тот путь, которым ходят
	for _, дверь := range []struct{ что, метод, путь string }{
		{"почта", "GET", "/api/ui?screen=inbox"},
		{"почта у веб-клиента", "GET", "/ui?screen=inbox"},
		{"файл", "GET", "/api/files/1"},
		{"память", "GET", "/api/ui?screen=memory"},
		{"память у веб-клиента", "GET", "/ui?screen=memory"},
		{"действие приложения", "POST", "/api/action"},
	} {
		got := sessionWithCookie(s, дверь.метод, дверь.путь, token, снаружи)
		if got.Code != http.StatusForbidden {
			t.Errorf("%s (%s): ответ %d, ждали 403\n%s", дверь.что, дверь.путь, got.Code, got.Body.String())
		}
	}
	// действие веб-клиента — та же дверь, но `/do` без HX-Request отбивается
	// и без всякой урезанной сессии: заголовок ставим, иначе проверка
	// проходила бы по чужой причине
	r := httptest.NewRequest("POST", "http://localhost:8765/do", strings.NewReader("_кнопка=refresh|"))
	r.RemoteAddr = снаружи
	r.Header.Set("Cookie", "sema_session="+token)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got := httptest.NewRecorder()
	s.Handler().ServeHTTP(got, r)
	// клиенту отказ приходит кодом 401 — по нему он уходит на /login, как и с
	// истёкшей сессией (изменение от 1 октября 2026); действие не выполняется
	// в любом случае, а как именно, проверяет TestRestrictedSessionExecutesNothing
	if got.Code != http.StatusUnauthorized {
		t.Errorf("действие веб-клиента (/do): ответ %d, ждали 401\n%s", got.Code, got.Body.String())
	}
	// а церемония заведения ключа — единственное, что открыто
	if got := sessionWithCookie(s, "POST", "/api/auth/register/begin?name=телефон", token, снаружи); got.Code != http.StatusOK {
		t.Fatalf("завести ключ урезанной сессией нельзя: %d %s", got.Code, got.Body.String())
	}
	// заведённый ключ даёт полный доступ: его сессия урезанной не бывает
	pid, err := st.AddPasskey("телефон", `{}`, s.auth.RPID())
	if err != nil {
		t.Fatal(err)
	}
	ключевой, err := s.auth.OpenSession(pid, "проба", "203.0.113.10", "вход по passkey")
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionWithCookie(s, "GET", "/api/ui?screen=inbox", ключевой, снаружи); got.Code != http.StatusOK {
		t.Fatalf("сессия по ключу не открывает почту: %d %s", got.Code, got.Body.String())
	}
}

// Из своей сети та же фраза открывает Сёму целиком: второго понятия
// доверенной сети не заводим, решает та же IsLocal.
func TestPhraseFromLocalNetworkIsFull(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	фраза, err := s.auth.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	w, token := loginByPhrase(t, s, фраза, "127.0.0.1:1234")
	if w.Code != http.StatusOK || token == "" {
		t.Fatalf("фраза не пустила из своей сети: %d %s", w.Code, w.Body.String())
	}
	if got := sessionWithCookie(s, "GET", "/api/ui?screen=inbox", token, "127.0.0.1:1234"); got.Code != http.StatusOK {
		t.Fatalf("сессия из своей сети урезана: %d %s", got.Code, got.Body.String())
	}
}

// Каждая неудача идёт в журнал доступа, а запертый вход говорит, когда
// откроется: «не подходит» без срока человек читает как поломку.
func TestPhraseFailuresVisibleInLog(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(auth.SettingPhraseLimit, "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.SetPhrase(); err != nil {
		t.Fatal(err)
	}
	const снаружи = "198.51.100.200:5555"
	const чужая = "abcd-efgh-jkmn-pqrs-tuvw-xyz2-3456"
	w, _ := loginByPhrase(t, s, чужая, снаружи)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("чужая фраза: %d, ждали 401 (%s)", w.Code, w.Body.String())
	}
	w, token := loginByPhrase(t, s, чужая, снаружи)
	if w.Code != http.StatusTooManyRequests || token != "" {
		t.Fatalf("после предела: %d, ждали 429 без сессии (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "до ") {
		t.Fatalf("запертый вход не сказал, когда откроется: %q", w.Body.String())
	}
	журнал, err := st.AccessLog(50)
	if err != nil {
		t.Fatal(err)
	}
	неудач := 0
	for _, e := range журнал {
		if e.Method == "PHRASE" && e.Status >= 400 {
			неудач++
		}
	}
	if неудач < 2 {
		t.Fatalf("в журнале доступа %d неудачных попыток фразы, ждали хотя бы 2", неудач)
	}
}

// Смена адреса входа без заведённой фразы предупреждает особо: ключи
// прежнего имени пускать перестанут, и войти снаружи будет нечем вовсе.
func TestOriginChangeWithoutPhraseWarnsSpecially(t *testing.T) {
	s, st := testServer(t)
	if _, err := st.AddPasskey("мак", `{}`, "localhost"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(auth.SettingRPID, "sema.example"); err != nil {
		t.Fatal(err)
	}
	text := screenAsText(t, s, accessScreen)
	if !strings.Contains(text, "Адрес входа сменился") {
		t.Fatal("расхождение ключей с адресом входа не показано")
	}
	if !strings.Contains(text, "фразы входа нет") {
		t.Fatal("без фразы человек запирает себя сам, а особого предупреждения нет")
	}
	if !strings.Contains(text, "Порядок переезда") {
		t.Fatal("порядок переезда не показан")
	}
	// с заведённой фразой предупреждение остаётся, но уже не «нечем вовсе»
	if _, err := s.auth.SetPhrase(); err != nil {
		t.Fatal(err)
	}
	text = screenAsText(t, s, accessScreen)
	if !strings.Contains(text, "Адрес входа сменился") {
		t.Fatal("с фразой расхождение адреса перестало показываться")
	}
	if strings.Contains(text, "войти нечем вовсе") {
		t.Fatal("фраза заведена, а Сёма всё ещё говорит «нечем вовсе»")
	}
}

// Фраза заводится, сменяется и отзывается с «Доступа». Показывается она
// один раз: отозванная из описания экрана исчезает вместе с записью.
func TestPhraseOnAccessScreen(t *testing.T) {
	s, _ := testServer(t)
	if !strings.Contains(screenAsText(t, s, accessScreen), "Фразы входа нет") {
		t.Fatal("отсутствие фразы не названо состоянием")
	}
	фраза, err := s.auth.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screenAsText(t, s, accessScreen), фраза) {
		t.Fatal("свежая фраза не показана ни разу — узнать её больше негде")
	}
	if err := s.auth.RevokePhrase(); err != nil {
		t.Fatal(err)
	}
	text := screenAsText(t, s, accessScreen)
	if strings.Contains(text, фраза) {
		t.Fatal("отозванная фраза осталась в описании экрана")
	}
	if !strings.Contains(text, "Фразы входа нет") {
		t.Fatal("отозванная фраза всё ещё считается заведённой")
	}
}
