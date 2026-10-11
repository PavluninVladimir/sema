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

// Внутренняя вкладка раздела переключается НАЖАТИЕМ, а не вызовом из
// консоли. Урок 10 сентября 2026 (кнопка меню на телефоне, которую
// собственный обработчик закрывал тем же кликом) стоил месяцев: сборка
// разметки может быть безупречной, а живой клик — не работать. Здесь
// проверяется то место, которым пользуется человек.
func TestBrowserSubtabSwitchesOnTap(t *testing.T) {
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
		chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate(srv.URL+"/#screen=settings:models")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === 'settings:models'; })()`,
		&ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("экран не нарисовался: %v", err)
	}
	// полоса внутренних вкладок есть и она не та же, что пять постоянных
	var вкладок int
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.querySelectorAll('.subtabs-bar .segment').length`, &вкладок)); err != nil {
		t.Fatal(err)
	}
	if вкладок != 4 {
		t.Fatalf("внутренних вкладок %d, ждали четыре", вкладок)
	}
	// настоящий клик по «Проблемам»
	if err := chromedp.Run(ctx, chromedp.Click(
		`.subtabs-bar .segment:nth-child(4)`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать вкладку: %v", err)
	}
	var открылось bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`document.getElementById('screen').dataset.screen.endsWith('~issues')`, &открылось,
		chromedp.WithPollingTimeout(20*time.Second))); err != nil || !открылось {
		var after string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('screen').dataset.screen`, &after))
		t.Fatalf("после нажатия открыт %q, а не «Проблемы» (%v)", after, err)
	}
	// выбранная вкладка помечена для голосового доступа
	var выбрана string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(document.querySelector('.subtabs-bar .segment[aria-selected="true"]')||{}).textContent||''`, &выбрана)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(выбрана, "Проблемы") {
		t.Errorf("выбранной помечена %q", выбрана)
	}
}
