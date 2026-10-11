package store

import (
	"fmt"
	"strings"
	"time"
)

// Skill — инструкция Сёме на русском: что проверять, как читать, как
// отчитываться. Живёт в базе, правится с экрана и по просьбе из чата,
// подмешивается модели по словам-приметам, модулям отдаётся по имени.
type Skill struct {
	Name     string // идентификатор: linux-admin, mikrotik
	Title    string
	Group    string // блок на вкладке: «Администрирование»
	Keywords string // слова-приметы через запятую
	When     string // одна строка описи: когда применять
	Text     string
	Enabled  bool
	Source   string // builtin | own | chat
	Hash     string // хэш встроенного текста, с которым скилл пришёл из сборки
	EditedBy string // кто правил последним: «человек», «чат «Серверы»»; пусто — не правили
	EditedAt string
	Created  string
	LastUsed string
	Uses     int
}

const (
	SkillBuiltin = "builtin"
	SkillOwn     = "own"
	SkillChat    = "chat"
)

func (s *Store) migrateSkills() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS skills (
		name       TEXT PRIMARY KEY,
		title      TEXT NOT NULL,
		grp        TEXT NOT NULL DEFAULT '',
		keywords   TEXT NOT NULL DEFAULT '',
		when_      TEXT NOT NULL DEFAULT '',
		text       TEXT NOT NULL,
		enabled    INTEGER NOT NULL DEFAULT 1,
		source     TEXT NOT NULL DEFAULT 'own',
		hash       TEXT NOT NULL DEFAULT '',
		edited_by  TEXT NOT NULL DEFAULT '',
		edited_at  TEXT NOT NULL DEFAULT '',
		created    TEXT NOT NULL,
		last_used  TEXT NOT NULL DEFAULT '',
		uses       INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return fmt.Errorf("скиллы: %w", err)
	}
	return nil
}

const skillCols = `name, title, grp, keywords, when_, text, enabled, source, hash, edited_by, edited_at, created, last_used, uses`

func scanSkill(r rowScanner) (*Skill, error) {
	k := &Skill{}
	var enabled int
	err := r.Scan(&k.Name, &k.Title, &k.Group, &k.Keywords, &k.When, &k.Text, &enabled, &k.Source, &k.Hash,
		&k.EditedBy, &k.EditedAt, &k.Created, &k.LastUsed, &k.Uses)
	k.Enabled = enabled != 0
	return k, err
}

func (s *Store) Skills() ([]*Skill, error) {
	rows, err := s.db.Query(`SELECT ` + skillCols + ` FROM skills ORDER BY grp, title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Skill
	for rows.Next() {
		k, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) Skill(name string) (*Skill, bool) {
	k, err := scanSkill(s.db.QueryRow(`SELECT `+skillCols+` FROM skills WHERE name = ?`, name))
	return k, err == nil
}

// SaveSkill пишет скилл целиком (новый или обновлённый).
func (s *Store) SaveSkill(k *Skill) error {
	k.Name = strings.TrimSpace(k.Name)
	if k.Name == "" || strings.TrimSpace(k.Title) == "" || strings.TrimSpace(k.Text) == "" {
		return fmt.Errorf("у скилла нужны имя, название и текст")
	}
	if k.Created == "" {
		k.Created = time.Now().Format(time.RFC3339)
	}
	if k.Source == "" {
		k.Source = SkillOwn
	}
	enabled := 0
	if k.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`INSERT INTO skills (`+skillCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET title = excluded.title, grp = excluded.grp, keywords = excluded.keywords,
		when_ = excluded.when_, text = excluded.text, enabled = excluded.enabled, source = excluded.source,
		hash = excluded.hash, edited_by = excluded.edited_by, edited_at = excluded.edited_at`,
		k.Name, k.Title, k.Group, k.Keywords, k.When, k.Text, enabled, k.Source, k.Hash, k.EditedBy, k.EditedAt, k.Created, k.LastUsed, k.Uses)
	return err
}

func (s *Store) SetSkillEnabled(name string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE skills SET enabled = ? WHERE name = ?`, v, name)
	return err
}

func (s *Store) DeleteSkill(name string) error {
	_, err := s.db.Exec(`DELETE FROM skills WHERE name = ?`, name)
	return err
}

// SkillUsed отмечает, что скилл подмешали модели.
func (s *Store) SkillUsed(name string) {
	_, _ = s.db.Exec(`UPDATE skills SET last_used = ?, uses = uses + 1 WHERE name = ?`, time.Now().Format(time.RFC3339), name)
}
