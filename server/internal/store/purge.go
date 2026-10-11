package store

// Журнал удалений с почтового сервера.
//
// Правило проекта: письма из архива Сёмы не удаляются никогда, удаление
// касается только почтового сервера. Значит у каждого удаления есть две
// стороны — письмо у Сёмы осталось, а на сервере его больше нет, — и
// человек вправе в любой момент узнать, что именно Сёма стёр и когда.
// Без такой записи «почистил спам» это слово, а не отчёт.

import (
	"fmt"
	"strings"
	"time"
)

// Deletion — одна запись журнала.
type Deletion struct {
	ID        int64
	MessageID int64
	Account   string
	Folder    string
	Subject   string
	From      string
	At        time.Time
	Policy    string // по какому правилу: «спам» или «срок хранения»
}

func (s *Store) migrateDeletions() {
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS mail_purged (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id INTEGER NOT NULL,
		account TEXT NOT NULL DEFAULT '',
		folder TEXT NOT NULL DEFAULT '',
		policy TEXT NOT NULL DEFAULT '',
		at TEXT NOT NULL
	)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS mail_purged_at ON mail_purged(at)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS mail_purged_msg ON mail_purged(message_id)`)
}

// LogDeletion — письмо стёрто с почтового сервера.
func (s *Store) LogDeletion(msgID int64, account, folder, policy string) error {
	_, err := s.db.Exec(
		`INSERT INTO mail_purged (message_id, account, folder, policy, at) VALUES (?, ?, ?, ?, ?)`,
		msgID, account, folder, policy, time.Now().Format(time.RFC3339))
	return err
}

// DeletionsSince — сколько писем стёрто с сервера за последние дни.
func (s *Store) DeletionsSince(days int) int {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	n := 0
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM mail_purged WHERE at >= ?`, cutoff).Scan(&n)
	return n
}

// RecentDeletions — журнал для экрана: что стёрли и откуда.
func (s *Store) RecentDeletions(limit int) ([]*Deletion, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT p.id, p.message_id, p.account, p.folder, p.policy, p.at,
       COALESCE(m.subject, ''), COALESCE(m.from_addr, '')
FROM mail_purged p LEFT JOIN messages m ON m.id = p.message_id
ORDER BY p.at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Deletion
	for rows.Next() {
		del := &Deletion{}
		var at string
		if err := rows.Scan(&del.ID, &del.MessageID, &del.Account, &del.Folder, &del.Policy, &at,
			&del.Subject, &del.From); err != nil {
			return nil, err
		}
		del.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, del)
	}
	return out, rows.Err()
}

// Deletions — журнал стёртого с сервера: с отбором по правилу, поиском по
// теме и отправителю и порцией. Экран со списком писем нужен затем, что
// «что именно стёрли» — вопрос про письма, а не про настройки.
func (s *Store) Deletions(policy, search string, limit int) ([]*Deletion, int, error) {
	if limit <= 0 {
		limit = 50
	}
	where, args := []string{"1=1"}, []any{}
	if policy != "" && policy != "все" {
		where = append(where, "p.policy = ?")
		args = append(args, policy)
	}
	if s := strings.TrimSpace(search); s != "" {
		where = append(where, "(m.subject LIKE ? OR m.from_addr LIKE ? OR p.account LIKE ? OR p.folder LIKE ?)")
		pattern := "%" + s + "%"
		args = append(args, pattern, pattern, pattern, pattern)
	}
	cond := strings.Join(where, " AND ")
	total := 0
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM mail_purged p LEFT JOIN messages m ON m.id = p.message_id WHERE `+cond, args...).Scan(&total)
	rows, err := s.db.Query(`
SELECT p.id, p.message_id, p.account, p.folder, p.policy, p.at,
       COALESCE(m.subject, ''), COALESCE(m.from_addr, '')
FROM mail_purged p LEFT JOIN messages m ON m.id = p.message_id
WHERE `+cond+`
ORDER BY p.at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	var out []*Deletion
	for rows.Next() {
		del := &Deletion{}
		var at string
		if err := rows.Scan(&del.ID, &del.MessageID, &del.Account, &del.Folder, &del.Policy, &at,
			&del.Subject, &del.From); err != nil {
			return nil, total, err
		}
		del.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, del)
	}
	return out, total, rows.Err()
}

// OldestDay — самый старый день, письма которого пора стирать с
// сервера, и сами эти письма.
//
// Папка чистится по дням, а не целиком: у человека в спаме могут лежать
// тысячи писем, и «почистить всё разом» это долгий проход, который либо
// упрётся в предел задачи посередине, либо так нагрузит почтовый сервер,
// что тот начнёт отказывать. По дню за проход видно, что происходит,
// можно остановить на любом шаге, и каждый день закрыт целиком — не
// бывает «полдня стёрто, полдня осталось».
//
// `statuses` — какие письма вообще подлежат правилу, `before` — срок,
// `column` — по какому времени считается возраст.
func (s *Store) OldestDay(account, column string, statuses []string, before time.Time) (day string, messages []*Message, err error) {
	if len(statuses) == 0 {
		return "", nil, fmt.Errorf("состояния не названы")
	}
	if column != "moved_at" && column != "date" {
		return "", nil, fmt.Errorf("по такому времени возраст не считается: %q", column)
	}
	placeholders := strings.Repeat(",?", len(statuses)-1)
	args := []any{}
	for _, st := range statuses {
		args = append(args, st)
	}
	args = append(args, before.Format(time.RFC3339))
	// письмо, уже стёртое с сервера, второй раз не берём
	whereSQL := ` WHERE status IN (?` + placeholders + `)
  AND ` + column + ` IS NOT NULL AND ` + column + ` <> '' AND ` + column + ` < ?
  AND id NOT IN (SELECT message_id FROM mail_purged)`
	if account != "" {
		whereSQL += ` AND account = ?`
		args = append(args, account)
	}
	err = s.db.QueryRow(`SELECT substr(`+column+`, 1, 10) FROM messages`+whereSQL+
		` ORDER BY `+column+` ASC LIMIT 1`, args...).Scan(&day)
	if err != nil || day == "" {
		return "", nil, nil // чистить нечего — это не ошибка
	}
	rows, err := s.db.Query(selectMsg+whereSQL+
		` AND substr(`+column+`, 1, 10) = ? ORDER BY id`, append(args, day)...)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return "", nil, err
		}
		messages = append(messages, m)
	}
	return day, messages, rows.Err()
}

// CountUnderTTL — сколько писем старше срока и ещё не стёрто с сервера.
// Та же выборка, что у СтарейшегоДня, только счётчиком: экрану нужно
// одно число, а не тысяча строк.
func (s *Store) CountUnderTTL(statuses []string, before time.Time) (int, error) {
	if len(statuses) == 0 {
		return 0, fmt.Errorf("состояния не названы")
	}
	placeholders := strings.Repeat(",?", len(statuses)-1)
	args := make([]any, 0, len(statuses)+1)
	for _, st := range statuses {
		args = append(args, st)
	}
	args = append(args, before.Format(time.RFC3339))
	n := 0
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages
	 WHERE status IN (?`+placeholders+`)
	   AND date IS NOT NULL AND date <> '' AND date < ?
	   AND id NOT IN (SELECT message_id FROM mail_purged)`, args...).Scan(&n)
	return n, err
}
