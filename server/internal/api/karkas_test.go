package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// settingsScreens — корень, Обзор своим адресом и все разделы настроек.
func settingsScreens() []string {
	out := []string{settingsRoot, settingsOverview}
	for _, group := range settingsGroups {
		out = append(out, group.ID)
		out = append(out, group.sections()...)
	}
	return out
}

// Заголовки групп и их порядок названы владельцем: «Каналы · Поведение ·
// Система · Безопасность», те же слова, что были у вкладок.
var wantGroups = []string{"Каналы", "Поведение", "Система", "Безопасность"}

// Каждый раздел живёт ровно в одной группе, а у группы есть заголовок и
// короткое имя у каждого раздела.
//
// Настройки разрослись в каталог: семнадцать экранов, у каждого свой
// вход из меню, и параметр искали перебором.
func TestEverySectionHasOneGroup(t *testing.T) {
	where := map[string]string{}
	if len(settingsGroups) != len(wantGroups) {
		t.Fatalf("групп %d, а решено %d", len(settingsGroups), len(wantGroups))
	}
	for i, group := range settingsGroups {
		if group.Name != wantGroups[i] {
			t.Errorf("группа №%d зовётся «%s», а должна «%s»", i+1, group.Name, wantGroups[i])
		}
		if len(group.sections()) == 0 {
			t.Errorf("группа %q без разделов", group.Name)
		}
		for _, section := range group.sections() {
			if previous, before := where[section]; before {
				t.Errorf("раздел %q стоит и в «%s», и в «%s» — это снова каталог", section, previous, group.Name)
			}
			where[section] = group.Name
			if sectionTitle[section] == "" {
				t.Errorf("у раздела %q нет короткого имени для списка", section)
			}
		}
		for _, block := range group.Blocks {
			if len(block.Sections) == 0 || len(block.Sections) > 6 {
				t.Errorf("%s → %s: %d строк в подгруппе", group.Name, block.Title, len(block.Sections))
			}
		}
	}
	if where[botsScreen] != "Каналы" {
		t.Errorf("%q находится в группе %q, ожидались «Каналы»", botsTitle, where[botsScreen])
	}
	if where[settingsRoot] != "" {
		t.Errorf("«Обзор» стоит в группе «%s»: он над группами, а не в них", where[settingsRoot])
	}
}

// Хвост экрана не сбивает выбор раздела: «settings:mail@ящик» — та же
// «Почта», «settings:servers~log:имя» — те же «Серверы». Обзор своим
// адресом — тот же «Обзор», что и «settings».
func TestScreenTailDoesNotBreakSection(t *testing.T) {
	cases := map[string]string{
		"settings:mail@owner@example.com": "settings:mail",
		"settings:servers~log:Microtik":   serversScreen,
		"settings:secrets@ssh:хозяин":     secretsScreen,
		modelsScreen:                      modelsScreen,
		settingsRoot:                      settingsRoot,
		settingsOverview:                  settingsRoot,
	}
	for screen, want := range cases {
		group, section, ok := settingsSectionOf(screen)
		if !ok {
			t.Errorf("%q не признан настройкой", screen)
			continue
		}
		if section != want {
			t.Errorf("%q → раздел %q, а ждали %q (группа «%s»)", screen, section, want, group.Name)
		}
	}
	if _, _, ok := settingsSectionOf("inbox"); ok {
		t.Error("«Почта» из меню принята за настройку")
	}
}

// Адрес прежней вкладки ведёт в первый раздел её группы: закладки,
// уведомления и ссылки в чужих журналах помнят его.
func TestOldTabAddressLeadsToFirstSection(t *testing.T) {
	for oldTab, want := range map[string]string{
		"settings:каналы":       "settings:mail",
		"settings:поведение":    modelsScreen,
		"settings:система":      serversScreen,
		"settings:безопасность": accessScreen,
	} {
		where, ok := tabScreen(oldTab)
		if !ok || where != want {
			t.Errorf("%q ведёт в %q, а должен в %q", oldTab, where, want)
		}
	}
	if _, ok := tabScreen(modelsScreen); ok {
		t.Error("раздел принят за прежнюю вкладку")
	}
}

// После перехода по адресу прежней вкладки экран ЗОВЁТСЯ разделом.
//
// Первая редакция подменяла только screenName, а в data-screen уезжал
// doc.Screen с исходным «settings:каналы». Клиент запоминал это имя как
// текущий экран и слал его заголовком HX-Current-Screen; кириллица в
// заголовке HTTP роняет каждый следующий запрос ещё в браузере — вся
// навигация вставала после первого же нажатия.
func TestOldTabCanonicalizedToSection(t *testing.T) {
	s, _ := testServer(t)
	for _, phone := range []bool{false, true} {
		for tab, section := range map[string]string{
			"settings:каналы":       "settings:mail",
			"settings:поведение":    modelsScreen,
			"settings:система":      serversScreen,
			"settings:безопасность": accessScreen,
		} {
			doc, err := s.buildScreenForPhone(tab, "", "", 0, phone, false)
			if err != nil {
				t.Fatalf("%s: %v", tab, err)
			}
			if doc.Screen != section {
				t.Errorf("после %q экран зовётся %q, а должен %q", tab, doc.Screen, section)
			}
			for _, r := range doc.Screen {
				if r > 127 {
					t.Errorf("в адресе экрана %q не-ASCII знак: он поедет заголовком HTTP", doc.Screen)
					break
				}
			}
			group, _, _ := settingsSectionOf(section)
			if doc.ActivePanel != group.ID {
				t.Errorf("после %q выбрана группа %q, а должна %q", tab, doc.ActivePanel, group.ID)
			}
		}
	}
}

// Вкладок у настроек нет нигде — ни на широком экране, ни на телефоне, —
// и выбранной вкладки тоже.
func TestSettingsHaveNoTabs(t *testing.T) {
	s, _ := testServer(t)
	for _, phone := range []bool{false, true} {
		for _, screen := range settingsScreens() {
			doc, err := s.buildScreenForPhone(screen, "", "", 50, phone, false)
			if err != nil {
				t.Fatalf("%s: %v", screen, err)
			}
			if len(doc.Tabs) != 0 || doc.ActiveTab != "" {
				t.Errorf("%s (телефон: %v): вкладки %d, выбрана %q", screen, phone, len(doc.Tabs), doc.ActiveTab)
			}
		}
	}
}

// panelShape — состав колонки без подсветки и счётчиков: то, что обязано
// стоять одинаково на каждом экране настроек.
func panelShape(panel []PanelItem) string {
	var b strings.Builder
	for _, item := range panel {
		b.WriteString(item.Kind + "|" + item.ID + "|" + item.Title + "\n")
	}
	return b.String()
}

func TestSettingsPanelShowsGroups(t *testing.T) {
	s, _ := testServer(t)
	want := []string{settingsRoot, settingsChannels, settingsBehavior, settingsSystem, settingsSecurity}
	for _, phone := range []bool{false, true} {
		for _, screen := range settingsScreens() {
			doc, err := s.buildScreenForPhone(screen, "", "", 50, phone, false)
			if err != nil {
				t.Fatalf("%s: %v", screen, err)
			}
			if doc.PanelKind != "sections" || doc.PanelTitle != "Настройки" || len(doc.Panel) != len(want) {
				t.Fatalf("%s: неверная панель %q: %+v", screen, doc.PanelKind, doc.Panel)
			}
			for i, item := range doc.Panel {
				if item.ID != want[i] || item.Kind != "" || item.Title == "" {
					t.Errorf("%s: пункт %d: %+v", screen, i, item)
				}
			}
			group, _, _ := settingsSectionOf(screen)
			active := group.ID
			if active == "" {
				active = settingsRoot
			}
			if doc.ActivePanel != active {
				t.Errorf("%s: выбрано %q, ожидалось %q", screen, doc.ActivePanel, active)
			}
		}
	}
}

// найти — элемент по id.
func nodeByID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode && attr(n, "id") == id {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := nodeByID(c, id); r != nil {
			return r
		}
	}
	return nil
}

func TestMailCounterInCatalog(t *testing.T) {
	s, st := testServer(t)
	for i := 0; i < 3; i++ {
		m := &store.Message{MessageID: "<ждёт" + string(rune('а'+i)) + "@т>", Subject: "Тема",
			FromAddr: "a@пример.рф", Date: time.Now(), Status: store.StatusPending, Confidence: 0.9, Account: "проба"}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, screen := range []string{settingsRoot, settingsChannels} {
		doc, err := s.buildScreen(screen, "", "", 50)
		if err != nil {
			t.Fatal(err)
		}
		mail := docByID(doc, "catalog-"+mailScreen)
		if mail == nil || len(mail.Badges) != 1 || mail.Badges[0].Text != "3" || mail.Badges[0].Level != LevelWarn {
			t.Errorf("%s: неверный счётчик у «Почты»: %+v", screen, mail)
		}
		if overview := doc.Panel[0]; overview.ID != settingsRoot || overview.Level == "" || overview.Meta == "" {
			t.Errorf("%s: нет счётчика у «Обзора»: %+v", screen, overview)
		}
	}
}

// Поле поиска — первой строкой колонки, а не в полосе фильтров экрана:
// поле одно на экран. Колонка при этом без кнопки-шторки, которой
// пользовались, пока разделы приезжали снизу.
func TestSearchIsFirstRowOfColumn(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range []string{settingsRoot, modelsScreen, "settings:mail~spam"} {
		doc, err := s.buildScreen(screen, "", "", 50)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if strings.Contains(string(markup), "section-picker") {
			t.Errorf("%s: в разметке осталась кнопка-шторка section-picker", screen)
		}
		tree, _ := html.Parse(strings.NewReader("<div id=\"window\">" + string(markup) + "</div>"))
		panel := nodeByID(tree, "panel")
		if panel == nil || !hasClass(panel, "sections") {
			t.Fatalf("%s: колонки разделов нет", screen)
		}
		var fields []*html.Node
		var walk func(n *html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode && n.Data == "input" && attr(n, "id") == "search" {
				fields = append(fields, n)
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(tree)
		if len(fields) != 1 {
			t.Fatalf("%s: полей поиска %d, а должно быть одно", screen, len(fields))
		}
		if !inside(fields[0], panel) {
			t.Errorf("%s: поле поиска не в колонке разделов", screen)
		}
		// до первой строки колонки ничего, кроме названия: поле — первое
		var firstRow *html.Node
		var findPanelRow func(n *html.Node)
		findPanelRow = func(n *html.Node) {
			if firstRow != nil {
				return
			}
			if n.Type == html.ElementNode && (hasClass(n, "panel-row") || hasClass(n, "panel-heading")) {
				firstRow = n
				return
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				findPanelRow(c)
			}
		}
		findPanelRow(panel)
		if firstRow == nil {
			t.Fatalf("%s: в колонке нет ни одной строки", screen)
		}
		// поле раньше первой строки в порядке документа
		var order []*html.Node
		var collect func(n *html.Node)
		collect = func(n *html.Node) {
			order = append(order, n)
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				collect(c)
			}
		}
		collect(panel)
		iField, iRow := -1, -1
		for i, n := range order {
			if n == fields[0] {
				iField = i
			}
			if n == firstRow {
				iRow = i
			}
		}
		if iField < 0 || iRow < 0 || iField > iRow {
			t.Errorf("%s: поле поиска стоит не перед строками колонки", screen)
		}
	}
}

func TestSettingsCatalogLinks(t *testing.T) {
	s, _ := testServer(t)
	for _, phone := range []bool{false, true} {
		for _, group := range settingsGroups {
			doc, err := s.buildScreenForPhone(group.ID, "", "", 50, phone, false)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Heading != group.Name || len(doc.Components) != len(group.Blocks) {
				t.Fatalf("%s: неверная структура каталога", group.ID)
			}
			for i, block := range group.Blocks {
				section := doc.Components[i]
				if section.Title != block.Title || len(section.Children) != len(block.Sections) {
					t.Fatalf("%s: неверная подгруппа %q", group.ID, section.Title)
				}
				for j, addr := range block.Sections {
					row := section.Children[j]
					if row.Subtitle != sectionTitle[addr] || len(row.Actions) != 1 || row.Actions[0].Target != addr {
						t.Errorf("%s: неверная строка %s: %+v", group.ID, addr, row)
					}
				}
			}
		}
	}
}

func TestSettingsCatalogHidesAdminScreens(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range []string{settingsChannels, settingsSecurity, settingsOverview} {
		doc, err := s.buildScreenForPhone(screen, "", "", 50, true, false, asGuest())
		if err != nil {
			t.Fatal(err)
		}
		for _, addr := range []string{botsScreen, maxScreen, wechatScreen, accessScreen} {
			if docByID(doc, "catalog-"+addr) != nil || docByID(doc, "беда-"+addr) != nil {
				t.Errorf("%s: гость видит ссылку на %s", screen, addr)
			}
		}
	}
}

// Запрос на телефонном корне — результаты поиска вместо списка: поле
// остаётся, стирание запроса возвращает список.
func TestPhoneRootSearchShowsResults(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreenForPhone(settingsRoot, "", "пуши", 50, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Searchable {
		t.Error("с запросом поле поиска пропало")
	}
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "Нашлось") || !strings.Contains(dump, "Уведомления") {
		t.Errorf("по «пуши» не нашлись «Уведомления»:\n%s", clipTest(dump))
	}
	if docByID(doc, "settings-catalog") != nil {
		t.Error("с запросом остался каталог")
	}
}

func TestSettingsBackToCatalog(t *testing.T) {
	s, _ := testServer(t)
	for _, phone := range []bool{false, true} {
		for _, group := range settingsGroups {
			for _, screen := range group.sections() {
				doc, err := s.buildScreenForPhone(screen, "", "", 50, phone, false)
				if err != nil {
					t.Fatal(err)
				}
				if len(doc.Components) == 0 || doc.Components[0].Type != "crumb" {
					t.Fatalf("%s: нет обратного пути", screen)
				}
				back := doc.Components[0].Actions
				if len(back) != 1 || back[0].Target != group.ID || back[0].Label != "‹ "+group.Name {
					t.Errorf("%s: неверный обратный путь: %+v", screen, back)
				}
			}
		}
	}
}

func TestOverviewHasOwnAddress(t *testing.T) {
	s, _ := testServer(t)
	a, err := s.buildScreen(settingsRoot, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.buildScreen(settingsOverview, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	content := func(d *Document) string {
		j, _ := json.Marshal(d.Components)
		return string(j) + panelShape(d.Panel) + d.Heading + d.ActivePanel
	}
	if content(a) != content(b) {
		t.Error("на широком экране «settings» и «settings:overview» показывают разное")
	}
	if b.Screen != settingsOverview || b.ActivePanel != settingsRoot || b.Heading != "Обзор" {
		t.Errorf("Обзор по своему адресу: экран %q, выбран %q, заголовок %q", b.Screen, b.ActivePanel, b.Heading)
	}
	ph, err := s.buildScreenForPhone(settingsOverview, "", "", 50, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if ph.Heading != "Обзор" || len(ph.Panel) != 5 || docByID(ph, "settings-catalog") == nil {
		t.Errorf("Обзор на телефоне: заголовок %q, панель из %d пунктов", ph.Heading, len(ph.Panel))
	}
	if !strings.Contains(dumpScreen(ph), "Ждут решения") {
		t.Errorf("на телефонном Обзоре нет полки «Ждут решения»")
	}
	// сохраняется на устройстве так же, как «settings»
	if !s.canStoreScreen(settingsOverview) {
		t.Error("Обзор по своему адресу не хранится в снимке, а «settings» хранится")
	}
}

// Список слева, который не колонка настроек (скрипты), на телефоне по-прежнему
// открывается кнопкой: другого пути к списку у экрана нет. Кнопка зовётся
// panel-picker и принадлежит только таким спискам — у настроек её нет.
func TestListPickerStaysForScripts(t *testing.T) {
	s, st := testServer(t)
	if _, err := st.CreateScript("Письма за сутки", "return 1"); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreenForPhone("scripts", "", "", 50, true, false)
	if err != nil {
		t.Fatal(err)
	}
	markup, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markup), `class="action panel-picker"`) {
		t.Error("у «Скриптов» на телефоне пропала кнопка списка")
	}
	if strings.Contains(string(markup), "section-picker") {
		t.Error("в разметке «Скриптов» осталось старое имя section-picker")
	}
}
