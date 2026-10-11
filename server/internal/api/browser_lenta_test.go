package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// feedMeasure — где стоит прокрутка ленты разговора.
type feedMeasure struct {
	Height   int  `json:"высота"`
	Scroll   int  `json:"прокрутка"`
	Window   int  `json:"окно"`
	AtBottom bool `json:"внизу"`
	Messages int  `json:"сообщений"`
	Last     bool `json:"последнее"`
}

func measureFeed(t *testing.T, ctx context.Context) feedMeasure {
	t.Helper()
	var з feedMeasure
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const л = document.getElementById('feed');
		if (!л) return {высота: -1};
		const пузыри = л.querySelectorAll('[class*="bubble"], .bubble, [data-role]');
		const текст = л.innerText || '';
		return {
			высота: л.scrollHeight, прокрутка: Math.round(л.scrollTop), окно: л.clientHeight,
			внизу: л.scrollHeight - л.scrollTop - л.clientHeight < 60,
			сообщений: пузыри.length,
			последнее: текст.includes('сообщение 80'),
		};
	})()`, &з)); err != nil {
		t.Fatal(err)
	}
	return з
}

// Разговор открывается на последнем сообщении, а не на первом из порции:
// переписку читают снизу, и человек не должен листать полсотни реплик,
// чтобы увидеть, что ему только что ответили.
func TestBrowserConversationOpensAtLast(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Про отпуск")
	for i := 1; i <= 80; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		_ = st.AddChatMsg(conversation, role, fmt.Sprintf("сообщение %d — %s", i, strings.Repeat("текст реплики ", 6)))
	}
	_ = store.ChatKindSema

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	addr := "chat:" + strconv.FormatInt(conversation, 10)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate(srv.URL+"/#screen="+addr)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть: %v", err)
	}
	waitScreen(t, ctx, addr)
	// карточкам и шрифтам нужно мгновение, чтобы лента выросла до конца
	_ = chromedp.Run(ctx, chromedp.Sleep(800*time.Millisecond))

	з := measureFeed(t, ctx)
	t.Logf("лента: высота %d, прокрутка %d, окно %d, внизу %v, последнее видно %v",
		з.Height, з.Scroll, з.Window, з.AtBottom, з.Last)
	if !з.AtBottom {
		t.Errorf("разговор открылся не на последнем сообщении: прокрутка %d из %d", з.Scroll, з.Height-з.Window)
	}
	if !з.Last {
		t.Error("последнего сообщения нет в ленте")
	}
}

// Повторный заход в разговор через меню — снова на последнем сообщении.
// Признак «человек был внизу» ставился при первом показе и не
// сбрасывался никогда: во второй раз лента приходила новой, стояла
// вверху, и клиент считал, что человек читает историю, — вниз не ехал.
func TestBrowserReenterConversationAtBottom(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Про отпуск")
	for i := 1; i <= 80; i++ {
		_ = st.AddChatMsg(conversation, "user", fmt.Sprintf("сообщение %d — %s", i, strings.Repeat("текст реплики ", 6)))
	}

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	addr := "chat:" + strconv.FormatInt(conversation, 10)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate(srv.URL+"/#screen="+addr)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть: %v", err)
	}
	waitScreen(t, ctx, addr)
	_ = chromedp.Run(ctx, chromedp.Sleep(500*time.Millisecond))

	// у каждого узла меню свой id: раскрытие узлов клиент помнит по нему.
	// Адрес узла гасится, когда первый ребёнок ведёт туда же, и id из
	// адреса выходил «м-» сразу у «Почты» и «Чатов» — раскрытие путалось
	var дубли []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const виденные = {}, дубли = [];
		document.querySelectorAll('#menu details[id]').forEach((у) => {
			if (виденные[у.id] || у.id === 'menu-') дубли.push(у.id || '(пусто)');
			виденные[у.id] = true;
		});
		return дубли;
	})()`, &дубли)); err != nil {
		t.Fatal(err)
	}
	if len(дубли) > 0 {
		t.Errorf("у узлов меню повторяются или пусты id: %v", дубли)
	}
	// в узле «Чаты» нет строк-двойников: «Обзор» и «Все разговоры»
	// открывали тот же разговор с Сёмой, что и первый ребёнок
	var вЧатах string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => { const у = document.getElementById('menu-ask'); return у ? у.innerText : '' })()`, &вЧатах))
	for _, двойник := range []string{"Обзор", "Все разговоры"} {
		if strings.Contains(вЧатах, двойник) {
			t.Errorf("в узле «Чаты» осталась строка-двойник %q", двойник)
		}
	}

	// человек почитал историю и ушёл в «Файлы»
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('feed').scrollTop = 0; true`, nil),
		chromedp.Click(`//aside[@id='menu']//button[contains(@class,'item') and contains(normalize-space(.),'Файлы')]`, chromedp.BySearch),
	); err != nil {
		t.Fatalf("уйти в «Файлы»: %v", err)
	}
	waitScreen(t, ctx, "files")

	// и вернулся в разговор через меню: раскрыл «Чаты» и нажал разговор
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const у = document.getElementById('menu-ask'); if (у) у.open = true; return true })()`, nil),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Click(`//aside[@id='menu']//*[@hx-get='/ui?screen=`+addr+`']`, chromedp.BySearch),
	); err != nil {
		t.Fatalf("вернуться в разговор: %v", err)
	}
	waitScreen(t, ctx, addr)
	_ = chromedp.Run(ctx, chromedp.Sleep(800*time.Millisecond))

	з := measureFeed(t, ctx)
	t.Logf("после возврата: высота %d, прокрутка %d, окно %d, внизу %v", з.Height, з.Scroll, з.Window, з.AtBottom)
	if !з.AtBottom {
		t.Errorf("повторный заход открыл разговор не на последнем: прокрутка %d из %d", з.Scroll, з.Height-з.Window)
	}
}

// Ранние сообщения грузятся сами, когда человек долистал до верха, — и
// лента при этом не прыгает: ранние встают сверху, а место, где он
// читал, остаётся на месте, а не уезжает вниз к последнему.
func TestBrowserEarlierLoadThemselves(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Про отпуск")
	for i := 1; i <= 120; i++ {
		_ = st.AddChatMsg(conversation, "user", fmt.Sprintf("сообщение %d — %s", i, strings.Repeat("текст реплики ", 6)))
	}

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	addr := "chat:" + strconv.FormatInt(conversation, 10)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate(srv.URL+"/#screen="+addr)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть: %v", err)
	}
	waitScreen(t, ctx, addr)
	_ = chromedp.Run(ctx, chromedp.Sleep(600*time.Millisecond))

	естьТекст := func(text string) bool {
		var да bool
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('feed').innerText||'').includes(`+strconv.Quote(text)+`)`, &да))
		return да
	}
	// сначала видна только последняя порция
	if естьТекст("сообщение 30 ") {
		t.Fatal("ранние сообщения видны сразу — порций нет, проверять нечего")
	}

	// человек долистал до верха
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const л = document.getElementById('feed'); л.scrollTop = 0; л.dispatchEvent(new Event('scroll')); return true })()`, nil),
	); err != nil {
		t.Fatal(err)
	}
	// ранние подгрузились сами, без нажатия
	подгрузились := false
	for i := 0; i < 40; i++ {
		_ = chromedp.Run(ctx, chromedp.Sleep(150*time.Millisecond))
		if естьТекст("сообщение 30 ") {
			подгрузились = true
			break
		}
	}
	if !подгрузились {
		t.Fatal("долистал до верха — ранние сообщения не подгрузились сами")
	}
	_ = chromedp.Run(ctx, chromedp.Sleep(300*time.Millisecond))

	з := measureFeed(t, ctx)
	t.Logf("после подгрузки: высота %d, прокрутка %d, окно %d, внизу %v", з.Height, з.Scroll, з.Window, з.AtBottom)
	if з.AtBottom {
		t.Error("подгрузка ранних утащила ленту вниз к последнему — человек потерял место")
	}
	if з.Scroll < 200 {
		t.Errorf("после подгрузки лента стоит у самого верха (%d) — ранние не сдвинули место чтения", з.Scroll)
	}
}

// Ранние сообщения подгружаются сами и тогда, когда в чат пришли через
// меню, а не по адресу. 4 октября 2026 правка гонки переходов считала
// запросы в окно, но события запроса, чья кнопка выпала из страницы, htmx
// шлёт в document, а не в body: счётчик залипал после первого же перехода
// из меню, и подгрузка ранних больше не начиналась (ревью perf-pass 5).
func TestBrowserEarlierLoadAfterMenuNavigation(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Про отпуск")
	for i := 1; i <= 120; i++ {
		_ = st.AddChatMsg(conversation, "user", fmt.Sprintf("сообщение %d — %s", i, strings.Repeat("текст реплики ", 6)))
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1100, 820), chromedp.Navigate(srv.URL+"/#screen=files")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть: %v", err)
	}
	waitScreen(t, ctx, "files")
	// в «Задачи» и оттуда в чат — оба раза кнопками меню
	if err := chromedp.Run(ctx, chromedp.Click(`//aside[@id='menu']//button[contains(@class,'item') and contains(normalize-space(.),'Задачи')]`, chromedp.BySearch)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "tasks")
	addr := "chat:" + strconv.FormatInt(conversation, 10)
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const у = document.getElementById('menu-ask'); if (у) у.open = true; return true })()`, nil),
		chromedp.Click(`//aside[@id='menu']//button[contains(@class,'item') and contains(normalize-space(.),'Про отпуск')]`, chromedp.BySearch)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, addr)
	var busy bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!sema.windowBusy()`, &busy, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("после переходов из меню окно числится занятым: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const л = document.getElementById('feed'); л.scrollTop = 0; л.dispatchEvent(new Event('scroll')); return true })()`, nil)); err != nil {
		t.Fatal(err)
	}
	var loaded bool
	if err := chromedp.Run(ctx, chromedp.Poll(`(document.getElementById('feed').innerText || '').includes('сообщение 30 ')`, &loaded,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil || !loaded {
		t.Fatalf("пришёл в чат через меню, долистал до верха — ранние не подгрузились: %v", err)
	}
}
