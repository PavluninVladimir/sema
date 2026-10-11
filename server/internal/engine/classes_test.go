package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Классы чувствительности: секретное наружу не уходит никогда, чувствительное
// — по согласию, пометка человека важнее приметы.

func TestOutsideCut(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	// по умолчанию чувствительное отдаём
	cases := []struct {
		cloud bool
		class string
		give  bool
	}{
		{false, store.ClassSecret, true},   // локальной модели можно всё
		{true, store.ClassPlain, true},     // обычное уходит
		{true, store.ClassSensitive, true}, // режим «разрешать»
		{true, store.ClassSecret, false},   // секретное — никогда
	}
	for _, c := range cases {
		give, why := e.outsideCut(c.cloud, c.class, "кто@то.рф")
		if give != c.give {
			t.Errorf("outsideCut(облако=%v, %q) = %v (%s)", c.cloud, c.class, give, why)
		}
	}
	// режим «спрашивать»: чувствительное вырезается, пока не разрешат
	if err := e.SetSensitiveMode("ask"); err != nil {
		t.Fatal(err)
	}
	if give, why := e.outsideCut(true, store.ClassSensitive, "банк@пример.рф"); give {
		t.Errorf("чувствительное ушло без согласия (%s)", why)
	}
	if err := st.SetConsent("sender", "банк@пример.рф", true, "Вадим"); err != nil {
		t.Fatal(err)
	}
	if give, _ := e.outsideCut(true, store.ClassSensitive, "банк@пример.рф"); !give {
		t.Error("после согласия письмо всё равно вырезано")
	}
	// согласие одному отправителю не открывает других
	if give, _ := e.outsideCut(true, store.ClassSensitive, "другой@пример.рф"); give {
		t.Error("согласие распространилось на чужого отправителя")
	}
	// и секретное не открывается согласием
	if give, _ := e.outsideCut(true, store.ClassSecret, "банк@пример.рф"); give {
		t.Error("секретное ушло по согласию на чувствительное")
	}
}

// Модель узнаёт, что часть контекста скрыта: молча урезанный контекст даёт
// уверенный ответ по неполным данным.
func TestCutNotice(t *testing.T) {
	if cutNotice(0, 0, nil) != "" {
		t.Error("без вырезанного лишняя строка")
	}
	got := cutNotice(2, 1, []string{"банк@пример.рф"})
	for _, want := range []string{"2", "1", "Withheld", "банк@пример.рф"} {
		if !strings.Contains(got, want) {
			t.Errorf("в пометке нет %q: %s", want, got)
		}
	}
}

// Класс считается один раз и запоминается; пометка человека главнее.
func TestClassOfMessageRemembers(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{ID: 42, FromAddr: "shop@пример.рф", Subject: "Счёт на оплату", Snippet: "сумма 1890 ₽"}
	if got := e.ClassOfMessage(m); got != store.ClassSensitive {
		t.Fatalf("класс письма со счётом: %q", got)
	}
	saved, ok := st.ContentClass(store.ClassMsg, 42)
	if !ok || saved != store.ClassSensitive {
		t.Fatalf("класс не запомнен: %q, %v", saved, ok)
	}
	// человек пометил иначе — примета больше не спорит
	if err := e.MarkClass(store.ClassMsg, 42, store.ClassSecret, "Вадим"); err != nil {
		t.Fatal(err)
	}
	if got := e.ClassOfMessage(m); got != store.ClassSecret {
		t.Errorf("пометка человека затёрта приметой: %q", got)
	}
	mark, _ := st.ClassMarkOf(store.ClassMsg, 42)
	if mark.By != "Вадим" {
		t.Errorf("кто пометил: %q", mark.By)
	}
	if err := e.MarkClass(store.ClassMsg, 42, "чепуха", "Вадим"); err == nil {
		t.Error("принят несуществующий класс")
	}
}
