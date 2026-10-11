package engine

// Цена модели полями.
//
// На экране модели цена правилась мини-языком в текстовом вопросе: «$0.22 /
// $0.66 / $0.007 / 1M | $0.44 / $1.32 / $0.014 / 1M | пн-пт 01:00-04:00 UTC».
// Текущее значение не видно, формат надо помнить, ошибка приходит после «ОК».
// Теперь это поля — вход, выход, вход из кэша, за минуту звука, часы пик, — а
// сервер по-прежнему хранит ровно ту же строку: цену собирает из полей
// `Line`, а прочитать её обратно в поля умеет `PricePartsOf`. Разбор цены и
// расчёт расхода (`parsePrices3`, `parsePerMinute`, `costOfUsage`) не
// менялись: поля — это другой вход в ту же строку, а не другая цена.

import (
	"fmt"
	"strconv"
	"strings"
)

// Знаки валюты, которые разбирает parsePrice.
var PriceCurrencies = []string{"$", "€", "₽"}

// PriceParts — цена модели по полям. Числа — текстом, как их вписал человек;
// пустое поле — «не названо».
type PriceParts struct {
	Currency string // «$», «€» или «₽»; пусто — «$»
	// цена за миллион токенов: вход, выход (пусто — как вход), вход из кэша
	In, Out, Cache string
	// цена за минуту звука: у моделей речи вместо цены за токены
	Minute string
	// тариф в часы пик и когда эти часы: «пн-пт 01:00-04:00,06:00-10:00 UTC»
	PeakIn, PeakOut, PeakCache string
	Schedule                   string
}

// IsPerMinute — цена за минуту звука.
func (p PriceParts) IsPerMinute() bool { return strings.TrimSpace(p.Minute) != "" }

// HasPeak — часы пик заданы хоть чем-то.
func (p PriceParts) HasPeak() bool {
	return strings.TrimSpace(p.PeakIn) != "" || strings.TrimSpace(p.PeakOut) != "" ||
		strings.TrimSpace(p.PeakCache) != "" || strings.TrimSpace(p.Schedule) != ""
}

// priceNum — число без хвоста нулей: «2.5», а не «2.500000».
func priceNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// PricePartsOfLine — строка цены в поля: цена, пиковая цена и расписание — те
// три части, на которые строку делит SetModelPrice. Что не разобралось, в
// поля не попадает: пустое поле честнее выдуманного числа.
func PricePartsOfLine(price, peak, schedule string) PriceParts {
	var p PriceParts
	if price = strings.TrimSpace(price); price != "" {
		if v, cur, ok := parsePerMinute(price); ok {
			p.Currency, p.Minute = cur, priceNum(v)
		} else if in, out, cache, cur := parsePrices3(price); in > 0 || out > 0 {
			p.Currency, p.In, p.Out = cur, priceNum(in), priceNum(out)
			if cache > 0 {
				p.Cache = priceNum(cache)
			}
		}
	}
	if peak = strings.TrimSpace(peak); peak != "" {
		if in, out, cache, cur := parsePrices3(peak); in > 0 || out > 0 {
			if p.Currency == "" {
				p.Currency = cur
			}
			p.PeakIn, p.PeakOut = priceNum(in), priceNum(out)
			if cache > 0 {
				p.PeakCache = priceNum(cache)
			}
		}
	}
	p.Schedule = strings.TrimSpace(schedule)
	return p
}

// PricePartsOf — цена названной модели по полям; false — такой модели нет.
func (e *Engine) PricePartsOf(model string) (PriceParts, bool) {
	c, ok := e.modelConf(model)
	if !ok {
		return PriceParts{}, false
	}
	return PricePartsOfLine(c.Price, c.PricePeak, c.Peak), true
}

// priceField — число из поля цены: пусто и ноль — «не названо» (false),
// мусор и отрицательное — ошибка с названием поля. Запятая у десятичных
// принимается: так пишут по-русски.
func priceField(label, text string) (float64, bool, error) {
	text = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(text), "$€₽ "))
	if text == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(text, ",", "."), 64)
	if err != nil || v < 0 {
		return 0, false, fmt.Errorf("«%s» — число вроде 2.50, а не %q", label, text)
	}
	return v, v > 0, nil
}

// tokensPrice — «$in / $out / $cache / 1M» в самой короткой форме, какую
// читает parsePrices3: одна цена на оба вида, вход и выход, плюс кэш.
func tokensPrice(cur string, in, out, cache float64) string {
	switch {
	case cache > 0:
		return fmt.Sprintf("%s%s / %s%s / %s%s / 1M", cur, priceNum(in), cur, priceNum(out), cur, priceNum(cache))
	case in == out:
		return fmt.Sprintf("%s%s / 1M", cur, priceNum(in))
	}
	return fmt.Sprintf("%s%s / %s%s / 1M", cur, priceNum(in), cur, priceNum(out))
}

// Compose — цена, пиковая цена и расписание из полей: те же три части, что
// принимает SetModelPrice. Ошибка называет поле, в котором дело.
func (p PriceParts) Compose() (price, peak, schedule string, err error) {
	cur := strings.TrimSpace(p.Currency)
	if cur == "" {
		cur = "$"
	}
	known := false
	for _, c := range PriceCurrencies {
		known = known || c == cur
	}
	if !known {
		return "", "", "", fmt.Errorf("валюта — $, € или ₽")
	}
	in, hasIn, err := priceField("Вход", p.In)
	if err != nil {
		return "", "", "", err
	}
	out, hasOut, err := priceField("Выход", p.Out)
	if err != nil {
		return "", "", "", err
	}
	cache, hasCache, err := priceField("Вход из кэша", p.Cache)
	if err != nil {
		return "", "", "", err
	}
	minute, hasMinute, err := priceField("За минуту звука", p.Minute)
	if err != nil {
		return "", "", "", err
	}
	if hasMinute {
		if hasIn || hasOut || hasCache {
			return "", "", "", fmt.Errorf("цена либо за миллион токенов, либо за минуту звука — не обе сразу: " +
				"у модели речи оставьте только «За минуту звука»")
		}
		if p.HasPeak() {
			return "", "", "", fmt.Errorf("часы пик у цены за минуту звука не работают: очистите их поля")
		}
		return fmt.Sprintf("%s%s / мин", cur, priceNum(minute)), "", "", nil
	}
	switch {
	case !hasIn && !hasOut && !hasCache:
		// цены нет — это тоже ответ: расход по модели считаться не будет
		if p.HasPeak() {
			return "", "", "", fmt.Errorf("часы пик без обычной цены бессмысленны: сначала впишите вход и выход")
		}
		return "", "", "", nil
	case !hasIn:
		return "", "", "", fmt.Errorf("«Вход» пуст: от него считается и выход, и кэш")
	case !hasOut:
		out = in // одна цена на оба вида — так читает её и parsePrices
	}
	price = tokensPrice(cur, in, out, cache)

	if !p.HasPeak() {
		return price, "", "", nil
	}
	pin, hasPin, err := priceField("Вход в часы пик", p.PeakIn)
	if err != nil {
		return "", "", "", err
	}
	pout, hasPout, err := priceField("Выход в часы пик", p.PeakOut)
	if err != nil {
		return "", "", "", err
	}
	pcache, _, err := priceField("Вход из кэша в часы пик", p.PeakCache)
	if err != nil {
		return "", "", "", err
	}
	if !hasPin {
		if hasPout {
			return "", "", "", fmt.Errorf("«Вход в часы пик» пуст: от него считается и выход")
		}
		return "", "", "", fmt.Errorf("часы пик заданы, а цена в них — нет: впишите вход и выход в пик или очистите расписание")
	}
	if !hasPout {
		pout = pin
	}
	schedule = strings.TrimSpace(p.Schedule)
	if schedule == "" {
		return "", "", "", fmt.Errorf("цена в часы пик без расписания бессмысленна: впишите, когда они, — например «пн-пт 01:00-04:00 UTC»")
	}
	if len(parsePeak(schedule)) == 0 {
		return "", "", "", fmt.Errorf("расписание не разобрал: ждал что-то вроде «пн-пт 01:00-04:00,06:00-10:00 UTC»")
	}
	return price, tokensPrice(cur, pin, pout, pcache), schedule, nil
}

// Line — цена одной строкой в том формате, который хранит и читает сервер:
// «цена | пик | расписание», а без пика — одна цена.
func (p PriceParts) Line() (string, error) {
	price, peak, schedule, err := p.Compose()
	if err != nil {
		return "", err
	}
	if peak == "" {
		return price, nil
	}
	return price + " | " + peak + " | " + schedule, nil
}

// SetModelPriceParts — сохранить цену, собранную из полей. Если с тем, что
// уже стоит у модели, цена не разошлась — ничего не пишется: запись
// отмечает цену «вписана вами» и ставит дату, и форма, где поменяли только
// имя в прайсе, не должна отнимать у цены отметку «с прайса провайдера».
func (e *Engine) SetModelPriceParts(name string, p PriceParts) error {
	line, err := p.Line()
	if err != nil {
		return err
	}
	was, ok := e.PricePartsOf(name)
	if !ok {
		return fmt.Errorf("модели %q нет", name)
	}
	if wasLine, werr := was.Line(); werr == nil && wasLine == line {
		return nil
	}
	return e.SetModelPrice(name, line)
}

// PriceMissing — модели, у которых цена не названа, а деньги за них платят.
func (e *Engine) PriceMissing() map[string]bool {
	out := map[string]bool{}
	for _, c := range e.ModelCards() {
		if c.NoPrice() {
			out[c.Name] = true
		}
	}
	return out
}
