package api

// Приложение на телефоне: манифест глазами самого браузера.
//
// Свой разбор манифеста JSON-ом проверяет только то, что мы написали то,
// что хотели написать. А откроется ли Сёма отдельным окном без адресной
// строки, решает разборщик браузера — и он же молча отказывает, когда
// чего-то не хватает. Поэтому здесь манифест спрашивается у Chrome
// (`Page.getAppManifest`), а иконки не просто отдаются с кодом 200, а
// разбираются браузером в картинку: битый PNG отдался бы так же успешно,
// а на домашнем экране остался бы пустой значок.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestBrowserAppManifest(t *testing.T) {
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
	ctx, cancelTime := context.WithTimeout(ctx, 120*time.Second)
	defer cancelTime()

	var exceptions []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			exceptions = append(exceptions, e.ExceptionDetails.Error())
		}
	})

	var манифест *page.WebAppManifest
	var ошибкиМанифеста []*page.AppManifestError
	if err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL),
		chromedp.WaitVisible("#menu", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, ош, _, м, err := page.GetAppManifest().Do(ctx)
			ошибкиМанифеста, манифест = ош, м
			return err
		}),
	); err != nil {
		t.Fatalf("браузер: %v", err)
	}

	for _, о := range ошибкиМанифеста {
		if о.Critical != 0 {
			t.Errorf("браузер не принял манифест (строка %d): %s", о.Line, о.Message)
			continue
		}
		t.Logf("замечание браузера к манифесту (строка %d): %s", о.Line, о.Message)
	}
	if манифест == nil {
		t.Fatal("браузер не разобрал манифест вовсе")
	}
	// «Открывается без адресной строки» — это и есть display: standalone,
	// прочитанный браузером, а не нами. Значение приходит именем из
	// перечисления CDP: «kStandalone», а не «standalone»
	if режим := strings.TrimPrefix(манифест.Display, "k"); !strings.EqualFold(режим, "standalone") {
		t.Errorf("режим показа %q, ожидается standalone — иначе приложение откроется с адресной строкой", манифест.Display)
	}
	// short_name браузер в разобранном виде не отдаёт вовсе, поэтому его
	// проверяет тест манифеста в пакете web; здесь — только имя
	if манифест.Name == "" {
		t.Error("имя приложения пустое")
	}
	// Цвет строки состояния браузер берёт отсюда
	if манифест.ThemeColor == "" {
		t.Error("цвет темы не разобран браузером — строка состояния будет чужого цвета")
	}
	if len(манифест.Icons) < 4 {
		t.Errorf("браузер увидел %d иконок, ожидается 4", len(манифест.Icons))
	}

	// Каждая иконка должна не просто отдаться, а разобраться браузером в
	// картинку заявленного размера
	const js = `(async () => {
  const манифест = await (await fetch('/manifest.webmanifest')).json();
  const адреса = манифест.icons.map((и) => и.src);
  const ссылка = document.querySelector('link[rel="apple-touch-icon"]');
  if (ссылка) адреса.push(ссылка.getAttribute('href'));
  const итог = [];
  for (const адрес of адреса) {
    const ответ = await fetch(адрес);
    if (!ответ.ok) { итог.push({адрес, беда: 'код ' + ответ.status}); continue; }
    const тело = await ответ.blob();
    if (тело.size === 0) { итог.push({адрес, беда: 'пусто'}); continue; }
    if (адрес.endsWith('.svg')) { итог.push({адрес, ш: 0, в: 0}); continue; }
    try {
      const картинка = await createImageBitmap(тело);
      итог.push({адрес, ш: картинка.width, в: картинка.height});
    } catch (e) { итог.push({адрес, беда: 'не разбирается в картинку: ' + e}); }
  }
  return JSON.stringify(итог);
})()`
	var сырое string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &сырое,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
	)); err != nil {
		t.Fatalf("проверка иконок: %v", err)
	}
	var иконки []struct {
		Addr    string `json:"адрес"`
		Ш       int    `json:"ш"`
		В       int    `json:"в"`
		Trouble string `json:"беда"`
	}
	if err := json.Unmarshal([]byte(сырое), &иконки); err != nil {
		t.Fatalf("ответ браузера про иконки: %v (%s)", err, сырое)
	}
	размеры := map[string]string{}
	for _, и := range иконки {
		if и.Trouble != "" {
			t.Errorf("иконка %s: %s", и.Addr, и.Trouble)
			continue
		}
		if и.Ш > 0 {
			размеры[и.Addr] = fmt.Sprintf("%dx%d", и.Ш, и.В)
		}
	}
	want := map[string]string{
		"/icon-192.png":          "192x192",
		"/icon-512.png":          "512x512",
		"/icon-maskable-512.png": "512x512",
		"/icon-180.png":          "180x180", // apple-touch-icon: её берёт iOS
	}
	for addr, размер := range want {
		if размеры[addr] != размер {
			t.Errorf("иконка %s разобрана как %q, ожидается %s", addr, размеры[addr], размер)
		}
	}

	// Значок приложения: число даёт сервер атрибутом, а клиент не должен
	// на нём спотыкаться там, где браузер значков не метит
	var ждут string
	var значокЕсть bool
	if err := chromedp.Run(ctx,
		chromedp.AttributeValue("#menu", "data-waiting", &ждут, nil, chromedp.ByQuery),
		chromedp.Evaluate(`'setAppBadge' in navigator`, &значокЕсть),
	); err != nil {
		t.Fatalf("значок: %v", err)
	}
	if !strings.Contains(сырое, "/icon-180.png") {
		t.Error("apple-touch-icon не ведёт на /icon-180.png — iOS возьмёт вектор и нарисует пустой значок")
	}
	t.Logf("значок: data-waiting=%q, браузер метить значок %s", ждут, map[bool]string{true: "умеет", false: "не умеет"}[значокЕсть])

	if len(exceptions) > 0 {
		t.Errorf("исключения на странице: %s", strings.Join(exceptions, "; "))
	}
}
