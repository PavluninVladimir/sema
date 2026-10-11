package api

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"syscall"
)

// PortBusy — ошибка привязки означает занятый адрес.
func PortBusy(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	// на Windows ошибка звучит иначе: «Only one usage of each socket address…»
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") || strings.Contains(msg, "only one usage of each socket address")
}

// PortDiagnostic — занятый порт: отказ, а не переезд. Порт входит в адрес
// входа, и самовольный переезд сломал бы вход, закладки и адрес в Sema.app.
// Сообщение говорит, что занято, где это настраивается и чем посмотреть,
// кто держит порт — под каждую систему; в контейнере — отдельный случай
// проброса на хозяине.
func PortDiagnostic(addr, settingName, confPath string, inContainer bool) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = addr
	}
	var b strings.Builder
	fmt.Fprintf(&b, "адрес %s уже занят — Сёма не переезжает на другой порт сама: он входит в адрес входа и в адрес у приложения.\n", addr)
	fmt.Fprintf(&b, "Настройка: %s в файле запуска %s (или переменная SEMA_LISTEN, или -listen).\n", settingName, confPath)
	b.WriteString("Кто держит порт:\n")
	switch {
	case inContainer:
		fmt.Fprintf(&b, "  внутри контейнера: другой процесс Сёмы в этом же контейнере — docker exec sema ps\n")
		fmt.Fprintf(&b, "  проброс на хозяине: docker ps --format '{{.Names}} {{.Ports}}' | grep %s — второй контейнер с тем же портом; правится ports: в compose или -p у docker run\n", port)
	case runtime.GOOS == "darwin":
		fmt.Fprintf(&b, "  lsof -nP -iTCP:%s -sTCP:LISTEN\n", port)
	case runtime.GOOS == "windows":
		fmt.Fprintf(&b, "  netstat -ano | findstr :%s   (последний столбец — PID; tasklist /FI \"PID eq <PID>\")\n", port)
	default:
		fmt.Fprintf(&b, "  ss -ltnp 'sport = :%s'   (или lsof -nP -iTCP:%s -sTCP:LISTEN)\n", port, port)
		fmt.Fprintf(&b, "  Docker на этой машине: docker ps --format '{{.Names}} {{.Ports}}' | grep %s\n", port)
	}
	b.WriteString("Чаще всего это вторая Сёма: остановите её, а не меняйте порт.")
	return b.String()
}
