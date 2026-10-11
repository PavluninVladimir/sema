package api

// Навигация по настройкам — последовательными нажатиями в настоящем Chrome.
//
// 12 сентября 2026 приёмка нашла: после нажатия на вкладку «Поведение»
// вся навигация вставала. Вкладок настроек с 1 октября 2026 нет — их
// заменили заголовки групп в колонке разделов, — но беда живёт не во
// вкладке, а между экранами, и цепочка осталась. Сборка каждого экрана по отдельности была
// зелёной, а цепочка из двух нажатий — нет: первое запоминало кириллический
// адрес вкладки как текущий экран, второе уносило его заголовком HTTP и
// падало в браузере ошибкой «non ISO-8859-1 code point». Ни один тест на
// сборку экрана этого не увидел бы: беда живёт между экранами.
//
// Поэтому здесь не один экран, а цепочки, и после каждого нажатия три
// условия: сменились адрес в строке браузера, data-screen и выбранный
// пункт; содержимое стало другим; консоль молчит.

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
)

// navStep — одно нажатие и что после него должно быть.
type navStep struct {
	What   string // как назвать в отчёте
	To     string // XPath нажимаемого
	Screen string // data-screen после
	At     string // XPath элемента, который должен стать выбранным
	Name   string // его текст
}

func sectionOf(name string) string {
	return `//div[@id='panel']//button[contains(@class,'item') and contains(normalize-space(.),'` + name + `')]`
}
func subtabOf(name string) string {
	return `//div[contains(@class,'subtabs-bar')]//a[@role='tab' and contains(normalize-space(.),'` + name + `')]`
}

const sectionSelected = `//div[@id='panel']//button[contains(@class,'item') and contains(@class,'selected')]`
const subtabSelected = `//div[contains(@class,'subtabs-bar')]//a[@role='tab' and @aria-selected='true']`

func TestBrowserSettingsChains(t *testing.T) {
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
	ctx, cancelTime := context.WithTimeout(ctx, 180*time.Second)
	defer cancelTime()

	// консоль: исключения и console.error — всё, что человек увидел бы в
	// DevTools красным. Ошибка заголовка приходит именно сюда
	var консоль []string
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			консоль = append(консоль, "исключение: "+e.ExceptionDetails.Error())
		case *runtime.EventConsoleAPICalled:
			if e.Type == runtime.APITypeError {
				var части []string
				for _, a := range e.Args {
					части = append(части, string(a.Value))
				}
				консоль = append(консоль, "console.error: "+strings.Join(части, " "))
			}
		}
	})

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate(srv.URL+"/#screen=settings")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, "settings")
	// htmx:error — своя ошибка htmx, в консоль она не попадает: собираем сами
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`window.__ошибки = []; document.body.addEventListener('htmx:error', (e) => {
			const д = e.detail || {}; window.__ошибки.push('htmx:error ' + String(д.error || д.message || д.xhr && д.xhr.status || ''));
		}); true`, nil)); err != nil {
		t.Fatal(err)
	}

	шаги := []navStep{
		// Обзор → «Модели и роли» → «Проблемы», дальше по колонке через все
		// четыре группы
		{"Обзор → Модели и роли", sectionOf("Модели и роли"), modelsScreen, sectionSelected, "Модели и роли"},
		{"Модели → Проблемы", subtabOf("Проблемы"), modelsScreen + "~" + modelsTabIssues, subtabSelected, "Проблемы"},
		{"→ Почта", sectionOf("Почта"), "settings:mail", sectionSelected, "Почта"},
		{"Почта → Telegram", sectionOf("Telegram"), tgScreen, sectionSelected, "Telegram"},
		{"→ Серверы", sectionOf("Серверы"), serversScreen, sectionSelected, "Серверы"},
		{"Серверы → Инструменты", sectionOf("Инструменты"), "settings:tools", sectionSelected, "Инструменты"},
		{"→ Вход и доступ", sectionOf("Вход и доступ"), accessScreen, sectionSelected, "Вход и доступ"},
		{"Доступ → Доверенная сеть", subtabOf("Доверенная сеть"), accessScreen + "~" + accessTabNetwork, subtabSelected, "Доверенная сеть"},
		{"→ Обзор", sectionOf("Обзор"), "settings", sectionSelected, "Обзор"},
	}
	for _, ш := range шаги {
		before := screenFingerprint(t, ctx)
		if err := chromedp.Run(ctx, chromedp.Click(ш.To, chromedp.BySearch)); err != nil {
			t.Fatalf("%s: нажать не вышло: %v", ш.What, err)
		}
		waitScreen(t, ctx, ш.Screen)
		// адрес в строке браузера — тот же экран, закодированный
		var хэш string
		if err := chromedp.Run(ctx, chromedp.Poll(
			`location.hash === '#screen=' + encodeURIComponent(`+jsString(ш.Screen)+`)`,
			nil, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
			_ = chromedp.Run(ctx, chromedp.Evaluate(`location.hash`, &хэш))
			t.Errorf("%s: в адресе %q, а экран %q", ш.What, хэш, ш.Screen)
		}
		// содержимое сменилось
		if after := screenFingerprint(t, ctx); after == before {
			t.Errorf("%s: экран назван %q, а содержимое то же самое", ш.What, ш.Screen)
		}
		// выбранный пункт помечен — и это тот, что нажали
		var text string
		if err := chromedp.Run(ctx, chromedp.Text(ш.At, &text, chromedp.BySearch)); err != nil {
			t.Errorf("%s: выбранного пункта нет: %v", ш.What, err)
		} else if !strings.Contains(text, ш.Name) {
			t.Errorf("%s: выбранным помечен %q, а нажимали %q", ш.What, strings.TrimSpace(text), ш.Name)
		}
	}

	// Экран с кириллицей в адресе — журнал сервера «хозяин». Следующее
	// нажатие унесёт этот адрес заголовком HX-Current-Screen: без
	// кодирования браузер уронит запрос ещё до сервера ошибкой
	// «non ISO-8859-1 code point». Заходим по прежнему адресу («~log:хозяин»):
	// он переадресуется на нынешний, и тот остаётся кириллическим законно
	кириллический := entityAddr(serversScreen, "хозяин", serversTabLog)
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen="+serversScreen+"~log:хозяин")); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, кириллический)
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`window.__ошибки = window.__ошибки || []; document.body.addEventListener('htmx:error', (e) => {
			const д = e.detail || {}; window.__ошибки.push('htmx:error ' + String(д.error || д.message || ''));
		}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(sectionOf("Почта"), chromedp.BySearch)); err != nil {
		t.Fatalf("с журнала сервера на «Почту»: %v", err)
	}
	waitScreen(t, ctx, "settings:mail")

	// после всего обхода — ни одной ошибки ни в консоли, ни у htmx
	var htmxОшибки []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__ошибки`, &htmxОшибки)); err != nil {
		t.Fatal(err)
	}
	for _, о := range append(консоль, htmxОшибки...) {
		t.Errorf("после обхода вкладок: %s", о)
	}
	for _, о := range консоль {
		if strings.Contains(о, "ISO-8859-1") {
			t.Errorf("заголовок с кириллицей снова уронил запрос: %s", о)
		}
	}
}

// waitScreen — пока data-screen не станет ожидаемым.
func waitScreen(t *testing.T, ctx context.Context, screen string) {
	t.Helper()
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === `+jsString(screen)+`; })()`,
		&ready, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		var after string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('screen')||{dataset:{}}).dataset.screen||''`, &after))
		t.Fatalf("ждали экран %q, а открыт %q (%v)", screen, after, err)
	}
}

// screenFingerprint — чтобы увидеть, что содержимое действительно сменилось,
// а не только подпись на секции.
func screenFingerprint(t *testing.T, ctx context.Context) string {
	t.Helper()
	var о string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const h = document.getElementById('screen').innerHTML; let x = 0;
		   for (let i = 0; i < h.length; i++) x = (x * 31 + h.charCodeAt(i)) | 0;
		   return h.length + ':' + x; })()`, &о)); err != nil {
		t.Fatal(err)
	}
	return о
}

var _ = fmt.Sprintf
