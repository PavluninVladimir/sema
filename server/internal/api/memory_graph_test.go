package api

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Граф памяти «не работал» (5 октября 2026): узел на рисунке и строка
// «Списком» никуда не вели — адрес экрана узла терялся по дороге в шаблон;
// подпись делала первую букву заглавной, и разные узлы «pi» и «Pi»
// выглядели дублями; заголовок обещал «показаны 200», а на рисунке было 44.
// А экран узла читал все связи заново и шёл в базу за каждым соседом и
// каждой записью — 243 запроса на Pi.
func TestMemoryGraphNodesLeadToTheirScreens(t *testing.T) {
	s, st := testServer(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	центр, err := st.EnsureMemoryNode("сущность", "Сёма", "код")
	must(err)
	piwg, err := st.EnsureMemoryNode("сущность", "pi-wg", "код")
	must(err)
	// у центра — шестьдесят один сосед, каждый из своей записи: экран узла не
	// должен ходить в базу за каждым
	for i := 0; i < 60; i++ {
		id, err := st.SaveMemory(&store.Memory{Kind: store.MemFact, Area: "код", Text: fmt.Sprintf("факт номер %d про Сёму", i), Agent: "тест"})
		must(err)
		сосед, err := st.EnsureMemoryNode("сущность", fmt.Sprintf("сосед с очень длинным именем %02d", i), "код")
		must(err)
		must(st.AddMemoryEdge(центр, сосед, "знает", id))
	}
	must(st.AddMemoryEdge(центр, piwg, "живёт на", 0))

	page := listMarkup(t, s, "memory:graph")
	if !strings.Contains(page, `data-goto="memory:node:`+strconv.FormatInt(центр, 10)+`"`) {
		t.Error("узел на рисунке не ведёт на свой экран")
	}
	if !strings.Contains(page, "<title>сосед с очень длинным именем 01 · код</title>") {
		t.Error("полного имени узла не видно при наведении")
	}
	if !strings.Contains(page, ">pi-wg</text>") {
		t.Error("подпись узла меняет регистр — «pi» и «Pi» выглядели одним и тем же")
	}
	doc, err := s.buildScreen("memory:graph", "", "", 0)
	must(err)
	if strings.Contains(doc.Subheading, "показаны 62") || !strings.Contains(doc.Subheading, "на рисунке 44") {
		t.Errorf("подзаголовок расходится с рисунком: %q", doc.Subheading)
	}
	// экран слабосвязанного узла рисует его самого — и в середине: раньше
	// рисунок брал 44 самых связанных, и выбранного на нём не было
	слабый, ok := st.MemoryNodeByName("сущность", "сосед с очень длинным именем 59")
	if !ok {
		t.Fatal("нет узла соседа 59")
	}
	nodePage := listMarkup(t, s, "memory:node:"+strconv.FormatInt(слабый.ID, 10))
	if !strings.Contains(nodePage, `class="node selected" data-goto="memory:node:`+strconv.FormatInt(слабый.ID, 10)+`"`) {
		t.Error("на экране узла его самого нет на рисунке")
	}
	// «Списком» — те же переходы
	if !strings.Contains(page, `class="row`) || strings.Count(page, `data-goto="memory:node:`) < 2*graphDrawn {
		t.Errorf("строки «Списком» не ведут на экраны узлов: переходов %d", strings.Count(page, `data-goto="memory:node:`))
	}

	// экран узла: число запросов не растёт с числом соседей и записей —
	// сверх самого графа карточка узла стоит считанных запросов
	tr0 := store.TraceSQL()
	_ = listMarkup(t, s, "memory:graph")
	base := tr0.Stats().N
	tr0.Stop()
	addr := "memory:node:" + strconv.FormatInt(центр, 10)
	_ = listMarkup(t, s, addr)
	tr := store.TraceSQL()
	tr.Keep = true
	page = listMarkup(t, s, addr)
	n := tr.Stats().N
	tr.Stop()
	t.Logf("граф — %d запросов, экран узла — %d", base, n)
	// цвет области просили на каждый узел — запрос к областям на узел
	if base > 45 {
		t.Errorf("граф из 63 узлов делает %d запросов к базе — по запросу на узел", base)
	}
	if n > base+10 {
		often := map[string]int{}
		for _, q := range tr.Queries() {
			often[q]++
		}
		var lines []string
		for q, k := range often {
			if k > 1 {
				lines = append(lines, fmt.Sprintf("%d× %.140s", k, q))
			}
		}
		t.Errorf("экран узла с 62 соседями делает %d запросов к базе при %d у графа — по запросу на соседа или запись:\n%s", n, base, strings.Join(lines, "\n"))
	}
	if !strings.Contains(page, "факт номер 7 про Сёму") {
		t.Error("на экране узла нет записи-источника связи")
	}
}

// Слияние с экрана по имени без учёта регистра выбирает узел своей области,
// а двусмысленность называет, а не решает молча: узел чужой области принял
// бы связи, которые там видны не тем.
func TestMergeTargetPrefersOwnArea(t *testing.T) {
	_, st := testServer(t)
	pi, _ := st.EnsureMemoryNode("сущность", "pi", "код")
	Pi, _ := st.EnsureMemoryNode("сущность", "Pi", "личное")
	изКода, _ := st.EnsureMemoryNode("сущность", "дубль", "код")
	изРаботы, _ := st.EnsureMemoryNode("сущность", "другой дубль", "работа")
	if n, err := mergeTarget(st, изКода, "PI"); err != nil || n.ID != pi {
		t.Errorf("из «кода» «PI» должен найти «pi» своей области: %+v %v", n, err)
	}
	if n, err := mergeTarget(st, изКода, "Pi"); err != nil || n.ID != Pi {
		t.Errorf("точное имя — тот самый узел: %+v %v", n, err)
	}
	if _, err := mergeTarget(st, изРаботы, "PI"); err == nil || !strings.Contains(err.Error(), "несколько") {
		t.Errorf("двусмысленность решена молча: %v", err)
	}
}
