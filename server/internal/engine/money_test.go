package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Письмо с признаком денег Сёма сам никуда не переносит. На живой Сёме
// двадцать шесть чеков из ста четырёх лежали в письмах, подтверждённых как
// спам: рассылка и счёт приходят с одного адреса, а правило — про адрес.
func TestCountNotLeavesInSpamSelf(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)

	// по словам в теме
	count := &store.Message{FromAddr: "noreply@магазин.рф", Subject: "Счёт-квитанция за август"}
	if !e.holdForMoney(count, "правило: отправитель уже подтверждён как спамер") {
		t.Fatal("счёт не придержан")
	}
	if count.Status != store.StatusPending {
		t.Errorf("статус %q, а счёт должен ждать решения человека", count.Status)
	}
	if count.MovedAt != nil {
		t.Error("у придержанного письма проставлено время переноса")
	}
	if !strings.Contains(count.Reason, "решите сами") || !strings.Contains(count.Reason, "«счёт»") {
		t.Errorf("причина не объясняет, за что письмо задержано: %q", count.Reason)
	}

	// а обычная рассылка от того же отправителя переносится как прежде
	mailing := &store.Message{FromAddr: "noreply@магазин.рф", Subject: "Стильные новинки осени"}
	if e.holdForMoney(mailing, "правило") {
		t.Errorf("рассылка придержана как счёт: %q", mailing.Reason)
	}
}

// История весомее слов: отправитель, от которого уже приходили чеки,
// придерживается независимо от темы — её-то как раз легко подделать.
func TestSenderReceiptsKeeps(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{MessageID: "<чек@т>", FromAddr: "billing@энерго.рф",
		Subject: "Показания", Status: store.StatusHam}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	file := &store.Attachment{MessageID: m.ID, Filename: "счёт.pdf", SHA256: "аб"}
	if err := st.InsertAttachment(file); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkAttachment(file.ID, store.AttKindReceipt, "Энерго", 512.30, "RUB", "счёт за август"); err != nil {
		t.Fatal(err)
	}
	if n := st.ReceiptsFromSender("billing@энерго.рф"); n != 1 {
		t.Fatalf("чеков от отправителя насчитано %d, а он один", n)
	}
	// тема ни о чём не говорит, а придержать надо
	fresh := &store.Message{FromAddr: "billing@энерго.рф", Subject: "Уведомление"}
	if !e.holdForMoney(fresh, "правило") {
		t.Fatal("письмо от отправителя чеков не придержано")
	}
	if !strings.Contains(fresh.Reason, "уже приходили чеки") {
		t.Errorf("причина: %q", fresh.Reason)
	}
}

// Бережение — настройка, а не гвоздь: человек вправе его выключить, и
// слова-приметы у каждого свои.
func TestThriftDisabledAndEditable(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &store.Message{FromAddr: "a@b.рф", Subject: "Счёт за свет"}
	if !e.holdForMoney(m, "правило") {
		t.Fatal("по умолчанию бережение должно работать")
	}
	if err := e.SetMoneyGuard(false); err != nil {
		t.Fatal(err)
	}
	m2 := &store.Message{FromAddr: "a@b.рф", Subject: "Счёт за свет"}
	if e.holdForMoney(m2, "правило") {
		t.Error("бережение выключено, а письмо придержано")
	}
	if err := e.SetMoneyGuard(true); err != nil {
		t.Fatal(err)
	}
	if err := e.SetMoneyWords("накладная"); err != nil {
		t.Fatal(err)
	}
	m3 := &store.Message{FromAddr: "a@b.рф", Subject: "Счёт за свет"}
	if e.holdForMoney(m3, "правило") {
		t.Error("слово убрали из справочника, а оно всё ещё держит")
	}
	m4 := &store.Message{FromAddr: "a@b.рф", Subject: "Накладная № 12"}
	if !e.holdForMoney(m4, "правило") {
		t.Error("своё слово из справочника не сработало")
	}
}
