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

// Меню на телефоне: шторка снизу, поиск по разделам на месте, полоса
// вкладок видна всегда. Проверяется в настоящем Chrome на ширине
// телефона — правила экрана иначе не проверить, они живут в @media.
func TestBrowserPhoneMenuSheet(t *testing.T) {
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

	// полоса видна на телефоне и стоит внизу экрана
	var низПолосы, высота float64
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('tabbar').getBoundingClientRect().bottom`, &низПолосы),
		chromedp.Evaluate(`window.innerHeight`, &высота)); err != nil {
		t.Fatalf("полосы нет: %v", err)
	}
	if низПолосы < высота-2 || низПолосы > высота+2 {
		t.Errorf("полоса не у нижнего края: низ %.0f при высоте окна %.0f", низПолосы, высота)
	}

	// меню закрыто, пока не нажали
	var видно bool
	_ = chromedp.Run(ctx, chromedp.Evaluate(`getComputedStyle(document.getElementById('menu')).display !== 'none'`, &видно))
	if видно {
		t.Error("шторка видна до нажатия")
	}

	// открываем: приезжает снизу и не занимает экран целиком
	var верх, ширина float64
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`sema.panel('menu', true)`, nil),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`document.getElementById('menu').getBoundingClientRect().top`, &верх),
		chromedp.Evaluate(`document.getElementById('menu').getBoundingClientRect().width`, &ширина)); err != nil {
		t.Fatal(err)
	}
	if верх < высота*0.1 {
		t.Errorf("шторка занимает почти весь экран: верх на %.0f из %.0f", верх, высота)
	}
	if ширина < 380 {
		t.Errorf("шторка должна быть во всю ширину, а она %.0f", ширина)
	}

	// поиск по разделам: набрали — лишнее спряталось
	var total, послеПоиска int
	видимых := `Array.from(document.querySelectorAll('#menu .item')).filter((п) => п.offsetParent !== null).length`
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(видимых, &total),
		chromedp.Evaluate(`sema.searchMenu('память')`, nil),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(видимых, &послеПоиска)); err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Fatal("в шторке не видно ни одного пункта")
	}
	if послеПоиска == 0 || послеПоиска >= total {
		t.Errorf("поиск не отсеял: было %d, стало %d", total, послеПоиска)
	}

	// поиск по слову, которого нет в названии: раздел находится по тому,
	// как его обычно называют
	var поСловам int
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`sema.searchMenu('пуши')`, nil),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(видимых, &поСловам)); err != nil {
		t.Fatal(err)
	}
	if поСловам == 0 {
		t.Error("«пуши» должны находить «Уведомления»: раздел ищется и по словам, которыми его называют")
	}

	// стёрли — вернулось всё
	var вернулось int
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`sema.searchMenu('')`, nil),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(видимых, &вернулось)); err != nil {
		t.Fatal(err)
	}
	if вернулось != total {
		t.Errorf("после стирания видно %d из %d", вернулось, total)
	}
}

// На широком экране ни полосы, ни шторки: там рельс меню слева и прятать
// нечего.
func TestBrowserWideScreenWithoutBar(t *testing.T) {
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
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(srv.URL+"/#screen=tasks")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!!document.getElementById('screen')`, &ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("экран не нарисовался: %v", err)
	}
	var полоса, search, меню bool
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`getComputedStyle(document.getElementById('tabbar')).display !== 'none'`, &полоса),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('#menu .menu-search')).display !== 'none'`, &search),
		chromedp.Evaluate(`getComputedStyle(document.getElementById('menu')).display !== 'none'`, &меню)); err != nil {
		t.Fatal(err)
	}
	if полоса {
		t.Error("на широком экране полоса вкладок лишняя")
	}
	if search {
		t.Error("на широком экране поиск по меню лишний: разделы видны целиком")
	}
	if !меню {
		t.Error("рельс меню должен стоять слева всегда")
	}
}
