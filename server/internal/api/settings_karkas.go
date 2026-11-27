package api

// Каркас настроек: компактный выбор группы и каталоги разделов.
// Один Panel идёт слева на широком экране и сверху на телефоне.

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	settingsRoot     = "settings"
	settingsOverview = "settings:overview"
	settingsChannels = "settings:channels"
	settingsBehavior = "settings:behavior"
	settingsSystem   = "settings:system"
	settingsSecurity = "settings:security"
)

type settingsBlock struct {
	Title    string
	Sections []string
}

type settingsGroup struct {
	ID, Legacy, Name, Icon string
	Blocks                 []settingsBlock
}

// sections — порядок разделов в каталоге, индексе и старых переходах.
func (g settingsGroup) sections() []string {
	var sections []string
	for _, block := range g.Blocks {
		sections = append(sections, block.Sections...)
	}
	return sections
}

var settingsGroups = []settingsGroup{
	{ID: settingsChannels, Legacy: "settings:каналы", Name: "Каналы", Icon: "bubble.left", Blocks: []settingsBlock{
		{Sections: []string{mailScreen}},
		{Title: "Мессенджеры", Sections: []string{tgScreen, maxScreen, wechatScreen, botsScreen}},
		{Title: "Обработка сообщений", Sections: []string{notifySettingsScreen, translateScreen}},
	}},
	{ID: settingsBehavior, Legacy: "settings:поведение", Name: "Поведение", Icon: "brain", Blocks: []settingsBlock{
		{Title: "Решения и ответы", Sections: []string{modelsScreen, searchScreen}},
		{Title: "Возможности", Sections: []string{skillsScreen, learningScreen}},
	}},
	{ID: settingsSystem, Legacy: "settings:система", Name: "Система", Icon: "server.rack", Blocks: []settingsBlock{
		{Title: "Инфраструктура", Sections: []string{serversScreen, storagesScreen, mcpScreen}},
		{Title: "Выполнение", Sections: []string{agentsScreen, toolsScreen, watchScreen}},
	}},
	{ID: settingsSecurity, Legacy: "settings:безопасность", Name: "Безопасность", Icon: "lock", Blocks: []settingsBlock{
		{Title: "Учётная запись", Sections: []string{accessScreen, secretsScreen}},
		{Title: "Аудит", Sections: []string{sentScreen, changesScreen}},
	}},
}

func settingsGroupAt(screen string) (settingsGroup, bool) {
	for _, group := range settingsGroups {
		if group.ID == screen {
			return group, true
		}
	}
	return settingsGroup{}, false
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
	botsScreen:           botsTitle,
	wechatScreen:         "WeChat",
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
	if group, ok := settingsGroupAt(base); ok {
		return group, base, true
	}
	for _, group := range settingsGroups {
		for _, section := range group.sections() {
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
		if doc.guest && adminSettingsScreen(addr) {
			continue
		}
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

// settingsPanel — общий список групп. Счётчики разделов стоят в каталогах.
func settingsPanel(mark sectionMark) []PanelItem {
	overview := PanelItem{ID: settingsRoot, Title: sectionTitle[settingsRoot], Icon: "house", Level: mark.Level}
	if mark.Count > 0 {
		overview.Meta = strconv.Itoa(mark.Count)
	}
	panel := []PanelItem{overview}
	for _, group := range settingsGroups {
		panel = append(panel, PanelItem{ID: group.ID, Title: group.Name, Icon: group.Icon})
	}
	return panel
}

// settingsFrame добавляет общий поиск и обратный путь после сборки экрана.
func (s *Server) settingsFrame(doc *Document, screen string, phone bool) {
	group, section, ok := settingsSectionOf(screen)
	if !ok {
		return
	}
	savingBar(doc.Components)
	doc.Tabs, doc.ActiveTab = nil, ""
	doc.PanelAction = nil
	doc.Searchable = !phone || !doc.noPhoneSearch
	if doc.SearchHint == "" {
		doc.SearchHint = "Найти настройку"
	}
	doc.PanelTitle = "Настройки"
	doc.PanelKind = "sections"
	doc.ActivePanel = settingsRoot
	if group.ID != "" {
		doc.ActivePanel = group.ID
	}
	doc.Panel = settingsPanel(s.sectionMarks(doc)[settingsRoot])
	if group.ID == "" || section == group.ID {
		return
	}
	for _, c := range doc.Components {
		if c.Type == "crumb" {
			return // у сущности свой обратный путь внутри раздела
		}
	}
	back := Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: group.ID, Label: "‹ " + group.Name}}}
	doc.Components = append([]Component{back}, doc.Components...)
}

// tabScreen сохраняет назначение прежних адресов вкладок группы.
func tabScreen(screen string) (string, bool) {
	for _, group := range settingsGroups {
		if group.Legacy == screen {
			return group.Blocks[0].Sections[0], true
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
