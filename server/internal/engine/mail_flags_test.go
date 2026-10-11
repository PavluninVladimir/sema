package engine

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ядро не знает модулей по имени. Признак сбора у ящика объявляет тот,
// кто его исполняет: ядро — свой, модуль — свой, в манифесте. Раньше
// «receipts» и «attention» были зашиты в ядре, и галочка «Чеки и счета»
// пережила сам модуль чеков: человеку предлагали включить то, чего нет.
func TestFlagsDeclareSameWhoTheirExecutes(t *testing.T) {
	e, _ := newTestEngine(t, "модель", nil)

	// на чистой машине есть только признак ядра
	flags := e.MailFlags()
	if len(flags) != 1 || flags[0].Name != store.CollectAttentionName {
		t.Fatalf("без модулей ожидался один признак ядра, а вышло %+v", flags)
	}
	if flags[0].Module != "" {
		t.Fatalf("признак ядра приписан модулю %q", flags[0].Module)
	}

	// модуль приносит свою галочку вместе с собой
	m := &Module{Name: "чеки", Title: "Чеки", Flag: "receipts",
		FlagTitle: "Считать чеки и счета", FlagDefault: "personal",
		Code: "const run = () => 'ок';", Enabled: true}
	if err := e.InstallModule(m); err != nil {
		t.Fatalf("установка модуля: %v", err)
	}
	if err := e.EnableModule("чеки", true); err != nil {
		t.Fatalf("включение: %v", err)
	}
	f, ok := e.MailFlagByName("receipts")
	if !ok {
		t.Fatal("модуль принёс признак, а ядро его не видит")
	}
	if f.Title != "Считать чеки и счета" || f.Module != "чеки" {
		t.Fatalf("подпись и хозяин признака берутся не из манифеста: %+v", f)
	}

	// умолчание «personal»: у рабочего ящика не собираем, у личного собираем
	work := &store.MailConf{Purpose: "work"}
	private := &store.MailConf{Purpose: "personal"}
	if f.On(work) {
		t.Error("у рабочего ящика признак «personal» должен быть выключен")
	}
	if !f.On(private) {
		t.Error("у личного ящика признак «personal» должен быть включён")
	}

	// решение человека сильнее умолчания
	work.SetCollect("receipts", true)
	if !f.On(work) {
		t.Error("галочка человека не пересилила умолчание")
	}

	// модуль выключили — галочки нет: включать нечего
	if err := e.EnableModule("чеки", false); err != nil {
		t.Fatalf("выключение: %v", err)
	}
	if _, ok := e.MailFlagByName("receipts"); ok {
		t.Error("модуль выключен, а его галочка всё ещё предлагается человеку")
	}
}

// Настройки, сделанные до переезда на карту, читаются по-прежнему.
func TestOldCheckboxAccountRead(t *testing.T) {
	no := false
	c := &store.MailConf{CollectAttention: &no}
	if c.AttentionOn() {
		t.Error("старое поле collect_attention перестало читаться")
	}
	if c.CollectOn("receipts", true) != true {
		t.Error("незаданный признак должен брать умолчание")
	}
}

// Описания прав не обещают того, чего нет: мёртвая строка про «receipts»
// жила в ядре после переезда чеков в модуль, и модель по ней просила
// право, которого не существует.
func TestDescriptionGrantsNotPromiseMissing(t *testing.T) {
	for name := range primitiveWhy {
		if _, ok := primitives[name]; !ok {
			t.Errorf("описано право %q, которого нет в списке прав", name)
		}
	}
}
