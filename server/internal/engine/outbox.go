package engine

// Ответ на письмо.
//
// До 10 сентября 2026 Сёма не умела отправлять почту вовсе: SMTP в коде не
// было ни строки. Теперь умеет — но по умолчанию **не отправляет**, а
// кладёт ответ в «Черновики» ящика.
//
// Это настройка, а не гвоздь, и у неё три положения:
//
//	«черновик»  — ответ ложится в папку черновиков, отправляет человек из
//	              своей почты. Так стоит по умолчанию;
//	«по кнопке» — ответ показывается в Сёме, отправка по нажатию;
//	«сама»      — Сёма отправляет названным адресатам без кнопки.
//
// Порядок именно такой, и умолчание именно такое, потому что черновик
// отменяем, а отправленное письмо — нет. Ассистент, однажды отправивший не
// то и не тому, теряет доверие насовсем, и вместе с ним обесценивается всё
// остальное, что он делает с почтой.
//
// Что бы ни было выбрано, письмо собирает и отправляет ядро: пароль ящика
// берётся из хранилища секретов по имени и в текст модели не попадает.

import (
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	sendModeKey  = "mail.send_mode"
	whitelistKey = "mail.send_auto_to"

	ModeDraft  = "черновик"
	ModeButton = "по кнопке"
	ModeSelf   = "сама"
)

// SendMode — что Сёма делает с готовым ответом.
func (e *Engine) SendMode() string {
	m := strings.TrimSpace(e.store.Setting(sendModeKey, ModeDraft))
	switch m {
	case ModeButton, ModeSelf:
		return m
	}
	return ModeDraft
}

func (e *Engine) SetSendMode(m string) error {
	switch strings.TrimSpace(m) {
	case ModeDraft, ModeButton, ModeSelf:
		return e.store.SetSetting(sendModeKey, strings.TrimSpace(m))
	}
	return fmt.Errorf("непонятный режим отправки: %q", m)
}

// SendAutoTo — кому Сёма вправе отвечать сама. Пустой список в режиме
// «сама» означает «никому»: режим включён, а адресатов не назвали — это
// недонастроенность, и трактовать её как «всем» нельзя.
func (e *Engine) SendAutoTo() []string {
	var out []string
	for _, s := range strings.Split(e.store.Setting(whitelistKey, ""), ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (e *Engine) SetSendAutoTo(list string) error {
	return e.store.SetSetting(whitelistKey, strings.TrimSpace(list))
}

// CanSendItself — вправе ли Сёма отправить это письмо без кнопки.
func (e *Engine) CanSendItself(to []string) bool {
	if e.SendMode() != ModeSelf {
		return false
	}
	allowed := e.SendAutoTo()
	if len(allowed) == 0 {
		return false
	}
	for _, a := range to {
		a = strings.ToLower(strings.TrimSpace(a))
		res := false
		for _, r := range allowed {
			if a == r || (strings.HasPrefix(r, "@") && strings.HasSuffix(a, r)) {
				res = true
				break
			}
		}
		if !res {
			// хоть один адресат вне списка — письмо целиком ждёт человека
			return false
		}
	}
	return true
}

// ReplyOutcome — что случилось с ответом.
type ReplyOutcome struct {
	Where   string // «черновики» | «отправлено» | «ждёт кнопки»
	Folder  string // папка черновиков, если ответ лёг туда
	To      []string
	Subject string
	Message []byte
}

// ReplyToMessage — собрать ответ и поступить с ним по настройке.
// `сейчас` — «человек нажал кнопку»: тогда отправляем, даже если режим
// «по кнопке». Из чата и из задач сюда приходит `false`.
func (e *Engine) ReplyToMessage(id int64, text string, now bool) (*ReplyOutcome, error) {
	m, err := e.store.Message(id)
	if err != nil {
		return nil, fmt.Errorf("письма %d нет", id)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("пустой ответ отправлять некуда")
	}
	acct, ok := e.accountByName(e.accountOr(m.Account))
	if !ok {
		return nil, fmt.Errorf("ящик %q не настроен", m.Account)
	}
	to := replyRecipients(m, acct.Username)
	if len(to) == 0 {
		return nil, fmt.Errorf("некому отвечать: у письма нет обратного адреса")
	}
	message, err := mail.Build(mail.Message{
		From: acct.Username, FromName: acct.Name,
		To: to, Subject: m.Subject, Text: text,
		InReplyTo: m.MessageID, References: strings.Fields(m.Refs),
		Date: time.Now(),
	})
	if err != nil {
		return nil, err
	}
	outcome := &ReplyOutcome{To: to, Subject: m.Subject, Message: message}

	send := now || e.CanSendItself(to)
	if !send && e.SendMode() == ModeDraft {
		folder, err := e.inDrafts(acct, message)
		if err != nil {
			return nil, err
		}
		outcome.Where, outcome.Folder = "черновики", folder
		e.saveOutcome(m, outcome)
		return outcome, nil
	}
	if !send {
		outcome.Where = "ждёт кнопки"
		return outcome, nil
	}
	k, err := e.smtpConf(acct.Name)
	if err != nil {
		return nil, err
	}
	if err := mail.Send(k, acct.Username, to, message); err != nil {
		return nil, err
	}
	outcome.Where = "отправлено"
	e.saveOutcome(m, outcome)
	return outcome, nil
}

// replyRecipients — кому уходит ответ. `Reply-To` весомее `From`: рассылки
// нарочно ставят его, чтобы ответ попал живому человеку. Свой же адрес из
// списка вычёркиваем — иначе Сёма пишет сама себе.
func replyRecipients(m *store.Message, own string) []string {
	candidates := []string{m.ReplyTo, m.FromAddr}
	var out []string
	for _, a := range candidates {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || a == strings.ToLower(own) {
			continue
		}
		out = append(out, a)
		break
	}
	return out
}

// saveOutcome — ответ живёт в архиве Сёмы как обычное исходящее письмо:
// иначе «есть ли наш ответ» снова станет угадыванием, а вся переписка
// будет видна только наполовину.
func (e *Engine) saveOutcome(original *store.Message, i *ReplyOutcome) {
	our := &store.Message{
		Account: original.Account, MessageID: mail.NewID(original.Account),
		FromAddr: original.Account, Subject: i.Subject, Date: time.Now(),
		Status: store.StatusArchived, Direction: "out",
		Reason:    "ответ Сёмы · " + i.Where,
		InReplyTo: strings.Trim(original.MessageID, "<>"),
		To:        strings.Join(i.To, " "),
		Snippet:   clip(messageText(i.Message), 300),
	}
	if err := e.store.InsertMessage(our); err != nil {
		e.LastErr = "ответ не записан в архив: " + err.Error()
	}
}

// messageText — тело собранного письма для превью: всё после пустой
// строки, до конца.
func messageText(message []byte) string {
	s := string(message)
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		return strings.TrimSpace(s[i+4:])
	}
	return ""
}

// smtpConf — куда ходить, чтобы отправить из этого ящика. Пароль берётся
// из хранилища секретов по имени: значение секрета не должно лежать в
// настройках ящика и не попадает ни в описание экрана, ни в промпт.
func (e *Engine) smtpConf(account string) (mail.SMTPConf, error) {
	c := e.MailConf(account)
	if c == nil {
		return mail.SMTPConf{}, fmt.Errorf("ящик %q не настроен", account)
	}
	if c.SMTPHost == "" {
		return mail.SMTPConf{}, fmt.Errorf("для ящика %q не задан SMTP-сервер", account)
	}
	k := mail.SMTPConf{
		Host: c.SMTPHost, Port: c.SMTPPort, Login: c.SMTPUser,
		StartTLS: c.SMTPStartTLS,
	}
	if k.Login == "" {
		k.Login = c.Username
	}
	if c.SMTPSecret != "" {
		password, has := e.coreSecret(c.SMTPSecret, "почта · отправка")
		if !has {
			return mail.SMTPConf{}, fmt.Errorf("секрет %q не читается: заведите его в «Ключи и секреты»", c.SMTPSecret)
		}
		k.Password = password
	} else {
		k.Password = c.Password
	}
	if k.Password == "" {
		return mail.SMTPConf{}, fmt.Errorf("нет пароля для отправки из %q", account)
	}
	return k, nil
}

// inDrafts — положить ответ в папку черновиков ящика.
func (e *Engine) inDrafts(acct config.Mail, message []byte) (string, error) {
	sess, _, err := e.dialConfigured(acct.Name)
	if err != nil {
		return "", err
	}
	defer sess.Close()
	return sess.ToDrafts(message)
}

// SendReady — можно ли вообще отправлять из этого ящика. Экран
// спрашивает это, чтобы не показывать кнопку, которая заведомо откажет.
func (e *Engine) SendReady(account string) (bool, string) {
	if _, err := e.smtpConf(e.accountOr(account)); err != nil {
		return false, err.Error()
	}
	return true, ""
}
