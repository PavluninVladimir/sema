package api

// Обзор настроек. Шестнадцать разделов в две строки чипов человек не
// помнит: он ищет «где Telegram» и «почему Сёма не решает сам». Обзор
// отвечает на это сразу — по каждому разделу видно, за что он отвечает и
// что там сейчас намерено.
//
// Группы отвечают на четыре вопроса, а не называют слои системы: откуда
// Сёма берёт переписку, что он умеет, где работает, кто что видит.
//
// Норма молчит. Зелёная точка стояла у всех шестнадцати разделов и не
// значила ничего; теперь пометка появляется, только когда есть о чём
// сказать — жёлтая «работает не так, как задумано», красная «без вас
// дальше не поедет». Померенное (числа, адреса, даты) идёт метой:
// клиент набирает её моноширинным.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const modelsScreen = "settings:models"

// settingsSection — что известно про раздел: как он зовётся, за что
// отвечает и всё ли с ним в порядке. Пустой уровень значит «всё как вы
// оставили»: ни точки, ни капсулы.
//
// Раздел с уровнем даёт строку в «Ждут решения» на «Главном», и там у
// него обязаны быть «Беда» — что случилось и чем это грозит — и кнопка,
// ведущая прямо к настройке, которая беду устраняет. «Посмотрите ниже»
// замечанием не считается: искать глазами человек не должен.
type settingsSection struct {
	Screen, Title, Why, Meta string
	Level, Pill              string
	// Count — сколько здесь того, что ждёт решения; у колонки разделов это
	// число рядом с названием. Ноль — число неизвестно (или нет смысла в
	// счёте: «не подключён»), и пометка остаётся точкой
	Count   int
	Trouble string // строка «Ждут решения»; пусто — берём капсулу
	Button  Action // одна и ведёт прямо к настройке
	Plain   bool   // замечание попроще: уходит в свёрнутый список
}

// synonyms — по каким словам человек ищет раздел. Названия разделов он
// не помнит: ищут «пуши», «пароль», «ключи», «сколько денег». Список
// живёт рядом с обзором, потому что это часть текста интерфейса, а не
// данные: он меняется тогда же, когда меняются сами разделы. Провод, опрос и
// пояс жили у «Уведомлений» и переехали на «Работу Сёмы» вместе со словами,
// которыми их ищут: иначе «опрос» перестал бы находить что-либо.
var synonyms = map[string]string{
	"settings:mail":      "почта ящик imap gmail яндекс письма папки спам архив oauth smtp отправка ответ черновик подключение пароль хранение стирание срок белый список порог",
	tgScreen:             "телеграм telegram тг чаты группы топики темы каналы бот админ учётка учётки аккаунт аккаунты рабочая личная",
	maxScreen:            "макс max мессенджер чаты",
	translateScreen:      "перевод язык переводить иностранные английский translate",
	notifySettingsScreen: "уведомления пуши push напоминания сроки каналы доставки тихие часы счётчик отложить",
	watchScreen:          "работа сёмы самонаблюдение находки наблюдатель провод опрос экран связь живое обновление склейка часовой пояс время timezone чаты команд пинатель пинки ветка хвост родителя снимок экрана",
	modelsScreen:         "модели llm провайдер ключ api роли токены деньги цена бюджет доверие облако локальная эмбеддер вектор картинки ocr",
	searchScreen:         "поиск интернет perplexity searxng ссылки страницы",
	skillsScreen:         "скиллы инструкции подсказки промпт знания",
	learningScreen:       "развитие самообучение черновики судья скиллы из чата",
	"settings:tools":     "инструменты ocr pdf tesseract офисные файлы песочница",
	serversScreen:        "серверы ssh команды круги отпечаток sudo хозяин ночная смена суд арбитр судья",
	agentsScreen:         "агенты житель машины провод сертификат код память шлюз приглашение способности отпечаток корень удостоверяющий центр",
	mcpScreen:            "интеграции mcp сервисы вебхуки",
	"settings:storages":  "хранилища файлы папки s3 minio зеркало диск",
	accessScreen:         "доступ вход пароль passkey ключ токен сессия вход без входа сеть",
	secretsScreen:        "секреты ключи хранилище пароли токены журнал шифрование sema_key ключ шифрования смена ключа служебные выдача модулям",
	sentScreen:           "что ушло наружу облако класс секретное чувствительное журнал отправок",
	changesScreen:        "журнал настроек история правок кто когда менял параметр аудит",
}

// matchesWords — виден ли раздел по запросу. Ищем по названию, пояснению,
// мете и синонимам: человек ищет «пуши», а раздел называется
// «Уведомления».
func matchesWords(r settingsSection, req string) bool {
	if req == "" {
		return true
	}
	// по рунам, без регистра и без различия «е» и «ё» (normText): «ключи» и
	// «Ключи», «счёт» и «счет» — одно и то же слово
	стог := normText(r.Title + " " + r.Why + " " + r.Meta + " " + r.Pill + " " + r.Screen + " " + synonyms[r.Screen])
	for _, слово := range queryWords(req) {
		if !strings.Contains(стог, слово) {
			return false
		}
	}
	return true
}

// все разделы — один список: из него и ищут, и собирают замечания.
// Порядок тот же, что во вкладках каркаса.
func (s *Server) allSections() []settingsSection {
	return []settingsSection{
		s.overviewMail(), s.overviewTelegram(), s.overviewMAX(), s.overviewTranslate(), s.overviewNotifications(),
		s.overviewModels(), s.overviewSearch(), s.overviewSkills(), s.overviewLearning(), s.overviewTools(),
		s.overviewHosts(), s.overviewAgents(), s.overviewIntegrations(), s.overviewStorages(), s.overviewWatch(),
		s.overviewAccess(), s.overviewSecrets(), s.overviewSent(), s.overviewSettingsLog(),
	}
}

// buildSettingsOverview — вкладка «Главное».
//
// Первый экран настроек обычно делают оглавлением — и он бесполезен:
// человек приходит сюда не «посмотреть список разделов», а потому что
// что-то перестало работать или он хочет проверить, всё ли цело.
// Оглавление тут и не нужно — его роль играют пять вкладок сверху.
//
// Поэтому блока три: что сломано и чем это чинить, кто сейчас подключён
// и жив ли, что менялось за последние дни. Поиск по разделам остаётся
// четвёртым: названия человек не помнит, он ищет «пуши» и «ключи».
func (s *Server) buildSettingsOverview(doc *Document, search string, limit int) error {
	// заголовок называется как строка колонки: «Обзор» в колонке и «Настройки»
	// над содержимым читались как два разных места
	doc.Heading = "Обзор"
	doc.Subheading = "Что ждёт решения, что подключено и живо ли, что вы меняли сами"
	doc.Layout = "wide"
	doc.Searchable = true
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	req := strings.TrimSpace(search)
	s.sectionIndex(doc)
	разделы := doc.sections

	// с запросом Обзор становится поиском по настройкам: искать в подключениях
	// и журнале человеку незачем, он ищет настройку (settings_search.go)
	if req != "" {
		doc.Heading = "Нашлось «" + req + "»"
		doc.Subheading = searchSubheading
		for _, block := range s.searchBlocks(doc, req, limit) {
			add(block)
		}
		return nil
	}

	s.awaitingDecision(add, разделы)
	s.connections(add)
	s.handySettings(add)
	s.recentEdits(add, doc.guest)
	return nil
}

// awaitingDecision — что сломано и чем это чинить. У каждой строки ровно одна
// кнопка, и она ведёт прямо к настройке, которая беду устраняет: сегодня
// на «Моделях» четыре замечания занимают весь первый экран, и ни у
// одного нет действия — человек читает «выберите другую в строке роли
// ниже» и идёт искать её глазами.
//
// Замечания попроще уходят в свёрнутый список: «у пятидесяти моделей не
// названа цена» не должно стоять вровень с «роль не отвечает». Но и
// пропадать не должно — у каждого своя строка и своя кнопка.
func (s *Server) awaitingDecision(add func(Component), разделы []settingsSection) {
	var срочные, попроще []Component
	строка := func(r settingsSection) Component {
		title := r.Trouble
		if title == "" {
			// раздел с уровнем, но без объяснения: капсула хотя бы
			// называет, что не так, — молчать нельзя
			title = r.Title + " · " + r.Pill
		}
		кнопка := r.Button
		if кнопка.Action == "" {
			кнопка = Action{Action: "$goto", Target: r.Screen, Label: "Открыть"}
		}
		кнопка.Style = "primary"
		// стрелка говорит, что команда уводит на экран настройки, а не
		// чинит на месте. Глагол оставляем свой у каждого замечания:
		// «Выбрать модель» и «Подключить ящик» говорят, что произойдёт,
		// а общее «Исправить» этого не говорит и одинаково для всех бед
		if !strings.HasSuffix(кнопка.Label, "→") {
			кнопка.Label += " →"
		}
		c := Component{Type: "row", Layout: "wide", ID: "беда-" + r.Screen,
			Subtitle: title, Caption: r.Meta,
			Facts:   []Prop{{Label: "Раздел", Value: r.Title}},
			Actions: []Action{кнопка}}
		withLevel(&c, r.Level)
		return c
	}
	for _, r := range разделы {
		if r.Level == "" || r.Level == LevelOK {
			continue
		}
		if r.Plain && r.Level != LevelAlert {
			попроще = append(попроще, строка(r))
			continue
		}
		срочные = append(срочные, строка(r))
	}
	if len(срочные) == 0 && len(попроще) == 0 {
		add(Component{Type: "shelf", Title: "Ждут решения", Meta: "ничего", Children: []Component{{
			Type: "row", Layout: "wide", Level: LevelOK, Subtitle: "Всё работает так, как вы оставили",
			Caption: "сюда попадает только то, что без вас дальше не поедет: роль без модели, сервер со сменившимся отпечатком, команда на подтверждении"}}})
		return
	}
	if len(срочные) > 0 {
		add(Component{Type: "shelf", Title: "Ждут решения",
			Meta:     plural(len(срочные), "замечание", "замечания", "замечаний") + " · кнопка открывает саму настройку",
			Children: срочные})
	}
	if len(попроще) > 0 {
		add(Component{Type: "section", ID: "беды-попроще", Collapsed: len(срочные) > 0,
			Title:    "Ещё " + plural(len(попроще), "замечание попроще", "замечания попроще", "замечаний попроще"),
			Children: попроще})
	}
}

// foundInSettings — разделы настроек по запросу: название, назначение,
// слова-приметы. Зовётся и с «Главного», и с любого раздела.
func (s *Server) foundInSettings(разделы []settingsSection, req string, guest bool) []Component {
	var kids []Component
	for _, r := range разделы {
		// «Вход и доступ» гостю (токен без права «всё») не показывается и в поиске
		if guest && baseScreen(r.Screen) == accessScreen {
			continue
		}
		if !matchesWords(r, req) {
			continue
		}
		c := Component{Type: "row", Layout: "wide", ID: "set-" + r.Screen,
			Subtitle: r.Title, Caption: r.Why, Meta: r.Meta,
			Actions: []Action{{Action: "$goto", Target: r.Screen, Label: "Открыть"}}}
		if r.Level != "" && r.Level != LevelOK {
			withLevel(&c, r.Level)
			if r.Pill != "" {
				c.Badges = []Badge{{Text: r.Pill, Level: r.Level}}
			}
		}
		kids = append(kids, c)
	}
	return kids
}

// sectionSearch — набранное в верхнем поле на разделе настроек: сверху
// «Нашлось в настройках», ниже сам раздел, а его ленивые части получают
// тот же запрос (app.js). Раньше набранное уводило с раздела на «Главное»,
// и список из 93 чатов на «MAX» искать было нечем.
func (s *Server) sectionSearch(doc *Document, req string, limit int) {
	s.sectionIndex(doc)
	blocks := s.searchBlocks(doc, req, limit)
	// ничего не нашлось: пустое место с объяснением тут слишком громко, раздел
	// ниже честно ищется в своих списках
	if len(blocks) == 1 && blocks[0].Style == "empty" {
		blocks = []Component{{Type: "text", Style: "secondary",
			Text: "В настройках по «" + req + "» ничего не нашлось; ниже ищется в списках этого раздела"}}
	}
	doc.Components = append(blocks, doc.Components...)
}

// connections — всё, что у Сёмы есть снаружи: почта, мессенджеры, модели,
// серверы, хранилища. Пять строк, и ни одной больше: строка отвечает
// «живо или нет» точкой и одной фразой, а числа идут парами «подпись —
// значение», а не склейкой через точку.
func (s *Server) connections(add func(Component)) {
	st := s.eng.Store()
	строка := func(id, name, что string, level string, факты []Prop, куда string) Component {
		c := Component{Type: "row", Layout: "wide", ID: "связь-" + id,
			Subtitle: name, Caption: что, Facts: факты,
			Actions: []Action{{Action: "$goto", Target: куда, Label: "Открыть"}}}
		withLevel(&c, level)
		return c
	}
	var rows []Component

	// почта
	accounts := s.eng.Accounts()
	папок := 0
	for _, a := range accounts {
		папок += len(s.eng.MailConf(a.Name).Folders)
	}
	писем, _ := st.CountMessages()
	почтаУровень, почтаЧто := LevelOK, "Сёма читает ящики и наводит в них порядок"
	if len(accounts) == 0 {
		почтаУровень, почтаЧто = LevelWarn, "ни одного ящика — читать нечего"
	}
	rows = append(rows, строка("mail", "Почта", почтаЧто, почтаУровень, []Prop{
		{Label: "Ящиков", Value: strconv.Itoa(len(accounts))},
		{Label: "Папок в разборе", Value: strconv.Itoa(папок)},
		{Label: "Писем в архиве", Value: strconv.Itoa(писем)},
	}, "settings:mail"))

	// мессенджеры: Telegram и MAX одной строкой — это одно и то же дело
	tgЕсть := false
	if _, err := s.eng.TGCreds(); err == nil {
		for _, acc := range s.eng.TGAccounts() {
			if acc.State == "ok" {
				tgЕсть = true
			}
		}
	}
	tgChats, _ := st.TGChats("", false) // все учётки разом: строка про Telegram целиком
	tgПрав := 0
	for _, c := range tgChats {
		if c.Collect || c.Reply || c.Send {
			tgПрав++
		}
	}
	maxAcc, maxЕсть := st.MAXAccountGet()
	maxChats, _ := st.MAXChats(false)
	maxВыбран := 0
	for _, c := range maxChats {
		if c.Collect {
			maxВыбран++
		}
	}
	мессУровень, мессЧто := LevelOK, "оба на связи и от вашего имени"
	switch {
	case !tgЕсть && !(maxЕсть && maxAcc.State == "ok"):
		мессУровень, мессЧто = LevelWarn, "ни один не подключён — переписка идёт мимо Сёмы"
	case !tgЕсть:
		мессУровень, мессЧто = LevelWarn, "Telegram не подключён"
	case !(maxЕсть && maxAcc.State == "ok"):
		мессУровень, мессЧто = LevelInfo, "MAX не подключён"
	}
	rows = append(rows, строка("chats", "Мессенджеры", мессЧто, мессУровень, []Prop{
		{Label: "Telegram", Value: fmt.Sprintf("%d чатов, права у %d", len(tgChats), tgПрав)},
		{Label: "MAX", Value: fmt.Sprintf("%d чатов, выбран %d", len(maxChats), maxВыбран)},
	}, tgScreen))

	// модели
	// модели собираются одним проходом: прежняя сборка спрашивала цену у
	// каждой модели отдельно, и на каждый экран настроек приходились сотни
	// чтений из базы
	models := s.eng.ModelCards()
	// эмбеддер не роль и в счёт ролей не входит: он живёт среди служебных
	// ролей, и строкой таблицы его нигде не показывают
	roles := peerRoles(s.eng.Roles())
	занято, безЦены := 0, 0
	for _, r := range roles {
		if r.Model != "" && r.Model != "нет" {
			занято++
		}
	}
	for _, m := range models {
		if m.NoPrice() {
			безЦены++
		}
	}
	day := s.eng.SentToday()
	модУровень, модЧто := LevelOK, "роли заняты, расход считается"
	switch {
	case len(models) == 0:
		модУровень, модЧто = LevelAlert, "ни одной модели — думать нечем"
	case безЦены > 0:
		модУровень = LevelWarn
		модЧто = "у " + plural(безЦены, "модели не названа цена", "моделей не названа цена", "моделей не названа цена") +
			" — их расход считается нулём"
	}
	rows = append(rows, строка("models", "Модели", модЧто, модУровень, []Prop{
		{Label: "Подключено", Value: strconv.Itoa(len(models))},
		{Label: "Ролей занято", Value: fmt.Sprintf("%d из %d", занято, len(roles))},
		{Label: "Отправок за сутки", Value: strconv.Itoa(day.Calls)},
	}, modelsScreen))

	// серверы
	hosts, _ := st.Hosts()
	отвечают, сами := 0, 0
	for _, h := range hosts {
		if h.LastErr == "" && h.Fingerprint != "" {
			отвечают++
		}
		if h.Auto {
			сами++
		}
	}
	servУровень, servЧто := LevelOK, "все отвечают"
	switch {
	case len(hosts) == 0:
		servУровень, servЧто = LevelInfo, "ни одного сервера"
	case отвечают < len(hosts):
		servУровень = LevelWarn
		servЧто = fmt.Sprintf("%d из %d не отвечают", len(hosts)-отвечают, len(hosts))
	}
	rows = append(rows, строка("hosts", "Серверы", servЧто, servУровень, []Prop{
		{Label: "Машин", Value: strconv.Itoa(len(hosts))},
		{Label: "Отвечают", Value: strconv.Itoa(отвечают)},
		{Label: "Сёма ведёт сам", Value: strconv.Itoa(сами)},
	}, serversScreen))

	// хранилища
	файлов, байт, _ := st.FileStats()
	storУровень, storЧто := LevelOK, "файлы лежат на месте"
	зеркало := "нет"
	storages, _ := st.Storages()
	for _, c := range storages {
		if c.Primary {
			continue
		}
		done, all := st.MirrorProgress(c.Name)
		зеркало = fmt.Sprintf("%s · %d из %d", c.Name, done, all)
		if all-done > 0 {
			storУровень = LevelWarn
			storЧто = fmt.Sprintf("зеркало отстаёт: %d файлов туда не уехали", all-done)
		}
	}
	rows = append(rows, строка("files", "Хранилища", storЧто, storУровень, []Prop{
		{Label: "Файлов", Value: strconv.Itoa(файлов)},
		{Label: "Занято", Value: humanBytes(байт)},
		{Label: "Зеркало", Value: зеркало},
	}, "settings:storages"))

	add(Component{Type: "section", Title: "Подключения", Children: rows})
}

// accessEvent — действие человека с доступом, которого нет ни в журнале настроек,
// ни в журнале секретов: завёл или отозвал ключ устройства, выдал или отозвал
// токен. Блок «Действия с доступом и секретами» с Обзора ушёл (2 октября 2026), а
// эти события жили только в нём: журнал секретов хранит обращения к секретам,
// решения по скиллам — на «Развитии», а ключи и токены — только свои вкладки с
// датами. Чтобы не терять их молча, они встают в «Вы меняли» наравне с правками
// настроек (так же нарисован макет — «Заведён ключ устройства «iPhone»»).
type accessEvent struct {
	When string // RFC3339 — по нему и сортируем
	What string
	Addr string
}

// accessEvents — недавние события с ключами и токенами, новые сверху.
func (s *Server) accessEvents(limit int) []accessEvent {
	st := s.eng.Store()
	var out []accessEvent
	if keys, err := st.Passkeys(); err == nil {
		for _, k := range keys {
			out = append(out, accessEvent{When: k.Created.Format(time.RFC3339),
				What: "Заведён ключ устройства «" + k.Name + "»", Addr: accessScreen})
		}
	}
	if tokens, err := st.Tokens(); err == nil {
		for _, t := range tokens {
			addr := accessScreen + "~" + accessTabTokens
			out = append(out, accessEvent{When: t.Created.Format(time.RFC3339), What: "Выдан токен «" + t.Name + "»", Addr: addr})
			if !t.Revoked.IsZero() {
				out = append(out, accessEvent{When: t.Revoked.Format(time.RFC3339), What: "Отозван токен «" + t.Name + "»", Addr: addr})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When > out[j].When })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *Server) overviewMail() settingsSection {
	r := settingsSection{Screen: "settings:mail", Title: "Почта",
		Why: "ящики, папки, режимы и что из них берётся"}
	accounts := s.eng.Accounts()
	if len(accounts) == 0 {
		r.Meta = "ни одного ящика"
		r.Level, r.Pill = LevelWarn, "не подключена"
		r.Trouble = "Ни одного ящика: Сёма не читает почту"
		r.Button = Action{Action: "$goto", Target: entityAddr(mailScreen, mailNew, ""), Label: "Подключить ящик"}
		return r
	}
	папок := 0
	for _, a := range accounts {
		папок += len(s.eng.MailConf(a.Name).Folders)
	}
	r.Meta = plural(len(accounts), "ящик", "ящика", "ящиков")
	if папок == 0 {
		r.Meta += " · папки ещё не прочитаны с сервера"
	} else {
		r.Meta += " · " + plural(папок, "папка", "папки", "папок")
	}
	if n, err := s.eng.Store().CountByStatus(store.StatusPending); err == nil && n > 0 {
		r.Level, r.Pill, r.Count = LevelWarn, plural(n, "ждёт", "ждут", "ждут"), n
		r.Trouble = plural(n, "письмо ждёт", "письма ждут", "писем ждут") + " вашего решения по спаму"
		r.Button = Action{Action: "$goto", Target: "inbox", Label: "Разобрать"}
		r.Plain = true
	}
	return r
}

func (s *Server) overviewTelegram() settingsSection {
	r := settingsSection{Screen: tgScreen, Title: "Telegram",
		Why: "ваш аккаунт, а не бот: Сёма видит те чаты, что видите вы"}
	if _, err := s.eng.TGCreds(); err != nil {
		r.Meta = "нужны api_id, api_hash и вход по телефону"
		r.Level, r.Pill = LevelWarn, "не подключён"
		r.Trouble = "Telegram не подключён — половина переписки живёт мимо Сёмы"
		r.Button = Action{Action: "$goto", Target: tgScreen + "~" + tgTabConn, Label: "Подключить"}
		r.Plain = true
		return r
	}
	chats, _ := s.eng.Store().TGChats("", false)
	читаем := 0
	for _, c := range chats {
		if c.Collect {
			читаем++
		}
	}
	r.Meta = fmt.Sprintf("%s · читает %d", plural(len(chats), "чат", "чата", "чатов"), читаем)
	if учёток := len(s.eng.TGAccounts()); учёток > 1 {
		r.Meta = plural(учёток, "учётка", "учётки", "учёток") + " · " + r.Meta
	}
	return r
}

func (s *Server) overviewMAX() settingsSection {
	r := settingsSection{Screen: maxScreen, Title: "MAX",
		Why: "второй мессенджер, устроен так же"}
	acc, ok := s.eng.Store().MAXAccountGet()
	if !ok || acc.State != "ok" {
		r.Meta = "вход по телефону не выполнен"
		r.Level, r.Pill = LevelWarn, "не подключён"
		r.Trouble = "MAX не подключён"
		r.Button = Action{Action: "$goto", Target: maxScreen, Label: "Подключить"}
		r.Plain = true
		return r
	}
	chats, _ := s.eng.Store().MAXChats(false)
	читаем := 0
	for _, c := range chats {
		if c.Collect {
			читаем++
		}
	}
	r.Meta = fmt.Sprintf("%s · %s · читает %d", acc.Phone, plural(len(chats), "чат", "чата", "чатов"), читаем)
	return r
}

// overviewTranslate — включён ли перевод и кто переводит. Модель называем
// прямо: от неё зависит, уходит ли текст письма наружу.
func (s *Server) overviewTranslate() settingsSection {
	r := settingsSection{Screen: translateScreen, Title: "Перевод",
		Why: "письма не на вашем языке — сразу по-русски"}
	язык := s.eng.TranslateTo()
	if !s.eng.TranslateOn() {
		r.Meta = "выключен"
		return r
	}
	модель := s.eng.Role(engine.RoleTranslate).Model
	if модель == "" || модель == "нет" {
		r.Level, r.Pill = LevelWarn, "нет модели"
		r.Meta = "включён, но роли «Перевод» не назначена модель"
		r.Trouble = "Перевод включён, а роли «Перевод» не назначена модель — письма остаются на языке оригинала"
		r.Button = Action{Action: "$goto", Target: roleHighlight(engine.RoleTranslate), Label: "Назначить модель"}
		return r
	}
	сделано := s.eng.Store().TranslationStats(язык)
	ждут, _ := s.eng.Store().MessagesToTranslate(язык, 500)
	r.Meta = fmt.Sprintf("на «%s» · %s · %s", язык, модель, plural(сделано, "письмо", "письма", "писем"))
	if len(ждут) > 0 {
		r.Pill = fmt.Sprintf("ждут: %d", len(ждут))
	}
	return r
}

func (s *Server) overviewNotifications() settingsSection {
	st := s.eng.Store()
	r := settingsSection{Screen: notifySettingsScreen, Title: "Уведомления",
		Why: "что и когда доходит до вас — и куда именно"}
	chans, _ := st.NotifyChannels()
	rules, _ := st.NotifyRules()
	включено := 0
	for _, c := range chans {
		if c.Enabled {
			включено++
		}
	}
	r.Meta = fmt.Sprintf("%s · %s", plural(включено, "канал", "канала", "каналов"),
		plural(len(rules), "правило", "правила", "правил"))
	if включено == 0 {
		r.Meta = "ни одного канала доставки · " + plural(len(rules), "правило", "правила", "правил")
		r.Level, r.Pill = LevelWarn, "никуда не доходит"
		r.Trouble = "Уведомления никуда не доходят: ни одного включённого канала доставки"
		r.Button = Action{Action: "$goto", Target: notifySettingsScreen, Label: "Включить канал"}
	}
	return r
}

func (s *Server) overviewModels() settingsSection {
	r := settingsSection{Screen: modelsScreen, Title: "Модели и роли",
		Why: "кто отвечает за письма, чаты, память, поиск и суд над командами"}
	модели := s.eng.ModelCards()
	роли := peerRoles(s.eng.Roles())
	безМодели := 0
	падает, первая := "", ""
	for _, role := range роли {
		if role.Model == "" || role.Model == "нет" {
			безМодели++
			if первая == "" {
				первая = role.Name
			}
		}
		if role.LastErr != "" && падает == "" {
			падает = role.Name
		}
	}
	r.Meta = fmt.Sprintf("%s · %s", plural(len(модели), "модель", "модели", "моделей"),
		plural(len(роли), "роль", "роли", "ролей"))
	switch {
	case len(модели) == 0:
		r.Meta = "ни одной модели — Сёма не сможет ответить"
		r.Level, r.Pill = LevelAlert, "нечем думать"
		r.Trouble = "Ни одной модели: отвечать, разбирать письма и искать нечем"
		r.Button = Action{Action: "$goto", Target: entityAddr(modelsScreen, modelsNew, ""), Label: "Подключить провайдера"}
	case безМодели > 0:
		r.Level, r.Count = LevelWarn, безМодели
		r.Pill = plural(безМодели, "роль без модели", "роли без модели", "ролей без модели")
		// замечание ведёт прямо туда, где его чинят, а не «в раздел»
		r.Screen = modelsScreen + "~" + modelsTabIssues
		r.Trouble = plural(безМодели, "роль осталась без модели", "роли остались без модели", "ролей остались без модели") +
			" — их работа просто не делается"
		// кнопка ведёт к строке первой роли без модели, а не к таблице целиком
		r.Button = Action{Action: "$goto", Target: roleHighlight(первая), Label: "Выбрать модель"}
	case падает != "":
		r.Level, r.Pill, r.Count = LevelWarn, "роль «"+падает+"» падает", 1
		r.Screen = modelsScreen + "~" + modelsTabIssues
		r.Trouble = "Роль «" + падает + "» не отвечает"
		r.Button = Action{Action: "$goto", Target: modelsScreen + "~" + modelsTabIssues, Label: "Открыть «Проблемы»"}
	}
	return r
}

func (s *Server) overviewSearch() settingsSection {
	r := settingsSection{Screen: searchScreen, Title: "Поиск",
		Why: "кто ходит в интернет за Сёму, что уходит в запрос и что из ответа сохраняется"}
	if n := s.eng.SavePagesCount(); n > 0 {
		r.Meta = "страниц из ответа в файлы: " + strconv.Itoa(n)
	} else {
		r.Meta = "страницы из ответа не сохраняются"
	}
	if !s.eng.SearchReady() {
		r.Meta = "роли «Поиск» не назначена модель"
		r.Level, r.Pill = LevelWarn, "Сёма про поиск молчит"
		r.Trouble = "Роли «Поиск» не назначена модель — в сеть Сёма не ходит и про поиск не заикается"
		r.Button = Action{Action: "$goto", Target: roleHighlight(engine.RoleSearch), Label: "Назначить модель"}
		r.Plain = true
	}
	return r
}

func (s *Server) overviewSkills() settingsSection {
	r := settingsSection{Screen: skillsScreen, Title: "Скиллы",
		Why: "инструкции Сёме на русском: когда и как поступать"}
	skills := s.eng.Skills()
	включено := 0
	блоки := map[string]bool{}
	for _, k := range skills {
		if k.Enabled {
			включено++
		}
		if k.Group != "" {
			блоки[k.Group] = true
		}
	}
	switch {
	case len(skills) == 0:
		r.Meta = "ни одной инструкции"
	default:
		r.Meta = fmt.Sprintf("%d из %s · %s", включено,
			plural(len(skills), "инструкции", "инструкций", "инструкций"),
			plural(len(блоки), "блок", "блока", "блоков"))
	}
	return r
}

func (s *Server) overviewLearning() settingsSection {
	r := settingsSection{Screen: learningScreen, Title: "Развитие",
		Why: "чему Сёма научился сам и что предлагает добавить"}
	stat := s.eng.LearnStats(time.Now().Add(-7 * 24 * time.Hour))
	r.Meta = fmt.Sprintf("%s за неделю · %s",
		plural(stat.Lessons, "урок", "урока", "уроков"),
		plural(stat.Open, "черновик", "черновика", "черновиков"))
	if stat.Open > 0 {
		r.Level, r.Pill, r.Count = LevelWarn, plural(stat.Open, "ждёт решения", "ждут решения", "ждут решения"), stat.Open
		r.Trouble = plural(stat.Open, "черновик скилла ждёт", "черновика скиллов ждут", "черновиков скиллов ждут") +
			" вашего слова: до согласия в промпт не попадает ничего"
		r.Button = Action{Action: "$goto", Target: learningScreen, Label: "Посмотреть"}
		r.Plain = true
	}
	return r
}

func (s *Server) overviewTools() settingsSection {
	r := settingsSection{Screen: "settings:tools", Title: "Инструменты",
		Why: "чем читаются PDF, картинки и документы на этой машине"}
	var has []string
	нет := 0
	for _, t := range s.eng.Tools() {
		if t.OK {
			has = append(has, strings.ToLower(t.Title))
			continue
		}
		нет++
	}
	if len(has) > 3 {
		has = has[:3]
	}
	r.Meta = strings.Join(has, " · ")
	if r.Meta == "" {
		r.Meta = "ничего не установлено"
	}
	if нет > 0 {
		r.Level, r.Count = LevelWarn, нет
		r.Pill = plural(нет, "не установлен", "не установлены", "не установлены")
		r.Trouble = plural(нет, "инструмент не установлен", "инструмента не установлены", "инструментов не установлены") +
			" — такие файлы Сёма прочитать не сможет"
		r.Button = Action{Action: "$goto", Target: "settings:tools", Label: "Посмотреть"}
		r.Plain = true
	}
	return r
}

func (s *Server) overviewHosts() settingsSection {
	st := s.eng.Store()
	r := settingsSection{Screen: serversScreen, Title: "Серверы",
		Why: "куда Сёма ходит по SSH и что ему там позволено"}
	hosts, _ := st.Hosts()
	сам := 0
	отпечаток := false
	// кому сменился отпечаток: если одному — замечание ведёт прямо на его
	// экран, а не в общий список, где его ещё надо найти
	кому, сколько := "", 0
	for _, h := range hosts {
		if h.Auto {
			сам++
		}
		if h.NewFP != "" {
			отпечаток = true
			кому = h.Name
			сколько++
		}
	}
	if len(hosts) == 0 {
		r.Meta = "ни одного сервера"
		return r
	}
	r.Meta = fmt.Sprintf("%s · %d ведёт сам", plural(len(hosts), "сервер", "сервера", "серверов"), сам)
	pending, _ := st.SSHPendingList()
	switch {
	case отпечаток:
		r.Level, r.Pill, r.Count = LevelAlert, "сменился отпечаток", 1
		r.Trouble = "У сервера сменился отпечаток — команды не выполняются, пока вы не примете новый"
		r.Button = Action{Action: "$goto", Target: serversScreen, Label: "Открыть «Серверы»"}
		if сколько == 1 {
			r.Button = Action{Action: "$goto", Target: withHighlight(entityAddr(serversScreen, кому, ""), hlRow("host-fp-changed")),
				Label: "Открыть «" + кому + "»"}
		}
	case len(pending) > 0:
		r.Level, r.Pill, r.Count = LevelAlert, plural(len(pending), "команда ждёт", "команды ждут", "команд ждут"), len(pending)
		r.Trouble = plural(len(pending), "команда ждёт", "команды ждут", "команд ждут") + " вашей кнопки"
		r.Button = Action{Action: "$goto", Target: serversScreen, Label: "Решить"}
	}
	return r
}

func (s *Server) overviewAgents() settingsSection {
	r := settingsSection{Screen: agentsScreen, Title: "Агенты",
		Why: "кто звонит Сёме своим проводом и что ему разрешено"}
	agents, _ := s.eng.Store().Agents()
	if len(agents) == 0 {
		r.Meta = "ни одного агента"
		return r
	}
	var names []string
	for _, a := range agents {
		names = append(names, "«"+a.Name+"»")
		if a.State == store.AgentFPBad {
			r.Level, r.Pill, r.Count = LevelAlert, "сменился отпечаток", r.Count+1
			r.Trouble = "У агента «" + a.Name + "» сменился отпечаток: по проводу его больше не пускают"
			r.Button = Action{Action: "$goto", Target: withHighlight(entityAddr(agentsScreen, a.Name, ""), hlRow("agent-fp-changed")),
				Label: "Открыть агента"}
		}
	}
	r.Meta = strings.Join(names, ", ")
	if last := lastSeen(agents); last != "" {
		r.Meta += " · на связи " + last
	}
	return r
}

// lastSeen — когда кто-нибудь из агентов звонил последним.
func lastSeen(agents []*store.Agent) string {
	last := ""
	for _, a := range agents {
		if a.Seen != "" && a.Seen > last {
			last = a.Seen
		}
	}
	if last == "" {
		return ""
	}
	return whenText(last)
}

func (s *Server) overviewIntegrations() settingsSection {
	st := s.eng.Store()
	r := settingsSection{Screen: mcpScreen, Title: "Интеграции",
		Why: "чужие серверы MCP"}
	servers, _ := st.MCPServers()
	if len(servers) == 0 {
		r.Meta = "ничего не подключено"
	} else {
		r.Meta = plural(len(servers), "сервер MCP", "сервера MCP", "серверов MCP")
	}
	if pending, _ := st.MCPPendingList(); len(pending) > 0 {
		r.Level, r.Pill, r.Count = LevelAlert, plural(len(pending), "вызов ждёт", "вызова ждут", "вызовов ждут"), len(pending)
		r.Trouble = plural(len(pending), "вызов MCP ждёт", "вызова MCP ждут", "вызовов MCP ждут") + " вашей кнопки"
		r.Button = Action{Action: "$goto", Target: mcpScreen, Label: "Решить"}
	}
	return r
}

func (s *Server) overviewStorages() settingsSection {
	st := s.eng.Store()
	r := settingsSection{Screen: "settings:storages", Title: "Хранилища",
		Why: "где лежат файлы и откуда Сёма их берёт"}
	total, bytes, _ := st.FileStats()
	if total == 0 {
		r.Meta = "файлов пока нет"
		return r
	}
	r.Meta = fmt.Sprintf("%s · %s", plural(total, "файл", "файла", "файлов"), humanBytes(bytes))
	storages, _ := st.Storages()
	for _, c := range storages {
		if c.Primary { // основное хранилище само себе зеркалом не бывает
			continue
		}
		done, all := st.MirrorProgress(c.Name)
		if all-done > 0 {
			r.Level, r.Count = LevelWarn, r.Count+all-done
			r.Pill = fmt.Sprintf("%d не в зеркале", all-done)
			r.Trouble = fmt.Sprintf("Зеркало «%s» отстаёт: %d файлов туда не уехали", c.Name, all-done)
			r.Button = Action{Action: "$goto", Target: "settings:storages", Label: "Посмотреть"}
			r.Plain = true
		}
	}
	return r
}

func (s *Server) overviewAccess() settingsSection {
	st := s.eng.Store()
	r := settingsSection{Screen: accessScreen, Title: sectionTitle[accessScreen],
		Why: "кто входит в самого Сёму и по чему"}
	keys, _ := st.Passkeys()
	tokens, _ := st.Tokens()
	живых := 0
	for _, t := range tokens {
		if t.Revoked.IsZero() {
			живых++
		}
	}
	sessions, _ := st.Sessions()
	r.Meta = fmt.Sprintf("%s · %s", plural(len(keys), "ключ", "ключа", "ключей"),
		plural(живых, "токен", "токена", "токенов"))
	switch {
	case len(keys) == 0:
		r.Meta = "ключей нет: снаружи войти нечем"
		r.Level, r.Pill = LevelWarn, "только с этой машины"
		r.Trouble = "Ключей устройств нет: снаружи войти нечем, а первый ключ заводится только с самой машины"
		r.Button = Action{Action: "$goto", Target: accessScreen, Label: "Завести ключ"}
	case len(sessions) > 4:
		r.Level, r.Pill = LevelWarn, fmt.Sprintf("%d входов открыто", len(sessions))
		r.Trouble = fmt.Sprintf("Открыто %d входов — забытые вкладки живут до своего срока", len(sessions))
		// сессии переехали с первой вкладки на «Кто внутри» (2 октября 2026)
		r.Button = Action{Action: "$goto", Target: accessScreen + "~" + accessTabInside, Label: "Посмотреть входы"}
		r.Plain = true
	}
	return r
}

func (s *Server) overviewSecrets() settingsSection {
	r := settingsSection{Screen: secretsScreen, Title: "Ключи и секреты",
		Why: "значения закрыты: видно имя, кому выдан и кто брал"}
	total, sealed := s.eng.Store().SecretCounts()
	r.Meta = plural(total, "секрет", "секрета", "секретов")
	if total == 0 {
		r.Meta = "хранилище пустое"
	}
	if total > 0 && sealed < total {
		r.Level, r.Count = LevelAlert, total-sealed
		r.Pill = fmt.Sprintf("%d не читаются", total-sealed)
		r.Trouble = fmt.Sprintf("%d секретов не читаются нынешним ключом — проверьте, с тем ли ключом запущен Сёма", total-sealed)
		r.Button = Action{Action: "$goto", Target: entityAddr(secretsScreen, "", secretsTabCrypt), Label: "Проверить ключ"}
	}
	return r
}

// overviewWatch — «Работа Сёмы»: наблюдатель за собой, провод, чаты команд,
// часовой пояс. Норма молчит: уровня у раздела нет, а открытые находки видны
// счётчиком у вкладки «Самонаблюдение» и на «Здоровье» (там им и место).
func (s *Server) overviewWatch() settingsSection {
	r := settingsSection{Screen: watchScreen, Title: sectionTitle[watchScreen],
		Why: "наблюдение за собой, провод и опрос экрана, чаты команд, часовой пояс"}
	open, _ := s.eng.Store().OpenFindings()
	// вторая половина строки говорит, что ещё лежит в разделе: на телефоне это
	// единственная подсказка, где искать провод, опрос и пояс
	const rest = " · провод, опрос, часовой пояс"
	switch {
	case !s.eng.WatchOn():
		r.Meta = "наблюдение выключено" + rest
	case open == 0:
		r.Meta = "наблюдение включено" + rest
	default:
		r.Meta = plural(open, "открытая находка", "открытые находки", "открытых находок") + rest
	}
	return r
}

func (s *Server) overviewSent() settingsSection {
	r := settingsSection{Screen: sentScreen, Title: sectionTitle[sentScreen],
		Why: "каждая отправка модели: кто просил и что было в контексте"}
	day := s.eng.SentToday()
	if day.Calls == 0 {
		r.Meta = "за сутки Сёма моделей не спрашивал"
		return r
	}
	r.Meta = fmt.Sprintf("за сутки наружу %d из %d", day.Outside, day.Calls)
	// это число обязано быть нулём: секретное недоверенной модели —
	// не подробность журнала, а утечка
	if day.SecretUntrusted > 0 {
		r.Level, r.Count = LevelAlert, day.SecretUntrusted
		r.Pill = fmt.Sprintf("секретного недоверенной %d", day.SecretUntrusted)
		r.Trouble = fmt.Sprintf("Секретное ушло недоверенной модели %d раз за сутки — так быть не должно", day.SecretUntrusted)
		r.Button = Action{Action: "$goto", Target: sentScreen + "~" + sentTabLog, Label: "Посмотреть отправки"}
	}
	return r
}

// settingsWords — по каким словам «Настройки» находятся поиском в меню.
//
// Раньше разделы стояли подпунктами меню, и человек искал «пуши» или
// «ключи» прямо там. Дерево убрано: в меню остался один пункт, а
// разбираться по областям — дело пяти вкладок внутри. Но слова терять
// нельзя, иначе поиск по меню внезапно перестал бы находить половину
// настроек. Собираем их из того же справочника, по которому ищет обзор:
// два списка синонимов разошлись бы через неделю.
func settingsWords() string {
	слова := make([]string, 0, len(synonyms)+1)
	слова = append(слова, "настройки")
	for _, с := range synonyms {
		слова = append(слова, с)
	}
	sort.Strings(слова)
	return strings.Join(слова, " ")
}

// overviewSettingsLog — история правок. Норма здесь молчит: журнал,
// который заполняется, — это и есть порядок, а не повод для пометки.
func (s *Server) overviewSettingsLog() settingsSection {
	r := settingsSection{Screen: changesScreen, Title: "Журнал настроек",
		Why: "кто, когда и какой параметр менял; значения секретов не пишутся"}
	total, людьми := s.eng.Store().CountSettingsLog()
	if total == 0 {
		r.Meta = "пока пусто — настройки ещё не менялись"
		return r
	}
	r.Meta = fmt.Sprintf("%s · ваших %d", plural(total, "правка", "правки", "правок"), людьми)
	return r
}
