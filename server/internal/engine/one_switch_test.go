package engine

import "testing"

// Один выключатель, а не два. На живой Сёме 10 сентября 2026 настройки
// «Копия наружу» и «Разгрузка файлов» стояли включёнными, а сами задачи
// сердцебиения — выключенными: копия не снималась, файлы не разгружались,
// а экран показывал «включено». Ошибки при этом не было нигде.
func TestToggleClicksAndTask(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	// строки задач заводит сердцебиение при старте; в тесте его нет
	e.ensureBuiltinTasks()

	if e.BackupOn() || e.BackupTaskOn() {
		t.Fatal("копия по умолчанию должна быть выключена")
	}
	if err := e.SetBackupOn(true); err != nil {
		t.Fatal(err)
	}
	if !e.BackupOn() {
		t.Error("настройка не включилась")
	}
	if !e.BackupTaskOn() {
		t.Error("задача осталась выключенной — копия не снимется, а экран скажет «включено»")
	}
	if err := e.SetBackupOn(false); err != nil {
		t.Fatal(err)
	}
	if e.BackupTaskOn() {
		t.Error("задача осталась включённой после выключения настройки")
	}

	// то же у разгрузки: она удаляет местные копии, и расхождение здесь
	// опаснее — человек думает, что место освобождается, а оно нет
	if err := e.SetOffload(true); err != nil {
		t.Fatal(err)
	}
	if !e.OffloadOn() || !e.OffloadTaskOn() {
		t.Error("разгрузка: настройка и задача разошлись")
	}
	if err := e.SetOffload(false); err != nil {
		t.Fatal(err)
	}
	if e.OffloadTaskOn() {
		t.Error("задача разгрузки осталась включённой")
	}
}
