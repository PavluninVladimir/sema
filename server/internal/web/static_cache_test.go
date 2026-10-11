package web

import (
	"compress/gzip"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// Оболочка отдаётся с отпечатком сборки: браузер спрашивает «то же
// самое?» и получает 304 вместо ста семидесяти килобайт стилей, htmx и
// клиента на каждое открытие вкладки. Отпечаток меняется вместе со
// сборкой — старый клиент после выката не задержится.
func TestShellAnswersWithBuildETag(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/style.css", "/htmx.js", "/app.js", "/index.html"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: код %d", path, w.Code)
		}
		etag := w.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: без отпечатка браузеру нечего спросить", path)
		}
		if !strings.Contains(etag, Build()) {
			t.Errorf("%s: отпечаток не привязан к сборке: %q", path, etag)
		}
		if ct := w.Header().Get("Content-Type"); ct == "" {
			t.Errorf("%s: без Content-Type браузер не исполнит файл", path)
		}

		r2 := httptest.NewRequest("GET", path, nil)
		r2.Header.Set("If-None-Match", etag)
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		if w2.Code != 304 {
			t.Errorf("%s: на тот же отпечаток ждали 304, а пришло %d", path, w2.Code)
		}
		if w2.Body.Len() != 0 {
			t.Errorf("%s: 304 не должен нести тело (%d байт)", path, w2.Body.Len())
		}
	}
}

// Стили и клиент едут сжатыми — это 170 КБ против примерно сорока.
// Шрифты сжимать незачем: woff2 сжат сам по себе.
func TestStylesGoCompressed(t *testing.T) {
	h := Handler()
	r := httptest.NewRequest("GET", "/style.css", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("стили не сжались: %q", w.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), "#screen") {
		t.Error("распаковалось не то")
	}
	if w.Body.Len() >= len(plain) {
		t.Errorf("сжатое не меньше исходного: %d против %d", w.Body.Len(), len(plain))
	}

	fontReq := httptest.NewRequest("GET", "/fonts/"+firstFont(t), nil)
	fontReq.Header.Set("Accept-Encoding", "gzip")
	wf := httptest.NewRecorder()
	h.ServeHTTP(wf, fontReq)
	if wf.Header().Get("Content-Encoding") == "gzip" {
		t.Error("woff2 сжат сам по себе — второй раз его жать незачем")
	}
}

func firstFont(t *testing.T) string {
	t.Helper()
	ents, err := files.ReadDir("static/fonts")
	if err != nil || len(ents) == 0 {
		t.Skip("шрифтов нет")
	}
	return ents[0].Name()
}

// Оболочка по адресу с версией сборки хранится год без проверок, едет
// очищенной от комментариев и сжатой один раз (perf-pass 6.1–6.2); index.html
// и работник просят файлы именно так.
func TestShellVersionedAndPrepared(t *testing.T) {
	h := Handler()
	get := func(path string, gz bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if gz {
			r.Header.Set("Accept-Encoding", "gzip")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	page := get("/", false).Body.String()
	for _, f := range []string{"/app.js", "/style.css", "/htmx.js", "/fonts.css"} {
		if !strings.Contains(page, f+"?v="+Build()) {
			t.Errorf("index.html просит %s без версии сборки", f)
		}
	}
	if strings.Contains(page, buildMark) {
		t.Error("в index.html осталась метка сборки")
	}
	w := get("/app.js?v="+Build(), true)
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("app.js с версией: Cache-Control %q", cc)
	}
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("app.js едет несжатым")
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if strings.Contains(string(body), "\n// ") || strings.Contains(string(body), "Всё, чего не умеет htmx сам") {
		t.Error("в отданном клиенте остались комментарии")
	}
	if !strings.Contains(string(body), "openScreen") {
		t.Error("отдан не клиент")
	}
	if cc := get("/app.js?v=чужая", false).Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("app.js с чужой версией: Cache-Control %q, ждали проверку", cc)
	}
	if cc := get("/app.js", false).Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("app.js без версии: Cache-Control %q", cc)
	}
	sw := get("/sw.js", false).Body.String()
	if !strings.Contains(sw, "/app.js?v="+Build()) {
		t.Error("работник кладёт в кэш клиент без версии — по адресу из index.html он его не найдёт")
	}
}
