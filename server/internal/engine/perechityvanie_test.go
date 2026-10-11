package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Файл, из которого прочитали только код, обязан перестать быть
// «файлом без текста».
//
// 11–12 сентября 2026 три картинки с QR-кодами чеков крутились в петле
// двое суток: перечитывание брало десять свежих файлов без текста,
// находило на них код, ставило разбор моделью — и не записывало ничего.
// Следующий проход брал те же файлы снова. 2300 обращений к модели по
// трём файлам, 9,2 млн токенов ответа и почти вся плата за DeepSeek.
// Ни ошибки, ни строки в журнале: каждый проход честно кончался успехом.
func TestFileWithCodeNotReturnsInReread(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	a, _, err := e.AddFile(context.Background(), "qr.png", "image/png",
		[]byte("не картинка, но нам важна запись"), FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// текста нет, а код прочитан — ровно то состояние, в котором файлы и заперлись
	a.Extracted = ""
	if err := st.UpdateAttachmentText(a); err != nil {
		t.Fatal(err)
	}
	codes := []store.FileCode{{Symbology: "qr", Format: "фнс",
		Value: "t=20260905T1227&s=150.00&fn=7381440900904092&i=45653&fp=2041776889&n=1"}}
	if err := st.SaveFileCodes(a.ID, codes); err != nil {
		t.Fatal(err)
	}

	// то, что делает перечитывание, когда текста нет, а коды есть
	a.Extracted = codeText(codes)
	if err := st.UpdateAttachmentText(a); err != nil {
		t.Fatal(err)
	}

	without, err := st.AttachmentsWithoutText(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range without {
		if f.ID == a.ID {
			t.Fatal("файл с прочитанным кодом снова считается файлом без текста — петля вернулась")
		}
	}
	// и содержимое кода стало текстом файла: чек должен находиться поиском
	if !strings.Contains(a.Extracted, "s=150.00") {
		t.Errorf("содержимое кода не стало текстом файла: %q", a.Extracted)
	}
}

// Пустой список кодов тоже должен давать непустой текст: иначе файл
// вернётся в перечитывание следующим же проходом.
func TestTextCodesNeverNotEmpty(t *testing.T) {
	if codeText(nil) == "" {
		t.Error("на пустых кодах текст пустой — файл вернётся в петлю")
	}
	if codeText([]store.FileCode{{Value: "  "}}) == "" {
		t.Error("на пустом значении текст пустой — файл вернётся в петлю")
	}
}

// Файл, в котором текста нет вовсе, не должен крутиться в
// перечитывании вечно. 12 сентября 2026 двадцать шесть картинок
// («кольцо премиум», «ChatGPT Image») читались tesseract'ом в ноль по
// десять раз в минуту — на Raspberry Pi это заметная работа без
// всякой надежды. Файл без текста и файл, до которого не дошли руки,
// в базе выглядели одинаково.
func TestFileWithoutTextMarkedSeen(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	a, _, err := e.AddFile(context.Background(), "картинка.png", "image/png",
		[]byte("тут нет текста"), FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Extracted = ""
	if err := st.UpdateAttachmentText(a); err != nil {
		t.Fatal(err)
	}
	// то, что делает перечитывание, когда ни текста, ни кодов
	a.Extracted = lookedWith("", e.whatWeCanRead(a.Path, a.MIME))
	if err := st.UpdateAttachmentText(a); err != nil {
		t.Fatal(err)
	}
	without, err := st.AttachmentsWithoutText(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range without {
		if f.ID == a.ID {
			t.Fatal("просмотренный файл снова ждёт перечитывания — петля вернулась")
		}
	}
	// и сказано, кто смотрел. Имя берётся не из того, кто вернул текст
	// (его здесь никто не вернул), а из того, что есть на машине:
	// «нечем было» на машине с tesseract — неправда, за которой человек
	// идёт ставить уже поставленное
	if strings.Contains(a.Extracted, "нечем") && haveTesseract {
		t.Errorf("сказано «нечем», хотя tesseract на месте: %q", a.Extracted)
	}

	// кнопка «Перечитать файлы» снимает пометку: инструмент мог появиться
	n, err := st.ClearMarksWithoutText()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("снято отметок %d, а ставили одну", n)
	}
	again, err := st.AttachmentsWithoutText(50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range again {
		if f.ID == a.ID {
			found = true
		}
	}
	if !found {
		t.Error("после «Перечитать файлы» файл не вернулся — новый инструмент до него не дойдёт")
	}
}

// Настоящий извлечённый текст кнопка не стирает: иначе одно нажатие
// заставило бы Сёму перечитать и переразобрать весь архив.
func TestRereadNotErasesRealText(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	a, _, err := e.AddFile(context.Background(), "счёт.txt", "text/plain",
		[]byte("Итого к оплате 1500 рублей"), FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Extracted == "" {
		t.Skip("текст не извлёкся — проверять нечего")
	}
	if _, err := st.ClearMarksWithoutText(); err != nil {
		t.Fatal(err)
	}
	without, err := st.AttachmentsWithoutText(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range without {
		if f.ID == a.ID {
			t.Error("настоящий текст стёрт кнопкой перечитывания")
		}
	}
}
