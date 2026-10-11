package engine

// Правка модуля по функциям. Модуль целиком переписывался на каждую
// правку, и 17 сентября 2026 «Технический форум» дорос до 1400 строк:
// ответ модели обрывался на середине (у deepseek — по времени, у Opus —
// на 32 000 токенах), а каждый заход стоил около доллара. Блок
// ```sema-module-patch:<имя>``` несёт только то, что меняется: объявления
// верхнего уровня (функции, переменные) заменяют одноимённые, новые
// дописываются в конец; манифест в начале блока необязателен и меняет
// только названные поля, «remove» убирает объявления. Дальше модуль идёт
// тем же путём, что целиком: разбор, проверка прав и связей, установка.

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// decl — кусок кода верхнего уровня, у которого есть имя.
type decl struct {
	names      []string
	start, end int // смещения в байтах, [начало, конец)
}

// topLevelDecls — функции и переменные верхнего уровня с их
// границами в исходнике.
func topLevelDecls(code string) ([]decl, error) {
	prog, err := parser.ParseFile(nil, "module.js", code, 0)
	if err != nil {
		return nil, err
	}
	var out []decl
	for _, st := range prog.Body {
		var names []string
		switch v := st.(type) {
		case *ast.FunctionDeclaration:
			if v.Function != nil && v.Function.Name != nil {
				names = []string{v.Function.Name.Name.String()}
			}
		case *ast.VariableStatement:
			names = bindingNames(v.List)
		case *ast.LexicalDeclaration:
			names = bindingNames(v.List)
		}
		if len(names) == 0 {
			continue
		}
		start, end := int(st.Idx0())-1, int(st.Idx1())-1
		if start < 0 || end > len(code) || start >= end {
			continue
		}
		// точка с запятой после переменной — часть объявления
		if tail := strings.TrimLeft(code[end:], " \t"); strings.HasPrefix(tail, ";") {
			end = len(code) - len(tail) + 1
		}
		out = append(out, decl{names: names, start: start, end: end})
	}
	return out, nil
}

func bindingNames(list []*ast.Binding) []string {
	var out []string
	for _, b := range list {
		if id, ok := b.Target.(*ast.Identifier); ok {
			out = append(out, id.Name.String())
		}
	}
	return out
}

// CodePatch — итог наложения правки: новый код и что в нём поменялось.
type CodePatch struct {
	Code     string
	Replaced []string
	Added    []string
	Removed  []string
}

// ApplyPatch — заменить в коде модуля одноимённые объявления, дописать
// новые и убрать перечисленные в remove.
func ApplyPatch(old, patch string, remove []string) (CodePatch, error) {
	was, err := topLevelDecls(old)
	if err != nil {
		return CodePatch{}, fmt.Errorf("код установленного модуля не разбирается: %v", err)
	}
	became, err := topLevelDecls(patch)
	if err != nil {
		return CodePatch{}, fmt.Errorf("код правки не разобран: %v", err)
	}
	if len(became) == 0 && len(remove) == 0 {
		return CodePatch{}, fmt.Errorf("в правке нет ни одного объявления верхнего уровня: присылай функции целиком — function имя(…) { … }")
	}
	byName := map[string]int{}
	for i, o := range was {
		for _, name := range o.names {
			byName[name] = i
		}
	}
	replace := map[int]string{} // номер старого объявления → новый текст ("" — убрать)
	var result CodePatch
	var tail []string
	for _, o := range became {
		text := patch[o.start:o.end]
		i, has := -1, false
		for _, name := range o.names {
			if j, ok := byName[name]; ok {
				i, has = j, true
				break
			}
		}
		if !has {
			tail = append(tail, text)
			result.Added = append(result.Added, strings.Join(o.names, ", "))
			continue
		}
		if _, already := replace[i]; already {
			return CodePatch{}, fmt.Errorf("в правке «%s» объявлено дважды", strings.Join(o.names, ", "))
		}
		replace[i] = text
		result.Replaced = append(result.Replaced, strings.Join(o.names, ", "))
	}
	for _, name := range remove {
		i, ok := byName[strings.TrimSpace(name)]
		if !ok {
			return CodePatch{}, fmt.Errorf("убрать «%s» нельзя: такого объявления в модуле нет", name)
		}
		if _, already := replace[i]; already {
			return CodePatch{}, fmt.Errorf("«%s» и заменяется, и убирается", name)
		}
		replace[i] = ""
		result.Removed = append(result.Removed, name)
	}
	// с конца, чтобы смещения ещё не тронутых кусков не съезжали
	number := make([]int, 0, len(replace))
	for i := range replace {
		number = append(number, i)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(number)))
	code := old
	for _, i := range number {
		o := was[i]
		code = code[:o.start] + replace[i] + code[o.end:]
	}
	if len(tail) > 0 {
		code = strings.TrimRight(code, "\n ") + "\n\n" + strings.Join(tail, "\n\n") + "\n"
	}
	result.Code = code
	return result, nil
}

// extractModulePatch — блок ```sema-module-patch:<имя>```: имя модуля,
// содержимое и ответ без блока.
func extractModulePatch(answer string) (name, block, clean string, ok bool) {
	lines := strings.Split(answer, "\n")
	start, depth := -1, 0
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if start < 0 {
			if strings.HasPrefix(t, "```sema-module-patch") {
				start, depth = i, 1
				name = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "```sema-module-patch"), ":"))
			}
			continue
		}
		switch {
		case strings.HasPrefix(t, "```") && len(t) > 3:
			depth++
		case t == "```":
			depth--
		}
		if depth == 0 {
			block = strings.Join(lines[start+1:i], "\n")
			clean = strings.TrimSpace(strings.Join(append(append([]string{}, lines[:start]...), lines[i+1:]...), "\n"))
			return name, block, clean, true
		}
	}
	if start >= 0 {
		return name, strings.Join(lines[start+1:], "\n"), strings.TrimSpace(strings.Join(lines[:start], "\n")), true
	}
	return "", "", answer, false
}

// moduleByPatch — установленный модуль с наложенной правкой, собранный
// так же, как если бы модель прислала его целиком.
func (e *Engine) moduleByPatch(name, block string) (*Module, CodePatch, error) {
	var result CodePatch
	block = strings.TrimSpace(fenceLine.ReplaceAllString(block, ""))
	old, ok := e.Module(name)
	if name == "" || !ok {
		return nil, result, fmt.Errorf("правка для модуля «%s», а такого модуля нет: в ограждении пиши имя установленного модуля — ```sema-module-patch:<name>", name)
	}
	if old.Builtin {
		return nil, result, fmt.Errorf("«%s» — встроенный модуль: правкой его не менять, пришли копию целиком под новым name", name)
	}
	// манифест в правке необязателен: только поля, которые меняются
	fields := map[string]any{}
	code := block
	if before := strings.TrimSpace(skipLeadingComments(block)); strings.HasPrefix(before, "{") {
		head, rest := splitManifest(before)
		if head == "" || json.Unmarshal([]byte(head), &fields) != nil {
			return nil, result, fmt.Errorf("манифест правки не разобран: это должен быть JSON-объект только с меняющимися полями")
		}
		code = rest
	}
	var remove []string
	if v, ok := fields["remove"]; ok {
		remove = pickStrings(map[string]any{"remove": v}, "remove")
		delete(fields, "remove")
	}
	result, err := ApplyPatch(old.Code, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(code), "---")), remove)
	if err != nil {
		return nil, result, err
	}
	// прежний манифест, поверх — поля правки; решения человека ParseModule
	// всё равно обнуляет, а InstallModule переносит с установленного
	raw, err := json.Marshal(old)
	if err != nil {
		return nil, result, err
	}
	manifest := map[string]any{}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, result, err
	}
	for _, k := range []string{"code", "granted", "granted_memory", "granted_hosts", "granted_mcp", "granted_events", "enabled", "pending_needs", "builtin", "broken", "broken_why"} {
		delete(manifest, k)
	}
	for k, v := range fields {
		if k == "name" {
			continue // правка не переименовывает модуль
		}
		manifest[k] = v
	}
	if v, ok := fields["on"]; ok { // «on» в тексте модели, «on_handlers» в хранилище
		delete(manifest, "on_handlers")
		manifest["on"] = v
	}
	head, err := json.Marshal(manifest)
	if err != nil {
		return nil, result, err
	}
	m, err := ParseModule(string(head) + "\n" + result.Code)
	if err != nil {
		return nil, result, err
	}
	log.Printf("правка модуля «%s»: заменены %v, добавлены %v, убраны %v", name, result.Replaced, result.Added, result.Removed)
	return m, result, nil
}
