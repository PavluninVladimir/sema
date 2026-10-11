package api

// Поиск по настройкам: находит настройку с путём и текущим значением, а не раздел;
// сущности — сверху; ё и регистр не мешают; гостю не показывает вход и доступ.

import (
	"net/http"
	"strings"
	"testing"
)

// resultRows — строки результатов поиска по настройкам (настройки и сущности).
func resultRows(doc *Document) (settings, entities []Component) {
	var walk func(cs []Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			switch {
			case strings.HasPrefix(c.ID, "найдено-"):
				settings = append(settings, c)
			case strings.HasPrefix(c.ID, "сущность-"):
				entities = append(entities, c)
			}
			walk(c.Children)
		}
	}
	walk(doc.Components)
	return settings, entities
}

func findResult(rows []Component, title, pathPart string) *Component {
	for i := range rows {
		if rows[i].Subtitle == title && strings.Contains(rows[i].Title, pathPart) {
			return &rows[i]
		}
	}
	return nil
}

func searchDoc(t *testing.T, s *Server, screen, q string) *Document {
	t.Helper()
	doc, err := s.buildScreen(screen, "", q, 0)
	if err != nil {
		t.Fatalf("поиск %q на %s: %v", q, screen, err)
	}
	return doc
}

// «окно», «sudo», «пуш», «тихие часы», «цена», имя сервера и адрес ящика находят
// ожидаемые строки с верным путём, значением и переходом к строке.
func TestSearchFindsSettingsWithPaths(t *testing.T) {
	s, _ := indexFix(t, 173)
	cases := []struct {
		query, title, path, goTo, value string
	}{
		{"окно", "Окно", "Система → Серверы → srv-vpn → Самостоятельность", "settings:servers@srv-vpn~auto^f:host-auto-window", "02:00–06:00"},
		{"sudo", "Команды с sudo", "Система → Серверы → srv-vpn → Обзор", "settings:servers@srv-vpn^r:host-sudo", "включено"},
		{"пуш", "Хранить пуш для спящего устройства", "Каналы → Уведомления → Каналы доставки", "settings:notify^f:push-settings-ttl_min", "60 минут"},
		{"тихие часы", "Тихие часы", "Chrome на маке", "settings:notify^f:channel-form-", "23:00–8:00"},
		{"цена", "Адрес страницы или JSON с ценами", "Поведение → Модели и роли → OpenAI → Цены", "settings:models@provider:OpenAI~prices^f:psrc-OpenAI-url", "developers.openai.com"},
		{"опрос провод", "Опрос экрана без провода", "Система → Работа Сёмы → Экран и связь", "settings:watch~screen^f:work-live-poll_s", "4 с"},
	}
	for _, c := range cases {
		for _, screen := range []string{"settings", "settings:servers"} {
			settings, _ := resultRows(searchDoc(t, s, screen, c.query))
			row := findResult(settings, c.title, c.path)
			if row == nil {
				t.Errorf("%q на %s: нет строки «%s» с путём «…%s»", c.query, screen, c.title, c.path)
				continue
			}
			if len(row.Actions) != 1 || !strings.HasPrefix(row.Actions[0].Target, strings.Split(c.goTo, "^")[0]) ||
				!strings.Contains(row.Actions[0].Target, "^"+strings.SplitN(c.goTo, "^", 2)[1]) {
				t.Errorf("%q: переход %v, ждали %q", c.query, row.Actions, c.goTo)
			}
			if !strings.Contains(row.Caption, c.value) {
				t.Errorf("%q: значение в строке %q, ждали «%s»", c.query, row.Caption, c.value)
			}
		}
	}
	// «Тонкая настройка» помечена в результате: клиент раскроет её сам
	settings, _ := resultRows(searchDoc(t, s, "settings", "пуш"))
	if r := findResult(settings, "Хранить пуш для спящего устройства", ""); r == nil || len(r.Badges) == 0 || r.Badges[0].Text != "тонкая настройка" {
		t.Errorf("строка из «Тонкой настройки» без пометки: %+v", r)
	}
}

// Имя сервера и адрес ящика находят сущность — сверху, отдельным блоком — и её
// настройки с путём через эту сущность.
func TestSearchFindsEntitiesAndTheirSettings(t *testing.T) {
	s, _ := indexFix(t, 173)
	for _, c := range []struct{ query, title, path, goTo string }{
		{"srv-vpn", "srv-vpn", "Система → Серверы", "settings:servers@srv-vpn"},
		{"owner", "owner@example.com", "Каналы → Почта", "settings:mail@owner@example.com"},
		{"gpt-6-luna", "gpt-6-luna", "Поведение → Модели и роли", "settings:models@gpt-6-luna"},
		{"planner", "planner", "Система → Интеграции", "settings:integrations@planner"},
	} {
		doc := searchDoc(t, s, "settings", c.query)
		_, entities := resultRows(doc)
		row := findResult(entities, c.title, c.path)
		if row == nil || len(row.Actions) != 1 || row.Actions[0].Target != c.goTo {
			t.Errorf("%q: сущности %+v", c.query, entities)
		}
		// блок сущностей стоит над настройками
		var order []string
		for _, b := range doc.Components {
			order = append(order, b.Title)
		}
		if len(order) == 0 || !strings.HasPrefix(order[0], "Нашлось · разделы и сущности") {
			t.Errorf("%q: сверху %v, ждали «разделы и сущности»", c.query, order)
		}
	}
	// настройки сервера находятся по его имени вместе с вкладкой
	settings, _ := resultRows(searchDoc(t, s, "settings", "srv-vpn окно"))
	if len(settings) == 0 || findResult(settings, "Окно", "srv-vpn → Самостоятельность") == nil {
		t.Errorf("«srv-vpn окно»: %+v", settings)
	}
	for _, r := range settings {
		if !strings.Contains(r.Title, "srv-vpn") {
			t.Errorf("«srv-vpn окно» нашло чужую строку: %s → %s", r.Title, r.Subtitle)
		}
	}
	// ящик: настройки лежат на его экране, адрес ящика входит в путь
	settings, _ = resultRows(searchDoc(t, s, "settings", "owner@example.com обработка"))
	if len(settings) == 0 || !strings.Contains(settings[0].Title, "Каналы → Почта → owner@example.com → Обработка") {
		t.Errorf("настройки ящика: %+v", settings)
	}
}

// Регистр и «ё» вместо «е» поиску не мешают: «Счёт», «счет», «СЧЁТ» — один запрос.
func TestSearchIgnoresCaseAndYo(t *testing.T) {
	s, _ := indexFix(t, 20)
	want := ""
	for _, q := range []string{"счёт", "счет", "СЧЁТ", "Счет", "СчЁт"} {
		settings, _ := resultRows(searchDoc(t, s, "settings", q))
		if len(settings) == 0 {
			t.Fatalf("«%s» ничего не нашёл", q)
		}
		var got []string
		for _, r := range settings {
			got = append(got, r.Title+"|"+r.Subtitle)
		}
		joined := strings.Join(got, "\n")
		if want == "" {
			want = joined
		} else if joined != want {
			t.Errorf("«%s» нашёл другое, чем «счёт»", q)
		}
	}
	a, _ := resultRows(searchDoc(t, s, "settings", "ПУШ"))
	b, _ := resultRows(searchDoc(t, s, "settings", "пуш"))
	if len(a) == 0 || len(a) != len(b) {
		t.Errorf("«ПУШ» и «пуш»: %d и %d строк", len(a), len(b))
	}
	// у экранов-разделов то же: слова раздела («ключи», «Ключи») тоже без регистра
	for _, q := range []string{"ключи", "КЛЮЧИ"} {
		if !containsScreen(searchDoc(t, s, "settings", q), secretsScreen) {
			t.Errorf("«%s» не нашёл раздел «Ключи и секреты»", q)
		}
	}
}

// Одинаковая настройка у каждой сущности («цена» у ста семидесяти трёх моделей)
// показывается несколько раз и честно говорит, сколько таких же не показано.
func TestSearchGroupsSameSettingOfManyEntities(t *testing.T) {
	s, _ := indexFix(t, 173)
	settings, _ := resultRows(searchDoc(t, s, "settings", "цена"))
	if len(settings) > 80 {
		t.Errorf("«цена» показала %d строк: повторы моделей не свёрнуты", len(settings))
	}
	grouped, perModel := 0, map[string]int{}
	for _, r := range settings {
		if len(r.Badges) > 0 && strings.HasPrefix(r.Badges[0].Text, "такая же у ещё ") {
			grouped++
		}
		if strings.Contains(r.Title, "Модели и роли") && !strings.Contains(r.Title, "Цены") {
			perModel[r.Subtitle]++
		}
	}
	if grouped == 0 {
		t.Error("ни у одной строки нет пометки «такая же у ещё N»")
	}
	for title, n := range perModel {
		if n > maxSameSetting {
			t.Errorf("настройка «%s» показана %d раз, предел %d", title, n, maxSameSetting)
		}
	}
	// уточнение по имени возвращает нужную модель
	exact, _ := resultRows(searchDoc(t, s, "settings", "цена gpt-6-luna"))
	if len(exact) == 0 {
		t.Fatal("«цена gpt-6-luna» ничего не нашла")
	}
	for _, r := range exact {
		if !strings.Contains(r.Title, "gpt-6-luna") {
			t.Errorf("«цена gpt-6-luna» нашла чужую модель: %s", r.Title)
		}
	}
}

// Пустой результат объясняет себя; на «Обзоре» и в корне телефона поиск один и тот же.
func TestSearchEmptyAndPhoneRoot(t *testing.T) {
	s, _ := indexFix(t, 20)
	doc := searchDoc(t, s, "settings", "квазимодо")
	if len(doc.Components) != 1 || doc.Components[0].Style != "empty" {
		t.Fatalf("пустой поиск: %d блоков", len(doc.Components))
	}
	phone, err := s.buildScreenForPhone("settings", "", "окно", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	pSettings, _ := resultRows(phone)
	dSettings, _ := resultRows(searchDoc(t, s, "settings", "окно"))
	if len(pSettings) == 0 || len(pSettings) != len(dSettings) {
		t.Errorf("поиск в корне телефона: %d строк, на широком экране %d", len(pSettings), len(dSettings))
	}
	// на разделе результаты стоят над содержимым раздела
	sec := searchDoc(t, s, "settings:servers", "окно")
	if len(sec.Components) < 2 || !strings.HasPrefix(sec.Components[0].Title, "Нашлось · ") {
		t.Errorf("на разделе результаты не сверху: %q", sec.Components[0].Title)
	}
}

// «Показать ещё»: порция настроек ограничена, остальные приезжают кнопкой.
func TestSearchPagesSettings(t *testing.T) {
	s, _ := indexFix(t, 20)
	doc, err := s.buildScreen("settings", "", "уведомления", 10)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := resultRows(doc)
	if len(settings) != 10 {
		t.Fatalf("порция в 10 строк, получили %d", len(settings))
	}
	hasMore := false
	for _, c := range doc.Components {
		if c.Type == "button" && len(c.Actions) == 1 && c.Actions[0].Action == "$more" {
			hasMore = true
		}
	}
	if !hasMore {
		t.Error("нет кнопки «Показать ещё»")
	}
}

// Гость — токен без права «всё» — настроек входа и доступа в поиске не видит; хозяин
// видит. Пока обработчик не передал `viewerOpts`, сборка отдавала бы их всем.
func TestSearchHidesAccessFromGuests(t *testing.T) {
	s, st := indexFix(t, 20)
	guest, err := st.AddToken("читатель", []string{"ui"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := st.AddToken("хозяин", []string{"ui", "actions", "all"})
	if err != nil {
		t.Fatal(err)
	}
	// токену с правом «экраны» открыт JSON-путь, браузерный — только хозяину
	const label = "Неудач подряд до запирания" // настройка входа, «Тонкая настройка» «Входа и доступа»
	for _, path := range []string{"/api/ui?screen=settings&search=до+запирания", "/api/ui?screen=settings:servers&search=до+запирания"} {
		w := doRequest(s, "GET", path, "", "203.0.113.10:1234", guest)
		if w.Code != http.StatusOK {
			t.Fatalf("%s гостю: %d %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), label) {
			t.Errorf("%s: гость увидел настройку входа", path)
		}
	}
	for _, path := range []string{"/api/ui?screen=settings&search=до+запирания", "/ui?screen=settings&search=до+запирания",
		"/ui?screen=settings:servers&search=до+запирания"} {
		w := doRequest(s, "GET", path, "", "203.0.113.10:1234", owner)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), label) {
			t.Errorf("%s: хозяин не нашёл настройку входа (%d)", path, w.Code)
		}
	}
	// раздел «Вход и доступ» гостю не находится и по слову: строка результата, а не
	// колонка разделов (она стоит рядом всегда)
	const sectionRow = `"id":"set-settings:access"`
	w := doRequest(s, "GET", "/api/ui?screen=settings&search=пароль", "", "203.0.113.10:1234", guest)
	if strings.Contains(w.Body.String(), sectionRow) {
		t.Error("гость нашёл раздел «Вход и доступ» по слову")
	}
	if w := doRequest(s, "GET", "/api/ui?screen=settings&search=пароль", "", "203.0.113.10:1234", owner); !strings.Contains(w.Body.String(), sectionRow) {
		t.Error("хозяин не нашёл раздел «Вход и доступ» по слову: проверка гостя пуста")
	}
	// и сам экран входа гостю закрыт, как и был
	if w := doRequest(s, "GET", "/ui?screen=settings:access", "", "203.0.113.10:1234", guest); w.Code != http.StatusForbidden {
		t.Errorf("экран доступа гостю: %d", w.Code)
	}
}

// Сам по себе запрос через обработчик: метка подсветки едет в адресе перехода и не
// остаётся в адресе экрана.
func TestHighlightTravelsInAddressAndStaysOutOfScreen(t *testing.T) {
	s, _ := indexFix(t, 20)
	target := withHighlight("settings:servers@srv-vpn~auto", hlField("host-auto", "window"))
	if target != "settings:servers@srv-vpn~auto^f:host-auto-window" {
		t.Fatalf("адрес перехода %q", target)
	}
	addr, mark := splitHighlight(target)
	if addr != "settings:servers@srv-vpn~auto" || mark != "f:host-auto-window" {
		t.Errorf("разбор: %q и %q", addr, mark)
	}
	// хвост, не похожий на метку, частью адреса остаётся
	if a, m := splitHighlight("settings:mail@a^b@пример.рф"); a != "settings:mail@a^b@пример.рф" || m != "" {
		t.Errorf("«^» внутри адреса принят за метку: %q %q", a, m)
	}
	doc, err := s.buildScreen(target, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Screen != "settings:servers@srv-vpn~auto" || doc.Highlight != "f:host-auto-window" {
		t.Errorf("документ: экран %q, метка %q", doc.Screen, doc.Highlight)
	}
	markup := stnMarkup(t, doc)
	if !strings.Contains(markup, `data-highlight="f:host-auto-window"`) || !strings.Contains(markup, `data-screen="settings:servers@srv-vpn~auto"`) {
		t.Error("в разметке нет метки подсветки или адрес экрана не чистый")
	}
	// без метки — и в документе, и в разметке её нет: перечитывание опросом не подсвечивает
	plain, _ := s.buildScreen("settings:servers@srv-vpn~auto", "", "", 0)
	if plain.Highlight != "" || strings.Contains(stnMarkup(t, plain), "data-highlight") {
		t.Error("метка подсветки осталась у экрана, открытого без неё")
	}
	// через HTTP: тот же путь, что у клика по результату
	w := doRequest(s, "GET", "/ui?screen="+strings.ReplaceAll(target, "^", "%5E"), "", "127.0.0.1:1234", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `data-highlight="f:host-auto-window"`) {
		t.Errorf("/ui: %d, метка в разметке есть: %v", w.Code, strings.Contains(w.Body.String(), "data-highlight"))
	}
	// неизвестная метка не роняет экран: клиент просто ничего не найдёт
	if _, err := s.buildScreen("settings:servers^f:нет-такого", "", "", 0); err != nil {
		t.Errorf("экран с несуществующей меткой: %v", err)
	}
	// у каждого вида метки своя приставка и свой элемент в разметке
	for _, mark := range []string{hlField("form", "поле"), hlRow("строка"), hlSection("раздел")} {
		if _, m := splitHighlight(withHighlight("settings:servers", mark)); m != mark {
			t.Errorf("метка %q потерялась в адресе", mark)
		}
	}
	if hlRow("") != "" || hlSection("") != "" {
		t.Error("у компонента без ID метка есть")
	}
}

// Запрос с поиском отдаёт верхнее поле «Найти настройку» и находит настройку по
// записи реестра, не зная раздела: ключ — тоже слово поиска.
func TestSearchByRegistryKey(t *testing.T) {
	s, st := indexFix(t, 20)
	settings, _ := resultRows(searchDoc(t, s, "settings", "ui.poll_s"))
	if len(settings) == 0 || settings[0].Subtitle != "Опрос экрана без провода" {
		t.Fatalf("по ключу: %+v", settings)
	}
	// значение в результате — живое: поменяли, и поиск показывает новое сразу, без перестройки индекса
	if err := st.SetSetting("ui.poll_s", "11"); err != nil {
		t.Fatal(err)
	}
	settings, _ = resultRows(searchDoc(t, s, "settings", "ui.poll_s"))
	if !strings.Contains(settings[0].Caption, "11 с") {
		t.Errorf("значение в результате устарело: %q", settings[0].Caption)
	}
}
