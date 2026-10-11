package api

// «Настройки → Уведомления → Каналы доставки»: куда уведомление доставляется
// кроме экрана. Здесь же — «Пуши на устройства» (notifications_push.go).
//
// До 2 октября 2026 порог канала был списком в строке, а тихие часы, источники и
// название — вопросами в окне (`Prompt`): текущее значение не видно, формат
// («23:00-8:00», «servers, mail…») надо помнить, ошибка приходит после «ОК». Теперь
// у строки канала «Настройки» раскрываются на месте и это обычные поля формы:
//
//	Название      — текст
//	Порог уровня  — сегменты «инфо · внимание · тревога»
//	Источники     — чипы «Серверы · Почта · Запросы · Модули»
//	Тихие часы    — два поля времени; пусто в обоих — без тихих часов
//
// Форму сохраняет нижняя полоса действием `save_notify_channel`. «Проверить» —
// кнопка строки, «Удалить…» — в «···», тумблер «включён» — в строке. Прежние
// действия (`notify_channel_level`, `…_quiet`, `…_sources`, `…_title`) остались
// под своими именами: на них смотрят скрипты и старые карточки.
//
// «Добавить канал» — свой экран `settings:notify@new`, а не форма под списком.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// notifyNew — «канал» с этим словом в адресе означает экран «Добавить канал».
const notifyNew = "new"

// notifySourceGroups — группы источников записи: ключ (так лежит в канале) и слово
// на экране. Порядок — порядок чипов. Ключи называет `engine.NotifySourceGroup`.
var notifySourceGroups = []struct{ Key, Title string }{
	{"servers", "Серверы"},
	{"mail", "Почта"},
	{"requests", "Запросы"},
	{"modules", "Модули"},
}

// notifySourceOptions — подписи чипов «Источники».
func notifySourceOptions() []string {
	out := make([]string, 0, len(notifySourceGroups))
	for _, g := range notifySourceGroups {
		out = append(out, g.Title)
	}
	return out
}

// sourcesChosen — какие чипы отмечены у канала: пустое поле канала — все.
func sourcesChosen(sources string) []string {
	all := strings.TrimSpace(sources) == ""
	have := map[string]bool{}
	for _, k := range strings.Split(strings.ReplaceAll(sources, " ", ""), ",") {
		have[k] = true
	}
	var out []string
	for _, g := range notifySourceGroups {
		if all || have[g.Key] {
			out = append(out, g.Title)
		}
	}
	return out
}

// sourcesFromChoice — отмеченные чипы обратно в поле канала. Отмечены все — поле
// пусто («все источники»), как и было без списка; ни одного — отказ: канал без
// источников молчал бы навсегда, а выключается он тумблером.
func sourcesFromChoice(chosen []string) (string, error) {
	if len(chosen) == 0 {
		return "", fmt.Errorf("источники: отметьте хотя бы один — чтобы канал молчал совсем, выключите его")
	}
	var keys []string
	for _, g := range notifySourceGroups {
		for _, title := range chosen {
			if title == g.Title {
				keys = append(keys, g.Key)
			}
		}
	}
	if len(keys) == len(notifySourceGroups) {
		return "", nil
	}
	return strings.Join(keys, ","), nil
}

// sourcesWords — источники канала для подписи строки.
func sourcesWords(sources string) string {
	if strings.TrimSpace(sources) == "" {
		return "все источники"
	}
	return "источники: " + strings.Join(sourcesChosen(sources), ", ")
}

// checkedParams — какие варианты поля «checks» выбраны: у каждого чипа свой флажок
// «имя.вариант» («true» — отмечен), снятый форма присылает как «false» (список
// флажков идёт в `toggles`). Второй результат — пришло ли поле вовсе: нет ни одного
// флажка значит «не трогали».
func checkedParams(params map[string]string, name string, options []string) ([]string, bool) {
	var out []string
	present := false
	for _, opt := range options {
		v, has := params[name+"."+opt]
		if !has {
			continue
		}
		present = true
		if truthy(v) {
			out = append(out, opt)
		}
	}
	return out, present
}

// quietSpan — тихие часы канала одним значением «23:00-08:00»; пусто — нет.
func quietSpan(from, to string) string {
	if from == "" || to == "" {
		return ""
	}
	return from + "-" + to
}

// quietParam — тихие часы из двух полей времени. Оба пусты — «нет тихих часов»;
// одно из двух — ошибка: половина окна ничего не значит; совпадающие концы тоже —
// такое окно не наступает, и тихие часы молча стали бы «никогда». Третий результат —
// пришло ли поле вовсе: непришедшее значит «не трогали».
func quietParam(params map[string]string, name string) (from, to string, present bool, err error) {
	rawFrom, hasFrom := params[name+"_from"]
	rawTo, hasTo := params[name+"_to"]
	if !hasFrom && !hasTo {
		return "", "", false, nil
	}
	from, to = strings.TrimSpace(rawFrom), strings.TrimSpace(rawTo)
	switch {
	case from == "" && to == "":
		return "", "", true, nil
	case from == "" || to == "":
		return "", "", true, fmt.Errorf("тихие часы: нужны оба времени — «с» и «до», либо ни одного (без тихих часов)")
	case from == to:
		return "", "", true, fmt.Errorf("тихие часы: «с» и «до» совпадают — такие часы не наступят; для работы без тихих часов оставьте оба поля пустыми")
	}
	if _, e := time.Parse("15:04", from); e != nil {
		return "", "", true, fmt.Errorf("тихие часы: время вида 23:00")
	}
	if _, e := time.Parse("15:04", to); e != nil {
		return "", "", true, fmt.Errorf("тихие часы: время вида 08:00")
	}
	return from, to, true, nil
}

// channelKindWord — вид канала словами для капсулы.
func channelKindWord(kind string) string {
	switch {
	case kind == "mac":
		return "Mac"
	case kind == "web":
		return "пуши в браузер"
	case strings.HasPrefix(kind, "module:"):
		return "модуль " + strings.TrimPrefix(kind, "module:")
	}
	return kind
}

// channelKindSaid — название канала уже говорит, какого он вида («Mac · системные
// уведомления», «Пуши на устройства»): капсула с видом тогда повторила бы название.
func channelKindSaid(c *store.NotifyChannel) bool {
	title := strings.ToLower(c.Title)
	switch {
	case c.Kind == "mac":
		return strings.Contains(title, "mac")
	case c.Kind == "web":
		return strings.Contains(title, "пуш") || strings.Contains(title, "push")
	case strings.HasPrefix(c.Kind, "module:"):
		return strings.Contains(title, strings.ToLower(strings.TrimPrefix(c.Kind, "module:")))
	}
	return false
}

// lastDelivery — последняя запись журнала доставки канала: когда и чем кончилась.
func lastDelivery(log []string) (when string, failed bool) {
	if len(log) == 0 {
		return "", false
	}
	at, _, hasErr := strings.Cut(log[0], " · ")
	return at, hasErr
}

// channelPill — как канал себя чувствует, одним словом: норма названа тоже, а то
// «выключен» и «работает» читались бы по цвету тумблера. Второй результат —
// уровень строки.
func (s *Server) channelPill(c *store.NotifyChannel, subs int, failed bool) (Badge, string) {
	switch {
	case !c.Enabled:
		return Badge{Text: "выключен"}, ""
	case c.Kind == "web" && !s.eng.HTTPSOK():
		return Badge{Text: "без https не заработает", Level: LevelAlert}, LevelAlert
	case c.Kind == "web" && subs == 0:
		return Badge{Text: "нет устройств", Level: LevelWarn}, LevelWarn
	case c.Kind == "mac" && strings.HasPrefix(s.eng.MacChannelNote(), "показывать некому"):
		return Badge{Text: "некому показывать", Level: LevelWarn}, LevelWarn
	case strings.HasPrefix(c.Kind, "module:"):
		if m, ok := s.eng.Module(strings.TrimPrefix(c.Kind, "module:")); !ok || !m.Enabled {
			return Badge{Text: "модуль выключен", Level: LevelWarn}, LevelWarn
		}
	}
	if failed {
		return Badge{Text: "не дошло", Level: LevelWarn}, LevelWarn
	}
	return Badge{Text: "работает", Level: LevelOK}, ""
}

// channelPhrase — одна фраза строки: кто доставляет, когда было в последний раз,
// тихие часы и источники. Подробности и настройки — в раскрытии.
func (s *Server) channelPhrase(c *store.NotifyChannel, subs int, when string, failed bool) string {
	var parts []string
	switch {
	case c.Kind == "mac":
		// кто именно показывает, видно здесь же: канал молчит, когда житель
		// машины без связи или способность не разрешена
		parts = append(parts, s.eng.MacChannelNote())
	case strings.HasPrefix(c.Kind, "module:"):
		parts = append(parts, "доставляет модуль «"+strings.TrimPrefix(c.Kind, "module:")+"» функцией deliver")
	case c.Kind == "web":
		parts = append(parts, "пуш на подписанные устройства · "+plural(subs, "устройство", "устройства", "устройств"))
	}
	if when != "" {
		word := "последняя доставка "
		if failed {
			word = "последняя попытка не дошла: "
		}
		parts = append(parts, word+whenText(when))
	}
	if q := quietSpan(c.QuietFrom, c.QuietTo); q != "" {
		parts = append(parts, "тихие часы "+strings.ReplaceAll(q, "-", "–"))
	}
	parts = append(parts, sourcesWords(c.Sources))
	return strings.Join(parts, " · ")
}

// channelRow — тонкая строка канала: название, не больше двух капсул (состояние и
// вид), одна фраза, тумблер, «Проверить», «···»; «Настройки» раскрываются на
// месте — поля формы вместо вопросов в окне.
func (s *Server) channelRow(c *store.NotifyChannel, subs int) Component {
	st := s.eng.Store()
	id := strconv.FormatInt(c.ID, 10)
	when, failed := lastDelivery(st.NotifyLog(c.Kind, 1))
	pill, level := s.channelPill(c, subs, failed)
	row := Component{Type: "row", Layout: "wide", ID: "channel-" + id, Subtitle: c.Title, Toggle: boolToggle(c.Enabled),
		Caption: s.channelPhrase(c, subs, when, failed),
		Badges:  []Badge{pill},
		Actions: []Action{
			{Action: "toggle_notify_channel", Target: id, Label: "toggle"},
			{Action: "test_notify_channel", Target: id, Label: "Проверить"},
		},
		Menu: []Action{{Action: "delete_notify_channel", Target: id, Label: "Удалить…", Style: "destructive",
			Confirm: "Удалить канал «" + c.Title + "»?", ConfirmOK: "Удалить"}},
		Expandable: true, Expand: "Настройки"}
	if !channelKindSaid(c) {
		row.Badges = append(row.Badges, Badge{Text: channelKindWord(c.Kind)})
	}
	if level != "" {
		withLevel(&row, level)
	}
	levelHint := "ниже порога канал молчит, запись остаётся на экране «Уведомления»"
	if c.Kind == "web" {
		levelHint += " · у устройства может быть свой порог, он главнее"
	}
	quietHint := "в это время канал молчит; запись не теряется — её повторят после тихих часов. Пусто — без тихих часов"
	if c.Kind == "web" {
		quietHint += " · у устройства могут быть свои"
	}
	row.Children = []Component{{Type: "form", ID: "channel-form-" + id, Style: formRows,
		Fields: []Field{
			{Type: "text", Name: "title", Label: "Название", Value: c.Title},
			{Type: "segments", Name: "level", Label: "Порог уровня", Hint: levelHint,
				Options: notifyLevelWords, Tones: []string{"", LevelWarn, LevelAlert}, Value: notifyLevelWord(c.MinLevel)},
			{Type: "checks", Name: "sources", Label: "Источники", Options: notifySourceOptions(),
				Value: strings.Join(sourcesChosen(c.Sources), ","),
				Hint:  "откуда брать записи: серверы — отчёты по ним; почта — письма и напоминания; запросы — то, что ждёт вашего решения; модули — всё от модулей. Отмечены все — берёт всё"},
			{Type: "timespan", Name: "quiet", Label: "Тихие часы", Hint: quietHint, Value: quietSpan(c.QuietFrom, c.QuietTo)},
		},
		Submit: &Action{Action: "save_notify_channel", Target: id, Label: "Сохранить"}}}
	return row
}

// notifyLevelWords — порог словом на сегментах: те же три уровня, что и в списках
// «от «инфо»» прежних действий, без «от».
var notifyLevelWords = []string{"инфо", "внимание", "тревога"}

// notifyLevelWord — уровень канала словом сегментов.
func notifyLevelWord(level string) string {
	switch level {
	case "info":
		return notifyLevelWords[0]
	case "alert":
		return notifyLevelWords[2]
	}
	return notifyLevelWords[1]
}

// buildNotifyChannels — вкладка «Каналы доставки».
func (s *Server) buildNotifyChannels(doc *Document, add func(Component)) {
	st := s.eng.Store()
	// «Добавить канал» — единственная кнопка шапки вкладки: свой экран, а не форма
	// под списком
	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(notifySettingsScreen, notifyNew, ""),
		Label: "Добавить канал", Style: "primary"}}

	// доходит ли вообще — первым делом. Молчащий ассистент выглядит как
	// спокойная жизнь, и понять разницу человеку неоткуда
	if не, total, note := s.eng.DeliveryWeek(); total > 0 || note != "" {
		r := Component{Type: "row", Layout: "wide", Subtitle: "Доставка за неделю"}
		switch {
		case total == 0:
			r.Caption, r.Level = note, LevelInfo
		case не == 0:
			r.Caption, r.Level = fmt.Sprintf("дошли все %d", total), LevelOK
		default:
			r.Caption = fmt.Sprintf("не доставлено %d из %d", не, total)
			if note != "" {
				r.Caption += " · " + note
			}
			r.Level = LevelWarn
			if не*2 >= total {
				r.Level = LevelAlert
			}
			r.Actions = []Action{{Action: "$goto", Target: notifyScreen, Label: "Посмотреть"}}
		}
		add(Component{Type: "section", Title: "Доходит ли", Children: []Component{r}})
	}

	chans, _ := st.NotifyChannels()
	subs, _ := st.PushSubscriptions()
	rows := make([]Component, 0, len(chans))
	for _, c := range chans {
		rows = append(rows, s.channelRow(c, len(subs)))
	}
	if len(rows) == 0 {
		empty := emptyState("Каналов доставки нет",
			"Уведомления лежат на экране «Уведомления» всегда. Канал — куда ещё их доставить: Mac, пуши в браузер, мессенджер через модуль", "bell")
		empty.Actions = []Action{{Action: "$goto", Target: entityAddr(notifySettingsScreen, notifyNew, ""), Label: "Добавить канал", Style: "primary"}}
		rows = append(rows, empty)
	}
	add(Component{Type: "section", ID: "channels-list", Title: "Каналы доставки · " + strconv.Itoa(len(chans)), Children: rows})
	s.pushDevicesSection(add, chans)

	// параметры пушей — «Тонкая настройка» (settings_rare.go): адрес для связи,
	// сроки хранения и срочность правят раз в год
	s.pushSettingsForm(add, st)

	add(Component{Type: "section", ID: "channels-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Уведомление рождается в ядре и лежит на экране «Уведомления» всегда. Каналы — куда его ещё доставить: Mac, мессенджер через модуль. " +
			"У каждого канала свой порог уровня, источники и тихие часы — они раскрываются в строке канала. Ключи каналов — в хранилище секретов. " +
			"Пуш на устройство: обычное уведомление — заголовок и итог, закрытый ящик или «секретное» — только «есть новое», содержимого писем нет никогда; " +
			"Chrome и Edge на компьютере принимают пуш, пока запущены; iPhone — только у Сёмы, добавленного на экран «Домой»."}}})
}

// buildNotifyChannelNew — «Добавить канал»: свой экран `settings:notify@new`. Вид,
// название и настройки вида (JSON): имена полей прежние, как и действие.
func (s *Server) buildNotifyChannelNew(doc *Document) error {
	doc.Heading = "Добавить канал"
	doc.Subheading = "Куда ещё доставлять уведомления: порог, источники и тихие часы настраиваются в строке канала после добавления"
	doc.Layout = "read"
	kinds := []string{"mac", "web"}
	for _, m := range s.eng.Modules() {
		if m.Enabled && hasNeed(m.Needs, "notify_channel") {
			kinds = append(kinds, "module:"+m.Name)
		}
	}
	doc.Components = append(doc.Components,
		Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: notifySettingsScreen, Label: "‹ Уведомления"}}},
		Component{Type: "form", ID: "channel-new", Style: formRows,
			Fields: []Field{
				{Type: "picker", Name: "kind", Label: "Вид", Options: kinds, Value: kinds[0],
					Hint: "виды приносят модули с правом notify_channel — Telegram, Matrix, ntfy"},
				{Type: "text", Name: "title", Label: "Название", Hint: "например «Мой телефон»"},
				{Type: "text", Name: "config", Label: "Настройки", More: true,
					Hint: "JSON вида, например {\"chat\": \"123\", \"token\": \"telegram-bot\"} — ключи по именам из хранилища"},
			},
			Submit: &Action{Action: "add_notify_channel", Label: "Добавить", Style: "primary"}})
	return nil
}

// saveNotifyChannel — «Сохранить» настроек канала. Сначала проверяется всё, потом
// пишется: половина формы не сохраняется. Непришедшее поле — «не трогали».
func (s *Server) saveNotifyChannel(req actionRequest) (string, error) {
	st := s.eng.Store()
	c, ok := st.NotifyChannel(atoi64(req.Target))
	if !ok {
		return "", fmt.Errorf("канала нет")
	}
	if v, has := req.Params["title"]; has {
		title := strings.TrimSpace(v)
		if title == "" {
			return "", fmt.Errorf("у канала должно быть название")
		}
		c.Title = clipRunes(title, 80)
	}
	if v, has := req.Params["level"]; has && strings.TrimSpace(v) != "" {
		c.MinLevel = levelFromOption(v)
	}
	if chosen, present := checkedParams(req.Params, "sources", notifySourceOptions()); present {
		sources, err := sourcesFromChoice(chosen)
		if err != nil {
			return "", err
		}
		c.Sources = sources
	}
	from, to, present, err := quietParam(req.Params, "quiet")
	if err != nil {
		return "", err
	}
	if present {
		c.QuietFrom, c.QuietTo = from, to
	}
	return "", st.SaveNotifyChannel(c)
}
