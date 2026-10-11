package api

// Пуши в настоящем Chrome: служебный работник и строка «Пуши на этом
// устройстве». Чего здесь нет и быть не может: iOS и Android. Ни
// «Домой» на iPhone, ни настоящая служба пушей Apple или Google из Chrome на
// маке не проверяются — это проверка владельцем руками (см. отчёт). Зато
// проверяется то, что от платформы не зависит: работник на событие `push`
// всегда показывает уведомление, `notificationclick` открывает экран, а
// страница честно называет, что мешает подписке, и просит разрешение
// первым делом в обработчике касания.
//
// НИ ОДИН ТЕСТ ЗДЕСЬ НЕ ПОКАЗЫВАЕТ НАСТОЯЩЕЕ УВЕДОМЛЕНИЕ. Headless-Chrome
// (`--headless=new`) на macOS не заглушает показ: уведомления уходят в
// настоящий центр уведомлений хозяина машины и с `requireInteraction` висят
// там, пока их не закроют. 1 октября 2026 так первые версии этих тестов
// засыпали центр десятками «Диск на srv-bots». Поэтому работнику подаётся
// событие `push` как обычный объект (не через протокол доставки пушей Chrome,
// у которого свои правила показа «по умолчанию»), а его `showNotification`
// подменён целиком: вызов записывается, и настоящий не вызывается вовсе.
// Сторож — `TestBrowserPushNeverShowsRealNotification`: он читает этот файл и
// падает при любом возврате настоящего показа.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// pushBrowser поднимает Сёму на loopback и Chrome с одной вкладкой. Всё
// закрывается само по окончании теста: Chrome не должен оставаться жить.
func pushBrowser(t *testing.T) (context.Context, *httptest.Server, *Server, *store.Store) {
	t.Helper()
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, st := testServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	t.Cleanup(cancelTime)
	return ctx, srv, s, st
}

func awaitPromise(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }

// pollJS — ждёт, пока выражение на странице станет истинным.
func pollJS(t *testing.T, ctx context.Context, what, expr string) {
	t.Helper()
	// Первая загрузка в новом Chrome ставит служебного работника, и страница
	// перезагружается один раз (controllerchange в app.js): контекст, в
	// котором спрашивали, пропадает. Поэтому ждём, терпя такие ошибки, а не
	// падая на первой из них
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &ok)); err == nil && ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

// pushWaitScreen — ждёт, пока нарисован экран, терпя перезагрузку страницы.
func pushWaitScreen(t *testing.T, ctx context.Context, screen string) {
	t.Helper()
	pollJS(t, ctx, "экран "+screen+" нарисован",
		`(() => { const e = document.getElementById('screen'); return !!e && e.dataset.screen === `+jsString(screen)+`; })()`)
}

// Работник на событие `push` показывает уведомление всегда и в том виде,
// как велит сервер; нажатие открывает нужный экран в уже открытом окне.
func TestBrowserPushWorkerShowsAndOpens(t *testing.T) {
	ctx, srv, _, _ := pushBrowser(t)
	if err := chromedp.Run(ctx,
		// запрещено на случай, если подмена ниже когда-нибудь сломается:
		// настоящий показ тогда отвергнется, а не попадёт в центр уведомлений
		browser.SetPermission(&browser.PermissionDescriptor{Name: "notifications"}, browser.PermissionSettingDenied).WithOrigin(srv.URL),
		chromedp.Navigate(srv.URL+"/#screen=inbox"),
	); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	pushWaitScreen(t, ctx, "inbox")
	var scope string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(async () => { const r = await navigator.serviceWorker.ready; return r.scope; })()`, &scope, awaitPromise)); err != nil {
		t.Fatalf("работник не поднялся: %v", err)
	}

	var workerID target.ID
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		infos, err := target.GetTargets().Do(ctx)
		for _, in := range infos {
			if in.Type == "service_worker" && strings.HasPrefix(in.URL, srv.URL) {
				workerID = in.TargetID
			}
		}
		return err
	})); err != nil || workerID == "" {
		t.Fatalf("цель работника не найдена: %v", err)
	}
	wctx, cancelW := chromedp.NewContext(ctx, chromedp.WithTargetID(workerID))
	defer cancelW()

	// Подмена целиком: `showNotification` только записывает, что ему
	// передали, и возвращает готовое обещание. Настоящий показ не вызывается
	// нигде и ни при каких условиях. `__push` подаёт работнику событие `push`
	// обычным объектом с телом и `waitUntil` — как его видит обработчик.
	if err := chromedp.Run(wctx, chromedp.Evaluate(`(() => {
  self.__shown = []; self.__badge = [];
  self.registration.showNotification = (title, o) => {
    self.__shown.push({ title, body: o.body, tag: o.tag, icon: o.icon, badge: o.badge, lang: o.lang,
      renotify: !!o.renotify, requireInteraction: !!o.requireInteraction, url: o.data && o.data.url });
    return Promise.resolve();
  };
  Object.defineProperty(self.navigator, 'setAppBadge', { configurable: true, value: (n) => { self.__badge.push(n); return Promise.resolve(); } });
  Object.defineProperty(self.navigator, 'clearAppBadge', { configurable: true, value: () => { self.__badge.push('снят'); return Promise.resolve(); } });
  self.__push = async (raw) => {
    let pending = null;
    const ev = new Event('push');
    ev.data = raw === null ? null : { json() { return JSON.parse(raw); }, text() { return raw; } };
    ev.waitUntil = (p) => { pending = p; };
    self.dispatchEvent(ev);
    if (!pending) return 'обработчик не попросил дождаться';
    await pending;
    return 'ok';
  };
  return 1;
})()`, nil)); err != nil {
		t.Fatalf("подмена в работнике: %v", err)
	}
	// подмена действительно встала: дальше любой пуш идёт в неё, а не в настоящий показ
	var armed bool
	if err := chromedp.Run(wctx, chromedp.Evaluate(`self.registration.showNotification.toString().includes('__shown')`, &armed)); err != nil || !armed {
		t.Fatalf("подмена showNotification не встала (%v): дальше пошёл бы настоящий показ", err)
	}

	type shown struct {
		Title, Body, Tag, Icon, Badge, Lang, URL string
		Renotify, RequireInteraction             bool
	}
	// push подаёт работнику тело (nil — пуш без тела) и возвращает то, с чем
	// он попросил показать уведомление
	push := func(raw *string) shown {
		t.Helper()
		arg := "null"
		if raw != nil {
			arg = jsString(*raw)
		}
		var res, last string
		if err := chromedp.Run(wctx,
			chromedp.Evaluate(`self.__push(`+arg+`)`, &res, awaitPromise),
			chromedp.Evaluate(`JSON.stringify(self.__shown[self.__shown.length - 1] || null)`, &last),
		); err != nil {
			t.Fatalf("пуш работнику: %v", err)
		}
		if res != "ok" {
			t.Fatalf("работник на пуш: %s", res)
		}
		var n *shown
		if err := json.Unmarshal([]byte(last), &n); err != nil || n == nil {
			t.Fatalf("пуш без показа: Safari за такое отзывает подписку целиком (%v)", err)
		}
		return *n
	}
	body := func(s string) *string { return &s }

	// 1. тревога: показано как велено, значки — PNG, тег и повтор на месте
	n := push(body(`{"title":"Диск на srv-bots","body":"занято 95%","url":"/#screen=notifications","tag":"sema-alert1","level":"alert","renotify":true,"waiting":3}`))
	if n.Title != "Диск на srv-bots" || n.Body != "занято 95%" || n.URL != "/#screen=notifications" || n.Lang != "ru" || n.Tag != "sema-alert1" {
		t.Errorf("показано не то, что прислал сервер: %+v", n)
	}
	if n.Icon != "/icon-192.png" || n.Badge != "/icon-badge.png" {
		t.Errorf("значки уведомления: icon %q, badge %q — нужны PNG, SVG уведомления не рисуются", n.Icon, n.Badge)
	}
	// тревога без `sticky` не висит: удержание решает сервер настройкой, а не
	// работник по уровню (иначе каждая тревога оседала бы в центре уведомлений мака)
	if !n.Renotify || n.RequireInteraction {
		t.Errorf("тревога без sticky: ждали повтор со звуком и без удержания, а вышло %+v", n)
	}
	// с `sticky` — висит, и от уровня это не зависит
	if st := push(body(`{"title":"Висит","tag":"sema-sticky1","level":"alert","renotify":true,"sticky":true}`)); !st.RequireInteraction {
		t.Errorf("sticky: уведомление не удерживается: %+v", st)
	}
	if st := push(body(`{"title":"Не висит","tag":"sema-sticky2","level":"warn","sticky":false}`)); st.RequireInteraction {
		t.Errorf("sticky=false: уведомление удерживается: %+v", st)
	}
	if st := push(body(`{"title":"Висит по полю","tag":"sema-sticky3","level":"warn","sticky":true}`)); !st.RequireInteraction {
		t.Errorf("удержание следует за полем sticky, а не за уровнем: %+v", st)
	}

	// 2. «внимание»: ни повтора, ни удержания
	if w := push(body(`{"title":"Сервер: место","body":"4%","tag":"sema-warn1","level":"warn","renotify":false,"waiting":4}`)); w.Renotify || w.RequireInteraction || w.URL != "/" || w.Tag != "sema-warn1" {
		t.Errorf("«внимание» показано как тревога или без адреса по умолчанию: %+v", w)
	}

	// 3. тела нет вовсе, тега нет — всё равно уведомление, и тег по умолчанию
	if b := push(body(`{"title":"Без тега"}`)); b.Tag != "sema" || b.Title != "Без тега" {
		t.Errorf("пуш без тега: %+v", b)
	}
	if e := push(nil); e.Title != "Сёма: есть новое" || e.Body != "" {
		t.Errorf("пуш без тела должен показать «есть новое»: %+v", e)
	}

	// 4. мусор вместо тела — всё равно уведомление «есть новое», без догадок
	// о содержимом: Safari иначе отзовёт подписку
	if j := push(body(`это не JSON`)); j.Title != "Сёма: есть новое" || j.Body != "" {
		t.Errorf("нечитаемый пуш показан не как «есть новое»: %+v", j)
	}

	// 5. чужой адрес в пуше не открывается: данные приходят с сервера, но
	// открывать по нажатию чужую страницу уведомление не должно
	if e := push(body(`{"title":"Чужой адрес","tag":"sema-evil","url":"https://evil.example/x"}`)); e.URL != "/" {
		t.Errorf("чужой адрес попал в уведомление: %q", e.URL)
	}

	// 6. нажатие открывает экран в уже открытом окне, не перезагружая его.
	// Событие подаётся обычным объектом — настоящего уведомления для него
	// не создаётся: обработчик видит `notification.data.url`, зовёт `close()`
	// и просит дождаться `waitUntil`, а всё остальное, что он делает с окнами
	// (письмо странице, фокус), настоящее
	var marker string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__marker = 'не перезагружалось'`, &marker)); err != nil {
		t.Fatal(err)
	}
	var fired string
	if err := chromedp.Run(wctx, chromedp.Evaluate(`(async () => {
  let closed = false, pending = null;
  const ev = new Event('notificationclick');
  ev.notification = { close() { closed = true; }, data: { url: '/#screen=notifications' }, tag: 'sema-alert1' };
  ev.waitUntil = (p) => { pending = p; };
  self.dispatchEvent(ev);
  if (!pending) return 'обработчик не попросил дождаться';
  await pending;
  return closed ? 'нажато' : 'уведомление не закрыто';
})()`, &fired, awaitPromise)); err != nil {
		t.Fatalf("нажатие на уведомление: %v", err)
	}
	if fired != "нажато" {
		t.Fatalf("нажатие: %s", fired)
	}
	pushWaitScreen(t, ctx, "notifications")
	var still string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`window.__marker || ''`, &still))
	if still != "не перезагружалось" {
		t.Error("окно перезагрузилось по нажатию: набранное и открытый чат были бы потеряны")
	}

	// 7. значок-счётчик: число из пуша ставится работником, ноль снимает
	if err := chromedp.Run(wctx, chromedp.Evaluate(`self.__badge.splice(0).length`, nil)); err != nil {
		t.Fatal(err)
	}
	push(body(`{"title":"Значок","tag":"sema-badge1","waiting":3}`))
	push(body(`{"title":"Значок 2","tag":"sema-badge2","waiting":0}`))
	var badge string
	if err := chromedp.Run(wctx, chromedp.Evaluate(`JSON.stringify(self.__badge)`, &badge)); err != nil {
		t.Fatal(err)
	}
	if badge != `[3,"снят"]` {
		t.Errorf("значок: %s, ждали [3,\"снят\"]: число из пуша ставится, ноль снимает", badge)
	}

	// Доказательство, что в системе ничего не показано: у регистрации нет ни
	// одного настоящего уведомления. Всё, что видел тест, — записи подмены
	var real string
	if err := chromedp.Run(wctx, chromedp.Evaluate(`(async () => String((await self.registration.getNotifications()).length))()`, &real, awaitPromise)); err != nil {
		t.Fatal(err)
	}
	if real != "0" {
		t.Errorf("у работника %s настоящих уведомлений: тест показал что-то в центре уведомлений хозяина", real)
	}
}

// pushRow — текст и кнопка строки «Пуши на этом устройстве».
func pushRow(t *testing.T, ctx context.Context) (state, caption, button string) {
	t.Helper()
	var out [3]string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
  const b = document.querySelector('[data-action="$push"]');
  const c = b && b.closest('.row') && b.closest('.row').querySelector('.caption');
  return [b ? (b.getAttribute('data-state') || '') : '', c ? c.textContent : '', b ? b.textContent : ''];
})()`, &out)); err != nil {
		t.Fatal(err)
	}
	return out[0], out[1], out[2]
}

// Страница называет, что мешает пушам на этом устройстве, и не врёт про
// возможности: на iPhone в обычной вкладке — «добавьте на экран «Домой»»,
// а не «браузер пуши не умеет».
func TestBrowserPushStatesOnPhones(t *testing.T) {
	const (
		iphoneTab  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
		androidChr = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
	)
	ctx, srv, _, _ := pushBrowser(t)
	// Режим окна подменяется в самой странице: Chrome не даёт переопределить
	// `display-mode` эмуляцией медиа, а на iOS «с экрана „Домой“» и вовсе
	// называется иначе — navigator.standalone. Обе дороги проверяются
	const (
		homeIOS     = `Object.defineProperty(Navigator.prototype, 'standalone', { configurable: true, get() { return true; } })`
		homeDisplay = `(() => { const was = window.matchMedia.bind(window); window.matchMedia = (q) => /display-mode:\s*standalone/.test(q) ? { matches: true, media: q, addEventListener() {}, removeEventListener() {} } : was(q); })()`
	)
	var injected page.ScriptIdentifier
	open := func(ua, standaloneScript string, perm browser.PermissionSetting) {
		t.Helper()
		acts := []chromedp.Action{
			emulation.SetUserAgentOverride(ua),
			browser.SetPermission(&browser.PermissionDescriptor{Name: "notifications"}, perm).WithOrigin(srv.URL),
			chromedp.ActionFunc(func(ctx context.Context) error {
				if injected != "" {
					_ = page.RemoveScriptToEvaluateOnNewDocument(injected).Do(ctx)
					injected = ""
				}
				if standaloneScript == "" {
					return nil
				}
				id, err := page.AddScriptToEvaluateOnNewDocument(standaloneScript).Do(ctx)
				injected = id
				return err
			}),
			// с пустой страницы: переход по тому же адресу меняет только хэш и
			// страницу не перезагружает, а режим окна читается при загрузке
			chromedp.Navigate("about:blank"),
			chromedp.Navigate(srv.URL + "/#screen=settings:notify"),
		}
		if err := chromedp.Run(ctx, acts...); err != nil {
			if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
				t.Skipf("Chrome не найден: %v", err)
			}
			t.Fatal(err)
		}
		pushWaitScreen(t, ctx, "settings:notify")
		pollJS(t, ctx, "строка пушей нарисована состоянием, а не заготовкой",
			`(() => { const b = document.querySelector('[data-action="$push"]'); return !!b && b.hasAttribute('data-state'); })()`)
	}

	// iPhone, обычная вкладка Safari: пушей здесь нет, и сказано, что делать
	open(iphoneTab, "", browser.PermissionSettingGranted)
	state, caption, button := pushRow(t, ctx)
	if state != "ios-tab" || !strings.Contains(caption, "На экран „Домой“") || !strings.Contains(caption, "только у Сёмы, добавленного на экран «Домой»") ||
		button != "Проверить ещё раз" {
		t.Errorf("iPhone во вкладке: состояние %q, кнопка %q, текст %q", state, button, caption)
	}
	if strings.Contains(caption, "браузер пуши не умеет") {
		t.Error("iPhone во вкладке назван неумеющим: умеет, но только с экрана «Домой»")
	}

	// iPhone с экрана «Домой», разрешения не давали: предлагается подписаться
	open(iphoneTab, homeIOS, browser.PermissionSettingPrompt)
	state, caption, button = pushRow(t, ctx)
	if state != "off" || button != "Получать пуши" || !strings.Contains(caption, "iPhone спросит разрешение один раз") {
		t.Errorf("iPhone с экрана «Домой»: состояние %q, кнопка %q, текст %q", state, button, caption)
	}

	// iPhone с экрана «Домой», разрешение отклонено: как вернуть — в настройках
	open(iphoneTab, homeIOS, browser.PermissionSettingDenied)
	state, caption, _ = pushRow(t, ctx)
	if state != "denied" || !strings.Contains(caption, "«Настройки» → «Уведомления» → «Сёма»") {
		t.Errorf("iPhone, разрешение отклонено: состояние %q, текст %q", state, caption)
	}

	// Android, разрешение отклонено: путь свой
	open(androidChr, "", browser.PermissionSettingDenied)
	state, caption, _ = pushRow(t, ctx)
	if state != "denied" || !strings.Contains(caption, "значок замка") || !strings.Contains(caption, "«Приложения»") {
		t.Errorf("Android, разрешение отклонено: состояние %q, текст %q", state, caption)
	}

	// Android, всё разрешено, не подписано
	open(androidChr, homeDisplay, browser.PermissionSettingGranted)
	state, caption, button = pushRow(t, ctx)
	if state != "off" || button != "Получать пуши" || strings.Contains(caption, "iPhone") {
		t.Errorf("Android не подписан: состояние %q, кнопка %q, текст %q", state, button, caption)
	}
}

// Подписка в браузере есть — страница сверяется с сервером: подписано,
// «сервер забыл» (передаётся заново без жеста), «ключ сменился» (нужна
// кнопка). Служба пушей здесь подменена: настоящая подписка потребовала бы
// настоящей службы Google.
func TestBrowserPushStateWithSubscription(t *testing.T) {
	ctx, srv, s, st := pushBrowser(t)
	const endpoint = "https://fcm.googleapis.com/fcm/send/ПРОБА"
	fake := `(() => {
  const sub = { endpoint: ` + jsString(endpoint) + `, unsubscribed: false,
    toJSON() { return { endpoint: this.endpoint, keys: { p256dh: 'BKey', auth: 'Aut' } }; },
    unsubscribe() { this.unsubscribed = true; return Promise.resolve(true); } };
  window.__sub = sub; window.__subscribed = 0;
  PushManager.prototype.getSubscription = function () { return Promise.resolve(window.__sub.unsubscribed ? null : window.__sub); };
  PushManager.prototype.subscribe = function (opts) { window.__subscribeOpts = opts; window.__subscribed++; window.__sub.unsubscribed = false; return Promise.resolve(window.__sub); };
})()`
	if err := chromedp.Run(ctx,
		browser.SetPermission(&browser.PermissionDescriptor{Name: "notifications"}, browser.PermissionSettingGranted).WithOrigin(srv.URL),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(fake).Do(ctx)
			return err
		}),
		chromedp.Navigate(srv.URL+"/#screen=settings:notify"),
	); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	pushWaitScreen(t, ctx, "settings:notify")

	// сервер подписки не знает: страница передаёт её сама, без нажатия
	pollJS(t, ctx, "сервер получил забытую подписку", `(() => { const b = document.querySelector('[data-action="$push"]'); return !!b && b.getAttribute('data-state') === 'on'; })()`)
	if _, ok := st.PushSubscriptionByEndpoint(endpoint); !ok {
		t.Fatal("страница сказала «подписано», а сервер подписки не получил")
	}
	state, caption, button := pushRow(t, ctx)
	if state != "on" || button != "Отписать это устройство" || !strings.Contains(caption, "Подписано") {
		t.Errorf("подписано: состояние %q, кнопка %q, текст %q", state, button, caption)
	}

	// ключ на сервере другой: нужна новая подписка, и это сказано
	dev, _ := st.PushSubscriptionByEndpoint(endpoint)
	if err := st.SavePushSubscription(&store.PushSubscription{Endpoint: endpoint, P256dh: dev.P256dh, Auth: dev.Auth, VAPIDKey: "старый-ключ", Agent: dev.Agent}); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(async () => { await sema.pushPaint(); return 1; })()`, nil, awaitPromise)); err != nil {
		t.Fatal(err)
	}
	state, caption, button = pushRow(t, ctx)
	if state != "resub" || button != "Переподписать" || !strings.Contains(caption, "Ключи пушей Сёмы сменились") {
		t.Errorf("ключ сменился: состояние %q, кнопка %q, текст %q", state, button, caption)
	}

	// нажатие: разрешение уже есть, подписка идёт заново под ключом сервера,
	// сервер получает её с режимом окна и касаниями
	pub, _ := s.eng.PushPublicKey()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('[data-action="$push"]').click()`, nil)); err != nil {
		t.Fatal(err)
	}
	pollJS(t, ctx, "подписка сделана заново", `(() => window.__subscribed === 1)()`)
	var opts string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`JSON.stringify({user: window.__subscribeOpts.userVisibleOnly, key: Array.from(new Uint8Array(window.__subscribeOpts.applicationServerKey)).length})`, &opts)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opts, `"user":true`) || !strings.Contains(opts, `"key":65`) {
		t.Errorf("подписка без userVisibleOnly или с чужим ключом: %s", opts)
	}
	pollJS(t, ctx, "после переподписки состояние «подписано»", `(() => { const b = document.querySelector('[data-action="$push"]'); return !!b && b.getAttribute('data-state') === 'on'; })()`)
	if d, _ := st.PushSubscriptionByEndpoint(endpoint); d.VAPIDKey != pub {
		t.Errorf("сервер записал подписку под ключом %q, ждали текущий", d.VAPIDKey)
	}

	// «Отписать»: сервер забывает устройство
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('[data-action="$push"]').click()`, nil)); err != nil {
		t.Fatal(err)
	}
	pollJS(t, ctx, "устройство отписано", `(() => { const b = document.querySelector('[data-action="$push"]'); return !!b && b.getAttribute('data-state') === 'off'; })()`)
	if _, ok := st.PushSubscriptionByEndpoint(endpoint); ok {
		t.Error("устройство осталось у сервера после «Отписать»")
	}
}

// Разрешение просится первым делом в обработчике касания, до любого
// запроса в сеть: iOS и Safari принимают requestPermission только оттуда, а
// subscribe() сразу за ним.
func TestBrowserPushAsksPermissionFirst(t *testing.T) {
	ctx, srv, _, st := pushBrowser(t)
	const spy = `(() => {
  window.__order = [];
  Notification.requestPermission = function () { window.__order.push('разрешение'); return Promise.resolve('granted'); };
  const was = window.fetch.bind(window);
  window.fetch = function (...a) {
    const path = String(a[0]).split('?')[0];
    if (path.startsWith('/api/push')) window.__order.push('сеть ' + path);
    return was(...a);
  };
  const sub = { endpoint: 'https://fcm.googleapis.com/fcm/send/КАСАНИЕ', unsubscribed: true,
    toJSON() { return { endpoint: this.endpoint, keys: { p256dh: 'BKey', auth: 'Aut' } }; },
    unsubscribe() { this.unsubscribed = true; return Promise.resolve(true); } };
  PushManager.prototype.getSubscription = function () { return Promise.resolve(sub.unsubscribed ? null : sub); };
  PushManager.prototype.subscribe = function () { window.__order.push('подписка'); sub.unsubscribed = false; return Promise.resolve(sub); };
  Object.defineProperty(Notification, 'permission', { configurable: true, get() { return window.__order.includes('разрешение') ? 'granted' : 'default'; } });
})()`
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(spy).Do(ctx)
			return err
		}),
		chromedp.Navigate(srv.URL+"/#screen=settings:notify"),
	); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	pushWaitScreen(t, ctx, "settings:notify")
	// ключ готов до нажатия: между касанием и подпиской сети быть не должно
	pollJS(t, ctx, "ключ и работник подготовлены заранее", `(() => { const b = document.querySelector('[data-action="$push"]'); return !!b && b.getAttribute('data-state') === 'off' && !!sema._pushKey && !!sema._pushReg; })()`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__order.length = 0; document.querySelector('[data-action="$push"]').click()`, nil)); err != nil {
		t.Fatal(err)
	}
	pollJS(t, ctx, "устройство подписалось", `(() => window.__order.includes('сеть /api/push/subscribe'))()`)
	var order string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`JSON.stringify(window.__order)`, &order)); err != nil {
		t.Fatal(err)
	}
	var steps []string
	_ = json.Unmarshal([]byte(order), &steps)
	if len(steps) < 3 || steps[0] != "разрешение" {
		t.Fatalf("первым в обработчике касания должно идти разрешение, а вышло: %v", steps)
	}
	// между разрешением и подпиской — никакой сети
	for _, st := range steps[1:] {
		if st == "подписка" {
			break
		}
		if strings.HasPrefix(st, "сеть") {
			t.Errorf("между разрешением и подпиской ушёл запрос %q: iOS потеряет жест", st)
		}
	}
	if steps[1] != "подписка" {
		t.Errorf("после разрешения должна идти подписка: %v", steps)
	}
	// запрос уже ушёл, но сервер мог его ещё не принять: ждём запись, а не гадаем
	var subs []*store.PushSubscription
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(subs) == 0; time.Sleep(50 * time.Millisecond) {
		subs, _ = st.PushSubscriptions()
	}
	if len(subs) != 1 || subs[0].Endpoint != "https://fcm.googleapis.com/fcm/send/КАСАНИЕ" {
		t.Errorf("сервер получил %d устройств", len(subs))
	}
}

// ---- Предложение подписаться на «Ждут вас» ----
//
// Владелец на iPhone до кнопки «Получать пуши» не дошёл: она лежала на
// глубине четырёх шагов в настройках. Теперь та же кнопка стоит наверху
// экрана «Ждут вас». Строку рисует сервер скрытой, а открывает клиент по
// состоянию устройства. Служба пушей, показ уведомлений и разрешение здесь
// не настоящие: подписка подменена, разрешение выставляется через CDP.

// offerPushStub — подмена подписки в странице: поддельная подписка, которая
// то есть, то нет. Служба пушей не нужна: подписку примет наш сервер, а
// уведомлений никто не показывает.
func offerPushStub(subscribed bool) string {
	return `(() => {
  const sub = { endpoint: 'https://fcm.googleapis.com/fcm/send/ПРЕДЛОЖЕНИЕ', unsubscribed: ` + strconv.FormatBool(!subscribed) + `,
    toJSON() { return { endpoint: this.endpoint, keys: { p256dh: 'BKey', auth: 'Aut' } }; },
    unsubscribe() { this.unsubscribed = true; return Promise.resolve(true); } };
  PushManager.prototype.getSubscription = function () { return Promise.resolve(sub.unsubscribed ? null : sub); };
  PushManager.prototype.subscribe = function () { sub.unsubscribed = false; window.__subscribed = (window.__subscribed || 0) + 1; return Promise.resolve(sub); };
})()`
}

// offerBrowser открывает экран с заданными строкой браузера, разрешением и
// подменой в странице; прежняя подмена снимается.
type offerBrowser struct {
	t        *testing.T
	ctx      context.Context
	base     string
	injected page.ScriptIdentifier
}

func (b *offerBrowser) open(screen, ua, script string, perm browser.PermissionSetting) {
	b.t.Helper()
	err := chromedp.Run(b.ctx,
		emulation.SetUserAgentOverride(ua),
		browser.SetPermission(&browser.PermissionDescriptor{Name: "notifications"}, perm).WithOrigin(b.base),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if b.injected != "" {
				_ = page.RemoveScriptToEvaluateOnNewDocument(b.injected).Do(ctx)
				b.injected = ""
			}
			if script == "" {
				return nil
			}
			id, err := page.AddScriptToEvaluateOnNewDocument(script).Do(ctx)
			b.injected = id
			return err
		}),
		// с пустой страницы: переход по тому же адресу меняет только хэш
		chromedp.Navigate("about:blank"),
		chromedp.Navigate(b.base+"/#screen="+screen),
	)
	if err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			b.t.Skipf("Chrome не найден: %v", err)
		}
		b.t.Fatal(err)
	}
	// Первая загрузка в новом Chrome ставит служебного работника, и страница
	// перезагружается один раз (controllerchange в app.js): контекст, в
	// котором спрашивали, пропадает. Поэтому ждём, терпя такие ошибки, а не
	// падая на первой из них
	b.waitFor("экран "+screen+" нарисован", `(() => { const e = document.getElementById('screen'); return !!e && e.dataset.screen === `+jsString(screen)+`; })()`)
	// строка пушей нарисована состоянием: клиент проверил устройство
	b.waitFor("состояние пушей определено", `(() => { const k = document.querySelector('[data-action="$push"]'); return !!k && k.hasAttribute('data-state'); })()`)
}

// waitFor — ждёт истинного выражения на странице, не падая на исчезнувшем
// контексте (перезагрузка страницы под ногами).
func (b *offerBrowser) waitFor(what, expr string) {
	b.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		if err := chromedp.Run(b.ctx, chromedp.Evaluate(expr, &ok)); err == nil && ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.t.Fatalf("не дождались: %s", what)
}

// offerState — видна ли строка предложения: «видна», «скрыта» или «нет».
func (b *offerBrowser) offerState() string {
	b.t.Helper()
	var out string
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`(() => {
  const r = document.querySelector('.row.offer');
  if (!r) return 'нет';
  const gone = (e) => !e || getComputedStyle(e).display === 'none';
  return gone(r) || gone(r.closest('.row-wrap')) || r.getBoundingClientRect().height === 0 ? 'скрыта' : 'видна';
})()`, &out)); err != nil {
		b.t.Fatal(err)
	}
	return out
}

func newOfferBrowser(t *testing.T) *offerBrowser {
	ctx, srv, _, _ := pushBrowser(t)
	return &offerBrowser{t: t, ctx: ctx, base: srv.URL}
}

// «Ждут вас» показывает предложение, когда с подпиской можно что-то сделать,
// и прячет, когда нельзя или не нужно; «Не предлагать» убирает его насовсем
// для этого браузера, а в настройках строка остаётся.
func TestBrowserPushOfferShownByState(t *testing.T) {
	const (
		iphoneTab  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
		androidChr = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
	)
	b := newOfferBrowser(t)

	// не подписано, разрешение можно спросить: предложение видно, с основной кнопкой
	b.open("notifications", androidChr, offerPushStub(false), browser.PermissionSettingPrompt)
	if got := b.offerState(); got != "видна" {
		t.Fatalf("не подписано: строка %s, ждали «видна»", got)
	}
	var offer [3]string
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`(() => {
  const r = document.querySelector('.row.offer');
  const k = r.querySelector('[data-action="$push"]');
  return [k.textContent, r.querySelector('.caption').textContent, k.className];
})()`, &offer)); err != nil {
		t.Fatal(err)
	}
	if offer[0] != "Получать пуши" || !strings.Contains(offer[1], "Не подписано") || !strings.Contains(offer[2], "primary") {
		t.Errorf("предложение: кнопка %q, текст %q, класс %q", offer[0], offer[1], offer[2])
	}

	// iPhone в обычной вкладке и отклонённое разрешение: человек может что-то
	// сделать (добавить на экран «Домой», вернуть разрешение) — строка видна
	b.open("notifications", iphoneTab, "", browser.PermissionSettingGranted)
	if got := b.offerState(); got != "видна" {
		t.Errorf("iPhone во вкладке: строка %s, ждали «видна»", got)
	}
	b.open("notifications", androidChr, offerPushStub(false), browser.PermissionSettingDenied)
	if got := b.offerState(); got != "видна" {
		t.Errorf("разрешение отклонено: строка %s, ждали «видна»", got)
	}

	// подписано: предлагать нечего
	b.open("notifications", androidChr, offerPushStub(true), browser.PermissionSettingGranted)
	pollJS(b.t, b.ctx, "устройство подписано", `(() => document.querySelector('[data-action="$push"]').getAttribute('data-state') === 'on')()`)
	if got := b.offerState(); got != "скрыта" {
		t.Errorf("подписано: строка %s, ждали «скрыта»", got)
	}

	// браузер пуши не умеет: предложить нечего
	b.open("notifications", androidChr, `delete window.PushManager`, browser.PermissionSettingGranted)
	if got := b.offerState(); got != "скрыта" {
		t.Errorf("браузер не умеет пуши: строка %s, ждали «скрыта»", got)
	}

	// в «Настройки → Уведомления» строка видна при любом состоянии
	b.open(notifySettingsScreen, androidChr, offerPushStub(true), browser.PermissionSettingGranted)
	var inSettings string
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`(() => {
  const k = document.querySelector('[data-action="$push"]');
  const r = k && k.closest('.row');
  return r && !r.classList.contains('offer') && r.getBoundingClientRect().height > 0 ? 'видна' : 'нет';
})()`, &inSettings)); err != nil {
		t.Fatal(err)
	}
	if inSettings != "видна" {
		t.Errorf("в настройках строка пушей %s", inSettings)
	}

	// «Не предлагать на этом устройстве»: строка исчезает и не возвращается
	// ни при обновлении экрана, ни после перезагрузки страницы
	b.open("notifications", androidChr, offerPushStub(false), browser.PermissionSettingPrompt)
	if got := b.offerState(); got != "видна" {
		t.Fatalf("перед отказом строка %s", got)
	}
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`document.querySelector('.row.offer [data-action="$push_later"]').click()`, nil)); err != nil {
		t.Fatal(err)
	}
	if got := b.offerState(); got != "скрыта" {
		t.Errorf("после «Не предлагать» строка %s", got)
	}
	var stored string
	_ = chromedp.Run(b.ctx, chromedp.Evaluate(`localStorage.getItem('sema.push.offer.off') || ''`, &stored))
	if stored != "1" {
		t.Errorf("отказ не запомнился в браузере: %q", stored)
	}
	// обновление экрана: строка снова приезжает с сервера, но остаётся скрытой
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`new Promise((done) => {
  const el = document.getElementById('screen');
  document.body.addEventListener('htmx:after:swap', () => setTimeout(() => done(1), 400), { once: true });
  htmx.trigger(el, 'sema:refresh');
})`, nil, awaitPromise)); err != nil {
		t.Fatal(err)
	}
	if got := b.offerState(); got != "скрыта" {
		t.Errorf("после обновления экрана строка %s", got)
	}
	b.open("notifications", androidChr, offerPushStub(false), browser.PermissionSettingPrompt)
	if got := b.offerState(); got != "скрыта" {
		t.Errorf("после перезагрузки страницы строка %s: отказ не должен забываться", got)
	}
	// а в настройках кнопка осталась
	b.open(notifySettingsScreen, androidChr, offerPushStub(false), browser.PermissionSettingPrompt)
	var left string
	_ = chromedp.Run(b.ctx, chromedp.Evaluate(`(() => { const k = document.querySelector('[data-action="$push"]'); return k && k.getBoundingClientRect().height > 0 ? 'есть' : 'нет'; })()`, &left))
	if left != "есть" {
		t.Errorf("после отказа в настройках пропала кнопка: %s", left)
	}
}

// Строка предложения не мигает при обновлении экрана (живое обновление
// перерисовывает «Ждут вас» постоянно), а после удачной подписки говорит
// «Готово» и исчезает при следующей отрисовке.
func TestBrowserPushOfferNoFlicker(t *testing.T) {
	const androidChr = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
	b := newOfferBrowser(t)
	b.open("notifications", androidChr, offerPushStub(false), browser.PermissionSettingGranted)
	if got := b.offerState(); got != "видна" {
		t.Fatalf("строка %s", got)
	}

	// каждый кадр отрисовки: видна ли строка. Экран перерисовывается несколько
	// раз подряд, и ни в одном кадре строка не должна пропасть
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`(() => {
  window.__frames = []; window.__swaps = 0; window.__run = true;
  document.body.addEventListener('htmx:after:swap', () => { window.__swaps++; });
  const seen = () => {
    const r = document.querySelector('.row.offer');
    const gone = (e) => !e || getComputedStyle(e).display === 'none';
    return !!r && !gone(r) && !gone(r.closest('.row-wrap'));
  };
  const tick = () => { window.__frames.push(seen()); if (window.__run) requestAnimationFrame(tick); };
  requestAnimationFrame(tick);
  return 1;
})()`, nil)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		before := 0
		_ = chromedp.Run(b.ctx, chromedp.Evaluate(`window.__swaps`, &before))
		if err := chromedp.Run(b.ctx, chromedp.Evaluate(`htmx.trigger(document.getElementById('screen'), 'sema:refresh')`, nil)); err != nil {
			t.Fatal(err)
		}
		pollJS(t, b.ctx, "экран перерисован", `(() => window.__swaps > `+strconv.Itoa(before)+`)()`)
		time.Sleep(250 * time.Millisecond) // пока клиент ещё раз проверяет устройство
	}
	var res string
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`(() => { window.__run = false; return JSON.stringify({ n: window.__frames.length, hidden: window.__frames.filter((v) => !v).length, swaps: window.__swaps }); })()`, &res)); err != nil {
		t.Fatal(err)
	}
	var m struct{ N, Hidden, Swaps int }
	_ = json.Unmarshal([]byte(strings.NewReplacer(`"n"`, `"N"`, `"hidden"`, `"Hidden"`, `"swaps"`, `"Swaps"`).Replace(res)), &m)
	if m.Swaps < 3 || m.N < 10 {
		t.Fatalf("проверка мигания не набрала данных: %s", res)
	}
	if m.Hidden > 0 {
		t.Errorf("строка мигала: скрыта в %d кадрах из %d при %d перерисовках экрана", m.Hidden, m.N, m.Swaps)
	}

	// подписка: нажатие на кнопку предложения. Разрешение уже выдано, подписка
	// подменена; строка говорит «Готово» и остаётся, пока экран не перерисуют
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`document.querySelector('.row.offer [data-action="$push"]').click()`, nil)); err != nil {
		t.Fatal(err)
	}
	pollJS(t, b.ctx, "строка сказала «Готово»", `(() => { const c = document.querySelector('.row.offer .caption'); return !!c && c.textContent.startsWith('Готово.'); })()`)
	if got := b.offerState(); got != "видна" {
		t.Errorf("после подписки строка %s: она должна сказать «Готово» и остаться", got)
	}
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`htmx.trigger(document.getElementById('screen'), 'sema:refresh')`, nil)); err != nil {
		t.Fatal(err)
	}
	pollJS(t, b.ctx, "строка исчезла при следующей отрисовке", `(() => { const r = document.querySelector('.row.offer'); return !!r && getComputedStyle(r).display === 'none'; })()`)
}

// На телефоне (390 точек) строка предложения не выпирает вбок: основная
// кнопка и «···» остаются в окне. Состояние «нужен экран „Домой“» — самое
// длинное по тексту, на нём и мерим.
func TestBrowserPushOfferPhoneWidth(t *testing.T) {
	const iphoneTab = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
	b := newOfferBrowser(t)
	if err := chromedp.Run(b.ctx,
		emulation.SetDeviceMetricsOverride(390, 844, 3, true),
		emulation.SetTouchEmulationEnabled(true),
	); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]struct{ ua, script string }{
		"iPhone во вкладке": {iphoneTab, ""},
		"не подписано":      {"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36", offerPushStub(false)},
	} {
		b.open("notifications", setup.ua, setup.script, browser.PermissionSettingPrompt)
		if got := b.offerState(); got != "видна" {
			t.Fatalf("%s: строка %s", name, got)
		}
		var m struct {
			Scroll, Client int
			Left, Right    float64
			Button, Menu   float64
			Overflow       []string
		}
		var raw string
		if err := chromedp.Run(b.ctx, chromedp.Evaluate(`(() => {
  const d = document.documentElement, r = document.querySelector('.row.offer');
  const box = r.getBoundingClientRect();
  const k = r.querySelector('.action.primary').getBoundingClientRect();
  const menu = r.querySelector('.row-menu summary').getBoundingClientRect();
  const over = [];
  for (const e of r.querySelectorAll('*')) { const q = e.getBoundingClientRect(); if (q.width > 0 && q.right > d.clientWidth + 0.5) over.push(e.tagName + '.' + e.className); }
  return JSON.stringify({ Scroll: d.scrollWidth, Client: d.clientWidth, Left: box.left, Right: box.right, Button: k.right, Menu: menu.right, Overflow: over });
})()`, &raw)); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		if m.Scroll > m.Client {
			t.Errorf("%s: страница шире окна: %d > %d", name, m.Scroll, m.Client)
		}
		if m.Left < 0 || m.Right > float64(m.Client) || m.Button > float64(m.Client) || m.Menu > float64(m.Client) {
			t.Errorf("%s: строка выпирает вбок: %s", name, raw)
		}
		if len(m.Overflow) > 0 {
			t.Errorf("%s: за краем окна: %v", name, m.Overflow)
		}
	}
}
