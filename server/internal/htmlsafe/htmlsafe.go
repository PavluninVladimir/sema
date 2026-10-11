// Пакет htmlsafe чистит HTML из писем перед показом.
//
// 7 сентября 2026 в Сёме закрывали дыру: HTML и SVG из писем исполнялись в
// адресе Сёмы с её cookie. Вывод оттуда простой — чужой HTML показывать
// можно, но двумя стенами сразу, и ни одна из них не заменяет другую.
//
// Первая стена — эта очистка: разбираем дерево настоящим парсером (тем же,
// которым его разберёт браузер) и оставляем только разрешённое. Список
// разрешённого, а не список запрещённого: запрещённое всегда неполно, а
// письма ежедневно приносят разметку, которой мы не видели.
//
// Вторая стена — заголовки ответа у ручки, которая это отдаёт: `CSP
// sandbox`, `default-src 'none'`, `nosniff` и отдельный адрес, чтобы
// документ жил в непрозрачном источнике и до cookie Сёмы не дотянулся.
//
// Отдельно про картинки: внешняя картинка в письме — это отметка о
// прочтении. Рассылка узнаёт, что адрес живой и письмо открыли, ровно в
// тот миг, когда браузер сходит за картинкой. Поэтому по умолчанию они не
// грузятся, а человек нажимает кнопку сам.
package htmlsafe

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Result — что получилось из письма.
type Result struct {
	HTML   string // очищенная разметка
	Images int    // сколько внешних картинок не загружено
	Links  int
}

// разрешённые теги. Всё, чего здесь нет, выбрасывается — с содержимым или
// без, смотря по `сНачинкой`.
var allowedTags = map[atom.Atom]bool{
	atom.P: true, atom.Br: true, atom.Div: true, atom.Span: true, atom.A: true,
	atom.B: true, atom.Strong: true, atom.I: true, atom.Em: true, atom.U: true,
	atom.S: true, atom.Strike: true, atom.Sub: true, atom.Sup: true, atom.Mark: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Ul: true, atom.Ol: true, atom.Li: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
	atom.Blockquote: true, atom.Pre: true, atom.Code: true, atom.Kbd: true, atom.Samp: true,
	atom.Table: true, atom.Thead: true, atom.Tbody: true, atom.Tfoot: true,
	atom.Tr: true, atom.Td: true, atom.Th: true, atom.Caption: true,
	atom.Colgroup: true, atom.Col: true, atom.Img: true, atom.Hr: true,
	atom.Center: true, atom.Font: true, atom.Small: true, atom.Big: true,
	atom.Address: true, atom.Figure: true, atom.Figcaption: true, atom.Abbr: true,
	atom.Section: true, atom.Article: true, atom.Header: true, atom.Footer: true,
	atom.Main: true, atom.Time: true, atom.Wbr: true, atom.Style: true,
}

// droppedWithContent — теги, которые выбрасываются вместе с содержимым. У обычного
// неизвестного тега начинка — это текст письма, и её надо сохранить; у
// этих начинка и есть опасность.
var droppedWithContent = map[atom.Atom]bool{
	atom.Script: true, atom.Iframe: true, atom.Object: true, atom.Embed: true,
	atom.Applet: true, atom.Form: true, atom.Input: true, atom.Button: true,
	atom.Select: true, atom.Option: true, atom.Textarea: true, atom.Noscript: true,
	atom.Base: true, atom.Meta: true, atom.Link: true, atom.Title: true,
	atom.Frame: true, atom.Frameset: true, atom.Template: true, atom.Svg: true,
	atom.Math: true, atom.Head: true, atom.Audio: true, atom.Video: true,
	atom.Source: true, atom.Track: true, atom.Canvas: true, atom.Map: true, atom.Area: true,
}

// allowedAttrs — какие атрибуты остаются у тега. `on*` не
// перечислены нигде и потому не проходят по определению: список
// разрешённого сильнее любой проверки на приставку.
var allowedAttrs = map[string]bool{
	"title": true, "alt": true, "width": true, "height": true, "align": true,
	"valign": true, "colspan": true, "rowspan": true, "bgcolor": true, "color": true,
	"face": true, "size": true, "border": true, "cellpadding": true, "cellspacing": true,
	"class": true, "dir": true, "lang": true, "style": true, "span": true,
}

// linkSchemes — куда можно вести ссылке письма. Относительный адрес не
// разрешён нарочно: он разрешился бы в адрес самой Сёмы, и человек, кликнув
// в письме, попал бы к ней же, думая, что ушёл к отправителю.
var linkSchemes = map[string]bool{"http": true, "https": true, "mailto": true, "tel": true}

// Очистить разбирает письмо и собирает заново из разрешённого.
// картинки — грузить ли внешние: по умолчанию нет.
func Clean(src string, images bool) Result {
	tree, err := html.Parse(strings.NewReader(src))
	if err != nil {
		// парсер x/net/html не падает на кривой разметке почти никогда;
		// если всё же упал — показываем письмо текстом, а не сырым HTML
		return Result{HTML: html.EscapeString(src)}
	}
	r := &Result{}
	var b strings.Builder
	walk(&b, tree, r, images)
	r.HTML = b.String()
	return *r
}

func walk(b *strings.Builder, n *html.Node, r *Result, images bool) {
	switch n.Type {
	case html.TextNode:
		if n.Parent != nil && n.Parent.DataAtom == atom.Style {
			b.WriteString(cleanCSS(n.Data, images)) // стили письма нужны, но без @import и expression()
			return
		}
		b.WriteString(html.EscapeString(n.Data))
		return
	case html.CommentNode, html.DoctypeNode:
		return // комментарий из письма нам ничего не говорит, а условные комментарии IE — оружие
	case html.ElementNode:
		if droppedWithContent[n.DataAtom] {
			return
		}
		if !allowedTags[n.DataAtom] {
			// неизвестный или нежелательный тег: сам выбрасываем, детей
			// оставляем — иначе из письма пропал бы текст
			children(b, n, r, images)
			return
		}
		name := n.Data
		attrs, skip := cleanAttrs(n, r, images)
		if skip {
			children(b, n, r, images)
			return
		}
		b.WriteString("<" + name)
		for _, a := range attrs {
			b.WriteString(" " + a.Key + `="` + html.EscapeString(a.Val) + `"`)
		}
		if voidTag(n.DataAtom) {
			b.WriteString(">")
			return
		}
		b.WriteString(">")
		children(b, n, r, images)
		b.WriteString("</" + name + ">")
		return
	}
	children(b, n, r, images)
}

func children(b *strings.Builder, n *html.Node, r *Result, images bool) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(b, c, r, images)
	}
}

func voidTag(a atom.Atom) bool {
	switch a {
	case atom.Br, atom.Hr, atom.Img, atom.Col, atom.Wbr:
		return true
	}
	return false
}

// cleanAttrs — что остаётся у тега. Второе значение — «выбросить сам
// тег, оставив детей»: так уходит картинка, которую мы не грузим.
func cleanAttrs(n *html.Node, r *Result, images bool) ([]html.Attribute, bool) {
	var out []html.Attribute
	for _, a := range n.Attr {
		key := strings.ToLower(strings.TrimSpace(a.Key))
		switch {
		case key == "href" && n.DataAtom == atom.A:
			if addr, ok := cleanLink(a.Val); ok {
				r.Links++
				out = append(out,
					html.Attribute{Key: "href", Val: addr},
					// ссылка из письма открывается новой вкладкой и без
					// адреса-откуда: отправителю незачем знать, что человек
					// читает почту у Сёмы
					html.Attribute{Key: "target", Val: "_blank"},
					html.Attribute{Key: "rel", Val: "noopener noreferrer nofollow"})
			}
		case (key == "data-blocked-src" || key == "data-blocked-background") && n.DataAtom == atom.Img:
			// письмо приходит уже помеченным: `internal/mail` при сохранении
			// переименовывает сетевой `src`, чтобы браузер за картинкой не
			// пошёл. Возвращаем его на место только по кнопке человека
			r.Images++
			if images {
				out = append(out, html.Attribute{Key: "src", Val: strings.TrimSpace(a.Val)})
			}
		case key == "src" && n.DataAtom == atom.Img:
			scheme, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(a.Val)), ":")
			switch {
			case scheme == "data":
				out = append(out, html.Attribute{Key: "src", Val: a.Val})
			case (scheme == "http" || scheme == "https") && images:
				out = append(out, html.Attribute{Key: "src", Val: strings.TrimSpace(a.Val)})
			default:
				// внешняя картинка при выключенном показе: считаем и
				// оставляем `alt` — часто в нём и есть смысл
				r.Images++
			}
		case key == "style":
			if v := cleanStyle(a.Val); v != "" {
				if images {
					v = strings.ReplaceAll(strings.ReplaceAll(v, "x-blocked-https:", "https:"), "x-blocked-http:", "http:")
				}
				out = append(out, html.Attribute{Key: "style", Val: v})
			}
		case allowedAttrs[key]:
			out = append(out, html.Attribute{Key: key, Val: a.Val})
		}
	}
	if n.DataAtom == atom.Img && !has(out, "src") {
		return nil, true // картинки нет — и тега нет; alt покажем как текст
	}
	return out, false
}

func has(list []html.Attribute, key string) bool {
	for _, a := range list {
		if a.Key == key {
			return true
		}
	}
	return false
}

// cleanLink — адрес, по которому можно дать перейти.
func cleanLink(v string) (string, bool) {
	v = strings.TrimSpace(v)
	// пробелы и управляющие внутри схемы: «java\tscript:» браузер поймёт,
	// а наивная проверка приставки — нет
	bare := strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, v)
	scheme, rest, ok := strings.Cut(bare, ":")
	if !ok {
		return "", false // относительный адрес увёл бы к самой Сёме
	}
	if strings.ContainsAny(scheme, "/?#") {
		return "", false // это не схема, а путь: «page:1» тоже не адрес
	}
	if !linkSchemes[strings.ToLower(scheme)] || rest == "" {
		return "", false
	}
	return v, true
}

// cleanStyle — свойство style. CSP уже не пустит ни скрипт, ни внешний
// адрес, но `expression()` и `behavior:` старые браузеры выполняют, а
// `position: fixed` из письма перекрыло бы страницу — если бы письмо не
// сидело в своей рамке. Убираем всё это до кучи: дешевле, чем спорить.
func cleanStyle(v string) string {
	low := strings.ToLower(v)
	for _, bad := range []string{"expression(", "javascript:", "behavior:", "-moz-binding", "position:fixed", "position: fixed", "@import"} {
		if strings.Contains(low, bad) {
			return ""
		}
	}
	return strings.TrimSpace(v)
}

// cleanCSS — содержимое `<style>`. Правила письма сохраняем: без них
// рассылка превращается в столбик слов. Убираем только то, что тянет
// чужое или исполняется.
func cleanCSS(css string, allow bool) string {
	low := strings.ToLower(css)
	for _, bad := range []string{"@import", "expression(", "javascript:", "behavior:", "-moz-binding", "</style"} {
		if strings.Contains(low, bad) {
			return ""
		}
	}
	if allow {
		css = strings.ReplaceAll(css, "x-blocked-https:", "https:")
		css = strings.ReplaceAll(css, "x-blocked-http:", "http:")
	}
	return css
}

// ---- перевод по месту ----
//
// Перевод не должен превращать письмо в простыню текста: вёрстка, таблицы,
// картинки и порядок остаются, меняются только слова. Поэтому переводим не
// «письмо», а его текстовые узлы — по одному, с сохранением места.
//
// Тексты вынимаются в том же порядке, в каком встречаются в дереве, и
// возвращаются по номеру. Если перевод пришёл не для всех — на месте
// остаётся подлинник: половина письма на чужом языке лучше, чем половина
// письма, съехавшая на чужое место.

// Lines — тексты письма, которые имеет смысл переводить. Пробелы,
// содержимое `<style>` и строки без единой буквы пропускаются: их перевод
// ничего не даст, а места в запросе к модели они занимают много.
func Lines(src string) []string {
	tree, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return nil
	}
	var out []string
	collectTexts(tree, func(s string) { out = append(out, s) })
	return out
}

// Translate — то же письмо, но с заменёнными текстами. Порядок и число
// строк те же, что вернули `Строки`; лишние и недостающие игнорируются.
func Translate(src string, translations []string, images bool) Result {
	tree, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return Clean(src, images)
	}
	i := 0
	replaceTexts(tree, translations, &i)
	var b strings.Builder
	r := &Result{}
	walk(&b, tree, r, images)
	r.HTML = b.String()
	return *r
}

func collectTexts(n *html.Node, take func(string)) {
	if n.Type == html.TextNode {
		if translatable(n) {
			take(strings.TrimSpace(n.Data))
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectTexts(c, take)
	}
}

func replaceTexts(n *html.Node, translations []string, i *int) {
	if n.Type == html.TextNode {
		if translatable(n) {
			if *i < len(translations) && strings.TrimSpace(translations[*i]) != "" {
				// пробелы вокруг сохраняем: в «<b>Скидка</b> 15%» пробел
				// перед числом принадлежит вёрстке, а не слову
				n.Data = leadSpace(n.Data) + translations[*i] + trailSpace(n.Data)
			}
			*i++
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		replaceTexts(c, translations, i)
	}
}

// translatable — текстовый узел, который стоит отдавать модели.
func translatable(n *html.Node) bool {
	if n.Parent != nil {
		switch n.Parent.DataAtom {
		case atom.Style, atom.Script, atom.Title, atom.Head:
			return false
		}
	}
	s := strings.TrimSpace(n.Data)
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false // «15 %», «—», «&nbsp;» переводить нечего
}

func leadSpace(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t\r\n"))] }
func trailSpace(s string) string {
	trimmed := strings.TrimRight(s, " \t\r\n")
	return s[len(trimmed):]
}
