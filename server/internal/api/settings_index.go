package api

// Индекс настроек: каждая строка настройки и каждое поле формы — одной
// записью, с путём словами, адресом экрана и меткой строки.
//
// Раньше поиск по настройкам находил раздел по синонимам: набираешь «окно» —
// получаешь «Серверы», а до окна дальше идёшь сам, через вкладку, сервер и
// ещё одну вкладку. Теперь он находит саму настройку и ведёт к её строке
// (`Document.Highlight`). Тот же индекс даёт человеческие имена параметров
// «Журналу настроек» (вместо `notify.mac_last_err`) и контролы блоку «Под
// рукой» на Обзоре.
//
// Как он собирается. Индекс не ведёт своего списка настроек — это был бы второй
// список рядом с экранами, который разошёлся бы с ними через неделю. Он обходит
// **сами сборщики экранов**: каждый раздел, его внутренние вкладки, «Тонкую
// настройку» и экраны сущностей (серверы, ящики, модели, провайдеры, серверы MCP,
// секреты, агенты, хранилища, скиллы, учётки Telegram — настоящие, из данных).
// Что на экране есть строкой настройки или полем формы, то и в индексе; новый
// экран попадает в него сам, не зная о нём. Что экрана не имеет — не в индексе,
// и тест `TestIndexHasEverySettingOfRegistry` перебирает реестр умолчаний, чтобы
// такая настройка не прошла незамеченной.
//
// Сборка ничего не делает наружу и ничего не пишет: ни сети, ни записей в
// базу, ни чтения значений секретов (в журнале секретов от неё ни строки), ни
// вопросов жителю машины. Экран для индекса собирается с признаком
// `Document.offline`: сборщик, которому для показа нужен такой шаг, берёт
// запомненное или молчит. Держит `TestIndexBuildTouchesNothing`.
//
// Значения секретов, токены, пароли, фразы в индекс не попадают никогда: поля
// вида `secure` индексируются подписью без значения, и значение прячется у
// каждого поля, чьё имя говорит о ключе или пароле.
//
// Индекс не собирается на каждый запрос экрана. У рабочего сервера (Serve,
// ServeTLS) он строится в фоне — через несколько секунд после старта и через
// секунду тишины после действия человека, меняющего состав экранов, — а пока
// новый не готов, поиск и Обзор отвечают по прежнему; индекса ещё нет вовсе —
// экран говорит «индекс собирается» и не ждёт (perf-pass 4.2–4.3: первое
// открытие «Настроек» на Pi ждало его 1,2 с). Сервер из New (тесты) собирает
// в запросе, как раньше: тесту нужен индекс, видящий только что сделанное.
// Живёт под своим замком и не знает, чей запрос его спросил.

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// indexTTL — сколько собранный индекс считается свежим, если после него никто
// ничего не менял руками: сервер, найденный жителем машины, или модель, которую
// подключил модуль, видны поиску не позже этого срока.
const indexTTL = 5 * time.Minute

// indexMaxScreens — потолок на число собираемых экранов: индекс обязан кончаться,
// даже если какой-то справочник разросся.
const indexMaxScreens = 1500

// Виды записи индекса.
const (
	entrySetting = "setting" // строка настройки или поле формы
	entryEntity  = "entity"  // сущность: сервер, ящик, модель, секрет…
)

// indexEntry — одна запись индекса.
type indexEntry struct {
	Kind  string
	Title string // подпись настройки или имя сущности
	Hint  string // подсказка настройки или фраза про сущность
	// Path — путь словами: группа, раздел, сущность, внутренняя вкладка
	Path []string
	// Addr — экран, где строка лежит («settings:servers@srv-vpn~auto»), Anchor — метка
	// строки для Document.Highlight («f:…», «r:…»); у сущности метки нет
	Addr, Anchor string
	// Fine — строка лежит в «Тонкой настройке» экрана: клиент раскроет её сам
	Fine bool
	// Section — адрес раздела («settings:servers»); Admin — раздел виден только
	// хозяину (вход и доступ)
	Section string
	Admin   bool
	// Careful — менять «с ходу» нельзя: из «Опасного», настройка входа и доступа,
	// доверия, самостоятельности серверов. В «Под рукой» не берётся никогда
	Careful bool
	// Key — ключ настройки: из реестра (`store.SettingDef`), если поле собрано из
	// него, или названный строкой, тумблером или списком (`Component.key`,
	// `Select.key`), если настройки в реестре нет. По нему журнал узнаёт настройку, а
	// блок «Под рукой» — как её нарисовать (только у настроек реестра).
	// Invert — значение настройки читается наоборот тумблеру
	Key     string
	Invert  bool
	Control string // вид поля: number, toggle, text, segments, picker, timespan, checks…
	Unit    string
	// Value — как значение выглядело на момент сборки («включено», «30 дн.»);
	// у настройки реестра при показе читается заново
	Value   string
	Default *string
	// как сохранить: действие формы, её цель и имя поля
	Action, Target, Param string
	// Words — слова, которыми настройку ищут, помимо подписи: синонимы раздела,
	// заголовки раздела и формы, где она лежит
	Words string

	// Entity — сущность, на чьём экране лежит строка; Group — к какой «одинаковой
	// настройке у каждой сущности» она относится: у ста семидесяти трёх моделей
	// «Вход» — одна настройка, и поиск по слову «цена» показывает её несколько раз,
	// а не сто семьдесят три (settings_search.go). Пусто у настроек самого раздела
	Entity, Group string

	norm, normTitle, normHint string // нормализованный текст для поиска (settings_search.go)
}

// settingsIndex — собранный индекс. После сборки не меняется: читают его без
// замка, а сброс — это замена указателя.
type settingsIndex struct {
	At      time.Time
	Took    time.Duration
	Screens int
	Entries []*indexEntry
	byKey   map[string][]*indexEntry
	// byAction — записи по имени действия сохранения: журнал помнит, каким
	// действием правка сделана, и по нему находит поле, чьё имя ему неизвестно
	byAction map[string][]*indexEntry
}

// indexCache — запомненный индекс под замком. В синхронном режиме замок
// держится на время сборки: два одновременных первых поиска собрали бы индекс
// дважды. В фоновом сборка идёт без замка, а замок защищает только поля.
type indexCache struct {
	mu    sync.Mutex
	idx   *settingsIndex
	stale bool // действие человека могло сделать его неверным
	// background — сборка в фоне (рабочий сервер); building — она идёт,
	// again — за время сборки индекс снова устарел
	background, building, again bool
}

// indexBuildingID — строка «индекс собирается» на Обзоре и в поиске.
const indexBuildingID = "index-building"

// indexSettle — секунда тишины после действия: пачка действий даёт одну сборку.
const indexSettle = time.Second

// settingsIndex — индекс настроек. Синхронно: свежий или собранный заново. В
// фоне: тот, что есть (может быть nil — ещё не собран), а устаревший или
// отсутствующий заказывается на сборку.
func (s *Server) settingsIndex() *settingsIndex {
	c := &s.index
	c.mu.Lock()
	defer c.mu.Unlock()
	fresh := c.idx != nil && !c.stale && time.Since(c.idx.At) < indexTTL
	if fresh {
		return c.idx
	}
	if !c.background {
		c.idx, c.stale = s.buildSettingsIndex(), false
		return c.idx
	}
	// читатель только запускает сборку, если её нет, и не заказывает
	// повтор: иначе опрос открытого Обзора гонял пересборки по кругу без
	// единого действия человека (ревью perf-pass 4) — повтор заказывает
	// только сброс, у него есть причина
	if !c.building {
		s.scheduleIndexLocked(0)
	}
	return c.idx
}

// scheduleIndexLocked — заказать сборку в фоне (под c.mu). Идёт — ещё одна
// после неё; сборка, попавшая на новое устаревание, повторяется.
func (s *Server) scheduleIndexLocked(wait time.Duration) {
	c := &s.index
	if c.building {
		c.again = true
		return
	}
	c.building = true
	go func() {
		// паника в сборке индекса не роняет Сёму: раньше сборка шла в
		// запросе, и панику гасил net/http — падал один запрос
		defer func() {
			if r := recover(); r != nil {
				log.Printf("индекс настроек: сборка упала: %v", r)
				c.mu.Lock()
				c.building, c.again = false, false
				c.mu.Unlock()
			}
		}()
		for {
			if wait > 0 {
				time.Sleep(wait)
			}
			idx := s.buildSettingsIndex()
			c.mu.Lock()
			first := c.idx == nil
			c.idx = idx
			if first {
				// Обзор, открытый до первой сборки, сказал «индекс собирается»
				// и обещал перечитаться сам — сигнал по проводу
				s.eng.Changed("settings")
			}
			if !c.again {
				c.stale, c.building = false, false
				c.mu.Unlock()
				return
			}
			c.again, c.stale = false, true
			c.mu.Unlock()
			wait = indexSettle
		}
	}()
}

// dropSettingsIndex — индекс устарел: после действия человека он может быть
// неверным (новый сервер, переименованный ящик). В фоне — пересборка через
// секунду тишины, а до неё отвечает прежний.
func (s *Server) dropSettingsIndex() {
	c := &s.index
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stale = true
	if c.background {
		s.scheduleIndexLocked(indexSettle)
	}
}

// liveServer — сервер для работы, а не для теста: индекс в фоне, первая
// сборка — через несколько секунд после старта, когда схлынут миграции.
func liveServer(eng *engine.Engine) *Server {
	s := New(eng)
	s.index.background = true
	time.AfterFunc(5*time.Second, func() {
		s.index.mu.Lock()
		s.scheduleIndexLocked(0)
		s.index.mu.Unlock()
	})
	return s
}

// indexStaleAfter — могло ли действие поменять состав экранов настроек или
// показанное поиском (perf-pass 4.4: прежде индекс сбрасывало любое действие —
// отправка в чат, «не спам», «прочитано»). Не могли:
//   - правки одних ключей реестра (значение поиск читает заново) и служебных
//     отметок (прочтение чата, курсоры папок — `settings_log.marks`);
//   - действия без правок настроек вне экранов настроек — кроме тех немногих,
//     что заводят сущность настроек снаружи (скилл из чата, правило MCP из
//     уведомления);
//   - вход и токены: ключи, сессии, токены в индекс не входят.
func (s *Server) indexStaleAfter(req actionRequest, keys []string) bool {
	if indexKeepActions[req.Action] {
		return false
	}
	marks := s.eng.Store().SettingMarks()
	other := 0
	for _, k := range keys {
		if _, ok := store.LookupSetting(k); ok || store.IsServiceMark(k, marks) {
			continue
		}
		other++
	}
	switch {
	case other > 0:
		return true
	case len(keys) > 0:
		return false
	case strings.HasPrefix(req.Screen, "settings"):
		return true
	}
	return indexEntityActions[req.Action]
}

// indexKeepActions — действия входа и доступа: их следы (сессии, токены,
// ключи) индекс не держит.
var indexKeepActions = map[string]bool{
	"logout_session": true, "logout_all": true, "mark_own_device": true, "unmark_own_device": true,
	"add_token": true, "revoke_token": true, "rename_passkey": true, "delete_passkey": true,
	"new_phrase": true, "revoke_phrase": true,
}

// indexEntityActions — действия не с экранов настроек, которые всё же меняют
// то, что индекс показывает: скилл, принятый из чата, правило MCP «впредь» из
// уведомления, правило сервера «впредь» с карточки команды.
var indexEntityActions = map[string]bool{
	"decide_skill": true, "approve_mcp_always": true, "approve_ssh": true,
}

// indexEntity — сущность раздела: её адрес и то, как её назвать. Walk — собирать
// ли экран сущности ради её настроек (у секрета настроек нет, и тридцать экранов
// ради них не нужны).
type indexEntity struct {
	Title, Caption, Addr string
	Walk                 bool
}

// indexShared — то, что экраны одной сборки индекса читают одинаково: карточки
// моделей и роли. Собираются один раз на сборку: экран модели строил их заново
// для себя, и сто семьдесят три экрана стоили три четверти всей сборки.
type indexShared struct {
	cards map[string]engine.ModelCard
	roles []engine.Role
}

// modelCardOf — карточка модели для экрана: из общих (сборка индекса) или из
// движка.
func (s *Server) modelCardOf(doc *Document, name string) (engine.ModelCard, bool) {
	if doc.shared != nil {
		c, ok := doc.shared.cards[name]
		return c, ok
	}
	return s.eng.ModelCardOf(name)
}

// rolesOf — роли для экрана: из общих или из движка.
func (s *Server) rolesOf(doc *Document) []engine.Role {
	if doc.shared != nil {
		return doc.shared.roles
	}
	return s.eng.Roles()
}

// indexBuilder — одна сборка индекса.
type indexBuilder struct {
	s       *Server
	idx     *settingsIndex
	visited map[string]bool
	shared  *indexShared
}

// indexCtx — где стоит обход: путь, раздел, что вокруг.
type indexCtx struct {
	path    []string
	addr    string
	section string
	admin   bool
	words   string
	fine    bool
	danger  bool
	// entity — имя сущности, чей экран обходится («srv-vpn», «gpt-6-luna»); пусто у
	// настроек самого раздела
	entity string
}

// buildSettingsIndex — собрать индекс. Ничего не пишет и не ходит наружу.
func (s *Server) buildSettingsIndex() *settingsIndex {
	started := time.Now()
	shared := &indexShared{cards: map[string]engine.ModelCard{}, roles: s.eng.Roles()}
	for _, c := range s.eng.ModelCards() {
		shared.cards[c.Name] = c
	}
	b := &indexBuilder{s: s, idx: &settingsIndex{At: started,
		byKey: map[string][]*indexEntry{}, byAction: map[string][]*indexEntry{}}, visited: map[string]bool{}, shared: shared}
	for _, group := range settingsGroups {
		for _, section := range group.Sections {
			b.section(group, section)
		}
	}
	for _, e := range b.idx.Entries {
		e.normTitle, e.normHint = normText(e.Title), normText(e.Hint)
		if e.Kind == entryEntity {
			// сущность ищется по имени и по тому, что это: путь и слова раздела
			// ей не годятся — по слову «серверы» нашлись бы все серверы разом
			e.norm = normText(e.Title + " " + e.Hint)
			continue
		}
		e.norm = normText(e.Title + " " + e.Hint + " " + strings.Join(e.Path, " ") + " " + e.Words + " " + e.Key)
		if e.Key != "" {
			b.idx.byKey[e.Key] = append(b.idx.byKey[e.Key], e)
		}
		if e.Action != "" {
			b.idx.byAction[e.Action] = append(b.idx.byAction[e.Action], e)
		}
	}
	b.idx.Took = time.Since(started)
	return b.idx
}

// section — раздел целиком: его экран, внутренние вкладки, сущности.
func (b *indexBuilder) section(group settingsGroup, addr string) {
	title := sectionTitle[addr]
	base := indexCtx{path: []string{group.Name, title}, section: addr,
		admin: baseScreen(addr) == accessScreen, words: synonyms[addr]}
	b.screen(addr, base)
	for _, ent := range b.s.indexEntities(addr) {
		path := append(append([]string(nil), base.path...), ent.Title)
		b.add(&indexEntry{Kind: entryEntity, Title: ent.Title, Hint: ent.Caption, Path: base.path,
			Addr: ent.Addr, Section: addr, Admin: base.admin})
		if ent.Walk {
			ctx := base
			ctx.path = path
			ctx.entity = ent.Title
			b.screen(ent.Addr, ctx)
		}
	}
}

// screen — собрать экран по адресу и обойти его, потом его внутренние вкладки.
func (b *indexBuilder) screen(addr string, ctx indexCtx) {
	if b.visited[addr] || len(b.visited) >= indexMaxScreens {
		return
	}
	b.visited[addr] = true
	doc := b.s.buildIndexDoc(addr, b.shared)
	if doc == nil {
		return
	}
	b.idx.Screens++
	here := ctx
	here.addr = addr
	// у экрана с внутренними вкладками в путь входит вкладка, на которой стоим
	for _, tab := range doc.Subtabs {
		if tab.ID == doc.ActiveSubtab {
			here.path = append(append([]string(nil), ctx.path...), tab.Title)
		}
	}
	b.walk(doc.Components, here)
	rare := here
	rare.fine = true
	b.walk(doc.rare, rare)
	for _, tab := range doc.Subtabs {
		if tab.ID != doc.ActiveSubtab {
			b.screen(tab.ID, ctx)
		}
	}
}

// toolsFor — состояние инструментов для экрана «Инструменты». Узнать его —
// значит проверить, что есть на машине, а первое обращение к системному
// распознавателю собирает помощника компилятором; индексу настроек это не
// нужно: строки состояния настройками не являются.
func (s *Server) toolsFor(doc *Document) []engine.Tool {
	if doc.offline {
		doc.partial = true
		return nil
	}
	return s.eng.Tools()
}

// buildIndexDoc — экран для индекса: только содержимое, без оболочки (меню,
// колонки, поиска по разделам) и без всего, что ходит наружу. Адрес должен быть
// каноническим.
func (s *Server) buildIndexDoc(addr string, shared *indexShared) (doc *Document) {
	defer func() {
		// сломанный экран не должен ронять поиск по всем остальным
		if r := recover(); r != nil {
			log.Printf("индекс настроек: экран %q не собрался: %v", addr, r)
			doc = nil
		}
	}()
	doc = &Document{Components: []Component{}, Active: "settings", offline: true, shared: shared}
	if _, err := s.buildSettingsBody(doc, addr, "", "", defaultRows, false); err != nil {
		log.Printf("индекс настроек: экран %q: %v", addr, err)
		return nil
	}
	tidyRows(doc.Components)
	foldRare(doc)
	return doc
}

func (b *indexBuilder) add(e *indexEntry) { b.idx.Entries = append(b.idx.Entries, e) }

// entry — запись настройки на обходимом экране: общее для всех записей берётся
// из места обхода.
func (b *indexBuilder) entry(ctx indexCtx, e *indexEntry) {
	e.Kind = entrySetting
	e.Path, e.Addr, e.Fine = ctx.path, ctx.addr, ctx.fine
	e.Section, e.Admin, e.Words = ctx.section, ctx.admin, ctx.words
	e.Careful = e.Careful || ctx.danger
	e.Entity = ctx.entity
	if ctx.entity != "" {
		// «одинаковая настройка у каждой сущности»: тот же раздел, тот же путь без имени
		// сущности и та же подпись
		var rest []string
		for _, p := range ctx.path {
			if p != ctx.entity {
				rest = append(rest, p)
			}
		}
		e.Group = ctx.section + "|" + strings.Join(rest, "→") + "|" + e.Title
	}
	b.add(e)
}

// walk — обойти компоненты экрана и записать то, что настраивается.
func (b *indexBuilder) walk(cs []Component, ctx indexCtx) {
	for i := range cs {
		c := &cs[i]
		switch c.Type {
		case "matrix", "graph", "crumb":
			// матрицы прав и граф — не строки настройки; сущности в них названы
			// своими экранами
			continue
		case "form":
			b.form(c, ctx)
		case "row":
			b.row(c, ctx)
		case "table":
			b.table(c, ctx)
		}
		next := ctx
		if c.Title != "" && (c.Type == "section" || c.Type == "form") {
			next.words += " " + c.Title
		}
		if c.Type == "section" && isDanger(*c) {
			next.danger = true
		}
		if c.Type == "row" && len(c.Children) > 0 {
			// настройки внутри раскрытия строки («Mac» → порог, тихие часы): строка
			// входит в путь, иначе «Порог уровня» у двух каналов не различить
			if t := rowTitle(c); t != "" {
				next.path = append(append([]string(nil), ctx.path...), t)
			}
		}
		b.walk(c.Children, next)
	}
}

// rowTitle — как называется строка.
func rowTitle(c *Component) string {
	if c.Subtitle != "" {
		return c.Subtitle
	}
	return c.Title
}

// formSubmit — действие сохранения формы. У большинства форм это `Submit`; форма со
// своими кнопками («Сохранить» и «Проверить») отправки не имеет, и сохранением
// считается её единственная кнопка с `save_` или `set_`: иначе поле «Адрес SearXNG»
// не попадало бы в индекс вовсе.
func formSubmit(c *Component) *Action {
	if c.Submit != nil {
		return c.Submit
	}
	var only *Action
	for i := range c.Actions {
		if editsExisting(c.Actions[i].Action) {
			if only != nil {
				return nil
			}
			only = &c.Actions[i]
		}
	}
	return only
}

// form — поля формы, которая правит существующее. Форма, что заводит новое
// (`add_*`, `create_*`), настроек не несёт: поля у неё пустые, а смысл — «создать».
func (b *indexBuilder) form(c *Component, ctx indexCtx) {
	sub := formSubmit(c)
	if sub == nil || !editsExisting(sub.Action) {
		return
	}
	for _, f := range c.Fields {
		e := &indexEntry{Title: f.Label, Hint: clipHint(f.Hint), Anchor: hlField(c.ID, f.Name),
			Key: f.key, Control: f.Type, Unit: f.Unit, Default: f.Default,
			Action: sub.Action, Target: sub.Target, Param: f.Name}
		if f.key != "" {
			if def, ok := store.LookupSetting(f.key); ok && def.Careful {
				e.Careful = true
			}
		}
		e.Value = shownValue(f)
		// значение у поля, чьё имя говорит о секрете, не пишется вовсе: даже если
		// экран его показывает, индекс — новое место, и держать там незачем
		if f.Type == "secure" || store.NameSmellsSecret(f.Name) || store.NameSmellsSecret(f.key) {
			e.Value = ""
			e.Careful = true
		}
		b.entry(ctx, e)
	}
}

// row — настройки в строке: тумблер и списки. «Перейти» и переключатель сущности
// настройками не считаются.
func (b *indexBuilder) row(c *Component, ctx indexCtx) {
	title := rowTitle(c)
	if title == "" {
		return
	}
	if c.Toggle != "" {
		for _, a := range c.Actions {
			if a.Label != "toggle" {
				continue
			}
			value := "выключено"
			if c.Toggle == "on" {
				value = "включено"
			}
			e := &indexEntry{Title: title, Hint: clipHint(c.Caption), Anchor: hlRow(c.ID),
				Control: "row-toggle", Value: value, Action: a.Action, Target: a.Target,
				Key: c.key, Invert: c.keyInverted}
			// настройка реестра тумблером строки («Считать своей всю частную
			// сеть»: строка, а не поле, ради вопроса перед включением) —
			// умолчание и осторожность из описания, как у поля; умолчание —
			// в виде самой настройки, а не «включено»
			if def, ok := store.LookupSetting(c.key); ok {
				d := def.ShownDefault()
				e.Default, e.Careful = &d, def.Careful
			}
			b.entry(ctx, e)
		}
	}
	for _, sel := range c.Selects {
		if sel.Action.Action == "entity_goto" {
			continue
		}
		label := title
		if sel.Label != "" {
			label = title + " — " + strings.ToLower(sel.Label)
		}
		b.entry(ctx, &indexEntry{Title: label, Anchor: hlRow(c.ID), Control: "select", Value: sel.Value,
			Action: sel.Action.Action, Target: sel.Action.Target, Key: sel.key})
	}
}

// table — списки в ячейках таблицы (роли: основная и запасная модель).
func (b *indexBuilder) table(c *Component, ctx indexCtx) {
	for i := range c.Children {
		row := &c.Children[i]
		for j, cell := range row.Cells {
			if cell.Select == nil {
				continue
			}
			column := ""
			if j+1 < len(c.Columns) {
				column = strings.ToLower(c.Columns[j+1])
			}
			title := row.Subtitle
			if column != "" {
				title += " — " + column
			}
			inTable := ctx
			inTable.words += " " + c.Title
			b.entry(inTable, &indexEntry{Title: title, Hint: clipHint(row.Caption), Anchor: hlRow(row.ID),
				Control: "select", Value: cell.Select.Value,
				Action: cell.Select.Action.Action, Target: cell.Select.Action.Target, Key: cell.Select.key})
		}
		b.row(row, ctx)
	}
}

// clipHint — подсказка для индекса: первые сто знаков по рунам.
func clipHint(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}

// shownValue — как значение поля выглядит человеку: «включено», «30 дн.»,
// «02:00–06:00». Значение секрета не показывается никогда.
func shownValue(f Field) string {
	switch f.Type {
	case "secure", "code":
		return ""
	}
	return formatValue(f.Type, f.Unit, f.Value)
}

// formatValue — значение поля словами. Вид поля решает, как читать строку.
func formatValue(kind, unit, raw string) string {
	raw = strings.TrimSpace(raw)
	switch kind {
	case "toggle", "row-toggle":
		// «required» — режим проверки владельца ключа (`auth.user_verification`):
		// настройка хранит слово, а тумблер говорит «включено»
		if truthy(raw) || raw == "required" {
			return "включено"
		}
		return "выключено"
	case "timespan":
		if raw == "" {
			return "круглые сутки"
		}
		return strings.Replace(raw, "-", "–", 1)
	case "checks":
		if raw == "" {
			return "все"
		}
		return strings.ReplaceAll(raw, ",", ", ")
	}
	if raw == "" {
		return "не задано"
	}
	if r := []rune(raw); len(r) > 80 {
		raw = string(r[:80]) + "…"
	}
	if unit != "" {
		return raw + " " + unit
	}
	return raw
}

// ---- сущности ----

// indexEntities — сущности раздела, как они лежат в данных сейчас.
func (s *Server) indexEntities(section string) []indexEntity {
	st := s.eng.Store()
	var out []indexEntity
	switch section {
	case serversScreen:
		hosts, _ := st.Hosts()
		for _, h := range hosts {
			kind := "Linux"
			if h.Kind == store.HostRouterOS {
				kind = "RouterOS"
			}
			out = append(out, indexEntity{Title: h.Name, Caption: kind + " · " + hostAddr(h),
				Addr: entityAddr(serversScreen, h.Name, ""), Walk: true})
		}
	case mailScreen:
		for _, a := range s.eng.Accounts() {
			out = append(out, indexEntity{Title: a.Name, Caption: "ящик · " + a.IMAPHost,
				Addr: entityAddr(mailScreen, a.Name, ""), Walk: true})
		}
	case modelsScreen:
		seen := map[string]bool{}
		for _, c := range s.eng.ModelCards() {
			if c.Group != "" && !seen[c.Group] {
				seen[c.Group] = true
				out = append(out, indexEntity{Title: c.Group, Caption: "провайдер моделей",
					Addr: providerAddr(c.Group, ""), Walk: true})
			}
		}
		for _, c := range s.eng.ModelCards() {
			out = append(out, indexEntity{Title: c.Name, Caption: strings.TrimSpace(c.Group + " · " + c.Model),
				Addr: modelAddr(c.Name), Walk: true})
		}
	case mcpScreen:
		servers, _ := st.MCPServers()
		for _, m := range servers {
			out = append(out, indexEntity{Title: m.Name, Caption: "сервер MCP · " + m.URL,
				Addr: engine.MCPServerScreen(m.Name, ""), Walk: true})
		}
	case secretsScreen:
		secrets, _ := st.VaultSecrets()
		for _, sec := range secrets {
			out = append(out, indexEntity{Title: sec.Name, Caption: "секрет · " + sec.Kind,
				Addr: engine.SecretScreen(sec.Name, "")})
		}
	case agentsScreen:
		agents, _ := st.Agents()
		for _, a := range agents {
			out = append(out, indexEntity{Title: a.Name, Caption: "агент",
				Addr: engine.AgentScreen(a.Name, ""), Walk: true})
		}
	case storagesScreen:
		storages, _ := st.Storages()
		for _, c := range storages {
			out = append(out, indexEntity{Title: c.Name, Caption: "хранилище · " + c.Kind,
				Addr: storageAddr(c.Name), Walk: true})
		}
	case skillsScreen:
		for _, k := range s.eng.Skills() {
			out = append(out, indexEntity{Title: k.Title, Caption: k.When, Addr: engine.SkillScreen(k.Name), Walk: true})
		}
	case tgScreen:
		// первая учётка открывается самим разделом; остальным нужен свой адрес
		for i, a := range s.eng.TGAccounts() {
			if i == 0 {
				continue
			}
			out = append(out, indexEntity{Title: a.Phone, Caption: "учётка Telegram", Addr: tgBase(a), Walk: true})
		}
	}
	return out
}

// String — для сообщений об ошибках в тестах.
func (e *indexEntry) String() string {
	return fmt.Sprintf("%s → %s [%s %s]", strings.Join(e.Path, " → "), e.Title, e.Addr, e.Anchor)
}
