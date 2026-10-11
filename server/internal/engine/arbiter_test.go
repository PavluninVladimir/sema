package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Самостоятельное управление: жёсткие правила работают до всякой модели, а
// суд не собирается, пока роли не на разных моделях.

func TestAutoWindowOK(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 9, 4, h, m, 0, 0, time.Local) }
	cases := []struct {
		window string
		when   time.Time
		want   bool
	}{
		{"", at(13, 0), true},             // окна нет — круглые сутки
		{"02:00-06:00", at(3, 30), true},  // внутри
		{"02:00-06:00", at(6, 0), false},  // ровно на границе — уже нельзя
		{"02:00-06:00", at(13, 0), false}, // снаружи
		{"22:00-04:00", at(23, 30), true}, // через полночь
		{"22:00-04:00", at(2, 0), true},   // через полночь, вторая половина
		{"22:00-04:00", at(12, 0), false}, // днём нельзя
		{"мусор", at(3, 0), false},        // не разобрали — значит нельзя
		{"25:00-26:00", at(3, 0), false},  // часов таких нет
	}
	for _, c := range cases {
		if got := AutoWindowOK(c.window, c.when); got != c.want {
			t.Errorf("AutoWindowOK(%q, %s) = %v, хотели %v", c.window, c.when.Format("15:04"), got, c.want)
		}
	}
}

// «Спросить» идёт на суд, а неясный синтаксис — нет: судить то, чего не
// разобрал даже разбор команды, суду нельзя, пока сервер не отдан Сёме целиком.
func TestAskGoesToCourtUnclearSyntaxDoesNot(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", User: "root", Key: "ssh:проба",
		Auto: true, Window: "", MaxHour: 5}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"cat /root/.ssh/authorized_keys > /tmp/x", "echo $(cat /etc/shadow)", "ufw disable && $(reboot)",
	} {
		ok, why := e.courtAllowed(h, cmd, SSHOrigin{Who: "чат"})
		if ok {
			t.Errorf("команда %q с неясным синтаксисом пошла на суд", cmd)
		}
		if strings.Contains(why, "суд не собран") {
			t.Errorf("команду %q остановила не охрана синтаксиса, а отсутствие ролей: %s", cmd, why)
		}
	}
	// ясная изменяющая команда до суда доходит: останавливает её лишь то,
	// что роли ещё не назначены
	if ok, why := e.courtAllowed(h, "systemctl restart nginx", SSHOrigin{Who: "чат"}); ok {
		t.Errorf("суд собрался без назначенных ролей: %s", why)
	} else if !strings.Contains(why, "суд не собран") {
		t.Errorf("ясная команда остановлена не там, где надо: %s", why)
	}
}

// Суд не собирается, если арбитр и судья на одной модели.
func TestCourtNeedsDifferentModels(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	roles := e.Roles()
	for i := range roles {
		if roles[i].Name == RoleArbiter || roles[i].Name == RoleJudge {
			roles[i].Model = "одна-и-та-же"
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	ok, why := e.CourtReady()
	if ok {
		t.Fatal("суд собрался на одной модели")
	}
	if !strings.Contains(why, "одной модели") {
		t.Errorf("причина: %q", why)
	}
}

// Намерение снаружи полномочий не даёт: такая команда на суд не идёт.
func TestExternalOriginNeverGoesToCourt(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", User: "root", Key: "ssh:проба", Auto: true}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	ok, why := e.courtAllowed(h, "systemctl restart nginx", SSHOrigin{Who: "письмо", External: true})
	if ok {
		t.Fatal("внешнее намерение пошло на суд")
	}
	if !strings.Contains(why, "снаружи") {
		t.Errorf("причина: %q", why)
	}
}

// Молчание или бессвязный ответ модели — не разрешение: вердикт «человеку».
func TestCourtFallsBackToHuman(t *testing.T) {
	fake := &fakeProvider{answer: "я не понял вопроса"}
	e, st := newTestEngine(t, "арбитр", fake)
	e.providers["судья"] = &fakeProvider{answer: `{"decision": "run", "why": "давай"}`}
	roles := e.Roles()
	for i := range roles {
		switch roles[i].Name {
		case RoleArbiter:
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "арбитр", roleNoneName, 30
		case RoleJudge:
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "судья", roleNoneName, 30
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", User: "root", Key: "ssh:проба", Auto: true}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	ctx := llm.WithConsumer(context.Background(), "core:test")
	out := e.Court(ctx, h, "systemctl restart nginx", SSHOrigin{Who: "чат"})
	if out.Decision != VerdictHuman {
		t.Fatalf("вердикт %q при неразобранном ответе арбитра", out.Decision)
	}
	if !strings.Contains(out.Why, "арбитр") {
		t.Errorf("причина: %q", out.Why)
	}
}

// Разбор без пробы не годится: проверить результат нечем — значит человеку.
func TestCourtRequiresProbe(t *testing.T) {
	arb := &fakeProvider{answer: `{"what":"перезапуск nginx","reversible":true,"risk":"короткий простой","instead":"","probe":""}`}
	e, st := newTestEngine(t, "арбитр", arb)
	e.providers["судья"] = &fakeProvider{answer: `{"decision":"run","why":"безопасно"}`}
	roles := e.Roles()
	for i := range roles {
		switch roles[i].Name {
		case RoleArbiter:
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "арбитр", roleNoneName, 30
		case RoleJudge:
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "судья", roleNoneName, 30
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", User: "root", Key: "ssh:проба", Auto: true}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	out := e.Court(llm.WithConsumer(context.Background(), "core:test"), h, "systemctl restart nginx", SSHOrigin{Who: "чат"})
	if out.Decision != VerdictHuman || !strings.Contains(out.Why, "пробу") {
		t.Fatalf("вердикт %q, причина %q", out.Decision, out.Why)
	}
}

// Потолок изменений в час: исчерпан — команда идёт человеку.
func TestAutoRateLimit(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", User: "root", Key: "ssh:проба", Auto: true, MaxHour: 2}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		st.LogSSH(store.SSHLogEntry{Host: "проба", Cmd: "systemctl restart nginx", Who: "суд",
			Decision: store.SSHAuto, Code: 0})
	}
	if ok, why := e.courtAllowed(h, "systemctl restart nginx", SSHOrigin{Who: "чат"}); ok {
		t.Fatal("потолок не сработал")
	} else if !strings.Contains(why, "потолок") {
		t.Errorf("причина: %q", why)
	}
}

// Главный инвариант: даже если суд единогласно за, «Запретить» отменяет
// решение. Модель, договорившаяся сама с собой, ничего не решает.
func TestDenyBeatsUnanimousCourt(t *testing.T) {
	arb := &fakeProvider{answer: `{"what":"удаляет пользователя","reversible":true,"risk":"нет","instead":"","probe":"id olduser"}`}
	e, st := newTestEngine(t, "арбитр", arb)
	e.providers["судья"] = &fakeProvider{answer: `{"decision":"run","why":"да делай уже"}`}
	roles := e.Roles()
	for i := range roles {
		switch roles[i].Name {
		case RoleArbiter:
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "арбитр", roleNoneName, 30
		case RoleJudge:
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "судья", roleNoneName, 30
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", User: "root", Key: "ssh:проба", Auto: true}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	// userdel и запрещён, и спрашивается: запрет сильнее, и до суда дело не доходит
	addRules(t, st, "проба", store.RuleAsk, "userdel")
	addRules(t, st, "проба", store.RuleDeny, "userdel")
	out := e.RequestSSH(context.Background(), SSHOrigin{Who: "чат"}, "проба", "userdel olduser")
	if out.Policy != PolicyDeny {
		t.Fatalf("запрещённая команда получила ответ %q", out.Policy)
	}
	if out.Output != "" || out.PendingID != 0 || out.NoRuleID != 0 {
		t.Fatalf("у запрета по правилу нет ни вывода, ни кнопок: %+v", out)
	}
	if out.Err == "" {
		t.Fatal("команда не стала отказом")
	}
	// в журнале нет отметки «сделано самой»
	if n := st.CountSSHChanges("проба", "2000-01-01T00:00:00Z"); n != 0 {
		t.Errorf("в журнале %d самостоятельных изменений", n)
	}
}

// «Спросить» на «под судом» идёт на суд: цепочка доходит до вердикта, и
// единогласное «run» выполняет команду. Правило «Спросить» — разрешение
// спрашивать, а не запрет судить.
func TestAskReachesCourtOnCourtLevel(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	arbiter := &courtModel{name: "арбитр",
		parse:   `{"what":"перезапуск службы","reversible":true,"risk":"минута простоя","probe":"systemctl is-active nginx"}`,
		verdict: `{"decision":"run","why":"обратимо и с пробой"}`}
	judge := &courtModel{name: "судья", parse: arbiter.parse, verdict: `{"decision":"run","why":"согласен"}`}
	e.setProvider("арбитр", arbiter)
	e.setProvider("судья", judge)
	roles := e.Roles()
	for i := range roles {
		switch roles[i].Name {
		case RoleArbiter:
			roles[i].Model = "арбитр"
		case RoleJudge:
			roles[i].Model = "судья"
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	h := addHost(t, st, "проба", store.HostLinux)
	h.Auto, h.Autonomy = true, store.AutonomyCourt
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	takeSet(t, e, "проба", store.RuleSetLinux)
	addRules(t, st, "проба", store.RuleAsk, "systemctl restart")
	e.sshVia = (&nightSSH{}).run
	if err := st.SaveSecret(&store.Secret{Name: "ssh:проба", Kind: "ssh"}, "ключ-понарошку"); err != nil {
		t.Fatal(err)
	}
	out := e.RequestSSH(context.Background(), SSHOrigin{Who: "чат"}, "проба", "systemctl restart nginx")
	if out.PendingID != 0 || out.Err != "" {
		t.Fatalf("суд не выполнил команду: %+v", out)
	}
	if arbiter.calls == 0 || judge.calls == 0 {
		t.Fatalf("суд не состоялся: арбитр %d, судья %d", arbiter.calls, judge.calls)
	}
}
