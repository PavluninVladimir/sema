package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Причина, которая сама не проходит, не должна крутить работу вечно:
// после нескольких откладываний работа встаёт и зовёт человека.
func TestConstantReasonStopsWork(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.Enqueue("memory_review", "7", "", 5); err != nil {
		t.Fatal(err)
	}
	list, _ := st.WorkList("memory_review", store.WorkWaiting, 5)
	if len(list) != 1 {
		t.Fatalf("работа не встала в очередь: %+v", list)
	}
	id := list[0].ID
	limit := e.postponeLimit()
	for i := 0; i < limit; i++ {
		n, err := st.PostponeWorkN(id, time.Now(), "область «серверы» закрыта наружу")
		if err != nil {
			t.Fatal(err)
		}
		if n != i+1 {
			t.Fatalf("откладывания не считаются: %d на шаге %d", n, i+1)
		}
	}
	if err := st.HoldWork(id, "не проходит само: область закрыта"); err != nil {
		t.Fatal(err)
	}
	held, _ := st.WorkList("memory_review", store.WorkHeld, 5)
	if len(held) != 1 || !strings.Contains(held[0].LastErr, "не проходит само") {
		t.Fatalf("работа не встала: %+v", held)
	}
	if wait, _ := st.WorkList("memory_review", store.WorkWaiting, 5); len(wait) != 0 {
		t.Fatalf("работа осталась в ожидании: %+v", wait)
	}
}

// Здоровье не краснеет от одного отказа при пяти вызовах: доля считается
// от разумного числа вызовов.
func TestHealthNotReddensOnSmallNumbers(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	for i := 0; i < 4; i++ {
		_ = st.AddUsage(&store.UsageRow{At: time.Now(), Consumer: "core:проба", Kind: "llm", Model: "м", OK: true})
	}
	_ = st.AddUsage(&store.UsageRow{At: time.Now(), Consumer: "core:проба", Kind: "llm", Model: "м", OK: false, Note: "таймаут"})
	cards, level := e.HealthCards()
	if level == "bad" {
		t.Fatalf("один отказ из пяти покрасил здоровье в красный: %s", level)
	}
	for _, c := range cards {
		if c.Key == "models" && !strings.Contains(c.Meta, "доля считается") {
			t.Fatalf("карточка не объясняет, почему доли нет: %q", c.Meta)
		}
	}
}
