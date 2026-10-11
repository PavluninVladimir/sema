package engine

import "testing"

func TestParseBuiltin(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	for _, name := range []string{"receipts", "rates"} {
		m, ok := e.Module(name)
		if !ok {
			t.Logf("модуля %q нет", name)
			continue
		}
		check, err := e.CheckModuleCode(m)
		if err != nil {
			t.Errorf("модуль %s: %v", name, err)
			continue
		}
		t.Logf("модуль %s: функций %d, права по коду не хватает %v, лишние %v, непонятно %v",
			name, len(check.Code.Funcs), check.Missing, check.Extra, check.Unknown)
		t.Logf("обращения: %v", check.Code.Uses)
		t.Logf("оглавление:\n%s", check.Code.Outline())
	}
}
