package engine

// Справочник провайдеров моделей. Человек выбирает провайдера, а не
// вписывает адрес: адрес, протокол и подсказка про ключ лежат в базе.
// Первичное заполнение — это данные, а не поведение: любую запись можно
// поменять или завести свою.

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const providersKey = "providers.json"

// providersSeed — метка досыпанного. Справочник заполняется один раз, а
// новые пресеты появляются позже: без метки они либо не появятся вовсе
// (список уже не пустой), либо вернутся удалённые человеком.
const providersSeedKey = "providers.seed"

// providersSeedNow — что досыпано на сегодня. Растёт вместе со списком.
const providersSeedNow = "7"

// providersMineKey — что Сёма засеял сам. По нему видно, правил ли
// человек пресет: совпадает с засеянным — не правил, можно обновлять.
const providersMineKey = "providers.seeded"

type ProviderConf struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`     // openai | anthropic | cli | perplexity | fal
	BaseURL string `json:"base_url"` // для cli — команда
	Hint    string `json:"hint"`     // где взять ключ или что установить
	NeedKey bool   `json:"need_key"`
	Catalog bool   `json:"catalog"` // умеет отдавать список моделей

	// Откуда провайдер берёт цены и остаток. Источник — у провайдера, а не
	// одной настройкой на всех: у каждого своя страница цен, а остаток
	// одни называют адресом, другим его можно только прикинуть
	// (price_watch.go, balance.go).
	PriceKind   string  `json:"price_kind,omitempty"`   // api | page | reported | none
	PriceURL    string  `json:"price_url,omitempty"`    // адрес JSON или страницы цен
	BalanceKind string  `json:"balance_kind,omitempty"` // api | estimate | none
	BalanceLow  float64 `json:"balance_low,omitempty"`  // «предупредить, когда меньше»; 0 — без порога
}

// Виды источника цен.
const (
	PriceAPI      = "api"      // JSON с ценами за токен (формат OpenRouter)
	PricePage     = "page"     // страница цен, разбирает модель, проверяет код
	PriceReported = "reported" // провайдер называет цену сам в каждом ответе
	PriceNone     = "none"
)

// Виды источника остатка.
const (
	BalanceAPI      = "api"      // документированный адрес провайдера
	BalanceEstimate = "estimate" // «было столько-то на такую-то дату» минус расход Сёмы
	BalanceNone     = "none"
)

var seedProviders = []ProviderConf{
	{Name: "OpenRouter", Kind: "openai", BaseURL: "https://openrouter.ai/api/v1",
		Hint: "openrouter.ai/keys · один ключ — сотни моделей", NeedKey: true, Catalog: true,
		PriceKind: PriceAPI, PriceURL: "https://openrouter.ai/api/v1/models", BalanceKind: BalanceAPI},
	{Name: "OpenAI", Kind: "openai", BaseURL: "https://api.openai.com/v1",
		Hint: "platform.openai.com/api-keys · оплата по токенам, подписка ChatGPT сюда не входит", NeedKey: true, Catalog: true,
		PriceKind: PricePage, PriceURL: "https://developers.openai.com/api/docs/pricing", BalanceKind: BalanceEstimate},
	{Name: "Anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com",
		Hint: "console.anthropic.com · оплата по токенам", NeedKey: true, Catalog: true,
		PriceKind: PricePage, PriceURL: "https://platform.claude.com/docs/en/about-claude/pricing", BalanceKind: BalanceEstimate},
	{Name: "DeepSeek", Kind: "openai", BaseURL: "https://api.deepseek.com/v1",
		Hint: "platform.deepseek.com", NeedKey: true, Catalog: true,
		PriceKind: PricePage, PriceURL: "https://api-docs.deepseek.com/quick_start/pricing/", BalanceKind: BalanceAPI},
	{Name: "Groq", Kind: "openai", BaseURL: "https://api.groq.com/openai/v1",
		Hint: "console.groq.com/keys · очень быстрые ответы", NeedKey: true, Catalog: true},
	{Name: "Google Gemini", Kind: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		Hint: "aistudio.google.com/apikey", NeedKey: true, Catalog: true,
		PriceKind: PricePage, PriceURL: "https://ai.google.dev/gemini-api/docs/pricing", BalanceKind: BalanceEstimate},
	{Name: "LM Studio (в своей сети)", Kind: "openai", BaseURL: "http://127.0.0.1:1234/v1",
		Hint: "сервер моделей на своей машине или в своей сети · ключ не нужен · из контейнера 127.0.0.1 — это сам контейнер, укажите имя хозяина · доверие закрытому включается отдельно", Catalog: true},
	{Name: "Ollama (в своей сети)", Kind: "openai", BaseURL: "http://127.0.0.1:11434/v1",
		Hint: "сервер моделей на своей машине или в своей сети · ключ не нужен · из контейнера укажите имя хозяина", Catalog: true},
	{Name: "Claude Code (подписка)", Kind: "cli", BaseURL: "claude",
		Hint: "официальный клиент Anthropic на этой машине · оплата подпиской, лимиты подписки"},
	{Name: "Codex (подписка)", Kind: "cli", BaseURL: "codex",
		Hint: "официальный клиент OpenAI на этой машине · оплата подпиской, лимиты подписки"},
	{Name: "Perplexity", Kind: "perplexity", BaseURL: "https://api.perplexity.ai",
		Hint: "console.perplexity.ai · один ключ на Anthropic, OpenAI, Google и xAI; " +
			"в роли «Поиск» та же модель ходит в сеть сама и приносит ссылки",
		NeedKey: true, Catalog: true,
		PriceKind: PriceReported, BalanceKind: BalanceEstimate},
	// fal.ai рисует картинки, а не отвечает в чате: его модели в роли не
	// попадают, ключ нужен для картинок к постам. Модель Сёма берёт сам —
	// самую дешёвую по ценам fal
	{Name: FalProvider, Kind: "fal", BaseURL: "https://fal.ai",
		Hint:    "fal.ai/dashboard/keys · картинки к постам · самую дешёвую модель Сёма подберёт сам по ценам fal",
		NeedKey: true},
	{Name: "Свой адрес", Kind: "openai", BaseURL: "",
		Hint: "любой OpenAI-совместимый сервер: vLLM, llama.cpp, корпоративный шлюз", NeedKey: true, Catalog: true},
}

// Providers — справочник; при первом обращении заполняется пресетами.
func (e *Engine) Providers() []ProviderConf {
	ver := e.store.SettingVersion(providersKey)
	e.cacheMu.Lock()
	if e.provOK && e.provVer == ver {
		out := append([]ProviderConf(nil), e.provList...)
		e.cacheMu.Unlock()
		return out
	}
	e.cacheMu.Unlock()
	raw := e.store.Setting(providersKey, "")
	if raw == "" {
		_ = e.SaveProviders(seedProviders)
		e.rememberSeeded(seedProviders)
		// первое заполнение — тоже досыпка: без метки следующий же вызов
		// вернул бы удалённое человеком
		_ = e.store.SetSetting(providersSeedKey, providersSeedNow)
		// копия: вызывающий правит строки списка, и пресеты в памяти не
		// должны меняться под его руками
		return append([]ProviderConf(nil), seedProviders...)
	}
	var out []ProviderConf
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out) == 0 {
		return append([]ProviderConf(nil), seedProviders...)
	}
	full := e.topUpProviders(out)
	// досыпка могла записать справочник — тогда версия уже другая, и
	// следующий вызов разберёт его один раз заново
	e.cacheMu.Lock()
	e.provVer, e.provOK, e.provList = ver, true, append([]ProviderConf(nil), full...)
	e.cacheMu.Unlock()
	return full
}

// renamedProviders — пресеты, которые переименовались. Без этого список у
// того, кто уже запускался, показывал бы и старое имя, и новое.
var renamedProviders = map[string]string{
	"LM Studio (локально)":      "LM Studio (в своей сети)",
	"Ollama (локально)":         "Ollama (в своей сети)",
	"Perplexity (поиск)":        "Perplexity",
	"Perplexity · поиск в сети": "Perplexity",
	"Perplexity · модели":       "Perplexity",
}

// topUpProviders — досыпать новые пресеты в уже заполненный справочник.
// Одного раза достаточно: удалённое человеком не возвращается.
func (e *Engine) topUpProviders(have []ProviderConf) []ProviderConf {
	// Экраны и роли спрашивают справочник разом при старте: без замка два
	// прохода видят разные списки, каждый пишет своё, и обновление
	// пресетов теряется — так и случилось с Perplexity.
	e.provMu.Lock()
	defer e.provMu.Unlock()
	if raw := e.store.Setting(providersKey, ""); raw != "" {
		// перечитываем под замком: пока ждали, список мог поменяться
		var latest []ProviderConf
		if json.Unmarshal([]byte(raw), &latest) == nil && len(latest) > 0 {
			have = latest
		}
	}
	// Переименования проверяются всегда, а не по метке: метку ставит
	// первый же запуск, и переименование, пришедшее позже, иначе
	// никогда бы не применилось — в списке остались бы близнецы.
	var kept []ProviderConf
	seen := map[string]bool{}
	renamed := 0
	// touched — что в этот раз привели к пресету: только про них Сёма
	// «помнит засеянное». Про остальные память не трогаем: иначе правка
	// человека (адрес страницы цен, порог остатка) записалась бы как
	// «засеянная Сёмой», и следующее обновление пресета её затёрло бы
	touched := map[string]bool{}
	for _, p := range have {
		if to, ok := renamedProviders[p.Name]; ok && to != p.Name {
			// пресет переименовался — значит поменялся сам, а не вывеска:
			// берём его нынешний вид целиком, иначе останутся старые
			// адрес, подсказка и «списка не отдаёт»
			p.Name = to
			if fresh, ok := seedProvider(to); ok {
				p = fresh
			}
			touched[to] = true
			renamed++
		}
		if seen[p.Name] {
			continue // два старых имени схлопнулись в одно
		}
		seen[p.Name] = true
		kept = append(kept, p)
	}
	dropped := len(have) - len(kept)
	have = kept

	// Обновление пресетов: запись, которую человек не правил (она совпадает
	// с тем, что Сёма засеял), принимает нынешний вид — адрес, подсказку,
	// есть ли список моделей. Правленое человеком остаётся как есть.
	mine := e.seededProviders()
	// Памяти о засеянном ещё нет (она появилась позже самого справочника) —
	// значит про правки человека мы ничего не знаем и считаем пресеты
	// нашими: один раз приводим их к нынешнему виду и с этого мига помним.
	firstMemory := len(mine) == 0
	refreshed := 0
	for i, p := range have {
		fresh, isSeed := seedProvider(p.Name)
		if !isSeed || fresh == p {
			continue
		}
		was, known := mine[p.Name]
		if firstMemory || (known && was == p) {
			have[i] = fresh
			touched[p.Name] = true
			refreshed++
		}
	}

	added := 0
	if e.store.Setting(providersSeedKey, "") != providersSeedNow {
		var fresh []ProviderConf
		for _, p := range seedProviders {
			if !seen[p.Name] {
				fresh = append(fresh, p)
			}
		}
		if added = len(fresh); added > 0 {
			have = insertBeforeOwn(have, fresh)
			for _, p := range fresh {
				touched[p.Name] = true
			}
		}
		_ = e.store.SetSetting(providersSeedKey, providersSeedNow)
	}
	if added+renamed+dropped+refreshed > 0 {
		_ = e.SaveProviders(have)
		e.rememberSeededOnly(have, touched)
		log.Printf("модели: справочник провайдеров обновлён — новых %d, переименовано %d, близнецов убрано %d, обновлено %d",
			added, renamed, dropped, refreshed)
	}
	return have
}

// seedProvider — нынешний вид пресета по имени.
func seedProvider(name string) (ProviderConf, bool) {
	for _, p := range seedProviders {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderConf{}, false
}

// insertBeforeOwn — новые пресеты встают перед «Своим адресом»: эта
// строка по смыслу последняя, за ней ничего не читается.
func insertBeforeOwn(have, fresh []ProviderConf) []ProviderConf {
	for i, p := range have {
		if p.Name == "Свой адрес" {
			out := append([]ProviderConf{}, have[:i]...)
			out = append(out, fresh...)
			return append(out, have[i:]...)
		}
	}
	return append(have, fresh...)
}

func (e *Engine) SaveProviders(list []ProviderConf) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return e.store.SetSetting(providersKey, string(raw))
}

// Provider ищет провайдера по имени.
func (e *Engine) Provider(name string) (ProviderConf, bool) {
	for _, p := range e.Providers() {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderConf{}, false
}

// ProviderNamesList — для выпадающего списка.
func (e *Engine) ProviderNamesList() []string {
	list := e.Providers()
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.Name)
	}
	return out
}

// AddProvider заводит свою запись справочника.
func (e *Engine) AddProvider(p ProviderConf) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errNoName
	}
	list := e.Providers()
	for i, ex := range list {
		if ex.Name == p.Name {
			list[i] = p
			return e.SaveProviders(list)
		}
	}
	return e.SaveProviders(append(list, p))
}

// seededProviders — что Сёма засеял в прошлый раз.
func (e *Engine) seededProviders() map[string]ProviderConf {
	out := map[string]ProviderConf{}
	raw := e.store.Setting(providersMineKey, "")
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// rememberSeeded запоминает пресеты в том виде, в каком они сейчас в
// справочнике: правку человека потом видно сравнением.
func (e *Engine) rememberSeeded(list []ProviderConf) {
	mine := map[string]ProviderConf{}
	for _, p := range list {
		if _, isSeed := seedProvider(p.Name); isSeed {
			mine[p.Name] = p
		}
	}
	if raw, err := json.Marshal(mine); err == nil {
		_ = e.store.SetSetting(providersMineKey, string(raw))
	}
}

// rememberSeededOnly — запомнить засеянное только про названных: остальные
// пресеты человек мог поправить, и «засеянным» их считать нельзя.
func (e *Engine) rememberSeededOnly(list []ProviderConf, only map[string]bool) {
	mine := e.seededProviders()
	empty := len(mine) == 0
	for _, p := range list {
		if _, isSeed := seedProvider(p.Name); isSeed && (only[p.Name] || empty) {
			mine[p.Name] = p
		}
	}
	if raw, err := json.Marshal(mine); err == nil {
		_ = e.store.SetSetting(providersMineKey, string(raw))
	}
}

// providerSecret — имя секрета с ключом провайдера. Ключ один на
// провайдера, а не на каждую модель: у OpenRouter моделей сотни, и
// вводить один и тот же ключ сотню раз незачем.
func providerSecret(name string) string { return "model:" + name }

// ProviderKey — ключ провайдера из хранилища секретов.
func (e *Engine) ProviderKey(name string) string {
	v, _ := e.store.SecretValue(providerSecret(name))
	return v
}

// SetProviderKey — задать или убрать ключ провайдера. Все модели этого
// провайдера пересобираются сразу: ключ поменяли — работает везде.
func (e *Engine) SetProviderKey(name, key string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("нужен провайдер")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		if err := e.store.DeleteSecret(providerSecret(name)); err != nil {
			return err
		}
	} else if err := e.store.SaveSecret(&store.Secret{
		Name: providerSecret(name), Kind: store.SecretToken,
		Note: "ключ провайдера моделей · " + name,
	}, key); err != nil {
		return err
	}
	e.rebuildProviderModels(name)
	return nil
}

// rebuildProviderModels — пересобрать модели провайдера с нынешним
// ключом. Без этого новый ключ подхватился бы только после перезапуска.
func (e *Engine) rebuildProviderModels(name string) {
	catalog := e.Providers()
	for _, c := range e.userModels() {
		if providerOf(catalog, c.Type, c.BaseURL) != name {
			continue
		}
		if p, err := e.buildModel(c); err == nil {
			e.setProvider(c.Name, p)
		}
	}
}

// moveKeysToProviders — разовый переезд: ключи, лежавшие у каждой модели
// по отдельности, становятся ключом провайдера. У OpenRouter один и тот
// же ключ лежал в полусотне строк — менять его приходилось везде.
func (e *Engine) moveKeysToProviders() {
	const mark = "models.keys_moved"
	if e.store.Setting(mark, "") == "1" {
		return
	}
	list, err := e.userModelsE()
	if err != nil {
		return // список не разобрался — ничего не трогаем
	}
	catalog := e.Providers()
	moved, cleared := 0, 0
	for i := range list {
		key := strings.TrimSpace(list[i].APIKey)
		if key == "" {
			continue
		}
		name := providerOf(catalog, list[i].Type, list[i].BaseURL)
		if e.ProviderKey(name) == "" {
			if err := e.SetProviderKey(name, key); err != nil {
				log.Printf("модели: ключ провайдера %q не сохранён: %v", name, err)
				continue
			}
			moved++
		}
		// ключ модели убираем только если у провайдера теперь тот же самый:
		// чужой ключ в строке — это осознанный выбор человека
		if e.ProviderKey(name) == key {
			list[i].APIKey = ""
			cleared++
		}
	}
	if cleared > 0 {
		if err := e.saveUserModels(list); err != nil {
			log.Printf("модели: ключи не переехали: %v", err)
			return
		}
	}
	_ = e.store.SetSetting(mark, "1")
	if moved > 0 || cleared > 0 {
		log.Printf("модели: ключи переехали к провайдерам (%d ключей, очищено строк %d)", moved, cleared)
	}
}

// ProviderModelNames — имена подключённых моделей этого провайдера.
func (e *Engine) ProviderModelNames(name string) []string {
	catalog := e.Providers()
	var out []string
	for _, c := range e.userModels() {
		if providerOf(catalog, c.Type, c.BaseURL) == name {
			out = append(out, c.Name)
		}
	}
	return out
}

// RemoveProviderModels — отключить разом все модели провайдера. Ключ при
// этом остаётся: человек выключает модели, а не забывает провайдера.
func (e *Engine) RemoveProviderModels(name string) (int, error) {
	names := e.ProviderModelNames(name)
	removed := 0
	for _, n := range names {
		if err := e.RemoveModel(n); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// ProviderSearchOn — есть ли у провайдера хоть одна модель с поиском.
// По нему кнопка называется честно: «выключить» или «включить у всех».
func (e *Engine) ProviderSearchOn(name string) bool {
	catalog := e.Providers()
	for _, c := range e.userModels() {
		if providerOf(catalog, c.Type, c.BaseURL) == name && c.Search {
			return true
		}
	}
	return false
}

// SetProviderSearch — «ходит в сеть» разом у всех моделей провайдера.
// Список сохраняется один раз: полсотни отдельных записей — полсотни
// перезаписей одной строки в базе.
func (e *Engine) SetProviderSearch(name string, on bool) (int, error) {
	list, err := e.userModelsE()
	if err != nil {
		return 0, err
	}
	catalog := e.Providers()
	changed := 0
	for i := range list {
		if providerOf(catalog, list[i].Type, list[i].BaseURL) != name || list[i].Search == on {
			continue
		}
		list[i].Search = on
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, e.saveUserModels(list)
}
