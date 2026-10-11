package engine

import "testing"

// Трекеры задач сняты 30 сентября 2026: ни строки сердцебиения, ни вида
// работы очереди, ни обработчика у них не осталось.
func TestNoTrackerWorkerOrQueueKind(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	for _, b := range builtinTasks {
		if b.System == "trackers" {
			t.Errorf("встроенная задача %q осталась", b.Title)
		}
	}
	if _, ok := e.queueHandlers()["tracker_poll"]; ok {
		t.Error("у очереди остался обработчик tracker_poll")
	}
	if _, ok := QueueKinds["tracker_poll"]; ok {
		t.Error("у очереди остался вид tracker_poll")
	}
	if _, ok := e.traces()["tracker_poll"]; ok {
		t.Error("у вида tracker_poll остался след")
	}
}
