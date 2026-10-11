package store

import (
	"fmt"
	"strings"
	"time"
)

// MAX как источник: аккаунт, выбранные чаты и собранные сообщения. Токен
// доступа шифруется тем же ключом, что и секреты, и наружу не выдаётся —
// он и есть доступ ко всей переписке.

// MAXAccount — единственный аккаунт (пока один, как в Telegram).
type MAXAccount struct {
	Phone    string
	State    string // new | code | ok | fail
	Note     string
	DeviceID string // удостоверение устройства: со сменой MAX просит вход заново
	Token    string // токен доступа, всегда шифрованный
	CanSend  bool   // разрешено ли отправлять от вашего имени
	Created  string
	Last     string
}

// MAXChat — диалог или группа в выборе. Права те же, что у Telegram:
// человек выдаёт их поимённо, и «читать» не значит «отвечать».
type MAXChat struct {
	ID      int64
	Title   string
	Kind    string // dialog | chat | channel
	Collect bool   // читать переписку
	Send    bool   // Сёма может писать первой
	Files   bool   // забирать вложения
	Blocked bool   // не трогать вовсе
	Class   string // класс чувствительности по умолчанию
	Count   int
	Updated string
}

// MAXMessage — собранное сообщение: по нему считается «ждёт ответа» и
// строится история в чате Сёмы.
type MAXMessage struct {
	ID     int64
	ChatID int64
	MsgID  int64
	Text   string
	Out    bool
	At     string
}

func (s *Store) migrateMAX() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS max_account (
			phone     TEXT PRIMARY KEY,
			state     TEXT NOT NULL DEFAULT 'new',
			note      TEXT NOT NULL DEFAULT '',
			device_id TEXT NOT NULL DEFAULT '',
			token     TEXT NOT NULL DEFAULT '',
			can_send  INTEGER NOT NULL DEFAULT 0,
			created   TEXT NOT NULL,
			last      TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS max_chats (
			id      INTEGER PRIMARY KEY,
			title   TEXT NOT NULL DEFAULT '',
			kind    TEXT NOT NULL DEFAULT '',
			collect INTEGER NOT NULL DEFAULT 0,
			send    INTEGER NOT NULL DEFAULT 0,
			files   INTEGER NOT NULL DEFAULT 0,
			blocked INTEGER NOT NULL DEFAULT 0,
			class   TEXT NOT NULL DEFAULT '',
			count   INTEGER NOT NULL DEFAULT 0,
			updated TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS max_messages (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			msg_id  INTEGER NOT NULL,
			text    TEXT NOT NULL DEFAULT '',
			out     INTEGER NOT NULL DEFAULT 0,
			at      TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_max_msg ON max_messages(chat_id, msg_id)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue // миграция идёт на каждом старте
			}
			return fmt.Errorf("max: %w", err)
		}
	}
	return nil
}

// MAXAccountGet — аккаунт с расшифрованным токеном.
func (s *Store) MAXAccountGet() (*MAXAccount, bool) {
	a := &MAXAccount{}
	var send int
	err := s.db.QueryRow(`SELECT phone, state, note, device_id, token, can_send, created, last
		FROM max_account LIMIT 1`).Scan(&a.Phone, &a.State, &a.Note, &a.DeviceID, &a.Token, &send, &a.Created, &a.Last)
	if err != nil {
		return nil, false
	}
	a.CanSend = send == 1
	a.Token = s.open(a.Token) // расшифровка на выходе
	return a, true
}

// SaveMAXAccount — запись аккаунта. Без ключа шифрования токен не
// сохраняется вовсе: открытым такое не лежит.
func (s *Store) SaveMAXAccount(a *MAXAccount) error {
	a.Phone = strings.TrimSpace(a.Phone)
	if a.Phone == "" {
		return fmt.Errorf("нужен номер телефона")
	}
	if strings.TrimSpace(a.Token) != "" && s.box == nil {
		return fmt.Errorf("ключ шифрования не задан: токен MAX лёг бы в базу открытым")
	}
	if a.Created == "" {
		a.Created = time.Now().Format(time.RFC3339)
	}
	send := 0
	if a.CanSend {
		send = 1
	}
	_, err := s.db.Exec(`INSERT INTO max_account (phone, state, note, device_id, token, can_send, created, last)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(phone) DO UPDATE SET state = excluded.state, note = excluded.note,
			device_id = excluded.device_id, token = excluded.token, can_send = excluded.can_send,
			last = excluded.last`,
		a.Phone, a.State, a.Note, a.DeviceID, s.seal(a.Token), send, a.Created, a.Last)
	return err
}

// DeleteMAXAccount — забыть аккаунт: токен стирается, собранное остаётся.
func (s *Store) DeleteMAXAccount(phone string) error {
	_, err := s.db.Exec(`DELETE FROM max_account WHERE phone = ?`, phone)
	return err
}

// MAXChats — список чатов; onlyCollected — только выбранные человеком.
func (s *Store) MAXChats(onlyCollected bool) ([]*MAXChat, error) {
	q := `SELECT id, title, kind, collect, send, files, blocked, class, count, updated FROM max_chats`
	if onlyCollected {
		q += ` WHERE collect = 1`
	}
	q += ` ORDER BY collect DESC, title`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MAXChat
	for rows.Next() {
		c := &MAXChat{}
		var collect, send, files, blocked int
		if err := rows.Scan(&c.ID, &c.Title, &c.Kind, &collect, &send, &files, &blocked,
			&c.Class, &c.Count, &c.Updated); err != nil {
			return nil, err
		}
		c.Collect, c.Send, c.Files, c.Blocked = collect == 1, send == 1, files == 1, blocked == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) MAXChat(id int64) (*MAXChat, bool) {
	list, err := s.MAXChats(false)
	if err != nil {
		return nil, false
	}
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return nil, false
}

// SaveMAXChat — чат из списка. Выбор человека при обновлении списка не
// затирается: из MAX приходят название и вид, а права решает человек.
func (s *Store) SaveMAXChat(c *MAXChat) error {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	_, err := s.db.Exec(`INSERT INTO max_chats (id, title, kind, collect, send, files, blocked, class, count, updated)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title = excluded.title, kind = excluded.kind, updated = excluded.updated`,
		c.ID, c.Title, c.Kind, b(c.Collect), b(c.Send), b(c.Files), b(c.Blocked),
		c.Class, c.Count, time.Now().Format(time.RFC3339))
	return err
}

// SetMAXChatRight — переключить одно право чата.
func (s *Store) SetMAXChatRight(id int64, right string, on bool) error {
	col := map[string]string{
		"collect": "collect", "send": "send", "files": "files", "blocked": "blocked",
	}[right]
	if col == "" {
		return fmt.Errorf("неизвестное право %q", right)
	}
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE max_chats SET `+col+` = ?, updated = ? WHERE id = ?`,
		v, time.Now().Format(time.RFC3339), id)
	return err
}

// SetMAXChatCollect — «читать переписку» вместе с классом по умолчанию.
func (s *Store) SetMAXChatCollect(id int64, on bool, class string) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE max_chats SET collect = ?, class = ?, updated = ? WHERE id = ?`,
		v, class, time.Now().Format(time.RFC3339), id)
	return err
}

// AddMAXMessage — сообщение в архив. Второе значение false: такое уже
// собрано, и повторно его в переписку класть не нужно.
func (s *Store) AddMAXMessage(m *MAXMessage) (bool, error) {
	if m.At == "" {
		m.At = time.Now().Format(time.RFC3339)
	}
	out := 0
	if m.Out {
		out = 1
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO max_messages (chat_id, msg_id, text, out, at)
		VALUES (?,?,?,?,?)`, m.ChatID, m.MsgID, m.Text, out, m.At)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_, _ = s.db.Exec(`UPDATE max_chats SET count = count + 1, updated = ? WHERE id = ?`,
			time.Now().Format(time.RFC3339), m.ChatID)
	}
	return n > 0, nil
}

// MAXMessages — последние сообщения чата, от новых к старым.
func (s *Store) MAXMessages(chatID int64, limit int) ([]*MAXMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, chat_id, msg_id, text, out, at FROM max_messages
		WHERE chat_id = ? ORDER BY id DESC LIMIT ?`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MAXMessage
	for rows.Next() {
		m := &MAXMessage{}
		var isOut int
		if err := rows.Scan(&m.ID, &m.ChatID, &m.MsgID, &m.Text, &isOut, &m.At); err != nil {
			return nil, err
		}
		m.Out = isOut == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// MAXWaiting — сколько сообщений собеседника ждут ответа и когда пришло
// первое из них; то же, что у Telegram, для семафора «вам не ответили».
func (s *Store) MAXWaiting(chatID int64) (count int, since time.Time, last string) {
	var lastOut string
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(at), '') FROM max_messages WHERE chat_id = ? AND out = 1`,
		chatID).Scan(&lastOut)
	rows, err := s.db.Query(`SELECT at, text FROM max_messages
		WHERE chat_id = ? AND out = 0 AND at > ? ORDER BY at`, chatID, lastOut)
	if err != nil {
		return 0, time.Time{}, ""
	}
	defer rows.Close()
	for rows.Next() {
		var at, text string
		if rows.Scan(&at, &text) != nil {
			continue
		}
		if count == 0 {
			since = parseTime(at)
		}
		count++
		if strings.TrimSpace(text) != "" {
			last = text
		}
	}
	return count, since, last
}
