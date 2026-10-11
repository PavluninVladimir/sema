package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/version"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

type Server struct {
	eng  *engine.Engine
	live liveConns // сколько проводов живого обновления держит каждая сессия
	// порог медленного запроса, прочитанный не чаще раза в slowLimitTTL:
	// настройка — тоже запрос к базе, а нужна она после каждого экрана
	slowSQL slowLimit
	// цвета областей памяти на время сборки экрана (areaPalette)
	areaColorsMu  sync.Mutex
	areaColorsMap map[string]string
	areaColorsAt  time.Time

	mu               sync.Mutex
	lastQuery        string
	lastAnswer       string
	showNewTask      bool                // открыт ли мастер создания задачи
	lastCheck        string              // результат последней проверки модели
	editTaskID       int64               // задача, которую сейчас правят в окне
	scriptRuns       map[int64]scriptRun // что показал последний запуск скрипта
	catalog          catalogState        // выбранный провайдер и его список моделей
	pendingModule    *engine.Module      // разобранный модуль, ждущий подтверждения прав
	pendingGrants    map[string]bool     // какие из его секретов человек решил выдать
	freshInvite      *freshInvite        // только что выданное приглашение агенту: код показывается один раз
	lastAgentCheck   agentCheck          // результат последней «Проверить связь» у агента
	linksVia         func() []string     // подмена списка живых проводов к агентам: только в тестах, как Engine.tgVia
	graphEyes        string              // чьими глазами показывать граф памяти: «вы» или модель
	lastStorageCheck string              // результат последней проверки хранилища: «имя: что сказал»
	lastFindCheck    string              // результат последней проверки адреса поиска страниц
	lastReply        string              // что случилось с последним ответом: черновик или отправка
	lastToken        string              // только что выданный токен: показывается недолго
	lastTokenAt      time.Time
	auth             *auth.Auth

	// подмена проверки хранилища: настоящей нужна сеть, а тесты в неё не ходят
	checkStorageVia func(name string) (string, error)

	// индекс настроек для поиска, журнала и «Под рукой»: под своим замком, без
	// данных запроса (settings_index.go)
	index indexCache
}

// scriptRun — результат запуска скрипта для показа в интерфейсе.
// catalogState — незавершённое подключение модели: провайдер выбран, ключ
// введён, список моделей получен. Ключ живёт только в памяти и попадает в
// базу зашифрованным, когда человек нажимает «Добавить».
type catalogState struct {
	Provider string
	Key      string
	Models   []engine.CatalogModel
	Err      string
}

type scriptRun struct {
	At      time.Time // время запуска: без него повторный запуск с тем же
	Log     []string  // результатом не менял бы документ, и экран молчал
	Value   string
	ErrText string
	ErrLine int
	ErrCode string
	Seconds float64
}

// newTaskFormOpen — открыт ли мастер создания задачи.
func (s *Server) newTaskFormOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.showNewTask
}

// editingTask — какую задачу правят в окне (0 — никакую).
func (s *Server) editingTask() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editTaskID
}

func New(eng *engine.Engine) *Server { return &Server{eng: eng, auth: auth.New(eng.Store())} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerAuth(mux)
	trustProxy.Store(s.eng.Store().Setting(auth.SettingTrustProxy, "false") == "true")
	s.auth.LoadLocalScope()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "version": version.String()})
	})
	// пуши в браузер: ключ для подписки и сама подписка. Всё под входом
	mux.HandleFunc("GET /api/push/key", s.handlePushKey)
	mux.HandleFunc("POST /api/push/subscribe", s.handlePushSubscribe)
	mux.HandleFunc("POST /api/push/unsubscribe", s.handlePushUnsubscribe)
	mux.HandleFunc("GET /api/ui", s.handleUI)
	// живое обновление: имена изменившихся экранов по постоянному проводу
	mux.HandleFunc("GET /api/ws", s.handleLive)
	// возврат с входа OAuth на сервер MCP: под тем же protect, что и всё
	// остальное — его зовёт браузер владельца со своей сессией
	mux.HandleFunc("GET "+engine.MCPCallbackPath, s.handleMCPCallback)
	// документ клиента для серверов входа: открыт без входа (его скачивает
	// чужой сервер), отдаёт только настройки — см. handleMCPClientDoc
	// (без метода в образце: у открытого пути чужой метод должен получить 405,
	// а не оболочку интерфейса с общего маршрута «/»)
	mux.HandleFunc(engine.MCPClientDocPath, s.handleMCPClientDoc)
	mux.HandleFunc("POST /api/action", s.handleAction)
	// файлы получают и загружают только через сервер: клиенту всё равно, где они лежат
	mux.HandleFunc("GET /api/files/{id}", s.handleFileGet)
	// файл по подписанной ссылке — без входа (file-share-links)
	mux.HandleFunc("GET "+engine.SharePath+"{token}", s.handleShare)
	mux.HandleFunc("POST /api/files", s.handleFileUpload)
	// память для внешних агентов: MCP и простые хуки сессии
	// вход агента по одноразовому коду: единственное место, куда он
	// приходит без сертификата
	mux.HandleFunc("POST /api/agents/enroll", s.eng.HandleEnroll)
	// интерфейс в браузере: разметку собирает сам сервер, а обмен идёт
	// htmx — куском страницы вместо перерисовки всего
	// тело письма отдельным документом: в песочнице, чужой HTML не живёт
	// в адресе Сёмы (см. mailbody.go)
	mux.HandleFunc("GET "+messagePath+"/{id}", s.handleMessageBody)
	mux.HandleFunc(pprofPath, s.handlePprof)
	mux.HandleFunc("GET /ui", s.handleWebUI)
	mux.HandleFunc("POST /do", s.handleWebDo)
	mux.HandleFunc("POST /upload", s.handleWebUpload)
	// оболочка (страница, стиль, htmx, работник) — из бинарника
	// неизвестный путь под /api — честный отказ, а не оболочка: клиент,
	// который стучится не туда (или в снятую ручку), должен видеть 404, а
	// не HTML со страницей
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "нет такой ручки: "+r.URL.Path, http.StatusNotFound)
	})
	mux.Handle("/", web.Handler())
	return s.protect(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if !s.allowScreen(w, r, r.URL.Query().Get("screen")) {
		return
	}
	s.eng.AdoptClientTimezone(r.Header.Get("X-Sema-TZ"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	doc := outdatedClient(r)
	if doc == nil {
		var err error
		// снимочная сборка — тем же путём и тем же параметром, что у /ui
		снимок := r.URL.Query().Get("snapshot") == "1"
		doc, err = s.buildScreenForPhone(r.URL.Query().Get("screen"), r.URL.Query().Get("filter"), search, limit, false, снимок, s.viewerOpts(r)...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// признак ставит обработчик, не сборка экрана: сессия — свойство
	// запроса, а не построенного документа (offline-snapshot, группа 3.3)
	doc.OwnDevice = s.ownDevice(r)
	stampDictionary(w, doc)
	// ETag: если экран не изменился, клиент получает 304 и ничего не перерисовывает
	body, err := json.Marshal(doc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	etag := fmt.Sprintf(`W/"%x"`, sha256.Sum256(body))
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	web.WriteMaybeGzip(w, r, body)
}

type actionRequest struct {
	Action string            `json:"action"`
	Target string            `json:"target"`
	Screen string            `json:"screen"`
	Filter string            `json:"filter"`
	Limit  int               `json:"limit"`
	Params map[string]string `json:"params"`

	// Откуда и от кого пришло действие. Выставляет только executeSigned из
	// запроса, из тела они не читаются (JSON неэкспортируемое не видит):
	// присланным клиентом адресом возврата OAuth воспользоваться нельзя.
	origin string // адрес Сёмы, с которого нажали: схема и хост
	who    string // кто нажал (Identity.Who)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if doc := outdatedClient(r); doc != nil {
		// действие старого клиента не выполняется: он мог нажать не то,
		// что видел человек
		doc.OwnDevice = s.ownDevice(r)
		stampDictionary(w, doc)
		writeJSON(w, doc)
		return
	}
	// действие принимается только как JSON от своего клиента: форма с
	// чужого сайта шлёт text/plain и не может выставить этот заголовок, а
	// Sec-Fetch-Site у браузера честно говорит, откуда запрос
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "действие принимается только как application/json", http.StatusUnsupportedMediaType)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
		http.Error(w, "действие с чужого сайта не выполняется", http.StatusForbidden)
		return
	}
	var req actionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !s.allowAction(w, r, req) {
		return
	}
	redirect, err := s.executeSigned(r, req)
	if err != nil {
		log.Printf("action %s(%s): %v", req.Action, req.Target, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if redirect == "" {
		redirect = req.Screen
	}
	// после действия сразу возвращаем обновлённый экран
	if !s.allowScreen(w, r, redirect) {
		return
	}
	doc, err := s.buildScreen(redirect, req.Filter, "", req.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc.OwnDevice = s.ownDevice(r)
	writeJSON(w, doc)
}

// errUnhandled — группа действий не знает такого действия; пробуем следующую.
var errUnhandled = errors.New("действие не из этой группы")

// executeSigned — единственный путь действия человека внутрь Сёмы: и `/api/action`,
// и `/do` веб-клиента приходят сюда.
//
// Здесь стоит вторая дверь журнала настроек. Первая (`store.SetSetting`)
// знает, ЧТО изменилось: она единственный писатель таблицы настроек, и
// мимо неё правка не проходит. Эта отвечает на второй вопрос — КТО.
//
// Подпись ставится не в местах вызова, а вокруг всего действия: нам всё
// равно, какой дорогой оно добралось до настройки — напрямую через
// хранилище, сеттером движка или через модуль. Список мест вызова пришлось
// бы дополнять каждому новому экрану настроек, забытая строка означала бы
// «Сёма сам» под правкой человека, а такой журнал хуже отсутствующего:
// ему верят. `TestActionSignsEdits` роняет сборку, если
// `execute` позовут мимо этой обёртки.
func (s *Server) executeSigned(r *http.Request, req actionRequest) (string, error) {
	st := s.eng.Store()
	метка := st.LastChange()
	req.origin, req.who = s.requestOrigin(r), s.auth.Identify(r).Who
	redirect, err := s.execute(req)
	st.SignChanges(метка, whoActs(s.auth.Identify(r)), editOrigin(req))
	// после действия человека индекс настроек мог устареть: завели сервер,
	// переименовали ящик, поменяли значение, которое поиск показывает
	if s.indexStaleAfter(req, st.ChangedKeysSince(метка)) {
		s.dropSettingsIndex()
	}
	return redirect, err
}

// whoActs — как назвать человеку того, кто нажал кнопку. Хозяин один,
// и «вы» честнее номера ключа; токен называется по имени, потому что это
// уже не рука за экраном, а чужая программа с правом.
func whoActs(id auth.Identity) string {
	switch id.Kind {
	case "session":
		return "вы"
	case "local":
		return "вы · с этой машины"
	case "token":
		if id.Token != nil {
			return "токен «" + id.Token.Name + "»"
		}
		return "токен"
	}
	return ""
}

// editOrigin — экран и действие: по ним видно, какой кнопкой правка
// сделана, даже когда параметр давно переехал в другой раздел.
func editOrigin(req actionRequest) string {
	if req.Screen == "" {
		return req.Action
	}
	return req.Screen + " · " + req.Action
}

func (s *Server) execute(req actionRequest) (string, error) {
	for _, group := range []func(actionRequest) (string, error){
		s.mailActions, s.chatsActions, s.scriptsActions,
		s.tasksActions, s.modelsActions, s.pricesActions, s.toolsActions, s.secretsActions, s.memoryActions, s.serversActions,
		s.digestGroupAction, s.filesActions, s.settingsChangesActions, s.healthActions, s.accessActions, s.skillsActions, s.notifyActions, s.queueActions, s.sentActions, s.workActions, s.searchActions, s.learningActions, s.integrationsActions, s.sshRuleActions, s.entityActions, s.telegramActions, s.maxActions, s.agentsActions, s.translateActions, s.vendorsActions, s.assignActionsGroup, s.sharesActions,
	} {
		redirect, err := group(req)
		if errors.Is(err, errUnhandled) {
			continue
		}
		return redirect, err
	}
	return "", fmt.Errorf("unknown action %q", req.Action)
}

// ids — target как список чисел через запятую (множественный выбор).
// waitingTitle — «Ждут вас» у закреплённого блока и обычные
// «Уведомления», когда ждать нечего: закрепление без повода — это шум.
func waitingTitle(badge string) string {
	if badge == "" {
		return "Уведомления"
	}
	return "Ждут вас"
}

func (r actionRequest) ids() ([]int64, error) {
	parts := strings.Split(r.Target, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// one — target как одно число.
func (r actionRequest) one() (int64, error) { return strconv.ParseInt(r.Target, 10, 64) }

// buildScreen собирает экран по id из сайдбара (inbox по умолчанию).
// buildScreen собирает экран. Строка поиска — параметр запроса, а не поле
// сервера: два клиента (мак и браузер) ищут одновременно, и общее поле
// подменяло одному то, что набрал другой.
// buildScreen — экран для широкого окна. Телефон просит своё через
// buildScreenForPhone: на узком экране «Чаты» это список, а не
// последний разговор с панелью за бургером.
func (s *Server) buildScreen(screen, filter, search string, limit int) (*Document, error) {
	return s.buildScreenForPhone(screen, filter, search, limit, false, false)
}

// snapshot — эта сборка идёт не человеку на экран, а в снимок на устройстве
// (группа 2а «Снимок собирается отдельно», offline-snapshot): письма и
// вложения закрытых для облака ящиков в неё физически не попадают, вместо
// «пометить и вычистить у клиента» — см. design.md, решение 8а. Признак
// живёт только в этом вызове, а не полем `Server`: то же правило, что уже
// нарушалось с полем поиска (см. «Грабли»).
func (s *Server) buildScreenForPhone(screen, filter, search string, limit int, phone, snapshot bool, opts ...screenOpt) (*Document, error) {
	menuStart := time.Now()
	menuSQLStart := -1
	if limit <= 0 {
		limit = defaultRows
	}
	if screen == "" {
		// разговор во главе угла: Сёма открывается тем, что ждёт ответа,
		// а не разбором почты
		screen = "ask"
	}
	// метка подсветки едет в адресе перехода («settings:servers@srv-vpn~auto^f:…»),
	// но экраном не является: адрес без неё — тот, что клиент запомнит и что
	// будет перечитывать опросом, а метка уйдёт в документе один раз
	screen, highlight := splitHighlight(screen)
	var viewer screenOptions
	for _, opt := range opts {
		opt(&viewer)
	}
	// сборка целиком, вместе с колонкой настроек (её defer ниже и
	// выполнится раньше этого) — для Server-Timing
	if viewer.timing != nil {
		defer func() { viewer.timing.build = time.Since(menuStart) }()
		menuSQLStart = viewer.timing.sql.Stats().N
	}
	// «Почта» разделена по ящикам: экран inbox@<account>, senders@<account>
	screenName, account := screen, ""
	if base, acct, ok := strings.Cut(screen, "@"); ok && (strings.HasPrefix(screen, "inbox@") || strings.HasPrefix(screen, "senders@")) {
		screenName, account = base, acct
	}
	// «Что ушло наружу» живёт в каркасе настроек (`settings:sent`), а закладки,
	// записи в чужих журналах и старые карточки помнят «sent», «sent~log»,
	// «sent~12»: они открывают то же самое. Подменяется и screen, а не только
	// screenName — по той же причине, что у прежних вкладок настроек ниже: в
	// data-экран уезжает doc.Screen, и клиент должен запомнить новый адрес
	if canon := canonSentAddr(screenName); canon != screenName {
		screenName, screen = canon, canon
	}
	// Отсев — параметром в сами вызовы данных (Digest, ByStatusAccount и
	// соседние), а не состоянием: снимок собирается тем же кодом, что и
	// обычный показ, только с этим доводом на входе. Пусто (nil) — обычный
	// показ, и запросы не меняются вовсе.
	var отсев *store.Exclusion
	if snapshot {
		отсев = s.eng.SnapshotExclusion()
	}
	// счётчик узла «Почта» — по всем ящикам всегда: на «inbox@ящик» он
	// показывал число одного ящика, на «Отправителях» — общее, и цифра в
	// меню прыгала 16 → 48 от одного нажатия вкладки (13 сентября 2026)
	badge := ""
	if n, err := s.eng.Store().CountByStatus(store.StatusPending); err == nil && n > 0 {
		badge = strconv.Itoa(n)
	}
	accounts := s.eng.Accounts()
	var boxes []Screen
	if len(accounts) > 1 { // «Все ящики» имеет смысл, только когда их несколько
		all := Screen{ID: "inbox", Title: "Все ящики", Icon: "tray.full", Tint: "grey"}
		if n, err := s.eng.Store().CountByStatus(store.StatusPending); err == nil && n > 0 {
			all.Badge = strconv.Itoa(n)
		}
		boxes = append(boxes, all)
	}
	dots := mailboxDots
	for i, a := range accounts {
		буква, оттенок := accountTile(s.eng.MailConf(a.Name).Host)
		b := Screen{ID: "inbox@" + a.Name, Title: a.Name, Dot: dots[i%len(dots)],
			Tile: true, Initials: буква, Tint: оттенок}
		if n, err := s.eng.Store().CountByStatusAccount(store.StatusPending, a.Name, nil); err == nil && n > 0 {
			b.Badge = strconv.Itoa(n)
		}
		boxes = append(boxes, b)
	}

	footer := "Ещё не проверял почту"
	if !s.eng.LastPoll.IsZero() {
		footer = "Проверено в " + s.eng.LastPoll.Format("15:04") + " · " + s.eng.ActiveProvider()
	}
	if polling, newN := s.eng.PollState(); polling {
		footer = "Проверяю почту…"
		if newN > 0 {
			footer += fmt.Sprintf(" разобрано %d", newN)
		}
		footer += " · " + s.eng.ActiveProvider()
	}
	if missing, total := s.eng.ArchiveProgress(); missing > 0 {
		footer += fmt.Sprintf("\nАрхив писем · %d из %d", total-missing, total)
	}
	if done, total := s.eng.BackfillProgress(); total > 0 && done < total {
		footer += fmt.Sprintf("\nГлубокий разбор ящика · %d из %d", done, total)
	}

	// уровень и счётчик уведомлений стоят в меню дважды (у «Чатов» и у
	// «Уведомлений») и ещё раз значком приложения — считаются один раз
	notifyLvl, notifyCnt := s.notifyLevel(), s.notifyBadge()
	// список чатов — тоже один раз: он нужен и счётчику, и дереву разговоров
	allChats, _ := s.eng.Store().Chats()
	doc := &Document{
		Title:      "Сёма",
		Components: []Component{}, // пустой список, а не null
		Active:     screen,
		Footer:     footer,
		guest:      viewer.guest,
		offline:    viewer.prefetch,
		Screens: []Screen{
			// три группы по вопросу «зачем я сюда захожу»: что ждёт
			// сегодня, что Сёма делает сам, и настройки за чертой.
			// Разговор — первое, что видит человек: всё, что ждёт
			// решения, приходит карточкой в чат с Сёмой, а не в
			// отдельный список. Поэтому пункта «Ждут вас» в меню нет —
			// он был вторым местом для того же самого; сам экран жив и
			// открывается из шапки разговора
			{ID: "ask", Title: "Чаты", Icon: "bubble.left", Group: "Переписка", Tint: "blue", GroupIcon: "bubble.left",
				// счётчик в списке чатов человек читает как непрочитанное —
				// значит это и должно быть число непрочитанных разговоров.
				// Сколько ждёт решения, говорит точка уровня и подпись
				// самого разговора с Сёмой
				Badge: s.unreadConversations(allChats), Level: notifyLvl, Children: s.chatScreens(screen, allChats, notifyLvl), Open: true,
				// своей строки у узла нет: адрес «ask» открывает тот же
				// разговор с Сёмой, что и первый ребёнок, — двойник
				NoSelf: true,
				// новый чат — прямо из строки «Чатов»: кнопка жила над панелью
				// со списком и пропала вместе с ней
				Add:   &Action{Action: "new_chat", Label: "Новый чат"},
				Words: "чаты разговоры вопрос спросить телеграм макс треды уведомления ждут вас сроки запросы"},
			// уведомления — своим пунктом рядом с перепиской: владелец вернул
			// его в меню 17 сентября 2026. Название постоянное: «Ждут вас» и
			// «Уведомления» сменяли друг друга, а меню от перехода меняться не
			// должно. Цифра здесь — число записей, ждущих решения: на пункте
			// «Уведомления» её не спутать с непрочитанными сообщениями
			{ID: notifyScreen, Title: "Уведомления", Icon: "bell", Tint: "orange", Divider: true,
				Badge: notifyCnt, Level: notifyLvl,
				Words: "уведомления ждут вас пуши напоминания сроки запросы"},
			// ящик один — «Все ящики» не рисуется, и строка «Обзор» стала бы
			// двойником этого единственного ящика
			{ID: "inbox", Title: "Почта", Icon: "envelope", Tint: "blue", Badge: badge, Children: boxes,
				NoSelf: len(accounts) <= 1,
				Words:  "почта письма ящик спам архив разбор"},
			{ID: "files", Title: "Файлы", Icon: "doc", Tint: "grey", Divider: true, Words: "файлы вложения документы сканы чеки pdf"},
			{ID: "tasks", Title: "Задачи", Icon: "clock", Tint: "blue", Group: "Сёма сам", Words: "задачи расписание воркеры сердцебиение очередь"},
			{ID: "scripts", Title: "Скрипты", Icon: "chevron.left.forwardslash.chevron.right", Tint: "violet", Words: "скрипты код javascript"},
			{ID: "modules", Title: "Модули", Icon: "cube", Tint: "green", Words: "модули права события инструменты"},
			{ID: "memory", Title: "Память", Icon: "brain", Tint: "orange", Words: "память записи уроки факты кристаллы граф области агенты"},
			{ID: "health", Title: "Здоровье", Icon: "waveform.path.ecg", Tint: "red", Level: s.healthLevel(),
				Words: "здоровье расход деньги токены бюджеты пульс машина температура очередь"},
			// без подпунктов: три уровня навигации на одном экране —
			// дерево слева, вкладки сверху и список разделов рядом —
			// спорили друг с другом, и человек не понимал, где он стоит.
			// Разбор по областям живёт во вкладках, в меню одна дверь
			{ID: "settings", Title: "Настройки", Icon: "gearshape", Tint: "grey", Divider: true, Group: "Своё",
				Words: settingsWords()},
		},
	}
	// разделы, которые принесли модули: клиент не пересобирается
	for _, sc := range s.eng.ModuleScreens() {
		icon := sc.Icon
		if icon == "" {
			icon = "puzzlepiece.extension"
		}
		// модульные разделы приезжали без группы и висели в конце сами по
		// себе: человеку непонятно, почему «Чеки» стоят особняком
		doc.Screens = append(doc.Screens, Screen{ID: sc.ID, Title: sc.Title, Icon: icon, Tint: "grey", Group: "Своё"})
	}

	// нижняя полоса телефона и чей это Сёма — в шапку меню
	doc.PhoneTabs = barTabs(doc.Screens, s.eng.PhoneTabs(), screen)
	if len(accounts) > 0 {
		doc.Owner = accounts[0].Username
	}
	// число ждущих уведомлений для значка приложения: то же, что на пункте меню
	doc.Wait = notifyCnt

	// «Почта» — один раздел с вкладками «Письма» и «Отправители»
	mailTabs := []Filter{{ID: "inbox", Title: "Письма"}, {ID: "senders", Title: "Отправители"}}

	if account != "" {
		mailTabs = []Filter{{ID: "inbox@" + account, Title: "Письма"}, {ID: "senders@" + account, Title: "Отправители"}}
	}

	// меню, подвал и счётчики готовы — дальше своё у экрана (Server-Timing)
	if viewer.timing != nil {
		viewer.timing.menu = time.Since(menuStart)
		viewer.timing.menuSQL = viewer.timing.sql.Stats().N - menuSQLStart
	}
	var err error
	if mail, model := s.eng.Configured(); !mail || !model {
		if screenName != "settings" && !strings.HasPrefix(screenName, "settings:") {
			doc.Active = "setup"
			return doc, s.buildSetup(doc)
		}
	}
	switch {
	case screenName == "senders" || strings.HasPrefix(screenName, "senders~"):
		doc.Active = "inbox"
		doc.Tabs, doc.ActiveTab = mailTabs, mailTabs[1].ID
		err = s.buildSenders(doc, filter, strings.TrimPrefix(strings.TrimPrefix(screenName, "senders"), "~"), search, отсев, limit)
	case screenName == "health":
		err = s.buildHealth(doc, filter, limit)
	case screenName == "digest:groups":
		doc.Active = "inbox"
		err = s.buildDigestGroups(doc)
	case screenName == "files":
		err = s.buildFiles(doc, filter, search, limit, отсев)
	case screenName == purgedScreen:
		doc.Active = "inbox"
		err = s.buildPurged(doc, filter, search, limit)
	case screenName == "ask":
		if phone {
			// на телефоне «Чаты» — это список, а не последний разговор:
			// список за бургером на 288 точек обрезал имена на середине
			err = s.buildChatsList(doc, filter, search)
			break
		}
		err = s.buildChats(doc, 0, limit)
	case screenName == "scripts" || strings.HasPrefix(screenName, "script:"):
		doc.Active = "scripts"
		var sid int64
		if v, ok := strings.CutPrefix(screenName, "script:"); ok {
			sid, _ = strconv.ParseInt(v, 10, 64)
		}
		err = s.buildScripts(doc, sid)
	case screenName == "modules":
		err = s.buildModules(doc)
	case strings.HasPrefix(screenName, "modules@"):
		err = s.buildModuleCode(doc, strings.TrimPrefix(screenName, "modules@"))
	case screenName == "memory" || strings.HasPrefix(screenName, "memory:") || strings.HasPrefix(screenName, "memory@") || strings.HasPrefix(screenName, "memory~"):
		doc.Active = "memory"
		err = s.buildMemoryScreen(doc, screenName, filter, limit)
	case strings.HasPrefix(screenName, "module:"):
		err = s.buildModuleScreen(doc, screenName)
	case screenName == "tasks":
		err = s.buildTasks(doc, limit)
	case screenName == "categories":
		// категории трат переехали в модуль «Чеки» 3 сентября 2026, а
		// адрес остался и молча открывал «Почту». Экран, который врёт
		// молча, хуже отсутствующего: говорим, куда идти
		doc.Active = "modules"
		err = s.buildCategoriesMoved(doc)
	case screenName == queueScreen:
		doc.Active = "health"
		err = s.buildQueue(doc)
	case screenName == vendorsScreen || strings.HasPrefix(screenName, vendorsScreen+"@"):
		doc.Active = "files"
		err = s.buildVendors(doc, strings.TrimPrefix(strings.TrimPrefix(screenName, vendorsScreen), "@"))
	case screenName == notifyScreen || strings.HasPrefix(screenName, notifyScreen+"@"):
		doc.Active = notifyScreen
		err = s.buildNotifications(doc, strings.TrimPrefix(strings.TrimPrefix(screenName, notifyScreen), "@"), limit)
	case screenName == "settings" || strings.HasPrefix(screenName, "settings:"):
		doc.Active = "settings"
		// прежняя вкладка («settings:каналы») экраном больше не бывает:
		// закладка или ссылка из чужого журнала открывает первый раздел её
		// группы, а не пустую страницу. Подменяется и screen, а не только
		// screenName: в data-экран уезжает doc.Screen = screen, и с одной
		// лишь подменой screenName клиент запоминал «settings:каналы» как
		// текущий экран. Дальше это имя шло заголовком HX-Current-Screen, а
		// кириллица в заголовке HTTP роняет каждый следующий запрос
		if первый, это := tabScreen(screenName); это {
			screenName, screen = первый, первый
		}
		// прежние адреса «Серверов» («~log:имя», «~rules:имя») помнят
		// уведомления и закладки: они открывают экран сервера на своей вкладке,
		// и подменяется адрес целиком — по той же причине, что выше
		if canon := s.canonServersAddr(screenName); canon != screenName {
			screenName, screen = canon, canon
		}
		// у «Интеграций» вкладок раздела нет: прежний хвост («~trackers») открывает
		// сам раздел
		if canon := canonMCPAddr(screenName); canon != screenName {
			screenName, screen = canon, canon
		}
		// «Шифрование» переехало с «Инструментов» в «Ключи и секреты»: закладки и
		// замечания помнят «settings:tools~crypt»
		if canon := canonToolsAddr(screenName); canon != screenName {
			screenName, screen = canon, canon
		}
		// прежние адреса «Почты» (вкладки ящика без ящика, «Спам» с ящиком в
		// адресе) открывают то же, что открывали; общее — без ящика в адресе
		if canon := s.canonMailAddr(screenName); canon != screenName {
			screenName, screen = canon, canon
		}
		// колонка разделов (на телефоне — «назад» в корень) общая для всех
		// настроек, см. settings_karkas.go
		defer s.settingsFrame(doc, screenName, phone)
		// Поиск один на все настройки и доступен из любого раздела.
		//
		// Он живёт в оболочке, а не на «Главном»: человек ищет «пуши»
		// или «срок хранения» оттуда, где стоит, а не возвращаясь в
		// начало. Набранное уводит в общий поиск по разделам — искать
		// строки внутри одного экрана бессмысленно, их там полтора
		// десятка и все на виду
		// с запросом раздел строится как обычно, а «Нашлось в настройках»
		// встаёт над ним (sectionSearch после этого switch): раньше
		// набранное уводило на «Главное», и списки раздела было не найти
		// сам выбор экрана вынесен в buildSettingsBody (settings_body.go): его же
		// зовёт индекс настроек, собирая экраны без оболочки
		search, err = s.buildSettingsBody(doc, screenName, filter, search, limit, phone)
		if req := strings.TrimSpace(search); req != "" && err == nil && doc.ActiveTab != "settings" {
			s.sectionSearch(doc, req, limit)
		}
	case strings.HasPrefix(screenName, "chat:"):
		doc.Active = "ask"
		id, perr := strconv.ParseInt(strings.TrimPrefix(screenName, "chat:"), 10, 64)
		switch {
		case perr != nil:
			err = perr
		case phone:
			// разговор на телефоне — отдельный экран с «назад», без
			// панели: две колонки в 390 точек не помещаются
			err = s.buildChat(doc, id, limit)
			s.markChatRead(id)
			shrinkConversationHeader(doc)
		default:
			err = s.buildChats(doc, id, limit)
		}
	case strings.HasPrefix(screenName, "msg:"):
		doc.Active = "inbox"
		// формат: msg:<id>[~<развёрнутые id через запятую>] («+» в URL значит пробел)
		spec := strings.TrimPrefix(screenName, "msg:")
		base, extra, _ := strings.Cut(spec, "~")
		id, perr := strconv.ParseInt(base, 10, 64)
		if perr != nil {
			err = perr
		} else {
			want := map[int64]bool{}
			for _, p := range strings.Split(extra, ",") {
				if v, e := strconv.ParseInt(strings.TrimSpace(p), 10, 64); e == nil {
					want[v] = true
				}
			}
			err = s.buildMessage(doc, id, want)
		}
	default:
		doc.Tabs, doc.ActiveTab = mailTabs, mailTabs[0].ID
		err = s.buildInbox(doc, filter, limit, account, search, отсев)
	}
	tidyRows(doc.Components)
	// редкое — в «Тонкую настройку» внизу: проход общий, экраны ничего о нём не знают,
	// кроме метки Tier на форме, разделе или поле (settings_rare.go)
	foldRare(doc)
	if snapshot {
		cutUnexcluded(doc)
	}
	// Экран хранимый, если всё его содержимое можно хранить на устройстве.
	// Молчание сервера на этот вопрос читается как «нельзя»: клиент не знает
	// предметной области и не может отличить закрытый ящик от открытого.
	// Сомнение — не хранимый: если не уверены, ошибка в сторону безопасности.
	doc.Storable = s.canStoreScreen(screen)
	// Снимок отличается от показанного везде, где экран может нести почту:
	// сама снимочная сборка — честный отсев, и второй раз объявлять о разнице
	// незачем, иначе клиент, получив уже честную сборку, снова просил бы её.
	// Нехранимому экрану второй запрос не нужен вовсе: снимка у него не
	// будет, а клиент сходил бы за ним впустую.
	//
	// Закрытых ящиков в условии больше нет, и это не мелочь: клиент просит
	// снимочную сборку только по этому признаку, а прежнее «есть хоть один
	// закрытый ящик» в обычном доме ложно — у владельца оба ящика открыты.
	// Тогда на диск ложилась показанная сборка, и отсев «секретного»,
	// сделанный на слое данных, до устройства не доходил вовсе. «Секретное»
	// вырезается в снимочной сборке всегда (store.Отсев.Секретное), значит и
	// спрашивать её надо всегда, где экран вообще может нести почту.
	if !snapshot {
		doc.SnapshotDiffers = doc.Storable && holdsMail(screen)
	}

	tidyToolbar(doc)
	collapseLong(doc.Components, 20)
	doc.Screen = screen
	if err == nil {
		doc.Highlight = highlight
	}
	return doc, err
}

// storableScreens — белый список: на диск устройства ложится только то, что
// названо здесь поимённо.
//
// Почему белый, а не чёрный. Чёрный список закрывает ровно то, о чём
// подумали, а требование спеки — «ни при каких настройках»: содержимое
// закрытых ящиков, записи класса «секретное» и хранилище секретов в снимок
// не попадают. Четыре запрета и `return true` в конце этого не держали:
// «Стёртое» отдавало тему письма и имя ящика, уведомление несёт тему письма
// в заголовке, экран модуля ядро рисует вслепую и не знает, что там внутри,
// — и каждый следующий новый экран уезжал бы на телефон молча, просто
// потому что о нём никто не вспомнил. Сомнение — не хранимый.
//
// Хвост адреса хранимость не меняет: «@ящик» и «~вкладка» разрешены у
// названного экрана, но сделать хранимым экран, которого здесь нет, не могут.
var storableScreens = map[string]bool{
	// Почта, отправители и файлы: закрытые ящики и «секретное» из снимочной
	// сборки вырезает отсев на слое данных (store.Отсев, группа 2а), поэтому
	// решать по имени ящика в адресе здесь нечего. Полку «Ждут вас», до
	// которой отсев не доходит, из снимка убирает cutUnexcluded.
	"inbox":   true,
	"senders": true,
	"files":   true,
	// Справочник групп дайджеста: приметы, которые завёл человек, писем нет.
	"digest:groups": true,
	// Своё состояние: расход по потребителям, пульс, очередь работ. Чужого
	// содержимого нет — вид работы, номер объекта и числа.
	"health": true,
	"queue":  true,
	// Настройки — свои параметры и справочники. Не хранятся только те, где
	// лежат сами ключи и двери: хранилище секретов, «Доступ» и «Что ушло»
	// с полным текстом отправок.
	//
	// Журнал команд на серверах, списки чатов Telegram и MAX и журнал
	// вызовов MCP хранятся по решению владельца (27 сентября 2026), хотя в
	// них и лежит чужое: вывод команд, имена собеседников, ответы серверов.
	// Довод владельца — снимок ложится на его же отмеченное устройство и
	// показывает ровно то, что он и так там видит.
	"settings":              true,
	"settings:overview":     true,
	"settings:servers":      true,
	"settings:telegram":     true,
	"settings:max":          true,
	"settings:integrations": true,
	"settings:mail":         true,
	"settings:models":       true,
	"settings:tools":        true,
	"settings:storages":     true,
	"settings:notify":       true,
	"settings:search":       true,
	"settings:learning":     true,
	"settings:skills":       true,
	"settings:changes":      true,
	"settings:watch":        true,
	"settings:translate":    true,
}

// cutUnexcluded — что ядро не умеет отсеять на слое данных, в снимок не
// едет вовсе.
//
// Сегодня это одна полка: «Ждут вас» на «Почте» (notifyInboxBlock). Заголовок
// уведомления о письме — это его тема и отправитель («Ответить коллега@… ·
// «тема»»), а отсев (store.Отсев) до полки не доходит: уведомления собираются
// своей дорогой и ящика в себе не несут. Пока это так, полки в снимке нет —
// иначе тема письма закрытого ящика легла бы на диск телефона.
//
// Человек от этого офлайн ничего не теряет: единственная кнопка полки ведёт
// на «Уведомления», а они не хранимы, и без связи это была бы дорога в
// пустоту. Сторожит зарубка «уведомление» в TestSnapshotWithoutClosedAccounts: полку
// переименуют — тест назовёт экран.
func cutUnexcluded(doc *Document) {
	// новый срез, а не запись в тот же массив: `out := comps[:0]` молча
	// переписывает уже отданные строки (см. «Грабли» про разделы)
	оставить := make([]Component, 0, len(doc.Components))
	for _, c := range doc.Components {
		if c.Type == "shelf" && c.Title == "Ждут вас" {
			continue
		}
		оставить = append(оставить, c)
	}
	doc.Components = оставить
}

// canStoreScreen — можно ли сохранить этот экран в снимке на устройстве.
// Не хранимый по умолчанию: хранимо только названное в storableScreens, а у
// письма своя, точная проверка — по ящику именно этого письма и его классу.
func (s *Server) canStoreScreen(screen string) bool {
	// Письмо: msg:<id>[~<развёрнутые>]. Ящик скопом здесь не судья — одно
	// письмо открытого ящика хранимо, даже когда соседний ящик закрыт.
	if strings.HasPrefix(screen, "msg:") {
		spec := strings.TrimPrefix(screen, "msg:")
		base, _, _ := strings.Cut(spec, "~")
		msgID, err := strconv.ParseInt(base, 10, 64)
		if err != nil {
			// Не смогли разобрать ID — сомнение, не хранимый
			return false
		}

		msg, err := s.eng.Store().Message(msgID)
		if err != nil || msg == nil {
			// Письма нет или ошибка БД — сомнение, не хранимый
			return false
		}

		// Письмо из закрытого ящика не хранится
		closedAccounts := s.eng.ClosedAccounts(true)
		if closedAccounts[msg.Account] {
			return false
		}

		// Письмо класса «секретное» не хранится
		class, _ := s.eng.Store().ContentClass(store.ClassMsg, msgID)
		if class == store.ClassSecret {
			return false
		}

		return true
	}

	// Имя экрана — всё до «@ящика» и до «~вкладки»: судим по экрану, а не по
	// его хвосту. Экран модуля (module:<имя>:<экран>) имени в списке не имеет
	// и не получит: что рисует модуль, ядро не знает.
	name := screen
	if i := strings.IndexAny(name, "@~"); i >= 0 {
		name = name[:i]
	}
	return storableScreens[name]
}

// holdsMail — может ли экран показать письма или вложения, чьё
// происхождение — ящик. Список нарочно щедрый: ошибиться в сторону
// «просить снимок отдельно, когда он совпал бы» дешевле, чем не спросить
// там, где закрытый ящик есть. Это не запрет, а вопрос «нужен ли клиенту
// второй, снимочный запрос» (см. Document.SnapshotDiffers).
// Письмо (msg:) сюда не входит: у него своя, точная проверка — по ящику
// этого письма и его классу, а не по всему ящику скопом.
func holdsMail(screen string) bool {
	if strings.HasPrefix(screen, "inbox") || strings.HasPrefix(screen, "senders") ||
		strings.HasPrefix(screen, "chat:") || strings.HasPrefix(screen, "digest:") {
		return true
	}
	switch screen {
	case "files", "ask", "notifications", "notify", "categories":
		return true
	}
	return false
}

// tidyToolbar — в шапке остаётся одно действие, остальные уходят под «…».
// Три кнопки по 44 точки на телефоне переносятся вторым рядом и растят
// шапку до 130 точек — выше самого списка. Правило то же, что у строк, и
// применяется ко всем экранам сразу, а не переписыванием каждого.
// Главное действие (`primary`) остаётся видимым всегда.
func tidyToolbar(doc *Document) {
	if len(doc.ToolbarActions) < 2 {
		return
	}
	видимые := make([]Action, 0, 2)
	скрытые := doc.ToolbarMenu
	оставлено := false
	for _, д := range doc.ToolbarActions {
		switch {
		// дорога назад — не действие: без неё человек не выйдет с экрана
		// письма и журнала. Переход куда-то ещё («Группы», «Сессии») —
		// обычное действие и уходит под «…» наравне с прочими
		case д.Action == "$goto" && strings.HasPrefix(д.Label, "‹"), д.Style == "primary":
			видимые = append(видимые, д)
		case !оставлено && д.Style != "destructive":
			видимые = append(видимые, д)
			оставлено = true
		default:
			скрытые = append(скрытые, д)
		}
	}
	doc.ToolbarActions, doc.ToolbarMenu = видимые, скрытые
}

// tidyRows — общий проход по собранному экрану: в строке остаётся одно
// действие, остальные уходят в меню. Экраны при этом не переписываются
// поодиночке: 200 кнопок «Удалить», 130 «Открыть» и 106 «Проверить»
// исчезают со всех сразу, а глаз получает иерархию.
//
// Что остаётся видимым: тумблер (это состояние, а не действие), главные
// действия (`primary` — «Выполнить» и «Отклонить» у команды на
// подтверждении нужны оба) и первое обычное. Разрушительное не
// показывается никогда — только в меню.
func tidyRows(cs []Component) {
	for i := range cs {
		// «Опасное»: кнопка у строки своя и видимая — карточка сама
		// предупреждение, и прятать разрушительное в «···» здесь значит
		// отнять у человека то, зачем он сюда пришёл
		if cs[i].Type == "section" && cs[i].Style == "danger" {
			continue
		}
		if cs[i].Type == "row" {
			tidyRow(&cs[i])
		}
		tidyRows(cs[i].Children)
	}
}

func tidyRow(c *Component) {
	if len(c.Actions) == 0 {
		return
	}
	var keep, hide []Action
	seenPlain := false
	for _, a := range c.Actions {
		switch {
		// переход кнопкой не рисуется вовсе — по нему кликается строка,
		// и считать его «тем самым одним действием» неправильно: у
		// отправителя тогда «Вернуть из спама» уезжало под «…»
		case a.Label == "toggle" || a.Style == "primary" || a.Action == "$goto" || a.Action == "$file":
			keep = append(keep, a)
		case a.Style == "destructive":
			hide = append(hide, a)
		case !seenPlain:
			seenPlain = true
			keep = append(keep, a)
		default:
			hide = append(hide, a)
		}
	}
	if len(hide) == 0 {
		return
	}
	c.Actions = keep
	c.Menu = append(c.Menu, hide...)
}

// Списки отдаются порциями: полная перерисовка сотен строк в SwiftUI
// стоит секунды процессорного времени.
const defaultRows = 50

// plural — «1 письмо», «3 письма», «61 письмо»
func plural(n int, one, few, many string) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod10 == 1 && mod100 != 11:
		return fmt.Sprintf("%d %s", n, one)
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return fmt.Sprintf("%d %s", n, few)
	default:
		return fmt.Sprintf("%d %s", n, many)
	}
}

// plural2 — то же склонение, но без числа: число уже стоит в строке
// рядом. «1 стоят внимания» на «Почте» жило до 10 сентября 2026.
func plural2(n int, one, few, many string) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod10 == 1 && mod100 != 11:
		return one
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return few
	default:
		return many
	}
}

var monthNames = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря"}

var avatarPalette = []string{"#C7532F", "#E0B100", "#7952B3", "#2E7DD1", "#4E9A51", "#B33A72", "#5A6ACF", "#3A8FA3"}

// ServeTLS — тот же сервер по защищённому соединению, рядом с обычным.
// Без него телефон остаётся зрителем: браузер разрешает passkey, установку
// на домашний экран и пуши только по https (исключение — localhost).
//
// Обычный порт при этом не выключается: клиент на маке ходит по нему, и
// ломать работающее ради нового нельзя.
func ServeTLS(ctx context.Context, addr, cert, key string, eng *engine.Engine) error {
	// адрес входа по умолчанию — от адреса прослушивания и на этом порту
	srv := &http.Server{
		Addr:              addr,
		Handler:           liveServer(eng).Handler(),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      5 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(),
			time.Duration(eng.StopSoftSec())*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	// сертификат и порт — до объявления, что защищённый порт есть: открытый
	// порт переадресует сюда страницы (perf-pass 7.4), и вести на неработающий
	// адрес нельзя
	if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if _, port, err := net.SplitHostPort(ln.Addr().String()); err == nil {
		tlsPort.Store(port)
		defer tlsPort.Store("")
	}
	log.Printf("Sema API on https://%s", addr)
	if err := srv.ServeTLS(ln, cert, key); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// tlsPort — порт работающего защищённого сервера ("" — его нет).
var tlsPort atomic.Value

// redirectToTLS — открытый порт ведёт страницу на защищённый (perf-pass 7.4):
// старая закладка http://sema.local:8765 открывалась без HTTP/2 и с секундой
// на разрешение имени. Только переходы на страницу (GET «/» и «/login») и
// только по имени хоста — сертификат выписан на имя, а не на адрес;
// /api/* (хуки, житель машины, проверка здоровья), экраны, действия, ссылки
// на файлы и запросы через прокси — как были. Выключается настройкой.
func (s *Server) redirectToTLS(w http.ResponseWriter, r *http.Request) bool {
	port, _ := tlsPort.Load().(string)
	if port == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	if r.URL.Path != "/" && r.URL.Path != "/login" {
		return false
	}
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Forwarded-Proto") != "" || r.TLS != nil {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || host == "localhost" || net.ParseIP(strings.Trim(host, "[]")) != nil {
		return false
	}
	if !s.eng.RedirectTLS() {
		return false
	}
	// временная и без кэша: постоянную браузер запомнил бы навсегда, и
	// выключенная настройка или упавший TLS оставили бы http-закладку
	// ведущей на мёртвый порт (ревью perf-pass 7)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "https://"+net.JoinHostPort(host, port)+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	return true
}

func Serve(ctx context.Context, addr string, eng *engine.Engine) error {
	s := liveServer(eng)
	s.auth.SetListen(addr)
	handler := s.Handler()
	srv := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.redirectToTLS(w, r) {
				return
			}
			handler.ServeHTTP(w, r)
		}),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      5 * time.Minute, // действия ходят в IMAP
	}
	go func() {
		<-ctx.Done() // SIGINT/SIGTERM: останавливаем и HTTP, не только движок
		// пять секунд не хватало: действие может ходить в IMAP или на
		// сервер по SSH, и обрыв на середине оставляет работу без записи
		// результата. Срок — настройка: на маленькой машине он больше
		мягкий := time.Duration(eng.StopSoftSec()) * time.Second
		log.Printf("закрываюсь: доделываю начатое, не дольше %s", мягкий)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), мягкий)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("Sema API on http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func boolValue(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// splitWords разбирает список слов-примет, введённый человеком.
func splitWords(raw string) []string {
	var out []string
	for _, w := range strings.Split(raw, ",") {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// httpGet — скачать модуль по ссылке. Отдельная маленькая функция, чтобы
// не тащить в обработчик действий целый клиент.
func httpGet(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("сервер ответил кодом %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(raw), err
}

// mailboxDots — цвета ящиков: одни и те же в боковом меню и в капсулах строк.
var mailboxDots = []string{"#D93025", "#FC3F1D", "#2E7DD1", "#4E9A51", "#7952B3"}

// mailboxBadge — капсула ящика цветом из бокового меню; пустое имя — основной.
func (s *Server) mailboxBadge(name string) Badge {
	accounts := s.eng.Accounts()
	if name == "" && len(accounts) > 0 {
		name = accounts[0].Name
	}
	for i, a := range accounts {
		if a.Name == name {
			return Badge{Text: name, Color: mailboxDots[i%len(mailboxDots)]}
		}
	}
	return Badge{Text: name, Color: "#8E8E93"}
}

// barTabs — три места, куда ходят чаще всего. Берём их из самого
// меню, а не описываем заново: у пункта уже есть значок, название,
// счётчик и уровень, и второй список разошёлся бы с первым. Названного
// раздела нет (модуль выключили) — вкладка просто не показывается.
func barTabs(меню []Screen, names []string, screen string) []Screen {
	найти := func(id string) (Screen, bool) {
		for _, э := range меню {
			if э.ID == id {
				return э, true
			}
		}
		return Screen{}, false
	}
	var out []Screen
	for _, name := range names {
		э, ok := найти(name)
		if !ok {
			continue
		}
		// на полосе «Уведомления» зовутся «Ждут вас» и в закреплённом
		// блоке, и здесь: заголовок пункта меняется со счётчиком, а
		// подпись вкладки должна стоять на месте
		if э.ID == notifyScreen {
			э.Title = "Ждут вас"
		}
		out = append(out, э)
	}
	return out
}

// shrinkConversationHeader — шапка разговора на телефоне в одну строку:
// «назад», имя и «…». Всё остальное уезжает под «…»: заголовок, длинная
// подпись, две выпадашки и две кнопки занимали полэкрана до первой
// реплики. Выпадашки остаются — модель и усилие меняют прямо в
// разговоре, — но идут второй строкой, которая прокручивается вбок.
func shrinkConversationHeader(doc *Document) {
	// «назад» — обратный путь над заголовком, а не кнопка в ряду
	// управления: в ряду она уезжала в конец полосы прокрутки, за край
	// экрана, и вернуться было нечем
	// имя разговора в пути не повторяем — оно и так стоит заголовком
	// строкой ниже; в пути нужен только ход назад
	doc.Components = append([]Component{{Type: "crumb",
		Actions: []Action{{Action: "$goto", Target: "ask", Label: "Чаты"}}}}, doc.Components...)
	doc.ToolbarMenu = append(doc.ToolbarActions, doc.ToolbarMenu...)
	doc.ToolbarActions = nil
	// подпись говорит то же, что и первая реплика: на широком экране она
	// объясняет, куда попал человек, а на телефоне съедает три строки
	doc.Subheading = ""
}

// accountTile — буква и оттенок плитки ящика в меню по провайдеру. Смотрим
// хост, а не домен адреса: свой домен часто живёт у Яндекса или Google.
func accountTile(host string) (буква, оттенок string) {
	h := strings.ToLower(host)
	switch {
	case strings.Contains(h, "yandex"):
		return "Я", "red"
	case strings.Contains(h, "gmail"), strings.Contains(h, "google"):
		return "G", "blue"
	case strings.Contains(h, "mail.ru"):
		return "@", "orange"
	}
	return "@", "sky"
}
