package store

// Каждое место, где настройка пишется с ключом «приставка + идентификатор»
// (`chat.read.<чат>`, `vision.decision.<файл>`, `tg.chat.<учётка>.<чат>`),
// обязано быть названо: либо приставка входит в первичное заполнение служебных
// отметок журнала (`settingsLogMarksSeed`) — это Сёма пишет фоном и по десять раз в
// минуту, и человеческой правкой оно не считается, — либо она стоит в явном
// списке `humanPrefixes` «это правка человека» с причиной.
//
// 2 октября 2026 живой журнал показал, чем кончается «по умолчанию»: под «Ваши
// правки» оказались 51 решение по картинкам (`vision.decision.*`), счётчики
// чатов MAX и Telegram, привязки чатов — всё это машина пишет в долю секунды
// действия человека, и подпись `SignChanges` приписывает ей его руку. Тест
// разбирает исходники (go/ast), а не перечисляет имена по памяти: новая запись с
// ключом по образцу «приставка + номер» без названного хозяина роняет сборку.

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// humanPrefixes — приставки ключей, которые пишет **человек**, а не Сёма: правка
// настоящая, и в «Ваших правках» ей место. Причина обязательна: список — не
// способ заткнуть тест.
var humanPrefixes = map[string]string{
	"mcp.groups.":      "какие группы инструментов сервера MCP показывать — выбор человека на «Интеграциях»",
	"tg.wait_off.":     "«не напоминать про этот чат» — решение человека, одно на чат",
	"balance.base.":    "остаток на счёте провайдера «на дату» вписывает человек: от него считается оценка",
	"mail.folders_ok.": "«эти папки проверены» — человек подтвердил выбор папок ящика",
	"queue.pause_ms.":  "свой предел скорости на вид работы: кнопка «Предел скорости» в строке вида",
	"worker.":          "тумблеры и расписание воркеров, правятся на «Задачах»",
}

// unresolvedWrites — места записи, где ключ сборкой не вычисляется: берётся из
// кнопки экрана, из цикла по таблице или из параметра функции-обёртки. Каждое
// названо с причиной; запись, которой тут нет, а ключ у неё не вычислился, роняет
// тест: пусть тот, кто завёл новый путь записи, скажет, чей это ключ.
var unresolvedWrites = map[string]string{
	"healthActions":       "«health.*» — пороги Здоровья из формы, ключи проверены приставкой и реестром",
	"memoryActions":       "ключ — цель кнопки экрана «Память», через SetMemorySetting; правка человека",
	"pushActions":         "ключи из таблицы {поле формы → ключ} в соседних строках, все push.*",
	"saveSettings":        "ключ из описания SettingDef поля формы: единый путь правок человека",
	"serversActions":      "ключ — цель кнопки экрана «Серверы», через SetSSHSetting; правка человека",
	"telegramActions":     "ключ — цель кнопки «Telegram» из закрытого списка tgFlagDefaults; правка человека",
	"purgeExpiredChats":   "уборка отметки chat.expires.*, которая и так служебная; удаление — не правка",
	"tgChannelFeed":       "разовая сборка ленты канала: снимает отметки истории tg.history.*",
	"migrateFiles":        "миграция store: переносит и снимает собственные отметки",
	"migrateNewsGone":     "миграция store: сносит ключи снятой возможности",
	"migrateTGKeys":       "миграция store: переименовывает tg.*",
	"migrateTrackersGone": "миграция store: сносит ключи снятой возможности",
	"encryptScriptMemory": "шифрование памяти скриптов: переписывает script.* как есть",
	"vaultReseal":         "смена ключа: перешифровывает значения как есть",
	"RotateKey":           "смена ключа: перешифровывает значения как есть",
	"SaveSetting":         "единая запись по описанию SettingDef: ключ — из описания",
	"seedLogMarks":        "засев списка служебных отметок самим журналом",
	"settleAuthDefaults":  "миграция store: умолчания входа по описаниям SettingDef (AuthLocalOpen, AuthLocalScope) — ключ из описания",
}

// keyShape — что известно о ключе в месте записи.
type keyShape struct {
	Kind  string // static | prefix | unknown | param
	Value string
	Where string // файл:строка
	Func  string
	Src   string // как ключ записан в коде
}

// doorWriters — функции, которые пишут настройку, и номер параметра с ключом.
// Дверь (`SetSetting`, `setSetting`, `SetSecretSetting`, `DeleteSettingKey`) — всегда;
// обёртки над ней находятся обходом.
var doorWriters = map[string]int{"SetSetting": 0, "setSetting": 0, "SetSecretSetting": 0, "DeleteSettingKey": 0}

// sourceScan — разобранные исходники дерева.
type sourceScan struct {
	fset   *token.FileSet
	files  map[string]*ast.File
	consts map[string]string
	funcs  map[string]*ast.FuncDecl
}

func scanSources(t *testing.T) *sourceScan {
	t.Helper()
	sc := &sourceScan{fset: token.NewFileSet(), files: map[string]*ast.File{}, consts: map[string]string{}, funcs: map[string]*ast.FuncDecl{}}
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, perr := parser.ParseFile(sc.fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			sc.files[path] = f
			return nil
		})
		if err != nil {
			t.Fatalf("обход исходников: %v", err)
		}
	}
	for _, f := range sc.files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				sc.funcs[d.Name.Name] = d
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					sc.addConsts(spec)
				}
			}
		}
	}
	return sc
}

func (sc *sourceScan) addConsts(spec ast.Spec) {
	vs, ok := spec.(*ast.ValueSpec)
	if !ok {
		return
	}
	for i, name := range vs.Names {
		if i >= len(vs.Values) {
			continue
		}
		if v, ok := sc.constString(vs.Values[i]); ok {
			sc.consts[name.Name] = v
		}
	}
}

// constString — значение строковой константы: литерал или сумма литералов и
// ранее найденных констант.
func (sc *sourceScan) constString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			v, err := strconv.Unquote(x.Value)
			return v, err == nil
		}
	case *ast.Ident:
		v, ok := sc.consts[x.Name]
		return v, ok
	case *ast.ParenExpr:
		return sc.constString(x.X)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			l, lok := sc.constString(x.X)
			r, rok := sc.constString(x.Y)
			return l + r, lok && rok
		}
	}
	return "", false
}

// local — присваивания и локальные константы в теле функции: «key := …» — по имени
// переменной.
func (sc *sourceScan) local(fn *ast.FuncDecl) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	if fn == nil || fn.Body == nil {
		return out
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Lhs) != len(x.Rhs) {
				return true
			}
			for i, l := range x.Lhs {
				if id, ok := l.(*ast.Ident); ok {
					if _, seen := out[id.Name]; !seen {
						out[id.Name] = x.Rhs[i]
					}
				}
			}
		case *ast.DeclStmt:
			gd, ok := x.Decl.(*ast.GenDecl)
			if !ok {
				return true
			}
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if _, seen := out[name.Name]; !seen {
								out[name.Name] = vs.Values[i]
							}
						}
					}
				}
			}
		}
		return true
	})
	return out
}

// shapeOf — вид ключа по выражению. fn нужен, чтобы раскрыть локальные переменные.
func (sc *sourceScan) shapeOf(e ast.Expr, fn *ast.FuncDecl, depth int) keyShape {
	if depth > 6 {
		return keyShape{Kind: "unknown"}
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			v, _ := strconv.Unquote(x.Value)
			return keyShape{Kind: "static", Value: v}
		}
	case *ast.Ident:
		if rhs, ok := sc.local(fn)[x.Name]; ok {
			return sc.shapeOf(rhs, fn, depth+1)
		}
		if v, ok := sc.consts[x.Name]; ok {
			return keyShape{Kind: "static", Value: v}
		}
	case *ast.ParenExpr:
		return sc.shapeOf(x.X, fn, depth+1)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			left := sc.shapeOf(x.X, fn, depth+1)
			right := sc.shapeOf(x.Y, fn, depth+1)
			switch {
			case left.Kind == "static" && right.Kind == "static":
				return keyShape{Kind: "static", Value: left.Value + right.Value}
			case left.Kind == "static" || left.Kind == "prefix":
				return keyShape{Kind: "prefix", Value: left.Value}
			}
		}
	case *ast.CallExpr:
		switch fun := x.Fun.(type) {
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok && id.Name == "fmt" && fun.Sel.Name == "Sprintf" && len(x.Args) > 0 {
				// формат — строка или константа со строкой («chat.msg.%d»)
				if f := sc.shapeOf(x.Args[0], fn, depth+1); f.Kind == "static" {
					if i := strings.Index(f.Value, "%"); i >= 0 {
						return keyShape{Kind: "prefix", Value: f.Value[:i]}
					}
					return keyShape{Kind: "static", Value: f.Value}
				}
			}
			if sc.funcs[fun.Sel.Name] != nil {
				return sc.shapeOfFunc(sc.funcs[fun.Sel.Name], depth+1)
			}
		case *ast.Ident:
			if sc.funcs[fun.Name] != nil {
				return sc.shapeOfFunc(sc.funcs[fun.Name], depth+1)
			}
		}
	case *ast.SelectorExpr:
		// только экспортированная константа пакета: поле структуры `kind.prefix`
		// с тем же именем, что у чужой константы, константой не является
		if v, ok := sc.consts[x.Sel.Name]; ok && x.Sel.IsExported() {
			return keyShape{Kind: "static", Value: v}
		}
	}
	return keyShape{Kind: "unknown"}
}

// shapeOfFunc — вид ключа, который возвращает функция вида `xxxKey(…) string`.
func (sc *sourceScan) shapeOfFunc(fn *ast.FuncDecl, depth int) keyShape {
	if fn.Body == nil {
		return keyShape{Kind: "unknown"}
	}
	var out keyShape
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 && !found {
			out, found = sc.shapeOf(r.Results[0], fn, depth), true
		}
		return !found
	})
	if !found {
		return keyShape{Kind: "unknown"}
	}
	return out
}

func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}

func paramNames(ft *ast.FuncType) map[string]int {
	out := map[string]int{}
	if ft == nil || ft.Params == nil {
		return out
	}
	i := 0
	for _, field := range ft.Params.List {
		for _, id := range field.Names {
			out[id.Name] = i
			i++
		}
	}
	return out
}

// writers — функции, пишущие настройку: дверь и обёртки над ней, находятся
// обходом до неподвижной точки.
func (sc *sourceScan) writers() map[string]int {
	writers := map[string]int{}
	for k, v := range doorWriters {
		writers[k] = v
	}
	for round := 0; round < 4; round++ {
		for _, f := range sc.files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name.Name == "SetSetting" || fn.Name.Name == "setSetting" {
					continue
				}
				params := paramNames(fn.Type)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					c, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					idx, isWriter := writers[calleeName(c)]
					if !isWriter || idx >= len(c.Args) {
						return true
					}
					if id, ok := c.Args[idx].(*ast.Ident); ok {
						if pi, isParam := params[id.Name]; isParam {
							if _, had := writers[fn.Name.Name]; !had {
								writers[fn.Name.Name] = pi
							}
						}
					}
					return true
				})
			}
		}
	}
	return writers
}

// writes — все места записи настройки с видом ключа. Запись, где ключ — параметр
// функции-обёртки или замыкания, не считается: настоящие ключи стоят на местах
// вызова обёртки, а они разбираются на своём месте.
func (sc *sourceScan) writes() []keyShape {
	writers := sc.writers()
	var out []keyShape
	for _, f := range sc.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var stack []ast.Node
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				idx, isWriter := writers[calleeName(c)]
				if !isWriter || idx >= len(c.Args) {
					return true
				}
				if id, ok := c.Args[idx].(*ast.Ident); ok {
					if _, isParam := paramNames(fn.Type)[id.Name]; isParam {
						return true
					}
					for _, up := range stack {
						if lit, ok := up.(*ast.FuncLit); ok {
							if _, isParam := paramNames(lit.Type)[id.Name]; isParam {
								return true
							}
						}
					}
				}
				shape := sc.shapeOf(c.Args[idx], fn, 0)
				shape.Where = sc.fset.Position(c.Pos()).String()
				shape.Func = fn.Name.Name
				var src strings.Builder
				_ = printer.Fprint(&src, sc.fset, c.Args[idx])
				shape.Src = src.String()
				out = append(out, shape)
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Where < out[j].Where })
	return out
}

// TestEveryKeyPrefixHasAnOwner — у каждой приставки ключа есть хозяин: служебная
// отметка журнала или явно названная правка человека.
func TestEveryKeyPrefixHasAnOwner(t *testing.T) {
	sc := scanSources(t)
	marks := ParseSettingMarks(settingsLogMarksSeed)
	prefixes := map[string]string{} // приставка → первое место записи
	for _, w := range sc.writes() {
		if w.Kind == "prefix" && w.Value != "" {
			if _, seen := prefixes[w.Value]; !seen {
				prefixes[w.Value] = w.Where
			}
		}
	}
	if len(prefixes) < 10 {
		t.Fatalf("разбор исходников нашёл слишком мало приставок (%d): сторож сломался сам", len(prefixes))
	}
	for prefix, where := range prefixes {
		marked := IsServiceMark(prefix+"x", marks)
		reason, human := humanPrefixes[prefix]
		switch {
		case marked && human:
			t.Errorf("приставка %q и служебная, и «правка человека»: что-то одно (%s)", prefix, where)
		case !marked && !human:
			t.Errorf("приставка %q (%s) не названа: добавьте её в settingsLogMarksAddedV2, если это пишет Сёма сам, "+
				"или в humanPrefixes с причиной, если пишет человек", prefix, where)
		case human && strings.TrimSpace(reason) == "":
			t.Errorf("приставка %q в humanPrefixes без причины", prefix)
		}
	}
	// устаревшие записи списка: приставки, которую больше никто не пишет
	for prefix := range humanPrefixes {
		if _, seen := prefixes[prefix]; !seen {
			t.Errorf("humanPrefixes: приставки %q в коде больше нет — уберите запись", prefix)
		}
	}
}

// TestUnresolvedWritesAreNamed — запись с невычислимым ключом названа в списке
// `unresolvedWrites` с причиной, а список не копит мёртвых строк.
func TestUnresolvedWritesAreNamed(t *testing.T) {
	sc := scanSources(t)
	seen := map[string]bool{}
	for _, w := range sc.writes() {
		if w.Kind != "unknown" {
			continue
		}
		seen[w.Func] = true
		if reason, ok := unresolvedWrites[w.Func]; !ok {
			t.Errorf("%s (%s): ключ записи %q не вычисляется, а функции нет в unresolvedWrites — назовите, чей это ключ", w.Where, w.Func, w.Src)
		} else if strings.TrimSpace(reason) == "" {
			t.Errorf("unresolvedWrites[%s] без причины", w.Func)
		}
	}
	for fn := range unresolvedWrites {
		if !seen[fn] {
			t.Errorf("unresolvedWrites: у функции %s больше нет невычислимых записей — уберите строку", fn)
		}
	}
}

// TestZZListStaticWrites — разведка: ключи без реестра и без отметки, которые пишет
// код. Ничего не проверяет, печатается только с -v.
func TestZZListStaticWrites(t *testing.T) {
	sc := scanSources(t)
	marks := ParseSettingMarks(settingsLogMarksSeed)
	seen := map[string]string{}
	for _, w := range sc.writes() {
		if w.Kind != "static" || w.Value == "" {
			continue
		}
		if _, ok := LookupSetting(w.Value); ok || IsServiceMark(w.Value, marks) {
			continue
		}
		if _, dup := seen[w.Value]; !dup {
			seen[w.Value] = w.Where + " (" + w.Func + ")"
		}
	}
	var keys []string
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("static  %-40s %s", k, seen[k])
	}
}
