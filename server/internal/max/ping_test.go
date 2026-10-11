package max

// Пингует сервер, а отвечает клиент — и связь держится ровно этим.
//
// Тест поднимает настоящий WebSocket и ведёт себя как MAX: присылает
// `cmd=0 op=1` и ждёт ответа. Не дождался — рвёт связь, как это делает
// настоящий сервер через десять секунд. Проверять тут надо именно жест:
// прежний код был уверен, что ping шлёт он, и молчал в ответ.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeMAX — сервер, который пингует клиента.
func fakeMAX(t *testing.T, replies chan<- frame) {
	t.Helper()
	srv, cl := twoEnds(t)

	c := &Client{waiting: map[int]chan frame{}, conn: cl, dead: make(chan struct{})}
	go func() { _ = c.read(context.Background()) }()

	go func() {
		ctx := context.Background()
		raw, _ := json.Marshal(frame{Ver: proto, Cmd: cmdRequest, Seq: 1,
			Opcode: opPing, Payload: json.RawMessage("{}")})
		if err := srv.Write(ctx, websocket.MessageText, raw); err != nil {
			return
		}
		_, data, err := srv.Read(ctx)
		if err != nil {
			close(replies)
			return
		}
		var f frame
		_ = json.Unmarshal(data, &f)
		replies <- f
		close(replies)
	}()
	// связь тут живая, и вежливое прощание ждало бы ответного кадра от
	// поддельного сервера все пять секунд — рвём сразу, это же проверка
	t.Cleanup(func() { cl.CloseNow() })
}

func twoEnds(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	ready := make(chan *websocket.Conn, 1)
	// обработчик обязан дожить до конца проверки, но не дольше: иначе
	// httptest ждёт его при закрытии и тест стоит пять секунд на ровном месте
	stop := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ready <- c
		<-stop
	}))
	t.Cleanup(ts.Close)               // сработает последним
	t.Cleanup(func() { close(stop) }) // а это — раньше, и отпустит его
	s := "ws" + ts.URL[len("http"):]
	cl, _, err := websocket.Dial(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("не подключился: %v", err)
	}
	select {
	case server = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("сервер не принял соединение")
	}
	t.Cleanup(func() { cl.CloseNow(); server.CloseNow() })
	return server, cl
}

// На ping сервера клиент обязан ответить тем же номером и видом «ответ».
func TestWeAnswerServerPing(t *testing.T) {
	replies := make(chan frame, 1)
	fakeMAX(t, replies)

	select {
	case f, ok := <-replies:
		if !ok {
			t.Fatal("клиент промолчал на ping сервера — настоящий MAX закрыл бы связь через 10 секунд")
		}
		if f.Cmd != cmdReply {
			t.Errorf("ответ пришёл видом %d, а должен быть «ответ» (%d)", f.Cmd, cmdReply)
		}
		if f.Seq != 1 {
			t.Errorf("ответ с номером %d, а спрашивали номером 1 — сервер его не узнает", f.Seq)
		}
		if f.Opcode != opPing {
			t.Errorf("ответ на опкод %d вместо %d", f.Opcode, opPing)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("клиент не ответил на ping сервера")
	}
}
