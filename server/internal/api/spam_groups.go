package api

// Разбор спама по отправителям.
//
// 10 сентября 2026 на живой Сёме в этом списке лежало 89 писем, и сорок
// одно из них — одно и то же письмо от Ozon: та же тема, та же уверенность,
// то же «Почему» почти теми же словами. Человек листал сорок одинаковых
// строк и нажимал «Не спам» сорок раз.
//
// Список стал списком отправителей. Строка отвечает на три вопроса сразу:
// кто, сколько и почему — а решение принимается на всех письма отправителя
// разом. Отдельное письмо никуда не делось: «Показать письма» раскрывает
// группу, и вернуть можно по одному.
//
// Порог группировки — настройка: отправителю с одним письмом группа не
// нужна, там сразу строка письма.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// spamSender — все письма одного отправителя, ждущие решения.
type spamSender struct {
	Addr      string
	Name      string
	Messages  []*store.Message
	Subjects  []string // разные темы, самая частая первой
	Confident float64  // худшая уверенность: она и красит строку
	From      time.Time
	Till      time.Time
	Reason    string // «Почему» — одно на отправителя
}

// bySender — разложить письма по отправителям. Порядок: сначала те,
// у кого писем больше — там и выигрыш от одного нажатия больше всего.
func bySender(pending []*store.Message) []*spamSender {
	byAddr := map[string]*spamSender{}
	var порядок []string
	for _, m := range pending {
		addr := strings.ToLower(strings.TrimSpace(m.FromAddr))
		г, has := byAddr[addr]
		if !has {
			г = &spamSender{Addr: addr, Name: m.FromName, From: m.Date, Till: m.Date}
			byAddr[addr] = г
			порядок = append(порядок, addr)
		}
		if г.Name == "" {
			г.Name = m.FromName
		}
		г.Messages = append(г.Messages, m)
		if m.Confidence > г.Confident {
			г.Confident = m.Confidence
		}
		if !m.Date.IsZero() {
			if г.From.IsZero() || m.Date.Before(г.From) {
				г.From = m.Date
			}
			if m.Date.After(г.Till) {
				г.Till = m.Date
			}
		}
	}
	out := make([]*spamSender, 0, len(byAddr))
	for _, addr := range порядок {
		г := byAddr[addr]
		г.Subjects = senderSubjects(г.Messages)
		г.Reason = senderReason(г.Messages)
		out = append(out, г)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Messages) > len(out[j].Messages) })
	return out
}

// senderSubjects — разные темы, частая первой. Сорок писем «Ваш чек +
// (1) подарок» — это одна тема, а не сорок.
func senderSubjects(письма []*store.Message) []string {
	счёт := map[string]int{}
	var порядок []string
	for _, m := range письма {
		т := strings.TrimSpace(m.Subject)
		if т == "" {
			т = "без темы"
		}
		if счёт[т] == 0 {
			порядок = append(порядок, т)
		}
		счёт[т]++
	}
	sort.SliceStable(порядок, func(i, j int) bool { return счёт[порядок[i]] > счёт[порядок[j]] })
	return порядок
}

// senderReason — «Почему» одно на группу. Берём самую частую
// формулировку: у сорока писем она отличается перестановкой слов, и
// показывать все сорок незачем.
func senderReason(письма []*store.Message) string {
	счёт := map[string]int{}
	лучшая, лучшийСчёт := "", 0
	for _, m := range письма {
		п := strings.TrimSpace(m.Reason)
		if п == "" {
			continue
		}
		счёт[п]++
		if счёт[п] > лучшийСчёт {
			лучшая, лучшийСчёт = п, счёт[п]
		}
	}
	return лучшая
}

// subjectsLine — «„Ваш чек + (1) подарок“ и ещё 2 темы».
func subjectsLine(темы []string) string {
	if len(темы) == 0 {
		return ""
	}
	s := "«" + clipRunes(темы[0], 70) + "»"
	if len(темы) > 1 {
		s += fmt.Sprintf(" и ещё %s", plural(len(темы)-1, "тема", "темы", "тем"))
	}
	return s
}

// dateSpread — «с 7 июля по 26 августа». Один день — просто дата: «с 7
// июля по 7 июля» человек читает как ошибку.
func dateSpread(раньше, позже time.Time) string {
	if раньше.IsZero() || позже.IsZero() {
		return ""
	}
	// месяцы по-русски стандартная библиотека не умеет: «Format("2
	// января")» печатает слово «января» буквально в любом месяце
	if раньше.Format("2006-01-02") == позже.Format("2006-01-02") {
		return ruDateShort(позже)
	}
	return "с " + ruDateShort(раньше) + " по " + ruDateShort(позже)
}

// SpamGroupFrom — со скольких писем отправителя показывать группой.
// Одному письму группа не нужна: там сразу строка письма.
func (s *Server) groupThreshold() int {
	v, err := strconv.Atoi(s.eng.Store().Setting("mail.spam_group_from", "2"))
	if err != nil || v < 2 {
		return 2
	}
	return v
}

// spamBySender — раздел «Похоже на спам» списком отправителей.
// `limit` считается в отправителях, а не в письмах: показать «50 из 89»
// писем нельзя, когда строка — это отправитель.
func (s *Server) spamBySender(pending []*store.Message, limit int) []Component {
	группы := bySender(pending)
	всегоПисем := len(pending)
	// предел приходит в строках писем (50 по умолчанию, «ещё» его
	// удваивает), а строка здесь — отправитель: их вчетверо меньше
	limit = limit / 4
	if limit < 8 {
		limit = 8
	}
	показано := группы
	скрытоГрупп, скрытоПисем := 0, 0
	if len(группы) > limit {
		показано = группы[:limit]
		for _, г := range группы[limit:] {
			скрытоГрупп++
			скрытоПисем += len(г.Messages)
		}
	}

	rows := make([]Component, 0, len(показано)+1)
	for _, г := range показано {
		rows = append(rows, spamSenderRow(г, s.groupThreshold()))
	}
	// кнопка ВНУТРИ раздела и говорит, сколько добавится. «Показать ещё
	// (50 из 89)» стояло снаружи свёрнутого списка и обещало пятьдесят
	// там, где показано ноль
	if скрытоГрупп > 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide",
			Subtitle: fmt.Sprintf("Ещё %s · %s",
				plural(скрытоГрупп, "отправитель", "отправителя", "отправителей"),
				plural(скрытоПисем, "письмо", "письма", "писем")),
			Actions: []Action{{Action: "$more", Label: "Показать"}}})
	}
	title := fmt.Sprintf("Похоже на спам — подтвердите · %s · %s",
		plural(всегоПисем, "письмо", "письма", "писем"),
		plural(len(группы), "отправитель", "отправителя", "отправителей"))
	return []Component{{
		Type: "section", ID: "spam-groups", Title: title,
		// $selected клиент заменяет на id отмеченных строк
		Actions: []Action{
			{Action: "not_spam", Target: "$selected", Label: "Не спам"},
			{Action: "confirm_spam", Target: "$selected", Label: "В спам выбранные", Style: "destructive"},
		},
		Children: rows,
	}}
}

// spamSenderRow — одна строка списка: кто, сколько, почему и что
// с этим сделать. У отправителя с одним письмом группы нет — там письмо.
func spamSenderRow(г *spamSender, порог int) Component {
	name := г.Name
	if name == "" {
		name = г.Addr
	}
	if len(г.Messages) < порог {
		m := г.Messages[0]
		r := msgRow(m, []Action{
			{Action: "not_spam", Target: strconv.FormatInt(m.ID, 10), Label: "Не спам"},
			{Action: "confirm_spam", Target: strconv.FormatInt(m.ID, 10), Label: "В спам", Style: "destructive"},
		})
		r.Selectable = true
		return r
	}
	label := plural(len(г.Messages), "письмо", "письма", "писем")
	// Причина от модели — два-три предложения: в списке из десяти
	// отправителей это тридцать строк текста, который читают один раз.
	// В строке остаётся тема и короткая причина, полная уезжает в
	// раскрытие вместе с адресом и разбросом дат.
	коротко := subjectsLine(г.Subjects)
	if к := shortReason(г.Reason); к != "" {
		if коротко != "" {
			коротко += " · "
		}
		коротко += к
	}
	свойства := []Prop{{Label: "Адрес", Value: г.Addr}}
	if г.Reason != "" {
		свойства = append(свойства, Prop{Label: "Почему", Value: г.Reason})
	}
	if д := dateSpread(г.From, г.Till); д != "" {
		свойства = append(свойства, Prop{Label: "Когда", Value: д})
	}
	r := Component{Type: "row", Layout: "wide", ID: "спам-" + г.Addr,
		Title: name, Subtitle: label, Caption: коротко,
		Avatar: initialOf(г.Name, г.Addr), AvatarColor: avatarColor(name),
		Selectable: true,
		Props:      свойства,
		Actions: []Action{
			{Action: "$goto", Target: "senders~" + г.Addr, Label: "Показать письма"},
			{Action: "not_spam_sender", Target: г.Addr,
				Label: fmt.Sprintf("Не спам — все %d", len(г.Messages))},
			{Action: "confirm_spam_sender", Target: г.Addr, Style: "destructive",
				Label: fmt.Sprintf("В спам — все %d", len(г.Messages)),
				Confirm: fmt.Sprintf("Отправить в спам все %s от «%s»? Письма уедут в папку спама на почтовом сервере; из архива Сёмы они не пропадут.",
					plural(len(г.Messages), "письмо", "письма", "писем"), name),
				ConfirmOK: "В спам"},
		},
	}
	if г.Confident > 0 {
		// без слова «спам»: оно уже стоит в заголовке раздела, а в
		// строке место дорогое
		r.Badges = []Badge{{Text: fmt.Sprintf("%.0f %%", г.Confident*100), Color: confidenceColor(г.Confident)}}
	}
	return r
}

// shortReason — первая мысль из объяснения модели, до 60 знаков.
// Режем по границе предложения или запятой, а не по букве: обрывок
// «Подозрительная тема с "подар» хуже, чем ничего.
func shortReason(п string) string {
	п = strings.TrimSpace(п)
	if п == "" {
		return ""
	}
	// служебный хвост, который дописывает разбор задним числом
	if i := strings.Index(п, " · разобрано"); i > 0 {
		п = п[:i]
	}
	руны := []rune(п)
	if len(руны) <= 60 {
		return strings.TrimRight(п, " .")
	}
	// ищем последнюю границу в пределах предела
	граница := -1
	for i := 0; i < 60 && i < len(руны); i++ {
		switch руны[i] {
		case '.', ',', ';', '—':
			граница = i
		}
	}
	if граница < 20 {
		// границы нет — режем по слову
		for i := 59; i > 20; i-- {
			if руны[i] == ' ' {
				граница = i
				break
			}
		}
	}
	if граница < 20 {
		граница = 60
	}
	return strings.TrimRight(string(руны[:граница]), " .,;—") + "…"
}

// confidenceColor — 95 % и выше заливной капсулой, ниже — точкой. Так же,
// как это делает разбор одного письма.
func confidenceColor(c float64) string {
	if c >= 0.95 {
		return "#BE3320"
	}
	return "#9C6100"
}
