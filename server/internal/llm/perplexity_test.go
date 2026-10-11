package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

// Agent API: запрос должен нести инструмент web_search, а ответ —
// разбираться и приносить ссылки. Без ссылок найденное нечем проверить.
func TestPerplexityAgentCall(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent" {
			t.Errorf("постучались не туда: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer ключ" {
			t.Errorf("ключ не ушёл: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"Курс 92 рубля",
			"annotations":[{"url":"https://cbr.ru/","title":"ЦБ"}]}]}],
			"usage":{"input_tokens":11,"output_tokens":22}}`))
	}))
	defer srv.Close()

	p := newPerplexity("Perplexity", config.Provider{Type: "perplexity", BaseURL: srv.URL, Model: "openai/gpt-5.6-luna", APIKey: "ключ"})
	// поиск включает не провайдер, а пометка вызова: роль «Поиск»
	text, err := p.Complete(WithWebSearch(context.Background()), "какой курс")
	if err != nil {
		t.Fatalf("вызов: %v", err)
	}
	if !strings.Contains(text, "Курс 92 рубля") || !strings.Contains(text, "https://cbr.ru/") {
		t.Errorf("ответ без текста или ссылок:\n%s", text)
	}
	if got["model"] != "openai/gpt-5.6-luna" || got["input"] != "какой курс" {
		t.Errorf("не то ушло: %+v", got)
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("инструменты не объявлены: %+v", got["tools"])
	}
	kinds := map[string]bool{}
	for _, t := range tools {
		if m, ok := t.(map[string]any); ok {
			kinds[fmt.Sprint(m["type"])] = true
		}
	}
	if !kinds["web_search"] || !kinds["fetch_url"] {
		t.Errorf("нужны и поиск, и чтение страниц: %+v", tools)
	}
}

// Адрес пишут и с /v1, и без, а модель могут не указать вовсе.
func TestPerplexityDefaults(t *testing.T) {
	p := newPerplexity("П", config.Provider{Type: "perplexity", BaseURL: "https://api.perplexity.ai/v1/"})
	if p.baseURL != "https://api.perplexity.ai" {
		t.Errorf("адрес приведён неверно: %q", p.baseURL)
	}
	if p.model != "openai/gpt-5.6-luna" {
		t.Errorf("модель по умолчанию: %q", p.model)
	}
}

// Пустой ответ — ошибка, а не «модель промолчала».
func TestPerplexityEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"output":[],"usage":{}}`))
	}))
	defer srv.Close()
	p := newPerplexity("П", config.Provider{Type: "perplexity", BaseURL: srv.URL})
	if _, err := p.Complete(context.Background(), "вопрос"); err == nil {
		t.Fatal("пустой ответ прошёл как удачный")
	}
}

// Пресет вместо модели: пишется в том же поле, уходит другим.
func TestPerplexityPreset(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"status":"completed","output_text":"ответ","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := newPerplexity("П", config.Provider{BaseURL: srv.URL, Model: "preset:medium"})
	if _, err := p.Complete(context.Background(), "вопрос"); err != nil {
		t.Fatal(err)
	}
	if got["preset"] != "medium" {
		t.Errorf("пресет не ушёл: %+v", got)
	}
	if _, has := got["model"]; has {
		t.Error("вместе с пресетом ушла и модель")
	}
}

// Источники берутся и из пунктов search_results, а не только из аннотаций.
func TestPerplexitySearchResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"completed","output":[
			{"type":"search_results","queries":["курс"],"results":[{"url":"https://cbr.ru/","title":"ЦБ","date":"2026-09-04"}]},
			{"type":"message","content":[{"type":"output_text","text":"Курс 92"}]}],
			"usage":{"input_tokens":5,"output_tokens":6}}`))
	}))
	defer srv.Close()
	p := newPerplexity("П", config.Provider{BaseURL: srv.URL})
	text, err := p.Complete(context.Background(), "курс")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Курс 92") || !strings.Contains(text, "https://cbr.ru/") || !strings.Contains(text, "2026-09-04") {
		t.Errorf("источник из search_results не доехал:\n%s", text)
	}
}

// Незавершённый ответ — ошибка: молчание нельзя выдавать за итог.
func TestPerplexityIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"failed","error":{"message":"tool limit"},"output":[{"type":"message","content":[{"type":"output_text","text":"полуответ"}]}]}`))
	}))
	defer srv.Close()
	p := newPerplexity("П", config.Provider{BaseURL: srv.URL})
	_, err := p.Complete(context.Background(), "вопрос")
	if err == nil || !strings.Contains(err.Error(), "tool limit") {
		t.Fatalf("незавершённый ответ прошёл как удачный: %v", err)
	}
}

// 429 говорит, когда возвращаться, — это должно дойти до очереди.
func TestPerplexityTooOften(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newPerplexity("П", config.Provider{BaseURL: srv.URL})
	_, err := p.Complete(context.Background(), "вопрос")
	if err == nil || !strings.Contains(err.Error(), "30") {
		t.Fatalf("срок возврата потерян: %v", err)
	}
}

// Без пометки поиска инструменты не объявляются: это обычная модель тем
// же ключом, а каждый вызов web_search стоит денег.
func TestPerplexityWithoutSearch(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"status":"completed","output_text":"просто ответ","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := newPerplexity("П", config.Provider{BaseURL: srv.URL, Model: "openai/gpt-5.6-luna"})
	text, err := p.Complete(context.Background(), "сколько будет два плюс два")
	if err != nil {
		t.Fatal(err)
	}
	if text != "просто ответ" {
		t.Errorf("ответ не тот: %q", text)
	}
	if _, has := got["tools"]; has {
		t.Errorf("поиск объявлен без надобности: %+v", got["tools"])
	}
}

// Предел ответа обязателен для моделей Anthropic через Agent API: без него
// приходит отказ 400, поэтому он должен уходить, когда задан.
func TestPerplexityMaxOut(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"status":"completed","output_text":"да","usage":{}}`))
	}))
	defer srv.Close()
	p := newPerplexity("П", config.Provider{BaseURL: srv.URL, Model: "anthropic/claude-opus-5", MaxOut: 8192})
	if _, err := p.Complete(context.Background(), "вопрос"); err != nil {
		t.Fatal(err)
	}
	if got["max_output_tokens"] != float64(8192) {
		t.Errorf("предел ответа не ушёл: %+v", got["max_output_tokens"])
	}
	// не задан — не передаём: чужим провайдерам он мешает
	p2 := newPerplexity("П", config.Provider{BaseURL: srv.URL})
	got = nil
	if _, err := p2.Complete(context.Background(), "вопрос"); err != nil {
		t.Fatal(err)
	}
	if _, has := got["max_output_tokens"]; has {
		t.Error("предел ушёл, хотя не задан")
	}
}
