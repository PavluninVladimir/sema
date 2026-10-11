package api

// Вход у новой установки (open-source, решение 5; спека access-control):
// свежая установка закрыта, обновлённая — как была, сосед по сети без кода
// первой установки ключ не заводит, код после первого ключа не принимается,
// подбор кода запирается. Всё — настоящими запросами через protect и ручки
// входа, как у человека.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// freshServer — сервер свежей установки: умолчания входа такие, какие
// разовая миграция записывает пустой базе (это проверяет
// store.TestAuthDefaultsFreshBase). testServer изображает обновлённого Сёму,
// у которой своя сеть пускается без входа.
func freshServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	for key, value := range map[string]string{auth.SettingLocalOpen: "false", auth.SettingLocalScope: store.ScopeLoopback} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	prev := auth.LocalPrivate.Load()
	t.Cleanup(func() { auth.LocalPrivate.Store(prev) })
	s.auth.LoadLocalScope()
	return s, st
}

// loginBySetup — вход кодом первой установки; возвращает ответ и cookie.
func loginBySetup(t *testing.T, s *Server, code, remote string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	w := doRequest(s, "POST", "/api/auth/setup", `{"code":`+strconv.Quote(code)+`}`, remote, "")
	for _, c := range w.Result().Cookies() {
		if c.Name == "sema_session" {
			return w, c.Value
		}
	}
	return w, ""
}

// Соседи по сети — домашний Wi-Fi, контейнер рядом, мост Docker.
var соседи = []string{"192.168.1.20:5000", "10.0.0.4:5000", "172.17.0.1:5000"}

// Свежая установка: без проверки личности не пускается никто, первый ключ
// заводит только сама машина, а сосед по сети видит вход и ничего больше.
func TestFreshInstallIsClosed(t *testing.T) {
	s, _ := freshServer(t)
	for _, addr := range соседи {
		if w := doRequest(s, "GET", "/api/ui?screen=inbox", "", addr, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: экран без входа — %d, ждали 401", addr, w.Code)
		}
		if w := doRequest(s, "POST", "/api/auth/register/begin", "", addr, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s: сосед начал заводить первый ключ — %d, ждали 403", addr, w.Code)
		}
	}
	// сама машина экранов без ключа не видит (вход без проверки личности
	// выключен), но первый ключ заводит прямо здесь
	if w := doRequest(s, "GET", "/api/ui?screen=inbox", "", "127.0.0.1:5000", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("петля без ключа: %d, ждали 401", w.Code)
	}
	if w := doRequest(s, "POST", "/api/auth/register/begin", "", "127.0.0.1:5000", ""); w.Code != http.StatusOK {
		t.Errorf("с самой машины первый ключ не заводится: %d %s", w.Code, w.Body.String())
	}
	// и страница входа соседу честно говорит, что ключ ему отсюда не завести
	w := doRequest(s, "GET", "/api/auth/state", "", соседи[0], "")
	if !strings.Contains(w.Body.String(), `"canRegister":false`) || !strings.Contains(w.Body.String(), `"localOpen":false`) {
		t.Errorf("состояние входа для соседа: %s", w.Body.String())
	}
}

// Частная сеть — своя только по выбору человека: выбрал — сосед может завести
// первый ключ, как раньше; не выбрал — нет.
func TestPrivateNetworkRegistersOnlyWhenChosen(t *testing.T) {
	s, st := freshServer(t)
	if w := doRequest(s, "POST", "/api/auth/register/begin", "", соседи[0], ""); w.Code != http.StatusForbidden {
		t.Fatalf("частная сеть не выбрана, а первый ключ из неё заводится: %d", w.Code)
	}
	if err := st.SetSetting(auth.SettingLocalScope, store.ScopePrivate); err != nil {
		t.Fatal(err)
	}
	s.auth.LoadLocalScope()
	if w := doRequest(s, "POST", "/api/auth/register/begin", "", соседи[0], ""); w.Code != http.StatusOK {
		t.Fatalf("частная сеть выбрана своей, а первый ключ из неё не заводится: %d %s", w.Code, w.Body.String())
	}
}

// Обновление работающего Сёмы: база с данными, где умолчания входа не
// записаны (прежняя версия читала их литералами), — после перезапуска своя
// сеть пускается без входа, как до обновления, а «Вход и доступ» говорит,
// что так выбрано. Проходит через настоящую миграцию при открытии базы.
func TestUpgradedInstallAsBefore(t *testing.T) {
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
	path := filepath.Join(dir, "sema.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAccounts([]*store.Account{{Name: "проба", Email: "проба@пример.рф", Host: "imap.пример.рф", Port: 993}}); err != nil {
		t.Fatal(err)
	}
	// так базу оставила прежняя версия: ключей входа в таблице нет
	st.DeleteSettingKey(auth.SettingLocalOpen)
	st.DeleteSettingKey(auth.SettingLocalScope)
	_ = st.Close()

	st, err = store.Open(path) // перезапуск новой версией
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	conf := &config.Config{Data: dir, KeyEnv: "SEMA_KEY"}
	conf.Files.Dir = filepath.Join(dir, "files")
	eng, err := engine.New(conf, st)
	if err != nil {
		t.Fatal(err)
	}
	s := New(eng)
	prev := auth.LocalPrivate.Load()
	t.Cleanup(func() { auth.LocalPrivate.Store(prev) })

	for _, addr := range []string{"192.168.1.20:5000", "127.0.0.1:5000"} {
		if w := doRequest(s, "GET", "/api/ui?screen=inbox", "", addr, ""); w.Code != http.StatusOK {
			t.Errorf("%s: обновлённый Сёма закрылся от своей сети — %d", addr, w.Code)
		}
	}
	text := screenAsText(t, s, accessScreen+"~"+accessTabNetwork)
	if !strings.Contains(text, "эта машина и вся частная сеть") {
		t.Errorf("на «Доверенной сети» не видно, что своя сеть пускается без входа:\n%s", text)
	}
}

// Сосед по сети без кода первой установки ключ не заводит; с кодом — заводит,
// но сессия по коду урезана: ни экрана, ни действия, ни файла.
func TestNeighbourNeedsSetupCode(t *testing.T) {
	s, st := freshServer(t)
	code, _, ok := auth.IssueSetupCode(st)
	if !ok {
		t.Fatal("ключей нет, а код не выдан")
	}
	сосед := соседи[2] // браузер хозяина в Docker приходит из сети моста
	if w := doRequest(s, "GET", "/api/auth/state", "", сосед, ""); !strings.Contains(w.Body.String(), `"setup":true`) {
		t.Fatalf("страница входа не узнаёт, что код есть: %s", w.Body.String())
	}
	if w, token := loginBySetup(t, s, "setup-aaaa-bbbb-cccc-dddd", сосед); w.Code != http.StatusUnauthorized || token != "" {
		t.Fatalf("чужой код пустил: %d", w.Code)
	}
	w, token := loginBySetup(t, s, code, сосед)
	if w.Code != http.StatusOK || token == "" {
		t.Fatalf("свой код не пустил: %d %s", w.Code, w.Body.String())
	}
	se, ok := st.SessionByToken(token, auth.SessionTTL)
	if !ok || !se.Restricted || !se.BySetupCode() {
		t.Fatalf("сессия по коду не урезана: %+v", se)
	}
	for _, дверь := range []struct{ метод, путь string }{
		{"GET", "/api/ui?screen=inbox"}, {"POST", "/api/action"}, {"GET", "/api/files/1"}, {"GET", "/api/ws"},
	} {
		if got := sessionWithCookie(s, дверь.метод, дверь.путь, token, сосед); got.Code < 400 {
			t.Errorf("%s %s: сессия по коду прошла — %d", дверь.метод, дверь.путь, got.Code)
		}
	}
	if got := sessionWithCookie(s, "POST", "/api/auth/register/begin", token, сосед); got.Code != http.StatusOK {
		t.Fatalf("сессия по коду не заводит первый ключ: %d %s", got.Code, got.Body.String())
	}
	if got := sessionWithCookie(s, "GET", "/api/auth/state", token, сосед); !strings.Contains(got.Body.String(), `"bySetup":true`) {
		t.Errorf("страница входа не знает, что сессия открыта кодом: %s", got.Body.String())
	}
}

// Код после первого ключа не принимается, попытка видна в журнале доступа,
// а сессия, открытая кодом раньше, второй ключ уже не заводит.
func TestSetupCodeAfterFirstKey(t *testing.T) {
	s, st := freshServer(t)
	code, _, _ := auth.IssueSetupCode(st)
	сосед := соседи[2]
	_, раньше := loginBySetup(t, s, code, сосед)
	if раньше == "" {
		t.Fatal("код не пустил до первого ключа")
	}
	if _, err := st.AddPasskey("мак", "{}", "localhost"); err != nil {
		t.Fatal(err)
	}
	w, token := loginBySetup(t, s, code, "192.168.1.30:5000")
	if w.Code != http.StatusUnauthorized || token != "" {
		t.Fatalf("код из старого журнала пустил после первого ключа: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "больше не действует") {
		t.Errorf("отказ не говорит, что код погас: %q", w.Body.String())
	}
	if got := sessionWithCookie(s, "POST", "/api/auth/register/begin", раньше, сосед); got.Code != http.StatusForbidden {
		t.Errorf("сессия по коду завела ключ после первого: %d", got.Code)
	}
	if s.auth.SetupActive() {
		t.Error("код всё ещё считается действующим")
	}
	журнал, err := st.AccessLog(50)
	if err != nil {
		t.Fatal(err)
	}
	отказ := false
	for _, e := range журнал {
		if e.Method == "SETUP" && e.Status == http.StatusUnauthorized && e.IP == "192.168.1.30" && strings.Contains(e.Note, "больше не действует") {
			отказ = true
		}
	}
	if !отказ {
		t.Error("попытки старым кодом нет в журнале доступа")
	}
}

// Подбор кода запирается растущим сроком, как подбор фразы, — и запертый
// вход не принимает уже и верный код. В журнале доступа — вход и отказы, но
// не сам код: ни верный, ни подобранный.
func TestSetupCodeGuessingLocks(t *testing.T) {
	s, st := freshServer(t)
	if err := st.SetSetting(auth.SettingPhraseLimit, "3"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := auth.IssueSetupCode(st)
	const перебор = "203.0.113.66:5000"
	догадки := []string{"setup-aaaa-bbbb-cccc-dddd", "setup-eeee-ffff-gggg-hhhh", "setup-jjjj-kkkk-mmmm-nnnn"}
	var last *httptest.ResponseRecorder
	for _, g := range догадки {
		last, _ = loginBySetup(t, s, g, перебор)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("третья неудача при пределе 3 не заперла: %d %s", last.Code, last.Body.String())
	}
	if w, token := loginBySetup(t, s, code, перебор); w.Code != http.StatusTooManyRequests || token != "" {
		t.Fatalf("запертый вход принял верный код: %d", w.Code)
	}
	// с другого адреса код по-прежнему действует: предел — по источнику
	if w, token := loginBySetup(t, s, code, "203.0.113.67:5000"); w.Code != http.StatusOK || token == "" {
		t.Fatalf("соседний источник заперт чужим перебором: %d", w.Code)
	}
	журнал, err := st.AccessLog(100)
	if err != nil {
		t.Fatal(err)
	}
	входов, отказов := 0, 0
	for _, e := range журнал {
		for _, секрет := range append(догадки, code, strings.TrimPrefix(code, auth.SetupCodePrefix)) {
			if strings.Contains(e.Note, секрет) || strings.Contains(e.Path, секрет) || strings.Contains(e.Who, секрет) {
				t.Errorf("код в журнале доступа: %+v", e)
			}
		}
		if e.Method == "SETUP" {
			if e.Status == http.StatusOK {
				входов++
			} else {
				отказов++
			}
		}
	}
	if входов != 1 || отказов < 4 {
		t.Errorf("в журнале доступа входов кодом %d, отказов %d — ждали 1 и не меньше 4", входов, отказов)
	}
}

// «Считать своей всю частную сеть» включается только через вопрос, и вопрос
// называет, кто войдёт без ключа; ответ «да» действительно открывает сеть.
func TestPrivateNetworkToggleAsks(t *testing.T) {
	s, st := freshServer(t)
	row := findComponent(t, s, accessScreen+"~"+accessTabNetwork, "auth-local-scope")
	if row.Toggle != "off" || len(row.Actions) != 1 || row.Actions[0].Label != "toggle" {
		t.Fatalf("строка частной сети: %+v", row)
	}
	ask := row.Actions[0]
	for _, слово := range []string{"192.168.0.0/16", "контейнеры", "секреты", "почту", "серверы"} {
		if !strings.Contains(ask.Confirm, слово) {
			t.Errorf("вопрос перед включением не называет «%s»: %q", слово, ask.Confirm)
		}
	}
	if ask.ConfirmOK == "" || ask.Style != "destructive" {
		t.Errorf("кнопка согласия: %q, вид %q", ask.ConfirmOK, ask.Style)
	}
	if err := st.SetSetting(auth.SettingLocalOpen, "true"); err != nil {
		t.Fatal(err)
	}
	if w := doRequest(s, "GET", "/api/ui?screen=inbox", "", соседи[0], ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("сосед вошёл до выбора человека: %d", w.Code)
	}
	if _, err := s.accessActions(actionRequest{Action: ask.Action, Target: ask.Target}); err != nil {
		t.Fatal(err)
	}
	if w := doRequest(s, "GET", "/api/ui?screen=inbox", "", соседи[0], ""); w.Code != http.StatusOK {
		t.Fatalf("частная сеть выбрана своей, а сосед не входит: %d", w.Code)
	}
	// обратно — тоже вопросом, и он говорит, кто останется без входа
	row = findComponent(t, s, accessScreen+"~"+accessTabNetwork, "auth-local-scope")
	if row.Toggle != "on" || !strings.Contains(row.Actions[0].Confirm, "ключу устройства") {
		t.Fatalf("строка включённой частной сети: %+v", row)
	}
	if _, err := s.accessActions(actionRequest{Action: "toggle_local_scope"}); err != nil {
		t.Fatal(err)
	}
	if v := st.SettingText(store.AuthLocalScope); v != store.ScopeLoopback || auth.LocalPrivate.Load() {
		t.Fatalf("после выключения своя сеть %q, флаг %v", v, auth.LocalPrivate.Load())
	}
}

// findComponent — компонент экрана по ID, где бы он ни лежал.
func findComponent(t *testing.T, s *Server, screen, id string) Component {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var found *Component
	var walk func([]Component)
	walk = func(cs []Component) {
		for i := range cs {
			if cs[i].ID == id && found == nil {
				found = &cs[i]
			}
			walk(cs[i].Children)
		}
	}
	walk(doc.Components)
	if found == nil {
		t.Fatalf("на экране %s нет %s", screen, id)
	}
	return *found
}
