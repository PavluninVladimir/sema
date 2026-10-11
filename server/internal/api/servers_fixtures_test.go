package api

// Данные для экранов «Серверов»: четыре-пять машин разного рода и уровня,
// команды, ждущие кнопки, команда без правила, записи журнала. Их берут и
// юнит-тесты, и браузерные, и снимки для ревью.

import (
	"fmt"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// serversFix — сервер с машинами. court — собран ли суд: у собранного обе роли
// назначены разным моделям, и «под судом» и «полная» доступны; у несобранного
// они серые, и экран называет причину.
func serversFix(t *testing.T, court bool) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	if court {
		if err := s.eng.AddModel(engine.ModelConf{Name: "вторая", Type: "openai",
			BaseURL: "http://127.0.0.1:1/v1", Model: "test2"}); err != nil {
			t.Fatal(err)
		}
		if err := s.eng.SetRoleField(engine.RoleArbiter, "model", "тестовая"); err != nil {
			t.Fatal(err)
		}
		if err := s.eng.SetRoleField(engine.RoleJudge, "model", "вторая"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ssh:Microtik", "ssh:srv-vpn"} {
		if err := st.SaveSecret(&store.Secret{Name: name, Kind: store.SecretSSH,
			Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIпроба " + name}, "приватная часть "+name); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	hosts := []*store.Host{
		{Name: "srv-bots", Addr: "203.0.113.26", Port: 22, User: "sema", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Bx3HQBtR2m9kXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTM", NewFP: "SHA256:Zk81pQ0mVxA2nTe5LwR7HcYd3GiUo9sFbJ6XvNq4aE0",
			Autonomy: store.AutonomyFull, Window: "02:00-04:00", LastCmd: "ps aux --sort=-%cpu | head -n 8", LastAt: ago(5 * time.Hour)},
		{Name: "Microtik", Addr: "192.168.1.1", Port: 22, User: "sema", Key: "ssh:Microtik", Kind: store.HostRouterOS,
			Fingerprint: "SHA256:Cq9mXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTMBx3HQBtR",
			Autonomy:    store.AutonomyFull, Window: "02:00-04:00", LastCmd: "/system script print detail", LastAt: ago(90 * time.Minute)},
		{Name: "srv-vpn", Addr: "203.0.113.12", Port: 22, User: "sema", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Bx3HQBtR2m9kXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTM",
			Autonomy:    store.AutonomyFull, Window: "02:00-06:00", MaxHour: 5, LastCmd: "df -h", LastAt: ago(5 * time.Hour)},
		{Name: "WireGuard", Addr: "203.0.113.40", Port: 2222, User: "sema", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Dd8mXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTMBx3HQBtR",
			Autonomy:    store.AutonomyCourt, LastCmd: "uptime", LastAt: ago(26 * time.Hour)},
		{Name: engine.HostOwnerName, Addr: "172.18.0.1", Port: 22, User: "ivan", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Ee7mXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTMBx3HQBtR", NoSudo: true},
	}
	for _, h := range hosts {
		if err := st.SaveHost(h); err != nil {
			t.Fatal(err)
		}
		set := store.RuleSetLinux
		if h.Kind == store.HostRouterOS {
			set = store.RuleSetRouterOS
		}
		if _, err := s.eng.TakeRuleSet(h.Name, set); err != nil {
			t.Fatal(err)
		}
	}
	// команды на кнопке: одна и та же на двух серверах (свёрнута в группу) и одна своя
	for _, p := range []*store.SSHPending{
		{Host: "srv-bots", Cmd: "sudo apt-get upgrade -y", Who: "модуль «Серверы под присмотром»", Created: ago(2 * time.Hour)},
		{Host: "srv-vpn", Cmd: "sudo apt-get upgrade -y", Who: "модуль «Серверы под присмотром»", Created: ago(2 * time.Hour)},
		{Host: "srv-vpn", Cmd: "systemctl restart wg-quick@wg0", Who: "чат «Серверы»", Created: ago(40 * time.Minute)},
	} {
		if _, err := st.AddSSHPending(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.AddSSHNoRule(&store.SSHNoRule{Host: engine.HostOwnerName, Cmd: "docker stats --no-stream", Who: "чат «Серверы»",
		Why: "ни одно правило сервера не подошло", CanAllow: true, Created: ago(25 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	// журнал: разные решения, ночная смена и «сама»
	for i, e := range []store.SSHLogEntry{
		{Host: "Microtik", Cmd: "/system script print detail", Who: "чат «Серверы»", Decision: store.SSHExecuted, Millis: 800, Size: 4100, Output: "name=backup"},
		{Host: "srv-bots", Cmd: "ps aux --sort=-%cpu | head -n 8", Who: "ночная смена · srv-bots · осмотр", Decision: store.SSHExecuted, Millis: 400, Size: 1200, Output: "USER PID"},
		{Host: "srv-vpn", Cmd: "rm -rf /var/log/old", Who: "чат «Уведомление · srv-vpn»", Decision: store.SSHDenied, Output: "правило «Запретить»: rm -rf"},
		{Host: "srv-vpn", Cmd: "systemctl restart wg-quick@wg0", Who: "ночная смена · srv-vpn", Decision: store.SSHAuto, Millis: 1100, Size: 0, Output: "суд: арбитр → судья → проба сошлась"},
		{Host: "srv-vpn", Cmd: "df -h", Who: "ночная смена · srv-vpn", Decision: store.SSHExecuted, Millis: 300, Size: 800, Output: "Filesystem"},
		{Host: engine.HostOwnerName, Cmd: "uptime", Who: "чат «Серверы»", Decision: store.SSHExecuted, Millis: 100, Size: 60, Output: "up 12 days"},
	} {
		e.At = ago(time.Duration(len(hosts)-i) * time.Hour)
		st.LogSSH(e)
	}
	return s, st
}

// serversManyLog — ещё записей в журнал: чтобы проверить первую страницу и «Показать ещё».
func serversManyLog(t *testing.T, st *store.Store, host string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		st.LogSSH(store.SSHLogEntry{Host: host, Cmd: fmt.Sprintf("echo %d", i), Who: "чат «Серверы»",
			Decision: store.SSHExecuted, Millis: 100, Size: 10, Output: "ok",
			At: time.Now().Add(-time.Duration(i+10) * time.Minute).Format(time.RFC3339)})
	}
}
