package api

// Корень настроек на телефоне: список разделов экраном.
//
// На широком экране `settings` — это Обзор рядом с колонкой разделов. На
// телефоне колонке места нет, и корень — сама колонка, но экраном: поле
// поиска первой строкой, «Обзор» и четыре группы с разделами (макет —
// `design/nastroyki-2/Karkas.html`, экран `#root`). Состав тот же, что у
// колонки: оба списка собирает `settingsGroups`, разойтись им нечем.
// Строка богаче пункта колонки — под названием идёт то, что стоит
// в разделе сейчас (`settingsSection.Meta`), а справа счётчик замечаний.
//
// Раздел открывается целым экраном с «‹ Настройки» над заголовком
// (`settingsFrame`); «Обзор» — своим адресом `settings:overview`.

import (
	"strconv"
	"strings"
)

// buildSettingsRoot — корень настроек для узкого окна.
func (s *Server) buildSettingsRoot(doc *Document, search string, limit int) error {
	doc.Heading = "Настройки"
	doc.Layout = "wide"
	doc.Searchable = true
	doc.SearchHint = "Найти настройку"
	doc.ActiveTab = settingsRoot
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	req := strings.TrimSpace(search)
	idx := s.sectionIndex(doc)
	marks := s.sectionMarks(doc)

	// с запросом корень становится результатами поиска, и поле остаётся
	// над ними: стирание запроса возвращает список
	if req != "" {
		doc.Heading = "Нашлось «" + req + "»"
		doc.Subheading = searchSubheading
		for _, block := range s.searchBlocks(doc, req, limit) {
			add(block)
		}
		return nil
	}

	// строка корня: название, что в разделе сейчас, справа число замечаний.
	// Вся строка кликается — кнопки у неё нет. Число неизвестно («не
	// подключён») — капсула без текста, то есть одна точка уровня справа:
	// слово-капсула уезжала бы второй строкой и растила корень вдвое, а
	// точка слева сдвигала бы название
	row := func(addr, caption string) Component {
		c := Component{Type: "row", Layout: "wide", ID: "корень-" + addr,
			Subtitle: sectionTitle[addr], Caption: caption,
			Actions: []Action{{Action: "$goto", Target: addr, Label: sectionTitle[addr]}}}
		if m, ok := marks[addr]; ok {
			text := ""
			if m.Count > 0 {
				text = strconv.Itoa(m.Count)
			}
			c.Badges = []Badge{{Text: text, Level: m.Level}}
		}
		return c
	}

	// «Обзор»: что ждёт решения, теми же словами, что на самом Обзоре
	var waiting []string
	for _, group := range settingsGroups {
		for _, section := range group.Sections {
			if m, ok := marks[section]; ok && (m.Level == LevelWarn || m.Level == LevelAlert) {
				waiting = append(waiting, sectionTitle[section]+" — "+idx[section].Pill)
			}
		}
	}
	overview := "всё работает так, как вы оставили"
	doc.Subheading = "Выберите раздел или найдите настройку поиском"
	if n := marks[settingsRoot].Count; n > 0 {
		// два первых замечания, остальные числом: строка в две строки, а не в пять
		if len(waiting) > 2 {
			waiting = append(waiting[:2], "и ещё "+strconv.Itoa(len(waiting)-2))
		}
		overview = strings.Join(waiting, " · ")
		doc.Subheading = plural(n, "замечание ждёт", "замечания ждут", "замечаний ждут") + " вас"
	}
	overviewRow := row(settingsRoot, overview)
	// у корня «Обзор» ведёт на свой адрес: `settings` на телефоне — сам список
	overviewRow.Actions = []Action{{Action: "$goto", Target: settingsOverview, Label: "Обзор"}}
	add(Component{Type: "section", Children: []Component{overviewRow}})

	for _, group := range settingsGroups {
		var rows []Component
		for _, section := range group.Sections {
			rows = append(rows, row(section, idx[section].Meta))
		}
		add(Component{Type: "section", Title: group.Name, Children: rows})
	}
	return nil
}
