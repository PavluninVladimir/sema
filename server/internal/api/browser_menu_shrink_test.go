package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Меню длиннее окна прокручивается, а пункты сохраняют свою высоту.
// 17 сентября 2026 в чатах стало на пару пунктов больше, и «Задачи»,
// «Скрипты», «Модули» сплющились до 18 px и налезли друг на друга.
func TestBrowserLongMenuDoesNotShrinkItems(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	for i := 0; i < 14; i++ {
		id, _ := st.CreateChat("Разговор " + strconv.Itoa(i))
		_ = st.AddChatMsg(id, "user", "привет")
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

	var низкие []string
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1290, 600),
		chromedp.Navigate(srv.URL+"/#screen=tasks"),
		chromedp.WaitVisible(`#menu .item`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Evaluate(`[...document.querySelectorAll('#menu .item')]
			.filter(п => п.offsetParent !== null && п.getBoundingClientRect().height < 28)
			.map(п => п.textContent.trim() + ' ' + Math.round(п.getBoundingClientRect().height))`, &низкие),
	); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	if len(низкие) > 0 {
		t.Fatalf("пункты меню сжаты: %v", низкие)
	}
}
