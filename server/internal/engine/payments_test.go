package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ближайший платёж — то, чего почтовый ящик не знает: он показывает
// письма, а Сёма видит документ внутри и срок по нему.
func TestNearestPayment(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	through := time.Now().AddDate(0, 0, 20).Format("2006-01-02")
	far := count(t, e, st, "АО «Горэнерго»", 900, through, "д")
	near := count(t, e, st, "АО «Горэнерго»", 512.30, tomorrow, "б")

	a, line := e.NearestPayment(nil)
	if a == nil {
		t.Fatal("платёж есть, а Сёма молчит")
	}
	if a.ID != near {
		t.Errorf("выбран счёт %d, а ближайший — %d", a.ID, near)
	}
	if !strings.Contains(line, "512,30") || !strings.Contains(line, "Горэнерго") {
		t.Errorf("строка платежа: %q", line)
	}
	_ = far

	// оплатили — и Сёма перестаёт про него говорить, переходя к дальнему
	receipt(t, e, st, "АО «Горэнерго»", 512.30, "ч")
	a, _ = e.NearestPayment(nil)
	if a == nil || a.ID != far {
		t.Errorf("после чека ближайшим должен стать дальний счёт, а Сёма показывает %v", a)
	}
}

// «Счёт есть, чека нет» — Сёма говорит именно это, а не «ты не заплатил».
// Разница между этими двумя фразами — разница между ассистентом, которому
// верят, и тем, которого выключают.
func TestCountWithoutReceiptSaysCareful(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	long := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	count(t, e, st, "АО «Горэнерго»", 3480, long, "а")

	if n := e.paymentsPass(time.Now()); n != 1 {
		t.Fatalf("сказано о %d счетах, а он один", n)
	}
	list, err := st.Notifications(true, 10)
	if err != nil || len(list) == 0 {
		t.Fatal("уведомления нет")
	}
	n := list[0]
	if n.Level != "warn" {
		t.Errorf("уровень %q: Сёма не знает, оплачен ли счёт, — это не тревога", n.Level)
	}
	if strings.Contains(n.Summary, "не оплатили") || strings.Contains(n.Summary, "не заплатил") {
		t.Errorf("Сёма утверждает то, чего не знает: %q", n.Summary)
	}
	if !strings.Contains(n.Summary, "не видела") {
		t.Errorf("итог должен говорить про то, что Сёма видит, а не про поступки человека: %q", n.Summary)
	}

	// пришёл чек — и разговор окончен
	receipt(t, e, st, "АО «Горэнерго»", 3480, "б")
	if n := e.paymentsPass(time.Now()); n != 0 {
		t.Errorf("чек есть, а Сёма всё ещё спрашивает про %d счетов", n)
	}
}

// Допуск на копейки: банк округляет, комиссия добавляется, а чек бывает
// выписан на копейку иначе. Но чужая сумма оплатой не считается.
func TestReceiptWithDiffInKopecks(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	long := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	count(t, e, st, "Ростелеком", 1000, long, "а")
	receipt(t, e, st, "Ростелеком", 1000.50, "б")
	if n := e.paymentsPass(time.Now()); n != 0 {
		t.Errorf("чек на 1000,50 за счёт на 1000 не зачтён")
	}

	e2, st2 := newTestEngine(t, "", nil)
	count(t, e2, st2, "Ростелеком", 1000, long, "а")
	receipt(t, e2, st2, "Ростелеком", 300, "б")
	if n := e2.paymentsPass(time.Now()); n != 1 {
		t.Errorf("чек на 300 зачтён за счёт на 1000: сказано о %d", n)
	}
}

// Проверка независимо от «Снимка»: тест TestСнимокБезЗакрытыхЯщиков не ловит
// утечку через «Ближайший платёж» на живых данных — счёт без вендора и
// суммы рисуется строкой «без поставщика · до …», и зарубка из имени файла
// туда не попадает; получилось бы зелено даже без фильтра. Здесь проверяем
// сам БлижайшийПлатёж напрямую: единственный счёт лежит в закрытом ящике —
// без excluded он находится, с excluded — нет.
func TestNearestPaymentFiltersClosedAccount(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	msg := &store.Message{Account: "закрытый", MessageID: "<платёж-закрытый@пример.рф>", Subject: "Счёт"}
	if err := st.InsertMessage(msg); err != nil {
		t.Fatalf("вставить письмо: %v", err)
	}
	att := &store.Attachment{MessageID: msg.ID, Filename: "счёт.pdf"}
	if err := st.InsertAttachment(att); err != nil {
		t.Fatalf("вставить вложение: %v", err)
	}
	// InsertAttachment не пишет to_pay/due_date вовсе (их нет в её INSERT) —
	// без этого вызова счёт до BillsDue не доехал бы, и тест ничего не
	// проверял бы, как и было с зарубкой в TestСнимокБезЗакрытыхЯщиков
	due := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	if err := st.SetFileDates(att.ID, "", due, true); err != nil {
		t.Fatalf("проставить срок: %v", err)
	}

	// без отсева счёт закрытого ящика виден — иначе тест ничего не проверяет
	a, _ := e.NearestPayment(nil)
	if a == nil || a.ID != att.ID {
		t.Fatalf("без excluded счёт закрытого ящика не найден: %v", a)
	}

	// с отсевом — единственный счёт лежит в закрытом ящике, платить нечего
	a, _ = e.NearestPayment(&store.Exclusion{Accounts: map[string]bool{"закрытый": true}})
	if a != nil {
		t.Errorf("ДЫРА: «Ближайший платёж» вернул счёт закрытого ящика %d при включённом отсеве", a.ID)
	}
}

func count(t *testing.T, e *Engine, st *store.Store, who string, amount float64, due, sha string) int64 {
	t.Helper()
	a, _ := providerDocument(t, e, st, who, amount, sha)
	// дата счёта — не срок оплаты: счёт выписан сегодня, платить до срока
	issued := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if err := st.SetFileDates(a.ID, issued, due, true); err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func receipt(t *testing.T, e *Engine, st *store.Store, who string, amount float64, sha string) int64 {
	t.Helper()
	a, _ := providerDocument(t, e, st, who, amount, sha)
	if err := st.SetFileDates(a.ID, time.Now().Format("2006-01-02"), "", false); err != nil {
		t.Fatal(err)
	}
	return a.ID
}
