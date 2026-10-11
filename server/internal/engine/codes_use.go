package engine

// Что Сёма делает с найденными кодами: показывает их модели отдельной
// строкой, отдаёт модулям в событии и называет их человеку.

import (
	"fmt"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// FormatTitle — как код зовётся человеку: капсула в списке файлов.
func FormatTitle(format string) string {
	switch format {
	case store.CodeFNS:
		return "QR ФНС"
	case store.CodeMark:
		return "Честный знак"
	case store.CodeGTIN:
		return "EAN"
	case store.CodeURL:
		return "ссылка"
	case store.CodeSecretURL:
		return "ссылка с секретом"
	}
	return "код"
}

// SymbologyTitle — вид кода словами.
func SymbologyTitle(kind string) string {
	switch kind {
	case "qr":
		return "QR"
	case "datamatrix":
		return "DataMatrix"
	case "aztec":
		return "Aztec"
	case "ean13":
		return "EAN-13"
	case "ean8":
		return "EAN-8"
	case "upca":
		return "UPC-A"
	case "upce":
		return "UPC-E"
	case "code128":
		return "Code 128"
	}
	return kind
}

// notForModel — поля, которые в промпт не идут. Номер накопителя и
// фискальный признак модели не нужны ни для чего: у неё спрашивают
// продавца и о чём документ. А десятизначный ФП рядом со словом «чек»
// примета честно опознаёт как ИНН, и файл целиком становится секретным —
// то есть точное число из кода закрыло бы доступ к самому файлу.
// В карточке файла и в поиске оба номера остаются.
var notForModel = map[string]bool{"ФН": true, "ФП": true, "Ключ проверки": true, "Код проверки": true}

// CodesForModel — строка «Коды на картинке» для контекста модели. Идёт
// рядом с текстом OCR, а не вместо него: модель видит и прочитанное
// буквами, и точные числа из кода — и сама решает, чему верить.
//
// Секретного здесь не бывает: адрес с токеном пришёл сюда уже без него.
func CodesForModel(codes []store.FileCode) string {
	codes = store.Codes(codes)
	if len(codes) == 0 {
		return ""
	}
	var parts []string
	for _, k := range codes {
		if description := codeDescription(k); description != "" {
			parts = append(parts, description)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Коды на картинке: " + strings.Join(parts, "; ")
}

func codeDescription(k store.FileCode) string {
	var b strings.Builder
	switch k.Format {
	case store.CodeFNS:
		b.WriteString("чек ФНС")
	case store.CodeMark:
		b.WriteString("марка «Честного знака»")
	default:
		b.WriteString(SymbologyTitle(k.Symbology))
	}
	var pairs []string
	for _, f := range k.Fields {
		if notForModel[f.Label] {
			continue
		}
		pairs = append(pairs, f.Label+" "+f.Value)
	}
	if len(pairs) > 0 {
		b.WriteString(" " + strings.Join(pairs, ", "))
		return b.String()
	}
	if len(k.Fields) > 0 {
		return b.String() // всё, что было, модели не показываем
	}
	b.WriteString(" " + clip(k.Value, 200))
	return b.String()
}

// ReceiptFromCodes — реквизиты чека ФНС, если он среди кодов файла. Именно по
// нему разбор файла ставит сумму и дату: OCR на фотографии читает «55830»
// как «5583О», а в коде то же число лежит цифрами.
func ReceiptFromCodes(codes []store.FileCode) *FNSReceipt {
	for _, k := range codes {
		if k.Format != store.CodeFNS {
			continue
		}
		if receipt, ok := ReceiptDetails(k.Value); ok {
			return receipt
		}
	}
	return nil
}

// codesForEvent — коды в событии `file.added` для модулей: поля картой,
// потому что модулю удобнее `codes[0].fields.ФД`, чем перебор списка.
func codesForEvent(codes []store.FileCode) []map[string]any {
	codes = store.Codes(codes)
	out := make([]map[string]any, 0, len(codes))
	for _, k := range codes {
		fields := map[string]string{}
		for _, f := range k.Fields {
			fields[f.Label] = f.Value
		}
		out = append(out, map[string]any{
			"kind": k.Symbology, "value": k.Value, "format": k.Format,
			"fields": fields, "page": k.Page,
		})
	}
	return out
}

// FileCodes — настоящие коды файла, без отметки «искали и не нашли».
func (e *Engine) FileCodes(id int64) []store.FileCode {
	return store.Codes(e.store.FileCodes(id))
}

// markWithoutCodes — запись о том, что коды искали и не нашли.
func markWithoutCodes(elapsed float64) store.FileCode {
	return store.FileCode{Format: store.CodeNone,
		Fields: []store.CodeField{{Label: "Искали", Value: fmt.Sprintf("%.1f с", elapsed)}}}
}

// codeLog — что записать в журнал после приёма файла. Честный итог
// важнее молчания: «кодов не найдено» — это тоже результат, и человеку он
// объясняет, почему в карточке пусто.
func codeLog(name string, codes []store.FileCode, elapsed float64) string {
	if len(codes) == 0 {
		return fmt.Sprintf("коды не найдены за %.1f с: %s", elapsed, name)
	}
	var kinds []string
	for _, k := range codes {
		kinds = append(kinds, FormatTitle(k.Format))
	}
	return fmt.Sprintf("коды на %s за %.1f с: %s", name, elapsed, strings.Join(kinds, ", "))
}
