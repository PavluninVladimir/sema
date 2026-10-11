package llm

// Распознавание речи у OpenAI-совместимых серверов: /audio/transcriptions.
// Тот же протокол у Groq (whisper) и у локальных серверов вроде
// faster-whisper, поэтому вид подключения тот же — «openai», а умеет ли
// модель, решает её имя в настройках, не тип провайдера.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// Transcribe — POST /audio/transcriptions, multipart: file, model, language,
// response_format. Формат ответа json понимают все модели (у gpt-4o-transcribe
// других нет); слова лежат в поле text.
func (o *openAI) Transcribe(ctx context.Context, audio []byte, mime, lang string) (string, error) {
	started := time.Now()
	sec, _ := AudioSeconds(audio)
	label := AudioLabel(sec)
	fail := func(err error) (string, error) {
		report(ctx, o.name, "transcribe", label, "", Usage{}, started, err)
		return "", err
	}
	if len(audio) == 0 {
		return fail(fmt.Errorf("%s: пустая запись", o.name))
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("model", o.model)
	_ = mw.WriteField("response_format", "json")
	if lang = strings.TrimSpace(lang); lang != "" {
		_ = mw.WriteField("language", lang)
	}
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, audioFileName(mime)))
	if mime == "" {
		mime = "application/octet-stream"
	}
	hdr.Set("Content-Type", mime)
	part, err := mw.CreatePart(hdr)
	if err != nil {
		return fail(err)
	}
	if _, err := part.Write(audio); err != nil {
		return fail(err)
	}
	if err := mw.Close(); err != nil {
		return fail(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return fail(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("%s: HTTP %d: %s", o.name, resp.StatusCode, clipText(string(raw), 300)))
	}

	var out struct {
		Text  string `json:"text"`
		Usage struct {
			Type    string  `json:"type"`
			Seconds float64 `json:"seconds"`
			In      int     `json:"input_tokens"`
			Out     int     `json:"output_tokens"`
		} `json:"usage"`
	}
	text := ""
	if err := json.Unmarshal(raw, &out); err == nil {
		text = strings.TrimSpace(out.Text)
	} else if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		// сервер проигнорировал response_format и отдал голый текст
		text = strings.TrimSpace(string(raw))
	} else {
		return fail(fmt.Errorf("%s: ответ не разобран: %w", o.name, err))
	}

	u := Usage{AudioSec: sec, In: out.Usage.In, Out: out.Usage.Out, Exact: out.Usage.In > 0 || out.Usage.Out > 0}
	if out.Usage.Type == "duration" && out.Usage.Seconds > 0 {
		u.AudioSec = out.Usage.Seconds // провайдер сам назвал длительность — она точнее нашей
	}
	if !u.Exact {
		u.Out = Estimate(text)
	}
	report(ctx, o.name, "transcribe", label, text, u, started, nil)
	return text, nil
}
