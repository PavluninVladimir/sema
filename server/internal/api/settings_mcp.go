package api

// Раздел «Интеграции»: список серверов MCP тонкими строками, полка «Ждут вас»,
// «Тонкая настройка» и общий журнал вызовов — последним, ленивым. У каждого
// сервера свой экран (settings_mcp_server.go): вход, «В чате», подсказки и
// правила пятидесяти инструментов жили в раскрытии строки, и строка сервера
// несла три факта, пять капсул, тумблер и семь действий. Теперь строка —
// имя, не больше двух капсул, одна фраза, одна кнопка и «···», а вся строка
// ведёт на экран сервера.
//
// Кнопка «Добавить сервер MCP» одна — в шапке раздела: форма добавления из
// десяти полей, развёрнутая под списком, больше списка над ней.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// mcpLogPart — часть экрана с общим журналом вызовов (реестр parts.go).
const mcpLogPart = "mcp-log"

// mcpLogPage — сколько строк журнала приезжает первой страницей.
const mcpLogPage = 20

// mcpDayAgo — граница «за сутки» для счётчика вызовов, RFC3339 как в базе.
func mcpDayAgo() string { return time.Now().Add(-24 * time.Hour).Format(time.RFC3339) }

// mcpTile — буква и цвет плитки: сервер жителя машины отличается цветом от
// серверов по адресу.
func mcpTile(m *store.MCPServer) (letter, color string) {
	letter, color = firstRuneOfName(m.Name), "#7A5AE0"
	if m.Agent != "" {
		color = "#2E9A5B"
	}
	return letter, color
}

// mcpStatePill — как сервер себя чувствует, одним словом. Выключенный
// сервер не вызывается вовсе, и это важнее того, что он отвечал раньше.
func mcpStatePill(m *store.MCPServer) Badge {
	switch {
	case !m.Enabled:
		return Badge{Text: "выключен"}
	case m.State == "ok":
		return Badge{Text: "на связи", Level: LevelOK}
	case m.State == "login":
		// сервер жив и ждёт входа: это не поломка, а следующий шаг
		return Badge{Text: "нужен вход", Level: LevelWarn}
	case m.State == "fail":
		// у сервера-агента отказ чаще не про сеть: способность просто не
		// разрешена, и «не отвечает» тут врёт
		if strings.Contains(m.Note, "не разрешена") {
			return Badge{Text: "нужно разрешение", Level: LevelWarn}
		}
		return Badge{Text: "не отвечает", Level: LevelWarn}
	}
	return Badge{Text: "не проверен"}
}

// mcpTrouble — у сервера что-то не так: уровень для полоски и точки.
func mcpTrouble(m *store.MCPServer) string {
	if m.Enabled && (m.State == "login" || m.State == "fail") {
		return LevelWarn
	}
	return ""
}

// mcpRulePhrase — «правила: 22 разрешить, 30 спросить»: что уйдёт без вопроса,
// что будет ждать кнопки. Нулевое молчит; решённых нет — молчит вся фраза.
func mcpRulePhrase(states []engine.MCPToolState) string {
	allow, ask, deny, _ := engine.MCPRuleCounts(states)
	var words []string
	for _, c := range []struct {
		n    int
		word string
	}{{allow, "разрешить"}, {ask, "спросить"}, {deny, "запретить"}} {
		if c.n > 0 {
			words = append(words, fmt.Sprintf("%d %s", c.n, c.word))
		}
	}
	if len(words) == 0 {
		return ""
	}
	return "правила: " + strings.Join(words, ", ")
}

// mcpToolsPill — сколько инструментов назвал сервер.
func mcpToolsPill(m *store.MCPServer) (Badge, bool) {
	if n := len(m.Tools); n > 0 {
		return Badge{Text: plural(n, "инструмент", "инструмента", "инструментов")}, true
	}
	return Badge{}, false
}

// mcpRow — тонкая строка сервера: плитка, имя, не больше двух капсул (как
// себя чувствует и сколько инструментов), одна фраза, одна кнопка и «···».
// Вся строка ведёт на экран сервера. Вход, «В чате», подсказки и правила —
// на самом экране.
func (s *Server) mcpRow(m *store.MCPServer, view engine.MCPLoginView, day string) Component {
	letter, color := mcpTile(m)
	pills := []Badge{mcpStatePill(m)}
	if tools, ok := mcpToolsPill(m); ok {
		pills = append(pills, tools)
	}

	// одна фраза: беда, адрес, как входит, правила, вызовы
	var parts []string
	if m.State == "fail" && m.Enabled && m.Note != "" {
		parts = append(parts, clipRunes(strings.Join(strings.Fields(m.Note), " "), 120))
	}
	if m.Agent != "" {
		can := m.Can
		if can == "" {
			can = "код"
		}
		parts = append(parts, "агент «"+m.Agent+"», способность «"+can+"»", "шифрованный канал")
	} else {
		parts = append(parts, m.URL)
		switch {
		case m.Secret != "":
			parts = append(parts, "ключ «"+m.Secret+"»")
		case view.Pending:
			parts = append(parts, "вход начат")
		case view.Has:
			parts = append(parts, "вход OAuth выполнен")
		case m.State != "login":
			parts = append(parts, "без ключа")
		}
	}
	states := engine.MCPToolStates(m)
	if phrase := mcpRulePhrase(states); phrase != "" {
		parts = append(parts, phrase)
	}
	// новые инструменты — на полке «Ждут вас», в фразу они не едут: она и так в две строки
	if n := s.eng.Store().CountMCPCalls(m.Name, day); n > 0 {
		parts = append(parts, plural(n, "вызов", "вызова", "вызовов")+" за сутки")
	}

	row := Component{Type: "row", Layout: "wide", ID: "mcp-" + m.Name, Subtitle: m.Name,
		Avatar: letter, AvatarColor: color, AvatarShape: "square",
		Caption: strings.Join(parts, " · "), Badges: pills}
	if level := mcpTrouble(m); level != "" {
		withLevel(&row, level)
	}
	primary := mcpPrimary(m, view)
	row.Actions = []Action{{Action: "$goto", Target: entityAddr(mcpScreen, m.Name, ""), Label: m.Name}, primary}
	row.Menu = mcpMenu(m, view, primary)
	return row
}

// mcpMenu — «···» сервера: что не главное. Проверка, когда главная кнопка
// другая; вход, начатый заново или отменённый; короткие пути по вкладкам —
// у телефона вкладки в одну полосу, а меню под рукой; выключатель сервера.
func mcpMenu(m *store.MCPServer, view engine.MCPLoginView, primary Action) []Action {
	var menu []Action
	if primary.Action != "check_mcp" {
		menu = append(menu, Action{Action: "check_mcp", Target: m.Name, Label: "Проверить"})
	}
	for _, a := range loginActions(m, view) {
		if a.Action != primary.Action {
			menu = append(menu, a)
		}
	}
	menu = append(menu,
		Action{Action: "$goto", Target: entityAddr(mcpScreen, m.Name, mcpTabRules), Label: "Правила"},
		Action{Action: "$goto", Target: entityAddr(mcpScreen, m.Name, mcpTabLog), Label: "Журнал"})
	if m.Enabled {
		menu = append(menu, Action{Action: "toggle_mcp", Target: m.Name, Label: "Выключить сервер"})
	} else {
		menu = append(menu, Action{Action: "toggle_mcp", Target: m.Name, Label: "Включить сервер"})
	}
	return menu
}

// mcpByPhrase — строка под заголовком раздела: сколько серверов, как они себя
// чувствуют, сколько открыто чату и что ждёт кнопки.
func mcpByPhrase(servers []*store.MCPServer, pending int) string {
	if len(servers) == 0 {
		return "Ни одного сервера MCP. Сёма ходит к ним тем же протоколом, которым сам отвечает агентам; ключ или токены входа подставляет ядро, в текст модели они не попадают."
	}
	ok, login, fail, chat := 0, 0, 0, 0
	for _, m := range servers {
		if !m.Enabled {
			continue
		}
		switch m.State {
		case "ok":
			ok++
		case "login":
			login++
		case "fail":
			fail++
		}
		if m.Chat && m.Agent == "" {
			chat++
		}
	}
	phrase := plural(len(servers), "сервер MCP", "сервера MCP", "серверов MCP")
	var states []string
	if ok > 0 {
		states = append(states, fmt.Sprintf("на связи %d", ok))
	}
	if login > 0 {
		states = append(states, fmt.Sprintf("ждут входа %d", login))
	}
	if fail > 0 {
		states = append(states, fmt.Sprintf("не отвечают %d", fail))
	}
	if len(states) > 0 {
		phrase += ": " + strings.Join(states, ", ")
	}
	phrase += "; открыто чату " + strconv.Itoa(chat) + "."
	if pending > 0 {
		phrase += " " + plural(pending, "вызов ждёт", "вызова ждут", "вызовов ждут") + " вашей кнопки."
	}
	return phrase
}

// mcpShelf — полка «Ждут вас»: вызовы на подтверждение и серверы, у которых
// появились инструменты, про которые ещё не решено. Всё, что не поедет без
// человека, одним тёплым блоком. Пустое имя — у всех серверов; нет ничего —
// полки нет.
func (s *Server) mcpShelf(server string) *Component {
	st := s.eng.Store()
	var rows []Component
	pending := 0
	list, _ := st.MCPPendingList()
	for _, p := range list {
		if server != "" && p.Server != server {
			continue
		}
		pending++
		title := p.Tool
		caption := "просил " + p.Who + " · " + whenText(p.Created) + "\n" + clipRunes(p.Args, 300)
		if server == "" {
			title += " · " + p.Server
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "mcp-pending-" + strconv.FormatInt(p.ID, 10),
			Level: LevelWarn, Dot: levelDot(LevelWarn), Subtitle: title, Caption: caption,
			Actions: []Action{
				{Action: "approve_mcp", Target: strconv.FormatInt(p.ID, 10), Label: "Выполнить", Style: "primary"},
				{Action: "reject_mcp", Target: strconv.FormatInt(p.ID, 10), Label: "Отклонить"},
				{Action: "approve_mcp_always", Target: strconv.FormatInt(p.ID, 10), Label: "Выполнить и разрешить впредь"},
			}})
	}
	fresh := 0
	servers, _ := st.MCPServers()
	for _, m := range servers {
		if server != "" && m.Name != server {
			continue
		}
		_, _, _, n := engine.MCPRuleCounts(engine.MCPToolStates(m))
		if n == 0 {
			continue
		}
		fresh += n
		def := engine.PolicyTitle(defaultOf(m))
		row := Component{Type: "row", Layout: "wide", ID: "mcp-new-" + m.Name, Level: LevelInfo, Dot: levelDot(LevelInfo),
			Subtitle: "«" + m.Name + "»: " + plural(n, "новый инструмент", "новых инструмента", "новых инструментов") + " ждут решения",
			Caption:  "пока для них «" + def + "»: модель их не видит, пока вы не решите за каждый",
			Actions:  []Action{{Action: "$goto", Target: entityAddr(mcpScreen, m.Name, mcpTabRules), Label: "Решить", Style: "primary"}}}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	var meta []string
	if pending > 0 {
		meta = append(meta, plural(pending, "вызов", "вызова", "вызовов")+" из круга «Спросить» · до нажатия на чужой сервер ничего не уходит")
	}
	if fresh > 0 {
		meta = append(meta, plural(fresh, "новый инструмент", "новых инструмента", "новых инструментов")+" без решения")
	}
	return &Component{Type: "shelf", ID: "mcp-shelf", Title: "Ждут вас", Meta: strings.Join(meta, " · "), Children: rows}
}

// buildMCPSection — раздел: заголовок с одной кнопкой «Добавить сервер MCP»,
// полка, список серверов, справка, «Тонкая настройка» (её собирает foldRare) и
// общий журнал вызовов — последним, ленивым.
func (s *Server) buildMCPSection(doc *Document) error {
	st := s.eng.Store()
	doc.Heading = "Интеграции"
	servers, err := st.MCPServers()
	if err != nil {
		return err
	}
	pend, _ := st.MCPPendingList()
	doc.Subheading = mcpByPhrase(servers, len(pend))
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	// «Добавить сервер MCP» — единственная кнопка шапки раздела
	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(mcpScreen, mcpNew, ""),
		Label: "Добавить сервер MCP", Style: "primary"}}

	// то, что ждёт кнопки, стоит вверху: раньше это лежало под формами
	if shelf := s.mcpShelf(""); shelf != nil {
		add(*shelf)
	}
	day := mcpDayAgo()
	rows := make([]Component, 0, len(servers))
	for _, m := range servers {
		rows = append(rows, s.mcpRow(m, s.eng.MCPLoginView(m), day))
	}
	if len(rows) == 0 {
		empty := emptyState("Серверов MCP пока нет",
			"Сервер MCP — чужой сервис, у которого Сёма берёт инструменты: задачи, вики, репозитории. Нужен адрес вида https://…/mcp; вход — ключом из «Ключей и секретов» или кнопкой «Войти», если сервер так умеет", "tray")
		empty.Actions = []Action{{Action: "$goto", Target: entityAddr(mcpScreen, mcpNew, ""), Label: "Добавить сервер MCP", Style: "primary"}}
		rows = append(rows, empty)
	}
	add(Component{Type: "section", Title: fmt.Sprintf("Серверы MCP · %d", len(servers)), Children: rows})
	add(Component{Type: "section", ID: "mcp-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Сёма ходит к чужим серверам MCP тем же протоколом, которым сам отвечает агентам; ключ или токены входа подставляет ядро, в текст модели они не попадают. " +
			"Разрешённый вызов идёт сразу, «Спросить» ложится в «Уведомления» и ждёт вашей кнопки, запрещённый не выполняется никогда. " +
			"Инструмент, про который вы ещё не решили, запрещён. Правила — у каждого инструмента свои, на вкладке «Правила» сервера, а каждый вызов виден в его журнале."}}})

	s.mcpLimits(add)

	// общий журнал — хроника, а не настройка: последним и по раскрытию
	add(Component{Type: "section", ID: mcpLogPart, Title: "Журнал вызовов", Caption: "что звали, по какому правилу и чем кончилось — по всем серверам",
		Tier: tierTail, Collapsed: true, Lazy: mcpLogPart})
	return nil
}

// mcpLimits — пределы раздела: «Тонкая настройка» (settings_rare.go). Их
// правят раз в год, а лежали двумя формами под списком серверов. Умолчания и
// границы — из реестра настроек, а не числами рядом с полем.
func (s *Server) mcpLimits(add func(Component)) {
	add(Component{Type: "form", ID: "mcp-chat-limits", Title: "Серверы MCP в чате", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("mcp.chat_chars", "chat_chars", "Знаков на описания инструментов в промпте",
				"Сколько места занимает полный список инструментов; не помещается — описания ужимаются, потом режутся по инструментам целиком", ""),
			s.settingField("mcp.chat_calls", "chat_calls", "Вызовов за один ответ",
				"Остальные не выполняются, и модель об этом узнаёт", ""),
			s.settingField("mcp.chat_result_chars", "chat_result_chars", "Знаков результата, которые видит модель",
				"Длиннее — обрезается с названным остатком; длинный список записей приходит выжимкой", ""),
			s.settingField("mcp.chat_list_fields", "chat_list_fields", "Поля записи в выжимке длинного списка",
				"Длинный список модель видит строками из этих полей, а не началом JSON. Поля через запятую", ""),
			s.settingField("mcp.card_chars", "card_chars", "Знаков результата в карточке чата",
				"Остальное — по кнопке «Весь вывод»", ""),
			s.settingField("mcp.recheck_s", "recheck_s", "Перепроверять упавший сервер не чаще",
				"Сервер, отвечавший «не отвечает», чат проверяет сам, чтобы не остаться без него из-за минутного сбоя; 0 — не перепроверять", "с"),
			s.settingField("mcp.error_chars", "error_chars", "Знаков сути ошибки инструмента в первой строке",
				"Суть ошибки сервера (то, что он сказал словами) стоит первой строкой карточки и ответа модели и по «знакам результата для модели» не режется; сырой ответ идёт ниже и режется", ""),
			s.settingField("mcp.arg_retries", "arg_retries", "Сколько раз модель исправляет вызов после отказа по аргументам",
				"Сервер не принял формат — модель читает ошибку и присылает вызов снова (изменяющий снова ждёт вашей кнопки); после этого числа отказов подряд модель отвечает честно; 0 — не просить повторять", ""),
			s.settingField("mcp.tool_hint_days", "tool_hint_days", "Дней помнить подсказку про формат аргументов",
				"Когда сервер отклонил аргументы, его слова про формат идут в список инструментов в промпте («⚠ сервер требует»), чтобы следующий вызов был сразу верным; 0 — не подсказывать. Стереть раньше — «Забыть подсказки» на экране сервера", ""),
			s.settingField("mcp.tool_hint_chars", "tool_hint_chars", "Знаков сути в подсказке",
				"Длиннее — обрезается с названным остатком", ""),
		},
		Submit: &Action{Action: "set_mcp_chat_limits", Label: "Сохранить"}})
	docHint := "Вместо регистрации Сёма называет серверу входа адрес своего документа, а тот скачивает его сам: так вход не зависит от того, какой из узлов сервера ответит. Сейчас недоступно: нужен публичный https-адрес в «Доступ → Адрес страницы входа»"
	if u, ok := s.eng.MCPClientDocumentURL(); ok {
		docHint = "Вместо регистрации Сёма называет серверу входа адрес " + u + ", а тот скачивает документ сам: так вход не зависит от того, какой из узлов сервера ответит. Работает, когда сервер это умеет и вход начат именно с этого адреса; иначе — регистрация, как раньше"
	}
	add(Component{Type: "form", ID: "mcp-login-limits", Title: "Вход на серверы MCP", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("mcp.login_ttl_min", "login_ttl_min", "Сколько живёт начатый вход",
				"От нажатия «Войти» до возврата со страницы сервера", "мин"),
			s.settingField("mcp.refresh_skew_s", "refresh_skew_s", "Продлевать токен за",
				"До конца срока: токен обновляется до того, как вызов упрётся в его конец", "с"),
			s.settingField("mcp.net_timeout_s", "net_timeout_s", "Сколько ждать сервер входа",
				"Один запрос: метаданные, регистрация, выдача токенов", "с"),
			s.settingField("mcp.token_retries", "token_retries", "Повторов при invalid_client",
				"Обмен кода и продление: сервер входа за балансировщиком отвечает через раз «клиент неизвестен», повтор попадает на другой его узел; 0 — не повторять", ""),
			s.settingField("mcp.token_retry_ms", "token_retry_ms", "Пауза между повторами",
				"", "мс"),
			s.settingField("mcp.client_metadata", "client_metadata", "Называть себя документом клиента", docHint, ""),
		},
		Submit: &Action{Action: "set_mcp_login_limits", Label: "Сохранить"}})
}

// mcpLogFragment — тело общего журнала: двадцать строк и «Показать ещё».
// Хроника приезжает по раскрытию, а не вместе с экраном.
func (s *Server) mcpLogFragment(_ string, _ string, limit int) ([]Component, error) {
	st := s.eng.Store()
	entries, err := st.MCPLogFiltered("", store.MCPLogAll, limit+1)
	if err != nil {
		return nil, err
	}
	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	rows := make([]Component, 0, len(entries)+1)
	for _, u := range entries {
		rows = append(rows, mcpLogRow(u, true))
	}
	if len(rows) == 0 {
		return []Component{emptyState("Вызовов ещё не было", mcpLogEmptyWhy, "list.bullet")}, nil
	}
	if more {
		rows = append(rows, moreInFragment(len(entries), st.CountMCPLog("", store.MCPLogAll)))
	}
	return rows, nil
}

const mcpLogEmptyWhy = "Сюда попадает каждый вызов чужого сервера: кто просил, по какому правилу прошёл и чем кончился. Ответы сервера проходят маски секретов до записи"

// mcpLogRow — строка журнала вызовов. Имя сервера в заголовке — только у
// общего журнала: у журнала самого сервера оно лишнее.
func mcpLogRow(u store.MCPUse, withServer bool) Component {
	badge := map[string]Badge{
		store.MCPUseDone:     {Text: "выполнен", Level: LevelOK},
		store.MCPUseWaiting:  {Text: "ждал кнопки", Level: LevelWarn},
		store.MCPUseRejected: {Text: "отклонён"},
		store.MCPUseDenied:   {Text: "отказ", Level: LevelAlert},
		store.MCPUseError:    {Text: "ошибка", Level: LevelAlert},
	}[u.State()]
	title := u.Tool
	if withServer {
		title += " · " + u.Server
	}
	// ярус ответа стоит капсулой: точность ответа важнее его длины, и в журнале
	// она видна сразу
	tier, result := splitTier(u.Result)
	row := Component{Type: "row", Layout: "wide", ID: "mcp-log-" + strconv.FormatInt(u.ID, 10), Subtitle: title,
		Caption: u.Who + " · " + whenText(u.At) + " · " + circleTitle(u.Circle),
		Badges:  []Badge{badge}, Detail: result, Expandable: result != ""}
	// с чем звали — отдельной строкой фактов: в подписи аргументы читались как часть правила
	if args := strings.TrimSpace(u.Args); args != "" && args != "{}" && args != "null" {
		row.Facts = []Prop{{Label: "Аргументы", Value: clipRunes(args, 200)}}
	}
	if tier != "" {
		row.Badges = append(row.Badges, tierBadge(tier))
	}
	return row
}
