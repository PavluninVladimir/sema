package llm

// Модели OpenAI с рассуждением (gpt-5*, gpt-6*, o-серия) отвечают 400 на
// параметры, законные у остальных, — а DeepSeek, llama.cpp и прочие
// OpenAI-совместимые без них обойтись не могут. Проверка на поддельном
// сервере, который ведёт себя как настоящий: отвечает отказом с названием
// параметра, а не «что-то пошло не так».

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

// picky — сервер, который принимает не всё. reject — какие поля тела он
// отвергает и что при этом отвечает.
type picky struct {
	mu     sync.Mutex
	bodies []map[string]any
	reject map[string]string // поле тела → JSON-ошибка сервера
	answer string            // ответ 200; по умолчанию обычный
}

func (p *picky) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	p.mu.Lock()
	p.bodies = append(p.bodies, body)
	p.mu.Unlock()
	// порядок проверки — как у настоящего: первая же неприемлемая причина
	for _, field := range []string{"temperature", "max_tokens", "reasoning_effort"} {
		if msg, has := p.reject[field]; has {
			if _, sent := body[field]; sent {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(msg))
				return
			}
		}
	}
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"при\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"вет\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3," +
			"\"prompt_tokens_details\":{\"cached_tokens\":8}}}\n\n" +
			"data: [DONE]\n\n"))
		return
	}
	ans := p.answer
	if ans == "" {
		ans = `{"choices":[{"message":{"content":"привет"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`
	}
	_, _ = w.Write([]byte(ans))
}

func (p *picky) seen() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.bodies...)
}

func newPicky(t *testing.T, reject map[string]string) (*picky, string) {
	t.Helper()
	p := &picky{reject: reject}
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	t.Cleanup(srv.Close)
	return p, srv.URL
}

const (
	errTemperature = `{"error":{"message":"Unsupported value: 'temperature' does not support 0 with this model. Only the default (1) value is supported.","type":"invalid_request_error","param":"temperature","code":"unsupported_value"}}`
	errMaxTokens   = `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`
	errEffort      = `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error","param":"reasoning_effort"}}`
)

func fieldsOf(b map[string]any) []string {
	var out []string
	for _, k := range []string{"temperature", "max_tokens", "max_completion_tokens", "reasoning_effort"} {
		if _, has := b[k]; has {
			out = append(out, k)
		}
	}
	return out
}

// Незнакомая модель-«рассуждалка»: 400 на temperature, потом на max_tokens,
// третья попытка проходит; следующий вызов идёт сразу правильными полями, а
// пересобранный провайдер (сменили ключ) не тратит попыток заново.
func TestReasoningModelLearnsFromServerAnswers(t *testing.T) {
	p, url := newPicky(t, map[string]string{"temperature": errTemperature, "max_tokens": errMaxTokens})
	o := newOpenAI("думалка", config.Provider{BaseURL: url, Model: "странная-думалка-1"})
	o.limit = 8192

	got, err := o.Complete(context.Background(), "привет")
	if err != nil || got != "привет" {
		t.Fatalf("ответ %q, ошибка %v", got, err)
	}
	seen := p.seen()
	if len(seen) != 3 {
		t.Fatalf("запросов %d, ждали три: temperature, max_tokens, успех", len(seen))
	}
	if f := strings.Join(fieldsOf(seen[0]), ","); f != "temperature,max_tokens" {
		t.Fatalf("первый запрос: %s", f)
	}
	if f := strings.Join(fieldsOf(seen[1]), ","); f != "max_tokens" {
		t.Fatalf("второй запрос (без temperature): %s", f)
	}
	if f := strings.Join(fieldsOf(seen[2]), ","); f != "max_completion_tokens" || seen[2]["max_completion_tokens"] != float64(8192) {
		t.Fatalf("третий запрос: %s", f)
	}

	// запомнено: четвёртый вызов — сразу правильными полями, один запрос
	if _, err := o.Complete(context.Background(), "ещё"); err != nil {
		t.Fatal(err)
	}
	seen = p.seen()
	if len(seen) != 4 || strings.Join(fieldsOf(seen[3]), ",") != "max_completion_tokens" {
		t.Fatalf("после обучения запросов %d, поля %v", len(seen), fieldsOf(seen[len(seen)-1]))
	}

	// провайдера пересобирают при смене ключа: выученное переживает это
	o2 := newOpenAI("думалка", config.Provider{BaseURL: url, Model: "странная-думалка-1", APIKey: "новый"})
	o2.limit = 8192
	if _, err := o2.Complete(context.Background(), "снова"); err != nil {
		t.Fatal(err)
	}
	if n := len(p.seen()); n != 5 {
		t.Fatalf("пересобранный провайдер потратил попытки заново: запросов %d", n)
	}
}

// Обычная модель — DeepSeek, qwen3 на llama.cpp — получает то, что получала:
// temperature 0. Предел ответа сейчас никому не шлётся вовсе (поле limit
// пустое), а если его завести — обычные модели получат его как max_tokens.
func TestPlainModelKeepsTemperature(t *testing.T) {
	p, url := newPicky(t, nil)
	o := newOpenAI("qwen", config.Provider{BaseURL: url, Model: "qwen3-1.7b"})
	if _, err := o.Complete(context.Background(), "привет"); err != nil {
		t.Fatal(err)
	}
	seen := p.seen()
	if len(seen) != 1 || strings.Join(fieldsOf(seen[0]), ",") != "temperature" || seen[0]["temperature"] != float64(0) {
		t.Fatalf("обычная модель: запросов %d, поля %v", len(seen), fieldsOf(seen[0]))
	}
	o.limit = 4000
	if _, err := o.Complete(context.Background(), "привет"); err != nil {
		t.Fatal(err)
	}
	if f := strings.Join(fieldsOf(p.seen()[1]), ","); f != "temperature,max_tokens" {
		t.Fatalf("с пределом: %s", f)
	}
}

// Известная по имени модель не тратит попытку на заведомый отказ: gpt-6-luna
// с первого же вызова идёт без temperature.
func TestKnownReasoningNameSkipsWastedAttempt(t *testing.T) {
	for _, model := range []string{"gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-5.6", "o3-mini", "openai/gpt-5-mini"} {
		if !isReasoning(model) {
			t.Errorf("%s не узнана как модель с рассуждением", model)
		}
	}
	for _, model := range []string{"gpt-4o", "gpt-4.1-mini", "qwen3-1.7b", "deepseek-chat", "omni-moderation-latest", "text-embedding-3-small"} {
		if isReasoning(model) {
			t.Errorf("%s ошибочно узнана как модель с рассуждением", model)
		}
	}
	p, url := newPicky(t, map[string]string{"temperature": errTemperature, "max_tokens": errMaxTokens})
	o := newOpenAI("луна", config.Provider{BaseURL: url, Model: "gpt-6-luna"})
	o.limit = 1000
	if _, err := o.Complete(context.Background(), "привет"); err != nil {
		t.Fatal(err)
	}
	seen := p.seen()
	if len(seen) != 1 || strings.Join(fieldsOf(seen[0]), ",") != "max_completion_tokens" {
		t.Fatalf("запросов %d, поля %v", len(seen), fieldsOf(seen[0]))
	}
}

// Ответ сервера главнее имени: модель, которую имя объявило «рассуждалкой»,
// а сервер temperature принимает, всё равно работает — просто без него.
func TestNameIsOnlyAStartingGuess(t *testing.T) {
	p, url := newPicky(t, nil) // сервер принимает всё
	o := newOpenAI("гибрид", config.Provider{BaseURL: url, Model: "gpt-5-chat-latest"})
	if got, err := o.Complete(context.Background(), "привет"); err != nil || got != "привет" {
		t.Fatalf("%q, %v", got, err)
	}
	if n := len(p.seen()); n != 1 {
		t.Fatalf("запросов %d", n)
	}
}

// reasoning_effort уходит только моделям с рассуждением, а если сервер его
// не принимает — повтор без него и запоминание.
func TestReasoningEffortOnlyForReasoningModels(t *testing.T) {
	p, url := newPicky(t, nil)
	deep := WithEffort(context.Background(), "high")

	luna := newOpenAI("луна", config.Provider{BaseURL: url, Model: "gpt-6-luna"})
	if _, err := luna.Complete(deep, "подумай"); err != nil {
		t.Fatal(err)
	}
	qwen := newOpenAI("qwen", config.Provider{BaseURL: url, Model: "qwen3-1.7b"})
	if _, err := qwen.Complete(deep, "подумай"); err != nil {
		t.Fatal(err)
	}
	seen := p.seen()
	if seen[0]["reasoning_effort"] != "high" {
		t.Fatalf("модели с рассуждением не передали, как долго думать: %v", seen[0])
	}
	if _, has := seen[1]["reasoning_effort"]; has {
		t.Fatalf("обычной модели ушёл reasoning_effort: %v", seen[1])
	}
	// без просьбы параметра нет вовсе
	if _, err := luna.Complete(context.Background(), "просто"); err != nil {
		t.Fatal(err)
	}
	if _, has := p.seen()[2]["reasoning_effort"]; has {
		t.Fatal("reasoning_effort ушёл без просьбы")
	}

	// сервер (шлюз) параметр не знает — повтор без него, и запомнили
	pp, url2 := newPicky(t, map[string]string{"reasoning_effort": errEffort})
	gate := newOpenAI("шлюз", config.Provider{BaseURL: url2, Model: "gpt-6-sol"})
	if _, err := gate.Complete(deep, "подумай"); err != nil {
		t.Fatal(err)
	}
	if n := len(pp.seen()); n != 2 {
		t.Fatalf("запросов %d, ждали отказ и повтор", n)
	}
	if _, err := gate.Complete(deep, "ещё"); err != nil {
		t.Fatal(err)
	}
	if n := len(pp.seen()); n != 3 {
		t.Fatalf("параметр не запомнен как неприемлемый: запросов %d", n)
	}
}

// Поток подстраивается так же, а usage в потоке просится всегда: без него
// токены и кэш не доходят до журнала расхода.
func TestStreamAdaptsAndAsksForUsage(t *testing.T) {
	p, url := newPicky(t, map[string]string{"temperature": errTemperature})
	o := newOpenAI("думалка", config.Provider{BaseURL: url, Model: "странная-думалка-2"})
	var calls []Call
	OnCall = func(c Call) { calls = append(calls, c) }
	t.Cleanup(func() { OnCall = nil })

	var got strings.Builder
	text, err := o.CompleteStream(context.Background(), "привет", func(d string) { got.WriteString(d) })
	if err != nil || text != "привет" || got.String() != "привет" {
		t.Fatalf("поток: %q (кусочки %q), %v", text, got.String(), err)
	}
	seen := p.seen()
	if len(seen) != 2 {
		t.Fatalf("запросов %d", len(seen))
	}
	for i, b := range seen {
		opts, _ := b["stream_options"].(map[string]any)
		if b["stream"] != true || opts["include_usage"] != true {
			t.Fatalf("запрос %d без stream_options.include_usage: %v", i, b)
		}
	}
	if _, has := seen[1]["temperature"]; has {
		t.Fatal("повтор потока ушёл с temperature")
	}
	if len(calls) != 1 || calls[0].Usage.In != 11 || calls[0].Usage.Cached != 8 || !calls[0].Usage.Exact {
		t.Fatalf("токены и кэш не дошли до журнала: %+v", calls)
	}
}

// Картинка идёт теми же полями: роль «Чтение картинок» тоже уходит на gpt-6-luna.
func TestImageUsesSameFields(t *testing.T) {
	p, url := newPicky(t, map[string]string{"temperature": errTemperature})
	o := newOpenAI("глаз", config.Provider{BaseURL: url, Model: "странная-думалка-3"})
	got, err := o.CompleteImage(context.Background(), "что тут", []byte("png"), "image/png")
	if err != nil || got != "привет" {
		t.Fatalf("%q, %v", got, err)
	}
	seen := p.seen()
	if len(seen) != 2 {
		t.Fatalf("запросов %d", len(seen))
	}
	if _, has := seen[1]["temperature"]; has {
		t.Fatal("повтор с картинкой ушёл с temperature")
	}
	msgs, _ := seen[1]["messages"].([]any)
	if len(msgs) != 1 || !strings.Contains(mustJSON(msgs[0]), "data:image/png;base64,") {
		t.Fatalf("картинка потерялась при повторе: %v", seen[1]["messages"])
	}
	// и известная по имени модель идёт без temperature сразу
	q, url2 := newPicky(t, map[string]string{"temperature": errTemperature})
	luna := newOpenAI("луна", config.Provider{BaseURL: url2, Model: "gpt-6-luna"})
	if _, err := luna.CompleteImage(context.Background(), "что тут", []byte("png"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if n := len(q.seen()); n != 1 {
		t.Fatalf("запросов %d", n)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Пустой ответ с finish_reason=length — не «модель промолчала», а «предел
// съело рассуждение»: чинится пределом, а не сменой модели.
func TestEmptyAnswerOnLengthIsHonestError(t *testing.T) {
	p, url := newPicky(t, nil)
	p.answer = `{"choices":[{"message":{"content":""},"finish_reason":"length"}],
		"usage":{"prompt_tokens":40,"completion_tokens":8192,"completion_tokens_details":{"reasoning_tokens":8192}}}`
	o := newOpenAI("луна", config.Provider{BaseURL: url, Model: "gpt-6-luna"})
	var calls []Call
	OnCall = func(c Call) { calls = append(calls, c) }
	t.Cleanup(func() { OnCall = nil })

	text, err := o.Complete(context.Background(), "подумай")
	if err == nil || !strings.Contains(err.Error(), "рассуждение") || !strings.Contains(err.Error(), "8192") || text != "" {
		t.Fatalf("текст %q, ошибка %v", text, err)
	}
	// токены при этом потрачены, и журнал расхода их видит
	if len(calls) != 1 || calls[0].Err == nil || calls[0].Usage.Out != 8192 || calls[0].Usage.Reasoning != 8192 {
		t.Fatalf("расход: %+v", calls)
	}
	// обрезанный, но не пустой ответ — обычный ответ, а не ошибка
	p.answer = `{"choices":[{"message":{"content":"половина"},"finish_reason":"length"}]}`
	if got, err := o.Complete(context.Background(), "подумай"); err != nil || got != "половина" {
		t.Fatalf("обрезанный ответ: %q, %v", got, err)
	}
}

func TestEmptyStreamOnLengthIsHonestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":100," +
			"\"completion_tokens_details\":{\"reasoning_tokens\":100}}}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	o := newOpenAI("луна", config.Provider{BaseURL: srv.URL, Model: "gpt-6-luna"})
	if _, err := o.CompleteStream(context.Background(), "подумай", nil); err == nil || !strings.Contains(err.Error(), "рассуждение") {
		t.Fatalf("пустой поток на length: %v", err)
	}
}

// 400 про другое не повторяется и не глотается: ошибка сервера доходит целиком.
// А если сервер жалуется на параметр, которого мы уже не шлём, цикла нет.
func TestUnrelatedOrStubbornRefusalIsNotRetriedForever(t *testing.T) {
	var other int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid schema for messages","param":"messages"}}`))
	}))
	t.Cleanup(srv.Close)
	o := newOpenAI("сервер", config.Provider{BaseURL: srv.URL, Model: "gpt-6-luna"})
	_, err := o.Complete(context.Background(), "привет")
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "Invalid schema") {
		t.Fatalf("ошибка сервера потерялась: %v", err)
	}
	if other != 1 {
		t.Fatalf("400 про другое повторили %d раз", other)
	}

	// сервер жалуется на temperature, хотя мы её уже не шлём: одна попытка
	// на выучивание и всё, а не бесконечный повтор
	var hits int
	stubborn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errTemperature))
	}))
	t.Cleanup(stubborn.Close)
	o2 := newOpenAI("упрямый", config.Provider{BaseURL: stubborn.URL, Model: "qwen3-1.7b"})
	if _, err := o2.Complete(context.Background(), "привет"); err == nil {
		t.Fatal("упрямый сервер принят за успех")
	}
	if hits != 2 {
		t.Fatalf("запросов %d: ждали попытку с temperature и один повтор без неё", hits)
	}
}
