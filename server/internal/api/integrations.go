package api

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

// Раздел «Интеграции»: серверы MCP. Список тонких строк (settings_mcp.go), у
// каждого сервера свой экран с вкладками «Обзор · Правила · Журнал»
// (settings_mcp_server.go) и экран «Добавить сервер MCP» (settings_mcp_new.go).
// Правила по инструментам — integrations_rules.go. Ядро хранит справочник;
// ключ живёт в хранилище секретов, здесь только его имя.
//
// Адреса (макет — design/nastroyki-2/Predely.html):
//
//	settings:integrations                 раздел: список серверов
//	settings:integrations@new             «Добавить сервер MCP»
//	settings:integrations@<имя>           экран сервера, «Обзор»
//	settings:integrations@<имя>~rules|log
//
// До 2 октября 2026 правила открытого сервера лежали на этом же экране под
// списком (`settings:integrations@<имя>`), а у раздела была одна лента; адрес
// без вкладки теперь открывает «Обзор», а уведомления ядра строят адрес через
// `engine.MCPServerScreen` и называют вкладку.

const mcpScreen = "settings:integrations"

// Внутренние вкладки сервера. У раздела вкладок нет: список, «Тонкая
// настройка» и общий журнал вызовов одной лентой.
const (
	mcpTabOverview = ""      // Обзор
	mcpTabRules    = "rules" // Правила: разрешить, спросить, запретить
	mcpTabLog      = "log"   // Журнал вызовов этого сервера

	// mcpNew — «сервер» с этим именем означает экран добавления
	mcpNew = "new"
)

// mcpAddr — разобранный адрес экрана «Интеграций».
type mcpAddr struct {
	Server string // имя сервера; пусто — раздел; «new» — «Добавить сервер MCP»
	Tab    string // вкладка сервера
}

func parseMCPAddr(screen string) mcpAddr {
	_, id, tab := splitEntity(screen)
	return mcpAddr{Server: strings.TrimSpace(id), Tab: tab}
}

// canonMCPAddr — адрес по нынешним правилам. У раздела вкладок нет, а старые
// закладки («~trackers») помнят их: такой хвост открывает сам раздел, и адрес
// документа — адрес раздела. Адрес сервера остаётся как есть: неизвестная
// вкладка у него открывает «Обзор».
func canonMCPAddr(screen string) string {
	section, id, tab := splitEntity(screen)
	if section == mcpScreen && id == "" && tab != "" {
		return mcpScreen
	}
	return screen
}

// mcpOwnSearch — верхнее поле на этом экране ищет инструменты правил, а не
// настройки: поле одно на экран, и набранное нужно списку правил сервера.
func mcpOwnSearch(screen string) bool {
	a := parseMCPAddr(screen)
	return a.Server != "" && a.Server != mcpNew && a.Tab == mcpTabRules
}

// mcpServerOfSecret — чья это запись входа: токены лежат в секрете
// «mcp:<сервер>:oauth».
func mcpServerOfSecret(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "mcp:")
	if !ok {
		return "", false
	}
	server, ok := strings.CutSuffix(rest, ":oauth")
	return server, ok && server != ""
}

// stayMCP — после действия остаёмся там, где нажали: «Проверить» на экране
// сервера не должно уводить в список.
func stayMCP(req actionRequest, def string) string {
	if strings.HasPrefix(req.Screen, mcpScreen) {
		return req.Screen
	}
	return def
}

func (s *Server) buildMCP(doc *Document, screen, filter, search string, limit int) error {
	a := parseMCPAddr(screen)
	doc.Layout = "wide"
	switch {
	case a.Server == mcpNew:
		return s.buildMCPNew(doc)
	case a.Server != "":
		return s.buildMCPServer(doc, a, filter, search, limit)
	}
	return s.buildMCPSection(doc)
}

// loginFact — строка «Вход» в факты сервера: выполнен, нужен или не требуется.
// Токен не читается: сведения берутся из строки сервера, а не из хранилища.
func loginFact(m *store.MCPServer, v engine.MCPLoginView) Prop {
	scope := ""
	if v.Scope != "" {
		scope = " · права: " + clipRunes(v.Scope, 160)
	}
	switch {
	case v.Pending:
		return Prop{Label: "Вход", Value: "начат — откройте страницу входа, она действует до " + v.Until.Local().Format("15:04") +
			". Если сервер ответил там «invalid_client», вернитесь и нажмите «Начать вход заново»: у серверов за балансировщиком это бывает через раз", Color: "#B25000"}
	case v.Has:
		switch {
		case v.Expires.IsZero():
			return Prop{Label: "Вход", Value: "выполнен" + scope}
		case v.Expires.After(time.Now()):
			return Prop{Label: "Вход", Value: "выполнен, действует до " + whenText(v.Expires.Format(time.RFC3339)) + scope}
		case v.Refresh:
			return Prop{Label: "Вход", Value: "выполнен, токен обновится сам при следующем вызове" + scope}
		}
		return Prop{Label: "Вход", Value: "токен истёк " + whenText(v.Expires.Format(time.RFC3339)) + ", обновить нечем — нужен вход", Color: "#B25000"}
	case m.State == "login":
		return Prop{Label: "Вход", Value: "нужен вход", Color: "#B25000"}
	case m.State == "ok":
		return Prop{Label: "Вход", Value: "не требуется"}
	}
	return Prop{Label: "Вход", Value: "не проверен: нажмите «Проверить»"}
}

// mcpCanLogin — у сервера бывает вход OAuth: он по адресу и без постоянного
// ключа. Серверу жителя машины и серверу с ключом входить некуда.
func mcpCanLogin(m *store.MCPServer) bool { return m.Agent == "" && m.Secret == "" }

// mcpPrimary — единственная кнопка строки и шапки сервера: то, что сейчас
// нужно сделать. Вход начат — открыть страницу входа; нужен вход — «Войти»;
// иначе — проверить. «Войти» готовит вход, «Открыть страницу входа» уводит на
// страницу сервера входа: одним нажатием нельзя, браузер не даёт открыть новую
// вкладку из ответа, пришедшего после запроса, а настоящий щелчок по кнопке
// проходит всегда.
func mcpPrimary(m *store.MCPServer, v engine.MCPLoginView) Action {
	if mcpCanLogin(m) {
		switch {
		case v.Pending:
			return Action{Action: "$open", Target: v.PendingURL, Label: "Открыть страницу входа", Style: "primary"}
		case m.State == "login" && !v.Has:
			return Action{Action: "mcp_login", Target: m.Name, Label: "Войти", Style: "primary"}
		}
	}
	return Action{Action: "check_mcp", Target: m.Name, Label: "Проверить"}
}

// loginActions — кнопки входа по состоянию. Главная — mcpPrimary; остальное
// («Начать вход заново», «Отменить вход», «Войти» у сервера, который просто
// не проверен) уходит в меню: правило одного действия в строке.
func loginActions(m *store.MCPServer, v engine.MCPLoginView) []Action {
	if !mcpCanLogin(m) {
		return nil
	}
	switch {
	case v.Pending:
		return []Action{
			{Action: "mcp_login", Target: m.Name, Label: "Начать вход заново"},
			{Action: "mcp_login_cancel", Target: m.Name, Label: "Отменить вход"}}
	case v.Has:
		return nil
	}
	return []Action{{Action: "mcp_login", Target: m.Name, Label: "Войти"}}
}

// mcpHintCounts — сколько подсказок инструментов у сервера в силе и сколько
// всего. Подсказка за сроком остаётся в базе, но в промпт не идёт.
func mcpHintCounts(m *store.MCPServer, days int) (fresh, all int) {
	for _, h := range m.ToolHints {
		all++
		if mcpHintFresh(h, days) {
			fresh++
		}
	}
	return fresh, all
}

func mcpHintFresh(h store.MCPToolHint, days int) bool {
	at, err := time.Parse(time.RFC3339, h.At)
	return days > 0 && err == nil && time.Since(at) <= time.Duration(days)*24*time.Hour
}

// mcpHintRows — подсказки инструментов строками: что сервер сказал, когда и
// жива ли подсказка; у каждой своя кнопка «Забыть».
func mcpHintRows(m *store.MCPServer, days int) []Component {
	tools := make([]string, 0, len(m.ToolHints))
	for tool := range m.ToolHints {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	var rows []Component
	for _, tool := range tools {
		h := m.ToolHints[tool]
		level, state := LevelInfo, "идёт в промпт чата"
		if !mcpHintFresh(h, days) {
			level, state = LevelWarn, "срок вышел: в промпт не идёт"
			if days <= 0 {
				state = "подсказки выключены (срок 0 дней): в промпт не идёт"
			}
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", Level: level, Dot: levelDot(level),
			Subtitle: tool, Caption: "сервер сказал " + whenText(h.At) + " · " + state + "\n" + clipRunes(h.Gist, 400),
			Actions: []Action{{Action: "forget_mcp_hint", Target: m.Name + "~" + tool, Label: "Забыть"}}})
	}
	return rows
}

// circleTitle — ответ правила в журнале вызовов. Старые строки журнала
// хранят прежние слова кругов (read, confirm, never), новые — allow, ask,
// deny; читаются и те, и другие.
func circleTitle(c string) string {
	switch c {
	case engine.PolicyAllow, "read":
		return "разрешено"
	case engine.PolicyAsk, "confirm":
		return "с подтверждением"
	case engine.PolicyDeny, "never":
		return "запрещено"
	}
	return c
}

func (s *Server) integrationsActions(req actionRequest) (string, error) {
	st := s.eng.Store()
	switch req.Action {
	case "add_mcp":
		name := strings.TrimSpace(req.Params["name"])
		// имя едет в адресе экрана («settings:integrations@имя~вкладка»): знаки,
		// которыми адрес делится, в нём недопустимы, а «new» — экран добавления
		if strings.ContainsAny(name, "@~") {
			return "", fmt.Errorf("в имени сервера не бывает «@» и «~»: ими делится адрес экрана")
		}
		if name == mcpNew {
			return "", fmt.Errorf("имя «%s» занято под экран добавления — назовите сервер иначе", mcpNew)
		}
		m := &store.MCPServer{
			Name:    name,
			URL:     strings.TrimSpace(req.Params["url"]),
			Secret:  strings.TrimSpace(req.Params["secret"]),
			Header:  strings.TrimSpace(req.Params["header"]),
			Prefix:  req.Params["prefix"],
			Agent:   strings.TrimSpace(req.Params["agent"]),
			Can:     strings.TrimSpace(req.Params["can"]),
			Enabled: true,

			LoginScope: req.Params["login_scope"],
			// заведённый человеком сервер по HTTP открыт чату: подключил — модель
			// видит его инструменты. Сервер жителя машины — нет, у него инструменты кода
			Chat:     strings.TrimSpace(req.Params["agent"]) == "",
			About:    strings.TrimSpace(req.Params["about"]),
			Keywords: strings.TrimSpace(req.Params["keywords"]),
			Class:    store.ClassPlain,
		}
		if err := st.SaveMCPServer(m); err != nil {
			return "", err
		}
		// причина отказа записана в состоянии сервера и видна на его экране, куда
		// человека и ведут
		_ = s.eng.CheckMCP(context.Background(), m.Name)
		return entityAddr(mcpScreen, m.Name, ""), nil
	case "check_mcp":
		_ = s.eng.CheckMCP(context.Background(), req.Target)
		return stayMCP(req, mcpScreen), nil
	case "toggle_mcp":
		m, ok := st.MCPServer(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		m.Enabled = !m.Enabled
		return stayMCP(req, mcpScreen), st.SaveMCPServer(m)
	case "toggle_mcp_chat":
		m, ok := st.MCPServer(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		if m.Agent != "" {
			return "", fmt.Errorf("сервер жителя машины чату не открывается: у него инструменты кода")
		}
		m.Chat = !m.Chat
		return stayMCP(req, mcpScreen), st.SaveMCPServer(m)
	case "save_mcp_chat":
		m, ok := st.MCPServer(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		if m.Agent != "" {
			return "", fmt.Errorf("сервер жителя машины чату не открывается: у него инструменты кода")
		}
		class := strings.TrimSpace(req.Params["class"])
		switch class {
		case store.ClassPlain, store.ClassSensitive, store.ClassSecret:
		case "":
			class = m.Class
		default:
			return "", fmt.Errorf("класс данных бывает обычное, чувствительное или секретное")
		}
		m.Chat = req.Params["chat"] == "true"
		m.About = strings.TrimSpace(req.Params["about"])
		m.Keywords = strings.TrimSpace(req.Params["keywords"])
		m.Class = class
		return stayMCP(req, entityAddr(mcpScreen, m.Name, "")), st.SaveMCPServer(m)
	case "set_mcp_chat_limits":
		return mcpScreen, s.saveSettings(req,
			settingParam{"chat_chars", "mcp.chat_chars", "знаков на описания инструментов"},
			settingParam{"chat_calls", "mcp.chat_calls", "вызовов за ответ"},
			settingParam{"chat_result_chars", "mcp.chat_result_chars", "знаков результата для модели"},
			settingParam{"chat_list_fields", "mcp.chat_list_fields", "поля записи в выжимке списка"},
			settingParam{"card_chars", "mcp.card_chars", "знаков результата в карточке"},
			settingParam{"error_chars", "mcp.error_chars", "знаков сути ошибки"},
			settingParam{"arg_retries", "mcp.arg_retries", "повторов после отказа по аргументам"},
			settingParam{"tool_hint_days", "mcp.tool_hint_days", "дней подсказки про формат"},
			settingParam{"tool_hint_chars", "mcp.tool_hint_chars", "знаков сути в подсказке"},
			settingParam{"recheck_s", "mcp.recheck_s", "секунд между перепроверками"})
	case "forget_mcp_hints":
		if _, ok := st.MCPServer(req.Target); !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		_, err := st.ForgetMCPToolHints(req.Target, "")
		return stayMCP(req, entityAddr(mcpScreen, req.Target, "")), err
	case "forget_mcp_hint":
		// цель — «сервер~инструмент»: подсказка стирается у одного инструмента
		server, tool, _ := strings.Cut(req.Target, "~")
		if _, ok := st.MCPServer(server); !ok || strings.TrimSpace(tool) == "" {
			return "", fmt.Errorf("нужен сервер и инструмент, например planner~task_update")
		}
		_, err := st.ForgetMCPToolHints(server, tool)
		return stayMCP(req, entityAddr(mcpScreen, server, "")), err
	case "delete_mcp":
		return mcpScreen, s.eng.DeleteMCPServer(context.Background(), req.Target)
	case "mcp_login":
		// первый щелчок готовит вход (находит сервер входа, регистрирует
		// Сёму), второй — «Открыть страницу входа» — уводит на страницу
		// согласия. Одним щелчком нельзя: браузер не даёт открыть новую
		// вкладку из ответа, пришедшего после запроса, а настоящий щелчок
		// по кнопке проходит всегда
		if _, err := s.eng.MCPLoginBegin(context.Background(), req.Target, req.origin, req.who); err != nil {
			return "", err
		}
		return stayMCP(req, mcpScreen), nil
	case "mcp_login_cancel":
		s.eng.MCPLoginCancel(req.Target)
		return stayMCP(req, mcpScreen), nil
	case "mcp_logout":
		return stayMCP(req, mcpScreen), s.eng.MCPLogout(context.Background(), req.Target)
	case "set_mcp_login_limits":
		return mcpScreen, s.saveSettings(req,
			settingParam{"login_ttl_min", "mcp.login_ttl_min", "минут жизни начатого входа"},
			settingParam{"refresh_skew_s", "mcp.refresh_skew_s", "секунд до конца токена"},
			settingParam{"net_timeout_s", "mcp.net_timeout_s", "секунд ожидания сервера входа"},
			settingParam{"token_retries", "mcp.token_retries", "повторов при invalid_client"},
			settingParam{"token_retry_ms", "mcp.token_retry_ms", "миллисекунд между повторами"},
			settingParam{"client_metadata", "mcp.client_metadata", "документ клиента"})
	case "mcp_login_scope", "save_mcp_login_scope":
		// прежний вызов («Права входа…» с вопросом) шлёт ответ в `value`, поле
		// на экране сервера — в `login_scope`; действует при следующем входе
		m, ok := st.MCPServer(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		scope, has := req.Params["login_scope"]
		if !has {
			scope = req.Params["value"]
		}
		m.LoginScope = scope
		return stayMCP(req, mcpScreen), st.SaveMCPServer(m)
	case mcpRulesSaving:
		return s.saveMCPRules(req)
	case "save_mcp_groups":
		if _, ok := st.MCPServer(req.Target); !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		var lines []string
		for _, line := range strings.Split(req.Params["groups"], "\n") {
			if p, title, ok := strings.Cut(line, "="); ok && strings.TrimSpace(p) != "" && strings.TrimSpace(title) != "" {
				lines = append(lines, strings.TrimSpace(p)+" = "+strings.TrimSpace(title))
			}
		}
		return stayMCP(req, entityAddr(mcpScreen, req.Target, mcpTabRules)), st.SetSetting(mcpGroupsKey+req.Target, strings.Join(lines, "\n"))
	case "approve_mcp", "reject_mcp", "approve_mcp_always":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("нужен номер вызова")
		}
		decide := func() (string, error) {
			if req.Action == "approve_mcp_always" {
				return s.eng.ApproveMCPAlways(context.Background(), id)
			}
			return s.eng.ApproveMCP(context.Background(), id, req.Action == "approve_mcp")
		}
		// нажали не на «Интеграциях» (карточка в чате, уведомление): остаёмся на
		// своём экране, а вызов, который может идти минуту, не держит запрос
		if !strings.HasPrefix(req.Screen, mcpScreen) {
			go func() { _, _ = decide() }()
			return "", nil
		}
		if _, err := decide(); err != nil {
			return "", err
		}
		return req.Screen, nil
	}
	return "", errUnhandled
}

// splitTier — карточка агента начинается со строки «[ярус lsp] …»:
// вынимаем ярус, чтобы он стал капсулой, а не шумом в тексте.
func splitTier(result string) (tier, rest string) {
	if !strings.HasPrefix(result, "[ярус ") {
		return "", result
	}
	end := strings.Index(result, "]")
	if end < 0 {
		return "", result
	}
	tier = strings.TrimSpace(result[len("[ярус "):end])
	rest = strings.TrimLeft(result[end+1:], " \n")
	return tier, rest
}

// tierBadge — чем добыт ответ. Приблизительный ярус виден цветом: клиент,
// не знающий точности ответа, будет уверенно ошибаться.
func tierBadge(tier string) Badge {
	switch tier {
	case "lsp":
		return Badge{Text: "ярус lsp", Color: "#1E7A32"}
	case "tags":
		return Badge{Text: "ярус tags · приблизительно", Color: "#B25000"}
	case "text":
		return Badge{Text: "ярус text · догадка", Color: "#B25000"}
	}
	return Badge{Text: "ярус " + tier, Color: "#8E8E93"}
}
