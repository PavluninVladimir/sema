package api

// Экран «Память»: области с правилами, потребители и выданные области,
// уроки и факты с происхождением, граф сущностей, журнал с токенами,
// guardrails. Плюс MCP и хуки сессий для внешних агентов.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

var areaColors = []string{"#7952B3", "#1E7A32", "#2E7DD1", "#B25E09", "#B33A72", "#3A8FA3", "#5A6ACF", "#C7532F"}

func (s *Server) areaColor(name string) string {
	if c, ok := s.areaPalette()[name]; ok {
		return c
	}
	return "#8E8E93"
}

// areaPalette — цвет каждой области по её месту в списке. Список помнится
// areaPaletteTTL: цвет просят на каждый узел графа и каждую строку записи,
// и граф из двухсот узлов делал двести одинаковых запросов к областям
// (5 октября 2026). Области меняются редко, а устаревший на секунду цвет
// новой области безвреден.
func (s *Server) areaPalette() map[string]string {
	s.areaColorsMu.Lock()
	defer s.areaColorsMu.Unlock()
	if s.areaColorsMap != nil && time.Since(s.areaColorsAt) < areaPaletteTTL {
		return s.areaColorsMap
	}
	areas, err := s.eng.Store().MemoryAreas()
	palette := make(map[string]string, len(areas))
	for i, a := range areas {
		palette[a.Name] = areaColors[i%len(areaColors)]
	}
	if err == nil {
		s.areaColorsMap, s.areaColorsAt = palette, time.Now()
	}
	return palette
}

const areaPaletteTTL = 2 * time.Second

var confirmModes = []string{"авто", "спросить", "никогда"}
var ttlOptions = []string{"без срока", "30 дней", "90 дней", "180 дней", "365 дней"}

func confirmTitle(mode string) string {
	switch mode {
	case store.ConfirmAsk:
		return "спросить"
	case store.ConfirmNever:
		return "никогда"
	}
	return "авто"
}

func confirmMode(title string) string {
	switch title {
	case "спросить":
		return store.ConfirmAsk
	case "никогда":
		return store.ConfirmNever
	}
	return store.ConfirmAuto
}

func ttlTitle(days int) string {
	if days <= 0 {
		return "без срока"
	}
	return fmt.Sprintf("%d дней", days)
}

// buildMemoryScreen — маршруты раздела: memory, memory@<область>,
// memory:graph, memory:log, memory:node:<id>, memory:rec:<id>.
func (s *Server) buildMemoryScreen(doc *Document, screen, filter string, limit int) error {
	// внутренняя вкладка — хвостом через «~», как у разделов настроек:
	// «memory~areas» — области и доступ
	tab := ""
	if i := strings.Index(screen, "~"); i >= 0 {
		screen, tab = screen[:i], screen[i+1:]
	}
	switch {
	case screen == "memory:log":
		return s.buildMemoryLog(doc, limit)
	case screen == "memory:sessions":
		return s.buildMemorySessions(doc, limit)
	case strings.HasPrefix(screen, "memory:session:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(screen, "memory:session:"), 10, 64)
		return s.buildMemorySession(doc, id)
	case screen == "memory:graph":
		return s.buildMemoryGraph(doc, 0)
	case strings.HasPrefix(screen, "memory:node:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(screen, "memory:node:"), 10, 64)
		return s.buildMemoryGraph(doc, id)
	case strings.HasPrefix(screen, "memory:rec:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(screen, "memory:rec:"), 10, 64)
		return s.buildMemoryRecord(doc, id)
	}
	area := filter // чипы областей шлют filter; ссылки из карточек — memory@<область>
	if _, a, ok := strings.Cut(screen, "@"); ok && area == "" {
		area = a
	}
	return s.buildMemory(doc, area, limit, tab)
}

// buildMemory — две внутренние вкладки. Одной лентой экран нёс 1 818
// узлов и 217 КБ (замер 12 сентября 2026): области, потребители, выдача
// и правила — это доступ, а записи, разбор и журнал — сама память, и
// смотрят их в разное время.
func (s *Server) buildMemory(doc *Document, filterArea string, limit int, tab string) error {
	st := s.eng.Store()
	doc.Heading = "Память"
	doc.Subheading = "одна на Сёму, его модули и агентов; область — граница доступа, а не склад"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	doc.ToolbarActions = []Action{
		{Action: "$goto", Target: "memory:graph", Label: "Граф"},
		{Action: "$goto", Target: "memory:sessions", Label: "Сессии"},
		{Action: "$goto", Target: "memory:log", Label: "Журнал"},
	}

	areas, err := st.MemoryAreas()
	if err != nil {
		return err
	}
	consumers, _ := st.MemoryConsumers()
	byArea, byKind := st.CountMemories()
	setSubtabs(doc, "memory", tab, []innerTab{
		{ID: "", Name: "Записи"},
		{ID: "areas", Name: "Области и доступ", Count: strconv.Itoa(len(areas))},
	})
	var rows []Component
	if tab != "areas" {
		return s.memoryRecords(doc, filterArea, limit, areas, byArea, byKind)
	}

	// ---- области ----
	rows = nil
	for _, a := range areas {
		var who []string
		for _, c := range consumers {
			if c.Has(a.Name) {
				who = append(who, c.Label())
			}
		}
		caption := "выдана: никому"
		if len(who) > 0 {
			caption = "выдана: " + strings.Join(who, " · ")
		}
		caption += fmt.Sprintf(" · записей %d", byArea[a.Name])
		toggle := "off"
		if a.Outbound {
			toggle = "on"
		}
		// капсула не повторяет ни заголовок строки, ни положение
		// тумблера: «Личное» при заголовке «личное» и «закрыто для
		// облака» при выключенном тумблере — то же самое дважды
		var badges []Badge
		if a.Forget {
			badges = append(badges, Badge{Text: "не запоминать", Color: "#C4271C"})
		}
		if a.Outbound {
			badges = append(badges, Badge{Text: "уходит наружу", Color: "#C93400"})
		}
		// «выдана» бывает на тридцать строк (агент плюс рабочая папка) — в
		// строке счёт, а кому именно, показывает матрица выдачи ниже на этой
		// же вкладке. Список в раскрытии каждой области её повторял: на живой
		// Сёме 6 областей × 31 потребитель — тысяча невидимых элементов
		// (perf-pass 5)
		facts := []Prop{{Label: "Записей", Value: strconv.Itoa(byArea[a.Name])}}
		switch {
		case len(who) == 0:
			facts = append(facts, Prop{Label: "Выдана", Value: "никому"})
		case len(who) == 1:
			facts = append(facts, Prop{Label: "Выдана", Value: who[0]})
		default:
			facts = append(facts, Prop{Label: "Выдана", Value: plural(len(who), "потребитель", "потребителя", "потребителей") + " · кому — в матрице ниже"})
		}
		rows = append(rows, Component{
			Type: "row", Layout: "wide", ID: "area-" + a.Name, Subtitle: a.Name,
			// подпись колонки стоит в заголовке раздела: повторять её у
			// каждой области значит написать одно и то же шесть раз
			Facts:  facts,
			Toggle: toggle, Badges: badges,
			Selects: []Select{
				{Name: "value", Value: confirmTitle(a.Confirm), Options: confirmModes, Action: Action{Action: "set_area_confirm", Target: a.Name}},
				{Name: "value", Value: ttlTitle(a.TTLDays), Options: ttlOptions, Action: Action{Action: "set_area_ttl", Target: a.Name}},
			},
			Actions: []Action{
				{Action: "toggle_area_outbound", Target: a.Name, Label: "toggle"},
				{Action: "$goto", Target: "memory@" + a.Name, Label: "Записи"},
				{Action: "toggle_area_forget", Target: a.Name, Label: map[bool]string{true: "Запоминать", false: "Не запоминать"}[a.Forget]},
				{Action: "delete_area", Target: a.Name, Label: "Удалить", Style: "destructive",
					Confirm: "Удалить область «" + a.Name + "» вместе с записями и узлами графа?", ConfirmOK: "Удалить"},
			},
		})
	}
	for _, sg := range s.eng.AreaSuggestions() {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "suggest-" + sg.Name, Subtitle: sg.Name,
			Caption: sg.Why + " · до вашего решения записи по этой теме не сохраняются",
			Badges:  []Badge{{Text: "Сёма предлагает", Color: "#0058B0"}},
			Actions: []Action{
				{Action: "create_area", Target: sg.Name, Label: "Завести", Style: "primary"},
				{Action: "drop_area_suggestion", Target: sg.Name, Label: "Не надо"},
			}})
	}
	add(Component{Type: "section", Title: "Области · наружу · режим записи · срок", Children: rows})
	add(Component{Type: "form", ID: "newarea", Title: "Новая область",
		Fields: []Field{{Type: "text", Name: "name", Label: "Имя", Hint: "коротко и строчными: дача, машина, код:dochub"},
			{Type: "text", Name: "title", Label: "Название", Hint: "как показывать в списке"}},
		Submit: &Action{Action: "create_area_form", Label: "Завести"}})

	// ---- потребители ----
	// список — при раскрытии: кому что выдано, видно в матрице ниже, а здесь
	// последняя сессия и «Забыть агента». На живой Сёме 31 потребитель —
	// тридцать запросов сессий и полсотни килобайт при каждом открытии
	// вкладки (perf-pass 5)
	add(Component{Type: "section", ID: "memory-consumers", Title: fmt.Sprintf("Потребители · %d · кто подключён и когда обращался", len(consumers)),
		Collapsed: true, Lazy: "memory-consumers"})

	// матрица выдачи: селект «выдать…» внутри строки агента давал девять
	// агентов на шесть областей — 54 отдельных действия и никакой
	// картины. Здесь всё поле видно сразу, и пустая строка бросается в
	// глаза: у Codex в rskhb не выдано ничего, отсюда и отказы
	// порциями: строка матрицы — клетка на каждую область, и тридцать агентов
	// с рабочими папками это сотни кнопок (perf-pass 5)
	limit = s.listLimitFor(doc, limit)
	// свежие первыми: агент с рабочей папкой заводится на каждую папку, и
	// давно молчащие не должны занимать первую порцию. Чат Сёмы в матрице
	// не стоит (прав не спрашивает) — и в счёт порции не идёт
	var показать []*store.MemoryConsumer
	for _, c := range consumers {
		if c.Kind != store.ConsumerChat {
			показать = append(показать, c)
		}
	}
	всего := len(показать)
	sort.SliceStable(показать, func(i, j int) bool { return показать[i].Seen > показать[j].Seen })
	if len(показать) > limit {
		показать = показать[:limit]
	}
	add(s.grantMatrix(показать, areas))
	if всего > len(показать) {
		add(Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d потребителей", len(показать), всего)}}})
	}
	// правила — при раскрытии: маски секретов, пределы и роль правят
	// редко, а с подсказками они весили сотню узлов и десяток килобайт
	add(Component{Type: "section", ID: "memory-rules", Title: "Правила · guardrails",
		Collapsed: true, Lazy: "memory-rules"})
	return nil
}

// memoryConsumersFragment — потребители памяти: последняя сессия и «Забыть
// агента»; содержимое ленивого раздела вкладки «Области и доступ».
func (s *Server) memoryConsumersFragment(_ string, _ string, _ int) ([]Component, error) {
	st := s.eng.Store()
	areas, err := st.MemoryAreas()
	if err != nil {
		return nil, err
	}
	consumers, _ := st.MemoryConsumers()
	var rows []Component
	for _, c := range consumers {
		caption := "области: " + strings.Join(c.Areas, ", ")
		badges := []Badge{{Text: "выдано", Color: "#1E7A32"}}
		if len(c.Areas) == 0 {
			caption = "области не выданы — на все обращения отказ"
			badges = []Badge{{Text: "ждёт выдачи", Color: "#C93400"}}
		}
		if last := st.LastMemorySession(c.ID); last != nil {
			caption += " · последняя сессия " + whenText(last.Started)
		} else if c.Seen != "" {
			caption += " · обращался " + whenText(c.Seen)
		}
		var free []string
		for _, a := range areas {
			if !c.Has(a.Name) {
				free = append(free, a.Name)
			}
		}
		row := Component{Type: "row", Layout: "wide", ID: fmt.Sprintf("consumer-%d", c.ID),
			Subtitle: c.Label(), Caption: caption, Badges: badges}
		_ = free
		if c.Kind == store.ConsumerMCP {
			row.Actions = append(row.Actions, Action{Action: "delete_consumer", Target: strconv.FormatInt(c.ID, 10), Label: "Забыть агента", Style: "destructive",
				Confirm: "Убрать потребителя «" + c.Label() + "»? При следующем обращении он появится снова без областей.", ConfirmOK: "Убрать"})
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, Component{Type: "text", Style: "secondary", Text: "Потребителей пока нет: агент появится здесь при первом обращении к памяти."})
	}
	return rows, nil
}

// memoryRulesFragment — правила guardrails и форма новой маски.
func (s *Server) memoryRulesFragment(_ string, _ string, _ int) ([]Component, error) {
	var out []Component
	s.memoryRules(func(c Component) {
		// сам раздел «Правила» внутри части не нужен: часть и есть он
		if c.Type == "section" && c.Title == "Правила · guardrails" {
			out = append(out, c.Children...)
			return
		}
		out = append(out, c)
	})
	return out, nil
}

// memoryRecords — вкладка «Записи»: разбор, ждущие, уроки и факты, граф,
// журнал и кто пользуется.
func (s *Server) memoryRecords(doc *Document, filterArea string, limit int, areas []*store.MemoryArea, byArea, byKind map[string]int) error {
	st := s.eng.Store()
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	var rows []Component

	// ---- разбор памяти самой Сёмой ----
	waiting, last := s.eng.MemoryReviewStat()
	toggle := "off"
	caption := "записи ждут вашей кнопки: подтвердить, исправить или забыть"
	if s.eng.MemoryReviewOn() {
		toggle = "on"
		caption = "Сёма сам смотрит новые записи в сведении памяти: оставляет, переписывает понятнее или забывает мусор"
	}
	caption += "\nждут разбора: " + strconv.Itoa(waiting)
	if last != "" {
		caption += " · последний разбор " + whenText(last)
	}
	caption += " · области с режимом «спросить» Сёма не трогает, решения видны в журнале"
	add(Component{Type: "section", Title: "Разбор памяти", Children: []Component{{
		Type: "row", Layout: "wide", ID: "memory-review", Subtitle: "Сёма подтверждает записи сам", Toggle: toggle, Caption: caption,
		Actions: []Action{
			{Action: "toggle_memory_review", Label: "toggle"},
			{Action: "run_memory_review", Label: "Разобрать сейчас"},
			{Action: "$goto", Target: "memory:log", Label: "Журнал"},
		}}}})

	// ---- ожидающие подтверждения ----
	if pending, _ := st.PendingMemories(); len(pending) > 0 {
		// сами записи приезжают при раскрытии: ждущих бывает сотня, каждая
		// с текстом и кнопками, и экран «Память» нёс 2 228 узлов
		add(Component{Type: "section", ID: "memory-pending",
			Title:     fmt.Sprintf("Ждут вашего решения · %d", len(pending)),
			Collapsed: true, Expanded: len(pending) <= fragmentLimit, Lazy: "memory-pending",
			SearchHint: "Слово из записи"})
		add(Component{Type: "text", Style: "secondary",
			Text: "Это извлечено из чатов и задач в область «личное», а она в режиме «спросить». Пустые итоги вроде «писем нет» больше сюда не попадают; что уже накопилось — решите разом или включите области режим «авто» (тогда решает разбор памяти самой Сёмы)."})
		add(Component{Type: "button", Actions: []Action{
			{Action: "forget_all_pending", Label: "Забыть все ждущие", Style: "destructive",
				Confirm: fmt.Sprintf("Забыть все %d ждущих записей? Они не попадут в память.", len(pending)), ConfirmOK: "Забыть"},
			{Action: "accept_all_pending", Label: "Запомнить все",
				Confirm: fmt.Sprintf("Принять все %d ждущих записей как есть?", len(pending)), ConfirmOK: "Запомнить"},
		}})
	}

	// ---- уроки и факты ----
	doc.Filters = []Filter{{ID: "", Title: fmt.Sprintf("все · %d", byKind[store.MemLesson]+byKind[store.MemFact]+byKind[store.MemCrystal])}}
	for _, a := range areas {
		doc.Filters = append(doc.Filters, Filter{ID: a.Name, Title: fmt.Sprintf("%s · %d", a.Name, byArea[a.Name])})
	}
	doc.ActiveFilter = filterArea
	// записи памяти длинные, а экран обновляется раз в пять секунд:
	// показываем свежие, остальные — по кнопке
	// пятнадцать: у записи текст, факты, две кнопки с подтверждениями и
	// меню — двадцать пять весили 575 узлов, половину экрана
	if limit <= 0 || limit == defaultRows || limit > 400 {
		limit = 15
	}
	list, _ := st.Memories(filterArea, "", limit+1)
	moreMemories := len(list) > limit
	if moreMemories {
		list = list[:limit]
	}
	rows = nil
	for _, m := range list {
		if m.Kind == store.MemObservation || m.Status != store.MemOK {
			continue
		}
		rows = append(rows, s.memoryRow(m))
	}
	title := "Уроки и факты"
	if filterArea != "" {
		title += " · " + filterArea
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Записей пока нет",
			"Они появятся из чатов, модулей и сессий агентов: урок — это факт, повторившийся дважды или подтверждённый вами", "brain"))
	}
	if moreMemories {
		title += fmt.Sprintf(" · свежие %d", len(rows))
	}
	add(Component{Type: "section", Title: title, Children: rows})
	if moreMemories {
		add(Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · сейчас %d", len(rows))},
		}})
	}
	add(Component{Type: "text", Style: "secondary",
		Text: "Урок — факт, повторившийся дважды или подтверждённый вами; в промпт идёт раньше эпизодов. Исправление создаёт новую версию с источником «человек», старая остаётся в истории."})

	// ---- граф ----
	// сам граф — на своём экране: в общем документе он весил 82 КБ и ехал
	// заново каждые пять секунд, хотя смотрят его редко
	if n, e := st.CountMemoryGraph(); n > 0 {
		add(Component{Type: "row", Layout: "wide", ID: "memory-graph", Icon: "circle.hexagongrid",
			Subtitle: "Граф памяти",
			Caption:  "цвет — область, размер — число связей",
			Facts: []Prop{
				{Label: "Узлов", Value: strconv.Itoa(n)},
				{Label: "Связей", Value: strconv.Itoa(e)},
			},
			Actions: []Action{{Action: "$goto", Target: "memory:graph", Label: "Открыть граф"}}})
	}

	// ---- журнал ----
	since := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	tokens, saved, denied := st.MemoryLogTotals(since)
	entries, _ := st.MemoryLog(8)
	rows = nil
	for _, e := range entries {
		rows = append(rows, memoryLogRow(e))
	}
	if len(rows) == 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Обращений ещё не было"})
	}
	// доля сбережённого: сколько контекста не пришлось собирать заново
	savedPct := ""
	if tokens+saved > 0 {
		savedPct = fmt.Sprintf(" (%d %%)", saved*100/(tokens+saved))
	}
	add(Component{Type: "section", Title: fmt.Sprintf("Журнал · за сутки из памяти %d токенов, сбережено ≈ %d%s, отказов %d", tokens, saved, savedPct, denied), Children: rows})
	// кто пользуется: у чата экономия — письма, которые не собирались; у
	// внешних агентов — только сводка прошлой сессии на старте, остальное
	// измерить нечем, и это сказано, а не показано нулём
	var use []Component
	for _, u := range st.MemoryLogByConsumer(since) {
		savedText := "не считается"
		if u.Measured {
			savedText = fmt.Sprintf("≈ %d", u.Saved)
		}
		use = append(use, Component{Type: "row", Layout: "wide", Subtitle: u.Consumer,
			Facts: []Prop{{Label: "Обращений", Value: strconv.Itoa(u.Calls)}, {Label: "Выдано", Value: strconv.Itoa(u.Tokens)}, {Label: "Сбережено", Value: savedText}}})
	}
	if len(use) > 0 {
		add(Component{Type: "section", Title: "Кто пользуется · за сутки", Children: use})
		add(Component{Type: "text", Style: "secondary", Text: "«Сбережено» считается там, где есть с чем сравнить: в чате Сёмы — письма и чеки, которые не собирались, у агентов — сводка прошлой сессии вместо перечитывания. Остальные обращения экономию не измеряют."})
	}

	return nil
}

// Вид записи одним словом и его цвет — общие для строки списка и экрана
// одной записи: одно и то же слово в двух местах не должно расходиться.
var memKindTitles = map[string]string{store.MemLesson: "урок", store.MemFact: "факт", store.MemCrystal: "кристалл", store.MemObservation: "наблюдение"}
var memKindColors = map[string]string{store.MemLesson: "#0058B0", store.MemFact: "#8E8E93", store.MemCrystal: "#1E7A32", store.MemObservation: "#8E8E93"}

// memoryRow — строка записи: вид, область, происхождение, версия, действия.
func (s *Server) memoryRow(m *store.Memory) Component {
	st := s.eng.Store()
	kind := memKindTitles[m.Kind]
	kindColor := memKindColors[m.Kind]
	badges := []Badge{{Text: kind, Color: kindColor}, {Text: m.Area, Color: s.areaColor(m.Area)}}
	if m.Status == store.MemPending {
		badges = append(badges, Badge{Text: "ждёт решения", Color: "#C93400"})
	}
	if m.Confirmed {
		badges = append(badges, Badge{Text: "подтверждено", Color: "#1E7A32"})
	}
	caption := m.Agent + " · " + whenText(m.Created)
	if m.Model != "" {
		caption += " · извлёк " + m.Model
	}
	if m.Weight > 1 {
		caption += fmt.Sprintf(" · повторов %d", m.Weight)
	}
	if len(m.Sources) > 1 {
		caption += " · источники: " + strings.Join(m.Sources, ", ")
	}
	if m.Supersedes > 0 {
		if old, err := st.MemoryByID(m.Supersedes); err == nil {
			caption += " · заменила «" + clipRunes(old.Text, 40) + "» от " + whenText(old.Created)
		}
	}
	if m.Expires != "" {
		caption += " · до " + whenText(m.Expires)
	}
	id := strconv.FormatInt(m.ID, 10)
	row := Component{Type: "row", Layout: "wide", ID: "mem-" + id, Subtitle: m.Text, Caption: caption, Badges: badges}
	if history, _ := st.MemoryHistory(m.ID); len(history) > 1 {
		var lines []string
		for _, h := range history[1:] {
			lines = append(lines, whenText(h.Created)+" · "+h.Agent+": "+h.Text)
		}
		row.Detail, row.Expandable = "История версий:\n"+strings.Join(lines, "\n"), true
	}
	// строка списка коротка и обрезана, а про запись бывает что сказать:
	// откуда пришла, чем её переписывали, к какому месту в коде привязана.
	// Всё это — на своём экране; переход кнопкой не рисуется, по нему
	// кликается сама строка, и место главного действия он не занимает
	открыть := Action{Action: "$goto", Target: "memory:rec:" + id, Label: "Открыть"}
	if m.Status == store.MemPending {
		row.Actions = []Action{
			открыть,
			{Action: "accept_memory", Target: id, Label: "Запомнить", Style: "primary"},
			{Action: "fix_memory", Target: id, Label: "Исправить", Prompt: "Текст записи", Value: m.Text},
			{Action: "forget_memory", Target: id, Label: "Не надо"},
		}
		return row
	}
	row.Actions = append(row.Actions, открыть)
	if !m.Confirmed {
		row.Actions = append(row.Actions, Action{Action: "accept_memory", Target: id, Label: "Подтвердить"})
	}
	row.Actions = append(row.Actions, Action{Action: "fix_memory", Target: id, Label: "Исправить", Prompt: "Текст записи", Value: m.Text})
	if m.Kind == store.MemFact {
		row.Actions = append(row.Actions, Action{Action: "promote_memory", Target: id, Label: "Закрепить уроком"})
	}
	row.Actions = append(row.Actions, Action{Action: "forget_memory", Target: id, Label: "Забыть", Style: "destructive",
		Confirm: "Забыть запись «" + clipRunes(m.Text, 60) + "»?", ConfirmOK: "Забыть"})
	return row
}

// buildMemoryRecord — одна запись памяти целиком: откуда она взялась, чем
// её переписывали, к какому месту в коде привязана и что из неё выросло в
// графе. В списке от записи видна строка с обрезанным текстом, и человеку
// негде было спросить «а это вообще откуда и кто это утверждает».
func (s *Server) buildMemoryRecord(doc *Document, id int64) error {
	st := s.eng.Store()
	m, err := st.MemoryByID(id)
	if err != nil {
		return err
	}
	sid := strconv.FormatInt(m.ID, 10)
	doc.Heading = m.Text
	doc.Layout = "wide"
	doc.ToolbarActions = []Action{{Action: "$goto", Target: "memory", Label: "‹ Память"}}
	части := []string{memKindTitles[m.Kind], "область «" + m.Area + "»"}
	switch {
	case m.Status == store.MemPending:
		части = append(части, "ждёт вашего решения — в промпт не попадает")
	case m.Confirmed:
		части = append(части, "подтверждено вами")
	default:
		части = append(части, "Сёма записал сам, вы это не подтверждали")
	}
	doc.Subheading = strings.Join(части, " · ")
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	// действия те же, что у строки в списке: решение о записи принимается
	// одинаково, где бы человек её ни встретил. Переход на этот же экран
	// из списка кнопок убираем — открывать самого себя незачем
	var кнопки []Action
	for _, a := range s.memoryRow(m).Actions {
		if a.Action == "$goto" && a.Target == "memory:rec:"+sid {
			continue
		}
		кнопки = append(кнопки, a)
	}
	if len(кнопки) > 0 {
		add(Component{Type: "button", ID: "rec-действия-" + sid, Actions: кнопки})
	}

	// ---- откуда ----
	facts := []Prop{{Label: "Записал", Value: m.Agent}}
	if m.Agent == "" {
		facts = []Prop{{Label: "Записал", Value: "не сказано"}}
	}
	if m.Model != "" {
		facts = append(facts, Prop{Label: "Извлекла модель", Value: m.Model})
	}
	facts = append(facts, Prop{Label: "Когда", Value: whenText(m.Created)})
	if m.Weight > 1 {
		facts = append(facts, Prop{Label: "Повторов", Value: strconv.Itoa(m.Weight)})
	}
	if len(m.Sources) > 0 {
		facts = append(facts, Prop{Label: "Источники", Value: strings.Join(m.Sources, ", ")})
	}
	if m.Expires != "" {
		facts = append(facts, Prop{Label: "Срок", Value: whenText(m.Expires)})
	}
	откуда := Component{Type: "row", Layout: "wide", ID: "rec-откуда-" + sid,
		Subtitle: "Происхождение", Facts: facts}
	var сессия *store.MemorySession
	if m.SessionID > 0 {
		if v, err := st.MemorySession(m.SessionID); err == nil {
			сессия = v
			откуда.Caption = "сессия «" + v.Agent + "» от " + whenText(v.Started)
			откуда.Actions = []Action{{Action: "$goto",
				Target: "memory:session:" + strconv.FormatInt(v.ID, 10), Label: "Открыть сессию"}}
		}
	}
	if сессия == nil {
		откуда.Caption = "сессии нет: так записывают чат, модуль или вы сами"
	}
	add(Component{Type: "section", Title: "Откуда", Children: []Component{откуда}})

	// ---- история версий ----
	история, _ := st.MemoryHistory(m.ID)
	var версии []Component
	for i, h := range история {
		label := h.Agent + " · " + whenText(h.Created)
		if h.Model != "" {
			label += " · извлёк " + h.Model
		}
		строка := Component{Type: "row", Layout: "wide",
			ID:       fmt.Sprintf("rec-версия-%d", h.ID),
			Subtitle: h.Text, Caption: label}
		if i == 0 {
			строка.Badges = []Badge{{Text: "нынешняя", Color: "#1E7A32"}}
		} else {
			строка.Actions = []Action{{Action: "$goto",
				Target: "memory:rec:" + strconv.FormatInt(h.ID, 10), Label: "Открыть"}}
		}
		версии = append(версии, строка)
	}
	title := "История версий"
	if len(версии) < 2 {
		версии = []Component{emptyState("Запись не переписывали",
			"Исправление не правит текст на месте, а заводит новую версию: старая остаётся здесь вместе с тем, кто её утверждал. "+
				"У этой записи версия одна — та, что в заголовке.", "clock.arrow.circlepath")}
	} else {
		title += fmt.Sprintf(" · %d", len(версии))
	}
	add(Component{Type: "section", Title: title, Children: версии})

	// ---- место в коде ----
	var места []Component
	for i, п := range st.MemoryPlaces(m.ID) {
		name := п.File
		if name == "" {
			name = п.Symbol
		}
		if name == "" && п.Dir != "" {
			name = п.Dir + "/"
		}
		var координата []Prop
		if п.Dir != "" && п.File == "" {
			координата = append(координата, Prop{Label: "Папка", Value: п.Dir + "/"})
		}
		if п.Symbol != "" {
			координата = append(координата, Prop{Label: "Символ", Value: п.Symbol})
		}
		if п.Line > 0 {
			координата = append(координата, Prop{Label: "Строка", Value: strconv.Itoa(п.Line) + " (подсказка)"})
		}
		if п.Repo != "" {
			координата = append(координата, Prop{Label: "Репозиторий", Value: п.Repo})
		}
		if п.Source != "" {
			координата = append(координата, Prop{Label: "Утверждает", Value: п.Source})
		}
		if п.Verified != "" {
			координата = append(координата, Prop{Label: "Сверена", Value: whenText(п.Verified)})
		}
		строка := Component{Type: "row", Layout: "wide",
			ID:   fmt.Sprintf("rec-место-%d", i),
			Icon: "chevron.left.forwardslash.chevron.right",
			// координату сверяет житель машины: файлы репозитория видит
			// только он, Сёма живёт в контейнере
			Subtitle: name, Facts: координата}
		switch {
		case п.Doubtful && strings.HasPrefix(п.Source, engine.PrincipleSource):
			строка.Level = "warn"
			строка.Badges = []Badge{{Text: "устарело", Color: "#C93400"}}
			строка.Caption = "абзаца с таким заголовком в CLAUDE.md больше нет; запись осталась, привязка устарела"
		case п.Doubtful && п.Symbol == "" && п.Dir != "":
			строка.Level = "warn"
			строка.Badges = []Badge{{Text: "папки больше нет", Color: "#C93400"}}
			строка.Caption = "место могло переехать: обновите привязку или снимите её, знание останется"
		case п.Doubtful:
			строка.Level = "warn"
			строка.Badges = []Badge{{Text: "символа больше нет", Color: "#C93400"}}
			строка.Caption = "устарела координата, а не урок: место могло переехать, и знание осталось верным"
		}
		// снять можно любую привязку: знание остаётся, уходит координата.
		// Принцип из CLAUDE.md при следующем разборе завёлся бы снова —
		// поэтому у него вторая кнопка: снять и не заводить, пока абзац
		// не изменился
		target := placeTarget(п)
		строка.Actions = []Action{{Action: "unbind_place", Target: target, Label: "Снять", Style: "destructive",
			Confirm: "Снять привязку «" + name + "»? Запись останется, уйдёт только место.", ConfirmOK: "Снять"}}
		if strings.HasPrefix(п.Source, engine.PrincipleSource) {
			heading := strings.TrimPrefix(п.Source, engine.PrincipleSource)
			if s.eng.PrincipleBlocked(heading, engine.PrincipleHash(m)) {
				строка.Badges = append(строка.Badges, Badge{Text: "не заводить снова", Color: "#8E8E93"})
				строка.Actions = append(строка.Actions, Action{Action: "unblock_principle", Target: target, Label: "Заводить снова"})
			} else {
				строка.Actions = append(строка.Actions, Action{Action: "block_principle", Target: target, Label: "Снять и не заводить",
					Style: "destructive", Confirm: "Снять привязку и не заводить её снова, пока абзац «" + heading + "» в CLAUDE.md не изменится?",
					ConfirmOK: "Снять и не заводить"})
			}
		}
		места = append(места, строка)
	}
	if len(места) == 0 {
		места = append(места, emptyState("Место в коде не указано",
			"Привязка необязательна: память живёт и без неё. Ставит её тот, кто правит код, — "+
				"полями file и symbol у memory_remember; Сёма файлов репозитория не видит вовсе.",
			"chevron.left.forwardslash.chevron.right"))
	}
	add(Component{Type: "section", Title: "Место в коде", Children: места})

	// ---- кристалл и связи в графе ----
	var связи []Component
	if сессия != nil && сессия.Crystal > 0 && сессия.Crystal != m.ID {
		if к, err := st.MemoryByID(сессия.Crystal); err == nil {
			связи = append(связи, Component{Type: "row", Layout: "wide", ID: "rec-кристалл-" + sid,
				Subtitle: clipRunes(к.Text, 120), Caption: "кристалл той же сессии — сводка, в которую вошла эта запись",
				Badges:  []Badge{{Text: "кристалл", Color: memKindColors[store.MemCrystal]}},
				Actions: []Action{{Action: "$goto", Target: "memory:rec:" + strconv.FormatInt(к.ID, 10), Label: "Открыть кристалл"}}})
		}
	}
	edges, _ := st.MemoryEdges()
	for _, e := range edges {
		if e.MemoryID != m.ID {
			continue
		}
		от, err1 := st.MemoryNode(e.From)
		до, err2 := st.MemoryNode(e.To)
		if err1 != nil || err2 != nil {
			continue
		}
		связи = append(связи, Component{Type: "row", Layout: "wide",
			ID:       fmt.Sprintf("rec-связь-%d", e.ID),
			Subtitle: от.Name + " —" + e.Relation + "→ " + до.Name, Meta: до.Area,
			Actions: []Action{{Action: "$goto",
				Target: "memory:node:" + strconv.FormatInt(от.ID, 10), Label: "К узлу"}}})
	}
	if len(связи) == 0 {
		почему := "Сущности и связи Сёма достаёт из записей ролью «Память» раз в сутки — из этой ещё не доставал."
		if m.Graphed {
			почему = "Сёма уже смотрел эту запись и сущностей в ней не нашёл: связывать нечего."
		}
		связи = append(связи, emptyState("В графе записи нет", почему,
			"point.3.connected.trianglepath.dotted"))
	}
	add(Component{Type: "section", Title: "Кристалл и связи", Children: связи})
	return nil
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func memoryLogRow(e store.MemoryLogEntry) Component {
	// «выдано» зелёным у каждой строки — цвет без смысла: зелёным
	// отмечаем отданное наружу, обычное обращение остаётся серым
	badge := Badge{Text: "выдано"}
	if !e.Granted {
		badge = Badge{Text: "отказано", Color: "#C4271C"}
	}
	facts := []Prop{}
	if e.Area != "" {
		facts = append(facts, Prop{Label: "Область", Value: e.Area})
	}
	if e.Tokens > 0 {
		v := fmt.Sprintf("%d", e.Tokens)
		if e.Saved > 0 {
			v += fmt.Sprintf(" вместо ≈ %d", e.Saved+e.Tokens)
		}
		facts = append(facts, Prop{Label: "Токенов", Value: v})
	}
	// подробность режется по словам, а не посреди слова, и целиком видна
	// в раскрытии: обрывок «…запись о серв» не сообщает ничего
	caption := clipWords(strings.TrimSpace(e.Detail), 90)
	var props []Prop
	if len([]rune(strings.TrimSpace(e.Detail))) > 90 {
		props = append(props, Prop{Label: "Подробность", Value: e.Detail})
	}
	return Component{Type: "row", Layout: "wide", Subtitle: e.Consumer + " · " + actionTitle(e.Action),
		Caption: caption, Meta: whenText(e.At), Facts: facts, Props: props,
		Expandable: len(props) > 0, Badges: []Badge{badge}}
}

func actionTitle(a string) string {
	return map[string]string{"recall": "вспомнить", "remember": "запомнить", "deny": "отказ", "reject": "отвергнуто",
		"observe": "сессия", "forget": "забыть", "merge": "слияние"}[a]
}

func (s *Server) buildMemoryLog(doc *Document, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Память · журнал"
	doc.Layout = "wide"
	doc.ToolbarActions = []Action{{Action: "$goto", Target: "memory", Label: "‹ Память"}}
	since := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	tokens, saved, denied := st.MemoryLogTotals(since)
	doc.Subheading = fmt.Sprintf("за сутки из памяти ушло %d токенов, сбережено ≈ %d (только там, где есть с чем сравнить), отказов %d · хранятся последние 2000 записей", tokens, saved, denied)
	// двести обращений разом — 4 402 узла и 350 КБ (замер 12 сентября
	// 2026); первая страница двадцать пять, остальное по «ещё»
	if limit <= 0 || limit == defaultRows {
		limit = 25
	}
	entries, err := st.MemoryLog(limit + 1)
	if err != nil {
		return err
	}
	ещё := len(entries) > limit
	if ещё {
		entries = entries[:limit]
	}
	rows := make([]Component, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, memoryLogRow(e))
	}
	if len(rows) == 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Обращений ещё не было"})
	}
	doc.Components = append(doc.Components, Component{Type: "section", Title: "Обращения", Children: rows})
	if ещё {
		doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · сейчас %d", len(rows))}}})
	}
	return nil
}

// buildMemorySessions — список сессий всех потребителей, как в agentmemory:
// кто, когда, чем кончилось, есть ли кристалл; по клику — воспроизведение.
// grantMatrix — «кто × что» одной таблицей. Клетка переключается
// нажатием: выдано — синяя с галочкой, выдано и уходит наружу облачной
// модели — жёлтая со стрелкой, не выдано — пустая.
func (s *Server) grantMatrix(consumers []*store.MemoryConsumer, areas []*store.MemoryArea) Component {
	m := Component{Type: "matrix", Title: "Выдача областей · кто что видит"}
	наружу := map[string]bool{}
	for _, a := range areas {
		m.Columns = append(m.Columns, a.Name)
		наружу[a.Name] = a.Outbound
	}
	for _, c := range consumers {
		// чат самой Сёмы прав не спрашивает: он и есть Сёма
		if c.Kind == store.ConsumerChat {
			continue
		}
		name, путь := c.Name, c.Path
		row := Component{Type: "row", ID: fmt.Sprintf("выдача-%d", c.ID), Subtitle: name, Caption: путь}
		for _, a := range areas {
			клетка := Cell{Title: name + " · " + a.Name}
			switch {
			case !c.Has(a.Name):
				клетка.State = "off"
				клетка.Title = "не выдано: " + клетка.Title
				клетка.Action = Action{Action: "grant_area_cell", Target: fmt.Sprintf("%d~%s", c.ID, a.Name), Label: "Выдать"}
			case наружу[a.Name]:
				клетка.State = "out"
				клетка.Title = "выдано, и область открыта наружу: " + клетка.Title
				клетка.Action = Action{Action: "revoke_consumer_area", Target: fmt.Sprintf("%d~%s", c.ID, a.Name), Label: "Отозвать"}
			default:
				клетка.State = "on"
				клетка.Title = "выдано: " + клетка.Title
				клетка.Action = Action{Action: "revoke_consumer_area", Target: fmt.Sprintf("%d~%s", c.ID, a.Name), Label: "Отозвать"}
			}
			row.Cells = append(row.Cells, клетка)
		}
		m.Children = append(m.Children, row)
	}
	m.Caption = "Нажатие переключает. Жёлтая клетка со стрелкой — область открыта наружу: её записи уходят облачной модели."
	return m
}

func (s *Server) buildMemorySessions(doc *Document, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Память · сессии"
	doc.Subheading = "Каждый разговор с Сёмой и каждая сессия агента — запись со сводкой; из закрытой сессии сведение делает кристалл, факты и граф"
	doc.Layout = "wide"
	doc.ToolbarActions = []Action{{Action: "$goto", Target: "memory", Label: "‹ Память"}}
	if limit <= 0 || limit == defaultRows {
		limit = 25
	}
	list, err := st.MemorySessions(limit + 1)
	if err != nil {
		return err
	}
	ещё := len(list) > limit
	if ещё {
		list = list[:limit]
	}
	rows := make([]Component, 0, len(list))
	for _, m := range list {
		rows = append(rows, s.sessionRow(m))
	}
	if len(rows) == 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Сессий ещё не было"})
	}
	doc.Components = append(doc.Components, Component{Type: "section", Title: fmt.Sprintf("Сессии · последние %d", len(rows)), Children: rows})
	if ещё {
		doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · сейчас %d", len(rows))}}})
	}
	return nil
}

func (s *Server) sessionRow(m *store.MemorySession) Component {
	st := s.eng.Store()
	id := strconv.FormatInt(m.ID, 10)
	badges := []Badge{}
	caption := "началась " + whenText(m.Started)
	switch {
	case m.Ended == "":
		badges = append(badges, Badge{Text: "идёт", Color: "#1E7A32"})
	case m.Crystal > 0:
		badges = append(badges, Badge{Text: "кристалл", Color: "#1E7A32"})
		caption += " · закрыта " + whenText(m.Ended)
	case m.Crystal < 0:
		badges = append(badges, Badge{Text: "без кристалла", Color: "#8E8E93"})
		caption += " · закрыта " + whenText(m.Ended)
	default:
		badges = append(badges, Badge{Text: "ждёт сведения", Color: "#B7791F"})
		caption += " · закрыта " + whenText(m.Ended)
	}
	if m.Model != "" {
		caption += " · модель " + m.Model
	}
	caption += fmt.Sprintf(" · сводка %d символов", len([]rune(m.Summary)))
	if born, _ := st.MemoriesBySession(m.ID); len(born) > 0 {
		caption += fmt.Sprintf(" · записей %d", len(born))
	}
	// кристалл — не мета, а суть сессии: он идёт отдельной строкой
	// обычным шрифтом, а не хвостом к перечислению времён и модели
	итог := ""
	if m.Crystal > 0 {
		if cr, err := st.MemoryByID(m.Crystal); err == nil {
			итог = clipWords(cr.Text, 200)
		}
	}
	row := Component{Type: "row", Layout: "wide", ID: "session-" + id, Subtitle: m.Agent,
		Title: итог, MetaInline: итог != "", Meta: caption, Badges: badges,
		Actions: []Action{{Action: "$goto", Target: "memory:session:" + id, Label: "Воспроизвести"}}}
	if итог == "" {
		row.Caption, row.Meta, row.MetaInline = caption, "", false
	}
	// свести можно только закрытую сессию, которой есть что сводить:
	// у закрытой с пустой сводкой кнопка обещала бы невозможное
	if m.Ended != "" && m.Crystal == 0 && strings.TrimSpace(m.Summary) != "" {
		row.Actions = append(row.Actions, Action{Action: "crystallize_session", Target: id, Label: "Свести сейчас"})
	}
	return row
}

// buildMemorySession — воспроизведение: сводка как была, кристалл и всё,
// что из неё извлечено.
func (s *Server) buildMemorySession(doc *Document, id int64) error {
	st := s.eng.Store()
	m, err := st.MemorySession(id)
	if err != nil {
		return err
	}
	doc.Heading = "Сессия · " + m.Agent
	doc.Layout = "wide"
	doc.ToolbarActions = []Action{{Action: "$goto", Target: "memory:sessions", Label: "‹ Сессии"}}
	head := s.sessionRow(m)
	head.Actions = nil
	if m.Ended == "" || m.Crystal <= 0 {
		head.Actions = append(head.Actions, Action{Action: "crystallize_session", Target: strconv.FormatInt(id, 10), Label: "Свести сейчас", Style: "primary"})
	}
	head.Actions = append(head.Actions, Action{Action: "delete_session", Target: strconv.FormatInt(id, 10), Label: "Забыть сессию", Style: "destructive",
		Confirm: "Забыть сессию? Записи, извлечённые из неё, останутся.", ConfirmOK: "Забыть"})
	doc.Components = append(doc.Components, Component{Type: "section", Title: "Сессия", Children: []Component{head}})
	born, _ := st.MemoriesBySession(id)
	var rows []Component
	for _, b := range born {
		rows = append(rows, s.memoryRow(b))
	}
	if len(rows) > 0 {
		doc.Components = append(doc.Components, Component{Type: "section", Title: "Что извлечено", Children: rows})
	}
	// сводка показывается сразу: за раскрытием прятался единственный
	// текст, ради которого на экран и заходят
	summary := strings.TrimSpace(m.Summary)
	свод := []Component{}
	if summary == "" {
		свод = append(свод, emptyState("Сводки нет",
			"Сессия ещё идёт или агент ничего не прислал. Сводку присылает хук конца сессии; закрытая по тишине сессия остаётся без неё.",
			"brain"))
	} else {
		свод = append(свод,
			Component{Type: "row", Layout: "wide", ID: "summary-" + strconv.FormatInt(id, 10),
				Subtitle: "Текст сессии", Detail: summary, Expanded: true},
			Component{Type: "text", Style: "secondary", Text: fmt.Sprintf(
				"%d символов · строки с секретами убраны масками до сохранения", len([]rune(summary)))})
	}
	doc.Components = append(doc.Components, Component{Type: "section", Title: "Сводка · как её прислал агент", Children: свод})
	return nil
}

// memoryGraphComponent — граф для клиента: узлы с цветом области и размером
// по числу связей; закрытые для выбранной облачной модели узлы бледнее.
// Вторым значением возвращаются одиночки — узлы, у которых нет ни одной
// связи. В кольцевой раскладке они занимали половину картинки и не
// говорили ничего: рисовать связи узла, у которого связей нет, нечем.
// В графе их не будет, они уходят строкой капсул под ним.
// memoryGraph — граф памяти, прочитанный один раз за сборку экрана: им же
// пользуется карточка узла, а не читает все связи заново.
type memoryGraph struct {
	byID  map[int64]*store.MemoryNode
	edges []*store.MemoryEdge
}

func (s *Server) memoryGraphComponent(focus int64, onlyArea string) (Component, []*store.MemoryNode, memoryGraph) {
	st := s.eng.Store()
	nodes, _ := st.MemoryNodes()
	edges, _ := st.MemoryEdges()
	read := memoryGraph{byID: make(map[int64]*store.MemoryNode, len(nodes)), edges: edges}
	for _, n := range nodes {
		read.byID[n.ID] = n
	}
	s.mu.Lock()
	eyes := s.graphEyes
	s.mu.Unlock()
	cloud := eyes != "" && eyes != "вы" && s.eng.ModelIsCloud(eyes)
	outbound := map[string]bool{}
	if areas, err := st.MemoryAreas(); err == nil {
		for _, a := range areas {
			outbound[a.Name] = a.Outbound
		}
	}
	deg := map[int64]int{}
	for _, e := range edges {
		deg[e.From]++
		deg[e.To]++
	}
	// Полный граф памяти растёт без предела, а раскладка считает силы для
	// каждой пары узлов: на пятистах узлах окно переставало отзываться.
	// Показываем самые связанные — они и есть каркас; предел правится
	// настройкой, а не гвоздём в коде.
	limit := s.eng.GraphNodesLimit()
	if onlyArea == "" && limit > 0 && len(nodes) > limit {
		sort.SliceStable(nodes, func(i, j int) bool {
			if deg[nodes[i].ID] != deg[nodes[j].ID] {
				return deg[nodes[i].ID] > deg[nodes[j].ID]
			}
			return nodes[i].ID > nodes[j].ID
		})
		// узел, на котором стоит человек, остаётся всегда
		kept := nodes[:limit]
		if focus > 0 {
			has := false
			for _, n := range kept {
				if n.ID == focus {
					has = true
					break
				}
			}
			if !has {
				for _, n := range nodes[limit:] {
					if n.ID == focus {
						kept = append(kept, n)
						break
					}
				}
			}
		}
		nodes = kept
	}
	g := &Graph{}
	keep := map[int64]bool{}
	var одиночки []*store.MemoryNode
	for _, n := range nodes {
		if onlyArea != "" && n.Area != onlyArea {
			continue
		}
		if deg[n.ID] == 0 && n.ID != focus {
			одиночки = append(одиночки, n)
			continue
		}
		keep[n.ID] = true
		g.Nodes = append(g.Nodes, GraphNode{
			ID: strconv.FormatInt(n.ID, 10), Label: n.Name, Area: n.Area, Color: s.areaColor(n.Area),
			Size: 4 + deg[n.ID], Dim: cloud && !outbound[n.Area], Selected: n.ID == focus,
			Target: "memory:node:" + strconv.FormatInt(n.ID, 10),
		})
	}
	for _, e := range edges {
		if keep[e.From] && keep[e.To] {
			g.Edges = append(g.Edges, GraphEdge{From: strconv.FormatInt(e.From, 10), To: strconv.FormatInt(e.To, 10), Label: e.Relation})
		}
	}
	return Component{Type: "graph", ID: "memory-graph", Graph: g}, одиночки, read
}

// constellations — на сколько несвязанных кусков распадается картинка и какой
// из них самый большой. «Двести узлов» человеку ничего не говорят, а
// «одно созвездие из 126 и одиннадцать мелких» — говорят, насколько
// память связная.
func constellations(g *Graph) (крупнейшее, total int) {
	if g == nil || len(g.Nodes) == 0 {
		return 0, 0
	}
	корень := map[string]string{}
	var найти func(string) string
	найти = func(x string) string {
		if корень[x] == "" || корень[x] == x {
			корень[x] = x
			return x
		}
		корень[x] = найти(корень[x])
		return корень[x]
	}
	for _, n := range g.Nodes {
		корень[n.ID] = n.ID
	}
	for _, e := range g.Edges {
		a, b := найти(e.From), найти(e.To)
		if a != b {
			корень[a] = b
		}
	}
	размер := map[string]int{}
	for _, n := range g.Nodes {
		размер[найти(n.ID)]++
	}
	for _, к := range размер {
		if к > крупнейшее {
			крупнейшее = к
		}
	}
	return крупнейшее, len(размер)
}

func (s *Server) buildMemoryGraph(doc *Document, focus int64) error {
	st := s.eng.Store()
	doc.Heading = "Память · граф"
	doc.Layout = "fill" // граф во всё окно; строки узла — прокруткой снизу
	doc.ToolbarActions = []Action{{Action: "$goto", Target: "memory", Label: "‹ Память"}}
	eyesOptions := append([]string{"вы"}, s.eng.ProviderNames()...)
	s.mu.Lock()
	eyes := s.graphEyes
	s.mu.Unlock()
	if eyes == "" {
		eyes = "вы"
	}
	doc.HeaderSelects = []Select{{Name: "value", Value: eyes, Options: eyesOptions, Action: Action{Action: "set_graph_eyes"}}}
	g, одиночки, прочитано := s.memoryGraphComponent(focus, "")
	g.Layout = "fill"
	nodes := 0
	if g.Graph != nil {
		nodes = len(g.Graph.Nodes)
	}
	all, _ := st.CountMemoryGraph()
	крупнейшее, кусков := constellations(g.Graph)
	// честно говорим, что показан каркас: иначе человек считает, что в
	// памяти всего двести сущностей. И сразу — насколько она связная
	// рисунок вмещает graphDrawn узлов, отобранных больше: раньше здесь
	// стояло «показаны 200», а на картинке было 44
	части := []string{plural(all, "узел", "узла", "узлов") + " в памяти"}
	if nodes > graphDrawn {
		списком := fmt.Sprintf("все %d отобранных", nodes)
		if nodes > graphListed {
			списком = fmt.Sprintf("первые %d из %d отобранных", graphListed, nodes)
		}
		части = append(части, fmt.Sprintf("на рисунке %d самых связанных, %s — «Списком» под ним", graphDrawn, списком))
	} else if nodes+len(одиночки) < all {
		части = append(части, fmt.Sprintf("показаны %d самых связанных", nodes+len(одиночки)))
	}
	if крупнейшее > 0 {
		кусок := fmt.Sprintf("самое большое созвездие среди отобранных — %s", plural(крупнейшее, "узел", "узла", "узлов"))
		if кусков > 1 {
			кусок += fmt.Sprintf(", кроме него ещё %d", кусков-1)
		}
		части = append(части, кусок)
	}
	if len(одиночки) > 0 {
		части = append(части, plural(len(одиночки), "узел", "узла", "узлов")+" без единой связи — строкой под графом")
	}
	doc.Subheading = strings.Join(части, " · ") +
		". Крупнее те, у кого больше связей; «глазами» показывает, что уйдёт выбранной модели: бледное для облака закрыто"
	if nodes == 0 && len(одиночки) == 0 {
		doc.Components = append(doc.Components, emptyState("Граф пока пуст",
			"Сущности и связи Сёма достаёт из записей памяти ролью «Память» — раз в сутки по расписанию. "+
				"Можно и вручную: скажите ему в чате «граф: Сёма | живёт на | Raspberry Pi».", "point.3.connected.trianglepath.dotted"))
		return nil
	}
	if nodes > 0 {
		doc.Components = append(doc.Components, g)
	}
	if len(одиночки) > 0 {
		var капсулы []Badge
		for _, n := range одиночки {
			капсулы = append(капсулы, Badge{Text: n.Name})
		}
		doc.Components = append(doc.Components, Component{Type: "chipset", ID: "graph-одиночки",
			Title: "Отдельно от всех · " + plural(len(одиночки), "узел", "узла", "узлов") + " без единой связи",
			Caption: "Узел без связей — не мусор: это слово, которое встретилось в записи один раз. " +
				"Связи у него появятся, когда о нём зайдёт речь ещё раз, — или не появятся никогда.",
			Badges: капсулы})
	}
	if focus > 0 {
		if карточка, ok := s.nodeCard(focus, прочитано); ok {
			doc.Components = append(doc.Components, карточка...)
		}
	}
	return nil
}

// nodeCard — узел графа целиком: чем он оказался, с кем связан и, что
// важнее всего, откуда эти связи взялись. Связь без записи-источника —
// утверждение без автора: раньше запись приписывалась хвостом к каждой
// строке связи и повторялась в ней по десять раз.
//
// Узлы и связи — из уже прочитанного графом (read), записи-источники — одним
// запросом: прежде карточка читала все связи заново и шла в базу за каждым
// соседом и каждой записью — 243 запроса и 125 мс на Pi (5 октября 2026).
func (s *Server) nodeCard(focus int64, read memoryGraph) ([]Component, bool) {
	st := s.eng.Store()
	n := read.byID[focus]
	if n == nil {
		return nil, false
	}
	edges := read.edges
	var источники []int64
	{
		seen := map[int64]bool{}
		for _, e := range edges {
			if (e.From == focus || e.To == focus) && !seen[e.MemoryID] {
				seen[e.MemoryID] = true
				источники = append(источники, e.MemoryID)
			}
		}
	}
	записи, _ := st.MemoriesByIDs(источники)
	type кучка struct {
		запись *store.Memory
		rowsOf []Component
	}
	группы := map[int64]*кучка{}
	var порядок []int64
	связей := 0
	var соседи []Badge
	for _, e := range edges {
		if e.From != focus && e.To != focus {
			continue
		}
		otherID := e.To
		if e.To == focus {
			otherID = e.From
		}
		other := read.byID[otherID]
		if other == nil {
			continue
		}
		связей++
		соседи = append(соседи, Badge{Text: other.Name})
		label := n.Name + " —" + e.Relation + "→ " + other.Name
		if e.To == focus {
			label = other.Name + " —" + e.Relation + "→ " + n.Name
		}
		строка := Component{Type: "row", Layout: "wide", Subtitle: label, Meta: other.Area,
			Actions: []Action{{Action: "$goto", Target: "memory:node:" + strconv.FormatInt(otherID, 10), Label: "К узлу"}}}
		g, ok := группы[e.MemoryID]
		if !ok {
			g = &кучка{запись: записи[e.MemoryID]}
			группы[e.MemoryID] = g
			порядок = append(порядок, e.MemoryID)
		}
		g.rowsOf = append(g.rowsOf, строка)
	}

	areas, _ := st.MemoryAreas()
	var areaNames []string
	for _, a := range areas {
		areaNames = append(areaNames, a.Name)
	}
	id := strconv.FormatInt(focus, 10)
	head := Component{Type: "row", Layout: "wide", Subtitle: n.Name,
		Caption: n.Type,
		Meta:    plural(связей, "связь", "связи", "связей") + " · " + plural(len(группы), "запись", "записи", "записей"),
		Badges:  []Badge{{Text: n.Area, Color: s.areaColor(n.Area)}},
		Selects: []Select{{Name: "value", Value: n.Area, Options: areaNames, Action: Action{Action: "move_node", Target: id}}},
		Actions: []Action{
			{Action: "merge_node", Target: id, Label: "Объединить с…", Prompt: "Имя узла, в который влить этот"},
			{Action: "forget_node", Target: id, Label: "Забыть узел", Style: "destructive",
				Confirm: "Забыть узел «" + n.Name + "» и его связи?", ConfirmOK: "Забыть"},
		}}
	out := []Component{{Type: "section", Title: "Узел · " + n.Name, Children: []Component{head}}}
	if len(соседи) > 0 {
		out = append(out, Component{Type: "chipset", ID: "узел-соседи-" + id,
			Title: "С кем связан", Badges: соседи})
	}

	// связи, сгруппированные по записям, из которых выросли: у каждой
	// видно, кто и когда это утверждал
	var кучки []Component
	for _, mid := range порядок {
		g := группы[mid]
		name := "Запись не сохранилась"
		мета := "связь осталась, а записи, из которой её достали, уже нет"
		var действия []Action
		if g.запись != nil {
			name = clipRunes(g.запись.Text, 120)
			мета = g.запись.Agent + " · " + whenText(g.запись.Created)
			if g.запись.Model != "" {
				мета += " · извлёк " + g.запись.Model
			}
			// у записи есть свой экран: там видно и происхождение, и
			// историю версий, и место в коде — ведём туда, а не в сессию
			действия = []Action{{Action: "$goto",
				Target: "memory:rec:" + strconv.FormatInt(g.запись.ID, 10), Label: "Открыть запись"}}
		}
		if len(g.rowsOf) == 1 {
			строка := g.rowsOf[0]
			строка.Caption = name
			строка.Meta = мета
			if len(действия) > 0 {
				строка.Menu = действия
			}
			кучки = append(кучки, строка)
			continue
		}
		кучки = append(кучки, Component{Type: "rowgroup", ID: fmt.Sprintf("узел-%s-зап-%d", id, mid),
			Subtitle: name, Caption: мета, Children: g.rowsOf})
	}
	if len(кучки) == 0 {
		кучки = append(кучки, emptyState("Связей нет",
			"Узел встретился Сёме один раз и больше ни с чем не сошёлся. "+
				"Связь появится, когда о нём зайдёт речь ещё раз в другой записи.", "point.3.connected.trianglepath.dotted"))
	}
	out = append(out, Component{Type: "section", Title: "Откуда эти связи", Children: кучки})
	return out, true
}

// memoryRules — guardrails: маски, лимиты, пороги, роль.
func (s *Server) memoryRules(add func(Component)) {
	st := s.eng.Store()
	masks, _ := st.MemoryRules("mask")
	var rows []Component
	rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Маски секретов на входе",
		Caption: "совпало — запись не сохраняется, попытка в журнале · " + strings.Join(masks, " · "),
		Actions: func() []Action {
			var out []Action
			for _, m := range masks {
				out = append(out, Action{Action: "delete_mask", Target: m, Label: "убрать " + m})
			}
			return out
		}()})
	limitOpts := []string{"0", "1", "2", "3", "5", "8", "10", "15", "20"}
	rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Лимит на промпт",
		Caption: "уроков · фактов · эпизодов — сколько записей уходит модели за один ответ",
		Selects: []Select{
			{Name: "value", Value: strconv.Itoa(s.eng.MemorySetting("memory.limit_lessons")), Options: limitOpts, Action: Action{Action: "set_memory_setting", Target: "memory.limit_lessons"}},
			{Name: "value", Value: strconv.Itoa(s.eng.MemorySetting("memory.limit_facts")), Options: limitOpts, Action: Action{Action: "set_memory_setting", Target: "memory.limit_facts"}},
			{Name: "value", Value: strconv.Itoa(s.eng.MemorySetting("memory.limit_episodes")), Options: limitOpts, Action: Action{Action: "set_memory_setting", Target: "memory.limit_episodes"}},
		}})
	rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Когда факт становится уроком",
		Caption: "повторился столько раз или подтверждён вами",
		Selects: []Select{{Name: "value", Value: strconv.Itoa(s.eng.MemorySetting("memory.lesson_threshold")), Options: []string{"2", "3", "5"},
			Action: Action{Action: "set_memory_setting", Target: "memory.lesson_threshold"}}}})
	rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Наблюдения живут · глубина графа",
		Caption: "сырые события сессий удаляются по сроку · на сколько шагов обходить граф от названной сущности",
		Selects: []Select{
			{Name: "value", Value: strconv.Itoa(s.eng.MemorySetting("memory.observe_ttl_days")), Options: []string{"1", "3", "7", "14", "30"}, Action: Action{Action: "set_memory_setting", Target: "memory.observe_ttl_days"}},
			{Name: "value", Value: strconv.Itoa(s.eng.MemorySetting("memory.graph_depth")), Options: []string{"1", "2", "3"}, Action: Action{Action: "set_memory_setting", Target: "memory.graph_depth"}},
		}})
	role := s.eng.Role(engine.RoleMemory)
	roleCaption := "извлекает факты и граф, сводит дубли, сжимает эпизоды · модель: " + role.Model
	if !s.eng.MemoryModelSet() {
		roleCaption = "модель не назначена — память только пишет и ищет, без сжатия и графа"
	}
	if last := s.eng.MemoryLastPass(); last != "" {
		roleCaption += " · последнее сведение " + whenText(last)
	}
	rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Роль «Память»", Caption: roleCaption,
		Actions: []Action{{Action: "$goto", Target: "settings", Label: "В моделях"}, {Action: "run_memory_pass", Label: "Свести сейчас"}}})
	add(Component{Type: "section", Title: "Правила · guardrails", Children: rows})
	add(Component{Type: "form", ID: "newmask", Title: "Добавить маску секрета",
		Fields: []Field{{Type: "text", Name: "value", Label: "Подстрока", Hint: "например AKIA или api_key="}},
		Submit: &Action{Action: "add_mask", Label: "Добавить"}})
}

// memoryActions — действия раздела «Память».
func (s *Server) memoryActions(req actionRequest) (string, error) {
	st := s.eng.Store()
	ctx := context.Background()
	switch req.Action {
	case "toggle_area_outbound", "toggle_area_forget":
		a, ok := st.MemoryArea(req.Target)
		if !ok {
			return "", fmt.Errorf("области нет")
		}
		if req.Action == "toggle_area_outbound" {
			a.Outbound = !a.Outbound
		} else {
			a.Forget = !a.Forget
		}
		return "", st.SaveMemoryArea(a)
	case "set_area_confirm", "set_area_ttl":
		a, ok := st.MemoryArea(req.Target)
		if !ok {
			return "", fmt.Errorf("области нет")
		}
		if req.Action == "set_area_confirm" {
			a.Confirm = confirmMode(req.Params["value"])
		} else {
			var d int
			_, _ = fmt.Sscanf(req.Params["value"], "%d", &d)
			a.TTLDays = d
		}
		return "", st.SaveMemoryArea(a)
	case "delete_area":
		return "", st.DeleteMemoryArea(req.Target)
	case "create_area":
		return "", s.eng.CreateArea(req.Target, "")
	case "create_area_form":
		return "", s.eng.CreateArea(req.Params["name"], strings.TrimSpace(req.Params["title"]))
	case "drop_area_suggestion":
		s.eng.DropAreaSuggestion(req.Target)
		return "", nil
	case "grant_consumer_area":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		area := req.Params["value"]
		if area == "" || area == "выдать…" {
			return "", nil
		}
		return "", s.eng.GrantArea(id, area, true)
	case "grant_area_cell":
		idText, area := splitTarget(req.Target)
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			return "", err
		}
		return "", s.eng.GrantArea(id, area, true)
	case "revoke_consumer_area":
		idText, area := splitTarget(req.Target)
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			return "", err
		}
		return "", s.eng.GrantArea(id, area, false)
	case "delete_consumer":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", st.DeleteMemoryConsumer(id)
	case "accept_all_pending":
		_, err := s.eng.DecideAllPending(ctx, true)
		return "", err
	case "forget_all_pending":
		_, err := s.eng.DecideAllPending(ctx, false)
		return "", err
	case "accept_memory":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.ConfirmMemory(ctx, id, "")
	case "accept_memory_area":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.AcceptMemoryCard(ctx, id, strings.TrimSpace(req.Params["value"]))
	case "fix_memory":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.ConfirmMemory(ctx, id, req.Params["value"])
	case "promote_memory":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.PromoteMemory(id)
	case "toggle_memory_review":
		return "", s.eng.SetMemoryReview(!s.eng.MemoryReviewOn())
	case "run_memory_review":
		n := s.eng.EnqueueAllMemoryReview()
		if n == 0 {
			return "", nil
		}
		return queueScreen, nil
	case "unbind_place", "block_principle", "unblock_principle":
		pl, err := s.placeFromTarget(req.Target)
		if err != nil {
			return "", err
		}
		m, err := st.MemoryByID(pl.MemoryID)
		if err != nil {
			return "", err
		}
		if req.Action != "unbind_place" {
			if !strings.HasPrefix(pl.Source, engine.PrincipleSource) {
				return "", fmt.Errorf("это не принцип из CLAUDE.md")
			}
			heading := strings.TrimPrefix(pl.Source, engine.PrincipleSource)
			if err := s.eng.BlockPrinciple(heading, engine.PrincipleHash(m), req.Action == "block_principle"); err != nil {
				return "", err
			}
			if req.Action == "unblock_principle" {
				return "", nil
			}
		}
		return "", st.UnbindPlace(pl)
	case "forget_memory":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.ForgetMemory(id)
	case "set_graph_eyes":
		s.mu.Lock()
		s.graphEyes = req.Params["value"]
		s.mu.Unlock()
		return "", nil
	case "move_node":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", st.MoveMemoryNode(id, req.Params["value"])
	case "merge_node":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		target, err := mergeTarget(st, id, strings.TrimSpace(req.Params["value"]))
		if err != nil {
			return "", err
		}
		return "memory:node:" + strconv.FormatInt(target.ID, 10), st.MergeMemoryNodes(id, target.ID)
	case "forget_node":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "memory:graph", st.DeleteMemoryNode(id)
	case "delete_mask":
		return "", st.DeleteMemoryRule("mask", req.Target)
	case "add_mask":
		v := strings.TrimSpace(req.Params["value"])
		if v == "" {
			return "", fmt.Errorf("пустая маска")
		}
		return "", st.AddMemoryRule("mask", v, "добавлено вручную")
	case "set_memory_setting":
		var v int
		if _, err := fmt.Sscanf(req.Params["value"], "%d", &v); err != nil {
			return "", err
		}
		if v == 0 {
			v = -1 // ноль — тоже значение: «ничего не класть»
		}
		return "", s.eng.SetMemorySetting(req.Target, v)
	case "run_memory_pass":
		go s.eng.RunMemoryPassNow()
		return "", nil
	case "crystallize_session":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.CrystallizeSession(ctx, id)
	case "delete_session":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "memory:sessions", st.DeleteMemorySession(id)
	default:
		return "", errUnhandled
	}
}

// ---- Память и внешние агенты ----
//
// Ручек памяти по сети здесь больше нет. Хуки и инструменты агентов
// разработки ходят к жителю машины, а он спрашивает Сёму своим проводом
// (`engine/agent_ask.go`): по сети к памяти не ведёт ни одного пути.

// pendingRecordsFragment — записи памяти, ждущие решения человека: первые
// двадцать, остальные по «ещё» или поиском по тексту.
func (s *Server) pendingRecordsFragment(_ string, search string, предел int) ([]Component, error) {
	pending, err := s.eng.Store().PendingMemories()
	if err != nil {
		return nil, err
	}
	var подходят []*store.Memory
	for _, m := range pending {
		if search == "" || looksLike(search, m.Text, m.Area) {
			подходят = append(подходят, m)
		}
	}
	показать := подходят
	if len(показать) > предел {
		показать = показать[:предел]
	}
	var rows []Component
	for _, m := range показать {
		rows = append(rows, s.memoryRow(m))
	}
	if len(rows) == 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Ничего не нашлось",
			Caption: "по «" + search + "» среди ждущих записей"})
	}
	if len(подходят) > len(показать) {
		rows = append(rows, moreInFragment(len(показать), len(подходят)))
	}
	return rows, nil
}

// mergeTarget — в какой узел вливать, по имени, которое ввёл человек: точное
// имя — этот узел; без учёта регистра — узел той же области, что и
// вливаемый, а если таких несколько или все в других областях и их больше
// одного — Сёма называет их и просит ввести имя точно. Молча выбрать узел
// чужой области значило бы перенести туда связи, которые там видны не тем.
func mergeTarget(st *store.Store, source int64, name string) (*store.MemoryNode, error) {
	if n, ok := st.MemoryNodeByName("сущность", name); ok {
		return n, nil
	}
	list, err := st.MemoryNodesByKey("сущность", name)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("узла %q нет", name)
	}
	if len(list) == 1 {
		return list[0], nil
	}
	if src, err := st.MemoryNode(source); err == nil {
		var same []*store.MemoryNode
		for _, n := range list {
			if n.Area == src.Area && n.ID != source {
				same = append(same, n)
			}
		}
		if len(same) == 1 {
			return same[0], nil
		}
	}
	var names []string
	for _, n := range list {
		names = append(names, "«"+n.Name+"» ("+n.Area+")")
	}
	return nil, fmt.Errorf("узлов с таким именем несколько: %s — введите имя точно", strings.Join(names, ", "))
}

// placeTarget — привязка одной строкой для действия: запись и её ключ.
// Разделитель — вертикальная черта: в путях и именах символов её нет.
func placeTarget(p store.Place) string {
	return strings.Join([]string{strconv.FormatInt(p.MemoryID, 10), p.Repo, p.Dir, p.File, p.Symbol}, "|")
}

// placeFromTarget — обратно; источник подтягивается из базы, в цели его
// нет: он нужен только для принципов, и подделать его из клиента нельзя.
func (s *Server) placeFromTarget(target string) (store.Place, error) {
	parts := strings.Split(target, "|")
	if len(parts) != 5 {
		return store.Place{}, fmt.Errorf("непонятная привязка")
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id == 0 {
		return store.Place{}, fmt.Errorf("непонятная привязка")
	}
	want := store.Place{MemoryID: id, Repo: parts[1], Dir: parts[2], File: parts[3], Symbol: parts[4]}
	// источник — из базы: кнопка знает только координату
	for _, p := range s.eng.Store().MemoryPlaces(id) {
		if p.Repo == want.Repo && p.Dir == want.Dir && p.File == want.File && p.Symbol == want.Symbol {
			return p, nil
		}
	}
	return want, nil
}
