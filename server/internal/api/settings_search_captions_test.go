package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Строка результата поиска короткая: путь, название и значение с первой фразой
// подсказки — не абзац. Макет рисует её в две-три строки, и длинная подсказка
// (у «Окна» сервера их три предложения) растянула бы выдачу.
func TestSearchResultCaptionsAreShort(t *testing.T) {
	s, _ := indexFix(t, 40)
	for _, q := range []string{"окно", "пуш", "опрос", "цена", "ssh"} {
		settings, _ := resultRows(searchDoc(t, s, "settings", q))
		if len(settings) == 0 {
			t.Fatalf("«%s» ничего не нашёл", q)
		}
		for _, r := range settings {
			if n := utf8.RuneCountInString(r.Caption); n > 160 {
				t.Errorf("«%s»: подпись результата %q — %d знаков", q, r.Subtitle, n)
			}
			if strings.Contains(r.Caption, "\n") {
				t.Errorf("«%s»: перевод строки в подписи результата %q", q, r.Subtitle)
			}
		}
	}
}
