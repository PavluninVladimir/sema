package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func messageTest(t *testing.T, st *store.Store, addr, status string, n int) int64 {
	t.Helper()
	m := &store.Message{Account: "проба", MessageID: fmt.Sprintf("<%s-%d@тест>", addr, n), UID: uint32(n),
		FromAddr: addr, Subject: "рассылка", Snippet: "скидки на всё", Date: time.Now(), Status: status}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

// Правило «спамер» касается и писем того же адреса, которые уже ждут
// решения: «уже несколько раз отправлял в спам, а отправитель всё
// просится» — просились соседние письма (13 сентября 2026).
func TestRuleSpammerTakesPendingMessages(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	var own []int64
	for i := 0; i < 3; i++ {
		own = append(own, messageTest(t, st, "promo@spam.example", store.StatusPending, i))
	}
	foreign := messageTest(t, st, "friend@ok.example", store.StatusPending, 9)
	if n := e.applySpammerRule("promo@spam.example"); n != 3 {
		t.Fatalf("уехало %d писем, ждали 3", n)
	}
	for _, id := range own {
		m, _ := st.Message(id)
		if m.Status != store.StatusQueued {
			t.Errorf("письмо %d: %s, ждали queued", id, m.Status)
		}
	}
	if m, _ := st.Message(foreign); m.Status != store.StatusPending {
		t.Errorf("чужое письмо тронуто: %s", m.Status)
	}
}

// Несколько писем адреса в спаме у провайдера — адрес спамер и у Сёмы;
// белый список сильнее, порог 0 выключает учёбу, корзина не считается.
func TestLearningOfSpamHost(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	rule := func(addr string) string {
		rules, _ := st.Rules()
		return matchRule(rules, addr)
	}
	// два письма — ещё не спамер, третье — спамер
	for i := 0; i < 2; i++ {
		messageTest(t, st, "news@shop.example", store.StatusServerSpam, i)
		e.learnFromServerSpam("news@shop.example")
	}
	if r := rule("news@shop.example"); r != "" {
		t.Fatalf("после двух писем уже правило %q", r)
	}
	waited := messageTest(t, st, "news@shop.example", store.StatusPending, 7)
	messageTest(t, st, "news@shop.example", store.StatusServerSpam, 2)
	e.learnFromServerSpam("news@shop.example")
	if r := rule("news@shop.example"); r != "spam" {
		t.Fatalf("после трёх писем правило %q, ждали spam", r)
	}
	if m, _ := st.Message(waited); m.Status != store.StatusQueued {
		t.Errorf("ждущее письмо спамера не уехало: %s", m.Status)
	}
	// белый список сильнее
	_ = st.AddRule("sender", "bank@ok.example", "ham")
	for i := 0; i < 4; i++ {
		messageTest(t, st, "bank@ok.example", store.StatusServerSpam, i)
		e.learnFromServerSpam("bank@ok.example")
	}
	if r := rule("bank@ok.example"); r != "ham" {
		t.Errorf("белый список переписан: %q", r)
	}
	// порог 0 — не учиться
	if err := e.SetServerSpamVotes(0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		messageTest(t, st, "zero@x.example", store.StatusServerSpam, i)
		e.learnFromServerSpam("zero@x.example")
	}
	if r := rule("zero@x.example"); r != "" {
		t.Errorf("при пороге 0 появилось правило %q", r)
	}
}
