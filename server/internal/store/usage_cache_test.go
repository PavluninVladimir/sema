package store

import (
	"testing"
	"time"
)

// Сводку расхода за сутки «Задачи» и «Здоровье» просят при каждом открытии,
// а на живой Сёме она стоила на Pi около 120 мс: кэш был, но ключ с
// точностью до секунды менялся каждым запросом (5 октября 2026). Теперь ключ —
// длина окна: соседний запрос не ходит в базу, устаревший ответ отдаётся
// сразу и пересчитывается в фоне, а совсем старый — считается на месте.
func TestUsageByCachesWindowNotMoment(t *testing.T) {
	st := testStore(t)
	add := func() {
		t.Helper()
		if err := st.AddUsage(&UsageRow{At: time.Now().Add(-time.Hour), Consumer: "chat:1", Model: "м", TokensIn: 10, OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	calls := func() int {
		t.Helper()
		sums, err := st.UsageBy("consumer", time.Now().Add(-24*time.Hour), "")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, s := range sums {
			n += s.Calls
		}
		return n
	}
	add()
	if n := calls(); n != 1 {
		t.Fatalf("вызовов %d, ждали 1", n)
	}

	// тот же вопрос мигом позже — из памяти, без похода в базу, даже если
	// миг начала окна сдвинулся на секунды
	add()
	tr := TraceSQL()
	sums, _ := st.UsageBy("consumer", time.Now().Add(-24*time.Hour-3*time.Second), "")
	queries := tr.Stats().N
	tr.Stop()
	if queries != 0 || len(sums) != 1 || sums[0].Calls != 1 {
		t.Fatalf("соседний запрос того же окна сходил в базу (%d запросов) или ответ не тот: %+v", queries, sums)
	}

	// устарел — прежнее сразу, пересчёт в фоне
	age := func(by time.Duration) {
		st.usageMu.Lock()
		for k := range st.usageCache {
			for i := range st.usageCache[k] {
				st.usageCache[k][i].at = time.Now().Add(-by)
			}
		}
		st.usageMu.Unlock()
	}
	age(time.Minute)
	if n := calls(); n != 1 {
		t.Fatalf("устаревший ответ не отдан сразу: %d", n)
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("фоновый пересчёт не дошёл")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// совсем старый — считается на месте
	add()
	age(usageStale + time.Minute)
	if n := calls(); n != 3 {
		t.Fatalf("старый ответ не пересчитан на месте: %d", n)
	}
}

// Окно с началом в определённый миг («Здоровье» по умолчанию — «с полуночи»)
// растёт каждую минуту, и ключ «длина окна» промахивался раз в минуту — запрос
// в 95 мс на Pi. Запись ищется по ближайшему началу: у такого окна оно одно и
// то же, и устаревший ответ отдаётся сразу с пересчётом в фоне. А окно другой
// длины чужой записи не получает.
func TestUsageByFixedStartWindow(t *testing.T) {
	st := testStore(t)
	if err := st.AddUsage(&UsageRow{At: time.Now().Add(-time.Hour), Consumer: "chat:1", Model: "м", TokensIn: 10, OK: true}); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-3 * time.Hour) // «с полуночи»: начало не движется
	if sums, _ := st.UsageBy("consumer", start, ""); len(sums) != 1 {
		t.Fatalf("первый ответ: %+v", sums)
	}
	st.usageMu.Lock()
	for k := range st.usageCache {
		for i := range st.usageCache[k] {
			st.usageCache[k][i].at = time.Now().Add(-2 * time.Minute) // прошло две минуты
		}
	}
	st.usageMu.Unlock()
	tr := TraceSQL()
	_, _ = st.UsageBy("consumer", start, "")
	queries := tr.Stats().N
	tr.Stop()
	if queries != 0 {
		t.Errorf("окно с тем же началом через две минуты сходило в базу на месте: %d запросов", queries)
	}
	// фоновый пересчёт дожидаемся: иначе он ещё пишет в базу, когда тест
	// убирает её каталог, и уборка падает «directory not empty»
	waitUsageRefresh(t, st)
	// неделя — не «с полуночи»: своя запись, свой запрос
	tr = TraceSQL()
	_, _ = st.UsageBy("consumer", time.Now().AddDate(0, 0, -7), "")
	queries = tr.Stats().N
	tr.Stop()
	if queries != 1 {
		t.Errorf("окно другой длины взяло чужую запись: %d запросов", queries)
	}
}

// waitUsageRefresh — дождаться конца фоновых пересчётов сводки (условие, а
// не срок).
func waitUsageRefresh(t *testing.T, st *Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := false
		st.usageMu.Lock()
		for _, list := range st.usageCache {
			for _, c := range list {
				busy = busy || c.refreshing
			}
		}
		st.usageMu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("фоновый пересчёт сводки не кончился")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
