package api

// Сквозной обход: все основные пункты меню и все разделы настроек,
// последовательными нажатиями, на десктопе и на телефоне.
//
// После каждого нажатия три условия — сменились адрес в строке браузера,
// data-screen и выбранный пункт; и три числа — сколько шёл переход, сколько
// узлов и байт приехало. Переход дольше 500 мс — предупреждение, дольше
// секунды в локальной сети — ошибка. После обхода консоль обязана быть
// чистой: ноль error, список warn прикладывается к итогу. Числа ложатся в
// testdata/обход.json, и следующий прогон показывает рост относительно
// прошлого.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type transitionMetric struct {
	Ms    int `json:"мс"`
	Nodes int `json:"узлов"`
	Bytes int `json:"байт"`
}

const (
	transitionWarnMs = 500
	transitionErrMs  = 1000
)

func TestBrowserEndToEndWalkthrough(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	отчёт := map[string]map[string]transitionMetric{}
	for _, окно := range []struct {
		имя     string
		ш, в    int64
		phoneUA bool
	}{{"десктоп 1290×806", 1290, 806, false}, {"телефон 390×844", 390, 844, true}} {
		отчёт[окно.имя] = traverseScreens(t, srv, окно.ш, окно.в, окно.phoneUA)
	}
	compareAndSave(t, отчёт)
}

// traverseScreens — один обход в одном окне.
func traverseScreens(t *testing.T, srv *httptest.Server, ш, в int64, телефон bool) map[string]transitionMetric {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 240*time.Second)
	defer cancelTime()

	var ошибки, предупреждения []string
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			ошибки = append(ошибки, "исключение: "+e.ExceptionDetails.Error())
		case *runtime.EventConsoleAPICalled:
			var части []string
			for _, a := range e.Args {
				части = append(части, string(a.Value))
			}
			switch e.Type {
			case runtime.APITypeError:
				ошибки = append(ошибки, "console.error: "+strings.Join(части, " "))
			case runtime.APITypeWarning:
				предупреждения = append(предупреждения, strings.Join(части, " "))
			}
		}
	})
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(ш, в),
		chromedp.Navigate(srv.URL+"/#screen=inbox")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, "inbox")
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__ошибки = []; document.body.addEventListener('htmx:error', (e) => {
		const д = e.detail || {}; window.__ошибки.push('htmx:error ' + String(д.error || д.message || '')); }); true`, nil)); err != nil {
		t.Fatal(err)
	}

	меры := map[string]transitionMetric{}
	окно := "десктоп"
	if телефон {
		окно = "телефон"
	}
	// шаг — одно нажатие: открыть, чем нажать, чего ждать, где выбранное
	нажать := func(куда string) error {
		// у каждого нажатия свой срок: зависшее нажатие не должно съедать
		// бюджет всего обхода, как это было с невидимым пунктом в
		// свёрнутой группе меню
		к, отмена := context.WithTimeout(ctx, 10*time.Second)
		defer отмена()
		return chromedp.Run(к, chromedp.Click(куда, chromedp.BySearch))
	}
	шаг := func(что, куда, screen, where, name string) {
		t.Helper()
		начало := time.Now()
		if err := нажать(куда); err != nil {
			t.Errorf("%s · %s: нажать не вышло: %v", окно, что, err)
			return
		}
		var ок bool
		if err := chromedp.Run(ctx, chromedp.Poll(
			`(() => { const э = document.getElementById('screen'); return !!э && э.dataset.screen === `+jsString(screen)+`; })()`,
			&ок, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !ок {
			var after string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('screen')||{dataset:{}}).dataset.screen||''`, &after))
			t.Errorf("%s · %s: ждали экран %q, открыт %q (%v)", окно, что, screen, after, err)
			return
		}
		мс := int(time.Since(начало).Milliseconds())
		var хэшОк bool
		_ = chromedp.Run(ctx, chromedp.Poll(`location.hash === '#screen=' + encodeURIComponent(`+jsString(screen)+`)`,
			&хэшОк, chromedp.WithPollingTimeout(5*time.Second)))
		if !хэшОк {
			t.Errorf("%s · %s: адрес в строке браузера не сменился на %q", окно, что, screen)
		}
		if where != "" {
			// textContent, а не innerText: на телефоне шторка разделов
			// после выбора закрыта, и у скрытого узла innerText пуст
			var text string
			if err := chromedp.Run(ctx, chromedp.Evaluate(
				`(() => { const р = document.evaluate(`+jsString(where)+`, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue; return р ? (r => r.textContent)(р) : ''; })()`, &text)); err != nil || !strings.Contains(text, name) {
				t.Errorf("%s · %s: выбранным помечен %q, а нажимали %q", окно, что, strings.TrimSpace(text), name)
			}
		}
		var узлов, байт int
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('window').querySelectorAll('*').length`, &узлов))
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('window').innerHTML.length`, &байт))
		меры[screen] = transitionMetric{Ms: мс, Nodes: узлов, Bytes: байт}
		switch {
		case мс > transitionErrMs:
			t.Errorf("%s · %s: переход %d мс — дольше секунды", окно, что, мс)
		case мс > transitionWarnMs:
			t.Logf("%s · %s: переход %d мс — дольше полсекунды", окно, что, мс)
		}
	}

	// ---- основные пункты меню ----
	пункты := []struct{ экран, имя string }{
		{"notifications", "Уведомления"}, {"ask", "Чаты"}, {"files", "Файлы"}, {"tasks", "Задачи"},
		{"scripts", "Скрипты"}, {"modules", "Модули"}, {"memory", "Память"}, {"health", "Здоровье"},
		// у «Почты» с одним ящиком своей строки нет (она была бы двойником
		// ящика): почта открывается самим ящиком
		{"settings", "Настройки"}, {"inbox@проба", "Почта"},
	}
	for _, п := range пункты {
		if телефон {
			// меню на телефоне — шторка; открываем её кнопкой нижней полосы
			if err := нажать(`//*[@id='tabbar']//button[@data-panel='menu']`); err != nil {
				t.Fatalf("телефон: открыть меню: %v", err)
			}
			if err := chromedp.Run(ctx, chromedp.Poll(`document.getElementById('menu').classList.contains('open-sheet')`, nil,
				chromedp.WithPollingTimeout(5*time.Second))); err != nil {
				t.Fatalf("телефон: меню не открылось: %v", err)
			}
		}
		// пункт может лежать в свёрнутой группе («Настройки» — узел с
		// детьми): раскрываем её нажатием на заголовок группы, как человек
		var вГруппе bool
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => { const к = document.querySelector('#menu .item[hx-get="/ui?screen=`+п.экран+`"]');
			const д = к && к.closest('details.menu-node'); return !!(д && !д.open); })()`, &вГруппе))
		if вГруппе {
			if err := нажать(`//*[@id='menu']//details[contains(@class,'menu-node')][.//*[@hx-get='/ui?screen=` + п.экран + `']]/summary`); err != nil {
				t.Errorf("%s: раскрыть группу для %s: %v", окно, п.имя, err)
				continue
			}
		}
		// именно пункт списка: в шапке меню есть шестерёнка с тем же
		// адресом, на десктопе она скрыта, и нажатие в неё ждало бы вечно
		шаг("меню → "+п.имя, `//*[@id='menu']//*[contains(@class,'item') and @hx-get='/ui?screen=`+п.экран+`']`, п.экран,
			`//*[@id='menu']//*[contains(@class,'item') and contains(@class,'selected')]`, "")
	}

	// ---- все разделы настроек ----
	// обход меню кончается на «Почте», колонки настроек там нет — сначала в
	// настройки, тем же пунктом меню. Дальше на десктопе колонка разделов
	// (одна на все, без вкладок), а на телефоне — корень настроек: строка
	// раздела, потом «‹ Настройки» назад в корень
	if телефон {
		if err := нажать(`//*[@id='tabbar']//button[@data-panel='menu']`); err != nil {
			t.Fatalf("телефон: открыть меню: %v", err)
		}
	}
	шаг("меню → Настройки", `//*[@id='menu']//*[contains(@class,'item') and @hx-get='/ui?screen=settings']`, "settings", "", "")
	// строка корня (телефон) или пункт колонки (десктоп) — и обратно
	section := func(screen, name string) {
		if телефон {
			шаг("корень → "+name, `//*[@id='feed']//*[contains(@class,'row') and @data-goto='`+screen+`']`, screen, "", "")
			шаг(name+" → назад", `//nav[contains(@class,'crumbs')]//button[contains(@class,'crumb')]`, "settings", "", "")
			return
		}
		target := screen
		if screen == settingsOverview {
			target = "settings"
		}
		шаг("раздел "+name, sectionOf(name), target, sectionSelected, name)
	}
	section(settingsOverview, "Обзор")
	for _, group := range settingsGroups {
		for _, name := range group.Sections {
			section(name, sectionTitle[name])
		}
	}

	// полоска ожидания после обхода погашена: она бежала вечно, когда
	// htmx обрывал свой же запрос (13 сентября 2026)
	var want bool
	_ = chromedp.Run(ctx, chromedp.Poll(`!document.body.classList.contains('waiting')`, &want,
		chromedp.WithPollingTimeout(3*time.Second)))
	if !want {
		t.Errorf("%s: после обхода полоска ожидания не погасла", окно)
	}
	var htmxОшибки []string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`window.__ошибки`, &htmxОшибки))
	for _, о := range append(ошибки, htmxОшибки...) {
		t.Errorf("%s: после обхода в консоли ошибка: %s", окно, о)
	}
	if len(предупреждения) > 0 {
		t.Logf("%s: предупреждений в консоли %d:\n  %s", окно, len(предупреждения), strings.Join(предупреждения, "\n  "))
	}
	return меры
}

// compareAndSave — числа обхода против прошлого прогона и заново в файл.
func compareAndSave(t *testing.T, отчёт map[string]map[string]transitionMetric) {
	t.Helper()
	путь := filepath.Join("testdata", "обход.json")
	прежний := map[string]map[string]transitionMetric{}
	if b, err := os.ReadFile(путь); err == nil {
		_ = json.Unmarshal(b, &прежний)
	}
	var строки []string
	for окно, меры := range отчёт {
		var screens []string
		for э := range меры {
			screens = append(screens, э)
		}
		sort.Strings(screens)
		for _, э := range screens {
			м := меры[э]
			рост := ""
			if п, has := прежний[окно][э]; has && п.Nodes > 0 {
				if d := (м.Nodes - п.Nodes) * 100 / п.Nodes; d != 0 {
					рост = fmt.Sprintf("  узлов %+d %%", d)
				}
				if d := (м.Bytes - п.Bytes) * 100 / п.Bytes; d != 0 {
					рост += fmt.Sprintf("  байт %+d %%", d)
				}
			}
			строки = append(строки, fmt.Sprintf("%-18s %-30s %5d мс %6d узлов %7.1f КБ%s", окно, э, м.Ms, м.Nodes, float64(м.Bytes)/1024, рост))
		}
	}
	t.Logf("обход:\n%s", strings.Join(строки, "\n"))
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(отчёт, "", "  ")
	if err := os.WriteFile(путь, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
