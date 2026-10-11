package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Иконки приложения: растровые размеры для iOS и маскируемая иконка для
// систем, обрезающих значок по своей форме. SVG остаётся для систем,
// которые его понимают.
func TestManifestAndIcons(t *testing.T) {
	data, err := files.ReadFile("static/manifest.webmanifest")
	if err != nil {
		t.Fatalf("не прочитан манифест: %v", err)
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("манифест не JSON: %v", err)
	}

	// короткое имя подписывает значок на домашнем экране; в разобранном
	// виде браузер его не отдаёт, поэтому проверяется только здесь
	if manifest["short_name"] != "Сёма" {
		t.Errorf("short_name = %v, ожидается «Сёма»", manifest["short_name"])
	}
	if manifest["display"] != "standalone" {
		t.Errorf("display = %v, ожидается standalone", manifest["display"])
	}

	entries, ok := manifest["icons"].([]interface{})
	if !ok || len(entries) == 0 {
		t.Fatal("icons не массив или пуст")
	}

	byKey := map[string]int{}
	maskable := 0

	for _, ic := range entries {
		icon, ok := ic.(map[string]interface{})
		if !ok {
			t.Fatal("иконка не объект")
		}
		src, _ := icon["src"].(string)
		sizes, _ := icon["sizes"].(string)
		purpose, _ := icon["purpose"].(string)

		byKey[fmt.Sprintf("%s,%s", src, sizes)]++
		if purpose == "maskable" {
			maskable++
		}
	}

	if entries[0].(map[string]interface{})["src"] != "/icon.svg" {
		t.Fatal("первая иконка должна быть /icon.svg")
	}

	if byKey["/icon-192.png,192x192"] != 1 {
		t.Error("нет /icon-192.png 192x192")
	}
	if byKey["/icon-512.png,512x512"] != 1 {
		t.Error("нет /icon-512.png 512x512")
	}
	if byKey["/icon-maskable-512.png,512x512"] != 1 {
		t.Error("нет /icon-maskable-512.png 512x512 maskable")
	}

	if maskable != 1 {
		t.Errorf("маскируемых иконок %d, ожидается 1", maskable)
	}
}

// PNG иконки существуют и имеют заявленные размеры
func TestPNGIconSizes(t *testing.T) {
	cases := []struct {
		path   string
		width  int
		height int
	}{
		{"/icon-192.png", 192, 192},
		{"/icon-512.png", 512, 512},
		{"/icon-180.png", 180, 180},
		{"/icon-maskable-512.png", 512, 512},
	}

	for _, c := range cases {
		t.Run(strings.TrimPrefix(c.path, "/"), func(t *testing.T) {
			data, err := files.ReadFile("static" + c.path)
			if err != nil {
				t.Fatalf("не найден %s: %v", c.path, err)
			}

			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("не PNG или испорчен %s: %v", c.path, err)
			}

			if cfg.Width != c.width || cfg.Height != c.height {
				t.Errorf("%s: размер %dx%d, ожидается %dx%d",
					c.path, cfg.Width, cfg.Height, c.width, c.height)
			}
		})
	}
}

// apple-touch-icon ведёт на PNG, не на SVG
func TestIOSIconIsPNG(t *testing.T) {
	data, err := files.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("не прочитан index.html: %v", err)
	}
	html := string(data)

	if !strings.Contains(html, `<link rel="apple-touch-icon" href="/icon-180.png">`) {
		t.Fatal("apple-touch-icon не на /icon-180.png")
	}

	if !strings.Contains(html, `<link rel="icon" href="/icon.svg" type="image/svg+xml">`) {
		t.Fatal("иконка SVG не на месте")
	}

	metas := []string{
		`<meta name="apple-mobile-web-app-capable" content="yes">`,
		`<meta name="apple-mobile-web-app-status-bar-style" content="default">`,
		`<meta name="apple-mobile-web-app-title" content="Сёма">`,
	}
	for _, meta := range metas {
		if !strings.Contains(html, meta) {
			t.Errorf("не найден мета-тег: %s", meta)
		}
	}
}

// Иконки берёт браузер по адресам из манифеста, а не из встроенной
// файловой системы. Проверять надо ту дорогу, которой ходят: имя,
// разошедшееся с адресом в манифесте, дало бы 404 молча — на телефоне это
// просто пустой значок, и никто бы не понял почему.
func TestIconsServedAtManifestPaths(t *testing.T) {
	data, err := files.ReadFile("static/manifest.webmanifest")
	if err != nil {
		t.Fatalf("не прочитан манифест: %v", err)
	}
	var manifest struct {
		Icons []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("манифест не JSON: %v", err)
	}
	h := Handler()
	addrs := []string{"/manifest.webmanifest", "/icon-180.png"}
	for _, ic := range manifest.Icons {
		addrs = append(addrs, ic.Src)
	}
	for _, addr := range addrs {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", addr, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: код %d, ожидается 200", addr, w.Code)
			continue
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s: пустой ответ", addr)
		}
	}
}

// SVG-исходники отслеживаются по хэшу: если они поменялись, нужно
// пересгенерировать PNG.
func TestSVGHashes(t *testing.T) {
	// Хэши обновляются командой: make icons
	wantHashes := map[string]string{
		"icon.svg":          "70e373b2e19c67a71883ee5e51408c30f3cc3780442466d9c6a8c2f6f7ab06e2",
		"icon-maskable.svg": "9212a6a89bb584be27841d81c55bd5d9899247165e0a8076c31b7acafb102e0e",
		"icon-badge.svg":    "847c1e3d81b9ef14380b30cb84e41bd6ed46ef9ff427053dc085c5e8f93f140c",
	}

	for name, want := range wantHashes {
		data, err := files.ReadFile("static/" + name)
		if err != nil {
			t.Errorf("не прочитан %s: %v", name, err)
			continue
		}

		h := sha256.New()
		h.Write(data)
		got := hex.EncodeToString(h.Sum(nil))

		if got != want {
			t.Errorf("%s изменился: поменяли SVG?\n"+
				"Пересгенерируйте иконки: make icons\n"+
				"Обновите хэш в этом тесте: %s",
				name, got)
		}
	}
}
