package api

import (
	"sort"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Колонка разделов стоит на каждом экране настроек, и её счётчики собираются
// из всех девятнадцати разделов «Обзора». 5 октября 2026 на Pi это было
// около 440 запросов к базе на любой экран настроек (корень — 463, 97 мс из
// 145): разделы считали своё строка за строкой. Тест держит число запросов
// на данных живого размера и называет раздел, который его съедает.
func TestSettingsColumnQueryBudget(t *testing.T) {
	s, _ := indexFix(t, 173)
	sections := map[string]func() settingsSection{
		"mail": s.overviewMail, "telegram": s.overviewTelegram, "max": s.overviewMAX, "translate": s.overviewTranslate,
		"notify": s.overviewNotifications, "models": s.overviewModels, "search": s.overviewSearch, "skills": s.overviewSkills,
		"learning": s.overviewLearning, "tools": s.overviewTools, "hosts": s.overviewHosts, "agents": s.overviewAgents,
		"integrations": s.overviewIntegrations, "storages": s.overviewStorages, "watch": s.overviewWatch,
		"access": s.overviewAccess, "secrets": s.overviewSecrets, "sent": s.overviewSent, "log": s.overviewSettingsLog,
	}
	// первый проход прогревает кэши движка, считается второй
	for _, f := range sections {
		f()
	}
	type cost struct {
		name string
		n    int
	}
	var costs []cost
	total := 0
	for name, f := range sections {
		tr := store.TraceSQL()
		f()
		n := tr.Stats().N
		tr.Stop()
		costs = append(costs, cost{name, n})
		total += n
	}
	sort.Slice(costs, func(i, j int) bool { return costs[i].n > costs[j].n })
	for _, c := range costs {
		t.Logf("%-14s %4d запросов", c.name, c.n)
	}
	t.Logf("все разделы: %d запросов", total)
	// раздел не должен расти с числом моделей, серверов, секретов: на 173
	// моделях «Модели и роли» делали 182 запроса, по одному на модель
	for _, c := range costs {
		if c.n > 25 {
			t.Errorf("раздел «%s» делает %d запросов — похоже, по запросу на строку", c.name, c.n)
		}
	}
	if total > 100 {
		t.Errorf("колонка разделов стоит %d запросов на каждом экране настроек", total)
	}

	for _, addr := range []string{"settings", "settings:mail", "settings:models", "settings:models~providers",
		"settings:models~issues", "settings:models~list", "settings:servers", "settings:notify", "settings:watch",
		"settings:max", "settings:integrations", "settings:models@provider:DeepSeek~prices",
		"settings:mail@owner@example.com"} {
		_ = listMarkup(t, s, addr)
		tr := store.TraceSQL()
		_ = listMarkup(t, s, addr)
		n := tr.Stats().N
		tr.Stop()
		t.Logf("экран %-42s %4d запросов", addr, n)
		if n > 200 {
			t.Errorf("экран %s делает %d запросов к базе (до 5 октября 2026 было 300–460)", addr, n)
		}
	}
}
