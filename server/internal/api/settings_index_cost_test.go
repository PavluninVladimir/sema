package api

import (
	"strings"
	"testing"
	"time"
)

// Сколько стоит каждый раздел в сборке индекса: число экранов и миллисекунды на
// экран. Печатается в журнал теста, ничего не ограничивает — по нему видно, что
// съело бы время на машине послабее (на Raspberry Pi всё в несколько раз
// медленнее).
func TestIndexCostPerSection(t *testing.T) {
	s, _ := indexFix(t, 173)
	shared := &indexShared{roles: s.eng.Roles()}
	shared.cards = map[string]engineCard{}
	for _, c := range s.eng.ModelCards() {
		shared.cards[c.Name] = c
	}
	for _, group := range settingsGroups {
		for _, section := range group.Sections {
			started := time.Now()
			screens := 1
			s.buildIndexDoc(section, shared)
			for _, e := range s.indexEntities(section) {
				if e.Walk {
					s.buildIndexDoc(e.Addr, shared)
					screens++
				}
			}
			spent := time.Since(started)
			t.Logf("%-28s %4d экранов %8s (%.2f мс на экран)", strings.TrimPrefix(section, "settings:"), screens,
				spent.Round(time.Microsecond), float64(spent.Microseconds())/1000/float64(screens))
		}
	}
}
