package mail

// Куда девается собранное письмо: в «Черновики» ящика или в SMTP.
//
// По умолчанию — в черновики, и это не осторожность ради осторожности.
// Черновик отменяем: человек открывает почту, читает, правит, отправляет
// или удаляет. Отправленное письмо не отменяется ничем; ассистент, который
// однажды отправил не то и не тому, теряет доверие навсегда — а вместе с
// ним теряет смысл всё остальное, что он делает с почтой.
//
// Поэтому SMTP тут есть, но включается настройкой, и по умолчанию она
// стоит на черновике.

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// SMTPConf — куда ходить, чтобы отправить.
type SMTPConf struct {
	Host     string
	Port     int
	Login    string
	Password string
	// НачатьTLS — 587 с STARTTLS (обычный случай) против 465, где TLS с
	// первого байта. Порт сам по себе не приказ: бывают и другие.
	StartTLS bool
}

// Send — письмо в SMTP. Сроки жёсткие: висящее соединение к почтовому
// серверу задерживает очередь Сёмы целиком.
func Send(conf SMTPConf, from string, to []string, msg []byte) error {
	if conf.Host == "" {
		return fmt.Errorf("адрес SMTP-сервера не задан")
	}
	if len(to) == 0 {
		return fmt.Errorf("некому отправлять")
	}
	port := conf.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(conf.Host, fmt.Sprint(port))

	var conn net.Conn
	var err error
	timeout := 45 * time.Second
	if conf.StartTLS {
		conn, err = net.DialTimeout("tcp", addr, timeout)
	} else {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", addr,
			&tls.Config{ServerName: conf.Host, MinVersion: tls.VersionTLS12})
	}
	if err != nil {
		return fmt.Errorf("соединение с %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	defer conn.Close()

	c, err := smtp.NewClient(conn, conf.Host)
	if err != nil {
		return fmt.Errorf("SMTP: %w", err)
	}
	defer c.Close()

	if conf.StartTLS {
		if err := c.StartTLS(&tls.Config{ServerName: conf.Host, MinVersion: tls.VersionTLS12}); err != nil {
			// без шифрования не отправляем: пароль ящика и текст письма
			// ушли бы по сети открытыми
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if conf.Login != "" {
		if err := c.Auth(smtp.PlainAuth("", conf.Login, conf.Password, conf.Host)); err != nil {
			return fmt.Errorf("вход на SMTP: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("передача письма: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("конец письма: %w", err)
	}
	return c.Quit()
}

// ToDrafts — положить письмо в папку черновиков ящика по IMAP. Ничего не
// отправляет: человек увидит письмо в своей почте и решит сам.
func (s *Session) ToDrafts(msg []byte) (string, error) {
	folder := s.DraftsFolder()
	if folder == "" {
		return "", fmt.Errorf("папка черновиков не найдена")
	}
	cmd := s.c.Append(folder, int64(len(msg)), &imap.AppendOptions{
		Flags: []imap.Flag{imap.FlagDraft},
		Time:  time.Now(),
	})
	if _, err := cmd.Write(msg); err != nil {
		_ = cmd.Close()
		return "", fmt.Errorf("запись черновика: %w", err)
	}
	if err := cmd.Close(); err != nil {
		return "", fmt.Errorf("закрытие черновика: %w", err)
	}
	if _, err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("сервер не принял черновик: %w", err)
	}
	return folder, nil
}

// DraftsFolder — папка черновиков по метке сервера, а нет метки — по
// имени. Имена у всех разные («Drafts», «Черновики», «[Gmail]/Черновики»),
// поэтому метка важнее.
func (s *Session) DraftsFolder() string {
	boxes, err := s.Mailboxes()
	if err != nil {
		return ""
	}
	for _, b := range boxes {
		if b.has(imap.MailboxAttrDrafts) {
			return b.Name
		}
	}
	for _, b := range boxes {
		name := strings.ToLower(b.Name)
		if strings.Contains(name, "draft") || strings.Contains(name, "черновик") {
			return b.Name
		}
	}
	return ""
}
