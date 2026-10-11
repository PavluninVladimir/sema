package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Журнал расхода: сводка потребителя должна брать индекс, а не читать все
// строки за срок, и при этом отвечать ровно то же, что отвечала раньше.
// Второе важнее первого: капсула «за сутки 42 запуска» — это цифра, которую
// человек читает, и ускорение не имеет права её сдвинуть.

// sumByOldQuery — прежний запрос UsageFor слово в слово. Он и есть
// эталон: сравниваем не с пересказом правила, а с тем, что работало.
func sumByOldQuery(t *testing.T, st *Store, consumerPrefix string, since time.Time) *UsageSum {
	t.Helper()
	u := &UsageSum{Key: consumerPrefix}
	var est int
	var last string
	err := st.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN ok = 0 THEN 1 ELSE 0 END),0), COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0),
		COALESCE(MAX(estimated),0), COALESCE(SUM(seconds),0), COALESCE(SUM(cost),0), COALESCE(MAX(currency),''), COALESCE(MAX(at),'')
		FROM usage WHERE at >= ? AND (consumer = ? OR consumer LIKE ?)`,
		since.Format(time.RFC3339), consumerPrefix, consumerPrefix+":%").
		Scan(&u.Calls, &u.Fails, &u.TokensIn, &u.TokensOut, &est, &u.Seconds, &u.Cost, &u.Currency, &last)
	if err != nil {
		t.Fatalf("старый запрос: %v", err)
	}
	u.Estimated = est == 1
	u.LastAt = parseTime(last)
	return u
}

func sumPrint(u *UsageSum) string {
	return fmt.Sprintf("вызовов=%d отказов=%d вход=%d выход=%d оценка=%v секунд=%.3f деньги=%.4f валюта=%q последняя=%s",
		u.Calls, u.Fails, u.TokensIn, u.TokensOut, u.Estimated, u.Seconds, u.Cost, u.Currency,
		u.LastAt.UTC().Format(time.RFC3339))
}

// fillUsage — журнал, похожий на живой: фоновые воркеры отметками
// «run», вызовы моделей «llm», у части — деньги, оценка и отказы.
func fillUsage(t *testing.T, st *Store) time.Time {
	t.Helper()
	base := time.Now().Add(-40 * 24 * time.Hour)
	consumers := []string{
		"core", "core:archive", "core:attachments", "core:queue", "core:poll",
		"module:receipts", "module:watch", "module",
		"script:7", "script:12", "script", "task:3",
		"chat:1", "chat:2", "chat",
		"mcp:claude-code",
		// соседи по алфавиту: «core-прочее» и «cores» под приставку «core:»
		// попадать не должны, а «core» точным именем — должен
		"core-прочее", "cores", "coreX", "modules",
	}
	for i, c := range consumers {
		for j := 0; j < 7; j++ {
			kind := "run"
			if j%3 == 0 {
				kind = "llm"
			}
			r := &UsageRow{
				At:        base.Add(time.Duration(i*7+j) * time.Hour),
				Consumer:  c,
				Kind:      kind,
				Model:     fmt.Sprintf("модель-%d", i%3),
				TokensIn:  100 + i*3 + j,
				TokensOut: 10 + j,
				Cached:    j,
				Estimated: j == 4,
				Seconds:   float64(j) / 4,
				OK:        j != 5,
				Cost:      float64(j) / 100,
				Currency:  "USD",
			}
			if err := st.AddUsage(r); err != nil {
				t.Fatalf("запись расхода: %v", err)
			}
		}
	}
	return base
}

// TestUsageSummaryUnchanged — новый запрос отвечает то же, что старый,
// для каждого потребителя и каждого вида.
func TestUsageSummaryUnchanged(t *testing.T) {
	st := testStore(t)
	base := fillUsage(t, st)

	for _, since := range []time.Time{
		base.Add(-time.Hour),          // весь журнал
		base.Add(48 * time.Hour),      // хвост
		time.Now().Add(time.Hour),     // пусто
		base.Add(1000 * time.Hour),    // тоже пусто
		base.Add(3*time.Hour + 30*60), // между записями
	} {
		for _, c := range []string{
			"core", "module", "script", "chat", "task", "mcp",
			"module:receipts", "core:archive", "script:7", "chat:2",
			"нет-такого", "core-прочее", "cores",
		} {
			before := sumByOldQuery(t, st, c, since)
			after := st.UsageFor(c, since)
			if a, b := sumPrint(before), sumPrint(after); a != b {
				t.Errorf("сводка %q с %s разошлась:\nбыло:  %s\nстало: %s",
					c, since.Format(time.RFC3339), a, b)
			}
		}
	}
}

// TestKindSummaryCollectsAllItsOwn — «вид» (module/script/chat/core) — это не
// украшение: по нему считаются бюджеты. Он обязан собрать и точное имя, и
// всех, кто под ним, и не прихватить соседа по алфавиту.
func TestKindSummaryCollectsAllItsOwn(t *testing.T) {
	st := testStore(t)
	longAgo := time.Now().Add(-48 * time.Hour)
	add := func(consumer string, tokens int) {
		if err := st.AddUsage(&UsageRow{At: time.Now().Add(-time.Hour), Consumer: consumer,
			Kind: "llm", TokensIn: tokens, OK: true}); err != nil {
			t.Fatalf("запись: %v", err)
		}
	}
	add("module", 1)          // сам вид
	add("module:receipts", 2) // под ним
	add("module:watch", 4)
	add("module:a:b", 8) // глубже — тоже под ним
	add("modules", 16)   // сосед: не под «module:»
	add("module-мой", 32)
	add("chat:1", 64)

	u := st.UsageFor("module", longAgo)
	if u.TokensIn != 1+2+4+8 {
		t.Errorf("вид «module» собрал %d токенов, ждали %d", u.TokensIn, 1+2+4+8)
	}
	if u.Calls != 4 {
		t.Errorf("вид «module» собрал %d вызовов, ждали 4", u.Calls)
	}
	if one := st.UsageFor("module:receipts", longAgo); one.TokensIn != 2 {
		t.Errorf("точное имя собрало %d токенов, ждали 2", one.TokensIn)
	}
}

// TestUsageSummaryUsesIndex — то, ради чего всё затевалось: индекс
// idx_usage_consumer(consumer, at) должен работать. Без него SQLite читает
// все строки за срок, и каждая капсула на экране стоит миллисекунд.
func TestUsageSummaryUsesIndex(t *testing.T) {
	st := testStore(t)
	fillUsage(t, st)
	since := time.Now().Add(-365 * 24 * time.Hour).Format(time.RFC3339)

	plan := queryPlan(t, st, sqlUsageFor, "module", since, "module:", "module;", since)
	t.Logf("план: %v", plan)
	if planScansTable(plan, "usage") {
		t.Errorf("в плане остался полный проход по журналу расхода: %v", plan)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "idx_usage_consumer") {
		t.Errorf("индекс idx_usage_consumer не используется: %v", plan)
	}
}

// TestWeedingKeepsModelCalls — отметки о запусках живут своим,
// коротким сроком, а строки «llm» им не задеваются: по ним считаются деньги.
func TestWeedingKeepsModelCalls(t *testing.T) {
	st := testStore(t)
	add := func(kind string, days int) {
		if err := st.AddUsage(&UsageRow{At: time.Now().Add(-time.Duration(days) * 24 * time.Hour),
			Consumer: "core:archive", Kind: kind, TokensIn: 1, OK: true}); err != nil {
			t.Fatalf("запись: %v", err)
		}
	}
	for _, days := range []int{1, 10, 20, 60, 100} {
		add("run", days)
		add("llm", days)
	}
	count := func(kind string) int {
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM usage WHERE kind = ?`, kind).Scan(&n); err != nil {
			t.Fatalf("счёт: %v", err)
		}
		return n
	}
	if count("run") != 5 || count("llm") != 5 {
		t.Fatalf("до прополки run=%d llm=%d", count("run"), count("llm"))
	}

	st.PruneUsage(90*24*time.Hour, 14*24*time.Hour)

	if n := count("llm"); n != 4 {
		t.Errorf("вызовов модели осталось %d, ждали 4 (уходит только столетний)", n)
	}
	if n := count("run"); n != 2 {
		t.Errorf("отметок о запусках осталось %d, ждали 2 (1 и 10 дней)", n)
	}
	// срок отметок не задан — живут столько же, сколько вызовы модели
	st2 := testStore(t)
	for _, days := range []int{1, 100} {
		if err := st2.AddUsage(&UsageRow{At: time.Now().Add(-time.Duration(days) * 24 * time.Hour),
			Consumer: "core:archive", Kind: "run", OK: true}); err != nil {
			t.Fatalf("запись: %v", err)
		}
	}
	st2.PruneUsage(90*24*time.Hour, 0)
	var n int
	if err := st2.db.QueryRow(`SELECT COUNT(*) FROM usage WHERE kind = 'run'`).Scan(&n); err != nil {
		t.Fatalf("счёт: %v", err)
	}
	if n != 1 {
		t.Errorf("без своего срока отметок осталось %d, ждали 1", n)
	}
}

// TestUsageGroupingUnchanged — «GROUP BY +consumer» отличается от
// «GROUP BY consumer» только планом: ответ обязан остаться прежним.
func TestUsageGroupingUnchanged(t *testing.T) {
	st := testStore(t)
	fillUsage(t, st)
	for _, field := range []string{"consumer", "model"} {
		for _, kind := range []string{"", "llm", "run"} {
			since := time.Now().Add(-365 * 24 * time.Hour)
			after, err := st.UsageBy(field, since, kind)
			if err != nil {
				t.Fatalf("группировка: %v", err)
			}
			q := `SELECT ` + field + `, COUNT(*), SUM(CASE WHEN ok = 0 THEN 1 ELSE 0 END), SUM(tokens_in), SUM(tokens_out),
				MAX(estimated), SUM(seconds), SUM(cost), MAX(currency), MAX(at), COALESCE(SUM(cached), 0) FROM usage WHERE at >= ?`
			args := []any{since.Format(time.RFC3339)}
			if kind != "" {
				q += ` AND kind = ?`
				args = append(args, kind)
			}
			if field == "model" {
				q += ` AND model <> ''`
			}
			q += ` GROUP BY ` + field + ` ORDER BY SUM(tokens_in) + SUM(tokens_out) DESC, COUNT(*) DESC`
			rows, err := st.db.Query(q, args...)
			if err != nil {
				t.Fatalf("старая группировка: %v", err)
			}
			var before []*UsageSum
			for rows.Next() {
				u := &UsageSum{}
				var est int
				var last string
				if err := rows.Scan(&u.Key, &u.Calls, &u.Fails, &u.TokensIn, &u.TokensOut, &est, &u.Seconds, &u.Cost, &u.Currency, &last, &u.Cached); err != nil {
					t.Fatalf("разбор: %v", err)
				}
				u.Estimated, u.LastAt = est == 1, parseTime(last)
				before = append(before, u)
			}
			rows.Close()
			if len(before) != len(after) {
				t.Fatalf("%s/%s: групп было %d, стало %d", field, kind, len(before), len(after))
			}
			for i := range before {
				if before[i].Key != after[i].Key {
					t.Fatalf("%s/%s: порядок групп разошёлся на %d: %q против %q", field, kind, i, before[i].Key, after[i].Key)
				}
				if a, b := sumPrint(before[i]), sumPrint(after[i]); a != b {
					t.Errorf("%s/%s группа %q:\nбыло:  %s\nстало: %s", field, kind, before[i].Key, a, b)
				}
			}
		}
	}
}

// TestUsageGroupingUsesTime — окно «за сутки» не должно стоить
// прохода по всему журналу: план обязан взять индекс по времени.
func TestUsageGroupingUsesTime(t *testing.T) {
	st := testStore(t)
	fillUsage(t, st)
	q := `SELECT consumer, COUNT(*), SUM(CASE WHEN ok = 0 THEN 1 ELSE 0 END), SUM(tokens_in), SUM(tokens_out),
		MAX(estimated), SUM(seconds), SUM(cost), MAX(currency), MAX(at), COALESCE(SUM(cached), 0) FROM usage WHERE at >= ?
		GROUP BY +consumer ORDER BY SUM(tokens_in) + SUM(tokens_out) DESC, COUNT(*) DESC`
	plan := queryPlan(t, st, q, time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	t.Logf("план: %v", plan)
	if !strings.Contains(strings.Join(plan, "\n"), "idx_usage_at") {
		t.Errorf("группировка не берёт индекс по времени и читает журнал целиком: %v", plan)
	}
}
