package api

// «Агенты» (2 октября 2026): две вкладки раздела, экран агента, приглашение
// своим экраном, корневой сертификат карточкой, «Шлюз». Проверяется сборка
// документа и действия; настоящий Chrome — в browser_secrets_agents_test.go.
// Живых проводов к агентам в тестах нет: их называет проба (Server.linksVia).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// У раздела две вкладки; шапка одна на обеих: одна кнопка «Пригласить агента»;
// в порядке экрана — полка, список, корень, справка, «Тонкая настройка» и журнал
// последним, ленивым.
func TestAgentsSectionTabs(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	for _, tab := range []string{agentsTabList, agentsTabGateway} {
		screen := entityAddr(agentsScreen, "", tab)
		doc := serverDoc(t, s, screen)
		if doc.Screen != screen || doc.ActiveSubtab != screen || doc.ActivePanel != agentsScreen {
			t.Errorf("%s: экран %q, вкладка %q, колонка %q", screen, doc.Screen, doc.ActiveSubtab, doc.ActivePanel)
		}
		var titles []string
		for _, f := range doc.Subtabs {
			titles = append(titles, f.Title)
		}
		if strings.Join(titles, "|") != "Агенты|Шлюз" {
			t.Errorf("%s: вкладки %v", screen, titles)
		}
		if len(doc.ToolbarActions) != 1 || doc.ToolbarActions[0].Label != "Пригласить агента" ||
			doc.ToolbarActions[0].Target != entityAddr(agentsScreen, agentsNew, "") || doc.ToolbarActions[0].Style != "primary" {
			t.Errorf("%s: шапка раздела: %+v", screen, doc.ToolbarActions)
		}
	}
	doc := serverDoc(t, s, agentsScreen)
	// у вкладок счётчики: сменившийся отпечаток ждёт решения, у шлюза провода нет
	if b := doc.Subtabs[0]; b.Badge != "1 ждут" || b.Level != LevelAlert {
		t.Errorf("вкладка «Агенты»: %+v", b)
	}
	if b := doc.Subtabs[1]; b.Badge != "провода нет" || b.Level != LevelAlert {
		t.Errorf("вкладка «Шлюз»: %+v", b)
	}
	// порядок: полка первой, журнал и «Тонкая настройка» в конце — настройка перед журналом
	if doc.Components[0].Type != "shelf" {
		t.Errorf("первым должна стоять полка, а стоит %q", doc.Components[0].Type)
	}
	var order []string
	for _, c := range doc.Components {
		order = append(order, c.ID)
	}
	last := doc.Components[len(doc.Components)-1]
	if last.ID != agentsLogPart || last.Lazy != agentsLogPart || last.Tier != tierTail || !last.Collapsed {
		t.Errorf("журнал должен быть последним, ленивым и свёрнутым: %v", order)
	}
	if fold := doc.Components[len(doc.Components)-2]; fold.ID != rareID {
		t.Errorf("«Тонкая настройка» должна стоять перед журналом: %v", order)
	}
	// журнал приезжает частью: двадцать строк и «Показать ещё»
	part, err := s.buildPart(agentsScreen, agentsLogPart, "", 0)
	if err != nil || len(part) == 0 {
		t.Fatalf("часть журнала: %v %v", len(part), err)
	}
}

// Строка списка тонкая: не больше двух капсул, одна видимая кнопка, один переход
// на экран агента, «···»; разрушительное только под «···» и с вопросом.
func TestAgentsListRowsAreThin(t *testing.T) {
	fx := secretsAgentsFix(t)
	doc := serverDoc(t, fx.S, agentsScreen)
	list := saSection(doc, "agents-list")
	if list == nil || len(list.Children) != 3 {
		t.Fatalf("агентов в списке: %+v", list)
	}
	byName := map[string]Component{}
	for _, row := range list.Children {
		byName[row.Subtitle] = row
		if len(row.Badges) > 2 {
			t.Errorf("%s: капсул %d, а не больше двух", row.Subtitle, len(row.Badges))
		}
		buttons, gotos := 0, 0
		for _, a := range row.Actions {
			if a.Action == "$goto" {
				gotos++
				if a.Target != entityAddr(agentsScreen, row.Subtitle, "") {
					t.Errorf("%s: строка ведёт на %q", row.Subtitle, a.Target)
				}
				continue
			}
			buttons++
			if a.Style == "destructive" {
				t.Errorf("%s: разрушительное %q на виду", row.Subtitle, a.Action)
			}
		}
		if buttons > 1 || gotos != 1 {
			t.Errorf("%s: видимых кнопок %d и переходов %d, а нужны не больше одной и один", row.Subtitle, buttons, gotos)
		}
		if len(row.Menu) == 0 || row.Avatar == "" || row.Caption == "" {
			t.Errorf("%s: нет «···», плитки или фразы: %+v", row.Subtitle, row)
		}
		for _, a := range row.Menu {
			if (a.Action == "revoke_agent" || a.Action == "forget_agent" || a.Action == "accept_agent_fp") && (a.Confirm == "" || a.ConfirmOK == "") {
				t.Errorf("%s: %s без вопроса: %+v", row.Subtitle, a.Action, a)
			}
		}
	}
	// на связи — зелёная капсула со сроком словами; сменившийся отпечаток — тревожная
	if b := byName["мак"].Badges; len(b) != 1 || b[0].Text != "на связи · 3 ч 12 мин" || b[0].Level != LevelOK {
		t.Errorf("мак: %+v", b)
	}
	if b := byName["рабочая"].Badges; len(b) != 1 || b[0].Text != "отпечаток сменился" || b[0].Level != LevelAlert {
		t.Errorf("рабочая: %+v", b)
	}
	if b := byName["шлюз"].Badges; len(b) != 2 || b[0].Text != "не на связи" || b[1].Text != "шлюз" {
		t.Errorf("шлюз: %+v", b)
	}
	// кнопка: у жителя с разрешённым «кодом» — «Проверить связь», у шлюза её нет, а у
	// сменившего отпечаток — принять, и с вопросом
	if a := hasAction(byName["мак"].Actions, "check_agent"); a == nil {
		t.Errorf("у мака нет «Проверить связь»: %+v", byName["мак"].Actions)
	}
	if a := hasAction(byName["шлюз"].Actions, "check_agent"); a != nil {
		t.Errorf("у шлюза «Проверить связь» зовёт «код», которого у шлюза нет: %+v", a)
	}
	if a := hasAction(byName["рабочая"].Actions, "accept_agent_fp"); a == nil || a.Confirm == "" || a.ConfirmOK == "" {
		t.Errorf("у рабочей нет «Принять новый отпечаток…» с вопросом: %+v", byName["рабочая"].Actions)
	}
	// полка: сменившийся отпечаток с «Было → Стало»
	shelf := doc.Components[0]
	if len(shelf.Children) != 1 || shelf.Children[0].ID != "agent-wait-fp-рабочая" || len(shelf.Children[0].Facts) != 2 {
		t.Errorf("полка: %+v", shelf)
	}
}

// Корневой сертификат — карточка, а не строка общего списка: отпечаток виден,
// закрытой части нет нигде, а действий над корнем нет.
func TestRootCertificateCardIsNotInTheList(t *testing.T) {
	fx := secretsAgentsFix(t)
	doc := serverDoc(t, fx.S, agentsScreen)
	card := saSection(doc, "agents-ca")
	if card == nil || card.Title != "Удостоверяющий центр Сёмы" || len(card.Children) != 1 {
		t.Fatalf("карточка корня: %+v", card)
	}
	fp := fx.S.eng.RootFingerprint()
	row := card.Children[0]
	if fp == "" || len(row.Facts) != 1 || row.Facts[0].Label != "Отпечаток" || row.Facts[0].Value != fp {
		t.Errorf("отпечаток корня не виден: %+v", row.Facts)
	}
	if !strings.Contains(row.Caption, "наружу не выдаётся") {
		t.Errorf("не сказано, что закрытая часть не выдаётся: %q", row.Caption)
	}
	if len(row.Actions) != 1 || row.Actions[0].Action != "$copy" || row.Actions[0].Target != fp {
		t.Errorf("кнопки карточки: %+v", row.Actions)
	}
	for _, a := range append(append([]Action{}, row.Actions...), row.Menu...) {
		if strings.Contains(a.Action, "delete") || strings.Contains(a.Action, "root") || a.Style == "destructive" {
			t.Errorf("над корнем появилось действие: %+v", a)
		}
	}
	// корень не среди агентов
	for _, r := range saSection(doc, "agents-list").Children {
		if strings.Contains(r.Subtitle, "Корень") || strings.Contains(r.Subtitle, "Удостоверяющий") {
			t.Errorf("корень стоит среди агентов: %+v", r)
		}
	}
}

// Экран агента: три вкладки, закреплённая строка, «Опасное» последним с вопросом у
// каждой кнопки; у сменившего отпечаток — «Было → Стало».
func TestAgentScreenTabsAndDanger(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	for _, tab := range []string{agentsTabList, agentTabCaps, agentsTabLog} {
		screen := entityAddr(agentsScreen, "мак", tab)
		doc := serverDoc(t, s, screen)
		var titles []string
		for _, f := range doc.Subtabs {
			titles = append(titles, f.Title)
		}
		if strings.Join(titles, "|") != "Обзор|Способности|Журнал" {
			t.Errorf("%s: вкладки %v", screen, titles)
		}
		if doc.Screen != screen || doc.ActiveSubtab != screen || doc.ActivePanel != agentsScreen || doc.Heading != "мак" {
			t.Errorf("%s: экран %q, вкладка %q, колонка %q, заголовок %q", screen, doc.Screen, doc.ActiveSubtab, doc.ActivePanel, doc.Heading)
		}
		if doc.Components[0].Type != "crumb" || doc.Components[0].Actions[0].Label != "‹ Агенты" || doc.Components[0].Actions[0].Target != agentsScreen {
			t.Fatalf("%s: над содержимым нет «‹ Агенты»: %+v", screen, doc.Components[0])
		}
		bar := doc.Components[1]
		if bar.Type != "section" || bar.Style != "sticky" || len(bar.Children) != 1 || bar.Children[0].Subtitle != "мак" {
			t.Fatalf("%s: закреплённой строки нет: %+v", screen, bar)
		}
	}
	doc := serverDoc(t, s, entityAddr(agentsScreen, "мак", ""))
	// обзор: роль, отпечаток с «Скопировать», выдан и истекает, последняя связь
	for _, id := range []string{"agent-role", "agent-fingerprint", "agent-issued", "agent-expires", "agent-seen", "agent-memory"} {
		if docByID(doc, id) == nil {
			t.Errorf("в «Обзоре» нет строки %q", id)
		}
	}
	if r := docByID(doc, "agent-seen"); !strings.Contains(r.Caption, "сейчас на связи 3 ч 12 мин") {
		t.Errorf("последняя связь: %q", r.Caption)
	}
	if r := docByID(doc, "agent-fingerprint"); len(r.Actions) != 1 || r.Actions[0].Action != "$copy" || r.Actions[0].Target == "" {
		t.Errorf("отпечаток агента без «Скопировать»: %+v", r.Actions)
	}
	last := doc.Components[len(doc.Components)-1]
	if last.ID != "agent-danger" || last.Style != "danger" || last.Title != "Опасное" {
		t.Fatalf("«Опасное» не последнее: %q", last.ID)
	}
	var got []string
	for _, r := range last.Children {
		for _, a := range r.Actions {
			got = append(got, a.Action)
			if a.Confirm == "" || a.ConfirmOK == "" || a.Style != "destructive" {
				t.Errorf("кнопка «Опасного» %q без вопроса или не красная: %+v", a.Action, a)
			}
		}
	}
	if strings.Join(got, ",") != "revoke_agent,forget_agent" {
		t.Errorf("«Опасное» у агента в порядке: %v", got)
	}
	// у сменившего отпечаток «Принять» — первая, и тревога вверху обзора
	work := serverDoc(t, s, entityAddr(agentsScreen, "рабочая", ""))
	if r := docByID(work, "agent-fp-changed"); r == nil || len(r.Facts) != 2 || r.Facts[0].Label != "Было" || r.Facts[1].Label != "Стало" {
		t.Errorf("«Было → Стало»: %+v", r)
	}
	danger := work.Components[len(work.Components)-1]
	if danger.Children[0].Actions[0].Action != "accept_agent_fp" {
		t.Errorf("первой в «Опасном» должна быть «Принять»: %+v", danger.Children[0].Actions)
	}
	// шапка: одна главная кнопка и «···»
	bar := work.Components[1].Children[0]
	if len(bar.Actions) != 1 || bar.Actions[0].Action != "accept_agent_fp" || len(bar.Menu) == 0 || len(bar.Selects) != 1 {
		t.Errorf("шапка рабочей: действий %d, «···» %d, списков %d", len(bar.Actions), len(bar.Menu), len(bar.Selects))
	}
	// переключатель ведёт к соседу на той же вкладке
	back, err := s.execute(actionRequest{Action: "entity_goto", Target: agentsScreen + "~" + agentTabCaps, Params: map[string]string{"value": "шлюз"}})
	if err != nil || back != entityAddr(agentsScreen, "шлюз", agentTabCaps) {
		t.Errorf("переключатель: %q (%v)", back, err)
	}
}

// Способность разрешает человек поимённо: новая выключена, тумблер включает
// ровно её, неразрешённую Сёма не зовёт вовсе, а тумблера «разрешить всё» нет.
func TestAgentCapabilityIsAllowedOnlyByName(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	here := entityAddr(agentsScreen, "мак", agentTabCaps)
	doc := serverDoc(t, s, here)
	caps := saSection(doc, "agent-caps")
	if caps == nil || len(caps.Children) != 3 {
		t.Fatalf("способности: %+v", caps)
	}
	state := map[string]string{}
	toggles := 0
	for _, r := range caps.Children {
		state[r.Subtitle] = r.Toggle
		toggles++
		if a := r.Actions[0]; a.Action != "allow_agent" || a.Target != "мак~"+r.Subtitle {
			t.Errorf("тумблер %q: %+v", r.Subtitle, a)
		}
	}
	if state["код"] != "on" || state["память"] != "on" || state["уведомления"] != "off" {
		t.Errorf("тумблеры: %v", state)
	}
	// ни «разрешить всё», ни пачкой: действие одно и по одному имени
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
				if strings.Contains(a.Action, "allow") && a.Action != "allow_agent" {
					t.Errorf("появилось действие %q", a.Action)
				}
				if a.Action == "allow_agent" && !strings.Contains(a.Target, "~") {
					t.Errorf("«разрешить» без имени способности: %+v", a)
				}
			}
		})
	}
	// неразрешённая не зовётся: «уведомления» у мака выключены
	if _, err := s.eng.AskAgent(context.Background(), "мак", "уведомления", "уведомления.показать", nil); err == nil ||
		!strings.Contains(err.Error(), "не разрешена") {
		t.Errorf("неразрешённая способность вызвана или отказ невнятный: %v", err)
	}
	// тумблер включает ровно её
	back, err := s.execute(actionRequest{Action: "allow_agent", Target: "мак~уведомления", Screen: here})
	if err != nil || back != here {
		t.Fatalf("включение: %q (%v)", back, err)
	}
	if !s.eng.AgentAllowed("мак", "уведомления") || !s.eng.AgentAllowed("мак", "код") {
		t.Error("после включения «уведомлений» что-то из способностей не разрешено")
	}
	if a, _ := st.Agent("мак"); len(a.Allowed) != 3 {
		t.Errorf("разрешено %v", a.Allowed)
	}
	// второе нажатие забирает
	if _, err := s.execute(actionRequest{Action: "allow_agent", Target: "мак~уведомления", Screen: here}); err != nil {
		t.Fatal(err)
	}
	if s.eng.AgentAllowed("мак", "уведомления") {
		t.Error("«уведомления» остались разрешёнными после второго нажатия")
	}
	// агент без единой разрешённой способности — на полке «Ждут вас»
	if err := st.SaveAgent(&store.Agent{Name: "новый", Role: engine.RoleResident, Can: []string{"код", "память"}, State: store.AgentOK}); err != nil {
		t.Fatal(err)
	}
	list := serverDoc(t, s, agentsScreen)
	found := false
	for _, r := range list.Components[0].Children {
		found = found || r.ID == "agent-wait-caps-новый"
	}
	if !found {
		t.Errorf("агент без разрешённых способностей не на полке: %+v", list.Components[0])
	}
	// и вкладка «Способности» говорит о числе до захода в неё
	if b := serverDoc(t, s, entityAddr(agentsScreen, "новый", "")).Subtabs[1]; b.Badge != "2 не разрешены" {
		t.Errorf("вкладка «Способности»: %+v", b)
	}
}

// Отзыв действует немедленно и требует вопроса; принять новый отпечаток — тоже;
// «Забыть» уводит в список, потому что экрана агента больше нет.
func TestAgentRevokeAcceptForget(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	here := entityAddr(agentsScreen, "мак", "")
	back, err := s.execute(actionRequest{Action: "revoke_agent", Target: "мак", Screen: here})
	if err != nil || back != here {
		t.Fatalf("отзыв: %q (%v)", back, err)
	}
	a, _ := st.Agent("мак")
	if a.State != store.AgentRevoked || len(a.Allowed) != 0 || s.eng.AgentAllowed("мак", "код") {
		t.Errorf("после отзыва: %+v", a)
	}
	// экран отозванного: «Новое приглашение» главной кнопкой, «Отозвать» уже нет
	doc := serverDoc(t, s, here)
	bar := doc.Components[1].Children[0]
	if len(bar.Actions) != 1 || bar.Actions[0].Action != "invite_agent" || bar.Actions[0].Target != "мак" {
		t.Errorf("шапка отозванного: %+v", bar.Actions)
	}
	for _, r := range doc.Components[len(doc.Components)-1].Children {
		if r.Actions[0].Action == "revoke_agent" {
			t.Error("у отозванного снова предложено «Отозвать»")
		}
	}
	// принять отпечаток
	back, err = s.execute(actionRequest{Action: "accept_agent_fp", Target: "рабочая", Screen: entityAddr(agentsScreen, "рабочая", "")})
	if err != nil || back != entityAddr(agentsScreen, "рабочая", "") {
		t.Fatalf("принять: %q (%v)", back, err)
	}
	if w, _ := st.Agent("рабочая"); w.State != store.AgentOK || w.NewFP != "" {
		t.Errorf("после принятия: %+v", w)
	}
	// забыть
	back, err = s.execute(actionRequest{Action: "forget_agent", Target: "шлюз", Screen: entityAddr(agentsScreen, "шлюз", "")})
	if err != nil || back != agentsScreen {
		t.Fatalf("забыть: %q (%v)", back, err)
	}
	if _, ok := st.Agent("шлюз"); ok {
		t.Error("агент остался в справочнике")
	}
	// экран забытого — объяснение с дорогой назад, а не ошибка
	gone := serverDoc(t, s, entityAddr(agentsScreen, "шлюз", ""))
	if gone.Components[0].Type != "crumb" || findByType(gone, "row") == nil {
		t.Errorf("экран забытого агента: %+v", gone.Components)
	}
}

// Приглашение: код виден один раз со сроком, дальше «Ждут агента» знает его по
// отпечатку; «Отменить» работает по отпечатку и, как прежде, по коду; имя «new»
// и «~» не принимаются.
func TestInviteCodeShownOnceAndCancelByRef(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	back, err := s.execute(actionRequest{Action: "invite_agent", Params: map[string]string{"name": "ноут", "role": "житель"}})
	if err != nil || back != entityAddr(agentsScreen, agentsNew, "") {
		t.Fatalf("приглашение: %q (%v)", back, err)
	}
	s.mu.Lock()
	code := s.freshInvite.Code
	s.mu.Unlock()
	doc := serverDoc(t, s, back)
	fresh := docByID(doc, "invite-code")
	if fresh == nil || fresh.Subtitle != code || !strings.Contains(fresh.Caption, "действует до") || !strings.Contains(fresh.Caption, "один раз") ||
		len(fresh.Actions) != 1 || fresh.Actions[0].Action != "$copy" || fresh.Actions[0].Target != code {
		t.Fatalf("карточка кода: %+v", fresh)
	}
	// второй раз — кода нет, а приглашение в «Ждут агента» есть и называется отпечатком
	again := serverDoc(t, s, back)
	if docByID(again, "invite-code") != nil {
		t.Error("код показан второй раз")
	}
	invites, _ := st.Invites()
	if len(invites) != 1 {
		t.Fatalf("приглашений %d", len(invites))
	}
	row := docByID(again, "invite-"+invites[0].Ref())
	if row == nil || row.Actions[0].Action != "drop_invite" || row.Actions[0].Target != invites[0].Ref() || row.Actions[0].Target == code {
		t.Fatalf("строка ждущего приглашения: %+v", row)
	}
	// форма: имя и роль, «Создать код»; «Опасного» и других кнопок нет
	form := docByID(again, "agent-invite")
	if form == nil || form.Submit == nil || form.Submit.Action != "invite_agent" || form.Deferred {
		t.Errorf("форма приглашения: %+v", form)
	}
	// отмена по отпечатку
	if _, err := s.execute(actionRequest{Action: "drop_invite", Target: invites[0].Ref(), Screen: back}); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.Invites(); len(left) != 0 {
		t.Errorf("приглашение не отменилось: %+v", left)
	}
	// прежний вызов с кодом тоже работает
	if _, err := s.execute(actionRequest{Action: "invite_agent", Params: map[string]string{"name": "второй", "role": "житель"}}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	code2 := s.freshInvite.Code
	s.mu.Unlock()
	if _, err := s.execute(actionRequest{Action: "drop_invite", Target: code2}); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.Invites(); len(left) != 0 {
		t.Errorf("приглашение по коду не отменилось: %+v", left)
	}
	// имя «new» занято под экран приглашения, «~» отделяет вкладку
	for _, bad := range []string{"new", "а~б"} {
		if _, err := s.execute(actionRequest{Action: "invite_agent", Params: map[string]string{"name": bad, "role": "житель"}}); err == nil {
			t.Errorf("приглашение для имени %q принято", bad)
		}
	}
	// «Новое приглашение» из строки агента берёт его роль, а не значение формы
	if _, err := s.execute(actionRequest{Action: "invite_agent", Target: "шлюз"}); err != nil {
		t.Fatal(err)
	}
	if invites, _ := st.Invites(); len(invites) != 1 || invites[0].Role != engine.RoleGateway {
		t.Errorf("новое приглашение шлюзу: %+v", invites)
	}
}

// «Шлюз»: состояние связи, предупреждения про правила доступа, счётчики, «Тонкая
// настройка» — адрес шлюза и куда он вносит запросы.
func TestGatewayTab(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	doc := serverDoc(t, s, entityAddr(agentsScreen, "", agentsTabGateway))
	state := saSection(doc, "gateway-state")
	if state == nil {
		t.Fatalf("раздела «Вход из интернета» нет: %+v", doc.Components)
	}
	ids := map[string]Component{}
	for _, r := range state.Children {
		ids[r.ID] = r
	}
	// предупреждения: вход без пароля из своей сети включён, доверие заголовкам прокси выключено
	if w, ok := ids["gateway-warning"]; !ok || !strings.Contains(w.Caption, "вход без пароля из своей сети") || !strings.Contains(w.Caption, "доверие заголовкам прокси") {
		t.Errorf("предупреждение: %+v", w)
	}
	gw, ok := ids["gateway"]
	if !ok || gw.Badges[0].Text != "провода нет" || gw.Badges[0].Level != LevelAlert {
		t.Fatalf("шлюз: %+v", gw)
	}
	var props []string
	for _, p := range gw.Props {
		props = append(props, p.Label)
	}
	for _, want := range []string{"Звоним на", "Вносит запросы в", "Потоков сейчас", "Потоков за запуск"} {
		found := false
		for _, p := range props {
			found = found || p == want
		}
		if !found {
			t.Errorf("у шлюза нет строки %q: %v", want, props)
		}
	}
	// «Тонкая настройка» вкладки: адрес шлюза и куда он вносит запросы
	fields := rareFieldsOf(rareBody(t, s, entityAddr(agentsScreen, "", agentsTabGateway)))
	for _, name := range []string{"gateway", "upstream"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("в «Тонкой настройке» шлюза нет поля %q", name)
		}
	}
	// а где слушать — на вкладке «Агенты»
	listen := rareFieldsOf(rareBody(t, s, agentsScreen))
	if _, ok := listen["listen"]; !ok {
		t.Error("в «Тонкой настройке» агентов нет «Где слушать»")
	}
	// без шлюза — объяснение, а не пустота
	if err := fx.St.SetSetting("agents.gateway", ""); err != nil {
		t.Fatal(err)
	}
	if err := fx.St.DeleteAgent("шлюз"); err != nil {
		t.Fatal(err)
	}
	empty := serverDoc(t, s, entityAddr(agentsScreen, "", agentsTabGateway))
	if r := saSection(empty, "gateway-state").Children[len(saSection(empty, "gateway-state").Children)-1]; r.Style != "empty" {
		t.Errorf("без шлюза нет объяснения: %+v", r)
	}
}

// Журнал агента: чипы «Все · Вызовы · Неудачи», двадцать строк, «Показать ещё».
func TestAgentJournalFiltersAndPages(t *testing.T) {
	fx := secretsAgentsFix(t)
	s, st := fx.S, fx.St
	for i := 0; i < 30; i++ {
		st.LogAgent(store.AgentUse{Agent: "мак", What: "←память.memory_recall", OK: true, Detail: "ответ"})
	}
	screen := entityAddr(agentsScreen, "мак", agentsTabLog)
	doc := serverDoc(t, s, screen)
	var titles []string
	for _, f := range doc.Filters {
		titles = append(titles, f.Title)
	}
	if len(titles) != 3 || !strings.HasPrefix(titles[1], "Вызовы ·") || !strings.HasPrefix(titles[2], "Неудачи ·") {
		t.Errorf("чипы: %v", titles)
	}
	rows := 0
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if strings.HasPrefix(c.ID, "agent-log-") {
				rows++
			}
		})
	}
	if rows != 20 {
		t.Errorf("строк первой страницы %d, а нужно 20", rows)
	}
	if more := doc.Components[len(doc.Components)-1]; more.Type != "button" || more.Actions[0].Action != "$more" {
		t.Errorf("нет «Показать ещё»: %+v", more)
	}
	fail, _ := s.buildScreen(screen, store.AgentLogFail, "", 0)
	n := 0
	for i := range fail.Components {
		walkthrough(&fail.Components[i], func(c *Component) {
			if strings.HasPrefix(c.ID, "agent-log-") {
				n++
				if c.Level != LevelAlert {
					t.Errorf("в «Неудачах» не неудача: %+v", c)
				}
			}
		})
	}
	if n != 1 {
		t.Errorf("неудач у мака %d, а в журнале одна", n)
	}
}

// Прежние адреса и переходы из уведомлений ведут на настоящие экраны: раздел,
// вкладка, агент на своей вкладке, «Шлюз»; экраны, которых нет, объясняют себя.
func TestAgentsOldAddressesAndNotificationsLead(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	for _, addr := range []string{"settings:agents", engine.AgentScreen("", engine.AgentTabGateway), engine.AgentScreen("мак", ""),
		engine.AgentScreen("мак", "caps"), engine.AgentScreen("рабочая", "log"), engine.SecretScreen("", "crypt"),
		engine.SecretScreen("gitlab-token", ""), engine.SecretScreen("gitlab-token", "grants"), engine.SecretScreen("", "log")} {
		doc, err := s.buildScreen(addr, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if doc.Screen != addr || doc.ActivePanel == "" {
			t.Errorf("%s: открыл %q (колонка %q)", addr, doc.Screen, doc.ActivePanel)
		}
	}
	if engine.AgentScreen("мак", "caps") != entityAddr(agentsScreen, "мак", agentTabCaps) ||
		engine.SecretScreen("x", "grants") != entityAddr(secretsScreen, "x", secretTabGrants) {
		t.Error("адреса движка разошлись с адресами экранов")
	}
	// «Агент пропал со связи» и сменившийся отпечаток ведут на экран агента:
	// это держит engine.TestAgentNotificationsLeadToAgentScreen
}

// Адреса экранов секретов и агентов строит один помощник: уведомления ядра — engine.SecretScreen и
// engine.AgentScreen, экраны — entityAddr. Склейка «settings:secrets@» + имя где-то ещё — это адрес
// без вкладки, который забудут обновить.
func TestNoHandWrittenSecretAndAgentAddressesInCode(t *testing.T) {
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
			slash := filepath.ToSlash(path)
			if strings.HasSuffix(slash, "engine/secrets.go") || strings.HasSuffix(slash, "engine/agent_link.go") {
				return nil // здесь они и живут
			}
			for _, bad := range []string{`"settings:secrets@" +`, `"settings:secrets@"+`, `secretsScreen + "@" +`, `secretsScreen+"@"+`,
				`"settings:agents@" +`, `"settings:agents@"+`, `agentsScreen + "@" +`, `agentsScreen+"@"+`,
				`"settings:tools~crypt"`, `"settings:tools~" + toolsTabCrypt`} {
				if strings.Contains(string(raw), bad) {
					found = append(found, path+": "+bad)
				}
			}
			return nil
		})
	}
	if len(found) > 0 {
		t.Errorf("адреса секретов и агентов склеиваются руками: %v", found)
	}
}

// Ни одно действие и ни одна настройка прежних экранов «Ключей и секретов» и
// «Агентов» не пропали: список имён — снимок того, что было на одной ленте до
// разреза, а не угаданный набор.
func TestSecretsAndAgentsSettingsNotLost(t *testing.T) {
	fx := secretsAgentsFix(t)
	s := fx.S
	// приглашение ждёт агента: у него своё действие «Отменить»
	if _, err := s.eng.NewInvite("ноут", engine.RoleResident); err != nil {
		t.Fatal(err)
	}
	// ваш ключ SSH, токен, служебный секрет и агент в каждом состоянии: у каждого свои действия
	has := screenSettings(t, s, saScreens(fx)...)
	checkSet(t, "Ключи и секреты", has, []string{
		// добавление: вид, имя, значение, заметка, приватный ключ для импорта
		"kind", "name", "value", "note", "private", "!add_secret",
		// замена значения полем (было вопросом «replace_secret»), выдача, отзыв, удаление
		"!save_secret_value", "!grant_secret", "!revoke_secret", "!delete_secret",
		// публичная часть ключа и его команда, смена ключа шифрования
		"!$copy", "!rotate_key",
	})
	checkSet(t, "Агенты", has, []string{
		// приглашение: как назвать, роль, «Создать код», «Отменить»
		"name", "role", "!invite_agent", "!drop_invite",
		// способности и отзыв, отпечаток, «Забыть», «Проверить связь»
		"!allow_agent", "!revoke_agent", "!accept_agent_fp", "!forget_agent", "!check_agent",
		// где слушать и куда звонить шлюзу
		"listen", "!set_agent_listen", "gateway", "upstream", "!set_gateway",
	})
	// прежние действия живы под прежними именами: их зовут уведомления и старые клиенты
	for _, act := range []string{"replace_secret", "dismiss_key", "copy_root_fp", "grant_pending_secret"} {
		if _, err := s.execute(actionRequest{Action: act, Target: "никто", Params: map[string]string{"value": ""}}); err == errUnhandled {
			t.Errorf("прежнее действие %q больше никем не обрабатывается", act)
		}
	}
}
