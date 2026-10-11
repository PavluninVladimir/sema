package web

import (
	"strings"
	"testing"
)

// Таблица для строк с одинаковым управлением: список прямо в ячейке (общий набор
// вариантов, действие сразу), капсула строки рядом с названием, полоса уровня
// строки, подпись колонки у каждой ячейки для телефона.
func TestTableCellWithListAndRowLevel(t *testing.T) {
	scr := Screen{OptionSets: map[string][]string{"модели": {"a · X", "b · Y"}}, Blocks: []Block{{
		Kind: "таблица", Title: "Роли",
		Columns: []string{"Роль", "Основная", "Ждём", "Последний ответ"},
		Children: []Block{
			{Kind: "строка", Name: "Чаты", Caption: "ответы по письмам",
				Cells: []Cell{
					{Select: &Select{Name: "value", Value: "a · X", OptionSet: "модели", SpokenLabel: "Чаты — основная",
						Action: Button{Action: "set_role_model", Target: "Чаты"}}},
					{Select: &Select{Name: "value", Value: "60 с", Options: []string{"30 с", "60 с"},
						Action: Button{Action: "set_role_timeout", Target: "Чаты"}}},
					{Text: "ещё не спрашивали"},
				}},
			{Kind: "строка", Name: "Перевод", Level: "warn", Pills: []Pill{{Text: "без модели", Level: "warn"}},
				Cells: []Cell{{Text: "нет"}, {Text: "60 с"}, {Text: "—"}}},
		},
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, want := range []string{
		// список в ячейке с общим набором: в разметке одно значение, варианты у клиента
		`data-option-set="модели"`,
		`aria-label="Чаты — основная"`,
		`"action":"set_role_model","target":"Чаты"`,
		// список со своими вариантами
		`<option value="30 с" >30 с</option>`,
		// подпись колонки — у каждой ячейки, на телефоне она печатается перед значением
		`data-label="Основная"`, `data-label="Ждём"`, `data-label="Последний ответ"`,
		// уровень строки и капсула рядом с названием
		`class="table-row warn"`, `<span class="pill warn">без модели</span>`,
		`has-select`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в разметке таблицы нет %q:\n%s", want, got)
		}
	}
	if strings.Count(got, `id="option-sets"`) != 1 {
		t.Error("общий набор вариантов приехал не один раз")
	}
	// капсула живёт у названия, а не в колонке управления: колонки управления у
	// этой таблицы нет вовсе
	if strings.Contains(got, `table-cell controls`) {
		t.Errorf("у таблицы без управления строки появилась колонка управления:\n%s", got)
	}
}
