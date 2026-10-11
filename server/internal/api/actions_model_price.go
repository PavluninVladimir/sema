package api

import (
	"fmt"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// saveModelPrice — «Сохранить» у цены модели. Форма на экране модели шлёт
// поля: `in`, `out`, `cache`, `minute`, `schedule`, `peak_in`, `peak_out`,
// `peak_cache`, `currency` и `price_name` (имя в прайсе); непришедшее поле — «не
// трогали». Строку в прежнем формате собирает из них движок (PriceParts), и
// пишется она тем же SetModelPrice, так что отметка «вписана вами» ставится
// ровно когда цена и правда поменялась.
//
// Старый вызов с `value` — мини-языком, ответом на вопрос — работает как
// раньше: на него могут смотреть карточки в «Уведомлениях» и скрипты.
func (s *Server) saveModelPrice(req actionRequest) (string, error) {
	name := req.Target
	if line, legacy := req.Params["value"]; legacy {
		if err := s.eng.SetModelPrice(name, line); err != nil {
			return "", err
		}
		s.setLastCheck(priceSavedLine(name, strings.TrimSpace(line) == ""))
		return "", nil
	}

	card, ok := s.eng.ModelCardOf(name)
	if !ok {
		return "", fmt.Errorf("модели %q нет", name)
	}
	parts, _ := s.eng.PricePartsOf(name)
	touched := false
	set := func(param string, to *string) {
		if v, present := req.Params[param]; present {
			*to, touched = v, true
		}
	}
	set("in", &parts.In)
	set("out", &parts.Out)
	set("cache", &parts.Cache)
	set("minute", &parts.Minute)
	set("schedule", &parts.Schedule)
	set("peak_in", &parts.PeakIn)
	set("peak_out", &parts.PeakOut)
	set("peak_cache", &parts.PeakCache)
	set("currency", &parts.Currency)

	// сначала проверяется всё, потом пишется: половина формы не сохраняется
	if touched {
		if _, err := parts.Line(); err != nil {
			return "", err
		}
	}
	if v, present := req.Params["price_name"]; present && strings.TrimSpace(v) != card.PriceName {
		if err := s.eng.SetPriceName(name, v); err != nil {
			return "", err
		}
		s.setLastCheck(name + ": связка с прайсом сохранена — цены будут узнаваться на странице провайдера")
	}
	if touched {
		was, _ := s.eng.PricePartsOf(name)
		if err := s.eng.SetModelPriceParts(name, parts); err != nil {
			return "", err
		}
		if changed, _ := priceChanged(was, parts); changed {
			line, _ := parts.Line()
			s.setLastCheck(priceSavedLine(name, line == ""))
		}
	}
	return "", nil
}

// priceChanged — разошлись ли две цены так, что их строки разные: форма, где
// поменяли только имя в прайсе, цену не меняет.
func priceChanged(was, now engine.PriceParts) (bool, error) {
	a, err := was.Line()
	if err != nil {
		return true, nil
	}
	b, err := now.Line()
	if err != nil {
		return false, err
	}
	return a != b, nil
}

// priceSavedLine — что сказать после сохранения цены.
func priceSavedLine(model string, removed bool) string {
	if removed {
		return model + ": цена убрана — расход по модели считаться не будет"
	}
	return model + ": цена сохранена — расход считается со следующих вызовов, прошлые записи остаются как есть"
}

// setLastCheck — итог последнего действия: его покажет экран, с которого
// нажали кнопку.
func (s *Server) setLastCheck(line string) {
	s.mu.Lock()
	s.lastCheck = line
	s.mu.Unlock()
}
