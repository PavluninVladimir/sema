package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Часть отдаётся по HTTP той же дорогой, что и экран, и несёт строки —
// без шапки и меню. Пустой ответ здесь читался бы как «в разделе пусто».
func TestFragmentOverHTTP(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveMAXAccount(&store.MAXAccount{Phone: "+70000000000", State: "ok"}); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 3; i++ {
		if err := st.SaveMAXChat(&store.MAXChat{ID: i, Title: "Чат", Kind: "chat"}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	r, err := http.Get(srv.URL + "/ui?screen=settings:max&part=max-chats")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 {
		t.Fatalf("часть: %d %s", r.StatusCode, body)
	}
	// чаты едут строками матрицы прав: id — у самой строки таблицы
	for _, id := range []string{`id="mx-max-1"`, `id="mx-max-2"`, `id="mx-max-3"`} {
		if !strings.Contains(string(body), id) {
			t.Fatalf("в части нет строки %s; тело (%d байт): %.400s", id, len(body), strings.Join(strings.Fields(string(body)), " "))
		}
	}
	if strings.Contains(string(body), `id="screen"`) || strings.Contains(string(body), "<h1") {
		t.Fatal("часть приехала с шапкой экрана")
	}
}
