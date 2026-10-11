package api

// «Серверы» в настоящем Chrome: настоящие нажатия и ввод, ожидание условий на
// странице вместо пауз. Состояние проверяется и на странице, и в базе — вызовом
// функции вместо жеста ничего не доказать.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// serversClickTab — нажать внутреннюю вкладку по имени и дождаться её адреса.
func serversClickTab(t *testing.T, ctx context.Context, name, screen string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'subtabs-bar')]//a[contains(@class,'segment')][contains(.,'`+name+`')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать вкладку «%s»: %v", name, err)
	}
	waitScreen(t, ctx, screen)
	serversPoll(t, ctx, "экран перечитан", `!document.querySelector('.htmx-request')`)
}

// serversClickHost — нажать строку сервера в списке: по названию, как мышью.
func serversClickHost(t *testing.T, ctx context.Context, host string) {
	t.Helper()
	sel := `#feed .row-wrap > .row.clickable[data-goto="` + entityAddr(serversScreen, host, "") + `"] a.row-link`
	if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать строку «%s»: %v", host, err)
	}
	waitScreen(t, ctx, entityAddr(serversScreen, host, ""))
	serversPoll(t, ctx, "экран сервера нарисован", `!!document.getElementById('sec-entity')`)
}

// serversSave — «Сохранить» нижней полосой и ждать, пока она погаснет.
func serversSave(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	serversPoll(t, ctx, "полоса погасла после сохранения", `(() => { const bar = document.getElementById('save-bar'); return !!bar && bar.hidden && !document.querySelector('.htmx-request'); })()`)
}

// Список → сервер → вкладки → окно и уровень полями → «‹ Серверы». Состояние
// после каждого шага читается и со страницы, и из базы.
func TestBrowserServersWalk(t *testing.T) {
	serversBrowserOnly(t)
	s, st := serversFix(t, true)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, serversScreen)
	waitScreen(t, ctx, serversScreen)
	serversPoll(t, ctx, "список машин нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:servers@"]').length === 5`)

	// ---- список: тонкие строки, полка, одна кнопка в шапке ----
	list := serversRead(t, ctx)
	if len(list.Rows) != 5 || !list.Shelf || !strings.Contains(list.ShelfText, "Ждут вас") {
		t.Fatalf("список: строк %d, полка %v: %+v", len(list.Rows), list.Shelf, list)
	}
	for _, row := range list.Rows {
		if row.Pills > 2 || row.Buttons > 1 || !row.More {
			t.Errorf("строка не тонкая — капсул %d, кнопок %d, «···» %v: %s", row.Pills, row.Buttons, row.More, row.Text)
		}
	}
	if strings.Join(list.HeaderButtons, "|") != "Добавить сервер" {
		t.Errorf("кнопки шапки раздела: %v", list.HeaderButtons)
	}
	if list.SW > list.CW {
		t.Errorf("список едет вбок: %d при %d", list.SW, list.CW)
	}
	karkasShot(t, ctx, "w1-list")

	// ---- в сервер кликом по строке ----
	serversClickHost(t, ctx, "srv-vpn")
	host := serversRead(t, ctx)
	if host.Crumb != "‹ Серверы" || host.H1 != "srv-vpn" || !host.BarVisible || host.BarButtons != 1 || !host.BarMore ||
		!strings.Contains(host.BarText, "Linux") || !strings.HasPrefix(host.SelectedTab, "Обзор") || !strings.Contains(host.SelectedTab, "2 ждут") {
		t.Errorf("экран сервера: %+v", host)
	}
	if !host.Danger || !host.DangerLast || host.DangerButtons < 1 {
		t.Errorf("«Опасное» должно стоять последним и с видимой кнопкой: %+v", host)
	}
	karkasShot(t, ctx, "w2-host-overview")

	// «Удалить…» спрашивает, а «Отмена» оставляет сервер на месте
	if err := chromedp.Run(ctx, chromedp.Click(`#sec-host-danger button[data-confirm]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Удалить…»: %v", err)
	}
	serversPoll(t, ctx, "окно вопроса открыто", `!!document.querySelector('dialog#confirm[open]')`)
	karkasShot(t, ctx, "w2b-confirm-delete")
	if err := chromedp.Run(ctx, chromedp.Click(`dialog#confirm button.cancel`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	serversPoll(t, ctx, "окно вопроса закрыто", `!document.querySelector('dialog#confirm[open]')`)
	if _, ok := st.Host("srv-vpn"); !ok {
		t.Fatal("«Отмена» удалила сервер")
	}

	// ---- «Самостоятельность»: окно полями времени ----
	serversClickTab(t, ctx, "Самостоятельность", entityAddr(serversScreen, "srv-vpn", serversTabAuto))
	karkasShot(t, ctx, "w3-host-auto")
	const from, to = `input[name="window_from"]`, `input[name="window_to"]`
	serversPoll(t, ctx, "поля окна нарисованы со значением",
		`(() => { const a = document.querySelector('`+from+`'), b = document.querySelector('`+to+`'); return !!a && !!b && a.value === '02:00' && b.value === '06:00'; })()`)
	// набор цифр в поле времени, как руками: фокус на часы и четыре цифры
	for _, f := range []struct{ sel, keys string }{{from, "0300"}, {to, "0545"}} {
		// без предварительного нажатия: оно попало бы в середину поля, на минуты, а
		// фокус клавиатурой встаёт на часы
		if err := chromedp.Run(ctx, chromedp.Focus(f.sel, chromedp.ByQuery), chromedp.SendKeys(f.sel, f.keys, chromedp.ByQuery)); err != nil {
			t.Fatalf("набрать %s в %s: %v", f.keys, f.sel, err)
		}
	}
	serversPoll(t, ctx, "полоса сохранения с одной правкой окна",
		`(() => { const a = document.querySelector('`+from+`'), b = document.querySelector('`+to+`'); const bar = document.getElementById('save-bar');
		  return a.value === '03:00' && b.value === '05:45' && !!bar && !bar.hidden && bar.textContent.includes('Изменено 1') && bar.textContent.includes('«Окно» — 02:00–06:00 → 03:00–05:45'); })()`)
	karkasShot(t, ctx, "w3b-host-auto-edited")
	serversSave(t, ctx)
	if h, _ := st.Host("srv-vpn"); h.Window != "03:00-05:45" {
		t.Fatalf("в базе окно %q, а набрано 03:00-05:45", h.Window)
	}
	// движок видит новое окно: 04:00 внутри, 06:00 снаружи
	if h, _ := st.Host("srv-vpn"); !engine.AutoWindowOK(h.Window, time.Date(2026, 10, 1, 4, 0, 0, 0, time.Local)) || engine.AutoWindowOK(h.Window, time.Date(2026, 10, 1, 6, 0, 0, 0, time.Local)) {
		t.Errorf("движок читает окно %q неверно", h.Window)
	}
	// перезагрузка: значения на месте
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(serversScreen, "srv-vpn", serversTabAuto))
	serversPoll(t, ctx, "после перезагрузки окно на месте",
		`(() => { const a = document.querySelector('`+from+`'), b = document.querySelector('`+to+`'); return !!a && !!b && a.value === '03:00' && b.value === '05:45'; })()`)

	// «круглые сутки» очищает оба поля, а сохраняет их та же полоса
	if err := chromedp.Run(ctx, chromedp.Click(`.field.setting:has(input[name="window_from"]) .to-default`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «круглые сутки»: %v", err)
	}
	serversPoll(t, ctx, "оба поля пусты, полоса ждёт сохранения",
		`(() => { const bar = document.getElementById('save-bar'); return document.querySelector('`+from+`').value === '' && document.querySelector('`+to+`').value === '' && !bar.hidden; })()`)
	serversSave(t, ctx)
	if h, _ := st.Host("srv-vpn"); h.Window != "" {
		t.Fatalf("в базе окно %q после «круглые сутки»", h.Window)
	}

	// ---- уровень: сегменты настоящим нажатием, в том числе «ручное» ----
	for _, lvl := range []struct{ label, want string }{{"под судом", store.AutonomyCourt}, {"ручное", store.AutonomyOff}, {"полная", store.AutonomyFull}} {
		if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'field-segments')]//label[contains(@class,'segment')][normalize-space(.)='`+lvl.label+`']`, chromedp.BySearch)); err != nil {
			t.Fatalf("нажать «%s»: %v", lvl.label, err)
		}
		serversPoll(t, ctx, "полоса ждёт сохранения уровня «"+lvl.label+"»",
			`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Уровень'); })()`)
		serversSave(t, ctx)
		if h, _ := st.Host("srv-vpn"); h.Autonomy != lvl.want || h.Auto != (lvl.want != store.AutonomyOff) {
			t.Fatalf("после «%s» в базе уровень %q (Auto %v)", lvl.label, h.Autonomy, h.Auto)
		}
	}

	// ---- «‹ Серверы» возвращает в список ----
	if err := chromedp.Run(ctx, chromedp.Click(`#screen .crumbs .crumb`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, serversScreen)
	serversPoll(t, ctx, "список вернулся", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:servers@"]').length === 5`)
}

// Правила (добавить образец, проверить команду), журнал (первая страница,
// «Показать ещё», чипы) и «Добавить сервер» — настоящими нажатиями.
func TestBrowserServersRulesJournalAdd(t *testing.T) {
	serversBrowserOnly(t)
	s, st := serversFix(t, true)
	serversManyLog(t, st, "srv-vpn", 30)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, serversScreen)
	waitScreen(t, ctx, serversScreen)
	serversPoll(t, ctx, "список машин нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:servers@"]').length === 5`)
	serversClickHost(t, ctx, "srv-vpn")

	// ---- правила: образец в колонку «Разрешить», проверка команды ----
	serversClickTab(t, ctx, "Правила", entityAddr(serversScreen, "srv-vpn", serversTabRules))
	karkasShot(t, ctx, "w4-host-rules")
	serversPoll(t, ctx, "три колонки нарисованы", `document.querySelectorAll('.pattern-col').length === 3`)
	if got, _ := s.eng.Classify("srv-vpn", "pwgen 12"); got != engine.PolicyDeny {
		t.Fatalf("до правки pwgen должен быть запрещён, а он %q", got)
	}
	const allow = `.pattern-col.ok .pat-add input`
	if err := chromedp.Run(ctx, chromedp.Click(allow, chromedp.ByQuery), chromedp.SendKeys(allow, "pwgen", chromedp.ByQuery),
		chromedp.Click(`.pattern-col.ok .pat-add button`, chromedp.ByQuery)); err != nil {
		t.Fatalf("добавить образец: %v", err)
	}
	serversPoll(t, ctx, "образец появился в колонке «Разрешить»",
		`Array.from(document.querySelectorAll('.pattern-col.ok .pat > span:first-child')).some((p) => p.textContent === 'pwgen')`)
	if got, _ := s.eng.Classify("srv-vpn", "pwgen 12"); got != engine.PolicyAllow {
		t.Errorf("движок: pwgen после добавления %q", got)
	}
	if !strings.HasSuffix(serversRead(t, ctx).Screen, "~rules") {
		t.Errorf("после добавления образца ушли с вкладки «Правила»: %q", serversRead(t, ctx).Screen)
	}

	// проверка команды: сработавший образец подсвечен, ответ — «Запретить»
	const cmd = `input[name="cmd"]`
	if err := chromedp.Run(ctx, chromedp.Click(cmd, chromedp.ByQuery), chromedp.SendKeys(cmd, "rm -rf /tmp/x", chromedp.ByQuery),
		chromedp.Click(`//form[.//input[@name='cmd']]//button[contains(@class,'action')][contains(.,'Проверить')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("проверить команду: %v", err)
	}
	serversPoll(t, ctx, "ответ проверки: «Запретить» по образцу rm -rf",
		`(() => { const v = document.getElementById('sec-rules-verdict') || document.querySelector('[id*="rules-verdict"]');
		  return document.body.textContent.includes('rm -rf /tmp/x') && !!document.querySelector('.pattern-col.alert .pat.hit'); })()`)
	karkasShot(t, ctx, "w4b-host-rules-verdict")

	// ---- журнал: двадцать строк, «Показать ещё», чипы ----
	serversClickTab(t, ctx, "Журнал", entityAddr(serversScreen, "srv-vpn", serversTabLog))
	serversPoll(t, ctx, "двадцать строк журнала", `document.querySelectorAll('#feed .section:not(#sec-entity) .row-wrap').length === 20`)
	karkasShot(t, ctx, "w5-host-log")
	if err := chromedp.Run(ctx, chromedp.Click(`#feed [data-action="$more"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Показать ещё»: %v", err)
	}
	serversPoll(t, ctx, "после «Показать ещё» строк больше двадцати", `document.querySelectorAll('#feed .section:not(#sec-entity) .row-wrap').length > 20`)
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'filters-bar')]//a[contains(@class,'segment')][contains(.,'Отказы')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать чип «Отказы»: %v", err)
	}
	serversPoll(t, ctx, "чип «Отказы»: одна строка, и это отказ",
		`(() => { const rows = document.querySelectorAll('#feed .section:not(#sec-entity) .row-wrap'); return rows.length === 1 && rows[0].textContent.includes('rm -rf /var/log/old'); })()`)

	// ---- «Добавить сервер»: из списка кнопкой шапки ----
	karkasOpen(t, ctx, serversScreen)
	serversPoll(t, ctx, "список вернулся", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:servers@"]').length === 5`)
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'controls')]/button[contains(.,'Добавить сервер')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать «Добавить сервер»: %v", err)
	}
	waitScreen(t, ctx, entityAddr(serversScreen, serversNew, ""))
	serversPoll(t, ctx, "форма добавления нарисована", `!!document.querySelector('form input[name="name"]')`)
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`form input[name="name"]`, "web-9", chromedp.ByQuery),
		chromedp.SendKeys(`form input[name="addr"]`, "198.51.100.9", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	// порт и sudo под «Ещё 2 поля»: свёрнуты, пока их не раскрыли
	serversPoll(t, ctx, "«Ещё 2 поля» свёрнуто, порт не виден",
		`(() => { const d = document.querySelector('details.more-fields'); const p = document.querySelector('input[name="port"]');
		  return !!d && !d.open && d.textContent.includes('Ещё 2 поля') && !p.checkVisibility(); })()`)
	karkasShot(t, ctx, "w6-new")
	if err := chromedp.Run(ctx, chromedp.Click(`details.more-fields > summary`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	serversPoll(t, ctx, "порт виден после раскрытия", `document.querySelector('input[name="port"]').checkVisibility()`)
	karkasShot(t, ctx, "w6b-new-open")
	if err := chromedp.Run(ctx,
		chromedp.Clear(`input[name="port"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="port"]`, "2022", chromedp.ByQuery),
		chromedp.Click(`form .form-actions button`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(serversScreen, "web-9", ""))
	h, ok := st.Host("web-9")
	if !ok || h.Addr != "198.51.100.9" || h.Port != 2022 || !h.NoSudo || h.User != "root" {
		t.Fatalf("после добавления в базе: %+v (%v)", h, ok)
	}
	// фоновая проверка связи упирается в неразбираемый ключ раньше сети
	serversPoll(t, ctx, "экран нового сервера нарисован", `!!document.getElementById('sec-entity')`)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h, _ := st.Host("web-9"); h.LastErr != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("фоновая проверка связи не кончилась")
}

// Телефон 390×844: список, сервер с одной кнопкой и «···», вкладки одной
// полосой с прокруткой, окно полями без бокового сдвига.
func TestBrowserServersPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, st := serversFix(t, true)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)

	karkasOpen(t, ctx, serversScreen)
	serversPoll(t, ctx, "список машин нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:servers@"]').length === 5`)
	list := serversRead(t, ctx)
	if list.Crumb != "‹ Настройки" || list.SW > list.CW {
		t.Errorf("список на телефоне: «назад» %q, ширина %d при %d", list.Crumb, list.SW, list.CW)
	}
	for _, row := range list.Rows {
		if row.Pills > 2 || row.Buttons > 1 {
			t.Errorf("строка на телефоне не тонкая: %s", row.Text)
		}
	}
	karkasShot(t, ctx, "p1-list")
	// строки машин под полкой
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#feed .row-wrap > .row.clickable[data-goto="settings:servers@Microtik"]').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "p1b-list-machines")

	// настоящее касание строки
	if err := chromedp.Run(ctx, chromedp.Click(`#feed .row-wrap > .row.clickable[data-goto="settings:servers@srv-vpn"] a.row-link`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(serversScreen, "srv-vpn", ""))
	serversPoll(t, ctx, "экран сервера нарисован", `!!document.getElementById('sec-entity')`)
	host := serversRead(t, ctx)
	if host.Crumb != "‹ Серверы" || host.BarButtons != 1 || !host.BarMore || host.SW > host.CW {
		t.Errorf("шапка сервера на телефоне: «назад» %q, кнопок %d, «···» %v, ширина %d при %d", host.Crumb, host.BarButtons, host.BarMore, host.SW, host.CW)
	}
	var selectShown bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const s = document.querySelector('#sec-entity .select'); return !!s && s.getBoundingClientRect().width > 0; })()`, &selectShown)); err != nil {
		t.Fatal(err)
	}
	if selectShown {
		t.Error("в шапке сервера на телефоне остался переключатель: кнопок должно быть одна и «···»")
	}
	karkasShot(t, ctx, "p2-host-overview")

	// вкладки — одна полоса: влезают по высоте в одну строку и прокручиваются вбок
	var strip struct {
		Rows       int  `json:"rows"`
		Scrolls    bool `json:"scrolls"`
		BodyScroll bool `json:"bodyScroll"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const bar = document.querySelector('.subtabs-bar .segments'); const tops = new Set(Array.from(bar.children).map((c) => Math.round(c.getBoundingClientRect().top)));
	  return {rows: tops.size, scrolls: bar.scrollWidth > bar.clientWidth, bodyScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth}; })()`, &strip)); err != nil {
		t.Fatal(err)
	}
	if strip.Rows != 1 || !strip.Scrolls || strip.BodyScroll {
		t.Errorf("вкладки на телефоне: %+v", strip)
	}
	serversClickTab(t, ctx, "Самостоятельность", entityAddr(serversScreen, "srv-vpn", serversTabAuto))
	auto := serversRead(t, ctx)
	if auto.SW > auto.CW {
		t.Errorf("«Самостоятельность» едет вбок: %d при %d", auto.SW, auto.CW)
	}
	// на телефоне поля окна — на всю ширину, под подписью; касанием меняется уровень
	var layout struct {
		LabelBottom float64 `json:"labelBottom"`
		FieldTop    float64 `json:"fieldTop"`
		FieldRight  float64 `json:"fieldRight"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const row = document.querySelector('.field.setting:has(input[name="window_from"])');
	  const label = row.querySelector('.setting-label').getBoundingClientRect(); const input = row.querySelector('input[name="window_to"]').getBoundingClientRect();
	  return {labelBottom: label.bottom, fieldTop: input.top, fieldRight: input.right}; })()`, &layout)); err != nil {
		t.Fatal(err)
	}
	if layout.FieldTop < layout.LabelBottom || layout.FieldRight > 390 {
		t.Errorf("поле окна на телефоне: %+v", layout)
	}
	karkasShot(t, ctx, "p3-host-auto")
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'field-segments')]//label[contains(@class,'segment')][normalize-space(.)='под судом']`, chromedp.BySearch)); err != nil {
		t.Fatal(err)
	}
	serversPoll(t, ctx, "полоса сохранения над нижней полосой приложения",
		`(() => { const bar = document.getElementById('save-bar'); const tab = document.getElementById('tabbar'); return !!bar && !bar.hidden && bar.getBoundingClientRect().bottom <= tab.getBoundingClientRect().top + 1; })()`)
	serversSave(t, ctx)
	if h, _ := st.Host("srv-vpn"); h.Autonomy != store.AutonomyCourt {
		t.Errorf("с телефона уровень не сохранился: %q", h.Autonomy)
	}
}
