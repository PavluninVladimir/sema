package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Уровень самостоятельности: тумблер переезжает в «под судом», на «полной»
// правило «Спросить» идёт на суд только со страховкой возврата, а машине
// Сёмы и серверу без sudo «полная» не даётся вовсе.
func TestLevelsAutonomy(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", Port: 22, User: "u", Key: "ssh:проба", Auto: true}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Host("проба")
	if got.Autonomy != store.AutonomyCourt || !got.Auto || got.Full() {
		t.Fatalf("тумблер не стал уровнем «под судом»: %+v", got)
	}

	// на «под судом» файрвол идёт дальше обычной проверки: страховка не нужна
	cmd := "sudo ufw allow 22"
	ok, why := e.courtAllowed(got, cmd, SSHOrigin{Who: "проба"})
	if ok || strings.Contains(why, "страховк") {
		t.Fatalf("на «под судом» спросили страховку или пропустили без судьи: %v %q", ok, why)
	}

	// «полная» — та же команда идёт на суд, потому что есть чем подстраховать
	got.Autonomy = store.AutonomyFull
	if err := st.SaveHost(got); err != nil {
		t.Fatal(err)
	}
	full, _ := st.Host("проба")
	if !full.Full() {
		t.Fatalf("уровень не сохранился: %+v", full)
	}
	if ok, why := e.courtAllowed(full, cmd, SSHOrigin{Who: "проба"}); !ok && !strings.Contains(why, "суд не собран") {
		t.Fatalf("на «полной» страховка нашлась, а команда всё равно остановлена не судом: %q", why)
	}
	// у сервера без sudo страховку ставить нечем — «Спросить» на «полной» идёт человеку
	full.NoSudo = true
	if ok, why := e.courtAllowed(full, cmd, SSHOrigin{Who: "проба"}); ok || !strings.Contains(why, "страховк") {
		t.Fatalf("без sudo страховка не нашлась, а команда пошла на суд: %v %q", ok, why)
	}

	// машина Сёмы и сервер без sudo — «полная» не даётся
	own := &store.Host{Name: HostOwnerName, Addr: "127.0.0.1", Port: 22, User: "u", Key: "ssh:хозяин", NoSudo: true}
	if why := e.FullAutonomyDenied(own); why == "" {
		t.Fatal("машине Сёмы отдали полную самостоятельность")
	}
	if why := e.FullAutonomyDenied(&store.Host{Name: "другой", NoSudo: true}); !strings.Contains(why, "sudo") {
		t.Fatalf("серверу без sudo отдали полную самостоятельность: %q", why)
	}
}

// Страховка возврата не заводится без возврата и без понятного возврата:
// «вернуть нечем» — это и есть необратимое.
func TestInsuranceRequiresRollback(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", Port: 22, User: "u", Key: "ssh:проба",
		Kind: "linux", Autonomy: store.AutonomyFull}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	if _, err := e.placeInsurance(context.Background(), h, "  "); err == nil ||
		!strings.Contains(err.Error(), "возврат не назван") {
		t.Fatalf("страховка завелась без возврата: %v", err)
	}
	if _, err := e.placeInsurance(context.Background(), h, "ufw allow $(cat /tmp/x)"); err == nil ||
		!strings.Contains(err.Error(), "непонятного вида") {
		t.Fatalf("страховка приняла непонятный возврат: %v", err)
	}
	// без ключа в хранилище дальше не пройдёт — но причина уже про ключ,
	// а не про форму возврата: значит проверки формы отработали
	if _, err := e.placeInsurance(context.Background(), h, "ufw allow 22"); err == nil ||
		strings.Contains(err.Error(), "возврат") {
		t.Fatalf("ожидали отказ по ключу, а не по возврату: %v", err)
	}
}

// Суд не выпускает команду с правилом «Спросить» без названного возврата
// на «полной», даже когда сервер отдан целиком.
func TestCourtRequiresRollbackForDangerous(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	arbiter := &courtModel{name: "арбитр",
		parse:   `{"what":"открыть порт","reversible":true,"risk":"дыра","probe":"ufw status","back":""}`,
		verdict: `{"decision":"run","why":"ладно"}`}
	judge := &courtModel{name: "судья",
		parse:   `{"what":"то же","reversible":true,"risk":"нет","probe":"ufw status","back":""}`,
		verdict: `{"decision":"run","why":"согласен"}`}
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
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", Port: 22, User: "u", Key: "ssh:проба",
		Kind: "linux", Autonomy: store.AutonomyFull}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	out := e.Court(context.Background(), h, "sudo ufw allow 22", SSHOrigin{Who: "проба"})
	if out.Decision != VerdictHuman || !strings.Contains(out.Why, "возврат") {
		t.Fatalf("суд выпустил опасное без возврата: %+v", out)
	}
}
