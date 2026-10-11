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

// Кнопка шапки не ездит от чипа фильтра: на «Отправителях» «В спаме» даёт
// меню «···», «Белый список» — нет, и правый край «Разрешить адрес» обязан
// стоять на месте в обоих случаях (запись экрана 13 сентября 2026).
func TestBrowserHeaderButtonDoesNotMoveWithFilter(t *testing.T) {
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
		chromedp.EmulateViewport(1200, 820),
		chromedp.Navigate(srv.URL+"/#screen=senders")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, "senders")
	край := func() (float64, string) {
		var п struct {
			Right float64 `json:"right"`
			Name  string  `json:"имя"`
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`(() => { const к = document.querySelector('.header .controls .action'); if (!к) return {right: -1, имя: ''};
			 return {right: Math.round(к.getBoundingClientRect().right), имя: к.textContent.trim()}; })()`, &п)); err != nil {
			t.Fatal(err)
		}
		return п.Right, п.Name
	}
	до, name := край()
	if до < 0 {
		t.Fatal("кнопки шапки на «Отправителях» нет")
	}
	for _, чип := range []string{"Белый список", "В спаме"} {
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`[...document.querySelectorAll('.filters-bar [role=tab]')].find(э => э.textContent.includes('`+чип+`')).click()`, nil)); err != nil {
			t.Fatal(err)
		}
		var ready bool
		if err := chromedp.Run(ctx, chromedp.Poll(
			`document.querySelector('.filters-bar [role=tab][aria-selected=true]').textContent.includes('`+чип+`')`,
			&ready, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !ready {
			t.Fatalf("чип «%s» не выбрался: %v", чип, err)
		}
		после, имя2 := край()
		if после != до {
			t.Errorf("на «%s» правый край кнопки «%s» %v, было «%s» %v — кнопка ездит", чип, имя2, после, name, до)
		}
		// в тестовых данных чеков нет, и «···» здесь не появляется ни на
		// одном фильтре — поэтому отдельно: место под него занято, 28 точек
		// место под «···» — того же роста, что кнопка рядом (--control-h)
		var место []float64
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`(() => { const р = document.querySelector('.header .controls'); const п = р && р.lastElementChild;
			 if (!п || !(п.classList.contains('row-menu') || п.classList.contains('more-slot'))) return [-1, -1];
			 const к = р.querySelector('.action');
			 return [Math.round(п.getBoundingClientRect().width), k = Math.round(к.getBoundingClientRect().height)]; })()`, &место)); err != nil {
			t.Fatal(err)
		}
		if len(место) != 2 || место[0] <= 0 || место[0] != место[1] {
			t.Errorf("на «%s» место под «···» не занято или не в рост кнопки: %v", чип, место)
		}
	}
}
