package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Заставка вместо белого листа: пока первый экран не приехал, видна она —
// в цвете фона Сёмы, со своей подписью «не отвечает», — а с экраном
// уходит со страницы совсем. 4 октября 2026 владелец жаловался, что при
// запуске и после выгрузки приложение долго висит белым.
func TestBrowserSplashUntilFirstScreen(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, _ := testServer(t)
	var down atomic.Bool
	down.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui" && down.Load() {
			http.Error(w, "Сёма поднимается", http.StatusBadGateway)
			return
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx := splashBrowser(t)

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen=notifications")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	// экрана нет — заставка на месте и честно говорит, что Сёма не отвечает
	var told bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const b = document.getElementById('splash'); return !!b && !b.classList.contains('gone') && b.textContent.includes('не отвечает — пробую снова'); })()`,
		&told, chromedp.WithPollingTimeout(30*time.Second))); err != nil || !told {
		t.Fatalf("заставка не сказала, что Сёма не отвечает: %v", err)
	}
	// и она не белая: фон заставки — фон Сёмы (светлая тема браузера)
	var bg string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`getComputedStyle(document.getElementById('splash')).backgroundColor`, &bg)); err != nil {
		t.Fatal(err)
	}
	if bg != "rgb(251, 250, 247)" {
		t.Errorf("фон заставки %s, ждали цвет --bg Сёмы rgb(251, 250, 247)", bg)
	}

	// Сёма поднялся — экран приехал, заставки на странице больше нет
	down.Store(false)
	var gone bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => !!document.getElementById('screen') && !document.getElementById('splash'))()`,
		&gone, chromedp.WithPollingTimeout(60*time.Second))); err != nil || !gone {
		t.Fatalf("экран приехал, а заставка осталась: %v", err)
	}
}

// Оболочка при повторном запуске берётся из кэша работника, а не из сети:
// сеть до Сёмы бывает медленной (Wi-Fi до Pi, шлюз через VPS), и каждый
// запуск ждал её с белым экраном. Сервер здесь отдаёт оболочку с задержкой
// в 20 секунд и считает обращения: после перезагрузки экран обязан
// приехать быстрее, а к файлам оболочки — ни одного запроса.
func TestBrowserShellFromCacheFirst(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, _ := testServer(t)
	shell := map[string]bool{"/": true, "/app.js": true, "/style.css": true, "/fonts.css": true, "/htmx.js": true}
	var slow atomic.Bool
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() && shell[r.URL.Path] {
			hits.Add(1)
			time.Sleep(20 * time.Second)
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx := splashBrowser(t)

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen=notifications")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	// работник встал и взял страницу: его кэш оболочки наполнен
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => !!document.getElementById('screen') && !!navigator.serviceWorker.controller)()`,
		&ready, chromedp.WithPollingTimeout(30*time.Second))); err != nil || !ready {
		t.Fatalf("работник не взял страницу: %v", err)
	}
	var cached bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(async () => { for (const n of await caches.keys()) { if (!n.startsWith('сёма-оболочка')) continue; for (const r of await (await caches.open(n)).keys()) { if (new URL(r.url).pathname === '/app.js') return true; } } return false; })()`,
		&cached, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil || !cached {
		t.Fatalf("клиента нет в кэше оболочки: %v", err)
	}

	slow.Store(true)
	started := time.Now()
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatalf("перезагрузка: %v", err)
	}
	var back bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === 'notifications'; })()`,
		&back, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !back {
		t.Fatalf("после перезагрузки экран не приехал за 15 с — оболочку ждали из сети: %v (запросов к оболочке %d)", err, hits.Load())
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("повторный запуск занял %s", took)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("повторный запуск сходил за оболочкой в сеть %d раз, ждали ни одного", n)
	}
}

func splashBrowser(t *testing.T) context.Context {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelTime := context.WithTimeout(ctx, 150*time.Second)
	t.Cleanup(cancelTime)
	// тема браузера — светлая нарочно: иначе тест следует теме мака и
	// вечером ждёт не тот фон заставки
	if err := chromedp.Run(ctx, emulation.SetEmulatedMedia().WithFeatures(
		[]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "light"}})); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("светлая тема: %v", err)
	}
	return ctx
}
