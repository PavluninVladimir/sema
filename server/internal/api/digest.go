package api

// Дайджест «Почты»: сводка писем по группам справочника за окно, и экран
// правки самих групп. Ничего из примет не зашито в код.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func fmtMoney(amount float64, cur string) string {
	if cur == "" || cur == "RUB" {
		cur = "₽"
	}
	whole := strconv.FormatInt(int64(amount), 10)
	// разряды пробелом: 6 321 ₽
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	frac := amount - float64(int64(amount))
	if frac >= 0.005 {
		return fmt.Sprintf("%s,%02d %s", b.String(), int(frac*100+0.5), cur)
	}
	return b.String() + " " + cur
}

func sumsText(sum map[string]float64) string {
	var parts []string
	for cur, v := range sum {
		parts = append(parts, fmtMoney(v, cur))
	}
	return strings.Join(parts, " + ")
}

// digestSections — секции сводки: у свёрнутой группы одна строка со
// счётчиком по отправителям, у остальных — письма с тем, что Сёма понял.
func (s *Server) digestSections(d *engine.Digest, account string) []Component {
	var out []Component
	for _, sec := range d.Sections {
		if len(sec.Items) == 0 {
			continue
		}
		title := fmt.Sprintf("%s · %d", sec.Group.Title, len(sec.Items))
		if t := sumsText(sec.Sum); t != "" {
			title += " · " + t
		}
		// свёртка бережёт место, а короткую группу беречь не от чего:
		// «Счета и чеки · 1 · свёрнуто» со строкой «Ozon 1» вместо самого
		// письма — это потеря. Порог — настройка: у одного человека
		// «Уведомления» это три письма в день, у другого сорок
		// Свёртка — жест, а не кнопка в строке: раздел раскрывается
		// нажатием на свой заголовок, как все свёртки клиента. Прежде у
		// свёрнутой группы стояла строка-заглушка с кнопкой
		// «Развернуть», у развёрнутой — кнопка «Свернуть» в заголовке, и
		// в заголовке же объяснялось, почему она так называется: три
		// разных вида у одного и того же (13 сентября 2026)
		свёрнута := sec.Group.Collapsed && len(sec.Items) >= s.eng.DigestCollapseFrom()
		rows := make([]Component, 0, len(sec.Items)+1)
		if свёрнута {
			// кто в группе — одной строкой: по ней видно, стоит ли
			// раскрывать
			var parts []string
			for i, sc := range sec.Senders {
				if i == 6 {
					parts = append(parts, "…")
					break
				}
				parts = append(parts, fmt.Sprintf("%s %d", sc.Name, sc.Count))
			}
			title += " · " + strings.Join(parts, " · ")
		}
		for _, it := range sec.Items {
			rows = append(rows, digestRow(it, account))
		}
		out = append(out, Component{
			Type: "section", ID: "digest-" + sec.Group.Name, Title: title, Children: rows,
			Collapsed: свёрнута,
		})
	}
	return out
}

// digestRow — строка письма в сводке: тема, отправитель, что понято.
func digestRow(it engine.DigestItem, insideAccount string) Component {
	m := it.Msg
	row := msgRowIn(m, nil, insideAccount)
	row.Badge, row.BadgeColor = "", ""
	row.Caption = ""
	var caption []string
	var badges []Badge
	if r := it.Receipt; r != nil {
		if r.Vendor != "" {
			row.Title = r.Vendor + " — " + m.Subject
		}
		if r.Amount > 0 {
			caption = append(caption, fmtMoney(r.Amount, r.Currency))
		}
		if r.Summary != "" {
			caption = append(caption, r.Summary)
		}
		kind := "чек"
		if strings.Contains(strings.ToLower(m.Subject+" "+r.Summary), "сч") {
			kind = "счёт"
		}
		badges = append(badges, Badge{Text: kind, Color: "#1E7A32"})
	}
	for i, f := range it.Files {
		if i == 3 {
			caption = append(caption, fmt.Sprintf("ещё %d", len(it.Files)-3))
			break
		}
		caption = append(caption, f.Filename)
		if i == 0 {
			ext := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(f.Filename[strings.LastIndex(f.Filename, ".")+1:])), ".")
			if len(ext) <= 5 && len(ext) > 0 && strings.Contains(f.Filename, ".") {
				badges = append(badges, Badge{Text: ext, Color: "#0058B0"})
			}
			row.Actions = append(row.Actions, Action{Action: "$open", Target: f.Path, Label: "Файл"})
		}
	}
	if len(caption) == 0 && m.Snippet != "" {
		caption = append(caption, clipRunes(strings.TrimSpace(m.Snippet), 120))
	}
	row.Caption = strings.Join(caption, " · ")
	row.Badges = badges
	for i := range row.Actions {
		if row.Actions[i].Action == "$goto" {
			row.Actions[i].Label = "Открыть письмо"
		}
	}
	return row
}

// buildDigestGroups — справочник групп: приметы правятся здесь.
func (s *Server) buildDigestGroups(doc *Document) error {
	doc.Heading = "Группы дайджеста"
	doc.Subheading = "По ним письма на «Почте» раскладываются по смыслу"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	groups, err := s.eng.Store().DigestGroups()
	if err != nil {
		return err
	}
	rows := make([]Component, 0, len(groups))
	for _, g := range groups {
		id := strconv.FormatInt(g.ID, 10)
		var marks []string
		if g.Rules.Receipt {
			marks = append(marks, "есть чек или счёт")
		}
		if len(g.Rules.Words) > 0 {
			marks = append(marks, "в теме: "+strings.Join(g.Rules.Words, ", "))
		}
		if len(g.Rules.Senders) > 0 {
			marks = append(marks, "отправитель: "+strings.Join(g.Rules.Senders, ", "))
		}
		if len(g.Rules.Attachments) > 0 {
			marks = append(marks, "вложения: "+strings.Join(g.Rules.Attachments, ", "))
		}
		if g.Rules.Robots {
			marks = append(marks, "роботы и рассылки")
		}
		if g.Rules.People {
			marks = append(marks, "всё остальное не от роботов")
		}
		var badges []Badge
		if g.Collapsed {
			badges = append(badges, Badge{Text: "свёрнуто", Color: "#8E8E93"})
		}
		badges = append(badges, Badge{Text: g.Name, Color: "#8E8E93"})
		collapse := "Сворачивать"
		if g.Collapsed {
			collapse = "Разворачивать"
		}
		rows = append(rows, Component{
			Type: "row", Layout: "wide", ID: "dg-" + id,
			Subtitle: g.Title,
			Caption:  strings.Join(marks, " · "),
			Badges:   badges,
			Actions: []Action{
				{Action: "digest_words", Target: id, Label: "Слова в теме", Prompt: "Слова в теме через запятую", Value: strings.Join(g.Rules.Words, ", ")},
				{Action: "digest_senders", Target: id, Label: "Отправители", Prompt: "Части адреса отправителя через запятую", Value: strings.Join(g.Rules.Senders, ", ")},
				{Action: "digest_attachments", Target: id, Label: "Вложения", Prompt: "Расширения вложений через запятую", Value: strings.Join(g.Rules.Attachments, ", ")},
				{Action: "digest_toggle", Target: g.Name, Label: collapse},
				{Action: "delete_digest_group", Target: id, Label: "Удалить", Style: "destructive",
					Confirm: "Удалить группу «" + g.Title + "»? Письма никуда не денутся — только перестанут собираться в неё.", ConfirmOK: "Удалить"},
			},
		})
	}
	add(Component{Type: "section", Title: "Группы · письмо идёт в первую подошедшую сверху", Children: rows})
	add(Component{
		Type: "form", ID: "adddigestgroup", Title: "Новая группа",
		Fields: []Field{
			{Type: "text", Name: "title", Label: "Название"},
			{Type: "text", Name: "words", Label: "Слова в теме через запятую"},
			{Type: "text", Name: "senders", Label: "Части адреса отправителя через запятую", Hint: "например, github.com или billing@"},
			{Type: "text", Name: "attachments", Label: "Расширения вложений через запятую", Hint: "pdf, xlsx"},
			{Type: "toggle", Name: "collapsed", Label: "Показывать свёрнуто", Value: "false"},
		},
		Submit: &Action{Action: "add_digest_group", Label: "Добавить", Style: "primary"},
	})
	add(Component{Type: "text", Style: "secondary",
		Text: "Первичный набор — только заполнение: любую группу можно переименовать приметами, свернуть или удалить. " +
			"Модуль «Требует внимания» ссылается на группы по устойчивому имени в капсуле."})
	return nil
}

// digestGroupAction — правки справочника групп.
func (s *Server) digestGroupAction(req actionRequest) (string, error) {
	st := s.eng.Store()
	find := func(target string) (*store.DigestGroup, error) {
		id, err := strconv.ParseInt(target, 10, 64)
		if err != nil {
			return nil, err
		}
		groups, err := st.DigestGroups()
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if g.ID == id {
				return g, nil
			}
		}
		return nil, fmt.Errorf("группа не найдена")
	}
	switch req.Action {
	case "digest_window":
		return "", s.eng.SetDigestWindow(req.Target)
	case "digest_toggle":
		groups, err := st.DigestGroups()
		if err != nil {
			return "", err
		}
		for _, g := range groups {
			if g.Name == req.Target {
				g.Collapsed = !g.Collapsed
				return "", st.SaveDigestGroup(g)
			}
		}
		return "", fmt.Errorf("группа %q не найдена", req.Target)
	case "digest_words", "digest_senders", "digest_attachments":
		g, err := find(req.Target)
		if err != nil {
			return "", err
		}
		words := splitWords(req.Params["value"])
		switch req.Action {
		case "digest_words":
			g.Rules.Words = words
		case "digest_senders":
			g.Rules.Senders = words
		default:
			g.Rules.Attachments = words
		}
		return "digest:groups", st.SaveDigestGroup(g)
	case "add_digest_group":
		title := strings.TrimSpace(req.Params["title"])
		if title == "" {
			return "", fmt.Errorf("нужно название группы")
		}
		return "digest:groups", st.SaveDigestGroup(&store.DigestGroup{
			Title: title, Sort: 50, Collapsed: req.Params["collapsed"] == "true",
			Rules: store.DigestRules{
				Words: splitWords(req.Params["words"]), Senders: splitWords(req.Params["senders"]),
				Attachments: splitWords(req.Params["attachments"]),
			},
		})
	case "delete_digest_group":
		g, err := find(req.Target)
		if err != nil {
			return "", err
		}
		return "digest:groups", st.DeleteDigestGroup(g.ID)
	}
	return "", errUnhandled
}
