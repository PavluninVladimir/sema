package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

// openAI — любой OpenAI-совместимый сервер: LM Studio, Ollama, vLLM,
// OpenRouter, сам OpenAI. Достаточно base_url и model в конфиге.
type openAI struct {
	name    string
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
	// limit — предел ответа в токенах; ноль — не отправлять. Сейчас ноль
	// всегда: так работали и работают DeepSeek, llama.cpp и OpenRouter, а
	// моделям с рассуждением предел мешает (см. quirkCompletionTokens). Поле
	// есть, чтобы имя параметра выбиралось в одном месте, когда предел
	// понадобится.
	limit int
}

func newOpenAI(name string, p config.Provider) *openAI {
	return &openAI{
		name:    name,
		baseURL: strings.TrimRight(p.BaseURL, "/"),
		model:   p.Model,
		apiKey:  p.Key(),
		client:  &http.Client{Timeout: 10 * time.Minute}, // предел задаёт контекст вызова (таймаут роли)
	}
}

func (o *openAI) Name() string { return o.name }

// ---- модели с рассуждением ----
//
// Модели OpenAI с рассуждением (gpt-5*, gpt-6*, o1/o3/o4…) отвечают 400 на
// параметры, законные у остальных: «Unsupported value: 'temperature' does not
// support 0 with this model» и «Unsupported parameter: 'max_tokens' … Use
// 'max_completion_tokens'». Через Perplexity такие модели работали — он
// сглаживал параметры, — а напрямую нет.
//
// Список моделей по имени не выучить: они выходят чаще, чем правится код, и
// «reasoning» бывает и у чужих серверов. Поэтому ответ сервера главнее
// имени: 400 с названием параметра — повтор без него, и модель запоминается.
// Имя нужно лишь затем, чтобы первый же вызов известной модели не тратил
// попытку зря; ошибиться оно вправе, ответ сервера поправит.

// quirk — чем сервер отличается от «обычного» OpenAI-совместимого.
type quirk uint32

const (
	// quirkNoTemperature — temperature=0 не принимается, остаётся умолчание
	quirkNoTemperature quirk = 1 << iota
	// quirkCompletionTokens — предел ответа зовётся max_completion_tokens.
	// У моделей с рассуждением он считает и скрытые токены рассуждения:
	// маленький предел съедается ими целиком, и ответа нет вовсе
	quirkCompletionTokens
	// quirkNoEffort — reasoning_effort не принимается
	quirkNoEffort
)

// maxQuirkRetries — сколько раз один вызов подстраивается под сервер: по
// одному разу на каждую особенность, и хватит.
const maxQuirkRetries = 3

// reReasoning — модели OpenAI с рассуждением по имени: gpt-5, gpt-5.1,
// gpt-6-luna, gpt-6.1-sol, o1, o3-mini, o4-mini. gpt-4o и gpt-4.1 сюда не
// входят: они принимают temperature.
var reReasoning = regexp.MustCompile(`(?i)^(gpt-([5-9]|[1-9]\d)([.\-]|$)|o[1-9]([.\-]|$))`)

// isReasoning — похоже ли имя на модель с рассуждением. Приставка провайдера
// («openai/gpt-5») отбрасывается.
func isReasoning(model string) bool {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return reReasoning.MatchString(model)
}

// quirkTable — выученное про сервер и модель. Ключ — адрес и название модели:
// провайдера пересобирают при смене ключа, а выученное должно пережить это,
// иначе каждый раз одна попытка уходила бы в отказ. Память процесса; после
// перезапуска модель выучится за один лишний вызов.
var quirkTable sync.Map

// quirks — выученное про эту модель; для известной по имени с самого начала
// стоят обе привычки моделей с рассуждением.
func (o *openAI) quirks() *atomic.Uint32 {
	key := o.baseURL + "|" + o.model
	if v, ok := quirkTable.Load(key); ok {
		return v.(*atomic.Uint32)
	}
	q := new(atomic.Uint32)
	if isReasoning(o.model) {
		q.Store(uint32(quirkNoTemperature | quirkCompletionTokens))
	}
	v, _ := quirkTable.LoadOrStore(key, q)
	return v.(*atomic.Uint32)
}

// quirkOf — на какую особенность жалуется 400. Смотрит на поле param, а
// если сервер его не заполнил — на текст: у разных серверов ошибка приходит
// по-разному.
func quirkOf(raw []byte) quirk {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	msg := strings.ToLower(e.Error.Message)
	if msg == "" {
		msg = strings.ToLower(string(raw))
	}
	param := strings.ToLower(e.Error.Param)
	switch {
	case param == "temperature" || strings.Contains(msg, "'temperature'") || strings.Contains(msg, `"temperature"`):
		return quirkNoTemperature
	case param == "max_tokens" || strings.Contains(msg, "'max_tokens'") || strings.Contains(msg, "max_completion_tokens"):
		return quirkCompletionTokens
	case param == "reasoning_effort" || strings.Contains(msg, "reasoning_effort"):
		return quirkNoEffort
	}
	return 0
}

// fields — тело запроса к /chat/completions с учётом выученного. Собирается
// заново на каждую попытку: после отказа оно уже другое.
func (o *openAI) fields(ctx context.Context, messages any, stream bool) map[string]any {
	q := quirk(o.quirks().Load())
	body := map[string]any{"model": o.model, "messages": messages}
	if q&quirkNoTemperature == 0 {
		body["temperature"] = 0
	}
	if o.limit > 0 {
		if q&quirkCompletionTokens != 0 {
			body["max_completion_tokens"] = o.limit
		} else {
			body["max_tokens"] = o.limit
		}
	}
	// как долго думать — только моделям с рассуждением: остальным параметр
	// неизвестен, и одни серверы его игнорируют, а другие отвечают 400
	if effort := EffortOf(ctx); effort != "" && q&quirkNoEffort == 0 && isReasoning(o.model) {
		body["reasoning_effort"] = effort
	}
	if stream {
		body["stream"] = true
		// последний кусок с usage: точные токены и в потоке (сервер может проигнорировать)
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return body
}

// post отправляет запрос и подстраивается под сервер: 400 с названием
// параметра, который у этой модели не принимается, — повтор без него и
// запись в quirkTable. Прочие ответы (и 400 про другое) возвращаются как
// есть, с целым телом.
func (o *openAI) post(ctx context.Context, build func() map[string]any, sse bool) (*http.Response, error) {
	q := o.quirks()
	for attempt := 0; ; attempt++ {
		payload, err := json.Marshal(build())
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if sse {
			req.Header.Set("Accept", "text/event-stream")
		}
		if o.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+o.apiKey)
		}
		resp, err := o.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusBadRequest || attempt >= maxQuirkRetries {
			return resp, nil
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if bit := quirkOf(raw); bit != 0 && quirk(q.Load())&bit == 0 {
			q.Or(uint32(bit))
			log.Printf("openai: модель %s не принимает %s — повторяю без него и запоминаю", o.model, quirkName(bit))
			continue
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		return resp, nil
	}
}

func quirkName(q quirk) string {
	switch q {
	case quirkNoTemperature:
		return "temperature"
	case quirkCompletionTokens:
		return "max_tokens"
	case quirkNoEffort:
		return "reasoning_effort"
	}
	return "параметр"
}

// errRanOutOnReasoning — модель потратила весь предел на рассуждение и
// ответа не написала. Пустая строка вместо ответа выглядела бы как «модель
// промолчала», а это другое: чинится пределом, а не сменой модели.
func (o *openAI) errRanOutOnReasoning(reasoning int) error {
	why := "предел ответа кончился раньше, чем модель начала писать"
	if reasoning > 0 {
		why = fmt.Sprintf("предел ответа съело рассуждение (%d токенов), самого ответа нет", reasoning)
	}
	return fmt.Errorf("%s: %s — поднимите предел или уберите его", o.name, why)
}

func (o *openAI) Complete(ctx context.Context, prompt string) (string, error) {
	// поиск у OpenAI живёт не в chat/completions, а в Responses API: там
	// объявляется инструмент web_search. Обычные вызовы туда не уводим —
	// у них другой разбор ответа и другие подводные камни
	if WebSearchOf(ctx) {
		return o.search(ctx, prompt)
	}
	messages := []map[string]string{{"role": "user", "content": prompt}}
	started := time.Now()
	text, u, err := o.send(ctx, func() map[string]any { return o.fields(ctx, messages, false) })
	report(ctx, o.name, "text", prompt, text, u, started, err)
	return text, err
}

// send отправляет запрос и достаёт текст ответа и учёт.
func (o *openAI) send(ctx context.Context, build func() map[string]any) (string, Usage, error) {
	resp, err := o.post(ctx, build, false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("%s: HTTP %d: %s", o.name, resp.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			// кэш входа называют по-разному: DeepSeek — своими полями,
			// OpenAI-совместимые — вложенным details. Читаем оба
			CacheHit     int `json:"prompt_cache_hit_tokens"`
			CacheMiss    int `json:"prompt_cache_miss_tokens"`
			PromptDetail struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionDetail struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", Usage{}, err
	}
	u := Usage{In: out.Usage.Prompt, Out: out.Usage.Completion, Exact: out.Usage.Prompt > 0,
		Cached: out.Usage.CacheHit, Reasoning: out.Usage.CompletionDetail.Reasoning}
	if u.Cached == 0 {
		u.Cached = out.Usage.PromptDetail.Cached
	}
	if len(out.Choices) == 0 {
		return "", u, fmt.Errorf("%s: empty response", o.name)
	}
	if strings.TrimSpace(out.Choices[0].Message.Content) == "" && out.Choices[0].FinishReason == "length" {
		return "", u, o.errRanOutOnReasoning(u.Reasoning)
	}
	return out.Choices[0].Message.Content, u, nil
}

// CompleteStream — тот же /chat/completions, но с stream=true: ответ
// приходит по кусочкам, и Сёма «печатает» его в чате на глазах.
func (o *openAI) CompleteStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	messages := []map[string]string{{"role": "user", "content": prompt}}
	started := time.Now()
	resp, err := o.post(ctx, func() map[string]any { return o.fields(ctx, messages, true) }, true)
	if err != nil {
		report(ctx, o.name, "stream", prompt, "", Usage{}, started, err)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		err := fmt.Errorf("%s: HTTP %d: %s", o.name, resp.StatusCode, raw)
		report(ctx, o.name, "stream", prompt, "", Usage{}, started, err)
		return "", err
	}

	var full strings.Builder
	var usage Usage
	finish := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			// у потока статистика приходит последним куском
			Usage *struct {
				Prompt       int `json:"prompt_tokens"`
				Completion   int `json:"completion_tokens"`
				CacheHit     int `json:"prompt_cache_hit_tokens"`
				PromptDetail struct {
					Cached int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				CompletionDetail struct {
					Reasoning int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil && chunk.Usage.Prompt > 0 {
			usage = Usage{In: chunk.Usage.Prompt, Out: chunk.Usage.Completion, Exact: true,
				Cached: chunk.Usage.CacheHit, Reasoning: chunk.Usage.CompletionDetail.Reasoning}
			if usage.Cached == 0 {
				usage.Cached = chunk.Usage.PromptDetail.Cached
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if r := chunk.Choices[0].FinishReason; r != "" {
			finish = r
		}
		if d := chunk.Choices[0].Delta.Content; d != "" {
			full.WriteString(d)
			if onDelta != nil {
				onDelta(d)
			}
		}
	}
	err = sc.Err()
	if err == nil && strings.TrimSpace(full.String()) == "" && finish == "length" {
		err = o.errRanOutOnReasoning(usage.Reasoning)
	}
	report(ctx, o.name, "stream", prompt, full.String(), usage, started, err)
	return full.String(), err
}

// CompleteImage — картинка в формате data-URL: так её принимают и OpenAI,
// и локальные vision-модели в LM Studio. Текстовая модель ответит ошибкой,
// и Сёма отступит на свой OCR. Поля запроса те же, что у текста, — с теми же
// поправками под модель.
func (o *openAI) CompleteImage(ctx context.Context, prompt string, image []byte, mime string) (string, error) {
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image)
	messages := []map[string]any{{
		"role": "user",
		"content": []map[string]any{
			{"type": "text", "text": prompt},
			{"type": "image_url", "image_url": map[string]string{"url": url}},
		},
	}}
	started := time.Now()
	text, u, err := o.send(ctx, func() map[string]any { return o.fields(ctx, messages, false) })
	report(ctx, o.name, "image", prompt, text, u, started, err)
	return text, err
}

// search — поисковый вызов через Responses API (`POST /responses`). Формат
// ответа другой: текст лежит в `output` среди элементов разных видов, а
// ссылки — в `annotations` у текстовых частей. Собираем и то и другое:
// найденное без ссылок нечем проверить.
func (o *openAI) search(ctx context.Context, prompt string) (string, error) {
	started := time.Now()
	body, _ := json.Marshal(map[string]any{
		"model": o.model,
		"input": prompt,
		"tools": []map[string]any{{"type": "web_search"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("%s: HTTP %d: %s", o.name, resp.StatusCode, clipText(string(raw), 400))
		report(ctx, o.name, "search", prompt, "", Usage{}, started, err)
		return "", err
	}
	var out struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Annotations []struct {
					Type  string `json:"type"`
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"annotations"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		err = fmt.Errorf("%s: ответ не разобран: %w", o.name, err)
		report(ctx, o.name, "search", prompt, "", Usage{}, started, err)
		return "", err
	}
	u := Usage{In: out.Usage.In, Out: out.Usage.Out, Exact: out.Usage.In > 0}
	if out.Error != nil && out.Error.Message != "" {
		err := fmt.Errorf("%s: %s", o.name, out.Error.Message)
		report(ctx, o.name, "search", prompt, "", u, started, err)
		return "", err
	}
	// статус не «завершено» — это ошибка, а не итог: незаконченный ответ
	// выглядит как настоящий и тем опаснее
	if st := out.Status; st != "" && st != "completed" {
		err := fmt.Errorf("%s: ответ со статусом %q", o.name, st)
		report(ctx, o.name, "search", prompt, "", u, started, err)
		return "", err
	}

	var b strings.Builder
	links := map[string]string{}
	var order []string
	for _, item := range out.Output {
		for _, c := range item.Content {
			if t := strings.TrimSpace(c.Text); t != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(t)
			}
			for _, a := range c.Annotations {
				if a.URL == "" || links[a.URL] != "" {
					continue
				}
				title := a.Title
				if title == "" {
					title = a.URL
				}
				links[a.URL] = title
				order = append(order, a.URL)
			}
		}
	}
	if len(order) > 0 {
		b.WriteString("\n\nИсточники:\n")
		for i, u := range order {
			fmt.Fprintf(&b, "%d. %s — %s\n", i+1, links[u], u)
		}
	}
	text := b.String()
	if strings.TrimSpace(text) == "" {
		err := fmt.Errorf("%s: пустой ответ поиска", o.name)
		report(ctx, o.name, "search", prompt, "", u, started, err)
		return "", err
	}
	report(ctx, o.name, "search", prompt, text, u, started, nil)
	return text, nil
}
