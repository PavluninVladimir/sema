package engine

import (
	"errors"
	"strconv"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Разбор входящих файлов ядром: поля записи заполняются по тексту, непонятный
// ответ модели ничего не портит, а файл из закрытого ящика облачной модели не
// показывается вовсе.

// newParseEngine — движок с поддельной моделью на роли «Модули» и своей
// папкой для файлов.
func newParseEngine(t *testing.T, answer string) (*Engine, *store.Store, *fakeProvider) {
	t.Helper()
	fake := &fakeProvider{answer: answer}
	e, st := newTestEngine(t, "тестовая", fake)
	e.conf.Files.Dir = t.TempDir()
	if err := e.SetRoleField(RoleModules, "model", "тестовая"); err != nil {
		t.Fatalf("роль: %v", err)
	}
	return e, st, fake
}

func TestParseFileFillsFields(t *testing.T) {
	e, st, fake := newParseEngine(t,
		`{"kind": "receipt", "vendor": "Пятёрочка", "amount": "1 234,50", "currency": "rub", "summary": "продукты за неделю"}`)

	att, _, err := e.AddFile(t.Context(), "чек.txt", "text/plain",
		[]byte("Итого 1234.50 руб · Пятёрочка · молоко, хлеб"),
		FileSource{Kind: store.SourceUpload, Ref: "проба"}, nil)
	if err != nil {
		t.Fatalf("приём файла: %v", err)
	}
	ref := strconv.FormatInt(att.ID, 10)

	// работа встала в очередь сама, при file.added
	if !st.HasWork(WorkFileParse, ref) {
		t.Fatal("файл принят, а работы «Разбор файлов» в очереди нет")
	}
	if e.queueHandlers()[WorkFileParse] == nil {
		t.Fatal("вид работы не подключён к очереди")
	}
	if QueueKinds[WorkFileParse] == "" {
		t.Error("у вида работы нет понятного имени для экрана «Очередь»")
	}

	if err := e.handleFileParse(t.Context(), &store.WorkItem{Kind: WorkFileParse, Ref: ref}); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("модель звали %d раз", fake.calls)
	}
	got, err := st.AttachmentByID(att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != store.AttKindReceipt {
		t.Errorf("вид %q, ожидали %q", got.Kind, store.AttKindReceipt)
	}
	if got.Vendor != "Пятёрочка" {
		t.Errorf("получатель %q", got.Vendor)
	}
	if got.Amount != 1234.50 {
		t.Errorf("сумма %v, ожидали 1234.50", got.Amount)
	}
	if got.Currency != "RUB" {
		t.Errorf("валюта %q", got.Currency)
	}
	if got.Summary != "продукты за неделю" {
		t.Errorf("итог %q", got.Summary)
	}
}

// Непонятный ответ модели не должен ни портить запись, ни отправлять работу
// по кругу: файл остаётся тем, чем был, и это видно в журнале.
func TestUnclearReplyNothingBreaks(t *testing.T) {
	e, st, fake := newParseEngine(t, "Не могу сказать, тут какая-то каша из символов")

	att, _, err := e.AddFile(t.Context(), "непонятно.txt", "text/plain",
		[]byte("ЖЖЖ ЖЖЖ ЖЖЖ"), FileSource{Kind: store.SourceUpload, Ref: "проба"}, nil)
	if err != nil {
		t.Fatalf("приём файла: %v", err)
	}
	before, err := st.AttachmentByID(att.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref := strconv.FormatInt(att.ID, 10)
	if err := e.handleFileParse(t.Context(), &store.WorkItem{Kind: WorkFileParse, Ref: ref}); err != nil {
		t.Fatalf("непонятный ответ не должен ронять работу: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("модель звали %d раз", fake.calls)
	}
	got, err := st.AttachmentByID(att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != store.AttKindDocument {
		t.Errorf("вид стал %q, а должен был остаться %q", got.Kind, store.AttKindDocument)
	}
	if got.Vendor != before.Vendor || got.Amount != before.Amount ||
		got.Currency != before.Currency || got.Summary != before.Summary {
		t.Errorf("запись изменилась: было %+v стало %+v", before, got)
	}
}

// Файл из закрытого ящика облачной модели не показывается: доверенной модели
// нет, значит работа ждёт человека, а не отправляет текст наружу.
func TestFileFromClosedAccountNotLeavesCloudModel(t *testing.T) {
	e, st, fake := newParseEngine(t, `{"kind": "receipt", "vendor": "кто-то", "amount": 100}`)

	if err := st.SaveAccounts([]*store.Account{{Name: "работа", Email: "work@пример.рф",
		Host: "imap.пример.рф", Port: 993}}); err != nil {
		t.Fatal(err)
	}
	c := e.MailConf("работа")
	c.Purpose = store.PurposeWork
	if err := e.SaveMailConf("работа", c); err != nil {
		t.Fatal(err)
	}
	if !e.mailboxClosedForCloud("работа") {
		t.Fatal("рабочий ящик должен быть закрыт для облачной модели")
	}
	if !e.ModelIsCloud("тестовая") {
		t.Fatal("незнакомая модель должна считаться облачной")
	}

	m := &store.Message{Account: "работа", FromAddr: "buh@пример.рф", Subject: "акт", Status: "inbox_ok"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	att := &store.Attachment{MessageID: m.ID, Filename: "акт.txt", MIME: "text/plain",
		Kind: store.AttKindDocument, Extracted: "Акт выполненных работ на 100 рублей"}
	if err := st.InsertAttachment(att); err != nil {
		t.Fatal(err)
	}
	if err := st.AddFileSource(att.ID, store.SourceMail, strconv.FormatInt(m.ID, 10), "работа"); err != nil {
		t.Fatal(err)
	}
	if !e.fileClosedForCloud(att) {
		t.Fatal("файл из закрытого ящика не признан закрытым")
	}

	err := e.handleFileParse(t.Context(), &store.WorkItem{Kind: WorkFileParse,
		Ref: strconv.FormatInt(att.ID, 10)})
	var pp *errPostpone
	if !errors.As(err, &pp) {
		t.Fatalf("работа должна отложиться, а не идти дальше: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("закрытый файл ушёл облачной модели: вызовов %d, текст %q", fake.calls, fake.last)
	}
	got, err := st.AttachmentByID(att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != store.AttKindDocument || got.Vendor != "" || got.Amount != 0 {
		t.Errorf("запись тронули без разбора: %+v", got)
	}
}

// Сумма приходит от модели как угодно: числом, строкой с пробелами и
// запятой, с валютой рядом. Выдумывать ничего нельзя — не разобралось, ноль.
func TestAmountFromReplyModel(t *testing.T) {
	cases := []struct {
		дано any
		want float64
	}{
		{float64(1234.5), 1234.5},
		{"1 234,50", 1234.5},
		{"1 234,50 ₽", 1234.5},
		{"1.234,50", 1234.5},
		{"1234", 1234},
		{"", 0},
		{"не знаю", 0},
		{nil, 0},
		{float64(-5), 0},
	}
	for _, c := range cases {
		if got := numberFromReply(c.дано); got != c.want {
			t.Errorf("из %v получили %v, ждали %v", c.дано, got, c.want)
		}
	}
}

// Не чек — значит без суммы: у рекламной рассылки «итого» тоже найдётся, и в
// «Файлах» она встала бы рядом с настоящими тратами.
func TestDocumentWithoutAmounts(t *testing.T) {
	kind, vendor, amount, currency, summary := fileFactsClean(fileFacts{
		Kind: "document", Vendor: " Реклама ", Amount: "999", Currency: "rub", Summary: " скидки "})
	if kind != store.AttKindDocument || amount != 0 || currency != "" {
		t.Errorf("документ с суммой: %q %v %q", kind, amount, currency)
	}
	if vendor != "Реклама" || summary != "скидки" {
		t.Errorf("поля не подчищены: %q %q", vendor, summary)
	}
	// незнакомый вид от модели не должен просачиваться в базу
	if k, _, _, _, _ := fileFactsClean(fileFacts{Kind: "накладная"}); k != store.AttKindDocument {
		t.Errorf("незнакомый вид %q пролез в базу", k)
	}
}
