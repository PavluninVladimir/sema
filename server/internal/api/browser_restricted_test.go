package api

// Сессия по фразе снаружи в настоящем Chrome (1 октября 2026).
//
// Что делает клиент с отказом — рукописный JS, которого не видит ни один
// тест на Go: уходит ли страница на /login, не стучится ли в провод и в
// экран снова и снова. Тест открывает Сёму так, как открывает iPhone:
// с cookie урезанной сессии, и ждёт, где окажется страница.
//
// Chrome нет — тест пропускается: SEMA_BROWSER=1 go test ./internal/api -run Браузер -p 1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// restrictedBrowser — Chrome с cookie сессии на адресе сервера; исключения
// страниц копятся в *exceptions. Адрес — localhost, а не 127.0.0.1: страница
// входа уходит с числа на имя из настроек.
func restrictedBrowser(t *testing.T, srv *httptest.Server, token string, exceptions *[]string) (context.Context, string) {
	t.Helper()
	addr := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	t.Cleanup(cancelTime)
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			*exceptions = append(*exceptions, e.ExceptionDetails.Error())
		}
	})
	// cookie до первого перехода: иначе первый же запрос ушёл бы без сессии
	if err := chromedp.Run(ctx, network.Enable(), network.SetCookie("sema_session", token).WithURL(addr)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("положить cookie сессии: %v", err)
	}
	return ctx, addr
}

// pageUntil — ждёт, пока выражение на странице станет истинным. Страница
// здесь уходит на другой документ, и опрос в старом падает с «Inspected
// target navigated»: такую ошибку переживаем и спрашиваем уже новый.
func pageUntil(ctx context.Context, выражение string, срок time.Duration) bool {
	конец := time.Now().Add(срок)
	for time.Now().Before(конец) {
		var да bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(выражение, &да)); err == nil && да {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// onLoginAsKeyNeeded — страница стоит на /login и объясняет, что нужен
// ключ, и кнопка заведения ключа на месте.
func onLoginAsKeyNeeded(t *testing.T, ctx context.Context, зачем string) {
	t.Helper()
	if !pageUntil(ctx, `location.pathname === '/login' && (document.getElementById('login')||{}).textContent.includes('Нужен ключ устройства')`, 15*time.Second) {
		var где string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`location.pathname + ' | ' + document.body.innerText.slice(0, 200)`, &где))
		t.Fatalf("%s: страница не пришла на /login с объяснением (сейчас: %s)", зачем, где)
	}
	var кнопка string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
      const р = document.getElementById('reg'), к = document.getElementById('mk');
      return getComputedStyle(р).display + '|' + (к ? к.textContent : 'нет кнопки');
    })()`, &кнопка)); err != nil {
		t.Fatal(err)
	}
	if кнопка != "flex|Завести passkey" {
		t.Fatalf("%s: кнопки заведения ключа нет или она спрятана: %q", зачем, кнопка)
	}
}

// addVirtualAuthenticator — хранитель ключей в Chrome. Проверка человека у
// него «пройдена»: auth.user_verification по умолчанию требует её. Он один
// на вкладку и помнит ключи, пока вкладка жива, — поэтому «другое
// устройство с тем же синхронизированным ключом» здесь это та же вкладка с
// новой cookie: ключ уже лежит у хранителя, а сессии нет.
func addVirtualAuthenticator(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, webauthn.Enable(), chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := webauthn.AddVirtualAuthenticator(&webauthn.VirtualAuthenticatorOptions{
			Protocol: webauthn.AuthenticatorProtocolCtap2, Transport: webauthn.AuthenticatorTransportInternal,
			HasResidentKey: true, HasUserVerification: true, IsUserVerified: true, AutomaticPresenceSimulation: true,
		}).Do(ctx)
		return err
	})); err != nil {
		t.Fatalf("виртуальный хранитель ключей: %v", err)
	}
}

// pageState — на что похожа страница входа сейчас: адрес, видимость блока
// заведения, подписи кнопок «Войти ключом» и «Завести passkey», подсказка и
// ошибка. Одной строкой, чтобы тест при провале показывал её целиком.
const pageState = `(() => {
  const el = id => document.getElementById(id), txt = id => (el(id) || {}).textContent || '';
  const shown = id => el(id) && getComputedStyle(el(id)).display !== 'none';
  return [location.pathname,
    'go=' + (el('go') ? el('go').textContent : '-'),
    'mk=' + (shown('reg') && el('mk') ? el('mk').textContent : '-'),
    'hint=' + (shown('hint') ? txt('hint') : '-'),
    'err=' + txt('err'), 'msg=' + txt('msg')].join(' | ');
})()`

// secondDevice — «другое устройство» с тем же ключом у хранителя: cookie
// прежней сессии заменяется свежей урезанной, как после входа фразой
// снаружи. Возвращает токен и хэш новой сессии.
func secondDevice(t *testing.T, ctx context.Context, s *Server, st *store.Store, addr string) (token, hash string) {
	t.Helper()
	token, hash = restrictedToken(t, s, st)
	if err := chromedp.Run(ctx, network.ClearBrowserCookies(), network.SetCookie("sema_session", token).WithURL(addr)); err != nil {
		t.Fatalf("положить cookie новой урезанной сессии: %v", err)
	}
	return token, hash
}

// openLoginAsRestricted — страница входа под урезанной сессией, куда клиент
// уводит с экрана, и ожидание, пока обе дороги нарисованы.
func openLoginAsRestricted(t *testing.T, ctx context.Context, addr string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Navigate(addr+"/login?next=%2F")); err != nil {
		t.Fatalf("открыть страницу входа: %v", err)
	}
	onLoginAsKeyNeeded(t, ctx, "урезанная сессия, ключ уже есть")
	if !pageUntil(ctx, `(document.getElementById('go')||{}).textContent === 'Войти ключом'`, 10*time.Second) {
		t.Fatalf("под урезанной сессией нет «Войти ключом»: %s", evaluate(t, ctx, pageState))
	}
	// над именем — значок Сёмы, и он загрузился, а не пустая рамка
	if !pageUntil(ctx, `(() => { const i = document.querySelector('.card img'); return !!i && i.complete && i.naturalWidth > 0; })()`, 10*time.Second) {
		t.Fatalf("значок Сёмы на странице входа не загрузился: %s", evaluate(t, ctx, pageState))
	}
	// Safari отдаёт ключ только по клику: параметры входа берутся заранее
	if !pageUntil(ctx, `!!pre.login && !!pre.register`, 10*time.Second) {
		t.Fatalf("параметры церемоний не приехали: %s", evaluate(t, ctx, pageState))
	}
}

// loggedInFully — вкладка вернулась в Сёму с полной сессией по ключу номер
// «ключей»: состояние, экран и отметка последнего входа. Прежняя урезанная
// сессия из базы ушла.
func loggedInFully(t *testing.T, ctx context.Context, st *store.Store, oldHash string, keys int) {
	t.Helper()
	if !pageUntil(ctx, `location.pathname === '/' && !!document.getElementById('screen')`, 20*time.Second) {
		t.Fatalf("после входа страница не вернулась в Сёму: %s", evaluate(t, ctx, pageState))
	}
	if got := evaluate(t, ctx, `fetch('/api/auth/state').then(r => r.json()).then(s => s.kind + '|' + s.restricted + '|' + s.passkeys)`); got != "session|false|"+strconv.Itoa(keys) {
		t.Fatalf("состояние после входа: %q, ждали session|false|%d", got, keys)
	}
	if got := evaluate(t, ctx, `fetch('/ui?screen=notifications', {headers: {'HX-Request': 'true'}}).then(r => String(r.status))`); got != "200" {
		t.Fatalf("экран новой сессии не открывается: %s", got)
	}
	if k, _ := st.Passkeys(); len(k) != keys || k[0].LastUsed.IsZero() {
		t.Fatalf("у ключа не проставлен последний вход: %+v", k)
	}
	if se := sessionByHash(t, st, oldHash); se != nil {
		t.Errorf("урезанная сессия осталась в базе: %+v", se)
	}
}

// enrollKey — первый ключ, как его заводит владелец под урезанной сессией:
// страница входа, «Завести passkey», возврат в Сёму полной сессией.
func enrollKey(t *testing.T, ctx context.Context, addr, name string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Navigate(addr+"/login?next=%2F")); err != nil {
		t.Fatalf("открыть страницу входа: %v", err)
	}
	onLoginAsKeyNeeded(t, ctx, "до заведения ключа")
	if !pageUntil(ctx, `!!pre.register`, 10*time.Second) {
		t.Fatal("параметры церемонии не приехали: кнопка заведения ключа нажмётся впустую")
	}
	if err := chromedp.Run(ctx, chromedp.SendKeys("#name", name, chromedp.ByID), chromedp.Click("#mk", chromedp.ByID)); err != nil {
		t.Fatalf("нажать «Завести passkey»: %v", err)
	}
	if !pageUntil(ctx, `location.pathname === '/' && !!document.getElementById('screen')`, 20*time.Second) {
		t.Fatalf("ключ не заведён: %s", evaluate(t, ctx, pageState))
	}
}

// Оболочка открылась, первый же запрос экрана получил «ключ нужен» — и
// страница сама уходит на /login, а не рисует отказ в окне и не крутит
// запросы. Адрес возврата сохраняется.
func TestBrowserRestrictedSessionGoesToLogin(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер -p 1")
	}
	s, st := testServer(t)
	token, _ := restrictedToken(t, s, st)
	var ui, wire atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ui":
			ui.Add(1)
		case "/api/ws":
			wire.Add(1)
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	var exceptions []string
	ctx, addr := restrictedBrowser(t, srv, token, &exceptions)
	if err := st.SetSetting(auth.SettingOrigin, addr); err != nil {
		t.Fatal(err)
	}

	if err := chromedp.Run(ctx, chromedp.Navigate(addr+"/")); err != nil {
		t.Fatalf("открыть Сёму: %v", err)
	}
	onLoginAsKeyNeeded(t, ctx, "урезанная сессия")
	if next := evaluate(t, ctx, `new URLSearchParams(location.search).get('next') || ''`); next != "/" {
		t.Errorf("адрес возврата %q, ждали «/»", next)
	}

	// без петли: страница входа не просит ни экранов, ни провода, а оболочка
	// до неё успела спросить экран не больше пары раз
	time.Sleep(3 * time.Second)
	if n := ui.Load(); n < 1 || n > 2 {
		t.Errorf("запросов экрана %d: ждали один-два (петля или оболочка вовсе не спрашивала)", n)
	}
	if n := wire.Load(); n != 0 {
		t.Errorf("к проводу стучались %d раз: урезанной сессии он не нужен", n)
	}
	if len(exceptions) > 0 {
		t.Fatalf("исключения на странице:\n%s", strings.Join(exceptions, "\n"))
	}
}

// Провод, который сервер не открыл ни разу: браузер про отказ скажет только
// «1006», и без вопроса к серверу его не отличить от обрыва сети. Страница
// спрашивает, кто она такая, и если сессия урезана — уходит на /login, а не
// стучится каждые пару секунд (так было живьём: четыре попытки за десять).
func TestBrowserRestrictedWireLeadsToLogin(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер -p 1")
	}
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	// опрос экрана выключен: на вход страницу может увести только провод
	for _, k := range []string{"ui.poll_s", "ui.poll_live_s"} {
		if err := st.SetSetting(k, "3600"); err != nil {
			t.Fatal(err)
		}
	}
	token, hash := makeSession(t, s, st)
	var wire atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ws" && wire.Add(1) == 1 {
			// экран уже нарисован ключевой сессией; к первому рукопожатию
			// сессия оказывается урезанной — как у iPhone с cookie от фразы
			_ = st.SetSessionRestricted(hash, true)
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	var exceptions []string
	ctx, addr := restrictedBrowser(t, srv, token, &exceptions)
	if err := st.SetSetting(auth.SettingOrigin, addr); err != nil {
		t.Fatal(err)
	}

	if err := chromedp.Run(ctx, chromedp.Navigate(addr+"/#screen=notifications")); err != nil {
		t.Fatalf("открыть Сёму: %v", err)
	}
	onLoginAsKeyNeeded(t, ctx, "отказ в проводе")
	if n := wire.Load(); n < 1 || n > 2 {
		t.Errorf("к проводу стучались %d раз, ждали одну-две попытки до ухода на вход", n)
	}
	if len(exceptions) > 0 {
		t.Fatalf("исключения на странице:\n%s", strings.Join(exceptions, "\n"))
	}
}

// Дорога целиком, как её проходит владелец: урезанная сессия → страница
// входа → «Завести passkey» → ключ заведён → та же вкладка уходит на
// прежний адрес с полной сессией. Ключ создаёт виртуальный хранитель
// Chrome (проверка человека у него «пройдена»: auth.user_verification по
// умолчанию требует её). Это не живой iPhone с его Bitwarden или связкой
// iCloud, но вся серверная часть церемонии и страница входа — настоящие.
func TestBrowserRestrictedSessionEnrollsKey(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер -p 1")
	}
	s, st := testServer(t)
	token, hash := restrictedToken(t, s, st)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	var exceptions []string
	ctx, addr := restrictedBrowser(t, srv, token, &exceptions)
	if err := st.SetSetting(auth.SettingOrigin, addr); err != nil {
		t.Fatal(err)
	}
	addVirtualAuthenticator(t, ctx)

	// страница входа, куда клиент уводит с экрана: адрес возврата — «/»
	if err := chromedp.Run(ctx, chromedp.Navigate(addr+"/login?next=%2F")); err != nil {
		t.Fatalf("открыть страницу входа: %v", err)
	}
	onLoginAsKeyNeeded(t, ctx, "до заведения ключа")
	// Safari отдаёт ключ только по клику, поэтому параметры церемонии
	// страница берёт заранее; нажимаем, когда они уже готовы
	if !pageUntil(ctx, `!!pre.register`, 10*time.Second) {
		t.Fatal("параметры церемонии не приехали: кнопка заведения ключа нажмётся впустую")
	}
	if err := chromedp.Run(ctx, chromedp.SendKeys("#name", "iPhone", chromedp.ByID), chromedp.Click("#mk", chromedp.ByID)); err != nil {
		t.Fatalf("нажать «Завести passkey»: %v", err)
	}
	// ключ заведён → страница возвращает на «/», где рисуется оболочка
	if !pageUntil(ctx, `location.pathname === '/' && !!document.getElementById('screen')`, 20*time.Second) {
		var где string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`location.pathname + ' | ' + (document.getElementById('err')||{}).textContent + ' | ' + (document.getElementById('msg')||{}).textContent`, &где))
		t.Fatalf("после заведения ключа страница не вернулась в Сёму (%s)", где)
	}
	// сессия — по ключу и полная: экран открывается, «только ключ» снят
	if got := evaluate(t, ctx, `fetch('/api/auth/state').then(r => r.json()).then(s => s.kind + '|' + s.restricted + '|' + s.passkeys)`); got != "session|false|1" {
		t.Fatalf("состояние после заведения ключа: %q, ждали session|false|1", got)
	}
	if got := evaluate(t, ctx, `fetch('/ui?screen=notifications', {headers: {'HX-Request': 'true'}}).then(r => String(r.status))`); got != "200" {
		t.Fatalf("экран новой сессии не открывается: %s", got)
	}
	if keys, _ := st.Passkeys(); len(keys) != 1 || keys[0].Name != "iPhone" {
		t.Fatalf("ключ в базе: %+v", keys)
	}
	// прежняя урезанная сессия остаётся урезанной: ключ заводит новую, а
	// не повышает старую
	if se := sessionByHash(t, st, hash); se != nil && !se.Restricted {
		t.Error("урезанная сессия сама стала полной")
	}
	if len(exceptions) > 0 {
		t.Fatalf("исключения на странице:\n%s", strings.Join(exceptions, "\n"))
	}
}

// Ключ Сёмы уже лежит у хранителя (связка iCloud, менеджер паролей), а на
// устройстве — урезанная сессия по фразе: страница обязана предложить
// войти им, а не только завести. Вход проходит, сессия полная, экран
// открывается, урезанная ушла, у ключа — последний вход. Так владелец
// 1 октября 2026 должен был попасть внутрь с iPhone.
func TestBrowserRestrictedSessionLogsInWithExistingKey(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер -p 1")
	}
	s, st := testServer(t)
	token, _ := restrictedToken(t, s, st)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	var exceptions []string
	ctx, addr := restrictedBrowser(t, srv, token, &exceptions)
	if err := st.SetSetting(auth.SettingOrigin, addr); err != nil {
		t.Fatal(err)
	}
	addVirtualAuthenticator(t, ctx)
	enrollKey(t, ctx, addr, "MacBookEx")
	if k, _ := st.Passkeys(); len(k) != 1 || !k[0].LastUsed.IsZero() {
		t.Fatalf("после заведения ключ ещё не входил: %+v", k)
	}

	// «iPhone»: ключ у хранителя есть, сессии нет — снова только фраза
	_, hash := secondDevice(t, ctx, s, st, addr)
	openLoginAsRestricted(t, ctx, addr)
	// обе дороги на виду, и ничего лишнего: ни ошибки, ни подсказки
	if got := evaluate(t, ctx, pageState); !strings.Contains(got, "go=Войти ключом") || !strings.Contains(got, "mk=Завести passkey") ||
		!strings.Contains(got, "hint=- | err= ") {
		t.Fatalf("страница урезанной сессии: %s", got)
	}
	if got := evaluate(t, ctx, `document.getElementById('login').textContent`); !strings.Contains(got, "связке iCloud") || !strings.Contains(got, "войдите им") {
		t.Errorf("пояснение не говорит про уже заведённый ключ: %q", got)
	}
	if err := chromedp.Run(ctx, chromedp.Click("#go", chromedp.ByID)); err != nil {
		t.Fatalf("нажать «Войти ключом»: %v", err)
	}
	loggedInFully(t, ctx, st, hash, 1)
	if len(exceptions) > 0 {
		t.Fatalf("исключения на странице:\n%s", strings.Join(exceptions, "\n"))
	}
}

// Владелец нажал «Завести passkey», хотя ключ уже есть у хранителя:
// хранитель отвечает InvalidStateError («The object is in an invalid
// state»). Это не ошибка, а сигнал — страница говорит, что ключ уже есть, и
// ставит перед глазами «Войти ключом»; вход тем же нажатием не запускается
// (Safari не даст без нового касания). Второй ключ не заводится.
func TestBrowserEnrollOnDeviceWithKeySuggestsLogin(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер -p 1")
	}
	s, st := testServer(t)
	token, _ := restrictedToken(t, s, st)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	var exceptions []string
	ctx, addr := restrictedBrowser(t, srv, token, &exceptions)
	if err := st.SetSetting(auth.SettingOrigin, addr); err != nil {
		t.Fatal(err)
	}
	addVirtualAuthenticator(t, ctx)
	enrollKey(t, ctx, addr, "MacBookEx")

	_, hash := secondDevice(t, ctx, s, st, addr)
	openLoginAsRestricted(t, ctx, addr)
	if err := chromedp.Run(ctx, chromedp.SendKeys("#name", "iPhone", chromedp.ByID), chromedp.Click("#mk", chromedp.ByID)); err != nil {
		t.Fatalf("нажать «Завести passkey»: %v", err)
	}
	if !pageUntil(ctx, `getComputedStyle(document.getElementById('hint')).display !== 'none'`, 15*time.Second) {
		t.Fatalf("на «уже есть» страница ничего не сказала: %s", evaluate(t, ctx, pageState))
	}
	got := evaluate(t, ctx, pageState)
	if !strings.Contains(got, "уже есть ключ Сёмы") || !strings.Contains(got, "go=Войти ключом") {
		t.Fatalf("подсказки про готовый ключ нет: %s", got)
	}
	if strings.Contains(got, "invalid state") || strings.Contains(got, "err=The") || !strings.Contains(got, "err= ") {
		t.Fatalf("InvalidStateError показан человеком как ошибка: %s", got)
	}
	// вход не запускался сам: страница осталась на месте, сессия урезанная
	if where := evaluate(t, ctx, `location.pathname`); where != "/login" {
		t.Fatalf("страница ушла с входа без нажатия: %s", where)
	}
	if se := sessionByHash(t, st, hash); se == nil || !se.Restricted {
		t.Fatalf("без нажатия сессия изменилась: %+v", se)
	}
	if k, _ := st.Passkeys(); len(k) != 1 {
		t.Fatalf("второй ключ всё же заведён: %+v", k)
	}
	// кнопка входа в фокусе, а параметры нового входа готовы к касанию
	if !pageUntil(ctx, `document.activeElement && document.activeElement.id === 'go' && !!pre.login`, 10*time.Second) {
		t.Fatalf("«Войти ключом» не готова к нажатию: %s", evaluate(t, ctx, pageState))
	}
	if err := chromedp.Run(ctx, chromedp.Click("#go", chromedp.ByID)); err != nil {
		t.Fatalf("нажать «Войти ключом»: %v", err)
	}
	loggedInFully(t, ctx, st, hash, 1)
	if len(exceptions) > 0 {
		t.Fatalf("исключения на странице:\n%s", strings.Join(exceptions, "\n"))
	}
}
