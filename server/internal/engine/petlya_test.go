package engine

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Петля, какой она была 11 сентября: один файл, много работ, след не
// меняется. Наблюдатель обязан увидеть её по следу, а не по числу работ:
// работа, которая каждый раз что-то меняет, петлёй не считается.
func TestRetryWithoutTraceVisibleInQueue(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	a, _, err := e.AddFile(context.Background(), "qr.png", "image/png",
		[]byte("картинка"), FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := strconv.FormatInt(a.ID, 10)
	trace, ok := e.Trace(WorkFileParse, ref)
	if !ok {
		t.Fatal("следа нет")
	}
	// шесть работ подряд, и все оставили один отпечаток
	for i := 0; i < 6; i++ {
		if err := st.Enqueue(WorkFileParse, ref, "", 5); err != nil {
			t.Fatal(err)
		}
		batch, err := st.TakeWork(1, time.Minute)
		if err != nil || len(batch) == 0 {
			t.Fatalf("работа не взялась: %v", err)
		}
		w := batch[0]
		if err := st.RememberTrace(w.ID, trace); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishWork(w.ID); err != nil {
			t.Fatal(err)
		}
	}
	retries, err := st.RepeatsWithoutTrace(5, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range retries {
		if p.Kind == WorkFileParse && p.Ref == ref {
			found = true
			if p.Count < 6 {
				t.Errorf("повторов насчитано %d из шести", p.Count)
			}
		}
	}
	if !found {
		t.Fatal("петля не найдена: шесть работ по одному объекту с одним следом")
	}
}

// А работа, которая каждый раз что-то меняет, петлёй не считается — иначе
// опрос почты и пульс попадут в подозреваемые.
func TestWorkWithTraceNotLoop(t *testing.T) {
	_, st := newTestEngine(t, "тест", nil)
	for i := 0; i < 8; i++ {
		if err := st.Enqueue(WorkMsgJudge, "77", "", 5); err != nil {
			t.Fatal(err)
		}
		batch, err := st.TakeWork(1, time.Minute)
		if err != nil || len(batch) == 0 {
			t.Fatalf("работа не взялась: %v", err)
		}
		w := batch[0]
		// каждый проход оставляет свой отпечаток
		if err := st.RememberTrace(w.ID, "след-"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishWork(w.ID); err != nil {
			t.Fatal(err)
		}
	}
	retries, err := st.RepeatsWithoutTrace(5, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range retries {
		if p.Ref == "77" {
			t.Errorf("работа со сменой следа объявлена петлёй: %d повторов", p.Count)
		}
	}
}
