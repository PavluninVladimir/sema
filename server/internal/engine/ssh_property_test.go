package engine

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Проверка правил сервера на множестве сгенерированных случаев. Инварианты
// важнее отдельных примеров: что бы модель ни написала, команда с образцом
// «Запретить» не должна оказаться разрешённой, а sudo и неясный синтаксис —
// пройти по «Разрешить». Судим настоящим движком (`Engine.Classify` по правилам
// сервера из базы) — копия правил в тесте разошлась бы с кодом и молча
// перестала защищать.

var (
	safeParts = []string{"ls /var/log", "cat /etc/hostname", "uptime", "df -h", "grep -c error file", "ps aux", "wg show", "apt list --upgradable", "/system resource print"}
	badParts  = []string{"rm -rf /", "mkfs.ext4 /dev/sda", "shutdown now", "reboot", "/system reset-configuration"}
	oddParts  = []string{"sudo systemctl restart nginx", "echo hi > /etc/passwd", "curl example.com", "dd if=/dev/zero"}
	joins     = []string{" | ", " && ", " ; ", " || "}
)

// classifierEngine — движок на временной базе и сервер «правила» с обоими
// наборами: Linux и RouterOS. Раньше все серверы делили одни круги, теперь у
// каждого свои, и судить команду можно только «на сервере».
func classifierEngine(t testing.TB) *Engine {
	t.Helper()
	e, _ := newTestEngine(t, "", nil)
	addHost(t, e.store, rulesHost, "")
	takeSet(t, e, rulesHost, store.RuleSetLinux)
	takeSet(t, e, rulesHost, store.RuleSetRouterOS)
	return e
}

// Свежая база: наборы на месте, и в «Запретить» каждого из них стоят
// rm -rf, mkfs и shutdown. Раньше засев v2 делал список чтения непустым, и
// опасные команды оставались без запрета.
func TestFreshDatabaseHasDenyInSets(t *testing.T) {
	e := classifierEngine(t)
	for _, cmd := range []string{"rm -rf /var", "mkfs.ext4 /dev/sda", "shutdown -h now", "reboot", "dd if=/dev/zero of=/dev/sda"} {
		if policy, why := e.Classify(rulesHost, cmd); policy != PolicyDeny {
			t.Errorf("на свежей базе %q получила ответ %q (%s)", cmd, policy, why)
		}
	}
}

// Свойство 1: команда, собранная только из читающих частей, всегда разрешена.
func TestPropertySafeCommandsAlwaysAllowed(t *testing.T) {
	e := classifierEngine(t)
	rnd := rand.New(rand.NewSource(20260904))
	for i := 0; i < 500; i++ {
		n := 1 + rnd.Intn(3)
		parts := make([]string, 0, n)
		for j := 0; j < n; j++ {
			parts = append(parts, safeParts[rnd.Intn(len(safeParts))])
		}
		cmd := strings.Join(parts, joins[rnd.Intn(len(joins))])
		if got, why := e.Classify(rulesHost, cmd); got != PolicyAllow {
			t.Fatalf("команда %q получила ответ %q (%s), ожидали «Разрешить»", cmd, got, why)
		}
	}
}

// Свойство 2: если в команде есть запрещённая часть, ответ всегда «Запретить»,
// куда бы её ни поставили и чем бы ни разбавляли.
func TestPropertyDenyAlwaysWins(t *testing.T) {
	e := classifierEngine(t)
	rnd := rand.New(rand.NewSource(4092026))
	for i := 0; i < 500; i++ {
		bad := badParts[rnd.Intn(len(badParts))]
		var parts []string
		for j := 0; j < rnd.Intn(3); j++ {
			parts = append(parts, safeParts[rnd.Intn(len(safeParts))])
		}
		pos := rnd.Intn(len(parts) + 1)
		parts = append(parts[:pos], append([]string{bad}, parts[pos:]...)...)
		cmd := strings.Join(parts, joins[rnd.Intn(len(joins))])
		if got, why := e.Classify(rulesHost, cmd); got != PolicyDeny {
			t.Fatalf("команда %q получила ответ %q (%s), ожидали «Запретить»", cmd, got, why)
		}
	}
}

// Свойство 3: незнакомая или изменяющая часть никогда не проходит как разрешённая.
func TestPropertyUnknownNeverAllowed(t *testing.T) {
	e := classifierEngine(t)
	rnd := rand.New(rand.NewSource(777))
	for i := 0; i < 300; i++ {
		odd := oddParts[rnd.Intn(len(oddParts))]
		cmd := odd
		if rnd.Intn(2) == 0 {
			cmd = safeParts[rnd.Intn(len(safeParts))] + " && " + odd
		}
		if got, _ := e.Classify(rulesHost, cmd); got == PolicyAllow {
			t.Fatalf("команда %q прошла как разрешённая", cmd)
		}
	}
}

// Свойство 4: приписка к читающей команде не даёт ей пройти как разрешённой —
// ни перенаправлением, ни sudo, ни подстановкой.
func TestPropertyDecorationNeverAllowed(t *testing.T) {
	e := classifierEngine(t)
	rnd := rand.New(rand.NewSource(31415))
	tails := []string{" > /etc/crontab", " >> /root/.ssh/authorized_keys", " 2> /var/log/x.log"}
	for i := 0; i < 300; i++ {
		base := safeParts[rnd.Intn(len(safeParts))]
		var cmd string
		switch rnd.Intn(3) {
		case 0:
			cmd = base + tails[rnd.Intn(len(tails))]
		case 1:
			cmd = "sudo " + base
		default:
			cmd = base + " $(rm -rf /tmp/x)"
		}
		if got, _ := e.Classify(rulesHost, cmd); got == PolicyAllow {
			t.Fatalf("команда %q прошла как разрешённая", cmd)
		}
	}
}

// Свойство 5: у сервера без правил всё запрещено — и читающее, и изменяющее, и
// пустяк вроде «uptime». Запрещено по умолчанию: человеку можно предложить
// завести правило, и это видно по признакам ответа.
func TestPropertyNoRulesDeniesEverything(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	addHost(t, st, "пустой", store.HostLinux)
	rnd := rand.New(rand.NewSource(5150))
	pools := [][]string{safeParts, badParts, oddParts, {"uptime", "ls", "", "   ", "echo hi"}}
	for i := 0; i < 400; i++ {
		pool := pools[rnd.Intn(len(pools))]
		cmd := pool[rnd.Intn(len(pool))]
		if rnd.Intn(3) == 0 {
			cmd += joins[rnd.Intn(len(joins))] + safeParts[rnd.Intn(len(safeParts))]
		}
		r := e.Decide("пустой", cmd)
		if r.Policy != PolicyDeny {
			t.Fatalf("у сервера без правил команда %q получила %q", cmd, r.Policy)
		}
		if !r.NoRules || !r.Default {
			t.Fatalf("запрет у сервера без правил не назван «нет правил»: %+v", r)
		}
	}
	// сервера, которого нет, правил нет тоже
	if policy, _ := e.Classify("такого-нет", "uptime"); policy != PolicyDeny {
		t.Fatalf("на несуществующем сервере команда получила %q", policy)
	}
}

// Свойство 6: «Запретить» сильнее «Разрешить» и «Спросить»: образец, стоящий
// в нескольких колонках сразу, отказывает.
func TestPropertyDenyBeatsAllowAndAsk(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	rnd := rand.New(rand.NewSource(8080))
	words := []string{"ls", "cat", "df", "uptime", "grep", "ps", "whoami", "free"}
	for i := 0; i < 200; i++ {
		host := "п" + string(rune('а'+i%26)) + strings.Repeat("я", i/26)
		addHost(t, st, host, store.HostLinux)
		w := words[rnd.Intn(len(words))]
		addRules(t, st, host, store.RuleAllow, w)
		if rnd.Intn(2) == 0 {
			addRules(t, st, host, store.RuleAsk, w)
		}
		addRules(t, st, host, store.RuleDeny, w)
		cmd := w + " /tmp"
		if got, why := e.Classify(host, cmd); got != PolicyDeny {
			t.Fatalf("%q при образце во всех колонках получила %q (%s)", cmd, got, why)
		}
	}
}

// Свойство 7: sudo, doas и su не бывают «Разрешить» — никакими правилами, хоть
// бы сам образец был «sudo». Проходят только по правилу «Спросить», а иначе
// запрещены по умолчанию.
func TestPropertySudoNeverAllowed(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	addHost(t, st, "а", store.HostLinux)
	addHost(t, st, "б", store.HostLinux)
	takeSet(t, e, "а", store.RuleSetLinux)
	addRules(t, st, "а", store.RuleAllow, "sudo", "doas", "su", "sudo ls", "sudo systemctl restart nginx")
	addRules(t, st, "б", store.RuleAsk, "systemctl restart")
	rnd := rand.New(rand.NewSource(1234))
	roots := []string{"sudo", "doas", "su -c", "sudo -n"}
	bodies := append(append([]string{}, safeParts...), "systemctl restart nginx", "ls", "sudo ls")
	for i := 0; i < 400; i++ {
		cmd := roots[rnd.Intn(len(roots))] + " " + bodies[rnd.Intn(len(bodies))]
		if rnd.Intn(3) == 0 {
			cmd = safeParts[rnd.Intn(len(safeParts))] + " | " + cmd
		}
		if got, why := e.Classify("а", cmd); got == PolicyAllow {
			t.Fatalf("команда с root %q разрешена (%s)", cmd, why)
		}
		if strings.Contains(cmd, "systemctl restart") {
			if got, _ := e.Classify("б", cmd); got != PolicyAsk {
				t.Fatalf("sudo при правиле «Спросить» ответил %q: %q", got, cmd)
			}
		}
	}
}

// Свойство 8: неясный синтаксис не разрешается образцами никогда: подстановка,
// обратные кавычки, записи в файл, awk system() — только «Спросить».
func TestPropertyUnclearSyntaxNeverAllowedByPattern(t *testing.T) {
	e := classifierEngine(t)
	rnd := rand.New(rand.NewSource(99))
	unclear := []string{
		"echo $(touch /tmp/x)", "echo `id`", "awk 'BEGIN {system(\"id\")}'", "cat <(ls)",
		"ls > /tmp/out", "df >> /etc/cron.d/x", "sort -o /tmp/x /etc/hosts", "find /tmp -delete", "sed -i s/a/b/ /etc/hosts",
	}
	for i := 0; i < 300; i++ {
		cmd := unclear[rnd.Intn(len(unclear))]
		if rnd.Intn(2) == 0 {
			cmd = safeParts[rnd.Intn(len(safeParts))] + " && " + cmd
		}
		r := e.Decide(rulesHost, cmd)
		if r.Policy == PolicyAllow {
			t.Fatalf("неясная команда %q разрешена образцами (%s)", cmd, r.Why)
		}
		if r.Unsafe == "" {
			t.Fatalf("неясная команда %q не названа неясной", cmd)
		}
		if r.CanAllow() {
			t.Fatalf("неясную команду %q можно предложить «Разрешить её»", cmd)
		}
	}
}

// Единственное, что пропускает неясный синтаксис в «Разрешить», — команда,
// вписанная ЦЕЛИКОМ, слово в слово: решение человека про одну строку. Хвост
// делает строку другой, и разрешение к ней не относится; sudo целиком не
// разрешается никогда.
func TestWholeLineIsTheOnlyWayPastUnclearSyntax(t *testing.T) {
	e, st := rulesBench(t)
	const line = "find /home -maxdepth 3 -name authorized_keys -exec md5sum {} + 2>/dev/null"
	if got, _ := e.Classify(rulesHost, line); got == PolicyAllow {
		t.Fatal("строка разрешена до того, как человек её вписал")
	}
	addRules(t, st, rulesHost, store.RuleAllow, line)
	if got, why := e.Classify(rulesHost, line); got != PolicyAllow {
		t.Fatalf("вписанная целиком строка не разрешена: %s (%s)", got, why)
	}
	if got, _ := e.Classify(rulesHost, line+" && rm /tmp/x"); got == PolicyAllow {
		t.Fatal("разрешение расползлось на строку с хвостом")
	}
	const root = "sudo ls /root"
	addRules(t, st, rulesHost, store.RuleAllow, root)
	if got, _ := e.Classify(rulesHost, root); got == PolicyAllow {
		t.Fatal("sudo разрешён целиком")
	}
}

// Свойство 9: у двух серверов правила разные и не смешиваются — что
// разрешено одному, другому запрещено (у него своих правил нет или другие), а
// правка одного второго не трогает.
func TestPropertyHostsDoNotShareRules(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	addHost(t, st, "первый", store.HostLinux)
	addHost(t, st, "второй", store.HostLinux)
	addHost(t, st, "третий", store.HostRouterOS)
	addRules(t, st, "первый", store.RuleAllow, "uptime", "df")
	addRules(t, st, "второй", store.RuleAllow, "free")
	addRules(t, st, "второй", store.RuleDeny, "df")
	addRules(t, st, "третий", store.RuleAllow, "/system resource print")
	cases := []struct {
		host, cmd, want string
	}{
		{"первый", "uptime", PolicyAllow}, {"второй", "uptime", PolicyDeny}, {"третий", "uptime", PolicyDeny},
		{"первый", "df -h", PolicyAllow}, {"второй", "df -h", PolicyDeny},
		{"второй", "free -m", PolicyAllow}, {"первый", "free -m", PolicyDeny},
		{"третий", "/system resource print", PolicyAllow}, {"первый", "/system resource print", PolicyDeny},
	}
	for _, c := range cases {
		if got, why := e.Classify(c.host, c.cmd); got != c.want {
			t.Errorf("%s · %q: ответ %q (%s), ждали %q", c.host, c.cmd, got, why, c.want)
		}
	}
	// правка одного сервера другого не касается
	addRules(t, st, "первый", store.RuleAllow, "free")
	if got, _ := e.Classify("третий", "free"); got != PolicyDeny {
		t.Errorf("правило первого сервера просочилось на третий: %q", got)
	}
	if err := st.DeleteHostRulePattern("первый", store.RuleAllow, "uptime"); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Classify("первый", "uptime"); got != PolicyDeny {
		t.Errorf("убранное правило всё ещё действует: %q", got)
	}
	if got, _ := e.Classify("второй", "free"); got != PolicyAllow {
		t.Errorf("правка первого сервера задела второй: %q", got)
	}
}

// Фаззинг: разбор любой строки не паникует и всегда даёт один из трёх ответов.
func FuzzClassify(f *testing.F) {
	e := classifierEngine(f)
	f.Add("ls /tmp")
	f.Add("rm -rf /")
	f.Add("wg show 2>/dev/null || (systemctl is-active wg-quick@wg0)")
	f.Add("")
	f.Fuzz(func(t *testing.T, cmd string) {
		policy, why := e.Classify(rulesHost, cmd)
		switch policy {
		case PolicyAllow, PolicyAsk, PolicyDeny:
		default:
			t.Fatalf("неизвестный ответ %q для %q (%s)", policy, cmd, why)
		}
	})
}
