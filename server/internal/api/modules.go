package api

// Экран «Модули»: что установлено, какими правами обладает и из чего
// состоит. Права выдаются один раз при включении и видны потом в списке —
// модуль не может расширить их сам.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func (s *Server) buildModules(doc *Document) error {
	doc.Heading = "Модули"
	doc.Subheading = "Модуль — манифест и код в базе Сёмы: ничего не компилируется и не ставится в систему"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	s.mu.Lock()
	pending := s.pendingModule
	s.mu.Unlock()
	if pending != nil {
		s.moduleGrant(add, pending)
		return nil
	}

	mods := s.eng.Modules()
	rows := make([]Component, 0, len(mods))
	// расход и бюджеты — один раз на весь список, а не по три запроса на модуль
	usage := s.buildUsage()
	for _, m := range mods {
		rows = append(rows, s.moduleRow(m, usage))
	}
	chars, tokens, share := s.eng.InstructionsSize()
	включено := 0
	for _, m := range mods {
		if m.Enabled {
			включено++
		}
	}
	if len(mods) > 0 {
		add(Component{Type: "figs", ID: "modules-figs", Children: []Component{
			{Text: strconv.Itoa(len(mods)), Title: "модулей"},
			{Text: strconv.Itoa(включено), Title: "включено"},
			{Text: strconv.Itoa(tokens), Title: "токенов в инструкциях"},
		}})
	}
	if len(mods) == 0 {
		rows = append([]Component{emptyState("Своих модулей пока нет",
			"модуль — это манифест (что делает и какие права просит), строка --- и код на JavaScript · "+
				"проще всего описать словами в чате: Сёма напишет его сам, а права выдадите вы", "puzzlepiece")}, rows...)
	}
	add(Component{Type: "section", ID: "modules-list", Title: "Установленные модули",
		Children: collapseSame(rows, 3)})
	// Инструкция модуля живёт в раскрытии его строки, а не отдельной
	// формой на экране: четыре редактора кода, открытых одновременно,
	// занимали экран целиком, хотя правят их редко. Общие правила самой
	// Сёмы остаются формой — это единственное, что нужно под рукой.
	for _, m := range mods {
		if m.Name != "sema" {
			continue
		}
		add(Component{
			Type: "form", ID: "instr-" + m.Name, Title: "Общие правила Сёмы",
			Fields: []Field{{Type: "code", Name: "text", Label: "Инструкция",
				Hint:  "характер и правила: как отвечать, что помнить о вас, чего не делать никогда · старше инструкций всех модулей",
				Value: m.Instructions}},
			Submit: &Action{Action: "set_module_instructions", Target: m.Name, Label: "Сохранить"},
		})
	}
	add(Component{Type: "text", Style: "secondary",
		Text: fmt.Sprintf("Инструкции всех модулей вместе — %d символов, примерно %d токенов, %s.", chars, tokens, share)})

	add(Component{
		Type:  "form",
		ID:    "addmodule",
		Title: "Добавить модуль",
		Fields: []Field{
			{Type: "text", Name: "url", Label: "Ссылка на модуль",
				Hint: "или вставьте манифест и код ниже — тогда ссылка не нужна"},
			{Type: "code", Name: "source", Label: "Манифест, строка --- и код",
				Hint: `{"name":"servers","title":"Серверы","needs":["http","schedule"]}` + "\n---\n" +
					`sema.tool('servers.ping', ...)`},
		},
		Submit: &Action{Action: "install_module", Label: "Разобрать и показать права", Style: "primary"},
	})
	add(Component{Type: "text", Style: "secondary",
		Text: "Модуль видит ровно то, что перечислено в его правах, и ничего сверх: их выдаёте вы при включении. " +
			"Сёма умеет писать модули сам — опишите словами, что нужно, в чате."})
	add(Component{Type: "form", ID: "modules-limits", Title: "Пределы",
		Fields: []Field{{Type: "stepper", Name: "task_timeout", Label: "Задача модуля, секунд", Hint: "обход серверов и ожидание модели; событиям и контексту чата — 2 минуты",
			Value: fmt.Sprint(int(s.eng.ModuleTaskTimeout().Seconds())), Min: 30, Max: 3600, Step: 30, Instant: true},
			{Type: "stepper", Name: "script_timeout", Label: "Скрипт из чата, секунд", Hint: "поиск, текст моделью и картинка в одном скрипте; зависший цикл всё равно прервётся",
				Value: fmt.Sprint(int(s.eng.ScriptTimeout().Seconds())), Min: 10, Max: 3600, Step: 30, Instant: true}},
		Actions: []Action{{Action: "set_module_task_timeout", Label: "Сохранить"}}})
	return nil
}

// moduleCodeAddr — экран модуля с его кодом: «modules@<имя>».
func moduleCodeAddr(name string) string { return "modules@" + name }

// buildModuleCode — экран одного модуля: та же строка, что в списке, и код,
// раскрытый сразу. Код живёт здесь, а не в списке (perf-pass 5.2).
func (s *Server) buildModuleCode(doc *Document, name string) error {
	doc.Active = "modules"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	add(Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: "modules", Label: "‹ Модули"}}})
	m, ok := s.eng.Module(name)
	if !ok {
		doc.Heading = "Модуля нет"
		add(emptyState("Модуля «"+name+"» нет",
			"его удалили или переименовали — список установленных на экране «Модули»", "puzzlepiece"))
		return nil
	}
	doc.Heading = "Модуль «" + m.Title + "»"
	doc.Subheading = m.Name
	row := s.moduleRow(m, s.buildUsage())
	var acts []Action
	for _, a := range row.Actions {
		if a.Action != "$goto" {
			acts = append(acts, a)
		}
	}
	row.Actions = acts
	add(row)
	code := m.Code
	if code == "" {
		code = "// у модуля нет кода: он состоит из одной инструкции"
	}
	add(Component{Type: "section", ID: "module-code", Title: "Код", Expanded: true, Children: []Component{
		{Type: "row", Layout: "wide", ID: "module-code-" + m.Name, Subtitle: "JavaScript · " +
			plural(strings.Count(code, "\n")+1, "строка", "строки", "строк"),
			Detail: code, Expandable: true, Expand: "Показать код"},
	}})
	return nil
}

func (s *Server) moduleRow(m *engine.Module, usage *screenUsage) Component {
	var perms []string
	for _, n := range m.Needs {
		perms = append(perms, engine.PrimitiveTitle(n))
	}
	parts := []string{}
	if len(m.Tools) > 0 {
		parts = append(parts, fmt.Sprintf("%d инструмента", len(m.Tools)))
	}
	if len(m.Tasks) > 0 {
		parts = append(parts, fmt.Sprintf("%d задачи", len(m.Tasks)))
	}
	if len(m.Screens) > 0 {
		parts = append(parts, "свой экран")
	}
	// описание — фразой, всё остальное — фактами: раньше это склеивалось
	// в одну строку до 485 знаков, где права, данные и диагностика шли
	// вперемешку и обрезались на середине
	caption := m.About
	var facts []Prop
	fact := func(label string, vals []string) {
		if len(vals) > 0 {
			facts = append(facts, Prop{Label: label, Value: strings.Join(vals, ", ")})
		}
	}
	fact("Права", perms)
	fact("Секреты", m.Granted)
	fact("Память", m.GrantedMemory)
	fact("Скиллы", m.Skills)
	fact("MCP", m.GrantedMCP)
	fact("Серверы", m.GrantedHosts)
	if ev := s.eng.ModuleEventSummary(m); ev != "" {
		facts = append(facts, Prop{Label: "События", Value: ev})
	}
	if counts := s.eng.Store().CountModuleDocs(m.Name); len(counts) > 0 {
		var got []string
		for c, n := range counts {
			got = append(got, fmt.Sprintf("%s %d", c, n))
		}
		sort.Strings(got)
		facts = append(facts, Prop{Label: "Данные", Value: strings.Join(got, ", ")})
	}
	if len(parts) > 0 {
		facts = append(facts, Prop{Label: "Есть", Value: strings.Join(parts, ", ")})
	}
	// расход — факт, а не капсула: «за сутки 42 запуска · 197 тыс.
	// токенов» стояло у каждого модуля и кричало громче поломки.
	// Капсулой он становится, только когда жмёт бюджет
	if u := usage.usageBadge("module:" + m.Name); u != "" {
		facts = append(facts, Prop{Label: "За сутки", Value: strings.TrimPrefix(u, "за сутки ")})
	}
	var капсулы []Badge
	if b := engine.BudgetPick(usage.budgets, "module:"+m.Name); b != nil {
		switch {
		case b.Exceeded:
			капсулы = append(капсулы, Badge{Text: "бюджет исчерпан", Level: LevelAlert})
		case b.Pct >= 80:
			капсулы = append(капсулы, Badge{Text: fmt.Sprintf("бюджет %d %%", b.Pct), Level: LevelWarn})
		}
	}
	toggle := "off"
	if m.Enabled {
		toggle = "on"
	}
	// код в строке списка не едет: тридцать модулей — сто килобайт кода,
	// свёрнутого и невидимого, на каждое открытие «Модулей» (perf-pass 5.2);
	// он на экране модуля
	row := Component{
		Type: "row", Layout: "wide", ID: "module-" + m.Name,
		Subtitle: m.Title, Caption: caption, Facts: facts, Toggle: toggle, Badges: капсулы,
		Actions: []Action{
			{Action: "toggle_module", Target: m.Name, Label: "toggle"},
			{Action: "$goto", Target: moduleCodeAddr(m.Name), Label: "Код модуля"},
			{Action: "remove_module", Target: m.Name, Label: "Удалить", Style: "destructive",
				ConfirmOK: "Удалить",
				Confirm:   "Удалить модуль «" + m.Title + "»? Его инструменты, задачи и экраны исчезнут."},
		},
	}
	// что Сёма понял про код модуля: лишние права, мёртвые функции,
	// что отвалится. Разбор дешёвый, поэтому прямо в карточке.
	if check, err := s.eng.CheckModuleCode(m); err == nil {
		var notes []string
		if len(check.Extra) > 0 {
			var titles []string
			for _, n := range check.Extra {
				titles = append(titles, engine.PrimitiveTitle(n))
			}
			notes = append(notes, "права выданы, но в коде не встречаются: "+strings.Join(titles, ", "))
		}
		if dead := engine.DeadFuncs(m, check.Code); len(dead) > 0 {
			notes = append(notes, "никто не зовёт: "+strings.Join(dead, ", "))
		}
		if broken := engine.Broken(m, check.Code); len(broken) > 0 {
			var lost []string
			for _, b := range broken {
				lost = append(lost, b.What+" «"+b.Note+"» ждёт функцию "+b.Fn)
			}
			notes = append(notes, "не хватает функций: "+strings.Join(lost, "; "))
			row.Badges = append(row.Badges, Badge{Text: "манифест без функции", Color: "#C4271C"})
		}
		if check.ByString {
			notes = append(notes, "есть обращение вида sema[…] — сверка прав по коду неполна")
		}
		if len(notes) > 0 {
			caption += "\n" + strings.Join(notes, " · ")
			row.Caption = caption
		}
	} else {
		row.Badges = append(row.Badges, Badge{Text: "код не разбирается", Color: "#C4271C"})
		row.Caption = caption + "\n" + err.Error()
	}
	if len(m.PendingNeeds) > 0 {
		// обновление попросило новые примитивы: до решения человека модуль
		// работает по старым правам
		var titles []string
		for _, n := range m.PendingNeeds {
			titles = append(titles, engine.PrimitiveTitle(n))
		}
		row.Badges = append(row.Badges, Badge{Text: "просит права", Color: "#B25000"})
		row.Caption = "Просит новые права: " + strings.Join(titles, ", ") + " · пока работает по старым · " + caption
		row.Actions = append(row.Actions,
			Action{Action: "grant_module_needs", Target: m.Name, Label: "Выдать", Style: "primary",
				Confirm:   "Выдать модулю «" + m.Title + "» права: " + strings.Join(titles, ", ") + "?",
				ConfirmOK: "Выдать"},
			Action{Action: "reject_module_needs", Target: m.Name, Label: "Не выдавать"})
	}
	if m.Broken != "" {
		row.Badges = []Badge{{Text: "неисправен", Color: "#C4271C"}}
		row.Caption = "Отключён сам: " + m.Broken + " · " + caption
	}
	// чем модуль уже ломался: одной поломки мало, повторы важнее свежести —
	// после «включить заново» причина стирается, и та же беда выглядит первой
	if len(m.Breaks) > 0 {
		var rows []CmdRow
		for _, b := range m.Breaks {
			когда := ""
			if t, err := time.Parse(time.RFC3339, b.When); err == nil {
				когда = whenShort(t.Local())
			}
			rows = append(rows, CmdRow{Text: b.Why, Note: когда})
		}
		row.Cmds = append(row.Cmds, rows...)
		if len(m.Breaks) > 1 {
			row.Facts = append(row.Facts, Prop{Label: "Ломался", Value: plural(len(m.Breaks), "раз", "раза", "раз")})
		}
	}
	if m.Builtin {
		row.Badges = []Badge{{Text: "встроенный", Color: "#8E8E93"}}
		if m.Source == "" { // способность ядра: нечего включать и копировать
			row.Toggle, row.Actions = "", nil
		} else { // модуль из образа: выключается и копируется, но не удаляется
			row.Actions = []Action{
				{Action: "toggle_module", Target: m.Name, Label: "toggle"},
				{Action: "$goto", Target: moduleCodeAddr(m.Name), Label: "Код модуля"},
				{Action: "copy_module", Target: m.Name, Label: "Скопировать под своим именем",
					Prompt: "Имя копии (латиницей)", Value: m.Name + "-my"},
			}
		}
	}
	return row
}

// buildModuleScreen рисует раздел, который принёс модуль. Модуль возвращает
// то же описание экрана, что и ядро, поэтому клиент не отличает их.
func (s *Server) buildModuleScreen(doc *Document, screenID string) error {
	raw, err := s.eng.ModuleScreenDoc(context.Background(), screenID)
	if err != nil {
		doc.Heading = "Модуль"
		doc.Components = append(doc.Components, Component{Type: "text", Style: "secondary", Text: err.Error()})
		return nil
	}
	// разбираем описание модуля в наш документ: имена полей те же
	body, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var from Document
	if err := json.Unmarshal(body, &from); err != nil {
		return fmt.Errorf("модуль вернул непонятное описание экрана: %w", err)
	}
	doc.Heading = from.Heading
	doc.Subheading = from.Subheading
	doc.Layout = from.Layout
	doc.Components = append(doc.Components, from.Components...)
	doc.ToolbarActions = from.ToolbarActions
	return nil
}

// moduleGrant — экран подтверждения прав. Показываем и то, что модуль просит,
// и то, чего ему не дадут: иначе подтверждение превращается в ритуал.
func (s *Server) moduleGrant(add func(Component), m *engine.Module) {
	asked := map[string]bool{}
	var rows []Component
	for _, n := range m.Needs {
		asked[n] = true
		badge := Badge{Text: "нужно", Color: "#C93400"}
		rows = append(rows, Component{Type: "row", Layout: "wide",
			Subtitle: engine.PrimitiveTitle(n), Caption: engine.PrimitiveWhy(n), Badges: []Badge{badge}})
	}
	// секреты — отдельными строками с тумблером на каждый: выдача поимённая
	s.mu.Lock()
	grants := s.pendingGrants
	s.mu.Unlock()
	for _, name := range m.Secrets {
		toggle, badge := "off", Badge{Text: "не выдаём", Color: "#8E8E93"}
		if grants[name] {
			toggle, badge = "on", Badge{Text: "выдать", Color: "#C93400"}
		}
		title, why := "Секрет «"+name+"»", "Модуль получит значение по имени; выдача — только на это имя"
		if sec, ok := s.eng.Store().Secret(name); ok && sec.Kind == store.SecretSSH {
			title = "Ключ SSH «" + name + "»"
			why = "Приватную часть модуль не увидит: ею воспользуется только SSH-примитив ядра по имени"
		} else if ok && sec.Kind == store.SecretOAuth {
			title = "Токены входа «" + name + "»"
			why = "Модуль их не получит никогда: на сервер MCP он ходит через sema.mcp.call, а токен держит ядро"
		}
		row := Component{Type: "row", Layout: "wide", ID: "grant-" + name,
			Subtitle: title, Caption: why, Toggle: toggle, Badges: []Badge{badge},
			Actions: []Action{{Action: "grant_pending_secret", Target: name, Label: "toggle"}}}
		if _, ok := s.eng.Store().Secret(name); !ok {
			// выдавать нечего: добавьте секрет — и право можно дать с экрана хранилища
			row.Caption = why + " · такого секрета пока нет — добавьте его на экране «Ключи и секреты», право выдаётся там же"
			row.Badges, row.Toggle, row.Actions = []Badge{{Text: "нет в хранилище", Color: "#C4271C"}}, "", nil
		}
		rows = append(rows, row)
	}
	for _, area := range m.Memory {
		toggle, badge := "off", Badge{Text: "не выдаём", Color: "#8E8E93"}
		if grants["memory:"+area] {
			toggle, badge = "on", Badge{Text: "выдать", Color: "#C93400"}
		}
		row := Component{Type: "row", Layout: "wide", ID: "grant-memory-" + area,
			Subtitle: "Память · область «" + area + "»",
			Caption:  "Модуль будет вспоминать и записывать в эту область; каждая запись подписана модулем и задачей, видна на экране «Память»",
			Toggle:   toggle, Badges: []Badge{badge},
			Actions: []Action{{Action: "grant_pending_secret", Target: "memory:" + area, Label: "toggle"}}}
		if _, ok := s.eng.Store().MemoryArea(area); !ok {
			row.Caption = "Такой области пока нет — заведите её на экране «Память», право выдаётся там же"
			row.Badges, row.Toggle, row.Actions = []Badge{{Text: "нет области", Color: "#C4271C"}}, "", nil
		}
		rows = append(rows, row)
	}
	for _, srv := range m.MCP {
		toggle, badge := "off", Badge{Text: "не выдаём", Color: "#8E8E93"}
		if grants["mcp:"+srv] {
			toggle, badge = "on", Badge{Text: "выдать", Color: "#C93400"}
		}
		row := Component{Type: "row", Layout: "wide", ID: "grant-mcp-" + srv,
			Subtitle: "MCP · сервер «" + srv + "»",
			Caption:  "Читающие вызовы модуль делает сам; меняющие ждут вашей кнопки в «Уведомлениях». Ключ модулю не отдаётся: его подставляет ядро",
			Toggle:   toggle, Badges: []Badge{badge},
			Actions: []Action{{Action: "grant_pending_secret", Target: "mcp:" + srv, Label: "toggle"}}}
		if _, ok := s.eng.Store().MCPServer(srv); !ok {
			row.Caption = "Такого сервера MCP пока нет — заведите его на вкладке «Интеграции», право выдаётся там же"
			row.Badges, row.Toggle, row.Actions = []Badge{{Text: "нет сервера", Color: "#C4271C"}}, "", nil
		}
		rows = append(rows, row)
	}
	for _, host := range m.SSH {
		toggle, badge := "off", Badge{Text: "не выдаём", Color: "#8E8E93"}
		if grants["ssh:"+host] {
			toggle, badge = "on", Badge{Text: "выдать", Color: "#C93400"}
		}
		row := Component{Type: "row", Layout: "wide", ID: "grant-ssh-" + host,
			Subtitle: "SSH · сервер «" + host + "»",
			Caption:  "Команды из круга «чтение» — сам; остальные ждут вашего подтверждения на вкладке «Серверы»; ключ модулю не отдаётся",
			Toggle:   toggle, Badges: []Badge{badge},
			Actions: []Action{{Action: "grant_pending_secret", Target: "ssh:" + host, Label: "toggle"}}}
		if _, ok := s.eng.Store().Host(host); !ok {
			row.Caption = "Такого сервера нет — добавьте его на вкладке «Серверы», право выдаётся там же"
			row.Badges, row.Toggle, row.Actions = []Badge{{Text: "нет сервера", Color: "#C4271C"}}, "", nil
		}
		rows = append(rows, row)
	}
	for _, kind := range engine.EventKinds() {
		if m.Handler(kind) == "" {
			continue
		}
		toggle, badge := "off", Badge{Text: "не выдаём", Color: "#8E8E93"}
		if grants["event:"+kind] {
			toggle, badge = "on", Badge{Text: "выдать", Color: "#C93400"}
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "grant-event-" + kind,
			Subtitle: "События · " + engine.EventTitle(kind),
			Caption:  "Модуль будет запускаться на каждое такое событие сразу, а не по расписанию; ошибка модуля не остановит разбор почты",
			Toggle:   toggle, Badges: []Badge{badge},
			Actions: []Action{{Action: "grant_pending_secret", Target: "event:" + kind, Label: "toggle"}}})
	}
	if strings.Contains(m.Code, "sema.data") {
		rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Данные модуля · свои коллекции",
			Caption: "Свои записи в базе Сёмы, другим модулям не видны · удаляются только вместе с модулем по вашему подтверждению",
			Badges:  []Badge{{Text: "своё", Color: "#8E8E93"}}})
	}
	var denied []string
	for _, n := range engine.PrimitiveNames() {
		if !asked[n] {
			denied = append(denied, engine.PrimitiveTitle(n))
		}
	}
	if len(denied) > 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide",
			Subtitle: "Не даём", Caption: strings.Join(denied, ", ") + " — модуль этого не просил, ядро ему и не даст",
			Badges: []Badge{{Text: "закрыто", Color: "#8E8E93"}}})
	}
	if strings.TrimSpace(m.Instructions) != "" {
		rows = append(rows, Component{Type: "row", Layout: "wide",
			Subtitle: "Инструкция для модели",
			Caption:  "Этот текст будет управлять моделью — прочитайте его так же внимательно, как права",
			Detail:   m.Instructions, Expanded: true, Expandable: true,
			Badges: []Badge{{Text: "управляет моделью", Color: "#C93400"}}})
	}
	add(Component{Type: "section", Title: "Что просит модуль «" + m.Title + "»", Children: rows})
	add(Component{Type: "text", Style: "secondary", Text: m.About})
	add(Component{Type: "row", Layout: "wide", Subtitle: "Код модуля",
		Caption: fmt.Sprintf("%d строк · посмотрите, прежде чем включать", strings.Count(m.Code, "\n")+1),
		Detail:  m.Code, Expandable: true})
	add(Component{Type: "button", Actions: []Action{
		{Action: "confirm_module", Target: "enable", Label: "Установить и включить", Style: "primary"},
		{Action: "confirm_module", Target: "install", Label: "Установить выключенным"},
		{Action: "cancel_module", Label: "Отмена"},
	}})
}
