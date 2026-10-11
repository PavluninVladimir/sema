package engine

import (
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Папка чистится по одному дню за проход, а не целиком.
//
// «Стереть всё разом» у человека с тысячами писем в спаме либо упрётся
// в предел времени задачи посередине, либо так нагрузит почтовый
// сервер, что тот начнёт отказывать — и половина дня окажется стёртой,
// половина нет. День за проход виден в журнале и всегда закрыт целиком.
func TestPurgedByOneDay(t *testing.T) {
	_, st := newTestEngine(t, "", nil)
	days := []string{"2026-07-01", "2026-07-02", "2026-07-03"}
	for i, d := range days {
		for j := 0; j < 3; j++ {
			when, _ := time.Parse("2006-01-02", d)
			m := &store.Message{Account: "я@почта", MessageID: d + string(rune('a'+j)),
				FromAddr: "кто@то", Subject: "рассылка", Status: store.StatusConfirmed,
				Date: when}
			if err := st.InsertMessage(m); err != nil {
				t.Fatal(err)
			}
			if err := st.SetStatus(m.ID, store.StatusConfirmed, &when); err != nil {
				t.Fatal(err)
			}
			_ = i
		}
	}
	due := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	day, messages, err := st.OldestDay("я@почта", "moved_at",
		[]string{store.StatusConfirmed, store.StatusAutoMoved}, due)
	if err != nil {
		t.Fatal(err)
	}
	if day != days[0] {
		t.Errorf("взят день %q, а самый старый — %q", day, days[0])
	}
	if len(messages) != 3 {
		t.Errorf("в дне %d писем, а положили 3: проход берёт не весь день", len(messages))
	}
	for _, m := range messages {
		if got := m.MovedAt.Format("2006-01-02"); got != days[0] {
			t.Errorf("в пачку попало письмо за %s — это уже другой день", got)
		}
	}
}

// Стёртое с сервера второй раз не берётся: иначе каждый проход
// возвращался бы к тем же письмам и журнал рос бы впустую.
func TestErasedSecondOnceNotTake(t *testing.T) {
	_, st := newTestEngine(t, "", nil)
	when := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	m := &store.Message{Account: "я@почта", MessageID: "<один@проба>", FromAddr: "кто@то",
		Subject: "рассылка", Status: store.StatusConfirmed, Date: when}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(m.ID, store.StatusConfirmed, &when); err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	statuses := []string{store.StatusConfirmed, store.StatusAutoMoved}
	if _, messages, _ := st.OldestDay("я@почта", "moved_at", statuses, due); len(messages) != 1 {
		t.Fatalf("письмо не попало в чистку: %d", len(messages))
	}
	if err := st.LogDeletion(m.ID, "я@почта", "Спам", spamPolicy); err != nil {
		t.Fatal(err)
	}
	if _, messages, _ := st.OldestDay("я@почта", "moved_at", statuses, due); len(messages) != 0 {
		t.Errorf("уже стёртое письмо взято снова: %d", len(messages))
	}
}

// Каждое удаление записано: у Сёмы копия остаётся навсегда, но человек
// вправе знать, чего больше нет на почтовом сервере.
func TestDeleteReachesInLog(t *testing.T) {
	_, st := newTestEngine(t, "", nil)
	m := &store.Message{Account: "я@почта", MessageID: "<два@проба>", FromAddr: "банк@там",
		Subject: "Счёт за август", Status: store.StatusConfirmed, Date: time.Now()}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := st.LogDeletion(m.ID, "я@почта", "Спам", spamPolicy); err != nil {
		t.Fatal(err)
	}
	list, err := st.RecentDeletions(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("журнал пуст: %v", err)
	}
	z := list[0]
	if z.Folder != "Спам" || z.Policy != spamPolicy {
		t.Errorf("запись не называет, откуда и почему: %+v", z)
	}
	// письмо у Сёмы осталось — это и есть смысл записи
	if z.Subject != "Счёт за август" {
		t.Errorf("журнал потерял связь с письмом: %q", z.Subject)
	}
	if st.DeletionsSince(1) != 1 {
		t.Error("счётчик удалений за сутки не считает")
	}
}

// Письма, ждущие решения человека, срок хранения не трогает: стереть
// то, о чём ещё не спросили, значит решить за него.
func TestPendingDecisionsNotErased(t *testing.T) {
	for _, s := range dueStatuses {
		if s == store.StatusPending || s == store.StatusQueued {
			t.Errorf("под срок хранения попало состояние %q — это решение человека", s)
		}
		if s == store.StatusConfirmed || s == store.StatusAutoMoved {
			t.Errorf("спам попал под общий срок %q, а у него свой, короче", s)
		}
	}
}

// Обычные письма не стираются, пока человек не разрешил.
func TestPlainNotErasedWithoutPermission(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if e.PurgeRest() {
		t.Error("по умолчанию Сёма стирает переписку человека с сервера")
	}
	if got := e.RestTTL(); got != 30 {
		t.Errorf("срок по умолчанию %d дней", got)
	}
	if err := e.SetPurgeRest(true); err != nil {
		t.Fatal(err)
	}
	if !e.PurgeRest() {
		t.Error("разрешение не запомнилось")
	}
	if err := e.SetRestTTL(0); err == nil {
		t.Error("ноль дней принят за срок хранения")
	}
}

// Срок спама считается от даты письма, а не от того, когда Сёма унёс
// его в спам. 1 сентября 2026 массовый разбор задним числом поставил
// тысяче старых писем один moved_at, и в спаме на почтовом сервере
// осталась почта за апрель: настройка обещала «через 15 дней», а
// письмо от апреля собиралось уехать только в середине сентября — и
// все разом, одним днём, ровно тем комом, которого чистка по дням и
// должна избегать.
func TestDueSpamFromDatesMessages(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	older := time.Now().AddDate(0, 0, -120)
	message := &store.Message{
		MessageID: "<старое@пример>", FromAddr: "спамер@пример.рф", Subject: "реклама",
		Date: older, Status: store.StatusConfirmed, Account: "я@пример.рф",
	}
	if err := st.InsertMessage(message); err != nil {
		t.Fatal(err)
	}
	// осуждено только вчера — по старой мерке ждать ещё две недели
	when := time.Now().AddDate(0, 0, -1)
	if err := st.SetStatus(message.ID, store.StatusConfirmed, &when); err != nil {
		t.Fatal(err)
	}
	// судим сам выбор движка, а не свой пересказ хранилища
	day, messages, err := e.spamUnderDue("я@пример.рф")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		t.Fatalf("письмо от %s под срок не попало — в спаме останется почта за апрель", older.Format("2006-01-02"))
	}
	if day != older.Format("2006-01-02") {
		t.Errorf("день чистки %q, а дата письма %q — считаем не от письма", day, older.Format("2006-01-02"))
	}
}
