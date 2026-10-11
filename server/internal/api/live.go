package api

// Живое обновление: постоянное соединение, по которому Сёма сообщает
// веб-клиенту имена изменившихся экранов. Содержимое клиент перечитывает
// обычным запросом `/ui` — по проводу не ходит ни разметка, ни данные.
//
// Ручка живёт под тем же `protect`, что и всё: cookie сессии у браузера,
// Bearer у приложения, локальный запрос при «без входа». Рукопожатие —
// GET, и общий запрет чужого источника его не касается, поэтому источник
// проверяется здесь: страница с другого сайта в том же браузере иначе
// получала бы живой провод с cookie человека.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// liveConns — сколько соединений держит каждая сессия. Вкладок много не
// бывает; бесконечно их открывает только тот, кто хочет занять память.
type liveConns struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *liveConns) take(who string, limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	if c.n[who] >= limit {
		return false
	}
	c.n[who]++
	return true
}

func (c *liveConns) release(who string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[who] > 1 {
		c.n[who]--
	} else {
		delete(c.n, who)
	}
}

// liveFrame — кадр: какие экраны перечитать и какая сборка у сервера.
//
// Сборка нужна затем, что расхождение клиента и сервера прежде замечалось
// только на htmx-запросе. Вкладка, открытая до выката, живёт на проводе и
// видит новую разметку со старым app.js: кнопки на месте, а обработчиков
// у них нет — «нажимаю, ничего не происходит» (13 сентября 2026).
type liveFrame struct {
	Screens []string `json:"экраны"`
	Build   string   `json:"сборка,omitempty"`
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	// чужой источник — отказ до рукопожатия, с записью в журнал доступа
	if crossSite(r) {
		s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: r.Method, Path: r.URL.Path,
			Status: http.StatusForbidden, IP: clientIP(r), Note: "чужой источник у провода: " + r.Header.Get("Origin")})
		http.Error(w, "провод с чужого сайта не открывается", http.StatusForbidden)
		return
	}
	id := s.auth.Identify(r)
	// предел считается на устройство, а не на всех сразу: у локальных
	// общий Who == "local", у одного passkey — общий session:<id>; мак,
	// телефон и планшет не должны делить одну восьмёрку
	who := id.Who + "|" + clientIP(r)
	if id.Session != nil {
		who = "session:" + id.Session.Hash
	}
	ls := s.eng.LiveSettings()
	if !s.live.take(who, ls.PerSession) {
		s.eng.Store().AddAccess(&store.AccessEntry{Who: id.Who, Method: r.Method, Path: r.URL.Path,
			Status: http.StatusTooManyRequests, IP: clientIP(r), Note: "слишком много проводов у одной сессии"})
		http.Error(w, "слишком много соединений у этой сессии", http.StatusTooManyRequests)
		return
	}
	defer s.live.release(who)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// Подписка готова к моменту ответа 101: первое изменение после
	// рукопожатия не теряется между открытием сокета и подпиской.
	frames := s.eng.SubscribeLive(ctx)

	// Origin сверяет и библиотека — с адресом запроса; своё мы уже
	// проверили выше, отключать её проверку незачем
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// клиент нам ничего не шлёт; читаем только ради того, чтобы узнать
	// о закрытии с его стороны
	readDone := conn.CloseRead(ctx)
	ping := time.NewTicker(ls.WriteWait * 2)
	defer ping.Stop()
	for {
		select {
		case <-readDone.Done():
			return
		case <-ctx.Done():
			return
		case <-ping.C:
			if !s.liveAccessValid(r, id) {
				conn.Close(websocket.StatusPolicyViolation, "доступ отозван или истёк")
				return
			}
			// провод без сигналов часами — обычное дело; пинг отличает
			// тихий провод от мёртвого
			pctx, pcancel := context.WithTimeout(ctx, ls.WriteWait)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		case screens, ok := <-frames:
			if !ok {
				return
			}
			if !s.liveAccessValid(r, id) {
				conn.Close(websocket.StatusPolicyViolation, "доступ отозван или истёк")
				return
			}
			body, err := json.Marshal(liveFrame{Screens: screens, Build: web.Build()})
			if err != nil {
				return
			}
			// не вычитывает дольше срока — закрываем: клиент переподключится
			wctx, wcancel := context.WithTimeout(ctx, ls.WriteWait)
			err = conn.Write(wctx, websocket.MessageText, body)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

// Проверяем тот же пропуск, которым открыли провод. После отзыва cookie
// соединение не получает вместо неё локальное исключение.
func (s *Server) liveAccessValid(r *http.Request, id auth.Identity) bool {
	switch id.Kind {
	case "session":
		return id.Session != nil && s.eng.Store().SessionActive(id.Session.Hash)
	case "token":
		current := s.auth.Identify(r)
		return current.Kind == "token" && current.Token.ID == id.Token.ID && auth.HasScope(current.Token, "ui")
	case "local":
		return s.auth.LocalOpen() && auth.IsLocal(r) && s.localHostAllowed(r.Host)
	}
	return false
}
