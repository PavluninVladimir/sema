package api

// Экран одного ящика: `settings:mail@<ящик>~вкладка`. Шапку (путь назад,
// вкладки, закреплённая строка с переключателем ящика) рисует общий
// entityFrame, содержимое — здесь:
//
//	Обработка   — что Сёма с ящиком делает (назначение, режим, что собирать,
//	              облачной модели ли открыт, глубина «Входящих») и задачи ящика
//	Подключение — как Сёма входит; «Опасное»: перечитать вложения, отключить
//	Отправка    — SMTP: другое полномочие, чем у чтения, и своя вкладка
//	Папки       — куда складывает и в какие заходит
//	Хранение    — когда стирает с почтового сервера (своё у этого ящика)
//
// Что правится — поля строками настройки (`Form.Style: "rows"`) под одной
// нижней полосой сохранения. Общее для всех ящиков здесь не стоит: оно на
// вкладках «Спам» и «Общее» раздела, и в «Хранении» об этом сказано.
//
// Шапку сущности (`entityFrame`) почтовый ящик получил 2 октября 2026
// вместо закреплённой полосы `pinnedAccount`: переключатель ящика, плитка и
// «···» у неё те же, а «‹ Почта» и вкладки встали на общее место.
//
// Решений экран не принимает: он показывает и зовёт прежние действия
// (`save_mail_modes`, `save_mail_conn`, `save_mail_send`, `save_mail_targets`,
// `save_mail_params`, `toggle_folder`, `toggle_mail_task`…).

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func (s *Server) buildMailBox(doc *Document, accounts []config.Mail, name, tab string, limit int) error {
	doc.Layout = "read"
	acc, ok := mailAccountByName(accounts, name)
	if !ok {
		// ящик могли отключить, пока экран был открыт: не ошибка, а объяснение
		doc.Heading = "Почта"
		doc.Components = append(doc.Components,
			Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: mailScreen, Label: "‹ Почта"}}},
			emptyState("Ящика «"+name+"» нет", "Его отключили или переименовали. Список ящиков — по кнопке «‹ Почта» выше", "tray"))
		return nil
	}
	conf := s.eng.MailConf(acc.Name)
	tab, _ = mailBoxTab(tab)
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	switch tab {
	case mailTabConn:
		s.mailConnection(add, acc, conf)
	case mailTabSend:
		s.mailSend(add, acc.Name, conf)
	case mailTabFolders:
		s.mailFoldersTab(add, acc.Name, conf)
	case mailTabKeep:
		s.mailKeep(add, acc.Name, conf)
	default:
		s.mailModes(add, acc, conf)
		if err := s.mailTasks(add, acc.Name, conf, limit); err != nil {
			return err
		}
	}
	entityFrame(doc, s.mailEntity(acc, accounts, conf, tab))
	return nil
}

// mailBoxTabs — вкладки ящика. Счётчик у вкладки говорит, что там есть
// чинить или смотреть, до того как человек туда зайдёт.
func (s *Server) mailBoxTabs(name string, c *store.MailConf) []innerTab {
	tabs := []innerTab{
		{ID: "", Name: "Обработка"},
		{ID: mailTabConn, Name: "Подключение"},
		{ID: mailTabSend, Name: "Отправка"},
		{ID: mailTabFolders, Name: "Папки"},
		{ID: mailTabKeep, Name: "Хранение"},
	}
	// на «Папках» смотреть есть что, когда Сёма разбирает ярлыки Gmail зря или
	// архив указывает на ярлык: перенос туда ничего не делает
	n := len(s.loudFolders(name, c))
	if !c.ReadOnly() && c.Archive != "" && isVirtualFolder(c.Archive) {
		n++
	}
	if n > 0 {
		tabs[3].Count, tabs[3].Level = strconv.Itoa(n), LevelWarn
	}
	if c.LastErr != "" {
		tabs[1].Count, tabs[1].Level = "!", LevelAlert
	}
	return tabs
}

// mailEntity — шапка ящика: плитка, адрес, одна строка фактов («Google OAuth ·
// 15 папок · заходил сегодня 08:46 · личный · читает и наводит порядок»),
// переключатель ящика и «···». Главной кнопки у шапки нет: проверка
// подключения живёт в «···» и на вкладке «Подключение».
func (s *Server) mailEntity(acc config.Mail, accounts []config.Mail, c *store.MailConf, tab string) entity {
	e := entity{Section: mailScreen, Back: "Почта", ID: acc.Name, Name: acc.Username,
		Avatar: firstRuneOfName(acc.Username), AvatarColor: avatarColor(acc.Username),
		Caption: accountByMeta(c) + " · " + purposeTitle(c) + " · " + modeTitle(c),
		Tab:     tab, Tabs: s.mailBoxTabs(acc.Name, c), Switch: "Ящик"}
	if c.LastErr != "" {
		e.Level = LevelAlert
		e.Badges = []Badge{{Text: "не подключается", Level: LevelAlert}}
	}
	check := mailConnectAction(acc.Name, c)
	check.Style = ""
	e.Menu = []Action{
		check,
		{Action: "$goto", Target: entityAddr(mailScreen, acc.Name, mailTabFolders), Label: "Папки"},
		// спам и общее живут у раздела, а не у ящика: отсюда до них не дойти
		// иначе как через «‹ Почта»
		{Action: "$goto", Target: entityAddr(mailScreen, "", mailTabSpam), Label: "Спам и общие настройки"},
	}
	for _, a := range accounts {
		e.Others = append(e.Others, a.Name)
	}
	return e
}

// mailFetchDefault — за сколько дней смотрятся «Входящие», пока ящику не
// назначили своё: окно из настроек запуска, а у ящика без него — неделя.
func (s *Server) mailFetchDefault(account string) int {
	for _, a := range s.eng.Accounts() {
		if a.Name == account && a.FetchWindowDays > 0 {
			return a.FetchWindowDays
		}
	}
	return 7
}

// ---- Обработка ----

// mailModes — что Сёма делает с этим ящиком: назначение, режим, что
// собирать, отдавать ли облачной модели и как глубоко смотреть «Входящие».
// Умолчания галочек зависят от назначения, но после сохранения формы
// становятся решением человека.
func (s *Server) mailModes(add func(Component), acc config.Mail, c *store.MailConf) {
	account := acc.Name
	cloud := s.eng.CloudModelsHint()
	fetch := c.FetchDays
	if fetch == 0 {
		fetch = s.mailFetchDefault(account)
	}
	поля := []Field{
		{Type: "segments", Name: "purpose", Label: "Ящик",
			Hint:  "рабочий по умолчанию закрыт для облачной модели и не даёт чеков — это единственная разница",
			Value: purposeTitle(c), Options: titlesOf(purposeTitles)},
		{Type: "segments", Name: "mode", Label: "Что Сёма делает",
			Hint: "«только читает» — на сервере не меняется ничего: ни переносов, ни пометок, ни очистки. " +
				"Письма и файлы всё равно попадут в архив Сёмы и в поиск. Переключение ничего не откатывает: " +
				"письма, перенесённые раньше, остаются там, куда их унесли",
			Value: modeTitle(c), Options: titlesOf(modeTitles)},
	}
	// признаки сбора: их объявляют ядро и установленные модули. Модуля нет —
	// нет и галочки: включать то, чего нет, человеку предлагать нельзя.
	for _, f := range s.eng.MailFlags() {
		подсказка := "признак ядра"
		if f.Module != "" {
			подсказка = "собирает модуль «" + f.Module + "»"
		}
		поля = append(поля, Field{Type: "toggle", Name: "сбор:" + f.Name, Label: f.Title,
			Hint: подсказка, Value: boolValue(f.On(c))})
	}
	поля = append(поля,
		Field{Type: "toggle", Name: "outbound", Label: "Показывать облачной модели",
			Hint:  "выключите — письма увидит только модель на этой машине (" + cloud + " их не получит), а Сёма честно скажет, что ящик закрыт, вместо «письма нет»",
			Value: boolValue(c.OutboundOn())},
		Field{Type: "picker", Name: "fetch", Label: "Смотреть «Входящие» за",
			Hint:  "всё, что старше, разбирает отдельная задача — от свежих писем к старым; «всё время» означает «каждый заход смотрит весь ящик», и это долго",
			Value: fetchTitle(fetch), Options: fetchOptions})
	add(Component{
		Type:   "form",
		ID:     "mailmodes-" + account,
		Style:  formRows,
		Fields: поля,
		Submit: &Action{Action: "save_mail_modes", Target: account, Label: "Сохранить"},
	})
}

// mailTasks — задачи ящика: тут их только подключают и отключают, а
// расписание и текст правятся на вкладке «Задачи».
func (s *Server) mailTasks(add func(Component), account string, c *store.MailConf, limit int) error {
	tasks, err := s.eng.Store().Tasks()
	if err != nil {
		return err
	}
	attached := map[int64]bool{}
	for _, id := range c.Tasks {
		attached[id] = true
	}
	// три кучки вместо одного списка из двадцати четырёх строк: что
	// работает всегда, что человек подключил сам и что он ещё может
	// подключить. Тумблер здесь значит «эта задача работает с этим
	// ящиком», а не «задача включена» — это и написано под списком
	var всегда, подключены, можно []Component
	for _, t := range tasks {
		if t.Root {
			continue
		}
		builtin := engine.IsMailTask(t.System)
		row := Component{
			Type:     "row",
			Layout:   "wide",
			ID:       "mailtask-" + strconv.FormatInt(t.ID, 10),
			Subtitle: t.Title,
			Caption:  t.Prompt,
			Meta:     taskByMeta(t),
			Actions:  []Action{{Action: "$goto", Target: "tasks", Label: "Открыть"}},
		}
		// задачи модулей, стоящих за галочками ящика («Чеки и счета»,
		// «Уведомления из писем»), и очередь событий — обязательны: тумблера нет
		if r, ok := s.flagTaskRow(row, t, c); ok {
			всегда = append(всегда, r)
			continue
		}
		// задачи, невозможные в режиме ящика, — серые с пояснением, включить
		// их нельзя, пока режим не сменён
		switch {
		case c.ReadOnly() && (t.System == engine.WorkerFolders || t.System == engine.WorkerPurge):
			row.Style = "secondary"
			row.Caption = strings.TrimSpace(row.Caption + " · в режиме «только читает» не работает")
			всегда = append(всегда, row)
		case builtin:
			всегда = append(всегда, row)
		default:
			row.Toggle = "off"
			if attached[t.ID] {
				row.Toggle = "on"
			}
			row.Actions = append([]Action{{Action: "toggle_mail_task",
				Target: account + "~" + strconv.FormatInt(t.ID, 10), Label: "toggle"}}, row.Actions...)
			if attached[t.ID] {
				подключены = append(подключены, row)
			} else {
				можно = append(можно, row)
			}
		}
	}
	group := func(title, id string, kids []Component) *Component {
		if len(kids) == 0 {
			return nil
		}
		return &Component{Type: "section", ID: id,
			Title: fmt.Sprintf("%s · %d", title, len(kids)), Children: kids}
	}
	// двадцать восемь строк занимали половину экрана ящика. Задачи — одним
	// свёрнутым разделом: сверху строка-итог, а кому нужно подключить задачу
	// к ящику — разворачивает. Совсем убрать их нельзя: привязка задачи к
	// ящику живёт только здесь, на «Задачах» её нет
	var inner []Component
	inner = append(inner, Component{Type: "row", Layout: "wide", ID: "mailtasks-итог-" + account,
		Subtitle: "Расписание, текст и порядок задач правятся на «Задачах»",
		Caption:  "здесь — только привязка задачи к этому ящику",
		Actions:  []Action{{Action: "$goto", Target: "tasks", Label: "Задачи"}}})
	if g := group("Работают с каждым ящиком всегда", "mailtasks-always-"+account, всегда); g != nil {
		inner = append(inner, *g)
	}
	if g := group("Подключены к этому ящику", "mailtasks-on-"+account, подключены); g != nil {
		inner = append(inner, *g)
	}
	// «Можно подключить» — восемнадцать строк, которые почти никогда не
	// нужны. Свёрнутый раздел всё равно едет к человеку целиком: его
	// свёрнутость живёт у клиента. Поэтому до нажатия их просто нет
	if limit <= 0 || limit == defaultRows {
		if len(можно) > 0 {
			inner = append(inner, Component{Type: "button", Actions: []Action{{Action: "$more",
				Label: fmt.Sprintf("Показать, что ещё можно подключить · %d", len(можно))}}})
		}
	} else if g := group("Можно подключить", "mailtasks-off-"+account, можно); g != nil {
		inner = append(inner, *g)
	}
	inner = append(inner, Component{Type: "text", Style: "secondary",
		Text: "Тумблер здесь значит «эта задача работает с этим ящиком», а не «задача включена». Верхние отключить нельзя — без них Сёма перестанет быть почтовым."})
	add(Component{Type: "section", ID: "mailtasks-" + account, Collapsed: true,
		Title:    "Задачи, работающие с этим ящиком · " + plural(len(всегда)+len(подключены), "включённая", "включённые", "включённых"),
		Children: inner})
	return nil
}

// taskByMeta — что у задачи померено: как часто и когда была в
// последний раз. Расписание стояло капсулой и спорило с состоянием.
func taskByMeta(t *store.Task) string {
	части := []string{t.Schedule}
	if !t.LastRun.IsZero() {
		части = append(части, "последний раз "+whenText(t.LastRun.Format(time.RFC3339)))
	} else {
		части = append(части, "ещё не запускалась")
	}
	return strings.Join(части, " · ")
}

// mailboxFlagOn — признак сбора у ящика: подпись и значение. Состав
// признаков объявляют ядро и установленные модули (engine.MailFlags), а
// не этот файл: раньше здесь стояли имена модулей, и галочка «Чеки и
// счета» пережила сам модуль чеков.
func (s *Server) mailboxFlagOn(c *store.MailConf, flag string) (title string, on bool, known bool) {
	f, ok := s.eng.MailFlagByName(flag)
	if !ok {
		return "", false, false
	}
	return f.Title, f.On(c), true
}

// flagTaskRow — строка задачи, которую нельзя отключить в карточке ящика:
// её включает галочка выше. Выключена галочка — строка серая; модуль или
// сама задача выключены где-то ещё — красная капсула и ссылка туда.
func (s *Server) flagTaskRow(row Component, t *store.Task, c *store.MailConf) (Component, bool) {
	row.Toggle, row.Actions = "", nil
	if t.System == engine.WorkerNotifyDue {
		// задача ядра «Сроки»: нужна, пока в ящике включены уведомления из писем
		switch {
		case !c.AttentionOn():
			return row, false
		case !t.Enabled:
			row.Badges = append(row.Badges, Badge{Text: "выключена на вкладке «Задачи»", Color: "#C4271C"})
			row.Actions = []Action{{Action: "$goto", Target: "tasks", Label: "Задачи"}}
		default:
			row.Badges = append(row.Badges, Badge{Text: "нужна уведомлениям из писем · всегда", Color: "#1E7A32"})
		}
		return row, true
	}
	if t.System == engine.WorkerModuleEvents {
		anyOn := false
		for _, m := range s.eng.Modules() {
			if _, on, known := s.mailboxFlagOn(c, m.Flag); known && on && m.Enabled {
				anyOn = true
			}
		}
		switch {
		case !anyOn:
			return row, false // обычная задача, пока модули галочек не включены
		case !t.Enabled:
			row.Badges = append(row.Badges, Badge{Text: "выключена на вкладке «Задачи»", Color: "#C4271C"})
			row.Actions = []Action{{Action: "$goto", Target: "tasks", Label: "Задачи"}}
		default:
			row.Badges = append(row.Badges, Badge{Text: "нужна модулям ящика · всегда", Color: "#1E7A32"})
		}
		return row, true
	}
	mod, _ := engine.ModuleOfTask(t.System)
	if mod == "" {
		return row, false
	}
	m, ok := s.eng.Module(mod)
	if !ok || m.Flag == "" {
		return row, false
	}
	title, on, known := s.mailboxFlagOn(c, m.Flag)
	if !known {
		return row, false
	}
	switch {
	case !on:
		row.Style = "secondary"
		row.Badges = append(row.Badges, Badge{Text: "выключено", Color: "#8E8E93"})
		row.Caption = strings.TrimSpace(row.Caption + " · выключено галочкой «" + title + "» выше")
	case !m.Enabled:
		row.Badges = append(row.Badges, Badge{Text: "модуль выключен", Color: "#C4271C"})
		row.Caption = strings.TrimSpace(row.Caption + " · галочка «" + title + "» включена, а модуль «" + m.Title + "» нет")
		row.Actions = []Action{{Action: "$goto", Target: "modules", Label: "Модули"}}
	case !t.Enabled:
		row.Badges = append(row.Badges, Badge{Text: "выключена на вкладке «Задачи»", Color: "#C4271C"})
		row.Actions = []Action{{Action: "$goto", Target: "tasks", Label: "Задачи"}}
	default:
		row.Badges = append(row.Badges, Badge{Text: "нужна для «" + title + "» · всегда", Color: "#1E7A32"})
	}
	return row, true
}

// ---- Подключение ----

// mailConnectAction — «Проверить подключение» (у Gmail, куда ещё не входили, —
// «Войти через Google») — главная кнопка вкладки.
func mailConnectAction(name string, c *store.MailConf) Action {
	a := mailPrimary(name, c)
	if a.Label == "Проверить" {
		a.Label = "Проверить подключение"
	}
	a.Style = "primary"
	return a
}

// mailConnection — «Подключение»: поля только своего типа входа, состояние и
// главная кнопка. Секреты в разметку не попадают: поле закрытое и пустое, в
// подсказке — только «задан» или «не задан». «Опасное» — внизу.
func (s *Server) mailConnection(add func(Component), acc config.Mail, c *store.MailConf) {
	account := acc.Name
	fields := []Field{{
		Type: "picker", Name: "auth", Label: "Тип авторизации",
		Hint:    "У каждого типа свои поля: у OAuth — ключи клиента, у пароля — логин и пароль",
		Value:   authTitle(c.Auth),
		Options: authTitlesInOrder(),
	}}
	switch c.Auth {
	case store.AuthGoogle, store.AuthExchange:
		fields = append(fields,
			Field{Type: "text", Name: "client_id", Label: "Client ID",
				Hint: "OAuth-клиент типа Desktop: Google Cloud Console → APIs & Services → Credentials", Value: c.ClientID},
			// значение секрета наружу не отдаём: приложение видит только,
			// задан он или нет, и может задать новый
			Field{Type: "secure", Name: "secret", Label: "Client secret",
				Hint: "значение не показывается — только заменяется · " + secretHint(c.Secret)})
		if c.Auth == store.AuthExchange {
			fields = append(fields, Field{Type: "text", Name: "tenant", Label: "Tenant ID",
				Hint: "Каталог Microsoft 365; для общего — common", Value: c.Tenant})
		}
		fields = append(fields, Field{Type: "text", Name: "username", Label: "Логин", Value: c.Username})
	case store.AuthAppPassword:
		fields = append(fields,
			Field{Type: "text", Name: "username", Label: "Логин", Value: c.Username},
			Field{Type: "secure", Name: "password", Label: "Пароль приложения",
				Hint: secretHint(c.Password) + " · не основной пароль: у Яндекса и Mail.ru он создаётся отдельно"},
			Field{Type: "text", Name: "host", Label: "IMAP-хост", Value: c.Host})
	default:
		fields = append(fields,
			Field{Type: "text", Name: "host", Label: "IMAP-хост", Value: c.Host},
			Field{Type: "number", Name: "port", Label: "Порт", Hint: "SSL/TLS обязателен", Value: strconv.Itoa(c.Port), Min: 1, Max: 65535},
			Field{Type: "text", Name: "username", Label: "Логин", Value: c.Username},
			Field{Type: "secure", Name: "password", Label: "Пароль", Hint: secretHint(c.Password)})
	}
	add(Component{
		Type:   "form",
		ID:     "mailconn-" + account,
		Style:  formRows,
		Fields: fields,
		Submit: &Action{Action: "save_mail_conn", Target: account, Label: "Сохранить"},
	})

	// состояние входа: что вышло в последний раз и кнопка проверить
	status := Component{Type: "row", Layout: "wide", ID: "mailconn-status-" + account,
		Subtitle: "Последняя проверка", Actions: []Action{mailConnectAction(account, c)}}
	switch {
	case c.LastErr != "":
		status.Caption = "не подключается: " + c.LastErr
		withLevel(&status, LevelAlert)
	case c.Connected != "":
		status.Caption = fmt.Sprintf("подключено · %s · папок: %d", whenText(c.Connected), len(c.Folders))
	default:
		status.Caption = "вход ещё не выполнялся"
	}
	rows := []Component{status}
	if c.Auth == store.AuthGoogle {
		// вход в Google — один раз и из терминала: окно согласия открывает
		// браузер на той машине, где запущена команда
		access := Component{Type: "row", Layout: "wide", ID: "mailconn-google-" + account, Subtitle: "Доступ к Google",
			Caption: "Вход делается один раз из терминала: go run ./cmd/sema -google-login — команда откроет браузер " +
				"и положит токен в базу; на экран токен не попадает"}
		if c.Connected != "" && c.LastErr == "" {
			access.Badges = []Badge{{Text: "вход выполнен", Level: LevelOK}}
		} else {
			access.Badges = []Badge{{Text: "нужен вход", Level: LevelWarn}}
		}
		rows = append([]Component{access}, rows...)
	}
	add(Component{Type: "section", ID: "mailconn-state-" + account, Title: "Проверка", Children: rows})

	add(s.mailDanger(acc))
}

// mailDanger — «Опасное» ящика: внизу своей карточкой, кнопки видимы, у каждой
// вопрос. Перечитать вложения — это время и трафик; отключить ящик — Сёма
// перестаёт его читать. Письма и архив остаются у Сёмы в любом случае.
func (s *Server) mailDanger(acc config.Mail) Component {
	rows := []Component{{Type: "row", Layout: "wide", ID: "danger-rescan",
		Subtitle: "Перечитать вложения ящика",
		Caption:  "Сёма заново пройдёт по письмам без найденных файлов и скачает вложения: это время и трафик",
		Actions: []Action{{Action: "rescan_attachments", Target: acc.Name, Label: "Перечитать…", Style: "destructive",
			Confirm:   "Сёма заново пройдёт по письмам этого ящика без найденных файлов и скачает вложения. Это займёт время и трафик.",
			ConfirmOK: "Перечитать"}}}}
	if s.storedMailboxes()[acc.Username] {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "danger-remove",
			Subtitle: "Отключить ящик",
			Caption:  "Сёма перестанет читать " + acc.Username + ". Письма и архив останутся у него, из них ничего не удаляется",
			Actions: []Action{{Action: "remove_account", Target: acc.Username, Label: "Отключить…", Style: "destructive",
				Confirm:   "Отключить ящик " + acc.Username + "? Письма и архив останутся у Сёмы.",
				ConfirmOK: "Отключить"}}})
	} else {
		// ящик из файла запуска с экрана не отключается — иначе кнопка молча
		// ничего бы не делала
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "danger-remove-file",
			Subtitle: "Отключить ящик",
			Caption:  "Этот ящик назван в файле запуска (config.yaml), с экрана он не отключается: уберите его оттуда"})
	}
	return Component{Type: "section", ID: "mail-box-danger", Title: "Опасное", Style: "danger", Children: rows}
}

// ---- Отправка ----

// mailSend — отправка: своя вкладка, а не строка в подключении. Чтение
// почты и отправка от вашего имени — разные полномочия, и человек должен
// видеть, что включает второе.
func (s *Server) mailSend(add func(Component), account string, c *store.MailConf) {
	готово, почему := s.eng.SendReady(account)
	label := "Сёма умеет положить ответ в «Черновики» этого ящика (для этого SMTP не нужен) и отправить его, когда вы так решите."
	callout := Component{Type: "text", Style: "callout", ID: "mailsend-итог-" + account, Text: label}
	if !готово {
		// главное — первым и жирным: форма ниже, а отправки нет, пока SMTP не назван
		callout.Title = "Сейчас отправлять нельзя: " + strings.TrimRight(почему, ". ") + "."
	}
	add(callout)
	add(Component{Type: "form", ID: "mailsend-" + account, Style: formRows,
		Fields: []Field{
			{Type: "text", Name: "smtp_host", Label: "SMTP-хост", Value: c.SMTPHost,
				Hint: "у Яндекса smtp.yandex.ru, у Gmail smtp.gmail.com; пусто — отправка выключена, черновики всё равно работают"},
			{Type: "number", Name: "smtp_port", Label: "Порт", Value: portLine(c.SMTPPort), Min: 1, Max: 65535,
				Hint: "587 со STARTTLS или 465 с TLS сразу; без шифрования Сёма не отправляет вовсе"},
			{Type: "toggle", Name: "smtp_starttls", Label: "STARTTLS (порт 587)", Value: boolValue(c.SMTPStartTLS),
				Hint: "выключено — TLS с первого байта (порт 465). Без шифрования Сёма не отправляет вовсе"},
			{Type: "text", Name: "smtp_user", Label: "Логин", Value: c.SMTPUser,
				Hint: "пусто — тот же, что и для чтения"},
			s.secretFieldWithPick("smtp_secret", "Пароль отправки", c.SMTPSecret,
				"имя секрета из «Ключей и секретов»; само значение сюда не попадает и в описание экрана не уходит"),
		},
		Submit: &Action{Action: "save_mail_send", Target: account, Label: "Сохранить"}})
}

// ---- Папки ----

// mailFoldersTab — «Папки»: что в них не так, куда Сёма складывает и по
// каким заходит. Папки приезжают только после удачного входа: до него
// показывать нечего.
func (s *Server) mailFoldersTab(add func(Component), account string, c *store.MailConf) {
	if len(c.Folders) == 0 {
		add(Component{Type: "section", Title: "Папки", Children: []Component{
			emptyState("Папок ещё не видно",
				"Список папок Сёма читает прямо с почтового сервера — он появится после первого удачного входа", "folder"),
		}})
		return
	}
	s.mailAttention(add, account, c)
	if !c.ReadOnly() {
		s.mailTargets(add, account, c)
	}
	s.mailFolders(add, account, c)
}

// mailAttention — полка «Стоит посмотреть»: то в настройках ящика, что
// работает не так, как человек, скорее всего, думает. Молча это не
// исправляется — Сёма только показывает, где смотреть.
// archiveBroken — папка архива выбрана, а переноса нет.
//
// «[Gmail]/Вся почта» — ярлык, а не папка: перенос в него ничего не
// делает, письмо остаётся во «Входящих». Строка выбора об этом честно
// предупреждала подсказкой, но подсказку читают при выборе, а беда
// живёт дальше — месяцами, молча, при внешне настроенном ящике.
// Разобранное никуда не уезжает, «Входящие» не пустеют, и человек
// думает, что Сёма не работает вовсе.
func (s *Server) archiveBroken(add func(Component), account string, c *store.MailConf) {
	if c.ReadOnly() || c.Archive == "" || !isVirtualFolder(c.Archive) {
		return
	}
	настоящие := realFolders(c.Folders, "")
	беда := Component{Type: "row", Level: LevelAlert,
		Subtitle: "Разобранное никуда не уезжает: «" + c.Archive + "» — ярлык, а не папка",
		Caption: "перенос в ярлык Gmail ничего не делает — письмо остаётся во «Входящих», и папка не пустеет. " +
			"Нужна настоящая папка: например Archive, её Сёма заведёт сам",
		Actions: []Action{{Action: "$goto", Target: entityAddr(mailScreen, account, mailTabFolders),
			Label: "Выбрать рабочую папку →", Style: "primary"}}}
	if len(настоящие) > 0 {
		беда.Facts = []Prop{{Label: "Настоящих папок у ящика", Value: strconv.Itoa(len(настоящие))}}
	}
	add(Component{Type: "shelf", Title: "Стоит посмотреть", Meta: "1 замечание", Children: []Component{беда}})
}

// pointlessFolders — папки, которые Сёма разбирает зря: ярлыки Gmail, корзина,
// черновики. Число стоит счётчиком у вкладки «Папки», сама беда — полкой на
// ней же, поэтому счёт считается отдельной функцией.
func pointlessFolders(c *store.MailConf) []string {
	if c.ReadOnly() || len(c.Folders) == 0 {
		return nil
	}
	var зря []string
	for _, f := range c.Folders {
		if f == c.Archive || f == c.Junk || c.Skipped(f) {
			continue
		}
		if isVirtualFolder(f) {
			зря = append(зря, f)
		}
	}
	return зря
}

func (s *Server) mailAttention(add func(Component), account string, c *store.MailConf) {
	s.archiveBroken(add, account, c)
	зря := pointlessFolders(c)
	if len(зря) == 0 {
		return
	}
	// человек мог сказать «знаю, пусть разбирает» — тогда молчим, но не
	// навсегда: список папок у ящика меняется
	молчать, решено := s.eng.FoldersKept(account, зря)
	if молчать {
		return
	}
	// у замечания всегда есть действие, и оно ведёт прямо к настройке:
	// «посмотрите ниже» — это не действие, а отговорка. Действий два:
	// выключить и оставить — «только выключить» неверно, папка может
	// понадобиться
	выключить := Action{Action: "skip_folders", Target: account,
		Label:     "Выключить все " + strconv.Itoa(len(зря)),
		Style:     "primary",
		Confirm:   "Сёма перестанет заходить в " + strings.Join(зря, ", ") + ". Письма, уже лежащие у него, остаются.",
		ConfirmOK: "Выключить"}
	оставить := Action{Action: "keep_folders", Target: account, Label: "Оставить"}
	caption := strings.Join(зря, ", ") + ". Из ярлыка Gmail перенести письмо нельзя — только потеряется пометка, " +
		"а корзину и черновики Сёма и так не разбирает по существу."
	if !решено.IsZero() {
		caption += " Вы оставляли их " + whenText(решено.Format(time.RFC3339)) + " — состав папок с тех пор изменился или вышел срок проверки."
	} else if дней := s.eng.FoldersCheckEvery(); дней > 0 {
		caption += " «Оставить» — Сёма замолчит про них и спросит снова через " +
			plural(дней, "день", "дня", "дней") + " или раньше, если список папок изменится."
	}
	add(Component{Type: "shelf", Title: "Стоит посмотреть", Meta: "1 замечание", Children: []Component{{
		Type:     "row",
		Subtitle: "Сёма разбирает " + plural(len(зря), "папку", "папки", "папок") + ", где разбирать нечего",
		Caption:  caption,
		Actions:  []Action{выключить, оставить},
	}}})
}

// isSentFolder — папка отправленных по имени. Метка \Sent видна только
// живому соединению с почтовым сервером, а в настройках лежат одни имена,
// поэтому здесь угадываем по имени: это подпись, а не решение — читает
// такие папки на месте сам разбор папок.
func isSentFolder(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "sent") || strings.Contains(n, "отправлен") || strings.Contains(n, "исходящ")
}

// foreignCaption — зачем Сёма туда заходит, если решение не его.
func foreignCaption(kind string) string {
	switch kind {
	case "спам":
		return "спамом это посчитал почтовый сервер. Сёма читает папку на месте: не судит, не помечает, не переносит — но письмо есть в поиске, и видно, что оно было"
	case "корзина":
		return "удалили вы. Сёма читает папку на месте и ничего не трогает: копия остаётся у него, даже когда сервер очистит корзину"
	}
	return "сюда Gmail кладёт письма, помеченные «Пропустить входящие». Читается на месте; то, что уже есть, не задваивается"
}

// serverDecision — папка, где решение принял почтовый сервер или сам
// человек: спам провайдера, корзина, «вся почта» Gmail. Здесь угадываем
// по имени: в настройках лежат одни имена, метки IMAP видны только
// живому соединению. Настоящее решение принимает mail.ПапкиСервера.
func serverDecision(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "spam") || strings.Contains(n, "спам"):
		return "спам"
	case strings.Contains(n, "trash") || strings.Contains(n, "корзина") ||
		strings.Contains(n, "удален") || strings.Contains(n, "удалён"):
		return "корзина"
	case strings.Contains(n, "вся почта") || strings.Contains(n, "all mail"):
		return "вся почта"
	}
	return ""
}

// mailFolders — папки ящика, разложенные по смыслу: что Сёма с какой
// делает. Пятнадцать папок одним списком с капсулами «разбираем» у
// каждой не отвечали на единственный вопрос человека — «а зачем он лезет
// в корзину». Куда Сёма складывает, показывает форма выше (mailTargets), а
// здесь остальные: разбирает, читает на месте (и то, что решил не он),
// разбирает зря и не заходит.
func (s *Server) mailFolders(add func(Component), account string, c *store.MailConf) {
	папка := func(f, caption string, toggle bool) Component {
		row := Component{Type: "row", Layout: "wide", ID: "folder-" + account + "~" + f,
			Subtitle: f, Caption: caption}
		if toggle {
			row.Toggle = "on"
			if c.Skipped(f) {
				row.Toggle = "off"
			}
			row.Actions = []Action{{Action: "toggle_folder", Target: account + "~" + f, Label: "toggle"}}
		}
		return row
	}

	серверные := s.eng.ReadServerFolders()
	var разбираем, наМесте, зря, мимо []Component
	for _, f := range c.Folders {
		kind := serverDecision(f)
		switch {
		case !c.ReadOnly() && (f == c.Archive || f == c.Junk):
			// куда складываем — в форме выше, а не строкой без управления
		case c.Skipped(f):
			мимо = append(мимо, папка(f, "Сёма сюда не заходит", true))
		case серверные && kind != "":
			наМесте = append(наМесте, папка(f, foreignCaption(kind), true))
		case c.ReadOnly():
			наМесте = append(наМесте, папка(f, "читается на месте: ни переносов, ни пометок", true))
		case isSentFolder(f):
			наМесте = append(наМесте, папка(f,
				"ваши письма Сёма читает от старых к новым, ничего не переносит и не помечает — они нужны, чтобы знать, на что вы уже ответили", true))
		case isVirtualFolder(f):
			row := папка(f, labelWhy(f), true)
			withLevel(&row, LevelWarn)
			зря = append(зря, row)
		default:
			разбираем = append(разбираем, папка(f, "", true))
		}
	}

	section := func(title string, kids []Component, collapsed bool) {
		if len(kids) == 0 {
			return
		}
		add(Component{Type: "section", ID: "folders-" + account + "-" + title, Collapsed: collapsed,
			Title: fmt.Sprintf("%s · %d", title, len(kids)), Children: kids})
	}
	section("Разбирает", разбираем, false)
	section("Читает на месте", наМесте, false)
	section("Разбирает, хотя не стоило бы", зря, false)
	section("Не заходит", мимо, true)

	порядок := "Тумблер значит «Сёма заходит в эту папку». Порядок один для всех писем: сначала решается, спам ли это; " +
		"спам уезжает в папку спама, остальное помечается прочитанным и уходит в архив на сервере. " +
		"У Сёмы копия остаётся в любом случае — из его архива письма не удаляются никогда."
	if c.ReadOnly() {
		порядок = "Тумблер значит «Сёма заходит в эту папку». В режиме «только читает» он ничего не переносит и не помечает: " +
			"письма и файлы просто попадают в архив Сёмы и в поиск."
	}
	add(Component{Type: "text", Style: "secondary", Text: порядок})
}

// labelWhy — почему в эту папку лезть не стоит. У каждого случая своя
// причина, и «виртуальный ярлык» человеку ничего не объясняет.
func labelWhy(f string) string {
	switch {
	case strings.Contains(f, "Корзина") || strings.Contains(f, "Trash"):
		return "выброшенное вами — Сёма всё равно оставит копию себе навсегда"
	case strings.Contains(f, "Черновик") || strings.Contains(f, "Draft"):
		return "неотправленное"
	case strings.Contains(f, "Помеченные") || strings.Contains(f, "Starred"):
		return "ярлык Gmail, а не папка: перенести отсюда нельзя, только снимется пометка"
	}
	return "ярлык Gmail, а не папка: перенести отсюда нельзя, только снимется пометка"
}

// mailTargets — куда складываем: архив и спам. Две строки настройки вместо
// строк-описаний и отдельной формы «Сменить папку».
func (s *Server) mailTargets(add func(Component), account string, c *store.MailConf) {
	junkHint := "Подтверждённый спам уезжает сюда и чистится по сроку. Обычно находится сама по метке \\Junk — здесь можно указать другую папку"
	if s.eng.ReadServerFolders() {
		junkHint += ". Сёма читает её и на месте — чтобы знать и о том, что сервер посчитал спамом сам"
	}
	add(Component{Type: "section", ID: "mailtargets-раздел-" + account, Title: "Куда Сёма складывает", Children: []Component{{
		Type:  "form",
		ID:    "mailtargets-" + account,
		Style: formRows,
		Fields: []Field{
			{Type: "picker", Name: "archive", Label: "Разобранное уносить в",
				Hint: archiveHint(c), Value: c.Archive, Options: realFolders(c.Folders, c.Archive)},
			{Type: "picker", Name: "junk", Label: "Спам уносить в",
				Hint:  junkHint,
				Value: c.Junk, Options: realFolders(c.Folders, c.Junk)},
		},
		Submit: &Action{Action: "save_mail_targets", Target: account, Label: "Сохранить"},
	}}})
}

// ---- Хранение ----

// mailKeep — сроки стирания с почтового сервера у этого ящика.
//
// Сроки у каждого ящика свои: у рабочего переписку не стирают вовсе, у
// личного спам живёт две недели — одним числом на оба это не описать.
// «Как у всех» отправляет к общим срокам на вкладке «Общее», и это не то же
// самое, что «нет».
func (s *Server) mailKeep(add func(Component), account string, c *store.MailConf) {
	add(Component{
		Type: "form", ID: "mailstorage-" + account, Style: formRows,
		Fields: []Field{
			{Type: "number", Name: "junk", Label: "Стирать спам с сервера через",
				Hint: "у этого ящика; считается от даты письма. Стирается только на почтовом сервере, копия у Сёмы остаётся навсегда. " +
					"Пока своё не назвали, срок общий — на вкладке «Общее»",
				Value: strconv.Itoa(s.eng.SpamTTLForAccount(account)), Min: 1, Max: 365, Unit: "дней"},
			{Type: "segments", Name: "purge_other", Label: "Стирать с сервера и обычные письма",
				Hint: "«как у всех» — по общей настройке на вкладке «Общее»; это ваша переписка, а не спам: у Сёмы копия " +
					"останется навсегда, но на почтовом сервере письма не будет",
				Value: threeStates(c.PurgeOther), Options: []string{"как у всех", "да", "нет"}},
			{Type: "number", Name: "keep_days", Label: "Обычные письма стирать через",
				Hint:  "считается от даты письма. Ноль значит «как у всех»",
				Value: strconv.Itoa(c.KeepDays), Min: 0, Max: 3650, Unit: "дней"},
		},
		Submit: &Action{Action: "save_mail_params", Target: account, Label: "Сохранить"},
	})
	s.purgedFromServer(add, account)
	add(Component{Type: "text", Style: "secondary",
		Text: "Из архива Сёмы письма не удаляются никогда: они остаются в поиске, в памяти и в сводках. " +
			"Общие сроки и «Опасное» — на вкладке «Общее» раздела."})
}
