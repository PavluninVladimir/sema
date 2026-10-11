package engine

// Чтение содержимого вложений. PDF читает pdftotext, картинки — OCR,
// офисные документы Сёма разбирает сама: docx, xlsx и pptx — это zip
// с XML внутри, внешние программы для них не нужны.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"html"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// officeText достаёт текст из docx/xlsx/pptx.
func officeText(data []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ""
	}
	var parts []string
	// Лимит общий для документа: тысячи сжатых XML по 8 МБ каждый
	// раньше обходили ограничение размера вложения и заполняли память.
	remaining := int64(8 << 20)
	for _, f := range zr.File {
		if remaining <= 0 {
			break
		}
		name := f.Name
		switch {
		case name == "word/document.xml", // текст письма-документа
			name == "xl/sharedStrings.xml",                 // строки таблицы
			strings.HasPrefix(name, "xl/worksheets/sheet"), // числа в ячейках
			strings.HasPrefix(name, "ppt/slides/slide"):    // текст слайдов
		default:
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(rc, remaining))
		rc.Close()
		remaining -= int64(len(raw))
		if err != nil {
			continue
		}
		if t := xmlText(raw); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// xmlText вытаскивает из XML только сам текст, без разметки.
func xmlText(raw []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.CharData:
			if s := strings.TrimSpace(string(t)); s != "" {
				b.WriteString(s)
				b.WriteByte(' ')
			}
		case xml.EndElement:
			// абзац документа и строка таблицы — это конец смысловой строки
			switch t.Name.Local {
			case "p", "row", "tr":
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

var haveTesseract = func() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}()

// ocrText распознаёт текст на картинке. Без tesseract возвращает пусто —
// в настройках, во вкладке «Система», видно, что инструмента не хватает.
func ocrText(ctx context.Context, path string) string {
	if !haveTesseract {
		return ""
	}
	out, err := extractCommand(ctx, "tesseract", path, "stdout", "-l", "rus+eng")
	if err != nil {
		// языковых пакетов может не быть — пробуем как есть
		if out, err = extractCommand(ctx, "tesseract", path, "stdout"); err != nil {
			return ""
		}
	}
	return string(out)
}

// officeExts — расширения, которые Сёма читает сам.
var officeExts = map[string]bool{
	".docx": true, ".xlsx": true, ".pptx": true,
	".docm": true, ".xlsm": true,
}

func isOffice(path, mime string) bool {
	if officeExts[strings.ToLower(filepath.Ext(path))] {
		return true
	}
	return strings.Contains(mime, "officedocument") ||
		strings.Contains(mime, "wordprocessingml") ||
		strings.Contains(mime, "spreadsheetml")
}

// ext — расширение файла в нижнем регистре.
func ext(path string) string { return strings.ToLower(filepath.Ext(path)) }

func isImage(path, mime string) bool {
	if strings.HasPrefix(mime, "image/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".heic":
		return true
	}
	return false
}

// htmlText вытаскивает текст из HTML-вложения: брокерские отчёты, выписки
// и уведомления часто приходят именно так, причём помеченные как
// octet-stream — тип определяем по расширению, а не по словам клиента.
func htmlText(data []byte) string {
	t := reScripts.ReplaceAllString(string(data), " ")
	t = reBreaks.ReplaceAllString(t, "\n")
	t = reAnyTag.ReplaceAllString(t, " ")
	return html.UnescapeString(t)
}

var (
	reScripts = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reBreaks  = regexp.MustCompile(`(?i)</(p|div|tr|li|h[1-6]|table)>|<br\s*/?>`)
	reAnyTag  = regexp.MustCompile(`<[^>]+>`)
)

func isHTML(path, mime string) bool {
	if strings.Contains(mime, "text/html") {
		return true
	}
	switch ext(path) {
	case ".html", ".htm", ".xhtml":
		return true
	}
	return false
}

// isPlainText — форматы, которые читаются как есть, даже если клиент назвал
// их octet-stream: календарные приглашения, XML, CSV, текст.
func isPlainText(path, mime string) bool {
	if strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/csv" ||
		mime == "application/ics" || mime == "text/calendar" {
		return true
	}
	switch ext(path) {
	case ".txt", ".csv", ".json", ".xml", ".ics", ".md", ".log", ".mxfile", ".excalidraw":
		return true
	}
	return false
}
