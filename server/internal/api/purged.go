package api

import (
	"fmt"
	"strconv"
	"strings"
)

// Экран «Стёрто с сервера»: письма, которые Сёма убрал с почтового
// сервера по сроку хранения.
//
// «Что именно стёрли» — вопрос про письма, а не про настройки: кнопка из
// сводки вела на «Папки и хранение», то есть в настройки, и человек
// искал там список (13 сентября 2026). Список живёт своим экраном рядом
// с «Почтой», отбирается по правилу и ищется по теме и отправителю.
//
// Из архива Сёмы письма не удаляются никогда — поэтому каждую строку
// можно открыть: письмо на месте, нет его только на почтовом сервере.

const purgedScreen = "purged"

func (s *Server) buildPurged(doc *Document, filter, search string, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Стёрто с сервера"
	doc.Layout = "wide"
	doc.Searchable = true
	if limit <= 0 {
		limit = 50
	}
	правило := ""
	switch filter {
	case "спам", "срок хранения":
		правило = filter
	}
	doc.Filters = []Filter{
		{ID: "", Title: "Все"},
		{ID: "спам", Title: "Спам"},
		{ID: "срок хранения", Title: "По сроку"},
	}
	doc.ActiveFilter = filter
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	заСутки, заНеделю := st.DeletionsSince(1), st.DeletionsSince(7)
	doc.Subheading = fmt.Sprintf("за сутки %d · за неделю %d · из архива Сёмы письма не удаляются никогда",
		заСутки, заНеделю)

	список, total, err := st.Deletions(правило, search, limit)
	if err != nil {
		return err
	}
	if len(список) == 0 {
		empty := "здесь появятся письма, которые Сёма убрал с почтового сервера: спам по своему сроку, обычные письма — только если вы это разрешили"
		if strings.TrimSpace(search) != "" {
			empty = "по «" + search + "» ничего не нашлось — ищется по теме, отправителю, ящику и папке"
		}
		add(Component{Type: "section", Title: "Ничего не стёрто", Children: []Component{
			emptyState("Список пуст", empty, "trash")}})
		return nil
	}
	rows := make([]Component, 0, len(список)+1)
	for _, у := range список {
		тема := у.Subject
		if тема == "" {
			тема = "письмо №" + strconv.FormatInt(у.MessageID, 10)
		}
		строка := Component{Type: "row", Layout: "wide",
			ID:       "стёрто-" + strconv.FormatInt(у.ID, 10),
			Subtitle: тема,
			Caption:  у.From,
			Facts: []Prop{
				{Label: "Когда", Value: у.At.Format("02.01 15:04")},
				{Label: "Правило", Value: у.Policy},
			}}
		if у.Account != "" {
			строка.Facts = append(строка.Facts, Prop{Label: "Ящик", Value: у.Account})
		}
		if у.Folder != "" {
			строка.Facts = append(строка.Facts, Prop{Label: "Папка", Value: у.Folder})
		}
		if у.MessageID > 0 {
			строка.Actions = []Action{{Action: "$goto",
				Target: "msg:" + strconv.FormatInt(у.MessageID, 10), Label: "Открыть", Style: "primary"}}
		}
		rows = append(rows, строка)
	}
	// длинный список — порциями: «показать ещё» просит тот же экран с
	// большим пределом
	if total > len(список) {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "стёрто-ещё",
			Subtitle: fmt.Sprintf("Ещё %d", total-len(список)),
			Actions:  []Action{{Action: "$more", Target: strconv.Itoa(limit * 2), Label: "Показать ещё"}}})
	}
	title := fmt.Sprintf("Письма · %d", total)
	if правило != "" {
		title = fmt.Sprintf("Письма по правилу «%s» · %d", правило, total)
	}
	add(Component{Type: "section", ID: "стёртые-письма", Title: title, Children: rows})
	add(Component{Type: "text", Style: "secondary",
		Text: "Стирается только копия на почтовом сервере. У Сёмы письмо остаётся навсегда: его видно в поиске, " +
			"оно идёт в контекст моделям и в сводки — поэтому каждую строку можно открыть."})
	return nil
}
