package engine

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Три написания одного поставщика сводятся в одну строку — на живой Сёме
// это двенадцать документов на 17 051 ₽, которые не складывались.
func TestWritingConsolidatedInOneProvider(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	writing := []struct {
		name   string
		amount float64
	}{
		{`АО "Горэнерго"`, 1000},
		{`АО Горэнерго`, 2000},
		{`ОП Городское отделение АО Горэнерго`, 3000},
	}
	for i, n := range writing {
		a := &store.Attachment{Filename: "счёт.pdf", SHA256: string(rune('а' + i))}
		if err := st.InsertAttachment(a); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkAttachment(a.ID, store.AttKindReceipt, n.name, n.amount, "RUB", "счёт"); err != nil {
			t.Fatal(err)
		}
		fresh, err := st.AttachmentByID(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.FileProvider(fresh); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.Vendors()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		var names []string
		for _, v := range list {
			names = append(names, v.Name)
		}
		t.Fatalf("поставщиков %d, а он один: %v", len(list), names)
	}
	v := list[0]
	if v.Files != 3 || v.Sum != 6000 {
		t.Errorf("документов %d на %.0f, а надо 3 на 6000", v.Files, v.Sum)
	}
	if len(v.Names) != 3 {
		t.Errorf("написаний запомнено %d, а их 3: %v", len(v.Names), v.Names)
	}
	// сырое имя на файле не тронуто: так написано в документе
	files, err := st.FilesOfVendor(v.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Vendor] = true
	}
	for _, n := range writing {
		if !seen[n.name] {
			t.Errorf("сырое имя %q потеряно", n.name)
		}
	}
}

// Догадки не делаем: «Т Плюс ПАО» и «ТП ПАО» — это разные строки, пока
// человек не скажет обратное. Сложить чужие деньги хуже, чем показать две
// строки.
func TestUnguessableNotConsolidated(t *testing.T) {
	// справочник форм — тот же, что в бою: список по умолчанию
	form := formFromLine(formSeed)
	if providerKey("Т Плюс ПАО", form) == providerKey("ТП ПАО", form) {
		t.Error("Сёма догадался за человека и сложил разных поставщиков")
	}
	// а вот это — одно и то же
	pairs := [][2]string{
		{`ООО "УМНЫЙ РИТЕЙЛ"`, `УМНЫЙ РИТЕЙЛ`},
		{`ООО «Ле Монлид»`, `Ле Монлид`},
		{`АО "Горэнерго"`, `ОП Городское отделение АО Горэнерго`},
		{`LTD Beget`, `Beget`},
	}
	for _, p := range pairs {
		if providerKey(p[0], form) != providerKey(p[1], form) {
			t.Errorf("%q и %q не свелись: %q против %q",
				p[0], p[1], providerKey(p[0], form), providerKey(p[1], form))
		}
	}
}

// Слияние руками ничего не портит: документы переезжают, сырые имена
// остаются, а второй записи больше нет.
func TestMergeByHandNotLosesDocuments(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	one := providerFile(t, e, st, "Т Плюс ПАО", 700, "а")
	two := providerFile(t, e, st, "ТП ПАО", 300, "б")
	if one == two {
		t.Fatal("эти двое не должны были слиться сами")
	}
	if err := st.MergeVendors(one, two); err != nil {
		t.Fatal(err)
	}
	list, err := st.Vendors()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("после слияния поставщиков %d", len(list))
	}
	if list[0].Files != 2 || list[0].Sum != 1000 {
		t.Errorf("после слияния %d документов на %.0f, а надо 2 на 1000", list[0].Files, list[0].Sum)
	}
	if len(list[0].Names) != 2 {
		t.Errorf("написания при слиянии потеряны: %v", list[0].Names)
	}
}

// providerFile — файл с суммой и поставщиком; возвращает поставщика.
func providerFile(t *testing.T, e *Engine, st *store.Store, name string, amount float64, sha string) int64 {
	t.Helper()
	_, provider := providerDocument(t, e, st, name, amount, sha)
	return provider
}

// providerDocument — тот же файл, но отдаёт и сам документ: тестам про
// платежи нужен он, а не поставщик.
func providerDocument(t *testing.T, e *Engine, st *store.Store, name string, amount float64, sha string) (*store.Attachment, int64) {
	t.Helper()
	a := &store.Attachment{Filename: "счёт.pdf", SHA256: sha}
	if err := st.InsertAttachment(a); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkAttachment(a.ID, store.AttKindReceipt, name, amount, "RUB", "счёт"); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.AttachmentByID(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := e.FileProvider(fresh)
	if err != nil {
		t.Fatal(err)
	}
	return fresh, provider
}
