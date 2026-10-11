package web

import (
	"strings"
	"testing"
)

// Строка с переходом не объявляет ссылкой саму себя, когда внутри неё
// стоят флажок выбора и кнопки. 10 сентября 2026 разбор доступности
// показал: строка письма — role="link", а внутри неё флажок «Отметить» и
// кнопка «Не спам». Для чтения вслух это одна ссылка, проглотившая свои
// же кнопки, а пальцем легко открыть письмо вместо нажатия кнопки.
// Переход теперь живёт ссылкой на названии, мышью кликается вся строка.
func TestRowWithButtonsNotDeclaredLink(t *testing.T) {
	html := buildBlock(t, Block{
		Kind:       "строка",
		Name:       "Счёт за свет",
		Caption:    "похоже на спам · 78 %",
		GoTo:       "msg:42",
		Selectable: true,
		Buttons:    []Button{{Name: "Не спам", Action: "not_spam", Target: "42"}},
	})
	if strings.Contains(html, `role="link"`) {
		t.Error("строка с флажком и кнопкой объявлена ссылкой целиком")
	}
	// двоеточие в адресе экрана html/template экранирует (%3a) — так же,
	// как в ссылках вкладок; URLSearchParams у клиента читает его обратно
	if !strings.Contains(html, `<a class="row-link" href="#screen=msg%3a42">Счёт за свет</a>`) {
		t.Errorf("названию не досталось ссылки перехода:\n%s", html)
	}
	// клик по всей строке остаётся: он живёт на data-goto, а не на роли
	if !strings.Contains(html, `data-goto="msg:42"`) || !strings.Contains(html, "clickable") {
		t.Error("строка перестала кликаться мышью")
	}
	// кнопка и флажок никуда не делись
	if !strings.Contains(html, "Не спам") || !strings.Contains(html, `aria-label="Отметить"`) {
		t.Error("потерялись собственные элементы управления строки")
	}
}

// А там, где называть нечего, ссылке не на чем держаться — строка
// остаётся ролью «ссылка», иначе с клавиатуры до неё не добраться вовсе.
func TestRowWithoutNameStaysLink(t *testing.T) {
	html := buildBlock(t, Block{Kind: "строка", Meta: "12:40", GoTo: "msg:7"})
	if !strings.Contains(html, `role="link"`) || !strings.Contains(html, `tabindex="0"`) {
		t.Errorf("безымянная строка потеряла доступ с клавиатуры:\n%s", html)
	}
}

// Заголовок строки тоже годится в имя ссылки: у строк без «Названия»
// именно он и есть название.
func TestLinkTakesTitleWhenNameMissing(t *testing.T) {
	html := buildBlock(t, Block{Kind: "строка", Title: "Перечитать вложения", GoTo: "files"})
	if strings.Contains(html, `role="link"`) {
		t.Error("роль осталась, хотя ссылка есть")
	}
	if !strings.Contains(html, `<a class="row-link" href="#screen=files">Перечитать вложения</a>`) {
		t.Errorf("заголовок не стал ссылкой:\n%s", html)
	}
}

// Вкладки и фильтры говорят, какая из них выбрана. Без aria-selected
// role="tab" обещает состояние и не сообщает его: вслух все вкладки
// звучат одинаково.
func TestTabsReportSelected(t *testing.T) {
	scr := Screen{
		Title:   "Уведомления",
		Addr:    "notifications",
		Tabs:    []Chip{{Name: "Все", Addr: "notifications"}, {Name: "Почта", Addr: "notifications~mail"}},
		Tab:     "notifications~mail",
		Filters: []Chip{{Name: "Открытые", Addr: "open"}, {Name: "Сделано", Addr: "done"}},
		Filter:  "done",
	}
	markup, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	html := string(markup)
	for _, want := range []string{
		`role="tab" aria-selected="false"`,
		`role="tab" aria-selected="true"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("нет %q", want)
		}
	}
	// ровно по одной выбранной на каждый из двух рядов
	if got := strings.Count(html, `aria-selected="true"`); got != 2 {
		t.Errorf("выбранных вкладок %d, а рядов два", got)
	}
	if got := strings.Count(html, `aria-selected="false"`); got != 2 {
		t.Errorf("невыбранных вкладок %d, а должно быть две", got)
	}
}

func buildBlock(t *testing.T, blk Block) string {
	t.Helper()
	markup, err := Render(Screen{Title: "проба", Addr: "проба", Blocks: []Block{blk}})
	if err != nil {
		t.Fatal(err)
	}
	return string(markup)
}
