package llm

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
)

// Сетевую беду надо отличать от поломки модели в одном месте — от этого
// зависит, сгорит ли попытка в очереди и уйдёт ли роль на передышку.
func TestNetworkError(t *testing.T) {
	network := []error{
		&net.DNSError{Err: "no such host", Name: "api.deepseek.com"},
		fmt.Errorf("Post \"https://x\": dial tcp 1.2.3.4:443: connect: connection refused"),
		fmt.Errorf("обёртка: %w", syscall.EHOSTUNREACH),
		io.ErrUnexpectedEOF,
		&net.OpError{Op: "dial", Err: errors.New("no route to host")},
		fmt.Errorf("Get \"https://x\": net/http: TLS handshake timeout"),
	}
	for _, err := range network {
		if !IsNetworkErr(err) {
			t.Errorf("не узнана сетевая ошибка: %v", err)
		}
	}
	notNetwork := []error{
		nil,
		fmt.Errorf("HTTP 500: internal error"),
		fmt.Errorf("HTTP 429: rate limit"),
		fmt.Errorf("ключ не подошёл"),
		fmt.Errorf("context deadline exceeded"), // медленная модель — не сеть
	}
	for _, err := range notNetwork {
		if IsNetworkErr(err) {
			t.Errorf("обычная ошибка принята за сетевую: %v", err)
		}
	}
}
