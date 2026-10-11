package mail

import (
	"strings"
	"testing"
)

// Свой список сущностей делал две беды сразу, и вторая — не косметическая.
func TestEntitiesInMessageText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"числовая сущность — это буква, а не пробел",
			"Пр&#1086;зрачность", "Прозрачность"},
		{"разорванная переносом числовая сущность",
			"Пр&#1 086;зрачность", "Прозрачность"},
		{"незнакомая сущность не остаётся текстом",
			"Скидка&zwnj;&zwnj;&zwnj; 15%", "Скидка 15%"},
		{"известные разворачиваются как прежде",
			"&laquo;ECCO&raquo; &mdash; обувь", "«ECCO» — обувь"},
		{"разметка, спрятанная за сущностями, тоже уходит",
			"текст &lt;td width=3D&quot;10&quot;&gt; ещё", "текст  ещё"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tidyText(stripHTML(c.in), 8000)
			// пробелы схлопнуты tidyText — сравниваем по словам
			if norm(got) != norm(c.want) {
				t.Errorf("вышло %q, надо %q", got, c.want)
			}
		})
	}
}

// Преамбула рассылки из трёхсот «&zwnj;» не должна доезжать до человека и
// до модели: она съедает предел текста, ничего не значащий.
func TestInvisiblePreambleNeverArrives(t *testing.T) {
	src := "Успейте купить" + strings.Repeat("&nbsp;&zwnj;", 300) + "Скидка 15%"
	got := tidyText(stripHTML(src), 8000)
	if strings.Contains(got, "zwnj") {
		t.Errorf("сущности остались текстом: %q", clipRunes(got, 80))
	}
	if len([]rune(got)) > 60 {
		t.Errorf("преамбула съела %d знаков: %q", len([]rune(got)), clipRunes(got, 80))
	}
	if !strings.Contains(got, "Скидка 15%") {
		t.Errorf("текст после преамбулы потерян: %q", got)
	}
}

// clipRunes — обрезка для сообщения об ошибке: по рунам, иначе тест сам
// печатает битый UTF-8.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }
