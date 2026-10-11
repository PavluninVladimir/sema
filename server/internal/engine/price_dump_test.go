package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Отладка разбора страниц: рядом с каждым `<имя>.html` из SEMA_PRICE_PAGES
// кладёт `<имя>.txt` (то, что уйдёт модели) и `<имя>.sec` (то же с номером
// строки и разделом), чтобы видеть, где проходит граница разделов и какая
// шапка стоит над таблицей. Без переменной — пропуск.
func TestPriceDumpPages(t *testing.T) {
	dir := os.Getenv("SEMA_PRICE_PAGES")
	if dir == "" {
		t.Skip("SEMA_PRICE_PAGES не задан")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.html"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		doc := priceDocument(string(raw))
		base := strings.TrimSuffix(f, ".html")
		if err := os.WriteFile(base+".txt", []byte(doc.Text), 0o644); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for i, l := range strings.Split(doc.Text, "\n") {
			sec := ""
			if i < len(doc.Sections) {
				sec = doc.Sections[i]
			}
			fmt.Fprintf(&b, "%d\t[%s]\t%s\n", i, sec, l)
		}
		if err := os.WriteFile(base+".sec", []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
