package engine

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// newsMarkup — страница новости как у настоящего сайта: меню, статья,
// «читайте также», подвал с датой-«сегодня», ленты и JSON-LD.
func newsMarkup(ld string) string {
	article := strings.Repeat("Курс доллара к рублю вырос на фоне укрепления нефтяных котировок, а аналитики ожидают, что динамика сохранится до конца недели. ", 5)
	return `<!doctype html><html><head><meta charset="utf-8">
<title>Курс доллара вырос | Новости24</title>
<meta property="og:title" content="Курс доллара вырос">
<base href="/">
<link rel="alternate" type="application/rss+xml" title="Все новости" href="rss/all">
<link rel="ALTERNATE" type="application/atom+xml; charset=utf-8" href="https://news.example/atom/economy">
<link rel="alternate" type="application/rss+xml" href="rss/all">
<link rel="stylesheet" href="/s.css">` + ld + `
</head><body>
<header><nav><a href="/politika">Политика</a> <a href="/ekonomika">Экономика</a> <a href="/sport">Спорт</a></nav></header>
<article><h1>Курс доллара вырос</h1>
<p>` + article + `</p>
<p>` + article + `</p>
<p>Об этом сообщили в Центральном банке, отметив, что торги прошли спокойно.</p></article>
<aside><h3>Читайте также</h3><ul><li><a href="/a1">Нефть дорожает</a></li><li><a href="/a2">Ставка ЦБ</a></li></ul></aside>
<footer>© 2026 Новости24. Сегодня, 10:30. Реклама. Контакты. <a href="/about">О сайте</a></footer>
</body></html>`
}

const newsLD = `<script type="application/ld+json">{"@context":"https://schema.org","@type":"NewsArticle","headline":"Курс доллара вырос","datePublished":"2026-09-29T21:15:00+03:00","author":{"@type":"Person","name":"Иван Петров"}}</script>`

// serveMarkup — сайт, отдающий одну и ту же страницу на любом пути.
func serveMarkup(t *testing.T, markup string) string {
	t.Helper()
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(markup))
	}))
	return srv.URL
}

// Страница новости: текст без меню, «читайте также» и подвала, заголовок без
// названия сайта, дата — из JSON-LD, ленты — абсолютные. А ссылки по-прежнему
// со всей разметки: меню и подвал для читающего текст мусор, а для модуля,
// что ходит по сайту, — как раз то, что нужно.
func TestWebPageNewsArticleReadable(t *testing.T) {
	url := serveMarkup(t, newsMarkup(newsLD))
	got, err := fetchWebPage(url+"/news/1", pageMinCharsDefault)
	if err != nil {
		t.Fatal(err)
	}
	if got.Via != viaReadability {
		t.Errorf("текст достали не readability: %q", got.Via)
	}
	if !strings.Contains(got.Text, "Курс доллара к рублю вырос на фоне укрепления нефтяных котировок") ||
		!strings.Contains(got.Text, "Об этом сообщили в Центральном банке") {
		t.Fatalf("текста статьи нет: %.300q", got.Text)
	}
	for _, junk := range []string{"Политика", "Читайте также", "Нефть дорожает", "Реклама. Контакты", "© 2026", "Сегодня, 10:30"} {
		if strings.Contains(got.Text, junk) {
			t.Errorf("в тексте осталось %q: %.400q", junk, got.Text)
		}
	}
	if got.Title != "Курс доллара вырос" {
		t.Errorf("заголовок с названием сайта: %q", got.Title)
	}
	if got.Date != "2026-09-29T21:15:00+03:00" {
		t.Errorf("дата из JSON-LD: %q", got.Date)
	}
	if len(got.Data) != 1 || got.DataTotal != 1 {
		t.Fatalf("данные JSON-LD: %d из %d", len(got.Data), got.DataTotal)
	}
	if obj, _ := got.Data[0].(map[string]any); obj["@type"] != "NewsArticle" || obj["headline"] != "Курс доллара вырос" {
		t.Errorf("объект JSON-LD не как есть: %+v", got.Data[0])
	}
	// <base href="/"> — от корня сайта, повтор ленты снят, регистр rel и параметры type терпятся
	wantFeeds := []string{url + "/rss/all", "https://news.example/atom/economy"}
	if fmt.Sprint(got.Feeds) != fmt.Sprint(wantFeeds) {
		t.Errorf("ленты: %v, ждали %v", got.Feeds, wantFeeds)
	}
	links := map[string]bool{}
	for _, l := range got.Links {
		links[l.URL] = true
	}
	for _, path := range []string{"/politika", "/ekonomika", "/a1", "/about"} {
		if !links[url+path] {
			t.Errorf("ссылки страницы потеряли %s: меню и подвал должны остаться: %+v", path, got.Links)
		}
	}
}

// Карточка товара: цена лежит в JSON-LD `Product.offers`, и модулю магазина
// нужна она целиком. Даты публикации у товара нет — и «Обновлено: сегодня»
// из тела страницы ею не становится.
func TestWebPageProductDataAndNoInventedDate(t *testing.T) {
	markup := `<html><head><title>Смартфон X — купить</title>
<script type="application/ld+json">{"@context":"https://schema.org/","@type":"Product","name":"Смартфон X","sku":"X-100",
"offers":{"@type":"Offer","price":"19990","priceCurrency":"RUB","availability":"https://schema.org/InStock"}}</script>
</head><body><h1>Смартфон X</h1><p>Цена 19 990 ₽</p><div>Обновлено: сегодня в 10:30</div><time>Сегодня, 10:30</time></body></html>`
	url := serveMarkup(t, markup)
	// текста мало (меньше предела), но разметка есть — это не ошибка
	got, err := fetchWebPage(url, pageMinCharsDefault)
	if err != nil {
		t.Fatalf("карточка с JSON-LD принята за пустую оболочку: %v", err)
	}
	if len(got.Data) != 1 {
		t.Fatalf("данных: %d", len(got.Data))
	}
	product, _ := got.Data[0].(map[string]any)
	offers, _ := product["offers"].(map[string]any)
	if product["@type"] != "Product" || offers["price"] != "19990" || offers["priceCurrency"] != "RUB" {
		t.Errorf("Product.offers.price потерялась: %+v", got.Data[0])
	}
	if got.Date != "" {
		t.Errorf("дата выдумана из текста страницы: %q", got.Date)
	}
}

// Пустая оболочка — честная ошибка, а не мусор. Есть разметка или лента —
// это уже что-то, и страница отдаётся с пустым текстом без ошибки.
func TestWebPageEmptyShell(t *testing.T) {
	shell := func(head string) string {
		return `<!doctype html><html><head><title>Документация</title>` + head +
			`<script src="/bundle.js"></script></head><body><div id="app"></div><noscript>Включите JavaScript</noscript></body></html>`
	}
	// ни JSON-LD, ни фидов — ошибка со словами о скрипте в браузере
	url := serveMarkup(t, shell(""))
	_, err := fetchWebPage(url, pageMinCharsDefault)
	// сначала факты (сколько знаков, какой предел, какая настройка), потом догадка
	if err == nil || !strings.HasPrefix(err.Error(), "на странице ") ||
		!strings.Contains(err.Error(), "знаков текста при пределе 200 (настройка web.page_min_chars)") ||
		!strings.HasSuffix(err.Error(), "похоже, она собирается скриптом в браузере") {
		t.Fatalf("пустая оболочка не дала честной ошибки: %v", err)
	}
	// с лентой: без ошибки, лента отдана, текст пуст или короток
	url = serveMarkup(t, shell(`<link rel="alternate" type="application/rss+xml" href="/docs/rss.xml">`))
	got, err := fetchWebPage(url+"/docs/", pageMinCharsDefault)
	if err != nil {
		t.Fatalf("оболочка с лентой отвергнута: %v", err)
	}
	if len(got.Feeds) != 1 || got.Feeds[0] != url+"/docs/rss.xml" {
		t.Errorf("лента: %v", got.Feeds)
	}
	if n := len([]rune(got.Text)); n >= pageMinCharsDefault {
		t.Errorf("текст оболочки не пуст: %d знаков", n)
	}
	// с JSON-LD — то же
	url = serveMarkup(t, shell(`<script type="application/ld+json">{"@type":"WebSite","name":"Документация"}</script>`))
	got, err = fetchWebPage(url, pageMinCharsDefault)
	if err != nil || len(got.Data) != 1 {
		t.Fatalf("оболочка с JSON-LD: %v %+v", err, got)
	}
	// предел — настройка: ноль отключает проверку длины, но совсем пустая
	// страница всё равно ошибка
	url = serveMarkup(t, `<html><head><title>x</title></head><body><p>Короткая страница</p></body></html>`)
	if _, err := fetchWebPage(url, pageMinCharsDefault); err == nil || !strings.HasPrefix(err.Error(), "на странице 17 знаков текста при пределе 200") {
		t.Errorf("страница короче предела: %v", err)
	}
	if got, err := fetchWebPage(url, 0); err != nil || got.Text != "Короткая страница" {
		t.Errorf("без предела: %v %+v", err, got)
	}
}

// Предел «прочитанной страницы» — настройка `web.page_min_chars`, и тот же
// вызов из песочницы модуля ошибку отдаёт ошибкой инструмента.
func TestWebPageMinCharsSetting(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if e.WebPageMinChars() != pageMinCharsDefault || pageMinCharsDefault != 200 {
		t.Fatalf("по умолчанию %d", e.WebPageMinChars())
	}
	url := serveMarkup(t, `<html><body><p>Немного текста на странице, всего сотня знаков, не больше и не меньше, чем нужно для пробы предела.</p></body></html>`)
	if _, err := e.fetchPage(url); err == nil {
		t.Fatal("страница короче 200 знаков принята")
	}
	if err := st.SetSetting(pageMinCharsKey, "50"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fetchPage(url); err != nil {
		t.Fatalf("после снижения предела: %v", err)
	}
}

// JSON-LD бывает разной формы: массив, `@graph`, объект внутри объекта.
// Дата берётся у статьи, а не у чего попало; дата без времени остаётся датой.
func TestWebPageJSONLDShapes(t *testing.T) {
	long := strings.Repeat("Текст статьи о настоящем и будущем, достаточно длинный, чтобы его считали прочитанным. ", 4)
	page := func(ld string) string {
		return `<html><head><title>Статья</title>` + ld + `</head><body><article><h1>Статья</h1><p>` + long + `</p></article></body></html>`
	}
	cases := []struct {
		name, ld, date string
		objects        int
	}{
		{"graph: дата у статьи, а не у организации",
			`<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
				{"@type":"Organization","name":"Сайт","datePublished":"2001-01-01"},
				{"@type":["WebPage","ItemPage"],"@id":"#page"},
				{"@type":"BlogPosting","headline":"Статья","datePublished":"2026-09-01"}]}</script>`, "2026-09-01", 3},
		{"массив блоков",
			`<script type="application/ld+json">[{"@type":"BreadcrumbList"},{"@type":"https://schema.org/TechArticle","datePublished":"30.09.2026 09:05"}]</script>`,
			"2026-09-30T09:05:00", 2},
		{"статья внутри страницы (mainEntity)",
			`<script type="application/ld+json">{"@type":"WebPage","mainEntity":{"@type":"Article","datePublished":"2026-09-02T08:00:00Z"}}</script>`, "2026-09-02T08:00:00Z", 1},
		{"у страницы дата есть, статьи нет",
			`<script type="application/ld+json">{"@type":"WebPage","datePublished":"2026-08-15T10:00:00+03:00"}</script>`, "2026-08-15T10:00:00+03:00", 1},
		{"сломанный JSON: перевод строки внутри строки",
			"<script type=\"application/ld+json\">{\"@type\":\"NewsArticle\",\"headline\":\"Строка\nс переводом\",\"datePublished\":\"2026-09-03\"}</script>", "2026-09-03", 1},
		{"обёртка-комментарий и CDATA",
			"<script type=\"application/ld+json\">//<![CDATA[\n{\"@type\":\"Article\",\"datePublished\":\"2026-09-04\"}\n//]]></script>", "2026-09-04", 1},
		{"непонятная дата остаётся как есть",
			`<script type="application/ld+json">{"@type":"Article","datePublished":"четверг, утром"}</script>`, "четверг, утром", 1},
		{"совсем не JSON",
			`<script type="application/ld+json">это не разметка</script>`, "", 0},
		{"без разметки", ``, "", 0},
	}
	for _, c := range cases {
		url := serveMarkup(t, page(c.ld))
		got, err := fetchWebPage(url, pageMinCharsDefault)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Date != c.date {
			t.Errorf("%s: дата %q, ждали %q", c.name, got.Date, c.date)
		}
		if got.DataTotal != c.objects || len(got.Data) != c.objects {
			t.Errorf("%s: объектов %d из %d, ждали %d", c.name, len(got.Data), got.DataTotal, c.objects)
		}
	}
}

// Запасная дата — мета-поле статьи; дата в тексте («Сегодня, 10:30») не
// берётся никогда.
func TestWebPageDateFromMetaOnly(t *testing.T) {
	long := strings.Repeat("Длинный абзац про то, как принимают решения. ", 8)
	body := `<body><article><h1>Заметка</h1><p>` + long + `</p><time>Сегодня, 10:30</time></article></body>`
	cases := []struct{ name, head, want string }{
		{"article:published_time", `<meta property="article:published_time" content="2026-09-28T10:00:00Z">`, "2026-09-28T10:00:00Z"},
		{"itemprop у meta", `<meta itemprop="datePublished" content="2026-09-27">`, "2026-09-27"},
		{"пусто, когда разметка молчит", `<meta property="og:title" content="Заметка">`, ""},
	}
	for _, c := range cases {
		url := serveMarkup(t, `<html><head><title>Заметка</title>`+c.head+`</head>`+body+`</html>`)
		got, err := fetchWebPage(url, pageMinCharsDefault)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Date != c.want {
			t.Errorf("%s: дата %q, ждали %q", c.name, got.Date, c.want)
		}
	}
	// <time datetime> с itemprop — тоже разметка для машин, а голый <time> — нет
	url := serveMarkup(t, `<html><head><title>x</title></head><body><article><h1>x</h1><p>`+long+`</p>
<time itemprop="datePublished" datetime="2026-09-26T07:00:00+03:00">вчера</time></article></body></html>`)
	if got, err := fetchWebPage(url, pageMinCharsDefault); err != nil || got.Date != "2026-09-26T07:00:00+03:00" {
		t.Errorf("<time itemprop>: %v %+v", err, got)
	}
}

// Потолок данных: у магазина в разметке десятки товаров, в ответ идут первые,
// а сколько было всего — говорит DataTotal. Слишком большой объект
// пропускается целиком, а не режется посреди.
func TestWebPageDataCeiling(t *testing.T) {
	var ld strings.Builder
	for i := 0; i < pageDataMax+10; i++ {
		fmt.Fprintf(&ld, `<script type="application/ld+json">{"@type":"Product","name":"Товар %d"}</script>`, i)
	}
	// один объект больше всего бюджета
	ld.WriteString(`<script type="application/ld+json">{"@type":"ItemList","big":"` + strings.Repeat("я", pageDataBytes+10) + `"}</script>`)
	url := serveMarkup(t, `<html><head><title>Каталог</title>`+ld.String()+`</head><body><p>Каталог товаров магазина.</p></body></html>`)
	got, err := fetchWebPage(url, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != pageDataMax || got.DataTotal != pageDataMax+11 {
		t.Fatalf("объектов %d из %d, ждали %d из %d", len(got.Data), got.DataTotal, pageDataMax, pageDataMax+11)
	}
	for _, d := range got.Data {
		if obj, _ := d.(map[string]any); obj["@type"] == "ItemList" {
			t.Error("объект больше бюджета попал в ответ")
		}
	}
}

// Если readability ничего не вернула (или страница ей не по силам) — запасной
// путь по всему тексту, а не пустой ответ.
func TestWebPageFallbackWhenReadabilityFails(t *testing.T) {
	was := pageMaxNodes
	pageMaxNodes = 3 // всё, что больше трёх узлов, библиотека отвергает
	t.Cleanup(func() { pageMaxNodes = was })
	url := serveMarkup(t, `<html><head><title>Курс</title></head><body><div><p>Курс доллара сегодня девяносто, а евро — сто.</p><p>Второй абзац.</p></div></body></html>`)
	got, err := fetchWebPage(url, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Via != viaFallback || !strings.Contains(got.Text, "Курс доллара сегодня девяносто") || got.Title != "Курс" {
		t.Errorf("запасной путь: via=%q title=%q text=%q", got.Via, got.Title, got.Text)
	}
}

// Что оставить странице: удачный выбор readability не трогаем и целый текст
// даже не считаем; обрывок короче предела, когда по всей странице текста
// больше, заменяется целым; пусто — тоже целым; а если целого не больше,
// остаётся выбор библиотеки.
func TestPickPageText(t *testing.T) {
	long := strings.Repeat("слово ", 50) // 300 знаков
	asked := 0
	whole := func(s string) func() string { return func() string { asked++; return s } }
	cases := []struct {
		name, readable, all string
		min                 int
		want, via           string
		wantAsked           bool
	}{
		{"выбор readability удачный", long, "меню " + long + " подвал", 200, long, viaReadability, false},
		{"обрывок короче предела, целого больше", "сноска", long, 200, long, viaFallback, true},
		{"обрывок, а целого не больше", "сноска про подвал", "сноска", 200, "сноска про подвал", viaReadability, true},
		{"пусто", "", long, 200, long, viaFallback, true},
		{"предел 0: обрывок не проверяется", "сноска", long, 0, "сноска", viaReadability, false},
		{"предел 0, но пусто", "", long, 0, long, viaFallback, true},
	}
	for _, c := range cases {
		asked = 0
		got, via := pickPageText(c.readable, viaReadability, whole(c.all), c.min)
		if got != c.want || via != c.via || (asked > 0) != c.wantAsked {
			t.Errorf("%s: %q via %s, спрашивали целый текст %v", c.name, clip(got, 20), via, asked > 0)
		}
	}
}

// Ответ инструмента: поля читателя доходят до модуля, а описание для модели
// называет каждое из них — модель пишет код по описанию.
func TestWebPageToolExposesReaderFields(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	url := serveMarkup(t, newsMarkup(newsLD))
	m := &Module{Name: "читатель", Needs: []string{"http"}, Code: `function read() { return sema.web.page(` + strconv.Quote(url+"/n") + `); }`}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	res, err := e.RunModule(context.Background(), m.Name, "read()")
	if err != nil || res.Err != nil {
		t.Fatalf("вызов: %v %v", err, res.Err)
	}
	got := pageValue(t, res.Value)
	if got["date"] != "2026-09-29T21:15:00+03:00" || got["title"] != "Курс доллара вырос" {
		t.Errorf("дата и заголовок: %v %v", got["date"], got["title"])
	}
	if data, _ := got["data"].([]any); len(data) != 1 || got["dataTotal"].(float64) != 1 {
		t.Errorf("data: %v (всего %v)", got["data"], got["dataTotal"])
	}
	if feeds, _ := got["feeds"].([]any); len(feeds) != 2 {
		t.Errorf("feeds: %v", got["feeds"])
	}
	var desc string
	for _, tool := range e.builtinTools() {
		if tool.Name == "web.page" {
			desc = tool.Desc
		}
	}
	for _, w := range []string{"date", "data", "dataTotal", "feeds", "JSON-LD", "web.page_min_chars"} {
		if !strings.Contains(desc, w) {
			t.Errorf("в описании web.page нет %q", w)
		}
	}
}

// Страница без текста, но с разметкой, возвращается модулю; а сохранение в
// «Файлы» кладёт то, что есть: заголовок и JSON-LD вместо пустого файла.
func TestWebPageSaveKeepsMarkupWhenNoText(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	url := serveMarkup(t, `<html><head><title>Товар</title>
<script type="application/ld+json">{"@type":"Product","name":"Чайник","offers":{"price":"1500"}}</script></head><body><div id="app"></div></body></html>`)
	att, err := e.SaveWebPage(context.Background(), url)
	if err != nil || att == nil {
		t.Fatalf("сохранение: %v", err)
	}
	if !strings.Contains(att.Extracted, "Чайник") || !strings.Contains(att.Extracted, "1500") || !strings.Contains(att.Extracted, "Товар") {
		t.Errorf("в файле нет заголовка и разметки: %q", att.Extracted)
	}
	if files, _ := st.FilesByRef(store.SourceWeb, url); len(files) != 1 {
		t.Errorf("файлов: %d", len(files))
	}
	// лента без текста и без данных — сохранять нечего, и это сказано словами
	feedURL := serveMarkup(t, `<html><head><link rel="alternate" type="application/rss+xml" href="/rss"></head><body><div id="app"></div></body></html>`)
	if _, err := e.SaveWebPage(context.Background(), feedURL); err == nil || !strings.Contains(err.Error(), "сохранять нечего") || !strings.Contains(err.Error(), "/rss") {
		t.Errorf("страница из одной ленты: %v", err)
	}
}

// Срок годности копии: свежая не качается, старая качается заново; не
// изменилась — вернётся тот же файл (и отметка обновится), изменилась —
// ляжет новым файлом; ноль — копия не устаревает.
func TestSaveWebPageRefreshesStaleCopy(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	shortPagesOK(t, st)
	var mu sync.Mutex
	body, hits := "Курс на понедельник: девяносто рублей", 0
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		_, _ = w.Write([]byte(`<html><body><p>` + body + `</p></body></html>`))
	}))
	addr := srv.URL + "/rate"
	ctx := context.Background()
	getHits := func() int { mu.Lock(); defer mu.Unlock(); return hits }
	if e.ReadLinksTTLHours() != 24 {
		t.Fatalf("срок по умолчанию %d", e.ReadLinksTTLHours())
	}

	first, err := e.SaveWebPage(ctx, addr)
	if err != nil || first == nil {
		t.Fatalf("первое: %v", err)
	}
	if again, _ := e.SaveWebPage(ctx, addr); again == nil || again.ID != first.ID || getHits() != 1 {
		t.Fatalf("свежая копия скачана заново: hits=%d", getHits())
	}

	// прошли сутки, страница та же: скачали, файл прежний, отметка свежая
	if err := st.AgeFileSource(first.ID, store.SourceWeb, addr, time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	same, err := e.SaveWebPage(ctx, addr)
	if err != nil || same.ID != first.ID || getHits() != 2 {
		t.Fatalf("устаревшая копия: id=%v hits=%d err=%v", same, getHits(), err)
	}
	if _, err := e.SaveWebPage(ctx, addr); err != nil || getHits() != 2 {
		t.Fatalf("после обновления отметки страница снова качается: hits=%d", getHits())
	}

	// страница изменилась: новый файл, старый цел, «последний» — новый
	mu.Lock()
	body = "Курс на вторник: девяносто пять рублей"
	mu.Unlock()
	if err := st.AgeFileSource(first.ID, store.SourceWeb, addr, time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	second, err := e.SaveWebPage(ctx, addr)
	if err != nil || second.ID == first.ID || !strings.Contains(second.Extracted, "девяносто пять") {
		t.Fatalf("изменившаяся страница: %+v %v", second, err)
	}
	if files, _ := st.FilesByRef(store.SourceWeb, addr); len(files) != 2 {
		t.Errorf("файлов %d, ждали два (версии страницы)", len(files))
	}
	if latest, _ := e.SaveWebPage(ctx, addr); latest.ID != second.ID {
		t.Errorf("свежая копия — не последняя версия: %d", latest.ID)
	}

	// ноль — копия не устаревает: так было до настройки
	if err := st.SetSetting(readLinksTTLKey, "0"); err != nil {
		t.Fatal(err)
	}
	if err := st.AgeFileSource(second.ID, store.SourceWeb, addr, time.Now().Add(-1000*time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := getHits()
	if _, err := e.SaveWebPage(ctx, addr); err != nil || getHits() != before {
		t.Fatalf("при сроке 0 страница скачана заново: hits %d → %d", before, getHits())
	}
}
