package api

import (
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// rowActions — всё, что можно сделать со строкой: и видимое, и
// уехавшее под «…» проходом tidyRows.
func rowActions(c Component) []string {
	var out []string
	for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
		out = append(out, a.Action)
	}
	return out
}

// Экран одной записи: про запись сказано всё — откуда она, чем её
// переписывали, где она в коде и что из неё выросло в графе. Раньше от
// записи была видна только строка списка с обрезанным текстом.
func TestMemoryRecordScreenTellsAboutRecord(t *testing.T) {
	s, st := testServer(t)
	сессия, err := st.StartMemorySession(0, "Claude Code · ~/repo/sema", "opus")
	if err != nil {
		t.Fatal(err)
	}
	старая, err := st.SaveMemory(&store.Memory{Kind: store.MemFact, Area: "код",
		Text: "htmx подставляет ошибки в цель", Agent: "Сёма-чат"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.SaveMemory(&store.Memory{Kind: store.MemLesson, Area: "код",
		Text:       "htmx 4 подставляет ответы 4xx в цель как обычный ответ — JSON 401 попадал в окно",
		Agent:      "Claude Code · ~/repo/sema",
		Model:      "opus",
		SessionID:  сессия,
		Supersedes: старая,
		Weight:     2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindToPlace(store.Place{MemoryID: id, Repo: "server",
		File: "internal/web/static/app.js", Symbol: "обработатьОтвет", Line: 42, Source: "урок из работы"}); err != nil {
		t.Fatal(err)
	}
	узел, err := st.EnsureMemoryNode("сущность", "htmx", "код")
	if err != nil {
		t.Fatal(err)
	}
	другой, err := st.EnsureMemoryNode("сущность", "веб-клиент", "код")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMemoryEdge(узел, другой, "живёт в", id); err != nil {
		t.Fatal(err)
	}

	doc, err := s.buildScreen("memory:rec:"+strconv.FormatInt(id, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Heading, "JSON 401 попадал в окно") {
		t.Errorf("в заголовке нет текста записи: %q", doc.Heading)
	}
	if !strings.Contains(doc.Subheading, "урок") || !strings.Contains(doc.Subheading, "код") {
		t.Errorf("подпись не называет вид и область: %q", doc.Subheading)
	}
	назад := false
	for _, a := range append(append([]Action{}, doc.ToolbarActions...), doc.ToolbarMenu...) {
		if a.Action == "$goto" && a.Target == "memory" {
			назад = true
		}
	}
	if !назад {
		t.Error("с экрана записи некуда вернуться в «Память»")
	}

	откуда := findSection(doc.Components, "Откуда")
	if откуда == nil {
		t.Fatal("нет раздела «Откуда»")
	}
	var видноСессию bool
	for _, r := range откуда.Children {
		for _, a := range append(append([]Action{}, r.Actions...), r.Menu...) {
			if a.Target == "memory:session:"+strconv.FormatInt(сессия, 10) {
				видноСессию = true
			}
		}
		var подписи []string
		for _, f := range r.Facts {
			подписи = append(подписи, f.Label+": "+f.Value)
		}
		if !strings.Contains(strings.Join(подписи, " · "), "opus") {
			t.Errorf("не сказано, какая модель извлекла: %v", подписи)
		}
	}
	if !видноСессию {
		t.Error("из «Откуда» не попасть в сессию, где запись родилась")
	}

	версии := findSection(doc.Components, "История версий · 2")
	if версии == nil {
		t.Fatalf("нет истории версий: %v", sectionTitles(doc.Components))
	}
	if len(версии.Children) != 2 {
		t.Fatalf("версий %d, ждали две", len(версии.Children))
	}
	if !strings.Contains(версии.Children[1].Subtitle, "htmx подставляет ошибки") {
		t.Errorf("прежней версии не видно: %q", версии.Children[1].Subtitle)
	}
	if версии.Children[1].Caption == "" {
		t.Error("у версии не сказано, кто и когда её записал")
	}

	место := findSection(doc.Components, "Место в коде")
	if место == nil {
		t.Fatal("нет раздела «Место в коде»")
	}
	if !strings.Contains(место.Children[0].Subtitle, "app.js") {
		t.Errorf("файла привязки не видно: %q", место.Children[0].Subtitle)
	}
	var координата string
	for _, f := range место.Children[0].Facts {
		координата += f.Label + ": " + f.Value + " · "
	}
	for _, want := range []string{"обработатьОтвет", "урок из работы"} {
		if !strings.Contains(координата, want) {
			t.Errorf("в координате нет «%s»: %q", want, координата)
		}
	}

	связи := findSection(doc.Components, "Кристалл и связи")
	if связи == nil {
		t.Fatal("нет раздела «Кристалл и связи»")
	}
	if !strings.Contains(связи.Children[0].Subtitle, "htmx —живёт в→ веб-клиент") {
		t.Errorf("связь записи не показана: %q", связи.Children[0].Subtitle)
	}
	if len(связи.Children[0].Actions) == 0 || связи.Children[0].Actions[0].Target != "memory:node:"+strconv.FormatInt(узел, 10) {
		t.Errorf("из связи не попасть на узел: %+v", связи.Children[0].Actions)
	}

	// решения о записи принимаются и здесь, а не только в списке
	var кнопки []string
	for _, c := range doc.Components {
		if c.Type == "button" {
			for _, a := range c.Actions {
				кнопки = append(кнопки, a.Action)
			}
		}
	}
	for _, want := range []string{"accept_memory", "fix_memory", "forget_memory"} {
		if !strings.Contains(strings.Join(кнопки, " "), want) {
			t.Errorf("на экране нет действия %s: %v", want, кнопки)
		}
	}
}

// Голая запись — без сессии, привязок к коду, прежних версий и узлов
// графа — тоже собирается, и пустые места объясняют себя, а не молчат.
func TestRecordScreenWithoutPlacesAndHistory(t *testing.T) {
	s, st := testServer(t)
	id, err := st.SaveMemory(&store.Memory{Kind: store.MemFact, Area: "личное",
		Text: "Вадим пьёт кофе без сахара", Agent: "Сёма-чат"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("memory:rec:"+strconv.FormatInt(id, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Heading, "без сахара") {
		t.Errorf("в заголовке нет текста записи: %q", doc.Heading)
	}
	for _, name := range []string{"История версий", "Место в коде", "Кристалл и связи"} {
		section := sectionByPrefix(doc.Components, name)
		if section == nil {
			t.Fatalf("нет раздела «%s»: %v", name, sectionTitles(doc.Components))
		}
		if len(section.Children) != 1 {
			t.Fatalf("в «%s» %d строк, ждали одно пустое место", name, len(section.Children))
		}
		empty := section.Children[0]
		if empty.Style != "empty" {
			t.Errorf("в «%s» вместо пустого места %+v", name, empty)
		}
		if empty.Caption == "" {
			t.Errorf("пустое место в «%s» не объясняет себя", name)
		}
	}
}

// Строка записи в списке ведёт на её экран, и прежние действия при этом
// остались на месте.
func TestRecordRowLeadsToRecordScreen(t *testing.T) {
	s, st := testServer(t)
	id, err := st.SaveMemory(&store.Memory{Kind: store.MemLesson, Area: "код",
		Text: "Список строк раздела нельзя переиспользовать срезом", Agent: "Сёма-чат"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("memory", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var строка *Component
	for _, r := range allKinds(doc.Components, "row") {
		if r.ID == "mem-"+strconv.FormatInt(id, 10) {
			к := r
			строка = &к
		}
	}
	if строка == nil {
		t.Fatal("записи нет в списке")
	}
	открыть := false
	for _, a := range append(append([]Action{}, строка.Actions...), строка.Menu...) {
		if a.Action == "$goto" && a.Target == "memory:rec:"+strconv.FormatInt(id, 10) {
			открыть = true
		}
	}
	if !открыть {
		t.Errorf("из строки не попасть на экран записи: %+v", строка.Actions)
	}
	before := rowActions(*строка)
	for _, want := range []string{"fix_memory", "forget_memory", "accept_memory"} {
		if !strings.Contains(strings.Join(before, " "), want) {
			t.Errorf("действие %s пропало из строки: %v", want, before)
		}
	}
}

func sectionTitles(cs []Component) []string {
	var out []string
	for i := range cs {
		if cs[i].Type == "section" {
			out = append(out, cs[i].Title)
		}
		out = append(out, sectionTitles(cs[i].Children)...)
	}
	return out
}

// sectionByPrefix — раздел, чей заголовок начинается с имени: у
// длинных списков к заголовку приписывается счётчик.
func sectionByPrefix(cs []Component, name string) *Component {
	for i := range cs {
		if cs[i].Type == "section" && strings.HasPrefix(cs[i].Title, name) {
			return &cs[i]
		}
		if c := sectionByPrefix(cs[i].Children, name); c != nil {
			return c
		}
	}
	return nil
}
