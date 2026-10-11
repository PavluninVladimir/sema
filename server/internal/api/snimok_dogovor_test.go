package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Договор снимка (offline-snapshot, группа 2): политика хранения и
// откладываемость решают на сервере, и решение принимается поимённо для
// каждого экрана и действия.
//
// РЕГРЕСС: хранимо ровно то, что названо белым списком (api.storableScreens);
// всё прочее — не хранимо, включая экран, о котором никто не подумал.
// Откладываемыми помечены ровно два действия по имени.
//
// Обход идёт по обоим видам окна и двум спискам экранов (coreScreens +
// allScreens), как в TestAllScreensBuildOnEmptyDatabase.
//
// Чего он НЕ видит: база в целом пустая, поэтому осматриваются только ветки,
// которые рисуются без данных. Что именно уезжает внутри хранимого экрана,
// проверяет зарубками TestSnapshotWithoutClosedAccounts.
func TestSnapshotRetentionPolicy(t *testing.T) {
	s, st := testServer(t)

	// Экраны, которые обязаны быть хранимыми: без них белый список
	// превратился бы в «ничего офлайн», и человек потерял бы снимок молча.
	ждёмХранимые := []string{
		"inbox", "inbox@проба", "senders", "files", "digest:groups", "health", "queue",
		"settings", "settings:mail", "settings:models", "settings:tools",
		"settings:storages", "settings:notify", "settings:search",
		"settings:learning", "settings:skills", "settings:changes",
		"settings:watch",
		// пущены владельцем, хотя несут чужое: вывод команд, имена
		// собеседников, ответы серверов MCP
		"settings:servers", "settings:servers~log:хозяин", "settings:telegram",
		"settings:max", "settings:integrations",
		// хвост адреса хранимости не меняет: @ящик и ~вкладка у названного экрана
		"senders~robots", "settings:mail~folders", "settings:models~issues",
	}

	// Всё остальное — не хранимо. Здесь и то, что запрещено по существу
	// (секреты, «Что ушло», доступ), и то, о чём прежний чёрный список не
	// вспомнил: «Стёртое» с темой письма, уведомления, поставщики, память,
	// чаты, скрипты и экран модуля.
	опасные := []string{
		"settings:secrets", "settings:secrets@имя-секрета",
		"sent", "sent~log", "sent~7",
		"settings:access", "settings:access~tokens", "settings:access~network", "settings:access~log",
		"purged", "notifications", "notify", "vendors", "vendors@1",
		"memory", "memory~areas", "memory:graph", "memory:sessions", "memory:log",
		"ask", "tasks", "scripts", "script:1", "modules", "categories",
		"settings:agents",
	}

	// У письма своя, точная проверка, и проверять её надо всеми тремя
	// ответами. Номер письма берём тот, который выдала база: InsertMessage
	// поле ID не читает, и «msg:42» указывал бы в пустоту — проверка падала
	// бы на «письма нет», а класс и ящик не смотрели бы вовсе. Именно так
	// этот тест и был зелен по ложной причине.
	положить := func(account, subject, class string) int64 {
		m := &store.Message{Account: account, MessageID: "<" + subject + "@пример.рф>",
			Subject: subject, FromAddr: "кто-то@пример.рф", Date: time.Now(), Status: store.StatusInboxOK}
		if err := st.InsertMessage(m); err != nil {
			t.Fatalf("вставить письмо %q: %v", subject, err)
		}
		if m.ID == 0 {
			t.Fatalf("письмо %q не получило номера", subject)
		}
		if class != "" {
			if err := st.SetContentClass(store.ClassMsg, m.ID, class, "", "тест"); err != nil {
				t.Fatalf("класс письма %q: %v", subject, err)
			}
		}
		return m.ID
	}
	// закрытый для облака ящик: письмо из него не хранится, даже обычное
	accounts, err := st.Accounts()
	if err != nil {
		t.Fatalf("список ящиков: %v", err)
	}
	accounts = append(accounts, &store.Account{Name: "закрытый", Email: "закрытый@пример.рф",
		Host: "imap.пример.рф", Port: 993})
	if err := st.SaveAccounts(accounts); err != nil {
		t.Fatalf("ящики: %v", err)
	}
	conf := st.MailConf("закрытый")
	нет := false
	conf.Outbound = &нет
	if err := st.SaveMailConf("закрытый", conf); err != nil {
		t.Fatalf("настройки закрытого ящика: %v", err)
	}
	if !s.eng.ClosedAccounts(true)["закрытый"] {
		t.Fatal("ящик не считается закрытым для облака — письмо из него проверять нечем")
	}
	// обычное письмо открытого ящика — хранимо: без этого случая проверка
	// письма отвечала бы «нет» всегда и всем, и тест этого не заметил бы
	ждёмХранимые = append(ждёмХранимые, "msg:"+strconv.FormatInt(положить("проба", "обычное-письмо", ""), 10))
	опасные = append(опасные,
		"msg:"+strconv.FormatInt(положить("проба", "секретное-письмо", store.ClassSecret), 10),
		"msg:"+strconv.FormatInt(положить("закрытый", "письмо-закрытого", ""), 10),
		// письма нет и номер не разобрать — сомнение, не хранимый
		"msg:999999", "msg:не-число")

	// Создадим тестовый чат для проверки откладываемых действий
	chatID, err := st.CreateChat("Тестовый чат")
	if err != nil {
		t.Fatalf("создать чат: %v", err)
	}
	опасные = append(опасные, "chat:"+strconv.FormatInt(chatID, 10))

	проверить := func(cond bool, msg string) {
		if !cond {
			t.Error(msg)
		}
	}
	// хранимость экрана от вида окна не зависит, но проверяем оба: телефон
	// собирает «Чаты» и разговор другими сборщиками
	walk := func(screens []string, want bool) {
		seen := map[string]bool{}
		for _, screen := range screens {
			if seen[screen] {
				continue
			}
			seen[screen] = true
			for _, телефон := range []bool{false, true} {
				doc, err := s.buildScreenForPhone(screen, "", "", 0, телефон, false)
				if err != nil || doc == nil {
					continue
				}
				where := screen
				if телефон {
					where += " (телефон)"
				}
				switch {
				case want && !doc.Storable:
					t.Errorf("экран %q не помечен хранимым — офлайн его не будет", where)
				case !want && doc.Storable:
					t.Errorf("ДЫРА: опасный экран %q помечен хранимым", where)
				}
			}
		}
	}
	t.Logf("Ждём хранимыми: %v", ждёмХранимые)
	walk(ждёмХранимые, true)
	t.Logf("Ждём нехранимыми: %v", опасные)
	walk(опасные, false)

	// Умолчание — запрет: адрес, которого в белом списке нет, не хранится,
	// даже если он собирается без ошибки (выдуманное имя падает в «Почту»).
	// Экран модуля — никогда: что рисует модуль, ядро не знает.
	walk([]string{
		"выдуманный:экран", "module:проба:главный",
		"module:чеки:отчёт", "inbox-подделка", "sentинное",
	}, false)

	// Ни один экран из списков обхода не должен оказаться хранимым, если его
	// имени нет в белом списке: это тот же вопрос, но заданный не поимённо, а
	// всем экранам сразу — новый экран попадёт сюда сам.
	seen := map[string]bool{}
	for _, screen := range append(append([]string{}, coreScreens...), allScreens...) {
		if seen[screen] {
			continue
		}
		seen[screen] = true
		name := screen
		if i := strings.IndexAny(name, "@~"); i >= 0 {
			name = name[:i]
		}
		doc, err := s.buildScreenForPhone(screen, "", "", 0, false, false)
		if err != nil || doc == nil {
			continue
		}
		if doc.Storable && !storableScreens[name] && !strings.HasPrefix(screen, "msg:") {
			t.Errorf("ДЫРА: экран %q хранимый, а в белом списке его нет", screen)
		}
	}

	// Проверяем откладываемые действия: их должно быть ровно два
	отложены := map[string]int{}
	seen = map[string]bool{}
	// Добавим экран чата для проверки действий Composer
	screens := append(append([]string{}, coreScreens...), allScreens...)
	screens = append(screens, "chat:"+strconv.FormatInt(chatID, 10))
	for _, screen := range screens {
		if seen[screen] {
			continue
		}
		seen[screen] = true
		for _, телефон := range []bool{false, true} {
			doc, err := s.buildScreenForPhone(screen, "", "", 0, телефон, false)
			if err != nil || doc == nil {
				continue
			}
			for _, a := range buildActions(doc) {
				if a.Deferrable {
					отложены[a.Action]++
				}
			}
		}
	}

	t.Logf("Откладываемые действия: %v", отложены)

	// Ровно два типа действий должны быть откладываемы
	проверить(len(отложены) == 2, fmt.Sprintf("ДЫРА: откладываемых действий %d, ждали 2; найдены: %v", len(отложены), отложены))
	проверить(отложены["send_message"] > 0, "ДЫРА: откладываемое действие \"send_message\" не найдено")
	проверить(отложены["attach_file"] > 0, "ДЫРА: откладываемое действие \"attach_file\" не найдено")
}

// buildActions — все действия документа, куда бы их ни положили:
// шапка и её меню, панель, форма ввода, мастер, списки в шапке, строки,
// их меню и кнопки, ячейки таблиц и матриц, команды в раскрытии, пункты
// бокового меню. Тот же обход в духе usedTypes в dictionary_test.go, но
// по действиям, а не по типам компонентов.
func buildActions(doc *Document) []Action {
	var acts []Action
	add := func(a Action) { acts = append(acts, a) }
	addP := func(a *Action) {
		if a != nil {
			acts = append(acts, *a)
		}
	}

	acts = append(acts, doc.ToolbarActions...)
	acts = append(acts, doc.ToolbarMenu...)
	addP(doc.PanelAction)
	for _, p := range doc.Panel {
		addP(p.Delete)
	}
	if doc.Composer != nil {
		add(doc.Composer.Submit)
		addP(doc.Composer.Attach)
	}
	if doc.Wizard != nil {
		add(doc.Wizard.Submit)
		add(doc.Wizard.Cancel)
	}
	for _, sel := range doc.HeaderSelects {
		add(sel.Action)
	}

	var walkScreens func([]Screen)
	walkScreens = func(ss []Screen) {
		for _, sc := range ss {
			addP(sc.Add)
			walkScreens(sc.Children)
		}
	}
	walkScreens(doc.Screens)
	walkScreens(doc.PhoneTabs)

	var walkComponents func([]Component)
	walkComponents = func(cs []Component) {
		for _, c := range cs {
			acts = append(acts, c.Menu...)
			addP(c.Submit)
			acts = append(acts, c.Actions...)
			for _, sel := range c.Selects {
				add(sel.Action)
			}
			for _, cell := range c.Cells {
				add(cell.Action)
			}
			for _, cmd := range c.Cmds {
				acts = append(acts, cmd.Actions...)
			}
			walkComponents(c.Children)
		}
	}
	walkComponents(doc.Components)

	return acts
}

// TestSnapshotWithoutClosedAccounts — регресс группы 2а «Снимок собирается
// отдельно» (offline-snapshot, design.md, решение 8а): экраны показывают
// человеку закрытый для облака ящик как есть (он закрыт для модели, а не
// от хозяина), но снимок на устройстве собирается отдельной сборкой с
// отсевом — и в неё письмо закрытого ящика попасть не должно ни при каких
// обстоятельствах. Ищем по всему документу (сериализованному в JSON), а не
// по паре полей: забытый сборщик прячется именно в поле, о котором не
// подумали.
func TestSnapshotWithoutClosedAccounts(t *testing.T) {
	s, st := testServer(t)

	// второй ящик — закрытый для облачной модели
	accounts, err := st.Accounts()
	if err != nil {
		t.Fatalf("список ящиков: %v", err)
	}
	accounts = append(accounts, &store.Account{Name: "закрытый", Email: "закрытый@пример.рф", Host: "imap.пример.рф", Port: 993})
	if err := st.SaveAccounts(accounts); err != nil {
		t.Fatalf("ящики: %v", err)
	}
	conf := st.MailConf("закрытый")
	нет := false
	conf.Outbound = &нет
	if err := st.SaveMailConf("закрытый", conf); err != nil {
		t.Fatalf("настройки закрытого ящика: %v", err)
	}

	// без этого тест ничего не значит: не увидели ящик закрытым — дальше
	// нечего проверять, а тест прошёл бы вхолостую
	closed := s.eng.ClosedAccounts(true)
	if !closed["закрытый"] {
		t.Fatal("новый ящик не считается закрытым для облака — проверять нечего")
	}

	const секрет = "зарубка-секрет-77"
	msg := &store.Message{
		Account:   "закрытый",
		MessageID: "<" + секрет + "@пример.рф>",
		FromAddr:  "коллега@пример.рф",
		FromName:  "Коллега",
		Subject:   секрет,
		Date:      time.Now(),
		Status:    store.StatusInboxOK,
	}
	if err := st.InsertMessage(msg); err != nil {
		t.Fatalf("вставить письмо закрытого ящика: %v", err)
	}

	// Вложение того же ящика, и не простое, а счёт к оплате: «Файлы»
	// показывают вложения, а «Почта» — блок ближайшего платежа, и оба
	// берут вложения своей дорогой, мимо отсева писем. Без этой зарубки
	// тест сторожил бы только одну из двух дорог.
	const вложение = "зарубка-вложение-77"
	if err := st.InsertAttachment(&store.Attachment{
		MessageID: msg.ID,
		Filename:  вложение + ".pdf",
		MIME:      "application/pdf",
		Size:      10,
		ToPay:     true,
		DueDate:   time.Now().AddDate(0, 0, 3).Format("2006-01-02"),
	}); err != nil {
		t.Fatalf("вставить вложение закрытого ящика: %v", err)
	}
	// Загруженный человеком файл без письма (MessageID == 0, account пуст
	// всегда): фильтр в SQL по account NOT IN (...) срезал бы такой файл
	// вместе с закрытым ящиком, стоит закрыть хоть один ящик с пустым
	// именем, — поэтому отсев идёт после выборки и только по письму.
	// Этот файл должен остаться в снимке.
	const загрузка = "зарубка-загрузка-77"
	if err := st.InsertAttachment(&store.Attachment{
		Filename: загрузка + ".pdf",
		MIME:     "application/pdf",
		Size:     5,
	}); err != nil {
		t.Fatalf("вставить загруженный файл: %v", err)
	}

	// Журнал удалений («Стёртое»): тему письма он берёт из самого письма, и
	// отсева у него нет вовсе — прежний чёрный список про этот экран не
	// вспомнил, а тема с именем ящика уезжали на телефон.
	if err := st.LogDeletion(msg.ID, "закрытый", "Junk", "спам по сроку"); err != nil {
		t.Fatalf("записать удаление: %v", err)
	}

	// Уведомление: тема письма стоит у него в заголовке (engine/notifications.go).
	const уведомление = "зарубка-уведомление-77"
	if _, err := st.PutNotification(&store.Notification{
		Key: "проба:зарубка", Source: "mail", Level: "warn",
		Title: "Письмо без ответа: " + уведомление, Summary: "проба зарубки",
		Ref: "msg:" + strconv.FormatInt(msg.ID, 10), State: store.NotifyOpen,
	}); err != nil {
		t.Fatalf("положить уведомление: %v", err)
	}

	// Поставщик: «Поставщики» и его документы берут вложения своей дорогой,
	// мимо отсева, — значит счёт закрытого ящика виден и оттуда.
	const поставщик = "зарубка-поставщик-77"
	vendorID, err := st.EnsureVendor(поставщик)
	if err != nil {
		t.Fatalf("завести поставщика: %v", err)
	}
	файлы, err := st.AttachmentsOfMessage(msg.ID)
	if err != nil || len(файлы) == 0 {
		t.Fatalf("вложения письма: err=%v, найдено %d", err, len(файлы))
	}
	if err := st.SetFileVendor(файлы[0].ID, vendorID); err != nil {
		t.Fatalf("привязать файл к поставщику: %v", err)
	}

	// Запись памяти: в память попадает всё, включая разговоры про закрытый ящик.
	const память = "зарубка-память-77"
	if _, err := st.SaveMemory(&store.Memory{Kind: store.MemFact, Area: "проба",
		Text: память, Agent: "тест"}); err != nil {
		t.Fatalf("записать память: %v", err)
	}

	// Сообщение чата: в переписке с Сёмой лежат цитаты писем и вывод команд.
	const переписка = "зарубка-переписка-77"
	chatID, err := st.CreateChat("Проба зарубки")
	if err != nil {
		t.Fatalf("создать чат: %v", err)
	}
	if err := st.AddChatMsg(chatID, "assistant", "в письме сказано: "+переписка); err != nil {
		t.Fatalf("написать в чат: %v", err)
	}

	зарубки := []string{секрет, вложение, уведомление, поставщик, память, переписка}

	screens := append(append([]string{}, coreScreens...), allScreens...)
	screens = append(screens, "inbox@закрытый", purgedScreen, "vendors",
		vendorsScreen+"@"+strconv.FormatInt(vendorID, 10),
		"chat:"+strconv.FormatInt(chatID, 10))

	// Зарубка, которая ни на одном экране не встретилась, ничего не сторожит:
	// такой тест зелен по ложной причине. Считаем каждую поимённо.
	доехали := map[string]bool{}
	var протеклоВСнимке []string
	seen := map[string]bool{}
	for _, screen := range screens {
		if seen[screen] {
			continue
		}
		seen[screen] = true
		for _, телефон := range []bool{false, true} {
			where := screen
			if телефон {
				where += " (телефон)"
			}
			// обычный показ: человеку видно всё, включая закрытые ящики
			if doc, err := s.buildScreenForPhone(screen, "", "", 0, телефон, false); err == nil && doc != nil {
				if raw, jerr := json.Marshal(doc); jerr == nil {
					for _, з := range зарубки {
						if strings.Contains(string(raw), з) {
							доехали[з] = true
						}
					}
				}
			}
			// Снимочная сборка. Проверяется только то, что человек и правда
			// сохранит: экран, помеченный нехранимым, до устройства не
			// доедет, и его содержимое здесь ничего не решает.
			doc, err := s.buildScreenForPhone(screen, "", "", 0, телефон, true)
			if err != nil || doc == nil || !doc.Storable {
				continue
			}
			raw, jerr := json.Marshal(doc)
			if jerr != nil {
				continue
			}
			for _, з := range зарубки {
				if strings.Contains(string(raw), з) {
					протеклоВСнимке = append(протеклоВСнимке, where+" ["+з+"]")
				}
			}
		}
	}

	for _, з := range зарубки {
		if !доехали[з] {
			t.Fatalf("зарубка %q не встретилась ни на одном обычном экране — она ничего не сторожит", з)
		}
	}
	if len(протеклоВСнимке) > 0 {
		t.Errorf("ДЫРА: секретное слово из закрытого ящика попало в снимочную сборку экранов: %v", протеклоВСнимке)
	}

	// Ошибка, ради которой отсев не в SQL: загруженный файл без письма не
	// должен пропасть из снимка вместе с закрытым ящиком.
	docFiles, err := s.buildScreenForPhone("files", "", "", 0, false, true)
	if err != nil || docFiles == nil || !docFiles.Storable {
		t.Fatalf("собрать снимок «Файлов»: err=%v doc=%v", err, docFiles)
	}
	rawFiles, err := json.Marshal(docFiles)
	if err != nil {
		t.Fatalf("сериализовать снимок «Файлов»: %v", err)
	}
	if !strings.Contains(string(rawFiles), загрузка) {
		t.Errorf("ДЫРА: загруженный файл без письма пропал из снимочной сборки вместе с закрытым ящиком")
	}
	// Счётчик «Показаны N из M» считается запросом и не знает про отсев:
	// в снимке он и соврал бы, и намекнул, что за кадром остались чьи-то
	// файлы. В снимке его быть не должно вовсе.
	if strings.Contains(string(rawFiles), "Показаны ") || strings.Contains(string(rawFiles), "Показать ещё") {
		t.Errorf("ДЫРА: снимок «Файлов» называет число спрятанных файлов")
	}
}

// TestSnapshotWithoutSecretWhenNoClosedAccounts — второй заход отсева, и
// главный: закрытых ящиков нет вовсе (обычный дом — у владельца оба ящика
// открыты), а письмо и файл помечены «секретное».
//
// Сторожит две вещи разом.
//
// Первое: признак «снимок отличается» (Document.SnapshotDiffers) не имеет
// права зависеть от закрытых ящиков. Клиент просит снимочную сборку
// (?snapshot=1) только по этому признаку, и прежнее условие «есть хоть один
// закрытый ящик» в таком доме ложно: на диск ложилась **показанная**
// сборка, и отсев «секретного», сделанный на слое данных, до устройства не
// доходил вовсе.
//
// Второе: сама снимочная сборка «секретного» не содержит.
//
// Инвариант сформулирован так, чтобы не перечислять экраны руками: если
// зарубка видна в показанной сборке хранимого экрана, этот экран обязан
// сказать «снимок отличается» — иначе именно показанное и ляжет на диск.
func TestSnapshotWithoutSecretWhenNoClosedAccounts(t *testing.T) {
	s, st := testServer(t)

	// без этого тест ничего не значит: появился закрытый ящик — и признак
	// снова стал бы правдой по другой причине, а дыра осталась бы
	if closed := s.eng.ClosedAccounts(true); len(closed) > 0 {
		t.Fatalf("в доме есть закрытые ящики (%v) — проверять нечего", closed)
	}

	const секрет = "зарубка-секретное-88"
	msg := &store.Message{
		Account:   "проба",
		MessageID: "<" + секрет + "@пример.рф>",
		FromAddr:  "нотариус@пример.рф",
		FromName:  "Нотариус",
		Subject:   секрет,
		Date:      time.Now(),
		Status:    store.StatusInboxOK,
	}
	if err := st.InsertMessage(msg); err != nil {
		t.Fatalf("вставить письмо: %v", err)
	}
	if err := st.SetContentClass(store.ClassMsg, msg.ID, store.ClassSecret, "паспорт в теме", "тест"); err != nil {
		t.Fatalf("класс письма: %v", err)
	}

	// файл того же письма: «Файлы» берут вложения своей дорогой, и класс у
	// файла свой — проверяем обе метки, письма и файла
	const вложение = "зарубка-вложение-88"
	att := &store.Attachment{
		MessageID: msg.ID,
		Filename:  вложение + ".pdf",
		MIME:      "application/pdf",
		Size:      10,
	}
	if err := st.InsertAttachment(att); err != nil {
		t.Fatalf("вставить вложение: %v", err)
	}
	if err := st.SetContentClass(store.ClassFile, att.ID, store.ClassSecret, "скан паспорта", "тест"); err != nil {
		t.Fatalf("класс файла: %v", err)
	}

	зарубки := []string{секрет, вложение}
	screens := append(append([]string{}, coreScreens...), allScreens...)
	screens = append(screens, "inbox@проба", "senders@проба")

	доехали := map[string]bool{}
	var молчатОРазнице, протеклоВСнимке []string
	seen := map[string]bool{}
	for _, screen := range screens {
		if seen[screen] {
			continue
		}
		seen[screen] = true
		for _, телефон := range []bool{false, true} {
			where := screen
			if телефон {
				where += " (телефон)"
			}
			показанный, err := s.buildScreenForPhone(screen, "", "", 0, телефон, false)
			if err != nil || показанный == nil {
				continue
			}
			raw, jerr := json.Marshal(показанный)
			if jerr != nil {
				continue
			}
			виднаЗарубка := false
			for _, з := range зарубки {
				if strings.Contains(string(raw), з) {
					доехали[з] = true
					виднаЗарубка = true
				}
			}
			// показанная сборка несёт секретное — значит на диск она лечь не
			// может, и сервер обязан сказать, что снимок другой
			if показанный.Storable && виднаЗарубка && !показанный.SnapshotDiffers {
				молчатОРазнице = append(молчатОРазнице, where)
			}

			снимок, err := s.buildScreenForPhone(screen, "", "", 0, телефон, true)
			if err != nil || снимок == nil || !снимок.Storable {
				continue
			}
			сырой, jerr := json.Marshal(снимок)
			if jerr != nil {
				continue
			}
			for _, з := range зарубки {
				if strings.Contains(string(сырой), з) {
					протеклоВСнимке = append(протеклоВСнимке, where+" ["+з+"]")
				}
			}
		}
	}

	for _, з := range зарубки {
		if !доехали[з] {
			t.Fatalf("зарубка %q не встретилась ни на одном показанном экране — она ничего не сторожит", з)
		}
	}
	if len(молчатОРазнице) > 0 {
		t.Errorf("ДЫРА: хранимый экран показывает секретное, но не говорит «снимок отличается» — клиент положит на диск показанное: %v", молчатОРазнице)
	}
	if len(протеклоВСнимке) > 0 {
		t.Errorf("ДЫРА: секретное попало в снимочную сборку экранов: %v", протеклоВСнимке)
	}
}
