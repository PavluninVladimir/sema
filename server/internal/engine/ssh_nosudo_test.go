package engine

import (
	"context"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Без sudo» у сервера сильнее подтверждения: такая команда не показывается
// человеку кнопкой, а отклоняется совсем.
func TestHostWithoutSudo(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveHost(&store.Host{Name: "хозяин", Addr: "127.0.0.1", Port: 22, User: "ivan", Key: "ssh:хозяин", Kind: store.HostLinux, NoSudo: true}); err != nil {
		t.Fatal(err)
	}
	takeSet(t, e, "хозяин", store.RuleSetLinux)
	// даже правило «Спросить» на sudo не открывает дорогу: «без sudo» сильнее
	addRules(t, st, "хозяин", store.RuleAsk, "sudo")
	h, ok := st.Host("хозяин")
	if !ok || !h.NoSudo {
		t.Fatal("признак «без sudo» не сохранился")
	}
	for _, cmd := range []string{"sudo apt-get update", "echo ok && sudo reboot", "doas ls", "echo $(sudo cat /etc/shadow)"} {
		out := e.RequestSSH(context.Background(), SSHOrigin{Who: "проба"}, "хозяин", cmd)
		if out.Policy != PolicyDeny || out.PendingID != 0 || out.NoRuleID != 0 {
			t.Errorf("%q: ответ %q, ожидание %d, запрос правила %d — sudo должен отклоняться совсем, без кнопок", cmd, out.Policy, out.PendingID, out.NoRuleID)
		}
	}
	// обычная читающая команда не задета
	if out := e.RequestSSH(context.Background(), SSHOrigin{Who: "проба"}, "хозяин", "uptime"); out.Policy == PolicyDeny {
		t.Errorf("чтение отклонено: %s", out.Why)
	}
}
