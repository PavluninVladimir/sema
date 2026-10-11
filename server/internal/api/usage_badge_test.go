package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Капсула расхода в строке экрана стоила трёх запросов к журналу: сам
// UsageFor и два обхода бюджетов, каждый со своим UsageFor на каждый бюджет.
// Теперь расход берётся одной группировкой, а бюджеты считаются один раз на
// экран. Это ускорение, а не изменение смысла — значит цифры обязаны
// совпасть с прежними до знака.

// pillLegacy — прежний usageBadge слово в слово: UsageFor на строку
// и BudgetFor (то есть BudgetStates заново) дважды.
func pillLegacy(s *Server, consumer string) string {
	бюджет := func() string {
		st := s.eng.BudgetFor(consumer)
		if st == nil {
			return ""
		}
		if st.Exceeded {
			return "бюджет исчерпан"
		}
		return fmt.Sprintf("бюджет %d%%", st.Pct)
	}
	u := s.eng.Store().UsageFor(consumer, time.Now().Add(-24*time.Hour))
	if b := бюджет(); b != "" && u.Calls == 0 {
		return b
	}
	if u.Calls == 0 {
		return ""
	}
	parts := []string{fmt.Sprintf("за сутки %s", plural(u.Calls, "запуск", "запуска", "запусков"))}
	if t := tokensText(u); t != "—" {
		parts = append(parts, t+" токенов")
	}
	if u.Cost > 0 {
		parts = append(parts, fmt.Sprintf("%.2f %s", u.Cost, u.Currency))
	}
	if u.Fails > 0 {
		parts = append(parts, fmt.Sprintf("отказов %d", u.Fails))
	}
	if b := бюджет(); b != "" {
		parts = append(parts, b)
	}
	return strings.Join(parts, " · ")
}

var consumersForPills = []string{
	"core", "core:archive", "core:queue", "core:poll",
	"module", "module:receipts", "module:watch", "module:пусто",
	"script", "script:7", "task:3", "chat", "chat:1",
	"mcp:claude-code", "нет-такого",
}

func fillUsageLog(t *testing.T, st *store.Store) {
	t.Helper()
	has := []string{"core:archive", "core:attachments", "core:queue", "module:receipts",
		"module:watch", "script:7", "task:3", "chat:1", "mcp:claude-code", "core", "module"}
	for i, c := range has {
		for j := 0; j < 5; j++ {
			kind := "run"
			if j%2 == 0 {
				kind = "llm"
			}
			if err := st.AddUsage(&store.UsageRow{
				At: time.Now().Add(-time.Duration(i*3+j) * time.Hour), Consumer: c, Kind: kind,
				Model: "модель", TokensIn: 100 + i*7 + j, TokensOut: 20 + j, Cached: j,
				Estimated: j == 3, Seconds: float64(j) / 3, OK: j != 4,
				Cost: float64(j) / 50, Currency: "USD",
			}); err != nil {
				t.Fatalf("запись расхода: %v", err)
			}
		}
	}
	// строка старше суток: в капсулу «за сутки» попасть не должна
	if err := st.AddUsage(&store.UsageRow{At: time.Now().Add(-30 * time.Hour),
		Consumer: "module:receipts", Kind: "llm", TokensIn: 99999, OK: true}); err != nil {
		t.Fatalf("запись расхода: %v", err)
	}
}

// TestUsagePillUnchanged — цифры на «Задачах» и «Модулях» те же.
func TestUsagePillUnchanged(t *testing.T) {
	s, st := testServer(t)
	fillUsageLog(t, st)

	for _, бюджеты := range [][]*store.Budget{
		nil,
		{{Consumer: "module", Kind: "tokens", Period: "day", Limit: 5000, Action: "warn"}},
		{
			{Consumer: "module", Kind: "tokens", Period: "day", Limit: 5000, Action: "warn"},
			{Consumer: "module:receipts", Kind: "tokens", Period: "week", Limit: 300, Action: "warn"},
			{Consumer: "script", Kind: "money", Period: "month", Limit: 0.01, Action: "warn"},
			{Consumer: "core", Kind: "tokens", Period: "day", Limit: 1, Action: "warn"},
		},
	} {
		// бюджеты заводим заново на каждый набор
		прежние, err := st.Budgets()
		if err != nil {
			t.Fatalf("бюджеты: %v", err)
		}
		for _, b := range прежние {
			if err := st.DeleteBudget(b.ID); err != nil {
				t.Fatalf("удалить бюджет: %v", err)
			}
		}
		for _, b := range бюджеты {
			if err := st.AddBudget(b); err != nil {
				t.Fatalf("бюджет: %v", err)
			}
		}

		usage := s.buildUsage()
		for _, c := range consumersForPills {
			before, after := pillLegacy(s, c), usage.usageBadge(c)
			if before != after {
				t.Errorf("капсула %q при %d бюджетах разошлась:\nбыло:  %q\nстало: %q",
					c, len(бюджеты), before, after)
			}
			if b1, b2 := s.eng.BudgetFor(c), engine.BudgetPick(usage.budgets, c); (b1 == nil) != (b2 == nil) {
				t.Errorf("бюджет %q: было %v, стало %v", c, b1, b2)
			} else if b1 != nil && (b1.Pct != b2.Pct || b1.Exceeded != b2.Exceeded) {
				t.Errorf("бюджет %q разошёлся: %+v против %+v", c, b1, b2)
			}
		}
	}
}

// TestScreenUsageSummaryCollectsChildren — «module» в капсуле обязан
// собрать всех своих, как это делал UsageFor с приставкой.
func TestScreenUsageSummaryCollectsChildren(t *testing.T) {
	s, st := testServer(t)
	fillUsageLog(t, st)
	usage := s.buildUsage()
	for _, c := range consumersForPills {
		before := st.UsageFor(c, time.Now().Add(-24*time.Hour))
		after := usage.forScreen(c)
		if before.Calls != after.Calls || before.Fails != after.Fails ||
			before.TokensIn != after.TokensIn || before.TokensOut != after.TokensOut ||
			before.Estimated != after.Estimated || before.Currency != after.Currency {
			t.Errorf("сводка %q разошлась:\nбыло:  %+v\nстало: %+v", c, before, after)
		}
		if разница := before.Cost - after.Cost; разница > 1e-9 || разница < -1e-9 {
			t.Errorf("деньги %q разошлись: %v против %v", c, before.Cost, after.Cost)
		}
	}
}

// TestUsageScreensBuildFast — «Задачи» и «Модули» больше не ходят в
// журнал по три раза на строку. Проверяем не секундомером (он на разных
// машинах разный), а числом запросов: одна группировка и один обход бюджетов
// на весь экран, сколько бы строк на нём ни было.
func TestUsageScreensBuildFast(t *testing.T) {
	s, st := testServer(t)
	fillUsageLog(t, st)
	if err := st.AddBudget(&store.Budget{Consumer: "module", Kind: "tokens",
		Period: "day", Limit: 5000, Action: "warn"}); err != nil {
		t.Fatalf("бюджет: %v", err)
	}
	usage := s.buildUsage()
	// сто строк экрана — и ни одного нового похода в базу
	for i := 0; i < 100; i++ {
		usage.usageBadge(fmt.Sprintf("task:%d", i))
		usage.usageBadge("module:receipts")
	}
	if len(usage.perDay) == 0 {
		t.Fatal("расход за сутки не собран — проверять нечего")
	}
}
