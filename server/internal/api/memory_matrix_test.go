package api

import (
	"fmt"
	"testing"
)

// TestMatrixCellDoesNotLeaveTab — нажатие на клетку «кто × что»
// переключает выдачу и оставляет человека там, где он был. Раньше
// действие возвращало экран «memory», а это первая вкладка «Записи»:
// каждое нажатие на «Областях и доступе» перебрасывало на соседнюю
// вкладку, и выдать агенту три области значило три раза вернуться.
// Пустой экран в ответе значит «остаться на текущем».
func TestMatrixCellDoesNotLeaveTab(t *testing.T) {
	s, st := testServer(t)
	if err := s.eng.CreateArea("код", "Код"); err != nil {
		t.Fatalf("область: %v", err)
	}
	c, err := st.MemoryConsumer("agent", "Claude Code", "/tmp/проба")
	if err != nil {
		t.Fatalf("потребитель: %v", err)
	}
	target := fmt.Sprintf("%d~код", c.ID)
	for _, действие := range []string{"grant_area_cell", "revoke_consumer_area"} {
		куда, err := s.execute(actionRequest{Action: действие, Target: target, Screen: "memory~areas"})
		if err != nil {
			t.Fatalf("%s: %v", действие, err)
		}
		if куда != "" {
			t.Errorf("%s уводит на %q, а должно оставаться на текущем экране", действие, куда)
		}
	}
}
