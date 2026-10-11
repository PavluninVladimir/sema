package store

// Правила доступа у каждого сервера свои. Три ответа, как в Claude Code:
// «Разрешить» (allow), «Спросить» (ask), «Запретить» (deny). Не подошло
// ни одно правило — запрещено: у нового сервера правил нет, и любая
// команда на нём отказана, пока человек не возьмёт набор или не впишет
// образцы сам.
//
// Раньше правила были общими на все серверы (таблица `ssh_rules`, три круга:
// чтение, «только человек», «никогда»). Та таблица осталась в базе как
// запись: читает её только разовый переезд (`migrateHostRules`), а сборка,
// которую придётся откатить, найдёт свои круги на месте.

import (
	"fmt"
	"strings"
)

// Ответы правила.
const (
	RuleAllow = "allow"
	RuleAsk   = "ask"
	RuleDeny  = "deny"
)

// Наборы — готовые заполнения для нового сервера. Набор не привязан к
// серверу: «взять набор» копирует образцы, и дальше правила сервера живут
// своей жизнью.
const (
	RuleSetLinux    = "linux"
	RuleSetRouterOS = "routeros"
)

// RuleSets — имена наборов в порядке показа.
var RuleSets = []string{RuleSetLinux, RuleSetRouterOS}

// RuleSetTitle — набор словами человека.
func RuleSetTitle(set string) string {
	switch set {
	case RuleSetLinux:
		return "Linux"
	case RuleSetRouterOS:
		return "RouterOS"
	}
	return set
}

// ValidRuleKind — ответ правила из трёх допустимых.
func ValidRuleKind(kind string) bool {
	return kind == RuleAllow || kind == RuleAsk || kind == RuleDeny
}

// RuleList — образцы сервера или набора по трём ответам.
type RuleList struct {
	Allow []string
	Ask   []string
	Deny  []string
}

// Count — сколько образцов всего.
func (r RuleList) Count() int { return len(r.Allow) + len(r.Ask) + len(r.Deny) }

// Empty — образцов нет вовсе.
func (r RuleList) Empty() bool { return r.Count() == 0 }

// Of — образцы одного ответа.
func (r RuleList) Of(kind string) []string {
	switch kind {
	case RuleAllow:
		return r.Allow
	case RuleAsk:
		return r.Ask
	case RuleDeny:
		return r.Deny
	}
	return nil
}

func (r *RuleList) add(kind, pattern string) {
	switch kind {
	case RuleAllow:
		r.Allow = append(r.Allow, pattern)
	case RuleAsk:
		r.Ask = append(r.Ask, pattern)
	case RuleDeny:
		r.Deny = append(r.Deny, pattern)
	}
}

// HostRule — одно правило сервера со своим номером: по номеру его убирает
// кнопка «×» на экране, а образец может быть любой строкой.
type HostRule struct {
	ID      int64
	Host    string
	Kind    string
	Pattern string
}

func (s *Store) migrateHostRules() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS host_rules (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			host    TEXT NOT NULL,
			kind    TEXT NOT NULL,
			pattern TEXT NOT NULL,
			UNIQUE(host, kind, pattern)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_host_rules_host ON host_rules(host, kind, id)`,
		`CREATE TABLE IF NOT EXISTS ssh_rule_sets (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			set_name TEXT NOT NULL,
			kind     TEXT NOT NULL,
			pattern  TEXT NOT NULL,
			UNIQUE(set_name, kind, pattern)
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("правила серверов: %w", err)
		}
	}
	if err := s.migrateSSHNoRule(); err != nil {
		return err
	}
	return s.layOutSharedRules()
}

// ruleMigratedKey — метка: общие правила уже разложены по серверам.
const ruleMigratedKey = "ssh.host_rules_migrated"

// RuleSetOfPattern — в какой набор идёт образец прежнего общего списка.
// Команды RouterOS начинаются с «/», команды Linux — нет: на этом и
// держится разделение. Образец «никогда» нужен обоим.
func RuleSetOfPattern(pattern string) string {
	if strings.HasPrefix(strings.TrimSpace(pattern), "/") {
		return RuleSetRouterOS
	}
	return RuleSetLinux
}

// layOutSharedRules — разовый переезд общих правил на серверы и в наборы.
//
// Каждый существующий сервер получает образцы по своему роду: RouterOS —
// образцы, начинающиеся с «/», плюс все «никогда»; Linux — остальные плюс
// все «никогда»; род неизвестен — всё. Так поведение живых серверов в
// пределах их правил сохраняется: читающее по-прежнему без вопроса, «только
// человек» по-прежнему спрашивается, «никогда» по-прежнему запрещено. А
// вот «остальное», что раньше шло на подтверждение без всякого правила,
// теперь запрещено, пока человек не заведёт для него правило.
//
// Метка не даёт повторить: серверы, добавленные потом, начинают без
// правил, и правила, убранные человеком, не возвращаются.
func (s *Store) layOutSharedRules() error {
	if s.Setting(ruleMigratedKey, "") == "1" {
		return nil
	}
	shared := map[string][]string{} // прежний вид → образцы по порядку
	rows, err := s.db.Query(`SELECT kind, pattern FROM ssh_rules ORDER BY id`)
	if err != nil {
		return fmt.Errorf("прежние круги команд: %w", err)
	}
	for rows.Next() {
		var kind, p string
		if err := rows.Scan(&kind, &p); err != nil {
			rows.Close()
			return err
		}
		shared[kind] = append(shared[kind], p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// прежний вид круга → ответ: чтение — «Разрешить», «только человек» —
	// «Спросить», «никогда» — «Запретить»
	answer := map[string]string{"read": RuleAllow, "human": RuleAsk, "never": RuleDeny}
	sets := map[string]*RuleList{RuleSetLinux: {}, RuleSetRouterOS: {}}
	for old, kind := range answer {
		for _, p := range shared[old] {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if kind == RuleDeny {
				sets[RuleSetLinux].add(kind, p)
				sets[RuleSetRouterOS].add(kind, p)
				continue
			}
			sets[RuleSetOfPattern(p)].add(kind, p)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	put := func(table, scope, kind, p string) error {
		col := "host"
		if table == "ssh_rule_sets" {
			col = "set_name"
		}
		_, err := tx.Exec(`INSERT OR IGNORE INTO `+table+` (`+col+`, kind, pattern) VALUES (?, ?, ?)`, scope, kind, p)
		return err
	}
	// наборы — из живых образцов, а не из кода: то, что человек правил, не
	// теряется. Пустой прежний список (свежая база) наборов не создаёт:
	// их заполнит движок первичным заполнением
	for _, name := range RuleSets {
		for _, kind := range []string{RuleAllow, RuleAsk, RuleDeny} {
			for _, p := range sets[name].Of(kind) {
				if err := put("ssh_rule_sets", name, kind, p); err != nil {
					return err
				}
			}
		}
	}
	hostRows, err := tx.Query(`SELECT name, kind FROM hosts ORDER BY name`)
	if err != nil {
		return err
	}
	type hostKind struct{ name, kind string }
	var hosts []hostKind
	for hostRows.Next() {
		var h hostKind
		if err := hostRows.Scan(&h.name, &h.kind); err != nil {
			hostRows.Close()
			return err
		}
		hosts = append(hosts, h)
	}
	hostRows.Close()
	for _, h := range hosts {
		var list []string // наборы, чьи образцы сервер получает
		switch h.kind {
		case HostRouterOS:
			list = []string{RuleSetRouterOS}
		case HostLinux:
			list = []string{RuleSetLinux}
		default:
			list = RuleSets // род неизвестен — всё
		}
		for _, name := range list {
			for _, kind := range []string{RuleAllow, RuleAsk, RuleDeny} {
				for _, p := range sets[name].Of(kind) {
					if err := put("host_rules", h.name, kind, p); err != nil {
						return err
					}
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// метка — после переезда, а не внутри него: таблицу настроек пишет одна
	// дверь (SetSetting), и записать метку в одной транзакции с правилами
	// значило бы писать её мимо. Сорвись запуск между двумя шагами, переезд
	// повторится, а он безопасен: образцы, что уже лежат, не дублируются
	return s.SetSetting(ruleMigratedKey, "1")
}

// HostRuleSet — образцы сервера по трём ответам, в порядке заведения.
func (s *Store) HostRuleSet(host string) RuleList {
	var out RuleList
	rows, err := s.db.Query(`SELECT kind, pattern FROM host_rules WHERE host = ? ORDER BY id`, host)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var kind, p string
		if rows.Scan(&kind, &p) == nil {
			out.add(kind, p)
		}
	}
	return out
}

// HostRules — правила сервера со номерами, для экрана.
func (s *Store) HostRules(host string) []HostRule {
	rows, err := s.db.Query(`SELECT id, host, kind, pattern FROM host_rules WHERE host = ? ORDER BY id`, host)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []HostRule
	for rows.Next() {
		var r HostRule
		if rows.Scan(&r.ID, &r.Host, &r.Kind, &r.Pattern) == nil {
			out = append(out, r)
		}
	}
	return out
}

// HostRuleByID — одно правило по номеру.
func (s *Store) HostRuleByID(id int64) (HostRule, bool) {
	var r HostRule
	err := s.db.QueryRow(`SELECT id, host, kind, pattern FROM host_rules WHERE id = ?`, id).
		Scan(&r.ID, &r.Host, &r.Kind, &r.Pattern)
	return r, err == nil
}

// AddHostRule — добавить правило серверу. Тот же образец с тем же ответом
// второй раз не заводится.
func (s *Store) AddHostRule(host, kind, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return fmt.Errorf("пустой образец")
	}
	if !ValidRuleKind(kind) {
		return fmt.Errorf("ответ правила: разрешить, спросить или запретить")
	}
	if _, ok := s.Host(host); !ok {
		return fmt.Errorf("сервера %q нет", host)
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO host_rules (host, kind, pattern) VALUES (?, ?, ?)`, host, kind, pattern)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.logRuleChange(host, kind, "добавлено: "+pattern)
		}
		s.touch(ScreenServers)
	}
	return err
}

// DeleteHostRule — убрать правило по номеру.
func (s *Store) DeleteHostRule(id int64) error {
	r, ok := s.HostRuleByID(id)
	_, err := s.db.Exec(`DELETE FROM host_rules WHERE id = ?`, id)
	if err == nil {
		if ok {
			s.logRuleChange(r.Host, r.Kind, "убрано: "+r.Pattern)
		}
		s.touch(ScreenServers)
	}
	return err
}

// DeleteHostRulePattern — убрать правило по образцу: так убирает то, что
// просит перенос из старого «круга», без номера под рукой.
func (s *Store) DeleteHostRulePattern(host, kind, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	res, err := s.db.Exec(`DELETE FROM host_rules WHERE host = ? AND kind = ? AND pattern = ?`, host, kind, pattern)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.logRuleChange(host, kind, "убрано: "+pattern)
		}
		s.touch(ScreenServers)
	}
	return err
}

// RuleSetOf — образцы набора.
func (s *Store) RuleSetOf(set string) RuleList {
	var out RuleList
	rows, err := s.db.Query(`SELECT kind, pattern FROM ssh_rule_sets WHERE set_name = ? ORDER BY id`, set)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var kind, p string
		if rows.Scan(&kind, &p) == nil {
			out.add(kind, p)
		}
	}
	return out
}

// RuleSetsEmpty — наборов ещё нет ни одного образца.
func (s *Store) RuleSetsEmpty() bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM ssh_rule_sets`).Scan(&n)
	return n == 0
}

// AddRuleSetPattern — положить образец в набор (первичное заполнение).
func (s *Store) AddRuleSetPattern(set, kind, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || !ValidRuleKind(kind) {
		return fmt.Errorf("образец набора: нужен текст и ответ")
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO ssh_rule_sets (set_name, kind, pattern) VALUES (?, ?, ?)`, set, kind, pattern)
	return err
}

// TakeRuleSet — дописать серверу образцы набора: что уже есть, не
// повторяется, а то, что человек убрал или добавил сам, остаётся как есть.
// Возвращает, сколько образцов прибавилось.
func (s *Store) TakeRuleSet(host, set string) (int, error) {
	if _, ok := s.Host(host); !ok {
		return 0, fmt.Errorf("сервера %q нет", host)
	}
	list := s.RuleSetOf(set)
	if list.Empty() {
		return 0, fmt.Errorf("набор «%s» пуст", RuleSetTitle(set))
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, kind := range []string{RuleAllow, RuleAsk, RuleDeny} {
		for _, p := range list.Of(kind) {
			res, err := tx.Exec(`INSERT OR IGNORE INTO host_rules (host, kind, pattern) VALUES (?, ?, ?)`, host, kind, p)
			if err != nil {
				return 0, err
			}
			if c, _ := res.RowsAffected(); c > 0 {
				n++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if n > 0 {
		s.logRuleChange(host, "набор", fmt.Sprintf("взят набор «%s»: %d образцов", RuleSetTitle(set), n))
	}
	s.touch(ScreenServers)
	return n, nil
}
