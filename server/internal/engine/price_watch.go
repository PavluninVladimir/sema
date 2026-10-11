package engine

// Цены у провайдеров: забрать, сверить с нашими, предложить человеку.
//
// Провайдеры меняют цены и не присылают об этом писем, а неверная цена хуже
// отсутствующей: по ней считаются бюджеты и решения. Поэтому правило то же,
// что у скиллов и модулей: Сёма **предлагает**, принимает человек. Одна
// карточка на провайдера — «модель, было, станет» — и «Принять все».
// Цена, которую человек вписал руками, пачкой не меняется: строка помечена
// «ваша цена, на странице другая» и принимается отдельным решением.
//
// Откуда цены, говорит сам провайдер в справочнике (`ProviderConf.PriceKind`):
//   - api — JSON с ценами за токен (OpenRouter), разбор кодом;
//   - page — страница цен: текст с таблицами читает `fetchPriceText`, разбирает
//     модель роли «Модули», а ответ проверяет код (price_verify.go);
//   - reported — цену провайдер называет сам в каждом ответе (Perplexity),
//     сверять нечего;
//   - none — цен не берём.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const WorkerPrices = "prices"

// pricesSourceLegacyKey — единственная страница цен, как было до источников
// у провайдеров. Настройки больше нет: адрес переехал к DeepSeek.
const pricesSourceLegacyKey = "prices.page"

// Виды строк предложения.
const (
	priceNew     = "new"     // у модели цены не было
	priceChanged = "changed" // цена изменилась, а прежнюю принял сам прайс
	priceMine    = "mine"    // на странице другая, а прежнюю вписал человек
)

// Кто поставил цену модели.
const (
	priceByHuman = "человек"
	// PriceByPrice — цену поставил прайс провайдера (принята из карточки
	// проверки или пришла в списке моделей)
	PriceByPrice = "прайс"
	priceByPrice = PriceByPrice
)

// PriceItem — строка предложения.
type PriceItem struct {
	Model    string `json:"model"` // наше имя модели
	PageName string `json:"page_name,omitempty"`
	Was      string `json:"was,omitempty"`
	Price    string `json:"price"`
	Peak     string `json:"peak,omitempty"`
	Hours    string `json:"hours,omitempty"`
	Kind     string `json:"kind"`
	Note     string `json:"note,omitempty"`
}

// PriceProposal — то, что лежит в карточке «Уведомлений» и ждёт человека.
type PriceProposal struct {
	Provider string      `json:"provider"`
	At       string      `json:"at"`
	Source   string      `json:"source"` // адрес
	Items    []PriceItem `json:"items"`
}

// PriceCheck — итог последней проверки провайдера: строка у него на экране.
type PriceCheck struct {
	Provider string `json:"provider"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	OK       bool   `json:"ok"`
	Line     string `json:"line"` // «новых 12, изменилось 3» или «цены не разобраны: причина»
	New      int    `json:"new"`
	Changed  int    `json:"changed"`
	Mine     int    `json:"mine"`
	Same     int    `json:"same"`
}

func priceProposalKey(provider string) string { return "price.proposal." + provider }
func priceLastKey(provider string) string     { return "price.last." + provider }
func priceOfferedKey(provider string) string  { return "price.offered." + provider }

// proposalPrint — отпечаток предложения: те же строки с теми же ценами —
// то же предложение, сколько бы раз ни проверяли.
func proposalPrint(prop PriceProposal) string {
	h := sha256.New()
	for _, it := range prop.Items {
		fmt.Fprintf(h, "%s|%s|%s|%s|%s\n", it.Kind, it.Model, it.Price, it.Peak, it.Was)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// priceRun — кто сейчас проверяется. Проверка идёт в своей нитке: страница и
// модель занимают минуту, а кнопка не должна висеть.
type priceRun struct {
	mu      sync.Mutex
	running map[string]bool
}

func (r *priceRun) start(provider string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[provider] {
		return false
	}
	if r.running == nil {
		r.running = map[string]bool{}
	}
	r.running[provider] = true
	return true
}

func (r *priceRun) done(provider string) {
	r.mu.Lock()
	delete(r.running, provider)
	r.mu.Unlock()
}

// PriceChecking — идёт ли сейчас проверка провайдера.
func (e *Engine) PriceChecking(provider string) bool {
	e.prices.mu.Lock()
	defer e.prices.mu.Unlock()
	return e.prices.running[provider]
}

// StartPriceCheck — проверить цены провайдера в отдельной нитке: кнопка и
// подключение провайдера не ждут страницы и модели. false — проверка уже
// идёт. Итог ложится в «последнюю проверку» и, если есть что предложить, в
// «Уведомления».
func (e *Engine) StartPriceCheck(provider string) bool {
	if !e.prices.start(provider) {
		return false
	}
	go func() {
		defer e.prices.done(provider)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("цены %s: проверка упала: %v", provider, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		e.CheckPrices(ctx, provider, true)
		e.Changed(append(providerScreens(provider), "notifications")...)
	}()
	return true
}

// providerModelConfs — подключённые модели провайдера, которым нужна цена
// (клиенты по подписке денег не стоят).
func (e *Engine) providerModelConfs(provider string) []ModelConf {
	catalog := e.Providers()
	var out []ModelConf
	for _, c := range e.userModels() {
		if c.Type == "cli" || providerOf(catalog, c.Type, c.BaseURL) != provider {
			continue
		}
		out = append(out, c)
	}
	return out
}

// pricesPass — задача «Цены у провайдеров»: все источники api и page раз в
// сутки. Возвращает строку для журнала задачи.
func (e *Engine) pricesPass(ctx context.Context) string {
	var parts []string
	for _, p := range e.Providers() {
		if p.PriceKind != PriceAPI && p.PriceKind != PricePage {
			continue
		}
		if len(e.providerModelConfs(p.Name)) == 0 {
			continue // у провайдера нет подключённых моделей: цены считать не для чего
		}
		// кнопка человека уже ведёт эту проверку: второй вызов модели зря
		if !e.prices.start(p.Name) {
			parts = append(parts, p.Name+": уже проверяется")
			continue
		}
		res := e.CheckPrices(ctx, p.Name, false)
		e.prices.done(p.Name)
		parts = append(parts, p.Name+": "+res.Line)
	}
	if len(parts) == 0 {
		return "источников цен для подключённых провайдеров нет"
	}
	return strings.Join(parts, " · ")
}

// CheckPrices — проверить цены одного провайдера и, если есть что
// предложить, положить карточку в «Уведомления». Итог — в настройках
// (`price.last.<провайдер>`): по нему экран говорит, когда и чем кончилась
// проверка. Не разобралось — строка «цены не разобраны: причина», а не
// карточка-пустышка. manual — проверку попросил человек (кнопка, подключение
// провайдера): карточка ложится всегда. Суточная задача повторяет карточку
// только если предложение стало другим: отложенное «Не сейчас» не должно
// возвращаться каждое утро тем же самым.
func (e *Engine) CheckPrices(ctx context.Context, provider string, manual bool) PriceCheck {
	res := PriceCheck{Provider: provider, At: time.Now().Format(time.RFC3339)}
	fail := func(why string) PriceCheck {
		res.Line = "цены не разобраны: " + why
		e.savePriceCheck(res)
		log.Printf("цены %s: %s", provider, res.Line)
		return res
	}
	p, ok := e.Provider(provider)
	if !ok {
		return fail("провайдера нет в справочнике")
	}
	res.Kind, res.URL = p.PriceKind, p.PriceURL
	models := e.providerModelConfs(provider)
	if len(models) == 0 {
		return fail("у провайдера нет подключённых моделей")
	}

	var found []foundPrice
	var st priceStats
	var err error
	switch p.PriceKind {
	case PriceReported:
		res.OK, res.Line = true, "цену называет сам провайдер в каждом ответе — сверять нечего"
		return res
	case PriceAPI:
		found, st, err = e.pricesFromAPI(p, models)
	case PricePage:
		found, st, err = e.pricesFromPage(ctx, p, models)
	default:
		return fail("источник цен не задан")
	}
	if err != nil {
		return fail(shortErr(err.Error()))
	}

	prop := e.buildProposal(p, models, found)
	for _, it := range prop.Items {
		switch it.Kind {
		case priceNew:
			res.New++
		case priceChanged:
			res.Changed++
		case priceMine:
			res.Mine++
		}
	}
	res.Same = len(found) - len(prop.Items)
	res.OK = true
	res.Line = priceCheckLine(res, st, len(models)-len(found))
	if len(found) == 0 {
		// страница разобралась, но ни одна наша модель на ней не нашлась:
		// это не «цены совпадают», а «не знаем, к чему цены»
		res.OK = false
		res.Line = "цены не разобраны: ни одна из " + strconv.Itoa(len(models)) +
			" моделей не найдена по имени на странице — назовите её имя в прайсе в строке модели" + priceLineTail(st)
		e.savePriceCheck(res)
		return res
	}
	e.savePriceCheck(res)
	e.offerPrices(p, prop, res, manual)
	return res
}

// priceCheckLine — итог проверки словами; нулевые части не пишем.
func priceCheckLine(res PriceCheck, st priceStats, without int) string {
	var parts []string
	add := func(n int, format string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf(format, n))
		}
	}
	add(res.New, "новых %d")
	add(res.Changed, "изменилось %d")
	add(res.Mine, "ваших с другой ценой %d")
	if res.New+res.Changed+res.Mine == 0 {
		parts = append(parts, fmt.Sprintf("цены совпадают у %d моделей", res.Same))
	} else {
		add(res.Same, "совпало %d")
	}
	add(without, "без цены на странице %d")
	line := strings.Join(parts, ", ")
	return line + priceLineTail(st)
}

func priceLineTail(st priceStats) string {
	var parts []string
	if st.Dropped > 0 {
		parts = append(parts, fmt.Sprintf("не подтверждено страницей %d (%s)", st.Dropped, strings.Join(st.DroppedWhy, "; ")))
	}
	if st.Free > 0 {
		parts = append(parts, fmt.Sprintf("бесплатных %d", st.Free))
	}
	if len(st.UnmatchedNames) > 0 {
		parts = append(parts, "на странице без нашей модели: "+strings.Join(st.UnmatchedNames, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// pricesFromPage — цены со страницы: модель разбирает, код проверяет. Нет
// модели — для страницы DeepSeek есть запасной разбор регулярками, для
// прочих честная строка: гадать по чужой вёрстке нельзя.
func (e *Engine) pricesFromPage(ctx context.Context, p ProviderConf, models []ModelConf) ([]foundPrice, priceStats, error) {
	var st priceStats
	if p.PriceURL == "" {
		return nil, st, fmt.Errorf("адрес страницы цен не задан")
	}
	doc, raw, err := fetchPriceText(p.PriceURL, e.PriceToken(p.Name), e.priceTextLimit())
	if err != nil {
		return nil, st, fmt.Errorf("страница не открылась: %w", err)
	}
	answer, _, err := e.askRoleGuarded(llm.WithConsumer(ctx, "core:prices"), RoleModules, pricePrompt(p, doc.Text, models), true)
	if err != nil {
		if p.Name == "DeepSeek" || strings.Contains(strings.ToLower(p.PriceURL), "deepseek") {
			if found := priceFallback(raw, models); len(found) > 0 {
				log.Printf("цены %s: модель роли «%s» не ответила (%v), взят запасной разбор", p.Name, RoleModules, err)
				return found, st, nil
			}
		}
		return nil, st, fmt.Errorf("модель роли «%s» (%s) не ответила: %v — назначьте роли модель посильнее на «Модели → Роли»",
			RoleModules, e.Role(RoleModules).Model, shortErr(err.Error()))
	}
	answers, err := parsePriceAnswer(answer)
	if err != nil {
		return nil, st, err
	}
	st.Asked = len(answers)
	verified := crossCheckTable(raw, verifyAnswers(doc, e.priceSkipList(), answers, &st), &st)
	return bindPrices(verified, models, &st), st, nil
}

// priceFallback — запасной разбор DeepSeek: регулярки по столбцам.
func priceFallback(raw string, models []ModelConf) []foundPrice {
	table := priceFromCells(cells(raw))
	var out []foundPrice
	for _, c := range models {
		full := priceForModel(table, c)
		if full == "" {
			continue
		}
		price, peak, hours := splitPeak(full)
		out = append(out, foundPrice{Model: c.Name, PageName: c.PriceName, Price: price, Peak: peak, Hours: hours,
			Note: "разобрано запасным способом, без модели"})
	}
	return out
}

// splitPeak — «обычная | пик | часы» на три части, как их принимает
// SetModelPrice.
func splitPeak(price string) (main, peak, hours string) {
	parts := strings.Split(price, "|")
	main = strings.TrimSpace(parts[0])
	if len(parts) >= 3 {
		peak, hours = strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	}
	return main, peak, hours
}

// buildProposal — найденные цены против нынешних. Не изменилось — строки
// нет. Различаются три случая: у модели цены не было; прежнюю поставил
// сам прайс и она изменилась; прежнюю вписал человек (или неизвестно кто —
// цены до появления отметки) и на странице другая: такую пачкой не меняем.
func (e *Engine) buildProposal(p ProviderConf, models []ModelConf, found []foundPrice) PriceProposal {
	byName := map[string]ModelConf{}
	for _, c := range models {
		byName[c.Name] = c
	}
	prop := PriceProposal{Provider: p.Name, At: time.Now().Format(time.RFC3339), Source: p.PriceURL}
	for _, f := range found {
		c := byName[f.Model]
		it := PriceItem{Model: f.Model, PageName: f.PageName, Was: c.Price, Price: f.Price,
			Peak: f.Peak, Hours: f.Hours, Note: f.Note}
		same := linePricesMatch(c.Price, f.Price) && (f.Peak == "" || linePricesMatch(c.PricePeak, f.Peak))
		switch {
		case strings.TrimSpace(c.Price) == "":
			it.Kind = priceNew
		case same:
			continue
		case c.PriceBy == priceByPrice:
			it.Kind = priceChanged
		default:
			it.Kind = priceMine
		}
		prop.Items = append(prop.Items, it)
	}
	return prop
}

// briefPrice — цена коротко, для строки карточки.
func briefPrice(s string) string {
	if strings.TrimSpace(s) == "" {
		return "не задана"
	}
	if v, cur, ok := parsePerMinute(s); ok {
		return fmt.Sprintf("%s%g / мин", cur, v)
	}
	in, out, cache, cur := parsePrices3(s)
	if cur == "" {
		cur = "$"
	}
	line := fmt.Sprintf("%s%g / %s%g", cur, in, cur, out)
	if cache > 0 {
		line += fmt.Sprintf(" · кэш %s%g", cur, cache)
	}
	return line
}

// offerPrices — карточка в «Уведомлениях» или тишина: совпало — прежняя
// карточка про этого провайдера закрывается, а новой нет.
func (e *Engine) offerPrices(p ProviderConf, prop PriceProposal, res PriceCheck, manual bool) {
	key := "price:" + p.Name
	if len(prop.Items) == 0 {
		_ = e.store.SetSetting(priceProposalKey(p.Name), "")
		_ = e.store.SetSetting(priceOfferedKey(p.Name), "")
		e.CloseNotification(key, store.NotifyDone, "цены совпали со страницей")
		return
	}
	fp := proposalPrint(prop)
	if !manual && e.store.Setting(priceOfferedKey(p.Name), "") == fp {
		return // то же предложение уже показывали: молчим, а карточка, если ещё открыта, работает
	}
	raw, err := json.Marshal(prop)
	if err != nil {
		return
	}
	if err := e.store.SetSetting(priceProposalKey(p.Name), string(raw)); err != nil {
		log.Printf("цены %s: предложение не сохранено: %v", p.Name, err)
		return
	}
	_ = e.store.SetSetting(priceOfferedKey(p.Name), fp)

	// в карточку — двадцать строк, сперва то, что важнее: изменившееся, потом
	// ваши цены, потом новые. Остальное числом: принимаются все разом
	var fields []store.NotifyField
	for _, kind := range []string{priceChanged, priceMine, priceNew} {
		for _, it := range prop.Items {
			if it.Kind != kind || len(fields) >= 20 {
				continue
			}
			f := store.NotifyField{Label: it.Model}
			switch kind {
			case priceMine:
				f.Level = "warn"
				f.Value = "ваша цена, на странице другая: " + briefPrice(it.Was) + " → " + briefPrice(it.Price)
			case priceChanged:
				f.Value = briefPrice(it.Was) + " → " + briefPrice(it.Price)
			default:
				f.Level = "ok"
				f.Value = "не задана → " + briefPrice(it.Price)
			}
			if it.Note != "" {
				f.Value += " · " + it.Note
			}
			fields = append(fields, f)
		}
	}
	if rest := len(prop.Items) - len(fields); rest > 0 {
		fields = append(fields, store.NotifyField{Label: "Остальное", Value: fmt.Sprintf("ещё %d строк — принимаются вместе с этими", rest)})
	}

	var title []string
	if res.New > 0 {
		title = append(title, fmt.Sprintf("новых %d", res.New))
	}
	if res.Changed > 0 {
		title = append(title, fmt.Sprintf("изменилось %d", res.Changed))
	}
	if res.Mine > 0 {
		title = append(title, fmt.Sprintf("ваших с другой ценой %d", res.Mine))
	}
	safe := res.New + res.Changed
	actions := []store.NotifyAction{}
	if safe > 0 {
		actions = append(actions, store.NotifyAction{Kind: "core", Op: "accept_prices", Ref: p.Name, Arg: "all",
			Label: fmt.Sprintf("Принять все · %d", safe), Style: "primary"})
	}
	if res.New > 0 && res.Changed > 0 {
		actions = append(actions, store.NotifyAction{Kind: "core", Op: "accept_prices", Ref: p.Name, Arg: "new",
			Label: fmt.Sprintf("Принять только новые · %d", res.New)})
	}
	if res.Mine > 0 {
		actions = append(actions, store.NotifyAction{Kind: "core", Op: "accept_prices", Ref: p.Name, Arg: "force",
			Label: fmt.Sprintf("Принять и заменить ваши · %d", res.Mine)})
	}
	// сверить глазами: экран провайдера с источником цен и итогом проверки
	actions = append(actions,
		store.NotifyAction{Kind: "goto", Label: "Цены " + p.Name, Screen: ProviderScreen(p.Name, "prices")},
		store.NotifyAction{Kind: "dismiss", Label: "Не сейчас"})

	summary := fmt.Sprintf("Источник: %s · проверено %s. Цены встанут в формате Сёмы; по одной их можно поставить в строке модели. "+
		"Цена, вписанная вами, пачкой не меняется: она помечена, и заменить её можно только отдельной кнопкой.",
		prop.Source, whenShortTime(prop.At))
	_, _ = e.Notify(&store.Notification{
		Key: key, Source: "core", Level: "info",
		Title:   "Цены · " + p.Name + ": " + strings.Join(title, ", "),
		Summary: summary, Fields: fields, Actions: actions,
	})
}

// whenShortTime — «30.09 19:20» из RFC3339.
func whenShortTime(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.Local().Format("02.01 15:04")
}

// LoadPriceProposal — предложение, которое ждёт человека.
func (e *Engine) LoadPriceProposal(provider string) (PriceProposal, bool) {
	raw := e.store.Setting(priceProposalKey(provider), "")
	if raw == "" {
		return PriceProposal{}, false
	}
	var prop PriceProposal
	if err := json.Unmarshal([]byte(raw), &prop); err != nil || len(prop.Items) == 0 {
		return PriceProposal{}, false
	}
	return prop, true
}

// AcceptPrices — принять предложение: `new` — только новые, `all` — новые и
// изменившиеся, `force` — ещё и ваши цены, с явной отметкой человека.
// Строка, у которой цена с тех пор изменилась (человек успел поставить свою),
// пропускается: предложение считалось от старой. Возвращает, сколько цен
// встало и сколько пропущено.
func (e *Engine) AcceptPrices(provider, mode string) (applied, skipped int, err error) {
	prop, ok := e.LoadPriceProposal(provider)
	if !ok {
		return 0, 0, fmt.Errorf("предложения цен по «%s» больше нет — проверьте цены заново", provider)
	}
	take := func(kind string) bool {
		switch mode {
		case "new":
			return kind == priceNew
		case "force":
			return true
		}
		return kind == priceNew || kind == priceChanged
	}
	list, err := e.userModelsE()
	if err != nil {
		return 0, 0, err
	}
	now := time.Now().Format(time.RFC3339)
	for _, it := range prop.Items {
		if !take(it.Kind) {
			continue
		}
		done := false
		for i := range list {
			if list[i].Name != it.Model {
				continue
			}
			if strings.TrimSpace(list[i].Price) != strings.TrimSpace(it.Was) {
				break // цену успели поменять после проверки
			}
			list[i].Price, list[i].PriceBy, list[i].PriceAt = it.Price, priceByPrice, now
			if it.Peak != "" {
				list[i].PricePeak, list[i].Peak = it.Peak, it.Hours
			}
			done = true
			break
		}
		if done {
			applied++
		} else {
			skipped++
		}
	}
	if applied > 0 {
		if err := e.saveUserModels(list); err != nil {
			return 0, 0, err
		}
	}
	// принято всё, что было можно: предложение больше не нужно, а оставшееся
	// (ваши цены при «Принять все») вернётся со следующей проверкой
	_ = e.store.SetSetting(priceProposalKey(provider), "")
	log.Printf("цены %s: принято %d, пропущено %d (режим %s)", provider, applied, skipped, mode)
	return applied, skipped, nil
}

// savePriceCheck — итог проверки для строки у провайдера.
func (e *Engine) savePriceCheck(res PriceCheck) {
	if raw, err := json.Marshal(res); err == nil {
		_ = e.store.SetSetting(priceLastKey(res.Provider), string(raw))
	}
}

// LastPriceCheck — итог последней проверки; false — не проверяли.
func (e *Engine) LastPriceCheck(provider string) (PriceCheck, bool) {
	raw := e.store.Setting(priceLastKey(provider), "")
	if raw == "" {
		return PriceCheck{}, false
	}
	var res PriceCheck
	if json.Unmarshal([]byte(raw), &res) != nil {
		return PriceCheck{}, false
	}
	return res, true
}

// migratePricePage — разовый переезд: страница цен была одна на всех
// (`prices.page`), теперь она у провайдера. Адрес, который человек поставил
// сам, становится источником DeepSeek; настройка после этого не нужна. Не
// поставил — DeepSeek получает страницу по умолчанию, если пресеты ещё не
// успели её вписать. Карточка «модели не опознаны» из прежней проверки
// закрывается: связку теперь предлагает каждая проверка.
func (e *Engine) migratePricePage() {
	const mark = "prices.page_moved"
	if e.store.Setting(mark, "") == "1" {
		return
	}
	explicit := strings.TrimSpace(e.store.Setting(pricesSourceLegacyKey, ""))
	list := e.Providers()
	for i, p := range list {
		if p.Name != "DeepSeek" {
			continue
		}
		switch {
		case explicit != "" && p.PriceURL != explicit:
			list[i].PriceKind, list[i].PriceURL = PricePage, explicit
		case p.PriceKind == "":
			seed, _ := seedProvider("DeepSeek")
			list[i].PriceKind, list[i].PriceURL = seed.PriceKind, seed.PriceURL
		default:
			continue
		}
		if err := e.SaveProviders(list); err != nil {
			log.Printf("цены: страница цен не переехала к DeepSeek: %v", err)
			return
		}
		log.Printf("цены: страница цен %s у провайдера DeepSeek", list[i].PriceURL)
	}
	e.store.DeleteSettingKey(pricesSourceLegacyKey)
	e.CloseNotification("price:связка", store.NotifyDone, "цены теперь проверяются по провайдерам")
	_ = e.store.SetSetting(mark, "1")
}

// SetPriceSource — источник цен у провайдера: вид и адрес. Адрес нужен
// api и page; для reported и none он не читается, но остаётся — переключить
// обратно проще, чем вписывать заново.
func (e *Engine) SetPriceSource(provider, kind, url string) error {
	kind, url = strings.TrimSpace(kind), strings.TrimSpace(url)
	switch kind {
	case PriceAPI, PricePage:
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return fmt.Errorf("адрес источника цен должен начинаться с http:// или https://")
		}
	case PriceReported, PriceNone:
	default:
		return fmt.Errorf("вид источника цен: api, page, reported или none")
	}
	list := e.Providers()
	for i, p := range list {
		if p.Name != provider {
			continue
		}
		list[i].PriceKind, list[i].PriceURL = kind, url
		return e.SaveProviders(list)
	}
	return fmt.Errorf("провайдера «%s» нет в справочнике", provider)
}

// PriceCoverage — сколько подключённых моделей провайдера и у скольких из
// них цена не названа: без неё расход по модели не считается.
func (e *Engine) PriceCoverage(provider string) (total, without int) {
	for _, c := range e.providerModelConfs(provider) {
		total++
		if strings.TrimSpace(c.Price) == "" {
			without++
		}
	}
	return total, without
}

// NoPriceWithSpend — модели без цены, у которых за последние дни были
// вызовы: расход по ним считается нулём, и это не пробел на будущее, а
// дыра в уже записанном счёте. Имена — по числу вызовов, больше первыми.
func (e *Engine) NoPriceWithSpend(days int) []string {
	sums, err := e.store.UsageBy("model", time.Now().AddDate(0, 0, -days), "")
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range sums {
		if s.Calls == 0 || s.Cost > 0 || !e.ModelIsRemote(s.Key) || e.PriceOf(s.Key) != "" {
			continue
		}
		if _, ok := e.modelConf(s.Key); ok {
			out = append(out, s.Key)
		}
	}
	return out
}

// modelConf — подключённая модель по имени.
func (e *Engine) modelConf(name string) (ModelConf, bool) {
	list, idx, err := e.modelsShared()
	if err != nil {
		return ModelConf{}, false
	}
	if i, ok := idx[name]; ok {
		return list[i], true
	}
	return ModelConf{}, false
}

// ---- какие разделы страницы не берём ----

const (
	priceSkipKey = "prices.skip_sections"
	// priceSkipDefault — слова в заголовках разделов, под которыми цены не про
	// обычный вызов модели: дообучение (там цены инференса дообученных моделей
	// с теми же именами и датами), пакетный, гибкий и приоритетный тарифы,
	// звук и реалтайм-сессии, встроенные инструменты. Значение по умолчанию,
	// а не закон: список правится в «Тонкой настройке» вкладки «Провайдеры».
	priceSkipDefault = "fine-tun, finetun, training, batch, flex, priority, realtime, real-time, live session, tool"
)

// PriceSkipSections — слова заголовков, чьи разделы страницы цен не берутся.
func (e *Engine) PriceSkipSections() string {
	return e.store.SettingText(priceSkipDef)
}

// SetPriceSkipSections — задать слова; пусто — вернуть умолчание: пустой
// список пустил бы в разбор дообучение и пакетный тариф.
func (e *Engine) SetPriceSkipSections(v string) error {
	if strings.TrimSpace(v) == "" {
		v = priceSkipDefault
	}
	return e.store.SetSetting(priceSkipKey, v)
}

func (e *Engine) priceSkipList() []string {
	var out []string
	for _, w := range strings.Split(e.PriceSkipSections(), ",") {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// ---- токен источника цен ----

// priceTokenSecret — секрет с токеном источника цен. Он отдельный от ключа
// для запросов к моделям и от ключа управления остатком: адрес с ценами может
// потребовать свой ключ, а тот, что даёт доступ к моделям, туда отдавать нельзя.
func priceTokenSecret(provider string) string { return providerSecret(provider) + ":prices" }

// PriceToken — токен источника цен; пусто — не задан, источник открытый.
func (e *Engine) PriceToken(provider string) string {
	v, _ := e.store.SecretValue(priceTokenSecret(provider))
	return v
}

// SetPriceToken — задать или убрать токен источника цен.
func (e *Engine) SetPriceToken(provider, token string) error {
	provider, token = strings.TrimSpace(provider), strings.TrimSpace(token)
	if provider == "" {
		return fmt.Errorf("нужен провайдер")
	}
	if token == "" {
		return e.store.DeleteSecret(priceTokenSecret(provider))
	}
	return e.store.SaveSecret(&store.Secret{
		Name: priceTokenSecret(provider), Kind: store.SecretToken,
		Note: "токен источника цен провайдера · " + provider + " · только чтение страницы или JSON с ценами",
	}, token)
}
