package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Список моделей приезжает один раз на экран, а не в каждой строке.
//
// У «Моделей» девять ролей, у каждой список основной и запасной модели,
// а моделей подключено полсотни: в разметке это под тысячу одинаковых
// строчек вариантов — больше трети всех узлов экрана (3597 из них 1458).
// Человек при этом видит по одному имени в строке.
func TestVariantsNotRepeatedInEveryRow(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings:models", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.OptionSets) == 0 {
		t.Fatal("общих наборов нет — значит списки снова в каждой строке")
	}
	// ни один список роли не тащит свои варианты
	var своих int
	for _, c := range doc.Components {
		своих += variantsInside(c)
	}
	if своих > 0 {
		t.Errorf("списков со своими вариантами: %d", своих)
	}
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	// набор в разметке ровно один
	if n := strings.Count(string(html), `id="option-sets"`); n != 1 {
		t.Errorf("наборов в разметке %d, а должен быть один", n)
	}
	if !strings.Contains(string(html), `data-option-set=`) {
		t.Error("списки не помечены набором — клиенту неоткуда взять варианты")
	}
}

// variantsInside — сколько списков со своим набором вариантов среди
// тех, что ссылаются на общий набор. Такой список — ошибка: варианты
// поехали бы дважды.
func variantsInside(c Component) int {
	n := 0
	for _, sel := range c.Selects {
		if sel.OptionsRef != "" && len(sel.Options) > 0 {
			n++
		}
	}
	for _, ch := range c.Children {
		n += variantsInside(ch)
	}
	return n
}

// Имя со слэшем не должно закрывать тег <script>: внутри него браузер
// разметку не разбирает, но «</script>» ищет.
func TestSetDoesNotCloseScriptTag(t *testing.T) {
	э := web.Screen{Title: "проба", Addr: "проба",
		OptionSets: map[string][]string{"модели": {"злая</script><b>модель"}}}
	html, err := web.Render(э)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "</script><b>") {
		t.Error("значение из набора закрыло тег скрипта — страница развалится")
	}
}
