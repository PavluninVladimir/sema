package api

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Раздел настроек «Что ушло наружу»: расход показывает цену вызова, а этот
// раздел — содержимое. Видно, какая модель, куда, что было в контексте и что
// именно отправлено после масок секретов.
//
// Адрес — `settings:sent` (2 октября 2026, шаг 8 плана
// `design/nastroyki-2/README.md`): раздел живёт в каркасе настроек, с колонкой
// разделов, и называется там так же, как над содержимым. Прежние адреса
// («sent», «sent~log», «sent~12») открывают то же самое (`canonSentAddr`).

const sentScreen = "settings:sent"

// sentTabLog — хвост адреса ленты: «settings:sent~log». Номером идёт одна
// отправка («settings:sent~12»), поэтому хвост-слово и хвост-число здесь
// значат разное — и разбираются по тому, число это или нет.
const sentTabLog = "log"

// sentScreenOld — адрес раздела до переезда в каркас настроек.
const sentScreenOld = "sent"

// canonSentAddr — адрес по нынешним правилам. Прежний «sent» (с хвостом
// «~log», «~<номер>» или «@…») открывает раздел «Что ушло наружу» там, где он
// теперь живёт; остальные адреса остаются как есть.
func canonSentAddr(screen string) string {
	switch {
	case screen == sentScreenOld:
		return sentScreen
	case strings.HasPrefix(screen, sentScreenOld+"~"):
		return sentScreen + strings.TrimPrefix(screen, sentScreenOld)
	case strings.HasPrefix(screen, sentScreenOld+"@"):
		// «@» у этого раздела не бывало: адрес вёл в сам раздел
		return sentScreen
	}
	return screen
}

// buildSentScreen — раздел и его два дочерних экрана: «settings:sent» —
// правила и хранение, «settings:sent~log» — лента отправок, «settings:sent~<id>»
// — одна отправка целиком.
func (s *Server) buildSentScreen(doc *Document, screen, filter string, limit int) error {
	_, tab := splitSubtab(screen)
	switch tab {
	case "":
		return s.buildSent(doc)
	case sentTabLog:
		return s.buildSentLog(doc, filter, limit)
	}
	id, _ := strconv.ParseInt(tab, 10, 64)
	return s.buildSentOne(doc, id)
}

// buildSentLog — сама лента: 958 отправок в сутки. Это хроника, поэтому она
// не листается вместе с правилами, а открывается своим экраном (из раздела и
// со «Здоровья»), но внутри каркаса: колонка разделов на месте, выбран «Что
// ушло наружу».
func (s *Server) buildSentLog(doc *Document, filter string, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Журнал отправок"
	doc.Subheading = "каждая просьба к модели: кто просил, что было в контексте и полный текст"
	doc.Layout = "wide"
	// полоса фильтров есть, а поиска по самой ленте нет: верхнее поле ищет по
	// настройкам, и на телефоне ему здесь места нет
	doc.noPhoneSearch = true
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	// обратный путь: правила и хранение живут в разделе, и вернуться
	// к ним надо чем-то, кроме кнопки браузера
	add(Component{Type: "crumb", Title: "Журнал отправок",
		Actions: []Action{{Action: "$goto", Target: sentScreen, Label: "Что ушло наружу"}}})

	day := s.eng.SentToday()
	// счётчики стояли строкой в заголовке раздела — там их никто не читал:
	// шесть чисел, склеенных точками, глаз не разбирает. Теперь это шапка
	// экрана, где у каждого числа своя подпись
	add(Component{Type: "figs", ID: "sent-figs", Children: []Component{
		{Type: "metric", Title: "отправок за сутки", Text: strconv.Itoa(day.Calls)},
		{Type: "metric", Title: "ушло наружу", Text: strconv.Itoa(day.Outside)},
		{Type: "metric", Title: "токенов наружу", Text: humanTokens(day.TokensOut)},
		{Type: "metric", Title: "не обычного наружу", Text: strconv.Itoa(day.Sensitive)},
		// это число обязано быть нулём, и считается оно по недоверию, а не
		// по адресу: доверенная модель бывает удалённой и законно даёт
		// «наружу», а недоверенная стоит в соседнем контейнере
		{Type: "metric", Title: "секретного недоверенной", Text: strconv.Itoa(day.SecretUntrusted),
			Level: leakLevel(day.SecretUntrusted)},
		{Type: "metric", Title: "повторов", Text: strconv.Itoa(day.Retries)},
		{Type: "metric", Title: "отказов", Text: strconv.Itoa(day.Fails)},
	}})
	// у чипа со счётчиком человек читает число, у чипа без счётчика —
	// гадает, пусто там или просто не посчитали
	классы := s.eng.SentClassesToday()
	doc.Filters = []Filter{
		{ID: "", Title: fmt.Sprintf("все · %d", day.Calls)},
		{ID: "outside", Title: fmt.Sprintf("наружу · %d", day.Outside)},
		{ID: store.ClassSensitive, Title: fmt.Sprintf("чувствительное · %d", классы[store.ClassSensitive])},
		{ID: store.ClassSecret, Title: fmt.Sprintf("секретное · %d", классы[store.ClassSecret])},
	}
	doc.ActiveFilter = filter

	class := ""
	outside := false
	switch filter {
	case "outside":
		outside = true
	case store.ClassSensitive, store.ClassSecret:
		class = filter
	}
	// экран обновляется раз в пять секунд, а полный текст каждой отправки
	// весит килобайты: берём порцию и даём «показать ещё»
	if limit <= 0 || limit == defaultRows || limit > 200 {
		limit = 25
	}
	list, err := st.SentList(outside, class, "", limit+1)
	if err != nil {
		return err
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	rows := s.sentRows(list)
	if len(rows) == 0 {
		rows = append(rows, emptyState("Сёма ещё ничего не отправлял",
			"Сюда попадает каждая просьба к модели: кто просил, какая модель, что было в контексте и полный текст. "+
				"Строки заводит сам Сёма, как только позовёт модель, — ждать нечего", "paperplane"))
	}
	add(Component{Type: "section", Title: "Отправки", Children: rows})
	if more {
		add(Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d за сутки", day.Calls)},
		}})
	}

	add(Component{Type: "text", Style: "secondary",
		Text: "Секреты вырезаны масками ещё до записи. Повтор — неудача, которую переделает очередь; отказ — работа, которую никто не подберёт. " +
			"Класс Сёма ставит по содержимому: секретное облачной модели не достаётся."})
	return nil
}

// buildSent — «Передача наружу» в настройках: правила и хранение. Лента
// отсюда выведена — вместо неё итог одной строкой и ссылка на журнал:
// настройка и хроника живут по разным законам, а вместе они читались
// как один бесконечный список.
func (s *Server) buildSent(doc *Document) error {
	st := s.eng.Store()
	doc.Heading = "Что ушло наружу"
	doc.Subheading = "что Сёма отдаёт моделям, а что вырезает из контекста"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	day := s.eng.SentToday()
	итог := Component{Type: "row", Layout: "wide", ID: "sent-итог", Level: LevelOK,
		Subtitle: "За сутки Сёма моделей не спрашивал",
		Caption:  "строка появится, как только он позовёт модель",
		Actions:  []Action{{Action: "$goto", Target: sentScreen + "~" + sentTabLog, Label: "Открыть журнал"}}}
	if day.Calls > 0 {
		итог.Subtitle = fmt.Sprintf("Наружу ушло %d отправок из %d", day.Outside, day.Calls)
		итог.Caption = "полный список — кто просил, какая модель, что было в контексте и полный текст — на своём экране"
		итог.Facts = []Prop{
			{Label: "Токенов наружу", Value: humanTokens(day.TokensOut)},
			{Label: "Не обычного наружу", Value: strconv.Itoa(day.Sensitive)},
			{Label: "Секретного недоверенной", Value: strconv.Itoa(day.SecretUntrusted)},
		}
		// это число обязано быть нулём: недоверие, а не адрес
		if day.SecretUntrusted > 0 {
			withLevel(&итог, LevelAlert)
		} else if day.Sensitive > 0 {
			withLevel(&итог, LevelWarn)
		}
	}
	add(Component{Type: "section", Title: "За сутки", Children: []Component{итог}})

	// классы и согласия: что именно не уходит наружу
	ask := "off"
	if s.eng.SensitiveMode() == "ask" {
		ask = "on"
	}
	// правило, которое работает как задумано, — это норма: ни точки, ни
	// заливной капсулы. Раньше строка «секретное не уходит» горела
	// красным, будто что-то сломалось
	classRows := []Component{
		{Type: "row", Layout: "wide", ID: "class-secret",
			Subtitle: "Секретное облачной модели не отдаётся никогда",
			Caption:  "паспорт, СНИЛС, медицина, договоры — Сёма вырезает их из контекста и говорит модели, что именно скрыл",
			Meta: fmt.Sprintf("помечено писем %d · файлов %d",
				st.CountByClass(store.ClassMsg, store.ClassSecret), st.CountByClass(store.ClassFile, store.ClassSecret))},
		askRow(ask, st),
	}
	// свои значения: ИНН, СНИЛС, счета, телефоны владельца. Приметы по
	// словам ловят «ИНН 7710140679», а голые двенадцать цифр в реплике не
	// ловит никто — так номер и уехал наружу 8 сентября
	свои := s.eng.OwnValues()
	видыСвоих := map[string]int{}
	for _, з := range свои {
		видыСвоих[з.Kind]++
	}
	чтоВСписке := "список пуст — свой номер в реплике без слова-подсказки рядом Сёма не узнает"
	if len(свои) > 0 {
		var части []string
		for _, kind := range []string{"ИНН", "СНИЛС", "карта", "счёт", "телефон", "номер"} {
			if n := видыСвоих[kind]; n > 0 {
				части = append(части, fmt.Sprintf("%s · %d", kind, n))
			}
		}
		чтоВСписке = strings.Join(части, " · ")
	}
	classRows = append(classRows, Component{Type: "row", Layout: "wide", ID: "свои-значения",
		Subtitle: "Свои значения",
		Caption: "номера документов, счетов и телефонов владельца. Хранятся солёными хэшами — самих номеров в базе нет, " +
			"и на экран они не возвращаются. Текст с таким номером недоверенной модели не уходит",
		Meta: чтоВСписке,
		Actions: []Action{
			{Action: "add_own_value", Label: "Добавить номер",
				Prompt: "Номер целиком: ИНН, СНИЛС, карта, счёт или телефон. Сёма запомнит только хэш"},
		}})
	for _, з := range свои {
		classRows = append(classRows, Component{Type: "row", Layout: "wide",
			Subtitle: з.Kind, Meta: whenText(з.At),
			Actions: []Action{{Action: "drop_own_value", Target: з.Hash, Label: "Убрать",
				Confirm: "Убрать это значение из списка?", ConfirmOK: "Убрать"}}})
	}
	if consents, err := st.Consents(); err == nil {
		for _, c := range consents {
			row := Component{Type: "row", Layout: "wide", Subtitle: c.By, Meta: whenText(c.At)}
			if c.Class != "разрешено" {
				// отказ — исключение из общего «разрешено», его и видно
				row.Badges = []Badge{{Text: c.Class, Level: LevelWarn}}
			}
			classRows = append(classRows, row)
		}
	}
	add(Component{Type: "section", Title: "Что не уходит наружу", Children: classRows})

	full := "off"
	if s.eng.SentKeepFull() {
		full = "on"
	}
	// подпись говорит о том, в каком состоянии тумблер стоит сейчас, а не
	// о том, что случится при нажатии: раньше выключенный тумблер был
	// подписан «включено — …», и человек читал состояние наоборот
	keepWhat := "сейчас Сёма хранит текст запроса и ответ целиком"
	if full == "off" {
		keepWhat = "сейчас остаётся только выжимка: открыть отправку и прочитать, что ушло, не выйдет"
	}
	add(Component{Type: "section", Title: "Хранение", Children: []Component{{
		Type: "row", Layout: "wide", ID: "sent-keep", Subtitle: "Хранить полный текст отправленного", Toggle: full,
		Caption: keepWhat, Meta: fmt.Sprintf("срок хранения %d дней", s.eng.SentKeepDays()),
		Actions: []Action{
			{Action: "toggle_sent_full", Label: "toggle"},
			{Action: "set_sent_keep", Label: "Срок хранения", Prompt: "Сколько дней хранить журнал", Value: strconv.Itoa(s.eng.SentKeepDays())},
		}}}})
	return nil
}

// sentRows — список отправок: подряд идущие обычные отправки одного
// потребителя к одной модели сворачиваются в группу со счётчиком. Очередь
// памяти пишет их десятками, и за ними не видно единственной строки с
// «секретным». Всё, что не обычное — чужой класс, отказ, повтор, — в кучу
// не попадает никогда: исключение должно остаться на виду.
func (s *Server) sentRows(list []*store.SentRow) []Component {
	обычная := func(r *store.SentRow) bool { return r.OK && r.Class == store.ClassPlain && !r.Outside }
	var rows []Component
	for i := 0; i < len(list); {
		j := i + 1
		for обычная(list[i]) && j < len(list) && обычная(list[j]) &&
			list[j].Consumer == list[i].Consumer && list[j].Model == list[i].Model {
			j++
		}
		var кучка []Component
		for _, r := range list[i:j] {
			кучка = append(кучка, sentRow(r))
		}
		if len(кучка) < sentCollapseFrom {
			rows = append(rows, кучка...)
			i = j
			continue
		}
		// список идёт от свежих к старым: первая строка кучки — последняя
		// по времени
		rows = append(rows, Component{Type: "rowgroup", ID: fmt.Sprintf("sent-гр-%d", list[i].ID),
			Subtitle: consumerHuman(list[i].Consumer),
			Caption:  fmt.Sprintf("одна и та же работа · %s · %s — %s", list[i].Model, whenText(list[j-1].At), whenText(list[i].At)),
			Children: кучка})
		i = j
	}
	return rows
}

// sentCollapseFrom — со скольких одинаковых подряд строка становится кучей.
const sentCollapseFrom = 3

// sentRow — одна отправка: кто просил, строкой меты то, что Сёма
// померил, и капсула только у исключения. Раньше у каждой из двухсот
// строк стояла капсула «своя сеть» и зелёная точка — то есть ничего.
func sentRow(r *store.SentRow) Component {
	var badges []Badge
	level := ""
	if r.Outside {
		badges = append(badges, Badge{Text: "наружу", Level: LevelInfo})
	}
	switch r.Class {
	case store.ClassSecret:
		badges = append(badges, Badge{Text: "секретное", Level: LevelAlert})
		level = LevelAlert
	case store.ClassSensitive:
		badges = append(badges, Badge{Text: "чувствительное", Level: LevelWarn})
		level = LevelWarn
	}
	switch {
	case !r.OK && r.Retry:
		badges = append(badges, Badge{Text: "повтор", Level: LevelInfo})
	case !r.OK:
		badges = append(badges, Badge{Text: "отказ", Level: LevelAlert})
		level = LevelAlert
	}
	row := Component{Type: "row", Layout: "wide", ID: fmt.Sprintf("sent-%d", r.ID),
		Subtitle: consumerHuman(r.Consumer), Meta: sentMeta(r), Badges: badges}
	if !r.OK && r.Err != "" {
		row.Caption = r.Err
	}
	if level != "" {
		withLevel(&row, level)
	}
	// полный текст — на своём экране: в списке он весил сотни килобайт на
	// каждое обновление, а читают его редко
	row.Actions = []Action{{Action: "$goto",
		Target: fmt.Sprintf("%s~%d", sentScreen, r.ID), Label: "Показать текст"}}
	return row
}

// sentMeta — одна строка того, что Сёма померил: время, модель, токены,
// сколько заняло и что лежало в контексте. Три пары фактов под каждой
// строкой занимали три экрана на две сотни отправок.
func sentMeta(r *store.SentRow) string {
	части := []string{whenText(r.At), r.Model,
		humanTokens(r.TokensIn) + " → " + humanTokens(r.TokensOut) + " токенов",
		sentSeconds(r.Seconds)}
	if r.Sources != "" {
		части = append(части, "контекст: "+r.Sources)
	}
	return strings.Join(части, " · ")
}

// sentSeconds — «2,6 с» и «1 мин 04 с»: у быстрых вызовов важны десятые,
// у долгих — минуты. Запятая, потому что по-русски дробь пишется так.
func sentSeconds(sec float64) string {
	if sec >= 60 {
		return fmt.Sprintf("%d мин %02d с", int(sec)/60, int(sec)%60)
	}
	return strings.Replace(fmt.Sprintf("%.1f с", sec), ".", ",", 1)
}

// leakLevel — секретное у недоверенной модели это не «повышенное
// внимание», а случившаяся утечка: числу нужен красный, а не жёлтый.
func leakLevel(n int) string {
	if n > 0 {
		return LevelAlert
	}
	return ""
}

// sentBlock — сводка «что ушло» для «Здоровья».
func (s *Server) sentBlock() *Component {
	day := s.eng.SentToday()
	if day.Calls == 0 {
		return nil
	}
	// норма молчит: точка загорается, только когда наружу ушло не обычное
	row := Component{Type: "row", Layout: "wide",
		Subtitle: fmt.Sprintf("За сутки наружу %d из %d отправок", day.Outside, day.Calls),
		Meta:     fmt.Sprintf("не обычного %d · повторов %d · отказов %d", day.Sensitive, day.Retries, day.Fails),
		Actions:  []Action{{Action: "$goto", Target: sentScreen + "~" + sentTabLog, Label: "Открыть"}}}
	if day.Sensitive > 0 {
		withLevel(&row, LevelWarn)
	}
	if day.SecretUntrusted > 0 {
		row.Meta = fmt.Sprintf("секретного недоверенной модели %d · %s", day.SecretUntrusted, row.Meta)
		withLevel(&row, LevelAlert)
	}
	return &Component{Type: "section", Title: "Что ушло наружу", Children: []Component{row}}
}

func (s *Server) sentActions(req actionRequest) (string, error) {
	switch req.Action {
	case "toggle_sent_full":
		return sentScreen, s.eng.SetSentKeepFull(!s.eng.SentKeepFull())
	case "toggle_sensitive_ask":
		mode := "ask"
		if s.eng.SensitiveMode() == "ask" {
			mode = "allow"
		}
		return sentScreen, s.eng.SetSensitiveMode(mode)
	case "mark_class":
		kind, rest := splitTarget(req.Target)
		id, class := splitTarget(rest)
		// класс может прийти списком в шапке: там значение в параметре,
		// а в цели только вид и номер
		if v := strings.TrimSpace(req.Params["value"]); v != "" {
			class = v
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return "", fmt.Errorf("нужен номер записи")
		}
		return "", s.eng.MarkClass(kind, n, class, "человек")
	case "add_own_value":
		вид, err := s.eng.AddOwnValue(req.Params["value"])
		if err != nil {
			return "", err
		}
		_ = вид
		return sentScreen, nil
	case "drop_own_value":
		return sentScreen, s.eng.RemoveOwnValue(req.Target)
	case "set_sent_keep":
		n, err := strconv.Atoi(strings.TrimSpace(req.Params["value"]))
		if err != nil || n < 1 {
			return "", fmt.Errorf("нужно число дней от 1")
		}
		return sentScreen, s.eng.Store().SetSetting("audit.keep_days", strconv.Itoa(n))
	}
	return "", errUnhandled
}

// buildSentOne — одна отправка целиком: что ушло модели и что она
// ответила. В общем списке этого текста нет вовсе.
func (s *Server) buildSentOne(doc *Document, id int64) error {
	r, ok := s.eng.Store().Sent(id)
	if !ok {
		return fmt.Errorf("отправки %d нет", id)
	}
	doc.Heading = consumerHuman(r.Consumer)
	doc.Subheading = "одна отправка целиком: что ушло модели и что она ответила"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	// обратный путь стоит над заголовком, а не кнопкой в ленте: заголовок
	// у всех отправок разный, и без пути непонятно, откуда сюда пришли
	add(Component{Type: "crumb", Title: "Отправка",
		Actions: []Action{{Action: "$goto", Target: sentScreen + "~" + sentTabLog, Label: "Журнал отправок"}}})
	add(Component{Type: "figs", ID: "sent-one-figs", Children: []Component{
		{Type: "metric", Title: "токенов туда", Text: humanTokens(r.TokensIn)},
		{Type: "metric", Title: "токенов обратно", Text: humanTokens(r.TokensOut)},
		{Type: "metric", Title: "заняло", Text: sentSeconds(r.Seconds)},
	}})

	where := "своя сеть"
	if r.Outside {
		where = "наружу"
	}
	props := []Prop{
		{Label: "Когда", Value: whenText(r.At)},
		{Label: "Модель", Value: r.Model},
		{Label: "Куда", Value: where},
		{Label: "Класс", Value: map[string]string{
			store.ClassPlain:     "обычное",
			store.ClassSensitive: "чувствительное",
			store.ClassSecret:    "секретное",
		}[r.Class]},
	}
	if r.Sources != "" {
		props = append(props, Prop{Label: "Было в контексте", Value: r.Sources})
	}
	if r.Err != "" {
		props = append(props, Prop{Label: "Ошибка", Value: r.Err, Color: "#C4271C"})
	}
	// факты видны сразу: раскрытие «Подробнее» с четырьмя парами внутри
	// пряталo то, ради чего экран и открывают
	head := Component{Type: "row", Layout: "wide", Subtitle: "Отправка", Facts: props}
	switch {
	case r.Class == store.ClassSecret || !r.OK:
		withLevel(&head, LevelAlert)
	case r.Class == store.ClassSensitive:
		withLevel(&head, LevelWarn)
	}
	add(head)

	text := r.Prompt
	if text == "" {
		text = r.Preview + "\n\nПолного текста нет: хранить его выключено в «Что ушло наружу» → «Хранение»."
	}
	add(Component{Type: "section", Title: "Что ушло модели", Children: []Component{
		{Type: "row", Layout: "wide", Detail: text, Expanded: true, Expandable: true, Subtitle: "Текст запроса"},
	}})
	if r.Answer != "" {
		add(Component{Type: "section", Title: "Что ответила", Children: []Component{
			{Type: "row", Layout: "wide", Detail: r.Answer, Expanded: true, Expandable: true, Subtitle: "Ответ"},
		}})
	}
	return nil
}

// askRow — тумблер «спрашивать про чувствительное». Подпись зависит от
// значения: иначе она врёт ровно в половине случаев.
func askRow(ask string, st *store.Store) Component {
	what := "сейчас чувствительное уходит облачной модели как есть"
	if ask == "on" {
		what = "сейчас такие письма вырезаются, пока вы не разрешите отправителя"
	}
	return Component{Type: "row", Layout: "wide", ID: "class-ask", Toggle: ask,
		Subtitle: "Спрашивать про чувствительное",
		Caption:  "счета, суммы, адреса — " + what,
		Meta: fmt.Sprintf("помечено писем %d · файлов %d",
			st.CountByClass(store.ClassMsg, store.ClassSensitive),
			st.CountByClass(store.ClassFile, store.ClassSensitive)),
		Actions: []Action{{Action: "toggle_sensitive_ask", Label: "toggle"}}}
}

// consumerHuman — кто просил, человеческим языком: «Разбор письма», а не
// «core:poll». В заголовке строки раньше стояло начало промпта, одинаковое
// у всех отправок подряд.
func consumerHuman(consumer string) string {
	kind, name, _ := strings.Cut(consumer, ":")
	switch kind {
	case "core":
		return "Ядро · " + name
	case "module":
		return "Модуль «" + name + "»"
	case "chat":
		return "Чат " + name
	case "task":
		return "Задача " + name
	case "script":
		return "Скрипт " + name
	}
	return consumer
}
