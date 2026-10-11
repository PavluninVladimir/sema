package llm

// Сетевая беда — не поломка модели и не вина работы. Отличать их надо в
// одном месте: очередь по сетевой ошибке откладывает, а не сжигает
// попытку; роль не уходит на передышку; модуль не выключается; человек
// читает «нет связи», а не «поменяйте модель».

import (
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// IsNetworkErr — до модели не дошли вовсе: имя не разрешилось, соединение
// не открылось, оборвалось по дороге. Ответ модели с кодом 5xx сюда не
// относится — там связь как раз была.
//
// «Истёк срок» нарочно не считается сетевой ошибкой: медленная модель и
// пропавший Wi-Fi выглядят одинаково, а решения у них разные — за
// медленную отвечает передышка роли.
func IsNetworkErr(err error) bool {
	if err == nil {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	// провайдеры заворачивают ошибки в свой текст: остаётся смотреть слова
	s := strings.ToLower(err.Error())
	for _, w := range []string{
		"no such host", "connection refused", "no route to host", "network is unreachable",
		"connection reset", "tls handshake timeout", "dial tcp", "unexpected eof",
		"server misbehaving", "temporary failure in name resolution",
	} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
