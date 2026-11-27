package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	WeChatReplyMode     = "reply"
	WeChatAdminMode     = "admin"
	WeChatCommandPrefix = "/sema "
)

func (c WeChatChat) SupportsWeChatAdmin() bool {
	return c.Peer == c.Account || c.Peer == "filehelper"
}

func (c WeChatChat) SupportsWeChatReply() bool {
	return !c.SupportsWeChatAdmin() && c.Peer != "weixin" &&
		!strings.HasSuffix(c.Peer, "@chatroom") && !strings.HasPrefix(c.Peer, "gh_")
}

func (s *Store) EnsureWeChatOwnerChat(ctx context.Context, account string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO wechat_chats(account,peer,last_at) VALUES(?,'filehelper','')
	  ON CONFLICT(account,peer) DO NOTHING`, account)
	return err
}

func clearWeChatAutomation(ctx context.Context, tx *sql.Tx, account, peer string) error {
	if err := pauseWeChatForward(ctx, tx, account, peer); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wechat_automation WHERE account=? AND peer=?`, account, peer); err != nil {
		return err
	}
	if err := cancelWeChatAdminRequests(ctx, tx, account, peer); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE wechat_work SET state='skipped' WHERE account=? AND peer=? AND state IN ('pending','running')`, account, peer)
	return err
}

func (s *Store) SetWeChatAutomation(ctx context.Context, account, peer, mode string, enabled bool) error {
	chat := WeChatChat{Account: account, Peer: peer}
	reply := mode == WeChatReplyMode && chat.SupportsWeChatReply()
	admin := mode == WeChatAdminMode && chat.SupportsWeChatAdmin()
	if !reply && !admin {
		return errors.New("это право недоступно для данного чата WeChat")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO wechat_automation(account,peer,reply,admin)
	SELECT account,peer,?,? FROM wechat_chats WHERE account=? AND peer=? AND collect=1 AND blocked=0
	ON CONFLICT(account,peer) DO UPDATE SET reply=CASE WHEN ? THEN excluded.reply ELSE reply END,
	admin=CASE WHEN ? THEN excluded.admin ELSE admin END`, reply && enabled, admin && enabled, account, peer, reply, admin)
	if err := wechatRightResult(result, err); err != nil {
		return err
	}
	if !enabled {
		if _, err := tx.ExecContext(ctx, `UPDATE wechat_work SET state='skipped'
		  WHERE account=? AND peer=? AND mode=? AND state IN ('pending','running')`, account, peer, mode); err != nil {
			return err
		}
		if admin {
			if err := cancelWeChatAdminRequests(ctx, tx, account, peer); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func cancelWeChatAdminRequests(ctx context.Context, tx *sql.Tx, account, peer string) error {
	_, err := tx.ExecContext(ctx, `UPDATE wechat_admin_requests SET state='rejected' WHERE state='pending'
	  AND message IN (SELECT message FROM wechat_work WHERE account=? AND (?='' OR peer=?))`, account, peer, peer)
	return err
}

func (s *Store) CancelWeChatWork(ctx context.Context, account string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := pauseWeChatForward(ctx, tx, account, ""); err != nil {
		return err
	}
	if err := cancelWeChatAdminRequests(ctx, tx, account, ""); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wechat_work SET state='skipped' WHERE account=? AND state IN ('pending','running')`, account); err != nil {
		return err
	}
	return tx.Commit()
}

func queueWeChatWork(ctx context.Context, tx *sql.Tx, account string, m WeChatMessage, row int64) error {
	chat := WeChatChat{Account: account, Peer: m.Peer}
	body := strings.TrimSpace(string(m.Body))
	if body == "" || len(body) > 12*1024 {
		return nil
	}
	mode := WeChatReplyMode
	if m.Outgoing {
		if !chat.SupportsWeChatAdmin() || m.Sender != account ||
			(body != "/sema" && !strings.HasPrefix(body, WeChatCommandPrefix)) {
			return nil
		}
		mode = WeChatAdminMode
	} else if !chat.SupportsWeChatReply() || m.Sender != m.Peer {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO wechat_work(message,account,peer,mode)
	  SELECT ?,c.account,c.peer,? FROM wechat_automation a
	  JOIN wechat_chats c ON c.account=a.account AND c.peer=a.peer
	  WHERE a.account=? AND a.peer=? AND c.collect=1 AND c.blocked=0
	  AND CASE ? WHEN 'reply' THEN a.reply ELSE a.admin END=1
	  ON CONFLICT(message) DO NOTHING`, row, mode, account, m.Peer, mode)
	return err
}

type WeChatWork struct {
	Message             int64
	Account, Peer, Mode string
	Body                string
}

const wechatWorkPermission = `EXISTS(
	SELECT 1 FROM wechat_automation a JOIN wechat_chats c ON c.account=a.account AND c.peer=a.peer
	WHERE a.account=wechat_work.account AND a.peer=wechat_work.peer AND c.collect=1 AND c.blocked=0
	AND CASE wechat_work.mode WHEN 'reply' THEN a.reply WHEN 'admin' THEN a.admin ELSE 0 END=1)`

// ClaimWeChatWork takes a new, recent message once. Stale requests and rate
// limit excesses are skipped rather than answered much later in a burst.
func (s *Store) ClaimWeChatWork(ctx context.Context, account string, now time.Time) (WeChatWork, error) {
	var w WeChatWork
	limit := now.UTC().Add(-15 * time.Minute).Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_work SET state='skipped'
	  WHERE account=? AND state='pending' AND (NOT `+wechatWorkPermission+` OR message IN
	  (SELECT id FROM wechat_messages WHERE account=? AND at<?))`, account, account, limit)
	if err != nil {
		return w, err
	}
	err = s.db.QueryRowContext(ctx, `UPDATE wechat_work SET state='running',started=?
	  WHERE message=(SELECT w.message FROM wechat_work w WHERE w.account=? AND w.state='pending'
	  ORDER BY w.message LIMIT 1) AND state='pending' AND `+wechatWorkPermission+`
	  RETURNING message,account,peer,mode`, now.UTC().Format(time.RFC3339), account).
		Scan(&w.Message, &w.Account, &w.Peer, &w.Mode)
	if err != nil {
		return w, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT CAST(body AS TEXT) FROM wechat_messages WHERE id=?`, w.Message).Scan(&w.Body)
	return w, err
}

func (s *Store) WeChatWorkAllowed(ctx context.Context, w WeChatWork) (bool, error) {
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wechat_work WHERE message=?
	  AND account=? AND peer=? AND mode=? AND state='running' AND `+wechatWorkPermission+`)`,
		w.Message, w.Account, w.Peer, w.Mode).Scan(&allowed)
	return allowed, err
}

func (s *Store) RecentWeChatWork(ctx context.Context, w WeChatWork, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM wechat_work WHERE account=? AND peer=? AND mode=?
	  AND message<>? AND started>=? AND state<>'skipped'`, w.Account, w.Peer, w.Mode, w.Message,
		since.UTC().Format(time.RFC3339)).Scan(&count)
	return count, err
}

func (s *Store) FinishWeChatWork(ctx context.Context, id int64, state string) error {
	switch state {
	case "done", "skipped", "failed":
	default:
		return errors.New("неверный итог обработки WeChat")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_work SET state=? WHERE message=? AND state='running'`, state, id)
	return err
}

func (s *Store) AddWeChatAdminRequest(ctx context.Context, w WeChatWork, host, command string) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO wechat_admin_requests(message,host,command)
	  SELECT message,?,? FROM wechat_work WHERE message=? AND account=? AND peer=?
	  AND mode='admin' AND state='running' AND `+wechatWorkPermission+`
	  ON CONFLICT(message) DO NOTHING`, host, command, w.Message, w.Account, w.Peer)
	return wechatRightResult(result, err)
}

func (s *Store) ClaimWeChatAdminRequest(ctx context.Context, account string, message int64) (host, cmd string, err error) {
	err = s.db.QueryRowContext(ctx, `UPDATE wechat_admin_requests SET state='running'
	  WHERE message=? AND state='pending' AND EXISTS(SELECT 1 FROM wechat_work WHERE message=? AND account=?
	  AND mode='admin' AND `+wechatWorkPermission+`) RETURNING host,command`, message, message, account).Scan(&host, &cmd)
	return host, cmd, err
}

func (s *Store) FinishWeChatAdminRequest(ctx context.Context, message int64, state string) error {
	var current string
	switch state {
	case "rejected":
		current = "pending"
	case "done", "failed":
		current = "running"
	default:
		return errors.New("неверный итог команды WeChat")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE wechat_admin_requests SET state=? WHERE message=? AND state=?`, state, message, current)
	return wechatRightResult(result, err)
}
