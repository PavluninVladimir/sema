package api

// «Вход и доступ» — вкладки Ключи · Кто внутри · Токены · Доверенная сеть · Кто
// стучался (2 октября 2026). Меняется только раскладка: вход и доступ работают
// как работали. Тесты держат три вещи: ни одна настройка и ни одно действие не
// пропали и не появились новые; всё необратимое по-прежнему спрашивает; секретов
// (значение токена, секрет сессии, материал ключа) в разметке нет.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// accessFix — два ключа, три сессии, два токена, записи журнала. Возвращает
// секреты, которых на экране быть не должно.
type accessSecrets struct {
	sessionTokens []string
	credential    string
	apiToken      string
}

func accessFix(t *testing.T) (*Server, *store.Store, accessSecrets) {
	t.Helper()
	s, st := testServer(t)
	sec := accessSecrets{credential: "МАТЕРИАЛ-КЛЮЧА-НЕ-ДЛЯ-ЭКРАНА"}
	id1, err := st.AddPasskey("MacBook", sec.credential, "sema.local")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := st.AddPasskey("iPhone", sec.credential+"-2", "старый.пример")
	if err != nil {
		t.Fatal(err)
	}
	for i, se := range []struct {
		id        int64
		agent, ip string
	}{{id1, "Macintosh Chrome", "192.168.1.10"}, {id2, "iPhone Safari", "192.168.1.20"}, {id1, "Macintosh Firefox", "10.0.0.5"}} {
		token := fmt.Sprintf("СЕКРЕТ-СЕССИИ-%d", i)
		sec.sessionTokens = append(sec.sessionTokens, token)
		if err := st.AddSession(token, se.id, se.agent, se.ip, "вход по passkey", 24*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if sessions, _ := st.Sessions(); len(sessions) > 0 {
		if err := st.SetSessionOwn(sessions[0].Hash, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddToken("claude-code", []string{"memory", "ui"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddToken("старый", []string{"files"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		e := &store.AccessEntry{Who: "token:claude-code", Method: "GET", Path: fmt.Sprintf("/api/memory/%d", i), Status: 200, IP: "10.0.0.5"}
		switch {
		case i%7 == 0:
			e = &store.AccessEntry{Who: "anon", Method: "POST", Path: "/do", Status: 403, IP: "203.0.113.9", Note: "чужой источник: https://чужой.пример"}
		case i%5 == 0:
			e = &store.AccessEntry{Who: "session:1", Method: "LOGIN", Path: "/api/auth/login", Status: 200, IP: "192.168.1.10", Note: "вход по passkey"}
		}
		st.AddAccess(e)
	}
	return s, st, sec
}

var accessTabAddrs = []string{
	accessScreen, accessScreen + "~" + accessTabInside, accessScreen + "~" + accessTabTokens,
	accessScreen + "~" + accessTabNetwork, accessScreen + "~" + accessTabLog,
}

// Пять вкладок по порядку, адреса прежних вкладок открываются, а выбранной
// оказывается ровно одна; в колонке разделов стоит «Вход и доступ».
func TestAccessTabsAndOldAddresses(t *testing.T) {
	s, _, _ := accessFix(t)
	for i, screen := range accessTabAddrs {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		var names []string
		for _, f := range doc.Subtabs {
			names = append(names, f.Title)
		}
		if got := strings.Join(names, " · "); got != "Ключи · Кто внутри · Токены · Доверенная сеть · Кто стучался" {
			t.Errorf("%s: вкладки %q", screen, got)
		}
		if doc.ActiveSubtab != doc.Subtabs[i].ID || doc.ActivePanel != accessScreen {
			t.Errorf("%s: выбрана вкладка %q (ждали %q), раздел %q", screen, doc.ActiveSubtab, doc.Subtabs[i].ID, doc.ActivePanel)
		}
	}
	// счётчики говорят о составе до захода: ключей два, сессий три, токенов два
	doc, _ := s.buildScreen(accessScreen, "", "", 0)
	for i, want := range []string{"2", "3", "2"} {
		if doc.Subtabs[i].Badge != want {
			t.Errorf("счётчик вкладки %q: %q, ждали %q", doc.Subtabs[i].Title, doc.Subtabs[i].Badge, want)
		}
	}
	// неизвестный хвост открывает первую вкладку, а не ошибку
	doc, err := s.buildScreen(accessScreen+"~нет-такой", "", "", 0)
	if err != nil || doc.ActiveSubtab != accessScreen {
		t.Errorf("неизвестный хвост: вкладка %q, ошибка %v", doc.ActiveSubtab, err)
	}
	// переходы из других экранов называют старые адреса — они живы
	for _, addr := range []string{"settings:access", "settings:access~tokens", "settings:access~network", "settings:access~log"} {
		if _, err := s.buildScreen(addr, "", "", 0); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

// «Ключи»: тонкие строки с раскрытием для названия и «···» для удаления, без
// сессий; «Как это устроено» — одна свёрнутая строка с одним текстом.
func TestAccessKeysTab(t *testing.T) {
	s, _, _ := accessFix(t)
	doc, err := s.buildScreen(accessScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.ToolbarActions) != 1 || doc.ToolbarActions[0].Action != "$login" {
		t.Errorf("шапка вкладки: %+v", doc.ToolbarActions)
	}
	dump := dumpScreen(doc)
	for _, нет := range []string{"logout_session", "logout_all", "Кто сейчас внутри"} {
		if strings.Contains(dump, нет) {
			t.Errorf("на «Ключах» остались сессии: %s", нет)
		}
	}
	var rows []Component
	for _, id := range []string{"passkey-1", "passkey-2"} {
		row := findByID(doc.Components, id)
		if row == nil {
			t.Fatalf("строки ключа %s нет", id)
		}
		rows = append(rows, *row)
	}
	for _, row := range rows {
		if len(row.Actions) != 0 || len(row.Menu) != 1 || row.Menu[0].Action != "delete_passkey" ||
			row.Menu[0].Confirm == "" || row.Menu[0].ConfirmOK == "" || row.Menu[0].Style != "destructive" {
			t.Errorf("%s: удаление не одно, не в «···» или без вопроса: действия %v, меню %+v", row.ID, row.Actions, row.Menu)
		}
		// переименовать — поле в раскрытии с прежним действием
		if !row.Expandable || len(row.Children) != 1 || row.Children[0].Type != "form" {
			t.Fatalf("%s: переименовать нечем: %+v", row.ID, row.Children)
		}
		form := row.Children[0]
		if form.Submit == nil || form.Submit.Action != "rename_passkey" || form.Submit.Target != strings.TrimPrefix(row.ID, "passkey-") ||
			len(form.Fields) != 1 || form.Fields[0].Name != "value" || form.Fields[0].Value != row.Subtitle {
			t.Errorf("%s: форма переименования: %+v", row.ID, form)
		}
		if len(row.Facts) < 3 {
			t.Errorf("%s: у тонкой строки ключа меньше трёх фактов: %+v", row.ID, row.Facts)
		}
	}
	// ключ iPhone заведён для другого имени сервера — это видно уровнем строки
	if rows[1].Level != LevelWarn || !strings.Contains(dumpScreen(&Document{Components: []Component{rows[1]}}), "старый.пример") {
		t.Errorf("ключ с чужого адреса не помечен: %+v", rows[1])
	}
	// справка: ровно одна строка, свёрнутая, с одним текстом; прежних трёх абзацев
	// отдельными текстами на вкладке нет
	var how []Component
	texts := 0
	for _, c := range doc.Components {
		if c.Title == "Как это устроено" {
			how = append(how, c)
		}
		if c.Type == "text" {
			texts++
		}
	}
	if len(how) != 1 || !how[0].Collapsed || len(how[0].Children) != 1 || how[0].Children[0].Type != "text" {
		t.Fatalf("справка вкладки: %+v", how)
	}
	for _, want := range []string{"Пароля у Сёмы нет вовсе", "Фраза набирается", "Ключи привязаны к имени сервера"} {
		if !strings.Contains(how[0].Children[0].Text, want) {
			t.Errorf("в справке нет абзаца про %q", want)
		}
	}
	if texts != 0 {
		t.Errorf("на вкладке осталось %d абзацев вне справки", texts)
	}
	// предел попыток — по-прежнему «Тонкая настройка» вкладки (TestRareFieldsNotLost…)
	if fold := rareFoldOf(t, s, accessScreen); !strings.Contains(fold.Caption, "1 параметр") {
		t.Errorf("«Тонкая настройка»: %q", fold.Caption)
	}
}

// «Кто внутри»: сессии тонкими строками с «Завершить»; «Выйти везде» — в
// «Опасном» с вопросом; пометка «своё» — в «···».
func TestAccessInsideTab(t *testing.T) {
	s, st, _ := accessFix(t)
	doc, err := s.buildScreen(accessScreen+"~"+accessTabInside, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	sessions, _ := st.Sessions()
	for _, se := range sessions {
		row := findByID(doc.Components, "session-"+se.Hash[:12])
		if row == nil {
			t.Fatalf("строки сессии нет: %s", se.Agent)
		}
		if len(row.Actions) != 1 || row.Actions[0].Action != "logout_session" || row.Actions[0].Label != "Завершить" || row.Actions[0].Target != se.Hash {
			t.Errorf("%s: «Завершить»: %+v", se.Agent, row.Actions)
		}
		if len(row.Facts) != 2 || !strings.Contains(row.Caption, "действует до") {
			t.Errorf("%s: строка не тонкая: %+v", se.Agent, row)
		}
		wantMark := "mark_own_device"
		if se.Own {
			wantMark = "unmark_own_device"
		}
		if len(row.Menu) != 1 || row.Menu[0].Action != wantMark {
			t.Errorf("%s: пометка «моё» не в «···»: %+v", se.Agent, row.Menu)
		}
		if se.Own && (row.Menu[0].Confirm == "" || row.Menu[0].Style != "destructive") {
			t.Errorf("снятие пометки «моё» без вопроса: %+v", row.Menu[0])
		}
	}
	danger := findByID(doc.Components, "access-danger")
	if danger == nil || danger.Style != "danger" || !strings.HasPrefix(danger.Title, "Опасное") {
		t.Fatalf("«Опасного» нет: %+v", danger)
	}
	all := findByID(danger.Children, "danger-logout-all")
	if all == nil || len(all.Actions) != 1 || all.Actions[0].Action != "logout_all" || all.Actions[0].Confirm == "" ||
		all.Actions[0].ConfirmOK == "" || !strings.Contains(all.Actions[0].Confirm, "включая этот браузер") {
		t.Errorf("«Завершить все» без вопроса или не то действие: %+v", all)
	}
	// «Опасное» последним на вкладке, а ключей здесь нет
	if last := doc.Components[len(doc.Components)-1]; last.ID != "access-danger" {
		t.Errorf("«Опасное» не последнее: %q", last.ID)
	}
	if strings.Contains(dumpScreen(doc), "rename_passkey") || strings.Contains(dumpScreen(doc), "delete_passkey") {
		t.Error("на «Кто внутри» остались ключи")
	}
	// сессий нет — «Опасного» нет: завершать нечего
	for _, se := range sessions {
		if err := st.DeleteSession(se.Hash); err != nil {
			t.Fatal(err)
		}
	}
	doc, _ = s.buildScreen(accessScreen+"~"+accessTabInside, "", "", 0)
	if findByID(doc.Components, "access-danger") != nil {
		t.Error("«Опасное» осталось без единой сессии")
	}
	if !strings.Contains(dumpScreen(doc), "Никто не вошёл") {
		t.Error("пустая вкладка не объясняет себя")
	}
}

// «Токены»: тонкие строки, права капсулами, отзыв с вопросом; новый токен
// показывается один раз и только на этой вкладке.
func TestAccessTokensTab(t *testing.T) {
	s, _, _ := accessFix(t)
	doc, err := s.buildScreen(accessScreen+"~"+accessTabTokens, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	row := findByID(doc.Components, "token-1")
	if row == nil || len(row.Badges) != 2 {
		t.Fatalf("строка токена: %+v", row)
	}
	if len(row.Menu) != 1 || row.Menu[0].Action != "revoke_token" || row.Menu[0].Confirm == "" || row.Menu[0].ConfirmOK == "" {
		t.Errorf("отзыв токена без вопроса или не в «···»: %+v", row.Menu)
	}
	// новый токен: действие прежнее, значение — один раз
	if _, err := s.execute(actionRequest{Action: "add_token", Params: map[string]string{"name": "новый", "scope_memory": "true"}}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	value := s.lastToken
	s.mu.Unlock()
	if !strings.HasPrefix(value, "sema_") {
		t.Fatalf("токен не выдан: %q", value)
	}
	for _, screen := range accessTabAddrs {
		doc, _ := s.buildScreen(screen, "", "", 0)
		shown := strings.Contains(dumpScreen(doc), value)
		if shown != (screen == accessScreen+"~"+accessTabTokens) {
			t.Errorf("%s: свежий токен виден=%v", screen, shown)
		}
	}
	var how int
	doc, _ = s.buildScreen(accessScreen+"~"+accessTabTokens, "", "", 0)
	for _, c := range doc.Components {
		if c.Title == "Как это устроено" {
			how++
		}
	}
	if how != 1 {
		t.Errorf("справок на вкладке %d", how)
	}
}

// «Доверенная сеть»: итог, тумблеры, «Тонкая настройка», «Опасное» отдельной
// формой — как было (TestDangerousSavedSeparately); справка свёрнута.
func TestAccessNetworkTabKeepsItsForms(t *testing.T) {
	s, _, _ := accessFix(t)
	doc, err := s.buildScreen(accessScreen+"~"+accessTabNetwork, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"сеть-итог", "auth-settings", "auth-danger", "access-network-how", rareID} {
		if findByID(doc.Components, id) == nil {
			t.Errorf("на «Доверенной сети» нет %q", id)
		}
	}
	if how := findByID(doc.Components, "access-network-how"); how == nil || !how.Collapsed {
		t.Errorf("справка не свёрнута: %+v", how)
	}
}

// «Кто стучался»: двадцать строк, «Показать ещё», чипы с числами; отбор идёт на
// сервере, и порция растёт нажатием.
func TestAccessLogTabPagesAndFilters(t *testing.T) {
	s, _, _ := accessFix(t)
	screen := accessScreen + "~" + accessTabLog
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	journal := findByID(doc.Components, "access-journal")
	if journal == nil {
		t.Fatal("журнала нет")
	}
	more := func(doc *Document) string {
		for _, c := range doc.Components {
			if c.Type == "button" && len(c.Actions) > 0 && c.Actions[0].Action == "$more" {
				return c.Actions[0].Label
			}
		}
		return ""
	}
	if len(journal.Children) != accessLogPage || !strings.Contains(more(doc), "20 из") {
		t.Errorf("первая порция: %d строк, «ещё» %q", len(journal.Children), more(doc))
	}
	titles := map[string]string{}
	for _, f := range doc.Filters {
		titles[f.ID] = f.Title
	}
	// 70 записей: каждая седьмая — отказ (10), из остальных каждая пятая — вход (12)
	for id, want := range map[string]string{accessFilterAll: "Все · 70", accessFilterDenied: "Отказы · 10",
		accessFilterLogins: "Входы · 12", accessFilterTokens: "Токены · 48"} {
		if titles[id] != want {
			t.Errorf("чип %s: %q, ждали %q", id, titles[id], want)
		}
	}
	doc, _ = s.buildScreen(screen, accessFilterDenied, "", 0)
	journal = findByID(doc.Components, "access-journal")
	if len(journal.Children) != 1 || doc.ActiveFilter != accessFilterDenied || more(doc) != "" {
		t.Errorf("отбор «Отказы»: %d строк (одинаковые свёрнуты), чип %q, «ещё» %q", len(journal.Children), doc.ActiveFilter, more(doc))
	}
	if !strings.Contains(dumpScreen(&Document{Components: journal.Children}), "×10") {
		t.Errorf("десять отказов не свёрнуты в одну строку со счётчиком: %v", dumpScreen(&Document{Components: journal.Children}))
	}
	doc, _ = s.buildScreen(screen, accessFilterAll, "", 100)
	if journal = findByID(doc.Components, "access-journal"); len(journal.Children) <= accessLogPage || more(doc) != "" {
		t.Errorf("после «ещё» строк %d, «ещё» %q", len(journal.Children), more(doc))
	}
	// чужие попытки — полкой над журналом, а не на вкладке «Ключи»
	if !strings.Contains(dumpScreen(doc), "нажать кнопку в Сёме с чужой страницы") {
		t.Error("отбитые попытки не показаны на «Кто стучался»")
	}
	if keys, _ := s.buildScreen(accessScreen, "", "", 0); strings.Contains(dumpScreen(keys), "с чужой страницы") {
		t.Error("отбитые попытки остались на «Ключах»")
	}
	if !doc.noPhoneSearch {
		t.Error("у журнала без своего поиска на телефоне осталось поле «ищи по настройкам»")
	}
}

// Всё необратимое на вкладках «Доступа» спрашивает: удаление ключа, отзыв токена,
// «Завершить все», снятие пометки «моё», смена и отзыв фразы.
func TestAccessIrreversibleActionsAskFirst(t *testing.T) {
	s, st, _ := accessFix(t)
	if _, err := s.auth.SetPhrase(); err != nil {
		t.Fatal(err)
	}
	asks := map[string]bool{"delete_passkey": true, "revoke_token": true, "logout_all": true,
		"unmark_own_device": true, "new_phrase": true, "revoke_phrase": true}
	found := map[string]bool{}
	for _, screen := range accessTabAddrs {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		var walk func([]Component)
		walk = func(cs []Component) {
			for _, c := range cs {
				for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
					if asks[a.Action] {
						found[a.Action] = true
						if a.Confirm == "" || a.ConfirmOK == "" {
							t.Errorf("%s: действие %s без вопроса", screen, a.Action)
						}
					}
				}
				walk(c.Children)
			}
		}
		walk(doc.Components)
	}
	for name := range asks {
		if !found[name] {
			t.Errorf("действие %s не нашлось ни на одной вкладке", name)
		}
	}
	_ = st
}

// Прежние действия работают под прежними именами: переименование — через поле
// формы, а не вопрос в окне; удаление ключа закрывает его сессии.
func TestAccessActionsWorkThroughNewLayout(t *testing.T) {
	s, st, _ := accessFix(t)
	doc, _ := s.buildScreen(accessScreen, "", "", 0)
	form := findByID(doc.Components, "passkey-form-1")
	if form == nil {
		t.Fatal("формы переименования нет")
	}
	if _, err := s.execute(actionRequest{Action: form.Submit.Action, Target: form.Submit.Target, Params: map[string]string{"value": "Рабочий мак"}}); err != nil {
		t.Fatal(err)
	}
	keys, _ := st.Passkeys()
	if keys[0].Name != "Рабочий мак" {
		t.Errorf("переименование не сохранилось: %q", keys[0].Name)
	}
	before, _ := st.Sessions()
	if _, err := s.execute(actionRequest{Action: "delete_passkey", Target: "1"}); err != nil {
		t.Fatal(err)
	}
	after, _ := st.Sessions()
	if len(after) >= len(before) {
		t.Errorf("сессии удалённого ключа не закрылись: было %d, стало %d", len(before), len(after))
	}
	one := after[0]
	if _, err := s.execute(actionRequest{Action: "logout_session", Target: one.Hash}); err != nil {
		t.Fatal(err)
	}
	if rest, _ := st.Sessions(); len(rest) != len(after)-1 {
		t.Errorf("«Завершить» не закрыло сессию: %d → %d", len(after), len(rest))
	}
	if _, err := s.execute(actionRequest{Action: "revoke_token", Target: "2"}); err != nil {
		t.Fatal(err)
	}
	if tokens, _ := st.Tokens(); tokens[1].Revoked.IsZero() {
		t.Error("токен не отозван")
	}
	if _, err := s.execute(actionRequest{Action: "logout_all"}); err != nil {
		t.Fatal(err)
	}
	if rest, _ := st.Sessions(); len(rest) != 0 {
		t.Errorf("«Завершить все» оставило %d сессий", len(rest))
	}
}

// Ни значение токена, ни секрет сессии, ни материал ключа, ни фраза входа не
// попадают в разметку ни одной вкладки — кроме одноразового показа нового токена
// и новой фразы на своих вкладках, как и было.
func TestAccessSecretsNeverInMarkup(t *testing.T) {
	s, st, sec := accessFix(t)
	phrase, err := s.auth.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	// заведённый токен: его значение живёт в базе только хэшем
	issued, err := st.AddToken("для-проверки", []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := append([]string{sec.credential, sec.credential + "-2", issued}, sec.sessionTokens...)
	for _, screen := range accessTabAddrs {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatal(err)
		}
		text := dumpScreen(doc) + string(markup)
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s: в разметке секрет %q", screen, secret)
			}
		}
		// фраза показывается один раз, и только на «Ключах», где её заводят
		if shown := strings.Contains(text, phrase); shown != (screen == accessScreen) {
			t.Errorf("%s: свежая фраза видна=%v", screen, shown)
		}
		for _, hex := range []string{"credential", "Credential"} {
			if strings.Contains(text, hex) {
				t.Errorf("%s: в разметке упомянут материал ключа (%q)", screen, hex)
			}
		}
	}
}
