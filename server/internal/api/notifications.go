package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Экран «Уведомления»: один список, чипы по источнику, без поиска.
// Вкладка «Настройки → Уведомления»: каналы, счётчик, хранение, правила.

const (
	notifyScreen         = "notifications"
	notifySettingsScreen = "settings:notify"
)

var notifyColor = map[string]string{"alert": "#FF3B30", "warn": "#FF9500", "info": "#0058B0", "ok": "#28C840"}
var notifyLevelTitle = map[string]string{"alert": "тревога", "warn": "внимание", "info": "инфо", "ok": "в норме"}
var sourceTitle = map[string]string{"servers": "серверы", "mail": "почта", "requests": "запрос", "modules": "модуль"}

func (s *Server) notifyBadge() string {
	if n := s.eng.NotifyCounter(); n > 0 {
		return strconv.Itoa(n)
	}
	return ""
}

// notifyLevel — состояние пункта меню: тревога, если хоть одна запись
// ждёт человека; иначе жёлтое, если есть что посмотреть.
func (s *Server) notifyLevel() string {
	switch s.eng.Store().TopOpenNotifyLevel() {
	case "alert":
		return LevelAlert
	case "warn":
		return LevelWarn
	case "info":
		return LevelInfo
	}
	return ""
}

func (s *Server) buildNotifications(doc *Document, filter string, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Уведомления"
	doc.Subheading = "Куда приходят уведомления, какие письма их рождают, счётчик в меню и срок хранения"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	openList, err := st.Notifications(true, 300)
	if err != nil {
		return err
	}
	// «ok» скрыт в общем списке, но в чипе «Серверы» виден каждый сервер —
	// сколько серверов, столько и строк
	doneList, _ := st.Notifications(false, 1000)
	var visible []*store.Notification
	counts := map[string]int{}
	serverKeys := map[string]bool{}
	for _, n := range append(append([]*store.Notification{}, openList...), doneList...) {
		if strings.HasPrefix(n.Key, "server:") {
			serverKeys[n.Key] = true
		}
	}
	// Чип показывает то, что человек увидит на своей вкладке. Раньше
	// «Серверы» считали все серверы (включая принятые к сведению), а «Все»
	// — только ждущие: получалось «Все 3» при «Серверы 4», и отчёт об
	// упавшем обновлении не был виден на главной вкладке.
	for _, n := range openList {
		if n.Level == "ok" {
			continue
		}
		visible = append(visible, n)
		counts[engine.NotifySourceGroup(n)]++
		counts["all"]++
	}
	// ждущих серверных записей нет — чип идёт без числа: иначе «Серверы 4»
	// при «Все 2» читается как потерянные записи, хотя все четыре в норме
	_ = serverKeys
	// число отделено от слова точкой-разделителем: «Почта 4» читалось как
	// название вкладки, а не как счётчик
	chip := func(id, title string, n int) Filter {
		if n > 0 {
			title = fmt.Sprintf("%s · %d", title, n)
		}
		return Filter{ID: id, Title: title}
	}
	doc.Tabs = []Filter{
		chip(notifyScreen, "Все", counts["all"]),
		chip(notifyScreen+"@servers", "Серверы", counts["servers"]),
		chip(notifyScreen+"@mail", "Почта", counts["mail"]),
		chip(notifyScreen+"@requests", "Запросы", counts["requests"]+counts["modules"]),
		// «История 1000» — это предел выборки, а не число записей: показываем
		// столько, сколько закрытых на самом деле
		chip(notifyScreen+"@done", "История", st.CountClosedNotifications()),
	}
	doc.ActiveTab = notifyScreen
	if filter != "" {
		doc.ActiveTab = notifyScreen + "@" + filter
	}

	// переход из уведомления Mac или по ссылке: весь список, нужная запись
	// раскрыта и стоит первой
	var focus int64
	if id, err := strconv.ParseInt(filter, 10, 64); err == nil && id > 0 {
		focus, filter = id, ""
		doc.ActiveTab = notifyScreen
		if n, ok := st.Notification(id); ok && n.State != store.NotifyOpen {
			add(Component{Type: "section", Title: "Уведомление · уже решено", Children: []Component{s.notifyRow(n, true)}})
		}
	}

	if filter == "done" {
		// история за всё время, что хранится: раньше сюда попадала только
		// первая сотня, и человек не мог посмотреть, что было неделю назад
		var rows []Component
		keep := limit
		if keep <= 0 || keep == defaultRows {
			keep = 50
		}
		shown := doneList
		if len(shown) > keep {
			shown = shown[:keep]
		}
		for _, n := range shown {
			rows = append(rows, s.notifyRow(n, false))
		}
		if len(rows) == 0 {
			rows = append(rows, emptyState("Пока ничего не решали",
				"сюда ложится история: что вы сделали, что отклонили и какие отчёты сменились новыми", "clock.arrow.circlepath"))
		}
		doc.Subheading = fmt.Sprintf("%s за %s: сделанное, отклонённое и заменённые отчёты",
			plural(len(doneList), "решение", "решения", "решений"),
			plural(s.eng.NotifyKeepDays(), "день", "дня", "дней"))
		add(Component{Type: "section", Children: rows})
		if len(doneList) > len(shown) {
			add(Component{Type: "button", Actions: []Action{
				{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(shown), len(doneList))},
			}})
		}
		return nil
	}

	// предложение подписаться на пуши — наверху главного списка: сюда ведёт и
	// значок, и пуш, и нижняя полоса телефона (pushOffer)
	if filter == "" {
		add(pushOffer())
	}

	list := visible
	if filter == "servers" {
		// по серверу — последний отчёт, даже принятый к сведению: сколько
		// серверов, столько строк
		seen := map[string]bool{}
		list = nil
		for _, n := range append(append([]*store.Notification{}, openList...), doneList...) {
			if !strings.HasPrefix(n.Key, "server:") || seen[n.Key] {
				continue
			}
			seen[n.Key] = true
			list = append(list, n)
		}
		counts["servers"] = len(list)
		doc.Tabs[1] = chip(notifyScreen+"@servers", "Серверы", len(list))
	}
	// строка собирается вместе со своей записью: раньше уровень брали из
	// list[i], а rows[i] мог быть уже от другой записи — отбор по чипу
	// пропускал строки, а раскрытая вставала первой. «Ждут вас» набивалось
	// отчётами «к сведению», и группировка врала.
	type строка struct {
		запись *store.Notification
		блок   Component
	}
	// почему запись молчит — считаем один раз на весь список
	молчание := s.eng.DeliveryChecker(list)
	var собрано []строка
	for _, n := range list {
		g := engine.NotifySourceGroup(n)
		if filter == "servers" && g != "servers" {
			continue
		}
		if filter == "mail" && g != "mail" {
			continue
		}
		if filter == "requests" && g != "requests" && g != "modules" {
			continue
		}
		собрано = append(собрано, строка{запись: n, блок: s.notifyRowWhy(n, n.ID == focus, молчание)})
	}
	// запись, за которой пришли по ссылке, встаёт первой в своей группе
	for i := range собрано {
		if собрано[i].запись.ID == focus && i > 0 {
			это := собрано[i]
			copy(собрано[1:i+1], собрано[:i])
			собрано[0] = это
			break
		}
	}
	byLevel := map[string][]Component{}
	for _, стр := range собрано {
		lvl := стр.запись.Level
		if lvl != "alert" && lvl != "warn" {
			lvl = "info"
		}
		byLevel[lvl] = append(byLevel[lvl], стр.блок)
	}
	// человеку важен не источник, а «поедет ли без меня»: три группы по
	// уровню, и первой стоит та, что держит работу
	order := []struct{ level, title string }{
		{"alert", "Не поедет без вас"},
		{"warn", "Стоит посмотреть"},
		{"info", "К сведению"},
	}
	if len(собрано) == 0 {
		add(emptyState("Ничего не ждёт вашего решения",
			notifyEmptyWhy(filter, s.notifyOkLine(openList)), "checkmark.circle"))
	} else {
		doc.Subheading = notifyLead(byLevel)
		for _, o := range order {
			children := byLevel[o.level]
			if len(children) == 0 {
				continue
			}
			// одинаковые записи сворачиваются только в «К сведению»:
			// в двух верхних группах у строки есть кнопка, и прятать
			// её под счётчик значит прятать саму работу
			if o.level == "info" {
				children = collapseSame(children, 3)
			}
			add(Component{Type: "section", ID: "notify-" + o.level,
				Title:    fmt.Sprintf("%s · %d", o.title, len(byLevel[o.level])),
				Children: children})
		}
	}
	if ok := s.notifyOkLine(openList); ok != "" && len(собрано) > 0 {
		add(Component{Type: "text", Style: "secondary", Text: ok})
	}
	add(Component{Type: "text", Style: "secondary",
		Text: fmt.Sprintf("Решение уходит туда, где родился запрос, а здесь остаётся история — вкладка «История». "+
			"Старые записи Сёма чистит сам через %s. Точка у пункта меню загорается с уровня «стоит посмотреть» — "+
			"порог и каналы доставки в «Настройки → Уведомления».", plural(s.eng.NotifyKeepDays(), "день", "дня", "дней"))})
	return nil
}

// notifyLead — подпись экрана человеческой фразой: сколько записей и что
// из них держит работу. Раньше счётчики стояли только в чипах по
// источнику, и «четыре не поедут без вас» было не видно вовсе.
func notifyLead(byLevel map[string][]Component) string {
	total := len(byLevel["alert"]) + len(byLevel["warn"]) + len(byLevel["info"])
	var части []string
	if n := len(byLevel["alert"]); n > 0 {
		части = append(части, plural(n, "не поедет без вас", "не поедут без вас", "не поедут без вас"))
	}
	if n := len(byLevel["warn"]); n > 0 {
		части = append(части, fmt.Sprintf("%d стоит посмотреть", n))
	}
	if n := len(byLevel["info"]); n > 0 {
		части = append(части, fmt.Sprintf("%d — к сведению", n))
	}
	лид := plural(total, "запись", "записи", "записей")
	if len(части) == 0 {
		return лид
	}
	return лид + ": " + strings.Join(части, ", ")
}

// notifyEmptyWhy — пустое место объясняет себя: что здесь бывает и почему
// сейчас пусто именно на этой вкладке.
func notifyEmptyWhy(filter, okLine string) string {
	why := map[string]string{
		"servers": "отчёты об обходе серверов приходят сюда — сейчас ни один не просит вмешательства",
		"mail":    "сюда попадают письма со сроком и без ответа — таких сейчас нет",
		"requests": "здесь ждут решения команды на серверах, права модулей и предложенные скиллы — " +
			"ничего не ждёт",
	}[filter]
	if why == "" {
		why = "сюда сходится всё, что не поедет без вас: команды на серверах, письма со сроком, " +
			"права модулей, предложенные скиллы. Сейчас ничего такого нет"
	}
	if okLine != "" {
		why += " · " + okLine
	}
	return why
}

// notifyOkLine — «серверы: 3 в норме · проверено сегодня 08:00» из записей ok.
func (s *Server) notifyOkLine(open []*store.Notification) string {
	okServers, last := 0, ""
	for _, n := range open {
		if n.Level == "ok" && strings.HasPrefix(n.Key, "server:") {
			okServers++
			if n.Updated > last {
				last = n.Updated
			}
		}
	}
	if okServers == 0 {
		return ""
	}
	return fmt.Sprintf("серверы: %d в норме · проверено %s", okServers, whenText(last))
}

// snoozedUntil — до каких пор запись отложена. Срок в прошлом уже не
// откладывает: задача «Сроки» вот-вот поднимет запись обратно.
func snoozedUntil(n *store.Notification) (time.Time, bool) {
	if n.State != store.NotifyOpen || n.RemindAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, n.RemindAt)
	if err != nil || !t.After(time.Now()) {
		return time.Time{}, false
	}
	return t, true
}

func (s *Server) notifyRow(n *store.Notification, expanded bool) Component {
	return s.notifyRowWhy(n, expanded, nil)
}

// notifyRowWhy — та же строка, но со словом о молчании. Недоставленная
// запись молчит дважды: человек её не получил и не знает, что не получил.
// Проверка приходит снаружи готовой на весь список: каналы и отказы
// читаются один раз, а не на каждую из трёх сотен строк.
func (s *Server) notifyRowWhy(n *store.Notification, expanded bool, почему func(*store.Notification) string) Component {
	g := engine.NotifySourceGroup(n)
	// капсула называет источник, а уровень показывает точка слева:
	// «почта · инфо» при синей точке — то же самое сказано дважды
	badges := []Badge{{Text: sourceTitle[g], Color: "#8E8E93"}}
	if n.State != store.NotifyOpen {
		state := map[string]string{store.NotifyDone: "сделано", store.NotifyDismissed: "отклонено", store.NotifyReplaced: "заменено"}[n.State]
		if n.DecidedBy != "" {
			state += " · " + n.DecidedBy
		}
		badges = []Badge{{Text: state, Color: "#8E8E93"}}
	}
	// заголовок уже несёт итог — в подписи только откуда и когда
	meta := sourceLabel(n.Source) + " · " + whenText(n.Updated)
	if n.Summary != "" && !strings.Contains(n.Title, n.Summary) {
		meta = n.Summary + " · " + meta
	}
	if n.Due != "" {
		if t, err := time.Parse(time.RFC3339, n.Due); err == nil {
			meta += " · срок " + t.Format("02.01 15:04")
		}
	}
	// отложенное говорит об этом само: иначе запись просто тише
	// остальных, и человек не понимает, почему она молчит
	if отложено, ok := snoozedUntil(n); ok {
		meta = "отложено до " + отложено.Format("02.01 15:04") + " · " + meta
	}
	// молчание — впереди всего: «Проверить продление домена» ушло никуда,
	// и это важнее, чем когда запись обновилась
	if почему != nil {
		if нет := почему(n); нет != "" {
			meta = нет + " · " + meta
			badges = append(badges, Badge{Text: "молчит", Color: notifyColor["warn"]})
		}
	}
	var props []Prop
	for _, f := range n.Fields {
		if f.Value == "" {
			continue
		}
		props = append(props, Prop{Label: f.Label, Value: f.Value, Color: notifyColor[f.Level]})
	}
	var cmds []CmdRow
	var actions []Action
	if n.State == store.NotifyOpen {
		for i, a := range n.Actions {
			target := fmt.Sprintf("%d~%d", n.ID, i)
			switch a.Kind {
			case "ssh":
				cmds = append(cmds, CmdRow{Text: a.Cmd, Note: a.Label, Actions: []Action{
					{Action: "notify_act", Target: target, Label: "Выполнить", Style: "primary",
						Confirm: "Выполнить на " + a.Host + ":\n" + a.Cmd, ConfirmOK: "Выполнить"},
					{Action: "notify_state", Target: fmt.Sprintf("%d~dismiss", n.ID), Label: "Отклонить", Style: "link"}}})
			case "goto":
				actions = append(actions, Action{Action: "$goto", Target: a.Screen, Label: a.Label})
			default:
				actions = append(actions, Action{Action: "notify_act", Target: target, Label: a.Label})
			}
		}
		if len(cmds) > 0 && len(actions) == 0 {
			actions = append(actions,
				Action{Action: "$goto", Target: notifyScreen + "@" + strconv.FormatInt(n.ID, 10), Label: "Подробнее"})
		}
		// «Прочитано» есть у каждой открытой записи: чаще всего с записью
		// ничего делать не нужно, её достаточно закрыть. Кнопки перехода
		// клиент не рисует (по строке и так можно щёлкнуть), поэтому без
		// этой строки у записи не оставалось видимого действия, кроме
		// «Разобраться» — а разбираться обычно не с чем
		// «Прочитано» — только у записи, которую и правда достаточно
		// закрыть. У записи, которая держит решение (команда на
		// подтверждении), закрыть её значит соврать: решение остаётся в
		// очереди, и через пять минут пинатель приносит запись обратно —
		// «нажал выполнено, они вернулись» (13 сентября 2026). Такое
		// решение снимается только «Выполнить» или «Отклонить»
		if !holdsDecision(n) {
			done := "Прочитано"
			if len(cmds) > 0 {
				done = "Принято к сведению"
			}
			actions = append(actions, Action{Action: "notify_state",
				Target: fmt.Sprintf("%d~done", n.ID), Label: done})
		}
		// разговор заводится по кнопке, а не сам: в чате Сёма продолжает работу
		// «Разобраться» — непонятное обещание: нажатие заводит разговор с
		// Сёмой об этой записи, и так и надо называть
		chatLabel := "Спросить Сёму"
		if id := s.eng.NotifyChatID(n.ID); id > 0 {
			chatLabel = "Открыть чат"
			actions = append(actions, Action{Action: "$goto", Target: "chat:" + strconv.FormatInt(id, 10), Label: chatLabel})
		} else {
			actions = append(actions, Action{Action: "notify_chat", Target: strconv.FormatInt(n.ID, 10), Label: chatLabel})
		}
		// «Отложить» — то же «не сейчас», только с обещанием вернуться:
		// без него запись закрывали «прочитано», и дело о ней забывалось.
		// Сроки берём из настройки: у каждого свой вечер. Кладём в конец
		// нарочно: видимыми остаются «Прочитано» и «Разобраться», а сроки
		// уходят под «…» — четыре кнопки в строке это не выбор, а забор
		for _, когда := range s.eng.SnoozeOptions() {
			actions = append(actions, Action{Action: "notify_snooze",
				Target: strconv.FormatInt(n.ID, 10) + "~" + когда, Label: "Отложить · " + когда})
		}
		// по макету: первая кнопка — главная, вторая — ссылка, остальные — тоже ссылки
		for i := range actions {
			if i == 0 {
				actions[i].Style = "primary"
			} else {
				actions[i].Style = "link"
			}
		}
	} else {
		actions = []Action{{Action: "notify_state", Target: fmt.Sprintf("%d~open", n.ID), Label: "Вернуть", Style: "link"}}
	}
	// заливка строки убрана: розовая карточка читалась как ошибка, хотя
	// «ждёт ответа» — это ожидание. Уровень показывают точка слева и
	// узкая полоса у края, их рисует клиент по Level
	row := Component{Type: "row", Layout: "wide", ID: fmt.Sprintf("notify-%d", n.ID),
		Level: n.Level, Dot: levelDot(n.Level),
		Subtitle: n.Title, Caption: meta, Badges: badges, Actions: actions, Props: props, Cmds: cmds}
	// картинка показывается в самой записи: решать «показывать ли её
	// модели», не видя картинки, нельзя — в вопросе стоял хэш содержимого
	// (13 сентября 2026)
	картинка, естьКартинка := recordFile(n)
	if len(props) > 0 || len(cmds) > 0 {
		row.Expandable = true
		row.Expanded = expanded
	}
	if естьКартинка {
		// картинкой, а не рамкой: файл отдаётся с запретом встраивания, и
		// в рамке вместо картинки было «сайт не позволяет установить
		// соединение» (14 сентября 2026)
		row.Image = fmt.Sprintf("/api/files/%d", картинка)
		// видна сразу: под свёрткой человек её не увидит, а решать
		// «показывать ли её модели» не глядя нельзя
		row.Expandable = true
		row.Expanded = true
	}
	return row
}

// recordFile — номер файла, к которому относится запись: «file:<id>» в
// Ref. По нему в строке показывается сама картинка.
func recordFile(n *store.Notification) (int64, bool) {
	if !strings.HasPrefix(n.Ref, "file:") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(n.Ref, "file:"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func sourceLabel(source string) string {
	switch {
	case source == "core":
		return "ядро"
	case source == "mail":
		return "почта"
	case strings.HasPrefix(source, "module:"):
		return "модуль " + strings.TrimPrefix(source, "module:")
	}
	return source
}

// notifyInboxBlock — полка «Ждут вас» на «Почте». Показывает ТОЛЬКО
// почтовые записи. Раньше она считала все открытые подряд, и на почтовом
// экране первой строкой стояла команда `crontab -l` с сервера
// srv-bots — человек пришёл читать письма, а ему предъявили работу
// администратора. Правило простое: экран говорит про своё, а общий
// список ждёт в «Уведомлениях», где у пункта меню и так есть счётчик.
func (s *Server) notifyInboxBlock() *Component {
	open, err := s.eng.Store().Notifications(true, 300)
	if err != nil {
		return nil
	}
	var top *store.Notification
	waiting := 0
	byLevel := map[string]int{}
	for _, n := range open {
		if n.Level == "ok" || engine.NotifySourceGroup(n) != "mail" {
			continue
		}
		waiting++
		byLevel[n.Level]++
		// самая громкая — наверх: тревога весомее «внимания», а оно —
		// весомее «к сведению». Внутри уровня порядок хранилища (свежие
		// первыми) уже верный
		if top == nil || higherLevel(n.Level, top.Level) {
			top = n
		}
	}
	if waiting == 0 {
		// по почте ничего не ждёт — и полки нет. Строка «всё спокойно»
		// на этом экране была бы неправдой: спокойно по почте, а не везде
		return nil
	}
	var parts []string
	for _, l := range []string{"alert", "warn", "info"} {
		if byLevel[l] > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", levelWord(l), byLevel[l]))
		}
	}
	// полка, а не раздел: то, что не поедет без человека, стоит вверху
	// одним тёплым блоком и не теряется среди дайджеста
	return &Component{Type: "shelf", Title: "Ждут вас", Meta: strings.Join(parts, " · "),
		Level: top.Level, Children: []Component{{
			Type: "row", Subtitle: top.Title, Caption: waitingLine(waiting, top),
			Actions: []Action{{Action: "$goto", Target: notifyScreen + "@mail", Label: "Открыть", Style: "primary"}}}}}
}

// higherLevel — какая запись громче. Порядок один на весь интерфейс.
func higherLevel(a, b string) bool { return levelWeight(a) > levelWeight(b) }

func levelWeight(l string) int {
	switch l {
	case "alert":
		return 3
	case "warn":
		return 2
	case "info":
		return 1
	}
	return 0
}

// levelWord — для подписи полки: «тревога: 1 · внимание: 2».
func levelWord(l string) string {
	switch l {
	case "alert":
		return "тревога"
	case "warn":
		return "внимание"
	case "info":
		return "к сведению"
	}
	return l
}

// waitingLine — сколько ждёт и что это за запись: заголовок стоит
// названием строки, а сюда идёт пояснение, зачем её открыть.
func waitingLine(waiting int, top *store.Notification) string {
	из := plural(waiting, "запись ждёт", "записи ждут", "записей ждут")
	if top.Summary != "" {
		return из + " решения · " + clipRunes(top.Summary, 90)
	}
	return из + " решения"
}

// ---- настройки: каналы ----

var levelOptions = []string{"от «инфо»", "от «внимание»", "от «тревога»"}

func levelFromOption(o string) string {
	switch {
	case strings.Contains(o, "инфо"):
		return "info"
	case strings.Contains(o, "тревог"):
		return "alert"
	}
	return "warn"
}

func levelOption(level string) string {
	switch level {
	case "info":
		return levelOptions[0]
	case "alert":
		return levelOptions[2]
	}
	return levelOptions[1]
}

// Внутренние вкладки «Уведомлений». Одной лентой здесь лежали каналы
// доставки, устройства с пушами, правила почты и форма из четырнадцати
// полей про счётчик, провод и пинки — 827 узлов и три разных дела.
// «Куда доходит», «из каких писем рождается» и «как часто» — три вопроса,
// и человек приходит с одним.
const (
	notifyTabChannels = ""      // Каналы доставки — куда уходит
	notifyTabRules    = "rules" // Правила почты — из чего рождается
	notifyTabShow     = "show"  // Что показывать — счётчик, хранение, пределы
)

func (s *Server) buildNotifySettings(doc *Document, screen string) error {
	// «settings:notify~rules», «settings:notify@new»: вкладка раздела или «Добавить канал»
	_, id, tab := splitEntity(screen)
	st := s.eng.Store()
	doc.Heading = "Уведомления"
	doc.Layout = "wide"
	if id == notifyNew {
		return s.buildNotifyChannelNew(doc)
	}
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	chans0, _ := st.NotifyChannels()
	включено := 0
	for _, c := range chans0 {
		if c.Enabled {
			включено++
		}
	}
	rules0, _ := st.NotifyRules()
	tabs := []innerTab{
		{ID: notifyTabChannels, Name: "Каналы доставки", Count: strconv.Itoa(включено)},
		{ID: notifyTabRules, Name: "Правила почты", Count: strconv.Itoa(len(rules0))},
		{ID: notifyTabShow, Name: "Что показывать"},
	}
	// ни одного включённого канала — уведомления никуда не доходят, и это
	// видно до того, как человек откроет вкладку
	if включено == 0 {
		tabs[0].Count, tabs[0].Level = "нет", LevelWarn
	}
	setSubtabs(doc, notifySettingsScreen, tab, tabs)

	switch tab {
	case notifyTabRules:
		s.mailRules(add, st)
		return nil
	case notifyTabShow:
		s.whatToShow(add, st)
		return nil
	}

	// каналы доставки, устройства с пушами и параметры пушей — settings_notify_channels.go
	s.buildNotifyChannels(doc, add)
	return nil
}

// whatToShow — счётчик, хранение и пределы: то, что правят раз в
// полгода. Форма одна, и шлёт она все свои поля разом — резать её на
// части нельзя: непришедшее поле здесь значит «сбросить».
//
// Провод, опрос экрана, чаты команд, пинатель, хвост ветки и часовой пояс
// жили здесь между правилами писем (2 октября 2026 переехали на «Работу
// Сёмы»): это про то, как Сёма работает, а не про уведомления. Прежнее
// место не пустует молча — две строки ведут туда, где они теперь.
func (s *Server) whatToShow(add func(Component), st *store.Store) {
	add(Component{Type: "form", ID: "notify-settings", Title: "Что показывать",
		Fields: []Field{
			{Type: "picker", Name: "counter", Label: "Счётчик в меню", Options: levelOptions, Value: levelOption(st.Setting("notify.counter_level", "warn")), Hint: "сколько открытых считать"},
			{Type: "stepper", Name: "keep", Label: "Хранить «Сделано», дней", Value: strconv.Itoa(s.eng.NotifyKeepDays()), Min: 7, Max: 730, Step: 7},
			{Type: "text", Name: "snooze", Label: "На сколько откладывать", Hint: "кнопки «Отложить» у записи, через запятую; слова разбирает тот же разборщик сроков, что и напоминания",
				Value: strings.Join(s.eng.SnoozeOptions(), ", ")},
		},
		Actions: []Action{{Action: "set_notify_settings", Label: "Сохранить", Style: "primary"}}})
	add(Component{Type: "section", ID: "notify-moved", Title: "Переехало в «Работу Сёмы»", Children: []Component{
		{Type: "row", Layout: "wide", ID: "notify-moved-screen",
			Subtitle: "Провод, опрос экрана и часовой пояс",
			Caption:  "это про то, как Сёма работает, а не про уведомления; сами настройки остались прежними",
			Actions:  []Action{{Action: "$goto", Target: entityAddr(watchScreen, "", workTabScreen), Label: "Открыть", Style: "primary"}}},
		{Type: "row", Layout: "wide", ID: "notify-moved-chats",
			Subtitle: "Чаты команд, пинатель и хвост родителя в ветке",
			Caption:  "сколько живёт чат «Выполнить», когда Сёма сам продолжает начатое и сколько разговора он видит в ветке",
			Actions:  []Action{{Action: "$goto", Target: entityAddr(watchScreen, "", workTabChats), Label: "Открыть", Style: "primary"}}},
	}})
}

// mailRules — из каких писем рождаются уведомления.
func (s *Server) mailRules(add func(Component), st *store.Store) {
	rules, _ := st.NotifyRules()
	var ruleRows []Component
	for _, r := range rules {
		toggle := "off"
		if r.Enabled {
			toggle = "on"
		}
		kind := map[string]string{"due": "срок из письма", "noreply": "без ответа", "ics": "встреча .ics", "none": "не напоминать"}[r.Kind]
		mode := map[string]string{"now": "сразу", "suggest": "предложить"}[r.Mode]
		caption := fmt.Sprintf("%s · %s · за %d дн. · %s", r.Cond, kind, r.Days, mode)
		if r.About != "" {
			caption += "\n" + r.About
		}
		ruleRows = append(ruleRows, Component{Type: "row", Layout: "wide", ID: "rule-" + r.ID, Subtitle: r.Title, Toggle: toggle, Caption: caption,
			Actions: []Action{
				{Action: "toggle_notify_rule", Target: r.ID, Label: "toggle"},
				{Action: "notify_rule_days", Target: r.ID, Label: "Дни", Prompt: "За сколько дней напоминать", Value: strconv.Itoa(r.Days)},
				{Action: "delete_notify_rule", Target: r.ID, Label: "Удалить", Style: "destructive", Confirm: "Удалить правило «" + r.Title + "»?", ConfirmOK: "Удалить"},
			}})
	}
	add(Component{Type: "section", Title: "Правила почты · какие письма рождают уведомления", Children: ruleRows})
	add(Component{Type: "form", ID: "rule-new", Title: "Добавить правило",
		Fields: []Field{
			{Type: "text", Name: "title", Label: "Название", Value: ""},
			{Type: "text", Name: "cond", Label: "Условие", Hint: "group:receipts · sender:@bank.ru · words:оплатить, счёт · ics", Value: ""},
			{Type: "picker", Name: "kind", Label: "Что делать", Options: []string{"срок из письма", "без ответа", "встреча .ics", "не напоминать"}, Value: "срок из письма"},
			{Type: "stepper", Name: "days", Label: "За сколько дней", Value: "3", Min: 0, Max: 30, Step: 1},
		},
		Actions: []Action{{Action: "add_notify_rule", Label: "Добавить", Style: "primary"}}})
}

func hasNeed(needs []string, n string) bool {
	for _, x := range needs {
		if x == n {
			return true
		}
	}
	return false
}

// ---- действия ----

// holdsDecision — у записи есть действие, без которого работа не поедет:
// команда на сервере, смена отпечатка, черновик скилла, права модуля.
// Закрыть такую «прочитано» нельзя: решение никуда не денется, и запись
// вернётся сама.
func holdsDecision(n *store.Notification) bool {
	for _, a := range n.Actions {
		if a.Kind == "core" || a.Kind == "ssh" || a.Kind == "module" {
			return true
		}
	}
	return false
}

func (s *Server) notifyActions(req actionRequest) (string, error) {
	eng := s.eng
	st := eng.Store()
	if strings.HasPrefix(req.Action, "push_") || req.Action == "set_push_settings" || req.Action == "save_push_device" {
		return s.pushActions(req)
	}
	switch req.Action {
	case "notify_act":
		idText, idx := splitTarget(req.Target)
		id, _ := strconv.ParseInt(idText, 10, 64)
		i, _ := strconv.Atoi(idx)
		return "", eng.NotifyDecide(context.Background(), id, i, "человек")
	case "notify_chat":
		id, err := eng.NotifyChat(atoi64(req.Target))
		if err != nil {
			return "", err
		}
		return "chat:" + strconv.FormatInt(id, 10), nil
	case "notify_snooze":
		idText, spec := splitTarget(req.Target)
		id, _ := strconv.ParseInt(idText, 10, 64)
		_, err := eng.SnoozeNotify(id, spec)
		return "", err
	case "notify_state":
		idText, state := splitTarget(req.Target)
		id, _ := strconv.ParseInt(idText, 10, 64)
		if state == "open" {
			return "", st.SetNotificationState(id, store.NotifyOpen, "")
		}
		return "", st.SetNotificationState(id, store.NotifyDone, "человек")
	case "save_notify_channel":
		return s.saveNotifyChannel(req)
	case "toggle_notify_channel":
		c, ok := st.NotifyChannel(atoi64(req.Target))
		if !ok {
			return "", fmt.Errorf("канала нет")
		}
		c.Enabled = !c.Enabled
		return notifySettingsScreen, st.SaveNotifyChannel(c)
	case "notify_channel_level":
		c, ok := st.NotifyChannel(atoi64(req.Target))
		if !ok {
			return "", fmt.Errorf("канала нет")
		}
		c.MinLevel = levelFromOption(req.Params["value"])
		return notifySettingsScreen, st.SaveNotifyChannel(c)
	case "notify_channel_quiet":
		c, ok := st.NotifyChannel(atoi64(req.Target))
		if !ok {
			return "", fmt.Errorf("канала нет")
		}
		from, to, _ := strings.Cut(strings.TrimSpace(req.Params["value"]), "-")
		c.QuietFrom, c.QuietTo = strings.TrimSpace(from), strings.TrimSpace(to)
		if c.QuietFrom != "" {
			if _, err := time.Parse("15:04", c.QuietFrom); err != nil {
				return "", fmt.Errorf("время вида 23:00")
			}
		}
		if c.QuietTo != "" {
			if _, err := time.Parse("15:04", c.QuietTo); err != nil {
				return "", fmt.Errorf("время вида 8:00 или 08:00")
			}
		}
		return notifySettingsScreen, st.SaveNotifyChannel(c)
	case "notify_channel_title":
		c, ok := st.NotifyChannel(atoi64(req.Target))
		if !ok {
			return "", fmt.Errorf("канала нет")
		}
		title := strings.TrimSpace(req.Params["value"])
		if title == "" {
			return "", fmt.Errorf("у канала должно быть название")
		}
		c.Title = clipRunes(title, 80)
		return notifySettingsScreen, st.SaveNotifyChannel(c)
	case "notify_channel_sources":
		c, ok := st.NotifyChannel(atoi64(req.Target))
		if !ok {
			return "", fmt.Errorf("канала нет")
		}
		c.Sources = strings.ReplaceAll(strings.TrimSpace(req.Params["value"]), " ", "")
		return notifySettingsScreen, st.SaveNotifyChannel(c)
	case "delete_notify_channel":
		return notifySettingsScreen, st.DeleteNotifyChannel(atoi64(req.Target))
	case "add_notify_channel":
		c := &store.NotifyChannel{Kind: strings.TrimSpace(req.Params["kind"]), Title: strings.TrimSpace(req.Params["title"]), Enabled: true, MinLevel: "warn", Config: strings.TrimSpace(req.Params["config"])}
		if c.Title == "" {
			c.Title = c.Kind
		}
		return notifySettingsScreen, st.SaveNotifyChannel(c)
	case "test_notify_channel":
		c, ok := st.NotifyChannel(atoi64(req.Target))
		if !ok {
			return "", fmt.Errorf("канала нет")
		}
		if c.Kind == "web" {
			// у пушей проба идёт сразу на все устройства, а итог виден в
			// разделе устройств: у каждого — последняя доставка или отказ;
			// не принявшее называется ошибкой действия
			_, err := eng.TestPush(context.Background())
			return notifySettingsScreen, err
		}
		_, err := eng.Notify(&store.Notification{Key: "test:" + req.Target, Source: "core", Level: "alert",
			Title: "Проверка канала «" + c.Title + "»", Summary: "если вы это видите — канал работает",
			Actions: []store.NotifyAction{{Label: "Работает", Kind: "done", Style: "primary"}}})
		return notifySettingsScreen, err
	case "set_notify_settings":
		if v := req.Params["counter"]; v != "" {
			_ = st.SetSetting("notify.counter_level", levelFromOption(v))
		}
		if v := req.Params["keep"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 7 {
				_ = st.SetSetting("notify.keep_days", strconv.Itoa(n))
			}
		}
		if v, ok := req.Params["snooze"]; ok {
			if v = strings.TrimSpace(v); v != "" {
				_ = st.SetSetting("notify.snooze", v)
			}
		}
		// провод, опрос, чаты команд, хвост ветки и пояс переехали на «Работу
		// Сёмы» (`set_live_settings`, `set_chat_settings`, `set_timezone`);
		// прежнее имя действия и прежние параметры работают — скрипты и старые
		// карточки о переезде не знают. Пустое поле, как и раньше, значит «не
		// трогали»
		if err := s.saveWorkSettings(req); err != nil {
			return "", err
		}
		return notifySettingsScreen, nil
	case "toggle_notify_rule":
		r, ok := st.NotifyRule(req.Target)
		if !ok {
			return "", fmt.Errorf("правила нет")
		}
		r.Enabled = !r.Enabled
		return notifySettingsScreen, st.SaveNotifyRule(r)
	case "notify_rule_days":
		r, ok := st.NotifyRule(req.Target)
		if !ok {
			return "", fmt.Errorf("правила нет")
		}
		d, err := strconv.Atoi(strings.TrimSpace(req.Params["value"]))
		if err != nil || d < 0 {
			return "", fmt.Errorf("нужно число дней")
		}
		r.Days = d
		return notifySettingsScreen, st.SaveNotifyRule(r)
	case "delete_notify_rule":
		return notifySettingsScreen, st.DeleteNotifyRule(req.Target)
	case "add_notify_rule":
		kind := map[string]string{"срок из письма": "due", "без ответа": "noreply", "встреча .ics": "ics", "не напоминать": "none"}[req.Params["kind"]]
		if kind == "" {
			kind = "due"
		}
		cond := strings.TrimSpace(req.Params["cond"])
		if cond != "" && !strings.Contains(cond, ":") && cond != "ics" {
			if strings.Contains(cond, "@") || strings.Contains(cond, ".") {
				cond = "sender:" + cond
			} else {
				cond = "words:" + cond
			}
		}
		days, _ := strconv.Atoi(req.Params["days"])
		r := &store.NotifyRule{Title: strings.TrimSpace(req.Params["title"]), Cond: cond, Kind: kind, Days: days, Mode: "now", Sort: 100, Enabled: true}
		if r.Title == "" {
			return "", fmt.Errorf("нужно название правила")
		}
		return notifySettingsScreen, st.SaveNotifyRule(r)
	}
	return "", errUnhandled
}

func atoi64(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}
