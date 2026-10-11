package web

// Разметка экрана из того же документа, который уходит клиенту на маке.
// Сборка экрана одна (`api.buildScreen`), представлений два: JSON для
// SwiftUI и HTML для браузера. Так два клиента не могут разойтись в
// понимании словаря — расходиться нечему, оба берут один документ.
//
// Взаимодействие — на htmx: клик по строке, отправка формы и опрос
// приходят обычными запросами, а сервер отвечает готовым куском разметки.
// Своего JavaScript почти нет.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strings"
)

// Screen — то, что нужно разметке от документа. Пакет `web` не знает типов
// пакета `api` (иначе вышло бы кольцо), поэтому договор описан здесь, а
// api складывает документ в эти же поля.
type Screen struct {
	Title      string
	Subheading string
	Width      string   // read — узкая колонка для чтения
	Addr       string   // текущий адрес экрана
	Breadcrumb []Button // обратный путь над заголовком: «‹ Настройки · Почта»
	Here       string   // где стоим — последнее звено пути, без ссылки
	Items      []Item
	Panel      []Item
	PanelTitle string
	// Вид панели: пусто — список чатов или скриптов; sections — колонка
	// разделов настроек: поле поиска первой строкой (когда на экране есть
	// поиск), заголовки групп (Kind == "heading"), счётчик или точка
	// справа от названия. На телефоне такую колонку сервер не присылает
	// вовсе: корень настроек там рисуется обычными строками
	PanelKind string
	PanelAdd  *Button
	Active    string
	Tabs      []Chip
	Tab       string
	// Подвкладки — внутренние вкладки раздела настроек: «Почта» делится
	// на шесть, «Модели» на четыре. Своя полоса под шапкой, а не второй
	// ряд к пяти постоянным: это разные уровни, и смешать их значит
	// сказать, что «Спам» и «Безопасность» — соседи
	Subtabs []Chip
	Subtab  string
	Filters []Chip
	Filter  string
	// Подсветить — метка строки, к которой клиент прокрутит экран после перехода
	// и которую подсветит на пару секунд («f:форма-поле», «r:строка», «s:раздел»);
	// метку несёт `data-hl` у самого элемента. Ложится атрибутом на `#screen`, а
	// не в ленту: перечитывание ленты опросом её не трогает, и подсветка не
	// возвращается
	Highlight    string
	Search       bool
	SearchHint   string // что написано в пустом поле поиска; пусто — «Поиск»
	SearchQuery  string // что набрано: возвращаем в поле, иначе оно стирается при подмене
	Wizard       *Wizard
	Buttons      []Button
	MenuButtons  []Button
	Selects      []Select
	Blocks       []Block
	Banner       string
	Composer     *Composer
	PollSec      int // как часто перечитывать экран без провода, с
	PollSecWired int // и при живом проводе — редко, как страховка
	// Prefetch — приставки экранов, которые можно подгрузить заранее при
	// наведении (через пробел); PrefetchMs — сколько держать такой ответ
	// (perf-pass 7)
	Prefetch   string
	PrefetchMs int
	// Сохранение — на экране есть формы, которые сохраняет нижняя полоса.
	// Полоса живёт вне ленты: перечитывание экрана подменяет только
	// ленту, а несохранённое человека — не содержимое ленты
	SaveBar bool
	Footer  []FooterLine
	// Хозяин — чей это Сёма: строка в шапке меню. Полоса — нижние
	// вкладки телефона; на широком экране их не видно, там рельс слева
	Owner string
	// Ждут — число ждущих уведомлений для значка приложения
	Waiting string
	// Наборы — общие наборы вариантов: имя набора → варианты. Клиент
	// кладёт их в страницу один раз и берёт оттуда, открывая список.
	OptionSets map[string][]string
	TabBar     []Item
	Limit      int // сколько строк показано: «Показать ещё» просит вдвое больше
	// ---- снимок экрана на устройстве (offline-snapshot, группа 4) ----
	//
	// Решение принимает сервер, а не браузер: клиент не знает ни про
	// закрытые ящики, ни про класс «секретное». Признаки едут в разметку
	// атрибутами у `#screen`, потому что читают их двое — страница и
	// служебный работник, — и оба видят одну и ту же разметку. Отсутствие
	// атрибута читается как запрет: молчание сервера не разрешение.
	//
	// Хранимый — экран можно положить в снимок (Document.Storable).
	Storable bool
	// СнимокОтличается — снимочная сборка этого экрана не равна показанной
	// (в показанной есть письма закрытых ящиков). Тогда хранить надо не то,
	// что видит человек, а второй, снимочный ответ (`?snapshot=1`).
	SnapshotDiffers bool
	// СвоёУстройство — это устройство отмечено своим на «Доступе». Без
	// отметки не хранится ничего, даже хранимое.
	OwnDevice bool
	// Словарь — версия словаря UI-DSL. Входит в ключ снимка: разметка от
	// прежнего словаря нарисовала бы неправду, поэтому снимок не
	// показывается и стирается, а не «показывается как устаревший».
	Dictionary int
	// СнимокДней — срок жизни снимка, `ui.snapshot_days`.
	SnapshotDays int
}

type Item struct {
	Pinned    bool // блок «ждут вас» над меню, а не строка в списке
	Addr      string
	Name      string
	Icon      string // имя SF Symbol; рисуется своим набором (icons.go)
	Small     string
	Level     string
	Count     string
	Nested    bool
	Depth     int
	Kind      string // container | thread
	Open      bool
	Group     string
	Selected  bool
	Delete    *Button
	Separated bool
	// Дети — подпункты меню: ящики у почты, группы у настроек, пункты
	// внутри группы. Раскрыт тот узел, в котором человек сейчас стоит;
	// остальные свёрнуты, и меню не растёт до сорока строк
	Children []Item
	// Внутри — адрес контейнера, которому принадлежит пункт панели:
	// свернули контейнер — его дети скрываются, и это решает клиент
	Inside string
	// ВГруппе — имя группы пункта. Свёрнутая группа прячет свои строки
	// одним правилом стилей по этому имени: флаг hidden ставили два
	// независимых кода и перетирали друг другу
	InGroup string
	IsNode  bool // строка с детьми: у неё шеврон и она раскрывается
	// Ключ — постоянное имя узла для id и памяти раскрытия. Адрес узла
	// гасится, когда первый ребёнок ведёт туда же («Все ящики», «Все
	// разговоры»), и id из адреса выходил пустым у двух узлов сразу:
	// клиент путал им раскрытие
	Key string
	// Помнить — раскрытие узла решает человек, а клиент помнит его
	// решение между переходами. Иначе сервер разворачивал бы свёрнутое
	// рукой при каждом нажатии, и меню прыгало бы уже от этого
	Remember bool
	// вид значка: оттенок раздела, плитка с белым знаком или инициалы
	Tint     string
	Tile     bool
	Initials string
	// Черта — разделитель над пунктом
	Divider bool
	// ЗначокГруппы — значок у подписи группы; Раздел — к какой подписи
	// относится пункт: по нему клиент сворачивает раздел целиком
	GroupIcon string
	Section   string
	// Плюс — создать новое прямо из строки пункта
	Add *Button
	// Слова — по чему этот пункт находится поиском по меню. Отдаём их
	// клиенту атрибутом: искать он должен без похода на сервер, иначе
	// каждая набранная буква — запрос
	Words string
}

// FooterLine — строка состояния под меню: текст и, если это ход
// работы вида «1 240 из 6 716», доля для полосы.
type FooterLine struct {
	Text     string
	Fraction float64 // 0 — полосы нет
	Dot      string  // уровень для точки состояния
}

type Chip struct {
	Addr  string
	Name  string
	Count string // число у имени: «Папки и хранение 5»
	Level string // ok | info | warn | alert — цвет счётчика
}

type Button struct {
	Action    string // серверное имя действия либо клиентское: $goto, $file, $more, $copy, $copy_field, $open
	Target    string
	Name      string
	Kind      string // primary | destructive | link
	Prompt    string // текст вопроса перед отправкой
	Confirm   string
	Masked    bool
	Value     string
	ConfirmOK string // надпись на кнопке подтверждения
	// Откладываемое — действие можно поставить в очередь и отправить, когда
	// связь появится (Action.Deferrable). Таких ровно два — отправка
	// сообщения в чат и прикрепление файла; всё остальное без связи не
	// откладывается и в снимке не показывается вовсе.
	Deferrable bool
}

type Select struct {
	Name    string
	Label   string // подпись рядом со списком; пусто — список сам по себе
	Value   string
	Options []string
	Action  Button
	// Вслух — как список зовётся для чтения вслух. Подпись у списка
	// короткая («Основная»), потому что она стоит в строке роли и
	// смысл берёт у соседей. Скринридеру соседей не видно: он читает
	// «Основная» девять раз подряд и ни разу не говорит, чьей роли
	// это основная. Здесь имя целиком: «Чаты — основная модель».
	SpokenLabel string
	// Набор — имя общего набора вариантов вместо своего списка. Сам
	// набор лежит у экрана один раз, а список собирается клиентом в тот
	// миг, когда человек его открыл. У «Моделей» это 972 одинаковые
	// строчки вариантов, которых больше нет в разметке.
	OptionSet string
}

// Клетка матрицы выдачи: состояние и кнопка переключения. Она же —
// ячейка таблицы: тогда у неё есть текст, а кнопки может не быть.
type Cell struct {
	Tooltip string
	State   string // on | out | off | na (права у такого чата не бывает: не нажимается, причина — в подсказке)
	// Наследует — значение не своё, а чужое по умолчанию: у темы группы это
	// право группы. Клетка при этом нажимается — нажатие делает право своим
	Inherited bool
	Text      string // содержимое ячейки таблицы
	Level     string // ok | info | warn | alert — цвет значения
	Mono      bool   // машинный факт: моноширинным и по правому краю
	Button    Button
	// Список — выпадающий список прямо в ячейке таблицы: роль меняет модель
	// там же, где её видно, а не на отдельном экране. Применяется сразу, как
	// список в строке
	Select *Select
}

// Node — компонент экрана. Вид повторяет типы словаря: строка, раздел,
// пузырь, форма, кнопки, карточки, заголовок.
// Узел и Связь — граф памяти. Раскладка считается на сервере: у клиента
// на htmx своего JavaScript почти нет, а силовая раскладка в браузере
// потребовала бы библиотеки — за неё платить нечем.
type Node struct {
	ID       string
	Name     string
	Area     string
	X, Y     float64
	Radius   float64
	Color    string
	Dim      bool
	Selected bool
	Addr     string
}

type Edge struct {
	X1, Y1 float64
	X2, Y2 float64
	Label  string
}

type Graph struct {
	Width, Height float64
	Nodes         []Node
	Edges         []Edge
	Total         int // сколько узлов всего: показываем не все
	Legend        []AreaLegend
}

// AreaLegend — что значит цвет узла. Без легенды цвет ничего не
// сообщает: человек видит синие и оранжевые кружки и гадает.
type AreaLegend struct {
	Name  string
	Color string
}

type Block struct {
	Kind         string
	ID           string
	Text         string
	Title        string
	Name         string
	Caption      string
	Meta         string
	MetaInline   bool
	Selectable   bool     // строка отмечается флажком для группового действия
	Unit         string   // мелким рядом со значением показателя
	Columns      []string // подписи колонок матрицы
	RightAligned []bool   // колонки таблицы, чьи значения стоят по правому краю
	Cells        []Cell   // клетки строки матрицы
	Role         string
	Level        string
	Style        string
	Collapsed    bool
	Open         bool
	// Раскрытие — подпись «Подробнее» у строки с раскрытием, когда внутри не
	// пояснение, а настройки: «Настройки». Пусто — «Подробнее»
	Expand string
	// Часть — адрес содержимого, которое приедет при раскрытии; пусто —
	// дети уже здесь. ПоискЧасти — подсказка поля поиска внутри
	Fragment       string
	FragmentSearch string
	// Отложена — форму сохраняет нижняя полоса экрана: своей кнопки у неё
	// нет, а действие лежит скрытым полем, чтобы полоса отправила форму
	// ровно тем же путём, каким её отправляла бы кнопка
	Deferred bool
	Toggle   string
	Pills    []Pill
	More     string // хвост ряда капсул: «ещё 74»
	Facts    []Prop
	Props    []Prop
	Commands []Command
	Files    []File
	Buttons  []Button
	// Ссылки — адреса в теле карточки дела: найденное в сети нечем
	// проверить без них, а кнопкой ссылка выглядит как поручение
	Links       []Button
	Menu        []Button
	Selects     []Select
	Fields      []Field
	Children    []Block
	Output      string
	RowIcon     string // имя SF Symbol: рисуется набором, а не первой буквой
	Avatar      string // инициал в кружке слева
	AvatarColor string
	AvatarKind  string // circle | file
	Tone        string // подсветка строки: red | blue | green
	Wide        bool   // значки и кнопки по правому краю
	GoTo        string // экран, который открывает клик по строке ($goto)
	FileID      int64  // файл, который открывает клик по строке ($file)
	// Картинка — адрес картинки строки: показывается как картинка, а не
	// рамкой. Рамка для картинки не годится — файл отдаётся с запретом
	// встраивания
	ImageURL string
	// Рамка — адрес чужого документа (тело письма), который показывается
	// в песочнице. Разметка письма не должна жить в адресе Сёмы: её CSS
	// перекрасил бы интерфейс, а её ссылки вели бы по путям Сёмы
	FrameURL      string
	BlockedImages int    // внешних картинок в рамке не загружено
	Translation   string // «на «Русский»» — если у письма есть перевод
	Graph         *Graph
	Fraction      *float64
	CountdownTo   string
	Sparkline     string // точки мини-графика (SVG polyline), пусто — графика нет
	// Управление — у строк матрицы есть своё меню «···» (у темы группы это
	// «Как у группы»): тогда у таблицы последняя колонка под него
	Controls bool
}

type Pill struct {
	Text  string
	Level string
	Color string
}

type Prop struct {
	Label string
	Value string
	Color string
}

type Command struct {
	Text    string
	Note    string
	Buttons []Button
}

type File struct {
	ID      int64
	Name    string
	Meta    string
	IsImage bool
	Path    string
}

type Field struct {
	Kind string
	Name string
	// Опора — id элемента, на который смотрит <label for>. Имя поля для
	// этого не годится: две формы на одном экране законно держат поля с
	// одним именем («Стирать обычные письма» у ящика и у всех сразу), и
	// тогда подпись одной формы переключала бы поле другой
	AnchorID string
	Label    string
	Hint     string
	Value    string
	Options  []string
	// Тона — цвет выбранного варианта сегментного переключателя по
	// порядку вариантов: ok, warn, alert. Цвет только у выбранного: три
	// раскрашенных кнопки сразу кричали бы, а человеку нужно видеть ответ
	Tones []string
	// Disabled — варианты, которые сейчас выбрать нельзя («полная»
	// самостоятельность машине, где нет суда): вариант виден, но серый, а
	// причину говорит сервер рядом. Выбранный вариант не отключается
	Disabled []string
	// More — поле редкое: форма прячет его под «Ещё N полей» и отправляет
	// вместе со всеми остальными
	More      bool
	Unit      string
	Immediate bool
	Min       float64
	Max       float64
	Step      float64
	// Default — то, что настройка значит, пока её не трогали. Есть оно
	// (HasDefault), и строка настройки показывает «по умолчанию»; NotDefault —
	// значение с ним расходится, и кнопка видна сразу, а не после первой правки
	Default    string
	HasDefault bool
	NotDefault bool
}

// Wizard — пошаговая форма (новая задача, новый ящик). В вебе шаги идут
// один под другим: листать нечем, а видеть всё сразу удобнее.
type Wizard struct {
	Title  string
	Steps  []Step
	Submit Button
	Cancel Button
}

type Step struct {
	Title  string
	Hint   string
	Fields []Field
}

type Composer struct {
	Placeholder string
	FieldName   string
	Submit      Button
	To          string
	CallWord    string
	Attach      *Button
}

var tmpl = template.Must(template.New("screen").Funcs(template.FuncMap{
	"attrs":          attrs,
	"icon":           Icon,
	"hasIcon":        HasIcon,
	"percent":        func(v *float64) string { return fmt.Sprintf("%.0f%%", *v) },
	"ratio":          func(v float64) string { return fmt.Sprintf("%.0f%%", v*100) },
	"firstLetter":    firstLetter,
	"nodeLabel":      nodeLabel,
	"add":            func(a, b, c float64) float64 { return a + b + c },
	"inc":            func(n int) int { return n + 1 },
	"diff":           diffLines,
	"hasDestructive": hasDestructive,
	"optionSets":     optionSetsJSON,
	"toggleNames":    formToggleNames,
	"at":             at,
	"has":            has,
	"chosen":         chosen,
	"timeFrom":       func(v string) string { from, _ := timespanParts(v); return from },
	"timeTo":         func(v string) string { _, to := timespanParts(v); return to },
	"plainFields":    plainFields,
	"moreFields":     moreFields,
	"moreWord":       moreWord,
}).Parse(markupTemplate))

// has — есть ли строка в списке. Нужен шаблону, чтобы понять, отключён ли
// вариант сегментного переключателя.
func has(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// chosen — выбран ли вариант в поле «checks»: значение — выбранные варианты
// через запятую (запятых внутри вариантов нет).
func chosen(value, opt string) bool {
	for _, v := range strings.Split(value, ",") {
		if strings.TrimSpace(v) == opt {
			return true
		}
	}
	return false
}

// timespanParts — «02:00-06:00» на два времени. Пустое значение — «круглые
// сутки»: оба поля пусты. Часы без нуля («2:00») браузерное поле времени не
// принимает, поэтому добиваем до двух знаков.
func timespanParts(v string) (from, to string) {
	a, b, ok := strings.Cut(strings.TrimSpace(v), "-")
	if !ok {
		return "", ""
	}
	pad := func(s string) string {
		s = strings.TrimSpace(s)
		if h, m, ok := strings.Cut(s, ":"); ok && len(h) == 1 {
			return "0" + h + ":" + m
		}
		return s
	}
	return pad(a), pad(b)
}

// plainFields и moreFields делят поля формы на обычные и спрятанные под
// «Ещё N полей»: сами поля остаются в той же форме и уходят вместе с ней.
func plainFields(fields []Field) []Field {
	var out []Field
	for _, f := range fields {
		if !f.More {
			out = append(out, f)
		}
	}
	return out
}

func moreFields(fields []Field) []Field {
	var out []Field
	for _, f := range fields {
		if f.More {
			out = append(out, f)
		}
	}
	return out
}

// moreWord — «2 поля», «5 полей», «1 поле».
func moreWord(n int) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod10 == 1 && mod100 != 11:
		return fmt.Sprintf("%d поле", n)
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return fmt.Sprintf("%d поля", n)
	}
	return fmt.Sprintf("%d полей", n)
}

// at — элемент списка по номеру; за краем — пусто. Нужен шаблону, чтобы
// взять цвет варианта по его порядку.
func at(list []string, i int) string {
	if i < 0 || i >= len(list) {
		return ""
	}
	return list[i]
}

// formToggleNames — имена тумблеров формы через запятую.
//
// Невыбранный флажок браузер не отправляет вовсе, и обработчик, который
// различает «выключено» и «этой настройки не присылали» (`save_automation`
// — четыре формы на разных вкладках шлют каждая свои поля), выключить
// тумблер не мог: поля просто не было, и он читал это как «не трогали».
// Сервер давно умеет принимать список тумблеров формы (`toggles`), но
// присылал его только снятый клиент на маке — веб не присылал никогда,
// и «Заходить в спам и корзину почтового сервера» не выключалось молча.
func formToggleNames(fields []Field) string {
	var names []string
	for _, f := range fields {
		switch f.Kind {
		case "toggle":
			names = append(names, f.Name)
		case "checks":
			// у каждого чипа свой флажок «имя.вариант»: снятый не придёт вовсе
			for _, opt := range f.Options {
				names = append(names, f.Name+"."+opt)
			}
		}
	}
	return strings.Join(names, ",")
}

// optionSetsJSON — общие наборы вариантов в тег <script type="application/json">.
// Внутри такого тега браузер не разбирает разметку, но строку «</script>»
// он ищет — экранируем её, иначе имя модели со слэшем закрыло бы тег и
// развалило страницу.
func optionSetsJSON(m map[string][]string) template.JS {
	b, err := json.Marshal(m)
	if err != nil {
		return template.JS("{}")
	}
	out := strings.ReplaceAll(string(b), "</", `<\/`)
	return template.JS(out)
}

// DiffLine — строка правки скилла: знак решает цвет. Разница
// показывается человеку до согласия, и «+» зелёным, «−» красным читается
// быстрее, чем те же строки одним куском текста.
type DiffLine struct {
	Sign string // plus | minus | пусто
	Text string
}

func diffLines(text string) []DiffLine {
	var out []DiffLine
	for _, line := range strings.Split(text, "\n") {
		sign := ""
		switch {
		case strings.HasPrefix(line, "+"):
			sign = "plus"
		case strings.HasPrefix(line, "-"), strings.HasPrefix(line, "\u2212"):
			sign = "minus"
		}
		out = append(out, DiffLine{Sign: sign, Text: line})
	}
	return out
}

// hasDestructive — есть ли среди кнопок разрушительная. Карточка дела отделяет
// такую кнопку пустотой: «Отклонить» рядом с «Выполнить» нажимается
// сгоряча, а отменить выполненную команду нечем.
func hasDestructive(buttons []Button) bool {
	for _, btn := range buttons {
		if btn.Kind == "destructive" {
			return true
		}
	}
	return false
}

// attrs — атрибуты кнопки для htmx: действие уходит на сервер, ответом приходит
// готовый экран, и htmx подменяет им середину страницы.
func attrs(btn Button) template.HTMLAttr {
	var b strings.Builder
	// клиентские действия живут в браузере, на /do их не шлём. Переход —
	// обычный hx-get; остальное разбирает app.js по data-action
	if strings.HasPrefix(btn.Action, "$") {
		switch btn.Action {
		case "$goto":
			fmt.Fprintf(&b, ` hx-get="/ui?screen=%s" hx-target="#window" hx-swap="innerHTML"`, template.HTMLEscapeString(url.QueryEscape(btn.Target)))
		default:
			fmt.Fprintf(&b, ` data-action="%s" data-target="%s"`, template.HTMLEscapeString(btn.Action), template.HTMLEscapeString(btn.Target))
		}
		return template.HTMLAttr(b.String())
	}
	fmt.Fprintf(&b, ` hx-post="/do" hx-target="#window" hx-swap="innerHTML"`)
	// откладываемое действие клиент узнаёт по признаку от сервера, а не по
	// имени действия и не по виду элемента разметки: имя — предметная
	// область, которой клиент не знает, а вид элемента разошёлся бы с
	// сервером в первый же день, когда откладываемым станет что-то третье
	if btn.Deferrable {
		b.WriteString(` data-deferrable="1"`)
	}
	// групповое действие: цель «$selected» сервер заменяет на номера
	// отмеченных строк, а отметки приходят полем «выбрано»
	if btn.Target == "$selected" {
		b.WriteString(` hx-include=".row-check input:checked"`)
		// признак для клиента: такую кнопку он переносит в полосу выбора,
		// где ей и место — без отмеченных строк нажимать её незачем
		b.WriteString(` data-bulk="1"`)
	}
	// JSON в атрибуте экранируется как HTML: кавычки становятся &#34; и
	// возвращаются браузером как есть. Экранирование «как строка JS» давало
	// {\"action\":…}, htmx 4 разбирал это через JSON.parse и падал молча —
	// ни одна кнопка не порождала запроса
	fmt.Fprintf(&b, ` hx-vals="%s"`, template.HTMLEscapeString(actionValues(btn)))
	if btn.Confirm != "" {
		// своё окно, а не hx-confirm: браузерное окно приходит с чужой
		// шапкой «Подтвердите действие на sema.local:8443», кнопками
		// «Отмена/OK» и своим шрифтом — человек в нём не читает вопрос,
		// а ищет, где согласиться
		fmt.Fprintf(&b, ` data-confirm="%s"`, template.HTMLEscapeString(btn.Confirm))
		if btn.ConfirmOK != "" {
			fmt.Fprintf(&b, ` data-confirm-ok="%s"`, template.HTMLEscapeString(btn.ConfirmOK))
		}
		if btn.Kind == "destructive" {
			b.WriteString(` data-danger="1"`)
		}
	}
	if btn.Prompt != "" {
		// hx-prompt в htmx 4 нет: вопрос задаёт app.js и кладёт ответ в value
		fmt.Fprintf(&b, ` data-prompt="%s"`, template.HTMLEscapeString(btn.Prompt))
		if btn.Masked {
			b.WriteString(` data-masked="1"`)
		}
		if btn.Value != "" {
			fmt.Fprintf(&b, ` data-value="%s"`, template.HTMLEscapeString(btn.Value))
		}
		if btn.ConfirmOK != "" {
			fmt.Fprintf(&b, ` data-confirm-ok="%s"`, template.HTMLEscapeString(btn.ConfirmOK))
		}
	}
	return template.HTMLAttr(b.String())
}

func actionValues(btn Button) string {
	return fmt.Sprintf(`{"action":%q,"target":%q}`, btn.Action, btn.Target)
}

// nodeLabel — подпись узла графа. Длинное имя обрезается: подписи не должны
// наезжать друг на друга, а полное видно при наведении и в списке под
// графом. Регистр остаётся как есть: прежняя подпись делала первую букву
// заглавной, и разные узлы «pi» и «Pi», «sema» и «Sema» рисовались
// одинаково — граф выглядел полным дублей (5 октября 2026).
func nodeLabel(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 14 {
		return string(r[:14]) + "…"
	}
	return string(r)
}

func firstLetter(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "?"
	}
	return strings.ToUpper(string(r[0]))
}

// Разметка экрана: htmx подменяет `#screen` целиком, поэтому шаблон один и
// тот же и для первой отрисовки, и для ответа на действие.
func Render(scr Screen) (template.HTML, error) {
	var b strings.Builder
	if err := tmpl.Execute(&b, scr); err != nil {
		return "", err
	}
	return template.HTML(collapseGaps(b.String())), nil
}

// collapseGaps — пробелы между тегами схлопываются до одного перевода строки.
//
// Шаблон читается человеком, и между {{if}} остаются пустые строки с
// отступами: у одной строки задачи из 6,2 КБ полтора — пустоты. Браузер
// такие пробелы и так считает одним, поэтому смысл разметки не меняется;
// ужимается только то, что стоит между «>» и «<» и содержит перевод
// строки. Содержимое <pre> не задевается: там текст, а не теги.
var gapsBetweenTags = regexp.MustCompile(`>[ \t]*\n[ \t\n]*<`)

func collapseGaps(markup string) string {
	return gapsBetweenTags.ReplaceAllString(markup, ">\n<")
}

// RenderFragment — разметка одних блоков без шапки и меню: так приезжает
// содержимое ленивого раздела. Шаблон тот же «блок», что и у экрана,
// поэтому строка в части выглядит ровно как строка на экране.
func RenderFragment(blocks []Block) (template.HTML, error) {
	var b strings.Builder
	for _, blk := range blocks {
		if err := tmpl.ExecuteTemplate(&b, "block", blk); err != nil {
			return "", err
		}
	}
	return template.HTML(collapseGaps(b.String())), nil
}
