package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// fakeSearx — SearXNG на httptest: помнит, что у него спросили и когда, и
// отвечает тем, что положили в reply. Форма ответа — с живого SearXNG на Pi.
type fakeSearx struct {
	mu     sync.Mutex
	srv    *httptest.Server
	hits   int
	seen   []url.Values
	at     []time.Time
	status int
	reply  string
}

func newFakeSearx(t *testing.T, reply string) *fakeSearx {
	t.Helper()
	f := &fakeSearx{reply: reply, status: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		f.hits++
		f.seen = append(f.seen, r.URL.Query())
		f.at = append(f.at, time.Now())
		if f.status != 200 {
			http.Error(w, "нет", f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.reply))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSearx) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

func (f *fakeSearx) last() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) == 0 {
		return nil
	}
	return f.seen[len(f.seen)-1]
}

// searxReply — ответ SearXNG: результаты `[url, title, content, engine,
// publishedDate]` и пары «не ответивших».
func searxReply(results [][5]string, unresponsive ...[2]string) string {
	var rs []map[string]any
	for _, r := range results {
		item := map[string]any{"url": r[0], "title": r[1], "content": r[2], "engine": r[3], "engines": []string{r[3]}, "category": "general"}
		if r[4] != "" {
			item["publishedDate"] = r[4]
		} else {
			item["publishedDate"] = nil
		}
		rs = append(rs, item)
	}
	un := make([][]string, 0, len(unresponsive))
	for _, u := range unresponsive {
		un = append(un, []string{u[0], u[1]})
	}
	raw, _ := json.Marshal(map[string]any{"query": "x", "number_of_results": 0, "results": rs, "answers": []any{}, "unresponsive_engines": un})
	return string(raw)
}

// findEngine — движок с настроенным SearXNG; пауза выключена, чтобы тесты не
// спали (пауза проверяется отдельно).
func findEngine(t *testing.T, f *fakeSearx) (*Engine, *store.Store) {
	t.Helper()
	e, st := newTestEngine(t, "", nil)
	if f != nil {
		if err := st.SetSetting(webFindURLKey, f.srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetSetting(webFindGapKey, "0"); err != nil {
		t.Fatal(err)
	}
	return e, st
}

func find(t *testing.T, e *Engine, consumer string, args ...any) ([]map[string]any, error) {
	t.Helper()
	got, err := e.toolWebFind(context.Background(), consumer, args)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, it := range got.([]any) {
		out = append(out, it.(map[string]any))
	}
	return out, nil
}

var twoResults = [][5]string{
	{"https://news.example/a", "Курс <b>доллара</b> вырос", "Доллар &amp; евро подорожали за неделю.\n Аналитики ждут продолжения.", "yandex", "2026-09-29T21:15:00"},
	{"https://blog.example/b", "Обзор недели", "Короткая выдержка", "duckduckgo", ""},
}

// Форма ответа: адрес, заголовок, выдержка, дата и движок; теги и сущности
// из выдержки убраны, дата приведена к ISO, а пустая дата осталась пустой.
func TestWebFindShape(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	e, _ := findEngine(t, f)
	got, err := find(t, e, "module:новости", "курс доллара")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("результатов %d", len(got))
	}
	first := got[0]
	if first["url"] != "https://news.example/a" || first["title"] != "Курс доллара вырос" ||
		first["snippet"] != "Доллар & евро подорожали за неделю. Аналитики ждут продолжения." ||
		first["date"] != "2026-09-29T21:15:00" || first["engine"] != "yandex" {
		t.Errorf("первый результат: %+v", first)
	}
	if got[1]["date"] != "" || got[1]["engine"] != "duckduckgo" {
		t.Errorf("второй результат: %+v", got[1])
	}
	q := f.last()
	if q.Get("q") != "курс доллара" || q.Get("format") != "json" || q.Get("language") != "ru" ||
		q.Get("time_range") != "" || q.Get("categories") != "" {
		t.Errorf("параметры запроса: %v", q)
	}
}

// fresh и news превращаются в параметры SearXNG; язык по умолчанию ru, lang
// его меняет; max режет, а потолок не даёт попросить больше тридцати.
func TestWebFindOptionsBecomeParams(t *testing.T) {
	var many [][5]string
	for i := 0; i < 40; i++ {
		many = append(many, [5]string{fmt.Sprintf("https://site.example/%d", i), fmt.Sprint("Заголовок ", i), "выдержка", "bing", ""})
	}
	f := newFakeSearx(t, searxReply(many))
	e, st := findEngine(t, f)
	if err := st.SetSetting(webFindCacheKey, "0"); err != nil { // кэш мешал бы читать параметры
		t.Fatal(err)
	}
	for _, fresh := range []string{"day", "week", "month", "year"} {
		if _, err := find(t, e, "module:м", "новости "+fresh, map[string]any{"fresh": fresh}); err != nil {
			t.Fatal(err)
		}
		if got := f.last().Get("time_range"); got != fresh {
			t.Errorf("fresh %q → time_range %q", fresh, got)
		}
	}
	if _, err := find(t, e, "module:м", "новости", map[string]any{"news": true, "lang": "en"}); err != nil {
		t.Fatal(err)
	}
	if q := f.last(); q.Get("categories") != "news" || q.Get("language") != "en" || q.Get("time_range") != "" {
		t.Errorf("news и lang: %v", q)
	}
	for _, c := range []struct {
		opts map[string]any
		want int
	}{
		{nil, webFindMaxDefault}, {map[string]any{"max": 3}, 3}, {map[string]any{"max": float64(25)}, 25}, {map[string]any{"max": 100}, webFindMaxCeiling},
	} {
		got, err := find(t, e, "module:м", "много", c.opts)
		if err != nil || len(got) != c.want {
			t.Errorf("max %v: %d результатов (%v), ждали %d", c.opts, len(got), err, c.want)
		}
	}
	// слова, которых SearXNG не знает, до сети не доходят
	before := f.count()
	for _, bad := range []map[string]any{{"fresh": "hour"}, {"lang": "русский язык"}, {"lang": "ru&categories=files"}} {
		if _, err := find(t, e, "module:м", "проба", bad); err == nil {
			t.Errorf("опции %v приняты", bad)
		}
	}
	if f.count() != before {
		t.Error("непонятные опции дошли до SearXNG")
	}
}

// Кэш: тот же запрос с теми же fresh, news и языком в сеть не идёт, а другой
// — идёт; ноль в настройке кэш выключает; max при отдаче режется из кэша.
func TestWebFindCache(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	e, st := findEngine(t, f)
	if e.WebFindCacheMin() != 180 {
		t.Fatalf("кэш по умолчанию %d минут", e.WebFindCacheMin())
	}
	first, err := find(t, e, "module:м", "Курс Доллара", map[string]any{"fresh": "week"})
	if err != nil || len(first) != 2 || f.count() != 1 {
		t.Fatalf("первый: %v hits=%d", err, f.count())
	}
	// регистр запроса и число результатов на ключ не влияют; потребитель — тоже
	again, err := find(t, e, "module:другой", "курс доллара", map[string]any{"fresh": "week", "max": 1})
	if err != nil || len(again) != 1 || f.count() != 1 {
		t.Fatalf("повтор: %v %d штук, hits=%d — ждали кэш", err, len(again), f.count())
	}
	for name, opts := range map[string]map[string]any{
		"другой срок": {"fresh": "day"}, "новости": {"fresh": "week", "news": true}, "язык": {"fresh": "week", "lang": "en"},
	} {
		before := f.count()
		if _, err := find(t, e, "module:м", "курс доллара", opts); err != nil || f.count() != before+1 {
			t.Errorf("%s: запрос не ушёл в сеть: hits %d → %d (%v)", name, before, f.count(), err)
		}
	}
	// выдача устарела — идём в сеть
	e.find.mu.Lock()
	for k, c := range e.find.cache {
		c.at = time.Now().Add(-4 * time.Hour)
		e.find.cache[k] = c
	}
	e.find.mu.Unlock()
	before := f.count()
	if _, err := find(t, e, "module:м", "курс доллара", map[string]any{"fresh": "week"}); err != nil || f.count() != before+1 {
		t.Errorf("устаревший кэш не обновился: hits %d → %d", before, f.count())
	}
	// кэш выключен
	if err := st.SetSetting(webFindCacheKey, "0"); err != nil {
		t.Fatal(err)
	}
	before = f.count()
	for i := 0; i < 2; i++ {
		if _, err := find(t, e, "module:м", "без кэша"); err != nil {
			t.Fatal(err)
		}
	}
	if f.count() != before+2 {
		t.Errorf("кэш выключен, а запросов было %d, ждали 2", f.count()-before)
	}
}

// Кэш не растёт без предела.
func TestWebFindCacheIsBounded(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	e, st := findEngine(t, f)
	if err := st.SetSetting(webFindDayKey, "10000"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < webFindCacheSize+30; i++ {
		if _, err := find(t, e, "module:м", fmt.Sprint("запрос номер ", i)); err != nil {
			t.Fatal(err)
		}
	}
	e.find.mu.Lock()
	n := len(e.find.cache)
	e.find.mu.Unlock()
	if n > webFindCacheSize {
		t.Errorf("в кэше %d записей, потолок %d", n, webFindCacheSize)
	}
}

// Пауза одна на весь процесс: запросы разных потребителей начинаются не
// чаще, чем раз в web.find_gap_ms.
func TestWebFindGapBetweenRequests(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	e, st := findEngine(t, f)
	if err := st.SetSetting(webFindGapKey, "250"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i, who := range []string{"module:а", "module:б", "script:7"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := find(t, e, who, fmt.Sprint("разный запрос ", i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.at) != 3 {
		t.Fatalf("запросов %d", len(f.at))
	}
	// порядок прихода — порядок слотов; допуск на планировщик
	for i := 1; i < len(f.at); i++ {
		if gap := f.at[i].Sub(f.at[i-1]); gap < 200*time.Millisecond {
			t.Errorf("между запросами %d и %d прошло %v, а пауза 250 мс", i-1, i, gap)
		}
	}
}

// По умолчанию пауза — три секунды: значение из коробки, а не число в коде
// инструмента, поэтому настройка и читается.
func TestWebFindDefaults(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if e.WebFindPerDay() != 100 || e.WebFindGapMS() != 3000 || e.WebFindCacheMin() != 180 || e.WebFindURL() != "" || e.WebFindReady() {
		t.Errorf("умолчания: %d в день, пауза %d мс, кэш %d мин, адрес %q", e.WebFindPerDay(), e.WebFindGapMS(), e.WebFindCacheMin(), e.WebFindURL())
	}
}

// Предел в день: у каждого потребителя свой, считается по журналу (значит
// переживает перезапуск), кэш его не тратит, неудача тратит.
func TestWebFindDailyLimit(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	e, st := findEngine(t, f)
	if err := st.SetSetting(webFindDayKey, "3"); err != nil {
		t.Fatal(err)
	}
	// три разных запроса — три траты; повтор из кэша не в счёт
	for i := 0; i < 3; i++ {
		if _, err := find(t, e, "module:новости", fmt.Sprint("запрос ", i)); err != nil {
			t.Fatalf("запрос %d: %v", i, err)
		}
	}
	if _, err := find(t, e, "module:новости", "запрос 0"); err != nil {
		t.Errorf("ответ из кэша упёрся в предел: %v", err)
	}
	hits := f.count()
	_, err := find(t, e, "module:новости", "четвёртый")
	if err == nil || !strings.Contains(err.Error(), "предел 3") || !strings.Contains(err.Error(), "web.find_per_day") {
		t.Fatalf("четвёртый запрос прошёл или молчит: %v", err)
	}
	if f.count() != hits {
		t.Error("отказ по пределу дошёл до SearXNG")
	}
	// другой потребитель считается отдельно
	if _, err := find(t, e, "module:другой", "запрос 0 у другого"); err != nil {
		t.Errorf("предел одного модуля закрыл другого: %v", err)
	}
	// счёт лежит в журнале, а не в памяти процесса: «перезапуск» — новый
	// движок на той же базе — его не сбрасывает
	if n := st.CountSentSince("module:новости", webFindKind, time.Now().Add(-time.Hour)); n != 3 {
		t.Errorf("в журнале %d запросов модуля, ждали 3", n)
	}
	e2, err := New(e.conf, st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := find(t, e2, "module:новости", "после перезапуска"); err == nil || !strings.Contains(err.Error(), "предел 3") {
		t.Errorf("предел сбросился вместе с процессом: %v", err)
	}
	// вчерашние запросы не считаются
	if n := st.CountSentSince("module:новости", webFindKind, time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("будущее посчиталось: %d", n)
	}
	// упавший SearXNG тоже тратит: долбящий лежащий сервер упирается в предел
	f.mu.Lock()
	f.status = 503
	f.mu.Unlock()
	for i := 0; i < 3; i++ {
		_, _ = find(t, e, "module:упавший", fmt.Sprint("сломанный ", i))
	}
	if _, err := find(t, e, "module:упавший", "четвёртый"); err == nil || !strings.Contains(err.Error(), "предел 3") {
		t.Errorf("неудачные запросы предела не тратят: %v", err)
	}
}

// Движки не ответили, а выдачи нет — это ошибка с названиями и причинами, а
// не пустой список: пустое модуль принял бы за «новостей нет». Что-то нашлось
// при отказавших движках — ответ; ничего не нашлось и все ответили — пусто.
func TestWebFindEnginesFailure(t *testing.T) {
	f := newFakeSearx(t, searxReply(nil, [2]string{"yandex", "timeout"}, [2]string{"bing", "CAPTCHA"}))
	e, st := findEngine(t, f)
	if err := st.SetSetting(webFindCacheKey, "0"); err != nil {
		t.Fatal(err)
	}
	_, err := find(t, e, "module:м", "ничего не ответило")
	if err == nil || !strings.Contains(err.Error(), "yandex (timeout)") || !strings.Contains(err.Error(), "bing (CAPTCHA)") {
		t.Fatalf("отказ движков: %v", err)
	}
	f.mu.Lock()
	f.reply = searxReply(twoResults, [2]string{"google", "CAPTCHA"})
	f.mu.Unlock()
	if got, err := find(t, e, "module:м", "часть ответила"); err != nil || len(got) != 2 {
		t.Errorf("часть движков отказала, но результаты есть: %v %d", err, len(got))
	}
	f.mu.Lock()
	f.reply = searxReply(nil)
	f.mu.Unlock()
	got, err := find(t, e, "module:м", "ничего не нашлось")
	if err != nil || len(got) != 0 {
		t.Errorf("просто пусто: %v %v", err, got)
	}
}

// SearXNG не отвечает — «поиск страниц недоступен: причина»; коды 403, 429 и
// не-JSON объяснены словами.
func TestWebFindUnavailable(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	e, st := findEngine(t, f)
	if err := st.SetSetting(webFindCacheKey, "0"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		status int
		reply  string
		want   string
	}{
		{"403 — выдача json выключена", 403, "", "json"},
		{"429 — частые запросы", 429, "", "web.find_gap_ms"},
		{"500", 500, "", "500"},
		{"не JSON", 200, "<html>это страница, а не выдача</html>", "не JSON"},
	}
	for _, c := range cases {
		f.mu.Lock()
		f.status, f.reply = c.status, c.reply
		f.mu.Unlock()
		_, err := find(t, e, "module:м", "проба "+c.name)
		if err == nil || !strings.Contains(err.Error(), "поиск страниц недоступен") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// SearXNG вообще выключен: порт закрыт
	f.srv.Close()
	_, err := find(t, e, "module:м", "порт закрыт")
	if err == nil || !strings.Contains(err.Error(), "поиск страниц недоступен") {
		t.Errorf("закрытый порт: %v", err)
	}
	// ошибка запишется в «Что ушло» как неудача
	rows, _ := st.SentList(false, "", "", 50)
	failed := 0
	for _, r := range rows {
		if r.Kind == webFindKind && !r.OK && r.Err != "" {
			failed++
		}
	}
	if failed < 5 {
		t.Errorf("неудач в журнале %d, ждали пять", failed)
	}
}

// Адрес SearXNG — только из настройки. Опции скрипта адреса не задают, а
// частная сеть, закрытая для sema.http.get, для SearXNG открыта: он там и
// живёт.
func TestWebFindAddressComesOnlyFromSettings(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	other := newFakeSearx(t, searxReply(twoResults))
	e, _ := findEngine(t, f)
	if _, err := find(t, e, "module:м", "проба", map[string]any{
		"url": other.srv.URL, "baseUrl": other.srv.URL, "endpoint": other.srv.URL, "host": other.srv.URL, "proxy": other.srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 || other.count() != 0 {
		t.Fatalf("запрос ушёл не по адресу из настройки: настроенный %d, чужой %d", f.count(), other.count())
	}
	// тот же loopback для sema.http.get закрыт охраной (клиент не подменён)
	if _, err := httpRequest("GET", f.srv.URL+"/search?q=x&format=json", "", "", nil); err == nil {
		t.Error("охрана скриптов пустила на loopback")
	}
	if f.count() != 1 {
		t.Error("охрана скриптов пропустила запрос до сервера")
	}
	// переадресацию с SearXNG не принимаем: адрес задал человек, а не сервер
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+"/search", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	e2, st2 := findEngine(t, nil)
	_ = st2.SetSetting(webFindURLKey, redirect.URL)
	if _, err := find(t, e2, "module:м", "с переадресацией"); err == nil {
		t.Error("переадресация принята")
	}
	if other.count() != 0 {
		t.Error("запрос ушёл вслед за переадресацией")
	}
}

// Адрес в настройке приводится к виду без хвоста и без логина.
func TestNormalizeWebFindURL(t *testing.T) {
	ok := map[string]string{
		"":                               "",
		"  ":                             "",
		"http://searxng:8080":            "http://searxng:8080",
		"http://searxng:8080/":           "http://searxng:8080",
		"http://searxng:8080/search":     "http://searxng:8080",
		"http://searxng:8080/search/":    "http://searxng:8080",
		"https://s.example/searx/search": "https://s.example/searx",
		"http://searxng:8080/?q=x#y":     "http://searxng:8080",
	}
	for in, want := range ok {
		if got, err := NormalizeWebFindURL(in); err != nil || got != want {
			t.Errorf("%q → %q (%v), ждали %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"searxng:8080", "ftp://searxng", "file:///etc/passwd", "http://", "http://user:pass@searxng:8080", "javascript:alert(1)"} {
		if got, err := NormalizeWebFindURL(bad); err == nil {
			t.Errorf("%q принят как %q", bad, got)
		}
	}
}

// Пока адрес не задан, модели про инструмент не говорится: ни в списке
// инструментов, ни в промпте чата; в песочнице он лежит (с правом search) и
// отвечает ошибкой «не настроен». Задали — есть везде.
func TestWebFindHiddenUntilConfigured(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	has := func() (sandbox, hint bool) {
		for _, tool := range e.ToolsFor([]string{"search"}) {
			if tool.Name == "web.find" {
				sandbox = true
			}
		}
		return sandbox, strings.Contains(e.ToolsHint(), "sema.web.find(")
	}
	if sb, hint := has(); !sb || hint {
		t.Fatalf("адрес не задан: в песочнице должен лежать (%v), в подсказке модели — нет (%v)", sb, hint)
	}
	if _, err := find(t, e, "module:м", "проба"); err == nil || !strings.Contains(err.Error(), webFindNotSet) {
		t.Errorf("вызов без адреса: %v", err)
	}
	if strings.Contains(e.chatScriptsPrompt(), "sema.web.find") {
		t.Error("адрес не задан, а промпт чата про поиск страниц говорит")
	}
	if err := st.SetSetting(webFindURLKey, "http://searxng:8080"); err != nil {
		t.Fatal(err)
	}
	if sb, hint := has(); !sb || !hint {
		t.Fatalf("адрес задан, а инструмента нет: песочница %v, подсказка %v", sb, hint)
	}
	if !strings.Contains(e.chatScriptsPrompt(), "sema.web.find") {
		t.Error("адрес задан, а промпт чата про поиск страниц молчит")
	}
	// право — то же, что у sema.search: без него в песочнице нет
	for _, tool := range e.ToolsFor([]string{"http"}) {
		if tool.Name == "web.find" {
			t.Error("web.find выдан модулю без права search")
		}
	}
	// и в реестре прав он значится под search, как sema.search
	if need := e.toolNeeds()["web.find"]; need != "search" {
		t.Errorf("право web.find: %q", need)
	}
	// описание — форма ответа и опции, по нему модель пишет код
	for _, tool := range e.builtinTools() {
		if tool.Name != "web.find" {
			continue
		}
		for _, w := range []string{"url", "title", "snippet", "date", "engine", "fresh", "news", "max", "lang", "sema.web.page", "кэш", "web.find_per_day"} {
			if !strings.Contains(tool.Desc, w) {
				t.Errorf("в описании web.find нет %q", w)
			}
		}
	}
}

// Модуль с sema.web.find: без права search не ставится; без адреса SearXNG не
// ставится с объяснением (новому модулю нечего начинать с вызова, который
// всегда отвечает «не настроен»); с обоими ставится и работает.
func TestWebFindModuleGrantAndSetup(t *testing.T) {
	code := `function look() { return sema.web.find("курс доллара", {fresh: "week", max: 2}); }`
	f := newFakeSearx(t, searxReply(twoResults))
	e, st := findEngine(t, nil) // адрес пока не задан

	m := &Module{Name: "искатель", Needs: []string{"search"}, Code: code}
	if err := e.InstallModule(m); err == nil || !strings.Contains(err.Error(), "SearXNG") {
		t.Fatalf("модуль поставлен без адреса SearXNG или отказ невнятный: %v", err)
	}
	if err := st.SetSetting(webFindURLKey, f.srv.URL); err != nil {
		t.Fatal(err)
	}
	// без права search — отказ до установки
	bare := &Module{Name: "искатель-без-права", Needs: []string{"http"}, Code: code}
	if err := e.InstallModule(bare); err == nil || !strings.Contains(err.Error(), "search") {
		t.Fatalf("модуль без права search поставлен: %v", err)
	}
	if check, err := e.CheckModuleCode(bare); err != nil || len(check.Missing) != 1 || check.Missing[0] != "search" || len(check.Unknown) != 0 {
		t.Errorf("проверка: %+v %v", check, err)
	}
	if err := e.InstallModule(m); err != nil {
		t.Fatalf("модуль с правом search и адресом: %v", err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	res, err := e.RunModule(context.Background(), m.Name, "look()")
	if err != nil || res.Err != nil {
		t.Fatalf("вызов: %v %v", err, res.Err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(res.Value), &got); err != nil || len(got) != 2 || got[0]["url"] != "https://news.example/a" {
		t.Fatalf("ответ модулю: %v %q", err, res.Value)
	}
	if f.last().Get("time_range") != "week" {
		t.Errorf("fresh не дошёл: %v", f.last())
	}
}

// Модуль стоит и включён, адрес очистили, задача модуля зовёт sema.web.find:
// ошибка инструмента «поиск страниц не настроен», заход пропущен, а модуль
// остаётся включённым и не помечен сломанным («is not a function» выключил бы
// его как сломанный код). Вернули адрес — то же самое работает.
func TestWebFindClearedAddressKeepsModuleEnabled(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	e, st := findEngine(t, f)
	m := &Module{Name: "ежедневный-поиск", Needs: []string{"search"}, Code: `function look() { return sema.web.find("новости недели", {fresh: "week"}); }`}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(webFindURLKey, ""); err != nil { // адрес очистили
		t.Fatal(err)
	}
	for _, who := range []string{"задача «Новости недели»", "вызов"} {
		res, err := e.RunModuleFor(context.Background(), m.Name, "look()", who)
		if err != nil {
			t.Fatal(err)
		}
		if res.Err == nil || !res.ToolErr || !strings.Contains(res.Err.Error()+res.ErrText, webFindNotSet) {
			t.Fatalf("%s: ждали ошибку инструмента %q, вышло %+v", who, webFindNotSet, res)
		}
	}
	got, ok := e.Module(m.Name)
	if !ok || !got.Enabled || got.Broken != "" || len(got.Breaks) != 0 {
		t.Fatalf("модуль выключен или помечен сломанным из-за очищенного адреса: %+v", got)
	}
	if f.count() != 0 {
		t.Errorf("запрос ушёл без адреса: %d", f.count())
	}
	// адрес вернули — тот же модуль работает
	if err := st.SetSetting(webFindURLKey, f.srv.URL); err != nil {
		t.Fatal(err)
	}
	if res, err := e.RunModuleFor(context.Background(), m.Name, "look()", "задача «Новости недели»"); err != nil || res.Err != nil {
		t.Fatalf("после возврата адреса: %v %v", err, res.Err)
	}
}

// Ошибка поиска — ошибка инструмента, а не сломанный код: модуль остаётся
// включённым (правило платформы), и это видно на самом поиске.
func TestWebFindFailureDoesNotDisableModule(t *testing.T) {
	f := newFakeSearx(t, searxReply(nil, [2]string{"yandex", "timeout"}))
	e, _ := findEngine(t, f)
	m := &Module{Name: "неудачник", Needs: []string{"search"}, Code: `function look() { return sema.web.find("нет ответа"); }`}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	res, err := e.RunModule(context.Background(), m.Name, "look()")
	if err != nil {
		t.Fatal(err)
	}
	if res.Err == nil || !res.ToolErr || !strings.Contains(res.Err.Error()+res.ErrText, "yandex (timeout)") {
		t.Fatalf("ошибка поиска: %+v", res)
	}
	got, ok := e.Module(m.Name)
	if !ok || !got.Enabled {
		t.Errorf("модуль выключен из-за ошибки поиска: %+v", got)
	}
}

// Запрос проходит ту же охрану, что запрос Perplexity: секретное не уходит
// вовсе, у чувствительного вырезаются адреса и суммы. Что ушло — на «Что ушло»
// с потребителем и без маски-исключений.
func TestWebFindQueryGuardAndAudit(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults))
	e, st := findEngine(t, f)
	// секретное: ни запроса к SearXNG, ни записи о том, что ушло
	for _, q := range []string{"проверить паспорт серия и номер 4510 123456", "договор № 15 диагноз"} {
		if _, err := find(t, e, "module:м", q); err == nil {
			t.Errorf("секретный запрос %q принят", q)
		}
	}
	if f.count() != 0 {
		t.Fatalf("секретное дошло до SearXNG: %d запросов", f.count())
	}
	// чувствительное: адрес и сумма вырезаны до отправки
	if _, err := find(t, e, "module:почтовик", "счёт от ivan@example.com на 15000 руб курс"); err != nil {
		t.Fatal(err)
	}
	sent := f.last().Get("q")
	if strings.Contains(sent, "ivan@example.com") || strings.Contains(sent, "15000") || !strings.Contains(sent, "курс") {
		t.Errorf("в SearXNG ушло %q", sent)
	}
	rows, _ := st.SentList(false, "", "", 50)
	var row *store.SentRow
	for _, r := range rows {
		if r.Kind == webFindKind {
			row = r
		}
	}
	if row == nil {
		t.Fatal("в «Что ушло» запроса нет")
	}
	if row.Consumer != "module:почтовик" || !row.Outside || row.Untrusted || !row.OK ||
		!strings.Contains(row.Prompt, "курс") || strings.Contains(row.Prompt, "ivan@example.com") ||
		!strings.Contains(row.Prompt, "вычищено личное") || !strings.Contains(row.Answer, "https://news.example/a") {
		t.Errorf("запись журнала: %+v", row)
	}
	if !strings.Contains(row.Model, f.srv.Listener.Addr().String()) {
		t.Errorf("не сказано, куда ходили: %q", row.Model)
	}
}

// «Проверить»: сколько нашлось, кто ответил, кто отказал; адрес — из формы,
// а не только сохранённый; запрос виден на «Что ушло» под core:web.find и
// предела модулей не тратит.
func TestCheckWebFind(t *testing.T) {
	f := newFakeSearx(t, searxReply(twoResults, [2]string{"google", "CAPTCHA"}))
	e, st := findEngine(t, nil)
	if _, err := e.CheckWebFind(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "не задан") {
		t.Errorf("проверка без адреса: %v", err)
	}
	line, err := e.CheckWebFind(context.Background(), f.srv.URL+"/search")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"нашлось 2", "ответили: yandex, duckduckgo", "не ответили: google (CAPTCHA)"} {
		if !strings.Contains(line, w) {
			t.Errorf("в строке проверки нет %q: %q", w, line)
		}
	}
	if e.WebFindURL() != "" {
		t.Error("проверка сохранила адрес: сохраняет только «Сохранить»")
	}
	if n := st.CountSentSince("core:"+webFindKind, webFindKind, time.Now().Add(-time.Minute)); n != 1 {
		t.Errorf("в журнале проверок %d", n)
	}
	if _, err := e.CheckWebFind(context.Background(), "ftp://x"); err == nil {
		t.Error("чужая схема принята")
	}
	f.srv.Close()
	if _, err := e.CheckWebFind(context.Background(), f.srv.URL); err == nil || !strings.Contains(err.Error(), "поиск страниц недоступен") {
		t.Errorf("закрытый порт: %v", err)
	}
	empty := newFakeSearx(t, searxReply(nil, [2]string{"bing", "timeout"}))
	if _, err := e.CheckWebFind(context.Background(), empty.srv.URL); err == nil || !strings.Contains(err.Error(), "bing (timeout)") {
		t.Errorf("движки молчат: %v", err)
	}
}
