package store

// Серверы для SSH: справочник, отпечатки хостов, ожидающие подтверждения
// команды и журнал. Правила доступа — у каждого сервера свои, см.
// host_rules.go. Ключ к серверу — имя секрета в хранилище;
// сам ключ здесь не хранится.

import (
	"fmt"
	"strings"
	"time"
)

// Уровни самостоятельности сервера.
const (
	AutonomyOff   = ""       // ручное: изменяющее ждёт человека
	AutonomyCourt = "суд"    // изменения предлагает кто-то другой, решает суд двух моделей
	AutonomyFull  = "полная" // Сёма сама находит работу в своём окне
)

type Host struct {
	Name        string
	Addr        string
	Port        int
	User        string
	Key         string // имя секрета вида ssh:…
	Fingerprint string // SHA256:… — принят при первом входе
	NewFP       string // отпечаток, который увидели вместо принятого
	Created     string
	LastCmd     string
	LastAt      string
	LastErr     string
	Kind        string // linux | routeros — угадывается по первому ответу, правится на «Серверах»
	// Самостоятельное управление тремя уровнями: «» — ручное, «суд» —
	// изменения предлагает кто-то другой, а решает суд двух моделей,
	// «полная» — Сёма сама находит работу в окне. Уровень выставляет
	// человек по каждому серверу отдельно.
	Autonomy string
	Auto     bool   // «не ручное»: считается по уровню, в базе не хранится
	Window   string // «02:00-06:00»; пусто — круглые сутки
	MaxHour  int    // потолок изменяющих команд в час; 0 — по умолчанию
	Paused   string // причина остановки: проба не сошлась, связь пропала
	AutoFrom string // когда включили самостоятельность
	// NoSudo — на этом сервере Сёме не даётся root: команда с sudo
	// отклоняется совсем, а не уходит человеку на подтверждение. Для
	// машины, где живёт сам Сёма, это включено по умолчанию.
	NoSudo bool
}

const (
	HostLinux    = "linux"
	HostRouterOS = "routeros"
)

// Решения по командам в журнале.
const (
	SSHExecuted = "executed"
	SSHWaiting  = "pending"
	SSHDenied   = "denied"   // правило «Запретить», ни одно правило не подошло или сервер не выдан
	SSHRejected = "rejected" // человек отклонил
	SSHAuto     = "auto"     // выполнено самим Сёмой по решению суда моделей
	SSHRuled    = "rule"     // человек завёл правило для команды, которой не было
	SSHError    = "error"
)

type SSHPending struct {
	ID      int64
	Host    string
	Cmd     string
	Who     string
	ChatID  int64
	Created string
}

type SSHLogEntry struct {
	ID       int64
	Host     string
	Cmd      string
	Who      string
	Decision string
	Code     int
	Millis   int64
	Size     int
	Output   string
	At       string
}

func (s *Store) migrateHosts() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS hosts (
			name        TEXT PRIMARY KEY,
			addr        TEXT NOT NULL,
			port        INTEGER NOT NULL DEFAULT 22,
			user        TEXT NOT NULL,
			key         TEXT NOT NULL,
			fingerprint TEXT NOT NULL DEFAULT '',
			new_fp      TEXT NOT NULL DEFAULT '',
			created     TEXT NOT NULL,
			last_cmd    TEXT NOT NULL DEFAULT '',
			last_at     TEXT NOT NULL DEFAULT '',
			last_err    TEXT NOT NULL DEFAULT ''
		)`,
		// прежние общие круги (read | human | never). Остались записью: их
		// читает только разовый переезд на правила серверов, а сборка, которую
		// придётся откатить, найдёт свои круги на месте
		`CREATE TABLE IF NOT EXISTS ssh_rules (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			kind    TEXT NOT NULL,
			pattern TEXT NOT NULL,
			UNIQUE(kind, pattern)
		)`,
		`CREATE TABLE IF NOT EXISTS ssh_pending (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			host    TEXT NOT NULL,
			cmd     TEXT NOT NULL,
			who     TEXT NOT NULL,
			chat_id INTEGER NOT NULL DEFAULT 0,
			created TEXT NOT NULL,
			decided TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS ssh_log (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			host     TEXT NOT NULL,
			cmd      TEXT NOT NULL,
			who      TEXT NOT NULL,
			decision TEXT NOT NULL,
			code     INTEGER NOT NULL DEFAULT 0,
			millis   INTEGER NOT NULL DEFAULT 0,
			size     INTEGER NOT NULL DEFAULT 0,
			output   TEXT NOT NULL DEFAULT '',
			at       TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ssh_log_host ON ssh_log(host, id DESC)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("серверы: %w", err)
		}
	}
	_, _ = s.db.Exec(`ALTER TABLE hosts ADD COLUMN kind TEXT NOT NULL DEFAULT ''`)
	// самостоятельное управление сервером
	_, _ = s.db.Exec(`ALTER TABLE hosts ADD COLUMN auto INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE hosts ADD COLUMN autonomy TEXT NOT NULL DEFAULT ''`)
	// прежний тумблер становится уровнем «суд»: поведение то же, что было
	_, _ = s.db.Exec(`UPDATE hosts SET autonomy = ? WHERE auto = 1 AND autonomy = ''`, AutonomyCourt)
	_, _ = s.db.Exec(`ALTER TABLE hosts ADD COLUMN window_ TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE hosts ADD COLUMN max_hour INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE hosts ADD COLUMN paused TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE hosts ADD COLUMN auto_from TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE hosts ADD COLUMN no_sudo INTEGER NOT NULL DEFAULT 0`)
	return nil
}

const hostCols = `name, addr, port, user, key, fingerprint, new_fp, created, last_cmd, last_at, last_err, kind, auto, window_, max_hour, paused, auto_from, no_sudo, autonomy`

func scanHost(r rowScanner) (*Host, error) {
	h := &Host{}
	var auto, noSudo int
	err := r.Scan(&h.Name, &h.Addr, &h.Port, &h.User, &h.Key, &h.Fingerprint, &h.NewFP, &h.Created, &h.LastCmd, &h.LastAt, &h.LastErr, &h.Kind,
		&auto, &h.Window, &h.MaxHour, &h.Paused, &h.AutoFrom, &noSudo, &h.Autonomy)
	h.NoSudo = noSudo == 1
	if h.Autonomy == "" && auto == 1 {
		h.Autonomy = AutonomyCourt // база старая, миграция ещё не прошла
	}
	h.Auto = h.Autonomy != AutonomyOff
	return h, err
}

func (s *Store) Hosts() ([]*Host, error) {
	rows, err := s.db.Query(`SELECT ` + hostCols + ` FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) Host(name string) (*Host, bool) {
	h, err := scanHost(s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE name = ?`, name))
	return h, err == nil
}

// Full — сервер отдан Сёме целиком: она сама находит работу в окне и
// может браться за то, что иначе ушло бы человеку.
func (h *Host) Full() bool { return h.Autonomy == AutonomyFull }

// AutonomyTitle — уровень словами человека.
func (h *Host) AutonomyTitle() string {
	switch h.Autonomy {
	case AutonomyCourt:
		return "под судом"
	case AutonomyFull:
		return "полная"
	}
	return "ручное"
}

func (s *Store) SaveHost(h *Host) error {
	if h.Created == "" {
		h.Created = time.Now().Format(time.RFC3339)
	}
	if h.Port <= 0 {
		h.Port = 22
	}
	auto, noSudo := 0, 0
	if h.Autonomy == "" && h.Auto {
		h.Autonomy = AutonomyCourt // старый вызов с тумблером
	}
	h.Auto = h.Autonomy != AutonomyOff
	if h.Auto {
		auto = 1
	}
	if h.NoSudo {
		noSudo = 1
	}
	_, err := s.db.Exec(`INSERT INTO hosts (`+hostCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET addr = excluded.addr, port = excluded.port, user = excluded.user, key = excluded.key,
		fingerprint = excluded.fingerprint, new_fp = excluded.new_fp, last_cmd = excluded.last_cmd, last_at = excluded.last_at, last_err = excluded.last_err,
		kind = excluded.kind, auto = excluded.auto, autonomy = excluded.autonomy, window_ = excluded.window_, max_hour = excluded.max_hour,
		paused = excluded.paused, auto_from = excluded.auto_from, no_sudo = excluded.no_sudo`,
		h.Name, h.Addr, h.Port, h.User, h.Key, h.Fingerprint, h.NewFP, h.Created, h.LastCmd, h.LastAt, h.LastErr, h.Kind,
		auto, h.Window, h.MaxHour, h.Paused, h.AutoFrom, noSudo, h.Autonomy)
	return err
}

func (s *Store) DeleteHost(name string) error {
	for _, q := range []string{`DELETE FROM hosts WHERE name = ?`, `DELETE FROM ssh_pending WHERE host = ?`,
		`DELETE FROM host_rules WHERE host = ?`, `DELETE FROM ssh_norule WHERE host = ?`} {
		if _, err := s.db.Exec(q, name); err != nil {
			return err
		}
	}
	return nil
}

// ---- ожидающие ----

// SetSSHPendingChat — привязать ожидающую команду к чату, куда уйдёт результат.
func (s *Store) SetSSHPendingChat(id, chatID int64) error {
	_, err := s.db.Exec(`UPDATE ssh_pending SET chat_id = ? WHERE id = ?`, chatID, id)
	return err
}

// AddSSHPending — команда встаёт в очередь на подтверждение. Время берём
// переданное, а пустое ставим сами: поле в записи было, а значение молча
// выбрасывалось — и записать команду задним числом было нечем.
func (s *Store) AddSSHPending(p *SSHPending) (int64, error) {
	at := strings.TrimSpace(p.Created)
	if at == "" {
		at = time.Now().Format(time.RFC3339)
	}
	res, err := s.db.Exec(`INSERT INTO ssh_pending (host, cmd, who, chat_id, created) VALUES (?, ?, ?, ?, ?)`,
		p.Host, p.Cmd, p.Who, p.ChatID, at)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SSHPendingList() ([]*SSHPending, error) {
	rows, err := s.db.Query(`SELECT id, host, cmd, who, chat_id, created FROM ssh_pending WHERE decided = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SSHPending
	for rows.Next() {
		p := &SSHPending{}
		if err := rows.Scan(&p.ID, &p.Host, &p.Cmd, &p.Who, &p.ChatID, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SSHPendingState — чем кончилась команда: пусто (ждёт), executed, rejected;
// «нет такой» — для карточек из истории, где строку уже убрали.
func (s *Store) SSHPendingState(id int64) (string, bool) {
	var decided string
	if err := s.db.QueryRow(`SELECT decided FROM ssh_pending WHERE id = ?`, id).Scan(&decided); err != nil {
		return "", false
	}
	return decided, true
}

func (s *Store) SSHPendingByID(id int64) (*SSHPending, error) {
	p := &SSHPending{}
	var decided string
	err := s.db.QueryRow(`SELECT id, host, cmd, who, chat_id, created, decided FROM ssh_pending WHERE id = ?`, id).
		Scan(&p.ID, &p.Host, &p.Cmd, &p.Who, &p.ChatID, &p.Created, &decided)
	if err != nil {
		return nil, err
	}
	if decided != "" {
		return nil, fmt.Errorf("команда уже решена: %s", decided)
	}
	return p, nil
}

func (s *Store) DecideSSHPending(id int64, decision string) error {
	_, err := s.db.Exec(`UPDATE ssh_pending SET decided = ? WHERE id = ?`, decision, id)
	return err
}

// ---- журнал ----

func (s *Store) LogSSH(e SSHLogEntry) {
	if e.At == "" {
		e.At = time.Now().Format(time.RFC3339)
	}
	_, _ = s.db.Exec(`INSERT INTO ssh_log (host, cmd, who, decision, code, millis, size, output, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Host, e.Cmd, e.Who, e.Decision, e.Code, e.Millis, e.Size, e.Output, e.At)
	_, _ = s.db.Exec(`DELETE FROM ssh_log WHERE id NOT IN (SELECT id FROM ssh_log ORDER BY id DESC LIMIT 2000)`)
	_, _ = s.db.Exec(`UPDATE hosts SET last_cmd = ?, last_at = ? WHERE name = ?`, e.Cmd, e.At, e.Host)
}

func (s *Store) SSHLog(host string, limit int) ([]SSHLogEntry, error) {
	q := `SELECT id, host, cmd, who, decision, code, millis, size, output, at FROM ssh_log`
	var args []any
	if host != "" {
		q += ` WHERE host = ?`
		args = append(args, host)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSHLogEntry
	for rows.Next() {
		var e SSHLogEntry
		if err := rows.Scan(&e.ID, &e.Host, &e.Cmd, &e.Who, &e.Decision, &e.Code, &e.Millis, &e.Size, &e.Output, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Отборы журнала команд: чипы вкладки «Журнал». Слова отбора — договор между
// экраном и хранилищем, поэтому лежат здесь, а не на экране.
const (
	SSHLogAll    = ""       // всё подряд
	SSHLogDone   = "done"   // выполнены: человеком и самим Сёмой
	SSHLogDenied = "denied" // отказы: правило «Запретить», нет правила, отклонено человеком, ошибка
	SSHLogSelf   = "self"   // выполнено самим Сёмой по решению суда
	SSHLogNight  = "night"  // ночная смена
)

// sshLogWhere — условие отбора журнала по серверу и отбору.
func sshLogWhere(host, filter string) (string, []any) {
	var conds []string
	var args []any
	if host != "" {
		conds = append(conds, `host = ?`)
		args = append(args, host)
	}
	switch filter {
	case SSHLogDone:
		conds = append(conds, `decision IN (?, ?)`)
		args = append(args, SSHExecuted, SSHAuto)
	case SSHLogDenied:
		conds = append(conds, `decision IN (?, ?, ?)`)
		args = append(args, SSHDenied, SSHRejected, SSHError)
	case SSHLogSelf:
		conds = append(conds, `decision = ?`)
		args = append(args, SSHAuto)
	case SSHLogNight:
		conds = append(conds, `who LIKE ?`)
		args = append(args, "ночная смена%")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(conds, ` AND `), args
}

// SSHLogFiltered — журнал команд сервера (пустое имя — всех серверов) с отбором;
// свежие первыми.
func (s *Store) SSHLogFiltered(host, filter string, limit int) ([]SSHLogEntry, error) {
	where, args := sshLogWhere(host, filter)
	rows, err := s.db.Query(`SELECT id, host, cmd, who, decision, code, millis, size, output, at FROM ssh_log`+
		where+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSHLogEntry
	for rows.Next() {
		var e SSHLogEntry
		if err := rows.Scan(&e.ID, &e.Host, &e.Cmd, &e.Who, &e.Decision, &e.Code, &e.Millis, &e.Size, &e.Output, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountSSHLog — сколько записей журнала подходит под отбор: число у чипа.
func (s *Store) CountSSHLog(host, filter string) int {
	where, args := sshLogWhere(host, filter)
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM ssh_log`+where, args...).Scan(&n)
	return n
}

// LastSSHLogFor — последняя запись журнала по серверу и команде: результат
// команды, выполненной из уведомления.
func (s *Store) LastSSHLogFor(host, cmd string) (*SSHLogEntry, bool) {
	list, err := s.SSHLog(host, 50)
	if err != nil {
		return nil, false
	}
	for i := range list {
		if list[i].Cmd == cmd && list[i].Decision == SSHExecuted {
			return &list[i], true
		}
	}
	return nil, false
}

func (s *Store) SSHLogEntry(id int64) (*SSHLogEntry, error) {
	var e SSHLogEntry
	err := s.db.QueryRow(`SELECT id, host, cmd, who, decision, code, millis, size, output, at FROM ssh_log WHERE id = ?`, id).
		Scan(&e.ID, &e.Host, &e.Cmd, &e.Who, &e.Decision, &e.Code, &e.Millis, &e.Size, &e.Output, &e.At)
	return &e, err
}

// CountSSHChanges — сколько изменяющих команд выполнено на сервере с
// указанного момента: потолок самостоятельной работы считается по нему.
func (s *Store) CountSSHChanges(host, since string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM ssh_log WHERE host = ? AND at >= ? AND decision = ?`,
		host, since, SSHAuto).Scan(&n)
	return n
}

// PendingForHost — команды сервера, ждущие решения человека.
func (s *Store) PendingForHost(host string, limit int) ([]*SSHPending, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, host, cmd, who, chat_id, created FROM ssh_pending
WHERE host = ? AND decided = '' ORDER BY id DESC LIMIT ?`, host, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SSHPending
	for rows.Next() {
		p := &SSHPending{}
		if err := rows.Scan(&p.ID, &p.Host, &p.Cmd, &p.Who, &p.ChatID, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
