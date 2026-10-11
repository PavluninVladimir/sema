package api

import (
	"fmt"
	"strconv"
	"strings"
)

// Действия с ценами и остатком провайдеров. Часть общего разбора действий:
// чужое действие — errUnhandled.

func (s *Server) pricesActions(req actionRequest) (string, error) {
	switch req.Action {
	case "check_prices":
		// проверка идёт в своей нитке: страница и модель занимают минуту, а
		// кнопка не должна висеть. Итог — карточка в «Уведомлениях» и строка
		// у провайдера
		if !s.eng.StartPriceCheck(req.Target) {
			return "", fmt.Errorf("цены «%s» уже проверяются — подождите итога", req.Target)
		}
		return "", nil
	case "save_price_source":
		kind := kindByAnyTitle(priceKindTitles, priceKindShort, req.Params["kind"])
		if kind == "" {
			return "", fmt.Errorf("выберите источник цен из списка")
		}
		return "", s.eng.SetPriceSource(req.Target, kind, req.Params["url"])
	case "save_balance_source":
		kind := kindByAnyTitle(balanceKindTitles, balanceKindShort, req.Params["kind"])
		if kind == "" {
			return "", fmt.Errorf("выберите источник остатка из списка")
		}
		low := 0.0
		if v := strings.TrimSpace(req.Params["low"]); v != "" {
			n, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimLeft(v, "$€₽¥ "), ",", "."), 64)
			if err != nil || n < 0 {
				return "", fmt.Errorf("порог — число вроде 5 или 2.50")
			}
			low = n
		}
		if err := s.eng.SetBalanceSource(req.Target, kind, low); err != nil {
			return "", err
		}
		// «было» вписывают вместе с формой; пустое поле — оставить прежнее
		if v := strings.TrimSpace(req.Params["amount"]); v != "" {
			if err := s.eng.SetBalanceBase(req.Target, v, req.Params["date"]); err != nil {
				return "", err
			}
		}
		return "", nil
	case "set_price_token":
		return "", s.eng.SetPriceToken(req.Target, req.Params["value"])
	case "save_price_skip":
		return "", s.eng.SetPriceSkipSections(req.Params["words"])
	case "set_management_key":
		return "", s.eng.SetManagementKey(req.Target, req.Params["value"])
	case "refresh_balance":
		if !s.eng.StartBalanceRefresh(req.Target) {
			return "", fmt.Errorf("остаток «%s» уже спрашивается", req.Target)
		}
		return "", nil
	}
	return "", errUnhandled
}
