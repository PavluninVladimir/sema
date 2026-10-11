package web

import (
	"strings"
	"testing"
)

// На телефоне Сёма — приложение, а не страница: масштаб запрещён тремя
// слоями, и каждый закрывает своё. viewport — приближение у полей ввода
// мельче 16px, touch-action — двойное касание, gesturestart — щипок в
// Safari на iOS, который user-scalable=no ради доступности не слушает.
// Снимешь один — вернётся ровно его жест (4 октября 2026, просьба владельца).
func TestPhoneZoomIsOff(t *testing.T) {
	read := func(name string) string {
		data, err := files.ReadFile("static/" + name)
		if err != nil {
			t.Fatalf("не прочитан %s: %v", name, err)
		}
		return string(data)
	}
	page := read("index.html")
	for _, want := range []string{"maximum-scale=1", "user-scalable=no"} {
		if !strings.Contains(page, want) {
			t.Errorf("в viewport нет %s: приближение у полей ввода вернётся", want)
		}
	}
	if !strings.Contains(read("style.css"), "touch-action: manipulation") {
		t.Error("нет touch-action: manipulation: двойное касание снова приближает")
	}
	if !strings.Contains(read("app.js"), "'gesturestart'") {
		t.Error("щипок в Safari на iOS не погашен")
	}
}
