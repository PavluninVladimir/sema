package engine

import (
	"strings"
	"testing"
)

// Границы поиска: что уходит наружу, а что остаётся дома.

func TestSearchQueryGuard(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	cases := []struct {
		in     string
		ok     bool
		hasNot []string
	}{
		{"как читать вывод journalctl", true, nil},
		{"счёт от owner@example.org на 1890 ₽", true, []string{"@example.org", "1890"}},
		{"скан паспорта, что с ним делать", false, nil},
		{"   ", false, nil},
	}
	for _, c := range cases {
		got, err := e.searchQueryGuard(c.in)
		if (err == nil) != c.ok {
			t.Errorf("searchQueryGuard(%q) = %q, ошибка %v", c.in, got, err)
			continue
		}
		for _, bad := range c.hasNot {
			if strings.Contains(got, bad) {
				t.Errorf("в запрос наружу попало %q: %q", bad, got)
			}
		}
	}
}

func TestSearchLinks(t *testing.T) {
	answer := "Смотри https://go.dev/blog/go1.24 и https://example.org/a?utm_source=x. Ещё раз https://go.dev/blog/go1.24."
	links := searchLinks(answer)
	if len(links) != 2 {
		t.Fatalf("ссылок %d: %v", len(links), links)
	}
	if strings.HasSuffix(links[1], ".") {
		t.Errorf("точка предложения попала в ссылку: %q", links[1])
	}
}

// Фаззинг: любой ответ модели разбирается на ссылки без паники, и всё
// найденное действительно похоже на ссылку.
func FuzzSearchLinks(f *testing.F) {
	f.Add("https://example.org")
	f.Add("текст без ссылок")
	f.Add("")
	f.Fuzz(func(t *testing.T, answer string) {
		for _, l := range searchLinks(answer) {
			if !strings.HasPrefix(l, "http") {
				t.Fatalf("не ссылка: %q", l)
			}
		}
	})
}

// Текст страницы достаёт ядро, а не регулярки в скрипте модели.
func TestHtmlToText(t *testing.T) {
	raw := `<html><head><style>.a{color:red}</style><script>var x=1</script></head>
<body><h1>Заголовок</h1><p>Первый абзац &laquo;в кавычках&raquo;.</p><br><p>Второй&nbsp;абзац.</p></body></html>`
	got := htmlToText(raw)
	for _, want := range []string{"Заголовок", "Первый абзац «в кавычках»", "Второй абзац"} {
		if !strings.Contains(got, want) {
			t.Errorf("в тексте нет %q: %q", want, got)
		}
	}
	for _, bad := range []string{"<h1>", "color:red", "var x=1", "&nbsp;"} {
		if strings.Contains(got, bad) {
			t.Errorf("в тексте осталась разметка %q", bad)
		}
	}
	if htmlToText("") != "" {
		t.Error("пустая страница дала текст")
	}
}

func TestPageFileName(t *testing.T) {
	cases := map[string]string{
		"https://go.dev/blog/go1.24":             "go.dev_go1.24.txt",
		"https://example.org/":                   "example.org.txt",
		"http://example.org/news/1?utm_source=x": "example.org_1.txt",
	}
	for url, want := range cases {
		if got := pageFileName(url); got != want {
			t.Errorf("pageFileName(%q) = %q, хотели %q", url, got, want)
		}
	}
}

// Фаззинг: любая страница разбирается без паники и без разметки на выходе.
func FuzzHtmlToText(f *testing.F) {
	f.Add("<p>привет</p>")
	f.Add("<script>alert(1)</script>")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		got := htmlToText(raw)
		if strings.Contains(got, "<script") || strings.Contains(got, "</style>") {
			t.Fatalf("разметка выжила: %q", got)
		}
	})
}
