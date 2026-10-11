package api

// Поиск по настройкам и «Под рукой» в настоящем Chrome (2 октября 2026): набор в
// поле колонки, нажатие по результату, переход с подсветкой строки (в том числе из
// «Тонкой настройки»), живые контролы на Обзоре и переход из «Вы меняли». Проверяем
// то, что делает человек: набор, нажатие, «Сохранить» полосой — а не вызов функции.
// Ждём условий на странице, а не сроков.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).
// Chrome без головы; в сеть ничего не ходит.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// hlState — где стоит строка с меткой и подсвечена ли она.
func hlState(t *testing.T, ctx context.Context, mark string) hlView {
	t.Helper()
	var raw string
	script := `(() => {
	  const el = document.querySelector('#window [data-hl="' + CSS.escape(` + jsString(mark) + `) + '"]');
	  const fold = document.getElementById('fold-rare');
	  const scr = document.getElementById('screen');
	  const base = { screen: scr ? scr.getAttribute('data-screen') : '', pending: !!(scr && scr.getAttribute('data-highlight')),
	    hash: decodeURIComponent(location.hash), fine: !!fold, fineOpen: !!fold && fold.open,
	    anyHl: document.querySelectorAll('.hl').length };
	  if (!el) return JSON.stringify({ ...base, found: false });
	  const r = el.getBoundingClientRect();
	  const style = getComputedStyle(el);
	  const feed = document.getElementById('feed');
	  const fr = feed ? feed.getBoundingClientRect() : { top: 0, bottom: innerHeight };
	  return JSON.stringify({ ...base, found: true, hl: el.classList.contains('hl'), height: Math.round(r.height),
	    shown: r.width > 0 && r.height > 0 && style.display !== 'none',
	    inView: r.top >= Math.max(0, fr.top) - 1 && r.bottom <= Math.min(innerHeight, fr.bottom) + 1,
	    top: Math.round(r.top), bottom: Math.round(r.bottom), innerHeight: innerHeight,
	    inFold: !!el.closest('#fold-rare'), text: (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 80) });
	})()`
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &raw)); err != nil {
		t.Fatal(err)
	}
	var v hlView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

type hlView struct {
	Screen      string `json:"screen"`
	Pending     bool   `json:"pending"`
	Hash        string `json:"hash"`
	Fine        bool   `json:"fine"`
	FineOpen    bool   `json:"fineOpen"`
	AnyHl       int    `json:"anyHl"`
	Found       bool   `json:"found"`
	Hl          bool   `json:"hl"`
	Height      int    `json:"height"`
	Shown       bool   `json:"shown"`
	InView      bool   `json:"inView"`
	Top         int    `json:"top"`
	Bottom      int    `json:"bottom"`
	InnerHeight int    `json:"innerHeight"`
	InFold      bool   `json:"inFold"`
	Text        string `json:"text"`
}

// hlPoll — ждать, пока строка с меткой станет такой, как нужно.
func hlPoll(t *testing.T, ctx context.Context, what, mark, cond string) hlView {
	t.Helper()
	script := `(() => {
	  const el = document.querySelector('#window [data-hl="' + CSS.escape(` + jsString(mark) + `) + '"]');
	  if (!el) return false;
	  const r = el.getBoundingClientRect();
	  const feed = document.getElementById('feed');
	  const fr = feed ? feed.getBoundingClientRect() : { top: 0, bottom: innerHeight };
	  const hl = el.classList.contains('hl');
	  const inView = r.height > 0 && r.top >= Math.max(0, fr.top) - 1 && r.bottom <= Math.min(innerHeight, fr.bottom) + 1;
	  return ` + cond + `;
	})()`
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(script, &ok, chromedp.WithPollingTimeout(25*time.Second))); err != nil || !ok {
		t.Fatalf("не дождались: %s (%v); состояние: %+v", what, err, hlState(t, ctx, mark))
	}
	return hlState(t, ctx, mark)
}

// indexType — набрать запрос в поле поиска настоящими нажатиями и дождаться ответа
// сервера: поздний ответ перерисовал бы экран под нажатием.
func indexType(t *testing.T, ctx context.Context, query string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Click(`#search`, chromedp.ByQuery),
		chromedp.SendKeys(`#search`, kb.End+strings.Repeat(kb.Backspace, 40)+query, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const f = document.getElementById('search'), feed = document.getElementById('feed');
		  return !!f && f.value === `+jsString(query)+` && !document.querySelector('.htmx-request') &&
		    !!feed && feed.textContent.includes('Нашлось') && feed.querySelectorAll('.row[data-goto]').length > 0; })()`,
		&ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').textContent.replace(/\s+/g, ' ').slice(0, 300)`, &text))
		t.Fatalf("поиск %q не показал результаты: %v; на экране: %s", query, err, text)
	}
}

// indexClickResult — нажать результат поиска по адресу перехода. Строку подводим к
// середине окна: закреплённая шапка перекрывает элемент у верхнего края, а Click
// не прокручивает уже «видимый» элемент.
func indexClickResult(t *testing.T, ctx context.Context, goTo string) {
	t.Helper()
	sel := `#feed .row.clickable[data-goto="` + goTo + `"]`
	var found bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const el = document.querySelector(`+jsString(sel)+`);
	  if (!el) return false; el.scrollIntoView({block: 'center'}); return true; })()`, &found)); err != nil || !found {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`Array.from(document.querySelectorAll('#feed .row[data-goto]')).map(r => r.getAttribute('data-goto')).join(' | ')`, &text))
		t.Fatalf("нет результата с переходом %q (%v); есть: %s", goTo, err, text)
	}
	if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать результат %q: %v", goTo, err)
	}
}

// browserIndexFixture — сервер с данными индекса и журналом.
func browserIndexFixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, st := indexFix(t, 40)
	indexJournal(t, st)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

// Десктоп: набрать «окно» в поле колонки, нажать результат «Серверы → srv-vpn →
// Самостоятельность → Окно» — открылся экран сервера на нужной вкладке, строка в
// области видимости и подсвечена; подсветка гаснет сама и не возвращается при
// перерисовке экрана опросом; в адресе экрана метки нет.
func TestBrowserSearchJumpsAndHighlights(t *testing.T) {
	serversBrowserOnly(t)
	_, srv := browserIndexFixture(t)
	ctx := rowsDesktop(t, srv.URL, "settings")
	waitScreen(t, ctx, "settings")
	indexType(t, ctx, "окно")
	karkasShot(t, ctx, "index-desktop-1-search")

	const goTo = "settings:servers@srv-vpn~auto^f:host-auto-window"
	const mark = "f:host-auto-window"
	indexClickResult(t, ctx, goTo)
	waitScreen(t, ctx, "settings:servers@srv-vpn~auto")
	v := hlPoll(t, ctx, "строка «Окно» подсвечена и в области видимости", mark, `hl && inView`)
	if !v.Shown || v.InFold {
		t.Errorf("строка «Окно»: %+v", v)
	}
	if strings.Contains(v.Hash, "^") || !strings.HasSuffix(v.Hash, "settings:servers@srv-vpn~auto") {
		t.Errorf("метка попала в адрес экрана: %q", v.Hash)
	}
	if v.Pending {
		t.Error("метка осталась в атрибуте экрана: следующая перерисовка подсветила бы снова")
	}
	karkasShot(t, ctx, "index-desktop-2-highlight")
	hlPoll(t, ctx, "подсветка погасла сама", mark, `!hl`)

	// перерисовка ленты опросом: подсветка не возвращается
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__swaps = 0;
	  document.body.addEventListener('htmx:after:swap', () => { window.__swaps++; });
	  htmx.trigger(document.getElementById('screen'), 'sema:refresh'); true`, nil)); err != nil {
		t.Fatal(err)
	}
	var swapped bool
	if err := chromedp.Run(ctx, chromedp.Poll(`window.__swaps >= 1 && !document.querySelector('.htmx-request')`, &swapped,
		chromedp.WithPollingTimeout(15*time.Second))); err != nil || !swapped {
		t.Fatalf("лента не перечитана: %v", err)
	}
	if after := hlState(t, ctx, mark); after.Hl || after.AnyHl != 0 || after.Pending {
		t.Errorf("после перерисовки опросом подсветка вернулась: %+v", after)
	}
}

// Десктоп: результат из «Тонкой настройки» — экран открылся, «Тонкая настройка»
// раскрылась сама, строка подведена к середине окна и подсвечена.
func TestBrowserSearchOpensFineSetting(t *testing.T) {
	serversBrowserOnly(t)
	_, srv := browserIndexFixture(t)
	ctx := rowsDesktop(t, srv.URL, "settings")
	waitScreen(t, ctx, "settings")
	indexType(t, ctx, "пуш")

	const goTo = "settings:notify^f:push-settings-ttl_min"
	const mark = "f:push-settings-ttl_min"
	indexClickResult(t, ctx, goTo)
	waitScreen(t, ctx, "settings:notify")
	v := hlPoll(t, ctx, "строка из «Тонкой настройки» подсвечена и в области видимости", mark, `hl && inView`)
	if !v.InFold || !v.FineOpen || !v.Shown {
		t.Errorf("«Тонкая настройка» не раскрылась или строка не в ней: %+v", v)
	}
	if v.Top < 60 || v.Bottom > v.InnerHeight {
		t.Errorf("строка у края окна: %d–%d из %d", v.Top, v.Bottom, v.InnerHeight)
	}
	karkasShot(t, ctx, "index-desktop-3-fine-highlight")
	hlPoll(t, ctx, "подсветка погасла сама", mark, `!hl`)
}

// Телефон, 390×844: поиск в корне настроек, переход по результату из «Тонкой
// настройки» — тот же путь, та же подсветка; страница не едет вбок.
func TestBrowserSearchJumpsOnPhone(t *testing.T) {
	serversBrowserOnly(t)
	_, srv := browserIndexFixture(t)
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)
	karkasOpen(t, ctx, "settings")
	indexType(t, ctx, "пуш")
	karkasShot(t, ctx, "index-phone-1-search")

	const goTo = "settings:notify^f:push-settings-ttl_min"
	const mark = "f:push-settings-ttl_min"
	indexClickResult(t, ctx, goTo)
	waitScreen(t, ctx, "settings:notify")
	v := hlPoll(t, ctx, "на телефоне строка из «Тонкой настройки» подсвечена и в области видимости", mark, `hl && inView`)
	if !v.InFold || !v.FineOpen {
		t.Errorf("«Тонкая настройка» не раскрылась: %+v", v)
	}
	karkasShot(t, ctx, "index-phone-2-fine-highlight")
	var sw, cw int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`Math.max(document.documentElement.scrollWidth, document.getElementById('window').scrollWidth)`, &sw),
		chromedp.Evaluate(`document.documentElement.clientWidth`, &cw)); err != nil {
		t.Fatal(err)
	}
	if sw > cw {
		t.Errorf("экран едет вбок: %d при %d", sw, cw)
	}
	hlPoll(t, ctx, "подсветка погасла сама", mark, `!hl`)

	// и обычная настройка с телефона: сервер → «Самостоятельность» → «Окно»
	karkasOpen(t, ctx, "settings")
	indexType(t, ctx, "окно sRv-VpN")
	indexClickResult(t, ctx, "settings:servers@srv-vpn~auto^f:host-auto-window")
	waitScreen(t, ctx, "settings:servers@srv-vpn~auto")
	hlPoll(t, ctx, "«Окно» на телефоне подсвечено", "f:host-auto-window", `hl && inView`)
}

// Обзор: «Под рукой» — живой контрол из реестра; число меняется настоящим вводом,
// сохраняется полосой тем же действием, на экране настройки то же значение, а в «Вы
// меняли» встаёт новая правка, и переход из неё ведёт к настройке с подсветкой.
func TestBrowserHandyEditsLiveControl(t *testing.T) {
	serversBrowserOnly(t)
	s, srv := browserIndexFixture(t)
	ctx := rowsDesktop(t, srv.URL, "settings")
	waitScreen(t, ctx, "settings")
	rowsPoll(t, ctx, "«Под рукой» нарисовано", `!!document.querySelector('form[data-form="pin-ui.poll_s"] input[name="poll_s"]')`)
	var before string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('form[data-form="pin-ui.poll_s"] input[name="poll_s"]').value`, &before)); err != nil {
		t.Fatal(err)
	}
	if before == "" || before == "12" {
		t.Fatalf("исходное значение %q", before)
	}
	karkasShot(t, ctx, "index-desktop-4-overview")

	const field = `form[data-form="pin-ui.poll_s"] input[name="poll_s"]`
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector(`+jsString(field)+`).scrollIntoView({block: 'center'}); true`, nil),
		chromedp.Click(field, chromedp.ByQuery),
		chromedp.SendKeys(field, kb.End+strings.Repeat(kb.Backspace, 4)+"12", chromedp.ByQuery)); err != nil {
		t.Fatalf("вписать число: %v", err)
	}
	rowsPoll(t, ctx, "полоса сохранения с одной правкой",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1'); })()`)
	karkasShot(t, ctx, "index-desktop-5-overview-savebar")
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	rowsPoll(t, ctx, "после сохранения полоса погасла, а в «Вы меняли» новая правка наверху",
		`(() => { const bar = document.getElementById('save-bar'); const first = document.querySelector('#sec-recent-edits .row-wrap');
		  return !!bar && bar.hidden && !!first && first.textContent.includes('Опрос экрана без провода') && first.textContent.includes('12 с'); })()`)
	if got := s.eng.LiveSettings().PollS; got != 12 {
		t.Fatalf("движок видит опрос %d с, а вписано 12", got)
	}
	// на экране настройки то же значение
	karkasOpen(t, ctx, "settings:watch~screen")
	var shown string
	rowsPoll(t, ctx, "поле на экране настройки", `!!document.querySelector('input[name="poll_s"]')`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('input[name="poll_s"]').value`, &shown)); err != nil || shown != "12" {
		t.Errorf("на экране настройки значение %q, а сохранено 12 (%v)", shown, err)
	}

	// из «Вы меняли» — к настройке
	karkasOpen(t, ctx, "settings")
	rowsPoll(t, ctx, "«Вы меняли» с новой правкой", `!!document.querySelector('#sec-recent-edits .row.clickable[data-goto^="settings:watch~screen^f:work-live-poll_s"]')`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#sec-recent-edits .row.clickable').scrollIntoView({block: 'center'}); true`, nil),
		chromedp.Click(`#sec-recent-edits .row.clickable`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "settings:watch~screen")
	hlPoll(t, ctx, "из «Вы меняли» строка подсвечена и в области видимости", "f:work-live-poll_s", `hl && inView`)
}

// Тумблер «Под рукой»: тот же переключатель, что на экране настройки, сохраняется
// полосой — настройка меняется, и на экране настройки тот же ответ.
func TestBrowserHandyToggle(t *testing.T) {
	serversBrowserOnly(t)
	s, st := indexFix(t, 20)
	for i := 0; i < 9; i++ {
		mark := st.LastChange()
		_ = st.SetSetting("push.sticky_alert", []string{"1", "0"}[i%2])
		st.SignChanges(mark, "вы", "settings:notify · save_push_settings")
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := rowsDesktop(t, srv.URL, "settings")
	waitScreen(t, ctx, "settings")
	const sw = `form[data-form="pin-push.sticky_alert"] .field-toggle`
	rowsPoll(t, ctx, "тумблер нарисован", `!!document.querySelector('`+sw+`')`)
	was := s.eng.PushStickyAlert()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('`+sw+`').scrollIntoView({block: 'center'}); true`, nil),
		chromedp.Click(sw, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "полоса сохранения", `(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden; })()`)
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "после сохранения полоса погасла", `(() => { const bar = document.getElementById('save-bar'); return !!bar && bar.hidden; })()`)
	if s.eng.PushStickyAlert() == was {
		t.Errorf("тумблер с Обзора не поменял настройку: было %v, стало %v", was, s.eng.PushStickyAlert())
	}
}

// Снимки для ревью (SEMA_SHOTS=<папка>): Обзор целиком (верх и низ), «Журнал
// настроек» — «Ваши правки» и «Служебные», десктоп и телефон. Без SEMA_SHOTS ничего
// не снимается, а страницы просто собираются.
func TestBrowserIndexShots(t *testing.T) {
	serversBrowserOnly(t)
	_, srv := browserIndexFixture(t)
	ctx := rowsDesktop(t, srv.URL, "settings")
	waitScreen(t, ctx, "settings")
	karkasOpen(t, ctx, "settings")
	karkasShot(t, ctx, "index-desktop-6-overview-top")
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "index-desktop-7-overview-bottom")
	karkasOpen(t, ctx, changesScreen)
	rowsPoll(t, ctx, "журнал нарисован", `document.querySelectorAll('#feed .row-wrap').length > 3`)
	karkasShot(t, ctx, "index-desktop-8-journal-mine")
	karkasOpen(t, ctx, changesScreen)
	if err := chromedp.Run(ctx, chromedp.Click(`.filters-bar .segment:nth-child(2)`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	rowsPoll(t, ctx, "«Служебные» выбраны", `(() => { const a = document.querySelector('.filters-bar .segment.selected'); return !!a && a.textContent.includes('Служебные') && !document.querySelector('.htmx-request'); })()`)
	karkasShot(t, ctx, "index-desktop-9-journal-service")

	phone := phoneChrome(t, srv.URL, 4*time.Minute)
	karkasOpen(t, phone, settingsOverview)
	karkasShot(t, phone, "index-phone-3-overview")
	if err := chromedp.Run(phone, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, phone, "index-phone-4-overview-bottom")
	karkasOpen(t, phone, changesScreen)
	karkasShot(t, phone, "index-phone-5-journal")
}
