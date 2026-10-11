package engine

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestMemoryEndRejectsAnotherConsumerSession(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveAgent(&store.Agent{Name: "машина", State: store.AgentOK,
		Allowed: []string{CanMemory}}); err != nil {
		t.Fatal(err)
	}
	owner := e.ExternalOrigin("Codex", "/tmp/audit-owner")
	sid, err := st.StartMemorySession(owner.Consumer.ID, owner.Agent, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EndMemorySession(sid, "исходная сводка"); err != nil {
		t.Fatal(err)
	}
	for _, granted := range []bool{false, true} {
		t.Run(fmt.Sprint(granted), func(t *testing.T) {
			other := e.ExternalOrigin("Codex", "/tmp/audit-other")
			if granted {
				if err := st.SetConsumerAreas(other.Consumer.ID, []string{"код"}); err != nil {
					t.Fatal(err)
				}
			}
			params, _ := json.Marshal(map[string]any{"client": "Codex", "path": "/tmp/audit-other",
				"session_id": sid, "summary": "подмена"})
			if _, err := e.handleAgentAsk(context.Background(), "машина", "память.memory_session_end", params); err == nil {
				t.Error("чужой session_id принят")
			}
			got, err := st.MemorySession(sid)
			if err != nil || got.Summary != "исходная сводка" {
				t.Fatalf("чужая сводка изменена: %+v, %v", got, err)
			}
		})
	}
}

func TestMemoryNoteRequiresGrantedAreas(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	o := e.ExternalOrigin("Codex", "/tmp/audit-note")
	if err := e.SessionNote("Codex", o.Consumer.Path, "запись без прав"); err == nil {
		t.Error("заметка без областей принята")
	}
	if s := st.LastMemorySession(o.Consumer.ID); s != nil {
		t.Fatalf("без прав создана сессия: %+v", s)
	}
	if err := st.SetConsumerAreas(o.Consumer.ID, []string{"код"}); err != nil {
		t.Fatal(err)
	}
	if err := e.SessionNote("Codex", o.Consumer.Path, "разрешённая запись"); err != nil {
		t.Fatal(err)
	}
	if s := st.LastMemorySession(o.Consumer.ID); s == nil || !strings.Contains(s.Summary, "разрешённая запись") {
		t.Fatalf("своя заметка потеряна: %+v", s)
	}
}

func TestAgentFrameRejectsOversizedAndAmbiguousHeaders(t *testing.T) {
	for _, header := range []string{
		"X-Fill: " + strings.Repeat("a", 70<<10) + "\r\nContent-Length: 2\r\n\r\n",
		strings.Repeat("X: a\r\n", 10000) + "Content-Length: 2\r\n\r\n",
		"Content-Length: 1\r\nContent-Length: 2\r\n\r\n",
	} {
		if _, err := readFramed(bufio.NewReader(strings.NewReader(header + "{}"))); err == nil {
			t.Errorf("опасный заголовок принят: %d байт", len(header))
		}
	}
	got, err := readFramed(bufio.NewReader(strings.NewReader("Content-Type: application/json\r\nContent-Length: 2\r\n\r\n{}")))
	if err != nil || string(got) != "{}" {
		t.Fatalf("обычный кадр не читается: %q, %v", got, err)
	}
}

func TestAgentRevokeClosesExistingConnection(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveAgent(&store.Agent{Name: "машина", State: store.AgentOK}); err != nil {
		t.Fatal(err)
	}
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	e.links["машина"] = &link{agent: "машина", conn: server}
	if err := e.RevokeAgent("машина"); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, err := peer.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("соединение осталось открыто")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("отзыв не закрыл уже открытое соединение")
	}
}

func TestOfficeArchiveHasTotalExpansionBudget(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for i := 0; i < 4; i++ {
		f, err := zw.Create(fmt.Sprintf("ppt/slides/slide%d.xml", i))
		if err != nil {
			t.Fatal(err)
		}
		body := strings.Repeat("a", 3<<20)
		if i == 3 {
			body = "SHOULD_NOT_BE_DECOMPRESSED"
		}
		if _, err := fmt.Fprintf(f, "<p><t>%s</t></p>", body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	got := officeText(archive.Bytes())
	if len(got) == 0 {
		t.Fatal("обычный текст потерян")
	}
	if len(got) > 8<<20 || strings.Contains(got, "SHOULD_NOT_BE_DECOMPRESSED") {
		t.Fatalf("совокупная распаковка не ограничена: %d байт текста", len(got))
	}
}

func TestAgentHandshakeHasOwnDeadline(t *testing.T) {
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	conn := tls.Server(server, &tls.Config{MinVersion: tls.VersionTLS13})
	started := time.Now()
	if err := agentHandshake(context.Background(), conn, 100*time.Millisecond); err == nil {
		t.Fatal("молчащий peer прошёл рукопожатие")
	}
	if time.Since(started) > time.Second {
		t.Fatal("рукопожатие удерживает соединение сверх срока")
	}
}

func TestAgentRequestFloodClosesConnection(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveAgent(&store.Agent{Name: "машина", State: store.AgentOK, Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	l := &link{agent: "машина", fingerprint: "fp", conn: server, out: bufio.NewWriter(server),
		waiting: map[int]chan json.RawMessage{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { l.read(ctx, e, "машина"); close(done) }()
	// Агент шлёт запросы и не читает ответы. Они не должны создавать
	// неограниченное число ожидающих горутин.
	_ = peer.SetWriteDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i <= linkMaxRequests; i++ {
		body := fmt.Sprintf(`{"id":%d,"method":"unknown.method"}`, i)
		if _, err := fmt.Fprintf(peer, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = peer.Close()
		<-done
		t.Fatal("переполнение очереди не остановило провод и обработчики")
	}
}
