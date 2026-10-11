package engine

import (
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Машина, на которой работает Сёма. Из контейнера видно немногое, но
// главное видно: температура процессора (/sys/class/thermal) и нагрузка
// (/proc/loadavg) — это хозяйское железо, а не контейнер. Остальное —
// через сервер «хозяин» по SSH, как любой другой.

// MachineStats — что Сёма знает о своей машине без SSH.
type MachineStats struct {
	TempC   float64
	HasTemp bool
	Load1   float64
	Load5   float64
}

// ReadMachine — температура (самая горячая зона) и нагрузка.
func ReadMachine() MachineStats {
	var m MachineStats
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	for _, z := range zones {
		b, err := os.ReadFile(z)
		if err != nil {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil && v > 0 {
			t := v / 1000
			if !m.HasTemp || t > m.TempC {
				m.TempC, m.HasTemp = t, true
			}
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 2 {
			m.Load1, _ = strconv.ParseFloat(f[0], 64)
			m.Load5, _ = strconv.ParseFloat(f[1], 64)
		}
	}
	return m
}

// String — одной строкой для описи и журнала.
func (m MachineStats) String() string {
	parts := []string{}
	if m.HasTemp {
		parts = append(parts, fmt.Sprintf("процессор %.0f °C", m.TempC))
	}
	parts = append(parts, fmt.Sprintf("нагрузка %.2f", m.Load1))
	return strings.Join(parts, " · ")
}

// HostGateway — адрес хозяина из контейнера: шлюз по умолчанию в
// /proc/net/route. Вне контейнера это тоже шлюз, поэтому вызывающий
// решает сам, что подставить (127.0.0.1, если контейнера нет).
func HostGateway() string {
	return hostGatewayFrom("/proc/net/route")
}

func hostGatewayFrom(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		// в /proc/net/route адрес записан младшим байтом вперёд
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String()
	}
	return ""
}
