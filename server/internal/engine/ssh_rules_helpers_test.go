package engine

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// rulesHost — сервер, на котором стенды проверяют правила. Правила у каждого
// сервера свои, а у нового их нет вовсе, поэтому команду можно судить только
// «на сервере».
const rulesHost = "правила"

// rulesBench — движок и сервер «правила» с набором Linux: то, что раньше
// давали общие круги всем серверам сразу.
func rulesBench(t testing.TB) (*Engine, *store.Store) {
	t.Helper()
	e, st := newTestEngine(t, "", nil)
	addHost(t, st, rulesHost, store.HostLinux)
	takeSet(t, e, rulesHost, store.RuleSetLinux)
	return e, st
}

// addHost — сервер без правил.
func addHost(t testing.TB, st *store.Store, name, kind string) *store.Host {
	t.Helper()
	h := &store.Host{Name: name, Addr: "127.0.0.1", Port: 22, User: "sema", Key: "ssh:" + name, Kind: kind}
	if err := st.SaveHost(h); err != nil {
		t.Fatalf("сервер %s: %v", name, err)
	}
	return h
}

// takeSet — сервер берёт набор «Linux» или «RouterOS».
func takeSet(t testing.TB, e *Engine, host, set string) {
	t.Helper()
	if _, err := e.TakeRuleSet(host, set); err != nil {
		t.Fatalf("набор %s для %s: %v", set, host, err)
	}
}

// addRules — правила серверу по ответам.
func addRules(t testing.TB, st *store.Store, host, kind string, patterns ...string) {
	t.Helper()
	for _, p := range patterns {
		if err := st.AddHostRule(host, kind, p); err != nil {
			t.Fatalf("правило %s %q для %s: %v", kind, p, host, err)
		}
	}
}

// decideMCP — человек решил про инструменты сервера MCP: правила дописываются к
// тем, что уже есть. Инструмент без решения «новый» и запрещён.
func decideMCP(t testing.TB, st *store.Store, server string, rules map[string]string) {
	t.Helper()
	m, ok := st.MCPServer(server)
	if !ok {
		t.Fatalf("сервера MCP %q нет", server)
	}
	merged := map[string]string{}
	for k, v := range m.Rules {
		merged[k] = v
	}
	for k, v := range rules {
		merged[k] = v
	}
	if err := st.SetMCPRules(server, merged, m.RuleDefault); err != nil {
		t.Fatalf("правила сервера %s: %v", server, err)
	}
}
