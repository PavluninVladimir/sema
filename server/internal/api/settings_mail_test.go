package api

// «Почта» в настройках, 2 октября 2026: раздел из трёх вкладок (Ящики · Спам ·
// Общее) и экран ящика из пяти (Обработка · Подключение · Отправка · Папки ·
// Хранение). Проверяется то, что обещано человеку: все экраны собираются,
// старые адреса открываются, ни одна настройка и ни одно действие не пропали,
// общее не живёт на экране ящика и наоборот, каждая форма сохраняется так, что
// движок видит новое значение, а секреты в разметку не попадают.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Подсеянные секреты: ищем их в разметке, и ни один не должен там найтись.
const (
	mailSecretApp   = "СЕКРЕТ-ПАРОЛЬ-ПРИЛОЖЕНИЯ-7741"
	mailSecretOAuth = "СЕКРЕТ-OAUTH-КЛИЕНТА-5528"
	mailSecretWork  = "СЕКРЕТ-ПАРОЛЬ-РАБОТЫ-9013"
	mailSecretSMTP  = "СЕКРЕТ-ПАРОЛЬ-SMTP-4410"
)

var mailSecrets = []string{mailSecretApp, mailSecretOAuth, mailSecretWork, mailSecretSMTP}

// Ящики фикстуры: пароль приложения, Gmail с OAuth и рабочий закрытый «только
// читать», которому не удаётся войти.
const (
	mailBoxApp  = "проба"
	mailBoxGoog = "gmail@пример.рф"
	mailBoxWork = "работа@пример.рф"
)

var mailBoxNames = []string{mailBoxApp, mailBoxGoog, mailBoxWork}

// mailFix — три ящика разных способов входа и режимов, ждущие решения письма,
// свой скрипт-задача и пароль SMTP в хранилище. Настоящих соединений нет:
// ящики не подключаются, а проверка входа ни одним тестом не зовётся.
func mailFix(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	if err := st.SaveAccounts([]*store.Account{
		{Name: mailBoxApp, Email: "проба@пример.рф", Host: "imap.пример.рф", Port: 993, Password: mailSecretApp},
		{Name: mailBoxGoog, Email: mailBoxGoog, Host: "imap.gmail.com", Port: 993},
		{Name: mailBoxWork, Email: mailBoxWork, Host: "imap.пример.рф", Port: 993, Password: mailSecretWork},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSecret(&store.Secret{Name: "mail:проба-smtp", Kind: "пароль"}, mailSecretSMTP); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339)
	folders := []string{"INBOX", "Archive", "Spam", "Работа", "Sent", "[Gmail]/Вся почта", "[Gmail]/Корзина", "[Gmail]/Черновики"}
	closed := false
	confs := map[string]*store.MailConf{
		mailBoxApp: {Auth: store.AuthAppPassword, Username: "проба@пример.рф", Password: mailSecretApp,
			Host: "imap.пример.рф", Port: 993, Folders: folders, Archive: "Archive", Junk: "Spam", Connected: now,
			SMTPHost: "smtp.пример.рф", SMTPPort: 587, SMTPStartTLS: true, SMTPSecret: "mail:проба-smtp"},
		mailBoxGoog: {Auth: store.AuthGoogle, Username: mailBoxGoog, ClientID: "клиент.apps.googleusercontent.com",
			Secret: mailSecretOAuth, Folders: folders, Archive: "[Gmail]/Вся почта", Junk: "Spam", Connected: now},
		mailBoxWork: {Auth: store.AuthAppPassword, Username: mailBoxWork, Password: mailSecretWork,
			Host: "imap.пример.рф", Port: 993, Folders: folders, Archive: "Archive", Junk: "Spam",
			Purpose: store.PurposeWork, Mode: store.ModeReadOnly, Outbound: &closed,
			LastErr: "dial tcp 203.0.113.5:993: i/o timeout"},
	}
	for name, c := range confs {
		if err := s.eng.SaveMailConf(name, c); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := st.InsertMessage(&store.Message{MessageID: fmt.Sprintf("<ждёт%d@т>", i), Subject: "Похоже на спам",
			FromAddr: "спам@пример.рф", Status: store.StatusPending, Account: mailBoxApp}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertMessage(&store.Message{MessageID: "<входящее@т>", Subject: "Входящее",
		FromAddr: "кто-то@пример.рф", Status: store.StatusInboxOK, Account: mailBoxApp}); err != nil {
		t.Fatal(err)
	}
	// своя задача, которую подключают к ящику, и две встроенные, невозможные в
	// «только читает» (их заводит запуск движка, а в тесте базы пустая)
	for _, task := range []*store.Task{
		{Title: "Своя задача", Schedule: "раз в час", Prompt: "что-нибудь"},
		{Title: "Папки ящика", System: engine.WorkerFolders, Schedule: "раз в час", Enabled: true},
		{Title: "Очистка спама", System: engine.WorkerPurge, Schedule: "раз в час", Enabled: true},
	} {
		if err := st.CreateTask(task); err != nil {
			t.Fatal(err)
		}
	}
	return s, st
}

var mailBoxTabsAll = []string{"", mailTabConn, mailTabSend, mailTabFolders, mailTabKeep}

// mailAllScreens — каждый экран «Почты»: три вкладки раздела, «Новый ящик» и
// пять вкладок каждого ящика.
func mailAllScreens() []string {
	out := []string{mailScreen, entityAddr(mailScreen, "", mailTabSpam), entityAddr(mailScreen, "", mailTabCommon),
		entityAddr(mailScreen, mailNew, "")}
	for _, name := range mailBoxNames {
		for _, tab := range mailBoxTabsAll {
			out = append(out, entityAddr(mailScreen, name, tab))
		}
	}
	return out
}

// walkDoc — по всем компонентам документа и их детям.
func walkDoc(doc *Document, fn func(*Component)) {
	for i := range doc.Components {
		walkthrough(&doc.Components[i], fn)
	}
}

// findForm — форма по номеру на экране или в его «Тонкой настройке».
func findForm(t *testing.T, s *Server, screen, id string) *Component {
	t.Helper()
	var found *Component
	look := func(cs []Component) {
		for i := range cs {
			walkthrough(&cs[i], func(c *Component) {
				if c.Type == "form" && c.ID == id && found == nil {
					found = c
				}
			})
		}
	}
	doc := serverDoc(t, s, screen)
	look(doc.Components)
	if found == nil {
		look(rareBody(t, s, screen))
	}
	if found == nil {
		t.Fatalf("на экране %s нет формы %q", screen, id)
	}
	return found
}

// formParams — то, что отправила бы форма нетронутой: значения полей, а у
// тумблера — «true» или «false» (браузер не шлёт снятый флажок, а `toggles`
// говорит серверу, что он снят). Закрытые поля пусты.
func formParams(f *Component) map[string]string {
	params := map[string]string{}
	for _, fld := range f.Fields {
		switch fld.Type {
		case "toggle":
			params[fld.Name] = boolValue(truthy(fld.Value))
		case "secure":
		default:
			params[fld.Name] = fld.Value
		}
	}
	return params
}

// mailSave — сохранить форму так, как её сохранит нижняя полоса: действием
// самой формы с её значениями, одно из которых изменено.
func mailSave(t *testing.T, s *Server, screen, formID string, override map[string]string) {
	t.Helper()
	form := findForm(t, s, screen, formID)
	if form.Submit == nil {
		t.Fatalf("у формы %q нет действия", formID)
	}
	params := formParams(form)
	for k, v := range override {
		if _, ok := params[k]; !ok && !isSecureField(form, k) {
			t.Fatalf("у формы %q нет поля %q", formID, k)
		}
		params[k] = v
	}
	if _, err := s.execute(actionRequest{Action: form.Submit.Action, Target: form.Submit.Target, Screen: screen, Params: params}); err != nil {
		t.Fatalf("форма %q: %v", formID, err)
	}
}

func isSecureField(f *Component, name string) bool {
	for _, fld := range f.Fields {
		if fld.Name == name && fld.Type == "secure" {
			return true
		}
	}
	return false
}

// ---- сборка и адреса ----

// Каждая вкладка раздела и каждая вкладка каждого ящика собирается; раздел в
// колонке — «Почта»; у вкладки свой адрес; над ящиком — «‹ Почта», потом шапка
// сущности, а вкладок раздела на нём нет.
func TestMailScreensBuildEveryTab(t *testing.T) {
	s, _ := mailFix(t)
	for _, screen := range mailAllScreens() {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if doc.ActivePanel != mailScreen {
			t.Errorf("%s: в колонке выбран %q", screen, doc.ActivePanel)
		}
		if doc.Screen != screen {
			t.Errorf("%s: документ называет себя %q", screen, doc.Screen)
		}
		a := parseMailAddr(screen)
		switch {
		case a.Box == mailNew:
			if doc.Heading != "Новый ящик" || len(doc.Subtabs) != 0 {
				t.Errorf("%s: заголовок %q, вкладок %d", screen, doc.Heading, len(doc.Subtabs))
			}
		case a.Box == "":
			if doc.Heading != "Почта" || len(doc.Subtabs) != 3 || doc.ActiveSubtab != screen {
				t.Errorf("%s: заголовок %q, вкладок %d, выбрана %q", screen, doc.Heading, len(doc.Subtabs), doc.ActiveSubtab)
			}
			names := []string{}
			for _, tab := range doc.Subtabs {
				names = append(names, tab.Title)
			}
			if strings.Join(names, "|") != "Ящики|Спам|Общее" {
				t.Errorf("%s: вкладки раздела %v", screen, names)
			}
			if len(doc.ToolbarActions) != 1 || doc.ToolbarActions[0].Label != "Добавить ящик" || doc.ToolbarActions[0].Style != "primary" {
				t.Errorf("%s: кнопки шапки %+v", screen, doc.ToolbarActions)
			}
		default:
			names := []string{}
			for _, tab := range doc.Subtabs {
				names = append(names, tab.Title)
			}
			if strings.Join(names, "|") != "Обработка|Подключение|Отправка|Папки|Хранение" {
				t.Errorf("%s: вкладки ящика %v", screen, names)
			}
			if doc.ActiveSubtab != screen {
				t.Errorf("%s: выбрана вкладка %q", screen, doc.ActiveSubtab)
			}
			if len(doc.Components) < 2 || doc.Components[0].Type != "crumb" || doc.Components[0].Actions[0].Label != "‹ Почта" ||
				doc.Components[0].Actions[0].Target != mailScreen || doc.Components[1].ID != "entity" {
				t.Errorf("%s: над содержимым нет «‹ Почта» и шапки ящика", screen)
			}
			if doc.Layout != "read" {
				t.Errorf("%s: ширина %q, у экрана сущности — read", screen, doc.Layout)
			}
		}
	}
}

// Старые адреса открывают то же, что открывали: вкладки ящика без ящика — на
// первом ящике, «Спам» с ящиком в хвосте — общую вкладку, незнакомый хвост —
// первую вкладку. Документ называет себя новым адресом.
func TestMailOldAddressesStillOpen(t *testing.T) {
	s, _ := mailFix(t)
	first := mailBoxApp // первый ящик в Accounts()
	if accs := s.eng.Accounts(); accs[0].Name != first {
		t.Fatalf("первый ящик %q, ждали %q", accs[0].Name, first)
	}
	for old, want := range map[string]string{
		"settings:mail":              "settings:mail",
		"settings:mail~boxes":        "settings:mail",
		"settings:mail~выдумка":      "settings:mail",
		"settings:mail~spam":         "settings:mail~spam",
		"settings:mail~common":       "settings:mail~common",
		"settings:mail@проба~spam":   "settings:mail~spam",
		"settings:mail@проба~common": "settings:mail~common",
		// вкладки ящика без ящика: первый ящик, как было
		"settings:mail~work":    "settings:mail@проба",
		"settings:mail~conn":    "settings:mail@проба~conn",
		"settings:mail~send":    "settings:mail@проба~send",
		"settings:mail~folders": "settings:mail@проба~folders",
		"settings:mail~keep":    "settings:mail@проба~keep",
		// ящик и прежний хвост первой вкладки
		"settings:mail@проба":                "settings:mail@проба",
		"settings:mail@проба~work":           "settings:mail@проба",
		"settings:mail@проба~folders":        "settings:mail@проба~folders",
		"settings:mail@проба~выдумка":        "settings:mail@проба",
		"settings:mail@gmail@пример.рф~conn": "settings:mail@gmail@пример.рф~conn",
		"settings:mail@new":                  "settings:mail@new",
		"settings:mail@new~conn":             "settings:mail@new",
	} {
		doc, err := s.buildScreen(old, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", old, err)
		}
		if doc.Screen != want {
			t.Errorf("%s: открылся %q, ждали %q", old, doc.Screen, want)
		}
		if !strings.HasPrefix(want, "settings:mail@new") && doc.ActiveSubtab != want {
			t.Errorf("%s: выбрана вкладка %q, ждали %q", old, doc.ActiveSubtab, want)
		}
	}
	// прежний переход из закреплённой полосы остался действием
	got, err := s.execute(actionRequest{Action: "goto_mailbox", Target: "folders", Params: map[string]string{"value": mailBoxGoog}})
	if err != nil || got != "settings:mail@gmail@пример.рф~folders" {
		t.Errorf("goto_mailbox вернул %q (%v)", got, err)
	}
	got, err = s.execute(actionRequest{Action: "goto_mailbox", Target: "work", Params: map[string]string{"value": mailBoxGoog}})
	if err != nil || got != "settings:mail@gmail@пример.рф" {
		t.Errorf("goto_mailbox с прежним хвостом вернул %q (%v)", got, err)
	}
	// ящика нет — объяснение, а не пустота и не ошибка
	doc, err := s.buildScreen("settings:mail@нет-такого", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dumpScreen(doc), "Ящика «нет-такого» нет") || doc.Components[0].Type != "crumb" {
		t.Errorf("про отсутствующий ящик не сказано:\n%s", clipTest(dumpScreen(doc)))
	}
	// ящиков нет вовсе — «Новый ящик» на любом адресе, без пути назад в пустоту
	s2, st2 := testServer(t)
	if err := st2.SaveAccounts(nil); err != nil {
		t.Fatal(err)
	}
	for _, screen := range []string{"settings:mail", "settings:mail~spam", "settings:mail~conn", "settings:mail@проба"} {
		doc, err := s2.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s без ящиков: %v", screen, err)
		}
		if doc.Heading != "Новый ящик" {
			t.Errorf("%s без ящиков: заголовок %q", screen, doc.Heading)
		}
		for _, c := range doc.Components {
			if c.Type == "crumb" {
				t.Errorf("%s без ящиков: «‹ Почта» ведёт в пустоту", screen)
			}
		}
	}
}

// Адрес вкладки — это и есть адрес: собранный экран называет себя им, а
// вкладка в полосе ведёт на него же.
func TestMailTabsAreAddressable(t *testing.T) {
	s, _ := mailFix(t)
	for _, name := range mailBoxNames {
		doc := serverDoc(t, s, entityAddr(mailScreen, name, ""))
		for _, tab := range doc.Subtabs {
			again := serverDoc(t, s, tab.ID)
			if again.Screen != tab.ID || again.ActiveSubtab != tab.ID {
				t.Errorf("вкладка %q ведёт на %q и выбирает %q", tab.ID, again.Screen, again.ActiveSubtab)
			}
		}
	}
}

// Переключатель ящика в шапке — перейти к соседу, оставшись на той же вкладке.
func TestMailEntitySwitchKeepsTab(t *testing.T) {
	s, _ := mailFix(t)
	for _, tab := range mailBoxTabsAll {
		doc := serverDoc(t, s, entityAddr(mailScreen, mailBoxApp, tab))
		var sel *Select
		walkDoc(doc, func(c *Component) {
			if c.ID == "entity-"+mailBoxApp && len(c.Selects) == 1 {
				sel = &c.Selects[0]
			}
		})
		if sel == nil || sel.Label != "Ящик" || sel.Value != mailBoxApp || len(sel.Options) != 3 {
			t.Fatalf("%q: переключатель ящика %+v", tab, sel)
		}
		got, err := s.execute(actionRequest{Action: sel.Action.Action, Target: sel.Action.Target,
			Params: map[string]string{"value": mailBoxGoog}})
		want := entityAddr(mailScreen, mailBoxGoog, tab)
		if err != nil || got != want {
			t.Errorf("с вкладки %q переключатель ведёт на %q (%v), ждали %q", tab, got, err, want)
		}
		if _, err := s.buildScreen(got, "", "", 0); err != nil {
			t.Errorf("%s: %v", got, err)
		}
	}
}

// Адреса почтовых экранов строит entityAddr; склейка руками — это адрес без
// вкладки, который забудут обновить.
func TestNoHandWrittenMailAddressesInCode(t *testing.T) {
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
			for _, bad := range []string{`"settings:mail@" +`, `"settings:mail@"+`, `"settings:mail~" +`, `"settings:mail~"+`,
				`mailScreen + "@" +`, `mailScreen+"@"+`, `mailScreen + "~" +`, `mailScreen+"~"+`} {
				if strings.Contains(string(raw), bad) {
					found = append(found, path+": "+bad)
				}
			}
			return nil
		})
	}
	if len(found) > 0 {
		t.Errorf("адреса экранов почты склеиваются руками: %v", found)
	}
}

// ---- ничего не потерялось ----

// beforeMail — имена полей и действий «Почты» на 2 октября 2026, до разреза на
// вкладки раздела и ящика (снято с кода по всем способам входа и режимам).
// Строка отсюда пропала — значит настройка или действие стали недостижимыми.
// Действие с «!»; `goto_mailbox` из списка вынут: его место занял
// entity_goto, а само действие осталось (TestMailOldAddressesStillOpen).
var beforeMail = []string{
	"!$goto", "!add_account", "!connect_mailbox", "!keep_folders", "!remove_account", "!rescan_attachments",
	"!save_automation", "!save_mail_conn", "!save_mail_modes", "!save_mail_params", "!save_mail_send",
	"!save_mail_targets", "!save_purge_other", "!skip_folders", "!toggle_folder", "!clean_inbox", "!toggle_mail_task",
	"archive", "auth", "client_id", "email", "fetch", "host", "junk", "keep_days", "mode", "money_guard", "money_words",
	"outbound", "password", "port", "preset", "purge_other", "purpose", "retention", "secret", "send_auto_to",
	"send_mode", "server_folders", "server_votes", "smtp_host", "smtp_port", "smtp_secret", "smtp_starttls",
	"smtp_user", "tenant", "threshold", "username", "whitelist", "сбор:attention",
}

// Все четыре способа входа и оба режима: поля подключения и папок зависят от
// них, и каждое должно найтись.
func TestMailNothingLostAgainstBefore(t *testing.T) {
	s, _ := mailFix(t)
	has := map[string]bool{}
	for _, auth := range []string{store.AuthGoogle, store.AuthAppPassword, store.AuthIMAP, store.AuthExchange} {
		for _, readonly := range []bool{false, true} {
			c := s.eng.MailConf(mailBoxApp)
			c.Auth, c.Mode = auth, store.ModeTidy
			if readonly {
				c.Mode = store.ModeReadOnly
			}
			if err := s.eng.SaveMailConf(mailBoxApp, c); err != nil {
				t.Fatal(err)
			}
			for name := range screenSettings(t, s, mailAllScreens()...) {
				has[name] = true
			}
		}
	}
	// «Можно подключить» приезжает по «Показать ещё»: тумблер задачи — там
	more, err := s.buildScreen(entityAddr(mailScreen, mailBoxApp, ""), "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	walkDoc(more, func(c *Component) {
		for _, a := range c.Actions {
			has["!"+a.Action] = true
		}
	})
	checkSet(t, "Почта", has, beforeMail)
}

// Общее для всех ящиков не стоит на экране ящика, а настройки ящика — на
// вкладках раздела: чей параметр, видно по месту. Поля с одним именем у
// ящика и у всех («keep_days», «purge_other») различаются формами.
func TestMailCommonAndBoxSettingsDoNotMix(t *testing.T) {
	s, _ := mailFix(t)
	common := []string{"server_folders", "send_mode", "send_auto_to", "retention", "whitelist", "threshold",
		"money_guard", "money_words", "server_votes"}
	boxOnly := []string{"purpose", "mode", "outbound", "fetch", "auth", "client_id", "secret", "tenant", "username",
		"password", "host", "port", "smtp_host", "smtp_port", "smtp_starttls", "smtp_user", "smtp_secret", "archive",
		"junk", "сбор:attention"}
	commonForms := []string{"общее-ящики", "общее-ответы", "общее-хранение", "опасное-хранение", "спам-кого", "спам-исключения"}
	for _, screen := range mailAllScreens() {
		a := parseMailAddr(screen)
		has := screenSettings(t, s, screen)
		forms := map[string]bool{}
		var collect func(cs []Component)
		collect = func(cs []Component) {
			for _, c := range cs {
				if c.Type == "form" {
					forms[strings.TrimSuffix(c.ID, "-rare")] = true
				}
				collect(c.Children)
			}
		}
		doc := serverDoc(t, s, screen)
		collect(doc.Components)
		if doc.rare != nil {
			collect(doc.rare)
		}
		switch {
		case a.Box != "" && a.Box != mailNew: // экран ящика
			for _, name := range common {
				if has[name] {
					t.Errorf("%s: общая настройка %q стоит на экране ящика", screen, name)
				}
			}
			for _, id := range commonForms {
				if forms[id] {
					t.Errorf("%s: общая форма %q стоит на экране ящика", screen, id)
				}
			}
			// «keep_days» и «purge_other» у ящика свои — на «Хранении»
			if (has["keep_days"] || has["purge_other"]) && a.Tab != mailTabKeep {
				t.Errorf("%s: срок или стирание ящика вне вкладки «Хранение»", screen)
			}
		case a.Box == "": // вкладки раздела
			for _, name := range boxOnly {
				if has[name] {
					t.Errorf("%s: настройка ящика %q стоит на вкладке раздела", screen, name)
				}
			}
			for name := range forms {
				if strings.HasPrefix(name, "mail") {
					t.Errorf("%s: форма ящика %q стоит на вкладке раздела", screen, name)
				}
			}
		}
	}
}

// ---- что на экране ----

// Строка списка тонкая: не больше двух капсул, одна кнопка, «···», вся строка
// ведёт на экран ящика; капсулы показывают только то, что отличает.
func TestMailListRowIsThin(t *testing.T) {
	s, _ := mailFix(t)
	doc := serverDoc(t, s, mailScreen)
	rows := map[string]Component{}
	walkDoc(doc, func(c *Component) {
		if strings.HasPrefix(c.ID, "mailbox-") {
			rows[strings.TrimPrefix(c.ID, "mailbox-")] = *c
		}
	})
	if len(rows) != 3 {
		t.Fatalf("строк ящиков %d, ждали 3", len(rows))
	}
	for name, row := range rows {
		goto_, buttons := "", 0
		for _, a := range row.Actions {
			switch {
			case a.Action == "$goto":
				goto_ = a.Target
			case a.Label == "toggle":
			default:
				buttons++
			}
		}
		if goto_ != entityAddr(mailScreen, name, "") {
			t.Errorf("%s: строка ведёт на %q", name, goto_)
		}
		if buttons != 1 {
			t.Errorf("%s: кнопок %d, а нужна одна", name, buttons)
		}
		if len(row.Badges) > 2 {
			t.Errorf("%s: капсул %d", name, len(row.Badges))
		}
		if len(row.Menu) == 0 {
			t.Errorf("%s: у строки нет «···»", name)
		}
		for _, b := range row.Badges {
			if b.Text == "личный" || b.Text == "читает и наводит порядок" {
				t.Errorf("%s: капсула %q стоит у каждой строки и ничего не отличает", name, b.Text)
			}
		}
	}
	// у рабочего ящика, который не подключается и только читает, — две самые
	// важные капсулы: беда и режим
	pills := []string{}
	for _, b := range rows[mailBoxWork].Badges {
		pills = append(pills, b.Text)
	}
	if strings.Join(pills, "|") != "не подключается|только читает" {
		t.Errorf("капсулы рабочего ящика: %v", pills)
	}
	if rows[mailBoxWork].Level != LevelAlert {
		t.Errorf("беда не красит строку: %q", rows[mailBoxWork].Level)
	}
	// Gmail с архивом-ярлыком: «папок зря» — капсула, а не молчание
	found := false
	for _, b := range rows[mailBoxGoog].Badges {
		if strings.Contains(b.Text, "зря") {
			found = true
		}
	}
	if !found {
		t.Errorf("у Gmail разбираются ярлыки зря, а капсулы нет: %+v", rows[mailBoxGoog].Badges)
	}
	// отключить с экрана можно только ящик, заведённый с экрана, и с вопросом
	for name, row := range rows {
		for _, m := range row.Menu {
			if m.Action == "remove_account" && (m.Confirm == "" || m.ConfirmOK == "" || m.Style != "destructive") {
				t.Errorf("%s: «Отключить ящик» без вопроса: %+v", name, m)
			}
		}
	}
	// полка «Ждут решения» — то, что без человека не поедет
	shelf := ""
	walkDoc(doc, func(c *Component) {
		if c.Type == "shelf" {
			shelf = c.Title + ": " + dumpScreen(&Document{Components: c.Children})
		}
	})
	if !strings.Contains(shelf, "Ждут решения") || !strings.Contains(shelf, "2 письма похожи на спам") || !strings.Contains(shelf, "Разобрать →") {
		t.Errorf("полка ожидающих писем: %s", shelf)
	}
}

// «Опасное» — последним, своей карточкой, и у каждой кнопки вопрос. Форма
// внутри «Опасного» (стирание обычных писем) сохраняется своим действием.
func TestMailDangerLastAndConfirmed(t *testing.T) {
	s, _ := mailFix(t)
	withDanger := 0
	for _, screen := range mailAllScreens() {
		doc := serverDoc(t, s, screen)
		var dangers []int
		for i, c := range doc.Components {
			if c.Type == "section" && c.Style == "danger" {
				dangers = append(dangers, i)
			}
		}
		if len(dangers) == 0 {
			continue
		}
		withDanger++
		if len(dangers) != 1 || dangers[0] != len(doc.Components)-1 {
			t.Errorf("%s: «Опасное» не единственное и не последнее: %v из %d", screen, dangers, len(doc.Components))
			continue
		}
		danger := doc.Components[dangers[0]]
		buttons := 0
		for i := range danger.Children {
			walkthrough(&danger.Children[i], func(c *Component) {
				for _, a := range c.Actions {
					buttons++
					if a.Confirm == "" || a.ConfirmOK == "" || a.Style != "destructive" {
						t.Errorf("%s: кнопка «%s» в «Опасном» без вопроса или не опасная: %+v", screen, a.Label, a)
					}
				}
			})
		}
		forms := screenForms(danger.Children)
		if buttons == 0 && len(forms) == 0 {
			t.Errorf("%s: в «Опасном» нечего нажать", screen)
		}
		for _, f := range forms {
			if f.Submit == nil || !f.Deferred {
				t.Errorf("%s: форма %q в «Опасном» без действия или не под полосой", screen, f.ID)
			}
		}
	}
	// «Подключение» каждого ящика, «Спам» (письма на перенос) и «Общее»
	if withDanger < 5 {
		t.Errorf("«Опасное» нашлось на %d экранах из ожидаемых пяти", withDanger)
	}
	// ящик из файла запуска с экрана не отключается — и кнопки для этого нет
	doc := serverDoc(t, s, entityAddr(mailScreen, mailBoxApp, mailTabConn))
	has := screenSettings(t, s, entityAddr(mailScreen, mailBoxApp, mailTabConn))
	if !has["!remove_account"] || !has["!rescan_attachments"] {
		t.Errorf("в «Опасном» ящика нет «Отключить» и «Перечитать вложения»: %v", has)
	}
	_ = doc
}

// Режим «только читать»: задачи, невозможные в нём, — серые строки без
// тумблера; включить их нельзя, пока режим не сменён.
func TestMailReadOnlyTasksStayGrey(t *testing.T) {
	s, _ := mailFix(t)
	doc := serverDoc(t, s, entityAddr(mailScreen, mailBoxWork, ""))
	grey := map[string]bool{}
	walkDoc(doc, func(c *Component) {
		if c.Type != "row" || !strings.HasPrefix(c.ID, "mailtask-") {
			return
		}
		for _, a := range c.Actions {
			if a.Action == "toggle_mail_task" {
				// «Своя задача» — обычная, её подключают; а встроенные нет
				if strings.Contains(c.Subtitle, "Очистка спама") || strings.Contains(c.Subtitle, "Папки") {
					t.Errorf("задача «%s» невозможна в «только читает», а тумблер есть", c.Subtitle)
				}
			}
		}
		if c.Style == "secondary" && c.Toggle == "" {
			grey[c.Subtitle] = true
		}
	})
	if len(grey) == 0 {
		t.Fatalf("в «только читает» нет серых строк задач — встроенные «Папки» и «Очистка спама» должны быть серыми")
	}
	// а у ящика, который читает и наводит порядок, те же задачи обычные
	doc = serverDoc(t, s, entityAddr(mailScreen, mailBoxApp, ""))
	walkDoc(doc, func(c *Component) {
		if c.Type == "row" && strings.HasPrefix(c.ID, "mailtask-") && c.Style == "secondary" && c.Toggle == "" &&
			strings.Contains(c.Caption, "только читает") {
			t.Errorf("у ящика, который наводит порядок, задача «%s» помечена «не работает»", c.Subtitle)
		}
	})
}

// Шапка ящика: плитка и адрес, одна строка фактов с назначением и режимом,
// «···» с проверкой подключения, и ни одной видимой кнопки — главная живёт на
// вкладке «Подключение». Не подключается — шапка красная.
func TestMailEntityHeader(t *testing.T) {
	s, _ := mailFix(t)
	doc := serverDoc(t, s, entityAddr(mailScreen, mailBoxWork, ""))
	var head *Component
	walkDoc(doc, func(c *Component) {
		if c.ID == "entity-"+mailBoxWork {
			head = c
		}
	})
	if head == nil {
		t.Fatal("шапки нет")
	}
	if head.Subtitle != mailBoxWork || head.AvatarColor == "" || head.Avatar == "" {
		t.Errorf("плитка и адрес: %+v", head)
	}
	for _, want := range []string{"Пароль приложения", "рабочий", "только читает"} {
		if !strings.Contains(head.Caption, want) {
			t.Errorf("в строке фактов нет «%s»: %q", want, head.Caption)
		}
	}
	if head.Level != LevelAlert || len(head.Badges) != 1 || head.Badges[0].Text != "не подключается" {
		t.Errorf("беда не видна в шапке: %+v %+v", head.Level, head.Badges)
	}
	if len(head.Actions) != 0 || len(head.Menu) < 2 {
		t.Errorf("кнопок %d, «···» %d", len(head.Actions), len(head.Menu))
	}
	// счётчики вкладок: «!» у «Подключения» и число папок зря у «Папок»
	tabs := map[string]Filter{}
	for _, tab := range doc.Subtabs {
		tabs[tab.Title] = tab
	}
	if tabs["Подключение"].Badge != "!" || tabs["Подключение"].Level != LevelAlert {
		t.Errorf("у «Подключения» нет «!»: %+v", tabs["Подключение"])
	}
	doc = serverDoc(t, s, entityAddr(mailScreen, mailBoxApp, ""))
	tabs = map[string]Filter{}
	for _, tab := range doc.Subtabs {
		tabs[tab.Title] = tab
	}
	if tabs["Папки"].Badge != "3" || tabs["Папки"].Level != LevelWarn {
		t.Errorf("у «Папок» нет числа папок зря: %+v", tabs["Папки"])
	}
	if tabs["Подключение"].Badge != "" {
		t.Errorf("у подключённого ящика на «Подключении» счётчик %q", tabs["Подключение"].Badge)
	}
}

// «Новый ящик»: обязательное строками, хост — под «Ещё 1 полем», та же форма
// стоит в мастере первого запуска; «‹ Почта», пока ящики есть.
func TestMailNewBoxScreen(t *testing.T) {
	s, _ := mailFix(t)
	doc := serverDoc(t, s, entityAddr(mailScreen, mailNew, ""))
	if doc.Components[0].Type != "crumb" {
		t.Error("нет «‹ Почта»")
	}
	forms := screenForms(doc.Components)
	if len(forms) != 1 || forms[0].ID != "addaccount" || forms[0].Style != formRows || forms[0].Submit.Action != "add_account" || forms[0].Deferred {
		t.Fatalf("форма: %+v", forms)
	}
	more := 0
	names := []string{}
	for _, f := range forms[0].Fields {
		names = append(names, f.Name)
		if f.More {
			more++
			if f.Name != "host" {
				t.Errorf("под «Ещё» ушло %q", f.Name)
			}
		}
	}
	if strings.Join(names, ",") != "purpose,preset,email,password,host" || more != 1 {
		t.Errorf("поля %v, редких %d", names, more)
	}
	// пароль закрытый: не обычное текстовое поле
	for _, f := range forms[0].Fields {
		if f.Name == "password" && f.Type != "secure" {
			t.Errorf("пароль — поле %q", f.Type)
		}
	}
	// мастер первого запуска берёт ту же форму
	var inWizard []Component
	newMailbox(func(c Component) { inWizard = append(inWizard, c) })
	if len(screenForms(inWizard)) != 1 {
		t.Error("мастер потерял форму «Новый ящик»")
	}
	// «~» в адресе оборвал бы адрес экрана ящика: отказ до входа на сервер
	if _, err := s.execute(actionRequest{Action: "add_account",
		Params: map[string]string{"email": "а~б@пример.рф", "password": "x", "preset": "Яндекс"}}); err == nil {
		t.Error("ящик с «~» в адресе принят")
	}
}

// Редкое «Спама» — «Тонкая настройка» вкладки, а не строки среди ежедневного.
func TestMailSpamFineSettings(t *testing.T) {
	s, _ := mailFix(t)
	screen := entityAddr(mailScreen, "", mailTabSpam)
	doc := serverDoc(t, s, screen)
	fold := rareFoldOf(t, s, screen)
	if !strings.HasPrefix(fold.Caption, "2 параметра") || fold.Meta != "всё по умолчанию" {
		t.Errorf("«Тонкая настройка»: %q · %q", fold.Caption, fold.Meta)
	}
	onScreen := screenSettings(t, s, screen)
	for _, name := range []string{"whitelist", "threshold", "money_guard", "server_votes", "money_words"} {
		if !onScreen[name] {
			t.Errorf("на вкладке нет настройки %q", name)
		}
	}
	plain := rareFieldsOf(doc.Components)
	for _, name := range []string{"server_votes", "money_words"} {
		if _, dup := plain[name]; dup {
			t.Errorf("редкое %q осталось среди ежедневного", name)
		}
	}
	// «Опасное» — после «Тонкой настройки», последним
	if last := doc.Components[len(doc.Components)-1]; last.Style != "danger" {
		t.Errorf("последним стоит %q, а не «Опасное»", last.ID)
	}
}

// ---- сохранение ----

// Каждая форма вкладок ящика и раздела сохраняется так, как её сохраняет
// нижняя полоса, и движок видит новое значение.
func TestMailEveryFormSavesAndEngineSees(t *testing.T) {
	s, st := mailFix(t)
	box := func(tab string) string { return entityAddr(mailScreen, mailBoxApp, tab) }
	section := func(tab string) string { return entityAddr(mailScreen, "", tab) }

	// Обработка: назначение, режим, признак сбора, «наружу», глубина «Входящих»
	mailSave(t, s, box(""), "mailmodes-"+mailBoxApp, map[string]string{
		"purpose": "рабочий", "mode": "только читает", "сбор:attention": "false", "outbound": "false", "fetch": "30 дней"})
	c := s.eng.MailConf(mailBoxApp)
	if !c.IsWork() || !c.ReadOnly() || c.OutboundOn() || c.AttentionOn() || c.FetchDays != 30 {
		t.Errorf("обработка: %+v", c)
	}
	if !s.eng.MailboxReadOnly(mailBoxApp) || s.eng.ClosedAccounts(true)[mailBoxApp] != true {
		t.Error("движок не видит режима и закрытости ящика")
	}
	// верно и обратно
	mailSave(t, s, box(""), "mailmodes-"+mailBoxApp, map[string]string{
		"purpose": "личный", "mode": "читает и наводит порядок", "сбор:attention": "true", "outbound": "true"})
	if c = s.eng.MailConf(mailBoxApp); c.IsWork() || c.ReadOnly() || !c.OutboundOn() || !c.AttentionOn() {
		t.Errorf("обработка обратно: %+v", c)
	}

	// Подключение: поля входа; пустой секрет — «оставить прежний»
	mailSave(t, s, box(mailTabConn), "mailconn-"+mailBoxApp, map[string]string{"username": "новый@пример.рф", "host": "imap.новый.рф"})
	if c = s.eng.MailConf(mailBoxApp); c.Username != "новый@пример.рф" || c.Host != "imap.новый.рф" || c.Password != mailSecretApp {
		t.Errorf("подключение: логин %q, хост %q, пароль сохранён %v", c.Username, c.Host, c.Password == mailSecretApp)
	}
	mailSave(t, s, box(mailTabConn), "mailconn-"+mailBoxApp, map[string]string{"password": "НОВЫЙ-ПАРОЛЬ-123"})
	if c = s.eng.MailConf(mailBoxApp); c.Password != "НОВЫЙ-ПАРОЛЬ-123" {
		t.Errorf("новый пароль не сохранился")
	}
	g := entityAddr(mailScreen, mailBoxGoog, mailTabConn)
	mailSave(t, s, g, "mailconn-"+mailBoxGoog, map[string]string{"client_id": "другой-клиент"})
	if c = s.eng.MailConf(mailBoxGoog); c.ClientID != "другой-клиент" || c.Secret != mailSecretOAuth {
		t.Errorf("Google: client_id %q, секрет остался %v", c.ClientID, c.Secret == mailSecretOAuth)
	}

	// Отправка
	mailSave(t, s, box(mailTabSend), "mailsend-"+mailBoxApp, map[string]string{
		"smtp_host": "smtp.другой.рф", "smtp_port": "465", "smtp_starttls": "false", "smtp_user": "отправитель"})
	if c = s.eng.MailConf(mailBoxApp); c.SMTPHost != "smtp.другой.рф" || c.SMTPPort != 465 || c.SMTPStartTLS || c.SMTPUser != "отправитель" || c.SMTPSecret != "mail:проба-smtp" {
		t.Errorf("отправка: %+v", c)
	}

	// Папки: куда складывать
	mailSave(t, s, box(mailTabFolders), "mailtargets-"+mailBoxApp, map[string]string{"archive": "Работа", "junk": "Archive"})
	if c = s.eng.MailConf(mailBoxApp); c.Archive != "Работа" || c.Junk != "Archive" {
		t.Errorf("папки: архив %q, спам %q", c.Archive, c.Junk)
	}

	// Хранение: своё у ящика; общее число «как у всех» от соседнего поля не
	// превращается в своё
	if err := s.eng.SetRetentionDays(15); err != nil {
		t.Fatal(err)
	}
	mailSave(t, s, box(mailTabKeep), "mailstorage-"+mailBoxApp, map[string]string{"purge_other": "да"})
	if c = s.eng.MailConf(mailBoxApp); c.JunkDays != 0 || c.PurgeOther == nil || !*c.PurgeOther {
		t.Errorf("«как у всех» стало своим из-за соседнего поля: срок спама %d, стирание %v", c.JunkDays, c.PurgeOther)
	}
	mailSave(t, s, box(mailTabKeep), "mailstorage-"+mailBoxApp, map[string]string{"junk": "20", "keep_days": "90"})
	if c = s.eng.MailConf(mailBoxApp); c.JunkDays != 20 || c.KeepDays != 90 || s.eng.SpamTTLForAccount(mailBoxApp) != 20 || s.eng.RestTTLForAccount(mailBoxApp) != 90 {
		t.Errorf("хранение: %+v", c)
	}
	if s.eng.SpamTTLForAccount(mailBoxGoog) != 15 {
		t.Errorf("чужой ящик пошёл за правкой: срок %d", s.eng.SpamTTLForAccount(mailBoxGoog))
	}

	// Спам
	// порог писем у провайдера и слова счёта — «Тонкая настройка»: свои формы-близнецы
	mailSave(t, s, section(mailTabSpam), "спам-кого", map[string]string{"whitelist": "true", "threshold": "85"})
	mailSave(t, s, section(mailTabSpam), "спам-кого-rare", map[string]string{"server_votes": "5"})
	if !s.eng.WhitelistOnly() || s.eng.AutoThreshold() != 0.85 || s.eng.ServerSpamVotes() != 5 {
		t.Errorf("спам: белый список %v, порог %v, голосов %d", s.eng.WhitelistOnly(), s.eng.AutoThreshold(), s.eng.ServerSpamVotes())
	}
	mailSave(t, s, section(mailTabSpam), "спам-кого", map[string]string{"whitelist": "false", "threshold": "90"})
	mailSave(t, s, section(mailTabSpam), "спам-кого-rare", map[string]string{"server_votes": "0"})
	if s.eng.WhitelistOnly() || s.eng.ServerSpamVotes() != 0 {
		t.Errorf("спам обратно: белый список %v, голосов %d", s.eng.WhitelistOnly(), s.eng.ServerSpamVotes())
	}
	mailSave(t, s, section(mailTabSpam), "спам-исключения", map[string]string{"money_guard": "false"})
	mailSave(t, s, section(mailTabSpam), "спам-исключения-rare", map[string]string{"money_words": "Счёт, Invoice"})
	if s.eng.MoneyGuard() || strings.Join(s.eng.MoneyWords(), ",") != "счёт,invoice" {
		t.Errorf("исключения: %v %v", s.eng.MoneyGuard(), s.eng.MoneyWords())
	}

	// Общее
	mailSave(t, s, section(mailTabCommon), "общее-ящики", map[string]string{"server_folders": "false"})
	if s.eng.ReadServerFolders() {
		t.Error("«заходить в спам и корзину сервера» не выключилось")
	}
	mailSave(t, s, section(mailTabCommon), "общее-ответы", map[string]string{"send_mode": "сам", "send_auto_to": "@пример.рф, друг@пример.рф"})
	if s.eng.SendMode() != engine.ModeSelf || strings.Join(s.eng.SendAutoTo(), ",") != "@пример.рф,друг@пример.рф" {
		t.Errorf("ответы: режим %q, кому %v", s.eng.SendMode(), s.eng.SendAutoTo())
	}
	mailSave(t, s, section(mailTabCommon), "общее-ответы", map[string]string{"send_mode": "по кнопке"})
	if s.eng.SendMode() != engine.ModeButton {
		t.Errorf("режим «по кнопке»: %q", s.eng.SendMode())
	}
	// прежнее слово «сама» из закладок и скриптов тоже принимается
	if _, err := s.execute(actionRequest{Action: "save_automation", Params: map[string]string{"send_mode": "сама"}}); err != nil || s.eng.SendMode() != engine.ModeSelf {
		t.Errorf("прежнее «сама»: %q (%v)", s.eng.SendMode(), err)
	}
	mailSave(t, s, section(mailTabCommon), "общее-хранение", map[string]string{"retention": "21"})
	if s.eng.SpamTTL() != 21 {
		t.Errorf("общий срок спама: %d", s.eng.SpamTTL())
	}
	mailSave(t, s, section(mailTabCommon), "опасное-хранение", map[string]string{"purge_other": "true", "keep_days": "60"})
	if !s.eng.PurgeRest() || s.eng.RestTTL() != 60 {
		t.Errorf("стирание обычных писем: %v, срок %d", s.eng.PurgeRest(), s.eng.RestTTL())
	}
	// а порог писем от провайдера принимает 0..50 и отказывает по-человечески
	if _, err := s.execute(actionRequest{Action: "save_automation", Params: map[string]string{"server_votes": "99"}}); err == nil {
		t.Error("порог 99 принят")
	}
	if _, err := s.execute(actionRequest{Action: "save_automation", Params: map[string]string{"server_votes": "много"}}); err == nil {
		t.Error("порог «много» принят")
	}
	if _, err := s.execute(actionRequest{Action: "save_automation", Params: map[string]string{"server_votes": ""}}); err != nil {
		t.Errorf("пустой порог — «не трогали», а он отказ: %v", err)
	}
	_ = st
}

// Правки соседних форм не гасят друг друга: каждая шлёт только свои поля.
func TestMailFormsDoNotMuteEachOther(t *testing.T) {
	s, _ := mailFix(t)
	if err := s.eng.SetWhitelistOnly(true); err != nil {
		t.Fatal(err)
	}
	if err := s.eng.SetServerSpamVotes(7); err != nil {
		t.Fatal(err)
	}
	mailSave(t, s, entityAddr(mailScreen, "", mailTabSpam), "спам-исключения", map[string]string{"money_guard": "false"})
	mailSave(t, s, entityAddr(mailScreen, "", mailTabCommon), "общее-хранение", map[string]string{"retention": "40"})
	if !s.eng.WhitelistOnly() || s.eng.ServerSpamVotes() != 7 {
		t.Errorf("правка соседней формы сбросила настройку: белый список %v, голосов %d", s.eng.WhitelistOnly(), s.eng.ServerSpamVotes())
	}
}

// ---- секреты ----

// Пароли приложения, секреты OAuth, пароль SMTP и токены в разметку не
// попадают никогда: ни в описание экрана, ни в разметку страницы, ни в
// «Тонкую настройку». Показывается только «задан» или «не задан».
func TestMailSecretsNeverInMarkup(t *testing.T) {
	s, _ := mailFix(t)
	checked := 0
	for _, screen := range append(mailAllScreens(), "settings:mail~work", "settings:mail~conn", "settings:secrets", "settings") {
		doc := serverDoc(t, s, screen)
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatalf("%s: разметка: %v", screen, err)
		}
		whole := dumpWithRare(t, s, screen, doc) + "\n" + string(markup)
		for _, secret := range mailSecrets {
			if strings.Contains(whole, secret) {
				t.Errorf("%s: секрет %q попал в разметку", screen, secret)
			}
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("проверено экранов %d", checked)
	}
	// и «задан» сказано: человек видит, что секрет есть, а не пустоту
	doc := serverDoc(t, s, entityAddr(mailScreen, mailBoxApp, mailTabConn))
	if !strings.Contains(dumpScreen(doc), "задан · введите новый, чтобы заменить") {
		t.Error("про пароль приложения не сказано, что он задан")
	}
	// закрытое поле не несёт значения
	walkDoc(doc, func(c *Component) {
		for _, f := range c.Fields {
			if f.Type == "secure" && f.Value != "" {
				t.Errorf("закрытое поле %q несёт значение", f.Name)
			}
		}
	})
}
