package api

// Каркас настроек в настоящем Chrome (1 октября 2026): одна колонка разделов
// вместо пяти вкладок на широком экране, корень-список и «‹ Настройки» на
// телефоне. Проверяем то, чем пользуется человек: нажатие по строке и по
// «назад», а не вызов функции; ждём условий на странице, а не сроков.
//
// Снимки для ревью: SEMA_SHOTS=<папка> кладёт туда PNG (десктоп 1290×806 и
// телефон 390×844). Окон на экране нет — Chrome без головы.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// karkasFixtures — сервер с письмами, ждущими решения: у «Почты» и «Обзора»
// появляются счётчики, и их видно на снимках.
func karkasFixtures(t *testing.T) *Server {
	t.Helper()
	s, st := testServer(t)
	for i := 0; i < 12; i++ {
		name := string(rune('а' + i))
		m := &store.Message{MessageID: "<ждёт-" + name + "@т>", Subject: "Рассылка " + name,
			FromAddr: name + "@пример.рф", FromName: "Отправитель " + name, Date: time.Now(),
			Status: store.StatusPending, Confidence: 0.9, Account: "проба"}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// karkasShot — снимок окна в папку SEMA_SHOTS; нет переменной — ничего не делает.
func karkasShot(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	dir := os.Getenv("SEMA_SHOTS")
	if dir == "" {
		return
	}
	// шрифты приезжают с сервера: снимок до их загрузки показал бы запасной
	var loaded bool
	_ = chromedp.Run(ctx, chromedp.Poll(`document.fonts.status === 'loaded'`, &loaded,
		chromedp.WithPollingTimeout(10*time.Second)))
	var png []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&png)); err != nil {
		t.Errorf("снимок %s: %v", name, err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
}

// karkasOpen — открыть экран переходом по адресу и дождаться, пока он приедет
// и догрузится (ленивые части, шрифты).
func karkasOpen(t *testing.T, ctx context.Context, screen string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=' + encodeURIComponent(`+jsString(screen)+`); true`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, screen)
	var quiet bool
	_ = chromedp.Run(ctx, chromedp.Poll(`!document.querySelector('.htmx-request')`, &quiet,
		chromedp.WithPollingTimeout(10*time.Second)))
}

// karkasPhoneState — что на телефонном экране лишнее и что стоит над
// содержимым: колонки, шторки, вкладок экрана нет; полосы над лентой.
const karkasPhoneState = `(() => {
  const visible = (el) => { if (!el) return false; const rect = el.getBoundingClientRect(); const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'; };
  const feed = document.getElementById('feed');
  const screenEl = document.getElementById('screen');
  const head = screenEl.querySelector('.header');
  const strips = [];
  for (const el of screenEl.children) {
    if (el === feed) break;
    if (visible(el)) strips.push((el.className || el.tagName).toString().split(' ')[0] + ':' + Math.round(el.getBoundingClientRect().height));
  }
  const root = document.documentElement;
  return JSON.stringify({
    panel: visible(document.getElementById('panel')),
    listButtons: Array.from(document.querySelectorAll('.section-picker, .panel-picker')).filter(visible).length,
    screenTabs: visible(screenEl.querySelector('.tabs-bar')),
    filtersBar: visible(screenEl.querySelector('.filters-bar')),
    subtabs: visible(screenEl.querySelector('.subtabs-bar')),
    search: !!document.getElementById('search') && visible(document.getElementById('search')),
    stripCount: strips.length,
    strips: strips,
    feedTop: Math.round(feed.getBoundingClientRect().top - screenEl.getBoundingClientRect().top),
    back: (() => { const crumb = screenEl.querySelector('.crumbs .crumb'); return crumb && visible(crumb) ? crumb.textContent.trim() : ''; })(),
    h1: head && head.querySelector('h1') ? head.querySelector('h1').textContent.trim() : '',
    rows: Array.from(feed.querySelectorAll('.row.clickable[data-goto]')).filter(visible).map((el) => el.getAttribute('data-goto')),
    sw: Math.max(root.scrollWidth, document.getElementById('window').scrollWidth, feed.scrollWidth), cw: root.clientWidth,
  });
})()`

type karkasPhone struct {
	Panel       bool     `json:"panel"`
	ListButtons int      `json:"listButtons"`
	ScreenTabs  bool     `json:"screenTabs"`
	FiltersBar  bool     `json:"filtersBar"`
	Subtabs     bool     `json:"subtabs"`
	Search      bool     `json:"search"`
	StripCount  int      `json:"stripCount"`
	Strips      []string `json:"strips"`
	FeedTop     int      `json:"feedTop"`
	Back        string   `json:"back"`
	H1          string   `json:"h1"`
	Rows        []string `json:"rows"`
	SW          int      `json:"sw"`
	CW          int      `json:"cw"`
}

func karkasPhoneRead(t *testing.T, ctx context.Context) karkasPhone {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(karkasPhoneState, &raw)); err != nil {
		t.Fatal(err)
	}
	var state karkasPhone
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return state
}

// Телефон, 390×844: корень настроек — список разделов; нажатие по строке
// открывает раздел целым экраном, «‹ Настройки» возвращает в корень; шторки
// и колонки нет, над содержимым не больше двух полос, вбок страница не едет.
func TestBrowserKarkasPhone(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s := karkasFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 3*time.Minute)

	// ---- корень ----
	karkasOpen(t, ctx, "settings")
	root := karkasPhoneRead(t, ctx)
	if root.Panel || root.ListButtons != 0 || root.ScreenTabs {
		t.Errorf("в корне телефона колонка, шторка или вкладки: %+v", root)
	}
	if !root.Search {
		t.Errorf("в корне нет поля поиска: %+v", root)
	}
	want := []string{settingsOverview}
	for _, group := range settingsGroups {
		want = append(want, group.Sections...)
	}
	if strings.Join(root.Rows, "|") != strings.Join(want, "|") {
		t.Errorf("строки корня %v, а должны %v", root.Rows, want)
	}
	if root.Back != "" {
		t.Errorf("у корня есть «назад»: %q", root.Back)
	}
	if root.SW > root.CW {
		t.Errorf("корень едет вбок: %d при %d", root.SW, root.CW)
	}
	karkasShot(t, ctx, "phone-1-root")

	// ---- строка раздела с внутренними вкладками: настоящее нажатие ----
	if err := chromedp.Run(ctx, chromedp.Click(`#feed .row.clickable[data-goto="settings:mail"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать строку «Почта»: %v", err)
	}
	waitScreen(t, ctx, "settings:mail")
	var settled bool
	_ = chromedp.Run(ctx, chromedp.Poll(`!document.querySelector('.htmx-request') && !!document.querySelector('#screen .subtabs-bar')`, &settled,
		chromedp.WithPollingTimeout(10*time.Second)))
	section := karkasPhoneRead(t, ctx)
	if section.Panel || section.ListButtons != 0 || section.ScreenTabs || section.FiltersBar {
		t.Errorf("у раздела на телефоне лишнее: %+v", section)
	}
	if section.Back != "‹ Настройки" {
		t.Errorf("над разделом «назад» зовётся %q", section.Back)
	}
	if !section.Subtabs {
		t.Errorf("у «Почты» нет внутренних вкладок: %+v", section)
	}
	// две полосы: шапка («назад» и заголовок) и внутренние вкладки
	if section.StripCount > 2 {
		t.Errorf("над содержимым раздела %d полос, а должно быть не больше двух: %v", section.StripCount, section.Strips)
	}
	if section.SW > section.CW {
		t.Errorf("раздел едет вбок: %d при %d", section.SW, section.CW)
	}
	t.Logf("«Почта» на телефоне: полосы %v, лента начинается на %d точках", section.Strips, section.FeedTop)
	karkasShot(t, ctx, "phone-3-mail")

	// ---- «‹ Настройки» возвращает в корень ----
	if err := chromedp.Run(ctx, chromedp.Click(`#screen .crumbs .crumb`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «‹ Настройки»: %v", err)
	}
	waitScreen(t, ctx, "settings")
	var returned bool
	if err := chromedp.Run(ctx, chromedp.Poll(`document.querySelectorAll('#feed .row.clickable[data-goto]').length > 10`, &returned,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil || !returned {
		t.Fatalf("после «назад» корень не нарисовался: %v", err)
	}

	// ---- Обзор: свой адрес, «назад» тоже в корень ----
	if err := chromedp.Run(ctx, chromedp.Click(`#feed .row.clickable[data-goto="settings:overview"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать строку «Обзор»: %v", err)
	}
	waitScreen(t, ctx, settingsOverview)
	_ = chromedp.Run(ctx, chromedp.Poll(`!document.querySelector('.htmx-request')`, &settled, chromedp.WithPollingTimeout(10*time.Second)))
	overview := karkasPhoneRead(t, ctx)
	if overview.Back != "‹ Настройки" || overview.H1 != "Обзор" {
		t.Errorf("Обзор на телефоне: «назад» %q, заголовок %q", overview.Back, overview.H1)
	}
	if overview.StripCount > 1 {
		t.Errorf("над Обзором %d полос: %v", overview.StripCount, overview.Strips)
	}
	if overview.SW > overview.CW {
		t.Errorf("Обзор едет вбок: %d при %d", overview.SW, overview.CW)
	}
	karkasShot(t, ctx, "phone-2-overview")
	if err := chromedp.Run(ctx, chromedp.Click(`#screen .crumbs .crumb`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «‹ Настройки» на Обзоре: %v", err)
	}
	waitScreen(t, ctx, "settings")

	// ---- поиск в корне: результаты вместо списка ----
	if err := chromedp.Run(ctx, chromedp.Click(`#search`, chromedp.ByQuery),
		chromedp.SendKeys(`#search`, "пуш", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var found bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const field = document.getElementById('search'), feedEl = document.getElementById('feed');
		  return !!field && field.value === 'пуш' && feedEl.textContent.includes('Нашлось') && !feedEl.textContent.includes('Безопасность'); })()`,
		&found, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !found {
		t.Errorf("поиск «пуш» в корне телефона не показал результаты: %v", err)
	}
	karkasShot(t, ctx, "phone-4-search")
}

// Широкое окно: колонка разделов слева, одна на все настройки, с полем
// поиска первой строкой; Обзор рядом. Нажатие по разделу в колонке открывает
// его, колонка остаётся на месте, выбран ровно один пункт.
func TestBrowserKarkasDesktop(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s := karkasFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelTime()
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1290, 806), chromedp.Navigate(srv.URL+"/#screen=settings")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	waitScreen(t, ctx, "settings")
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!!document.querySelector('#panel.sections .panel-row')`, &ok,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil || !ok {
		t.Fatalf("колонка разделов не нарисовалась: %v", err)
	}

	const state = `(() => {
	  const panelEl = document.getElementById('panel'), screenEl = document.getElementById('screen');
	  const panelRect = panelEl.getBoundingClientRect(), screenRect = screenEl.getBoundingClientRect();
	  const field = document.getElementById('search');
	  const first = panelEl.querySelector('.panel-row, .panel-heading');
	  return JSON.stringify({
	    panelLeft: panelRect.left < screenRect.left && panelRect.right <= screenRect.left + 1,
	    width: Math.round(panelRect.width),
	    searchInPanel: !!field && panelEl.contains(field),
	    searchFirst: !!field && !!first && (field.compareDocumentPosition(first) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0,
	    searchFields: document.querySelectorAll('#search').length,
	    tabs: document.querySelectorAll('.tabs-bar').length,
	    listButtons: Array.from(document.querySelectorAll('.section-picker, .panel-picker')).filter((el) => isVisible(el)).length,
	    headings: Array.from(panelEl.querySelectorAll('.panel-heading')).map((el) => el.textContent.trim()),
	    selected: Array.from(panelEl.querySelectorAll('.item.selected')).map((el) => el.textContent.trim().replace(/\s+/g, ' ')),
	    back: !!(screenEl.querySelector('.crumbs')),
	    h1: (screenEl.querySelector('h1') || {}).textContent || '',
	    sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth,
	  });
	  function isVisible(el) { const rect = el.getBoundingClientRect(); return rect.width > 0 && rect.height > 0 && getComputedStyle(el).display !== 'none'; }
	})()`
	type view struct {
		PanelLeft     bool     `json:"panelLeft"`
		Width         int      `json:"width"`
		SearchInPanel bool     `json:"searchInPanel"`
		SearchFirst   bool     `json:"searchFirst"`
		SearchFields  int      `json:"searchFields"`
		Tabs          int      `json:"tabs"`
		ListButtons   int      `json:"listButtons"`
		Headings      []string `json:"headings"`
		Selected      []string `json:"selected"`
		Back          bool     `json:"back"`
		H1            string   `json:"h1"`
		SW            int      `json:"sw"`
		CW            int      `json:"cw"`
	}
	read := func() view {
		t.Helper()
		var raw string
		if err := chromedp.Run(ctx, chromedp.Evaluate(state, &raw)); err != nil {
			t.Fatal(err)
		}
		var v view
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("разбор: %v: %s", err, raw)
		}
		return v
	}
	check := func(what string, want string) {
		t.Helper()
		v := read()
		if !v.PanelLeft || !v.SearchInPanel || !v.SearchFirst || v.SearchFields != 1 {
			t.Errorf("%s: колонка или поиск не на месте: %+v", what, v)
		}
		if v.Tabs != 0 || v.ListButtons != 0 || v.Back {
			t.Errorf("%s: на широком экране вкладки, шторка или «назад»: %+v", what, v)
		}
		if strings.Join(v.Headings, "|") != strings.Join(wantGroups, "|") {
			t.Errorf("%s: заголовки групп %v", what, v.Headings)
		}
		if len(v.Selected) != 1 || !strings.Contains(v.Selected[0], want) {
			t.Errorf("%s: выбрано %v, а должно быть одно — %q", what, v.Selected, want)
		}
		if v.SW > v.CW {
			t.Errorf("%s: страница едет вбок: %d при %d", what, v.SW, v.CW)
		}
	}
	check("Обзор", "Обзор")
	if v := read(); v.H1 != "Обзор" || v.Width < 200 || v.Width > 260 {
		t.Errorf("Обзор: заголовок %q, ширина колонки %d", v.H1, v.Width)
	}
	karkasShot(t, ctx, "desktop-1-overview")

	// настоящее нажатие по разделу в колонке
	if err := chromedp.Run(ctx, chromedp.Click(`#panel .item[hx-get="/ui?screen=settings:notify"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Уведомления» в колонке: %v", err)
	}
	waitScreen(t, ctx, "settings:notify")
	var ready bool
	_ = chromedp.Run(ctx, chromedp.Poll(`!document.querySelector('.htmx-request') && !!document.querySelector('#screen .subtabs-bar')`, &ready,
		chromedp.WithPollingTimeout(10*time.Second)))
	check("Уведомления", "Уведомления")
	karkasShot(t, ctx, "desktop-2-notify")
}

// Окно сузили или повернули телефон: у настроек документ другой целиком
// (колонка разделов с поиском — или «‹ Настройки» над заголовком), и
// перечитывается окно, а не только лента. Иначе после поворота остались бы
// колонка без разметки или экран без «назад».
func TestBrowserKarkasResize(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s := karkasFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelTime()
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1100, 820), chromedp.Navigate(srv.URL+"/#screen=settings:mail")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	waitScreen(t, ctx, "settings:mail")
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!!document.querySelector('#panel.sections') && !document.querySelector('#screen .crumbs')`, &ok,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil || !ok {
		t.Fatalf("на широком экране нет колонки: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(390, 844)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!document.getElementById('panel') && !!document.querySelector('#screen .crumbs .crumb') && document.querySelector('#screen .crumbs .crumb').textContent.trim() === '‹ Настройки'`,
		&ok, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !ok {
		t.Fatalf("после сужения окна нет «‹ Настройки» или осталась колонка: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1100, 820)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!!document.querySelector('#panel.sections') && !document.querySelector('#screen .crumbs') && !!document.querySelector('#panel #search')`,
		&ok, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !ok {
		t.Fatalf("после расширения окна нет колонки с поиском или остался «назад»: %v", err)
	}
}

// Над содержимым любого раздела настроек на телефоне не больше двух полос:
// шапка («назад» и заголовок) и внутренние вкладки. Раньше к ним
// добавлялись пять вкладок настроек, кнопка-шторка и поле поиска — около
// 190 точек из 760 до первой строки.
func TestBrowserKarkasPhoneStrips(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s := karkasFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)

	for _, screen := range settingsScreens() {
		if screen == settingsRoot {
			continue
		}
		karkasOpen(t, ctx, screen)
		state := karkasPhoneRead(t, ctx)
		t.Logf("%-28s полос %d %v, лента с %d", screen, state.StripCount, state.Strips, state.FeedTop)
		if state.Panel || state.ListButtons != 0 || state.ScreenTabs {
			t.Errorf("%s: на телефоне колонка, шторка или вкладки экрана: %+v", screen, state)
		}
		if state.Back != "‹ Настройки" {
			t.Errorf("%s: «назад» зовётся %q", screen, state.Back)
		}
		if state.StripCount > 2 {
			t.Errorf("%s: над содержимым %d полос: %v", screen, state.StripCount, state.Strips)
		}
	}
}
