package api

// Размеченное редкое по разделам: ни одна настройка не пропала, умолчание на
// экране — то же, что у движка, каждая сохраняется своим действием и читается
// обратно, возврат умолчания возвращает умолчание.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// rareSetting — одно поле «Тонкой настройки»: имя поля формы (параметр
// действия) и ключ настройки в реестре.
type rareSetting struct{ Field, Key string }

// rareScreens — что лежит в «Тонкой настройке» какого экрана. Список имён —
// договор: поле, пропавшее отсюда, стало недостижимым, а человек этого сам не
// заметит. Он же — единственное место теста, где названы ключи.
var rareScreens = map[string][]rareSetting{
	mcpScreen: {
		{"chat_chars", "mcp.chat_chars"}, {"chat_calls", "mcp.chat_calls"}, {"chat_result_chars", "mcp.chat_result_chars"},
		{"card_chars", "mcp.card_chars"}, {"recheck_s", "mcp.recheck_s"}, {"error_chars", "mcp.error_chars"},
		{"arg_retries", "mcp.arg_retries"}, {"tool_hint_days", "mcp.tool_hint_days"}, {"tool_hint_chars", "mcp.tool_hint_chars"},
		{"login_ttl_min", "mcp.login_ttl_min"}, {"refresh_skew_s", "mcp.refresh_skew_s"}, {"net_timeout_s", "mcp.net_timeout_s"},
		{"token_retries", "mcp.token_retries"}, {"token_retry_ms", "mcp.token_retry_ms"}, {"client_metadata", "mcp.client_metadata"},
		{"chat_list_fields", "mcp.chat_list_fields"},
	},
	searchScreen: {
		{"read_chars", "chats.read_links_chars"}, {"page_chars", "web.page_chars"}, {"page_links", "web.page_links"},
		{"page_min_chars", "web.page_min_chars"}, {"read_ttl_h", "chats.read_links_ttl_h"},
		{"find_per_day", "web.find_per_day"}, {"find_gap_ms", "web.find_gap_ms"}, {"find_cache_min", "web.find_cache_min"},
	},
	serversScreen + "~" + serversTabAuto: {
		{"max_steps", "night.max_steps"}, {"look_max", "night.look_max"}, {"look_timeout_s", "night.look_timeout_s"},
		{"every_h", "night.every_h"}, {"skill_linux", "night.skill.linux"}, {"skill_routeros", "night.skill.routeros"},
		{"rollback_min", "auto.rollback_min"}, {"max_hour", "auto.max_hour"},
		{"timeout_s", "ssh.timeout_s"}, {"confirm_timeout_s", "ssh.confirm_timeout_s"}, {"max_output", "ssh.max_output"},
	},
	accessScreen:                          {{auth.SettingPhraseLimit, auth.SettingPhraseLimit}},
	accessScreen + "~" + accessTabNetwork: {{auth.SettingLocalHosts, auth.SettingLocalHosts}, {auth.SettingRPID, auth.SettingRPID}, {auth.SettingOrigin, auth.SettingOrigin}},
	skillsScreen:                          {{"max", "skills.max_tokens"}},
	learningScreen:                        {{"max", "learn.max_per_day"}, {"score", "learn.min_score"}},
	// «Агенты» (2 октября 2026): где слушать — на вкладке «Агенты», адрес шлюза и
	// куда он вносит запросы — на вкладке «Шлюз»
	agentsScreen:                          {{"listen", "agents.listen"}},
	agentsScreen + "~" + agentsTabGateway: {{"gateway", "agents.gateway"}, {"upstream", "agents.gateway_upstream"}},
	// журнал настроек: срок хранения и список служебных отметок (2 октября 2026)
	changesScreen: {{"value", "settings.log_keep_days"}, {"marks", "settings_log.marks"}},
	// общее для всех провайдеров: слова закрытых разделов страницы цен и предел
	// ответа модели (1 октября 2026, «Модели и роли»)
	modelsScreen + "~" + modelsTabProviders: {{"words", "prices.skip_sections"}, {"max_output", "models.max_output"}},
	// почта, вкладка «Спам» (2 октября 2026): порог писем у провайдера и слова счёта
	mailScreen + "~" + mailTabSpam: {{"server_votes", "spam.server_votes"}, {"money_words", "mail.money_words"}},
	// «Уведомления», вкладка «Каналы доставки» (2 октября 2026): параметры пушей —
	// адрес для связи, сроки хранения, «тревога висит», срочность по уровням
	notifySettingsScreen: {
		{"subject", "push.subject"}, {"ttl_min", "push.ttl_min"}, {"ttl_alert_min", "push.ttl_alert_min"},
		{"sticky_alert", "push.sticky_alert"}, {"urgency_alert", "push.urgency.alert"},
		{"urgency_warn", "push.urgency.warn"}, {"urgency_info", "push.urgency.info"},
	},
	// «Инструменты» (2 октября 2026): время на коды одного файла и предел размера картинки
	toolsScreen: {{"limit_s", "codes.max_s"}, {"max_mp", "codes.max_mp"}},
	// «Работа Сёмы», вкладка «Экран и связь» (2 октября 2026): сколько ждать клиента
	// и сколько живёт снимок экрана на устройстве
	watchScreen + "~" + workTabScreen: {{"ws_write_s", "ui.ws_write_s"}, {"snapshot_days", "ui.snapshot_days"}, {"list_rows", "ui.list_rows"}, {"prefetch_ms", "ui.prefetch_ms"}, {"redirect_tls", "http.redirect_tls"}, {"slow_sql_ms", "debug.slow_sql_ms"}, {"pprof", "debug.pprof"}},
	watchScreen + "~" + workTabChats: {{"read_steps", "chats.read_steps"}, {"promise_markers", "chats.promise_markers"}, {"claim_markers", "chats.claim_markers"},
		{"assign_confirm", "assign.confirm"}, {"assign_max_steps", "assign.max_steps"}, {"assign_step_turns", "assign.step_turns"},
		{"assign_days", "assign.days"}, {"assign_tracker", "assign.tracker"}, {"assign_tracker_project", "assign.tracker_project"},
		{"assign_tracker_create", "assign.tracker_create_tool"}, {"assign_tracker_done", "assign.tracker_done_tool"},
		{"assign_tracker_cancel", "assign.tracker_cancel_tool"}},
}

func rareFieldsOf(cs []Component) map[string]Field {
	out := map[string]Field{}
	var walk func([]Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			for _, f := range c.Fields {
				out[f.Name] = f
			}
			walk(c.Children)
		}
	}
	walk(cs)
	return out
}

// rareFormOf — форма «Тонкой настройки», где лежит поле: у неё — действие сохранения.
func rareFormOf(cs []Component, field string) *Component {
	for i := range cs {
		for _, f := range cs[i].Fields {
			if cs[i].Type == "form" && f.Name == field {
				return &cs[i]
			}
		}
		if f := rareFormOf(cs[i].Children, field); f != nil {
			return f
		}
	}
	return nil
}

func rareFoldOf(t *testing.T, s *Server, screen string) Component {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("экран %s: %v", screen, err)
	}
	for _, c := range doc.Components {
		if c.ID == rareID {
			return c
		}
	}
	t.Fatalf("на экране %s нет «Тонкой настройки»", screen)
	return Component{}
}

// Ни одно поле прежнего экрана не пропало, лишнего не появилось, и ни одно не
// осталось на самом экране дубликатом: редкое переехало, а не размножилось.
func TestRareFieldsNotLostAndNotDuplicated(t *testing.T) {
	s, _ := testServer(t)
	for screen, want := range rareScreens {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("экран %s: %v", screen, err)
		}
		got := rareFieldsOf(rareBody(t, s, screen))
		onScreen := rareFieldsOf(doc.Components)
		for _, w := range want {
			if _, ok := got[w.Field]; !ok {
				t.Errorf("%s: поле %q пропало из «Тонкой настройки»", screen, w.Field)
			}
			if _, dup := onScreen[w.Field]; dup && w.Field != "max" && w.Field != "value" {
				t.Errorf("%s: поле %q осталось и на самом экране", screen, w.Field)
			}
		}
		if len(got) != len(want) {
			names := []string{}
			for n := range got {
				names = append(names, n)
			}
			sort.Strings(names)
			t.Errorf("%s: в «Тонкой настройке» %d полей, а размечено %d: %v", screen, len(got), len(want), names)
		}
	}
}

// Умолчание на экране — умолчание реестра, а на пустой базе значение равно
// умолчанию у каждой размеченной настройки; подпись раздела говорит «всё по
// умолчанию» и считает параметры.
func TestRareDefaultsOnScreenAreRegistryDefaults(t *testing.T) {
	s, _ := testServer(t)
	for screen, list := range rareScreens {
		fields := rareFieldsOf(rareBody(t, s, screen))
		for _, r := range list {
			def, ok := store.LookupSetting(r.Key)
			if !ok {
				t.Errorf("%s: настройки %s нет в реестре", screen, r.Key)
				continue
			}
			f := fields[r.Field]
			if f.Default == nil {
				t.Errorf("%s: у поля %q нет умолчания", screen, r.Field)
				continue
			}
			want := def.ShownDefault()
			if r.Key == auth.SettingOrigin {
				want = s.auth.OriginDefault() // умолчание адреса входа выводится, а не пусто
			}
			if *f.Default != want {
				t.Errorf("%s: умолчание поля %q на экране %q, а в реестре %q", screen, r.Field, *f.Default, want)
			}
			if f.Value != *f.Default {
				t.Errorf("%s: на пустой базе поле %q показывает %q при умолчании %q", screen, r.Field, f.Value, *f.Default)
			}
		}
		fold := rareFoldOf(t, s, screen)
		if fold.Meta != "всё по умолчанию" || !strings.HasPrefix(fold.Caption, strconv.Itoa(len(list))+" ") {
			t.Errorf("%s: подпись «Тонкой настройки» %q · %q, параметров ждали %d", screen, fold.Caption, fold.Meta, len(list))
		}
	}
}

// otherValue — другое допустимое значение настройки: на единицу от умолчания в
// пределах границ, обратный тумблер или своя строка.
func otherValue(def store.SettingDef, field string) string {
	switch def.Kind {
	case store.SettingFlag:
		return strconv.FormatBool(!def.DefaultFlag())
	case store.SettingPositive, store.SettingCount:
		v := def.DefaultInt() + 1
		if def.Max > 0 && v > def.Max {
			v = def.DefaultInt() - 1
		}
		return strconv.Itoa(v)
	}
	switch def.Key {
	case "push.subject":
		return "https://alt.example.org" // адрес для связи проверяется: слова вместо адреса отвергаются
	case "push.urgency.alert", "push.urgency.warn", "push.urgency.info":
		return "low" // срочность — одно из слов службы, не любая строка
	case auth.SettingOrigin:
		return "https://alt.example:8443"
	case auth.SettingLocalHosts:
		return "alt.example"
	case auth.SettingRPID:
		return "alt.example"
	}
	return "alt-" + field
}

// Каждая размеченная настройка сохраняется действием своей формы, читается
// обратно и на экране, подпись раздела говорит «1 изменён»; возврат умолчания
// возвращает «всё по умолчанию» и умолчание в базе — у каждой, а не у одной.
func TestRareSettingSavesByItsActionAndReturnsToDefault(t *testing.T) {
	s, st := testServer(t)
	for screen, list := range rareScreens {
		for _, r := range list {
			def, _ := store.LookupSetting(r.Key)
			form := rareFormOf(rareBody(t, s, screen), r.Field)
			if form == nil || form.Submit == nil {
				t.Errorf("%s: у поля %q нет формы с действием сохранения", screen, r.Field)
				continue
			}
			save := func(value string) error {
				_, err := s.execute(actionRequest{Action: form.Submit.Action, Target: form.Submit.Target, Screen: screen,
					Params: map[string]string{r.Field: value}})
				return err
			}
			alt := otherValue(def, r.Field)
			if err := save(alt); err != nil {
				t.Errorf("%s: %q = %q не сохранилось: %v", screen, r.Field, alt, err)
				continue
			}
			shown := st.SettingShown(def)
			if def.Kind == store.SettingFlag {
				shown = strconv.FormatBool(st.SettingFlag(def))
			}
			if shown != alt {
				t.Errorf("%s: %q сохранено %q, а читается %q", screen, r.Field, alt, shown)
			}
			if f := rareFieldsOf(rareBody(t, s, screen))[r.Field]; f.Value != alt {
				t.Errorf("%s: экран показывает %q, а сохранено %q", screen, f.Value, alt)
			}
			if fold := rareFoldOf(t, s, screen); fold.Meta != "1 изменён" {
				t.Errorf("%s: после правки %q подпись раздела %q", screen, r.Field, fold.Meta)
			}
			// возврат умолчания тем же жестом: значение умолчания через то же действие
			back := def.ShownDefault()
			if r.Key == auth.SettingOrigin {
				back = s.auth.OriginDefault()
			}
			if err := save(back); err != nil {
				t.Errorf("%s: возврат умолчания %q = %q: %v", screen, r.Field, back, err)
				continue
			}
			if fold := rareFoldOf(t, s, screen); fold.Meta != "всё по умолчанию" {
				t.Errorf("%s: после возврата %q подпись раздела %q", screen, r.Field, fold.Meta)
			}
			if f := rareFieldsOf(rareBody(t, s, screen))[r.Field]; f.Value != back {
				t.Errorf("%s: после возврата экран показывает %q, а умолчание %q", screen, f.Value, back)
			}
		}
	}
}

// Плохое значение отвергается со словами и ничего не пишет; непришедшее поле
// не сбрасывает остальные.
func TestRareSettingRejectsBadValueAndKeepsOthers(t *testing.T) {
	s, st := testServer(t)
	if _, err := s.execute(actionRequest{Action: "set_ssh_limits", Params: map[string]string{"timeout_s": "30"}}); err != nil {
		t.Fatal(err)
	}
	// второе поле не пришло — первое осталось
	if _, err := s.execute(actionRequest{Action: "set_ssh_limits", Params: map[string]string{"max_output": "5000"}}); err != nil {
		t.Fatal(err)
	}
	if s.eng.SSHTimeout() != 30 || s.eng.SSHMaxOutput() != 5000 {
		t.Errorf("непришедшее поле сбросило соседа: %d %d", s.eng.SSHTimeout(), s.eng.SSHMaxOutput())
	}
	for _, bad := range []map[string]string{{"timeout_s": "0"}, {"timeout_s": "много"}, {"timeout_s": ""}, {"confirm_timeout_s": "-5"}, {"timeout_s": "20", "max_output": "x"}} {
		if _, err := s.execute(actionRequest{Action: "set_ssh_limits", Params: bad}); err == nil {
			t.Errorf("%v принято", bad)
		}
	}
	if s.eng.SSHTimeout() != 30 {
		t.Errorf("отвергнутая форма что-то записала: %d", s.eng.SSHTimeout())
	}
	// пустое имя скилла осмотра — отказ
	if _, err := s.execute(actionRequest{Action: "set_night_limits", Params: map[string]string{"skill_linux": "  "}}); err == nil {
		t.Error("пустой скилл осмотра принят")
	}
	// тумблер приходит через `toggles`, и снятый не теряется
	if _, err := s.execute(actionRequest{Action: "set_mcp_login_limits", Params: map[string]string{"client_metadata": "false"}}); err != nil {
		t.Fatal(err)
	}
	if st.Setting("mcp.client_metadata", "") != "0" {
		t.Errorf("тумблер не сохранился: %q", st.Setting("mcp.client_metadata", ""))
	}
}

// ---- реестр: второго числа нет ----

// Читать зарегистрированную настройку «по-старому» (с числом-умолчанием в
// вызове) нельзя нигде в движке, входе и экранах: такой вызов — это второй
// список чисел, который разойдётся с реестром и с подписью «по умолчанию».
// Тест разбирает исходники, а не верит намерениям: см. store/setting_defs.go.
func TestRegisteredSettingsAreReadOnlyThroughRegistry(t *testing.T) {
	_, _ = testServer(t) // реестры движка и входа заполняются при запуске пакетов
	keys := map[string]bool{}
	for _, d := range store.SettingDefs() {
		keys[d.Key] = true
	}
	if len(keys) < 40 {
		t.Fatalf("в реестре %d настроек: часть пакетов не подключилась", len(keys))
	}
	readers := map[string]bool{"settingInt": true, "settingCount": true, "Setting": true}
	for _, dir := range []string{"../engine", "../auth", "."} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		fset := token.NewFileSet()
		parsed := map[string]*ast.File{}
		consts := map[string]string{} // имя константы пакета → строка
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			parsed[f] = file
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								if v, err := strconv.Unquote(lit.Value); err == nil {
									consts[name.Name] = v
								}
							}
						}
					}
				}
			}
		}
		for _, file := range parsed {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !readers[sel.Sel.Name] {
					return true
				}
				key := ""
				switch a := call.Args[0].(type) {
				case *ast.BasicLit:
					key, _ = strconv.Unquote(a.Value)
				case *ast.Ident:
					key = consts[a.Name]
				case *ast.SelectorExpr: // auth.SettingRPID
					key = consts[a.Sel.Name]
				}
				if keys[key] {
					t.Errorf("%s: настройка %s читается с числом-умолчанием в вызове — читать надо через реестр (store.SettingInt и родня)",
						fset.Position(call.Pos()), key)
				}
				return true
			})
		}
	}
}

// ---- разметка и словарь ----

// Строка настройки — другая раскладка той же формы: старый клиент нарисует
// обычную форму, поэтому версия словаря поднята, а нижняя граница нет.
func TestRowsStyleKeepsOldClientsWorking(t *testing.T) {
	if UIDictVersion < 25 {
		t.Errorf("словарь не поднят: %d", UIDictVersion)
	}
	if uiDictMin != 1 {
		t.Errorf("uiDictMin сдвинут до %d: строки настройки ничего не ломают у старого клиента", uiDictMin)
	}
	s, _ := testServer(t)
	doc, err := s.buildScreen(mcpScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var fold *Component
	for i := range doc.Components {
		if doc.Components[i].ID == rareID {
			fold = &doc.Components[i]
		}
	}
	// раздел — обычный ленивый свёрнутый: так его понимает клиент с версией 17
	if fold == nil || fold.Type != "section" || !fold.Collapsed || fold.Lazy == "" {
		t.Fatalf("«Тонкая настройка» устроена не как свёрнутый ленивый раздел: %+v", fold)
	}
	for _, c := range rareBody(t, s, mcpScreen) {
		// форма — обычный `form` с обычными полями; Style «rows» старый клиент не знает и пропустит
		if c.Type != "form" || len(c.Fields) == 0 || c.Style != formRows {
			t.Errorf("форма тонкой настройки: %+v", c)
		}
		for _, f := range c.Fields {
			// «text» — в словаре с первой версии: строка через запятую (поля выжимки)
			if f.Type != "number" && f.Type != "toggle" && f.Type != "text" {
				t.Errorf("поле %q типа %q: словарь клиента такого не знает", f.Name, f.Type)
			}
		}
	}
	// клиент прежней версии пускается и получает экран, а не «Обновите клиент»
	r := httptest.NewRequest("GET", "/ui?screen="+mcpScreen, nil)
	r.Header.Set(uiDictHeader, "24")
	if outdatedClient(r) != nil {
		t.Error("клиента версии 24 не пустили")
	}
}

// Формы «Тонкой настройки» сохраняет та же одна полоса внизу экрана, что и
// остальные: у них нет своих кнопок, а у самого свёрнутого раздела стоит
// признак «внутри есть то, что сохраняет полоса», иначе полосы не было бы на
// экране до раскрытия.
func TestRareFormsAreSavedByTheBar(t *testing.T) {
	s, _ := testServer(t)
	for screen := range rareScreens {
		if fold := rareFoldOf(t, s, screen); !fold.Deferred {
			t.Errorf("%s: у «Тонкой настройки» нет признака Deferred: полосы на экране не будет", screen)
		}
		doc, _ := s.buildScreen(screen, "", "", 0)
		if !toScreen(doc).SaveBar {
			t.Errorf("%s: полосы сохранения на экране нет", screen)
		}
		for _, form := range screenForms(rareBody(t, s, screen)) {
			if !form.Deferred || form.Submit == nil || len(form.Actions) != 0 || form.Style != formRows {
				t.Errorf("%s · форма %q: %+v", screen, form.ID, form)
			}
		}
	}
}
