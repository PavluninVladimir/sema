package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Живые страницы цен: вёрстка провайдера — единственная правда, и выдуманный
// пример легко подогнать под свой код. Файлы кладутся руками в каталог из
// SEMA_PRICE_PAGES (openai.html, deepseek.html, anthropic.html, google.html —
// то, что отдаёт curl по адресам из справочника); нет каталога — тест
// пропускается, чтобы не ходить в сеть и не зависеть от чужих цен. Числа
// ниже — со страниц на 30 сентября 2026.
func TestPriceLivePages(t *testing.T) {
	dir := os.Getenv("SEMA_PRICE_PAGES")
	if dir == "" {
		t.Skip("SEMA_PRICE_PAGES не задан — живые страницы не проверяю")
	}
	read := func(name string) priceDoc {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Skipf("нет %s: %v", name, err)
		}
		return priceDocument(string(raw))
	}
	check := func(page priceDoc, ans priceAnswer, others ...string) {
		t.Helper()
		if _, ok, st := verifyDoc(page, ans, others...); !ok {
			t.Errorf("%s: живая строка не подтвердилась: %+v", ans.PageName, st)
		}
	}
	reject := func(page priceDoc, ans priceAnswer, others ...string) {
		t.Helper()
		if _, ok, _ := verifyDoc(page, ans, others...); ok {
			t.Errorf("%s: неверные числа приняты", ans.PageName)
		}
	}

	openai := read("openai.html")
	check(openai, priceAnswer{PageName: "gpt-6-luna", Input: 0.1, Cached: 0.01, Output: 0.5}, "gpt-6.1-sol", "gpt-6-sol")
	check(openai, priceAnswer{PageName: "gpt-6.1-sol", Input: 2, Cached: 0.1, Output: 10}, "gpt-6-luna", "gpt-6-sol")
	check(openai, priceAnswer{PageName: "gpt-5.4-mini", Input: 0.75, Cached: 0.075, Output: 4.5}, "gpt-5.4", "gpt-5.4-nano")
	check(openai, priceAnswer{PageName: "gpt-5.4", Input: 2.5, Cached: 0.25, Output: 15}, "gpt-5.4-mini", "gpt-5.4-nano")
	check(openai, priceAnswer{PageName: "gpt-4o-mini-transcribe", PerMinute: 0.003}, "gpt-4o-transcribe")
	check(openai, priceAnswer{PageName: "gpt-4o", Input: 2.5, Cached: 1.25, Output: 10}, "gpt-4o-mini", "gpt-4o-2024-05-13")
	// gpt-6-sol: выходом на живой странице была взята запись в кэш (2.5), а не $10
	check(openai, priceAnswer{PageName: "gpt-6-sol", Input: 2, Cached: 0.2, Output: 10}, "gpt-6-luna", "gpt-6.1-sol")
	reject(openai, priceAnswer{PageName: "gpt-6-sol", Input: 2, Cached: 0.2, Output: 2.5}, "gpt-6-luna", "gpt-6.1-sol")
	// цена пакетного тарифа gpt-6-luna — 0.05, и первой строкой цен она не стоит
	reject(openai, priceAnswer{PageName: "gpt-6-luna", Input: 0.05, Cached: 0.005, Output: 0.25}, "gpt-6-sol")
	// имена с датой дообученных моделей: их цены лежат только под «Finetuning»
	reject(openai, priceAnswer{PageName: "gpt-4o-2024-08-06", Input: 3.75, Cached: 1.875, Output: 15})
	reject(openai, priceAnswer{PageName: "gpt-4o-mini-2024-07-18", Input: 0.3, Cached: 0.15, Output: 1.2})
	reject(openai, priceAnswer{PageName: "o4-mini-2025-04-16", Input: 4, Cached: 1, Output: 16})
	t.Logf("OpenAI: %d знаков текста, %d строк", len(openai.Text), len(openai.Sections))

	// сквозная привязка: модель ответила «как на Pi» — и базовыми строками, и
	// ценами дообученных под датами. Модели с датой получают цену базовой
	ourModels := []ModelConf{{Name: "gpt-6-sol", Model: "gpt-6-sol"}, {Name: "gpt-6-luna", Model: "gpt-6-luna"},
		{Name: "gpt-4o-2024-08-06", Model: "gpt-4o-2024-08-06"}, {Name: "gpt-4o-mini-2024-07-18", Model: "gpt-4o-mini-2024-07-18"},
		{Name: "o4-mini-2025-04-16", Model: "o4-mini-2025-04-16"}, {Name: "gpt-4o-mini-transcribe", Model: "gpt-4o-mini-transcribe"}}
	answers := []priceAnswer{
		{PageName: "gpt-6-sol", Input: 2, Cached: 0.2, Output: 2.5},
		{PageName: "gpt-6-luna", Input: 0.1, Cached: 0.01, Output: 0.5},
		{PageName: "gpt-4o", Input: 2.5, Cached: 1.25, Output: 10},
		{PageName: "gpt-4o-2024-08-06", Input: 3.75, Cached: 1.875, Output: 15},
		{PageName: "gpt-4o-mini", Input: 0.15, Cached: 0.075, Output: 0.6},
		{PageName: "gpt-4o-mini-2024-07-18", Input: 0.3, Cached: 0.15, Output: 1.2},
		{PageName: "o4-mini", Input: 1.1, Cached: 0.275, Output: 4.4},
		{PageName: "o4-mini-2025-04-16", Input: 4, Cached: 1, Output: 16},
		{PageName: "gpt-4o-mini-transcribe", PerMinute: 0.003},
	}
	var st priceStats
	got := map[string]string{}
	for _, f := range bindPrices(verifyAnswers(openai, defaultSkip(), answers, &st), ourModels, &st) {
		got[f.Model] = f.Price
	}
	want := map[string]string{
		"gpt-6-luna":             "$0.1 / $0.5 / $0.01 / 1M",
		"gpt-4o-2024-08-06":      "$2.5 / $10 / $1.25 / 1M",
		"gpt-4o-mini-2024-07-18": "$0.15 / $0.6 / $0.075 / 1M",
		"o4-mini-2025-04-16":     "$1.1 / $4.4 / $0.275 / 1M",
		"gpt-4o-mini-transcribe": "$0.003 / мин",
	}
	for model, price := range want {
		if got[model] != price {
			t.Errorf("%s: цена %q, ждали %q", model, got[model], price)
		}
	}
	if _, has := got["gpt-6-sol"]; has {
		t.Errorf("gpt-6-sol с записью в кэш вместо выхода получила цену %q", got["gpt-6-sol"])
	}
	t.Logf("после проверки: %d цен, отброшено %d: %v", len(got), st.Dropped, st.DroppedWhy)

	// все строки обычного тарифа со страницы, как их назвала бы хорошая модель:
	// каждая верная строка обязана пройти (иначе проверка столбцов отбросила бы
	// законное), а та же строка с выходом из соседнего столбца — нет
	lines := strings.Split(openai.Text, "\n")
	start := -1
	for i, l := range lines {
		if l == "Тариф: standard" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("в тексте OpenAI нет обычного тарифа")
	}
	var rows, passed, replaced int
	for _, l := range lines[start+2:] { // после «Тариф» и шапки
		cells := strings.Split(l, "\t")
		if len(cells) < 4 || cellRole(cells[1]) != roleOther && !hasDigitsOnly(cells[1]) && cells[1] != "-" {
			break // таблица кончилась
		}
		name := strings.TrimSpace(reParen.ReplaceAllString(cells[0], ""))
		num := func(i int) float64 {
			v, _ := cellNum(cells[i])
			return v
		}
		in, cached, out := num(1), num(2), num(len(cells)-1)
		if in == 0 || out == 0 {
			continue // цены нет, только «-»
		}
		rows++
		if _, ok, st := verifyDoc(openai, priceAnswer{PageName: name, Input: flexNum(in), Cached: flexNum(cached), Output: flexNum(out)}); ok {
			passed++
		} else {
			t.Errorf("верная строка тарифа отброшена: %s %v/%v/%v: %v", name, in, cached, out, st.DroppedWhy)
		}
		if len(cells) == 5 && num(3) != out && num(3) >= in { // столбец записи в кэш вместо выхода
			if _, ok, _ := verifyDoc(openai, priceAnswer{PageName: name, Input: flexNum(in), Cached: flexNum(cached), Output: flexNum(num(3))}); ok {
				t.Errorf("%s: запись в кэш %v принята за выход", name, num(3))
			}
			replaced++
		}
	}
	if rows < 20 {
		t.Errorf("строк обычного тарифа разобрано %d — таблица не нашлась", rows)
	}
	t.Logf("обычный тариф OpenAI: верных строк %d, прошло %d; подмен выхода записью в кэш проверено %d, все отброшены", rows, passed, replaced)

	deep := read("deepseek.html")
	check(deep, priceAnswer{PageName: "deepseek-flash", Input: 0.15, Cached: 0.003, Output: 0.6,
		PeakIn: 0.3, PeakCache: 0.006, PeakOut: 1.2, PeakHours: "пн-пт 01:00-04:00,06:00-10:00 UTC"}, "deepseek-v4-pro")
	// цены соседнего столбца: числа на странице есть, но стоят не под этим именем,
	// и ловит это только независимый разбор таблицы
	rawDeep, _ := os.ReadFile(filepath.Join(dir, "deepseek.html"))
	st = priceStats{}
	swapped := verifiedPrice{PageName: "deepseek-flash", Price: "$0.66 / $1.98 / $0.022 / 1M"}
	if got := crossCheckTable(string(rawDeep), []verifiedPrice{swapped}, &st); len(got) != 0 || st.Dropped != 1 {
		t.Errorf("цены соседнего столбца прошли сверку с таблицей: %+v", got)
	}
	// запасной разбор на живой странице: обе модели, цены не перепутаны
	table := priceFromCells(cells(string(rawDeep)))
	if len(table) != 2 {
		t.Fatalf("запасной разбор живой страницы DeepSeek: %v", table)
	}
	for name, line := range table {
		t.Logf("DeepSeek %s → %s", name, line)
	}

	anth := read("anthropic.html")
	// 2.5 у Sonnet 5.5 — запись в кэш на 5 минут, выходом она не бывает
	reject(anth, priceAnswer{PageName: "Claude Sonnet 5.5", Input: 2, Cached: 0.2, Output: 2.5}, "Claude Haiku 4.5", "Claude Opus 5.5")
	check(anth, priceAnswer{PageName: "Claude Sonnet 5.5", Input: 2, Cached: 0.2, Output: 10}, "Claude Haiku 4.5", "Claude Opus 5.5")
	check(anth, priceAnswer{PageName: "Claude Haiku 4.5", Input: 1, Cached: 0.1, Output: 5}, "Claude Sonnet 5.5", "Claude Opus 5.5")
	reject(anth, priceAnswer{PageName: "Claude Haiku 4.5", Input: 2, Cached: 0.2, Output: 10}, "Claude Sonnet 5.5")

	goog := read("google.html")
	check(goog, priceAnswer{PageName: "gemini-2.5-flash", Input: 0.3, Cached: 0.03, Output: 2.5}, "gemini-2.5-flash-lite")
	check(goog, priceAnswer{PageName: "gemini-2.5-flash-lite", Input: 0.1, Output: 0.4}, "gemini-2.5-flash")
	reject(goog, priceAnswer{PageName: "gemini-2.5-flash", Input: 0.1, Output: 0.4}, "gemini-2.5-flash-lite")
}
