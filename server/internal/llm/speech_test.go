package llm

// Распознавание на поддельном OpenAI: сервер записывает, что у него
// спросили, и отвечает как настоящий. Так видна форма запроса — поля
// multipart, формат ответа, — а не только то, что вызов «не упал».

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

// oggOpus — минимальный Ogg Opus нужной длины: страница с OpusHead и
// страница с позицией. Звука в нём нет, но контейнер настоящий.
func oggOpus(seconds float64, preSkip uint16) []byte {
	page := func(granule int64, payload []byte) []byte {
		h := make([]byte, 27)
		copy(h, "OggS")
		binary.LittleEndian.PutUint64(h[6:], uint64(granule))
		h[26] = 1
		return append(append(h, byte(len(payload))), payload...)
	}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8], head[9] = 1, 1
	binary.LittleEndian.PutUint16(head[10:], preSkip)
	binary.LittleEndian.PutUint32(head[12:], 24000)
	out := page(0, head)
	return append(out, page(int64(seconds*48000)+int64(preSkip), []byte("тишина"))...)
}

func TestAudioSecondsFromContainer(t *testing.T) {
	if sec, exact := AudioSeconds(oggOpus(3, 312)); !exact || sec < 2.99 || sec > 3.01 {
		t.Fatalf("Ogg Opus: %.3f с, точно=%v", sec, exact)
	}
	// WAV: 8 кГц, 16 бит, моно — 16000 байт в секунду; два с половиной
	// секунды — сорок тысяч байт данных
	wav := make([]byte, 44+40000)
	copy(wav, "RIFF")
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint32(wav[28:], 16000)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 40000)
	if sec, exact := AudioSeconds(wav); !exact || sec < 2.49 || sec > 2.51 {
		t.Fatalf("WAV: %.3f с, точно=%v", sec, exact)
	}
	// mp3 и прочее сжатое — оценка по размеру, и она названа оценкой
	if sec, exact := AudioSeconds(make([]byte, 32000)); exact || sec != 2 {
		t.Fatalf("оценка по размеру: %.1f с, точно=%v", sec, exact)
	}
	if sec, _ := AudioSeconds(nil); sec != 0 {
		t.Fatalf("пустая запись длится %.1f с", sec)
	}
	// обрезанный контейнер не роняет разбор
	if _, exact := AudioSeconds(oggOpus(3, 0)[:30]); exact {
		t.Fatal("обрубок Ogg принят за целый")
	}
}

func fakeOpenAI(t *testing.T, h http.HandlerFunc) *openAI {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return newOpenAI("речь", config.Provider{BaseURL: srv.URL, Model: "gpt-4o-transcribe", APIKey: "ключ"})
}

func TestTranscribeSendsMultipart(t *testing.T) {
	var gotModel, gotLang, gotFormat, gotFile, gotType, gotAuth string
	var gotBytes int
	o := fakeOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("путь: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
			return
		}
		gotModel, gotLang, gotFormat = r.FormValue("model"), r.FormValue("language"), r.FormValue("response_format")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("нет поля file: %v", err)
			return
		}
		raw, _ := io.ReadAll(f)
		gotFile, gotType, gotBytes = hdr.Filename, hdr.Header.Get("Content-Type"), len(raw)
		_ = json.NewEncoder(w).Encode(map[string]any{"text": " привет, Сёма ",
			"usage": map[string]any{"type": "tokens", "input_tokens": 80, "output_tokens": 6, "total_tokens": 86}})
	})
	audio := oggOpus(4, 312)
	var calls []Call
	OnCall = func(c Call) { calls = append(calls, c) }
	t.Cleanup(func() { OnCall = nil })

	text, err := o.Transcribe(context.Background(), audio, "audio/ogg", "ru")
	if err != nil || text != "привет, Сёма" {
		t.Fatalf("текст %q, ошибка %v", text, err)
	}
	if gotModel != "gpt-4o-transcribe" || gotLang != "ru" || gotFormat != "json" {
		t.Fatalf("поля: model=%q language=%q response_format=%q", gotModel, gotLang, gotFormat)
	}
	if gotFile != "audio.ogg" || gotType != "audio/ogg" || gotBytes != len(audio) {
		t.Fatalf("файл: имя %q, тип %q, %d байт из %d", gotFile, gotType, gotBytes, len(audio))
	}
	if gotAuth != "Bearer ключ" {
		t.Fatalf("ключ: %q", gotAuth)
	}
	// журнал: не байты, а секунды; токены названы провайдером
	if len(calls) != 1 {
		t.Fatalf("записей расхода %d", len(calls))
	}
	c := calls[0]
	if c.Kind != "transcribe" || c.Prompt != "аудио 4 с" || c.Answer != "привет, Сёма" ||
		c.Usage.AudioSec < 3.99 || c.Usage.AudioSec > 4.01 || c.Usage.In != 80 || !c.Usage.Exact {
		t.Fatalf("запись расхода: %+v", c)
	}
}

func TestTranscribeErrorsAreVisible(t *testing.T) {
	o := fakeOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Invalid file format"}}`, http.StatusBadRequest)
	})
	var calls []Call
	OnCall = func(c Call) { calls = append(calls, c) }
	t.Cleanup(func() { OnCall = nil })
	if _, err := o.Transcribe(context.Background(), []byte("не звук"), "audio/mpeg", "ru"); err == nil ||
		!strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "Invalid file format") {
		t.Fatalf("ошибка сервера потерялась: %v", err)
	}
	if len(calls) != 1 || calls[0].Err == nil || calls[0].Usage.AudioSec != 0 {
		t.Fatalf("неудача записана не так: %+v", calls)
	}
	// пустая запись до сети не доходит
	if _, err := o.Transcribe(context.Background(), nil, "audio/ogg", ""); err == nil {
		t.Fatal("пустая запись принята")
	}
}

func TestTranscribeReadsPlainTextAndDurationUsage(t *testing.T) {
	o := fakeOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("просто текст\n"))
	})
	if text, err := o.Transcribe(context.Background(), []byte("аудио"), "audio/wav", ""); err != nil || text != "просто текст" {
		t.Fatalf("голый текст: %q, %v", text, err)
	}
	o = fakeOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.ParseMultipartForm(1<<20) == nil && r.FormValue("language") != "" {
			t.Errorf("пустой язык всё равно отправлен: %q", r.FormValue("language"))
		}
		_, _ = w.Write([]byte(`{"text":"так","usage":{"type":"duration","seconds":61}}`))
	})
	var calls []Call
	OnCall = func(c Call) { calls = append(calls, c) }
	t.Cleanup(func() { OnCall = nil })
	if _, err := o.Transcribe(context.Background(), []byte("аудио"), "audio/mpeg", ""); err != nil {
		t.Fatal(err)
	}
	// провайдер назвал длительность сам — она точнее нашей оценки по размеру
	if len(calls) != 1 || calls[0].Usage.AudioSec != 61 {
		t.Fatalf("длительность от провайдера: %+v", calls)
	}
}

// Умение — тип, а не настройка: у провайдера, который его не объявил, Hear
// отвечает «не умеет», а не падает.
func TestHearOnProvidersWithoutTheSkill(t *testing.T) {
	var p Provider = &cliProviderStub{}
	if _, ok, err := Hear(context.Background(), p, []byte("x"), "audio/ogg", "ru"); ok || err != nil {
		t.Fatalf("слушает: ok=%v err=%v", ok, err)
	}
}

type cliProviderStub struct{}

func (*cliProviderStub) Name() string                                     { return "заглушка" }
func (*cliProviderStub) Complete(context.Context, string) (string, error) { return "", nil }
