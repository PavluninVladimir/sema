package engine

// Модули — способ добавить Сёме способность, не пересобирая сервер. Модуль
// это манифест и код на JavaScript: он лежит в базе, исполняется в той же
// песочнице, что и скрипты, и видит ровно те примитивы ядра, которые человек
// ему разрешил. Ошибка модуля останавливает только модуль.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/llm"
	"log"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Примитивы ядра — то, что вообще можно дать модулю. Список именованный,
// потому что права выдаёт человек, и он должен читать их словами.
var primitives = map[string]string{
	"mail":     "письма и разбор почты",
	"files":    "вложения из писем",
	"http":     "запросы в сеть",
	"search":   "поиск в интернете",
	"mcp":      "серверы MCP: чужие сервисы и вики",
	"store":    "память между запусками",
	"llm":      "обращение к модели",
	"chat":     "запись в чат",
	"schedule": "запуск по расписанию",
	"ui":       "свой экран в приложении",
	"telegram": "публикация в ваши Telegram-каналы",
	"max":      "публикация в ваши каналы MAX",
	"image":    "рисование картинок через fal.ai",
}

// primitiveWhy — что именно разрешает право; человеку нужно понимать
// последствия, а не название.
var primitiveWhy = map[string]string{
	"mail":     "читать письма и менять их судьбу: переносить в спам, помечать прочитанными",
	"files":    "читать содержимое вложений, включая документы и чеки",
	"http":     "ходить в сеть: скачивать и отправлять данные наружу",
	"mcp":      "звать инструменты чужих сервисов по выданным серверам; меняющие вызовы всё равно ждут вашей кнопки",
	"search":   "спрашивать поисковую модель наружу и искать страницы через SearXNG: запрос уходит из дома в чужие поисковики, личное из него вырезается",
	"store":    "хранить своё состояние между запусками",
	"llm":      "обращаться к модели от вашего имени, расходуя лимиты и деньги",
	"chat":     "писать сообщения в ваши чаты",
	"schedule": "запускаться сам по расписанию, без вашего участия",
	"telegram": "публиковать посты от вашего имени в каналы, где вы включили «Публиковать в канал»: пост сразу видят подписчики",
	"max":      "публиковать посты от вашего имени в каналы MAX, где вы включили «Писать»: пост сразу видят подписчики",
	"image":    "рисовать картинки во внешнем сервисе fal.ai: описание уходит из дома, и каждая картинка стоит денег",
	"ui":       "показывать свой раздел в приложении",
}

// PrimitiveNames — все права, по порядку.
func PrimitiveNames() []string {
	out := make([]string, 0, len(primitives))
	for n := range primitives {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// PrimitiveWhy — что разрешает право.
func PrimitiveWhy(name string) string {
	if w, ok := primitiveWhy[name]; ok {
		return w
	}
	return ""
}

// PrimitiveTitle — как назвать право человеку.
func PrimitiveTitle(name string) string {
	if t, ok := primitives[name]; ok {
		return t
	}
	return name
}

// Module — манифест и код. Всё, кроме кода, показывается человеку до
// установки: что делает, что просит, что принесёт.
type Module struct {
	Name  string   `json:"name"`
	Title string   `json:"title"`
	About string   `json:"about"`
	Needs []string `json:"needs"` // примитивы ядра
	// PendingNeeds — примитивы, которые модуль запросил при обновлении, но
	// человек ещё не подтвердил: до подтверждения модуль работает по старым
	// правам. Иначе обновление модуля из чата тихо добавляло бы себе «http».
	PendingNeeds []string       `json:"pending_needs,omitempty"`
	Tools        []string       `json:"tools"`   // имена инструментов, которые он даёт
	Tasks        []ModuleTask   `json:"tasks"`   // задачи по расписанию
	Screens      []ModuleScreen `json:"screens"` // разделы приложения
	// Secrets — имена секретов, которые модуль просит; Granted — какие из
	// них человек выдал. Права «все секреты» нет: только поимённо.
	Secrets []string `json:"secrets,omitempty"`
	Granted []string `json:"granted,omitempty"`
	// Memory — области памяти, которые модуль просит; GrantedMemory — выданные.
	Memory        []string `json:"memory,omitempty"`
	GrantedMemory []string `json:"granted_memory,omitempty"`
	// SSH — серверы, которые модуль просит; GrantedHosts — выданные.
	SSH          []string `json:"ssh,omitempty"`
	GrantedHosts []string `json:"granted_hosts,omitempty"`
	// MCP — серверы MCP, которые модуль просит; GrantedMCP — выданные.
	MCP        []string `json:"mcp,omitempty"`
	GrantedMCP []string `json:"granted_mcp,omitempty"`
	// Skills — скиллы, чей текст модуль берёт через sema.skill(name); не
	// секрет, права не требуют, но только перечисленные.
	Skills []string `json:"skills,omitempty"`
	// Среда второго поколения: события (вид → функция), выданные события,
	// функция контекста для чата, блоки на экранах ядра, источник кода.
	On            map[string]string `json:"on_handlers,omitempty"`
	GrantedEvents []string          `json:"granted_events,omitempty"`
	Context       string            `json:"context,omitempty"`
	Blocks        map[string]string `json:"blocks,omitempty"`
	Source        string            `json:"source,omitempty"`
	// Flag — какой признак сбора у ящика реализует модуль: его задачи в
	// карточке ящика обязательны, пока галочка включена. Подпись и
	// умолчание объявляет сам модуль — ядро имён модулей не знает.
	// FlagDefault: "on" (везде включено), "off" (везде выключено),
	// "personal" (у личных ящиков да, у рабочих нет); пусто — "on".
	Flag        string `json:"flag,omitempty"`
	FlagTitle   string `json:"flag_title,omitempty"`
	FlagDefault string `json:"flag_default,omitempty"`
	Code        string `json:"code"`
	// Instructions — как модели пользоваться модулем и какие правила
	// соблюдать. Модуль может состоять из одной инструкции без кода.
	Instructions string `json:"instructions"`
	Builtin      bool   `json:"builtin,omitempty"` // встроенный: не удаляется

	Enabled bool   `json:"enabled"`
	Broken  string `json:"broken,omitempty"` // почему модуль отключён сам
	// Breaks — чем модуль уже ломался: последние поломки, самая свежая
	// первой. Одного поля `Broken` мало: человек включает модуль заново,
	// причина стирается, и через неделю та же поломка выглядит первой.
	// Модель, которая правит модуль из чата, тоже должна видеть историю.
	Breaks []ModuleBreak `json:"breaks,omitempty"`
	Added  string        `json:"added,omitempty"`
}

// ModuleTask — задача модуля: попадает в общий список задач и подчиняется
// тем же правилам, а при удалении модуля исчезает вместе с ним.
type ModuleTask struct {
	// теги нужны при записи: без них ключи уезжают с заглавной буквы, и
	// следующее чтение уже не находит их — поля молча теряются
	Title    string `json:"title"`
	Schedule string `json:"schedule"`
	Run      string `json:"run"` // что вызвать; пусто — выполнить модуль целиком
}

// UnmarshalJSON принимает задачу в любом разумном виде: модуль пишет и
// человек, и модель, и называют они поля по-разному. Пустое имя функции —
// не ошибка: тогда выполняется код модуля целиком.
func (t *ModuleTask) UnmarshalJSON(raw []byte) error {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		t.Title, t.Run = asString, asString
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	pick := pickString(m)
	t.Title = pick("title", "name", "описание")
	t.Schedule = pick("schedule", "cron", "every", "when", "расписание")
	t.Run = pick("run", "function", "fn", "call", "handler", "method")
	return nil
}

// ModuleScreen — раздел, который модуль приносит в приложение. Рисуется
// обычными компонентами, клиент не пересобирается.
type ModuleScreen struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon"`
	Build string `json:"build"` // функция, возвращающая описание экрана
}

// UnmarshalJSON — экран тоже описывают по-разному: строкой или объектом.
func (sc *ModuleScreen) UnmarshalJSON(raw []byte) error {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		sc.ID, sc.Title, sc.Build = asString, asString, asString
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	pick := pickString(m)
	sc.ID = pick("id", "name", "screen")
	sc.Title = pick("title", "name")
	sc.Icon = pick("icon")
	sc.Build = pick("build", "function", "fn", "render")
	if sc.Build == "" {
		sc.Build = sc.ID
	}
	return nil
}

const modulesKey = "modules.json"

// modulesMap читает список модулей. Ошибку разбора глотать нельзя: пустая
// карта плюс следующая запись = стёртые модули. Уже наступали на это с
// моделями — здесь такого не повторяем.
//
// Разобранный список живёт в памяти, пока не сменилась версия ключа в
// хранилище (perf-pass 2.2): modules.json — 100 КБ вместе с кодом модулей,
// и «Здоровье» разбирало его на каждую из 59 строк расхода — 2,1 с на Pi.
// Выдаётся копия: вызывающие правят модули на месте и потом сохраняют
// (или не сохраняют) — кэш их правок видеть не должен.
func (e *Engine) modulesMap() (map[string]*Module, error) {
	shared, err := e.modulesShared()
	if err != nil {
		return nil, err
	}
	return cloneModules(shared), nil
}

// modulesShared — разобранный список из кэша без копии: только для чтения.
// Словарь заменяется целиком при разборе и на месте не правится; кто
// правит модули, берёт копию через modulesMap.
func (e *Engine) modulesShared() (map[string]*Module, error) {
	ver := e.store.SettingVersion(modulesKey)
	e.cacheMu.Lock()
	if e.modCache != nil && e.modVer == ver {
		out := e.modCache
		e.cacheMu.Unlock()
		return out, nil
	}
	e.cacheMu.Unlock()
	out := map[string]*Module{}
	raw := e.store.Setting(modulesKey, "{}")
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("список модулей не разобран, ничего не меняю: %v", err)
		return nil, fmt.Errorf("список модулей повреждён: %w", err)
	}
	e.cacheMu.Lock()
	e.modVer, e.modCache = ver, out
	e.cacheMu.Unlock()
	return out, nil
}

// cloneModules — копия списка, у которой ни один срез и ни один словарь не
// общий с исходным. Новое поле-срез у Module без строки в clone роняет
// TestModuleCloneIsDeep.
func cloneModules(all map[string]*Module) map[string]*Module {
	out := make(map[string]*Module, len(all))
	for k, m := range all {
		out[k] = m.clone()
	}
	return out
}

func (m *Module) clone() *Module {
	if m == nil {
		return nil
	}
	c := *m
	c.Needs = slices.Clone(m.Needs)
	c.PendingNeeds = slices.Clone(m.PendingNeeds)
	c.Tools = slices.Clone(m.Tools)
	c.Tasks = slices.Clone(m.Tasks)
	c.Screens = slices.Clone(m.Screens)
	c.Secrets = slices.Clone(m.Secrets)
	c.Granted = slices.Clone(m.Granted)
	c.Memory = slices.Clone(m.Memory)
	c.GrantedMemory = slices.Clone(m.GrantedMemory)
	c.SSH = slices.Clone(m.SSH)
	c.GrantedHosts = slices.Clone(m.GrantedHosts)
	c.MCP = slices.Clone(m.MCP)
	c.GrantedMCP = slices.Clone(m.GrantedMCP)
	c.Skills = slices.Clone(m.Skills)
	c.On = maps.Clone(m.On)
	c.GrantedEvents = slices.Clone(m.GrantedEvents)
	c.Blocks = maps.Clone(m.Blocks)
	c.Breaks = slices.Clone(m.Breaks)
	return &c
}

// Modules — все установленные, по алфавиту.
func (e *Engine) Modules() []*Module {
	all, err := e.modulesMap()
	if err != nil {
		return nil
	}
	out := make([]*Module, 0, len(all))
	for _, m := range all {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// Module — копия одного модуля: строка «Здоровья» спрашивает название
// модуля на каждого потребителя, и копировать ради этого все модули
// незачем (perf-pass 3).
func (e *Engine) Module(name string) (*Module, bool) {
	all, err := e.modulesShared()
	if err != nil {
		return nil, false
	}
	m, ok := all[name]
	if !ok {
		return nil, false
	}
	return m.clone(), true
}

func (e *Engine) saveModules(all map[string]*Module) error {
	raw, err := json.Marshal(all)
	if err != nil {
		return err
	}
	err = e.store.SetSetting(modulesKey, string(raw))
	// запись поднимет версию и так; сброс — ради записи «того же самого»,
	// после которой версия не растёт
	e.cacheMu.Lock()
	e.modCache = nil
	e.cacheMu.Unlock()
	return err
}

// pickString достаёт строку по любому из имён, не глядя на регистр: манифест
// пишут и человек, и модель, и «Title» встречается не реже «title».
func pickString(m map[string]any) func(keys ...string) string {
	lower := make(map[string]any, len(m))
	for k, v := range m {
		lower[strings.ToLower(k)] = v
	}
	return func(keys ...string) string {
		for _, k := range keys {
			if v, ok := lower[strings.ToLower(k)].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
}

// pickStrings — список строк по любому из имён: и массивом, и одной строкой
// через запятую — модель пишет и так, и так.
func pickStrings(m map[string]any, keys ...string) []string {
	lower := make(map[string]any, len(m))
	for k, v := range m {
		lower[strings.ToLower(k)] = v
	}
	for _, k := range keys {
		switch v := lower[k].(type) {
		case []any:
			var out []string
			for _, item := range v {
				switch it := item.(type) {
				case string:
					if strings.TrimSpace(it) != "" {
						out = append(out, strings.TrimSpace(it))
					}
				case map[string]any:
					if s := fnName(it); s != "" {
						out = append(out, s)
					}
				}
			}
			return out
		case string:
			var out []string
			for _, s := range strings.Split(v, ",") {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
			return out
		case map[string]any:
			// {"gitlab-token": "зачем"} или {"areas": [...]} — берём ключи,
			// а если внутри единственный список — его
			var out []string
			for k, item := range v {
				if list, ok := item.([]any); ok && len(v) == 1 {
					return pickStrings(map[string]any{k: list}, k)
				}
				if strings.TrimSpace(k) != "" {
					out = append(out, strings.TrimSpace(k))
				}
			}
			sort.Strings(out)
			return out
		}
	}
	return nil
}

// ParseModule разбирает модуль: сначала манифест (JSON), затем код.
// Разделитель --- не обязателен — модуль пишут и человек, и модель, а
// требовать точной пунктуации значит получать отказ на ровном месте.
// fnName — имя функции из значения манифеста: строка, либо объект вида
// {"function": "context"} / {"fn": …} / {"name": …} — модели пишут по-разному.
func fnName(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		for _, k := range []string{"function", "fn", "func", "name", "call", "handler", "build"} {
			if s, ok := t[k].(string); ok && s != "" {
				return strings.TrimSpace(s)
			}
		}
		for _, x := range t {
			if s, ok := x.(string); ok && validIdent(s) {
				return s
			}
		}
	}
	return ""
}

// fenceLine — строка-ограждение (```, ```json, ```javascript) внутри блока
// модуля: модели оборачивают манифест и код в свои ограждения.
var fenceLine = regexp.MustCompile("(?m)^[ \\t]*```[A-Za-z]*[ \\t]*$\\n?")

func ParseModule(raw string) (*Module, error) {
	raw = strings.TrimSpace(fenceLine.ReplaceAllString(raw, ""))
	// манифест идёт первым: первая «{» где-то в коде — это не манифест.
	// 17 сентября 2026 модель дважды прислала модуль без манифеста, с
	// комментарием и кодом, и в ответ получила «invalid character 'r'
	// looking for beginning of object key» — скобку из кода разбирали как
	// JSON, и понять из такой ошибки, что манифест пропущен, было нельзя
	if before := strings.TrimSpace(skipLeadingComments(raw)); !strings.HasPrefix(before, "{") {
		return nil, fmt.Errorf("не нашёл манифест: блок модуля начинается с JSON-объекта {\"name\": …, \"tasks\": …}, а код идёт после него; сейчас блок начинается с «%s»", clip(firstLine(before), 60))
	}
	head, code := splitManifest(raw)
	if head == "" {
		return nil, fmt.Errorf("не нашёл манифест: модуль начинается с JSON-объекта, дальше код")
	}
	m := &Module{}
	defer func() {
		// права и включённость — решение человека, а манифест пишет модель:
		// «granted» и «enabled» в тексте модуля не значат ничего. Иначе модуль
		// из чата включал бы себя сам и брал любой секрет по имени
		m.Granted, m.GrantedMemory, m.GrantedHosts, m.GrantedMCP, m.GrantedEvents = nil, nil, nil, nil, nil
		m.Enabled, m.PendingNeeds = false, nil
	}()
	// поля, которые модель пишет как угодно (строкой, объектом, списком),
	// вынимаем из строгого разбора и разбираем сами ниже
	var loose map[string]any
	strict := head
	if json.Unmarshal([]byte(head), &loose) == nil {
		rest := map[string]any{}
		for k, v := range loose {
			switch k {
			case "on", "context", "blocks", "tools", "контекст",
				"needs", "secrets", "memory", "ssh", "mcp", "секреты", "память", "серверы", "hosts", "servers", "areas", "skills", "скиллы":
				continue
			}
			rest[k] = v
		}
		if raw, err := json.Marshal(rest); err == nil {
			strict = string(raw)
		}
	}
	if err := json.Unmarshal([]byte(strict), m); err != nil {
		return nil, fmt.Errorf("манифест не разобран: %w", err)
	}
	if loose != nil {
		m.Tools = pickStrings(loose, "tools", "инструменты")
		m.Needs = pickStrings(loose, "needs", "права")
		if m.Instructions == "" {
			m.Instructions = pickString(loose)("instructions", "instruction", "prompt", "инструкция")
		}
		if len(m.Secrets) == 0 {
			m.Secrets = pickStrings(loose, "secrets", "secret", "секреты")
		}
		if len(m.Memory) == 0 {
			m.Memory = pickStrings(loose, "memory", "areas", "память")
		}
		if len(m.MCP) == 0 {
			m.MCP = pickStrings(loose, "mcp", "mcp_servers")
		}
		if len(m.SSH) == 0 {
			m.SSH = pickStrings(loose, "ssh", "hosts", "servers", "серверы")
		}
		if len(m.Skills) == 0 {
			m.Skills = pickStrings(loose, "skills", "скиллы")
		}
		if v, ok := loose["on"]; ok {
			m.On = parseOn(v)
		}
		for _, k := range []string{"context", "контекст"} {
			if v, ok := loose[k]; ok && m.Context == "" {
				m.Context = fnName(v)
			}
		}
		if raw, ok := loose["blocks"].(map[string]any); ok {
			m.Blocks = map[string]string{}
			for k, v := range raw {
				if fn := fnName(v); fn != "" {
					m.Blocks[strings.ToLower(k)] = fn
				}
			}
		}
	}
	code = strings.TrimSpace(code)
	code = strings.TrimPrefix(code, "---")
	m.Code = strings.TrimSpace(code)
	return m, m.check()
}

// splitManifest отделяет первый JSON-объект от остального текста по балансу
// скобок: так разделитель перестаёт быть обязательным.
// skipLeadingComments — текст после комментариев в начале: модель иногда
// подписывает манифест строкой «// манифест» или блоком /* … */.
func skipLeadingComments(raw string) string {
	for {
		t := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(t, "//"):
			i := strings.Index(t, "\n")
			if i < 0 {
				return ""
			}
			raw = t[i+1:]
		case strings.HasPrefix(t, "/*"):
			i := strings.Index(t, "*/")
			if i < 0 {
				return ""
			}
			raw = t[i+2:]
		default:
			return t
		}
	}
}

// firstLine — первая строка текста.
func firstLine(s string) string {
	return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
}

func splitManifest(raw string) (head, rest string) {
	start := strings.Index(raw, "{")
	if start < 0 {
		return "", raw
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// внутри строки скобки не считаем
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return raw[start : i+1], raw[i+1:]
			}
		}
	}
	return "", raw
}

func (m *Module) check() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("у модуля должно быть имя")
	}
	if m.Title == "" {
		m.Title = m.Name
	}
	if strings.TrimSpace(m.Code) == "" && strings.TrimSpace(m.Instructions) == "" {
		return fmt.Errorf("у модуля нет ни кода, ни инструкции")
	}
	// модели пишут «ssh», «secrets», «skills» и в needs тоже — это не право
	// ядра, а указание на список; список решает, needs чистим
	var needs []string
	for _, n := range m.Needs {
		if _, ok := primitives[n]; ok {
			needs = append(needs, n)
			continue
		}
		switch n {
		case "secrets", "secret":
			if len(m.Secrets) == 0 {
				return fmt.Errorf("права «все секреты» нет: перечислите имена в поле secrets манифеста")
			}
		case "memory":
			if len(m.Memory) == 0 {
				return fmt.Errorf("права «вся память» нет: перечислите области в поле memory манифеста")
			}
		case "ssh", "servers", "hosts":
			if len(m.SSH) == 0 {
				m.SSH = []string{"*"} // все известные серверы; каждый всё равно выдаёт человек
			}
		case "skills", "skill":
			// скиллы — не право: список в манифесте достаточен
		default:
			return fmt.Errorf("неизвестное право %q", n)
		}
	}
	m.Needs = needs
	for i, n := range m.Secrets {
		m.Secrets[i] = strings.TrimSpace(n)
		if err := checkSecretName(m.Secrets[i]); err != nil {
			return fmt.Errorf("секрет %q: %w", n, err)
		}
	}
	for i, n := range m.Memory {
		m.Memory[i] = strings.ToLower(strings.TrimSpace(n))
		if m.Memory[i] == "" {
			return fmt.Errorf("пустое имя области памяти в манифесте")
		}
	}
	// движок выполняет код синхронно и не докручивает микрозадачи: всё, что
	// стоит после первого await, молча не выполнится. Лучше отказать сразу,
	// чем отдать модуль, который делает вид, что работает.
	if asyncCode.MatchString(m.Code) {
		return fmt.Errorf("уберите async и await: обращения sema.* синхронные, " +
			"а код после первого await не выполнится")
	}
	return nil
}

var asyncCode = regexp.MustCompile(`\basync\b|\bawait\b`)

// InstallModule сохраняет модуль выключенным: права человек подтверждает
// отдельным действием, включая его.
func (e *Engine) InstallModule(m *Module) error {
	if err := m.check(); err != nil {
		return err
	}
	// разбор кода до записи: поломка должна находиться здесь, а не в
	// рантайме, когда модуль выключится сам
	check, err := e.CheckModuleCode(m)
	if err != nil {
		return err
	}
	if problem := check.Problem(); problem != "" {
		return fmt.Errorf("%s", problem)
	}
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	if old, ok := all[m.Name]; ok {
		m.Enabled = old.Enabled // переустановка не меняет решения человека
		// новые примитивы не выдаются молча: модуль продолжает работать по
		// старым правам, а новые ждут человека
		if extra := extraNeeds(m.Needs, old.Needs); len(extra) > 0 {
			m.PendingNeeds = extra
			m.Needs = old.Needs
			e.NotifyModuleGrants(m, []string{"новые права: " + strings.Join(extra, ", ")})
		}
		if m.Granted == nil {
			m.Granted = old.Granted
		}
		if m.GrantedMemory == nil {
			m.GrantedMemory = old.GrantedMemory
		}
		if m.GrantedHosts == nil {
			m.GrantedHosts = old.GrantedHosts
		}
		if m.GrantedMCP == nil {
			m.GrantedMCP = old.GrantedMCP
		}
		if m.GrantedEvents == nil {
			m.GrantedEvents = old.GrantedEvents
		}
	}
	m.Added = time.Now().Format(time.RFC3339)
	all[m.Name] = m
	return e.saveModules(all)
}

// extraNeeds — права, которых у модуля раньше не было.
func extraNeeds(want, had []string) []string {
	old := map[string]bool{}
	for _, n := range had {
		old[n] = true
	}
	var out []string
	for _, n := range want {
		if !old[n] {
			out = append(out, n)
		}
	}
	return out
}

// GrantModuleNeeds — человек подтвердил новые примитивы модуля.
func (e *Engine) GrantModuleNeeds(name string, allow bool) error {
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	m, ok := all[name]
	if !ok {
		return fmt.Errorf("модуль %q не установлен", name)
	}
	if allow {
		m.Needs = append(m.Needs, m.PendingNeeds...)
	}
	m.PendingNeeds = nil
	all[name] = m
	if err := e.saveModules(all); err != nil {
		return err
	}
	e.store.CloseNotificationByKey("grants:"+name, store.NotifyDone, "человек")
	return nil
}

// ModuleTaskTimeout — предел времени задачи модуля (modules.task_timeout_s).
func (e *Engine) ModuleTaskTimeout() time.Duration {
	var v int
	if _, err := fmt.Sscanf(e.store.Setting(ModuleTaskTimeoutKey, ""), "%d", &v); err == nil && v >= 30 {
		return time.Duration(v) * time.Second
	}
	return 10 * time.Minute
}

func (e *Engine) EnableModule(name string, on bool) error {
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	m, ok := all[name]
	if !ok {
		return fmt.Errorf("модуль %q не установлен", name)
	}
	m.Enabled = on
	if on {
		m.Broken = "" // человек включил заново — даём модулю второй шанс;
		// история в Breaks остаётся: она и нужна, чтобы видеть повторы
	}
	if err := e.saveModules(all); err != nil {
		return err
	}
	if on {
		return e.syncModuleTasks(m)
	}
	return e.dropModuleTasks(name)
}

func (e *Engine) RemoveModule(name string) error {
	if m, ok := e.Module(name); ok && m.Builtin {
		return fmt.Errorf("модуль «%s» встроенный — его не удалить, только править", m.Title)
	}
	if err := e.dropModuleTasks(name); err != nil {
		return err
	}
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	delete(all, name)
	if err := e.saveModules(all); err != nil {
		return err
	}
	return e.store.DeleteModuleData(name) // данные уходят только вместе с модулем
}

// ModuleTaskSystem — как задача модуля называется в общем списке.
func ModuleTaskSystem(module, run string) string { return "module:" + module + ":" + run }

// ModuleOfTask — чей это модуль и что вызывать (пусто, если задача не от модуля).
func ModuleOfTask(system string) (module, run string) {
	if !strings.HasPrefix(system, "module:") {
		return "", ""
	}
	rest := strings.TrimPrefix(system, "module:")
	module, run, _ = strings.Cut(rest, ":")
	return module, run
}

// syncModuleTasks заводит задачи включённого модуля в общем списке.
func (e *Engine) syncModuleTasks(m *Module) error {
	existing, err := e.store.Tasks()
	if err != nil {
		return err
	}
	// задачи модуля по функции — списком, а не одной: у модуля бывает
	// несколько задач с одной функцией и разным расписанием («Утренний пост»
	// в 9:00 и «Вечерний» в 19:00 — обе зовут run). Одна задача на функцию
	// склеивала их: обе строки манифеста переписывали одну задачу в базе, и
	// 17 сентября 2026 в «Технический форум» стояли два «Утренних поста» на
	// 9:00 — модуль запускался дважды, а вечернего не было вовсе
	have := map[string][]*store.Task{}
	for _, t := range existing {
		if mod, _ := ModuleOfTask(t.System); mod == m.Name {
			have[t.System] = append(have[t.System], t)
		}
	}
	// сопоставляем по порядку: задачи заводятся в порядке манифеста, и самая
	// старая задача функции соответствует первой строке с этой функцией
	for _, list := range have {
		sort.SliceStable(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	}
	taken := map[string]int{}
	for _, mt := range m.Tasks {
		system := ModuleTaskSystem(m.Name, mt.Run)
		title := mt.Title
		if title == "" {
			title = m.Title + " · " + mt.Run
		}
		schedule := mt.Schedule
		if schedule == "" {
			schedule = "каждый час"
		}
		i := taken[system]
		taken[system]++
		if i < len(have[system]) {
			t := have[system][i]
			// модуль обновили на месте: манифест — источник правды для
			// названия, включённость и последний результат остаются. С
			// расписанием иначе: поставленное человеком сильнее манифеста
			if t.Title != title {
				if err := e.store.SetTaskTitle(t.ID, title); err != nil {
					return err
				}
			}
			// расписание из манифеста не перебивает поставленное руками:
			// человек менял час на «Задачах», а запуск сервера молча
			// возвращал прежний
			if t.Schedule != schedule {
				if err := e.store.SetTaskScheduleFromManifest(t.ID, schedule, t.AfterID); err != nil {
					return err
				}
			}
			continue
		}
		task := &store.Task{
			Title: title, System: system, Schedule: schedule, Enabled: true, Sort: 200,
			Prompt: "Задача модуля «" + m.Title + "»",
		}
		if err := e.store.CreateTask(task); err != nil {
			return err
		}
	}
	// задачи, которых в манифесте больше нет, убираем: иначе осиротевшая
	// задача запускала бы функцию по старому расписанию дальше
	for system, list := range have {
		for i := taken[system]; i < len(list); i++ {
			if err := e.store.DeleteTask(list[i].ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropModuleTasks убирает задачи модуля: выключили или удалили — их нет.
func (e *Engine) dropModuleTasks(name string) error {
	tasks, err := e.store.Tasks()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if mod, _ := ModuleOfTask(t.System); mod == name {
			if err := e.store.DeleteTask(t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ModuleScreens — разделы включённых модулей для бокового меню.
func (e *Engine) ModuleScreens() []ModuleScreen {
	var out []ModuleScreen
	// меню собирается на каждом экране: читаем общий кэш без копий, порядок
	// — как у Modules (по названию)
	all, err := e.modulesShared()
	if err != nil {
		return nil
	}
	list := make([]*Module, 0, len(all))
	for _, m := range all {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Title < list[j].Title })
	for _, m := range list {
		if !m.Enabled {
			continue
		}
		for _, sc := range m.Screens {
			sc.ID = "module:" + m.Name + ":" + sc.ID
			if sc.Title == "" {
				sc.Title = m.Title
			}
			out = append(out, sc)
		}
	}
	return out
}

// ModuleScreenDoc — описание экрана, которое вернул модуль.
func (e *Engine) ModuleScreenDoc(ctx context.Context, screenID string) (map[string]any, error) {
	rest := strings.TrimPrefix(screenID, "module:")
	name, id, _ := strings.Cut(rest, ":")
	m, ok := e.Module(name)
	if !ok || !m.Enabled {
		return nil, fmt.Errorf("модуль %q выключен или не установлен", name)
	}
	call := ""
	for _, sc := range m.Screens {
		if sc.ID == id {
			call = sc.Build
		}
	}
	if call == "" {
		call = id
	}
	res, err := e.RunModule(ctx, name, call+"()")
	if err != nil {
		return nil, err
	}
	if res.Err != nil {
		return nil, fmt.Errorf("модуль не смог собрать экран: %s", res.ErrText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Value), &doc); err != nil {
		return nil, fmt.Errorf("модуль вернул не описание экрана: %w", err)
	}
	return doc, nil
}

// markBroken выключает модуль, который сломался: остальная Сёма продолжает
// работать, а причина видна в списке модулей.
func (e *Engine) markBroken(name, why string) {
	all, err := e.modulesMap()
	if err != nil {
		return
	}
	if m, ok := all[name]; ok {
		m.Broken = why
		m.Enabled = false
		m.Breaks = append([]ModuleBreak{{When: time.Now().Format(time.RFC3339), Why: clip(why, 300)}}, m.Breaks...)
		if len(m.Breaks) > breaksKept {
			m.Breaks = m.Breaks[:breaksKept]
		}
		all[name] = m
		_ = e.saveModules(all)
		log.Printf("модуль %q отключён: %s", name, why)
	}
}

// HasGrant — выдан ли модулю секрет с таким именем.
func (m *Module) HasGrant(name string) bool {
	for _, g := range m.Granted {
		if g == name {
			return true
		}
	}
	return false
}

// RunModule выполняет код модуля с его правами и возвращает результат.
// Всё, чего модуль не просил, ему недоступно физически: инструменты просто
// не кладутся в песочницу.
func (e *Engine) RunModule(ctx context.Context, name, call string) (script.Result, error) {
	return e.RunModuleFor(ctx, name, call, "")
}

// RunModuleFor — то же, но с подписью, кто запустил: она попадает в журнал
// секретов («задача «Проверка дисков»», а не просто имя модуля).
func (e *Engine) RunModuleFor(ctx context.Context, name, call, who string) (script.Result, error) {
	m, ok := e.Module(name)
	if !ok {
		return script.Result{}, fmt.Errorf("модуль %q не установлен", name)
	}
	if !m.Enabled {
		return script.Result{}, fmt.Errorf("модуль %q выключен", name)
	}
	if who == "" {
		who = "модуль «" + m.Title + "»"
	}
	ctx = llm.WithConsumer(ctx, "module:"+m.Name) // расход модуля — на его имя
	code := m.Code
	if call != "" {
		code += "\n;return " + call + ";"
	}
	// sema.secret кладём всегда: про невыданное он отвечает отказом и пишет
	// в журнал — так видно, кто просил чужое
	tools := append(e.toolsForFill(m.Needs, m.Name, "module:"+m.Name, e.moduleSecretFiller(m, who)), e.secretTool(m, who))
	tools = append(tools, e.memoryTools(m, who)...)
	tools = append(tools, e.sshTools(m, who)...)
	tools = append(tools, e.mcpTools(m.GrantedMCP, who)...)
	tools = append(tools, e.skillTools(m)...) // скиллы — по манифесту, права не требуют
	tools = append(tools, e.notifyTools("module:"+m.Name)...)
	tools = append(tools, e.dataTools(m)...) // коллекции — свои по имени, права не требуют
	// задача по расписанию ходит по серверам и ждёт модель — ей даётся больше
	// времени, чем событию или контексту чата; предел — настройка
	timeout := 2 * time.Minute
	switch {
	case strings.HasPrefix(who, "задача"):
		timeout = e.ModuleTaskTimeout()
	case strings.HasPrefix(who, "инструмент"):
		timeout = e.ScriptTimeout() // зовут из скрипта или модуля — предел как у них
	}
	res := script.Run(ctx, code, tools, timeout)
	// выключаем модуль только за сломанный код: отказ модели или сети и
	// остановка по времени — не его вина, событие повторится само
	if res.Err != nil && !res.ToolErr && !res.Interrupted {
		why := res.ErrText
		if why == "" {
			why = res.Err.Error()
		}
		if res.ErrLine > 0 {
			why = fmt.Sprintf("строка %d: %s", res.ErrLine, why)
		}
		e.markBroken(name, why)
	}
	return res, nil
}

// SetModuleInstructions меняет инструкцию модуля: действует со следующего
// сообщения, перезапуск не нужен.
func (e *Engine) SetModuleInstructions(name, text string) error {
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	m, ok := all[name]
	if !ok {
		return fmt.Errorf("модуль %q не установлен", name)
	}
	m.Instructions = strings.TrimSpace(text)
	return e.saveModules(all)
}

// AppendModuleRule дописывает правило в инструкцию: так Сёма запоминает
// решения из разговора — видимо и с подтверждением человека.
func (e *Engine) AppendModuleRule(name, rule string) error {
	m, ok := e.Module(name)
	if !ok {
		return fmt.Errorf("модуль %q не установлен", name)
	}
	text := strings.TrimSpace(m.Instructions)
	if ruleAlreadyIn(text, rule) {
		return nil // такое правило уже записано: копий не плодим
	}
	if text != "" {
		text += "\n"
	}
	return e.SetModuleInstructions(name, text+strings.TrimSpace(rule))
}

// selfModule — встроенный модуль «Сёма»: характер и общие правила одной
// инструкцией, старше инструкций всех остальных модулей.
const selfModule = "sema"

func (e *Engine) ensureSelfModule() {
	all, err := e.modulesMap()
	if err != nil {
		return
	}
	if _, ok := all[selfModule]; ok {
		return
	}
	all[selfModule] = &Module{
		Name: selfModule, Title: "Сёма", Builtin: true, Enabled: true,
		About:        "Характер и общие правила: как отвечать, что помнить о вас, чего не делать никогда",
		Instructions: "Отвечай по делу и коротко. Всё, что меняет внешние системы, делай только после подтверждения человека.",
		Added:        time.Now().Format(time.RFC3339),
	}
	_ = e.saveModules(all)
}

// InstructionsPrompt — инструкции включённых модулей для системной части
// промпта. Старшинство названо явно: правила Сёмы, затем слова человека,
// затем модули — инструкция модуля не может отменить ограничения ядра.
func (e *Engine) InstructionsPrompt() string {
	var self string
	var mods []string
	for _, m := range e.Modules() {
		if !m.Enabled || strings.TrimSpace(m.Instructions) == "" {
			continue
		}
		if m.Name == selfModule {
			self = m.Instructions
			continue
		}
		mods = append(mods, "### Модуль «"+m.Title+"»\n"+strings.TrimSpace(m.Instructions))
	}
	var names []string
	for _, m := range e.Modules() {
		if m.Enabled && m.Name != selfModule {
			names = append(names, m.Name+" («"+m.Title+"»)")
		}
	}
	if self == "" && len(mods) == 0 && len(names) == 0 {
		return ""
	}
	var b strings.Builder
	if len(names) > 0 {
		b.WriteString("\nInstalled modules (use these names for sema-rule blocks): " + strings.Join(names, ", ") + ".\n")
	}
	b.WriteString("\nStanding instructions. Precedence: core rules above, then what the user says in this chat, " +
		"then the module instructions below. A module instruction can never override core rules or the user.\n")
	if self != "" {
		b.WriteString("### Общие правила от владельца\n" + strings.TrimSpace(self) + "\n")
	}
	for _, m := range mods {
		b.WriteString(m + "\n")
	}
	return b.String()
}

// InstructionsSize — объём инструкций: символы, примерная оценка токенов и
// доля контекста модели на роли «Чаты». Это сведение, а не предел.
func (e *Engine) InstructionsSize() (chars, tokens int, share string) {
	text := e.InstructionsPrompt()
	chars = len([]rune(text))
	tokens = chars / 4 // грубая оценка: для кириллицы ближе к 3, для кода к 4
	model := e.Role(RoleChat).Model
	for _, c := range e.userModels() {
		if c.Name == model && c.Context > 0 {
			share = fmt.Sprintf("%.1f%% контекста %s", float64(tokens)*100/float64(c.Context), model)
		}
	}
	if share == "" {
		share = "контекст модели " + model + " неизвестен"
	}
	return chars, tokens, share
}

// breaksKept — сколько поломок помним. Больше не нужно: чинят по последним,
// а список в карточке не должен превращаться в простыню.
const breaksKept = 5

// ModuleBreak — одна поломка: когда и почему модуль выключился сам.
type ModuleBreak struct {
	When string `json:"when"`
	Why  string `json:"why"`
}
