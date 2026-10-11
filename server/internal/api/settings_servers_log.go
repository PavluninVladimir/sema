package api

// Журнал команд на серверах: «Что делалось» у раздела и «Журнал» у сервера —
// одна и та же лента, у сервера отобранная по нему. Это хроника, а не
// настройка: на первом экране раздела её нет, она приезжает только с
// вкладкой, первой страницей в двадцать строк и кнопкой «Показать ещё».
// Чипы отбирают: выполнены, отказы, Сёма сам, ночная смена.

import (
	"fmt"
	"strconv"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// serversLogPage — сколько строк журнала приезжает первой страницей.
const serversLogPage = 20

// serversLogFilters — чипы журнала по порядку: слово отбора и подпись.
var serversLogFilters = []struct{ ID, Title string }{
	{store.SSHLogAll, "Все"},
	{store.SSHLogDone, "Выполнены"},
	{store.SSHLogDenied, "Отказы"},
	{store.SSHLogSelf, "Сёма сам"},
	{store.SSHLogNight, "Ночная смена"},
}

// serversJournal — журнал команд сервера (пустое имя — всех серверов): чипы
// отбора с числами, первая страница и «Показать ещё». Добавляется к
// документу как есть.
func (s *Server) serversJournal(doc *Document, host, filter string, limit int) error {
	st := s.eng.Store()
	// у журнала есть полоса чипов, но нет своего поиска по строкам: верхнее поле
	// «ищи по настройкам» на телефоне ему не нужно
	doc.noPhoneSearch = true
	known := false
	for _, f := range serversLogFilters {
		doc.Filters = append(doc.Filters, Filter{ID: f.ID,
			Title: fmt.Sprintf("%s · %d", f.Title, st.CountSSHLog(host, f.ID))})
		if f.ID == filter {
			known = true
		}
	}
	if !known {
		filter = store.SSHLogAll
	}
	doc.ActiveFilter = filter

	if limit <= 0 || limit == defaultRows {
		limit = serversLogPage
	}
	entries, err := st.SSHLogFiltered(host, filter, limit+1)
	if err != nil {
		return err
	}
	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	rows := make([]Component, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, sshLogRow(e, host == ""))
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Команд ещё не было",
			"Сюда попадает каждая команда: кто просил, сколько заняла, что ответил сервер и по какому правилу прошла. Вывод проходит маски секретов до записи", "list.bullet"))
		if filter != store.SSHLogAll {
			rows[0] = emptyState("Под этот отбор ничего не подошло",
				"Выберите «Все» — там вся хроника этого журнала", "list.bullet")
		}
	}
	title := "Что делалось на серверах"
	if host != "" {
		title = "Что делалось на «" + host + "»"
	}
	doc.Components = append(doc.Components, Component{Type: "section", Title: title, Children: rows})
	if more {
		total := st.CountSSHLog(host, filter)
		doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(rows), total)}}})
	}
	return nil
}

// sshLogRow — строка журнала. Имя сервера в подписи — только у общего
// журнала: у журнала самого сервера оно лишнее.
func sshLogRow(e store.SSHLogEntry, withHost bool) Component {
	badge := map[string]Badge{
		store.SSHExecuted: {Text: "выполнена", Level: LevelOK},
		store.SSHAuto:     {Text: "выполнена · сам", Level: LevelOK},
		store.SSHWaiting:  {Text: "ждала", Level: LevelWarn},
		store.SSHDenied:   {Text: "отказ", Level: LevelAlert},
		store.SSHRejected: {Text: "отклонена"},
		store.SSHRuled:    {Text: "правило", Level: LevelInfo},
		store.SSHError:    {Text: "ошибка", Level: LevelAlert},
	}[e.Decision]
	caption := e.Who + " · " + whenText(e.At)
	if withHost {
		caption = e.Host + " · " + caption
	}
	switch e.Decision {
	case store.SSHExecuted, store.SSHAuto:
		caption += fmt.Sprintf(" · код %d · %.1f с · %d символов", e.Code, float64(e.Millis)/1000, e.Size)
	default:
		if e.Output != "" {
			caption += " · " + clipRunes(e.Output, 100)
		}
	}
	row := Component{Type: "row", Layout: "wide", ID: "ssh-log-" + strconv.FormatInt(e.ID, 10), Subtitle: e.Cmd, Caption: caption}
	if badge.Text != "" {
		row.Badges = []Badge{badge}
	}
	if (e.Decision == store.SSHExecuted || e.Decision == store.SSHAuto) && e.Output != "" {
		row.Detail, row.Expandable = e.Output, true
	}
	return row
}
