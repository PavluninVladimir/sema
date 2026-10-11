package engine

// Аудиофайл как файл Сёмы: запись читается как картинка — у неё есть текст,
// и текст ищется, попадает в контекст чата и в память. Но сама по себе
// расшифровка стоит денег и открывает чужие голоса, поэтому «как картинка»
// здесь только для загруженного человеком (`speech.files`). Записи из почты
// и папок не расшифровываются сами никогда: массово — значит платить за
// каждое чужое голосовое, о котором никто не просил. По просьбе в чате
// такую запись расшифровывает `sema.files.transcribe(id)`.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// isAudio — запись: по типу, а если клиент прислал octet-stream — по
// расширению.
func isAudio(path, mime string) bool {
	m := strings.ToLower(mime)
	if strings.HasPrefix(m, "audio/") || m == "application/ogg" {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ogg", ".oga", ".opus", ".mp3", ".m4a", ".wav", ".flac", ".aac", ".amr":
		return true
	}
	return false
}

// audioMIME чинит тип записи: почтовые клиенты и браузеры нередко присылают
// octet-stream, а провайдер распознавания выбирает формат по типу.
func audioMIME(name, mime string) string {
	m := strings.ToLower(strings.TrimSpace(mime))
	if strings.HasPrefix(m, "audio/") {
		return m
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	case ".aac":
		return "audio/aac"
	case ".webm":
		return "audio/webm"
	}
	return "audio/ogg"
}

// audioFileText — текст записи при приёме файла. Возвращает и пояснение,
// если текста нет по причине: оно ляжет в карточку файла, чтобы «не
// расшифровано» отличалось от «даже не пробовали».
func (e *Engine) audioFileText(ctx context.Context, name, mime string, data []byte, src FileSource) (text, via, note string) {
	if !e.speechFromSource(src.Kind) {
		return "", "", "" // объяснит extractHint: сами такие записи не читаем
	}
	text, model, err := e.Hear(ctx, VoiceIn{Audio: data, MIME: audioMIME(name, mime), Own: true, Consumer: "core:files"})
	if err != nil {
		return "", "", "аудио не распознано: " + err.Error()
	}
	return text, "распознавание речи · " + model, ""
}

// hasRealText — у файла настоящий текст, а не пометка «посмотрели, ничего
// не нашлось» (её ставит перечитывание, чтобы не возвращаться к файлу
// вечно).
func hasRealText(a *store.Attachment) bool {
	return strings.TrimSpace(a.Extracted) != "" && !strings.HasPrefix(a.Extracted, "текста в файле не нашлось")
}

// TranscribeFile расшифровывает уже лежащую в файлах запись по просьбе
// человека. Просьба — его решение: голос считается своим (`speech.outside`
// = own его пропускает), а вот источник записи значения не имеет. Уже
// расшифрованное второй раз не оплачивается.
func (e *Engine) TranscribeFile(ctx context.Context, id int64, consumer string) (string, error) {
	att, err := e.store.AttachmentByID(id)
	if err != nil {
		return "", fmt.Errorf("файла #%d нет", id)
	}
	if !isAudio(att.Filename, att.MIME) {
		return "", fmt.Errorf("файл «%s» — не аудиозапись", att.Filename)
	}
	if hasRealText(att) {
		return att.Extracted, nil
	}
	data, err := e.FileBytes(att)
	if err != nil {
		return "", err
	}
	text, model, err := e.Hear(ctx, VoiceIn{Audio: data, MIME: audioMIME(att.Filename, att.MIME), Own: true, Consumer: consumer})
	if err != nil {
		return "", err
	}
	att.Extracted = text
	att.Summary = "прочитано: распознавание речи · " + model + " · по просьбе"
	if err := e.store.UpdateAttachmentText(att); err != nil {
		return "", err
	}
	e.fileGotText(ctx, att)
	return text, nil
}

// fileGotText — у файла появился текст позже приёма: дальше всё как у любого
// такого файла. Модули узнают, что файл изменился; класс пересчитывается по
// тексту (он умеет только расти, пометку человека не трогает); ставится
// разбор; вектор строится заново — старый описывал файл без слов.
func (e *Engine) fileGotText(ctx context.Context, att *store.Attachment) {
	e.emitModuleEvent(EventFileChanged, att.ID)
	class, why := sentClassWhy(att.Filename + "\n" + clip(att.Extracted, 2000))
	e.raiseClass(store.ClassFile, att.ID, class, why)
	e.EnqueueFileParse(att.ID)
	if e.embedder != nil {
		if vec, err := e.embedIfAllowed(ctx, "File: "+att.Filename+"\n"+att.Summary+"\n"+clip(att.Extracted, 800)); err == nil && vec != nil {
			_ = e.store.SaveAttEmbedding(att.ID, embed.EncodeVec(vec))
		}
	}
}

// toolTranscribe — `sema.files.transcribe(id)`. Только из чата: это прямая
// просьба владельца. Модуль с правом «files» мог бы иначе расшифровывать
// всю почту подряд — ровно то, чего мы не делаем.
func (e *Engine) toolTranscribe(ctx context.Context, consumer string, id int64) (any, error) {
	if !strings.HasPrefix(consumer, "chat:") {
		return nil, fmt.Errorf("files.transcribe: расшифровка записи — только по просьбе владельца в чате")
	}
	if id <= 0 {
		return nil, fmt.Errorf("files.transcribe: нужен id файла")
	}
	return e.TranscribeFile(ctx, id, consumer)
}
