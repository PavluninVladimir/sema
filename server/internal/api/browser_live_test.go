package api

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

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Живое обновление в настоящем Chrome: опрос выключен (раз в десять
// минут), изменение на сервере доезжает до открытого экрана по проводу.
func TestBrowserLiveUpdate(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	// опрос отключаем: если экран обновится, то только по проводу
	if err := st.SetSetting("ui.poll_s", "600"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("ui.poll_live_s", "600"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen=notifications")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === 'notifications'; })()`,
		&ready, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !ready {
		t.Fatalf("экран уведомлений не нарисовался: %v", err)
	}
	// провод открывается после первого экрана; даём ему секунду
	time.Sleep(time.Second)

	title := "Живой сигнал " + time.Now().Format("15:04:05.000")
	if _, err := s.eng.Notify(&store.Notification{Key: "проба-провода", Source: "core", Level: "info", Title: title}); err != nil {
		t.Fatal(err)
	}
	var видно bool
	err := chromedp.Run(ctx, chromedp.Poll(
		`document.getElementById('screen').textContent.includes(`+jsString(title)+`)`,
		&видно, chromedp.WithPollingTimeout(5*time.Second)))
	if err != nil || !видно {
		t.Fatalf("уведомление не появилось на экране без опроса: %v", err)
	}
}

// При живом проводе опрос становится редким: на экране чата за пять секунд
// с опросом раз в секунду без провода было бы пять запросов /ui — с
// проводом их не должно быть вовсе. Проверяется в настоящем Chrome, потому
// что переключение триггера у htmx 4 иначе не проверить.
func TestBrowserPollRareWhenWired(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	for k, v := range map[string]string{"ui.poll_s": "1", "ui.poll_live_s": "600"} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	chat, err := st.CreateChat("проба провода")
	if err != nil {
		t.Fatal(err)
	}
	var ui atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui" {
			ui.Add(1)
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()
	screen := "chat:" + strconv.FormatInt(chat, 10)
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	var открыт bool
	if err := chromedp.Run(ctx, chromedp.Poll(`(() => { const п = window.sema && sema.wire && sema.wire(); return !!п && п.open; })()`,
		&открыт, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !открыт {
		t.Fatalf("провод не открылся: %v", err)
	}
	var trigger string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('screen').getAttribute('hx-trigger')`, &trigger)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trigger, "every 600s") {
		t.Fatalf("опрос не стал редким: %q", trigger)
	}
	before := ui.Load()
	time.Sleep(5 * time.Second)
	if n := ui.Load() - before; n > 1 {
		t.Fatalf("при живом проводе за пять секунд %d запросов /ui — опрос не переключился", n)
	}
}
