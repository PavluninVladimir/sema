package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Кадр провода несёт сборку сервера: вкладка, открытая до выката, живёт
// на проводе и htmx-запросов может не делать вовсе — тогда расхождение
// клиента и сервера замечать было нечем, и кнопки новой разметки не
// работали молча (13 сентября 2026).
func TestWireFrameCarriesBuild(t *testing.T) {
	b, err := json.Marshal(liveFrame{Screens: []string{"inbox"}, Build: web.Build()})
	if err != nil {
		t.Fatal(err)
	}
	var кадр map[string]any
	if err := json.Unmarshal(b, &кадр); err != nil {
		t.Fatal(err)
	}
	if кадр["сборка"] != web.Build() {
		t.Errorf("в кадре нет сборки сервера: %s", b)
	}
	if _, ok := кадр["экраны"]; !ok {
		t.Errorf("в кадре нет экранов: %s", b)
	}
	// и клиент её проверяет тем же путём, что заголовок ответа
	src, err := os.ReadFile(filepath.Join("..", "web", "static", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)
	if !strings.Contains(js, "res.сборка") || !strings.Contains(js, "checkBuild(") {
		t.Error("клиент не сверяет сборку из кадра провода")
	}
}
