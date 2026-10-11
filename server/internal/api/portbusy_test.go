package api

import (
	"errors"
	"strings"
	"testing"
)

func TestBusyPortDiagnostics(t *testing.T) {
	if !PortBusy(errors.New("listen tcp 0.0.0.0:8765: bind: address already in use")) || PortBusy(errors.New("другое")) {
		t.Fatal("занятый порт не распознан")
	}
	m := PortDiagnostic("0.0.0.0:8765", "listen", "/etc/sema/config.yaml", false)
	for _, want := range []string{"0.0.0.0:8765", "listen", "/etc/sema/config.yaml", "8765", "не переезжает"} {
		if !strings.Contains(m, want) {
			t.Errorf("в диагностике нет %q:\n%s", want, m)
		}
	}
	d := PortDiagnostic("0.0.0.0:8765", "listen", "config.yaml", true)
	if !strings.Contains(d, "docker ps") || !strings.Contains(d, "ports:") {
		t.Errorf("в контейнере не назван проброс:\n%s", d)
	}
}
