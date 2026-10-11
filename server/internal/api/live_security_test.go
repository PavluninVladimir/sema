package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/coder/websocket"
)

func TestLiveStopsAfterAccessRevocation(t *testing.T) {
	for _, kind := range []string{"session", "token", "local"} {
		t.Run(kind, func(t *testing.T) {
			s, st := testServer(t)
			hdr := http.Header{}
			var revoke func() error
			switch kind {
			case "session":
				token, err := s.auth.OpenSession(0, "тест", "127.0.0.1", "")
				if err != nil {
					t.Fatal(err)
				}
				hdr.Set("Cookie", "sema_session="+token)
				// Локальный обход остаётся включён: отозванная сессия не
				// должна незаметно превратиться в локальное соединение.
				revoke = func() error { return st.DeleteSession(store.HashSecret(token)) }
			case "token":
				token, err := st.AddToken("провод", []string{"ui"})
				if err != nil {
					t.Fatal(err)
				}
				hdr.Set("Authorization", "Bearer "+token)
				info, _ := st.TokenByValue(token)
				revoke = func() error { return st.RevokeToken(info.ID) }
			case "local":
				revoke = func() error { return st.SetSetting(auth.SettingLocalOpen, "false") }
			}
			srv := httptest.NewServer(s.Handler())
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/ws",
				&websocket.DialOptions{HTTPHeader: hdr})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			s.eng.Changed("health")
			if _, _, err := conn.Read(ctx); err != nil {
				t.Fatal(err)
			}
			if err := revoke(); err != nil {
				t.Fatal(err)
			}
			s.eng.Changed("chat:123")
			_, body, err := conn.Read(ctx)
			if err == nil {
				t.Fatalf("после отзыва пришёл кадр: %s", body)
			}
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("соединение не закрыто из-за отзыва доступа: %v", err)
			}
		})
	}
}
