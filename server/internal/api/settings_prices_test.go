package api

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Провайдеры и цены»: у провайдера — откуда его цены и остаток, когда
// проверяли и что вышло, кнопка «Проверить цены». Сеть в этих тестах не
// нужна: адрес источника ведёт на закрытый порт, и проверка честно
// говорит, что страница не открылась.

// untilTrue ждёт условия, а не срока: проверка цен идёт в своей нитке.
func untilTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func openAIRig(t *testing.T) *Server {
	t.Helper()
	s, _ := testServer(t)
	for _, name := range []string{"gpt-a", "gpt-b"} {
		if err := s.eng.AddModel(engine.ModelConf{Name: name, Type: "openai", BaseURL: "https://api.openai.com/v1", Model: name}); err != nil {
			t.Fatal(err)
		}
	}
	// страница цен — на закрытый порт, чтобы тест не ходил в интернет
	if _, err := s.execute(actionRequest{Action: "save_price_source", Target: "OpenAI",
		Params: map[string]string{"kind": kindTitle(priceKindTitles, engine.PricePage), "url": "http://127.0.0.1:1/pricing"}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProvidersTabShowsSourcesAndCheckButton(t *testing.T) {
	s := openAIRig(t)
	// вкладка «Провайдеры»: у провайдера с моделями — своя строка, ведущая на
	// его экран; без моделей и без ключа провайдеров в списке нет
	list, err := s.buildScreen("settings:models~providers", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	line := findRow(list, "provider-OpenAI")
	if line == nil || len(line.Actions) == 0 || line.Actions[0].Action != "$goto" || line.Actions[0].Target != providerAddr("OpenAI", "") {
		t.Fatalf("строка провайдера не ведёт на его экран: %+v", line)
	}
	if !strings.Contains(line.Caption, "цены со страницы") || !strings.Contains(line.Caption, "остаток") {
		t.Errorf("строка не говорит ни про цены, ни про остаток: %q", line.Caption)
	}
	var withoutPrice bool
	for _, b := range line.Badges {
		withoutPrice = withoutPrice || strings.Contains(b.Text, "2 без цены")
	}
	if !withoutPrice {
		t.Errorf("в строке нет капсулы про модели без цены: %+v", line.Badges)
	}
	if findRow(list, "provider-Anthropic") != nil {
		t.Error("в списке провайдер без моделей и без ключа")
	}

	// экран провайдера, вкладка «Цены»
	doc, err := s.buildScreen(providerAddr("OpenAI", providerTabPrices), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(doc, "pricerow-OpenAI")
	if row == nil || len(row.Actions) != 1 || row.Actions[0].Action != "check_prices" || row.Actions[0].Target != "OpenAI" || row.Actions[0].Style != "primary" {
		t.Fatalf("строка цен без кнопки «Проверить цены»: %+v", row)
	}
	facts := map[string]string{}
	for _, f := range row.Facts {
		facts[f.Label] = f.Value
	}
	if facts["Моделей без цены"] != "2 из 2" {
		t.Errorf("факты строки цен: %v", facts)
	}
	form := findRow(doc, "psrc-OpenAI")
	if form == nil {
		t.Fatal("нет формы источника цен")
	}
	for _, f := range form.Fields {
		switch f.Name {
		case "kind":
			if f.Type != "segments" || f.Value != "страница" {
				t.Errorf("источник цен: %+v", f)
			}
		case "url":
			if f.Value != "http://127.0.0.1:1/pricing" {
				t.Errorf("адрес страницы: %q", f.Value)
			}
		}
	}
	// вкладка «Остаток»
	balance, err := s.buildScreen(providerAddr("OpenAI", providerTabBalance), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if findRow(balance, "bal-OpenAI") == nil {
		t.Error("нет формы остатка")
	}
	// вкладки цен и остатка есть у провайдера, где платят за вызовы
	var tabs []string
	for _, tab := range doc.Subtabs {
		tabs = append(tabs, tab.Title)
	}
	if strings.Join(tabs, "|") != "Ключ и модели|Цены|Остаток" {
		t.Errorf("вкладки экрана провайдера: %v", tabs)
	}
	dump := dumpScreen(doc) + dumpScreen(balance)
	for _, want := range []string{"Проверить цены", "Источник цен", "Откуда остаток"} {
		if !strings.Contains(dump, want) {
			t.Errorf("на экране нет %q", want)
		}
	}
}

// Кнопка запускает проверку, итог — строкой у провайдера; страница не
// открылась — карточки нет.
func TestCheckPricesButtonReportsResult(t *testing.T) {
	s := openAIRig(t)
	if _, err := s.execute(actionRequest{Action: "check_prices", Target: "OpenAI"}); err != nil {
		t.Fatal(err)
	}
	untilTrue(t, "итог проверки цен", func() bool {
		last, ok := s.eng.LastPriceCheck("OpenAI")
		return ok && !s.eng.PriceChecking("OpenAI") && last.Line != ""
	})
	last, _ := s.eng.LastPriceCheck("OpenAI")
	if last.OK || !strings.HasPrefix(last.Line, "цены не разобраны: ") || !strings.Contains(last.Line, "страница не открылась") {
		t.Fatalf("итог: %+v", last)
	}
	if _, ok := s.eng.Store().OpenNotificationByKey("price:OpenAI"); ok {
		t.Error("карточка-пустышка при неоткрывшейся странице")
	}
	doc, _ := s.buildScreen(providerAddr("OpenAI", providerTabPrices), "", "", 0)
	row := findRow(doc, "pricerow-OpenAI")
	if row == nil || !strings.Contains(row.Caption, "цены не разобраны") || row.Level != LevelWarn {
		t.Errorf("строка у провайдера не сказала про беду: %+v", row)
	}
	var checked bool
	for _, f := range row.Facts {
		checked = checked || f.Label == "Проверено"
	}
	if !checked {
		t.Error("у строки нет времени проверки")
	}
}

func TestSavePriceSourceValidates(t *testing.T) {
	s, _ := testServer(t)
	for name, params := range map[string]map[string]string{
		"вид не из списка":    {"kind": "как-нибудь", "url": "https://example.org"},
		"страница без адреса": {"kind": kindTitle(priceKindTitles, engine.PricePage), "url": ""},
		"адрес без схемы":     {"kind": kindTitle(priceKindTitles, engine.PriceAPI), "url": "example.org/p"},
	} {
		if _, err := s.execute(actionRequest{Action: "save_price_source", Target: "OpenAI", Params: params}); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
	// «называет сам» и «не брать» адреса не требуют
	if _, err := s.execute(actionRequest{Action: "save_price_source", Target: "OpenAI",
		Params: map[string]string{"kind": kindTitle(priceKindTitles, engine.PriceNone)}}); err != nil {
		t.Errorf("«не брать цены»: %v", err)
	}
	if p, _ := s.eng.Provider("OpenAI"); p.PriceKind != engine.PriceNone {
		t.Errorf("источник не сохранён: %+v", p)
	}
}

// «Подключить провайдера»: пачка новых моделей без цен получает проверку
// сразу, а каталог не подставляет цену входа как цену и входа, и выхода.
func TestConnectProviderStartsPriceCheck(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.AddProvider(engine.ProviderConf{Name: "Проба", Kind: "openai", BaseURL: "http://127.0.0.1:1/v1",
		Catalog: true, PriceKind: engine.PricePage, PriceURL: "http://127.0.0.1:1/prices"}); err != nil {
		t.Fatal(err)
	}
	if err := s.eng.AddProvider(engine.ProviderConf{Name: "Без источника", Kind: "openai", BaseURL: "http://127.0.0.1:2/v1", Catalog: true}); err != nil {
		t.Fatal(err)
	}
	models := []engine.CatalogModel{{ID: "vendor/m1", Price: "$1.00 / 1M"}}

	added, _, err := s.connectAllCounted(catalogState{Provider: "Проба", Models: models})
	if err != nil || added != 1 {
		t.Fatalf("подключено %d, ошибка %v", added, err)
	}
	if got := s.eng.PriceOf("m1"); got != "" {
		t.Errorf("каталог подставил цену входа как цену модели: %q", got)
	}
	untilTrue(t, "проверка цен после подключения", func() bool {
		_, ok := s.eng.LastPriceCheck("Проба")
		return ok && !s.eng.PriceChecking("Проба")
	})
	s.mu.Lock()
	line := s.lastCheck
	s.mu.Unlock()
	if !strings.Contains(line, "цены проверяются") {
		t.Errorf("человеку не сказано, что цены проверяются: %q", line)
	}

	// у провайдера без источника цены остаются такими, как их отдал каталог, — и помечены прайсом
	if _, _, err := s.connectAllCounted(catalogState{Provider: "Без источника", Models: models}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.eng.LastPriceCheck("Без источника"); ok {
		t.Error("проверка пошла у провайдера без источника цен")
	}
}

// Остаток: форма источника сохраняет порог и «было», Здоровье показывает
// оценку с пометкой «оценка», а не голым числом.
func TestBalanceFormAndHealthRow(t *testing.T) {
	s := openAIRig(t)
	if _, err := s.execute(actionRequest{Action: "save_balance_source", Target: "OpenAI",
		Params: map[string]string{"kind": kindTitle(balanceKindTitles, engine.BalanceEstimate), "low": "$5",
			"amount": "$10", "date": time.Now().Format("2006-01-02")}}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.eng.Provider("OpenAI")
	if p.BalanceKind != engine.BalanceEstimate || p.BalanceLow != 5 {
		t.Fatalf("источник остатка: %+v", p)
	}
	if base, ok := s.eng.BalanceBaseOf("OpenAI"); !ok || base.Amount != 10 || base.Currency != "$" {
		t.Fatalf("«было» не сохранено: %+v", base)
	}
	var found bool
	for _, row := range s.balanceRows() {
		if strings.HasPrefix(row.Subtitle, "OpenAI") {
			found = true
			if !strings.Contains(row.Subtitle, "оценка") || !strings.Contains(row.Caption, "≈ $10.00") || !strings.Contains(row.Caption, "траты вне Сёмы не видны") {
				t.Errorf("строка на «Здоровье»: %q · %q", row.Subtitle, row.Caption)
			}
		}
	}
	if !found {
		t.Error("оценка остатка на «Здоровье» не показана")
	}
	// ошибки ввода — вслух
	for name, params := range map[string]map[string]string{
		"порог не число": {"kind": kindTitle(balanceKindTitles, engine.BalanceEstimate), "low": "мало"},
		"нет вида":       {"kind": "", "low": "5"},
		"сумма словами":  {"kind": kindTitle(balanceKindTitles, engine.BalanceEstimate), "amount": "много"},
	} {
		if _, err := s.execute(actionRequest{Action: "save_balance_source", Target: "OpenAI", Params: params}); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
	// адреса остатка у OpenAI в документации нет — спрашивать его нельзя
	if _, err := s.execute(actionRequest{Action: "save_balance_source", Target: "OpenAI",
		Params: map[string]string{"kind": kindTitle(balanceKindTitles, engine.BalanceAPI)}}); err == nil {
		t.Error("у OpenAI разрешили спрашивать остаток по несуществующему адресу")
	}
}

// У OpenRouter — своя строка: ключ управления, отдельный от ключа запросов.
func TestOpenRouterManagementKeyRow(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.AddModel(engine.ModelConf{Name: "or", Type: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "a/b"}); err != nil {
		t.Fatal(err)
	}
	doc, _ := s.buildScreen(providerAddr("OpenRouter", providerTabBalance), "", "", 0)
	row := findRow(doc, "mgmt-OpenRouter")
	if row == nil || row.Level != LevelWarn || row.Actions[0].Action != "set_management_key" || !row.Actions[0].Secure {
		t.Fatalf("строки ключа управления нет или она без закрытого ввода: %+v", row)
	}
	if _, err := s.execute(actionRequest{Action: "set_management_key", Target: "OpenRouter", Params: map[string]string{"value": "sk-or-mgmt"}}); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.buildScreen(providerAddr("OpenRouter", providerTabBalance), "", "", 0)
	if row := findRow(doc, "mgmt-OpenRouter"); row == nil || row.Level != LevelOK {
		t.Errorf("ключ задан, а строка не позеленела: %+v", row)
	}
	// значение секрета в описание экрана не попадает никогда
	if strings.Contains(dumpScreen(doc), "sk-or-mgmt") {
		t.Error("ключ управления попал в описание экрана")
	}
}

// Замечание «нет цены»: с вызовами оно называет дыру в записанном счёте и
// ведёт прямо к проверке цен.
func TestFindingNoPriceWithSpendLeadsToCheck(t *testing.T) {
	s := openAIRig(t)
	if err := s.eng.Store().AddUsage(&store.UsageRow{Consumer: "core:test", Kind: "llm", Model: "gpt-a", OK: true,
		TokensIn: 1000, TokensOut: 100}); err != nil {
		t.Fatal(err)
	}
	var finding *Component
	for _, f := range s.modelFindings(s.eng.Models(), s.eng.Roles()) {
		if strings.Contains(f.Subtitle, "Расход не считается") {
			f := f
			finding = &f
		}
	}
	if finding == nil {
		t.Fatal("замечания «расход не считается: нет цены» нет")
	}
	if !strings.Contains(finding.Caption, "gpt-a") {
		t.Errorf("не названа модель, у которой были вызовы: %q", finding.Caption)
	}
	if len(finding.Actions) != 1 || finding.Actions[0].Label != "Проверить цены" || finding.Actions[0].Style != "primary" ||
		finding.Actions[0].Target != providerAddr("OpenAI", providerTabPrices) {
		t.Errorf("замечание не ведёт к проверке цен: %+v", finding.Actions)
	}
}

// В настройках провайдера — адрес с ценами и, если адрес закрыт, токен. Токен
// вводится закрытым полем и в описание экрана не попадает никогда.
func TestPriceSourceAddressAndTokenInProviderSettings(t *testing.T) {
	s := openAIRig(t)
	doc, _ := s.buildScreen(providerAddr("OpenAI", providerTabPrices), "", "", 0)
	form := findRow(doc, "psrc-OpenAI")
	if form == nil {
		t.Fatal("формы источника цен нет")
	}
	var url string
	for _, f := range form.Fields {
		if f.Name == "url" {
			url = f.Value
			if !strings.Contains(f.Label, "Адрес") {
				t.Errorf("поле адреса без подписи: %q", f.Label)
			}
		}
	}
	if url != "http://127.0.0.1:1/pricing" {
		t.Errorf("адрес с ценами не виден в настройках провайдера: %q", url)
	}
	row := findRow(doc, "pricetoken-OpenAI")
	if row == nil || len(row.Actions) != 1 || row.Actions[0].Action != "set_price_token" || !row.Actions[0].Secure {
		t.Fatalf("нет строки токена источника цен с закрытым вводом: %+v", row)
	}
	if _, err := s.execute(actionRequest{Action: "set_price_token", Target: "OpenAI", Params: map[string]string{"value": "sk-цены-секрет"}}); err != nil {
		t.Fatal(err)
	}
	if s.eng.PriceToken("OpenAI") != "sk-цены-секрет" {
		t.Fatal("токен не сохранён")
	}
	doc, _ = s.buildScreen(providerAddr("OpenAI", providerTabPrices), "", "", 0)
	if row := findRow(doc, "pricetoken-OpenAI"); row == nil || row.Level != LevelOK {
		t.Errorf("токен задан, а строка не позеленела: %+v", row)
	}
	if strings.Contains(dumpScreen(doc), "sk-цены-секрет") {
		t.Error("токен источника цен попал в описание экрана")
	}
	// у провайдера, что сам называет цену, источника нет — и токена тоже
	if err := s.eng.AddModel(engine.ModelConf{Name: "sonar", Type: "perplexity", BaseURL: "https://api.perplexity.ai", Model: "sonar"}); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.buildScreen(providerAddr("Perplexity", providerTabPrices), "", "", 0)
	if findRow(doc, "pricetoken-Perplexity") != nil {
		t.Error("строка токена у провайдера, что называет цену сам")
	}
}

// Слова закрытых разделов страницы цен правятся на экране.
func TestPriceSkipFormSavesWords(t *testing.T) {
	s := openAIRig(t)
	// слова общие для всех провайдеров, и лежат в «Тонкой настройке» вкладки
	// «Провайдеры», подписанной как общее
	rare := rareFoldOf(t, s, "settings:models~providers")
	if !strings.Contains(rare.Caption, "общее для всех провайдеров") {
		t.Errorf("«Тонкая настройка» не подписана общей: %q", rare.Caption)
	}
	form := rareFormOf(rareBody(t, s, "settings:models~providers"), "words")
	if form == nil || form.Submit == nil || form.Submit.Action != "save_price_skip" || form.ID != "prices-skip" {
		t.Fatalf("формы слов закрытых разделов нет: %+v", form)
	}
	if _, err := s.execute(actionRequest{Action: "save_price_skip", Params: map[string]string{"words": "batch, tool"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.eng.PriceSkipSections(); got != "batch, tool" {
		t.Errorf("слова не сохранены: %q", got)
	}
}
