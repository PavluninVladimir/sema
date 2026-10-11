package engine

// Сёма разбирает свой код. Модули живут в базе, а не файлами, и правит их
// модель по просьбе из чата — значит проверять их надо до установки, а не
// узнавать о поломке из рантайма.
//
// Разбор делает парсер goja: тот же, что потом выполняет код. Значит
// проверка честна ровно настолько, насколько честен рантайм — что не
// разобралось, то и не запустится.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"

	"github.com/PavluninVladimir/sema/server/internal/script"
)

// ModuleFunc — функция модуля: имя, границы, размер.
type ModuleFunc struct {
	Name  string
	Line  int // строка объявления, считая с единицы
	Lines int // сколько строк занимает
	Body  string
}

// ModuleCode — что Сёма понял про код модуля.
type ModuleCode struct {
	Funcs []ModuleFunc
	// Uses — что код зовёт у sema, без приставки: «data.put», «ssh.run».
	// Именно в таком виде имена сверяются с реестром инструментов в
	// `Needs`, поэтому приставку сюда добавлять нельзя — право перестанет
	// находиться.
	Uses []string
	Err  string // синтаксис не разобрался
}

// Func — функция по имени.
func (c ModuleCode) Func(name string) (ModuleFunc, bool) {
	for _, f := range c.Funcs {
		if f.Name == name {
			return f, true
		}
	}
	return ModuleFunc{}, false
}

// ParseModuleCode разбирает код модуля. Ошибка синтаксиса возвращается, а
// не глотается: именно ради неё всё и затевалось.
func ParseModuleCode(code string) (ModuleCode, error) {
	out := ModuleCode{}
	prog, err := parser.ParseFile(nil, "module.js", code, 0)
	if err != nil {
		out.Err = clip(err.Error(), 300)
		return out, fmt.Errorf("код не разобран: %s", out.Err)
	}
	for _, st := range prog.Body {
		switch v := st.(type) {
		case *ast.FunctionDeclaration:
			if v.Function == nil || v.Function.Name == nil {
				continue
			}
			out.Funcs = append(out.Funcs, funcOf(code, v.Function.Name.Name.String(),
				int(v.Function.Function), v.Function.Source))
		case *ast.VariableStatement:
			out.Funcs = append(out.Funcs, bindings(code, v.List)...)
		case *ast.LexicalDeclaration:
			// const refresh = () => {…} — так пишут модели, и это тоже функция
			out.Funcs = append(out.Funcs, bindings(code, v.List)...)
		}
	}
	out.Uses = usedPrimitives(code)
	return out, nil
}

// bindings — функции, объявленные переменной: `const run = () => {}` и
// `var run = function () {}`.
func bindings(code string, list []*ast.Binding) []ModuleFunc {
	var out []ModuleFunc
	for _, b := range list {
		id, ok := b.Target.(*ast.Identifier)
		if !ok || b.Initializer == nil {
			continue
		}
		switch fn := b.Initializer.(type) {
		case *ast.FunctionLiteral:
			out = append(out, funcOf(code, id.Name.String(), int(fn.Function), fn.Source))
		case *ast.ArrowFunctionLiteral:
			out = append(out, funcOf(code, id.Name.String(), int(fn.Start), fn.Source))
		}
	}
	return out
}

func funcOf(code, name string, idx int, body string) ModuleFunc {
	return ModuleFunc{Name: name, Line: lineOf(code, idx), Lines: strings.Count(body, "\n") + 1, Body: body}
}

// lineOf — номер строки по смещению в исходнике; смещения goja с единицы.
func lineOf(code string, idx int) int {
	if idx <= 0 || idx > len(code) {
		return 1
	}
	return strings.Count(code[:idx-1], "\n") + 1
}

var (
	// sema.llm(…), sema.files.add(…), sema.чеки.list(…)
	reSemaCall = regexp.MustCompile(`sema\s*\.\s*([\p{L}_][\p{L}\d_]*)(?:\s*\.\s*([\p{L}_][\p{L}\d_]*))?`)
	// обращение через строку: sema["ssh"] — парсером не связывается с правом
	reSemaIndex = regexp.MustCompile(`sema\s*\[`)
)

// toolNeeds — какое право стоит за каким инструментом песочницы. Берётся
// из реестра инструментов, а не из списка в коде: реестр — единственная
// правда, и он же строит подсказку модели.
func (e *Engine) toolNeeds() map[string]string {
	out := map[string]string{}
	for _, t := range e.builtinTools() {
		out[t.Name] = t.Need
	}
	for _, t := range e.moduleTools("") {
		out[t.Name] = t.Need
	}
	// наборы, которые кладутся в песочницу отдельно (см. сборку модуля)
	empty := &Module{}
	for _, set := range [][]script.Tool{e.memoryTools(empty, ""), e.sshTools(empty, ""), e.mcpTools(nil, "")} {
		for _, t := range set {
			out[t.Name] = t.Need
		}
	}
	// эти кладутся в песочницу всегда и права не требуют: коллекции свои
	// по имени, скиллы по манифесту, уведомления и секрет — со своей
	// проверкой внутри. Их Need в реестре декоративный.
	for _, set := range [][]script.Tool{e.skillTools(empty), e.notifyTools(""), e.dataTools(empty),
		{e.secretTool(empty, "")}} {
		for _, t := range set {
			out[t.Name] = ""
		}
	}
	// мелочи песочницы без права: разбор дат и периодов по-русски
	out["date"], out["period"] = "", ""
	return out
}

// usedPrimitives — что код зовёт: имена обращений к sema.* и чужие
// инструменты. Сверка по именам: обращение через переменную или строку
// сюда не попадёт, поэтому итог — подсказка, а не приговор.
func usedPrimitives(code string) []string {
	var calls []string
	seen := map[string]bool{}
	for _, m := range reSemaCall.FindAllStringSubmatch(code, -1) {
		name := m[1]
		if m[2] != "" {
			name = m[1] + "." + m[2]
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		calls = append(calls, name)
	}
	sort.Strings(calls)
	return calls
}

// Needs — какие права нужны коду по его обращениям. Второе значение —
// имена, которые не удалось связать ни с одним инструментом.
func (e *Engine) Needs(c ModuleCode) (needs []string, unknown []string) {
	reg := e.toolNeeds()
	set := map[string]bool{}
	for _, call := range c.Uses {
		if need, ok := reg[call]; ok {
			if need != "" {
				set[need] = true
			}
			continue
		}
		// sema.<модуль>.<функция> — инструмент другого модуля: право
		// «modules». Но только если первая часть не известный набор
		// (files.add, data.put и прочие уже разобраны выше).
		if base, _, ok := strings.Cut(call, "."); ok {
			if _, known := reg[base]; !known && !knownNamespace(base) {
				set["modules"] = true
				continue
			}
		}
		unknown = append(unknown, call)
	}
	for n := range set {
		needs = append(needs, n)
	}
	sort.Strings(needs)
	sort.Strings(unknown)
	return needs, unknown
}

// CheckModuleCode — проверка перед установкой. Возвращает беды, которые
// надо вернуть модели вторым ходом, и подсказки человеку.
type CodeCheck struct {
	Code     ModuleCode
	Missing  []string // код зовёт, а права не просил — это отказ
	Extra    []string // право просил, а в коде не зовёт — подсказка
	Unknown  []string // обращения, которые не связались с инструментом
	ByString bool     // есть обращение вида sema["…"]: сверка неполна
	Broken   []Bound  // манифест зовёт функцию, которой нет на верхнем уровне — это отказ
	// код зовёт инструмент, который ещё не настроен (`sema.web.find` без
	// адреса SearXNG) — это отказ до установки: на запуске такой вызов
	// отвечал бы ошибкой «не настроен», и новому модулю с этого нечего
	// начинать. Уже стоящий модуль этим не выключается: для него вызов —
	// ошибка инструмента, а не сломанный код
	Unavailable []string
}

func (e *Engine) CheckModuleCode(m *Module) (CodeCheck, error) {
	out := CodeCheck{}
	code, err := ParseModuleCode(m.Code)
	out.Code = code
	if err != nil {
		return out, err
	}
	needs, unknown := e.Needs(code)
	reg := e.toolNeeds()
	out.Unknown = unknown
	out.ByString = reSemaIndex.MatchString(m.Code)

	asked := map[string]bool{}
	for _, n := range m.Needs {
		asked[n] = true
	}
	// «ssh» из needs вычищается разбором манифеста: это не примитив, а
	// указание на список серверов, и оно переезжает в m.SSH. Без этой
	// строки проверка требовала дописать то, что сама же и убирала, и
	// любой модуль с sema.ssh.run нельзя было ни поставить, ни обновить
	// из чата — «Серверы под присмотром» замёрз в том виде, в каком его
	// установили 3 сентября
	if len(m.SSH) > 0 {
		asked["ssh"] = true
	}
	used := map[string]bool{}
	for _, n := range needs {
		used[n] = true
		if !asked[n] {
			out.Missing = append(out.Missing, n)
		}
	}
	// право без инструментов в песочнице в коде и не может встретиться:
	// оно про манифест, а не про вызовы. Такие же и права, которые
	// манифест берёт на себя: задачи по расписанию и свой экран.
	callable := map[string]bool{}
	for _, need := range reg {
		if need != "" {
			callable[need] = true
		}
	}
	// «ssh» из needs вычищается разбором манифеста: это не примитив, а
	// указание на список серверов, и оно переезжает в m.SSH. Проверка
	// после этого не находила его в needs и требовала дописать —
	// а дописанное снова вычищалось. Любой модуль с sema.ssh.run стало
	// нельзя ни поставить, ни обновить из чата: «Серверы под присмотром»
	// так и замёрз в том виде, в каком его установили 3 сентября
	byManifest := map[string]bool{
		"schedule": len(m.Tasks) > 0,
		"ui":       len(m.Screens) > 0 || len(m.Blocks) > 0,
		"ssh":      len(m.SSH) > 0,
	}

	for _, n := range m.Needs {
		if !used[n] && callable[n] && !byManifest[n] {
			out.Extra = append(out.Extra, n)
		}
	}
	sort.Strings(out.Missing)
	sort.Strings(out.Extra)
	out.Broken = Broken(m, code)
	if !e.WebFindReady() {
		for _, call := range code.Uses {
			if call == "web.find" {
				out.Unavailable = append(out.Unavailable, call)
			}
		}
	}
	return out, nil
}

// Problem — беда, которую надо вернуть модели. Пусто — можно ставить.
func (c CodeCheck) Problem() string {
	if c.Code.Err != "" {
		return "код не разобрался: " + c.Code.Err
	}
	if len(c.Missing) > 0 {
		var titles []string
		for _, n := range c.Missing {
			titles = append(titles, "«"+PrimitiveTitle(n)+"» ("+n+")")
		}
		return "код обращается к тому, чего не просил в манифесте: " + strings.Join(titles, ", ") +
			". Допиши права в манифест или убери вызовы."
	}
	if len(c.Unavailable) > 0 {
		return "код зовёт sema.web.find, а поиск страниц не настроен: адрес SearXNG не задан в «Настройки → Поиск», " +
			"и каждый вызов отвечал бы ошибкой. Настрой адрес или обойдись без этого вызова (sema.search — поиск моделью, если ей назначена роль «Поиск»)."
	}
	// манифест зовёт функцию, а её нет на верхнем уровне кода. Ядро зовёт
	// задачу, инструмент и контекст по имени, и функция внутри обёртки
	// (function () { … })() снаружи не видна: 17 сентября 2026 модуль
	// канала встал с «обновлён и работает», а preview и run падали
	// «is not defined»
	if len(c.Broken) > 0 {
		var what []string
		for _, b := range c.Broken {
			what = append(what, b.Fn+" ("+b.What+")")
		}
		msg := "манифест зовёт функции, которых нет на верхнем уровне кода: " + strings.Join(what, ", ") +
			". Объяви их на верхнем уровне — function run() { … }"
		if len(c.Code.Funcs) == 0 {
			msg += "; код целиком обёрнут в (function () { … })() — ядро такие функции не видит, обёртку убери"
		}
		return msg + "."
	}
	return ""
}

// Outline — оглавление модуля для промпта: функции с размером вместо
// девятисот строк кода.
func (c ModuleCode) Outline() string {
	if len(c.Funcs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range c.Funcs {
		fmt.Fprintf(&b, "  %s — строка %d, %d строк\n", f.Name, f.Line, f.Lines)
	}
	return b.String()
}

// knownNamespace — наборы инструментов ядра: у них имя вида «files.add»,
// и первая часть сама по себе инструментом не является.
func knownNamespace(base string) bool {
	switch base {
	case "files", "data", "http", "memory", "ssh", "mcp", "store", "notify", "web", "crypto":
		return true
	}
	return false
}

// ---- Радиус поражения внутри Сёмы ----

// Bound — что завязано на функцию модуля.
type Bound struct {
	Fn   string // имя функции
	What string // задача, экран, событие, инструмент, контекст, блок
	Note string // как это называется человеку
}

// ModuleBounds — на какие функции модуля что завязано. Берётся из
// манифеста: он надёжнее вывода из кода.
func ModuleBounds(m *Module) []Bound {
	var out []Bound
	for _, t := range m.Tasks {
		if t.Run == "" {
			continue // задача выполняет модуль целиком, отдельной функции нет
		}
		out = append(out, Bound{Fn: fnOf(t.Run), What: "задача", Note: t.Title + " · " + t.Schedule})
	}
	for kind, fn := range m.On {
		out = append(out, Bound{Fn: fnOf(fn), What: "событие", Note: kind})
	}
	for _, t := range m.Tools {
		name, _, _ := parseToolSpec(t)
		out = append(out, Bound{Fn: fnOf(name), What: "инструмент",
			Note: "sema." + m.Name + "." + fnOf(name)})
	}
	for _, sc := range m.Screens {
		if sc.Build == "" {
			continue
		}
		out = append(out, Bound{Fn: fnOf(sc.Build), What: "экран", Note: sc.Title})
	}
	for place, fn := range m.Blocks {
		out = append(out, Bound{Fn: fnOf(fn), What: "блок", Note: "на экране «" + place + "»"})
	}
	if m.Context != "" {
		out = append(out, Bound{Fn: fnOf(m.Context), What: "контекст", Note: "идёт в промпт чата"})
	}
	return out
}

// Broken — что отвалится, если поставить этот код: связи манифеста,
// которым больше нет функции.
func Broken(m *Module, code ModuleCode) []Bound {
	var out []Bound
	for _, b := range ModuleBounds(m) {
		if b.Fn == "" {
			continue
		}
		if _, ok := code.Func(b.Fn); !ok {
			out = append(out, b)
		}
	}
	return out
}

// DeadFuncs — функции, которые не зовёт ни манифест, ни другая функция.
// Показываются предложением: код мог просто ещё не дописать эту ветку.
func DeadFuncs(m *Module, code ModuleCode) []string {
	used := map[string]bool{}
	for _, b := range ModuleBounds(m) {
		used[b.Fn] = true
	}
	// вызовы внутри самого модуля: имя функции со скобкой в чужом теле,
	// либо её имя строкой — так модуль называет действия своих форм
	// (`module_action` зовёт функцию по имени из описания экрана)
	for _, f := range code.Funcs {
		for _, other := range code.Funcs {
			if other.Name == f.Name {
				continue
			}
			if strings.Contains(other.Body, f.Name+"(") ||
				strings.Contains(other.Body, `"`+f.Name+`"`) ||
				strings.Contains(other.Body, "'"+f.Name+"'") {
				used[f.Name] = true
				break
			}
		}
	}
	var out []string
	for _, f := range code.Funcs {
		if !used[f.Name] {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// fnOf — имя функции из записи манифеста: там пишут и «run», и «run()»,
// и «имя(аргумент)».
func fnOf(spec string) string {
	spec = strings.TrimSpace(spec)
	if i := strings.IndexByte(spec, '('); i >= 0 {
		spec = spec[:i]
	}
	spec = strings.TrimSpace(spec)
	// инструмент объявляют и как «list», и как «receipts.list»: имя
	// функции — последняя часть
	if i := strings.LastIndexByte(spec, '.'); i >= 0 {
		spec = spec[i+1:]
	}
	return spec
}

// ---- Диагностика через Сёма-агента ----

// moduleURI — условный адрес модуля: файла на диске у него нет, но
// языковой сервер умеет работать с текстом, присланным в открытии.
func moduleURI(name string) string { return "sema://module/" + name + ".js" }

// Diagnose спрашивает у агента машины, что не так с кодом модуля. Агента
// нет или он не умеет этот язык — шаг пропускается: проверка синтаксиса и
// прав к тому моменту уже прошла.
func (e *Engine) Diagnose(ctx context.Context, m *Module) (string, bool) {
	server := e.serverWithTool("diagnostics")
	if server == "" {
		return "", false // некому спросить: агент машины не подключён
	}
	res := e.RequestMCP(ctx, "ядро · проверка модуля", server, "diagnostics",
		map[string]any{"path": moduleURI(m.Name), "text": m.Code})
	if res.Err != "" {
		log.Printf("проверка модуля «%s»: агент не ответил: %s", m.Name, clip(res.Err, 120))
		return "", false
	}
	text := strings.TrimSpace(res.Text)
	if text == "" || strings.Contains(text, "Замечаний нет") {
		return "", true
	}
	return text, true
}

// serverWithTool — первый подключённый сервер MCP, умеющий этот
// инструмент. Так ядро спрашивает способность, а не помнит имя сервера:
// агент машины может называться как угодно.
func (e *Engine) serverWithTool(tool string) string {
	list, err := e.store.MCPServers()
	if err != nil {
		return ""
	}
	for _, s := range list {
		if !s.Enabled || s.State != "ok" {
			continue
		}
		for _, t := range s.Tools {
			if t == tool {
				return s.Name
			}
		}
	}
	return ""
}
