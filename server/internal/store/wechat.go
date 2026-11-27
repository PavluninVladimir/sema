package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) migrateWeChat() error {
	_, err := s.db.Exec(`
 CREATE TABLE IF NOT EXISTS wechat_sync (
  account TEXT PRIMARY KEY,
  cursor BLOB NOT NULL
 );
 CREATE TABLE IF NOT EXISTS wechat_chats (
  account TEXT NOT NULL,
  peer TEXT NOT NULL,
  last_at TEXT NOT NULL,
  collect INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(account, peer)
 );
 CREATE TABLE IF NOT EXISTS wechat_messages (
  id INTEGER PRIMARY KEY,
  account TEXT NOT NULL,
  peer TEXT NOT NULL,
  message_id TEXT NOT NULL,
  sender TEXT NOT NULL,
  kind INTEGER NOT NULL,
  outgoing INTEGER NOT NULL,
  body BLOB NOT NULL,
  at TEXT NOT NULL,
  UNIQUE(account, peer, message_id)
 );
 CREATE TABLE IF NOT EXISTS wechat_contacts (
  account TEXT NOT NULL,
  peer TEXT NOT NULL,
  title TEXT NOT NULL,
  PRIMARY KEY(account,peer)
 );
 CREATE TABLE IF NOT EXISTS wechat_contact_sync (
  account TEXT PRIMARY KEY,
  cursor BLOB NOT NULL
 );
 CREATE TABLE IF NOT EXISTS wechat_send_rights (
  account TEXT NOT NULL,
  peer TEXT NOT NULL,
  PRIMARY KEY(account,peer)
 );
 CREATE TABLE IF NOT EXISTS wechat_automation (
  account TEXT NOT NULL,
  peer TEXT NOT NULL,
  reply INTEGER NOT NULL DEFAULT 0,
  admin INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(account,peer)
 );
 CREATE TABLE IF NOT EXISTS wechat_work (
  message INTEGER PRIMARY KEY,
  account TEXT NOT NULL,
  peer TEXT NOT NULL,
  mode TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending',
  started TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(message) REFERENCES wechat_messages(id) ON DELETE CASCADE
 );
 CREATE INDEX IF NOT EXISTS idx_wechat_work_state ON wechat_work(account,state,message);
 CREATE INDEX IF NOT EXISTS idx_wechat_work_rates ON wechat_work(account,peer,mode,started);
 CREATE TABLE IF NOT EXISTS wechat_admin_requests (
  message INTEGER PRIMARY KEY,
  host TEXT NOT NULL,
  command TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending',
  FOREIGN KEY(message) REFERENCES wechat_work(message) ON DELETE CASCADE
 );
 CREATE TABLE IF NOT EXISTS wechat_outbox (
  account TEXT NOT NULL,
  client_id INTEGER NOT NULL,
  peer TEXT NOT NULL,
  state TEXT NOT NULL,
  operation_id TEXT,
  server_id TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL,
  PRIMARY KEY(account,client_id)
 );
 CREATE UNIQUE INDEX IF NOT EXISTS idx_wechat_operation ON wechat_outbox(account, operation_id);
 CREATE INDEX IF NOT EXISTS idx_wechat_timeline ON wechat_messages(account, peer, at);
 UPDATE wechat_work SET state='needs-review' WHERE state='running';
 UPDATE wechat_admin_requests SET state='needs-review' WHERE state='running';
 `)
	if err != nil {
		return err
	}
	if err := s.migrateWeChatForward(); err != nil {
		return err
	}
	if s.hasColumn("wechat_chats", "files") {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Existing subscriptions already included files. Only migrate that choice once.
	for _, stmt := range []string{
		`ALTER TABLE wechat_chats ADD COLUMN files INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE wechat_chats ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE wechat_chats ADD COLUMN wait_since TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE wechat_chats ADD COLUMN wait_notified INTEGER NOT NULL DEFAULT 0`,
		`UPDATE wechat_chats SET files=collect WHERE collect=1`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var ErrWeChatCursorChanged = errors.New("позиция синхронизации WeChat уже изменилась")
var ErrWeChatOperationStarted = errors.New("отправка WeChat с этим ключом уже начата, проверьте результат в чате")

const EventWeChatMessageAdded = "wechat.message.added"

type WeChatContact struct{ Peer, Title string }

type WeChatMessage struct {
	Peer, ID, Sender string
	Kind             int32
	Outgoing         bool
	Body             []byte
	At               time.Time
}

type WeChatBatch struct {
	Messages     []WeChatMessage
	Contacts     []WeChatContact
	EventModules []string
}

func (s *Store) WeChatContactCursor(ctx context.Context, account string) ([]byte, error) {
	var cursor []byte
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM wechat_contact_sync WHERE account=?`, account).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return cursor, err
}

func saveWeChatContacts(ctx context.Context, tx *sql.Tx, account string, contacts []WeChatContact) error {
	for _, c := range contacts {
		if c.Peer == "" {
			return errors.New("контакт WeChat без идентификатора")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO wechat_contacts(account,peer,title) VALUES(?,?,?)
   ON CONFLICT(account,peer) DO UPDATE SET title=CASE WHEN excluded.title='' THEN title ELSE excluded.title END`, account, c.Peer, c.Title); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO wechat_chats(account,peer,last_at) VALUES(?,?,'')
   ON CONFLICT(account,peer) DO NOTHING`, account, c.Peer); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CommitWeChatContacts(ctx context.Context, account string, prior, next []byte, contacts []WeChatContact) error {
	if account == "" || len(prior) > 65536 || len(next) > 65536 {
		return errors.New("неверная позиция контактов WeChat")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actual []byte
	err = tx.QueryRowContext(ctx, `SELECT cursor FROM wechat_contact_sync WHERE account=?`, account).Scan(&actual)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !bytes.Equal(actual, prior) {
		return ErrWeChatCursorChanged
	}
	if err := saveWeChatContacts(ctx, tx, account, contacts); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO wechat_contact_sync(account,cursor) VALUES(?,?)
  ON CONFLICT(account) DO UPDATE SET cursor=excluded.cursor`, account, append([]byte{}, next...)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) WeChatCursor(ctx context.Context, account string) ([]byte, error) {
	var cursor []byte
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM wechat_sync WHERE account=?`, account).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return cursor, err
}

// CommitWeChatBatch commits the cursor together with all selected messages.
// A caller using an outdated cursor must fetch again from the saved position.
func (s *Store) CommitWeChatBatch(ctx context.Context, account string, prior, next []byte, batch WeChatBatch) error {
	if account == "" || len(prior) > 65536 || len(next) > 65536 {
		return errors.New("неверная позиция синхронизации WeChat")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actual []byte
	err = tx.QueryRowContext(ctx, `SELECT cursor FROM wechat_sync WHERE account=?`, account).Scan(&actual)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !bytes.Equal(actual, prior) {
		return ErrWeChatCursorChanged
	}
	if err := saveWeChatContacts(ctx, tx, account, batch.Contacts); err != nil {
		return err
	}
	for _, m := range batch.Messages {
		if m.Peer == "" || m.ID == "" || m.At.IsZero() {
			return errors.New("неполное сообщение WeChat")
		}
		at := m.At.UTC().Format(time.RFC3339)
		if _, err = tx.ExecContext(ctx, `INSERT INTO wechat_chats(account,peer,last_at) VALUES(?,?,?)
   ON CONFLICT(account,peer) DO UPDATE SET last_at=max(last_at,excluded.last_at) WHERE wechat_chats.blocked=0`, account, m.Peer, at); err != nil {
			return err
		}
		body := m.Body
		if body == nil {
			body = []byte{}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO wechat_messages(account,peer,message_id,sender,kind,outgoing,body,at)
   SELECT ?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM wechat_chats WHERE account=? AND peer=? AND collect=1 AND blocked=0 AND (? NOT IN (3,49) OR files=1))
   ON CONFLICT(account,peer,message_id) DO NOTHING`, account, m.Peer, m.ID, m.Sender, m.Kind, m.Outgoing, body, at, account, m.Peer, m.Kind)
		if err != nil {
			return err
		}
		if m.Kind == 1 || !m.Outgoing && len(batch.EventModules) > 0 {
			inserted, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if inserted == 0 {
				continue
			}
			refID, err := result.LastInsertId()
			if err != nil {
				return err
			}
			if m.Kind == 1 {
				if err := queueWeChatWork(ctx, tx, account, m, refID); err != nil {
					return err
				}
				if err := queueWeChatForward(ctx, tx, account, m, refID); err != nil {
					return err
				}
			}
			if m.Outgoing {
				continue
			}
			for _, module := range batch.EventModules {
				if _, err := tx.ExecContext(ctx, `INSERT INTO module_events(module,kind,ref_id,created) VALUES(?,?,?,?)`, module, EventWeChatMessageAdded, refID, at); err != nil {
					return err
				}
			}
		}
	}
	if next == nil {
		next = []byte{}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO wechat_sync(account,cursor) VALUES(?,?)
  ON CONFLICT(account) DO UPDATE SET cursor=excluded.cursor`, account, next); err != nil {
		return err
	}
	return tx.Commit()
}

// WeChatEvent returns only metadata and respects collection at delivery time.
func (s *Store) WeChatEvent(account string, id int64) (int32, string, error) {
	var kind int32
	var at string
	err := s.db.QueryRow(`SELECT m.kind,m.at FROM wechat_messages m
  JOIN wechat_chats c ON c.account=m.account AND c.peer=m.peer
  WHERE m.id=? AND m.account=? AND c.collect=1 AND c.blocked=0 AND m.outgoing=0`, id, account).Scan(&kind, &at)
	return kind, at, err
}

func (s *Store) SetWeChatCollect(ctx context.Context, account, peer string, enabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE wechat_chats SET collect=?,
		wait_since=CASE WHEN ? THEN wait_since ELSE '' END WHERE account=? AND peer=? AND blocked=0`, enabled, enabled, account, peer)
	if err := wechatRightResult(result, err); err != nil {
		return err
	}
	if !enabled {
		if err := clearWeChatAutomation(ctx, tx, account, peer); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetWeChatFiles(ctx context.Context, account, peer string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE wechat_chats SET files=? WHERE account=? AND peer=? AND blocked=0`, enabled, account, peer)
	return wechatRightResult(result, err)
}

func (s *Store) SetWeChatWait(ctx context.Context, account, peer string, enabled bool) error {
	since := ""
	if enabled {
		since = time.Now().UTC().Format(time.RFC3339)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE wechat_chats SET wait_since=CASE
		WHEN ?='' OR wait_since='' THEN ? ELSE wait_since END
		WHERE account=? AND peer=? AND blocked=0 AND (collect=1 OR ?='')`, since, since, account, peer, since)
	return wechatRightResult(result, err)
}

func (s *Store) SetWeChatBlocked(ctx context.Context, account, peer string, blocked bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE wechat_chats SET blocked=?,
		collect=CASE WHEN ? THEN 0 ELSE collect END, files=CASE WHEN ? THEN 0 ELSE files END,
		wait_since=CASE WHEN ? THEN '' ELSE wait_since END
		WHERE account=? AND peer=?`, blocked, blocked, blocked, blocked, account, peer)
	if err := wechatRightResult(result, err); err != nil {
		return err
	}
	if blocked {
		if _, err := tx.ExecContext(ctx, `DELETE FROM wechat_send_rights WHERE account=? AND peer=?`, account, peer); err != nil {
			return err
		}
		if err := clearWeChatAutomation(ctx, tx, account, peer); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func wechatRightResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("чат WeChat не найден или включено «Не трогать»")
	}
	return nil
}

type WeChatChat struct {
	Account string
	Title   string
	Peer    string
	Collect bool
	CanSend bool
	Files   bool
	Blocked bool
	Wait    bool
	Reply   bool
	Admin   bool
	Count   int
	LastAt  string
}

const wechatChatSelect = `SELECT c.account,c.peer,c.collect,c.last_at,COALESCE(t.title,''),
  c.blocked=0 AND EXISTS(SELECT 1 FROM wechat_send_rights r WHERE r.account=c.account AND r.peer=c.peer),
  c.files,c.blocked,c.wait_since!='',
  COALESCE(a.reply,0),COALESCE(a.admin,0),
  (SELECT count(*) FROM wechat_messages m WHERE m.account=c.account AND m.peer=c.peer)
  FROM wechat_chats c LEFT JOIN wechat_contacts t ON t.account=c.account AND t.peer=c.peer
  LEFT JOIN wechat_automation a ON a.account=c.account AND a.peer=c.peer`

func (s *Store) WeChatChat(ctx context.Context, account, peer string) (WeChatChat, error) {
	var chat WeChatChat
	err := s.db.QueryRowContext(ctx, wechatChatSelect+` WHERE c.account=? AND c.peer=?`, account, peer).
		Scan(&chat.Account, &chat.Peer, &chat.Collect, &chat.LastAt, &chat.Title, &chat.CanSend, &chat.Files, &chat.Blocked, &chat.Wait, &chat.Reply, &chat.Admin, &chat.Count)
	return chat, err
}

func (s *Store) WeChatChats(ctx context.Context, account, search string) ([]WeChatChat, error) {
	rows, err := s.db.QueryContext(ctx, wechatChatSelect+`
  WHERE c.account=? AND (?='' OR instr(lower(c.peer),lower(?))>0 OR instr(lower(t.title),lower(?))>0)
  ORDER BY c.collect DESC,c.last_at DESC,c.peer`, account, search, search, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chats []WeChatChat
	for rows.Next() {
		var chat WeChatChat
		if err = rows.Scan(&chat.Account, &chat.Peer, &chat.Collect, &chat.LastAt, &chat.Title, &chat.CanSend, &chat.Files, &chat.Blocked, &chat.Wait, &chat.Reply, &chat.Admin, &chat.Count); err != nil {
			return nil, err
		}
		chats = append(chats, chat)
	}
	return chats, rows.Err()
}

// WeChatText returns a bounded timeline for a chat with collection enabled.
func (s *Store) WeChatText(ctx context.Context, account, peer string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT CAST(substr(m.body,1,4000) AS TEXT) FROM wechat_messages m
  JOIN wechat_chats c ON c.account=m.account AND c.peer=m.peer
  WHERE m.account=? AND m.peer=? AND c.collect=1 AND c.blocked=0 AND m.kind=1
  ORDER BY m.at DESC,m.message_id DESC LIMIT 50`, account, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []string
	for rows.Next() {
		var message string
		if err = rows.Scan(&message); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) SetWeChatSend(ctx context.Context, account, peer string, allow bool) error {
	if !allow {
		_, err := s.db.ExecContext(ctx, `DELETE FROM wechat_send_rights WHERE account=? AND peer=?`, account, peer)
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO wechat_send_rights(account,peer)
 SELECT account,peer FROM wechat_chats WHERE account=? AND peer=? AND blocked=0
 ON CONFLICT(account,peer) DO UPDATE SET peer=excluded.peer`, account, peer)
	return wechatRightResult(result, err)
}

var ErrWeChatSendDenied = errors.New("нет права отправки в чат WeChat")

func (s *Store) BeginWeChatSend(ctx context.Context, account, peer string, clientID uint32, operationID string) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO wechat_outbox(account,client_id,peer,state,at,operation_id)
 SELECT c.account,?,c.peer,'unknown',?,NULLIF(?,'') FROM wechat_send_rights r
 JOIN wechat_chats c ON c.account=r.account AND c.peer=r.peer WHERE c.account=? AND c.peer=? AND c.blocked=0
 ON CONFLICT(account,operation_id) DO NOTHING`, clientID, time.Now().UTC().Format(time.RFC3339), operationID, account, peer)
	return s.weChatSendAdmission(ctx, result, err, account, operationID)
}

func (s *Store) BeginWeChatWorkSend(ctx context.Context, w WeChatWork, clientID uint32, operationID string) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO wechat_outbox(account,client_id,peer,state,at,operation_id)
 SELECT account,?,peer,'unknown',?,? FROM wechat_work
 WHERE message=? AND account=? AND peer=? AND mode=? AND state='running' AND `+wechatWorkPermission+`
 ON CONFLICT(account,operation_id) DO NOTHING`, clientID, time.Now().UTC().Format(time.RFC3339), operationID,
		w.Message, w.Account, w.Peer, w.Mode)
	return s.weChatSendAdmission(ctx, result, err, w.Account, operationID)
}

func (s *Store) weChatSendAdmission(ctx context.Context, result sql.Result, err error, account, operationID string) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		var existing int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM wechat_outbox WHERE account=? AND operation_id=?`, account, operationID).Scan(&existing); err == nil {
			return ErrWeChatOperationStarted
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return ErrWeChatSendDenied
	}
	return nil
}

func (s *Store) EndWeChatSend(ctx context.Context, account string, clientID uint32, state, serverID string) error {
	if state != "confirmed" && state != "rejected" {
		return errors.New("неверный результат отправки WeChat")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_outbox SET state=?,server_id=? WHERE account=? AND client_id=? AND state='unknown'`,
		state, serverID, account, clientID)
	return err
}

type WeChatFileCarrier struct {
	ID   string
	Body []byte
}

func (s *Store) WeChatFiles(ctx context.Context, account, peer string) ([]WeChatFileCarrier, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.message_id,m.body FROM wechat_messages m
 JOIN wechat_chats c ON c.account=m.account AND c.peer=m.peer
 WHERE m.account=? AND m.peer=? AND c.collect=1 AND c.files=1 AND c.blocked=0 AND m.kind IN (3,49) AND length(m.body)<=262144
 ORDER BY m.at DESC,m.message_id DESC LIMIT 20`, account, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []WeChatFileCarrier
	for rows.Next() {
		var file WeChatFileCarrier
		if err = rows.Scan(&file.ID, &file.Body); err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	return result, rows.Err()
}

func (s *Store) WeChatFileBody(ctx context.Context, account, peer, id string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT m.body FROM wechat_messages m
 JOIN wechat_chats c ON c.account=m.account AND c.peer=m.peer
 WHERE m.account=? AND m.peer=? AND m.message_id=? AND c.collect=1 AND c.files=1 AND c.blocked=0 AND m.kind IN (3,49)
 AND length(m.body)<=262144`, account, peer, id).Scan(&data)
	return data, err
}

func (s *Store) WeChatCanSend(ctx context.Context, account, peer string) (bool, error) {
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wechat_send_rights r
 JOIN wechat_chats c ON c.account=r.account AND c.peer=r.peer WHERE r.account=? AND r.peer=? AND c.blocked=0)`, account, peer).Scan(&allowed)
	return allowed, err
}

type WeChatWaiting struct {
	Peer, Since string
	Latest      int64
	Notified    int64
}

func (s *Store) WeChatWaitingChats(ctx context.Context, account string) ([]WeChatWaiting, error) {
	rows, err := s.db.QueryContext(ctx, `
	SELECT c.peer,COALESCE(min(m.at),''),COALESCE(max(m.id),0),c.wait_notified
	FROM wechat_chats c LEFT JOIN wechat_messages m ON m.account=c.account AND m.peer=c.peer
	  AND m.kind=1 AND m.outgoing=0 AND m.at >= c.wait_since
	  AND m.at > max(
	    COALESCE((SELECT max(o.at) FROM wechat_messages o WHERE o.account=c.account AND o.peer=c.peer AND o.outgoing=1),''),
	    COALESCE((SELECT max(o.at) FROM wechat_outbox o WHERE o.account=c.account AND o.peer=c.peer AND o.state='confirmed'),'')
	  )
	WHERE c.account=? AND c.wait_since!='' AND c.collect=1 AND c.blocked=0
	GROUP BY c.peer,c.wait_notified`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []WeChatWaiting
	for rows.Next() {
		var waiting WeChatWaiting
		if err := rows.Scan(&waiting.Peer, &waiting.Since, &waiting.Latest, &waiting.Notified); err != nil {
			return nil, err
		}
		result = append(result, waiting)
	}
	return result, rows.Err()
}

func (s *Store) MarkWeChatWaitNotified(ctx context.Context, account, peer string, message int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_chats SET wait_notified=? WHERE account=? AND peer=?`, message, account, peer)
	return err
}
