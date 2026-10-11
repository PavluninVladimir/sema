package api

// Каркас настроек: одна колонка разделов под четырьмя заголовками.
//
// Настройки разрослись в каталог: восемнадцать разделов, и человек ищет
// параметр перебором. Замеры 11 сентября 2026: «Модели и роли» — 3597
// узлов и 319 КБ, «Телеграм» — 1487, «Серверы» — 1142; на телефоне
// «Почта» занимала больше одиннадцати экранов прокрутки, «Доступ» —
// больше семи.
//
// Первый каркас (11 сентября) делил их на пять вкладок, и внутри вкладки
// был ещё список разделов. На телефоне это выходило ≈190 точек из 760 до
// первой строки содержимого и три нажатия до раздела: вкладка, кнопка,
// строка шторки. 1 октября 2026 вкладок не стало (макеты —
// `design/nastroyki-2/Karkas.html`): слова «Каналы · Поведение · Система ·
// Безопасность» остались заголовками групп, а колонка одна.
//
// Колонка — мебель: её состав не меняется от экрана к экрану, меняются
// подсветка и счётчики. Один выбранный пункт, строка «Обзор» наверху,
// поле поиска первой строкой.
//
// На телефоне колонки нет: корень настроек (`settings`) — тот же список,
// но экраном (`buildSettingsRoot`), а у раздела над заголовком «‹ Настройки».
// Какой документ нужен, знает сервер — клиент говорит, что окно узкое,
// заголовком `X-Sema-Phone`, как у «Чатов».
//
// Экраны при этом не переписаны: каркас — общая обёртка над ними, и
// каждый экран настроек получает её одинаково, чем бы он ни был занят.

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// settingsRoot — «Настройки». На широком экране это Обзор рядом с
	// колонкой, на телефоне — список разделов
	settingsRoot = "settings"
	// settingsOverview — Обзор своим адресом: на телефоне до него идут
	// строкой корня, и «назад» оттуда ведёт в список. На широком экране это
	// тот же Обзор, что и `settings`
	settingsOverview = "settings:overview"
)

// settingsGroup — заголовок колонки и разделы под ним. Порядок разделов
// важен: он же порядок в колонке.
type settingsGroup struct {
	// ID — адрес прежней вкладки: старые ссылки («settings:каналы») ведут в
	// первый раздел группы
	ID, Name string
	Sections []string
}

// settingsGroups — четыре заголовка. Раздел живёт ровно в одной группе:
// повторить его в двух значило бы снова завести каталог. «Обзор» не
// входит ни в одну — он стоит над ними.
var settingsGroups = []settingsGroup{
	{ID: "settings:каналы", Name: "Каналы", Sections: []string{
		"settings:mail", tgScreen, maxScreen, notifySettingsScreen, translateScreen}},
	{ID: "settings:поведение", Name: "Поведение", Sections: []string{
		modelsScreen, searchScreen, skillsScreen, learningScreen}},
	{ID: "settings:система", Name: "Система", Sections: []string{
		serversScreen, agentsScreen, mcpScreen, storagesScreen, toolsScreen, watchScreen}},
	{ID: "settings:безопасность", Name: "Безопасность", Sections: []string{
		accessScreen, secretsScreen, sentScreen, changesScreen}},
}

// sectionTitle — как раздел зовётся в списке. Короткое имя, а не заголовок
// экрана: в узкой колонке «Ключи и секреты» читается, а «Хранилище
// секретов и журнал обращений» — нет. Над содержимым раздел зовётся тем же
// именем (2 октября 2026 это привели к одному: «Доступ» → «Вход и доступ»,
// «Что Сёма нашёл в себе» и «Самонаблюдение» → «Работа Сёмы», «Перевод
// писем» → «Перевод»); держит `TestSectionNameInColumnIsScreenHeading`.
var sectionTitle = map[string]string{
	settingsRoot:         "Обзор",
	"settings:mail":      "Почта",
	tgScreen:             "Telegram",
	maxScreen:            "MAX",
	notifySettingsScreen: "Уведомления",
	translateScreen:      "Перевод",
	modelsScreen:         "Модели и роли",
	searchScreen:         "Поиск",
	skillsScreen:         "Скиллы",
	learningScreen:       "Развитие",
	serversScreen:        "Серверы",
	agentsScreen:         "Агенты",
	mcpScreen:            "Интеграции",
	storagesScreen:       "Хранилища",
	toolsScreen:          "Инструменты",
	watchScreen:          "Работа Сёмы",
	accessScreen:         "Вход и доступ",
	secretsScreen:        "Ключи и секреты",
	sentScreen:           "Что ушло наружу",
	changesScreen:        "Журнал настроек",
}

// baseScreen — адрес без хвоста. У экранов бывают хвосты:
// «settings:mail@ящик», «settings:servers@имя~log», «settings:secrets@имя».
// Раздел — то, что до хвоста.
func baseScreen(screen string) string {
	for _, sep := range []string{"@", "~"} {
		if i := strings.Index(screen, sep); i >= 0 {
			screen = screen[:i]
		}
	}
	return screen
}

// settingsSectionOf — раздел, которому принадлежит экран, и его группа. Для
// Обзора группы нет (пустая), а раздел — settingsRoot. Последнее значение —
// «экран из настроек»: у «inbox» оно ложь.
func settingsSectionOf(screen string) (settingsGroup, string, bool) {
	base := baseScreen(screen)
	if base == settingsRoot || base == settingsOverview {
		return settingsGroup{}, settingsRoot, true
	}
	for _, group := range settingsGroups {
		for _, section := range group.Sections {
			if section == base {
				return group, base, true
			}
		}
	}
	return settingsGroup{}, "", false
}

// sectionMark — что в разделе не так: число того, что ждёт решения
// (0 — число неизвестно, и пометка остаётся точкой), и уровень замечания.
// Норма молчит: у спокойного раздела пометки нет.
type sectionMark struct {
	Count int
	Level string
}

// sectionIndex — разделы со своими замечаниями по адресу раздела. Собираются
// один раз на документ: колонка, «Ждут решения» и поиск читают одно и то же.
func (s *Server) sectionIndex(doc *Document) map[string]settingsSection {
	if doc.sections == nil {
		doc.sections = s.allSections()
	}
	idx := make(map[string]settingsSection, len(doc.sections))
	for _, section := range doc.sections {
		idx[baseScreen(section.Screen)] = section
	}
	return idx
}

// sectionMarks — пометки по разделам для колонки и для корня на телефоне.
// У Обзора счётчик — число разделов с замечаниями, цвет — худшее из них:
// «Ждут решения» на нём считается по тем же разделам.
func (s *Server) sectionMarks(doc *Document) map[string]sectionMark {
	marks := map[string]sectionMark{}
	troubled, worst := 0, ""
	for addr, section := range s.sectionIndex(doc) {
		if section.Level == "" || section.Level == LevelOK {
			continue
		}
		marks[addr] = sectionMark{Count: section.Count, Level: section.Level}
		troubled++
		if worst != LevelAlert {
			worst = section.Level
		}
	}
	if troubled > 0 {
		marks[settingsRoot] = sectionMark{Count: troubled, Level: worst}
	}
	return marks
}

// settingsPanel — колонка разделов: «Обзор», потом четыре группы с
// заголовками. Заголовок группы — пункт без адреса. Один и тот же состав
// на каждом экране настроек: выбранным отмечен ровно один пункт.
func settingsPanel(marks map[string]sectionMark) []PanelItem {
	makeItem := func(addr string) PanelItem {
		item := PanelItem{ID: addr, Title: sectionTitle[addr]}
		if item.Title == "" {
			item.Title = addr
		}
		if m, ok := marks[addr]; ok {
			item.Level = m.Level
			if m.Count > 0 {
				item.Meta = strconv.Itoa(m.Count)
			}
		}
		return item
	}
	panel := []PanelItem{makeItem(settingsRoot)}
	for _, group := range settingsGroups {
		panel = append(panel, PanelItem{Title: group.Name, Kind: "heading"})
		for _, section := range group.Sections {
			panel = append(panel, makeItem(section))
		}
	}
	return panel
}

// settingsFrame — колонка разделов (на телефоне — «назад» в корень). Зовётся
// после того, как экран собран: он сам о каркасе не знает.
func (s *Server) settingsFrame(doc *Document, screen string, phone bool) {
	_, section, ok := settingsSectionOf(screen)
	if !ok {
		return
	}
	// формы настроек сохраняет одна полоса внизу экрана, а не кнопка у
	// каждой: см. settings_saving.go
	savingBar(doc.Components)
	// вкладок у настроек больше нет, и выбранная вкладка не значит ничего
	doc.Tabs, doc.ActiveTab = nil, ""
	doc.PanelAction = nil
	if phone {
		// колонки нет: корень — список разделов, раздел — целый экран с
		// «назад» в него. Над содержимым остаётся заголовок и, у каталогов,
		// внутренние вкладки — и всё
		doc.Panel, doc.PanelTitle, doc.PanelKind, doc.ActivePanel = nil, "", "", ""
		// у экрана сущности путь назад уже есть («‹ Серверы»), и «назад» на
		// нём один: в корень настроек возвращаются через список
		ownBack := len(doc.Components) > 0 && doc.Components[0].Type == "crumb"
		if screen != settingsRoot {
			if !ownBack {
				doc.Components = append([]Component{{Type: "crumb",
					Actions: []Action{{Action: "$goto", Target: settingsRoot, Label: "‹ Настройки"}}}}, doc.Components...)
			}
			// верхнее поле ищет по всем настройкам, а у раздела на телефоне
			// ему места нет: поле остаётся только там, где у экрана уже есть
			// полоса фильтров и собственный поиск по списку (правила сервера
			// MCP)
			if len(doc.Filters) == 0 || doc.noPhoneSearch {
				doc.Searchable = false
			}
		}
		return
	}
	// поиск по настройкам виден из любого раздела: он про все настройки
	// сразу, и возвращаться за ним на «Обзор» человеку незачем. Поле стоит
	// первой строкой колонки (PanelKind) и при этом одно на экран: ленивые
	// части экрана своего не рисуют (toScreen снимает SearchHint), а
	// набранное сверху уходит и в них
	doc.Searchable = true
	// у экрана со своим списком поле ищет по списку, и подсказка говорит об этом
	// (список моделей: «Найти модель по имени»)
	if doc.SearchHint == "" {
		doc.SearchHint = "Найти настройку"
	}
	// Колонка есть и у Обзора: она мебель, и без неё заголовок уезжал влево
	// на её ширину при каждом переходе между разделами
	doc.PanelTitle = "Настройки"
	doc.PanelKind = "sections"
	doc.ActivePanel = section
	doc.Panel = settingsPanel(s.sectionMarks(doc))
}

// tabScreen — куда вести, когда пришли по адресу прежней вкладки
// («settings:каналы»): в первый раздел её группы. Самой вкладки больше нет,
// но закладки, уведомления и ссылки в чужих журналах помнят её адрес.
func tabScreen(screen string) (string, bool) {
	for _, group := range settingsGroups {
		if group.ID == screen && len(group.Sections) > 0 {
			return group.Sections[0], true
		}
	}
	return "", false
}

// Внутренние вкладки раздела.
//
// Пять постоянных вкладок делят настройки на области, но внутри области
// раздел бывает сам по себе каталогом: у «Почты» двадцать восемь
// настроек и 886 узлов одной лентой, у «Моделей» — 3597 узлов. Второй
// уровень режет раздел по задаче человека: подключение отдельно от
// папок, роли отдельно от прайса.
//
// Внутренняя вкладка — это адрес, а не фильтр: «settings:mail@ящик~folders»
// можно открыть ссылкой и вернуться по ней. Хвост отделяется знаком «~»,
// как журнал сервера («settings:servers@имя~log»).

// splitSubtab — отделить внутреннюю вкладку от адреса экрана.
// Возвращает адрес без хвоста и саму вкладку (пусто — первая).
func splitSubtab(screen string) (base, tab string) {
	if i := strings.Index(screen, "~"); i >= 0 {
		return screen[:i], screen[i+1:]
	}
	return screen, ""
}

// innerTab — одна внутренняя вкладка. Пустой ИД у первой: адрес без
// хвоста должен вести куда-то осмысленно, а не в никуда.
type innerTab struct {
	ID, Name string
	Count    string
	Level    string
	// Addr — свой адрес вкладки вместо «база~ИД». Нужен вкладкам,
	// которые ни к чему не привязаны: «Спам» — это настройки всех ящиков
	// сразу, и адрес «settings:mail@owner@example.org~spam» врал бы,
	// что порог правится одному ящику. Такую ссылку ещё и пересылают.
	Addr string
}

// setSubtabs — полоса внутренних вкладок под шапкой. `base` —
// адрес экрана без хвоста, `selected` — ИД текущей.
func setSubtabs(doc *Document, base, selected string, list []innerTab) {
	doc.Subtabs = nil
	known := false
	for _, tab := range list {
		if tab.ID == selected {
			known = true
		}
	}
	if !known {
		selected = ""
	}
	for _, tab := range list {
		addr := base
		if tab.ID != "" {
			addr += "~" + tab.ID
		}
		if tab.Addr != "" {
			addr = tab.Addr
		}
		doc.Subtabs = append(doc.Subtabs, Filter{ID: addr, Title: tab.Name, Badge: tab.Count, Level: tab.Level})
		if tab.ID == selected {
			doc.ActiveSubtab = addr
		}
	}
}

// ---- экран сущности ----
//
// Сервер, модель, провайдер, ящик, сервер MCP, секрет, агент, хранилище —
// у каждого свой экран, а строка в списке тонкая (макеты —
// `design/nastroyki-2/`, приём 1 из README). Шапка у всех одна и та же, и
// рисует её не экран, а `entityFrame`: иначе у каждого она вышла бы своей.
//
// Адрес сущности — `раздел@сущность~вкладка`: `settings:servers@srv-vpn~auto`,
// `settings:mail@ящик~folders`. Раздел без `@` — список, `@new` — экран
// добавления. Хвост после второго `~` принадлежит самой вкладке («проверка
// команды» у правил): он едет в адресе, чтобы ссылку можно было переслать.

// entity — что нужно шапке сущности.
type entity struct {
	// Section — адрес раздела («settings:servers»), Back — как он зовётся в
	// «‹ Серверы». BackTo — куда ведёт «‹ …», если не в сам раздел: у модели
	// это её список, а не первая вкладка «Роли»
	Section, Back, BackTo string
	// ID — имя сущности в адресе. Name — как она зовётся в шапке и в h1
	ID, Name string
	// Avatar — буква или значок в плитке слева; AvatarColor — её цвет
	Avatar, AvatarColor string
	// Caption — одна строка фактов («Linux · sema@203.0.113.12:22 · на связи»)
	Caption string
	Badges  []Badge
	// Level красит полоску слева: тревога видна, не читая подписи
	Level string
	// Primary — единственная главная кнопка; остальное — в Menu («···»)
	Primary *Action
	Menu    []Action
	// Others — имена всех сущностей раздела, если их больше одной: тогда в
	// шапке есть переключатель, и перейти к соседней можно, не возвращаясь в
	// список. Switch — его подпись («Сервер»)
	Others []string
	Switch string
	// Tab — выбранная вкладка (пусто — первая), Tabs — все
	Tab  string
	Tabs []innerTab
}

// entityAddr — адрес сущности: раздел, сущность и вкладка. Пустая вкладка —
// первая; пустая сущность — сам раздел.
func entityAddr(section, id, tab string) string {
	addr := section
	if id != "" {
		addr += "@" + id
	}
	if tab != "" {
		addr += "~" + tab
	}
	return addr
}

// splitEntity — адрес сущности на раздел, сущность и хвост вкладки. Сущность
// — всё между первым «@» и первым «~», поэтому адрес почты с собакой внутри
// («settings:mail@owner@example.org~folders») разбирается верно.
func splitEntity(screen string) (section, id, tab string) {
	rest, tab, _ := strings.Cut(screen, "~")
	section, id, _ = strings.Cut(rest, "@")
	return section, id, tab
}

// entityFrame — шапка сущности: обратный путь, вкладки и закреплённая
// строка с именем, фактами, одной главной кнопкой и «···». Зовётся до
// содержимого экрана, потому что кладёт компоненты первыми.
//
// На телефоне вторая кнопка прячется в «···» сама: в строке стоит одна
// кнопка и меню, а лишнее — в меню (проход tidyRows).
func entityFrame(doc *Document, e entity) {
	doc.Heading = e.Name
	setSubtabs(doc, entityAddr(e.Section, e.ID, ""), e.Tab, e.Tabs)

	row := Component{Type: "row", Layout: "wide", ID: "entity-" + e.ID,
		Avatar: e.Avatar, AvatarColor: e.AvatarColor, AvatarShape: "square",
		Subtitle: e.Name, Caption: e.Caption, Badges: e.Badges, Menu: e.Menu}
	if e.Level != "" {
		withLevel(&row, e.Level)
	}
	if e.Primary != nil {
		row.Actions = []Action{*e.Primary}
	}
	if len(e.Others) > 1 {
		row.Selects = []Select{{Name: "value", Label: e.Switch, Value: e.ID, Options: e.Others,
			Action: Action{Action: "entity_goto", Target: entityAddr(e.Section, "", e.Tab)}}}
	}
	back := e.Section
	if e.BackTo != "" {
		back = e.BackTo
	}
	doc.Components = append([]Component{
		{Type: "crumb", Actions: []Action{{Action: "$goto", Target: back, Label: "‹ " + e.Back}}},
		{Type: "section", ID: "entity", Style: "sticky", Children: []Component{row}},
	}, doc.Components...)
}

// entityActions — переключатель сущности в шапке: перейти к другой, оставшись
// на той же вкладке. Цель — адрес раздела с вкладкой («settings:servers~auto»),
// выбранное имя приходит значением списка.
func (s *Server) entityActions(req actionRequest) (string, error) {
	if req.Action != "entity_goto" {
		return "", errUnhandled
	}
	section, tab := splitSubtab(req.Target)
	id := strings.TrimSpace(req.Params["value"])
	if section == "" || id == "" {
		return "", fmt.Errorf("нужен раздел и сущность")
	}
	return entityAddr(section, id, tab), nil
}
