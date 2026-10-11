package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGatewayHostMachineFromTableRoutes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "route")
	_ = os.WriteFile(p, []byte("Iface\tDestination\tGateway\tFlags\nеth0\t00000000\t011212AC\t0003\neth0\t000012AC\t00000000\t0001\n"), 0o644)
	if got := hostGatewayFrom(p); got != "172.18.18.1" {
		t.Fatalf("шлюз: %q", got)
	}
}
