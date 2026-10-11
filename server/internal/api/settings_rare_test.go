package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// rareBody — тело «Тонкой настройки» экрана: то, что приезжает по раскрытию
// (часть `rare`). Старые проверки «на экране есть поле» после переезда
// пределов смотрят и сюда: поле не исчезло, а лежит в свёрнутом разделе.
func rareBody(t *testing.T, s *Server, screen string) []Component {
	t.Helper()
	blocks, err := s.buildPart(screen, rareID, "", 0)
	if err != nil {
		t.Fatalf("тонкая настройка экрана %s: %v", screen, err)
	}
	return blocks
}

// dumpWithRare — экран и тело его «Тонкой настройки» одной строкой JSON.
func dumpWithRare(t *testing.T, s *Server, screen string, doc *Document) string {
	t.Helper()
	b, err := json.Marshal(rareBody(t, s, screen))
	if err != nil {
		t.Fatal(err)
	}
	return dumpScreen(doc) + string(b)
}

// formIn — форма с таким ID среди компонентов, в том числе вложенных.
func formIn(cs []Component, id string) *Component {
	for i := range cs {
		if cs[i].Type == "form" && cs[i].ID == id {
			return &cs[i]
		}
		if f := formIn(cs[i].Children, id); f != nil {
			return f
		}
	}
	return nil
}

// ---- проход foldRare ----

func strPtr(s string) *string { return &s }

// rareDoc — экран с обычным, редким и опасным.
func rareDoc() *Document {
	return &Document{Components: []Component{
		{Type: "text", Text: "вводная"},
		{Type: "form", ID: "plain", Title: "Обычная", Fields: []Field{{Type: "text", Name: "a", Label: "А", Value: "1"}},
			Submit: &Action{Action: "save_plain"}},
		{Type: "form", ID: "limits", Title: "Пределы", Tier: tierRare, Fields: []Field{
			{Type: "number", Name: "x", Label: "Икс", Value: "5", Default: strPtr("5")},
			{Type: "number", Name: "y", Label: "Игрек", Value: "9", Default: strPtr("7")},
		}, Submit: &Action{Action: "set_limits"}},
		{Type: "form", ID: "mixed", Title: "Смешанная", Fields: []Field{
			{Type: "toggle", Name: "daily", Label: "Каждый день", Value: "true"},
			{Type: "text", Name: "rare", Label: "Редкое", Value: "", Default: strPtr(""), Tier: tierRare},
		}, Submit: &Action{Action: "set_mixed"}},
		{Type: "section", Title: "Раздел", Tier: tierRare, Children: []Component{
			{Type: "form", ID: "inner", Title: "Внутри", Fields: []Field{{Type: "toggle", Name: "t", Label: "Тумблер", Value: "on", Default: strPtr("true")}},
				Submit: &Action{Action: "set_inner"}},
		}},
		{Type: "form", ID: "danger", Title: "Опасное · сохраняется отдельно", Fields: []Field{{Type: "toggle", Name: "d", Label: "Д", Value: "false"}},
			Submit: &Action{Action: "save_danger"}},
		{Type: "text", Style: "secondary", Text: "после опасного"},
	}}
}

// Всё помеченное собирается в один свёрнутый ленивый раздел и ставится перед
// «Опасным»; в нём — счёт параметров и сколько из них не по умолчанию.
func TestFoldRareGathersOneSectionBeforeDanger(t *testing.T) {
	doc := rareDoc()
	foldRare(doc)

	var folds []int
	for i, c := range doc.Components {
		if c.ID == rareID {
			folds = append(folds, i)
		}
	}
	if len(folds) != 1 {
		t.Fatalf("разделов «Тонкая настройка» %d, а должен быть один", len(folds))
	}
	fold := doc.Components[folds[0]]
	if fold.Type != "section" || !fold.Collapsed || fold.Lazy != rareID || fold.Style != fineStyle || !fold.Deferred || len(fold.Children) != 0 {
		t.Errorf("раздел собран не так: %+v", fold)
	}
	if next := doc.Components[folds[0]+1]; !isDanger(next) {
		t.Errorf("после «Тонкой настройки» должно стоять «Опасное», а стоит %q", next.Title)
	}
	// параметров четыре: x, y, rare, t; не по умолчанию один — y
	if !strings.HasPrefix(fold.Caption, "4 параметра") || fold.Meta != "1 изменён" || fold.Level != LevelInfo {
		t.Errorf("подпись раздела: %q · %q (%q)", fold.Caption, fold.Meta, fold.Level)
	}
	for _, name := range []string{"пределы", "смешанная", "внутри"} {
		if !strings.Contains(fold.Caption, name) {
			t.Errorf("в подписи нет группы %q: %q", name, fold.Caption)
		}
	}

	// в теле — две формы и раздел со своей формой, всё строками настройки; у
	// смешанной обычное поле осталось на месте
	var ids []string
	for _, c := range doc.rare {
		ids = append(ids, c.ID+"/"+c.Type)
		if c.Type == "form" && c.Style != formRows {
			t.Errorf("форма %q в тонкой настройке не строками настройки: %q", c.ID, c.Style)
		}
	}
	// помеченный раздел уезжает целиком, со своими детьми
	if got := strings.Join(ids, " "); got != "limits/form mixed-rare/form /section" {
		t.Errorf("в теле редкого: %s", got)
	}
	if inner := formIn(doc.rare, "inner"); inner == nil || inner.Style != formRows {
		t.Errorf("форма внутри помеченного раздела: %+v", inner)
	}
	for _, c := range doc.Components {
		if c.ID == "mixed" {
			if len(c.Fields) != 1 || c.Fields[0].Name != "daily" {
				t.Errorf("обычное поле смешанной формы: %+v", c.Fields)
			}
		}
	}
	twin := formIn(doc.rare, "mixed-rare")
	if twin == nil || twin.Submit == nil || twin.Submit.Action != "set_mixed" || twin.Title != "Смешанная" || len(twin.Actions) != 0 {
		t.Errorf("близнец смешанной формы: %+v", twin)
	}
	// раздел, из которого вынули всё, пустым не остаётся
	for _, c := range doc.Components {
		if c.Type == "section" && c.Title == "Раздел" {
			t.Error("пустой раздел остался на экране")
		}
	}
}

// Без единой метки экран не меняется ни на строку: проход не имеет права
// сворачивать то, что ему не показали.
func TestFoldRareLeavesPlainScreenAlone(t *testing.T) {
	doc := &Document{Components: []Component{
		{Type: "text", Text: "вводная"},
		{Type: "form", ID: "plain", Fields: []Field{{Type: "text", Name: "a", Value: "1", Default: strPtr("1")}}},
	}}
	before, _ := json.Marshal(doc.Components)
	foldRare(doc)
	after, _ := json.Marshal(doc.Components)
	if string(before) != string(after) || doc.rare != nil {
		t.Errorf("экран без редкого изменился:\n%s\n%s", before, after)
	}
}

// Всё по умолчанию — так и сказано; склонение «изменён / изменены».
func TestFoldRareStateWords(t *testing.T) {
	mk := func(values ...string) *Document {
		var fields []Field
		for i, v := range values {
			fields = append(fields, Field{Type: "number", Name: "f" + string(rune('a'+i)), Value: v, Default: strPtr("1")})
		}
		return &Document{Components: []Component{{Type: "form", ID: "f", Title: "Форма", Tier: tierRare, Fields: fields, Submit: &Action{Action: "set_f"}}}}
	}
	for want, values := range map[string][]string{
		"всё по умолчанию": {"1", "1", "1"},
		"1 изменён":        {"2", "1"},
		"2 изменены":       {"2", "3", "1"},
	} {
		doc := mk(values...)
		foldRare(doc)
		if got := doc.Components[0].Meta; got != want {
			t.Errorf("значения %v: подпись %q, а ждали %q", values, got, want)
		}
	}
}

// Поле без умолчания «не изменено»: сравнивать нечего.
func TestFieldWithoutDefaultIsNotChanged(t *testing.T) {
	if fieldChanged(Field{Type: "text", Value: "x"}) {
		t.Error("поле без умолчания сочтено изменённым")
	}
	if fieldChanged(Field{Type: "toggle", Value: "on", Default: strPtr("true")}) {
		t.Error("«on» и «true» у тумблера — одно и то же")
	}
	if !fieldChanged(Field{Type: "toggle", Value: "false", Default: strPtr("true")}) {
		t.Error("выключенный тумблер при включённом умолчании не сочтён изменённым")
	}
}

// ---- разметка ----

// Свёрнутая «Тонкая настройка» приезжает заголовком с подписью и без тела, а
// на экране стоит нижняя полоса: форм там нет, но они приедут по раскрытию.
func TestFineFoldMarkup(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting("mcp.chat_calls", "5"); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen(mcpScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	markup, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	html := string(markup)
	for _, want := range []string{
		`class="fold fragment fine"`, `id="fold-rare"`, `data-fragment="rare"`,
		`<span class="fine-note">16 параметров`, `<span class="fine-state changed">1 изменён</span>`,
		`id="save-bar"`, // полоса есть, хотя форм в самом экране уже нет
	} {
		if !strings.Contains(html, want) {
			t.Errorf("в разметке экрана нет %q", want)
		}
	}
	if strings.Contains(html, `name="chat_calls"`) {
		t.Error("тело «Тонкой настройки» приехало вместе с экраном, а не по раскрытию")
	}
}

// Строка настройки в разметке: корень остаётся полем формы (по нему считает
// полоса), подпись, подсказка, контрол, единица и «по умолчанию».
func TestSettingRowMarkup(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting("mcp.recheck_s", "10"); err != nil {
		t.Fatal(err)
	}
	blocks := rareBody(t, s, mcpScreen)
	var rendered []web.Block
	for _, c := range blocks {
		rendered = append(rendered, toBlock(c))
	}
	markup, err := web.RenderFragment(rendered)
	if err != nil {
		t.Fatal(err)
	}
	html := string(markup)
	for _, want := range []string{
		`class="form deferred rows"`, `class="field setting`, `class="setting-label"`, `class="hint setting-hint"`,
		`class="setting-number" type="number"`, `<span class="unit">с</span>`,
		`data-default="300"`, `class="to-default"`, // умолчание и кнопка у изменённой строки
		`field setting setting-switch is-default`, // тумблер документа клиента — по умолчанию
		`class="field setting is-default"`,        // строка без правок: кнопка скрыта
	} {
		if !strings.Contains(html, want) {
			t.Errorf("в разметке строк настройки нет %q", want)
		}
	}
	// изменённая строка — без is-default
	i := strings.Index(html, `name="recheck_s"`)
	if i < 0 || strings.Contains(html[max(0, i-400):i], "is-default") {
		t.Error("у изменённой строки стоит is-default")
	}
	// форма без Style «rows» осталась прежней
	plain := Component{Type: "form", ID: "p", Fields: []Field{{Type: "text", Name: "a", Label: "А"}}, Submit: &Action{Action: "save_x"}}
	plainMarkup, err := web.RenderFragment([]web.Block{toBlock(plain)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plainMarkup), "setting") || strings.Contains(string(plainMarkup), " rows") {
		t.Errorf("обычная форма нарисована строками настройки: %s", plainMarkup)
	}
}
