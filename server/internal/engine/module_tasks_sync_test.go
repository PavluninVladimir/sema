package engine

import (
	"sort"
	"testing"
)

// У модуля может быть несколько задач с одной функцией и разным расписанием.
// Сверка с манифестом склеивала их в одну: 17 сентября 2026 в «Техническом
// фэншуе» стояли два «Утренних поста» на 9:00, модуль запускался дважды, а
// вечернего не было. Задачи, пропавшие из манифеста, не должны оставаться
// сиротами.
func TestTaskModuleWithOneFuncNotMerged(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	m := &Module{Name: "канал", Title: "Канал", Tasks: []ModuleTask{
		{Title: "Утренний пост", Schedule: "9:00", Run: "run"},
		{Title: "Вечерний пост", Schedule: "19:00", Run: "run"},
	}}
	task := func() []string {
		t.Helper()
		all, err := st.Tasks()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, z := range all {
			if mod, _ := ModuleOfTask(z.System); mod == "канал" {
				out = append(out, z.Title+" · "+z.Schedule)
			}
		}
		sort.Strings(out)
		return out
	}
	want := []string{"Вечерний пост · 19:00", "Утренний пост · 9:00"}
	check := func(what string, want []string) {
		t.Helper()
		got := task()
		if len(got) != len(want) {
			t.Fatalf("%s: задачи %v, а ждали %v", what, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: задачи %v, а ждали %v", what, got, want)
			}
		}
	}

	if err := e.syncModuleTasks(m); err != nil {
		t.Fatal(err)
	}
	check("первая установка", want)

	// обновление модуля на месте: сверка не должна переставить или склеить
	for i := 0; i < 3; i++ {
		if err := e.syncModuleTasks(m); err != nil {
			t.Fatal(err)
		}
	}
	check("повторные сверки", want)

	// вечернюю задачу из манифеста убрали — её задача в базе не остаётся сиротой
	m.Tasks = m.Tasks[:1]
	if err := e.syncModuleTasks(m); err != nil {
		t.Fatal(err)
	}
	check("вечернюю убрали", []string{"Утренний пост · 9:00"})
}
