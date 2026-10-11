package engine

// Текст страницы цен для разбора моделью.
//
// Читатель страниц ядра (`fetchWebPage`) отдаёт основной текст статьи и
// таблицы выбрасывает: на странице цен DeepSeek таблица цен — и есть вся
// страница, а readability оставляет вокруг неё две сноски. У OpenAI хуже:
// таблица тарифов приезжает не разметкой, а свойствами острова Astro
// (`<astro-island props="…">`), и в видимом тексте от сотни моделей остаётся
// три строки. Поэтому здесь свой проход по тому же дереву страницы: ячейки
// строки — через табуляцию, строки — через перевод строки, а свойства
// островов с таблицами разворачиваются в такие же строки. Скачивание — та же
// охранная дверь, что у `fetchWebPage` (`httpFetch`): частные сети и
// метаданные облака недоступны.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

// priceTextKey — предел текста страницы, который уходит модели, в знаках.
// Настройка: у Google страница в два раза длиннее, чем у OpenAI.
const (
	priceTextKey     = "prices.text_chars"
	priceTextDefault = 90000
	// пустая страница цен — почти всегда оболочка, что собирается скриптом
	priceTextMin = 300
)

func (e *Engine) priceTextLimit() int { return e.settingInt(priceTextKey, priceTextDefault) }

// priceDoc — страница цен: текст и раздел у каждой его строки. Раздел — цепочка
// заголовков от верхнего до ближайшего («Pricing › Finetuning»): по нему код
// отсекает дообучение, пакетный тариф и другое, что не про обычный вызов
// модели, — не надеясь на то, что это заметит промпт.
type priceDoc struct {
	Text     string
	Sections []string // Sections[i] — раздел строки i в Text; может быть короче или пуст
}

// sectionSep — разделитель заголовков в пути раздела.
const sectionSep = " › "

// fetchPriceText — страница цен как текст с таблицами. raw возвращается
// вместе с текстом: запасной разбор DeepSeek читает разметку. token, если он
// есть, уходит заголовком Authorization: адрес с ценами бывает закрытым.
func fetchPriceText(url, token string, limit int) (doc priceDoc, raw string, err error) {
	raw, err = fetchPriceRaw(url, token)
	if err != nil {
		return priceDoc{}, "", err
	}
	doc = priceDocument(raw)
	if n := utf8.RuneCountInString(doc.Text); n < priceTextMin {
		return priceDoc{}, raw, fmt.Errorf("на странице %d знаков текста — похоже, цены собираются скриптом в браузере", n)
	}
	if limit > 0 && utf8.RuneCountInString(doc.Text) > limit {
		doc.Text = string([]rune(doc.Text)[:limit])
		if lines := strings.Count(doc.Text, "\n") + 1; len(doc.Sections) > lines {
			doc.Sections = doc.Sections[:lines]
		}
	}
	return doc, raw, nil
}

// priceHeaders — заголовки запроса к источнику цен; токен, если он задан,
// идёт как Bearer.
func priceHeaders(token string) map[string]string {
	headers := map[string]string{
		"User-Agent": "Sema/1 (личный ассистент)",
		"Accept":     "text/html,text/plain,application/json",
	}
	if t := strings.TrimSpace(token); t != "" {
		headers["Authorization"] = "Bearer " + t
	}
	return headers
}

// fetchPriceRaw — тело страницы или JSON: та же охранная дверь, что у
// `fetchWebPage`, но с токеном источника, если человек его задал.
func fetchPriceRaw(url, token string) (string, error) {
	raw, _, err := httpFetch("GET", url, "", "", priceHeaders(token))
	if err != nil {
		return "", err
	}
	if len(raw) > maxPageBytes {
		raw = raw[:maxPageBytes]
	}
	return decodePage(raw), nil
}

// skipTags — что в текст страницы не идёт вовсе.
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "svg": true, "head": true,
	"template": true, "iframe": true,
	// меню и подвал: на них цен нет, а токены они едят — у OpenAI боковое
	// меню длиннее самих цен
	"nav": true, "aside": true, "footer": true,
}

// blockTags — после них и перед ними строка кончается.
var blockTags = map[string]bool{
	"p": true, "div": true, "li": true, "ul": true, "ol": true, "section": true,
	"article": true, "table": true, "thead": true, "tbody": true, "tfoot": true,
	"caption": true, "details": true, "summary": true, "dl": true, "dt": true,
	"dd": true, "header": true, "main": true,
	"pre": true, "blockquote": true, "form": true, "figure": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"hr": true, "fieldset": true, "tr": true,
}

// priceText — текст страницы: таблицы строками с табуляцией между ячейками.
func priceText(raw string) string { return priceDocument(raw).Text }

// priceDocument — текст страницы и раздел каждой строки.
func priceDocument(raw string) priceDoc {
	doc, err := xhtml.Parse(strings.NewReader(raw))
	if err != nil {
		return priceDoc{}
	}
	w := &priceWalk{}
	w.walk(doc)
	return tidyPriceDoc(w.b.String())
}

// priceWalk — обход дерева страницы: текст и стек заголовков. Заголовок
// меняет раздел всех строк за ним, пока не придёт заголовок того же или
// более высокого уровня.
type priceWalk struct {
	b     strings.Builder
	heads []priceHead
}

type priceHead struct {
	level int
	text  string
}

// sectionMark — строка-метка в потоке: следующие строки лежат в этом разделе.
// В готовый текст она не попадает.
const sectionMark = "\x01"

func (w *priceWalk) path() string {
	parts := make([]string, len(w.heads))
	for i, h := range w.heads {
		parts[i] = h.text
	}
	return strings.Join(parts, sectionSep)
}

// plainText — текст узла одной строкой, без скриптов и меню.
func plainText(n *xhtml.Node) string {
	var b strings.Builder
	var rec func(*xhtml.Node)
	rec = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
			return
		}
		if n.Type == xhtml.ElementNode && skipTags[n.Data] {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func (w *priceWalk) walk(n *xhtml.Node) {
	b := &w.b
	switch n.Type {
	case xhtml.TextNode:
		if f := strings.Fields(n.Data); len(f) > 0 {
			// пробел на краях нужен: «Input <b>price</b>» — два слова
			if strings.TrimLeft(n.Data, " \t\r\n") != n.Data {
				b.WriteByte(' ')
			}
			b.WriteString(strings.Join(f, " "))
			if strings.TrimRight(n.Data, " \t\r\n") != n.Data {
				b.WriteByte(' ')
			}
		} else if strings.TrimSpace(n.Data) == "" && n.Data != "" {
			b.WriteByte(' ')
		}
		return
	case xhtml.ElementNode:
		tag := n.Data
		if skipTags[tag] {
			return
		}
		if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
			level := int(tag[1] - '0')
			if text := plainText(n); text != "" {
				for len(w.heads) > 0 && w.heads[len(w.heads)-1].level >= level {
					w.heads = w.heads[:len(w.heads)-1]
				}
				w.heads = append(w.heads, priceHead{level, text})
				b.WriteString("\n" + sectionMark + w.path() + "\n")
			}
		}
		if tag == "astro-island" {
			w.island(n)
			return
		}
		if tag == "br" {
			b.WriteByte('\n')
			return
		}
		block := blockTags[tag]
		if block {
			b.WriteByte('\n')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c)
		}
		switch {
		case tag == "td" || tag == "th":
			b.WriteByte('\t')
		case block:
			b.WriteByte('\n')
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c)
	}
}

// island — остров Astro: таблица из свойств, а следом то, что отрисовано на
// сервере (шапка и первые строки). Шапки в свойствах нет, она в отрисованном:
// берём её оттуда и ставим над строками из свойств, урезав до первой группы —
// в свойствах только короткий контекст.
func (w *priceWalk) island(n *xhtml.Node) {
	sub := &priceWalk{heads: w.heads}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sub.walk(c)
	}
	kids := sub.b.String()
	for _, a := range n.Attr {
		if a.Key == "props" {
			if t := islandText(a.Val, headerOf(kids)); t != "" {
				w.b.WriteString("\n" + t + "\n")
			}
		}
	}
	w.b.WriteString("\n" + kids + "\n")
}

// headerOf — первая шапка таблицы цен в тексте, урезанная до первой группы.
func headerOf(text string) string {
	for _, l := range strings.Split(text, "\n") {
		cells := strings.Split(l, "\t")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		for len(cells) > 0 && cells[len(cells)-1] == "" {
			cells = cells[:len(cells)-1]
		}
		if isHeaderCells(cells) {
			return strings.Join(firstGroup(cells), "\t")
		}
	}
	return ""
}

// tidyPriceDoc — строки без лишних пробелов и пустых ячеек по краям; пустые
// ячейки посередине остаются: по ним считают столбцы. Длинную прозу без денег
// выбрасываем: цену в ней не найти, а токены она ест. Метки разделов уходят из
// текста и оседают в Sections.
func tidyPriceDoc(s string) priceDoc {
	var lines []docLine
	prev, section := "", ""
	for _, l := range strings.Split(s, "\n") {
		if path, ok := strings.CutPrefix(l, sectionMark); ok {
			section = path
			continue
		}
		cells := strings.Split(l, "\t")
		for i := range cells {
			cells[i] = strings.TrimSpace(strings.ReplaceAll(cells[i], "\u00a0", " "))
		}
		l = strings.Trim(strings.Join(cells, "\t"), "\t ")
		if l == "" || l == prev {
			continue
		}
		if utf8.RuneCountInString(l) > 220 && !strings.ContainsAny(l, "$€₽") {
			continue
		}
		lines = append(lines, docLine{l, section})
		prev = l
	}
	lines = dropMenuRuns(lines)
	doc := priceDoc{Sections: make([]string, len(lines))}
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i], doc.Sections[i] = l.text, l.section
	}
	doc.Text = strings.Join(texts, "\n")
	return doc
}

// docLine — строка текста и её раздел.
type docLine struct{ text, section string }

// dropMenuRuns — длинные колонки коротких строк без денег и без таблиц: это
// боковое меню, а не цены. У OpenAI оно длиннее самих цен и стоит перед ними;
// модель читала бы шестьсот названий разделов, чтобы дойти до таблицы.
// Прогон, где заметная доля строк с цифрами, — данные, а не меню: цены
// без знака доллара («0.15», «0.075») выброшены не будут.
func dropMenuRuns(lines []docLine) []docLine {
	const minRun = 30
	isMenu := func(l string) bool {
		return utf8.RuneCountInString(l) < 60 && !strings.ContainsAny(l, "$€₽\t")
	}
	var out []docLine
	for i := 0; i < len(lines); {
		if !isMenu(lines[i].text) {
			out = append(out, lines[i])
			i++
			continue
		}
		j, digits := i, 0
		for j < len(lines) && isMenu(lines[j].text) {
			if strings.ContainsAny(lines[j].text, "0123456789") {
				digits++
			}
			j++
		}
		if j-i >= minRun && digits*5 < j-i {
			// меню: колонка целиком, кроме хвоста — заголовок таблицы
			// («Prices per 1M tokens») нередко стоит сразу за ним
			out = append(out, lines[j-3:j]...)
			i = j
			continue
		}
		out = append(out, lines[i:j]...)
		i = j
	}
	return out
}

// ---- острова Astro ----

// islandText — таблица из свойств острова. Свойства Astro лежат парами
// «вид, значение»: 0 — обычное значение, 1 — список. Остров без таблицы
// (поиск, меню) не даёт ничего. Тариф, отличный от обычного (пакетный,
// гибкий, приоритетный), не берём: цены по умолчанию — обычные, а другие
// режимы у провайдеров дешевле или дороже вдвое и отдельно называются.
func islandText(props, header string) string {
	var top map[string]any
	if json.Unmarshal([]byte(props), &top) != nil {
		return ""
	}
	dec := make(map[string]any, len(top))
	for k, v := range top {
		dec[k] = astroValue(v)
	}
	var lines []string
	if tier, _ := dec["tier"].(string); tier != "" {
		if !strings.EqualFold(tier, "standard") {
			return ""
		}
		lines = append(lines, "Тариф: "+tier)
	}
	if header != "" {
		lines = append(lines, header)
	}
	before := len(lines)
	tableLines(dec, &lines)
	if len(lines) == before {
		return ""
	}
	return strings.Join(lines, "\n")
}

func astroValue(v any) any {
	pair, ok := v.([]any)
	if !ok || len(pair) != 2 {
		return v
	}
	kind, _ := pair[0].(float64)
	switch int(kind) {
	case 0:
		if m, ok := pair[1].(map[string]any); ok {
			out := make(map[string]any, len(m))
			for k, x := range m {
				out[k] = astroValue(x)
			}
			return out
		}
		return pair[1]
	case 1:
		list, _ := pair[1].([]any)
		out := make([]any, len(list))
		for i := range list {
			out[i] = astroValue(list[i])
		}
		return out
	}
	return nil
}

// tableLines — строки таблицы из разобранных свойств: список скаляров —
// строка, список списков — таблица, объект «model + rows» — строки модели,
// каждая с именем модели впереди (у OpenAI имя объединено на несколько строк,
// а нам оно нужно у каждой).
func tableLines(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if (keys[i] == "headings") != (keys[j] == "headings") {
				return keys[i] == "headings"
			}
			return keys[i] < keys[j]
		})
		if model, ok := x["model"].(string); ok {
			if rows, ok := x["rows"].([]any); ok {
				for _, r := range rows {
					if cells, ok := r.([]any); ok {
						*out = append(*out, model+"\t"+cellsLine(cells))
					}
				}
				return
			}
		}
		for _, k := range keys {
			tableLines(x[k], out)
		}
	case []any:
		if len(x) > 0 && allScalar(x) {
			*out = append(*out, cellsLine(x))
			return
		}
		for _, e := range x {
			tableLines(e, out)
		}
	}
}

func allScalar(list []any) bool {
	for _, e := range list {
		switch e.(type) {
		case []any:
			return false
		case map[string]any:
			if _, html := e.(map[string]any)["__pricingHtml"]; !html {
				return false
			}
		}
	}
	return true
}

func cellsLine(cells []any) string {
	parts := make([]string, len(cells))
	for i, c := range cells {
		switch v := c.(type) {
		case string:
			parts[i] = v
		case float64:
			parts[i] = strconv.FormatFloat(v, 'f', -1, 64)
		case nil:
			parts[i] = "-"
		case map[string]any:
			// ячейка с разметкой: «o4-mini<br /><small>with data sharing</small>»
			if h, ok := v["__pricingHtml"].(string); ok {
				parts[i] = strings.Join(strings.Fields(strings.ReplaceAll(priceText("<p>"+h+"</p>"), "\n", " ")), " ")
			}
		}
	}
	return strings.Join(parts, "\t")
}
