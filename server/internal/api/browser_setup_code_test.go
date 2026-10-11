package api

// Первый ключ кодом первой установки в настоящем Chrome — так, как его
// заводит человек со свежим Сёмой в Docker: браузер хозяина приходит в
// контейнер из сети моста, а не по петле, и страница входа просит код из
// `docker logs`. Страница входа — рукописный JS, которого не видит ни один
// тест на Go: поле кода, урезанная сессия, форма ключа и возврат в Сёму.
//
// Chrome нет — тест пропускается: SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/auth"
)

func TestBrowserFirstKeyBySetupCode(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, st := freshServer(t)
	code, _, ok := auth.IssueSetupCode(st)
	if !ok {
		t.Fatal("код первой установки не выдан")
	}
	// сеть моста Docker: сервер видит браузер хозяина не на петле
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "172.17.0.1:40000"
		s.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	addr := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	if err := st.SetSetting(auth.SettingOrigin, addr); err != nil {
		t.Fatal(err)
	}

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()
	var exceptions []string
	chromedp.ListenTarget(ctx, func(ev any) {
		e, ok := ev.(*runtime.EventExceptionThrown)
		if !ok {
			return
		}
		// беды оболочки интерфейса ловят её собственные тесты
		if text := e.ExceptionDetails.Error(); !strings.Contains(text, "app.js") && !strings.Contains(text, "htmx.js") {
			exceptions = append(exceptions, text)
		}
	})
	if err := chromedp.Run(ctx, chromedp.Navigate(addr+"/login")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу входа: %v", err)
	}
	addVirtualAuthenticator(t, ctx)

	// ключей нет, страница открыта не с самой машины: поле кода и объяснение,
	// где его взять; формы ключа нет — без кода её не дадут
	if !pageUntil(ctx, `(() => { const б = document.getElementById('setupBox'); return !!б && getComputedStyle(б).display !== 'none'; })()`, 15*time.Second) {
		t.Fatalf("поля кода первой установки нет: %s", evaluate(t, ctx, pageState))
	}
	if объяснение := evaluate(t, ctx, `document.getElementById('login').textContent`); !strings.Contains(объяснение, "docker logs") {
		t.Errorf("страница не говорит, где взять код: %q", объяснение)
	}
	if видна := evaluate(t, ctx, `getComputedStyle(document.getElementById('reg')).display`); видна != "none" {
		t.Errorf("форма ключа видна до кода: %s", видна)
	}

	// неверный код — отказ словами, и форма ключа не открывается
	if err := chromedp.Run(ctx,
		chromedp.SendKeys("#setup", "setup-aaaa-bbbb-cccc-dddd", chromedp.ByID),
		chromedp.Click("#setupGo", chromedp.ByID)); err != nil {
		t.Fatalf("набрать код: %v", err)
	}
	if !pageUntil(ctx, `(document.getElementById('err')||{}).textContent.includes('не подходит')`, 10*time.Second) {
		t.Fatalf("неверный код не назван неверным: %s", evaluate(t, ctx, pageState))
	}

	// свой код — урезанная сессия и сразу форма первого ключа
	evaluate(t, ctx, `(document.getElementById('setup').value = '', '')`)
	if err := chromedp.Run(ctx,
		chromedp.SendKeys("#setup", strings.ToUpper(code), chromedp.ByID),
		chromedp.Click("#setupGo", chromedp.ByID)); err != nil {
		t.Fatalf("набрать свой код: %v", err)
	}
	if !pageUntil(ctx, `(document.getElementById('login')||{}).textContent.includes('Заведите первый ключ') && getComputedStyle(document.getElementById('reg')).display === 'flex' && !!pre.register`, 15*time.Second) {
		t.Fatalf("после кода нет формы первого ключа: %s", evaluate(t, ctx, pageState))
	}
	if экран := evaluate(t, ctx, `fetch('/ui?screen=inbox', {headers: {'HX-Request': 'true'}}).then(r => String(r.status))`); экран != "401" {
		t.Fatalf("сессия по коду открыла экран: %s", экран)
	}
	if err := chromedp.Run(ctx, chromedp.SendKeys("#name", "MacBook", chromedp.ByID), chromedp.Click("#mk", chromedp.ByID)); err != nil {
		t.Fatalf("нажать «Завести passkey»: %v", err)
	}
	if !pageUntil(ctx, `location.pathname === '/' && !!document.getElementById('screen')`, 20*time.Second) {
		t.Fatalf("первый ключ не заведён: %s", evaluate(t, ctx, pageState))
	}
	if got := evaluate(t, ctx, `fetch('/api/auth/state').then(r => r.json()).then(s => s.kind + '|' + s.restricted + '|' + s.passkeys + '|' + s.setup)`); got != "session|false|1|false" {
		t.Fatalf("состояние после первого ключа: %q, ждали session|false|1|false", got)
	}
	if got := evaluate(t, ctx, `fetch('/ui?screen=notifications', {headers: {'HX-Request': 'true'}}).then(r => String(r.status))`); got != "200" {
		t.Fatalf("экран сессии по ключу не открывается: %s", got)
	}
	// код погас: из старого журнала им больше не войти
	if res := s.auth.TrySetupCode("172.17.0.9", code); res.OK {
		t.Fatal("код принят после первого ключа")
	}
	if len(exceptions) > 0 {
		t.Fatalf("исключения на странице входа:\n%s", strings.Join(exceptions, "\n"))
	}
}
