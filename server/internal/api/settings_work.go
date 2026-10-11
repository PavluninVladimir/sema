package api

// «Работа Сёмы» — как Сёма работает сам по себе: смотрит за собой, обновляет
// экран, ведёт чаты команд.
//
// Раздел называется так в колонке и над содержимым (раньше «Самонаблюдение»
// и «Что Сёма нашёл в себе» — два имени у одного места). Адрес остался
// прежним, `settings:watch`: на него ведут закладки, замечания «Здоровья» и
// записи в чужих журналах.
//
// Сюда с «Уведомлений» переехало то, что лежало там между правилами писем и
// к уведомлениям отношения не имело (2 октября 2026, шаг 8 плана
// `design/nastroyki-2/README.md`): провод и опрос экрана (`ui.*`), срок чатов
// команд и пинатель (`chats.*`), хвост родителя в ветке, часовой пояс.
// Ключи в базе не менялись — переехало только место на экране.
//
// Три вкладки. Первая — то, что здесь было: наблюдатель и его находки. Вторая
// — «Экран и связь»: часовой пояс, опрос и провод; редкое (сколько ждать
// клиента, срок снимка) — в «Тонкой настройке». Третья — «Чаты и ветки»: срок
// чатов команд, пинатель, хвост родителя в ветке.

import (
	"fmt"
	"strconv"
	"strings"
)

// Вкладки раздела. Первая без хвоста: адрес `settings:watch` ведёт туда же,
// куда вёл всегда.
const (
	workTabWatch  = ""       // «Самонаблюдение» — наблюдатель и находки
	workTabScreen = "screen" // «Экран и связь» — часовой пояс, опрос, провод
	workTabChats  = "chats"  // «Чаты и ветки» — чаты команд, пинатель, хвост ветки
)

// workLiveParams — поля вкладки «Экран и связь»: имя поля формы, ключ в реестре
// и слова для сообщения об ошибке. Имена полей — те, что были на «Уведомлениях»:
// это договор с прежним действием `set_notify_settings`.
var workLiveParams = []settingParam{
	{"poll_s", "ui.poll_s", "опрос экрана без провода"},
	{"poll_live_s", "ui.poll_live_s", "опрос при живом проводе"},
	{"live_ms", "ui.live_ms", "окно склейки сигналов"},
	{"ws_per_session", "ui.ws_per_session", "проводов на устройство"},
	{"ws_write_s", "ui.ws_write_s", "сколько ждать клиента"},
	{"snapshot_days", "ui.snapshot_days", "срок снимка экрана"},
	{"pprof", "debug.pprof", "профилировщик"},
	{"slow_sql_ms", "debug.slow_sql_ms", "порог медленного запроса"},
	{"list_rows", "ui.list_rows", "строк до «Показать ещё»"},
	{"prefetch_ms", "ui.prefetch_ms", "срок подгруженного заранее экрана"},
	{"redirect_tls", "http.redirect_tls", "переадресация на защищённый порт"},
}

// workChatParams — поля вкладки «Чаты и ветки».
var workChatParams = []settingParam{
	{"cmd_ttl", "chats.command_ttl_h", "срок чатов команд"},
	{"nudge_after", "chats.nudge_after_min", "через сколько пинать чат"},
	{"nudge_max", "chats.nudge_max", "пинков на чат"},
	{"thread_tail", "chats.thread_tail", "хвост родителя в ветке"},
	{"read_steps", "chats.read_steps", "ходов модели за одно обращение"},
	{"promise_markers", "chats.promise_markers", "слова обещания"},
	{"claim_markers", "chats.claim_markers", "слова «уже сделано»"},
	// поручения (engine/assign.go)
	{"assign_confirm", "assign.confirm", "начинать поручение по кнопке"},
	{"assign_max_steps", "assign.max_steps", "шагов в плане поручения"},
	{"assign_step_turns", "assign.step_turns", "ходов на шаг поручения"},
	{"assign_days", "assign.days", "срок поручения"},
	{"assign_tracker", "assign.tracker", "трекер задач поручений"},
	{"assign_tracker_project", "assign.tracker_project", "проект в трекере"},
	{"assign_tracker_create", "assign.tracker_create_tool", "инструмент создания задачи"},
	{"assign_tracker_done", "assign.tracker_done_tool", "инструмент «задача сделана»"},
	{"assign_tracker_cancel", "assign.tracker_cancel_tool", "инструмент «задача отменена»"},
}

func (s *Server) buildWork(doc *Document, screen string) error {
	_, tab := splitSubtab(screen)
	doc.Heading = sectionTitle[watchScreen]
	doc.Layout = "wide"

	open, _ := s.eng.Store().OpenFindings()
	tabs := []innerTab{
		{ID: workTabWatch, Name: "Самонаблюдение"},
		{ID: workTabScreen, Name: "Экран и связь"},
		{ID: workTabChats, Name: "Чаты и ветки"},
	}
	// открытая находка видна до того, как человек откроет вкладку
	if open > 0 {
		tabs[0].Count, tabs[0].Level = strconv.Itoa(open), LevelWarn
	}
	setSubtabs(doc, watchScreen, tab, tabs)

	switch tab {
	case workTabScreen:
		return s.buildWorkScreen(doc)
	case workTabChats:
		return s.buildWorkChats(doc)
	}
	return s.buildSelfWatch(doc, open)
}

// buildWorkScreen — «Экран и связь». Три строки, за которыми приходят (пояс и
// два опроса), а окно склейки и число проводов правят реже — они рядом; сколько
// ждать клиента и сколько живёт снимок — «Тонкая настройка».
func (s *Server) buildWorkScreen(doc *Document) error {
	doc.Subheading = "Опрос экрана, провод и часовой пояс"
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	// пояс — не из реестра: его «умолчание» — пояс самой машины, и сохраняет его
	// `SetTimezone`, он же проверяет, что такой пояс есть
	add(Component{Type: "form", ID: "work-time", Title: "Время", Style: formRows, Fields: []Field{
		{Type: "text", Name: "tz", Label: "Часовой пояс",
			Hint:  "Время в чатах, карточках и расписаниях. Берётся от приложения при первом запуске: Europe/Ulyanovsk, Europe/Moscow, UTC",
			Value: s.eng.Timezone()},
	}, Submit: &Action{Action: "set_timezone", Label: "Сохранить"}})

	add(Component{Type: "form", ID: "work-live", Title: "Как экран узнаёт об изменениях", Style: formRows, Fields: []Field{
		s.settingField("ui.poll_s", "poll_s", "Опрос экрана без провода",
			"Веб-клиент перечитывает открытый чат сам, пока живого провода нет", "с"),
		s.settingField("ui.poll_live_s", "poll_live_s", "Опрос при живом проводе",
			"Страховка: провод молчит и когда всё цело, и когда его тихо оборвали", "с"),
		s.settingField("ui.live_ms", "live_ms", "Окно склейки сигналов",
			"Пока Сёма пишет, экран перечитывается не чаще этого", "мс"),
		s.settingField("ui.ws_per_session", "ws_per_session", "Проводов на устройство",
			"Вкладок много не бывает; бесконечно их открывает только тот, кто хочет занять память", ""),
	}, Submit: &Action{Action: "set_live_settings", Label: "Сохранить"}})

	// редкое — «Тонкая настройка» (settings_rare.go)
	add(Component{Type: "form", ID: "work-wire", Title: "Провод и снимок экрана", Tier: tierRare, Style: formRows, Fields: []Field{
		s.settingField("ui.ws_write_s", "ws_write_s", "Провод: ждать клиента",
			"Не вычитал кадр за этот срок — соединение закрывается, клиент переподключится", "с"),
		s.settingField("ui.snapshot_days", "snapshot_days", "Снимок экрана живёт",
			"На устройстве, отмеченном своим, последний увиденный экран показывается без связи; по сроку он стирается, а не показывается устаревшим", "дней"),
	}, Submit: &Action{Action: "set_live_settings", Label: "Сохранить"}})
	// куда уходит время экрана: Server-Timing есть всегда, а профилировщик —
	// только по выбору и только входу с правом «всё» (perf-pass 1.3)
	add(Component{Type: "form", ID: "work-pprof", Title: "Скорость экранов", Tier: tierRare, Style: formRows, Fields: []Field{
		s.settingField("ui.list_rows", "list_rows", "Строк в длинном списке",
			"Сколько строк показывать сразу — расход на «Здоровье», свои задачи; остальное по «Показать ещё»", ""),
		s.settingField("ui.prefetch_ms", "prefetch_ms", "Экран, подгруженный заранее, живёт",
			"Наведение на пункт меню или касание подгружают экран до нажатия; столько он ждёт нажатия. 0 — не подгружать", "мс"),
		s.settingField("http.redirect_tls", "redirect_tls", "Открытый адрес ведёт на защищённый",
			"Страница с http://…:8765 переходит на https://…:8443, когда защищённый порт работает: там HTTP/2 и вход по passkey. API, экраны и ссылки на файлы не трогаются", ""),
		s.settingField("debug.slow_sql_ms", "slow_sql_ms", "Медленный запрос к базе",
			"Самый долгий запрос экрана дольше этого — строка в журнале сервера: сколько, какой запрос (без значений) и кто его позвал. 0 — не писать", "мс"),
		s.settingField("debug.pprof", "pprof", "Профилировщик Go",
			"Открывает /debug/pprof входу с правом «всё»: где сервер тратит время и память. Пока включён, «Здоровье» об этом напоминает", ""),
	}, Submit: &Action{Action: "set_live_settings", Label: "Сохранить"}})
	return nil
}

// buildWorkChats — «Чаты и ветки»: чат, который ядро заводит под команду из
// уведомления, живёт по сроку, а замолчавшего Сёму ядро пинает; ветка под
// разговором видит хвост родителя.
func (s *Server) buildWorkChats(doc *Document) error {
	doc.Subheading = "Срок чатов команд, пинатель и хвост ветки"
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	add(Component{Type: "form", ID: "work-chats", Title: "Чаты команд", Style: formRows, Fields: []Field{
		s.settingField("chats.command_ttl_h", "cmd_ttl", "Чаты команд живут",
			"«Выполнить» из уведомления идёт через чат: Сёма видит результат и продолжает; чат убирается по сроку, каждое подтверждение его продлевает", "ч"),
		s.settingField("chats.nudge_after_min", "nudge_after", "Пинать чат через",
			"Сёма сам продолжает начатое: делает следующий шаг, ждёт таймером или говорит, чего ждёт от вас", "мин"),
		s.settingField("chats.nudge_max", "nudge_max", "Пинков на чат",
			"0 — не пинать", "раз"),
	}, Submit: &Action{Action: "set_chat_settings", Label: "Сохранить"}})

	add(Component{Type: "form", ID: "work-threads", Title: "Ветки", Style: formRows, Fields: []Field{
		s.settingField("chats.thread_tail", "thread_tail", "Хвост родителя в ветке",
			"Сколько последних реплик разговора Сёма видит, отвечая в ветке", "реплик"),
	}, Submit: &Action{Action: "set_chat_settings", Label: "Сохранить"}})

	// шаги читающих вызовов и обещание без действия (engine/chat_steps.go)
	add(Component{Type: "form", ID: "work-steps", Title: "Шаги в ответе", Tier: tierRare, Style: formRows, Fields: []Field{
		s.settingField("chats.read_steps", "read_steps", "Ходов модели за одно обращение",
			"Пока модель зовёт читающие инструменты, она получает их результат и следующий ход. Вызов, ждущий вашей кнопки, шаги заканчивает", "ходов"),
		s.settingField("chats.promise_markers", "promise_markers", "Слова, которыми модель обещает действие",
			"Ответ с таким словом и без блока человеку не уходит. Модель один раз получает напоминание сделать обещанное; слова через запятую", ""),
		s.settingField("chats.claim_markers", "claim_markers", "Слова, которыми модель говорит «уже сделано»",
			"Ответ на ваше сообщение с таким словом, но без единого блока, — значит, ничего не случилось: модель получает напоминание; слова через запятую", ""),
	}, Submit: &Action{Action: "set_chat_settings", Label: "Сохранить"}})

	// поручения: работа «до готовности» (engine/assign.go)
	add(Component{Type: "form", ID: "work-assign", Title: "Поручения", Tier: tierRare, Style: formRows, Fields: []Field{
		s.settingField("assign.confirm", "assign_confirm", "Начинать поручение только кнопкой",
			"Выключено — план из чата сразу идёт в работу (из Telegram кнопку не нажать). Включено — ждёт «Начать» в Сёме", ""),
		s.settingField("assign.max_steps", "assign_max_steps", "Шагов в плане не больше",
			"Лишние шаги плана отбрасываются: большой план лучше разбить на два поручения", "шагов"),
		s.settingField("assign.step_turns", "assign_step_turns", "Ходов на шаг без итога",
			"Столько раз Сёма пробует сделать шаг сам; потом спрашивает вас, что мешает", "ходов"),
		s.settingField("assign.days", "assign_days", "Сколько дней поручение идёт само",
			"Дольше — Сёма спросит, продолжать ли", "дн."),
		s.settingField("assign.tracker", "assign_tracker", "Трекер задач",
			"Сервер MCP, где поручение заводит задачу: «auto» — первый открытый чату сервер с инструментом создания задачи; пусто — без трекера", ""),
		s.settingField("assign.tracker_project", "assign_tracker_project", "Проект в трекере",
			"Номер проекта для новых задач; пусто — как решит трекер (входящие)", ""),
		s.settingField("assign.tracker_create_tool", "assign_tracker_create", "Инструмент создания задачи",
			"Имя инструмента у сервера трекера; аргументы — как у Planner: title, note, projectId", ""),
		s.settingField("assign.tracker_done_tool", "assign_tracker_done", "Инструмент «задача сделана»",
			"Имя инструмента у сервера трекера; аргумент — id задачи", ""),
		s.settingField("assign.tracker_cancel_tool", "assign_tracker_cancel", "Инструмент «задача отменена»",
			"Им отменяется зеркало снятого поручения; аргумент — id задачи; пусто — трекер отмены не знает", ""),
	}, Submit: &Action{Action: "set_chat_settings", Label: "Сохранить"}})
	return nil
}

// workActions — сохранение вкладок «Работы Сёмы». Каждое поле необязательно:
// что пришло, то и проверяется; сначала проверяем всё, потом пишем.
func (s *Server) workActions(req actionRequest) (string, error) {
	switch req.Action {
	case "set_live_settings":
		return "", s.saveSettings(req, workLiveParams...)
	case "set_chat_settings":
		return "", s.saveSettings(req, workChatParams...)
	case "set_timezone":
		return "", s.saveTimezone(req)
	}
	return "", errUnhandled
}

// saveTimezone — сменить часовой пояс. Не пришло поле — не трогали; стёртое —
// отказ с причиной, а не молчаливое «сохранено»; неизвестный пояс отвергает
// `SetTimezone`.
func (s *Server) saveTimezone(req actionRequest) error {
	raw, ok := req.Params["tz"]
	if !ok {
		return nil
	}
	name := strings.TrimSpace(raw)
	if name == "" {
		return fmt.Errorf("часовой пояс: впишите название, например Europe/Moscow")
	}
	if name == s.eng.Timezone() {
		return nil
	}
	return s.eng.SetTimezone(name)
}

// saveWorkSettings — всё, что переехало с «Уведомлений», одним действием: так
// его знает прежнее `set_notify_settings`. Пустое поле у него значило «не
// трогали», и это сохранено.
func (s *Server) saveWorkSettings(req actionRequest) error {
	filled := req
	filled.Params = map[string]string{}
	for name, value := range req.Params {
		if strings.TrimSpace(value) != "" {
			filled.Params[name] = value
		}
	}
	list := make([]settingParam, 0, len(workLiveParams)+len(workChatParams))
	list = append(append(list, workLiveParams...), workChatParams...)
	if err := s.saveSettings(filled, list...); err != nil {
		return err
	}
	return s.saveTimezone(filled)
}
