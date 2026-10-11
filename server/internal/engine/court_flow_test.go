package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// судМодель отвечает по-разному арбитру и судье: они и в жизни разные
// модели, и промпты у них разные. Это позволяет проверить цепочку целиком,
// не трогая живой сервер.
type courtModel struct {
	name    string
	parse   string
	verdict string
	calls   int
}

func (m *courtModel) Name() string { return m.name }

func (m *courtModel) Complete(ctx context.Context, prompt string) (string, error) {
	m.calls++
	reply := m.parse
	if strings.Contains(prompt, "вердикт") || strings.Contains(prompt, "decision") {
		reply = m.verdict
	}
	llm.Report(ctx, m.name, "text", prompt, reply, llm.Usage{In: 10, Out: 5, Exact: true}, time.Now(), nil)
	return reply, nil
}

func (m *courtModel) CompleteStream(ctx context.Context, p string, on func(string)) (string, error) {
	return m.Complete(ctx, p)
}

func (m *courtModel) Vision(ctx context.Context, p string, img []byte, mime string) (string, error) {
	return m.Complete(ctx, p)
}

// Цепочка «арбитр → судья» целиком: разбор с пробой, вердикт, решение.
// Раньше проверялась только охрана вокруг суда, а сам суд — ни разу.
func TestCourtPassesChain(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	arbiter := &courtModel{name: "арбитр",
		parse:   `{"what":"перезапуск службы","reversible":true,"risk":"минута простоя","instead":"","probe":"systemctl is-active nginx"}`,
		verdict: `{"decision":"run","why":"обратимо и с пробой"}`}
	judge := &courtModel{name: "судья",
		parse:   `{"what":"то же","reversible":true,"risk":"нет","probe":"systemctl is-active nginx"}`,
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
	if ok, why := e.CourtReady(); !ok {
		t.Fatalf("суд не собрался: %s", why)
	}

	h := &store.Host{Name: "проба", Addr: "127.0.0.1", User: "root", Key: "ssh:проба",
		Auto: true, Window: "", MaxHour: 5}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	cmd := "systemctl restart nginx"
	if ok, why := e.courtAllowed(h, cmd, SSHOrigin{Who: "чат «Серверы»"}); !ok {
		t.Fatalf("команда не дошла до суда: %s", why)
	}

	out := e.Court(context.Background(), h, cmd, SSHOrigin{Who: "чат «Серверы»"})
	if !out.Held {
		t.Fatal("суд не состоялся")
	}
	if out.Decision != VerdictRun {
		t.Fatalf("вердикт %q, причина %q", out.Decision, out.Why)
	}
	// проба обязательна: без неё нечем проверить, что всё хорошо
	if strings.TrimSpace(out.Probe) == "" {
		t.Fatal("суд разрешил без пробы")
	}
	if arbiter.calls == 0 || judge.calls == 0 {
		t.Fatalf("спросили не обоих: арбитр %d, судья %d", arbiter.calls, judge.calls)
	}
}

// Молчание не разрешение: арбитр без пробы — значит человеку.
func TestCourtWithoutProbeGoesHuman(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	silent := &courtModel{name: "молчун",
		parse:   `{"what":"что-то","reversible":true,"risk":"неясно","probe":""}`,
		verdict: `{"decision":"run","why":"давай"}`}
	second := &courtModel{name: "второй", parse: silent.parse, verdict: silent.verdict}
	e.setProvider("молчун", silent)
	e.setProvider("второй", second)
	roles := e.Roles()
	for i := range roles {
		switch roles[i].Name {
		case RoleArbiter:
			roles[i].Model = "молчун"
		case RoleJudge:
			roles[i].Model = "второй"
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", User: "root", Key: "ssh:проба", Auto: true, MaxHour: 5}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	out := e.Court(context.Background(), h, "systemctl restart nginx", SSHOrigin{Who: "чат"})
	if out.Decision != VerdictHuman {
		t.Fatalf("без пробы вердикт %q — должно быть «человеку»", out.Decision)
	}
	if !strings.Contains(out.Why, "проб") {
		t.Fatalf("причина не про пробу: %q", out.Why)
	}
}
