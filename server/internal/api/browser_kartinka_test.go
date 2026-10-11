package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/chromedp/chromedp"
)

// Картинка в записи показывается картинкой: рамка-песочница для неё не
// годится — файл отдаётся с запретом встраивания, и человек видел «сайт
// не позволяет установить соединение» вместо своей картинки
// (14 сентября 2026).
func TestBrowserImageInRecordVisible(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	// настоящий png 1×1, чтобы браузер его действительно нарисовал
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
		0, 0, 0, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1,
		0x0d, 0x0a, 0x2d, 0xb4, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82}
	a, _, err := s.eng.AddFile(context.Background(), "qr.png", "image/png", png,
		engine.FileSource{Kind: "upload"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutNotification(&store.Notification{
		Key: "vision:" + a.SHA256, Source: "core", Level: "info",
		Title:   "Показать картинку модели?",
		Summary: "Файл «qr.png»: на картинке не нашлось текста",
		Ref:     "file:" + itoa(a.ID),
		Fields:  []store.NotifyField{{Label: "Файл", Value: "qr.png"}},
		Actions: []store.NotifyAction{
			{Kind: "core", Label: "Показать", Op: "vision_show", Ref: a.SHA256},
			{Kind: "core", Label: "Не показывать", Op: "vision_skip", Ref: a.SHA256},
		}}); err != nil {
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

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(srv.URL+"/#screen="+notifyScreen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, notifyScreen)
	var kind struct {
		Есть     bool `json:"есть"`
		Рамок    int  `json:"рамок"`
		Загрузил bool `json:"загрузил"`
	}
	if err := chromedp.Run(ctx, chromedp.Poll(`(() => {
		const и = document.querySelector('#feed .row-image img');
		return !!и && и.complete && и.naturalWidth > 0;
	})()`, &kind.Загрузил, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !kind.Загрузил {
		var состояние string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => { const и = document.querySelector('#feed .row-image img');
			if (!и) return 'картинки нет, рамок: ' + document.querySelectorAll('#feed iframe').length;
			return 'src=' + и.getAttribute('src') + ' complete=' + и.complete + ' w=' + и.naturalWidth; })()`, &состояние))
		t.Fatalf("картинка не нарисовалась: %v (%s)", err, состояние)
	}
	// и рамки в записи нет вовсе
	var рамок int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelectorAll('#feed iframe').length`, &рамок)); err != nil {
		t.Fatal(err)
	}
	if рамок != 0 {
		t.Errorf("в записи осталась рамка (%d) — в ней картинку не видно", рамок)
	}
}
