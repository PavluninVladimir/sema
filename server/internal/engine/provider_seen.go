package engine

// Что провайдер предлагает сегодня.
//
// Кнопка «Подключить все модели» умела добавлять новое, но не умела
// сказать о пропавшем. 10 сентября 2026 DeepSeek переименовал модели:
// вместо `deepseek-reasoner` и `deepseek-chat` он отдаёт `deepseek-v4-pro`
// и `deepseek-flash`. Новые подключились, старые остались в списке — и
// экран показывал их наравне со всеми, хотя обращение к ним теперь
// заканчивается отказом.
//
// Хуже того, роли по-прежнему указывали на старые: «Фильтр писем» ходил к
// модели, которой у провайдера больше нет, и человек узнавал об этом из
// красной строки на «Здоровье», а не из списка моделей.
//
// Поэтому обновление списка теперь запоминает, что провайдер отдал, и
// экран сравнивает подключённое с этим списком. Сама Сёма ничего не
// удаляет: модель могла пропасть из списка на час, а решение отключать —
// человека. Её дело сказать.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const seenKey = "providers.seen."

// ProviderSeen — что провайдер отдавал в последний раз и когда.
type ProviderSeen struct {
	When  string   `json:"at"`
	Names []string `json:"ids"`
}

// RememberCatalog — сохранить список моделей, отданный провайдером.
func (e *Engine) RememberCatalog(provider string, model []CatalogModel) error {
	v := ProviderSeen{When: time.Now().Format(time.RFC3339)}
	for _, m := range model {
		v.Names = append(v.Names, m.ID)
	}
	sort.Strings(v.Names)
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return e.store.SetSetting(seenKey+provider, string(raw))
}

// ProviderCatalog — что запомнили в прошлый раз. Второе значение —
// спрашивали ли вообще: «не спрашивали» и «провайдер ничего не отдал» —
// разные вещи, и путать их значит объявить живые модели пропавшими.
func (e *Engine) ProviderCatalog(provider string) (ProviderSeen, bool) {
	raw := e.store.Setting(seenKey+provider, "")
	if strings.TrimSpace(raw) == "" {
		return ProviderSeen{}, false
	}
	var v ProviderSeen
	if err := json.Unmarshal([]byte(raw), &v); err != nil || len(v.Names) == 0 {
		return ProviderSeen{}, false
	}
	return v, true
}

// GoneAtProvider — подключённые модели, которых провайдер больше не
// предлагает. Пусто и когда всё на месте, и когда список не спрашивали:
// второе видно по `КаталогПровайдера`.
func (e *Engine) GoneAtProvider(provider string) []string {
	v, has := e.ProviderCatalog(provider)
	if !has {
		return nil
	}
	live := map[string]bool{}
	for _, id := range v.Names {
		live[id] = true
	}
	var gone []string
	catalog := e.Providers()
	for _, c := range e.userModels() {
		if providerOf(catalog, c.Type, c.BaseURL) != provider {
			continue
		}
		// сравниваем по имени модели у провайдера, а не по нашему имени:
		// «DeepseekPro» — это как её зовём мы, а провайдер знает
		// «deepseek-reasoner»
		if !live[c.Model] {
			gone = append(gone, c.Name)
		}
	}
	sort.Strings(gone)
	return gone
}

// ModelGone — эту конкретную модель провайдер больше не предлагает.
func (e *Engine) ModelGone(name string) bool {
	catalog := e.Providers()
	for _, c := range e.userModels() {
		if c.Name != name {
			continue
		}
		provider := providerOf(catalog, c.Type, c.BaseURL)
		if provider == "" {
			return false
		}
		v, has := e.ProviderCatalog(provider)
		if !has {
			return false
		}
		for _, id := range v.Names {
			if id == c.Model {
				return false
			}
		}
		return true
	}
	return false
}

// RefreshProviderModels — спросить у провайдера список заново.
// Возвращает человеческий итог: сколько добавлено, сколько было и чего у
// провайдера больше нет.
func (e *Engine) RefreshProviderModels(ctx context.Context, provider string,
	add func([]CatalogModel) (int, int, error)) (string, error) {

	p, ok := e.Provider(provider)
	if !ok {
		return "", fmt.Errorf("провайдера %q нет в справочнике", provider)
	}
	key := e.ProviderKey(p.Name)
	if p.NeedKey && key == "" {
		return "", fmt.Errorf("сначала задайте ключ провайдера")
	}
	model, err := e.Catalog(ctx, p, key)
	if err != nil {
		return "", err
	}
	if len(model) == 0 {
		// пустой список не запоминаем: иначе один сбой у провайдера
		// объявит пропавшими все модели разом
		return "", fmt.Errorf("%s не отдал ни одной модели", p.Name)
	}
	added, skipped := 0, 0
	if add != nil {
		added, skipped, err = add(model)
		if err != nil {
			return "", err
		}
	}
	if err := e.RememberCatalog(provider, model); err != nil {
		return "", err
	}
	result := fmt.Sprintf("%s: у провайдера %s", provider, plural(len(model), "модель", "модели", "моделей"))
	if added > 0 {
		result += fmt.Sprintf(" · подключено новых: %d", added)
	}
	if skipped > 0 {
		result += fmt.Sprintf(" · уже были: %d", skipped)
	}
	if gone := e.GoneAtProvider(provider); len(gone) > 0 {
		result += " · у провайдера больше нет: " + strings.Join(gone, ", ")
		if role := e.RolesOnMissing(gone); len(role) > 0 {
			result += " · на них смотрят роли: " + strings.Join(role, ", ")
		}
	}
	return result, nil
}

// RolesOnMissing — какие роли указывают на эти модели. Молча пропавшая
// модель у роли — это отказ при первом же обращении, а человек ищет
// причину на «Здоровье».
func (e *Engine) RolesOnMissing(model []string) []string {
	gone := map[string]bool{}
	for _, m := range model {
		gone[m] = true
	}
	var role []string
	for _, r := range e.Roles() {
		if gone[r.Model] || gone[r.Spare] {
			role = append(role, r.Name)
		}
	}
	return role
}

// ModelProviders — чья каждая подключённая модель. Список моделей у
// человека бывает в полсотни имён, и по имени не понять, чьё оно:
// «gpt-5.6-luna» бывает и у OpenAI, и у чужого шлюза, а «grok-4.6» и
// «kimi-k3» не говорят ничего вовсе.
func (e *Engine) ModelProviders() map[string]string {
	catalog := e.Providers()
	out := map[string]string{}
	for _, c := range e.userModels() {
		if p := providerOf(catalog, c.Type, c.BaseURL); p != "" {
			out[c.Name] = p
		}
	}
	return out
}
