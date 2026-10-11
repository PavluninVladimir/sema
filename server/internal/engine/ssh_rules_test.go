package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, part string
		want          bool
	}{
		{"/* print*", "/system resource print", true},
		{"/* print*", "/interface wireguard peers print detail", true},
		{"/* print*", "/system reboot", false},
		{"/* print*", "/system reset-configuration", false},
		{"apt *", "apt list --upgradable", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.part); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.part, got, c.want)
		}
	}
}

func TestRedirectsToFile(t *testing.T) {
	cases := map[string]bool{
		"apt list --upgradable 2>/dev/null":        false,
		"ls /var/run/reboot-required* 2>/dev/null": false,
		"echo x > /etc/passwd":                     true,
		"cat a >> b.txt":                           true,
		"grep -c 'Failed' file":                    false,
		"awk -F: '$3>=1000'":                       false,
	}
	for part, want := range cases {
		if got := redirectsToFile(part); got != want {
			t.Errorf("redirectsToFile(%q) = %v, want %v", part, got, want)
		}
	}
}

func TestSplitCommandQuotes(t *testing.T) {
	got := splitCommand(`getent passwd | awk -F: '$3>=1000 && $7!~/nologin|false/'`)
	if len(got) != 2 || got[1] != ` awk -F: '$3>=1000 && $7!~/nologin|false/'` {
		t.Errorf("splitCommand = %q", got)
	}
	got = splitCommand(`apt list 2>/dev/null | grep -E 'a|b' | head -5`)
	if len(got) != 3 {
		t.Errorf("splitCommand pipes = %q", got)
	}
}

func TestSplitCommandSubshell(t *testing.T) {
	parts := splitCommand(`wg show 2>/dev/null || (systemctl is-active wg-quick@wg0 && ip -br link show wg0)`)
	want := []string{"wg show 2>/dev/null ", " (systemctl is-active wg-quick@wg0 ", " ip -br link show wg0)"}
	if len(parts) != len(want) {
		t.Fatalf("splitCommand = %q", parts)
	}
	for i := range want {
		if strings.Trim(strings.TrimSpace(parts[i]), "() \t") != strings.Trim(strings.TrimSpace(want[i]), "() \t") {
			t.Errorf("part %d = %q, want %q", i, parts[i], want[i])
		}
	}
}

func TestContainsWord(t *testing.T) {
	cases := []struct {
		cmd, p string
		want   bool
	}{
		{"ls /var/run/reboot-required*", "reboot", false},
		{"sudo reboot", "reboot", true},
		{"/system reboot", "reboot", true},
		{"reboot", "reboot", true},
		{"cat x > /dev/sda", "> /dev/sd", true},
		{"echo rm -rf", "rm -rf", true},
	}
	for _, c := range cases {
		if got := containsWord(c.cmd, c.p); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v", c.cmd, c.p, got)
		}
	}
}

func TestResolveHostSuffix(t *testing.T) {
	// модель дописала пояснение после имени сервера — имя всё равно должно найтись
	raw := "srv-bots · ps -p 695620 -o pid,etime,cmd"
	if got := strings.TrimSpace(strings.FieldsFunc(raw, func(r rune) bool { return r == ' ' || r == '·' })[0]); got != "srv-bots" {
		t.Errorf("first field = %q", got)
	}
}

// «Запретить» смотрит границу слова с обеих сторон. На живых данных из
// тринадцати отказов этого круга верными были два: чтение файла
// «/var/run/reboot-required» и строка «echo REBOOT_REQUIRED» считались
// командой reboot, и утренняя проверка серверов не могла узнать, нужен ли
// перезапуск, — ровно то, ради чего её заводили.
func TestPolicyDenyNotCatchesPartWords(t *testing.T) {
	e, _ := rulesBench(t)
	reading := []string{
		"cat /var/run/reboot-required.pkgs",
		"ls /var/run/reboot-required*",
		`if [ -f /var/run/reboot-required ]; then echo REBOOT_REQUIRED; fi`,
		"which ddrescue",
		"grep evaluate /etc/hosts",
	}
	for _, c := range reading {
		if r := e.Decide(rulesHost, c); r.Policy == PolicyDeny && !r.Default {
			t.Errorf("отказано читающей команде по правилу «Запретить»: %s (%s)", c, r.Why)
		}
	}
	// настоящая перезагрузка по-прежнему запрещена правилом
	for _, c := range []string{"reboot", "sudo reboot", "shutdown -r now"} {
		policy, why := e.Classify(rulesHost, c)
		if policy != PolicyDeny || !strings.Contains(why, "«Запретить»") {
			t.Errorf("опасная команда прошла: %s → %s (%s)", c, policy, why)
		}
	}
}

// Маска «*» в образцах: у «Разрешить» — на всю часть команды, у «Спросить» —
// внутри команды, у «Запретить» — от границы слова.
func TestPatternMasks(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	addHost(t, st, "м", store.HostRouterOS)
	addRules(t, st, "м", store.RuleAllow, "/* print*")
	addRules(t, st, "м", store.RuleAsk, "/ip firewall*add")
	addRules(t, st, "м", store.RuleDeny, "/system * reset*")
	for cmd, want := range map[string]string{
		"/interface wireguard peers print detail": PolicyAllow,
		"/ip firewall filter add chain=input":     PolicyAsk,
		"/system package reset-configuration":     PolicyDeny,
		"/ip address add address=10.0.0.1":        PolicyDeny, // правил на него нет: запрещено по умолчанию
	} {
		if got, why := e.Classify("м", cmd); got != want {
			t.Errorf("%q: ответ %q (%s), ждали %q", cmd, got, why, want)
		}
	}
}

// Запрет по умолчанию: команда, для которой ни одно правило не подошло,
// называется «нет правила» — по этому признаку человеку предлагают завести
// правило кнопкой. Запрет по правилу «Запретить» — не по умолчанию, кнопок у
// него нет.
func TestDefaultDenyIsNotRuleDeny(t *testing.T) {
	e, _ := rulesBench(t)
	byDefault := e.Decide(rulesHost, "systemctl restart nginx")
	if byDefault.Policy != PolicyDeny || !byDefault.Default || !byDefault.CanAllow() {
		t.Fatalf("неизвестная команда: %+v", byDefault)
	}
	sudoless := e.Decide(rulesHost, "sudo systemctl restart nginx")
	if sudoless.Policy != PolicyDeny || !sudoless.Default || sudoless.CanAllow() {
		t.Fatalf("sudo без правила «Спросить»: запрещено по умолчанию, но разрешить нельзя: %+v", sudoless)
	}
	byRule := e.Decide(rulesHost, "rm -rf /tmp/x")
	if byRule.Policy != PolicyDeny || byRule.Default {
		t.Fatalf("запрет по правилу назван запретом по умолчанию: %+v", byRule)
	}
}

// Правила разложены по серверам по роду, и разложенное безопасно повторять: то,
// что человек убрал, не возвращается, а то, что дописал, остаётся.
func TestTakeRuleSetMergesAndKeepsHumanEdits(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	addHost(t, st, "лин", store.HostLinux)
	addHost(t, st, "рос", store.HostRouterOS)
	takeSet(t, e, "лин", store.RuleSetLinux)
	takeSet(t, e, "рос", store.RuleSetRouterOS)
	lin, ros := st.HostRuleSet("лин"), st.HostRuleSet("рос")
	if len(lin.Allow) == 0 || len(ros.Allow) == 0 {
		t.Fatalf("наборы пусты: %d / %d", len(lin.Allow), len(ros.Allow))
	}
	for _, p := range ros.Allow {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("в «Разрешить» RouterOS попал образец Linux: %q", p)
		}
	}
	for _, p := range lin.Allow {
		if strings.HasPrefix(p, "/") {
			t.Errorf("в «Разрешить» Linux попал образец RouterOS: %q", p)
		}
	}
	// «Запретить» общее: необратимое нужно обоим
	if len(lin.Deny) == 0 || len(lin.Deny) != len(ros.Deny) {
		t.Errorf("«Запретить» у наборов разное: %d / %d", len(lin.Deny), len(ros.Deny))
	}
	if err := st.DeleteHostRulePattern("лин", store.RuleAllow, "uptime"); err != nil {
		t.Fatal(err)
	}
	addRules(t, st, "лин", store.RuleAllow, "мой-образец")
	n, err := e.TakeRuleSet("лин", store.RuleSetLinux)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("повторный набор дописал %d образцов, а убранный был один", n)
	}
	for _, p := range st.HostRuleSet("лин").Allow {
		if p == "мой-образец" {
			return
		}
	}
	t.Error("образец, дописанный человеком, пропал")
}

// Пара «Разрешить docker» и «Спросить docker rm» не принимается: «Разрешить»
// стоит перед «Спросить» для чистой команды, и «docker rm» шёл бы без вопроса.
// Человек получает объяснение, а не правило, которое молча ничего не делает.
// Слово из аргументов («sshd_config»), маска и запрет проверки не требуют.
func TestRuleAddGuardRefusesSilentConflicts(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	addHost(t, st, "а", store.HostLinux)
	addRules(t, st, "а", store.RuleAllow, "docker", "cat")
	if err := e.CheckRuleAdd("а", store.RuleAsk, "docker rm"); err == nil || !strings.Contains(err.Error(), "не подействует") {
		t.Errorf("«Спросить docker rm» при «Разрешить docker» принято: %v", err)
	}
	if err := e.CheckRuleAdd("а", store.RuleAsk, "cat"); err == nil {
		t.Error("«Спросить cat» при «Разрешить cat» принято")
	}
	// слово из аргументов читающую команду не останавливает — такое правило рабочее
	if err := e.CheckRuleAdd("а", store.RuleAsk, "sshd_config"); err != nil {
		t.Errorf("«Спросить sshd_config» отклонено: %v", err)
	}
	// команда, которой нет среди разрешённых, спрашивается как надо
	if err := e.CheckRuleAdd("а", store.RuleAsk, "systemctl restart"); err != nil {
		t.Errorf("«Спросить systemctl restart» отклонено: %v", err)
	}
	if err := e.CheckRuleAdd("а", store.RuleDeny, "docker rm"); err != nil {
		t.Errorf("запрет сильнее всего и проверки не требует: %v", err)
	}
	// и наоборот: «Разрешить» не должно открыть то, что сервер спрашивает по началу команды
	addRules(t, st, "а", store.RuleAsk, "systemctl restart")
	if err := e.CheckRuleAdd("а", store.RuleAllow, "systemctl"); err == nil || !strings.Contains(err.Error(), "systemctl restart") {
		t.Errorf("«Разрешить systemctl» при «Спросить systemctl restart» принято: %v", err)
	}
	if err := e.CheckRuleAdd("а", store.RuleAllow, "systemctl status"); err != nil {
		t.Errorf("«Разрешить systemctl status» отклонено: %v", err)
	}
}
