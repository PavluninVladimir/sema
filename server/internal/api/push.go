package api

// Подписка браузера на пуши. Ключ отдаётся только вошедшему — он не
// секрет, но и раздавать его всем подряд незачем; подписка и отписка
// пишутся от имени сессии.
//
// Маршрутов здесь ровно три, и новых заводить нельзя: шлюз на VPS пускает
// наружу только перечисленные пути. Поэтому «что сервер знает про это
// устройство» спрашивается тем же GET /api/push/key — с адресом подписки
// параметром.

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	pub, err := s.eng.PushPublicKey()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	out := map[string]any{"key": pub, "tls": s.eng.TLSOn()}
	// страница с готовой подпиской спрашивает, помнит ли её сервер и под тем
	// ли ключом она сделана: забыть могли (служба ответила 410, база
	// восстановлена из копии), а ключ сменить — при переносе без SEMA_KEY
	if endpoint := r.URL.Query().Get("endpoint"); endpoint != "" {
		known, id, name, sameKey := s.eng.PushDeviceStatus(endpoint)
		out["known"], out["id"], out["name"], out["same_key"] = known, id, name, sameKey
	}
	writeJSON(w, out)
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		// окно открыто как установленное приложение («Домой» на iPhone) и
		// сколько касаний понимает экран: по ним выводится имя устройства
		Standalone bool `json:"standalone"`
		Touch      int  `json:"touch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		http.Error(w, "подписка не разобрана: "+err.Error(), http.StatusBadRequest)
		return
	}
	// строку браузера берём из заголовка, а не от страницы: страница могла
	// бы назвать себя как угодно, а заголовок ставит браузер
	sub, err := s.eng.PushSubscribe(in.Endpoint, in.Keys.P256dh, in.Keys.Auth, r.UserAgent(), in.Standalone, in.Touch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": sub.ID, "name": sub.Agent})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.eng.PushUnsubscribe(in.Endpoint); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
