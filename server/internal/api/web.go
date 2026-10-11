package api

// Интерфейс в браузере. Экран собирается один раз (`buildScreen`), а
// отдаётся в двух видах: JSON — клиенту на маке, HTML — браузеру. Здесь
// живёт только перевод одного в другое; знание предметной области
// остаётся в сборке экранов, а разметка — в пакете web.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// handleWebUI — кусок страницы для htmx: меню, панель и сам экран.
// телефон — узкое окно. Ширину знает только клиент, и он говорит о ней
// заголовком: экран, а не стиль, решает, что показать в «Чатах».
func phoneUA(r *http.Request) bool { return r.Header.Get("X-Sema-Phone") == "1" }

func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
	if !s.allowScreen(w, r, r.URL.Query().Get("screen")) {
		return
	}
	t, r := startTiming(r)
	defer s.stopTiming(t, r)
	s.eng.AdoptClientTimezone(r.Header.Get("X-Sema-TZ"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if limit <= 0 {
		limit = defaultRows
	}
	// часть экрана — содержимое ленивого раздела: приезжает при
	// раскрытии, без шапки и меню, см. parts.go
	if часть := r.URL.Query().Get("part"); часть != "" {
		s.writePart(w, r, r.URL.Query().Get("screen"), часть, search, limit)
		return
	}
	// снимочная сборка — тем же путём, тем же запросом, только с этим
	// параметром: клиент просит её вторым заходом, когда прошлый ответ
	// сказал Document.SnapshotDiffers (группа 2а offline-snapshot)
	снимок := r.URL.Query().Get("snapshot") == "1"
	opts := s.viewerOpts(r)
	// подогрев при наведении (perf-pass 7): только разрешённые экраны и
	// без всего, что ходит наружу, — наведение не намерение человека.
	// Экрану без сети показать нечего — 204, и нажатие откроет его как
	// обычно
	подогрев := r.Header.Get("X-Sema-Prefetch") == "1"
	if подогрев {
		if снимок || !prefetchable(r.URL.Query().Get("screen")) {
			noPrefetch(w)
			return
		}
		opts = append(opts, asPrefetch())
	}
	doc, err := s.buildScreenForPhone(r.URL.Query().Get("screen"), r.URL.Query().Get("filter"), search, limit, phoneUA(r), снимок, opts...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if подогрев && doc.partial {
		noPrefetch(w)
		return
	}
	// признак ставит обработчик: сессия — свойство запроса, а не
	// построенного документа (offline-snapshot, группа 3.3)
	doc.OwnDevice = s.ownDevice(r)
	s.writeWeb(w, r, doc, search, limit)
}

// handleWebDo — действие из браузера. htmx шлёт форму, поэтому значения
// полей приходят обычными параметрами, а что делать — в hx-vals.
// requireHtmx — запросы, меняющие состояние, принимаются только от
// оболочки: htmx 4 шлёт `HX-Request: true`, а обычная форма с чужого
// сайта — нет. При `auth.local_open` сессии не требуется, и без этой
// проверки любой сайт в той же сети выполнил бы действие от имени хозяина.
func requireHtmx(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("HX-Request") == "true" {
		return true
	}
	http.Error(w, "запрос не из оболочки Сёмы", http.StatusForbidden)
	return false
}

// errNothingSelected — групповое действие без единой галочки: раньше на
// сервер уходила цель «$selected» и человек видел «strconv.ParseInt: …»
var errNothingSelected = errors.New("ничего не отмечено: поставьте галочки у писем и повторите")

func (s *Server) handleWebDo(w http.ResponseWriter, r *http.Request) {
	if !requireHtmx(w, r) {
		return
	}
	t, r := startTiming(r)
	defer s.stopTiming(t, r)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		_ = r.ParseForm()
	}
	req := actionRequest{
		Action: r.FormValue("action"),
		Target: r.FormValue("target"),
		Screen: r.FormValue("screen"),
		Filter: r.FormValue("filter"),
		Params: map[string]string{},
	}
	for name, значения := range r.Form {
		switch name {
		case "action", "target", "screen", "filter", "limit":
			continue
		}
		if len(значения) > 0 {
			req.Params[name] = значения[0]
		}
	}
	// «$selected» — групповое действие над отмеченными строками: браузер
	// присылает их поле «выбрано» столько раз, сколько отмечено
	if req.Target == "$selected" {
		req.Target = strings.Join(r.Form["выбрано"], ",")
		if req.Target == "" {
			req.Screen = currentScreen(r)
			s.webError(w, r, req, errNothingSelected)
			return
		}
	}
	// htmx отправляет ответ на hx-prompt заголовком, а не полем
	if спрошено := r.Header.Get("HX-Prompt"); спрошено != "" {
		req.Params["value"] = спрошено
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	// невыбранный флажок браузер не отправляет вовсе: восстанавливаем «false»,
	// иначе тумблер нельзя выключить — сервер просто не узнает об этом
	for _, name := range strings.Split(r.FormValue("toggles"), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, has := req.Params[name]; !has {
			req.Params[name] = "false"
		}
	}
	// кнопка формы несёт действие в своём name/value: htmx 4 не читает
	// hx-vals с кнопки отправки, а браузер кладёт в данные формы ровно ту
	// кнопку, что нажали. Поле формы с именем «action» при этом остаётся
	// полем формы — у бюджета такое есть
	if к := r.FormValue("_кнопка"); к != "" {
		действие, target, _ := strings.Cut(к, "|")
		req.Action, req.Target = действие, target
		delete(req.Params, "_кнопка")
	}
	if req.Screen == "" {
		req.Screen = currentScreen(r)
	}
	if !s.allowAction(w, r, req) {
		return
	}

	// выполнить, а не execute: обёртка подписывает правки настроек тем,
	// кто их сделал. Сохранение полосой идёт этой же дорогой — иначе
	// половина правок в журнале была бы без имени
	acted := time.Now()
	redirect, err := s.executeSigned(r, req)
	t.action = time.Since(acted)
	// сохранение нижней полосой: ответ — да или нет, а не экран целиком.
	// Форм на экране настроек несколько, сохраняются они по одной, и
	// подмена экрана после первой же стёрла бы правки в остальных —
	// человек нажал «Сохранить», а половина работы исчезла. Заодно отказ
	// достаётся той форме, которая его вызвала, и стоит у её строки, а не
	// общей красной строкой наверху, где непонятно, о чём он.
	if r.Header.Get(saveHeader) == "1" {
		w.Header().Set(serverTimingHeader, t.header())
		replyJSON(w, err)
		return
	}
	if err != nil {
		s.webError(w, r, req, err)
		return
	}
	if redirect == "" {
		redirect = req.Screen
	}
	if !s.allowScreen(w, r, redirect) {
		return
	}
	filter, search, limit := keepListContext(r, redirect, req.Screen, req.Filter, search)
	doc, err := s.buildScreenForPhone(redirect, filter, search, limit, phoneUA(r), false, s.viewerOpts(r)...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc.OwnDevice = s.ownDevice(r)
	s.writeWeb(w, r, doc, search, limit)
}

// Заголовки с положением человека в списке: отбор, поиск и порция. Клиент ставит
// их на каждый запрос (app.js), а разбирает сервер — только там, где без них
// список прыгал бы.
const (
	filterHeader = "X-Sema-Filter"
	searchHeader = "X-Sema-Search"
	limitHeader  = "X-Sema-Limit"
)

// keepListContext — после действия на том же экране настроек список остаётся
// таким, каким человек его видел: тот же отбор, тот же поиск, та же порция.
// Иначе нашёл чат поиском, нажал клетку в матрице прав — и поиск пропал, а
// список вернулся к первой порции: нужной строки на экране уже нет. Только для
// настроек: на остальных экранах действие по-прежнему перечитывает экран с нуля.
// Если действие увело на другой экран, чужой отбор ему не годится.
func keepListContext(r *http.Request, redirect, current, filter, search string) (string, string, int) {
	if redirect != current || !strings.HasPrefix(redirect, "settings:") {
		return filter, search, 0
	}
	decode := func(name string) string {
		v := r.Header.Get(name)
		if d, err := url.PathUnescape(v); err == nil {
			return d
		}
		return v
	}
	if filter == "" {
		filter = decode(filterHeader)
	}
	if search == "" {
		search = strings.TrimSpace(decode(searchHeader))
	}
	limit, _ := strconv.Atoi(decode(limitHeader))
	if limit < 0 {
		limit = 0
	}
	return filter, search, limit
}

// saveHeader — признак того, что форму отправила нижняя полоса
// сохранения, а не кнопка самой формы.
const saveHeader = "X-Sema-Save"

// replyJSON — итог сохранения одной формы. Отказ едет с кодом 200:
// это не поломка сервера, а ответ «так нельзя, вот почему», и клиент
// показывает его у строки. Код ошибки увёл бы ответ к общему обработчику
// отказов, который уводит на страницу входа и подменяет окно.
func replyJSON(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	ответ := map[string]any{"ок": err == nil}
	if err != nil {
		ответ["ошибка"] = err.Error()
	}
	_ = json.NewEncoder(w).Encode(ответ)
}

// webError — отказ показывается на том же экране плашкой, а не белой
// страницей с текстом ошибки.
func (s *Server) webError(w http.ResponseWriter, r *http.Request, req actionRequest, err error) {
	filter, search, limit := keepListContext(r, req.Screen, req.Screen, req.Filter, "")
	doc, berr := s.buildScreenForPhone(req.Screen, filter, search, limit, phoneUA(r), false, s.viewerOpts(r)...)
	if berr != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc.Components = append([]Component{{Type: "text", Style: "error", Text: err.Error()}}, doc.Components...)
	doc.OwnDevice = s.ownDevice(r)
	s.writeWeb(w, r, doc, search, limit)
}

// writeWeb отдаёт разметку экрана. Запрос нужен ради сжатия: экран
// «Отправители» — 453 КБ разметки, и по Wi-Fi до Pi это полсекунды
// только на передачу. Ручка /api/ui сжимала ответ с самого начала, а
// веб-путь — нет, хотя именно им и ходит единственный живой клиент.
func (s *Server) writeWeb(w http.ResponseWriter, r *http.Request, doc *Document, search string, limit int) {
	w.Header().Set(web.BuildHeader, web.Build())
	if limit <= 0 {
		limit = defaultRows
	}
	э := toScreen(doc)
	э.Limit = limit
	if doc.rows > 0 {
		э.Limit = doc.rows // показанная порция — её и удвоит «Показать ещё»
	}
	э.SearchQuery = search
	ls := s.eng.LiveSettings()
	э.PollSec, э.PollSecWired = ls.PollS, ls.PollLiveS
	э.Prefetch, э.PrefetchMs = strings.Join(prefetchPrefixes, " "), s.eng.PrefetchMs()
	// версия словаря и срок жизни снимка — не свойства документа, а того,
	// чем его отдают: первая входит в ключ снимка, второй решает, когда
	// снимок стереть (offline-snapshot, группа 4)
	э.Dictionary, э.SnapshotDays = UIDictVersion, ls.SnapshotDays
	rendered := time.Now()
	markup, err := web.Render(э)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if t := timingOf(r); t != nil {
		t.render = time.Since(rendered)
		w.Header().Set(serverTimingHeader, t.header())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// на диск браузера экраны не ложатся: в них письма и секреты, и
	// «no-store» тут дороже сэкономленного запроса
	w.Header().Set("Cache-Control", "no-store")
	web.WriteMaybeGzip(w, r, []byte(markup))
}

// ---- перевод документа в разметку ----

// clearFragmentSearch — на экране одно поле поиска. Когда оно есть сверху
// (Searchable), ленивые части своего не рисуют: набранное сверху уходит
// и в них (app.js, загрузитьЧасть). Два поля на «Telegram» читались как
// ошибка — «зачем два поиска» (13 сентября 2026).
func clearFragmentSearch(cs []Component) {
	for i := range cs {
		cs[i].SearchHint = ""
		clearFragmentSearch(cs[i].Children)
	}
}

func toScreen(d *Document) web.Screen {
	if d.Searchable {
		clearFragmentSearch(d.Components)
	}
	э := web.Screen{
		Title:      d.Heading,
		Subheading: d.Subheading,
		Width:      d.Layout,
		Addr:       d.Screen,
		Active:     d.ActivePanel,
		PanelTitle: d.PanelTitle,
		PanelKind:  d.PanelKind,
		Tab:        d.ActiveTab,
		Filter:     d.ActiveFilter,
		Highlight:  d.Highlight,
		Search:     d.Searchable,
		SearchHint: d.SearchHint,
		Wizard:     toWizard(d.Wizard),
		Footer:     footerLines(d.Footer),
		// признаки снимка доезжают до браузера только отсюда: разметку
		// рисует пакет web, и до этой строки ни один из трёх в неё не
		// попадал вовсе (offline-snapshot, 4.0)
		Storable:        d.Storable,
		SnapshotDiffers: d.SnapshotDiffers,
		OwnDevice:       d.OwnDevice,
	}
	if d.Notice != nil {
		э.Banner = d.Notice.Text
	}
	section := ""
	for _, э2 := range d.Screens {
		п := toMenuItem(э2, d.Screen, d.Active)
		// раздел наследуется: подпись группы стоит только у первого пункта
		if п.Group != "" {
			section = п.Group
		}
		п.Section = section
		э.Items = append(э.Items, п)
	}
	э.Owner = d.Owner
	э.Waiting = d.Wait
	э.OptionSets = d.OptionSets
	// счётчик и уровень собираем с детей той же меркой, что и меню: у
	// «Почты» в меню стоит 3 по всем ящикам, и полоса не должна
	// говорить другое число про то же самое
	for _, в := range d.PhoneTabs {
		счёт, level := в.Badge, в.Level
		if len(в.Children) > 0 {
			счёт, level = childSummary(в, счёт, level)
		}
		э.TabBar = append(э.TabBar, web.Item{Addr: в.ID, Name: в.Title, Icon: в.Icon,
			Count: счёт, Level: level, Selected: insidePath(в, d.Screen)})
	}
	for _, п := range d.Panel {
		пункт := web.Item{Addr: п.ID, Name: п.Title, Small: п.Subtitle, Level: п.Level,
			Count: п.Meta, Group: п.Group, Selected: п.ID == d.ActivePanel, Nested: п.Nested,
			Depth: п.Depth, Kind: п.Kind, Open: п.Open, Inside: п.Under, InGroup: п.InGroup}
		if п.Delete != nil {
			к := toButton(*п.Delete)
			пункт.Delete = &к
		}
		э.Panel = append(э.Panel, пункт)
	}
	if d.PanelAction != nil {
		к := toButton(*d.PanelAction)
		э.PanelAdd = &к
	}
	for _, в := range d.Tabs {
		э.Tabs = append(э.Tabs, web.Chip{Addr: в.ID, Name: в.Title, Count: в.Badge, Level: в.Level})
	}
	э.Subtab = d.ActiveSubtab
	for _, в := range d.Subtabs {
		э.Subtabs = append(э.Subtabs, web.Chip{Addr: в.ID, Name: в.Title, Count: в.Badge, Level: в.Level})
	}
	for _, ф := range d.Filters {
		э.Filters = append(э.Filters, web.Chip{Addr: ф.ID, Name: ф.Title, Count: ф.Badge, Level: ф.Level})
	}
	for _, д := range d.ToolbarActions {
		э.Buttons = append(э.Buttons, toButton(д))
	}
	for _, д := range d.ToolbarMenu {
		э.MenuButtons = append(э.MenuButtons, toButton(д))
	}
	for _, с := range d.HeaderSelects {
		э.Selects = append(э.Selects, toList(с))
	}
	for _, к := range d.Components {
		// обратный путь живёт в шапке, а не в ленте: у пятнадцати вкладок
		// настроек заголовок один и тот же, и где именно стоишь, видно
		// только по подсвеченному пункту меню — которого на телефоне нет
		if к.Type == "crumb" && э.Here == "" {
			э.Here = к.Title
			for _, д := range к.Actions {
				э.Breadcrumb = append(э.Breadcrumb, toButton(д))
			}
			continue
		}
		э.Blocks = append(э.Blocks, toBlock(к))
	}
	э.SaveBar = hasDeferred(э.Blocks)
	if d.Composer != nil {
		ввод := web.Composer{
			Placeholder: d.Composer.Placeholder,
			FieldName:   d.Composer.Field,
			Submit:      toButton(d.Composer.Submit),
			To:          d.Composer.Peer,
			CallWord:    d.Composer.Call,
		}
		if d.Composer.Attach != nil {
			к := toButton(*d.Composer.Attach)
			ввод.Attach = &к
		}
		э.Composer = &ввод
	}
	return э
}

// hasDeferred — есть ли на экране хоть одна форма, которую сохраняет
// нижняя полоса. Формы лежат и внутри разделов, поэтому смотрим вглубь:
// на «Почте» все формы стоят по разделам, и без обхода детей полоса не
// появилась бы ни разу.
func hasDeferred(блоки []web.Block) bool {
	for _, б := range блоки {
		if б.Deferred || hasDeferred(б.Children) {
			return true
		}
	}
	return false
}

func toButton(д Action) web.Button {
	return web.Button{
		Action: д.Action, Target: д.Target, Name: д.Label, Kind: д.Style,
		Prompt: д.Prompt, Confirm: д.Confirm, Masked: д.Secure, Value: д.Value, ConfirmOK: д.ConfirmOK,
		Deferrable: д.Deferrable,
	}
}

func toList(с Select) web.Select {
	return web.Select{Name: с.Name, Label: с.Label, Value: с.Value,
		Options: с.Options, OptionSet: с.OptionsRef, Action: toButton(с.Action)}
}

// rowSelects — списки в строке, подписанные для чтения вслух именем
// самой строки. «Основная» в строке роли понятна глазам: рядом видно,
// чья это роль. Скринридер соседей не видит и читает девять раз подряд
// «Основная», ни разу не сказав, к чему она относится.
func rowSelects(name string, списки []Select) []web.Select {
	out := make([]web.Select, 0, len(списки))
	for _, с := range списки {
		л := toList(с)
		switch {
		case name == "":
		case л.Label == "":
			л.SpokenLabel = name
		default:
			л.SpokenLabel = name + " — " + strings.ToLower(л.Label)
		}
		out = append(out, л)
	}
	return out
}

// pillsPerRow — сколько капсул ряда `chipset` показывается, прежде чем
// остальное сворачивается в хвост «ещё N». Двенадцать — это две строки
// на широком экране и три на телефоне; больше ряд перестаёт читаться и
// становится обоями.
const pillsPerRow = 12

// startsWithDigit — показатель это число, а не фраза, и потому набирается
// моноширинным. «3 мин назад» и «0 в очереди» начинаются с цифры, но
// числом не являются: моноширинным в 32 точки они расползаются на две
// строки и кричат громче настоящих цифр.
func startsWithDigit(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && r != ',' && r != '.' && r != ' ' && r != '\u00a0' {
			return false
		}
	}
	return true
}

// toMenuItem — пункт меню со всеми потомками. Раскрыт тот узел, внутри
// которого стоит человек: меню — не состояние, которое надо помнить, а
// следствие адреса экрана. Тогда оно не может «застрять» раскрытым, и
// после любого действия перерисовывается тем же.
// toMenuItem — пункт меню. активный — пункт, к которому экран относится
// (doc.Active): «Отправители» — это семья «Почты», хотя адрес у них свой,
// и узел «Почта» на них остаётся раскрытым и выбранным. Иначе меню
// перестраивалось от одного нажатия вкладки: ящики сворачивались, пункт
// гас (13 сентября 2026).
func toMenuItem(э Screen, screen, активный string) web.Item {
	п := web.Item{
		Addr: э.ID, Key: э.ID, Name: э.Title, Icon: э.Icon, Level: э.Level, Count: э.Badge,
		Group: э.Group, Selected: э.ID == screen, Pinned: э.Pinned, Words: э.Words,
		Tint: э.Tint, Tile: э.Tile, Initials: э.Initials, Divider: э.Divider, GroupIcon: э.GroupIcon,
	}
	if э.Add != nil {
		к := toButton(*э.Add)
		п.Add = &к
	}
	for _, д := range э.Children {
		// детям «активный» не передаётся: кого подсветить в узле, решает
		// сам узел по ящику из адреса (pickChild), иначе «Все ящики»
		// с адресом inbox перехватывал бы подсветку у «senders@ящик»
		п.Children = append(п.Children, toMenuItem(д, screen, ""))
	}
	п.IsNode = len(п.Children) > 0
	if п.IsNode {
		// подсветка узла живёт на одном из его пунктов, а не на самом узле:
		// на «Все ящики» белели и узел, и пункт, на ящике — только пункт, на
		// «Отправителях» — только узел, и заголовок «Почта» мигал от каждого
		// нажатия (запись 13 сентября 2026). Пункт выбирается по ящику из
		// адреса экрана: «senders@ящик» подсвечивает тот же ящик, что и
		// «inbox@ящик», без ящика — первый пункт
		п.Selected = false
		if insidePath(э, screen) || (активный != "" && э.ID == активный) {
			pickChild(п.Children, screen)
		}
		п.Remember = э.Open
	} else if !п.Selected && активный != "" && э.ID == активный {
		п.Selected = true
	}
	// у «Почты» первый ребёнок — «Все ящики» с тем же адресом: строка
	// «Обзор» была бы его двойником
	// своя строка гасится только у настоящего узла: без детей пункт —
	// обычная кнопка, и без адреса он стал бы мёртвым. На свежей установке
	// чатов ещё нет, и «Чаты» не открывались бы вовсе
	if э.NoSelf && п.IsNode {
		п.Addr = ""
	}
	for _, д := range п.Children {
		if д.Addr == п.Addr {
			п.Addr = ""
			break
		}
	}
	if п.IsNode {
		// у группы без адреса ID пустой: с пустым активным она раскрывалась бы вся
		п.Open = э.Open || insidePath(э, screen) || (активный != "" && э.ID == активный)
		// свёрнутый узел показывает, что у него внутри: счётчик и
		// уровень поднимаются с детей, иначе ждущее письмо в закрытой
		// ветке становится невидимым
		if !п.Open {
			п.Count, п.Level = childSummary(э, п.Count, п.Level)
		}
	}
	return п
}

// pickChild — подсветить в узле ровно один пункт: точное совпадение
// адреса, иначе пункт с тем же ящиком («@ящик» в хвосте), иначе первый.
func pickChild(children []web.Item, screen string) {
	// выбранный может жить глубже — в группе или в теме: тогда первого
	// подсвечивать нельзя, иначе горят два пункта сразу
	if hasSelected(children) {
		return
	}
	if len(children) == 0 {
		return
	}
	if _, ящик, ок := strings.Cut(screen, "@"); ок {
		for i := range children {
			if strings.HasSuffix(children[i].Addr, "@"+ящик) {
				children[i].Selected = true
				return
			}
		}
	}
	children[0].Selected = true
}

// insidePath — стоит ли человек в этом узле или в ком-то из его потомков.
func insidePath(э Screen, screen string) bool {
	if э.ID == screen {
		return true
	}
	for _, д := range э.Children {
		if insidePath(д, screen) {
			return true
		}
	}
	return false
}

// childSummary — счётчик суммой и худший уровень: свёрнутый узел обязан
// сказать, что внутри что-то ждёт.
func childSummary(э Screen, счётчик, level string) (string, string) {
	сумма, свой := 0, false
	if n, err := strconv.Atoi(счётчик); err == nil {
		сумма, свой = n, true
	}
	вес := map[string]int{"": 0, LevelOK: 1, LevelInfo: 2, LevelWarn: 3, LevelAlert: 4}
	худший := level
	var walk func(Screen)
	walk = func(s Screen) {
		for _, д := range s.Children {
			if n, err := strconv.Atoi(д.Badge); err == nil {
				сумма, свой = сумма+n, true
			}
			if вес[д.Level] > вес[худший] {
				худший = д.Level
			}
			walk(д)
		}
	}
	walk(э)
	if !свой || сумма == 0 {
		return счётчик, худший
	}
	return strconv.Itoa(сумма), худший
}

func toBlock(к Component) web.Block {
	б := web.Block{
		ID: к.ID, Text: к.Text, Title: к.Title, Name: к.Subtitle,
		Caption: к.Caption, Meta: к.Meta, MetaInline: к.MetaInline, Level: к.Level, Style: к.Style,
		Selectable: к.Selectable, Unit: к.Unit, Columns: к.Columns,
		Collapsed: к.Collapsed, Open: к.Expanded, Expand: к.Expand, Toggle: к.Toggle, Deferred: к.Deferred,
		Fragment: к.Lazy, FragmentSearch: к.SearchHint,
		Output: к.Detail, Fraction: к.Progress, CountdownTo: к.Until,
		ImageURL: к.Image,
		FrameURL: к.Frame, BlockedImages: к.FrameImages, Translation: к.FrameTranslated,
		Sparkline: sparkline(к.Spark),
	}
	switch к.Type {
	case "card":
		б.Kind = "карточка"
	case "metric":
		б.Kind = "показатель"
		// «пусто», «3 мин назад» — это не число, а слово: моноширинным
		// в 32 точки такое расползается на две строки и кричит громче
		// настоящих цифр
		if !startsWithDigit(к.Text) {
			б.Style = "прозой"
		}
	case "alarm":
		б.Kind = "тревога"
	case "figs":
		б.Kind = "числа"
	case "shelf":
		б.Kind = "полка"
	case "rowgroup":
		б.Kind = "группа-строк"
	case "matrix":
		б.Kind = "матрица"
	case "table":
		б.Kind = "таблица"
	case "chipset":
		б.Kind = "капсулы-ряд"
	case "step":
		б.Kind = "шаг"
	case "thinking":
		// именное ожидание: «Сёма читает файл · DeepseekPro · 4 с». Не
		// «загрузка»: локальная модель думает минутами, и человек должен
		// видеть, кого он ждёт и сколько, а не крутящийся кружок
		б.Kind = "ожидание"
	case "toolcard":
		б.Kind = "карточка-дела"
		if web.HasIcon(к.Icon) {
			б.RowIcon = к.Icon
		}
	case "chain":
		б.Kind = "цепочка"
	case "classpicker":
		б.Kind = "класс"
		if web.HasIcon(к.Icon) {
			б.RowIcon = к.Icon
		}
	case "switcher":
		б.Kind = "переключатель"
	case "patterns":
		// правила сервера тремя колонками: колонки — дети, образцы — дети
		// колонок, а «×» и «добавить» — их действия
		б.Kind = "образцы"
	case "crumb":
		// путь обычно поднимается в шапку (`toScreen`); сюда он попадает,
		// только если пришёл вложенным — тогда рисуем строкой
		б.Kind = "путь"
	case "header":
		б.Kind = "заголовок"
	case "group":
		б.Kind = "группа"
	case "divider":
		б.Kind = "черта"
	case "text":
		б.Kind = "текст"
	case "bubble":
		б.Kind = "пузырь"
		б.Role = roleOf(к.Role)
	case "section":
		б.Kind = "раздел"
		if к.Title != "" {
			б.Title = к.Title
		}
	case "cards":
		б.Kind = "карточки"
	case "form":
		б.Kind = "форма"
	case "button":
		б.Kind = "кнопки"
	case "row":
		if к.Style == "empty" {
			б.Kind = "пусто"
			// на маке у пустого места иконка 26 pt и одно действие — вебу то же
			б.Avatar = iconSF(к.Icon)
			break
		}
		б.Kind = "строка"
		// поля, которые веб терял по дороге: под них уже есть стили
		б.Avatar, б.AvatarColor, б.AvatarKind = к.Avatar, к.AvatarColor, к.AvatarShape
		if б.Avatar == "" && к.Icon != "" {
			// значок из набора; нет такого — первая буква имени символа,
			// как было: это хотя бы отличает виды карточек друг от друга
			if web.HasIcon(к.Icon) {
				б.RowIcon, б.AvatarColor = к.Icon, к.IconColor
			} else {
				б.Avatar, б.AvatarColor, б.AvatarKind = firstRune(к.Icon), к.IconColor, "icon"
			}
		}
		// строка, чьё тело догружается по требованию: на маке клиент
		// подменяет экран (`senders~<адрес>`, `msg:<id>~<письмо>`), в
		// браузере тяжёлое уезжает на свой экран — клик открывает его
		if к.Expandable && к.Detail == "" && б.GoTo == "" {
			switch {
			case strings.HasPrefix(к.ID, "snd-"):
				б.GoTo = "senders~" + strings.TrimPrefix(к.ID, "snd-")
			case strings.HasPrefix(к.ID, "thr-"):
				б.GoTo = "msg:" + strings.TrimPrefix(к.ID, "thr-")
			}
		}
		б.Tone, б.Wide = к.Tint, к.Layout == "wide"
		if б.Meta == "" {
			б.Meta = к.Meta
		}
	case "graph":
		б.Kind = "граф"
		б.Graph = layoutGraph(к.Graph)
		if б.Graph == nil {
			// пустой граф — не картинка в никуда, а объяснение
			б.Kind = "пусто"
			б.Name = "Связей пока нет"
			б.Caption = "Граф рисуется по связям, которые Сёма находит в записях памяти"
			return б
		}
		// запасной вид: список узлов рядом с рисунком — по нему ищут глазами
		for i, н := range к.Graph.Nodes {
			if i >= graphListed {
				break
			}
			// строка ведёт туда же, куда узел на рисунке: на его экран
			б.Children = append(б.Children, web.Block{Kind: "строка", Name: н.Label, Caption: н.Area, GoTo: н.Target})
		}
		return б
	case "":
		// Без вида — это не «неизвестный компонент», а носитель значения
		// внутри родителя, который рисует детей сам: у «чисел» ребёнок
		// несёт число и подпись, а разметку даёт родитель.
		//
		// Раньше такой ребёнок попадал в ветку ниже, и его значение
		// затиралось словами «неизвестный компонент: ». На «Файлах»
		// вместо «260 файлов · 31.9 МБ · 4 источника» стояло три раза
		// «неизвестный компонент» — то есть converter портил данные,
		// которых не понял, вместо того чтобы их не трогать.
	default:
		б.Kind = "текст"
		б.Text = "неизвестный компонент: " + к.Type
	}

	if к.Badge != "" {
		// старые экраны присылают капсуле цвет, а не уровень — переводим,
		// иначе все капсулы серые
		б.Pills = append(б.Pills, web.Pill{Text: к.Badge, Level: levelByColor(к.BadgeColor), Color: к.BadgeColor})
	}
	for _, кап := range к.Badges {
		ур := кап.Level
		if ур == "" {
			ур = levelByColor(кап.Color)
		}
		б.Pills = append(б.Pills, web.Pill{Text: кап.Text, Level: ур, Color: кап.Color})
	}
	// ряд капсул не растёт бесконечно: восемьдесят образцов круга чтения
	// и пятьдесят четыре модели занимали семь строк экрана. Показываем
	// столько, сколько влезает в две строки, и честно говорим, сколько
	// осталось — счёт человеку важнее самих имён
	if б.Kind == "капсулы-ряд" && len(б.Pills) > pillsPerRow {
		б.More = fmt.Sprintf("ещё %d", len(б.Pills)-pillsPerRow)
		б.Pills = б.Pills[:pillsPerRow]
	}
	for _, п := range к.Facts {
		б.Facts = append(б.Facts, web.Prop{Label: п.Label, Value: п.Value, Color: п.Color})
	}
	for _, п := range к.Props {
		б.Props = append(б.Props, web.Prop{Label: п.Label, Value: п.Value, Color: п.Color})
	}
	for _, с := range к.Cmds {
		команда := web.Command{Text: с.Text, Note: с.Note}
		for _, д := range с.Actions {
			команда.Buttons = append(команда.Buttons, toButton(д))
		}
		б.Commands = append(б.Commands, команда)
	}
	for _, ф := range к.Files {
		б.Files = append(б.Files, web.File{ID: ф.ID, Name: ф.Name, Meta: ф.Meta, IsImage: ф.Image, Path: ф.Path})
	}
	for _, кл := range к.Cells {
		клетка := web.Cell{Tooltip: кл.Title, State: кл.State, Inherited: кл.Inherited,
			Text: кл.Text, Level: кл.Level, Button: toButton(кл.Action)}
		// «это Сёма померил» — моноширинным и по правому краю: в столбце
		// цифры должны стоять под цифрами. Число узнаётся само, остальное
		// (адрес, цена, время) помечает тот, кто собирает экран
		клетка.Mono = кл.Mono || startsWithDigit(кл.Text)
		if кл.Select != nil {
			список := toList(*кл.Select)
			клетка.Select = &список
		}
		б.Cells = append(б.Cells, клетка)
	}
	for _, д := range к.Actions {
		// переход и файл кнопкой не рисуем: по ним кликается вся строка
		switch д.Action {
		case "$goto":
			// в карточке дела и в цепочке кликается не весь блок, а
			// кнопка: без этого «Открыть скрипт» и «Ещё N писем»
			// пропадали молча — действие было, а нажать нечего
			if б.Kind == "карточка-дела" || б.Kind == "цепочка" {
				б.Buttons = append(б.Buttons, toButton(д))
				continue
			}
			// главное действие остаётся кнопкой, даже если это переход.
			//
			// Строка замечания «Роль „Чаты“ не отвечает» несла действие
			// «Разобраться», а на экране у неё не было ни одной кнопки:
			// переход молча становился кликом по всей строке. Для списка
			// это верно — там строка и есть вещь, которую открывают. Для
			// замечания нет: человек должен видеть команду, а не
			// догадываться, что строка куда-то ведёт. Кликается при этом
			// по-прежнему и вся строка — кнопка её не отменяет
			if д.Style == "primary" {
				б.Buttons = append(б.Buttons, toButton(д))
				if б.GoTo == "" {
					б.GoTo = д.Target
				}
				continue
			}
			б.GoTo = д.Target
			continue
		case "$open":
			// найденное в сети проверяется по ссылке, а не по кнопке:
			// кнопка в ряду действий читается как поручение Сёме, а это
			// всего лишь адрес, куда можно сходить самому
			if б.Kind == "карточка-дела" && strings.HasPrefix(д.Target, "http") {
				б.Links = append(б.Links, toButton(д))
				continue
			}
		case "$file":
			if id, err := strconv.ParseInt(strings.TrimSpace(д.Target), 10, 64); err == nil {
				б.FileID = id
				continue
			}
		}
		б.Buttons = append(б.Buttons, toButton(д))
	}
	for _, д := range к.Menu {
		б.Menu = append(б.Menu, toButton(д))
	}
	б.Selects = append(б.Selects, rowSelects(к.Subtitle, к.Selects)...)
	for _, поле := range к.Fields {
		п := toField(поле)
		// id поля уникален в пределах страницы: к имени поля добавляем id
		// формы. Без этого две формы с полем «keep_days» делили один id, и
		// подпись общей настройки переключала настройку ящика
		if к.ID != "" {
			п.AnchorID = к.ID + "-" + п.Name
		}
		б.Fields = append(б.Fields, п)
	}
	if к.Submit != nil {
		б.Buttons = append([]web.Button{toButton(*к.Submit)}, б.Buttons...)
	}
	for _, дитя := range к.Children {
		б.Children = append(б.Children, toBlock(дитя))
	}
	if б.Kind == "таблица" {
		б.RightAligned = rightColumns(б)
		speakTableLists(&б)
	}
	if б.Kind == "матрица" {
		// у строки матрицы своё меню — тогда последняя колонка отдана ему у всех
		// строк разом, иначе ячейки поехали бы
		for _, дитя := range б.Children {
			if len(дитя.Buttons) > 0 || len(дитя.Menu) > 0 {
				б.Controls = true
				break
			}
		}
	}
	return б
}

// speakTableLists — подпись для чтения вслух у списков в ячейках таблицы:
// «Чаты — основная». Глаз берёт её из заголовка колонки, скринридер заголовка
// у ячейки не видит и читал бы три списка подряд без имени роли и колонки.
func speakTableLists(table *web.Block) {
	for i := range table.Children {
		row := &table.Children[i]
		for j := range row.Cells {
			list := row.Cells[j].Select
			if list == nil || list.SpokenLabel != "" {
				continue
			}
			// колонки: первая — название строки, ячейки идут со второй
			column := ""
			if j+1 < len(table.Columns) {
				column = strings.ToLower(table.Columns[j+1])
			}
			switch {
			case row.Name != "" && column != "":
				list.SpokenLabel = row.Name + " — " + column
			case row.Name != "":
				list.SpokenLabel = row.Name
			default:
				list.SpokenLabel = column
			}
		}
	}
}

// rightColumns — какие колонки таблицы стоят по правому краю: те, где
// все значения машинные. Заголовок обязан стоять над своим столбцом:
// подпись «Писем» слева над числами справа читается как чужая.
func rightColumns(б web.Block) []bool {
	право := make([]bool, len(б.Columns))
	for i := range право {
		// первая колонка — название строки, последняя — управление:
		// они не числа никогда
		if i == 0 || i == len(право)-1 {
			continue
		}
		has := false
		все := true
		for _, стр := range б.Children {
			if i-1 >= len(стр.Cells) {
				continue
			}
			кл := стр.Cells[i-1]
			if кл.Text == "" {
				continue
			}
			has = true
			все = все && кл.Mono
		}
		право[i] = has && все
	}
	return право
}

// roleOf пузыря словами разметки: свой, ушло от вашего имени, собеседник,
// служебное, Сёма.
func roleOf(r string) string {
	switch r {
	case "user":
		return "свой"
	case "sent":
		return "ушло"
	case "peer":
		return "собеседник"
	case "note":
		return "служебное"
	}
	return "сёма"
}

// handleWebUpload — файл, выбранный скрепкой в переписке.
func (s *Server) handleWebUpload(w http.ResponseWriter, r *http.Request) {
	if !requireHtmx(w, r) {
		return
	}
	screen := r.URL.Query().Get("screen")
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "файл не принят: "+err.Error(), http.StatusBadRequest)
		return
	}
	файл, шапка, err := r.FormFile("файл")
	if err != nil {
		http.Error(w, "нет файла", http.StatusBadRequest)
		return
	}
	defer файл.Close()
	данные, err := io.ReadAll(файл)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	att, _, err := s.eng.AddFile(r.Context(), шапка.Filename, шапка.Header.Get("Content-Type"), данные,
		engine.FileSource{Kind: store.SourceUpload, Ref: screen}, nil)
	if err != nil {
		s.webError(w, r, actionRequest{Screen: screen}, err)
		return
	}
	id := att.ID
	if chatID, ok := strings.CutPrefix(screen, "chat:"); ok {
		if n, err := strconv.ParseInt(chatID, 10, 64); err == nil {
			if err := s.eng.AttachFileToChat(n, id); err != nil {
				s.webError(w, r, actionRequest{Screen: screen}, err)
				return
			}
		}
	}
	doc, err := s.buildScreenForPhone(screen, "", "", 0, phoneUA(r), false, s.viewerOpts(r)...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc.OwnDevice = s.ownDevice(r)
	s.writeWeb(w, r, doc, "", 0)
}

// graphDrawn — сколько узлов графа на рисунке: больше не помещается
// по-человечески. Остальные отобранные — «Списком» под рисунком; подзаголовок
// экрана называет оба числа (buildMemoryGraph).
const graphDrawn = 44

// graphListed — сколько узлов в списке под рисунком.
const graphListed = 200

// layoutGraph — раскладка графа памяти на сервере. Клиент на htmx своего
// JavaScript почти не имеет, тянуть ради этого библиотеку незачем, а
// силовая раскладка в браузере — та самая работа, которую мы решили не
// делать дважды.
//
// Раскладка простая и предсказуемая: узлы по кругу, самые связанные —
// ближе к середине. Она не «красивая», зато одинаковая при каждом
// открытии: прыгающий граф читать невозможно.
func layoutGraph(g *Graph) *web.Graph {
	if g == nil || len(g.Nodes) == 0 {
		return nil
	}
	// Кольца были тесные: шестьдесят узлов на четыре кольца по 80 —
	// подписи наезжали друг на друга, и граф читался как каша. Сделали
	// поле шире, колец больше, а в кольце — столько, сколько влезает по
	// длине окружности при шаге в 92 пикселя между подписями
	const (
		ширина     = 1080.0
		высота     = 700.0
		предел     = graphDrawn
		шагПодписи = 92.0
	)
	// считаем связи: чем больше, тем ближе к середине и крупнее
	вес := map[string]int{}
	for _, e := range g.Edges {
		вес[e.From]++
		вес[e.To]++
	}
	узлы := append([]GraphNode(nil), g.Nodes...)
	// на экране узла рисунок строится вокруг него: сам узел в середине, его
	// соседи — ближе всех, дальше по числу связей. Иначе слабосвязанный узел
	// не попадал в graphDrawn самых связанных, и экран открывался без него
	выбран, соседи := "", map[string]bool{}
	for _, н := range узлы {
		if н.Selected {
			выбран = н.ID
		}
	}
	if выбран != "" {
		for _, e := range g.Edges {
			if e.From == выбран {
				соседи[e.To] = true
			}
			if e.To == выбран {
				соседи[e.From] = true
			}
		}
	}
	ранг := func(id string) int {
		switch {
		case id == выбран && выбран != "":
			return 2
		case соседи[id]:
			return 1
		}
		return 0
	}
	sort.SliceStable(узлы, func(i, j int) bool {
		if ri, rj := ранг(узлы[i].ID), ранг(узлы[j].ID); ri != rj {
			return ri > rj
		}
		return вес[узлы[i].ID] > вес[узлы[j].ID]
	})
	total := len(узлы)
	if len(узлы) > предел {
		узлы = узлы[:предел]
	}

	место := map[string][2]float64{}
	out := &web.Graph{Width: ширина, Height: высота, Total: total}
	цx, цy := ширина/2, высота/2
	// раскладка по кольцам: сколько узлов влезает в кольцо, столько и
	// ставим — остальные уходят на следующее
	кольца := [][2]int{{0, 1}} // {первый номер, вместимость}
	место0 := 1
	for r := 1; место0 < len(узлы); r++ {
		радиус := float64(r) * 118
		вместимость := int(2 * math.Pi * радиус / шагПодписи)
		if вместимость < 1 {
			вместимость = 1
		}
		кольца = append(кольца, [2]int{место0, вместимость})
		место0 += вместимость
	}
	// шаг колец подгоняем под поле: иначе внешнее кольцо вылезало за
	// картинку, и половина подписей оказывалась срезанной
	шагКольца := 118.0
	if n := float64(len(кольца) - 1); n > 0 {
		if влезает := (высота/2 - 46) / n; влезает < шагКольца {
			шагКольца = влезает
		}
	}
	номерКольца := func(i int) (кольцо, номер, вКольце int) {
		for k := len(кольца) - 1; k >= 0; k-- {
			if i >= кольца[k][0] {
				return k, i - кольца[k][0], кольца[k][1]
			}
		}
		return 0, 0, 1
	}
	for i, н := range узлы {
		кольцо, номер, вКольце := номерКольца(i)
		if вКольце < 1 {
			вКольце = 1
		}
		// каждое кольцо повёрнуто на пол-шага: подписи соседних колец не
		// встают в одну линию
		угол := 2*math.Pi*float64(номер)/float64(вКольце) + float64(кольцо)*math.Pi/float64(вКольце*2)
		радиус := float64(кольцо) * шагКольца
		x, y := цx+радиус*math.Cos(угол), цy+радиус*math.Sin(угол)
		место[н.ID] = [2]float64{x, y}
		размер := 9.0 + math.Min(float64(вес[н.ID])*1.6, 13)
		out.Nodes = append(out.Nodes, web.Node{
			ID: н.ID, Name: н.Label, Area: н.Area, X: x, Y: y, Radius: размер,
			Color: н.Color, Dim: н.Dim, Selected: н.Selected, Addr: н.Target,
		})
	}
	// легенда: какая область каким цветом — по показанным узлам
	seen := map[string]string{}
	var порядок []string
	for _, н := range узлы {
		if н.Area == "" || н.Color == "" || seen[н.Area] != "" {
			continue
		}
		seen[н.Area] = н.Color
		порядок = append(порядок, н.Area)
	}
	for _, name := range порядок {
		out.Legend = append(out.Legend, web.AreaLegend{Name: name, Color: seen[name]})
	}
	for _, e := range g.Edges {
		a, ok1 := место[e.From]
		b, ok2 := место[e.To]
		if !ok1 || !ok2 {
			continue // узел не попал в показанные — связь рисовать некуда
		}
		out.Edges = append(out.Edges, web.Edge{X1: a[0], Y1: a[1], X2: b[0], Y2: b[1], Label: e.Label})
	}
	return out
}

// levelByColor — старые экраны присылают капсуле цвет, а не уровень;
// переводим в уровень, иначе все капсулы серые
func levelByColor(цвет string) string {
	// имена уровней словами: сервер иногда шлёт их вместо кода цвета
	switch strings.ToLower(цвет) {
	case "red":
		return "alert"
	case "orange", "yellow":
		return "warn"
	case "green":
		return "ok"
	case "blue":
		return "info"
	case "gray", "grey":
		return ""
	}
	switch strings.ToUpper(цвет) {
	case "#1E7A32", "#4E9A51", "#28C840":
		return "ok"
	case "#0058B0", "#2E7DD1", "#0063CC", "#7952B3":
		return "info"
	case "#B25000", "#B7791F", "#E0B100", "#FF9500":
		return "warn"
	case "#C4271C", "#C93400", "#FF3B30":
		return "alert"
	}
	return ""
}

func toField(поле Field) web.Field {
	п := web.Field{
		Kind: поле.Type, Name: поле.Name, Label: поле.Label, Hint: поле.Hint,
		Value: поле.Value, Options: поле.Options, Unit: поле.Unit,
		Immediate: поле.Instant, Min: поле.Min, Max: поле.Max, Step: поле.Step,
		Tones: поле.Tones, Disabled: поле.Disabled, More: поле.More,
	}
	if поле.Default != nil {
		п.Default, п.HasDefault = *поле.Default, true
		п.NotDefault = fieldChanged(поле)
	}
	п.AnchorID = п.Name
	// шаг, на который значение не делится, ломает отправку формы: браузер
	// считает поле неверным и молча ничего не шлёт. Такой шаг убираем
	if п.Step > 0 {
		if v, err := strconv.ParseFloat(strings.TrimSpace(п.Value), 64); err == nil {
			ост := math.Mod(v-п.Min, п.Step)
			if ост > 1e-9 && п.Step-ост > 1e-9 {
				п.Step = 0
			}
		}
	}
	return п
}

// toWizard — пошаговая форма одной страницей: в вебе листать нечем, а
// видеть все шаги сразу удобнее.
func toWizard(w *Wizard) *web.Wizard {
	if w == nil {
		return nil
	}
	м := &web.Wizard{Title: w.Title, Submit: toButton(w.Submit), Cancel: toButton(w.Cancel)}
	for _, ш := range w.Steps {
		шаг := web.Step{Title: ш.Title, Hint: ш.Hint}
		for _, п := range ш.Fields {
			шаг.Fields = append(шаг.Fields, toField(п))
		}
		м.Steps = append(м.Steps, шаг)
	}
	return м
}

// sparkline — точки мини-графика для SVG 160×28: последние значения ряда,
// как `Sparkline` на маке. Меньше двух точек — рисовать нечего.
func sparkline(ряд []float64) string {
	if len(ряд) < 2 {
		return ""
	}
	lo, hi := ряд[0], ряд[0]
	for _, v := range ряд {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if hi <= lo {
		hi = lo + 1
	}
	var b strings.Builder
	for i, v := range ряд {
		x := float64(i) * 160 / float64(len(ряд)-1)
		y := 26 - (v-lo)/(hi-lo)*24
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	return b.String()
}

// iconSF — знак для пустого места по имени символа SF, который просит
// экран. Веб не умеет SF Symbols, а пустое место без знака выглядит
// голым заголовком; неизвестное имя даёт нейтральный кружок.
func iconSF(name string) string {
	switch strings.Split(name, ".")[0] {
	case "tray":
		return "📥"
	case "checkmark":
		return "✓"
	case "key":
		return "🔑"
	case "bell":
		return "🔔"
	case "envelope":
		return "✉"
	case "doc", "folder":
		return "📄"
	case "server", "network":
		return "🖥"
	case "person":
		return "👤"
	case "magnifyingglass":
		return "🔍"
	case "brain", "sparkles":
		return "✦"
	case "":
		return ""
	}
	return "○"
}

// firstRune — значок SF Symbol в вебе нарисовать нечем; показываем первую
// букву имени как аватар, чтобы место не пустовало
func firstRune(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return ""
}

// footerLines — статусная строка меню по строчкам: первая говорит, жив
// ли разбор (точка), у строк вида «1 240 из 6 716» появляется полоса хода.
// Сервер шлёт тот же текст, что и раньше, — разбирается он здесь.
func footerLines(text string) []web.FooterLine {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []web.FooterLine
	for i, строка := range strings.Split(text, "\n") {
		строка = strings.TrimSpace(строка)
		if строка == "" {
			continue
		}
		с := web.FooterLine{Text: строка}
		if i == 0 {
			с.Dot = LevelOK
			if strings.HasPrefix(строка, "Ещё не") {
				с.Dot = LevelWarn
			}
		}
		if m := reFraction.FindStringSubmatch(строка); m != nil {
			сделано := numberFromLine(m[1])
			total := numberFromLine(m[2])
			if total > 0 {
				с.Fraction = float64(сделано) / float64(total)
			}
		}
		out = append(out, с)
	}
	return out
}

// reFraction — «1 240 из 6 716»: пробелы внутри числа неразрывные, потому что
// их ставит humanCount
var reFraction = regexp.MustCompile(`([0-9][0-9\x{00a0} ]*) из ([0-9][0-9\x{00a0} ]*)`)

func numberFromLine(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	return n
}

// currentScreen — какой экран открыт у человека, из заголовка запроса.
//
// Клиент кодирует значение (encodeURIComponent): в адресах экранов бывает
// кириллица — «settings:servers@хозяин~log», — а заголовок HTTP терпит
// только ISO-8859-1, и браузер с кириллицей внутри роняет сам запрос
// ошибкой «non ISO-8859-1 code point», ещё до сервера. Старое значение
// без кодирования читается как есть: PathUnescape его не трогает.
func currentScreen(r *http.Request) string {
	v := r.Header.Get("HX-Current-Screen")
	if д, err := url.PathUnescape(v); err == nil {
		return д
	}
	return v
}

// hasSelected — выбран ли кто-то в поддереве.
func hasSelected(children []web.Item) bool {
	for _, д := range children {
		if д.Selected || hasSelected(д.Children) {
			return true
		}
	}
	return false
}
