package llm

// Речь: слушать. Провайдер, умеющий распознавать, объявляет Transcribe. Как и
// с картинками, это отдельное умение, а не Provider: у большинства моделей
// его нет, и вопрос «умеет ли» решается типом, а не настройкой.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Transcriber — провайдер, который переводит речь в текст.
type Transcriber interface {
	// Transcribe распознаёт аудио. mime — тип записи (audio/ogg у голосового
	// Telegram), lang — код языка («ru») или пусто: тогда модель определит сама.
	Transcribe(ctx context.Context, audio []byte, mime, lang string) (string, error)
}

// ErrNoTranscribe — обёртка вокруг провайдера объявляет Transcribe всегда
// (обёртка продвигает только методы своего интерфейса), поэтому «не умею» она
// говорит ответом, а не отсутствием метода. Тот же приём, что у ErrNoImages,
// по той же причине.
var ErrNoTranscribe = errors.New("провайдер не распознаёт речь")

// Hear — попытка распознать речь. ok=false, если провайдер не умеет.
func Hear(ctx context.Context, p Provider, audio []byte, mime, lang string) (string, bool, error) {
	t, has := p.(Transcriber)
	if !has {
		return "", false, nil
	}
	out, err := t.Transcribe(ctx, audio, mime, lang)
	if errors.Is(err, ErrNoTranscribe) {
		return "", false, nil
	}
	return out, true, err
}

// ---- длительность записи: по ней считается цена ----

// bytesPerSecond — грубая прикидка для сжатых форматов, длительность
// которых из файла не прочитать: 128 кбит/с — типичный mp3.
const bytesPerSecond = 16000

// AudioSeconds — сколько длится запись. exact — число прочитано из самого
// файла (Ogg Opus, WAV); иначе это оценка по размеру, и вызывающий обязан
// об этом знать: деньги по ней считаются приблизительно.
func AudioSeconds(data []byte) (sec float64, exact bool) {
	if s, ok := oggOpusSeconds(data); ok {
		return s, true
	}
	if s, ok := wavSeconds(data); ok {
		return s, true
	}
	if len(data) == 0 {
		return 0, false
	}
	return float64(len(data)) / bytesPerSecond, false
}

// oggOpusSeconds — длительность Ogg Opus. Последняя страница контейнера
// несёт позицию в отсчётах 48 кГц (у Opus частота фиксирована), из неё
// вычитается pre-skip из заголовка OpusHead.
func oggOpusSeconds(b []byte) (float64, bool) {
	if len(b) < 27 || string(b[:4]) != "OggS" {
		return 0, false
	}
	head := b
	if len(head) > 1024 {
		head = head[:1024]
	}
	i := bytes.Index(head, []byte("OpusHead"))
	if i < 0 || len(b) < i+12 {
		return 0, false
	}
	pre := int64(binary.LittleEndian.Uint16(b[i+10:]))
	for j := bytes.LastIndex(b, []byte("OggS")); j >= 0; j = bytes.LastIndex(b[:j], []byte("OggS")) {
		if j+14 > len(b) || b[j+4] != 0 {
			continue
		}
		granule := int64(binary.LittleEndian.Uint64(b[j+6 : j+14]))
		if granule <= 0 { // -1 — «позиции нет»: страница посреди пакета
			continue
		}
		samples := granule - pre
		if samples < 0 {
			samples = 0
		}
		return float64(samples) / 48000, true
	}
	return 0, false
}

// wavSeconds — длительность WAV: размер данных на байты в секунду.
func wavSeconds(b []byte) (float64, bool) {
	if len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, false
	}
	var byteRate uint32
	for pos := 12; pos+8 <= len(b); {
		id := string(b[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if body+12 <= len(b) {
				byteRate = binary.LittleEndian.Uint32(b[body+8 : body+12])
			}
		case "data":
			if byteRate == 0 {
				return 0, false
			}
			if size <= 0 || body+size > len(b) {
				size = len(b) - body // поток без итоговой длины
			}
			return float64(size) / float64(byteRate), true
		}
		if size < 0 || body+size+size%2 <= pos {
			return 0, false
		}
		pos = body + size + size%2
	}
	return 0, false
}

// audioFileName — имя файла для multipart-запроса: OpenAI определяет
// формат по расширению, а не по типу части.
func audioFileName(mime string) string {
	m := strings.ToLower(strings.TrimSpace(mime))
	if i := strings.Index(m, ";"); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	ext := ".ogg"
	switch m {
	case "audio/mpeg", "audio/mp3", "audio/mpga":
		ext = ".mp3"
	case "audio/mp4", "audio/x-m4a", "audio/m4a", "audio/aac":
		ext = ".m4a"
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		ext = ".wav"
	case "audio/webm", "video/webm":
		ext = ".webm"
	case "audio/flac", "audio/x-flac":
		ext = ".flac"
	case "audio/ogg", "audio/opus", "application/ogg", "audio/x-opus+ogg":
		ext = ".ogg"
	}
	return "audio" + ext
}

// AudioLabel — как аудио называется в журнале отправленного: не байты, а
// секунды — по ним видно, что ушло и сколько это стоит.
func AudioLabel(sec float64) string {
	if sec < 1 {
		return "аудио меньше секунды"
	}
	return fmt.Sprintf("аудио %d с", int(sec+0.5))
}
