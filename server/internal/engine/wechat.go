package engine

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/wechat"
)

const wechatSecret = "wechat:account:oauth"

type wechatLoginState struct {
	mu              sync.Mutex
	attempt         *wechat.Login
	expiry          *time.Timer
	lastContactSync time.Time
}

func (s *wechatLoginState) clear() {
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
	if s.attempt != nil {
		s.attempt.Close()
		s.attempt = nil
	}
}

func (e *Engine) WeChatBegin(ctx context.Context) error {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	if !e.Store().SecretsEncrypted() {
		return errors.New("для WeChat нужен ключ шифрования Сёмы")
	}
	if _, found := e.Store().Secret(wechatSecret); found {
		return errors.New("аккаунт WeChat уже сохранён")
	}
	s.clear()
	s.lastContactSync = time.Time{}
	attempt, err := wechat.BeginLogin(ctx)
	if err != nil {
		return err
	}
	s.attempt = attempt
	_, until := attempt.QR()
	s.expiry = time.AfterFunc(time.Until(until), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.attempt == attempt {
			s.clear()
		}
	})
	return nil
}

func (e *Engine) WeChatQR() ([]byte, time.Time) {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempt == nil {
		return nil, time.Time{}
	}
	image, until := s.attempt.QR()
	if !time.Now().Before(until) {
		s.clear()
		return nil, time.Time{}
	}
	return image, until
}

func (e *Engine) WeChatPoll(ctx context.Context) (wechat.LoginState, error) {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempt == nil {
		return "", errors.New("сначала запросите QR WeChat")
	}
	state, secret, err := s.attempt.Poll(ctx)
	if err != nil {
		s.clear()
		return "", err
	}
	defer clear(secret)
	switch state {
	case wechat.LoginConnected:
		defer s.clear()
		err = e.Store().SaveSecret(&store.Secret{Name: wechatSecret, Kind: store.SecretOAuth, Note: "Сессия WeChat"}, string(secret))
		if err != nil {
			return "", err
		}
	case wechat.LoginRejected:
		s.clear()
	}
	return state, nil
}

func (e *Engine) WeChatConnected() bool {
	_, ok := e.Store().Secret(wechatSecret)
	return ok
}

func (e *Engine) WeChatDisconnect() error {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clear()
	account, _ := e.wechatIdentity()
	if err := e.Store().DeleteSecret(wechatSecret); err != nil {
		return err
	}
	if account != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := e.Store().CancelWeChatWork(ctx, account); err != nil {
			return err
		}
		chats, _ := e.Store().WeChatWaitingChats(ctx, account)
		for _, chat := range chats {
			e.CloseNotification(wechatWaitKey(account, chat.Peer), store.NotifyDone, "WeChat отключён")
		}
	}
	return nil
}

func (e *Engine) syncWeChat(ctx context.Context, refreshContacts bool) (string, error) {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, exists := e.Store().SecretValue(wechatSecret)
	if !exists || secret == "" {
		return "", errors.New("аккаунт WeChat не подключён или сессия не читается")
	}
	snapshot := []byte(secret)
	defer func() { clear(snapshot) }()
	account, err := wechat.AccountIdentity(snapshot)
	if err != nil {
		return "", err
	}
	if err := e.Store().EnsureWeChatOwnerChat(ctx, account); err != nil {
		return "", err
	}
	cursor, err := e.Store().WeChatCursor(ctx, account)
	if err != nil {
		return "", err
	}
	renewed := false
	for i := 0; i < 5; i++ {
		batch, next, err := wechat.Pull(ctx, snapshot, cursor)
		if err != nil && !renewed && wechat.SessionRejected(err) {
			renewed = true
			fresh, renewErr := wechat.Renew(ctx, snapshot)
			if renewErr != nil {
				return "", renewErr
			}
			if err = e.Store().SaveSecret(&store.Secret{Name: wechatSecret, Kind: store.SecretOAuth, Note: "Сессия WeChat"}, string(fresh)); err != nil {
				clear(fresh)
				return "", err
			}
			clear(snapshot)
			snapshot = fresh
			batch, next, err = wechat.Pull(ctx, snapshot, cursor)
		}
		if err != nil {
			return "", err
		}
		messages := make([]store.WeChatMessage, 0, len(batch.Messages))
		for _, m := range batch.Messages {
			messages = append(messages, store.WeChatMessage{Peer: m.Peer, ID: m.ID, Sender: m.Sender,
				Kind: m.Kind, Outgoing: m.Outgoing, Body: m.Body, At: m.At})
		}
		var subscribers []string
		for _, m := range e.Modules() {
			if m.Enabled && m.Handler(EventWeChatMessageAdded) != "" && hasString(canonEvents(m.GrantedEvents), EventWeChatMessageAdded) {
				subscribers = append(subscribers, m.Name)
			}
		}
		if len(next) > 0 {
			err = e.Store().SaveSecret(&store.Secret{Name: wechatSecret, Kind: store.SecretOAuth, Note: "Сессия WeChat"}, string(next))
			clear(snapshot)
			snapshot = next
			if err != nil {
				return "", err
			}
		}
		err = e.Store().CommitWeChatBatch(ctx, account, cursor, batch.Cursor, store.WeChatBatch{
			Messages: messages, Contacts: wechatContacts(batch.Contacts), EventModules: subscribers,
		})
		if err != nil {
			return "", err
		}
		if len(subscribers) > 0 && len(messages) > 0 {
			e.wakeModuleEvents()
		}
		cursor = batch.Cursor
		if !batch.More {
			if err := e.wechatWaitPass(ctx, account); err != nil {
				return "", err
			}
			if len(cursor) > 0 && (refreshContacts || time.Since(s.lastContactSync) >= 10*time.Minute) {
				complete, err := e.syncWeChatContacts(ctx, account, snapshot, cursor)
				if err != nil {
					return "", err
				}
				if complete {
					s.lastContactSync = time.Now()
				}
			}
			return account, nil
		}
	}
	return account, nil
}

func wechatContacts(contacts []wechat.Contact) []store.WeChatContact {
	var result []store.WeChatContact
	for _, c := range contacts {
		result = append(result, store.WeChatContact{Peer: c.Peer, Title: c.Title})
	}
	return result
}

func (e *Engine) syncWeChatContacts(ctx context.Context, account string, snapshot, messageCursor []byte) (bool, error) {
	cursor, err := e.Store().WeChatContactCursor(ctx, account)
	if err != nil {
		return false, err
	}
	snapshot = append([]byte(nil), snapshot...)
	defer func() { clear(snapshot) }()
	for i := 0; i < 5; i++ {
		page, next, err := wechat.PullContacts(ctx, snapshot, cursor, messageCursor, i > 0)
		if err != nil {
			return false, err
		}
		if len(next) > 0 {
			err = e.Store().SaveSecret(&store.Secret{Name: wechatSecret, Kind: store.SecretOAuth, Note: "Сессия WeChat"}, string(next))
			clear(snapshot)
			snapshot = next
			if err != nil {
				return false, err
			}
		}
		if err := e.Store().CommitWeChatContacts(ctx, account, cursor, page.Cursor, wechatContacts(page.Contacts)); err != nil {
			return false, err
		}
		if !page.More {
			return true, nil
		}
		cursor = page.Cursor
	}
	return false, nil
}

func (e *Engine) WeChatRefresh(ctx context.Context) error {
	_, err := e.syncWeChat(ctx, true)
	return err
}

func (e *Engine) wechatIdentity() (string, error) {
	value, ok := e.Store().SecretValue(wechatSecret)
	if !ok || value == "" {
		return "", errors.New("сессия WeChat не читается")
	}
	snapshot := []byte(value)
	defer clear(snapshot)
	return wechat.AccountIdentity(snapshot)
}

func (e *Engine) WeChatChats(ctx context.Context) ([]store.WeChatChat, error) {
	account, err := e.wechatIdentity()
	if err != nil {
		return nil, err
	}
	return e.Store().WeChatChats(ctx, account, "")
}

func (e *Engine) WeChatChat(ctx context.Context, peer string) (store.WeChatChat, error) {
	account, err := e.wechatIdentity()
	if err != nil {
		return store.WeChatChat{}, err
	}
	return e.Store().WeChatChat(ctx, account, peer)
}

func (e *Engine) WeChatSetCollect(ctx context.Context, peer string, enabled bool) error {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	account, err := e.wechatIdentity()
	if err != nil {
		return err
	}
	if err := e.Store().SetWeChatCollect(ctx, account, peer, enabled); err != nil {
		return err
	}
	if !enabled {
		e.CloseNotification(wechatWaitKey(account, peer), store.NotifyDone, "чтение WeChat выключено")
	}
	return nil
}

func (e *Engine) WeChatSetChatRight(ctx context.Context, peer, right string, enabled bool) error {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	account, err := e.wechatIdentity()
	if err != nil {
		return err
	}
	switch right {
	case "files":
		return e.Store().SetWeChatFiles(ctx, account, peer, enabled)
	case "blocked":
		err = e.Store().SetWeChatBlocked(ctx, account, peer, enabled)
	case "wait":
		err = e.Store().SetWeChatWait(ctx, account, peer, enabled)
	case store.WeChatReplyMode, store.WeChatAdminMode:
		return e.Store().SetWeChatAutomation(ctx, account, peer, right, enabled)
	default:
		return errors.New("неизвестное право WeChat")
	}
	if err == nil && ((right == "blocked" && enabled) || (right == "wait" && !enabled)) {
		e.CloseNotification(wechatWaitKey(account, peer), store.NotifyDone, "напоминания WeChat выключены")
	}
	return err
}

func (e *Engine) WeChatText(ctx context.Context, peer string) ([]string, error) {
	account, err := e.wechatIdentity()
	if err != nil {
		return nil, err
	}
	return e.Store().WeChatText(ctx, account, peer)
}

func (e *Engine) WeChatSetSend(ctx context.Context, peer string, enabled bool) error {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	account, err := e.wechatIdentity()
	if err != nil {
		return err
	}
	return e.Store().SetWeChatSend(ctx, account, peer, enabled)
}

func (e *Engine) WeChatSendText(ctx context.Context, peer, body string) error {
	return e.wechatSendText(ctx, peer, body, "")
}

func (e *Engine) wechatSendText(ctx context.Context, peer, body, operationID string) error {
	return e.wechatSend(ctx, peer, operationID, nil, func(snapshot []byte, id uint32, begin func() error) (wechat.Delivery, error) {
		return wechat.SendText(ctx, snapshot, peer, body, id, begin)
	})
}

func (e *Engine) WeChatSendFile(ctx context.Context, peer string, fileID int64, asImage bool) error {
	return e.wechatSend(ctx, peer, "", nil, func(snapshot []byte, id uint32, begin func() error) (wechat.Delivery, error) {
		file, err := e.Store().AttachmentByID(fileID)
		if err != nil {
			return wechat.Delivery{}, err
		}
		if e.ClassOfFile(file) == store.ClassSecret {
			return wechat.Delivery{}, errors.New("секретный файл нельзя отправить в WeChat")
		}
		if file.Size <= 0 || file.Size > 25*1024*1024-17 {
			return wechat.Delivery{}, errors.New("файл WeChat должен помещаться в 25 МиБ")
		}
		data, err := e.FileBytes(file)
		if err != nil {
			return wechat.Delivery{}, err
		}
		defer clear(data)
		if asImage {
			return wechat.SendJPEG(ctx, snapshot, peer, file.Filename, data, id, begin)
		}
		return wechat.SendFile(ctx, snapshot, peer, file.Filename, data, id, begin)
	})
}

func (e *Engine) wechatSend(ctx context.Context, peer, operationID string, work *store.WeChatWork, send func([]byte, uint32, func() error) (wechat.Delivery, error)) error {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	account, err := e.wechatIdentity()
	if err != nil {
		return err
	}
	var allowed bool
	if work == nil {
		allowed, err = e.Store().WeChatCanSend(ctx, account, peer)
	} else if account == work.Account && peer == work.Peer {
		allowed, err = e.Store().WeChatWorkAllowed(ctx, *work)
	}
	if err != nil {
		return err
	}
	if !allowed {
		return store.ErrWeChatSendDenied
	}
	value, _ := e.Store().SecretValue(wechatSecret)
	snapshot := []byte(value)
	defer clear(snapshot)
	var random [4]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	id := binary.BigEndian.Uint32(random[:])
	if id == 0 {
		id = 1
	}
	recorded := false
	delivery, err := send(snapshot, id, func() error {
		var err error
		if work == nil {
			err = e.Store().BeginWeChatSend(ctx, account, peer, id, operationID)
		} else {
			err = e.Store().BeginWeChatWorkSend(ctx, *work, id, operationID)
		}
		if err != nil {
			return err
		}
		recorded = true
		return nil
	})
	if err != nil {
		if recorded {
			return fmt.Errorf("отправка WeChat №%d осталась без подтверждения. Проверьте чат перед повтором: %w", id, err)
		}
		return err
	}
	status := "confirmed"
	if delivery.Rejected {
		status = "rejected"
	}
	interruptible, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err = e.Store().EndWeChatSend(interruptible, account, id, status, delivery.ServerID); err != nil {
		return fmt.Errorf("WeChat ответил, но итог отправки №%d не записан. Проверьте чат перед повтором: %w", id, err)
	}
	if delivery.Rejected {
		return fmt.Errorf("WeChat отклонил отправку, код %d", delivery.Code)
	}
	return nil
}

// ListenWeChat picks up new messages after restart using the stored cursor.
// Network failures back off and leave the cursor at the last committed batch.
func (e *Engine) ListenWeChat(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	delay := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if e.WeChatConnected() {
				job, cancel := context.WithTimeout(ctx, 45*time.Second)
				_, err := e.syncWeChat(job, false)
				cancel()
				if ctx.Err() == nil {
					work, stop := context.WithTimeout(ctx, 2*time.Minute)
					e.processWeChatWork(work)
					stop()
					forward, finishForward := context.WithTimeout(ctx, 3*time.Minute)
					e.processWeChatForward(forward)
					finishForward()
				}
				if err != nil {
					delay = min(delay*2, 15*time.Minute)
				} else {
					delay = time.Minute
				}
			} else {
				delay = time.Minute
			}
			timer.Reset(delay)
		}
	}
}

type WeChatFile struct {
	ID, Name string
}

func (e *Engine) WeChatFiles(ctx context.Context, peer string) ([]WeChatFile, error) {
	account, err := e.wechatIdentity()
	if err != nil {
		return nil, err
	}
	carriers, err := e.Store().WeChatFiles(ctx, account, peer)
	if err != nil {
		return nil, err
	}
	var files []WeChatFile
	for _, m := range carriers {
		info, err := wechat.InspectFile(m.Body)
		if err == nil {
			files = append(files, WeChatFile{ID: m.ID, Name: info.Name})
		}
		clear(m.Body)
	}
	return files, nil
}

func (e *Engine) WeChatDownload(ctx context.Context, peer, id string) (wechat.FileInfo, []byte, error) {
	s := &e.wechatLogin
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := e.Store().SecretValue(wechatSecret)
	if !ok || value == "" {
		return wechat.FileInfo{}, nil, errors.New("сессия WeChat не читается")
	}
	snapshot := []byte(value)
	defer clear(snapshot)
	account, err := wechat.AccountIdentity(snapshot)
	if err != nil {
		return wechat.FileInfo{}, nil, err
	}
	body, err := e.Store().WeChatFileBody(ctx, account, peer, id)
	if err != nil {
		return wechat.FileInfo{}, nil, err
	}
	defer clear(body)
	return wechat.DownloadFile(ctx, snapshot, body)
}
