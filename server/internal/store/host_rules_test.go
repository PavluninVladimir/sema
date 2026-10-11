package store

import (
	"strings"
	"testing"
)

// oldCircles — общие круги прежнего вида, как они лежали в живой базе.
func oldCircles(t *testing.T, s *Store) {
	t.Helper()
	rows := map[string][]string{
		"read":  {"uptime", "df", "ip", "/* print*", "/system resource print", "cat /var/run/reboot-required"},
		"human": {"userdel", "sshd_config", "ufw", "/user", "/ip firewall"},
		"never": {"rm -rf", "reboot", "/file remove", "/system upgrade"},
	}
	for kind, list := range rows {
		for _, p := range list {
			if _, err := s.db.Exec(`INSERT INTO ssh_rules (kind, pattern) VALUES (?, ?)`, kind, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, h := range []*Host{
		{Name: "лин", Addr: "1.1.1.1", User: "u", Key: "k", Kind: HostLinux},
		{Name: "рос", Addr: "2.2.2.2", User: "u", Key: "k", Kind: HostRouterOS},
		{Name: "кто", Addr: "3.3.3.3", User: "u", Key: "k"},
	} {
		if err := s.SaveHost(h); err != nil {
			t.Fatal(err)
		}
	}
	// как до выката: разложенного ещё нет
	for _, q := range []string{`DELETE FROM host_rules`, `DELETE FROM ssh_rule_sets`, `DELETE FROM settings WHERE key = 'ssh.host_rules_migrated'`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func has(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}

// Переезд общих кругов на серверы: по роду, «никогда» — всем, неизвестный род —
// всё. Круг чтения становится «Разрешить», «только человек» — «Спросить»,
// «никогда» — «Запретить».
func TestLayOutSharedRulesByHostKind(t *testing.T) {
	s := openTestStore(t)
	oldCircles(t, s)
	if err := s.layOutSharedRules(); err != nil {
		t.Fatal(err)
	}
	lin, ros, any := s.HostRuleSet("лин"), s.HostRuleSet("рос"), s.HostRuleSet("кто")

	// Linux: всё, что не начинается с «/», плюс все «никогда»
	if !has(lin.Allow, "uptime") || !has(lin.Allow, "cat /var/run/reboot-required") || has(lin.Allow, "/system resource print") {
		t.Errorf("«Разрешить» у Linux: %v", lin.Allow)
	}
	if !has(lin.Ask, "sshd_config") || has(lin.Ask, "/user") {
		t.Errorf("«Спросить» у Linux: %v", lin.Ask)
	}
	if len(lin.Deny) != 4 {
		t.Errorf("«Запретить» у Linux: %v", lin.Deny)
	}
	// RouterOS: образцы с «/», плюс все «никогда»
	for _, p := range ros.Allow {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("в «Разрешить» RouterOS попал образец Linux %q", p)
		}
	}
	if len(ros.Allow) != 2 || !has(ros.Ask, "/user") || !has(ros.Ask, "/ip firewall") || len(ros.Ask) != 2 {
		t.Errorf("RouterOS: %v / %v", ros.Allow, ros.Ask)
	}
	if !has(ros.Deny, "rm -rf") || !has(ros.Deny, "/system upgrade") || len(ros.Deny) != 4 {
		t.Errorf("«Запретить» у RouterOS — все «никогда»: %v", ros.Deny)
	}
	// род неизвестен — всё
	if len(any.Allow) != 6 || len(any.Ask) != 5 || len(any.Deny) != 4 {
		t.Errorf("неизвестный род получил не всё: %d / %d / %d", len(any.Allow), len(any.Ask), len(any.Deny))
	}
	// наборы заполнены живыми образцами
	if set := s.RuleSetOf(RuleSetLinux); !has(set.Allow, "df") || has(set.Allow, "/* print*") {
		t.Errorf("набор Linux: %v", set.Allow)
	}
	if set := s.RuleSetOf(RuleSetRouterOS); !has(set.Allow, "/* print*") || !has(set.Deny, "reboot") {
		t.Errorf("набор RouterOS: %v / %v", set.Allow, set.Deny)
	}
}

// Повтор безопасен: убранное человеком не возвращается, добавленное остаётся, а
// сервер, заведённый после переезда, начинает без правил.
func TestLayOutSharedRulesRepeatSafe(t *testing.T) {
	s := openTestStore(t)
	oldCircles(t, s)
	if err := s.layOutSharedRules(); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHostRulePattern("лин", RuleAllow, "uptime"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHostRule("лин", RuleAsk, "мой-образец"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHost(&Host{Name: "новый", Addr: "4.4.4.4", User: "u", Key: "k", Kind: HostLinux}); err != nil {
		t.Fatal(err)
	}
	if err := s.layOutSharedRules(); err != nil {
		t.Fatal(err)
	}
	lin := s.HostRuleSet("лин")
	if has(lin.Allow, "uptime") {
		t.Error("повторный переезд вернул убранное человеком")
	}
	if !has(lin.Ask, "мой-образец") {
		t.Error("повторный переезд стёр добавленное человеком")
	}
	if !s.HostRuleSet("новый").Empty() {
		t.Error("сервер, заведённый после переезда, получил правила: у нового сервера их нет")
	}
	// и даже без метки повтор не плодит дубли: образец и ответ уникальны
	if _, err := s.db.Exec(`DELETE FROM settings WHERE key = 'ssh.host_rules_migrated'`); err != nil {
		t.Fatal(err)
	}
	before := s.HostRuleSet("рос").Count()
	if err := s.layOutSharedRules(); err != nil {
		t.Fatal(err)
	}
	if after := s.HostRuleSet("рос").Count(); after != before {
		t.Errorf("повтор без метки изменил число правил: %d → %d", before, after)
	}
}

// Удаление сервера уносит его правила и запросы на правила.
func TestDeleteHostDropsRules(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveHost(&Host{Name: "а", Addr: "1.1.1.1", User: "u", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHostRule("а", RuleAllow, "uptime"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddSSHNoRule(&SSHNoRule{Host: "а", Cmd: "reboot now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHost("а"); err != nil {
		t.Fatal(err)
	}
	if !s.HostRuleSet("а").Empty() || len(s.SSHNoRuleList(10)) != 0 {
		t.Error("правила удалённого сервера остались")
	}
	// правило нельзя завести серверу, которого нет
	if err := s.AddHostRule("а", RuleAllow, "uptime"); err == nil {
		t.Error("правило заведено несуществующему серверу")
	}
	if err := s.AddHostRule("б", "можно", "x"); err == nil {
		t.Error("ответ не из трёх принят")
	}
}

// Запрос на правило не плодится: та же команда на том же сервере, что ещё ждёт
// решения, остаётся одной записью.
func TestNoRuleRequestNotDuplicated(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveHost(&Host{Name: "а", Addr: "1.1.1.1", User: "u", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	id1, fresh1, _ := s.AddSSHNoRule(&SSHNoRule{Host: "а", Cmd: "docker restart web"})
	id2, fresh2, _ := s.AddSSHNoRule(&SSHNoRule{Host: "а", Cmd: " docker restart web ", ChatID: 7})
	if id1 != id2 || !fresh1 || fresh2 {
		t.Fatalf("дубль запроса: %d %v / %d %v", id1, fresh1, id2, fresh2)
	}
	if n, _ := s.SSHNoRuleByID(id1); n.ChatID != 7 {
		t.Errorf("повтор из чата не привязал чат к запросу: %+v", n)
	}
	if err := s.DecideSSHNoRule(id1, NoRuleAsk); err != nil {
		t.Fatal(err)
	}
	if id3, fresh3, _ := s.AddSSHNoRule(&SSHNoRule{Host: "а", Cmd: "docker restart web"}); id3 == id1 || !fresh3 {
		t.Errorf("после решения запрос не завёлся заново: %d %v", id3, fresh3)
	}
}
