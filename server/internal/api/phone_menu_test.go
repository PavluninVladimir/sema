package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Вкладки полосы берутся из самого меню: у пункта уже есть значок,
// название, счётчик и уровень, и второй список разошёлся бы с первым.
func TestBarTabsComeFromMenu(t *testing.T) {
	меню := []Screen{
		{ID: notifyScreen, Title: "Ждут вас · 4", Icon: "bell", Badge: "4", Level: LevelAlert},
		{ID: "inbox", Title: "Почта", Icon: "tray.full", Badge: "3"},
		{ID: "ask", Title: "Чаты", Icon: "bubble.left"},
	}
	got := barTabs(меню, []string{notifyScreen, "inbox", "модуля-нет"}, "inbox")
	if len(got) != 2 {
		t.Fatalf("раздела нет — вкладки быть не должно: %d вкладок", len(got))
	}
	if got[0].Title != "Ждут вас" {
		t.Errorf("подпись вкладки должна стоять на месте, а не меняться со счётчиком: %q", got[0].Title)
	}
	if got[0].Icon != "bell" || got[1].Badge != "3" {
		t.Errorf("значок и счётчик должны приехать из меню: %+v", got)
	}
}

// Счётчик вкладки — тот же, что в меню: у свёрнутой «Почты» он собран с
// ящиков, и полоса не должна говорить другое число про то же самое.
func TestTabCounterCollectedFromChildren(t *testing.T) {
	почта := Screen{ID: "inbox", Title: "Почта", Icon: "tray.full", Children: []Screen{
		{ID: "inbox@a", Title: "a", Badge: "2"},
		{ID: "inbox@b", Title: "b", Badge: "1"},
	}}
	doc := &Document{Screen: "tasks", Screens: []Screen{почта}, PhoneTabs: []Screen{почта}}
	э := toScreen(doc)
	if len(э.TabBar) != 1 {
		t.Fatalf("вкладок %d", len(э.TabBar))
	}
	if э.TabBar[0].Count != "3" {
		t.Errorf("на полосе %q, а в меню собрано с ящиков 3", э.TabBar[0].Count)
	}
}

// Разметка телефона: полоса, ручка, шапка с хозяином, поиск и слова, по
// которым пункт находится.
func TestMarkupCarriesBarAndMenuSearch(t *testing.T) {
	doc := &Document{
		Screen: "tasks", Owner: "owner@example.org",
		Screens: []Screen{
			{ID: "tasks", Title: "Задачи", Icon: "clock", Words: "задачи расписание воркеры"},
			{ID: "inbox", Title: "Почта", Icon: "tray.full", Badge: "3"},
		},
		PhoneTabs: []Screen{{ID: "inbox", Title: "Почта", Icon: "tray.full", Badge: "3"}},
	}
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, чего := range []string{`id="tabbar"`, `class="grabber"`, `class="menu-header"`, `id="menu-search"`,
		"owner@example.org", `data-words="задачи расписание воркеры"`} {
		if !strings.Contains(s, чего) {
			t.Errorf("в разметке нет %s", чего)
		}
	}
	// на полосе вкладка ведёт на экран тем же способом, что пункт меню
	if !strings.Contains(s, `<nav id="tabbar"`) || !strings.Contains(s, "tab-name") {
		t.Error("полоса вкладок не нарисована")
	}
}

// Хозяина нет — шапки нет: пустой кружок с пустой строкой хуже, чем её
// отсутствие.
func TestWithoutOwnerNoHeaderMenu(t *testing.T) {
	doc := &Document{Screen: "tasks", Screens: []Screen{{ID: "tasks", Title: "Задачи"}}}
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), `class="menu-header"`) {
		t.Error("шапка нарисована без хозяина")
	}
}
