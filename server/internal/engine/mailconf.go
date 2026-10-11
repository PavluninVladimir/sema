package engine

// Настройки ящика: тип авторизации со своими полями, папки архива и спама,
// список папок. Список папок Сёма читает с сервера — но только после того,
// как вход удался: до этого показывать нечего.

import (
	"context"
	"fmt"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// MailConf — настройки ящика с подставленными значениями из config.yaml.
func (e *Engine) MailConf(account string) *store.MailConf {
	acct, _ := e.accountByName(account)
	c := e.store.MailConf(acct.Name)
	if c.Auth == "" {
		switch acct.Auth {
		case "gmail":
			c.Auth = store.AuthGoogle
		case "password":
			c.Auth = store.AuthAppPassword
		default:
			c.Auth = store.AuthIMAP
		}
	}
	if c.Host == "" {
		c.Host = acct.IMAPHost
	}
	if c.Port == 0 {
		c.Port = acct.IMAPPort
	}
	if c.Username == "" {
		c.Username = acct.Username
	}
	if c.Auth == store.AuthGoogle && c.ClientID == "" {
		c.ClientID = e.conf.Mail.Google.ClientID
		c.Secret = e.conf.Mail.Google.Secret()
	}
	return c
}

func (e *Engine) SaveMailConf(account string, c *store.MailConf) error {
	acct, _ := e.accountByName(account)
	return e.store.SaveMailConf(acct.Name, c)
}

// mailAccount собирает параметры подключения из настроек ящика: именно они,
// а не config.yaml, решают, как Сёма входит в почту.
func (e *Engine) mailAccount(account string) (config.Mail, *store.MailConf) {
	acct, _ := e.accountByName(account)
	c := e.MailConf(acct.Name)
	acct.IMAPHost = c.Host
	acct.IMAPPort = c.Port
	acct.Username = c.Username
	if c.FetchDays != 0 { // -1 — за всё время
		acct.FetchWindowDays = c.FetchDays
	}
	if c.JunkDays > 0 {
		acct.JunkRetentionDay = c.JunkDays
	}
	if c.Password != "" {
		acct.Password = c.Password
	}
	switch c.Auth {
	case store.AuthGoogle:
		acct.Auth = "gmail"
	default:
		acct.Auth = "password"
	}
	return acct, c
}

// ConnectMailbox проверяет вход и подтягивает список папок ящика.
// Возвращает, что увидели: сколько папок и какие из них архив и спам.
func (e *Engine) ConnectMailbox(ctx context.Context, account string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	acct, c := e.mailAccount(account)
	sess, err := e.dialAccount(acct)
	if err != nil {
		c.LastErr = err.Error()
		c.Connected = ""
		_ = e.SaveMailConf(account, c)
		return "", err
	}
	defer sess.Close()

	boxes, err := sess.Mailboxes()
	if err != nil {
		c.LastErr = err.Error()
		_ = e.SaveMailConf(account, c)
		return "", err
	}
	names := make([]string, 0, len(boxes))
	for _, b := range boxes {
		if b.Selectable() {
			names = append(names, b.Name)
		}
	}
	c.Folders = names
	c.LastErr = ""
	c.Connected = time.Now().Format(time.RFC3339)
	if c.Junk == "" {
		c.Junk = sess.JunkFolder()
	}
	if c.Archive == "" {
		if box, _ := sess.ArchiveFolder(); box != "" {
			c.Archive = box
		}
	}
	if err := e.SaveMailConf(account, c); err != nil {
		return "", err
	}
	return fmt.Sprintf("вход выполнен · папок: %d", len(names)), nil
}

// DialMailbox — сеанс с ящиком по имени для диагностики (semacheck).
func (e *Engine) DialMailbox(name string) (*mail.Session, error) {
	sess, _, err := e.dialConfigured(name)
	return sess, err
}

// dialConfigured открывает сеанс по настройкам ящика и учитывает выбранную
// вручную папку спама.
func (e *Engine) dialConfigured(account string) (*mail.Session, *store.MailConf, error) {
	acct, c := e.mailAccount(account)
	sess, err := e.dialAccount(acct)
	if err != nil {
		return nil, c, err
	}
	if c.Junk != "" {
		sess.SetJunk(c.Junk)
	}
	return sess, c, nil
}
