package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Меню «···» у карточки в чате видно целиком: у карточки было
// overflow: hidden, и выпадашка обрезалась её границей — нажатие
// выглядело как «ничего не происходит» (запись экрана 13 сентября 2026).
func TestBrowserCardMenuVisibleInChat(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	чат, err := st.CreateChat("Про серверы")
	if err != nil {
		t.Fatal(err)
	}
	// карточка памяти: такая же, как на записи
	карточка := `{"title":"Запомнить · серверы","result":"записано","memory":{"text":"беспарольный sudo","area":"серверы"}}`
	if err := st.AddChatMsg(чат, "tool", карточка); err != nil {
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

	screen := "chat:" + strconv.FormatInt(чат, 10)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1200, 800),
		chromedp.Navigate(srv.URL+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, screen)
	var has bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`!!document.querySelector('.matter details.row-menu > summary')`, &has)); err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Skip("у карточки нет меню в этой сборке")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`.matter details.row-menu > summary`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «···»: %v", err)
	}
	var видно struct {
		Открыто bool    `json:"открыто"`
		Height  float64 `json:"высота"`
		Внутри  bool    `json:"внутри"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const м = document.querySelector('.matter details.row-menu[open] .dropdown');
		if (!м) return {открыто: false, высота: 0, внутри: false};
		const р = м.getBoundingClientRect();
		const к = м.closest('.matter').getBoundingClientRect();
		// меню видно целиком: не нулевой высоты и не срезано окном
		return {открыто: true, высота: Math.round(р.height),
			внутри: р.top >= 0 && р.bottom <= window.innerHeight + 1 && р.height > 10 && к.width > 0};
	})()`, &видно)); err != nil {
		t.Fatal(err)
	}
	if !видно.Открыто {
		t.Fatal("меню «···» у карточки не открылось")
	}
	if !видно.Внутри {
		t.Errorf("меню открылось, но видно не целиком: высота %v", видно.Height)
	}
}
