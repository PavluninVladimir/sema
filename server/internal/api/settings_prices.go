package api

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Цены и остаток провайдера: откуда они берутся, когда это проверяли и что
// вышло. Раньше это была вкладка «Провайдеры и цены» со свёрнутым разделом на
// каждого провайдера; теперь это вкладки «Цены» и «Остаток» на экране самого
// провайдера (settings_provider.go), а здесь — строки и формы, из которых они
// складываются. Новых компонентов нет: строки, формы и разделы, которые клиент
// уже умеет.

// Подписи видов источника. Значение в форме приходит подписью, обратно её
// достаёт `kindByTitle`; короткие подписи сегментов — `priceKindShort` и
// `balanceKindShort`, их читает `kindByAnyTitle`.
var priceKindTitles = []struct{ kind, title string }{
	{engine.PricePage, "Страница цен (разбирает модель)"},
	{engine.PriceAPI, "JSON с ценами (формат OpenRouter)"},
	{engine.PriceReported, "Провайдер называет цену сам"},
	{engine.PriceNone, "Не брать цены"},
}

var balanceKindTitles = []struct{ kind, title string }{
	{engine.BalanceAPI, "Спрашивать у провайдера"},
	{engine.BalanceEstimate, "Оценка: было минус расход Сёмы"},
	{engine.BalanceNone, "Не показывать"},
}

func kindTitle(list []struct{ kind, title string }, kind string) string {
	for _, k := range list {
		if k.kind == kind {
			return k.title
		}
	}
	return list[len(list)-1].title // пусто — «не берём»
}

func kindByTitle(list []struct{ kind, title string }, title string) string {
	title = strings.TrimSpace(title)
	for _, k := range list {
		if k.title == title || k.kind == title {
			return k.kind
		}
	}
	return ""
}

func kindOptions(list []struct{ kind, title string }) []string {
	out := make([]string, 0, len(list))
	for _, k := range list {
		out = append(out, k.title)
	}
	return out
}

// priceTokenRow — токен источника цен. Нужен, только если адрес с ценами закрыт:
// уходит заголовком Authorization: Bearer и только на этот адрес. Значение
// экрану не отдаётся никогда — «задан» или нет.
func (s *Server) priceTokenRow(p engine.ProviderConf) Component {
	value, level := "не задан — источник открытый", LevelInfo
	if s.eng.PriceToken(p.Name) != "" {
		value, level = "задан", LevelOK
	}
	return Component{Type: "row", Layout: "wide", ID: "pricetoken-" + p.Name, Subtitle: "Токен источника цен",
		Caption: "нужен, только если адрес с ценами требует ключ: он уходит заголовком Authorization: Bearer на этот адрес и " +
			"больше никуда. Ключ для запросов к моделям сюда не подставляется",
		Level: level, Dot: levelDot(level), Facts: []Prop{{Label: "Токен", Value: value}},
		Actions: []Action{{Action: "set_price_token", Target: p.Name, Label: "Задать токен",
			Prompt: "Токен для адреса с ценами «" + p.Name + "» · пусто — убрать", Secure: true}}}
}

// balanceRow — строка остатка и, где нужен, строка ключа управления.
func (s *Server) balanceRow(p engine.ProviderConf) []Component {
	if p.BalanceKind == engine.BalanceNone || p.BalanceKind == "" {
		return []Component{{Type: "row", Layout: "wide", ID: "balrow-" + p.Name, Subtitle: "Остаток на счёте",
			Caption: "остаток у этого провайдера не показывается; выберите источник выше"}}
	}
	var current *engine.Balance
	for _, b := range s.eng.BalancesFromCache() {
		if b.Provider == p.Name {
			b := b
			current = &b
		}
	}
	level, caption := LevelInfo, "остаток ещё не спрашивали"
	facts := []Prop{{Label: "Источник", Value: kindTitle(balanceKindTitles, p.BalanceKind)}}
	if p.BalanceLow > 0 {
		facts = append(facts, Prop{Label: "Порог", Value: fmt.Sprintf("%g", p.BalanceLow)})
	}
	if current != nil {
		caption = current.Text()
		switch {
		case current.Err != "":
			level = LevelWarn
		case current.Kind == engine.BalanceAPI && !current.Enough && !current.At.IsZero():
			level = LevelAlert
		case current.Low > 0 && len(current.Amounts) > 0 && current.Amounts[0].Total < current.Low:
			level = LevelWarn
		case !current.At.IsZero():
			level = LevelOK
		}
	}
	row := Component{Type: "row", Layout: "wide", ID: "balrow-" + p.Name, Subtitle: "Остаток на счёте",
		Caption: caption, Level: level, Dot: levelDot(level), Facts: facts}
	if p.BalanceKind == engine.BalanceAPI {
		row.Actions = []Action{{Action: "refresh_balance", Target: p.Name, Label: "Спросить сейчас"}}
	}
	out := []Component{row}
	if p.BalanceKind == engine.BalanceAPI && strings.Contains(p.BaseURL, "openrouter.ai") {
		value, lv := "не задан", LevelWarn
		if s.eng.ManagementKey(p.Name) != "" {
			value, lv = "задан", LevelOK
		}
		out = append(out, Component{Type: "row", Layout: "wide", ID: "mgmt-" + p.Name, Subtitle: "Ключ управления",
			Caption: "остаток OpenRouter отдаёт только ключ управления (Management key): openrouter.ai/settings/management-keys. " +
				"Обычный ключ адрес не пускает; ключ управления к моделям не ходит и лежит отдельно",
			Level: lv, Dot: levelDot(lv), Facts: []Prop{{Label: "Ключ", Value: value}},
			Actions: []Action{{Action: "set_management_key", Target: p.Name, Label: "Задать ключ управления",
				Prompt: "Ключ управления OpenRouter · нужен только для остатка на счёте", Secure: true}}})
	}
	return out
}

// balanceForm — источник остатка, порог и «было столько-то на дату». Остаток
// «у провайдера» предлагается там, где его адрес документирован (DeepSeek,
// OpenRouter); у остальных — оценка: гадать по недокументированным адресам
// нельзя.
func (s *Server) balanceForm(p engine.ProviderConf) Component {
	low := ""
	if p.BalanceLow > 0 {
		low = fmt.Sprintf("%g", p.BalanceLow)
	}
	base, has := s.eng.BalanceBaseOf(p.Name)
	amount, date := "", time.Now().Format("2006-01-02")
	hint := "для оценки: сколько на счёте по личному кабинету и на какую дату — Сёма вычтет свой расход после неё"
	if has {
		amount = fmt.Sprintf("%s%g", base.Currency, base.Amount)
		date = base.Date
		hint = fmt.Sprintf("сейчас в оценке: %s%g на %s. Пополнили счёт — впишите новое «было»", base.Currency, base.Amount, base.Date)
	}
	kinds := make([]string, 0, len(balanceKindShort))
	for _, k := range balanceKindShort {
		if k.kind == engine.BalanceAPI && !engine.CanAskBalance(p.BaseURL) && p.BalanceKind != engine.BalanceAPI {
			continue
		}
		kinds = append(kinds, k.title)
	}
	return Component{Type: "form", ID: "bal-" + p.Name, Style: formRows, Fields: []Field{
		{Type: "segments", Name: "kind", Label: "Откуда остаток", Options: kinds, Value: kindTitle(balanceKindShort, p.BalanceKind),
			Hint: "спрашивать можно только там, где есть документированный адрес (DeepSeek, OpenRouter); " +
				"у остальных — оценка: «было» из кабинета минус расход Сёмы, траты вне Сёмы ей не видны"},
		{Type: "text", Name: "low", Label: "Предупредить, когда меньше", Value: low,
			Hint: "в валюте счёта; пусто — не предупреждать. Приходит уведомление, для оценки — с пометкой «по оценке»; " +
				"у тех, кто сам называет остаток, это тревога: следующий вызов модели просто не пройдёт"},
		{Type: "text", Name: "amount", Label: "На счёте было", Value: amount, Hint: hint},
		{Type: "text", Name: "date", Label: "на дату", Value: date, Hint: "ГГГГ-ММ-ДД"},
	}, Submit: &Action{Action: "save_balance_source", Target: p.Name, Label: "Сохранить"}}
}

// shortURL — адрес без схемы и хвоста: хост и путь.
func shortURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host + strings.TrimRight(u.Path, "/")
}
