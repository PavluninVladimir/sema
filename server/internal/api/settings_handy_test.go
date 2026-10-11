package api

// «Журнал настроек», «Вы меняли» и «Под рукой» на Обзоре.

import (
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// rowsOf — строки секции экрана по её ID, а у журнала — по заголовку.
func sectionRows(doc *Document, id string) []Component {
	for _, c := range doc.Components {
		if c.ID == id || c.Title == id {
			return c.Children
		}
	}
	return nil
}

func editKeys(rows []Component) []string {
	var keys []string
	for _, r := range rows {
		if strings.HasPrefix(r.ID, "правка-") {
			keys = append(keys, r.Meta+"|"+r.Subtitle)
		}
	}
	return keys
}

// По умолчанию журнал — «Ваши правки»: подписано человеком и не служебная отметка.
// Чип «Служебные» показывает остальное, «Все» — как есть; ничего не удалено и счёт
// чипов сходится.
func TestJournalDefaultsToYourEdits(t *testing.T) {
	s, st := indexFix(t, 20)
	indexJournal(t, st)
	totalBefore := func() int { n, _ := st.CountSettingsLog(); return n }
	was := totalBefore()

	doc := mustDoc(t, s, changesScreen)
	if len(doc.Filters) != 3 || doc.ActiveFilter != "" {
		t.Fatalf("чипы %+v, активный %q: ждали три чипа и «Ваши правки» по умолчанию", doc.Filters, doc.ActiveFilter)
	}
	for i, want := range []string{"Ваши правки", "Служебные", "Все"} {
		if !strings.HasPrefix(doc.Filters[i].Title, want) {
			t.Errorf("чип %d: %q, ждали «%s»", i, doc.Filters[i].Title, want)
		}
	}
	mine := sectionRows(doc, "Ваши правки")
	if len(mine) == 0 || len(mine) > editsPerScreen {
		t.Fatalf("«Ваши правки»: %d строк", len(mine))
	}
	for _, r := range mine {
		for _, mark := range []string{"chat.read.", "folders.cursor.", "tg.history.", "last_err"} {
			if strings.Contains(r.Meta, mark) || strings.Contains(r.Subtitle, mark) {
				t.Errorf("служебная отметка в «Ваших правках»: %s %s", r.Meta, r.Subtitle)
			}
		}
		if !strings.Contains(r.Caption, "вы") {
			t.Errorf("в «Ваших правках» не человек: %q", r.Caption)
		}
	}
	service := mustDocFilter(t, s, changesScreen, store.SettingsLogService)
	svc := sectionRows(service, "Служебные отметки Сёмы")
	joined := strings.Join(editKeys(svc), " ")
	for _, key := range []string{"chat.read.71", "folders.cursor.vp.INBOX", "notify.mac_last_err", "tg.history.79990000000.70"} {
		if !strings.Contains(joined, key) {
			t.Errorf("«Служебные» не показали %s", key)
		}
	}
	all := mustDocFilter(t, s, changesScreen, store.SettingsLogAll)
	if len(sectionRows(all, "Все записи")) < len(mine) {
		t.Error("«Все» короче «Ваших правок»")
	}
	// счёт чипов — по базе, и сумма двух отборов равна всему
	nMine, nService, nTotal := st.CountSettingsLogView()
	if nMine+nService != nTotal || nTotal != totalBefore() {
		t.Errorf("счёт: %d + %d ≠ %d (в базе %d)", nMine, nService, nTotal, totalBefore())
	}
	if totalBefore() != was {
		t.Errorf("показ журнала изменил его: %d → %d", was, totalBefore())
	}
	// прежние закладки («люди», «сам») открываются теми же отборами
	if d, err := s.buildScreen(changesScreen, "люди", "", 0); err != nil || d.ActiveFilter != "" || len(sectionRows(d, "Ваши правки")) != len(mine) {
		t.Error("закладка «люди» читается не как «ваши правки»")
	}
}

// mustDocFilter — экран с отбором и большой порцией: журнал со ста правками
// показывает по двадцать, а проверке нужны все.
func mustDocFilter(t *testing.T, s *Server, screen, filter string) *Document {
	t.Helper()
	doc, err := s.buildScreen(screen, filter, "", 400)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// Порции по двадцать и «Показать ещё»: отбор на сервере, а не на показанном.
func TestJournalPagesTwentyAtATime(t *testing.T) {
	s, st := indexFix(t, 20)
	for i := 0; i < 55; i++ {
		mark := st.LastChange()
		_ = st.SetSetting("ui.poll_s", strings.Repeat("1", i%7+2)+string(rune('a'+i%5)))
		st.SignChanges(mark, "вы", "settings:watch~screen · set_live_settings")
	}
	doc := mustDoc(t, s, changesScreen)
	if rows := sectionRows(doc, "Ваши правки"); len(rows) != editsPerScreen {
		t.Fatalf("первая порция: %d строк, ждали %d", len(rows), editsPerScreen)
	}
	hasMore := false
	for _, c := range doc.Components {
		if c.Type == "button" && len(c.Actions) == 1 && c.Actions[0].Action == "$more" {
			hasMore = true
		}
	}
	if !hasMore {
		t.Error("нет «Показать ещё»")
	}
	more, err := s.buildScreen(changesScreen, "", "", 2*editsPerScreen)
	if err != nil {
		t.Fatal(err)
	}
	if rows := sectionRows(more, "Ваши правки"); len(rows) != 2*editsPerScreen {
		t.Errorf("вторая порция: %d строк", len(rows))
	}
}

// Список служебных отметок — настройка `settings_log.marks` в «Тонкой настройке»
// журнала с первичным заполнением: поменяли строку — другие записи спрятаны. Записи
// при этом остаются: чип «Служебные» их показывает.
func TestJournalMarksAreASetting(t *testing.T) {
	s, st := indexFix(t, 20)
	indexJournal(t, st)
	if got := strings.Join(editKeys(sectionRows(mustDocFilter(t, s, changesScreen, ""), "Ваши правки")), " "); !strings.Contains(got, "ui.poll_s") {
		t.Fatalf("«Ваши правки» без ui.poll_s: %s", got)
	}
	// настройка видна в «Тонкой настройке» экрана и называет приставки
	rare := rareFieldsOf(rareBody(t, s, changesScreen))["marks"]
	if rare.Value == "" || !strings.Contains(rare.Value, "chat.read.") || !strings.Contains(rare.Value, "*.last_err") {
		t.Errorf("поле отметок: %q", rare.Value)
	}
	// сохранить — тем же действием, что у экрана, через общий разбор настроек
	if _, err := s.execute(actionRequest{Action: "set_settings_log_marks", Screen: changesScreen,
		Params: map[string]string{"marks": "chat.read., ui.poll_"}}); err != nil {
		t.Fatal(err)
	}
	doc := mustDocFilter(t, s, changesScreen, "")
	if got := strings.Join(editKeys(sectionRows(doc, "Ваши правки")), " "); strings.Contains(got, "ui.poll_s") || !strings.Contains(got, "tg.post_days") {
		t.Errorf("после новой отметки: %s", got)
	}
	if got := strings.Join(editKeys(sectionRows(mustDocFilter(t, s, changesScreen, store.SettingsLogService), "Служебные отметки Сёмы")), " "); !strings.Contains(got, "ui.poll_s") {
		t.Errorf("спрятанное не показано в «Служебных»: %s", got)
	}
	// срок хранения остался прежним
	if s.eng.SettingsLogKeepDays() != 180 {
		t.Errorf("срок хранения журнала %d, ждали прежние 180", s.eng.SettingsLogKeepDays())
	}
}

// Параметры в журнале называются по-человечески: путь над названием, ключ мелко,
// «было → стало» словами, запись ведёт к настройке с подсветкой строки.
func TestJournalNamesParametersByIndex(t *testing.T) {
	s, st := indexFix(t, 20)
	indexJournal(t, st)
	rows := sectionRows(mustDocFilter(t, s, changesScreen, ""), "Ваши правки")
	var poll, unknown *Component
	for i := range rows {
		switch {
		case rows[i].Meta == "ui.poll_s" && poll == nil:
			poll = &rows[i]
		case rows[i].Subtitle == "auth.phrase_limit" && unknown == nil:
			unknown = &rows[i]
		}
	}
	if poll == nil {
		t.Fatalf("нет правки ui.poll_s: %v", editKeys(rows))
	}
	if poll.Subtitle != "Опрос экрана без провода" || poll.Title != "Система → Работа Сёмы → Экран и связь" {
		t.Errorf("имя и путь: «%s» · «%s»", poll.Subtitle, poll.Title)
	}
	if len(poll.Actions) != 1 || poll.Actions[0].Target != "settings:watch~screen^f:work-live-poll_s" {
		t.Errorf("переход: %+v", poll.Actions)
	}
	var facts []string
	for _, f := range poll.Facts {
		facts = append(facts, f.Label+" "+f.Value)
	}
	if !strings.Contains(strings.Join(facts, "|"), "с") {
		t.Errorf("«было → стало» без единицы: %v", facts)
	}
	_ = unknown
	// имя по виду ключа и по действию, когда ключа в реестре нет
	idx := s.settingsIndex()
	if n := idx.nameOf(store.SettingChange{Key: "ssh.rules.srv-vpn.allow", After: "docker ps"}); n.Title != "Правила сервера — разрешить" ||
		n.Path != "Система → Серверы → srv-vpn → Правила" || !strings.HasPrefix(n.Addr, "settings:servers@srv-vpn~rules") {
		t.Errorf("правило сервера: %+v", n)
	}
	if n := idx.nameOf(store.SettingChange{Key: "mcp.rules.planner.task_list"}); n.Title != "Правило инструмента «task_list»" ||
		n.Path != "Система → Интеграции → planner → Правила" {
		t.Errorf("правило MCP: %+v", n)
	}
	if n := idx.nameOf(store.SettingChange{Key: "что-то.неизвестное"}); n.Title != "что-то.неизвестное" || n.Addr != "" {
		t.Errorf("неизвестный ключ: %+v", n)
	}
	// по действию: форма сохраняет одно поле на экране — оно и есть имя
	if n := idx.nameOf(store.SettingChange{Key: "hosts.x", From: "settings:servers@srv-vpn~auto · save_host_window"}); n.Entry != nil && n.Entry.Action != "save_host_window" {
		t.Errorf("по действию нашлось не то: %+v", n)
	}
}

// «Вы меняли» на Обзоре: последние пять правок человека без служебных отметок,
// с человеческим именем и переходом; ключ и токен встают рядом с ними (блок «Действия
// с доступом» ушёл с Обзора, и эти события жили только в нём).
func TestOverviewRecentEdits(t *testing.T) {
	s, st := indexFix(t, 20)
	indexJournal(t, st)
	// самое свежее — служебные отметки: на Обзор они не попадают
	for i := 0; i < 5; i++ {
		_ = st.SetSetting("chat.read.99", strings.Repeat("9", i+1))
	}
	doc := mustDoc(t, s, "settings")
	rows := sectionRows(doc, "recent-edits")
	var edits, other int
	for _, r := range rows {
		switch {
		case strings.HasPrefix(r.ID, "правка-"):
			edits++
			if strings.Contains(r.Meta, "chat.read") || strings.Contains(r.Subtitle, "chat.read") || strings.Contains(r.Subtitle, "folders.cursor") {
				t.Errorf("служебная отметка в «Вы меняли»: %s %s", r.Meta, r.Subtitle)
			}
		case r.ID == "changes-all":
		default:
			other++
		}
	}
	if edits == 0 || edits+other > recentEditsN {
		t.Errorf("«Вы меняли»: %d правок и %d событий, предел %d", edits, other, recentEditsN)
	}
	// блоков прежнего Обзора больше нет
	text := dumpScreen(doc)
	for _, gone := range []string{"Изменения настроек", "Действия с доступом и секретами"} {
		if strings.Contains(text, gone) {
			t.Errorf("на Обзоре остался блок «%s»", gone)
		}
	}
	// ключ устройства и токен не потеряны — стоят в «Вы меняли», пока свежие
	if _, err := st.AddToken("codex-hooks", []string{"ui"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPasskey("iPhone", "{}", "localhost"); err != nil {
		t.Fatal(err)
	}
	joined := dumpScreen(mustDoc(t, s, "settings"))
	if !strings.Contains(joined, "Заведён ключ устройства «iPhone»") || !strings.Contains(joined, "Выдан токен «codex-hooks»") {
		t.Error("заведённые ключ и токен пропали с Обзора")
	}
	// гостю доступ не показывается
	guestDoc, err := s.buildScreenForPhone("settings", "", "", 0, false, false, asGuest())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dumpScreen(guestDoc), "codex-hooks") {
		t.Error("гость увидел токены на Обзоре")
	}
	// пустой журнал объясняет себя
	empty, _ := indexFix(t, 20)
	if e := sectionRows(mustDoc(t, empty, "settings"), "recent-edits"); len(e) < 1 || e[0].Style != "empty" && !strings.HasPrefix(e[0].ID, "правка-") {
		t.Errorf("пустое «Вы меняли»: %+v", e)
	}
}

// pinForms — формы «Под рукой» на Обзоре.
func pinForms(doc *Document) []Component {
	var out []Component
	for _, c := range sectionRows(doc, "handy") {
		if c.Type == "form" {
			out = append(out, c)
		}
	}
	return out
}

// «Под рукой»: пять самых частых правок человека за месяц, живыми контролами; вход,
// доступ и самостоятельность серверов не берутся, даже будучи самыми частыми.
func TestHandyCountsJournalAndSkipsCareful(t *testing.T) {
	s, st := indexFix(t, 20)
	indexJournal(t, st)
	// самые частые в журнале — осторожные: auth.phrase_limit (9) и night.max_steps (8)
	top, err := st.TopEditedKeys(time.Now().AddDate(0, 0, -30).Format(time.RFC3339), 3)
	if err != nil || len(top) < 2 || top[0].Key != "auth.phrase_limit" || top[1].Key != "night.max_steps" {
		t.Fatalf("исходные данные: самые частые %+v (%v)", top, err)
	}
	forms := pinForms(mustDoc(t, s, "settings"))
	var keys []string
	for _, f := range forms {
		keys = append(keys, strings.TrimPrefix(f.ID, "pin-"))
	}
	want := "ui.poll_s,tg.post_days,search.save_pages,chats.command_ttl_h,mcp.chat_calls"
	if strings.Join(keys, ",") != want {
		t.Errorf("«Под рукой»: %v, ждали %s", keys, want)
	}
	if len(forms) > handyN {
		t.Errorf("больше пяти: %d", len(forms))
	}
	for _, f := range forms {
		field := f.Fields[0]
		// то же поле, что на экране настройки: вид, границы, единица, умолчание
		var e *indexEntry
		for _, c := range s.settingsIndex().byKey[strings.TrimPrefix(f.ID, "pin-")] {
			if c.pinnable() {
				e = c
			}
		}
		if e == nil {
			t.Fatalf("%s: нет записи в индексе", f.ID)
		}
		own := s.settingField(e.Key, e.Param, e.Title, "", e.Unit)
		if field.Type != own.Type || field.Min != own.Min || field.Max != own.Max || field.Unit != own.Unit ||
			*field.Default != *own.Default || field.Value != own.Value || field.Label != own.Label {
			t.Errorf("%s: контрол не тот, что у настройки: %+v и %+v", f.ID, field, own)
		}
		if f.Style != formRows || f.Submit == nil || f.Submit.Action != e.Action || f.Submit.Target != e.Target {
			t.Errorf("%s: форма сохраняет не тем действием: %+v", f.ID, f.Submit)
		}
		if !strings.Contains(field.Hint, " → ") && !strings.Contains(field.Hint, e.Path[1]) {
			t.Errorf("%s: под строкой нет подписи раздела: %q", f.ID, field.Hint)
		}
	}
	// осторожные не попали ни в какой из блоков Обзора
	text := dumpScreen(mustDoc(t, s, "settings"))
	for _, careful := range []string{"pin-auth.", "pin-night.", "pin-ssh.", "pin-auth.rp_id"} {
		if strings.Contains(text, careful) {
			t.Errorf("«Под рукой» взяло осторожную настройку: %s", careful)
		}
	}
	// форма на Обзоре — отложенная, её сохраняет нижняя полоса, как на любом экране настроек
	for _, f := range forms {
		if !f.Deferred {
			t.Errorf("%s: форму не сохраняет нижняя полоса", f.ID)
		}
	}
	if markup := stnMarkup(t, mustDoc(t, s, "settings")); !strings.Contains(markup, `id="save-bar"`) {
		t.Error("на Обзоре с «Под рукой» нет полосы сохранения")
	}
}

// Меньше пяти правок — показываются те, что есть; правок нет вовсе — блок объясняет себя.
func TestHandyFewAndNone(t *testing.T) {
	s, st := indexFix(t, 20)
	rows := sectionRows(mustDoc(t, s, "settings"), "handy")
	if len(rows) != 1 || rows[0].Style != "empty" || !strings.Contains(rows[0].Caption, "не попадают никогда") {
		t.Fatalf("без правок: %+v", rows)
	}
	for i := 0; i < 2; i++ {
		mark := st.LastChange()
		_ = st.SetSetting("ui.poll_s", []string{"6", "7"}[i])
		st.SignChanges(mark, "вы", "settings:watch~screen · set_live_settings")
	}
	if n := len(pinForms(mustDoc(t, s, "settings"))); n != 1 {
		t.Errorf("с одной настройкой в журнале «Под рукой» показало %d", n)
	}
	// секрет, правленный человеком чаще всех, на Обзор не выносится
	for i := 0; i < 6; i++ {
		mark := st.LastChange()
		if err := st.SetSecretSetting("google.refresh_token", strings.Repeat("т", i+1)); err != nil {
			t.Fatal(err)
		}
		st.SignChanges(mark, "вы", "settings:x · save_x")
	}
	for _, f := range pinForms(mustDoc(t, s, "settings")) {
		if strings.Contains(f.ID, "token") {
			t.Errorf("секрет вынесен на Обзор: %s", f.ID)
		}
	}
}

// Сохранение с Обзора — то же действие, что у экрана настройки, с теми же проверками:
// хорошее значение меняет ту же настройку и попадает в журнал «Ваши правки», плохое
// отвергается и ничего не пишет.
func TestHandySavesBySameActionWithSameChecks(t *testing.T) {
	s, st := indexFix(t, 20)
	indexJournal(t, st)
	forms := pinForms(mustDoc(t, s, "settings"))
	if len(forms) == 0 {
		t.Fatal("«Под рукой» пусто")
	}
	var form *Component
	for i := range forms {
		if forms[i].ID == "pin-ui.poll_s" {
			form = &forms[i]
		}
	}
	if form == nil {
		t.Fatalf("нет ui.poll_s: %v", forms)
	}
	field := form.Fields[0]
	post := func(value string) *httpResult {
		body, _ := json.Marshal(map[string]any{"action": form.Submit.Action, "target": form.Submit.Target,
			"screen": "settings", "params": map[string]string{field.Name: value}})
		w := doRequest(s, "POST", "/api/action", string(body), "127.0.0.1:1234", "")
		return &httpResult{w.Code, w.Body.String()}
	}
	if r := post("12"); r.code != http.StatusOK {
		t.Fatalf("сохранение с Обзора: %d %s", r.code, r.body)
	}
	if got := st.SettingShown(settingDef("ui.poll_s")); got != "12" {
		t.Errorf("настройка после сохранения с Обзора: %q", got)
	}
	// и на её собственном экране — то же значение
	if f := rareFieldsOf(mustDoc(t, s, "settings:watch~screen").Components)["poll_s"]; f.Value != "12" {
		t.Errorf("на экране настройки значение %q, ждали 12", f.Value)
	}
	// запись легла в журнал человеческой правкой
	top := must1(st.SettingsLogView(store.SettingsLogMine, 1))
	if len(top) != 1 || top[0].Key != "ui.poll_s" || top[0].After != "12" || !strings.HasPrefix(top[0].Who, "вы") {
		t.Errorf("журнал после сохранения с Обзора: %+v", top)
	}
	// плохое значение: те же границы, что у экрана; ничего не записано
	for _, bad := range []string{"0", "1", "много", "", "61", "-4"} {
		before := st.SettingShown(settingDef("ui.poll_s"))
		if r := post(bad); r.code == http.StatusOK {
			t.Errorf("ui.poll_s = %q принято с Обзора", bad)
		}
		if after := st.SettingShown(settingDef("ui.poll_s")); after != before {
			t.Errorf("отвергнутое %q что-то записало: %s → %s", bad, before, after)
		}
	}
}

type httpResult struct {
	code int
	body string
}

// Каждую закрепляемую настройку можно сохранить тем же действием и тем же
// параметром, что у её формы, — по одной, без остальных полей: это и есть то, на чём
// стоит «Под рукой». Перебор по индексу, а не по пятёрке из журнала.
func TestEveryPinnableSettingSavesAlone(t *testing.T) {
	s, st := indexFix(t, 20)
	idx := s.buildSettingsIndex()
	checked := 0
	for _, e := range idx.Entries {
		if !e.pinnable() {
			continue
		}
		def := settingDef(e.Key)
		alt := otherValue(def, e.Param)
		if _, err := s.execute(actionRequest{Action: e.Action, Target: e.Target, Screen: e.Addr,
			Params: map[string]string{e.Param: alt}}); err != nil {
			t.Errorf("%s: %q не сохранилось действием %s: %v", e.Key, alt, e.Action, err)
			continue
		}
		shown := st.SettingShown(def)
		if def.Kind == store.SettingFlag {
			shown = map[bool]string{true: "true", false: "false"}[st.SettingFlag(def)]
		}
		if shown != alt {
			t.Errorf("%s: сохранено %q, читается %q", e.Key, alt, shown)
		}
		checked++
	}
	if checked < 40 {
		t.Errorf("проверено закрепляемых настроек %d: индекс отдал слишком мало", checked)
	}
	// осторожные не закрепляются никогда: вход, доступ, доверие, самостоятельность серверов
	for _, e := range idx.Entries {
		if e.Kind != entrySetting || e.Key == "" {
			continue
		}
		if def, ok := store.LookupSetting(e.Key); ok && def.Careful && e.pinnable() {
			t.Errorf("осторожная настройка %s закрепляется", e.Key)
		}
	}
	for _, key := range []string{"auth.phrase_limit", "auth.origin", "auth.rp_id", "auth.local_hosts", "night.max_steps", "auto.max_hour",
		"ssh.timeout_s", "agents.listen", "agents.gateway", "speech.outside", "mcp.client_metadata", "mcp.login_ttl_min"} {
		def := settingDef(key)
		if !def.Careful {
			t.Errorf("%s не помечена осторожной в реестре", key)
		}
	}
}

// Кнопка замечания на Обзоре ведёт к самой строке, где она известна: роль без модели,
// сменившийся отпечаток сервера и агента. Метка в адресе кнопки есть, и по адресу экран
// открывается и несёт элемент с этой меткой.
func TestOverviewFindingsOpenTheirRow(t *testing.T) {
	servers, _ := serversFix(t, true)
	agents := secretsAgentsFix(t)
	models, _ := modelsFix(t, 20)
	for name, c := range map[string]struct {
		s    *Server
		want string
	}{"сервер": {servers, "settings:servers@srv-bots^r:host-fp-changed"},
		"агент":  {agents.S, "settings:agents@рабочая^r:agent-fp-changed"},
		"модели": {models, "settings:models^r:role-Перевод"}} {
		doc := mustDoc(t, c.s, "settings")
		var targets []string
		var walk func([]Component)
		walk = func(cs []Component) {
			for _, r := range cs {
				if strings.HasPrefix(r.ID, "беда-") && len(r.Actions) == 1 {
					targets = append(targets, r.Actions[0].Target)
				}
				walk(r.Children)
			}
		}
		walk(doc.Components)
		found := false
		for _, target := range targets {
			if target != c.want {
				continue
			}
			found = true
			addr, mark := splitHighlight(target)
			screen := mustDoc(t, c.s, addr)
			if !strings.Contains(html.UnescapeString(stnMarkup(t, screen)), `data-hl="`+mark+`"`) {
				t.Errorf("%s: экран %s не несёт элемента %s", name, addr, mark)
			}
		}
		if !found {
			t.Errorf("%s: у замечаний нет кнопки с адресом %q: %v", name, c.want, targets)
		}
	}
}
