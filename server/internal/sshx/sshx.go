// Package sshx — одна команда на сервере по ключу. Чистый Go
// (golang.org/x/crypto/ssh): работает и на Raspberry Pi, и в Docker без
// системного ssh. Одно соединение на команду: команд мало, а держать
// открытые сессии к серверам незачем.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type Target struct {
	Addr string
	Port int
	User string
}

type Result struct {
	Output      string
	Code        int
	Fingerprint string // отпечаток хоста, с которым соединились
	Duration    time.Duration
	Truncated   bool
}

// ErrFingerprint — хост предъявил не тот ключ, что запомнен.
type ErrFingerprint struct{ Known, Seen string }

func (e *ErrFingerprint) Error() string {
	return "отпечаток хоста сменился: было " + e.Known + ", стало " + e.Seen
}

// ErrUnreachable — до сервера не дошли: соединение не установилось (порт
// закрыт, фильтр на роутере, нет маршрута, молчание до срока). Отличается от
// отказа после соединения тем, что следующая команда на тот же адрес упадёт
// так же, — и звать её незачем. Различать надо по типу, а не по тексту: текст
// уходит в журнал и переводится по мере правок.
type ErrUnreachable struct {
	Addr string
	Err  error
}

func (e *ErrUnreachable) Error() string {
	return "нет связи с " + e.Addr + ": " + e.Err.Error()
}

func (e *ErrUnreachable) Unwrap() error { return e.Err }

// Run выполняет команду. known — принятый отпечаток (пусто — первый вход,
// принимаем и возвращаем в Result). limit — предел размера вывода в байтах.
func Run(ctx context.Context, t Target, privatePEM, known, cmd string, limit int) (Result, error) {
	var res Result
	signer, err := ssh.ParsePrivateKey([]byte(privatePEM))
	if err != nil {
		return res, fmt.Errorf("ключ не разобран: %w", err)
	}
	seen := ""
	cfg := &ssh.ClientConfig{
		User: t.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			seen = ssh.FingerprintSHA256(key)
			if known != "" && known != seen {
				return &ErrFingerprint{Known: known, Seen: seen}
			}
			return nil
		},
		Timeout: 15 * time.Second,
	}
	port := t.Port
	if port <= 0 {
		port = 22
	}
	addr := net.JoinHostPort(t.Addr, strconv.Itoa(port))
	started := time.Now()

	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return res, &ErrUnreachable{Addr: addr, Err: err}
	}
	// DialContext ограничивал только TCP: молчащий peer держал handshake
	// и открытие канала без срока. Отмена закрывает всё соединение.
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	deadline := time.Now().Add(cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return res, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		var fp *ErrFingerprint
		if errors.As(err, &fp) {
			return res, fp
		}
		return res, fmt.Errorf("вход на %s не удался: %w", addr, err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	res.Fingerprint = seen

	sess, err := client.NewSession()
	if err != nil {
		return res, err
	}
	defer sess.Close()
	// Рукопожатие и открытие канала закончились; длительность команды
	// задаётся её контекстом, а не коротким сроком подключения.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return res, err
	}
	var out bytes.Buffer
	sess.Stdout = &limitedWriter{buf: &out, limit: limit, truncated: &res.Truncated}
	sess.Stderr = sess.Stdout

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = client.Close()
		<-done // больше нет писателей stdout/stderr перед чтением буфера
		res.Output = out.String()
		res.Duration = time.Since(started)
		return res, fmt.Errorf("команда не уложилась в срок и была прервана")
	case err = <-done:
	}
	res.Duration = time.Since(started)
	res.Output = out.String()
	if res.Truncated {
		res.Output += "\n… вывод обрезан по пределу"
	}
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.Code = exitErr.ExitStatus()
	default:
		return res, err
	}
	return res, nil
}

// limitedWriter пишет до предела и помечает обрезание: чужой сервер может
// вылить гигабайт, а нам нужно показать модели разумный кусок.
type limitedWriter struct {
	mu        sync.Mutex
	buf       *bytes.Buffer
	limit     int
	truncated *bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p) // отчитываемся за всё: иначе io.Copy решит, что запись неполная (short write)
	room := w.limit - w.buf.Len()
	if room <= 0 {
		*w.truncated = true
		return n, nil
	}
	if len(p) > room {
		*w.truncated = true
		p = p[:room]
	}
	w.buf.Write(p)
	return n, nil
}
