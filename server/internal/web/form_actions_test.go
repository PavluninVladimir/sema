package web

import (
	"strings"
	"testing"
)

// Ряд кнопок формы — свой класс `form-actions`, а не `right-side` строки
// списка со стилем в разметке (30 сентября 2026: кнопка «Добавить и
// проверить» липла к последнему полю). Разметку проверяет этот тест,
// вёрстку — браузерный `TestBrowserFormActionsDoNotStickToLastField`,
// который идёт только с Chrome.
//
// Обычная форма и мастер собираются двумя разными ветками шаблона; обе
// обязаны отдавать один и тот же ряд. Форма без кнопок (её сохраняет нижняя
// полоса) ряда не получает вовсе.
func TestFormButtonsUseFormActionsRow(t *testing.T) {
	поле := Field{Kind: "text", Name: "имя", AnchorID: "f-имя", Label: "Имя", Hint: "коротко"}
	формы := func(разметка string) []string {
		var найдено []string
		for {
			i := strings.Index(разметка, "<form")
			if i < 0 {
				return найдено
			}
			j := strings.Index(разметка[i:], "</form>")
			if j < 0 {
				t.Fatalf("форма не закрыта:\n%s", разметка[i:])
			}
			найдено = append(найдено, разметка[i:i+j])
			разметка = разметка[i+j:]
		}
	}
	// ряд лежит после последнего поля и не несёт ни right-side, ни стиля
	// прямо в разметке
	проверить := func(метка, форма string) {
		t.Helper()
		if !strings.Contains(форма, `<div class="form-actions">`) {
			t.Errorf("%s: нет ряда form-actions:\n%s", метка, форма)
			return
		}
		if strings.Contains(форма, "right-side") || strings.Contains(форма, "justify-content") {
			t.Errorf("%s: в форме остался right-side или стиль в разметке:\n%s", метка, форма)
		}
		if strings.LastIndex(форма, `class="field`) > strings.Index(форма, `<div class="form-actions">`) {
			t.Errorf("%s: поле стоит после ряда кнопок", метка)
		}
		if !strings.Contains(форма, `type="submit"`) {
			t.Errorf("%s: в ряду нет кнопки отправки", метка)
		}
	}

	out, err := Render(Screen{Addr: "settings:search", Blocks: []Block{{
		Kind: "форма", ID: "web-find", Title: "Поиск",
		Fields:  []Field{поле},
		Buttons: []Button{{Name: "Сохранить", Action: "set", Kind: "primary"}, {Name: "Проверить", Action: "check"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ф := формы(string(out))
	if len(ф) != 1 {
		t.Fatalf("форм %d, ждали 1", len(ф))
	}
	проверить("форма", ф[0])
	if strings.Count(ф[0], `type="submit"`) != 2 {
		t.Errorf("форма: кнопок отправки не две:\n%s", ф[0])
	}

	out, err = Render(Screen{Addr: "tasks", Wizard: &Wizard{
		Title:  "Новая задача",
		Steps:  []Step{{Title: "Что делать", Hint: "словами", Fields: []Field{поле}}},
		Submit: Button{Name: "Создать", Action: "create", Kind: "primary"},
		Cancel: Button{Name: "Отмена", Action: "cancel"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ф = формы(string(out))
	if len(ф) != 1 {
		t.Fatalf("мастер: форм %d, ждали 1", len(ф))
	}
	проверить("мастер", ф[0])

	out, err = Render(Screen{Addr: "settings:mail", Blocks: []Block{{
		Kind: "форма", ID: "общее", Deferred: true, Title: "Общее",
		Fields:  []Field{поле},
		Buttons: []Button{{Name: "Сохранить", Action: "save"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ф = формы(string(out))
	if len(ф) != 1 || strings.Contains(ф[0], "form-actions") || strings.Contains(ф[0], `<button`) {
		t.Errorf("форма нижней полосы получила ряд кнопок или кнопку:\n%v", ф)
	}
}
