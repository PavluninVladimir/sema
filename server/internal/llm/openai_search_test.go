package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

// Поиск у OpenAI живёт в Responses API, а обычные вызовы — в
// chat/completions. Перепутать нельзя: у них разные адреса и разный разбор
// ответа, и обычный запрос, ушедший в поиск, стоит отдельных денег.
func TestSearchGoesToResponses(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			_, _ = w.Write([]byte(`{"status":"completed","usage":{"input_tokens":10,"output_tokens":20},
			"output":[{"type":"message","content":[{"type":"output_text","text":"Курс 92 рубля",
			"annotations":[{"type":"url_citation","url":"https://cbr.ru/","title":"ЦБ РФ"}]}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"обычный ответ"}}],
		"usage":{"prompt_tokens":5,"completion_tokens":7}}`))
	}))
	defer srv.Close()

	o := newOpenAI("проба", config.Provider{BaseURL: srv.URL, Model: "gpt-5.6"})

	plain, err := o.Complete(context.Background(), "сколько будет два плюс два")
	if err != nil || plain != "обычный ответ" {
		t.Fatalf("обычный вызов: %q, %v", plain, err)
	}
	search, err := o.Complete(WithWebSearch(context.Background()), "какой курс доллара")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(search, "Курс 92 рубля") {
		t.Fatalf("текст поиска потерялся: %q", search)
	}
	// найденное без ссылок нечем проверить — они обязаны быть в ответе
	if !strings.Contains(search, "https://cbr.ru/") || !strings.Contains(search, "Источники") {
		t.Fatalf("ссылки не приложены: %q", search)
	}
	if len(paths) != 2 || paths[0] != "/chat/completions" || paths[1] != "/responses" {
		t.Fatalf("запросы ушли не туда: %v", paths)
	}
}

// Незаконченный ответ выглядит как настоящий и тем опаснее: статус не
// «completed» — это ошибка, а не итог.
func TestSearchNotCompletedIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"incomplete","output":[{"content":[{"text":"половина ответа"}]}]}`))
	}))
	defer srv.Close()
	o := newOpenAI("проба", config.Provider{BaseURL: srv.URL, Model: "gpt-5.6"})
	if _, err := o.Complete(WithWebSearch(context.Background()), "вопрос"); err == nil {
		t.Fatal("незавершённый ответ принят за итог")
	}
}
