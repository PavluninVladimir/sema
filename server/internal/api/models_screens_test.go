package api

// «Модели и роли» после разреза на вкладки и экраны сущностей (1 октября 2026):
// все вкладки и экраны собираются, старые адреса открываются, ни одно действие и
// ни одна настройка прежнего раздела не пропали, цена правится полями и ложится
// той же строкой, список ищет и отбирает на сервере, строка тонкая, значения
// ключей не попадают на экран, а каждый переход ведёт на существующий экран.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// modelsAddrs — все адреса раздела, какие бывают у человека: вкладки, подключение,
// модели разного вида и провайдеры на всех вкладках.
func modelsAddrs() []string {
	return []string{
		modelsScreen, modelsScreen + "~" + modelsTabList, modelsScreen + "~" + modelsTabProviders, modelsScreen + "~" + modelsTabIssues,
		entityAddr(modelsScreen, modelsNew, ""),
		modelAddr("gpt-6-luna"), modelAddr("deepseek-v4-flash"), modelAddr("gpt-4o-mini-transcribe"), modelAddr("qwen3"), modelAddr("мой-сервер"),
		providerAddr("OpenAI", ""), providerAddr("OpenAI", providerTabPrices), providerAddr("OpenAI", providerTabBalance),
		providerAddr("DeepSeek", providerTabPrices), providerAddr("DeepSeek", providerTabBalance),
		providerAddr("OpenRouter", providerTabBalance), providerAddr("Perplexity", providerTabPrices),
		providerAddr(engine.GroupThisMachine, ""), providerAddr(engine.FalProvider, ""),
	}
}

// modelsRichFix — фикстура модели с ещё тремя: своя с неугадываемым умением,
// OpenRouter (ключ управления остатка) и сервер без каталога.
func modelsRichFix(t *testing.T) *Server {
	t.Helper()
	s, _ := modelsFix(t, 173)
	modelsFixAdd(t, s, engine.ModelConf{Name: "мой-сервер", Type: "openai", BaseURL: "http://192.168.1.5:9000/v1", Model: "large-v3"})
	modelsFixAdd(t, s, engine.ModelConf{Name: "or-модель", Type: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "a/b"})
	if err := s.eng.SetProviderKey("OpenRouter", "or-проба-СЕКРЕТ-0005"); err != nil {
		t.Fatal(err)
	}
	return s
}

// buildAll — собрать экран и тело его «Тонкой настройки».
func buildAll(t *testing.T, s *Server, screen string) (*Document, []Component) {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("экран %s: %v", screen, err)
	}
	var rare []Component
	if doc.rare != nil {
		rare = rareBody(t, s, screen)
	}
	return doc, rare
}

// walkAll — обойти компоненты, их детей и ленивые тела.
func walkAll(cs []Component, fn func(Component)) {
	for _, c := range cs {
		fn(c)
		walkAll(c.Children, fn)
	}
}

// everyAction — все действия компонента: кнопки, меню, списки в строке и в
// ячейках таблицы, отправка формы.
func everyAction(c Component) []Action {
	out := append([]Action(nil), c.Actions...)
	out = append(out, c.Menu...)
	for _, s := range c.Selects {
		out = append(out, s.Action)
	}
	for _, cell := range c.Cells {
		if cell.Select != nil {
			out = append(out, cell.Select.Action)
		}
		if cell.Action.Action != "" {
			out = append(out, cell.Action)
		}
	}
	if c.Submit != nil {
		out = append(out, *c.Submit)
	}
	return out
}

// reachable — что нашлось на экранах: «!действие» и имена полей.
func reachable(t *testing.T, s *Server, screens ...string) map[string]bool {
	t.Helper()
	has := map[string]bool{}
	for _, screen := range screens {
		doc, rare := buildAll(t, s, screen)
		visit := func(c Component) {
			for _, a := range everyAction(c) {
				has["!"+a.Action] = true
			}
			for _, f := range c.Fields {
				has[f.Name] = true
			}
		}
		walkAll(doc.Components, visit)
		walkAll(rare, visit)
		for _, a := range doc.ToolbarActions {
			has["!"+a.Action] = true
		}
	}
	return has
}

// Вкладки и экраны сущностей собираются в разметку, а адрес выбирает ровно один
// пункт колонки разделов; внутренние вкладки — это адреса.
func TestModelsAllScreensBuild(t *testing.T) {
	s := modelsRichFix(t)
	for _, screen := range modelsAddrs() {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Errorf("%s: %v", screen, err)
			continue
		}
		if doc.ActivePanel != modelsScreen {
			t.Errorf("%s: в колонке разделов выбран %q", screen, doc.ActivePanel)
		}
		if len(doc.Tabs) != 0 {
			t.Errorf("%s: у настроек вкладок быть не должно", screen)
		}
		markup, err := web.Render(toScreen(doc))
		if err != nil || len(markup) == 0 || strings.Contains(string(markup), "<no value>") {
			t.Errorf("%s: разметка: %v", screen, err)
		}
		if strings.Contains(dumpScreen(doc), "неизвестный компонент") {
			t.Errorf("%s: клиент не знает компонента", screen)
		}
	}
	// у раздела четыре вкладки, и выбранная — адрес; счётчик у «Моделей»
	doc, _ := buildAll(t, s, modelsScreen+"~"+modelsTabList)
	var titles []string
	for _, tab := range doc.Subtabs {
		titles = append(titles, tab.Title+"|"+tab.Badge)
	}
	if got := strings.Join(titles, ";"); !strings.HasPrefix(got, fmt.Sprintf("Роли|;Модели|%d;Провайдеры|;Проблемы|", len(s.eng.ModelCards()))) {
		t.Errorf("вкладки раздела: %s", got)
	}
	if doc.ActiveSubtab != modelsScreen+"~"+modelsTabList {
		t.Errorf("выбранная вкладка %q", doc.ActiveSubtab)
	}
	// экран модели и провайдера — своя шапка и обратный путь
	for screen, back := range map[string]string{
		modelAddr("gpt-6-luna"):                   modelsScreen + "~" + modelsTabList,
		providerAddr("OpenAI", providerTabPrices): modelsScreen + "~" + modelsTabProviders,
	} {
		doc, _ := buildAll(t, s, screen)
		if len(doc.Components) < 2 || doc.Components[0].Type != "crumb" || doc.Components[0].Actions[0].Target != back {
			t.Errorf("%s: обратный путь %+v", screen, doc.Components[0])
		}
		if doc.Components[1].ID != "entity" || doc.Components[1].Style != "sticky" {
			t.Errorf("%s: нет закреплённой шапки сущности", screen)
		}
	}
	// у провайдера, за чьи вызовы платят, три вкладки; у локальных — одна, без полосы
	prices, _ := buildAll(t, s, providerAddr("OpenAI", providerTabPrices))
	if len(prices.Subtabs) != 3 || prices.ActiveSubtab != providerAddr("OpenAI", providerTabPrices) {
		t.Errorf("вкладки провайдера: %+v · %q", prices.Subtabs, prices.ActiveSubtab)
	}
	local, _ := buildAll(t, s, providerAddr(engine.GroupThisMachine, providerTabPrices))
	if len(local.Subtabs) != 0 {
		t.Errorf("у локального провайдера вкладки цен и остатка: %+v", local.Subtabs)
	}
	// несуществующего не бывает «пусто»: объяснение с дорогой назад
	for _, screen := range []string{modelAddr("нет-такой"), providerAddr("Нет-такого", "")} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil || !strings.Contains(dumpScreen(doc), "нет") || doc.Components[0].Type != "crumb" {
			t.Errorf("%s: %v · %s", screen, err, dumpScreen(doc))
		}
	}
}

// Прежние адреса продолжают открываться: вкладки раздела не переименованы, а
// выдуманный хвост ведёт на первую вкладку.
func TestModelsOldAddressesStillOpen(t *testing.T) {
	s := modelsRichFix(t)
	for _, screen := range []string{"settings:models", "settings:models~list", "settings:models~providers", "settings:models~issues", "settings:models~выдумка"} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if doc.ActivePanel != modelsScreen {
			t.Errorf("%s: выбран раздел %q", screen, doc.ActivePanel)
		}
	}
	// старые «Провайдеры и цены» открываются списком провайдеров, а не пустотой
	doc, _ := buildAll(t, s, "settings:models~providers")
	if findRow(doc, "provider-OpenAI") == nil || findRow(doc, "provider-DeepSeek") == nil {
		t.Error("на «Провайдерах» нет строк провайдеров")
	}
}

// Ни одно действие и ни одна настройка прежнего раздела не пропали: список —
// то, что было на трёх вкладках до разреза, включая служебные роли, речь, цены и
// остаток. Что переехало — переехало (цена и «Имя в прайсе» теперь поля одной
// формы), но достижимо.
func TestModelsSettingsNotLost(t *testing.T) {
	s := modelsRichFix(t)
	if err := s.eng.AddProvider(engine.ProviderConf{Name: "Шлюз", Kind: "openai", BaseURL: "http://gw.example.org/v1", NeedKey: true}); err != nil {
		t.Fatal(err)
	}
	screens := append([]string{}, modelsAddrs()...)
	has := reachable(t, s, screens...)
	// шаги подключения живут в памяти процесса: каждое состояние — свой экран
	for _, state := range []catalogState{
		{Provider: "Claude Code (подписка)"},
		{Provider: "Шлюз"},
		{Provider: "OpenAI", Models: []engine.CatalogModel{{ID: "a"}, {ID: "b"}}},
	} {
		s.mu.Lock()
		s.catalog = state
		s.mu.Unlock()
		for name := range reachable(t, s, entityAddr(modelsScreen, modelsNew, "")) {
			has[name] = true
		}
	}
	s.mu.Lock()
	s.catalog = catalogState{}
	s.mu.Unlock()
	for name := range reachable(t, s, entityAddr(modelsScreen, modelsNew, "")) {
		has[name] = true
	}
	for _, name := range []string{
		// роли
		"!set_role_model", "!set_role_spare", "!set_role_timeout", "!dismiss_role_err",
		// модель
		"!check_model", "!toggle_model_search", "!toggle_model_hears", "!toggle_model_trust", "!remove_model",
		"!set_model_price", "price_name", "in", "out", "cache", "minute", "schedule", "peak_in", "peak_out", "peak_cache", "currency",
		// провайдер: ключ, поиск, список, отключение
		"!set_provider_key", "!toggle_provider_search", "!connect_provider_models", "!remove_provider_models",
		// цены и остаток провайдера
		"!save_price_source", "!check_prices", "!set_price_token", "!save_balance_source", "!refresh_balance", "!set_management_key",
		"kind", "url", "low", "amount", "date",
		// «Тонкая настройка» провайдеров
		"!save_price_skip", "words", "!set_models_limits", "max_output",
		// служебные роли
		"!save_embed_model", "!toggle_embed_trust", "!save_settings", "!save_vision_model", "!save_ocr_engine", "!save_no_text",
		"!pick_image_model", "!save_image_mode",
		// речь
		"!save_speech_outside", "!save_speech_language", "!save_speech_files",
		// подключение
		"!load_catalog", "!add_cli_model", "!add_model_manual", "!add_model", "!add_all_models", "!reset_catalog",
		"provider", "key", "trusted", "model",
	} {
		if !has[name] {
			t.Errorf("прежнее %q не нашлось ни на одном экране раздела — оно стало недостижимым", name)
		}
	}
}

// Действия на прежних кнопках уже лежащих уведомлений и карточек продолжают
// работать под прежними именами: роль, цена, остаток, речь.
func TestModelsOldActionsKeepWorking(t *testing.T) {
	s := modelsRichFix(t)
	run := func(action, target string, params map[string]string) error {
		_, err := s.execute(actionRequest{Action: action, Target: target, Params: params})
		return err
	}
	// список в ячейке таблицы шлёт то же, что слал список в строке
	if err := run("set_role_model", engine.RoleChat, map[string]string{"value": "gpt-6.1-sol · OpenAI"}); err != nil {
		t.Fatal(err)
	}
	if err := run("set_role_spare", engine.RoleChat, map[string]string{"value": "нет"}); err != nil {
		t.Fatal(err)
	}
	if err := run("set_role_timeout", engine.RoleChat, map[string]string{"value": "180 с"}); err != nil {
		t.Fatal(err)
	}
	if r := s.eng.Role(engine.RoleChat); r.Model != "gpt-6.1-sol" || r.Spare != "нет" || r.Timeout != 180 {
		t.Errorf("роль: %+v", r)
	}
	// цена прежним мини-языком и имя в прайсе прежним действием
	if err := run("set_model_price", "gpt-5.6-luna", map[string]string{"value": "$0.5 / $2 / 1M"}); err != nil {
		t.Fatal(err)
	}
	if got := s.eng.PriceOf("gpt-5.6-luna"); got != "$0.5 / $2 / 1M" {
		t.Errorf("цена мини-языком: %q", got)
	}
	if err := run("set_price_name", "gpt-5.6-luna", map[string]string{"value": "gpt-5.6-luna-pro"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.eng.ModelCardOf("gpt-5.6-luna"); c.PriceName != "gpt-5.6-luna-pro" {
		t.Errorf("имя в прайсе: %q", c.PriceName)
	}
	// «Модель для картинок» шлёт имя с провайдером: в роль ложится имя
	if err := run("save_vision_model", "", map[string]string{"model": "gpt-6-luna · OpenAI"}); err != nil {
		t.Fatal(err)
	}
	if got := s.eng.Role(engine.RoleVision).Model; got != "gpt-6-luna" {
		t.Errorf("в роль картинок легло %q", got)
	}
	// остаток: прежний вызов с длинной подписью и новый с короткой
	for _, kind := range []string{kindTitle(balanceKindTitles, engine.BalanceEstimate), "оценка"} {
		if err := run("save_balance_source", "OpenAI", map[string]string{"kind": kind, "low": "5", "amount": "$10"}); err != nil {
			t.Errorf("остаток %q: %v", kind, err)
		}
	}
	for _, kind := range []string{kindTitle(priceKindTitles, engine.PriceNone), "не брать", "none"} {
		if err := run("save_price_source", "OpenAI", map[string]string{"kind": kind}); err != nil {
			t.Errorf("источник цен %q: %v", kind, err)
		}
	}
	// предел ответа модели — настройка реестра, читается движком
	if err := run("set_models_limits", "", map[string]string{"max_output": "4096"}); err != nil {
		t.Fatal(err)
	}
	if got := s.eng.Store().Setting("models.max_output", ""); got != "4096" {
		t.Errorf("предел ответа: %q", got)
	}
	if err := run("set_models_limits", "", map[string]string{"max_output": "много"}); err == nil {
		t.Error("плохой предел принят")
	}
}

// Цена полями: форма на экране модели показывает то, что лежит в строке, и
// сохраняет её тем же SetModelPrice. Поля ↔ строка туда и обратно для всех форм
// записи; ручная цена помечается как раньше; форма с одним именем в прайсе цену не
// трогает.
func TestModelPriceFormSavesLikeTheOldLine(t *testing.T) {
	s := modelsRichFix(t)
	form := func(name string) *Component {
		t.Helper()
		doc, _ := buildAll(t, s, modelAddr(name))
		f := findRow(doc, "model-price")
		if f == nil || f.Submit == nil || f.Submit.Action != "set_model_price" || f.Submit.Target != name {
			t.Fatalf("%s: формы цены нет: %+v", name, f)
		}
		if f.Style != formRows || !f.Deferred {
			t.Errorf("%s: цена правится не строками настройки с полосой: %+v", name, f)
		}
		return f
	}
	values := func(f *Component) map[string]string {
		out := map[string]string{}
		for _, fld := range f.Fields {
			out[fld.Name] = fld.Value
		}
		return out
	}
	// у DeepSeek в строке всё: вход, выход, кэш, пик, расписание
	v := values(form("deepseek-v4-flash"))
	for field, want := range map[string]string{"in": "0.22", "out": "0.66", "cache": "0.007", "peak_in": "0.44", "peak_out": "1.32",
		"peak_cache": "0.014", "schedule": "пн-пт 01:00-04:00,06:00-10:00 UTC", "currency": "$", "price_name": "deepseek-v4-flash"} {
		if v[field] != want {
			t.Errorf("поле %s: %q, ждали %q", field, v[field], want)
		}
	}
	// поминутная цена показывается полем «За минуту», токенов нет
	v = values(form("gpt-4o-mini-transcribe"))
	if v["minute"] != "0.003" || v["in"] != "" || v["out"] != "" {
		t.Errorf("поминутная цена в полях: %v", v)
	}
	// у модели без слуха и без минутной цены такого поля нет
	if _, has := values(form("gpt-6.1-sol"))["minute"]; has {
		t.Error("у обычной модели есть поле «За минуту звука»")
	}
	// пик, пока его нет, и валюта прячутся под «Ещё N полей», а заданный виден
	for _, c := range []struct {
		model string
		more  map[string]bool
	}{
		{"gpt-6.1-sol", map[string]bool{"schedule": true, "peak_in": true, "currency": true}},
		{"deepseek-v4-flash", map[string]bool{"schedule": false, "peak_in": false, "currency": true}},
	} {
		for _, fld := range form(c.model).Fields {
			if want, ok := c.more[fld.Name]; ok && fld.More != want {
				t.Errorf("%s.%s: More=%v, ждали %v", c.model, fld.Name, fld.More, want)
			}
		}
	}

	// сохранение полями: каждая форма записи из CLAUDE.md
	cases := []struct {
		name   string
		params map[string]string
		price  string
		peak   string
		sched  string
	}{
		{"одна цена", map[string]string{"in": "0.55"}, "$0.55 / 1M", "", ""},
		{"вход и выход", map[string]string{"in": "0.55", "out": "2.20"}, "$0.55 / $2.2 / 1M", "", ""},
		{"с кэшем", map[string]string{"in": "0.22", "out": "0.66", "cache": "0.007"}, "$0.22 / $0.66 / $0.007 / 1M", "", ""},
		{"с пиком и расписанием", map[string]string{"in": "0.22", "out": "0.66", "cache": "0.007", "peak_in": "0.44", "peak_out": "1.32",
			"peak_cache": "0.014", "schedule": "пн-пт 01:00-04:00,06:00-10:00 UTC"},
			"$0.22 / $0.66 / $0.007 / 1M", "$0.44 / $1.32 / $0.014 / 1M", "пн-пт 01:00-04:00,06:00-10:00 UTC"},
		{"поминутная", map[string]string{"in": "", "out": "", "cache": "", "minute": "0.003"}, "$0.003 / мин", "", ""},
	}
	for _, c := range cases {
		params := map[string]string{"in": "", "out": "", "cache": "", "minute": "", "peak_in": "", "peak_out": "", "peak_cache": "", "schedule": "", "currency": "$"}
		for k, v := range c.params {
			params[k] = v
		}
		if _, err := s.execute(actionRequest{Action: "set_model_price", Target: "gpt-5.6-luna", Params: params}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		card, _ := s.eng.ModelCardOf("gpt-5.6-luna")
		if card.Price != c.price || card.PricePeak != c.peak || card.Peak != c.sched {
			t.Errorf("%s: лежит %q | %q | %q, ждали %q | %q | %q", c.name, card.Price, card.PricePeak, card.Peak, c.price, c.peak, c.sched)
		}
		if card.PriceBy != "человек" {
			t.Errorf("%s: ручная цена помечена %q", c.name, card.PriceBy)
		}
		// и экран показывает то же, что записали: поля ↔ строка туда и обратно
		got := values(form("gpt-5.6-luna"))
		back := engine.PricePartsOfLine(c.price, c.peak, c.sched)
		if got["in"] != back.In || got["out"] != back.Out || got["cache"] != back.Cache || got["minute"] != back.Minute || got["schedule"] != back.Schedule {
			t.Errorf("%s: экран показывает %v, а в строке %+v", c.name, got, back)
		}
	}

	// форма, где поменяли только имя в прайсе, цену не трогает и отметку прайса оставляет
	was, _ := s.eng.ModelCardOf("gpt-6-luna")
	if was.PriceBy != engine.PriceByPrice {
		t.Fatalf("у gpt-6-luna цена не с прайса: %q", was.PriceBy)
	}
	params := map[string]string{"price_name": "luna-2", "in": "2.5", "out": "10", "cache": "0.25", "minute": "", "peak_in": "", "peak_out": "",
		"peak_cache": "", "schedule": "", "currency": "$"}
	if _, err := s.execute(actionRequest{Action: "set_model_price", Target: "gpt-6-luna", Params: params}); err != nil {
		t.Fatal(err)
	}
	now, _ := s.eng.ModelCardOf("gpt-6-luna")
	if now.PriceName != "luna-2" || now.PriceBy != engine.PriceByPrice || now.PriceAt != was.PriceAt || now.Price != was.Price {
		t.Errorf("форма с одним именем тронула цену: было %+v, стало %+v", was, now)
	}

	// отказ называет поле и ничего не пишет: ни цену, ни имя
	bad := map[string]string{"price_name": "не должно записаться", "in": "дорого", "currency": "$"}
	_, err := s.execute(actionRequest{Action: "set_model_price", Target: "gpt-6-luna", Params: bad})
	if err == nil || !strings.Contains(err.Error(), "«Вход»") {
		t.Errorf("плохое число: %v", err)
	}
	if after, _ := s.eng.ModelCardOf("gpt-6-luna"); after.PriceName != "luna-2" {
		t.Errorf("отказанная форма записала имя: %q", after.PriceName)
	}
	// все поля пусты — цена убрана вместе с отметкой
	clear := map[string]string{"in": "", "out": "", "cache": "", "minute": "", "peak_in": "", "peak_out": "", "peak_cache": "", "schedule": "", "currency": "$"}
	if _, err := s.execute(actionRequest{Action: "set_model_price", Target: "gpt-6-sol", Params: clear}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.eng.ModelCardOf("gpt-6-sol"); c.Price != "" || c.PriceBy != "" {
		t.Errorf("цена не убралась: %+v", c)
	}
}

// Список моделей ищет и отбирает на сервере по всем моделям, а не по показанным.
func TestModelsListSearchAndFilterOnServer(t *testing.T) {
	s := modelsRichFix(t)
	rowsOf := func(filter, search string, limit int) (rows []string, doc *Document) {
		t.Helper()
		doc, err := s.buildScreen("settings:models~list", filter, search, limit)
		if err != nil {
			t.Fatal(err)
		}
		walkAll(doc.Components, func(c Component) {
			if c.Type == "row" && strings.HasPrefix(c.ID, "model-") {
				rows = append(rows, strings.TrimPrefix(c.ID, "model-"))
			}
		})
		return rows, doc
	}
	all, doc := rowsOf("", "", 0)
	if len(all) != modelsPage {
		t.Errorf("первая порция: %d строк вместо %d", len(all), modelsPage)
	}
	// чипы с числами по всем моделям, а не по показанным
	counts := map[string]string{}
	for _, f := range doc.Filters {
		counts[f.ID] = f.Title
	}
	cards := s.eng.ModelCards()
	var noPrice, search, trusted, roles int
	for _, c := range cards {
		if c.NoPrice() {
			noPrice++
		}
		if c.Search {
			search++
		}
		if c.Trusted {
			trusted++
		}
		if c.InRoles() {
			roles++
		}
	}
	for id, n := range map[string]int{"noprice": noPrice, "search": search, "trusted": trusted, "roles": roles, "all": len(cards)} {
		if !strings.HasSuffix(counts[id], fmt.Sprintf("· %d", n)) {
			t.Errorf("чип %q: %q, а моделей %d", id, counts[id], n)
		}
	}
	if doc.ActiveFilter != "all" || doc.SearchHint == "" || !doc.Searchable {
		t.Errorf("отбор %q, подсказка %q, поиск %v", doc.ActiveFilter, doc.SearchHint, doc.Searchable)
	}

	// поиск находит модель, которой нет на первой странице
	var deep string
	for _, c := range cards {
		if strings.HasPrefix(c.Name, "pplx-ряд-") {
			deep = c.Name // последняя из россыпи: в первую порцию она не попадает
		}
	}
	if deep == "" {
		t.Fatal("в фикстуре нет глубокой модели")
	}
	for _, shown := range all {
		if shown == deep {
			t.Fatalf("глубокая модель %s уже на первой странице", deep)
		}
	}
	// «pplx-ряд» — по рунам, а не по байтам; регистр не важен
	stem := string([]rune(deep)[:8])
	found, _ := rowsOf("", strings.ToUpper(stem), 0)
	if len(found) == 0 {
		t.Fatalf("поиск не нашёл %s", deep)
	}
	for _, name := range found {
		if !strings.Contains(strings.ToLower(name), strings.ToLower(stem)) {
			t.Errorf("в поиске чужая модель %q", name)
		}
	}
	// по провайдеру тоже
	if byProvider, _ := rowsOf("", "perplexity", 0); len(byProvider) == 0 {
		t.Error("поиск по имени провайдера ничего не нашёл")
	}

	// отбор
	for filter, ok := range map[string]func(engine.ModelCard) bool{
		"noprice": func(c engine.ModelCard) bool { return c.NoPrice() },
		"search":  func(c engine.ModelCard) bool { return c.Search },
		"hears":   func(c engine.ModelCard) bool { return c.Hears },
		"trusted": func(c engine.ModelCard) bool { return c.Trusted },
		"roles":   func(c engine.ModelCard) bool { return c.InRoles() },
		"gone":    func(c engine.ModelCard) bool { return c.Gone },
	} {
		got, d := rowsOf(filter, "", 200)
		if d.ActiveFilter != filter {
			t.Errorf("%s: выбран отбор %q", filter, d.ActiveFilter)
		}
		byName := map[string]engine.ModelCard{}
		want := 0
		for _, c := range cards {
			byName[c.Name] = c
			if ok(c) {
				want++
			}
		}
		if len(got) != want {
			t.Errorf("%s: строк %d, а подходящих моделей %d", filter, len(got), want)
		}
		for _, name := range got {
			if !ok(byName[name]) {
				t.Errorf("%s: в отбор попала %q", filter, name)
			}
		}
	}
	// незнакомый отбор — «Все»; пустой отбор объясняет себя
	if _, d := rowsOf("выдумка", "", 0); d.ActiveFilter != "all" {
		t.Errorf("незнакомый отбор: %q", d.ActiveFilter)
	}
	none, d := rowsOf("", "такой-модели-нет-нигде", 0)
	if len(none) != 0 || !strings.Contains(dumpScreen(d), "Под отбор ничего не подошло") {
		t.Errorf("пустой отбор: %v", none)
	}

	// «Показать ещё»: удвоенный предел отдаёт остальное
	more, _ := rowsOf("", "", 100)
	if len(more) != 100 {
		t.Errorf("по «Показать ещё» %d строк", len(more))
	}
	if !strings.Contains(dumpScreen(doc), "Показать ещё") {
		t.Error("нет кнопки «Показать ещё»")
	}
}

// Группы по провайдеру: без запроса открыты только те, где модели стоят в
// ролях; с запросом — те, где есть совпадения.
func TestModelsListGroupsOpenWhereMatches(t *testing.T) {
	s := modelsRichFix(t)
	open := func(filter, search string) map[string]bool {
		t.Helper()
		doc, err := s.buildScreen("settings:models~list", filter, search, 200)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		walkAll(doc.Components, func(c Component) {
			if c.Type == "section" && strings.HasPrefix(c.ID, "prov-") {
				if !c.Collapsed {
					t.Errorf("%s: раздел провайдера не свёрнут по построению", c.ID)
				}
				out[strings.TrimPrefix(c.ID, "prov-")] = c.Expanded
			}
		})
		return out
	}
	idle := open("", "")
	if !idle["OpenAI"] || !idle[engine.GroupThisMachine] {
		t.Errorf("без запроса закрыты группы с ролями: %v", idle)
	}
	if idle["DeepSeek"] {
		t.Errorf("без запроса открыта группа без ролей: %v", idle)
	}
	searched := open("", "deepseek")
	if len(searched) != 1 || !searched["DeepSeek"] {
		t.Errorf("с запросом должна остаться и открыться группа с совпадениями: %v", searched)
	}
}

// Строка списка тонкая: не больше двух капсул, одна видимая кнопка, остальное в
// меню; вся строка ведёт на экран модели.
func TestModelRowIsThin(t *testing.T) {
	s := modelsRichFix(t)
	for _, screen := range []string{modelsScreen + "~list", providerAddr("OpenAI", ""), providerAddr("Perplexity", "")} {
		doc, err := s.buildScreen(screen, "", "", 200)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		walkAll(doc.Components, func(c Component) {
			if c.Type != "row" || !strings.HasPrefix(c.ID, "model-") {
				return
			}
			n++
			if len(c.Badges) > 2 {
				t.Errorf("%s: у строки %s капсул %d", screen, c.ID, len(c.Badges))
			}
			buttons, goTo := 0, ""
			for _, a := range c.Actions {
				switch {
				case a.Action == "$goto":
					goTo = a.Target
				case a.Label != "toggle":
					buttons++
				}
			}
			if buttons > 1 {
				t.Errorf("%s: у строки %s кнопок %d", screen, c.ID, buttons)
			}
			if goTo != modelAddr(strings.TrimPrefix(c.ID, "model-")) {
				t.Errorf("%s: строка %s не ведёт на экран модели: %q", screen, c.ID, goTo)
			}
			if len(c.Facts) != 0 || c.Toggle != "" || len(c.Selects) != 0 {
				t.Errorf("%s: у тонкой строки %s лишнее: %d фактов, тумблер %q", screen, c.ID, len(c.Facts), c.Toggle)
			}
			for _, a := range c.Menu {
				if a.Style == "destructive" && a.Confirm == "" {
					t.Errorf("%s: удаление без вопроса у %s", screen, c.ID)
				}
			}
		})
		if n == 0 {
			t.Errorf("%s: строк моделей нет", screen)
		}
	}
}

// Значения ключей не попадают на экран ни в каком виде: ни в описание, ни в
// разметку, ни в тело «Тонкой настройки».
func TestProviderKeysNeverOnScreen(t *testing.T) {
	s := modelsRichFix(t)
	// куски значений из фикстуры: целиком ключи не называем, чтобы тест не
	// содержал того, что сторожит
	secrets := []string{"СЕКРЕТ-openai", "СЕКРЕТ-0002", "СЕКРЕТ-0003", "СЕКРЕТ-0004", "СЕКРЕТ-0005"}
	for _, screen := range modelsAddrs() {
		doc, rare := buildAll(t, s, screen)
		raw, _ := json.Marshal(rare)
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatal(err)
		}
		all := dumpScreen(doc) + string(raw) + string(markup)
		for _, secret := range secrets {
			if strings.Contains(all, secret) {
				t.Errorf("%s: значение ключа (%s) попало на экран", screen, secret)
			}
		}
	}
	// «задан» видно, значение — нет
	doc, _ := buildAll(t, s, providerAddr("OpenAI", ""))
	row := findRow(doc, "provkey-OpenAI")
	if row == nil || len(row.Badges) == 0 || !strings.HasPrefix(row.Badges[0].Text, "задан") {
		t.Fatalf("строка ключа: %+v", row)
	}
	if len(row.Actions) != 1 || row.Actions[0].Action != "set_provider_key" || !row.Actions[0].Secure {
		t.Errorf("ключ меняется не закрытым вводом: %+v", row.Actions)
	}
	if row.Actions[0].Value != "" {
		t.Error("в вопросе про ключ стоит начальное значение")
	}
	// ключ меняется, а не показывается: после замены на экране всё так же пусто
	if _, err := s.execute(actionRequest{Action: "set_provider_key", Target: "OpenAI", Params: map[string]string{"value": "sk-новый-СЕКРЕТ-9999"}}); err != nil {
		t.Fatal(err)
	}
	doc, _ = buildAll(t, s, providerAddr("OpenAI", ""))
	if strings.Contains(dumpScreen(doc), "СЕКРЕТ-9999") {
		t.Error("новый ключ попал на экран")
	}
}

// Переходы с экранов раздела и из уведомлений ведут на существующие экраны:
// каждый собирается, а не объясняет, что такого нет.
func TestModelsGotoTargetsExist(t *testing.T) {
	s := modelsRichFix(t)
	targets := map[string]string{}
	for _, screen := range append(modelsAddrs(), "settings", "settings:tools", "health") {
		doc, rare := buildAll(t, s, screen)
		visit := func(c Component) {
			for _, a := range everyAction(c) {
				if a.Action == "$goto" {
					targets[a.Target] = screen
				}
			}
		}
		walkAll(doc.Components, visit)
		walkAll(rare, visit)
		for _, a := range doc.ToolbarActions {
			if a.Action == "$goto" {
				targets[a.Target] = screen
			}
		}
	}
	// адреса, которые строит движок для уведомлений: карточки цен и остатка,
	// живое обновление
	for _, addr := range []string{engine.ProviderScreen("OpenAI", ""), engine.ProviderScreen("DeepSeek", "prices"), engine.ProviderScreen("DeepSeek", "balance"),
		"settings:models~providers", "settings:models~issues", "settings:models"} {
		targets[addr] = "движок"
	}
	if len(targets) < 8 {
		t.Fatalf("на экранах нашлось слишком мало переходов: %v", targets)
	}
	for target, from := range targets {
		if target == "" || strings.HasPrefix(target, "$") {
			t.Errorf("%s: переход без адреса: %q", from, target)
			continue
		}
		doc, err := s.buildScreen(target, "", "", 0)
		if err != nil {
			t.Errorf("%s: переход на %q: %v", from, target, err)
			continue
		}
		dump := dumpScreen(doc)
		if strings.Contains(dump, "Модели «") && strings.Contains(dump, "» нет") || strings.Contains(dump, "Провайдера «") && strings.Contains(dump, "» нет") {
			t.Errorf("%s: переход на %q ведёт на «такого нет»", from, target)
		}
	}
}

// Уведомления ядра ведут в осмысленное место: карточка цен — на вкладку «Цены»
// провайдера, остаток — на «Остаток», живое обновление называет экраны всех
// вкладок провайдера.
func TestModelNotificationsLeadToProviderTabs(t *testing.T) {
	if got := engine.ProviderScreen("OpenAI", "prices"); got != "settings:models@provider:OpenAI~prices" {
		t.Errorf("адрес вкладки цен: %q", got)
	}
	s := modelsRichFix(t)
	// проверка цен закончилась карточкой: в ней есть путь к экрану провайдера
	if err := s.eng.SetPriceSource("OpenAI", engine.PriceNone, ""); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{engine.ProviderScreen("OpenAI", "prices"), engine.ProviderScreen("OpenAI", "balance")} {
		if _, err := s.buildScreen(addr, "", "", 0); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

// Роль на передышке видна и в таблице, и на экране модели; закрытая отметка
// сбой снимает замечание.
func TestRoleOnRestVisible(t *testing.T) {
	s := modelsRichFix(t)
	rows := roleRows(t, s)
	mem := rows[engine.RoleMemory]
	if mem == nil || mem.Level != LevelWarn || !strings.Contains(mem.Cells[3].Text, "отдыхает до") {
		t.Fatalf("роль на передышке в таблице: %+v", mem)
	}
	doc, _ := buildAll(t, s, modelAddr("gpt-6-luna"))
	if findRow(doc, "model-rest-"+engine.RoleMemory) == nil {
		t.Error("на экране модели не сказано, что её роль отдыхает")
	}
	// а у роли без модели — капсула и полоса
	tr := rows[engine.RoleTranslate]
	if tr == nil || len(tr.Badges) == 0 || tr.Badges[0].Text != "без модели" || tr.Level != LevelWarn {
		t.Errorf("роль без модели: %+v", tr)
	}
}

// Удаление: с экрана самой модели уходим к списку, вопрос называет роли, которые
// останутся без модели; доверие по-прежнему спрашивается.
func TestModelDeleteAndTrustKeepTheirQuestions(t *testing.T) {
	s := modelsRichFix(t)
	doc, _ := buildAll(t, s, modelAddr("gpt-6-luna"))
	del := findRow(doc, "danger-delete")
	if del == nil || len(del.Actions) != 1 || del.Actions[0].Confirm == "" || del.Actions[0].Style != "destructive" {
		t.Fatalf("удаление: %+v", del)
	}
	for _, role := range []string{"Чаты", "Память", "Модули"} {
		if !strings.Contains(del.Actions[0].Confirm, role) {
			t.Errorf("вопрос про удаление не называет роль %q: %s", role, del.Actions[0].Confirm)
		}
	}
	trust := findRow(doc, "trust-toggle")
	if trust == nil || len(trust.Actions) != 1 || trust.Actions[0].Action != "toggle_model_trust" || trust.Actions[0].Confirm == "" {
		t.Fatalf("доверие без вопроса: %+v", trust)
	}
	// удаление с экрана модели ведёт к списку
	redirect, err := s.execute(actionRequest{Action: "remove_model", Target: "gpt-5.6-luna", Screen: modelAddr("gpt-5.6-luna")})
	if err != nil || redirect != modelsScreen+"~"+modelsTabList {
		t.Errorf("после удаления: %q %v", redirect, err)
	}
	if _, ok := s.eng.ModelCardOf("gpt-5.6-luna"); ok {
		t.Error("модель не удалена")
	}
	// доверие переключается тем же действием
	if _, err := s.execute(actionRequest{Action: "toggle_model_trust", Target: "gpt-6-sol"}); err != nil {
		t.Fatal(err)
	}
	if !s.eng.ModelIsTrusted("gpt-6-sol") {
		t.Error("доверие не включилось")
	}
	// клиент по подписке доверенным не бывает: переключателя нет вовсе
	if err := s.eng.AddModel(engine.ModelConf{Name: "подписка", Type: "cli", BaseURL: "claude", Model: "claude"}); err != nil {
		t.Fatal(err)
	}
	cli, _ := buildAll(t, s, modelAddr("подписка"))
	if row := findRow(cli, "trust-toggle"); row == nil || len(row.Actions) != 0 || row.Toggle != "" {
		t.Errorf("у клиента по подписке есть переключатель доверия: %+v", row)
	}
}

// Доверие — утверждение человека про конкретную модель, и пачкой оно не
// выдаётся: модели, подключённые провайдером разом, всегда внешние, что бы ни
// пришло в запросе (его могли подделать). Тумблер остаётся только у добавления
// одной модели.
func TestBatchConnectNeverTrusts(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.AddProvider(engine.ProviderConf{Name: "Проба", Kind: "openai", BaseURL: "http://127.0.0.1:1/v1", Catalog: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.eng.AddProvider(engine.ProviderConf{Name: "Шлюз", Kind: "openai", BaseURL: "http://gw.example.org/v1", NeedKey: true}); err != nil {
		t.Fatal(err)
	}
	setCatalog := func(c catalogState) {
		s.mu.Lock()
		s.catalog = c
		s.mu.Unlock()
	}
	// «Подключить все» с подделанным trusted=true: все модели внешние
	setCatalog(catalogState{Provider: "Проба", Models: []engine.CatalogModel{{ID: "x/пачка-а"}, {ID: "x/пачка-б"}}})
	if _, err := s.execute(actionRequest{Action: "add_all_models", Params: map[string]string{"trusted": "true"}}); err != nil {
		t.Fatal(err)
	}
	// и обычное автоподключение пачкой
	if _, _, err := s.connectAllCounted(catalogState{Provider: "Проба", Models: []engine.CatalogModel{{ID: "x/пачка-в"}}}); err != nil {
		t.Fatal(err)
	}
	// провайдер без списка: первый шаг с подделанным trusted=true, второй — тоже
	if _, err := s.execute(actionRequest{Action: "load_catalog",
		Params: map[string]string{"provider": "Шлюз", "key": "k", "trusted": "true"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "add_model_manual",
		Params: map[string]string{"name": "пачка-г", "model": "g", "trusted": "true"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"пачка-а", "пачка-б", "пачка-в", "пачка-г"} {
		if _, ok := s.eng.ModelCardOf(name); !ok {
			t.Fatalf("модель %s не подключилась", name)
		}
		if s.eng.ModelIsTrusted(name) {
			t.Errorf("модель %s, подключённая пачкой, стала доверенной", name)
		}
	}

	// одна модель: тумблер действует — с ним доверенная, без него внешняя
	one := func(name, trusted string) {
		t.Helper()
		setCatalog(catalogState{Provider: "Проба", Models: []engine.CatalogModel{{ID: "x/" + name}}})
		params := map[string]string{"name": name, "model": "x/" + name}
		if trusted != "" {
			params["trusted"] = trusted
		}
		if _, err := s.execute(actionRequest{Action: "add_model", Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	one("одна-доверенная", "true")
	one("одна-внешняя", "")
	one("одна-выключенная", "false")
	if !s.eng.ModelIsTrusted("одна-доверенная") {
		t.Error("тумблер у добавления одной модели не сделал её доверенной")
	}
	if s.eng.ModelIsTrusted("одна-внешняя") || s.eng.ModelIsTrusted("одна-выключенная") {
		t.Error("модель без отметки стала доверенной")
	}

	// экран подключения: тумблера в форме пачки нет, а сказано, что доверие — на экране модели
	doc, _ := buildAll(t, s, entityAddr(modelsScreen, modelsNew, ""))
	form := findRow(doc, "connect")
	if form == nil || form.Submit == nil || form.Submit.Action != "load_catalog" {
		t.Fatalf("формы подключения нет: %+v", form)
	}
	for _, f := range form.Fields {
		if f.Name == "trusted" {
			t.Errorf("в форме подключения пачкой остался тумблер доверия: %+v", f)
		}
		if f.Name == "key" && (f.Type != "secure" || f.Value != "") {
			t.Errorf("ключ вводится не закрытым полем: %+v", f)
		}
	}
	note := findRow(doc, "connect-trust")
	if note == nil || !strings.Contains(note.Text, "Модели подключаются внешними") || !strings.Contains(note.Text, "на её экране") {
		t.Errorf("нет пояснения про доверие: %+v", note)
	}
	// у выбора одной модели тумблер остался
	setCatalog(catalogState{Provider: "Проба", Models: []engine.CatalogModel{{ID: "x/m"}}})
	pick, _ := buildAll(t, s, entityAddr(modelsScreen, modelsNew, ""))
	var toggle bool
	if f := findRow(pick, "pickmodel"); f != nil {
		for _, fld := range f.Fields {
			toggle = toggle || (fld.Name == "trusted" && fld.Type == "toggle" && fld.Value == "false")
		}
	}
	if !toggle {
		t.Error("у добавления одной модели нет выключенного тумблера «Доверять закрытое»")
	}
}

// Вес раздела на ста семидесяти моделях: до тысячи узлов и ста килобайт на
// каждой вкладке и каждом экране сущности (до разреза: 1262 узла и 108 КБ у
// списка, 3597 и 319 КБ в сентябре).
func TestModelsSectionWeight(t *testing.T) {
	s := modelsRichFix(t)
	for _, screen := range modelsAddrs() {
		w := weigh(t, s, screen)
		if w.Nodes > budgetNodes || w.Bytes > budgetBytes {
			t.Errorf("%s: %d узлов и %d КБ при бюджете %d и %d\n%s", screen, w.Nodes, w.Bytes/1024, budgetNodes, budgetBytes/1024, parseWeight(t, s, screen))
		}
	}
}

// Телефон: у списка моделей остаётся своё поле поиска и полоса отбора, а
// таблица ролей не теряет списков — раскладывает их клиент.
func TestModelsOnPhone(t *testing.T) {
	s := modelsRichFix(t)
	list, err := s.buildScreenForPhone("settings:models~list", "", "", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !list.Searchable || len(list.Filters) == 0 {
		t.Errorf("на телефоне у списка моделей нет поиска или отбора: %v %d", list.Searchable, len(list.Filters))
	}
	roles, err := s.buildScreenForPhone("settings:models", "", "", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if roles.Searchable {
		t.Error("на телефоне у таблицы ролей верхнее поле ищет по настройкам, а места ему нет")
	}
	markup, err := web.Render(toScreen(roles))
	if err != nil || !strings.Contains(string(markup), `data-label="Основная"`) {
		t.Errorf("у ячеек таблицы нет подписей колонок для телефона: %v", err)
	}
}
