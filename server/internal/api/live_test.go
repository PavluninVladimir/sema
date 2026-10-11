package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// провод — тот же вход, что и у всего: без сессии при выключенном
// «без входа» соединение отвергается, с сессией — открывается и приносит
// имена изменившихся экранов
func TestWireUnderSameLogin(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, resp, err := websocket.Dial(ctx, wsURL, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("провод без входа: err=%v код=%d", err, code)
	}

	token, err := s.auth.OpenSession(0, "проба", "127.0.0.1", "проба")
	if err != nil {
		t.Fatal(err)
	}
	hdr := http.Header{"Cookie": {"sema_session=" + token}}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("провод с сессией: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// изменение в хранилище доезжает кадром с именем экрана
	if _, err := s.eng.Notify(&store.Notification{Key: "проба", Source: "core", Level: "info", Title: "Проба"}); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("кадр не пришёл: %v", err)
	}
	var f struct {
		Screens []string `json:"экраны"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sc := range f.Screens {
		if sc == store.ScreenNotifications {
			found = true
		}
	}
	if !found {
		t.Fatalf("в кадре нет уведомлений: %v", f.Screens)
	}
}

// страница с другого сайта в том же браузере получала бы живой провод с
// cookie человека: чужой источник отвергается до рукопожатия
func TestWireDoesNotOpenForForeignSite(t *testing.T) {
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{"Origin": {"https://evil.invalid"}}
	_, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("чужой Origin открыл провод: err=%v resp=%v", err, resp)
	}
}

// соединений на сессию — не больше предела: вкладок много не бывает
func TestWireLimitPerSession(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting("ui.ws_per_session", "2"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	token, _ := s.auth.OpenSession(0, "проба", "127.0.0.1", "проба")
	hdr := http.Header{"Cookie": {"sema_session=" + token}}
	var conns []*websocket.Conn
	defer func() {
		for _, c := range conns {
			c.Close(websocket.StatusNormalClosure, "")
		}
	}()
	for i := 0; i < 2; i++ {
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
		if err != nil {
			t.Fatalf("провод %d: %v", i+1, err)
		}
		conns = append(conns, c)
	}
	_, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("третий провод прошёл сверх предела: err=%v", err)
	}
}

// десять вкладок — это десять проводов; каждый стоит горутин на пальцах
// одной руки и ничего не течёт после закрытия
func TestTenWiresAreCheap(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting("ui.ws_per_session", "20"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token, _ := s.auth.OpenSession(0, "проба", "127.0.0.1", "проба")
	hdr := http.Header{"Cookie": {"sema_session=" + token}}
	before := runtime.NumGoroutine()
	var conns []*websocket.Conn
	for i := 0; i < 10; i++ {
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
		if err != nil {
			t.Fatalf("провод %d: %v", i+1, err)
		}
		conns = append(conns, c)
	}
	time.Sleep(200 * time.Millisecond)
	perConn := (runtime.NumGoroutine() - before) / 10
	if perConn > 8 {
		t.Fatalf("провод стоит %d горутин — слишком дорого для Pi", perConn)
	}
	for _, c := range conns {
		c.Close(websocket.StatusNormalClosure, "")
	}
	time.Sleep(500 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+5 {
		t.Fatalf("после закрытия проводов осталось лишних горутин: %d → %d", before, after)
	}
	t.Logf("горутин на провод: %d (клиент и сервер в одном процессе)", perConn)
}
