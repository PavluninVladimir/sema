package api

// «Ключи и секреты» и «Агенты» в настоящем Chrome (2 октября 2026): нажатие по
// строке, по вкладке, по клетке матрицы, по «‹ Ключи и секреты», ввод в поле и
// «Сохранить» нижней полосой — а не вызов функции. Ждём условий на странице, а
// не сроков; состояние читается и со страницы, и из базы. Chrome без головы,
// настоящих соединений нет, буфер обмена не трогается: «Скопировать» не
// нажимается — проверяется, что у кнопки есть цель копирования.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// saState — что видно на экранах «Ключей и секретов» и «Агентов».
const saState = `(() => {
  // checkVisibility: содержимое закрытого <details> скрыто через content-visibility, и
  // прямоугольник у такой строки всё равно ненулевой
  const visible = (el) => { if (!el) return false; if (el.checkVisibility && !el.checkVisibility()) return false;
    const rect = el.getBoundingClientRect(); const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'; };
  const root = document.documentElement;
  const q = (sel) => Array.from(document.querySelectorAll(sel));
  const info = (row) => ({
    goto: row.getAttribute('data-goto'),
    pills: row.querySelectorAll('.right-side .pill').length,
    buttons: Array.from(row.querySelectorAll('.right-side > button.action')).filter(visible).length,
    more: !!row.querySelector('.row-menu'),
    text: row.textContent.replace(/\s+/g, ' ').trim(),
  });
  const bar = document.getElementById('sec-entity');
  const barRow = bar ? bar.querySelector('.row') : null;
  const danger = document.querySelector('#feed .section.danger');
  const sections = Array.from(document.querySelectorAll('#feed > .column > *')).filter(visible);
  const last = sections.length ? sections[sections.length - 1] : null;
  const savebar = document.getElementById('save-bar');
  return JSON.stringify({
    screen: (document.getElementById('screen') || {dataset: {}}).dataset.screen || '',
    h1: (document.querySelector('#screen .header h1') || {}).textContent || '',
    crumb: (() => { const c = document.querySelector('#screen .crumbs .crumb'); return c && visible(c) ? c.textContent.trim() : ''; })(),
    subtabs: q('#screen .subtabs-bar .segment').map((a) => a.textContent.replace(/\s+/g, ' ').trim()),
    selectedTab: (() => { const a = document.querySelector('#screen .subtabs-bar .segment.selected'); return a ? a.textContent.replace(/\s+/g, ' ').trim() : ''; })(),
    secretRows: q('#feed .row-wrap > .row.clickable[data-goto^="settings:secrets@"]').filter(visible).map(info),
    agentRows: q('#feed .row-wrap > .row.clickable[data-goto^="settings:agents@"]').filter(visible).map(info),
    shelf: !!document.querySelector('#feed .shelf'),
    shelfText: document.querySelector('#feed .shelf') ? document.querySelector('#feed .shelf').textContent.replace(/\s+/g, ' ').trim() : '',
    barVisible: visible(barRow),
    barButtons: barRow ? Array.from(barRow.querySelectorAll('.right-side > button.action')).filter(visible).length : 0,
    barMore: barRow ? visible(barRow.querySelector('.row-menu')) : false,
    barText: barRow ? barRow.textContent.replace(/\s+/g, ' ').trim() : '',
    danger: !!danger,
    dangerLast: !!danger && last === danger.closest('#feed > .column > *'),
    dangerButtons: danger ? danger.querySelectorAll('button.action').length : 0,
    barShown: !!savebar && !savebar.hidden,
    barLabel: savebar ? ((savebar.querySelector('.what') || {}).textContent || '') : '',
    headerButtons: q('#screen .header .controls > button.action').filter(visible).map((b) => b.textContent.trim()),
    sw: Math.max(root.scrollWidth, document.getElementById('window').scrollWidth, document.getElementById('feed').scrollWidth), cw: root.clientWidth,
  });
})()`

type saView struct {
	Screen        string       `json:"screen"`
	H1            string       `json:"h1"`
	Crumb         string       `json:"crumb"`
	Subtabs       []string     `json:"subtabs"`
	SelectedTab   string       `json:"selectedTab"`
	SecretRows    []serversRow `json:"secretRows"`
	AgentRows     []serversRow `json:"agentRows"`
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

func saRead(t *testing.T, ctx context.Context) saView {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(saState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v saView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

// saPoll — ждать условия на странице; не дождались — показать, что на ней.
func saPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(saState, &now))
		t.Fatalf("не дождались: %s (%v); состояние: %s", what, err, now)
	}
}

// saClick — подвести элемент к середине окна и нажать: закреплённая строка
// сущности перекрывает элемент у верхнего края, и нажатие уходило бы в неё.
func saClick(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const el = document.querySelector(`+jsString(sel)+`); if (el) el.scrollIntoView({block: 'center'}); return !!el; })()`, nil),
		chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать %s: %v", sel, err)
	}
}

func saClickTab(t *testing.T, ctx context.Context, name, screen string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'subtabs-bar')]//a[contains(@class,'segment')][contains(.,'`+name+`')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать вкладку «%s»: %v", name, err)
	}
	waitScreen(t, ctx, screen)
	saPoll(t, ctx, "экран перечитан", `!document.querySelector('.htmx-request')`)
}

func saClickRow(t *testing.T, ctx context.Context, screen string) {
	t.Helper()
	sel := `#feed .row-wrap > .row.clickable[data-goto="` + screen + `"] a.row-link`
	if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать строку %s: %v", screen, err)
	}
	waitScreen(t, ctx, screen)
	saPoll(t, ctx, "экран нарисован", `!!document.getElementById('sec-entity')`)
}

func saSave(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	saPoll(t, ctx, "полоса погасла после сохранения", `(() => { const bar = document.getElementById('save-bar'); return !!bar && bar.hidden && !document.querySelector('.htmx-request'); })()`)
}

func saServer(t *testing.T) (*saFixture, *httptest.Server) {
	t.Helper()
	fx := secretsAgentsFix(t)
	srv := httptest.NewServer(fx.S.Handler())
	t.Cleanup(srv.Close)
	return fx, srv
}

// «Ключи и секреты»: список → секрет → «Обзор», замена значения полем и нижней
// полосой, «Кому выдан» (выдать и отозвать в матрице), «Журнал» → «‹ Ключи и
// секреты» → «Добавить секрет».
func TestBrowserSecretsWalk(t *testing.T) {
	serversBrowserOnly(t)
	fx, srv := saServer(t)
	st := fx.St
	ctx := serversDesktop(t, srv.URL, secretsScreen)
	waitScreen(t, ctx, secretsScreen)
	saPoll(t, ctx, "список секретов нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:secrets@"]').length > 0`)

	// ---- список: две группы, тонкие строки, полка, одна кнопка в шапке ----
	list := saRead(t, ctx)
	if len(list.SecretRows) != 6 || !list.Shelf || !strings.Contains(list.ShelfText, "Ждут вас") {
		t.Fatalf("список: ваших строк %d (служебные свёрнуты), полка %v: %+v", len(list.SecretRows), list.Shelf, list)
	}
	for _, row := range list.SecretRows {
		if row.Pills > 2 || row.Buttons > 1 || !row.More {
			t.Errorf("строка не тонкая — капсул %d, кнопок %d, «···» %v: %s", row.Pills, row.Buttons, row.More, row.Text)
		}
	}
	if strings.Join(list.HeaderButtons, "|") != "Добавить секрет" {
		t.Errorf("кнопки шапки раздела: %v", list.HeaderButtons)
	}
	if len(list.Subtabs) != 3 || !strings.HasPrefix(list.Subtabs[0], "Секреты") || !strings.HasPrefix(list.Subtabs[1], "Шифрование") ||
		!strings.HasPrefix(list.Subtabs[2], "Журнал") || !strings.Contains(list.Subtabs[0], "2 ждут") {
		t.Errorf("вкладки раздела: %v", list.Subtabs)
	}
	if list.SW > list.CW {
		t.Errorf("список едет вбок: %d при %d", list.SW, list.CW)
	}
	karkasShot(t, ctx, "s1-list")
	// служебные свёрнуты, а раскрываются нажатием; «Удалить» у них нигде нет
	var deletes int
	if err := chromedp.Run(ctx, chromedp.Click(`#fold-secrets-service > summary`, chromedp.ByQuery)); err != nil {
		t.Fatalf("раскрыть «Заводятся сами»: %v", err)
	}
	saPoll(t, ctx, "служебные раскрыты", `document.querySelectorAll('#fold-secrets-service .row-wrap').length >= 20`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`Array.from(document.querySelectorAll('#fold-secrets-service button')).filter((b) => b.textContent.includes('Удалить')).length`, &deletes)); err != nil {
		t.Fatal(err)
	}
	if deletes != 0 {
		t.Errorf("у служебных секретов %d кнопок «Удалить»", deletes)
	}
	karkasShot(t, ctx, "s1b-list-service")

	// ---- в секрет кликом по строке ----
	saClickRow(t, ctx, entityAddr(secretsScreen, "planner-token", ""))
	one := saRead(t, ctx)
	if one.Crumb != "‹ Ключи и секреты" || one.H1 != "planner-token" || !one.BarVisible || !one.BarMore ||
		!strings.HasPrefix(one.SelectedTab, "Обзор") || len(one.Subtabs) != 3 {
		t.Errorf("экран секрета: %+v", one)
	}
	if !one.Danger || !one.DangerLast || one.DangerButtons < 1 {
		t.Errorf("«Опасное» должно стоять последним и с видимой кнопкой: %+v", one)
	}
	karkasShot(t, ctx, "s2-secret-overview")

	// «Удалить…» спрашивает и называет, кто останется без ключа; «Отмена» оставляет секрет
	if err := chromedp.Run(ctx, chromedp.Click(`#sec-secret-danger button[data-confirm]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Удалить…»: %v", err)
	}
	saPoll(t, ctx, "окно вопроса открыто и называет сервер MCP", `(() => { const d = document.querySelector('dialog#confirm[open]'); return !!d && d.textContent.includes('сервер MCP «sing»'); })()`)
	karkasShot(t, ctx, "s2b-confirm-delete")
	if err := chromedp.Run(ctx, chromedp.Click(`dialog#confirm button.cancel`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	saPoll(t, ctx, "окно вопроса закрыто", `!document.querySelector('dialog#confirm[open]')`)
	if _, ok := st.Secret("planner-token"); !ok {
		t.Fatal("«Отмена» удалила секрет")
	}

	// ---- «Заменить значение»: поле пустое, набор попадает в полосу и не показывается ----
	const field = `form.deferred input[type=password][name="value"]`
	saPoll(t, ctx, "поле замены нарисовано пустым", `(() => { const f = document.querySelector('`+field+`'); return !!f && f.value === ''; })()`)
	if err := chromedp.Run(ctx, chromedp.Focus(field, chromedp.ByQuery), chromedp.SendKeys(field, saNewValueSentinel, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	saPoll(t, ctx, "полоса сохранения одной правки, а набранное в ней закрыто",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1') && bar.textContent.includes('новое значение') && !bar.textContent.includes('`+saNewValueSentinel+`'); })()`)
	karkasShot(t, ctx, "s2c-secret-replace-typed")
	saSave(t, ctx)
	if v, _ := st.SecretValue("planner-token"); v != saNewValueSentinel {
		t.Fatalf("после «Сохранить» ядро читает %q", v)
	}
	// после сохранения поле снова пустое, а значения нет нигде на странице
	saPoll(t, ctx, "поле снова пустое", `(() => { const f = document.querySelector('`+field+`'); return !!f && f.value === ''; })()`)
	var shown bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.outerHTML.includes('`+saNewValueSentinel+`')`, &shown)); err != nil {
		t.Fatal(err)
	}
	if shown {
		t.Error("значение секрета осталось в разметке страницы")
	}
	if got, _ := st.SecretLogFiltered("planner-token", store.SecretLogChanges, 5); len(got) == 0 || got[0].Note != "значение заменено" {
		t.Errorf("замена не записана в журнал: %+v", got)
	}

	// ---- ключ SSH: у «Скопировать» есть цель — публичная часть, а приватной нет нигде ----
	karkasOpen(t, ctx, entityAddr(secretsScreen, "ssh:Microtik", ""))
	saPoll(t, ctx, "экран ключа нарисован", `!!document.getElementById('sec-entity') && document.querySelectorAll('button[data-action="$copy"]').length >= 2`)
	var copyTargets []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`Array.from(document.querySelectorAll('button[data-action="$copy"]')).map((b) => b.getAttribute('data-target'))`, &copyTargets)); err != nil {
		t.Fatal(err)
	}
	pubKey, _ := st.Secret("ssh:Microtik")
	for _, target := range copyTargets {
		if !strings.Contains(target, "ssh-ed25519") || !strings.Contains(target, pubKey.Public) || strings.Contains(target, "PRIVATE") {
			t.Errorf("цель копирования не публичная часть: %q", target)
		}
	}
	karkasShot(t, ctx, "s2d-ssh-overview")

	// ---- «Кому выдан»: у gitlab-token модуль просит — выдать и отозвать нажатием ----
	karkasOpen(t, ctx, entityAddr(secretsScreen, "gitlab-token", ""))
	saPoll(t, ctx, "экран gitlab-token нарисован", `!!document.getElementById('sec-entity')`)
	saClickTab(t, ctx, "Кому выдан", entityAddr(secretsScreen, "gitlab-token", secretTabGrants))
	saPoll(t, ctx, "матрица нарисована с невыданной клеткой", `document.querySelectorAll('table.matrix .cell-button.off').length === 1`)
	karkasShot(t, ctx, "s3-secret-grants")
	saClick(t, ctx, `table.matrix .cell-button.off`)
	saPoll(t, ctx, "клетка стала выданной", `document.querySelectorAll('table.matrix .cell-button.on').length === 1`)
	if m, _ := fx.S.eng.Module("gitlab-sync"); !m.HasGrant("gitlab-token") {
		t.Fatal("после нажатия модуль не получил секрет")
	}
	saClick(t, ctx, `table.matrix .cell-button.on`)
	saPoll(t, ctx, "клетка снова пустая", `document.querySelectorAll('table.matrix .cell-button.off').length === 1`)
	if m, _ := fx.S.eng.Module("gitlab-sync"); m.HasGrant("gitlab-token") {
		t.Fatal("после отзыва модуль сохранил секрет")
	}
	if !strings.HasSuffix(saRead(t, ctx).Screen, "~grants") {
		t.Errorf("выдача увела с вкладки «Кому выдан»: %q", saRead(t, ctx).Screen)
	}

	// ---- «Журнал» секрета, потом «‹ Ключи и секреты» ----
	saClickTab(t, ctx, "Журнал", entityAddr(secretsScreen, "gitlab-token", secretsTabLog))
	saPoll(t, ctx, "в журнале есть записи, а чипы — с числами", `document.querySelectorAll('#feed .section:not(#sec-entity) .row-wrap').length >= 3 && document.querySelectorAll('.filters-bar .segment').length === 4`)
	karkasShot(t, ctx, "s4-secret-log")
	if err := chromedp.Run(ctx, chromedp.Click(`#screen .crumbs .crumb`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, secretsScreen)
	saPoll(t, ctx, "список вернулся", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:secrets@"]').length > 0`)

	// ---- вкладки раздела: «Шифрование» и «Журнал» ----
	saClickTab(t, ctx, "Шифрование", entityAddr(secretsScreen, "", secretsTabCrypt))
	saPoll(t, ctx, "«Шифрование» нарисовано", `document.body.textContent.includes('Ключ шифрования') && document.body.textContent.includes('из ')`)
	karkasShot(t, ctx, "s5-crypt")
	saClickTab(t, ctx, "Журнал", entityAddr(secretsScreen, "", secretsTabLog))
	saPoll(t, ctx, "общий журнал нарисован", `document.querySelectorAll('#feed .section .row-wrap').length >= 3`)
	karkasShot(t, ctx, "s6-log")

	// ---- «Добавить секрет»: кнопкой шапки, вид «Токен», имя, значение ----
	karkasOpen(t, ctx, secretsScreen)
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'controls')]/button[contains(.,'Добавить секрет')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать «Добавить секрет»: %v", err)
	}
	waitScreen(t, ctx, entityAddr(secretsScreen, secretsNew, ""))
	saPoll(t, ctx, "форма добавления нарисована", `!!document.querySelector('form input[name="name"]') && !!document.querySelector('form input[type=password][name="value"]')`)
	karkasShot(t, ctx, "s7-new")
	if err := chromedp.Run(ctx,
		chromedp.SetValue(`form select[name="kind"]`, "Токен", chromedp.ByQuery),
		chromedp.SendKeys(`form input[name="name"]`, "nextcloud-token", chromedp.ByQuery),
		chromedp.SendKeys(`form input[name="value"]`, "значение-токена-nextcloud", chromedp.ByQuery),
		chromedp.SendKeys(`form input[name="note"]`, "для резервных копий", chromedp.ByQuery),
		chromedp.Click(`form .form-actions button`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(secretsScreen, "nextcloud-token", ""))
	sec, ok := st.Secret("nextcloud-token")
	if !ok || sec.Kind != store.SecretToken || sec.Note != "для резервных копий" || sec.CreatedBy != store.SecretByHuman {
		t.Fatalf("после добавления в базе: %+v (%v)", sec, ok)
	}
	if v, _ := st.SecretValue("nextcloud-token"); v != "значение-токена-nextcloud" {
		t.Errorf("ядро читает %q", v)
	}
	saPoll(t, ctx, "экран нового секрета нарисован", `!!document.getElementById('sec-entity')`)
}

// «Агенты»: список → агент → «Способности» (тумблер) → «‹ Агенты» → «Пригласить
// агента» (код один раз) → вкладка «Шлюз».
func TestBrowserAgentsWalk(t *testing.T) {
	serversBrowserOnly(t)
	fx, srv := saServer(t)
	st := fx.St
	ctx := serversDesktop(t, srv.URL, agentsScreen)
	waitScreen(t, ctx, agentsScreen)
	saPoll(t, ctx, "список агентов нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:agents@"]').length === 3`)

	// ---- список: тонкие строки, полка, одна кнопка в шапке, вкладки ----
	list := saRead(t, ctx)
	if len(list.AgentRows) != 3 || !list.Shelf || !strings.Contains(list.ShelfText, "сменился отпечаток") {
		t.Fatalf("список: строк %d, полка %v: %+v", len(list.AgentRows), list.Shelf, list)
	}
	for _, row := range list.AgentRows {
		if row.Pills > 2 || row.Buttons > 1 || !row.More {
			t.Errorf("строка не тонкая — капсул %d, кнопок %d, «···» %v: %s", row.Pills, row.Buttons, row.More, row.Text)
		}
	}
	if strings.Join(list.HeaderButtons, "|") != "Пригласить агента" {
		t.Errorf("кнопки шапки раздела: %v", list.HeaderButtons)
	}
	if len(list.Subtabs) != 2 || !strings.HasPrefix(list.Subtabs[0], "Агенты") || !strings.HasPrefix(list.Subtabs[1], "Шлюз") {
		t.Errorf("вкладки раздела: %v", list.Subtabs)
	}
	karkasShot(t, ctx, "a1-list")

	// ---- в агента ----
	saClickRow(t, ctx, entityAddr(agentsScreen, "мак", ""))
	mac := saRead(t, ctx)
	if mac.Crumb != "‹ Агенты" || mac.H1 != "мак" || !mac.BarVisible || mac.BarButtons != 1 || !mac.BarMore ||
		!strings.Contains(mac.BarText, "на связи") || !strings.HasPrefix(mac.SelectedTab, "Обзор") {
		t.Errorf("экран агента: %+v", mac)
	}
	if !mac.Danger || !mac.DangerLast || mac.DangerButtons != 2 {
		t.Errorf("«Опасное» последним, с двумя видимыми кнопками: %+v", mac)
	}
	karkasShot(t, ctx, "a2-agent-overview")

	// «Отозвать…» спрашивает; «Отмена» оставляет доступ
	if err := chromedp.Run(ctx, chromedp.Click(`#sec-agent-danger button[data-confirm]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Отозвать…»: %v", err)
	}
	saPoll(t, ctx, "окно вопроса открыто", `!!document.querySelector('dialog#confirm[open]')`)
	karkasShot(t, ctx, "a2b-confirm-revoke")
	if err := chromedp.Run(ctx, chromedp.Click(`dialog#confirm button.cancel`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	saPoll(t, ctx, "окно вопроса закрыто", `!document.querySelector('dialog#confirm[open]')`)
	if a, _ := st.Agent("мак"); a.State != store.AgentOK {
		t.Fatal("«Отмена» отозвала доступ")
	}

	// ---- «Способности»: «уведомления» выключена, нажатие тумблера включает ровно её ----
	saClickTab(t, ctx, "Способности", entityAddr(agentsScreen, "мак", agentTabCaps))
	saPoll(t, ctx, "три тумблера нарисованы", `document.querySelectorAll('#sec-agent-caps button.switch').length === 3 && document.querySelectorAll('#sec-agent-caps button.switch.on').length === 2`)
	karkasShot(t, ctx, "a3-agent-caps")
	if fx.S.eng.AgentAllowed("мак", "уведомления") {
		t.Fatal("до нажатия «уведомления» уже разрешены")
	}
	// тумблер «уведомления»: у строки способности нет своего адреса в разметке — находим
	// по её названию и нажимаем настоящим жестом
	modelsTag(t, ctx, `Array.from(document.querySelectorAll('#sec-agent-caps .row')).find((r) => r.textContent.includes('уведомления')).querySelector('button.switch')`)
	saClick(t, ctx, `[data-test="picked"]`)
	saPoll(t, ctx, "тумблер включился", `document.querySelectorAll('#sec-agent-caps button.switch.on').length === 3`)
	if !fx.S.eng.AgentAllowed("мак", "уведомления") || !fx.S.eng.AgentAllowed("мак", "код") {
		t.Errorf("после нажатия разрешены не те способности: %+v", func() []string { a, _ := st.Agent("мак"); return a.Allowed }())
	}
	if !strings.HasSuffix(saRead(t, ctx).Screen, "~caps") {
		t.Errorf("тумблер увёл с вкладки «Способности»: %q", saRead(t, ctx).Screen)
	}

	// ---- «Журнал» агента, «‹ Агенты» ----
	saClickTab(t, ctx, "Журнал", entityAddr(agentsScreen, "мак", agentsTabLog))
	saPoll(t, ctx, "журнал нарисован, чипов три", `document.querySelectorAll('#feed .section:not(#sec-entity) .row-wrap').length >= 3 && document.querySelectorAll('.filters-bar .segment').length === 3`)
	karkasShot(t, ctx, "a4-agent-log")
	if err := chromedp.Run(ctx, chromedp.Click(`#screen .crumbs .crumb`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, agentsScreen)
	saPoll(t, ctx, "список вернулся", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:agents@"]').length === 3`)

	// ---- «Пригласить агента»: код один раз ----
	if err := chromedp.Run(ctx, chromedp.Click(`//div[contains(@class,'controls')]/button[contains(.,'Пригласить агента')]`, chromedp.BySearch)); err != nil {
		t.Fatalf("нажать «Пригласить агента»: %v", err)
	}
	waitScreen(t, ctx, entityAddr(agentsScreen, agentsNew, ""))
	saPoll(t, ctx, "форма приглашения нарисована", `!!document.querySelector('form input[name="name"]')`)
	karkasShot(t, ctx, "a5-invite")
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`form input[name="name"]`, "ноут", chromedp.ByQuery),
		chromedp.Click(`form .form-actions button`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	saPoll(t, ctx, "код показан со сроком и «Скопировать»", `!!document.querySelector('#sec-invite-fresh') && document.querySelector('#sec-invite-fresh').textContent.includes('действует до') && !!document.querySelector('#sec-invite-fresh button[data-action="$copy"]')`)
	invites, _ := st.Invites()
	if len(invites) != 1 {
		t.Fatalf("приглашений %d", len(invites))
	}
	var shownCode bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#sec-invite-fresh').textContent.includes('`+invites[0].Code+`')`, &shownCode)); err != nil {
		t.Fatal(err)
	}
	if !shownCode {
		t.Error("при выдаче код на экране не показан")
	}
	karkasShot(t, ctx, "a5b-invite-code")
	// после перезагрузки кода нет, а приглашение ждёт — с «Отменить»
	if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(agentsScreen, agentsNew, ""))
	saPoll(t, ctx, "после перезагрузки кода нет, а приглашение ждёт", `!document.querySelector('#sec-invite-fresh') && document.body.textContent.includes('Ждут агента')`)
	var leaked bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.outerHTML.includes('`+invites[0].Code+`')`, &leaked)); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Error("код приглашения остался в разметке после первого показа")
	}
	saClick(t, ctx, `#sec-invites-waiting button.action`)
	saPoll(t, ctx, "приглашение отменено", `!document.body.textContent.includes('Ждут агента')`)
	if left, _ := st.Invites(); len(left) != 0 {
		t.Errorf("приглашение не отменилось: %+v", left)
	}

	// ---- «Шлюз» ----
	karkasOpen(t, ctx, agentsScreen)
	saClickTab(t, ctx, "Шлюз", entityAddr(agentsScreen, "", agentsTabGateway))
	saPoll(t, ctx, "шлюз нарисован с предупреждениями", `document.body.textContent.includes('Вход из интернета') && document.body.textContent.includes('Правила доступа не готовы')`)
	karkasShot(t, ctx, "a6-gateway")
}

// Телефон 390×844: «Секреты», секрет и «Агенты» — одна кнопка и «···» в строке,
// ничего не едет вбок, полоса сохранения над нижней полосой приложения.
func TestBrowserSecretsAgentsPhone(t *testing.T) {
	serversBrowserOnly(t)
	_, srv := saServer(t)
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)

	karkasOpen(t, ctx, secretsScreen)
	saPoll(t, ctx, "список секретов нарисован", `Array.from(document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:secrets@"]')).filter((r) => r.checkVisibility()).length === 6`)
	list := saRead(t, ctx)
	if list.Crumb != "‹ Настройки" || list.SW > list.CW {
		t.Errorf("список секретов на телефоне: «назад» %q, ширина %d при %d", list.Crumb, list.SW, list.CW)
	}
	for _, row := range list.SecretRows {
		if row.Pills > 2 || row.Buttons > 1 {
			t.Errorf("строка на телефоне не тонкая: %s", row.Text)
		}
	}
	karkasShot(t, ctx, "p1-secrets")

	if err := chromedp.Run(ctx, chromedp.Click(`#feed .row-wrap > .row.clickable[data-goto="`+entityAddr(secretsScreen, "planner-token", "")+`"] a.row-link`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, entityAddr(secretsScreen, "planner-token", ""))
	saPoll(t, ctx, "экран секрета нарисован", `!!document.getElementById('sec-entity')`)
	one := saRead(t, ctx)
	if one.Crumb != "‹ Ключи и секреты" || one.BarButtons > 1 || !one.BarMore || one.SW > one.CW {
		t.Errorf("шапка секрета на телефоне: «назад» %q, кнопок %d, «···» %v, ширина %d при %d", one.Crumb, one.BarButtons, one.BarMore, one.SW, one.CW)
	}
	karkasShot(t, ctx, "p2-secret")
	// набор в поле: полоса над нижней полосой приложения, значение в ней закрыто
	const field = `form.deferred input[type=password][name="value"]`
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('`+field+`').scrollIntoView({block: 'center'}); true`, nil),
		chromedp.Focus(field, chromedp.ByQuery), chromedp.SendKeys(field, "x", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	saPoll(t, ctx, "полоса сохранения над нижней полосой приложения",
		`(() => { const bar = document.getElementById('save-bar'); const tab = document.getElementById('tabbar'); return !!bar && !bar.hidden && bar.getBoundingClientRect().bottom <= tab.getBoundingClientRect().top + 1; })()`)
	karkasShot(t, ctx, "p2b-secret-typed")

	karkasOpen(t, ctx, agentsScreen)
	saPoll(t, ctx, "список агентов нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:agents@"]').length === 3`)
	agents := saRead(t, ctx)
	if agents.Crumb != "‹ Настройки" || agents.SW > agents.CW {
		t.Errorf("список агентов на телефоне: «назад» %q, ширина %d при %d", agents.Crumb, agents.SW, agents.CW)
	}
	for _, row := range agents.AgentRows {
		if row.Pills > 2 || row.Buttons > 1 {
			t.Errorf("строка агента на телефоне не тонкая: %s", row.Text)
		}
	}
	karkasShot(t, ctx, "p3-agents")
	for _, screen := range []string{entityAddr(agentsScreen, "мак", ""), entityAddr(agentsScreen, "мак", agentTabCaps),
		entityAddr(agentsScreen, "", agentsTabGateway), entityAddr(secretsScreen, "gitlab-token", secretTabGrants),
		entityAddr(secretsScreen, "", secretsTabCrypt), entityAddr(agentsScreen, agentsNew, "")} {
		karkasOpen(t, ctx, screen)
		saPoll(t, ctx, "экран нарисован", `!document.querySelector('.htmx-request')`)
		if v := saRead(t, ctx); v.SW > v.CW {
			t.Errorf("%s едет вбок: %d при %d", screen, v.SW, v.CW)
		}
	}
}

// Снимки для ревью (SEMA_SHOTS=<папка>): десктоп 1290×806 и телефон 390×844. Без
// SEMA_SHOTS ничего не снимается, а страницы просто собираются.
func TestBrowserSecretsAgentsShots(t *testing.T) {
	serversBrowserOnly(t)
	_, srv := saServer(t)
	ctx := serversDesktop(t, srv.URL, secretsScreen)
	waitScreen(t, ctx, secretsScreen)
	for _, shot := range []struct{ name, screen string }{
		{"desktop-1-secrets", secretsScreen},
		{"desktop-3-crypt", entityAddr(secretsScreen, "", secretsTabCrypt)},
		{"desktop-4-secret-overview", entityAddr(secretsScreen, "ssh:Microtik", "")},
		{"desktop-5-secret-grants", entityAddr(secretsScreen, "yandex-app-password", secretTabGrants)},
		{"desktop-6-secret-new", entityAddr(secretsScreen, secretsNew, "")},
		{"desktop-7-agents", agentsScreen},
		{"desktop-8-gateway", entityAddr(agentsScreen, "", agentsTabGateway)},
		{"desktop-9-agent-overview", entityAddr(agentsScreen, "мак", "")},
		{"desktop-10-agent-caps", entityAddr(agentsScreen, "мак", agentTabCaps)},
		{"desktop-11-agent-alert", entityAddr(agentsScreen, "рабочая", "")},
		{"desktop-12-secret-service", entityAddr(secretsScreen, "model:OpenAI", "")},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	// служебные раскрыты
	karkasOpen(t, ctx, secretsScreen)
	if err := chromedp.Run(ctx, chromedp.Click(`#fold-secrets-service > summary`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	saPoll(t, ctx, "служебные раскрыты", `document.querySelectorAll('#fold-secrets-service .row-wrap').length >= 20`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('fold-secrets-service').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-2-secrets-service")
	// низ «Обзора» агента: «Опасное» красной карточкой
	karkasOpen(t, ctx, entityAddr(agentsScreen, "мак", ""))
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-9b-agent-overview-bottom")
}

func TestBrowserSecretsAgentsShotsPhone(t *testing.T) {
	serversBrowserOnly(t)
	_, srv := saServer(t)
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)
	for _, shot := range []struct{ name, screen string }{
		{"phone-1-secrets", secretsScreen},
		{"phone-2-secret", entityAddr(secretsScreen, "ssh:Microtik", "")},
		{"phone-3-agents", agentsScreen},
		{"phone-4-agent", entityAddr(agentsScreen, "мак", "")},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
}
