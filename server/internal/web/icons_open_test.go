package web

import (
	"bytes"
	"encoding/json"
	"image"
	_ "image/png"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Значки приложения — часть оболочки и открыты без входа: iPhone просит их
// без cookie, когда человек кладёт Сёму на экран «Домой», и 401 вместо
// картинки означает безликую букву на сером. В файлах ничего, кроме
// картинки, нет — вход тут ничего не защищает.
func TestEveryIconIsShell(t *testing.T) {
	ents, err := files.ReadDir("static")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "icon") {
			continue
		}
		seen++
		if !IsShell("/" + e.Name()) {
			t.Errorf("/%s лежит среди файлов оболочки, но без входа не отдаётся", e.Name())
		}
	}
	if seen < 6 {
		t.Fatalf("значков найдено %d — поиск сломан и тест ничего не проверяет", seen)
	}
	for _, p := range []string{"/apple-touch-icon.png", "/apple-touch-icon-precomposed.png", "/favicon.ico"} {
		if !IsShell(p) {
			t.Errorf("%s не открыт без входа: система просит его сама", p)
		}
	}
	// открыто ровно перечисленное: соседние имена — нет
	for _, p := range []string{"/icon-secret.png", "/apple-touch-icon-x.png", "/favicon.icox", "/fonts/", "/static/icon.svg", "/ui", "/do"} {
		if IsShell(p) {
			t.Errorf("%s открыт без входа, а перечислен не был", p)
		}
	}
}

// Всё, на что ссылаются манифест и страница, открыто без входа: имя,
// разошедшееся со списком, снова дало бы 401 молча.
func TestIconsNamedByManifestAndPageAreShell(t *testing.T) {
	data, err := files.ReadFile("static/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Icons []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	refs := []string{}
	for _, ic := range manifest.Icons {
		refs = append(refs, ic.Src)
	}
	page, err := files.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`<link rel="(?:icon|apple-touch-icon)" href="([^"]+)"`).FindAllStringSubmatch(string(page), -1) {
		refs = append(refs, m[1])
	}
	if len(refs) < 5 {
		t.Fatalf("ссылок на значки найдено %d — разбор сломан", len(refs))
	}
	h := Handler()
	for _, ref := range refs {
		if !IsShell(ref) {
			t.Errorf("%s назван в манифесте или странице, а без входа не отдаётся", ref)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", ref, nil))
		if w.Code != 200 || strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: код %d, тип %q — отдалась не картинка", ref, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

// Значок отдаётся с настоящим типом, по правилам кэша оболочки (отпечаток
// сборки, 304 на повтор) и не пережатым: PNG уже сжат сам.
func TestIconsServedLikeShell(t *testing.T) {
	h := Handler()
	cases := []struct {
		path, typ string
		width     int
	}{
		{"/icon-180.png", "image/png", 180},
		{"/icon-192.png", "image/png", 192},
		{"/icon-512.png", "image/png", 512},
		{"/icon-maskable-512.png", "image/png", 512},
		{"/icon.svg", "image/svg+xml", 0},
		{"/icon-maskable.svg", "image/svg+xml", 0},
		{"/apple-touch-icon.png", "image/png", 180},
		{"/apple-touch-icon-precomposed.png", "image/png", 180},
		{"/favicon.ico", "image/png", 192},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("%s: код %d", c.path, w.Code)
			continue
		}
		if got := w.Header().Get("Content-Type"); got != c.typ {
			t.Errorf("%s: тип %q, ждали %q", c.path, got, c.typ)
		}
		etag := w.Header().Get("ETag")
		if !strings.Contains(etag, Build()) {
			t.Errorf("%s: отпечаток не привязан к сборке: %q", c.path, etag)
		}
		if w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: правила кэша не как у оболочки: %q", c.path, w.Header().Get("Cache-Control"))
		}
		if c.width > 0 {
			if w.Header().Get("Content-Encoding") != "" {
				t.Errorf("%s: PNG пережат — он сжат сам", c.path)
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
			if err != nil || cfg.Width != c.width {
				t.Errorf("%s: отдалась не та картинка (%v, ширина %d, ждали %d)", c.path, err, cfg.Width, c.width)
			}
		}
		r2 := httptest.NewRequest("GET", c.path, nil)
		r2.Header.Set("If-None-Match", etag)
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		if w2.Code != 304 {
			t.Errorf("%s: на тот же отпечаток ждали 304, пришло %d", c.path, w2.Code)
		}
	}
}
