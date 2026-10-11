package api

// Вкладка «Роли»: кто отвечает, когда Сёме нужна модель, и служебные назначения.
//
// Десять ролей по три списка каждая — это таблица, а не десять карточек: подпись
// «Основная» печаталась десять раз, и на телефоне три списка в строке
// разваливались. Теперь роли — строки одной таблицы, список меняется в ячейке на
// месте (то же действие, что и раньше: `set_role_model`, `set_role_spare`,
// `set_role_timeout`), а на телефоне строка раскладывается карточкой с
// подписями колонок.
//
// Под таблицей — служебные назначения: их выбирает не роль, а сама работа
// (эмбеддер, модель для картинок, OCR, «картинка без текста», рисование).
// Раньше они стояли под списком из ста семидесяти моделей, а замечание про
// пропавший эмбеддер приходило на «Модели»: человек искал строку не там, где
// ему сказали. Параметры распознавания речи — редкое — лежат в «Тонкой
// настройке» вкладки; сама роль речи — строка таблицы, отдельного раздела под
// ней больше нет.

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// roleTimeouts — из чего выбирают, сколько секунд ждать ответа роли.
var roleTimeouts = []int{15, 30, 60, 120, 180, 300}

// timeoutOptions — варианты ожидания «120 с»; нынешнее значение, которого нет
// среди готовых, остаётся в списке: выпадашка без своего значения молча
// подменила бы его первым попавшимся.
func timeoutOptions(current int) []string {
	list := append([]int(nil), roleTimeouts...)
	known := false
	for _, v := range list {
		known = known || v == current
	}
	if !known && current > 0 {
		list = append(list, current)
		sort.Ints(list)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, strconv.Itoa(v)+" с")
	}
	return out
}

// rolesTab — «Роли».
func (s *Server) rolesTab(doc *Document, add func(Component), cards []engine.ModelCard, roles []engine.Role) {
	labels, label := s.modelLabels(cards)
	// наборы вариантов отдаются один раз на документ, а не повторяются в каждой
	// строке: девять ролей × два списка × полторы сотни моделей — это тысячи
	// одинаковых строчек вариантов, больше трети всех узлов экрана
	spare := append([]string{"нет"}, labels...)
	hearing := []string{"нет"}
	cardOf := make(map[string]engine.ModelCard, len(cards))
	for _, c := range cards {
		cardOf[c.Name] = c
	}
	// роль речи берёт только модели со слухом: остальным на ней делать нечего
	for _, name := range s.eng.ProviderNames() {
		if c, ok := cardOf[name]; ok && c.Hears {
			hearing = append(hearing, label(name))
		}
	}
	doc.OptionSets = map[string][]string{"модели": labels, "запасные": spare, "слышащие": hearing}

	var rows, fails []Component
	for _, r := range peerRoles(roles) {
		rows = append(rows, s.roleRow(r, label, len(hearing) == 1))
		if r.LastErr != "" {
			fails = append(fails, Component{Type: "row", Layout: "wide",
				Subtitle: r.Name + " · " + r.Model + " не ответил",
				Caption:  r.LastErr, Meta: "ответила запасная " + r.Spare})
		}
	}
	add(Component{Type: "table", ID: "roles-table",
		Title:    "Кто отвечает, когда Сёме нужна модель · основная · запасная · сколько секунд ждём",
		Columns:  []string{"Роль", "Основная", "Запасная", "Ждём", "Последний ответ"},
		Children: rows})
	add(courtNote(s))
	if n := searchingCount(cards); n > 0 {
		add(Component{Type: "text", Style: "secondary",
			Text: plural(n, "модель ищет", "модели ищут", "моделей ищут") + " в интернете — их отбирает фильтр «Ищут» " +
				"на вкладке «Модели»; на роль «Поиск» ставят именно их."})
	}
	if len(fails) > 0 {
		add(Component{Type: "section", ID: "role-fails", Title: "Последние отказы · показываем нарочно", Children: fails})
	}

	s.serviceRoles(add, label, doc.offline)
	if doc.offline {
		doc.partial = true // эмбеддер и распознавание — запомненным, а не спрошенным
	}
	add(Component{Type: "section", ID: "models-how", Title: "Как это работает", Collapsed: true, Children: []Component{
		{Type: "text", Style: "secondary",
			Text: "Доверие — ваше утверждение, а не адрес: модель на своей машине тоже нужно объявить доверенной. " +
				"Секретное не уходит наружу никогда, что бы вы ни включили; чувствительное — по правилу на экране «Что ушло наружу». " +
				"Подключённая модель, которую никто не спрашивает, денег не стоит — выключать её не нужно. " +
				"Не ответила основная — работу доделывает запасная, и в чате видно, кто ответил; три отказа подряд — " +
				"роль уходит на полчаса отдохнуть, чтобы одна медленная модель не красила «Здоровье» в сплошные отказы."}}})
	s.speechFine(add)
}

// lastCheckLine — итог последнего действия с моделью или провайдером («X:
// ответила за 1.2 с»), если он про этот предмет; пусто — про другой или не было.
// Он живёт в памяти процесса и показывается там, где человек нажал кнопку.
func (s *Server) lastCheckLine(subject string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastCheck == "" || !strings.HasPrefix(s.lastCheck, subject+":") {
		return ""
	}
	return s.lastCheck
}

// searchingCount — сколько моделей помечено как умеющие искать в сети.
func searchingCount(cards []engine.ModelCard) int {
	n := 0
	for _, c := range cards {
		if c.Search {
			n++
		}
	}
	return n
}

// roleRow — строка таблицы ролей: название с короткой подписью, три списка и
// чем кончился последний вызов. Роль без модели и роль на передышке видны
// полоской слева и словом, а не только цветом.
// roleRowID — ID строки роли в таблице ролей.
func roleRowID(role string) string { return "role-" + role }

// roleHighlight — адрес вкладки «Роли» с подсветкой строки роли: замечание «роли
// не назначена модель» кнопкой открывает саму строку, а не вкладку целиком.
func roleHighlight(role string) string {
	return withHighlight(modelsScreen, hlRow(roleRowID(role)))
}

func (s *Server) roleRow(r engine.Role, label func(string) string, noHearing bool) Component {
	// ID строки роли — метка для подсветки: замечание «роль без модели» ведёт к её строке
	row := Component{Type: "row", ID: roleRowID(r.Name), Subtitle: r.Name, Caption: roleMeaning(r.Name)}
	cells := []Cell{
		{Select: &Select{Name: "value", Value: label(r.Model), OptionsRef: setOfRole(r.Name, "модели"),
			Action: Action{Action: "set_role_model", Target: r.Name}}},
		{Select: &Select{Name: "value", Value: label(r.Spare), OptionsRef: setOfRole(r.Name, "запасные"),
			Action: Action{Action: "set_role_spare", Target: r.Name}}},
		{Select: &Select{Name: "value", Value: strconv.Itoa(r.Timeout) + " с", Options: timeoutOptions(r.Timeout),
			Action: Action{Action: "set_role_timeout", Target: r.Name}}},
	}
	last := Cell{Text: "ещё не спрашивали"}
	if r.Last != "" {
		last.Text = r.Last
	}
	if r.Model == "" || r.Model == "нет" {
		row.Badges = []Badge{{Text: "без модели", Level: LevelWarn}}
		row.Level = LevelWarn
		last = Cell{Text: "—"}
	}
	if until, resting := s.eng.RoleRest(r.Name); resting {
		row.Level = LevelWarn
		last = Cell{Text: "отдыхает до " + until.Format("15:04"), Level: LevelWarn,
			Title: "три отказа подряд: роль отдыхает, чтобы одна медленная модель не красила «Здоровье»"}
	}
	row.Cells = append(cells, last)
	// у роли речи моделей со слухом может не быть вовсе: человеку сказано, что
	// делать, а не показан пустой список
	if r.Name == engine.RoleTranscribe && noHearing {
		row.Caption += ". Моделей со слухом пока нет: подключите OpenAI — gpt-4o-mini-transcribe появится среди моделей сам"
	}
	return row
}

// courtNote — суд двух моделей одним абзацем и с именами. «Роли арбитра и
// судьи должны различаться» ничего не объясняло: человек не знал ни кто эти
// двое, ни зачем их двое.
func courtNote(s *Server) Component {
	judge, arbiter := "", ""
	for _, r := range s.eng.Roles() {
		switch r.Name {
		case engine.RoleArbiter:
			arbiter = r.Model
		case engine.RoleJudge:
			judge = r.Model
		}
	}
	if ready, why := s.eng.CourtReady(); !ready {
		return Component{Type: "text", Style: "secondary",
			Text: why + ". Пока так, изменяющие команды на серверах идут вам на кнопку, " +
				"а самостоятельное управление не включится: одна модель, проверяющая сама себя, — это не проверка."}
	}
	return Component{Type: "text", Style: "secondary",
		Text: "Суд собран: арбитр " + arbiter + " разбирает изменяющую команду на сервере, судья " + judge +
			" по этому разбору решает — выполнить, отклонить или спросить вас. Модели разные, как и требуется; " +
			"любая заминка читается как «спросить вас»."}
}

// setOfRole — какой набор вариантов у роли: обычной — все модели, роли речи —
// только те, что слышат.
func setOfRole(role, common string) string {
	if role == engine.RoleTranscribe {
		return "слышащие"
	}
	return common
}

// roleMeaning — одна строка о роли под её названием в таблице.
func roleMeaning(name string) string {
	switch name {
	case engine.RoleTranscribe:
		return "голосовые Telegram и MAX, аудиофайлы · только модели со слухом"
	case engine.RoleFilter:
		return "спам ли письмо — на каждом новом"
	case engine.RoleVision:
		return "фото и скриншоты чеков"
	case engine.RoleChat:
		return "ответы по письмам и файлам"
	case engine.RoleEmbed:
		return "поиск и похожесть — векторы всего архива"
	case engine.RoleMemory:
		return "факты и граф из сессий, дубли, сжатие; «нет» — память только пишет и ищет"
	case engine.RoleModules:
		return "sema.llm у модулей: сроки, разбор писем"
	case engine.RoleSearch:
		return "ходит в интернет; «нет» — Сёма про поиск не заикается"
	case engine.RoleTranslate:
		return "письма не на русском; «нет» — показываются как есть"
	case engine.RoleArbiter:
		return "разбирает команду на сервере; не судья"
	case engine.RoleJudge:
		return "вердикт по разбору арбитра; не арбитр"
	}
	return ""
}

// ---- служебные роли ----

// serviceRoles — те, что выбирает не роль, а сама работа: эмбеддер, чем читать
// картинку без модели и что делать, когда в картинке не нашлось ни слова. На
// «Инструментах» осталось то, что моделью не является: pdftotext, коды, офисные
// файлы, песочница.
func (s *Server) serviceRoles(add func(Component), label func(string) string, offline bool) {
	// список моделей эмбеддера спрашивается у его сервера по сети (запоминается на
	// минуту), а чем читать картинки без модели — выясняется, собирая на первом
	// обращении помощника распознавания. Индекс настроек (offline) не делает ни
	// того, ни другого: берёт запомненное и выбранное
	embedModels, ocrValue, ocrOptions := s.eng.EmbedModels, s.eng.OCREngine, s.eng.OCREngines
	if offline {
		embedModels = s.eng.EmbedModelsCached
		ocrValue = s.eng.OCREngineSaved
		ocrOptions = func() []string { return []string{s.eng.OCREngineSaved()} }
	}
	rows := []Component{
		toolRow("Эмбеддинги", "Поиск по письмам, похожесть для авторешений и память",
			&Select{Name: "model", Value: s.eng.EmbedModel(), Options: embedModels(),
				Action: Action{Action: "save_embed_model"}}),
		embedTrustRow(s),
		// «Модель-фильтр» (так строка звалась) письма не размечает: размечает роль
		// «Фильтр писем». Но выбранная здесь модель по-прежнему стоит там, где
		// роли своей модели ещё не назначено, — поэтому строка осталась, а подпись
		// говорит, что она на самом деле решает
		toolRow("Модель по умолчанию", "На неё смотрят роли, пока им модель не назначена, и разговор по почте без роли",
			&Select{Name: "provider", Value: label(s.eng.ActiveProvider()), OptionsRef: "модели",
				Action: Action{Action: "save_settings"}}),
		toolRow("Модель для картинок", "Какой моделью читать картинку: то же, что роль «Чтение картинок» в таблице",
			&Select{Name: "model", Value: label(s.eng.VisionModel()), OptionsRef: "модели",
				Action: Action{Action: "save_vision_model"}}),
		toolRow("Чтение картинок без модели", "Запасной путь, когда модель картинки не умеет",
			&Select{Name: "engine", Value: ocrValue(), Options: ocrOptions(),
				Action: Action{Action: "save_ocr_engine"}, key: engine.OCREngineKey}),
		toolRow("Картинка без распознанного текста", "Наш OCR не нашёл ни слова — значит о содержимом ничего не известно",
			&Select{Name: "mode", Value: noTextLabel(s.eng.NoTextMode()), Options: noTextOptions(),
				Action: Action{Action: "save_no_text"}}),
		s.drawingRow(),
	}
	add(Component{Type: "section", ID: "service-roles",
		Title: "Служебные роли · их выбирает не роль, а сама работа", Children: rows})
	if problem := s.eng.VisionState(); problem != "" {
		add(Component{Type: "text", Style: "secondary", Text: "Модель картинки не приняла: " + problem})
	}
}

// drawingRow — чем Сёма рисует картинки к постам. Модель не выбирают руками:
// берётся самая дешёвая по ценам fal.ai, и здесь видно, какая и почём. Ключ —
// общим путём, через «Подключить провайдера».
func (s *Server) drawingRow() Component {
	row := Component{Type: "row", ID: "image-model", Layout: "wide", Subtitle: "Рисование картинок"}
	if !s.eng.FalReady() {
		row.Caption = "картинки к постам рисует fal.ai — подключите провайдера «fal.ai»"
		row.Actions = []Action{{Action: "$goto", Target: entityAddr(modelsScreen, modelsNew, ""), Label: "Подключить fal.ai", Style: "primary"}}
		return row
	}
	model, verified, pinned := s.eng.RememberedImageModel()
	switch {
	case pinned:
		row.Caption = "fal.ai · " + model.Endpoint + " · закреплена вручную (images.model)"
	case model.Endpoint == "":
		row.Caption = "fal.ai · модель выберется сама — самая дешёвая при первой картинке"
	default:
		mode := "самая дешёвая"
		if model.Mode == engine.ModeBetter {
			mode = "самая дешёвая из сильных моделей"
		}
		row.Caption = "fal.ai · " + model.Endpoint + " · " + engine.ImagePrice(model) + " · " + mode
		if !verified.IsZero() {
			row.Caption += " · цены проверены " + whenText(verified.Format(time.RFC3339))
		}
	}
	if !pinned {
		row.Actions = []Action{{Action: "pick_image_model", Label: "Подобрать заново"}}
		// самая дешёвая или самая дешёвая из сильных: у самых дешёвых картинка
		// заметно слабее, и выбор — за человеком
		row.Selects = []Select{{Name: "value", Value: s.eng.ImageMode(),
			Options: []string{engine.ModeCheap, engine.ModeBetter},
			Action:  Action{Action: "save_image_mode"}, key: engine.ImageModeKey}}
	}
	return row
}
