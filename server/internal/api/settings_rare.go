package api

// «Тонкая настройка» и строки настройки.
//
// Пределы правят раз в год, а лежали рядом с тем, что правят каждый день:
// у «Интеграций» пятнадцать полей двумя формами под списком серверов, у
// «Поиска» одиннадцать столбиком с подсказками по три строки, у «Серверов»
// одиннадцать вовсе не нашлись на экране. Разбор 1 октября 2026
// (`design/nastroyki-2/RAZBOR.md`): человек пролистывает пределы, чтобы
// дойти до того, ради чего пришёл.
//
// Лекарство из двух частей, обе — в DSL, а не в каждом экране отдельно:
//
//   - **строки настройки** (`Form.Style == "rows"`): подпись и подсказка
//     слева, один контрол справа. Тот же `form`, те же `Field`, другая
//     разметка — старый клиент нарисует обычную форму, вреда нет;
//   - **«Тонкая настройка»** (`Tier == "rare"`): сервер помечает редкое
//     (форму, раздел или отдельное поле), а проход `foldRare` при сборке
//     экрана собирает всё помеченное в один свёрнутый раздел внизу, перед
//     «Опасным». Раздел ленивый: тело приезжает по раскрытию частью `rare`
//     (parts.go), как у списков чатов.
//
// Подпись «всё по умолчанию» / «K изменены» считается от умолчаний, а не
// от вкуса: у каждого редкого поля есть `Default`, и берётся он из реестра
// настроек (`store.SettingDef`), откуда читает и сам движок.

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	// tierRare — значение Component.Tier и Field.Tier
	tierRare = "rare"
	// tierTail — значение Component.Tier у раздела, который остаётся в самом
	// низу экрана: «Тонкая настройка» встаёт перед ним. Так стоит ленивый
	// журнал раздела — хроника, а не настройка, и последним ему быть по праву
	tierTail = "tail"
	// rareID — ID раздела «Тонкая настройка» и имя его ленивой части. Раздел
	// на экране один, поэтому одного имени хватает: по нему клиент помнит,
	// что человек его раскрыл
	rareID = "rare"
	// formRows — Form.Style: поля строками настройки
	formRows = "rows"
	// fineStyle — Section.Style: раздел «Тонкая настройка»
	fineStyle = "fine"
)

// foldRare — собрать редкое экрана в «Тонкую настройку». Зовётся один раз
// при сборке экрана, рядом с tidyRows и collapseLong; экран без редкого не
// меняется.
//
// Раздел ставится перед «Опасным» и перед ленивым журналом (`tierTail`), если
// они есть, иначе — последним: опасное и хроника должны оставаться самыми
// нижними, и открыть тонкую настройку не должно сдвигать их дальше, чем нужно.
func foldRare(doc *Document) {
	rest, rare := takeRare(doc.Components)
	if len(rare) == 0 {
		return
	}
	doc.rare = rare
	at := len(rest)
	for i, c := range rest {
		if isDanger(c) || c.Tier == tierTail {
			at = i
			break
		}
	}
	out := make([]Component, 0, len(rest)+1)
	out = append(out, rest[:at]...)
	out = append(out, rareSection(rare))
	out = append(out, rest[at:]...)
	doc.Components = out
}

// isDanger — блок «Опасное»: красная карточка со своей кнопкой.
func isDanger(c Component) bool {
	return c.Style == "danger" || strings.HasPrefix(c.Title, "Опасное")
}

// takeRare — вынуть редкое из компонентов: целую форму или раздел с меткой,
// а у формы с метками на отдельных полях — только эти поля. Возвращает
// остаток и вынутое. Новые срезы, а не переписанные старые: срез, уже отданный
// другому разделу, молча менять нельзя.
func takeRare(cs []Component) (rest, rare []Component) {
	for _, c := range cs {
		switch {
		case c.Tier == tierRare:
			rare = append(rare, rowsStyle(c))
		case c.Type == "form":
			keep, moved := splitRareFields(c)
			if moved != nil {
				rare = append(rare, rowsStyle(*moved))
			}
			if keep != nil {
				rest = append(rest, *keep)
			}
		case c.Type == "section" && len(c.Children) > 0:
			inner, found := takeRare(c.Children)
			if len(found) == 0 {
				rest = append(rest, c)
				continue
			}
			rare = append(rare, found...)
			// раздел, из которого вынули всё, пустым не остаётся
			if len(inner) > 0 {
				c.Children = inner
				rest = append(rest, c)
			}
		default:
			rest = append(rest, c)
		}
	}
	return rest, rare
}

// splitRareFields — разделить форму по меткам полей. Нет редких полей — форма
// остаётся как была; все поля редкие — уезжает целиком; смесь — обычные поля
// остаются, а редкие уезжают формой-близнецом с теми же подписью и действием.
func splitRareFields(c Component) (keep, moved *Component) {
	var plain, fine []Field
	for _, f := range c.Fields {
		if f.Tier == tierRare {
			fine = append(fine, f)
		} else {
			plain = append(plain, f)
		}
	}
	switch {
	case len(fine) == 0:
		return &c, nil
	case len(plain) == 0:
		return nil, &c
	}
	twin := c
	twin.ID = c.ID + "-rare"
	twin.Fields = fine
	twin.Actions = nil
	c.Fields = plain
	return &c, &twin
}

// rowsStyle — в «Тонкой настройке» поля идут строками настройки, чей бы ни
// была форма: единый вид важнее вкуса каждого экрана.
func rowsStyle(c Component) Component {
	if c.Type == "form" {
		c.Style = formRows
	}
	if len(c.Children) > 0 {
		kids := make([]Component, len(c.Children))
		for i, k := range c.Children {
			kids[i] = rowsStyle(k)
		}
		c.Children = kids
	}
	return c
}

// rareSection — свёрнутый ленивый раздел вместо вынутого редкого. Подпись
// «N параметров · о чём они» слева и «всё по умолчанию» / «K изменены»
// справа; Deferred — внутри будут формы, которые сохраняет нижняя полоса, и
// она должна быть на экране ещё до того, как раздел раскроют.
func rareSection(rare []Component) Component {
	params, changed, titles := rareStats(rare)
	state := "всё по умолчанию"
	level := ""
	if changed > 0 {
		state = fmt.Sprintf("%d %s", changed, plural2(changed, "изменён", "изменены", "изменены"))
		level = LevelInfo
	}
	caption := plural(params, "параметр", "параметра", "параметров")
	if len(titles) > 0 {
		caption += " · " + strings.Join(titles, ", ")
	}
	return Component{Type: "section", ID: rareID, Title: "Тонкая настройка", Style: fineStyle,
		Caption: caption, Meta: state, Level: level,
		Collapsed: true, Lazy: rareID, Deferred: true}
}

// rareStats — сколько параметров в редком, сколько из них не по умолчанию и
// как называются группы (по заголовкам форм и разделов).
func rareStats(rare []Component) (params, changed int, titles []string) {
	seen := map[string]bool{}
	var walk func(cs []Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			if t := strings.TrimSpace(c.Title); t != "" && (c.Type == "form" || c.Type == "section") && !seen[t] {
				seen[t] = true
				titles = append(titles, lowerFirst(t))
			}
			for _, f := range c.Fields {
				params++
				if fieldChanged(f) {
					changed++
				}
			}
			if c.Type == "row" && (c.Toggle != "" || len(c.Selects) > 0) {
				params++
			}
			walk(c.Children)
		}
	}
	walk(rare)
	if len(titles) > 4 {
		titles = titles[:4]
	}
	return params, changed, titles
}

// lowerFirst — «Серверы MCP в чате» после «параметров ·» читается строчной
// буквой. Слово с ещё одной заглавной внутри («SSH», «SearXNG») — имя, и оно
// остаётся как есть.
func lowerFirst(s string) string {
	word := s
	if i := strings.IndexByte(s, ' '); i >= 0 {
		word = s[:i]
	}
	upper := 0
	for _, r := range word {
		if unicode.IsUpper(r) {
			upper++
		}
	}
	r := []rune(s)
	if len(r) > 0 && upper == 1 && unicode.IsUpper(r[0]) {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

// fieldChanged — у поля есть умолчание, и значение с ним расходится. Нет
// умолчания — нечего сравнивать, поле «не изменено».
func fieldChanged(f Field) bool {
	if f.Default == nil {
		return false
	}
	return !sameValue(f.Type, f.Value, *f.Default)
}

// sameValue — равны ли значения поля; у тумблера «on», «true» и «1» — одно.
func sameValue(kind, a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if kind == "toggle" {
		return truthy(a) == truthy(b)
	}
	return a == b
}

func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "true", "on", "1", "да":
		return true
	}
	return false
}

// rareFragment — тело «Тонкой настройки»: та же сборка экрана, из которой
// foldRare вынул редкое. Поле поиска сверху ищет по подписям, подсказкам и
// названиям групп — как у других ленивых частей, поиск идёт на сервере.
func (s *Server) rareFragment(screen, search string, _ int) ([]Component, error) {
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		return nil, err
	}
	blocks := filterRare(doc.rare, search)
	if len(blocks) == 0 {
		if len(doc.rare) == 0 {
			return []Component{emptyState("Тонкой настройки здесь нет",
				"Редких параметров у этого экрана не осталось", "slider.horizontal.3")}, nil
		}
		return []Component{emptyState("Ничего не нашлось",
			"По «"+search+"» в тонкой настройке этого экрана пусто", "magnifyingglass")}, nil
	}
	savingBar(blocks)
	return blocks, nil
}

// filterRare — оставить из редкого то, что отвечает поиску: форма остаётся,
// если ему отвечает её заголовок (тогда целиком) или хоть одно поле (тогда
// только такие поля). Пустой поиск ничего не отсеивает.
func filterRare(rare []Component, search string) []Component {
	words := strings.Fields(strings.ToLower(search))
	if len(words) == 0 {
		return rare
	}
	matches := func(text string) bool {
		low := strings.ToLower(text)
		for _, w := range words {
			if !strings.Contains(low, w) {
				return false
			}
		}
		return true
	}
	var out []Component
	for _, c := range rare {
		switch {
		case matches(c.Title):
			out = append(out, c)
		case c.Type == "form":
			var fields []Field
			for _, f := range c.Fields {
				if matches(f.Label + " " + f.Hint + " " + f.Name) {
					fields = append(fields, f)
				}
			}
			if len(fields) > 0 {
				c.Fields = fields
				out = append(out, c)
			}
		case len(c.Children) > 0:
			if kids := filterRare(c.Children, search); len(kids) > 0 {
				c.Children = kids
				out = append(out, c)
			}
		}
	}
	return out
}

// ---- поля из реестра ----

// settingField — поле редкой настройки, собранное из её описания в реестре
// (`store.SettingDef`): вид по виду настройки, текущее значение тем же путём,
// каким читает движок, умолчание и границы оттуда же. Ни числа, ни границы
// здесь вписывать не нужно. Имя поля — параметр действия сохранения.
func (s *Server) settingField(key, name, label, hint, unit string) Field {
	def := settingDef(key)
	f := Field{Name: name, Label: label, Hint: hint, Unit: unit, key: key,
		Value: s.eng.Store().SettingShown(def)}
	d := def.ShownDefault()
	f.Default = &d
	switch def.Kind {
	case store.SettingPositive, store.SettingCount:
		f.Type = "number"
		f.Min = float64(def.Min)
		if def.Kind == store.SettingPositive && f.Min < 1 {
			f.Min = 1
		}
		f.Max = float64(def.Max)
	case store.SettingFlag:
		f.Type = "toggle"
	default:
		f.Type = "text"
	}
	return f
}

// rare — пометить поле редким: проход foldRare унесёт его в «Тонкую
// настройку». Нужно полям, чья форма смешанная; форму из одних редких полей
// проще пометить целиком (Component.Tier).
func rare(f Field) Field {
	f.Tier = tierRare
	return f
}

// settingDef — описание настройки из реестра. Настройки, которой там нет, на
// экране быть не может: паника при сборке (и в тесте на сборку всех экранов)
// дешевле поля, чьё умолчание никто не знает.
func settingDef(key string) store.SettingDef {
	def, ok := store.LookupSetting(key)
	if !ok {
		panic("настройка " + key + " не описана в реестре: умолчание у неё должно быть в одном месте")
	}
	return def
}

// settingParam — как параметр действия сохранения связан с настройкой: имя
// поля формы, ключ в реестре и слова для сообщения об ошибке.
type settingParam struct {
	Param string
	Key   string
	What  string
}

// saveSettings — записать настройки, названные в параметрах. Сначала
// проверяем все, потом пишем: половина формы не сохраняется. Непришедшее
// поле — «не трогали», а не «сбросить»: живой клиент шлёт то, что у формы
// есть, и тумблеры идут отдельным списком `toggles`. Стёртое число — не
// «не трогали», а отказ с причиной: молча пропущенная правка выглядела бы
// как сохранённая.
func (s *Server) saveSettings(req actionRequest, list ...settingParam) error {
	type change struct {
		def   store.SettingDef
		value string
	}
	var changes []change
	for _, p := range list {
		raw, ok := req.Params[p.Param]
		if !ok {
			continue
		}
		def := settingDef(p.Key)
		value, err := def.Check(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", p.What, err)
		}
		changes = append(changes, change{def, value})
	}
	for _, c := range changes {
		if err := s.eng.Store().SetSetting(c.def.Key, c.value); err != nil {
			return err
		}
	}
	return nil
}
