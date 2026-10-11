package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Черновики скиллов и мера их пользы. Черновик живёт в базе, а не в памяти
// процесса: предложение, пропавшее после перезапуска, — та же потеря работы,
// что и потерянная задача.

type SkillDraft struct {
	ID        string
	Name      string // имя скилла: новый или существующий
	Title     string
	Group     string
	Keywords  string
	When      string
	Text      string
	IsNew     bool
	Kind      string // chat | experience | fix
	Who       string // откуда: «чат «Серверы»», «работа «обновление»»
	Why       string // почему предложен
	Diff      string
	Chat      int64
	Judge     string // мнение судьи на другой модели, если спрашивали
	State     string // open | accepted | rejected
	Print     string // отпечаток предложения: отклонённое не повторяется
	Created   string
	Decided   string
	DecidedBy string
}

const (
	DraftOpen     = "open"
	DraftAccepted = "accepted"
	DraftRejected = "rejected"

	DraftChat       = "chat"
	DraftExperience = "experience"
	DraftFix        = "fix"
)

func (s *Store) migrateLearning() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS skill_drafts (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			title      TEXT NOT NULL DEFAULT '',
			grp        TEXT NOT NULL DEFAULT '',
			keywords   TEXT NOT NULL DEFAULT '',
			when_      TEXT NOT NULL DEFAULT '',
			text       TEXT NOT NULL,
			is_new     INTEGER NOT NULL DEFAULT 0,
			kind       TEXT NOT NULL DEFAULT 'chat',
			who        TEXT NOT NULL DEFAULT '',
			why        TEXT NOT NULL DEFAULT '',
			diff       TEXT NOT NULL DEFAULT '',
			chat       INTEGER NOT NULL DEFAULT 0,
			judge      TEXT NOT NULL DEFAULT '',
			state      TEXT NOT NULL DEFAULT 'open',
			print      TEXT NOT NULL DEFAULT '',
			created    TEXT NOT NULL,
			decided    TEXT NOT NULL DEFAULT '',
			decided_by TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_drafts_state ON skill_drafts(state, created)`,
		`CREATE TABLE IF NOT EXISTS skill_score (
			name      TEXT NOT NULL,
			chat      INTEGER NOT NULL,
			at        TEXT NOT NULL,
			outcome   TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (name, chat)
		)`,
		// Предотвращённое считается отдельно от исхода работы: работа может
		// закончиться успехом и всё равно содержать момент, где скилл не дал
		// сделать глупость. Одна строка — один такой момент.
		`CREATE TABLE IF NOT EXISTS skill_saved (
			name TEXT NOT NULL,
			chat INTEGER NOT NULL,
			at   TEXT NOT NULL,
			why  TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_skill_saved ON skill_saved(name, at)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("развитие: %w", err)
		}
	}
	return nil
}

const draftCols = `id, name, title, grp, keywords, when_, text, is_new, kind, who, why, diff, chat, judge, state, print, created, decided, decided_by`

func scanDraft(r rowScanner) (*SkillDraft, error) {
	d := &SkillDraft{}
	var isNew int
	if err := r.Scan(&d.ID, &d.Name, &d.Title, &d.Group, &d.Keywords, &d.When, &d.Text, &isNew, &d.Kind,
		&d.Who, &d.Why, &d.Diff, &d.Chat, &d.Judge, &d.State, &d.Print, &d.Created, &d.Decided, &d.DecidedBy); err != nil {
		return nil, err
	}
	d.IsNew = isNew == 1
	return d, nil
}

func (s *Store) AddSkillDraft(d *SkillDraft) error {
	if d.ID == "" {
		d.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if d.State == "" {
		d.State = DraftOpen
	}
	if d.Print == "" {
		d.Print = DraftPrint(d.Name, d.Text)
	}
	d.Created = time.Now().Format(time.RFC3339)
	isNew := 0
	if d.IsNew {
		isNew = 1
	}
	_, err := s.db.Exec(`INSERT INTO skill_drafts (`+draftCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Name, d.Title, d.Group, d.Keywords, d.When, d.Text, isNew, d.Kind,
		d.Who, d.Why, d.Diff, d.Chat, d.Judge, d.State, d.Print, d.Created, d.Decided, d.DecidedBy)
	return err
}

func (s *Store) SkillDraft(id string) (*SkillDraft, bool) {
	d, err := scanDraft(s.db.QueryRow(`SELECT `+draftCols+` FROM skill_drafts WHERE id = ?`, id))
	if err != nil {
		return nil, false
	}
	return d, true
}

// SkillDrafts — черновики по состоянию; пустое состояние — все.
func (s *Store) SkillDrafts(state string, limit int) ([]*SkillDraft, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + draftCols + ` FROM skill_drafts`
	args := []any{}
	if state != "" {
		q += ` WHERE state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY created DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SkillDraft
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DecideSkillDraft(id, state, by string) error {
	res, err := s.db.Exec(`UPDATE skill_drafts SET state = ?, decided = ?, decided_by = ? WHERE id = ?`,
		state, time.Now().Format(time.RFC3339), by, id)
	if err != nil {
		return err
	}
	// решение по пропавшему черновику — не решение: молчать об этом нельзя
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("черновика %q больше нет", id)
	}
	return nil
}

// DraftRejected — отклонённое человеком не предлагается снова.
func (s *Store) DraftRejected(print string) bool {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM skill_drafts WHERE print = ? AND state = ?`, print, DraftRejected).Scan(&one)
	return err == nil
}

// OpenDraftFor — уже висящий черновик по тому же скиллу: похожее сливается в
// один, а не сыпется карточками.
func (s *Store) OpenDraftFor(name string) (*SkillDraft, bool) {
	d, err := scanDraft(s.db.QueryRow(`SELECT `+draftCols+` FROM skill_drafts
		WHERE name = ? AND state = ? ORDER BY created DESC LIMIT 1`, name, DraftOpen))
	if err != nil {
		return nil, false
	}
	return d, true
}

func (s *Store) DropSkillDraft(id string) error {
	_, err := s.db.Exec(`DELETE FROM skill_drafts WHERE id = ?`, id)
	return err
}

// CountDrafts — сколько черновиков в состоянии за период (пусто — за всё время).
func (s *Store) CountDrafts(state, since string) int {
	q := `SELECT COUNT(*) FROM skill_drafts WHERE state = ?`
	args := []any{state}
	if since != "" {
		q += ` AND created >= ?`
		args = append(args, since)
	}
	var n int
	_ = s.db.QueryRow(q, args...).Scan(&n)
	return n
}

// DraftPrint — отпечаток предложения: имя скилла и его текст без пробелов и
// регистра. Тот же смысл, набранный иначе, отпечаток не меняет.
func DraftPrint(name, text string) string {
	norm := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	sum := sha256.Sum256([]byte(strings.ToLower(name) + "\n" + norm))
	return hex.EncodeToString(sum[:16])
}

// ---- Мера пользы скилла ----

// NoteSkillUse — скилл подмешался в работу; исход дописывается позже.
func (s *Store) NoteSkillUse(name string, chat int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO skill_score (name, chat, at, outcome) VALUES (?, ?, ?, '')`,
		name, chat, time.Now().Format(time.RFC3339))
	return err
}

// CloseSkillUses — работа закончилась: всем скиллам этой работы ставится исход.
func (s *Store) CloseSkillUses(chat int64, outcome string) error {
	_, err := s.db.Exec(`UPDATE skill_score SET outcome = ? WHERE chat = ? AND outcome = ''`, outcome, chat)
	return err
}

// SkillScore — сколько раз скилл участвовал и чем эти работы кончились.
type SkillScore struct {
	Name  string
	Used  int
	Wins  int
	Fails int
}

func (s *Store) SkillScores(since string) ([]SkillScore, error) {
	rows, err := s.db.Query(`SELECT name,
			COUNT(*),
			SUM(CASE WHEN outcome = 'win' THEN 1 ELSE 0 END),
			SUM(CASE WHEN outcome = 'fail' THEN 1 ELSE 0 END)
		FROM skill_score WHERE at >= ? GROUP BY name ORDER BY COUNT(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SkillScore
	for rows.Next() {
		var sc SkillScore
		if err := rows.Scan(&sc.Name, &sc.Used, &sc.Wins, &sc.Fails); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// MarkSkillSaved — в работе предотвратили ошибку: опасную команду не
// выполнили. Отмечается всем скиллам, что были подмешаны в эту работу:
// разобраться, который именно помог, нельзя, а знать, что помогло, — можно.
func (s *Store) MarkSkillSaved(chat int64, why string) error {
	_, err := s.db.Exec(`INSERT INTO skill_saved (name, chat, at, why)
		SELECT name, chat, ?, ? FROM skill_score WHERE chat = ?`,
		time.Now().Format(time.RFC3339), why, chat)
	return err
}

// SkillSaved — сколько раз каждый скилл участвовал в предотвращённом, с
// какого времени считаем.
func (s *Store) SkillSaved(since string) map[string]int {
	out := map[string]int{}
	rows, err := s.db.Query(`SELECT name, COUNT(*) FROM skill_saved WHERE at >= ? GROUP BY name`, since)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if rows.Scan(&name, &n) == nil {
			out[name] = n
		}
	}
	return out
}
