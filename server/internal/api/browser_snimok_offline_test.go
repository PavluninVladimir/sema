package api

// Снимок экрана в настоящем Chrome (offline-snapshot, 4.5).
//
// Проверяется то, что обещано спекой, и теми жестами, которыми это делает
// человек: посмотрел экран, Сёма пропала, открыл приложение заново. Ни одна
// функция клиента напрямую не зовётся — иначе зелено, а сломано.
//
// ПОЧЕМУ НЕ СНАСТЬ `disableNetwork`. Она честная и правда блокирует запросы,
// но блокировка CDP (`Network.setBlockedURLs`) стоит **до** служебного
// работника: запрос не доходит до него вовсе. Проверено трассой кадров
// 27 сентября 2026: при включённой блокировке событие `fetch` в работнике не
// наступает ни разу, а после перезагрузки страницы даже `app.js` и `htmx.js`
// приходят со статусом 0 — то есть оболочку из кэша работника тоже не
// отдать. Снимок в такой обстановке проверить нечем: путь, который его
// достаёт, живёт ровно в том обработчике, до которого запрос не доезжает.
//
// Поэтому связь рвётся так, как она рвётся на самом деле: **Сёма перестаёт
// отвечать**. Это и есть настоящий случай владельца — сервер дома, телефон
// в городе. Сервер поднимается своим слушателем, чтобы его можно было
// остановить и поднять обратно на том же порту: адрес входит в происхождение
// страницы, и переезд на другой порт означал бы чистый браузер без снимка.
// `navigator.onLine` при этом переключается снастью — без блокировки адресов
// она как раз это и делает.

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// scriptSnapshotKeys — что лежит в кэше снимков. Читаем кэш глазами
// страницы, а не работника: имя кэша — договор между ними, и если они
// разойдутся, тест это увидит.
const scriptSnapshotKeys = `(async () => {
  const кэш = await caches.open('сёма-снимки');
  const ключи = await кэш.keys();
  return ключи.map((к) => decodeURIComponent(new URL(к.url).search || new URL(к.url).pathname)).join(' | ');
})()`

func waitFor(t *testing.T, ctx context.Context, условие, зачем string, срок time.Duration) {
	t.Helper()
	var готово bool
	err := chromedp.Run(ctx, chromedp.Poll(условие, &готово, chromedp.WithPollingTimeout(срок)))
	if err != nil || !готово {
		var что string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
      const э = document.getElementById('screen');
      const ш = document.getElementById('snapshot-header');
      const п = document.getElementById('link-banner');
      return 'экран=' + (э ? э.getAttribute('data-screen') : 'нет')
        + ' шапка=' + (ш ? ш.textContent : 'нет')
        + ' плашка=' + (п && !п.hidden ? п.textContent : 'нет');
    })()`, &что))
		t.Fatalf("%s: %v (на странице: %s)", зачем, err, что)
	}
}

func evaluate(t *testing.T, ctx context.Context, скрипт string) string {
	t.Helper()
	var итог string
	if err := chromedp.Run(ctx, chromedp.Evaluate(скрипт, &итог,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
		t.Fatalf("скрипт %q: %v", скрипт, err)
	}
	return итог
}

// semaOnPort — сервер на своём слушателе: его можно остановить и поднять
// обратно на том же адресе.
func semaOnPort(t *testing.T, s *Server, addr string) (string, func()) {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("слушатель на %s: %v", addr, err)
	}
	srv := &http.Server{Handler: s.Handler()}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), func() { _ = srv.Close() }
}

func TestBrowserCachedScreenOffline(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	// вход нужен всем: отметка «моё устройство» живёт у сессии, а у
	// локального доступа своей сессии нет вовсе
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	token, hash := makeSession(t, s, st)
	if _, err := s.accessActions(actionRequest{Action: "mark_own_device", Target: hash}); err != nil {
		t.Fatal(err)
	}
	addr, стоп := semaOnPort(t, s, "")
	корень := "http://" + addr

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 150*time.Second)
	defer cancelTime()

	// Консоль проверяется не в конце, а на каждом шаге, и падение здесь
	// немедленное. Иначе исключение уезжает под первый же t.Fatalf по
	// другому поводу — а именно так и проехала кириллица в имени
	// заголовка: `Headers.get('X-Sema-Снято')` роняло клиент на каждом
	// обмене с htmx, и снимок молча не складывался (27 сентября 2026).
	var exceptions []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			exceptions = append(exceptions, e.ExceptionDetails.Error())
		}
	})
	чисто := func(where string) {
		t.Helper()
		if len(exceptions) > 0 {
			t.Fatalf("исключения в консоли браузера (%s):\n%s", where, strings.Join(exceptions, "\n"))
		}
	}

	// cookie сессии до первого перехода: иначе первый же запрос получит 401
	if err := chromedp.Run(ctx, network.Enable(),
		network.SetCookie("sema_session", token).WithURL(корень)); err != nil {
		t.Fatalf("положить cookie сессии: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Navigate(корень+"/#screen=inbox")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitFor(t, ctx, `(() => { const э = document.getElementById('screen'); return !!э && э.getAttribute('data-screen') === 'inbox'; })()`,
		"«Почта» не нарисовалась", 20*time.Second)

	// признаки и правда доехали до браузера (4.0): не «сервер их проставил»,
	// а «живой клиент их видит»
	признаки := evaluate(t, ctx, `(() => {
    const э = document.getElementById('screen');
    return ['storable', 'own-device', 'dictionary'].map((и) => и + '=' + э.getAttribute('data-' + и)).join(' ');
  })()`)
	t.Logf("признаки у #screen: %s", признаки)
	чисто("первая отрисовка")
	if !strings.Contains(признаки, "storable=1") || !strings.Contains(признаки, "own-device=1") {
		t.Fatalf("признаки снимка не доехали до браузера: %s", признаки)
	}

	var работникЕсть bool
	_ = chromedp.Run(ctx, chromedp.Evaluate(
		`(async () => { const р = await navigator.serviceWorker.ready.catch(() => null); return !!(р && р.active); })()`,
		&работникЕсть, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	if !работникЕсть {
		t.Skip("служебный работник не поднялся — снимок складывать некому")
	}
	// работник берёт страницу не сразу: пока он не контролирует её, запросы
	// идут мимо него и снимок не складывается. Просим экран заново, пока
	// снимок не появится в кэше
	waitFor(t, ctx, `(async () => {
    if (!navigator.serviceWorker.controller) return false;
    const кэш = await caches.open('сёма-снимки');
    const ключи = await кэш.keys();
    if (ключи.some((к) => к.url.includes('inbox'))) return true;
    htmx.trigger(document.getElementById('screen'), 'sema:refresh');
    return false;
  })()`, "снимок «Почты» не лёг в кэш", 30*time.Second)
	t.Logf("в кэше снимков: %s", evaluate(t, ctx, scriptSnapshotKeys))
	чисто("снимок сложен")

	// заглянули в хранилище секретов: этот экран храниться не должен ни при
	// каких обстоятельствах
	_ = chromedp.Run(ctx, chromedp.Evaluate(`htmx.ajax('GET','/ui?screen=settings%3Asecrets',{target:'#window',swap:'innerHTML'})`, nil))
	waitFor(t, ctx, `(() => { const э = document.getElementById('screen'); return !!э && э.getAttribute('data-screen').indexOf('settings:secrets') === 0; })()`,
		"хранилище секретов не открылось", 20*time.Second)
	if got := evaluate(t, ctx, `document.getElementById('screen').getAttribute('data-storable')`); got != "0" {
		t.Errorf("хранилище секретов помечено хранимым: data-storable=%q", got)
	}
	if ключи := evaluate(t, ctx, scriptSnapshotKeys); strings.Contains(ключи, "secrets") {
		t.Fatalf("секреты легли в снимок: %s", ключи)
	}
	// вернулись на «Почту»
	_ = chromedp.Run(ctx, chromedp.Evaluate(`htmx.ajax('GET','/ui?screen=inbox',{target:'#window',swap:'innerHTML'})`, nil))
	waitFor(t, ctx, `(() => { const э = document.getElementById('screen'); return !!э && э.getAttribute('data-screen') === 'inbox'; })()`,
		"«Почта» не вернулась", 20*time.Second)

	// ---- Сёма пропала: человек открывает приложение заново ----
	if err := chromedp.Run(ctx, network.OverrideNetworkState(true, 0, -1, -1)); err != nil {
		t.Fatalf("пометить страницу офлайном: %v", err)
	}
	стоп()
	// сервер и правда не отвечает: без этой проверки «снимок» мог бы
	// оказаться свежим ответом, а тест — зелёным на пустом месте
	waitFor(t, ctx, `(async () => {
    try { await fetch('/api/health', {cache: 'no-store'}); return false; } catch (e) { return true; }
  })()`, "Сёма всё ещё отвечает — рвать связь не получилось", 20*time.Second)

	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Logf("перезагрузка без Сёмы: %v", err) // оболочка приезжает из кэша, событий может не быть
	}
	_ = chromedp.Run(ctx, chromedp.WaitReady("body", chromedp.ByQuery))
	waitFor(t, ctx, `(() => {
    const ш = document.getElementById('snapshot-header');
    const э = document.getElementById('screen');
    return !!ш && !!э && э.getAttribute('data-screen') === 'inbox' && ш.textContent.indexOf('Снимок от') === 0;
  })()`, "без связи не видно прежнего экрана с шапкой снимка", 40*time.Second)
	шапка := evaluate(t, ctx, `document.getElementById('snapshot-header').textContent`)
	t.Logf("шапка снимка: %s", шапка)
	чисто("показан снимок")
	if !strings.Contains(шапка, "связи нет") {
		t.Errorf("шапка не говорит, что связи нет: %q", шапка)
	}

	// неоткладываемые действия в снимке спрятаны, а сам снимок помечен
	скрыто := evaluate(t, ctx, `(() => {
    const э = document.getElementById('screen');
    const видно = [...э.querySelectorAll('[hx-post]')].filter((эл) => !эл.classList.contains('not-in-snapshot')
      && !эл.hasAttribute('data-deferrable') && !эл.closest('[data-deferrable]'));
    return э.classList.contains('snapshot') + ' лишних=' + видно.length + ' спрятано=' + э.querySelectorAll('.not-in-snapshot').length;
  })()`)
	t.Logf("снимок помечен и спрятал лишнее: %s", скрыто)
	if !strings.HasPrefix(скрыто, "true") || !strings.Contains(скрыто, "лишних=0") {
		t.Errorf("в снимке остались действия, которые нельзя отложить: %s", скрыто)
	}

	// экран, которого в снимке нет, из снимка и не показывается — и сказано,
	// почему
	_ = chromedp.Run(ctx, chromedp.Evaluate(`htmx.ajax('GET','/ui?screen=settings%3Asecrets',{target:'#window',swap:'innerHTML'})`, nil))
	waitFor(t, ctx, `(() => {
    const п = document.getElementById('link-banner');
    const э = document.getElementById('screen');
    return !!э && э.getAttribute('data-screen') === 'inbox' && !!п && !п.hidden && п.textContent.indexOf('Связи нет') === 0;
  })()`, "секреты без связи должны не показаться и сказать, почему", 30*time.Second)
	t.Logf("про секреты без связи сказано: %s", evaluate(t, ctx, `document.getElementById('link-banner').textContent`))

	// ---- срок снимка (4.4): просроченный стирается, а не показывается ----
	// Состариваем сам снимок, а не часы: время получения лежит у него в
	// заголовке, и это единственное, чем измеряется срок.
	постарел := evaluate(t, ctx, `(async () => {
    const кэш = await caches.open('сёма-снимки');
    const ключ = (await кэш.keys()).find((к) => к.url.includes('inbox'));
    if (!ключ) return 'снимка нет';
    const текст = await (await кэш.match(ключ)).text();
    await кэш.put(ключ, new Response(текст, { headers: {
      'Content-Type': 'text/html; charset=utf-8',
      'X-Sema-Snapshot-At': new Date(Date.now() - 8 * 86400000).toISOString(),
      'X-Sema-Snapshot-Days': '7' } }));
    return 'состарен на 8 дней при сроке 7';
  })()`)
	t.Logf("срок снимка: %s", постарел)
	_ = chromedp.Run(ctx, chromedp.Evaluate(`sema.hideBanner(); htmx.ajax('GET','/ui?screen=inbox',{target:'#window',swap:'innerHTML'})`, nil))
	waitFor(t, ctx, `(async () => {
    const п = document.getElementById('link-banner');
    if (!п || п.hidden || п.textContent.indexOf('Связи нет') !== 0) return false;
    const кэш = await caches.open('сёма-снимки');
    return !(await кэш.keys()).some((к) => к.url.includes('inbox'));
  })()`, "просроченный снимок должен не показаться и стереться", 30*time.Second)
	чисто("срок снимка")

	// ---- Сёма вернулась: шапка снимка снята ----
	_, стоп2 := semaOnPort(t, s, addr)
	defer стоп2()
	if err := chromedp.Run(ctx, network.OverrideNetworkState(false, 0, -1, -1)); err != nil {
		t.Fatalf("вернуть страницу в сеть: %v", err)
	}
	waitFor(t, ctx, `(async () => {
    try { await fetch('/api/health', {cache: 'no-store'}); return true; } catch (e) { return false; }
  })()`, "Сёма не поднялась обратно", 30*time.Second)
	_ = chromedp.Run(ctx, chromedp.Evaluate(`htmx.ajax('GET','/ui?screen=inbox',{target:'#window',swap:'innerHTML'})`, nil))
	waitFor(t, ctx, `(() => {
    const э = document.getElementById('screen');
    return !!э && э.getAttribute('data-screen') === 'inbox' && !document.getElementById('snapshot-header');
  })()`, "связь вернулась, а шапка снимка осталась", 40*time.Second)
	// Подмена ленты сама на миг уносит шапку: её рисует клиент поверх
	// пришедшей разметки, и опрос «шапки нет» ловит именно этот миг. Значит
	// смотреть надо не один раз, а ещё раз спустя время: живой ответ шапку
	// снимает насовсем, а не до следующей отрисовки. Без этой второй
	// проверки тест не замечал поломки «шапка никогда не снимается»
	// (проверено нарочно 27 сентября 2026: было «ok»).
	time.Sleep(3 * time.Second)
	осталось := evaluate(t, ctx, `(() => {
    const ш = document.getElementById('snapshot-header');
    const э = document.getElementById('screen');
    return 'шапка=' + (ш ? ш.textContent : 'нет') + ' снимок-класс=' + (!!э && э.classList.contains('snapshot'))
      + ' спрятано=' + (э ? э.querySelectorAll('.not-in-snapshot').length : -1);
  })()`)
	t.Logf("после возврата связи: %s", осталось)
	if !strings.HasPrefix(осталось, "шапка=нет") || !strings.Contains(осталось, "снимок-класс=false") || !strings.HasSuffix(осталось, "спрятано=0") {
		t.Errorf("связь вернулась, а признаки снимка остались: %s", осталось)
	}

	чисто("после возврата связи")
}
