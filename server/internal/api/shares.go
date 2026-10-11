package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ссылки на файлы на экранах и сама отдача по ссылке (file-share-links).

// shareInline — что по ссылке показывается на месте: только растровые
// картинки. SVG — тоже картинка, но со скриптами: на скачивание.
func shareInline(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	}
	return false
}

// handleShare — `GET /share/<токен>`: без входа, по подписи. Любая неудача —
// одно и то же «нет такой ссылки», без подробностей.
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	a, row, err := s.eng.OpenShare(r.PathValue("token"))
	if err != nil {
		http.Error(w, "Ссылка не действует", http.StatusNotFound)
		return
	}
	data, err := s.eng.FileBytes(a)
	if err != nil {
		http.Error(w, "Ссылка не действует", http.StatusNotFound)
		return
	}
	mime := a.MIME
	if mime == "" || mime == "application/octet-stream" {
		mime = engine.GuessMIME(a.Filename)
	}
	name := path.Base(strings.ReplaceAll(a.Filename, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = "файл"
	}
	disposition := "attachment"
	if shareInline(mime) {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'")
	w.Header().Set("Content-Disposition", disposition+`; filename*=UTF-8''`+urlEscape(name))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	log.Printf("ссылка %d: файл #%d открыт", row.ID, a.ID)
	_, _ = w.Write(data)
}

// shareState — состояние ссылки словами и уровнем.
func shareState(f *store.FileShare) (string, string) {
	switch {
	case f.Revoked != "":
		return "отозвана", ""
	case !f.Active(time.Now()):
		return "срок вышел", ""
	}
	return "действует", LevelOK
}

func shareUntil(f *store.FileShare) string {
	if t, err := time.Parse(time.RFC3339, f.Expires); err == nil {
		return t.Local().Format("02.01.2006")
	}
	return f.Expires
}

// shareCardComponent — карточка выданной ссылки в переписке: по базе.
func (s *Server) shareCardComponent(card engine.ToolCard, at string) Component {
	f, ok := s.eng.Store().FileShare(card.Share.ID)
	if !ok {
		return Component{Type: "toolcard", Icon: "link", Title: "Ссылка на файл", Meta: at, Caption: "Ссылки больше нет."}
	}
	state, level := shareState(f)
	c := Component{Type: "toolcard", Icon: "link", Title: "Ссылка на «" + card.Share.Name + "»",
		Meta:    "до " + shareUntil(f) + " · " + at,
		Text:    s.eng.ShareURL(f),
		Badges:  []Badge{{Text: state, Level: level}},
		Caption: fmt.Sprintf("Открывается без входа в Сёму. Открыта %d раз.", f.Opens)}
	if level != "" {
		withLevel(&c, level)
		id := strconv.FormatInt(f.ID, 10)
		c.Actions = []Action{
			{Action: "$copy", Target: s.eng.ShareURL(f), Label: "Скопировать"},
			{Action: "share_revoke", Target: id, Label: "Отозвать…", Style: "destructive",
				Confirm: "Отозвать ссылку на «" + card.Share.Name + "»? Кто откроет её после этого, увидит «Ссылка не действует».", ConfirmOK: "Отозвать"},
		}
	}
	return c
}

// addShares — ссылки в строке файла: факт со сроком и открытиями, в меню —
// «Скопировать ссылку», «Отозвать ссылки» и «Выдать ссылку».
func (s *Server) addShares(row *Component, a *store.Attachment, shares []store.FileShare) {
	id := strconv.FormatInt(a.ID, 10)
	if len(shares) > 0 {
		last := shares[0]
		opens := 0
		for _, f := range shares {
			opens += f.Opens
		}
		value := "до " + shareUntil(&last) + fmt.Sprintf(" · открыта %d раз", opens)
		if len(shares) > 1 {
			value = fmt.Sprintf("%d действуют · последняя ", len(shares)) + value
		}
		row.Facts = append(row.Facts, Prop{Label: "Ссылка", Value: value})
		row.Actions = append(row.Actions,
			Action{Action: "$copy", Target: s.eng.ShareURL(&last), Label: "Скопировать ссылку"},
			Action{Action: "share_revoke_file", Target: id, Label: "Отозвать ссылки…", Style: "destructive",
				Confirm: "Отозвать все ссылки на «" + a.Filename + "»? Кто откроет их после этого, увидит «Ссылка не действует».", ConfirmOK: "Отозвать"})
	}
	row.Actions = append(row.Actions, Action{Action: "share_file", Target: id, Label: "Выдать ссылку"})
}

// sharesActions — «Выдать ссылку», «Отозвать», «Отозвать ссылки».
func (s *Server) sharesActions(req actionRequest) (string, error) {
	switch req.Action {
	case "share_file":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		_, err = s.eng.ShareFromScreen(context.Background(), id)
		return "", err
	case "share_revoke":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.RevokeShare(id)
	case "save_share_days":
		return "", s.saveSettings(req, settingParam{"share_days", "share.days", "срок ссылки на файл"})
	case "share_revoke_file":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.Store().RevokeFileShares(id)
	}
	return "", errUnhandled
}
