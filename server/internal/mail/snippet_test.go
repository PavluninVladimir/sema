package mail

import (
	"strings"
	"testing"
)

// Превью письма — это текст письма, а не его устройство. Раньше в него
// уезжали границы частей и содержимое <style>, и то же самое уходило в
// контекст модели и в вектор поиска.
func TestCleanSnippetDropsMIME(t *testing.T) {
	raw := "----boundary_6123_4b474693-4fda-4201\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"<html><head><style>@viewport {width: device-width;} body { background: #fff; }</style></head>" +
		"<body><p>Счёт на оплату 2 165,76 руб.</p></body></html>"
	got := cleanSnippet(raw)
	for _, bad := range []string{"boundary", "Content-Type", "@viewport", "background"} {
		if strings.Contains(got, bad) {
			t.Errorf("в превью осталось %q: %s", bad, got)
		}
	}
	if !strings.Contains(got, "Счёт на оплату 2 165,76 руб.") {
		t.Errorf("текст письма потерялся: %q", got)
	}
}

// Обычное письмо чистка не портит: ни текста, ни двойных дефисов подписи.
func TestCleanSnippetKeepsPlainText(t *testing.T) {
	raw := "Здравствуйте!\nСчёт во вложении, срок оплаты 15.09.\n--\nС уважением, Пётр"
	got := cleanSnippet(raw)
	for _, want := range []string{"Здравствуйте", "срок оплаты 15.09", "Пётр"} {
		if !strings.Contains(got, want) {
			t.Errorf("потеряно %q в %q", want, got)
		}
	}
}

// Правила CSS без тега <style> (письмо обрезано на середине) тоже не текст.
func TestCleanSnippetDropsLooseCSS(t *testing.T) {
	raw := "@media (max-width: 600px) { .wrap { padding: 0 } } Ваш заказ собран"
	got := cleanSnippet(raw)
	if strings.Contains(got, "max-width") || strings.Contains(got, "padding") {
		t.Errorf("остатки CSS в превью: %q", got)
	}
	if !strings.Contains(got, "Ваш заказ собран") {
		t.Errorf("текст потерялся: %q", got)
	}
}

// Сохранённые превью уже склеены в одну строку: заголовки и границы там
// стоят через пробел, и построчная чистка их не берёт.
func TestCleanSnippetDropsInlineMIME(t *testing.T) {
	raw := "----boundary_6123_4b474693 Content-Type: text/html; charset=utf-8 " +
		"Content-Transfer-Encoding: quoted-printable Здравствуйте! Ваш счёт готов."
	got := cleanSnippet(raw)
	for _, bad := range []string{"boundary", "Content-Type", "charset", "quoted-printable"} {
		if strings.Contains(got, bad) {
			t.Errorf("осталось %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "Ваш счёт готов") {
		t.Errorf("текст потерялся: %q", got)
	}
}

// Тело письма в base64, за которым идёт ещё что-то, раньше не
// расшифровывалось вовсе: в списке писем стояло «0KPQstCw0LbQsNC1…».
func TestCleanSnippetDecodesBase64Body(t *testing.T) {
	body := "Уважаемый клиент! Направляем Вам электронный платежный документ."
	enc := "0KPQstCw0LbQsNC10LzRi9C5INC60LvQuNC10L3RgiEg0J3QsNC/0YDQsNCy0LvRj9C10Lwg0JLQsNC8INGN0LvQtdC60YLRgNC+0L3QvdGL0Lkg0L/Qu9Cw0YLQtdC20L3Ri9C5INC00L7QutGD0LzQtdC90YIu"
	got := cleanSnippet(enc + " --boundary_42")
	if !strings.Contains(got, "Уважаемый клиент") {
		t.Errorf("base64 не расшифрован: %q", got)
	}
	if strings.Contains(got, "0KPQstCw") {
		t.Errorf("в превью осталась кодировка: %q", got)
	}
	_ = body
}

// Сущности HTML и невидимые знаки: в превью уезжало «Paygine &nbsp;
// <td style "font-family: Arial,» и «96 25% &#847;&zwnj;&nbsp;» вместо
// текста письма.
func TestSnippetEntities(t *testing.T) {
	got := cleanSnippet(`Paygine &nbsp; &nbsp; &lt;td style "font-family: Arial,&gt; Успешная оплата`)
	if strings.Contains(got, "&nbsp;") || strings.Contains(got, "<td") || strings.Contains(got, "&lt;") {
		t.Fatalf("разметка осталась: %q", got)
	}
	if !strings.Contains(got, "Успешная оплата") {
		t.Fatalf("текст письма потерялся: %q", got)
	}
	got = cleanSnippet("96 25% &#847;&zwnj;&nbsp;&#847;&zwnj; кешбэка")
	if strings.Contains(got, "&#") || strings.Contains(got, "zwnj") {
		t.Fatalf("сущности остались: %q", got)
	}
	if !strings.Contains(got, "кешбэка") {
		t.Fatalf("текст потерялся: %q", got)
	}
}

// Сущность, разорванная переносом строки письма: «Пр&#1 086;зрачность».
func TestSnippetBrokenEntity(t *testing.T) {
	got := cleanSnippet("Пр&#1 086;зрачность з&#1072;трат")
	if strings.Contains(got, "&#") {
		t.Fatalf("сущности остались: %q", got)
	}
	if !strings.Contains(got, "Прозрачность") {
		t.Fatalf("слово не склеилось: %q", got)
	}
}
