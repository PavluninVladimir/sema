package api

// «Работа Сёмы» и «Что ушло наружу» в каркасе (2 октября 2026, шаг 8 плана
// `design/nastroyki-2/README.md`): ни одна настройка не пропала и не размножилась,
// сохранённое доезжает до движка, прежние адреса открывают то же, переходы
// ведут на существующие экраны.

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// workScreens — три вкладки раздела.
func workScreens() []string {
	return []string{watchScreen, entityAddr(watchScreen, "", workTabScreen), entityAddr(watchScreen, "", workTabChats)}
}

// notifyScreens — вкладки «Уведомлений» и «Добавить канал».
func notifyScreens() []string {
	return []string{notifySettingsScreen, notifySettingsScreen + "~" + notifyTabRules, notifySettingsScreen + "~" + notifyTabShow,
		entityAddr(notifySettingsScreen, notifyNew, "")}
}

// fieldCounts — сколько раз имя поля встретилось на экранах, вместе с «Тонкой
// настройкой».
func fieldCounts(t *testing.T, s *Server, screens ...string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, screen := range screens {
		doc, rare := buildAll(t, s, screen)
		visit := func(c Component) {
			for _, f := range c.Fields {
				counts[f.Name]++
			}
		}
		walkAll(doc.Components, visit)
		walkAll(rare, visit)
	}
	return counts
}

// movedFieldNames — имена полей, переехавших с «Уведомлений»: те же, что были
// там (договор с прежним действием `set_notify_settings`).
func movedFieldNames() []string {
	names := []string{"tz"}
	for _, p := range workLiveParams {
		names = append(names, p.Param)
	}
	for _, p := range workChatParams {
		names = append(names, p.Param)
	}
	sort.Strings(names)
	return names
}

// У раздела одно имя в колонке и над содержимым, и три вкладки: первая — то,
// что здесь было, остальные — переехавшее.
func TestWorkSectionHasOneNameAndThreeTabs(t *testing.T) {
	s, _ := testServer(t)
	for i, screen := range workScreens() {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if doc.Heading != "Работа Сёмы" || sectionTitle[watchScreen] != "Работа Сёмы" {
			t.Errorf("%s: заголовок %q, имя в колонке %q", screen, doc.Heading, sectionTitle[watchScreen])
		}
		if doc.ActivePanel != watchScreen {
			t.Errorf("%s: в колонке выбран %q", screen, doc.ActivePanel)
		}
		var ids, names []string
		for _, tab := range doc.Subtabs {
			ids = append(ids, tab.ID)
			names = append(names, tab.Title)
		}
		if strings.Join(ids, " ") != strings.Join(workScreens(), " ") {
			t.Errorf("%s: вкладки %v", screen, ids)
		}
		if strings.Join(names, " · ") != "Самонаблюдение · Экран и связь · Чаты и ветки" {
			t.Errorf("%s: имена вкладок %v", screen, names)
		}
		if doc.ActiveSubtab != screen {
			t.Errorf("%s: выбрана вкладка %q", screen, doc.ActiveSubtab)
		}
		if i == 0 {
			// первая вкладка — прежнее содержимое: наблюдатель и находки
			if _, ok := actionOnScreen(doc, "toggle_watch"); !ok {
				t.Error("на первой вкладке нет наблюдателя")
			}
		}
	}
	// прежнее имя нигде в колонке не осталось
	doc, _ := s.buildScreen(watchScreen, "", "", 0)
	for _, item := range doc.Panel {
		if strings.Contains(item.Title, "Самонаблюдение") || strings.Contains(item.Title, "нашёл в себе") {
			t.Errorf("в колонке осталось прежнее имя: %q", item.Title)
		}
	}
	// открытая находка видна числом у первой вкладки, до того как её откроют
	if _, err := s.eng.Store().SaveFinding(store.Finding{Key: "проба:счётчик", Kind: "повтор без следа", Ref: "file:7",
		Level: store.FindingWarn, Summary: "повторяется", Measure: store.MeasureAlert, Screen: "queue"}); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.buildScreen(entityAddr(watchScreen, "", workTabChats), "", "", 0)
	if len(doc.Subtabs) == 0 || doc.Subtabs[0].Badge != "1" || doc.Subtabs[0].Level != LevelWarn {
		t.Errorf("число находок у вкладки «Самонаблюдение»: %+v", doc.Subtabs)
	}
}

// Ни одна переехавшая настройка не пропала: все поля и действия на «Работе
// Сёмы», а на месте, где они жили, остались счётчик, срок хранения и «Отложить».
func TestWorkSettingsNotLost(t *testing.T) {
	s, _ := testServer(t)
	has := screenSettings(t, s, workScreens()...)
	checkSet(t, "Работа Сёмы", has, append(movedFieldNames(),
		"!set_live_settings", "!set_chat_settings", "!set_timezone",
		// наблюдатель — как был
		"!toggle_watch", "!toggle_watch_stop", "!toggle_watch_disagree", "!toggle_watch_doctor",
		"repeat", "window", "keep", "!save_watch_limits"))
}

// Каждая переехавшая настройка — ровно в одном месте: на «Работе Сёмы» раз, на
// «Уведомлениях» ни разу, а там осталась строка-ссылка туда, где они теперь.
func TestMovedSettingsLiveInOnePlace(t *testing.T) {
	s, _ := testServer(t)
	onWork := fieldCounts(t, s, workScreens()...)
	onNotify := fieldCounts(t, s, notifyScreens()...)
	for _, name := range movedFieldNames() {
		if onWork[name] != 1 {
			t.Errorf("поле %q на «Работе Сёмы» %d раз, ждали один", name, onWork[name])
		}
		if onNotify[name] != 0 {
			t.Errorf("поле %q осталось на «Уведомлениях» (%d раз)", name, onNotify[name])
		}
	}
	// у «Уведомлений» остались своё: счётчик, срок «Сделано», «Отложить»
	for _, name := range []string{"counter", "keep", "snooze"} {
		if onNotify[name] != 1 {
			t.Errorf("поле %q на «Уведомлениях» %d раз", name, onNotify[name])
		}
	}
	// человек, пришедший по привычке, находит дорогу: две строки с переходом, и
	// оба перехода собираются
	doc, err := s.buildScreen(notifySettingsScreen+"~"+notifyTabShow, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	walkAll(doc.Components, func(c Component) {
		for _, a := range c.Actions {
			if a.Action == "$goto" && strings.HasPrefix(a.Target, watchScreen) {
				targets = append(targets, a.Target)
			}
		}
	})
	sort.Strings(targets)
	want := []string{entityAddr(watchScreen, "", workTabChats), entityAddr(watchScreen, "", workTabScreen)}
	if strings.Join(targets, " ") != strings.Join(want, " ") {
		t.Fatalf("переходы с «Что показывать»: %v, ждали %v", targets, want)
	}
	for _, target := range targets {
		if d, err := s.buildScreen(target, "", "", 0); err != nil || d.Screen != target {
			t.Errorf("переход %q не открывается: %v", target, err)
		}
	}
}

// workValue — как движок читает настройку после сохранения.
func (s *Server) workValue(key string) int {
	live := s.eng.LiveSettings()
	switch key {
	case "ui.live_ms":
		return int(live.Window / time.Millisecond)
	case "ui.poll_s":
		return live.PollS
	case "ui.poll_live_s":
		return live.PollLiveS
	case "ui.ws_write_s":
		return int(live.WriteWait / time.Second)
	case "ui.ws_per_session":
		return live.PerSession
	case "ui.snapshot_days":
		return live.SnapshotDays
	case "ui.list_rows":
		return s.eng.ListRows()
	case "ui.prefetch_ms":
		return s.eng.PrefetchMs()
	case "debug.slow_sql_ms":
		return s.eng.SlowSQLMs()
	case "chats.command_ttl_h":
		return s.eng.CommandChatTTL()
	case "chats.nudge_after_min":
		return s.eng.NudgeAfterMin()
	case "chats.nudge_max":
		return s.eng.NudgeMax()
	case "chats.thread_tail":
		return s.eng.ThreadTail()
	case "chats.read_steps":
		return s.eng.ChatReadSteps()
	case "assign.max_steps":
		return s.eng.AssignLimits().MaxSteps
	case "assign.step_turns":
		return s.eng.AssignLimits().StepTurns
	case "assign.days":
		return s.eng.AssignLimits().Days
	}
	return -1
}

// workText — то же строкой: у слов обещания значение — текст, а не число.
func (s *Server) workText(key string) string {
	lim := s.eng.AssignLimits()
	switch key {
	case "chats.promise_markers":
		return s.eng.PromiseMarkers()
	case "chats.claim_markers":
		return s.eng.ClaimMarkers()
	case "assign.confirm":
		return strconv.FormatBool(lim.Confirm)
	case "debug.pprof":
		return strconv.FormatBool(s.eng.PprofOn())
	case "http.redirect_tls":
		return strconv.FormatBool(s.eng.RedirectTLS())
	case "assign.tracker":
		return lim.Tracker
	case "assign.tracker_project":
		return lim.TrackerProj
	case "assign.tracker_create_tool":
		return lim.CreateTool
	case "assign.tracker_done_tool":
		return lim.DoneTool
	case "assign.tracker_cancel_tool":
		return lim.CancelTool
	}
	return strconv.Itoa(s.workValue(key))
}

// sameWork — движок читает то, что сохранили: у тумблера «1» и «true» —
// одно и то же.
func sameWork(def store.SettingDef, got, want string) bool {
	if def.Kind == store.SettingFlag {
		on := func(v string) bool { return v == "1" || v == "true" || v == "on" }
		return on(got) == on(want)
	}
	return got == want
}

// Каждое поле сохраняется действием своей формы, движок видит новое значение,
// экран показывает его; возврат умолчания тем же жестом возвращает прежнее
// чтение. У каждой, а не у одной.
func TestWorkSettingsSaveByTheirActionsAndReturnToDefault(t *testing.T) {
	s, _ := testServer(t)
	exercised := 0
	defer func() {
		if want := len(workLiveParams) + len(workChatParams); exercised != want {
			t.Errorf("проверено полей %d, а настроек у раздела %d", exercised, want)
		}
	}()
	for _, screen := range workScreens()[1:] {
		for _, param := range append(append([]settingParam{}, workLiveParams...), workChatParams...) {
			doc, rare := buildAll(t, s, screen)
			form := rareFormOf(append(append([]Component{}, doc.Components...), rare...), param.Param)
			if form == nil {
				continue // поле другой вкладки
			}
			exercised++
			if form.Submit == nil || !strings.HasPrefix(form.Submit.Action, "set_") {
				t.Errorf("%s: у поля %q нет действия сохранения: %+v", screen, param.Param, form.Submit)
				continue
			}
			def, ok := store.LookupSetting(param.Key)
			if !ok {
				t.Fatalf("%s не в реестре", param.Key)
			}
			// умолчание на экране — умолчание реестра и значение пустой базы
			field := rareFieldsOf(append(append([]Component{}, doc.Components...), rare...))[param.Param]
			if field.Default == nil || *field.Default != def.ShownDefault() || field.Value != *field.Default {
				t.Errorf("%s: поле %q показывает %q при умолчании %v", screen, param.Param, field.Value, field.Default)
			}
			save := func(value string) error {
				_, err := s.execute(actionRequest{Action: form.Submit.Action, Screen: screen,
					Params: map[string]string{param.Param: value}})
				return err
			}
			alt := otherValue(def, param.Param)
			if err := save(alt); err != nil {
				t.Errorf("%s: %q = %q не сохранилось: %v", screen, param.Param, alt, err)
				continue
			}
			if got := s.workText(param.Key); !sameWork(def, got, alt) {
				t.Errorf("%s: сохранено %q, а движок читает %s", param.Key, alt, got)
			}
			doc, rare = buildAll(t, s, screen)
			if f := rareFieldsOf(append(append([]Component{}, doc.Components...), rare...))[param.Param]; f.Value != alt {
				t.Errorf("%s: экран показывает %q, а сохранено %q", param.Key, f.Value, alt)
			}
			if err := save(def.Default); err != nil {
				t.Errorf("%s: возврат умолчания: %v", param.Key, err)
			}
			if got := s.workText(param.Key); !sameWork(def, got, def.Default) {
				t.Errorf("%s: после возврата движок читает %s, а умолчание %s", param.Key, got, def.Default)
			}
		}
	}
}

// Плохое число отвергается словами и ничего не пишет; непришедшее поле соседа
// не сбрасывает; стёртое число — отказ, а не молчаливое «сохранено».
func TestWorkSettingsRejectBadValuesAndKeepOthers(t *testing.T) {
	s, _ := testServer(t)
	run := func(action string, params map[string]string) error {
		_, err := s.execute(actionRequest{Action: action, Params: params})
		return err
	}
	if err := run("set_live_settings", map[string]string{"poll_s": "9"}); err != nil {
		t.Fatal(err)
	}
	if err := run("set_live_settings", map[string]string{"live_ms": "700"}); err != nil {
		t.Fatal(err)
	}
	if live := s.eng.LiveSettings(); live.PollS != 9 || live.Window != 700*time.Millisecond {
		t.Errorf("непришедшее поле сбросило соседа: %+v", live)
	}
	for _, bad := range []map[string]string{{"poll_s": "1"}, {"poll_s": "61"}, {"poll_s": "много"}, {"poll_s": ""},
		{"live_ms": "99"}, {"ws_per_session": "0"}, {"poll_live_s": "5"}, {"poll_s": "20", "live_ms": "x"}} {
		if err := run("set_live_settings", bad); err == nil {
			t.Errorf("%v принято", bad)
		}
	}
	if s.eng.LiveSettings().PollS != 9 {
		t.Errorf("отвергнутая форма что-то записала: опрос %d", s.eng.LiveSettings().PollS)
	}
	// у пинков ноль законен, у срока чата — нет
	if err := run("set_chat_settings", map[string]string{"nudge_max": "0"}); err != nil || s.eng.NudgeMax() != 0 {
		t.Errorf("ноль пинков: %v, читается %d", err, s.eng.NudgeMax())
	}
	for _, bad := range []map[string]string{{"cmd_ttl": "0"}, {"cmd_ttl": "721"}, {"nudge_max": "21"}, {"nudge_max": "-1"}, {"thread_tail": "0"}} {
		if err := run("set_chat_settings", bad); err == nil {
			t.Errorf("%v принято", bad)
		}
	}
}

// Прежнее действие `set_notify_settings` с прежними параметрами работает: скрипты
// и старые карточки о переезде не знают. Пустое поле — «не трогали».
func TestOldNotifySettingsActionStillSavesMovedSettings(t *testing.T) {
	s, st := testServer(t)
	if _, err := s.execute(actionRequest{Action: "set_notify_settings", Params: map[string]string{
		"poll_s": "6", "poll_live_s": "120", "live_ms": "500", "ws_write_s": "15", "ws_per_session": "4", "snapshot_days": "14",
		"thread_tail": "8", "cmd_ttl": "12", "nudge_after": "7", "nudge_max": "2", "counter": "", "keep": "", "snooze": ""}}); err != nil {
		t.Fatal(err)
	}
	live := s.eng.LiveSettings()
	if live.PollS != 6 || live.PollLiveS != 120 || live.Window != 500*time.Millisecond || live.WriteWait != 15*time.Second ||
		live.PerSession != 4 || live.SnapshotDays != 14 {
		t.Errorf("провод и опрос: %+v", live)
	}
	if s.eng.ThreadTail() != 8 || s.eng.CommandChatTTL() != 12 || s.eng.NudgeAfterMin() != 7 || s.eng.NudgeMax() != 2 {
		t.Errorf("чаты: хвост %d, срок %d, пинать через %d, пинков %d",
			s.eng.ThreadTail(), s.eng.CommandChatTTL(), s.eng.NudgeAfterMin(), s.eng.NudgeMax())
	}
	// своё действие «Уведомлений» работает как работало
	if _, err := s.execute(actionRequest{Action: "set_notify_settings", Params: map[string]string{"keep": "90", "poll_s": ""}}); err != nil {
		t.Fatal(err)
	}
	if got := st.Setting("notify.keep_days", ""); got != "90" {
		t.Errorf("срок «Сделано» %q", got)
	}
	if s.eng.LiveSettings().PollS != 6 {
		t.Errorf("пустое поле сбросило опрос: %d", s.eng.LiveSettings().PollS)
	}
}

// Часовой пояс — как был: первый запрос приложения приносит свой (заголовок
// X-Sema-TZ), выбранный человеком приложение не перебивает, неизвестный пояс
// отвергается понятной ошибкой.
func TestTimezoneBehavesAsBefore(t *testing.T) {
	was := time.Local
	t.Cleanup(func() { time.Local = was })
	s, st := testServer(t)

	ui := func(tz string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8765/ui?screen="+watchScreen+"~"+workTabScreen, nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("X-Sema-TZ", tz)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ответ %d: %s", rec.Code, rec.Body.String())
		}
	}
	// человек ничего не выбирал: пояс приносит приложение
	ui("Europe/Samara")
	if got := st.Setting("app.timezone", ""); got != "Europe/Samara" {
		t.Fatalf("пояс от приложения не взят: %q", got)
	}
	// выбрал — приложение его не перебивает
	if _, err := s.execute(actionRequest{Action: "set_timezone", Params: map[string]string{"tz": "Europe/Moscow"}}); err != nil {
		t.Fatal(err)
	}
	ui("Asia/Tokyo")
	if got := s.eng.Timezone(); got != "Europe/Moscow" {
		t.Errorf("пояс %q, а выбран Europe/Moscow", got)
	}
	if time.Local.String() != "Europe/Moscow" {
		t.Errorf("пояс не применён на лету: %s", time.Local)
	}
	// поле на экране показывает нынешний
	doc, _ := buildAll(t, s, watchScreen+"~"+workTabScreen)
	if f := rareFieldsOf(doc.Components)["tz"]; f.Value != "Europe/Moscow" {
		t.Errorf("поле «Часовой пояс» показывает %q", f.Value)
	}
	// неизвестный пояс — понятная ошибка, ничего не записано
	_, err := s.execute(actionRequest{Action: "set_timezone", Params: map[string]string{"tz": "Atlantis/Nowhere"}})
	if err == nil || !strings.Contains(err.Error(), "не найден") || !strings.Contains(err.Error(), "Europe/Moscow") {
		t.Errorf("ошибка неизвестного пояса: %v", err)
	}
	if s.eng.Timezone() != "Europe/Moscow" {
		t.Errorf("отвергнутый пояс что-то записал: %q", s.eng.Timezone())
	}
	// стёртое поле — отказ, а не «сохранено»; непришедшее — «не трогали»
	if _, err := s.execute(actionRequest{Action: "set_timezone", Params: map[string]string{"tz": "  "}}); err == nil {
		t.Error("пустой пояс принят")
	}
	if _, err := s.execute(actionRequest{Action: "set_timezone"}); err != nil {
		t.Errorf("форма без поля пояса: %v", err)
	}
	// прежним действием «Уведомлений» — тоже
	if _, err := s.execute(actionRequest{Action: "set_notify_settings", Params: map[string]string{"tz": "Europe/Ulyanovsk"}}); err != nil {
		t.Fatal(err)
	}
	if s.eng.Timezone() != "Europe/Ulyanovsk" {
		t.Errorf("прежнее действие пояс не сменило: %q", s.eng.Timezone())
	}
}

// ---- «Что ушло наружу» в каркасе ----

// Раздел, его лента и одна отправка рисуются внутри каркаса настроек: колонка
// разделов на месте, выбран «Что ушло наружу», на телефоне — «‹ Настройки».
func TestSentLivesInSettingsFrame(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddSent(&store.SentRow{Consumer: "core:queue", Model: "qwen3", OK: true,
		Class: store.ClassPlain, TokensIn: 10, TokensOut: 5, Seconds: 1, Prompt: "текст"}); err != nil {
		t.Fatal(err)
	}
	list, _ := st.SentList(false, "", "", 1)
	if len(list) == 0 {
		t.Fatal("отправка не записалась")
	}
	one := sentScreen + "~" + strconv.FormatInt(list[0].ID, 10)
	for _, screen := range []string{sentScreen, sentScreen + "~" + sentTabLog, one} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if doc.Screen != screen || doc.Active != "settings" || doc.ActivePanel != sentScreen {
			t.Errorf("%s: экран %q, меню %q, в колонке %q", screen, doc.Screen, doc.Active, doc.ActivePanel)
		}
		var picked []string
		for _, item := range doc.Panel {
			if item.ID == doc.ActivePanel {
				picked = append(picked, item.Title)
			}
		}
		if len(doc.Panel) < 20 || len(picked) != 1 || picked[0] != "Что ушло наружу" {
			t.Errorf("%s: колонка из %d пунктов, выбран %v", screen, len(doc.Panel), picked)
		}
		if doc.Storable {
			t.Errorf("%s: экран с отправленным стал хранимым на устройстве", screen)
		}
		// телефон: колонки нет, «назад» один — в настройки или в раздел
		phoneDoc, err := s.buildScreenForPhone(screen, "", "", 0, true, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(phoneDoc.Panel) != 0 {
			t.Errorf("%s: на телефоне осталась колонка", screen)
		}
		backs := 0
		for _, c := range phoneDoc.Components {
			if c.Type == "crumb" {
				backs++
			}
		}
		if screen == sentScreen && (backs != 1 || phoneDoc.Components[0].Actions[0].Target != settingsRoot) {
			t.Errorf("%s: «назад» на телефоне: %d, %+v", screen, backs, phoneDoc.Components[0])
		}
		if screen != sentScreen && backs != 1 {
			t.Errorf("%s: на телефоне путей назад %d, ждали один свой", screen, backs)
		}
	}
	// заголовок раздела — то имя, что в колонке; у ленты и отправки свои, но
	// путь назад называет раздел
	doc, _ := s.buildScreen(sentScreen, "", "", 0)
	if doc.Heading != sectionTitle[sentScreen] || doc.Heading != "Что ушло наружу" {
		t.Errorf("заголовок раздела %q, в колонке %q", doc.Heading, sectionTitle[sentScreen])
	}
	journal, _ := s.buildScreen(sentScreen+"~"+sentTabLog, "", "", 0)
	if journal.Heading != "Журнал отправок" {
		t.Errorf("заголовок ленты %q", journal.Heading)
	}
	back := firstKind(journal.Components, "crumb")
	if back == nil || len(back.Actions) == 0 || back.Actions[0].Target != sentScreen || back.Actions[0].Label != "Что ушло наружу" {
		t.Errorf("путь назад из ленты: %+v", back)
	}
}

// Прежние адреса открывают то же самое, что нынешние, и сами называют нынешний:
// клиент запомнит новый адрес, а не тот, что в закладке.
func TestOldSentAddressesOpenSettingsSection(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddSent(&store.SentRow{Consumer: "core:queue", Model: "qwen3", OK: true,
		Class: store.ClassPlain, TokensIn: 10, TokensOut: 5, Seconds: 1, Prompt: "текст"}); err != nil {
		t.Fatal(err)
	}
	list, _ := st.SentList(false, "", "", 1)
	id := strconv.FormatInt(list[0].ID, 10)
	for old, now := range map[string]string{
		"sent":                        "settings:sent",
		"sent~" + sentTabLog:          "settings:sent~" + sentTabLog,
		"sent~" + id:                  "settings:sent~" + id,
		"sent@чужой":                  "settings:sent",
		"settings:sent":               "settings:sent",
		"settings:sent~" + id:         "settings:sent~" + id,
		sentScreen + "~" + sentTabLog: "settings:sent~" + sentTabLog,
	} {
		doc, err := s.buildScreen(old, "", "", 0)
		if err != nil {
			t.Errorf("%s: %v", old, err)
			continue
		}
		fresh, err := s.buildScreen(now, "", "", 0)
		if err != nil {
			t.Errorf("%s: %v", now, err)
			continue
		}
		if doc.Screen != now {
			t.Errorf("%s открыл %q, ждали %q", old, doc.Screen, now)
		}
		if dumpScreen(doc) != dumpScreen(fresh) {
			t.Errorf("%s и %s показывают разное", old, now)
		}
	}
	// чужие адреса на «sent…» не переписываются
	for _, other := range []string{"senders", "sentinel", "settings:senders"} {
		if canonSentAddr(other) != other {
			t.Errorf("адрес %q переписан в %q", other, canonSentAddr(other))
		}
	}
	// фильтр ленты и порция работают на новом адресе, как на старом
	for _, filter := range []string{"", "outside", store.ClassSensitive, store.ClassSecret} {
		if _, err := s.buildScreen(sentScreen+"~"+sentTabLog, filter, "", 30); err != nil {
			t.Errorf("лента с отбором %q: %v", filter, err)
		}
	}
}

// Каждый переход на «Что ушло наружу» и на «Работу Сёмы», который строят экраны,
// ведёт на существующий экран по нынешнему адресу — а не на тот, что открывается
// только благодаря переадресации.
func TestSentAndWorkGotosLeadToRealScreens(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddSent(&store.SentRow{Consumer: "core:queue", Model: "qwen3", OK: true,
		Class: store.ClassPlain, TokensIn: 10, TokensOut: 5, Seconds: 1, Prompt: "текст"}); err != nil {
		t.Fatal(err)
	}
	// «секретное недоверенной» зажигает кнопку «Посмотреть отправки» в «Ждут решения»
	if err := st.AddSent(&store.SentRow{Consumer: "core:poll", Model: "облако", OK: true, Outside: true, Untrusted: true,
		Class: store.ClassSecret, TokensIn: 10, TokensOut: 5, Seconds: 1}); err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{}
	take := func(from, target string) {
		if strings.HasPrefix(target, "sent") || strings.HasPrefix(target, sentScreen) || strings.HasPrefix(target, watchScreen) {
			targets[target] = from
		}
	}
	screens := append([]string{"settings", settingsOverview, "health", "notifications", sentScreen, sentScreen + "~" + sentTabLog,
		accessScreen, secretsScreen, modelsScreen, searchScreen, tgScreen}, append(workScreens(), notifyScreens()...)...)
	for _, screen := range screens {
		doc, rare := buildAll(t, s, screen)
		visit := func(c Component) {
			for _, a := range everyAction(c) {
				if a.Action == "$goto" {
					take(screen, a.Target)
				}
			}
		}
		walkAll(doc.Components, visit)
		walkAll(rare, visit)
		for _, a := range append(append([]Action{}, doc.ToolbarActions...), doc.ToolbarMenu...) {
			if a.Action == "$goto" {
				take(screen, a.Target)
			}
		}
		for _, tab := range doc.Subtabs {
			take(screen, tab.ID)
		}
	}
	for _, section := range s.allSections() {
		if section.Button.Action == "$goto" {
			take("обзор: "+section.Screen, section.Button.Target)
		}
	}
	// куда ведут тревоги «Здоровья» и записи движка
	take("тревога «Ушло наружу»", alertTarget("sent"))
	take("тревога «Нашёл в себе»", alertTarget("watch"))
	for _, want := range []string{sentScreen, sentScreen + "~" + sentTabLog, entityAddr(watchScreen, "", workTabScreen),
		entityAddr(watchScreen, "", workTabChats), watchScreen} {
		if targets[want] == "" {
			t.Errorf("переход на %q ни с одного экрана не нашёлся", want)
		}
	}
	for target, from := range targets {
		if target == "sent" || strings.HasPrefix(target, "sent~") || strings.HasPrefix(target, "sent@") {
			t.Errorf("%s: переход на прежний адрес %q", from, target)
			continue
		}
		doc, err := s.buildScreen(target, "", "", 0)
		if err != nil {
			t.Errorf("%s: переход на %q: %v", from, target, err)
			continue
		}
		if doc.Screen != target {
			t.Errorf("%s: переход на %q открыл %q: адрес держится на переадресации", from, target, doc.Screen)
		}
		if _, section, ok := settingsSectionOf(target); !ok || (section != sentScreen && section != watchScreen) {
			t.Errorf("%s: переход на %q ведёт не в «Что ушло наружу» и не на «Работу Сёмы»", from, target)
		}
	}
}
