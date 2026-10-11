package api

// Вход фразой в настоящем браузере: страница `/login` — рукописный JS,
// которого не видит ни один тест на Go. Тест жмёт кнопку и набирает фразу,
// как человек, а не зовёт ручку: «зелено, а сломано» здесь уже было.
//
// Chrome нет — тест пропускается: SEMA_BROWSER=1 go test ./internal/api -run Браузер

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/auth"
)

func TestBrowserLoginByPhrase(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	// вход без проверки личности выключен: иначе браузер с loopback попал
	// бы внутрь и без фразы, и жест ничего бы не проверял
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	фраза, err := s.auth.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	// passkey и адрес входа живут на именах, а не на числах: страница сама
	// уводит с «127.0.0.1» на адрес из настроек, и по IP тест зацикливался
	addr := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	if err := st.SetSetting(auth.SettingOrigin, addr); err != nil {
		t.Fatal(err)
	}

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()

	var exceptions []string
	chromedp.ListenTarget(ctx, func(ev any) {
		e, ok := ev.(*runtime.EventExceptionThrown)
		if !ok {
			return
		}
		// этот тест отвечает за страницу входа, а её скрипт живёт в самой
		// странице. Беды оболочки интерфейса (app.js, htmx) ловят её
		// собственные тесты — здесь они сбивали бы с толку
		text := e.ExceptionDetails.Error()
		if strings.Contains(text, "app.js") || strings.Contains(text, "htmx.js") {
			return
		}
		exceptions = append(exceptions, text)
	})

	if err := chromedp.Run(ctx, chromedp.Navigate(addr+"/login")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу входа: %v", err)
	}
	// кнопка фразы появляется только когда фраза заведена
	var видна bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const т = document.getElementById('phraseToggle'); return !!т && т.style.display !== 'none'; })()`,
		&видна, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !видна {
		t.Fatalf("кнопки входа фразой нет: %v", err)
	}
	var подсказка string
	if err := chromedp.Run(ctx,
		chromedp.Click("#phraseShow", chromedp.ByID),
		chromedp.SendKeys("#phrase", фраза, chromedp.ByID),
		chromedp.Text("#phraseNote", &подсказка, chromedp.ByID, chromedp.NodeVisible),
	); err != nil {
		t.Fatalf("набрать фразу: %v", err)
	}
	// из своей сети фраза полноценна, и подсказка говорит именно это
	if !strings.Contains(подсказка, "свою сеть") && !strings.Contains(подсказка, "своей сетью") {
		t.Fatalf("подсказка не объясняет, что даёт фраза здесь: %q", подсказка)
	}
	if err := chromedp.Run(ctx, chromedp.Click("#phraseGo", chromedp.ByID)); err != nil {
		t.Fatalf("нажать «Войти фразой»: %v", err)
	}
	// из своей сети сессия полноценная, поэтому страница уводит внутрь —
	// то есть сама уходит на другой документ: опрос в старом падает с
	// «target navigated», и ждать надо оболочку интерфейса
	if err := chromedp.Run(ctx, chromedp.WaitReady("#screen", chromedp.ByID)); err != nil {
		var ошибка string
		_ = chromedp.Run(ctx, chromedp.Text("#err", &ошибка, chromedp.ByID))
		t.Fatalf("вход фразой не открыл Сёму: %v, на странице «%s»", err, ошибка)
	}
	if len(exceptions) > 0 {
		t.Fatalf("исключения на странице входа: %v", exceptions)
	}
}
