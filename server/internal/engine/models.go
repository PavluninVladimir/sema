package engine

// Свои модели: кроме тех, что описаны в config.yaml, человек добавляет
// модели прямо из интерфейса — имя, тип, адрес сервера, название модели
// и ключ. Они живут в базе Сёмы и подхватываются без перезапуска.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/llm"
)

const modelsKey = "models.json"

type ModelConf struct {
	Name    string `json:"name"`
	Vision  bool   `json:"vision,omitempty"`  // видит картинки
	Search  bool   `json:"search,omitempty"`  // умеет искать в интернете
	Hears   bool   `json:"hears,omitempty"`   // слышит: распознаёт речь (/audio/transcriptions)
	Context int    `json:"context,omitempty"` // размер контекста
	// Price — цена за миллион токенов; у моделей речи — за минуту звука:
	// «$0.003 / мин» (см. parsePerMinute)
	Price string `json:"price,omitempty"`
	// PricePeak и Peak — цена в часы пик и когда эти часы. У DeepSeek
	// дневной тариф вдвое дороже ночного, и без этого счёт врёт вдвое.
	// Расписание — настройка, а не код: провайдер меняет его когда хочет,
	// и зашитые часы однажды начнут врать молча.
	// Peak: «пн-пт 01:00-04:00,06:00-10:00 UTC»
	PricePeak string `json:"price_peak,omitempty"`
	Peak      string `json:"peak,omitempty"`
	// PriceName — как модель называется в прайсе провайдера. У DeepSeek
	// в API это «deepseek-chat», а в таблице цен «deepseek-v4-flash»: без
	// этой связки слежение за ценами не узнаёт наши модели. Ставит человек:
	// угадывать соответствие нельзя, ошибка стоит денег.
	PriceName string `json:"price_name,omitempty"`
	// PriceBy — кто поставил цену: «человек» или «прайс» (принята из
	// карточки проверки цен). Пусто — цена из времён, когда отметки не было,
	// и её считаем вписанной человеком: пачкой «Принять все» такая цена не
	// меняется. PriceAt — когда поставлена.
	PriceBy string `json:"price_by,omitempty"`
	PriceAt string `json:"price_at,omitempty"`
	// Trusted — доверенная модель: человек сам сказал, что ей можно
	// показывать закрытые ящики и секретное. Из адреса не выводится:
	// адрес ничего не гарантирует — 10.0.0.5 через VPN провайдера не
	// свой, а сервер в частной сети арендованного офиса — чужой. Новая
	// модель заводится внешней, как секреты и серверы: невыданное не
	// действует.
	Trusted bool   `json:"trusted,omitempty"`
	Type    string `json:"type"` // openai | anthropic
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key"`
}

// Model — строка списка моделей: и свои, и те, что пришли из config.yaml.
// ModelFact — пара «подпись — значение» для строки модели.
type ModelFact struct{ Label, Value string }

type Model struct {
	Name     string
	Provider string // чьими руками отвечает: имя пресета из справочника
	Meta     string
	Human    string // фраза человеческим языком
	Facts    []ModelFact
	Search   bool // ходит в интернет
	Hears    bool // слышит: распознаёт речь
	Active   bool
	FromFile bool   // из config.yaml — правится файлом, не из интерфейса
	Type     string // openai | anthropic | cli | perplexity — вид подключения
	Problem  string
	NeedsKey bool // облачная модель: ключ меняется из интерфейса
	HasKey   bool
}

// buildModel — собрать провайдера модели. Ключ берётся у самой модели,
// а если его там нет — у провайдера: ключ выдаётся один на провайдера, и
// все его модели работают этим ключом.
func (e *Engine) buildModel(c ModelConf) (llm.Provider, error) {
	return llm.Build(c.Name, config.Provider{
		Type: c.Type, BaseURL: hostAlias(c.BaseURL), Model: c.Model,
		APIKey: e.keyFor(c), MaxOut: e.maxOutTokens(),
	})
}

// keyFor — ключ, которым ходит модель.
func (e *Engine) keyFor(c ModelConf) string {
	if strings.TrimSpace(c.APIKey) != "" {
		return c.APIKey // свой ключ у модели сильнее: так было раньше
	}
	return e.ProviderKey(providerOf(e.Providers(), c.Type, c.BaseURL))
}

// keyChecker — есть ли у модели чем ходить (свой ключ или ключ провайдера),
// для списка моделей: ключ провайдера читается один раз на провайдера, а не
// на модель. Чтение с расшифровкой на каждую из 170
// моделей было запросом к базе на модель на каждом экране настроек (их
// колонка считает «Модели и роли») и на экране ящика (5 октября 2026).
// Провайдер — тот же, что вычислил бы keyFor (providerOf по справочнику).
func (e *Engine) keyChecker() func(c ModelConf, provider string) bool {
	keys := map[string]bool{}
	return func(c ModelConf, provider string) bool {
		if strings.TrimSpace(c.APIKey) != "" {
			return true // свой ключ у модели сильнее, как в keyFor
		}
		has, done := keys[provider]
		if !done {
			has = strings.TrimSpace(e.ProviderKey(provider)) != ""
			keys[provider] = has
		}
		return has
	}
}

func (e *Engine) userModels() []ModelConf {
	out, _ := e.userModelsE()
	return out
}

// userModelsE отличает «моделей нет» от «список не разобрался»: без этого
// любая ошибка чтения молча стирала бы весь список при следующей записи.
func (e *Engine) userModelsE() ([]ModelConf, error) {
	list, _, err := e.modelsShared()
	if err != nil {
		return nil, err
	}
	// у ModelConf одни строки и числа: копия среза — полная копия
	return append([]ModelConf(nil), list...), nil
}

// modelsShared — разобранный список из кэша без копии и индекс по имени.
// Только для чтения: срез заменяется целиком при разборе и на месте не
// правится никем. Строка «Здоровья» и меню берут модель по имени отсюда, а
// не копируют весь список ради одной (perf-pass 3).
func (e *Engine) modelsShared() ([]ModelConf, map[string]int, error) {
	// версия — до чтения строки: запись между ними поднимет её, и
	// следующий вызов разберёт заново, а не поверит устаревшему
	ver := e.store.SettingVersion(modelsKey)
	e.cacheMu.Lock()
	if e.modelsOK && e.modelsVer == ver {
		list, idx := e.modelsList, e.modelsIdx
		e.cacheMu.Unlock()
		return list, idx, nil
	}
	e.cacheMu.Unlock()

	raw := e.store.Setting(modelsKey, "[]")
	var out []ModelConf
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("список моделей не разобран, ничего не меняю: %v", err)
		return nil, nil, err
	}
	idx := make(map[string]int, len(out))
	for i := range out {
		out[i].APIKey = e.store.Open(out[i].APIKey)
		if _, dup := idx[out[i].Name]; !dup {
			idx[out[i].Name] = i
		}
	}
	e.cacheMu.Lock()
	e.modelsVer, e.modelsOK, e.modelsList, e.modelsIdx = ver, true, out, idx
	e.cacheMu.Unlock()
	return out, idx, nil
}

func (e *Engine) saveUserModels(list []ModelConf) error {
	// секреты не читаются (сервер подняли с чужим ключом) — запись
	// отменяется: иначе пустые после неудачной расшифровки ключи легли бы
	// в базу поверх настоящих. Так уже потерялись ключи DeepSeek и
	// Perplexity 5 сентября 2026.
	if p := e.store.SecretProblem(); p != "" {
		return fmt.Errorf("список моделей не сохранён: %s", p)
	}
	closed := make([]ModelConf, len(list))
	for i, c := range list {
		c.APIKey = e.store.Seal(c.APIKey)
		closed[i] = c
	}
	raw, err := json.Marshal(closed)
	if err != nil {
		return err
	}
	err = e.store.SetSetting(modelsKey, string(raw))
	// запись поднимет версию и так; сброс здесь — чтобы и запись «того же
	// самого» (версия не растёт) не оставила разобранное с чужими ключами
	e.cacheMu.Lock()
	e.modelsOK = false
	e.cacheMu.Unlock()
	return err
}

// Models — что показывать в настройках. После переноса файла в базу одна и
// та же модель описана дважды; показываем её один раз, версия из базы важнее.
func (e *Engine) Models() []Model {
	active := e.ActiveProvider()
	// список читается один раз: раньше он собирался дважды, а каждая
	// сборка расшифровывает ключ каждой модели — при 52 моделях это
	// полсотни лишних расшифровок на каждое открытие экрана
	mine := e.userModels()
	// справочник провайдеров тоже берём один раз: providerOf иначе
	// перечитывал бы его для каждой строки
	catalog := e.Providers()
	hasKey := e.keyChecker()
	fromDB := map[string]bool{}
	for _, c := range mine {
		fromDB[c.Name] = true
	}
	removed := e.removedModels()
	var out []Model
	for name, pc := range e.conf.LLM.Providers {
		if fromDB[name] || removed[name] {
			continue
		}
		m := Model{Name: name, FromFile: true, Active: name == active,
			Provider: providerOf(catalog, pc.Type, pc.BaseURL),
			Meta:     modelMeta(pc.Type, pc.BaseURL, pc.Model)}
		if pc.Type == "anthropic" && pc.Key() == "" {
			m.Problem = "нет ключа: " + pc.APIKeyEnv
		}
		out = append(out, m)
	}
	for _, c := range mine {
		if removed[c.Name] {
			continue
		}
		provider := providerOf(catalog, c.Type, c.BaseURL)
		m := Model{Name: c.Name, Active: c.Name == active, Meta: modelMeta(c.Type, c.BaseURL, c.Model),
			Provider: provider,
			Search:   c.Search, Hears: c.CanHear(),
			NeedsKey: c.Type != "cli", HasKey: hasKey(c, provider)}
		m.Human, m.Facts = modelFacts(c, e.ModelIsCloud(c.Name))
		m.Type = c.Type
		if extra := modelAbilities(c); extra != "" {
			m.Meta += " · " + extra
		}
		if m.NeedsKey && !m.HasKey && e.ModelIsCloud(c.Name) {
			m.Problem = "нет ключа"
		}
		if c.Type == "cli" {
			m.Meta = "Подписка · клиент " + c.BaseURL + " на этой машине"
			if a, ok := e.provider(c.Name).(interface{ Available() error }); ok {
				if err := a.Available(); err != nil {
					m.Problem = "клиент " + err.Error()
				}
			}
		}
		out = append(out, m)
	}
	return out
}

func modelMeta(kind, baseURL, model string) string {
	name := "OpenAI-совместимая"
	switch kind {
	case "anthropic":
		name = "Anthropic"
	case "cli":
		name = "Подписка"
	case "perplexity":
		name = "Perplexity · Agent API"
	}
	parts := []string{name}
	if baseURL != "" {
		parts = append(parts, baseURL)
	}
	if model != "" {
		parts = append(parts, model)
	}
	return strings.Join(parts, " · ")
}

// AddModel заводит модель из интерфейса и сразу делает её доступной.
func (e *Engine) AddModel(c ModelConf) error {
	c.Name = strings.TrimSpace(c.Name)
	c.Model = strings.TrimSpace(c.Model)
	if c.Name == "" || c.Model == "" {
		return fmt.Errorf("нужны имя и название модели")
	}
	// у модели свой адрес в настройках (settings:models@<имя>): имя, которое
	// читалось бы как «подключить провайдера», как провайдер или как хвост
	// вкладки, открыло бы не тот экран
	if c.Name == "new" || strings.HasPrefix(c.Name, "provider:") || strings.Contains(c.Name, "~") {
		return fmt.Errorf("имя %q занято под адрес экрана настроек — выберите другое", c.Name)
	}
	if e.hasProvider(c.Name) {
		return fmt.Errorf("модель с именем %q уже есть", c.Name)
	}
	if e.removedModels()[c.Name] { // вернули то, что раньше удалили
		if err := e.forgetRemoved(c.Name); err != nil {
			return err
		}
	}
	if c.Type == "" {
		c.Type = "openai"
	}
	if c.Type == "openai" && c.BaseURL == "" {
		return fmt.Errorf("для OpenAI-совместимой модели нужен адрес сервера")
	}
	if c.Type == "cli" && c.Model == "" {
		c.Model = c.BaseURL // у клиента по подписке модель выбирает он сам
	}
	if c.Type == "perplexity" {
		// Agent API ходит в сеть сам — помечать это руками незачем
		c.Search = true
	}
	// умение слышать провайдеры называют в имени модели (gpt-4o-transcribe,
	// whisper-1): отмечаем сразу, чтобы модель можно было поставить на роль,
	// а не искать её среди сотни чатовых
	c.Hears = c.Hears || hearsByName(c.Model)
	p, err := e.buildModel(c)
	if err != nil {
		return err
	}
	if err := e.saveUserModels(append(e.userModels(), c)); err != nil {
		return err
	}
	e.setProvider(c.Name, p)
	return nil
}

// SetModelKey меняет ключ своей модели: провайдер пересобирается сразу,
// перезапуск не нужен. Пустой ключ допустим для локальных серверов.
func (e *Engine) SetModelKey(name, key string) error {
	list, err := e.userModelsE()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].Name != name {
			continue
		}
		list[i].APIKey = strings.TrimSpace(key)
		c := list[i]
		p, err := e.buildModel(c)
		if err != nil {
			return err
		}
		if err := e.saveUserModels(list); err != nil {
			return err
		}
		e.setProvider(c.Name, p)
		return nil
	}
	return fmt.Errorf("модель %q задана файлом запуска — ключ меняется там", name)
}

const removedModelsKey = "models_removed"

// removedModels — модели, которые человек удалил. Файл запуска — только
// первичное заполнение, поэтому описанная в нём модель не должна
// возвращаться после перезапуска.
func (e *Engine) removedModels() map[string]bool {
	out := map[string]bool{}
	var list []string
	_ = json.Unmarshal([]byte(e.store.Setting(removedModelsKey, "[]")), &list)
	for _, n := range list {
		out[n] = true
	}
	return out
}

// forgetRemoved снимает пометку удаления: человек подключил модель снова.
func (e *Engine) forgetRemoved(name string) error {
	removed := e.removedModels()
	delete(removed, name)
	list := make([]string, 0, len(removed))
	for n := range removed {
		list = append(list, n)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return e.store.SetSetting(removedModelsKey, string(raw))
}

func (e *Engine) rememberRemoved(name string) error {
	removed := e.removedModels()
	removed[name] = true
	list := make([]string, 0, len(removed))
	for n := range removed {
		list = append(list, n)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return e.store.SetSetting(removedModelsKey, string(raw))
}

// RemoveModel убирает модель отовсюду: из списка в базе и из числа тех,
// что поднимаются из файла запуска.
func (e *Engine) RemoveModel(name string) error {
	list, err := e.userModelsE()
	if err != nil {
		return fmt.Errorf("список моделей повреждён — удаление отменено")
	}
	if err := e.rememberRemoved(name); err != nil {
		return err
	}
	var kept []ModelConf
	for _, c := range list {
		if c.Name != name {
			kept = append(kept, c)
		}
	}
	if err := e.saveUserModels(kept); err != nil {
		return err
	}
	e.dropProvider(name)
	if e.ActiveProvider() == name {
		if names := e.ProviderNames(); len(names) > 0 {
			_ = e.SetActiveProvider(names[0])
		}
	}
	return nil
}

// loadUserModels поднимает сохранённые модели при старте.
func (e *Engine) loadUserModels() {
	for _, c := range e.userModels() {
		p, err := e.buildModel(c)
		if err != nil {
			continue
		}
		e.setProvider(c.Name, p)
	}
}

var errNoName = errors.New("у провайдера должно быть имя")

// CheckModel проверяет модель настоящим обращением: человеку нужен ответ
// «работает или нет», а не код ошибки.
func (e *Engine) CheckModel(ctx context.Context, name string) (time.Duration, error) {
	p := e.provider(name)
	if p == nil {
		return 0, fmt.Errorf("модель не подключена")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	started := time.Now()
	// наружу не уходит: проба готовности, никакого содержимого в ней нет
	if _, err := p.Complete(ctx, "Ответь одним словом: готов"); err != nil {
		return time.Since(started), humanModelErr(err)
	}
	// модель ответила — роли, где она основная, выходят с передышки. Иначе
	// после пополнения счёта (17 сентября 2026) «Проверить» показывало
	// «ответила», а роль «Память» ещё полчаса стояла и висело уведомление
	for _, r := range e.Roles() {
		if r.Model == name {
			e.clearRoleFails(r.Name)
		}
	}
	return time.Since(started), nil
}

// humanModelErr переводит ответ сервера модели на человеческий язык.
// деньгиКончились — провайдер отказал, потому что на счёте нет денег.
// 17 сентября 2026 Perplexity отвечал «401 insufficient_quota», а Сёма
// писал «ключ не подошёл» и «модель не отвечает» — человек пошёл бы
// менять ключ, а надо пополнить счёт.
func moneyRanOut(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, p := range []string{"insufficient_quota", "exceeded your current quota", "insufficient balance",
		"insufficient_balance", "credit balance is too low", "payment required", "http 402"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func humanModelErr(err error) error {
	s := err.Error()
	switch {
	case moneyRanOut(err):
		return fmt.Errorf("на счёте у провайдера закончились деньги — пополните баланс")
	case strings.Contains(s, "401"), strings.Contains(s, "403"),
		strings.Contains(s, "invalid_api_key"), strings.Contains(s, "authentication"):
		return fmt.Errorf("ключ не подошёл")
	case strings.Contains(s, "404"), strings.Contains(s, "model_not_found"):
		return fmt.Errorf("такой модели у провайдера нет")
	case strings.Contains(s, "429"):
		return fmt.Errorf("превышен лимит запросов")
	case strings.Contains(s, "deadline exceeded"), strings.Contains(s, "timeout"):
		return fmt.Errorf("не ответила вовремя")
	case llm.IsNetworkErr(err):
		return fmt.Errorf("нет связи с сетью — вопрос остался, повторите, когда связь вернётся")
	}
	return err
}

// Configured — есть ли ящик и модель. Пока чего-то нет, Сёма показывает
// мастер, а фоновые задачи ждут, а не считаются сломанными.
func (e *Engine) Configured() (mail, model bool) {
	return len(e.Accounts()) > 0, len(e.ProviderNames()) > 0
}

// LocalModelServer — поднят ли на этой машине сервер моделей: если да,
// первый запуск сводится к одному нажатию.
func (e *Engine) LocalModelServer(ctx context.Context) string {
	for _, url := range []string{"http://127.0.0.1:1234/v1", "http://127.0.0.1:11434/v1"} {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/models", nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return url
			}
		}
	}
	return ""
}

// modelAbilities — то, что провайдер рассказал о модели: показываем рядом с
// ней, чтобы не ставить читать картинки ту, что их не видит.
func modelAbilities(c ModelConf) string {
	var parts []string
	if c.Vision {
		parts = append(parts, "видит картинки")
	}
	if c.Search {
		parts = append(parts, "ищет в интернете")
	}
	if c.CanHear() {
		parts = append(parts, "слышит речь")
	}
	if c.Context > 0 {
		parts = append(parts, humanContext(c.Context))
	}
	if c.Price != "" {
		// в перечислении рядом с моделью — коротко: вход и выход. Полная
		// цена с кэшем и пиком видна фактами в карточке
		if per, cur, ok := parsePerMinute(c.Price); ok {
			// у моделей речи цена за минуту звука, а не за миллион токенов
			parts = append(parts, fmt.Sprintf("%s%g / мин", cur, per))
		} else {
			in, out, cur := parsePrices(c.Price)
			switch {
			case in > 0 && in != out:
				parts = append(parts, fmt.Sprintf("%s%g → %s%g / 1M", cur, in, cur, out))
			case in > 0:
				parts = append(parts, fmt.Sprintf("%s%g / 1M", cur, in))
			}
		}
	}
	if strings.HasPrefix(c.BaseURL, "http://127.0.0.1") || strings.HasPrefix(c.BaseURL, "http://localhost") {
		parts = append(parts, "локально, письма не покидают эту машину")
	}
	return strings.Join(parts, " · ")
}

// SearchModels — модели, отмеченные как умеющие искать: подсказка при
// назначении роли «Поиск».
func (e *Engine) SearchModels() []string {
	var out []string
	for _, c := range e.userModels() {
		if c.Search {
			out = append(out, c.Name)
		}
	}
	return out
}

// SetModelSearch — пометить модель как умеющую искать в интернете.
func (e *Engine) SetModelSearch(name string, on bool) error {
	list, err := e.userModelsE()
	if err != nil {
		return err
	}
	found := false
	for i := range list {
		if list[i].Name == name {
			list[i].Search, found = on, true
		}
	}
	if !found {
		return fmt.Errorf("модель %q правится не из интерфейса", name)
	}
	return e.saveUserModels(list)
}

// SetModelTrust — доверять модели закрытое или считать внешней.
func (e *Engine) SetModelTrust(name string, on bool) error {
	list, err := e.userModelsE()
	if err != nil {
		return err
	}
	found := false
	for i := range list {
		if list[i].Name == name {
			list[i].Trusted, found = on, true
		}
	}
	if !found {
		return fmt.Errorf("модель %q правится не из интерфейса", name)
	}
	return e.saveUserModels(list)
}

// ModelIsTrusted — объявил ли человек модель доверенной. Клиент по
// подписке (cli) доверенным не бывает: он уносит текст провайдеру.
func (e *Engine) ModelIsTrusted(name string) bool {
	for _, c := range e.userModels() {
		if c.Name == name {
			return c.Trusted && c.Type != "cli"
		}
	}
	return false
}

// ModelOnThisMachine — модель считает на этой же машине: по адресу, а не
// по доверию. Нужно очереди для пауз под нагрузку процессора; к тому,
// что модели можно показывать, отношения не имеет.
func (e *Engine) ModelOnThisMachine(name string) bool {
	for _, c := range e.userModels() {
		if c.Name == name {
			return c.Type != "cli" && llm.HostIsHome(c.BaseURL)
		}
	}
	return false
}

// ModelIsRemote — модель считает на другой машине: по адресу. Для денег
// («локальная — бесплатно») и подписи «наружу» в журнале отправленного;
// к тому, можно ли ей закрытое, отношения не имеет — это ModelIsCloud.
func (e *Engine) ModelIsRemote(name string) bool {
	if name == "" {
		return false
	}
	if c, ok := e.modelConf(name); ok {
		return c.Type == "cli" || !llm.HostIsHome(c.BaseURL)
	}
	return true
}

// trustSeedKey — метка разового переноса: до появления признака доверия
// «своими» считались модели по адресу (своя сеть, тот же докер). Один раз
// переносим это в признак, чтобы работающая установка не встала — дальше
// доверие ставит только человек.
const trustSeedKey = "models.trust_seeded"

func (e *Engine) seedModelTrust() {
	if e.store.Setting(trustSeedKey, "") != "" {
		return
	}
	list, err := e.userModelsE()
	if err != nil {
		return
	}
	changed := false
	for i := range list {
		if !list[i].Trusted && list[i].Type != "cli" && llm.HostIsHome(list[i].BaseURL) {
			list[i].Trusted, changed = true, true
		}
	}
	if changed {
		if err := e.saveUserModels(list); err != nil {
			return
		}
	}
	_ = e.store.SetSetting(trustSeedKey, time.Now().Format(time.RFC3339))
}

// providerOf — чей это адрес. Имя провайдера в модели не хранится: адрес
// может смениться, а справочник — единственное место, где он назван.
func providerOf(catalog []ProviderConf, kind, baseURL string) string {
	url := strings.TrimRight(baseURL, "/")
	for _, p := range catalog {
		if p.Kind == kind && strings.TrimRight(p.BaseURL, "/") == url {
			return p.Name
		}
	}
	if kind == "cli" {
		return "По подписке"
	}
	if url == "" {
		return "Свои"
	}
	return url
}

// modelsMaxOutputKey — предел ответа модели в токенах (`models.max_output`).
const modelsMaxOutputKey = "models.max_output"

// maxOutTokens — предел ответа модели в токенах. Настройка, а не число в
// коде: у одних провайдеров он обязателен, у других мешает. Ноль законен —
// «без предела»; мусор читается как умолчание.
func (e *Engine) maxOutTokens() int {
	return e.store.SettingInt(modelsMaxOutputDef)
}

// modelFacts — фраза о модели и факты парами. Раньше это склеивалось
// через «·» и потом разбиралось обратно: половина значений оставалась без
// подписи («Agent API», «deepseek-reasoner»).
func modelFacts(c ModelConf, cloud bool) (string, []ModelFact) {
	human := "доверенная — видит закрытые ящики и секретное"
	if cloud {
		human = "внешняя — получает только обычное, закрытые ящики и секретное ей не показываются"
	}
	switch c.Type {
	case "cli":
		human = "клиент по подписке на этой машине"
	case "perplexity":
		human = "Agent API: сама ходит в сеть и приносит ссылки"
	}
	facts := []ModelFact{}
	if c.Model != "" {
		facts = append(facts, ModelFact{"Модель", c.Model})
	}
	if c.BaseURL != "" {
		facts = append(facts, ModelFact{"Адрес",
			strings.TrimPrefix(strings.TrimPrefix(c.BaseURL, "https://"), "http://")})
	}
	if c.Vision {
		facts = append(facts, ModelFact{"Картинки", "видит"})
	}
	if c.Search {
		facts = append(facts, ModelFact{"Поиск", "ходит в сеть"})
	}
	if c.CanHear() {
		facts = append(facts, ModelFact{"Слышит", "распознаёт речь"})
	}
	if c.Type != "cli" {
		if c.Trusted {
			facts = append(facts, ModelFact{"Доверие", "доверенная"})
		} else {
			facts = append(facts, ModelFact{"Доверие", "внешняя"})
		}
	}
	if c.Context > 0 {
		facts = append(facts, ModelFact{"Контекст", humanContext(c.Context)})
	}
	if v, cur, ok := parsePerMinute(c.Price); ok {
		facts = append(facts, ModelFact{"Цена", fmt.Sprintf("%s%g / мин звука", cur, v)})
	} else if c.Price != "" {
		in, out, cache, cur := parsePrices3(c.Price)
		facts = append(facts, ModelFact{"Вход", fmt.Sprintf("%s%g / 1M", cur, in)})
		if out != in {
			facts = append(facts, ModelFact{"Выход", fmt.Sprintf("%s%g / 1M", cur, out)})
		}
		if cache > 0 {
			facts = append(facts, ModelFact{"Вход из кэша", fmt.Sprintf("%s%g / 1M", cur, cache)})
		}
		if c.PricePeak != "" {
			facts = append(facts, ModelFact{"В часы пик", c.PricePeak})
			facts = append(facts, ModelFact{"Часы пик", c.Peak})
		}
	}
	// кто назвал цену: пачкой «Принять все» меняются только те, что пришли
	// от прайса, — по этой строке видно, какая перед глазами
	if c.Price != "" {
		switch c.PriceBy {
		case PriceByPrice:
			facts = append(facts, ModelFact{"Цена взята", "с прайса провайдера" + priceDate(c.PriceAt)})
		case priceByHuman:
			facts = append(facts, ModelFact{"Цена взята", "вписана вами" + priceDate(c.PriceAt)})
		}
	}
	return human, facts
}

// PriceOf — цена модели строкой из настроек. Пусто — цена не задана, и
// расход по этой модели посчитать не из чего: показывать ноль было бы
// хуже, чем сказать правду.
func (e *Engine) PriceOf(model string) string {
	if c, ok := e.modelConf(model); ok {
		return strings.TrimSpace(c.Price)
	}
	return ""
}

// SetModelPrice — цена модели строкой: «$0.55 / 1M», «$0.55 / $2.20 / 1M»
// или «$0.22 / $0.66 / $0.007 / 1M» (вход, выход, вход из кэша). Пустая
// строка убирает цену — тогда расход по модели не считается вовсе, и на
// «Здоровье» так и написано.
//
// Задним числом ничего не пересчитывается: в журнале записано то, что мы
// думали в момент вызова, и переписывать историю нельзя.
// Строка может нести и пиковый тариф через «|»:
// «$0.22 / $0.66 / $0.007 / 1M | $0.44 / $1.32 / $0.014 / 1M | пн-пт 01:00-04:00,06:00-10:00 UTC»
func (e *Engine) SetModelPrice(name, price string) error {
	price = strings.TrimSpace(price)
	var peak, schedule string
	if parts := strings.Split(price, "|"); len(parts) >= 2 {
		price = strings.TrimSpace(parts[0])
		peak = strings.TrimSpace(parts[1])
		if len(parts) >= 3 {
			schedule = strings.TrimSpace(parts[2])
		}
	}
	if price != "" {
		if in, out, _ := parsePrices(price); in <= 0 && out <= 0 {
			return fmt.Errorf("цену не разобрал: ждал что-то вроде «$0.55 / 1M»")
		}
	}
	if peak != "" {
		if in, out, _ := parsePrices(peak); in <= 0 && out <= 0 {
			return fmt.Errorf("пиковую цену не разобрал")
		}
		if schedule == "" {
			return fmt.Errorf("пиковая цена без расписания бессмысленна: добавьте «| пн-пт 01:00-04:00 UTC»")
		}
		if len(parsePeak(schedule)) == 0 {
			return fmt.Errorf("расписание не разобрал: ждал что-то вроде «пн-пт 01:00-04:00,06:00-10:00 UTC»")
		}
	}
	list, err := e.userModelsE()
	if err != nil {
		return err
	}
	found := false
	for i := range list {
		if list[i].Name == name {
			list[i].Price, list[i].PricePeak, list[i].Peak = price, peak, schedule
			// вписано человеком (и старой карточкой «Принять цену»): пачкой
			// проверки цен такая цена не меняется
			list[i].PriceBy, list[i].PriceAt = priceByHuman, time.Now().Format(time.RFC3339)
			if price == "" {
				list[i].PriceBy, list[i].PriceAt = "", ""
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("модели %q нет", name)
	}
	return e.saveUserModels(list)
}

// SetPriceName — как модель называется в прайсе провайдера. Ставит
// человек: угадывать соответствие нельзя, ошибка стоит денег напрямую.
func (e *Engine) SetPriceName(name, priceName string) error {
	list, err := e.userModelsE()
	if err != nil {
		return err
	}
	found := false
	for i := range list {
		if list[i].Name == name {
			list[i].PriceName, found = strings.TrimSpace(priceName), true
		}
	}
	if !found {
		return fmt.Errorf("модели %q нет", name)
	}
	return e.saveUserModels(list)
}

// PriceShort — цена модели коротко, для капсулы: «$0.22 → $0.66 / 1M»
// (вход → выход). Раньше в капсулу попадало всё после последнего знака
// доллара, и с появлением третьей цены там оказывалась цена кэша — модель
// выглядела в тридцать раз дешевле, чем есть.
func (e *Engine) PriceShort(model string) string {
	c, ok := e.modelConf(model)
	if !ok {
		return ""
	}
	return priceShortOf(c.Price, c.PricePeak)
}

// priceShortOf — та же короткая цена по строкам цены и пика: её берёт и
// PriceShort, и карточка модели.
func priceShortOf(price, peak string) string {
	price = strings.TrimSpace(price)
	if price == "" {
		return ""
	}
	if v, cur, ok := parsePerMinute(price); ok {
		return fmt.Sprintf("%s%g / мин", cur, v)
	}
	in, out, cur := parsePrices(price)
	if in <= 0 && out <= 0 {
		return ""
	}
	if cur == "" {
		cur = "$"
	}
	if in == out {
		return fmt.Sprintf("%s%g / 1M", cur, in)
	}
	s := fmt.Sprintf("%s%g → %s%g / 1M", cur, in, cur, out)
	// про пик говорим одним словом: подробности — в карточке модели
	if peak != "" {
		s += " · в пик дороже"
	}
	return s
}

// priceDate — « · 30.09» к строке о цене; нет даты — ничего.
func priceDate(at string) string {
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return " · " + t.Local().Format("02.01")
	}
	return ""
}
