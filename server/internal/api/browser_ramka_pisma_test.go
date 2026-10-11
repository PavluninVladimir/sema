package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Письмо видно сразу, а не после переустановки адреса рамки: рамка,
// приехавшая подменой htmx вместе с src, загрузку не начинала, и человек
// видел пустое белое поле там, где письмо (14 сентября 2026).
func TestBrowserMessageVisibleAtOnce(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	m := &store.Message{MessageID: "<рамка@т>", Subject: "Вакансии недели",
		FromAddr: "news@пример.рф", FromName: "Рассылка", Date: time.Now(),
		Status: store.StatusInboxOK, Account: "проба"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBody(m.ID, store.Body{
		Text: "простой текст письма",
		HTML: `<html><body><p id="узнаваемый">Привет, Вадим! Показываем предложения.</p></body></html>`}); err != nil {
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

	screen := "msg:" + strconv.FormatInt(m.ID, 10)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(srv.URL+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, screen)
	// у рамки появился настоящий src — и она загрузилась сама
	var ок bool
	if err := chromedp.Run(ctx, chromedp.Poll(`(() => {
		const р = document.querySelector('iframe.mail-frame');
		return !!р && !!р.getAttribute('src') && р.getAttribute('src') === р.getAttribute('data-src');
	})()`, &ок, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !ок {
		var состояние string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => { const р = document.querySelector('iframe.mail-frame');
			return р ? 'src=' + р.getAttribute('src') + ' data-src=' + р.getAttribute('data-src') : 'рамки нет'; })()`, &состояние))
		t.Fatalf("рамка письма не получила адрес: %v (%s)", err, состояние)
	}
	// и в ней настоящее письмо: проверяем через отдельную загрузку того же
	// адреса — в песочницу заглянуть нельзя
	var body string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(async () => {
		const р = document.querySelector('iframe.mail-frame');
		const о = await fetch(р.getAttribute('src'));
		return await о.text();
	})()`, &body, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Привет, Вадим") {
		t.Errorf("по адресу рамки не письмо:\n%s", clipTest(body))
	}
}
