package api

// Экраны «Серверов» в настоящем Chrome (1 октября 2026): список машин тонкими
// строками, экран сервера с вкладками, окно полями времени, правила, журнал,
// «Добавить сервер». Проверяем то, чем пользуется человек: нажатие по строке,
// по вкладке, по «‹ Серверы», ввод в поле и «Сохранить» полосой — а не вызов
// функции. Ждём условий на странице, а не сроков.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).
// Chrome без головы; настоящих соединений по SSH нет: у машин в фикстурах
// ключи, которые не разбираются, и проверка связи не доходит до сети.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// serversState — что видно на экранах «Серверов».
const serversState = `(() => {
  const visible = (el) => { if (!el) return false; const rect = el.getBoundingClientRect(); const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'; };
  const root = document.documentElement;
  const screenEl = document.getElementById('screen');
  const rows = Array.from(document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:servers@"]')).filter(visible).map((row) => ({
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
  return JSON.stringify({
    screen: screenEl ? screenEl.dataset.screen : '',
    h1: (document.querySelector('#screen .header h1') || {}).textContent || '',
    crumb: (() => { const c = document.querySelector('#screen .crumbs .crumb'); return c && visible(c) ? c.textContent.trim() : ''; })(),
    subtabs: Array.from(document.querySelectorAll('#screen .subtabs-bar .segment')).map((a) => a.textContent.replace(/\s+/g, ' ').trim()),
    selectedTab: (() => { const a = document.querySelector('#screen .subtabs-bar .segment.selected'); return a ? a.textContent.replace(/\s+/g, ' ').trim() : ''; })(),
    rows: rows,
    shelf: !!document.querySelector('#feed .shelf'),
    shelfText: (document.querySelector('#feed .shelf') || {}).textContent ? document.querySelector('#feed .shelf').textContent.replace(/\s+/g, ' ').trim() : '',
    barVisible: visible(barRow),
    barButtons: barRow ? Array.from(barRow.querySelectorAll('.right-side > button.action')).filter(visible).length : 0,
    barMore: barRow ? visible(barRow.querySelector('.row-menu')) : false,
    barText: barRow ? barRow.textContent.replace(/\s+/g, ' ').trim() : '',
    danger: !!danger,
    dangerLast: !!danger && last === danger.closest('#feed > .column > *'),
    dangerButtons: danger ? danger.querySelectorAll('button.action').length : 0,
    barShown: !!savebar && !savebar.hidden,
    barLabel: savebar ? ((savebar.querySelector('.what') || {}).textContent || '') : '',
    headerButtons: Array.from(document.querySelectorAll('#screen .header .controls > button.action')).filter(visible).map((b) => b.textContent.trim()),
    sw: Math.max(root.scrollWidth, document.getElementById('window').scrollWidth, document.getElementById('feed').scrollWidth), cw: root.clientWidth,
  });
})()`

type serversRow struct {
	Goto    string `json:"goto"`
	Pills   int    `json:"pills"`
	Buttons int    `json:"buttons"`
	More    bool   `json:"more"`
	Text    string `json:"text"`
}

type serversView struct {
	Screen        string       `json:"screen"`
	H1            string       `json:"h1"`
	Crumb         string       `json:"crumb"`
	Subtabs       []string     `json:"subtabs"`
	SelectedTab   string       `json:"selectedTab"`
	Rows          []serversRow `json:"rows"`
	Shelf         bool         `json:"shelf"`
	ShelfText     string       `json:"shelfText"`
	BarVisible    bool         `json:"barVisible"`
	BarButtons    int          `json:"barButtons"`
	BarMore       bool         `json:"barMore"`
	BarText       string       `json:"barText"`
	Danger        bool         `json:"danger"`
	DangerLast    bool         `json:"dangerLast"`
	DangerButtons int          `json:"dangerButtons"`
	BarShown      bool         `json:"barShown"`
	BarLabel      string       `json:"barLabel"`
	HeaderButtons []string     `json:"headerButtons"`
	SW            int          `json:"sw"`
	CW            int          `json:"cw"`
}

func serversRead(t *testing.T, ctx context.Context) serversView {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(serversState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v serversView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

// serversPoll — ждать условия на странице; не дождались — показать, что на ней.
func serversPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(serversState, &now))
		t.Fatalf("не дождались: %s (%v); состояние: %s", what, err, now)
	}
}

// serversDesktop — Chrome 1290×806 с русской локалью: поле времени тогда
// двадцатичетырёхчасовое, и «03:00» набирается цифрами, а не с AM/PM.
func serversDesktop(t *testing.T, url, screen string) context.Context {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"), chromedp.Flag("lang", "ru-RU"))...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelTime := context.WithTimeout(ctx, 5*time.Minute)
	t.Cleanup(cancelTime)
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1290, 806), chromedp.Navigate(url+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	return ctx
}

func serversBrowserOnly(t *testing.T) {
	t.Helper()
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
}

// Снимки для ревью (SEMA_SHOTS=<папка>): десктоп 1290×806 и телефон 390×844,
// данные из serversFix — пять машин разного рода и уровня, ждущие команды,
// записи журнала. Без SEMA_SHOTS ничего не снимается, а страницы просто
// собираются.
func TestBrowserServersShots(t *testing.T) {
	serversBrowserOnly(t)
	s, st := serversFix(t, true)
	serversManyLog(t, st, "srv-vpn", 30)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, serversScreen)
	waitScreen(t, ctx, serversScreen)
	for _, shot := range []struct{ name, screen string }{
		{"desktop-1-list", serversScreen},
		{"desktop-2-overview", entityAddr(serversScreen, "srv-vpn", "")},
		{"desktop-3-autonomy", entityAddr(serversScreen, "srv-vpn", serversTabAuto)},
		{"desktop-4-rules", entityAddr(serversScreen, "srv-vpn", serversTabRules)},
		{"desktop-5-journal", entityAddr(serversScreen, "srv-vpn", serversTabLog)},
		{"desktop-6-add", entityAddr(serversScreen, serversNew, "")},
		{"desktop-7-alert-host", entityAddr(serversScreen, "srv-bots", "")},
		{"desktop-8-section-autonomy", entityAddr(serversScreen, "", serversTabAuto)},
		{"desktop-9-section-journal", entityAddr(serversScreen, "", serversTabLog)},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	// низ «Обзора»: отпечаток, ключ, «Опасное» красной карточкой
	karkasOpen(t, ctx, entityAddr(serversScreen, "srv-vpn", ""))
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-2b-overview-bottom")
	// «Тонкая настройка» раздела раскрыта
	karkasOpen(t, ctx, entityAddr(serversScreen, "", serversTabAuto))
	rowsOpenFold(t, ctx)
	karkasShot(t, ctx, "desktop-8b-section-autonomy-fine")
}

func TestBrowserServersShotsPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := serversFix(t, true)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)
	for _, shot := range []struct{ name, screen string }{
		{"phone-1-list", serversScreen},
		{"phone-2-overview", entityAddr(serversScreen, "srv-vpn", "")},
		{"phone-3-autonomy", entityAddr(serversScreen, "srv-vpn", serversTabAuto)},
		{"phone-4-rules", entityAddr(serversScreen, "srv-vpn", serversTabRules)},
		{"phone-5-journal", entityAddr(serversScreen, "srv-vpn", serversTabLog)},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
}
