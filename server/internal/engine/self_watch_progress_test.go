package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Счётчик, который стоит, пока задача работает, — это зеркало, месяц
// простоявшее на нуле из двухсот шестидесяти. Экран показывал строку
// «зеркало» без единого числа, и это выглядело порядком.
func TestStandingCounterFound(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.CreateTask(&store.Task{Title: "Зеркало файлов",
		System: WorkerFilesMirror, Schedule: "каждые 1 мин", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	task := taskBySystem(t, st, WorkerFilesMirror)

	// первый проход: запоминаем, что сделано 0 из 10 на такой-то запуск
	e.countersForTest = func() []workCounter {
		return []workCounter{{Name: "Зеркало файлов", Task: WorkerFilesMirror,
			Where: "settings:storages", Take: func() (int, int) { return 0, 10 }}}
	}
	if err := st.TaskRan(task.ID, "уложено 0"); err != nil {
		t.Fatal(err)
	}
	if n := e.checkCounters(); n != 0 {
		t.Fatalf("на первом проходе сравнивать не с чем, а найдено %d", n)
	}
	// задача отработала ещё раз, а счётчик не сдвинулся
	time.Sleep(1100 * time.Millisecond)
	if err := st.TaskRan(task.ID, "уложено 0"); err != nil {
		t.Fatal(err)
	}
	if n := e.checkCounters(); n == 0 {
		t.Fatal("счётчик стоит, задача работает — а находки нет")
	}
	findings, _ := st.Findings(true, 10)
	found := false
	for _, n := range findings {
		if strings.Contains(n.Summary, "Зеркало файлов") && strings.Contains(n.Numbers, "0 из 10") {
			found = true
		}
	}
	if !found {
		t.Error("находка не называет дело и числа")
	}
}

// Дело, доведённое до конца, счётчиком не растёт и расти не должно:
// «260 из 260» — это порядок, а не остановка.
func TestDoneWholeNotTrouble(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.CreateTask(&store.Task{Title: "Зеркало файлов",
		System: WorkerFilesMirror, Schedule: "каждые 1 мин", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	task := taskBySystem(t, st, WorkerFilesMirror)
	if err := st.TaskRan(task.ID, "готово"); err != nil {
		t.Fatal(err)
	}
	e.countersForTest = func() []workCounter {
		return []workCounter{{Name: "Зеркало файлов", Task: WorkerFilesMirror,
			Take: func() (int, int) { return 260, 260 }}}
	}
	e.checkCounters()
	e.checkCounters()
	if findings, _ := st.Findings(true, 10); len(findings) > 0 {
		t.Errorf("законченное дело объявлено остановкой: %s", findings[0].Summary)
	}
}

// Выключенная задача — решение человека, а не беда. Про выключенное
// говорит другая проверка, и говорить о нём дважды незачем.
func TestDisabledTaskNotCountsStop(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.CreateTask(&store.Task{Title: "Зеркало файлов",
		System: WorkerFilesMirror, Schedule: "каждые 1 мин", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	e.countersForTest = func() []workCounter {
		return []workCounter{{Name: "Зеркало файлов", Task: WorkerFilesMirror,
			Take: func() (int, int) { return 0, 260 }}}
	}
	e.checkCounters()
	e.checkCounters()
	if findings, _ := st.Findings(true, 10); len(findings) > 0 {
		t.Errorf("выключенная задача объявлена остановкой: %s", findings[0].Summary)
	}
}

func taskBySystem(t *testing.T, st *store.Store, system string) *store.Task {
	t.Helper()
	task, err := st.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range task {
		if x.System == system {
			return x
		}
	}
	t.Fatalf("задачи %q нет", system)
	return nil
}
