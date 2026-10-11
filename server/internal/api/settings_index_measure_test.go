package api

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Замер сборки индекса настроек по разделам (perf-pass 1.4). Порогов нет:
// это прибор, а не сторож. По CLAUDE.md индекс собирался за 90 мс, а 3
// октября 2026 — за 1,2 с на Pi и 1,7 с на маке; чтобы понять, какой
// раздел подорожал, его сборку надо разложить. Обход — тот же, что у
// buildSettingsIndex (раздел, его вкладки, его сущности), с тем же общим
// запасом карточек моделей. Читать с -v:
//
//	go test ./internal/api -run TestSettingsIndexCostBySection -v
func TestSettingsIndexCostBySection(t *testing.T) {
	s, _ := indexFix(t, 173)
	start := time.Now()
	shared := &indexShared{cards: map[string]engine.ModelCard{}, roles: s.eng.Roles()}
	for _, c := range s.eng.ModelCards() {
		shared.cards[c.Name] = c
	}
	t.Logf("общий запас (карточки моделей, роли) %.1f мс", ms64(time.Since(start)))
	b := &indexBuilder{s: s, idx: &settingsIndex{At: start,
		byKey: map[string][]*indexEntry{}, byAction: map[string][]*indexEntry{}}, visited: map[string]bool{}, shared: shared}
	type cost struct {
		d       time.Duration
		screens int
		sql     int
	}
	costs := map[string]*cost{}
	var names []string
	for _, group := range settingsGroups {
		for _, section := range group.Sections {
			before := b.idx.Screens
			tr := store.TraceSQL()
			t0 := time.Now()
			b.section(group, section)
			costs[section] = &cost{d: time.Since(t0), screens: b.idx.Screens - before, sql: tr.Stats().N}
			tr.Stop()
			names = append(names, section)
		}
	}
	var total time.Duration
	for _, c := range costs {
		total += c.d
	}
	sort.Slice(names, func(i, j int) bool { return costs[names[i]].d > costs[names[j]].d })
	for _, k := range names {
		c := costs[k]
		per := 0.0
		if c.screens > 0 {
			per = ms64(c.d) / float64(c.screens)
		}
		t.Logf("%-24s %3d экранов %8.1f мс (%6.2f мс/экран, %5d запросов)",
			strings.TrimPrefix(k, "settings:"), c.screens, ms64(c.d), per, c.sql)
	}
	t.Logf("разделы %.1f мс, %d экранов; индекс целиком %.1f мс", ms64(total), b.idx.Screens, ms64(timeOf(func() { s.buildSettingsIndex() })))
}

func ms64(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func timeOf(f func()) time.Duration {
	start := time.Now()
	f()
	return time.Since(start)
}
