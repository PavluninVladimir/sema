package api

// Провайдеры: вкладка раздела, экран одного провайдера и «Подключить
// провайдера».
//
// Десять форм подряд (по форме на провайдера, у каждой ещё источник цен и
// остаток) стали пятью строками, а у каждого провайдера — свой экран с тремя
// вкладками: «Ключ и модели», «Цены», «Остаток» (макет —
// `design/nastroyki-2/Modeli.html`). Ключ задан или нет видно в строке, а его
// значение не попадает на экран никогда: оно только заменяется.
//
// Адрес провайдера — `settings:models@provider:<имя>~вкладка`. Имя — как в
// справочнике («OpenAI»), у моделей с чужого шлюза — «на этой машине» или имя
// сервера: адрес вместо имени человеку ничего не говорит, а у соседних серверов
// одной машины (llm:8080, embed:8080) одна забота — письма отсюда не уходят.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Короткие подписи видов источников для сегментов: длинные («Страница цен
// (разбирает модель)») в сегмент не помещаются, и их смысл — в подсказке.
var priceKindShort = []struct{ kind, title string }{
	{engine.PricePage, "страница"},
	{engine.PriceAPI, "JSON"},
	{engine.PriceReported, "называет сам"},
	{engine.PriceNone, "не брать"},
}

var balanceKindShort = []struct{ kind, title string }{
	{engine.BalanceAPI, "у провайдера"},
	{engine.BalanceEstimate, "оценка"},
	{engine.BalanceNone, "не показывать"},
}

// kindByAnyTitle — вид по длинной подписи, короткой или самому слову: старые
// карточки и скрипты присылают одно, сегменты экрана — другое.
func kindByAnyTitle(long, short []struct{ kind, title string }, title string) string {
	if kind := kindByTitle(long, title); kind != "" {
		return kind
	}
	return kindByTitle(short, title)
}

// providerInfo — провайдер вместе с его моделями.
type providerInfo struct {
	Group   string
	Conf    engine.ProviderConf
	HasConf bool // есть в справочнике; у «на этой машине» и серверов по адресу — нет
	Cards   []engine.ModelCard
}

// priced — у провайдера есть цены и остаток: деньги платят за вызовы его
// моделей. Клиенты по подписке и fal.ai (там цену картинки подбирает сам
// Сёма) в счёт не входят.
func (i providerInfo) priced() bool {
	return i.HasConf && i.Conf.NeedKey && i.Conf.Kind != "cli" && i.Conf.Kind != "fal" && len(i.Cards) > 0
}

// noPrice — сколько его моделей без цены.
func (i providerInfo) noPrice() int {
	n := 0
	for _, c := range i.Cards {
		if c.NoPrice() {
			n++
		}
	}
	return n
}

// providerInfos — провайдеры, о которых есть что сказать: с подключёнными
// моделями или с заданным ключом, плюс группы моделей по адресу. Остальные из
// справочника — кандидаты в «Подключить провайдера», а не строки списка.
func (s *Server) providerInfos(cards []engine.ModelCard) []providerInfo {
	by := map[string][]engine.ModelCard{}
	for _, c := range cards {
		by[c.Group] = append(by[c.Group], c)
	}
	var out []providerInfo
	seen := map[string]bool{}
	for _, p := range s.eng.Providers() {
		seen[p.Name] = true
		_, keySet := s.eng.ProviderKeySet(p.Name)
		if len(by[p.Name]) == 0 && !keySet {
			continue
		}
		out = append(out, providerInfo{Group: p.Name, Conf: p, HasConf: true, Cards: by[p.Name]})
	}
	// важное сверху: у кого модели стоят в ролях, потом у кого их больше
	// (порядок групп тот же, что в списке моделей); без моделей — в конце
	rank := map[string]int{}
	for i, g := range groupCards(cards) {
		rank[g.Name] = i + 1
		if !seen[g.Name] {
			out = append(out, providerInfo{Group: g.Name, Cards: g.Cards})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank[out[i].Group], rank[out[j].Group]
		switch {
		case ri != 0 && rj != 0:
			return ri < rj
		case (ri == 0) != (rj == 0):
			return ri != 0
		}
		return out[i].Group < out[j].Group
	})
	return out
}

// ---- вкладка «Провайдеры» ----

// providersTab — по строке на провайдера и «Тонкая настройка» с тем, что общее
// для всех.
func (s *Server) providersTab(add func(Component), cards []engine.ModelCard) {
	infos := s.providerInfos(cards)
	balances := map[string]engine.Balance{}
	for _, b := range s.eng.BalancesFromCache() {
		balances[b.Provider] = b
	}
	rows := make([]Component, 0, len(infos))
	for _, info := range infos {
		rows = append(rows, s.providerRow(info, balances))
	}
	if len(rows) == 0 {
		empty := emptyState("Провайдеров пока нет",
			"Провайдер — это тот, чьими руками отвечают модели: OpenAI, Anthropic, DeepSeek, свой сервер. Один ключ — все его модели сразу", "tray")
		empty.Actions = []Action{{Action: "$goto", Target: entityAddr(modelsScreen, modelsNew, ""), Label: "Подключить провайдера", Style: "primary"}}
		rows = append(rows, empty)
	}
	add(Component{Type: "section", ID: "providers-list", Title: "Провайдеры · " + strconv.Itoa(len(infos)), Children: rows})
	s.providersFine(add)
}

// providerRow — тонкая строка провайдера: имя, капсула про ключ, одна фраза и
// переход на его экран.
func (s *Server) providerRow(info providerInfo, balances map[string]engine.Balance) Component {
	letter, color := providerTile(info.Group)
	row := Component{Type: "row", Layout: "wide", ID: "provider-" + info.Group, Subtitle: groupTitle(info.Group),
		Avatar: letter, AvatarColor: color, AvatarShape: "square",
		Actions: []Action{{Action: "$goto", Target: providerAddr(info.Group, providerTabKey), Label: info.Group}}}
	parts := []string{}
	if len(info.Cards) > 0 {
		parts = append(parts, plural(len(info.Cards), "модель", "модели", "моделей"))
	}
	if info.HasConf && info.Conf.NeedKey {
		if _, set := s.eng.ProviderKeySet(info.Group); set {
			row.Badges = append(row.Badges, Badge{Text: "ключ задан", Level: LevelOK})
		} else {
			row.Badges = append(row.Badges, Badge{Text: "нет ключа", Level: LevelWarn})
			withLevel(&row, LevelWarn)
		}
	} else if len(info.Cards) > 0 {
		parts = append(parts, "ключ не нужен")
	}
	switch {
	case info.priced():
		parts = append(parts, s.priceSummary(info.Conf))
		parts = append(parts, balanceSummary(info.Conf, balances[info.Group]))
		if n := info.noPrice(); n > 0 {
			row.Badges = append(row.Badges, Badge{Text: fmt.Sprintf("%d без цены", n), Level: LevelWarn})
		}
	case info.HasConf && info.Conf.Kind == "fal":
		parts = append(parts, "рисование картинок: самую дешёвую модель Сёма подбирает сам")
	case info.HasConf && info.Conf.Kind == "cli":
		parts = append(parts, "оплата подпиской, лимиты подписки")
	case !info.HasConf:
		parts = append(parts, "цена 0")
	}
	row.Caption = strings.Join(parts, " · ")
	return row
}

// priceSummary — откуда цены и когда проверяли, одной фразой.
func (s *Server) priceSummary(p engine.ProviderConf) string {
	word := "цены не берутся"
	switch p.PriceKind {
	case engine.PricePage:
		word = "цены со страницы"
	case engine.PriceAPI:
		word = "цены из JSON"
	case engine.PriceReported:
		return "цену называет сам"
	}
	if last, ok := s.eng.LastPriceCheck(p.Name); ok && last.OK {
		if t, err := time.Parse(time.RFC3339, last.At); err == nil {
			word += ", проверены " + whenText(t.Format(time.RFC3339))
		}
	}
	return word
}

// balanceSummary — остаток коротко: цифра, оценка или «не спрашивается».
func balanceSummary(p engine.ProviderConf, b engine.Balance) string {
	switch p.BalanceKind {
	case engine.BalanceAPI, engine.BalanceEstimate:
		switch {
		case b.Provider == "" || (b.Kind == engine.BalanceAPI && b.At.IsZero() && b.Err == ""):
			return "остаток ещё не спрашивали"
		case b.Kind == engine.BalanceEstimate && len(b.Amounts) == 0:
			// оценка без «было»: ей не от чего считать, и фраза про это, а не
			// пояснение оценки целиком
			return "остаток не оценивается: впишите, сколько на счёте"
		}
		return "остаток " + b.Text()
	}
	return "остаток не спрашивается"
}

// providersFine — «Тонкая настройка» вкладки: то, что общее для всех
// провайдеров и правится раз в год. Умолчания — из реестра настроек.
func (s *Server) providersFine(add func(Component)) {
	skip := s.settingField("prices.skip_sections", "words", "Разделы страницы цен, которые не берём",
		"слова в заголовках разделов, через запятую: прайс делит цены на основные и дополнительные услуги, и разбор однажды взял бы соседнюю ячейку. "+
			"Раздел, в заголовке которого есть такое слово (и всё под ним), в разбор не идёт: там цены не про обычный вызов модели — "+
			"дообучение, пакетный и гибкий тарифы, инструменты. Пусто — вернуть список по умолчанию", "")
	limit := s.settingField("models.max_output", "max_output", "Предел ответа модели",
		"моделям Anthropic через Agent API он обязателен — без него отказ 400; у других провайдеров он, наоборот, мешает, и тогда 0", "токенов")
	add(Component{Type: "section", ID: "providers-common", Title: "Общее для всех провайдеров", Tier: tierRare, Children: []Component{
		{Type: "form", ID: "prices-skip", Style: formRows, Fields: []Field{skip},
			Submit: &Action{Action: "save_price_skip", Label: "Сохранить"}},
		{Type: "form", ID: "models-limits", Style: formRows, Fields: []Field{limit},
			Submit: &Action{Action: "set_models_limits", Label: "Сохранить"}},
	}})
}

// ---- экран провайдера ----

// buildProvider — экран одного провайдера.
func (s *Server) buildProvider(doc *Document, group, tab string, limit int) error {
	doc.Layout = "read"
	cards := s.eng.ProviderModels(group)
	conf, hasConf := s.eng.Provider(group)
	if !hasConf && len(cards) == 0 {
		doc.Heading = "Провайдеры"
		doc.Components = append(doc.Components,
			Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: modelsScreen + "~" + modelsTabProviders, Label: "‹ Провайдеры"}}},
			emptyState("Провайдера «"+group+"» нет", "Его нет в справочнике, и моделей у него не осталось. Список — по кнопке «‹ Провайдеры» выше", "tray"))
		return nil
	}
	info := providerInfo{Group: group, Conf: conf, HasConf: hasConf, Cards: cards}
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	// вкладки цен и остатка есть только там, где за вызовы платят
	var tabs []innerTab
	if info.priced() {
		tabs = []innerTab{{ID: providerTabKey, Name: "Ключ и модели"}, {ID: providerTabPrices, Name: "Цены"}, {ID: providerTabBalance, Name: "Остаток"}}
		if n := info.noPrice(); n > 0 {
			tabs[1].Count, tabs[1].Level = strconv.Itoa(n), LevelWarn
		}
		if lvl := s.balanceLevel(conf); lvl != "" {
			tabs[2].Count, tabs[2].Level = "!", lvl
		}
	} else {
		tab = providerTabKey
	}
	switch tab {
	case providerTabPrices:
		s.providerPrices(add, conf, info)
	case providerTabBalance:
		s.providerBalance(add, conf)
	default:
		tab = providerTabKey
		s.providerKey(add, info, limit)
	}

	letter, color := providerTile(group)
	e := entity{Section: modelsScreen, BackTo: modelsScreen + "~" + modelsTabProviders, Back: "Провайдеры",
		ID: modelsProviderPrefix + group, Name: groupTitle(group), Avatar: letter, AvatarColor: color,
		Caption: s.providerCaption(info), Tab: tab, Tabs: tabs}
	_, keySet := s.eng.ProviderKeySet(group)
	switch {
	case hasConf && conf.NeedKey && !keySet:
		e.Level = LevelWarn
		e.Badges = []Badge{{Text: "нет ключа", Level: LevelWarn}}
		primary := Action{Action: "set_provider_key", Target: group, Label: "Задать ключ", Style: "primary",
			Prompt: "Ключ API для «" + group + "» · одним ключом работают все его модели", Secure: true}
		e.Primary = &primary
	case hasConf && conf.Catalog && conf.Kind != "cli" && conf.Kind != "fal":
		primary := Action{Action: "connect_provider_models", Target: group, Label: "Обновить список моделей"}
		e.Primary = &primary
	}
	if info.priced() {
		if conf.PriceKind == engine.PricePage || conf.PriceKind == engine.PriceAPI {
			e.Menu = append(e.Menu, Action{Action: "check_prices", Target: group, Label: "Проверить цены"})
		}
		if conf.BalanceKind == engine.BalanceAPI {
			e.Menu = append(e.Menu, Action{Action: "refresh_balance", Target: group, Label: "Спросить остаток"})
		}
	}
	entityFrame(doc, e)
	return nil
}

// providerCaption — одна строка фактов шапки: сколько моделей, ключ, остаток.
func (s *Server) providerCaption(info providerInfo) string {
	var parts []string
	parts = append(parts, plural(len(info.Cards), "модель", "модели", "моделей"))
	switch {
	case info.HasConf && info.Conf.NeedKey:
		if _, set := s.eng.ProviderKeySet(info.Group); set {
			parts = append(parts, "ключ задан")
		} else {
			parts = append(parts, "ключа нет")
		}
	default:
		parts = append(parts, "ключ не нужен")
	}
	if info.priced() {
		for _, b := range s.eng.BalancesFromCache() {
			if b.Provider == info.Group && (b.Kind == engine.BalanceAPI || b.Kind == engine.BalanceEstimate) {
				parts = append(parts, balanceSummary(info.Conf, b))
			}
		}
	}
	return strings.Join(parts, " · ")
}

// balanceLevel — остаток вызывает беспокойство: уровень для счётчика у вкладки.
func (s *Server) balanceLevel(p engine.ProviderConf) string {
	for _, b := range s.eng.BalancesFromCache() {
		if b.Provider != p.Name {
			continue
		}
		switch {
		case b.Err != "":
			return LevelWarn
		case b.Kind == engine.BalanceAPI && !b.Enough && !b.At.IsZero():
			return LevelAlert
		case b.Low > 0 && len(b.Amounts) > 0 && b.Amounts[0].Total < b.Low:
			return LevelWarn
		}
	}
	return ""
}

// providerKey — «Ключ и модели».
func (s *Server) providerKey(add func(Component), info providerInfo, limit int) {
	var rows []Component
	if info.HasConf && info.Conf.NeedKey {
		created, set := s.eng.ProviderKeySet(info.Group)
		row := Component{Type: "row", Layout: "wide", ID: "provkey-" + info.Group, Subtitle: "Ключ API",
			Caption: "одним ключом работают все модели провайдера; значение не показывается — только заменяется"}
		if info.Conf.Hint != "" {
			row.Caption += " · " + info.Conf.Hint
		}
		label := "Задать…"
		if set {
			text := "задан"
			if created != "" {
				text += " · " + whenText(created)
			}
			row.Badges = []Badge{{Text: text, Level: LevelOK}}
			label = "Заменить…"
		} else {
			row.Badges = []Badge{{Text: "не задан", Level: LevelWarn}}
			withLevel(&row, LevelWarn)
		}
		row.Actions = []Action{{Action: "set_provider_key", Target: info.Group, Label: label,
			Prompt: "Ключ API для «" + info.Group + "» · одним ключом работают все его модели", Secure: true}}
		rows = append(rows, row)
	} else {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "provkey-" + info.Group, Subtitle: "Ключ",
			Caption: noKeyWhy(info), Badges: []Badge{{Text: "не нужен"}}})
	}
	if len(info.Cards) > 0 && !(info.HasConf && info.Conf.Kind == "cli") {
		search := "выключен у всех"
		on := false
		for _, c := range info.Cards {
			on = on || c.Search
		}
		if on {
			search = "включён"
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "provsearch-" + info.Group,
			Subtitle: "Поиск в сети у всех моделей", Toggle: boolToggle(on),
			Caption: "разом у всех моделей провайдера (сейчас " + search + "); у одной — на её экране",
			Actions: []Action{{Action: "toggle_provider_search", Target: info.Group, Label: "toggle"}}})
	}
	add(Component{Type: "section", ID: "provider-key", Children: rows})

	if info.HasConf && info.Conf.Kind == "fal" {
		add(Component{Type: "text", Style: "secondary",
			Text: "fal.ai рисует картинки к постам, а не отвечает в чате: его модели в роли не попадают. " +
				"Ключ нужен, а самую дешёвую модель Сёма подберёт сам по ценам fal.ai — выбор и «Подобрать заново» на вкладке «Роли»."})
	}
	s.providerModels(add, info, limit)
	if line := s.lastCheckLine(info.Group); line != "" {
		add(Component{Type: "text", Style: "secondary", Text: line})
	}
	if info.HasConf && len(info.Cards) > 0 {
		add(Component{Type: "section", ID: "provider-danger", Title: "Опасное", Style: "danger", Children: []Component{{
			Type: "row", Layout: "wide", ID: "danger-remove-models", Subtitle: "Отключить все модели",
			Caption: "ключ останется, модели можно подключить снова одной кнопкой",
			Actions: []Action{{Action: "remove_provider_models", Target: info.Group, Label: "Отключить…", Style: "destructive",
				Confirm:   "Отключить все модели «" + info.Group + "»? Ключ останется, модели можно подключить снова одной кнопкой.",
				ConfirmOK: "Отключить"}}}}})
	}
}

// noKeyWhy — почему ключ не нужен: модель на своей машине или клиент по
// подписке.
func noKeyWhy(info providerInfo) string {
	if info.HasConf && info.Conf.Kind == "cli" {
		return "клиент по подписке на этой машине: оплата подпиской, ключ не нужен"
	}
	if info.Group == engine.GroupThisMachine {
		return "серверы моделей на этой же машине: ключ не нужен, цена 0, письма отсюда не уходят"
	}
	return "сервер без ключа: адрес в справочнике провайдеров или у самой модели"
}

// providerModels — модели провайдера тонкими строками, порцией: у OpenAI их под
// сотню, и все строки разом — это те же тысячи узлов, что были.
func (s *Server) providerModels(add func(Component), info providerInfo, limit int) {
	if len(info.Cards) == 0 {
		if info.HasConf && info.Conf.Kind != "fal" && info.Conf.Kind != "cli" {
			add(Component{Type: "section", Title: "Модели · 0", Children: []Component{emptyState("Моделей пока нет",
				"Ключ задан, а модели ещё не подключены: «Обновить список моделей» в шапке спросит у провайдера и подключит все", "tray")}})
		}
		return
	}
	page := 20
	if limit > 0 && limit != defaultRows {
		page = limit
	}
	list := groupCards(info.Cards)[0].Cards
	shown := list
	if len(shown) > page {
		shown = shown[:page]
	}
	rows := make([]Component, 0, len(shown))
	for _, c := range shown {
		rows = append(rows, s.modelRow(c))
	}
	title := "Модели · " + strconv.Itoa(len(list))
	if len(shown) < len(list) {
		title = fmt.Sprintf("Модели · %d из %d", len(shown), len(list))
	}
	add(Component{Type: "section", ID: "provider-models", Title: title, Children: rows})
	if len(shown) < len(list) {
		add(Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(shown), len(list))}}})
	}
}

// providerPrices — «Цены»: откуда берутся, когда проверяли и что вышло. Ничего
// не подставляется само: изменившаяся цена приходит карточкой в «Уведомления»,
// принимает человек.
func (s *Server) providerPrices(add func(Component), p engine.ProviderConf, info providerInfo) {
	kinds := make([]string, 0, len(priceKindShort))
	for _, k := range priceKindShort {
		// «называет сам» — у тех, кто и правда называет цену в каждом ответе
		if k.kind == engine.PriceReported && p.Kind != "perplexity" && p.PriceKind != engine.PriceReported {
			continue
		}
		kinds = append(kinds, k.title)
	}
	form := Component{Type: "form", ID: "psrc-" + p.Name, Style: formRows, Fields: []Field{
		{Type: "segments", Name: "kind", Label: "Источник цен", Options: kinds, Value: kindTitle(priceKindShort, p.PriceKind),
			Hint: "страница — её читает модель роли «Модули», а ответ проверяет код: каждое число должно стоять на странице рядом с именем модели; " +
				"JSON — список моделей с ценой за токен; «называет сам» — цену провайдер присылает в каждом ответе; «не брать» — цены вписываются руками"},
		{Type: "text", Name: "url", Label: "Адрес страницы или JSON с ценами", Value: p.PriceURL,
			Hint: "нужен странице и JSON; цены сами не подставляются — приходит карточка в «Уведомления», принимаете вы"},
	}, Submit: &Action{Action: "save_price_source", Target: p.Name, Label: "Сохранить"}}
	rows := []Component{s.priceStatusRow(p)}
	if p.PriceKind == engine.PricePage || p.PriceKind == engine.PriceAPI {
		rows = append(rows, s.priceTokenRow(p))
	}
	if task := s.priceWatchRow(); task != nil {
		rows = append(rows, *task)
	}
	add(Component{Type: "section", ID: "provider-prices", Children: []Component{form}})
	add(Component{Type: "section", ID: "provsrc-" + p.Name, Title: "Что нашла проверка", Children: rows})
}

// priceStatusRow — когда проверяли, что вышло и кнопка «Проверить цены».
func (s *Server) priceStatusRow(p engine.ProviderConf) Component {
	total, without := s.eng.PriceCoverage(p.Name)
	facts := []Prop{{Label: "Моделей без цены", Value: fmt.Sprintf("%d из %d", without, total)}}
	level, caption := LevelInfo, "цены ещё не проверяли"
	if last, ok := s.eng.LastPriceCheck(p.Name); ok {
		if t, err := time.Parse(time.RFC3339, last.At); err == nil {
			facts = append(facts, Prop{Label: "Проверено", Value: whenText(t.Format(time.RFC3339))})
		}
		caption = last.Line
		switch {
		case !last.OK:
			level = LevelWarn
		case last.New+last.Changed+last.Mine > 0:
			level = LevelInfo
		default:
			level = LevelOK
		}
	}
	row := Component{Type: "row", Layout: "wide", ID: "pricerow-" + p.Name, Subtitle: "Цены",
		Level: level, Dot: levelDot(level), Facts: facts}
	switch {
	case s.eng.PriceChecking(p.Name):
		caption = "проверяется… итог придёт карточкой в «Уведомления» и строкой здесь"
	case p.PriceKind == engine.PriceReported:
		caption, row.Level = "цену провайдер называет сам в каждом ответе — сверять нечего", LevelOK
		row.Dot = levelDot(LevelOK)
	case p.PriceKind == engine.PriceNone || p.PriceKind == "":
		caption = "цены у этого провайдера не берутся; выберите источник выше, и проверка станет доступна"
	default:
		row.Actions = []Action{{Action: "check_prices", Target: p.Name, Label: "Проверить цены", Style: "primary"}}
	}
	row.Caption = caption
	return row
}

// priceWatchRow — слежение за ценами раз в сутки: общая задача сердцебиения,
// выключенная по умолчанию. Переключается тем же действием, что и на «Задачах».
func (s *Server) priceWatchRow() *Component {
	tasks, err := s.eng.Store().Tasks()
	if err != nil {
		return nil
	}
	for _, t := range tasks {
		if t.System != engine.WorkerPrices {
			continue
		}
		return &Component{Type: "row", Layout: "wide", ID: "price-watch", Subtitle: "Слежение раз в сутки",
			Caption: "задача «" + t.Title + "» — общая для всех провайдеров; выключена, пока вы её не включите",
			Toggle:  boolToggle(t.Enabled),
			Actions: []Action{{Action: "toggle_task", Target: strconv.FormatInt(t.ID, 10), Label: "toggle"}}}
	}
	return nil
}

// providerBalance — «Остаток»: откуда он, порог предупреждения и «на счёте было
// столько-то на такую-то дату» руками. Показывается с оговоркой: оценка — не
// слово провайдера.
func (s *Server) providerBalance(add func(Component), p engine.ProviderConf) {
	add(Component{Type: "section", ID: "provider-balance", Children: []Component{s.balanceForm(p)}})
	add(Component{Type: "section", ID: "balance-state", Title: "Сейчас", Children: s.balanceRow(p)})
}
