package api

// Пуши на устройства: «Настройки → Уведомления → Каналы доставки». Здесь
// вторая половина канала `web` — не «куда» (это строка канала), а «кому»:
// каждое подписанное устройство отдельной строкой, со своим именем,
// пробным пушем и последним ответом службы.
//
// Порог и тихие часы у канала общие, а у устройства могут быть свои:
// телефону ночью тишина, Mac — как был. Пустое значение — «как у канала»,
// поэтому прежний канал работает без единой правки.
//
// Имя, порог и тихие часы устройства — поля формы в раскрытии строки
// («Настройки»), а не вопросы в окне (2 октября 2026): текущее значение видно,
// формат часов помнить не нужно. Форму сохраняет нижняя полоса действием
// `save_push_device`; прежние `push_rename`, `push_level`, `push_quiet` остались
// под своими именами. Параметры пушей (адрес для связи, сроки хранения, срочность,
// «тревога висит») — «Тонкая настройка» вкладки.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const pushLevelInherit = "как у канала"

// pushLevelOptions — сегменты порога устройства: «как у канала» и три уровня теми же
// словами, что у порога канала (прежнее действие `push_level` принимает и их, и
// прежние «от «инфо»»: уровень в нём ищется по слову)
var pushLevelOptions = append([]string{pushLevelInherit}, notifyLevelWords...)

// Тихие часы устройства: три состояния, а не одно поле. Пусто в записи — «как у
// канала», «-» — «своих часов нет и канальные не действуют», «с-до» — свои.
const (
	pushQuietInherit = "как у канала"
	pushQuietOwn     = "свои"
	pushQuietNone    = "без тихих часов"
)

var pushQuietModes = []string{pushQuietInherit, pushQuietOwn, pushQuietNone}

// pushUrgencies — срочность по RFC 8030, как её называет служба.
var pushUrgencies = []string{"high", "normal", "low", "very-low"}

// pushWebChannel — канал, чьи порог и тихие часы наследуют устройства.
func pushWebChannel(chans []*store.NotifyChannel) *store.NotifyChannel {
	var first *store.NotifyChannel
	for _, c := range chans {
		if c.Kind != "web" {
			continue
		}
		if c.Enabled {
			return c
		}
		if first == nil {
			first = c
		}
	}
	return first
}

// pushOffer — предложение подписать это устройство на пуши, наверху экрана
// «Ждут вас». Туда человек попадает с телефона одним касанием (по пушу, по
// значку, по нижней полосе), а до «Настроек → Уведомления → Пуши на
// устройства» четыре шага — и на iPhone до кнопки не дошли.
//
// Сервер не знает, подписано ли устройство: подписка живёт в браузере. Поэтому
// строка приезжает скрытой (класс `offer`, правило в style.css), а клиент
// открывает её, когда состояние такое, что человек может что-то сделать
// (`pushPaint` в app.js), и прячет при «подписано». «Получать пуши» — основная
// кнопка, «Не предлагать на этом устройстве» — в меню «···»: клиентское
// действие, запоминается в браузере и на сервер не ходит.
func pushOffer() Component {
	return Component{Type: "row", Layout: "wide", ID: "push-offer", Style: "offer", Icon: "bell",
		Subtitle: "Пуши на этом устройстве",
		Caption:  "Подпишите это устройство, и уведомления будут приходить, даже когда Сёма закрыт.",
		Actions:  []Action{{Action: "$push", Label: "Получать пуши", Style: "primary"}},
		Menu:     []Action{{Action: "$push_later", Label: "Не предлагать на этом устройстве"}}}
}

// pushDevicesSection — подписка этого устройства и список всех устройств.
func (s *Server) pushDevicesSection(add func(Component), chans []*store.NotifyChannel) {
	subs, _ := s.eng.Store().PushSubscriptions()
	web := pushWebChannel(chans)

	// Подписка этого устройства — действие браузера, не сервера: страница
	// сама пишет сюда, подписано ли устройство и что мешает (клиент знает
	// и систему, и разрешение, и режим окна). Статичный текст ниже — на
	// случай, если сценарий страницы не отработал
	rows := []Component{{Type: "row", Layout: "wide", ID: "push-here", Subtitle: "Пуши на этом устройстве",
		Caption: "Браузер спросит разрешение и подпишет именно это устройство. На iPhone пуши работают только у Сёмы, добавленного на экран «Домой»; " +
			"на Android — и во вкладке Chrome, и у установленного приложения.",
		Actions: []Action{{Action: "$push", Label: "Получать пуши", Style: "primary"}}}}

	// беды, из-за которых не дойдёт никуда: называем до списка, и у каждой есть
	// действие, которое ведёт прямо к настройке
	switch {
	case web == nil:
		rows = append(rows, Component{Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Канала «web» нет — пуши никуда не уходят",
			Caption:  "Подписанные устройства получают пуши через канал вида web. Добавьте его кнопкой «Добавить канал» выше.",
			Actions:  []Action{{Action: "$goto", Target: entityAddr(notifySettingsScreen, notifyNew, ""), Label: "Добавить канал", Style: "primary"}}})
	case !web.Enabled:
		rows = append(rows, Component{Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Канал «" + web.Title + "» выключен — пуши не уходят",
			Caption:  "Включите его тумблером в списке каналов выше."})
	}
	if web != nil && !s.eng.HTTPSOK() {
		// условия, без которых канал невозможен, называются здесь же, где его
		// включают, а не обнаруживаются потом по отказам
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "push-https", Level: LevelAlert, Dot: levelDot(LevelAlert),
			Subtitle: "Пуши не заработают без https",
			Caption:  "Браузер разрешает пуши только по защищённому адресу: задайте адрес входа https://… на «Доступе», если https даёт прокси",
			Actions:  []Action{{Action: "$goto", Target: accessScreen, Label: "Открыть «Вход и доступ»", Style: "primary"}}})
	}
	if _, real := s.eng.PushSubject(); !real {
		rows = append(rows, Component{Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Адрес для связи не задан",
			Caption: "Apple (iPhone) отвергает пуши без настоящего адреса для связи — https:// или mailto: (403 BadJwtToken). " +
				"Google и Mozilla пока принимают. Задайте адрес в «Тонкой настройке» ниже; пусто — берётся адрес входа (auth.origin), он здесь не подошёл."})
	}

	for _, p := range subs {
		rows = append(rows, s.pushDeviceRow(p, web))
	}
	if len(subs) == 0 {
		rows = append(rows, emptyState("Ни одно устройство не подписано",
			"Откройте Сёму на телефоне или в браузере компьютера и нажмите «Получать пуши» в строке выше. "+
				"iPhone: сначала «Поделиться» → «На экран „Домой“», затем открыть Сёму оттуда. "+
				"На телефоне та же кнопка стоит и наверху «Ждут вас».", "bell"))
	}
	add(Component{Type: "section", ID: "push-devices", Title: fmt.Sprintf("Пуши на устройства · %d", len(subs)), Children: rows})
}

// pushQuietMode — какое из трёх состояний тихих часов у устройства.
func pushQuietMode(quiet string) string {
	switch quiet {
	case "":
		return pushQuietInherit
	case "-":
		return pushQuietNone
	}
	return pushQuietOwn
}

// pushDeviceRow — одно устройство: имя, последний ответ службы, пробный пуш именно
// ему; «Настройки» раскрываются на месте — имя, порог и тихие часы полями.
func (s *Server) pushDeviceRow(p *store.PushSubscription, web *store.NotifyChannel) Component {
	name := engine.PushDeviceLabel(p)
	id := strconv.FormatInt(p.ID, 10)

	level, status := LevelInfo, "доставок ещё не было — нажмите «Проверить»"
	switch {
	case p.LastErr != "":
		level, status = LevelWarn, "не удалось "+whenText(p.ErrAt)+": "+p.LastErr
	case p.LastOK != "":
		level = LevelOK
		status = "последняя доставка " + whenText(p.LastOK)
		if p.LastStatus > 0 {
			status += " · " + engine.PushServiceName(p.Endpoint) + " ответила " + strconv.Itoa(p.LastStatus)
		}
	}
	caption := "подписано " + whenText(p.Created) + " · " + status

	// что действует на самом деле: своё или канальное — и чьё именно; по этому
	// видно, чем «как у канала» кончится
	minLevel, quietFrom, quietTo := "warn", "", ""
	if web != nil {
		minLevel, quietFrom, quietTo = web.MinLevel, web.QuietFrom, web.QuietTo
	}
	channelLevel := notifyLevelWord(minLevel)
	channelQuiet := "нет"
	if span := quietSpan(quietFrom, quietTo); span != "" {
		channelQuiet = strings.ReplaceAll(span, "-", "–")
	}
	// капсулы — только то, что отличает устройство от канала
	var badges []Badge
	if p.MinLevel != "" {
		badges = append(badges, Badge{Text: "свой порог: " + notifyLevelWord(p.MinLevel)})
	}
	switch pushQuietMode(p.Quiet) {
	case pushQuietOwn:
		badges = append(badges, Badge{Text: "свои тихие часы"})
	case pushQuietNone:
		badges = append(badges, Badge{Text: pushQuietNone})
	}

	choice := pushLevelInherit
	if p.MinLevel != "" {
		choice = notifyLevelWord(p.MinLevel)
	}
	ownSpan := ""
	if mode := pushQuietMode(p.Quiet); mode == pushQuietOwn {
		ownSpan = p.Quiet
	}
	autoName := engine.PushDeviceLabel(&store.PushSubscription{Agent: p.Agent, Endpoint: p.Endpoint})

	return Component{Type: "row", Layout: "wide", ID: "push-" + id, Level: level, Dot: levelDot(level),
		Subtitle: name, Caption: caption, Badges: badges,
		Actions: []Action{{Action: "push_test", Target: id, Label: "Проверить"}},
		Menu: []Action{{Action: "push_unsubscribe", Target: id, Label: "Убрать…", Style: "destructive",
			Confirm: "Убрать «" + name + "»? Пуши на него перестанут приходить; подписаться снова можно с самого устройства.", ConfirmOK: "Убрать"}},
		Expandable: true, Expand: "Настройки",
		Children: []Component{{Type: "form", ID: "push-form-" + id, Style: formRows,
			Fields: []Field{
				{Type: "text", Name: "title", Label: "Имя устройства", Value: p.Title,
					Hint: "пусто — как определилось само (сейчас: " + autoName + ")"},
				{Type: "segments", Name: "level", Label: "Порог уровня", Options: pushLevelOptions, Value: choice,
					Tones: []string{"", "", LevelWarn, LevelAlert},
					Hint:  "ниже порога устройство молчит; «как у канала» — сейчас «" + channelLevel + "»"},
				{Type: "segments", Name: "quiet_mode", Label: "Тихие часы", Options: pushQuietModes, Value: pushQuietMode(p.Quiet),
					Hint: "«как у канала» — сейчас " + channelQuiet + "; «свои» — часы из поля ниже; «без тихих часов» — устройство получает пуши и ночью"},
				{Type: "timespan", Name: "quiet", Label: "Свои тихие часы", Value: ownSpan,
					Hint: "действуют, только когда выше выбрано «свои»"},
			},
			Submit: &Action{Action: "save_push_device", Target: id, Label: "Сохранить"}}}}
}

// pushSettingsForm — параметры пушей: «Тонкая настройка» вкладки. Адрес для связи,
// сроки хранения, срочность и «тревога висит» правят раз в год; умолчания живут в
// реестре настроек (`engine/setting_defs.go`), откуда читает и сам движок. Форма
// одна, действие прежнее — `set_push_settings`.
func (s *Server) pushSettingsForm(add func(Component), st *store.Store) {
	subject, real := s.eng.PushSubject()
	hint := "mailto:вы@почта.рф или https://адрес-сёмы. Apple без настоящего адреса отвечает 403 BadJwtToken. Пусто — берётся адрес входа (auth.origin)"
	if real {
		hint += "; сейчас в подписи: " + subject
	}
	const urgencyHint = "high — разбудить сразу; low и very-low служба может придержать до зарядки и Wi-Fi"
	urgency := func(key, name, label, hint string) Field {
		f := s.settingField(key, name, label, hint, "")
		f.Type, f.Options = "picker", pushUrgencies
		// непонятное значение движок читает как умолчание: поле показывает то же
		valid := false
		for _, u := range pushUrgencies {
			valid = valid || u == f.Value
		}
		if !valid && f.Default != nil {
			f.Value = *f.Default
		}
		return f
	}
	add(Component{Type: "form", ID: "push-settings", Title: "Параметры пушей", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("push.subject", "subject", "Адрес для связи", hint, ""),
			s.settingField("push.ttl_min", "ttl_min", "Хранить пуш для спящего устройства",
				"служба держит его, пока телефон без сети или выключен; потом выбрасывает", "минут"),
			s.settingField("push.ttl_alert_min", "ttl_alert_min", "То же для тревоги",
				"тревога не должна протухать за ночь: сутки по умолчанию", "минут"),
			s.settingField("push.sticky_alert", "sticky_alert", "Тревога висит на компьютере, пока её не закроют",
				"на телефонах не действует; по умолчанию уведомление уходит в центр уведомлений само", ""),
			urgency("push.urgency.alert", "urgency_alert", "Срочность тревоги", urgencyHint),
			urgency("push.urgency.warn", "urgency_warn", "Срочность «внимания»", ""),
			urgency("push.urgency.info", "urgency_info", "Срочность «инфо»", ""),
		},
		Submit: &Action{Action: "set_push_settings", Label: "Сохранить"}})
}

// pushActions — действия над устройствами и параметрами пушей.
func (s *Server) pushActions(req actionRequest) (string, error) {
	eng := s.eng
	st := eng.Store()
	device := func() (*store.PushSubscription, error) {
		id, err := strconv.ParseInt(strings.TrimSpace(req.Target), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("устройство не названо")
		}
		p, ok := st.PushSubscriptionByID(id)
		if !ok {
			return nil, fmt.Errorf("такого устройства уже нет")
		}
		return p, nil
	}
	switch req.Action {
	case "push_unsubscribe":
		// прежняя запись в строке называла устройство адресом у службы,
		// нынешняя — номером: понимаем обе
		if _, err := strconv.ParseInt(strings.TrimSpace(req.Target), 10, 64); err != nil {
			return notifySettingsScreen, eng.PushUnsubscribe(req.Target)
		}
		p, err := device()
		if err != nil {
			return notifySettingsScreen, nil // убрали уже: цель достигнута
		}
		return notifySettingsScreen, eng.PushUnsubscribe(p.Endpoint)
	case "push_test":
		p, err := device()
		if err != nil {
			return "", err
		}
		// отказ службы — ошибка действия: человек видит её сразу, а не ищет в строке
		_, err = eng.TestPushDevice(p.ID)
		return notifySettingsScreen, err
	case "save_push_device":
		return s.savePushDevice(req, device)
	case "push_rename":
		p, err := device()
		if err != nil {
			return "", err
		}
		return notifySettingsScreen, st.SetPushTitle(p.ID, clipRunes(strings.TrimSpace(req.Params["value"]), 80))
	case "push_level":
		p, err := device()
		if err != nil {
			return "", err
		}
		level := ""
		if v := req.Params["value"]; v != "" && v != pushLevelInherit {
			level = levelFromOption(v)
		}
		return notifySettingsScreen, st.SetPushLevel(p.ID, level)
	case "push_quiet":
		p, err := device()
		if err != nil {
			return "", err
		}
		quiet, err := parsePushQuiet(req.Params["value"])
		if err != nil {
			return "", err
		}
		return notifySettingsScreen, st.SetPushQuiet(p.ID, quiet)
	case "set_push_settings":
		if v := strings.TrimSpace(req.Params["subject"]); v != "" {
			subject, ok := engine.NormalizePushSubject(v)
			if !ok {
				return "", fmt.Errorf("адрес для связи — это mailto:вы@почта.рф или https://адрес-сёмы с настоящим именем (не localhost и не имя из своей сети)")
			}
			_ = st.SetSetting("push.subject", subject)
		} else if _, has := req.Params["subject"]; has {
			_ = st.SetSetting("push.subject", "")
		}
		for name, key := range map[string]string{"ttl_min": "push.ttl_min", "ttl_alert_min": "push.ttl_alert_min"} {
			if v := strings.TrimSpace(req.Params[name]); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return "", fmt.Errorf("срок хранения — число минут, не меньше одной")
				}
				_ = st.SetSetting(key, strconv.Itoa(n))
			}
		}
		// тумблер: невыбранный браузер не шлёт вовсе, но форма перечисляет свои
		// тумблеры в `toggles`, и сервер восстанавливает «false» — так что
		// пришедшее поле значит выбор человека, а непришедшее — не наша форма
		if v, ok := req.Params["sticky_alert"]; ok {
			on := "0"
			if v == "true" {
				on = "1"
			}
			_ = st.SetSetting("push.sticky_alert", on)
		}
		for name, level := range map[string]string{"urgency_alert": "alert", "urgency_warn": "warn", "urgency_info": "info"} {
			if v := strings.TrimSpace(req.Params[name]); v != "" {
				ok := false
				for _, u := range pushUrgencies {
					ok = ok || u == v
				}
				if !ok {
					return "", fmt.Errorf("срочность — одно из: %s", strings.Join(pushUrgencies, ", "))
				}
				_ = st.SetSetting("push.urgency."+level, v)
			}
		}
		return notifySettingsScreen, nil
	}
	return "", errUnhandled
}

// savePushDevice — «Сохранить» настроек устройства: имя, порог, тихие часы. Сначала
// проверяется всё, потом пишется: половина формы не сохраняется. Непришедшее поле —
// «не трогали».
func (s *Server) savePushDevice(req actionRequest, device func() (*store.PushSubscription, error)) (string, error) {
	st := s.eng.Store()
	p, err := device()
	if err != nil {
		return "", err
	}
	title, hasTitle := req.Params["title"]
	level, hasLevel := req.Params["level"]
	var newLevel string
	if hasLevel {
		if level != "" && level != pushLevelInherit {
			newLevel = levelFromOption(level)
		}
	}
	// тихие часы: режим решает, что пишется; «свои» требуют обоих времён
	mode, hasMode := req.Params["quiet_mode"]
	from, to, hasSpan, err := quietParam(req.Params, "quiet")
	if err != nil && (mode == pushQuietOwn || !hasMode) {
		return "", err
	}
	var newQuiet string
	changeQuiet := false
	switch {
	case hasMode && mode == pushQuietInherit:
		changeQuiet = true
	case hasMode && mode == pushQuietNone:
		newQuiet, changeQuiet = "-", true
	case hasMode && mode == pushQuietOwn:
		if from == "" || to == "" {
			return "", fmt.Errorf("тихие часы: выбрано «свои», а времени нет — впишите «с» и «до» или выберите другое")
		}
		newQuiet, changeQuiet = from+"-"+to, true
	case !hasMode && hasSpan && from != "":
		// форма без режима (вызов напрямую): записанное окно — свои часы
		newQuiet, changeQuiet = from+"-"+to, true
	}
	if hasTitle {
		if err := st.SetPushTitle(p.ID, clipRunes(strings.TrimSpace(title), 80)); err != nil {
			return "", err
		}
	}
	if hasLevel {
		if err := st.SetPushLevel(p.ID, newLevel); err != nil {
			return "", err
		}
	}
	if changeQuiet {
		if err := st.SetPushQuiet(p.ID, newQuiet); err != nil {
			return "", err
		}
	}
	return "", nil
}

// parsePushQuiet — «23:00-8:00» → как есть; пусто → «как у канала»; «нет» и
// «-» → «без тихих часов вовсе».
func parsePushQuiet(in string) (string, error) {
	v := strings.TrimSpace(strings.NewReplacer("–", "-", "—", "-").Replace(in))
	switch strings.ToLower(v) {
	case "":
		return "", nil
	case "-", "нет":
		return "-", nil
	}
	from, to, ok := strings.Cut(v, "-")
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if !ok || from == "" || to == "" {
		return "", fmt.Errorf("тихие часы — два времени через тире, например 23:00-8:00")
	}
	if _, err := time.Parse("15:04", from); err != nil {
		return "", fmt.Errorf("время вида 23:00")
	}
	if _, err := time.Parse("15:04", to); err != nil {
		return "", fmt.Errorf("время вида 8:00 или 08:00")
	}
	return from + "-" + to, nil
}
