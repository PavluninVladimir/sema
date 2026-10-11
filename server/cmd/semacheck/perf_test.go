package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Замер открывает только то, что сервер назвал безопасным (`data-prefetch`),
// и просит экран режимом подогрева — тем, где сервер ничего не делает
// наружу; экран, которому без сети показать нечего (204), пропускается, а
// не меряется. Чат и «Почту» замер не трогает, даже если ссылки на них стоят
// в меню; экземпляров одного вида берёт не больше заданного (perf-pass 8).
func TestPerfOpensOnlyWhatServerAllows(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]int{}
	var wrong []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer проба" {
			http.Error(w, "нет входа", http.StatusUnauthorized)
			return
		}
		screen := r.URL.Query().Get("screen")
		mu.Lock()
		asked[screen]++
		// ни параметров кроме screen («Здоровье» запомнило бы период), ни
		// пояса (сервер принял бы его как пояс владельца), и всегда — подогрев
		if len(r.URL.Query()) != 1 || r.Header.Get("X-Sema-Prefetch") != "1" || r.Header.Get("X-Sema-TZ") != "" {
			wrong = append(wrong, r.URL.RawQuery)
		}
		mu.Unlock()
		if strings.HasPrefix(screen, "settings:agents@") {
			w.WriteHeader(http.StatusNoContent) // экран ходит к агенту — сервер отказал подогреву
			return
		}
		w.Header().Set("Server-Timing", `menu;dur=14.3;desc="19 queries", build;dur=20.5, sql;dur=9.1;desc="42 queries", total;dur=31`)
		fmt.Fprint(w, `<nav id="menu">
<button class="item" hx-get="/ui?screen=chat%3A1">Чат</button>
<button class="item" hx-get="/ui?screen=inbox&amp;filter=x">Почта</button>
<button class="item" hx-get="/ui?screen=health&amp;filter=month">Здоровье за месяц</button>
<button class="item" hx-get="/ui?screen=tasks">Задачи</button>
<a class="segment" hx-get="/ui?screen=settings%3Aservers%40a%7Eauto">Вкладка</a>
</nav>
<section id="screen" data-prefetch="health settings tasks script:">
<div class="row clickable" data-goto="settings:servers@b^f:x"></div>
<div class="row clickable" data-goto="settings:servers@c"></div>
<div class="row clickable" data-goto="settings:servers@d"></div>
<div class="row clickable" data-goto="settings:agents@мак"></div>
<div class="row clickable" data-goto="script:7"></div>
<div class="row clickable" data-goto="script:8"></div>
<div class="row clickable" data-goto="script:9"></div>
<div class="row clickable" data-goto="$file 3"></div>
<div class="row clickable" data-goto="chat:2"></div>
</section>`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	run, err := perfCrawl(perfOptions{base: srv.URL, token: "проба", repeat: 3, perKind: 2, max: 50, out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if len(wrong) > 0 {
		t.Errorf("замер слал лишнее или не подогревом: %v", wrong)
	}
	for _, never := range []string{"chat:1", "chat:2", "inbox", "$file 3"} {
		if asked[never] > 0 {
			t.Errorf("замер открыл %q — этого экрана нет в списке сервера", never)
		}
	}
	count := map[string]int{}
	for _, s := range run.Screens {
		count[s.Kind]++
		// три запроса на экран, и у семени первый — тот, которым взят список
		if asked[s.Screen] != 3 {
			t.Errorf("%s открыт %d раз, ждали 3", s.Screen, asked[s.Screen])
		}
	}
	if count["settings:servers@*"] != 2 || count["script:N"] != 2 {
		t.Errorf("предел вида не сработал: %v", count)
	}
	if asked["settings:servers@b"] == 0 {
		t.Error("метка подсветки после «^» не отрезана от адреса")
	}
	if len(run.Skipped) != 1 || run.Skipped[0] != "settings:agents@мак" {
		t.Errorf("экран с отказом подогрева не пропущен: %v", run.Skipped)
	}
	for _, s := range run.Screens {
		if strings.HasPrefix(s.Screen, "settings:agents@") {
			t.Errorf("пропущенный экран попал в замер: %+v", s)
		}
	}
	got := run.Screens[0]
	if got.Timing["menu"] != 14.3 || got.Queries["sql"] != 42 || got.Elements == 0 || got.NetKB == 0 {
		t.Errorf("Server-Timing и размер не разобраны: %+v", got)
	}
}

// Сборка без списка безопасных экранов — отказ, а не обход всего подряд:
// молчание не разрешение.
func TestPerfRefusesWithoutServerList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<section id="screen"><div data-goto="chat:1"></div></section>`)
	}))
	defer srv.Close()
	_, err := perfCrawl(perfOptions{base: srv.URL, token: "x", repeat: 2, perKind: 3, max: 10, out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "data-prefetch") {
		t.Fatalf("без списка сервера замер пошёл: %v", err)
	}
}

// Экран, ответивший ошибкой, в медианы и «было → стало» не идёт: быстрый
// отказ выглядел бы ускорением.
func TestPerfKindAndCompare(t *testing.T) {
	for addr, want := range map[string]string{
		"settings:servers@srv-vpn~auto":          "settings:servers@*~auto",
		"script:12":                              "script:N",
		"settings:models@provider:OpenAI~prices": "settings:models@*~prices",
	} {
		if got := perfKind(addr); got != want {
			t.Errorf("вид %q = %q, ждали %q", addr, got, want)
		}
	}
	was := perfRun{Build: "a", Screens: []perfScreen{{Screen: "health", Status: 200, TotalMs: 2146},
		{Screen: "tasks", Status: 200, TotalMs: 40}, {Screen: "files", Status: 200, TotalMs: 300}}}
	now := perfRun{Build: "b", Screens: []perfScreen{{Screen: "health", Status: 200, TotalMs: 69},
		{Screen: "tasks", Status: 200, TotalMs: 140}, {Screen: "files", Status: 500, TotalMs: 3}}}
	var out bytes.Buffer
	perfCompare(&out, was, now, "perf-1.json")
	text := out.String()
	for _, want := range []string{"было → стало", "общих экранов 2", "2146 →     69", "медленнее:", "tasks"} {
		if !strings.Contains(text, want) {
			t.Errorf("в сравнении нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "files") {
		t.Errorf("экран с ошибкой попал в сравнение:\n%s", text)
	}
	if !perfInsecure("https://sema.local:8443", false) || perfInsecure("https://sema.local:8443", true) ||
		perfInsecure("https://sema.example.org", false) || perfInsecure("http://127.0.0.1:8765", false) {
		t.Error("проверка сертификата выключается не там")
	}
}
