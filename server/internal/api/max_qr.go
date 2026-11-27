package api

import (
	"net/http"
	"strconv"
)

const maxQRPath = "/api/max/qr"

func (s *Server) handleMAXQR(w http.ResponseWriter, r *http.Request) {
	if !s.mayAdmin(r) {
		http.Error(w, "QR MAX доступен только хозяину", http.StatusForbidden)
		return
	}
	image, until := s.eng.MAXQR()
	if len(image) == 0 || r.URL.Query().Get("t") != strconv.FormatInt(until.UnixNano(), 10) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(image)
}

func (s *Server) maxQRRow() (Component, bool) {
	row := Component{Type: "row", ID: "max-qr", Subtitle: "Подтвердите вход в приложении MAX",
		Actions: []Action{{Action: "max_qr_begin", Label: "Показать QR-код", Style: "primary"}}}
	if image, until := s.eng.MAXQR(); len(image) > 0 {
		row.Subtitle = "Отсканируйте QR-код в приложении MAX и подтвердите вход"
		row.Caption = "Сёма автоматически проверит подтверждение"
		row.Image = maxQRPath + "?t=" + strconv.FormatInt(until.UnixNano(), 10)
		row.inlinePNG = image
		row.Expand, row.Expanded = "QR-код", true
		row.Actions = []Action{
			{Action: "max_qr_poll", Label: "Проверить вход", Style: "primary"},
			{Action: "max_qr_begin", Label: "Получить новый QR"},
			{Action: "max_qr_cancel", Label: "Отменить"},
		}
		return row, true
	}
	return row, false
}
