package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

type anthropic struct {
	name    string
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

func newAnthropic(name string, p config.Provider) *anthropic {
	base := p.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return &anthropic{
		name:    name,
		baseURL: base,
		model:   p.Model,
		apiKey:  p.Key(),
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (a *anthropic) Name() string { return a.name }

func (a *anthropic) Complete(ctx context.Context, prompt string) (string, error) {
	req := map[string]any{
		"model":      a.model,
		"max_tokens": 300,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	// поисковый вызов: модель ищет сама своим инструментом, ответу нужно
	// больше места под ссылки
	if WebSearchOf(ctx) {
		req["max_tokens"] = 4000
		req["tools"] = []map[string]any{{
			"type": "web_search_20250305", "name": "web_search", "max_uses": 5,
		}}
	}
	body, _ := json.Marshal(req)
	started := time.Now()
	text, u, err := a.send(ctx, body)
	report(ctx, a.name, "text", prompt, text, u, started, err)
	return text, err
}

// send отправляет уже собранное тело запроса и достаёт текст ответа.
func (a *anthropic) send(ctx context.Context, body []byte) (string, Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("%s: HTTP %d: %s", a.name, resp.StatusCode, raw)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", Usage{}, err
	}
	u := Usage{In: out.Usage.In, Out: out.Usage.Out, Exact: out.Usage.In > 0}
	// при поиске ответ приходит несколькими кусками вперемешку с находками:
	// берём весь текст, а не первый кусок
	var text []string
	for _, c := range out.Content {
		if c.Type == "text" && c.Text != "" {
			text = append(text, c.Text)
		}
	}
	if len(text) > 0 {
		return strings.Join(text, "\n"), u, nil
	}
	return "", u, fmt.Errorf("%s: empty response", a.name)
}

// CompleteImage показывает модели картинку вместе с текстом запроса.
func (a *anthropic) CompleteImage(ctx context.Context, prompt string, image []byte, mime string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      a.model,
		"max_tokens": 2000,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "image", "source": map[string]string{
					"type": "base64", "media_type": mime,
					"data": base64.StdEncoding.EncodeToString(image),
				}},
				{"type": "text", "text": prompt},
			},
		}},
	})
	started := time.Now()
	text, u, err := a.send(ctx, body)
	report(ctx, a.name, "image", prompt, text, u, started, err)
	return text, err
}
