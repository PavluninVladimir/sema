package engine

import (
	"context"
	"testing"
	"time"
)

// Экономия у внешнего агента считается на старте: сводка прошлой сессии
// приходит кристаллом вместо перечитывания переписки.
func TestSavingsMemoryOnStartSession(t *testing.T) {
	fp := &fakeProvider{answer: `{"area":"код","crystal":"Чинили вёрстку и выкатили.","facts":[],"lessons":[],"graph":[]}`}
	e, st := newTestEngine(t, "модель", fp)
	_ = e.SetRoleField(RoleMemory, "model", "модель")
	o := e.ExternalOrigin("Claude Code", "/tmp/проба")
	if err := st.SetConsumerAreas(o.Consumer.ID, []string{"код"}); err != nil {
		t.Fatal(err)
	}
	sid, _, _ := e.SessionStart(context.Background(), "Claude Code", "/tmp/проба", "", "", false)
	if err := e.SessionEnd(context.Background(), "Claude Code", "/tmp/проба", sid, "Долгая сводка: чинили вёрстку ролей, выкатили на Pi, проверили пуши.", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.CrystallizeSession(context.Background(), sid); err != nil {
		t.Fatalf("кристалл: %v", err)
	}
	_, text, _ := e.SessionStart(context.Background(), "Claude Code", "/tmp/проба", "", "", false)
	if text == "" {
		t.Fatal("вторая сессия не получила память")
	}
	since := time.Now().Add(-time.Hour).Format(time.RFC3339)
	use := st.MemoryLogByConsumer(since)
	found := false
	for _, u := range use {
		if u.Consumer == "Claude Code · /tmp/проба" && u.Measured && u.Saved > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("экономия на старте не посчитана: %+v", use)
	}
}
