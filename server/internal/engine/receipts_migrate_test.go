package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Переезд чеков: факты вложения ложатся на сам файл, а вся выписка
// целиком — одной таблицей в «Файлы», чтобы её нашли поиском. Терять
// девяносто семь чеков при снятии модуля нельзя.
func TestMigrationReceiptsInFiles(t *testing.T) {
	e, st := newTestEngine(t, "", nil)

	// письмо с вложением: у такого чека факты уедут на файл
	m := &store.Message{Account: "я@пример.рф", MessageID: "счёт@пример", FromAddr: "no-reply@energy.example",
		Subject: "Счёт за август", Status: store.StatusInboxOK}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.AddFile(t.Context(), "счёт.pdf", "application/pdf", []byte("счёт на 2165 рублей"),
		FileSource{Kind: "mail", Ref: "1"}, m); err != nil {
		t.Fatal(err)
	}

	receipts := []map[string]any{
		{"date": "2026-09-04T00:50:43+04:00", "vendor": "АО Горэнерго", "amount": 2165.76,
			"currency": "RUB", "category": "Коммунальные услуги", "source": "attachment",
			"msg": float64(m.ID), "summary": "счёт.pdf · оплата за электроэнергию", "account": "я@пример.рф"},
		{"date": "2026-05-27T07:41:14Z", "vendor": "Умный ритейл", "amount": 2601.0,
			"currency": "RUB", "category": "Продукты", "source": "body", "msg": float64(m.ID),
			"summary": "оплата за продукты", "subject": "Чек; с точкой с запятой"},
	}
	for i, h := range receipts {
		if err := st.PutModuleDoc("receipts", "receipts", "ч"+string(rune('1'+i)), h); err != nil {
			t.Fatal(err)
		}
	}

	// движок при старте уже отметил, что переезжать нечего: коллекции
	// тогда были пусты. Снимаем метку — это ровно тот случай, ради
	// которого переезд и писан
	if err := st.SetSetting(receiptsMigrationKey, ""); err != nil {
		t.Fatal(err)
	}
	e.MigrateReceiptsModule(t.Context())

	// 1. факты чека-вложения — на самом файле
	attachments, err := st.AttachmentsOfMessage(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got *store.Attachment
	for _, f := range attachments {
		if f.Filename == "счёт.pdf" {
			got = f
		}
	}
	if got == nil {
		t.Fatalf("вложения письма пропали: %d штук", len(attachments))
	}
	if got.Kind != store.AttKindReceipt {
		t.Errorf("файл не помечен чеком: %q", got.Kind)
	}
	if got.Amount != 2165.76 || got.Vendor != "АО Горэнерго" {
		t.Errorf("факты не переехали: сумма %v, получатель %q", got.Amount, got.Vendor)
	}

	// 2. чек из тела письма стал файлом — с суммой, получателем и связью
	// с тем же письмом: иначе суммы в сводке «Почты» обеднели бы втрое
	attachments, err = st.AttachmentsOfMessage(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var note *store.Attachment
	for _, f := range attachments {
		if f.Filename != "счёт.pdf" {
			note = f
		}
	}
	if note == nil {
		t.Fatalf("чек из тела письма не стал файлом: %d вложений", len(attachments))
	}
	if note.Kind != store.AttKindReceipt || note.Amount != 2601 || note.Vendor != "Умный ритейл" {
		t.Errorf("записка без фактов чека: %+v", note)
	}
	if !strings.Contains(note.Filename, "Умный ритейл") || !strings.Contains(note.Filename, "₽") {
		t.Errorf("имя записки не читается без открытия: %q", note.Filename)
	}
	for _, what := range []string{"Умный ритейл", "2601.00", "Продукты", "оплата за продукты"} {
		if !strings.Contains(note.Extracted, what) {
			t.Errorf("в записке нет %q: %q", what, note.Extracted)
		}
	}

	// 3. коллекции модуля убраны, метка стоит, второй раз не переезжаем
	docs, _ := st.FindModuleDocs("receipts", "receipts", store.DocQuery{Limit: 10})
	if len(docs) != 0 {
		t.Errorf("коллекция модуля осталась: %d записей", len(docs))
	}
	was := len(attachments)
	e.MigrateReceiptsModule(t.Context())
	again, _ := st.AttachmentsOfMessage(m.ID)
	if len(again) != was {
		t.Errorf("переезд повторился: файлов было %d, стало %d", was, len(again))
	}
}

// Модуля не было — переезжать нечего, и второй раз в базу не ходим.
func TestMigrationReceiptsWithoutModule(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.MigrateReceiptsModule(t.Context())
	if st.Setting(receiptsMigrationKey, "") != "1" {
		t.Error("метка должна стоять и когда переезжать нечего")
	}
	for _, m := range e.Modules() {
		if m.Name == "receipts" {
			t.Error("модуля «Чеки» быть не должно")
		}
	}
}

// Модуль снимается ДО того, как заводятся файлы: пока он жив, он слышит
// «file.added» и на каждую записку пишет себе новый чек — переезд кормит
// сам себя. А остатки прошлого переезда подметаются при следующем старте.
func TestBalancesRemovedModuleSweptUp(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	// метка стоит (движок уже отработал), модуля нет, а записи остались —
	// ровно то, что осталось на живой Сёме после первого переезда
	if err := st.PutModuleDoc("receipts", "receipts", "хвост", map[string]any{"amount": 1.0}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutModuleDoc("receipts", "categories", "кат", map[string]any{"title": "Продукты"}); err != nil {
		t.Fatal(err)
	}
	e.MigrateReceiptsModule(t.Context())
	for _, coll := range []string{"receipts", "categories"} {
		docs, _ := st.FindModuleDocs("receipts", coll, store.DocQuery{Limit: 10})
		if len(docs) != 0 {
			t.Errorf("коллекция %q не подметена: %d записей", coll, len(docs))
		}
	}
}
