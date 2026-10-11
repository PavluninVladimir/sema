package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Соединение не установилось — ошибка узнаётся по типу, а текст остаётся
// прежним: он пишется в журнал команд и по нему человек ищет причину. Порт
// закрывается на месте (listen, потом close), так что ответом будет отказ
// в соединении и сети для проверки не нужно.
func TestRefusedConnectionIsUnreachable(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	p, _ := strconv.Atoi(port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = Run(ctx, Target{Addr: host, Port: p, User: "test"}, string(pem.EncodeToMemory(block)), "", "uptime", 100)
	var down *ErrUnreachable
	if !errors.As(err, &down) {
		t.Fatalf("отказ в соединении не узнаётся по типу: %T %v", err, err)
	}
	if down.Addr != net.JoinHostPort(host, port) {
		t.Fatalf("адрес в ошибке: %q", down.Addr)
	}
	// прежний текст: «нет связи с <адрес>: <что сказала сеть>»
	if want := "нет связи с " + down.Addr + ": "; !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("текст ошибки изменился: %q", err.Error())
	}
	if !strings.HasSuffix(err.Error(), down.Err.Error()) {
		t.Fatalf("в тексте нет ответа сети: %q", err.Error())
	}
	// причина не потеряна: сетевую ошибку можно достать из-под обёртки
	var op *net.OpError
	if !errors.As(err, &op) {
		t.Fatalf("под ErrUnreachable нет исходной сетевой ошибки: %v", err)
	}
	// а ошибка другого рода недоступностью не считается
	var fp error = &ErrFingerprint{Known: "a", Seen: "b"}
	if errors.As(fp, &down) {
		t.Fatal("смена отпечатка принята за недоступность")
	}
}
