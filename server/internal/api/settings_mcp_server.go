package api

// Экран одного сервера MCP: `settings:integrations@<имя>~вкладка`. Шапку (путь
// назад, вкладки, закреплённая строка) рисует общий entityFrame, содержимое —
// здесь:
//
//	Обзор   — что за сервер и чем входит: адрес, состояние, вход (кнопки «Войти»
//	          и «Открыть страницу входа»), «В чате», подсказки про формат
//	          аргументов, «Опасное»
//	Правила — у каждого инструмента свой ответ (integrations_rules.go)
//	Журнал  — вызовы этого сервера: чипы отбора, двадцать строк, «Показать ещё»
//
// Что правится — поля строками настройки (`Form.Style: "rows"`): значение видно,
// формат не нужно помнить, а ошибка приходит не после «ОК». Сохраняет их одна
// нижняя полоса. «Права входа» были вопросом в меню строки (`Prompt`), теперь —
// поле. Опасное — «Выйти» и «Удалить сервер» — внизу «Обзора» своей карточкой,
// у каждой кнопки `Confirm`.
//
// Решений экран не принимает: вход, продление токенов, правила и круги — как
// были, он только показывает и зовёт прежние действия.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// mcpLogFilters — чипы журнала по порядку: слово отбора и подпись.
var mcpLogFilters = []struct{ ID, Title string }{
	{store.MCPLogAll, "Все"},
	{store.MCPLogDone, "Выполнены"},
	{store.MCPLogAsked, "Ждали кнопку"},
	{store.MCPLogDenied, "Отказы"},
	{store.MCPLogError, "Ошибки"},
}

// mcpTabs — вкладки сервера. Счётчик у вкладки говорит, что там есть решать,
// до того как человек туда зайдёт.
func (s *Server) mcpTabs(m *store.MCPServer, states []engine.MCPToolState) []innerTab {
	tabs := []innerTab{
		{ID: mcpTabOverview, Name: "Обзор"},
		{ID: mcpTabRules, Name: "Правила"},
		{ID: mcpTabLog, Name: "Журнал"},
	}
	waiting := 0
	pend, _ := s.eng.Store().MCPPendingList()
	for _, p := range pend {
		if p.Server == m.Name {
			waiting++
		}
	}
	if waiting > 0 {
		tabs[0].Count, tabs[0].Level = strconv.Itoa(waiting)+" ждут", LevelAlert
	}
	if len(states) > 0 {
		tabs[1].Count = strconv.Itoa(len(states))
		// новые инструменты запрещены, пока человек не решит: это видно на
		// вкладке до захода в неё
		if _, _, _, fresh := engine.MCPRuleCounts(states); fresh > 0 {
			tabs[1].Count, tabs[1].Level = strconv.Itoa(fresh)+" новых", LevelWarn
		}
	}
	return tabs
}

// mcpLoginWords — как сервер входит, словами для шапки и строки.
func mcpLoginWords(m *store.MCPServer, view engine.MCPLoginView) string {
	switch {
	case m.Agent != "":
		return "по каналу агентов"
	case m.Secret != "":
		return "ключ «" + m.Secret + "»"
	case view.Pending:
		return "вход начат"
	case view.Has:
		return "вход OAuth выполнен"
	case m.State == "login":
		return ""
	}
	return "без ключа"
}

// mcpCaption — одна строка фактов закреплённой шапки: адрес, как входит,
// сколько инструментов и когда проверен.
func mcpCaption(m *store.MCPServer, view engine.MCPLoginView) string {
	var parts []string
	if m.Agent != "" {
		can := m.Can
		if can == "" {
			can = "код"
		}
		parts = append(parts, "у агента «"+m.Agent+"», способность «"+can+"»")
	} else {
		parts = append(parts, m.URL)
	}
	if w := mcpLoginWords(m, view); w != "" && m.Agent == "" {
		parts = append(parts, w)
	}
	if n := len(m.Tools); n > 0 {
		parts = append(parts, plural(n, "инструмент", "инструмента", "инструментов"))
	}
	if m.Checked != "" {
		parts = append(parts, "проверен "+whenText(m.Checked))
	}
	return strings.Join(parts, " · ")
}

func (s *Server) buildMCPServer(doc *Document, a mcpAddr, filter, search string, limit int) error {
	st := s.eng.Store()
	doc.Layout = "read"
	m, ok := st.MCPServer(a.Server)
	if !ok {
		// сервер могли удалить, пока экран был открыт: не ошибка, а объяснение
		doc.Heading = "Интеграции"
		doc.Components = append(doc.Components,
			Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: mcpScreen, Label: "‹ Интеграции"}}},
			emptyState("Сервера «"+a.Server+"» нет", "Его удалили или переименовали. Список серверов — по кнопке «‹ Интеграции» выше", "tray"))
		return nil
	}
	view := s.eng.MCPLoginView(m)
	states := engine.MCPToolStates(m)
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	switch a.Tab {
	case mcpTabRules:
		s.mcpRulesSection(doc, add, m, filter, search)
	case mcpTabLog:
		if err := s.mcpJournal(doc, m.Name, filter, limit); err != nil {
			return err
		}
	default:
		a.Tab = mcpTabOverview
		s.mcpOverview(add, m, view)
	}

	letter, color := mcpTile(m)
	e := entity{Section: mcpScreen, Back: "Интеграции", ID: m.Name, Name: m.Name,
		Avatar: letter, AvatarColor: color, Caption: mcpCaption(m, view),
		Tab: a.Tab, Tabs: s.mcpTabs(m, states), Switch: "Сервер"}
	switch {
	case !m.Enabled:
		e.Badges = []Badge{mcpStatePill(m)}
	case mcpTrouble(m) != "":
		e.Level = mcpTrouble(m)
		e.Badges = []Badge{mcpStatePill(m)}
	}
	primary := mcpPrimary(m, view)
	e.Primary = &primary
	// «···»: что не главное — проверка, когда главная кнопка другая, остальные
	// шаги входа, короткие пути по вкладкам и выключатель сервера
	e.Menu = mcpMenu(m, view, primary)
	servers, _ := st.MCPServers()
	for _, other := range servers {
		e.Others = append(e.Others, other.Name)
	}
	sort.Strings(e.Others)
	entityFrame(doc, e)
	return nil
}

// mcpOverview — «Обзор»: что за сервер. Свойства — строки, правки — поля,
// вход — кнопки, опасное — внизу.
func (s *Server) mcpOverview(add func(Component), m *store.MCPServer, view engine.MCPLoginView) {
	st := s.eng.Store()
	lim := s.eng.MCPChatLimits()

	// то, что ждёт именно этого сервера, — вверху: вызовы на кнопке и новые
	// инструменты
	if shelf := s.mcpShelf(m.Name); shelf != nil {
		add(*shelf)
	}

	// ---- подключение ----
	var rows []Component
	if m.Agent != "" {
		can := m.Can
		if can == "" {
			can = "код"
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "mcp-addr", Subtitle: "Где живёт",
			Caption: "У агента «" + m.Agent + "», способность «" + can + "». Адреса у сервера нет: вызовы идут только по шифрованному каналу агента, " +
				"HTTP запасным путём не служит — иначе шифрование и разрешения обходились бы стороной",
			Actions: []Action{{Action: "$goto", Target: agentsScreen, Label: "Агенты"}}})
	} else {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "mcp-addr", Subtitle: "Адрес", Caption: m.URL})
	}

	stateRow := Component{Type: "row", Layout: "wide", ID: "mcp-state", Subtitle: "Состояние", Badges: []Badge{mcpStatePill(m)}}
	var stateParts []string
	if m.Checked != "" {
		stateParts = append(stateParts, "проверен "+whenText(m.Checked))
	} else {
		stateParts = append(stateParts, "ещё не проверен: нажмите «Проверить»")
	}
	if n := len(m.Tools); n > 0 {
		stateParts = append(stateParts, plural(n, "инструмент", "инструмента", "инструментов"))
	}
	stateRow.Caption = strings.Join(stateParts, " · ")
	// что сказала последняя проверка: причина отказа или итог входа. Такая
	// строка — единственное место, где причина видна целиком
	if m.Note != "" {
		stateRow.Caption += "\n" + clipRunes(m.Note, 320)
	}
	rows = append(rows, stateRow)

	auth := Component{Type: "row", Layout: "wide", ID: "mcp-auth", Subtitle: "Чем входит"}
	switch {
	case m.Agent != "":
		auth.Caption = "Сертификатом агента по каналу агентов: ни ключа, ни входа у сервера нет"
	case m.Secret != "":
		header, prefix := m.Header, m.Prefix
		if header == "" {
			header = "Authorization"
		}
		if prefix == "" {
			prefix = "Bearer "
		}
		auth.Caption = "Постоянный ключ «" + m.Secret + "» из хранилища · заголовок «" + header + "» · приставка «" + prefix + "». " +
			"Значение ключа Сёма подставляет сам: ни здесь, ни в тексте модели его нет"
		if _, has := st.Secret(m.Secret); has {
			auth.Actions = []Action{{Action: "$goto", Target: entityAddr(secretsScreen, m.Secret, secretsTabLog), Label: "Журнал ключа"}}
		}
	default:
		auth.Caption = "Вход OAuth, если сервер его требует, иначе без ключа. Токены входа лежат в хранилище секретов, читает их только ядро"
	}
	rows = append(rows, auth)

	if mcpCanLogin(m) {
		fact := loginFact(m, view)
		login := Component{Type: "row", Layout: "wide", ID: "mcp-login", Subtitle: "Вход", Caption: fact.Value}
		// «Войти» готовит вход, «Открыть страницу входа» уводит на страницу
		// сервера входа: две кнопки по очереди, как и раньше
		primary := mcpPrimary(m, view)
		if primary.Action != "check_mcp" {
			login.Actions = []Action{primary}
		}
		for _, a := range loginActions(m, view) {
			if a.Action != primary.Action {
				login.Menu = append(login.Menu, a)
			}
		}
		rows = append(rows, login)
	}

	enabled := "выключен: чат и модули его не вызывают"
	if m.Enabled {
		enabled = "выключенный сервер не вызывается ни чатом, ни модулями; правила и вход остаются"
	}
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "mcp-enabled", Subtitle: "Включён", Caption: enabled,
		Toggle:  boolToggle(m.Enabled),
		Actions: []Action{{Action: "toggle_mcp", Target: m.Name, Label: "toggle"}}})

	calls := Component{Type: "row", Layout: "wide", ID: "mcp-calls", Subtitle: "Вызовов за сутки",
		Caption: strconv.Itoa(st.CountMCPCalls(m.Name, mcpDayAgo())),
		Actions: []Action{{Action: "$goto", Target: entityAddr(mcpScreen, m.Name, mcpTabLog), Label: "Журнал"}}}
	rows = append(rows, calls)
	// адрес и ключ на месте не правятся: человеку нужно знать, как их сменить
	if m.Agent == "" {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "mcp-change", Subtitle: "Сменить адрес или ключ",
			Caption: "Адрес, ключ, заголовок и приставка на месте не правятся: добавьте сервер заново под тем же именем — " +
				"правила, подсказки и вход останутся, а «В чате» и права входа придётся вписать снова",
			Actions: []Action{{Action: "$goto", Target: entityAddr(mcpScreen, mcpNew, ""), Label: "Добавить сервер MCP"}}})
	}
	add(Component{Type: "section", ID: "mcp-conn", Title: "Подключение", Children: rows})

	// права входа — поле, а не вопрос в меню строки
	if mcpCanLogin(m) {
		add(Component{Type: "form", ID: "mcp-login-scope", Style: formRows,
			Fields: []Field{{Type: "text", Name: "login_scope", Label: "Права входа",
				Hint:  "Через пробел; пусто — все, что назовёт сервер. Действует при следующем входе: выданные сейчас права не меняются",
				Value: m.LoginScope}},
			Submit: &Action{Action: "save_mcp_login_scope", Target: m.Name, Label: "Сохранить"}})
	}

	// ---- в чате ----
	if m.Agent == "" {
		class := m.Class
		if class == "" {
			class = store.ClassPlain
		}
		add(Component{Type: "form", ID: "mcp-chat-" + m.Name, Title: "В чате", Style: formRows,
			Fields: []Field{
				{Type: "toggle", Name: "chat", Label: "Открыт чату",
					Hint:  "Модель видит инструменты сервера и зовёт их сама, как Claude Code и Codex; меняющие вызовы всё равно ждут вашей кнопки. Закрыт — про сервер она знает только по имени",
					Value: boolValue(m.Chat)},
				{Type: "text", Name: "about", Label: "Что это",
					Hint: "Одна строка для описи в промпте: о чём этот сервер", Value: m.About},
				{Type: "text", Name: "keywords", Label: "Когда звать",
					Hint:  "Слова через запятую: встретились в вопросе — модель получает полный список инструментов. Имя сервера работает всегда",
					Value: m.Keywords},
				{Type: "segments", Name: "class", Label: "Класс данных",
					Hint:    "Что содержат ответы сервера. Облачной модели ответы выше «обычного» не отдаются: секретное — никогда, чувствительное — по режиму «Что ушло наружу»",
					Options: []string{store.ClassPlain, store.ClassSensitive, store.ClassSecret}, Tones: []string{"", LevelWarn, LevelAlert},
					Value: class},
			},
			Submit: &Action{Action: "save_mcp_chat", Target: m.Name, Label: "Сохранить"}})
	} else {
		add(Component{Type: "section", ID: "mcp-chat-agent", Title: "В чате", Children: []Component{{
			Type: "row", Layout: "wide", ID: "mcp-chat-no", Subtitle: "Чату не открывается",
			Caption: "У сервера жителя машины инструменты кода: их зовут агенты разработки, а не чат Сёмы"}}})
	}

	// ---- подсказки инструментов ----
	// что сервер сам сказал про формат аргументов: видно, чему Сёма научился,
	// и есть чем это стереть
	if fresh, all := mcpHintCounts(m, lim.HintDays); all > 0 {
		value := fmt.Sprintf("%d — идут в промпт чата, пока не истёк срок (%d дн.)", fresh, lim.HintDays)
		switch {
		case lim.HintDays <= 0:
			value = fmt.Sprintf("%d — подсказки выключены (срок 0 дней), в промпт не идут", all)
		case fresh == 0:
			value = fmt.Sprintf("%d, все за сроком (%d дн.): в промпт уже не идут", all, lim.HintDays)
		}
		hints := []Component{{Type: "row", Layout: "wide", ID: "mcp-hints", Subtitle: "Подсказки про формат аргументов", Caption: value,
			Actions: []Action{{Action: "forget_mcp_hints", Target: m.Name, Label: "Забыть все",
				Confirm:   "Забыть подсказки сервера «" + m.Name + "»? Сёма перестанет напоминать модели про формат аргументов, пока сервер снова не отклонит вызов.",
				ConfirmOK: "Забыть"}}}}
		hints = append(hints, mcpHintRows(m, lim.HintDays)...)
		add(Component{Type: "section", ID: "mcp-hints-section", Title: "Подсказки инструментов", Children: hints})
	}

	// ---- опасное: внизу своей карточкой, с подтверждением ----
	var danger []Component
	if view.Has {
		danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-logout",
			Subtitle: "Выйти из сервера",
			Caption:  "Токены входа удалятся, сервер отзовёт их у себя; войти снова можно кнопкой «Войти»",
			Actions: []Action{{Action: "mcp_logout", Target: m.Name, Label: "Выйти…", Style: "destructive",
				Confirm:   "Выйти из сервера «" + m.Name + "»? Токены входа удалятся, сервер отзовёт их у себя; войти снова можно кнопкой «Войти».",
				ConfirmOK: "Выйти"}}})
	}
	danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-delete",
		Subtitle: "Удалить сервер",
		Caption:  "Правила, подсказки и вход пропадут (токены сервер отзовёт). Постоянный ключ в хранилище и журнал вызовов останутся",
		Actions: []Action{{Action: "delete_mcp", Target: m.Name, Label: "Удалить…", Style: "destructive",
			Confirm:   "Удалить сервер MCP «" + m.Name + "»? Правила по инструментам, подсказки и вход пропадут; чат и модули перестанут его вызывать.",
			ConfirmOK: "Удалить сервер"}}})
	add(Component{Type: "section", ID: "mcp-danger", Title: "Опасное", Style: "danger", Children: danger})
}

// mcpJournal — журнал вызовов сервера: чипы отбора с числами, первая страница
// и «Показать ещё». Добавляется к документу как есть. Хроника, а не
// настройка: на первом экране сервера её нет, она приезжает с вкладкой.
func (s *Server) mcpJournal(doc *Document, server, filter string, limit int) error {
	st := s.eng.Store()
	// у журнала есть полоса чипов, но нет своего поиска по строкам: верхнее поле
	// «ищи по настройкам» на телефоне ему не нужно
	doc.noPhoneSearch = true
	known := false
	for _, f := range mcpLogFilters {
		doc.Filters = append(doc.Filters, Filter{ID: f.ID,
			Title: fmt.Sprintf("%s · %d", f.Title, st.CountMCPLog(server, f.ID))})
		if f.ID == filter {
			known = true
		}
	}
	if !known {
		filter = store.MCPLogAll
	}
	doc.ActiveFilter = filter

	if limit <= 0 || limit == defaultRows {
		limit = mcpLogPage
	}
	entries, err := st.MCPLogFiltered(server, filter, limit+1)
	if err != nil {
		return err
	}
	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	rows := make([]Component, 0, len(entries))
	for _, u := range entries {
		rows = append(rows, mcpLogRow(u, false))
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Вызовов ещё не было", mcpLogEmptyWhy, "list.bullet"))
		if filter != store.MCPLogAll {
			rows[0] = emptyState("Под этот отбор ничего не подошло", "Выберите «Все» — там вся хроника этого журнала", "list.bullet")
		}
	}
	doc.Components = append(doc.Components, Component{Type: "section", Title: "Вызовы «" + server + "»", Children: rows})
	if more {
		total := st.CountMCPLog(server, filter)
		doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(rows), total)}}})
	}
	return nil
}
