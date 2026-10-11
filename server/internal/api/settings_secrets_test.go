package api

// «Ключи и секреты» (2 октября 2026): три вкладки раздела, экран секрета,
// «Добавить секрет», служебные отдельно от ваших, «Шифрование» с «Инструментов».
// Проверяется сборка документа и действия; настоящий Chrome — в
// browser_secrets_agents_test.go.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// saGet — запрос с этой машины, без входа: так тесты смотрят то, что уходит
// наружу по экрану.
func saGet(t *testing.T, s *Server, path string) string {
	t.Helper()
	r := httptest.NewRequest("GET", "http://localhost:8765"+path, nil)
	r.Header.Set("HX-Request", "true")
	r.RemoteAddr, r.Host = "127.0.0.1:54321", "127.0.0.1:8765"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: код %d, тело %s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

// saBodies — всё, что уходит наружу по экрану: разметка, документ /api/ui и
// ленивые части (журнал агентов, «Тонкая настройка»).
func saBodies(t *testing.T, s *Server, screen string) map[string]string {
	t.Helper()
	q := url.QueryEscape(screen)
	out := map[string]string{
		"разметка": saGet(t, s, "/ui?screen="+q),
		"/api/ui":  saGet(t, s, "/api/ui?screen="+q),
	}
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("экран %s: %v", screen, err)
	}
	for _, part := range lazySections(doc.Components) {
		out["часть "+part] = saGet(t, s, "/ui?screen="+q+"&part="+url.QueryEscape(part))
	}
	return out
}

func saSection(doc *Document, id string) *Component {
	for i := range doc.Components {
		if doc.Components[i].ID == id {
			return &doc.Components[i]
		}
	}
	return nil
}

// У раздела три вкладки, каждая собирается по своему адресу; «Добавить секрет» —
// одна кнопка шапки на всех вкладках.
func TestSecretsSectionTabs(t *testing.T) {
	fx := secretsAgentsFix(t)
	for _, tab := range []string{secretsTabList, secretsTabCrypt, secretsTabLog} {
		screen := entityAddr(secretsScreen, "", tab)
		doc := serverDoc(t, fx.S, screen)
		if doc.Screen != screen || doc.ActiveSubtab != screen {
			t.Errorf("%s: экран %q, выбранная вкладка %q", screen, doc.Screen, doc.ActiveSubtab)
		}
		var titles []string
		for _, f := range doc.Subtabs {
			titles = append(titles, f.Title)
		}
		if strings.Join(titles, "|") != "Секреты|Шифрование|Журнал" {
			t.Errorf("%s: вкладки %v", screen, titles)
		}
		if doc.ActivePanel != secretsScreen || doc.Heading != "Ключи и секреты" {
			t.Errorf("%s: в колонке выбран %q, заголовок %q", screen, doc.ActivePanel, doc.Heading)
		}
		if len(doc.ToolbarActions) != 1 || doc.ToolbarActions[0].Label != "Добавить секрет" ||
			doc.ToolbarActions[0].Target != entityAddr(secretsScreen, secretsNew, "") || doc.ToolbarActions[0].Style != "primary" {
			t.Errorf("%s: шапка раздела: %+v", screen, doc.ToolbarActions)
		}
	}
	// ваши — первая вкладка, 6 штук; счётчик у вкладки
	doc := serverDoc(t, fx.S, secretsScreen)
	if doc.Subtabs[0].Badge != "2 ждут" {
		t.Errorf("вкладка «Секреты»: %+v: ждут два — модуль просит gitlab-token и нет nextcloud-token", doc.Subtabs[0])
	}
}

// Две группы: ваши (шесть) и служебные (двадцать плюс ящики и модели с ключом
// у владельцев) — свёрнутые и без «Удалить». Признак — кто завёл, а не имя.
func TestSecretsListGroups(t *testing.T) {
	fx := secretsAgentsFix(t)
	doc := serverDoc(t, fx.S, secretsScreen)
	own, service := saSection(doc, "secrets-own"), saSection(doc, "secrets-service")
	if own == nil || service == nil {
		t.Fatalf("групп нет: %+v", doc.Components)
	}
	if !strings.HasPrefix(own.Title, "Ваши ключи и токены") || len(own.Children) != 6 || own.Collapsed {
		t.Errorf("ваши: %q, %d строк, свёрнуто %v", own.Title, len(own.Children), own.Collapsed)
	}
	if !strings.HasPrefix(service.Title, "Заводятся сами · 20") || !service.Collapsed {
		t.Errorf("служебные: %q, свёрнуто %v", service.Title, service.Collapsed)
	}
	names := func(c *Component) map[string]bool {
		out := map[string]bool{}
		for _, r := range c.Children {
			out[r.Subtitle] = true
		}
		return out
	}
	for _, want := range []string{"ssh:Microtik", "ssh:srv-vpn", "planner-token", "gitlab-token", "yandex-app-password", "s3-secret"} {
		if !names(own)[want] {
			t.Errorf("среди ваших нет %q: %v", want, names(own))
		}
	}
	for _, want := range []string{"agents:root-key", "model:OpenAI", "mcp:sing:oauth", "push:vapid-private", "tg:api_hash", "mail:owner@example.com"} {
		if !names(service)[want] {
			t.Errorf("среди служебных нет %q", want)
		}
		if names(own)[want] {
			t.Errorf("служебный %q попал к вашим", want)
		}
	}
	// ящик и модель, чьи секреты лежат у владельцев, показаны среди служебных
	if !names(service)["owner@example.com"] {
		t.Errorf("пароль ящика у владельца не показан среди служебных: %v", names(service))
	}
}

// Строка тонкая: не больше двух капсул, одна видимая кнопка, один переход на
// экран секрета, «···»; у служебного нигде нет «Удалить», у вашего — есть, с
// вопросом, в котором названо, кто останется без ключа.
func TestSecretsRowsAreThin(t *testing.T) {
	fx := secretsAgentsFix(t)
	doc := serverDoc(t, fx.S, secretsScreen)
	check := func(group string, deletable bool) {
		sec := saSection(doc, group)
		for _, row := range sec.Children {
			if row.ID == "" || !strings.HasPrefix(row.ID, "secret-") {
				continue // строки секретов ящиков и моделей у владельцев — не записи хранилища
			}
			if len(row.Badges) > 2 {
				t.Errorf("%s: капсул %d, а не больше двух", row.Subtitle, len(row.Badges))
			}
			buttons, gotos := 0, 0
			for _, a := range row.Actions {
				switch {
				case a.Action == "$goto":
					gotos++
					if a.Target != entityAddr(secretsScreen, row.Subtitle, "") {
						t.Errorf("%s: строка ведёт на %q", row.Subtitle, a.Target)
					}
				case a.Style == "destructive":
					// разрушительное tidyRows уносит в «···» при сборке экрана
				default:
					buttons++
				}
			}
			if buttons > 1 || gotos != 1 {
				t.Errorf("%s: видимых кнопок %d и переходов %d, а нужны не больше одной и один", row.Subtitle, buttons, gotos)
			}
			if len(row.Menu) == 0 {
				t.Errorf("%s: нет «···»", row.Subtitle)
			}
			var del *Action
			for _, a := range append(append([]Action{}, row.Actions...), row.Menu...) {
				if a.Action == "delete_secret" {
					a := a
					del = &a
				}
			}
			switch {
			case deletable && (del == nil || del.Confirm == "" || del.ConfirmOK == "" || del.Style != "destructive"):
				t.Errorf("%s: у вашего секрета нет «Удалить» с вопросом: %+v", row.Subtitle, del)
			case !deletable && del != nil:
				t.Errorf("%s: у служебного секрета есть «Удалить»", row.Subtitle)
			}
		}
	}
	check("secrets-own", true)
	check("secrets-service", false)

	// и вообще нигде в списке «Удалить» служебному не предложено
	for _, row := range saSection(doc, "secrets-service").Children {
		for _, a := range append(append([]Action{}, row.Actions...), row.Menu...) {
			if a.Action == "delete_secret" {
				t.Errorf("%s: «Удалить» у служебного", row.Subtitle)
			}
		}
	}
	// вопрос называет, кто останется без ключа
	for _, row := range saSection(doc, "secrets-own").Children {
		if row.Subtitle != "ssh:Microtik" {
			continue
		}
		for _, a := range append(append([]Action{}, row.Actions...), row.Menu...) {
			if a.Action == "delete_secret" && !strings.Contains(a.Confirm, "сервер «Microtik»") {
				t.Errorf("вопрос про ключ Microtik не называет сервер: %q", a.Confirm)
			}
		}
	}
	// полка: модуль просит секрет, и имя без секрета
	shelf := findByType(doc, "shelf")
	if shelf == nil || doc.Components[0].Type != "shelf" {
		t.Fatalf("полка «Ждут вас» первой: %+v", shelf)
	}
	text := ""
	for _, c := range shelf.Children {
		text += c.Subtitle + "|"
		for _, a := range c.Actions {
			text += a.Action + ":" + a.Target + ","
		}
	}
	for _, want := range []string{"Синхронизация GitLab", "grant_secret:gitlab-sync~gitlab-token", "nextcloud-token", entityAddr(secretsScreen, secretsNew, secretsNewName+"nextcloud-token")} {
		if !strings.Contains(text, want) {
			t.Errorf("в полке нет %q: %s", want, text)
		}
	}
}

// Экран секрета: три вкладки, закреплённая строка, «Опасное» последним у вашего
// и никакого — у служебного.
func TestSecretScreenTabs(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	for _, tab := range []string{secretsTabList, secretTabGrants, secretsTabLog} {
		screen := entityAddr(secretsScreen, "planner-token", tab)
		doc := serverDoc(t, s, screen)
		var titles []string
		for _, f := range doc.Subtabs {
			titles = append(titles, f.Title)
		}
		if strings.Join(titles, "|") != "Обзор|Кому выдан|Журнал" {
			t.Errorf("%s: вкладки %v", screen, titles)
		}
		if doc.Screen != screen || doc.ActiveSubtab != screen || doc.ActivePanel != secretsScreen || doc.Heading != "planner-token" {
			t.Errorf("%s: экран %q, вкладка %q, колонка %q, заголовок %q", screen, doc.Screen, doc.ActiveSubtab, doc.ActivePanel, doc.Heading)
		}
		if doc.Components[0].Type != "crumb" || doc.Components[0].Actions[0].Label != "‹ Ключи и секреты" ||
			doc.Components[0].Actions[0].Target != secretsScreen {
			t.Fatalf("%s: над содержимым нет «‹ Ключи и секреты»: %+v", screen, doc.Components[0])
		}
		bar := doc.Components[1]
		if bar.Type != "section" || bar.Style != "sticky" || len(bar.Children) != 1 || bar.Children[0].Subtitle != "planner-token" {
			t.Fatalf("%s: закреплённой строки нет: %+v", screen, bar)
		}
	}

	// Обзор вашего секрета: поле «Заменить значение» пустое, «Опасное» последним
	doc := serverDoc(t, s, entityAddr(secretsScreen, "planner-token", ""))
	f := fieldOf(doc, "secret-value", "value")
	if f == nil || f.Type != "secure" || f.Value != "" || !strings.Contains(f.Hint, "задан") {
		t.Fatalf("поле замены значения: %+v", f)
	}
	form := docByID(doc, "secret-value")
	if !form.Deferred || form.Submit == nil || form.Submit.Action != "save_secret_value" || form.Submit.Target != "planner-token" {
		t.Errorf("поле замены не под полосой сохранения: %+v", form)
	}
	last := doc.Components[len(doc.Components)-1]
	if last.ID != "secret-danger" || last.Style != "danger" || last.Title != "Опасное" {
		t.Errorf("«Опасное» не последнее: %q %q", last.ID, last.Title)
	}
	del := last.Children[0].Actions[0]
	if del.Action != "delete_secret" || del.Confirm == "" || del.ConfirmOK == "" || !strings.Contains(del.Confirm, "сервер MCP «sing»") {
		t.Errorf("«Удалить» вашего секрета: %+v", del)
	}
	// «заведён вами», «менялся» — из журнала: правка записи
	if r := docByID(doc, "secret-created"); r == nil || !strings.Contains(r.Caption, "вами") {
		t.Errorf("когда заведён: %+v", r)
	}
	if r := docByID(doc, "secret-changed"); r == nil || !strings.Contains(r.Caption, "ни разу") {
		t.Errorf("менялся: %+v", r)
	}

	// ключ SSH: публичная часть с «Скопировать», приватной нет, замены значением нет
	ssh := serverDoc(t, s, entityAddr(secretsScreen, "ssh:Microtik", ""))
	pub := docByID(ssh, "secret-public")
	sec, _ := fx.St.Secret("ssh:Microtik")
	if pub == nil || pub.Caption != sec.Public || len(pub.Actions) != 1 || pub.Actions[0].Action != "$copy" || pub.Actions[0].Target != sec.Public {
		t.Errorf("публичная часть: %+v", pub)
	}
	if docByID(ssh, "secret-value") != nil {
		t.Error("у ключа SSH есть поле замены значения")
	}
	if r := docByID(ssh, "secret-private"); r == nil || !strings.Contains(r.Caption, "Закрыта") {
		t.Errorf("про приватную часть: %+v", r)
	}

	// служебный: ни «Удалить», ни поля замены, ни «Опасного»; сказано, где он живёт
	svc := serverDoc(t, s, entityAddr(secretsScreen, "model:OpenAI", ""))
	if docByID(svc, "secret-danger") != nil || docByID(svc, "secret-value") != nil {
		t.Error("у служебного секрета есть «Опасное» или поле замены")
	}
	if owner := docByID(svc, "secret-owners"); owner == nil || len(owner.Children) == 0 ||
		owner.Children[0].Actions[0].Target != providerAddr("OpenAI", "") {
		t.Errorf("где живёт служебный секрет: %+v", owner)
	}
	for _, c := range svc.Components {
		walkthrough(&c, func(x *Component) {
			for _, a := range append(append([]Action{}, x.Actions...), x.Menu...) {
				if a.Action == "delete_secret" || a.Action == "replace_secret" || a.Action == "save_secret_value" {
					t.Errorf("у служебного секрета предложено %s", a.Action)
				}
			}
		})
	}
}

// «Кому выдан»: матрица «секрет × потребитель». Выдача и отзыв — нажатием, и это
// прежние действия; ядро, которое берёт секрет само, показано отдельно.
func TestSecretGrantsMatrix(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	doc := serverDoc(t, s, entityAddr(secretsScreen, "gitlab-token", secretTabGrants))
	m := docByID(doc, "grants-matrix")
	if m == nil || m.Type != "matrix" || len(m.Columns) != 1 || m.Columns[0] != "gitlab-token" || len(m.Children) != 1 {
		t.Fatalf("матрица: %+v", m)
	}
	cell := m.Children[0].Cells[0]
	if cell.State != "off" || cell.Action.Action != "grant_secret" || cell.Action.Target != "gitlab-sync~gitlab-token" {
		t.Errorf("клетка просящего модуля: %+v", cell)
	}
	// нажатие: выдано, и клетка переключилась на «отозвать»
	back, err := s.execute(actionRequest{Action: cell.Action.Action, Target: cell.Action.Target,
		Screen: entityAddr(secretsScreen, "gitlab-token", secretTabGrants)})
	if err != nil || back != entityAddr(secretsScreen, "gitlab-token", secretTabGrants) {
		t.Fatalf("выдача: %q (%v)", back, err)
	}
	if mod, _ := s.eng.Module("gitlab-sync"); !mod.HasGrant("gitlab-token") {
		t.Fatal("после «Выдать» модуль не получил секрет")
	}
	doc = serverDoc(t, s, entityAddr(secretsScreen, "gitlab-token", secretTabGrants))
	cell = docByID(doc, "grants-matrix").Children[0].Cells[0]
	if cell.State != "on" || cell.Action.Action != "revoke_secret" {
		t.Errorf("после выдачи клетка: %+v", cell)
	}
	if _, err := s.execute(actionRequest{Action: cell.Action.Action, Target: cell.Action.Target,
		Screen: entityAddr(secretsScreen, "gitlab-token", secretTabGrants)}); err != nil {
		t.Fatal(err)
	}
	if mod, _ := s.eng.Module("gitlab-sync"); mod.HasGrant("gitlab-token") {
		t.Error("после «Отозвать» модуль сохранил секрет")
	}
	// ядро, которое берёт ключ само, и ключ SSH, который модулю не выдаётся
	core := serverDoc(t, s, entityAddr(secretsScreen, "ssh:Microtik", secretTabGrants))
	if docByID(core, "grants-matrix") != nil || docByID(core, "grants-no-ssh") == nil {
		t.Error("у ключа SSH должна быть строка «не выдаётся», а не матрица")
	}
	if sec := docByID(core, "grants-core"); sec == nil || sec.Children[0].Subtitle != "сервер «Microtik»" ||
		sec.Children[0].Actions[0].Target != entityAddr(serversScreen, "Microtik", "") {
		t.Errorf("кто берёт ключ сам: %+v", sec)
	}
	// у токена входа тоже матрицы нет
	oauth := serverDoc(t, s, entityAddr(secretsScreen, "mcp:sing:oauth", secretTabGrants))
	if docByID(oauth, "grants-matrix") != nil || docByID(oauth, "grants-no-oauth") == nil {
		t.Error("у токена входа должна быть строка «не выдаётся»")
	}
	// секрета, которого никто не просит, — пустое объяснение, а не пустая матрица
	free := serverDoc(t, s, entityAddr(secretsScreen, "s3-secret", secretTabGrants))
	if docByID(free, "grants-matrix") != nil {
		t.Error("матрица у секрета, которого никто не просит")
	}
}

// Служебный секрет не удаляется и не заменяется: сервер отклоняет действие, а
// ваш — удаляется, и в журнале остаётся запись.
func TestServiceSecretIsNotDeletedNorReplaced(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	for _, name := range []string{"model:OpenAI", "agents:root-key", "mcp:sing:oauth", "push:vapid-private"} {
		for _, act := range []string{"delete_secret", "replace_secret", "save_secret_value"} {
			req := actionRequest{Action: act, Target: name, Params: map[string]string{"value": "подмена"}}
			if _, err := s.execute(req); err == nil {
				t.Errorf("%s над служебным %s не отклонено", act, name)
			}
			if _, ok := st.Secret(name); !ok {
				t.Fatalf("%s: служебный секрет %s исчез", act, name)
			}
		}
		if v, _ := st.SecretValue(name); strings.Contains(v, "подмена") {
			t.Errorf("значение служебного секрета %s подменено", name)
		}
	}
	// ваш удаляется, и в «Изменениях» журнала остаётся запись «удалён»
	if _, err := s.execute(actionRequest{Action: "delete_secret", Target: "s3-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Secret("s3-secret"); ok {
		t.Fatal("ваш секрет не удалился")
	}
	log, _ := st.SecretLogFiltered("s3-secret", store.SecretLogChanges, 10)
	if len(log) == 0 || log[0].Note != "удалён" {
		t.Errorf("записи об удалении нет: %+v", log)
	}
	// удалённый секрет, о котором осталась запись «удалён», просителем не считается
	orphans, _ := st.OrphanSecretRequests()
	for _, o := range orphans {
		if o.Name == "s3-secret" {
			t.Errorf("удалённый секрет назван «просили, а такого нет»: %+v", o)
		}
	}
}

// «Заменить значение» полем: набранное сохраняется и читается ядром, пустое
// поле — «не трогали»; замена пишется в журнал как правка, а значения в журнале
// нет.
func TestSaveSecretValueByField(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	here := entityAddr(secretsScreen, "gitlab-token", "")

	// пусто — не трогали
	back, err := s.execute(actionRequest{Action: "save_secret_value", Target: "gitlab-token", Screen: here,
		Params: map[string]string{"value": "   "}})
	if err != nil || back != here {
		t.Fatalf("пустое поле: %q (%v)", back, err)
	}
	if v, _ := st.SecretValue("gitlab-token"); v != saGitlabSentinel {
		t.Errorf("пустое поле изменило значение: %q", v)
	}
	if got, _ := st.SecretLogFiltered("gitlab-token", store.SecretLogChanges, 10); len(got) != 1 || got[0].Note != "заведён" {
		t.Errorf("пустое поле оставило след в журнале: %+v", got)
	}

	// набрано — заменено и прочитано ядром
	back, err = s.execute(actionRequest{Action: "save_secret_value", Target: "gitlab-token", Screen: here,
		Params: map[string]string{"value": saNewValueSentinel}})
	if err != nil || back != here {
		t.Fatalf("замена: %q (%v)", back, err)
	}
	if v, ok := st.SecretValue("gitlab-token"); !ok || v != saNewValueSentinel {
		t.Errorf("ядро читает %q, а набрано %q", v, saNewValueSentinel)
	}
	if sec, _ := st.Secret("gitlab-token"); sec.CreatedBy != store.SecretByHuman || sec.Kind != store.SecretToken {
		t.Errorf("замена значения изменила запись: %+v", sec)
	}
	changes, _ := st.SecretLogFiltered("gitlab-token", store.SecretLogChanges, 10)
	if len(changes) != 2 || changes[0].Note != "значение заменено" || !changes[0].Change {
		t.Errorf("правки в журнале: %+v", changes)
	}
	for _, c := range changes {
		if strings.Contains(c.Note+c.Who, saNewValueSentinel) {
			t.Errorf("значение попало в журнал: %+v", c)
		}
	}
	// экран «Обзор» отвечает, когда менялось
	doc := serverDoc(t, s, here)
	if r := docByID(doc, "secret-changed"); r == nil || !strings.Contains(r.Caption, "сегодня") {
		t.Errorf("когда менялось: %+v", r)
	}
	// ключ шифрования не подошёл к уже лежащему секрету — замена отклонена, и это
	// не ослаблено: прежнее значение на месте, а ошибка не носит значения
	if _, err := s.execute(actionRequest{Action: "save_secret_value", Target: "ssh:Microtik",
		Params: map[string]string{"value": "подмена"}}); err == nil {
		t.Error("ключ SSH заменён значением")
	}
}

// Открытие экранов само по себе не пишет в журнал «чтение»: экран спрашивает
// запись, а не значение.
func TestOpeningSecretScreensDoesNotLogReads(t *testing.T) {
	fx := secretsAgentsFix(t)
	before := fx.St.CountSecretLogFiltered("", store.SecretLogAll)
	reads := fx.St.CountSecretLogFiltered("", store.SecretLogReads)
	for _, screen := range saScreens(fx) {
		if _, err := fx.S.buildScreen(screen, "", "", 0); err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		saBodies(t, fx.S, screen)
	}
	if got := fx.St.CountSecretLogFiltered("", store.SecretLogAll); got != before {
		t.Errorf("открытие экранов добавило %d записей в журнал секретов", got-before)
	}
	if got := fx.St.CountSecretLogFiltered("", store.SecretLogReads); got != reads {
		t.Errorf("открытие экранов добавило %d «чтений»", got-reads)
	}
}

// Значение секрета, приватная часть ключа SSH, закрытая часть корня и код
// приглашения (после первого показа) не встречаются ни в разметке, ни в
// документе /api/ui, ни в ленивых частях — ни на одном экране обоих разделов.
func TestSecretsAndAgentsNeverShowValues(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S

	// приглашение: код виден ровно один раз, на экране приглашения
	if _, err := s.execute(actionRequest{Action: "invite_agent", Params: map[string]string{"name": "новая-машина", "role": "житель"}}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	code := s.freshInvite.Code
	s.mu.Unlock()
	if code == "" {
		t.Fatal("кода нет")
	}
	// первый запрос (разметка) забирает код, остальные его уже не видят: это и
	// есть «один раз»
	first := saBodies(t, s, entityAddr(agentsScreen, agentsNew, ""))
	if !strings.Contains(first["разметка"], code) {
		t.Fatalf("код не показан при первом открытии экрана приглашения")
	}
	if strings.Contains(first["/api/ui"], code) {
		t.Errorf("код показан и второй раз")
	}

	bad := append([]string{}, fx.Sentinels...)
	bad = append(bad, fx.PrivateLines...)
	bad = append(bad, code, saNewValueSentinel)
	if _, err := s.execute(actionRequest{Action: "save_secret_value", Target: "gitlab-token",
		Params: map[string]string{"value": saNewValueSentinel}}); err != nil {
		t.Fatal(err)
	}
	for _, screen := range saScreens(fx) {
		for kind, body := range saBodies(t, s, screen) {
			for _, secretText := range bad {
				if strings.Contains(body, secretText) {
					t.Errorf("%s (%s): виден секрет %q…", screen, kind, secretText[:min(24, len(secretText))])
				}
			}
			// и никакой строки PEM с закрытой частью
			for _, header := range []string{"BEGIN PRIVATE KEY", "BEGIN OPENSSH PRIVATE KEY", "BEGIN RSA PRIVATE KEY"} {
				if strings.Contains(body, header) {
					t.Errorf("%s (%s): виден заголовок закрытого ключа %q", screen, kind, header)
				}
			}
		}
	}
}

// Новый секрет: вид, имя, значение, заметка; ключ SSH генерируется там же;
// имя «new» занято под сам экран; без ключа шифрования вместо формы — объяснение.
func TestAddSecretScreenAndAction(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	doc := serverDoc(t, s, entityAddr(secretsScreen, secretsNew, ""))
	form := docByID(doc, "secret-new")
	if form == nil || form.Submit == nil || form.Submit.Action != "add_secret" || form.Deferred {
		t.Fatalf("форма добавления: %+v", form)
	}
	got := map[string]Field{}
	for _, f := range form.Fields {
		got[f.Name] = f
	}
	for name, typ := range map[string]string{"kind": "picker", "name": "text", "value": "secure", "note": "text", "private": "code"} {
		if got[name].Type != typ {
			t.Errorf("поле %q: %q, а ждали %q", name, got[name].Type, typ)
		}
	}
	if !got["private"].More || got["kind"].Value != "Ключ SSH — сгенерировать" {
		t.Errorf("приватный ключ для импорта должен быть под «Ещё», а вид по умолчанию — генерация: %+v %+v", got["private"], got["kind"])
	}
	// имя вписано из «просили, а такого нет»
	pre := serverDoc(t, s, entityAddr(secretsScreen, secretsNew, secretsNewName+"nextcloud-token"))
	if f := fieldOf(pre, "secret-new", "name"); f == nil || f.Value != "nextcloud-token" {
		t.Errorf("имя не вписано: %+v", f)
	}

	// токен с заметкой: завёл человек, заметка его
	back, err := s.execute(actionRequest{Action: "add_secret", Params: map[string]string{
		"kind": "Токен", "name": "nextcloud-token", "value": "значение-токена", "note": "для резервных копий"}})
	if err != nil || back != entityAddr(secretsScreen, "nextcloud-token", "") {
		t.Fatalf("добавление: %q (%v)", back, err)
	}
	sec, ok := st.Secret("nextcloud-token")
	if !ok || sec.CreatedBy != store.SecretByHuman || sec.Note != "для резервных копий" || sec.Kind != store.SecretToken {
		t.Errorf("запись: %+v", sec)
	}
	// теперь это ваш секрет, а «просили, а такого нет» погасло
	list := serverDoc(t, s, secretsScreen)
	if own := saSection(list, "secrets-own"); len(own.Children) != 7 {
		t.Errorf("ваших после добавления %d", len(own.Children))
	}
	for _, c := range findAll(list, "orphan-nextcloud-token") {
		t.Errorf("«просили, а такого нет» не погасло: %+v", c)
	}

	// ключ SSH генерируется; публичная часть на его экране
	back, err = s.execute(actionRequest{Action: "add_secret", Params: map[string]string{"kind": "Ключ SSH — сгенерировать", "name": "ssh:db-2"}})
	if err != nil || back != entityAddr(secretsScreen, "ssh:db-2", "") {
		t.Fatalf("генерация: %q (%v)", back, err)
	}
	if sec, _ := st.Secret("ssh:db-2"); sec == nil || sec.Public == "" || sec.CreatedBy != store.SecretByHuman {
		t.Errorf("сгенерированный ключ: %+v", sec)
	}
	// имя «new» занято, служебный не перезаписывается
	if _, err := s.execute(actionRequest{Action: "add_secret", Params: map[string]string{"kind": "Токен", "name": "new", "value": "x"}}); err == nil {
		t.Error("секрет назван «new»")
	}
	if _, err := s.execute(actionRequest{Action: "add_secret", Params: map[string]string{"kind": "Токен", "name": "model:OpenAI", "value": "x"}}); err == nil {
		t.Error("поверх служебного секрета завели ваш")
	}
	// импорт: приватный ключ — отдельным полем, прежний вызов с «value» тоже работает
	if _, err := s.execute(actionRequest{Action: "add_secret", Params: map[string]string{"kind": "Ключ SSH — импортировать", "name": "ssh:old", "private": "не ключ"}}); err == nil {
		t.Error("импорт не-ключа принят")
	}
}

// Без ключа шифрования секрет не кладётся: вместо формы — объяснение с дорогой на
// «Шифрование», у вкладки красный счётчик, а сохранение отклоняется хранилищем.
func TestNoEncryptionKeyNoSecrets(t *testing.T) {
	s, st := testServer(t)
	st.UseSecretBox(nil)
	doc := serverDoc(t, s, entityAddr(secretsScreen, secretsNew, ""))
	if docByID(doc, "secret-new") != nil || docByID(doc, "secret-new-no-key") == nil {
		t.Errorf("без ключа вместо формы должно быть объяснение: %+v", doc.Components)
	}
	list := serverDoc(t, s, secretsScreen)
	if b := list.Subtabs[1]; b.Badge != "нет ключа" || b.Level != LevelAlert {
		t.Errorf("вкладка «Шифрование» без ключа: %+v", b)
	}
	if shelf := findByType(list, "shelf"); shelf == nil || shelf.Children[0].ID != "secrets-no-key" {
		t.Errorf("полка без ключа: %+v", shelf)
	}
	if err := s.eng.AddSecret("без-ключа", store.SecretToken, "значение"); err == nil {
		t.Error("секрет сохранён открытым текстом")
	}
	crypt := serverDoc(t, s, entityAddr(secretsScreen, "", secretsTabCrypt))
	if r := docByID(crypt, "crypt-key"); r == nil || r.Badges[0].Text != "не задан" || r.Badges[0].Level != LevelAlert {
		t.Errorf("«Ключ шифрования» без ключа: %+v", r)
	}
	if docByID(crypt, "crypt-rotate") != nil {
		t.Error("без ключа предлагается смена ключа")
	}
}

// Секрет, который не читается нынешним ключом, не перезаписывается: сервер,
// поднятый с чужим SEMA_KEY, не должен молча затереть чужое. Ошибка не носит
// набранного значения, а прежнее остаётся на месте.
func TestReplaceRefusedWhenKeyDoesNotRead(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	right, err := secret.New("ключ-для-тестов")
	if err != nil || right == nil {
		t.Fatal(err)
	}
	other, _ := secret.New("чужой-ключ-совсем-другой")
	st.UseSecretBox(other)
	_, err = s.execute(actionRequest{Action: "save_secret_value", Target: "gitlab-token",
		Params: map[string]string{"value": saNewValueSentinel}})
	if err == nil || !strings.Contains(err.Error(), "не читается этим ключом") {
		t.Fatalf("замена под чужим ключом: %v", err)
	}
	if strings.Contains(err.Error(), saNewValueSentinel) {
		t.Errorf("ошибка носит набранное значение: %v", err)
	}
	st.UseSecretBox(right)
	if v, _ := st.SecretValue("gitlab-token"); v != saGitlabSentinel {
		t.Errorf("значение после отказа: %q", v)
	}
}

func findAll(doc *Document, id string) []*Component {
	var out []*Component
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.ID == id {
				out = append(out, c)
			}
		})
	}
	return out
}

// Журнал: двадцать строк, «Показать ещё», чипы «Чтения · Отказы · Изменения»; у
// секрета тот же журнал, отобранный по нему. Значения в нём нет.
func TestSecretsJournalFiltersAndPages(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	for i := 0; i < 30; i++ {
		st.LogSecret(store.SecretUse{Name: "ssh:srv-vpn", Who: "ядро · srv-vpn · df -h", Granted: true})
	}
	doc := serverDoc(t, s, entityAddr(secretsScreen, "", secretsTabLog))
	var titles []string
	for _, f := range doc.Filters {
		titles = append(titles, f.Title)
	}
	if len(titles) != 4 || !strings.HasPrefix(titles[1], "Чтения ·") || !strings.HasPrefix(titles[2], "Отказы ·") || !strings.HasPrefix(titles[3], "Изменения ·") {
		t.Errorf("чипы: %v", titles)
	}
	if doc.ActiveFilter != "" {
		t.Errorf("выбран отбор %q", doc.ActiveFilter)
	}
	section := doc.Components[0]
	if len(section.Children) != 20 {
		t.Errorf("строк первой страницы %d, а нужно 20", len(section.Children))
	}
	if more := doc.Components[len(doc.Components)-1]; more.Type != "button" || more.Actions[0].Action != "$more" {
		t.Errorf("нет «Показать ещё»: %+v", more)
	}
	// отбор «Отказы»: один отказ — модулю без права
	denied, err := s.buildScreen(entityAddr(secretsScreen, "", secretsTabLog), store.SecretLogDenied, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	rows := denied.Components[0].Children
	// два отказа: модулю без права и просьба имени, которого нет
	if len(rows) != 2 || rows[0].Badges[0].Level != LevelAlert || rows[1].Badges[0].Level != LevelAlert ||
		!strings.Contains(rows[0].Caption+rows[1].Caption, "право не выдано") {
		t.Errorf("отказы: %+v", rows)
	}
	changes, _ := s.buildScreen(entityAddr(secretsScreen, "", secretsTabLog), store.SecretLogChanges, "", 0)
	for _, r := range changes.Components[0].Children {
		if r.Badges[0].Text != "правка" {
			t.Errorf("в «Изменениях» не правка: %+v", r)
		}
	}
	// у секрета — только его записи
	one := serverDoc(t, s, entityAddr(secretsScreen, "yandex-app-password", secretsTabLog))
	rowsOfOne := 0
	for i := range one.Components {
		walkthrough(&one.Components[i], func(c *Component) {
			if strings.HasPrefix(c.ID, "secret-log-") {
				rowsOfOne++
				if strings.Contains(c.Subtitle+c.Caption, "ssh:srv-vpn") {
					t.Errorf("в журнале секрета чужая запись: %+v", c)
				}
			}
		})
	}
	if rowsOfOne == 0 {
		t.Error("в журнале секрета нет его записей")
	}
}

// «Шифрование» переехало с «Инструментов»: прежний адрес открывает его на новом
// месте, на «Инструментах» вкладки больше нет, а смена ключа осталась действием
// под прежним именем и возвращает на свою вкладку.
func TestEncryptionMovedFromTools(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	doc := serverDoc(t, s, "settings:tools~crypt")
	if doc.Screen != entityAddr(secretsScreen, "", secretsTabCrypt) || doc.ActivePanel != secretsScreen {
		t.Errorf("прежний адрес открыл %q (колонка %q)", doc.Screen, doc.ActivePanel)
	}
	crypt := docByID(doc, "crypt")
	if crypt == nil {
		t.Fatal("на вкладке «Шифрование» нет раздела «Безопасность»")
	}
	ids := map[string]bool{}
	for _, r := range crypt.Children {
		ids[r.ID] = true
	}
	for _, want := range []string{"crypt-key", "crypt-sealed", "crypt-not-sealed", "crypt-stay", "crypt-rotate"} {
		if !ids[want] {
			t.Errorf("на вкладке нет строки %q: %v", want, ids)
		}
	}
	// разрушительное действие строки tidyRows уносит под «···»
	rotRow := docByID(doc, "crypt-rotate")
	rot := hasAction(append(append([]Action{}, rotRow.Actions...), rotRow.Menu...), "rotate_key")
	if rot == nil || rot.Prompt == "" || rot.Confirm == "" || rot.ConfirmOK == "" {
		t.Fatalf("смена ключа: %+v", rot)
	}
	// «N из N зашифровано» — число секретов, все закрыты
	total, sealed := fx.St.SecretCounts()
	if r := docByID(doc, "crypt-sealed"); r == nil || r.Badges[0].Text != strconv.Itoa(sealed)+" из "+strconv.Itoa(total) || sealed != total {
		t.Errorf("«N из N»: %+v (%d из %d)", r, sealed, total)
	}
	// на «Инструментах» вкладки больше нет
	tools := serverDoc(t, s, "settings:tools")
	for _, f := range tools.Subtabs {
		if f.Title == "Шифрование" {
			t.Errorf("на «Инструментах» осталась вкладка «Шифрование»")
		}
	}
	// вкладка раздела видна с полосы вкладок: ключа нет — беда
	if got := serverDoc(t, s, secretsScreen).Subtabs[1]; got.Badge != "" {
		t.Errorf("счётчик «Шифрования» при заданном ключе: %+v", got)
	}
	// смена ключа возвращает на вкладку «Шифрование»
	back, err := s.execute(actionRequest{Action: "rotate_key", Params: map[string]string{"value": "новый-ключ-для-проверки"}})
	if err != nil || back != entityAddr(secretsScreen, "", secretsTabCrypt) {
		t.Errorf("после смены ключа: %q (%v)", back, err)
	}
	// и всё, что было зашифровано, читается новым ключом
	if v, _ := fx.St.SecretValue("gitlab-token"); v != saGitlabSentinel {
		t.Errorf("после смены ключа секрет читается как %q", v)
	}
}

// Прежние адреса открываются осмысленно: секрет без вкладки — «Обзор», имя без
// секрета, но с журналом, — журнал, имени нет вовсе — объяснение.
func TestSecretsOldAddressesOpen(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	// «settings:secrets@имя» когда-то был журналом: теперь это «Обзор» с вкладкой «Журнал»
	doc := serverDoc(t, s, "settings:secrets@ssh:Microtik")
	if doc.Heading != "ssh:Microtik" || docByID(doc, "secret-facts") == nil {
		t.Errorf("settings:secrets@имя не открыл «Обзор»: %q", doc.Heading)
	}
	// имя, которого нет, а обращения были: журнал на месте
	miss := serverDoc(t, s, "settings:secrets@nextcloud-token")
	if miss.Components[0].Type != "crumb" || docByID(miss, "secret-log-0") == nil {
		t.Errorf("имя без секрета, но с обращениями: нет пути назад или журнала: %+v", miss.Components)
	}
	none := serverDoc(t, s, "settings:secrets@совсем-нет")
	if none.Components[0].Type != "crumb" || findByType(none, "row") == nil {
		t.Errorf("несуществующий секрет: %+v", none.Components)
	}
	// ящик, провайдер, сервер: «В настройках» ведёт на настоящие экраны
	for _, a := range []string{entityAddr(mailScreen, "owner@example.com", mailTabConn), providerAddr("OpenAI", ""),
		entityAddr(serversScreen, "Microtik", ""), entityAddr(mcpScreen, "sing", "")} {
		if _, err := s.buildScreen(a, "", "", 0); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
}
