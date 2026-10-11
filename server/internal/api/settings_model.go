package api

// Экран одной модели: `settings:models@<имя>`. Шапку (путь назад, закреплённая
// строка с именем, фактами, главной кнопкой и «···») рисует общий entityFrame,
// содержимое — здесь:
//
//	Что не так   — только когда есть: пропала у провайдера, нет ключа, роль отдыхает
//	Что умеет    — видит, ищет, слышит; чем подключена
//	Доверие      — «Доверять закрытое» с объяснением и что видит модель сейчас
//	Цена         — вход, выход, кэш, за минуту звука, часы пик полями, а не строкой
//	Расход       — за сутки и за неделю
//	Опасное      — удалить модель
//
// Всё, что правится, — тумблеры строк (каждый применяется сразу, как раньше) и
// одна форма цены с полосой сохранения. Решения принимает движок, а не экран:
// «доверенная» — это утверждение человека (SetModelTrust), цена пишется той же
// строкой, что и раньше (SetModelPrice), а читать её обратно в поля умеет
// engine.PricePartsOfLine. Второй копии этих правил здесь нет.

import (
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// providerColors — цвета плиток провайдеров: узнаются глазом в списке.
var providerColors = map[string]string{
	"OpenAI": "#17181C", "Perplexity": "#229ED9", "DeepSeek": "#2F6FEB", engine.FalProvider: "#7A5AE0",
	"Anthropic": "#C96442", "Google Gemini": "#1A73E8", "OpenRouter": "#6B5BD7", "Groq": "#E0533D",
	engine.GroupThisMachine: "#2E9A5B",
}

// providerTile — буква и цвет плитки провайдера. Своего цвета нет — берётся из
// имени: один и тот же провайдер всегда одного цвета.
func providerTile(group string) (letter, color string) {
	letter = firstRuneOfName(groupTitle(group))
	if group == engine.GroupThisMachine {
		return "⌂", providerColors[group]
	}
	if c, ok := providerColors[group]; ok {
		return letter, c
	}
	palette := []string{"#2F6FEB", "#7A5AE0", "#2E9A5B", "#C93B6B", "#B7791F", "#0E8A8A", "#8E8E93"}
	sum := 0
	for _, r := range group {
		sum += int(r)
	}
	return letter, palette[sum%len(palette)]
}

// modelCaption — одна строка фактов шапки: чья, где занята.
func modelCaption(c engine.ModelCard) string {
	parts := []string{groupTitle(c.Group)}
	if len(c.Roles) > 0 {
		parts = append(parts, "в ролях: "+strings.Join(c.Roles, ", "))
	}
	if len(c.SpareOf) > 0 {
		parts = append(parts, "запасная у «"+strings.Join(c.SpareOf, "», «")+"»")
	}
	if !c.InRoles() {
		parts = append(parts, "не занята")
	}
	return strings.Join(parts, " · ")
}

// yesNoBadge — «да» или «нет» капсулой: у строки, которую здесь не правят.
func yesNoBadge(on bool, yes, no string) []Badge {
	if on {
		return []Badge{{Text: yes}}
	}
	return []Badge{{Text: no}}
}

// buildModel — экран модели.
func (s *Server) buildModel(doc *Document, name string) error {
	doc.Layout = "read"
	card, ok := s.modelCardOf(doc, name)
	if !ok {
		// модель могли удалить, пока экран был открыт: не ошибка, а объяснение
		doc.Heading = "Модели"
		doc.Components = append(doc.Components,
			Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: modelsScreen + "~" + modelsTabList, Label: "‹ Модели"}}},
			emptyState("Модели «"+name+"» нет", "Её удалили или переименовали. Список моделей — по кнопке «‹ Модели» выше", "tray"))
		return nil
	}
	roles := s.rolesOf(doc)
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	if alerts := s.modelAlerts(card, roles); len(alerts) > 0 {
		add(Component{Type: "section", ID: "model-alerts", Children: alerts})
	}
	s.modelAbilities(add, card)
	s.modelTrust(add, card)
	s.modelPrice(add, card)
	// расход — цифры из журнала, не настройка; индексу они не нужны, а на каждую
	// из ста семидесяти моделей это два запроса к журналу
	if !doc.offline {
		s.modelSpend(add, card)
	} else {
		doc.partial = true
	}
	if !card.FromFile {
		danger := Component{Type: "row", Layout: "wide", ID: "danger-delete", Subtitle: "Удалить модель",
			Caption: "Письма и архив не пострадают." + roleLossWarning(card),
			Actions: []Action{{Action: "remove_model", Target: card.Name, Label: "Удалить…", Style: "destructive",
				Confirm:   "Удалить модель " + card.Name + "? Письма и архив не пострадают." + roleLossWarning(card),
				ConfirmOK: "Удалить модель"}}}
		add(Component{Type: "section", ID: "model-danger", Title: "Опасное", Style: "danger", Children: []Component{danger}})
	}

	letter, color := providerTile(card.Group)
	e := entity{Section: modelsScreen, BackTo: modelsScreen + "~" + modelsTabList, Back: "Модели",
		ID: card.Name, Name: card.Name, Avatar: letter, AvatarColor: color, Caption: modelCaption(card)}
	switch {
	case card.Gone:
		e.Level = LevelWarn
		e.Badges = []Badge{{Text: "нет у провайдера", Level: LevelAlert}}
	case card.Problem != "":
		e.Level = LevelWarn
		e.Badges = []Badge{{Text: card.Problem, Level: LevelWarn}}
	case card.NoPrice():
		e.Badges = []Badge{{Text: "цена не названа", Level: LevelWarn}}
	}
	check := Action{Action: "check_model", Target: card.Name, Label: "Проверить"}
	e.Primary = &check
	e.Menu = []Action{
		{Action: "$goto", Target: providerAddr(card.Group, providerTabKey), Label: "Провайдер «" + groupTitle(card.Group) + "»"},
		{Action: "$goto", Target: "health", Label: "Расход на «Здоровье»"},
	}
	entityFrame(doc, e)
	return nil
}

// modelAlerts — что не так с моделью, а не вообще: пропала у провайдера, нет
// ключа, роль отдыхает или подводила. Норма молчит.
func (s *Server) modelAlerts(c engine.ModelCard, roles []engine.Role) []Component {
	var out []Component
	if c.Gone {
		out = append(out, Component{Type: "row", Layout: "wide", ID: "model-gone", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Провайдер больше не предлагает эту модель",
			Caption: "Обращение к ней кончится отказом: обновите список моделей провайдера или уберите её. " +
				"Сама Сёма её не удаляет: модель могла пропасть из списка на час",
			Actions: []Action{{Action: "$goto", Target: providerAddr(c.Group, providerTabKey), Label: "К провайдеру", Style: "primary"}}})
	}
	if c.Problem != "" {
		row := Component{Type: "row", Layout: "wide", ID: "model-problem", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: firstUpper(c.Problem)}
		if c.NeedsKey && !c.HasKey && !c.FromFile {
			row.Subtitle = "У провайдера не задан ключ"
			row.Caption = "Без ключа модель не отвечает. Ключ один на провайдера, и работают им все его модели"
			row.Actions = []Action{{Action: "$goto", Target: providerAddr(c.Group, providerTabKey), Label: "Задать ключ", Style: "primary"}}
		}
		out = append(out, row)
	}
	for _, r := range roles {
		if r.Model != c.Name {
			continue
		}
		if until, resting := s.eng.RoleRest(r.Name); resting {
			out = append(out, Component{Type: "row", Layout: "wide", ID: "model-rest-" + r.Name, Level: LevelWarn, Dot: levelDot(LevelWarn),
				Subtitle: "Роль «" + r.Name + "» отдыхает до " + until.Format("15:04"),
				Caption: "Три отказа подряд: роль отдыхает, чтобы одна медленная модель не красила «Здоровье» в сплошные отказы. " +
					"Удачная проверка снимает передышку сразу",
				Actions: []Action{{Action: "check_model", Target: c.Name, Label: "Проверить", Style: "primary"}}})
			continue
		}
		if r.LastErr != "" {
			failed, _, why := parseMark(r.LastErr)
			if failed != "" && failed != c.Name {
				continue // отметка про другую модель: эта роль с тех пор переназначена
			}
			out = append(out, Component{Type: "row", Layout: "wide", ID: "model-failed-" + r.Name, Level: LevelWarn, Dot: levelDot(LevelWarn),
				Subtitle: "Роль «" + r.Name + "» подводила эту модель", Caption: clipRunes(why, 240),
				Actions: []Action{{Action: "dismiss_role_err", Target: r.Name, Label: "Закрыть"}}})
		}
	}
	return out
}

// modelAbilities — «Что умеет». Умения — это подсказки для назначения и
// двери: видит ли картинки сказал провайдер, ищет ли в сети отмечает человек,
// слышит ли видно по названию или по его отметке.
func (s *Server) modelAbilities(add func(Component), c engine.ModelCard) {
	facts := []Prop{{Label: "Подключение", Value: connectionWord(c.Type)}}
	if c.BaseURL != "" {
		facts = append(facts, Prop{Label: "Адрес", Value: shortURL(c.BaseURL)})
	}
	if c.Model != "" && c.Model != c.Name {
		facts = append(facts, Prop{Label: "У провайдера", Value: c.Model})
	}
	rows := []Component{
		{Type: "row", Layout: "wide", ID: "model-facts", Subtitle: "Что это за модель", Facts: facts},
		{Type: "row", Layout: "wide", ID: "ability-vision", Subtitle: "Видит картинки", Caption: "сказал провайдер",
			Badges: yesNoBadge(c.Vision, "да", "нет")},
	}

	search := Component{Type: "row", Layout: "wide", ID: "ability-search", Subtitle: "Ищет в интернете",
		Caption: "подсказка при назначении на роль «Поиск»; сам поиск включается только на поисковом вызове"}
	if c.FromFile || c.Type == "cli" {
		search.Badges = yesNoBadge(c.Search, "да", "нет")
	} else {
		search.Toggle = boolToggle(c.Search)
		search.Actions = []Action{{Action: "toggle_model_search", Target: c.Name, Label: "toggle"}}
	}
	rows = append(rows, search)

	// слышит: умение есть только у OpenAI-совместимого подключения
	if c.Type == "openai" {
		hears := Component{Type: "row", Layout: "wide", ID: "ability-hears", Subtitle: "Слышит речь",
			Caption: "у моделей с «transcribe» или «whisper» в имени умение видно по названию; у остальных — ваша отметка"}
		switch {
		case c.FromFile:
			hears.Badges = yesNoBadge(c.Hears, "да", "нет")
		case c.HearsByName:
			hears.Badges = []Badge{{Text: "да · по названию"}}
		default:
			hears.Toggle = boolToggle(c.Hears)
			hears.Actions = []Action{{Action: "toggle_model_hears", Target: c.Name, Label: "toggle"}}
		}
		rows = append(rows, hears)
	}

	check := Component{Type: "row", Layout: "wide", ID: "ability-check", Subtitle: "Проверка",
		Caption: "«Проверить» в шапке шлёт модели слово «готов» и меряет, за сколько она ответила; удачный ответ снимает передышку у её ролей"}
	if line := s.lastCheckLine(c.Name); line != "" {
		check.Caption = line
	}
	rows = append(rows, check)
	add(Component{Type: "section", ID: "model-abilities", Title: "Что умеет", Children: rows})
}

// connectionWord — вид подключения словами.
func connectionWord(kind string) string {
	switch kind {
	case "anthropic":
		return "Anthropic"
	case "cli":
		return "клиент по подписке на этой машине"
	case "perplexity":
		return "Perplexity · Agent API"
	}
	return "OpenAI-совместимая"
}

// modelTrust — «Доверие». Доверенная модель видит закрытые ящики, секретное и
// записи памяти с личным. Это утверждение человека, а не адрес: «своя» по
// адресу бывает и чужой сервером через VPN. Секретное при этом не уходит наружу
// никогда, что бы ни было включено, — здесь это только сказано.
func (s *Server) modelTrust(add func(Component), c engine.ModelCard) {
	explain := "Доверенная модель видит закрытые ящики, файлы класса «секретное» и записи памяти с личным. " +
		"Доверяйте только модели на своём железе или в своей сети — адрес этого не гарантирует. " +
		"Секретное не уходит наружу никогда, что бы вы ни включили."
	trust := Component{Type: "row", Layout: "wide", ID: "trust-toggle", Subtitle: "Доверять закрытое", Caption: explain}
	switch {
	case c.Type == "cli":
		trust.Caption = "Клиент по подписке уносит текст провайдеру, поэтому доверенным не бывает."
		trust.Badges = []Badge{{Text: "внешняя"}}
	case c.FromFile:
		trust.Caption = "Модель описана файлом запуска: доверие ставится только у моделей, подключённых из интерфейса."
		trust.Badges = yesNoBadge(c.Trusted, "доверенная", "внешняя")
	default:
		trust.Toggle = boolToggle(c.Trusted)
		toggle := Action{Action: "toggle_model_trust", Target: c.Name, Label: "toggle"}
		if c.Trusted {
			toggle.Confirm = "Модель «" + c.Name + "» перестанет видеть закрытые ящики, секретное и записи памяти с личным. " +
				"Её работа по такому содержимому остановится, пока доверенной модели нет."
			toggle.ConfirmOK = "Считать внешней"
		} else {
			toggle.Confirm = "Доверенная модель получает то, что внешней не показывается: письма закрытых ящиков, файлы класса «секретное», " +
				"записи памяти с личным. Доверяйте только модели на своём железе или в своей сети — адрес этого не гарантирует."
			toggle.ConfirmOK = "Доверять"
		}
		trust.Actions = []Action{toggle}
	}
	sees := Component{Type: "row", Layout: "wide", ID: "trust-sees", Subtitle: "Видит сейчас",
		Badges: []Badge{{Text: map[bool]string{true: "всё, включая секретное", false: "только обычное"}[c.Trusted]}}}
	add(Component{Type: "section", ID: "model-trust", Title: "Доверие", Children: []Component{trust, sees}})
}

// ---- цена ----

// priceHint — подсказки к полям цены: что значит поле и чем рискуешь, не задав его.
const (
	priceNameHint  = "как модель называется в таблице цен провайдера; без связки слежение за ценами её не узнаёт (у DeepSeek в API «deepseek-chat», а в прайсе «deepseek-v4-flash»)"
	priceCacheHint = "у DeepSeek кэшированный вход дешевле обычного в тридцать раз, и без этого числа счёт завышен настолько же"
	priceMinHint   = "у моделей речи платят за секунды звука, а не за токены (gpt-4o-mini-transcribe — $0.003 за минуту); вместо входа и выхода, не вместе с ними"
	priceScheduleH = "свой тариф по расписанию в UTC: «пн-пт 01:00-04:00,06:00-10:00 UTC»; непонятное расписание значит «пика нет», пусто — пика нет вовсе"
)

// priceFields — поля цены по тому, что сейчас стоит у модели. Часы пик, пока их
// нет, и валюта прячутся под «Ещё N полей» той же формы: ими пользуются редко, а
// заданный пик виден сразу.
func priceFields(c engine.ModelCard, parts engine.PriceParts) []Field {
	cur := parts.Currency
	if cur == "" {
		cur = "$"
	}
	per := cur + " за 1M"
	peakMore := !parts.HasPeak()
	fields := []Field{
		{Type: "text", Name: "price_name", Label: "Имя в прайсе", Hint: priceNameHint, Value: c.PriceName},
		{Type: "stepper", Name: "in", Label: "Вход", Unit: per, Value: parts.In},
		{Type: "stepper", Name: "out", Label: "Выход", Unit: per, Value: parts.Out,
			Hint: "пусто — как вход: одна цена на оба вида"},
		{Type: "stepper", Name: "cache", Label: "Вход из кэша", Unit: per, Value: parts.Cache, Hint: priceCacheHint},
	}
	if c.Hears || parts.IsPerMinute() {
		fields = append(fields, Field{Type: "stepper", Name: "minute", Label: "За минуту звука", Unit: cur + " за минуту",
			Value: parts.Minute, Hint: priceMinHint})
	}
	fields = append(fields,
		Field{Type: "text", Name: "schedule", Label: "Часы пик", Hint: priceScheduleH, Value: parts.Schedule, More: peakMore},
		Field{Type: "stepper", Name: "peak_in", Label: "Вход в часы пик", Unit: per, Value: parts.PeakIn, More: peakMore},
		Field{Type: "stepper", Name: "peak_out", Label: "Выход в часы пик", Unit: per, Value: parts.PeakOut, More: peakMore},
		Field{Type: "stepper", Name: "peak_cache", Label: "Вход из кэша в часы пик", Unit: per, Value: parts.PeakCache, More: peakMore},
		Field{Type: "segments", Name: "currency", Label: "Валюта", Options: engine.PriceCurrencies, Value: cur, More: true,
			Hint: "знак у всех цен модели: «$», «€» или «₽»"})
	return fields
}

// priceOrigin — откуда стоит цена: с прайса провайдера или вписана человеком, и
// когда её сверяли.
func (s *Server) priceOrigin(c engine.ModelCard) string {
	if !c.Remote {
		return "модель считает ваша же машина: расход по ней не считается, цена не нужна"
	}
	if c.Price == "" {
		return "цена не названа — расход по модели не считается. Цены приносит проверка у провайдера: она разбирает его страницу и предлагает карточкой в «Уведомлениях»"
	}
	var parts []string
	switch c.PriceBy {
	case engine.PriceByPrice:
		parts = append(parts, "принята с прайса провайдера")
	default:
		// пусто — цена из времён, когда отметки не было: считаем вписанной человеком
		parts = append(parts, "вписана вами")
	}
	if c.PriceAt != "" {
		parts[0] += " · " + whenText(c.PriceAt)
	}
	if last, ok := s.eng.LastPriceCheck(c.Group); ok {
		if t, err := time.Parse(time.RFC3339, last.At); err == nil {
			parts = append(parts, "провайдер проверен "+whenText(t.Format(time.RFC3339)))
		}
	}
	return strings.Join(parts, " · ") + ". Цена, вписанная вами, пачкой проверки не меняется"
}

// modelPrice — «Цена»: четыре поля плюс часы пик вместо мини-языка в вопросе.
func (s *Server) modelPrice(add func(Component), c engine.ModelCard) {
	title := "Цена · по ней считается расход на «Здоровье» и в бюджетах"
	if c.FromFile {
		add(Component{Type: "section", ID: "model-price", Title: title, Children: []Component{
			{Type: "row", Layout: "wide", ID: "price-file", Subtitle: "Цена не задаётся",
				Caption: "Модель описана файлом запуска: цены и связка с прайсом — у моделей, подключённых из интерфейса"}}})
		return
	}
	parts, _ := s.eng.PricePartsOf(c.Name)
	form := Component{Type: "form", ID: "model-price", Style: formRows, Fields: priceFields(c, parts),
		Submit: &Action{Action: "set_model_price", Target: c.Name, Label: "Сохранить"}}
	origin := Component{Type: "row", Layout: "wide", ID: "price-origin", Subtitle: "Откуда цена", Caption: s.priceOrigin(c)}
	if c.Remote && c.Type != "cli" {
		origin.Caption += " · нажмите — экран провайдера"
		origin.Actions = []Action{{Action: "$goto", Target: providerAddr(c.Group, providerTabPrices), Label: "Провайдер →"}}
	}
	add(Component{Type: "section", ID: "model-price-section", Title: title, Children: []Component{form, origin}})
	if line := s.lastCheckLine(c.Name); line != "" && strings.Contains(line, "цен") {
		add(Component{Type: "text", Style: "secondary", Text: line})
	}
}

// ---- расход ----

// modelSpend — «Расход»: за сутки и за неделю одной строкой — вызовы, токены,
// деньги, доля кэша. Читается из журнала одним запросом на период (а не по
// запросу на модель): в журнале всё уже сгруппировано по модели.
func (s *Server) modelSpend(add func(Component), c engine.ModelCard) {
	st := s.eng.Store()
	now := time.Now().Truncate(time.Minute)
	line := func(since time.Time) string {
		sums, err := st.UsageBy("model", since, "")
		if err != nil {
			return "журнал не прочитан: " + err.Error()
		}
		for _, u := range sums {
			if u.Key == c.Name {
				return usageLine(u, c)
			}
		}
		return "вызовов не было"
	}
	rows := []Component{
		{Type: "row", Layout: "wide", ID: "usage-day", Subtitle: "За сутки", Caption: line(now.Add(-24 * time.Hour))},
		{Type: "row", Layout: "wide", ID: "usage-week", Subtitle: "За неделю", Caption: line(now.AddDate(0, 0, -7))},
		{Type: "row", Layout: "wide", ID: "usage-more", Subtitle: "Подробно",
			Caption: "расход по всем моделям и потребителям — на «Здоровье»",
			Actions: []Action{{Action: "$goto", Target: "health", Label: "Здоровье"}}},
	}
	add(Component{Type: "section", ID: "model-usage", Title: "Расход", Children: rows})
}

// usageLine — «318 вызовов · 1.2 млн токенов · $3.90 · доля кэша 61 %». Деньги
// по модели без цены не выдумываются: так и сказано, а не молчаливый ноль.
func usageLine(u *store.UsageSum, c engine.ModelCard) string {
	if u.Calls == 0 {
		return "вызовов не было"
	}
	parts := []string{plural(u.Calls, "вызов", "вызова", "вызовов")}
	if total := u.TokensIn + u.TokensOut; total > 0 {
		mark := ""
		if u.Estimated {
			mark = "≈ "
		}
		parts = append(parts, mark+humanTokens(total)+" токенов")
	}
	switch {
	case u.Cost > 0:
		parts = append(parts, fmt.Sprintf("%s%.2f", engine.CurrencySign(u.Currency), u.Cost))
	case c.NoPrice():
		parts = append(parts, "цена не названа — деньги не посчитаны")
	case !c.Remote:
		parts = append(parts, "бесплатно")
	}
	if u.Cached > 0 && u.TokensIn > 0 {
		parts = append(parts, fmt.Sprintf("доля кэша %d %%", u.Cached*100/u.TokensIn))
	}
	if u.Fails > 0 {
		parts = append(parts, plural(u.Fails, "отказ", "отказа", "отказов"))
	}
	return strings.Join(parts, " · ")
}
