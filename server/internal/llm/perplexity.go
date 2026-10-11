package llm

// Perplexity Agent API — не chat/completions: один запрос `POST /v1/agent`
// с полем `input` и списком инструментов, а модель сама решает, звать ли
// поиск. Сёме это нужно ровно для роли «Поиск»: своего движка у неё нет,
// поиск — это модель, которая ходит в сеть.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

type perplexity struct {
	name    string
	baseURL string
	model   string
	apiKey  string
	maxOut  int
	client  *http.Client
}

func newPerplexity(name string, p config.Provider) *perplexity {
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.perplexity.ai"
	}
	// адрес пишут и с /v1, и без: приводим к одному виду, чтобы человек
	// не гадал, какую строку вставлять
	base = strings.TrimSuffix(base, "/v1")
	model := p.Model
	if model == "" {
		model = "openai/gpt-5.6-luna"
	}
	return &perplexity{name: name, baseURL: base, model: model, apiKey: p.Key(), maxOut: p.MaxOut,
		client: &http.Client{Timeout: 10 * time.Minute}}
}

func (p *perplexity) Name() string { return p.name }

func (p *perplexity) Complete(ctx context.Context, prompt string) (string, error) {
	started := time.Now()
	text, u, err := p.ask(ctx, prompt)
	report(ctx, p.name, "text", prompt, text, u, started, err)
	return text, err
}

func (p *perplexity) ask(ctx context.Context, prompt string) (string, Usage, error) {
	ask := map[string]any{"input": prompt}
	// Модели Anthropic через Agent API без предела ответа отказывают
	// («max_output_tokens is required»), остальные его принимают молча
	if p.maxOut > 0 {
		ask["max_output_tokens"] = p.maxOut
	}
	// Разница между «поиском» и «обычной моделью» здесь ровно одна —
	// инструменты. Поиск включается пометкой вызова (роль «Поиск»), а не
	// вторым провайдером с тем же ключом.
	if WebSearchOf(ctx) {
		// web_search находит страницы, fetch_url их читает: без второго
		// модель пересказывает выдержки из выдачи, а не саму страницу
		ask["tools"] = []map[string]any{{"type": "web_search"}, {"type": "fetch_url"}}
	}
	// «preset:medium» вместо модели: пресет сам подбирает модель, набор
	// инструментов и пределы. Пишется в том же поле, что и модель, —
	// человеку не нужно знать про два разных поля.
	if name, ok := strings.CutPrefix(p.model, "preset:"); ok {
		ask["preset"] = strings.TrimSpace(name)
	} else {
		ask["model"] = p.model
	}
	body, _ := json.Marshal(ask)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/agent", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		// 429 — «слишком часто»: провайдер сам говорит, когда вернуться,
		// и это надо донести до очереди, а не молча упасть
		if resp.StatusCode == http.StatusTooManyRequests {
			after := resp.Header.Get("Retry-After")
			if after == "" {
				after = "немного"
			}
			return "", Usage{}, fmt.Errorf("%s: слишком часто, вернуться через %s", p.name, after)
		}
		return "", Usage{}, fmt.Errorf("%s: HTTP %d: %s", p.name, resp.StatusCode, clipText(string(raw), 400))
	}
	var out struct {
		Status     string `json:"status"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Annotations []struct {
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"annotations"`
			} `json:"content"`
			// ответ поиска приходит отдельным пунктом того же списка
			Results []struct {
				URL   string `json:"url"`
				Title string `json:"title"`
				Date  string `json:"date"`
			} `json:"results"`
		} `json:"output"`
		Usage struct {
			In    int `json:"input_tokens"`
			Out   int `json:"output_tokens"`
			Total int `json:"total_tokens"`
			// стоимость приходит от самой Perplexity, с платой за поиск
			// включительно; поля называются по-разному в разных ответах,
			// поэтому берём оба вида
			Cost struct {
				Total   float64 `json:"total_cost"`
				Input   float64 `json:"input_tokens_cost"`
				Output  float64 `json:"output_tokens_cost"`
				Request float64 `json:"request_cost"`
			} `json:"cost"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", Usage{}, fmt.Errorf("%s: ответ не разобран: %w", p.name, err)
	}
	u := Usage{In: out.Usage.In, Out: out.Usage.Out, Exact: out.Usage.In > 0}
	if c := out.Usage.Cost; c.Total > 0 || c.Input+c.Output+c.Request > 0 {
		u.Cost, u.Currency = c.Total, "$"
		if u.Cost == 0 {
			u.Cost = c.Input + c.Output + c.Request
		}
	}

	var b strings.Builder
	if s := strings.TrimSpace(out.OutputText); s != "" {
		b.WriteString(s)
	}
	var order []string
	links := map[string]string{}
	note := func(url, title string) {
		if url == "" || links[url] != "" && title == "" {
			return
		}
		if _, seen := links[url]; !seen {
			order = append(order, url)
		}
		links[url] = title
	}
	// output_text уже собран провайдером; если его нет, склеиваем все
	// текстовые части — брать только первую значит молча обрезать ответ
	haveText := b.Len() > 0
	for _, item := range out.Output {
		for _, c := range item.Content {
			if !haveText && strings.TrimSpace(c.Text) != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(strings.TrimSpace(c.Text))
			}
			for _, a := range c.Annotations {
				note(a.URL, a.Title)
			}
		}
		for _, r := range item.Results { // пункт «search_results»
			title := r.Title
			if r.Date != "" {
				title = strings.TrimSpace(title + " · " + r.Date)
			}
			note(r.URL, title)
		}
	}
	// «незавершённый» ответ — не ответ: молчание нельзя выдавать за итог
	if out.Status != "" && out.Status != "completed" {
		why := out.Status
		if out.Error != nil && out.Error.Message != "" {
			why += ": " + out.Error.Message
		}
		return "", u, fmt.Errorf("%s: %s", p.name, why)
	}
	if b.Len() == 0 {
		return "", u, fmt.Errorf("%s: пустой ответ", p.name)
	}
	// ссылки идут в ответ отдельным списком: без них найденное нечем
	// проверить, а проверять придётся — это данные из сети, не приказ
	if len(order) > 0 {
		b.WriteString("\n\nИсточники:\n")
		for _, url := range order {
			if title := links[url]; title != "" {
				fmt.Fprintf(&b, "- %s — %s\n", title, url)
			} else {
				fmt.Fprintf(&b, "- %s\n", url)
			}
		}
	}
	return b.String(), u, nil
}

// clipText — ответ провайдера в ошибку целиком не тащим.
func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
