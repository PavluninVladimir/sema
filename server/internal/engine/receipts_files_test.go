package engine

// Чеки ядро больше ни у кого не спрашивает: чек — это сам файл с видом
// «чек» и разобранной суммой. Проверяем на временной базе, что дайджест
// «Почты», экран «Отправители» и опись для модели считают те же числа, и
// что без единого чека все три места работают, а не падают.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const testAccount = "почта@пример.рф"

func messageForReceipt(t *testing.T, st *store.Store, mid, from, subject string, when time.Time) *store.Message {
	t.Helper()
	m := &store.Message{Account: testAccount, MessageID: mid, FromAddr: from, FromName: "Кто-то",
		Subject: subject, Date: when, Status: store.StatusInboxOK}
	if err := st.InsertMessage(m); err != nil {
		t.Fatalf("письмо %s: %v", mid, err)
	}
	return m
}

func fileForReceipt(t *testing.T, st *store.Store, msgID int64, name, kind, vendor string, amount float64) *store.Attachment {
	t.Helper()
	a := &store.Attachment{MessageID: msgID, Filename: name, MIME: "application/pdf", Size: 2048,
		Kind: kind, Vendor: vendor, Amount: amount, Currency: "RUB", Summary: "за август",
		Extracted: "итого " + strconv.FormatFloat(amount, 'f', 2, 64) + " руб"}
	if err := st.InsertAttachment(a); err != nil {
		t.Fatalf("файл %s: %v", name, err)
	}
	return a
}

// sectionDigest — раздел сводки по имени группы справочника.
func sectionDigest(t *testing.T, d *Digest, name string) *DigestSection {
	t.Helper()
	for i := range d.Sections {
		if d.Sections[i].Group.Name == name {
			return &d.Sections[i]
		}
	}
	t.Fatalf("в сводке нет группы %q", name)
	return nil
}

func TestReceiptsTakenOfFiles(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.SetDigestWindow("week"); err != nil {
		t.Fatalf("окно сводки: %v", err)
	}
	now := time.Now()

	// чек прямой связью: файл знает своё письмо
	m1 := messageForReceipt(t, st, "<energo-1>", "energo@example.com", "Счёт за август", now.Add(-2*time.Hour))
	fileForReceipt(t, st, m1.ID, "Счёт.pdf", store.AttKindReceipt, "Т Плюс", 1234.50)

	// чек через источник «письмо»: у файла message_id = 0, связь в file_sources
	m2 := messageForReceipt(t, st, "<shop-1>", "shop@example.com", "Ваш заказ", now.Add(-time.Hour))
	f2 := fileForReceipt(t, st, 0, "Чек.pdf", store.AttKindReceipt, "Лавка", 800.25)
	if err := st.AddFileSource(f2.ID, store.SourceMail, strconv.FormatInt(m2.ID, 10), testAccount); err != nil {
		t.Fatalf("источник файла: %v", err)
	}

	// не чек: обычный документ у письма от человека
	m3 := messageForReceipt(t, st, "<friend-1>", "friend@example.com", "Договор", now.Add(-30*time.Minute))
	fileForReceipt(t, st, m3.ID, "Договор.pdf", store.AttKindDocument, "", 0)

	d, err := e.Digest("", nil)
	if err != nil {
		t.Fatalf("сводка: %v", err)
	}
	sec := sectionDigest(t, d, "receipts")
	if len(sec.Items) != 2 {
		t.Fatalf("в «Счетах и чеках» %d писем, ожидали 2", len(sec.Items))
	}
	if got := sec.Sum["RUB"]; got < 2034.74 || got > 2034.76 {
		t.Errorf("сумма по группе %.2f, ожидали 2034.75", got)
	}
	for _, it := range sec.Items {
		if it.Receipt == nil {
			t.Fatalf("у письма %q нет чека", it.Msg.Subject)
		}
	}
	for _, it := range sec.Items {
		if it.Msg.ID != m1.ID {
			continue
		}
		r := it.Receipt
		if r.Vendor != "Т Плюс" || r.Amount != 1234.50 || r.Currency != "RUB" || r.Summary != "за август" {
			t.Errorf("чек письма: %+v", r)
		}
	}

	docs := sectionDigest(t, d, "documents")
	if len(docs.Items) != 1 || docs.Items[0].Receipt != nil {
		t.Errorf("документ попал в чеки: %d писем, чек %v", len(docs.Items), docs.Items[0].Receipt)
	}
	if len(docs.Sum) != 0 {
		t.Errorf("у группы без чеков появилась сумма: %v", docs.Sum)
	}

	if g := e.DigestGroupOf(m1); g != "receipts" {
		t.Errorf("группа письма с чеком — %q", g)
	}
	if g := e.DigestGroupOf(m3); g == "receipts" {
		t.Errorf("письмо без чека попало в чеки")
	}

	// «Отправители»: у кого есть чеки — считаются файлы-чеки, а не письма
	senders := e.SendersWithReceipts()
	if senders["energo@example.com"] != 1 || senders["shop@example.com"] != 1 {
		t.Errorf("отправители с чеками: %v", senders)
	}
	if senders["friend@example.com"] != 0 {
		t.Errorf("отправитель без чеков попал в список: %v", senders)
	}

	// опись для модели: сколько чеков и на какую сумму
	inv := e.buildInventory()
	if !strings.Contains(inv, "- receipts and invoices: 2, 2035 RUB in total") {
		t.Errorf("опись без чеков или с другими числами:\n%s", inv)
	}
	day := now.Format("2006-01-02")
	if !strings.Contains(inv, "from "+day+" to "+day) {
		t.Errorf("в описи не те даты чеков:\n%s", inv)
	}

	n, sum, first, last := st.ReceiptStats()
	if n != 2 || sum < 2034.74 || sum > 2034.76 || first != day || last != day {
		t.Errorf("итоги чеков: %d, %.2f, %s..%s", n, sum, first, last)
	}
}

// Чеков нет вовсе — сводка, отправители и опись работают молча, без сумм.
func TestWithoutReceiptsScreensWork(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.SetDigestWindow("week"); err != nil {
		t.Fatalf("окно сводки: %v", err)
	}
	m := messageForReceipt(t, st, "<plain-1>", "friend@example.com", "Привет", time.Now().Add(-time.Hour))
	fileForReceipt(t, st, m.ID, "Фото.pdf", store.AttKindDocument, "", 0)

	d, err := e.Digest("", nil)
	if err != nil {
		t.Fatalf("сводка без чеков: %v", err)
	}
	for _, sec := range d.Sections {
		for _, it := range sec.Items {
			if it.Receipt != nil {
				t.Errorf("чек взялся ниоткуда: %+v", it.Receipt)
			}
		}
		if len(sec.Sum) != 0 {
			t.Errorf("сумма без чеков в группе %q: %v", sec.Group.Name, sec.Sum)
		}
	}
	if d.Total != 1 {
		t.Errorf("писем в сводке %d, ожидали 1", d.Total)
	}
	if len(e.SendersWithReceipts()) != 0 {
		t.Errorf("отправители с чеками без чеков: %v", e.SendersWithReceipts())
	}
	if inv := e.buildInventory(); strings.Contains(inv, "receipts and invoices:") {
		t.Errorf("опись говорит о чеках, которых нет:\n%s", inv)
	}
	if n, sum, first, last := st.ReceiptStats(); n != 0 || sum != 0 || first != "" || last != "" {
		t.Errorf("итоги чеков на пустой базе: %d, %.2f, %q..%q", n, sum, first, last)
	}
}
