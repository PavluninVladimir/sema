package web

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Значок пуша (`badge`) — монохромный: Android берёт из картинки только
// прозрачность и закрашивает её цветом системы. Цветной квадрат без
// прозрачности превратился бы в сплошное белое пятно в строке состояния —
// именно так выглядит «значок не тот» у пушей без настоящего badge.
func TestPushBadgeIsMonochromeSilhouette(t *testing.T) {
	data, err := files.ReadFile("static/icon-badge.png")
	if err != nil {
		t.Fatalf("значка пуша нет: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("значок пуша не PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 96 || b.Dy() != 96 {
		t.Errorf("значок %dx%d, Android просит около 96x96", b.Dx(), b.Dy())
	}
	var empty, solid, colored int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			switch {
			case a == 0:
				empty++
			case a == 0xffff:
				solid++
			}
			// у непрозрачных точек цвет один и тот же — белый: остальное система красит сама
			if a > 0 && (r != a || g != a || bl != a) {
				colored++
			}
		}
	}
	if empty < 96*96/4 || solid < 96*96/20 {
		t.Errorf("не силуэт: прозрачных точек %d, сплошных %d из %d", empty, solid, 96*96)
	}
	if colored > 0 {
		t.Errorf("%d точек не белые: значок должен быть монохромным", colored)
	}
	// и отдаётся без входа вместе с остальными значками: просит его система
	if !IsShell("/icon-badge.png") {
		t.Error("/icon-badge.png не открыт без входа")
	}
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest("GET", "/icon-badge.png", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("/icon-badge.png: код %d, тип %q", w.Code, w.Header().Get("Content-Type"))
	}
}
