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
		out = append(out, group.Sections...)
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
		if len(group.Sections) == 0 {
			t.Errorf("группа %q без разделов", group.Name)
		}
		for _, section := range group.Sections {
			if previous, before := where[section]; before {
				t.Errorf("раздел %q стоит и в «%s», и в «%s» — это снова каталог", section, previous, group.Name)
			}
			where[section] = group.Name
			if sectionTitle[section] == "" {
				t.Errorf("у раздела %q нет короткого имени для списка", section)
			}
		}
		// список длиннее шести строк сам становится каталогом
		if len(group.Sections) > 6 {
			t.Errorf("в группе «%s» %d разделов — список перестаёт читаться", group.Name, len(group.Sections))
		}
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
			if !phone && doc.ActivePanel != section {
				t.Errorf("после %q в колонке выбран %q, а должен %q", tab, doc.ActivePanel, section)
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

// Колонка одна на все настройки: «Обзор» наверху, потом четыре группы с
// заголовками и все разделы в том же порядке и под теми же именами, что
// были у вкладок. Выбран ровно один пункт, и это тот, где человек стоит.
// Состав колонки не зависит от экрана: меняются подсветка и счётчики.
func TestPanelIsOneColumn(t *testing.T) {
	s, _ := testServer(t)
	var sample string
	for _, screen := range settingsScreens() {
		doc, err := s.buildScreen(screen, "", "", 50)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if doc.PanelKind != "sections" || doc.PanelTitle != "Настройки" {
			t.Errorf("%s: вид колонки %q, название %q", screen, doc.PanelKind, doc.PanelTitle)
		}
		// «Обзор» — первый пункт, не заголовок
		if len(doc.Panel) == 0 || doc.Panel[0].Kind != "" || doc.Panel[0].ID != settingsRoot || doc.Panel[0].Title != "Обзор" {
			t.Fatalf("%s: колонка начинается с %+v, а должна с «Обзор»", screen, doc.Panel[:1])
		}
		// порядок: заголовок группы, её разделы, следующий заголовок
		var headings []string
		for i, item := range doc.Panel {
			if item.Kind == "heading" {
				headings = append(headings, item.Title)
				if item.ID != "" || item.Delete != nil || item.Meta != "" || item.Level != "" {
					t.Errorf("%s: у заголовка группы «%s» есть адрес, действие или счётчик: %+v", screen, item.Title, item)
				}
				continue
			}
			if item.ID == "" {
				t.Errorf("%s: пункт №%d без адреса и не заголовок: %+v", screen, i, item)
			}
		}
		if strings.Join(headings, "|") != strings.Join(wantGroups, "|") {
			t.Errorf("%s: заголовки групп %v, а должны %v", screen, headings, wantGroups)
		}
		for _, group := range settingsGroups {
			var ids []string
			in := false
			for _, item := range doc.Panel {
				switch {
				case item.Kind == "heading":
					in = item.Title == group.Name
				case in:
					ids = append(ids, item.ID)
				}
			}
			if strings.Join(ids, "|") != strings.Join(group.Sections, "|") {
				t.Errorf("%s: под «%s» стоят %v, а должны %v", screen, group.Name, ids, group.Sections)
			}
		}
		for _, item := range doc.Panel {
			if item.Kind == "" && item.Title != sectionTitle[item.ID] {
				t.Errorf("%s: раздел %q зовётся «%s», а в списке «%s»", screen, item.ID, item.Title, sectionTitle[item.ID])
			}
		}
		// выбран ровно один, и это он
		want, _ := func() (string, bool) { _, section, ok := settingsSectionOf(screen); return section, ok }()
		if doc.ActivePanel != want {
			t.Errorf("%s: выбран %q, а должен %q", screen, doc.ActivePanel, want)
		}
		var selected int
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		tree, _ := html.Parse(strings.NewReader("<div id=\"window\">" + string(markup) + "</div>"))
		var walk func(n *html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode && hasClass(n, "item") && hasClass(n, "selected") && inside(n, nodeByID(tree, "panel")) {
				selected++
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(tree)
		if selected != 1 {
			t.Errorf("%s: в колонке отмечено %d пунктов, а должен один", screen, selected)
		}
		// состав один и тот же на каждом экране
		shape := panelShape(doc.Panel)
		if sample == "" {
			sample = shape
		} else if shape != sample {
			t.Errorf("%s: состав колонки отличается от остальных экранов настроек:\n%s", screen, shape)
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

// Счётчик «есть что решить» стоит у строки раздела, и только у неё: три
// письма ждут решения по спаму — три у «Почты», а не у заголовка группы
// и не у соседей. У «Обзора» счётчик — число разделов с замечаниями.
func TestPanelCounterOnSectionRow(t *testing.T) {
	s, st := testServer(t)
	for i := 0; i < 3; i++ {
		m := &store.Message{MessageID: "<ждёт" + string(rune('а'+i)) + "@т>", Subject: "Тема",
			FromAddr: "a" + string(rune('а'+i)) + "@пример.рф", Date: time.Now(),
			Status: store.StatusPending, Confidence: 0.9, Account: "проба"}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// с любого экрана настроек счётчики одни и те же
	for _, screen := range []string{settingsRoot, modelsScreen, accessScreen} {
		doc, err := s.buildScreen(screen, "", "", 50)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		marks := map[string]PanelItem{}
		for _, item := range doc.Panel {
			if item.Meta != "" || item.Level != "" {
				marks[item.ID+item.Kind] = item
			}
		}
		mail, ok := marks["settings:mail"]
		if !ok || mail.Meta != "3" || mail.Level != LevelWarn {
			t.Errorf("%s: у «Почты» ждали счётчик 3 уровня warn, а есть %+v", screen, mail)
		}
		overview, ok := marks[settingsRoot]
		if !ok || overview.Level == "" || overview.Meta == "" {
			t.Errorf("%s: у «Обзора» счётчика нет: %+v", screen, overview)
		}
		for key, item := range marks {
			if item.Kind == "heading" {
				t.Errorf("%s: счётчик у заголовка группы: %+v", screen, item)
			}
			if item.Level == LevelOK {
				t.Errorf("%s: норма помечена (%s): она молчит", screen, key)
			}
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

// Телефон, корень: список разделов экраном. Те же «Обзор» и четыре группы,
// что в колонке на широком экране; колонки и вкладок нет, поле поиска
// первой строкой.
func TestPhoneRootIsSectionList(t *testing.T) {
	s, st := testServer(t)
	m := &store.Message{MessageID: "<ждёт@т>", Subject: "Тема", FromAddr: "a@пример.рф",
		Date: time.Now(), Status: store.StatusPending, Confidence: 0.9, Account: "проба"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreenForPhone(settingsRoot, "", "", 50, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Panel) != 0 || doc.PanelTitle != "" || doc.PanelKind != "" || len(doc.Tabs) != 0 {
		t.Errorf("на телефоне у корня колонка или вкладки: панель %d, вид %q, вкладок %d", len(doc.Panel), doc.PanelKind, len(doc.Tabs))
	}
	if !doc.Searchable {
		t.Error("у корня настроек на телефоне нет поиска")
	}
	if doc.Screen != settingsRoot || doc.Heading != "Настройки" {
		t.Errorf("корень зовётся %q, заголовок %q", doc.Screen, doc.Heading)
	}
	// первым — «Обзор» своим адресом, дальше группы в порядке колонки
	var addrs, headings []string
	for _, comp := range doc.Components {
		if comp.Type != "section" {
			continue
		}
		if comp.Title != "" {
			headings = append(headings, comp.Title)
		}
		for _, child := range comp.Children {
			for _, a := range child.Actions {
				if a.Action == "$goto" {
					addrs = append(addrs, a.Target)
				}
			}
		}
	}
	want := []string{settingsOverview}
	for _, group := range settingsGroups {
		want = append(want, group.Sections...)
	}
	if strings.Join(addrs, "|") != strings.Join(want, "|") {
		t.Errorf("строки корня ведут в %v, а должны в %v", addrs, want)
	}
	if strings.Join(headings, "|") != strings.Join(wantGroups, "|") {
		t.Errorf("заголовки групп корня %v, а должны %v", headings, wantGroups)
	}
	// у «Почты» счётчик, у «Обзора» тоже
	dump := dumpScreen(doc)
	for _, needed := range []string{"Почта", "Обзор", "ящик"} {
		if !strings.Contains(dump, needed) {
			t.Errorf("в корне нет %q:\n%s", needed, clipTest(dump))
		}
	}
	var mail *Component
	for i, comp := range doc.Components {
		for j, child := range comp.Children {
			if child.Subtitle == "Почта" {
				mail = &doc.Components[i].Children[j]
			}
		}
	}
	if mail == nil || len(mail.Badges) == 0 || mail.Badges[0].Text != "1" {
		t.Errorf("у строки «Почта» счётчика нет: %+v", mail)
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
	if strings.Contains(dump, "Безопасность") {
		t.Errorf("с запросом на экране остался весь список:\n%s", clipTest(dump))
	}
}

// Телефон, раздел: колонки нет, вкладок нет, над заголовком «‹ Настройки»
// ведёт в корень, а верхнее поле поиска не занимает полосу.
func TestPhoneSectionHasBackNotColumn(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range settingsScreens() {
		if screen == settingsRoot {
			continue
		}
		doc, err := s.buildScreenForPhone(screen, "", "", 50, true, false)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if len(doc.Panel) != 0 || doc.PanelKind != "" || len(doc.Tabs) != 0 {
			t.Errorf("%s: на телефоне колонка или вкладки: панель %d, вкладок %d", screen, len(doc.Panel), len(doc.Tabs))
		}
		if len(doc.Components) == 0 || doc.Components[0].Type != "crumb" {
			t.Errorf("%s: первым компонентом не «назад»", screen)
			continue
		}
		back := doc.Components[0].Actions
		if len(back) != 1 || back[0].Action != "$goto" || back[0].Target != settingsRoot || back[0].Label != "‹ Настройки" {
			t.Errorf("%s: «назад» устроен не так: %+v", screen, back)
		}
		if doc.Searchable && len(doc.Filters) == 0 {
			t.Errorf("%s: у раздела на телефоне осталась отдельная полоса поиска", screen)
		}
		// разметка: ни колонки, ни шторки, ни вкладок экрана
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		for _, extra := range []string{"section-picker", "panel-picker", `id="panel"`, `class="tabs-bar"`} {
			if strings.Contains(string(markup), extra) {
				t.Errorf("%s: на телефоне в разметке есть %q", screen, extra)
			}
		}
	}
}

// Обзор: на широком экране `settings` и `settings:overview` — один и тот
// же экран рядом с колонкой, на телефоне `settings:overview` — содержимое
// Обзора с «назад» в корень, а `settings` — список.
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
	if ph.Heading != "Обзор" || len(ph.Components) < 2 || ph.Components[0].Type != "crumb" ||
		ph.Components[0].Actions[0].Target != settingsRoot {
		t.Errorf("Обзор на телефоне: заголовок %q, первый компонент %+v", ph.Heading, ph.Components[:1])
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
