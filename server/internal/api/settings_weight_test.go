package api

// Вес и время Обзора настроек: узлы, JSON, разметка, сборка. Числа идут в журнал
// теста и в отчёт (замер «до» и «после» — на одних и тех же данных: сто
// семьдесят три модели, пять серверов, двадцать пять секретов, журнал с правками
// человека и служебными отметками). Предел щедрый: он ловит порядок величины, а не
// скорость машины.

import (
	"sort"
	"testing"
	"time"
)

func countNodes(cs []Component) int {
	n := 0
	for _, c := range cs {
		n += 1 + len(c.Fields) + len(c.Facts) + len(c.Actions) + len(c.Badges) + countNodes(c.Children)
	}
	return n
}

// overviewWeight — вес Обзора; время — медиана из пяти сборок с уже собранным индексом
// и сборки «с холода».
func overviewWeight(t *testing.T, s *Server) (nodes, jsonBytes, htmlBytes int, cold, warm time.Duration) {
	t.Helper()
	started := time.Now()
	doc, err := s.buildScreen("settings", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	cold = time.Since(started)
	var times []time.Duration
	for i := 0; i < 5; i++ {
		started := time.Now()
		if doc, err = s.buildScreen("settings", "", "", 0); err != nil {
			t.Fatal(err)
		}
		times = append(times, time.Since(started))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return countNodes(doc.Components), len(dumpScreen(doc)), len(stnMarkup(t, doc)), cold, times[2]
}

func TestOverviewWeight(t *testing.T) {
	s, st := indexFix(t, 173)
	indexJournal(t, st)
	nodes, js, markup, cold, warm := overviewWeight(t, s)
	t.Logf("Обзор: %d узлов, JSON %d Б, разметка %d Б; сборка с холода %s, потом %s",
		nodes, js, markup, cold.Round(time.Microsecond), warm.Round(time.Microsecond))
	if nodes > 400 || js > 120_000 || markup > 160_000 {
		t.Errorf("Обзор потяжелел: %d узлов, %d Б JSON, %d Б разметки", nodes, js, markup)
	}
	if cold > 2*time.Second {
		t.Errorf("Обзор с холода собирается %s", cold)
	}
}
