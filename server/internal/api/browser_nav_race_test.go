package api

import (
	"context"
	"fmt"
	"github.com/chromedp/cdproto/input"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Два перехода подряд: первый медленный, второй быстрый — на экране второй,
// и плашки «Связи нет» нет. 4 октября 2026 опоздавший ответ (ранние сообщения
// чата, запрошенные прокруткой вверх) возвращал чат поверх «Файлов»; теперь
// новый запрос в окно уводит ответ прежнего в никуда. Ожидание в 1,2 с ниже —
// проверка того, что чего-то НЕ случилось: условия «ответ пришёл и ничего не
// подменил» на странице нет, есть только срок, к которому он бы пришёл.
func TestBrowserLateResponseDoesNotWin(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, _ := testServer(t)
	h := s.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui" && r.URL.Query().Get("screen") == "files" {
			time.Sleep(700 * time.Millisecond) // «Файлы» отвечают медленно
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1100, 820), chromedp.Navigate(srv.URL+"/#screen=notifications")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	waitScreen(t, ctx, "notifications")
	// медленный переход и сразу следом быстрый — так, как это делает
	// нетерпеливый человек
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const btn = (name) => Array.from(document.querySelectorAll('#menu button.item')).find((b) => b.textContent.trim().startsWith(name));
		btn('Файлы').click();
		setTimeout(() => btn('Задачи').click(), 50);
		return true;
	})()`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "tasks")
	// ответ «Файлов» пришёл бы через 700 мс — ждём дольше и смотрим, что он
	// ничего не подменил и связь не «оборвалась»
	var screen string
	var banner bool
	if err := chromedp.Run(ctx, chromedp.Sleep(1200*time.Millisecond), chromedp.Evaluate(`(() => {
		return document.getElementById('screen').dataset.screen;
	})()`, &screen), chromedp.Evaluate(`(() => { const b = document.getElementById('link-banner'); return !!b && !b.hidden; })()`, &banner)); err != nil {
		t.Fatal(err)
	}
	if screen != "tasks" {
		t.Errorf("на экране %q: опоздавший ответ подменил последний переход", screen)
	}
	if banner {
		t.Error("оборванный переход показал плашку о связи")
	}
}

// Первый экран просится сразу из index.html, и app.js забирает этот ответ:
// запрос экрана при загрузке страницы один, а не два (perf-pass 6.4).
func TestBrowserFirstScreenRequestedOnce(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, _ := testServer(t)
	h := s.Handler()
	var mu sync.Mutex
	asked := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui" && r.URL.Query().Get("part") == "" {
			mu.Lock()
			asked[r.URL.Query().Get("screen")]++
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1100, 820), chromedp.Navigate(srv.URL+"/#screen=files")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	waitScreen(t, ctx, "files")
	var used bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.semaFirstScreen === null`, &used)); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("ответ, запрошенный из index.html, не забран клиентом")
	}
	mu.Lock()
	n := asked["files"]
	mu.Unlock()
	if n != 1 {
		t.Errorf("экран «Файлы» при загрузке запрошен %d раз, ждали один", n)
	}
}

// Наведение на пункт меню подгружает экран заранее, и нажатие нового запроса
// не делает; наведение на чат запроса не делает вовсе — открытие чата ставит
// «прочитано» (perf-pass 7.3).
func TestBrowserPrefetchOnHover(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, st := testServer(t)
	chat, _ := st.CreateChat("Про отпуск")
	_ = st.AddChatMsg(chat, "user", "привет")
	h := s.Handler()
	var mu sync.Mutex
	asked := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui" && r.URL.Query().Get("part") == "" {
			mu.Lock()
			asked[r.URL.Query().Get("screen")]++
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	count := func(screen string) int { mu.Lock(); defer mu.Unlock(); return asked[screen] }
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1100, 820), chromedp.Navigate(srv.URL+"/#screen=files")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	waitScreen(t, ctx, "files")
	hoverOver := func(xpath string) {
		t.Helper()
		var box []float64
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const el = document.evaluate(`+jsString(xpath)+`, document, null, 9, null).singleNodeValue; if (!el) return []; el.scrollIntoView({block: 'center'}); const r = el.getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })()`, &box)); err != nil || len(box) != 2 {
			t.Fatalf("нет элемента %s: %v", xpath, err)
		}
		if err := chromedp.Run(ctx, chromedp.MouseEvent(input.MouseMoved, box[0], box[1])); err != nil {
			t.Fatal(err)
		}
	}
	tasks := `//aside[@id='menu']//button[contains(@class,'item') and contains(normalize-space(.),'Задачи')]`
	hoverOver(tasks)
	var warmed bool
	if err := chromedp.Run(ctx, chromedp.Poll(`sema.prefetched().some((u) => u.includes('screen=tasks'))`, &warmed,
		chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("наведение на «Задачи» не подгрузило экран: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(tasks, chromedp.BySearch)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "tasks")
	if n := count("tasks"); n != 1 {
		t.Errorf("экран «Задачи» запрошен %d раз: нажатие не взяло подгруженное", n)
	}
	// чат — нет: открытие ставит «прочитано»
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const у = document.getElementById('menu-ask'); if (у) у.open = true; return true })()`, nil)); err != nil {
		t.Fatal(err)
	}
	hoverOver(`//aside[@id='menu']//button[contains(@class,'item') and contains(normalize-space(.),'Про отпуск')]`)
	var none bool
	_ = chromedp.Run(ctx, chromedp.Sleep(400*time.Millisecond), chromedp.Evaluate(`!sema.prefetched().some((u) => u.includes('chat'))`, &none))
	if !none || count(fmt.Sprintf("chat:%d", chat)) != 0 {
		t.Error("наведение на чат подгрузило его — открытие поставило бы «прочитано»")
	}
}
