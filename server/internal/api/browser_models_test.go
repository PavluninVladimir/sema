package api

// Раздел «Модели и роли» в настоящем Chrome (1 октября 2026): роли таблицей и
// список на сервере (поиск и отбор), экран модели с ценой полями, экран
// провайдера по вкладкам, телефон 390 — таблица карточками. Проверяем то, чем
// пользуется человек: нажатие на строку, на вкладку, на чип, набор в поле и
// «Сохранить» полосой — а не вызов функции. Ждём условий на странице, а не
// сроков.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844),
// данные из modelsFix — полторы сотни моделей у четырёх провайдеров, роли,
// цены разных форм, одна роль на передышке. Chrome без головы; настоящих
// провайдеров здесь нет: адреса моделей ведут в никуда, а проверка модели
// честно говорит, что связи нет.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// modelsState — что видно на экранах «Моделей и ролей».
const modelsState = `(() => {
  const visible = (el) => { if (!el) return false; const rect = el.getBoundingClientRect(); const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'; };
  const root = document.documentElement;
  const screenEl = document.getElementById('screen');
  const rows = Array.from(document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:models@"]')).filter(visible).map((row) => ({
    goto: row.getAttribute('data-goto'),
    pills: row.querySelectorAll('.right-side .pill').length,
    buttons: Array.from(row.querySelectorAll('.right-side > button.action')).filter(visible).length,
    more: !!row.querySelector('.row-menu'),
    text: row.textContent.replace(/\s+/g, ' ').trim(),
  }));
  const table = document.querySelector('#feed table.table');
  const tableRows = table ? Array.from(table.querySelectorAll('tbody tr')).map((tr) => ({
    who: (tr.querySelector('td.who .title') || {}).textContent || '',
    lists: tr.querySelectorAll('td select').length,
    block: getComputedStyle(tr).display,
    labels: Array.from(tr.querySelectorAll('td[data-label]')).map((td) => td.getAttribute('data-label')),
  })) : [];
  const savebar = document.getElementById('save-bar');
  return JSON.stringify({
    screen: screenEl ? screenEl.dataset.screen : '',
    h1: (document.querySelector('#screen .header h1') || {}).textContent || '',
    crumb: (() => { const c = document.querySelector('#screen .crumbs .crumb'); return c && visible(c) ? c.textContent.trim() : ''; })(),
    subtabs: Array.from(document.querySelectorAll('#screen .subtabs-bar .segment')).map((a) => a.textContent.replace(/\s+/g, ' ').trim()),
    selectedTab: (() => { const a = document.querySelector('#screen .subtabs-bar .segment.selected'); return a ? a.textContent.replace(/\s+/g, ' ').trim() : ''; })(),
    chips: Array.from(document.querySelectorAll('#screen .filters-bar .segment')).map((a) => ({ text: a.textContent.trim(), on: a.classList.contains('selected') })),
    rows: rows,
    tableRows: tableRows,
    theadShown: table ? visible(table.querySelector('thead')) : false,
    sections: Array.from(document.querySelectorAll('#feed details.fold > summary.section-heading')).map((s) => ({ text: s.textContent.replace(/\s+/g, ' ').trim(), open: s.parentElement.open })),
    barShown: !!savebar && !savebar.hidden,
    barLabel: savebar ? ((savebar.querySelector('.what') || {}).textContent || '') : '',
    headerButtons: Array.from(document.querySelectorAll('#screen .header .controls > button.action')).filter(visible).map((b) => b.textContent.trim()),
    searchHint: (document.getElementById('search') || {}).placeholder || '',
    screenText: (document.getElementById('feed') || {}).textContent || '',
    danger: !!document.querySelector('#feed .section.danger'),
    sw: Math.max(root.scrollWidth, document.getElementById('window').scrollWidth, document.getElementById('feed').scrollWidth), cw: root.clientWidth,
  });
})()`

type modelsRow struct {
	Goto    string `json:"goto"`
	Pills   int    `json:"pills"`
	Buttons int    `json:"buttons"`
	More    bool   `json:"more"`
	Text    string `json:"text"`
}

type modelsTableRow struct {
	Who    string   `json:"who"`
	Lists  int      `json:"lists"`
	Block  string   `json:"block"`
	Labels []string `json:"labels"`
}

type modelsChip struct {
	Text string `json:"text"`
	On   bool   `json:"on"`
}

type modelsSection struct {
	Text string `json:"text"`
	Open bool   `json:"open"`
}

type modelsView struct {
	Screen        string           `json:"screen"`
	H1            string           `json:"h1"`
	Crumb         string           `json:"crumb"`
	Subtabs       []string         `json:"subtabs"`
	SelectedTab   string           `json:"selectedTab"`
	Chips         []modelsChip     `json:"chips"`
	Rows          []modelsRow      `json:"rows"`
	TableRows     []modelsTableRow `json:"tableRows"`
	TheadShown    bool             `json:"theadShown"`
	Sections      []modelsSection  `json:"sections"`
	BarShown      bool             `json:"barShown"`
	BarLabel      string           `json:"barLabel"`
	HeaderButtons []string         `json:"headerButtons"`
	SearchHint    string           `json:"searchHint"`
	ScreenText    string           `json:"screenText"`
	Danger        bool             `json:"danger"`
	SW            int              `json:"sw"`
	CW            int              `json:"cw"`
}

func modelsRead(t *testing.T, ctx context.Context) modelsView {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(modelsState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v modelsView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

// modelsPoll — ждать условия на странице; не дождались — показать, что на ней.
func modelsPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(modelsState, &now))
		t.Fatalf("не дождались: %s (%v); состояние: %s", what, err, now)
	}
}

// modelsServer — сервер с моделями и Chrome 1290×806.
func modelsServer(t *testing.T, models int) (*Server, context.Context) {
	t.Helper()
	serversBrowserOnly(t)
	s, _ := modelsFix(t, models)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, serversDesktop(t, srv.URL, modelsScreen)
}

// Роли — таблицей: подписи колонок один раз в шапке, три списка в каждой
// строке, и смена модели роли — настоящим выбором в ячейке, а не вызовом
// действия.
func TestBrowserModelsRolesTable(t *testing.T) {
	s, ctx := modelsServer(t, 60)
	waitScreen(t, ctx, modelsScreen)
	modelsPoll(t, ctx, "таблица ролей нарисована", `document.querySelectorAll('#feed table.table tbody tr').length >= 9`)
	v := modelsRead(t, ctx)
	if !v.TheadShown {
		t.Error("у таблицы ролей на десктопе нет шапки с подписями колонок")
	}
	for _, r := range v.TableRows {
		if r.Lists != 3 {
			t.Errorf("у роли %q списков %d вместо трёх", r.Who, r.Lists)
		}
	}
	// число модели стоит у имени вкладки отдельным значком: в тексте они слитно
	if got := strings.Join(v.Subtabs, "|"); !strings.HasPrefix(got, "Роли|Модели60|Провайдеры|Проблемы") {
		t.Errorf("вкладки раздела: %v", v.Subtabs)
	}
	if strings.Join(v.HeaderButtons, "|") != "Подключить провайдера" {
		t.Errorf("кнопки шапки раздела: %v", v.HeaderButtons)
	}

	// выбор в ячейке: свой выпадающий список клиента (набор вариантов один на
	// экран); меняем запасную у «Чатов» нажатиями, как человек: список → поиск
	// в нём → вариант
	modelsTag(t, ctx, `(() => {
	  const row = Array.from(document.querySelectorAll('#feed table.table tbody tr')).find((tr) => tr.querySelector('td.who .title').textContent.trim().startsWith('Чаты'));
	  const sel = row.querySelectorAll('td select')[1]; sel.setAttribute('data-test', 'spare'); return sel; })()`)
	if err := chromedp.Run(ctx, chromedp.Click(`[data-test=spare]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	modelsPoll(t, ctx, "открылся список выбора модели", `!!document.querySelector('#set-picker .option')`)
	if err := chromedp.Run(ctx, chromedp.SendKeys(`#set-picker input`, "gpt-6-sol", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	modelsPoll(t, ctx, "вариант найден поиском в списке", `Array.from(document.querySelectorAll('#set-picker .option')).some((o) => o.textContent.startsWith('gpt-6-sol'))`)
	modelsTag(t, ctx, `Array.from(document.querySelectorAll('#set-picker .option')).find((o) => o.textContent.startsWith('gpt-6-sol'))`)
	if err := chromedp.Run(ctx, chromedp.Click(`[data-test=picked]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	untilTrue(t, "запасная роли «Чаты» сменилась", func() bool { return s.eng.Role("Чаты").Spare == "gpt-6-sol" })
	// экран перерисован, и в ячейке стоит выбранное, а не прежнее
	modelsPoll(t, ctx, "ячейка показывает новую запасную", `(() => {
	  const row = Array.from(document.querySelectorAll('#feed table.table tbody tr')).find((tr) => tr.querySelector('td.who .title').textContent.trim().startsWith('Чаты'));
	  return !!row && row.querySelectorAll('td select')[1].value.startsWith('gpt-6-sol'); })()`)
}

// Список моделей: чип отбора и набор в поле поиска идут на сервер и находят то,
// чего нет на первой странице; нажатие на строку открывает экран модели, цена
// правится полями и сохраняется полосой, «‹ Модели» возвращает к списку.
func TestBrowserModelsSearchFilterAndEditPrice(t *testing.T) {
	s, ctx := modelsServer(t, 173)
	list := modelsScreen + "~" + modelsTabList
	karkasOpen(t, ctx, list)
	modelsPoll(t, ctx, "строки списка нарисованы", `document.querySelectorAll('#feed .row.clickable[data-goto^="settings:models@"]').length > 0`)
	v := modelsRead(t, ctx)
	if len(v.Rows) != modelsPage {
		t.Errorf("первая порция: %d строк вместо %d", len(v.Rows), modelsPage)
	}
	for _, r := range v.Rows {
		if r.Pills > 2 || r.Buttons > 1 || !r.More {
			t.Errorf("строка не тонкая: %+v", r)
		}
	}
	if len(v.Chips) != 7 || !v.Chips[0].On || v.SearchHint != "Найти модель по имени" {
		t.Errorf("полоса отбора: %+v · поле %q", v.Chips, v.SearchHint)
	}

	// чип «Без цены» — отбор на сервере: в строках только модели без цены
	modelsClickChip(t, ctx, "Без цены")
	modelsPoll(t, ctx, "в списке только модели без цены", `(() => {
	  const rows = Array.from(document.querySelectorAll('#feed .row.clickable[data-goto^="settings:models@"]'));
	  return rows.length > 0 && rows.every((r) => r.textContent.includes('цена не названа')); })()`)

	// набор в поле поиска: «Все» и слово, которого нет на первой странице
	modelsClickChip(t, ctx, "Все")
	var target string
	for _, c := range s.eng.ModelCards() {
		if strings.HasPrefix(c.Name, "pplx-ряд-") {
			target = c.Name
		}
	}
	if err := chromedp.Run(ctx, chromedp.SendKeys(`#search`, target, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	modelsPoll(t, ctx, "поиск нашёл модель на сервере", `(() => {
	  const rows = Array.from(document.querySelectorAll('#feed .row.clickable[data-goto^="settings:models@"]'));
	  return rows.length === 1 && rows[0].getAttribute('data-goto') === `+jsString(modelAddr(target))+`; })()`)
	// набранное в поле остаётся после ответа: человек видит, что искал
	var typed string
	if err := chromedp.Run(ctx, chromedp.Value(`#search`, &typed, chromedp.ByQuery)); err != nil || typed != target {
		t.Errorf("в поле поиска %q вместо %q (%v)", typed, target, err)
	}
	// очистить поиск и открыть модель с ценой: нажатием на название строки
	if err := chromedp.Run(ctx, chromedp.SetValue(`#search`, "gpt-6.1-sol", chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('search').dispatchEvent(new Event('input', {bubbles: true})); true`, nil)); err != nil {
		t.Fatal(err)
	}
	modelsPoll(t, ctx, "найдена gpt-6.1-sol", `document.querySelectorAll('#feed .row.clickable[data-goto="settings:models@gpt-6.1-sol"]').length === 1`)
	if err := chromedp.Run(ctx, chromedp.Click(`#feed .row.clickable[data-goto="settings:models@gpt-6.1-sol"] a.row-link`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, modelAddr("gpt-6.1-sol"))
	v = modelsRead(t, ctx)
	if v.Crumb != "‹ Модели" || !v.Danger {
		t.Errorf("экран модели: %+v", v)
	}

	// цена полями: вход и выход вместо мини-языка; полоса считает правку
	const inField, outField = `form[data-form="model-price"] input[name="in"]`, `form[data-form="model-price"] input[name="out"]`
	modelsPoll(t, ctx, "форма цены нарисована", `!!document.querySelector('`+inField+`')`)
	var was string
	if err := chromedp.Run(ctx, chromedp.Value(inField, &was, chromedp.ByQuery)); err != nil || was != "5" {
		t.Fatalf("вход у gpt-6.1-sol: %q (%v)", was, err)
	}
	for sel, val := range map[string]string{inField: "6", outField: "20"} {
		if err := chromedp.Run(ctx, chromedp.Clear(sel, chromedp.ByQuery), chromedp.SendKeys(sel, val, chromedp.ByQuery)); err != nil {
			t.Fatal(err)
		}
	}
	modelsPoll(t, ctx, "полоса сохранения считает две правки", `(() => { const b = document.getElementById('save-bar'); return !!b && !b.hidden && b.textContent.includes('Изменено 2'); })()`)
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	untilTrue(t, "цена сохранилась строкой прежнего формата", func() bool { return s.eng.PriceOf("gpt-6.1-sol") == "$6 / $20 / 1M" })
	card, _ := s.eng.ModelCardOf("gpt-6.1-sol")
	if card.PriceBy != "человек" {
		t.Errorf("ручная цена помечена %q", card.PriceBy)
	}
	// экран перечитан: поля показывают сохранённое, полоса убрана
	modelsPoll(t, ctx, "поля показывают сохранённое", `(() => { const f = document.querySelector('`+inField+`'); const b = document.getElementById('save-bar');
	  return !!f && f.value === '6' && (!b || b.hidden); })()`)

	// обратно к списку: «‹ Модели». После сохранения экран перечитывается, и
	// крошка — уже другой узел: между «нашёл» и «нажал» он успевает смениться
	// («Could not find node with given id», 2 октября 2026 — дважды в полном
	// прогоне на загруженной машине, в одиночку ни разу). Жест остаётся
	// настоящим нажатием, но повторяется, пока узел не устоит
	var clickErr error
	for attempt := 0; attempt < 5; attempt++ {
		if clickErr = chromedp.Run(ctx, chromedp.Click(`#screen .crumbs .crumb`, chromedp.ByQuery)); clickErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	waitScreen(t, ctx, list)
}

// Провайдер: строка ведёт на его экран, вкладки «Ключ и модели · Цены · Остаток»
// открываются нажатием, а ключ нигде не показан.
func TestBrowserModelsProviderTabs(t *testing.T) {
	_, ctx := modelsServer(t, 60)
	karkasOpen(t, ctx, modelsScreen+"~"+modelsTabProviders)
	modelsPoll(t, ctx, "строки провайдеров нарисованы", `document.querySelectorAll('#feed .row.clickable[data-goto^="settings:models@provider:"]').length >= 4`)
	if err := chromedp.Run(ctx, chromedp.Click(`#feed .row.clickable[data-goto="`+providerAddr("DeepSeek", "")+`"] a.row-link`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, providerAddr("DeepSeek", ""))
	v := modelsRead(t, ctx)
	if v.Crumb != "‹ Провайдеры" || len(v.Subtabs) != 3 {
		t.Errorf("экран провайдера: %+v", v)
	}
	if strings.Contains(v.ScreenText, "СЕКРЕТ") {
		t.Error("значение ключа на экране провайдера")
	}
	for _, tab := range []struct{ title, screen, marker string }{
		{"Цены", providerAddr("DeepSeek", providerTabPrices), `form[data-form="psrc-DeepSeek"]`},
		{"Остаток", providerAddr("DeepSeek", providerTabBalance), `form[data-form="bal-DeepSeek"]`},
		{"Ключ и модели", providerAddr("DeepSeek", ""), `#sec-provider-key`},
	} {
		modelsTag(t, ctx, `Array.from(document.querySelectorAll('#screen .subtabs-bar .segment')).find((a) => a.textContent.trim().startsWith(`+jsString(tab.title)+`))`)
		if err := chromedp.Run(ctx, chromedp.Click(`[data-test=picked]`, chromedp.ByQuery)); err != nil {
			t.Fatal(err)
		}
		waitScreen(t, ctx, tab.screen)
		modelsPoll(t, ctx, "вкладка «"+tab.title+"» нарисована", `!!document.querySelector('`+tab.marker+`')`)
	}
	// «Подключить провайдера» из шапки раздела
	karkasOpen(t, ctx, modelsScreen)
	modelsTag(t, ctx, `Array.from(document.querySelectorAll('#screen .header .controls > button.action')).find((b) => b.textContent.trim() === 'Подключить провайдера')`)
	if err := chromedp.Run(ctx, chromedp.Click(`[data-test=picked]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(modelsScreen, modelsNew, ""))
	modelsPoll(t, ctx, "форма подключения нарисована", `!!document.querySelector('form[hx-post] input[type=password][name="key"]')`)
}

// Телефон 390: таблица ролей раскладывается карточками с подписями колонок, шапки
// нет, ничего не едет вбок; список моделей и экран модели — тоже.
func TestBrowserModelsPhoneTable(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := modelsFix(t, 173)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)
	karkasOpen(t, ctx, modelsScreen)
	modelsPoll(t, ctx, "таблица ролей нарисована", `document.querySelectorAll('#feed table.table tbody tr').length >= 9`)
	v := modelsRead(t, ctx)
	if v.TheadShown {
		t.Error("на телефоне у таблицы осталась шапка колонок")
	}
	for _, r := range v.TableRows {
		if r.Block != "block" || len(r.Labels) < 4 {
			t.Errorf("роль %q не карточкой: display=%s подписей %v", r.Who, r.Block, r.Labels)
		}
		if r.Lists != 3 {
			t.Errorf("у роли %q списков %d", r.Who, r.Lists)
		}
	}
	if v.SW > v.CW {
		t.Errorf("таблица ролей на телефоне ездит вбок: %d при %d", v.SW, v.CW)
	}
	for _, screen := range []string{modelsScreen + "~" + modelsTabList, modelAddr("gpt-6-luna"), providerAddr("DeepSeek", providerTabPrices), modelsScreen + "~" + modelsTabProviders, modelsScreen + "~" + modelsTabIssues} {
		karkasOpen(t, ctx, screen)
		v = modelsRead(t, ctx)
		if v.SW > v.CW {
			t.Errorf("%s на телефоне ездит вбок: %d при %d", screen, v.SW, v.CW)
		}
	}
	// у списка моделей на телефоне есть своё поле поиска над списком
	karkasOpen(t, ctx, modelsScreen+"~"+modelsTabList)
	modelsPoll(t, ctx, "у списка на телефоне есть поле поиска", `!!document.querySelector('#screen .filters-bar #search')`)
}

// modelsTag — пометить узел, который вернуло выражение, атрибутом
// data-test=picked (или своим, если выражение его уже поставило): по метке его
// нажимает настоящий жест, а не вызов функции.
func modelsTag(t *testing.T, ctx context.Context, expr string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const el = `+expr+`; if (!el) return false;
	  if (!el.getAttribute('data-test')) el.setAttribute('data-test', 'picked'); return true; })()`, nil)); err != nil {
		t.Fatal(err)
	}
}

// Снимки для ревью.
func TestBrowserModelsShots(t *testing.T) {
	_, ctx := modelsServer(t, 173)
	waitScreen(t, ctx, modelsScreen)
	for _, shot := range []struct{ name, screen string }{
		{"desktop-1-roles", modelsScreen},
		{"desktop-2-models", modelsScreen + "~" + modelsTabList},
		{"desktop-4-model", modelAddr("gpt-6-luna")},
		{"desktop-4b-model-deepseek", modelAddr("deepseek-v4-flash")},
		{"desktop-5-provider-key", providerAddr("OpenAI", "")},
		{"desktop-6-provider-prices", providerAddr("DeepSeek", providerTabPrices)},
		{"desktop-7-provider-balance", providerAddr("DeepSeek", providerTabBalance)},
		{"desktop-8-providers", modelsScreen + "~" + modelsTabProviders},
		{"desktop-9-issues", modelsScreen + "~" + modelsTabIssues},
		{"desktop-10-connect", entityAddr(modelsScreen, modelsNew, "")},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	scrollEnd := `document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`
	// низ «Ролей»: служебные роли и «Тонкая настройка» с параметрами речи
	karkasOpen(t, ctx, modelsScreen)
	rowsOpenFold(t, ctx)
	if err := chromedp.Run(ctx, chromedp.Evaluate(scrollEnd, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-1b-roles-bottom")
	// «Тонкая настройка» вкладки «Провайдеры»
	karkasOpen(t, ctx, modelsScreen+"~"+modelsTabProviders)
	rowsOpenFold(t, ctx)
	karkasShot(t, ctx, "desktop-8b-providers-fine")
	// цена: часы пик и валюта под «Ещё N полей», расход и «Опасное» ниже
	karkasOpen(t, ctx, modelAddr("deepseek-v4-flash"))
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('sec-model-price-section').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-4c-model-price")
	if err := chromedp.Run(ctx, chromedp.Evaluate(scrollEnd, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-4d-model-bottom")
	// список с отбором «Без цены» и поиском: настоящие нажатие и набор
	karkasOpen(t, ctx, modelsScreen+"~"+modelsTabList)
	modelsClickChip(t, ctx, "Без цены")
	karkasShot(t, ctx, "desktop-3-models-noprice")
}

// modelsClickChip — нажать чип отбора по началу его названия и дождаться, пока
// он станет выбранным.
func modelsClickChip(t *testing.T, ctx context.Context, title string) {
	t.Helper()
	modelsTag(t, ctx, `Array.from(document.querySelectorAll('#screen .filters-bar .segment')).find((a) => a.textContent.trim().startsWith(`+jsString(title)+`))`)
	if err := chromedp.Run(ctx, chromedp.Click(`[data-test=picked]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать чип %q: %v", title, err)
	}
	modelsPoll(t, ctx, "чип «"+title+"» выбран", `(() => { const a = Array.from(document.querySelectorAll('#screen .filters-bar .segment.selected')); return a.length === 1 && a[0].textContent.trim().startsWith(`+jsString(title)+`); })()`)
}

func TestBrowserModelsShotsPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := modelsFix(t, 173)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)
	for _, shot := range []struct{ name, screen string }{
		{"phone-1-roles", modelsScreen},
		{"phone-2-models", modelsScreen + "~" + modelsTabList},
		{"phone-3-model", modelAddr("gpt-6-luna")},
		{"phone-4-provider", providerAddr("DeepSeek", providerTabPrices)},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
}
