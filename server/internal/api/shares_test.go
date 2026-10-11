package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ссылка отдаёт файл без входа: картинка — на месте, SVG — на скачивание,
// с nosniff и песочницей; подделка — «не действует».
func TestShareServesFileSafely(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting("auth.origin", "https://sema.example"); err != nil {
		t.Fatal(err)
	}
	add := func(name, mime, body string) *store.Attachment {
		a, _, err := s.eng.AddFile(context.Background(), name, mime, []byte(body), engine.FileSource{Kind: store.SourceUpload}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		path := url[strings.Index(url, "/share/"):]
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	png := add("баг.png", "image/png", "\x89PNG картинка")
	sh, err := s.eng.ShareFile(engine.ShareRequest{File: png.ID})
	if err != nil {
		t.Fatal(err)
	}
	rec := get(sh.URL)
	if rec.Code != http.StatusOK || rec.Body.String() != "\x89PNG картинка" {
		t.Fatalf("картинка по ссылке: %d %q", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if !strings.HasPrefix(h.Get("Content-Disposition"), "inline") || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("Set-Cookie") != "" {
		t.Errorf("заголовки картинки: %v", h)
	}
	svg := add("схема.svg", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	sh2, err := s.eng.ShareFile(engine.ShareRequest{File: svg.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(sh2.URL); !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("SVG отдан не на скачивание: %v", rec.Header())
	}
	if rec := get(sh.URL[:len(sh.URL)-3] + "xyz"); rec.Code != http.StatusNotFound {
		t.Errorf("подделка: %d", rec.Code)
	}
}

// В строке файла на «Файлах» — «Выдать ссылку», а выданная — фактом и
// «Скопировать ссылку» / «Отозвать ссылки».
func TestFileRowShareActions(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting("auth.origin", "https://sema.example"); err != nil {
		t.Fatal(err)
	}
	a, _, err := s.eng.AddFile(context.Background(), "баг.png", "image/png", []byte("картинка"), engine.FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	row := func() Component {
		doc, _ := buildAll(t, s, "files")
		var out Component
		walkAll(doc.Components, func(c Component) {
			for _, act := range everyAction(c) {
				if act.Action == "share_file" && act.Target == strconv.FormatInt(a.ID, 10) {
					out = c
				}
			}
		})
		return out
	}
	if r := row(); r.Type == "" {
		t.Fatal("у строки файла нет «Выдать ссылку»")
	}
	if _, err := s.execute(actionRequest{Action: "share_file", Target: strconv.FormatInt(a.ID, 10)}); err != nil {
		t.Fatal(err)
	}
	r := row()
	acts := map[string]bool{}
	for _, act := range everyAction(r) {
		acts[act.Action] = true
	}
	hasFact := false
	for _, f := range r.Facts {
		if f.Label == "Ссылка" {
			hasFact = true
		}
	}
	if !hasFact || !acts["$copy"] || !acts["share_revoke_file"] {
		t.Fatalf("выданная ссылка в строке файла: факты %+v, действия %v", r.Facts, acts)
	}
	if _, err := s.execute(actionRequest{Action: "share_revoke_file", Target: strconv.FormatInt(a.ID, 10)}); err != nil {
		t.Fatal(err)
	}
	if shares := st.ActiveSharesFor([]int64{a.ID}); len(shares[a.ID]) != 0 {
		t.Errorf("после «Отозвать ссылки» действуют %d", len(shares[a.ID]))
	}
}
