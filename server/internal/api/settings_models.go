package api

// «Модели и роли» — раздел настроек и его экраны.
//
// Раздел был самым тяжёлым в настройках (замер 11 сентября 2026: 3597 узлов и
// 319 КБ): десять ролей по три списка, полсотни моделей по восемь фактов,
// цена правилась мини-языком в текстовом вопросе, цены и остаток жили на своей
// вкладке. 1 октября 2026 он разрезан по тому, за чем человек приходит (макет —
// `design/nastroyki-2/Modeli.html`):
//
//	settings:models                       «Роли» — кто отвечает: таблица, под ней служебные роли
//	settings:models~list                  «Модели» — один список с поиском и фильтрами на сервере
//	settings:models~providers             «Провайдеры» — строка на провайдера и «Тонкая настройка»
//	settings:models~issues                «Проблемы» — то, из-за чего работа встанет или посчитается неверно
//	settings:models@new                   «Подключить провайдера»
//	settings:models@<модель>              экран модели: умения, доверие, цена полями, расход, «Опасное»
//	settings:models@provider:<имя>~вкладка  экран провайдера: Ключ и модели · Цены · Остаток
//
// Старые адреса открываются тем же: вкладки раздела не переименованы, а то, что
// лежало на «Провайдерах и ценах», теперь на экране провайдера.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Внутренние вкладки раздела. Роль, модель, провайдер и беда — четыре разных
// вопроса, и человек приходит ровно с одним из них.
const (
	modelsTabRoles     = ""          // Роли — кто именно отвечает, и служебные назначения
	modelsTabList      = "list"      // Модели — что подключено
	modelsTabProviders = "providers" // Провайдеры — ключи, цены и остаток по каждому
	modelsTabIssues    = "issues"    // Проблемы — то, что не работает
)

// Вкладки экрана провайдера.
const (
	providerTabKey     = ""        // Ключ и модели
	providerTabPrices  = "prices"  // Цены
	providerTabBalance = "balance" // Остаток
)

const (
	// modelsNew — «сущность» с этим именем означает экран «Подключить провайдера»
	modelsNew = "new"
	// modelsProviderPrefix — сущность с этим началом — провайдер, а не модель
	modelsProviderPrefix = "provider:"
)

// modelsAddr — разобранный адрес раздела «Модели и роли».
type modelsAddr struct {
	Entity string // пусто — раздел; «new»; «provider:<имя>»; иначе имя модели
	Tab    string
}

func parseModelsAddr(screen string) modelsAddr {
	_, id, tab := splitEntity(screen)
	return modelsAddr{Entity: strings.TrimSpace(id), Tab: tab}
}

// modelAddr — экран модели.
func modelAddr(name string) string { return entityAddr(modelsScreen, name, "") }

// providerAddr — экран провайдера (group — имя из справочника, «на этой машине»
// или имя сервера) на нужной вкладке.
func providerAddr(group, tab string) string {
	return entityAddr(modelsScreen, modelsProviderPrefix+group, tab)
}

// buildModels — «Модели и роли»: раздел с четырьмя вкладками или экран одной
// сущности. Строка поиска и отбор приходят параметрами: список моделей ищет и
// отбирает на сервере по всем моделям, а не по показанным.
func (s *Server) buildModels(doc *Document, screen, filter, search string, limit int) error {
	a := parseModelsAddr(screen)
	doc.Layout = "wide"
	switch {
	case a.Entity == modelsNew:
		return s.buildProviderNew(doc)
	case strings.HasPrefix(a.Entity, modelsProviderPrefix):
		return s.buildProvider(doc, strings.TrimPrefix(a.Entity, modelsProviderPrefix), a.Tab, limit)
	case a.Entity != "":
		return s.buildModel(doc, a.Entity)
	}
	return s.buildModelsSection(doc, a.Tab, filter, search, limit)
}

// modelsOwnSearch — верхнее поле на этом экране ищет модели, а не настройки:
// список моделей отбирает по нему сам, и общий поиск по разделам над ним не нужен.
func modelsOwnSearch(screen string) bool {
	a := parseModelsAddr(screen)
	return a.Entity == "" && a.Tab == modelsTabList
}

// buildModelsSection — раздел: заголовок, четыре вкладки и содержимое выбранной.
func (s *Server) buildModelsSection(doc *Document, tab, filter, search string, limit int) error {
	doc.Heading = "Модели и роли"
	cards := s.eng.ModelCards()
	roles := s.eng.Roles()
	doc.Subheading = modelsByPhrase(cards, roles)
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	findings := s.findingsOf(cards, roles, nil)
	tabs := []innerTab{
		{ID: modelsTabRoles, Name: "Роли"},
		{ID: modelsTabList, Name: "Модели", Count: strconv.Itoa(len(cards))},
		{ID: modelsTabProviders, Name: "Провайдеры"},
		{ID: modelsTabIssues, Name: "Проблемы"},
	}
	if len(findings) > 0 {
		tabs[3].Count, tabs[3].Level = strconv.Itoa(len(findings)), LevelWarn
	}
	setSubtabs(doc, modelsScreen, tab, tabs)
	// «Подключить провайдера» — единственная кнопка шапки раздела и на всех
	// вкладках: шапка не меняется от вкладки к вкладке
	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(modelsScreen, modelsNew, ""),
		Label: "Подключить провайдера", Style: "primary"}}

	switch tab {
	case modelsTabList:
		s.modelsListTab(doc, add, cards, filter, search, limit)
	case modelsTabProviders:
		s.providersTab(add, cards)
	case modelsTabIssues:
		s.issuesTab(add, findings)
	default:
		s.rolesTab(doc, add, cards, roles)
	}
	return nil
}

// peerRoles — роли, которые человек видит строками таблицы и считает глазами.
// Эмбеддер сюда не входит: у роли есть основная модель, запасная и таймаут, а у
// эмбеддера ничего этого нет — он живёт среди служебных ролей.
//
// Одна функция на всех, кто считает роли. До 11 сентября 2026 список строк
// отбрасывал эмбеддера, а счётчик в подписи — нет, и экран объявлял «10 ролей»
// над девятью строками. Считать одно и то же в двух местах разными мерками —
// значит однажды разойтись.
func peerRoles(roles []engine.Role) []engine.Role {
	out := make([]engine.Role, 0, len(roles))
	for _, r := range roles {
		if r.Name == engine.RoleEmbed {
			continue
		}
		out = append(out, r)
	}
	return out
}

// modelsByPhrase — строка под заголовком: сколько моделей, от скольких мест и
// все ли роли укомплектованы.
func modelsByPhrase(cards []engine.ModelCard, roles []engine.Role) string {
	roles = peerRoles(roles)
	places := map[string]bool{}
	for _, c := range cards {
		if c.Provider != "" {
			places[c.Group] = true
		}
	}
	without := 0
	for _, r := range roles {
		if r.Model == "" || r.Model == "нет" {
			without++
		}
	}
	phrase := plural(len(cards), "модель", "модели", "моделей")
	if len(places) > 0 {
		phrase += " от " + plural(len(places), "провайдера", "провайдеров", "провайдеров")
	}
	phrase += ". " + plural(len(roles), "роль", "роли", "ролей")
	if without == 0 {
		return phrase + " — у каждой есть кому отвечать."
	}
	return phrase + fmt.Sprintf(" — у %d из них модели нет, и эта работа не делается.", without)
}

// modelFindings — что здесь не работает, для тех, у кого есть только список
// моделей и роли: имена из списка называют «подключённые», а цены и пропавшие у
// провайдера берутся из карточек.
func (s *Server) modelFindings(models []engine.Model, roles []engine.Role) []Component {
	if len(models) == 0 {
		return s.findingsOf(nil, roles, nil)
	}
	names := make(map[string]bool, len(models))
	for _, m := range models {
		names[m.Name] = true
	}
	var cards []engine.ModelCard
	for _, c := range s.eng.ModelCards() {
		if names[c.Name] {
			cards = append(cards, c)
		}
	}
	return s.findingsOf(cards, roles, names)
}

// findingsOf — что здесь не работает: роль, которая падает, роль, смотрящая на
// модель, которой нет, модели без цены и модели, которых больше нет у
// провайдера. Прежде это была полка «Стоит посмотреть» на первом экране; теперь
// у замечаний своя вкладка со счётчиком, а первый экран занят ролями. Считать
// их всё равно надо до сборки: счётчик у имени вкладки должен стоять раньше, чем
// человек туда зайдёт.
//
// known — какие имена считать подключёнными; nil — те, что в карточках.
func (s *Server) findingsOf(cards []engine.ModelCard, roles []engine.Role, known map[string]bool) []Component {
	var kids []Component
	for _, r := range roles {
		if r.LastErr == "" {
			continue
		}
		// имя модели берём из самой отметки, а не из роли. Отметка — запись о
		// прошлом («DeepseekPro · 09.09 22:37 · …»), а роль с тех пор могли
		// переназначить: 10 сентября 2026 заголовок объявил «deepseek-flash не
		// тянет роль «Чаты»» про сбой, которого у этой модели не было вовсе.
		// Врать про модель нельзя ни в какую сторону
		failed, when, why := parseMark(r.LastErr)
		if failed == "" {
			failed = r.Model
		}
		row := Component{Type: "row",
			Subtitle: failed + " не тянет роль «" + r.Name + "»",
			Caption:  why}
		if when != "" {
			row.Meta = when
		}
		// хвост про запасную — только когда запасная есть. «Запасной у неё нет
		// — работа просто не делается. Молчаливый переход на запасную выглядит
		// как всё работает» человек читал как насмешку
		if r.Spare != "" && r.Spare != "нет" {
			row.Caption += " · работу доделывает запасная " + r.Spare +
				". Молчаливый переход на запасную выглядит как «всё работает» — а платите вы за обе"
		} else {
			row.Caption += " · запасной у неё нет — работа просто не делается"
		}
		// у замечания два действия: пойти к ролям и закрыть его. Какое главное —
		// зависит от того, устарело ли оно: после смены модели закрыть, иначе
		// чинить. Без кнопки «Закрыть» замечание висело неделями, и человек
		// спрашивал «как закрыть» (13 сентября 2026)
		toRoles := Action{Action: "$goto", Target: modelsScreen, Label: "К ролям"}
		dismiss := Action{Action: "dismiss_role_err", Target: r.Name, Label: "Закрыть"}
		if failed != r.Model && r.Model != "" && r.Model != "нет" {
			row.Caption += " · с тех пор на роли стоит " + r.Model +
				" — если она отвечает, замечание устарело"
			row.Badges = []Badge{{Text: "модель сменили", Color: "#8E8E93"}}
			dismiss.Style = "primary"
			row.Actions = []Action{dismiss, toRoles}
		} else {
			toRoles.Style = "primary"
			row.Actions = []Action{toRoles, dismiss}
		}
		kids = append(kids, row)
	}

	// роль смотрит на модель, которой нет вовсе. Так вышло 10 сентября 2026:
	// старые модели DeepSeek удалили, а запасная у роли «Чаты» осталась на
	// «DeepseekFlash». Обращение к ней кончится «модель не подключена» ровно
	// тогда, когда основная подведёт, — то есть в самый неподходящий момент
	has := known
	if has == nil {
		has = map[string]bool{}
		for _, c := range cards {
			has[c.Name] = true
		}
	}
	if len(has) > 0 {
		for _, r := range roles {
			// роль «Эмбеддинги» смотрит на служебную модель эмбеддера (embed_model
			// + свой адрес), а не на подключённую модель, и в общем списке её не
			// бывает по построению: замечание «модели нет в списке» про неё было
			// ложной тревогой (13 сентября 2026)
			if r.Name == engine.RoleEmbed {
				continue
			}
			var missing []string
			if r.Model != "" && r.Model != "нет" && !has[r.Model] {
				missing = append(missing, "основная "+r.Model)
			}
			if r.Spare != "" && r.Spare != "нет" && !has[r.Spare] {
				missing = append(missing, "запасная "+r.Spare)
			}
			if len(missing) == 0 {
				continue
			}
			kids = append(kids, Component{Type: "row", Level: LevelWarn,
				Subtitle: "У роли «" + r.Name + "» модели нет в списке",
				Caption: strings.Join(missing, " и ") + " — такой модели среди подключённых больше нет. " +
					"Обращение к ней кончится отказом; выберите другую в таблице ролей",
				Actions: []Action{{Action: "$goto", Target: modelsScreen, Label: "Выбрать модель", Style: "primary"}}})
		}
	}

	scope := cards
	if gone := goneCards(scope); len(gone) > 0 {
		group := gone[0].Group
		names := make([]string, 0, len(gone))
		for _, c := range gone {
			if c.Group == group {
				names = append(names, c.Name)
			}
		}
		subtitle := "У провайдера больше нет " + plural(len(gone), "модели", "моделей", "моделей")
		caption := strings.Join(firstN(names, 5), ", ")
		if len(names) > 5 {
			caption += fmt.Sprintf(" и ещё %d", len(names)-5)
		}
		caption += " · обращение к ним кончится отказом: обновите список моделей провайдера или уберите их. " +
			"Сама Сёма их не удаляет: модель могла пропасть из списка на час"
		kids = append(kids, Component{Type: "row", Level: LevelWarn, Subtitle: subtitle, Caption: caption,
			Actions: []Action{{Action: "$goto", Target: providerAddr(group, providerTabKey), Label: "Открыть провайдера", Style: "primary"}}})
	}

	if missing, group := noPriceByGroup(scope); missing > 0 {
		// с вызовами — тот же пробел, но уже в записанном счёте: расход этих
		// моделей на «Здоровье» и в бюджетах записан нулём
		subtitle := "У " + plural(missing, "модели", "моделей", "моделей") + " не названа цена"
		caption := "их расход на «Здоровье» и в бюджетах считается нулём. Цены приносит проверка: страница провайдера или его JSON, " +
			"Сёма разбирает и предлагает карточкой в «Уведомлениях», принимаете вы"
		if spend := s.eng.NoPriceWithSpend(7); len(spend) > 0 {
			subtitle = "Расход не считается: нет цены у " + plural(len(spend), "модели", "моделей", "моделей") + ", а вызовы за неделю были"
			caption = strings.Join(firstN(spend, 5), ", ") + " · " + caption
		}
		kids = append(kids, Component{Type: "row", Level: LevelWarn, Subtitle: subtitle, Caption: caption,
			Actions: []Action{{Action: "$goto", Target: providerAddr(group, providerTabPrices), Label: "Проверить цены", Style: "primary"}}})
	}
	// у каждого замечания есть действие, и оно ведёт прямо к настройке:
	// «посмотрите ниже» после переезда на свою вкладку было бы просто неправдой —
	// ниже там ничего нет. Переход помечен главным, иначе он не рисуется кнопкой,
	// а строка «кликается» молча — и человек не видит, что здесь вообще можно
	// нажать
	for i := range kids {
		if len(kids[i].Actions) == 0 {
			kids[i].Actions = []Action{{Action: "$goto", Target: modelsScreen, Label: "К ролям", Style: "primary"}}
		}
	}
	return kids
}

// goneCards — модели, которых провайдер больше не предлагает, сгруппированные
// по провайдеру: у первой группы они и показываются.
func goneCards(cards []engine.ModelCard) []engine.ModelCard {
	var out []engine.ModelCard
	for _, c := range cards {
		if c.Gone {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out
}

// noPriceByGroup — сколько моделей без цены всего и у какого провайдера их
// больше всего: туда и ведёт замечание.
func noPriceByGroup(cards []engine.ModelCard) (total int, worst string) {
	count := map[string]int{}
	for _, c := range cards {
		if c.NoPrice() {
			total++
			count[c.Group]++
		}
	}
	best := 0
	for group, n := range count {
		if n > best || (n == best && group < worst) {
			best, worst = n, group
		}
	}
	return total, worst
}

// issuesTab — «Проблемы»: только то, что не работает.
func (s *Server) issuesTab(add func(Component), findings []Component) {
	if len(findings) == 0 {
		add(emptyState("Всё отвечает",
			"Здесь появятся роли без модели, роли, чья модель молчит, модели, которых больше нет у провайдера, и модели без цены — то, из-за чего работа встанет или посчитается неверно", "checkmark.circle"))
		return
	}
	add(Component{Type: "section", Title: "Стоит посмотреть · " +
		plural(len(findings), "замечание", "замечания", "замечаний"), Children: findings})
	add(Component{Type: "text", Style: "secondary",
		Text: "Сюда попадают роли без модели, роли, чья модель молчит, модели, которых больше нет у провайдера, " +
			"и модели без цены — то, из-за чего работа встанет или посчитается неверно. Счётчик у вкладки виден до захода."})
}

// parseMark — «модель · 09.09 22:37 · причина» на части. Формат ставит
// noteRoleFail; старые отметки без разделителей возвращаются причиной целиком,
// а не выдуманной моделью.
func parseMark(mark string) (model, when, why string) {
	parts := strings.SplitN(mark, " · ", 3)
	if len(parts) < 3 {
		return "", "", mark
	}
	// вторая часть обязана быть похожа на время: иначе это не наш формат, и
	// первая часть — вовсе не модель
	if !looksLikeTime(parts[1]) {
		return "", "", mark
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
}

func looksLikeTime(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != len("02.01 15:04") {
		return false
	}
	for _, i := range []int{2, 8} {
		if s[i] != '.' && s[i] != ':' {
			return false
		}
	}
	return s[5] == ' '
}

// firstN — не больше n первых строк списка.
func firstN(list []string, n int) []string {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// ---- подписи моделей с провайдером ----

// modelsWithProvider — имена моделей с приписанным провайдером и способ подписать
// одно имя так же.
//
// Значение выбора должно совпадать с одним из вариантов, иначе выпадашка
// показывает пустоту вместо назначенной модели, — поэтому подпись одна и та же
// и там, и там. Обратно имя достаёт `modelName` при сохранении: хранить в роли
// «имя · провайдер» нельзя, роль указывает на модель.
func (s *Server) modelsWithProvider() ([]string, func(string) string) {
	return s.modelLabels(s.eng.ModelCards())
}

// modelLabels — то же по готовым карточкам: у модели с чужого шлюза вместо
// адреса стоит читаемое имя группы («на этой машине», «llm:8080»). Описанная
// файлом запуска подписывается голым именем, как и раньше.
func (s *Server) modelLabels(cards []engine.ModelCard) ([]string, func(string) string) {
	group := make(map[string]string, len(cards))
	for _, c := range cards {
		if !c.FromFile && c.Group != "" {
			group[c.Name] = c.Group
		}
	}
	label := func(name string) string {
		if name == "" || name == "нет" {
			return name
		}
		if g := group[name]; g != "" {
			return name + " · " + g
		}
		return name
	}
	names := s.eng.ProviderNames()
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, label(name))
	}
	return out, label
}

// modelName — обратно из «имя · провайдер» в имя. Разделитель тот же, что в
// списке моделей каталога: два разных здесь однажды разойдутся.
func modelName(v string) string {
	name, _, _ := strings.Cut(v, " · ")
	return strings.TrimSpace(name)
}

// withProvider / modelsWithProviderList — то же самое для мест, где нужен один
// список или одна подпись: экран чата, служебные роли.
func withProvider(s *Server, name string) string {
	_, label := s.modelsWithProvider()
	return label(name)
}

func modelsWithProviderList(s *Server) []string {
	list, _ := s.modelsWithProvider()
	return list
}
