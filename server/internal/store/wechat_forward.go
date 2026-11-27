package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const wechatForwardSchema = `
 CREATE TABLE IF NOT EXISTS wechat_forward_rules (
  account TEXT NOT NULL, peer TEXT NOT NULL, enabled INTEGER NOT NULL,
  source_lang TEXT NOT NULL, target_lang TEXT NOT NULL, active_after INTEGER NOT NULL,
  PRIMARY KEY(account,peer)
 );
 CREATE TABLE IF NOT EXISTS wechat_forward_destinations (
  account TEXT NOT NULL, peer TEXT NOT NULL, channel TEXT NOT NULL,
  target_account TEXT NOT NULL, target_chat INTEGER NOT NULL,
  PRIMARY KEY(account,peer,channel,target_account,target_chat)
 );
 CREATE TABLE IF NOT EXISTS wechat_forward_queue (
  message INTEGER NOT NULL REFERENCES wechat_messages(id) ON DELETE CASCADE,
  channel TEXT NOT NULL, target_account TEXT NOT NULL, target_chat INTEGER NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0, retry_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(message,channel,target_account,target_chat)
 );
 CREATE INDEX IF NOT EXISTS idx_wechat_forward_queue ON wechat_forward_queue(state,retry_at,message);
 CREATE INDEX IF NOT EXISTS idx_wechat_forward_queue_target ON wechat_forward_queue(channel,target_account,target_chat) WHERE state='pending';
`

const wechatForwardEligible = `SELECT 'telegram' AS channel,t.account AS target_account,t.id AS target_chat,t.title AS title,t.name AS username,
   CASE WHEN (SELECT count(*) FROM tg_account WHERE enabled=1 AND can_send=1 AND state='ok')>1
    THEN coalesce(nullif(trim(a.title),''),a.phone) ELSE '' END AS account_title
  FROM tg_chats t JOIN tg_account a ON a.phone=t.account
  LEFT JOIN tg_topics g ON t.forum=1 AND g.account=t.account AND g.chat_id=t.id AND g.topic_id=1
  WHERE t.kind IN ('user','group','channel') AND (t.kind<>'channel' OR t.can_post=1)
    AND CASE WHEN g.send IN (0,1) THEN g.send ELSE t.send END=1
    AND t.blocked=0 AND coalesce(g.blocked,-1)<>1 AND a.enabled=1 AND a.can_send=1 AND a.state='ok'
  UNION ALL
  SELECT 'max',a.phone,c.id,c.title,'','' FROM max_chats c CROSS JOIN (SELECT phone,state,can_send FROM max_account LIMIT 1) a
  WHERE c.kind IN ('dialog','chat','channel') AND c.send=1 AND c.blocked=0 AND a.state='ok' AND a.can_send=1`

const wechatForwardTargetAllowed = `EXISTS (
 SELECT 1 FROM (` + wechatForwardEligible + `) e WHERE e.channel=wechat_forward_queue.channel
 AND e.target_account=wechat_forward_queue.target_account AND e.target_chat=wechat_forward_queue.target_chat)`

func (s *Store) migrateWeChatForward() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(wechatForwardSchema + wechatForwardRevocations +
		`UPDATE wechat_forward_queue SET state='unknown' WHERE state='sending';`); err != nil {
		return err
	}
	var legacy bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='wechat_forward_targets')`).Scan(&legacy); err != nil {
		return err
	}
	if legacy {
		if _, err := tx.Exec(`
 INSERT OR IGNORE INTO wechat_forward_destinations(account,peer,channel,target_account,target_chat)
 SELECT account,peer,'telegram',tg_account,tg_chat FROM wechat_forward_targets;
 INSERT OR IGNORE INTO wechat_forward_queue(message,channel,target_account,target_chat,state,attempts,retry_at)
 SELECT message,'telegram',tg_account,tg_chat,CASE WHEN state='sending' THEN 'unknown' ELSE state END,attempts,retry_at
 FROM wechat_forward_deliveries;
 DROP TRIGGER IF EXISTS wechat_forward_tg_chat_rights;
 DROP TRIGGER IF EXISTS wechat_forward_tg_chat_removed;
 DROP TRIGGER IF EXISTS wechat_forward_tg_account_rights;
 DROP TRIGGER IF EXISTS wechat_forward_tg_account_removed;
 DROP TABLE wechat_forward_targets;
 DROP TABLE wechat_forward_deliveries;
 `); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const (
	WeChatForwardTelegram = "telegram"
	WeChatForwardMAX      = "max"
)

const wechatForwardRevocations = `
 CREATE TRIGGER IF NOT EXISTS wechat_forward_tg_chats_update AFTER UPDATE OF send,blocked,kind,can_post,forum ON tg_chats BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='telegram' AND state='pending' AND target_account=NEW.account AND target_chat=NEW.id AND NOT ` + wechatForwardTargetAllowed + `;
 END;
 CREATE TRIGGER IF NOT EXISTS wechat_forward_tg_chats_delete AFTER DELETE ON tg_chats BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='telegram' AND state='pending' AND target_account=OLD.account AND target_chat=OLD.id;
 END;
 CREATE TRIGGER IF NOT EXISTS wechat_forward_tg_topics_update AFTER UPDATE OF send,blocked ON tg_topics  WHEN NEW.topic_id=1 BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='telegram' AND state='pending' AND target_account=NEW.account AND target_chat=NEW.chat_id AND NOT ` + wechatForwardTargetAllowed + `;
 END;
 CREATE TRIGGER IF NOT EXISTS wechat_forward_tg_account_update AFTER UPDATE OF enabled,can_send,state ON tg_account
 WHEN NEW.enabled=0 OR NEW.can_send=0 OR NEW.state<>'ok' BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='telegram' AND state='pending' AND target_account=NEW.phone;
 END;
 CREATE TRIGGER IF NOT EXISTS wechat_forward_tg_account_delete AFTER DELETE ON tg_account BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='telegram' AND state='pending' AND target_account=OLD.phone;
 END;
 CREATE TRIGGER IF NOT EXISTS wechat_forward_max_chats_update AFTER UPDATE OF send,blocked,kind ON max_chats
 WHEN NEW.send=0 OR NEW.blocked=1 OR NEW.kind NOT IN ('dialog','chat','channel') BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='max' AND state='pending' AND target_chat=NEW.id;
 END;
 CREATE TRIGGER IF NOT EXISTS wechat_forward_max_chats_delete AFTER DELETE ON max_chats BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='max' AND state='pending' AND target_chat=OLD.id;
 END;
 CREATE TRIGGER IF NOT EXISTS wechat_forward_max_account_update AFTER UPDATE OF can_send,state ON max_account
 WHEN NEW.can_send=0 OR NEW.state<>'ok' BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='max' AND state='pending' AND target_account=NEW.phone;
 END;
 CREATE TRIGGER IF NOT EXISTS wechat_forward_max_account_delete AFTER DELETE ON max_account BEGIN
  UPDATE wechat_forward_queue SET state='skipped' WHERE channel='max' AND state='pending' AND target_account=OLD.phone;
 END;
`

type WeChatForwardTarget struct {
	Channel, Account string
	Chat             int64
}

type WeChatForwardRule struct {
	Enabled                bool
	SourceLang, TargetLang string
	Targets                []WeChatForwardTarget
}

type WeChatForwardRecipient struct {
	Target                        WeChatForwardTarget
	Title, Username, AccountTitle string
}

func (s *Store) WeChatForwardRecipients(ctx context.Context) ([]WeChatForwardRecipient, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT channel,target_account,target_chat,title,username,account_title
	  FROM (`+wechatForwardEligible+`) ORDER BY channel DESC,title,target_account,target_chat`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WeChatForwardRecipient
	for rows.Next() {
		var r WeChatForwardRecipient
		if err := rows.Scan(&r.Target.Channel, &r.Target.Account, &r.Target.Chat, &r.Title, &r.Username, &r.AccountTitle); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) WeChatForwardRule(ctx context.Context, account, peer string) (WeChatForwardRule, error) {
	var rule WeChatForwardRule
	err := s.db.QueryRowContext(ctx, `SELECT enabled,source_lang,target_lang FROM wechat_forward_rules
	  WHERE account=? AND peer=?`, account, peer).Scan(&rule.Enabled, &rule.SourceLang, &rule.TargetLang)
	if errors.Is(err, sql.ErrNoRows) {
		return rule, nil
	}
	if err != nil {
		return rule, err
	}
	rule.Targets, err = s.wechatForwardTargets(ctx, `SELECT channel,target_account,target_chat FROM wechat_forward_destinations
	  WHERE account=? AND peer=? ORDER BY channel,target_account,target_chat`, account, peer)
	return rule, err
}

func (s *Store) wechatForwardTargets(ctx context.Context, query string, args ...any) ([]WeChatForwardTarget, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []WeChatForwardTarget
	for rows.Next() {
		var target WeChatForwardTarget
		if err := rows.Scan(&target.Channel, &target.Account, &target.Chat); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *Store) SaveWeChatForwardRule(ctx context.Context, account, peer string, rule WeChatForwardRule, now time.Time) error {
	if rule.Enabled && len(rule.Targets) == 0 {
		return errors.New("выберите хотя бы один чат для пересылки")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO wechat_forward_rules(account,peer,enabled,source_lang,target_lang,active_after)
	  SELECT account,peer,?,?,?,? FROM wechat_chats WHERE account=? AND peer=? AND collect=1 AND blocked=0
	  ON CONFLICT(account,peer) DO UPDATE SET enabled=excluded.enabled,source_lang=excluded.source_lang,
	    target_lang=excluded.target_lang,active_after=excluded.active_after`,
		rule.Enabled, rule.SourceLang, rule.TargetLang, now.Unix(), account, peer)
	if err := wechatRightResult(result, err); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wechat_forward_destinations WHERE account=? AND peer=?`, account, peer); err != nil {
		return err
	}
	for _, target := range rule.Targets {
		result, err := tx.ExecContext(ctx, `INSERT INTO wechat_forward_destinations(account,peer,channel,target_account,target_chat)
		  SELECT ?,?,channel,target_account,target_chat FROM (`+wechatForwardEligible+`)
		  WHERE channel=? AND target_account=? AND target_chat=?`, account, peer, target.Channel, target.Account, target.Chat)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				return err
			}
			return errors.New("одному из выбранных чатов больше не разрешена отправка")
		}
	}
	if err := skipWeChatForward(ctx, tx, account, peer); err != nil {
		return err
	}
	return tx.Commit()
}

func skipWeChatForward(ctx context.Context, tx *sql.Tx, account, peer string) error {
	_, err := tx.ExecContext(ctx, `UPDATE wechat_forward_queue SET state='skipped' WHERE state='pending'
	  AND message IN (SELECT id FROM wechat_messages WHERE account=? AND (?='' OR peer=?))`, account, peer, peer)
	return err
}

func pauseWeChatForward(ctx context.Context, tx *sql.Tx, account, peer string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE wechat_forward_rules SET enabled=0 WHERE account=? AND (?='' OR peer=?)`,
		account, peer, peer); err != nil {
		return err
	}
	return skipWeChatForward(ctx, tx, account, peer)
}

func queueWeChatForward(ctx context.Context, tx *sql.Tx, account string, m WeChatMessage, row int64) error {
	if m.Outgoing {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO wechat_forward_queue(message,channel,target_account,target_chat)
	  SELECT ?,f.channel,f.target_account,f.target_chat FROM wechat_forward_rules r
	  JOIN wechat_forward_destinations f ON f.account=r.account AND f.peer=r.peer
	  JOIN (`+wechatForwardEligible+`) e USING (channel,target_account,target_chat)
	  WHERE r.account=? AND r.peer=? AND r.enabled=1 AND r.active_after<?`,
		row, account, m.Peer, m.At.Unix())
	return err
}

type WeChatForwardMessage struct {
	ID                           int64
	Body, SourceLang, TargetLang string
}

const wechatForwardAllowed = `EXISTS (
 SELECT 1 FROM wechat_messages m
 JOIN wechat_chats c ON c.account=m.account AND c.peer=m.peer
 JOIN wechat_forward_rules r ON r.account=m.account AND r.peer=m.peer
 JOIN wechat_forward_destinations f ON f.account=m.account AND f.peer=m.peer
 WHERE m.id=wechat_forward_queue.message AND c.collect=1 AND c.blocked=0 AND r.enabled=1
 AND f.channel=wechat_forward_queue.channel AND f.target_account=wechat_forward_queue.target_account
 AND f.target_chat=wechat_forward_queue.target_chat) AND ` + wechatForwardTargetAllowed

func (s *Store) NextWeChatForward(ctx context.Context, account string, now time.Time) (WeChatForwardMessage, []WeChatForwardTarget, error) {
	var m WeChatForwardMessage
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_forward_queue SET state='skipped'
	  WHERE state='pending' AND message IN (SELECT id FROM wechat_messages WHERE account=?)
	  AND NOT `+wechatForwardAllowed, account)
	if err != nil {
		return m, nil, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT m.id,CAST(m.body AS TEXT),r.source_lang,r.target_lang
	  FROM wechat_messages m JOIN wechat_forward_rules r ON r.account=m.account AND r.peer=m.peer
	  WHERE m.account=? AND m.id IN (SELECT message FROM wechat_forward_queue
	    WHERE state='pending' AND retry_at<=?) ORDER BY m.id LIMIT 1`, account, now.Unix()).
		Scan(&m.ID, &m.Body, &m.SourceLang, &m.TargetLang)
	if err != nil {
		return m, nil, err
	}
	targets, err := s.wechatForwardTargets(ctx, `SELECT channel,target_account,target_chat FROM wechat_forward_queue
	  WHERE message=? AND state='pending' AND retry_at<=? ORDER BY channel,target_account,target_chat`, m.ID, now.Unix())
	return m, targets, err
}

func (s *Store) BeginWeChatForward(ctx context.Context, message int64, target WeChatForwardTarget) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE wechat_forward_queue SET state='sending'
	  WHERE message=? AND channel=? AND target_account=? AND target_chat=? AND state='pending' AND `+wechatForwardAllowed,
		message, target.Channel, target.Account, target.Chat)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) FinishWeChatForward(ctx context.Context, message int64, target WeChatForwardTarget, delivered bool) error {
	state := "unknown"
	if delivered {
		state = "sent"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_forward_queue SET state=?
	  WHERE message=? AND channel=? AND target_account=? AND target_chat=? AND state='sending'`, state, message, target.Channel, target.Account, target.Chat)
	return err
}

func (s *Store) DeferWeChatForward(ctx context.Context, message int64, target WeChatForwardTarget, retryAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_forward_queue SET attempts=attempts+1,retry_at=?,
	  state=CASE WHEN attempts>=2 THEN 'failed' ELSE 'pending' END
	  WHERE message=? AND channel=? AND target_account=? AND target_chat=? AND state='pending'`,
		retryAt.Unix(), message, target.Channel, target.Account, target.Chat)
	return err
}

func (s *Store) StopWeChatForward(ctx context.Context, message int64, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_forward_queue SET state=? WHERE message=? AND state='pending'`, state, message)
	return err
}

func (s *Store) WeChatForwardCounts(ctx context.Context, account, peer string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.state,COUNT(*) FROM wechat_forward_queue d
	  JOIN wechat_messages m ON m.id=d.message WHERE m.account=? AND m.peer=? GROUP BY d.state`, account, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		counts[state] = n
	}
	return counts, rows.Err()
}
