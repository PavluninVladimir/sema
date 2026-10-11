package api

// Написанное без связи уходит РОВНО ОДИН РАЗ (offline-snapshot, задача 5.5).
//
// Самое дорогое в очереди исходящих — не «дошло», а «дошло дважды». Второе
// сообщение собеседнику не отменить, и извиняться за него человеку, а не
// Сёме. Поэтому попадания считаются на сервере: не «клиент думает, что
// отправил один раз», а «в Сёму пришёл один запрос». Одного счётчика мало —
// рядом проверяется и лента разговора: запрос мог прийти один, а сообщение
// лечь дважды.
//
// Жест настоящий: Сёма перестал отвечать (не блокировка адресов CDP — она
// стоит до служебного работника, см. browser_snimok_offline_test.go),
// человек написал, нажал «Отправить», закрыл и открыл приложение заново, и
// только потом Сёма вернулся.

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// semaWithCount — та же Сёма на своём слушателе, но каждый POST /do с
// отправкой сообщения виден снаружи. Тело читается целиком и кладётся
// обратно: иначе разбор формы съел бы его у настоящего обработчика, и
// сообщение не дошло бы вовсе — тест был бы зелёным на пустом месте.
func semaWithCount(t *testing.T, s *Server, addr string, счёт *int64) (string, func()) {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("слушатель на %s: %v", addr, err)
	}
	настоящий := s.Handler()
	считаем := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/do" && r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			// считаем ПОПАДАНИЯ, а не удачи: дубль виден и тогда, когда Сёма
			// ответила на него отказом
			if bytes.Contains(body, []byte("send_message")) {
				atomic.AddInt64(счёт, 1)
			}
		}
		настоящий.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: считаем}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), func() { _ = srv.Close() }
}

// queueState — очередь исходящих глазами страницы: «статус:текст» через
// разделитель. Читается из того же IndexedDB, в который пишет клиент: имя базы
// и хранилища — договор между ними, и разойдутся они — тест это увидит.
const queueState = `(async () => {
  const db = await new Promise((ок) => { const з = indexedDB.open('сёма-исходящие', 1); з.onsuccess = () => ок(з.result); з.onerror = () => ок(null); });
  if (!db) return 'базы нет';
  const все = await new Promise((ок) => { const з = db.transaction('очередь').objectStore('очередь').getAll(); з.onsuccess = () => ок(з.result || []); з.onerror = () => ок([]); });
  return все.map((з) => з.статус + ':' + (з.текст || з.имяФайла)).join(' | ') || 'пусто';
})()`

// queueLike — условие для ожидания: очередь стала ровно такой.
func queueLike(хотим string) string {
	return `(async () => (await ` + queueState + `) === ` + jsString(хотим) + `)()`
}

func TestBrowserQueueLeavesOnce(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	conversation, err := st.CreateChat("Про очередь")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddChatMsg(conversation, "user", "было до"); err != nil {
		t.Fatal(err)
	}
	screen := "chat:" + strconv.FormatInt(conversation, 10)
	const письмо = "одно сообщение без связи"

	var попаданий int64
	addr, стоп := semaWithCount(t, s, "", &попаданий)
	корень := "http://" + addr

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 180*time.Second)
	defer cancelTime()

	var exceptions []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			exceptions = append(exceptions, e.ExceptionDetails.Error())
		}
	})
	чисто := func(where string) {
		t.Helper()
		if len(exceptions) > 0 {
			t.Fatalf("исключения в консоли браузера (%s):\n%s", where, strings.Join(exceptions, "\n"))
		}
	}

	if err := chromedp.Run(ctx, network.Enable(), chromedp.Navigate(корень+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть разговор: %v", err)
	}
	waitFor(t, ctx, `(() => { const э = document.getElementById('screen'); return !!э && э.getAttribute('data-screen') === `+jsString(screen)+`; })()`,
		"разговор не открылся", 20*time.Second)

	// без работника перезагрузка без Сёмы даёт пустую страницу, и очередь
	// после неё некому разбирать: проверять было бы нечего
	waitFor(t, ctx, `(async () => {
    const р = await navigator.serviceWorker.ready.catch(() => null);
    return !!(р && р.active && navigator.serviceWorker.controller);
  })()`, "служебный работник не взял страницу", 30*time.Second)
	чисто("первая отрисовка")

	// ---- Сёма пропал ----
	if err := chromedp.Run(ctx, network.OverrideNetworkState(true, 0, -1, -1)); err != nil {
		t.Fatalf("пометить страницу офлайном: %v", err)
	}
	стоп()
	waitFor(t, ctx, `(async () => {
    try { await fetch('/api/health', {cache: 'no-store'}); return false; } catch (e) { return true; }
  })()`, "Сёма всё ещё отвечает — рвать связь не получилось", 20*time.Second)

	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#input`, письмо, chromedp.ByID),
		chromedp.Click(`form.composer button.send`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("написать и отправить без связи: %v", err)
	}
	waitFor(t, ctx, `(() => {
    const п = document.getElementById('outbox');
    return !!п && !п.hidden && п.textContent.indexOf(`+jsString(письмо)+`) >= 0;
  })()`, "написанное без связи не встало в очередь", 30*time.Second)
	чисто("сообщение в очереди")

	// ---- приложение закрыли и открыли заново ----
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Logf("перезагрузка без Сёмы: %v", err) // оболочка из кэша, событий может не быть
	}
	_ = chromedp.Run(ctx, chromedp.WaitReady("body", chromedp.ByQuery))
	// Запись пережила перезагрузку и ждёт СВЯЗИ, а не решения человека.
	// «Сомнительно» здесь означало бы, что клиент сам себе не верит: человеку
	// пришлось бы нажимать «Отправить всё равно» — то есть решать за него то,
	// чего не случилось. Ждём, а не смотрим один раз: свежая страница успевает
	// взять запись («уходит»), убедиться, что Сёмы нет, и вернуть её обратно.
	waitFor(t, ctx, queueLike("ожидает:"+письмо),
		"после перезагрузки запись должна ждать связи, а не человека", 30*time.Second)
	t.Logf("очередь после перезагрузки: %s", evaluate(t, ctx, queueState))

	// ---- Сёма вернулся ----
	_, стоп2 := semaWithCount(t, s, addr, &попаданий)
	defer стоп2()
	if err := chromedp.Run(ctx, network.OverrideNetworkState(false, 0, -1, -1)); err != nil {
		t.Fatalf("вернуть страницу в сеть: %v", err)
	}

	// ждём первого попадания, а потом ещё столько же — двойная отправка
	// приходит не мгновенно, а следующим кругом очереди (двадцать секунд)
	ждали := time.Now()
	for atomic.LoadInt64(&попаданий) == 0 && time.Since(ждали) < 90*time.Second {
		time.Sleep(500 * time.Millisecond)
	}
	if atomic.LoadInt64(&попаданий) == 0 {
		t.Fatalf("связь вернулась, а отложенное сообщение так и не ушло (очередь: %s)",
			evaluate(t, ctx, `(() => { const п = document.getElementById('outbox'); return п ? п.textContent : 'панели нет'; })()`))
	}
	t.Logf("первое попадание через %s", time.Since(ждали).Round(time.Millisecond))
	// 25 секунд — больше круга очереди (20 с): если запись не убрали или
	// объявили сомнительной, второй заход случится именно здесь
	time.Sleep(25 * time.Second)

	if n := atomic.LoadInt64(&попаданий); n != 1 {
		t.Errorf("отправка сообщения пришла в Сёму %d раз вместо одного — человек получил дубль", n)
	}
	// лента разговора — второй свидетель: запрос мог прийти один, а
	// сообщение лечь дважды
	сообщения, err := st.ChatMsgs(conversation, 200)
	if err != nil {
		t.Fatal(err)
	}
	сколько := 0
	for _, м := range сообщения {
		if strings.Contains(м.Content, письмо) {
			сколько++
		}
	}
	if сколько != 1 {
		t.Errorf("в разговоре %d копий написанного вместо одной", сколько)
	}
	// очередь пуста: запись убрана, а не осталась висеть «сомнительно» с
	// кнопкой «Отправить всё равно» — нажав её, человек послал бы третье
	осталось := evaluate(t, ctx, queueState)
	t.Logf("очередь после отправки: %s, попаданий: %d, копий в ленте: %d",
		осталось, atomic.LoadInt64(&попаданий), сколько)
	if осталось != "пусто" {
		t.Errorf("ушедшая запись осталась в очереди: %s", осталось)
	}
	чисто("после возврата связи")
}
