package api

// Вкладка «Модели»: один список, поиск и отбор на сервере.
//
// Полтораста моделей строками по восемь фактов были пятью тысячами узлов на
// экране, который открывают ради одной цены. Теперь строка тонкая — имя, не
// больше двух капсул, одна фраза, одна кнопка и «···», — а всё остальное
// (умения, доверие, цена, расход, удаление) на экране модели, куда ведёт вся
// строка. Верхнее поле поиска и чипы отбора идут параметрами на сервер: ищется
// по всем моделям, а не по показанным. Показывается порция в тридцать строк,
// остальное — по «Показать ещё»; группы по провайдеру свёрнуты, кроме тех, где
// есть совпадения (а без запроса — кроме тех, где модели стоят в ролях).

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// modelsPage — сколько строк списка моделей показывается с первого раза.
const modelsPage = 30

// modelFilterAll — отбор «Все»: не отбирает ничего.
const modelFilterAll = "all"

// modelFilter — один чип отбора списка моделей.
type modelFilter struct {
	ID, Title string
	Match     func(engine.ModelCard) bool
}

var modelFilters = []modelFilter{
	{modelFilterAll, "Все", func(engine.ModelCard) bool { return true }},
	{"roles", "В ролях", func(c engine.ModelCard) bool { return c.InRoles() }},
	{"noprice", "Без цены", func(c engine.ModelCard) bool { return c.NoPrice() }},
	{"search", "Ищут", func(c engine.ModelCard) bool { return c.Search }},
	{"hears", "Слышат", func(c engine.ModelCard) bool { return c.Hears }},
	{"trusted", "Доверенные", func(c engine.ModelCard) bool { return c.Trusted }},
	{"gone", "Нет у провайдера", func(c engine.ModelCard) bool { return c.Gone }},
}

// modelMatches — модель отвечает запросу: каждое слово есть в имени, названии
// у провайдера или имени провайдера. Регистр не важен.
func modelMatches(c engine.ModelCard, words []string) bool {
	if len(words) == 0 {
		return true
	}
	hay := strings.ToLower(c.Name + " " + c.Model + " " + c.Group)
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// cardGroup — модели одного провайдера и признак «хоть одна стоит в роли».
type cardGroup struct {
	Name    string
	Cards   []engine.ModelCard
	HasRole bool
}

// groupCards — карточки по провайдерам, в порядке важности: сперва те, у кого
// модели стоят в ролях, потом у кого их больше, потом по имени. Внутри группы —
// сперва занятые ролями, потом по имени.
func groupCards(cards []engine.ModelCard) []cardGroup {
	index := map[string]int{}
	var groups []cardGroup
	for _, c := range cards {
		i, seen := index[c.Group]
		if !seen {
			i = len(groups)
			index[c.Group] = i
			groups = append(groups, cardGroup{Name: c.Group})
		}
		groups[i].Cards = append(groups[i].Cards, c)
		groups[i].HasRole = groups[i].HasRole || c.InRoles()
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		switch {
		case a.HasRole != b.HasRole:
			return a.HasRole
		case len(a.Cards) != len(b.Cards):
			return len(a.Cards) > len(b.Cards)
		}
		return a.Name < b.Name
	})
	for i := range groups {
		list := groups[i].Cards
		sort.SliceStable(list, func(a, b int) bool {
			if list[a].InRoles() != list[b].InRoles() {
				return list[a].InRoles()
			}
			return list[a].Name < list[b].Name
		})
	}
	return groups
}

// groupTitle — имя группы для человека. Имя провайдера из справочника остаётся
// как есть («fal.ai» не становится «Fal.ai»); безымянная группа «на этой
// машине» — с заглавной, потому что стоит в начале строки.
func groupTitle(name string) string {
	if name == engine.GroupThisMachine {
		return firstUpper(name)
	}
	return name
}

// firstUpper — строка с заглавной первой буквой.
func firstUpper(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// modelsListTab — «Модели».
func (s *Server) modelsListTab(doc *Document, add func(Component), cards []engine.ModelCard, filter, search string, limit int) {
	// на телефоне колонки разделов нет, и поле поиска стоит над списком: оно
	// искало бы по настройкам, а не по списку, если бы экран не сказал, что у
	// него свой поиск
	doc.Searchable = true
	doc.SearchHint = "Найти модель по имени"

	counts := make([]int, len(modelFilters))
	for _, c := range cards {
		for i, f := range modelFilters {
			if f.Match(c) {
				counts[i]++
			}
		}
	}
	active := modelFilterAll
	match := modelFilters[0].Match
	for i, f := range modelFilters {
		doc.Filters = append(doc.Filters, Filter{ID: f.ID, Title: fmt.Sprintf("%s · %d", f.Title, counts[i])})
		if f.ID == filter {
			active, match = f.ID, f.Match
		}
	}
	doc.ActiveFilter = active
	words := strings.Fields(strings.ToLower(search))
	query := active != modelFilterAll || len(words) > 0

	page := modelsPage
	if limit > 0 && limit != defaultRows {
		page = limit
	}

	if len(cards) == 0 {
		empty := emptyState("Моделей пока нет",
			"Подключите провайдера: ключ подошёл и модели пришли — все они сразу оказываются в подключённых", "tray")
		empty.Actions = []Action{{Action: "$goto", Target: entityAddr(modelsScreen, modelsNew, ""), Label: "Подключить провайдера", Style: "primary"}}
		add(Component{Type: "section", Children: []Component{empty}})
		return
	}

	shown, matched := 0, 0
	var sections []Component
	var hidden []string
	for _, g := range groupCards(cards) {
		var list []engine.ModelCard
		for _, c := range g.Cards {
			if match(c) && modelMatches(c, words) {
				list = append(list, c)
			}
		}
		matched += len(list)
		if len(list) == 0 {
			continue
		}
		take := len(list)
		if left := page - shown; take > left {
			take = left
		}
		if take <= 0 {
			hidden = append(hidden, fmt.Sprintf("%s %d", groupTitle(g.Name), len(list)))
			continue
		}
		rows := make([]Component, 0, take)
		for _, c := range list[:take] {
			rows = append(rows, s.modelRow(c))
		}
		shown += take
		title := groupTitle(g.Name) + " · " + plural(len(list), "модель", "модели", "моделей")
		if take < len(list) {
			title += fmt.Sprintf(" · показано %d", take)
		}
		// свёрнут, пока человек не искал: открыты только группы с совпадениями
		// запроса и, без запроса, те, где модели стоят в ролях
		open := query || g.HasRole
		sections = append(sections, Component{Type: "section", ID: "prov-" + g.Name, Title: title,
			Collapsed: true, Expanded: open, Children: rows})
	}
	if matched == 0 {
		add(Component{Type: "section", Children: []Component{emptyState("Под отбор ничего не подошло",
			"Попробуйте другое имя — ищется по "+plural(len(cards), "модели", "моделей", "моделей")+" на сервере, не по показанным", "magnifyingglass")}})
		return
	}
	add(Component{Type: "section", ID: "models-list", Title: modelsListTitle(matched, len(cards), query), Children: sections})
	if shown < matched {
		note := fmt.Sprintf("Показано %d из %d", shown, matched)
		if len(hidden) > 0 {
			note += " · ещё у " + strings.Join(hidden, ", ")
		}
		add(Component{Type: "text", Style: "secondary", ID: "models-rest",
			Text: note + " — наберите имя, выберите отбор или нажмите «Показать ещё»."})
		add(Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", shown, matched)}}})
	}
	if line := s.lastModelResult(); line != "" {
		add(Component{Type: "text", Style: "secondary", Text: line})
	}
}

// modelsListTitle — заголовок списка: сколько нашлось и среди скольких.
func modelsListTitle(matched, total int, query bool) string {
	if query && matched != total {
		return fmt.Sprintf("Модели · нашлось %d из %d", matched, total)
	}
	return fmt.Sprintf("Модели · %d", total)
}

// lastModelResult — итог последнего действия, если он не привязан ни к модели,
// ни к провайдеру по имени (общие сообщения).
func (s *Server) lastModelResult() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCheck
}

// rolesPill — где модель занята, одной капсулой: названия ролей, а когда их
// много — числом.
func rolesPill(c engine.ModelCard) string {
	switch {
	case len(c.Roles) > 0 && len(c.Roles) <= 3:
		return strings.Join(c.Roles, " · ")
	case len(c.Roles) > 3:
		return fmt.Sprintf("в %d %s", len(c.Roles), plural2(len(c.Roles), "роли", "ролях", "ролях"))
	case len(c.SpareOf) == 1:
		return "запасная у «" + c.SpareOf[0] + "»"
	case len(c.SpareOf) > 1:
		return fmt.Sprintf("запасная у %d %s", len(c.SpareOf), plural2(len(c.SpareOf), "роли", "ролей", "ролей"))
	}
	return ""
}

// modelPills — капсулы строки модели: не больше двух, и только те, что
// отличают. Сперва то, что не так (нет у провайдера, нет ключа, цена не
// названа), потом где занята, потом умение или доверие.
func modelPills(c engine.ModelCard) []Badge {
	var pills []Badge
	if c.Gone {
		pills = append(pills, Badge{Text: "нет у провайдера", Level: LevelAlert})
	}
	if c.Problem != "" {
		pills = append(pills, Badge{Text: c.Problem, Level: LevelWarn})
	}
	if word := rolesPill(c); word != "" {
		pills = append(pills, Badge{Text: word})
	}
	if c.NoPrice() {
		pills = append(pills, Badge{Text: "цена не названа", Level: LevelWarn})
	}
	if c.Trusted {
		pills = append(pills, Badge{Text: "доверенная", Level: LevelOK})
	}
	if c.Search {
		pills = append(pills, Badge{Text: "ищет"})
	}
	if c.Hears {
		pills = append(pills, Badge{Text: "слышит"})
	}
	if len(pills) > 2 {
		pills = pills[:2]
	}
	return pills
}

// modelRow — тонкая строка списка: имя, не больше двух капсул, одна фраза, одна
// кнопка, «···». Вся строка ведёт на экран модели.
func (s *Server) modelRow(c engine.ModelCard) Component {
	row := Component{Type: "row", Layout: "wide", ID: "model-" + c.Name, Subtitle: c.Name,
		Caption: modelPhrase(c), Badges: modelPills(c)}
	if c.Gone {
		withLevel(&row, LevelWarn)
	}
	row.Actions = []Action{{Action: "$goto", Target: modelAddr(c.Name), Label: c.Name}}
	if !c.FromFile {
		row.Actions = append(row.Actions, Action{Action: "check_model", Target: c.Name, Label: "Проверить"})
		row.Menu = []Action{{Action: "remove_model", Target: c.Name, Label: "Удалить…", Style: "destructive",
			Confirm: "Удалить модель " + c.Name + "? Письма и архив не пострадают." + roleLossWarning(c), ConfirmOK: "Удалить"}}
	}
	return row
}

// modelPhrase — одна фраза о модели в списке: цена, а нет её — название у
// провайдера, если оно другое.
func modelPhrase(c engine.ModelCard) string {
	if price := c.PriceShort(); price != "" {
		return price
	}
	if c.Model != "" && c.Model != c.Name {
		return c.Model
	}
	return ""
}

// roleLossWarning — что случится с ролями, если модель удалить: роль, чья
// основная или запасная модель исчезла, остаётся без неё, и эта работа не
// делается. Удалять такую модель можно — как и раньше, — но человек должен
// знать, чем это кончится.
func roleLossWarning(c engine.ModelCard) string {
	var lost []string
	for _, r := range c.Roles {
		lost = append(lost, "«"+r+"»")
	}
	for _, r := range c.SpareOf {
		lost = append(lost, "запасная у «"+r+"»")
	}
	if len(lost) == 0 {
		return ""
	}
	return " Роли останутся без этой модели, и их работа не будет делаться, пока вы не назначите другую: " + strings.Join(lost, ", ") + "."
}
