package engine

// Модели для экранов настроек, одной сборкой.
//
// Список моделей в настройках спрашивал движок про каждую строку отдельно:
// цену, доверие, умение слышать, «нет у провайдера», роли, где занята. Каждый
// такой вопрос читал из базы весь список моделей заново (одна запись в
// настройках, у ста семидесяти моделей — десятки килобайт), и на экран из
// десяти строк приходилось несколько сотен чтений. Здесь список, справочник
// провайдеров, роли и то, что провайдер отдавал в прошлый раз, читаются по
// одному разу, а карточка модели — готовый ответ на все эти вопросы.
//
// Решений карточка не принимает: доверие, класс и дверь к модели считает тот
// же код, что и раньше. Она только собирает то, что экрану нужно показать.

import (
	"sort"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
)

// GroupThisMachine — как подписывается группа моделей, что считают на этой же
// машине: у каждой свой адрес (llm:8080, embed:8080), а человеку важно, что
// письма с машины не уходят.
const GroupThisMachine = "на этой машине"

// ModelCard — подключённая модель такой, какой её показывают списки и её
// собственный экран.
type ModelCard struct {
	Name string
	// Provider — как в справочнике провайдеров; у модели с чужого шлюза вместо
	// имени приходит адрес. Group — чем подписывать группу и по чему искать её
	// экран: имя провайдера, «на этой машине» или имя сервера из адреса
	Provider string
	Group    string
	Type     string // openai | anthropic | perplexity | cli
	Model    string // как модель называется у провайдера
	BaseURL  string
	FromFile bool // описана файлом запуска: из интерфейса не правится

	NeedsKey bool // облачная: у провайдера нужен ключ
	HasKey   bool
	Remote   bool // считает на другой машине: деньги и «наружу» (по адресу)
	Trusted  bool // доверие объявил человек, адрес его не даёт

	Vision      bool
	Search      bool
	Hears       bool // слышит: отметкой человека или по названию
	HearsByName bool // умение видно по названию, снимать его нельзя
	Context     int

	Gone    bool   // провайдер больше не предлагает эту модель
	Problem string // «нет ключа», «клиент не найден»…

	Price     string
	PricePeak string
	Peak      string
	PriceName string
	PriceBy   string
	PriceAt   string

	// Roles — роли, где модель основная; SpareOf — где запасная
	Roles   []string
	SpareOf []string
}

// NoPrice — расход этой модели считать не из чего: деньги за неё платят, а
// цена не названа. Локальные модели и клиенты по подписке цены не требуют:
// их вызов не стоит ничего, и «без цены» про них было бы враньём.
func (c ModelCard) NoPrice() bool {
	return !c.FromFile && c.Type != "cli" && c.Remote && strings.TrimSpace(c.Price) == ""
}

// InRoles — модель стоит хоть в одной роли, основной или запасной.
func (c ModelCard) InRoles() bool { return len(c.Roles) > 0 || len(c.SpareOf) > 0 }

// PriceShort — цена коротко, для строки списка: «$0.22 → $0.66 / 1M», у модели
// речи «$0.003 / мин»; пусто — цена не названа.
func (c ModelCard) PriceShort() string { return priceShortOf(c.Price, c.PricePeak) }

// CanAskBalance — у провайдера с таким адресом остаток отдаётся
// документированным адресом: DeepSeek и OpenRouter. У остальных его можно
// только оценить.
func CanAskBalance(baseURL string) bool { return canBalance(baseURL) }

// ModelCards — все подключённые модели одной сборкой, в порядке подключения;
// описанные файлом — после, по имени.
func (e *Engine) ModelCards() []ModelCard {
	mine := e.userModels()
	catalog := e.Providers()
	roles := e.Roles()
	removed := e.removedModels()

	mainOf, spareOf := map[string][]string{}, map[string][]string{}
	for _, r := range roles {
		if r.Model != "" && r.Model != roleNoneName {
			mainOf[r.Model] = append(mainOf[r.Model], r.Name)
		}
		if r.Spare != "" && r.Spare != roleNoneName {
			spareOf[r.Spare] = append(spareOf[r.Spare], r.Name)
		}
	}
	// что провайдер отдавал в прошлый раз: один разбор на провайдера, а не на
	// каждую его модель
	seen := map[string]map[string]bool{}
	live := func(provider string) map[string]bool {
		if ids, done := seen[provider]; done {
			return ids
		}
		var ids map[string]bool
		if v, has := e.ProviderCatalog(provider); has {
			ids = map[string]bool{}
			for _, id := range v.Names {
				ids[id] = true
			}
		}
		seen[provider] = ids
		return ids
	}

	hasKey := e.keyChecker()

	out := make([]ModelCard, 0, len(mine)+len(e.conf.LLM.Providers))
	fromDB := map[string]bool{}
	for _, c := range mine {
		fromDB[c.Name] = true
	}
	var fileNames []string
	for name := range e.conf.LLM.Providers {
		if !fromDB[name] && !removed[name] {
			fileNames = append(fileNames, name)
		}
	}
	sort.Strings(fileNames)
	for _, c := range mine {
		if removed[c.Name] {
			continue
		}
		provider := providerOf(catalog, c.Type, c.BaseURL)
		card := ModelCard{
			Name: c.Name, Type: c.Type, Model: c.Model, BaseURL: c.BaseURL,
			Provider: provider,
			NeedsKey: c.Type != "cli", HasKey: hasKey(c, provider),
			Remote:  c.Type == "cli" || !llm.HostIsHome(c.BaseURL),
			Trusted: c.Trusted && c.Type != "cli",
			Vision:  c.Vision, Search: c.Search, Hears: c.CanHear(), HearsByName: c.CanHear() && hearsByName(c.Model),
			Context: c.Context,
			Price:   strings.TrimSpace(c.Price), PricePeak: c.PricePeak, Peak: c.Peak,
			PriceName: c.PriceName, PriceBy: c.PriceBy, PriceAt: c.PriceAt,
			Roles: mainOf[c.Name], SpareOf: spareOf[c.Name],
		}
		card.Group = providerGroup(card.Provider, c)
		if ids := live(card.Provider); ids != nil && !ids[c.Model] {
			card.Gone = true
		}
		// ключа нет — беда у недоверенной модели, которой он нужен: так было и
		// в прежнем списке (ModelIsCloud — это «не доверенная»)
		if card.NeedsKey && !card.HasKey && !card.Trusted {
			card.Problem = "нет ключа"
		}
		if c.Type == "cli" {
			card.Problem = ""
			if a, ok := e.provider(c.Name).(interface{ Available() error }); ok {
				if err := a.Available(); err != nil {
					card.Problem = "клиент не найден"
				}
			}
		}
		out = append(out, card)
	}
	for _, name := range fileNames {
		pc := e.conf.LLM.Providers[name]
		file := ModelConf{Name: name, Type: pc.Type, Model: pc.Model, BaseURL: pc.BaseURL}
		card := ModelCard{Name: name, FromFile: true, Type: pc.Type, Model: pc.Model, BaseURL: pc.BaseURL,
			Provider: providerOf(catalog, pc.Type, pc.BaseURL),
			Remote:   !llm.HostIsHome(pc.BaseURL),
			Hears:    file.CanHear(), HearsByName: file.CanHear() && hearsByName(pc.Model),
			Roles: mainOf[name], SpareOf: spareOf[name]}
		card.Group = providerGroup(card.Provider, file)
		if pc.Type == "anthropic" && pc.Key() == "" {
			card.Problem = "нет ключа: " + pc.APIKeyEnv
		}
		out = append(out, card)
	}
	return out
}

// ModelCardOf — карточка одной модели; false — такой модели нет.
func (e *Engine) ModelCardOf(name string) (ModelCard, bool) {
	for _, c := range e.ModelCards() {
		if c.Name == name {
			return c, true
		}
	}
	return ModelCard{}, false
}

// providerGroup — чем подписать группу моделей. Имя из справочника остаётся
// как есть; адрес вместо имени читается человеком: «на этой машине», если
// сервер здесь же, иначе имя сервера без схемы и без «/v1».
func providerGroup(provider string, c ModelConf) string {
	if !strings.Contains(provider, "://") {
		return provider
	}
	if c.Type != "cli" && llm.HostIsHome(c.BaseURL) {
		return GroupThisMachine
	}
	addr := provider
	if _, rest, has := strings.Cut(addr, "://"); has {
		addr = rest
	}
	addr = strings.TrimSuffix(strings.TrimSuffix(addr, "/"), "/v1")
	return strings.TrimSuffix(addr, "/")
}

// ProviderModels — карточки моделей одной группы (имя провайдера из
// справочника, «на этой машине» или имя сервера).
func (e *Engine) ProviderModels(group string) []ModelCard {
	var out []ModelCard
	for _, c := range e.ModelCards() {
		if c.Group == group {
			out = append(out, c)
		}
	}
	return out
}

// ProviderScreen — адрес экрана провайдера в настройках на нужной вкладке: пусто
// — «Ключ и модели», «prices» — «Цены», «balance» — «Остаток». Адрес нужен
// уведомлениям (карточки цен и остатка) и живому обновлению экрана: экран один
// раз описан в api, а здесь — только его название. Заводить вкладку, которой у
// провайдера нет, нельзя: адрес всё равно откроет первую.
func ProviderScreen(provider, tab string) string {
	addr := modelsScreenName + "@provider:" + provider
	if tab != "" {
		addr += "~" + tab
	}
	return addr
}

// providerScreens — все адреса экрана провайдера: меняется любая вкладка, и
// открытый экран должен перечитаться, на какой бы он ни стоял.
func providerScreens(provider string) []string {
	return []string{ProviderScreen(provider, ""), ProviderScreen(provider, "prices"), ProviderScreen(provider, "balance"),
		modelsScreenName + "~providers", modelsScreenName + "~issues"}
}

// ProviderKeySet — задан ли ключ провайдера и когда: значение не читается и не
// расшифровывается вовсе, экрану нужен только факт.
func (e *Engine) ProviderKeySet(name string) (created string, set bool) {
	sec, ok := e.store.Secret(providerSecret(name))
	if !ok {
		return "", false
	}
	return sec.Created, true
}

// RoleRest — роль отдыхает после трёх отказов подряд, и до какого времени.
// Отметка снимается сама, как только срок вышел.
func (e *Engine) RoleRest(role string) (time.Time, bool) { return e.roleCooldown(role) }
