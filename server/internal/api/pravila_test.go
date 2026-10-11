package api

import (
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Экран правил: у каждого инструмента MCP и у каждого сервера по SSH — три
// ответа, «Разрешить», «Спросить», «Запретить». Макет — design/pravila.

// rulesMCPFix — сервер MCP с инструментами, часть из которых решена.
func rulesMCPFix(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	tools := []string{"task_list", "task_get", "task_create", "task_update", "task_group_list", "project_list",
		"checklist_item_check", "task_brand_new", "whoami"}
	info := make([]store.MCPTool, 0, len(tools))
	for _, n := range tools {
		info = append(info, store.MCPTool{Name: n, Desc: "описание " + n})
	}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: "http://127.0.0.1:1/mcp", Enabled: true,
		Tools: tools, ToolInfo: info}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPRules("sing", map[string]string{
		"task_list": store.RuleAllow, "task_get": store.RuleAllow, "task_create": store.RuleAsk,
		"task_update": store.RuleAsk, "task_group_list": store.RuleAllow, "project_list": store.RuleAllow,
		"checklist_item_check": store.RuleDeny, "whoami": store.RuleDeny,
		// task_brand_new — новый: решения про него нет
	}, store.RuleDeny); err != nil {
		t.Fatal(err)
	}
	return s, st
}

// mcpRulesDoc — вкладка «Правила» открытого сервера.
func mcpRulesDoc(t *testing.T, s *Server, filter, search string) *Document {
	t.Helper()
	doc, err := s.buildScreen(entityAddr(mcpScreen, "sing", mcpTabRules), filter, search, 0)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// rulesForms — формы правил по группам: их названия и поля.
func rulesForms(doc *Document) []*Component {
	var out []*Component
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.Type == "form" && strings.HasPrefix(c.ID, "mcp-rules-sing-") {
				out = append(out, c)
			}
		})
	}
	return out
}

func toolFields(forms []*Component) map[string]Field {
	out := map[string]Field{}
	for _, f := range forms {
		for _, fld := range f.Fields {
			if name, ok := strings.CutPrefix(fld.Name, "tool:"); ok {
				out[name] = fld
			}
		}
	}
	return out
}

// Раздел «Правила · сервер · N инструментов»: счётчики, ответ для нового,
// группы по приставке имени и у каждого инструмента переключатель на три
// положения, цвет только у выбранного.
func TestMCPRulesScreenShowsToolsByGroups(t *testing.T) {
	s, _ := rulesMCPFix(t)
	doc := mcpRulesDoc(t, s, "", "")
	var head *Component
	for i := range doc.Components {
		if doc.Components[i].Type == "section" && strings.HasPrefix(doc.Components[i].Title, "Правила · sing") {
			head = &doc.Components[i]
		}
	}
	if head == nil || !strings.Contains(head.Title, "9 инструментов") {
		t.Fatalf("заголовка раздела правил нет или без счёта: %+v", head)
	}
	counts := ""
	for _, b := range head.Children[0].Badges {
		counts += b.Text + "|"
	}
	for _, want := range []string{"Разрешить · 4", "Спросить · 2", "Запретить · 3", "новых · 1"} {
		if !strings.Contains(counts, want) {
			t.Errorf("в счётчиках нет %q: %s", want, counts)
		}
	}
	forms := rulesForms(doc)
	titles := ""
	for _, f := range forms {
		titles += f.Title + " | "
	}
	for _, want := range []string{"Задачи · ", "Группы задач · 1", "Проекты · 1", "Чек-листы · 1", "Прочее · 1"} {
		if !strings.Contains(titles, want) {
			t.Errorf("нет группы %q среди: %s", want, titles)
		}
	}
	fields := toolFields(forms)
	if len(fields) != 9 {
		t.Fatalf("инструментов на экране %d, а их девять", len(fields))
	}
	for name, f := range fields {
		if f.Type != "segments" || len(f.Options) != 3 || len(f.Tones) != 3 {
			t.Errorf("%s: переключатель не на три положения с цветами: %+v", name, f)
		}
	}
	if f := fields["task_get"]; f.Value != "Разрешить" {
		t.Errorf("task_get: %q", f.Value)
	}
	if f := fields["task_create"]; f.Value != "Спросить" {
		t.Errorf("task_create: %q", f.Value)
	}
	if f := fields["whoami"]; f.Value != "Запретить" {
		t.Errorf("whoami: %q", f.Value)
	}
	// новый инструмент: ничего не выбрано, а подсказка называет действующий ответ
	nf := fields["task_brand_new"]
	if nf.Value != "" || !strings.Contains(nf.Hint, "новый") || !strings.Contains(nf.Hint, "Запретить") {
		t.Errorf("новый инструмент: %+v", nf)
	}
	// формы сохраняет нижняя полоса, а не своя кнопка
	for _, f := range forms {
		if !f.Deferred {
			t.Errorf("форма %q не отдана нижней полосе", f.Title)
		}
	}
	// ответ для нового инструмента
	var def *Field
	for i := range doc.Components {
		if doc.Components[i].ID == "mcp-rules-default-sing" {
			def = &doc.Components[i].Fields[0]
		}
	}
	if def == nil || def.Value != "Запретить" {
		t.Errorf("ответ для нового инструмента: %+v", def)
	}
	if len(doc.Filters) != 5 || doc.Filters[4].ID != "new" {
		t.Errorf("фильтры: %+v", doc.Filters)
	}
}

// Фильтр по состоянию и поиск отбирают инструменты: на экране только то, что
// подошло.
func TestMCPRulesFilterAndSearch(t *testing.T) {
	s, _ := rulesMCPFix(t)
	names := func(filter, search string) string {
		var out []string
		for n := range toolFields(rulesForms(mcpRulesDoc(t, s, filter, search))) {
			out = append(out, n)
		}
		return strings.Join(sortedStrings(out), ",")
	}
	if got := names("ask", ""); got != "task_create,task_update" {
		t.Errorf("фильтр «Спросить»: %s", got)
	}
	if got := names("deny", ""); !strings.Contains(got, "whoami") || !strings.Contains(got, "task_brand_new") || strings.Contains(got, "task_list") {
		t.Errorf("фильтр «Запретить» (новый запрещён по умолчанию): %s", got)
	}
	if got := names("new", ""); got != "task_brand_new" {
		t.Errorf("фильтр «Новые»: %s", got)
	}
	if got := names("", "checklist"); got != "checklist_item_check" {
		t.Errorf("поиск: %s", got)
	}
	if got := names("allow", "group"); got != "task_group_list" {
		t.Errorf("фильтр и поиск вместе: %s", got)
	}
	doc := mcpRulesDoc(t, s, "ask", "")
	if doc.ActiveFilter != "ask" {
		t.Errorf("активный фильтр: %q", doc.ActiveFilter)
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Сохранение формы: переключатель ставит состояние, а решение по новому
// инструменту — любой выбор, даже «Запретить».
func TestSaveMCPRulesSwitchAndNewTool(t *testing.T) {
	s, st := rulesMCPFix(t)
	target := mcpRulesTarget("sing", "Задачи", "", "")
	if _, err := s.execute(actionRequest{Action: mcpRulesSaving, Target: target, Params: map[string]string{
		"tool:task_create": "Разрешить", // было «Спросить»
		"tool:task_get":    "Разрешить", // не менялось
		"tool:task_list":   "Запретить", // было «Разрешить»
	}}); err != nil {
		t.Fatal(err)
	}
	m, _ := st.MCPServer("sing")
	for tool, want := range map[string]string{"task_create": store.RuleAllow, "task_get": store.RuleAllow, "task_list": store.RuleDeny, "task_update": store.RuleAsk} {
		if m.Rules[tool] != want {
			t.Errorf("%s: %q, ждали %q", tool, m.Rules[tool], want)
		}
	}
	// новый инструмент решается выбором — хоть «Запретить», и перестаёт быть новым
	if _, err := s.execute(actionRequest{Action: mcpRulesSaving, Target: target, Params: map[string]string{
		"tool:task_brand_new": "Запретить"}}); err != nil {
		t.Fatal(err)
	}
	m, _ = st.MCPServer("sing")
	if p, decided := engine.MCPRuleOf(m, "task_brand_new"); !decided || p != store.RuleDeny {
		t.Errorf("решение по новому инструменту: %q decided=%v", p, decided)
	}
	// ответ для нового инструмента
	if _, err := s.execute(actionRequest{Action: mcpRulesSaving, Target: mcpRulesTarget("sing", "", "", ""),
		Params: map[string]string{"default": "Спросить"}}); err != nil {
		t.Fatal(err)
	}
	if m, _ = st.MCPServer("sing"); m.RuleDefault != store.RuleAsk {
		t.Errorf("ответ для нового: %q", m.RuleDefault)
	}
	// чужое слово вместо ответа — отказ, ничего не записано
	if _, err := s.execute(actionRequest{Action: mcpRulesSaving, Target: target, Params: map[string]string{
		"tool:task_get": "можно"}}); err == nil {
		t.Error("ответ не из трёх принят")
	}
}

// «Всем в группе» применяется к инструментам группы, которые не трогали
// отдельно, и к новым, у которых ничего не выбрано; тронутые отдельно
// остаются при своём выборе.
func TestSaveMCPRulesBulkForGroup(t *testing.T) {
	s, st := rulesMCPFix(t)
	target := mcpRulesTarget("sing", "Задачи", "", "")
	params := map[string]string{
		"bulk":             "Запретить",
		"tool:task_list":   "Разрешить", // не менялось
		"tool:task_get":    "Разрешить", // не менялось
		"tool:task_create": "Разрешить", // человек поменял отдельно: было «Спросить»
		"tool:task_update": "Спросить",  // не менялось
		// task_brand_new — новый, ничего не выбрано
	}
	if _, err := s.execute(actionRequest{Action: mcpRulesSaving, Target: target, Params: params}); err != nil {
		t.Fatal(err)
	}
	m, _ := st.MCPServer("sing")
	want := map[string]string{
		"task_list": store.RuleDeny, "task_get": store.RuleDeny, "task_update": store.RuleDeny, // не трогали — групповой ответ
		"task_create":    store.RuleAllow, // трогали — остался выбор человека
		"task_brand_new": store.RuleDeny,  // новый — решён групповым ответом
		// другие группы не затронуты
		"project_list": store.RuleAllow, "task_group_list": store.RuleAllow, "whoami": store.RuleDeny,
	}
	for tool, w := range want {
		if p, _ := engine.MCPRuleOf(m, tool); p != w {
			t.Errorf("%s: %q, ждали %q", tool, p, w)
		}
	}
	if _, decided := engine.MCPRuleOf(m, "task_brand_new"); !decided {
		t.Error("новый инструмент остался нерешённым после «всем в группе»")
	}
	// групповой ответ касается только показанного: при фильтре «Спросить» скрытые не трогаются
	s2, st2 := rulesMCPFix(t)
	tgt := mcpRulesTarget("sing", "Задачи", "ask", "")
	if _, err := s2.execute(actionRequest{Action: mcpRulesSaving, Target: tgt, Params: map[string]string{
		"bulk": "Разрешить", "tool:task_create": "Спросить", "tool:task_update": "Спросить"}}); err != nil {
		t.Fatal(err)
	}
	m2, _ := st2.MCPServer("sing")
	if m2.Rules["task_create"] != store.RuleAllow || m2.Rules["task_update"] != store.RuleAllow {
		t.Errorf("показанное не изменилось: %v", m2.Rules)
	}
	if m2.Rules["task_list"] != store.RuleAllow || m2.Rules["task_brand_new"] != "" {
		t.Errorf("групповой ответ затронул скрытое фильтром: %v", m2.Rules)
	}
}

// Неизвестная приставка — «Прочее»; названия групп правятся и помнятся.
func TestMCPGroupsConfigurable(t *testing.T) {
	st := openTestStoreAPI(t)
	if g := mcpGroupOf("whoami", mcpGroups(st, "x")); g != "Прочее" {
		t.Errorf("неизвестная приставка: %q", g)
	}
	if g := mcpGroupOf("task_group_update", mcpGroups(st, "x")); g != "Группы задач" {
		t.Errorf("самая длинная приставка не победила: %q", g)
	}
	if err := st.SetSetting(mcpGroupsKey+"x", "wiki = Вики\nwiki_page = Страницы"); err != nil {
		t.Fatal(err)
	}
	defs := mcpGroups(st, "x")
	if g := mcpGroupOf("wiki_page_get", defs); g != "Страницы" {
		t.Errorf("своя группа: %q", g)
	}
	if g := mcpGroupOf("task_list", defs); g != "Прочее" {
		t.Errorf("чужие приставки остались: %q", g)
	}
}

func openTestStoreAPI(t *testing.T) *store.Store {
	t.Helper()
	_, st := testServer(t)
	return st
}

// ---- SSH ----

// rulesSSHFix — два сервера: у «первый» правила есть, у «пустой» нет.
func rulesSSHFix(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	for _, h := range []*store.Host{
		{Name: "первый", Addr: "10.0.0.1", User: "u", Key: "ssh:1", Kind: store.HostLinux},
		{Name: "пустой", Addr: "10.0.0.2", User: "u", Key: "ssh:2", Kind: store.HostLinux},
	} {
		if err := st.SaveHost(h); err != nil {
			t.Fatal(err)
		}
	}
	for kind, list := range map[string][]string{
		store.RuleAllow: {"uptime", "df"}, store.RuleAsk: {"systemctl restart"}, store.RuleDeny: {"rm -rf", "reboot"},
	} {
		for _, p := range list {
			if err := st.AddHostRule("первый", kind, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, st
}

// rulesTabDoc — вкладка «Правила» сервера по адресу экрана (после 1 октября
// 2026 правила — часть экрана сервера: settings:servers@имя~rules).
func rulesTabDoc(t *testing.T, s *Server, screen string) *Document {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func findByType(doc *Document, typ string) *Component {
	var found *Component
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.Type == typ && found == nil {
				found = c
			}
		})
	}
	return found
}

// Три колонки образцов у сервера с правилами: в каждой образцы с «×» и
// поле «добавить».
func TestSSHRulesTabThreeColumns(t *testing.T) {
	s, _ := rulesSSHFix(t)
	doc := rulesTabDoc(t, s, rulesAddr("первый", ""))
	pat := findByType(doc, "patterns")
	if pat == nil || len(pat.Children) != 3 {
		t.Fatalf("три колонки не нарисованы: %+v", pat)
	}
	want := []struct {
		title string
		n     int
		level string
	}{{"Разрешить", 2, LevelOK}, {"Спросить", 1, LevelWarn}, {"Запретить", 2, LevelAlert}}
	for i, w := range want {
		col := pat.Children[i]
		if col.Title != w.title || len(col.Children) != w.n || col.Level != w.level {
			t.Errorf("колонка %d: %q, образцов %d, цвет %q", i, col.Title, len(col.Children), col.Level)
		}
		if hasAction(col.Actions, "add_host_rule") == nil {
			t.Errorf("в колонке %q нет «Добавить»", col.Title)
		}
		for _, chip := range col.Children {
			if hasAction(chip.Actions, "delete_host_rule") == nil {
				t.Errorf("у образца %q нет «×»", chip.Text)
			}
		}
	}
	// у сервера с правилами пустого состояния нет
	for i := range doc.Components {
		if strings.HasPrefix(doc.Components[i].ID, "rules-empty-") {
			t.Error("у сервера с правилами показано «нет правил»")
		}
	}
}

// У сервера без правил экран так и говорит: любая команда запрещена, и
// предлагает наборы.
func TestSSHRulesTabEmptyHostSaysSo(t *testing.T) {
	s, _ := rulesSSHFix(t)
	doc := rulesTabDoc(t, s, rulesAddr("пустой", ""))
	var empty *Component
	for i := range doc.Components {
		if doc.Components[i].ID == "rules-empty-пустой" {
			empty = &doc.Components[i]
		}
	}
	if empty == nil || !strings.Contains(empty.Subtitle, "любая команда запрещена") {
		t.Fatalf("у сервера без правил не сказано, что всё запрещено: %+v", empty)
	}
	sets := map[string]bool{}
	for _, a := range append(append([]Action{}, empty.Actions...), empty.Menu...) {
		if a.Action == "take_rule_set" {
			sets[a.Target] = true
		}
	}
	if !sets["пустой~linux"] || !sets["пустой~routeros"] {
		t.Errorf("нет кнопок наборов: %v", sets)
	}
	// вкладка несёт счётчик «нет» до захода в неё, а строка в списке машин
	// говорит, что любая команда там запрещена
	found := false
	for _, tab := range doc.Subtabs {
		if tab.Title == "Правила" && tab.Badge == "нет" && tab.Level == LevelWarn {
			found = true
		}
	}
	if !found {
		t.Errorf("счётчика «нет» у вкладки «Правила» нет: %+v", doc.Subtabs)
	}
	list, err := s.buildScreen(serversScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var caption string
	for _, c := range list.Components {
		if c.Type == "section" {
			for _, r := range c.Children {
				if r.ID == "host-пустой" {
					caption = r.Caption
				}
			}
		}
	}
	if !strings.Contains(caption, "нет правил") {
		t.Errorf("строка сервера без правил не говорит, что команды запрещены: %q", caption)
	}
}

// Правки колонок действиями сервера: добавить, убрать, взять набор.
func TestSSHRuleActions(t *testing.T) {
	s, st := rulesSSHFix(t)
	back, err := s.execute(actionRequest{Action: "add_host_rule", Target: "первый~" + store.RuleAllow, Params: map[string]string{"value": "free"}})
	if err != nil || back != rulesAddr("первый", "") {
		t.Fatalf("добавить: %v %q", err, back)
	}
	if got, _ := s.eng.Classify("первый", "free -m"); got != engine.PolicyAllow {
		t.Errorf("добавленное правило не действует: %q", got)
	}
	var id int64
	for _, r := range st.HostRules("первый") {
		if r.Pattern == "free" {
			id = r.ID
		}
	}
	if _, err := s.execute(actionRequest{Action: "delete_host_rule", Target: strconv.FormatInt(id, 10)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.eng.Classify("первый", "free -m"); got != engine.PolicyDeny {
		t.Errorf("убранное правило действует: %q", got)
	}
	if _, err := s.execute(actionRequest{Action: "take_rule_set", Target: "пустой~linux"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.eng.Classify("пустой", "uptime"); got != engine.PolicyAllow {
		t.Errorf("после набора «Linux» на пустом сервере uptime получил %q", got)
	}
	if got, _ := s.eng.Classify("первый", "docker ps"); got != engine.PolicyDeny {
		t.Errorf("набор, взятый другим сервером, просочился: %q", got)
	}
	if _, err := s.execute(actionRequest{Action: "add_host_rule", Target: "первый~можно", Params: map[string]string{"value": "x"}}); err == nil {
		t.Error("ответ не из трёх принят")
	}
}

// «Проверить команду» отвечает тем же решением, что и настоящий запрос, и
// подсвечивает образец, по которому решено.
func TestSSHRulesTesterShowsVerdictAndHit(t *testing.T) {
	s, _ := rulesSSHFix(t)
	back, err := s.execute(actionRequest{Action: "check_host_rule", Target: "первый", Params: map[string]string{"cmd": "sudo systemctl restart nginx && echo ok"}})
	if err != nil {
		t.Fatal(err)
	}
	doc := rulesTabDoc(t, s, back)
	var verdict *Component
	for i := range doc.Components {
		if doc.Components[i].ID == "rules-verdict" {
			verdict = &doc.Components[i]
		}
	}
	if verdict == nil || verdict.Badges[0].Text != "Спросить" || !strings.Contains(verdict.Caption, "systemctl restart") {
		t.Fatalf("ответ проверки: %+v", verdict)
	}
	hit := ""
	for _, col := range findByType(doc, "patterns").Children {
		for _, chip := range col.Children {
			if chip.Style == "hit" {
				hit += chip.Text + ";"
			}
		}
	}
	if hit != "systemctl restart;" {
		t.Errorf("подсвечено: %q", hit)
	}
	// команда без правила: проверка предлагает завести его
	back, _ = s.execute(actionRequest{Action: "check_host_rule", Target: "первый", Params: map[string]string{"cmd": "docker restart web"}})
	doc = rulesTabDoc(t, s, back)
	verdict = nil
	for i := range doc.Components {
		if doc.Components[i].ID == "rules-verdict" {
			verdict = &doc.Components[i]
		}
	}
	if verdict == nil || verdict.Badges[0].Text != "Запретить" {
		t.Fatalf("ответ для команды без правила: %+v", verdict)
	}
	acts := map[string]bool{}
	for _, a := range append(append([]Action{}, verdict.Actions...), verdict.Menu...) {
		acts[a.Label] = true
	}
	if !acts["Спрашивать для неё"] || !acts["Разрешить её"] {
		t.Errorf("у команды без правила нет кнопок заведения правила: %v", acts)
	}
	// у запрета по правилу кнопок нет
	back, _ = s.execute(actionRequest{Action: "check_host_rule", Target: "первый", Params: map[string]string{"cmd": "rm -rf /tmp/x"}})
	doc = rulesTabDoc(t, s, back)
	for i := range doc.Components {
		if doc.Components[i].ID == "rules-verdict" && (len(doc.Components[i].Actions) > 0 || len(doc.Components[i].Menu) > 0) {
			t.Errorf("у запрета по правилу есть кнопки: %+v", doc.Components[i])
		}
	}
}

// Карточка команды без правила в чате: две кнопки. У запрета по правилу
// карточка кнопок не имеет.
func TestChatCardNoRuleHasTwoButtons(t *testing.T) {
	s, st := rulesSSHFix(t)
	id, _, err := st.AddSSHNoRule(&store.SSHNoRule{Host: "первый", Cmd: "docker restart web", CanAllow: true, Why: "ни одно правило не подошло"})
	if err != nil {
		t.Fatal(err)
	}
	card := engine.ToolCard{Title: "первый · docker restart web",
		SSH: &engine.SSHCard{Host: "первый", Cmd: "docker restart web", Status: "norule", NoRuleID: id}}
	c := s.sshCardComponent(card, "10:00")
	got := map[string]bool{}
	for _, a := range c.Actions {
		got[a.Action] = true
	}
	if !got["rule_ask"] || !got["rule_allow"] {
		t.Fatalf("у карточки «нет правила» нет двух кнопок: %+v", c.Actions)
	}
	if !strings.Contains(c.Caption, "нет правила") {
		t.Errorf("подпись: %q", c.Caption)
	}
	// команду с sudo разрешить нельзя — остаётся одна кнопка
	id2, _, _ := st.AddSSHNoRule(&store.SSHNoRule{Host: "первый", Cmd: "sudo docker restart web", CanAllow: false})
	c2 := s.sshCardComponent(engine.ToolCard{SSH: &engine.SSHCard{Host: "первый", Cmd: "sudo docker restart web", Status: "norule", NoRuleID: id2}}, "10:00")
	for _, a := range c2.Actions {
		if a.Action == "rule_allow" {
			t.Error("у sudo есть «Разрешить её»")
		}
	}
	// запрет по правилу: карточка отказа без кнопок
	c3 := s.sshCardComponent(engine.ToolCard{Err: "запрещено", SSH: &engine.SSHCard{Host: "первый", Cmd: "rm -rf /", Status: "denied"}}, "10:00")
	if len(c3.Actions) != 0 {
		t.Errorf("у запрета по правилу есть кнопки: %+v", c3.Actions)
	}
}

// Новый тип компонента объявлен в словаре клиента и переводится в разметку.
func TestPatternsTypeKnownToClient(t *testing.T) {
	умеет, _ := dictionaryTypes(web.Dictionary())
	if !умеет["patterns"] {
		t.Error("клиент не объявил тип patterns")
	}
	if б := toBlock(Component{Type: "patterns"}); б.Kind != "образцы" {
		t.Errorf("patterns перевёлся в %q", б.Kind)
	}
	if UIDictVersion < 23 {
		t.Errorf("словарь не поднят: %d", UIDictVersion)
	}
}

// Пара «Разрешить» и «Спросить», которая молча ничего не делала бы, не
// принимается экраном: человек получает объяснение, что сузить.
func TestAddHostRuleRefusesSilentConflict(t *testing.T) {
	s, st := rulesSSHFix(t)
	if _, err := s.execute(actionRequest{Action: "add_host_rule", Target: "первый~" + store.RuleAllow, Params: map[string]string{"value": "docker"}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.execute(actionRequest{Action: "add_host_rule", Target: "первый~" + store.RuleAsk, Params: map[string]string{"value": "docker rm"}})
	if err == nil || !strings.Contains(err.Error(), "не подействует") {
		t.Fatalf("бессильное «Спросить» принято: %v", err)
	}
	for _, r := range st.HostRules("первый") {
		if r.Pattern == "docker rm" {
			t.Fatal("отклонённое правило всё же записано")
		}
	}
}
