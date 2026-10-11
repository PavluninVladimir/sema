package api

// Очередь исходящих узнаёт откладываемое по признаку сервера
// (offline-snapshot, 4.0 — перевод очереди с разметки на признак).
//
// До группы 4 признак `Action.Deferrable` до браузера не доезжал, и очередь
// опознавала свои два действия структурно — по классу `.composer` и по
// скрепке. Теперь она смотрит на `data-deferrable`. Имя атрибута —
// единственное, что связывает шаблон сервера и клиент, и опечатка в нём
// означала бы потерянное сообщение: человек написал без связи, а оно никуда
// не легло. Терять написанное нельзя, поэтому жест проверяется настоящий:
// Сёма перестала отвечать, человек написал в чат и нажал «Отправить».

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestBrowserQueueKnowsDeferrableByFlag(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	conversation, err := st.CreateChat("Про снимок")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddChatMsg(conversation, "user", "первое сообщение"); err != nil {
		t.Fatal(err)
	}
	addr, стоп := semaOnPort(t, s, "")
	defer стоп()
	screen := "chat:" + strconv.FormatInt(conversation, 10)

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 120*time.Second)
	defer cancelTime()

	var exceptions []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			exceptions = append(exceptions, e.ExceptionDetails.Error())
		}
	})

	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate("http://"+addr+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть разговор: %v", err)
	}
	waitFor(t, ctx, `(() => { const э = document.getElementById('screen'); return !!э && э.getAttribute('data-screen') === `+jsString(screen)+`; })()`,
		"разговор не открылся", 20*time.Second)

	// признак стоит ровно у композера и скрепки — их и знает очередь
	where := evaluate(t, ctx, `(() => [...document.querySelectorAll('[data-deferrable]')].map((эл) => эл.tagName.toLowerCase() + (эл.className ? '.' + эл.className : '') + (эл.type ? ':' + эл.type : '')).join(' '))()`)
	t.Logf("откладываемое на экране: %s", where)
	if !strings.Contains(where, "form.composer") {
		t.Fatalf("у композера нет признака откладываемости: %s", where)
	}

	// Сёма пропала — человек всё равно пишет
	стоп()
	waitFor(t, ctx, `(async () => {
    try { await fetch('/api/health', {cache: 'no-store'}); return false; } catch (e) { return true; }
  })()`, "Сёма всё ещё отвечает", 20*time.Second)

	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#input`, "письмо без связи", chromedp.ByID),
		chromedp.Click(`form.composer button.send`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("написать и отправить: %v", err)
	}
	// написанное не потерялось: оно в очереди, с временем постановки, и его
	// можно отменить до отправки
	waitFor(t, ctx, `(() => {
    const п = document.getElementById('outbox');
    if (!п || п.hidden) return false;
    return п.textContent.indexOf('письмо без связи') >= 0 && п.textContent.indexOf('ждёт связи') > 0;
  })()`, "написанное без связи не встало в очередь", 30*time.Second)
	t.Logf("в очереди: %s", evaluate(t, ctx, `document.getElementById('outbox').textContent`))
	// поле очистилось: сообщение ушло в очередь, а не осталось черновиком
	if got := evaluate(t, ctx, `document.getElementById('input').value`); got != "" {
		t.Errorf("поле ввода не очистилось: %q", got)
	}
	if len(exceptions) > 0 {
		t.Errorf("исключения в консоли браузера:\n%s", strings.Join(exceptions, "\n"))
	}
}
