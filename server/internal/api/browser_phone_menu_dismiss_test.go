package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Шторка меню на телефоне уходит двумя жестами: касанием мимо неё и
// повторным касанием «Меню» в нижней полосе (4 октября 2026, просьба
// владельца: открытое меню на iPhone не закрывалось ничем).
//
// Safari на iPhone не шлёт click по касанию в «некликабельное» место:
// затемнение было ::before у body, касание попадало в сам body, и слушатель
// на body его не получал — в Chrome это не видно, клик там приходит всегда.
// Поэтому тест проверяет не только «закрылось», но и ЧТО лежит под пальцем:
// на затемнении — свой узел со своим слушателем, на «Меню» — сама кнопка.
func TestBrowserPhoneMenuDismissByTap(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
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
		`(() => { const s = document.getElementById('screen'); return !!s && s.dataset.screen === 'tasks'; })()`,
		&ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("экран не нарисовался: %v", err)
	}
	const open = `document.getElementById('menu').classList.contains('open-sheet')`
	// середина кнопки «Меню» в полосе — туда и касаются второй раз
	var btn struct{ X, Y float64 }
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const r = document.querySelector('#tabbar button[aria-label="Меню"]').getBoundingClientRect(); return {X: r.x + r.width/2, Y: r.y + r.height/2}; })()`,
		&btn)); err != nil {
		t.Fatal(err)
	}
	tapOpen := func() {
		t.Helper()
		var on bool
		if err := chromedp.Run(ctx,
			chromedp.MouseClickXY(btn.X, btn.Y),
			chromedp.Poll(open, &on, chromedp.WithPollingTimeout(5*time.Second))); err != nil || !on {
			t.Fatalf("касание «Меню» шторку не открыло: %v", err)
		}
	}
	waitClosed := func(what string) {
		t.Helper()
		var off bool
		if err := chromedp.Run(ctx, chromedp.Poll(`!(`+open+`)`, &off,
			chromedp.WithPollingTimeout(5*time.Second))); err != nil || !off {
			t.Fatalf("%s — шторка осталась открытой: %v", what, err)
		}
	}

	// 1. повторное касание «Меню»: под пальцем сама кнопка, а не затемнение
	tapOpen()
	var under string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		fmt.Sprintf(`(() => { const el = document.elementFromPoint(%f, %f); const b = el && el.closest('[data-panel="menu"]'); return b ? 'кнопка' : (el ? el.tagName + '#' + el.id : 'ничего'); })()`, btn.X, btn.Y),
		&under)); err != nil {
		t.Fatal(err)
	}
	if under != "кнопка" {
		t.Fatalf("при открытом меню под значком «Меню» лежит %s, а не кнопка", under)
	}
	if err := chromedp.Run(ctx, chromedp.MouseClickXY(btn.X, btn.Y)); err != nil {
		t.Fatal(err)
	}
	waitClosed("повторное касание «Меню»")

	// 2. касание мимо шторки, над ней — по затемнению
	tapOpen()
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const el = document.elementFromPoint(195, 40); return el ? el.tagName + '#' + el.id : 'ничего'; })()`,
		&under)); err != nil {
		t.Fatal(err)
	}
	if under != "DIV#dim-overlay" {
		t.Fatalf("над шторкой под пальцем %s: Safari на iPhone не шлёт click в body, касание мимо меню его не закроет", under)
	}
	if err := chromedp.Run(ctx, chromedp.MouseClickXY(195, 40)); err != nil {
		t.Fatal(err)
	}
	waitClosed("касание мимо шторки")
	var dimGone bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const d = document.getElementById('dim-overlay'); return !d || getComputedStyle(d).display === 'none'; })()`,
		&dimGone)); err != nil {
		t.Fatal(err)
	}
	if !dimGone {
		t.Error("шторка закрылась, а затемнение осталось и съедает касания экрана")
	}
}

// Плашка о связи на телефоне встаёт над нижней полосой, а не на неё.
// 3 октября 2026 владелец прислал снимок: без связи «Связи нет, а снимка
// этого экрана на устройстве нет…» легла поверх «Меню» и всех вкладок, и
// уйти с экрана было нечем. Мерим по-честному: под серединой каждой
// вкладки — сама вкладка, а низ плашки выше верха полосы.
func TestBrowserPhoneBannerAboveTabbar(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 90*time.Second)

	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!!document.getElementById('screen') && document.querySelectorAll('#tabbar .tab').length >= 2`,
		&ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("экран с полосой не нарисовался: %v", err)
	}
	// тот самый текст: длинный, в несколько строк — плашка растёт вверх
	var res struct {
		Gap     float64  `json:"gap"`
		Covered []string `json:"covered"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
	  sema.banner('Связи нет, а снимка этого экрана на устройстве нет: он либо не сохраняется никогда (секреты, «Что ушло наружу», «Вход и доступ», закрытые ящики), либо вы его ещё не открывали при связи.');
	  const b = document.getElementById('link-banner').getBoundingClientRect();
	  const bar = document.getElementById('tabbar').getBoundingClientRect();
	  const covered = [];
	  for (const tab of document.querySelectorAll('#tabbar .tab')) {
	    const r = tab.getBoundingClientRect();
	    const el = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
	    if (!el || !tab.contains(el)) covered.push(tab.textContent.trim());
	  }
	  return {gap: bar.top - b.bottom, covered};
	})()`, &res)); err != nil {
		t.Fatal(err)
	}
	if len(res.Covered) > 0 {
		t.Errorf("плашка закрывает вкладки: %v", res.Covered)
	}
	if res.Gap < 0 {
		t.Errorf("низ плашки ниже верха полосы на %.0f точек", -res.Gap)
	}
}

// Листать пункты меню на телефоне — не закрывать его. 4 октября 2026
// владелец прокрутил шторку вниз, потянул список обратно наверх — и меню
// закрылось под пальцем: свайп вниз закрывал шторку при любой прокрутке.
// Теперь закрывает только из верха списка или за ручку.
func TestBrowserPhoneMenuScrollDoesNotClose(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 90*time.Second)
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!!document.getElementById('screen') && document.querySelectorAll('#tabbar .tab').length >= 2`,
		&ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("экран не нарисовался: %v", err)
	}
	const open = `document.getElementById('menu').classList.contains('open-sheet')`
	var scrolled float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
	  sema.panel('menu', true);
	  const m = document.getElementById('menu');
	  m.scrollTop = 160;
	  return m.scrollTop;
	})()`, &scrolled)); err != nil {
		t.Fatal(err)
	}
	if scrolled <= 0 {
		t.Fatal("меню целиком помещается в шторку — листать нечего, сторож ничего не проверяет")
	}
	// палец посреди списка тянет вниз: это прокрутка к началу, не закрытие
	pullDown(t, ctx, 195, 520, 220)
	var still bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(open, &still)); err != nil {
		t.Fatal(err)
	}
	if !still {
		t.Fatal("потянули прокрученный список меню вниз — шторка закрылась, а человек просто листал")
	}
	// за закреплённый верх шторка по-прежнему уходит вниз (при прокрутке
	// ручка, шапка и поиск стоят в одной точке — под пальцем любой из них)
	var grab struct{ X, Y float64 }
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
	  const r = document.querySelector('#menu .grabber').getBoundingClientRect();
	  return {X: r.x + r.width / 2, Y: r.y + r.height / 2};
	})()`, &grab)); err != nil {
		t.Fatal(err)
	}
	pullDown(t, ctx, grab.X, grab.Y, 220)
	var closed bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!(`+open+`)`, &closed,
		chromedp.WithPollingTimeout(5*time.Second))); err != nil || !closed {
		t.Fatalf("свайп вниз за ручку шторку не закрыл: %v", err)
	}
}
