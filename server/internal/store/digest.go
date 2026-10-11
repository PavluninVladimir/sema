package store

// Группы дайджеста «Почты» — справочник, а не константы: по каким приметам
// письмо считается счётом, документом, письмом от человека или
// уведомлением, решает человек. Первичный набор — только заполнение.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DigestRules — приметы группы. Письмо попадает в первую группу по порядку,
// у которой сошлась хоть одна примета.
type DigestRules struct {
	Senders     []string `json:"senders,omitempty"`     // подстроки адреса отправителя
	Words       []string `json:"words,omitempty"`       // слова в теме
	Receipt     bool     `json:"receipt,omitempty"`     // у письма разобран чек или счёт
	Attachments []string `json:"attachments,omitempty"` // расширения вложений
	People      bool     `json:"people,omitempty"`      // всё, что не от робота и не сошлось раньше
	Robots      bool     `json:"robots,omitempty"`      // отправитель похож на робота
}

type DigestGroup struct {
	ID        int64
	Name      string // устойчивое имя для правил модулей: receipts, documents, people, notifications
	Title     string
	Sort      int
	Collapsed bool // показывать одной строкой со счётчиком
	Rules     DigestRules
}

// seedDigestGroups — первичный набор; любую группу можно переименовать,
// удалить или дополнить своими приметами.
var seedDigestGroups = []DigestGroup{
	{Name: "receipts", Title: "Счета и чеки", Sort: 10, Rules: DigestRules{Receipt: true,
		Words: []string{"счёт", "счет", "чек", "оплат", "квитанц", "invoice", "receipt", "платёж", "платеж"}}},
	{Name: "documents", Title: "Документы", Sort: 20, Rules: DigestRules{
		Attachments: []string{"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx"}}},
	{Name: "notifications", Title: "Уведомления", Sort: 40, Collapsed: true, Rules: DigestRules{Robots: true,
		Senders: []string{"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "notification", "notify",
			"mailer", "newsletter", "bounce", "news@", "info@", "digest", "alert", "bot@", "robot", "рассылк", "support@", "hello@"}}},
	{Name: "people", Title: "От людей", Sort: 30, Rules: DigestRules{People: true}},
}

func (s *Store) migrateDigest() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS digest_groups (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		name      TEXT NOT NULL UNIQUE,
		title     TEXT NOT NULL,
		sort      INTEGER NOT NULL DEFAULT 100,
		collapsed INTEGER NOT NULL DEFAULT 0,
		rules     TEXT NOT NULL DEFAULT '{}')`); err != nil {
		return fmt.Errorf("группы дайджеста: %w", err)
	}
	if s.Setting("digest_groups_seeded", "") == "1" {
		return nil
	}
	for _, g := range seedDigestGroups {
		g := g
		if err := s.SaveDigestGroup(&g); err != nil {
			return err
		}
	}
	return s.SetSetting("digest_groups_seeded", "1")
}

func (s *Store) DigestGroups() ([]*DigestGroup, error) {
	rows, err := s.db.Query(`SELECT id, name, title, sort, collapsed, rules FROM digest_groups ORDER BY sort, title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DigestGroup
	for rows.Next() {
		g := &DigestGroup{}
		var raw string
		var collapsed int
		if err := rows.Scan(&g.ID, &g.Name, &g.Title, &g.Sort, &collapsed, &raw); err != nil {
			return nil, err
		}
		g.Collapsed = collapsed == 1
		_ = json.Unmarshal([]byte(raw), &g.Rules)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) SaveDigestGroup(g *DigestGroup) error {
	raw, err := json.Marshal(g.Rules)
	if err != nil {
		return err
	}
	if g.Name == "" {
		g.Name = slug(g.Title)
	}
	collapsed := 0
	if g.Collapsed {
		collapsed = 1
	}
	if g.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO digest_groups (name, title, sort, collapsed, rules) VALUES (?, ?, ?, ?, ?)`,
			g.Name, g.Title, g.Sort, collapsed, string(raw))
		if err != nil {
			return err
		}
		g.ID, _ = res.LastInsertId()
		return nil
	}
	_, err = s.db.Exec(`UPDATE digest_groups SET name = ?, title = ?, sort = ?, collapsed = ?, rules = ? WHERE id = ?`,
		g.Name, g.Title, g.Sort, collapsed, string(raw), g.ID)
	return err
}

func (s *Store) DeleteDigestGroup(id int64) error {
	_, err := s.db.Exec(`DELETE FROM digest_groups WHERE id = ?`, id)
	return err
}

// slug — устойчивое имя группы из названия: латиница и цифры, остальное — «_».
func slug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 'а' && r <= 'я', r == 'ё':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// MessagesBetween — письма ящика (или всех) за период с любым из статусов,
// от новых к старым. о — отсев этой сборки (снимок на устройстве); nil — не
// исключать никого, как раньше.
func (s *Store) MessagesBetween(account string, ex *Exclusion, since, until time.Time, statuses ...string) ([]*Message, error) {
	q := selectMsg + ` WHERE date >= ? AND date < ?`
	args := []any{fmtTime(since), fmtTime(until)}
	if account != "" {
		if ex.SkipsAccount(account) {
			return nil, nil
		}
		q += ` AND account = ?`
		args = append(args, account)
	}
	// условие ставится и для одного ящика: имя ящика отвечает только на
	// вопрос про ящик, а класс «секретное» вырезается в любом случае
	if clause, cargs := ex.Clause(); clause != "" {
		q += clause
		args = append(args, cargs...)
	}
	if len(statuses) > 0 {
		q += ` AND status IN (?` + strings.Repeat(",?", len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	rows, err := s.db.Query(q+` ORDER BY date DESC LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountBetween — сколько писем ящика за период имеют один из статусов.
// о — отсев этой сборки; nil — не исключать никого.
func (s *Store) CountBetween(account string, ex *Exclusion, since, until time.Time, statuses ...string) int {
	q := `SELECT COUNT(*) FROM messages WHERE date >= ? AND date < ?`
	args := []any{fmtTime(since), fmtTime(until)}
	if account != "" {
		if ex.SkipsAccount(account) {
			return 0
		}
		q += ` AND account = ?`
		args = append(args, account)
	}
	if clause, cargs := ex.Clause(); clause != "" {
		q += clause
		args = append(args, cargs...)
	}
	if len(statuses) > 0 {
		q += ` AND status IN (?` + strings.Repeat(",?", len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	var n int
	_ = s.db.QueryRow(q, args...).Scan(&n)
	return n
}

// AttachmentsForMessages — вложения сразу для набора писем, по письму.
func (s *Store) AttachmentsForMessages(ids []int64) (map[int64][]*Attachment, error) {
	out := map[int64][]*Attachment{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(selectAtt+` WHERE a.message_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY a.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out[a.MessageID] = append(out[a.MessageID], a)
	}
	return out, rows.Err()
}
