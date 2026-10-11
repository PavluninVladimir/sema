package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Экран разбора почты: письма по дням, строки писем, подписи.

var weekdayNames = [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}

// отсев — ящики, закрытых для ЭТОЙ сборки (снимок на устройстве, группа
// 2а «Снимок собирается отдельно»): их письма и вложения не попадают даже в
// счётчики, а не только прячутся у клиента. nil — обычный показ человеку,
// как было всегда: закрытый для облачной модели ящик от хозяина не закрыт.
func (s *Server) buildInbox(doc *Document, filter string, limit int, account, search string, отсев *store.Exclusion) error {
	st := s.eng.Store()
	// поиск по письмам. Его не было вовсе: письмо находилось либо глазами
	// в сводке за окно, либо через чат — по смысловой похожести, а она
	// зависит от эмбеддера, который на Pi засыпает вместе с моделью
	doc.Searchable = true
	if q := strings.TrimSpace(search); q != "" {
		return s.buildMailSearch(doc, q, account, отсев, limit)
	}
	// фильтры экрана — окно дайджеста; выбор запоминается
	if filter != "" && filter != s.eng.DigestWindow() {
		_ = s.eng.SetDigestWindow(filter)
	}
	d, err := s.eng.Digest(account, отсев)
	if err != nil {
		return err
	}
	pending, err := st.ByStatusAccount(store.StatusPending, account, отсев, 500)
	if err != nil {
		return err
	}
	doc.Searchable = true
	for _, w := range engine.DigestWindows {
		doc.Filters = append(doc.Filters, Filter{ID: w.ID, Title: w.Title})
	}
	doc.ActiveFilter = d.Window
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	where := "все ящики"
	if account != "" {
		where = account
	}
	doc.Heading = "Почта"
	// нули в подпись не пишем: «0 уведомлений» — не сведение, а шум.
	// Уведомления и спам отсюда убраны: под подписью тут же стоят их
	// заголовки разделов с теми же числами, а на телефоне подпись из-за
	// них уезжала на третью строку и отодвигала первое письмо.
	// Числа склоняются: «1 уведомлений» и «1 стоят внимания» стояли
	// прямо на экране до 10 сентября 2026.
	parts := []string{d.Label, plural(d.Total, "письмо", "письма", "писем")}
	if d.Notable > 0 {
		parts = append(parts, fmt.Sprintf("%d %s внимания", d.Notable,
			plural2(d.Notable, "стоит", "стоят", "стоят")))
	}
	doc.Subheading = strings.Join(parts, " · ") + " · " + where
	// связи нет — сказать, с какого момента: иначе пустой дайджест читается
	// как «писем нет», а не как «не проверяли»
	if since, down := s.eng.NetDown(); down {
		doc.Subheading += " · нет связи с сетью с " + since.Local().Format("15:04")
	}
	doc.ToolbarActions = []Action{
		{Action: "refresh", Label: "Проверить сейчас"},
		{Action: "$goto", Target: "digest:groups", Label: "Группы"},
	}
	// поломка — своим блоком, а не хвостом к подписи экрана
	if s.eng.LastErr != "" {
		add(Component{Type: "alarm", Subtitle: "Почта не разбирается",
			Caption: s.eng.LastErr,
			Actions: []Action{{Action: "refresh", Label: "Проверить снова"}}})
	}
	// ближайший платёж — первым: это единственное на экране почты, что
	// нельзя узнать из самого почтового ящика. Почта показывает письма, а
	// Сёма — документы внутри них и сроки по ним
	if счёт, строка := s.eng.NearestPayment(отсев); счёт != nil {
		r := Component{Type: "row", Layout: "wide", Icon: "creditcard",
			Subtitle: "Ближайший платёж", Caption: строка,
			Actions: []Action{{Action: "$file", Target: strconv.FormatInt(счёт.ID, 10), Label: "Открыть счёт"}}}
		if overdue(счёт.DueDate) {
			r.Level = LevelAlert
		} else if dueSoon(счёт.DueDate) {
			r.Level = LevelWarn
		}
		add(Component{Type: "section", Title: "Деньги", Children: []Component{r}})
	}

	// полка «ждут вас» — над всем остальным: то, что не поедет без
	// человека, не должно лежать под дайджестом
	if blk := s.notifyInboxBlock(); blk != nil {
		add(*blk)
	}

	// разбор спама — только когда есть что решать
	if len(pending) > 0 {
		// список отправителей, а не писем: сорок одинаковых «Ваш чек +
		// (1) подарок» от Ozon становятся одной строкой с числом
		for _, c := range s.spamBySender(pending, limit) {
			add(c)
		}
	} else {
		hamN, _ := st.CountRules("ham")
		caption := fmt.Sprintf("%s в спам по правилам отправителей · сомнений не было · %s",
			plural(d.Spam, "письмо", "письма", "писем"), plural(hamN, "разрешённый адрес", "разрешённых адреса", "разрешённых адресов"))
		if s.eng.WhitelistOnly() {
			caption = fmt.Sprintf("режим «только разрешённые отправители»: письма от всех, кроме %s, уходят в спам сразу; файлы и чеки Сёма всё равно забирает",
				plural(hamN, "разрешённого адреса", "разрешённых адресов", "разрешённых адресов"))
		}
		sendersID := "senders"
		if account != "" {
			sendersID = "senders@" + account
		}
		actions := []Action{{Action: "$goto", Target: sendersID, Label: "Отправители"}}
		if s.eng.WhitelistOnly() {
			actions = append(actions, Action{Action: "whitelist_off", Label: "Выключить режим"})
		}
		add(Component{Type: "section", Title: "Спам за окно · всё разобрано", Children: []Component{{
			Type: "row", ID: "spam-summary", Layout: "wide", Subtitle: "Новых писем на проверку нет",
			Caption: caption, Actions: actions,
		}}})
	}

	// сводка по группам
	sections := s.digestSections(d, account)
	if len(sections) == 0 {
		add(Component{Type: "text", Style: "secondary", Text: "За окно писем не было — сводка пуста. Окно меняется чипами сверху."})
	}
	for _, c := range sections {
		add(c)
	}

	// блоки модулей: что ещё модули поняли из писем
	for _, blk := range s.eng.ModuleBlocks(context.Background(), "inbox", account) {
		raw, err := json.Marshal(blk.Components)
		if err != nil {
			continue
		}
		var comps []Component
		if json.Unmarshal(raw, &comps) != nil || len(comps) == 0 {
			continue
		}
		add(Component{Type: "section", Title: blk.Module.Title + " · модуль", Children: comps})
	}

	if queued, qerr := st.ByStatus(store.StatusQueued, отсев, 100); qerr == nil && len(queued) > 0 {
		rows := make([]Component, 0, len(queued))
		for _, m := range queued {
			r := msgRow(m, nil)
			r.Badge, r.BadgeColor = "переносится…", "gray"
			rows = append(rows, r)
		}
		add(Component{Type: "section", Title: fmt.Sprintf("Переносятся в спам (%d)", len(queued)), Children: rows})
	}

	// работа Сёмы — одной строкой
	add(Component{Type: "section", Title: "Сёма сделал за окно", Children: []Component{{
		Type: "row", ID: "work-summary", Layout: "wide",
		Subtitle: s.eng.WorkSummary(account, d.Since),
		Actions:  []Action{{Action: "$goto", Target: "tasks", Label: "Подробнее"}},
	}}})

	if recent, err := st.ByStatusAccount(store.StatusAutoMoved, account, отсев, 8); err == nil && len(recent) > 0 && len(pending) == 0 {
		rows := make([]Component, 0, len(recent))
		for _, m := range recent {
			rows = append(rows, msgRowIn(m, nil, account))
		}
		add(Component{Type: "section", Title: "Последнее, что Сёма отправил в спам", Children: rows})
	}

	if n, err := st.CountByStatusAccount(store.StatusAutoMoved, account, отсев); err == nil && n > 0 {
		doc.Notice = &Notice{
			Icon: "trash",
			Text: fmt.Sprintf("%s перенесено в спам автоматически — Сёма удалит их из почты через %d дн., копии останутся у Сёмы навсегда",
				plural(n, "письмо", "письма", "писем"), s.eng.RetentionDays()),
		}
	}
	// сколько Сёма стёр с почтового сервера: журнал велся с самого начала,
	// но человеку его не показывали нигде — «сколько писем удалено»
	// приходилось спрашивать (13 сентября 2026)
	if за := st.DeletionsSince(1); за > 0 {
		// «что именно» — вопрос про письма, а не про настройки: кнопка
		// вела на «Папки и хранение», и человек искал список там
		// (13 сентября 2026)
		add(Component{Type: "row", Layout: "wide", ID: "стёрто-за-сутки", Icon: "trash",
			Subtitle: "С почтового сервера стёрто за сутки: " + strconv.Itoa(за),
			Caption: "по сроку хранения; за неделю — " + strconv.Itoa(st.DeletionsSince(7)) +
				". Из архива Сёмы письма не удаляются никогда",
			Actions: []Action{{Action: "$goto", Target: purgedScreen, Label: "Какие письма", Style: "primary"}}})
	}
	return nil
}

// dayGroup — «Сегодня» / «Вчера» / «12 августа» для подзаголовков списка.
func dayGroup(t time.Time) string {
	y, m, d := time.Now().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	switch {
	case !t.Before(today):
		return "Сегодня"
	case !t.Before(today.AddDate(0, 0, -1)):
		return "Вчера"
	case !t.Before(today.AddDate(0, 0, -6)):
		return russianWeekday(t)
	default:
		return fmt.Sprintf("%d %s", t.Day(), monthNames[int(t.Month())-1])
	}
}

func russianWeekday(t time.Time) string { return weekdayNames[int(t.Weekday())] }

// groupRows расставляет между строками подзаголовки-дни (как в Mail).
func groupRows(rows []Component, dates []time.Time) []Component {
	out := make([]Component, 0, len(rows)+4)
	last := ""
	for i, r := range rows {
		if g := dayGroup(dates[i]); g != last {
			last = g
			out = append(out, Component{Type: "group", Text: g})
		}
		out = append(out, r)
	}
	return out
}

func msgRow(m *store.Message, actions []Action) Component {
	return msgRowIn(m, actions, "")
}

// msgRowIn: showAccount != "" — не показываем метку ящика (мы уже внутри него).
func msgRowIn(m *store.Message, actions []Action, insideAccount string) Component {
	// уверенность видна всегда, когда она есть: по ней человек решает, на
	// какие письма смотреть внимательно, а какие подтверждать пачкой.
	// Цвет по величине: уверенное — тревога, сомнительное — внимание
	badge, color := "", "orange"
	if m.Confidence > 0 {
		badge = fmt.Sprintf("спам %.0f%%", m.Confidence*100)
		if m.Confidence >= 0.9 {
			color = "red"
		}
	}
	from := m.FromName
	if from == "" {
		from = m.FromAddr
	}
	// адрес не повторяем: он уже стоит заголовком, когда имени нет
	meta := ""
	if m.FromName != "" {
		meta = m.FromAddr
	}
	if !m.Date.IsZero() {
		meta += " · " + m.Date.Format("15:04")
	}
	if insideAccount == "" && m.Account != "" {
		meta += " · " + m.Account
	}
	meta = strings.TrimPrefix(meta, " · ")
	caption := ""
	var facts []Prop
	// «почему» показываем, только когда причина что-то объясняет: «папка
	// INBOX» под каждым письмом — это шум на весь экран
	if r := strings.TrimSpace(m.Reason); r != "" && !emptyReason(r) {
		facts = append(facts, Prop{Label: "Почему", Value: r})
	}

	initial := "?"
	for _, r := range from {
		initial = strings.ToUpper(string(r))
		break
	}
	var hash uint32
	for _, b := range []byte(m.FromAddr) {
		hash = hash*31 + uint32(b)
	}
	actions = append(actions, Action{Action: "$goto", Target: "msg:" + strconv.FormatInt(m.ID, 10), Label: "Открыть"})
	subject := m.Subject
	if subject == "" {
		subject = "(без темы)"
	}
	return Component{
		Type:        "row",
		ID:          strconv.FormatInt(m.ID, 10),
		Title:       subject,
		Subtitle:    from,
		Meta:        meta,
		MetaInline:  true,
		Caption:     caption,
		Facts:       facts,
		Badge:       badge,
		BadgeColor:  color,
		Avatar:      initial,
		AvatarColor: avatarPalette[hash%uint32(len(avatarPalette))],
		Actions:     actions,
	}
}

// emptyReason — причина разбора, которая ничего не объясняет. «Папка
// INBOX» стоит у каждого письма и занимает строку на всю ширину, не
// сообщая ничего: письмо и так лежит там, где лежит.
func emptyReason(r string) bool {
	low := strings.ToLower(strings.TrimSpace(r))
	// «папка: INBOX» приходит с двоеточием: причина, состоящая из одного
	// имени папки, ничего не объясняет в любом написании
	low = strings.TrimPrefix(low, "папка:")
	low = strings.TrimPrefix(low, "папка")
	low = strings.TrimSpace(low)
	switch low {
	case "inbox", "входящие", "правило", "по умолчанию", "":
		return true
	}
	return false
}

// overdue / dueSoon — как красить строку платежа. Пороги здесь
// намеренно простые: «сегодня и раньше» — красное, «на этой неделе» —
// жёлтое. Считать иначе значит спорить с календарём человека.
func overdue(due string) bool {
	t, err := time.Parse("2006-01-02", due)
	return err == nil && t.Before(time.Now().Truncate(24*time.Hour))
}

func dueSoon(due string) bool {
	t, err := time.Parse("2006-01-02", due)
	return err == nil && time.Until(t) < 7*24*time.Hour
}

// buildMailSearch — что нашлось по словам. Отдельный вид экрана, а не
// фильтр сводки: сводка отвечает на «что пришло за окно», а поиск — на
// «где то письмо», и смешивать их значит врать в обоих ответах.
func (s *Server) buildMailSearch(doc *Document, query, account string, отсев *store.Exclusion, limit int) error {
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	list, err := s.eng.Store().SearchMessages(query, account, отсев, limit)
	if err != nil {
		return err
	}
	doc.Heading = "Поиск по письмам"
	doc.Subheading = fmt.Sprintf("«%s» · %s", query, plural(len(list), "письмо", "письма", "писем"))
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	if len(list) == 0 {
		// указатель может ещё заполняться: сказать «ничего не нашлось»,
		// когда искать пока не по чему, — это неправда
		if ждут, _ := s.eng.Store().MessagesToIndex(1); len(ждут) > 0 {
			add(emptyState("Указатель ещё заполняется",
				"Сёма дописывает письма в поиск — это занимает секунды после запуска. Повторите запрос", "hourglass"))
			return nil
		}
		add(emptyState("Ничего не нашлось",
			"Поиск идёт по теме, отправителю и тексту письма — словами, а не по смыслу. "+
				"Письма, тело которых ещё не скачано, ищутся только по теме и первым строкам. "+
				"Можно искать по началу слова: «ульянов*»", "magnifyingglass"))
		return nil
	}
	var rows []Component
	for _, m := range list {
		rows = append(rows, messageSearchQuery(m))
	}
	add(Component{Type: "section", Title: "Нашлось", Children: rows})
	add(Component{Type: "text", Style: "secondary",
		Text: "Поиск словами: он работает, когда все модели спят. Смысловой поиск живёт в чате — там можно спросить «что там было про продление домена»."})
	return nil
}

func messageSearchQuery(m *store.Message) Component {
	label := m.FromAddr
	if m.FromName != "" {
		label = m.FromName + " · " + m.FromAddr
	}
	if !m.Date.IsZero() {
		label += " · " + m.Date.Format("02.01.2006")
	}
	// где письмо лежит — часть ответа: «в спаме» объясняет, почему его не
	// было видно
	if where := whereMessage(m.Status); where != "" {
		label += " · " + where
	}
	return Component{Type: "row", ID: "найдено-" + strconv.FormatInt(m.ID, 10),
		Subtitle: subjectOr(m.Subject), Caption: label,
		Avatar: initialOf(m.FromName, m.FromAddr), AvatarColor: "#8E8E93",
		Actions: []Action{{Action: "$goto", Target: "msg:" + strconv.FormatInt(m.ID, 10), Label: "Открыть"}}}
}

func whereMessage(status string) string {
	switch status {
	case store.StatusConfirmed, store.StatusAutoMoved:
		return "в спаме"
	case store.StatusPending:
		return "ждёт решения"
	case store.StatusArchived:
		return "в архиве"
	case store.StatusServerSpam:
		return "в спаме у провайдера"
	case store.StatusServerTrash:
		return "в корзине у провайдера"
	}
	return ""
}
