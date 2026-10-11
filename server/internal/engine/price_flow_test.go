package engine

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Проверка цен: страница → модель → проверка кодом → одна карточка →
// «Принять все». Страница и модель поддельные, но устроены как настоящие:
// страница OpenAI отдаёт таблицу свойствами острова Astro, со сносками,
// тирами и звуком; DeepSeek — таблицей по столбцам.

// priceModel — поддельная модель роли: отвечает по заданию и помнит,
// что ей прислали.
type priceModel struct {
	mu     sync.Mutex
	answer string
	err    error
	calls  int
	last   string
}

func (m *priceModel) Name() string { return "цены-проба" }

func (m *priceModel) Complete(ctx context.Context, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.last = prompt
	llm.Report(ctx, "цены-проба", "text", prompt, m.answer, llm.Usage{In: 10, Out: 5, Exact: true}, time.Now(), m.err)
	return m.answer, m.err
}

// pricePageOpenAI — как страница цен OpenAI: таблица обычного тарифа лежит
// в свойствах острова, а не в видимой разметке; пакетный тариф — отдельный
// остров; у меню и подвала цены чужие и попасть в ответ не должны.
const pricePageOpenAI = `<!doctype html><html><head><title>Pricing</title><script>var junk = 1</script></head><body>
<nav><a href="/a">Docs</a> gpt-9-menu $1 $2</nav>
<main><h1>Pricing</h1><h2>Flagship models</h2>
<p>Prices per 1M tokens. The tables below list the standard tier; batch, flex and priority processing are priced separately.
Regional processing endpoints are charged a 10% uplift. Prices are in USD and exclude taxes.</p>
<astro-island uid="a" component-url="/x.js" props="{&quot;tier&quot;:[0,&quot;standard&quot;],&quot;rows&quot;:[1,[` +
	`[1,[[0,&quot;gpt-6-luna&quot;],[0,0.1],[0,0.01],[0,0.125],[0,0.5]]],` +
	`[1,[[0,&quot;gpt-6-sol&quot;],[0,2],[0,0.2],[0,2.5],[0,10]]],` +
	`[1,[[0,&quot;gpt-5.4 (&lt;272K context length)&quot;],[0,2.5],[0,0.25],[0,&quot;-&quot;],[0,15]]],` +
	`[1,[[0,&quot;gpt-5.4-mini&quot;],[0,0.75],[0,0.075],[0,4.5]]],` +
	`[1,[[0,&quot;gpt-4o-mini&quot;],[0,0.15],[0,0.075],[0,0.6]]],` +
	`[1,[[0,&quot;o4-mini&quot;],[0,1.1],[0,0.275],[0,4.4]]],` +
	`[1,[[0,&quot;text-embedding-3-small&quot;],[0,0.02],[0,null],[0,null]]]]]}">
<table><tr><th>Model</th><th>Input</th><th>Cached input</th><th>Cache writes</th><th>Output</th></tr>
<tr><td>gpt-6-luna</td><td>$0.10</td><td>$0.01</td><td>$0.125</td><td>$0.50</td></tr></table></astro-island>
<astro-island uid="b" props="{&quot;tier&quot;:[0,&quot;batch&quot;],&quot;rows&quot;:[1,[[1,[[0,&quot;gpt-6-luna&quot;],[0,0.05],[0,0.005],[0,0.0625],[0,0.25]]]]]}"></astro-island>
<h2>Transcription models</h2>
<table><tr><th>Model</th><th>Use case</th><th>Input</th><th>Output</th><th>Estimated cost</th></tr>
<tr><td>gpt-4o-mini-transcribe</td><td>Transcription</td><td>$1.25</td><td>$5.00</td><td>$0.003 / minute</td></tr></table>
<h2>Finetuning</h2>
<p>Prices per 1M tokens. OpenAI is winding down the fine-tuning platform; fine-tuned models stay available for inference.</p>
<table><tr><th>Model</th><th>Training</th><th>Input</th><th>Cached input</th><th>Output</th></tr>
<tr><td>gpt-4o-mini-2024-07-18</td><td>3</td><td>0.3</td><td>0.15</td><td>1.2</td></tr>
<tr><td>o4-mini-2025-04-16</td><td>$100.00 / hour</td><td>4</td><td>1</td><td>16</td></tr>
<tr><td>gpt-4o-2024-08-06</td><td>25</td><td>3.75</td><td>1.875</td><td>15</td></tr></table>
</main><footer>gpt-8-footer $9</footer></body></html>`

// answerOpenAI — что ответила бы хорошая модель: без выдумок.
const answerOpenAI = `[
 {"model":"gpt-6-luna","page_name":"gpt-6-luna","input":0.1,"cached":0.01,"output":0.5},
 {"model":"gpt-5.4","page_name":"gpt-5.4","input":2.5,"cached":0.25,"output":15,"note":"до 272K контекста"},
 {"model":"gpt-5.4-2026-03-05","page_name":"gpt-5.4","input":2.5,"cached":0.25,"output":15},
 {"model":"gpt-4o-mini","page_name":"gpt-4o-mini","input":0.15,"cached":0.075,"output":0.6},
 {"model":"gpt-4o-mini-transcribe","page_name":"gpt-4o-mini-transcribe","per_minute":0.003},
 {"model":"gpt-5.4-mini","page_name":"gpt-5.4-mini","input":0.75,"cached":0.075,"output":4.5}
]`

// priceRig — движок с моделями OpenAI, поддельной страницей и поддельной
// моделью роли «Модули».
type priceRig struct {
	e     *Engine
	st    *store.Store
	model *priceModel
	page  *string // что отдаёт страница; можно менять по ходу теста
}

func newPriceRig(t *testing.T, page, answer string) *priceRig {
	t.Helper()
	return newPriceRigWith(t, page, answer, "gpt-6-luna", "gpt-5.4", "gpt-5.4-2026-03-05", "gpt-4o-mini", "gpt-4o-mini-transcribe", "text-embedding-3-small")
}

// newPriceRigWith — то же с заданным списком моделей OpenAI.
func newPriceRigWith(t *testing.T, page, answer string, names ...string) *priceRig {
	t.Helper()
	model := &priceModel{answer: answer}
	e, st := newTestEngine(t, "цены-проба", model)
	roles := e.Roles()
	for i := range roles {
		if roles[i].Name == RoleModules {
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "цены-проба", roleNoneName, 30
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	body := page
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	rig := &priceRig{e: e, st: st, model: model, page: &body}
	if err := e.SetPriceSource("OpenAI", PricePage, srv.URL+"/pricing"); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := e.AddModel(ModelConf{Name: name, Type: "openai", BaseURL: "https://api.openai.com/v1", Model: name}); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func (r *priceRig) card(t *testing.T) *store.Notification {
	t.Helper()
	n, ok := r.st.OpenNotificationByKey("price:OpenAI")
	if !ok {
		t.Fatal("карточки цен в «Уведомлениях» нет")
	}
	return n
}

func actionIndex(t *testing.T, n *store.Notification, arg string) int {
	t.Helper()
	for i, a := range n.Actions {
		if a.Op == "accept_prices" && a.Arg == arg {
			return i
		}
	}
	t.Fatalf("у карточки нет кнопки «принять: %s»: %+v", arg, n.Actions)
	return -1
}

func itemOf(prop PriceProposal, model string) (PriceItem, bool) {
	for _, it := range prop.Items {
		if it.Model == model {
			return it, true
		}
	}
	return PriceItem{}, false
}

// Верный ответ → одна карточка с новыми ценами; «Принять все» ставит их в
// формате Сёмы, и расход по ним считается верно. Цена, вписанная руками,
// пачкой не меняется и помечена: «ваша цена, на странице другая».
func TestPriceCheckOfferAndAcceptAll(t *testing.T) {
	rig := newPriceRig(t, pricePageOpenAI, answerOpenAI)
	e := rig.e
	if err := e.SetModelPrice("gpt-4o-mini", "$0.5 / $1 / 1M"); err != nil {
		t.Fatal(err)
	}

	res := e.CheckPrices(context.Background(), "OpenAI", true)
	if !res.OK {
		t.Fatalf("проверка не удалась: %s", res.Line)
	}
	if res.New != 4 || res.Mine != 1 || res.Changed != 0 {
		t.Fatalf("новых %d, ваших %d, изменённых %d; строка: %s", res.New, res.Mine, res.Changed, res.Line)
	}
	if !strings.Contains(res.Line, "без цены на странице 1") {
		t.Errorf("модель без цены на странице (эмбеддинги) не посчитана: %s", res.Line)
	}

	prop, ok := e.LoadPriceProposal("OpenAI")
	if !ok {
		t.Fatal("предложение не сохранено")
	}
	if it, _ := itemOf(prop, "gpt-6-luna"); it.Price != "$0.1 / $0.5 / $0.01 / 1M" || it.Kind != priceNew {
		t.Errorf("gpt-6-luna: %+v", it)
	}
	// у страницы одна строка «gpt-5.4», а у нас модель с датой в хвосте: цена базовой
	if it, ok := itemOf(prop, "gpt-5.4-2026-03-05"); !ok || it.Price != "$2.5 / $15 / $0.25 / 1M" {
		t.Errorf("модель с датой-хвостом не получила цену базовой: %+v", it)
	}
	if it, _ := itemOf(prop, "gpt-4o-mini-transcribe"); it.Price != "$0.003 / мин" {
		t.Errorf("звук считается за минуту: %+v", it)
	}
	if it, _ := itemOf(prop, "gpt-4o-mini"); it.Kind != priceMine || it.Was != "$0.5 / $1 / 1M" {
		t.Errorf("ваша цена не помечена: %+v", it)
	}
	if _, ok := itemOf(prop, "text-embedding-3-small"); ok {
		t.Error("эмбеддинги получили цену, которой на странице нет")
	}
	// в текст, что ушёл модели, не попали меню, подвал и чужие тарифы
	for _, junk := range []string{"gpt-9-menu", "gpt-8-footer", "0.0625"} {
		if strings.Contains(rig.model.last, junk) {
			t.Errorf("в промпт попало лишнее: %q", junk)
		}
	}
	for _, need := range []string{"gpt-5.4-mini\t0.75\t0.075\t4.5", "Тариф: standard", "gpt-4o-mini-transcribe"} {
		if !strings.Contains(rig.model.last, need) {
			t.Errorf("в промпте нет %q — модель не увидит таблицу", need)
		}
	}

	n := rig.card(t)
	if !strings.Contains(n.Title, "OpenAI") || !strings.Contains(n.Title, "новых 4") {
		t.Errorf("заголовок карточки: %q", n.Title)
	}
	var mineShown bool
	for _, f := range n.Fields {
		if f.Label == "gpt-4o-mini" && strings.Contains(f.Value, "ваша цена, на странице другая") {
			mineShown = true
		}
	}
	if !mineShown {
		t.Errorf("в карточке ваша цена не помечена: %+v", n.Fields)
	}

	// «Принять все»: новые встают, ваша цена остаётся
	if err := e.NotifyDecide(context.Background(), n.ID, actionIndex(t, n, "all"), "Вадим"); err != nil {
		t.Fatal(err)
	}
	if got := e.PriceOf("gpt-6-luna"); got != "$0.1 / $0.5 / $0.01 / 1M" {
		t.Fatalf("цена не встала: %q", got)
	}
	if got := e.PriceOf("gpt-4o-mini"); got != "$0.5 / $1 / 1M" {
		t.Fatalf("ваша цена перезаписана пачкой: %q", got)
	}
	if c, _ := e.modelConf("gpt-6-luna"); c.PriceBy != PriceByPrice || c.PriceAt == "" {
		t.Errorf("не отмечено, что цена от прайса: %+v", c)
	}
	if c, _ := e.modelConf("gpt-4o-mini"); c.PriceBy != priceByHuman {
		t.Errorf("ваша цена потеряла отметку: %+v", c)
	}
	if n2, _ := e.store.Notification(n.ID); n2.State != store.NotifyDone {
		t.Errorf("карточка не закрылась: %s", n2.State)
	}
	// расход считается по принятой цене: 0.5M нового входа, 0.5M из кэша, 1M выхода
	cost, cur := e.costOfUsage("gpt-6-luna", llm.Usage{In: 1_000_000, Cached: 500_000, Out: 1_000_000})
	if cost < 0.5549 || cost > 0.5551 || cur != "$" {
		t.Errorf("расход по принятой цене: %v %q, ждали 0.555 $", cost, cur)
	}

	// повторная проверка оставляет на решение только вашу цену; заменить
	// её можно отдельной кнопкой с явным согласием
	res = e.CheckPrices(context.Background(), "OpenAI", true)
	if res.New != 0 || res.Mine != 1 {
		t.Fatalf("после приёма осталось: новых %d, ваших %d (%s)", res.New, res.Mine, res.Line)
	}
	n = rig.card(t)
	for _, a := range n.Actions {
		if a.Op == "accept_prices" && a.Arg == "all" {
			t.Error("«Принять все» без новых и изменённых — кнопка-пустышка")
		}
	}
	if err := e.NotifyDecide(context.Background(), n.ID, actionIndex(t, n, "force"), "Вадим"); err != nil {
		t.Fatal(err)
	}
	if got := e.PriceOf("gpt-4o-mini"); got != "$0.15 / $0.6 / $0.075 / 1M" {
		t.Fatalf("явное согласие не заменило вашу цену: %q", got)
	}
	if c, _ := e.modelConf("gpt-4o-mini"); c.PriceBy != PriceByPrice {
		t.Errorf("после замены цена от прайса: %+v", c)
	}
}

// Только новые: «Принять только новые» не трогает изменившееся.
func TestPriceAcceptOnlyNew(t *testing.T) {
	rig := newPriceRig(t, pricePageOpenAI, answerOpenAI)
	e := rig.e
	// цена от прайса, а на странице другая: это «изменилась», не «ваша»
	if err := e.SetModelPrice("gpt-6-luna", "$0.2 / $0.9 / 1M"); err != nil {
		t.Fatal(err)
	}
	list, _ := e.userModelsE()
	for i := range list {
		if list[i].Name == "gpt-6-luna" {
			list[i].PriceBy = PriceByPrice
		}
	}
	if err := e.saveUserModels(list); err != nil {
		t.Fatal(err)
	}
	res := e.CheckPrices(context.Background(), "OpenAI", true)
	if res.Changed != 1 {
		t.Fatalf("изменение цены от прайса не замечено: %s", res.Line)
	}
	n := rig.card(t)
	if err := e.NotifyDecide(context.Background(), n.ID, actionIndex(t, n, "new"), "Вадим"); err != nil {
		t.Fatal(err)
	}
	if got := e.PriceOf("gpt-6-luna"); got != "$0.2 / $0.9 / 1M" {
		t.Errorf("«только новые» поменяла изменившуюся цену: %q", got)
	}
	if got := e.PriceOf("gpt-5.4"); got == "" {
		t.Error("новая цена не встала")
	}
}

// Модель выдумала число, которого нет на странице, — строка отброшена и
// посчитана, в предложение она не попадает. Так же отброшено число из
// соседней строки и модель, которой на странице нет.
func TestPriceInventedNumberDropped(t *testing.T) {
	answer := `[
 {"page_name":"gpt-6-luna","input":0.1,"cached":0.01,"output":0.55},
 {"page_name":"gpt-4o-mini","input":0.15,"cached":0.075,"output":4.5},
 {"page_name":"gpt-9","input":1,"output":2},
 {"page_name":"gpt-5.4","input":2.5,"cached":0.25,"output":15}
]`
	rig := newPriceRig(t, pricePageOpenAI, answer)
	res := rig.e.CheckPrices(context.Background(), "OpenAI", true)
	// верная строка gpt-5.4 достаётся и модели с датой в хвосте
	if !res.OK || res.New != 2 {
		t.Fatalf("итог: %+v", res)
	}
	if !strings.Contains(res.Line, "не подтверждено страницей 3") {
		t.Errorf("отброшенное не посчитано: %s", res.Line)
	}
	prop, _ := rig.e.LoadPriceProposal("OpenAI")
	for _, bad := range []string{"gpt-6-luna", "gpt-4o-mini", "gpt-9"} {
		if _, ok := itemOf(prop, bad); ok {
			t.Errorf("%s попала в предложение, хотя число не подтверждено страницей", bad)
		}
	}
	if _, ok := itemOf(prop, "gpt-5.4"); !ok {
		t.Error("верная строка потерялась вместе с выдуманными")
	}
}

// Дообучение и запись в кэш: то, что на живой странице OpenAI проскочило
// проверку 30.09.2026. Цены дообученных моделей (те же имена с датой, другие
// цены) в разбор не пускаются — раздел «Finetuning» закрыт по заголовку, и
// модель с датой в имени получает цену базовой. Запись в кэш (столбец «Cache
// writes») выходом не становится: число в строке есть, но не в том столбце.
func TestPriceFineTuningAndCacheWriteRejected(t *testing.T) {
	// то, что ответила настоящая модель: выход взят из столбца записи в кэш,
	// а у моделей с датой — цены дообученных
	answer := `[
 {"page_name":"gpt-6-sol","input":2,"cached":0.2,"output":2.5},
 {"page_name":"gpt-4o-mini","input":0.15,"cached":0.075,"output":0.6},
 {"page_name":"gpt-4o-mini-2024-07-18","input":0.3,"cached":0.15,"output":1.2},
 {"page_name":"o4-mini","input":1.1,"cached":0.275,"output":4.4},
 {"page_name":"o4-mini-2025-04-16","input":4,"cached":1,"output":16},
 {"page_name":"gpt-4o-2024-08-06","input":3.75,"cached":1.875,"output":15}
]`
	rig := newPriceRigWith(t, pricePageOpenAI, answer,
		"gpt-6-sol", "gpt-4o-mini", "gpt-4o-mini-2024-07-18", "o4-mini", "o4-mini-2025-04-16", "gpt-4o-2024-08-06")
	res := rig.e.CheckPrices(context.Background(), "OpenAI", true)
	if !res.OK {
		t.Fatalf("проверка не удалась: %s", res.Line)
	}
	prop, _ := rig.e.LoadPriceProposal("OpenAI")
	if _, ok := itemOf(prop, "gpt-6-sol"); ok {
		t.Error("запись в кэш принята за выход: gpt-6-sol")
	}
	want := map[string]string{
		"gpt-4o-mini-2024-07-18": "$0.15 / $0.6 / $0.075 / 1M", // база, не дообученная
		"o4-mini-2025-04-16":     "$1.1 / $4.4 / $0.275 / 1M",
	}
	for model, price := range want {
		if it, ok := itemOf(prop, model); !ok || it.Price != price {
			t.Errorf("%s: %+v, ждали %s", model, it, price)
		}
	}
	// у gpt-4o-2024-08-06 базовой строки в ответе нет, а своя лежит только под
	// «Finetuning»: без цены, а не с ценой дообученной
	if it, ok := itemOf(prop, "gpt-4o-2024-08-06"); ok {
		t.Errorf("цена дообученной модели предложена базовой: %+v", it)
	}
	if !strings.Contains(res.Line, "gpt-6-sol: в столбце «выход» стоит 10, а модель назвала 2.5") ||
		!strings.Contains(res.Line, "не подтверждено страницей 4") {
		t.Errorf("отброшенное не объяснено: %s", res.Line)
	}

	// верный выход — принимается
	right := strings.Replace(answer, `"input":2,"cached":0.2,"output":2.5`, `"input":2,"cached":0.2,"output":10`, 1)
	rig2 := newPriceRigWith(t, pricePageOpenAI, right, "gpt-6-sol")
	rig2.e.CheckPrices(context.Background(), "OpenAI", true)
	prop2, _ := rig2.e.LoadPriceProposal("OpenAI")
	if it, _ := itemOf(prop2, "gpt-6-sol"); it.Price != "$2 / $10 / $0.2 / 1M" {
		t.Errorf("gpt-6-sol: %+v", it)
	}
}

// Цена не разобралась — строка у провайдера, карточки нет.
func TestPriceNotParsedNoCard(t *testing.T) {
	// модель ответила не списком
	rig := newPriceRig(t, pricePageOpenAI, "цен не нашёл")
	res := rig.e.CheckPrices(context.Background(), "OpenAI", true)
	if res.OK || !strings.HasPrefix(res.Line, "цены не разобраны: ") {
		t.Fatalf("итог: %+v", res)
	}
	if _, ok := rig.st.OpenNotificationByKey("price:OpenAI"); ok {
		t.Error("карточка-пустышка при неразобранных ценах")
	}
	last, ok := rig.e.LastPriceCheck("OpenAI")
	if !ok || last.Line != res.Line {
		t.Errorf("строка у провайдера не сохранена: %+v", last)
	}

	// страница не открылась
	rig = newPriceRig(t, pricePageOpenAI, answerOpenAI)
	if err := rig.e.SetPriceSource("OpenAI", PricePage, "http://127.0.0.1:1/pricing"); err != nil {
		t.Fatal(err)
	}
	res = rig.e.CheckPrices(context.Background(), "OpenAI", true)
	if res.OK || !strings.Contains(res.Line, "страница не открылась") {
		t.Fatalf("итог: %+v", res)
	}
	if rig.model.calls != 0 {
		t.Error("модель позвали без страницы")
	}
	if _, ok := rig.st.OpenNotificationByKey("price:OpenAI"); ok {
		t.Error("карточка при закрытой странице")
	}

	// пустая оболочка: цены собираются скриптом
	rig = newPriceRig(t, `<html><body><div id="app"></div><script>render()</script></body></html>`, answerOpenAI)
	res = rig.e.CheckPrices(context.Background(), "OpenAI", true)
	if res.OK || !strings.Contains(res.Line, "скриптом") {
		t.Fatalf("итог: %+v", res)
	}
}

// Модель недоступна: у OpenAI честная строка, без гаданий по чужой вёрстке.
func TestPriceModelDownHonestLine(t *testing.T) {
	rig := newPriceRig(t, pricePageOpenAI, answerOpenAI)
	rig.model.err = errors.New("модель молчит")
	res := rig.e.CheckPrices(context.Background(), "OpenAI", true)
	if res.OK || !strings.Contains(res.Line, "не ответила") {
		t.Fatalf("итог: %+v", res)
	}
	if _, ok := rig.st.OpenNotificationByKey("price:OpenAI"); ok {
		t.Error("карточка без ответа модели")
	}
}

// pricePageDeepSeek — таблица DeepSeek по столбцам, как на живой странице:
// имена в шапке (у первого сноска), цены ниже — сперва вне пика, потом в пик.
const pricePageDeepSeek = `<html><body><h1>Models &amp; Pricing</h1>
<p>The prices listed below are in units of per 1M tokens. A token, the smallest unit of text that the model recognizes, can be a word, a number, or even a punctuation mark. We will bill based on the total number of input and output tokens by the model.</p>
<table>
<tr><td>MODEL</td><td>deepseek-flash<sup>(1)</sup></td><td>deepseek-v4-pro</td></tr>
<tr><td>BASE URL (OpenAI Format)</td><td colspan="2">https://api.deepseek.com</td></tr>
<tr><td>MODEL VERSION</td><td>DeepSeek-V4.1-Flash</td><td>DeepSeek-V4-Pro-0813</td></tr>
<tr><td>CONTEXT LENGTH</td><td colspan="2">1M</td></tr>
<tr><td rowspan="6">PRICING<sup>(2)</sup></td><td>1M INPUT TOKENS<br>(CACHE HIT)</td><td>OFF-PEAK</td><td>$0.003</td><td>$0.022</td></tr>
<tr><td>PEAK</td><td>$0.006</td><td>$0.044</td></tr>
<tr><td>1M INPUT TOKENS<br>(CACHE MISS)</td><td>OFF-PEAK</td><td>$0.15</td><td>$0.66</td></tr>
<tr><td>PEAK</td><td>$0.3</td><td>$1.32</td></tr>
<tr><td>1M OUTPUT TOKENS</td><td>OFF-PEAK</td><td>$0.6</td><td>$1.98</td></tr>
<tr><td>PEAK</td><td>$1.2</td><td>$3.96</td></tr>
<tr><td>Concurrency Limit<sup>(3)</sup></td><td>2500</td><td>500</td></tr>
</table>
<p>(2) Off-peak rates are half of the peak rates. Peak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC, Monday through Friday, excluding Chinese public holidays. All other hours are off-peak.</p>
</body></html>`

const answerDeepSeek = `[
 {"model":"deepseek-chat","page_name":"deepseek-flash","input":0.15,"cached":0.003,"output":0.6,
  "peak_input":0.3,"peak_cached":0.006,"peak_output":1.2,"peak_hours":"пн-пт 01:00-04:00,06:00-10:00 UTC"},
 {"model":"deepseek-reasoner","page_name":"deepseek-v4-pro","input":0.66,"cached":0.022,"output":1.98,
  "peak_input":1.32,"peak_cached":0.044,"peak_output":3.96,"peak_hours":"пн-пт 01:00-04:00,06:00-10:00 UTC"}
]`

func newDeepSeekRig(t *testing.T, answer string) *priceRig {
	t.Helper()
	rig := newPriceRig(t, pricePageDeepSeek, answer)
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(pricePageDeepSeek))
	}))
	if err := rig.e.SetPriceSource("DeepSeek", PricePage, srv.URL+"/pricing"); err != nil {
		t.Fatal(err)
	}
	// у DeepSeek в API «deepseek-chat», в прайсе «deepseek-flash»: связку ставит человек
	for _, m := range []ModelConf{
		{Name: "DeepseekFlash", Model: "deepseek-chat", PriceName: "deepseek-flash"},
		{Name: "DeepseekPro", Model: "deepseek-reasoner", PriceName: "deepseek-v4-pro"},
	} {
		m.Type, m.BaseURL = "openai", "https://api.deepseek.com/v1"
		if err := rig.e.AddModel(m); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

// Вёрстка по столбцам: имена в шапке, цены ниже. Пик и часы пика берутся
// тоже, но часы — только если каждое время стоит на странице.
func TestPriceColumnLayoutWithPeak(t *testing.T) {
	rig := newDeepSeekRig(t, answerDeepSeek)
	res := rig.e.CheckPrices(context.Background(), "DeepSeek", true)
	if !res.OK || res.New != 2 {
		t.Fatalf("итог: %+v", res)
	}
	prop, _ := rig.e.LoadPriceProposal("DeepSeek")
	it, ok := itemOf(prop, "DeepseekFlash")
	if !ok || it.Price != "$0.15 / $0.6 / $0.003 / 1M" || it.Peak != "$0.3 / $1.2 / $0.006 / 1M" ||
		it.Hours != "пн-пт 01:00-04:00,06:00-10:00 UTC" {
		t.Fatalf("flash: %+v", it)
	}
	n, _ := rig.st.OpenNotificationByKey("price:DeepSeek")
	if err := rig.e.NotifyDecide(context.Background(), n.ID, 0, "Вадим"); err != nil {
		t.Fatal(err)
	}
	c, _ := rig.e.modelConf("DeepseekFlash")
	if c.Price != "$0.15 / $0.6 / $0.003 / 1M" || c.PricePeak != "$0.3 / $1.2 / $0.006 / 1M" || c.Peak == "" {
		t.Errorf("пик не встал вместе с ценой: %+v", c)
	}
	// в пик расход считается по пиковой цене
	if !inPeak(c.Peak, time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC)) {
		t.Error("часы пика, принятые со страницы, свой же разбор не понимает")
	}
}

// Страница переименовала модель (deepseek-v4-flash → deepseek-flash), а связка
// «имя в прайсе» осталась старой: угадывать нельзя, но человеку сказано, как
// страница называет модели, — ему остаётся связать одно имя.
func TestPriceRenamedOnPageAsksToLink(t *testing.T) {
	rig := newDeepSeekRig(t, answerDeepSeek)
	if err := rig.e.SetPriceName("DeepseekFlash", "deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	if err := rig.e.SetPriceName("DeepseekPro", "deepseek-pro-старое"); err != nil {
		t.Fatal(err)
	}
	res := rig.e.CheckPrices(context.Background(), "DeepSeek", true)
	if res.OK || !strings.Contains(res.Line, "на странице без нашей модели: deepseek-flash, deepseek-v4-pro") ||
		!strings.Contains(res.Line, "имя в прайсе") {
		t.Fatalf("человеку не сказано, что связать: %+v", res)
	}
	if _, ok := rig.st.OpenNotificationByKey("price:DeepSeek"); ok {
		t.Error("карточка при непонятных именах")
	}
	if err := rig.e.SetPriceName("DeepseekFlash", "deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	res = rig.e.CheckPrices(context.Background(), "DeepSeek", true)
	if !res.OK || res.New != 1 {
		t.Fatalf("после связки: %+v", res)
	}
}

// Цены соседнего столбца: числа на странице есть, но стоят не под этим именем.
// Проверка по тексту этого не видит, ловит независимый разбор таблицы.
func TestPriceColumnSwapCaught(t *testing.T) {
	swapped := `[
 {"page_name":"deepseek-flash","input":0.66,"cached":0.022,"output":1.98},
 {"page_name":"deepseek-v4-pro","input":0.66,"cached":0.022,"output":1.98}
]`
	rig := newDeepSeekRig(t, swapped)
	res := rig.e.CheckPrices(context.Background(), "DeepSeek", true)
	prop, _ := rig.e.LoadPriceProposal("DeepSeek")
	if _, ok := itemOf(prop, "DeepseekFlash"); ok {
		t.Fatalf("цены pro приняты за цены flash: %s", res.Line)
	}
	if _, ok := itemOf(prop, "DeepseekPro"); !ok {
		t.Errorf("верная строка pro пропала: %s", res.Line)
	}
	if !strings.Contains(res.Line, "расходится с независимым разбором") {
		t.Errorf("отброшенное не объяснено: %s", res.Line)
	}
}

// Модель недоступна — у DeepSeek запасной разбор регулярками: та же таблица,
// те же цены, помечено «без модели». Сноска у имени не должна сбивать его:
// раньше на ней разбор терял вторую модель и раздавал цены пары одной.
func TestPriceDeepSeekFallbackWhenModelDown(t *testing.T) {
	rig := newDeepSeekRig(t, "")
	rig.model.err = errors.New("модель молчит")
	res := rig.e.CheckPrices(context.Background(), "DeepSeek", true)
	if !res.OK || res.New != 2 {
		t.Fatalf("запасной разбор не сработал: %+v", res)
	}
	prop, _ := rig.e.LoadPriceProposal("DeepSeek")
	it, _ := itemOf(prop, "DeepseekPro")
	if it.Price != "$0.66 / $1.98 / $0.022 / 1M" || it.Peak != "$1.32 / $3.96 / $0.044 / 1M" {
		t.Errorf("pro: %+v", it)
	}
	if !strings.Contains(it.Note, "без модели") {
		t.Errorf("не сказано, что разбор запасной: %q", it.Note)
	}
}

// Запасной разбор берёт только то, что сходится по счёту: раздел, где чисел
// больше или меньше, — другая вёрстка, и цены не раздаются.
func TestFallbackRejectsWrongCount(t *testing.T) {
	broken := strings.Replace(pricePageDeepSeek, "<td>PEAK</td><td>$1.2</td><td>$3.96</td>", "<td>PEAK</td><td>$1.2</td>", 1)
	if got := priceFromCells(cells(broken)); len(got) != 0 {
		t.Fatalf("раздел с недостающим числом разобрался: %v", got)
	}
	if got := priceFromCells(cells(pricePageDeepSeek)); len(got) != 2 {
		t.Fatalf("исправная страница не разобралась: %v", got)
	}
}

// OpenRouter: цены за токен переводятся в цену за миллион, бесплатные и
// «минус один» не предлагаются.
func TestPriceAPIOpenRouter(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
 {"id":"openai/gpt-5.6-luna","pricing":{"prompt":"0.0000001","completion":"0.0000005","input_cache_read":"0.00000001",
   "overrides":[{"min_prompt_tokens":272000,"prompt":"0.0000002","completion":"0.00000075"}]}},
 {"id":"vendor/cheap","pricing":{"prompt":"0.00000015","completion":"0.0000006"}},
 {"id":"vendor/free:free","pricing":{"prompt":"0","completion":"0"}},
 {"id":"openrouter/auto","pricing":{"prompt":"-1","completion":"-1"}}]}`))
	}))
	if err := e.SetPriceSource("OpenRouter", PriceAPI, srv.URL+"/api/v1/models"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"openai/gpt-5.6-luna", "vendor/cheap", "vendor/free:free", "openrouter/auto", "vendor/unknown"} {
		if err := e.AddModel(ModelConf{Name: shortName(id), Type: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: id}); err != nil {
			t.Fatal(err)
		}
	}
	res := e.CheckPrices(context.Background(), "OpenRouter", true)
	if !res.OK || res.New != 2 {
		t.Fatalf("итог: %+v", res)
	}
	prop, _ := e.LoadPriceProposal("OpenRouter")
	if it, _ := itemOf(prop, "gpt-5.6-luna"); it.Price != "$0.1 / $0.5 / $0.01 / 1M" {
		t.Errorf("цена за токен не переведена в цену за миллион: %+v", it)
	}
	if it, _ := itemOf(prop, "gpt-5.6-luna"); !strings.Contains(it.Note, "от 272K токенов: вход $0.2, выход $0.75") {
		t.Errorf("цена длинного контекста не названа припиской: %q", it.Note)
	}
	if it, _ := itemOf(prop, "cheap"); it.Price != "$0.15 / $0.6 / 1M" {
		t.Errorf("cheap: %+v", it)
	}
	for _, skipped := range []string{"free:free", "auto", "unknown"} {
		if _, ok := itemOf(prop, skipped); ok {
			t.Errorf("%s попала в предложение", skipped)
		}
	}
	if !strings.Contains(res.Line, "бесплатных 1") {
		t.Errorf("бесплатная не посчитана: %s", res.Line)
	}
}

func shortName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// Perplexity сам называет цену в ответе: сверять нечего, карточки нет.
func TestPricePerplexityReportedSkipped(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "sonar", Type: "perplexity", BaseURL: "https://api.perplexity.ai", Model: "sonar"}); err != nil {
		t.Fatal(err)
	}
	res := e.CheckPrices(context.Background(), "Perplexity", true)
	if !res.OK || !strings.Contains(res.Line, "называет сам") {
		t.Fatalf("итог: %+v", res)
	}
	if n, ok := st.OpenNotificationByKey("price:Perplexity"); ok {
		t.Errorf("карточка там, где сверять нечего: %+v", n)
	}
	if got := e.pricesPass(context.Background()); !strings.Contains(got, "нет") {
		t.Errorf("суточная задача полезла к провайдеру без источника: %q", got)
	}
}

// Суточная задача не возвращает отложенное «Не сейчас» тем же самым каждое
// утро; кнопка человека показывает карточку всегда.
func TestPriceDailyPassDoesNotRepeatDismissed(t *testing.T) {
	rig := newPriceRig(t, pricePageOpenAI, answerOpenAI)
	e := rig.e
	e.CheckPrices(context.Background(), "OpenAI", false)
	n := rig.card(t)
	dismiss := -1
	for i, a := range n.Actions {
		if a.Kind == "dismiss" {
			dismiss = i
		}
	}
	if err := e.NotifyDecide(context.Background(), n.ID, dismiss, "Вадим"); err != nil {
		t.Fatal(err)
	}
	e.CheckPrices(context.Background(), "OpenAI", false)
	if _, ok := rig.st.OpenNotificationByKey("price:OpenAI"); ok {
		t.Error("суточная проверка вернула отложенную карточку тем же самым")
	}
	e.CheckPrices(context.Background(), "OpenAI", true)
	if _, ok := rig.st.OpenNotificationByKey("price:OpenAI"); !ok {
		t.Error("кнопка «Проверить цены» не показала карточку")
	}
}

// Цены совпали — карточки нет, а прежняя, если висит, закрывается.
func TestPriceSameClosesOldCard(t *testing.T) {
	rig := newPriceRig(t, pricePageOpenAI, answerOpenAI)
	e := rig.e
	e.CheckPrices(context.Background(), "OpenAI", true)
	n := rig.card(t)
	if err := e.NotifyDecide(context.Background(), n.ID, actionIndex(t, n, "all"), "Вадим"); err != nil {
		t.Fatal(err)
	}
	// всё встало — вторая проверка ничего не предлагает
	res := e.CheckPrices(context.Background(), "OpenAI", true)
	if res.New+res.Changed+res.Mine != 0 || !strings.Contains(res.Line, "совпадают") {
		t.Fatalf("итог: %+v", res)
	}
	if _, ok := rig.st.OpenNotificationByKey("price:OpenAI"); ok {
		t.Error("карточка при совпавших ценах")
	}
}

// Пока человек решал, он мог поставить цену сам: приём эту строку
// пропускает, а не затирает.
func TestPriceAcceptSkipsPriceChangedSince(t *testing.T) {
	rig := newPriceRig(t, pricePageOpenAI, answerOpenAI)
	e := rig.e
	e.CheckPrices(context.Background(), "OpenAI", true)
	if err := e.SetModelPrice("gpt-5.4", "$9 / $9 / 1M"); err != nil {
		t.Fatal(err)
	}
	applied, skipped, err := e.AcceptPrices("OpenAI", "all")
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || applied != 4 {
		t.Fatalf("принято %d, пропущено %d", applied, skipped)
	}
	if got := e.PriceOf("gpt-5.4"); got != "$9 / $9 / 1M" {
		t.Errorf("цену, поставленную во время решения, затёрли: %q", got)
	}
	if _, _, err := e.AcceptPrices("OpenAI", "all"); err == nil {
		t.Error("предложение принято дважды")
	}
}

// Суточная задача обходит провайдеров с источником и подключёнными моделями
// и пишет по строке на каждого; там, где проверку уже ведёт кнопка, не
// звонит модели второй раз.
func TestPricePassWalksProviders(t *testing.T) {
	rig := newPriceRig(t, pricePageOpenAI, answerOpenAI)
	got := rig.e.pricesPass(context.Background())
	if !strings.HasPrefix(got, "OpenAI: новых 5") || strings.Contains(got, "DeepSeek") {
		t.Fatalf("итог задачи: %q", got)
	}
	if rig.model.calls != 1 {
		t.Errorf("модель звали %d раз", rig.model.calls)
	}
	if !rig.e.prices.start("OpenAI") {
		t.Fatal("проверка не освободилась после задачи")
	}
	defer rig.e.prices.done("OpenAI")
	if got := rig.e.pricesPass(context.Background()); !strings.Contains(got, "OpenAI: уже проверяется") || rig.model.calls != 1 {
		t.Errorf("задача полезла в проверку, что уже идёт: %q (вызовов %d)", got, rig.model.calls)
	}
}

// Проверка идёт в своей нитке и не запускается вторично.
func TestPriceStartCheckOnce(t *testing.T) {
	rig := newPriceRig(t, pricePageOpenAI, answerOpenAI)
	if !rig.e.prices.start("OpenAI") {
		t.Fatal("первая проверка не началась")
	}
	if rig.e.StartPriceCheck("OpenAI") {
		t.Error("вторая проверка того же провайдера пошла параллельно")
	}
	if !rig.e.PriceChecking("OpenAI") {
		t.Error("идущая проверка не видна")
	}
	rig.e.prices.done("OpenAI")
	if rig.e.PriceChecking("OpenAI") {
		t.Error("законченная проверка всё ещё идёт")
	}
}

// Страница цен, что была одной настройкой на всех, переезжает к DeepSeek.
func TestPricePageMovesToDeepSeek(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting("prices.page", "https://example.org/my-deepseek-prices"); err != nil {
		t.Fatal(err)
	}
	st.DeleteSettingKey("prices.page_moved")
	e.migratePricePage()
	p, _ := e.Provider("DeepSeek")
	if p.PriceKind != PricePage || p.PriceURL != "https://example.org/my-deepseek-prices" {
		t.Fatalf("адрес не переехал: %+v", p)
	}
	if st.Setting("prices.page", "") != "" {
		t.Error("старая настройка осталась")
	}
	// второй раз ничего не меняет
	if err := e.SetPriceSource("DeepSeek", PricePage, "https://example.org/other"); err != nil {
		t.Fatal(err)
	}
	e.migratePricePage()
	if p, _ := e.Provider("DeepSeek"); p.PriceURL != "https://example.org/other" {
		t.Errorf("миграция сработала дважды: %+v", p)
	}
}

// Справочник, сохранённый до источников цен, догоняет пресеты: DeepSeek получает
// страницу без настройки, а карточка прежней проверки закрывается.
func TestPricePageMigrationWithoutSetting(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	list := e.Providers()
	for i := range list {
		list[i].PriceKind, list[i].PriceURL = "", ""
	}
	if err := e.SaveProviders(list); err != nil {
		t.Fatal(err)
	}
	st.DeleteSettingKey("prices.page_moved")
	if _, err := e.Notify(&store.Notification{Key: "price:связка", Title: "Цены разобраны, но модели не опознаны"}); err != nil {
		t.Fatal(err)
	}
	e.migratePricePage()
	if p, _ := e.Provider("DeepSeek"); p.PriceKind != PricePage || p.PriceURL != "https://api-docs.deepseek.com/quick_start/pricing/" {
		t.Errorf("DeepSeek без настройки не получил страницу: %+v", p)
	}
	if _, ok := st.OpenNotificationByKey("price:связка"); ok {
		t.Error("карточка прежней проверки осталась открытой")
	}
}

// Источники у провайдеров засеяны пресетами; правка человека переживает
// обновление пресетов.
func TestProviderPriceSourcesSeeded(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	want := map[string][2]string{
		"OpenAI":        {PricePage, "https://developers.openai.com/api/docs/pricing"},
		"DeepSeek":      {PricePage, "https://api-docs.deepseek.com/quick_start/pricing/"},
		"Anthropic":     {PricePage, "https://platform.claude.com/docs/en/about-claude/pricing"},
		"Google Gemini": {PricePage, "https://ai.google.dev/gemini-api/docs/pricing"},
		"OpenRouter":    {PriceAPI, "https://openrouter.ai/api/v1/models"},
		"Perplexity":    {PriceReported, ""},
	}
	for name, w := range want {
		p, ok := e.Provider(name)
		if !ok || p.PriceKind != w[0] || p.PriceURL != w[1] {
			t.Errorf("%s: источник %q %q, ждали %q %q", name, p.PriceKind, p.PriceURL, w[0], w[1])
		}
	}
	for _, name := range []string{"LM Studio (в своей сети)", "Ollama (в своей сети)", "Свой адрес"} {
		if p, _ := e.Provider(name); p.PriceKind != "" && p.PriceKind != PriceNone {
			t.Errorf("%s: у своих моделей цен на странице нет, а источник %q", name, p.PriceKind)
		}
	}
	// человек поправил адрес: обновление пресетов, что досыпает новое, его
	// правку «засеянной» не записывает — иначе следующее обновление её бы затёрло
	if err := e.SetPriceSource("OpenAI", PricePage, "https://example.org/openai"); err != nil {
		t.Fatal(err)
	}
	var kept []ProviderConf
	for _, p := range e.Providers() {
		if p.Name != "Groq" {
			kept = append(kept, p)
		}
	}
	if err := e.SaveProviders(kept); err != nil {
		t.Fatal(err)
	}
	_ = e.store.SetSetting(providersSeedKey, "0")
	e.provOK = false // новая метка досыпки — как после перезапуска с новой сборкой
	if _, ok := e.Provider("Groq"); !ok {
		t.Fatal("досыпка не вернула пресет")
	}
	if p, _ := e.Provider("OpenAI"); p.PriceURL != "https://example.org/openai" {
		t.Errorf("правка человека затёрта: %q", p.PriceURL)
	}
	if mine := e.seededProviders()["OpenAI"]; mine.PriceURL != "https://developers.openai.com/api/docs/pricing" {
		t.Errorf("правка человека записана как засеянное: %q", mine.PriceURL)
	}
}

// В строке модели видно, кто назвал цену: пачка «Принять все» меняет только
// пришедшие от прайса.
func TestModelFactsSayWhoNamedThePrice(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	find := func(c ModelConf) string {
		_, facts := modelFacts(c, true)
		for _, f := range facts {
			if f.Label == "Цена взята" {
				return f.Value
			}
		}
		return ""
	}
	if got := find(ModelConf{Price: "$1 / $2 / 1M", PriceBy: PriceByPrice, PriceAt: at}); !strings.HasPrefix(got, "с прайса провайдера · ") {
		t.Errorf("цена от прайса: %q", got)
	}
	if got := find(ModelConf{Price: "$1 / $2 / 1M", PriceBy: priceByHuman}); got != "вписана вами" {
		t.Errorf("цена вписана человеком: %q", got)
	}
	if got := find(ModelConf{Price: "$1 / $2 / 1M"}); got != "" {
		t.Errorf("цена без отметки выдана за чью-то: %q", got)
	}
	if got := find(ModelConf{PriceBy: PriceByPrice}); got != "" {
		t.Errorf("отметка без цены: %q", got)
	}
}

// Адрес с ценами может потребовать ключ: токен источника уходит заголовком
// Authorization: Bearer и на страницу, и на JSON; без токена заголовка нет, а
// ключ провайдера для запросов к моделям туда не подставляется.
func TestPriceTokenSentToSource(t *testing.T) {
	var seen []string
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"data":[{"id":"vendor/m","pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`))
			return
		}
		_, _ = w.Write([]byte(pricePageDeepSeek))
	}))
	model := &priceModel{answer: answerDeepSeek}
	e, _ := newTestEngine(t, "цены-проба", model)
	roles := e.Roles()
	for i := range roles {
		if roles[i].Name == RoleModules {
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "цены-проба", roleNoneName, 30
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	if err := e.SetProviderKey("OpenRouter", "ключ-запросов"); err != nil {
		t.Fatal(err)
	}
	if err := e.AddModel(ModelConf{Name: "m", Type: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "vendor/m"}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetPriceSource("OpenRouter", PriceAPI, srv.URL+"/json"); err != nil {
		t.Fatal(err)
	}
	e.CheckPrices(context.Background(), "OpenRouter", true)
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("без токена заголовок ушёл: %q", seen)
	}
	if err := e.SetPriceToken("OpenRouter", "токен-цен"); err != nil {
		t.Fatal(err)
	}
	seen = nil
	e.CheckPrices(context.Background(), "OpenRouter", true)
	if len(seen) != 1 || seen[0] != "Bearer токен-цен" {
		t.Fatalf("токен источника не ушёл заголовком: %q", seen)
	}
	if e.PriceToken("OpenRouter") != "токен-цен" || e.ProviderKey("OpenRouter") != "ключ-запросов" {
		t.Error("токен источника и ключ провайдера перепутаны")
	}
	// страница — тем же способом
	if err := e.AddModel(ModelConf{Name: "flash", Type: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", PriceName: "deepseek-flash"}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetPriceSource("DeepSeek", PricePage, srv.URL+"/pricing"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetPriceToken("DeepSeek", "страница-токен"); err != nil {
		t.Fatal(err)
	}
	seen = nil
	e.CheckPrices(context.Background(), "DeepSeek", true)
	if len(seen) != 1 || seen[0] != "Bearer страница-токен" {
		t.Fatalf("токен не ушёл на страницу: %q", seen)
	}
	// пустое значение убирает токен
	if err := e.SetPriceToken("DeepSeek", ""); err != nil || e.PriceToken("DeepSeek") != "" {
		t.Errorf("токен не убран: %v", err)
	}
}

// Слова закрытых разделов правятся, а пустой список не открывает дообучение.
func TestPriceSkipSectionsSetting(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if got := e.PriceSkipSections(); got != priceSkipDefault {
		t.Fatalf("по умолчанию: %q", got)
	}
	if err := e.SetPriceSkipSections("batch, extra"); err != nil {
		t.Fatal(err)
	}
	if got := e.priceSkipList(); len(got) != 2 || got[1] != "extra" {
		t.Errorf("список: %v", got)
	}
	if err := e.SetPriceSkipSections("  "); err != nil || e.PriceSkipSections() != priceSkipDefault {
		t.Errorf("пустое значение не вернуло умолчание: %q", e.PriceSkipSections())
	}
}
