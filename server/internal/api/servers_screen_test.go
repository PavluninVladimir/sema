package api

// Экран сервера и раздел «Серверы» (1 октября 2026): у каждой машины свой
// экран, список — тонкие строки, окно, уровень и потолок — поля, а не вопросы.
// Проверяется сборка документа и действия; настоящий Chrome — в
// browser_servers_test.go.

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func docByID(doc *Document, id string) *Component {
	var found *Component
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.ID == id && found == nil {
				found = c
			}
		})
	}
	return found
}

func fieldOf(doc *Document, form, name string) *Field {
	c := docByID(doc, form)
	if c == nil {
		return nil
	}
	for i := range c.Fields {
		if c.Fields[i].Name == name {
			return &c.Fields[i]
		}
	}
	return nil
}

func serverDoc(t *testing.T, s *Server, screen string) *Document {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("экран %s: %v", screen, err)
	}
	return doc
}

// У экрана сервера четыре вкладки, и каждая собирается по своему адресу: адрес
// вкладки — выбранная внутренняя вкладка, раздел в колонке — «Серверы», над
// содержимым «‹ Серверы», а закреплённая строка называет сервер.
func TestServerScreenBuildsEveryTab(t *testing.T) {
	s, _ := serversFix(t, true)
	for _, tab := range []string{serversTabHosts, serversTabAuto, serversTabRules, serversTabLog} {
		screen := entityAddr(serversScreen, "srv-vpn", tab)
		doc := serverDoc(t, s, screen)
		if doc.Screen != screen || doc.ActiveSubtab != screen {
			t.Errorf("%s: экран %q, выбранная вкладка %q", screen, doc.Screen, doc.ActiveSubtab)
		}
		if doc.ActivePanel != serversScreen {
			t.Errorf("%s: в колонке разделов выбран %q", screen, doc.ActivePanel)
		}
		var titles []string
		for _, f := range doc.Subtabs {
			titles = append(titles, f.Title)
		}
		if strings.Join(titles, "|") != "Обзор|Самостоятельность|Правила|Журнал" {
			t.Errorf("%s: вкладки %v", screen, titles)
		}
		if doc.Heading != "srv-vpn" {
			t.Errorf("%s: заголовок %q", screen, doc.Heading)
		}
		if len(doc.Components) < 3 || doc.Components[0].Type != "crumb" || doc.Components[0].Actions[0].Label != "‹ Серверы" ||
			doc.Components[0].Actions[0].Target != serversScreen {
			t.Fatalf("%s: над содержимым нет «‹ Серверы»: %+v", screen, doc.Components[:1])
		}
		bar := doc.Components[1]
		if bar.Type != "section" || bar.Style != "sticky" || len(bar.Children) != 1 || bar.Children[0].Subtitle != "srv-vpn" {
			t.Fatalf("%s: закреплённой строки с именем нет: %+v", screen, bar)
		}
		row := bar.Children[0]
		if len(row.Actions) != 1 || len(row.Selects) != 1 {
			t.Errorf("%s: в шапке одна кнопка и переключатель, а у строки %d действий и %d списков", screen, len(row.Actions), len(row.Selects))
		}
		if len(row.Menu) == 0 {
			t.Errorf("%s: у шапки нет «···»", screen)
		}
	}
	// переключатель ведёт к соседу на той же вкладке
	back, err := s.execute(actionRequest{Action: "entity_goto", Target: serversScreen + "~" + serversTabAuto,
		Params: map[string]string{"value": "Microtik"}})
	if err != nil || back != entityAddr(serversScreen, "Microtik", serversTabAuto) {
		t.Errorf("переключатель: %q (%v)", back, err)
	}
}

// Сервера, которого нет, — не ошибка, а объяснение с дорогой назад.
func TestServerScreenOfMissingHost(t *testing.T) {
	s, _ := serversFix(t, true)
	doc := serverDoc(t, s, entityAddr(serversScreen, "нет-такого", serversTabAuto))
	if doc.Components[0].Type != "crumb" || findByType(doc, "row") == nil {
		t.Fatalf("у экрана несуществующего сервера нет ни пути назад, ни объяснения: %+v", doc.Components)
	}
}

// Прежние адреса из уведомлений и закладок открывают тот же экран на той же
// вкладке, и адрес документа — нынешний, а не прежний.
func TestOldServersAddressesRedirect(t *testing.T) {
	s, _ := serversFix(t, true)
	try := base64.RawURLEncoding.EncodeToString([]byte("rm -rf /tmp/x"))
	cases := map[string]string{
		"settings:servers~log:srv-vpn":              "settings:servers@srv-vpn~log",
		"settings:servers~log:хозяин":               "settings:servers@хозяин~log",
		"settings:servers~rules:srv-vpn":            "settings:servers@srv-vpn~rules",
		"settings:servers~rules:srv-vpn~try:" + try: "settings:servers@srv-vpn~rules~try:" + try,
		// сервер не назван — первый по имени
		"settings:servers~rules": "settings:servers@Microtik~rules",
		// и прежние вкладки раздела, у которых есть адрес и теперь
		"settings:servers~auto": "settings:servers~auto",
		"settings:servers~log":  "settings:servers~log",
		"settings:servers":      "settings:servers",
		"settings:servers~log:": "settings:servers~log",
	}
	for old, want := range cases {
		doc := serverDoc(t, s, old)
		if doc.Screen != want {
			t.Errorf("%s открыл экран %q, а должен %q", old, doc.Screen, want)
		}
	}
	// проверка команды доехала до вердикта
	doc := serverDoc(t, s, "settings:servers~rules:srv-vpn~try:"+try)
	if v := docByID(doc, "rules-verdict"); v == nil || v.Subtitle != "rm -rf /tmp/x" {
		t.Errorf("старый адрес с проверкой команды не показал вердикт: %+v", v)
	}
	// серверов нет — «Правила» без имени ведут в список, а не в ошибку
	empty, _ := testServer(t)
	if doc := serverDoc(t, empty, "settings:servers~rules"); doc.Screen != serversScreen {
		t.Errorf("без серверов «~rules» открыл %q", doc.Screen)
	}
}

// Строка списка тонкая: не больше двух капсул, одна видимая кнопка, «···» с
// короткими путями, и вся строка ведёт на экран сервера. Сверху полка «Ждут
// вас» с командами на кнопке и командой без правила.
func TestServersListRowsAreThin(t *testing.T) {
	s, _ := serversFix(t, true)
	doc := serverDoc(t, s, serversScreen)
	var machines *Component
	for i := range doc.Components {
		if doc.Components[i].Type == "section" && strings.HasPrefix(doc.Components[i].Title, "Машины") {
			machines = &doc.Components[i]
		}
	}
	if machines == nil || len(machines.Children) != 5 {
		t.Fatalf("машин в списке: %+v", machines)
	}
	for _, row := range machines.Children {
		if len(row.Badges) > 2 {
			t.Errorf("%s: капсул %d, а не больше двух", row.Subtitle, len(row.Badges))
		}
		buttons, gotos := 0, 0
		for _, a := range row.Actions {
			if a.Action == "$goto" {
				gotos++
				if a.Target != entityAddr(serversScreen, row.Subtitle, "") {
					t.Errorf("%s: строка ведёт на %q", row.Subtitle, a.Target)
				}
				continue
			}
			buttons++
		}
		if buttons != 1 || gotos != 1 {
			t.Errorf("%s: видимых кнопок %d и переходов %d, а нужны одна и один", row.Subtitle, buttons, gotos)
		}
		if len(row.Menu) == 0 {
			t.Errorf("%s: нет «···»", row.Subtitle)
		}
		if row.Avatar == "" || row.Caption == "" {
			t.Errorf("%s: нет плитки или фразы: %+v", row.Subtitle, row)
		}
	}
	byName := map[string]Component{}
	for _, r := range machines.Children {
		byName[r.Subtitle] = r
	}
	// сменившийся отпечаток — тревожная капсула первой и кнопка «Принять новый
	// отпечаток…» с вопросом
	fp := byName["srv-bots"]
	if len(fp.Badges) == 0 || fp.Badges[0].Text != "отпечаток сменился" || fp.Badges[0].Level != LevelAlert {
		t.Errorf("srv-bots: %+v", fp.Badges)
	}
	if a := hasAction(fp.Actions, "accept_fingerprint"); a == nil || a.Confirm == "" || a.ConfirmOK == "" {
		t.Errorf("srv-bots: нет кнопки принять отпечаток с вопросом: %+v", fp.Actions)
	}
	if b := byName[engine.HostOwnerName].Badges; len(b) != 2 || b[1].Text != "без sudo" {
		t.Errorf("хозяин: %+v", b)
	}
	// полка: команды и «нет правила»
	var shelf *Component
	for i := range doc.Components {
		if doc.Components[i].Type == "shelf" {
			shelf = &doc.Components[i]
		}
	}
	if shelf == nil || shelf.Title != "Ждут вас" || !strings.Contains(shelf.Meta, "4 команды") {
		t.Fatalf("полка: %+v", shelf)
	}
	if doc.Components[0].Type != "shelf" {
		t.Errorf("полка должна стоять первой, а первый компонент %q", doc.Components[0].Type)
	}
	if hasAction(shelf.Actions, "reject_all_ssh") == nil {
		t.Errorf("у полки нет «Снять все с очереди»: %+v", shelf.Actions)
	}
	text := ""
	for _, c := range shelf.Children {
		walkthrough(&c, func(x *Component) {
			text += x.Subtitle + "|"
			for _, a := range append(append([]Action{}, x.Actions...), x.Menu...) {
				text += a.Action + ","
			}
		})
	}
	for _, want := range []string{"sudo apt-get upgrade -y", "approve_ssh", "reject_ssh", "docker stats --no-stream", "rule_ask", "rule_allow"} {
		if !strings.Contains(text, want) {
			t.Errorf("в полке нет %q: %s", want, text)
		}
	}
	// у вкладки «Машины» счётчик ждущих до захода в неё
	if doc.Subtabs[0].Badge != "4 ждут" || doc.Subtabs[0].Level != LevelAlert {
		t.Errorf("вкладка «Машины»: %+v", doc.Subtabs[0])
	}
	// шапка раздела: одна кнопка «Добавить сервер» на экран добавления
	if len(doc.ToolbarActions) != 1 || doc.ToolbarActions[0].Label != "Добавить сервер" ||
		doc.ToolbarActions[0].Target != entityAddr(serversScreen, serversNew, "") || doc.ToolbarActions[0].Style != "primary" {
		t.Errorf("шапка раздела: %+v", doc.ToolbarActions)
	}
}

// У раздела три вкладки; правила у каждого сервера свои, вкладки «Правила» у
// раздела нет.
func TestServersSectionTabs(t *testing.T) {
	s, _ := serversFix(t, true)
	doc := serverDoc(t, s, serversScreen)
	var titles []string
	for _, f := range doc.Subtabs {
		titles = append(titles, f.Title)
	}
	if strings.Join(titles, "|") != "Машины|Самостоятельность|Что делалось" {
		t.Errorf("вкладки раздела: %v", titles)
	}
}

// Самостоятельность раздела: ночная смена, остановленные, «Сёма ведёт сам»,
// суд, скиллы, «Тонкая настройка» с одиннадцатью параметрами и «Опасное»
// последним — со стоп-краном под вопросом.
func TestServersSectionAutonomy(t *testing.T) {
	s, _ := serversFix(t, true)
	doc := serverDoc(t, s, serversScreen+"~"+serversTabAuto)
	last := doc.Components[len(doc.Components)-1]
	if last.Style != "danger" || len(last.Children) != 1 {
		t.Fatalf("последним должно стоять «Опасное»: %+v", last)
	}
	stop := hasAction(last.Children[0].Actions, "stop_all_auto")
	if stop == nil || stop.Confirm == "" || stop.ConfirmOK == "" {
		t.Errorf("стоп-кран без вопроса: %+v", last.Children[0].Actions)
	}
	var fine *Component
	for i := range doc.Components {
		if doc.Components[i].ID == rareID {
			fine = &doc.Components[i]
			if i != len(doc.Components)-2 {
				t.Errorf("«Тонкая настройка» должна стоять прямо перед «Опасным», а стоит %d из %d", i, len(doc.Components))
			}
		}
	}
	if fine == nil || !strings.Contains(fine.Caption, "11 параметров") {
		t.Errorf("«Тонкая настройка»: %+v", fine)
	}
	for _, id := range []string{"ночь-вхолостую", "auto-count", "auto-court", "auto-skills"} {
		if docByID(doc, id) == nil {
			t.Errorf("на «Самостоятельности» раздела нет %q", id)
		}
	}
	// уровень и окно у раздела не правятся: они у каждой машины свои
	for _, name := range []string{"level", "window"} {
		for i := range doc.Components {
			walkthrough(&doc.Components[i], func(c *Component) {
				for _, f := range c.Fields {
					if f.Name == name {
						t.Errorf("поле %q осталось на общей «Самостоятельности»", name)
					}
				}
			})
		}
	}
}

// «Обзор»: свойства — поля (адрес, порт, пользователь, ключ, род), sudo —
// тумблер с вопросом, а «Опасное» — последним, красной карточкой с видимыми
// кнопками, и у каждой есть вопрос и осмысленная надпись «ОК».
func TestServerOverviewFieldsAndDanger(t *testing.T) {
	s, _ := serversFix(t, true)
	doc := serverDoc(t, s, entityAddr(serversScreen, "srv-bots", ""))
	for _, name := range []string{"addr", "port", "user", "key", "kind"} {
		if f := fieldOf(doc, "host-props", name); f == nil {
			t.Errorf("на «Обзоре» нет поля %q", name)
		}
	}
	form := docByID(doc, "host-props")
	if form.Style != formRows || form.Submit == nil || form.Submit.Action != "save_host" || form.Submit.Target != "srv-bots" {
		t.Errorf("форма свойств: %+v", form)
	}
	if !form.Deferred {
		t.Errorf("свойства сохраняет нижняя полоса, а форма отдана не ей")
	}
	if f := fieldOf(doc, "host-props", "kind"); f.Type != "segments" || f.Value != "Linux" {
		t.Errorf("род: %+v", f)
	}
	sudo := docByID(doc, "host-sudo")
	if sudo == nil || sudo.Toggle != "on" || hasAction(sudo.Actions, "toggle_host_sudo") == nil || sudo.Actions[0].Confirm == "" {
		t.Errorf("sudo: %+v", sudo)
	}
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
	for _, name := range []string{"delete_host", "accept_fingerprint"} {
		a, ok := acts[name]
		if !ok || a.Confirm == "" || a.ConfirmOK == "" {
			t.Errorf("в «Опасном» нет %q с вопросом и надписью: %+v", name, a)
		}
	}
	// у сервера со спокойным отпечатком принять нечего
	calm := serverDoc(t, s, entityAddr(serversScreen, "srv-vpn", ""))
	for _, row := range calm.Components[len(calm.Components)-1].Children {
		if hasAction(row.Actions, "accept_fingerprint") != nil {
			t.Error("«Принять новый отпечаток» у сервера, чей отпечаток не менялся")
		}
	}
	// полный отпечаток виден целиком
	if fp := docByID(calm, "host-fingerprint"); fp == nil || !strings.Contains(fp.Caption, "SHA256:Bx3HQBtR2m9kXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTM") {
		t.Errorf("отпечаток: %+v", fp)
	}
}

// Уровень, окно и потолок — поля одной формы, а не вопросы: ни у одного
// действия на экране нет Prompt.
func TestServerAutonomyIsFieldsNotPrompts(t *testing.T) {
	s, _ := serversFix(t, true)
	doc := serverDoc(t, s, entityAddr(serversScreen, "srv-vpn", serversTabAuto))
	form := docByID(doc, "host-auto")
	if form == nil || form.Style != formRows || !form.Deferred || form.Submit.Action != "set_host_autonomy" || form.Submit.Target != "srv-vpn" {
		t.Fatalf("форма: %+v", form)
	}
	level := fieldOf(doc, "host-auto", "level")
	if level.Type != "segments" || strings.Join(level.Options, "|") != "ручное|под судом|полная" || level.Value != "полная" {
		t.Errorf("уровень: %+v", level)
	}
	if !strings.HasPrefix(level.Hint, "Полная") || !strings.Contains(level.Hint, "Ручное") || !strings.Contains(level.Hint, "Под судом") {
		t.Errorf("объяснение уровней: нынешний первым, остальные следом: %q", level.Hint)
	}
	window := fieldOf(doc, "host-auto", "window")
	if window.Type != "timespan" || window.Value != "02:00-06:00" || window.Default == nil || *window.Default != "" {
		t.Errorf("окно: %+v", window)
	}
	max := fieldOf(doc, "host-auto", "max_hour")
	if max.Type != "number" || max.Value != "5" || max.Default == nil || *max.Default != "3" || max.Min != 1 {
		t.Errorf("потолок: %+v", max)
	}
	// Prompt — только для переименований: на этом экране его нет
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
				if a.Prompt != "" && c.ID != "rules-verdict" {
					t.Errorf("у действия %q на экране самостоятельности остался вопрос: %q", a.Action, a.Prompt)
				}
			}
		})
	}
	// остановка и «Продолжить»
	if r := docByID(doc, "auto-paused"); r == nil || r.Subtitle != "Остановка" {
		t.Errorf("остановка: %+v", r)
	}
	h, _ := s.eng.Store().Host("srv-vpn")
	h.Paused = "проба не сошлась"
	_ = s.eng.Store().SaveHost(h)
	doc = serverDoc(t, s, entityAddr(serversScreen, "srv-vpn", serversTabAuto))
	r := docByID(doc, "auto-paused")
	if r == nil || hasAction(r.Actions, "resume_host") == nil || r.Actions[0].Confirm == "" {
		t.Errorf("остановленному серверу нет «Продолжить» с вопросом: %+v", r)
	}
	if tab := doc.Subtabs[1]; tab.Badge != "!" || tab.Level != LevelAlert {
		t.Errorf("вкладка «Самостоятельность» не говорит об остановке: %+v", tab)
	}
}

// Окно и потолок сохраняются полями, и движок видит новое значение; ошибка
// ничего не пишет.
func TestHostAutonomyFormSaves(t *testing.T) {
	s, st := serversFix(t, true)
	save := func(params map[string]string) error {
		_, err := s.execute(actionRequest{Action: "set_host_autonomy", Target: "srv-vpn", Params: params, Screen: entityAddr(serversScreen, "srv-vpn", serversTabAuto)})
		return err
	}
	if err := save(map[string]string{"level": "полная", "window_from": "03:00", "window_to": "05:30", "max_hour": "7"}); err != nil {
		t.Fatal(err)
	}
	h, _ := st.Host("srv-vpn")
	if h.Window != "03:00-05:30" || s.eng.AutoMaxHour(h) != 7 || h.Autonomy != store.AutonomyFull {
		t.Fatalf("сохранено окно %q, потолок %d, уровень %q", h.Window, s.eng.AutoMaxHour(h), h.Autonomy)
	}
	// окно видит и движок: 04:00 — внутри, 06:00 — снаружи
	if !engine.AutoWindowOK(h.Window, time.Date(2026, 10, 1, 4, 0, 0, 0, time.Local)) || engine.AutoWindowOK(h.Window, time.Date(2026, 10, 1, 6, 0, 0, 0, time.Local)) {
		t.Errorf("движок читает окно %q неверно", h.Window)
	}
	// потолок, равный общему, — «как у всех»: 0, и сервер идёт за общим
	if err := save(map[string]string{"max_hour": "3"}); err != nil {
		t.Fatal(err)
	}
	if h, _ = st.Host("srv-vpn"); h.MaxHour != 0 || s.eng.AutoMaxHour(h) != 3 {
		t.Errorf("потолок, равный общему, хранится как %d", h.MaxHour)
	}
	// пустые оба поля — круглые сутки
	if err := save(map[string]string{"window_from": "", "window_to": ""}); err != nil {
		t.Fatal(err)
	}
	if h, _ = st.Host("srv-vpn"); h.Window != "" {
		t.Errorf("окно после «круглые сутки»: %q", h.Window)
	}
	// ошибки не пишут ничего: половина окна, совпадающие концы, чужой уровень, потолок вне границ
	before, _ := st.Host("srv-vpn")
	for name, params := range map[string]map[string]string{
		"половина окна":  {"window_from": "02:00", "window_to": ""},
		"окно из точки":  {"window_from": "02:00", "window_to": "02:00"},
		"чужой уровень":  {"level": "как-нибудь"},
		"потолок вне":    {"max_hour": "0"},
		"потолок словом": {"max_hour": "много"},
		// ошибка в одном поле не даёт записать остальные
		"окно с хорошим потолком": {"window_from": "02:00", "window_to": "", "max_hour": "9"},
	} {
		if err := save(params); err == nil {
			t.Errorf("%s: принято", name)
		}
		if after, _ := st.Host("srv-vpn"); after.Window != before.Window || after.MaxHour != before.MaxHour || after.Autonomy != before.Autonomy {
			t.Errorf("%s: что-то записалось: %+v → %+v", name, before, after)
		}
	}
	// сохранение окна не снимает остановку, а смена уровня — снимает
	h, _ = st.Host("srv-vpn")
	h.Paused = "проба не сошлась"
	_ = st.SaveHost(h)
	if err := save(map[string]string{"level": "полная", "window_from": "01:00", "window_to": "02:00"}); err != nil {
		t.Fatal(err)
	}
	if h, _ = st.Host("srv-vpn"); h.Paused == "" || h.Window != "01:00-02:00" {
		t.Errorf("сохранение окна сняло остановку: %+v", h)
	}
	if err := save(map[string]string{"level": "под судом"}); err != nil {
		t.Fatal(err)
	}
	if h, _ = st.Host("srv-vpn"); h.Paused != "" || h.Autonomy != store.AutonomyCourt {
		t.Errorf("смена уровня не сняла остановку: %+v", h)
	}
}

// Недоступный уровень виден серым, с причиной от движка, и сервером не
// принимается: те же проверки, что и раньше (суд, sudo, хозяин).
func TestUnavailableAutonomyShownWithReasonAndRefused(t *testing.T) {
	// суд не собран: недоступны оба уровня
	s, st := serversFix(t, false)
	why := func() string { _, w := s.eng.CourtReady(); return w }()
	doc := serverDoc(t, s, entityAddr(serversScreen, "WireGuard", serversTabAuto))
	level := fieldOf(doc, "host-auto", "level")
	if strings.Join(level.Disabled, "|") != "под судом|полная" {
		t.Errorf("отключены %v", level.Disabled)
	}
	row := docByID(doc, "auto-unavailable")
	if row == nil || !strings.Contains(row.Caption, why) || row.Level != LevelWarn || hasAction(row.Actions, "$goto") == nil {
		t.Errorf("причина не названа: %+v", row)
	}
	h, _ := st.Host("Microtik")
	h.Autonomy, h.Auto = store.AutonomyOff, false
	_ = st.SaveHost(h)
	for _, level := range []string{"под судом", "полная"} {
		_, err := s.execute(actionRequest{Action: "set_host_autonomy", Target: "Microtik", Params: map[string]string{"level": level}})
		if err == nil || !strings.Contains(err.Error(), "суд не собран") {
			t.Errorf("%s без суда принят или без причины: %v", level, err)
		}
	}
	if h, _ = st.Host("Microtik"); h.Autonomy != store.AutonomyOff {
		t.Errorf("уровень записан вопреки отказу: %q", h.Autonomy)
	}

	// суд собран: «под судом» доступен, «полная» — не «хозяину» и не без sudo
	s, st = serversFix(t, true)
	for name, want := range map[string]struct{ off, why string }{
		engine.HostOwnerName: {"полная", "не дан sudo"},
		"srv-vpn":            {"", ""},
	} {
		doc := serverDoc(t, s, entityAddr(serversScreen, name, serversTabAuto))
		level := fieldOf(doc, "host-auto", "level")
		if strings.Join(level.Disabled, "|") != want.off {
			t.Errorf("%s: отключены %v, ждали %q", name, level.Disabled, want.off)
		}
		row := docByID(doc, "auto-unavailable")
		if (want.why == "") != (row == nil) || (row != nil && !strings.Contains(row.Caption, want.why)) {
			t.Errorf("%s: причина %+v, ждали %q", name, row, want.why)
		}
	}
	// «хозяин» без sudo: сначала sudo — и причина про sudo, и кнопка к свойствам
	doc = serverDoc(t, s, entityAddr(serversScreen, engine.HostOwnerName, serversTabAuto))
	if row := docByID(doc, "auto-unavailable"); row == nil || !strings.Contains(row.Caption, "не дан sudo") || hasAction(row.Actions, "$goto") == nil {
		t.Errorf("хозяин без sudo: %+v", row)
	}
	if _, err := s.execute(actionRequest{Action: "set_host_autonomy", Target: engine.HostOwnerName, Params: map[string]string{"level": "полная"}}); err == nil {
		t.Error("«хозяину» без sudo отдана полная самостоятельность")
	}
	// и с sudo «хозяину» её не дают: сломать машину, где живёт Сёма, значит остаться без него
	owner, _ := st.Host(engine.HostOwnerName)
	owner.NoSudo = false
	_ = st.SaveHost(owner)
	doc = serverDoc(t, s, entityAddr(serversScreen, engine.HostOwnerName, serversTabAuto))
	if row := docByID(doc, "auto-unavailable"); row == nil || !strings.Contains(row.Caption, "где живёт сам Сёма") || len(row.Actions) != 0 {
		t.Errorf("хозяин с sudo: %+v", row)
	}
	if _, err := s.execute(actionRequest{Action: "set_host_autonomy", Target: engine.HostOwnerName, Params: map[string]string{"level": "полная"}}); err == nil ||
		!strings.Contains(err.Error(), "где живёт сам Сёма") {
		t.Errorf("«хозяину» с sudo отдана полная самостоятельность: %v", err)
	}
	if h, _ := st.Host("WireGuard"); h.NoSudo {
		t.Fatal("WireGuard без sudo")
	}
	h, _ = st.Host("WireGuard")
	h.NoSudo = true
	_ = st.SaveHost(h)
	if _, err := s.execute(actionRequest{Action: "set_host_autonomy", Target: "WireGuard", Params: map[string]string{"level": "полная"}}); err == nil ||
		!strings.Contains(err.Error(), "не дан sudo") {
		t.Errorf("серверу без sudo отдана полная: %v", err)
	}
	// «под судом» серверу без sudo — можно, как и раньше
	if _, err := s.execute(actionRequest{Action: "set_host_autonomy", Target: "WireGuard", Params: map[string]string{"level": "под судом"}}); err != nil {
		t.Errorf("под судом серверу без sudo: %v", err)
	}
}

// Свойства сервера сохраняются полями; ошибка не пишет ничего.
func TestSaveHostFields(t *testing.T) {
	s, st := serversFix(t, true)
	save := func(params map[string]string) (string, error) {
		return s.execute(actionRequest{Action: "save_host", Target: "srv-vpn", Params: params, Screen: entityAddr(serversScreen, "srv-vpn", "")})
	}
	back, err := save(map[string]string{"addr": "198.51.100.7", "port": "2200", "user": "ops", "key": "ssh:Microtik", "kind": "RouterOS"})
	if err != nil || back != entityAddr(serversScreen, "srv-vpn", "") {
		t.Fatalf("%v %q", err, back)
	}
	h, _ := st.Host("srv-vpn")
	if h.Addr != "198.51.100.7" || h.Port != 2200 || h.User != "ops" || h.Key != "ssh:Microtik" || h.Kind != store.HostRouterOS {
		t.Errorf("записано: %+v", h)
	}
	// отпечаток и самостоятельность не тронуты
	if h.Fingerprint == "" || h.Autonomy != store.AutonomyFull {
		t.Errorf("правка свойств задела отпечаток или уровень: %+v", h)
	}
	for name, params := range map[string]map[string]string{
		"пустой адрес":     {"addr": " "},
		"порт словом":      {"port": "ssh"},
		"порт вне":         {"port": "70000"},
		"нет пользователя": {"user": ""},
		"чужой ключ":       {"key": "нет-такого"},
		"чужой род":        {"kind": "Windows"},
		"хорошее с плохим": {"addr": "203.0.113.99", "port": "0"},
	} {
		if _, err := save(params); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
	if after, _ := st.Host("srv-vpn"); after.Addr != "198.51.100.7" || after.Port != 2200 {
		t.Errorf("ошибка записала часть: %+v", after)
	}
}

// Добавление: тот же адрес не переписывает сервер, имя без знаков адреса,
// ответ — экран нового сервера. Проверка связи идёт в фоне и упирается в
// неразбираемый ключ раньше сети; дожидаемся её условием, а не сроком.
func TestAddHostChecks(t *testing.T) {
	s, st := serversFix(t, true)
	add := func(params map[string]string) (string, error) {
		return s.execute(actionRequest{Action: "add_host", Params: params})
	}
	if _, err := add(map[string]string{"name": "srv-vpn", "addr": "198.51.100.1", "key": "ssh:srv-vpn"}); err == nil {
		t.Error("существующий сервер переписан добавлением")
	}
	if h, _ := st.Host("srv-vpn"); h.Addr != "203.0.113.12" {
		t.Errorf("адрес существующего сервера переписан: %q", h.Addr)
	}
	for _, name := range []string{"a@b", "web~3"} {
		if _, err := add(map[string]string{"name": name, "addr": "198.51.100.1", "key": "ssh:srv-vpn"}); err == nil {
			t.Errorf("имя %q принято", name)
		}
	}
	if _, err := add(map[string]string{"name": "web-3", "addr": "198.51.100.1", "port": "99999", "key": "ssh:srv-vpn"}); err == nil {
		t.Error("порт вне границ принят")
	}
	back, err := add(map[string]string{"name": "web-3", "addr": "198.51.100.3", "port": "2022", "user": "ops", "key": "ssh:srv-vpn"})
	if err != nil || back != entityAddr(serversScreen, "web-3", "") {
		t.Fatalf("%v %q", err, back)
	}
	h, ok := st.Host("web-3")
	if !ok || h.Port != 2022 || h.User != "ops" || !h.NoSudo {
		t.Fatalf("записано: %+v", h)
	}
	// фоновая проверка связи кончилась (ключ не разобран, сети не было): дальше
	// тест закрывает базу
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h, _ := st.Host("web-3"); h.LastErr != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("фоновая проверка связи не кончилась")
}

// «Добавить сервер»: обязательные поля строками, порт и sudo под «Ещё N полей»
// в той же форме, а «Подключить хозяина» достижим.
func TestServerNewScreen(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveSecret(&store.Secret{Name: "ssh:Microtik", Kind: store.SecretSSH}, "приватная часть"); err != nil {
		t.Fatal(err)
	}
	doc := serverDoc(t, s, entityAddr(serversScreen, serversNew, ""))
	if doc.Heading != "Добавить сервер" || doc.Components[0].Type != "crumb" {
		t.Errorf("шапка: %q %+v", doc.Heading, doc.Components[0])
	}
	form := docByID(doc, "addhost")
	if form == nil || form.Style != formRows || form.Submit.Action != "add_host" || form.Deferred {
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
	if strings.Join(plain, ",") != "name,addr,user,key" || !more["port"] || !more["sudo"] {
		t.Errorf("обязательные %v, редкие %v", plain, more)
	}
	// хозяина ещё нет — форма «Подключить хозяина» на месте, поле пользователя на месте, а не вопрос
	home := docByID(doc, "home-host-form")
	if home == nil || home.Submit.Action != "add_home_host" || home.Submit.Prompt != "" || fieldOf(doc, "home-host-form", "user").Name != "user" {
		t.Errorf("«Подключить хозяина»: %+v", home)
	}
	// хозяин подключён — блока нет
	if err := st.SaveHost(&store.Host{Name: engine.HostOwnerName, Addr: "172.18.0.1", User: "u", Key: "ssh:Microtik"}); err != nil {
		t.Fatal(err)
	}
	if docByID(serverDoc(t, s, entityAddr(serversScreen, serversNew, "")), "home-host-form") != nil {
		t.Error("«Подключить хозяина» остался у подключённого хозяина")
	}
	// ключей нет — «Сначала ключ» с переходом на «Ключи и секреты»
	bare, _ := testServer(t)
	nokey := serverDoc(t, bare, entityAddr(serversScreen, serversNew, ""))
	if r := docByID(nokey, "addhost-no-key"); r == nil || r.Actions[0].Target != secretsScreen {
		t.Errorf("без ключей: %+v", r)
	}
	// из списка к «Подключить хозяина» ведёт меню шапки, пока хозяина нет
	list := serverDoc(t, bare, serversScreen)
	if len(list.ToolbarMenu) != 1 || !strings.Contains(list.ToolbarMenu[0].Label, "хозяин") {
		t.Errorf("меню шапки пустого списка: %+v", list.ToolbarMenu)
	}
}

// Прежние действия остались: на них есть кнопки в уведомлениях, карточках и
// скриптах, и они работают так же, как работали, хотя на экране их заменили
// поля.
func TestHostActionsWithoutScreenStillWork(t *testing.T) {
	s, st := serversFix(t, true)
	do := func(action, host string, params map[string]string) {
		t.Helper()
		if _, err := s.execute(actionRequest{Action: action, Target: host, Params: params}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	do("set_host_kind", "WireGuard", map[string]string{"value": "род: RouterOS"})
	do("set_host_window", "WireGuard", map[string]string{"value": "01:00-02:00"})
	do("set_host_max", "WireGuard", map[string]string{"value": "9"})
	h, _ := st.Host("WireGuard")
	if h.Kind != store.HostRouterOS || h.Window != "01:00-02:00" || h.MaxHour != 9 {
		t.Errorf("WireGuard: %+v", h)
	}
	do("toggle_host_auto", "WireGuard", nil) // под судом → ручное
	if h, _ = st.Host("WireGuard"); h.Auto {
		t.Error("toggle_host_auto не выключил самостоятельность")
	}
	do("toggle_host_auto", "WireGuard", nil)
	if h, _ = st.Host("WireGuard"); h.Autonomy != store.AutonomyCourt {
		t.Errorf("toggle_host_auto не включил «под судом»: %q", h.Autonomy)
	}
	// прежний вызов с вопросом: уровень словом в value
	do("set_host_autonomy", "WireGuard", map[string]string{"value": "ручное"})
	if h, _ = st.Host("WireGuard"); h.Auto {
		t.Error("set_host_autonomy со словом не сработал")
	}
	if _, err := s.execute(actionRequest{Action: "set_host_window", Target: "WireGuard", Params: map[string]string{"value": "утром"}}); err == nil {
		t.Error("неразборчивое окно принято")
	}
	do("toggle_host_sudo", "WireGuard", nil)
	if h, _ = st.Host("WireGuard"); !h.NoSudo {
		t.Error("toggle_host_sudo не запретил sudo")
	}
	do("resume_host", "srv-vpn", nil)
	// стоп-кран ставит остановку каждому неручному серверу
	do("stop_all_auto", "", nil)
	if h, _ = st.Host("srv-vpn"); h.Paused == "" {
		t.Error("стоп-кран не остановил srv-vpn")
	}
	do("resume_host", "srv-vpn", nil)
	if h, _ = st.Host("srv-vpn"); h.Paused != "" {
		t.Error("«Продолжить» не снял остановку")
	}
	// «Принять новый отпечаток» и удаление
	do("accept_fingerprint", "srv-bots", nil)
	if h, _ = st.Host("srv-bots"); h.NewFP != "" || !strings.HasPrefix(h.Fingerprint, "SHA256:Zk81") {
		t.Errorf("отпечаток не принят: %+v", h)
	}
	back, err := s.execute(actionRequest{Action: "delete_host", Target: "srv-bots", Screen: entityAddr(serversScreen, "srv-bots", "")})
	if err != nil || back != serversScreen {
		t.Fatalf("удаление: %v %q", err, back)
	}
	if _, ok := st.Host("srv-bots"); ok {
		t.Error("сервер не удалён")
	}
	// после удаления экран сервера объясняет, а не падает
	serverDoc(t, s, entityAddr(serversScreen, "srv-bots", ""))
}

// «Проверить связь» с экрана сервера оставляет на экране сервера, а не уводит в
// список (проверка идёт в фоне и упирается в неразбираемый ключ).
func TestActionsKeepTheOpenScreen(t *testing.T) {
	s, st := serversFix(t, true)
	here := entityAddr(serversScreen, "srv-vpn", serversTabAuto)
	// «Проверить связь» — последней: она пишет в базу из фона, и действия после
	// неё затёрли бы её запись своей, прочитанной раньше
	for _, action := range []string{"resume_host", "toggle_host_sudo", "stop_all_auto", "check_host"} {
		back, err := s.execute(actionRequest{Action: action, Target: "srv-vpn", Screen: here})
		if err != nil || back != here {
			t.Errorf("%s: вернул %q (%v), а нужно остаться на %q", action, back, err, here)
		}
	}
	// с чужого экрана — на список
	back, _ := s.execute(actionRequest{Action: "check_host", Target: "srv-vpn", Screen: "inbox"})
	if back != serversScreen {
		t.Errorf("с чужого экрана вернул %q", back)
	}
	// фоновая проверка связи пишет в базу: дожидаемся её, прежде чем тест её закроет
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h, _ := st.Host("srv-vpn"); h.LastErr != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("фоновая проверка связи не кончилась")
}

// Журнал: первая страница — двадцать строк и «Показать ещё», чипы отбирают, а
// у чипа число.
func TestServersJournalFiltersAndPages(t *testing.T) {
	s, st := serversFix(t, true)
	serversManyLog(t, st, "srv-vpn", 30)
	// у журнала раздела: все серверы
	doc, err := s.buildScreen(serversScreen+"~"+serversTabLog, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	count := func(d *Document) int {
		n := 0
		for _, c := range d.Components {
			if c.Type == "section" && strings.HasPrefix(c.Title, "Что делалось") {
				n = len(c.Children)
			}
		}
		return n
	}
	if n := count(doc); n != serversLogPage {
		t.Errorf("первая страница общего журнала: %d строк", n)
	}
	more := false
	for _, c := range doc.Components {
		if c.Type == "button" && len(c.Actions) == 1 && c.Actions[0].Action == "$more" && strings.Contains(c.Actions[0].Label, "20 из 36") {
			more = true
		}
	}
	if !more {
		t.Errorf("нет «Показать ещё · 20 из 36»: %+v", doc.Components)
	}
	// «Показать ещё» — клиент удваивает предел
	if n := func() int { d, _ := s.buildScreen(serversScreen+"~"+serversTabLog, "", "", 100); return count(d) }(); n != 36 {
		t.Errorf("со всем пределом: %d строк", n)
	}
	var chips []string
	for _, f := range doc.Filters {
		chips = append(chips, f.Title)
	}
	if strings.Join(chips, "|") != "Все · 36|Выполнены · 35|Отказы · 1|Сёма сам · 1|Ночная смена · 3" {
		t.Errorf("чипы: %v", chips)
	}
	if !doc.noPhoneSearch {
		t.Error("у журнала на телефоне осталось верхнее поле поиска")
	}
	// отборы
	for filter, want := range map[string]int{store.SSHLogDone: 35, store.SSHLogDenied: 1, store.SSHLogSelf: 1, store.SSHLogNight: 3, "": 36} {
		d, err := s.buildScreen(serversScreen+"~"+serversTabLog, filter, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		if n := count(d); n != want || d.ActiveFilter != filter {
			t.Errorf("отбор %q: %d строк при %d ожидаемых, активный %q", filter, n, want, d.ActiveFilter)
		}
	}
	// журнал сервера — только его строки, без имени сервера в подписи
	d := serverDoc(t, s, entityAddr(serversScreen, "srv-vpn", serversTabLog))
	if n := count(d); n != serversLogPage {
		t.Errorf("первая страница журнала сервера: %d", n)
	}
	for _, c := range d.Components {
		if c.Type == "section" && strings.HasPrefix(c.Title, "Что делалось") {
			for _, r := range c.Children {
				if strings.HasPrefix(r.Caption, "srv-vpn ·") {
					t.Errorf("в журнале сервера его имя стоит в подписи: %q", r.Caption)
				}
			}
		}
	}
	// «выполнена · сам» — своей капсулой, а не пустой
	d, _ = s.buildScreen(serversScreen+"~"+serversTabLog, store.SSHLogSelf, "", 100)
	for _, c := range d.Components {
		if c.Type == "section" && len(c.Children) > 0 && (len(c.Children[0].Badges) != 1 || c.Children[0].Badges[0].Text != "выполнена · сам") {
			t.Errorf("капсула самой Сёмы: %+v", c.Children[0].Badges)
		}
	}
}

// Окно — два поля времени, склеенные сервером: оба пусты — круглые сутки,
// одно — ошибка, совпадающие — ошибка.
func TestTimespanParam(t *testing.T) {
	for name, tc := range map[string]struct {
		in      map[string]string
		want    string
		present bool
		fails   bool
	}{
		"не пришло":      {map[string]string{}, "", false, false},
		"оба":            {map[string]string{"w_from": "02:00", "w_to": "06:00"}, "02:00-06:00", true, false},
		"через полночь":  {map[string]string{"w_from": "22:00", "w_to": "06:00"}, "22:00-06:00", true, false},
		"круглые сутки":  {map[string]string{"w_from": "", "w_to": ""}, "", true, false},
		"без нуля":       {map[string]string{"w_from": "2:00", "w_to": "6:00"}, "2:00-6:00", true, false},
		"только «с»":     {map[string]string{"w_from": "02:00", "w_to": ""}, "", true, true},
		"только «до»":    {map[string]string{"w_to": "02:00"}, "", true, true},
		"одно и то же":   {map[string]string{"w_from": "02:00", "w_to": "02:00"}, "", true, true},
		"не время":       {map[string]string{"w_from": "утро", "w_to": "вечер"}, "", true, true},
		"часы вне суток": {map[string]string{"w_from": "25:00", "w_to": "26:00"}, "", true, true},
	} {
		got, present, err := timespanParam(tc.in, "w")
		if (err != nil) != tc.fails || present != tc.present || got != tc.want {
			t.Errorf("%s: %q, пришло %v, ошибка %v", name, got, present, err)
		}
	}
}

// Каждый переход на серверы, который собирают экраны, ведёт на существующий
// экран по нынешнему адресу — а не на тот, что открывается только благодаря
// переадресации.
func TestServersGotosLeadToRealScreens(t *testing.T) {
	s, _ := serversFix(t, true)
	screens := []string{serversScreen, serversScreen + "~" + serversTabAuto, serversScreen + "~" + serversTabLog,
		entityAddr(serversScreen, serversNew, "")}
	for _, h := range []string{"srv-vpn", "srv-bots", "Microtik", "WireGuard", engine.HostOwnerName} {
		for _, tab := range []string{serversTabHosts, serversTabAuto, serversTabRules, serversTabLog} {
			screens = append(screens, entityAddr(serversScreen, h, tab))
		}
	}
	targets := map[string]bool{}
	for _, screen := range screens {
		doc := serverDoc(t, s, screen)
		for i := range doc.Components {
			walkthrough(&doc.Components[i], func(c *Component) {
				for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
					if a.Action == "$goto" && strings.HasPrefix(a.Target, serversScreen) {
						targets[a.Target] = true
					}
				}
			})
		}
		for _, a := range append(append([]Action{}, doc.ToolbarActions...), doc.ToolbarMenu...) {
			if a.Action == "$goto" && strings.HasPrefix(a.Target, serversScreen) {
				targets[a.Target] = true
			}
		}
		for _, f := range doc.Subtabs {
			targets[f.ID] = true
		}
	}
	if len(targets) < 10 {
		t.Fatalf("переходов собрано слишком мало: %v", targets)
	}
	for target := range targets {
		doc, err := s.buildScreen(target, "", "", 0)
		if err != nil {
			t.Errorf("переход %q: %v", target, err)
			continue
		}
		if doc.Screen != target {
			t.Errorf("переход %q открыл %q: адрес держится на переадресации", target, doc.Screen)
		}
	}
	// и то, что кладёт ядро в уведомления: по каждому адресу — экран сервера на своей вкладке
	for _, tab := range []string{"", "auto", "rules", "log"} {
		addr := engine.ServerScreen("srv-vpn", tab)
		doc := serverDoc(t, s, addr)
		if doc.Screen != addr || doc.Heading != "srv-vpn" {
			t.Errorf("уведомление ведёт на %q, открылось %q «%s»", addr, doc.Screen, doc.Heading)
		}
	}
}

// Старых хвостов в коде не заводят: «~log:имя» и «~rules:имя» остались только
// как адреса, которые умеют открывать. Новые уведомления и переходы строятся
// по нынешним.
func TestNoOldServerAddressesInCode(t *testing.T) {
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
			for _, old := range []string{`servers~log:`, `servers~rules:`, `servers~rules"`} {
				if strings.Contains(string(raw), old) {
					found = append(found, path+": "+old)
				}
			}
			return nil
		})
	}
	if len(found) > 0 {
		t.Errorf("в коде строятся прежние адреса серверов (открываются они только по переадресации): %v", found)
	}
}
