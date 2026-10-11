package api

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func chatsForPhone(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	для := []struct {
		имя   string
		кто   string
		текст string
	}{
		{"Telegram · Анна Ласточкина", "peer", "Ну всё, забирай"},
		{"Задача: Собери новости", "assistant", "За последние сутки писем нет"},
		{"Перепиши модуль watch", "assistant", "Модуль обновлён"},
	}
	for _, ч := range для {
		id, err := st.CreateChat(ч.имя)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddChatMsg(id, ч.кто, ч.текст); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return s, st
}

// На телефоне «Чаты» — это список, а не последний разговор: список за
// бургером на 288 точек обрезал имена на середине.
func TestOnPhoneChatsAreList(t *testing.T) {
	s, _ := chatsForPhone(t)

	широкий, err := s.buildScreen("ask", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// на широком экране дерево разговоров живёт в главном меню, а второй
	// панели с тем же деревом рядом нет — она дублировала меню
	if len(широкий.Panel) > 0 {
		t.Error("на широком экране осталась панель со списком — она дублирует меню")
	}
	if len(широкий.Screens) == 0 || len(широкий.Screens[0].Children) == 0 {
		t.Error("на широком экране разговоров нет в меню под «Чатами»")
	}
	if широкий.Composer == nil {
		t.Error("на широком экране рядом со списком открыт разговор")
	}

	узкий, err := s.buildScreenForPhone("ask", "", "", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(узкий.Panel) > 0 {
		t.Error("на телефоне панели быть не должно: список и есть экран")
	}
	if узкий.Composer != nil {
		t.Error("список чатов — не разговор, поля ввода в нём нет")
	}
	if узкий.Heading != "Чаты" {
		t.Errorf("заголовок экрана %q", узкий.Heading)
	}
	if !узкий.Searchable {
		t.Error("по чатам ищут: список длиннее экрана с первого же дня")
	}
	// у каждой строки лицо кружком: чат узнают по нему раньше, чем
	// читают название
	строк := 0
	for _, c := range узкий.Components {
		for _, стр := range c.Children {
			строк++
			if стр.Avatar == "" || стр.AvatarColor == "" {
				t.Errorf("строка без лица: %q", стр.Subtitle)
			}
		}
	}
	if строк != 3 {
		t.Errorf("строк в списке %d, а чатов 3", строк)
	}
}

// Разговор на телефоне — отдельный экран с ходом назад и без панели.
func TestConversationOnPhoneHasWayBack(t *testing.T) {
	s, st := chatsForPhone(t)
	чаты, _ := st.Chats()
	id := чаты[0].ID

	doc, err := s.buildScreenForPhone("chat:"+itoa(id), "", "", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Panel) > 0 {
		t.Error("две колонки в 390 точек не помещаются: панели быть не должно")
	}
	назад := false
	for _, c := range doc.Components {
		if c.Type != "crumb" {
			continue
		}
		for _, д := range c.Actions {
			if д.Target == "ask" {
				назад = true
			}
		}
	}
	if !назад {
		t.Error("из разговора нечем вернуться в список")
	}
	if doc.Subheading != "" {
		t.Error("длинная подпись на телефоне съедала три строки до первой реплики")
	}
	if len(doc.ToolbarActions) > 0 {
		t.Errorf("в шапке телефона остались кнопки: %+v", doc.ToolbarActions)
	}
	if len(doc.ToolbarMenu) == 0 {
		t.Error("кнопки должны уехать под «…», а не пропасть")
	}
	if doc.Composer == nil {
		t.Error("в разговоре пишут: поле ввода на месте")
	}
}

// Чипы источников и поиск сужают список, а пустой ответ объясняет себя.
func TestChatListSearchesAndFilters(t *testing.T) {
	s, _ := chatsForPhone(t)
	считать := func(doc *Document) int {
		n := 0
		for _, c := range doc.Components {
			n += len(c.Children)
		}
		return n
	}
	по, err := s.buildScreenForPhone("ask", "Telegram", "", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if считать(по) != 1 {
		t.Errorf("чип «Telegram» должен оставить один чат, а оставил %d", считать(по))
	}
	найдено, err := s.buildScreenForPhone("ask", "", "новости", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if считать(найдено) != 1 {
		t.Errorf("поиск по последней реплике должен найти один чат, а нашёл %d", считать(найдено))
	}
	empty, err := s.buildScreenForPhone("ask", "", "квазимодо", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Components) != 1 || empty.Components[0].Style != "empty" {
		t.Fatal("пустой ответ должен объяснять себя")
	}
	if !strings.Contains(empty.Components[0].Caption, "Ничего не нашлось") {
		t.Errorf("пустота объяснена как «здесь ничего не было»: %q", empty.Components[0].Caption)
	}
}

// Инициалы для кружка: из «Анна | Закупки из Китая» — «АЗ», а не «А|».
func TestChatInitials(t *testing.T) {
	случаи := map[string]string{
		"Анна | Закупки из Китая": "АЗ",
		"Вадим":               "В",
		"Мила Лучшая подруга": "МЛ",
		"···":                 "?",
		"telegram bot":        "TB",
	}
	for name, want := range случаи {
		if got := chatInitials(name); got != want {
			t.Errorf("%q дало %q, ждали %q", name, got, want)
		}
	}
	// цвет по имени не меняется от запроса к запросу
	if avatarColor("Вадим") != avatarColor("Вадим") {
		t.Error("цвет кружка должен быть устойчивым")
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
