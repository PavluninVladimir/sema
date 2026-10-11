package api

import "strings"

// Динамический UI-DSL Сёмы. Сервер (а в будущем — сам ассистент) собирает
// экран из компонентов; Mac-клиент рендерит их, не зная предметной области.
// Новая задача = новый набор компонентов, без изменения клиента.

type Document struct {
	// Версия словаря, на котором собран документ: клиент сверяет со своей
	// (см. dictionary.go). Отсутствует у документов старых серверов
	Dictionary int `json:"dictionary,omitempty"`
	// Storable — экран можно сохранить в снимке на устройстве. Отсутствие
	// поля — запрет, а не «пока не проставили»: клиент не знает
	// предметной области и не отличит закрытый ящик от открытого, поэтому
	// молчание сервера здесь читается как «хранить нельзя» — то же
	// правило, что у класса чувствительности в outsideCut.
	Storable bool `json:"storable,omitempty"`
	// SnapshotDiffers — правда, только когда этот же экран, собранный в
	// снимок (см. buildScreenForPhone, параметр excluded), покажет не то
	// же самое: на экране есть письма или вложения, а закрытые для облака
	// ящики существуют. Ложь — второй, снимочный запрос не нужен вовсе:
	// то, что уже пришло, и есть честный снимок (сегодня обычный случай —
	// у владельца оба ящика открыты).
	SnapshotDiffers bool `json:"snapshotDiffers,omitempty"`
	// OwnDevice — это устройство отмечено своим на экране «Доступ»
	// (см. store.Session.Own). Отсутствие поля — запрет писать снимок
	// на диск, тот же принцип, что у Storable: тумблер в localStorage
	// отвергнут нарочно — человек не увидел бы со стороны, у скольких
	// устройств есть снимок, и не снял бы отметку с потерянного
	// телефона. Признак решает не сборка экрана (ей запрос не виден —
	// это записанные грабли), а каждый обработчик, отдающий документ
	// наружу, сразу перед отправкой.
	OwnDevice bool `json:"ownDevice,omitempty"`
	// OptionSets — общие наборы вариантов для списков, которые повторяются
	// в каждой строке. Отдаются один раз на документ; список в строке
	// ссылается на набор по имени (Select.OptionsRef).
	OptionSets map[string][]string `json:"optionSets,omitempty"`
	// "wide" — экран занимает всю ширину окна (письмо, задачи)
	Layout         string   `json:"layout,omitempty"`
	Title          string   `json:"title"`
	Heading        string   `json:"heading,omitempty"`        // крупный заголовок в шапке экрана
	Subheading     string   `json:"subheading,omitempty"`     // строка под ним
	ToolbarActions []Action `json:"toolbarActions,omitempty"` // кнопки справа в шапке
	Notice         *Notice  `json:"notice,omitempty"`         // плашка внизу экрана

	// Панель слева от содержимого (список чатов) и закреплённое поле ввода
	Panel       []PanelItem `json:"panel,omitempty"`
	PanelTitle  string      `json:"panelTitle,omitempty"`
	PanelAction *Action     `json:"panelAction,omitempty"` // кнопка «+» в шапке панели
	// PanelKind — каким рисуется панель. Пусто — список чатов или скриптов.
	// «sections» — колонка разделов настроек: поле поиска первой строкой
	// (если у документа Searchable), заголовки групп без адреса
	// (PanelItem.Kind == "heading"), у пункта счётчик (Meta) или точка
	// (Level) справа, а не слева от названия
	PanelKind   string    `json:"panelKind,omitempty"`
	ActivePanel string    `json:"activePanel,omitempty"`
	Composer    *Composer `json:"composer,omitempty"`
	Wizard      *Wizard   `json:"wizard,omitempty"` // пошаговое окно поверх экрана

	// Screen — какой экран собран на самом деле. После действия сервер
	// возвращает готовый документ, и клиент в браузере должен понять,
	// сменился ли экран: «Разобраться» уводит в чат, «Новый чат» — в новый
	Screen  string   `json:"screen,omitempty"`
	Screens []Screen `json:"screens,omitempty"` // сайдбар; пусто — одноэкранный режим
	Active  string   `json:"active,omitempty"`  // id текущего экрана
	// PhoneTabs — нижняя полоса на телефоне: три места, куда ходят чаще
	// всего, плюс «Меню». Экран целиком в кармане не помещается, а
	// прятать переход к почте за жестом — значит прятать почту
	PhoneTabs []Screen `json:"phoneTabs,omitempty"`
	// Owner — чей это Сёма: строка в шапке меню. За одним адресом бывает
	// не один ящик, и человек должен видеть, в чей архив смотрит
	Owner string `json:"owner,omitempty"`
	// Wait — число ждущих для значка установленного приложения. Пока Сёма
	// открыт, значок ставит страница по этому числу; когда закрыт — служебный
	// работник по числу `waiting` из пуша (engine/push.go). Значок — то, что
	// говорит «есть новое», не открывая Сёму
	Wait   string `json:"wait,omitempty"`
	Footer string `json:"footer,omitempty"` // статусная строка внизу сайдбара
	// ToolbarMenu — второстепенные действия шапки: в строке остаётся одно
	// главное, остальное уходит под «…». Иначе управление переносится
	// вторым рядом и отжимает содержимое вниз
	ToolbarMenu   []Action `json:"toolbarMenu,omitempty"`
	Searchable    bool     `json:"searchable,omitempty"`    // клиент показывает поиск и фильтрует строки локально
	SearchHint    string   `json:"searchHint,omitempty"`    // что написано в пустом поле поиска; пусто — «Поиск»
	HeaderSelects []Select `json:"headerSelects,omitempty"` // списки в шапке экрана
	Tabs          []Filter `json:"tabs,omitempty"`          // вкладки раздела (экраны), выбор = переход
	ActiveTab     string   `json:"activeTab,omitempty"`
	// Subtabs — внутренние вкладки одного раздела настроек: «Почта»
	// делится на шесть, «Модели» на четыре. Это тоже экраны (адрес
	// вида «settings:mail@ящик~folders»), поэтому выбор — переход, а не
	// фильтр: на вкладку можно сослаться и вернуться по адресу.
	Subtabs      []Filter `json:"subtabs,omitempty"`
	ActiveSubtab string   `json:"activeSubtab,omitempty"`
	Filters      []Filter `json:"filters,omitempty"` // сегменты фильтра; выбор перезапрашивает экран
	ActiveFilter string   `json:"activeFilter,omitempty"`
	// Highlight — строка, к которой клиент прокрутит экран и которую подсветит
	// на пару секунд: человек пришёл по результату поиска по настройкам или по
	// кнопке замечания «открыть саму настройку». Значение — метка элемента:
	// «f:<форма>-<поле>» у поля формы, «r:<ID>» у строки, «s:<ID>» у раздела
	// (`hlField`, `hlRow`, `hlSection`). Строка в «Тонкой настройке» или в
	// свёрнутом разделе клиент сам раскрывает. Подсветка гаснет сама и при
	// перерисовке экрана опросом не возвращается: метка едет только в ответе на
	// переход, а в адресе экрана её нет (`withHighlight`, `splitHighlight`).
	// Старый клиент поля не знает и просто откроет экран без подсветки
	Highlight  string      `json:"highlight,omitempty"`
	Components []Component `json:"components"`

	// sections — разделы настроек со своими замечаниями, собранные один
	// раз на документ: из них и счётчики в колонке разделов, и «Ждут
	// решения» на Обзоре, и результаты поиска. Наружу не уходит
	sections []settingsSection
	// rare — редкое экрана, которое foldRare унёс в «Тонкую настройку»: тело
	// раздела приезжает отдельной частью `rare`, и она берёт его отсюда, а
	// не собирает заново. Наружу не уходит
	rare []Component
	// noPhoneSearch — полоса фильтров у экрана есть, а своего поиска по списку
	// нет: на телефоне верхнее поле «ищи по настройкам» ему не нужно, как и
	// любому экрану без полосы. Наружу не уходит
	noPhoneSearch bool
	// ownSearch — верхнее поле на этом экране ищет по его собственному списку
	// (чаты Telegram), а не по настройкам: общий поиск по разделам над ним не
	// нужен. Наружу не уходит
	ownSearch bool
	// offline — экран собирается для индекса настроек (`settings_index.go`): ни
	// сети, ни записи, ни запросов к жителю машины. Сборщик, которому для
	// показа нужен такой шаг (список моделей у сервера эмбеддингов, цифры
	// экономии агента, вопрос шлюзу), при этом признаке берёт запомненное или
	// молчит. Признак живёт у документа, а не у сервера: сборки идут
	// одновременно. Наружу не уходит. Им же собирается подогрев при
	// наведении (perf-pass 7): наведение — не намерение человека, и ходить
	// за него наружу нельзя
	offline bool
	// partial — сборщик при offline что-то пропустил (отвечать без сети и
	// без агента было нечем): такой документ человеку не показывается, и
	// подогрев отвечает 204 — нажатие откроет экран обычным запросом.
	// Наружу не уходит
	partial bool
	// rows — сколько строк длинного списка экран показал на самом деле
	// (listLimit): клиент удваивает именно его по «Показать ещё». Иначе в
	// data-limit уходил предел запроса (50), и после первой порции в 20
	// строк вторая была 100 (ревью perf-pass 5)
	rows int
	// shared — общее для экранов одной сборки индекса (карточки моделей, роли).
	// Наружу не уходит
	shared *indexShared
	// guest — экран собирает тот, кому нельзя смотреть доступ (токен без права
	// «всё»): поиск по настройкам не показывает ему настройки входа. Наружу не
	// уходит
	guest bool
}

type Filter struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Badge — счётчик у имени вкладки: «Папки и хранение 5»,
	// «Проблемы 4». Он говорит, есть ли там что смотреть, до того как
	// человек туда зайдёт, — иначе вкладка прячет беду, а не порядок.
	Badge string `json:"badge,omitempty"`
	Level string `json:"level,omitempty"` // ok | info | warn | alert — цвет счётчика
}

// PanelItem — строка боковой панели (чат в списке чатов).
type PanelItem struct {
	ID       string  `json:"id"` // экран, на который ведёт
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle,omitempty"`
	Group    string  `json:"group,omitempty"`   // заголовок группы перед этим пунктом
	Level    string  `json:"level,omitempty"`   // ok | info | warn | alert — состояние чата
	Meta     string  `json:"meta,omitempty"`    // время последнего сообщения, справа
	Delete   *Action `json:"delete,omitempty"`  // действие в контекстном меню
	Nested   bool    `json:"nested,omitempty"`  // ветка под родителем: с отступом, без своей группы
	Depth    int     `json:"depth,omitempty"`   // уровень вложенности: 1 — топик или ветка, 2 — ветка под топиком
	Kind     string  `json:"kind,omitempty"`    // container — группа с топиками или канал (ленты нет); thread — ветка с Сёмой; heading — заголовок группы без адреса и без действия (колонка разделов настроек)
	Open     bool    `json:"open,omitempty"`    // контейнер раскрыт: его разговоры идут следом
	Under    string  `json:"under,omitempty"`   // адрес контейнера, внутри которого этот пункт
	InGroup  string  `json:"inGroup,omitempty"` // имя группы, к которой относится пункт
}

// Composer — закреплённое поле ввода внизу экрана (сообщение в чат).
type Composer struct {
	Placeholder string `json:"placeholder"`
	Field       string `json:"field"`
	Submit      Action `json:"submit"`
	// Кому уйдёт написанное и каким словом позвать Сёму: клиент
	// подсказывает это на лету, пока человек печатает
	Peer string `json:"peer,omitempty"`
	Call string `json:"call,omitempty"`
	// Куда складывать вложения: пусто — прикреплять нечем
	Attach *Action `json:"attach,omitempty"`
}

// Wizard — окно-мастер: несколько шагов с полями и итоговой строкой.
type Wizard struct {
	Title   string       `json:"title"`
	Steps   []WizardStep `json:"steps"`
	Submit  Action       `json:"submit"`
	Cancel  Action       `json:"cancel"`
	Summary string       `json:"summary,omitempty"` // шаблон с {{имя_поля}}
}

type WizardStep struct {
	Title  string  `json:"title"`
	Hint   string  `json:"hint,omitempty"`
	Fields []Field `json:"fields"`
}

// Select — выпадающий список прямо в строке: выбор применяется сразу,
// отдельной кнопки «Сохранить» не нужно.
type Select struct {
	Name    string   `json:"name"`
	Value   string   `json:"value"`
	Options []string `json:"options"`
	Action  Action   `json:"action"`
	Label   string   `json:"label,omitempty"` // подпись рядом со списком: в строке из трёх списков без неё не понять, какой за что
	// OptionsRef — имя общего набора вариантов вместо своего списка.
	//
	// У «Моделей» девять ролей, у каждой список основной и запасной
	// модели, а моделей подключено 54 — это 972 одинаковых строчки
	// вариантов в разметке одного экрана. Из 3597 узлов экрана 1458
	// приходилось на них. Набор отдаётся один раз на документ, а список
	// собирается в тот миг, когда человек его открыл.
	OptionsRef string `json:"optionsRef,omitempty"`
	// key — ключ настройки, которую правит список (см. `Component.key`). Наружу не уходит
	key string
}

// Notice — итоговая плашка внизу экрана (как в Mail).
type Notice struct {
	Icon string `json:"icon,omitempty"` // SF Symbol
	Text string `json:"text"`
}

// Screen — пункт сайдбара. Клиент запрашивает GET /api/ui?screen=<id>.
type Screen struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`  // системное имя SF Symbol
	Badge string `json:"badge,omitempty"` // счётчик справа
	Dot   string `json:"dot,omitempty"`   // цветная точка (ящик)
	Level string `json:"level,omitempty"` // ok | info | warn | alert — состояние экрана
	// Group — заголовок группы, после которого идёт этот пункт. Меню из
	// двенадцати пунктов одним списком человек не читает, а ищет глазами.
	Group string `json:"group,omitempty"`
	// Pinned — закреплённый блок над меню: то, что ждёт человека, стоит
	// отдельно и заметно, а не строкой среди прочих. Пункт с этим
	// признаком в общий список не попадает
	Pinned bool `json:"pinned,omitempty"`
	// Words — слова, по которым человек ищет этот раздел, когда не помнит
	// его названия: «пуши», «ключи», «наружу». Поиск по меню на телефоне
	// главный способ добраться — шестнадцать разделов пальцем не листают
	Words    string   `json:"words,omitempty"`
	Children []Screen `json:"children,omitempty"`
	// NoSelf — у узла нет строки перехода на него самого («Обзор»): узел
	// только раскрывается. Нужно, когда адрес узла ничего своего не
	// открывает — у «Чатов» он вёл в тот же разговор с Сёмой, что и
	// первый ребёнок, и строка была двойником
	NoSelf bool `json:"noSelf,omitempty"`
	// Open — узел раскрыт всегда, а не только когда внутри стоят. Раскрытие
	// «где стоишь» заставляло меню прыгать: открыл разговор из другой
	// группы — прежняя группа сворачивалась, и всё ниже уезжало на
	// полэкрана (запись 16 сентября 2026). Свернуть такой узел может
	// человек, и клиент это запомнит
	Open bool `json:"open,omitempty"`
	// Tint — оттенок значка: blue, sky, violet, green, orange, red, grey.
	// Это цвет раздела, а не состояния: состояние по-прежнему говорит точка
	// справа, иначе красный значок «Здоровья» читался бы как тревога
	Tint string `json:"tint,omitempty"`
	// Tile — значок белым на цветной плитке оттенка: так узнаются
	// мессенджеры и ящики, у них свой цвет
	Tile bool `json:"tile,omitempty"`
	// Initials — буквы в кружке вместо значка: разговор с Сёмой
	Initials string `json:"initials,omitempty"`
	// Divider — черта над пунктом: блоки меню отделены друг от друга
	Divider bool `json:"divider,omitempty"`
	// GroupIcon — значок у подписи группы, которую открывает этот пункт
	GroupIcon string `json:"groupIcon,omitempty"`
	// Add — «плюс» в строке пункта: создать новое прямо из меню («Новый
	// чат» у «Чатов»). Жил кнопкой над панелью со списком чатов и пропал
	// вместе с ней
	Add *Action `json:"add,omitempty"`
}

type Component struct {
	// metric — плитка-показатель: подпись, крупное значение с единицей,
	// пояснение и уровень полосой слева. alarm — плашка поломки над
	// содержимым. figs — строка чисел-сводки в шапке экрана. shelf —
	// полка «ждут вас»: то, что не поедет без человека, стоит вверху
	// одним тёплым блоком. rowgroup — одинаковые строки, свёрнутые в
	// одну с причиной и счётчиком: тридцать отказов с одной и той же
	// ошибкой DNS занимали весь экран. table — таблица с подписями
	// колонок для строк с одинаковым управлением: подпись «наружу»
	// печаталась по разу в каждой из тридцати строк вместо одного
	// заголовка. chipset — ряд капсул с хвостом «ещё N»: пятьдесят
	// четыре модели не должны занимать семь строк. step — шаг мастера
	// первого запуска (сделан / раскрыт / впереди). crumb — обратный
	// путь над заголовком вложенной вкладки.
	//
	// Лента разговора и письмо. thinking — именное ожидание: кто думает,
	// какой моделью, сколько секунд и кнопка «Отменить»; локальная модель
	// думает по четыре минуты, и «загрузка» без имени ничего не говорит.
	// toolcard — карточка дела в ленте (команда на подтверждение,
	// выполненная команда, таймер, правка скилла, запись в память,
	// найденное в сети, скрипт, «вопрос закрыт»): значок, заголовок,
	// мета, полоска уровня, тело и ряд действий, где разрушительное
	// отделено пустотой. chain — свёрнутая цепочка писем: точечная шкала
	// и три строки вместо одиннадцати одинаковых. classpicker — класс
	// чувствительности письма сегментом и одной строкой про то, что
	// уходит наружу. patterns — правила сервера тремя колонками
	// образцов (Разрешить / Спросить / Запретить): колонка — ребёнок со своим
	// Title, Caption и Level, образец — ребёнок колонки (Text, Style "hit" у
	// сработавшего, «×» в Actions), «добавить» — Actions колонки.
	// switcher — выбор сущности карточками (ящики,
	// серверы): боковая панель на телефоне не живёт.
	Type string `json:"type"` // header | text | group | bubble | section | row | form | button | divider | metric | alarm | figs | shelf | rowgroup | matrix | table | chipset | step | crumb | thinking | toolcard | chain | classpicker | switcher
	ID   string `json:"id,omitempty"`

	// header / text / group (подзаголовок списка) / bubble (реплика чата)
	Text string `json:"text,omitempty"`
	// Style: secondary | error; у text ещё и callout (подсвеченное пояснение
	// над настройками: жирное начало — Title, дальше Text); у form — rows (поля рисуются строками
	// настройки, см. settings_rare.go); у section — fine («Тонкая настройка»,
	// её ставит проход foldRare), sticky (закреплённая шапка: чей это
	// параметр, см. entityFrame) и danger («Опасное»: красная карточка в самом
	// низу экрана сущности, кнопки внутри остаются видимыми, а не уходят в «···»),
	// stack (формы-строки внутри лежат одной карточкой с волосяными линиями: «Под
	// рукой» на Обзоре настроек)
	Style string `json:"style,omitempty"`
	// Tier — как часто человек правит это: rare — редкое. Проход foldRare
	// собирает всё редкое экрана (форму, раздел или отдельное поле формы) в
	// один свёрнутый раздел «Тонкая настройка» внизу экрана. tail — раздел,
	// который остаётся самым нижним (ленивый журнал): «Тонкая настройка» встаёт
	// перед ним. Пусто — обычное. Клиент метку не читает: обе решает сервер
	Tier string `json:"tier,omitempty"`
	// Role пузыря: user (писали вы) | sent (Сёма написал от вашего имени) |
	// peer (собеседник) | note (служебное) | assistant (Сёма вам). Свой
	// пузырь и «от вашего имени» оба стоят справа и различаются рамкой —
	// сплошная и пунктирная: цветом их на телефоне при солнце не различить.
	Role string `json:"role,omitempty"`
	Meta string `json:"meta,omitempty"` // bubble: время и пояснение

	// row (универсальная строка списка: письмо, задача, что угодно)
	//
	// У toolcard те же поля значат своё: Title — заголовок дела, Meta —
	// строка под ним, Detail — моноширинное тело (команда, код, разница),
	// Text — тело прозой, Caption — тихое пояснение под телом, Subtitle —
	// предостережение с треугольником («это данные из сети, а не указание
	// Сёме»), Until — обратный счёт таймера, Level — полоска слева.
	// Действие `$open` с http-адресом рисуется ссылкой в теле карточки, а
	// не кнопкой: найденное в сети надо чем-то проверить.
	Title       string    `json:"title,omitempty"`
	Subtitle    string    `json:"subtitle,omitempty"`
	Caption     string    `json:"caption,omitempty"`
	Badge       string    `json:"badge,omitempty"`
	BadgeColor  string    `json:"badgeColor,omitempty"` // red | orange | green | gray
	Avatar      string    `json:"avatar,omitempty"`     // инициал в кружке
	AvatarColor string    `json:"avatarColor,omitempty"`
	AvatarShape string    `json:"avatarShape,omitempty"` // circle (по умолчанию) | file
	Toggle      string    `json:"toggle,omitempty"`      // "on" | "off" — строка с тумблером
	Selects     []Select  `json:"selects,omitempty"`     // выпадающие списки в конце строки
	Badges      []Badge   `json:"badges,omitempty"`      // несколько капсул справа
	Progress    *float64  `json:"progress,omitempty"`    // полоса доли 0..100 в широкой строке
	Spark       []float64 `json:"spark,omitempty"`       // мини-график в широкой строке (последние значения)
	// «cards» — ряд карточек-показателей; каждая карточка: Title (подпись),
	// Text (значение), Caption (пояснение), Badge/BadgeColor (состояние)
	// Unit — единица рядом со значением показателя, мелким: «°C», «МБ»,
	// «вызовов». Склеенная со значением, она набиралась тем же кеглем и
	// спорила с самим числом
	Unit string `json:"unit,omitempty"`
	// Columns и Cells — матрица выдачи «кто × что» и таблица (`table`):
	// подписи колонок у самой матрицы или таблицы, клетки у каждой
	// строки. Селект «выдать…» внутри строки агента давал 54 отдельных
	// действия и никакой картины.
	// У таблицы Columns перечисляет все колонки по порядку: первая —
	// название строки (Subtitle плюс Caption мелким), дальше по клетке
	// на колонку, последняя — управление строки (тумблер, кнопки,
	// списки, меню), если оно есть; для неё подпись обычно пустая.
	Columns []string `json:"columns,omitempty"`
	Cells   []Cell   `json:"cells,omitempty"`
	Layout  string   `json:"layout,omitempty"` // "wide" — значки и кнопки по правому краю
	Tint    string   `json:"tint,omitempty"`   // подсветка строки: red | blue | green
	Menu    []Action `json:"menu,omitempty"`   // действия в контекстном меню строки
	Icon    string   `json:"icon,omitempty"`   // SF Symbol слева в широкой строке
	// MetaInline — мету рисовать рядом с названием, а не отдельной
	// строкой снизу: в списке писем адрес и время стоят в одну линию с
	// отправителем, а тема идёт следующей строкой
	MetaInline bool   `json:"metaInline,omitempty"`
	IconColor  string `json:"iconColor,omitempty"`
	Selectable bool   `json:"selectable,omitempty"` // строку можно отметить для группового действия
	Detail     string `json:"detail,omitempty"`     // разворачиваемое содержимое (клик по строке)
	Dot        string `json:"dot,omitempty"`        // цветная точка уровня слева вместо иконки и стрелки
	// Level — состояние одним словом: ok (работает) | info (к сведению) |
	// warn (посмотреть сегодня) | alert (ждёт человека или сломано).
	// Цвет по уровню держит клиент: до этого один и тот же смысл ездил
	// семнадцатью разными hex, и красный ничего не значил.
	Level string `json:"level,omitempty"`
	Until string `json:"until,omitempty"` // RFC3339: клиент показывает обратный отсчёт до этого момента
	Props []Prop `json:"props,omitempty"` // таблица «подпись — значение» в раскрытии
	// Facts — то же, но прямо в строке, мелким шрифтом под заголовком:
	// «Адрес 127.0.0.1:1234 · Модель qwen3-4b · Ключ не нужен». До этого
	// всё склеивалось в caption через «·» — семь фактов в три строки с
	// обрезкой на середине.
	Facts []Prop   `json:"facts,omitempty"`
	Cmds  []CmdRow `json:"cmds,omitempty"` // команды на подтверждение в раскрытии
	// Image — адрес картинки, которую строка показывает как картинку.
	// Рамка-песочница для картинки не годится: файл отдаётся с запретом
	// встраивания, и в рамке человек видел «сайт не позволяет установить
	// соединение» вместо своей картинки (14 сентября 2026).
	Image string `json:"image,omitempty"`
	// Frame — адрес отдельного документа, который строка показывает рамкой
	// в песочнице. Так показывается HTML письма: чужая разметка не должна
	// жить в адресе Сёмы (см. api/mailbody.go). Пусто — рамки нет.
	Frame string `json:"frame,omitempty"`
	// FrameImages — сколько внешних картинок в этом документе не загружено.
	// Внешняя картинка в письме — отметка о прочтении для отправителя,
	// поэтому их показывает человек кнопкой, а не Сёма сам.
	FrameImages int `json:"frameImages,omitempty"`
	// FrameTranslated — есть ли у этого письма перевод и на какой язык.
	// Перевод показывается той же рамкой: письмо остаётся письмом,
	// меняются только слова
	FrameTranslated string `json:"frameTranslated,omitempty"`
	DetailHTML      string `json:"detailHtml,omitempty"` // HTML-версия содержимого; рисуется рамкой Frame
	Expanded        bool   `json:"expanded,omitempty"`   // развёрнута ли строка при первом показе
	Expandable      bool   `json:"expandable,omitempty"` // строку можно развернуть (тело догрузится)
	Files           []File `json:"filesList,omitempty"`  // вложения развёрнутой строки

	// Expand — подпись раскрытия строки вместо «Подробнее»: «Настройки»
	// называет то, что внутри, а «Подробнее» обещает лишь пояснение. Нужна
	// строкам, у которых в раскрытии стоят поля формы (канал уведомлений,
	// устройство с пушами, наблюдаемая папка). Пусто — «Подробнее»
	Expand string `json:"expand,omitempty"`

	// section
	Children []Component `json:"children,omitempty"`
	// Свёрнутый раздел (аккордеон): заголовок кликается, содержимое
	// прячется. Нужен ID — по нему клиент помнит, что человек раскрыл.
	Collapsed bool `json:"collapsed,omitempty"`
	// Lazy — раздел приезжает пустым, содержимое догружается при
	// раскрытии по адресу части: `/ui?screen=…&part=<Lazy>`. Свёрнутый
	// раздел с сорока чатами и семью тумблерами у каждого весил две
	// тысячи узлов, которых человек не видел; замер 12 сентября 2026 —
	// 207 КБ у MAX, 138 КБ у Telegram. Часть строит своя функция из
	// реестра `части`, и раздел, чьей части в реестре нет, роняет тест.
	// SearchHint — подсказка поля поиска внутри части; пусто — поля нет.
	Lazy       string `json:"lazy,omitempty"`
	SearchHint string `json:"searchHint,omitempty"`

	// form
	Fields []Field `json:"fields,omitempty"`
	Submit *Action `json:"submit,omitempty"`
	// Deferred — форму сохраняет нижняя полоса экрана, своей кнопки внизу
	// у неё нет. На экране настроек форм бывает пять и больше, и у каждой
	// своя кнопка «Сохранить»: человек правил ящик, уходил на соседнюю
	// вкладку и терял правку молча — ничто на экране не говорило, что она
	// не сохранена. Полоса одна на экран: она считает несохранённое,
	// сохраняет всё разом и не даёт уйти, не сказав, что пропадёт.
	Deferred bool `json:"deferred,omitempty"`

	// row / button
	Actions []Action `json:"actions,omitempty"`

	// graph — граф узлов и связей: клиент раскладывает его силами сам
	Graph *Graph `json:"graph,omitempty"`

	// key — ключ настройки, которую правит тумблер строки, если поле для неё не
	// собрано (`settingField`): настройки нет в реестре (`store.SettingDef`) или
	// она в реестре, но правится строкой ради вопроса перед нажатием. По
	// ключу индекс настроек знает строку, а журнал называет правку по-человечески
	// («Ночная смена выполняет наряд»), а не ключом. keyInverted — тумблер читается
	// наоборот: «выполняет наряд» включено, когда настройка «вхолостую» выключена.
	// Наружу не уходят
	key         string
	keyInverted bool
}

// Graph — узлы с цветом области и размером по числу связей; клик по узлу
// ведёт на экран Target.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Area     string `json:"area,omitempty"`
	Color    string `json:"color,omitempty"`
	Size     int    `json:"size,omitempty"`
	Dim      bool   `json:"dim,omitempty"`      // закрыт для выбранной модели
	Selected bool   `json:"selected,omitempty"` // выбранный узел
	Target   string `json:"target,omitempty"`   // экран по клику
}

type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

type Field struct {
	// segments — выбор из двух-трёх коротких вариантов сегментным
	// переключателем справа в строке настройки: «личная / рабочая»
	// читается за один взгляд, а тот же выбор списком на всю ширину
	// экрана — нет.
	//
	// timespan — окно «с … до …» двумя полями времени: значение
	// «02:00-06:00» (пусто — круглые сутки), уходит двумя параметрами
	// <имя>_from и <имя>_to; склеивает их сервер (`timespanParam`)
	//
	// checks — несколько вариантов из списка чипами («Серверы · Почта ·
	// Запросы · Модули»): Value — выбранные через запятую (в вариантах запятых
	// нет), у каждого чипа свой флажок с именем «<имя>.<вариант>», а список
	// флажков идёт в `toggles` формы: снятый браузер не присылает, и сервер
	// узнаёт о нём как о «false» (`checkedParams`)
	Type  string `json:"type"` // text | secure | toggle | picker | slider | stepper | chips | segments | timespan | checks
	Name  string `json:"name"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"` // пояснение под полем
	Value string `json:"value,omitempty"`
	// Tones — цвет выбранного варианта сегментного переключателя по порядку
	// вариантов: ok, warn, alert, пустая строка — без цвета. Цвет только у
	// выбранного: ответ правила («Разрешить» зелёным, «Спросить» жёлтым,
	// «Запретить» красным) читается одним взглядом и не превращается в светофор
	Tones []string `json:"tones,omitempty"`
	// Disabled — варианты сегментного переключателя, которые сейчас выбрать
	// нельзя: «полная» самостоятельность машине без суда. Вариант виден, но
	// серый; почему нельзя, говорит строка рядом с полем — а не молчаливо
	// серый пункт. Уже выбранный вариант не отключается
	Disabled []string `json:"disabled,omitempty"`
	// More — поле редкое для формы: оно прячется под «Ещё N полей» в той же
	// форме и уходит вместе с остальными. Не путать с Tier: Tier уносит поле
	// в «Тонкую настройку» экрана отдельной формой, а форма, которая заводит
	// новое (сервер, ящик), такой не бывает — у неё одна кнопка
	More      bool     `json:"more,omitempty"`
	Options   []string `json:"options,omitempty"`
	Min       float64  `json:"min,omitempty"`
	Max       float64  `json:"max,omitempty"`
	Step      float64  `json:"step,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	Instant   bool     `json:"instant,omitempty"`   // применять сразу, без кнопки «Сохранить»
	ErrorLine int      `json:"errorLine,omitempty"` // строка с ошибкой — подсветить в редакторе
	// Tier — rare: поле редкое, и проход foldRare унесёт его в «Тонкую
	// настройку» отдельной формой с теми же подписью и действием
	Tier string `json:"tier,omitempty"`
	// Default — умолчание настройки в том же виде, что Value. Указатель, а
	// не строка: пустое умолчание («не задан») — тоже умолчание, и отличать
	// его от «умолчание неизвестно» нечем иначе. По нему строка настройки
	// показывает «по умолчанию», а «Тонкая настройка» считает, сколько
	// параметров изменено. Берётся из реестра настроек (store.SettingDef),
	// а не вписывается числом рядом с полем
	Default *string `json:"default,omitempty"`
	// key — ключ настройки в реестре (`store.SettingDef`), если поле собрано из
	// него (`settingField`). По ключу индекс настроек знает, какое это поле, а
	// журнал настроек — как оно называется по-человечески. Наружу не уходит
	key string
}

// Cell — клетка матрицы выдачи: что стоит на пересечении «кто» и «что»,
// и что произойдёт по нажатию. Состояние: on — выдано, out — выдано и
// уходит наружу облачной модели, off — не выдано.
//
// Та же клетка служит ячейкой таблицы (`table`): там у неё есть Text —
// то, что написано в ячейке, Level — цвет значения по состоянию и Mono —
// «это Сёма померил» (адрес, время, цена). Действие необязательно:
// пустое Action.Action рисует ячейку текстом, а не кнопкой.
//
// Select — выпадающий список прямо в ячейке таблицы: роль меняет модель там
// же, где её видно, и применяется сразу, как список в строке. Подписи у такого
// списка нет: её говорит заголовок колонки, а читающему вслух — «роль —
// колонка» (собирает toBlock).
//
// Права чатов — тоже матрица (2 октября 2026, «Telegram» и «MAX»): клетка
// матрицы бывает «na» — такого права у такого чата не бывает
// (админом может быть только личный чат, публиковать — только в канал): она
// не нажимается, а причина стоит в подсказке, вместо тумблера, который
// отказал бы после нажатия. У темы группы клетка «наследует» значение
// группы: она показывает действующее право и нажимается — нажатие делает его
// своим.
type Cell struct {
	Title     string  `json:"title,omitempty"`     // подсказка при наведении
	State     string  `json:"state,omitempty"`     // on | out | off | na
	Inherited bool    `json:"inherited,omitempty"` // значение чужое: у темы — право группы
	Text      string  `json:"text,omitempty"`      // содержимое ячейки таблицы
	Level     string  `json:"level,omitempty"`     // ok | info | warn | alert — цвет значения
	Mono      bool    `json:"mono,omitempty"`      // машинный факт: моноширинным и по правому краю
	Select    *Select `json:"select,omitempty"`
	Action    Action  `json:"action"`
}

// Prop — строка таблицы в раскрытии: подпись, значение, цвет значения.
type Prop struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Color string `json:"color,omitempty"`
}

// CmdRow — команда в раскрытии: моноширинный текст, пояснение, кнопки.
type CmdRow struct {
	Text    string   `json:"text"`
	Note    string   `json:"note,omitempty"`
	Actions []Action `json:"actions,omitempty"`
}

// Action: клиент шлёт POST /api/action {"action": ..., "target": ..., "params": {...}}
type Action struct {
	Action    string `json:"action"`
	Target    string `json:"target,omitempty"`
	Label     string `json:"label"`
	Style     string `json:"style,omitempty"`     // primary | destructive
	Prompt    string `json:"prompt,omitempty"`    // клиент спросит текст и пришлёт его в params["value"]
	Confirm   string `json:"confirm,omitempty"`   // необратимое действие: клиент сначала переспросит
	ConfirmOK string `json:"confirmOk,omitempty"` // надпись на кнопке подтверждения
	Value     string `json:"value,omitempty"`     // начальное значение для prompt
	Secure    bool   `json:"secure,omitempty"`    // prompt — закрытый ввод (ключ, пароль)
	// Deferrable — действие можно поставить в очередь и отправить, когда
	// появится связь. Отсутствие поля — запрет, а не «клиент решит сам»:
	// решения момента (подтверждение команды, перенос письма, приём
	// отпечатка) не должны повторяться часы спустя, и молчание сервера
	// здесь читается как «откладывать нельзя».
	Deferrable bool `json:"deferrable,omitempty"`
}

// Badge — капсула справа в строке (расписание, состояние).
type Badge struct {
	Text  string `json:"text"`
	Color string `json:"color,omitempty"` // blue | green | red | orange | purple | gray
	Level string `json:"level,omitempty"` // ok | info | warn | alert — палитру держит клиент
}

// File — чип вложения в развёрнутом письме.
type File struct {
	ID    int64  `json:"id,omitempty"` // файл Сёмы: клиент открывает его через сервер ($file)
	Name  string `json:"name"`
	Meta  string `json:"meta,omitempty"`  // размер, тип
	Badge string `json:"badge,omitempty"` // сумма чека
	Path  string `json:"path,omitempty"`  // локальный путь для $open (только инструменты на этой машине)
	Image bool   `json:"image,omitempty"` // картинка: клиент показывает её прямо в переписке
}

// Уровни состояния. Один смысл — одно слово; цвет по нему выбирает клиент.
const (
	LevelOK    = "ok"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelAlert = "alert"
)

// levelDot — цвет точки для уровня. Пока не все экраны переведены на
// уровни, hex остаётся понятным: перевод идёт постепенно, а не разом по
// семистам местам.
func levelDot(level string) string {
	switch level {
	case LevelOK:
		return "#28C840"
	case LevelInfo:
		return "#007AFF"
	case LevelWarn:
		return "#FEBC2E"
	case LevelAlert:
		return "#FF3B30"
	}
	return ""
}

// withLevel проставляет строке уровень и точку разом: так их невозможно
// рассинхронизировать.
func withLevel(c *Component, level string) {
	c.Level = level
	if dot := levelDot(level); dot != "" {
		c.Dot = dot
	}
}

// collapseLong — раздел длиннее порога сворачивается и несёт счётчик в
// заголовке. Пятьдесят строк подряд человек не читает: он их пролистывает.
func collapseLong(cs []Component, limit int) {
	// главный список экрана не сворачиваем: свернуть единственный раздел
	// значит показать человеку пустой экран
	sections := 0
	for _, c := range cs {
		if c.Type == "section" {
			sections++
		}
	}
	// открытым держим первый раздел экрана по порядку, а не первый
	// превысивший порог: иначе флаг тратился на длинный раздел где-то
	// посередине, и самый длинный список так и оставался лентой
	firstIdx := -1
	for i, c := range cs {
		if c.Type == "section" {
			firstIdx = i
			break
		}
	}
	for i := range cs {
		c := &cs[i]
		collapseLong(c.Children, limit)
		// раздел, который сборщик сам назвал раскрытым (Expanded), не сворачивается:
		// результаты поиска — это то, за чем человек пришёл, а не лента, которую
		// пролистывают
		if c.Type != "section" || c.Collapsed || c.Expanded || len(c.Children) <= limit {
			continue
		}
		if i == firstIdx || sections < 2 {
			continue
		}
		rows := 0
		waits := false
		for _, ch := range c.Children {
			if ch.Type == "row" {
				rows++
			}
			// раздел, где что-то ждёт человека, свёрнутым не прячем
			if ch.Level == LevelAlert || ch.Tint != "" {
				waits = true
			}
		}
		if rows <= limit || waits {
			continue
		}
		c.Collapsed = true
		if c.ID == "" {
			c.ID = "sec-" + c.Title
		}
	}
}

// splitFacts разбирает строку, склеенную через «·», на человеческую фразу
// и факты. Первая часть, если она не похожа на «подпись значение», —
// фраза; остальные становятся парами. Так строки, собранные годами через
// «·», превращаются в таблицу без переписывания каждого экрана.
func splitFacts(meta string) (string, []Prop) {
	parts := strings.Split(meta, " · ")
	if len(parts) < 2 {
		return meta, nil
	}
	var human []string
	var facts []Prop
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch {
		case strings.HasPrefix(p, "http://"), strings.HasPrefix(p, "https://"):
			facts = append(facts, Prop{Label: "Адрес", Value: strings.TrimPrefix(strings.TrimPrefix(p, "https://"), "http://")})
		case strings.Contains(p, "/") && !strings.Contains(p, " "):
			facts = append(facts, Prop{Label: "Модель", Value: p})
		case strings.HasPrefix(p, "$"), strings.HasSuffix(p, "контекст"):
			facts = append(facts, Prop{Label: "", Value: p})
		case len(strings.Fields(p)) <= 3 && len(human) > 0:
			facts = append(facts, Prop{Label: "", Value: p})
		default:
			human = append(human, p)
		}
	}
	return strings.Join(human, " · "), facts
}

// emptyState — пустое место, которое объясняет себя: что здесь бывает,
// почему сейчас пусто и что можно сделать. «Пока пусто» серой строкой —
// это не ответ.
func emptyState(title, why, icon string) Component {
	return Component{Type: "row", Layout: "wide", Style: "empty",
		Icon: icon, Subtitle: title, Caption: why}
}
