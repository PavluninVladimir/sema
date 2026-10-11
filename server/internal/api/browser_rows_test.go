package api

// Строки настройки и «Тонкая настройка» в настоящем Chrome (1 октября 2026).
//
// Проверяем то, чем пользуется человек: раскрыл свёрнутую строку, вписал число
// в поле, увидел полосу сохранения, нажал «Сохранить», перезагрузил экран и
// нашёл число на месте, а подпись — «1 изменён». Ждём условий на странице, а не
// сроков; вызовом функции вместо жеста ничего не проверяем.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// rowsState — что видно у «Тонкой настройки» и её строк.
const rowsState = `(() => {
  const fold = document.getElementById('fold-rare');
  if (!fold) return JSON.stringify({fold: false});
  const rect = (el) => el.getBoundingClientRect();
  const rows = Array.from(fold.querySelectorAll('.field.setting'));
  const first = rows[0];
  const label = first && first.querySelector('.setting-label');
  const control = first && first.querySelector('.setting-control');
  const number = fold.querySelector('.setting-number');
  const bar = document.getElementById('save-bar');
  const tabbar = document.getElementById('tabbar');
  const root = document.documentElement;
  const visible = (el) => !!el && rect(el).width > 0 && rect(el).height > 0 && getComputedStyle(el).display !== 'none';
  return JSON.stringify({
    fold: true, open: fold.open,
    summary: fold.querySelector('summary').textContent.trim().replace(/\s+/g, ' '),
    state: ((fold.querySelector('.fine-state') || {}).textContent || '').trim(),
    rows: rows.length,
    toDefault: rows.filter((row) => visible(row.querySelector('.to-default'))).length,
    labelLeft: label ? Math.round(rect(label).left) : 0,
    labelBottom: label ? Math.round(rect(label).bottom) : 0,
    controlLeft: control ? Math.round(rect(control).left) : 0,
    controlTop: control ? Math.round(rect(control).top) : 0,
    controlRight: control ? Math.round(rect(control).right) : 0,
    foldRight: Math.round(rect(fold).right),
    numberWidth: number ? Math.round(rect(number).width) : 0,
    barShown: !!bar && !bar.hidden,
    barText: bar ? ((bar.querySelector('.what') || {}).textContent || '') : '',
    barBottom: bar && !bar.hidden ? Math.round(rect(bar).bottom) : 0,
    tabbarTop: tabbar && visible(tabbar) ? Math.round(rect(tabbar).top) : 0,
    sw: Math.max(root.scrollWidth, document.getElementById('window').scrollWidth), cw: root.clientWidth,
  });
})()`

type rowsView struct {
	Fold         bool   `json:"fold"`
	Open         bool   `json:"open"`
	Summary      string `json:"summary"`
	State        string `json:"state"`
	Rows         int    `json:"rows"`
	ToDefault    int    `json:"toDefault"`
	LabelLeft    int    `json:"labelLeft"`
	LabelBottom  int    `json:"labelBottom"`
	ControlLeft  int    `json:"controlLeft"`
	ControlTop   int    `json:"controlTop"`
	ControlRight int    `json:"controlRight"`
	FoldRight    int    `json:"foldRight"`
	NumberWidth  int    `json:"numberWidth"`
	BarShown     bool   `json:"barShown"`
	BarText      string `json:"barText"`
	BarBottom    int    `json:"barBottom"`
	TabbarTop    int    `json:"tabbarTop"`
	SW           int    `json:"sw"`
	CW           int    `json:"cw"`
}

func rowsRead(t *testing.T, ctx context.Context) rowsView {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(rowsState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v rowsView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

// rowsPoll — ждать условия на странице.
func rowsPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(rowsState, &now))
		t.Fatalf("не дождались: %s (%v); состояние: %s", what, err, now)
	}
}

// rowsScrollToFold — подвести «Тонкую настройку» к середине окна: свёрнутая, она
// стоит внизу экрана, и на снимке её иначе не видно.
func rowsScrollToFold(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('fold-rare').scrollIntoView({block: 'center'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
}

// rowsOpenFold — раскрыть «Тонкую настройку» настоящим нажатием и дождаться
// строк: тело приезжает по раскрытию, а не вместе с экраном.
func rowsOpenFold(t *testing.T, ctx context.Context) {
	t.Helper()
	rowsPoll(t, ctx, "«Тонкая настройка» нарисована", `!!document.querySelector('#fold-rare > summary')`)
	if err := chromedp.Run(ctx, chromedp.Click(`#fold-rare > summary`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Тонкую настройку»: %v", err)
	}
	rowsPoll(t, ctx, "строки настройки приехали", `document.querySelectorAll('#fold-rare .field.setting').length > 0`)
}

func rowsDesktop(t *testing.T, url string, hash string) context.Context {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelTime := context.WithTimeout(ctx, 4*time.Minute)
	t.Cleanup(cancelTime)
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1290, 806), chromedp.Navigate(url+"/#screen="+hash)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	return ctx
}

// Десктоп: раскрыть «Тонкую настройку» на «Интеграциях», изменить число,
// увидеть полосу, сохранить, перечитать экран — число на месте, подпись «1
// изменён», у строки «по умолчанию»; нажатие по ней возвращает умолчание.
func TestBrowserRowsFineSettingDesktop(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := rowsDesktop(t, srv.URL, mcpScreen)
	waitScreen(t, ctx, mcpScreen)
	rowsPoll(t, ctx, "«Тонкая настройка» нарисована", `!!document.getElementById('fold-rare')`)

	closed := rowsRead(t, ctx)
	if closed.Open || !strings.Contains(closed.Summary, "Тонкая настройка") || !strings.Contains(closed.Summary, "16 параметров") ||
		closed.State != "всё по умолчанию" {
		t.Errorf("свёрнутая строка: %+v", closed)
	}
	rowsScrollToFold(t, ctx)
	karkasShot(t, ctx, "rows-desktop-1-integrations-closed")

	rowsOpenFold(t, ctx)
	open := rowsRead(t, ctx)
	if !open.Open || open.Rows != 16 {
		t.Fatalf("после раскрытия: %+v", open)
	}
	// подпись слева, контрол справа, число узкое, вбок ничего не едет
	if open.ControlLeft <= open.LabelLeft+200 || open.ControlRight > open.FoldRight || open.NumberWidth < 80 || open.NumberWidth > 92 {
		t.Errorf("раскладка строки: подпись слева %d, контрол слева %d справа %d (край раздела %d), число %d точек",
			open.LabelLeft, open.ControlLeft, open.ControlRight, open.FoldRight, open.NumberWidth)
	}
	if open.ToDefault != 0 || open.BarShown || open.SW > open.CW {
		t.Errorf("до правки: %+v", open)
	}
	karkasShot(t, ctx, "rows-desktop-2-integrations-open")

	// подсказка одной строкой с хвостом: нажатие раскрывает её целиком, второе сворачивает
	const hint = `#fold-rare .field.setting .setting-hint`
	var oneLine, full float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('`+hint+`').getBoundingClientRect().height`, &oneLine),
		chromedp.Click(hint, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать подсказку: %v", err)
	}
	rowsPoll(t, ctx, "подсказка раскрыта", `document.querySelector('`+hint+`').classList.contains('open')`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('`+hint+`').getBoundingClientRect().height`, &full)); err != nil {
		t.Fatal(err)
	}
	if full <= oneLine {
		t.Errorf("раскрытая подсказка не выше свёрнутой: %.0f и %.0f", full, oneLine)
	}
	if err := chromedp.Run(ctx, chromedp.Click(hint, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "подсказка снова свёрнута", `!document.querySelector('`+hint+`').classList.contains('open')`)

	// настоящий ввод в поле «Вызовов за один ответ»
	const calls = `#fold-rare input[name="chat_calls"]`
	if err := chromedp.Run(ctx,
		chromedp.Click(calls, chromedp.ByQuery),
		chromedp.SendKeys(calls, kb.End+kb.Backspace+"5", chromedp.ByQuery)); err != nil {
		t.Fatalf("вписать число: %v", err)
	}
	rowsPoll(t, ctx, "полоса сохранения с одной правкой",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1'); })()`)
	edited := rowsRead(t, ctx)
	if !strings.Contains(edited.BarText, "Вызовов за один ответ") || edited.ToDefault != 1 {
		t.Errorf("с правкой: полоса %q, «по умолчанию» у %d строк", edited.BarText, edited.ToDefault)
	}
	karkasShot(t, ctx, "rows-desktop-3-integrations-savebar")

	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	// экран перечитан: полоса гаснет, раздел остался раскрытым, подпись «1 изменён»
	rowsPoll(t, ctx, "после сохранения: раздел раскрыт, подпись говорит про одну правку",
		`(() => { const bar = document.getElementById('save-bar'); const fold = document.getElementById('fold-rare');
		  return !!bar && bar.hidden && !!fold && fold.open && fold.querySelectorAll('.field.setting').length === 16 &&
		    (fold.querySelector('.fine-state') || {}).textContent.trim() === '1 изменён'; })()`)
	if got := s.eng.MCPChatLimits().Calls; got != 5 {
		t.Fatalf("движок видит %d вызовов за ответ, а вписано 5", got)
	}
	karkasShot(t, ctx, "rows-desktop-3b-integrations-saved")
	saved := rowsRead(t, ctx)
	if saved.ToDefault != 1 {
		t.Errorf("после сохранения «по умолчанию» у %d строк, а изменена одна", saved.ToDefault)
	}

	// перезагрузка страницы: значение на месте, подпись прежняя
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, mcpScreen)
	rowsPoll(t, ctx, "подпись после перезагрузки", `((document.querySelector('#fold-rare .fine-state') || {}).textContent || '').trim() === '1 изменён'`)
	rowsOpenFold(t, ctx)
	var value string
	if err := chromedp.Run(ctx, chromedp.Value(calls, &value, chromedp.ByQuery)); err != nil || value != "5" {
		t.Fatalf("после перезагрузки в поле %q (%v), а сохранено 5", value, err)
	}

	// «по умолчанию» кладёт умолчание, а сохраняет его та же полоса
	if err := chromedp.Run(ctx, chromedp.Click(`#fold-rare .field.setting:has(input[name="chat_calls"]) .to-default`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «по умолчанию»: %v", err)
	}
	rowsPoll(t, ctx, "умолчание в поле, строка снова «по умолчанию», полоса ждёт сохранения",
		`(() => { const input = document.querySelector('#fold-rare input[name="chat_calls"]');
		  const bar = document.getElementById('save-bar');
		  return input.value === '3' && input.closest('.field').classList.contains('is-default') && !bar.hidden; })()`)
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "сохранено умолчание: подпись «всё по умолчанию»",
		`(() => { const fold = document.getElementById('fold-rare'); const bar = document.getElementById('save-bar');
		  return !!bar && bar.hidden && !!fold && fold.open && (fold.querySelector('.fine-state') || {}).textContent.trim() === 'всё по умолчанию'; })()`)
	if got := s.eng.MCPChatLimits().Calls; got != 3 {
		t.Fatalf("движок видит %d вызовов за ответ после возврата умолчания, а должно быть 3", got)
	}
}

// Экран перечитывается по проводу (то же событие `sema:refresh`, что шлёт кадр
// провода), и раскрытая «Тонкая настройка» остаётся раскрытой, а тело
// приезжает заново. Несохранённая правка в ней считается полосой: пока она не
// сохранена и не отменена, клиент не даёт перечитывать экран (`sema.unsaved`).
func TestBrowserRowsSurviveRedraw(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := rowsDesktop(t, srv.URL, searchScreen)
	waitScreen(t, ctx, searchScreen)
	rowsOpenFold(t, ctx)
	// метка на самом узле: перечитанный экран приедет без неё
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.getElementById('fold-rare').setAttribute('data-marked', '1');
		 htmx.trigger(document.getElementById('screen'), 'sema:refresh'); true`, nil)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "экран перечитан, раздел остался раскрытым и тело вернулось",
		`(() => { const fold = document.getElementById('fold-rare');
		  return !!fold && !fold.hasAttribute('data-marked') && fold.open && fold.querySelectorAll('.field.setting').length === 8; })()`)

	// правка в «Тонкой настройке» видна клиенту как несохранённая, отмена её снимает
	const field = `#fold-rare input[name="page_chars"]`
	if err := chromedp.Run(ctx, chromedp.Click(field, chromedp.ByQuery), chromedp.SendKeys(field, "0", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "клиент считает правку несохранённой", `sema.unsaved() === true`)
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="revert"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "после «Отменить» правки нет, поле вернулось",
		`sema.unsaved() === false && document.querySelector('#fold-rare input[name="page_chars"]').value === '30000'`)
}

// Поиск и «Серверы → Самостоятельность»: снимки для ревью и проверка состава.
func TestBrowserRowsOtherScreens(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := rowsDesktop(t, srv.URL, searchScreen)
	waitScreen(t, ctx, searchScreen)
	rowsPoll(t, ctx, "«Тонкая настройка» «Поиска»", `!!document.getElementById('fold-rare')`)
	if v := rowsRead(t, ctx); !strings.Contains(v.Summary, "8 параметров") || v.State != "всё по умолчанию" {
		t.Errorf("«Поиск»: %+v", v)
	}
	rowsScrollToFold(t, ctx)
	karkasShot(t, ctx, "rows-desktop-4-search-closed")
	rowsOpenFold(t, ctx)
	if v := rowsRead(t, ctx); v.Rows != 8 {
		t.Errorf("«Поиск» раскрыт: %+v", v)
	}
	karkasShot(t, ctx, "rows-desktop-5-search-open")

	karkasOpen(t, ctx, serversScreen+"~"+serversTabAuto)
	rowsPoll(t, ctx, "«Тонкая настройка» «Самостоятельности»", `!!document.getElementById('fold-rare')`)
	if v := rowsRead(t, ctx); !strings.Contains(v.Summary, "11 параметров") || v.State != "всё по умолчанию" {
		t.Errorf("«Серверы → Самостоятельность»: %+v", v)
	}
	rowsOpenFold(t, ctx)
	if v := rowsRead(t, ctx); v.Rows != 11 {
		t.Errorf("«Самостоятельность» раскрыта: %+v", v)
	}
	karkasShot(t, ctx, "rows-desktop-6-servers-open")
	// низ раскрытого раздела: выпадашки скиллов, страховка, SSH
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const rows = document.querySelectorAll('#fold-rare .field.setting'); rows[rows.length - 1].scrollIntoView({block: 'end'}); return true; })()`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "rows-desktop-6b-servers-bottom")

	// «Доступ → Доверенная сеть»: тумблеры остаются наверху, редкое — внизу, перед «Опасным»
	karkasOpen(t, ctx, accessScreen+"~"+accessTabNetwork)
	rowsPoll(t, ctx, "«Тонкая настройка» «Доступа»", `!!document.getElementById('fold-rare')`)
	var beforeDanger bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const fold = document.getElementById('fold-rare'); const danger = Array.from(document.querySelectorAll('#feed form h3'))
		  .find((h) => h.textContent.startsWith('Опасное'));
		  return !!danger && !!(fold.compareDocumentPosition(danger.closest('form')) & Node.DOCUMENT_POSITION_FOLLOWING); })()`, &beforeDanger)); err != nil || !beforeDanger {
		t.Errorf("«Тонкая настройка» должна стоять перед «Опасным» (%v)", err)
	}
	rowsOpenFold(t, ctx)
	if v := rowsRead(t, ctx); v.Rows != 3 {
		t.Errorf("«Доступ → Доверенная сеть» раскрыта: %+v", v)
	}
	karkasShot(t, ctx, "rows-desktop-7-access-open")
}

// Телефон, 390×844: контрол под подписью, число узкое, ничего не едет вбок,
// полоса сохранения стоит над нижней полосой приложения, «Отменить» и
// «Сохранить» одной ширины.
func TestBrowserRowsPhone(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 3*time.Minute)
	karkasOpen(t, ctx, mcpScreen)
	rowsPoll(t, ctx, "«Тонкая настройка» нарисована", `!!document.getElementById('fold-rare')`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('fold-rare').scrollIntoView({block: 'center'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	if v := rowsRead(t, ctx); v.Open || v.State != "всё по умолчанию" || v.SW > v.CW {
		t.Errorf("телефон, свёрнутая строка: %+v", v)
	}
	karkasShot(t, ctx, "rows-phone-1-integrations-closed")

	rowsOpenFold(t, ctx)
	v := rowsRead(t, ctx)
	if v.Rows != 16 || v.ControlTop < v.LabelBottom || v.NumberWidth < 80 || v.NumberWidth > 92 || v.SW > v.CW {
		t.Errorf("телефон: контрол должен стоять под подписью, число узкое, вбок не ехать: %+v", v)
	}
	// настоящий ввод, как пальцем: касание и набор
	const calls = `#fold-rare input[name="chat_calls"]`
	if err := chromedp.Run(ctx, chromedp.Click(calls, chromedp.ByQuery), chromedp.SendKeys(calls, kb.End+kb.Backspace+"4", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "полоса сохранения", `(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden; })()`)
	edited := rowsRead(t, ctx)
	if edited.TabbarTop == 0 || edited.BarBottom > edited.TabbarTop+1 {
		t.Errorf("полоса сохранения должна стоять над нижней полосой приложения: низ полосы %d, верх нижней полосы %d", edited.BarBottom, edited.TabbarTop)
	}
	var equal bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const buttons = Array.from(document.querySelectorAll('#save-bar .right-side .action'));
		  return buttons.length === 2 && Math.abs(buttons[0].getBoundingClientRect().width - buttons[1].getBoundingClientRect().width) < 2; })()`, &equal)); err != nil || !equal {
		t.Errorf("«Отменить» и «Сохранить» должны быть равной ширины (%v)", err)
	}
	if edited.SW > edited.CW {
		t.Errorf("с полосой экран едет вбок: %d при %d", edited.SW, edited.CW)
	}
	karkasShot(t, ctx, "rows-phone-2-integrations-open-savebar")
}
