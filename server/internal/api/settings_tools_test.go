package api

// «Инструменты» (2 октября 2026): один экран без внутренних вкладок, состояние каждого
// инструмента названо, «Коды на картинках» — свёрнутый раздел, пределы — «Тонкая настройка»,
// у служебных моделей одна строка-ссылка.

import (
	"strings"
	"testing"
)

func TestToolsOneScreenWithHonestStates(t *testing.T) {
	s, _ := stnFix(t)
	for _, screen := range []string{toolsScreen, toolsScreen + "~" + toolsTabCodes} {
		doc := mustDoc(t, s, screen)
		if len(doc.Subtabs) != 0 || doc.ActiveSubtab != "" {
			t.Errorf("%s: у «Инструментов» внутренних вкладок нет, а есть %+v", screen, doc.Subtabs)
		}
		list := findRow(doc, "tools-list")
		if list == nil || len(list.Children) < 7 {
			t.Fatalf("%s: список инструментов: %+v", screen, list)
		}
		for _, row := range list.Children {
			if row.Type != "row" || row.Level == "" {
				continue // строка-ссылка на модели и тумблер скриптов состояния не носят
			}
			// у каждого инструмента состояние названо: зелёная точка у работающего, жёлтая и
			// капсула у того, чего нет
			if row.Level == LevelWarn && len(row.Badges) == 0 {
				t.Errorf("%s: у «%s» тревожная точка без слов о том, что не так", screen, row.Subtitle)
			}
			if row.Level == LevelOK && len(row.Badges) != 0 {
				t.Errorf("%s: у работающего «%s» капсула, а норма молчит словами", screen, row.Subtitle)
			}
		}
	}
	doc := mustDoc(t, s, toolsScreen)
	// служебные модели не дублируются: одна строка-ссылка на «Модели и роли»
	link := findRow(doc, "tool-models")
	if link == nil || len(link.Actions) != 1 || link.Actions[0].Action != "$goto" || link.Actions[0].Target != modelsScreen {
		t.Errorf("строка-ссылка на служебные модели: %+v", link)
	}
	for _, id := range []string{"tool-chat-scripts"} {
		if findRow(doc, id) == nil {
			t.Errorf("пропала строка %s", id)
		}
	}
	// файлы без распознанного текста ждут инструмента: у строки главная кнопка «Перечитать»
	var wait *Component
	for i, row := range findRow(doc, "tools-list").Children {
		if row.Subtitle == "Ждут распознавания" {
			wait = &findRow(doc, "tools-list").Children[i]
		}
	}
	if wait != nil && (len(wait.Actions) != 1 || wait.Actions[0].Action != "recheck_tools" || wait.Actions[0].Style != "primary") {
		t.Errorf("«Ждут распознавания»: %+v", wait)
	}
	// «Коды на картинках» — свёрнутый раздел, а пределы — в «Тонкой настройке»
	codes := findSectionByID(doc.Components, "codes")
	if codes == nil || !codes.Collapsed || !strings.HasPrefix(codes.Title, "Коды на картинках") {
		t.Errorf("раздел про коды: %+v", codes)
	}
	if fold := rareFoldOf(t, s, toolsScreen); !strings.HasPrefix(fold.Caption, "2 параметра") {
		t.Errorf("«Тонкая настройка» «Инструментов»: %q", fold.Caption)
	}
	// «Шифрование» ушло к «Ключам и секретам»: прежний адрес по-прежнему открывает его там
	if got := canonToolsAddr("settings:tools~" + toolsTabCrypt); got != entityAddr(secretsScreen, "", secretsTabCrypt) {
		t.Errorf("прежний адрес «Шифрования» ведёт на %q", got)
	}
}
