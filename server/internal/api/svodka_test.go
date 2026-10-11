package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Проверки нового языка «Сводка» на экранах-списках: состав документа, а не
// разметка. Разметку глазами здесь не увидеть, поэтому смотрим то же, что
// увидит клиент, — компоненты.

// findIn — первый компонент такого вида на всём экране, включая вложенные.
func findIn(cs []Component, kind string) *Component {
	for i := range cs {
		if cs[i].Type == kind {
			return &cs[i]
		}
		if c := findIn(cs[i].Children, kind); c != nil {
			return c
		}
	}
	return nil
}

func rowsOf(cs []Component) []Component {
	var out []Component
	for i := range cs {
		if cs[i].Type == "row" {
			out = append(out, cs[i])
		}
		out = append(out, rowsOf(cs[i].Children)...)
	}
	return out
}

// Уровень записи решает, в какой группе она стоит. Раньше строки и записи
// сопоставлялись по номеру, и отбор по чипу сдвигал их друг относительно
// друга: тревога уезжала в «К сведению».
func TestNotificationsGroupedByLevel(t *testing.T) {
	s, st := testServer(t)
	записи := []struct{ key, level, title string }{
		{"проба:инфо", "info", "Отчёт по серверу"},
		{"проба:тревога", "alert", "Команда ждёт подтверждения"},
		{"проба:внимание", "warn", "Обновления не проверились"},
	}
	for _, з := range записи {
		if _, err := st.PutNotification(&store.Notification{
			Key: з.key, Source: "core", Level: з.level, Title: з.title,
		}); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := s.buildScreen(notifyScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var порядок []string
	группа := map[string]string{} // заголовок записи → заголовок группы
	for _, c := range doc.Components {
		if c.Type != "section" {
			continue
		}
		порядок = append(порядок, c.Title)
		for _, r := range rowsOf(c.Children) {
			группа[r.Subtitle] = c.Title
		}
	}
	if len(порядок) != 3 {
		t.Fatalf("групп %d, ждали три: %v", len(порядок), порядок)
	}
	want := []string{"Не поедет без вас · 1", "Стоит посмотреть · 1", "К сведению · 1"}
	for i, з := range want {
		if порядок[i] != з {
			t.Errorf("группа %d: %q, ждали %q", i, порядок[i], з)
		}
	}
	for _, з := range записи {
		name := map[string]string{"alert": "Не поедет без вас · 1", "warn": "Стоит посмотреть · 1", "info": "К сведению · 1"}[з.level]
		if группа[з.title] != name {
			t.Errorf("запись %q уровня %s попала в группу %q, а должна в %q", з.title, з.level, группа[з.title], name)
		}
	}
	if doc.Subheading == "" || !strings.Contains(doc.Subheading, "не поедет без вас") {
		t.Errorf("подпись экрана не называет, что держит работу: %q", doc.Subheading)
	}
}

// Пустой экран уведомлений объясняет себя, а не молчит серой строкой.
func TestNotificationsEmptyStateExplains(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen(notifyScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// первой стоит скрытая строка «Пуши на этом устройстве» (pushOffer): ищем
	// именно объяснение пустоты, а не первую попавшуюся строку
	var empty *Component
	for i := range doc.Components {
		if doc.Components[i].Type == "row" && doc.Components[i].Style == "empty" {
			empty = &doc.Components[i]
			break
		}
	}
	if empty == nil {
		t.Fatal("на пустом экране нет объяснения пустоты")
	}
	if len(empty.Caption) < 40 {
		t.Errorf("объяснение слишком короткое: %q", empty.Caption)
	}
}

// «Отправители»: заголовка раздела со счётчиком нет — счётчик стоит в
// фильтре; меню «…» остаётся только там, где есть второе действие.
func TestSendersWithoutExtraTitleAndMenu(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddRule("sender", "спамер@пример.рф", "spam"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule("sender", "свой@пример.рф", "ham"); err != nil {
		t.Fatal(err)
	}
	for _, отбор := range []string{"spam", "ham"} {
		doc, err := s.buildScreen("senders", отбор, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range doc.Components {
			if c.Type == "section" && strings.Contains(c.Title, "Отправител") {
				t.Errorf("%s: заголовок раздела повторяет счётчик фильтра: %q", отбор, c.Title)
			}
		}
		рядов := rowsOf(doc.Components)
		if len(рядов) != 1 {
			t.Fatalf("%s: строк %d, ждали одну", отбор, len(рядов))
		}
		r := рядов[0]
		if отбор == "ham" && len(r.Menu) != 0 {
			t.Errorf("ham: меню «…» с одним действием-двойником: %+v", r.Menu)
		}
		if отбор == "spam" && len(r.Menu) == 0 {
			t.Error("spam: «Удалить правило» должно оставаться в меню")
		}
		if len(r.Facts) == 0 {
			t.Errorf("%s: счётчики склеены в подпись, а не разложены фактами", отбор)
		}
		if r.Badge != "" {
			t.Errorf("%s: капсула %q повторяет вкладку — норма молчит", отбор, r.Badge)
		}
	}
}

// «Файлы» и «Модули»: счётчики из подписи уехали в строку чисел.
func TestCountersInNumbersRow(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("modules", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	числа := findIn(doc.Components, "figs")
	if числа == nil || len(числа.Children) == 0 {
		t.Fatal("на «Модулях» нет строки чисел")
	}
	for _, д := range числа.Children {
		if д.Text == "" || д.Title == "" {
			t.Errorf("число без значения или подписи: %+v", д)
		}
	}
	if strings.Contains(doc.Subheading, " · ") {
		t.Errorf("подпись «Модулей» всё ещё склеена через «·»: %q", doc.Subheading)
	}
	// на пустой базе файлов нет: экран должен объяснять пустоту
	files, err := s.buildScreen("files", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files.Subheading, " · ") {
		t.Errorf("подпись «Файлов» всё ещё склеена через «·»: %q", files.Subheading)
	}
	empty := findIn(files.Components, "row")
	if empty == nil || empty.Style != "empty" {
		t.Fatal("на пустом экране файлов нет объяснения пустоты")
	}
}

// «Скрипты» на пустой базе объясняют, что такое скрипт и как завести первый.
func TestScriptsEmptyStateExplains(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("scripts", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	empty := findIn(doc.Components, "row")
	if empty == nil || empty.Style != "empty" {
		t.Fatal("на пустом экране скриптов нет объяснения пустоты")
	}
	if !strings.Contains(empty.Caption, "чате") {
		t.Errorf("не сказано, что скрипт проще попросить в чате: %q", empty.Caption)
	}
}
