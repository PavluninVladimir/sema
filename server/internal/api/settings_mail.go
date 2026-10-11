package api

// «Почта» в настройках: раздел и общие помощники. Экран ящика —
// settings_mail_box.go, «Новый ящик» — settings_mail_new.go.
//
// Раздел делится на три вкладки, и каждая отвечает на свой вопрос: какие
// есть ящики (Ящики), что считать спамом (Спам) и что одинаково для всех
// ящиков (Общее). У каждого ящика свой экран — `settings:mail@<ящик>`, с
// пятью вкладками: Обработка, Подключение, Отправка, Папки, Хранение.
//
// Раньше шесть вкладок были одним полотном, где ящик закреплялся полосой
// сверху, а общее для всех ящиков лежало вперемешку с настройками ящика
// (886 узлов и 88 КБ одной лентой): пароли соседствовали с тумблерами
// папок, и было не понять, чей это параметр. Теперь чей параметр, видно по
// месту: на экране ящика — только его, а общее — на вкладках раздела, и
// каждое подписано как общее. Макет — `design/nastroyki-2/Pochta.html`.
//
// Адреса: `settings:mail` (Ящики), `settings:mail~spam`, `settings:mail~common`,
// `settings:mail@<ящик>~work|conn|send|folders|keep` (у первой вкладки хвоста
// нет), `settings:mail@new`. Прежние адреса (`~work` без ящика, `@ящик~spam`,
// `@ящик` на вкладке «Ящики») открывают то же, что открывали, — их
// приводит `canonMailAddr`.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

var authTitles = map[string]string{
	store.AuthGoogle:      "Google OAuth",
	store.AuthAppPassword: "Пароль приложения",
	store.AuthIMAP:        "Обычный IMAP-пароль",
	store.AuthExchange:    "Exchange / Office 365",
}

var authOrder = []string{store.AuthGoogle, store.AuthAppPassword, store.AuthIMAP, store.AuthExchange}

func authTitle(auth string) string {
	if t, ok := authTitles[auth]; ok {
		return t
	}
	return auth
}

func authByTitle(title string) string {
	for k, v := range authTitles {
		if v == title {
			return k
		}
	}
	return title
}

const (
	// mailScreen — раздел «Почта»
	mailScreen = "settings:mail"
	// mailNew — «ящик» с этим именем означает экран «Новый ящик»
	mailNew = "new"
)

// Внутренние вкладки. У раздела три, у ящика пять; хвосты прежние там, где
// они были («work», «conn», «send», «folders», «spam»).
const (
	mailTabBoxes  = ""       // раздел: Ящики
	mailTabSpam   = "spam"   // раздел: Спам — целиком общее для всех ящиков
	mailTabCommon = "common" // раздел: Общее — то, что одинаково для всех ящиков

	// mailTabWork — прежний хвост «Обработки»: у первой вкладки ящика хвоста
	// нет, как у «Обзора» сервера, но старые ссылки с `~work` открываются
	mailTabWork    = "work"
	mailTabConn    = "conn"    // ящик: Подключение — вход по IMAP
	mailTabSend    = "send"    // ящик: Отправка — SMTP: другое полномочие, другая вкладка
	mailTabFolders = "folders" // ящик: Папки — куда складывает и в какие заходит
	mailTabKeep    = "keep"    // ящик: Хранение — когда стирает с почтового сервера
)

// mailBoxTab — вкладка ящика по хвосту адреса: каноническая («» у первой) и
// известна ли она.
func mailBoxTab(tab string) (string, bool) {
	switch tab {
	case "", mailTabWork:
		return "", true
	case mailTabConn, mailTabSend, mailTabFolders, mailTabKeep:
		return tab, true
	}
	return "", false
}

// mailAddr — разобранный адрес «Почты». Адрес уже канонический.
type mailAddr struct {
	Box string // ящик; пусто — раздел; «new» — «Новый ящик»
	Tab string // вкладка
}

func parseMailAddr(screen string) mailAddr {
	_, id, tab := splitEntity(screen)
	return mailAddr{Box: strings.TrimSpace(id), Tab: tab}
}

// canonMailAddr — адрес по нынешним правилам. Прежние адреса помнят
// уведомления, закладки и чужие журналы, и они должны открывать то же, что
// открывали: общие вкладки без ящика в адресе, а вкладки ящика без ящика —
// на первом ящике, как было. Подменяется адрес документа целиком — клиент
// помнит нынешний.
func (s *Server) canonMailAddr(screen string) string {
	section, id, tab := splitEntity(screen)
	if section != mailScreen {
		return screen
	}
	// «Спам» и «Общее» — настройки всех ящиков сразу: ящик в их адресе врал бы,
	// что порог правится одному из них, а такую ссылку ещё и пересылают
	if tab == mailTabSpam || tab == mailTabCommon {
		return entityAddr(mailScreen, "", tab)
	}
	if id == mailNew {
		return entityAddr(mailScreen, mailNew, "")
	}
	boxTab, known := mailBoxTab(tab)
	if id != "" {
		return entityAddr(mailScreen, id, boxTab)
	}
	if !known || tab == "" {
		return mailScreen // «~boxes», незнакомый хвост — первая вкладка раздела
	}
	// прежний адрес вкладки ящика без ящика («settings:mail~conn») вёл на первый
	if accounts := s.eng.Accounts(); len(accounts) > 0 {
		return entityAddr(mailScreen, accounts[0].Name, boxTab)
	}
	return entityAddr(mailScreen, mailNew, "")
}

// mailAccountByName — ящик по имени из адреса. Имя совпадает с адресом
// ящика почти всегда, но ящик из файла запуска мог получить своё.
func mailAccountByName(accounts []config.Mail, name string) (config.Mail, bool) {
	for _, a := range accounts {
		if a.Name == name {
			return a, true
		}
	}
	for _, a := range accounts {
		if a.Username == name {
			return a, true
		}
	}
	return config.Mail{}, false
}

func (s *Server) buildSettingsMail(doc *Document, screen string, limit int) error {
	a := parseMailAddr(screen)
	accounts := s.eng.Accounts()
	switch {
	case a.Box == mailNew || len(accounts) == 0:
		return s.buildMailNew(doc, len(accounts) > 0)
	case a.Box == "":
		s.buildMailSection(doc, accounts, a.Tab)
		return nil
	}
	return s.buildMailBox(doc, accounts, a.Box, a.Tab, limit)
}

// buildMailSection — раздел: заголовок с одной кнопкой «Добавить ящик» и
// три вкладки — список ящиков, спам и то, что общее для всех.
func (s *Server) buildMailSection(doc *Document, accounts []config.Mail, tab string) {
	doc.Heading = "Почта"
	doc.Layout = "wide"
	doc.Subheading = accountsByPhrase(s, accounts)
	// «Добавить ящик» — единственная кнопка шапки, на всех трёх вкладках
	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(mailScreen, mailNew, ""),
		Label: "Добавить ящик", Style: "primary"}}
	setSubtabs(doc, mailScreen, tab, []innerTab{
		{ID: mailTabBoxes, Name: "Ящики", Count: strconv.Itoa(len(accounts))},
		{ID: mailTabSpam, Name: "Спам"},
		{ID: mailTabCommon, Name: "Общее"},
	})
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	switch tab {
	case mailTabSpam:
		s.mailSpamTab(add, accounts)
	case mailTabCommon:
		s.mailCommonTab(add, accounts)
	default:
		s.mailBoxesTab(add, accounts)
	}
}

// accountsList — какие именно ящики держит «общее»: без имён строка
// «одинаково для всех» не говорит, для кого именно.
func accountsList(accounts []config.Mail) string {
	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		names = append(names, a.Username)
	}
	return strings.Join(names, ", ") + "; меняется сразу у всех"
}

// mailCallout — подпись общей вкладки: настройки одинаковы для всех ящиков.
// Общее не должно выглядеть настройкой одного ящика, поэтому это сказано в
// самом верху подсвеченной плашкой, а не только в подсказках.
func mailCallout(id, lead, text string) Component {
	return Component{Type: "text", Style: "callout", ID: id, Title: lead, Text: text}
}

// accountsByPhrase — строка под заголовком: сколько ящиков, какие они и что
// Сёма с ними делает. Раньше здесь стояло слово «Настройки», которое не
// говорило ничего.
func accountsByPhrase(s *Server, accounts []config.Mail) string {
	личных, рабочих, читают := 0, 0, 0
	for _, a := range accounts {
		c := s.eng.MailConf(a.Name)
		if c.IsWork() {
			рабочих++
		} else {
			личных++
		}
		if c.ReadOnly() {
			читают++
		}
	}
	фраза := plural(len(accounts), "ящик", "ящика", "ящиков")
	switch {
	case рабочих == 0:
		фраза += ", все личные"
	case личных == 0:
		фраза += ", все рабочие"
	default:
		фраза += fmt.Sprintf(", %d личных и %d рабочих", личных, рабочих)
	}
	if читают == len(accounts) {
		return фраза + ". Сёма их только читает: на почтовом сервере ничего не меняется."
	}
	return фраза + ". Сёма читает их, раскладывает спам и уносит разобранное в архив на сервере."
}

// ---- вкладка «Ящики» ----

// mailBoxesTab — полка «Ждут решения» и ящики тонкими строками.
func (s *Server) mailBoxesTab(add func(Component), accounts []config.Mail) {
	if shelf := s.mailShelf(); shelf != nil {
		add(*shelf)
	}
	stored := s.storedMailboxes()
	rows := make([]Component, 0, len(accounts))
	for _, a := range accounts {
		rows = append(rows, s.mailRow(a, stored))
	}
	add(Component{Type: "section", ID: "mailboxes", Title: "Ящики", Children: rows})
}

// mailShelf — полка «Ждут решения»: то, что не поедет без человека. Письма,
// которые Сёма не взялся судить сам, решаются на «Почте», а не в настройках,
// поэтому кнопка ведёт туда. Нечего решать — полки нет.
func (s *Server) mailShelf() *Component {
	n, err := s.eng.Store().CountByStatus(store.StatusPending)
	if err != nil || n == 0 {
		return nil
	}
	caption := "Сёма сам такие письма не переносит: решение за вами"
	if pct := int(s.eng.AutoThreshold()*100 + 0.5); pct > 0 {
		caption += ". Порог, с которого он решает сам, — " + strconv.Itoa(pct) + " % похожести на размеченные вами письма"
	}
	row := Component{Type: "row", Layout: "wide", ID: "mail-pending", Level: LevelWarn, Dot: levelDot(LevelWarn),
		Subtitle: plural(n, "письмо похоже", "письма похожи", "писем похожи") + " на спам — подтвердите",
		Caption:  caption,
		Actions:  []Action{{Action: "$goto", Target: "inbox", Label: "Разобрать →", Style: "primary"}}}
	return &Component{Type: "shelf", ID: "mail-shelf", Title: "Ждут решения",
		Meta: plural(n, "письмо", "письма", "писем"), Children: []Component{row}}
}

// storedMailboxes — ящики, заведённые с экрана: их адреса (логины). Ящик из
// файла запуска с экрана не отключается — это правка config.yaml.
func (s *Server) storedMailboxes() map[string]bool {
	out := map[string]bool{}
	list, err := s.eng.Store().Accounts()
	if err != nil {
		return out
	}
	for _, a := range list {
		out[a.Email] = true
	}
	return out
}

// loudFolders — папки, которые Сёма разбирает зря и о которых человеку ещё не
// сказано «знаю, пусть разбирает». Число стоит счётчиком у вкладки и
// капсулой у ящика, сама беда — полкой на «Папках».
func (s *Server) loudFolders(account string, c *store.MailConf) []string {
	зря := pointlessFolders(c)
	if len(зря) == 0 {
		return nil
	}
	if молчать, _ := s.eng.FoldersKept(account, зря); молчать {
		return nil
	}
	return зря
}

// mailRow — тонкая строка ящика: плитка, адрес, не больше двух капсул,
// одна фраза, одна кнопка и «···». Вся строка ведёт на экран ящика. Капсула,
// стоящая у всех строк («личный», «читает и наводит порядок»), ничего не
// отличает и ушла в подпись раздела; остаётся то, что отличает.
func (s *Server) mailRow(a config.Mail, stored map[string]bool) Component {
	c := s.eng.MailConf(a.Name)
	row := Component{Type: "row", Layout: "wide", ID: "mailbox-" + a.Name, Subtitle: a.Username,
		Avatar: firstRuneOfName(a.Username), AvatarColor: avatarColor(a.Username),
		Caption: mailRowPhrase(c)}
	// одна беда красит строку; капсулы — не больше двух, сперва то, что не так
	loud := s.loudFolders(a.Name, c)
	switch {
	case c.LastErr != "":
		withLevel(&row, LevelAlert)
	case len(loud) > 0:
		withLevel(&row, LevelWarn)
	}
	var pills []Badge
	if c.LastErr != "" {
		pills = append(pills, Badge{Text: "не подключается", Level: LevelAlert})
	}
	if len(loud) > 0 {
		pills = append(pills, Badge{Text: plural(len(loud), "папка зря", "папки зря", "папок зря"), Level: LevelWarn})
	}
	if c.ReadOnly() {
		pills = append(pills, Badge{Text: "только читает"})
	}
	if c.IsWork() {
		pills = append(pills, Badge{Text: "рабочий"})
	}
	if len(pills) > 2 {
		pills = pills[:2]
	}
	row.Badges = pills

	row.Actions = []Action{{Action: "$goto", Target: entityAddr(mailScreen, a.Name, ""), Label: a.Username},
		mailPrimary(a.Name, c)}
	row.Menu = []Action{
		{Action: "$goto", Target: entityAddr(mailScreen, a.Name, mailTabConn), Label: "Подключение"},
		{Action: "$goto", Target: entityAddr(mailScreen, a.Name, mailTabFolders), Label: "Папки"},
	}
	if stored[a.Username] {
		row.Menu = append(row.Menu, Action{Action: "remove_account", Target: a.Username,
			Label: "Отключить ящик…", Style: "destructive", ConfirmOK: "Отключить",
			Confirm: "Отключить ящик " + a.Username + "? Письма и архив останутся у Сёмы."})
	}
	return row
}

// mailPrimary — главная кнопка ящика: проверить подключение, а у Gmail, куда
// ещё не входили, — войти.
func mailPrimary(name string, c *store.MailConf) Action {
	label := "Проверить"
	if c.Auth == store.AuthGoogle && c.Connected == "" {
		label = "Войти через Google"
	}
	return Action{Action: "connect_mailbox", Target: name, Label: label}
}

// mailRowPhrase — одна фраза строки списка: как входит, сколько папок, когда
// заходил, открыт ли облачной модели.
func mailRowPhrase(c *store.MailConf) string {
	phrase := accountByMeta(c)
	if c.OutboundOn() {
		return phrase + " · открыт облачной модели"
	}
	return phrase + " · закрыт для облачной модели"
}

// accountByMeta — что Сёма померил у ящика: как входит, сколько папок, когда
// последний раз заходил.
func accountByMeta(c *store.MailConf) string {
	части := []string{authTitle(c.Auth)}
	if len(c.Folders) > 0 {
		части = append(части, plural(len(c.Folders), "папка", "папки", "папок"))
	}
	if c.Connected != "" {
		части = append(части, "заходил "+whenText(c.Connected))
	} else {
		части = append(части, "вход ещё не выполнялся")
	}
	return strings.Join(части, " · ")
}

func firstRuneOfName(s string) string {
	for _, r := range s {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// ---- вкладка «Спам» ----

// mailSpamTab — вкладка «Спам» целиком: кого пускать во «Входящие» и что
// спамом не считать. Своего ящика у этих настроек нет, и сказано это сверху.
// Редкое — порог «после скольких писем у провайдера» и слова счёта — в «Тонкой
// настройке» вкладки; их умолчания лежат в реестре настроек движка.
func (s *Server) mailSpamTab(add func(Component), accounts []config.Mail) {
	add(mailCallout("спам-для-всех", "Одинаково для всех ящиков —",
		accountsList(accounts)+". Своего ящика у этих настроек нет."))
	hamCount, _ := s.eng.Store().CountRules("ham")
	add(Component{Type: "section", ID: "спам-кого-раздел", Title: "Кого пускать во «Входящие»", Children: []Component{{
		Type: "form", ID: "спам-кого", Style: formRows,
		Fields: []Field{
			{
				Type: "toggle", Name: "whitelist", Instant: true,
				Label: "Во «Входящих» — только те, кого вы разрешили",
				Hint: fmt.Sprintf("остальные считаются спамом и уходят в папку спама, минуя архив; файлы и чеки из них Сёма всё равно сохранит себе. Сейчас разрешено %s",
					plural(hamCount, "адрес", "адреса", "адресов")),
				Value: boolValue(s.eng.WhitelistOnly()),
			},
			{
				// «когда уверен на 90 %» было неправдой дважды. Уверенности
				// модели здесь нет вовсе: Сёма сравнивает новое письмо с
				// теми, что вы сами отметили, и меряет косинусную
				// похожесть (engine/rag.go, ragDecide). И порог работает в
				// обе стороны — не только «в спам», но и «во входящие»,
				// если ближайшим оказалось письмо из белого списка
				Type: "number", Name: "threshold",
				Label: "Порог похожести на размеченные вами письма",
				Hint: "Сёма сравнивает новое письмо с теми, что вы сами отметили спамом и не спамом. " +
					"Совпало выше порога — он решает так же и переносит сам; ниже — приносит вам на решение. " +
					"Это похожесть двух писем, а не уверенность модели: модель здесь не участвует",
				Value: strconv.Itoa(int(s.eng.AutoThreshold()*100 + 0.5)),
				Min:   50, Max: 100, Step: 1, Unit: "%",
			},
			// человек отправляет рассылку в спам в самом почтовом клиенте
			// и ждёт, что Сёма это запомнит. Одно письмо в спаме
			// провайдера — решение фильтра, оно бывает неверным; несколько
			// подряд от одного адреса — отправитель, которого не ждут
			rare(s.settingField("spam.server_votes", "server_votes",
				"Отправитель — спамер после N писем в спаме у провайдера",
				"письма, которые вы или фильтр провайдера отправили в спам в почтовом клиенте, Сёма читает на месте и считает по адресам. "+
					"Набралось N — адрес становится спамером и у Сёмы: его письма, ждущие вашего решения, уезжают в спам сами. "+
					"Белый список сильнее; корзина не считается. 0 — не учиться", "")),
		},
		Submit: &Action{Action: "save_automation", Label: "Сохранить"}}}})
	add(Component{Type: "section", ID: "спам-исключения-раздел", Title: "Что спамом не считать", Children: []Component{{
		Type: "form", ID: "спам-исключения", Style: formRows,
		Fields: []Field{
			{
				Type: "toggle", Name: "money_guard", Instant: true,
				Label: "Не уносить в спам письма со счетами",
				Hint: "рассылка и квитанция приходят с одного адреса, а правило — про адрес: " +
					"письмо, в котором виден счёт, Сёма сам никуда не переносит и приносит вам. " +
					"Отправитель, от которого уже приходили чеки, придерживается независимо от темы",
				Value: boolValue(s.eng.MoneyGuard()),
			},
			rare(s.settingField("mail.money_words", "money_words",
				"Слова, по которым письмо похоже на счёт",
				"через запятую; у каждого свои поставщики и свой язык счетов", "")),
		},
		Submit: &Action{Action: "save_automation", Label: "Сохранить"}}}})
	add(Component{Type: "text", Style: "secondary",
		Text: "Сроки стирания с почтового сервера живут на вкладке «Общее»: они про хранение, а не про то, что считать спамом."})

	// массовый перенос — «Опасное»: письма уезжают в папку спама на почтовом
	// сервере. Копии и файлы остаются у Сёмы; кнопка с вопросом и без
	// письма-кандидата не показывается
	if n := s.eng.InboxCleanupCount(); n > 0 {
		add(Component{Type: "section", ID: "mail-spam-danger", Title: "Опасное", Style: "danger", Children: []Component{{
			Type: "row", Layout: "wide", ID: "danger-clean-inbox",
			Subtitle: "Разобрать входящие: " + plural(n, "письмо", "письма", "писем") + " не от разрешённых",
			Caption: "уедут в папку спама на почтовом сервере; копии писем и файлы останутся у Сёмы. " +
				"Только письма ящиков, где Сёма меняет что-то на сервере",
			Actions: []Action{{Action: "clean_inbox", Label: "Перенести в спам…", Style: "destructive",
				Confirm: fmt.Sprintf("Перенести в спам %s от неразрешённых отправителей? Копии писем и файлы останутся у Сёмы.",
					plural(n, "письмо", "письма", "писем")),
				ConfirmOK: "Перенести"}}}}})
	}
}

// ---- вкладка «Общее» ----

// mailCommonTab — всё, что одинаково для всех ящиков: чтение серверных папок,
// ответы, сроки стирания на почтовом сервере и опасное. Каждое подписано как
// общее; то, что ящик решает сам, — на его экране.
func (s *Server) mailCommonTab(add func(Component), accounts []config.Mail) {
	add(mailCallout("общее-для-всех", "Одинаково для всех ящиков.",
		"То, что ящик решает сам, — на его экране; здесь то, что общее, и каждое подписано как общее. "+
			"Ящики: "+accountsList(accounts)+"."))
	add(Component{Type: "section", ID: "общее-ящики-раздел", Title: "Серверные папки", Children: []Component{{
		Type: "form", ID: "общее-ящики", Style: formRows,
		Fields: []Field{{
			Type: "toggle", Name: "server_folders", Instant: true,
			Label: "Заходить в спам и корзину почтового сервера",
			Hint: "решение там принял не Сёма, и он его не оспаривает: читает на месте, ничего не судит, " +
				"не помечает и не переносит. Без этого «письмо не нашлось» значило «письма нет», " +
				"хотя оно лежало в спаме у провайдера. Забранное остаётся у Сёмы навсегда — " +
				"из его архива письма не удаляются никогда",
			Value: boolValue(s.eng.ReadServerFolders()),
		}},
		Submit: &Action{Action: "save_automation", Label: "Сохранить"}}}})
	add(Component{Type: "section", ID: "общее-ответы-раздел", Title: "Ответы", Children: []Component{{
		Type: "form", ID: "общее-ответы", Style: formRows,
		Fields: []Field{
			{
				Type: "segments", Name: "send_mode", Label: "Что делать с готовым ответом",
				Value:   sendModeTitle(s.eng.SendMode()),
				Options: []string{engine.ModeDraft, engine.ModeButton, sendModeTitle(engine.ModeSelf)},
				Hint: "«черновик» — ответ ложится в папку черновиков ящика, отправляете вы из своей почты; " +
					"«по кнопке» — ответ показывается здесь и уходит по нажатию; " +
					"«сам» — Сёма отправляет названным ниже адресатам без кнопки. " +
					"По умолчанию черновик: его можно отменить, отправленное письмо — нет",
			},
			{
				Type: "text", Name: "send_auto_to", Label: "Кому Сёма вправе отвечать сам",
				Value: strings.Join(s.eng.SendAutoTo(), ", "),
				Hint: "через запятую; можно доменом («@наша-контора.рф»). Пусто в режиме «сам» значит «никому»: " +
					"режим включён, а адресатов не назвали — это недонастроенность, а не разрешение",
			},
		},
		Submit: &Action{Action: "save_automation", Label: "Сохранить"}}}})
	add(Component{Type: "section", ID: "общее-хранение-раздел", Title: "Хранение на почтовом сервере", Children: []Component{{
		Type: "form", ID: "общее-хранение", Style: formRows,
		Fields: []Field{{
			Type: "number", Name: "retention", Label: "Спам стирать с почтового сервера через",
			Unit: "дней", Min: 1, Max: 365,
			Value: strconv.Itoa(s.eng.SpamTTL()),
			Hint: "считается от даты письма, а не от того, когда Сёма унёс его в спам: «через 15 дней» значит " +
				"«письмо старше 15 дней». Стирается только с почтового сервера — у Сёмы копия остаётся навсегда, " +
				"письма из его архива не удаляются никогда. Папка чистится по одному дню за проход. " +
				"Свой срок у ящика — на его вкладке «Хранение»",
		}},
		Submit: &Action{Action: "save_automation", Label: "Сохранить"}}}})
	s.purgedFromServer(add, "")
	s.dangerousRetention(add)
}

// sendModeTitle — режим ответа так, как его называют на экране. В базе он
// «сама» (engine.ModeSelf), а Сёма — он: на экране «сам». Сохранение принимает
// оба слова.
func sendModeTitle(mode string) string {
	if mode == engine.ModeSelf {
		return "сам"
	}
	return mode
}

func sendModeByTitle(title string) string {
	if title == "сам" {
		return engine.ModeSelf
	}
	return title
}

// purgedFromServer — что Сёма стёр с почтового сервера: сколько за сутки и
// за неделю, и последние письма по одному. Журнал удалений велся с самого
// начала, но человеку его не показывали нигде: «сколько писем удалено»
// приходилось спрашивать у меня (13 сентября 2026). Из архива Сёмы письма
// не удаляются никогда — это надо говорить в каждом таком месте.
func (s *Server) purgedFromServer(add func(Component), account string) {
	st := s.eng.Store()
	заСутки, заНеделю := st.DeletionsSince(1), st.DeletionsSince(7)
	список, err := st.RecentDeletions(50)
	if err != nil {
		return
	}
	факты := []Prop{
		{Label: "За сутки", Value: strconv.Itoa(заСутки)},
		{Label: "За неделю", Value: strconv.Itoa(заНеделю)},
	}
	свои := 0
	for _, у := range список {
		if account == "" || у.Account == account || у.Account == "" {
			свои++
		}
	}
	// одна спокойная строка, а не большая пустая карточка: «за неделю ничего» —
	// нормальный итог, а не повод что-то объяснять. Из какого ящика — сказано в
	// названии, чтобы число не читалось как общее
	title := "Стёрто с почтового сервера"
	if account != "" {
		title += " из этого ящика"
	}
	итог := Component{Type: "row", Layout: "wide", ID: "стёрто-итог", Subtitle: title,
		Caption: "удаляется только на почтовом сервере: из архива Сёмы письма не удаляются никогда, " +
			"они остаются в поиске, в памяти и в сводках. Спам стирается по своему сроку, обычные письма — только если вы это разрешили",
		Facts: факты}
	if заНеделю == 0 {
		итог.Badges = []Badge{{Text: "за неделю ничего"}}
		add(Component{Type: "section", ID: "стёрто-с-сервера", Children: []Component{итог}})
		return
	}
	// список писем живёт своим экраном: в настройках — числа и переход,
	// иначе «что именно стёрли» приходится искать среди настроек
	итог.Badges = []Badge{{Text: plural(заНеделю, "письмо", "письма", "писем") + " за неделю"}}
	итог.Actions = []Action{{Action: "$goto", Target: purgedScreen, Label: "Какие письма →", Style: "primary"}}
	if свои > 0 && account != "" {
		итог.Facts = append(итог.Facts, Prop{Label: "Из этого ящика", Value: strconv.Itoa(свои)})
	}
	add(Component{Type: "section", ID: "стёрто-с-сервера", Children: []Component{итог}})
}

// dangerousRetention — стирание обычной переписки с почтового сервера.
//
// Это единственная настройка почты, которая стирает не спам, а вашу
// переписку, и делает это молча по расписанию. Она стояла тумблером
// посреди формы со сроками, между двумя безобидными числами, и
// уезжала общей кнопкой «Сохранить» вместе с ними. Теперь у неё свой
// блок — «Опасное» внизу вкладки, — своё действие и сказано, что именно
// произойдёт и со сколькими письмами прямо сейчас.
func (s *Server) dangerousRetention(add func(Component)) {
	включено := s.eng.PurgeRest()
	срок := s.eng.RestTTL()
	действие := Action{Action: "save_purge_other", Label: "Применить"}
	если := "Сейчас выключено: обычные письма с почтового сервера не стираются."
	if включено {
		действие.Style = "destructive"
		если = fmt.Sprintf("Сейчас включено: письма старше %s Сёма стирает с почтового сервера сам.",
			plural(срок, "дня", "дней", "дней"))
	}
	if n := s.eng.MessagesUnderTTL(); n > 0 {
		если += fmt.Sprintf(" Под срок уже подпадает %s.", plural(n, "письмо", "письма", "писем"))
	}
	add(Component{Type: "section", ID: "mail-common-danger", Title: "Опасное · сохраняется отдельно", Style: "danger",
		Children: []Component{
			{
				Type: "form", ID: "опасное-хранение", Style: formRows,
				Fields: []Field{
					{
						Type: "toggle", Name: "purge_other",
						Label: "Стирать с почтового сервера и обычные письма",
						Hint: "это ваша переписка, а не спам. Включив, вы разрешаете Сёме удалять её с почтового сервера " +
							"по расписанию и без вопросов — так же, как он стирает спам. У самого Сёмы копия остаётся " +
							"навсегда, но в почте (на телефоне, в веб-интерфейсе, у почтового клиента) писем не будет",
						Value: boolValue(включено),
					},
					{
						Type: "number", Name: "keep_days", Label: "Обычные письма стирать через",
						Unit: "дней", Min: 1, Max: 3650,
						Value: strconv.Itoa(срок),
						Hint:  "считается от даты письма; спам сюда не входит — у него свой срок выше",
					},
				},
				Submit: &действие,
			},
			{Type: "row", Layout: "wide", ID: "опасное-хранение-итог", Subtitle: "Сейчас", Caption: strings.TrimPrefix(если, "Сейчас ")},
		}})
}

// ---- помощники, общие для вкладок ----

// Назначение и режим ящика — подписи сегментов и обратно.
var (
	purposeTitles = [][2]string{{store.PurposePersonal, "личный"}, {store.PurposeWork, "рабочий"}}
	modeTitles    = [][2]string{{store.ModeTidy, "читает и наводит порядок"}, {store.ModeReadOnly, "только читает"}}
)

func purposeTitle(c *store.MailConf) string {
	if c.IsWork() {
		return "рабочий"
	}
	return "личный"
}

func modeTitle(c *store.MailConf) string {
	if c.ReadOnly() {
		return "только читает"
	}
	return "читает и наводит порядок"
}

func purposeByTitle(title string) string {
	for _, p := range purposeTitles {
		if p[1] == title || p[0] == title {
			return p[0]
		}
	}
	return store.PurposePersonal
}

func modeByTitle(title string) string {
	for _, m := range modeTitles {
		if m[1] == title || m[0] == title {
			return m[0]
		}
	}
	return store.ModeTidy
}

func titlesOf(pairs [][2]string) []string {
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p[1])
	}
	return out
}

func authTitlesInOrder() []string {
	out := make([]string, 0, len(authOrder))
	for _, a := range authOrder {
		out = append(out, authTitles[a])
	}
	return out
}

// splitTarget разбирает «ящик~значение».
func splitTarget(target string) (string, string) {
	account, rest, _ := strings.Cut(target, "~")
	return account, rest
}

// secretHint — что показать вместо секрета: значение наружу не уходит,
// человеку сообщается только, задан он или нет.
func secretHint(v string) string {
	if v == "" {
		return "не задан"
	}
	return "задан · введите новый, чтобы заменить"
}

// threeStates — «да», «нет» или «как у всех». Не решено — это не «нет»:
// ящик, которому ничего не назначали, должен идти за общей настройкой, а
// не молча остаться при своём.
func threeStates(v *bool) string {
	switch {
	case v == nil:
		return "как у всех"
	case *v:
		return "да"
	default:
		return "нет"
	}
}

// parseThree — обратное превращение. Пустая строка и «как у всех» —
// одно и то же: поле не прислали или человек вернул умолчание.
func parseThree(s string) *bool {
	да, нет := true, false
	switch s {
	case "да":
		return &да
	case "нет":
		return &нет
	}
	return nil
}

// Виртуальные ярлыки Gmail — не папки: переносить в них нельзя, письмо и так
// «во всей почте». В списках приёмников их не предлагаем.
var virtualFolderWords = []string{"Вся почта", "All Mail", "Помеченные", "Starred", "Важные", "Важное", "Important", "Черновики", "Drafts", "Корзина", "Trash"}

func isVirtualFolder(name string) bool {
	for _, w := range virtualFolderWords {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// realFolders — настоящие папки плюс текущее значение (даже неудачное), чтобы
// список показывал, что выбрано сейчас, и «Archive», которую Сёма создаст сам.
func realFolders(folders []string, current string) []string {
	var out []string
	for _, f := range folders {
		if !isVirtualFolder(f) {
			out = append(out, f)
		}
	}
	if current != "" && !hasString(out, current) {
		out = append([]string{current}, out...)
	}
	if !hasString(out, "Archive") {
		out = append(out, "Archive")
	}
	return out
}

func hasString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func archiveHint(c *store.MailConf) string {
	if isVirtualFolder(c.Archive) {
		return "«" + c.Archive + "» — ярлык Gmail, а не папка: перенос в него ничего не делает. Выберите настоящую папку, например Archive — Сёма создаст её сам"
	}
	return "Разобранные письма уезжают сюда; если такой папки нет, Сёма создаст её сам"
}

// Глубина просмотра «Входящих»: дни или «всё время» (в базе -1).
var fetchOptions = []string{"1 день", "3 дня", "7 дней", "14 дней", "30 дней", "90 дней", "365 дней", "всё время"}

func fetchTitle(days int) string {
	if days < 0 {
		return "всё время"
	}
	for _, o := range fetchOptions {
		if strings.HasPrefix(o, strconv.Itoa(days)+" ") {
			return o
		}
	}
	return strconv.Itoa(days) + " дней"
}

func fetchDays(title string) int {
	if strings.HasPrefix(title, "всё") {
		return -1
	}
	v, _ := strconv.Atoi(strings.Fields(title + " 0")[0])
	return v
}

// portLine — пустая строка вместо нуля: «0» в поле порта человек
// прочитает как настройку, а это просто «не задано».
func portLine(п int) string {
	if п == 0 {
		return ""
	}
	return strconv.Itoa(п)
}
