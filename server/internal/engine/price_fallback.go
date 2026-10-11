package engine

// Запасной разбор страницы цен DeepSeek — регулярками, без модели.
//
// Основной путь — модель с проверкой кодом (price_verify.go). Этот включается,
// когда модель недоступна, и только для страницы с такой вёрсткой: таблица
// свёрстана по столбцам — сначала имена всех моделей, потом разделы цен, и в
// каждом разделе чисел ровно вдвое больше, чем моделей (вне пика и в пик).
// Разбор строгий: не сошлось хоть что-то — пусто, а не половина таблицы.
// Неверная цена хуже отсутствующей: по ней принимают решения.

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

var (
	reNameModel = regexp.MustCompile(`^[a-z][a-z0-9.\-]*-[a-z0-9.\-]+$`)
	rePrice     = regexp.MustCompile(`^\$\s?([0-9]+(?:\.[0-9]+)?)$`)
	reTagStrip  = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)
	rePeak      = regexp.MustCompile(`(?i)peak hours are\s+(\d{1,2}):(\d{2})\s*-\s*(\d{1,2}):(\d{2})\s+and\s+(\d{1,2}):(\d{2})\s*-\s*(\d{1,2}):(\d{2})\s*utc,\s*monday through friday`)
	// сноска рядом с именем модели: «deepseek-flash<sup>(1)</sup>»
	reFootCell = regexp.MustCompile(`^[\(\[]\d{1,2}[\)\]]$`)
)

// priceForModel — связка нашей модели с именем в прайсе. Сначала то, что
// указал человек, потом имя в API, потом наше название. Угадывать дальше
// не станем: «deepseek-chat» и «deepseek-v4-flash» — это одно и то же
// только по документации провайдера, а не по написанию.
func priceForModel(price map[string]string, c ModelConf) string {
	for _, name := range []string{c.PriceName, c.Model, c.Name} {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		for k, v := range price {
			if strings.EqualFold(k, name) {
				return v
			}
		}
	}
	return ""
}

// linePricesMatch — сравнение по числам, а не по написанию: «$0.22» и
// «$0.220 » — одна и та же цена, и дёргать человека из-за пробела не надо.
func linePricesMatch(a, b string) bool {
	if pa, _, oka := parsePerMinute(a); oka {
		pb, _, okb := parsePerMinute(b)
		return okb && math.Abs(pa-pb) < 0.0000001
	}
	a1, a2, a3, _ := parsePrices3(a)
	b1, b2, b3, _ := parsePrices3(b)
	closeTo := func(x, y float64) bool { return math.Abs(x-y) < 0.0000001 }
	return closeTo(a1, b1) && closeTo(a2, b2) && closeTo(a3, b3)
}

// cells — плоский список кусков текста между тегами. Таблица цен у
// провайдера свёрстана по столбцам: сначала имена всех моделей, потом
// разделы цен, и в каждом столько чисел, сколько моделей — сперва вне
// пика, потом в пик. По строкам её не прочесть.
func cells(html string) []string {
	var out []string
	for _, chunk := range strings.Split(reTagStrip.ReplaceAllString(html, "\n"), "\n") {
		if k := strings.TrimSpace(chunk); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// priceFromCells — цены по столбцам. Разбор строгий: имена моделей должны
// найтись, каждый раздел обязан дать ровно вдвое больше чисел, чем
// моделей (вне пика и в пик), иначе возвращаем пусто. Половина разобранной
// таблицы опаснее, чем ничего: по неверной цене принимают решения.
func priceFromCells(cells []string) map[string]string {
	model := findModels(cells)
	if len(model) == 0 {
		return nil
	}
	// заголовки разделов пересекаются словами: «OUTPUT PRICE (CACHE HIT)»
	// содержит и «output», и «cache». Поэтому для выхода нужен заголовок,
	// в котором про кэш не сказано вовсе
	cache := numbersAfter(cells, "cache hit", "", len(model)*2)
	in := numbersAfter(cells, "cache miss", "", len(model)*2)
	outPrices := numbersAfter(cells, "output", "cache", len(model)*2)
	if cache == nil || in == nil || outPrices == nil {
		return nil
	}
	schedule := peakSchedule(strings.Join(cells, " "))
	out := map[string]string{}
	n := len(model)
	for i, name := range model {
		usually := fmt.Sprintf("$%s / $%s / $%s / 1M", in[i], outPrices[i], cache[i])
		if schedule == "" {
			out[name] = usually
			continue
		}
		peak := fmt.Sprintf("$%s / $%s / $%s / 1M", in[n+i], outPrices[n+i], cache[n+i])
		out[name] = usually + " | " + peak + " | " + schedule
	}
	return out
}

// findModels — имена сразу после ячейки MODEL. Идут подряд, поэтому
// останавливаемся на первой ячейке, не похожей на имя модели. Сноска
// между именами («(1)» у deepseek-flash) — не конец списка: на ней
// разбор останавливался, находил одну модель вместо двух и раздавал
// цены одной модели другой.
func findModels(cells []string) []string {
	for i, c := range cells {
		if !strings.EqualFold(strings.TrimSpace(c), "MODEL") {
			continue
		}
		var out []string
		for _, name := range cells[i+1:] {
			name = strings.TrimSpace(name)
			if reFootCell.MatchString(name) {
				continue
			}
			if !reNameModel.MatchString(name) {
				break
			}
			out = append(out, name)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// numbersAfter — ровно `сколько` цен в разделе после ячейки-заголовка.
// Меньше или больше — значит вёрстка другая, и догадываться нельзя.
func numbersAfter(cells []string, title, except string, howMany int) []string {
	for i, c := range cells {
		bottom := strings.ToLower(strings.TrimSpace(c))
		if !strings.Contains(bottom, title) {
			continue
		}
		if except != "" && strings.Contains(bottom, except) {
			continue // это другой раздел, просто слово общее
		}
		var out []string
		for _, x := range cells[i+1:] {
			x = strings.TrimSpace(x)
			// между группами стоят подписи «OFF-PEAK» и «PEAK» — это не
			// конец раздела, а его середина. Останавливаемся только на
			// заголовке следующего раздела
			if otherSection(x) {
				break
			}
			m := rePrice.FindStringSubmatch(x)
			if m == nil {
				continue
			}
			out = append(out, m[1])
		}
		if len(out) == howMany {
			return out
		}
	}
	return nil
}

// otherSection — ячейка начинает следующий раздел таблицы. Числа за ним
// относятся уже не к нашей цене: «Concurrency Limit» тоже состоит из цифр,
// и спутать их с деньгами было бы дорого.
func otherSection(cell string) bool {
	bottom := strings.ToLower(cell)
	// «price» — заголовок раздела старой вёрстки («INPUT PRICE (CACHE MISS)»):
	// без него раздел не кончался на соседнем и считал чужие числа своими
	for _, word := range []string{"1m input", "1m output", "concurrency", "limit", "rate", "context length", "price"} {
		if strings.Contains(bottom, word) {
			return true
		}
	}
	return false
}

// peakSchedule — часы пика из сноски под таблицей. Не нашлось — пусто, и
// тогда пиковая цена не предлагается вовсе: цена без расписания
// бессмысленна.
func peakSchedule(text string) string {
	m := rePeak.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return fmt.Sprintf("пн-пт %s:%s-%s:%s,%s:%s-%s:%s UTC",
		m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8])
}
