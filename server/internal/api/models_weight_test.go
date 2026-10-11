package api

// Вес раздела «Модели и роли» на данных настоящего размера: ~170 моделей у
// нескольких провайдеров. До переделки раздел был самым тяжёлым в настройках
// (замер 11 сентября 2026: 3597 узлов, 319 КБ); здесь число меряется на
// сборке, какой она уходит в браузер, и сравнивается с порогом.

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// modelsTimed — узлы, байты и время сборки с отрисовкой: медиана из пяти.
func modelsTimed(t *testing.T, s *Server, screen string) (screenWeight, time.Duration) {
	t.Helper()
	var times []time.Duration
	var w screenWeight
	for i := 0; i < 5; i++ {
		started := time.Now()
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatalf("%s: разметка: %v", screen, err)
		}
		times = append(times, time.Since(started))
		w = screenWeight{Screen: screen, Nodes: nodeCount(string(markup)), Bytes: len(markup)}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return w, times[len(times)/2]
}

// modelsWeightScreens — вкладки раздела: роли, список, провайдеры, проблемы.
func modelsWeightScreens() []string {
	return []string{modelsScreen, modelsScreen + "~list", modelsScreen + "~providers", modelsScreen + "~issues",
		modelAddr("gpt-6-luna"), providerAddr("OpenAI", ""), providerAddr("OpenAI", providerTabPrices),
		entityAddr(modelsScreen, modelsNew, "")}
}

func TestModelsWeight(t *testing.T) {
	s, _ := modelsFix(t, 173)
	var out strings.Builder
	fmt.Fprintf(&out, "\nмоделей %d\n%-34s %7s %8s %10s\n", len(s.eng.ProviderNames()), "экран", "узлов", "КБ", "сборка")
	for _, screen := range modelsWeightScreens() {
		w, took := modelsTimed(t, s, screen)
		fmt.Fprintf(&out, "%-34s %7d %8.1f %10s\n", screen, w.Nodes, float64(w.Bytes)/1024, took.Round(time.Millisecond))
		out.WriteString(parseWeight(t, s, screen))
	}
	t.Log(out.String())
}
