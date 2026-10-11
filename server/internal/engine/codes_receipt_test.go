package engine

// Чек с фотографии: сумма берётся из QR ФНС, а не из того, что модель
// прочитала на картинке. Это и есть смысл всей затеи — OCR на фото читает
// «55830.00» как «5583О», а модель по такому тексту уверенно называет
// неверное число.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestReceiptByQRWinsModel(t *testing.T) {
	// модель «читает» фотографию и ошибается ровно так, как ошибается
	// настоящая: сумма не та, дата не та, вид назван документом
	e, st, fake := newParseEngine(t,
		`{"kind": "document", "vendor": "Колеса Даром", "amount": 5583, "currency": "RUB", "doc_date": "2026-09-01", "summary": "фото чека на шины"}`)

	_, data := pattern(t, "чек-фнс.png")
	att, _, err := e.AddFile(t.Context(), "чек.png", "image/png", data,
		FileSource{Kind: store.SourceUpload, Ref: "проба"}, nil)
	if err != nil {
		t.Fatalf("приём файла: %v", err)
	}
	codes := st.FileCodes(att.ID)
	if len(codes) == 0 || codes[0].Format != store.CodeFNS {
		t.Fatalf("код чека не сохранён: %+v", codes)
	}

	ref := strconv.FormatInt(att.ID, 10)
	if !st.HasWork(WorkFileParse, ref) {
		t.Fatal("файл принят, а работы «Разбор файлов» в очереди нет")
	}
	if err := e.handleFileParse(t.Context(), &store.WorkItem{Kind: WorkFileParse, Ref: ref}); err != nil {
		t.Fatalf("разбор: %v", err)
	}

	got, err := st.AttachmentByID(att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != store.AttKindReceipt {
		t.Errorf("вид %q: чек по коду ФНС должен ставиться без спроса", got.Kind)
	}
	if got.Amount != 55830.00 {
		t.Errorf("сумма %v, а в QR — 55830.00", got.Amount)
	}
	if got.Currency != "RUB" {
		t.Errorf("валюта %q", got.Currency)
	}
	if got.DocDate != "2026-04-06" {
		t.Errorf("дата документа %q, а в QR — 06.04.2026", got.DocDate)
	}
	if got.Vendor != "Колеса Даром" {
		t.Errorf("продавца берём у модели: %q", got.Vendor)
	}
	// про сумму модель не спрашивали — в промпте прямо сказано её не называть
	if !strings.Contains(fake.last, "QR-код чека ФНС") {
		t.Errorf("модель не предупредили, что сумма уже известна:\n%s", clip(fake.last, 400))
	}
	if !strings.Contains(fake.last, "ФД 4719") {
		t.Errorf("коды не попали в вопрос модели:\n%s", clip(fake.last, 400))
	}
	// номер накопителя и фискальный признак модели не нужны, а рядом со
	// словом «чек» примета читает десятизначный ФП как ИНН и закрывает
	// файл целиком: точное число из кода лишило бы доступа к самому файлу
	if strings.Contains(fake.last, "3641530653") || strings.Contains(fake.last, "7380440902615257") {
		t.Errorf("ФН или ФП уехали модели:\n%s", clip(fake.last, 400))
	}
}

// Модель может не ответить вовсе — а чек всё равно должен быть записан:
// реквизиты у нас уже есть, ждать нечего.
func TestReceiptByQRSavedEvenWithoutModel(t *testing.T) {
	e, st, _ := newParseEngine(t, "не json вовсе")
	_, data := pattern(t, "чек-фнс.png")
	att, _, err := e.AddFile(t.Context(), "чек.png", "image/png", data,
		FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatalf("приём файла: %v", err)
	}
	if err := e.handleFileParse(t.Context(), &store.WorkItem{Kind: WorkFileParse,
		Ref: strconv.FormatInt(att.ID, 10)}); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	got, err := st.AttachmentByID(att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != store.AttKindReceipt || got.Amount != 55830.00 {
		t.Fatalf("чек не записан: вид %q, сумма %v", got.Kind, got.Amount)
	}
}

// Поиск по разобранному полю: «ФД 4719» находит тот самый чек.
func TestSearchByFDNumber(t *testing.T) {
	e, st, _ := newParseEngine(t, `{"kind": "receipt", "vendor": "Колеса Даром", "summary": "шины"}`)
	_, data := pattern(t, "чек-фнс.png")
	att, _, err := e.AddFile(t.Context(), "чек.png", "image/png", data, FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatalf("приём файла: %v", err)
	}
	found, err := st.SearchFiles("ФД 4719", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != att.ID {
		t.Fatalf("по «ФД 4719» нашлось %d файлов", len(found))
	}
	if found, _ := st.SearchFiles("ФД 9999", 10); len(found) != 0 {
		t.Fatalf("по чужому номеру нашлось %d файлов", len(found))
	}
}

// Событие модулям несёт коды: модуль-переходник должен видеть точные
// числа, а не только текст OCR.
func TestEventCarriesCodes(t *testing.T) {
	e, _, _ := newParseEngine(t, `{"kind": "receipt"}`)
	_, data := pattern(t, "чек-фнс.png")
	att, _, err := e.AddFile(t.Context(), "чек.png", "image/png", data, FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatalf("приём файла: %v", err)
	}
	payload, err := e.eventPayload(EventFileAdded, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	codes, ok := payload["codes"].([]map[string]any)
	if !ok || len(codes) == 0 {
		t.Fatalf("в событии нет кодов: %#v", payload["codes"])
	}
	fields, _ := codes[0]["fields"].(map[string]string)
	if fields["Сумма"] != "55830.00" {
		t.Fatalf("поля кода в событии: %+v", fields)
	}
}
