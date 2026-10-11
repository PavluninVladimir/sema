package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Меню «···» закрывается кликом мимо, а не вторым нажатием на точки:
// иначе висящее меню съедает следующий клик человека (13 сентября 2026).
func TestBrowserRowMenuClosesOnOutsideClick(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := telegramWithChannels(t, 2)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1200, 800),
		chromedp.Navigate(srv.URL+"/#screen="+tgScreen+"~conn")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, tgScreen+"~conn")
	var открыто bool
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const с = document.querySelector('#window details.row-menu > summary'); с.click();
			return !!document.querySelector('details.row-menu[open]'); })()`, &открыто)); err != nil {
		t.Fatal(err)
	}
	if !открыто {
		t.Fatal("меню «···» не открылось")
	}
	// клик мимо — по заголовку экрана
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const h = document.querySelector('#window h1'); const r = h.getBoundingClientRect();
		h.dispatchEvent(new PointerEvent('pointerdown', {bubbles: true, clientX: r.left + 5, clientY: r.top + 5}));
		return true; })()`, nil)); err != nil {
		t.Fatal(err)
	}
	var ещёОткрыто bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.querySelector('details.row-menu[open]')`, &ещёОткрыто)); err != nil {
		t.Fatal(err)
	}
	if ещёОткрыто {
		t.Error("меню «···» не закрылось от клика мимо — следующий клик человека уйдёт впустую")
	}
	// и открывается снова: закрытие «всех, кроме своего» на pointerdown
	// однажды гасило только что открытое меню
	if err := chromedp.Run(ctx, chromedp.Click(`#window details.row-menu > summary`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var снова bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.querySelector('details.row-menu[open]')`, &снова)); err != nil {
		t.Fatal(err)
	}
	if !снова {
		t.Error("после закрытия меню «···» больше не раскрывается")
	}
	// второе нажатие по точкам закрывает, как и положено details
	if err := chromedp.Run(ctx, chromedp.Click(`#window details.row-menu > summary`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var закрылось bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!document.querySelector('details.row-menu[open]')`, &закрылось)); err != nil {
		t.Fatal(err)
	}
	if !закрылось {
		t.Error("второе нажатие по точкам не закрыло меню")
	}
}
