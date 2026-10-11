package engine

import (
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Бюджеты считаются один раз на сборку экрана, а не заново для каждой
// строки. Отвечать они обязаны то же самое — иначе капсула «бюджет 80 %»
// поедет, а по ней человек решает, выключать ли модуль.
func TestBudgetFromReadyStates(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	budgets := []*store.Budget{
		{Consumer: "module", Kind: "tokens", Period: "day", Limit: 500, Action: "warn"},
		{Consumer: "module:receipts", Kind: "tokens", Period: "day", Limit: 100, Action: "warn"},
		{Consumer: "script", Kind: "money", Period: "month", Limit: 0.05, Action: "warn"},
		{Consumer: "core", Kind: "tokens", Period: "week", Limit: 10000, Action: "warn"},
	}
	for _, b := range budgets {
		if err := st.AddBudget(b); err != nil {
			t.Fatalf("бюджет: %v", err)
		}
	}
	for _, c := range []string{"module:receipts", "module:watch", "script:7", "task:3", "core:poll", "chat:1"} {
		for j := 0; j < 4; j++ {
			if err := st.AddUsage(&store.UsageRow{At: time.Now().Add(-time.Duration(j) * time.Hour),
				Consumer: c, Kind: "llm", TokensIn: 40 + j*10, TokensOut: 5, OK: true,
				Cost: 0.02, Currency: "USD"}); err != nil {
				t.Fatalf("расход: %v", err)
			}
		}
	}

	states := e.BudgetStates()
	for _, c := range []string{"module:receipts", "module:watch", "script:7", "task:3",
		"core:poll", "chat:1", "нет-такого"} {
		was, became := e.BudgetFor(c), BudgetPick(states, c)
		if (was == nil) != (became == nil) {
			t.Fatalf("бюджет %q: было %v, стало %v", c, was, became)
		}
		if was == nil {
			continue
		}
		if was.Budget.ID != became.Budget.ID || was.Pct != became.Pct ||
			was.Exceeded != became.Exceeded || was.Spent != became.Spent {
			t.Errorf("бюджет %q разошёлся:\nбыло:  %+v\nстало: %+v", c, was, became)
		}
	}
}

// Срок хранения отметок о запусках — настройка, а не константа: их пишет
// каждый проход фонового воркера, и жить они должны заметно меньше журнала
// вызовов модели.
func TestDueMarksAboutRunsConfigurable(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if d := e.UsageKeepRunDays(); d != healthKeepRunDays {
		t.Errorf("по умолчанию %d дней, ждали %d", d, healthKeepRunDays)
	}
	if e.UsageKeepRunDays() >= e.UsageKeepDays() {
		t.Errorf("отметки о запусках живут не меньше вызовов модели: %d против %d",
			e.UsageKeepRunDays(), e.UsageKeepDays())
	}
	if err := st.SetSetting(HealthKeepRunsKey, "3"); err != nil {
		t.Fatalf("настройка: %v", err)
	}
	if d := e.UsageKeepRunDays(); d != 3 {
		t.Errorf("настройка не подхвачена: %d", d)
	}
	if err := st.SetSetting(HealthKeepRunsKey, "чепуха"); err != nil {
		t.Fatalf("настройка: %v", err)
	}
	if d := e.UsageKeepRunDays(); d != healthKeepRunDays {
		t.Errorf("на мусор в настройке ждали умолчание, получили %d", d)
	}
}
