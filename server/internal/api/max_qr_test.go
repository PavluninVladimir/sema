package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMAXQRRequiresOwner(t *testing.T) {
	s, _ := testServer(t)
	row, pending := s.maxQRRow()
	if pending || len(row.Actions) != 1 || row.Actions[0].Action != "max_qr_begin" {
		t.Fatalf("неверное начальное состояние входа: %+v", row.Actions)
	}
	req := httptest.NewRequest(http.MethodGet, maxQRPath, nil)
	req.RemoteAddr = "203.0.113.12:12345"
	w := httptest.NewRecorder()
	s.handleMAXQR(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("QR доступен без разрешения владельца: %d", w.Code)
	}
	w = httptest.NewRecorder()
	if s.allowScreen(w, req, maxScreen) || w.Code != http.StatusForbidden {
		t.Fatalf("экран MAX доступен без разрешения владельца: %d", w.Code)
	}
}
