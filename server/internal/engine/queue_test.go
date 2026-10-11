package engine

import "testing"

// Очередь смотрит не только на процессор: на Pi локальная модель держит
// четыре с половиной гигабайта из восьми, подкачка кончается раньше, чем
// нагрузка дорастает до предела, и машина перестаёт считать, а начинает
// перекладывать страницы.
func TestQueueLooksOnMemory(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if e.QueueMinFreeMB() != 400 {
		t.Fatalf("умолчание предела памяти: %d", e.QueueMinFreeMB())
	}
	if err := e.store.SetSetting("queue.min_free_mb", "0"); err != nil {
		t.Fatal(err)
	}
	if e.QueueMinFreeMB() != 0 {
		t.Error("ноль должен означать «не смотреть»")
	}
	if err := e.store.SetSetting("queue.min_free_mb", "чепуха"); err != nil {
		t.Fatal(err)
	}
	if e.QueueMinFreeMB() != 400 {
		t.Error("непонятное значение должно давать умолчание, а не ноль")
	}
}
