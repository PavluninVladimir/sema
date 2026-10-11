package api

import (
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// «показать» у закрытого поля: настоящее нажатие делает поле обычным (в поле
// пароля macOS не даёт русскую раскладку — логин «мИванов» не набрать), а
// второе возвращает закрытое. Полоса сохранения набранное не выписывает и
// после «показать».
func TestBrowserSecureFieldShow(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := splashBrowser(t)
	screen := engine.SecretAddScreen("netscool:login")
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen="+url.QueryEscape(screen))); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!!document.querySelector('input[data-secure][name="value"]')`, &ok,
		chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		t.Fatalf("закрытого поля нет: %v", err)
	}
	var kind string
	if err := chromedp.Run(ctx,
		chromedp.Click(`input[data-secure][name="value"] + [data-show-secure]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('input[data-secure][name="value"]').type`, &kind)); err != nil {
		t.Fatal(err)
	}
	if kind != "text" {
		t.Fatalf("после «показать» поле %q", kind)
	}
	var label string
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`input[data-secure][name="value"]`, "мИванов", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-show-secure]').textContent`, &label)); err != nil {
		t.Fatal(err)
	}
	if label != "скрыть" {
		t.Errorf("подпись кнопки после «показать»: %q", label)
	}
	var bar string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('save-bar')||{}).textContent || ''`, &bar))
	if strings.Contains(bar, "мИванов") {
		t.Errorf("полоса сохранения выписала набранное: %q", bar)
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`[data-show-secure]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('input[data-secure][name="value"]').type`, &kind)); err != nil {
		t.Fatal(err)
	}
	if kind != "password" {
		t.Fatalf("второе нажатие не закрыло поле: %q", kind)
	}
}
