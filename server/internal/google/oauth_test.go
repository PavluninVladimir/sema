package google

import (
	"net/http/httptest"
	"testing"
)

func TestLoginCallbackRejectsInjectedCode(t *testing.T) {
	codes := make(chan string, 1)
	h := loginCallback("expected-state", codes)
	for _, path := range []string{"/?code=foreign", "/?state=wrong&code=foreign", "/other?state=expected-state&code=foreign"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 || len(codes) != 0 {
			t.Fatalf("accepted injected code: %s", path)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/?state=expected-state&code=own", nil))
	if w.Code != 200 || len(codes) != 1 {
		t.Fatal("valid callback rejected")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/?state=expected-state&code=duplicate", nil))
	if w.Code != 409 {
		t.Fatal("duplicate callback was not rejected")
	}
	if got := <-codes; got != "own" {
		t.Fatalf("code overwritten: %s", got)
	}
}
