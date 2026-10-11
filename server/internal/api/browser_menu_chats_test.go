package api

import (
	"context"
	"math"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Меню не прыгает при переходе между разговорами. Запись экрана
// 16 сентября 2026: открыл разговор из другой группы — узел «Чаты» или
// прежняя группа сворачивались, и всё меню ниже уезжало на полэкрана.
// Теперь группы раскрыты всегда, и «Почта» стоит на месте. Свёрнутую рукой
// группу следующий переход не разворачивает: это решение человека.
func TestBrowserChatsMenuDoesNotJump(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	я, _ := st.CreateChat("Telegram · Вадим")
	главный, _ := st.CreateThread(я, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(главный, "assistant", "готово")
	мила, _ := st.CreateChat("Telegram · Мила")
	_ = st.AddChatMsg(мила, "peer", "привет")
	семья, _ := st.CreateChat("MAX · Семья")
	_ = st.AddChatMsg(семья, "peer", "ужин в семь")
	свой, _ := st.CreateChat("Про отпуск")
	_ = st.AddChatMsg(свой, "user", "поедем")

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	addr := func(n int64) string { return "chat:" + strconv.FormatInt(n, 10) }
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1290, 900),
		chromedp.Navigate(srv.URL+"/#screen="+addr(мила))); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть: %v", err)
	}
	waitScreen(t, ctx, addr(мила))
	_ = chromedp.Run(ctx, chromedp.Sleep(400*time.Millisecond))

	гдеПочта := func() float64 {
		var y float64
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			const п = document.querySelector('#menu [hx-get="/ui?screen=inbox"], #menu #menu-inbox > summary');
			return п ? п.getBoundingClientRect().top : -1;
		})()`, &y)); err != nil {
			t.Fatal(err)
		}
		return y
	}
	нажать := func(n int64) {
		t.Helper()
		if err := chromedp.Run(ctx,
			chromedp.Click(`//aside[@id='menu']//*[@hx-get='/ui?screen=`+addr(n)+`']`, chromedp.BySearch),
		); err != nil {
			t.Fatalf("нажать разговор %d: %v", n, err)
		}
		waitScreen(t, ctx, addr(n))
		_ = chromedp.Run(ctx, chromedp.Sleep(400*time.Millisecond))
	}

	before := гдеПочта()
	if before < 0 {
		t.Fatal("пункта «Почта» в меню не нашлось")
	}
	// переходы между группами: Telegram → MAX → свои → главный
	for _, n := range []int64{семья, свой, главный, мила} {
		нажать(n)
		if after := гдеПочта(); math.Abs(after-before) > 1 {
			t.Errorf("после перехода в %s меню сдвинулось: «Почта» была на %.0f, стала на %.0f", addr(n), before, after)
		}
	}

	// человек свернул группу MAX — переход в другой разговор её не разворачивает
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const у = document.getElementById('menu-чаты:MAX'); if (у) у.open = false; return !!у;
	})()`, nil)); err != nil {
		t.Fatal(err)
	}
	_ = chromedp.Run(ctx, chromedp.Sleep(200*time.Millisecond))
	нажать(свой)
	var открыта bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const у = document.getElementById('menu-чаты:MAX'); return !!(у && у.open); })()`, &открыта)); err != nil {
		t.Fatal(err)
	}
	if открыта {
		t.Error("свёрнутая рукой группа MAX развернулась от перехода в другой разговор")
	}
}

// «Плюс» в строке «Чатов» заводит новый чат и открывает его. Кнопка жила над
// панелью со списком чатов и пропала вместе с ней (17 сентября 2026). Плюс
// стоит в заголовке узла, и нажатие по нему не должно сворачивать «Чаты».
func TestBrowserPlusCreatesNewChat(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	я, _ := st.CreateChat("Telegram · Вадим")
	главный, _ := st.CreateThread(я, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(главный, "assistant", "готово")
	before, _ := st.Chats()

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()

	addr := "chat:" + strconv.FormatInt(главный, 10)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1290, 900),
		chromedp.Navigate(srv.URL+"/#screen="+addr)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть: %v", err)
	}
	waitScreen(t, ctx, addr)
	_ = chromedp.Run(ctx, chromedp.Sleep(400*time.Millisecond))

	if err := chromedp.Run(ctx,
		chromedp.Click(`//details[@id='menu-ask']/summary//button[contains(@class,'item-plus')]`, chromedp.BySearch),
		chromedp.Sleep(900*time.Millisecond),
	); err != nil {
		t.Fatalf("нажать плюс у «Чатов»: %v", err)
	}

	after, _ := st.Chats()
	if len(after) != len(before)+1 {
		t.Fatalf("плюс не завёл чат: было %d, стало %d", len(before), len(after))
	}
	var screen string
	var раскрыт bool
	_ = chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('screen').getAttribute('data-screen')`, &screen),
		chromedp.Evaluate(`!!(document.getElementById('menu-ask') && document.getElementById('menu-ask').open)`, &раскрыт))
	if screen == addr || !strings.HasPrefix(screen, "chat:") {
		t.Errorf("после плюса открыт %q, а ждали новый чат", screen)
	}
	if !раскрыт {
		t.Error("нажатие плюса свернуло «Чаты»")
	}
}
