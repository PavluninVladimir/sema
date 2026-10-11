package api

// «Почта» в настоящем Chrome (2 октября 2026): список ящиков тонкими
// строками, экран ящика с пятью вкладками, правка и «Сохранить» полосой,
// переключатель ящика, «‹ Почта», «Спам» и «Общее», «Добавить ящик». Нажатия и
// ввод — настоящие, ожидание — условий на странице, а не сроков; состояние
// читается и со страницы, и из базы.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).
// Chrome без головы; настоящих соединений с почтовыми серверами нет: ящики
// фикстуры не подключаются, а кнопки проверки входа тесты не нажимают.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// mailState — что видно на экранах «Почты».
const mailState = `(() => {
  const visible = (el) => { if (!el) return false; const rect = el.getBoundingClientRect(); const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'; };
  const root = document.documentElement;
  const screenEl = document.getElementById('screen');
  const rows = Array.from(document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:mail@"]')).filter(visible).map((row) => ({
    goto: row.getAttribute('data-goto'),
    pills: row.querySelectorAll('.right-side .pill').length,
    buttons: Array.from(row.querySelectorAll('.right-side > button.action')).filter(visible).length,
    more: !!row.querySelector('.row-menu'),
    text: row.textContent.replace(/\s+/g, ' ').trim(),
  }));
  const bar = document.getElementById('sec-entity');
  const barRow = bar ? bar.querySelector('.row') : null;
  const danger = document.querySelector('#feed .section.danger');
  const sections = Array.from(document.querySelectorAll('#feed > .column > *')).filter(visible);
  const last = sections.length ? sections[sections.length - 1] : null;
  const savebar = document.getElementById('save-bar');
  const strip = document.querySelector('.subtabs-bar .segments');
  return JSON.stringify({
    screen: screenEl ? screenEl.dataset.screen : '',
    h1: (document.querySelector('#screen .header h1') || {}).textContent || '',
    crumb: (() => { const c = document.querySelector('#screen .crumbs .crumb'); return c && visible(c) ? c.textContent.trim() : ''; })(),
    subtabs: Array.from(document.querySelectorAll('#screen .subtabs-bar .segment')).map((a) => a.textContent.replace(/\s+/g, ' ').trim()),
    selectedTab: (() => { const a = document.querySelector('#screen .subtabs-bar .segment.selected'); return a ? a.textContent.replace(/\s+/g, ' ').trim() : ''; })(),
    stripRows: strip ? new Set(Array.from(strip.children).map((c) => Math.round(c.getBoundingClientRect().top))).size : 0,
    stripScrolls: strip ? strip.scrollWidth > strip.clientWidth : false,
    rows: rows,
    shelf: !!document.querySelector('#feed .shelf'),
    shelfText: (document.querySelector('#feed .shelf') || {}).textContent ? document.querySelector('#feed .shelf').textContent.replace(/\s+/g, ' ').trim() : '',
    barVisible: visible(barRow),
    barButtons: barRow ? Array.from(barRow.querySelectorAll('.right-side > button.action')).filter(visible).length : 0,
    barMore: barRow ? visible(barRow.querySelector('.row-menu')) : false,
    barSelect: barRow ? visible(barRow.querySelector('select')) : false,
    barText: barRow ? barRow.textContent.replace(/\s+/g, ' ').trim() : '',
    danger: !!danger,
    dangerLast: !!danger && last === danger.closest('#feed > .column > *'),
    dangerButtons: danger ? danger.querySelectorAll('button.action').length : 0,
    barShown: !!savebar && !savebar.hidden,
    barLabel: savebar ? ((savebar.querySelector('.what') || {}).textContent || '') : '',
    headerButtons: Array.from(document.querySelectorAll('#screen .header .controls > button.action')).filter(visible).map((b) => b.textContent.trim()),
    notSaved: Array.from(document.querySelectorAll('.not-saved:not([hidden])')).map((e) => e.textContent.trim()),
    callout: (() => { const r = document.querySelector('#feed .callout'); return r ? r.textContent.replace(/\s+/g, ' ').trim() : ''; })(),
    sw: Math.max(root.scrollWidth, document.getElementById('window').scrollWidth, document.getElementById('feed').scrollWidth), cw: root.clientWidth,
  });
})()`

type mailView struct {
	Screen        string       `json:"screen"`
	H1            string       `json:"h1"`
	Crumb         string       `json:"crumb"`
	Subtabs       []string     `json:"subtabs"`
	SelectedTab   string       `json:"selectedTab"`
	StripRows     int          `json:"stripRows"`
	StripScrolls  bool         `json:"stripScrolls"`
	Rows          []serversRow `json:"rows"`
	Shelf         bool         `json:"shelf"`
	ShelfText     string       `json:"shelfText"`
	BarVisible    bool         `json:"barVisible"`
	BarButtons    int          `json:"barButtons"`
	BarMore       bool         `json:"barMore"`
	BarSelect     bool         `json:"barSelect"`
	BarText       string       `json:"barText"`
	Danger        bool         `json:"danger"`
	DangerLast    bool         `json:"dangerLast"`
	DangerButtons int          `json:"dangerButtons"`
	BarShown      bool         `json:"barShown"`
	BarLabel      string       `json:"barLabel"`
	HeaderButtons []string     `json:"headerButtons"`
	NotSaved      []string     `json:"notSaved"`
	Callout       string       `json:"callout"`
	SW            int          `json:"sw"`
	CW            int          `json:"cw"`
}

func mailRead(t *testing.T, ctx context.Context) mailView {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(mailState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v mailView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

// mailPoll — ждать условия на странице; не дождались — показать, что на ней.
func mailPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(mailState, &now))
		t.Fatalf("не дождались: %s (%v); состояние: %s", what, err, now)
	}
}

// mailClick — настоящее нажатие. Закреплённая шапка перекрывает элемент у
// верхнего края окна, а Click не прокручивает уже «видимый» элемент: клик ушёл
// бы в шапку. Поэтому элемент подводится к середине окна, и только потом нажимается.
func mailClick(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	by, find := chromedp.ByQuery, `document.querySelector(`+jsString(sel)+`)`
	if strings.HasPrefix(sel, "//") {
		by, find = chromedp.BySearch, `document.evaluate(`+jsString(sel)+`, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue`
	}
	var moved bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const el = `+find+`; if (!el) return false; el.scrollIntoView({block: 'center'}); return true; })()`, &moved)); err != nil || !moved {
		t.Fatalf("подвести к середине окна %s: %v", sel, err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(sel, by)); err != nil {
		t.Fatalf("нажать %s: %v", sel, err)
	}
}

// mailClickTab — нажать внутреннюю вкладку по имени и дождаться её адреса.
func mailClickTab(t *testing.T, ctx context.Context, name, screen string) {
	t.Helper()
	mailClick(t, ctx, `//div[contains(@class,'subtabs-bar')]//a[contains(@class,'segment')][contains(.,'`+name+`')]`)
	waitScreen(t, ctx, screen)
	mailPoll(t, ctx, "экран перечитан", `!document.querySelector('.htmx-request')`)
}

// mailSaveBar — «Сохранить» нижней полосой и ждать, пока она погаснет.
func mailSaveBar(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	mailPoll(t, ctx, "полоса погасла после сохранения",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && bar.hidden && !document.querySelector('.htmx-request'); })()`)
}

// mailTypeInto — стереть число в поле клавишами и набрать новое: Clear менял бы
// defaultValue, и полоса не увидела бы правки.
func mailTypeInto(t *testing.T, ctx context.Context, sel, text string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Focus(sel, chromedp.ByQuery),
		chromedp.SendKeys(sel, kb.End+strings.Repeat(kb.Backspace, 8)+text, chromedp.ByQuery)); err != nil {
		t.Fatalf("набрать %q в %s: %v", text, sel, err)
	}
}

func mailBoxSel(screen string) string {
	return `#feed .row-wrap > .row.clickable[data-goto="` + screen + `"] a.row-link`
}

func mailListReady(t *testing.T, ctx context.Context) {
	t.Helper()
	mailPoll(t, ctx, "список ящиков нарисован",
		`document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:mail@"]').length === 3`)
}

// Список → ящик → пять вкладок → правка и «Сохранить» полосой → другой ящик →
// «‹ Почта» → «Спам» и «Общее» → «Добавить ящик». Состояние читается и со
// страницы, и из базы.
func TestBrowserMailWalk(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := mailFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, mailScreen)
	waitScreen(t, ctx, mailScreen)
	mailListReady(t, ctx)

	// ---- список: тонкие строки, полка, одна кнопка в шапке ----
	list := mailRead(t, ctx)
	if len(list.Rows) != 3 || !list.Shelf || !strings.Contains(list.ShelfText, "Ждут решения") {
		t.Fatalf("список: строк %d, полка %v: %+v", len(list.Rows), list.Shelf, list)
	}
	for _, row := range list.Rows {
		if row.Pills > 2 || row.Buttons > 1 || !row.More {
			t.Errorf("строка не тонкая — капсул %d, кнопок %d, «···» %v: %s", row.Pills, row.Buttons, row.More, row.Text)
		}
	}
	if strings.Join(list.Subtabs, "|") != "Ящики3|Спам|Общее" || !strings.HasPrefix(list.SelectedTab, "Ящики") {
		t.Errorf("вкладки раздела %v, выбрана %q", list.Subtabs, list.SelectedTab)
	}
	if strings.Join(list.HeaderButtons, "|") != "Добавить ящик" {
		t.Errorf("кнопки шапки раздела: %v", list.HeaderButtons)
	}
	if list.SW > list.CW {
		t.Errorf("список едет вбок: %d при %d", list.SW, list.CW)
	}
	karkasShot(t, ctx, "w1-list")

	// ---- в ящик кликом по строке ----
	mailClick(t, ctx, mailBoxSel(entityAddr(mailScreen, mailBoxApp, "")))
	waitScreen(t, ctx, entityAddr(mailScreen, mailBoxApp, ""))
	mailPoll(t, ctx, "экран ящика нарисован", `!!document.getElementById('sec-entity')`)
	box := mailRead(t, ctx)
	if box.Crumb != "‹ Почта" || box.H1 != "проба@пример.рф" || !box.BarVisible || box.BarButtons != 0 || !box.BarMore || !box.BarSelect ||
		!strings.Contains(box.BarText, "Пароль приложения") || !strings.HasPrefix(box.SelectedTab, "Обработка") {
		t.Errorf("экран ящика: %+v", box)
	}
	if strings.Join(box.Subtabs, "|") != "Обработка|Подключение|Отправка|Папки3|Хранение" {
		t.Errorf("вкладки ящика: %v", box.Subtabs)
	}
	karkasShot(t, ctx, "w2-box-work")

	// ---- Обработка: «наружу» тумблером и «только читает» сегментом, одной полосой ----
	if !s.eng.MailConf(mailBoxApp).OutboundOn() {
		t.Fatal("до правки ящик закрыт облачной модели")
	}
	mailClick(t, ctx, `label[for="fld-mailmodes-`+mailBoxApp+`-outbound"]`)
	mailPoll(t, ctx, "полоса ждёт сохранения «наружу»",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1') && bar.textContent.includes('Показывать облачной модели'); })()`)
	mailClick(t, ctx, `//div[contains(@class,'field-segments')]//label[contains(@class,'segment')][normalize-space(.)='только читает']`)
	mailPoll(t, ctx, "полоса ждёт две правки", `document.getElementById('save-bar').textContent.includes('Изменено 2')`)
	karkasShot(t, ctx, "w2b-box-work-edited")
	mailSaveBar(t, ctx)
	if c := s.eng.MailConf(mailBoxApp); c.OutboundOn() || !c.ReadOnly() {
		t.Fatalf("в базе: наружу %v, только читает %v", c.OutboundOn(), c.ReadOnly())
	}
	if !s.eng.MailboxReadOnly(mailBoxApp) || !s.eng.ClosedAccounts(true)[mailBoxApp] {
		t.Error("движок не видит нового режима ящика")
	}
	// шапка перечитана: «только читает» в строке фактов
	mailPoll(t, ctx, "шапка узнала о режиме", `document.getElementById('sec-entity').textContent.includes('только читает')`)

	// ---- пять вкладок по порядку ----
	for _, tab := range []struct{ name, tail string }{
		{"Подключение", mailTabConn}, {"Отправка", mailTabSend}, {"Папки", mailTabFolders}, {"Хранение", mailTabKeep},
	} {
		mailClickTab(t, ctx, tab.name, entityAddr(mailScreen, mailBoxApp, tab.tail))
		v := mailRead(t, ctx)
		if !strings.HasPrefix(v.SelectedTab, tab.name) || v.Crumb != "‹ Почта" || v.SW > v.CW {
			t.Errorf("вкладка «%s»: выбрана %q, «назад» %q, ширина %d при %d", tab.name, v.SelectedTab, v.Crumb, v.SW, v.CW)
		}
		karkasShot(t, ctx, "w3-box-"+tab.tail)
		if tab.tail == mailTabConn {
			if !v.Danger || !v.DangerLast || v.DangerButtons < 2 {
				t.Errorf("«Подключение»: «Опасное» последним и с видимыми кнопками: %+v", v)
			}
			// «Перечитать…» спрашивает, а «Отмена» ничего не делает
			mailClick(t, ctx, `#sec-mail-box-danger button[data-confirm]`)
			mailPoll(t, ctx, "окно вопроса открыто", `!!document.querySelector('dialog#confirm[open]')`)
			karkasShot(t, ctx, "w3b-confirm-rescan")
			if err := chromedp.Run(ctx, chromedp.Click(`dialog#confirm button.cancel`, chromedp.ByQuery)); err != nil {
				t.Fatal(err)
			}
			mailPoll(t, ctx, "окно вопроса закрыто", `!document.querySelector('dialog#confirm[open]')`)
		}
	}

	// ---- Хранение: число полем и «Сохранить» полосой ----
	if err := s.eng.SetRetentionDays(15); err != nil {
		t.Fatal(err)
	}
	mailTypeInto(t, ctx, `input[name="keep_days"]`, "45")
	mailPoll(t, ctx, "полоса ждёт сохранения срока",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1'); })()`)
	mailSaveBar(t, ctx)
	if c := s.eng.MailConf(mailBoxApp); c.KeepDays != 45 || c.JunkDays != 0 {
		t.Fatalf("в базе срок обычных %d, срок спама %d (свой не назначали)", c.KeepDays, c.JunkDays)
	}
	if s.eng.RestTTLForAccount(mailBoxApp) != 45 || s.eng.SpamTTLForAccount(mailBoxApp) != 15 {
		t.Errorf("движок читает сроки ящика неверно: %d и %d", s.eng.RestTTLForAccount(mailBoxApp), s.eng.SpamTTLForAccount(mailBoxApp))
	}

	// ---- другой ящик из списка в шапке: та же вкладка ----
	if err := chromedp.Run(ctx, chromedp.SetValue(`#sec-entity select`, mailBoxGoog, chromedp.ByQuery)); err != nil {
		t.Fatalf("выбрать ящик: %v", err)
	}
	waitScreen(t, ctx, entityAddr(mailScreen, mailBoxGoog, mailTabKeep))
	mailPoll(t, ctx, "шапка другого ящика", `document.getElementById('sec-entity').textContent.includes('Google OAuth')`)
	if v := mailRead(t, ctx); v.H1 != mailBoxGoog || !strings.HasPrefix(v.SelectedTab, "Хранение") {
		t.Errorf("после переключения: %+v", v)
	}

	// ---- «‹ Почта» возвращает в список ----
	mailClick(t, ctx, `#screen .crumbs .crumb`)
	waitScreen(t, ctx, mailScreen)
	mailListReady(t, ctx)

	// ---- «Спам»: порог — ежедневное, «после N писем у провайдера» — «Тонкая настройка» ----
	mailClickTab(t, ctx, "Спам", entityAddr(mailScreen, "", mailTabSpam))
	spam := mailRead(t, ctx)
	if !strings.Contains(spam.Callout, "Одинаково для всех ящиков") || spam.BarVisible {
		t.Errorf("«Спам»: сказано ли «для всех», шапка ящика: %+v", spam)
	}
	karkasShot(t, ctx, "w4-spam")
	mailTypeInto(t, ctx, `input[name="threshold"]`, "85")
	mailSaveBar(t, ctx)
	if got := s.eng.AutoThreshold(); got != 0.85 {
		t.Fatalf("порог в базе %v", got)
	}
	rowsOpenFold(t, ctx)
	karkasShot(t, ctx, "w4b-spam-fine")
	mailTypeInto(t, ctx, `#fold-rare input[name="server_votes"]`, "6")
	mailSaveBar(t, ctx)
	if got := s.eng.ServerSpamVotes(); got != 6 {
		t.Fatalf("«после N писем у провайдера» в базе %d: правка с вкладки «Спам» не дошла до движка", got)
	}

	// ---- «Общее»: способ ответа сегментом ----
	mailClickTab(t, ctx, "Общее", entityAddr(mailScreen, "", mailTabCommon))
	karkasShot(t, ctx, "w5-common")
	mailClick(t, ctx, `//div[contains(@class,'field-segments')]//label[contains(@class,'segment')][normalize-space(.)='по кнопке']`)
	mailPoll(t, ctx, "полоса ждёт способа ответа", `document.getElementById('save-bar').textContent.includes('Что делать с готовым ответом')`)
	mailSaveBar(t, ctx)
	if got := s.eng.SendMode(); got != "по кнопке" {
		t.Fatalf("способ ответа в базе %q", got)
	}
	common := mailRead(t, ctx)
	if !common.Danger || !common.DangerLast || !strings.Contains(common.Callout, "Одинаково для всех ящиков") {
		t.Errorf("«Общее»: «Опасное» последним и подпись «для всех»: %+v", common)
	}

	// ---- «Добавить ящик» из шапки ----
	mailClick(t, ctx, `//div[contains(@class,'controls')]/button[contains(.,'Добавить ящик')]`)
	waitScreen(t, ctx, entityAddr(mailScreen, mailNew, ""))
	mailPoll(t, ctx, "форма добавления нарисована", `!!document.querySelector('form input[name="email"]')`)
	mailPoll(t, ctx, "«Ещё 1 поле» свёрнуто, хост не виден",
		`(() => { const d = document.querySelector('details.more-fields'); const h = document.querySelector('input[name="host"]');
		  return !!d && !d.open && d.textContent.includes('Ещё 1 поле') && !h.checkVisibility(); })()`)
	karkasShot(t, ctx, "w6-new")
	mailClick(t, ctx, `#screen .crumbs .crumb`)
	waitScreen(t, ctx, mailScreen)
}

// Снимки для ревью (SEMA_SHOTS=<папка>): десктоп 1290×806 и телефон 390×844,
// данные из mailFix — три ящика разных способов входа и режимов.
func TestBrowserMailShots(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := mailFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, mailScreen)
	waitScreen(t, ctx, mailScreen)
	for _, shot := range []struct{ name, screen string }{
		{"desktop-1-boxes", mailScreen},
		{"desktop-2-spam", entityAddr(mailScreen, "", mailTabSpam)},
		{"desktop-3-common", entityAddr(mailScreen, "", mailTabCommon)},
		{"desktop-4-box-work", entityAddr(mailScreen, mailBoxApp, "")},
		{"desktop-5-box-conn", entityAddr(mailScreen, mailBoxApp, mailTabConn)},
		{"desktop-6-box-send", entityAddr(mailScreen, mailBoxApp, mailTabSend)},
		{"desktop-7-box-folders", entityAddr(mailScreen, mailBoxGoog, mailTabFolders)},
		{"desktop-8-box-keep", entityAddr(mailScreen, mailBoxApp, mailTabKeep)},
		{"desktop-9-new", entityAddr(mailScreen, mailNew, "")},
		{"desktop-10-box-google-conn", entityAddr(mailScreen, mailBoxGoog, mailTabConn)},
		{"desktop-11-box-work-alert", entityAddr(mailScreen, mailBoxWork, "")},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	// низ вкладки «Подключение»: «Опасное» отдельной карточкой
	karkasOpen(t, ctx, entityAddr(mailScreen, mailBoxApp, mailTabConn))
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-5b-box-conn-bottom")
	// «Спам»: «Тонкая настройка» раскрыта, внизу «Опасное»
	karkasOpen(t, ctx, entityAddr(mailScreen, "", mailTabSpam))
	rowsOpenFold(t, ctx)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-2b-spam-bottom")
	karkasOpen(t, ctx, entityAddr(mailScreen, "", mailTabCommon))
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-3b-common-bottom")
}

func TestBrowserMailShotsPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := mailFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)
	for _, shot := range []struct{ name, screen string }{
		{"phone-1-boxes", mailScreen},
		{"phone-2-box-work", entityAddr(mailScreen, mailBoxApp, "")},
		{"phone-3-box-folders", entityAddr(mailScreen, mailBoxGoog, mailTabFolders)},
		{"phone-4-spam", entityAddr(mailScreen, "", mailTabSpam)},
		{"phone-5-box-conn", entityAddr(mailScreen, mailBoxApp, mailTabConn)},
		{"phone-6-new", entityAddr(mailScreen, mailNew, "")},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
}

// Телефон 390×844: список, ящик с шапкой в одну линию, вкладки одной полосой с
// прокруткой, ничего не едет вбок; настоящее касание строки и правка полосой.
func TestBrowserMailPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := mailFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)

	karkasOpen(t, ctx, mailScreen)
	mailListReady(t, ctx)
	list := mailRead(t, ctx)
	if list.Crumb != "‹ Настройки" || list.SW > list.CW {
		t.Errorf("список на телефоне: «назад» %q, ширина %d при %d", list.Crumb, list.SW, list.CW)
	}
	for _, row := range list.Rows {
		if row.Pills > 2 || row.Buttons > 1 {
			t.Errorf("строка на телефоне не тонкая: %s", row.Text)
		}
	}
	// вкладки раздела — одна полоса
	if list.StripRows != 1 {
		t.Errorf("вкладки раздела в %d строк", list.StripRows)
	}

	// настоящее касание строки: ящик Gmail со «зряшными» папками
	mailTap(t, ctx, mailBoxSel(entityAddr(mailScreen, mailBoxGoog, "")))
	waitScreen(t, ctx, entityAddr(mailScreen, mailBoxGoog, ""))
	mailPoll(t, ctx, "экран ящика нарисован", `!!document.getElementById('sec-entity')`)
	box := mailRead(t, ctx)
	if box.Crumb != "‹ Почта" || box.SW > box.CW || box.BarSelect {
		t.Errorf("шапка ящика на телефоне: «назад» %q, переключатель виден %v, ширина %d при %d", box.Crumb, box.BarSelect, box.SW, box.CW)
	}
	if box.StripRows != 1 || !box.StripScrolls {
		t.Errorf("вкладки ящика на телефоне: строк %d, прокручиваются %v", box.StripRows, box.StripScrolls)
	}
	for _, tab := range []struct{ name, tail string }{
		{"Подключение", mailTabConn}, {"Отправка", mailTabSend}, {"Папки", mailTabFolders}, {"Хранение", mailTabKeep},
	} {
		mailClickTab(t, ctx, tab.name, entityAddr(mailScreen, mailBoxGoog, tab.tail))
		v := mailRead(t, ctx)
		if v.SW > v.CW {
			t.Errorf("вкладка «%s» едет вбок: %d при %d", tab.name, v.SW, v.CW)
		}
	}
	// строка настройки: контрол под подписью, полоса сохранения над нижней полосой приложения
	mailClickTab(t, ctx, "Хранение", entityAddr(mailScreen, mailBoxGoog, mailTabKeep))
	mailTypeInto(t, ctx, `input[name="keep_days"]`, "45")
	mailPoll(t, ctx, "полоса сохранения над нижней полосой приложения",
		`(() => { const bar = document.getElementById('save-bar'); const tab = document.getElementById('tabbar'); return !!bar && !bar.hidden && bar.getBoundingClientRect().bottom <= tab.getBoundingClientRect().top + 1; })()`)
	mailSaveBar(t, ctx)
	if c := s.eng.MailConf(mailBoxGoog); c.KeepDays != 45 {
		t.Errorf("с телефона срок не сохранился: %d", c.KeepDays)
	}
	for _, tab := range []string{mailTabSpam, mailTabCommon} {
		karkasOpen(t, ctx, entityAddr(mailScreen, "", tab))
		v := mailRead(t, ctx)
		if v.SW > v.CW || v.StripRows != 1 {
			t.Errorf("«%s» на телефоне: ширина %d при %d, строк вкладок %d", tab, v.SW, v.CW, v.StripRows)
		}
	}
}

// mailTap — настоящее касание: так нажимает телефон, а не мышь.
func mailTap(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	var x, y float64
	script := `(() => { const el = document.querySelector(` + jsString(sel) + `); if (!el) return [0, 0]; el.scrollIntoView({block: 'center'});
	  const r = el.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; })()`
	var pt []float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &pt)); err != nil || len(pt) != 2 || pt[0] == 0 {
		t.Fatalf("найти %s для касания: %v %v", sel, err, pt)
	}
	x, y = pt[0], pt[1]
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.ActionFunc(func(ctx context.Context) error {
			return input.DispatchTouchEvent(input.TouchStart, []*input.TouchPoint{{X: x, Y: y}}).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return input.DispatchTouchEvent(input.TouchEnd, []*input.TouchPoint{}).Do(ctx)
		}),
	}); err != nil {
		t.Fatalf("коснуться %s: %v", sel, err)
	}
}
