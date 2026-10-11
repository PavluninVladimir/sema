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

// Шапка на телефоне не съедает пол-экрана. Разбор 10 сентября 2026: на
// 390 px до первого письма было 195 точек — заголовок кеглем в 28,
// подпись на три строки, поиск во всю ширину отдельным рядом и пустая
// полоса, на которой стояло одно многоточие.
//
// Порог с запасом: тест сторожит порядок величины, а не пиксель.
func TestBrowserPhoneHeaderDoesNotEatScreen(t *testing.T) {
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
		chromedp.Navigate(srv.URL+"/#screen=inbox")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.getElementById('screen'); return !!э && !!document.querySelector('.feed'); })()`,
		&ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("экран не нарисовался: %v", err)
	}

	var верхЛенты float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.querySelector('.feed').getBoundingClientRect().top`, &верхЛенты)); err != nil {
		t.Fatal(err)
	}
	if верхЛенты > 150 {
		t.Errorf("шапка занимает %.0f точек из 844 — было 195, стало не лучше", верхЛенты)
	}
	t.Logf("до содержимого %.0f точек", верхЛенты)

	// ряд управления не бывает пустой полосой: если он есть, в нём
	// что-то стоит. Пустой съедал 38 точек ради одного многоточия
	var пустых int
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`Array.from(document.querySelectorAll('.header .controls'))
		   .filter(у => у.offsetParent !== null && у.getBoundingClientRect().height > 0 && у.children.length === 0).length`,
		&пустых)); err != nil {
		t.Fatal(err)
	}
	if пустых > 0 {
		t.Errorf("пустых полос управления: %d", пустых)
	}

	// ничего не выпирает вбок
	var вбок float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.documentElement.scrollWidth - document.documentElement.clientWidth`, &вбок)); err != nil {
		t.Fatal(err)
	}
	if вбок > 1 {
		t.Errorf("страница едет вбок на %.0f точек", вбок)
	}
}

// Счётчик в нижней полосе не лежит на значке: значок 22 точки, его
// правый край — «50 % + 11px», и счётчик должен начинаться за ним.
// «123» ложилось прямо на рисунок.
func TestBrowserTabCounterNotOnIcon(t *testing.T) {
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
		chromedp.EmulateViewport(390, 844),
		chromedp.Navigate(srv.URL+"/#screen=inbox")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!!document.getElementById('tabbar')`,
		&ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("полосы нет: %v", err)
	}
	// подставляем трёхзначный счётчик: с ним беда и была
	var наложение float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const в = document.querySelector('#tabbar .tab:nth-child(2)');
		if (!в) return -1000;
		let с = в.querySelector('.tab-count');
		// как в разметке: счётчик лежит в обёртке значка
		if (!с) { с = document.createElement('span'); с.className = 'tab-count'; (в.querySelector('.tab-glyph') || в).appendChild(с); }
		с.textContent = '123';
		const значок = в.querySelector('.tab-icon') || в.querySelector('svg');
		if (!значок) return -1000;
		return значок.getBoundingClientRect().right - с.getBoundingClientRect().left;
	})()`, &наложение)); err != nil {
		t.Fatal(err)
	}
	// -1000 — «нечего мерить»; раньше стоял -1, и он совпадал с честным
	// результатом «счётчик начинается на точку правее значка»: тест
	// молча пропускался ровно тогда, когда всё было как надо
	if наложение == -1000 {
		t.Skip("во вкладке нет значка — нечего мерить")
	}
	if наложение > 0.5 {
		t.Errorf("счётчик заезжает на значок на %.1f точки", наложение)
	}
}
