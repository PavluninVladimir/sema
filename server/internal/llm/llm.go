// Package llm — подключаемые модели. Любой провайдер сводится к интерфейсу
// Provider; новые типы добавляются в Build без изменения остального кода.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

type Email struct {
	From    string
	Subject string
	Snippet string
}

type Verdict struct {
	Spam       bool    `json:"spam"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

type Provider interface {
	Name() string
	// Complete отправляет prompt и возвращает сырой текст ответа модели.
	Complete(ctx context.Context, prompt string) (string, error)
}

// Streamer — провайдер, умеющий отдавать ответ по кускам.
type Streamer interface {
	// CompleteStream зовёт onDelta на каждый кусочек и возвращает весь ответ.
	CompleteStream(ctx context.Context, prompt string, onDelta func(string)) (string, error)
}

// Stream — потоковый ответ, если провайдер умеет; иначе обычный вызов.
func Stream(ctx context.Context, p Provider, prompt string, onDelta func(string)) (string, error) {
	if s, ok := p.(Streamer); ok {
		return s.CompleteStream(ctx, prompt, onDelta)
	}
	text, err := p.Complete(ctx, prompt)
	if err == nil && onDelta != nil {
		onDelta(text)
	}
	return text, err
}

func Build(name string, p config.Provider) (Provider, error) {
	switch p.Type {
	case "anthropic":
		return newAnthropic(name, p), nil
	case "openai":
		return newOpenAI(name, p), nil
	case "cli":
		return newCLI(name, p), nil
	case "perplexity":
		return newPerplexity(name, p), nil
	default:
		return nil, fmt.Errorf("provider %q: unknown type %q (want anthropic|openai|cli|perplexity)", name, p.Type)
	}
}

const classifyPrompt = `You are a spam filter for a personal mailbox. Classify the email below.

From: %s
Subject: %s
Body (truncated):
%s
%s
Respond with ONLY a JSON object, no other text:
{"spam": true|false, "confidence": 0.0-1.0, "reason": "short reason in Russian"}`

// Classify: examples — необязательный блок с уже размеченными похожими
// письмами (few-shot из RAG), пустая строка — без примеров.
func Classify(ctx context.Context, p Provider, e Email, examples string) (*Verdict, error) {
	raw, err := p.Complete(ctx, fmt.Sprintf(classifyPrompt, e.From, e.Subject, e.Snippet, examples))
	if err != nil {
		return nil, err
	}
	v := &Verdict{}
	if err := json.Unmarshal([]byte(extractJSON(raw)), v); err != nil {
		return nil, fmt.Errorf("%s returned unparseable verdict %q: %w", p.Name(), raw, err)
	}
	return v, nil
}

// extractJSON достаёт первый {...} из ответа — модели любят оборачивать JSON
// в пояснения или ```-блоки.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

// Vision — провайдер, который умеет смотреть картинку, а не только текст.
// Сёма всегда сначала спрашивает модель: она читает и рукописные чеки, и
// скриншоты, где OCR ошибается. Если модель картинки не умеет — остаётся
// наш OCR, а лучшее из двух выбирает опять же модель.
type Vision interface {
	CompleteImage(ctx context.Context, prompt string, image []byte, mime string) (string, error)
}

// ErrNoImages — обёртка вокруг провайдера объявляет CompleteImage
// всегда (методы встроенного интерфейса не продвигаются дальше самого
// интерфейса), поэтому сказать «не умею» она может только ответом. Без
// этого дверь перед моделью молча выключила бы разбор картинок целиком:
// `p.(Vision)` у обёртки не срабатывал, и Сёма записывал в настройках,
// что модель провайдера картинки не принимает.
var ErrNoImages = errors.New("провайдер не принимает картинки")

// SeeImage — попытка показать картинку модели. ok=false, если не умеет.
func SeeImage(ctx context.Context, p Provider, prompt string, image []byte, mime string) (string, bool, error) {
	v, okType := p.(Vision)
	if !okType {
		return "", false, nil
	}
	out, err := v.CompleteImage(ctx, prompt, image, mime)
	if errors.Is(err, ErrNoImages) {
		return "", false, nil
	}
	return out, true, err
}
