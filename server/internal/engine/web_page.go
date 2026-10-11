package engine

// Чтение страниц по адресу: текст без разметки, заголовок и ссылки. Ходит
// в сеть ядро, а не скрипт модели: на просьбу прочитать вики модель написала
// `sema.http.get(url).slice(0, 6000)` для страницы в 430 КБ, где текст
// начинается после 58 КБ шапки и скриптов, — и получила один `<head>`.
// Два шага здесь нарочно разделены: «скачать и разобрать» всегда идёт в
// сеть и ничего не сохраняет (модуль спрашивает страницу каждый день и
// должен видеть свежий текст, а не копии в «Файлах»), «положить в файлы» —
// отдельное решение того, кто читает (чат сохраняет, модуль — по просьбе).

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	neturl "net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	// сколько знаков текста sema.web.page отдаёт по умолчанию и сколько
	// ссылок; оба числа — настройки («Настройки → Поиск → Чтение страниц»)
	pageCharsKey = "web.page_chars"
	pageLinksKey = "web.page_links"
	// потолки для опций `chars` и `links` в самом вызове: настройка — то,
	// что человек считает разумным, а потолок бережёт память песочницы
	pageCharsMax = 500000
	pageLinksMax = 1000
	// значения из коробки: реестр инструментов строится и без базы, поэтому
	// описание для модели называет их, а не читает настройку
	pageCharsDefault = 30000
	pageLinksDefault = 100
)

// WebPageChars — знаков текста страницы, которые sema.web.page отдаёт по
// умолчанию. 30000 знаков — длинная страница документации целиком, а
// простыня в 2 МБ не едет в песочницу вся: `size` и `truncated` говорят, что
// осталось за краем.
func (e *Engine) WebPageChars() int { return e.store.SettingInt(pageCharsDef) }

// WebPageLinks — сколько ссылок со страницы sema.web.page отдаёт по
// умолчанию, по порядку в документе.
func (e *Engine) WebPageLinks() int { return e.store.SettingInt(pageLinksDef) }

// settingCount — целая настройка, где 0 законен («не надо») и отличается от
// «не задана»: у `settingInt` ноль читается как отсутствие и даёт умолчание.
func (e *Engine) settingCount(key string, def int) int {
	var v int
	if _, err := fmt.Sscanf(e.store.Setting(key, ""), "%d", &v); err == nil && v >= 0 {
		return v
	}
	return def
}

// WebPage — страница, разобранная ядром.
type WebPage struct {
	URL   string    // адрес, как его просили: по нему страницу и ищут в файлах
	Title string    // заголовок статьи, а нет его — <title>, потом первый <h1>
	Text  string    // основной текст без меню и подвала; резать под свой предел — дело читающего
	Links []WebLink // все ссылки страницы, абсолютные, по порядку и без повторов
	// Date — когда опубликована, как её назвала разметка (JSON-LD, мета-поля);
	// пусто, если разметка молчит: дату с экрана не выдумываем
	Date      string
	Data      []any    // объекты JSON-LD как есть, с потолком (`pageDataMax`, `pageDataBytes`)
	DataTotal int      // сколько объектов JSON-LD было на странице
	Feeds     []string // ленты RSS и Atom, абсолютные адреса
	Via       string   // чем достали текст: readability или запасной путь
}

// WebLink — ссылка со страницы: адрес и её текст.
type WebLink struct {
	URL  string
	Text string
}

var (
	rePageComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	rePageHead    = regexp.MustCompile(`(?is)<head[\s>].*?</head\s*>`)
	rePageTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title\s*>`)
	rePageH1      = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1\s*>`)
	// href — отдельным словом: у `data-href="…"` ссылки нет, а у «a href» есть
	rePageAnchor  = regexp.MustCompile(`(?is)<a\s+(?:[^>]*?\s)?href\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)[^>]*>(.*?)</a\s*>`)
	rePageBase    = regexp.MustCompile(`(?is)<base\s+(?:[^>]*?\s)?href\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	rePageCharset = regexp.MustCompile(`(?i)charset\s*=\s*["']?\s*([\w-]+)`)
)

// Как достали текст: библиотекой или запасным регулярным разбором.
const (
	viaReadability = "readability"
	viaFallback    = "htmlToText"
)

// fetchWebPage — скачать страницу и разобрать. Всегда идёт в сеть, ничего
// не сохраняет: адрес проходит ту же охрану, что `sema.http.get` (частные
// сети и метаданные облака недоступны). minChars — предел «страница
// прочитана» (`web.page_min_chars`); 0 — не проверять.
//
// Порядок такой: перекодировка → дерево → разметка (JSON-LD, дата, ленты)
// → основной текст через readability → если не вышло, `htmlToText` по всей
// странице. Ссылки берутся со всей разметки, а не с вырезки: меню и подвал
// для человека — мусор, а для модуля, что ходит по сайту, — как раз то,
// что нужно.
func fetchWebPage(url string, minChars int) (*WebPage, error) {
	raw, final, err := httpFetch("GET", url, "", "", map[string]string{
		"User-Agent": "Sema/1 (личный ассистент)",
		"Accept":     "text/html,text/plain",
	})
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPageBytes {
		raw = raw[:maxPageBytes]
	}
	raw = decodePage(raw)
	// заголовок и ссылки — из разметки без комментариев и скриптов: в них
	// лежат «закомментированные» ссылки и строки кода, похожие на теги
	body := reScriptStyle.ReplaceAllString(rePageComment.ReplaceAllString(raw, " "), " ")
	base := pageBase(body, final)
	page := &WebPage{URL: url, Links: pageLinks(body, final)}

	if doc, err := parsePage(raw); err == nil {
		m := readPageMarkup(doc, base)
		page.Date, page.Data, page.DataTotal, page.Feeds = m.Date, m.Data, m.DataTotal, m.Feeds
		if title, text, err := readableText(doc, final); err != nil {
			log.Printf("страница %s: readability не разобрала (%v), беру запасной разбор", clip(url, 80), err)
		} else {
			// заголовок библиотека называет и тогда, когда статьи не нашла
			page.Title = title
			if strings.TrimSpace(text) != "" {
				page.Text, page.Via = text, viaReadability
			}
		}
	}
	page.Text, page.Via = pickPageText(page.Text, page.Via, func() string {
		// без <head>: заголовок назван отдельно, а как «текст» он превращал
		// пустую оболочку в страницу из одного слова
		return strings.TrimSpace(htmlToText(rePageHead.ReplaceAllString(raw, " ")))
	}, minChars)
	if page.Title == "" {
		page.Title = pageTitle(body)
	}

	// у страницы без текста, данных и лент читать нечего; если же разметка
	// что-то дала (карточка товара, лента новостей), пустой текст — не ошибка
	if len(page.Data) == 0 && len(page.Feeds) == 0 {
		n := utf8.RuneCountInString(page.Text)
		if n == 0 || n < minChars {
			return nil, pageEmptyError(n, minChars)
		}
	}
	return page, nil
}

// pickPageText — какой текст оставить странице. readability не вышла (пусто)
// или взяла обрывок короче предела «прочитанной страницы» — у страниц-списков
// она нередко выбирает один блок из подвала: смотрим, не больше ли текста по
// всей странице. Больше — берём его: список заголовков лучше сноски, а
// страница, где текст есть, не должна называться пустой оболочкой из-за
// неудачного выбора библиотеки. Целый текст считается лениво: у нормальной
// статьи он не нужен. Предел 0 — обрывок не проверяется, работает одно
// «пусто».
func pickPageText(readable, via string, whole func() string, minChars int) (string, string) {
	n := utf8.RuneCountInString(readable)
	if n > 0 && (minChars <= 0 || n >= minChars) {
		return readable, via
	}
	all := whole()
	if n == 0 || utf8.RuneCountInString(all) > n {
		return all, viaFallback
	}
	return readable, via
}

// fetchPage — то же с пределом из настройки.
func (e *Engine) fetchPage(url string) (*WebPage, error) {
	return fetchWebPage(url, e.WebPageMinChars())
}

// decodePage — страница не в UTF-8 (старые русские сайты до сих пор в
// windows-1251) читается по метке кодировки в её первых килобайтах. Метки
// нет или она незнакомая — как есть: гадать, чем кончится, хуже, чем
// показать мусор, который человек узнает сразу.
func decodePage(raw string) string {
	if utf8.ValidString(raw) {
		return raw
	}
	head := raw
	if len(head) > 4096 {
		head = head[:4096]
	}
	m := rePageCharset.FindStringSubmatch(head)
	if m == nil {
		return raw
	}
	var cm *charmap.Charmap
	switch strings.ToLower(m[1]) {
	case "windows-1251", "cp1251", "win-1251", "x-cp1251":
		cm = charmap.Windows1251
	case "koi8-r", "koi8r":
		cm = charmap.KOI8R
	case "iso-8859-1", "latin1", "windows-1252":
		cm = charmap.Windows1252
	default:
		return raw
	}
	out, err := cm.NewDecoder().String(raw)
	if err != nil {
		return raw
	}
	return out
}

// pageText — кусок разметки одной строкой текста.
func pageText(markup string) string {
	return strings.Join(strings.Fields(html.UnescapeString(reTag.ReplaceAllString(markup, " "))), " ")
}

// pageTitle — заголовок страницы: <title>, а без него первый <h1>.
func pageTitle(body string) string {
	for _, re := range []*regexp.Regexp{rePageTitle, rePageH1} {
		if m := re.FindStringSubmatch(body); m != nil {
			if t := pageText(m[1]); t != "" {
				return clip(t, 300)
			}
		}
	}
	return ""
}

// pageBase — от какого адреса считаются относительные ссылки: от того, на
// котором остановились после перенаправлений, или от <base href>, если он
// есть.
func pageBase(body string, final *neturl.URL) *neturl.URL {
	if final == nil {
		return nil
	}
	if m := rePageBase.FindStringSubmatch(body); m != nil {
		if ref, err := neturl.Parse(html.UnescapeString(strings.Trim(m[1], `"'`))); err == nil {
			return final.ResolveReference(ref)
		}
	}
	return final
}

// pageLinks — ссылки страницы с абсолютными адресами. Считаются от адреса,
// на котором остановились после перенаправлений, или от <base href>, если
// он есть. Пропускаются якоря на саму страницу, mailto:, tel:, javascript: —
// всё, что не http и не https; фрагмент `#…` снимается, чтобы «статья» и
// «статья#комментарии» не были двумя ссылками. Повтор адреса оставляет
// первую позицию, а пустой текст (ссылка-картинка) заменяется первым
// непустым.
func pageLinks(body string, final *neturl.URL) []WebLink {
	if final == nil {
		return nil
	}
	base := pageBase(body, final)
	var out []WebLink
	at := map[string]int{}
	for _, m := range rePageAnchor.FindAllStringSubmatch(body, -1) {
		href := strings.TrimSpace(html.UnescapeString(strings.Trim(m[1], `"'`)))
		if href == "" || strings.HasPrefix(href, "#") {
			continue
		}
		ref, err := neturl.Parse(href)
		if err != nil {
			continue
		}
		u := base.ResolveReference(ref)
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		u.Fragment = ""
		addr := u.String()
		text := clip(pageText(m[2]), 200)
		if i, seen := at[addr]; seen {
			if out[i].Text == "" {
				out[i].Text = text
			}
			continue
		}
		at[addr] = len(out)
		out = append(out, WebLink{URL: addr, Text: text})
	}
	return out
}

// pageFileText — то, что ложится в файл: заголовок, дата и текст. Заголовок
// нужен потому, что readability выбрасывает из тела заголовок статьи (он
// уже назван отдельно), а файл потом ищется и читается без ответа с полями.
// Текста нет или его меньше предела «прочитанной страницы», а данные разметки
// есть (карточка товара) — в файл идёт и она: чат читает такую страницу по
// ссылке, и пустой файл ему ничего не даст.
func pageFileText(p *WebPage, minChars int) string {
	var b strings.Builder
	if p.Title != "" && !strings.HasPrefix(p.Text, p.Title) {
		b.WriteString(p.Title + "\n")
	}
	if p.Date != "" {
		b.WriteString("Опубликовано: " + p.Date + "\n")
	}
	if b.Len() > 0 && p.Text != "" {
		b.WriteString("\n")
	}
	b.WriteString(p.Text)
	if n := utf8.RuneCountInString(p.Text); len(p.Data) > 0 && (n == 0 || n < minChars) {
		if raw, err := json.MarshalIndent(p.Data, "", " "); err == nil {
			b.WriteString("\nРазметка страницы (JSON-LD):\n" + string(raw))
		}
	}
	if p.Text == "" && len(p.Data) == 0 {
		return ""
	}
	return b.String()
}

// storeWebPage — положить страницу в файлы Сёмы с источником `web`. Кладётся
// весь текст, а не то, что уйдёт читающему под его предел: файл потом ищется
// и открывается целиком. То же содержимое второй раз не сохраняется —
// дедупликация по хэшу в AddFile, но источник при этом отмечается заново:
// по времени последней отметки решается, не пора ли скачать страницу опять
// (`SaveWebPage`, `chats.read_links_ttl_h`).
func (e *Engine) storeWebPage(ctx context.Context, p *WebPage) (*store.Attachment, error) {
	text := pageFileText(p, e.WebPageMinChars())
	if text == "" {
		if len(p.Feeds) > 0 {
			return nil, fmt.Errorf("на странице нет текста, сохранять нечего (есть ленты: %s)", strings.Join(p.Feeds, ", "))
		}
		return nil, fmt.Errorf("на странице нет текста, сохранять нечего")
	}
	att, _, err := e.AddFile(ctx, pageFileName(p.URL), "text/plain", []byte(text), FileSource{Kind: store.SourceWeb, Ref: p.URL}, nil)
	if err == nil {
		_ = e.store.TouchFileSource(att.ID, store.SourceWeb, p.URL)
	}
	return att, err
}

// consumerMay — есть ли у того, кто зовёт инструмент, право. Модуль — по
// своему манифесту; скрипты чата и задач права не делят: что лежит в их
// песочнице, то им и открыто (так же работает `files.add`).
func (e *Engine) consumerMay(consumer, need string) bool {
	name, isModule := strings.CutPrefix(consumer, "module:")
	if !isModule {
		return true
	}
	m, ok := e.Module(name)
	if !ok {
		return false
	}
	for _, n := range m.Needs {
		if n == need {
			return true
		}
	}
	return false
}

// toolWebPage — `sema.web.page(url, {save, chars, links})`. Страница
// всегда скачивается заново; в «Файлы» она ложится только по `save: true`,
// и тогда модулю нужно ещё и право files — иначе сеть давала бы обход.
func (e *Engine) toolWebPage(ctx context.Context, consumer string, a []any) (any, error) {
	url := strings.TrimSpace(script.Str(a, 0))
	if url == "" {
		return nil, fmt.Errorf("web.page: нужен адрес страницы")
	}
	opts := script.Opts(a, 1)
	save, _ := opts["save"].(bool)
	if save && !e.consumerMay(consumer, "files") {
		return nil, fmt.Errorf("web.page: {save: true} кладёт страницу в «Файлы» — модулю нужно право files")
	}
	chars, links := e.WebPageChars(), e.WebPageLinks()
	if _, ok := opts["chars"]; ok {
		if n := intOpt(opts, "chars", chars); n > 0 {
			chars = n
		}
	}
	if _, ok := opts["links"]; ok {
		if n := intOpt(opts, "links", links); n >= 0 {
			links = n
		}
	}
	chars, links = min(chars, pageCharsMax), min(links, pageLinksMax)

	page, err := e.fetchPage(url)
	if err != nil {
		return nil, fmt.Errorf("web.page: %w", err)
	}
	var fileID int64
	if save {
		att, err := e.storeWebPage(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("web.page: страница прочитана, но в файлы не легла: %w", err)
		}
		fileID = att.ID
	}
	size := utf8.RuneCountInString(page.Text)
	out := make([]any, 0, min(links, len(page.Links)))
	for i, l := range page.Links {
		if i >= links {
			break
		}
		out = append(out, map[string]any{"url": l.URL, "text": l.Text})
	}
	data := page.Data
	if data == nil {
		data = []any{}
	}
	feeds := make([]any, 0, len(page.Feeds))
	for _, f := range page.Feeds {
		feeds = append(feeds, f)
	}
	return map[string]any{
		"url": url, "title": page.Title, "date": page.Date, "text": clip(page.Text, chars),
		"size": size, "truncated": size > chars,
		"data": data, "dataTotal": page.DataTotal, "feeds": feeds,
		"links": out, "linksTotal": len(page.Links), "file": fileID,
	}, nil
}
