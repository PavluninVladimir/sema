package api

// Навигация настроек в Chrome. Одна и та же группа выбирается слева
// на широком экране и сверху на телефоне.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
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

func clickSettings(t *testing.T, ctx context.Context, query, screen string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Click(query, chromedp.ByQuery)); err != nil {
		t.Fatalf("открыть %s: %v", screen, err)
	}
	waitScreen(t, ctx, screen)
}

func settingsGroupButton(screen string) string {
	return `#panel .item[hx-get="/ui?screen=` + screen + `"]`
}

func settingsCatalogRow(screen string) string {
	return `#feed .row.clickable[data-goto="` + screen + `"]`
}

func TestBrowserKarkasPhone(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: SEMA_BROWSER=1")
	}
	s := karkasFixtures(t)
	st := s.eng.Store()
	if err := st.SaveMAXAccount(&store.MAXAccount{Phone: "+70000000001", State: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMAXChat(&store.MAXChat{ID: 1, Title: "Семья", Kind: "dialog", Collect: true}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 3*time.Minute)
	karkasOpen(t, ctx, settingsRoot)
	root := karkasPhoneRead(t, ctx)
	if !root.Panel || !root.Search || root.H1 != "Обзор" || root.Back != "" || root.SW > root.CW {
		t.Fatalf("Обзор на телефоне: %+v", root)
	}
	clickSettings(t, ctx, settingsGroupButton(settingsChannels), settingsChannels)
	channels := karkasPhoneRead(t, ctx)
	if channels.H1 != "Каналы" || strings.Join(channels.Rows, "|") != strings.Join(settingsGroups[0].sections(), "|") {
		t.Fatalf("каталог каналов на телефоне: %+v", channels)
	}
	karkasShot(t, ctx, "phone-settings-channels")
	clickSettings(t, ctx, settingsCatalogRow(maxScreen), maxScreen)
	section := karkasPhoneRead(t, ctx)
	if !section.Panel || section.ListButtons != 0 || section.ScreenTabs || section.Back != "‹ Каналы" || section.SW > section.CW {
		t.Errorf("MAX на телефоне: %+v", section)
	}
	var connected bool
	if err := chromedp.Run(ctx, chromedp.Poll(`document.querySelector('[data-hl="r:max-acc"]')?.textContent.includes('Личный аккаунт') && !!document.querySelector('[data-hl="r:max-send"] .switch') && !!document.querySelector('#sec-max-chats .matrix.rights')`,
		&connected, chromedp.WithPollingTimeout(8*time.Second))); err != nil || !connected {
		t.Fatalf("подключённый MAX и чаты не появились: %v", err)
	}
	karkasShot(t, ctx, "phone-settings-max")
	botPage := botsListAddr("max")
	clickSettings(t, ctx, `#screen [hx-get="/ui?screen=`+url.QueryEscape(botPage)+`"]`, botPage)
	clickSettings(t, ctx, settingsGroupButton(settingsChannels), settingsChannels)
	clickSettings(t, ctx, settingsCatalogRow(maxScreen), maxScreen)
	clickSettings(t, ctx, `#screen .crumbs .crumb`, settingsChannels)
	clickSettings(t, ctx, settingsCatalogRow(mailScreen), mailScreen)
	mail := karkasPhoneRead(t, ctx)
	if !mail.Subtabs || mail.Back != "‹ Каналы" || mail.SW > mail.CW {
		t.Errorf("Почта на телефоне: %+v", mail)
	}
	clickSettings(t, ctx, settingsGroupButton(settingsRoot), settingsRoot)
	if err := chromedp.Run(ctx, chromedp.SendKeys(`#search`, "пуш", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var found bool
	if err := chromedp.Run(ctx, chromedp.Poll(`document.getElementById('search')?.value === 'пуш' && document.getElementById('feed')?.textContent.includes('Нашлось') && !document.getElementById('settings-catalog')`,
		&found, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !found {
		t.Errorf("поиск на телефоне: %v", err)
	}
}

func TestBrowserKarkasDesktop(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: SEMA_BROWSER=1")
	}
	s := karkasFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 4*time.Minute)
	defer cancelTime()
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1290, 806), chromedp.Navigate(srv.URL+"/#screen=settings")); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, settingsRoot)
	var panelOK bool
	if err := chromedp.Run(ctx, chromedp.Poll(`(() => {
	  const panel = document.querySelector('#panel.sections'), screen = document.querySelector('#screen');
	  return panel && screen && panel.getBoundingClientRect().right <= screen.getBoundingClientRect().left + 1
	    && panel.querySelectorAll('.panel-row').length === 5 && !!panel.querySelector('#search');
	})()`, &panelOK, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !panelOK {
		t.Fatalf("навигация не появилась слева: %v", err)
	}
	for _, group := range settingsGroups {
		clickSettings(t, ctx, settingsGroupButton(group.ID), group.ID)
		if group.ID == settingsChannels {
			karkasShot(t, ctx, "desktop-settings-channels")
		}
		for _, screen := range group.sections() {
			clickSettings(t, ctx, settingsCatalogRow(screen), screen)
			var selected bool
			query := `(() => {const items = document.querySelectorAll('#panel.sections .item.selected'); return items.length === 1 && items[0].getAttribute('hx-get') === ` + jsString("/ui?screen="+group.ID) + `;})()`
			if err := chromedp.Run(ctx, chromedp.Evaluate(query, &selected)); err != nil || !selected {
				t.Errorf("%s: навигация потеряла активную группу %s (%v)", screen, group.ID, err)
			}
			clickSettings(t, ctx, `#screen .crumbs .crumb`, group.ID)
		}
	}
}

func TestBrowserKarkasResize(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: SEMA_BROWSER=1")
	}
	s := karkasFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 2*time.Minute)
	karkasOpen(t, ctx, maxScreen)
	for _, size := range []struct{ width, height int }{{1100, 820}, {390, 844}, {320, 780}, {1100, 820}} {
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(int64(size.width), int64(size.height))); err != nil {
			t.Fatal(err)
		}
		var ok bool
		if err := chromedp.Run(ctx, chromedp.Poll(`(() => {
		  const panel = document.querySelector('#panel.sections'), main = document.querySelector('#screen');
		  if (!panel || main?.dataset.screen !== 'settings:max') return false;
		  const p = panel.getBoundingClientRect(), s = main.getBoundingClientRect();
		  return p.width > 0 && document.documentElement.scrollWidth <= innerWidth && (innerWidth <= 720 ? p.bottom <= s.top + 1 : p.right <= s.left + 1);
		})()`, &ok, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !ok {
			t.Fatalf("на ширине %d панель не на месте: %v", size.width, err)
		}
	}
}
