package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Граф в вебе рисуется на сервере: у клиента на htmx своего JavaScript
// почти нет, а тянуть библиотеку ради силовой раскладки незачем.
func TestGraphRenderedAsMarkup(t *testing.T) {
	g := &Graph{}
	for i := 0; i < 12; i++ {
		g.Nodes = append(g.Nodes, GraphNode{ID: string(rune('a' + i)), Label: "Узел " + string(rune('А'+i)), Area: "код"})
	}
	// первый узел связан со всеми: он должен оказаться в середине и крупнее
	for i := 1; i < 12; i++ {
		g.Edges = append(g.Edges, GraphEdge{From: "a", To: string(rune('a' + i))})
	}
	doc := &Document{Heading: "Память", Components: []Component{{Type: "graph", Graph: g}}}
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	// считаем внутри самого графа, а не по всей странице: в оболочке
	// живут свои значки (поиск по меню, шестерёнка), и счёт кружков по
	// документу ломался бы от каждого нового значка
	s := graphDrawing(t, string(html))
	if !strings.Contains(s, "<svg") {
		t.Fatal("граф не нарисован")
	}
	if n := strings.Count(s, "<circle"); n != 12 {
		t.Fatalf("узлов нарисовано %d, ждали 12", n)
	}
	if n := strings.Count(s, "<line"); n != 11 {
		t.Fatalf("связей нарисовано %d, ждали 11", n)
	}
	// запасной вид: список под рисунком — по нему ищут глазами
	if !strings.Contains(s, "Списком") || !strings.Contains(s, "Узел А") {
		t.Fatal("списка узлов под графом нет")
	}
	// подписи не наезжают: длинные имена обрезаны
	if strings.Contains(s, "Узел АБВГДЕЖЗИКЛМНОПРС") {
		t.Fatal("длинная подпись не обрезана")
	}
}

// Пустой граф — не картинка в никуда, а объяснение.
func TestEmptyGraphExplainsItself(t *testing.T) {
	doc := &Document{Components: []Component{{Type: "graph", Graph: &Graph{}}}}
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if strings.Contains(graphDrawing(t, s), "<svg") {
		t.Fatal("нарисован пустой граф")
	}
	if !strings.Contains(s, "Связей пока нет") {
		t.Fatalf("пустота не объяснена: %s", clipStr(s, 200))
	}
}

func clipStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Мастера в вебе не было вовсе — новую задачу и ящик завести было нельзя.
// В вебе шаги идут один под другим, а кнопки несут действие в name/value.
func TestWizardRenders(t *testing.T) {
	doc := &Document{Heading: "Задачи", Wizard: &Wizard{
		Title: "Новая задача",
		Steps: []WizardStep{
			{Title: "Что делать", Fields: []Field{{Type: "text", Name: "what", Label: "Что сделать"}}},
			{Title: "Когда", Hint: "готовое или своё", Fields: []Field{
				{Type: "chips", Name: "preset", Label: "Как", Options: []string{"каждый час", "9:00"}},
				{Type: "text", Name: "when", Label: "Точно"},
			}},
		},
		Submit: Action{Action: "create_task", Label: "Создать", Style: "primary"},
		Cancel: Action{Action: "close_task_form", Label: "Отмена"},
	}}
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{"<fieldset", "Что делать", "Когда", `name="what"`, `name="preset"`,
		`name="_кнопка" value="create_task|"`, `value="close_task_form|"`, "formnovalidate"} {
		if !strings.Contains(s, want) {
			t.Fatalf("в мастере нет %q", want)
		}
	}
}

// graphDrawing — кусок разметки от блока графа и до конца документа:
// всё, что до него, — оболочка со своими значками.
func graphDrawing(t *testing.T, html string) string {
	t.Helper()
	i := strings.Index(html, `<figure class="graph"`)
	if i < 0 {
		return ""
	}
	return html[i:]
}
