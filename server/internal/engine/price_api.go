package engine

// Цены из JSON провайдера (источник `api`). Формат — OpenRouter: список
// `data`, у каждой модели `id` и `pricing` с ценой за токен строками:
// `prompt` — вход, `completion` — выход, `input_cache_read` — вход из кэша.
// Модель ответа не нужна: поля названы, разбор кодом, проверять нечем и
// незачем. Цена за токен переводится в цену за миллион.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// pricesFromAPI — цены из JSON. Наша модель узнаётся по имени в API
// (`ModelConf.Model`) без поправок на регистр: у OpenRouter имя вместе с
// приставкой автора, «openai/gpt-5.6-luna», и угадывать нечего.
func (e *Engine) pricesFromAPI(p ProviderConf, models []ModelConf) ([]foundPrice, priceStats, error) {
	var st priceStats
	if p.PriceURL == "" {
		return nil, st, fmt.Errorf("адрес JSON с ценами не задан")
	}
	raw, _, err := httpFetch("GET", p.PriceURL, "", "", priceHeaders(e.PriceToken(p.Name)))
	if err != nil {
		return nil, st, fmt.Errorf("адрес не открылся: %w", err)
	}
	var reply struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt         flexNum `json:"prompt"`
				Completion     flexNum `json:"completion"`
				InputCacheRead flexNum `json:"input_cache_read"`
				// цена длинного контекста: в предложение идёт короткий, а
				// длинный — припиской, чтобы человек знал, что цена не одна
				Overrides []struct {
					MinPromptTokens int     `json:"min_prompt_tokens"`
					Prompt          flexNum `json:"prompt"`
					Completion      flexNum `json:"completion"`
				} `json:"overrides"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		return nil, st, fmt.Errorf("ответ не разобран как список моделей с ценами: %v", err)
	}
	if len(reply.Data) == 0 {
		return nil, st, fmt.Errorf("в ответе нет моделей")
	}
	type perToken struct {
		in, out, cache float64
		note           string
	}
	byID := map[string]perToken{}
	for _, m := range reply.Data {
		t := perToken{in: float64(m.Pricing.Prompt), out: float64(m.Pricing.Completion), cache: float64(m.Pricing.InputCacheRead)}
		if o := m.Pricing.Overrides; len(o) > 0 && o[0].MinPromptTokens > 0 {
			t.note = fmt.Sprintf("длинный контекст, от %dK токенов: вход $%s, выход $%s", o[0].MinPromptTokens/1000,
				numStr(perMillion(float64(o[0].Prompt))), numStr(perMillion(float64(o[0].Completion))))
		}
		byID[strings.ToLower(m.ID)] = t
	}
	var out []foundPrice
	for _, c := range models {
		t, ok := byID[strings.ToLower(c.Model)]
		if !ok {
			continue
		}
		st.Asked++
		in, outp, cache := perMillion(t.in), perMillion(t.out), perMillion(t.cache)
		switch {
		case t.in < 0 || t.out < 0:
			continue // у роутеров вроде openrouter/auto цена -1: цены нет, а не «минус один»
		case in == 0 && outp == 0:
			st.Free++
			continue // бесплатная: считать нечего, а цену «ноль» SetModelPrice не принимает
		case outp < in || cache > in:
			st.drop(c.Name + ": порядок цен не сходится")
			continue
		}
		out = append(out, foundPrice{Model: c.Name, PageName: c.Model, Price: tokenPrice(in, outp, cache), Note: t.note})
	}
	return out, st, nil
}

// perMillion — цена за токен → за миллион токенов. Округление до шести
// знаков убирает хвост двоичной арифметики: 0.0000005 × 1e6 не должно
// стать 0.49999999999999994.
func perMillion(perToken float64) float64 {
	if perToken <= 0 {
		return 0
	}
	return math.Round(perToken*1e6*1e6) / 1e6
}
