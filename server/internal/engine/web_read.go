package engine

// Разбор страницы для sema.web.page: основной текст, дата, данные из
// разметки и ленты. Текст раньше доставала `htmlToText` регулярками, и в нём
// на живых русских страницах было 57–81 % мусора: меню, подвал, ленты
// «читайте также» (новость lenta.ru — 2579 знаков вместо 488 знаков тела).
// Теперь основной текст выбирает go-readability v2 (порт Readability.js:
// чистый Go, без cgo, +5,6 МБ к бинарнику). Trafilatura чище, но держит
// около 100 МБ постоянной памяти — на тесной Pi это цена, которую владелец
// не согласился платить. Регулярный разбор остался запасным путём: страница
// без «статьи» (список, таблица) у readability бывает пустой.
//
// Дату и данные страницы читает наш разбор, а не readability: модулю магазина
// нужна `Product.offers.price` целиком, а не выбранные библиотекой поля.
// Дату не выдумываем: нет в разметке — пусто. «Сегодня» из шапки и год из
// подвала датой публикации не считаются.

import (
	"bytes"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html"
)

const (
	pageMinCharsKey = "web.page_min_chars"
	// текст короче этого, без данных и лент, — оболочка, а не страница
	pageMinCharsDefault = 200
	// потолки для `data` в ответе: у магазина в разметке десятки товаров, и
	// вся она в песочницу не едет — `dataTotal` говорит, сколько осталось за
	// краем. Это защита памяти, а не вкус, поэтому не настройка
	pageDataMax   = 20
	pageDataBytes = 64 << 10
)

// pageMaxNodes — потолок числа узлов для readability: библиотека ходит по
// дереву несколько раз, и страница-простыня ей не по силам, а запасной путь
// справится. Переменная, а не константа, ради проверки запасного пути.
var pageMaxNodes = 200000

// WebPageMinChars — сколько знаков текста должно быть на странице, чтобы её
// считали прочитанной, когда данных и лент в ней нет. Пустая оболочка
// (`<div id="app"></div>` и скрипт) отдаёт 200 и пустой `<body>` — без этого
// предела модуль получил бы «успех» с пустым текстом и принял его за
// содержание страницы. 0 — не проверять.
func (e *Engine) WebPageMinChars() int { return e.store.SettingInt(pageMinCharsDef) }

// pageMarkup — то, что страница сообщает о себе разметкой, а не текстом.
type pageMarkup struct {
	Date      string   // когда опубликована — как назвала разметка, ISO по возможности
	Data      []any    // объекты JSON-LD как есть, с потолком
	DataTotal int      // сколько объектов было до потолка
	Feeds     []string // ленты RSS и Atom, абсолютные адреса
}

// readPageMarkup — JSON-LD, дата и ленты из дерева страницы. Дерево не
// меняет: readability потом мутирует его, и читать надо раньше.
func readPageMarkup(doc *html.Node, base *neturl.URL) pageMarkup {
	var (
		out    pageMarkup
		blocks []any
		metaAt string // запасная дата из мета-полей и микроразметки
		feeds  = map[string]bool{}
	)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script":
				if isLDType(attrOf(n, "type")) {
					blocks = append(blocks, parseLD(nodeText(n))...)
				}
			case "link":
				if base != nil && isFeedLink(n) {
					if u := resolveHref(base, attrOf(n, "href")); u != "" && !feeds[u] {
						feeds[u] = true
						out.Feeds = append(out.Feeds, u)
					}
				}
			case "meta", "time":
				if metaAt == "" {
					metaAt = metaPublished(n)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	out.DataTotal = len(blocks)
	used := 0
	for _, b := range blocks {
		if len(out.Data) >= pageDataMax {
			break
		}
		raw, err := json.Marshal(b)
		if err != nil || used+len(raw) > pageDataBytes {
			continue // не влезло целиком — режем объектами, а не посреди объекта
		}
		used += len(raw)
		out.Data = append(out.Data, b)
	}
	if d := ldPublished(blocks); d != "" {
		out.Date = normalizePageDate(d)
	} else if metaAt != "" {
		out.Date = normalizePageDate(metaAt)
	}
	return out
}

func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// nodeText — весь текст внутри узла; у <script> это один текстовый узел.
func nodeText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

func isLDType(t string) bool {
	t = strings.ToLower(strings.TrimSpace(t))
	return strings.HasPrefix(t, "application/ld+json")
}

var (
	reLDWrapper = regexp.MustCompile(`(?s)^\s*(?:<!--|//\s*<!\[CDATA\[|<!\[CDATA\[)?\s*(.*?)\s*(?:-->|//\s*\]\]>|\]\]>)?\s*$`)
)

// parseLD — объекты одного блока JSON-LD: объект, массив или `@graph`.
// Блок из живого сайта нередко не чистый JSON (комментарий-обёртка, перевод
// строки внутри строки) — читаем тем, что получилось, а не бросаем блок.
func parseLD(text string) []any {
	if m := reLDWrapper.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		// управляющие знаки внутри строк JSON запрещены, а на практике их
		// ставят; вне строк это пробелы, и замена на пробел ничего не портит
		fixed := strings.Map(func(r rune) rune {
			if r < 0x20 {
				return ' '
			}
			return r
		}, text)
		if err := json.Unmarshal([]byte(fixed), &v); err != nil {
			return nil
		}
	}
	return flattenLD(v)
}

// flattenLD — список объектов: массив раскладывается, `@graph` тоже, а
// обёртка с одним `@context` пропадает.
func flattenLD(v any) []any {
	switch x := v.(type) {
	case []any:
		var out []any
		for _, it := range x {
			out = append(out, flattenLD(it)...)
		}
		return out
	case map[string]any:
		graph, ok := x["@graph"].([]any)
		if !ok {
			return []any{x}
		}
		out := flattenLD(graph)
		rest := map[string]any{}
		for k, val := range x {
			if k != "@graph" && k != "@context" {
				rest[k] = val
			}
		}
		if len(rest) > 0 {
			out = append([]any{rest}, out...)
		}
		return out
	}
	return nil
}

// ldTypes — значения `@type`: строка или список строк, без приставки
// схемы («https://schema.org/NewsArticle» → «NewsArticle»).
func ldTypes(m map[string]any) []string {
	var raw []any
	switch t := m["@type"].(type) {
	case string:
		raw = []any{t}
	case []any:
		raw = t
	}
	var out []string
	for _, r := range raw {
		s, _ := r.(string)
		if i := strings.LastIndexAny(s, "/:#"); i >= 0 {
			s = s[i+1:]
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// isArticleType — статья и её родня: Article, NewsArticle, BlogPosting,
// TechArticle, SocialMediaPosting, Report… Дата у них — дата выхода текста.
func isArticleType(t string) bool {
	return strings.HasSuffix(t, "Article") || strings.HasSuffix(t, "Posting") || t == "Report"
}

// ldPublished — `datePublished` статьи. Сначала статья и родня, потом сама
// страница (WebPage и её виды): Yoast и подобные кладут дату в оба места, но
// у части сайтов статьи нет, а страница есть. Объекты ищутся и внутри
// других (`mainEntity`, `mainEntityOfPage`).
func ldPublished(blocks []any) string {
	var article, page string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, it := range x {
				walk(it)
			}
		case map[string]any:
			if d, _ := x["datePublished"].(string); strings.TrimSpace(d) != "" {
				for _, t := range ldTypes(x) {
					switch {
					case isArticleType(t):
						if article == "" {
							article = d
						}
					case strings.HasSuffix(t, "Page"):
						if page == "" {
							page = d
						}
					}
				}
			}
			// порядок ключей у карты случаен, а «первая» дата должна быть одной
			// и той же от запуска к запуску
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				switch x[k].(type) {
				case []any, map[string]any:
					walk(x[k])
				}
			}
		}
	}
	walk(blocks)
	if article != "" {
		return article
	}
	return page
}

// metaPublished — дата публикации из мета-поля или микроразметки: то, что
// сайт сам объявил для машин. Текст на странице («Сегодня, 10:30») сюда не
// попадает никогда.
func metaPublished(n *html.Node) string {
	if n.Data == "meta" {
		key := strings.ToLower(strings.TrimSpace(attrOf(n, "property") + " " + attrOf(n, "name")))
		item := strings.EqualFold(strings.TrimSpace(attrOf(n, "itemprop")), "datePublished")
		if item {
			return strings.TrimSpace(attrOf(n, "content"))
		}
		for _, k := range strings.Fields(key) {
			switch k {
			case "article:published_time", "og:article:published_time", "parsely-pub-date",
				"pubdate", "publishdate", "dc.date.issued", "dcterms.issued", "dcterms.created":
				return strings.TrimSpace(attrOf(n, "content"))
			}
		}
		return ""
	}
	// <time itemprop="datePublished" datetime="…">
	if strings.EqualFold(strings.TrimSpace(attrOf(n, "itemprop")), "datePublished") {
		return strings.TrimSpace(attrOf(n, "datetime"))
	}
	return ""
}

// pageDateLayouts — как сайты пишут дату в разметке. Первая колонка —
// раскладка, вторая — во что превращается: с поясом RFC 3339, без пояса
// «локальное время», только число — «2026-09-30».
var pageDateLayouts = []struct{ in, out string }{
	{time.RFC3339Nano, time.RFC3339},
	{time.RFC3339, time.RFC3339},
	{"2006-01-02T15:04:05Z0700", time.RFC3339},
	{"2006-01-02 15:04:05Z07:00", time.RFC3339},
	{"2006-01-02 15:04:05Z0700", time.RFC3339},
	{"2006-01-02T15:04:05", "2006-01-02T15:04:05"},
	{"2006-01-02T15:04", "2006-01-02T15:04:05"},
	{"2006-01-02 15:04:05", "2006-01-02T15:04:05"},
	{"2006-01-02 15:04", "2006-01-02T15:04:05"},
	{"2006-01-02", "2006-01-02"},
	{time.RFC1123Z, time.RFC3339},
	{time.RFC1123, time.RFC3339},
	{"02.01.2006 15:04:05", "2006-01-02T15:04:05"},
	{"02.01.2006 15:04", "2006-01-02T15:04:05"},
	{"02.01.2006", "2006-01-02"},
}

// normalizePageDate — дата в ISO 8601, если сайт написал её одним из
// известных способов; иначе строка как есть. Пояс не выдумывается: «без
// пояса» остаётся без пояса.
func normalizePageDate(s string) string {
	s = strings.TrimSpace(s)
	for _, l := range pageDateLayouts {
		if t, err := time.Parse(l.in, s); err == nil {
			return t.Format(l.out)
		}
	}
	return s
}

// isFeedLink — <link rel="alternate" type="application/rss+xml" href="…">.
func isFeedLink(n *html.Node) bool {
	alternate := false
	for _, r := range strings.Fields(strings.ToLower(attrOf(n, "rel"))) {
		if r == "alternate" {
			alternate = true
		}
	}
	if !alternate {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(attrOf(n, "type"), ";", 2)[0])) {
	case "application/rss+xml", "application/atom+xml", "application/feed+json":
		return true
	}
	return false
}

// resolveHref — абсолютный адрес http или https от базы страницы; иное
// (mailto, javascript, пусто) — пустая строка. Фрагмент снимается.
func resolveHref(base *neturl.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || base == nil {
		return ""
	}
	ref, err := neturl.Parse(href)
	if err != nil {
		return ""
	}
	u := base.ResolveReference(ref)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// readableText — основной текст и заголовок страницы через readability.
// Пустой текст — «не вышло»: решать, что делать дальше, вызывающему (запасной
// путь). Библиотека мутирует дерево, поэтому звать её надо после разбора
// разметки. Паника чужого разбора не должна ронять чтение: страницы бывают
// какие угодно.
func readableText(doc *html.Node, base *neturl.URL) (title, text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			title, text, err = "", "", fmt.Errorf("readability: %v", r)
		}
	}()
	parser := readability.NewParser()
	parser.MaxElemsToParse = pageMaxNodes
	art, err := parser.ParseAndMutate(doc, base)
	if err != nil {
		return "", "", err
	}
	if art.Node == nil {
		return art.Title(), "", nil
	}
	var buf bytes.Buffer
	if err := art.RenderText(&buf); err != nil {
		return art.Title(), "", err
	}
	return strings.TrimSpace(art.Title()), tidyText(buf.String()), nil
}

// tidyText — строки без хвостовых пробелов, абзацы через одну пустую.
func tidyText(s string) string {
	var lines []string
	blank := false
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		l = strings.TrimSpace(strings.ReplaceAll(l, " ", " "))
		if l == "" {
			blank = len(lines) > 0
			continue
		}
		if blank {
			lines = append(lines, "")
			blank = false
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

// parsePage — дерево страницы. Кодировку уже привели к UTF-8 (`decodePage`).
func parsePage(raw string) (*html.Node, error) {
	return html.Parse(strings.NewReader(raw))
}

// pageEmptyError — почему страница не прочитана: текста меньше предела, а
// разметка ничего взамен не дала. Начинается с фактов — сколько знаков нашли
// и какой предел, — потому что тот же ответ получает и реально короткая
// страница, а не только пустая оболочка; вывод («похоже, собирается скриптом»)
// стоит после них и назван догадкой. У страницы, что собирается скриптом в
// браузере, сервер отдаёт 200 и пустой <body>: модулю есть что делать дальше
// (лента, другой адрес, API).
func pageEmptyError(chars, min int) error {
	if chars == 0 && min == 0 {
		return fmt.Errorf("на странице нет текста")
	}
	return fmt.Errorf("на странице %d знаков текста при пределе %d (настройка %s) — похоже, она собирается скриптом в браузере",
		chars, min, pageMinCharsKey)
}
