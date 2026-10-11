package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/llm"
)

// Цена выхода почти везде в несколько раз выше цены входа. Пока обе считались
// по одному числу, «Расход» врал в обе стороны: занижал там, где модель много
// писала, и завышал там, где много читала.
func TestPriceInAndOut(t *testing.T) {
	in, out, cur := parsePrices("$0.55 / $2.20 / 1M")
	if in != 0.55 || out != 2.20 || cur != "$" {
		t.Fatalf("две цены разобрались как %v / %v %q", in, out, cur)
	}
	// старая запись с одной ценой должна работать как раньше
	in, out, cur = parsePrices("$0.55 / 1M")
	if in != 0.55 || out != 0.55 || cur != "$" {
		t.Fatalf("одна цена разобралась как %v / %v %q", in, out, cur)
	}
	if v, out, _ := parsePrices(""); v != 0 || out != 0 {
		t.Fatalf("пустая цена дала %v / %v", v, out)
	}
}

// У DeepSeek вход из кэша дешевле обычного примерно в тридцать раз. Без
// учёта кэша наш счёт завышен настолько же — а это самая частая ошибка в
// таких прикидках.
func TestPriceWithWithCache(t *testing.T) {
	in, out, cache, cur := parsePrices3("$0.22 / $0.66 / $0.007 / 1M")
	if in != 0.22 || out != 0.66 || cache != 0.007 || cur != "$" {
		t.Fatalf("три цены разобрались как %v / %v / %v %q", in, out, cache, cur)
	}
	// прайс без цены кэша работает как раньше
	if _, _, k, _ := parsePrices3("$0.55 / $2.20 / 1M"); k != 0 {
		t.Fatalf("цена кэша взялась ниоткуда: %v", k)
	}

	e, _ := newTestEngine(t, "тест", nil)
	if err := e.saveUserModels([]ModelConf{{Name: "дипсик", Type: "openai",
		BaseURL: "https://api.deepseek.com/v1", Price: "$0.22 / $0.66 / $0.007 / 1M"}}); err != nil {
		t.Fatal(err)
	}
	// миллион входа, из них 800 тысяч из кэша, и 100 тысяч выхода
	withCache, _ := e.costOfUsage("дипсик", llm.Usage{In: 1_000_000, Cached: 800_000, Out: 100_000})
	withoutCache, _ := e.costOfUsage("дипсик", llm.Usage{In: 1_000_000, Out: 100_000})
	if withCache >= withoutCache {
		t.Fatalf("кэш не удешевил: с кэшем %.4f, без %.4f", withCache, withoutCache)
	}
	// 200к свежих по 0.22 + 800к из кэша по 0.007 + 100к выхода по 0.66
	want := 0.2*0.22 + 0.8*0.007 + 0.1*0.66
	if diff := withCache - want; diff > 0.0001 || diff < -0.0001 {
		t.Fatalf("цена %.5f, ждали %.5f", withCache, want)
	}
}

// Цена задаётся человеком и проверяется на входе: неразобранная строка
// молча превратилась бы в ноль, и расход показывал бы «бесплатно» там,
// где деньги тратятся.
func TestAssignmentPrice(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	if err := e.saveUserModels([]ModelConf{{Name: "м", Type: "openai", BaseURL: "https://api.example.com/v1"}}); err != nil {
		t.Fatal(err)
	}
	if e.PriceOf("м") != "" {
		t.Fatal("цена взялась ниоткуда")
	}
	if err := e.SetModelPrice("м", "$0.55 / $2.20 / 1M"); err != nil {
		t.Fatal(err)
	}
	if e.PriceOf("м") != "$0.55 / $2.20 / 1M" {
		t.Fatalf("цена не сохранилась: %q", e.PriceOf("м"))
	}
	// пустая строка — законный способ убрать цену
	if err := e.SetModelPrice("м", "  "); err != nil {
		t.Fatal(err)
	}
	if e.PriceOf("м") != "" {
		t.Fatal("цена не убралась")
	}
	// мусор не принимаем: молчаливый ноль хуже отказа
	if err := e.SetModelPrice("м", "дёшево"); err == nil {
		t.Fatal("неразобранная цена принята")
	}
	if err := e.SetModelPrice("которой-нет", "$1 / 1M"); err == nil {
		t.Fatal("цена поставлена несуществующей модели")
	}
}

// В капсулу попадало всё после последнего знака доллара. Пока цена была
// одна, это работало; с тремя ценами туда попадала цена кэша, и модель
// выглядела в тридцать раз дешевле, чем есть.
func TestShortPriceNotConfusesCache(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	if err := e.saveUserModels([]ModelConf{
		{Name: "три", Type: "openai", BaseURL: "https://api.deepseek.com/v1",
			Price: "$0.22 / $0.66 / $0.007 / 1M", PricePeak: "$0.44 / $1.32 / $0.014 / 1M",
			Peak: "пн-пт 01:00-04:00 UTC"},
		{Name: "одна", Type: "openai", BaseURL: "https://api.example.com/v1", Price: "$0.55 / 1M"},
		{Name: "без", Type: "openai", BaseURL: "https://api.example.com/v1"},
	}); err != nil {
		t.Fatal(err)
	}
	got := e.PriceShort("три")
	if !strings.Contains(got, "0.22") || !strings.Contains(got, "0.66") {
		t.Fatalf("вход и выход потерялись: %q", got)
	}
	if strings.Contains(got, "0.007") {
		t.Fatalf("в капсуле оказалась цена кэша: %q", got)
	}
	if !strings.Contains(got, "в пик дороже") {
		t.Fatalf("про пик не сказано: %q", got)
	}
	if got := e.PriceShort("одна"); got != "$0.55 / 1M" {
		t.Fatalf("одна цена: %q", got)
	}
	if got := e.PriceShort("без"); got != "" {
		t.Fatalf("цена взялась ниоткуда: %q", got)
	}
}

// Нет связи — модель ни при чём: роль не должна уходить на передышку и
// слать «поменяйте модель», когда починить надо Wi-Fi.
func TestNetNotCountsDenialRole(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	for i := 0; i < 5; i++ {
		e.countRoleFail(RoleChat, "м", fmt.Errorf("dial tcp: no route to host"))
	}
	if n := st.Setting("role.fails."+RoleChat, "0"); n != "0" {
		t.Fatalf("сетевые ошибки посчитаны отказами: %s", n)
	}
	if _, ok := e.roleCooldown(RoleChat); ok {
		t.Fatal("роль ушла на передышку из-за сети")
	}
	// а настоящий отказ модели считается как раньше
	e.countRoleFail(RoleChat, "м", fmt.Errorf("HTTP 500"))
	if n := st.Setting("role.fails."+RoleChat, "0"); n != "1" {
		t.Fatalf("настоящий отказ не посчитан: %s", n)
	}
}
