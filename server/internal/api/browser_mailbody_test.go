package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/chromedp/chromedp"
)

// Письмо в настоящем браузере: HTML показывается, а скрипт из письма не
// выполняется. Разметкой это не проверишь — нужен браузер, который сам
// решает, что ему исполнять.
func TestBrowserMessageInSandbox(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	// письмо-ловушка: скрипт, обработчик без кавычек и трекинг-пиксель
	m := &store.Message{MessageID: "<ловушка@тест>", Subject: "Счёт за август",
		FromAddr: "billing@пример.рф", FromName: "Пример", Status: store.StatusInboxOK}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	письмо := `<p id="метка">К оплате 512,30 ₽</p>` +
		`<script>window.top.сломал = 1; document.title = "взлом"</script>` +
		`<img src=https://track/пиксель.gif onerror=window.top.сломал=2>` +
		`<a href="javascript:window.top.сломал=3">жми</a>`
	if err := st.SaveBody(m.ID, store.Body{Text: "К оплате 512,30", HTML: mail.SanitizeHTML(письмо)}); err != nil {
		t.Fatal(err)
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

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen=msg:"+itoa(m.ID))); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть письмо: %v", err)
	}
	// рамка появилась
	var рамок int
	ждать := func(проба string, target *int) {
		t.Helper()
		for i := 0; i < 60; i++ {
			if err := chromedp.Run(ctx, chromedp.Evaluate(проба, target)); err == nil && *target > 0 {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	ждать(`document.querySelectorAll('.mail-frame').length`, &рамок)
	if рамок == 0 {
		t.Fatal("рамки письма нет на экране")
	}

	// дать письму время «выполниться», если бы оно могло
	time.Sleep(1500 * time.Millisecond)
	var сломал any
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.сломал ?? null`, &сломал)); err != nil {
		t.Fatal(err)
	}
	if сломал != nil {
		t.Fatalf("скрипт из письма достал до страницы Сёмы: %v", сломал)
	}
	var title string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.title`, &title)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(title, "взлом") {
		t.Fatalf("письмо переписало заголовок страницы: %q", title)
	}
	// и само письмо видно
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(document.querySelector('.mail-frame').contentDocument === null) ? 'непрозрачный' : 'ВИДЕН'`, &text)); err != nil {
		t.Fatal(err)
	}
	if text != "непрозрачный" {
		t.Error("страница Сёмы видит документ письма: песочница не работает")
	}
	// рамка занимает место и загрузилась: пустая песочница — это тоже
	// «письма не видно», и такой проверке грош цена без размера
	var размер float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.querySelector('.mail-frame').getBoundingClientRect().height`, &размер)); err != nil {
		t.Fatal(err)
	}
	if размер < 100 {
		t.Errorf("рамка письма высотой %.0f px — письма не видно", размер)
	}
	var загрузок int
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`performance.getEntriesByType('resource').filter(r => r.name.includes('%d0%bf%d0%b8%d1%81')).length`, &загрузок)); err != nil {
		t.Fatal(err)
	}
	if загрузок == 0 {
		t.Error("браузер за телом письма не ходил: рамка пуста")
	}
	// Что браузер СОГЛАСИЛСЯ рисовать рамку, здесь не проверить:
	// заблокированная рамка так же непрозрачна и так же шлёт запрос, а в
	// дерево фреймов песочница не попадает — у неё непрозрачный источник,
	// и браузер ведёт её отдельной целью. Это держит
	// TestMessageFrameNotForbiddenByHeader: там видно сами заголовки.
}
