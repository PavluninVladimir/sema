package api

// Снасть для проверки «без сети» в настоящем Chrome (нужна изменению
// offline-snapshot: задачи 4.5 «сеть выключена — виден прежний экран с
// шапкой снимка» и 5.5 «написал без связи, перезагрузил страницу, связь
// появилась — сообщение ушло один раз»).
//
// Одного Network.overrideNetworkState (он же то, что раньше называлось
// Network.emulateNetworkConditions с offline: true) НЕ хватает: он честно
// переключает navigator.onLine у страницы, но сами запросы по сети
// проходят как ни в чём не бывало — проверено дымовым тестом ниже. Реальную
// блокировку даёт Network.setBlockedURLs с образцом на все схемы, хосты и
// порты; без него «сеть выключена» была бы неправдой, которую ничто не
// проверяет.
//
// Про служебного работника (sw.js): он кэширует только оболочку и явно
// пропускает /api/, /ui, /do, /upload — эти пути ответит из кэша только
// после провала сетевого запроса (см. internal/web/static/sw.js). Значит
// сам факт, что страница открылась без сети, ничего не доказывает — она
// вполне может открыться из кэша работника, а доказывает только провалившийся
// запрос к серверу (`/api/health` в снасти ниже — он в списке пропускаемых
// путей и из кэша не отвечает никогда).

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// patternBlockAll — синтаксис URLPattern (не старый глоб), поэтому
// схема, хост и порт должны быть перечислены явно: одна лишь "*" не
// разбирается вовсе, а "*://*/*" без явного порта не совпадает с адресом
// httptest-сервера, у которого порт всегда нестандартный — оба варианта
// проверены и отброшены дымовым тестом раньше этой строки.
const patternBlockAll = "*://*:*/*"

// disableNetwork — страница считает себя офлайн (navigator.onLine === false)
// и любой сетевой запрос с неё проваливается настоящим сетевым отказом
// (fetch отвергается TypeError'ом), а не просто зависает.
func disableNetwork(ctx context.Context) error {
	return chromedp.Run(ctx,
		network.Enable(),
		network.OverrideNetworkState(true, 0, -1, -1),
		network.SetBlockedURLs().WithURLPatterns([]*network.BlockPattern{
			{URLPattern: patternBlockAll, Block: true},
		}),
	)
}

// enableNetwork — снимает и блокировку, и признак офлайна.
func enableNetwork(ctx context.Context) error {
	return chromedp.Run(ctx,
		network.OverrideNetworkState(false, 0, -1, -1),
		network.SetBlockedURLs(),
	)
}

// fetchAttempt — со страницы, живым fetch'ем, без кэша браузера: чтобы
// снасть не соврала себе, спрашиваем не «открылась ли страница» (её отдаст
// и служебный работник из кэша), а прошёл ли настоящий запрос к серверу.
const scriptFetch = `(async () => {
  try { const о = await fetch('/api/health', {cache: 'no-store'}); return 'ok ' + о.status; }
  catch (e) { return 'fail ' + e; }
})()`

func fetchAttempt(ctx context.Context) (string, error) {
	var итог string
	err := chromedp.Run(ctx, chromedp.Evaluate(scriptFetch, &итог,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	return итог, err
}

// Дымовой тест снасти: страница загружена при живой сети (обычный fetch
// проходит), сеть выключается (тот же fetch к тому же адресу отвергается),
// сеть включается обратно (fetch снова проходит). Без этой проверки снасть
// могла бы молча ничего не блокировать — Network.overrideNetworkState как
// раз так себя и вёл, пока к нему не добавили Network.setBlockedURLs.
func TestBrowserNetworkCanBeDisabledAndEnabled(t *testing.T) {
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
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()

	var exceptions []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			exceptions = append(exceptions, e.ExceptionDetails.Error())
		}
	})

	// настоящая страница Сёмы, а не голая заглушка: тем же путём пойдут
	// задачи 4.5 и 5.5, а служебный работник в ней и правда регистрируется
	// (страница отдаётся с loopback-адреса, а его Chrome считает надёжным
	// происхождением наравне с https)
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen=inbox")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === 'inbox'; })()`,
		&ready, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !ready {
		t.Fatalf("экран не нарисовался: %v", err)
	}

	// служебный работник регистрируется сам после первой отрисовки — ждём
	// его активации, чтобы дымовой тест видел ту же обстановку, в которой
	// будут работать задачи 4.5 и 5.5, а не пустую страницу без него
	var работникЕсть bool
	_ = chromedp.Run(ctx, chromedp.Evaluate(
		`(async () => { const р = await navigator.serviceWorker.ready.catch(() => null); return !!(р && р.active); })()`,
		&работникЕсть, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	t.Logf("служебный работник активен: %v", работникЕсть)

	доВыключения, err := fetchAttempt(ctx)
	if err != nil {
		t.Fatalf("fetch при живой сети: %v", err)
	}
	if доВыключения != "ok 200" {
		t.Fatalf("fetch при живой сети должен был пройти, а получилось %q — снасть проверяет офлайн на сломанном фундаменте", доВыключения)
	}

	if err := disableNetwork(ctx); err != nil {
		t.Fatalf("disableNetwork: %v", err)
	}
	var онлайн bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`navigator.onLine`, &онлайн)); err != nil {
		t.Fatalf("navigator.onLine: %v", err)
	}
	if онлайн {
		t.Error("navigator.onLine всё ещё true после disableNetwork")
	}
	офлайн, err := fetchAttempt(ctx)
	if err != nil {
		t.Fatalf("fetch без сети: %v", err)
	}
	if !strings.HasPrefix(офлайн, "fail ") {
		t.Fatalf("сеть должна была отказать, а fetch без сети вернул %q — снасть врёт, что сеть выключена", офлайн)
	}
	t.Logf("fetch без сети отвергнут: %s", офлайн)

	if err := enableNetwork(ctx); err != nil {
		t.Fatalf("enableNetwork: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`navigator.onLine`, &онлайн)); err != nil {
		t.Fatalf("navigator.onLine: %v", err)
	}
	if !онлайн {
		t.Error("navigator.onLine всё ещё false после enableNetwork")
	}
	снова, err := fetchAttempt(ctx)
	if err != nil {
		t.Fatalf("fetch после включения сети: %v", err)
	}
	if снова != "ok 200" {
		t.Fatalf("после enableNetwork fetch должен был снова пройти, получилось %q", снова)
	}

	if len(exceptions) > 0 {
		t.Errorf("исключения в консоли браузера:\n%s", strings.Join(exceptions, "\n"))
	}
}
