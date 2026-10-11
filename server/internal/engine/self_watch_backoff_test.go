package engine

import (
	"strings"
	"testing"
	"time"
)

// Растущая пауза — нормальная заминка, пока она растёт. Сто двадцать
// обрывов MAX за тринадцать часов с паузой, застрявшей в потолке, никто не
// заметил: каждая строка по отдельности выглядела обычной.
func TestGrowingPauseNotTrouble(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	for _, p := range []time.Duration{5 * time.Second, 10 * time.Second, time.Minute} {
		e.Dropped("MAX", p, 5*time.Minute, "context deadline exceeded")
	}
	if n := e.checkPauses(); n != 0 {
		t.Errorf("растущая пауза объявлена бедой (%d находок)", n)
	}
	if findings, _ := st.Findings(true, 5); len(findings) > 0 {
		t.Errorf("находка есть: %s", findings[0].Summary)
	}
}

// Пауза, упёршаяся в потолок и стоящая там дольше порога, — уже не
// заминка, а состояние.
func TestPauseInCapIsState(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.SetSetting("watch.ceiling_min", "0"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		e.Dropped("MAX", 5*time.Minute, 5*time.Minute, "context deadline exceeded")
	}
	if n := e.checkPauses(); n == 0 {
		t.Fatal("пауза стоит в потолке, а находки нет")
	}
	findings, _ := st.Findings(true, 5)
	if len(findings) == 0 {
		t.Fatal("находки нет")
	}
	n := findings[0]
	if !strings.Contains(n.Numbers, "обрывов 120") {
		t.Errorf("не сказано, сколько было обрывов: %q", n.Numbers)
	}
	if !strings.Contains(n.Numbers, "deadline") {
		t.Errorf("не названа причина: %q", n.Numbers)
	}
}

// Связь вернулась — находка закрывается сама. Обнуление паузы и есть то,
// за чем наблюдаем.
func TestRestoredLinkClosesFinding(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.SetSetting("watch.ceiling_min", "0"); err != nil {
		t.Fatal(err)
	}
	e.Dropped("MAX", 5*time.Minute, 5*time.Minute, "обрыв")
	e.checkPauses()
	if open, _ := st.Findings(true, 5); len(open) == 0 {
		t.Fatal("находки нет — проверять нечего")
	}
	e.LinkAlive("MAX")
	e.checkPauses()
	if open, _ := st.Findings(true, 5); len(open) > 0 {
		t.Errorf("связь вернулась, а находка осталась открытой: %s", open[0].Summary)
	}
}

// Связь, которая рвалась и снова пошла, начинает счёт заново: пауза
// должна где-то обнуляться, иначе она меряет всю историю, а не нынешнюю
// беду.
func TestAfterRollbackCountStartsAnew(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	for i := 0; i < 10; i++ {
		e.Dropped("MAX", 5*time.Minute, 5*time.Minute, "обрыв")
	}
	e.LinkAlive("MAX")
	e.Dropped("MAX", 5*time.Second, 5*time.Minute, "обрыв")
	res := e.LinkUnderWatch()
	if _, has := res["MAX"]; has {
		t.Error("после возврата связи пауза снова считается стоящей в потолке")
	}
}
