package web

import (
	"compress/gzip"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// Большое тело уезжает сжатым и разжимается обратно тем же; маленькое и
// клиент без gzip получают как есть.
func TestWriteMaybeGzip(t *testing.T) {
	big := strings.Repeat("описание экрана ", 500)
	r := httptest.NewRequest("GET", "/api/ui", nil)
	r.Header.Set("Accept-Encoding", "gzip, deflate")
	w := httptest.NewRecorder()
	WriteMaybeGzip(w, r, []byte(big))
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("не сжали: %q", w.Header().Get("Content-Encoding"))
	}
	if w.Body.Len() >= len(big) {
		t.Errorf("сжатое не меньше исходного: %d против %d", w.Body.Len(), len(big))
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != big {
		t.Error("после разжатия тело другое")
	}

	small := httptest.NewRecorder()
	WriteMaybeGzip(small, r, []byte("коротко"))
	if small.Header().Get("Content-Encoding") != "" {
		t.Error("сжали то, что сжимать не стоит")
	}

	plain := httptest.NewRequest("GET", "/api/ui", nil)
	pw := httptest.NewRecorder()
	WriteMaybeGzip(pw, plain, []byte(big))
	if pw.Header().Get("Content-Encoding") != "" || pw.Body.String() != big {
		t.Error("клиенту без gzip отдали сжатое")
	}
}
