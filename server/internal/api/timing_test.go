package api

import (
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// serverTimingShape — заголовок из одних названий частей и чисел: ни адреса
// экрана, ни данных в нём быть не может (perf-pass 1.2).
var serverTimingShape = regexp.MustCompile(`^[a-z]+;dur=[0-9.]+(;desc="[0-9]+ queries")?(, [a-z]+;dur=[0-9.]+(;desc="[0-9]+ queries")?)*$`)

// У экрана и у действия — Server-Timing: меню, сборка, разметка, база с
// числом запросов и всего. «Здоровье» 3 октября 2026 строилось 2,1 с, и
// узнать, где именно, было нечем.
func TestServerTimingOnScreensAndActions(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	check := func(what, got string, parts ...string) {
		t.Helper()
		if !serverTimingShape.MatchString(got) {
			t.Fatalf("%s: заголовок не из одних чисел и названий: %q", what, got)
		}
		for _, p := range parts {
			if !strings.Contains(got, p+";dur=") {
				t.Errorf("%s: нет части %q: %q", what, p, got)
			}
		}
		if regexp.MustCompile(`desc="0 queries"`).MatchString(got) {
			t.Errorf("%s: экран собрался без единого запроса к базе — счёт не работает: %q", what, got)
		}
	}

	r := httptest.NewRequest("GET", "/ui?screen=health", nil)
	r.Header.Set("HX-Request", "true")
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:8765"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("экран: %d %s", w.Code, w.Body.String())
	}
	check("экран", w.Header().Get(serverTimingHeader), "menu", "build", "render", "sql", "sqlmax", "total")

	form := url.Values{"action": {"set_setting"}, "target": {"ui.poll_s"}, "value": {"5"}}
	r = httptest.NewRequest("POST", "/do", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Current-Screen", "tasks")
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:8765"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("действие: %d %s", w.Code, w.Body.String())
	}
	check("действие", w.Header().Get(serverTimingHeader), "menu", "build", "render", "sql", "total")
}

// Строка журнала про медленный запрос (perf-pass 9.4): пишется только дольше
// порога, 0 — выключено; в строке — время, экран, кто позвал и шаблон
// запроса, а значений в шаблоне нет по построению.
func TestSlowSQLLine(t *testing.T) {
	slow := store.SQLSlow{D: 63 * time.Millisecond, Query: "SELECT COUNT(*) FROM messages WHERE account = ?",
		Caller: "store.(*Store).CountMessages ← api.(*Server).connections"}
	if _, ok := slowSQLLine(slow, 0, "/ui settings"); ok {
		t.Error("порог 0 — журнал выключен, а строка пишется")
	}
	if _, ok := slowSQLLine(slow, 100*time.Millisecond, "/ui settings"); ok {
		t.Error("запрос короче порога попал в журнал")
	}
	line, ok := slowSQLLine(slow, 50*time.Millisecond, "/ui settings")
	for _, want := range []string{"63.0 мс", "/ui settings", "CountMessages ← api.(*Server).connections", "FROM messages WHERE account = ?"} {
		if !ok || !strings.Contains(line, want) {
			t.Errorf("в строке нет %q: %q", want, line)
		}
	}
}

// Цепочка называет метод хранилища, через который в запрос вошли, а не
// внутренний помощник: «UsageBy», а не «usageBy» (ревью perf-pass 9.4).
func TestSlowSQLNamesEntryMethod(t *testing.T) {
	s, _ := testServer(t)
	tr := store.TraceSQL()
	_, _ = s.eng.Store().UsageBy("consumer", time.Now().Add(-time.Hour), "")
	tr.Stop()
	if c := tr.Slowest().Caller; !strings.HasPrefix(c, "store.(*Store).UsageBy ← api.") {
		t.Errorf("цепочка вызова: %q", c)
	}
}
