package api

// «Хранилища», «Скиллы», «Инструменты» и «Уведомления → Каналы доставки» в настоящем
// Chrome (2 октября 2026): нажатие по строке, раскрытие «Настроек» на месте, ввод в
// поле, чип и сегмент настоящим жестом, «Сохранить» нижней полосой — а не вызов
// функции. Ждём условий на странице, а не сроков. Chrome без головы; настоящих
// соединений нет (проверку хранилища подменяет `checkStorageVia`), пушей никто не
// посылает, буфер обмена не трогается: «Скопировать» не нажимается — проверяется цель
// копирования.
//
// Снимки для ревью: SEMA_SHOTS=<папка> (десктоп 1290×806 и телефон 390×844).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// stnState — что видно на экранах: строки с переходом, шапка сущности, «Опасное», полоса.
const stnState = `(() => {
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
    storageRows: q('#feed .row-wrap > .row.clickable[data-goto^="settings:storages@"]').filter(visible).map(info),
    skillRows: q('#feed .row-wrap > .row.clickable[data-goto^="settings:skills@"]').filter(visible).map(info),
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

type stnView struct {
	Screen        string       `json:"screen"`
	H1            string       `json:"h1"`
	Crumb         string       `json:"crumb"`
	Subtabs       []string     `json:"subtabs"`
	SelectedTab   string       `json:"selectedTab"`
	StorageRows   []serversRow `json:"storageRows"`
	SkillRows     []serversRow `json:"skillRows"`
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

func stnRead(t *testing.T, ctx context.Context) stnView {
	t.Helper()
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(stnState, &raw)); err != nil {
		t.Fatal(err)
	}
	var v stnView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("разбор состояния: %v: %s", err, raw)
	}
	return v
}

// stnPoll — ждать условия на странице; не дождались — показать, что на ней.
func stnPoll(t *testing.T, ctx context.Context, what, cond string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Poll(cond, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ok {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(stnState, &now))
		t.Fatalf("не дождались: %s (%v); состояние: %s", what, err, now)
	}
}

// stnClick — подвести элемент к середине окна и нажать: закреплённая строка сущности
// перекрывает элемент у верхнего края, и нажатие уходило бы в неё. Перерисовка экрана
// между «найти» и «нажать» бывает — нажатие повторяется, пока узел не устоит.
func stnClick(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	var err error
	for try := 0; try < 6; try++ {
		err = chromedp.Run(ctx,
			chromedp.Evaluate(`(() => { const el = document.querySelector(`+jsString(sel)+`); if (el) el.scrollIntoView({block: 'center'}); return !!el; })()`, nil),
			chromedp.Click(sel, chromedp.ByQuery))
		if err == nil {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("нажать %s: %v", sel, err)
}

// stnClickText — нажать элемент, текст которого начинается с такого: чип, сегмент,
// вкладка (у вкладки за названием стоит счётчик).
func stnClickText(t *testing.T, ctx context.Context, css, text string) {
	t.Helper()
	expr := `(() => { const el = Array.from(document.querySelectorAll(` + jsString(css) + `)).find((e) => e.textContent.replace(/\s+/g, ' ').trim().startsWith(` + jsString(text) + `));
	  if (!el) return false; el.setAttribute('data-test', 'picked'); el.scrollIntoView({block: 'center'}); return true; })()`
	var found bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &found)); err != nil || !found {
		t.Fatalf("нет элемента %s с текстом %q (%v)", css, text, err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`[data-test="picked"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('[data-test="picked"]').forEach((e) => e.removeAttribute('data-test')); true`, nil)); err != nil {
		t.Fatalf("нажать %s %q: %v", css, text, err)
	}
}

func stnSave(t *testing.T, ctx context.Context) {
	t.Helper()
	stnClick(t, ctx, `#save-bar [data-save="save"]`)
	stnPoll(t, ctx, "полоса погасла после сохранения", `(() => { const bar = document.getElementById('save-bar'); return !!bar && bar.hidden && !document.querySelector('.htmx-request'); })()`)
}

// stnOpenRow — открыть строку списка кликом по её имени.
func stnOpenRow(t *testing.T, ctx context.Context, screen string) {
	t.Helper()
	stnClick(t, ctx, `#feed .row-wrap > .row.clickable[data-goto="`+screen+`"] a.row-link`)
	waitScreen(t, ctx, screen)
	stnPoll(t, ctx, "экран нарисован", `!!document.getElementById('sec-entity')`)
}

func stnServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, _ := stnFix(t)
	// проверка хранилища — подмена: настоящая ходит в сеть
	s.checkStorageVia = func(name string) (string, error) {
		return "bucket есть, запись и чтение работают", nil
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

// Снимки для ревью: десктоп и телефон.
func TestBrowserStnShots(t *testing.T) {
	serversBrowserOnly(t)
	s, srv := stnServer(t)
	ctx := serversDesktop(t, srv.URL, storagesScreen)
	waitScreen(t, ctx, storagesScreen)
	for _, shot := range []struct{ name, screen string }{
		{"desktop-1a-storages-places", storagesScreen},
		{"desktop-1b-storages-sources", storagesScreen + "~" + storTabSources},
		{"desktop-2-storage-s3", storageAddr("yandex")},
		{"desktop-2b-storage-local", storageAddr("local")},
		{"desktop-2c-storage-new", entityAddr(storagesScreen, storNew, "")},
		{"desktop-3-skills", skillsScreen},
		{"desktop-4-skill", engine.SkillScreen("backups")},
		{"desktop-4b-skill-builtin", engine.SkillScreen("git-commits")},
		{"desktop-5-tools", toolsScreen},
		{"desktop-6a-notify-collapsed", notifySettingsScreen},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	st := s.eng.Store()
	// прежний адрес вкладки «Коды на картинках» открывает раздел развёрнутым
	karkasOpen(t, ctx, toolsScreen+"~"+toolsTabCodes)
	stnPoll(t, ctx, "раздел про коды развёрнут", `!!document.querySelector('#fold-codes[open]')`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('fold-codes').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-5b-tools-codes-open")
	// низ экрана хранилища: параметры полями, ключи ссылкой, «Опасное»
	karkasOpen(t, ctx, storageAddr("yandex"))
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTo(0, document.getElementById('feed').scrollHeight); window.scrollTo(0, document.body.scrollHeight); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-2d-storage-s3-lower")
	// раскрытые «Настройки»: сначала канал, потом устройство
	karkasOpen(t, ctx, notifySettingsScreen)
	web := stnWebChannel(t, st)
	phone, _ := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1")
	for _, step := range []struct{ id, shot string }{
		{"channel-" + strconv.FormatInt(web.ID, 10), "desktop-6b-notify-channel-expanded"},
		{"push-" + strconv.FormatInt(phone.ID, 10), "desktop-6c-notify-device-expanded"},
	} {
		stnClick(t, ctx, `#exp-`+step.id+` > summary`)
		stnPoll(t, ctx, "настройки раскрыты", `!!document.querySelector('#exp-`+step.id+`[open] form.rows')`)
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('exp-`+step.id+`').scrollIntoView({block: 'center'}); true`, nil)); err != nil {
			t.Fatal(err)
		}
		karkasShot(t, ctx, step.shot)
	}
	karkasOpen(t, ctx, storagesScreen+"~"+storTabSources)
	folders, _ := st.FileFolders()
	for _, f := range folders {
		if f.Path == "/data/scans" {
			stnClick(t, ctx, `#exp-folder-`+strconv.FormatInt(f.ID, 10)+` > summary`)
		}
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#folders-list, #sec-folders-list').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "desktop-1c-folder-expanded")
}

// Снимки для ревью, телефон 390×844: «Уведомления» с раскрытым каналом, «Скиллы», экран
// хранилища, «Хранилища».
func TestBrowserStnShotsPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, srv := stnServer(t)
	st := s.eng.Store()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)
	for _, shot := range []struct{ name, screen string }{
		{"phone-1-storages", storagesScreen},
		{"phone-2-storage", storageAddr("yandex")},
		{"phone-3-skills", skillsScreen},
		{"phone-4-skill", engine.SkillScreen("backups")},
		{"phone-5-tools", toolsScreen},
	} {
		karkasOpen(t, ctx, shot.screen)
		karkasShot(t, ctx, shot.name)
	}
	karkasOpen(t, ctx, notifySettingsScreen)
	web := stnWebChannel(t, st)
	id := "channel-" + strconv.FormatInt(web.ID, 10)
	stnClick(t, ctx, `#exp-`+id+` > summary`)
	stnPoll(t, ctx, "настройки канала раскрыты", `!!document.querySelector('#exp-`+id+`[open] form.rows')`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('exp-`+id+`').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "phone-6-notify-channel-expanded")
	phone, _ := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1")
	dev := "push-" + strconv.FormatInt(phone.ID, 10)
	stnClick(t, ctx, `#exp-`+dev+` > summary`)
	stnPoll(t, ctx, "настройки устройства раскрыты", `!!document.querySelector('#exp-`+dev+`[open] form.rows')`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('exp-`+dev+`').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "phone-7-notify-device-expanded")
}

// stnType — набрать текст в поле как руками: выделить прежнее и напечатать новое.
func stnType(t *testing.T, ctx context.Context, sel, text string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const el = document.querySelector(`+jsString(sel)+`); if (!el) return false; el.scrollIntoView({block: 'center'}); el.focus(); if (el.select) el.select(); return true; })()`, nil),
		chromedp.SendKeys(sel, text, chromedp.ByQuery)); err != nil {
		t.Fatalf("набрать %q в %s: %v", text, sel, err)
	}
}

// stnBarText — что говорит нижняя полоса.
func stnBarText(t *testing.T, ctx context.Context) string {
	t.Helper()
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const bar = document.getElementById('save-bar'); return bar && !bar.hidden ? bar.textContent.replace(/\s+/g, ' ').trim() : ''; })()`, &text)); err != nil {
		t.Fatal(err)
	}
	return text
}

// «Хранилища»: список → экран хранилища → параметры полем и «Сохранить» → отказ при
// переносе зеркала с файлами → «Проверить» → «‹ Хранилища» → «Откуда берутся» → «Настройки»
// папки → «Добавить хранилище».
func TestBrowserStoragesWalk(t *testing.T) {
	serversBrowserOnly(t)
	s, srv := stnServer(t)
	st := s.eng.Store()
	ctx := serversDesktop(t, srv.URL, storagesScreen)
	waitScreen(t, ctx, storagesScreen)
	stnPoll(t, ctx, "список хранилищ нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:storages@"]').length === 4`)

	// ---- список: тонкие строки, одна кнопка в шапке, две вкладки ----
	list := stnRead(t, ctx)
	for _, row := range list.StorageRows {
		if row.Pills > 2 || row.Buttons > 1 {
			t.Errorf("строка не тонкая — капсул %d, кнопок %d: %s", row.Pills, row.Buttons, row.Text)
		}
		if strings.Contains(row.Text, "no such host") {
			t.Errorf("ошибка проверки в строке списка: %s", row.Text)
		}
	}
	if strings.Join(list.HeaderButtons, "|") != "Добавить хранилище" {
		t.Errorf("кнопки шапки раздела: %v", list.HeaderButtons)
	}
	if len(list.Subtabs) != 2 || !strings.HasPrefix(list.Subtabs[0], "Где лежат") || !strings.HasPrefix(list.Subtabs[1], "Откуда берутся") {
		t.Errorf("вкладки раздела: %v", list.Subtabs)
	}
	if list.SW > list.CW {
		t.Errorf("список едет вбок: %d при %d", list.SW, list.CW)
	}

	// ---- в хранилище кликом по строке ----
	stnOpenRow(t, ctx, storageAddr("yandex"))
	one := stnRead(t, ctx)
	if one.Crumb != "‹ Хранилища" || one.H1 != "yandex" || !one.BarVisible || one.BarButtons != 1 {
		t.Errorf("экран хранилища: %+v", one)
	}
	if !one.Danger || !one.DangerLast || one.DangerButtons != 1 {
		t.Errorf("«Опасное» должно стоять последним и с видимой кнопкой: %+v", one)
	}
	var body string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').textContent`, &body)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "no such host") || strings.Contains(body, stnAccessSentinel) || strings.Contains(body, stnSecretSentinel) {
		t.Errorf("на экране хранилища ошибка проверки должна быть, а значений ключей — нет")
	}

	// ---- регион и HTTPS: набор поля и тумблер, одна правка в полосе, сохраняется ----
	const region = `form.deferred input[name="region"]`
	stnType(t, ctx, region, "eu-central-1")
	stnPoll(t, ctx, "полоса с одной правкой региона",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1') && bar.textContent.includes('«Регион» — ru-central1 → eu-central-1'); })()`)
	karkasShot(t, ctx, "w2-storage-edited")
	stnSave(t, ctx)
	c, _ := s.storageByName("yandex")
	if !strings.Contains(c.Config, `"region":"eu-central-1"`) {
		t.Fatalf("в базе после «Сохранить»: %s", c.Config)
	}
	if note := st.StorageNote("yandex"); note != "" {
		t.Errorf("прежняя проверка не сброшена: %q", note)
	}
	// «Bucket»: файлы уже в зеркале — отказ у строки, полоса остаётся
	stnPoll(t, ctx, "экран перечитан", `!!document.querySelector('form.deferred input[name="bucket"]') && !document.querySelector('.htmx-request')`)
	stnType(t, ctx, `form.deferred input[name="bucket"]`, "другой-bucket")
	stnClick(t, ctx, `#save-bar [data-save="save"]`)
	stnPoll(t, ctx, "отказ показан у формы словами",
		`(() => { const place = document.querySelector('form.deferred .not-saved:not([hidden])'); return !!place && place.textContent.includes('уже лежит'); })()`)
	karkasShot(t, ctx, "w2b-storage-refused")
	if c, _ := s.storageByName("yandex"); !strings.Contains(c.Config, `"bucket":"sema-files"`) {
		t.Errorf("отказанная правка записалась: %s", c.Config)
	}
	// «Отменить» возвращает поле
	stnClick(t, ctx, `#save-bar [data-save="revert"]`)
	stnPoll(t, ctx, "полоса погасла, поле вернулось",
		`(() => { const bar = document.getElementById('save-bar'); const f = document.querySelector('form.deferred input[name="bucket"]'); return !!bar && bar.hidden && f.value === 'sema-files'; })()`)

	// ---- «Проверить» в шапке: итог на этом же экране ----
	stnClick(t, ctx, `#sec-entity .right-side > button.action`)
	stnPoll(t, ctx, "итог проверки на экране хранилища",
		`document.getElementById('feed').textContent.includes('запись и чтение работают')`)
	karkasShot(t, ctx, "w2c-storage-checked")

	// ---- «‹ Хранилища» ведёт на список ----
	stnClick(t, ctx, `#screen .crumbs .crumb`)
	waitScreen(t, ctx, storagesScreen)
	stnPoll(t, ctx, "список вернулся", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:storages@"]').length === 4`)

	// ---- «Откуда берутся»: «Настройки» папки раскрываются на месте ----
	stnClickText(t, ctx, `#screen .subtabs-bar .segment`, "Откуда берутся")
	waitScreen(t, ctx, storagesScreen+"~"+storTabSources)
	folders, _ := st.FileFolders()
	var scans int64
	for _, f := range folders {
		if f.Path == "/data/scans" {
			scans = f.ID
		}
	}
	exp := `#exp-folder-` + strconv.FormatInt(scans, 10)
	stnPoll(t, ctx, "строка папки нарисована", `!!document.querySelector('`+exp+` > summary')`)
	stnClick(t, ctx, exp+` > summary`)
	stnPoll(t, ctx, "«Настройки» раскрыты и видны", `(() => { const d = document.querySelector('`+exp+`'); const f = d && d.querySelector('form.deferred input[name="delete_after"]'); return !!d && d.open && !!f; })()`)
	karkasShot(t, ctx, "w2d-folder-expanded")
	// тумблер «Удалять файлы после приёма» — настоящим нажатием по его ползунку
	stnClick(t, ctx, exp+` form.deferred .field-toggle`)
	stnPoll(t, ctx, "полоса с одной правкой «удалять после приёма»",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1') && bar.textContent.includes('Удалять файлы после приёма'); })()`)
	stnSave(t, ctx)
	for _, f := range mustFolders(t, st) {
		if f.ID == scans && f.DeleteAfter {
			t.Error("«удалять после приёма» не выключилось после «Сохранить»")
		}
	}
	// после сохранения раскрытие осталось
	stnPoll(t, ctx, "раскрытие пережило перерисовку", `(() => { const d = document.querySelector('`+exp+`'); return !!d && d.open; })()`)

	// ---- «Добавить хранилище»: Яндекс.Диск вкладкой, токен из списка, после — экран нового ----
	stnClickText(t, ctx, `#screen .header .controls > button.action`, "Добавить хранилище")
	waitScreen(t, ctx, entityAddr(storagesScreen, storNew, ""))
	stnClickText(t, ctx, `#screen .subtabs-bar .segment`, "Яндекс.Диск")
	waitScreen(t, ctx, entityAddr(storagesScreen, storNew, storNewDisk))
	stnPoll(t, ctx, "форма Диска нарисована", `!!document.querySelector('form input[name="folder"]') && !!document.querySelector('form select[name="token_secret"]')`)
	karkasShot(t, ctx, "w2e-storage-new-disk")
	stnType(t, ctx, `form input[name="name"]`, "диск-два")
	if err := chromedp.Run(ctx, chromedp.SetValue(`form select[name="token_secret"]`, "disk-token", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	stnClick(t, ctx, `form .form-actions button`)
	waitScreen(t, ctx, storageAddr("диск-два"))
	stnPoll(t, ctx, "экран нового хранилища нарисован и итог проверки на нём", `!!document.getElementById('sec-entity') && document.getElementById('feed').textContent.includes('запись и чтение работают')`)
	if c, ok := s.storageByName("диск-два"); !ok || c.Kind != "yadisk" || !strings.Contains(c.Config, `"token_secret":"disk-token"`) {
		t.Errorf("заведённое хранилище: %+v %v", c, ok)
	}
}

// «Скиллы»: список → скилл → правка поля и «Сохранить» → «Выключить» → «Скопировать текст»
// (цель — поле) → «‹ Скиллы» → «Добавить скилл» → удаление с вопросом.
func TestBrowserSkillsWalk(t *testing.T) {
	serversBrowserOnly(t)
	s, srv := stnServer(t)
	st := s.eng.Store()
	ctx := serversDesktop(t, srv.URL, skillsScreen)
	waitScreen(t, ctx, skillsScreen)
	stnPoll(t, ctx, "список скиллов нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:skills@"]').length === 7`)

	// ---- список: тонкие строки, текста скиллов в разметке нет ----
	list := stnRead(t, ctx)
	for _, row := range list.SkillRows {
		if row.Pills > 2 || row.Buttons > 0 {
			t.Errorf("строка не тонкая — капсул %d, кнопок %d: %s", row.Pills, row.Buttons, row.Text)
		}
	}
	if strings.Join(list.HeaderButtons, "|") != "Добавить скилл" || list.SW > list.CW {
		t.Errorf("шапка раздела: %v, ширина %d при %d", list.HeaderButtons, list.SW, list.CW)
	}
	var hasPhrase bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.outerHTML.includes(`+jsString(stnSkillPhrase)+`)`, &hasPhrase)); err != nil {
		t.Fatal(err)
	}
	if hasPhrase {
		t.Error("текст скилла лежит в страничке со списком")
	}
	karkasShot(t, ctx, "w3-skills-list")

	// ---- в скилл кликом по строке ----
	stnOpenRow(t, ctx, engine.SkillScreen("backups"))
	one := stnRead(t, ctx)
	if one.Crumb != "‹ Скиллы" || one.H1 != "Резервные копии" || !one.BarVisible || one.BarButtons != 1 || !one.BarMore {
		t.Errorf("экран скилла: %+v", one)
	}
	if !one.Danger || !one.DangerLast {
		t.Errorf("«Опасное» должно стоять последним: %+v", one)
	}
	// «Скопировать текст» — по полю: у кнопки есть цель-имя, текста в ней нет; буфер не трогаем
	var copyTarget string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const b = document.querySelector('#sec-entity button[data-action="$copy_field"]'); return b ? b.getAttribute('data-target') : ''; })()`, &copyTarget)); err != nil {
		t.Fatal(err)
	}
	if copyTarget != "text" {
		t.Errorf("цель копирования: %q, а должно быть имя поля «text»", copyTarget)
	}
	var fieldText string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('form.deferred textarea[name="text"]').value`, &fieldText)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fieldText, stnSkillPhrase) {
		t.Errorf("в поле «Текст» нет текста скилла: %q", fieldText)
	}
	karkasShot(t, ctx, "w4-skill")

	// ---- правка «Когда применять» полем, «Сохранить» полосой ----
	stnType(t, ctx, `form.deferred input[name="when"]`, "когда просят проверить копии — по-новому")
	stnPoll(t, ctx, "полоса с одной правкой",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1') && bar.textContent.includes('«Когда применять»'); })()`)
	stnSave(t, ctx)
	if k, _ := st.Skill("backups"); k.When != "когда просят проверить копии — по-новому" || k.EditedBy != "человек" {
		t.Fatalf("в базе после «Сохранить»: %+v", k)
	}
	if k, _ := st.Skill("backups"); !strings.Contains(k.Text, stnSkillPhrase) {
		t.Errorf("сохранение поля «Когда применять» задело текст: %q", k.Text)
	}

	// ---- «Выключить» в шапке: остаёмся на экране скилла ----
	stnClick(t, ctx, `#sec-entity .right-side > button.action`)
	stnPoll(t, ctx, "скилл выключен, экран тот же", `document.querySelector('#sec-entity .right-side > button.action').textContent.trim() === 'Включить'`)
	if k, _ := st.Skill("backups"); k.Enabled {
		t.Error("скилл остался включённым")
	}
	if got := stnRead(t, ctx).Screen; got != engine.SkillScreen("backups") {
		t.Errorf("«Выключить» увело с экрана скилла на %q", got)
	}
	// ---- «‹ Скиллы», и тумблер строки включает обратно ----
	stnClick(t, ctx, `#screen .crumbs .crumb`)
	waitScreen(t, ctx, skillsScreen)
	stnPoll(t, ctx, "список вернулся", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:skills@"]').length === 7`)
	stnClick(t, ctx, `#feed .row-wrap:has(.row[data-goto="`+engine.SkillScreen("backups")+`"]) button.switch`)
	stnPoll(t, ctx, "скилл включился", `!!document.querySelector('#feed .row-wrap:has(.row[data-goto="`+engine.SkillScreen("backups")+`"]) button.switch.on')`)
	if k, _ := st.Skill("backups"); !k.Enabled {
		t.Error("тумблер строки не включил скилл")
	}
	if got := stnRead(t, ctx).Screen; got != skillsScreen {
		t.Errorf("тумблер строки увёл с экрана: %q", got)
	}

	// ---- «Добавить скилл» ----
	stnClickText(t, ctx, `#screen .header .controls > button.action`, "Добавить скилл")
	waitScreen(t, ctx, entityAddr(skillsScreen, skillsNew, ""))
	stnPoll(t, ctx, "форма добавления нарисована", `!!document.querySelector('form textarea[name="text"]')`)
	stnType(t, ctx, `form input[name="name"]`, "browser-skill")
	stnType(t, ctx, `form input[name="title"]`, "Из браузера")
	stnType(t, ctx, `form textarea[name="text"]`, "Делай так, как просят.")
	karkasShot(t, ctx, "w4b-skill-new")
	stnClick(t, ctx, `form .form-actions button`)
	waitScreen(t, ctx, engine.SkillScreen("browser-skill"))
	if k, ok := st.Skill("browser-skill"); !ok || k.Title != "Из браузера" || k.Source != "own" {
		t.Errorf("добавленный скилл: %+v %v", k, ok)
	}

	// ---- «Удалить…» спрашивает; «Отмена» оставляет, «Удалить» удаляет и ведёт на список ----
	stnPoll(t, ctx, "экран нового скилла нарисован", `!!document.getElementById('sec-entity')`)
	stnClick(t, ctx, `#sec-skill-danger button[data-confirm]`)
	stnPoll(t, ctx, "окно вопроса открыто", `!!document.querySelector('dialog#confirm[open]')`)
	stnClick(t, ctx, `dialog#confirm button.cancel`)
	stnPoll(t, ctx, "окно вопроса закрыто", `!document.querySelector('dialog#confirm[open]')`)
	if _, ok := st.Skill("browser-skill"); !ok {
		t.Fatal("«Отмена» удалила скилл")
	}
	stnClick(t, ctx, `#sec-skill-danger button[data-confirm]`)
	stnPoll(t, ctx, "окно вопроса открыто снова", `!!document.querySelector('dialog#confirm[open]')`)
	stnClick(t, ctx, `dialog#confirm button.ok, dialog#confirm button.danger, dialog#confirm button[type="submit"]`)
	waitScreen(t, ctx, skillsScreen)
	if _, ok := st.Skill("browser-skill"); ok {
		t.Error("скилл остался после «Удалить»")
	}
}

// «Уведомления → Каналы доставки»: «Настройки» канала раскрываются на месте; порог —
// сегмент, источники — чип, тихие часы — поля времени; «Сохранить» полосой; движок
// видит новое. Устройство: имя полем и режим тихих часов сегментом. «Проверить»
// не нажимается: оно разослало бы пробный пуш на подписанные устройства.
func TestBrowserNotifyChannelsWalk(t *testing.T) {
	serversBrowserOnly(t)
	s, srv := stnServer(t)
	st := s.eng.Store()
	web := stnWebChannel(t, st)
	chID := strconv.FormatInt(web.ID, 10)
	ctx := serversDesktop(t, srv.URL, notifySettingsScreen)
	waitScreen(t, ctx, notifySettingsScreen)
	stnPoll(t, ctx, "каналы нарисованы", `!!document.getElementById('exp-channel-`+chID+`')`)

	// ---- шапка вкладки: одна кнопка; строки каналов тонкие ----
	var head struct {
		Buttons []string `json:"buttons"`
		Rows    []struct {
			Pills   int  `json:"pills"`
			Buttons int  `json:"buttons"`
			More    bool `json:"more"`
		} `json:"rows"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => ({
	  buttons: Array.from(document.querySelectorAll('#screen .header .controls > button.action')).map((b) => b.textContent.trim()),
	  rows: Array.from(document.querySelectorAll('#channels-list .row-wrap > .row, #sec-channels-list .row-wrap > .row')).map((r) => ({
	    pills: r.querySelectorAll('.right-side .pill').length,
	    buttons: Array.from(r.querySelectorAll('.right-side > button.action')).length,
	    more: !!r.querySelector('.row-menu') })) }))()`, &head)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(head.Buttons, "|") != "Добавить канал" || len(head.Rows) != 2 {
		t.Errorf("шапка и строки каналов: %+v", head)
	}
	for _, r := range head.Rows {
		if r.Pills > 2 || r.Buttons != 1 || !r.More {
			t.Errorf("строка канала не тонкая: %+v", r)
		}
	}

	// ---- раскрыть «Настройки» канала нажатием, поля на месте ----
	exp := `#exp-channel-` + chID
	stnClick(t, ctx, exp+` > summary`)
	stnPoll(t, ctx, "форма канала раскрыта",
		`(() => { const d = document.querySelector('`+exp+`'); return !!d && d.open && d.querySelectorAll('.field.setting').length === 4 && !!d.querySelector('input[name="quiet_from"]'); })()`)
	karkasShot(t, ctx, "w6-channel-expanded")

	// ---- порог сегментом, источник чипом, тихие часы полями времени ----
	stnClickText(t, ctx, exp+` .field-segments .segment`, "тревога")
	stnClickText(t, ctx, exp+` .field-chips .chip-choice`, "Запросы")
	const qFrom, qTo = `input[name="quiet_from"]`, `input[name="quiet_to"]`
	for _, f := range []struct{ sel, keys string }{{exp + ` ` + qFrom, "2200"}, {exp + ` ` + qTo, "0630"}} {
		if err := chromedp.Run(ctx, chromedp.Focus(f.sel, chromedp.ByQuery), chromedp.SendKeys(f.sel, f.keys, chromedp.ByQuery)); err != nil {
			t.Fatalf("набрать %s в %s: %v", f.keys, f.sel, err)
		}
	}
	stnPoll(t, ctx, "полоса: три правки канала, со словами",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 3'); })()`)
	karkasShot(t, ctx, "w6b-channel-edited")
	stnSave(t, ctx)
	c, _ := st.NotifyChannel(web.ID)
	if c.MinLevel != "alert" || c.Sources != "servers,mail,requests" || c.QuietFrom != "22:00" || c.QuietTo != "06:30" {
		t.Fatalf("канал после «Сохранить»: порог %q, источники %q, тихие часы %q–%q", c.MinLevel, c.Sources, c.QuietFrom, c.QuietTo)
	}
	// движок видит новое: запись «внимание» ниже порога, запрос теперь в списке источников
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local)
	if why := engine.ChannelTakes(c, &store.Notification{Key: "server:A", Source: "core", Level: "warn"}, now); why != "порог канала выше уровня записи" {
		t.Errorf("доставка при новом пороге: %q", why)
	}
	if why := engine.ChannelTakes(c, &store.Notification{Key: "ssh:1", Source: "core", Level: "alert"}, now); why != "" {
		t.Errorf("запрос уровня «тревога» вне тихих часов: %q", why)
	}
	if why := engine.ChannelTakes(c, &store.Notification{Key: "ssh:1", Source: "core", Level: "alert"}, time.Date(2026, 10, 2, 23, 0, 0, 0, time.Local)); !strings.Contains(why, "тихие часы") {
		t.Errorf("запись в тихие часы: %q", why)
	}
	// раскрытие пережило перерисовку, значения на экране — записанные
	stnPoll(t, ctx, "раскрытие осталось, значения — записанные",
		`(() => { const d = document.querySelector('`+exp+`'); return !!d && d.open && d.querySelector('input[name="quiet_from"]').value === '22:00' && d.querySelector('input[name="level"]:checked').value === 'тревога'; })()`)

	// ---- устройство: имя полем и режим тихих часов сегментом ----
	phone, _ := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1")
	devExp := `#exp-push-` + strconv.FormatInt(phone.ID, 10)
	stnClick(t, ctx, devExp+` > summary`)
	stnPoll(t, ctx, "форма устройства раскрыта", `(() => { const d = document.querySelector('`+devExp+`'); return !!d && d.open && !!d.querySelector('input[name="title"]'); })()`)
	stnType(t, ctx, devExp+` input[name="title"]`, "Телефон Вадима")
	stnClickText(t, ctx, devExp+` .field-segments .segment`, "без тихих часов")
	stnPoll(t, ctx, "полоса: две правки устройства",
		`(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 2'); })()`)
	karkasShot(t, ctx, "w6c-device-edited")
	stnSave(t, ctx)
	d, _ := st.PushSubscriptionByID(phone.ID)
	if d.Title != "Телефон Вадима" || d.Quiet != "-" || d.MinLevel != "info" {
		t.Errorf("устройство после «Сохранить»: %+v", d)
	}
	stnPoll(t, ctx, "новое имя в строке", `document.getElementById('feed').textContent.includes('Телефон Вадима')`)

	// ---- «Добавить канал» кнопкой шапки; «‹ Уведомления» назад ----
	stnClickText(t, ctx, `#screen .header .controls > button.action`, "Добавить канал")
	waitScreen(t, ctx, entityAddr(notifySettingsScreen, notifyNew, ""))
	stnPoll(t, ctx, "форма добавления нарисована", `!!document.querySelector('form select[name="kind"]') && !!document.querySelector('form input[name="title"]')`)
	karkasShot(t, ctx, "w6d-channel-new")
	stnClick(t, ctx, `#screen .crumbs .crumb`)
	waitScreen(t, ctx, notifySettingsScreen)
}

// Телефон 390×844: «Уведомления» с раскрытым каналом и устройством, «Скиллы», экран
// хранилища и «Инструменты» — ничего не едет вбок, элементы управления на всю ширину.
func TestBrowserStnPhone(t *testing.T) {
	serversBrowserOnly(t)
	s, srv := stnServer(t)
	st := s.eng.Store()
	ctx := phoneChrome(t, srv.URL, 5*time.Minute)

	// «Уведомления»: канал и устройство раскрыты
	karkasOpen(t, ctx, notifySettingsScreen)
	web := stnWebChannel(t, st)
	phone, _ := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1")
	stnPoll(t, ctx, "каналы нарисованы", `!!document.getElementById('exp-channel-`+strconv.FormatInt(web.ID, 10)+`')`)
	karkasShot(t, ctx, "p1a-notify")
	for _, id := range []string{"channel-" + strconv.FormatInt(web.ID, 10), "push-" + strconv.FormatInt(phone.ID, 10)} {
		stnClick(t, ctx, `#exp-`+id+` > summary`)
	}
	stnPoll(t, ctx, "настройки раскрыты", `document.querySelectorAll('details.expansion-wrap[open] form.rows').length === 2`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#exp-channel-`+strconv.FormatInt(web.ID, 10)+`').scrollIntoView({block: 'start'}); true`, nil)); err != nil {
		t.Fatal(err)
	}
	karkasShot(t, ctx, "p1-notify-expanded")
	// чип «Запросы» — настоящее касание; правка попадает в полосу
	stnClickText(t, ctx, `#exp-channel-`+strconv.FormatInt(web.ID, 10)+` .field-chips .chip-choice`, "Запросы")
	stnPoll(t, ctx, "полоса с правкой на телефоне", `(() => { const bar = document.getElementById('save-bar'); return !!bar && !bar.hidden && bar.textContent.includes('Изменено 1'); })()`)
	if v := stnRead(t, ctx); v.SW > v.CW {
		t.Errorf("«Уведомления» на телефоне едут вбок: %d при %d", v.SW, v.CW)
	}
	var overflow []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const w = document.documentElement.clientWidth; const out = [];
	  for (const e of document.querySelectorAll('#feed details[open] .field.setting *')) { const r = e.getBoundingClientRect();
	    if (r.width > 0 && (r.right > w + 0.5 || r.left < -0.5)) out.push(e.tagName + '.' + e.className + ':' + Math.round(r.right)); }
	  return out.slice(0, 8); })()`, &overflow)); err != nil {
		t.Fatal(err)
	}
	if len(overflow) > 0 {
		t.Errorf("элементы настроек выходят за ширину экрана: %v", overflow)
	}

	// «Скиллы» и экран скилла
	karkasOpen(t, ctx, skillsScreen)
	stnPoll(t, ctx, "список скиллов нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:skills@"]').length === 7`)
	if v := stnRead(t, ctx); v.SW > v.CW {
		t.Errorf("«Скиллы» на телефоне едут вбок: %d при %d", v.SW, v.CW)
	}
	karkasShot(t, ctx, "p2-skills")
	stnClick(t, ctx, `#feed .row-wrap > .row.clickable[data-goto="`+engine.SkillScreen("backups")+`"] a.row-link`)
	waitScreen(t, ctx, engine.SkillScreen("backups"))
	stnPoll(t, ctx, "экран скилла нарисован", `!!document.getElementById('sec-entity')`)
	if v := stnRead(t, ctx); v.SW > v.CW {
		t.Errorf("экран скилла на телефоне едет вбок: %d при %d", v.SW, v.CW)
	}
	karkasShot(t, ctx, "p2b-skill")

	// экран хранилища
	karkasOpen(t, ctx, storageAddr("yandex"))
	stnPoll(t, ctx, "экран хранилища нарисован", `!!document.getElementById('sec-entity')`)
	v := stnRead(t, ctx)
	if v.Crumb != "‹ Хранилища" || v.BarButtons != 1 || v.SW > v.CW {
		t.Errorf("экран хранилища на телефоне: «назад» %q, кнопок %d, ширина %d при %d", v.Crumb, v.BarButtons, v.SW, v.CW)
	}
	karkasShot(t, ctx, "p3-storage")
	karkasOpen(t, ctx, storagesScreen)
	stnPoll(t, ctx, "список хранилищ нарисован", `document.querySelectorAll('#feed .row-wrap > .row.clickable[data-goto^="settings:storages@"]').length === 4`)
	if v := stnRead(t, ctx); v.SW > v.CW {
		t.Errorf("«Хранилища» на телефоне едут вбок: %d при %d", v.SW, v.CW)
	}
	karkasShot(t, ctx, "p3b-storages")
	karkasOpen(t, ctx, toolsScreen)
	stnPoll(t, ctx, "инструменты нарисованы", `!!document.getElementById('sec-tools-list')`)
	if v := stnRead(t, ctx); v.SW > v.CW {
		t.Errorf("«Инструменты» на телефоне едут вбок: %d при %d", v.SW, v.CW)
	}
	karkasShot(t, ctx, "p4-tools")
}
