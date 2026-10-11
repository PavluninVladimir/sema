package api

// Правила сервера MCP — вкладка «Правила» экрана сервера
// (`settings:integrations@<имя>~rules`): у каждого инструмента свой ответ —
// «Разрешить», «Спросить» или «Запретить». Макет — design/pravila/Pravila.html;
// до 2 октября 2026 они лежали на этом же экране под списком серверов.
//
// Сохранение пачкой, а не каждым нажатием. Правило доступа — решение, а не
// настройка вида: случайный клик по «Разрешить» не должен вступать в силу
// в тот же миг, без взгляда на то, что именно меняется. Поэтому группы —
// формы, которые сохраняет нижняя полоса экрана (settings_saving.go): она
// считает правки («Изменено 3: «task_delete» — Запретить → Разрешить»), даёт
// «Отменить» и сохраняет всё одним обращением. Тот же механизм уже держит все
// формы настроек, и своей логики в браузере у этого экрана нет: переключатель
// — обычные радиокнопки формы, счёт и точка «изменено» — полоса.
//
// Фильтр по состоянию и поиск — на сервере, а не в браузере: клиент на htmx
// своего JavaScript почти не имеет, и то, что не выражается разметкой,
// выражается адресом экрана. Фильтр — чипы в шапке (переход, под защитой
// «уйдёте — изменение пропадёт»), поиск — поле сверху.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Фильтры списка инструментов.
const (
	mcpFilterAll   = ""
	mcpFilterNew   = "new"
	mcpOtherGroup  = "Прочее"
	mcpGroupsKey   = "mcp.groups." // + имя сервера: «приставка = название» по строке
	mcpRulesSaving = "save_mcp_rules"
)

// mcpGroupDef — группа инструментов: приставка имени и название на экране.
type mcpGroupDef struct{ Prefix, Title string }

// mcpGroupSeed — первичное заполнение групп: так называются приставки
// инструментов Planner. Список правится на самом экране (форма «Группы
// инструментов»), у каждого сервера свой; здесь только то, с чего он начинает.
var mcpGroupSeed = []mcpGroupDef{
	{"task_group", "Группы задач"}, {"kanban_status", "Канбан"}, {"habit_progress", "Отметки привычек"},
	{"checklist_item", "Чек-листы"}, {"time_stat", "Учёт времени"}, {"project", "Проекты"},
	{"habit", "Привычки"}, {"task", "Задачи"}, {"tag", "Теги"},
}

// mcpGroups — группы сервера: из настройки, а нет её — первичное заполнение.
func mcpGroups(st *store.Store, server string) []mcpGroupDef {
	raw := strings.TrimSpace(st.Setting(mcpGroupsKey+server, ""))
	if raw == "" {
		return mcpGroupSeed
	}
	var out []mcpGroupDef
	for _, line := range strings.Split(raw, "\n") {
		prefix, title, ok := strings.Cut(line, "=")
		prefix, title = strings.TrimSpace(prefix), strings.TrimSpace(title)
		if ok && prefix != "" && title != "" {
			out = append(out, mcpGroupDef{Prefix: strings.ToLower(prefix), Title: title})
		}
	}
	return out
}

// mcpGroupText — группы текстом для формы правки.
func mcpGroupText(defs []mcpGroupDef) string {
	lines := make([]string, 0, len(defs))
	for _, d := range defs {
		lines = append(lines, d.Prefix+" = "+d.Title)
	}
	return strings.Join(lines, "\n")
}

// mcpGroupOf — к какой группе относится инструмент: по самой длинной
// приставке, совпавшей с началом имени целым словом. Неизвестная приставка
// — «Прочее».
func mcpGroupOf(name string, defs []mcpGroupDef) string {
	key := strings.ToLower(name)
	best, title := -1, mcpOtherGroup
	for _, d := range defs {
		if key == d.Prefix || strings.HasPrefix(key, d.Prefix+"_") {
			if len(d.Prefix) > best {
				best, title = len(d.Prefix), d.Title
			}
		}
	}
	return title
}

// mcpToneOptions — три ответа в порядке показа и цвет выбранного.
var (
	mcpAnswerWords = []string{engine.PolicyTitle(engine.PolicyAllow), engine.PolicyTitle(engine.PolicyAsk), engine.PolicyTitle(engine.PolicyDeny)}
	mcpAnswerTones = []string{LevelOK, LevelWarn, LevelAlert}
)

// mcpVisible — инструменты, которые показывает экран при этом фильтре и
// поиске. Тот же отбор нужен при сохранении «всем в группе»: групповой
// ответ касается только показанного.
func mcpVisible(states []engine.MCPToolState, filter, search string) []engine.MCPToolState {
	search = strings.ToLower(strings.TrimSpace(search))
	var out []engine.MCPToolState
	for _, st := range states {
		switch filter {
		case mcpFilterAll:
		case mcpFilterNew:
			if !st.New {
				continue
			}
		default:
			if st.Policy != filter {
				continue
			}
		}
		if search != "" && !strings.Contains(strings.ToLower(st.Name+" "+st.Desc), search) {
			continue
		}
		out = append(out, st)
	}
	return out
}

// mcpRulesTarget — цель формы группы: «сервер~группа~фильтр~поиск». Фильтр и
// поиск едут с формой, чтобы «всем в группе» при сохранении знал, какие
// инструменты человек видел.
func mcpRulesTarget(server, group, filter, search string) string {
	return strings.Join([]string{server, group, filter, search}, "~")
}

func parseMCPRulesTarget(target string) (server, group, filter, search string) {
	parts := strings.SplitN(target, "~", 4)
	for len(parts) < 4 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2], parts[3]
}

// mcpRulesSection — раздел «Правила · <сервер> · N инструментов»: счётчики,
// ответ для нового инструмента, фильтр и поиск, группы по приставке имени.
func (s *Server) mcpRulesSection(doc *Document, add func(Component), m *store.MCPServer, filter, search string) {
	st := s.eng.Store()
	states := engine.MCPToolStates(m)
	allow, ask, deny, fresh := engine.MCPRuleCounts(states)
	title := fmt.Sprintf("Правила · %s · %s", m.Name, plural(len(states), "инструмент", "инструмента", "инструментов"))

	// фильтр по состоянию: чипы в шапке; выбор — переход, а не скрытие
	// строк, поэтому несохранённое защищает панель «уйдёте — пропадёт»
	doc.Filters = []Filter{
		{ID: mcpFilterAll, Title: "Все"},
		{ID: engine.PolicyAllow, Title: engine.PolicyTitle(engine.PolicyAllow)},
		{ID: engine.PolicyAsk, Title: engine.PolicyTitle(engine.PolicyAsk)},
		{ID: engine.PolicyDeny, Title: engine.PolicyTitle(engine.PolicyDeny)},
	}
	if fresh > 0 {
		doc.Filters = append(doc.Filters, Filter{ID: mcpFilterNew, Title: "Новые", Badge: fmt.Sprint(fresh), Level: LevelInfo})
	}
	doc.ActiveFilter = filter
	// верхнее поле ищет инструменты этого списка, а не настройки: подсказка
	// говорит об этом (mcpOwnSearch). На телефоне колонки разделов нет, и
	// поле стоит над списком — как у списка моделей, оно остаётся из-за полосы
	// фильтров
	doc.Searchable = true
	doc.SearchHint = "Найти инструмент"

	head := Component{Type: "row", Layout: "wide", ID: "mcp-rules-head-" + m.Name,
		Subtitle: "У каждого инструмента свой ответ",
		Caption:  "«Разрешить» выполняется сразу, «Спросить» ждёт вашей кнопки, «Запретить» не выполняется и модели не показывается. Не подошло ни одно правило — запрещено.",
		Badges: []Badge{
			{Text: "Разрешить · " + fmt.Sprint(allow), Level: LevelOK},
			{Text: "Спросить · " + fmt.Sprint(ask), Level: LevelWarn},
			{Text: "Запретить · " + fmt.Sprint(deny), Level: LevelAlert},
		}}
	if fresh > 0 {
		head.Badges = append(head.Badges, Badge{Text: "новых · " + fmt.Sprint(fresh), Level: LevelInfo})
	}
	add(Component{Type: "section", Title: title, Children: []Component{head}})

	if len(states) == 0 {
		add(emptyState("Инструментов ещё нет",
			"Правила заводятся на то, что назвал сервер: нажмите «Проверить» у сервера — инструменты появятся здесь, каждый «новым» и запрещённым, пока вы не решите", "tray"))
		return
	}

	// ответ для нового инструмента: запрещено, пока человек не решил иначе
	add(Component{Type: "form", ID: "mcp-rules-default-" + m.Name, Title: "Новый инструмент",
		Fields: []Field{{Type: "segments", Name: "default", Label: "Новый инструмент, которого сервер раньше не называл",
			Hint:    "по умолчанию «Запретить»: новый инструмент не выполняется и модели не виден, пока вы не решите за него",
			Options: mcpAnswerWords, Tones: mcpAnswerTones, Value: engine.PolicyTitle(defaultOf(m))}},
		Submit: &Action{Action: mcpRulesSaving, Target: mcpRulesTarget(m.Name, "", "", ""), Label: "Сохранить", Style: "primary"}})

	shown := mcpVisible(states, filter, search)
	if len(shown) == 0 {
		add(emptyState("Под фильтр ничего не подошло", "смените фильтр или поисковое слово сверху", "tray"))
	}
	defs := mcpGroups(st, m.Name)
	byGroup := map[string][]engine.MCPToolState{}
	for _, tool := range shown {
		g := mcpGroupOf(tool.Name, defs)
		byGroup[g] = append(byGroup[g], tool)
	}
	// порядок групп — как в настройке, «Прочее» последним
	var order []string
	seen := map[string]bool{}
	for _, d := range defs {
		if !seen[d.Title] && len(byGroup[d.Title]) > 0 {
			seen[d.Title] = true
			order = append(order, d.Title)
		}
	}
	var rest []string
	for g := range byGroup {
		if !seen[g] && g != mcpOtherGroup {
			rest = append(rest, g)
		}
	}
	sort.Strings(rest)
	order = append(order, rest...)
	if len(byGroup[mcpOtherGroup]) > 0 {
		order = append(order, mcpOtherGroup)
	}
	for i, g := range order {
		add(mcpGroupForm(m, g, i, byGroup[g], filter, search))
	}
	// как называются группы — правится здесь, а не в коде
	add(Component{Type: "form", ID: "mcp-rules-groups-" + m.Name, Title: "Группы инструментов · " + m.Name, Collapsed: true,
		Fields: []Field{{Type: "code", Name: "groups", Label: "Приставка имени = название группы",
			Hint:  "по строке на группу: «task = Задачи». Инструмент попадает в группу с самой длинной подходящей приставкой, а неизвестная приставка — в «Прочее»",
			Value: mcpGroupText(defs)}},
		Submit: &Action{Action: "save_mcp_groups", Target: m.Name, Label: "Сохранить", Style: "primary"}})
}

func defaultOf(m *store.MCPServer) string {
	if store.ValidRuleKind(m.RuleDefault) {
		return m.RuleDefault
	}
	return engine.PolicyDeny
}

// mcpGroupForm — одна группа инструментов: форма с переключателем «всем в
// группе» и по переключателю на каждый инструмент. Форма сохраняется нижней
// полосой (имя действия начинается с save_).
func mcpGroupForm(m *store.MCPServer, group string, n int, tools []engine.MCPToolState, filter, search string) Component {
	allow, ask, deny, fresh := engine.MCPRuleCounts(tools)
	summary := fmt.Sprintf("%s · %d", group, len(tools))
	var counts []string
	for _, c := range []struct {
		n    int
		word string
	}{{allow, "разрешено"}, {ask, "спрашивать"}, {deny, "запрещено"}} {
		if c.n > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", c.word, c.n))
		}
	}
	if fresh > 0 {
		counts = append(counts, fmt.Sprintf("новых %d", fresh))
	}
	if len(counts) > 0 {
		summary += " — " + strings.Join(counts, ", ")
	}
	fields := []Field{{Type: "segments", Name: "bulk", Label: "Всем в группе",
		Hint:    "применится при сохранении к инструментам группы, которые вы не трогали отдельно",
		Options: mcpAnswerWords, Tones: mcpAnswerTones}}
	for _, t := range tools {
		f := Field{Type: "segments", Name: "tool:" + t.Name, Label: t.Name, Hint: clipRunes(strings.Join(strings.Fields(t.Desc), " "), 200),
			Options: mcpAnswerWords, Tones: mcpAnswerTones}
		if t.New {
			// решения про него ещё нет: ничего не выбрано, а действует ответ по
			// умолчанию. Выбор — и есть решение, даже «Запретить»
			f.Hint = strings.TrimSpace("новый: пока «" + engine.PolicyTitle(t.Policy) + "» по умолчанию, выберите ответ. " + f.Hint)
		} else {
			f.Value = engine.PolicyTitle(t.Policy)
		}
		fields = append(fields, f)
	}
	return Component{Type: "form", ID: fmt.Sprintf("mcp-rules-%s-%d", m.Name, n), Title: summary, Fields: fields,
		Submit: &Action{Action: mcpRulesSaving, Target: mcpRulesTarget(m.Name, group, filter, search), Label: "Сохранить", Style: "primary"}}
}

// saveMCPRules — сохранение одной формы правил: ответ для нового
// инструмента и/или ответы по инструментам группы. Обрабатывается только то,
// что пришло в форме: группа, скрытая фильтром, не затрагивается.
func (s *Server) saveMCPRules(req actionRequest) (string, error) {
	st := s.eng.Store()
	server, group, filter, search := parseMCPRulesTarget(req.Target)
	m, ok := st.MCPServer(server)
	if !ok {
		return "", fmt.Errorf("сервера MCP %q нет", server)
	}
	rules := map[string]string{}
	for k, v := range m.Rules {
		rules[k] = v
	}
	def := defaultOf(m)
	if w, has := req.Params["default"]; has {
		p, known := engine.PolicyFromWord(w)
		if !known {
			return "", fmt.Errorf("ответ для нового инструмента: разрешить, спросить или запретить")
		}
		def = p
	}
	// сколько инструментов сейчас на экране: групповой ответ касается их
	states := engine.MCPToolStates(m)
	members := map[string]bool{}
	for _, tool := range mcpVisible(states, filter, search) {
		if group != "" && mcpGroupOf(tool.Name, mcpGroups(st, m.Name)) == group {
			members[tool.Name] = true
		}
	}
	bulk := ""
	if w, has := req.Params["bulk"]; has {
		p, known := engine.PolicyFromWord(w)
		if !known {
			return "", fmt.Errorf("ответ «всем в группе»: разрешить, спросить или запретить")
		}
		bulk = p
	}
	named := map[string]bool{}
	for key, w := range req.Params {
		name, isTool := strings.CutPrefix(key, "tool:")
		if !isTool {
			continue
		}
		p, known := engine.PolicyFromWord(w)
		if !known {
			return "", fmt.Errorf("инструмент %q: ответ разрешить, спросить или запретить", name)
		}
		named[name] = true
		was, decided := engine.MCPRuleOf(m, name)
		switch {
		case !decided || was != p:
			rules[name] = p // человек выбрал сам
		case bulk != "" && members[name]:
			rules[name] = bulk // не трогал — групповой ответ
		}
	}
	// новые инструменты, у которых ничего не выбрано: в форму они не
	// присылаются, и групповой ответ решает за них
	if bulk != "" {
		for name := range members {
			if !named[name] {
				if _, decided := engine.MCPRuleOf(m, name); !decided {
					rules[name] = bulk
				}
			}
		}
	}
	if err := st.SetMCPRules(server, rules, def); err != nil {
		return "", err
	}
	// решено по всем новым — уведомление «новые инструменты» закрывается само
	if cur, ok := st.MCPServer(server); ok {
		s.eng.NoteNewMCPTools(cur)
	}
	return stayMCP(req, entityAddr(mcpScreen, server, mcpTabRules)), nil
}
