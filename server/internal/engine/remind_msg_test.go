package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Вернуться к письму позже» — это напоминание, а не прятанье. Письмо
// остаётся там, где лежит: спрятанная почта — потерянная почта.
func TestReminderAboutMessageNotHidesIts(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{MessageID: "<счёт@т>", Subject: "Счёт за август",
		FromAddr: "billing@энерго.рф", FromName: "Горэнерго",
		Status: store.StatusInboxOK, Date: time.Now()}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := e.RemindAboutMessage(m.ID, "завтра"); err != nil {
		t.Fatal(err)
	}
	// письмо не тронуто
	fresh, err := st.Message(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != store.StatusInboxOK {
		t.Errorf("состояние письма изменилось на %q", fresh.Status)
	}
	// и оно по-прежнему ищется
	if list, _ := st.SearchMessages("горэнерго", "", nil, 10); len(list) != 1 {
		t.Error("после напоминания письмо пропало из поиска")
	}

	n, ok := st.OpenNotificationByKey("msg-later:" + itoa64(m.ID))
	if !ok {
		t.Fatal("напоминания нет")
	}
	if n.RemindAt == "" || n.Due == "" {
		t.Error("у напоминания нет срока — задача «Сроки» его не поднимет")
	}
	if n.Ref != "msg:"+itoa64(m.ID) {
		t.Errorf("напоминание не связано с письмом: %q", n.Ref)
	}
	// повтор заменяет прежнее, а не плодит второе
	if err := e.RemindAboutMessage(m.ID, "через неделю"); err != nil {
		t.Fatal(err)
	}
	list, err := st.Notifications(true, 50)
	if err != nil {
		t.Fatal(err)
	}
	howMany := 0
	for _, x := range list {
		if x.Key == "msg-later:"+itoa64(m.ID) {
			howMany++
		}
	}
	if howMany != 1 {
		t.Errorf("напоминаний об одном письме %d", howMany)
	}
}

// Тема секретного письма в напоминание не идёт: запись видна в списке и
// уходит в канал доставки, а тот проходит через чужую службу.
func TestSubjectSecretNotInReminder(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{MessageID: "<тайна@т>", Subject: "Паспорт: серия и номер",
		FromAddr: "hr@работа.рф", Status: store.StatusInboxOK, Date: time.Now()}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContentClass(store.ClassMsg, m.ID, store.ClassSecret, "человек", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.RemindAboutMessage(m.ID, "завтра"); err != nil {
		t.Fatal(err)
	}
	n, ok := st.OpenNotificationByKey("msg-later:" + itoa64(m.ID))
	if !ok {
		t.Fatal("напоминания нет")
	}
	if strings.Contains(n.Title, "Паспорт") {
		t.Errorf("тема секретного письма попала в напоминание: %q", n.Title)
	}
	if !strings.Contains(n.Title, "секретным") {
		t.Errorf("напоминание не говорит, почему темы нет: %q", n.Title)
	}
}

// Непонятный срок — отказ, а не молчаливое «никогда».
func TestUnclearDueRejected(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{MessageID: "<a@т>", Subject: "Счёт", Status: store.StatusInboxOK}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	for _, when := range []string{"", "как-нибудь", "вчера"} {
		if err := e.RemindAboutMessage(m.ID, when); err == nil {
			t.Errorf("срок %q принят молча", when)
		}
	}
}
