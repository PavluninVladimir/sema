package mail

import "testing"

// Живые превью с Pi: у них внутри комментарии CSS, @media с вложенными
// правилами и остатки Office-разметки. До правки в списке писем стояло
// «Ваш подарок /* /* /* These are our tablet…» вместо текста письма.
func TestPreviewWithoutMarkup(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"комментарии и @media",
			`Ваш подарок /* /* /* These are our tablet/medium screen media queries */ @media screen and (max-width: 799px) { /* Displ /* /* /* /* /* 100 /* 100pe /* 100percent width section wit`,
			"Ваш подарок"},
		{"тёмная тема Ozon",
			`@media (prefers-color-scheme: dark) { .display-td-dark-theme, [data-ogsc] .display-td-dark-theme, [ .hidden-dark-theme, ... } Ваш чек готов`,
			"Ваш чек готов"},
		{"обломок @media без скобок",
			`Выбор услуг для вашего Интернет-проекта 96 @media (prefers-colo`,
			"Выбор услуг для вашего Интернет-проекта 96"},
		{"селекторы без скобок",
			`.ExternalClass, .ExternalClass p, @med @media only s`,
			""},
		{"адрес почты не режется",
			`Письмо от info@shop.example про новинки`,
			"Письмо от info@shop.example про новинки"},
		{"незакрытый тег в конце",
			`Вадим, для вас стильные новинки <w:WordDocument "`,
			"Вадим, для вас стильные новинки"},
		{"Word",
			`= table , 96 <w:WordDocument xmlns:w "> <w:View>Normal</w:View> </w:WordDocument> Вадим, для вас стильные новинки`,
			"Вадим, для вас стильные новинки"},
	}
	for _, c := range cases {
		got := CleanSnippet(c.in)
		if got != c.want {
			t.Errorf("%s:\n  получили %q\n  ждали    %q", c.name, got, c.want)
		}
	}
}

// Незакрытое правило @media в конце превью: после чистки от него не должно
// остаться ни начала, ни хвоста — только текст письма перед ним.
func TestPreviewWithoutMediaTail(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"хвост после текста",
			`Выбор услуг для вашего Интернет-проекта 96 @media (prefers-color-scheme: dark) { .a { color: red }`,
			"Выбор услуг для вашего Интернет-проекта 96"},
		{"только правило",
			`@media screen and (max-width:360px){.Action{display:block!important}}`,
			""},
		{"правило в начале, текст после",
			`@media screen and (max-width:360px){.Action{display:block}} Здравствуйте, Вадим`,
			"Здравствуйте, Вадим"},
	}
	for _, c := range cases {
		if got := CleanSnippet(c.in); got != c.want {
			t.Errorf("%s:\n  получили %q\n  ждали    %q", c.name, got, c.want)
		}
	}
}

// Живые остатки с Pi: селекторы Outlook и скобки-обломки. Текст письма
// стоит перед ними, и он должен остаться.
func TestPreviewWithoutOutlookSelectors(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"текст и селекторы",
			`Вам назначен персональный менеджер .ExternalClass, .ExternalClass p, .ExternalClass span, .Ext`,
			"Вам назначен персональный менеджер"},
		{"штраф",
			`Уведомление о новом штрафе ГАИ .ExternalClass, .ExternalClass p, .ExternalClass span, .Ext`,
			"Уведомление о новом штрафе ГАИ"},
		{"только обломки",
			`[ [ .ExternalClass,.ExternalCla p,a,li,td,body,table,blockquote{-webkit-text-size-adjust:1`,
			""},
		{"обломки без точки",
			`ExternalClass, ExternalClas [ [ [`,
			""},
	}
	for _, c := range cases {
		if got := CleanSnippet(c.in); got != c.want {
			t.Errorf("%s:\n  получили %q\n  ждали    %q", c.name, got, c.want)
		}
	}
}
