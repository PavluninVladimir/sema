package api

// Жест «потянуть вниз» на телефоне (phone-refresh, задачи 6.1–6.3).
//
// Жест проверяется настоящими касаниями: `Input.dispatchTouchEvent` шлёт их
// тем же путём, которым идёт палец, — через настоящий конвейер событий
// браузера, с настоящим порядком обработчиков и настоящим preventDefault.
// Ни одна функция клиента здесь не зовётся: иначе зелено, а сломано.
//
// ЧЕГО ЭТОТ ТЕСТ НЕ ПРОВЕРЯЕТ. Плавность жеста, ощущение резинки и спор с
// инерцией прокрутки на настоящем пальце — этого браузером в headless не
// достать вовсе, и выдавать одно за другое нельзя. Их смотрит человек на
// живом телефоне (задача 6.4). Здесь проверяются следствия: ушёл ли запрос
// экрана, не перезагрузилась ли страница целиком и молчит ли жест там, где
// должен молчать.

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// semaWithScreenCount — считает, сколько раз спросили разметку экрана
// (`GET /ui`) и сколько раз качали саму оболочку (`GET /`). Первое — то, чего
// жест должен добиться; второе — то, чего он добиваться не должен ни разу:
// родное потягивание браузера перезагружает страницу целиком.
func semaWithScreenCount(t *testing.T, s *Server, экранов, оболочек *int64) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель: %v", err)
	}
	настоящий := s.Handler()
	считаем := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ui":
			atomic.AddInt64(экранов, 1)
		case r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/index.html"):
			atomic.AddInt64(оболочек, 1)
		}
		настоящий.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: считаем}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), func() { _ = srv.Close() }
}

// pullDown — палец вниз на длину `путь` от точки внутри ленты. Шагов
// несколько: жест сначала должен быть замечен (10 точек), и по одному
// прыжку с нуля до порога различить «замечен» от «сработал» нельзя.
func pullDown(t *testing.T, ctx context.Context, x, y, путь float64) {
	t.Helper()
	шаги := []*input.DispatchTouchEventParams{
		input.DispatchTouchEvent(input.TouchStart, []*input.TouchPoint{{X: x, Y: y, ID: 1}}),
	}
	for _, доля := range []float64{0.2, 0.5, 0.8, 1.0} {
		шаги = append(шаги, input.DispatchTouchEvent(input.TouchMove,
			[]*input.TouchPoint{{X: x, Y: y + путь*доля, ID: 1}}))
	}
	шаги = append(шаги, input.DispatchTouchEvent(input.TouchEnd, []*input.TouchPoint{}))
	for _, ш := range шаги {
		if err := chromedp.Run(ctx, ш); err != nil {
			t.Fatalf("касание: %v", err)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func TestBrowserPullDownRefreshesScreen(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	// Лента должна быть длиннее окна: иначе «посреди списка» не устроить —
	// scrollTop останется нулём, сколько его ни ставь.
	//
	// ПОЧЕМУ НЕ ЧАТ, хотя макет показывает и ленту переписки. В чате вверху
	// стоит «Показать более ранние», и клиент нажимает её сам, как только
	// лента доехала до верха (ленивая подгрузка переписки). Прокрутка в ноль
	// заводит подгрузку, та подменяет ленту и возвращает прокрутку к низу — и
	// жест проверять уже не на чем. В чате он и правда живёт только тогда,
	// когда вся история уже загружена, — это то же условие «в самом верху», а
	// не исключение из него. Проверяем на списке без ленивой подгрузки.
	for i := 0; i < 40; i++ {
		if err := st.CreateTask(&store.Task{
			Title: "Задача для длинного списка номер " + strconv.Itoa(i),
			Kind:  "prompt", Prompt: "ничего не делать", Schedule: "9:00", Sort: 100 + i,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// порция длинного списка (ui.list_rows, perf-pass 5) — больше задач:
	// «Показать ещё» внизу ленты мешало бы жесту, а список должен быть длинным
	if err := st.SetSetting(engine.ListRowsKey, "100"); err != nil {
		t.Fatal(err)
	}
	screen := "tasks"

	var экранов, оболочек int64
	addr, стоп := semaWithScreenCount(t, s, &экранов, &оболочек)
	defer стоп()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 150*time.Second)
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

	// узкое окно с касанием: жест живёт только на телефоне, и без
	// эмуляции касаний слать его нечем
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(390, 844),
		emulation.SetTouchEmulationEnabled(true).WithMaxTouchPoints(1),
		chromedp.Navigate("http://"+addr+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть разговор: %v", err)
	}
	waitFor(t, ctx, `(() => { const э = document.getElementById('screen'); return !!э && э.getAttribute('data-screen') === `+jsString(screen)+`; })()`,
		"разговор не открылся", 20*time.Second)
	// опрос глушим: он сам дёргает то же «sema:refresh», и счётчик экранов
	// рос бы без всякого жеста
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
    const э = document.getElementById('screen');
    э.setAttribute('hx-trigger', 'sema:refresh');
    htmx.process(э, true); // без true htmx 4 узел не переинициализирует, и every остался бы
    return true;
  })()`, nil)); err != nil {
		t.Fatalf("выключить опрос: %v", err)
	}
	// и провод тоже: его кадры дёргают то же событие
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => { try { sema.wire(); } catch (e) {} return true; })()`, nil))

	// разделы раскрыты: длинные списки сервер отдаёт свёрнутыми, и в
	// свёрнутом виде лента короче окна — прокручивать нечего. Человек их
	// раскрывает нажатием, тест — тем же самым `open`
	_ = chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { document.querySelectorAll('#feed details').forEach((д) => { д.open = true; }); return true; })()`, nil),
		chromedp.Sleep(300*time.Millisecond))

	// лента и правда прокручиваемая, иначе «посреди списка» не проверить
	размеры := evaluate(t, ctx, `(() => {
    const л = document.getElementById('feed');
    return л ? л.scrollHeight + '/' + л.clientHeight : 'ленты нет';
  })()`)
	t.Logf("лента (scrollHeight/clientHeight): %s", размеры)
	var прокручиваемая bool
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
    const л = document.getElementById('feed');
    return !!л && л.scrollHeight > л.clientHeight + 100;
  })()`, &прокручиваемая))
	if !прокручиваемая {
		t.Fatalf("лента не прокручивается (%s) — проверять «посреди списка» нечем", размеры)
	}
	// «Показать ещё» в ленте нет: иначе прокрутка в начало сама нажала бы её
	// (ленивая подгрузка), лента подменилась бы — и тест счёл бы это удачей
	// жеста, которого не было
	if ещё := evaluate(t, ctx, `String(document.querySelectorAll('#feed [data-action="$more"]').length)`); ещё != "0" {
		t.Fatalf("в ленте есть «Показать ещё» (%s штук): прокрутка в начало нажмёт её сама, и жест будет не при чём", ещё)
	}

	// метка в окне: полная перезагрузка страницы её сотрёт, и это прямее
	// любого счётчика запросов — оболочку браузер мог взять из кэша работника
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__метка_жеста = 'на месте'`, nil)); err != nil {
		t.Fatal(err)
	}
	// Оболочку считаем с этого места, а не с нуля: до сюда её просят дважды
	// законно — переходом браузера и служебным работником, который кладёт её
	// в кэш. Нам важен не счёт, а рост: от жеста он расти не должен вовсе.
	оболочекБыло := atomic.LoadInt64(&оболочек)

	раскрыть := `(() => { document.querySelectorAll('#feed details').forEach((д) => { д.open = true; }); return 'раскрыто'; })()`
	наверх := `(() => { const л = document.getElementById('feed'); л.scrollTop = 0; return String(л.scrollTop); })()`
	всередину := `(() => { const л = document.getElementById('feed'); л.scrollTop = 300; return String(л.scrollTop); })()`
	указатель := `(() => {
    const у = document.getElementById('pull-refresh');
    return у ? (у.getAttribute('data-view') || '?') + ':' + у.textContent : 'нет';
  })()`

	// ---- 1. в самом верху ленты жест обновляет экран ----
	_ = chromedp.Run(ctx, chromedp.Evaluate(наверх, nil), chromedp.Sleep(300*time.Millisecond))
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
    window.__касания = [];
    for (const и of ['touchstart', 'touchmove', 'touchend']) {
      document.addEventListener(и, (е) => {
        const т = (е.touches && е.touches[0]) || (е.changedTouches && е.changedTouches[0]);
        const л = document.getElementById('feed');
        window.__касания.push(и + '@' + (т ? Math.round(т.clientY) : '?')
          + ' цель=' + (е.target && е.target.tagName) + ' вЛенте=' + (л && л.contains(е.target))
          + ' scrollTop=' + (л ? Math.round(л.scrollTop) : '?'));
      }, { passive: true });
    }
    return true;
  })()`, nil))
	before := atomic.LoadInt64(&экранов)
	pullDown(t, ctx, 195, 300, 90)
	t.Logf("касания: %s", evaluate(t, ctx, `(window.__касания || []).join(' | ')`))
	t.Logf("указатель сразу после жеста: %s", evaluate(t, ctx, указатель))
	waitFor(t, ctx, `(() => {
    const у = document.getElementById('pull-refresh');
    return !!у && у.textContent.indexOf('Обновлено') >= 0;
  })()`, "жест в начале ленты не сказал «Обновлено»", 25*time.Second)
	if after := atomic.LoadInt64(&экранов); after <= before {
		t.Errorf("жест в начале ленты не спросил разметку экрана: было %d, стало %d", before, after)
	} else {
		t.Logf("жест в начале ленты: запросов экрана %d → %d", before, after)
	}
	чисто("жест сработал")

	// «Обновлено» держится 600 мс и сворачивается само — закрывать нечего
	waitFor(t, ctx, `document.getElementById('pull-refresh') === null`,
		"указатель после удачи не свернулся сам", 10*time.Second)

	// ---- 2. посреди списка тот же жест не обновляет ничего ----
	// разделы после подмены ленты снова свёрнуты — раскрываем заново, иначе
	// прокручивать опять нечего
	_ = evaluate(t, ctx, раскрыть)
	time.Sleep(300 * time.Millisecond)
	if got := evaluate(t, ctx, всередину); got == "0" {
		t.Fatalf("лента не прокрутилась в середину: scrollTop=%s", got)
	}
	before = atomic.LoadInt64(&экранов)
	pullDown(t, ctx, 195, 300, 90)
	time.Sleep(2 * time.Second)
	if after := atomic.LoadInt64(&экранов); after != before {
		t.Errorf("посреди списка жест обновил экран: было %d, стало %d — это должна быть обычная прокрутка к старым сообщениям", before, after)
	}
	if kind := evaluate(t, ctx, указатель); kind != "нет" {
		t.Errorf("посреди списка появился указатель обновления: %s", kind)
	}
	чисто("жест посреди списка")

	// ---- 3. при открытой шторке меню жест выключен вовсе ----
	_ = chromedp.Run(ctx,
		chromedp.Evaluate(наверх, nil),
		chromedp.Evaluate(`sema.panel('menu', true)`, nil),
		chromedp.Sleep(400*time.Millisecond))
	before = atomic.LoadInt64(&экранов)
	pullDown(t, ctx, 195, 300, 90)
	time.Sleep(2 * time.Second)
	if after := atomic.LoadInt64(&экранов); after != before {
		t.Errorf("при открытой шторке жест обновил экран: было %d, стало %d — там человек занят другим", before, after)
	}
	if kind := evaluate(t, ctx, указатель); kind != "нет" {
		t.Errorf("при открытой шторке появился указатель обновления: %s", kind)
	}
	_ = chromedp.Run(ctx, chromedp.Evaluate(`sema.panel('menu', false)`, nil), chromedp.Sleep(300*time.Millisecond))
	чисто("жест при открытой шторке")

	// ---- 4. отпустили раньше порога — не случилось ничего ----
	_ = chromedp.Run(ctx, chromedp.Evaluate(наверх, nil), chromedp.Sleep(300*time.Millisecond))
	before = atomic.LoadInt64(&экранов)
	pullDown(t, ctx, 195, 300, 30) // 30 < порога 60
	time.Sleep(2 * time.Second)
	if after := atomic.LoadInt64(&экранов); after != before {
		t.Errorf("отпускание раньше порога обновило экран: было %d, стало %d", before, after)
	}
	if kind := evaluate(t, ctx, указатель); kind != "нет" {
		t.Errorf("после отпускания раньше порога указатель остался: %s", kind)
	}

	// ---- 5. неудачу видно так же прямо, как удачу ----
	// Молчание после жеста неотличимо от «ничего не изменилось»: человек тянет
	// снова и снова, не понимая, что связи нет.
	_ = evaluate(t, ctx, раскрыть)
	_ = chromedp.Run(ctx, chromedp.Evaluate(наверх, nil), chromedp.Sleep(300*time.Millisecond))
	стоп() // Сёма перестал отвечать — так связь и рвётся на самом деле
	waitFor(t, ctx, `(async () => {
    try { await fetch('/api/health', {cache: 'no-store'}); return false; } catch (e) { return true; }
  })()`, "Сёма всё ещё отвечает — рвать связь не получилось", 20*time.Second)
	pullDown(t, ctx, 195, 300, 90)
	waitFor(t, ctx, `(() => {
    const у = document.getElementById('pull-refresh');
    return !!у && у.textContent.indexOf('Не получилось') >= 0;
  })()`, "жест без связи промолчал: человек не отличит неудачу от «ничего не изменилось»", 30*time.Second)
	t.Logf("указатель при неудаче: %s", evaluate(t, ctx, указатель))
	// и сворачивается сам, как галочка, только держится дольше — фразу с
	// причиной нужно успеть прочитать
	waitFor(t, ctx, `document.getElementById('pull-refresh') === null`,
		"указатель после неудачи не свернулся сам", 10*time.Second)
	чисто("жест без связи")

	// ---- 6. страница целиком не перезагружалась ни разу ----
	if метка := evaluate(t, ctx, `window.__метка_жеста || 'стёрта'`); метка != "на месте" {
		t.Errorf("страница перезагрузилась целиком: метка в окне %q", метка)
	}
	if n := atomic.LoadInt64(&оболочек); n != оболочекБыло {
		t.Errorf("за время жестов оболочку качали ещё %d раз — родное потягивание браузера не погашено", n-оболочекБыло)
	}
	t.Logf("оболочку качали раз: %d (до жестов %d), запросов экрана всего: %d",
		atomic.LoadInt64(&оболочек), оболочекБыло, atomic.LoadInt64(&экранов))
	чисто("конец")
}
