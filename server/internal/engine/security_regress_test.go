package engine

import (
	"context"
	"strconv"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Аудит 7 сентября 2026: команды, которые меняют сервер, читались как
// «чтение» — подстановки, фон, переводы строк, изменяющие ключи.
func TestHiddenCommandsNotRead(t *testing.T) {
	e, _ := rulesBench(t)
	for _, cmd := range []string{
		"echo $(touch /tmp/метка)",
		"echo `touch /tmp/метка`",
		"echo ok\ntouch /tmp/метка",
		"echo ok & touch /tmp/метка",
		"awk 'BEGIN {system(\"touch /tmp/метка\")}'",
		"find /tmp/проба -delete",
		"sort -o /tmp/вывод /tmp/ввод",
		"sysctl -w kernel.hostname=audit",
		"ip link set dev audit0 down",
		"mount -o remount,rw /audit",
		"sed -i 's/a/b/' /etc/hosts",
		"echo \"it's $(touch /tmp/x)\"",
		"cat <(touch /tmp/x)",
		"echo audit >&/tmp/security-audit-marker",
		"echo audit >& /tmp/security-audit-marker",
		"echo $(echo $(touch /tmp/x))",
		"sysctl kernel.hostname=audit",
		"sysctl '-w' kernel.hostname=audit",
		"sort -ro/tmp/out /tmp/in",
		"sort --compress-program=/tmp/program /tmp/in",
		"ip link s dev eth0 down",
		"ip addr show -batch /tmp/commands",
		"awk 'BEGIN {system (\"touch /tmp/x\")}'",
		"awk '@load \"/tmp/plugin\"'",
		"sed -n '1e touch /tmp/x' /etc/hosts",
		"hostname audit",
		"date -s 20260908",
		"dmesg -C",
		"journalctl --vacuum-time=1s",
		"/system resource print file=audit",
		"apt-get -s -o 'APT::Update::Pre-Invoke=touch /tmp/x' update",
	} {
		if policy, why := e.Classify(rulesHost, cmd); policy == PolicyAllow {
			t.Errorf("%q прочиталось как чтение (%s)", cmd, why)
		}
	}
	// читающие остались читающими
	for _, cmd := range []string{"cat /var/log/syslog 2>&1 | tail -n 5", "ip addr", "sysctl kernel.hostname", "find /tmp -name '*.log'", "awk '{print $1}' /etc/passwd"} {
		if policy, why := e.Classify(rulesHost, cmd); policy != PolicyAllow {
			t.Errorf("%q перестало быть чтением: %s (%s)", cmd, policy, why)
		}
	}
}

// Неясный синтаксис правилом «Разрешить» не становится никогда, а суду не
// отдаётся, пока сервер не отдан Сёме целиком: судить то, чего не разобрал
// даже разбор команды, нельзя.
func TestAmbiguousShellNeedsHuman(t *testing.T) {
	e, st := rulesBench(t)
	h, _ := st.Host(rulesHost)
	h.Auto, h.Autonomy = true, store.AutonomyCourt
	for _, cmd := range []string{"echo $(echo $(touch /tmp/x))", "sysctl kernel.hostname=audit", "sort -ro/tmp/out /tmp/in", "awk 'BEGIN {system (\"id\")}'", "ip -batch /tmp/commands"} {
		r := e.Decide(rulesHost, cmd)
		if r.Policy == PolicyAllow || r.Unsafe == "" {
			t.Errorf("неясную команду %q можно разрешить: %s, unsafe=%q", cmd, r.Policy, r.Unsafe)
		}
		if ok, _ := e.courtAllowed(h, cmd, SSHOrigin{Who: "чат"}); ok {
			t.Errorf("неясная команда %q пошла на суд", cmd)
		}
	}
}

func TestScriptsCannotReachInternalAPI(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	for _, raw := range []string{"http://127.0.0.1:8765/api/auth/handoff", "http://169.254.169.254/latest/meta-data", "http://[::ffff:127.0.0.1]:8765/api/ui"} {
		res := e.RunScript(context.Background(), "sema.http.get("+strconv.Quote(raw)+")")
		if res.Err == nil {
			t.Errorf("скрипт обратился к %s", raw)
		}
	}
}

// Права и включённость в манифесте ничего не значат: их даёт человек.
func TestManifestGrantsNothingItself(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.AddSecret("ключ", store.SecretToken, "значение"); err != nil {
		t.Fatal(err)
	}
	m, err := ParseModule(`{"name":"самозванец","enabled":true,"secrets":["ключ"],"granted":["ключ"]}
---
function audit() { return sema.secret("ключ"); }`)
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled || len(m.Granted) != 0 {
		t.Fatalf("манифест выдал себе права: enabled=%v granted=%v", m.Enabled, m.Granted)
	}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	res, err := e.RunModule(context.Background(), m.Name, "audit()")
	if err == nil && res.Err == nil && res.Value == "значение" {
		t.Fatal("модуль получил секрет без выдачи человеком")
	}
}

// Модуль без объявленных прав не получает почту и сеть «по умолчанию».
func TestModuleWithoutGrantsWithoutTools(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &Module{Name: "пустые-права", Code: `function audit() { var api = sema; return typeof api.messages + ":" + typeof (api.http && api.http.get); }`}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	res, err := e.RunModule(context.Background(), m.Name, "audit()")
	if err != nil {
		t.Fatal(err)
	}
	if res.Err == nil && res.Value == "function:function" {
		t.Fatal("модуль без прав получил почту и сеть")
	}
}
