package engine

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Расписание, поставленное человеком, переживает перезапуск. До 10
// сентября 2026 манифест модуля был источником правды для расписания, и
// «Утренняя проверка серверов» упорно возвращалась на 8:00, хотя окно
// самостоятельной работы стояло на 02:00–04:00. Человек менял час, а
// следующий запуск сервера молча всё отменял — и нигде об этом не
// говорилось.
func TestScheduleHumanSurvivesManifest(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	module := &Module{Name: "watch", Title: "Серверы под присмотром",
		Tasks: []ModuleTask{{Title: "Утренняя проверка серверов", Schedule: "8:00", Run: "check"}}}
	if err := e.syncModuleTasks(module); err != nil {
		t.Fatal(err)
	}
	find := func() *store.Task {
		t.Helper()
		tasks, err := st.Tasks()
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range tasks {
			if x.System == ModuleTaskSystem("watch", "check") {
				return x
			}
		}
		t.Fatal("задачи модуля нет")
		return nil
	}
	task := find()
	if task.Schedule != "8:00" {
		t.Fatalf("расписание из манифеста: %q", task.Schedule)
	}

	// человек переносит задачу в окно самостоятельной работы
	if err := st.SetTaskSchedule(task.ID, "2:30", 0); err != nil {
		t.Fatal(err)
	}
	// и перезапуск (та же синхронизация) его не отменяет
	if err := e.syncModuleTasks(module); err != nil {
		t.Fatal(err)
	}
	if got := find().Schedule; got != "2:30" {
		t.Errorf("манифест отменил решение человека: %q", got)
	}

	// а название манифест по-прежнему правит: его человек не трогал
	module.Tasks[0].Title = "Ночная проверка серверов"
	if err := e.syncModuleTasks(module); err != nil {
		t.Fatal(err)
	}
	fresh := find()
	if fresh.Title != "Ночная проверка серверов" {
		t.Errorf("название из манифеста не применилось: %q", fresh.Title)
	}
	if fresh.Schedule != "2:30" {
		t.Errorf("расписание человека потерялось при обновлении названия: %q", fresh.Schedule)
	}
}

// Пока человек расписание не трогал, манифест им управляет как прежде:
// иначе обновление модуля перестало бы что-либо значить.
func TestUntouchedScheduleGoesForManifest(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	module := &Module{Name: "rates", Title: "Курсы валют",
		Tasks: []ModuleTask{{Title: "Обновить курсы", Schedule: "каждые 30 мин", Run: "refresh"}}}
	if err := e.syncModuleTasks(module); err != nil {
		t.Fatal(err)
	}
	module.Tasks[0].Schedule = "каждый час"
	if err := e.syncModuleTasks(module); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range tasks {
		if x.System == ModuleTaskSystem("rates", "refresh") && x.Schedule != "каждый час" {
			t.Errorf("манифест не применился к нетронутому расписанию: %q", x.Schedule)
		}
	}
}
