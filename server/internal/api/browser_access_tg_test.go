package api

// «Вход и доступ» (пять вкладок) и «Telegram → Собеседники» (права матрицей) в
// настоящем Chrome (2 октября 2026): нажатие по вкладке, по клетке, по «···», ввод
// в поле поиска и в поле названия, чип отбора, окно вопроса — а не вызов функции.
// Ждём условий на странице, а не сроков; состояние читается и со страницы, и из
// базы. Chrome без головы; в Telegram тест не ходит (история в фикстуре выключена,
// а чтение у группы с темами не включается), окна WebAuthn не открываются: ключ
// не заводится, проверяются разметка и переходы.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// mxState — что видно в матрице прав и вокруг неё.
const mxState = `(() => {
  const visible = (el) => { if (!el) return false; if (el.checkVisibility && !el.checkVisibility()) return false;
    const rect = el.getBoundingClientRect(); const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'; };
  const root = document.documentElement;
  const table = document.querySelector('table.matrix.rights');
  const rows = Array.from(document.querySelectorAll('tr.matrix-row'));
  const text = (el) => el.textContent.replace(/\s+/g, ' ').trim();
  let maxRight = 0;
  if (table) for (const el of table.querySelectorAll('*')) { const r = el.getBoundingClientRect(); if (r.width > 0) maxRight = Math.max(maxRight, r.right); }
  const search = document.getElementById('search');
  return JSON.stringify({
    screen: (document.getElementById('screen') || {dataset: {}}).dataset.screen || '',
    present: !!table,
    theadShown: table ? visible(table.querySelector('thead')) : false,
    headers: table ? Array.from(table.querySelectorAll('thead th')).map(text) : [],
    fragments: document.querySelectorAll('details.fragment').length,
    rows: rows.map((r) => ({
      id: r.id, nested: r.classList.contains('nested'), level: ['ok', 'warn', 'alert', 'info'].find((l) => r.classList.contains(l)) || '',
      who: text(r.querySelector('td.who .title')), caption: r.querySelector('td.who small') ? text(r.querySelector('td.who small')) : '',
      on: r.querySelectorAll('.cell-button.on').length, off: r.querySelectorAll('.cell-button.off').length,
      na: r.querySelectorAll('.cell-na').length, naShown: Array.from(r.querySelectorAll('.cell-na')).filter(visible).length,
      inherited: r.querySelectorAll('.cell-button.inherited').length,
      chips: Array.from(r.querySelectorAll('.cell-button')).filter(visible).map(text),
      chipHeight: (() => { const b = Array.from(r.querySelectorAll('.cell-button')).find(visible); return b ? Math.round(b.getBoundingClientRect().height) : 0; })(),
      selects: r.querySelectorAll('select').length, menu: !!r.querySelector('.row-menu'),
      whoWidth: Math.round(r.querySelector('td.who').getBoundingClientRect().width), rowWidth: Math.round(r.getBoundingClientRect().width),
    })),
    filters: Array.from(document.querySelectorAll('.filters-bar .segments .segment')).map((a) => ({text: text(a), selected: a.classList.contains('selected')})),
    searchValue: search ? search.value : null, searchHint: search ? (search.placeholder || '') : null, searchShown: visible(search),
    subtabs: Array.from(document.querySelectorAll('#screen .subtabs-bar .segment')).map(text),
    selectedTab: (() => { const a = document.querySelector('#screen .subtabs-bar .segment.selected'); return a ? text(a) : ''; })(),
    more: Array.from(document.querySelectorAll('#feed button')).filter((b) => b.getAttribute('data-action') === '$more').map(text),
    busy: !!document.querySelector('.htmx-request'),
    sw: Math.max(root.scrollWidth, document.getElementById('window').scrollWidth, document.getElementById('feed').scrollWidth), cw: root.clientWidth,
    maxRight: Math.round(maxRight),
  });
})()`

type mxRow struct {
	ID         string   `json:"id"`
	Nested     bool     `json:"nested"`
	Level      string   `json:"level"`
	Who        string   `json:"who"`
	Caption    string   `json:"caption"`
	On         int      `json:"on"`
	Off        int      `json:"off"`
	NA         int      `json:"na"`
	NAShown    int      `json:"naShown"`
	Inherited  int      `json:"inherited"`
	Chips      []string `json:"chips"`
	ChipHeight int      `json:"chipHeight"`
	Selects    int      `json:"selects"`
	Menu       bool     `json:"menu"`
	WhoWidth   int      `json:"whoWidth"`
	RowWidth   int      `json:"rowWidth"`
}

type mxFilter struct {
	Text     string `json:"text"`
	Selected bool   `json:"selected"`
}

type mxView struct {
	Screen      string     `json:"screen"`
	Present     bool       `json:"present"`
	THeadShown  bool       `json:"theadShown"`
	Headers     []string   `json:"headers"`
	Fragments   int        `json:"fragments"`
	Rows        []mxRow    `json:"rows"`
	Filters     []mxFilter `json:"filters"`
	SearchValue *string    `json:"searchValue"`
	SearchHint  *string    `json:"searchHint"`
	SearchShown bool       `json:"searchShown"`
	Subtabs     []string   `json:"subtabs"`
	SelectedTab string     `json:"selectedTab"`
	More        []string   `json:"more"`
	Busy        bool       `json:"busy"`
	SW          int        `json:"sw"`
	CW          int        `json:"cw"`
	MaxRight    int        `json:"maxRight"`
}

func mxRead(t *testing.T, ctx context.Context) mxView {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(mxState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v mxView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

func (v mxView) row(id string) *mxRow {
	for i := range v.Rows {
		if v.Rows[i].ID == id {
			return &v.Rows[i]
		}
	}
	return nil
}

// mxPoll — ждать условия на странице; не дождались — показать, что на ней.
func mxPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(mxState, &now))
		t.Fatalf("не дождались: %s (%v); состояние: %s", what, err, now)
	}
}

// mxClick — нажать, дождавшись узла: нет такого узла — тест падает через двадцать
// секунд с картиной страницы, а не висит до конца срока Chrome.
func mxClick(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	mxPoll(t, ctx, "есть узел "+sel, `!!document.querySelector(`+jsString(sel)+`)`)
	stnClick(t, ctx, sel)
}

// mxServer — шестнадцать чатов: двенадцать личных, две группы (одна с тремя
// темами), два канала; у одного из личных выданы права.
func mxServer(t *testing.T) (*Server, *store.Store, *httptest.Server) {
	t.Helper()
	s, st := telegramMatrixFix(t)
	for i := 0; i < 11; i++ {
		if err := st.SaveTGChat(&store.TGChat{Account: telegramAccount, ID: int64(200 + i),
			Title: fmt.Sprintf("Собеседник %02d", i), Kind: "user", Class: store.ClassSensitive}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, st, srv
}

// cellSel — кнопка клетки строки по началу её подсказки.
func cellSel(rowID, title string) string {
	return `#mx-` + rowID + ` .cell-button[title^="` + title + `"]`
}

// Матрица прав в настоящем Chrome: клетка нажимается и переживает перечитывание,
// поиск и отбор остаются после нажатия, у темы наследованное отличается от своего,
// класс меняется списком, «na» не нажимается.
func TestBrowserTelegramMatrixWalk(t *testing.T) {
	serversBrowserOnly(t)
	s, st, srv := mxServer(t)
	screen := tgScreen + "@" + tgDigits
	ctx := serversDesktop(t, srv.URL, screen)
	waitScreen(t, ctx, screen)
	mxPoll(t, ctx, "матрица нарисована", `document.querySelectorAll('tr.matrix-row').length >= 19`)

	// ---- вид: одна матрица, без ленивых частей, чипы и поиск сверху ----
	v := mxRead(t, ctx)
	// последняя колонка без слова — «···» строки (класс чата, у темы «Как у группы»)
	if got := strings.Join(v.Headers, "|"); got != "|Читать|Файлы|Отвечать|Писать первым|Админ|Не трогать|Напоминать|" {
		t.Errorf("шапка матрицы: %q", got)
	}
	if v.Fragments != 0 {
		t.Errorf("на экране остались ленивые части: %d", v.Fragments)
	}
	if len(v.Rows) != 19 || len(v.More) != 0 {
		t.Errorf("в матрице %d строк (ждали 19: шестнадцать чатов и три темы), «ещё» %v", len(v.Rows), v.More)
	}
	var chips []string
	for _, f := range v.Filters {
		chips = append(chips, f.Text)
	}
	if got := strings.Join(chips, "|"); got != "Все · 16|Личные · 12|Группы · 2|Каналы · 2|С правами · 0" || !v.Filters[0].Selected {
		t.Errorf("чипы отбора: %q", got)
	}
	if !v.SearchShown || v.SearchHint == nil || !strings.Contains(*v.SearchHint, "чат") {
		t.Errorf("поле поиска сверху не ищет чаты: %+v", v.SearchHint)
	}
	if v.SW > v.CW {
		t.Errorf("матрица едет вбок: %d при %d", v.SW, v.CW)
	}
	karkasShot(t, ctx, "tg-matrix-desktop")
	// канал: «na» вместо кнопок, у темы — наследованное пунктиром
	channel := v.row("mx-tg-101")
	// четыре кнопки (читать, файлы, публиковать, не трогать), три прочерка (отвечать,
	// админ, напоминать) и «···» с классом
	if channel == nil || channel.NA != 3 || channel.Off != 4 || !channel.Menu {
		t.Errorf("строка канала владельца: %+v", channel)
	}
	if topic := v.row("mx-tg-3-2"); topic == nil || !topic.Nested || topic.Inherited != 5 || topic.NA != 2 || topic.Menu {
		t.Errorf("строка темы как у группы: %+v", topic)
	}

	// ---- нажать клетку: право включено в базе, а после перечитывания всё так же ----
	click := cellSel("tg-1", "Митя · читать переписку")
	stnClick(t, ctx, click)
	mxPoll(t, ctx, "клетка стала включённой", `!!document.querySelector('`+click+`.on')`)
	if c, _ := st.TGChat(telegramAccount, 1); !c.Collect {
		t.Fatal("нажали «Читать», а движок видит выключенным")
	}
	// после перечитывания страницы
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, screen)
	mxPoll(t, ctx, "матрица после перечитывания", `!!document.querySelector('`+click+`.on')`)
	if row := mxRead(t, ctx).row("mx-tg-1"); row == nil || row.Level != "ok" {
		t.Errorf("после перечитывания строка не «ok»: %+v", row)
	}
	stnClick(t, ctx, click)
	mxPoll(t, ctx, "клетка снова выключена", `!!document.querySelector('`+click+`.off')`)
	if c, _ := st.TGChat(telegramAccount, 1); c.Collect {
		t.Fatal("второе нажатие не выключило право")
	}

	// ---- поиск на сервере: набрать, найти одну строку, нажать клетку — поиск остался ----
	stnType(t, ctx, "#search", "Собеседник 03")
	mxPoll(t, ctx, "найден один чат", `document.querySelectorAll('tr.matrix-row').length === 1 && document.querySelector('#mx-tg-203') !== null`)
	found := cellSel("tg-203", "Собеседник 03 · забирать файлы")
	stnClick(t, ctx, found)
	mxPoll(t, ctx, "клетка включена, поиск и одна строка остались",
		`!!document.querySelector('`+found+`.on') && document.querySelectorAll('tr.matrix-row').length === 1 && document.getElementById('search').value === 'Собеседник 03'`)
	if c, _ := st.TGChat(telegramAccount, 203); !c.Files {
		t.Error("право «Файлы» найденному поиском чату не выдано")
	}
	karkasShot(t, ctx, "tg-matrix-search")

	// ---- чип отбора «Каналы», «na» не кнопка, список — класс ----
	// поиск стирается так, как стирает человек: поле пустеет и шлёт запрос
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const q = document.getElementById('search'); q.value = '';
	  q.dispatchEvent(new Event('input', {bubbles: true})); return true; })()`, nil)); err != nil {
		t.Fatal(err)
	}
	mxPoll(t, ctx, "матрица заново", `document.querySelectorAll('tr.matrix-row').length >= 19`)
	stnClickText(t, ctx, `.filters-bar .segments .segment`, "Каналы")
	mxPoll(t, ctx, "два канала", `document.querySelectorAll('tr.matrix-row').length === 2`)
	if v = mxRead(t, ctx); !v.Filters[3].Selected || v.row("mx-tg-102") == nil || v.row("mx-tg-102").NA != 4 {
		t.Errorf("отбор «Каналы»: %+v", v.Filters)
	}
	// клетка «Админ» канала — прочерк: ни кнопки, ни действия
	var buttons int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelectorAll('#mx-tg-101 .cell-na[title*="личный чат"]').length + 10 * document.querySelectorAll('#mx-tg-101 .cell-na button').length`, &buttons)); err != nil {
		t.Fatal(err)
	}
	if buttons != 1 {
		t.Errorf("у канала клетка «Админ» не одна неактивная: %d", buttons)
	}
	// действие над каналом (публикация владельцу) держит отбор
	pub := cellSel("tg-101", "Новости Ozon · публиковать")
	stnClick(t, ctx, pub)
	mxPoll(t, ctx, "публикация включена, отбор остался", `!!document.querySelector('`+pub+`.on') && document.querySelectorAll('tr.matrix-row').length === 2`)
	if c, _ := st.TGChat(telegramAccount, 101); !c.Send {
		t.Error("владельцу не включилась публикация")
	}
	// класс канала — пункт «···» строки, как менял человек; отбор остаётся
	mxClick(t, ctx, `#mx-tg-101 .row-menu > summary`)
	stnClickText(t, ctx, `#mx-tg-101 .row-menu .menu-item`, "Класс «секретное»")
	mxPoll(t, ctx, "класс сохранён, капсула на строке, отбор остался",
		`(() => { const row = document.getElementById('mx-tg-101'); return !!row && row.textContent.includes('секретное') && document.querySelectorAll('tr.matrix-row').length === 2 && !document.querySelector('.htmx-request'); })()`)
	if c, _ := st.TGChat(telegramAccount, 101); c.Class != store.ClassSecret {
		t.Errorf("класс канала: %q", c.Class)
	}

	// ---- тема: наследованное → своё, «Как у группы» возвращает ----
	// у группы выдано «файлы» напрямую в базе; экран перечитывается целиком
	if err := st.SetTGChatRight(telegramAccount, 3, "files", true); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, screen)
	mxPoll(t, ctx, "матрица с темами", `!!document.querySelector('#mx-tg-3-2')`)
	topicFiles := cellSel("tg-3-2", "Клуб · Разговоры · забирать файлы")
	mxPoll(t, ctx, "у темы право группы пунктиром", `!!document.querySelector('`+topicFiles+`.on.inherited')`)
	stnClick(t, ctx, topicFiles)
	mxPoll(t, ctx, "право темы стало своим (выключено)", `!!document.querySelector('`+topicFiles+`.off:not(.inherited)')`)
	if topic := mxRead(t, ctx).row("mx-tg-3-2"); topic == nil || !topic.Menu || topic.Caption != "свои права" {
		t.Errorf("у темы со своими правами нет «···»: %+v", topic)
	}
	stnClick(t, ctx, `#mx-tg-3-2 .row-menu > summary`)
	stnClickText(t, ctx, `#mx-tg-3-2 .row-menu .menu-item`, "Как у группы")
	mxPoll(t, ctx, "тема снова как у группы", `!!document.querySelector('`+topicFiles+`.on.inherited')`)
	if mxRead(t, ctx).SW > mxRead(t, ctx).CW {
		t.Error("матрица с темами едет вбок")
	}
	_ = s
}

// Телефон 390×844: матрица раскладывается строками с чипами прав, ничего не едет
// вбок, права, которых не бывает, чипами не рисуются; существующие матрицы выдачи
// (секрет модулям, области памяти) читаются так же.
func TestBrowserMatrixOnPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, st, srv := mxServer(t)
	_ = s
	ctx := phoneChrome(t, srv.URL, 5*time.Minute)
	screen := tgScreen + "@" + tgDigits
	karkasOpen(t, ctx, screen)
	mxPoll(t, ctx, "матрица нарисована", `document.querySelectorAll('tr.matrix-row').length >= 19`)
	v := mxRead(t, ctx)
	if v.THeadShown {
		t.Error("на телефоне осталась шапка таблицы")
	}
	if v.SW > v.CW || v.MaxRight > v.CW {
		t.Errorf("матрица едет вбок: ширина страницы %d при окне %d, правый край таблицы %d", v.SW, v.CW, v.MaxRight)
	}
	user := v.row("mx-tg-1")
	if user == nil {
		t.Fatal("строки личного чата нет")
	}
	// строка — название на всю ширину и ряд чипов под ним; подписи прав внутри чипов
	if user.WhoWidth < user.RowWidth-8 {
		t.Errorf("название не на всю ширину строки: %d из %d", user.WhoWidth, user.RowWidth)
	}
	if got := strings.Join(user.Chips, "|"); got != "Читать|Файлы|Отвечать|Писать первым|Админ|Не трогать|✓Напоминать" {
		t.Errorf("чипы личного чата: %q", got)
	}
	if user.ChipHeight < 30 {
		t.Errorf("чип ниже пальца: %d", user.ChipHeight)
	}
	channel := v.row("mx-tg-102")
	if channel == nil || channel.NAShown != 0 || channel.NA != 4 {
		t.Errorf("права, которых у канала нет, нарисованы чипами: %+v", channel)
	}
	if strings.Contains(strings.Join(channel.Chips, "|"), "Админ") {
		t.Errorf("у канала чип «Админ»: %v", channel.Chips)
	}
	karkasShot(t, ctx, "tg-matrix-phone")
	// настоящее касание по чипу включает право
	click := cellSel("tg-1", "Митя · читать переписку")
	stnClick(t, ctx, click)
	mxPoll(t, ctx, "чип включён", `!!document.querySelector('`+click+`.on')`)
	if c, _ := st.TGChat(telegramAccount, 1); !c.Collect {
		t.Error("касание по чипу не включило право")
	}
	if v = mxRead(t, ctx); v.SW > v.CW {
		t.Errorf("после касания экран едет вбок: %d при %d", v.SW, v.CW)
	}

	// ---- «Кому выдан» у секрета и «Области и доступ» памяти: те же правила ----
	fx := secretsAgentsFix(t)
	srv2 := httptest.NewServer(fx.S.Handler())
	defer srv2.Close()
	ctx2 := phoneChrome(t, srv2.URL, 5*time.Minute)
	karkasOpen(t, ctx2, entityAddr(secretsScreen, "gitlab-token", secretTabGrants))
	mxPoll(t, ctx2, "матрица выдачи нарисована", `document.querySelectorAll('table.matrix .cell-button').length >= 1`)
	var grant struct {
		Head bool    `json:"head"`
		SW   int     `json:"sw"`
		CW   int     `json:"cw"`
		H    float64 `json:"h"`
		Text string  `json:"text"`
	}
	if err := chromedp.Run(ctx2, chromedp.Evaluate(`(() => { const b = document.querySelector('table.matrix .cell-button'); const th = document.querySelector('table.matrix thead');
	  return { head: getComputedStyle(th).display !== 'none', sw: document.getElementById('feed').scrollWidth, cw: document.documentElement.clientWidth,
	    h: b.getBoundingClientRect().height, text: b.textContent.replace(/\s+/g, ' ').trim() }; })()`, &grant)); err != nil {
		t.Fatal(err)
	}
	if grant.Head || grant.SW > grant.CW || grant.H < 30 || !strings.Contains(grant.Text, "gitlab-token") {
		t.Errorf("матрица выдачи секрета на телефоне: %+v", grant)
	}
	karkasShot(t, ctx2, "secret-grants-phone")
}

// Снимки для ревью (SEMA_SHOTS=<папка>): десктоп — пять вкладок «Доступа»,
// «Telegram → Собеседники» матрицей с группой с темами, «Ответы и пределы»;
// телефон — «Ключи», «Кто внутри», матрица. Без SEMA_SHOTS ничего не снимается.
func TestBrowserAccessAndTelegramShots(t *testing.T) {
	serversBrowserOnly(t)
	// доступ: два ключа, три сессии, два токена, журнал
	as, _, _ := accessFix(t)
	asrv := httptest.NewServer(as.Handler())
	defer asrv.Close()
	ctx := serversDesktop(t, asrv.URL, accessScreen)
	waitScreen(t, ctx, accessScreen)
	for _, shot := range []struct{ name, screen string }{
		{"desktop-access-1-keys", accessScreen},
		{"desktop-access-2-inside", accessScreen + "~" + accessTabInside},
		{"desktop-access-3-tokens", accessScreen + "~" + accessTabTokens},
		{"desktop-access-4-network", accessScreen + "~" + accessTabNetwork},
		{"desktop-access-5-log", accessScreen + "~" + accessTabLog},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	// «Тонкая настройка» вкладки «Ключи» раскрыта
	karkasOpen(t, ctx, accessScreen)
	rowsOpenFold(t, ctx)
	karkasShot(t, ctx, "desktop-access-1b-keys-fine")

	// Telegram: матрица и «Ответы и пределы»
	_, _, srv := mxServer(t)
	ctx = serversDesktop(t, srv.URL, tgScreen)
	screen := tgScreen + "@" + tgDigits
	for _, shot := range []struct{ name, screen string }{
		{"desktop-tg-1-matrix", screen},
		{"desktop-tg-2-answer", screen + "~" + tgTabAnswer},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	karkasOpen(t, ctx, screen+"~"+tgTabAnswer)
	rowsOpenFold(t, ctx)
	karkasShot(t, ctx, "desktop-tg-2b-answer-fine")

	// телефон
	pctx := phoneChrome(t, asrv.URL, 4*time.Minute)
	for _, shot := range []struct{ name, screen string }{
		{"phone-access-1-keys", accessScreen},
		{"phone-access-2-inside", accessScreen + "~" + accessTabInside},
	} {
		karkasOpen(t, pctx, shot.screen)
		karkasShot(t, pctx, shot.name)
	}
	tctx := phoneChrome(t, srv.URL, 4*time.Minute)
	karkasOpen(t, tctx, screen)
	karkasShot(t, tctx, "phone-tg-1-matrix")
	karkasOpen(t, tctx, screen+"~"+tgTabAnswer)
	karkasShot(t, tctx, "phone-tg-2-answer")
}

// «Ответы и пределы» в Chrome: слово-вызов и предел автоответов — поля на самой
// вкладке, «раз в» и «за заход» — в «Тонкой настройке»; набор в поле попадает в
// нижнюю полосу, «Сохранить» пишет, движок читает то же; плохое значение полоса
// отвергает словами и ничего не пишет.
func TestBrowserTelegramAnswerFields(t *testing.T) {
	serversBrowserOnly(t)
	s, st, srv := mxServer(t)
	screen := tgScreen + "@" + tgDigits + "~" + tgTabAnswer
	ctx := serversDesktop(t, srv.URL, screen)
	waitScreen(t, ctx, screen)
	mxPoll(t, ctx, "поля нарисованы", `!!document.querySelector('input[name="help_word"]') && !!document.querySelector('input[name="reply_max_hour"]')`)

	stnType(t, ctx, `input[name="help_word"]`, "бот")
	stnType(t, ctx, `input[name="reply_max_hour"]`, "45")
	stnPoll(t, ctx, "полоса: две правки", `(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 2'); })()`)
	karkasShot(t, ctx, "tg-answer-edited")
	stnSave(t, ctx)
	if s.eng.TGHelpWord() != "бот" || s.eng.TGReplyMaxHour() != 45 {
		t.Errorf("после «Сохранить» движок читает слово %q и предел %d", s.eng.TGHelpWord(), s.eng.TGReplyMaxHour())
	}

	// плохое значение: полоса отказывает словами, ничего не записано
	stnType(t, ctx, `input[name="reply_max_hour"]`, "0")
	stnClick(t, ctx, `#save-bar [data-save="save"]`)
	stnPoll(t, ctx, "отказ стоит у строки", `document.body.textContent.includes('нужно целое число от 1')`)
	if s.eng.TGReplyMaxHour() != 45 {
		t.Errorf("отвергнутое значение записалось: %d", s.eng.TGReplyMaxHour())
	}

	// «Тонкая настройка»: «раз в» — поле учётки
	rowsOpenFold(t, ctx)
	stnType(t, ctx, `#fold-rare input[name="every"]`, "7")
	stnPoll(t, ctx, "правка в полосе", `(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено'); })()`)
	// плохая правка выше ещё не отменена: вернём поле к сохранённому и сохраним одну
	stnType(t, ctx, `input[name="reply_max_hour"]`, "45")
	stnSave(t, ctx)
	if acc, _ := st.TGAccount(telegramAccount); acc.Every != 7 {
		t.Errorf("«Как часто заходить» учётки: %d, ждали 7", acc.Every)
	}
}

// «Вход и доступ» в Chrome: вкладки по очереди нажатием, тонкие строки, окна
// вопроса у необратимого, переименование ключа полем, журнал порциями и чипами.
func TestBrowserAccessWalk(t *testing.T) {
	serversBrowserOnly(t)
	s, st, _ := accessFix(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, accessScreen)
	waitScreen(t, ctx, accessScreen)
	mxPoll(t, ctx, "ключи нарисованы", `!!document.getElementById('exp-passkey-1')`)

	// ---- «Ключи»: пять вкладок, две тонкие строки, сессий нет ----
	v := mxRead(t, ctx)
	// счётчик стоит сразу за названием вкладки
	if got := strings.Join(v.Subtabs, "|"); got != "Ключи2|Кто внутри3|Токены2|Доверенная сеть|Кто стучался10 отбито" || !strings.HasPrefix(v.SelectedTab, "Ключи") {
		t.Errorf("вкладки: %q, выбрана %q", got, v.SelectedTab)
	}
	var keys struct {
		Rows     int  `json:"rows"`
		Thin     bool `json:"thin"`
		Sessions bool `json:"sessions"`
		How      int  `json:"how"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const rows = Array.from(document.querySelectorAll('#sec-access-keys .row-wrap'));
	  return { rows: rows.length,
	    thin: rows.every((r) => r.querySelectorAll('.row > .right-side > button.action').length === 0 && !!r.querySelector('.row-menu')),
	    sessions: /Выйти|Завершить/.test(document.getElementById('feed').textContent),
	    how: Array.from(document.querySelectorAll('#feed summary')).filter((s) => s.textContent.trim() === 'Как это устроено').length }; })()`, &keys)); err != nil {
		t.Fatal(err)
	}
	if keys.Rows != 2 || !keys.Thin || keys.Sessions || keys.How != 1 {
		t.Errorf("«Ключи»: %+v", keys)
	}
	if v.SW > v.CW {
		t.Errorf("«Ключи» едут вбок: %d при %d", v.SW, v.CW)
	}

	// ---- переименовать полем в раскрытии ----
	mxClick(t, ctx, `#exp-passkey-1 > summary`)
	mxPoll(t, ctx, "форма названия раскрыта", `!!document.querySelector('#exp-passkey-1[open] input[name="value"]')`)
	stnType(t, ctx, `#exp-passkey-1 input[name="value"]`, "Рабочий мак")
	mxClick(t, ctx, `#exp-passkey-1 .form-actions button`)
	mxPoll(t, ctx, "ключ переименован на экране", `document.getElementById('feed').textContent.includes('Рабочий мак')`)
	if keys, _ := st.Passkeys(); keys[0].Name != "Рабочий мак" {
		t.Errorf("название ключа: %q", keys[0].Name)
	}

	// ---- удаление ключа: окно вопроса, «Отмена» оставляет ключ ----
	const secondKey = `#sec-access-keys .row-wrap:has(#exp-passkey-2)`
	mxClick(t, ctx, secondKey+` .row-menu > summary`)
	stnClickText(t, ctx, secondKey+` .row-menu .menu-item`, "Удалить")
	mxPoll(t, ctx, "окно вопроса открыто", `(() => { const d = document.querySelector('dialog#confirm[open]'); return !!d && d.textContent.includes('iPhone'); })()`)
	mxClick(t, ctx, `dialog#confirm button.cancel`)
	mxPoll(t, ctx, "окно закрыто", `!document.querySelector('dialog#confirm[open]')`)
	if keys, _ := st.Passkeys(); len(keys) != 2 {
		t.Fatal("«Отмена» удалила ключ")
	}

	// ---- «Кто внутри»: нажатием по вкладке ----
	stnClickText(t, ctx, `#screen .subtabs-bar .segment`, "Кто внутри")
	waitScreen(t, ctx, accessScreen+"~"+accessTabInside)
	mxPoll(t, ctx, "сессии нарисованы", `document.querySelectorAll('#sec-access-sessions .row-wrap').length === 3`)
	var inside struct {
		Rows       int  `json:"rows"`
		Complete   bool `json:"complete"`
		DangerLast bool `json:"dangerLast"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const rows = Array.from(document.querySelectorAll('#sec-access-sessions .row-wrap'));
	  const danger = document.querySelector('#feed .section.danger'); const cols = Array.from(document.querySelectorAll('#feed > .column > *'));
	  return { rows: rows.length,
	    complete: rows.every((r) => Array.from(r.querySelectorAll('.row > .right-side > button.action')).some((b) => b.textContent.trim() === 'Завершить')),
	    dangerLast: !!danger && cols[cols.length - 1] === danger.closest('#feed > .column > *') }; })()`, &inside)); err != nil {
		t.Fatal(err)
	}
	if inside.Rows != 3 || !inside.Complete || !inside.DangerLast {
		t.Errorf("«Кто внутри»: %+v", inside)
	}
	karkasShot(t, ctx, "access-inside")
	// «Завершить все…» спрашивает: «Отмена» оставляет сессии
	stnClickText(t, ctx, `#feed .section.danger button.action`, "Завершить все")
	mxPoll(t, ctx, "окно вопроса про все сессии", `(() => { const d = document.querySelector('dialog#confirm[open]'); return !!d && d.textContent.includes('включая этот браузер'); })()`)
	karkasShot(t, ctx, "access-confirm-logout-all")
	mxClick(t, ctx, `dialog#confirm button.cancel`)
	mxPoll(t, ctx, "окно закрыто", `!document.querySelector('dialog#confirm[open]')`)
	if rest, _ := st.Sessions(); len(rest) != 3 {
		t.Fatal("«Отмена» завершила сессии")
	}
	// «Завершить» у одной
	stnClickText(t, ctx, `#sec-access-sessions .row-wrap .row > .right-side > button.action`, "Завершить")
	mxPoll(t, ctx, "сессия закрыта", `document.querySelectorAll('#sec-access-sessions .row-wrap').length === 2`)
	if rest, _ := st.Sessions(); len(rest) != 2 {
		t.Errorf("сессий в базе %d, ждали 2", len(rest))
	}

	// ---- «Токены»: отзыв спрашивает ----
	stnClickText(t, ctx, `#screen .subtabs-bar .segment`, "Токены")
	waitScreen(t, ctx, accessScreen+"~"+accessTabTokens)
	mxPoll(t, ctx, "токены нарисованы", `document.querySelectorAll('#sec-access-tokens .row-wrap').length === 2`)
	mxClick(t, ctx, `#sec-access-tokens .row-wrap .row-menu > summary`)
	stnClickText(t, ctx, `#sec-access-tokens .row-wrap .row-menu .menu-item`, "Отозвать")
	mxPoll(t, ctx, "окно вопроса про токен", `!!document.querySelector('dialog#confirm[open]')`)
	mxClick(t, ctx, `dialog#confirm button.cancel`)
	mxPoll(t, ctx, "окно закрыто", `!document.querySelector('dialog#confirm[open]')`)
	if tokens, _ := st.Tokens(); !tokens[0].Revoked.IsZero() {
		t.Fatal("«Отмена» отозвала токен")
	}

	// ---- «Доверенная сеть» ----
	stnClickText(t, ctx, `#screen .subtabs-bar .segment`, "Доверенная сеть")
	waitScreen(t, ctx, accessScreen+"~"+accessTabNetwork)
	mxPoll(t, ctx, "вкладка нарисована", `document.body.textContent.includes('Сейчас без входа пускаются')`)
	if v = mxRead(t, ctx); v.SW > v.CW {
		t.Errorf("«Доверенная сеть» едет вбок: %d при %d", v.SW, v.CW)
	}

	// ---- «Кто стучался»: двадцать строк, «Показать ещё», чип «Отказы» ----
	stnClickText(t, ctx, `#screen .subtabs-bar .segment`, "Кто стучался")
	waitScreen(t, ctx, accessScreen+"~"+accessTabLog)
	mxPoll(t, ctx, "журнал нарисован", `document.querySelectorAll('#sec-access-journal .row-wrap').length === 20`)
	if v = mxRead(t, ctx); len(v.More) != 1 || !strings.Contains(v.More[0], "20 из") {
		t.Errorf("«Показать ещё»: %v", v.More)
	}
	if got := fmt.Sprint(len(v.Filters)); got != "4" {
		t.Errorf("чипов журнала %s", got)
	}
	mxClick(t, ctx, `#feed button[data-action="$more"]`)
	mxPoll(t, ctx, "показано больше двадцати", `document.querySelectorAll('#sec-access-journal .row-wrap').length > 20`)
	stnClickText(t, ctx, `.filters-bar .segments .segment`, "Отказы")
	mxPoll(t, ctx, "отказы одной строкой со счётчиком", `document.querySelectorAll('#sec-access-journal .row-wrap').length === 1 && document.getElementById('sec-access-journal').textContent.includes('×10')`)
	karkasShot(t, ctx, "access-log-denied")
}
