package api

// Экран «Журнал настроек»: кто, когда, какой параметр, старое и новое
// значение.
//
// Раньше на «Главном» стоял блок «Последняя активность», собранный из
// чужих журналов — доступа, секретов, решений по скиллам, — и под ним
// было написано, что истории правок здесь нет и обещать её нельзя. Потом
// история появилась (`store/settings_log.go`), но журнал показывал всё подряд:
// шесть тысяч курсоров `chat.read.*` стояли в одной ленте с правкой, которую
// человек сделал вчера.
//
// Теперь по умолчанию — «Ваши правки»: подписанное человеком и не служебное.
// Служебные отметки Сёмы (курсоры, «прочитано до», последние ошибки; список
// приставок — настройка `settings_log.marks` в «Тонкой настройке» журнала)
// **по-прежнему пишутся и хранятся ровно как раньше**, а чип «Служебные» их
// показывает, «Все» — журнал целиком. Решение владельца, стоит ли перестать их
// писать вовсе, не получено, поэтому выбран вариант без потери данных.
//
// Параметры называются по-человечески («Серверы → srv-vpn → Окно») — по индексу
// настроек (`settings_names.go`); ключ стоит рядом мелко, и запись ведёт к самой
// настройке.
//
// Значения секретов сюда не доезжают: их не пишет сама запись, и экран
// говорит об этом строкой, а не прочерком — «пусто» и «не показываем»
// человек обязан различать.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const changesScreen = "settings:changes"

// editsPerScreen — журнал читают глазами, а не выгружают: двадцать строк первой
// порцией, остальное — «Показать ещё» (замер 12 сентября 2026: сто правок — это
// 1 505 узлов на первом же показе).
const editsPerScreen = 20

// changesFilter — отбор журнала словами хранилища; прежние «люди» и «сам»
// (закладки, ссылки) читаются как «ваши» и «служебные».
func changesFilter(filter string) string {
	switch filter {
	case "люди":
		return store.SettingsLogMine
	case "сам":
		return store.SettingsLogService
	}
	return filter
}

func (s *Server) buildSettingsChanges(doc *Document, filter string, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Журнал настроек"
	doc.Subheading = "что вы меняли, когда и что в параметре стало; служебные отметки Сёмы — отдельным отбором"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	filter = changesFilter(filter)
	mine, service, total := st.CountSettingsLogView()
	add(Component{Type: "figs", ID: "changes-figs", Children: []Component{
		{Type: "metric", Title: "ваших правок", Text: strconv.Itoa(mine)},
		{Type: "metric", Title: "служебных отметок Сёмы", Text: strconv.Itoa(service)},
		{Type: "metric", Title: "срок хранения", Text: strconv.Itoa(s.eng.SettingsLogKeepDays()), Unit: "дней"},
	}})
	doc.Filters = []Filter{
		{ID: store.SettingsLogMine, Title: fmt.Sprintf("Ваши правки · %d", mine)},
		{ID: store.SettingsLogService, Title: fmt.Sprintf("Служебные · %d", service)},
		{ID: store.SettingsLogAll, Title: fmt.Sprintf("Все · %d", total)},
	}
	doc.ActiveFilter = filter

	if limit <= 0 || limit == defaultRows {
		limit = editsPerScreen
	}
	список, err := st.SettingsLogView(filter, limit+1)
	if err != nil {
		return err
	}
	ещё := len(список) > limit
	if ещё {
		список = список[:limit]
	}
	rows := make([]Component, 0, len(список))
	// индекс, который сам собирает этот экран, о себе не спрашивается: замок
	// индекса не вложенный, и имена здесь не нужны — записей журнала индексу не
	// надо (doc.offline)
	if len(список) > 0 && doc.offline {
		doc.partial = true
	}
	if len(список) > 0 && !doc.offline {
		idx := s.settingsIndex()
		for _, п := range список {
			rows = append(rows, s.namedEditRow(idx, п, false))
		}
	}
	if len(rows) == 0 {
		rows = append(rows, changesEmpty(filter, service))
	}
	add(Component{Type: "section", Title: changesTitle(filter), Children: rows})
	if ещё {
		add(Component{Type: "button", Actions: []Action{{Action: "$more", Label: "Показать ещё"}}})
	}

	// срок хранения и список служебных отметок — «Тонкая настройка» экрана
	// (settings_rare.go): их выбирают один раз, а не правят
	add(Component{Type: "form", ID: "changes-keep", Title: "Хранение", Tier: tierRare, Style: formRows,
		Fields: []Field{s.settingField(engine.SettingsLogKeepKey, "value", "Сколько хранить правки",
			"Старые записи убираются раз в час; у одного параметра остаются последние 200 правок, "+
				"чтобы курсоры папок и прочие метки Сёмы не вытеснили из журнала настоящие изменения", "дн.")},
		Submit: &Action{Action: "set_settings_log_keep", Label: "Сохранить"}})
	add(Component{Type: "form", ID: "changes-marks", Title: "Служебные отметки", Tier: tierRare, Style: formRows,
		Fields: []Field{s.settingField(store.SettingsLogMarks.Key, "marks", "Что считать служебной отметкой",
			"Через запятую: «chat.read.» — всё, что начинается так; «*.last_err» — всё, что так кончается. "+
				"Такие записи не считаются вашими правками и по умолчанию спрятаны, но пишутся и хранятся как раньше", "")},
		Submit: &Action{Action: "set_settings_log_marks", Label: "Сохранить"}})

	add(Component{Type: "text", Style: "secondary",
		Text: "Значения секретов в журнал не пишутся никогда: закрытая настройка, зашифрованное значение и параметр, " +
			"чьё имя говорит о ключе или пароле, попадают сюда без значения. Ключ провайдера внутри списка моделей " +
			"вырезают те же маски, которыми закрыт журнал сервера."})
	return nil
}

// changesTitle — заголовок списка по отбору.
func changesTitle(filter string) string {
	switch filter {
	case store.SettingsLogService:
		return "Служебные отметки Сёмы"
	case store.SettingsLogAll:
		return "Все записи"
	}
	return "Ваши правки"
}

// changesEmpty — пустой журнал объясняет себя: чего тут нет и где остальное.
func changesEmpty(filter string, service int) Component {
	if filter == store.SettingsLogMine && service > 0 {
		return emptyState("Ваших правок пока нет",
			"Сюда попадает всё, что вы поменяли в настройках: параметр, было и стало, время. Служебных отметок Сёмы "+
				strconv.Itoa(service)+" — они под отбором «Служебные»", "slider.horizontal.3")
	}
	return emptyState("Настройки пока не менялись",
		"Сюда попадает каждая правка настройки: параметр, старое значение, новое, время и кто. "+
			"Записи заводятся сами, как только что-то изменится", "slider.horizontal.3")
}

// editValue — «не было» и «не показываем» это разные ответы, и
// прочерк на оба одинаков: человек решит, что параметр был пуст.
func editValue(v string, скрыто bool) string {
	if скрыто {
		return "значение секрета не пишется"
	}
	if strings.TrimSpace(v) == "" {
		return "пусто"
	}
	return v
}

// settingsChangesActions — действия журнала настроек.
func (s *Server) settingsChangesActions(req actionRequest) (string, error) {
	switch req.Action {
	case "set_settings_log_keep":
		n, err := strconv.Atoi(strings.TrimSpace(req.Params["value"]))
		if err != nil || n < 1 {
			return "", fmt.Errorf("нужно число дней от 1")
		}
		return changesScreen, s.eng.SetSettingsLogKeepDays(n)
	case "set_settings_log_marks":
		return changesScreen, s.saveSettings(req,
			settingParam{Param: "marks", Key: store.SettingsLogMarks.Key, What: "служебные отметки"})
	}
	return "", errUnhandled
}
