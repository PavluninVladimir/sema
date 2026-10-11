package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Справочник серверов MCP: куда Сёма ходит чужим протоколом. Ключ хранится
// не здесь, а в хранилище секретов — тут только его имя.

type MCPServer struct {
	Name    string // как звать: planner, wiki
	URL     string // адрес /mcp
	Secret  string // имя секрета с токеном, пусто — без ключа
	Header  string // в каком заголовке отдавать ключ (по умолчанию Authorization)
	Prefix  string // что писать перед значением: «Bearer »
	Enabled bool
	State   string   // ok | fail
	Note    string   // последняя ошибка или имя сервера
	Tools   []string // вычитанные инструменты
	// LegacyRead и LegacyNever — прежние круги вызовов, образцы имён. Живут
	// только ради разового переезда на правила по инструментам
	// (engine.migrateMCPRules) и никем больше не читаются; колонки остались в
	// базе, чтобы сборка, которую придётся откатить, нашла свои круги на месте
	LegacyRead  []string
	LegacyNever []string
	Agent       string // сервер живёт у агента: вызовы идут по каналу агентов, а не по HTTP
	Can         string // какая способность агента: «код»
	Checked     string
	Created     string
	CallsDay    int // вызовов за сутки, для экрана

	// Чат. Chat — открыт ли сервер чату Сёмы: модель видит его инструменты и
	// зовёт их сама, как Claude Code и Codex. About — одна строка «что это»
	// для описи в промпте; Keywords — слова-приметы «когда звать» (через
	// запятую, как у скиллов): по ним полный список инструментов попадает в
	// промпт, когда вопрос про то, что хранит сервер. Class — класс данных
	// сервера (обычное / чувствительное / секретное): облачной модели ответы
	// сервера выше разрешённого не отдаются. ToolInfo — описания и схемы
	// аргументов, как сервер их назвал при проверке; Tools остаётся списком имён.
	Chat     bool
	About    string
	Keywords string
	Class    string
	ToolInfo []MCPTool

	// Правила по инструментам: у каждого свой ответ — allow, ask или deny.
	// Инструмента в Rules нет — он «новый»: решения человека про него ещё
	// нет, и отвечает RuleDefault (по умолчанию deny: не подошло ни одно
	// правило — запрещено). Как Login и ToolHints, правила SaveMCPServer НЕ
	// пишет: проверка, читавшая строку тридцать секунд назад, вернула бы
	// назад то, что человек решил за это время. Пишет SetMCPRules.
	Rules       map[string]string
	RuleDefault string
	RulesSet    bool // правила переехали из прежних кругов (или заведены): пусто — ещё нет

	// Вход OAuth. LoginScope — то, что человек попросил запрашивать у сервера
	// входа (через пробел; пусто — всё, что назвал сервер), это обычная
	// настройка строки. Login — несекретные сведения о выполненном входе для
	// экрана; SaveMCPServer его НЕ пишет: иначе проверка, загрузившая строку
	// раньше выхода, вернула бы «вход выполнен» уже удалённому входу. Токены
	// лежат в хранилище секретов, здесь их нет и быть не должно.
	LoginScope string
	Login      *MCPLogin

	// ToolHints — что сервер сам сказал про формат аргументов, когда отклонил
	// вызов: инструмент → последняя такая суть с временем. Как и Login, их
	// SaveMCPServer НЕ пишет: проверка, читавшая строку тридцать секунд назад,
	// затёрла бы подсказку, появившуюся за это время. Пишут и стирают
	// SetMCPToolHint и ForgetMCPToolHints.
	ToolHints map[string]MCPToolHint
}

// MCPToolHint — особенность инструмента, которую Сёма узнал от самого
// сервера: формат, который описание в схеме не назвало («ISO 8601 date
// string», а сервер требует дату со временем и поясом). Здесь только то, что
// сказал сервер, без аргументов вызова: подсказка идёт в промпт каждого чата
// и не должна нести чужие данные.
type MCPToolHint struct {
	Gist string `json:"gist"`
	At   string `json:"at"`
}

// MCPTool — инструмент сервера так, как он сам себя описал: имя, описание и
// схема аргументов (`inputSchema`) как есть. Сжимает её под промпт чата
// движок, а не хранилище: правило сжатия меняется, а схему заново тянуть не
// нужно.
type MCPTool struct {
	Name   string          `json:"name"`
	Desc   string          `json:"desc,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// MCPLogin — что экран знает о входе, не читая секрета: когда вошли, до
// какого часа действует токен, какие права выданы, есть ли чем продлить.
type MCPLogin struct {
	At      string `json:"at"`
	Expires string `json:"expires,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Refresh bool   `json:"refresh,omitempty"`
}

// MCPClientReg — зарегистрированный у сервера входа клиент. Регистрация
// принадлежит паре «сервер MCP + адрес возврата»: из другого адреса Сёма
// открыт — другой клиент.
type MCPClientReg struct {
	Server   string
	Redirect string
	Issuer   string
	ClientID string
	Created  string
}

func (s *Store) migrateMCP() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS mcp_servers (
			name     TEXT PRIMARY KEY,
			url      TEXT NOT NULL,
			secret   TEXT NOT NULL DEFAULT '',
			header   TEXT NOT NULL DEFAULT '',
			prefix   TEXT NOT NULL DEFAULT '',
			enabled  INTEGER NOT NULL DEFAULT 1,
			state    TEXT NOT NULL DEFAULT '',
			note     TEXT NOT NULL DEFAULT '',
			tools    TEXT NOT NULL DEFAULT '[]',
			read_    TEXT NOT NULL DEFAULT '[]', -- прежние круги: читает только переезд
			never    TEXT NOT NULL DEFAULT '[]',
			checked  TEXT NOT NULL DEFAULT '',
			created  TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_log (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			server  TEXT NOT NULL,
			tool    TEXT NOT NULL,
			who     TEXT NOT NULL DEFAULT '',
			circle  TEXT NOT NULL DEFAULT '',
			ok      INTEGER NOT NULL DEFAULT 1,
			args    TEXT NOT NULL DEFAULT '',
			result  TEXT NOT NULL DEFAULT '',
			at      TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_log_at ON mcp_log(at)`,
		`ALTER TABLE mcp_servers ADD COLUMN agent TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mcp_servers ADD COLUMN can TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mcp_servers ADD COLUMN login_scope TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mcp_servers ADD COLUMN login TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mcp_servers ADD COLUMN chat INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE mcp_servers ADD COLUMN about TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mcp_servers ADD COLUMN keywords TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mcp_servers ADD COLUMN class TEXT NOT NULL DEFAULT 'обычное'`,
		`ALTER TABLE mcp_servers ADD COLUMN tool_info TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE mcp_servers ADD COLUMN tool_hints TEXT NOT NULL DEFAULT '{}'`,
		// правила по инструментам: пустая строка — ещё не переехали из кругов
		`ALTER TABLE mcp_servers ADD COLUMN rules TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mcp_servers ADD COLUMN rule_default TEXT NOT NULL DEFAULT 'deny'`,
		// client_id публичного клиента — не секрет: по нему сервер входа лишь
		// узнаёт, кто просит. Секрет здесь только токены, и они в хранилище
		`CREATE TABLE IF NOT EXISTS mcp_clients (
			server   TEXT NOT NULL,
			redirect TEXT NOT NULL,
			issuer   TEXT NOT NULL,
			client   TEXT NOT NULL,
			created  TEXT NOT NULL,
			PRIMARY KEY (server, redirect, issuer)
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			// ALTER на уже добавленной колонке — не беда: миграция идёт
			// на каждом старте.
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("серверы MCP: %w", err)
		}
	}
	if err := s.migrateMCPPending(); err != nil {
		return err
	}
	return s.seedMCPChat()
}

// mcpChatSeedKey — метка разового заполнения «серверы в чате».
const mcpChatSeedKey = "mcp.chat_seeded"

// seedMCPChat — один раз, когда серверы MCP получили доступ из чата: HTTP-
// серверы, заведённые человеком, открываются чату (это то, чего человек
// ждёт от подключённого сервера), сервер жителя машины — нет (у него
// инструменты кода, и в чате они ни к чему). Описание и слова-приметы
// человек пишет сам на экране сервера. Метка не даёт повторить: выключенное человеком потом не включается само.
func (s *Store) seedMCPChat() error {
	if s.Setting(mcpChatSeedKey, "") != "" {
		return nil
	}
	if _, err := s.db.Exec(`UPDATE mcp_servers SET chat = 1 WHERE agent = ''`); err != nil {
		return fmt.Errorf("серверы MCP в чате: %w", err)
	}
	return s.SetSetting(mcpChatSeedKey, "1")
}

func listJSON(v []string) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func jsonList(raw string) []string {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) MCPServers() ([]*MCPServer, error) {
	rows, err := s.db.Query(`SELECT name, url, secret, header, prefix, enabled, state, note, tools, read_, never, agent, can, checked, created,
		login_scope, login, chat, about, keywords, class, tool_info, tool_hints, rules, rule_default FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MCPServer
	for rows.Next() {
		m := &MCPServer{}
		var on, chat int
		var tools, read, never, login, info, hints, rules string
		if err := rows.Scan(&m.Name, &m.URL, &m.Secret, &m.Header, &m.Prefix, &on, &m.State, &m.Note,
			&tools, &read, &never, &m.Agent, &m.Can, &m.Checked, &m.Created, &m.LoginScope, &login,
			&chat, &m.About, &m.Keywords, &m.Class, &info, &hints, &rules, &m.RuleDefault); err != nil {
			return nil, err
		}
		m.RulesSet = rules != ""
		m.Rules = map[string]string{}
		if rules != "" {
			_ = json.Unmarshal([]byte(rules), &m.Rules)
			if m.Rules == nil {
				m.Rules = map[string]string{}
			}
		}
		if !ValidRuleKind(m.RuleDefault) {
			m.RuleDefault = RuleDeny // не подошло ни одно правило — запрещено
		}
		m.Chat = chat == 1
		if m.Class == "" {
			m.Class = ClassPlain
		}
		if info != "" && info != "[]" {
			_ = json.Unmarshal([]byte(info), &m.ToolInfo)
		}
		if hints != "" && hints != "{}" {
			_ = json.Unmarshal([]byte(hints), &m.ToolHints)
		}
		if login != "" {
			var l MCPLogin
			if json.Unmarshal([]byte(login), &l) == nil {
				m.Login = &l
			}
		}
		m.Enabled = on == 1
		m.Tools, m.LegacyRead, m.LegacyNever = jsonList(tools), jsonList(read), jsonList(never)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MCPServer(name string) (*MCPServer, bool) {
	list, err := s.MCPServers()
	if err != nil {
		return nil, false
	}
	for _, m := range list {
		if m.Name == name {
			return m, true
		}
	}
	return nil, false
}

func (s *Store) SaveMCPServer(m *MCPServer) error {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return fmt.Errorf("нужно имя сервера")
	}
	if strings.TrimSpace(m.URL) == "" && strings.TrimSpace(m.Agent) == "" {
		return fmt.Errorf("нужен адрес сервера или имя агента")
	}
	if m.Created == "" {
		m.Created = time.Now().Format(time.RFC3339)
	}
	on := 0
	if m.Enabled {
		on = 1
	}
	chat := 0
	if m.Chat {
		chat = 1
	}
	class := strings.TrimSpace(m.Class)
	switch class {
	case ClassPlain, ClassSensitive, ClassSecret:
	default:
		class = ClassPlain
	}
	info := "[]"
	if len(m.ToolInfo) > 0 {
		if raw, err := json.Marshal(m.ToolInfo); err == nil {
			info = string(raw)
		}
	}
	// rules '{}' — у нового сервера правила заведены и пусты: все его
	// инструменты «новые» и запрещены, пока человек не решит. Пустая строка
	// значит «ещё не переехали из кругов» и бывает только у строки, которую
	// принесли прежние круги (старая копия, проверка переезда): у неё есть
	// образцы в LegacyRead или LegacyNever, а правил нет
	rulesInit := "{}"
	if len(m.LegacyRead)+len(m.LegacyNever) > 0 && len(m.Rules) == 0 && !m.RulesSet {
		rulesInit = ""
	}
	_, err := s.db.Exec(`INSERT INTO mcp_servers (name, url, secret, header, prefix, enabled, state, note, tools, read_, never, agent, can, checked, created, login_scope,
			chat, about, keywords, class, tool_info, rules)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET url = excluded.url, secret = excluded.secret, header = excluded.header,
			prefix = excluded.prefix, enabled = excluded.enabled, state = excluded.state, note = excluded.note,
			tools = excluded.tools, read_ = excluded.read_, never = excluded.never,
			agent = excluded.agent, can = excluded.can, checked = excluded.checked,
			login_scope = excluded.login_scope, chat = excluded.chat, about = excluded.about,
			keywords = excluded.keywords, class = excluded.class, tool_info = excluded.tool_info`,
		m.Name, strings.TrimSpace(m.URL), m.Secret, m.Header, m.Prefix, on, m.State, m.Note,
		listJSON(m.Tools), listJSON(m.LegacyRead), listJSON(m.LegacyNever), strings.TrimSpace(m.Agent), strings.TrimSpace(m.Can),
		m.Checked, m.Created, strings.Join(strings.Fields(m.LoginScope), " "),
		chat, strings.TrimSpace(m.About), strings.TrimSpace(m.Keywords), class, info, rulesInit)
	if err == nil {
		s.touch(ScreenIntegrations)
	}
	return err
}

// SetMCPRules — записать правила по инструментам и ответ для нового
// инструмента. Единственный писатель этих колонок: см. MCPServer.Rules.
// Правило с ответом не из трёх отклоняется целиком — молча принять
// половину списка значило бы разрешить то, что человек не решал.
func (s *Store) SetMCPRules(name string, rules map[string]string, def string) error {
	if rules == nil {
		rules = map[string]string{}
	}
	for tool, answer := range rules {
		if !ValidRuleKind(answer) {
			return fmt.Errorf("инструмент %q: ответ %q не из трёх (разрешить, спросить, запретить)", tool, answer)
		}
	}
	if !ValidRuleKind(def) {
		return fmt.Errorf("ответ для нового инструмента: разрешить, спросить или запретить")
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	before, _ := s.MCPServer(name)
	res, err := s.db.Exec(`UPDATE mcp_servers SET rules = ?, rule_default = ? WHERE name = ?`, string(raw), def, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("сервера MCP %q нет", name)
	}
	// первая запись правил — переезд из кругов: это не решение человека, и
	// полсотни строк «не решён → разрешить» журнал настроек только засорили бы
	if before != nil && before.RulesSet {
		s.logMCPRuleChanges(name, before.Rules, before.RuleDefault, rules, def)
	}
	s.touch(ScreenIntegrations)
	return nil
}

// SetMCPLogin — записать сведения о входе (nil — вход снят). Единственный
// писатель этой колонки: см. MCPServer.Login.
func (s *Store) SetMCPLogin(name string, l *MCPLogin) error {
	raw := ""
	if l != nil {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	_, err := s.db.Exec(`UPDATE mcp_servers SET login = ? WHERE name = ?`, raw, name)
	if err == nil {
		s.touch(ScreenIntegrations)
	}
	return err
}

// SetMCPState — сменить только состояние и примечание строки. Вход и выход
// живут в другом ритме, чем проверка связи, и переписывать из-за них всю
// строку (со списком инструментов, кругами и тумблером) значило бы затирать
// то, что успело поменяться рядом.
func (s *Store) SetMCPState(name, state, note string) error {
	_, err := s.db.Exec(`UPDATE mcp_servers SET state = ?, note = ?, checked = ? WHERE name = ?`,
		state, note, time.Now().Format(time.RFC3339), name)
	if err == nil {
		s.touch(ScreenIntegrations)
	}
	return err
}

// mcpHintMu — подсказки инструментов правятся чтением и записью одного JSON:
// два отказа подряд от разных чатов не должны затирать друг друга.
var mcpHintMu sync.Mutex

func (s *Store) mcpToolHints(server string) (map[string]MCPToolHint, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT tool_hints FROM mcp_servers WHERE name = ?`, server).Scan(&raw); err != nil {
		return nil, err
	}
	out := map[string]MCPToolHint{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out, nil
}

func (s *Store) putMCPToolHints(server string, hints map[string]MCPToolHint) error {
	raw := "{}"
	if len(hints) > 0 {
		b, err := json.Marshal(hints)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	_, err := s.db.Exec(`UPDATE mcp_servers SET tool_hints = ? WHERE name = ?`, raw, server)
	if err == nil {
		s.touch(ScreenIntegrations)
	}
	return err
}

// SetMCPToolHint — запомнить, что сервер сказал про формат аргументов
// инструмента. Новая суть заменяет прежнюю и обновляет время: срок считается
// от последнего такого отказа.
func (s *Store) SetMCPToolHint(server, tool, gist string, at time.Time) error {
	tool, gist = strings.TrimSpace(tool), strings.TrimSpace(gist)
	if tool == "" || gist == "" {
		return nil
	}
	mcpHintMu.Lock()
	defer mcpHintMu.Unlock()
	hints, err := s.mcpToolHints(server)
	if err != nil {
		return err
	}
	hints[tool] = MCPToolHint{Gist: gist, At: at.Format(time.RFC3339)}
	return s.putMCPToolHints(server, hints)
}

// ForgetMCPToolHints — забыть подсказку одного инструмента, а с пустым
// именем — все подсказки сервера. Возвращает, сколько забыто.
func (s *Store) ForgetMCPToolHints(server, tool string) (int, error) {
	mcpHintMu.Lock()
	defer mcpHintMu.Unlock()
	hints, err := s.mcpToolHints(server)
	if err != nil {
		return 0, err
	}
	n := len(hints)
	if tool = strings.TrimSpace(tool); tool != "" {
		if _, ok := hints[tool]; !ok {
			return 0, nil
		}
		delete(hints, tool)
		n = 1
	} else {
		hints = nil
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.putMCPToolHints(server, hints)
}

// SaveMCPClient — запомнить регистрацию клиента у сервера входа.
func (s *Store) SaveMCPClient(c MCPClientReg) error {
	if c.Created == "" {
		c.Created = time.Now().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`INSERT INTO mcp_clients (server, redirect, issuer, client, created) VALUES (?,?,?,?,?)
		ON CONFLICT(server, redirect, issuer) DO UPDATE SET client = excluded.client, created = excluded.created`,
		c.Server, c.Redirect, c.Issuer, c.ClientID, c.Created)
	return err
}

// MCPClient — client_id, выданный этому серверу входа для этого адреса возврата.
func (s *Store) MCPClient(server, redirect, issuer string) (string, bool) {
	var id string
	err := s.db.QueryRow(`SELECT client FROM mcp_clients WHERE server = ? AND redirect = ? AND issuer = ?`,
		server, redirect, issuer).Scan(&id)
	return id, err == nil && id != ""
}

// DeleteMCPClients — забыть регистрации сервера (выход и удаление сервера).
func (s *Store) DeleteMCPClients(server string) error {
	_, err := s.db.Exec(`DELETE FROM mcp_clients WHERE server = ?`, server)
	return err
}

func (s *Store) DeleteMCPServer(name string) error {
	_, err := s.db.Exec(`DELETE FROM mcp_servers WHERE name = ?`, name)
	if err == nil {
		s.touch(ScreenIntegrations)
	}
	return err
}

// MCPUse — одна строка журнала вызовов.
type MCPUse struct {
	ID     int64
	Server string
	Tool   string
	Who    string
	Circle string
	OK     bool
	Args   string
	Result string
	At     string
}

func (s *Store) LogMCP(u MCPUse) {
	ok := 0
	if u.OK {
		ok = 1
	}
	_, _ = s.db.Exec(`INSERT INTO mcp_log (server, tool, who, circle, ok, args, result, at) VALUES (?,?,?,?,?,?,?,?)`,
		u.Server, u.Tool, u.Who, u.Circle, ok, clipText(u.Args, 500), clipText(u.Result, 500),
		time.Now().Format(time.RFC3339))
}

// Отборы журнала вызовов: чипы вкладки «Журнал» сервера MCP. Слова отбора —
// договор между экраном и хранилищем, поэтому лежат здесь, а не на экране.
const (
	MCPLogAll    = ""       // всё подряд
	MCPLogDone   = "done"   // выполнены: вызов дошёл до сервера и тот ответил
	MCPLogAsked  = "asked"  // шли через кнопку: ждали человека, выполнены им или отклонены
	MCPLogDenied = "denied" // отказы: правило «Запретить» (и «новый инструмент») и отклонённые человеком
	MCPLogError  = "error"  // ошибки: сервер, сеть или вход не дали выполнить разрешённое
)

// Как журнал называет вызов, который лёг в очередь на кнопку и который человек
// отклонил. Строки пишет движок (`RequestMCPFrom`, `ApproveMCP`), а отбор по
// ним читает хранилище — поэтому слова стоят в одном месте.
const (
	MCPWaitingResult  = "ждёт подтверждения человека"
	MCPRejectedResult = "человек отклонил"
)

// Чем кончился вызов, как его видит журнал.
const (
	MCPUseDone     = "done"     // выполнен
	MCPUseWaiting  = "waiting"  // лёг в очередь и ждал кнопки
	MCPUseRejected = "rejected" // человек отклонил
	MCPUseDenied   = "denied"   // правило не пустило
	MCPUseError    = "error"    // сервер или вход не дали выполнить
)

// State — чем кончился вызов. Те же слова, что и у отборов: чип «Ошибки»
// отбирает ровно то, что строка называет ошибкой.
func (u MCPUse) State() string {
	deny := u.Circle == "deny" || u.Circle == "never"
	switch {
	case u.OK:
		return MCPUseDone
	case deny:
		return MCPUseDenied
	case u.Result == MCPWaitingResult:
		return MCPUseWaiting
	case u.Result == MCPRejectedResult:
		return MCPUseRejected
	}
	return MCPUseError
}

// mcpLogWhere — условие отбора журнала по серверу и отбору. Прежние слова
// кругов (read, confirm, never) стоят рядом с нынешними: старые строки журнала
// их так и хранят.
func mcpLogWhere(server, filter string) (string, []any) {
	var conds []string
	var args []any
	if server != "" {
		conds = append(conds, `server = ?`)
		args = append(args, server)
	}
	switch filter {
	case MCPLogDone:
		conds = append(conds, `ok = 1`)
	case MCPLogAsked:
		conds = append(conds, `circle IN ('ask', 'confirm')`)
	case MCPLogDenied:
		conds = append(conds, `ok = 0 AND (circle IN ('deny', 'never') OR result = ?)`)
		args = append(args, MCPRejectedResult)
	case MCPLogError:
		conds = append(conds, `ok = 0 AND circle NOT IN ('deny', 'never') AND result NOT IN (?, ?)`)
		args = append(args, MCPWaitingResult, MCPRejectedResult)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(conds, ` AND `), args
}

func (s *Store) MCPLog(server string, limit int) ([]MCPUse, error) {
	return s.MCPLogFiltered(server, MCPLogAll, limit)
}

// MCPLogFiltered — журнал вызовов сервера (пустое имя — всех серверов) с
// отбором; свежие первыми.
func (s *Store) MCPLogFiltered(server, filter string, limit int) ([]MCPUse, error) {
	if limit <= 0 {
		limit = 50
	}
	where, args := mcpLogWhere(server, filter)
	q := `SELECT id, server, tool, who, circle, ok, args, result, at FROM mcp_log` + where + ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPUse
	for rows.Next() {
		var u MCPUse
		var ok int
		if err := rows.Scan(&u.ID, &u.Server, &u.Tool, &u.Who, &u.Circle, &ok, &u.Args, &u.Result, &u.At); err != nil {
			return nil, err
		}
		u.OK = ok == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountMCPLog — сколько записей журнала подходит под отбор: число у чипа.
func (s *Store) CountMCPLog(server, filter string) int {
	where, args := mcpLogWhere(server, filter)
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM mcp_log`+where, args...).Scan(&n)
	return n
}

func (s *Store) CountMCPCalls(server, since string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM mcp_log WHERE server = ? AND at >= ?`, server, since).Scan(&n)
	return n
}

// ---- Вызовы, ждущие подтверждения ----

type MCPPending struct {
	ID      int64
	Server  string
	Tool    string
	Args    string
	Who     string
	ChatID  int64
	State   string // waiting | done | rejected
	Created string
}

func (s *Store) migrateMCPPending() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS mcp_pending (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		server  TEXT NOT NULL,
		tool    TEXT NOT NULL,
		args    TEXT NOT NULL DEFAULT '{}',
		who     TEXT NOT NULL DEFAULT '',
		chat    INTEGER NOT NULL DEFAULT 0,
		state   TEXT NOT NULL DEFAULT 'waiting',
		created TEXT NOT NULL
	)`)
	return err
}

func (s *Store) AddMCPPending(p *MCPPending) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO mcp_pending (server, tool, args, who, chat, state, created)
		VALUES (?,?,?,?,?,'waiting',?)`,
		p.Server, p.Tool, p.Args, p.Who, p.ChatID, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) MCPPendingList() ([]*MCPPending, error) {
	rows, err := s.db.Query(`SELECT id, server, tool, args, who, chat, state, created
		FROM mcp_pending WHERE state = 'waiting' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MCPPending
	for rows.Next() {
		p := &MCPPending{}
		if err := rows.Scan(&p.ID, &p.Server, &p.Tool, &p.Args, &p.Who, &p.ChatID, &p.State, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) MCPPendingByID(id int64) (*MCPPending, bool) {
	p := &MCPPending{}
	err := s.db.QueryRow(`SELECT id, server, tool, args, who, chat, state, created FROM mcp_pending WHERE id = ?`, id).
		Scan(&p.ID, &p.Server, &p.Tool, &p.Args, &p.Who, &p.ChatID, &p.State, &p.Created)
	if err != nil {
		return nil, false
	}
	return p, true
}

func (s *Store) DecideMCPPending(id int64, state string) error {
	_, err := s.db.Exec(`UPDATE mcp_pending SET state = ? WHERE id = ?`, state, id)
	return err
}
