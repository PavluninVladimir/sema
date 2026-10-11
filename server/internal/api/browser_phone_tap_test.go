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

// Меню на телефоне открывается НАЖАТИЕМ на кнопку «Меню» в нижней полосе.
//
// Отдельный тест рядом с TestBrowserPhoneMenuSheet нужен потому, что тот
// зовёт «sema.панель('меню', true)» из консоли и настоящую кнопку не трогает
// ни разу. 10 сентября 2026 из-за этого месяцами жила ошибка: кнопка полосы
// имеет класс «вкладка», а обработчик «закрыть по нажатию мимо шторки» делал
// исключение только для «.бургер» — тот же клик открывал шторку и тут же её
// закрывал. На телефоне были недоступны «Задачи», «Память», «Здоровье» и все
// настройки, а вызов из консоли работал безупречно.
//
// Отсюда правило: проверять то место, которым человек пользуется. Здесь это
// mouse-событие по координатам кнопки, а не вызов функции.
func TestBrowserPhoneMenuOpensOnTap(t *testing.T) {
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
		chromedp.Navigate(srv.URL+"/#screen=tasks")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === 'tasks'; })()`,
		&ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("экран не нарисовался: %v", err)
	}

	видно := `getComputedStyle(document.getElementById('menu')).display !== 'none'`
	var before bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(видно, &before)); err != nil {
		t.Fatal(err)
	}
	if before {
		t.Fatal("шторка видна до нажатия — проверять нечего")
	}

	// именно нажатие: chromedp.Click шлёт настоящие mousedown/mouseup по
	// координатам, и клик всплывает до body ровно так же, как у человека
	if err := chromedp.Run(ctx,
		chromedp.Click(`#tabbar button[aria-label="Меню"]`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond)); err != nil {
		t.Fatalf("нажать «Меню»: %v", err)
	}

	var открылось bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(видно, &открылось)); err != nil {
		t.Fatal(err)
	}
	if !открылось {
		t.Fatal("нажали «Меню», а шторки нет: на телефоне недоступны все разделы, кроме тех, что в полосе")
	}

	// и разделы в ней действительно видны, а не просто узел показан
	var пунктов int
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`Array.from(document.querySelectorAll('#menu .item')).filter((п) => п.offsetParent !== null).length`,
		&пунктов)); err != nil {
		t.Fatal(err)
	}
	if пунктов == 0 {
		t.Error("шторка открылась пустой")
	}

	// нажатие мимо шторки её по-прежнему закрывает: исключение сделано
	// для кнопки, а не для всех нажатий вообще
	var закрылось bool
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('screen').click()`, nil),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(видно, &закрылось)); err != nil {
		t.Fatal(err)
	}
	if закрылось {
		t.Error("нажатие мимо шторки должно её закрывать")
	}
}
