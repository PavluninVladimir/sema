package api

import (
	"strings"
	"testing"
)

// Поиск в настройках ищет по словам, которыми человек называет раздел,
// а не по его названию: он ищет «пуши» и «ключи», а разделы называются
// «Уведомления» и «Хранилище секретов».
func TestSettingsSearchByHumanWords(t *testing.T) {
	s, _ := testServer(t)
	случаи := []struct{ doRequest, ждём string }{
		{"пуши", notifySettingsScreen},
		{"ключи", secretsScreen},
		{"телеграм", tgScreen},
		{"наружу", sentScreen},
		{"пароль", accessScreen},
		// провод, опрос и пояс переехали с «Уведомлений» на «Работу Сёмы» вместе со
		// словами, которыми их ищут
		{"опрос", watchScreen},
		{"провод", watchScreen},
		{"часовой пояс", watchScreen},
		{"пинатель", watchScreen},
		{"работа сёмы", watchScreen},
	}
	for _, с := range случаи {
		doc, err := s.buildScreen("settings", "", с.doRequest, 0)
		if err != nil {
			t.Fatalf("%q: %v", с.doRequest, err)
		}
		if !containsScreen(doc, с.ждём) {
			t.Errorf("по запросу %q не нашёлся раздел %s", с.doRequest, с.ждём)
		}
	}
}

// Ничего не нашлось — экран объясняет, что здесь ищется, а не молчит
// пустотой.
func TestSettingsSearchEmptyAnswer(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings", "", "квазимодо", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Components) != 1 || doc.Components[0].Style != "empty" {
		t.Fatalf("ждали объяснение пустоты, а не %d блоков", len(doc.Components))
	}
	if !strings.Contains(doc.Components[0].Caption, "Сотрите запрос") {
		t.Errorf("пустое место должно говорить, что делать: %q", doc.Components[0].Caption)
	}
}

// Без запроса «Главное» — не оглавление: что сломано, кто подключён и
// что менялось. Оглавление играют пять вкладок сверху.
func TestMainIsNotTableOfContents(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	// 2 октября 2026 «Изменения настроек» (со служебными курсорами) заменило
	// «Вы меняли», а «Действия с доступом и секретами» с Обзора ушли: журнал
	// секретов — вкладка «Журнал» у «Ключей и секретов», ключи и токены — строки
	// «Вы меняли». Новый блок — «Под рукой»
	for _, что := range []string{"Ждут решения", "Подключения",
		"Под рукой", "Вы меняли"} {
		if !strings.Contains(dump, что) {
			t.Errorf("на «Главном» нет блока «%s»", что)
		}
	}
	// список разделов здесь больше не живёт: за ним идут во вкладки
	if strings.Contains(dump, `"id":"set-`) {
		t.Error("«Главное» снова стало каталогом разделов")
	}
	if !doc.Searchable {
		t.Error("обзор должен быть с поиском")
	}
}

// У каждого замечания есть кнопка, и она ведёт прямо к настройке, а не
// «в раздел»: человек не должен искать глазами, что именно чинить.
func TestFindingAlwaysHasAction(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	замечаний := 0
	var walk func([]Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			if strings.HasPrefix(c.ID, "беда-") {
				замечаний++
				if len(c.Actions) != 1 {
					t.Errorf("у замечания %q действий %d, а нужно ровно одно", c.Subtitle, len(c.Actions))
					continue
				}
				if c.Actions[0].Target == "" {
					t.Errorf("замечание %q никуда не ведёт", c.Subtitle)
				}
			}
			walk(c.Children)
		}
	}
	walk(doc.Components)
	// на пустой базе бед хватает: ни ящиков, ни моделей, ни каналов
	if замечаний == 0 {
		t.Error("на пустой базе не нашлось ни одного замечания — проверять нечего")
	}
}

func containsScreen(doc *Document, screen string) bool {
	var walk func([]Component) bool
	walk = func(cs []Component) bool {
		for _, c := range cs {
			if c.ID == "set-"+screen {
				return true
			}
			if walk(c.Children) {
				return true
			}
		}
		return false
	}
	return walk(doc.Components)
}
