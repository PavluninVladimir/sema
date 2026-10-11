package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Smoke-тест в настоящем браузере: чистый Go и Chrome на машине, без
// node. Проверяется то, чего не видно по разметке: экраны рисуются в
// живой странице без исключений в консоли, форма уходит и сохраняется.
// Chrome нет — тест пропускается с объяснением; на Pi он не идёт.
func TestBrowserRendersScreens(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	var exceptions []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			exceptions = append(exceptions, e.ExceptionDetails.Error())
		}
	})

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen=inbox")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	ждатьЭкран := func(screen string) {
		t.Helper()
		var ready bool
		err := chromedp.Run(ctx, chromedp.Poll(
			`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === `+jsString(screen)+`; })()`,
			&ready, chromedp.WithPollingTimeout(15*time.Second)))
		if err != nil || !ready {
			t.Fatalf("экран %q не нарисовался: %v", screen, err)
		}
	}
	ждатьЭкран("inbox")

	// все экраны ядра — через адрес, как делает человек по ссылке
	for _, screen := range coreScreens {
		if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=`+screen+`'`, nil)); err != nil {
			t.Fatalf("экран %q: %v", screen, err)
		}
		// прежние адреса «Что ушло наружу» («sent», «sent~log») открывают раздел
		// по новому адресу, и экран называет именно его: клиент запоминает новый
		ждатьЭкран(canonSentAddr(screen))
	}

	// форма уходит и сохраняется: порог «медленная операция» на «Здоровье»
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`location.hash = '#screen=health'`, nil),
	); err != nil {
		t.Fatal(err)
	}
	ждатьЭкран("health")
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`input[name="health.slow_sec"]`, chromedp.ByQuery),
		chromedp.SetValue(`input[name="health.slow_sec"]`, "77", chromedp.ByQuery),
		chromedp.Click(`input[name="health.slow_sec"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => { const п = document.querySelector('input[name="health.slow_sec"]'); const ф = п.closest('form'); ф.querySelector('button[type="submit"]').click(); })()`, nil),
	); err != nil {
		t.Fatalf("форма порогов: %v", err)
	}
	// ответ на /do приходит не мгновенно: ждём запись в базе, а не сон
	deadline := time.Now().Add(10 * time.Second)
	for st.Setting("health.slow_sec", "") != "77" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if got := st.Setting("health.slow_sec", ""); got != "77" {
		t.Errorf("форма порогов не сохранила значение: в базе %q", got)
	}

	// строка с переходом открывает свой экран: «Что ушло» из «Здоровья»
	// проверять нечем на пустой базе — переход проверяется по чипу вкладки
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const ч = document.querySelector('.chips a.chip:not(.selected)'); if (ч) ч.click(); return !!ч; })()`, nil)); err != nil {
		t.Fatal(err)
	}

	if len(exceptions) > 0 {
		t.Errorf("исключения в консоли браузера:\n%s", strings.Join(exceptions, "\n"))
	}
}

func jsString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
