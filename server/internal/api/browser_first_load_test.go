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

	"github.com/chromedp/chromedp"
)

// Сёма поднимается — оболочка ждёт и дозванивается сама. Приложение на
// телефоне просыпается из фона, оболочку отдаёт служебный работник из
// кэша, а сервер в этот момент перезапускается после выката: первый
// запрос экрана не доходит. Раньше он был ровно один, и приложение
// навсегда оставалось с надписью «Подключаюсь к Сёме…» — его выгружали
// из памяти руками, хотя тут же на экране обещали, что оно попробует
// снова само.
func TestBrowserReconnectsAfterDeploy(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := testServer(t)
	var лежит atomic.Bool
	лежит.Store(true)
	var попыток atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui" {
			попыток.Add(1)
			if лежит.Load() {
				// так отвечает прокси, пока контейнер поднимается
				http.Error(w, "Сёма поднимается", http.StatusBadGateway)
				return
			}
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 180*time.Second)
	defer cancelTime()

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen=notifications")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	// пока сервера нет — оболочка не сдаётся и стучится снова. Ждём это
	// по счётчику запросов на сервере, а не по надписи в окне: надпись
	// живёт между попытками, и проба может заглянуть ровно в тот миг,
	// когда очередной запрос уже ушёл
	деньги := time.Now()
	for попыток.Load() < 2 {
		if time.Since(деньги) > 45*time.Second {
			t.Fatalf("оболочка сдалась после первой попытки: запросов %d", попыток.Load())
		}
		time.Sleep(200 * time.Millisecond)
	}
	// и говорит об этом человеку, а не показывает «502 Bad Gateway»
	var сказал bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const о = document.getElementById('window'); return !!о && о.textContent.includes('пробую снова'); })()`,
		&сказал, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !сказал {
		var before string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('window')||{}).textContent || 'окна нет'`, &before))
		t.Fatalf("оболочка должна честно сказать, что пробует снова: %v\nв окне: %s", err, clipStr(before, 200))
	}

	// Сёма поднялась — экран приезжает сам, без перезагрузки страницы
	лежит.Store(false)
	var приехал bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === 'notifications'; })()`,
		&приехал, chromedp.WithPollingTimeout(60*time.Second))); err != nil || !приехал {
		t.Fatalf("после подъёма экран не приехал сам: %v", err)
	}

	// и на этом стучаться перестаём: экран есть, спрашивать больше нечего
	before := попыток.Load()
	if err := chromedp.Run(ctx, chromedp.Sleep(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if after := попыток.Load(); after > before {
		t.Errorf("экран приехал, а дозвон продолжается: %d лишних запросов", after-before)
	}
}
