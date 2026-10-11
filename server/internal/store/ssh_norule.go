package store

// Команды, для которых у сервера не нашлось правила. Такая команда не
// выполняется, но и не пропадает: она ждёт решения человека — завести
// для неё правило «Спросить» или «Разрешить» либо оставить запрещённой.
// Сёма правил сам не заводит: решение — кнопка человека.
//
// Запись живёт отдельно от `ssh_pending` нарочно: в той лежит то, что уже
// разрешено спросить и ждёт «Выполнить», а здесь — то, о чём правила ещё
// нет. Смешать их значило бы дать кнопке «Выполнить» дотянуться до команды,
// которую никто не разрешал спрашивать.

import (
	"fmt"
	"strings"
	"time"
)

// Решения по команде без правила.
const (
	NoRuleAsk       = "ask"       // человек завёл правило «Спросить»
	NoRuleAllow     = "allow"     // человек завёл правило «Разрешить»
	NoRuleDismissed = "dismissed" // оставлена запрещённой
)

// SSHNoRule — команда, которой нечего сопоставить в правилах сервера.
type SSHNoRule struct {
	ID       int64
	Host     string
	Cmd      string
	Who      string
	ChatID   int64
	Why      string // почему нет правила: слова для карточки
	CanAllow bool   // можно ли предложить «Разрешить её»: не sudo, не неясный синтаксис
	Created  string
	Decided  string // пусто — ждёт решения
}

func (s *Store) migrateSSHNoRule() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS ssh_norule (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		host      TEXT NOT NULL,
		cmd       TEXT NOT NULL,
		who       TEXT NOT NULL DEFAULT '',
		chat_id   INTEGER NOT NULL DEFAULT 0,
		why       TEXT NOT NULL DEFAULT '',
		can_allow INTEGER NOT NULL DEFAULT 0,
		created   TEXT NOT NULL,
		decided   TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return fmt.Errorf("команды без правила: %w", err)
	}
	return nil
}

const noRuleCols = `id, host, cmd, who, chat_id, why, can_allow, created, decided`

func scanNoRule(r rowScanner) (*SSHNoRule, error) {
	n := &SSHNoRule{}
	var can int
	err := r.Scan(&n.ID, &n.Host, &n.Cmd, &n.Who, &n.ChatID, &n.Why, &can, &n.Created, &n.Decided)
	n.CanAllow = can == 1
	return n, err
}

// AddSSHNoRule — записать команду без правила. Та же команда на том же
// сервере, что уже ждёт решения, второй записи не заводит: возвращается
// прежняя, а чат, откуда пришёл повтор, привязывается к ней, если своего у
// неё не было.
func (s *Store) AddSSHNoRule(n *SSHNoRule) (int64, bool, error) {
	cmd := strings.TrimSpace(n.Cmd)
	if old, ok := s.SSHNoRuleWaiting(n.Host, cmd); ok {
		if old.ChatID == 0 && n.ChatID != 0 {
			_, _ = s.db.Exec(`UPDATE ssh_norule SET chat_id = ? WHERE id = ?`, n.ChatID, old.ID)
		}
		return old.ID, false, nil
	}
	at := strings.TrimSpace(n.Created)
	if at == "" {
		at = time.Now().Format(time.RFC3339)
	}
	can := 0
	if n.CanAllow {
		can = 1
	}
	res, err := s.db.Exec(`INSERT INTO ssh_norule (host, cmd, who, chat_id, why, can_allow, created) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.Host, cmd, n.Who, n.ChatID, n.Why, can, at)
	if err != nil {
		return 0, false, err
	}
	id, err := res.LastInsertId()
	return id, true, err
}

// SSHNoRuleWaiting — та же команда на том же сервере уже ждёт решения.
func (s *Store) SSHNoRuleWaiting(host, cmd string) (*SSHNoRule, bool) {
	n, err := scanNoRule(s.db.QueryRow(`SELECT `+noRuleCols+` FROM ssh_norule WHERE host = ? AND cmd = ? AND decided = '' ORDER BY id DESC LIMIT 1`,
		host, strings.TrimSpace(cmd)))
	return n, err == nil
}

// SSHNoRuleByID — запись в любом состоянии.
func (s *Store) SSHNoRuleByID(id int64) (*SSHNoRule, bool) {
	n, err := scanNoRule(s.db.QueryRow(`SELECT `+noRuleCols+` FROM ssh_norule WHERE id = ?`, id))
	return n, err == nil
}

// DecideSSHNoRule — закрыть запись решением человека.
func (s *Store) DecideSSHNoRule(id int64, decision string) error {
	_, err := s.db.Exec(`UPDATE ssh_norule SET decided = ? WHERE id = ? AND decided = ''`, decision, id)
	return err
}

// SSHNoRuleList — команды без правила, ждущие решения, новые первыми.
func (s *Store) SSHNoRuleList(limit int) []*SSHNoRule {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+noRuleCols+` FROM ssh_norule WHERE decided = '' ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*SSHNoRule
	for rows.Next() {
		if n, err := scanNoRule(rows); err == nil {
			out = append(out, n)
		}
	}
	return out
}
