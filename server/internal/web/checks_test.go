package web

import (
	"strings"
	"testing"
)

// Поле «checks» — несколько вариантов чипами («Источники» канала уведомлений). У каждого
// чипа свой флажок «имя.вариант»: несколько одноимённых полей форма превращает в одно
// значение, а снятый флажок браузер не шлёт вовсе, поэтому его имя уходит в `toggles`
// формы и сервер восстанавливает «false».
func TestChecksFieldHasOwnCheckboxPerOption(t *testing.T) {
	field := Field{Kind: "checks", Name: "sources", AnchorID: "ch-sources", Label: "Источники",
		Options: []string{"Серверы", "Почта", "Запросы"}, Value: "Серверы,Почта", Hint: "откуда брать"}
	out, err := Render(Screen{Addr: "settings:notify", Blocks: []Block{{
		Kind: "форма", ID: "ch", Style: "rows", Deferred: true,
		Fields:  []Field{field},
		Buttons: []Button{{Name: "Сохранить", Action: "save_notify_channel", Target: "1"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	for _, want := range []string{
		`<input type="checkbox" name="sources.Серверы" value="true" checked>`,
		`<input type="checkbox" name="sources.Почта" value="true" checked>`,
		`<input type="checkbox" name="sources.Запросы" value="true" >`,
		`name="toggles" value="sources.Серверы,sources.Почта,sources.Запросы"`,
		`class="field-chips"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("в разметке нет %q:\n%s", want, html)
		}
	}
	// выбранные чипы помечены и на сервере — без этого они были бы серыми до первого щелчка
	if strings.Count(html, `chip-choice selected`) != 2 {
		t.Errorf("выбранных чипов %d, ждали 2", strings.Count(html, `chip-choice selected`))
	}
	// запятая внутри значения не склеивает варианты: «Почта» не находится в «Почтовый ящик»
	if chosen("Почтовый ящик", "Почта") || !chosen("Серверы, Почта", "Почта") {
		t.Error("выбор вариантов разбирается не по запятым")
	}
}

// У строки с раскрытием подпись «Подробнее» заменяется словом, которое называет то, что
// внутри: «Настройки». Пусто — прежнее «Подробнее».
func TestRowExpandLabel(t *testing.T) {
	render := func(expand string) string {
		out, err := Render(Screen{Addr: "x", Blocks: []Block{{
			Kind: "строка", ID: "r1", Name: "Канал", Expand: expand,
			Children: []Block{{Kind: "текст", Text: "внутри"}},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if html := render("Настройки"); !strings.Contains(html, `class="details-more">Настройки</summary>`) || strings.Contains(html, "Подробнее") {
		t.Errorf("подпись раскрытия не заменена:\n%s", html)
	}
	if html := render(""); !strings.Contains(html, `class="details-more">Подробнее</summary>`) {
		t.Errorf("прежняя подпись пропала:\n%s", html)
	}
}
