package api

import (
	"fmt"
	"strconv"
	"strings"
)

// Раздел «Инструменты»: чем читаем файлы и картинки, с честным состоянием
// каждого. Один экран без внутренних вкладок (2 октября 2026): вкладка
// «Шифрование» уехала в «Ключи и секреты», а «Коды на картинках» стали
// свёрнутым разделом того же экрана — это одна забота (чем читаем), а не две.
// Пределы кодов (время на файл, размер картинки) — «Тонкая настройка»:
// правят раз в год.
//
// Служебные модели (эмбеддер, чем читать картинку, что делать с картинкой без
// текста) здесь не дублируются: они на «Модели и роли → Роли», тут одна строка-
// ссылка.

const toolsScreen = "settings:tools"

// Хвосты прежних вкладок. Вкладок у раздела больше нет, но адреса помнят
// закладки и уведомления, и они ведут в осмысленное место.
const (
	// toolsTabCodes — прежняя вкладка «Коды на картинках»: адрес открывает тот же
	// экран с развёрнутым разделом про коды
	toolsTabCodes = "codes"
	// toolsTabCrypt — прежний адрес «Шифрования»: открывает его на новом месте
	// (canonToolsAddr)
	toolsTabCrypt = "crypt"
)

// canonToolsAddr — адрес по нынешним правилам: «Шифрование» живёт у «Ключей и
// секретов», и прежний «settings:tools~crypt» из закладок, уведомлений и чужих
// журналов открывает его там. Остальные адреса возвращаются как есть.
func canonToolsAddr(screen string) string {
	if screen == "settings:tools~"+toolsTabCrypt {
		return entityAddr(secretsScreen, "", secretsTabCrypt)
	}
	return screen
}

// buildToolsTab — «Инструменты»: один список, под ним свёрнутые «Коды на
// картинках», внизу «Тонкая настройка». Хвост `~codes` прежней вкладки
// разворачивает раздел про коды.
func (s *Server) buildToolsTab(doc *Document, tab string) error {
	doc.Heading = "Инструменты"
	doc.Layout = "wide"
	doc.Subheading = "Чем читаются PDF, картинки и офисные документы на этой машине."
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	// модели отсюда уехали на «Модели и роли → Роли → Служебные роли»: замечание про
	// пропавший эмбеддер приходило на «Модели», а строка стояла здесь —
	// человек искал её не там, где ему сказали. Тут остаётся то, что
	// моделью не является
	var rows []Component
	scripts := "off"
	if s.eng.ChatScriptsEnabled() {
		scripts = "on"
	}
	rows = append(rows, Component{
		Type: "row", Layout: "wide", ID: "tool-chat-scripts", Toggle: scripts,
		Subtitle: "Сёма пишет и запускает скрипты в чате",
		Caption: "Когда для ответа нужен расчёт по вашим данным, Сёма пишет скрипт, выполняет его в песочнице " +
			"и отвечает по результату; скрипт остаётся в «Скриптах» и виден в переписке",
		Actions: []Action{{Action: "toggle_chat_scripts", Label: "toggle"}},
	})
	for n, t := range s.toolsFor(doc) {
		caption := t.Meta
		if t.Hint != "" {
			caption += " · " + t.Hint
		}
		row := toolRow(t.Title, caption, nil)
		row.ID = fmt.Sprintf("tool-%d", n)
		// состояние у каждого честное: точка зелёная у того, что работает, и жёлтая
		// у того, чего на машине нет; капсулой — только у второго: «встроено» у
		// пяти строк подряд ничего не отличало бы
		if t.OK {
			withLevel(&row, LevelOK)
		} else {
			row.Badges = []Badge{{Text: t.State, Level: LevelWarn}}
			withLevel(&row, LevelWarn)
		}
		if t.Title == "Ждут распознавания" {
			row.Actions = []Action{{Action: "recheck_tools", Label: "Перечитать файлы", Style: "primary"}}
		}
		rows = append(rows, row)
	}
	// строка-ссылка: какая модель за что берётся — на «Моделях»
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "tool-models", Subtitle: "Служебные модели",
		Caption: "Какая модель за что берётся — эмбеддер, чтение картинок, что делать с картинкой без текста — " +
			"на «Модели и роли → Роли → Служебные роли»: там же и замечания про них",
		Actions: []Action{{Action: "$goto", Target: modelsScreen, Label: "Служебные роли"}}})
	add(Component{Type: "section", ID: "tools-list", Title: "Что умеет сама эта машина", Children: rows})

	// «Коды на картинках» — свёрнутым разделом того же экрана; хвост прежней
	// вкладки («~codes») разворачивает его
	codes := s.buildCodes()
	codes.Expanded = tab == toolsTabCodes
	add(codes)

	// пределы кодов — «Тонкая настройка» (settings_rare.go)
	add(Component{Type: "form", ID: "codes-limits", Title: "Коды на картинках: пределы", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("codes.max_s", "limit_s", "Сколько искать на одном файле",
				"Не уложились — файл принят без кодов, и это сказано в его карточке. Приём других файлов не ждёт", "с"),
			s.settingField("codes.max_mp", "max_mp", "Предел размера картинки",
				"Сжатый файл бывает маленьким, а развёрнутый — гигабайтом; такую картинку Сёма не разворачивает вовсе", "Мп"),
		},
		Submit: &Action{Action: "set_codes_limits", Label: "Сохранить"}})
	return nil
}

// buildCodes — «Коды на картинках»: какие символики искать. Предел времени на
// файл и размер картинки — «Тонкая настройка» экрана: ни то ни другое не гвоздь
// (на Raspberry Pi восемь секунд на файл при потоке из папки — это часы, а
// кому-то нужны только чеки и незачем перебирать пять читателей на каждой
// фотографии), но и правят это раз в год.
func (s *Server) buildCodes() Component {
	rows := make([]Component, 0, 8)
	enabled := 0
	for _, в := range s.eng.CodeKinds() {
		toggle := "off"
		if в.Enabled {
			toggle = "on"
			enabled++
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", Toggle: toggle,
			Subtitle: в.Title,
			Actions:  []Action{{Action: "toggle_code_kind", Target: в.Key, Label: "toggle"}}})
	}
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "codes-note", Subtitle: "Читаются на месте",
		Caption: "Без модели и без сети: в QR чека лежат дата, сумма и номера ФН, ФД и ФП " +
			"ровно такими, какими их знает налоговая, — а тот же итог, прочитанный с фотографии буквами, " +
			"модель уже перепутает. PDF417 не читается: чистой библиотеки на Go для него нет."})
	return Component{Type: "section", ID: "codes",
		Title:     fmt.Sprintf("Коды на картинках · включено %d из %d", enabled, len(rows)-1),
		Collapsed: true, Children: rows}
}

// numberFromCaption — «15 с» и «40 Мп» обратно в число.
func numberFromCaption(s string) int {
	поля := strings.Fields(s)
	if len(поля) == 0 {
		return 0
	}
	n, err := strconv.Atoi(поля[0])
	if err != nil {
		return 0
	}
	return n
}

func toolRow(title, caption string, sel *Select) Component {
	c := Component{Type: "row", Layout: "wide", Subtitle: title, Caption: caption}
	if sel != nil {
		c.Selects = []Select{*sel}
	}
	return c
}

// Картинка без распознанного текста: улик о содержимом нет, поэтому решение
// человек принимает заранее, а не по каждому файлу.
var noTextModes = []struct{ Key, Label string }{
	{"ask", "Спрашивать"},
	{"send", "Отдавать модели"},
	{"hold", "Не отдавать"},
}

func noTextOptions() []string {
	out := make([]string, 0, len(noTextModes))
	for _, m := range noTextModes {
		out = append(out, m.Label)
	}
	return out
}

func noTextLabel(key string) string {
	for _, m := range noTextModes {
		if m.Key == key {
			return m.Label
		}
	}
	return noTextModes[0].Label
}

func noTextKeyOf(label string) string {
	for _, m := range noTextModes {
		if m.Label == label {
			return m.Key
		}
	}
	return "ask"
}
