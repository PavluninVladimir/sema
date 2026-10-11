package api

// Ленивый раздел в настоящем Chrome: раскрытие догружает часть, «ещё»
// просит её же с удвоенным пределом, поиск фильтрует на сервере. Всё
// нажатиями — тем местом, которым пользуется человек.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestBrowserLazyMAXChatsSection(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	if err := st.SaveMAXAccount(&store.MAXAccount{Phone: "+70000000000", State: "ok"}); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 25; i++ {
		c := &store.MAXChat{ID: i, Title: fmt.Sprintf("Чат номер %02d", i), Kind: "chat"}
		if i == 7 {
			c.Title, c.Kind = "Ozon доставка", "channel"
		}
		if err := st.SaveMAXChat(c); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 120*time.Second)
	defer cancelTime()
	// консоль: что пошло не так, видно только оттуда
	var консоль []string
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			консоль = append(консоль, "исключение: "+e.ExceptionDetails.Error())
		case *runtime.EventConsoleAPICalled:
			var части []string
			for _, a := range e.Args {
				части = append(части, string(a.Value))
			}
			консоль = append(консоль, string(e.Type)+": "+strings.Join(части, " "))
		}
	})

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate(srv.URL+"/#screen="+maxScreen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, maxScreen)

	// свёрнутый раздел приехал без единого чата
	// каждый чат — одна строка матрицы прав в теле части
	const чатов = `document.querySelectorAll('details.fragment .fragment-body tr.matrix-row').length`
	var строкЧатов int
	if err := chromedp.Run(ctx, chromedp.Evaluate(чатов, &строкЧатов)); err != nil {
		t.Fatal(err)
	}
	if строкЧатов != 0 {
		t.Fatalf("до раскрытия в разделе уже %d строк — он не ленивый", строкЧатов)
	}
	// раскрываем нажатием на заголовок
	if err := chromedp.Run(ctx, chromedp.Click(`details.fragment > summary`, chromedp.ByQuery)); err != nil {
		t.Fatalf("раскрыть раздел: %v", err)
	}
	ждатьСтрок := func(сколько int, зачем string) {
		t.Helper()
		var ок bool
		if err := chromedp.Run(ctx, chromedp.Poll(
			fmt.Sprintf(`%s === %d`, чатов, сколько),
			&ок, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !ок {
			var has int
			_ = chromedp.Run(ctx, chromedp.Evaluate(чатов, &has))
			var состояние string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => { const д = document.querySelector('details.fragment'); if (!д) return 'details нет';
				const т = д.querySelector('.fragment-body'); return 'open=' + д.open + ' загружено=' + (т && т.getAttribute('data-loaded')) +
				' тело=' + (т ? т.innerHTML.slice(0, 300) : 'нет'); })()`, &состояние))
			// две пробы: доходит ли тело до браузера и работает ли подмена
			// по селектору — чтобы отличить беду сети от беды htmx
			var след string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`JSON.stringify(window.__части || 'нет') + ' | детей в теле: ' + (document.querySelector('.fragment-body') || {children: {length: -1}}).children.length + ' | id: ' + ((document.querySelector('.fragment-body') || {}).id)`, &след))
			t.Logf("след загрузчика: %s", след)
			var проба string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`(async () => {
				const д = document.querySelector('details.fragment'); const т = д.querySelector('.fragment-body');
				т.id = 'проба-тела';
				const r = await fetch('/ui?screen=' + encodeURIComponent(document.getElementById('screen').dataset.screen) + '&part=' + д.getAttribute('data-fragment'), {headers: {'HX-Request': 'true'}});
				const текст = await r.text();
				let через = 'нет';
				try { await htmx.ajax('GET', '/ui?screen=settings:max&part=max-chats', { target: '#проба-тела', swap: 'innerHTML' }); через = String(т.innerHTML.length); } catch (e) { через = 'ошибка ' + e; }
				return 'fetch=' + r.status + ' байт=' + текст.length + ' htmx.ajax(селектор)→байт в теле=' + через;
			})()`, &проба, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
			t.Fatalf("%s: строк %d, ждали %d (%v)\n%s\nпроба: %s\nконсоль: %s", зачем, has, сколько, err, состояние, проба, strings.Join(консоль, "\n"))
		}
	}
	ждатьСтрок(fragmentLimit, "после раскрытия")

	// «Показать ещё» — внутри части, и просит именно часть
	if err := chromedp.Run(ctx, chromedp.Click(`details.fragment [data-action="$more"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «ещё»: %v", err)
	}
	ждатьСтрок(25, "после «ещё»")

	// поиск — на сервере, через единственное поле поиска экрана (верхнее):
	// находит и тот чат, которого в первой двадцатке не было бы
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#search`, "ozon", chromedp.ByQuery)); err != nil {
		t.Fatalf("набрать поиск: %v", err)
	}
	ждатьСтрок(1, "после поиска")
	var name string
	if err := chromedp.Run(ctx, chromedp.Text(`details.fragment .fragment-body tr.matrix-row td.who .title`, &name, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(name, "Ozon") {
		t.Errorf("по «ozon» нашёлся %q", name)
	}
}
