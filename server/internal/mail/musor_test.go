package mail

import (
	"strings"
	"testing"
)

// Мусор из письма не доезжает до архива.
//
// 10 сентября 2026 в тексте письма ECCO стояло «4 дня –15% … · 96 ·
// Успейте купить …», а дальше три сотни «&zwnj;». Три разные беды в
// одном письме, и каждая ниже проверяется отдельно.
func TestConditionalCommentDoesNotLeakItsFilling(t *testing.T) {
	// «<[^>]+>» видит здесь два отдельных «тега» и оставляет всё, что
	// между ними: так «96» из настройки Outlook оказалось текстом письма
	html := `<p>Скидка 15%</p>
<!--[if (gte mso 9)|(IE)]>
<xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml>
<![endif]-->
<p>Успейте купить</p>`
	got := tidyText(stripHTML(html), 500)
	if strings.Contains(got, "96") {
		t.Errorf("начинка условного комментария в тексте письма: %q", got)
	}
	if strings.Contains(got, "endif") || strings.Contains(got, "OfficeDocument") {
		t.Errorf("обломки комментария в тексте: %q", got)
	}
	for _, want := range []string{"Скидка 15%", "Успейте купить"} {
		if !strings.Contains(got, want) {
			t.Errorf("вместе с мусором вычистили текст: нет %q в %q", want, got)
		}
	}
}

// Комментарий, открытый для всех кроме Outlook, показывает своё
// содержимое всем — его вычищать нельзя.
func TestOpenConditionalCommentKeepsText(t *testing.T) {
	html := `<!--[if !mso]><!--><p>Это видят все</p><!--<![endif]-->`
	got := tidyText(stripHTML(html), 500)
	if !strings.Contains(got, "Это видят все") {
		t.Errorf("текст, открытый всем, пропал: %q", got)
	}
}

// Набивка сущностями: рассыльщик пишет «&zwnj;» сотнями, чтобы
// растянуть преамбулу. Текстовую часть письма многие собирают наивным
// снятием тегов, и к нам это приезжает буквами, а не знаком.
func TestEntityPaddingIsRemoved(t *testing.T) {
	text := "Скидка 15%\n" + strings.Repeat("&zwnj; ", 200) + "\nКонец"
	got := tidyText(text, 500)
	if strings.Contains(got, "zwnj") {
		t.Errorf("набивка осталась: %q", got)
	}
	if !strings.Contains(got, "Скидка 15%") || !strings.Contains(got, "Конец") {
		t.Errorf("текст потерян: %q", got)
	}
	// одиночная сущность — законная: письмо может быть про вёрстку
	if got := tidyText("ставьте &nbsp; между словами", 500); !strings.Contains(got, "&nbsp;") {
		t.Errorf("одиночная сущность вычищена зря: %q", got)
	}
}

// Знаки, которые рисуются пустотой, — тоже набивка: брайлевский пробел
// и хангыльский филлер растягивают преамбулу в списке писем.
func TestBlankCharsDoNotCountAsText(t *testing.T) {
	got := tidyText("Заголовок\n"+strings.Repeat("⠀", 300)+"\nТекст", 500)
	if strings.Contains(got, "⠀") {
		t.Errorf("брайлевский пробел остался: %q", got)
	}
	if got := tidyText(strings.Repeat("ㅤ", 50)+"Текст", 500); strings.Contains(got, "ㅤ") {
		t.Errorf("хангыльский филлер остался: %q", got)
	}
}

// Из двух версий текста берём ту, где больше букв: текстовую часть
// рассыльщик нередко собирает хуже, чем мы сами разбираем HTML.
// Считаем по уже очищенным строкам — как это и делает разбор письма:
// в сырой набивке «&zwnj;» есть буквы «zwnj», и сравнение до чистки
// объявило бы её текстом.
func TestLettersCountedNotLength(t *testing.T) {
	padding := tidyText(strings.Repeat("&zwnj; ", 300)+"96", 8000)
	own := tidyText("Скидка 15%. Успейте купить, пока выгода действует.", 8000)
	if lettersIn(padding)*2 >= lettersIn(own) {
		t.Errorf("набивка (%d букв) считается текстом наравне с письмом (%d букв)",
			lettersIn(padding), lettersIn(own))
	}
}

// Превью, в котором нет письма, а есть его вёрстка: показывать такое
// незачем — в списке останется тема письма.
func TestPreviewFromMarkupGoesEmpty(t *testing.T) {
	for _, raw := range []string{
		`{border-collapse:collapse;mso-table-lspace:0;mso-t able-rspace:0`,
		`table width="632" border="0" cellpadding="0"`,
	} {
		if got := CleanSnippet(raw); got != "" {
			t.Errorf("вёрстка осталась превью: %q → %q", raw, got)
		}
	}
}
