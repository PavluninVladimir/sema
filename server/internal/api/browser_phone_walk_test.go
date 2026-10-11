package api

import (
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestBrowserPhoneWalkNoSideScroll — обход: каждый экран, на который ведёт
// меню и список разделов настроек, на телефоне не шире окна. Данные те же,
// что у `TestBrowserPhoneNoSideScroll`, поэтому длинные строки попадают и в
// экраны, о которых тот тест не знает; разметка остальных проверяется на
// ширину так же.
func TestBrowserPhoneWalkNoSideScroll(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, _ := phoneFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 5*time.Minute)

	// адреса, на которые ведут пункты меню, колонка разделов (на телефоне
	// колонки нет: разделы — строки корня настроек, они кликаются целиком)
	собрать := func() []string {
		var список []string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`Array.from(document.querySelectorAll('#menu [hx-get], #panel [hx-get], #feed [data-goto]'))
		  .map((э) => э.getAttribute('data-goto') || new URL(э.getAttribute('hx-get'), location.origin).searchParams.get('screen'))
		  .filter((а) => !!а)`, &список)); err != nil {
			t.Fatal(err)
		}
		return список
	}

	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=inbox'; true`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "inbox")
	очередь := собрать()
	// все разделы настроек: на телефоне они приезжают строками корня, а
	// обход должен знать о них заранее; корень и Обзор — первыми; плюс
	// вкладки «Серверов» и «Уведомлений»
	очередь = append(очередь, settingsRoot, settingsOverview)
	for _, group := range settingsGroups {
		очередь = append(очередь, group.Sections...)
	}
	очередь = append(очередь,
		serversScreen+"~"+serversTabAuto, serversScreen+"~"+serversTabLog, entityAddr(serversScreen, serversNew, ""),
		notifyScreen+"~"+notifyTabRules)
	видели := map[string]bool{"inbox": true}
	var обойдено, упало int
	for len(очередь) > 0 && обойдено < 120 {
		адрес := очередь[0]
		очередь = очередь[1:]
		if видели[адрес] {
			continue
		}
		видели[адрес] = true
		if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=' + encodeURIComponent(`+jsString(адрес)+`); true`, nil)); err != nil {
			t.Fatal(err)
		}
		var ок bool
		if err := chromedp.Run(ctx, chromedp.Poll(
			`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === `+jsString(адрес)+` && !!document.querySelector('#feed'); })()`,
			&ок, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !ок {
			t.Logf("экран %q не открылся — пропущен", адрес)
			continue
		}
		var готово bool
		_ = chromedp.Run(ctx, chromedp.Poll(`!document.querySelector('.htmx-request')`, &готово, chromedp.WithPollingTimeout(10*time.Second)))
		_ = chromedp.Run(ctx, chromedp.Evaluate(
			`document.querySelectorAll('#feed details').forEach((д) => { д.open = true; }); true`, nil))
		var p phoneProbe
		if err := chromedp.Run(ctx, chromedp.Evaluate(phoneProbeJS, &p)); err != nil {
			t.Fatal(err)
		}
		обойдено++
		if p.SW > p.CW {
			упало++
			t.Errorf("экран %q: страница едет вбок: %s", адрес, p)
		}
		if p.H1 >= 0 && p.H1 < phoneSide-0.5 {
			t.Errorf("экран %q: заголовок прижат к краю: %.1f", адрес, p.H1)
		}
		// разделы настроек и вложенные экраны находятся только изнутри
		for _, а := range собрать() {
			if !видели[а] {
				очередь = append(очередь, а)
			}
		}
	}
	var имена []string
	for а := range видели {
		имена = append(имена, а)
	}
	sort.Strings(имена)
	t.Logf("обойдено экранов: %d, с беглой прокруткой: %d: %v", обойдено, упало, имена)
	if обойдено < 10 {
		t.Errorf("обошли только %d экранов — меню не собралось", обойдено)
	}
}
