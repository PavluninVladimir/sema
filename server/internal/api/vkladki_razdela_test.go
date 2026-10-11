package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Карта переезда: «Почту» разрезали на шесть вкладок, а 2 октября 2026 —
// на три вкладки раздела и пять вкладок ящика, и ни одна настройка при этом
// не должна была исчезнуть. Список имён полей — снимок того, что было на одной
// ленте до разреза; строка отсюда пропала — значит настройка стала
// недостижимой, и человек этого сам не заметит. Полнее — в
// settings_mail_test.go (TestMailNothingLostAgainstBefore): там имена и
// действия снимка «до» по всем способам входа.
func TestMailSettingsNotLost(t *testing.T) {
	s, _ := testServer(t)
	// screenSettings обходит и ленивую «Тонкую настройку»: слова счёта и порог
	// писем у провайдера лежат там
	has := screenSettings(t, s, "settings:mail", "settings:mail~spam", "settings:mail~common", "settings:mail@new",
		"settings:mail@проба", "settings:mail@проба~conn", "settings:mail@проба~send",
		"settings:mail@проба~folders", "settings:mail@проба~keep")
	before := []string{
		// ящик
		// признак сбора зовётся «сбор:<имя>»: их состав объявляют ядро и
		// установленные модули, поэтому здесь только признак ядра —
		// модульные появляются вместе со своим модулем
		"purpose", "mode", "сбор:attention", "outbound", "fetch",
		// подключение и отправка
		"auth", "username", "password", "host",
		"smtp_host", "smtp_port", "smtp_starttls", "smtp_user", "smtp_secret",
		"!connect_mailbox", "!rescan_attachments",
		// хранение: своё у ящика и общее
		"junk", "purge_other", "keep_days", "retention",
		// общее
		"server_folders", "send_mode", "send_auto_to",
		"whitelist", "threshold", "money_guard", "money_words",
	}
	for _, name := range before {
		if !has[name] {
			t.Errorf("настройка %q не нашлась ни на одной вкладке «Почты» — она стала недостижимой", name)
		}
	}
}

// Общие настройки разъехались по четырём формам, и каждая шлёт только
// свои поля. Непришедшее поле значит «не трогали»: иначе сохранение
// сроков хранения молча гасило бы белый список.
func TestCommonFormsDoNotMuteEachOther(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.SetWhitelistOnly(true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "save_automation",
		Params: map[string]string{"keep_days": "45"}}); err != nil {
		t.Fatal(err)
	}
	if !s.eng.WhitelistOnly() {
		t.Error("сохранение сроков хранения выключило «только разрешённые»")
	}
	if s.eng.RestTTL() != 45 {
		t.Errorf("срок не сохранился: %d", s.eng.RestTTL())
	}
}

// Шапка ящика отвечает на вопрос «чей это параметр». На вкладках раздела её
// быть не должно: «Спам» и «Общее» целиком общие, и закреплённый ящик соврал
// бы, что порог уверенности правится ему одному. (Прежняя полоса `закреп`
// заменена общей шапкой сущности `entity`.)
func TestEntityHeaderOnlyOnBoxScreens(t *testing.T) {
	s, _ := testServer(t)
	шапка := func(screen string) bool {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range doc.Components {
			if c.ID == "entity" {
				return true
			}
		}
		return false
	}
	for _, в := range []string{"~work", "~conn", "~send", "~folders", "~keep", "@проба", "@проба~keep"} {
		if !шапка("settings:mail" + в) {
			t.Errorf("на settings:mail%s не видно, какой ящик настраиваем", в)
		}
	}
	for _, в := range []string{"", "~spam", "~common", "@new"} {
		if шапка("settings:mail" + в) {
			t.Errorf("на settings:mail%s шапка ящика, хотя экран общий", в)
		}
	}
}

// На «Спаме» и «Общем» вместо ящика — область действия: с чьими настройками
// человек имеет дело.
func TestSpamSaysItIsCommon(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range []string{"settings:mail~spam", "settings:mail~common"} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(dumpScreen(doc), "Одинаково для всех ящиков") {
			t.Errorf("%s: не сказано, что настройки общие", screen)
		}
	}
}

// Внутренняя вкладка — это адрес, а не фильтр: по нему можно вернуться.
// И пять постоянных вкладок при этом не сбиваются: хвост адреса не
// должен уводить каркас в «неизвестный раздел».
func TestTailDoesNotBreakFrame(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range []string{"settings:mail~folders", "settings:models~issues"} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if doc.ActivePanel == "" {
			t.Errorf("%s: раздел в списке не выбран", screen)
		}
		// у «Почты» адрес по дороге обрастает ящиком
		// («settings:mail@ящик~folders») — хвост при этом остаётся тем же
		_, хвост := splitSubtab(doc.ActiveSubtab)
		_, want := splitSubtab(screen)
		if хвост != want {
			t.Errorf("%s: выбранная внутренняя вкладка %q", screen, doc.ActiveSubtab)
		}
		if len(doc.Subtabs) < 4 {
			t.Errorf("%s: внутренних вкладок %d", screen, len(doc.Subtabs))
		}
	}
}

// Незнакомый хвост — не повод показать пустоту: открываем первую
// вкладку, а не «экран не найден».
func TestUnknownTabLeadsToFirst(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings:models~выдумка", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ActiveSubtab != modelsScreen {
		t.Errorf("выбрана вкладка %q вместо первой", doc.ActiveSubtab)
	}
}

// Служебные роли переехали с «Инструментов» к моделям, а со списка моделей — на
// вкладку «Роли»: замечание про пропавший эмбеддер приходит на «Модели и роли»,
// и строка должна быть там же, а не под списком из полутораста моделей.
func TestServiceModelsLiveWithModels(t *testing.T) {
	s, _ := testServer(t)
	роли, err := s.buildScreen("settings:models", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dumpScreen(роли), "Доверие эмбеддеру") {
		t.Error("на «Модели и роли → Роли» нет служебных ролей")
	}
	список, err := s.buildScreen("settings:models~list", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dumpScreen(список), "Доверие эмбеддеру") {
		t.Error("служебные роли остались и под списком моделей — их стало два места")
	}
	инструменты, err := s.buildScreen("settings:tools", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dumpScreen(инструменты), "Доверие эмбеддеру") {
		t.Error("служебные модели остались и на «Инструментах» — их стало два места")
	}
}

// «Выключить все» из замечания про зря разбираемые папки должно
// действительно их выключать: замечание без действия — отговорка.
func TestDisablePointlessFolders(t *testing.T) {
	s, _ := testServer(t)
	const ящик = "почта@пример.рф"
	c := &store.MailConf{Folders: []string{"INBOX", "[Gmail]/Вся почта", "[Gmail]/Корзина"},
		Archive: "Archive", Junk: "Spam"}
	if err := s.eng.SaveMailConf(ящик, c); err != nil {
		t.Fatal(err)
	}
	if len(pointlessFolders(s.eng.MailConf(ящик))) != 2 {
		t.Fatalf("зряшных папок не две: %v", pointlessFolders(s.eng.MailConf(ящик)))
	}
	if _, err := s.execute(actionRequest{Action: "skip_folders", Target: ящик}); err != nil {
		t.Fatal(err)
	}
	if зря := pointlessFolders(s.eng.MailConf(ящик)); len(зря) != 0 {
		t.Errorf("папки остались в разборе: %v", зря)
	}
}

// Список моделей идёт порциями: пятьдесят четыре строки — это пять
// тысяч узлов на экране, который открывают ради одной цены. Кнопка
// «Показать ещё» обязательна: поиск у клиента идёт по загруженному, и
// без неё до остальных моделей не добраться вовсе.
func TestModelListInBatches(t *testing.T) {
	s, _ := testServer(t)
	for i := 0; i < 45; i++ {
		if err := s.eng.AddModel(engine.ModelConf{Name: fmt.Sprintf("м%02d", i),
			Type: "openai", BaseURL: "https://шлюз.example.org/v1", Model: "м"}); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := s.buildScreen("settings:models~list", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "Показать ещё") {
		t.Error("список моделей отдан целиком, кнопки «Показать ещё» нет")
	}
	строк := strings.Count(dump, `"id":"model-`)
	if строк != modelsPage {
		t.Errorf("в порции %d строк вместо %d", строк, modelsPage)
	}
	// а по запросу — все
	все, err := s.buildScreen("settings:models~list", "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(dumpScreen(все), `"id":"model-`); n < 45 {
		t.Errorf("«Показать ещё» не показывает остальных: %d строк", n)
	}
}

// Свёрнутый раздел всё равно едет к человеку целиком: его свёрнутость
// живёт у клиента. «Можно подключить» — восемнадцать строк, которые
// почти никогда не нужны, поэтому до нажатия их в разметке нет.
func TestConnectableNotSentInVain(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.Store().CreateTask(&store.Task{Title: "Своя задача",
		Schedule: "раз в час", Prompt: "что-нибудь"}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:mail~work", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dumpScreen(doc), "mailtasks-off-") {
		t.Error("список «Можно подключить» уехал к человеку, хотя он его не просил")
	}
	по, err := s.buildScreen("settings:mail~work", "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dumpScreen(по), "mailtasks-off-") {
		t.Error("по «Показать ещё» список так и не появился")
	}
}

// Счётчик ролей и число строк ролей обязаны сойтись. 11 сентября 2026
// экран объявлял «10 ролей» над девятью строками: список отбрасывал
// эмбеддера, а подпись — нет. Эмбеддер не роль, и считать его нельзя
// нигде: ни в подписи экрана, ни на «Главном».
func TestRoleCounterMatchesRows(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings:models", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	строк := 0
	var walk func([]Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			// роль — строка таблицы с тремя списками в ячейках и «последним ответом»
			if c.Type == "row" && len(c.Cells) == 4 && c.Cells[0].Select != nil {
				строк++
			}
			walk(c.Children)
		}
	}
	walk(doc.Components)
	if строк == 0 {
		t.Fatal("строк ролей нет вовсе")
	}
	want := fmt.Sprintf("%d ролей", строк)
	if !strings.Contains(doc.Subheading, want) && !strings.Contains(doc.Subheading, fmt.Sprintf("%d роли", строк)) {
		t.Errorf("подпись «%s» не сходится с %d строками ролей", doc.Subheading, строк)
	}
	// и на «Главном» та же мерка
	главное, err := s.buildScreen("settings", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dumpScreen(главное), fmt.Sprintf("из %d", строк+1)) {
		t.Errorf("на «Главном» ролей на одну больше — считают эмбеддера")
	}
}

// Общая вкладка не должна нести в адресе ящик. «Спам» правит настройки
// всех ящиков сразу, и адрес «settings:mail@owner@example.org~spam»
// врёт, что порог меняется одному из них. Такую ссылку ещё и
// пересылают.
func TestCommonTabAddrHasNoAccount(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings:mail~spam", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ActiveSubtab != "settings:mail~spam" {
		t.Errorf("выбранная вкладка «%s» вместо канонической", doc.ActiveSubtab)
	}
	for _, в := range doc.Subtabs {
		if strings.HasSuffix(в.ID, "~spam") && strings.Contains(в.ID, "@") {
			t.Errorf("в адресе общей вкладки остался ящик: %s", в.ID)
		}
	}
	// у ящика вкладок «Спам» и «Общее» нет: они у раздела. Ссылка на них — в
	// «···» шапки ящика, и ведёт она на канонический адрес без ящика
	сЯщика, err := s.buildScreen("settings:mail@проба~folders", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, в := range сЯщика.Subtabs {
		if strings.HasSuffix(в.ID, "~spam") || strings.HasSuffix(в.ID, "~common") {
			t.Errorf("среди вкладок ящика осталась общая: %s", в.ID)
		}
	}
	нашли := false
	walkDoc(сЯщика, func(c *Component) {
		for _, a := range c.Menu {
			if a.Action == "$goto" && a.Target == "settings:mail~spam" {
				нашли = true
			}
		}
	})
	if !нашли {
		t.Error("со страницы ящика «Спам» ведёт не на канонический адрес")
	}
	// старый адрес «Спама» с ящиком в хвосте открывает ту же общую вкладку
	старый, err := s.buildScreen("settings:mail@проба~spam", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if старый.ActiveSubtab != "settings:mail~spam" {
		t.Errorf("прежний адрес «Спама» с ящиком открыл вкладку %q", старый.ActiveSubtab)
	}
}

// В меню один пункт «Настройки» и ни одного подпункта. Три уровня
// навигации на одном экране — дерево слева, пять вкладок сверху и
// список разделов рядом — спорили друг с другом, и человек не понимал,
// где стоит.
func TestMenuHasOneSettingsItem(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var пункт *Screen
	for i := range doc.Screens {
		if doc.Screens[i].ID == "settings" {
			пункт = &doc.Screens[i]
		}
	}
	if пункт == nil {
		t.Fatal("пункта «Настройки» нет в меню")
	}
	if len(пункт.Children) != 0 {
		t.Errorf("в меню снова дерево настроек: %d подпунктов", len(пункт.Children))
	}
	// но поиск по меню обязан находить настройку по её словам: дерево
	// убрали, а слова, которыми человек её ищет, терять нельзя
	for _, слово := range []string{"пуши", "ключи", "ящик", "наружу"} {
		if !strings.Contains(пункт.Words, слово) {
			t.Errorf("поиск по меню перестал находить «%s»", слово)
		}
	}
}

// У замечания на «Главном» должна быть видимая команда, а не молчаливый
// клик по строке. Переход в строке превращался в клик по всей строке, и
// кнопка исчезала совсем: «Роль „Чаты“ не отвечает» выглядела обычной
// строкой, и человек не знал, что с ней можно что-то сделать.
func TestFindingShowsCommand(t *testing.T) {
	s, _ := testServer(t)
	// беда, которая есть на пустой базе: ролям не назначены модели
	doc, err := s.buildScreen("settings", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var беды []Component
	var walk func([]Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			if strings.HasPrefix(c.ID, "беда-") {
				беды = append(беды, c)
			}
			walk(c.Children)
		}
	}
	walk(doc.Components)
	if len(беды) == 0 {
		t.Skip("на пустой базе замечаний не набралось")
	}
	for _, б := range беды {
		if len(б.Actions) != 1 {
			t.Errorf("у замечания «%s» действий %d, а нужно ровно одно", б.Subtitle, len(б.Actions))
			continue
		}
		д := б.Actions[0]
		if д.Style != "primary" {
			t.Errorf("команда замечания «%s» не главная: %q", б.Subtitle, д.Style)
		}
		if !strings.HasSuffix(д.Label, "→") {
			t.Errorf("команда «%s» не показывает, что уводит на экран настройки", д.Label)
		}
	}
	// и в разметке она остаётся кнопкой, а не растворяется в строке
	var кнопок int
	for _, б := range беды {
		блок := toBlock(б)
		кнопок += len(блок.Buttons)
	}
	if кнопок < len(беды) {
		t.Errorf("кнопок %d на %d замечаний — переход съел команду", кнопок, len(беды))
	}
}

// Доступ без проверки личности нельзя красить зелёным и нельзя называть
// «доступом для людей»: открытая сеть не отличает человека от
// программы и никого ни о чём не спрашивает. «Работает как задумано» и
// «безопасно» — разные утверждения.
func TestOpenNetworkIsNotGreen(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings:access~network", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var итог *Component
	var walk func([]Component)
	walk = func(cs []Component) {
		for i := range cs {
			if cs[i].ID == "сеть-итог" {
				итог = &cs[i]
			}
			walk(cs[i].Children)
		}
	}
	walk(doc.Components)
	if итог == nil {
		t.Fatal("итога по доверенной сети нет")
	}
	// на пустой базе вход без проверки открыт по умолчанию
	if итог.Level == LevelOK {
		t.Errorf("открытый доступ помечен зелёным: %q · %q", итог.Subtitle, итог.Caption)
	}
	if !strings.Contains(итог.Subtitle, "без проверки личности") {
		t.Errorf("итог не говорит, что личность не проверяется: %q", итог.Subtitle)
	}
	dump := dumpScreen(doc)
	if strings.Contains(dump, "для людей, а не программ") {
		t.Error("вернулась неправда про «людей, а не программ»")
	}
}

// Ярлык Gmail в качестве папки архива — это молчащая поломка: перенос в
// него ничего не делает, «Входящие» не пустеют, и человек думает, что
// Сёма не работает. Подсказка при выборе тут не помогает: её читают
// один раз, а беда живёт месяцами.
func TestLabelInsteadOfArchiveIsTrouble(t *testing.T) {
	s, _ := testServer(t)
	const ящик = "проба" // ящик из testServer: у неизвестного экрана ящика нет
	c := &store.MailConf{Folders: []string{"INBOX", "Archive", "[Gmail]/Вся почта"},
		Archive: "[Gmail]/Вся почта", Junk: "Spam"}
	if err := s.eng.SaveMailConf(ящик, c); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:mail@"+ящик+"~folders", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "ярлык, а не папка") {
		t.Errorf("про неработающий архив молчим:\n%s", clipTest(dump))
	}
	if !strings.Contains(dump, "Выбрать рабочую папку") {
		t.Error("у беды нет действия — человеку некуда нажать")
	}
	// а с настоящей папкой замечания быть не должно
	c.Archive = "Archive"
	if err := s.eng.SaveMailConf(ящик, c); err != nil {
		t.Fatal(err)
	}
	doc2, err := s.buildScreen("settings:mail@"+ящик+"~folders", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dumpScreen(doc2), "ярлык, а не папка") {
		t.Error("выдумали беду на рабочей папке")
	}
}

// Стирание обычной переписки сохраняется своим действием: тумблер,
// удаляющий вашу почту с сервера, не должен уезжать вместе с числом
// дней общей формы. Живёт оно в «Опасном» вкладки «Общее».
func TestDangerousPurgeHasOwnButton(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings:mail~common", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var опасная, обычная *Component
	var walk func([]Component)
	walk = func(cs []Component) {
		for i := range cs {
			switch cs[i].ID {
			case "опасное-хранение":
				опасная = &cs[i]
			case "общее-хранение":
				обычная = &cs[i]
			}
			walk(cs[i].Children)
		}
	}
	walk(doc.Components)
	if опасная == nil || обычная == nil {
		t.Fatal("форм хранения на вкладке нет")
	}
	if опасная.Submit.Action == обычная.Submit.Action {
		t.Errorf("обе формы шлют одно действие %q", опасная.Submit.Action)
	}
	for _, f := range обычная.Fields {
		if f.Name == "purge_other" {
			t.Error("тумблер стирания переписки остался в общей форме")
		}
	}
	// и сказано, что именно произойдёт
	if !strings.Contains(dumpScreen(doc), "в почте") {
		t.Error("не сказано, что писем не будет в самой почте")
	}
}

// Поиск по настройкам обещан единым, а жил только на «Главном».
// Человек ищет «пуши» или «срок хранения» оттуда, где стоит.
func TestSettingsSearchFromAnySection(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range []string{"settings:models", "settings:mail~folders", "settings:access~network"} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !doc.Searchable {
			t.Errorf("%s: поля поиска нет", screen)
		}
		// и набранное ищет по всем разделам, а не по строкам этого экрана
		нашли, err := s.buildScreen(screen, "", "пуши", 0)
		if err != nil {
			t.Fatal(err)
		}
		dump := dumpScreen(нашли)
		if !strings.Contains(dump, "Уведомления") {
			t.Errorf("%s: поиск «пуши» не нашёл «Уведомления»:\n%s", screen, clipTest(dump))
		}
	}
}
