package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bufio"
	"net"
	"sync"

	"github.com/chromedp/chromedp"
)

// Сёма перезапускается посреди работы: провод рвётся, клиент переходит на
// частый опрос и, когда Сёма поднялся, возвращается на провод сам. Ради
// этого опрос и не выкинули совсем: молчание провода неотличимо от
// «ничего не изменилось», а выкат случается каждый день.
func TestBrowserReturnsToWireAfterDeploy(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	// частота опроса разная у живого и оборванного провода: по ней и
	// видно, за кого клиент себя считает
	for k, v := range map[string]string{"ui.poll_s": "3", "ui.poll_live_s": "600"} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	// опрос живёт только на экране с полем ввода: у чата он и есть
	чат, err := st.CreateChat("проба переподключения")
	if err != nil {
		t.Fatal(err)
	}
	var лежит atomic.Bool
	провода := &intercept{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// «сервера нет»: рукопожатие провода отклоняется, экраны
		// продолжают отдаваться — так выглядит перезапуск за прокси
		if лежит.Load() && r.URL.Path == "/api/ws" {
			http.Error(w, "Сёма поднимается", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/ws" {
			w = &interceptor{ResponseWriter: w, stock: провода}
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	screen := "chat:" + strconv.FormatInt(чат, 10)
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen="+screen)); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	живой := `(() => { const п = window.sema && sema.wire && sema.wire(); return !!п && п.open; })()`
	var открыт bool
	if err := chromedp.Run(ctx, chromedp.Poll(живой, &открыт, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !открыт {
		t.Fatalf("провод не открылся с самого начала: %v", err)
	}
	if опрос := screenTrigger(ctx, t); !strings.Contains(опрос, "600s") {
		t.Fatalf("при живом проводе опрос должен быть редким, а стоит %q", опрос)
	}

	// Сёма легла посреди работы: связь пропадает под уже открытым
	// проводом — ровно так это выглядит при выкате, а не «страницу
	// открыли, когда сервера уже не было»
	лежит.Store(true)
	провода.breakWire()
	var упал bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!(`+живой+`)`, &упал, chromedp.WithPollingTimeout(30*time.Second))); err != nil || !упал {
		t.Fatalf("провод должен был оборваться: %v", err)
	}
	var частый bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`document.getElementById('screen').getAttribute('hx-trigger').includes('every 3s')`,
		&частый, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !частый {
		t.Fatalf("без провода клиент должен опрашивать часто, а стоит %q", screenTrigger(ctx, t))
	}

	// Сёма поднялась — клиент возвращается сам, без перезагрузки страницы
	лежит.Store(false)

	var вернулся bool
	if err := chromedp.Run(ctx, chromedp.Poll(живой, &вернулся, chromedp.WithPollingTimeout(60*time.Second))); err != nil || !вернулся {
		t.Fatalf("после выката клиент не вернулся на провод сам: %v", err)
	}
	var сноваРедкий bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`document.getElementById('screen').getAttribute('hx-trigger').includes('every 600s')`,
		&сноваРедкий, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !сноваРедкий {
		t.Fatalf("вернувшись на провод, опрос должен снова стать редким, а стоит %q", screenTrigger(ctx, t))
	}
}

func screenTrigger(ctx context.Context, t *testing.T) string {
	t.Helper()
	var v string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('screen').getAttribute('hx-trigger') || ''`, &v)); err != nil {
		return "не прочитать: " + err.Error()
	}
	return v
}

// intercept — забранные соединения провода: чтобы оборвать их так же
// грубо, как это делает остановка контейнера.
type intercept struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (п *intercept) add(c net.Conn) {
	п.mu.Lock()
	п.conns = append(п.conns, c)
	п.mu.Unlock()
}

func (п *intercept) breakWire() {
	п.mu.Lock()
	связи := п.conns
	п.conns = nil
	п.mu.Unlock()
	for _, c := range связи {
		_ = c.Close()
	}
}

type interceptor struct {
	http.ResponseWriter
	stock *intercept
}

func (п *interceptor) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := п.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	c, rw, err := h.Hijack()
	if err == nil {
		п.stock.add(c)
	}
	return c, rw, err
}

func (п *interceptor) Unwrap() http.ResponseWriter { return п.ResponseWriter }
