package engine

// Среда модулей второго поколения: события (письмо, файл), коллекции
// документов, вклад в контекст чата, блоки на экранах ядра, действия форм
// и встроенные модули из образа. Ядро собирает и хранит; модули понимают.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine/builtin"
	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const WorkerModuleEvents = "module_events"

// Словарь событий ядра: сущность и действие. Старые имена остаются
// синонимами — модули, написанные раньше, продолжают работать.
const (
	EventMessageAdded = "message.added"
	EventFileAdded    = "file.added"
	EventFileChanged  = "file.changed"
)

var eventHandlers = map[string]string{
	EventMessageAdded: "onMessage",
	EventFileAdded:    "onAttachment",
	EventFileChanged:  "onFileChanged",
}

var eventSynonyms = map[string]string{
	"message": EventMessageAdded, "mail": EventMessageAdded, "message.new": EventMessageAdded,
	"attachment": EventFileAdded, "file": EventFileAdded, "file.new": EventFileAdded,
	"file.updated": EventFileChanged, "attachment.changed": EventFileChanged,
}

// EventKind — каноническое имя события из любого написания.
func EventKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if canon, ok := eventSynonyms[kind]; ok {
		return canon
	}
	return kind
}

// EventKinds — виды событий по порядку, для экрана установки.
func EventKinds() []string { return []string{EventMessageAdded, EventFileAdded, EventFileChanged} }

func EventTitle(kind string) string {
	switch EventKind(kind) {
	case EventMessageAdded:
		return "новое письмо"
	case EventFileAdded:
		return "новый файл с текстом — из письма, загрузки, папки или от модуля"
	case EventFileChanged:
		return "у файла заново извлечён текст"
	}
	return kind
}

// Handler — функция модуля на событие (пусто — не подписан).
// HasHandlers — подписан ли модуль хоть на одно событие.
func (m *Module) HasHandlers() bool {
	for _, k := range EventKinds() {
		if m.Handler(k) != "" {
			return true
		}
	}
	return false
}

func (m *Module) Handler(kind string) string {
	kind = EventKind(kind)
	if fn, ok := m.On[kind]; ok && fn != "" {
		return fn
	}
	// модуль, записанный до словаря: ключи старые
	for k, fn := range m.On {
		if EventKind(k) == kind && fn != "" {
			return fn
		}
	}
	return ""
}

// canonEvents — права и подписки старого написания в каноническом виде.
func canonEvents(list []string) []string {
	var out []string
	for _, k := range list {
		c := EventKind(k)
		if !hasString(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// parseOn принимает `on` списком (["message"]) или объектом
// ({"message": "onMail"}): модель пишет и так, и так.
func parseOn(v any) map[string]string {
	out := map[string]string{}
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if kind, ok := item.(string); ok {
				kind = EventKind(kind)
				if fn, known := eventHandlers[kind]; known {
					out[kind] = fn
				}
			}
		}
	case map[string]any:
		for kind, fn := range t {
			kind = EventKind(kind)
			if _, known := eventHandlers[kind]; !known {
				continue
			}
			if name, ok := fn.(string); ok && strings.TrimSpace(name) != "" {
				out[kind] = strings.TrimSpace(name)
			} else {
				out[kind] = eventHandlers[kind]
			}
		}
	case string:
		for _, kind := range strings.Split(t, ",") {
			kind = EventKind(kind)
			if fn, known := eventHandlers[kind]; known {
				out[kind] = fn
			}
		}
	}
	return out
}

// ---- события ----

// emitModuleEvent кладёт событие каждому включённому модулю, подписанному
// на этот вид. Само событие выполняется задачей «События модулей»: ошибка
// или медленная модель не должны тормозить разбор почты.
func (e *Engine) emitModuleEvent(kind string, refID int64) {
	kind = EventKind(kind)
	if kind == EventMessageAdded {
		go e.applyMailRules(context.Background(), refID) // почтовые правила уведомлений — ядро, без очереди модулей
	}
	queued := false
	for _, m := range e.Modules() {
		if !m.Enabled || m.Handler(kind) == "" || !hasString(canonEvents(m.GrantedEvents), kind) {
			continue
		}
		if err := e.store.AddModuleEvent(m.Name, kind, refID); err != nil {
			log.Printf("событие %s для модуля %s: %v", kind, m.Name, err)
			continue
		}
		queued = true
	}
	if queued {
		e.wakeModuleEvents()
	}
}

// wakeModuleEvents разбирает очередь сразу, не дожидаясь задачи «События
// модулей»: она остаётся страховкой и местом для повторов. Один разборщик за
// раз — второй вызов лишь помечает, что после текущего прохода нужен ещё.
func (e *Engine) wakeModuleEvents() {
	e.eventsMu.Lock()
	if e.eventsRunning {
		e.eventsAgain = true
		e.eventsMu.Unlock()
		return
	}
	e.eventsRunning = true
	e.eventsMu.Unlock()
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			e.moduleEventsPass(ctx)
			cancel()
			e.eventsMu.Lock()
			if !e.eventsAgain {
				e.eventsRunning = false
				e.eventsMu.Unlock()
				return
			}
			e.eventsAgain = false
			e.eventsMu.Unlock()
		}
	}()
}

// moduleEventsPass выполняет накопившиеся события.
func (e *Engine) moduleEventsPass(ctx context.Context) string {
	// разбор идёт в одном потоке: мгновенная доставка и задача сердцебиения
	// не должны взять одно событие вдвоём — иначе модуль отработает дважды
	e.eventsPassMu.Lock()
	defer e.eventsPassMu.Unlock()
	events, err := e.store.PendingModuleEvents(50)
	if err != nil || len(events) == 0 {
		return "событий нет"
	}
	done, failed := 0, 0
	for _, ev := range events {
		select {
		case <-ctx.Done():
			return fmt.Sprintf("обработано %d, ошибок %d, прервано", done, failed)
		default:
		}
		started := time.Now()
		err := e.runModuleEvent(ctx, ev)
		note := ev.Kind
		if err != nil {
			note = ev.Kind + ": " + err.Error()
		}
		e.recordRun("module:"+ev.Module, time.Since(started).Seconds(), err == nil, note)
		if err != nil {
			failed++
			e.store.FinishModuleEvent(ev.ID, err.Error())
			continue
		}
		done++
		e.store.FinishModuleEvent(ev.ID, "")
	}
	return fmt.Sprintf("обработано %d, ошибок %d", done, failed)
}

func (e *Engine) runModuleEvent(ctx context.Context, ev *store.ModuleEvent) error {
	m, ok := e.Module(ev.Module)
	if !ok || !m.Enabled || !hasString(canonEvents(m.GrantedEvents), ev.Kind) {
		return nil // модуль ушёл или выключен — событие просто закрываем
	}
	fn := m.Handler(ev.Kind)
	if fn == "" {
		return nil
	}
	payload, err := e.eventPayload(ev.Kind, ev.RefID)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(payload)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := e.RunModuleFor(cctx, m.Name, fn+"("+string(raw)+")", "событие «"+EventTitle(ev.Kind)+"»")
	if err != nil {
		return err
	}
	if res.Err != nil {
		return fmt.Errorf("%s", res.ErrText)
	}
	return nil
}

// eventPayload — что получает обработчик: письмо с текстом или файл.
func (e *Engine) eventPayload(kind string, refID int64) (map[string]any, error) {
	switch EventKind(kind) {
	case EventMessageAdded:
		m, err := e.store.Message(refID)
		if err != nil {
			return nil, err
		}
		text := m.Snippet
		if bodies, err := e.store.Bodies([]int64{m.ID}); err == nil {
			if b, ok := bodies[m.ID]; ok && strings.TrimSpace(b.Text) != "" {
				text = b.Text
			}
		}
		account := m.Account
		if account == "" {
			account = e.conf.Mail.Name // основной ящик хранится без имени
		}
		conf := e.MailConf(account)
		payload := map[string]any{
			"id": m.ID, "from": m.FromAddr, "fromName": m.FromName, "subject": m.Subject,
			"date": m.Date.Format(time.RFC3339), "account": account, "status": m.Status,
			"text": clip(text, 6000), "local": e.mailboxClosedForCloud(account),
			"group": e.DigestGroupOf(m), // группа дайджеста: счета, документы, от людей, уведомления
			// галочки ящика: что из него собирать — модули уважают их сами;
			// состав признаков объявляют ядро и сами модули, см. MailFlags
			"collect": e.collectFlags(conf),
		}
		var files []map[string]any
		if atts, err := e.store.AttachmentsOfMessage(m.ID); err == nil {
			for _, a := range atts {
				files = append(files, map[string]any{"id": a.ID, "filename": a.Filename, "kind": a.Kind, "text": clip(a.Extracted, 3000)})
			}
		}
		payload["files"] = files
		return payload, nil
	case EventFileAdded, EventFileChanged:
		a, err := e.store.AttachmentByID(refID)
		if err != nil {
			return nil, err
		}
		sources := e.store.FileSources(a.ID)
		payload := map[string]any{
			"id": a.ID, "filename": a.Filename, "mime": a.MIME, "kind": a.Kind, "size": a.Size, "sha": a.SHA256,
			"text": clip(a.Extracted, 8000), "date": a.Date.Format(time.RFC3339),
			"location": map[string]any{"storage": a.Storage, "key": a.Key},
			"message":  nil, "local": false,
			"collect": e.collectFlags(nil),
			// коды с картинки: сумма и дата чека здесь точные, а в `text`
			// они прочитаны буквами и могут быть перепутаны
			"codes": codesForEvent(e.store.FileCodes(a.ID)),
		}
		var srcList []map[string]any
		for _, src := range sources {
			srcList = append(srcList, map[string]any{"kind": src.Kind, "ref": src.Ref, "account": src.Account, "title": SourceTitle(src)})
		}
		payload["sources"] = srcList
		if len(sources) > 0 {
			payload["source"] = srcList[0]
		} else {
			payload["source"] = map[string]any{"kind": a.Source, "ref": "", "account": "", "title": a.Source}
		}
		// файл из письма: письмо и правила его ящика
		if a.MessageID > 0 {
			account := a.Account
			if account == "" {
				account = e.conf.Mail.Name
			}
			conf := e.MailConf(account)
			payload["message"] = map[string]any{"id": a.MessageID, "from": a.FromAddr, "fromName": a.FromName, "subject": a.Subject, "date": a.Date.Format(time.RFC3339), "account": account}
			payload["local"] = e.mailboxClosedForCloud(account)
			payload["collect"] = e.collectFlags(conf)
		}
		return payload, nil
	}
	return nil, fmt.Errorf("неизвестное событие %q", kind)
}

// ---- коллекции ----

// dataTools — sema.data.* для модуля: коллекции принадлежат ему по имени.
func (e *Engine) dataTools(m *Module) []script.Tool {
	name := m.Name
	query := func(a []any, i int) store.DocQuery {
		o := script.Opts(a, i)
		q := store.DocQuery{Since: script.Str([]any{o["since"]}, 0), Until: script.Str([]any{o["until"]}, 0),
			DateField: script.Str([]any{o["dateField"]}, 0), OrderBy: script.Str([]any{o["orderBy"]}, 0)}
		if w, ok := o["where"].(map[string]any); ok {
			q.Where = w
		}
		if d, ok := o["desc"].(bool); ok {
			q.Desc = d
		}
		switch l := o["limit"].(type) {
		case int64:
			q.Limit = int(l)
		case float64:
			q.Limit = int(l)
		}
		return q
	}
	docsOut := func(list []*store.ModuleDoc) []map[string]any {
		out := make([]map[string]any, 0, len(list))
		for _, d := range list {
			doc := map[string]any{"id": d.ID, "created": d.Created, "updated": d.Updated}
			for k, v := range d.Doc {
				doc[k] = v
			}
			out = append(out, doc)
		}
		return out
	}
	return []script.Tool{
		{Name: "data.put", Need: "data", Args: "(collection, id, doc)", Desc: "сохранить документ в свою коллекцию (id пустой — новый)",
			Fn: func(a []any) (any, error) {
				id := script.Str(a, 1)
				if id == "" {
					id = fmt.Sprintf("%d", time.Now().UnixNano())
				}
				doc, _ := a[len(a)-1].(map[string]any)
				if len(a) < 3 || doc == nil {
					return nil, fmt.Errorf("data.put: нужны коллекция, id и объект")
				}
				return id, e.store.PutModuleDoc(name, script.Str(a, 0), id, doc)
			}},
		{Name: "data.get", Need: "data", Args: "(collection, id)", Desc: "документ по id или null",
			Fn: func(a []any) (any, error) {
				d, ok := e.store.GetModuleDoc(name, script.Str(a, 0), script.Str(a, 1))
				if !ok {
					return nil, nil
				}
				return docsOut([]*store.ModuleDoc{d})[0], nil
			}},
		{Name: "data.find", Need: "data", Args: "(collection, {where, since, until, dateField, orderBy, desc, limit})", Desc: "поиск по своей коллекции",
			Fn: func(a []any) (any, error) {
				list, err := e.store.FindModuleDocs(name, script.Str(a, 0), query(a, 1))
				if err != nil {
					return nil, err
				}
				return docsOut(list), nil
			}},
		{Name: "data.sum", Need: "data", Args: "(collection, field, {where, since, until})", Desc: "сумма поля по выборке, считается в базе",
			Fn: func(a []any) (any, error) {
				sum, n, err := e.store.SumModuleDocs(name, script.Str(a, 0), script.Str(a, 1), query(a, 2))
				if err != nil {
					return nil, err
				}
				return map[string]any{"sum": sum, "count": n}, nil
			}},
		{Name: "data.delete", Need: "data", Args: "(collection, id)", Desc: "удалить документ",
			Fn: func(a []any) (any, error) {
				return nil, e.store.DeleteModuleDoc(name, script.Str(a, 0), script.Str(a, 1))
			}},
	}
}

// ---- вклад в контекст чата ----

const (
	moduleContextEach  = 5000 // символов на модуль: выписка чеков за месяц длиннее заметки
	moduleContextTotal = 8000 // на все модули
)

// modulesContext зовёт функцию контекста у включённых модулей и собирает
// ответы в один блок с подписями. cloud — отвечает облачная модель:
// модулям передаётся признак, а ядро дополнительно не пропускает строки с
// закрытыми ящиками.
func (e *Engine) modulesContext(ctx context.Context, query string, cloud bool) (string, int) {
	var b strings.Builder
	total := 0
	for _, m := range e.Modules() {
		if !m.Enabled || strings.TrimSpace(m.Context) == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		arg, _ := json.Marshal(map[string]any{"query": query, "cloud": cloud})
		res, err := e.RunModuleFor(cctx, m.Name, m.Context+"("+string(arg)+")", "контекст чата")
		cancel()
		if err != nil || res.Err != nil {
			continue
		}
		text := strings.TrimSpace(res.Value)
		if text == "" {
			continue
		}
		text = clip(text, moduleContextEach)
		if cloud {
			text = e.dropClosedMailboxLines(text)
		}
		if total+len([]rune(text)) > moduleContextTotal {
			break
		}
		fmt.Fprintf(&b, "\n### Модуль «%s» знает:\n%s\n", m.Title, text)
		total += len([]rune(text))
	}
	if b.Len() == 0 {
		return "", 0
	}
	return "\nWhat installed modules know (structured data they keep; trust it over similarity search):\n" + b.String(), tokensOf(b.String())
}

// dropClosedMailboxLines убирает строки, где назван ящик, закрытый для облака.
func (e *Engine) dropClosedMailboxLines(text string) string {
	var closed []string
	for _, a := range e.Accounts() {
		if e.mailboxClosedForCloud(a.Name) {
			closed = append(closed, a.Name)
		}
	}
	if len(closed) == 0 {
		return text
	}
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		skip := false
		for _, c := range closed {
			if strings.Contains(line, c) {
				skip = true
			}
		}
		if !skip {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// ---- блоки на экранах ядра ----

// ModuleBlocks — компоненты, которые модули вставляют в экран ядра.
// Возвращает пары (модуль, компоненты); ошибка модуля блок опускает.
type ModuleBlock struct {
	Module     *Module
	Components []map[string]any
}

// account — какой ящик открыт («» — все): модуль показывает только своё.
func (e *Engine) ModuleBlocks(ctx context.Context, screen, account string) []ModuleBlock {
	arg, _ := json.Marshal(map[string]string{"screen": screen, "account": account})
	var out []ModuleBlock
	for _, m := range e.Modules() {
		fn := ""
		if m.Blocks != nil {
			fn = m.Blocks[screen]
		}
		if !m.Enabled || fn == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		res, err := e.RunModuleFor(cctx, m.Name, fn+"("+string(arg)+")", "блок на экране «"+screen+"»")
		cancel()
		if err != nil || res.Err != nil || strings.TrimSpace(res.Value) == "" {
			continue
		}
		var comps []map[string]any
		if err := json.Unmarshal([]byte(res.Value), &comps); err != nil {
			var one map[string]any
			if json.Unmarshal([]byte(res.Value), &one) != nil {
				continue
			}
			comps = []map[string]any{one}
		}
		out = append(out, ModuleBlock{Module: m, Components: comps})
	}
	return out
}

// ---- действия форм ----

// ModuleAction зовёт функцию модуля с параметрами формы или кнопки.
func (e *Engine) ModuleAction(ctx context.Context, name, fn string, params map[string]string) error {
	m, ok := e.Module(name)
	if !ok || !m.Enabled {
		return fmt.Errorf("модуль %q выключен или не установлен", name)
	}
	if !validIdent(fn) {
		return fmt.Errorf("недопустимое имя функции %q", fn)
	}
	arg, _ := json.Marshal(params)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := e.RunModuleFor(cctx, name, fn+"("+string(arg)+")", "действие «"+fn+"»")
	if err != nil {
		return err
	}
	if res.Err != nil {
		return fmt.Errorf("%s", res.ErrText)
	}
	return nil
}

func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// ---- встроенные модули ----

// loadBuiltinModules читает модули из образа: новый — ставится выключенным
// с признаком «встроенный»; изменившийся — обновляется без потери прав,
// инструкции и данных.
func (e *Engine) loadBuiltinModules() {
	entries, err := fs.ReadDir(builtin.FS, ".")
	if err != nil {
		return
	}
	all, err := e.modulesMap()
	if err != nil {
		return
	}
	changed := false
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".js") {
			continue
		}
		raw, err := fs.ReadFile(builtin.FS, ent.Name())
		if err != nil {
			continue
		}
		m, err := ParseModule(string(raw))
		if err != nil {
			log.Printf("встроенный модуль %s не разобран: %v", ent.Name(), err)
			continue
		}
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:8])
		m.Builtin, m.Source = true, "builtin:"+ent.Name()+"@"+hash
		old, exists := all[m.Name]
		if exists && old.Source == m.Source {
			continue
		}
		if exists {
			// обновление кода: решения человека и данные остаются
			m.Enabled, m.Granted, m.GrantedMemory, m.GrantedHosts = old.Enabled, old.Granted, old.GrantedMemory, old.GrantedHosts
			m.GrantedMCP = old.GrantedMCP
			m.GrantedEvents = old.GrantedEvents // иначе после каждой пересборки модуль глохнет к письмам
			if strings.TrimSpace(old.Instructions) != "" {
				m.Instructions = old.Instructions
			}
			m.Added = old.Added
			log.Printf("встроенный модуль «%s» обновлён", m.Title)
		} else {
			m.Added = time.Now().Format(time.RFC3339)
			log.Printf("встроенный модуль «%s» установлен выключенным", m.Title)
		}
		all[m.Name] = m
		changed = true
	}
	if changed {
		_ = e.saveModules(all)
		for _, m := range all {
			if m.Enabled {
				_ = e.syncModuleTasks(m)
			}
		}
	}
}

// CopyModule делает из встроенного обычный модуль-копию под своим именем:
// его можно править как угодно.
func (e *Engine) CopyModule(name, newName string) error {
	src, ok := e.Module(name)
	if !ok {
		return fmt.Errorf("модуль %q не установлен", name)
	}
	newName = strings.TrimSpace(newName)
	if newName == "" || newName == name {
		return fmt.Errorf("нужно другое имя")
	}
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	if _, taken := all[newName]; taken {
		return fmt.Errorf("модуль %q уже есть", newName)
	}
	cp := *src
	cp.Name, cp.Title = newName, src.Title+" (копия)"
	cp.Builtin, cp.Source, cp.Enabled, cp.Broken = false, "", false, ""
	cp.Added = time.Now().Format(time.RFC3339)
	all[newName] = &cp
	return e.saveModules(all)
}

// ModuleEventSummary — строка о событиях модуля для списка.
func (e *Engine) ModuleEventSummary(m *Module) string {
	if len(m.On) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(m.On))
	for k := range m.On {
		kinds = append(kinds, EventTitle(k))
	}
	sort.Strings(kinds)
	done, failed, pending := e.store.ModuleEventStats(m.Name)
	s := "события: " + strings.Join(kinds, ", ")
	if done+failed+pending > 0 {
		s += fmt.Sprintf(" · обработано %d, ошибок %d, в очереди %d", done, failed, pending)
	}
	return s
}
