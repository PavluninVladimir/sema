package engine

// Остаток на счёте у провайдера. Наш журнал расхода отвечает на вопрос
// «что именно потратило деньги» — подробнее, чем любой личный кабинет. Но
// на вопрос «сколько осталось» он не отвечает вовсе: пополнения ему
// неоткуда узнать. Источник остатка — у провайдера (`ProviderConf.BalanceKind`):
//
//   - api — провайдер называет остаток документированным адресом. Таких двое:
//     DeepSeek (`GET /user/balance`) и OpenRouter (`GET /api/v1/credits`,
//     нужен ключ управления — обычный даёт 403). Гадать по недокументированным
//     адресам нельзя: они меняются молча, и узнаёшь об этом по неверным цифрам.
//     У OpenAI, Anthropic, Perplexity и Google адреса остатка в документации
//     нет (у первых двух есть только отчёты о тратах под ключом администратора);
//   - estimate — человек вписывает «на счёте было $X на такую-то дату» из
//     личного кабинета, а Сёма вычитает расход, который сам записал в журнал.
//     Это оценка, и слово «оценка» стоит при ней всегда: траты вне Сёмы (другие
//     программы, тот же ключ в другом месте) ему не видны;
//   - none — остаток не показываем.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Amount — остаток в одной валюте. У DeepSeek счёт бывает и в юанях, и в
// долларах, и одновременно в обеих: показывать надо все, иначе человек
// увидит не свои деньги.
type Amount struct {
	Currency string
	Total    float64
	Granted  float64
	ToppedUp float64
}

// Balance — что показываем человеку.
type Balance struct {
	Provider string
	Kind     string // api | estimate
	Amounts  []Amount
	Enough   bool
	At       time.Time
	Err      string
	// Для оценки: чем она считалась и чего не видит.
	Note string
	// Low — порог «предупредить, когда меньше», в валюте счёта.
	Low float64
}

var (
	balancesMu sync.Mutex
	balances   = map[string]Balance{}
)

// balanceHTTP — чем спрашиваем провайдера; в тестах подменяется, чтобы не
// ходить в настоящую сеть.
var balanceHTTP = http.DefaultClient

// balanceRun — идущие сейчас проверки остатка; устроены как проверки цен.
type balanceRun = priceRun

// canBalance — у кого остаток отдаётся документированным адресом. Список
// нарочно короткий: у большинства такого адреса нет, а гадать по
// недокументированным нельзя — они меняются молча.
func canBalance(baseURL string) bool {
	return strings.Contains(baseURL, "api.deepseek.com") || strings.Contains(baseURL, "openrouter.ai")
}

// WorkerBalance — задача сердцебиения, которая и ходит к провайдерам.
const WorkerBalance = "balance"

// balanceAccount — у кого показываем остаток: провайдер с включённым
// источником и хотя бы одной подключённой моделью, и модель, чьим ключом
// ходим.
type balanceAccount struct {
	Provider ProviderConf
	Model    ModelConf
}

func (e *Engine) balanceAccounts() []balanceAccount {
	var out []balanceAccount
	for _, p := range e.Providers() {
		if p.BalanceKind != BalanceAPI && p.BalanceKind != BalanceEstimate {
			continue
		}
		models := e.providerModelConfs(p.Name)
		if len(models) == 0 {
			continue
		}
		if p.BalanceKind == BalanceAPI && !canBalance(p.BaseURL) {
			continue // адреса остатка у этого провайдера нет: спрашивать нечего
		}
		out = append(out, balanceAccount{Provider: p, Model: models[0]})
	}
	return out
}

// BalancesFromCache — то, что уже известно, без единого похода в сеть. Экран
// собирается из готового: раньше остаток спрашивали прямо в сборке
// «Здоровья», и раз в десять минут кто-то ждал четыре секунды, глядя на
// пустую страницу. Провайдер, которого ещё не спрашивали, отдаётся с пустым
// временем и говорит об этом прямо — ноль здесь был бы враньём о деньгах.
// Оценка считается на месте: она идёт из журнала, сети не трогает.
func (e *Engine) BalancesFromCache() []Balance {
	var out []Balance
	for _, a := range e.balanceAccounts() {
		if a.Provider.BalanceKind == BalanceEstimate {
			out = append(out, e.estimateBalance(a.Provider))
			continue
		}
		balancesMu.Lock()
		was, has := balances[a.Provider.Name]
		balancesMu.Unlock()
		if has {
			was.Low = a.Provider.BalanceLow
			out = append(out, was)
			continue
		}
		out = append(out, Balance{Provider: a.Provider.Name, Kind: BalanceAPI, Low: a.Provider.BalanceLow})
	}
	return out
}

// RefreshBalances — задача «Остаток на счетах»: спрашивает провайдеров и
// кладёт ответ в кэш. Свежее десяти минут не перезапрашиваем: цифра меняется
// медленно, а задача может быть заведена и почаще. Порог и «не хватает»
// поднимают уведомление — один раз на переход, а не каждые полчаса.
func (e *Engine) RefreshBalances(ctx context.Context) string {
	accounts := e.balanceAccounts()
	if len(accounts) == 0 {
		return "провайдеров, называющих остаток, нет"
	}
	var parts []string
	for _, a := range accounts {
		b := e.refreshBalance(ctx, a, false)
		parts = append(parts, a.Provider.Name+": "+b.Text())
	}
	return strings.Join(parts, " · ")
}

// refreshBalance — один провайдер. force — кнопка «Спросить сейчас»: свежесть
// кэша не смотрим.
func (e *Engine) refreshBalance(ctx context.Context, a balanceAccount, force bool) Balance {
	p := a.Provider
	if p.BalanceKind == BalanceEstimate {
		b := e.estimateBalance(p)
		e.balanceNotify(p, b)
		return b
	}
	balancesMu.Lock()
	was, has := balances[p.Name]
	balancesMu.Unlock()
	if has && !force && time.Since(was.At) < 10*time.Minute {
		was.Low = p.BalanceLow
		return was
	}
	var b Balance
	switch {
	case strings.Contains(p.BaseURL, "openrouter.ai"):
		b = askCredits(ctx, p.Name, p.BaseURL, e.ManagementKey(p.Name))
	default:
		b = askBalance(ctx, p.Name, p.BaseURL, e.keyFor(a.Model))
	}
	b.Kind, b.Low = BalanceAPI, p.BalanceLow
	balancesMu.Lock()
	balances[p.Name] = b
	balancesMu.Unlock()
	e.balanceNotify(p, b)
	return b
}

// StartBalanceRefresh — спросить остаток провайдера сейчас, в своей нитке.
func (e *Engine) StartBalanceRefresh(provider string) bool {
	if !e.bal.start(provider) {
		return false
	}
	go func() {
		defer e.bal.done(provider)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, a := range e.balanceAccounts() {
			if a.Provider.Name == provider {
				e.refreshBalance(ctx, a, true)
			}
		}
		e.Changed(append(providerScreens(provider), "health")...)
	}()
	return true
}

// askBalance — GET /user/balance у DeepSeek. Суммы приходят строками,
// и это не каприз: деньги считают строками, чтобы не терять копейки.
func askBalance(ctx context.Context, name, baseURL, key string) Balance {
	o := Balance{Provider: name, Kind: BalanceAPI, At: time.Now()}
	if strings.TrimSpace(key) == "" {
		o.Err = "ключ не задан"
		return o
	}
	addr := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1") + "/user/balance"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		o.Err = err.Error()
		return o
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := balanceHTTP.Do(req)
	if err != nil {
		o.Err = shortErr(err.Error())
		return o
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		o.Err = fmt.Sprintf("ответ %d", resp.StatusCode)
		return o
	}
	var reply struct {
		Available bool `json:"is_available"`
		Infos     []struct {
			Currency string `json:"currency"`
			Total    string `json:"total_balance"`
			Granted  string `json:"granted_balance"`
			ToppedUp string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		o.Err = "ответ не разобран"
		return o
	}
	o.Enough = reply.Available
	if len(reply.Infos) == 0 {
		o.Err = "остаток не назван"
		return o
	}
	// валют бывает несколько: у DeepSeek счёт держат и в юанях, и в
	// долларах. Показываем все — выбросить чужую валюту значит показать
	// человеку не его деньги
	for _, i := range reply.Infos {
		amount := Amount{Currency: i.Currency}
		amount.Total, _ = strconv.ParseFloat(i.Total, 64)
		amount.Granted, _ = strconv.ParseFloat(i.Granted, 64)
		amount.ToppedUp, _ = strconv.ParseFloat(i.ToppedUp, 64)
		o.Amounts = append(o.Amounts, amount)
	}
	return o
}

// askCredits — GET /api/v1/credits у OpenRouter: `total_credits` — сколько
// куплено, `total_usage` — сколько потрачено, остаток — их разность.
// Адрес пускает только ключ управления: обычный ключ получает 403, и это
// называется прямо, а не «ответ 403».
func askCredits(ctx context.Context, name, baseURL, key string) Balance {
	o := Balance{Provider: name, Kind: BalanceAPI, At: time.Now()}
	if strings.TrimSpace(key) == "" {
		o.Err = "нужен ключ управления (Management key): openrouter.ai/settings/management-keys — обычный ключ этот адрес не пускает"
		return o
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/credits", nil)
	if err != nil {
		o.Err = err.Error()
		return o
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := balanceHTTP.Do(req)
	if err != nil {
		o.Err = shortErr(err.Error())
		return o
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		o.Err = "адрес отвечает только ключу управления (Management key); обычный ключ его не пускает"
		return o
	case http.StatusUnauthorized:
		o.Err = "ключ не подошёл"
		return o
	default:
		o.Err = fmt.Sprintf("ответ %d", resp.StatusCode)
		return o
	}
	var reply struct {
		Data struct {
			Credits float64 `json:"total_credits"`
			Usage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		o.Err = "ответ не разобран"
		return o
	}
	left := reply.Data.Credits - reply.Data.Usage
	o.Amounts = []Amount{{Currency: "USD", Total: left, ToppedUp: reply.Data.Credits}}
	o.Enough = left > 0
	return o
}

// ---- оценка ----

// BalanceBase — «на счёте было столько-то на такую-то дату»: то, что человек
// прочёл в личном кабинете. От неё оценка и считается.
type BalanceBase struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"` // знак: «$», «€», «₽», «¥»
	Date     string  `json:"date"`     // ГГГГ-ММ-ДД, с начала этого дня считается расход
}

func balanceBaseKey(provider string) string  { return "balance.base." + provider }
func balanceStateKey(provider string) string { return "balance.state." + provider }

// BalanceBaseOf — что человек вписал; false — ещё не вписывал.
func (e *Engine) BalanceBaseOf(provider string) (BalanceBase, bool) {
	raw := e.store.Setting(balanceBaseKey(provider), "")
	if raw == "" {
		return BalanceBase{}, false
	}
	var b BalanceBase
	if json.Unmarshal([]byte(raw), &b) != nil || b.Date == "" {
		return BalanceBase{}, false
	}
	return b, true
}

// SetBalanceBase — «было $X на дату». amount — как в кабинете: «10», «$10.50»,
// «₽1200»; пустая дата — сегодня. Новое «было» — это пополнение или
// сверка: прежнее предупреждение об остатке закрывается, а счёт начинается
// заново.
func (e *Engine) SetBalanceBase(provider, amount, date string) error {
	v, cur := parsePrice(amount)
	if v <= 0 && strings.TrimSpace(amount) != "0" {
		return fmt.Errorf("сумму не разобрал: ждал что-то вроде «10» или «$10.50»")
	}
	date = strings.TrimSpace(date)
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	when, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return fmt.Errorf("дату не разобрал: ждал ГГГГ-ММ-ДД, например 2026-09-30")
	}
	if when.After(time.Now().Add(24 * time.Hour)) {
		return fmt.Errorf("дата в будущем: остаток бывает только на сегодня или раньше")
	}
	if _, ok := e.Provider(provider); !ok {
		return fmt.Errorf("провайдера «%s» нет в справочнике", provider)
	}
	raw, _ := json.Marshal(BalanceBase{Amount: v, Currency: cur, Date: date})
	if err := e.store.SetSetting(balanceBaseKey(provider), string(raw)); err != nil {
		return err
	}
	_ = e.store.SetSetting(balanceStateKey(provider), "")
	e.CloseNotification("balance:"+provider, store.NotifyDone, "остаток введён заново")
	return nil
}

// normCurrency — валюта строкой журнала («$», «USD») к знаку.
func normCurrency(c string) string {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "", "$", "USD":
		return "$"
	case "€", "EUR":
		return "€"
	case "₽", "RUB":
		return "₽"
	case "¥", "CNY", "RMB":
		return "¥"
	}
	return c
}

// estimateBalance — оценка: «было» из кабинета минус расход, который Сёма
// записал после этой даты, у моделей этого провайдера. Часть расхода может
// быть не посчитана (у модели нет цены) — тогда оценка так и говорит.
func (e *Engine) estimateBalance(p ProviderConf) Balance {
	b := Balance{Provider: p.Name, Kind: BalanceEstimate, Low: p.BalanceLow}
	base, ok := e.BalanceBaseOf(p.Name)
	if !ok {
		b.Note = "оценка не начата: впишите, сколько сейчас на счёте, — Сёма будет вычитать свой расход"
		return b
	}
	since, err := time.ParseInLocation("2006-01-02", base.Date, time.Local)
	if err != nil {
		b.Err = "дата в оценке не разобрана"
		return b
	}
	sums, err := e.store.UsageBy("model", since, "")
	if err != nil {
		b.Err = "журнал расхода не прочитан"
		return b
	}
	byModel := map[string]*store.UsageSum{}
	for _, s := range sums {
		byModel[s.Key] = s
	}
	spent, unpriced, other := 0.0, 0, 0
	for _, c := range e.providerModelConfs(p.Name) {
		s := byModel[c.Name]
		if s == nil || s.Calls == 0 {
			continue
		}
		if s.Cost > 0 {
			if normCurrency(s.Currency) == normCurrency(base.Currency) {
				spent += s.Cost
			} else {
				other += s.Calls // расход в другой валюте: вычесть нечем
			}
			continue
		}
		if e.ModelIsRemote(c.Name) && e.PriceOf(c.Name) == "" {
			unpriced += s.Calls
		}
	}
	left := base.Amount - spent
	b.At = time.Now()
	b.Enough = left > 0
	b.Amounts = []Amount{{Currency: base.Currency, Total: left}}
	sign := CurrencySign(base.Currency)
	if base.Currency == "" {
		sign = "$"
	}
	note := fmt.Sprintf("оценка: было %s%.2f на %s, минус расход Сёмы %s%.2f; траты вне Сёмы не видны",
		sign, base.Amount, shortBaseDate(base.Date), sign, spent)
	if unpriced > 0 {
		note += fmt.Sprintf("; часть расхода не посчитана: %s без цены", plural(unpriced, "вызов", "вызова", "вызовов"))
	}
	if other > 0 {
		note += fmt.Sprintf("; расход в другой валюте не вычтен: %s", plural(other, "вызов", "вызова", "вызовов"))
	}
	if left <= 0 {
		note += "; оценка ушла в ноль или ниже — вероятно, вы пополняли счёт: впишите новое «было»"
	}
	b.Note = note
	return b
}

// shortBaseDate — «2026-09-30» → «30.09».
func shortBaseDate(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("02.01")
	}
	return d
}

// ---- ключ управления ----

// managementSecret — имя секрета с ключом управления провайдера. Он отдельный
// от ключа для запросов: у OpenRouter обычный ключ адрес остатка не пускает, а
// ключ управления, наоборот, не годится для запросов к моделям.
func managementSecret(provider string) string { return providerSecret(provider) + ":management" }

// ManagementKey — ключ управления провайдера; пусто — не задан.
func (e *Engine) ManagementKey(provider string) string {
	v, _ := e.store.SecretValue(managementSecret(provider))
	return v
}

// SetManagementKey — задать или убрать ключ управления.
func (e *Engine) SetManagementKey(provider, key string) error {
	provider, key = strings.TrimSpace(provider), strings.TrimSpace(key)
	if provider == "" {
		return fmt.Errorf("нужен провайдер")
	}
	if key == "" {
		return e.store.DeleteSecret(managementSecret(provider))
	}
	return e.store.SaveSecret(&store.Secret{
		Name: managementSecret(provider), Kind: store.SecretToken,
		Note: "ключ управления провайдера · " + provider + " · только остаток на счёте",
	}, key)
}

// SetBalanceSource — источник остатка у провайдера и порог предупреждения.
func (e *Engine) SetBalanceSource(provider, kind string, low float64) error {
	switch kind {
	case BalanceAPI, BalanceEstimate, BalanceNone:
	default:
		return fmt.Errorf("вид источника остатка: api, estimate или none")
	}
	if low < 0 {
		return fmt.Errorf("порог не может быть отрицательным")
	}
	list := e.Providers()
	for i, p := range list {
		if p.Name != provider {
			continue
		}
		if kind == BalanceAPI && !canBalance(p.BaseURL) {
			return fmt.Errorf("у «%s» документированного адреса остатка нет — выберите оценку", provider)
		}
		list[i].BalanceKind, list[i].BalanceLow = kind, low
		return e.SaveProviders(list)
	}
	return fmt.Errorf("провайдера «%s» нет в справочнике", provider)
}

// ---- предупреждение ----

// balanceNotify — уведомление об остатке: «не хватает» (только у api) — тревога,
// ниже порога — предупреждение, в норме — карточка закрывается. Поднимается
// на переходе, а не каждые полчаса: человек, отложивший карточку, не должен
// получать её заново, пока остаток не вернулся в норму и не упал снова.
func (e *Engine) balanceNotify(p ProviderConf, b Balance) {
	if b.Err != "" || b.At.IsZero() {
		return // не узнали: про сбой говорит строка на «Здоровье», а не тревога о деньгах
	}
	key := "balance:" + p.Name
	level := ""
	title, summary := "", ""
	byEstimate := ""
	if b.Kind == BalanceEstimate {
		byEstimate = " (по оценке)"
	}
	switch {
	case b.Kind == BalanceAPI && !b.Enough:
		level = "alert"
		title = "На счёте " + p.Name + " не хватает денег"
		summary = "Провайдер сообщает, что остатка не хватает на запросы: " + b.Text() + ". Следующий вызов его моделей не пройдёт."
	case p.BalanceLow > 0 && len(b.Amounts) > 0 && b.Amounts[0].Total < p.BalanceLow:
		level = "warn"
		sign := CurrencySign(b.Amounts[0].Currency)
		title = fmt.Sprintf("Остаток на счёте %s ниже порога%s", p.Name, byEstimate)
		summary = fmt.Sprintf("%s%.2f при пороге %s%.2f. %s", sign, b.Amounts[0].Total, sign, p.BalanceLow, b.Text())
	}
	last := e.store.Setting(balanceStateKey(p.Name), "")
	if level == "" {
		if last != "" {
			_ = e.store.SetSetting(balanceStateKey(p.Name), "")
			e.CloseNotification(key, store.NotifyDone, "остаток в норме")
		}
		return
	}
	if last == level {
		// уже сказали: карточку не поднимаем заново, только обновляем цифры
		if n, ok := e.store.OpenNotificationByKey(key); ok {
			_ = e.store.UpdateNotificationLevel(n.ID, level, summary)
		}
		return
	}
	_ = e.store.SetSetting(balanceStateKey(p.Name), level)
	if _, err := e.Notify(&store.Notification{
		Key: key, Source: "core", Level: level, Title: title, Summary: summary,
		Actions: []store.NotifyAction{
			{Kind: "goto", Label: "Открыть остаток " + p.Name, Screen: ProviderScreen(p.Name, "balance"), Style: "primary"},
			{Kind: "dismiss", Label: "Понятно"},
		},
	}); err != nil {
		log.Printf("остаток %s: уведомление не легло: %v", p.Name, err)
	}
}

// Text — как остаток читается человеком. Валюты перечисляются все:
// пересчитывать юани в доллары мы не станем — курс наш выдуманный, а
// деньги чужие.
func (o Balance) Text() string {
	if o.Kind == BalanceEstimate {
		return o.estimateText()
	}
	if o.At.IsZero() && o.Err == "" {
		// молчать нельзя, а ноль — это враньё о деньгах
		return "ещё не спрашивал"
	}
	if o.Err != "" {
		return "остаток не узнать: " + o.Err
	}
	var parts []string
	for _, a := range o.Amounts {
		sign := CurrencySign(a.Currency)
		chunk := fmt.Sprintf("%s%.2f", sign, a.Total)
		if a.Granted > 0 {
			chunk += fmt.Sprintf(" (из них подарено %s%.2f)", sign, a.Granted)
		}
		parts = append(parts, chunk)
	}
	s := strings.Join(parts, " · ")
	if s == "" {
		s = "ноль"
	}
	if !o.Enough {
		s += " · на запросы не хватает"
	}
	return s
}

// estimateText — оценка всегда с оговоркой, и оговорка не мельче числа.
func (o Balance) estimateText() string {
	if o.Err != "" {
		return "оценку не посчитать: " + o.Err
	}
	if o.At.IsZero() || len(o.Amounts) == 0 {
		return o.Note
	}
	a := o.Amounts[0]
	sign := CurrencySign(a.Currency)
	if a.Currency == "" {
		sign = "$"
	}
	return fmt.Sprintf("≈ %s%.2f — %s", sign, a.Total, o.Note)
}

// Label — откуда цифра: «с провайдера» или «оценка».
func (o Balance) Label() string {
	if o.Kind == BalanceEstimate {
		return "оценка"
	}
	return "с провайдера"
}

// CurrencySign — знак или сам код, если знака не знаем. Выдумывать знак для
// незнакомой валюты нельзя: «$» вместо юаня — это враньё о сумме.
func CurrencySign(code string) string {
	switch strings.ToUpper(code) {
	case "USD":
		return "$"
	case "CNY", "RMB":
		return "¥"
	case "EUR":
		return "€"
	case "RUB":
		return "₽"
	}
	if code == "" {
		return ""
	}
	if code == "$" || code == "€" || code == "₽" || code == "¥" {
		return code
	}
	return code + " "
}
