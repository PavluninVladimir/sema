package engine

import (
	"strings"
	"testing"
	"time"
)

// Разбор страницы цен нарочно строгий: половина разобранной таблицы
// опаснее, чем ничего. Неверная цена хуже отсутствующей — по ней
// принимают решения.
func TestParseTablePrices(t *testing.T) {
	// вёрстка как на живой странице: таблица по столбцам, цены группами —
	// сперва вне пика, потом в пик
	html := `<table><tr><th>MODEL</th></tr>
	<td>deepseek-v4-flash</td><td>deepseek-v4-pro</td>
	<tr><td>OUTPUT PRICE (CACHE HIT)</td></tr>
	<td>$0.007</td><td>$0.022</td><td>$0.014</td><td>$0.044</td>
	<tr><td>INPUT PRICE (CACHE MISS)</td></tr>
	<td>$0.22</td><td>$0.66</td><td>$0.44</td><td>$1.32</td>
	<tr><td>OUTPUT PRICE</td></tr>
	<td>$0.66</td><td>$1.98</td><td>$1.32</td><td>$3.96</td>
	<p>(1) Off-peak rates are half of the peak rates. Peak hours are 01:00 - 04:00
	and 06:00 - 10:00 UTC, Monday through Friday (all other hours are off-peak).</p></table>`

	price := priceFromCells(cells(html))
	if len(price) != 2 {
		t.Fatalf("разобрано %d моделей: %v", len(price), price)
	}
	want := "$0.22 / $0.66 / $0.007 / 1M | $0.44 / $1.32 / $0.014 / 1M | пн-пт 01:00-04:00,06:00-10:00 UTC"
	if got := price["deepseek-v4-flash"]; got != want {
		t.Fatalf("flash:\n вышло %q\n ждали %q", got, want)
	}
	if got := price["deepseek-v4-pro"]; !strings.Contains(got, "$0.66 / $1.98 / $0.022") {
		t.Fatalf("pro: %q", got)
	}
	// расписание из сноски должно быть понятно нашему же разбору пика
	if !inPeak("пн-пт 01:00-04:00,06:00-10:00 UTC", time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC)) {
		t.Fatal("своё же расписание не разбирается")
	}
}

// Раздел, где чисел меньше, чем нужно, — это другая вёрстка: разбираем
// всё или ничего. Половина таблицы опаснее пустоты.
func TestPartialTableRejected(t *testing.T) {
	html := `<td>MODEL</td><td>deepseek-v4-flash</td><td>deepseek-v4-pro</td>
	<td>(CACHE HIT)</td><td>$0.007</td><td>$0.022</td>
	<td>(CACHE MISS)</td><td>$0.22</td><td>$0.66</td>
	<td>OUTPUT</td><td>$0.66</td><td>$1.98</td>`
	if price := priceFromCells(cells(html)); len(price) != 0 {
		t.Fatalf("неполная таблица разобралась: %v", price)
	}
}

// Пустая страница или изменившаяся вёрстка — это молчание, а не половина
// таблицы.
func TestLayoutChangedSilent(t *testing.T) {
	for _, text := range []string{"", "Pricing page", "<div>цены скоро</div>"} {
		if price := priceFromCells(cells(text)); len(price) != 0 {
			t.Fatalf("из %q что-то разобралось: %v", text, price)
		}
	}
}

// Одна и та же цена, записанная иначе, не должна дёргать человека.
func TestPriceComparedByNumbers(t *testing.T) {
	if !linePricesMatch("$0.22 / $0.66 / $0.007 / 1M", "$0.220 / $0.660 / $0.0070 / 1M") {
		t.Fatal("та же цена другим написанием посчиталась изменением")
	}
	if linePricesMatch("$0.22 / $0.66 / 1M", "$0.25 / $0.66 / 1M") {
		t.Fatal("настоящее изменение не замечено")
	}
	if linePricesMatch("", "$0.22 / $0.66 / 1M") {
		t.Fatal("появление цены не замечено")
	}
}

// Связка нашей модели с именем в прайсе. «deepseek-chat» и
// «deepseek-v4-flash» — одно и то же только по документации провайдера, а
// не по написанию: угадывать нельзя, ошибка стоит денег.
func TestBundleNameInPrice(t *testing.T) {
	price := map[string]string{"deepseek-v4-flash": "$0.22 / $0.66 / 1M"}

	// по имени в API не совпало — и это правильно
	res := ModelConf{Name: "DeepseekFlash", Model: "deepseek-chat"}
	if got := priceForModel(price, res); got != "" {
		t.Fatalf("угадали соответствие сами: %q", got)
	}
	// человек связал — нашлось
	res.PriceName = "deepseek-v4-flash"
	if got := priceForModel(price, res); got == "" {
		t.Fatal("после связки цена не нашлась")
	}
	// регистр не важен: в прайсе пишут как хотят
	res.PriceName = "DeepSeek-V4-Flash"
	if got := priceForModel(price, res); got == "" {
		t.Fatal("регистр помешал")
	}
	// имя в API совпадает напрямую — связка не нужна
	directly := ModelConf{Name: "Прямая", Model: "deepseek-v4-flash"}
	if got := priceForModel(price, directly); got == "" {
		t.Fatal("прямое совпадение по имени в API не сработало")
	}
}
