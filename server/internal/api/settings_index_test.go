package api

// Индекс настроек: полнота, честность адресов, молчание про секреты, «ничего не
// делает наружу и ничего не пишет», цена сборки.

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// indexScreenMarkup — разметка экрана записи так, как она уходит в браузер: для
// строки из «Тонкой настройки» — тело раздела, которое приезжает по раскрытию.
// Результат запоминается: сотни записей лежат на десятках экранов.
func indexScreenMarkup(t *testing.T, s *Server, e *indexEntry, cache map[string]string) string {
	t.Helper()
	key := fmt.Sprintf("%s|%v", e.Addr, e.Fine)
	if m, ok := cache[key]; ok {
		return m
	}
	var markup string
	if e.Fine {
		blocks, err := s.buildPart(e.Addr, rareID, "", 0)
		if err != nil {
			t.Fatalf("«Тонкая настройка» экрана %s: %v", e.Addr, err)
		}
		var bl []web.Block
		for _, c := range blocks {
			bl = append(bl, toBlock(c))
		}
		m, err := web.RenderFragment(bl)
		if err != nil {
			t.Fatal(err)
		}
		markup = string(m)
	} else {
		doc, err := s.buildScreen(e.Addr, "", "", 0)
		if err != nil {
			t.Fatalf("экран %s: %v", e.Addr, err)
		}
		markup = stnMarkup(t, doc)
	}
	markup = html.UnescapeString(markup)
	cache[key] = markup
	return markup
}

// Каждая настройка реестра умолчаний есть в индексе, у неё есть путь, адрес и
// метка строки, а по адресу экран действительно открывается и несёт элемент с этой
// меткой — у строки из «Тонкой настройки» после раскрытия. Перебор по реестру, а не
// проверка нескольких: новая настройка без экрана не пройдёт незамеченной.
func TestIndexHasEverySettingOfRegistry(t *testing.T) {
	s, _ := indexFix(t, 40)
	idx := s.buildSettingsIndex()
	cache := map[string]string{}
	defs := store.SettingDefs()
	if len(defs) < 80 {
		t.Fatalf("в реестре %d настроек: реестр не заполнился", len(defs))
	}
	for _, def := range defs {
		entries := idx.byKey[def.Key]
		if len(entries) == 0 {
			t.Errorf("настройки %s нет в индексе: у неё нет строки ни на одном экране", def.Key)
			continue
		}
		for _, e := range entries {
			switch {
			case len(e.Path) < 2 || e.Addr == "" || e.Anchor == "" || e.Title == "":
				t.Errorf("%s: запись неполна: %+v", def.Key, e)
			case e.Default == nil:
				t.Errorf("%s: у записи нет умолчания", def.Key)
			case !strings.Contains(indexScreenMarkup(t, s, e, cache), `data-hl="`+e.Anchor+`"`):
				t.Errorf("%s: экран %s не несёт элемента %s (в «Тонкой настройке»: %v)", def.Key, e.Addr, e.Anchor, e.Fine)
			}
		}
	}
}

// Остальные записи — те же правила: у каждой настройки путь, подпись и адрес, по
// адресу экран открывается, а метка строки на нём есть. Записи без метки (строка
// без ID) ведут на экран, а не к строке — их немного, и счёт виден.
func TestIndexEntriesOpenOnTheirScreens(t *testing.T) {
	s, _ := indexFix(t, 40)
	idx := s.buildSettingsIndex()
	cache := map[string]string{}
	withoutAnchor, checked := 0, 0
	for _, e := range idx.Entries {
		if e.Addr == "" || e.Title == "" || len(e.Path) < 2 {
			t.Errorf("запись без адреса, подписи или пути: %+v", e)
			continue
		}
		if e.Kind != entrySetting {
			continue
		}
		if e.Anchor == "" {
			withoutAnchor++
			continue
		}
		checked++
		if !strings.Contains(indexScreenMarkup(t, s, e, cache), `data-hl="`+e.Anchor+`"`) {
			t.Errorf("экран %s не несёт элемента %s (%s)", e.Addr, e.Anchor, e)
		}
	}
	if checked < 500 {
		t.Errorf("проверено записей %d: индекс обошёл слишком мало", checked)
	}
	t.Logf("записей с меткой %d, без метки %d из %d", checked, withoutAnchor, len(idx.Entries))
}

// Сущности — настоящие, из данных: каждый сервер, ящик, провайдер, модель, сервер
// MCP, секрет, агент, хранилище, скилл есть в индексе со своим экраном.
func TestIndexListsRealEntities(t *testing.T) {
	s, _ := indexFix(t, 173)
	idx := s.buildSettingsIndex()
	has := map[string]string{}
	for _, e := range idx.Entries {
		if e.Kind == entryEntity {
			has[e.Title] = e.Addr
		}
	}
	for title, addr := range map[string]string{
		"srv-vpn": "settings:servers@srv-vpn", "srv-bots": "settings:servers@srv-bots",
		"owner@example.com": "settings:mail@owner@example.com", "проба": "settings:mail@проба",
		"gpt-6-luna": "settings:models@gpt-6-luna", "gpt-ряд-000": "settings:models@gpt-ряд-000",
		"OpenAI": "settings:models@provider:OpenAI", "planner": "settings:integrations@planner",
		"ssh:srv-vpn": "settings:secrets@ssh:srv-vpn", "мак": "settings:agents@мак", "yandex": "settings:storages@yandex",
		"Linux-сервер": "settings:skills@linux-admin",
	} {
		if has[title] != addr {
			t.Errorf("сущность %q: адрес %q, ждали %q", title, has[title], addr)
		}
	}
	if len(idx.byKey) < 80 {
		t.Errorf("настроек реестра в индексе %d", len(idx.byKey))
	}
}

// Значения секретов, токены, пароли, фразы и ключи провайдеров в индекс не попадают
// никогда — ни в запись, ни в результаты поиска. Поля вида secure индексируются
// подписью без значения.
func TestIndexNeverHoldsSecrets(t *testing.T) {
	s, _ := indexFix(t, 60)
	idx := s.buildSettingsIndex()
	all := fmt.Sprintf("%+v", idx.Entries)
	for _, v := range indexSentinels() {
		if strings.Contains(all, v) {
			t.Errorf("значение %q попало в индекс", v)
		}
	}
	secure := 0
	for _, e := range idx.Entries {
		if e.Control == "secure" {
			secure++
			if e.Value != "" || s.entryValue(e) != "" {
				t.Errorf("у закрытого поля %q есть значение %q", e.Title, e.Value)
			}
			if e.pinnable() {
				t.Errorf("закрытое поле %q можно вынести на Обзор", e.Title)
			}
		}
	}
	if secure == 0 {
		t.Log("закрытых полей на экранах нет: проверка про них пуста")
	}
	// результаты поиска — те же слова, по которым ищут секреты, и всё, что на них находится
	for _, q := range []string{"токен", "пароль", "ключ", "секрет", "фраза", "planner", "s3", "yandex", "OpenAI", "ssh", "oauth",
		"api", "hash", "service", "служебный"} {
		for _, screen := range []string{"settings", "settings:servers", "settings:secrets"} {
			doc, err := s.buildScreen(screen, "", q, 0)
			if err != nil {
				t.Fatalf("поиск %q на %s: %v", q, screen, err)
			}
			text := dumpScreen(doc) + stnMarkup(t, doc)
			for _, v := range indexSentinels() {
				if strings.Contains(text, v) {
					t.Errorf("поиск %q на %s показал значение %q", q, screen, v)
				}
			}
		}
	}
}

// failTransport — любой сетевой вызов роняет тест.
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("сетевой вызов из сборки индекса: %s %s", r.Method, r.URL)
	return nil, errors.New("сеть из индекса закрыта")
}

// countTransport — считает сетевые вызовы и отказывает на каждый.
type countTransport struct{ n *int }

func (c countTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*c.n++
	return nil, errors.New("сеть закрыта")
}

// Сборка индекса ничего не делает наружу и ничего не пишет: ни сети (любой вызов
// роняет тест), ни вопроса жителю машины, ни чтения значений секретов (журнал секретов
// не растёт), ни изменения базы (отпечаток каждой таблицы тот же).
func TestIndexBuildTouchesNothing(t *testing.T) {
	s, st := indexFix(t, 173)
	indexJournal(t, st)
	// провода агентов не опрашиваются: индексу про них спрашивать нечего
	s.linksVia = func() []string {
		t.Error("индекс спросил, кто из агентов на связи")
		return nil
	}
	// у эмбеддера задан сервер: экран «Роли» спрашивал бы у него список моделей по сети
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	// контроль: страж сети в самом деле видит такое обращение — иначе зелёный тест
	// ничего бы не доказывал. Адрес здесь свой, чтобы запомненный ответ не закрыл
	// то, что проверяется ниже
	var seen int
	http.DefaultTransport = countTransport{&seen}
	if err := st.SetSetting("embed_base_url", "http://embed-control.invalid:8080/v1"); err != nil {
		t.Fatal(err)
	}
	s.eng.EmbedModels()
	if seen == 0 {
		t.Fatal("страж сети не увидел обращения к серверу эмбеддера: проверка слепа")
	}
	if err := st.SetSetting("embed_base_url", "http://embed.invalid:8080/v1"); err != nil {
		t.Fatal(err)
	}
	http.DefaultTransport = failTransport{t}

	secretLogBefore := st.CountSecretLogFiltered("", store.SecretLogAll)
	before, err := st.TablesDigest()
	if err != nil {
		t.Fatal(err)
	}
	idx := s.buildSettingsIndex()
	// и всё, что дальше делают из индекса: поиск и имена
	s.searchBlocks(&Document{sections: s.allSections()}, "окно", 50)
	for _, chg := range must1(st.SettingsLogView(store.SettingsLogAll, 50)) {
		idx.nameOf(chg)
	}
	after, err := st.TablesDigest()
	if err != nil {
		t.Fatal(err)
	}
	for table, sum := range before {
		if after[table] != sum {
			t.Errorf("таблица %s изменилась: сборка индекса что-то записала", table)
		}
	}
	if len(after) != len(before) {
		t.Errorf("таблиц было %d, стало %d", len(before), len(after))
	}
	if n := st.CountSecretLogFiltered("", store.SecretLogAll); n != secretLogBefore {
		t.Errorf("журнал секретов вырос с %d до %d: индекс читал значения секретов", secretLogBefore, n)
	}
	if len(before) < 20 {
		t.Errorf("отпечатков таблиц %d: сравнивать было почти нечего", len(before))
	}
}

func must1(list []store.SettingChange, err error) []store.SettingChange {
	if err != nil {
		panic(err)
	}
	return list
}

// Сколько стоит сборка индекса на тестовых данных (сто семьдесят три модели, пять
// серверов, двадцать пять секретов). Число идёт в журнал теста, а предел щедрый:
// он ловит порядок величины — раньше экран модели строил карточки всех моделей
// заново, и сборка занимала четыреста миллисекунд вместо девяноста, — а не
// скорость этой машины. На Raspberry Pi будет в несколько раз медленнее.
func TestIndexBuildCost(t *testing.T) {
	s, _ := indexFix(t, 173)
	started := time.Now()
	idx := s.buildSettingsIndex()
	spent := time.Since(started)
	size := 0
	for _, e := range idx.Entries {
		size += len(e.Title) + len(e.Hint) + len(e.Words) + len(e.Value) + len(e.Addr) + len(e.Anchor) + len(e.norm) + len(e.normTitle) + len(e.normHint)
		for _, p := range e.Path {
			size += len(p)
		}
	}
	t.Logf("сборка индекса: %s; экранов %d, записей %d (из них настроек %d), текста около %d КБ", spent.Round(time.Millisecond), idx.Screens,
		len(idx.Entries), countKind(idx, entrySetting), size/1024)
	if spent > 3*time.Second {
		t.Errorf("сборка индекса заняла %s", spent)
	}
	// второй вызов через замок — из памяти
	started = time.Now()
	s.settingsIndex()
	first := time.Since(started)
	started = time.Now()
	s.settingsIndex()
	if again := time.Since(started); again > 5*time.Millisecond {
		t.Errorf("запомненный индекс отдаётся за %s (первый раз %s)", again, first)
	}
	// поиск по готовому индексу — мгновенный: перебор записей в памяти. Первый показ
	// «Обзора» не в счёт: на macOS он один раз собирает помощника распознавания
	// картинок компилятором (~400 мс, `systemOCR`), и индекс тут ни при чём
	if _, err := s.buildScreen("settings", "", "", 0); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"окно", "цена", "пуш", "gpt"} {
		started = time.Now()
		doc, err := s.buildScreen("settings", "", q, 0)
		if err != nil {
			t.Fatal(err)
		}
		took := time.Since(started)
		settings, entities := resultRows(doc)
		t.Logf("поиск %q: %s, настроек %d, сущностей %d", q, took.Round(time.Microsecond), len(settings), len(entities))
		if took > 150*time.Millisecond {
			t.Errorf("поиск %q по готовому индексу занял %s", q, took)
		}
	}
}

func countKind(idx *settingsIndex, kind string) int {
	n := 0
	for _, e := range idx.Entries {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// Индекс держится в памяти, пока после него ничего не менялось руками, и
// сбрасывается после действия, которое могло поменять состав экранов; правка одних
// настроек из реестра его не сбрасывает — значение у них читается заново.
func TestIndexDropsAfterHumanAction(t *testing.T) {
	s, st := indexFix(t, 20)
	first := s.settingsIndex()
	if s.settingsIndex() != first {
		t.Fatal("индекс собран дважды подряд")
	}
	// правка настройки реестра: состав экранов тот же
	mark := st.LastChange()
	if err := st.SetSetting("ui.poll_s", "9"); err != nil {
		t.Fatal(err)
	}
	onSettings := actionRequest{Action: "save_x", Screen: "settings:servers"}
	if s.indexStaleAfter(onSettings, st.ChangedKeysSince(mark)) {
		t.Fatal("правка настройки реестра сбросила индекс")
	}
	// служебная отметка (прочтение чата) — тоже нет
	mark = st.LastChange()
	if err := st.SetSetting("chat.read.71", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if s.indexStaleAfter(actionRequest{Action: "send_message", Screen: "chat:71"}, st.ChangedKeysSince(mark)) {
		t.Error("отметка прочтения сбросила индекс")
	}
	// без правок настроек на экране настроек — устарел: могли завести сервер
	mark = st.LastChange()
	if !s.indexStaleAfter(onSettings, st.ChangedKeysSince(mark)) {
		t.Error("действие на экране настроек без правок настроек сочтено безобидным: оно могло завести сервер")
	}
	// вне настроек без правок — нет, кроме тех, что заводят сущность снаружи
	if s.indexStaleAfter(actionRequest{Action: "not_spam", Screen: "inbox"}, nil) {
		t.Error("«не спам» сбросил индекс настроек")
	}
	if !s.indexStaleAfter(actionRequest{Action: "decide_skill", Screen: "chat:5"}, nil) {
		t.Error("скилл, принятый из чата, не сбросил индекс")
	}
	// вход и токены — нет
	if s.indexStaleAfter(actionRequest{Action: "add_token", Screen: "settings:access~tokens"}, nil) {
		t.Error("новый токен сбросил индекс")
	}
	// правка не из реестра — устарел
	mark = st.LastChange()
	if err := st.SetSetting("mail.conf.проба", "{}"); err != nil {
		t.Fatal(err)
	}
	if !s.indexStaleAfter(actionRequest{Action: "x", Screen: "inbox"}, st.ChangedKeysSince(mark)) {
		t.Error("правка настройки не из реестра сочтена безобидной")
	}
	s.dropSettingsIndex()
	if s.settingsIndex() == first {
		t.Error("после сброса индекс не собран заново")
	}
	// срок жизни
	old := s.settingsIndex()
	old.At = time.Now().Add(-indexTTL - time.Second)
	if s.settingsIndex() == old {
		t.Error("просроченный индекс не пересобран")
	}
}

// У рабочего сервера индекс собирается в фоне (perf-pass 4.2–4.3): пока его нет,
// Обзор и поиск говорят «индекс собирается» и не ждут; после сброса до новой
// сборки отвечает прежний индекс.
func TestIndexInBackground(t *testing.T) {
	s, _ := indexFix(t, 20)
	s.index.background = true
	start := time.Now()
	if idx := s.settingsIndex(); idx != nil {
		t.Fatal("индекса ещё нет, а он отдан — значит, собран в запросе")
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("запрос ждал сборки %v", d)
	}
	// поиск без индекса не ждёт его: говорит, что он собирается, и находит
	// разделы (Обзору на пустой базе индекс не нужен вовсе — ему нечего называть)
	searched := time.Now()
	doc, err := s.buildScreen("settings", "", "опрос", 0)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(searched); d > 500*time.Millisecond {
		t.Errorf("поиск без индекса ждал %v", d)
	}
	if !strings.Contains(dumpScreen(doc), "Индекс настроек собирается") && s.settingsIndex() == nil {
		t.Error("поиск без индекса не сказал, что он собирается")
	}
	// ждём условия, а не срока: индекс, отличный от прежнего
	waitIndex := func(not *settingsIndex) *settingsIndex {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			s.index.mu.Lock()
			idx := s.index.idx
			s.index.mu.Unlock()
			if idx != nil && idx != not {
				return idx
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("фоновая сборка не дала нового индекса за 10 с")
		return nil
	}
	first := waitIndex(nil)
	if got := s.settingsIndex(); got != first {
		t.Fatal("готовый индекс не отдан")
	}
	s.dropSettingsIndex()
	if got := s.settingsIndex(); got != first {
		t.Error("после сброса, до новой сборки, отвечает не прежний индекс")
	}
	second := waitIndex(first)
	// чтения без действий пересборок не заказывают: опрос открытого Обзора
	// не должен гонять их по кругу
	stop := time.Now().Add(2*indexSettle + 300*time.Millisecond)
	for time.Now().Before(stop) {
		_ = s.settingsIndex()
		time.Sleep(2 * time.Millisecond)
	}
	if got := s.settingsIndex(); got != second {
		t.Error("одни чтения, без действий, пересобрали индекс")
	}
}
