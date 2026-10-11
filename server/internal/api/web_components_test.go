package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Обратный путь поднимается из ленты в шапку: строка «Настройки › Почта»
// стоит над заголовком, а не блоком посреди списка. Иначе на телефоне,
// где меню нет вовсе, у пятнадцати вкладок настроек один и тот же
// заголовок и непонятно, где ты стоишь.
func TestPathRisesToHeader(t *testing.T) {
	doc := &Document{Heading: "Настройки", Components: []Component{
		{Type: "crumb", Title: "Почта", Actions: []Action{
			{Action: "$goto", Target: "settings", Label: "Настройки"},
		}},
		{Type: "text", Text: "тело экрана"},
	}}
	э := toScreen(doc)
	if э.Here != "Почта" || len(э.Breadcrumb) != 1 {
		t.Fatalf("путь не поднялся: где %q, звеньев %d", э.Here, len(э.Breadcrumb))
	}
	for _, б := range э.Blocks {
		if б.Kind == "путь" {
			t.Fatal("путь остался ещё и блоком ленты")
		}
	}
	html, err := web.Render(э)
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if i, j := strings.Index(s, `class="crumbs"`), strings.Index(s, "<h1"); i < 0 || i > j { // у заголовка теперь tabindex
		t.Errorf("путь не над заголовком: %d и %d", i, j)
	}
}

// Ряд капсул обрезается на сервере и говорит, сколько осталось: клиент
// на htmx считать влезающее не умеет, а восемьдесят образцов круга
// чтения занимали семь строк экрана.
func TestPillRowTrimmedWithTail(t *testing.T) {
	c := Component{Type: "chipset", Title: "Круг чтения"}
	for i := 0; i < pillsPerRow+7; i++ {
		c.Badges = append(c.Badges, Badge{Text: fmt.Sprintf("образец %d", i)})
	}
	б := toBlock(c)
	if len(б.Pills) != pillsPerRow {
		t.Fatalf("в ряду осталось %d капсул", len(б.Pills))
	}
	if б.More != "ещё 7" {
		t.Fatalf("хвост ряда: %q", б.More)
	}
	// короткий ряд хвоста не получает: «ещё 0» было бы неправдой
	if б := toBlock(Component{Type: "chipset", Badges: []Badge{{Text: "df"}}}); б.More != "" {
		t.Fatalf("короткий ряд получил хвост %q", б.More)
	}
}

// Ячейка таблицы: число узнаётся само, остальное машинное помечает тот,
// кто собирает экран. Фраза моноширинной не становится — иначе колонка
// «закрыто / открыто» читается как счёт.
func TestTableCellMonospaced(t *testing.T) {
	б := toBlock(Component{Type: "row", Cells: []Cell{
		{Text: "128"},
		{Text: "закрыто"},
		{Text: "127.0.0.1:1234", Mono: true},
	}})
	want := []bool{true, false, true}
	for i, кл := range б.Cells {
		if кл.Mono != want[i] {
			t.Errorf("ячейка %q: моно %v, ждали %v", кл.Text, кл.Mono, want[i])
		}
	}
}

// Найденное в сети — данные, а не указание: адрес уходит ссылкой в тело
// карточки дела, а не кнопкой в ряд действий. Кнопка рядом с «Выполнить»
// читается как поручение Сёме, хотя это всего лишь адрес, куда можно
// сходить самому. В обычной строке `$open` остаётся кнопкой.
func TestPendingCardLinkIsNotButton(t *testing.T) {
	б := toBlock(Component{Type: "toolcard", Title: "Нашлось в сети", Icon: "network",
		Actions: []Action{
			{Action: "$open", Target: "https://docs.docker.com/network/", Label: "docs.docker.com"},
			{Action: "search_again", Target: "12", Label: "Поискать ещё"},
		}})
	if len(б.Links) != 1 || б.Links[0].Target != "https://docs.docker.com/network/" {
		t.Fatalf("ссылок в теле карточки %d", len(б.Links))
	}
	if len(б.Buttons) != 1 || б.Buttons[0].Name != "Поискать ещё" {
		t.Fatalf("кнопки карточки: %v", б.Buttons)
	}
	if б.RowIcon != "network" {
		t.Errorf("значок дела: %q", б.RowIcon)
	}
	// в обычной строке ссылка остаётся кнопкой: там нет тела карточки
	стр := toBlock(Component{Type: "row", Subtitle: "Страница цен",
		Actions: []Action{{Action: "$open", Target: "https://api-docs.deepseek.com/", Label: "Открыть"}}})
	if len(стр.Links) != 0 || len(стр.Buttons) != 1 {
		t.Errorf("в строке ссылка перестала быть кнопкой: %d и %d", len(стр.Links), len(стр.Buttons))
	}
}

// Новые типы ленты и письма доезжают до разметки видом, а не «неизвестным
// компонентом»: тип без ветки в переводе рисуется строкой с ошибкой.
func TestFeedAndMessageTypesAreTranslated(t *testing.T) {
	want := map[string]string{
		"thinking":    "ожидание",
		"toolcard":    "карточка-дела",
		"chain":       "цепочка",
		"classpicker": "класс",
		"switcher":    "переключатель",
	}
	for тип, kind := range want {
		if б := toBlock(Component{Type: тип, Title: "проба"}); б.Kind != kind {
			t.Errorf("%s перевёлся в %q, ждали %q", тип, б.Kind, kind)
		}
	}
}

// Словарь клиента знает новые типы: тип на сервере без строки в
// dictionary.txt — пустое место у человека, а не ошибка в журнале.
func TestNewTypesPresentInClientDictionary(t *testing.T) {
	умеет, _ := dictionaryTypes(web.Dictionary())
	for _, тип := range []string{"thinking", "toolcard", "chain", "classpicker", "switcher"} {
		if !умеет[тип] {
			t.Errorf("клиент не объявил тип %q", тип)
		}
	}
}

// В карточке дела и в цепочке переход остаётся кнопкой: кликается не весь
// блок, а действие. Без этого «Открыть скрипт» и «Ещё N писем» пропадали
// молча — действие есть, а нажать нечего.
func TestGotoInPendingCardStaysButton(t *testing.T) {
	дело := toBlock(Component{Type: "toolcard", Title: "Скрипт",
		Actions: []Action{{Action: "$goto", Target: "script:17", Label: "Открыть скрипт"}}})
	if len(дело.Buttons) != 1 || дело.Buttons[0].Name != "Открыть скрипт" {
		t.Fatalf("в карточке дела переход не стал кнопкой: %+v", дело.Buttons)
	}
	if дело.GoTo != "" {
		t.Error("карточка дела не должна кликаться целиком")
	}
	// у обычной строки поведение прежнее: кликается вся строка
	строка := toBlock(Component{Type: "row", Subtitle: "Письмо",
		Actions: []Action{{Action: "$goto", Target: "msg:1", Label: "Открыть"}}})
	if строка.GoTo != "msg:1" || len(строка.Buttons) != 0 {
		t.Fatalf("у строки переход должен оставаться переходом: %+v", строка)
	}
}

// Меню — следствие адреса экрана, а не состояние, которое надо помнить.
// Раскрыт тот узел, внутри которого стоит человек; свёрнутый показывает
// счётчик и уровень своих детей, иначе ждущее письмо в закрытой ветке
// становится невидимым.
func TestMenuExpandsNodeYouStandIn(t *testing.T) {
	меню := Screen{ID: "settings", Title: "Настройки", Group: "Своё", Children: []Screen{
		{Title: "Каналы", Children: []Screen{
			{ID: "settings:mail", Title: "Почта", Badge: "2", Level: LevelWarn},
			{ID: "settings:telegram", Title: "Telegram"},
		}},
		{Title: "Что Сёма умеет", Children: []Screen{
			{ID: "settings:models", Title: "Модели и роли"},
		}},
	}}

	// стоим в «Моделях»: раскрыты «Настройки» и «Что Сёма умеет»
	п := toMenuItem(меню, "settings:models", "")
	if !п.Open {
		t.Fatal("раздел, внутри которого стоим, свёрнут")
	}
	каналы, умеет := п.Children[0], п.Children[1]
	if каналы.Open {
		t.Error("чужая группа раскрыта")
	}
	if !умеет.Open {
		t.Error("группа, где стоим, свёрнута")
	}
	// свёрнутые «Каналы» показывают, что внутри кто-то ждёт
	if каналы.Count != "2" || каналы.Level != LevelWarn {
		t.Errorf("свёрнутый узел молчит о детях: счётчик %q, уровень %q", каналы.Count, каналы.Level)
	}
	// стоим в почте: раскрыты «Каналы», а не «Что Сёма умеет»
	п = toMenuItem(меню, "settings:mail", "")
	if !п.Children[0].Open || п.Children[1].Open {
		t.Error("раскрыта не та группа")
	}
	// раскрытая группа своё не суммирует: счётчики видны у самих пунктов
	if п.Children[0].Count != "" {
		t.Errorf("раскрытый узел дублирует счётчик детей: %q", п.Children[0].Count)
	}
}
