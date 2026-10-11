package api

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Набор типов — проверяемый договор: каждый клиент объявляет, что умеет,
// а тест обходит экраны и сверяет. Тип, которого клиент не знает, роняет
// тест с именем типа и экрана, а не экран у человека.
func TestWebClientDictionaryCoversScreens(t *testing.T) {
	s, _ := testServer(t)
	comps, fields := dictionaryTypes(web.Dictionary())
	verify(t, s, "веб-клиент", comps, fields)
}

func verify(t *testing.T, s *Server, who string, comps, fields map[string]bool) {
	t.Helper()
	for _, screen := range coreScreens {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil || doc == nil {
			continue // за сборку экранов отвечает TestAllScreensBuildOnEmptyDatabase
		}
		uc, uf := usedTypes(doc)
		for name := range uc {
			if name != "" && !comps[name] {
				t.Errorf("%s не знает компонент %q с экрана %q", who, name, screen)
			}
		}
		for name := range uf {
			if name != "" && !fields[name] {
				t.Errorf("%s не знает поле %q с экрана %q", who, name, screen)
			}
		}
	}
}

func TestDictionaryHandshake(t *testing.T) {
	cases := []struct {
		header string
		ok     bool
	}{
		{"", true}, // приложение до появления версии — совместимо, пока uiDictMin == 1
		{strconv.Itoa(UIDictVersion), true},
		{"0", false},
		{strconv.Itoa(UIDictVersion + 1), false},
		{"мусор", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/ui?screen=inbox", nil)
		if c.header != "" {
			r.Header.Set(uiDictHeader, c.header)
		}
		doc := outdatedClient(r)
		if c.ok && doc != nil {
			t.Errorf("заголовок %q: совместимая версия получила экран «обновите клиент»", c.header)
		}
		if !c.ok && doc == nil {
			t.Errorf("заголовок %q: несовместимая версия прошла", c.header)
		}
		if doc != nil {
			for _, comp := range doc.Components {
				if comp.Type != "header" && comp.Type != "text" && comp.Type != "button" {
					t.Errorf("экран «обновите клиент» использует тип %q, которого нет в первой версии словаря", comp.Type)
				}
			}
			if !strings.Contains(doc.Components[1].Text, strconv.Itoa(UIDictVersion)) {
				t.Errorf("экран не называет версию сервера: %q", doc.Components[1].Text)
			}
		}
	}
}

// В шапке остаётся одно действие: три кнопки по 44 точки на телефоне
// переносятся вторым рядом и растят шапку выше самого списка. Возврат и
// главное действие при этом не прячутся — иначе с экрана не выйти.
func TestHeaderLeavesOneAction(t *testing.T) {
	doc := &Document{ToolbarActions: []Action{
		{Action: "$goto", Target: "inbox", Label: "‹ Разбор"},
		{Action: "$goto", Target: "digest:groups", Label: "Группы"},
		{Action: "not_spam", Label: "Не спам"},
		{Action: "confirm_spam", Label: "В спам", Style: "destructive"},
		{Action: "refresh", Label: "Проверить сейчас"},
		{Action: "save", Label: "Сохранить", Style: "primary"},
	}}
	tidyToolbar(doc)
	var видно []string
	for _, д := range doc.ToolbarActions {
		видно = append(видно, д.Label)
	}
	want := []string{"‹ Разбор", "Группы", "Сохранить"}
	if len(видно) != len(want) {
		t.Fatalf("в шапке осталось %v, ждали %v", видно, want)
	}
	for i := range want {
		if видно[i] != want[i] {
			t.Fatalf("в шапке осталось %v, ждали %v", видно, want)
		}
	}
	if len(doc.ToolbarMenu) != 3 {
		t.Fatalf("под «…» ушло %d действий", len(doc.ToolbarMenu))
	}
}
