package engine

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Скрипт задачи и задача живут вместе: сирот в «Скриптах» быть не должно,
// а задача без кода — тем более.
func TestTaskLeavesTogetherWithScript(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	sid, err := st.CreateScript("Письма за сутки · из чата", "return 1;")
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{Title: "Письма за сутки", Schedule: "каждый день в 09:00", ScriptID: sid, Enabled: true}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	tid := task.ID
	// скрипт, который запускает задача, отдельно не удаляется
	if err := e.DeleteScript(sid); err == nil {
		t.Fatal("скрипт задачи удалился сам по себе")
	}
	if used := e.ScriptUsedBy(sid); len(used) != 1 || used[0] != "Письма за сутки" {
		t.Fatalf("не видно, кто пользуется скриптом: %v", used)
	}
	// задача забирает скрипт с собой
	name, err := e.DeleteTask(tid)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Письма за сутки · из чата" {
		t.Fatalf("скрипт не удалился вместе с задачей: %q", name)
	}
	if sc, err := st.Script(sid); err == nil && sc != nil {
		t.Fatal("скрипт остался сиротой")
	}
}

// Скрипт, который запускают две задачи, переживает удаление одной из них.
func TestCommonScriptStays(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	sid, _ := st.CreateScript("общий", "return 1;")
	first := &store.Task{Title: "первая", Schedule: "каждый час", ScriptID: sid}
	_ = st.CreateTask(first)
	_ = st.CreateTask(&store.Task{Title: "вторая", Schedule: "каждый час", ScriptID: sid})
	a := first.ID
	name, err := e.DeleteTask(a)
	if err != nil {
		t.Fatal(err)
	}
	if name != "" {
		t.Fatalf("общий скрипт удалён: %q", name)
	}
	if sc, err := st.Script(sid); err != nil || sc == nil {
		t.Fatal("общий скрипт пропал вместе с одной из задач")
	}
}
