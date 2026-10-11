package engine

import (
	"strings"
	"testing"
)

// Код модулей пишет модель, и он приходит каким угодно: оборванным на
// середине, с чужими скобками, с кириллицей в именах. Разбор не должен ни
// падать, ни зависать — он обязан либо разобрать, либо честно сказать
// «код не разобран». Падение здесь роняет установку модуля, а зависание —
// весь сервер: разбор идёт при каждом сохранении из чата.
func FuzzParseModuleCode(f *testing.F) {
	f.Add("function run() { return 1 }")
	f.Add("const run = () => sema.llm('привет')")
	f.Add("function run(){sema.data.put('x',{a:1})}\nfunction ещё(){}")
	f.Add("{{{")
	f.Add("function run() {")
	f.Add("async function run(){ await sema.http.get('http://x') }")
	f.Add("var run = function(){ sema.secret('ssh:vps') }")
	f.Add("")
	f.Add("\x00\x00")
	f.Add(strings.Repeat("function f(){}", 500))

	f.Fuzz(func(t *testing.T, code string) {
		c, err := ParseModuleCode(code)
		if err != nil {
			// ошибка обязана быть объяснимой, а не пустой
			if c.Err == "" && !strings.Contains(err.Error(), "код не разобран") {
				t.Fatalf("ошибка без объяснения: %v", err)
			}
			return
		}
		// разобралось — значит у каждой функции есть имя и вменяемые границы
		for _, fn := range c.Funcs {
			if fn.Name == "" {
				t.Fatalf("функция без имени в %q", clip(code, 120))
			}
			if fn.Lines < 0 {
				t.Fatalf("у функции %q отрицательный размер: %d", fn.Name, fn.Lines)
			}
		}
		// имена лежат без приставки («data.put»), и именно так они
		// сверяются с правами: приставка сломала бы поиск права
		for _, u := range c.Uses {
			if u == "" || strings.HasPrefix(u, "sema.") || strings.HasPrefix(u, ".") || strings.HasSuffix(u, ".") {
				t.Fatalf("имя обращения испорчено: %q", u)
			}
		}
	})
}
