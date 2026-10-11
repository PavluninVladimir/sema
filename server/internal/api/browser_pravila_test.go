package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Правила доступа настоящими нажатиями в Chrome: сегмент у инструмента →
// полоса «Изменено» → «Сохранить» → после перезагрузки экрана состояние на
// месте. Проверять вызовом функции из консоли бессмысленно: разметка бывает
// безупречной, а живое нажатие не работает. Ждём условий на странице, а не
// сроков: фиксированная пауза в таком тесте плавает.

// segmentXPath — вариант переключателя у поля по имени инструмента.
func segmentXPath(tool, word string) string {
	return `//form[contains(@class,'deferred')]//div[contains(@class,'field')][./label[normalize-space()='` + tool +
		`']]//label[contains(@class,'segment')][normalize-space()='` + word + `']`
}

// paintedJS — у варианта закрашен фон (выбранный), а не прозрачный.
func paintedJS(tool, word string) string {
	return `(() => { const f = Array.from(document.querySelectorAll('form.deferred .field')).find(
	    (p) => { const l = p.querySelector('label'); return l && l.textContent.trim() === '` + tool + `'; });
	  if (!f) return 'нет поля';
	  const s = Array.from(f.querySelectorAll('label.segment')).find((l) => l.textContent.trim() === '` + word + `');
	  if (!s) return 'нет варианта';
	  const c = getComputedStyle(s).backgroundColor;
	  return c === 'rgba(0, 0, 0, 0)' || c === 'transparent' ? 'прозрачный' : c; })()`
}

// checkedJS — какое слово выбрано у инструмента после перезагрузки.
func checkedJS(tool string) string {
	return `(() => { const f = Array.from(document.querySelectorAll('form.deferred .field')).find(
	    (p) => { const l = p.querySelector('label'); return l && l.textContent.trim() === '` + tool + `'; });
	  if (!f) return 'нет поля';
	  const r = f.querySelector('input[type=radio]:checked'); return r ? r.value : ''; })()`
}

func TestBrowserMCPRulesSegmentsSave(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, st := rulesMCPFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 120*time.Second)
	defer cancelTime()

	var ready bool
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 900),
		chromedp.Navigate(srv.URL+"/#screen=settings:integrations@sing~rules"),
		chromedp.Poll(`document.querySelectorAll('form.deferred .field-segments').length > 5`, &ready,
			chromedp.WithPollingTimeout(20*time.Second))); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("экран правил не нарисовался: %v", err)
	}

	// выбранный вариант закрашен, остальные — нет
	var warn, other string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(paintedJS("task_create", "Спросить"), &warn),
		chromedp.Evaluate(paintedJS("task_create", "Разрешить"), &other)); err != nil {
		t.Fatal(err)
	}
	if warn == "прозрачный" || other != "прозрачный" {
		t.Fatalf("цвет только у выбранного: «Спросить» %q, «Разрешить» %q", warn, other)
	}

	// нажали «Разрешить» у task_create: подсветка переехала сама, без перерисовки
	if err := chromedp.Run(ctx, chromedp.Click(segmentXPath("task_create", "Разрешить"), chromedp.BySearch)); err != nil {
		t.Fatalf("нажать «Разрешить»: %v", err)
	}
	var moved bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const p = document.getElementById('save-bar'); return !!p && !p.hidden; })()`, &moved,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil || !moved {
		t.Fatalf("полоса сохранения не появилась: %v", err)
	}
	var text, nowAllow, nowAsk string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelector('#save-bar .what').textContent`, &text),
		chromedp.Evaluate(paintedJS("task_create", "Разрешить"), &nowAllow),
		chromedp.Evaluate(paintedJS("task_create", "Спросить"), &nowAsk)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Изменено 1") || !strings.Contains(text, "task_create") ||
		!strings.Contains(text, "Спросить → Разрешить") {
		t.Errorf("полоса говорит %q — не названо, что изменилось", text)
	}
	if nowAllow == "прозрачный" || nowAsk != "прозрачный" {
		t.Errorf("подсветка не поехала за нажатием: «Разрешить» %q, «Спросить» %q", nowAllow, nowAsk)
	}
	// у изменённого поля стоит точка «изменено»
	var dot bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.querySelector('form.deferred .field.changed')`, &dot)); err != nil || !dot {
		t.Errorf("у изменённого поля нет точки «изменено»: %v", err)
	}

	// новый инструмент решается выбором — хоть «Запретить»
	var fresh string
	if err := chromedp.Run(ctx, chromedp.Evaluate(checkedJS("task_brand_new"), &fresh)); err != nil || fresh != "" {
		t.Fatalf("у нового инструмента что-то выбрано до решения: %q %v", fresh, err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(segmentXPath("task_brand_new", "Запретить"), chromedp.BySearch)); err != nil {
		t.Fatalf("нажать «Запретить» у нового: %v", err)
	}
	var two bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`document.querySelector('#save-bar .what').textContent.indexOf('Изменено 2') >= 0`, &two,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil || !two {
		t.Fatalf("полоса не насчитала две правки: %v", err)
	}

	// поиск в шапке не перечитывает экран под несохранённым: выбор остаётся,
	// а полоса мигает — видно, чего она ждёт
	var guarded bool
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#search`, "task_c", chromedp.ByQuery),
		chromedp.Poll(`document.getElementById('save-bar').classList.contains('attention')`, &guarded,
			chromedp.WithPollingTimeout(10*time.Second))); err != nil || !guarded {
		t.Fatalf("поиск под несохранённым не остановлен: %v", err)
	}
	var still string
	if err := chromedp.Run(ctx, chromedp.Evaluate(checkedJS("task_create"), &still)); err != nil || still != "Разрешить" {
		t.Fatalf("несохранённый выбор пропал при поиске: %q %v", still, err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('search').value = ''`, nil)); err != nil {
		t.Fatal(err)
	}

	// «Сохранить»: полоса гаснет, на сервере правила записаны
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	var saved bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const p = document.getElementById('save-bar'); return !!p && p.hidden; })()`, &saved,
		chromedp.WithPollingTimeout(20*time.Second))); err != nil || !saved {
		var what string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.querySelector('.not-saved:not([hidden])')||{}).textContent||''`, &what))
		t.Fatalf("сохранение не прошло (%v), у формы написано %q", err, what)
	}
	m, _ := st.MCPServer("sing")
	if m.Rules["task_create"] != store.RuleAllow || m.Rules["task_brand_new"] != store.RuleDeny {
		t.Fatalf("на сервере не то, что нажали: %v", m.Rules)
	}

	// перезагрузка экрана: состояние на месте, новый инструмент больше не новый
	var after, freshAfter string
	if err := chromedp.Run(ctx,
		chromedp.Reload(),
		chromedp.Poll(`document.querySelectorAll('form.deferred .field-segments').length > 5`, &ready,
			chromedp.WithPollingTimeout(20*time.Second)),
		chromedp.Evaluate(checkedJS("task_create"), &after),
		chromedp.Evaluate(checkedJS("task_brand_new"), &freshAfter)); err != nil {
		t.Fatal(err)
	}
	if after != "Разрешить" || freshAfter != "Запретить" {
		t.Fatalf("после перезагрузки: task_create %q, task_brand_new %q", after, freshAfter)
	}

	// фильтр по состоянию: на экране только «Спросить»
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'filters-bar')]//a[normalize-space()='Спросить']`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать фильтр: %v", err)
	}
	var only bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const f = Array.from(document.querySelectorAll('form.deferred[data-form^="mcp-rules-sing-"] .field')).filter((p) => p.querySelector('.field-segments') && p.querySelector('label').textContent.trim().indexOf('Всем') < 0);
		  return f.length > 0 && f.every((p) => { const r = p.querySelector('input[type=radio]:checked'); return r && r.value === 'Спросить'; }); })()`,
		&only, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !only {
		t.Fatalf("фильтр «Спросить» оставил чужое: %v", err)
	}
}

func TestBrowserSSHRulesAddRemoveAndTest(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, st := rulesSSHFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 120*time.Second)
	defer cancelTime()

	var ready bool
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 900),
		chromedp.Navigate(srv.URL+"/#screen=settings:servers~rules:первый"),
		chromedp.Poll(`document.querySelectorAll('.pattern-col').length === 3`, &ready,
			chromedp.WithPollingTimeout(20*time.Second))); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("вкладка правил не нарисовалась: %v", err)
	}

	// добавить образец в «Разрешить»
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`.pattern-col.ok .pat-add input`, "free", chromedp.ByQuery),
		chromedp.Click(`.pattern-col.ok .pat-add button`, chromedp.ByQuery)); err != nil {
		t.Fatalf("добавить образец: %v", err)
	}
	var added bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`Array.from(document.querySelectorAll('.pattern-col.ok .pat > span:first-child')).some((x) => x.textContent === 'free')`,
		&added, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !added {
		var dump string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.querySelector('.error')||{}).textContent + ' | ' + document.querySelector('.pattern-col.ok').outerHTML.slice(0, 1500)`, &dump))
		t.Fatalf("образец не появился в колонке: %v\n%s\nв базе: %v", err, dump, st.HostRuleSet("первый").Allow)
	}
	if got, _ := s.eng.Classify("первый", "free -m"); got != "allow" {
		t.Fatalf("добавленное правило не действует: %q", got)
	}

	// проверить команду: ответ и подсветка сработавшего образца
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`form input[name="cmd"]`, "systemctl restart nginx", chromedp.ByQuery),
		chromedp.Click(`//form[.//input[@name='cmd']]//button[normalize-space()='Проверить']`, chromedp.BySearch)); err != nil {
		t.Fatalf("проверить команду: %v", err)
	}
	var verdict string
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const hit = document.querySelector('.pattern-col.warn .pat.hit'); return hit ? hit.textContent.replace('×','').trim() : ''; })()`,
		&verdict, chromedp.WithPollingTimeout(15*time.Second))); err != nil || verdict != "systemctl restart" {
		t.Fatalf("сработавший образец не подсвечен: %q %v", verdict, err)
	}

	// убрать образец «×»
	if err := chromedp.Run(ctx, chromedp.Click(
		`//div[contains(@class,'pattern-col') and contains(@class,'ok')]//span[contains(@class,'pat')][./span[text()='free']]/button`, chromedp.BySearch)); err != nil {
		t.Fatalf("убрать образец: %v", err)
	}
	var gone bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!Array.from(document.querySelectorAll('.pattern-col.ok .pat > span:first-child')).some((x) => x.textContent === 'free')`,
		&gone, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !gone {
		t.Fatalf("образец не исчез: %v", err)
	}
	for _, r := range st.HostRules("первый") {
		if r.Pattern == "free" {
			t.Fatal("образец остался в базе")
		}
	}
}
