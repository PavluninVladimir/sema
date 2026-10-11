package api

// «Работа Сёмы» и «Что ушло наружу» в настоящем Chrome (2 октября 2026, шаг 8
// плана `design/nastroyki-2/README.md`). Проверяем то, чем пользуется человек:
// вписал число в поле, увидел полосу, нажал «Сохранить», перечитал экран; нажал
// раздел в колонке; открыл закладку со старым адресом — и главное, что
// сохранённое с нового места доезжает до клиента: опрос — в разметку чата,
// окно склейки — в подписку на провод. Ждём условий на странице, а не сроков.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).
// Chrome без головы; окон, звука и системных уведомлений нет.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// workType — вписать значение в поле настоящим вводом: встать в поле, стереть
// прежнее и набрать новое.
func workType(t *testing.T, ctx context.Context, name, value string) {
	t.Helper()
	sel := `#feed input[name="` + name + `"]`
	var current string
	if err := chromedp.Run(ctx, chromedp.Value(sel, &current, chromedp.ByQuery)); err != nil {
		t.Fatalf("прочитать поле %s: %v", name, err)
	}
	keys := kb.End + strings.Repeat(kb.Backspace, len([]rune(current))+1) + value
	if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery), chromedp.SendKeys(sel, keys, chromedp.ByQuery)); err != nil {
		t.Fatalf("вписать %s = %s: %v", name, value, err)
	}
}

// workPoll — ждать условия на странице; не дождались — сказать, что на ней.
func workPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('screen')||{dataset:{}}).dataset.screen+' | '+((document.getElementById('save-bar')||{}).textContent||'').trim()`, &now))
		t.Fatalf("не дождались: %s (%v); на странице: %s", what, err, now)
	}
}

// workWindowState — что видно над содержимым раздела настроек и вокруг него.
const workWindowState = `(() => {
  const visible = (el) => { if (!el) return false; const rect = el.getBoundingClientRect(); const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'; };
  const root = document.documentElement;
  const screenEl = document.getElementById('screen');
  const panelEl = document.getElementById('panel');
  return JSON.stringify({
    screen: screenEl ? screenEl.dataset.screen : '',
    h1: ((document.querySelector('#screen .header h1') || {}).textContent || '').trim(),
    crumb: (() => { const c = document.querySelector('#screen .crumbs .crumb'); return c && visible(c) ? c.textContent.trim() : ''; })(),
    subtabs: Array.from(document.querySelectorAll('#screen .subtabs-bar .segment')).map((a) => a.textContent.replace(/\s+/g, ' ').trim()),
    selectedTab: (() => { const a = document.querySelector('#screen .subtabs-bar .segment.selected'); return a ? a.textContent.replace(/\s+/g, ' ').trim() : ''; })(),
    panel: !!panelEl && visible(panelEl),
    selected: panelEl ? Array.from(panelEl.querySelectorAll('.item.selected')).map((el) => el.textContent.trim().replace(/\s+/g, ' ')) : [],
    panelTitles: panelEl ? Array.from(panelEl.querySelectorAll('.panel-row .item, .item')).map((el) => el.textContent.trim().replace(/\s+/g, ' ')) : [],
    fields: Array.from(document.querySelectorAll('#feed .field input, #feed .field select')).filter(visible).map((el) => el.name),
    barShown: !!document.getElementById('save-bar') && !document.getElementById('save-bar').hidden,
    sw: Math.max(root.scrollWidth, document.getElementById('window').scrollWidth, document.getElementById('feed').scrollWidth), cw: root.clientWidth,
  });
})()`

type workWindow struct {
	Screen      string   `json:"screen"`
	H1          string   `json:"h1"`
	Crumb       string   `json:"crumb"`
	Subtabs     []string `json:"subtabs"`
	SelectedTab string   `json:"selectedTab"`
	Panel       bool     `json:"panel"`
	Selected    []string `json:"selected"`
	PanelTitles []string `json:"panelTitles"`
	Fields      []string `json:"fields"`
	BarShown    bool     `json:"barShown"`
	SW          int      `json:"sw"`
	CW          int      `json:"cw"`
}

func workRead(t *testing.T, ctx context.Context) workWindow {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(workWindowState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v workWindow
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

// Десктоп: на «Работе Сёмы» вписать опрос и окно склейки, увидеть полосу,
// сохранить, перечитать экран — и убедиться, что клиент получил новое. Опрос
// приезжает в разметку чата (`data-poll`, `data-poll-wired`, а при живом проводе
// — в `hx-trigger`), окно склейки — в подписку на провод: кадр после сигнала
// приходит не раньше, чем через сохранённое окно.
func TestBrowserWorkSettingsReachClient(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, st := testServer(t)
	chat, err := st.CreateChat("проба опроса")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	screen := entityAddr(watchScreen, "", workTabScreen)
	ctx := rowsDesktop(t, srv.URL, screen)
	waitScreen(t, ctx, screen)
	workPoll(t, ctx, "поля «Экрана и связи» нарисованы", `!!document.querySelector('#feed input[name="poll_s"]') && !!document.querySelector('#feed input[name="tz"]')`)

	first := workRead(t, ctx)
	if first.H1 != "Работа Сёмы" || first.SelectedTab != "Экран и связь" || len(first.Selected) != 1 || !strings.Contains(first.Selected[0], "Работа Сёмы") {
		t.Errorf("вкладка «Экран и связь»: %+v", first)
	}
	if strings.Join(first.Subtabs, " · ") != "Самонаблюдение · Экран и связь · Чаты и ветки" {
		t.Errorf("вкладки раздела: %v", first.Subtabs)
	}
	for _, name := range []string{"tz", "poll_s", "poll_live_s", "live_ms", "ws_per_session"} {
		found := false
		for _, f := range first.Fields {
			found = found || f == name
		}
		if !found {
			t.Errorf("на вкладке нет поля %q: %v", name, first.Fields)
		}
	}
	if first.SW > first.CW {
		t.Errorf("вкладка едет вбок: %d при %d", first.SW, first.CW)
	}
	karkasShot(t, ctx, "work-desktop-2-screen")

	// настоящий ввод: опрос, опрос при проводе и окно склейки
	workType(t, ctx, "poll_s", "7")
	workType(t, ctx, "poll_live_s", "90")
	workType(t, ctx, "live_ms", "1500")
	workPoll(t, ctx, "полоса сохранения с тремя правками",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 3'); })()`)
	karkasShot(t, ctx, "work-desktop-3-screen-savebar")
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	workPoll(t, ctx, "после сохранения полоса погасла, поля на месте",
		`(() => { const bar = document.getElementById('save-bar'); const v = (n) => (document.querySelector('#feed input[name="' + n + '"]') || {}).value;
		  return !!bar && bar.hidden && v('poll_s') === '7' && v('poll_live_s') === '90' && v('live_ms') === '1500'; })()`)
	live := s.eng.LiveSettings()
	if live.PollS != 7 || live.PollLiveS != 90 || live.Window != 1500*time.Millisecond {
		t.Fatalf("движок читает %+v, а вписано 7 / 90 / 1500", live)
	}
	// перезагрузка страницы: значения на месте
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, screen)
	workPoll(t, ctx, "после перезагрузки значения на месте",
		`(() => { const v = (n) => (document.querySelector('#feed input[name="' + n + '"]') || {}).value;
		  return v('poll_s') === '7' && v('poll_live_s') === '90' && v('live_ms') === '1500'; })()`)

	// «Тонкая настройка» вкладки: свёрнутая строка на месте, внутри семь
	// параметров — провод, снимок, порция списка, предзагрузка,
	// переадресация на https, порог медленного запроса и профилировщик (perf-pass)
	workPoll(t, ctx, "«Тонкая настройка» нарисована", `!!document.getElementById('fold-rare')`)
	if v := rowsRead(t, ctx); !strings.Contains(v.Summary, "7 параметров") || v.State != "всё по умолчанию" {
		t.Errorf("«Тонкая настройка» вкладки: %+v", v)
	}
	rowsScrollToFold(t, ctx)
	rowsOpenFold(t, ctx)
	if v := rowsRead(t, ctx); v.Rows != 7 {
		t.Errorf("«Тонкая настройка» раскрыта: %+v", v)
	}
	karkasShot(t, ctx, "work-desktop-4-screen-fine")

	// 1. опрос доехал до разметки чата: переход по хэшу, как у человека
	chatScreen := "chat:" + strconv.FormatInt(chat, 10)
	karkasOpen(t, ctx, chatScreen)
	var poll, wired string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('screen').getAttribute('data-poll')`, &poll),
		chromedp.Evaluate(`document.getElementById('screen').getAttribute('data-poll-wired')`, &wired)); err != nil {
		t.Fatal(err)
	}
	if poll != "7" || wired != "90" {
		t.Errorf("клиент получил опрос %q и %q, а сохранено 7 и 90", poll, wired)
	}
	// провод открылся — опрос стал редким, и частоту берёт тот же атрибут
	workPoll(t, ctx, "провод открыт и опрос переключён на «при живом проводе»",
		`(() => { const w = window.sema && sema.wire && sema.wire(); return !!w && w.open && (w.poll || '').includes('every 90s'); })()`)

	// 2. окно склейки доехало до подписки: новое соединение получает кадр не
	// раньше сохранённых 1500 мс. Сначала ждём тишины — кадры от прежних
	// сигналов (сохранение само трогает хранилище) не должны сойти за ответ
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { window.__probe = {open: false, last: Date.now(), start: 0, at: 0};
	  const sock = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/ws');
	  sock.onopen = () => { window.__probe.open = true; window.__probe.last = Date.now(); };
	  sock.onmessage = () => { const p = window.__probe; p.last = Date.now(); if (p.start && !p.at) p.at = p.last; };
	  window.__probe.sock = sock; return true; })()`, nil)); err != nil {
		t.Fatal(err)
	}
	workPoll(t, ctx, "пробное соединение открыто и вокруг тихо", `window.__probe.open && Date.now() - window.__probe.last > 2500`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__probe.start = Date.now(); true`, nil)); err != nil {
		t.Fatal(err)
	}
	s.eng.Changed("work-probe")
	workPoll(t, ctx, "кадр пришёл", `window.__probe.at > 0`)
	var elapsed float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__probe.at - window.__probe.start`, &elapsed)); err != nil {
		t.Fatal(err)
	}
	t.Logf("кадр пришёл через %.0f мс после сигнала при окне склейки 1500 мс", elapsed)
	if elapsed < 1400 {
		t.Errorf("кадр пришёл через %.0f мс после сигнала: окно склейки 1500 мс до подписки не доехало", elapsed)
	}
	_ = chromedp.Run(ctx, chromedp.Evaluate(`window.__probe.sock.close(); true`, nil))

	// возврат умолчаний тем же жестом: «по умолчанию» у строки кладёт число
	karkasOpen(t, ctx, screen)
	workType(t, ctx, "poll_s", "4")
	workType(t, ctx, "poll_live_s", "60")
	workType(t, ctx, "live_ms", "300")
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	workPoll(t, ctx, "умолчания сохранены", `(() => { const bar = document.getElementById('save-bar'); return !!bar && bar.hidden; })()`)
	if live := s.eng.LiveSettings(); live.PollS != 4 || live.PollLiveS != 60 || live.Window != 300*time.Millisecond {
		t.Errorf("после возврата умолчаний движок читает %+v", live)
	}
}

// Десктоп: вкладки «Работы Сёмы», «Чаты и ветки» сохраняются полосой, «Уведомления»
// ведут сюда строками-ссылками, «Что ушло наружу» открывается из колонки и по
// старому адресу, лента и отправка живут внутри каркаса.
func TestBrowserWorkAndSentInFrameDesktop(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, st := testServer(t)
	if err := st.AddSent(&store.SentRow{Consumer: "core:queue", Model: "qwen3", OK: true,
		Class: store.ClassPlain, TokensIn: 262, TokensOut: 418, Seconds: 57.8, Prompt: "текст запроса"}); err != nil {
		t.Fatal(err)
	}
	list, _ := st.SentList(false, "", "", 1)
	if len(list) == 0 {
		t.Fatal("отправка не записалась")
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// закладка со старым адресом открывает раздел в каркасе и называет новый адрес
	ctx := rowsDesktop(t, srv.URL, "sent")
	waitScreen(t, ctx, sentScreen)
	workPoll(t, ctx, "раздел нарисован", `!!document.querySelector('#panel .item.selected') && !!document.querySelector('#screen .header h1')`)
	v := workRead(t, ctx)
	if v.H1 != "Что ушло наружу" || !v.Panel || len(v.Selected) != 1 || !strings.Contains(v.Selected[0], "Что ушло наружу") {
		t.Errorf("«Что ушло наружу» по старому адресу: %+v", v)
	}
	// имя в колонке равно заголовку экрана — у каждого пункта колонки
	if v.SW > v.CW {
		t.Errorf("раздел едет вбок: %d при %d", v.SW, v.CW)
	}
	karkasShot(t, ctx, "sent-desktop-1-section")

	// из колонки: «Уведомления» → «Что ушло наружу» настоящими нажатиями
	if err := chromedp.Run(ctx, chromedp.Click(`#panel .item[hx-get="/ui?screen=settings:notify~show"], #panel .item[hx-get="/ui?screen=settings:notify"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Уведомления» в колонке: %v", err)
	}
	waitScreen(t, ctx, "settings:notify")
	if err := chromedp.Run(ctx, chromedp.Click(`#screen .subtabs-bar a.segment[hx-get="/ui?screen=settings:notify~show"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Что показывать»: %v", err)
	}
	waitScreen(t, ctx, "settings:notify~show")
	workPoll(t, ctx, "«Что показывать» нарисовано, провода и опроса здесь нет", `(() => { const names = Array.from(document.querySelectorAll('#feed .field input, #feed .field select')).map((el) => el.name);
	  return names.includes('counter') && names.includes('keep') && !names.includes('poll_s') && !names.includes('tz') && !names.includes('cmd_ttl') && !!document.querySelector('#feed [data-goto="settings:watch~screen"]'); })()`)
	karkasShot(t, ctx, "notify-desktop-show")
	// ссылка «Переехало» ведёт туда, где настройки теперь
	if err := chromedp.Run(ctx, chromedp.Click(`#feed [data-goto="settings:watch~chats"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать строку-ссылку: %v", err)
	}
	waitScreen(t, ctx, "settings:watch~chats")
	workPoll(t, ctx, "вкладка «Чаты и ветки» нарисована", `!!document.querySelector('#feed input[name="cmd_ttl"]') && !!document.querySelector('#feed input[name="thread_tail"]')`)
	chats := workRead(t, ctx)
	if chats.H1 != "Работа Сёмы" || chats.SelectedTab != "Чаты и ветки" || len(chats.Selected) != 1 || !strings.Contains(chats.Selected[0], "Работа Сёмы") {
		t.Errorf("вкладка «Чаты и ветки»: %+v", chats)
	}
	karkasShot(t, ctx, "work-desktop-5-chats")
	// число пинков — ноль законен: «не пинать»
	workType(t, ctx, "nudge_max", "0")
	workType(t, ctx, "cmd_ttl", "48")
	workPoll(t, ctx, "полоса: две правки", `(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 2'); })()`)
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	workPoll(t, ctx, "сохранено", `(() => { const bar = document.getElementById('save-bar'); return !!bar && bar.hidden; })()`)
	if s.eng.NudgeMax() != 0 || s.eng.CommandChatTTL() != 48 {
		t.Errorf("движок читает пинков %d и срок %d, а вписано 0 и 48", s.eng.NudgeMax(), s.eng.CommandChatTTL())
	}

	// первая вкладка — прежнее содержимое: наблюдатель
	karkasOpen(t, ctx, watchScreen)
	watch := workRead(t, ctx)
	if watch.H1 != "Работа Сёмы" || watch.SelectedTab != "Самонаблюдение" {
		t.Errorf("первая вкладка: %+v", watch)
	}
	karkasShot(t, ctx, "work-desktop-1-watch")

	// из колонки — «Что ушло наружу»
	if err := chromedp.Run(ctx, chromedp.Click(`#panel .item[hx-get="/ui?screen=settings:sent"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Что ушло наружу» в колонке: %v", err)
	}
	waitScreen(t, ctx, sentScreen)
	workPoll(t, ctx, "раздел нарисован", `!!document.querySelector('#feed [data-goto="settings:sent~log"]') || !!document.querySelector('#feed .row .action')`)
	sent := workRead(t, ctx)
	if sent.H1 != "Что ушло наружу" || len(sent.Selected) != 1 || !strings.Contains(sent.Selected[0], "Что ушло наружу") {
		t.Errorf("«Что ушло наружу» из колонки: %+v", sent)
	}
	// лента — внутри каркаса: колонка на месте, выбран тот же пункт
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=sent~log'; true`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, sentScreen+"~"+sentTabLog)
	workPoll(t, ctx, "лента нарисована", `document.querySelector('#feed').textContent.includes('Ядро · queue')`)
	journal := workRead(t, ctx)
	if journal.H1 != "Журнал отправок" || !journal.Panel || len(journal.Selected) != 1 || !strings.Contains(journal.Selected[0], "Что ушло наружу") || journal.Crumb != "Что ушло наружу" {
		t.Errorf("лента отправок: %+v", journal)
	}
	karkasShot(t, ctx, "sent-desktop-2-journal")
	// одна отправка: нажатие «Показать текст» — тоже внутри каркаса
	if err := chromedp.Run(ctx, chromedp.Click(`#feed [data-goto="settings:sent~`+strconv.FormatInt(list[0].ID, 10)+`"], #feed .row.clickable`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать отправку: %v", err)
	}
	waitScreen(t, ctx, sentScreen+"~"+strconv.FormatInt(list[0].ID, 10))
	workPoll(t, ctx, "текст отправки нарисован", `document.querySelector('#feed').textContent.includes('текст запроса')`)
	one := workRead(t, ctx)
	if !one.Panel || len(one.Selected) != 1 || !strings.Contains(one.Selected[0], "Что ушло наружу") || one.Crumb != "Журнал отправок" {
		t.Errorf("одна отправка: %+v", one)
	}
}

// Телефон, 390×844: в корне настроек раздел зовётся «Работа Сёмы», у него три
// вкладки и ничего не едет вбок; «Что ушло наружу» открывается целым экраном с
// «‹ Настройки».
func TestBrowserWorkAndSentPhone(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s := karkasFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 3*time.Minute)

	karkasOpen(t, ctx, "settings")
	root := karkasPhoneRead(t, ctx)
	for _, want := range []string{watchScreen, sentScreen} {
		found := false
		for _, row := range root.Rows {
			found = found || row == want
		}
		if !found {
			t.Errorf("в корне нет строки %s: %v", want, root.Rows)
		}
	}
	var rows string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').textContent.replace(/\s+/g, ' ')`, &rows)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Работа Сёмы", "Что ушло наружу", "Вход и доступ"} {
		if !strings.Contains(rows, name) {
			t.Errorf("в корне телефона нет имени %q", name)
		}
	}
	for _, old := range []string{"Самонаблюдение", "Что Сёма нашёл в себе"} {
		if strings.Contains(rows, old) {
			t.Errorf("в корне телефона осталось прежнее имя %q", old)
		}
	}
	if root.SW > root.CW {
		t.Errorf("корень едет вбок: %d при %d", root.SW, root.CW)
	}
	karkasShot(t, ctx, "work-phone-1-root")
	// корень длинный: «Работа Сёмы» — внизу группы «Система», подведём к середине окна
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#feed .row.clickable[data-goto="settings:watch"]').scrollIntoView({block: 'center'}); true`, nil))
	karkasShot(t, ctx, "work-phone-1b-root-system")

	// строка раздела → экран с «‹ Настройки» и тремя вкладками
	if err := chromedp.Run(ctx, chromedp.Click(`#feed .row.clickable[data-goto="settings:watch"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать строку «Работа Сёмы»: %v", err)
	}
	waitScreen(t, ctx, watchScreen)
	workPoll(t, ctx, "вкладки раздела нарисованы", `document.querySelectorAll('#screen .subtabs-bar .segment').length === 3`)
	v := workRead(t, ctx)
	if v.Panel || v.Crumb != "‹ Настройки" || v.H1 != "Работа Сёмы" {
		t.Errorf("«Работа Сёмы» на телефоне: %+v", v)
	}
	if v.SW > v.CW {
		t.Errorf("«Самонаблюдение» едет вбок: %d при %d", v.SW, v.CW)
	}
	karkasShot(t, ctx, "work-phone-2-watch")
	for i, tab := range []string{entityAddr(watchScreen, "", workTabScreen), entityAddr(watchScreen, "", workTabChats)} {
		if err := chromedp.Run(ctx, chromedp.Click(`#screen .subtabs-bar a.segment[hx-get="/ui?screen=`+tab+`"]`, chromedp.ByQuery)); err != nil {
			t.Fatalf("нажать вкладку %s: %v", tab, err)
		}
		waitScreen(t, ctx, tab)
		field := []string{"poll_s", "cmd_ttl"}[i]
		workPoll(t, ctx, "поля вкладки нарисованы", `!!document.querySelector('#feed input[name="`+field+`"]')`)
		tabView := workRead(t, ctx)
		if tabView.SW > tabView.CW {
			t.Errorf("вкладка %s едет вбок: %d при %d", tab, tabView.SW, tabView.CW)
		}
		karkasShot(t, ctx, []string{"work-phone-3-screen", "work-phone-4-chats"}[i])
	}
	// «Тонкая настройка» вкладки «Экран и связь» — раскрывается без бокового сдвига
	karkasOpen(t, ctx, entityAddr(watchScreen, "", workTabScreen))
	rowsPoll(t, ctx, "«Тонкая настройка» нарисована", `!!document.getElementById('fold-rare')`)
	rowsScrollToFold(t, ctx)
	rowsOpenFold(t, ctx)
	if v := rowsRead(t, ctx); v.Rows != 6 || v.SW > v.CW {
		t.Errorf("«Тонкая настройка» на телефоне: %+v", v)
	}
	karkasShot(t, ctx, "work-phone-5-screen-fine")

	// «Что ушло наружу»: целый экран с «‹ Настройки»
	karkasOpen(t, ctx, "settings")
	if err := chromedp.Run(ctx, chromedp.Click(`#feed .row.clickable[data-goto="settings:sent"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать строку «Что ушло наружу»: %v", err)
	}
	waitScreen(t, ctx, sentScreen)
	workPoll(t, ctx, "раздел нарисован", `((document.querySelector('#screen .header h1') || {}).textContent || '').trim() === 'Что ушло наружу'`)
	sent := workRead(t, ctx)
	if sent.Panel || sent.Crumb != "‹ Настройки" || sent.SW > sent.CW {
		t.Errorf("«Что ушло наружу» на телефоне: %+v", sent)
	}
	karkasShot(t, ctx, "sent-phone-1-section")
}
