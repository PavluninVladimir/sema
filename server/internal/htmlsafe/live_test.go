package htmlsafe

import (
	"os"
	"strings"
	"testing"
)

// Прогон на настоящем письме: путь к файлу передаётся снаружи, поэтому в
// обычном прогоне тест пропускается.
func TestLiveLetter(t *testing.T) {
	path := os.Getenv("SEMA_LETTER")
	if path == "" {
		t.Skip("нужен SEMA_LETTER=<путь к html>")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := Clean(string(raw), false)
	t.Logf("было %d знаков, стало %d, картинок %d, ссылок %d",
		len(raw), len(r.HTML), r.Images, r.Links)
	visible := strings.TrimSpace(r.HTML)
	if len(visible) < 200 {
		t.Errorf("после очистки почти ничего не осталось:\n%s", visible)
	}
	t.Logf("начало: %s", clipForTest(r.HTML, 400))
}

func clipForTest(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
