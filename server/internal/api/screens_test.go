package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Сквозная проверка описаний экранов: на пустой базе (первый запуск) каждый
// экран должен собираться без ошибки и разворачиваться в JSON. Клиент тонкий:
// паника или битый документ здесь — это чёрное окно у человека.

func testServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	old, had := os.LookupEnv("SEMA_KEY")
	_ = os.Setenv("SEMA_KEY", "ключ-для-тестов")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEMA_KEY", old)
			return
		}
		_ = os.Unsetenv("SEMA_KEY")
	})
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// хранилище файлов — во временную папку. Без этого storage.NewLocal
	// получает пустой путь и сыплет блобы прямо в каталог пакета: в
	// репозитории так завелись «24/22/…», «8f/8c/…», «e8/f1/…». То же
	// самое уже случалось в engine — там это чинили 10 сентября 2026
	conf := &config.Config{Data: dir, KeyEnv: "SEMA_KEY"}
	conf.Files.Dir = filepath.Join(dir, "files")
	eng, err := engine.New(conf, st)
	if err != nil {
		t.Fatalf("движок: %v", err)
	}
	// первый запуск уводит на мастер настройки, поэтому заводим ящик и
	// модель: проверяем обычные экраны, а не мастер
	if err := st.SaveAccounts([]*store.Account{{Name: "проба", Email: "проба@пример.рф",
		Host: "imap.пример.рф", Port: 993}}); err != nil {
		t.Fatalf("ящик: %v", err)
	}
	if err := eng.AddModel(engine.ModelConf{Name: "тестовая", Type: "openai",
		BaseURL: "http://127.0.0.1:1/v1", Model: "test"}); err != nil {
		t.Fatalf("модель: %v", err)
	}
	// тестовый сервер изображает работающего Сёму, обновлённого с прежней
	// версии, а не свежую установку: своя сеть пускается без входа — те
	// значения, что разовая миграция записывает базе с данными
	// (store.settleAuthDefaults). Свежую установку проверяет freshServer
	for key, value := range map[string]string{auth.SettingLocalOpen: "true", auth.SettingLocalScope: store.ScopePrivate} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatalf("умолчания входа: %v", err)
		}
	}
	return New(eng), st
}

// Экраны ядра: список тот же, что в боковом меню, плюс вложенные разделы.
var coreScreens = []string{
	"inbox", "notifications", "files", "ask", "tasks", "scripts", "modules",
	"health", "memory", "memory~areas", "memory:graph", "memory:sessions", "memory:log",
	"settings", "settings:mail", "settings:models", "settings:tools",
	// внутренние вкладки «Почты» и «Моделей» — такие же экраны; у ящика свой
	// экран с пятью вкладками (ящик «проба» заводит testServer), прежние адреса
	// вкладок без ящика открываются каноническими адресами
	"settings:mail~spam", "settings:mail~common", "settings:mail@new",
	"settings:mail@проба", "settings:mail@проба~conn", "settings:mail@проба~send",
	"settings:mail@проба~folders", "settings:mail@проба~keep",
	"settings:models~list", "settings:models~providers", "settings:models~issues",
	// у сущностей раздела свои экраны: подключение, модель, провайдер на вкладках
	// (модель «тестовая» и «на этой машине» заводит сам testServer)
	"settings:models@new", "settings:models@тестовая", "settings:models@provider:на этой машине",
	"settings:models@provider:OpenAI", "settings:models@provider:OpenAI~prices",
	"settings:secrets", "settings:servers", "settings:access", "categories",
	"digest:groups", "skills", "queue", "sent", "roles",
	"settings:search", "settings:learning", "settings:skills", "settings:integrations", "settings:telegram", "settings:agents",
	"settings:changes", "settings:watch",
	// внутренние вкладки остальных разделов — тоже экраны: каждый должен
	// собираться сам по себе, по адресу
	"settings:access~tokens", "settings:access~network", "settings:access~log",
	"sent~log", "settings:notify", "settings:notify~rules", "settings:notify~show",
	// «Что ушло наружу» живёт в каркасе настроек (2 октября 2026): раздел, лента и
	// одна отправка — свои адреса; прежние «sent», «sent~log», «sent~<номер>»
	// открывают то же (TestOldSentAddressesOpenSettingsSection). «Работа Сёмы» —
	// три вкладки
	"settings:sent", "settings:sent~log",
	"settings:watch~screen", "settings:watch~chats",
	"settings:telegram~answer", "settings:telegram~conn",
	"settings:servers~auto", "settings:servers~log", "settings:servers@new",
	// у сервера MCP свой экран; «Добавить» и несуществующий сервер собираются и без серверов
	"settings:integrations@new", "settings:integrations@нет-такого",
	"settings:storages", "settings:storages~sources",
	"settings:tools~codes",
	// «Хранилища», «Скиллы», «Уведомления» (2 октября 2026): у хранилища и скилла
	// свои экраны, «Добавить …» — тоже; нет такого — объяснение, а не ошибка
	"settings:storages@local", "settings:storages@new", "settings:storages@new~yadisk", "settings:storages@нет-такого",
	"settings:skills@linux-admin", "settings:skills@new", "settings:skills@нет-такого", "settings:notify@new",
	// «Ключи и секреты» и «Агенты» (2 октября 2026): вкладки раздела, добавление,
	// приглашение, экраны несуществующих; «Шифрование» живёт здесь, а не на
	// «Инструментах» (прежний адрес открывается переадресацией — её держит
	// TestEncryptionMovedFromTools)
	"settings:secrets~crypt", "settings:secrets~log", "settings:secrets@new", "settings:secrets@нет-такого", "settings:secrets@нет-такого~log",
	"settings:agents~gateway", "settings:agents@new", "settings:agents@нет-такого", "settings:agents@нет-такого~caps",
}

func TestAllScreensBuildOnEmptyDatabase(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range coreScreens {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Errorf("экран %q: %v", screen, err)
			continue
		}
		if doc == nil {
			t.Errorf("экран %q: пустое описание", screen)
			continue
		}
		// вложенный раздел подсвечивает свой пункт меню: «Что ушло» —
		// «Здоровье», справочник дайджеста — «Почту»
		switch {
		// внутренняя вкладка («memory~areas») — тот же пункт меню
		case doc.Active == screen, strings.HasPrefix(screen, doc.Active+":"), strings.HasPrefix(screen, doc.Active+"~"):
		case screen == "queue" && doc.Active == "health",
			// «Что ушло наружу» — раздел настроек со своей лентой и отправками;
			// прежние адреса («sent», «sent~log») ведут в него
			screen == "sent" && doc.Active == "settings",
			screen == "sent~log" && doc.Active == "settings",
			screen == "settings:sent" && doc.Active == "settings",
			screen == "settings:sent~log" && doc.Active == "settings",
			// категории переехали в модуль «Чеки»: старый адрес ведёт к нему
			screen == "categories" && doc.Active == "modules",
			screen == "digest:groups" && doc.Active == "inbox":
		default:
			t.Errorf("экран %q: активным помечен %q", screen, doc.Active)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Errorf("экран %q не разворачивается в JSON: %v", screen, err)
			continue
		}
		if strings.Contains(string(raw), `"components":null`) {
			t.Errorf("экран %q отдаёт null вместо пустого списка компонентов", screen)
		}
	}
}

// Неизвестный экран не роняет сервер: человек мог остаться на экране модуля,
// который выключили.
func TestUnknownScreenIsHandled(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("module:которого-нет:главный", "", "", 0)
	if err == nil && doc == nil {
		t.Fatal("ни описания, ни ошибки")
	}
}

// Фильтры экранов не ломают сборку.
func TestScreenFilters(t *testing.T) {
	s, _ := testServer(t)
	cases := map[string][]string{
		"sent":          {"", "outside", store.ClassSensitive, store.ClassSecret},
		"notifications": {"", "open", "done"},
		"inbox":         {"", "pending", "spam"},
	}
	for screen, filters := range cases {
		for _, f := range filters {
			if _, err := s.buildScreen(screen, f, "", 0); err != nil {
				t.Errorf("экран %q, отбор %q: %v", screen, f, err)
			}
		}
	}
}

// Запрос описания по HTTP с этой же машины проходит без входа, а ответ —
// корректный JSON с активным экраном.
func TestUIOverHTTP(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8765/api/ui?screen=health", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ответ %d: %s", rec.Code, rec.Body.String())
	}
	var doc Document
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	if doc.Active != "health" {
		t.Errorf("активный экран %q", doc.Active)
	}
	if len(doc.Screens) == 0 {
		t.Error("в ответе нет списка экранов")
	}
}

// Ярус ответа агента вынимается из карточки и становится капсулой.
func TestSplitTier(t *testing.T) {
	cases := []struct{ in, tier, rest string }{
		{"[ярус lsp]\nfunc A — 12", "lsp", "func A — 12"},
		{"[ярус text] поиск по тексту\nx.go:3", "text", "поиск по тексту\nx.go:3"},
		{"обычный ответ", "", "обычный ответ"},
		{"[ярус", "", "[ярус"},
	}
	for _, c := range cases {
		tier, rest := splitTier(c.in)
		if tier != c.tier || rest != c.rest {
			t.Errorf("splitTier(%q) = %q, %q; хотели %q, %q", c.in, tier, rest, c.tier, c.rest)
		}
	}
	if tierBadge("tags").Text == tierBadge("lsp").Text {
		t.Error("приблизительный ярус не отличается от точного")
	}
}

// В строке остаётся одно действие, разрушительное — только в меню, а
// главные («Выполнить» и «Отклонить») остаются оба: без второго решение
// не принять.
func TestTidyRow(t *testing.T) {
	row := Component{Type: "row", Actions: []Action{
		{Action: "toggle_model", Label: "toggle"},
		{Action: "check_model", Label: "Проверить"},
		{Action: "set_model_key", Label: "Ключ"},
		{Action: "remove_model", Label: "Удалить", Style: "destructive"},
	}}
	tidyRow(&row)
	if len(row.Actions) != 2 || row.Actions[1].Label != "Проверить" {
		t.Errorf("на виду осталось не одно действие: %+v", row.Actions)
	}
	if len(row.Menu) != 2 {
		t.Fatalf("в меню ушло не всё лишнее: %+v", row.Menu)
	}
	for _, a := range row.Actions {
		if a.Style == "destructive" {
			t.Error("разрушительное действие осталось в строке")
		}
	}

	pending := Component{Type: "row", Actions: []Action{
		{Action: "approve_ssh", Label: "Выполнить", Style: "primary"},
		{Action: "reject_ssh", Label: "Отклонить", Style: "primary"},
		{Action: "$goto", Label: "Открыть чат"},
	}}
	tidyRow(&pending)
	if len(pending.Actions) != 3 {
		t.Errorf("решение человека урезано: %+v", pending.Actions)
	}

	plain := Component{Type: "row", Actions: []Action{{Action: "$goto", Label: "Открыть"}}}
	tidyRow(&plain)
	if len(plain.Actions) != 1 || len(plain.Menu) != 0 {
		t.Errorf("единственное действие тронуто: %+v / %+v", plain.Actions, plain.Menu)
	}
}

// Свёртка держит открытым первый раздел экрана, а не первый длинный:
// иначе самый длинный список оставался лентой.
func TestCollapseLongKeepsFirstSection(t *testing.T) {
	rows := func(n int) []Component {
		out := make([]Component, n)
		for i := range out {
			out[i] = Component{Type: "row"}
		}
		return out
	}
	doc := []Component{
		{Type: "text", Text: "пояснение"},
		{Type: "section", Title: "Короткий", Children: rows(3)},
		{Type: "section", Title: "Длинный", Children: rows(30)},
		{Type: "section", Title: "Очень длинный", Children: rows(52)},
	}
	collapseLong(doc, 20)
	if doc[1].Collapsed {
		t.Error("короткий первый раздел свернули")
	}
	if !doc[2].Collapsed || !doc[3].Collapsed {
		t.Errorf("длинные разделы не свернулись: %v / %v", doc[2].Collapsed, doc[3].Collapsed)
	}

	// единственный раздел — главный список экрана, его не сворачиваем
	one := []Component{{Type: "section", Title: "Файлы", Children: rows(50)}}
	collapseLong(one, 20)
	if one[0].Collapsed {
		t.Error("главный список экрана свернули — экран остался пустым")
	}
}

// У открытой записи всегда есть видимое действие «Прочитано»: кнопки
// перехода клиент не рисует, и без него у записи «к сведению» оставалось
// одно «Разобраться», хотя разбираться там не с чем.
func TestNotifyRowHasDone(t *testing.T) {
	s, st := testServer(t)
	id, err := st.PutNotification(&store.Notification{
		Key: "проба", Source: "core", Level: "info", Title: "Telegram · Проба",
		Summary: "Как дела?",
		Actions: []store.NotifyAction{{Label: "Открыть переписку", Kind: "goto", Ref: "chat:1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := st.Notification(id)
	if !ok {
		t.Fatal("запись не читается")
	}
	row := s.notifyRow(n, false)
	tidyRow(&row)
	var visible []string
	for _, a := range row.Actions {
		if a.Action != "$goto" { // клиент кнопки перехода не рисует
			visible = append(visible, a.Label)
		}
	}
	found := false
	for _, l := range visible {
		if l == "Прочитано" {
			found = true
		}
	}
	if !found {
		t.Fatalf("видимые действия: %v, среди них нет «Прочитано»", visible)
	}
}

// Оболочка интерфейса отдаётся самим сервером и без входа: рядом с
// бинарником файлов нет, а до сессии человек должен увидеть хотя бы
// страницу, которая уведёт его на вход.
func TestWebShellServed(t *testing.T) {
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, путь := range []string{"/", "/app.js", "/style.css", "/manifest.webmanifest", "/sw.js"} {
		resp, err := http.Get(srv.URL + путь)
		if err != nil {
			t.Fatalf("%s: %v", путь, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: ответ %d", путь, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Fatalf("%s: пусто", путь)
		}
	}
	// неизвестный адрес — это экран клиента, а не «не найдено»
	resp, err := http.Get(srv.URL + "/чего-то-нет")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("экран клиента отдался с ответом %d", resp.StatusCode)
	}
}

// Разметка для браузера собирается из того же документа, что уходит на
// мак. Проверяем все экраны разом: ошибка в шаблоне — это белая страница
// у человека, а не сообщение в журнале.
func TestWebRenderAllScreens(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range allScreens {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: сборка: %v", screen, err)
		}
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatalf("%s: разметка: %v", screen, err)
		}
		if len(markup) == 0 {
			t.Fatalf("%s: пустая разметка", screen)
		}
		if strings.Contains(string(markup), "<no value>") {
			t.Errorf("%s: в разметке остались незаполненные места", screen)
		}
	}
}

var allScreens = []string{
	"inbox", "senders", "ask", "files", "tasks", "scripts", "modules",
	"memory", "memory:graph", "health", "queue", "notify", "sent",
	"settings", "settings:mail", "settings:models", "settings:tools",
	"settings:mail@проба~folders", "settings:mail~spam", "settings:mail~common", "settings:models~list",
	"settings:models~providers", "settings:models~issues", "settings:models@new",
	"settings:telegram", "settings:max", "settings:servers", "settings:secrets",
	"settings:access", "settings:skills", "settings:learning", "settings:agents",
	"settings:search", "settings:notify", "settings:storages", "settings:integrations",
	"settings:integrations@new", "settings:changes", "settings:watch",
}

// Форма без кнопок и строка без действий не должны ронять разметку:
// такие компоненты сервер отдаёт (например, пустой мастер).
func TestWebRenderEdgeCases(t *testing.T) {
	empty := &Document{Heading: "Проба", Components: []Component{
		{Type: "form", Title: "Без кнопок", Fields: []Field{{Type: "text", Name: "a", Label: "А"}}},
		{Type: "row", Subtitle: "Строка без всего"},
		{Type: "section", Title: "Раздел", Children: []Component{{Type: "row", Subtitle: "внутри"}}},
		{Type: "bubble", Role: "peer", Text: "привет"},
		{Type: "cards", Children: []Component{{Type: "metric", Title: "Подпись", Text: "42"}}},
	}}
	markup, err := web.Render(toScreen(empty))
	if err != nil {
		t.Fatalf("разметка: %v", err)
	}
	for _, надо := range []string{"Без кнопок", "Строка без всего", "привет", "42"} {
		if !strings.Contains(string(markup), надо) {
			t.Errorf("в разметке нет %q", надо)
		}
	}
}

// Отказ в доступе имеет три вида, и путать их нельзя: приложение ждёт JSON,
// браузер при переходе — страницу входа, а htmx подменяет содержимое окна
// ответом любого кода, поэтому ему нужна разметка, а не строка JSON.
func TestDenyShapes(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.Store().SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	клиент := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	спросить := func(заголовки map[string]string) (*http.Response, string) {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/ui?screen=inbox", nil)
		if err != nil {
			t.Fatal(err)
		}
		for к, з := range заголовки {
			req.Header.Set(к, з)
		}
		resp, err := клиент.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	resp, body := спросить(nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"error"`) {
		t.Fatalf("приложению должен приходить JSON с 401, пришло %d %q", resp.StatusCode, body)
	}

	resp, _ = спросить(map[string]string{"Accept": "text/html,application/xhtml+xml"})
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Fatalf("браузер должен уходить на вход, пришло %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, body = спросить(map[string]string{"HX-Request": "true", "Accept": "*/*"})
	if strings.Contains(body, `"error"`) || !strings.Contains(body, "/login") {
		t.Fatalf("htmx должен получать разметку со ссылкой на вход, пришло %d %q", resp.StatusCode, body)
	}
}

// Две поломки, которые видны только в браузере, а не в тестах на сборку:
// JSON в hx-vals, экранированный как строка JS, ломал каждую кнопку молча;
// неизвестный компонент показывал служебную надпись вместо чисел.
func TestMarkupWithoutBreakingDetails(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range []string{"inbox", "health", "learning", "settings:models", "settings:notify", "chat:1", "files", "queue"} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			continue
		}
		html, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if strings.Contains(string(html), `{\"action`) {
			t.Fatalf("%s: hx-vals экранирован как строка JS — кнопки не отправят запрос", screen)
		}
		if strings.Contains(string(html), "неизвестный компонент") {
			t.Fatalf("%s: на экране неизвестный компонент", screen)
		}
	}
}

// Все потребители видны на «Памяти», даже когда есть записи, ждущие
// решения. Список строк одного раздела нельзя переиспользовать срезом:
// раздел уже отдан документу, и следующий список молча переписывает его
// строки — так с экрана пропали все потребители, кроме последнего, а
// вместе с ними и выпадашка «выдать…», без которой права не выдать.
func TestConsumersNotOverwrittenByPending(t *testing.T) {
	s, st := testServer(t)
	for _, путь := range []string{"/repo/один", "/repo/два", "/repo/три"} {
		if _, err := st.MemoryConsumer("mcp", "Claude Code", путь); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := st.SaveMemory(&store.Memory{Kind: store.MemFact, Area: "личное",
			Text: "ждёт решения " + string(rune('а'+i)), Status: store.MemPending}); err != nil {
			t.Fatal(err)
		}
	}
	// потребители живут на вкладке «Области и доступ» ленивым разделом
	// (perf-pass 5): на экране — его заголовок, строки — частью по раскрытию
	doc, err := s.buildScreen("memory~areas", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	ленивый := false
	for _, c := range doc.Components {
		if c.Type == "section" && strings.HasPrefix(c.Title, "Потребители") && c.Lazy == "memory-consumers" {
			ленивый = true
		}
	}
	if !ленивый {
		t.Fatal("раздела «Потребители» на вкладке нет")
	}
	потребители, err := s.buildPart("memory~areas", "memory-consumers", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(потребители) < 3 {
		t.Fatalf("в разделе «Потребители» строк %d, ждали не меньше трёх", len(потребители))
	}
	for _, путь := range []string{"/repo/один", "/repo/два", "/repo/три"} {
		нашли := false
		for _, r := range потребители {
			if strings.Contains(r.Subtitle, путь) {
				нашли = true
			}
		}
		if !нашли {
			t.Errorf("потребителя %s на экране нет", путь)
		}
	}
}

// Показатель словом набирается прозой, а не моноширинным в 32 точки:
// «пусто» и «3 мин назад» расползались на две строки и кричали громче
// настоящих цифр. Единица отделяется от числа только когда она одна.
func TestMetricByNumberAndWord(t *testing.T) {
	случаи := []struct {
		значение, число, единица string
		прозой                   bool
	}{
		{"72 °C", "72", "°C", false},
		{"119 МБ", "119", "МБ", false},
		{"пусто", "пусто", "", true},
		{"3 мин назад", "3 мин назад", "", true},
		{"0 в очереди", "0 в очереди", "", true},
		{"1 249", "1 249", "", false},
	}
	for _, с := range случаи {
		число, единица := valueUnit(с.значение)
		if число != с.число || единица != с.единица {
			t.Errorf("%q → %q + %q, ждали %q + %q", с.значение, число, единица, с.число, с.единица)
		}
		if startsWithDigit(число) == с.прозой {
			t.Errorf("%q: прозой=%v, а начало числом=%v", с.значение, с.прозой, startsWithDigit(число))
		}
	}
}

// Матрица выдачи показывает всех потребителей и все области сразу, и у
// каждой клетки своё действие: селект «выдать…» внутри строки агента
// давал 54 отдельных действия и никакой картины.
func TestGrantMatrix(t *testing.T) {
	s, st := testServer(t)
	for _, путь := range []string{"/repo/один", "/repo/два"} {
		if _, err := st.MemoryConsumer("mcp", "Claude Code", путь); err != nil {
			t.Fatal(err)
		}
	}
	consumers, err := st.MemoryConsumers()
	if err != nil {
		t.Fatal(err)
	}
	areas, err := st.MemoryAreas()
	if err != nil {
		t.Fatal(err)
	}
	m := s.grantMatrix(consumers, areas)
	if len(m.Columns) != len(areas) {
		t.Fatalf("колонок %d, областей %d", len(m.Columns), len(areas))
	}
	// чат Сёмы прав не спрашивает: он и есть Сёма
	for _, r := range m.Children {
		if strings.Contains(r.Subtitle, "Сёма") && r.Caption == "" {
			t.Errorf("чат Сёмы попал в матрицу: %q", r.Subtitle)
		}
		if len(r.Cells) != len(areas) {
			t.Fatalf("у строки %q клеток %d, а областей %d", r.Subtitle, len(r.Cells), len(areas))
		}
		for _, кл := range r.Cells {
			if кл.State == "" || кл.Action.Action == "" {
				t.Errorf("клетка без состояния или действия: %+v", кл)
			}
		}
	}
	if len(m.Children) < 2 {
		t.Fatalf("в матрице %d строк, ждали обоих потребителей", len(m.Children))
	}
}

// Почта, файлы и чеки — ядро на Go, а не модуль: в списке «Модули» им
// не место. Раньше туда подмешивалась строка-заглушка «Почта, файлы и
// чеки» с капсулой «встроенный», хотя модуля «Чеки» давно нет.
func TestModulesListHasNoCoreStub(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("modules", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	list := findSectionByID(doc.Components, "modules-list")
	if list == nil {
		t.Fatal("на экране «Модули» нет раздела modules-list")
	}
	if len(list.Children) == 0 {
		t.Fatal("в списке модулей пусто: ждали хотя бы встроенный модуль «Сёма»")
	}
	for i := range list.Children {
		walkthrough(&list.Children[i], func(c *Component) {
			if strings.Contains(c.Subtitle, "Почта, файлы и чеки") || strings.Contains(c.Caption, "первая способность") {
				t.Errorf("ядро выдано за модуль: %q · %q", c.Subtitle, c.Caption)
			}
		})
	}
}
