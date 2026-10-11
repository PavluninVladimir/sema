package api

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Экраны, которые клиент подгружает заранее при наведении, при открытии ничего
// не пишут (perf-pass 7.2): отпечаток базы до и после — один и тот же. А те,
// что пишут (чат ставит «прочитано», «Почта» сдвигает окно), в список не
// попадают.
func TestPrefetchableScreensWriteNothing(t *testing.T) {
	s := healthLiveSized(t)
	st := s.eng.Store()
	c, _ := st.CreateChat("Проба")
	_ = st.AddChatMsg(c, "user", "привет")
	chat := fmt.Sprintf("chat:%d", c)
	script, _ := st.CreateScript("Проба", "return 1;")
	for _, w := range []string{"ask", "inbox", chat, "msg:1", "module:receipts:main"} {
		if prefetchable(w) {
			t.Errorf("%s пишет при открытии (или исполняет код), а помечен для предзагрузки", w)
		}
	}
	screens := append([]string{}, coreScreens...)
	screens = append(screens, "modules@mod01", "queue", notifyScreen, "digest:groups", "settings:sent",
		"memory:graph", "memory:sessions", fmt.Sprintf("script:%d", script), "health")
	checked := 0
	for _, sc := range screens {
		if !prefetchable(sc) {
			continue
		}
		before, err := st.TablesDigest()
		if err != nil {
			t.Fatal(err)
		}
		_ = listMarkup(t, s, sc)
		after, _ := st.TablesDigest()
		for k, v := range after {
			if before[k] != v {
				t.Errorf("%s помечен для предзагрузки, а при открытии пишет в %s", sc, k)
			}
		}
		checked++
	}
	if checked < 20 {
		t.Errorf("проверено %d экранов — список пуст или сломан", checked)
	}
}

// Подогрев собирается, ничего не делая наружу (ревью perf-pass 7): экран,
// которому без сети, агента или эмбеддера показать нечего, отвечает 204, и
// нажатие идёт обычным запросом. Наведение — не намерение человека: вопрос
// жителю машины («Сэкономлено») или серверу эмбеддингов от пролёта курсора
// писал бы журнал агента и ходил в сеть. Экран вне списка — тоже 204.
func TestPrefetchGoesNowhere(t *testing.T) {
	s := healthLiveSized(t)
	c, _ := s.eng.Store().CreateChat("Проба")
	get := func(screen string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", "/ui?screen="+url.QueryEscape(screen), nil)
		r.Header.Set("HX-Request", "true")
		r.Header.Set("X-Sema-Prefetch", "1")
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:8765"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, body := get("tasks"); code != 200 || !strings.Contains(body, `id="screen"`) {
		t.Fatalf("«Задачи» подогреваются: %d, %d байт", code, len(body))
	}
	for _, sc := range []string{fmt.Sprintf("chat:%d", c), "inbox", "settings:tools", "settings:models"} {
		if code, _ := get(sc); code != 204 {
			t.Errorf("%s: подогрев ответил %d, ждали 204", sc, code)
		}
	}
	// обычный запрос того же экрана — целиком
	if body := listMarkup(t, s, "settings:tools"); !strings.Contains(body, "pdftotext") {
		t.Error("обычный запрос «Инструментов» потерял список")
	}
}
