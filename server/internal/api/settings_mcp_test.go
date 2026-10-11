package api

// Раздел «Интеграции» и экран сервера MCP (2 октября 2026): у каждого сервера
// свой экран, список — тонкие строки, «Добавить сервер MCP» — свой экран.
// Проверяется сборка документа и действия; настоящий Chrome — в
// browser_mcp_test.go.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

var mcpServerNames = []string{"sing", "ключ", "код", "wiki"}

func mcpTabNames(doc *Document) string {
	var titles []string
	for _, f := range doc.Subtabs {
		titles = append(titles, f.Title)
	}
	return strings.Join(titles, "|")
}

// У экрана сервера три вкладки, и каждая собирается по своему адресу: адрес
// вкладки — выбранная внутренняя вкладка, раздел в колонке — «Интеграции», над
// содержимым «‹ Интеграции», а закреплённая строка называет сервер.
func TestMCPServerScreenBuildsEveryTab(t *testing.T) {
	s, _ := mcpScreensFix(t)
	for _, name := range mcpServerNames {
		for _, tab := range []string{mcpTabOverview, mcpTabRules, mcpTabLog} {
			screen := entityAddr(mcpScreen, name, tab)
			doc := serverDoc(t, s, screen)
			if doc.Screen != screen || doc.ActiveSubtab != screen {
				t.Errorf("%s: экран %q, выбранная вкладка %q", screen, doc.Screen, doc.ActiveSubtab)
			}
			if doc.ActivePanel != mcpScreen {
				t.Errorf("%s: в колонке разделов выбран %q", screen, doc.ActivePanel)
			}
			if got := mcpTabNames(doc); got != "Обзор|Правила|Журнал" {
				t.Errorf("%s: вкладки %v", screen, got)
			}
			if doc.Heading != name {
				t.Errorf("%s: заголовок %q", screen, doc.Heading)
			}
			if len(doc.Components) < 3 || doc.Components[0].Type != "crumb" || doc.Components[0].Actions[0].Label != "‹ Интеграции" ||
				doc.Components[0].Actions[0].Target != mcpScreen {
				t.Fatalf("%s: над содержимым нет «‹ Интеграции»: %+v", screen, doc.Components[:1])
			}
			bar := doc.Components[1]
			if bar.Type != "section" || bar.Style != "sticky" || len(bar.Children) != 1 || bar.Children[0].Subtitle != name {
				t.Fatalf("%s: закреплённой строки с именем нет: %+v", screen, bar)
			}
			row := bar.Children[0]
			if len(row.Actions) != 1 || len(row.Selects) != 1 {
				t.Errorf("%s: в шапке одна кнопка и переключатель, а у строки %d действий и %d списков", screen, len(row.Actions), len(row.Selects))
			}
			if len(row.Menu) == 0 || row.Avatar == "" || row.Caption == "" {
				t.Errorf("%s: у шапки нет «···», плитки или фактов: %+v", screen, row)
			}
		}
	}
	// переключатель ведёт к соседу на той же вкладке
	back, err := s.execute(actionRequest{Action: "entity_goto", Target: mcpScreen + "~" + mcpTabRules, Params: map[string]string{"value": "код"}})
	if err != nil || back != entityAddr(mcpScreen, "код", mcpTabRules) {
		t.Errorf("переключатель: %q (%v)", back, err)
	}
	// счётчики вкладок: на «Обзоре» ждут два вызова sing, у «Правил» — три новых инструмента
	doc := serverDoc(t, s, entityAddr(mcpScreen, "sing", ""))
	if doc.Subtabs[0].Badge != "1 ждут" || doc.Subtabs[0].Level != LevelAlert {
		t.Errorf("вкладка «Обзор»: %+v", doc.Subtabs[0])
	}
	if doc.Subtabs[1].Badge != "3 новых" || doc.Subtabs[1].Level != LevelWarn {
		t.Errorf("вкладка «Правила»: %+v", doc.Subtabs[1])
	}
	if got := serverDoc(t, s, entityAddr(mcpScreen, "код", "")).Subtabs[1]; got.Badge != "7" {
		t.Errorf("вкладка «Правила» у сервера без новых: %+v", got)
	}
}

// Сервера, которого нет, — не ошибка, а объяснение с дорогой назад.
func TestMCPServerScreenOfMissingServer(t *testing.T) {
	s, _ := mcpScreensFix(t)
	doc := serverDoc(t, s, entityAddr(mcpScreen, "нет-такого", mcpTabRules))
	if doc.Components[0].Type != "crumb" || findByType(doc, "row") == nil {
		t.Fatalf("у экрана несуществующего сервера нет ни пути назад, ни объяснения: %+v", doc.Components)
	}
}

// Список: тонкие строки. Не больше двух капсул, одна фраза, одна видимая
// кнопка и «···», вся строка ведёт на экран сервера; сверху полка «Ждут вас»,
// внизу «Тонкая настройка», а за ней — ленивый общий журнал последним.
func TestMCPListRowsAreThin(t *testing.T) {
	s, _ := mcpScreensFix(t)
	doc := serverDoc(t, s, mcpScreen)
	var servers *Component
	for i := range doc.Components {
		if doc.Components[i].Type == "section" && strings.HasPrefix(doc.Components[i].Title, "Серверы MCP") {
			servers = &doc.Components[i]
		}
	}
	if servers == nil || len(servers.Children) != 4 || servers.Title != "Серверы MCP · 4" {
		t.Fatalf("серверов в списке: %+v", servers)
	}
	byName := map[string]Component{}
	for _, row := range servers.Children {
		byName[row.Subtitle] = row
		if len(row.Badges) > 2 {
			t.Errorf("%s: капсул %d, а не больше двух", row.Subtitle, len(row.Badges))
		}
		buttons, gotos := 0, 0
		for _, a := range row.Actions {
			if a.Action == "$goto" {
				gotos++
				if a.Target != entityAddr(mcpScreen, row.Subtitle, "") {
					t.Errorf("%s: строка ведёт на %q", row.Subtitle, a.Target)
				}
				continue
			}
			buttons++
		}
		if buttons != 1 || gotos != 1 {
			t.Errorf("%s: видимых кнопок %d и переходов %d, а нужны одна и один", row.Subtitle, buttons, gotos)
		}
		if len(row.Menu) == 0 || row.Avatar == "" || row.Caption == "" || row.Toggle != "" || len(row.Facts) != 0 {
			t.Errorf("%s: нет «···» или плитки, а есть тумблер и факты: %+v", row.Subtitle, row)
		}
		// «В чате», вход и подсказки в тонкую строку не едут
		for _, a := range row.Menu {
			if a.Action == "toggle_mcp_chat" || a.Action == "forget_mcp_hints" || a.Action == "mcp_logout" || a.Action == "delete_mcp" {
				t.Errorf("%s: в меню строки лишнее действие %q", row.Subtitle, a.Action)
			}
		}
	}
	// кнопка по состоянию: проверить, «Войти» у ждущего входа
	if a := byName["sing"].Actions[1]; a.Action != "check_mcp" || a.Label != "Проверить" {
		t.Errorf("sing: %+v", a)
	}
	if a := byName["wiki"].Actions[1]; a.Action != "mcp_login" || a.Label != "Войти" || a.Style != "primary" {
		t.Errorf("wiki: %+v", a)
	}
	if b := byName["wiki"].Badges; len(b) == 0 || b[0].Text != "нужен вход" || b[0].Level != LevelWarn {
		t.Errorf("wiki: капсулы %+v", b)
	}
	if b := byName["sing"].Badges; len(b) != 2 || b[0].Text != "на связи" || b[1].Text != "50 инструментов" {
		t.Errorf("sing: капсулы %+v", b)
	}
	// сервер жителя машины: адреса нет, канал назван словами
	if c := byName["код"].Caption; !strings.Contains(c, "агент «мак»") || !strings.Contains(c, "шифрованный канал") {
		t.Errorf("код: фраза %q", c)
	}
	for _, want := range []string{"вход OAuth выполнен", "правила:", "36 вызовов за сутки"} {
		if c := byName["sing"].Caption; !strings.Contains(c, want) {
			t.Errorf("sing: в фразе %q нет %q", c, want)
		}
	}
	if c := byName["ключ"].Caption; !strings.Contains(c, "ключ «mcp-key»") {
		t.Errorf("ключ: фраза %q", c)
	}

	// полка: два вызова на кнопке и новые инструменты
	if doc.Components[0].Type != "shelf" || doc.Components[0].Title != "Ждут вас" {
		t.Fatalf("полка должна стоять первой: %+v", doc.Components[0])
	}
	text := ""
	for _, c := range doc.Components[0].Children {
		walkthrough(&c, func(x *Component) {
			text += x.Subtitle + "|"
			for _, a := range append(append([]Action{}, x.Actions...), x.Menu...) {
				text += a.Action + ":" + a.Target + ","
			}
		})
	}
	for _, want := range []string{"task_create · sing", "page_update · ключ", "approve_mcp", "reject_mcp", "«sing»: 3 новых инструмента ждут решения",
		"$goto:" + entityAddr(mcpScreen, "sing", mcpTabRules)} {
		if !strings.Contains(text, want) {
			t.Errorf("в полке нет %q: %s", want, text)
		}
	}

	// шапка: одна кнопка на экран добавления, вкладок у раздела нет
	if len(doc.ToolbarActions) != 1 || doc.ToolbarActions[0].Label != "Добавить сервер MCP" ||
		doc.ToolbarActions[0].Target != entityAddr(mcpScreen, mcpNew, "") || doc.ToolbarActions[0].Style != "primary" {
		t.Errorf("шапка раздела: %+v", doc.ToolbarActions)
	}
	if len(doc.Subtabs) != 0 {
		t.Errorf("у раздела не должно быть вкладок: %+v", doc.Subtabs)
	}
	// формы добавления под списком больше нет
	if docByID(doc, "mcp-new") != nil {
		t.Error("форма «Добавить сервер MCP» осталась под списком")
	}
	// порядок: «Тонкая настройка», а за ней — ленивый журнал последним
	n := len(doc.Components)
	if doc.Components[n-1].Lazy != mcpLogPart || doc.Components[n-1].Tier != tierTail || !doc.Components[n-1].Collapsed {
		t.Errorf("последним должен стоять ленивый журнал: %+v", doc.Components[n-1])
	}
	if doc.Components[n-2].ID != rareID {
		t.Errorf("перед журналом должна стоять «Тонкая настройка»: %+v", doc.Components[n-2])
	}
}

// Пустой список объясняет себя и ведёт к «Добавить сервер MCP».
func TestMCPListEmptyExplainsItself(t *testing.T) {
	s, _ := testServer(t)
	doc := serverDoc(t, s, mcpScreen)
	var empty *Component
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.Style == "empty" {
				empty = c
			}
		})
	}
	if empty == nil || !strings.Contains(empty.Subtitle, "Серверов MCP пока нет") || len(empty.Actions) != 1 ||
		empty.Actions[0].Target != entityAddr(mcpScreen, mcpNew, "") {
		t.Errorf("пустое состояние: %+v", empty)
	}
	if !strings.Contains(doc.Subheading, "Ни одного сервера MCP") {
		t.Errorf("подпись раздела: %q", doc.Subheading)
	}
}

// «Обзор»: адрес, состояние, чем входит, вход, «В чате», подсказки; «Опасное»
// — последним, у каждой кнопки вопрос.
func TestMCPOverviewOAuthLoggedIn(t *testing.T) {
	s, _ := mcpScreensFix(t)
	doc := serverDoc(t, s, entityAddr(mcpScreen, "sing", ""))
	for id, want := range map[string]string{
		"mcp-addr":  "https://mcp.planner-app.example/mcp",
		"mcp-state": "проверен",
		"mcp-auth":  "Вход OAuth",
		"mcp-login": "выполнен, действует до",
	} {
		c := docByID(doc, id)
		if c == nil || !strings.Contains(c.Caption, want) {
			t.Errorf("%s: %+v, ждали «%s»", id, c, want)
		}
	}
	if c := docByID(doc, "mcp-login"); c != nil {
		if !strings.Contains(c.Caption, "права: tasks:read tasks:write") || len(c.Actions) != 0 || len(c.Menu) != 0 {
			t.Errorf("вход выполнен: права видны, кнопок входа нет: %+v", c)
		}
	}
	if c := docByID(doc, "mcp-state"); c == nil || len(c.Badges) != 1 || c.Badges[0].Text != "на связи" {
		t.Errorf("состояние: %+v", c)
	}
	en := docByID(doc, "mcp-enabled")
	if en == nil || en.Toggle != "on" || hasAction(en.Actions, "toggle_mcp") == nil {
		t.Errorf("«Включён»: %+v", en)
	}
	// права входа — поле, а не вопрос в меню
	form := docByID(doc, "mcp-login-scope")
	if form == nil || form.Style != formRows || !form.Deferred || form.Submit.Action != "save_mcp_login_scope" || form.Submit.Target != "sing" {
		t.Fatalf("форма прав входа: %+v", form)
	}
	if f := fieldOf(doc, "mcp-login-scope", "login_scope"); f == nil || f.Value != "tasks:read tasks:write" {
		t.Errorf("права входа: %+v", f)
	}
	// «В чате»: поля строками, под полосой
	for name, kind := range map[string]string{"chat": "toggle", "about": "text", "keywords": "text", "class": "segments"} {
		if f := fieldOf(doc, "mcp-chat-sing", name); f == nil || f.Type != kind {
			t.Errorf("«В чате»: поле %s: %+v", name, f)
		}
	}
	if f := fieldOf(doc, "mcp-chat-sing", "about"); f.Value != "задачи, привычки и проекты" {
		t.Errorf("«Что это»: %q", f.Value)
	}
	// подсказки: сводка, «Забыть все» под вопросом, строки с «Забыть»
	hints := docByID(doc, "mcp-hints-section")
	if hints == nil || len(hints.Children) != 3 {
		t.Fatalf("подсказки: %+v", hints)
	}
	if a := hasAction(hints.Children[0].Actions, "forget_mcp_hints"); a == nil || a.Confirm == "" || a.ConfirmOK == "" {
		t.Errorf("«Забыть все»: %+v", hints.Children[0].Actions)
	}
	// «Опасное» — последним, кнопки видимы, у каждой вопрос
	last := doc.Components[len(doc.Components)-1]
	if last.Type != "section" || last.Style != "danger" || last.Title != "Опасное" {
		t.Fatalf("последним должно стоять «Опасное»: %+v", last)
	}
	acts := map[string]Action{}
	for _, row := range last.Children {
		for _, a := range row.Actions {
			acts[a.Action] = a
			if a.Style != "destructive" {
				t.Errorf("кнопка %q в «Опасном» не разрушительная: %q", a.Action, a.Style)
			}
		}
		if len(row.Menu) != 0 {
			t.Errorf("в «Опасном» кнопки уехали в «···»: %+v", row)
		}
	}
	for _, name := range []string{"mcp_logout", "delete_mcp"} {
		if a, ok := acts[name]; !ok || a.Confirm == "" || a.ConfirmOK == "" || a.Target != "sing" {
			t.Errorf("в «Опасном» нет %q с вопросом и надписью: %+v", name, a)
		}
	}
	// обе кнопки входа сидят в шапке и в «Подключении»: вход выполнен — их нет
	if bar := doc.Components[1].Children[0]; bar.Actions[0].Action != "check_mcp" {
		t.Errorf("главная кнопка шапки после входа: %+v", bar.Actions)
	}
}

func TestMCPOverviewNeedsLogin(t *testing.T) {
	s, _ := mcpScreensFix(t)
	doc := serverDoc(t, s, entityAddr(mcpScreen, "wiki", ""))
	login := docByID(doc, "mcp-login")
	if login == nil || !strings.Contains(login.Caption, "нужен вход") {
		t.Fatalf("вход: %+v", login)
	}
	if len(login.Actions) != 1 || login.Actions[0].Action != "mcp_login" || login.Actions[0].Label != "Войти" || login.Actions[0].Style != "primary" {
		t.Errorf("кнопка входа: %+v", login.Actions)
	}
	bar := doc.Components[1].Children[0]
	if bar.Actions[0].Action != "mcp_login" || bar.Level != LevelWarn || len(bar.Badges) != 1 || bar.Badges[0].Text != "нужен вход" {
		t.Errorf("шапка: %+v", bar)
	}
	// выходить некуда: в «Опасном» только удаление
	last := doc.Components[len(doc.Components)-1]
	if len(last.Children) != 1 || last.Children[0].Actions[0].Action != "delete_mcp" {
		t.Errorf("«Опасное» у сервера без входа: %+v", last.Children)
	}

	// «Войти» готовит вход, а «Открыть страницу входа» ведёт на страницу сервера входа
	if _, err := s.execute(actionRequest{Action: "mcp_login", Target: "wiki", Screen: entityAddr(mcpScreen, "wiki", ""),
		origin: "https://sema.local:8443", who: "session:1"}); err != nil {
		t.Fatal(err)
	}
	link, _, ok := s.eng.MCPLoginPending("wiki")
	if !ok {
		t.Fatal("вход не начат")
	}
	doc = serverDoc(t, s, entityAddr(mcpScreen, "wiki", ""))
	login = docByID(doc, "mcp-login")
	if len(login.Actions) != 1 || login.Actions[0].Action != "$open" || login.Actions[0].Target != link || login.Actions[0].Label != "Открыть страницу входа" {
		t.Errorf("после «Войти»: %+v", login.Actions)
	}
	menu := ""
	for _, a := range login.Menu {
		menu += a.Action + ","
	}
	if !strings.Contains(menu, "mcp_login,") || !strings.Contains(menu, "mcp_login_cancel,") {
		t.Errorf("«Начать вход заново» и «Отменить вход» не в меню: %q", menu)
	}
	if bar := doc.Components[1].Children[0]; bar.Actions[0].Action != "$open" {
		t.Errorf("главная кнопка шапки при начатом входе: %+v", bar.Actions)
	}
	if !strings.Contains(login.Caption, "начат") {
		t.Errorf("вход начат, а подпись %q", login.Caption)
	}
}

// Токены отозваны или истекли — «Обзор» говорит «нужен вход», «Выйти» нет,
// «Войти» — главная кнопка.
func TestMCPOverviewTokensRevoked(t *testing.T) {
	s, st := mcpScreensFix(t)
	// токен истёк, продлить нечем
	if err := st.SetMCPLogin("sing", &store.MCPLogin{At: "2026-09-01T10:00:00Z", Expires: "2026-09-01T11:00:00Z", Scope: "tasks:read"}); err != nil {
		t.Fatal(err)
	}
	doc := serverDoc(t, s, entityAddr(mcpScreen, "sing", ""))
	if c := docByID(doc, "mcp-login"); c == nil || !strings.Contains(c.Caption, "токен истёк") {
		t.Errorf("истёкший токен: %+v", c)
	}
	// вышли: токенов нет, сервер просит вход
	if err := s.eng.MCPLogout(context.Background(), "sing"); err != nil {
		t.Fatal(err)
	}
	doc = serverDoc(t, s, entityAddr(mcpScreen, "sing", ""))
	c := docByID(doc, "mcp-login")
	if c == nil || !strings.Contains(c.Caption, "нужен вход") || len(c.Actions) != 1 || c.Actions[0].Label != "Войти" {
		t.Errorf("после выхода: %+v", c)
	}
	if danger := doc.Components[len(doc.Components)-1]; len(danger.Children) != 1 {
		t.Errorf("после выхода в «Опасном» остался «Выйти»: %+v", danger.Children)
	}
	if bar := doc.Components[1].Children[0]; bar.Actions[0].Action != "mcp_login" {
		t.Errorf("главная кнопка шапки после выхода: %+v", bar.Actions)
	}
	list := serverDoc(t, s, mcpScreen)
	for _, section := range list.Components {
		for _, row := range section.Children {
			if row.Subtitle == "sing" && (len(row.Badges) == 0 || row.Badges[0].Text != "нужен вход") {
				t.Errorf("строка списка после выхода: %+v", row.Badges)
			}
		}
	}
}

// Сервер с постоянным ключом: имя секрета, заголовок и приставка видны, значение
// — нет; входа нет, прав входа нет.
func TestMCPOverviewKeyServer(t *testing.T) {
	s, _ := mcpScreensFix(t)
	doc := serverDoc(t, s, entityAddr(mcpScreen, "ключ", ""))
	auth := docByID(doc, "mcp-auth")
	if auth == nil || !strings.Contains(auth.Caption, "«mcp-key»") || !strings.Contains(auth.Caption, "«X-Api-Key»") || !strings.Contains(auth.Caption, "«Bearer »") {
		t.Fatalf("чем входит: %+v", auth)
	}
	if a := hasAction(auth.Actions, "$goto"); a == nil || a.Target != entityAddr(secretsScreen, "mcp-key", secretsTabLog) {
		t.Errorf("к ключу ведёт %+v", auth.Actions)
	}
	if docByID(doc, "mcp-login") != nil || docByID(doc, "mcp-login-scope") != nil {
		t.Error("у сервера с ключом показан вход или права входа")
	}
	if docByID(doc, "mcp-chat-ключ") == nil {
		t.Error("у сервера с ключом нет «В чате»")
	}
	if danger := doc.Components[len(doc.Components)-1]; len(danger.Children) != 1 || danger.Children[0].Actions[0].Action != "delete_mcp" {
		t.Errorf("«Опасное» у сервера с ключом: %+v", danger.Children)
	}
}

// Сервер жителя машины: честно, что адреса нет и ходит он только по проводу
// агента; «В чате» нет, входа нет.
func TestMCPOverviewAgentServer(t *testing.T) {
	s, _ := mcpScreensFix(t)
	doc := serverDoc(t, s, entityAddr(mcpScreen, "код", ""))
	addr := docByID(doc, "mcp-addr")
	if addr == nil || addr.Subtitle != "Где живёт" || !strings.Contains(addr.Caption, "Адреса у сервера нет") ||
		!strings.Contains(addr.Caption, "только по шифрованному каналу агента") || !strings.Contains(addr.Caption, "«мак»") {
		t.Fatalf("где живёт: %+v", addr)
	}
	if a := hasAction(addr.Actions, "$goto"); a == nil || a.Target != agentsScreen {
		t.Errorf("к агентам ведёт %+v", addr.Actions)
	}
	if docByID(doc, "mcp-login") != nil || docByID(doc, "mcp-login-scope") != nil || docByID(doc, "mcp-chat-код") != nil {
		t.Error("у сервера жителя машины показан вход, права входа или «В чате»")
	}
	if docByID(doc, "mcp-chat-no") == nil {
		t.Error("не объяснено, почему серверу жителя машины чат не открывается")
	}
	if !strings.Contains(docByID(doc, "mcp-auth").Caption, "каналу агентов") {
		t.Errorf("чем входит: %q", docByID(doc, "mcp-auth").Caption)
	}
}

// Выключенный сервер: капсула «выключен» вместо «на связи», тумблер выключен.
func TestMCPDisabledServerSaysSo(t *testing.T) {
	s, st := mcpScreensFix(t)
	if _, err := s.execute(actionRequest{Action: "toggle_mcp", Target: "ключ"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MCPServer("ключ"); m.Enabled {
		t.Fatal("сервер остался включённым")
	}
	doc := serverDoc(t, s, entityAddr(mcpScreen, "ключ", ""))
	if en := docByID(doc, "mcp-enabled"); en == nil || en.Toggle != "off" {
		t.Errorf("«Включён»: %+v", en)
	}
	if bar := doc.Components[1].Children[0]; len(bar.Badges) != 1 || bar.Badges[0].Text != "выключен" {
		t.Errorf("шапка выключенного сервера: %+v", bar.Badges)
	}
	list := serverDoc(t, s, mcpScreen)
	for _, c := range list.Components {
		for _, row := range c.Children {
			if row.Subtitle == "ключ" {
				if row.Badges[0].Text != "выключен" || hasAction(row.Menu, "toggle_mcp") == nil || !strings.Contains(hasAction(row.Menu, "toggle_mcp").Label, "Включить") {
					t.Errorf("строка выключенного сервера: %+v %+v", row.Badges, row.Menu)
				}
			}
		}
	}
}

// Ни ключ, ни токены входа не попадают в разметку ни одного экрана раздела:
// ни в документ, ни в готовый HTML, ни в ленивые части.
func TestMCPNoSecretsInMarkup(t *testing.T) {
	s, _ := mcpScreensFix(t)
	// вход на wiki начат: ссылка страницы согласия на экране есть, а токенов у неё нет
	if _, err := s.eng.MCPLoginBegin(context.Background(), "wiki", "https://sema.local:8443", "session:1"); err != nil {
		t.Fatal(err)
	}
	screens := []string{mcpScreen, entityAddr(mcpScreen, mcpNew, "")}
	for _, name := range mcpServerNames {
		for _, tab := range []string{mcpTabOverview, mcpTabRules, mcpTabLog} {
			screens = append(screens, entityAddr(mcpScreen, name, tab))
		}
	}
	secrets := []string{mcpKeySentinel, mcpAccessSentinel, mcpRefreshSentinel}
	look := func(what, text string) {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s: виден %q", what, secret)
			}
		}
	}
	for _, screen := range screens {
		doc := serverDoc(t, s, screen)
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		look("документ "+screen, string(raw))
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatal(err)
		}
		look("разметка "+screen, string(markup))
		for _, part := range lazySections(doc.Components) {
			blocks, err := s.buildPart(screen, part, "", 0)
			if err != nil {
				t.Fatalf("часть %s экрана %s: %v", part, screen, err)
			}
			raw, _ := json.Marshal(blocks)
			look("часть "+part+" экрана "+screen, string(raw))
		}
	}
	// ленивые части раздела: общий журнал и «Тонкая настройка»
	for _, part := range []string{mcpLogPart, rareID} {
		blocks, err := s.buildPart(mcpScreen, part, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(blocks)
		look("часть "+part, string(raw))
	}
}

// Типы компонентов и полей, которыми собраны экраны раздела, объявлены
// словарём клиента: новый тип без строки у клиента роняет тест, а не экран.
func TestMCPScreensUseKnownTypes(t *testing.T) {
	s, _ := mcpScreensFix(t)
	comps, fields := dictionaryTypes(web.Dictionary())
	screens := []string{mcpScreen, entityAddr(mcpScreen, mcpNew, "")}
	for _, name := range mcpServerNames {
		for _, tab := range []string{mcpTabOverview, mcpTabRules, mcpTabLog} {
			screens = append(screens, entityAddr(mcpScreen, name, tab))
		}
	}
	for _, screen := range screens {
		uc, uf := usedTypes(serverDoc(t, s, screen))
		for name := range uc {
			if name != "" && !comps[name] {
				t.Errorf("клиент не знает компонент %q с экрана %q", name, screen)
			}
		}
		for name := range uf {
			if name != "" && !fields[name] {
				t.Errorf("клиент не знает поле %q с экрана %q", name, screen)
			}
		}
	}
}

// Прежние адреса: хвост-вкладка раздела («~trackers») открывает сам раздел, а
// адрес без вкладки — «Обзор» сервера (до 2 октября он открывал правила).
func TestOldMCPAddressesRedirect(t *testing.T) {
	s, _ := mcpScreensFix(t)
	if doc := serverDoc(t, s, mcpScreen+"~trackers"); doc.Screen != mcpScreen || doc.Heading != "Интеграции" || len(doc.Subtabs) != 0 {
		t.Errorf("~trackers открыл %q «%s»", doc.Screen, doc.Heading)
	}
	doc := serverDoc(t, s, mcpScreen+"@sing")
	if doc.Heading != "sing" || doc.ActiveSubtab != mcpScreen+"@sing" || docByID(doc, "mcp-conn") == nil {
		t.Errorf("settings:integrations@sing: %q, вкладка %q", doc.Heading, doc.ActiveSubtab)
	}
	// неизвестная вкладка сервера — «Обзор», а не ошибка
	if doc := serverDoc(t, s, mcpScreen+"@sing~нет"); doc.Heading != "sing" || docByID(doc, "mcp-conn") == nil {
		t.Errorf("неизвестная вкладка открыла %q", doc.Heading)
	}
	// старые «Обзоры» с «Решить» вели на сервер: на нём новые инструменты — полкой с кнопкой на правила
	over := serverDoc(t, s, mcpScreen+"@sing")
	if over.Components[2].Type != "shelf" || !strings.Contains(dumpScreen(over), entityAddr(mcpScreen, "sing", mcpTabRules)) {
		t.Errorf("на «Обзоре» нет полки с «Решить»: %s", dumpScreen(over))
	}
}

// Всё, что ядро кладёт в уведомления про серверы MCP, ведёт на существующий
// экран сервера на нужной вкладке — а не на тот, что открывается только
// благодаря переадресации.
func TestMCPNotificationsLeadToServerScreens(t *testing.T) {
	s, st := mcpScreensFix(t)
	for _, tab := range []string{"", "rules", "log"} {
		addr := engine.MCPServerScreen("sing", tab)
		if addr != entityAddr(mcpScreen, "sing", tab) {
			t.Errorf("engine.MCPServerScreen(%q) = %q, а экран зовётся %q", tab, addr, entityAddr(mcpScreen, "sing", tab))
		}
		doc := serverDoc(t, s, addr)
		if doc.Screen != addr || doc.Heading != "sing" || doc.ActiveSubtab != addr {
			t.Errorf("уведомление ведёт на %q, открылось %q «%s» (вкладка %q)", addr, doc.Screen, doc.Heading, doc.ActiveSubtab)
		}
	}

	lead := func(key string) string {
		t.Helper()
		n, open := st.OpenNotificationByKey(key)
		if !open {
			t.Fatalf("уведомления %q нет", key)
		}
		for _, a := range n.Actions {
			if a.Kind == "goto" {
				return a.Screen
			}
		}
		t.Fatalf("у уведомления %q нет перехода: %+v", key, n.Actions)
		return ""
	}
	// «новые инструменты ждут решения»: на вкладку «Правила»
	m, _ := st.MCPServer("sing")
	s.eng.NoteNewMCPTools(m)
	if addr := lead("mcp-new:sing"); addr != entityAddr(mcpScreen, "sing", mcpTabRules) {
		t.Errorf("mcp-new ведёт на %q", addr)
	} else if doc := serverDoc(t, s, addr); doc.ActiveSubtab != addr || doc.Filters[len(doc.Filters)-1].ID != mcpFilterNew {
		t.Errorf("mcp-new открыл не «Правила» с «Новыми»: %q %+v", doc.ActiveSubtab, doc.Filters)
	}
	// «нужен вход»: вызов без входа поднимает уведомление и ведёт на сервер
	if err := st.SetMCPRules("wiki", map[string]string{"task_list": store.RuleAllow}, store.RuleDeny); err != nil {
		t.Fatal(err)
	}
	if res := s.eng.RequestMCP(context.Background(), "тест", "wiki", "task_list", nil); res.Err == "" {
		t.Fatalf("вызов без входа прошёл: %+v", res)
	}
	if addr := lead("mcp_login:wiki"); addr != entityAddr(mcpScreen, "wiki", "") {
		t.Errorf("mcp_login ведёт на %q", addr)
	}
	// вызов на подтверждение: кнопки уведомления работают под прежними именами и
	// оставляют на экране сервера
	id, err := st.AddMCPPending(&store.MCPPending{Server: "sing", Tool: "task_delete", Args: `{}`, Who: "чат"})
	if err != nil {
		t.Fatal(err)
	}
	here := entityAddr(mcpScreen, "sing", "")
	back, err := s.execute(actionRequest{Action: "reject_mcp", Target: strconv.FormatInt(id, 10), Screen: here})
	if err != nil || back != here {
		t.Errorf("reject_mcp с экрана сервера: %q (%v)", back, err)
	}
	// запись входа в хранилище секретов ведёт к своему серверу
	found := false
	list := serverDoc(t, s, secretsScreen)
	for i := range list.Components {
		walkthrough(&list.Components[i], func(c *Component) {
			for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
				if a.Action == "$goto" && a.Label == "Сервер MCP" {
					found = found || a.Target == entityAddr(mcpScreen, "sing", "")
				}
			}
		})
	}
	if !found {
		t.Error("у записи входа в «Ключах и секретах» нет перехода на экран своего сервера")
	}
}

// Адреса экранов сервера MCP в коде строит один помощник: уведомления ядра —
// engine.MCPServerScreen, экраны — entityAddr. Склейка «settings:integrations@» + имя
// где-то ещё — это адрес без вкладки, который забудут обновить.
func TestNoHandWrittenMCPAddressesInCode(t *testing.T) {
	var found []string
	for _, root := range []string{"..", "../../cmd"} {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if strings.HasSuffix(filepath.ToSlash(path), "engine/mcp_rules.go") {
				return nil // здесь он и живёт
			}
			for _, bad := range []string{`"settings:integrations@" +`, `"settings:integrations@"+`, `mcpScreen + "@" +`, `mcpScreen+"@"+`} {
				if strings.Contains(string(raw), bad) {
					found = append(found, path+": "+bad)
				}
			}
			return nil
		})
	}
	if len(found) > 0 {
		t.Errorf("адреса сервера MCP склеиваются руками: %v", found)
	}
}

// Правила сохраняются полосой: форма группы уходит одним действием, движок
// видит ответы, а новый инструмент остаётся запрещённым, пока по нему не решено.
func TestMCPRulesSavedByBarAndSeenByEngine(t *testing.T) {
	s, st := mcpScreensFix(t)
	rulesAddr := entityAddr(mcpScreen, "sing", mcpTabRules)
	doc := serverDoc(t, s, rulesAddr)
	forms := 0
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.Type == "form" && strings.HasPrefix(c.ID, "mcp-rules-") {
				forms++
				if !c.Deferred || c.Submit == nil || (c.Submit.Action != mcpRulesSaving && c.Submit.Action != "save_mcp_groups") {
					t.Errorf("форма правил %q не под полосой: %+v", c.ID, c.Submit)
				}
			}
		})
	}
	if forms < 10 {
		t.Fatalf("форм правил %d: ждали ответ для нового, группы и названия групп", forms)
	}
	m, _ := st.MCPServer("sing")
	if p, _ := engine.ClassifyMCP(m, "tag_update"); p != engine.PolicyDeny {
		t.Fatalf("новый инструмент до решения: %q, ждали «запретить»", p)
	}
	if p, _ := engine.ClassifyMCP(m, "task_create"); p != engine.PolicyAsk {
		t.Fatalf("task_create: %q", p)
	}
	// «Разрешить» task_create и «Запретить» task_list одной формой группы
	back, err := s.execute(actionRequest{Action: mcpRulesSaving, Target: mcpRulesTarget("sing", "Задачи", "", ""), Screen: rulesAddr,
		Params: map[string]string{"tool:task_create": "Разрешить", "tool:task_list": "Запретить"}})
	if err != nil || back != rulesAddr {
		t.Fatalf("сохранение вернуло %q (%v)", back, err)
	}
	m, _ = st.MCPServer("sing")
	if p, _ := engine.ClassifyMCP(m, "task_create"); p != engine.PolicyAllow {
		t.Errorf("task_create после сохранения: %q", p)
	}
	if p, _ := engine.ClassifyMCP(m, "task_list"); p != engine.PolicyDeny {
		t.Errorf("task_list после сохранения: %q", p)
	}
	// новые инструменты остались запрещёнными и новыми; модель их не видит
	for _, name := range []string{"tag_update", "time_stat_create", "time_stat_update"} {
		if p, _ := engine.ClassifyMCP(m, name); p != engine.PolicyDeny {
			t.Errorf("%s без решения: %q", name, p)
		}
		if _, decided := engine.MCPRuleOf(m, name); decided {
			t.Errorf("%s стал решённым без решения человека", name)
		}
	}
	// без экрана сохранение возвращает вкладку «Правила» сервера
	if back, _ := s.execute(actionRequest{Action: mcpRulesSaving, Target: mcpRulesTarget("sing", "", "", ""),
		Params: map[string]string{"default": "Спросить"}}); back != rulesAddr {
		t.Errorf("без экрана вернулось %q", back)
	}
	if m, _ = st.MCPServer("sing"); m.RuleDefault != store.RuleAsk {
		t.Errorf("ответ для новых: %q", m.RuleDefault)
	}
}

// Вкладка «Правила»: верхнее поле ищет инструменты, а не настройки — и на
// десктопе, и на телефоне (там оно остаётся из-за полосы фильтров); на других
// вкладках сервера на телефоне поля нет.
func TestMCPRulesSearchHintAndPhone(t *testing.T) {
	s, _ := mcpScreensFix(t)
	rules := entityAddr(mcpScreen, "sing", mcpTabRules)
	doc := serverDoc(t, s, rules)
	if doc.SearchHint != "Найти инструмент" || !doc.Searchable {
		t.Errorf("десктоп: поле %q, Searchable %v", doc.SearchHint, doc.Searchable)
	}
	if !mcpOwnSearch(rules) || mcpOwnSearch(entityAddr(mcpScreen, "sing", "")) || mcpOwnSearch(mcpScreen) || mcpOwnSearch(entityAddr(mcpScreen, mcpNew, "")) {
		t.Error("mcpOwnSearch путает вкладки")
	}
	phone := func(screen string) *Document {
		doc, err := s.buildScreenForPhone(screen, "", "", 0, true, false)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	if d := phone(rules); !d.Searchable || d.SearchHint != "Найти инструмент" {
		t.Errorf("телефон, «Правила»: Searchable %v, подсказка %q", d.Searchable, d.SearchHint)
	}
	for _, tab := range []string{mcpTabOverview, mcpTabLog} {
		if d := phone(entityAddr(mcpScreen, "sing", tab)); d.Searchable {
			t.Errorf("телефон, вкладка %q: верхнее поле осталось, а искать в нём нечего", tab)
		}
	}
	// поиск отбирает инструменты: «checklist» оставляет чек-листы
	filtered := serverDocFor(t, s, rules, "", "checklist")
	if got := len(toolFields(rulesFormsOf(filtered, "sing"))); got != 6 {
		t.Errorf("по «checklist» на экране %d инструментов, а их шесть", got)
	}
}

// serverDocFor — экран с отбором и поиском.
func serverDocFor(t *testing.T, s *Server, screen, filter, search string) *Document {
	t.Helper()
	doc, err := s.buildScreen(screen, filter, search, 0)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func rulesFormsOf(doc *Document, server string) []*Component {
	var out []*Component
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.Type == "form" && strings.HasPrefix(c.ID, "mcp-rules-"+server+"-") {
				out = append(out, c)
			}
		})
	}
	return out
}

// Журнал сервера: чипы с числами отбирают, первая страница — двадцать строк и
// «Показать ещё»; чипы не врут: каждая строка называет тот же исход, что и чип.
func TestMCPJournalFiltersAndPages(t *testing.T) {
	s, st := mcpScreensFix(t)
	addr := entityAddr(mcpScreen, "sing", mcpTabLog)
	doc := serverDoc(t, s, addr)
	var chips []string
	for _, f := range doc.Filters {
		chips = append(chips, f.Title)
	}
	if got := strings.Join(chips, "|"); got != "Все · 36|Выполнены · 32|Ждали кнопку · 4|Отказы · 2|Ошибки · 1" {
		t.Errorf("чипы: %s", got)
	}
	section := doc.Components[len(doc.Components)-2]
	if len(section.Children) != 20 {
		t.Errorf("первая страница журнала: %d строк, а нужно двадцать", len(section.Children))
	}
	more := doc.Components[len(doc.Components)-1]
	if more.Type != "button" || more.Actions[0].Action != "$more" || !strings.Contains(more.Actions[0].Label, "20 из 36") {
		t.Errorf("«Показать ещё»: %+v", more)
	}
	// предел удваивается «Показать ещё»
	if doc := serverDocFor(t, s, addr, "", ""); doc.ActiveFilter != "" {
		t.Errorf("активный отбор по умолчанию: %q", doc.ActiveFilter)
	}
	full, err := s.buildScreen(addr, "", "", 40)
	if err != nil {
		t.Fatal(err)
	}
	if got := full.Components[len(full.Components)-1]; got.Type == "button" {
		t.Errorf("при пределе 40 все 36 строк влезают, а «Показать ещё» осталось: %+v", got)
	}
	// чипы отбирают: слово в базе и слово на экране говорят про одно и то же
	want := map[string]map[string]bool{
		store.MCPLogDone:   {store.MCPUseDone: true},
		store.MCPLogAsked:  {store.MCPUseWaiting: true, store.MCPUseRejected: true, store.MCPUseDone: true, store.MCPUseError: true},
		store.MCPLogDenied: {store.MCPUseDenied: true, store.MCPUseRejected: true},
		store.MCPLogError:  {store.MCPUseError: true},
	}
	for filter, states := range want {
		rows, err := st.MCPLogFiltered("sing", filter, 100)
		if err != nil || len(rows) == 0 {
			t.Fatalf("отбор %q: %d строк (%v)", filter, len(rows), err)
		}
		for _, u := range rows {
			if !states[u.State()] {
				t.Errorf("отбор %q принёс вызов с исходом %q: %+v", filter, u.State(), u)
			}
		}
		if n := st.CountMCPLog("sing", filter); n != len(rows) {
			t.Errorf("отбор %q: число у чипа %d, строк %d", filter, n, len(rows))
		}
	}
	// отбор «Ошибки» на экране: одна строка с исходом «ошибка»
	errs := serverDocFor(t, s, addr, store.MCPLogError, "")
	if errs.ActiveFilter != store.MCPLogError {
		t.Errorf("активный отбор: %q", errs.ActiveFilter)
	}
	rows := errs.Components[len(errs.Components)-1].Children
	if len(rows) != 1 || rows[0].Subtitle != "task_update" || rows[0].Badges[0].Text != "ошибка" || !rows[0].Expandable {
		t.Errorf("отбор «Ошибки»: %+v", rows)
	}
	// неизвестный отбор — «Все», а не пустой экран
	if d := serverDocFor(t, s, addr, "что-то", ""); d.ActiveFilter != "" || len(d.Components[len(d.Components)-2].Children) != 20 {
		t.Errorf("неизвестный отбор: %q", d.ActiveFilter)
	}
	// пустой отбор объясняет себя
	if err := st.SaveMCPServer(&store.MCPServer{Name: "пустой", URL: "http://127.0.0.1:1/mcp", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	empty := serverDoc(t, s, entityAddr(mcpScreen, "пустой", mcpTabLog))
	if r := empty.Components[len(empty.Components)-1].Children; len(r) != 1 || r[0].Style != "empty" {
		t.Errorf("пустой журнал: %+v", r)
	}
	// у журнала на телефоне верхнего поля нет
	if !empty.noPhoneSearch {
		t.Error("журналу не нужно верхнее поле на телефоне")
	}
}

// Общий журнал раздела — ленивая часть: двадцать строк и «Показать ещё» внутри
// части, строка называет сервер.
func TestMCPCommonJournalFragment(t *testing.T) {
	s, st := mcpScreensFix(t)
	blocks, err := s.buildPart(mcpScreen, mcpLogPart, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	total := st.CountMCPLog("", store.MCPLogAll)
	if len(blocks) != 21 || blocks[20].Type != "button" || !strings.Contains(blocks[20].Actions[0].Label, "20 из "+strconv.Itoa(total)) {
		t.Fatalf("первая страница общего журнала: %d блоков, последний %+v", len(blocks), blocks[len(blocks)-1])
	}
	if !strings.Contains(blocks[0].Subtitle, " · ") {
		t.Errorf("в общем журнале строка не называет сервер: %q", blocks[0].Subtitle)
	}
	all, err := s.buildPart(mcpScreen, mcpLogPart, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != total || all[len(all)-1].Type == "button" {
		t.Errorf("при пределе 100 журнал целиком (%d), а блоков %d", total, len(all))
	}
	// до раскрытия журнала на экране нет ни одной его строки
	doc := serverDoc(t, s, mcpScreen)
	if strings.Contains(dumpScreen(doc), "task_get_5") {
		t.Error("тело общего журнала приехало вместе с экраном, а не по раскрытию")
	}
}

// «Добавить сервер MCP»: три обязательных поля строками, остальное под «Ещё N
// полей» в той же форме; кнопка одна, заводит сервер и ведёт на его экран.
func TestMCPNewScreen(t *testing.T) {
	s, st := testServer(t)
	addr := entityAddr(mcpScreen, mcpNew, "")
	doc := serverDoc(t, s, addr)
	if doc.Heading != "Добавить сервер MCP" || doc.Components[0].Type != "crumb" || doc.Components[0].Actions[0].Label != "‹ Интеграции" {
		t.Errorf("шапка: %q %+v", doc.Heading, doc.Components[0])
	}
	form := docByID(doc, "mcp-new")
	if form == nil || form.Style != formRows || form.Submit == nil || form.Submit.Action != "add_mcp" || form.Deferred {
		t.Fatalf("форма: %+v", form)
	}
	more := map[string]bool{}
	var plain []string
	for _, f := range form.Fields {
		if f.More {
			more[f.Name] = true
		} else {
			plain = append(plain, f.Name)
		}
	}
	if strings.Join(plain, ",") != "name,url,secret" || len(more) != 7 {
		t.Errorf("обязательные %v, редкие %v", plain, more)
	}
	for _, name := range []string{"agent", "can", "header", "prefix", "login_scope", "about", "keywords"} {
		if !more[name] {
			t.Errorf("поле %q не под «Ещё N полей»", name)
		}
	}
	// заводит сервер и ведёт на его экран
	back, err := s.execute(actionRequest{Action: "add_mcp", Screen: addr, Params: map[string]string{"name": "wiki2", "url": "http://127.0.0.1:1/mcp", "about": "Вики"}})
	if err != nil || back != entityAddr(mcpScreen, "wiki2", "") {
		t.Fatalf("add_mcp вернул %q (%v)", back, err)
	}
	if m, ok := st.MCPServer("wiki2"); !ok || !m.Chat || m.About != "Вики" {
		t.Errorf("заведённый сервер: %+v", m)
	}
	// адрес экрана делится «@» и «~», а «new» — экран добавления
	for _, bad := range []string{"a@b", "a~b", mcpNew} {
		if _, err := s.execute(actionRequest{Action: "add_mcp", Params: map[string]string{"name": bad, "url": "http://127.0.0.1:1/mcp"}}); err == nil {
			t.Errorf("имя %q принято", bad)
		}
	}
	if _, err := s.execute(actionRequest{Action: "add_mcp", Params: map[string]string{"name": "без-адреса"}}); err == nil {
		t.Error("сервер без адреса и агента принят")
	}
}

// Действия остаются на открытом экране, а с чужого — возвращают список или
// экран сервера; прежние действия работают под прежними именами.
func TestMCPActionsKeepTheOpenScreen(t *testing.T) {
	s, st := mcpScreensFix(t)
	here := entityAddr(mcpScreen, "sing", "")
	for _, c := range []struct{ action, target string }{
		{"toggle_mcp", "sing"}, {"toggle_mcp", "sing"}, {"toggle_mcp_chat", "sing"}, {"toggle_mcp_chat", "sing"},
		{"forget_mcp_hints", "sing"}, {"mcp_login_cancel", "sing"}, {"save_mcp_login_scope", "sing"}, {"mcp_login_scope", "sing"},
	} {
		back, err := s.execute(actionRequest{Action: c.action, Target: c.target, Screen: here, Params: map[string]string{"login_scope": "tasks:read"}})
		if err != nil || back != here {
			t.Errorf("%s: вернул %q (%v), а нужно остаться на %q", c.action, back, err, here)
		}
	}
	// с чужого экрана — на список (экран сервера — для сохранения «В чате»)
	for action, want := range map[string]string{"toggle_mcp": mcpScreen, "check_mcp": mcpScreen, "mcp_login_cancel": mcpScreen} {
		if back, _ := s.execute(actionRequest{Action: action, Target: "sing", Screen: "inbox"}); back != want {
			t.Errorf("%s с чужого экрана вернул %q", action, back)
		}
	}
	// «Права входа» полем и прежним вопросом пишут одно и то же
	if _, err := s.execute(actionRequest{Action: "save_mcp_login_scope", Target: "sing", Params: map[string]string{"login_scope": "  a   b "}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MCPServer("sing"); m.LoginScope != "a b" {
		t.Errorf("права входа полем: %q", m.LoginScope)
	}
	if _, err := s.execute(actionRequest{Action: "mcp_login_scope", Target: "sing", Params: map[string]string{"value": "mcp:read"}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MCPServer("sing"); m.LoginScope != "mcp:read" {
		t.Errorf("права входа прежним вопросом: %q", m.LoginScope)
	}
	// удаление ведёт в список
	back, err := s.execute(actionRequest{Action: "delete_mcp", Target: "ключ", Screen: entityAddr(mcpScreen, "ключ", "")})
	if err != nil || back != mcpScreen {
		t.Errorf("delete_mcp: %q (%v)", back, err)
	}
	if _, ok := st.MCPServer("ключ"); ok {
		t.Error("сервер не удалён")
	}
}

// Что ждёт кнопки — на полке раздела и на «Обзоре» своего сервера, у обоих
// «Выполнить» и «Отклонить»; отклонённое уходит с полки.
func TestMCPShelfPendingCalls(t *testing.T) {
	s, st := mcpScreensFix(t)
	list := serverDoc(t, s, mcpScreen)
	shelf := list.Components[0]
	if shelf.Type != "shelf" || !strings.Contains(shelf.Meta, "2 вызова из круга «Спросить»") || !strings.Contains(shelf.Meta, "3 новых инструмента без решения") {
		t.Fatalf("полка раздела: %+v", shelf)
	}
	own := serverDoc(t, s, entityAddr(mcpScreen, "ключ", ""))
	own = &Document{Components: own.Components}
	var shelfOwn *Component
	for i := range own.Components {
		if own.Components[i].Type == "shelf" {
			shelfOwn = &own.Components[i]
		}
	}
	if shelfOwn == nil || len(shelfOwn.Children) != 1 || !strings.Contains(shelfOwn.Children[0].Subtitle, "page_update") {
		t.Fatalf("полка на экране сервера «ключ»: %+v", shelfOwn)
	}
	pend, _ := st.MCPPendingList()
	if _, err := s.execute(actionRequest{Action: "reject_mcp", Target: strconv.FormatInt(pend[0].ID, 10), Screen: mcpScreen}); err != nil {
		t.Fatal(err)
	}
	if got := serverDoc(t, s, mcpScreen).Components[0].Meta; !strings.Contains(got, "1 вызов") {
		t.Errorf("после отклонения полка говорит %q", got)
	}
}
