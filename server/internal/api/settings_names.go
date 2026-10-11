package api

// Человеческие имена параметров из индекса.
//
// Журнал хранит ключ параметра — `notify.mac_last_err`, `ui.poll_s`,
// `hosts.…` — и это единственное, что про него знает сама дверь записи. Человек
// ищет в журнале «опрос экрана без провода» и «окно сервера», а не ключ. Индекс
// настроек знает, как параметр называется на экране и где он лежит, и отдаёт
// журналу и Обзору имя, путь словами и адрес со строкой для подсветки.
//
// Параметр узнаётся по порядку, от точного к дальнему:
//  1. по ключу в реестре — поле собрано из него (`settingField`);
//  2. по виду ключа — правила серверов и серверов MCP пишутся в журнал своими
//     ключами (`ssh.rules.<сервер>.<ответ>`, `mcp.rules.<сервер>.<инструмент>`);
//  3. по тому, откуда правка: журнал помнит экран и действие, которыми она
//     сделана («settings:servers@srv-vpn~auto · save_host_window»), и по ним находится
//     поле формы на этом экране;
//  4. иначе — сам ключ, и путь по экрану, если он известен.
//
// Ничего из этого не выдумывается: что индекс не знает, показывается как есть,
// а не как «похожее».

import (
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// settingName — как параметр называется человеку и куда к нему вести.
type settingName struct {
	Title string // «Окно», «Опрос экрана без провода»; нет имени — ключ
	Path  string // «Система → Серверы → srv-vpn → Самостоятельность»
	// Addr — куда вести, уже с меткой строки; пусто — вести некуда
	Addr string
	// Entry — запись индекса, если параметр узнан по ней
	Entry *indexEntry
}

// pathWords — путь словами.
func pathWords(path []string) string { return strings.Join(path, " → ") }

// nameOf — имя параметра, к которому относится запись журнала. Индекса может не
// быть (nil): тогда имя — сам ключ.
func (idx *settingsIndex) nameOf(chg store.SettingChange) settingName {
	// составная настройка (`models.json`…): дверь записи уже сказала словами, что
	// изменилось, — это и есть имя; по ключу ничего, кроме «models.json», не узнать
	if chg.Note != nil {
		return noteName(*chg.Note)
	}
	name := settingName{Title: chg.Key}
	if idx == nil {
		return name
	}
	if e := idx.pick(idx.byKey[chg.Key]); e != nil {
		return named(e)
	}
	if e := outsideIndexKeys[chg.Key]; e != nil {
		return named(e)
	}
	if n, ok := ruleName(chg.Key); ok {
		return n
	}
	screen, action := splitSource(chg.From)
	if action != "" {
		if e := idx.byActionOnScreen(action, screen); e != nil {
			return named(e)
		}
		// форма сохраняет несколько полей, и какое из них правка, по действию не
		// узнать: ключ остаётся именем, а путь и адрес — формы
		if es := idx.onScreen(idx.byAction[action], screen); len(es) > 0 {
			name.Path = pathWords(es[0].Path)
			name.Addr = es[0].Addr
			return name
		}
	}
	return name
}

// outsideIndexKeys — параметры экранов, которые не разделы настроек, а свои:
// «Модули → Пределы» живёт на экране модулей, и индекс настроек его не обходит
// (поиск по настройкам про него не знает), а журналу имя нужно. Запись — как у
// индекса, но только для имени, перехода и «было → стало».
var outsideIndexKeys = map[string]*indexEntry{
	engine.ModuleTaskTimeoutKey: {Kind: entrySetting, Key: engine.ModuleTaskTimeoutKey, Title: "Предел времени задачи модуля",
		Path: []string{"Модули", "Пределы"}, Addr: "modules", Control: "number", Unit: "с"},
	"scripts.timeout_s": {Kind: entrySetting, Key: "scripts.timeout_s", Title: "Предел времени скрипта из чата",
		Path: []string{"Модули", "Пределы"}, Addr: "modules", Control: "number", Unit: "с"},

	// ---- экраны, которых индекс не обходит: очередь, память, здоровье, почта ----
	"queue.paused": {Kind: entrySetting, Key: "queue.paused", Title: "Очередь работает",
		Path: []string{"Очередь", "Сейчас"}, Addr: queueScreen, Control: "row-toggle", Invert: true},
	"queue.cloud_after": {Kind: entrySetting, Key: "queue.cloud_after", Title: "Звать облачную, когда ждут больше",
		Path: []string{"Очередь", "Пределы"}, Addr: queueScreen, Control: "stepper", Unit: "работ"},
	"queue.cloud_help": {Kind: entrySetting, Key: "queue.cloud_help", Title: "Помощь облачной модели (0 в «Звать облачную» — выключено)",
		Path: []string{"Очередь", "Пределы"}, Addr: queueScreen, Control: "toggle"},
	"queue.max_load": {Kind: entrySetting, Key: "queue.max_load", Title: "Пропускать проход при нагрузке выше",
		Path: []string{"Очередь", "Пределы"}, Addr: queueScreen, Control: "text", Unit: "на ядро"},
	"memory.review": {Kind: entrySetting, Key: "memory.review", Title: "Сёма подтверждает записи сам",
		Path: []string{"Память", "Записи", "Разбор памяти"}, Addr: "memory", Control: "row-toggle"},
	"update.url": {Kind: entrySetting, Key: "update.url", Title: "Адрес проверки новой версии",
		Path: []string{"Здоровье", "Проверка новой версии"}, Addr: "health", Control: "text"},
	"digest.window": {Kind: entrySetting, Key: "digest.window", Title: "Окно сводки «Почты»",
		Path: []string{"Почта", "Сводка"}, Addr: "inbox", Control: "select"},

	// ---- экраны настроек, чьи поля собраны руками, без key: у компонента.
	// Честный путь — константа в engine/setting_keys.go и key: у поля, тогда
	// индекс и поиск подхватят сами; пока этого нет, журнал зовёт их отсюда
	// (7 октября 2026, по разбору 55 безымянных ключей) ----
	"app.timezone": {Kind: entrySetting, Key: "app.timezone", Title: "Часовой пояс",
		Path: []string{"Система", "Работа Сёмы", "Экран и связь", "Время"}, Addr: entityAddr(watchScreen, "", workTabScreen), Control: "text"},
	"audit.keep_days": {Kind: entrySetting, Key: "audit.keep_days", Title: "Срок хранения журнала отправок",
		Path: []string{"Безопасность", "Что ушло наружу", "Хранение"}, Addr: sentScreen, Control: "number", Unit: "дней"},
	"audit.keep_full": {Kind: entrySetting, Key: "audit.keep_full", Title: "Хранить полный текст отправленного",
		Path: []string{"Безопасность", "Что ушло наружу", "Хранение"}, Addr: sentScreen, Control: "row-toggle"},
	"audit.sensitive": {Kind: entrySetting, Key: "audit.sensitive", Title: "Спрашивать про чувствительное",
		Path: []string{"Безопасность", "Что ушло наружу", "Что не уходит наружу"}, Addr: sentScreen, Control: "row-toggle"},
	auth.SettingTrustProxy: {Kind: entrySetting, Key: auth.SettingTrustProxy, Title: "За своим прокси",
		Path: []string{"Безопасность", "Вход и доступ", "Доверенная сеть", "Как Сёма узнаёт своих"}, Addr: entityAddr(accessScreen, "", accessTabNetwork), Control: "toggle", Admin: true, Careful: true},
	"backup.on": {Kind: entrySetting, Key: "backup.on", Title: "Копия наружу по ночам",
		Path: []string{"Система", "Хранилища", "Откуда берутся", "Источники файлов"}, Addr: entityAddr(storagesScreen, "", storTabSources), Control: "row-toggle"},
	"files.offload": {Kind: entrySetting, Key: "files.offload", Title: "Разгружать старые файлы",
		Path: []string{"Система", "Хранилища", "Откуда берутся", "Источники файлов"}, Addr: entityAddr(storagesScreen, "", storTabSources), Control: "row-toggle"},
	"files.parse": {Kind: entrySetting, Key: "files.parse", Title: "Разбирать, что внутри",
		Path: []string{"Система", "Хранилища", "Откуда берутся", "Источники файлов"}, Addr: entityAddr(storagesScreen, "", storTabSources), Control: "row-toggle"},
	filesUploadKey: {Kind: entrySetting, Key: filesUploadKey, Title: "Загрузка через сервер",
		Path: []string{"Система", "Хранилища", "Откуда берутся", "Источники файлов"}, Addr: entityAddr(storagesScreen, "", storTabSources), Control: "row-toggle"},
	"mirror.on": {Kind: entrySetting, Key: "mirror.on", Title: "Зеркало файлов",
		Path: []string{"Система", "Хранилища", "Откуда берутся", "Источники файлов"}, Addr: entityAddr(storagesScreen, "", storTabSources), Control: "row-toggle"},
	"chat_scripts": {Kind: entrySetting, Key: "chat_scripts", Title: "Сёма пишет и запускает скрипты в чате",
		Path: []string{"Система", "Инструменты", "Что умеет сама эта машина"}, Addr: toolsScreen, Control: "row-toggle"},
	"codes.kinds": {Kind: entrySetting, Key: "codes.kinds", Title: "Коды на картинках — какие искать",
		Path: []string{"Система", "Инструменты", "Коды на картинках"}, Addr: entityAddr(toolsScreen, "", toolsTabCodes), Control: "checks"},
	"embed.trusted": {Kind: entrySetting, Key: "embed.trusted", Title: "Доверие эмбеддеру",
		Path: []string{"Поведение", "Модели и роли", "Роли", "Служебные роли"}, Addr: modelsScreen, Control: "toggle", Careful: true},
	"embed_model": {Kind: entrySetting, Key: "embed_model", Title: "Эмбеддинги — модель",
		Path: []string{"Поведение", "Модели и роли", "Роли", "Служебные роли"}, Addr: modelsScreen, Control: "select"},
	"llm.active": {Kind: entrySetting, Key: "llm.active", Title: "Модель по умолчанию",
		Path: []string{"Поведение", "Модели и роли", "Роли", "Служебные роли"}, Addr: modelsScreen, Control: "select"},
	"vision.no_text": {Kind: entrySetting, Key: "vision.no_text", Title: "Картинка без распознанного текста",
		Path: []string{"Поведение", "Модели и роли", "Роли", "Служебные роли"}, Addr: modelsScreen, Control: "select"},
	"learn.on": {Kind: entrySetting, Key: "learn.on", Title: "Учиться из законченных работ",
		Path: []string{"Поведение", "Развитие", "Пределы"}, Addr: learningScreen, Control: "row-toggle"},
	"learn.judge": {Kind: entrySetting, Key: "learn.judge", Title: "Спрашивать судью на другой модели",
		Path: []string{"Поведение", "Развитие", "Пределы"}, Addr: learningScreen, Control: "row-toggle"},
	"junk.retention_days": {Kind: entrySetting, Key: "junk.retention_days", Title: "Спам стирать с почтового сервера через",
		Path: []string{"Каналы", "Почта", "Общее", "Хранение на почтовом сервере"}, Addr: entityAddr(mailScreen, "", mailTabCommon), Control: "number", Unit: "дней"},
	"mail.keep_days": {Kind: entrySetting, Key: "mail.keep_days", Title: "Обычные письма стирать через",
		Path: []string{"Каналы", "Почта", "Общее", "Опасное"}, Addr: entityAddr(mailScreen, "", mailTabCommon), Control: "number", Unit: "дней", Careful: true},
	"mail.purge_other": {Kind: entrySetting, Key: "mail.purge_other", Title: "Стирать с почтового сервера и обычные письма",
		Path: []string{"Каналы", "Почта", "Общее", "Опасное"}, Addr: entityAddr(mailScreen, "", mailTabCommon), Control: "toggle", Careful: true},
	"mail.read_server_folders": {Kind: entrySetting, Key: "mail.read_server_folders", Title: "Заходить в спам и корзину почтового сервера",
		Path: []string{"Каналы", "Почта", "Общее", "Серверные папки"}, Addr: entityAddr(mailScreen, "", mailTabCommon), Control: "toggle"},
	"mail.send_mode": {Kind: entrySetting, Key: "mail.send_mode", Title: "Что делать с готовым ответом",
		Path: []string{"Каналы", "Почта", "Общее", "Ответы"}, Addr: entityAddr(mailScreen, "", mailTabCommon), Control: "segments"},
	"mail.send_auto_to": {Kind: entrySetting, Key: "mail.send_auto_to", Title: "Кому Сёма вправе отвечать сам",
		Path: []string{"Каналы", "Почта", "Общее", "Ответы"}, Addr: entityAddr(mailScreen, "", mailTabCommon), Control: "text"},
	"mail.whitelist_only": {Kind: entrySetting, Key: "mail.whitelist_only", Title: "Во «Входящих» — только те, кого вы разрешили",
		Path: []string{"Каналы", "Почта", "Спам", "Кого пускать во «Входящие»"}, Addr: entityAddr(mailScreen, "", mailTabSpam), Control: "toggle"},
	"rag.auto_threshold": {Kind: entrySetting, Key: "rag.auto_threshold", Title: "Порог похожести на размеченные вами письма",
		Path: []string{"Каналы", "Почта", "Спам", "Кого пускать во «Входящие»"}, Addr: entityAddr(mailScreen, "", mailTabSpam), Control: "number", Unit: "%"},
	"mail.money_guard": {Kind: entrySetting, Key: "mail.money_guard", Title: "Не уносить в спам письма со счетами",
		Path: []string{"Каналы", "Почта", "Спам", "Что спамом не считать"}, Addr: entityAddr(mailScreen, "", mailTabSpam), Control: "toggle"},
	"mail.translate": {Kind: entrySetting, Key: "mail.translate", Title: "Переводить письма сам",
		Path: []string{"Каналы", "Перевод", "Как переводить"}, Addr: translateScreen, Control: "toggle"},
	"mail.translate_to": {Kind: entrySetting, Key: "mail.translate_to", Title: "На какой язык",
		Path: []string{"Каналы", "Перевод", "Как переводить"}, Addr: translateScreen, Control: "picker"},
	"mail.translate_chars": {Kind: entrySetting, Key: "mail.translate_chars", Title: "Сколько знаков переводить",
		Path: []string{"Каналы", "Перевод", "Как переводить"}, Addr: translateScreen, Control: "number", Unit: "знаков"},
	"max.allow_expired_cert": {Kind: entrySetting, Key: "max.allow_expired_cert", Title: "Принимать просроченный сертификат",
		Path: []string{"Каналы", "MAX", "Вход"}, Addr: maxScreen, Control: "row-toggle", Careful: true},
	"notify.counter_level": {Kind: entrySetting, Key: "notify.counter_level", Title: "Счётчик в меню",
		Path: []string{"Каналы", "Уведомления", "Что показывать"}, Addr: entityAddr(notifySettingsScreen, "", notifyTabShow), Control: "picker"},
	"notify.keep_days": {Kind: entrySetting, Key: "notify.keep_days", Title: "Хранить «Сделано», дней",
		Path: []string{"Каналы", "Уведомления", "Что показывать"}, Addr: entityAddr(notifySettingsScreen, "", notifyTabShow), Control: "stepper", Unit: "дней"},
	"notify.snooze": {Kind: entrySetting, Key: "notify.snooze", Title: "На сколько откладывать",
		Path: []string{"Каналы", "Уведомления", "Что показывать"}, Addr: entityAddr(notifySettingsScreen, "", notifyTabShow), Control: "text"},
	"tg.stream": {Kind: entrySetting, Key: "tg.stream", Title: "Как приходит ответ",
		Path: []string{"Каналы", "Telegram", "Ответы и пределы", "Разговор с вами вживую"}, Addr: entityAddr(tgScreen, "", tgTabAnswer), Control: "select"},
	"watch.on": {Kind: entrySetting, Key: "watch.on", Title: "Смотреть за формой своей работы",
		Path: []string{"Система", "Работа Сёмы", "Самонаблюдение", "Наблюдатель"}, Addr: watchScreen, Control: "row-toggle"},
	"watch.stop": {Kind: entrySetting, Key: "watch.stop", Title: "Останавливать работу, которая идёт впустую",
		Path: []string{"Система", "Работа Сёмы", "Самонаблюдение", "Наблюдатель"}, Addr: watchScreen, Control: "row-toggle", Careful: true},
	"watch.disagree": {Kind: entrySetting, Key: "watch.disagree", Title: "Сверять свой итог с хранилищем",
		Path: []string{"Система", "Работа Сёмы", "Самонаблюдение", "Наблюдатель"}, Addr: watchScreen, Control: "row-toggle"},
	"watch.repeat": {Kind: entrySetting, Key: "watch.repeat", Title: "Повторов по объекту — петля",
		Path: []string{"Система", "Работа Сёмы", "Самонаблюдение", "Пороги"}, Addr: watchScreen, Control: "stepper", Unit: "раз"},
	"watch.window_h": {Kind: entrySetting, Key: "watch.window_h", Title: "За какое окно считать",
		Path: []string{"Система", "Работа Сёмы", "Самонаблюдение", "Пороги"}, Addr: watchScreen, Control: "stepper", Unit: "ч"},
	"watch.keep_days": {Kind: entrySetting, Key: "watch.keep_days", Title: "Держать закрытые находки",
		Path: []string{"Система", "Работа Сёмы", "Самонаблюдение", "Пороги"}, Addr: watchScreen, Control: "stepper", Unit: "дн."},
}

// noteTarget — куда ведёт составная настройка: раздел, слово вкладки и адрес списка.
type noteTarget struct {
	Section string // экран раздела настроек; пусто — раздел вне настроек
	Tab     string // слово вкладки в пути
	Path    string // путь целиком, когда раздела настроек нет
	List    string // куда вести, когда сущность одна и её уже нет, или сущностей много
}

// noteTargets — по виду составной настройки (`store.Note*`). Короткая таблица: адреса
// сущностей строят функции экранов (`modelAddr`, `providerAddr`, `entityAddr`).
var noteTargets = map[string]noteTarget{
	store.NoteModels:    {Section: modelsScreen, Tab: "Модели", List: modelsScreen + "~" + modelsTabList},
	store.NoteRoles:     {Section: modelsScreen, Tab: "Роли", List: modelsScreen},
	store.NoteProviders: {Section: modelsScreen, Tab: "Провайдеры", List: modelsScreen + "~" + modelsTabProviders},
	store.NoteModules:   {Path: "Модули", List: "modules"},
	store.NoteMail:      {Section: mailScreen, List: mailScreen},
	store.NoteAccounts:  {Section: mailScreen, Tab: "Ящики", List: mailScreen},
}

// noteName — имя правки составной настройки: слова из записи журнала, путь по её
// виду и адрес сущности, если затронута одна и она есть, иначе — раздела.
func noteName(note store.ChangeNote) settingName {
	name := settingName{Title: note.Title}
	t, ok := noteTargets[note.Kind]
	if !ok {
		return name
	}
	name.Path = t.Path
	if t.Section != "" {
		if group, section, found := settingsSectionOf(t.Section); found {
			name.Path = pathWords(nonEmpty(group.Name, sectionTitle[section], t.Tab))
		}
	}
	name.Addr = t.List
	if note.Ref != "" && !note.Gone {
		switch note.Kind {
		case store.NoteModels:
			name.Addr = modelAddr(note.Ref)
		case store.NoteProviders:
			name.Addr = providerAddr(note.Ref, providerTabKey)
		case store.NoteMail:
			name.Addr = entityAddr(mailScreen, note.Ref, "")
		}
	}
	return name
}

// nonEmpty — слова пути без пустых.
func nonEmpty(words ...string) []string {
	var out []string
	for _, w := range words {
		if strings.TrimSpace(w) != "" {
			out = append(out, w)
		}
	}
	return out
}

// named — имя по записи индекса.
func named(e *indexEntry) settingName {
	return settingName{Title: e.Title, Path: pathWords(e.Path), Addr: withHighlight(e.Addr, e.Anchor), Entry: e}
}

// pick — запись, которой называть параметр, если их несколько: та, что не в
// «Тонкой настройке» и лежит ближе к корню.
func (idx *settingsIndex) pick(list []*indexEntry) *indexEntry {
	var best *indexEntry
	for _, e := range list {
		if best == nil || (best.Fine && !e.Fine) || (best.Fine == e.Fine && len(e.Path) < len(best.Path)) {
			best = e
		}
	}
	return best
}

// byActionOnScreen — единственное поле, которое сохраняет это действие на этом
// экране; несколько или ни одного — nil.
func (idx *settingsIndex) byActionOnScreen(action, screen string) *indexEntry {
	if idx == nil {
		return nil // индекс собирается (perf-pass 4.3)
	}
	es := idx.onScreen(idx.byAction[action], screen)
	if len(es) == 1 {
		return es[0]
	}
	return nil
}

// onScreen — из записей те, что лежат на экране (по адресу целиком, а нет таких —
// по адресу раздела).
func (idx *settingsIndex) onScreen(list []*indexEntry, screen string) []*indexEntry {
	if screen == "" {
		return nil
	}
	var exact, same []*indexEntry
	for _, e := range list {
		switch {
		case e.Addr == screen:
			exact = append(exact, e)
		case baseScreen(e.Addr) == baseScreen(screen):
			same = append(same, e)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return same
}

// splitSource — экран и действие из записи «экран · действие» (`editOrigin`).
func splitSource(from string) (screen, action string) {
	if i := strings.LastIndex(from, " · "); i >= 0 {
		return from[:i], from[i+len(" · "):]
	}
	return "", from
}

// ruleName — имя параметра, записанного правилами сервера или сервера MCP.
func ruleName(key string) (settingName, bool) {
	switch {
	case strings.HasPrefix(key, "ssh.rules."):
		rest := strings.TrimPrefix(key, "ssh.rules.")
		i := strings.LastIndex(rest, ".")
		if i <= 0 {
			return settingName{}, false
		}
		host, kind := rest[:i], rest[i+1:]
		return settingName{Title: "Правила сервера — " + ruleKindWord(kind),
			Path: "Система → Серверы → " + host + " → Правила",
			Addr: engine.ServerScreen(host, serversTabRules)}, true
	case strings.HasPrefix(key, "mcp.rules."):
		rest := strings.TrimPrefix(key, "mcp.rules.")
		i := strings.Index(rest, ".")
		if i <= 0 {
			return settingName{}, false
		}
		server, tool := rest[:i], rest[i+1:]
		return settingName{Title: "Правило инструмента «" + tool + "»",
			Path: "Система → Интеграции → " + server + " → Правила",
			Addr: engine.MCPServerScreen(server, "rules")}, true
	}
	return settingName{}, false
}

// ruleKindWord — как называется круг правил в записи журнала.
func ruleKindWord(kind string) string {
	switch kind {
	case store.RuleAllow:
		return "разрешить"
	case store.RuleAsk:
		return "спросить"
	case store.RuleDeny:
		return "запретить"
	}
	return kind
}

// pinnable — настройку можно вынести на Обзор живым контролом: у неё есть ключ
// в реестре, она не осторожная (вход и доступ, доверие, самостоятельность
// серверов, «Опасное», секреты), её форма правит существующее и контрол — из тех,
// что рисуются в строке настройки.
func (e *indexEntry) pinnable() bool {
	if e.Kind != entrySetting || e.Key == "" || e.Careful || e.Admin || e.Action == "" || !editsExisting(e.Action) {
		return false
	}
	// ключ назван тумблером или полем, а в реестре его нет (`night.dry_run`,
	// `web.find_url`): живой контрол собирает `settingField` из описания реестра, и
	// без описания ему нечего показывать
	if _, ok := store.LookupSetting(e.Key); !ok {
		return false
	}
	// только поля, собранные из реестра (`settingField`): их значение лежит так, как
	// показано, и сохраняет его `saveSettings`. Список и сегменты показывают подпись
	// варианта, а действие сохранения переводит её в ключ («Только мои» → own), —
	// такому полю чужая форма с сырым значением подставила бы не то
	return liveControl(e.Control)
}
