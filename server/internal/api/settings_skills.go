package api

import (
	"fmt"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Раздел «Скиллы»: инструкции Сёме блоками по группам. Правятся здесь и по
// просьбе из чата (блок sema-skill → карточка с разницей → «Принять»).
//
// До 2 октября 2026 каждая строка списка несла пять кнопок и капсулы, а в кнопке
// «Скопировать» лежал полный текст скилла: четыре встроенных скилла — 51 КБ
// текста, и весь он ехал в разметке списка (78 КБ и 333 узла, а с открытым
// скиллом — 111 КБ). Теперь строка тонкая: название, не больше двух капсул,
// фраза «когда применять», тумблер «включён» и «···». Вся строка ведёт на экран
// скилла (settings_skill.go), и **текст скилла едет только туда** — вместе с
// кнопкой «Скопировать текст». «Добавить скилл» — свой экран.
//
// Адреса:
//
//	settings:skills                раздел
//	settings:skills@new            «Добавить скилл»
//	settings:skills@<имя>          экран скилла (engine.SkillScreen)

const skillsScreen = "settings:skills"

// skillsNew — «скилл» с этим именем означает экран добавления.
const skillsNew = "new"

// skillsAddr — разобранный адрес экрана «Скиллов»: имя скилла («new» — экран
// добавления, пусто — раздел).
func parseSkillsAddr(screen string) string {
	_, id, _ := splitEntity(screen)
	return strings.TrimSpace(id)
}

func (s *Server) buildSkills(doc *Document, screen string) error {
	doc.Layout = "wide"
	switch name := parseSkillsAddr(screen); name {
	case "":
		return s.buildSkillsSection(doc)
	case skillsNew:
		return s.buildSkillNew(doc)
	default:
		return s.buildSkill(doc, name)
	}
}

func (s *Server) buildSkillsSection(doc *Document) error {
	doc.Heading = "Скиллы"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	eng := s.eng
	skills := eng.Skills()
	enabled := 0
	for _, k := range skills {
		if k.Enabled {
			enabled++
		}
	}
	doc.Subheading = fmt.Sprintf("Инструкции Сёме на русском: что проверять, как читать вывод, как отчитываться. Включено %d из %d.",
		enabled, len(skills))
	// «Добавить скилл» — единственная кнопка шапки раздела
	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(skillsScreen, skillsNew, ""),
		Label: "Добавить скилл", Style: "primary"}}

	// предложения правок, ждущие решения: у такого скилла в строке своя капсула
	waiting := map[string]bool{}
	if drafts, err := eng.Store().SkillDrafts(store.DraftOpen, 200); err == nil {
		for _, d := range drafts {
			waiting[d.Name] = true
		}
	}

	byGroup := map[string][]*store.Skill{}
	for _, k := range skills {
		g := k.Group
		if g == "" {
			g = "Прочее"
		}
		byGroup[g] = append(byGroup[g], k)
	}
	for _, g := range eng.SkillGroups() {
		list := byGroup[g]
		rows := make([]Component, 0, len(list))
		for _, k := range list {
			rows = append(rows, s.skillRow(k, waiting[k.Name]))
		}
		add(Component{Type: "section", ID: "skills-" + g, Title: fmt.Sprintf("%s · %s", g, plural(len(list), "скилл", "скилла", "скиллов")),
			Children: rows})
	}
	if len(skills) == 0 {
		empty := emptyState("Скиллов пока нет",
			"Скилл — это ваша инструкция Сёме: когда её применять и что делать. Встроенные приезжают со сборкой сервера; свой можно добавить кнопкой выше или просто попросить в чате", "doc.text")
		empty.Actions = []Action{{Action: "$goto", Target: entityAddr(skillsScreen, skillsNew, ""), Label: "Добавить скилл", Style: "primary"}}
		add(Component{Type: "section", Title: "Скиллы", Children: []Component{empty}})
	}

	add(Component{Type: "section", ID: "skills-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: fmt.Sprintf("В чат скилл попадает по словам-приметам (и по именам серверов для блока «Администрирование») или закреплением за чатом; "+
			"модуль берёт его через sema.skill('имя'). Опись включённых идёт в запрос всегда, полный текст — по приметам в вопросе и в пределе до %d токенов. "+
			"Править можно и на экране скилла, и просьбой в чате: «добавь в скилл Linux проверку fail2ban» — Сёма покажет разницу и применит после «Принять». "+
			"Ключи и пароли в текст не сохраняются.", eng.SkillsMaxTokens())}}})

	// предел — «Тонкая настройка» экрана (settings_rare.go)
	add(Component{Type: "form", ID: "skills-settings", Title: "Скиллы в запросе", Tier: tierRare, Style: formRows,
		Fields: []Field{s.settingField("skills.max_tokens", "max", "Токенов на скиллы в одном запросе",
			"Сколько места в запросе занимают тексты скиллов; опись включённых идёт всегда, полный текст — по приметам в вопросе и в этих пределах", "")},
		Submit: &Action{Action: "set_skills_max", Label: "Сохранить"}})
	return nil
}

// skillUsers — какие модули берут какой скилл: имя скилла → названия модулей.
// Один проход по модулям на экран, а не по разу на строку.
func (s *Server) skillUsers() map[string][]string {
	out := map[string][]string{}
	for _, m := range s.eng.Modules() {
		for _, name := range m.Skills {
			out[name] = append(out[name], "«"+m.Title+"»")
		}
	}
	return out
}

// skillSourceWord — откуда скилл словами.
func skillSourceWord(k *store.Skill) string {
	return map[string]string{store.SkillBuiltin: "встроенный", store.SkillOwn: "свой", store.SkillChat: "из чата"}[k.Source]
}

// skillBadges — капсулы строки, не больше двух, по важности: ждёт решения, чья это
// правка (свой, из чата, правлен вами), сколько места займёт в запросе. «Встроенный»
// капсулой не называется: он у большинства, и отличает только тот, что не такой.
func skillBadges(k *store.Skill, waiting bool) []Badge {
	var all []Badge
	if waiting {
		all = append(all, Badge{Text: "ждёт решения", Level: LevelWarn})
	}
	switch {
	case k.Source == store.SkillOwn:
		all = append(all, Badge{Text: "свой"})
	case k.Source == store.SkillChat:
		all = append(all, Badge{Text: "из чата"})
	case k.EditedBy != "":
		all = append(all, Badge{Text: "правлен"})
	}
	all = append(all, Badge{Text: fmt.Sprintf("≈%d токенов", tokensRough(k.Text))})
	if len(all) > 2 {
		all = all[:2]
	}
	return all
}

// skillRow — тонкая строка списка: название, не больше двух капсул, фраза «когда
// применять», тумблер «включён» и «···». Вся строка ведёт на экран скилла; текста
// скилла в строке нет — он едет только на экран скилла.
func (s *Server) skillRow(k *store.Skill, waiting bool) Component {
	row := Component{Type: "row", Layout: "wide", ID: "skill-" + k.Name, Subtitle: k.Title,
		Toggle: boolToggle(k.Enabled), Caption: k.When, Badges: skillBadges(k, waiting)}
	row.Actions = []Action{
		{Action: "toggle_skill", Target: k.Name, Label: "toggle"},
		{Action: "$goto", Target: engine.SkillScreen(k.Name), Label: k.Title},
	}
	if k.Source != store.SkillBuiltin {
		row.Menu = []Action{{Action: "delete_skill", Target: k.Name, Label: "Удалить…", Style: "destructive",
			Confirm: "Удалить скилл «" + k.Title + "»?", ConfirmOK: "Удалить"}}
	}
	return row
}

func tokensRough(s string) int { return len([]rune(s)) / 3 }

// stayOnSkills — после действия остаёмся там, где нажали: экран скилла не должен
// уступать место списку из-за тумблера «включён». Нажали не здесь (из «Развития»,
// из уведомления) — на список, как было.
func stayOnSkills(req actionRequest) string {
	if req.Screen == skillsScreen || strings.HasPrefix(req.Screen, skillsScreen+"@") {
		return req.Screen
	}
	return skillsScreen
}

func (s *Server) skillsActions(req actionRequest) (string, error) {
	eng := s.eng
	st := eng.Store()
	switch req.Action {
	case "toggle_skill":
		k, ok := st.Skill(req.Target)
		if !ok {
			return "", fmt.Errorf("скилла %q нет", req.Target)
		}
		return stayOnSkills(req), st.SetSkillEnabled(k.Name, !k.Enabled)
	case "save_skill":
		k, ok := st.Skill(req.Target)
		if !ok {
			return "", fmt.Errorf("скилла %q нет", req.Target)
		}
		was := *k
		k.Title, k.Group, k.Keywords, k.When, k.Text = field(req, "title", k.Title), field(req, "group", k.Group),
			field(req, "keywords", k.Keywords), field(req, "when", k.When), field(req, "text", k.Text)
		// «Сохранить» без правки скилл не трогает: иначе встроенный скилл, к которому
		// никто не прикасался, стал бы «правленным» и перестал обновляться из сборки
		if k.Title == was.Title && k.Group == was.Group && k.Keywords == was.Keywords && k.When == was.When && k.Text == was.Text {
			return stayOnSkills(req), nil
		}
		return stayOnSkills(req), eng.SaveSkill(k, "человек")
	case "add_skill":
		k := &store.Skill{Name: strings.TrimSpace(req.Params["name"]), Title: strings.TrimSpace(req.Params["title"]),
			Group: strings.TrimSpace(req.Params["group"]), Keywords: strings.TrimSpace(req.Params["keywords"]),
			When: strings.TrimSpace(req.Params["when"]), Text: strings.TrimSpace(req.Params["text"]), Enabled: true, Source: store.SkillOwn}
		if _, exists := st.Skill(k.Name); exists {
			return "", fmt.Errorf("скилл %q уже есть — откройте его", k.Name)
		}
		// после «Добавить» — на экран нового скилла, а не на список
		return engine.SkillScreen(k.Name), eng.SaveSkill(k, "человек")
	case "delete_skill":
		return skillsScreen, st.DeleteSkill(req.Target)
	case "reset_skill":
		return engine.SkillScreen(req.Target), eng.ResetSkill(req.Target)
	case "set_skills_max":
		return skillsScreen, s.saveSettings(req, settingParam{"max", "skills.max_tokens", "токенов на скиллы"})
	case "decide_skill":
		id, verdict := splitTarget(req.Target)
		return "", eng.DecideSkill(id, verdict == "accept")
	case "set_host_kind":
		h, ok := st.Host(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		switch req.Params["value"] {
		case "род: Linux":
			h.Kind = store.HostLinux
		case "род: RouterOS":
			h.Kind = store.HostRouterOS
		default:
			h.Kind = ""
		}
		return stayOnServers(req, serversScreen), st.SaveHost(h)
	}
	return "", errUnhandled
}

func field(req actionRequest, name, def string) string {
	if v, ok := req.Params[name]; ok {
		return strings.TrimSpace(v)
	}
	return def
}

// skillCardComponent — карточка предложения скилла в переписке.
func (s *Server) skillCardComponent(card engine.ToolCard, at string) Component {
	sk := card.Skill
	// карточка дела в ленте: разница «+/−» читается вдвое быстрее, чем тот
	// же текст одним куском, — стиль «разница» рисует её по знакам
	c := Component{Type: "toolcard", Icon: "book.closed", Title: card.Title, Meta: at,
		Style: "разница", Detail: sk.Diff, Caption: card.Result}
	what := "изменение"
	if sk.New {
		what = "новый скилл"
	}
	c.Badges = []Badge{{Text: what, Level: LevelInfo}}
	if sk.Status == "proposed" {
		// карточка в переписке показывает живое состояние: решение могли
		// принять в «Уведомлениях» или на «Развитии», и кнопки тогда лишние
		if d, ok := s.eng.Store().SkillDraft(sk.ID); ok && d.State != store.DraftOpen {
			text := "отклонено"
			if d.State == store.DraftAccepted {
				text = "принято"
				withLevel(&c, LevelOK)
			}
			c.Badges = append(c.Badges, Badge{Text: text})
			c.Caption = card.Result + " · " + text + " " + whenText(d.Decided)
			c.Menu = []Action{{Action: "$goto", Target: engine.SkillScreen(sk.Name), Label: "Открыть скилл"}}
			return c
		}
		withLevel(&c, LevelWarn)
		c.Badges = append(c.Badges, Badge{Text: "ждёт вас", Level: LevelWarn})
		c.Actions = []Action{
			{Action: "decide_skill", Target: sk.ID + "~accept", Label: "Принять", Style: "primary"},
			{Action: "decide_skill", Target: sk.ID + "~reject", Label: "Не надо", Style: "destructive"},
		}
		// переход — в «…»: в ряду действий он читался бы как третье решение
		c.Menu = []Action{{Action: "$goto", Target: engine.SkillScreen(sk.Name), Label: "Показать весь скилл"}}
	}
	return c
}
