package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// servePages поднимает поддельный сайт и пускает к нему сетевые инструменты:
// охрана адресов (`netguard`) не пускает на loopback, и так и должно быть в
// рабочем коде, поэтому тест подменяет клиента, а не охрану — тем же приёмом,
// что `falHTTP` у картинок. Отдельные тесты охраны идут без подмены.
func servePages(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	was := scriptHTTP
	scriptHTTP = srv.Client()
	t.Cleanup(func() { scriptHTTP = was; srv.Close() })
	return srv
}

// bigDocPage — страница как у вики из беды с чтением ссылки в чате: около 430 КБ, из
// них первые ~58 КБ — скрипты и стили в шапке, потом навигация, текст и
// длинный хвост из картинок и скриптов.
func bigDocPage(middle string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>Подключение MCP · Вики</title><style>`)
	b.WriteString(strings.Repeat(`.junk-style{color:red}`, 300))
	b.WriteString(`</style>`)
	for b.Len() < 58<<10 {
		b.WriteString(`<script>var JUNKSCRIPT = {"a":"<div>не текст</div>","b":[1,2,3]};</script>`)
	}
	b.WriteString(`</head><body><nav><a href="/wiki/">Вики</a> <a href="/wiki/mcp/tokens">Токены</a></nav><main><h1>Подключение MCP</h1><p>` + middle + `</p>`)
	b.WriteString(`<ul>` +
		`<li><a href="art1">Статья первая</a></li>` +
		`<li><a href="/wiki/art2#top">Статья <b>вторая</b></a></li>` +
		`<li><a href="art1"><img src="x.png"></a></li>` +
		`<li><a class="ext" href="https://other.example/x?a=1&amp;b=2">Чужая</a></li>` +
		`<li><a href="mailto:a@b.c">Почта</a></li>` +
		`<li><a href="javascript:void(0)">Скрипт</a></li>` +
		`<li><a href="#top">Вверх</a></li>` +
		`<li><a data-href="/nope">Не ссылка</a></li>` +
		`<!-- <a href="/commented">Закомментированная</a> -->` +
		`</ul></main><footer>`)
	for b.Len() < 430<<10 {
		b.WriteString(`<svg><path d="M0 0L10 10"/></svg><script>var FOOTJUNK = 1;</script>`)
	}
	b.WriteString(`</footer></body></html>`)
	return b.String()
}

// Страница в сотни килобайт даёт текст из середины, а не шапку: то, чего не
// вышло у `http.get(url).slice(0, 6000)`.
func TestWebPageGivesTextFromMiddle(t *testing.T) {
	page := bigDocPage("Адрес MCP: https://mcp.example/sse. Вход по токену в заголовке Authorization. " +
		"Токен выдаётся на странице «Токены» и показывается один раз: потеряли — выпустите новый, старый отзовите. " +
		"Соединение держится по SSE, при обрыве клиент переподключается сам и повторяет незавершённый запрос.")
	if len(page) < 430<<10 {
		t.Fatalf("страница вышла короче задуманного: %d байт", len(page))
	}
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	got, err := fetchWebPage(srv.URL+"/wiki/mcp/", pageMinCharsDefault)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "Адрес MCP: https://mcp.example/sse") {
		t.Fatalf("текста из середины страницы нет: %.300q", got.Text)
	}
	for _, junk := range []string{"JUNKSCRIPT", "FOOTJUNK", "junk-style", "<head>", "<script", "не текст"} {
		if strings.Contains(got.Text, junk) {
			t.Errorf("в тексте осталось %q", junk)
		}
	}
	if got.Title != "Подключение MCP · Вики" {
		t.Errorf("заголовок: %q", got.Title)
	}
	// ссылки: абсолютные, по порядку, без повторов; якоря, mailto, javascript,
	// data-href и закомментированное не считаются; фрагмент снят
	base := srv.URL
	want := []WebLink{
		{base + "/wiki/", "Вики"},
		{base + "/wiki/mcp/tokens", "Токены"},
		{base + "/wiki/mcp/art1", "Статья первая"},
		{base + "/wiki/art2", "Статья вторая"},
		{"https://other.example/x?a=1&b=2", "Чужая"},
	}
	if len(got.Links) != len(want) {
		t.Fatalf("ссылок %d, ждали %d: %+v", len(got.Links), len(want), got.Links)
	}
	for i, w := range want {
		if got.Links[i] != w {
			t.Errorf("ссылка %d: %+v, ждали %+v", i, got.Links[i], w)
		}
	}
}

// Относительные ссылки считаются от адреса, на котором остановились после
// перенаправления, а не от того, что просили.
func TestWebPageLinksFollowRedirect(t *testing.T) {
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new/section/", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`<html><body><p>Список статей</p><a href="art1">Первая</a></body></html>`))
	}))
	got, err := fetchWebPage(srv.URL+"/old", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Links) != 1 || got.Links[0].URL != srv.URL+"/new/section/art1" {
		t.Fatalf("ссылка посчитана не от конечного адреса: %+v", got.Links)
	}
	if got.URL != srv.URL+"/old" {
		t.Errorf("адрес страницы должен остаться тем, что просили: %q", got.URL)
	}
}

// Старые русские сайты до сих пор в windows-1251: без перекодировки вместо
// текста выходил бы мусор.
func TestWebPageOldEncoding(t *testing.T) {
	enc, err := charmap.Windows1251.NewEncoder().String(
		`<html><head><meta http-equiv="Content-Type" content="text/html; charset=windows-1251"><title>Курс валют</title></head>` +
			`<body><p>Доллар вырос на два рубля</p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(enc))
	}))
	got, err := fetchWebPage(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "Доллар вырос на два рубля") || got.Title != "Курс валют" {
		t.Fatalf("страница в windows-1251 не прочитана: title=%q text=%q", got.Title, got.Text)
	}
}

// Ошибка страницы — ошибка, а не пустой текст.
func TestWebPageFailures(t *testing.T) {
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gone":
			http.Error(w, "нет такой", http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(`<html><head><script>var a = 1;</script></head><body></body></html>`))
		}
	}))
	if _, err := fetchWebPage(srv.URL+"/gone", pageMinCharsDefault); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404 не дал ошибки с кодом: %v", err)
	}
	// страница без текста — не «нет текста», а причина: чаще всего она
	// собирается скриптом в браузере, и модулю надо знать, что делать дальше
	if _, err := fetchWebPage(srv.URL+"/empty", pageMinCharsDefault); err == nil || !strings.Contains(err.Error(), "на странице 0 знаков текста при пределе 200") || !strings.Contains(err.Error(), "собирается скриптом") {
		t.Errorf("страница без текста не дала ошибки: %v", err)
	}
	// без предела (0) остаётся прежнее честное «нет текста»
	if _, err := fetchWebPage(srv.URL+"/empty", 0); err == nil || !strings.Contains(err.Error(), "нет текста") {
		t.Errorf("пустая страница без предела: %v", err)
	}
}

// Охрана адресов действует и на этом пути: страница на loopback не читается,
// а до сервера дело не доходит. Здесь клиент НЕ подменён.
func TestWebPageRefusesPrivateNetwork(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = w.Write([]byte("<p>секрет внутренней сети</p>"))
	}))
	defer srv.Close()
	if _, err := fetchWebPage(srv.URL, 0); err == nil {
		t.Fatal("страница во внутренней сети прочитана")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("сервер во внутренней сети получил %d запросов", hits)
	}
}

// SaveWebPage: уже сохранённую страницу отдаёт, а не молчит `nil, nil`, и во
// второй раз в сеть не идёт.
func TestSaveWebPageReturnsSavedOne(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	shortPagesOK(t, st)
	var mu sync.Mutex
	hits := 0
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = w.Write([]byte(`<html><body><p>Текст страницы про Сёму</p></body></html>`))
	}))
	url := srv.URL + "/page"
	first, err := e.SaveWebPage(context.Background(), url)
	if err != nil || first == nil {
		t.Fatalf("первое сохранение: %v %v", first, err)
	}
	second, err := e.SaveWebPage(context.Background(), url)
	if err != nil || second == nil || second.ID != first.ID {
		t.Fatalf("повторное: %+v %v (ждали файл %d)", second, err, first.ID)
	}
	if hits != 1 {
		t.Errorf("страница скачана %d раз, ждали один", hits)
	}
	if !strings.Contains(second.Extracted, "Текст страницы про Сёму") {
		t.Errorf("у возвращённого файла нет текста: %q", second.Extracted)
	}
	if files, _ := st.FilesByRef(store.SourceWeb, url); len(files) != 1 {
		t.Errorf("файлов с источником web: %d", len(files))
	}
}

// shortPagesOK — страницы в тестах из одной строки, и предел «прочитанной
// страницы» (`web.page_min_chars`) им не нужен: тест про другое.
func shortPagesOK(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.SetSetting(pageMinCharsKey, "0"); err != nil {
		t.Fatal(err)
	}
}

// pageValue — что вернул модуль как JSON-объект ответа sema.web.page.
func pageValue(t *testing.T, raw string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("ответ модуля не JSON: %v: %.200q", err, raw)
	}
	return v
}

// Модуль читает одну и ту же страницу каждый день и должен видеть свежий
// текст; в «Файлах» при этом ничего не копится, пока модуль не попросит.
func TestWebPageToolFreshAndNothingSavedByDefault(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	shortPagesOK(t, st)
	var mu sync.Mutex
	body, hits := "Курс в понедельник: 90", 0
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		_, _ = w.Write([]byte(`<html><head><title>Курс</title></head><body><p>` + body + `</p></body></html>`))
	}))
	url := srv.URL + "/rate"
	m := &Module{Name: "курс", Needs: []string{"http"}, Code: `function read() { return sema.web.page(` + strconv.Quote(url) + `); }`}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		t.Helper()
		res, err := e.RunModule(context.Background(), m.Name, "read()")
		if err != nil || res.Err != nil {
			t.Fatalf("вызов: %v %v", err, res.Err)
		}
		return pageValue(t, res.Value)
	}
	first := read()
	if !strings.Contains(first["text"].(string), "Курс в понедельник: 90") || first["title"] != "Курс" {
		t.Fatalf("первый вызов: %+v", first)
	}
	mu.Lock()
	body = "Курс во вторник: 95"
	mu.Unlock()
	second := read()
	if !strings.Contains(second["text"].(string), "Курс во вторник: 95") {
		t.Fatalf("второй вызов не увидел новый текст: %+v", second)
	}
	if hits != 2 {
		t.Errorf("страница скачана %d раз, ждали каждый вызов (2)", hits)
	}
	if second["file"].(float64) != 0 {
		t.Errorf("без save файл не должен появляться: file=%v", second["file"])
	}
	if files, _ := st.FilesByRef(store.SourceWeb, url); len(files) != 0 {
		t.Fatalf("в «Файлы» легло %d, а модуль не просил", len(files))
	}
	if n := st.CountFiles("", store.SourceWeb); n != 0 {
		t.Fatalf("файлов «Из интернета»: %d", n)
	}
}

// {save: true} кладёт страницу в «Файлы», но только с правом files: сеть без
// него не должна становиться способом писать в файлы.
func TestWebPageToolSaveNeedsFilesGrant(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	shortPagesOK(t, st)
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><p>Правила курса</p></body></html>`))
	}))
	url := srv.URL + "/rules"
	code := `function save() { return sema.web.page(` + strconv.Quote(url) + `, {save: true}); }`
	for _, needs := range [][]string{{"http"}, {"http", "files"}} {
		m := &Module{Name: "сохранялка-" + strconv.Itoa(len(needs)), Needs: needs, Code: code}
		if err := e.InstallModule(m); err != nil {
			t.Fatal(err)
		}
		if err := e.EnableModule(m.Name, true); err != nil {
			t.Fatal(err)
		}
		res, err := e.RunModule(context.Background(), m.Name, "save()")
		if err != nil {
			t.Fatal(err)
		}
		if len(needs) == 1 {
			if res.Err == nil || !strings.Contains(res.Err.Error()+res.ErrText, "files") {
				t.Fatalf("сохранение без права files прошло или молчит: %+v", res)
			}
			if files, _ := st.FilesByRef(store.SourceWeb, url); len(files) != 0 {
				t.Fatal("без права files страница легла в «Файлы»")
			}
			continue
		}
		if res.Err != nil {
			t.Fatalf("сохранение с правом files: %v", res.Err)
		}
		got := pageValue(t, res.Value)
		files, _ := st.FilesByRef(store.SourceWeb, url)
		if len(files) != 1 || got["file"].(float64) != float64(files[0].ID) {
			t.Fatalf("после save: файлов %d, file=%v", len(files), got["file"])
		}
		if !strings.Contains(files[0].Extracted, "Правила курса") {
			t.Errorf("текст файла: %q", files[0].Extracted)
		}
	}
}

// chars и links в вызове меняют предел; size и truncated говорят, что осталось
// за краем, а ссылки идут абсолютными адресами.
func TestWebPageToolLimits(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(bigDocPage("Первая мысль. " + strings.Repeat("Длинный абзац документации. ", 200))))
	}))
	url := srv.URL + "/wiki/mcp/"
	m := &Module{Name: "пределы", Needs: []string{"http"}, Code: `
function small() { return sema.web.page(` + strconv.Quote(url) + `, {chars: 80, links: 2}); }
function noLinks() { return sema.web.page(` + strconv.Quote(url) + `, {links: 0}); }
function plain() { return sema.web.page(` + strconv.Quote(url) + `); }`}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	call := func(fn string) map[string]any {
		res, err := e.RunModule(context.Background(), m.Name, fn+"()")
		if err != nil || res.Err != nil {
			t.Fatalf("%s: %v %v", fn, err, res.Err)
		}
		return pageValue(t, res.Value)
	}
	small := call("small")
	if n := len([]rune(small["text"].(string))); n != 80 || small["truncated"] != true || small["size"].(float64) <= 80 {
		t.Errorf("chars: длина %d, truncated=%v, size=%v", n, small["truncated"], small["size"])
	}
	if links := small["links"].([]any); len(links) != 2 || small["linksTotal"].(float64) != 5 {
		t.Errorf("links: %d штук, всего %v", len(links), small["linksTotal"])
	} else if first := links[0].(map[string]any); first["url"] != srv.URL+"/wiki/" || first["text"] != "Вики" {
		t.Errorf("первая ссылка: %+v", first)
	}
	if links := call("noLinks")["links"].([]any); len(links) != 0 {
		t.Errorf("links: 0 всё равно вернул %d", len(links))
	}
	plain := call("plain")
	if plain["truncated"] != false || len(plain["links"].([]any)) != 5 {
		t.Errorf("по умолчанию: truncated=%v, ссылок %d", plain["truncated"], len(plain["links"].([]any)))
	}
}

// Модуль без права http не может позвать sema.web.page: проверка до установки
// отказывает, а в песочницу инструмент физически не кладётся.
func TestWebPageNeedsHTTPGrant(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &Module{Name: "без-сети", Code: `function read() { return sema.web.page("https://example.org/").text; }`}
	check, err := e.CheckModuleCode(m)
	if err != nil {
		t.Fatal(err)
	}
	if problem := check.Problem(); !strings.Contains(problem, "http") {
		t.Fatalf("проверка не потребовала право http: %q", problem)
	}
	m.Needs = []string{"http"}
	if check, _ = e.CheckModuleCode(m); check.Problem() != "" {
		t.Fatalf("с правом http модуль отклонён: %q", check.Problem())
	}
	// и сама установка без права отказывает — до того, как код попадёт в базу
	m.Needs = nil
	if err := e.InstallModule(m); err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("модуль без права http установился: %v", err)
	}
	has := func(needs []string) bool {
		for _, tool := range e.ToolsFor(needs) {
			if tool.Name == "web.page" {
				return true
			}
		}
		return false
	}
	if has(nil) || !has([]string{"http"}) {
		t.Fatalf("web.page в песочнице: без прав %v, с http %v", has(nil), has([]string{"http"}))
	}
}

// Модель выбирает инструмент по описанию: у обоих должно быть сказано, чем
// они различаются, а у web.page — форма ответа.
func TestWebPageDescriptionsSteerModel(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	var page, get string
	for _, tool := range e.builtinTools() {
		switch tool.Name {
		case "web.page":
			page = tool.Desc
		case "http.get":
			get = tool.Desc
		}
	}
	for _, w := range []string{"title", "text", "links", "truncated", "file", "sema.http.get", "save"} {
		if !strings.Contains(page, w) {
			t.Errorf("в описании web.page нет %q", w)
		}
	}
	if !strings.Contains(get, "sema.web.page") {
		t.Errorf("описание http.get не отсылает к web.page: %q", get)
	}
	if !strings.Contains(e.ToolsHint(), "sema.web.page(url, {save, chars, links})") {
		t.Error("web.page не попал в подсказку модели")
	}
}
