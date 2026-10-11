package api

import (
	"compress/gzip"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// Разметка экрана едет сжатой. Ручка /api/ui сжимала ответ с самого
// начала, а веб-путь — нет, хотя именно им ходит единственный живой
// клиент: «Здоровье» приезжало на 103 КБ вместо девяти.
func TestScreenMarkupTravelsCompressed(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	r := httptest.NewRequest("GET", "/ui?screen=health", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("HX-Request", "true")
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:8765" // «с этой машины»: вход в пробе не заводим
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("код %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("разметка не сжалась: %q", w.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Error("без Vary прокси отдаст сжатое тому, кто не умеет распаковывать")
	}
	сжато := w.Body.Len() // чтение из recorder'а опустошает буфер — меряем до
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	раскрыто, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(раскрыто), `id="screen"`) {
		t.Error("распаковалась не разметка экрана")
	}

	// клиент без gzip получает то же самое как есть
	r2 := httptest.NewRequest("GET", "/ui?screen=health", nil)
	r2.Header.Set("HX-Request", "true")
	r2.RemoteAddr, r2.Host = "127.0.0.1:5000", "127.0.0.1:8765"
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Header().Get("Content-Encoding") != "" {
		t.Errorf("не просили сжимать, а сжали: %q", w2.Header().Get("Content-Encoding"))
	}
	if w2.Body.String() != string(раскрыто) {
		t.Error("сжатое и несжатое разошлись")
	}
	t.Logf("разметка «Здоровья»: %d байт сжато против %d как есть", сжато, len(раскрыто))
}
