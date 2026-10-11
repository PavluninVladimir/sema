package api

// Переключение учётки Telegram в настоящем Chrome: нажатием, тем местом,
// которым пользуется человек. Закреплённая полоса сверху, список в ней —
// и после выбора на экране собеседники другой учётки, а не прежней.

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestBrowserTelegramAccountSwitch(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := telegramWithTwoAccounts(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 120*time.Second)
	defer cancelTime()

	// первая вкладка адресуется без хвоста: «~» без имени вкладки — это не адрес
	screen := tgScreen + "@" + tgDigits
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate(srv.URL+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, screen)

	// полоса с выбором учётки — на месте и подписана нынешней
	const списокВПолосе = `document.querySelector('.section.pinned-bar select')`
	var label string
	if err := chromedp.Run(ctx, chromedp.Evaluate(списокВПолосе+`.value`, &label)); err != nil {
		t.Fatalf("списка учёток в полосе нет: %v", err)
	}
	if label != "личная" {
		t.Fatalf("полоса показывает не ту учётку: %q", label)
	}

	// собеседники — матрица прав прямо на экране: чат личной учётки уже в ней
	const раздел = `table.matrix.rights`
	ждатьТекст := func(что string, зачем string) {
		t.Helper()
		var ок bool
		if err := chromedp.Run(ctx, chromedp.Poll(
			`document.querySelector('`+раздел+`').textContent.includes(`+"`"+что+"`"+`)`, &ок,
			chromedp.WithPollingTimeout(15*time.Second))); err != nil || !ок {
			var text string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('`+раздел+`').textContent`, &text))
			t.Fatalf("%s: «%s» не дождались, на экране:\n%s", зачем, что, text)
		}
	}
	ждатьТекст("Митя", "собеседник личной учётки")

	// переключаем учётку тем же событием, каким это делает человек
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		списокВПолосе+`.value = 'рабочая'; `+
			списокВПолосе+`.dispatchEvent(new Event('change', {bubbles: true})); true`, nil)); err != nil {
		t.Fatalf("выбрать другую учётку: %v", err)
	}
	waitScreen(t, ctx, tgScreen+"@"+secondDigits)
	ждатьТекст("Отдел кадров", "собеседник рабочей учётки")
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.querySelector('`+раздел+`').textContent`, &text)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Митя") {
		t.Fatalf("после переключения остались чаты прежней учётки:\n%s", text)
	}
}
