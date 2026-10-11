package store

import "testing"

// Черновик можно поправить, пока он ждёт: одно неверное слово не должно
// стоить всего текста. Решённый черновик не правится — отправленное
// назад не вернёшь.
func TestDraftEditableWhileWaiting(t *testing.T) {
	st := testStore(t)
	id, err := st.AddTGOutbox(&TGOutbox{ChatID: 7, SemaChat: 3, Who: "Анна", Text: "Пришлю завтра"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EditTGDraft(id, "Пришлю в понедельник"); err != nil {
		t.Fatal(err)
	}
	o, ok := st.TGOutboxByID(id)
	if !ok || o.Text != "Пришлю в понедельник" {
		t.Fatalf("текст не поправился: %+v", o)
	}
	if o.State != "waiting" {
		t.Errorf("правка не должна решать за человека: состояние %q", o.State)
	}
	if err := st.DecideTGOutbox(id, "rejected", "не надо"); err != nil {
		t.Fatal(err)
	}
	if err := st.EditTGDraft(id, "поздно"); err == nil {
		t.Error("решённый черновик править нельзя")
	}
}
