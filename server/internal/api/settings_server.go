package api

// Экран одного сервера: `settings:servers@<имя>~вкладка`. Шапку (путь назад,
// вкладки, закреплённая строка) рисует общий entityFrame, содержимое — здесь:
//
//	Обзор             — что за машина: адрес, ключ, отпечаток, род, sudo
//	Самостоятельность — уровень, окно, потолок, суд, остановка
//	Правила           — три колонки «Разрешить / Спросить / Запретить»
//	Журнал            — что делалось на этой машине
//
// Всё, что правится, — поля строками настройки: значение видно, формат не
// нужно помнить, а ошибка приходит не после «ОК». Сохраняет их одна нижняя
// полоса. Опасное — внизу «Обзора» своей карточкой.
//
// Решения принимает движок, а не экран: «можно ли отдать серверу полную
// самостоятельность» спрашивается у `FullAutonomyDenied` и `CourtReady`, и
// ответ с причиной показывается человеку. Второй копии этих правил здесь нет.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// serverTabs — вкладки сервера. Счётчик у вкладки говорит, что там есть
// чинить или решать, до того как человек туда зайдёт.
func (s *Server) serverTabs(h *store.Host) []innerTab {
	tabs := []innerTab{
		{ID: serversTabHosts, Name: "Обзор"},
		{ID: serversTabAuto, Name: "Самостоятельность"},
		{ID: serversTabRules, Name: "Правила"},
		{ID: serversTabLog, Name: "Журнал"},
	}
	if n := s.waitingCount(h.Name); n > 0 {
		tabs[0].Count, tabs[0].Level = strconv.Itoa(n)+" ждут", LevelAlert
	}
	if h.Paused != "" {
		tabs[1].Count, tabs[1].Level = "!", LevelAlert
	} else if s.eng.WindowMismatch(h.Name) != "" {
		tabs[1].Count, tabs[1].Level = "!", LevelWarn
	}
	// сервер без правил запрещает всё: это видно на вкладке до захода в неё
	if s.eng.Store().HostRuleSet(h.Name).Empty() {
		tabs[2].Count, tabs[2].Level = "нет", LevelWarn
	}
	return tabs
}

// serverCaption — одна строка фактов закреплённой шапки: род, адрес, связь,
// самостоятельность.
func serverCaption(h *store.Host) string {
	parts := []string{serverKindWord(h), h.User + "@" + h.Addr + ":" + strconv.Itoa(portOf(h))}
	switch trouble, _ := hostTrouble(h); {
	case trouble != "":
		parts = append(parts, trouble)
	case h.Fingerprint != "":
		parts = append(parts, "на связи")
	default:
		parts = append(parts, "ещё не подключался")
	}
	switch {
	case h.Paused != "":
	case h.Full():
		parts = append(parts, "полная самостоятельность")
	case h.Auto:
		parts = append(parts, "под судом")
	default:
		parts = append(parts, "ручное управление")
	}
	return strings.Join(parts, " · ")
}

func portOf(h *store.Host) int {
	if h.Port <= 0 {
		return 22
	}
	return h.Port
}

func (s *Server) buildServer(doc *Document, a serversAddr, filter string, limit int) error {
	st := s.eng.Store()
	doc.Layout = "read"
	h, ok := st.Host(a.Host)
	if !ok {
		// сервер могли удалить, пока экран был открыт: не ошибка, а объяснение
		doc.Heading = "Серверы"
		doc.Components = append(doc.Components,
			Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: serversScreen, Label: "‹ Серверы"}}},
			emptyState("Сервера «"+a.Host+"» нет", "Его удалили или переименовали. Список машин — по кнопке «‹ Серверы» выше", "tray"))
		return nil
	}
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	switch a.Tab {
	case serversTabAuto:
		s.serverAutonomy(add, h)
	case serversTabRules:
		s.serverRules(add, h, a.Try)
	case serversTabLog:
		if err := s.serversJournal(doc, h.Name, filter, limit); err != nil {
			return err
		}
	default:
		a.Tab = serversTabHosts
		s.serverOverview(add, h)
	}

	letter, color := hostTile(h)
	e := entity{Section: serversScreen, Back: "Серверы", ID: h.Name, Name: h.Name,
		Avatar: letter, AvatarColor: color, Caption: serverCaption(h),
		Tab: a.Tab, Tabs: s.serverTabs(h), Switch: "Сервер"}
	if trouble, _ := hostTrouble(h); trouble != "" {
		e.Level = LevelAlert
		e.Badges = []Badge{{Text: trouble, Level: LevelAlert}}
	}
	primary := hostPrimary(h)
	e.Primary = &primary
	// «···»: что не главное. Проверка связи, когда главная кнопка другая, и
	// короткие пути по вкладкам — у телефона вкладки в одну полосу, а меню под
	// рукой
	if primary.Action != "check_host" {
		e.Menu = append(e.Menu, Action{Action: "check_host", Target: h.Name, Label: "Проверить связь"})
	}
	e.Menu = append(e.Menu,
		Action{Action: "$goto", Target: entityAddr(serversScreen, h.Name, serversTabRules), Label: "Правила"},
		Action{Action: "$goto", Target: entityAddr(serversScreen, h.Name, serversTabLog), Label: "Журнал команд"})
	hosts, _ := st.Hosts()
	for _, other := range hosts {
		e.Others = append(e.Others, other.Name)
	}
	sort.Strings(e.Others)
	entityFrame(doc, e)
	return nil
}

// sshKeyNames — имена ключей SSH из хранилища: из них выбирают ключ сервера.
func (s *Server) sshKeyNames() []string {
	var keys []string
	if secrets, err := s.eng.Store().VaultSecrets(); err == nil {
		for _, sec := range secrets {
			if sec.Kind == store.SecretSSH {
				keys = append(keys, sec.Name)
			}
		}
	}
	return keys
}

// serverOverview — «Обзор»: что за машина. Свойства — поля, sudo — тумблер с
// вопросом (это право на root, а не настройка), опасное — внизу.
func (s *Server) serverOverview(add func(Component), h *store.Host) {
	st := s.eng.Store()

	// то, что ждёт именно этого сервера, — вверху: полка «Ждут вас» с кнопками
	if shelf := s.serversShelf(h.Name); shelf != nil {
		add(*shelf)
	}
	var alerts []Component
	switch {
	case h.NewFP != "":
		alerts = append(alerts, Component{Type: "row", Layout: "wide", ID: "host-fp-changed", Level: LevelAlert, Dot: levelDot(LevelAlert),
			Subtitle: "Отпечаток сервера сменился",
			Caption: "Команды не выполняются, пока вы не примете новый. Так бывает после переустановки сервера; " +
				"если вы его не переустанавливали — это тревога: на том конце может быть другая машина",
			Facts: []Prop{{Label: "Было", Value: shortFP(h.Fingerprint)}, {Label: "Стало", Value: shortFP(h.NewFP)}}})
	case h.LastErr != "":
		alerts = append(alerts, Component{Type: "row", Layout: "wide", ID: "host-not-answering", Level: LevelAlert, Dot: levelDot(LevelAlert),
			Subtitle: "Сервер не отвечает", Caption: clipRunes(h.LastErr, 300)})
	}
	if h.Paused != "" {
		alerts = append(alerts, Component{Type: "row", Layout: "wide", ID: "host-paused", Level: LevelAlert, Dot: levelDot(LevelAlert),
			Subtitle: "Самостоятельность остановлена", Caption: h.Paused,
			Actions: []Action{{Action: "$goto", Target: entityAddr(serversScreen, h.Name, serversTabAuto), Label: "Самостоятельность"}}})
	}
	if len(alerts) > 0 {
		add(Component{Type: "section", ID: "host-alerts", Children: alerts})
	}

	// ---- свойства полями ----
	keys := s.sshKeyNames()
	if !containsString(keys, h.Key) && h.Key != "" {
		keys = append([]string{h.Key}, keys...) // ключ, которого нет в хранилище, остаётся видимым
	}
	kind := ""
	switch h.Kind {
	case store.HostLinux:
		kind = "Linux"
	case store.HostRouterOS:
		kind = "RouterOS"
	}
	add(Component{Type: "form", ID: "host-props", Style: formRows,
		Fields: []Field{
			{Type: "text", Name: "addr", Label: "Адрес", Hint: "IP или имя хоста", Value: h.Addr},
			{Type: "number", Name: "port", Label: "Порт", Value: strconv.Itoa(portOf(h)), Min: 1, Max: 65535},
			{Type: "text", Name: "user", Label: "Пользователь", Hint: "под ним Сёма заходит по SSH", Value: h.User},
			{Type: "picker", Name: "key", Label: "Ключ", Options: keys, Value: h.Key,
				Hint: "из хранилища; публичную часть кладёте на сервер вы, а приватная не выдаётся ни модулю, ни модели"},
			{Type: "segments", Name: "kind", Label: "Род", Options: []string{"Linux", "RouterOS"}, Value: kind,
				Hint: "угадывается по ответу на uname; от рода зависят скилл осмотра и наборы правил"},
		},
		Submit: &Action{Action: "save_host", Target: h.Name, Label: "Сохранить"}})

	// ---- sudo и то, что Сёма знает о машине ----
	sudoWhy := "Выключено — команды с sudo, doas и su отклоняются совсем, даже с вашей кнопкой. " +
		"Включено — они всё равно идут на подтверждение, но root становится достижим"
	if h.Name == engine.HostOwnerName {
		sudoWhy += ". На машине, где живёт сам Сёма, это выключено по умолчанию: сломать её значит остаться без Сёмы"
	}
	rows := []Component{{Type: "row", Layout: "wide", ID: "host-sudo", Subtitle: "Команды с sudo", Caption: sudoWhy,
		Toggle: boolToggle(!h.NoSudo),
		Actions: []Action{{Action: "toggle_host_sudo", Target: h.Name, Label: "toggle",
			Confirm: map[bool]string{
				true:  "Разрешить Сёме команды с sudo на «" + h.Name + "»? Они всё равно пойдут на ваше подтверждение, но root станет достижим.",
				false: "Запретить sudo на «" + h.Name + "»? Команды с sudo, doas и su будут отклоняться совсем, даже с вашей кнопкой.",
			}[h.NoSudo],
			ConfirmOK: map[bool]string{true: "Разрешить", false: "Запретить"}[h.NoSudo]}}}}

	fp := Component{Type: "row", Layout: "wide", ID: "host-fingerprint", Subtitle: "Отпечаток сервера"}
	if h.Fingerprint != "" {
		fp.Caption = h.Fingerprint + " — целиком, чтобы сверить с ssh-keygen -lf"
		fp.Actions = []Action{{Action: "$copy", Target: h.Fingerprint, Label: "Скопировать"}}
	} else {
		fp.Caption = "ещё не запомнен: Сёма принимает его при первом входе, а сменившийся — только с вашего согласия"
	}
	rows = append(rows, fp)

	if sec, ok := st.Secret(h.Key); ok && sec.Public != "" {
		install := "echo \"" + sec.Public + "\" >> ~/.ssh/authorized_keys"
		key := Component{Type: "row", Layout: "wide", ID: "host-public-key", Subtitle: "Публичная часть ключа",
			Caption: "ключ «" + h.Key + "» из хранилища"}
		if h.Fingerprint == "" {
			// входа ещё не было: единственное, что нужно человеку, — положить
			// ключ на сервер
			key.Caption = "Дайте доступ: на сервере выполните " + install
		}
		key.Actions = []Action{{Action: "$copy", Target: install, Label: "Скопировать команду"}}
		rows = append(rows, key)
	}
	if h.LastCmd != "" {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "host-last", Subtitle: "Последняя команда",
			Caption: clipRunes(h.LastCmd, 120) + " · " + whenText(h.LastAt)})
	}
	add(Component{Type: "section", ID: "host-facts", Children: rows})

	// ---- опасное: внизу своей карточкой, с подтверждением ----
	var danger []Component
	if h.NewFP != "" {
		danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-fingerprint",
			Subtitle: "Принять новый отпечаток",
			Caption:  "Так бывает после переустановки сервера; если вы его не переустанавливали — это тревога, и принимать не нужно",
			Actions: []Action{{Action: "accept_fingerprint", Target: h.Name, Label: "Принять…", Style: "destructive",
				Confirm:   "Принять новый отпечаток " + h.Name + "? Так бывает после переустановки сервера; если вы его не переустанавливали — это тревога.",
				ConfirmOK: "Принять"}}})
	}
	danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-delete",
		Subtitle: "Удалить сервер",
		Caption:  "Ключ в хранилище останется; правила и журнал команд — нет",
		Actions: []Action{{Action: "delete_host", Target: h.Name, Label: "Удалить…", Style: "destructive",
			Confirm:   "Удалить сервер «" + h.Name + "»? Ключ в хранилище останется, а правила сервера и ожидающие команды пропадут.",
			ConfirmOK: "Удалить сервер"}}})
	add(Component{Type: "section", ID: "host-danger", Title: "Опасное", Style: "danger", Children: danger})
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// autonomyExplain — что значит каждый уровень. Нынешний — первым: именно его
// человек читает в одну строку подсказки, остальное раскрывается нажатием.
func autonomyExplain(current string) string {
	explain := map[string]string{
		"ручное":    "Ручное — изменяющая команда ждёт вашей кнопки.",
		"под судом": "Под судом — в окне изменяющая команда идёт на суд двух моделей; «Спросить» и sudo остаются вам.",
		"полная": "Полная — Сёма сам находит работу ночной сменой; правило «Спросить» идёт на суд, но только со страховкой возврата. " +
			"Не даётся «хозяину», серверу без sudo и когда суд не собран.",
	}
	order := []string{"ручное", "под судом", "полная"}
	parts := []string{explain[current]}
	for _, l := range order {
		if l != current {
			parts = append(parts, explain[l])
		}
	}
	return strings.Join(parts, " ")
}

// autonomyUnavailable — какие уровни этому серверу сейчас недоступны и почему.
// Спрашивает движок, а не экран: те же проверки, что при сохранении.
// Возвращает отключённые варианты, причину и куда за ней идти.
func (s *Server) autonomyUnavailable(h *store.Host) ([]string, string, *Action) {
	if ok, reason := s.eng.CourtReady(); !ok {
		return []string{"под судом", "полная"}, reason,
			&Action{Action: "$goto", Target: modelsScreen, Label: "К ролям", Style: "primary"}
	}
	if reason := s.eng.FullAutonomyDenied(h); reason != "" {
		var fix *Action
		if h.NoSudo {
			fix = &Action{Action: "$goto", Target: entityAddr(serversScreen, h.Name, ""), Label: "К свойствам", Style: "primary"}
		}
		return []string{"полная"}, reason, fix
	}
	return nil, "", nil
}

// serverAutonomy — «Самостоятельность»: уровень сегментами, окно двумя
// полями времени, потолок числом — полями, а не вопросами. Недоступный
// уровень виден серым, а причину говорит строка под полями.
func (s *Server) serverAutonomy(add func(Component), h *store.Host) {
	off, why, fix := s.autonomyUnavailable(h)
	maxHour := s.eng.AutoMaxHour(h)
	globalMax := s.eng.AutoMaxHour(&store.Host{})
	maxField := s.settingField("auto.max_hour", "max_hour", "Не больше изменений в час",
		"Сколько изменяющих команд Сёма делает на этом сервере сам за час. «По умолчанию» — общий потолок, он правится в «Тонкой настройке» раздела", "")
	maxField.Value = strconv.Itoa(maxHour)
	globalValue := strconv.Itoa(globalMax)
	maxField.Default = &globalValue

	noWindow := ""
	add(Component{Type: "form", ID: "host-auto", Style: formRows,
		Fields: []Field{
			{Type: "segments", Name: "level", Label: "Уровень", Options: []string{"ручное", "под судом", "полная"},
				Tones: []string{"", "", "warn"}, Value: h.AutonomyTitle(), Disabled: off,
				Hint: autonomyExplain(h.AutonomyTitle())},
			{Type: "timespan", Name: "window", Label: "Окно",
				Hint:  "Вне окна изменяющие команды ждут вашей кнопки; пусто — круглые сутки. Окно через полночь пишется так же: с 22:00 до 06:00",
				Value: h.Window, Default: &noWindow},
			maxField,
		},
		Submit: &Action{Action: "set_host_autonomy", Target: h.Name, Label: "Сохранить"}})

	var rows []Component
	if len(off) > 0 {
		title := "«Полная» самостоятельность недоступна"
		if len(off) > 1 {
			title = "«Под судом» и «полная» недоступны"
		}
		row := Component{Type: "row", Layout: "wide", ID: "auto-unavailable", Subtitle: title, Caption: why}
		withLevel(&row, LevelWarn)
		if fix != nil {
			row.Actions = []Action{*fix}
		}
		rows = append(rows, row)
	}
	if h.Paused != "" {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "auto-paused", Subtitle: "Самостоятельность остановлена",
			Caption: h.Paused + ". Первая несошедшаяся проба останавливает сервер до вас",
			Level:   LevelAlert, Dot: levelDot(LevelAlert),
			Actions: []Action{{Action: "resume_host", Target: h.Name, Label: "Продолжить", Style: "primary",
				Confirm: "Разобрались, что случилось на «" + h.Name + "»? Тогда Сёма снова сможет менять его сам.", ConfirmOK: "Продолжить"}}})
	} else {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "auto-paused", Subtitle: "Остановка",
			Caption: "Первая несошедшаяся проба останавливает сервер до вас и говорит, к какому времени он вернёт себя сам",
			Badges:  []Badge{{Text: "не остановлен", Level: LevelOK}}})
	}
	if line := s.eng.WindowMismatch(h.Name); line != "" {
		row := Component{Type: "row", Layout: "wide", ID: "auto-window-mismatch", Subtitle: "Окно и время команд не совпадают",
			Caption: line + ". Поменяйте окно или час задачи, которая их присылает",
			Actions: []Action{{Action: "$goto", Target: "tasks", Label: "Задачи", Style: "primary"}}}
		withLevel(&row, LevelWarn)
		rows = append(rows, row)
	}
	court := Component{Type: "row", Layout: "wide", ID: "auto-court", Subtitle: "Суд",
		Caption: "Изменяющую команду разбирает арбитр, вердикт выносит судья; любая заминка читается как «спросить вас»"}
	if ready, _ := s.eng.CourtReady(); ready {
		court.Badges = []Badge{{Text: s.eng.Role(engine.RoleArbiter).Model + " → " + s.eng.Role(engine.RoleJudge).Model, Level: LevelOK}}
	} else {
		court.Badges = []Badge{{Text: "не собран", Level: LevelWarn}}
	}
	rows = append(rows, court)
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "auto-insurance", Subtitle: "Страховка возврата",
		Caption: "Общий срок для всех серверов — в «Тонкой настройке» раздела",
		Badges:  []Badge{{Text: plural(s.eng.InsuranceMinutes(), "минута", "минуты", "минут")}},
		Actions: []Action{{Action: "$goto", Target: entityAddr(serversScreen, "", serversTabAuto), Label: "Тонкая настройка"}}})
	add(Component{Type: "section", ID: "host-auto-facts", Children: rows})
}

// ---- сохранение ----

// timespanParam — окно из двух полей времени. Оба пусты — «круглые сутки»;
// одно из двух — ошибка: половина окна ничего не значит. Второй результат —
// пришло ли поле вовсе: непришедшее значит «не трогали».
func timespanParam(params map[string]string, name string) (string, bool, error) {
	from, hasFrom := params[name+"_from"]
	to, hasTo := params[name+"_to"]
	if !hasFrom && !hasTo {
		return "", false, nil
	}
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	switch {
	case from == "" && to == "":
		return "", true, nil
	case from == "" || to == "":
		return "", true, fmt.Errorf("окно: нужны оба времени — «с» и «до», либо ни одного (круглые сутки)")
	case from == to:
		// такое окно никогда не наступает: самостоятельная работа молча стала бы «никогда»
		return "", true, fmt.Errorf("окно: «с» и «до» совпадают — такое окно не наступит; для круглых суток оставьте оба поля пустыми")
	}
	w := from + "-" + to
	if !engine.ValidWindow(w) {
		return "", true, fmt.Errorf("окно пишется как 02:00-06:00")
	}
	return w, true, nil
}

// saveHostAutonomy — «Сохранить» на вкладке «Самостоятельность»: уровень,
// окно и потолок разом. Сначала проверяется всё, потом пишется — половина
// формы не сохраняется. Те же проверки, что у прежних действий с вопросом
// (суд собран, полная — только серверу, которому её можно отдать); старый
// вызов с `value` — ответом на вопрос — работает как раньше.
func (s *Server) saveHostAutonomy(req actionRequest) (string, error) {
	st := s.eng.Store()
	h, ok := st.Host(req.Target)
	if !ok {
		return "", fmt.Errorf("сервера %q нет", req.Target)
	}
	back := stayOnServers(req, entityAddr(serversScreen, h.Name, serversTabAuto))

	word, hasLevel := req.Params["level"]
	legacy := false
	if !hasLevel {
		word, hasLevel = req.Params["value"]
		legacy = hasLevel
	}
	var level string
	changeLevel := false
	if hasLevel {
		var err error
		if level, err = autonomyFromWord(word); err != nil {
			return "", err
		}
		// с формы уровень, который не менялся, ничего не делает: сохранение
		// окна не должно заодно снимать остановку сервера. Прежний вызов с
		// вопросом применялся всегда
		changeLevel = legacy || level != h.Autonomy
	}
	window, hasWindow, err := timespanParam(req.Params, "window")
	if err != nil {
		return "", err
	}
	maxHour, hasMax := 0, false
	if raw, present := req.Params["max_hour"]; present {
		value, err := settingDef("auto.max_hour").Check(raw)
		if err != nil {
			return "", fmt.Errorf("потолок изменений в час: %w", err)
		}
		n, _ := strconv.Atoi(value)
		hasMax, maxHour = true, n
		// то же число, что общий потолок, — значит «как у всех»: общий
		// потолок потом поменяют, и сервер должен пойти за ним
		if n == s.eng.AutoMaxHour(&store.Host{}) {
			maxHour = 0
		}
	}

	if changeLevel && level != store.AutonomyOff {
		if ready, why := s.eng.CourtReady(); !ready {
			return "", fmt.Errorf("%s — сначала назначьте роли «Арбитр» и «Судья» разным моделям", why)
		}
		if level == store.AutonomyFull {
			if why := s.eng.FullAutonomyDenied(h); why != "" {
				return "", fmt.Errorf("%s", why)
			}
		}
	}
	if changeLevel {
		// Auto сбрасывается вместе с уровнем: SaveHost читает «Autonomy пуст,
		// а Auto включён» как старый вызов с тумблером и возвращает «под судом»,
		// так что «ручное» без этого не сохранялось вовсе
		h.Autonomy, h.Auto, h.Paused = level, level != store.AutonomyOff, ""
		if level != store.AutonomyOff && h.AutoFrom == "" {
			h.AutoFrom = time.Now().Format(time.RFC3339)
		}
	}
	if hasWindow {
		h.Window = window
	}
	if hasMax {
		h.MaxHour = maxHour
	}
	return back, st.SaveHost(h)
}

// saveHost — «Сохранить» на «Обзоре»: адрес, порт, пользователь, ключ, род.
// Непришедшее поле — «не трогали». Новый адрес на отпечаток не влияет: если за
// ним другая машина, Сёма увидит чужой отпечаток и откажется входить.
func (s *Server) saveHost(req actionRequest) (string, error) {
	st := s.eng.Store()
	h, ok := st.Host(req.Target)
	if !ok {
		return "", fmt.Errorf("сервера %q нет", req.Target)
	}
	if v, present := req.Params["addr"]; present {
		if v = strings.TrimSpace(v); v == "" {
			return "", fmt.Errorf("адрес не может быть пустым")
		}
		h.Addr = v
	}
	if v, present := req.Params["port"]; present {
		port, err := parsePort(v)
		if err != nil {
			return "", err
		}
		h.Port = port
	}
	if v, present := req.Params["user"]; present {
		if v = strings.TrimSpace(v); v == "" {
			return "", fmt.Errorf("пользователь не может быть пустым")
		}
		h.User = v
	}
	// ключ другого сервера или чужой секрет сюда не подойдёт: нужен именно
	// ключ SSH из хранилища
	if v, present := req.Params["key"]; present && strings.TrimSpace(v) != "" && strings.TrimSpace(v) != h.Key {
		sec, found := st.Secret(strings.TrimSpace(v))
		if !found || sec.Kind != store.SecretSSH {
			return "", fmt.Errorf("ключа SSH «%s» нет в хранилище", strings.TrimSpace(v))
		}
		h.Key = sec.Name
	}
	if v, present := req.Params["kind"]; present {
		switch strings.TrimSpace(v) {
		case "Linux":
			h.Kind = store.HostLinux
		case "RouterOS":
			h.Kind = store.HostRouterOS
		case "":
		default:
			return "", fmt.Errorf("род сервера — Linux или RouterOS")
		}
	}
	return stayOnServers(req, entityAddr(serversScreen, h.Name, "")), st.SaveHost(h)
}

// ---- «Добавить сервер» ----

// buildServerNew — свой экран добавления, а не форма под списком: обязательные
// поля строками, редкие под «Ещё N полей». Кнопка «Добавить сервер» одна —
// в шапке раздела; здесь — «Добавить и проверить связь».
func (s *Server) buildServerNew(doc *Document) error {
	st := s.eng.Store()
	doc.Heading = "Добавить сервер"
	doc.Subheading = "Ключ берётся из хранилища, пароли Сёма не хранит и не умеет; публичную часть ключа нужно положить на сервер в ~/.ssh/authorized_keys"
	doc.Layout = "read"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	add(Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: serversScreen, Label: "‹ Серверы"}}})

	keys := s.sshKeyNames()
	if len(keys) == 0 {
		add(Component{Type: "section", Children: []Component{{Type: "row", Layout: "wide", ID: "addhost-no-key",
			Subtitle: "Сначала ключ",
			Caption:  "В хранилище нет ни одного ключа SSH — сгенерируйте его на вкладке «Ключи и секреты»",
			Actions:  []Action{{Action: "$goto", Target: secretsScreen, Label: "Ключи и секреты", Style: "primary"}}}}})
	} else {
		add(Component{Type: "form", ID: "addhost", Style: formRows,
			Fields: []Field{
				{Type: "text", Name: "name", Label: "Имя", Hint: "по нему модель и модули просят сервер: web-3"},
				{Type: "text", Name: "addr", Label: "Адрес", Hint: "IP или хост"},
				{Type: "text", Name: "user", Label: "Пользователь", Value: "root"},
				{Type: "picker", Name: "key", Label: "Ключ из хранилища", Options: keys, Value: keys[0],
					Hint: "публичную часть ключа нужно добавить на сервер в ~/.ssh/authorized_keys"},
				{Type: "number", Name: "port", Label: "Порт", Value: "22", Min: 1, Max: 65535, More: true},
				{Type: "toggle", Name: "sudo", Label: "Разрешить sudo", Value: "false", More: true,
					Hint: "по умолчанию выключено: команды с sudo, doas и su отклоняются совсем. Разрешённый sudo всё равно идёт на ваше подтверждение"},
			},
			Submit: &Action{Action: "add_host", Label: "Добавить и проверить связь", Style: "primary"}})
	}

	// машина, где работает Сёма: из контейнера видны только температура и
	// нагрузка, полный контроль — тем же SSH, что у остальных серверов
	if _, has := st.Host(engine.HostOwnerName); !has {
		add(Component{Type: "section", Title: "Машина, где работает Сёма", Children: []Component{{Type: "row", Layout: "wide", ID: "home-host",
			Avatar: "⌂", AvatarColor: "#2E9A5B", AvatarShape: "square", Subtitle: "Хозяин Сёмы",
			Caption: "Сёма заведёт ключ ssh:хозяин и сервер «хозяин» по адресу хозяина; публичную часть ключа нужно будет добавить в ~/.ssh/authorized_keys — инструкция придёт в «Уведомления». " +
				"sudo на этой машине выключен: root Сёме не даётся, пока не разрешите"}}})
		add(Component{Type: "form", ID: "home-host-form", Style: formRows,
			Fields: []Field{{Type: "text", Name: "user", Label: "Пользователь на машине хозяина",
				Hint: "ваш пользователь на этой машине (whoami): под ним Сёма будет заходить по SSH"}},
			Submit: &Action{Action: "add_home_host", Label: "Подключить хозяина"}})
	}
	return nil
}
