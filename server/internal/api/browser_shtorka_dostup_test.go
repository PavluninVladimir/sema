package api

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Выбранная вкладка должна быть видна в полосе, а не за её краем. На
// телефоне «Спам» — последняя из шести — целиком оставалась справа за
// экраном: человек открывал раздел, экран менялся, а в полосе выбранного
// пункта не было видно вовсе. (Имя файла — от шторки разделов, которой с
// 1 октября 2026 нет.)
func TestBrowserSelectedTabVisible(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(390, 844),
		chromedp.Navigate(srv.URL+"/#screen=settings:mail~spam")); err != nil {
		t.Fatal(err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!!document.querySelector('.subtabs-bar .segment.selected')`, &ready,
		chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("полоса внутренних вкладок не нарисовалась: %v", err)
	}
	var видна bool
	if err := chromedp.Run(ctx, chromedp.Poll(`(() => {
		const ряд = document.querySelector('.subtabs-bar .segments');
		const в = ряд && ряд.querySelector('.segment.selected');
		if (!в) return false;
		const р = ряд.getBoundingClientRect(), э = в.getBoundingClientRect();
		return э.left >= р.left - 1 && э.right <= р.right + 1;
	})()`, &видна, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !видна {
		t.Error("выбранная вкладка за краем полосы — человек не видит, где стоит")
	}
}
