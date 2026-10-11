package engine

import (
	"fmt"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Пресеты IMAP-хостов для добавления ящика из UI.
var accountPresets = map[string]string{
	"yandex":   "imap.yandex.ru",
	"mailru":   "imap.mail.ru",
	"gmail":    "imap.gmail.com",
	"exchange": "outlook.office365.com",
}

// Accounts возвращает все ящики: основной (config), дополнительные из config
// и добавленные из UI (лежат в SQLite).
func (e *Engine) Accounts() []config.Mail {
	var out []config.Mail
	if e.conf.Mail.Username != "" { // ящик из файла запуска, если он там был
		out = append(out, e.conf.Mail)
	}
	out = append(out, e.conf.Extra...)
	dbAccounts, err := e.store.Accounts()
	if err != nil {
		return out
	}
	for _, a := range dbAccounts {
		out = append(out, config.Mail{
			Name:             a.Name,
			Auth:             "password",
			IMAPHost:         a.Host,
			IMAPPort:         a.Port,
			Username:         a.Email,
			Password:         a.Password,
			FetchWindowDays:  e.conf.Mail.FetchWindowDays,
			JunkRetentionDay: e.conf.Mail.JunkRetentionDay,
		})
	}
	return out
}

func (e *Engine) accountByName(name string) (config.Mail, bool) {
	for _, a := range e.Accounts() {
		if a.Name == name || (name == "" && a.Name == e.conf.Mail.Name) {
			return a, true
		}
	}
	return config.Mail{}, false
}

// dialAccount открывает IMAP-сеанс для ящика (OAuth — только у основного).
func (e *Engine) dialAccount(acct config.Mail) (*mail.Session, error) {
	tokens := mail.TokenSource(nil)
	if acct.Auth == "gmail" && acct.Name == e.conf.Mail.Name {
		tokens = e.tokens
	}
	return mail.Dial(acct, tokens)
}

// AddAccount добавляет ящик из UI (preset: yandex|mailru|gmail|exchange|imap).
func (e *Engine) AddAccount(preset, email, password, host, purpose string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return fmt.Errorf("нужны email и пароль приложения")
	}
	if h, ok := accountPresets[preset]; ok {
		host = h
	}
	if host == "" {
		return fmt.Errorf("для своего IMAP укажите хост")
	}
	acct := &store.Account{Name: email, Email: email, Password: password, Host: host, Port: 993}
	// проверяем вход сразу, чтобы не копить нерабочие ящики
	sess, err := mail.Dial(config.Mail{
		Auth: "password", Name: email, IMAPHost: host, IMAPPort: 993,
		Username: email, Password: password,
	}, nil)
	if err != nil {
		return fmt.Errorf("не удалось войти: %w", err)
	}
	sess.Close()

	list, err := e.store.Accounts()
	if err != nil {
		return err
	}
	for _, a := range list {
		if a.Email == email {
			return fmt.Errorf("ящик %s уже добавлен", email)
		}
	}
	if err := e.store.SaveAccounts(append(list, acct)); err != nil {
		return err
	}
	// назначение выбирается сразу: от него зависят умолчания чеков и «наружу»
	if purpose == store.PurposeWork || purpose == store.PurposePersonal {
		c := e.store.MailConf(email)
		c.Purpose = purpose
		if err := e.store.SaveMailConf(email, c); err != nil {
			return err
		}
	}
	e.TriggerPoll()
	return nil
}

func (e *Engine) RemoveAccount(email string) error {
	list, err := e.store.Accounts()
	if err != nil {
		return err
	}
	out := list[:0]
	for _, a := range list {
		if a.Email != email {
			out = append(out, a)
		}
	}
	return e.store.SaveAccounts(out)
}
