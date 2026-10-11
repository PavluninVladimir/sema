package engine

// Инструменты, которыми Сёма разбирает почту и файлы. Часть встроена в
// сервер, часть — внешние программы: их может не быть, и тогда картинки
// или PDF останутся непрочитанными. Всё это видно в настройках, чтобы не
// гадать, почему у файла пустая карточка.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

type Tool struct {
	Title string
	Meta  string
	State string // «работает» | «не установлен» | «встроено»
	OK    bool   // зелёная метка или оранжевая
	Hint  string // чего не хватает и как поставить
}

// Tools — состояние всех инструментов на этой машине.
func (e *Engine) Tools() []Tool {
	waiting := e.store.CountAttachmentsWithoutText()

	var out []Tool

	pdf := Tool{Title: "pdftotext (poppler)", Meta: "читает PDF: чеки, счета, акты"}
	if havePdftotext {
		pdf.State, pdf.OK = "установлен", true
	} else {
		pdf.State = "не установлен"
		pdf.Hint = "brew install poppler · на Raspberry Pi: apt install poppler-utils"
	}
	out = append(out, pdf)

	ocr := Tool{Title: "Своё распознавание картинок", Meta: "запасной путь, когда модель картинки не умеет"}
	if have, how, known := e.HaveOurOCRNow(); !known {
		// проверка идёт в фоне (на маке это сборка помощника): тревожить
		// «не установлено» рано, экран перечитается
		ocr.State, ocr.OK = "проверяю…", true
	} else if have {
		ocr.Meta += " · " + how
		ocr.State, ocr.OK = "работает", true
	} else {
		ocr.State = "не установлено"
		ocr.Hint = "apt install tesseract-ocr tesseract-ocr-rus (Raspberry Pi, Docker) · brew install tesseract tesseract-lang"
	}
	out = append(out, ocr)

	codes := Tool{Title: "Коды на картинках",
		Meta:  "QR, DataMatrix, Aztec, EAN-13/8, UPC, Code 128 — читаются на месте, без модели и сети (gozxing)",
		State: "встроено", OK: true}
	if n, files := e.store.CountFileCodes(); n > 0 {
		codes.Meta += fmt.Sprintf(" · найдено %d на %d файлах", n, files)
	}
	out = append(out, codes)

	out = append(out,
		Tool{Title: "Офисные документы", Meta: "docx, xlsx, pptx — Сёма читает сам, ставить нечего",
			State: "встроено", OK: true},
		Tool{Title: "JS-движок (goja)", Meta: "скрипты агента · без Node.js, работает и на Raspberry Pi",
			State: "встроено", OK: true},
		Tool{Title: "IMAP (go-imap)", Meta: "чтение почты, переносы, флаги, папки",
			State: "встроено", OK: true},
	)

	if waiting > 0 {
		out = append(out, Tool{
			Title: "Ждут распознавания",
			Meta:  fmt.Sprintf("%d файлов без извлечённого текста — перечитаются, когда инструмент появится", waiting),
			State: fmt.Sprintf("%d", waiting),
		})
	}
	return out
}

// RescanAttachments перечитывает файлы, из которых текст не достали раньше:
// поставили OCR — и старые картинки наконец разобраны.
func (e *Engine) RescanAttachments(ctx context.Context, limit int) (int, error) {
	list, err := e.store.AttachmentsWithoutText(limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, a := range list {
		select {
		case <-ctx.Done():
			return done, nil
		default:
		}
		data, _ := os.ReadFile(a.Path) // офисным форматам нужен сам файл
		start := time.Now()
		text, via, codes := e.extract(ctx, a.Path, a.MIME, data)
		if searched := isImage(a.Path, a.MIME) || isPDF(a.Path, a.MIME); searched || len(codes) > 0 {
			// коды появились на старом файле: распознавание завели позже,
			// чем приняли фотографию чека
			record := codes
			if searched && len(codes) == 0 {
				record = []store.FileCode{markWithoutCodes(time.Since(start).Seconds())}
			}
			if err := e.store.SaveFileCodes(a.ID, record); err != nil {
				return done, err
			}
		}
		if text == "" {
			if len(codes) == 0 {
				// Посмотрели и не нашли — так и запишем.
				//
				// Файл без текста возвращался в перечитывание каждый
				// проход, вечно: двадцать шесть картинок («кольцо
				// премиум», «ChatGPT Image») текста не несут вовсе,
				// tesseract читает их в ноль, а на Raspberry Pi это по
				// десять прогонов в минуту без всякой надежды.
				//
				// Пометка — не «сдаёмся навсегда»: кнопка «Перечитать
				// файлы» на «Инструментах» снимает её со всех и
				// заставляет пройти заново. Так и задумано — инструмент
				// мог появиться после того, как файл приняли.
				a.Extracted = lookedWith(via, e.whatWeCanRead(a.Path, a.MIME))
				if err := e.store.UpdateAttachmentText(a); err != nil {
					return done, err
				}
				continue
			}
			{
				// Прочитанный код — это и есть текст файла.
				//
				// Без этой строки файл оставался «без извлечённого
				// текста» навсегда: следующий проход снова брал его из
				// AttachmentsWithoutText, снова ставил разбор, снова
				// спрашивал модель. 11 сентября 2026 три картинки с
				// QR-кодами чеков крутились так двое суток — 2300
				// обращений к модели по одним и тем же трём файлам,
				// 9,2 млн токенов ответа и почти вся плата за DeepSeek.
				// Ни ошибки, ни строки в журнале: каждый проход честно
				// заканчивался успехом.
				//
				// Содержимое кода в текст кладут и по существу: чек ФНС
				// с суммой и датой должен находиться поиском, как любой
				// другой файл.
				a.Extracted = codeText(codes)
				if err := e.store.UpdateAttachmentText(a); err != nil {
					return done, err
				}
				e.emitModuleEvent(EventFileChanged, a.ID)
				e.EnqueueFileParse(a.ID)
				done++
			}
			continue
		}
		a.Extracted = text
		if strings.HasPrefix(a.Summary, "текст не извлечён") {
			a.Summary = "" // подсказка «почему пусто» больше не нужна
		}
		if via != "" {
			a.Summary = strings.TrimSpace(a.Summary + " · прочитано: " + via)
		}
		if err := e.store.UpdateAttachmentText(a); err != nil {
			return done, err
		}
		e.emitModuleEvent(EventFileChanged, a.ID)
		// класс считался при приёме по одному имени файла — «IMG_2841.jpg»
		// не говорит ничего. Текст появился только сейчас, и без пересчёта
		// фотография паспорта так и осталась бы «обычной» навсегда. Класс
		// умеет только расти, пометку человека `raiseClass` не трогает
		class, why := sentClassWhy(a.Filename + "\n" + clip(a.Extracted, 2000))
		e.raiseClass(store.ClassFile, a.ID, class, why)
		// при приёме разбирать было нечего, и без этой строки файл остался
		// бы без полей навсегда
		e.EnqueueFileParse(a.ID)
		// текст изменился — старый вектор больше не описывает файл
		if e.embedder != nil {
			text := "File: " + a.Filename + "\nFrom: " + a.FromAddr + "\n" + a.Summary + "\n" + clip(a.Extracted, 800)
			if vec, err := e.embedIfAllowed(ctx, text); err == nil && vec != nil {
				_ = e.store.SaveAttEmbedding(a.ID, embed.EncodeVec(vec))
			}
		}
		done++
	}
	return done, nil
}

// ToolsAvailable перепроверяет внешние программы: человек мог поставить
// их прямо сейчас, не перезапуская сервер.
func ToolsAvailable() {
	_, err := exec.LookPath("pdftotext")
	havePdftotext = err == nil
	_, err = exec.LookPath("tesseract")
	haveTesseract = err == nil
}

// codeText — что написано в прочитанных кодах, строками. Это
// единственное содержимое такого файла: картинка с одним QR-кодом
// текста не несёт, а его payload несёт и сумму, и дату, и продавца.
func codeText(codes []store.FileCode) string {
	var line []string
	for _, k := range codes {
		if strings.TrimSpace(k.Value) == "" {
			continue
		}
		kind := k.Symbology
		if k.Format != "" {
			kind += " · " + k.Format
		}
		line = append(line, strings.TrimSpace(kind+": "+k.Value))
	}
	if len(line) == 0 {
		// кодов нет, но искали — помечаем, что смотрели: иначе файл
		// вернётся сюда следующим проходом и так без конца
		return "кодов на картинке не найдено"
	}
	return strings.Join(line, "\n")
}

// lookedWith — отметка «смотрели и не нашли», она же объяснение
// человеку в карточке файла.
//
// Имя инструмента берём НЕ из `via`: его возвращает только тот, кто
// текст нашёл, а здесь никто не нашёл — и отметка выходила «смотрели:
// нечем было» на машине, где tesseract стоит и честно отработал.
// «Нечем было» человек читает как «поставь инструмент», и идёт ставить
// уже поставленное.
func lookedWith(via, can string) string {
	who := strings.TrimSpace(via)
	if who == "" {
		who = can
	}
	if who == "" {
		return "текста в файле не нашлось · читать его сейчас нечем"
	}
	return "текста в файле не нашлось · смотрели: " + who
}

// whatWeCanRead — чем Сёма может прочесть такой файл прямо сейчас.
// Перечисляем то, что есть на машине и включено настройкой, а не то,
// что вернуло текст.
func (e *Engine) whatWeCanRead(path, mime string) string {
	if isAudio(path, mime) {
		return "речь расшифровывается при загрузке и по просьбе в чате"
	}
	if !isImage(path, mime) && !isPDF(path, mime) {
		return "разбор по типу файла"
	}
	var who []string
	if e.useModelForImages() && e.VisionState() == "" {
		who = append(who, "модель")
	}
	switch e.OCREngine() {
	case OCRModelThenTess, OCRTesseract:
		if haveTesseract {
			who = append(who, "tesseract")
		}
	case OCRModelThenVision, OCRVision:
		if systemOCR(e.conf.Files.Dir) != "" {
			who = append(who, "Vision")
		}
	}
	return strings.Join(who, " и ")
}
