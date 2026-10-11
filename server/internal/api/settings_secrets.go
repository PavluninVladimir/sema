package api

// Раздел «Ключи и секреты»: три вкладки — Секреты · Шифрование · Журнал. У
// каждого секрета свой экран (settings_secret.go) и свой экран добавления
// (settings_secret_new.go).
//
// До 2 октября 2026 это была одна лента на 4,3 экрана: двадцать семь секретов
// подряд, шесть ваших вперемешку с двадцатью одним служебным (`agents:*`,
// `mail:*`, `model:*`, `mcp:*`), у каждого капсула «никем не используется ·
// ни разу не брали», у корневого сертификата агентов кнопка «Удалить» вровень с
// остальными, а внизу форма добавления, развёрнутая всегда. Теперь:
//
//   - вкладка «Секреты» делит список на «Ваши ключи и токены» и «Заводятся
//     сами» (свёрнуто, без «Удалить»): служебным секрет делает не имя, а тот,
//     кто его завёл (`store.Secret.CreatedBy`);
//   - строка тонкая: имя, вид, не больше двух капсул, одна фраза, одна кнопка,
//     «···»; вся строка ведёт на экран секрета;
//   - «Добавить секрет» — одна кнопка шапки, а добавление — свой экран;
//   - «Шифрование» переехало сюда с «Инструментов»: ключ, «N из N
//     зашифровано», смена ключа;
//   - «Журнал» — последней вкладкой, двадцать строк и «Показать ещё», отбор:
//     чтения · отказы · изменения.
//
// Значение секрета не попадает на экран ни в каком виде — ни в строке, ни в
// подсказке, ни в «Скопировать»: только имя, вид, кто пользуется и кто брал.
// Открытие экрана само по себе в журнал «чтение» не пишет: экран спрашивает
// список и счётчики, а не значение.
//
// Адреса:
//
//	settings:secrets                  раздел, вкладка «Секреты»
//	settings:secrets~crypt            «Шифрование»
//	settings:secrets~log              «Журнал» всех секретов
//	settings:secrets@new              «Добавить секрет» (хвост «~name:<имя>» вписывает имя)
//	settings:secrets@<имя>            экран секрета, «Обзор»
//	settings:secrets@<имя>~grants|log

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const secretsScreen = "settings:secrets"

// Внутренние вкладки. У раздела три: Секреты, Шифрование, Журнал; у секрета
// три: Обзор, Кому выдан, Журнал.
const (
	secretsTabList  = ""       // раздел: Секреты; секрет: Обзор
	secretsTabCrypt = "crypt"  // раздел: Шифрование
	secretsTabLog   = "log"    // раздел: Журнал всех секретов; секрет: Журнал одного
	secretTabGrants = "grants" // секрет: Кому выдан

	// secretsNew — «секрет» с этим именем означает экран добавления
	secretsNew = "new"
	// secretsNewName — хвост адреса «@new»: имя, которое экран вписывает сам
	// («модуль просит имя, которого нет» ведёт сюда)
	secretsNewName = "name:"
)

// secretsLogPage — сколько строк журнала приезжает первой страницей.
const secretsLogPage = 20

// secretsAddr — разобранный адрес экрана «Ключей и секретов».
type secretsAddr struct {
	Name string // имя секрета; пусто — раздел; «new» — «Добавить секрет»
	Tab  string // вкладка
}

func parseSecretsAddr(screen string) secretsAddr {
	_, id, tab := splitEntity(screen)
	return secretsAddr{Name: strings.TrimSpace(id), Tab: tab}
}

func (s *Server) buildSecrets(doc *Document, screen, filter string, limit int) error {
	a := parseSecretsAddr(screen)
	doc.Layout = "wide"
	switch {
	case a.Name == secretsNew:
		return s.buildSecretNew(doc, a.Tab)
	case a.Name != "":
		return s.buildSecret(doc, a, filter, limit)
	}
	return s.buildSecretsSection(doc, a.Tab, filter, limit)
}

// secretsGroups — секреты на ваши и служебные, каждая группа по имени.
func secretsGroups(list []*store.Secret) (own, service []*store.Secret) {
	for _, sec := range list {
		if sec.Service() {
			service = append(service, sec)
		} else {
			own = append(own, sec)
		}
	}
	return own, service
}

// secretsTabs — вкладки раздела. Счётчик у вкладки говорит, что там есть
// чинить или решать, до того как человек туда зайдёт: нет ключа шифрования —
// беда, и видна она с полосы вкладок.
func (s *Server) secretsTabs(own int, waiting int) []innerTab {
	st := s.eng.Store()
	tabs := []innerTab{
		{ID: secretsTabList, Name: "Секреты", Count: strconv.Itoa(own)},
		{ID: secretsTabCrypt, Name: "Шифрование"},
		{ID: secretsTabLog, Name: "Журнал"},
	}
	if waiting > 0 {
		tabs[0].Count, tabs[0].Level = strconv.Itoa(waiting)+" ждут", LevelWarn
	}
	total, sealed := st.SecretCounts()
	switch {
	case !st.SecretsEncrypted():
		tabs[1].Count, tabs[1].Level = "нет ключа", LevelAlert
	case st.SecretProblem() != "":
		tabs[1].Count, tabs[1].Level = "не читается", LevelAlert
	case sealed < total:
		tabs[1].Count, tabs[1].Level = strconv.Itoa(total-sealed)+" открыто", LevelAlert
	}
	if n := st.CountSecretLogFiltered("", store.SecretLogDenied); n > 0 {
		tabs[2].Count, tabs[2].Level = plural(n, "отказ", "отказа", "отказов"), LevelWarn
	}
	return tabs
}

// secretAsk — модуль просит секрет, а человек ещё не выдал.
type secretAsk struct {
	Module *engine.Module
	Secret string
}

// secretAsks — кто чего просит и не получил, по секретам, которые есть в
// хранилище. Один проход по модулям, а не по одному на секрет.
func (s *Server) secretAsks(known map[string]bool) []secretAsk {
	var out []secretAsk
	for _, m := range s.eng.Modules() {
		for _, name := range m.Secrets {
			if known[name] && !m.HasGrant(name) {
				out = append(out, secretAsk{Module: m, Secret: name})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Secret != out[j].Secret {
			return out[i].Secret < out[j].Secret
		}
		return out[i].Module.Title < out[j].Module.Title
	})
	return out
}

func (s *Server) buildSecretsSection(doc *Document, tab, filter string, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Ключи и секреты"
	list, err := st.VaultSecrets()
	if err != nil {
		return err
	}
	own, service := secretsGroups(list)
	known := map[string]bool{}
	for _, sec := range list {
		known[sec.Name] = true
	}
	asks := s.secretAsks(known)
	// то, что ждёт вас: число у вкладки — число строк полки, а полка — вверху
	// первой вкладки
	shelf := s.secretsShelf(asks)
	waiting := 0
	if shelf != nil {
		waiting = len(shelf.Children)
	}
	doc.Subheading = secretsPhrase(len(own), len(service))
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	setSubtabs(doc, secretsScreen, tab, s.secretsTabs(len(own), waiting))
	// «Добавить секрет» — единственная кнопка шапки раздела и стоит на всех
	// вкладках: шапка не меняется от вкладки к вкладке
	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(secretsScreen, secretsNew, ""),
		Label: "Добавить секрет", Style: "primary"}}

	switch tab {
	case secretsTabCrypt:
		s.secretsCrypt(add)
		return nil
	case secretsTabLog:
		return s.secretsJournal(doc, "", filter, limit)
	}

	// то, что ждёт вас, стоит вверху: ключа нет, модуль просит секрет, имя без
	// секрета
	if shelf != nil {
		add(*shelf)
	}

	index := s.eng.SecretConsumerIndex()
	askBy := map[string][]secretAsk{}
	for _, a := range asks {
		askBy[a.Secret] = append(askBy[a.Secret], a)
	}

	rows := make([]Component, 0, len(own))
	for _, sec := range own {
		rows = append(rows, s.secretRow(sec, index[sec.Name], askBy[sec.Name]))
	}
	if len(rows) == 0 {
		empty := emptyState("Своих ключей и токенов пока нет",
			"Сюда попадают ключи SSH, токены и пароли, которые завели вы: Сёма хранит их закрытыми и выдаёт модулям поимённо. Ключ SSH он может сгенерировать сам", "key")
		empty.Actions = []Action{{Action: "$goto", Target: entityAddr(secretsScreen, secretsNew, ""), Label: "Добавить секрет", Style: "primary"}}
		rows = append(rows, empty)
	}
	add(Component{Type: "section", ID: "secrets-own", Title: "Ваши ключи и токены · " + strconv.Itoa(len(own)), Children: rows})

	// служебные: свёрнуты и без «Удалить». Секреты ящиков и моделей, которые
	// живут не в хранилище, а у своих владельцев, показываются здесь же
	srows := make([]Component, 0, len(service)+1)
	// у свёрнутого раздела подписи нет, поэтому объяснение — первой строкой внутри
	srows = append(srows, Component{Type: "text", Style: "secondary",
		Text: "Ключи провайдеров, пароли ящиков, сертификаты агентов и прочее, что Сёма заводит для своих дел. " +
			"Здесь они видны, но не меняются и не удаляются: у каждого свой владелец."})
	for _, sec := range service {
		srows = append(srows, s.secretRow(sec, index[sec.Name], askBy[sec.Name]))
	}
	srows = append(srows, s.foreignSecretRows()...)
	add(Component{Type: "section", ID: "secrets-service", Title: "Заводятся сами · " + strconv.Itoa(len(service)),
		Collapsed: true, Children: srows})

	add(Component{Type: "section", ID: "secrets-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Значение секрета не показывается никогда — только заменяется. Модуль получает секрет по имени и только если вы выдали это имя; приватную часть ключа SSH и токены входа на серверы MCP не получает никто, кроме ядра. " +
			"Каждое обращение к секрету — выданное и отказанное — остаётся в журнале."}}})
	return nil
}

// secretsPhrase — строка под заголовком раздела.
func secretsPhrase(own, service int) string {
	phrase := "Значения закрыты, доступ выдаётся поимённо, каждое обращение видно."
	switch {
	case own == 0 && service == 0:
		return "Секретов пока нет. " + phrase
	case own == 0:
		return plural(service, "служебный секрет", "служебных секрета", "служебных секретов") + " Сёмы; своих ключей нет. " + phrase
	}
	return plural(own, "ваш ключ или токен", "ваших ключа или токена", "ваших ключей и токенов") + ", " +
		plural(service, "служебный", "служебных", "служебных") + ". " + phrase
}

// secretsShelf — полка «Ждут вас»: ключа шифрования нет, модуль просит секрет и
// не получил его, модуль просит имя, которого в хранилище нет. Нет ничего —
// полки нет.
func (s *Server) secretsShelf(asks []secretAsk) *Component {
	st := s.eng.Store()
	var rows []Component
	if !st.SecretsEncrypted() {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secrets-no-key", Level: LevelAlert, Dot: levelDot(LevelAlert),
			Subtitle: "Ключ шифрования не задан",
			Caption: "Секреты кладутся в базу только закрытыми. Запустите сервер с переменной " + s.eng.KeyEnvName() +
				" — и добавлять секреты станет можно",
			Actions: []Action{{Action: "$goto", Target: entityAddr(secretsScreen, "", secretsTabCrypt), Label: "Шифрование", Style: "primary"}}})
	}
	for _, a := range asks {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "ask-" + a.Module.Name + "-" + a.Secret, Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Модуль «" + a.Module.Title + "» просит секрет «" + a.Secret + "»",
			Caption:  "Он назван в манифесте модуля; пока вы не выдали, модуль его не получит",
			Actions: []Action{
				{Action: "$goto", Target: entityAddr(secretsScreen, a.Secret, secretTabGrants), Label: a.Secret},
				{Action: "grant_secret", Target: a.Module.Name + "~" + a.Secret, Label: "Выдать", Style: "primary"},
			}})
	}
	// модуль просил имя, которого в хранилище нет: без этой строки человек не
	// узнает, чего модулю не хватает
	if orphans, err := st.OrphanSecretRequests(); err == nil {
		for _, u := range orphans {
			rows = append(rows, Component{Type: "row", Layout: "wide", ID: "orphan-" + u.Name, Level: LevelWarn, Dot: levelDot(LevelWarn),
				Subtitle: "Просили секрет «" + u.Name + "», а такого нет",
				Caption:  u.Who + " просил · " + whenText(u.At) + " · отказано: такого секрета нет",
				Actions: []Action{
					{Action: "$goto", Target: entityAddr(secretsScreen, u.Name, secretsTabLog), Label: u.Name},
					{Action: "$goto", Target: entityAddr(secretsScreen, secretsNew, secretsNewName+u.Name), Label: "Завести", Style: "primary"},
				}})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return &Component{Type: "shelf", ID: "secrets-shelf", Title: "Ждут вас", Children: rows}
}

// secretKindPill — вид секрета одной капсулой.
func secretKindPill(sec *store.Secret) Badge {
	return Badge{Text: engine.SecretKindTitle(sec.Kind)}
}

// secretTile — буква и цвет плитки: вид отличает глазом, а служебный серый.
func secretTile(sec *store.Secret) (letter, color string) {
	letter = firstRuneOfName(sec.Name)
	if sec.Service() {
		return letter, "#8E8E93"
	}
	switch sec.Kind {
	case store.SecretSSH:
		return letter, "#2F6FEB"
	case store.SecretToken:
		return letter, "#7A5AE0"
	case store.SecretPassword:
		return letter, "#C7532F"
	}
	return letter, "#4E9A51"
}

// secretUsage — одна фраза про то, кто пользуется секретом: «используют:
// сервер «Microtik», модуль «Серверы»». Больше трёх — «и ещё N».
func secretUsage(consumers []engine.SecretConsumer) string {
	if len(consumers) == 0 {
		return "никем не используется"
	}
	var titles []string
	for _, c := range consumers {
		titles = append(titles, c.Title)
	}
	if len(titles) > 3 {
		return "используют: " + strings.Join(titles[:3], ", ") + " и ещё " + strconv.Itoa(len(titles)-3)
	}
	return "используют: " + strings.Join(titles, ", ")
}

// secretRow — тонкая строка списка: имя, вид, не больше двух капсул, одна
// фраза, одна кнопка, «···». Вся строка ведёт на экран секрета. Капсула — только
// если отличает: отказы, кому-то нужен и не выдан. Вид — первая и всегда
// стоит, поэтому вторая одна.
func (s *Server) secretRow(sec *store.Secret, consumers []engine.SecretConsumer, asks []secretAsk) Component {
	st := s.eng.Store()
	letter, color := secretTile(sec)
	pills := []Badge{secretKindPill(sec)}

	// отказы и просьбы — то, что отличает строку от соседних
	denials, lastDenied := st.SecretDenials(sec.Name)
	level := ""
	phrase := secretUsage(consumers)
	switch {
	case denials > 0:
		pills = append(pills, Badge{Text: plural(denials, "отказ", "отказа", "отказов"), Level: LevelAlert})
		level = LevelAlert
		if lastDenied != nil {
			phrase = lastDenied.Who + " просил · " + whenText(lastDenied.At) + " · отказано: " + lastDenied.Note
		}
	case len(asks) > 0:
		pills = append(pills, Badge{Text: "просят", Level: LevelWarn})
		level = LevelWarn
	}
	// кто брал последним: только у ваших, служебных ядро берёт каждую минуту
	if !sec.Service() && denials == 0 {
		if u := st.LastSecretUse(sec.Name); u != nil {
			phrase += " · брали " + whenText(u.At)
		}
	}

	row := Component{Type: "row", Layout: "wide", ID: "secret-" + sec.Name, Subtitle: sec.Name,
		Avatar: letter, AvatarColor: color, AvatarShape: "square",
		Caption: phrase, Badges: pills}
	if level != "" {
		withLevel(&row, level)
	}
	row.Actions = []Action{{Action: "$goto", Target: entityAddr(secretsScreen, sec.Name, ""), Label: sec.Name}}

	// одна кнопка: выдать тому, кто просит; у ключа SSH — публичная часть
	// (это не секрет, и она нужна чаще всего); иначе строка без кнопки
	var menu []Action
	for i, a := range asks {
		grant := Action{Action: "grant_secret", Target: a.Module.Name + "~" + sec.Name, Label: "Выдать «" + a.Module.Title + "»", Style: "primary"}
		if i == 0 {
			row.Actions = append(row.Actions, grant)
		} else {
			grant.Style = ""
			menu = append(menu, grant)
		}
	}
	if sec.Kind == store.SecretSSH && sec.Public != "" && !sec.Service() {
		copyKey := Action{Action: "$copy", Target: sec.Public, Label: "Скопировать публичный ключ"}
		if len(asks) == 0 {
			row.Actions = append(row.Actions, copyKey)
		} else {
			menu = append(menu, copyKey)
		}
	}
	menu = append(menu,
		Action{Action: "$goto", Target: entityAddr(secretsScreen, sec.Name, secretTabGrants), Label: "Кому выдан"},
		Action{Action: "$goto", Target: entityAddr(secretsScreen, sec.Name, secretsTabLog), Label: "Журнал"})
	if owner := s.secretOwnerAction(consumers); owner != nil && sec.Service() {
		menu = append(menu, *owner)
	}
	row.Menu = menu
	if !sec.Service() {
		row.Actions = append(row.Actions, s.deleteSecretAction(sec, consumers))
	}
	return row
}

// secretOwnerAction — куда идти за секретом, который заводит и меняет не человек:
// к тому, кто им пользуется. Первый из потребителей, у кого есть свой экран.
func (s *Server) secretOwnerAction(consumers []engine.SecretConsumer) *Action {
	for _, c := range consumers {
		if target, label := secretConsumerScreen(c); target != "" {
			return &Action{Action: "$goto", Target: target, Label: label}
		}
	}
	return nil
}

// deleteSecretAction — «Удалить» с вопросом, в котором названо, кто останется
// без ключа. Служебному её не даёт никто: секрет, которым пользуются, нельзя
// удалять не глядя, а служебный не удаляется вовсе.
func (s *Server) deleteSecretAction(sec *store.Secret, consumers []engine.SecretConsumer) Action {
	ask := "Удалить секрет " + sec.Name + "? Значение пропадёт безвозвратно."
	if len(consumers) > 0 {
		var who []string
		for _, c := range consumers {
			who = append(who, c.Title)
		}
		ask += " Без ключа останутся: " + strings.Join(who, ", ") + "."
	} else {
		ask += " Им никто не пользуется, так что ничего не сломается."
	}
	return Action{Action: "delete_secret", Target: sec.Name, Label: "Удалить…", Style: "destructive",
		Confirm: ask, ConfirmOK: "Удалить секрет"}
}

// secretConsumerScreen — куда ведёт потребитель секрета: экран сервера, ящика,
// провайдера. Пусто — у потребителя своего экрана нет.
func secretConsumerScreen(c engine.SecretConsumer) (target, label string) {
	switch c.Kind {
	case engine.ConsumerHost:
		return entityAddr(serversScreen, c.Name, ""), "Сервер"
	case engine.ConsumerMCP, engine.ConsumerMCPLogin:
		return entityAddr(mcpScreen, c.Name, ""), "Сервер MCP"
	case engine.ConsumerMailbox:
		return entityAddr(mailScreen, c.Name, mailTabConn), "Ящик"
	case engine.ConsumerProvider:
		return providerAddr(c.Name, ""), "Провайдер"
	case engine.ConsumerModule:
		return "modules", "Модули"
	case engine.ConsumerCore:
		switch c.Name {
		case engine.CoreAgents:
			return agentsScreen, "Агенты"
		case engine.CorePush:
			return notifySettingsScreen, "Уведомления"
		case engine.CoreTG:
			return tgScreen, "Telegram"
		case engine.CoreOwn:
			return sentScreen, "Что ушло наружу"
		}
	}
	return "", ""
}

// foreignSecretRows — секреты ящиков и моделей, которые лежат не в хранилище, а
// у своих владельцев: где живут, там и правятся. Здесь они только показаны.
func (s *Server) foreignSecretRows() []Component {
	st := s.eng.Store()
	var rows []Component
	for _, a := range s.eng.Accounts() {
		c := st.MailConf(a.Name)
		kind := ""
		switch {
		case c.Auth == store.AuthGoogle || (c.Auth == "" && a.Auth == "gmail"):
			kind = "OAuth-секрет и токен"
		case c.Password != "" || a.Password != "":
			kind = "пароль приложения"
		}
		if kind == "" {
			continue
		}
		rows = append(rows, Component{
			Type: "row", Layout: "wide", Subtitle: a.Name,
			Caption: "ящик · берётся при каждой проверке почты",
			Badges:  []Badge{{Text: kind}},
			// секрет ящика правится на его вкладке «Подключение»
			Actions: []Action{{Action: "$goto", Target: entityAddr(mailScreen, a.Name, mailTabConn), Label: "В настройках ящика"}},
		})
	}
	for _, name := range s.eng.KeyedModels() {
		roles := s.eng.RolesUsing(name)
		usage := "ни одной роли не назначена"
		if len(roles) > 0 {
			usage = "роли «" + strings.Join(roles, "», «") + "» · берётся при каждом ответе"
		}
		rows = append(rows, Component{
			Type: "row", Layout: "wide", Subtitle: name, Caption: usage,
			Badges:  []Badge{{Text: "ключ модели"}},
			Actions: []Action{{Action: "$goto", Target: modelAddr(name), Label: "В моделях"}},
		})
	}
	return rows
}

// ---- «Шифрование» ----

// secretsCrypt — вкладка «Шифрование»: что защищено, а что нет. Обещать больше,
// чем есть, тут опаснее, чем не обещать ничего. До 2 октября 2026 жила на
// «Инструментах» (settings:tools~crypt), оттуда и переехала: ключ шифрования —
// про секреты, а не про чтение файлов. Поведение то же, что было.
func (s *Server) secretsCrypt(add func(Component)) {
	st := s.eng.Store()
	total, sealed := st.SecretCounts()

	key := Component{Type: "row", Layout: "wide", ID: "crypt-key", Subtitle: "Ключ шифрования",
		Caption: "Берётся из переменной окружения SEMA_KEY · в базе и в резервных копиях его нет: копия без ключа неполна, храните ключ (или фразу восстановления от sema -rekey) отдельно",
		Badges:  []Badge{{Text: "задан"}}}
	if !st.SecretsEncrypted() {
		key.Badges = []Badge{{Text: "не задан", Level: LevelAlert}}
		key.Caption = "Задайте переменную SEMA_KEY при запуске — секреты станут закрытыми"
	}
	rows := []Component{key}

	if problem := st.SecretProblem(); problem != "" {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "crypt-problem",
			Subtitle: "Секреты не читаются", Caption: problem,
			Badges: []Badge{{Text: "проверьте ключ", Level: LevelAlert}}})
	}
	sealedLevel := LevelAlert
	if sealed == total {
		sealedLevel = ""
	}
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "crypt-sealed",
		Subtitle: "Что зашифровано",
		Caption: "Ключи моделей, пароли ящиков, секрет OAuth, токен доступа к почте, ключи и секреты хранилища, " +
			"память скриптов · расшифровываются только в момент обращения",
		Actions: []Action{{Action: "$goto", Target: secretsScreen, Label: "Секреты"}},
		Badges:  []Badge{{Text: fmt.Sprintf("%d из %d", sealed, total), Level: sealedLevel}}})

	if openList := st.OpenSecrets(); len(openList) > 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "crypt-open",
			Subtitle: "Лежат открытым текстом", Caption: strings.Join(openList, ", "),
			Badges: []Badge{{Text: "нужен ключ", Level: LevelAlert}}})
	}

	msgs, _ := st.CountMessages()
	files, _ := st.AttachmentStats()
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "crypt-not-sealed",
		Subtitle: "Что не шифруется",
		Caption: fmt.Sprintf("Письма (%d), вложения (%d) и поисковые векторы — по ним идёт поиск, "+
			"зашифрованными они бесполезны. Это уровень шифрования диска: FileVault на маке, LUKS на сервере", msgs, files),
		Badges: []Badge{{Text: "защищается диском"}}})

	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "crypt-stay",
		Subtitle: "Секреты не покидают сервер",
		Caption:  "В приложение приходит только признак «задан» — значение нельзя ни увидеть, ни скопировать"})

	if inFile := s.eng.SecretsInStartupFile(); len(inFile) > 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "crypt-startup-file",
			Subtitle: "В файле запуска остались секреты",
			Caption:  strings.Join(inFile, ", ") + " · они уже перенесены в базу и зашифрованы, файл можно удалить",
			Badges:   []Badge{{Text: "удалите файл", Level: LevelWarn}},
			Actions:  []Action{{Action: "$noop", Target: "file", Label: s.eng.StartupFilePath()}}})
	}
	if st.SecretsEncrypted() {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "crypt-rotate",
			Subtitle: "Смена ключа",
			Caption:  "Секреты перешифруются новым ключом; после этого запускайте сервер с ним, иначе они не прочитаются",
			Actions: []Action{{Action: "rotate_key", Label: "Сменить ключ",
				Prompt: "Новый ключ шифрования", Style: "destructive",
				Confirm:   "Перешифровать секреты новым ключом? Старый ключ после этого не подойдёт.",
				ConfirmOK: "Перешифровать"}}})
	}
	add(Component{Type: "section", ID: "crypt", Title: "Безопасность", Children: rows})
	add(Component{Type: "text", Style: "secondary",
		Text: "Шифрование секретов спасает от утечки базы и резервной копии. От того, у кого есть права на самой " +
			"машине, оно не спасает: ключ доступен процессу — обещать обратное было бы нечестно."})
}

// ---- «Журнал» ----

// secretsLogFilters — чипы журнала по порядку: слово отбора и подпись.
var secretsLogFilters = []struct{ ID, Title string }{
	{store.SecretLogAll, "Все"},
	{store.SecretLogReads, "Чтения"},
	{store.SecretLogDenied, "Отказы"},
	{store.SecretLogChanges, "Изменения"},
}

// secretsJournal — журнал секретов (пустое имя — всех): чипы отбора с числами,
// первая страница в двадцать строк и «Показать ещё». Добавляется к документу как
// есть. Это хроника, а не настройка: на первом экране раздела её нет, она
// приезжает только с вкладкой. Значения в журнале нет и не бывает: только имя,
// кто просил, когда и что вышло.
func (s *Server) secretsJournal(doc *Document, name, filter string, limit int) error {
	st := s.eng.Store()
	// у журнала есть полоса чипов, но нет своего поиска по строкам: верхнее поле
	// «ищи по настройкам» на телефоне ему не нужно
	doc.noPhoneSearch = true
	known := false
	for _, f := range secretsLogFilters {
		doc.Filters = append(doc.Filters, Filter{ID: f.ID,
			Title: fmt.Sprintf("%s · %d", f.Title, st.CountSecretLogFiltered(name, f.ID))})
		if f.ID == filter {
			known = true
		}
	}
	if !known {
		filter = store.SecretLogAll
	}
	doc.ActiveFilter = filter

	if limit <= 0 || limit == defaultRows {
		limit = secretsLogPage
	}
	entries, err := st.SecretLogFiltered(name, filter, limit+1)
	if err != nil {
		return err
	}
	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	rows := make([]Component, 0, len(entries))
	for i, u := range entries {
		rows = append(rows, secretLogRow(u, name == "", i))
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Записей нет",
			"Сюда попадает каждое обращение к секрету — выданное и отказанное — и каждая правка записи: заведён, значение заменено, выдан модулю, удалён. Значение секрета в журнал не попадает", "list.bullet"))
		if filter != store.SecretLogAll {
			rows[0] = emptyState("Под этот отбор ничего не подошло", "Выберите «Все» — там вся хроника этого журнала", "list.bullet")
		}
	}
	title := "Что было с секретами"
	if name != "" {
		title = "Что было с «" + name + "»"
	}
	doc.Components = append(doc.Components, Component{Type: "section", Title: title, Children: rows})
	if more {
		total := st.CountSecretLogFiltered(name, filter)
		doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(rows), total)}}})
	}
	return nil
}

// secretLogRow — строка журнала. Имя секрета в заголовке — только у общего
// журнала: у журнала самого секрета оно лишнее, там заголовок называет, кто
// просил.
func secretLogRow(u store.SecretUse, withName bool, n int) Component {
	badge := Badge{Text: "выдан", Level: LevelOK}
	switch {
	case u.Change:
		badge = Badge{Text: "правка", Level: LevelInfo}
	case !u.Granted:
		badge = Badge{Text: "отказано", Level: LevelAlert}
	}
	caption := whenText(u.At)
	if u.Note != "" {
		caption += " · " + u.Note
	}
	title := u.Who
	if withName {
		title = u.Name
		caption = u.Who + " · " + caption
	}
	if title == "" {
		title = "ядро"
	}
	return Component{Type: "row", Layout: "wide", ID: "secret-log-" + strconv.Itoa(n), Subtitle: title, Caption: caption,
		Badges: []Badge{badge}}
}
