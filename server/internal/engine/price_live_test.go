package engine

import (
	"os"
	"testing"
)

// Разбор проверяется на настоящей странице, а не только на выдуманной:
// вёрстка провайдера — единственная правда, и сочинённый пример легко
// подогнать под свой же код. Файл кладётся рядом руками; нет файла — тест
// пропускается, чтобы не ходить в сеть на каждом прогоне.
func TestParseLivePages(t *testing.T) {
	raw, err := os.ReadFile("/tmp/pricing.html")
	if err != nil {
		t.Skip("нет /tmp/pricing.html — пропускаю")
	}
	price := priceFromCells(cells(string(raw)))
	if len(price) == 0 {
		t.Fatal("на живой странице цен не нашлось — разбор надо чинить")
	}
	for name, price := range price {
		t.Logf("%s → %s", name, price)
		in, out, cache, _ := parsePrices3(price)
		if in <= 0 || out <= 0 || cache <= 0 {
			t.Errorf("%s: цена разобралась неполно (%v / %v / %v)", name, in, out, cache)
		}
		if cache >= in {
			t.Errorf("%s: кэш должен быть дешевле входа, а вышло %v против %v", name, cache, in)
		}
	}
}
