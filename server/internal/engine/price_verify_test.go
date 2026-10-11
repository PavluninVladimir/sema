package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// Проверка ответа модели кодом. Ошибки модели — соседняя ячейка, чужой
// тариф, выдуманное число — не должны доехать до предложения человеку.

func verifyOne(text string, a priceAnswer, others ...string) (verifiedPrice, bool, priceStats) {
	return verifyDoc(priceDoc{Text: text}, a, others...)
}

// verifyDoc — то же для страницы с разделами.
func verifyDoc(doc priceDoc, a priceAnswer, others ...string) (verifiedPrice, bool, priceStats) {
	list := []priceAnswer{a}
	for _, o := range others {
		list = append(list, priceAnswer{PageName: o})
	}
	var st priceStats
	got := verifyAnswers(doc, defaultSkip(), list, &st)
	if len(got) == 0 {
		return verifiedPrice{}, false, st
	}
	return got[0], true, st
}

// defaultSkip — слова заголовков по умолчанию, как в настройке.
func defaultSkip() []string {
	var out []string
	for _, w := range strings.Split(priceSkipDefault, ",") {
		out = append(out, strings.TrimSpace(w))
	}
	return out
}

func TestVerifyNameIsWholeWord(t *testing.T) {
	text := "Model\tInput\tOutput\ngpt-5.4\t2.5\t15\ngpt-5-mini\t0.25\t2\ngpt-5\t1.25\t10\n"
	// «gpt-5» не живёт внутри «gpt-5.4» и «gpt-5-mini»: цена берётся со своей строки
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "gpt-5", Input: 1.25, Output: 10}, "gpt-5.4", "gpt-5-mini"); !ok {
		t.Error("gpt-5 не нашлась на своей строке")
	}
	// числа соседа по префиксу для неё чужие
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "gpt-5", Input: 2.5, Output: 15}, "gpt-5.4"); ok {
		t.Error("цена gpt-5.4 принята за цену gpt-5")
	}
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "gpt-5-mini", Input: 0.25, Output: 2}, "gpt-5"); !ok {
		t.Error("gpt-5-mini не нашлась")
	}
}

// Число обязано стоять на странице рядом с именем: цена соседней модели —
// не подтверждение.
func TestVerifyNumberMustBeNearName(t *testing.T) {
	text := "gpt-4o-mini\t0.15\t0.075\t0.6\ngpt-5.4-mini\t0.75\t0.075\t4.5\n"
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "gpt-4o-mini", Input: 0.15, Cached: 0.075, Output: 0.6}, "gpt-5.4-mini"); !ok {
		t.Error("верная строка отброшена")
	}
	_, ok, st := verifyOne(text, priceAnswer{PageName: "gpt-4o-mini", Input: 0.15, Cached: 0.075, Output: 4.5}, "gpt-5.4-mini")
	if ok || st.Dropped != 1 {
		t.Errorf("выход из соседней строки принят: ok=%v %+v", ok, st)
	}
}

// Вёрстка Anthropic: имя с приклеенным описанием, цены отдельными строками
// под ним; имена моделей на странице «Claude Sonnet 4.5», в API —
// «claude-sonnet-4-5-20250929».
func TestVerifyGluedNameAndSeparateLines(t *testing.T) {
	text := "Model\tBase tokens\tPrompt caching\nName\tInput\tOutput\t5m writes\t1h writes\tHits and refreshes\n" +
		"Claude Sonnet 4.5For the best combination of speed and intelligence\n$3 / MTok\n$15 / MTok\n$3.75 / MTok\n$6 / MTok\n$0.30 / MTok\n" +
		"Claude Sonnet 4\n$3 / MTok\n$15 / MTok\n$3.75 / MTok\n$6 / MTok\n$0.30 / MTok\n" +
		"Claude Haiku 4.5\n$1 / MTok\n$5 / MTok\n$1.25 / MTok\n$2 / MTok\n$0.10 / MTok\n"
	ans := priceAnswer{PageName: "Claude Sonnet 4.5", Input: 3, Cached: 0.3, Output: 15}
	if _, ok, st := verifyOne(text, ans, "Claude Sonnet 4", "Claude Haiku 4.5"); !ok {
		t.Fatalf("приклеенное имя не найдено: %+v", st)
	}
	// цена Haiku для Sonnet — не подтверждение
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "Claude Sonnet 4.5", Input: 1, Cached: 0.1, Output: 5}, "Claude Sonnet 4", "Claude Haiku 4.5"); ok {
		t.Error("цены Haiku приняты за цены Sonnet")
	}

	// привязка к нашей модели с датой в хвосте: слова как множество, числа версии по порядку
	models := []ModelConf{
		{Name: "sonnet", Model: "claude-sonnet-4-5-20250929"},
		{Name: "sonnet4", Model: "claude-sonnet-4-20250514"},
		{Name: "haiku", Model: "claude-haiku-4-5-20251001"},
	}
	var st priceStats
	found := []verifiedPrice{
		{PageName: "Claude Sonnet 4.5", Price: "$3 / $15 / $0.3 / 1M"},
		{PageName: "Claude Sonnet 4", Price: "$3 / $15 / $0.3 / 1M"},
		{PageName: "Claude Haiku 4.5", Price: "$1 / $5 / $0.1 / 1M"},
	}
	byName := map[string]string{}
	for _, f := range bindPrices(found, models, &st) {
		byName[f.Model] = f.PageName
	}
	if byName["sonnet"] != "Claude Sonnet 4.5" || byName["sonnet4"] != "Claude Sonnet 4" || byName["haiku"] != "Claude Haiku 4.5" {
		t.Errorf("привязка по именам: %v", byName)
	}
}

// Вёрстка Google: ID модели отдельной строкой, цены ниже; сноски и цены
// нескольких видов входа не путают.
func TestVerifyGoogleLike(t *testing.T) {
	text := "Gemini 2.5 Flash\ngemini-2.5-flash\nTry it in Google AI Studio\nOur first hybrid reasoning model.\nStandard\n" +
		"Free Tier\tPaid Tier, per 1M tokens in USD\nInput price\tFree of charge\t$0.30 (text / image / video)\n$1.00 (audio)\n" +
		"Output price (including thinking tokens)\tFree of charge\t$2.50\nContext caching price\tNot available\t$0.03 (text / image / video)\n" +
		"Gemini 2.5 Flash-Lite\ngemini-2.5-flash-lite\nStandard\nInput price\tFree of charge\t$0.10 (text / image / video)\nOutput price\tFree of charge\t$0.40\n"
	ans := priceAnswer{PageName: "gemini-2.5-flash", Input: 0.3, Cached: 0.03, Output: 2.5}
	if _, ok, st := verifyOne(text, ans, "gemini-2.5-flash-lite"); !ok {
		t.Fatalf("цены Google не подтверждены: %+v", st)
	}
	// цены Flash-Lite для Flash — чужие
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "gemini-2.5-flash", Input: 0.1, Output: 0.4}, "gemini-2.5-flash-lite"); ok {
		t.Error("цены Flash-Lite приняты за цены Flash")
	}
}

// Здравый смысл цен: не тот столбец — отброшено, даже если числа на странице есть.
func TestVerifySanity(t *testing.T) {
	text := "Model\tInput\tCached input\tCache writes\tOutput\nmodel-x\t5\t1\t6\t10\n"
	for name, a := range map[string]priceAnswer{
		"выход дешевле входа": {PageName: "model-x", Input: 10, Output: 5},
		"кэш дороже входа":    {PageName: "model-x", Input: 5, Cached: 6, Output: 10},
		"отрицательная цена":  {PageName: "model-x", Input: -1, Output: 5},
		"нелепая цена":        {PageName: "model-x", Input: 5, Output: 99999},
	} {
		if _, ok, _ := verifyOne(text, a); ok {
			t.Errorf("%s: принято", name)
		}
	}
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "model-x", Input: 5, Cached: 1, Output: 10}); !ok {
		t.Error("здравая строка отброшена")
	}
	// строка без цены — пропуск, а не отброшенное
	_, ok, st := verifyOne(text, priceAnswer{PageName: "model-x"})
	if ok || st.Dropped != 0 || st.NoPrice != 1 {
		t.Errorf("строка без цены: ok=%v %+v", ok, st)
	}
}

// Часы пика — слова модели: годятся, если наш разбор их понимает и каждое
// время стоит на странице.
func TestVerifyPeakHours(t *testing.T) {
	text := "Model\tInput\tOutput\tPeak input\tPeak output\nmodel-y\t0.15\t0.6\t0.3\t1.2\nPeak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC\n"
	ok := priceAnswer{PageName: "model-y", Input: 0.15, Output: 0.6, PeakIn: 0.3, PeakOut: 1.2, PeakHours: "пн-пт 01:00-04:00,06:00-10:00 UTC"}
	got, pass, _ := verifyOne(text, ok)
	if !pass || got.Peak != "$0.3 / $1.2 / 1M" || got.Hours == "" {
		t.Fatalf("пик не взят: %+v", got)
	}
	invented := ok
	invented.PeakHours = "пн-пт 02:00-05:00 UTC" // такого на странице нет
	got, pass, _ = verifyOne(text, invented)
	if !pass || got.Peak != "" || !strings.Contains(got.Note, "часы не подтверждены") {
		t.Errorf("выдуманные часы пика приняты: %+v", got)
	}
}

func TestFlexNumIsForgiving(t *testing.T) {
	var got []priceAnswer
	raw := `[{"page_name":"a","input":"$0.10","output":"1,25","cached":"-","per_minute":null},{"page_name":"b","input":0.5,"output":2}]`
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if float64(got[0].Input) != 0.10 || float64(got[0].Cached) != 0 || float64(got[0].PerMinute) != 0 {
		t.Errorf("строка «$0.10»: %+v", got[0])
	}
	// «1,25» — запятая тысяч, а не десятичная: получится 125, и проверка
	// страницей такое число не пропустит — на странице цены пишут точкой
	if float64(got[0].Output) != 125 {
		t.Errorf("«1,25» разобрано как %v", float64(got[0].Output))
	}
}

func TestParsePriceAnswerWrappers(t *testing.T) {
	for name, in := range map[string]string{
		"ограждение":     "```json\n[{\"page_name\":\"a\",\"input\":1,\"output\":2}]\n```",
		"пояснение":      "Вот цены:\n[{\"page_name\":\"a\",\"input\":1,\"output\":2}]\nГотово.",
		"объект-обёртка": "{\"models\":[{\"page_name\":\"a\",\"input\":1,\"output\":2}]}",
	} {
		list, err := parsePriceAnswer(in)
		if err != nil || len(list) != 1 || list[0].PageName != "a" {
			t.Errorf("%s: %v %+v", name, err, list)
		}
	}
	if _, err := parsePriceAnswer("цен не нашёл"); err == nil {
		t.Error("ответ без списка принят")
	}
}

// Имя модели — слова как множество, числа версии — по порядку.
func TestPriceKey(t *testing.T) {
	same := [][2]string{
		{"Claude Haiku 3.5", "claude-3-5-haiku"},
		{"gpt-5.4 (<272K context length)", "gpt-5.4"},
		{"Gemini 2.5 Flash-Lite", "gemini-2.5-flash-lite"},
		{"deepseek-flash(1)", "deepseek-flash"},
	}
	for _, p := range same {
		if priceKey(p[0]) != priceKey(p[1]) {
			t.Errorf("%q и %q — одна модель, ключи %q и %q", p[0], p[1], priceKey(p[0]), priceKey(p[1]))
		}
	}
	diff := [][2]string{
		{"gpt-5.4", "gpt-4.5"},
		{"gpt-5", "gpt-5-mini"},
		{"claude-opus-4-1", "claude-opus-4"},
		{"gemini-2.5-flash", "gemini-2.5-flash-lite"},
	}
	for _, p := range diff {
		if priceKey(p[0]) == priceKey(p[1]) {
			t.Errorf("%q и %q — разные модели, а ключ один: %q", p[0], p[1], priceKey(p[0]))
		}
	}
}

// Отдельная строка на странице для модели с датой сильнее «цены базовой».
func TestBindPricesExactBeatsDateless(t *testing.T) {
	models := []ModelConf{{Name: "dated", Model: "gpt-4o-2024-05-13"}, {Name: "base", Model: "gpt-4o"}}
	found := []verifiedPrice{
		{PageName: "gpt-4o", Price: "$2.5 / $10 / 1M"},
		{PageName: "gpt-4o-2024-05-13", Price: "$5 / $15 / 1M"},
	}
	var st priceStats
	got := map[string]string{}
	for _, f := range bindPrices(found, models, &st) {
		got[f.Model] = f.Price
	}
	if got["dated"] != "$5 / $15 / 1M" || got["base"] != "$2.5 / $10 / 1M" {
		t.Errorf("отдельная строка с датой не победила: %v", got)
	}
	// связка человека сильнее написания
	models = []ModelConf{{Name: "flash", Model: "deepseek-chat", PriceName: "deepseek-flash"}}
	found = []verifiedPrice{{PageName: "deepseek-flash", Price: "$0.15 / $0.6 / 1M"}, {PageName: "deepseek-chat", Price: "$9 / $9 / 1M"}}
	if got := bindPrices(found, models, &st); len(got) != 1 || got[0].Price != "$0.15 / $0.6 / 1M" {
		t.Errorf("связка «имя в прайсе» не победила: %+v", got)
	}
}

// Текст страницы: таблицы строками с табуляцией, меню и чужие тарифы не
// едут модели.
func TestPriceTextKeepsTablesDropsMenus(t *testing.T) {
	text := priceText(pricePageOpenAI)
	for _, want := range []string{"gpt-6-luna\t0.1\t0.01\t0.125\t0.5", "Тариф: standard",
		"gpt-4o-mini-transcribe\tTranscription\t$1.25\t$5.00\t$0.003 / minute", "Model\tInput\tCached input\tCache writes\tOutput",
		"gpt-5.4 (<272K context length)\t2.5\t0.25\t-\t15"} {
		if !strings.Contains(text, want) {
			t.Errorf("в тексте нет %q\n%s", want, text)
		}
	}
	for _, junk := range []string{"gpt-9-menu", "gpt-8-footer", "0.0625", "var junk"} {
		if strings.Contains(text, junk) {
			t.Errorf("в тексте лишнее: %q", junk)
		}
	}
	ds := priceText(pricePageDeepSeek)
	if !strings.Contains(ds, "MODEL\tdeepseek-flash(1)\tdeepseek-v4-pro") || !strings.Contains(ds, "OFF-PEAK\t$0.003\t$0.022") {
		t.Errorf("таблица DeepSeek потеряла строки:\n%s", ds)
	}
}

// Боковое меню (сорок коротких названий подряд) не едет модели, а колонка
// цен без знака доллара — едет: это данные, а не меню.
func TestDropMenuRuns(t *testing.T) {
	toLines := func(list []string) []docLine {
		out := make([]docLine, len(list))
		for i, l := range list {
			out[i] = docLine{text: l, section: "s"}
		}
		return out
	}
	var menu []string
	for i := 0; i < 40; i++ {
		menu = append(menu, "Раздел "+string(rune('А'+i%26)))
	}
	menu = append(menu, "Prices per 1M tokens", "gpt-x\t1\t2\t3")
	got := dropMenuRuns(toLines(menu))
	// от меню остался хвост в три строки: заголовок таблицы стоит сразу за ним
	if len(got) != 4 || got[2].text != "Prices per 1M tokens" || got[3].text != "gpt-x\t1\t2\t3" || got[3].section != "s" {
		t.Fatalf("меню не убрано: %v", got)
	}
	var nums []string
	for i := 0; i < 40; i++ {
		nums = append(nums, "0."+string(rune('1'+i%9)))
	}
	if got := dropMenuRuns(toLines(nums)); len(got) != 40 {
		t.Errorf("колонка цен без знака доллара принята за меню: осталось %d из 40", len(got))
	}
	if got := dropMenuRuns(toLines(menu[:10])); len(got) != 10 {
		t.Errorf("короткий список принят за меню: %d из 10", len(got))
	}
}

// Роли столбцов по шапке: запись в кэш — не кэш и не выход («Cache writes»,
// «5m writes», «1h writes»), «Hits and refreshes» — кэш.
func TestColumnRoles(t *testing.T) {
	for cell, want := range map[string]colRole{
		"Input": roleInput, "Cached input": roleCached, "Cache writes": roleCacheWrite,
		"Output": roleOutput, "5m writes": roleCacheWrite, "1h writes": roleCacheWrite,
		"Hits and refreshes": roleCached, "Output / cost": roleOutput, "Estimated cost": roleCost,
		"Price per minute": roleCost, "Short context cache writes": roleCacheWrite,
		"Long context cached input": roleCached, "Model": roleOther, "Use case": roleOther,
		"Training": roleOther, "Category": roleOther, "Cached input write": roleCacheWrite,
		"1M INPUT TOKENS (CACHE MISS)": roleInput, "Запись в кэш": roleCacheWrite,
	} {
		if got := cellRole(cell); got != want {
			t.Errorf("%q: роль %v, ждали %v", cell, got, want)
		}
	}
	if !isHeaderCells(trimCells("Model\tInput\tCached input\tCache writes\tOutput")) {
		t.Error("шапка OpenAI не узнана")
	}
	if isHeaderCells(trimCells("Input price\tNot available\t$2.00, prompts <= 200k tokens")) {
		t.Error("подпись строки Google с ценой принята за шапку")
	}
	if isHeaderCells(trimCells("gpt-6-sol\t2\t0.2\t2.5\t10")) {
		t.Error("строка данных принята за шапку")
	}
	if got := strings.Join(firstGroup(trimCells("Model\tInput\tCached input\tCache writes\tOutput\tInput\tCached input\tCache writes\tOutput")), "|"); got != "Model|Input|Cached input|Cache writes|Output" {
		t.Errorf("первая группа шапки: %q", got)
	}
}

// Запись в кэш выходом не становится: число в строке стоит, но не в том
// столбце. Ровно ошибка gpt-6-sol на живой странице: `2 / 0.2 / 2.5 / 10`.
func TestVerifyCacheWriteIsNotOutput(t *testing.T) {
	text := "Model\tInput\tCached input\tCache writes\tOutput\ngpt-6-sol\t2\t0.2\t2.5\t10\ngpt-5.4-mini\t0.75\t0.075\t4.5\n"
	if _, ok, st := verifyOne(text, priceAnswer{PageName: "gpt-6-sol", Input: 2, Cached: 0.2, Output: 10}, "gpt-5.4-mini"); !ok {
		t.Fatalf("верная строка отброшена: %+v", st)
	}
	_, ok, st := verifyOne(text, priceAnswer{PageName: "gpt-6-sol", Input: 2, Cached: 0.2, Output: 2.5}, "gpt-5.4-mini")
	if ok || len(st.DroppedWhy) != 1 || !strings.Contains(st.DroppedWhy[0], "в столбце «выход» стоит 10, а модель назвала 2.5") {
		t.Fatalf("запись в кэш принята за выход: ok=%v %+v", ok, st)
	}
	// кэш из столбца записи тоже не проходит
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "gpt-6-sol", Input: 2, Cached: 2.5, Output: 10}, "gpt-5.4-mini"); ok {
		t.Error("запись в кэш принята за кэш")
	}
	// строка на одну ячейку короче шапки — модель без столбца записи в кэш:
	// три числа — вход, кэш, выход
	if _, ok, st := verifyOne(text, priceAnswer{PageName: "gpt-5.4-mini", Input: 0.75, Cached: 0.075, Output: 4.5}, "gpt-6-sol"); !ok {
		t.Errorf("строка без столбца записи в кэш отброшена: %+v", st)
	}
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "gpt-5.4-mini", Input: 0.75, Cached: 0.075, Output: 0.075}, "gpt-6-sol"); ok {
		t.Error("кэш принят за выход в короткой строке")
	}
}

// Шапки нет, а чисел больше трёх — строка не подтверждена: не угадываем,
// какое число за что. До трёх чисел вход, кэш и выход не спутать.
func TestVerifyManyNumbersWithoutHeaderNotConfirmed(t *testing.T) {
	text := "gpt-6-sol\t2\t0.2\t2.5\t10\ngpt-x\t1\t2\t3\n"
	_, ok, st := verifyOne(text, priceAnswer{PageName: "gpt-6-sol", Input: 2, Cached: 0.2, Output: 10}, "gpt-x")
	if ok || !strings.Contains(strings.Join(st.DroppedWhy, ";"), "шапка столбцов не найдена") {
		t.Fatalf("строка из четырёх чисел без шапки принята: ok=%v %+v", ok, st)
	}
	if _, ok, _ := verifyOne(text, priceAnswer{PageName: "gpt-x", Input: 1, Output: 3}, "gpt-6-sol"); !ok {
		t.Error("строка из трёх чисел без шапки отброшена")
	}
	// шапка есть, но не сходится со строкой (лишний столбец) — тоже не угадываем
	mismatch := "Model\tInput\tOutput\ngpt-6-sol\t2\t0.2\t2.5\t10\n"
	if _, ok, st := verifyOne(mismatch, priceAnswer{PageName: "gpt-6-sol", Input: 2, Output: 10}); ok || !strings.Contains(strings.Join(st.DroppedWhy, ";"), "не сходится с шапкой") {
		t.Errorf("строка, не сходящаяся с шапкой, принята: ok=%v %+v", ok, st)
	}
}

// Вёрстка Anthropic: имя, под ним значения по одному на строку, а шапка над
// таблицей — «Input, Output, 5m writes, 1h writes, Hits and refreshes».
// Запись в кэш (5m, 1h) выходом не становится.
func TestVerifyNearLayoutWithHeader(t *testing.T) {
	text := "Model\tBase tokens\tPrompt caching\nName\tInput\tOutput\t5m writes\t1h writes\tHits and refreshes\n" +
		"Claude Sonnet 4.5For balance\n$3 / MTok\n$15 / MTok\n$3.75 / MTok\n$6 / MTok\n$0.30 / MTok\n" +
		"Claude Sonnet 5\n/ MTok\n/ MTok\n$2.50 / MTok\n$4 / MTok\n$0.20 / MTok\n"
	ok := priceAnswer{PageName: "Claude Sonnet 4.5", Input: 3, Cached: 0.3, Output: 15}
	if _, pass, st := verifyOne(text, ok, "Claude Sonnet 5"); !pass {
		t.Fatalf("верная строка Anthropic отброшена: %+v", st)
	}
	// 3.75 — запись в кэш на 5 минут: числа на странице рядом с именем есть, но не в столбце выхода
	bad := priceAnswer{PageName: "Claude Sonnet 4.5", Input: 3, Cached: 0.3, Output: 3.75}
	if _, pass, st := verifyOne(text, bad, "Claude Sonnet 5"); pass || !strings.Contains(strings.Join(st.DroppedWhy, ";"), "«выход»") {
		t.Errorf("запись в кэш принята за выход: pass=%v %+v", pass, st)
	}
	// у Sonnet 5 в столбцах входа и выхода цены нет («/ MTok»): выдуманные числа не проходят
	if _, pass, _ := verifyOne(text, priceAnswer{PageName: "Claude Sonnet 5", Input: 3, Cached: 0.2, Output: 15}, "Claude Sonnet 4.5"); pass {
		t.Error("цена, которой на странице нет, принята")
	}
}

// Разделы, что не про обычный вызов модели, в проверку не пускаются: у имени
// с датой в «Finetuning» другая цена, и «первое вхождение решает» её бы взяло.
func TestVerifySkipsDeniedSections(t *testing.T) {
	doc := priceDoc{
		Text: "Model\tInput\tCached input\tOutput\ngpt-4o\t2.5\t1.25\t10\ngpt-4o-2024-08-06\t5\t2\t15\nModel\tTraining\tInput\tCached input\tOutput\ngpt-4o-2024-08-06\t25\t3.75\t1.875\t15\n",
		Sections: []string{"Pricing › Flagship models", "Pricing › Flagship models", "Pricing › Flagship models",
			"Pricing › Finetuning", "Pricing › Finetuning"},
	}
	// цена из дообучения: раздел закрыт, а других вхождений у этого имени... есть, в обычном
	if _, ok, _ := verifyDoc(doc, priceAnswer{PageName: "gpt-4o-2024-08-06", Input: 3.75, Cached: 1.875, Output: 15}); ok {
		t.Error("цена дообученной модели принята")
	}
	if v, ok, st := verifyDoc(doc, priceAnswer{PageName: "gpt-4o-2024-08-06", Input: 5, Cached: 2, Output: 15}); !ok || v.Section != "Pricing › Flagship models" {
		t.Errorf("цена из обычного раздела отброшена: %+v %+v", v, st)
	}
	only := priceDoc{Text: "Model\tTraining\tInput\tCached input\tOutput\no4-mini-2025-04-16\t25\t4\t1\t16\n",
		Sections: []string{"Pricing › Finetuning", "Pricing › Finetuning"}}
	_, ok, st := verifyDoc(only, priceAnswer{PageName: "o4-mini-2025-04-16", Input: 4, Cached: 1, Output: 16})
	if ok || !strings.Contains(strings.Join(st.DroppedWhy, ";"), "только в разделах не про обычный вызов") {
		t.Errorf("имя только из дообучения принято: ok=%v %+v", ok, st)
	}
	// список слов правится: без «fine-tun» раздел открыт
	var st2 priceStats
	list := []priceAnswer{{PageName: "o4-mini-2025-04-16", Input: 4, Cached: 1, Output: 16}}
	if got := verifyAnswers(only, []string{"batch"}, list, &st2); len(got) != 1 {
		t.Errorf("слова закрытых разделов не влияют на разбор: %+v", st2)
	}
}

// Отдельная строка с датой сильнее базовой только из того же раздела, что
// и базовая: дата в имени бывает и у дообученных моделей.
func TestBindDatedOnlyInSameSection(t *testing.T) {
	models := []ModelConf{{Name: "dated", Model: "gpt-4o-2024-05-13"}}
	base := verifiedPrice{PageName: "gpt-4o", Price: "$2.5 / $10 / 1M", Section: "A"}
	dated := verifiedPrice{PageName: "gpt-4o-2024-05-13", Price: "$5 / $15 / 1M", Section: "A"}
	var st priceStats
	if got := bindPrices([]verifiedPrice{base, dated}, models, &st); len(got) != 1 || got[0].Price != "$5 / $15 / 1M" {
		t.Errorf("своя строка с датой из того же раздела не победила: %+v", got)
	}
	dated.Section = "B" // другой раздел: не уточнение базовой, а другая цена
	if got := bindPrices([]verifiedPrice{base, dated}, models, &st); len(got) != 1 || got[0].Price != "$2.5 / $10 / 1M" {
		t.Errorf("строка с датой из другого раздела победила базовую: %+v", got)
	}
	// связка человека сильнее раздела
	pinned := []ModelConf{{Name: "dated", Model: "gpt-4o-2024-05-13", PriceName: "gpt-4o-2024-05-13"}}
	if got := bindPrices([]verifiedPrice{base, dated}, pinned, &st); len(got) != 1 || got[0].Price != "$5 / $15 / 1M" {
		t.Errorf("связка «имя в прайсе» не победила: %+v", got)
	}
}

// У страницы, разобранной с заголовками, у каждой строки свой раздел, а
// метки в текст не попадают.
func TestPriceDocumentSections(t *testing.T) {
	doc := priceDocument(`<html><body><h1>Pricing</h1><h2>Flagship</h2><p>Prices per 1M tokens, all of them in dollars for the standard tier of the service.</p>
<table><tr><td>gpt-a</td><td>1</td><td>2</td></tr></table>
<h2>Finetuning</h2><h3>Standard</h3><table><tr><td>gpt-b</td><td>3</td><td>4</td></tr></table>
<h2>Cloud platforms</h2><p>See the docs of Amazon Bedrock and Microsoft Azure that bill through those services.</p></body></html>`)
	if strings.Contains(doc.Text, "\x01") {
		t.Fatal("метка раздела попала в текст")
	}
	lines := strings.Split(doc.Text, "\n")
	if len(doc.Sections) != len(lines) {
		t.Fatalf("строк %d, разделов %d", len(lines), len(doc.Sections))
	}
	got := map[string]string{}
	for i, l := range lines {
		got[l] = doc.Sections[i]
	}
	if got["gpt-a\t1\t2"] != "Pricing › Flagship" || got["gpt-b\t3\t4"] != "Pricing › Finetuning › Standard" ||
		!strings.HasPrefix(got["See the docs of Amazon Bedrock and Microsoft Azure that bill through those services."], "Pricing › Cloud") {
		t.Errorf("разделы строк: %v", got)
	}
}

// Вёрстка Google: под именем модели идут обычный тариф и следом пакетный, со
// своими заголовками. Окно проверки в пакетный раздел не заходит: цены
// пакетного тарифа, названные моделью как обычные, не подтверждаются.
func TestVerifyWindowStopsAtDeniedSection(t *testing.T) {
	doc := priceDoc{
		Text: "gemini-2.5-flash\nTry it in Google AI Studio\nStandard\nInput price\tFree of charge\t$0.30 (text / image / video)\n" +
			"Output price (including thinking tokens)\tFree of charge\t$2.50\nContext caching price\tNot available\t$0.03\n" +
			"Batch\nInput price\tNot available\t$0.15 (text / image / video)\nOutput price (including thinking tokens)\tNot available\t$1.25\n",
		Sections: []string{"G › Flash", "G › Flash", "G › Flash › Standard", "G › Flash › Standard", "G › Flash › Standard", "G › Flash › Standard",
			"G › Flash › Batch", "G › Flash › Batch", "G › Flash › Batch"},
	}
	if _, ok, st := verifyDoc(doc, priceAnswer{PageName: "gemini-2.5-flash", Input: 0.3, Cached: 0.03, Output: 2.5}); !ok {
		t.Fatalf("обычный тариф отброшен: %+v", st)
	}
	if _, ok, _ := verifyDoc(doc, priceAnswer{PageName: "gemini-2.5-flash", Input: 0.15, Cached: 0.03, Output: 1.25}); ok {
		t.Error("цены пакетного тарифа приняты за обычные")
	}
}
