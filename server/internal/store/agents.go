package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Справочник агентов: кто может звонить Сёме, чем он себя доказывает и
// что ему разрешено. Пропуск — сертификат, выданный самой Сёмой, а не
// общий пароль: пароль нельзя отозвать у одного, не сменив всем.

type Agent struct {
	Name string // как называется: «мак», «рабочая»
	// Role — роль агента: житель машины или шлюз. Закрепляется при
	// выдаче одноразового кода и не меняется у заведённого агента: роль
	// решает, какие способности ему вообще положены, и позволить машине
	// назваться другой ролью значило бы отдать ей это решение. Пусто —
	// удостоверение выдано до появления ролей; такое читается как «житель»
	// (он им и был), а не как «роли нет».
	Role        string
	Fingerprint string   // отпечаток его сертификата
	Cert        string   // выданный сертификат (открытая часть)
	Can         []string // способности, о которых он заявил
	Allowed     []string // что человек разрешил
	State       string   // ok | revoked | fp_changed
	NewFP       string
	NewCert     string // сертификат со сменившимся отпечатком: ждёт решения   // отпечаток, который увидели вместо принятого
	Note        string
	Seen        string // когда был на связи
	Created     string
}

// Invite — одноразовое приглашение: код живёт минуты и сгорает. Роль
// человек выбирает здесь, в момент выдачи кода: она уезжает с кодом и
// закрепляется за агентом при входе.
type Invite struct {
	Code    string
	Name    string
	Role    string
	Until   string
	Used    string
	Created string
}

const (
	AgentOK      = "ok"
	AgentRevoked = "revoked"
	AgentFPBad   = "fp_changed"
)

func (s *Store) migrateAgents() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS agents (
			name        TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL DEFAULT '',
			cert        TEXT NOT NULL DEFAULT '',
			can         TEXT NOT NULL DEFAULT '[]',
			allowed     TEXT NOT NULL DEFAULT '[]',
			state       TEXT NOT NULL DEFAULT 'ok',
			new_fp      TEXT NOT NULL DEFAULT '',
			note        TEXT NOT NULL DEFAULT '',
			seen        TEXT NOT NULL DEFAULT '',
			created     TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS agent_invites (
			code    TEXT PRIMARY KEY,
			name    TEXT NOT NULL,
			until   TEXT NOT NULL,
			used    TEXT NOT NULL DEFAULT '',
			created TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS agent_savings (
			agent      TEXT NOT NULL,
			at         TEXT NOT NULL,
			calls      INTEGER NOT NULL DEFAULT 0,
			card_lines INTEGER NOT NULL DEFAULT 0,
			saved      INTEGER NOT NULL DEFAULT 0,
			tokens     INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_savings ON agent_savings(agent, at)`,
		`ALTER TABLE agents ADD COLUMN new_cert TEXT NOT NULL DEFAULT ''`,
		// роль: пустая у всех, кто вошёл до её появления, — они жители
		`ALTER TABLE agents ADD COLUMN role TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agent_invites ADD COLUMN role TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS agent_log (
			id     INTEGER PRIMARY KEY AUTOINCREMENT,
			agent  TEXT NOT NULL,
			what   TEXT NOT NULL,
			detail TEXT NOT NULL DEFAULT '',
			ok     INTEGER NOT NULL DEFAULT 1,
			at     TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_log_at ON agent_log(at)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			// ALTER на уже добавленной колонке — не беда: миграция идёт
			// на каждом старте
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("агенты: %w", err)
		}
	}
	return nil
}

func (s *Store) Agents() ([]*Agent, error) {
	rows, err := s.db.Query(`SELECT name, role, fingerprint, cert, can, allowed, state, new_fp, new_cert, note, seen, created
		FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Agent
	for rows.Next() {
		a := &Agent{}
		var can, allowed string
		if err := rows.Scan(&a.Name, &a.Role, &a.Fingerprint, &a.Cert, &can, &allowed, &a.State, &a.NewFP, &a.NewCert,
			&a.Note, &a.Seen, &a.Created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(can), &a.Can)
		_ = json.Unmarshal([]byte(allowed), &a.Allowed)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Agent(name string) (*Agent, bool) {
	list, err := s.Agents()
	if err != nil {
		return nil, false
	}
	for _, a := range list {
		if a.Name == name {
			return a, true
		}
	}
	return nil, false
}

// AgentByFingerprint — кто это звонит: пропуск проверяется по отпечатку,
// а имя из сертификата только подсказка.
func (s *Store) AgentByFingerprint(fp string) (*Agent, bool) {
	list, err := s.Agents()
	if err != nil {
		return nil, false
	}
	for _, a := range list {
		if a.Fingerprint == fp && fp != "" {
			return a, true
		}
	}
	return nil, false
}

func (s *Store) SaveAgent(a *Agent) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return fmt.Errorf("у агента должно быть имя")
	}
	if a.Created == "" {
		a.Created = time.Now().Format(time.RFC3339)
	}
	if a.State == "" {
		a.State = AgentOK
	}
	can, _ := json.Marshal(a.Can)
	allowed, _ := json.Marshal(a.Allowed)
	_, err := s.db.Exec(`INSERT INTO agents (name, role, fingerprint, cert, can, allowed, state, new_fp, new_cert, note, seen, created)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET role = excluded.role, fingerprint = excluded.fingerprint, cert = excluded.cert,
			can = excluded.can, allowed = excluded.allowed, state = excluded.state, new_fp = excluded.new_fp,
			new_cert = excluded.new_cert, note = excluded.note, seen = excluded.seen`,
		a.Name, a.Role, a.Fingerprint, a.Cert, string(can), string(allowed), a.State, a.NewFP, a.NewCert,
		a.Note, a.Seen, a.Created)
	return err
}

func (s *Store) TouchAgent(name string) {
	_, _ = s.db.Exec(`UPDATE agents SET seen = ? WHERE name = ?`, time.Now().Format(time.RFC3339), name)
}

func (s *Store) DeleteAgent(name string) error {
	_, err := s.db.Exec(`DELETE FROM agents WHERE name = ?`, name)
	return err
}

// ---- Приглашения ----

func (s *Store) AddInvite(code, name, role string, until time.Time) error {
	_, err := s.db.Exec(`INSERT INTO agent_invites (code, name, role, until, created) VALUES (?,?,?,?,?)`,
		code, strings.TrimSpace(name), strings.TrimSpace(role), until.Format(time.RFC3339),
		time.Now().Format(time.RFC3339))
	return err
}

// UseInvite — сжечь код. Второе значение false: кода нет, он просрочен или
// уже использован. Сжигание и проверка идут одним запросом, чтобы два
// одновременных агента не прошли по одному коду.
func (s *Store) UseInvite(code string) (*Invite, bool) {
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`UPDATE agent_invites SET used = ? WHERE code = ? AND used = '' AND until >= ?`,
		now, code, now)
	if err != nil {
		return nil, false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, false
	}
	inv := &Invite{}
	if err := s.db.QueryRow(`SELECT code, name, role, until, used, created FROM agent_invites WHERE code = ?`, code).
		Scan(&inv.Code, &inv.Name, &inv.Role, &inv.Until, &inv.Used, &inv.Created); err != nil {
		return nil, false
	}
	return inv, true
}

func (s *Store) Invites() ([]*Invite, error) {
	// только живые: просроченный код в списке выглядит рабочим, человек
	// копирует его и потом гадает, почему агент не входит
	rows, err := s.db.Query(`SELECT code, name, role, until, used, created FROM agent_invites
		WHERE used = '' AND until >= ? ORDER BY created DESC LIMIT 20`,
		time.Now().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Invite
	for rows.Next() {
		i := &Invite{}
		if err := rows.Scan(&i.Code, &i.Name, &i.Role, &i.Until, &i.Used, &i.Created); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) DropInvite(code string) error {
	_, err := s.db.Exec(`DELETE FROM agent_invites WHERE code = ?`, code)
	return err
}

// Ref — по чему приглашение называют на экране: короткий отпечаток кода. Сам
// код показывается один раз, при выдаче; дальше экран знает приглашение по
// этому слову, и «Отменить» не носит код в разметке.
func (i *Invite) Ref() string {
	sum := sha256.Sum256([]byte(i.Code))
	return hex.EncodeToString(sum[:])[:12]
}

// DropInviteRef отменяет приглашение по его отпечатку (Invite.Ref) или, как
// прежде, по самому коду. Возвращает, было ли что отменять.
func (s *Store) DropInviteRef(ref string) (bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false, nil
	}
	list, err := s.Invites()
	if err != nil {
		return false, err
	}
	for _, i := range list {
		if i.Ref() == ref || i.Code == ref {
			return true, s.DropInvite(i.Code)
		}
	}
	return false, nil
}

// ---- Журнал ----

type AgentUse struct {
	Agent  string
	What   string
	Detail string
	OK     bool
	At     string
}

func (s *Store) LogAgent(u AgentUse) {
	ok := 0
	if u.OK {
		ok = 1
	}
	_, _ = s.db.Exec(`INSERT INTO agent_log (agent, what, detail, ok, at) VALUES (?,?,?,?,?)`,
		u.Agent, u.What, clipText(u.Detail, 500), ok, time.Now().Format(time.RFC3339))
}

func (s *Store) AgentLog(agent string, limit int) ([]AgentUse, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT agent, what, detail, ok, at FROM agent_log`
	args := []any{}
	if agent != "" {
		q += ` WHERE agent = ?`
		args = append(args, agent)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentUse
	for rows.Next() {
		var u AgentUse
		var ok int
		if err := rows.Scan(&u.Agent, &u.What, &u.Detail, &ok, &u.At); err != nil {
			return nil, err
		}
		u.OK = ok == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// Отборы журнала агента: чипы вкладки «Журнал». Слова отбора — договор между
// экраном и хранилищем, как у журнала команд.
const (
	AgentLogAll   = ""      // всё подряд
	AgentLogCalls = "calls" // вызовы способностей: у метода в имени точка («код.agent_status», «←память.memory_recall»)
	AgentLogFail  = "fail"  // неудачи: отказы, оборванные вызовы, чужие сертификаты
)

// agentLogWhere — условие отбора журнала по агенту (пустое имя — по всем) и
// отбору.
func agentLogWhere(agent, filter string) (string, []any) {
	var conds []string
	var args []any
	if agent != "" {
		conds = append(conds, `agent = ?`)
		args = append(args, agent)
	}
	switch filter {
	case AgentLogCalls:
		conds = append(conds, `instr(what, '.') > 0`)
	case AgentLogFail:
		conds = append(conds, `ok = 0`)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(conds, ` AND `), args
}

// AgentLogFiltered — журнал агента (пустое имя — всех) с отбором, свежие
// первыми.
func (s *Store) AgentLogFiltered(agent, filter string, limit int) ([]AgentUse, error) {
	where, args := agentLogWhere(agent, filter)
	rows, err := s.db.Query(`SELECT agent, what, detail, ok, at FROM agent_log`+where+` ORDER BY id DESC LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentUse
	for rows.Next() {
		var u AgentUse
		var ok int
		if err := rows.Scan(&u.Agent, &u.What, &u.Detail, &ok, &u.At); err != nil {
			return nil, err
		}
		u.OK = ok == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountAgentLog — сколько записей подходит под отбор: число у чипа.
func (s *Store) CountAgentLog(agent, filter string) int {
	where, args := agentLogWhere(agent, filter)
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM agent_log`+where, args...).Scan(&n)
	return n
}

// ---- Счёт сэкономленного агентом ----

// Savings — снимок счёта агента: сколько строк кода не пришлось читать.
// Агент считает у себя нарастающим итогом и переживает перезапуск; мы
// храним снимки, чтобы показать «за неделю», а не только «за всё время».
type Savings struct {
	Agent     string
	At        string
	Calls     int
	CardLines int
	Saved     int
	Tokens    int
}

// SaveSavings — снимок счёта. Один снимок в час: чаще незачем, счёт
// меняется медленно, а таблица не должна расти зря.
func (s *Store) SaveSavings(savings Savings) error {
	var prev string
	err := s.db.QueryRow(`SELECT at FROM agent_savings WHERE agent = ? ORDER BY at DESC LIMIT 1`, savings.Agent).Scan(&prev)
	if err == nil && prev != "" {
		if t, e2 := time.Parse(time.RFC3339, prev); e2 == nil && time.Since(t) < time.Hour {
			return nil
		}
	}
	_, err = s.db.Exec(`INSERT INTO agent_savings (agent, at, calls, card_lines, saved, tokens)
		VALUES (?,?,?,?,?,?)`, savings.Agent, time.Now().Format(time.RFC3339),
		savings.Calls, savings.CardLines, savings.Saved, savings.Tokens)
	return err
}

// SavingsSince — сколько сэкономлено с этого времени: разница между первым и
// последним снимком. Счёт у агента нарастающий, поэтому вычитаем, а не
// складываем: иначе одно и то же посчиталось бы много раз.
func (s *Store) SavingsSince(agent, since string) Savings {
	var first, last Savings
	scan := func(q string) Savings {
		var savings Savings
		_ = s.db.QueryRow(q, agent, since).Scan(&savings.At, &savings.Calls, &savings.CardLines, &savings.Saved, &savings.Tokens)
		return savings
	}
	first = scan(`SELECT at, calls, card_lines, saved, tokens FROM agent_savings
		WHERE agent = ? AND at >= ? ORDER BY at LIMIT 1`)
	last = scan(`SELECT at, calls, card_lines, saved, tokens FROM agent_savings
		WHERE agent = ? AND at >= ? ORDER BY at DESC LIMIT 1`)
	return Savings{
		Agent: agent, At: first.At,
		Calls: last.Calls - first.Calls, CardLines: last.CardLines - first.CardLines,
		Saved: last.Saved - first.Saved, Tokens: last.Tokens - first.Tokens,
	}
}

// AgentMemory — сколько раз через этого агента спрашивали память с
// указанного мгновения и когда это было в последний раз. Считаем по
// журналу агента, а не по журналу памяти: там потребитель — агент
// разработки, а здесь нужен житель машины, через которого он пришёл.
func (s *Store) AgentMemory(agent, since string) (int, string) {
	var n int
	var last any
	_ = s.db.QueryRow(`SELECT COUNT(*), MAX(at) FROM agent_log
		WHERE agent = ? AND what LIKE '←память.%' AND at >= ?`, agent, since).Scan(&n, &last)
	if s, ok := last.(string); ok {
		return n, s
	}
	return n, ""
}
