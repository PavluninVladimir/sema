package api

// «Интеграции» в настоящем Chrome (2 октября 2026): список серверов MCP тонкими
// строками, экран сервера с вкладками, «В чате» полями, правила полосой,
// журнал, «Добавить сервер MCP». Настоящие нажатия и ввод, ожидание условий на
// странице вместо пауз; состояние проверяется и на странице, и в базе.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).
// Chrome без головы; настоящих обращений в сеть нет: адреса серверов в
// фикстурах закрыты, а у «wiki» — поддельный сервер mcptest.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// mcpState — что видно на экранах «Интеграций»: то же, что у «Серверов», только
// строки списка ищутся по адресам этого раздела.
var mcpState = strings.ReplaceAll(serversState, "settings:servers@", "settings:integrations@")

func mcpRead(t *testing.T, ctx context.Context) serversView {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(mcpState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v serversView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

// mcpPoll — ждать условия на странице; не дождались — показать, что на ней.
func mcpPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(mcpState, &now))
		t.Fatalf("не дождались: %s (%v); состояние: %s", what, err, now)
	}
}

const mcpRowsSel = `#feed .row-wrap > .row.clickable[data-goto^="settings:integrations@"]`

// mcpOpenList — открыть раздел и дождаться четырёх строк.
func mcpOpenList(t *testing.T, ctx context.Context) {
	t.Helper()
	waitScreen(t, ctx, mcpScreen)
	mcpPoll(t, ctx, "список серверов нарисован", `document.querySelectorAll('`+mcpRowsSel+`').length === 4`)
}

// mcpClickServer — нажать строку сервера в списке: по названию, как мышью.
func mcpClickServer(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	sel := mcpRowsSel + `[data-goto="` + entityAddr(mcpScreen, name, "") + `"] a.row-link`
	if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать строку «%s»: %v", name, err)
	}
	waitScreen(t, ctx, entityAddr(mcpScreen, name, ""))
	mcpPoll(t, ctx, "экран сервера нарисован", `!!document.getElementById('sec-entity')`)
}

// mcpScrollToCenter — подвести элемент к середине окна прокруткой: закреплённая
// строка сервера иначе перекрывает то, что стоит у верхнего края.
func mcpScrollToCenter(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('`+sel+`').scrollIntoView({block: 'center'}); true`, nil)); err != nil {
		t.Fatalf("подвести %s к середине окна: %v", sel, err)
	}
}

// mcpScrollXPath — то же для элемента, найденного XPath.
func mcpScrollXPath(t *testing.T, ctx context.Context, xpath string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.evaluate(`+jsString(xpath)+`, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue.scrollIntoView({block: 'center'}); true`, nil)); err != nil {
		t.Fatalf("подвести %s к середине окна: %v", xpath, err)
	}
}

// toggleSel — переключатель-тумблер у поля по имени.
func toggleSel(field string) string {
	return `.field.setting:has(input[name="` + field + `"]) label.field-toggle`
}

// Список → сервер → вкладки → «В чате» полями → «Правила» (поиск, ответ,
// полоса) → «Журнал» → «‹ Интеграции» → «Добавить сервер MCP». Состояние после
// каждого шага читается и со страницы, и из базы.
func TestBrowserMCPWalk(t *testing.T) {
	serversBrowserOnly(t)
	s, st := mcpScreensFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, mcpScreen)
	mcpOpenList(t, ctx)

	// ---- список: тонкие строки, полка, одна кнопка в шапке ----
	list := mcpRead(t, ctx)
	if len(list.Rows) != 4 || !list.Shelf || !strings.Contains(list.ShelfText, "Ждут вас") ||
		!strings.Contains(list.ShelfText, "task_create") || !strings.Contains(list.ShelfText, "новых инструмента") {
		t.Fatalf("список: строк %d, полка %v: %+v", len(list.Rows), list.Shelf, list)
	}
	for _, row := range list.Rows {
		if row.Pills > 2 || row.Buttons > 1 || !row.More {
			t.Errorf("строка не тонкая — капсул %d, кнопок %d, «···» %v: %s", row.Pills, row.Buttons, row.More, row.Text)
		}
	}
	if strings.Join(list.HeaderButtons, "|") != "Добавить сервер MCP" {
		t.Errorf("кнопки шапки раздела: %v", list.HeaderButtons)
	}
	if len(list.Subtabs) != 0 {
		t.Errorf("у раздела появилась полоса вкладок: %v", list.Subtabs)
	}
	if list.SW > list.CW {
		t.Errorf("список едет вбок: %d при %d", list.SW, list.CW)
	}
	// «Тонкая настройка» и ленивый журнал — внизу, журнал последним и пока закрыт
	var tail struct {
		Fine    bool `json:"fine"`
		Journal bool `json:"journal"`
		Last    bool `json:"last"`
		Rows    int  `json:"rows"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const fine = document.getElementById('fold-rare'); const log = document.getElementById('fold-mcp-log');
	  const kids = Array.from(document.querySelectorAll('#feed > .column > *'));
	  return {fine: !!fine, journal: !!log && !log.open, last: !!log && kids[kids.length - 1] === log.closest('#feed > .column > *') || (!!log && kids[kids.length - 1].contains(log)),
	    rows: log ? log.querySelectorAll('.row-wrap').length : -1}; })()`, &tail)); err != nil {
		t.Fatal(err)
	}
	if !tail.Fine || !tail.Journal || !tail.Last || tail.Rows != 0 {
		t.Errorf("«Тонкая настройка» и журнал внизу: %+v", tail)
	}

	// журнал приезжает по раскрытию
	if err := chromedp.Run(ctx, chromedp.Click(`#fold-mcp-log > summary`, chromedp.ByQuery)); err != nil {
		t.Fatalf("раскрыть журнал: %v", err)
	}
	mcpPoll(t, ctx, "строки общего журнала приехали", `document.querySelectorAll('#fold-mcp-log .row-wrap').length >= 20`)

	// ---- в сервер кликом по строке ----
	mcpClickServer(t, ctx, "sing")
	host := mcpRead(t, ctx)
	if host.Crumb != "‹ Интеграции" || host.H1 != "sing" || !host.BarVisible || host.BarButtons != 1 || !host.BarMore ||
		!strings.Contains(host.BarText, "mcp.planner-app.example") || !strings.HasPrefix(host.SelectedTab, "Обзор") {
		t.Errorf("экран сервера: %+v", host)
	}
	if strings.Join(host.Subtabs, "|") == "" || !strings.Contains(strings.Join(host.Subtabs, "|"), "Правила") {
		t.Errorf("вкладки: %v", host.Subtabs)
	}
	if !host.Danger || !host.DangerLast || host.DangerButtons != 2 {
		t.Errorf("«Опасное» должно стоять последним с двумя видимыми кнопками: %+v", host)
	}
	karkasShot(t, ctx, "m1-overview")

	// «Выйти…» и «Удалить…» спрашивают, а «Отмена» оставляет всё на месте
	for _, label := range []string{"Выйти", "Удалить"} {
		if err := chromedp.Run(ctx, chromedp.Click(`//*[@id='sec-mcp-danger']//button[contains(.,'`+label+`')]`, chromedp.BySearch)); err != nil {
			t.Fatalf("нажать «%s…»: %v", label, err)
		}
		mcpPoll(t, ctx, "окно вопроса открыто", `!!document.querySelector('dialog#confirm[open]')`)
		if err := chromedp.Run(ctx, chromedp.Click(`dialog#confirm button.cancel`, chromedp.ByQuery)); err != nil {
			t.Fatal(err)
		}
		mcpPoll(t, ctx, "окно вопроса закрыто", `!document.querySelector('dialog#confirm[open]')`)
	}
	if _, ok := st.MCPServer("sing"); !ok {
		t.Fatal("«Отмена» удалила сервер")
	}
	if _, has := st.Secret("mcp:sing:oauth"); !has {
		t.Fatal("«Отмена» стёрла токены входа")
	}

	// ---- «В чате»: поля строками, «Сохранить» полосой ----
	const keywords = `input[name="keywords"]`
	if err := chromedp.Run(ctx, chromedp.Click(keywords, chromedp.ByQuery), chromedp.SendKeys(keywords, kb.End+", канбан", chromedp.ByQuery)); err != nil {
		t.Fatalf("вписать слова-приметы: %v", err)
	}
	mcpPoll(t, ctx, "полоса: одна правка «Когда звать»",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1') && bar.textContent.includes('Когда звать'); })()`)
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'field-segments')][@aria-label='Класс данных']//label[contains(@class,'segment')][normalize-space(.)='чувствительное']`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать класс «чувствительное»: %v", err)
	}
	// переключатель выше середины окна лежит под закреплённой строкой сервера: подвести его к середине,
	// как это делает рука прокруткой, и нажать
	mcpScrollToCenter(t, ctx, toggleSel("chat"))
	if err := chromedp.Run(ctx, chromedp.Click(toggleSel("chat"), chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Открыт чату»: %v", err)
	}
	mcpPoll(t, ctx, "полоса: три правки", `(() => { const bar = document.getElementById('save-bar'); return !bar.hidden && bar.textContent.includes('Изменено 3'); })()`)
	karkasShot(t, ctx, "m1b-overview-edited")
	serversSave(t, ctx)
	m, _ := st.MCPServer("sing")
	if m.Keywords != "задача, привычка, проект, канбан" || m.Class != store.ClassSensitive || m.Chat {
		t.Fatalf("в базе после «Сохранить»: слова %q, класс %q, чат %v", m.Keywords, m.Class, m.Chat)
	}
	// перезагрузка: значения на месте
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(mcpScreen, "sing", ""))
	mcpPoll(t, ctx, "после перезагрузки «В чате» на месте",
		`(() => { const k = document.querySelector('`+keywords+`'); const c = document.querySelector('input[name="chat"]');
		  return !!k && k.value === 'задача, привычка, проект, канбан' && !!c && !c.checked; })()`)

	// ---- «Подсказки»: «Забыть» одну настоящим нажатием, «Забыть все» под вопросом ----
	mcpPoll(t, ctx, "подсказки нарисованы", `document.querySelectorAll('#sec-mcp-hints-section .row-wrap').length === 3`)
	if err := chromedp.Run(ctx, chromedp.Click(`(//*[@id='sec-mcp-hints-section']//button[normalize-space(.)='Забыть'])[2]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать «Забыть» у подсказки: %v", err)
	}
	mcpPoll(t, ctx, "осталась одна подсказка", `document.querySelectorAll('#sec-mcp-hints-section .row-wrap').length === 2`)
	if m, _ := st.MCPServer("sing"); len(m.ToolHints) != 1 {
		t.Errorf("подсказок в базе %d, а должна остаться одна", len(m.ToolHints))
	}

	// ---- «Правила»: поиск, ответ у инструмента, полоса, «Новые» ----
	serversClickTab(t, ctx, "Правила", entityAddr(mcpScreen, "sing", mcpTabRules))
	mcpPoll(t, ctx, "группы правил нарисованы", `document.querySelectorAll('form.deferred[data-form^="mcp-rules-sing-"]').length > 5`)
	karkasShot(t, ctx, "m2-rules")
	// верхнее поле ищет инструменты: «checklist» оставляет шесть
	if err := chromedp.Run(ctx, chromedp.Click(`#search`, chromedp.ByQuery), chromedp.SendKeys(`#search`, "checklist", chromedp.ByQuery)); err != nil {
		t.Fatalf("набрать поиск: %v", err)
	}
	mcpPoll(t, ctx, "на экране шесть чек-листов",
		`(() => { const names = Array.from(document.querySelectorAll('form.deferred[data-form^="mcp-rules-sing-"] .field')).filter((f) => f.querySelector('.field-segments') && f.querySelector('label').textContent.trim().indexOf('Всем') < 0).map((f) => f.querySelector('label').textContent.trim());
		  return names.length === 6 && names.every((n) => n.startsWith('checklist_item_')); })()`)
	// набор букв шлёт запрос на каждой паузе: ждём, пока последний ответ осядет, иначе он
	// перерисует экран под нажатием
	mcpPoll(t, ctx, "запросы поиска закончились", `!document.querySelector('.htmx-request')`)
	karkasShot(t, ctx, "m2b-rules-search")
	mcpScrollXPath(t, ctx, segmentXPath("checklist_item_check", "Разрешить"))
	if err := chromedp.Run(ctx, chromedp.Click(segmentXPath("checklist_item_check", "Разрешить"), chromedp.BySearch)); err != nil {
		t.Fatalf("нажать «Разрешить» у checklist_item_check: %v", err)
	}
	mcpPoll(t, ctx, "полоса называет правку",
		`(() => { const bar = document.getElementById('save-bar'); return !bar.hidden && bar.textContent.includes('Изменено 1') && bar.textContent.includes('checklist_item_check') && bar.textContent.includes('Спросить → Разрешить'); })()`)
	serversSave(t, ctx)
	m, _ = st.MCPServer("sing")
	if m.Rules["checklist_item_check"] != store.RuleAllow {
		t.Fatalf("в базе: %v", m.Rules["checklist_item_check"])
	}
	if p, _ := engine.ClassifyMCP(m, "checklist_item_check"); p != engine.PolicyAllow {
		t.Errorf("движок читает правило как %q", p)
	}
	// чип «Новые»: три инструмента без решения, и все они остались запрещёнными
	// стираем поиск настоящими нажатиями и ждём, пока вернутся все группы
	if err := chromedp.Run(ctx, chromedp.Click(`#search`, chromedp.ByQuery), chromedp.SendKeys(`#search`, kb.End+strings.Repeat(kb.Backspace, len("checklist")), chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	mcpPoll(t, ctx, "поиск стёрт, группы вернулись", `document.querySelectorAll('form.deferred[data-form^="mcp-rules-sing-"]').length > 5`)
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'filters-bar')]//a[contains(@class,'segment')][contains(.,'Новые')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать чип «Новые»: %v", err)
	}
	mcpPoll(t, ctx, "чип «Новые»: три инструмента, ничего не выбрано",
		`(() => { const f = Array.from(document.querySelectorAll('form.deferred[data-form^="mcp-rules-sing-"] .field')).filter((p) => p.querySelector('.field-segments') && p.querySelector('label').textContent.trim().indexOf('Всем') < 0);
		  return f.length === 3 && f.every((p) => !p.querySelector('input[type=radio]:checked')); })()`)
	for _, name := range []string{"tag_update", "time_stat_create", "time_stat_update"} {
		if p, _ := engine.ClassifyMCP(m, name); p != engine.PolicyDeny {
			t.Errorf("%s без решения: %q", name, p)
		}
	}

	// ---- «Журнал»: двадцать строк, «Показать ещё», чипы ----
	serversClickTab(t, ctx, "Журнал", entityAddr(mcpScreen, "sing", mcpTabLog))
	mcpPoll(t, ctx, "двадцать строк журнала", `document.querySelectorAll('#feed .section:not(#sec-entity) .row-wrap').length === 20`)
	karkasShot(t, ctx, "m3-log")
	if err := chromedp.Run(ctx, chromedp.Click(`#feed [data-action="$more"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Показать ещё»: %v", err)
	}
	mcpPoll(t, ctx, "после «Показать ещё» строк больше двадцати", `document.querySelectorAll('#feed .section:not(#sec-entity) .row-wrap').length > 20`)
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'filters-bar')]//a[contains(@class,'segment')][contains(.,'Ошибки')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать чип «Ошибки»: %v", err)
	}
	mcpPoll(t, ctx, "чип «Ошибки»: одна строка, и это ошибка по аргументам",
		`(() => { const rows = document.querySelectorAll('#feed .section:not(#sec-entity) .row-wrap'); return rows.length === 1 && rows[0].textContent.includes('task_update') && rows[0].textContent.includes('ошибка'); })()`)

	// ---- «‹ Интеграции» возвращает в список ----
	if err := chromedp.Run(ctx, chromedp.Click(`#screen .crumbs .crumb`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	mcpOpenList(t, ctx)
}

// «Добавить сервер MCP»: из списка кнопкой шапки, три поля строками и «Ещё 7
// полей» свёрнуто; вписанное из свёрнутых полей доезжает до базы.
func TestBrowserMCPAdd(t *testing.T) {
	serversBrowserOnly(t)
	s, st := mcpScreensFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, mcpScreen)
	mcpOpenList(t, ctx)

	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'controls')]/button[contains(.,'Добавить сервер MCP')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать «Добавить сервер MCP»: %v", err)
	}
	addr := entityAddr(mcpScreen, mcpNew, "")
	waitScreen(t, ctx, addr)
	mcpPoll(t, ctx, "форма добавления нарисована", `!!document.querySelector('form input[name="name"]')`)
	add := mcpRead(t, ctx)
	if add.Crumb != "‹ Интеграции" || add.H1 != "Добавить сервер MCP" {
		t.Errorf("шапка экрана добавления: %+v", add)
	}
	// три поля на виду, остальное — под «Ещё 7 полей»
	mcpPoll(t, ctx, "«Ещё 7 полей» свёрнуто, «Что это» не видно",
		`(() => { const d = document.querySelector('details.more-fields'); const a = document.querySelector('input[name="about"]');
		  return !!d && !d.open && d.textContent.includes('Ещё 7 полей') && !a.checkVisibility() &&
		    ['name', 'url', 'secret'].every((n) => document.querySelector('input[name="' + n + '"]').checkVisibility()); })()`)
	karkasShot(t, ctx, "m4-new")
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`form input[name="name"]`, "wiki2", chromedp.ByQuery),
		chromedp.SendKeys(`form input[name="url"]`, "http://127.0.0.1:1/mcp", chromedp.ByQuery),
		chromedp.Click(`details.more-fields > summary`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	mcpPoll(t, ctx, "редкие поля видны после раскрытия", `document.querySelector('input[name="about"]').checkVisibility()`)
	karkasShot(t, ctx, "m4b-new-open")
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`input[name="about"]`, "Вики для заметок", chromedp.ByQuery),
		chromedp.SendKeys(`input[name="keywords"]`, "вики, заметка", chromedp.ByQuery),
		chromedp.Click(`form .form-actions button`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(mcpScreen, "wiki2", ""))
	m, ok := st.MCPServer("wiki2")
	if !ok || m.URL != "http://127.0.0.1:1/mcp" || m.About != "Вики для заметок" || m.Keywords != "вики, заметка" || !m.Chat {
		t.Fatalf("после добавления в базе: %+v (%v)", m, ok)
	}
	// человек оказывается на экране нового сервера; проверка связи с закрытым портом сказала причину
	mcpPoll(t, ctx, "экран нового сервера нарисован", `!!document.getElementById('sec-entity') && document.getElementById('sec-entity').textContent.includes('wiki2')`)
	mcpPoll(t, ctx, "состояние сервера названо словом", `!!document.querySelector('#sec-entity .pill')`)
}

// Телефон 390×844: список, сервер с одной кнопкой и «···», вкладки одной
// полосой, правила с полем «Найти инструмент» и без бокового сдвига; «Добавить
// сервер MCP».
func TestBrowserMCPPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, st := mcpScreensFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)

	karkasOpen(t, ctx, mcpScreen)
	mcpOpenList(t, ctx)
	list := mcpRead(t, ctx)
	if list.Crumb != "‹ Настройки" || list.SW > list.CW {
		t.Errorf("список на телефоне: «назад» %q, ширина %d при %d", list.Crumb, list.SW, list.CW)
	}
	for _, row := range list.Rows {
		if row.Pills > 2 || row.Buttons > 1 {
			t.Errorf("строка на телефоне не тонкая: %s", row.Text)
		}
	}
	if strings.Join(list.HeaderButtons, "|") != "Добавить сервер MCP" {
		t.Errorf("шапка раздела на телефоне: %v", list.HeaderButtons)
	}
	karkasShot(t, ctx, "p1-list")

	// настоящее касание строки
	if err := chromedp.Run(ctx, chromedp.Click(mcpRowsSel+`[data-goto="`+entityAddr(mcpScreen, "sing", "")+`"] a.row-link`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(mcpScreen, "sing", ""))
	mcpPoll(t, ctx, "экран сервера нарисован", `!!document.getElementById('sec-entity')`)
	host := mcpRead(t, ctx)
	if host.Crumb != "‹ Интеграции" || host.BarButtons != 1 || !host.BarMore || host.SW > host.CW {
		t.Errorf("шапка сервера на телефоне: «назад» %q, кнопок %d, «···» %v, ширина %d при %d", host.Crumb, host.BarButtons, host.BarMore, host.SW, host.CW)
	}
	karkasShot(t, ctx, "p2-overview")
	// «В чате» на телефоне: подпись и подсказка на ширину, контрол под ними
	var layout struct {
		LabelBottom float64 `json:"labelBottom"`
		FieldTop    float64 `json:"fieldTop"`
		FieldRight  float64 `json:"fieldRight"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const row = document.querySelector('.field.setting:has(input[name="keywords"])');
	  const label = row.querySelector('.setting-label').getBoundingClientRect(); const input = row.querySelector('input[name="keywords"]').getBoundingClientRect();
	  return {labelBottom: label.bottom, fieldTop: input.top, fieldRight: input.right}; })()`, &layout)); err != nil {
		t.Fatal(err)
	}
	if layout.FieldTop < layout.LabelBottom || layout.FieldRight > 390 {
		t.Errorf("поле «Когда звать» на телефоне: %+v", layout)
	}

	// правила: вкладки одной полосой, верхнее поле — «Найти инструмент», вбок ничего не едет
	serversClickTab(t, ctx, "Правила", entityAddr(mcpScreen, "sing", mcpTabRules))
	mcpPoll(t, ctx, "группы правил нарисованы", `document.querySelectorAll('form.deferred[data-form^="mcp-rules-sing-"]').length > 5`)
	var search struct {
		Visible     bool   `json:"visible"`
		Placeholder string `json:"placeholder"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const i = document.getElementById('search'); return {visible: !!i && i.getBoundingClientRect().width > 0 && getComputedStyle(i).display !== 'none', placeholder: i ? i.placeholder : ''}; })()`, &search)); err != nil {
		t.Fatal(err)
	}
	if !search.Visible || !strings.Contains(search.Placeholder, "Найти инструмент") {
		t.Errorf("на телефоне у «Правил» нет поля «Найти инструмент»: %+v", search)
	}
	rules := mcpRead(t, ctx)
	if rules.SW > rules.CW {
		t.Errorf("«Правила» едут вбок: %d при %d", rules.SW, rules.CW)
	}
	karkasShot(t, ctx, "p3-rules")
	// поиск на телефоне настоящим вводом
	if err := chromedp.Run(ctx, chromedp.Click(`#search`, chromedp.ByQuery), chromedp.SendKeys(`#search`, "checklist", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	mcpPoll(t, ctx, "на телефоне поиск оставил шесть чек-листов",
		`(() => { const names = Array.from(document.querySelectorAll('form.deferred[data-form^="mcp-rules-sing-"] .field')).filter((f) => f.querySelector('.field-segments') && f.querySelector('label').textContent.trim().indexOf('Всем') < 0);
		  return names.length === 6; })()`)
	// ответ у инструмента касанием и «Сохранить» над нижней полосой приложения
	mcpScrollXPath(t, ctx, segmentXPath("checklist_item_uncheck", "Разрешить"))
	if err := chromedp.Run(ctx, chromedp.Click(segmentXPath("checklist_item_uncheck", "Разрешить"), chromedp.BySearch)); err != nil {
		t.Fatal(err)
	}
	mcpPoll(t, ctx, "полоса сохранения над нижней полосой приложения",
		`(() => { const bar = document.getElementById('save-bar'); const tab = document.getElementById('tabbar'); return !!bar && !bar.hidden && bar.getBoundingClientRect().bottom <= tab.getBoundingClientRect().top + 1; })()`)
	serversSave(t, ctx)
	if m, _ := st.MCPServer("sing"); m.Rules["checklist_item_uncheck"] != store.RuleAllow {
		t.Errorf("с телефона ответ не сохранился: %v", m.Rules["checklist_item_uncheck"])
	}

	// журнал и «Добавить» не едут вбок
	for _, screen := range []string{entityAddr(mcpScreen, "sing", mcpTabLog), entityAddr(mcpScreen, mcpNew, ""), entityAddr(mcpScreen, "wiki", "")} {
		karkasOpen(t, ctx, screen)
		v := mcpRead(t, ctx)
		if v.SW > v.CW {
			t.Errorf("%s едет вбок: %d при %d", screen, v.SW, v.CW)
		}
	}
}

// Снимки для ревью (SEMA_SHOTS=<папка>): десктоп 1290×806 — список, «Обзор»
// (вошли), «Обзор» (нужен вход), «Правила», «Журнал», «Добавить сервер MCP».
// Без SEMA_SHOTS ничего не снимается, а страницы просто собираются.
func TestBrowserMCPShots(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := mcpScreensFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, mcpScreen)
	mcpOpenList(t, ctx)
	for _, shot := range []struct{ name, screen string }{
		{"desktop-1-list", mcpScreen},
		{"desktop-2-overview-logged-in", entityAddr(mcpScreen, "sing", "")},
		{"desktop-3-overview-needs-login", entityAddr(mcpScreen, "wiki", "")},
		{"desktop-4-rules", entityAddr(mcpScreen, "sing", mcpTabRules)},
		{"desktop-5-journal", entityAddr(mcpScreen, "sing", mcpTabLog)},
		{"desktop-6-add", entityAddr(mcpScreen, mcpNew, "")},
		{"desktop-7-overview-key", entityAddr(mcpScreen, "ключ", "")},
		{"desktop-8-overview-agent", entityAddr(mcpScreen, "код", "")},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	// низ «Обзора»: подсказки и «Опасное» красной карточкой
	karkasOpen(t, ctx, entityAddr(mcpScreen, "sing", ""))
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-2b-overview-bottom")
	// «Тонкая настройка» и общий журнал на списке раскрыты
	karkasOpen(t, ctx, mcpScreen)
	rowsOpenFold(t, ctx)
	karkasShot(t, ctx, "desktop-1b-list-fine")
	if err := chromedp.Run(ctx, chromedp.Click(`#fold-rare > summary`, chromedp.ByQuery),
		chromedp.Click(`#fold-mcp-log > summary`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	mcpPoll(t, ctx, "общий журнал раскрыт", `document.querySelectorAll('#fold-mcp-log .row-wrap').length >= 20`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('fold-mcp-log').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-1c-list-journal")
}

// Телефон 390×844: список, «Обзор», «Правила».
func TestBrowserMCPShotsPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := mcpScreensFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)
	for _, shot := range []struct{ name, screen string }{
		{"phone-1-list", mcpScreen},
		{"phone-2-overview", entityAddr(mcpScreen, "sing", "")},
		{"phone-3-rules", entityAddr(mcpScreen, "sing", mcpTabRules)},
		{"phone-4-journal", entityAddr(mcpScreen, "sing", mcpTabLog)},
		{"phone-5-add", entityAddr(mcpScreen, mcpNew, "")},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
}
