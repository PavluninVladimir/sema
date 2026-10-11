package store

import (
	"path/filepath"
	"testing"
	"time"
)

// Работа не должна пропадать из-за смерти того, кто её взял. Аренда
// возвращается по сроку сама, без участия бравшего: он мог быть убит
// `docker kill` посреди работы и уже ничего не вернёт.
func TestLeaseReturnsWithoutHolder(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "очередь.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.Enqueue("проба", "объект-1", "почему", 5); err != nil {
		t.Fatal(err)
	}
	// первый берёт с короткой арендой и «умирает», ничего не вернув.
	// Аренда — не миллисекунда: под детектором гонок два вызова подряд
	// занимают больше, срок честно выходил между ними, и тест винил
	// очередь в том, что она работает как обещано
	taken, err := st.TakeWork(10, 200*time.Millisecond)
	if err != nil || len(taken) != 1 {
		t.Fatalf("не взялось: %v, %d", err, len(taken))
	}
	// пока аренда жива, второй её не видит
	more, _ := st.TakeWork(10, time.Minute)
	if len(more) != 0 {
		t.Fatal("работу взяли дважды при живой аренде")
	}
	time.Sleep(300 * time.Millisecond)

	// срок вышел — работа снова свободна, и её берёт кто угодно
	again, err := st.TakeWork(10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Ref != "объект-1" {
		t.Fatalf("работа не вернулась в очередь: %d", len(again))
	}
	if again[0].Attempts != taken[0].Attempts {
		t.Fatalf("возврат по сроку сжёг попытку: было %d, стало %d",
			taken[0].Attempts, again[0].Attempts)
	}
}

// Пара «вид + объект» уникальна среди незавершённых: иначе перезапуск
// сервера наплодит двойников той же работы.
func TestWorkIsNotDuplicated(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "очередь.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for i := 0; i < 3; i++ {
		if err := st.Enqueue("разбор", "письмо-7", "повтор", 5); err != nil {
			t.Fatal(err)
		}
	}
	taken, err := st.TakeWork(10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(taken) != 1 {
		t.Fatalf("одна и та же работа встала в очередь %d раз", len(taken))
	}
	// завершённая не мешает поставить ту же работу заново
	if err := st.FinishWork(taken[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue("разбор", "письмо-7", "снова", 5); err != nil {
		t.Fatal(err)
	}
	retaken, _ := st.TakeWork(10, time.Minute)
	if len(retaken) != 1 {
		t.Fatalf("после завершения та же работа не ставится заново: %d", len(retaken))
	}
}

// Внешнее действие повторять нельзя: очередь для того и повторяет работу,
// что внутренние работы безобидны, а перенос письма или команда на сервере —
// нет. Проверяем самый злой случай: процесс убит между действием и записью
// результата.
func TestOutsideActionNotDoneTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "внешнее.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := "перенос письма msg-42 в спам"

	// первый заход: отметились и «сделали снаружи»
	repeat, err := st.OutsideStarted(key)
	if err != nil {
		t.Fatal(err)
	}
	if repeat {
		t.Fatal("первый заход посчитан повтором")
	}
	if err := st.OutsideDone(key); err != nil {
		t.Fatal(err)
	}
	// ...и тут процесс убили, не записав результат работы
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	repeat, err = st2.OutsideStarted(key)
	if err != nil {
		t.Fatal(err)
	}
	if !repeat {
		t.Fatal("повтор пошёл вслепую — письмо перенесётся дважды")
	}
	if st2.OutsideState(key) != "done" {
		t.Fatalf("состояние потерялось: %q", st2.OutsideState(key))
	}

	// человек разобрался и разрешил заново
	if err := st2.ForgetOutside(key); err != nil {
		t.Fatal(err)
	}
	if repeat, _ := st2.OutsideStarted(key); repeat {
		t.Fatal("после разрешения человека действие всё ещё считается сделанным")
	}
}

// Убили между действием и записью «сделано» — состояние остаётся «делаю»,
// и это честнее, чем притвориться, что не начинали.
func TestKilledMidOutsideAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "внешнее2.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := "команда на srv-bots: systemctl restart nginx"
	if _, err := st.OutsideStarted(key); err != nil {
		t.Fatal(err)
	}
	_ = st.Close() // убили ровно здесь

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if got := st2.OutsideState(key); got != "doing" {
		t.Fatalf("состояние после убийства: %q, а должно быть «делаю»", got)
	}
	if repeat, _ := st2.OutsideStarted(key); !repeat {
		t.Fatal("повтор не заметил, что действие уже начиналось")
	}
}
