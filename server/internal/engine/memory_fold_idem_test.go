package engine

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Свёртка сводки повторяема: сбой между записью в память и заменой сводки
// не даёт второй записи и второго похода к модели.
func TestFoldIdempotentAfterFailure(t *testing.T) {
	fp := &fakeProvider{answer: "Коротко: чинили вёрстку и выкатили."}
	e, st := newTestEngine(t, "модель", fp)
	_ = e.SetRoleField(RoleMemory, "model", "модель")
	c, err := st.MemoryConsumer(store.ConsumerChat, "Сёма", "")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := st.StartMemorySession(c.ID, "проба", "")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("реплика про работу с кодом\n", 400)
	if err := st.SetSessionSummary(sid, long); err != nil {
		t.Fatal(err)
	}
	w := &store.WorkItem{Kind: WorkSessionFold, Ref: strconv.FormatInt(sid, 10)}
	if err := e.handleSessionFold(context.Background(), w); err != nil {
		t.Fatalf("свёртка: %v", err)
	}
	// область «личное» ждёт подтверждения: считаем и принятые, и ждущие
	total := func() int {
		_, byKind := st.CountMemories()
		n := 0
		for _, v := range byKind {
			n += v
		}
		pending, _ := st.PendingMemories()
		return n + len(pending)
	}
	before := total()
	if before == 0 || fp.calls != 1 {
		t.Fatalf("свёртка не записалась: записей %d, вызовов %d", before, fp.calls)
	}
	// сбой после ответа модели и записи в память, но до замены сводки:
	// ключ ещё «делаю», заметка на месте, сводка прежняя, память записана
	if err := st.SetSessionSummary(sid, long); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OutsideStarted("fold:" + strconv.FormatInt(sid, 10)); err != nil {
		t.Fatal(err)
	}
	if err := st.NoteForAction("fold:"+strconv.FormatInt(sid, 10), fp.answer); err != nil {
		t.Fatal(err)
	}
	if err := e.handleSessionFold(context.Background(), w); err != nil {
		t.Fatalf("повтор: %v", err)
	}
	if after := total(); after != before {
		t.Fatalf("повтор положил вторую запись: было %d, стало %d", before, after)
	}
	if fp.calls != 1 {
		t.Fatalf("повтор снова сходил к модели: %d", fp.calls)
	}
	s, _ := st.MemorySession(sid)
	if !strings.HasPrefix(s.Summary, foldedMarkPrefix) {
		t.Fatal("сводка после повтора не свёрнута")
	}
}
