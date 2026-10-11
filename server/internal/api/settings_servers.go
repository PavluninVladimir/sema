package api

// Раздел «Серверы»: список машин, общая самостоятельность и журнал всех
// команд. У каждой машины свой экран (settings_server.go) — адрес, ключ,
// отпечаток, уровень самостоятельности, окно, правила и журнал живут там, а
// не в строке списка: строка сервера несла три факта, четыре капсулы,
// тумблер, список, семь действий и «Подробнее» на восемь пар.
//
// Адреса (макет — design/nastroyki-2/Watch.html):
//
//	settings:servers                    раздел, вкладка «Машины»
//	settings:servers~auto               общая «Самостоятельность»
//	settings:servers~log                «Что делалось» — журнал всех серверов
//	settings:servers@new                «Добавить сервер»
//	settings:servers@<имя>              экран сервера, «Обзор»
//	settings:servers@<имя>~auto|rules|log
//
// Прежние адреса — `~log:<имя>`, `~rules`, `~rules:<имя>` — остались в
// уведомлениях и закладках и открываются тем же экраном (canonServersAddr).

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/host"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const serversScreen = "settings:servers"

// Внутренние вкладки. У раздела три: Машины, Самостоятельность, Что делалось;
// у сервера четыре: Обзор, Самостоятельность, Правила, Журнал. Правила и
// пределы у каждого сервера свои, поэтому у раздела вкладки «Правила» нет.
const (
	serversTabHosts = ""      // раздел: Машины; сервер: Обзор
	serversTabAuto  = "auto"  // Самостоятельность
	serversTabRules = "rules" // Правила сервера: разрешить, спросить, запретить
	serversTabLog   = "log"   // раздел: «Что делалось»; сервер: Журнал

	// serversNew — «сервер» с этим именем означает экран добавления
	serversNew = "new"
)

// serversAddr — разобранный адрес экрана «Серверов».
type serversAddr struct {
	Host string // имя сервера; пусто — раздел; «new» — «Добавить сервер»
	Tab  string // вкладка
	Try  string // команда проверки (base64) — только у «Правил» сервера
}

// parseServersAddr — адрес экрана на сервер, вкладку и команду проверки.
// Адрес уже канонический (canonServersAddr).
func parseServersAddr(screen string) serversAddr {
	_, id, tab := splitEntity(screen)
	tab, try, _ := strings.Cut(tab, rulesTry)
	return serversAddr{Host: strings.TrimSpace(id), Tab: tab, Try: try}
}

// canonServersAddr — адрес по нынешним правилам. Прежние хвосты
// («~log:имя», «~rules», «~rules:имя~try:…») помнят уведомления, закладки и
// чужие журналы, и они должны открывать то же, что открывали: экран сервера
// на нужной вкладке. Остальные адреса возвращаются как есть.
func (s *Server) canonServersAddr(screen string) string {
	section, id, tab := splitEntity(screen)
	if section != serversScreen || id != "" || tab == "" {
		return screen
	}
	if name, ok := strings.CutPrefix(tab, serversTabLog+":"); ok {
		if name = strings.TrimSpace(name); name != "" {
			return entityAddr(serversScreen, name, serversTabLog)
		}
		return entityAddr(serversScreen, "", serversTabLog)
	}
	if tab == serversTabRules || strings.HasPrefix(tab, serversTabRules+":") || strings.HasPrefix(tab, serversTabRules+rulesTry) {
		rest := strings.TrimPrefix(strings.TrimPrefix(tab, serversTabRules), ":")
		name, try, _ := strings.Cut(rest, rulesTry)
		name = strings.TrimSpace(name)
		if name == "" {
			// сервер не назван: правила у каждого свои, и первым открывается
			// первый по имени; серверов нет — список
			hosts, err := s.eng.Store().Hosts()
			if err != nil || len(hosts) == 0 {
				return serversScreen
			}
			name = hosts[0].Name
		}
		addr := entityAddr(serversScreen, name, serversTabRules)
		if try != "" {
			addr += rulesTry + try
		}
		return addr
	}
	return screen
}

// stayOnServers — после действия остаёмся там, где нажали: экран сервера не
// должен уступать место списку из-за кнопки «Проверить связь».
func stayOnServers(req actionRequest, def string) string {
	if strings.HasPrefix(req.Screen, serversScreen) {
		return req.Screen
	}
	return def
}

func (s *Server) buildServers(doc *Document, screen, filter string, limit int) error {
	a := parseServersAddr(screen)
	doc.Layout = "wide"
	switch {
	case a.Host == serversNew:
		return s.buildServerNew(doc)
	case a.Host != "":
		return s.buildServer(doc, a, filter, limit)
	}
	return s.buildServersSection(doc, a.Tab, filter, limit)
}

// buildServersSection — раздел: Машины · Самостоятельность · Что делалось.
// «Добавить сервер» — единственная кнопка шапки раздела и стоит на всех
// вкладках: шапка не меняется от вкладки к вкладке.
func (s *Server) buildServersSection(doc *Document, tab, filter string, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Серверы"
	hosts, err := st.Hosts()
	if err != nil {
		return err
	}
	doc.Subheading = hostsByPhrase(s, hosts)
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	tabs := []innerTab{
		{ID: serversTabHosts, Name: "Машины", Count: strconv.Itoa(len(hosts))},
		{ID: serversTabAuto, Name: "Самостоятельность"},
		{ID: serversTabLog, Name: "Что делалось"},
	}
	// команда ждёт кнопки — это видно до захода во вкладку: иначе она ждёт
	// ровно столько, сколько человек её не ищет
	if n := s.waitingCount(""); n > 0 {
		tabs[0].Count, tabs[0].Level = strconv.Itoa(n)+" ждут", LevelAlert
	}
	setSubtabs(doc, serversScreen, tab, tabs)

	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(serversScreen, serversNew, ""),
		Label: "Добавить сервер", Style: "primary"}}
	// машина, где работает Сёма, заводится с экрана добавления: из меню шапки
	// к ней ведёт строка, пока её нет
	if _, has := st.Host(engine.HostOwnerName); !has {
		doc.ToolbarMenu = []Action{{Action: "$goto", Target: entityAddr(serversScreen, serversNew, ""),
			Label: "Подключить хозяина…"}}
	}

	switch tab {
	case serversTabAuto:
		s.autonomy(add, hosts)
		return nil
	case serversTabLog:
		return s.serversJournal(doc, "", filter, limit)
	}

	// то, что ждёт кнопки, стоит вверху: раньше это лежало под списком
	// серверов и скиллами, и человек находил его, пролистав экран
	if shelf := s.serversShelf(""); shelf != nil {
		add(*shelf)
	}
	rows := make([]Component, 0, len(hosts))
	for _, h := range hosts {
		rows = append(rows, s.hostRow(h))
	}
	if len(rows) == 0 {
		empty := emptyState("Серверов пока нет",
			"Сервер — это машина, куда Сёма ходит по SSH: проверить место на диске, посмотреть журнал, поднять службу. Ключ берётся из хранилища, пароли Сёма не хранит и не умеет", "tray")
		empty.Actions = []Action{{Action: "$goto", Target: entityAddr(serversScreen, serversNew, ""), Label: "Добавить сервер", Style: "primary"}}
		rows = append(rows, empty)
	}
	add(Component{Type: "section", Title: "Машины · " + strconv.Itoa(len(hosts)), Children: rows})
	add(Component{Type: "section", ID: "servers-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Паролей Сёма не хранит и не умеет — только ключи из хранилища; приватная часть не выдаётся ни модулю, ни модели, а каждое её чтение видно в журнале ключа. " +
			"Отпечаток сервера запоминается при первом входе, и если он сменится — Сёма откажется входить, пока вы не подтвердите новый. " +
			"«Выполнить и разрешить впредь» заводит этому серверу правило «Разрешить» на команду целиком, а не на первое слово: «apt-get» открыл бы и «apt-get remove»."}}})
	return nil
}

// hostTrouble — что с машиной не так: слово для капсулы и фраза для строки.
// Пусто — всё в порядке: норма молчит.
func hostTrouble(h *store.Host) (pill, phrase string) {
	switch {
	case h.NewFP != "":
		return "отпечаток сменился", "команды не выполняются, пока вы не примете новый отпечаток"
	case h.LastErr != "":
		return "не отвечает", clipRunes(h.LastErr, 120)
	case h.Paused != "":
		return "остановлен", "самостоятельность остановлена: " + h.Paused
	}
	return "", ""
}

// hostTile — буква и цвет плитки: род машины отличает её глазом, а «хозяин»
// стоит особняком.
func hostTile(h *store.Host) (letter, color string) {
	letter = firstRuneOfName(h.Name)
	switch {
	case h.Name == engine.HostOwnerName:
		return "⌂", "#2E9A5B"
	case h.Kind == store.HostRouterOS:
		return letter, "#17181C"
	case h.Kind == store.HostLinux:
		return letter, "#2F6FEB"
	}
	return letter, "#8E8E93"
}

// hostAddr — «пользователь@адрес»; порт — только если он не 22.
func hostAddr(h *store.Host) string {
	addr := h.User + "@" + h.Addr
	if h.Port != 22 && h.Port != 0 {
		addr += ":" + strconv.Itoa(h.Port)
	}
	return addr
}

// hostWindowWord — окно словами: пусто значит круглые сутки.
func hostWindowWord(h *store.Host) string {
	if strings.TrimSpace(h.Window) == "" {
		return "круглые сутки"
	}
	return strings.Replace(h.Window, "-", "–", 1)
}

// hostPrimary — единственная кнопка строки и шапки сервера: то, что сейчас
// нужно сделать. Сменился отпечаток — принять или тревожиться; остановлен —
// продолжить; иначе — проверить связь.
func hostPrimary(h *store.Host) Action {
	switch {
	case h.NewFP != "":
		return Action{Action: "accept_fingerprint", Target: h.Name, Label: "Принять новый отпечаток…", Style: "primary",
			Confirm: "Принять новый отпечаток " + h.Name + "? Так бывает после переустановки сервера; если вы его не переустанавливали — это тревога.", ConfirmOK: "Принять"}
	case h.Paused != "":
		return Action{Action: "resume_host", Target: h.Name, Label: "Продолжить", Style: "primary",
			Confirm: "Разобрались, что случилось на «" + h.Name + "»? Тогда Сёма снова сможет менять его сам.", ConfirmOK: "Продолжить"}
	}
	return Action{Action: "check_host", Target: h.Name, Label: "Проверить связь"}
}

// hostRow — тонкая строка списка: имя, не больше двух капсул, одна фраза,
// одна кнопка, «···». Вся строка ведёт на экран сервера. Капсула — только
// если отличает: тревога, род, уровень, «без sudo» — в таком порядке, и
// остаются две первые.
func (s *Server) hostRow(h *store.Host) Component {
	letter, color := hostTile(h)
	var pills []Badge
	trouble, phrase := hostTrouble(h)
	if trouble != "" {
		pills = append(pills, Badge{Text: trouble, Level: LevelAlert})
	}
	switch h.Kind {
	case store.HostRouterOS:
		pills = append(pills, Badge{Text: "RouterOS"})
	case store.HostLinux:
		pills = append(pills, Badge{Text: "Linux"})
	}
	switch {
	case h.Full():
		pills = append(pills, Badge{Text: "полная", Level: LevelWarn})
	case h.Auto:
		pills = append(pills, Badge{Text: "под судом"})
	}
	if h.NoSudo {
		pills = append(pills, Badge{Text: "без sudo"})
	}
	if len(pills) > 2 {
		pills = pills[:2]
	}

	// одна фраза: беда, адрес, последняя команда, окно. Сменившийся отпечаток
	// и остановка говорят ещё и уровень: капсулы для него не осталось
	parts := []string{}
	if phrase != "" {
		parts = append(parts, phrase)
	}
	parts = append(parts, hostAddr(h))
	if h.LastCmd != "" {
		parts = append(parts, "последняя команда "+whenText(h.LastAt))
	}
	if h.Auto && h.Paused == "" {
		parts = append(parts, "окно "+hostWindowWord(h))
	}
	if h.Auto && trouble != "" && h.Paused == "" {
		parts = append(parts, map[bool]string{true: "полная самостоятельность", false: "под судом"}[h.Full()])
	}
	if !h.Auto && h.Name == engine.HostOwnerName {
		parts = append(parts, "машина, где работает Сёма")
	}
	// сервер без правил запрещает всё, и об этом нельзя узнавать, зайдя в него
	if s.eng.Store().HostRuleSet(h.Name).Empty() {
		parts = append(parts, "нет правил — любая команда запрещена")
	}

	row := Component{Type: "row", Layout: "wide", ID: "host-" + h.Name, Subtitle: h.Name,
		Avatar: letter, AvatarColor: color, AvatarShape: "square",
		Caption: strings.Join(parts, " · "), Badges: pills}
	if trouble != "" {
		withLevel(&row, LevelAlert)
	}
	primary := hostPrimary(h)
	row.Actions = []Action{{Action: "$goto", Target: entityAddr(serversScreen, h.Name, ""), Label: h.Name}, primary}
	if primary.Action != "check_host" {
		row.Menu = append(row.Menu, Action{Action: "check_host", Target: h.Name, Label: "Проверить связь"})
	}
	row.Menu = append(row.Menu,
		Action{Action: "$goto", Target: entityAddr(serversScreen, h.Name, serversTabRules), Label: "Правила"},
		Action{Action: "$goto", Target: entityAddr(serversScreen, h.Name, serversTabLog), Label: "Журнал"})
	return row
}

// waitingCount — сколько решений ждёт человека у серверов: команды на кнопке
// и команды без правила. Пустое имя — у всех серверов.
func (s *Server) waitingCount(host string) int {
	st := s.eng.Store()
	n := 0
	pending, _ := st.SSHPendingList()
	for _, p := range pending {
		if host == "" || p.Host == host {
			n++
		}
	}
	for _, r := range st.SSHNoRuleList(200) {
		if host == "" || r.Host == host {
			n++
		}
	}
	return n
}

// serversShelf — полка «Ждут вас»: команды на подтверждение, команды без
// правила и расхождение окна с временем прихода команд. Всё, что не поедет
// без человека, одним тёплым блоком. Пустое имя — у всех серверов; нет
// ничего — полки нет.
func (s *Server) serversShelf(host string) *Component {
	rows, commands := s.pendingRows(host)
	noRule, ruleCount := s.noRuleRows(host)
	rows = append(rows, noRule...)
	// замечание об окне стоит после команд: кнопки нужнее, чем объяснение
	rows = append(rows, s.mismatchRows(host)...)
	waiting := commands + ruleCount
	if waiting == 0 {
		return nil
	}
	shelf := Component{Type: "shelf", ID: "servers-shelf", Title: "Ждут вас",
		Meta:     plural(waiting, "команда", "команды", "команд") + " · до нажатия на сервер ничего не уходит",
		Children: rows}
	// Команда, пролежавшая неделю, перестаёт значить то, что значила:
	// «sudo kill 694563» с номером процесса восьмидневной давности —
	// сегодня либо ничто, либо совсем другой процесс. Разбирать такой
	// хвост по одной кнопке не работа, а очередь из сорока семи нажатий.
	// Ничего не выполняется: всё закрывается как отклонённое.
	if commands > 1 {
		shelf.Actions = []Action{{
			Action: "reject_all_ssh", Target: host, Label: "Снять все с очереди", Style: "destructive",
			Confirm: fmt.Sprintf("Снять с очереди %s? Ни одна не выполнится — они просто перестанут ждать. "+
				"Нужные Сёма попросит заново, когда они снова понадобятся.",
				plural(commands, "команду", "команды", "команд")),
			ConfirmOK: "Снять все",
		}}
	}
	return &shelf
}

// pendingRows — команды на подтверждение, и сколько их. Одна и та же команда
// на трёх серверах — это одно решение человека, а не три: такие строки
// сворачиваются в одну, а кнопки остаются у каждой внутри. Пустое имя — у
// всех серверов.
func (s *Server) pendingRows(host string) ([]Component, int) {
	st := s.eng.Store()
	all, _ := st.SSHPendingList()
	var pending []*store.SSHPending
	for _, p := range all {
		if host == "" || p.Host == host {
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		return nil, 0
	}
	// вГруппе: та же команда на нескольких серверах — текст её уже стоит
	// в заголовке группы, и повторять его у каждого сервера значит
	// показать простыню трижды. Внутри группы строка называется сервером
	строка := func(p *store.SSHPending, вГруппе bool) Component {
		ruling := s.eng.Decide(p.Host, p.Cmd)
		id := strconv.FormatInt(p.ID, 10)
		title, caption := p.Cmd, p.Host+" · просил "+p.Who+" · "+whenText(p.Created)+" · "+ruling.Why
		if вГруппе {
			title, caption = p.Host, "просил "+p.Who+" · "+whenText(p.Created)
		}
		// «Выполнить» и «Отклонить» нужны оба; «впредь без вопроса» — правило
		// «Разрешить» этому серверу, и в меню: sudo и неясный синтаксис
		// разрешить нельзя, поэтому кнопки у них нет
		actions := []Action{
			{Action: "approve_ssh", Target: id, Label: "Выполнить", Style: "primary"},
			{Action: "reject_ssh", Target: id, Label: "Отклонить"},
		}
		if ruling.CanAllow() {
			actions = append(actions, Action{Action: "approve_ssh_read", Target: id, Label: "Выполнить и разрешить впредь"})
		}
		return Component{Type: "row", Layout: "wide", ID: "pending-" + id, Subtitle: title, Caption: caption, Actions: actions}
	}
	var порядок []string
	кучки := map[string][]*store.SSHPending{}
	for _, p := range pending {
		if _, before := кучки[p.Cmd]; !before {
			порядок = append(порядок, p.Cmd)
		}
		кучки[p.Cmd] = append(кучки[p.Cmd], p)
	}
	var rows []Component
	for _, cmd := range порядок {
		кучка := кучки[cmd]
		if len(кучка) == 1 {
			rows = append(rows, строка(кучка[0], false))
			continue
		}
		var where []string
		children := make([]Component, 0, len(кучка))
		for _, p := range кучка {
			where = append(where, p.Host)
			children = append(children, строка(p, true))
		}
		why := s.eng.Decide(кучка[0].Host, cmd).Why
		rows = append(rows, Component{Type: "rowgroup", ID: "pending-same-" + cmd,
			Subtitle: cmd,
			Caption: "одна и та же команда на " + plural(len(кучка), "сервере", "серверах", "серверах") +
				" — " + strings.Join(where, ", ") + ". " + why + ". Подтверждать нужно каждую: кнопки внутри",
			Children: children})
	}
	return rows, len(pending)
}

// mismatchRows — расхождение окна и времени, в которое команды приходят: две
// настройки вместе значат «самостоятельной работы не будет никогда», и
// человек видит только «сейчас не окно» — но не то, что окно не наступит и
// завтра. Строка одна на сервер; пустое имя — у всех серверов.
func (s *Server) mismatchRows(host string) []Component {
	all, _ := s.eng.Store().SSHPendingList()
	var rows []Component
	сказано := map[string]bool{}
	for _, p := range all {
		if (host != "" && p.Host != host) || сказано[p.Host] {
			continue
		}
		сказано[p.Host] = true
		if строка := s.eng.WindowMismatch(p.Host); строка != "" {
			rows = append(rows, Component{Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn),
				ID:       "окно-" + p.Host,
				Subtitle: "«" + p.Host + "»: окно и время команд не совпадают",
				Caption:  строка + ". Поменяйте окно сервера или час задачи, которая их присылает",
				Actions: []Action{
					{Action: "$goto", Target: entityAddr(serversScreen, p.Host, serversTabAuto), Label: "Окно сервера"},
				}})
		}
	}
	return rows
}

// noRuleRows — команды без правила. Запрещены по умолчанию: ни одно правило
// сервера для них не подошло. Две кнопки — завести «Спросить» (после неё
// будет обычное «Выполнить») или «Разрешить» (если команду вообще можно
// разрешить), а «Оставить запрещённой» закрывает запрос без правила.
func (s *Server) noRuleRows(host string) ([]Component, int) {
	var rows []Component
	for _, n := range s.eng.Store().SSHNoRuleList(100) {
		if host != "" && n.Host != host {
			continue
		}
		id := strconv.FormatInt(n.ID, 10)
		row := Component{Type: "row", Layout: "wide", ID: "norule-" + id, Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: n.Cmd,
			Caption: n.Host + " · просил " + n.Who + " · " + whenText(n.Created) +
				" · нет правила для этой команды — она не выполнена. " + n.Why,
			Actions: []Action{{Action: "rule_ask", Target: id, Label: "Спрашивать для неё", Style: "primary"}}}
		if n.CanAllow {
			row.Actions = append(row.Actions, Action{Action: "rule_allow", Target: id, Label: "Разрешить её"})
		}
		row.Actions = append(row.Actions,
			Action{Action: "$goto", Target: rulesAddr(n.Host, ""), Label: "Правила сервера"},
			Action{Action: "rule_dismiss", Target: id, Label: "Оставить запрещённой", Style: "destructive"})
		rows = append(rows, row)
	}
	return rows, len(rows)
}

// autonomy — общая «Самостоятельность»: то, что касается всех машин сразу.
// Уровень, окно и потолок каждой машины — на её экране.
func (s *Server) autonomy(add func(Component), hosts []*store.Host) {
	// остановленные — первыми: самостоятельность стоит, и ждёт этого человек
	var stopped []Component
	autoCount := 0
	for _, h := range hosts {
		if h.Auto {
			autoCount++
		}
		if h.Paused != "" {
			stopped = append(stopped, Component{Type: "row", Layout: "wide", Level: LevelAlert, Dot: levelDot(LevelAlert),
				Subtitle: h.Name + " · самостоятельность остановлена", Caption: h.Paused,
				Actions: []Action{
					{Action: "resume_host", Target: h.Name, Label: "Продолжить", Style: "primary",
						Confirm: "Разобрались, что случилось на «" + h.Name + "»? Тогда Сёма снова сможет менять его сам.", ConfirmOK: "Продолжить"},
					{Action: "$goto", Target: entityAddr(serversScreen, h.Name, serversTabAuto), Label: h.Name}}})
		}
	}
	if len(stopped) > 0 {
		add(Component{Type: "section", Title: "Остановлены · " + strconv.Itoa(len(stopped)), Children: stopped})
	}

	// ---- ночная смена: смотрит или делает ----
	//
	// Переключатель жил только настройкой в базе. Право выполнять —
	// решение человека, и снимать его он должен там же, где выдал, а не
	// просьбой в чате: если ночью что-то пойдёт не так, искать ключ в
	// настройках некогда.
	холостая := s.eng.NightDryRun()
	строка := Component{Type: "row", Layout: "wide", ID: "ночь-вхолостую",
		Subtitle: "Ночная смена выполняет наряд", Toggle: boolToggle(!холостая),
		Actions: []Action{{Action: "toggle_night_dry", Label: "toggle"}},
		// тумблер называет «выполняет», а настройка — «вхолостую»
		key: engine.NightDryRunKey, keyInverted: true}
	if холостая {
		строка.Caption = "сейчас вхолостую: наряд собирается и целиком показывается в отчёте, но ни один шаг не выполняется. " +
			"Осмотр при этом идёт по-настоящему — читающие команды выполняются всегда. " +
			"Неделю смотрите, что Сёма собирался делать ночью, и по этому решаете, давать ли ему руки"
	} else {
		строка.Level, строка.Dot = LevelWarn, levelDot(LevelWarn)
		строка.Caption = "Сёма выполняет шаги наряда сам: в своё окно, через суд двух моделей и со страховкой возврата. " +
			"«Запретить» и sudo на машине без прав остаются запретом при любом уровне"
		строка.Facts = []Prop{
			{Label: "Срок возврата", Value: plural(s.eng.InsuranceMinutes(), "минута", "минуты", "минут")},
			{Label: "Шагов за смену", Value: strconv.Itoa(s.eng.NightMaxSteps())},
		}
	}
	rows := []Component{строка}

	// ---- кто сколько ведёт сам ----
	if len(hosts) > 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "auto-count",
			Subtitle: "Сёма ведёт сам",
			Caption:  "уровень, окно и потолок — у каждой машины на её экране",
			Badges:   []Badge{{Text: fmt.Sprintf("%d из %d", autoCount, len(hosts))}}})
	}

	// ---- суд ----
	арбитр, судья := s.eng.Role(engine.RoleArbiter).Model, s.eng.Role(engine.RoleJudge).Model
	court := Component{Type: "row", Layout: "wide", ID: "auto-court", Subtitle: "Суд двух моделей",
		Caption: "изменяющую команду разбирает арбитр, вердикт выносит судья; любая заминка читается как «спросить вас»"}
	if ready, why := s.eng.CourtReady(); ready {
		court.Badges = []Badge{{Text: арбитр + " → " + судья + " · разные", Level: LevelOK}}
		court.Actions = []Action{{Action: "$goto", Target: modelsScreen, Label: "К ролям"}}
	} else {
		court.Badges = []Badge{{Text: "не собран", Level: LevelWarn}}
		court.Caption = why + ". Пока так, изменяющие команды на серверах идут вам на кнопку, а самостоятельное управление не включится: " +
			"одна модель, проверяющая сама себя, — это не проверка"
		withLevel(&court, LevelWarn)
		court.Actions = []Action{{Action: "$goto", Target: modelsScreen, Label: "К ролям", Style: "primary"}}
	}
	rows = append(rows, court)

	// ---- скиллы администрирования ----
	var skillNames []string
	for _, k := range s.eng.Skills() {
		if k.Enabled && k.Group == "Администрирование" {
			skillNames = append(skillNames, "«"+k.Title+"»")
		}
	}
	if len(skillNames) > 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "auto-skills", Subtitle: "Скиллы администрирования",
			Caption: strings.Join(skillNames, ", ") + " — по ним Сёма осматривает сервер и пишет наряд; правятся на «Скиллах» или просьбой в чате",
			Actions: []Action{{Action: "$goto", Target: skillsScreen, Label: "Открыть скиллы"}}})
	}
	add(Component{Type: "section", Title: "Общее для всех машин", Children: rows})

	// пределы — и при серверах, и без них: это настройки Сёмы, а не серверов
	s.autonomyLimits(add)

	add(Component{Type: "section", ID: "auto-how", Title: "Что сильнее суда", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "«Запретить» отказывает сразу и на любом уровне; на «полной» правило «Спросить» (ключи, пользователи, файрвол, сеть, удаление пакетов, сброс настроек) " +
			"идёт на суд только со страховкой возврата; команда, пришедшая снаружи — например из письма, — идёт вам, кто бы её ни просил. " +
			"Что не подошло ни под одно правило сервера, запрещено. После выполнения Сёма гоняет пробу; не сошлось — сервер останавливается и приходит тревога."}}})

	// стоп-кран — «Опасное»: внизу своей карточкой, с подтверждением. Он не
	// возвращает серверы на ручное, а останавливает самостоятельность у каждого
	// (`paused`): уровень остаётся, и сервер возвращается в работу кнопкой
	// «Продолжить»
	if len(hosts) > 0 {
		add(Component{Type: "section", ID: "auto-danger", Title: "Опасное", Style: "danger", Children: []Component{{
			Type: "row", Layout: "wide", ID: "danger-stop-all", Subtitle: "Остановить самостоятельность на всех машинах",
			Caption: "Все изменения снова ждут вашей кнопки; уровни машин остаются, и каждая возвращается в работу кнопкой «Продолжить»",
			Actions: []Action{{Action: "stop_all_auto", Label: "Остановить все…", Style: "destructive",
				Confirm:   "Остановить самостоятельность на всех серверах? Все изменения снова будут ждать вашей кнопки, пока вы не нажмёте «Продолжить» у каждого.",
				ConfirmOK: "Остановить"}}}}})
	}
}

// autonomyLimits — «Тонкая настройка» вкладки «Самостоятельность»: пределы
// ночной смены, страховки возврата и SSH, одиннадцать параметров. Раньше
// ночные и страховка жили только в базе (правились просьбой в чате), а три
// предела SSH — выпадающими списками на вкладке «Правила», где им не место:
// правилами они не были. Умолчания и границы — из реестра настроек.
func (s *Server) autonomyLimits(add func(Component)) {
	skillField := func(key, name, label, hint string) Field {
		f := s.settingField(key, name, label, hint, "")
		f.Type, f.Options = "picker", s.skillChoices(f.Value, *f.Default)
		return f
	}
	add(Component{Type: "form", ID: "night-limits", Title: "Ночная смена", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("night.max_steps", "max_steps", "Шагов за смену, не больше",
				"Пустой наряд — нормальный итог: выдуманная ночью работа хуже несделанной", ""),
			s.settingField("night.look_max", "look_max", "Команд осмотра",
				"Читающие команды из скилла; круг чтения здесь и фильтр, и охрана", ""),
			s.settingField("night.look_timeout_s", "look_timeout_s", "Осмотр не дольше", "На весь осмотр сразу", "с"),
			s.settingField("night.every_h", "every_h", "Смена раз в", "У серверов без окна: с окном смена одна на окно", "ч"),
			skillField("night.skill.linux", "skill_linux", "Скилл осмотра для Linux",
				"По нему Сёма осматривает сервер и пишет наряд; правится на «Скиллах»"),
			skillField("night.skill.routeros", "skill_routeros", "Скилл осмотра для RouterOS", ""),
		},
		Submit: &Action{Action: "set_night_limits", Label: "Сохранить"}})
	add(Component{Type: "form", ID: "auto-limits", Title: "Страховка и потолок", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("auto.rollback_min", "rollback_min", "Срок страховки возврата",
				"Перед командой из круга «Спросить» на сервере заводится отложенный возврат; связь вернулась и проба сошлась — страховка снимается, иначе сервер вернёт себя сам через этот срок", "мин"),
			s.settingField("auto.max_hour", "max_hour", "Потолок изменений в час по умолчанию", "У сервера может быть свой", ""),
		},
		Submit: &Action{Action: "set_auto_limits", Label: "Сохранить"}})
	add(Component{Type: "form", ID: "ssh-limits", Title: "SSH", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("ssh.timeout_s", "timeout_s", "Таймаут чтения",
				"Суд и ночная смена работают с ним — долгое остаётся человеку", "с"),
			s.settingField("ssh.confirm_timeout_s", "confirm_timeout_s", "Таймаут подтверждённой команды",
				"Только у команды, подтверждённой вами: apt upgrade в минуту не укладывается", "с"),
			s.settingField("ssh.max_output", "max_output", "Предел вывода",
				"Вывод проходит маски секретов из «Памяти»", "симв."),
		},
		Submit: &Action{Action: "set_ssh_limits", Label: "Сохранить"}})
}

// skillChoices — из чего выбирать скилл осмотра: скиллы «Администрирования» и
// те два имени, что уже стоят (нынешнее и умолчание), даже если такого скилла
// нет: выпадашка без своего значения молча подменила бы его первым в списке.
func (s *Server) skillChoices(current, def string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	add(current)
	add(def)
	for _, k := range s.eng.Skills() {
		if k.Group == "Администрирование" {
			add(k.Name)
		}
	}
	return out
}

// hostsByPhrase — строка под заголовком: сколько машин, на скольких Сёма
// может что-то менять сам и где ему нарочно не дан root.
func hostsByPhrase(s *Server, hosts []*store.Host) string {
	if len(hosts) == 0 {
		return "Ни одного сервера. Сёма ходит по SSH только с ключом из хранилища — пароли он не хранит и не умеет."
	}
	сам, безSudo := 0, 0
	for _, h := range hosts {
		if h.Auto {
			сам++
		}
		if h.NoSudo {
			безSudo++
		}
	}
	фраза := plural(len(hosts), "машина", "машины", "машин") + ". "
	switch {
	case сам == 0:
		фраза += "Изменения на всех ждут вашей кнопки."
	case сам == len(hosts):
		фраза += "На всех Сёма может менять что-то сам, посоветовавшись с двумя моделями."
	default:
		фраза += fmt.Sprintf("На %d Сёма может менять что-то сам, посоветовавшись с двумя моделями; остальные — только вы.", сам)
	}
	if безSudo > 0 {
		фраза += fmt.Sprintf(" У %s sudo запрещён.", plural(безSudo, "машины", "машин", "машин"))
	}
	if n := hostsWithoutRules(s.eng.Store(), hosts); n > 0 {
		фраза += fmt.Sprintf(" У %s нет правил: любая команда там запрещена.", plural(n, "машины", "машин", "машин"))
	}
	return фраза
}

func shortFP(fp string) string {
	fp = strings.TrimPrefix(fp, "SHA256:")
	if len(fp) > 12 {
		return "SHA256:" + fp[:6] + "…" + fp[len(fp)-4:]
	}
	return fp
}

// serversActions — действия раздела «Серверы» и экрана сервера.
func (s *Server) serversActions(req actionRequest) (string, error) {
	st := s.eng.Store()
	ctx := context.Background()
	switch req.Action {
	case "add_host":
		name := strings.TrimSpace(req.Params["name"])
		if name == "" || strings.TrimSpace(req.Params["addr"]) == "" {
			return "", fmt.Errorf("нужны имя и адрес")
		}
		// имя едет в адресе экрана («settings:servers@имя~вкладка»): знаки,
		// которыми адрес делится, в нём недопустимы
		if strings.ContainsAny(name, "@~") {
			return "", fmt.Errorf("в имени сервера не бывает «@» и «~»: ими делится адрес экрана")
		}
		// тот же адрес правит существующий сервер: заведение молча переписало
		// бы его адрес и ключ, а теперь правка есть на экране сервера
		if _, exists := st.Host(name); exists {
			return "", fmt.Errorf("сервер «%s» уже есть — откройте его и поправьте там", name)
		}
		port := 22
		if v := strings.TrimSpace(req.Params["port"]); v != "" {
			p, err := parsePort(v)
			if err != nil {
				return "", err
			}
			port = p
		}
		h := &store.Host{Name: name, Addr: strings.TrimSpace(req.Params["addr"]), Port: port,
			User: strings.TrimSpace(req.Params["user"]), Key: req.Params["key"],
			// как секреты и серверы: невыданное не действует. sudo
			// разрешают поимённо пунктом строки, а не по умолчанию
			NoSudo: req.Params["sudo"] != "true"}
		if h.User == "" {
			h.User = "root"
		}
		if err := st.SaveHost(h); err != nil {
			return "", err
		}
		go func() {
			if _, err := s.eng.CheckHost(ctx, name); err != nil {
				fmt.Println("ssh: проверка связи", name+":", err)
			}
		}()
		return entityAddr(serversScreen, name, ""), nil
	case "save_host":
		return s.saveHost(req)
	case "add_home_host":
		// имя пользователя приходит полем «Подключить хозяина» или, как
		// раньше, ответом на вопрос
		user := strings.TrimSpace(req.Params["user"])
		if user == "" {
			user = strings.TrimSpace(req.Params["value"])
		}
		if user == "" {
			return "", fmt.Errorf("нужно имя пользователя на машине")
		}
		const keyName = "ssh:хозяин"
		public := ""
		if sec, ok := st.Secret(keyName); ok {
			public = sec.Public
		} else {
			p, err := s.eng.GenerateSSHKey(keyName)
			if err != nil {
				return "", err
			}
			public = p
		}
		addr := "127.0.0.1"
		if host.InContainer() {
			if gw := engine.HostGateway(); gw != "" {
				addr = gw
			}
		}
		// машина, где живёт сам Сёма: root здесь не даётся, пока человек не
		// разрешит явно — сломать её значит остаться без Сёмы
		if err := st.SaveHost(&store.Host{Name: engine.HostOwnerName, Addr: addr, Port: 22, User: user, Key: keyName, Kind: "linux", NoSudo: true}); err != nil {
			return "", err
		}
		_, _ = s.eng.Notify(&store.Notification{
			Key: "home-host", Source: "core", Level: "info",
			Title:   "Хозяин Сёмы: добавьте ключ на машину",
			Summary: "На машине хозяина выполните от пользователя «" + user + "»: echo '" + public + "' >> ~/.ssh/authorized_keys — и нажмите «Проверить связь» у сервера «хозяин» на «Серверах». Адрес " + addr + " — это хозяин, каким его видит Сёма из контейнера.",
			Fields:  []store.NotifyField{{Label: "Публичный ключ", Value: public}, {Label: "Адрес", Value: addr + ":22"}, {Label: "Пользователь", Value: user}},
			Actions: []store.NotifyAction{{Label: "Серверы", Kind: "goto", Ref: entityAddr(serversScreen, engine.HostOwnerName, "")}},
		})
		return entityAddr(serversScreen, engine.HostOwnerName, ""), nil
	case "check_host":
		go func() { _, _ = s.eng.CheckHost(ctx, req.Target) }()
		return stayOnServers(req, serversScreen), nil
	case "accept_fingerprint":
		return stayOnServers(req, serversScreen), s.eng.AcceptFingerprint(req.Target)
	case "toggle_host_sudo":
		h, ok := st.Host(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		h.NoSudo = !h.NoSudo
		return stayOnServers(req, serversScreen), st.SaveHost(h)
	case "set_host_autonomy":
		return s.saveHostAutonomy(req)
	case "toggle_host_auto":
		h, ok := st.Host(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		// тумблер переключает между ручным и «под судом»; «полная»
		// ставится отдельным действием и словами
		if h.Auto {
			h.Autonomy, h.Auto, h.Paused = store.AutonomyOff, false, "" // Auto — вместе с уровнем, иначе SaveHost вернёт «под судом»
		} else {
			if ready, why := s.eng.CourtReady(); !ready {
				return "", fmt.Errorf("%s — сначала назначьте роли «Арбитр» и «Судья» разным моделям", why)
			}
			h.Autonomy, h.Auto, h.Paused, h.AutoFrom = store.AutonomyCourt, true, "", time.Now().Format(time.RFC3339)
		}
		return stayOnServers(req, serversScreen), st.SaveHost(h)
	case "set_host_window":
		h, ok := st.Host(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		w := strings.TrimSpace(req.Params["value"])
		if w != "" && !engine.ValidWindow(w) {
			return "", fmt.Errorf("окно пишется как 02:00-06:00")
		}
		h.Window = w
		return stayOnServers(req, serversScreen), st.SaveHost(h)
	case "set_host_max":
		h, ok := st.Host(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		n, err := strconv.Atoi(strings.TrimSpace(req.Params["value"]))
		if err != nil || n < 1 {
			return "", fmt.Errorf("нужно число изменений в час от 1")
		}
		h.MaxHour = n
		return stayOnServers(req, serversScreen), st.SaveHost(h)
	case "stop_all_auto":
		s.eng.StopAllAuto("стоп-кран: выключено человеком")
		return stayOnServers(req, serversScreen), nil
	case "toggle_night_dry":
		// тумблер называет «выполняет», а настройка — «вхолостую»:
		// включили выполнение — значит холостая смена выключена
		return "", s.eng.SetNightDryRun(!s.eng.NightDryRun())
	case "resume_host":
		h, ok := st.Host(req.Target)
		if !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		h.Paused = ""
		return stayOnServers(req, serversScreen), st.SaveHost(h)
	case "delete_host":
		return serversScreen, st.DeleteHost(req.Target)
	case "approve_ssh", "approve_ssh_read":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		go func() { _ = s.eng.ApproveSSH(ctx, id, req.Action == "approve_ssh_read") }()
		return "", nil
	case "reject_all_ssh":
		n, err := s.eng.RejectAllPending(req.Target)
		if err != nil {
			return "", err
		}
		log.Printf("снято с очереди ожидающих команд: %d", n)
		return stayOnServers(req, serversScreen), nil
	case "reject_ssh":
		id, err := req.one()
		if err != nil {
			return "", err
		}
		return "", s.eng.RejectSSH(id)
	case "set_night_limits":
		// имя скилла — слово, не число: пустое отвергаем, а есть ли такой
		// скилл, смена скажет сама («скилла нет — осматривать нечем»)
		for _, name := range []string{"skill_linux", "skill_routeros"} {
			if v, ok := req.Params[name]; ok && strings.TrimSpace(v) == "" {
				return "", fmt.Errorf("нужно имя скилла осмотра")
			}
		}
		return serversScreen + "~" + serversTabAuto, s.saveSettings(req,
			settingParam{"max_steps", "night.max_steps", "шагов за смену"},
			settingParam{"look_max", "night.look_max", "команд осмотра"},
			settingParam{"look_timeout_s", "night.look_timeout_s", "секунд на осмотр"},
			settingParam{"every_h", "night.every_h", "часов между сменами"},
			settingParam{"skill_linux", "night.skill.linux", "скилл для Linux"},
			settingParam{"skill_routeros", "night.skill.routeros", "скилл для RouterOS"})
	case "set_auto_limits":
		return serversScreen + "~" + serversTabAuto, s.saveSettings(req,
			settingParam{"rollback_min", "auto.rollback_min", "минут страховки"},
			settingParam{"max_hour", "auto.max_hour", "изменений в час"})
	case "set_ssh_limits":
		return serversScreen + "~" + serversTabAuto, s.saveSettings(req,
			settingParam{"timeout_s", "ssh.timeout_s", "таймаут чтения"},
			settingParam{"confirm_timeout_s", "ssh.confirm_timeout_s", "таймаут подтверждённой команды"},
			settingParam{"max_output", "ssh.max_output", "предел вывода"})
	case "set_ssh_setting":
		var v int
		if _, err := fmt.Sscanf(req.Params["value"], "%d", &v); err != nil {
			return "", err
		}
		return "", s.eng.SetSSHSetting(req.Target, v)
	default:
		return "", errUnhandled
	}
}

// parsePort — порт SSH из поля формы.
func parsePort(v string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("порт — число от 1 до 65535")
	}
	return p, nil
}

// autonomyFromWord — уровень самостоятельности из слова человека. Слова, а
// не номера: в подтверждении человек читает «полная», а не «2».
func autonomyFromWord(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "ручное", "нет", "выключено", "":
		return store.AutonomyOff, nil
	case "под судом", "суд":
		return store.AutonomyCourt, nil
	case "полная", "полностью":
		return store.AutonomyFull, nil
	}
	return "", fmt.Errorf("уровень пишется словом: ручное, под судом или полная")
}
