package engine

import (
	"github.com/PavluninVladimir/sema/server/internal/store"
	"os"
	"path/filepath"
	"testing"
)

// Поднятая из копии Сёма не трогает внешний мир, пока человек не
// подтвердит привязку; подтверждение снимает состояние насовсем.
func TestNeededBindingAfterRecovery(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if e.NeedsBinding() || e.heldByBinding(&store.Task{System: WorkerPoll}) {
		t.Fatal("без маркера привязка не требуется")
	}
	if err := os.WriteFile(filepath.Join(e.conf.Data, bindingMarker), []byte("копия x · снята 2026-09-07"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !e.NeedsBinding() || !e.heldByBinding(&store.Task{System: WorkerPoll}) || !e.heldByBinding(&store.Task{System: WorkerFileFolders}) || !e.heldByBinding(&store.Task{System: "module:watch:check"}) || !e.heldByBinding(&store.Task{ScriptID: 3}) {
		t.Fatal("после восстановления почта и папки должны стоять")
	}
	if !e.heldByBinding(&store.Task{System: WorkerAttachments}) || !e.heldByBinding(&store.Task{System: WorkerModuleEvents}) || !e.heldByBinding(&store.Task{System: WorkerUpdate}) {
		t.Fatal("вложения, события модулей и проверка версии должны стоять")
	}
	if e.heldByBinding(&store.Task{System: WorkerPulse}) || e.heldByBinding(&store.Task{System: WorkerMemory}) {
		t.Fatal("внутренние работы не должны стоять")
	}
	cards, level := e.HealthCards()
	found := false
	for _, c := range cards {
		if c.Key == "binding" {
			found = true
		}
	}
	if !found || level != "bad" {
		t.Fatalf("«Здоровье» не показывает привязку: %v", level)
	}
	if err := e.ConfirmBinding(); err != nil {
		t.Fatal(err)
	}
	if e.NeedsBinding() {
		t.Fatal("подтверждение не снято")
	}
}
