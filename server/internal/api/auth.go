package api

// Вход: страница /login с WebAuthn в браузере, ручки /api/auth/*, защита
// всех остальных маршрутов, экран «Доступ» и журнал.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/web"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const accessScreen = "settings:access"

// statusWriter запоминает код и размер ответа для журнала доступа.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// trustProxy — Сёма стоит за своим прокси, и X-Forwarded-For пишет он.
// Без прокси заголовок принадлежит клиенту и верить ему нельзя: с ним
// подделывался адрес для выдачи сессии по коду. Настройка `auth.trust_proxy`
// на «Доступе», по умолчанию выключена.
var trustProxy atomic.Bool

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" && trustProxy.Load() {
		return auth.NormalizeIP(strings.TrimSpace(strings.Split(f, ",")[0]))
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return auth.NormalizeIP(r.RemoteAddr)
	}
	return auth.NormalizeIP(host)
}

// maxBody — предел тела обычного запроса; файлы идут своей дорогой.
const maxBody = 4 << 20

// protect — кто пришёл и можно ли ему сюда. Открыты только здоровье, вход
// и страница входа; остальное — сессия, токен с нужным правом или
// локальный запрос, пока это разрешено настройкой.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// заголовки — всем ответам разом, включая вход и здоровье: экран
		// не кэшируется прокси, тип не угадывается, в чужой iframe не
		// вставляется, адрес не утекает в Referer
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		// «в чужой iframe не вставляется» — но тело письма Сёма
		// показывает в СВОЕЙ рамке, и «DENY» запрещает даже это: браузер
		// отказывается рисовать документ в любом фрейме, включая наш
		// собственный. 10 сентября 2026 из-за этого письмо в песочнице
		// выглядело серым прямоугольником со значком «не открылось».
		//
		// Для этого одного пути ставим `frame-ancestors 'self'` — то же
		// самое правило, но точное: свой адрес может, чужие нет. Заголовок
		// CSP этой ручки дописывается в mailbody.go
		if p == messagePath || strings.HasPrefix(p, messagePath+"/") {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		} else {
			h.Set("X-Frame-Options", "DENY")
		}
		// «same-origin», а не «no-referrer»: адрес по-прежнему не уходит
		// на чужие сайты, но WebKit при no-referrer обнуляет и Origin у
		// собственных запросов страницы — Safari переставал сохранять
		// что-либо, получая «запрос с чужого сайта не выполняется»
		h.Set("Referrer-Policy", "same-origin")
		// Проверка действует и на публичные ручки входа: иначе чужой
		// сайт мог начать привязку ключа или передать сессию приложению.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && crossSite(r) {
			http.Error(w, "запрос с чужого сайта не выполняется", http.StatusForbidden)
			s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: r.Method, Path: p, Status: http.StatusForbidden, IP: clientIP(r), Note: "чужой источник: " + r.Header.Get("Origin")})
			return
		}
		if auth.IsLocal(r) && !s.localHostAllowed(r.Host) {
			http.Error(w, "неизвестное имя сервера: добавьте его в «Доступ → Имена сервера»", http.StatusForbidden)
			s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: r.Method, Path: p, Status: http.StatusForbidden, IP: clientIP(r), Note: "неизвестное имя сервера: " + r.Host})
			return
		}
		// тело запроса ограничено всем, кроме загрузки файлов: экран и
		// действие в мегабайты не укладываются только у того, кто хочет
		// занять память
		if r.Method != http.MethodGet && r.Method != http.MethodHead && p != "/api/files" && p != "/upload" {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		// вход агента по приглашению — свой пропуск: одноразовый код,
		// который живёт минуты и сгорает. Сессии и токена у агента ещё нет
		// оболочка интерфейса отдаётся без входа: в ней нет данных, а
		// страница входа нужна раньше сессии
		if webShell(p) || openPath(p) {
			next.ServeHTTP(w, r)
			return
		}
		id := s.auth.Identify(r)
		started := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		deny := func(code int, msg string) {
			// человек в браузере не должен видеть JSON с отказом: его
			// уводит на вход, а приложениям по-прежнему отвечаем телом
			// htmx 4 подменяет содержимое окна ответом любого кода, в том
			// числе отказом: без этого человек видел строку JSON вместо
			// экрана. Отдаём разметку с кнопкой, а уводит на вход клиент
			// по событию htmx:response:error
			if code == http.StatusUnauthorized && r.Header.Get("HX-Request") != "" {
				sw.Header().Set("Content-Type", "text/html; charset=utf-8")
				sw.WriteHeader(code)
				fmt.Fprint(sw, `<div class="connecting">Нужен вход. <a href="/login">Войти с passkey</a></div>`)
				s.eng.Store().AddAccess(&store.AccessEntry{Who: id.Who, Method: r.Method, Path: p, Status: code, IP: clientIP(r), Note: msg})
				return
			}
			if code == http.StatusUnauthorized && wantsPage(r) {
				s.eng.Store().AddAccess(&store.AccessEntry{Who: id.Who, Method: r.Method, Path: p, Status: http.StatusFound, IP: clientIP(r), Note: msg})
				http.Redirect(sw, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			sw.Header().Set("Content-Type", "application/json")
			sw.WriteHeader(code)
			fmt.Fprintf(sw, `{"error":%q,"login":"/login"}`, msg)
			s.eng.Store().AddAccess(&store.AccessEntry{Who: id.Who, Method: r.Method, Path: p, Status: code, IP: clientIP(r), Note: msg})
		}
		switch id.Kind {
		case "none":
			deny(http.StatusUnauthorized, "нужен вход: passkey на /login или токен в Authorization")
			return
		case "session":
			// сессия, открытая фразой снаружи, допускает ровно одно —
			// церемонию заведения ключа, а та идёт по /api/auth/register/*
			// и до этого места не доходит вовсе (открыта выше). Всё, что
			// сюда дошло с такой сессией, — экран, действие или файл —
			// получает отказ, каким бы путём ни попросили
			//
			// Исключение одно: запрос, который Сёма и без всякой сессии
			// пускает без входа (своя сеть при включённом «без входа»).
			// Иначе забытая на телефоне урезанная cookie запирала бы
			// хозяина дома — там, где он вообще ничего не предъявляет
			if id.Session != nil && id.Session.Restricted && !(s.auth.LocalOpen() && auth.IsLocal(r)) {
				msg := "фраза снаружи даёт только завести ключ устройства: откройте /login и заведите passkey — дальше будет пускать он"
				if id.Session.BySetupCode() {
					// код первой установки — тот же урезанный вид, свои слова
					msg = "код первой установки даёт только завести первый ключ: откройте /login и заведите passkey — дальше будет пускать он"
				}
				switch {
				case r.Header.Get("HX-Request") != "":
					// Отказ верный, но человек видел его пустым экраном
					// (1 октября 2026: iPhone через шлюз, 403 на каждый
					// /ui, и ни слова про вход). Клиент уводит на /login
					// по 401 — в трёх местах (подмена окна, очередь
					// исходящих, сохранение форм), и служебный работник
					// по нему же стирает снимки. Четвёртого пути для
					// «ключ нужен» не заводим: тот же код, свои слова.
					// Данных в ответе нет, действие не выполняется
					sw.Header().Set("Content-Type", "text/html; charset=utf-8")
					sw.WriteHeader(http.StatusUnauthorized)
					чем := "фраза снаружи"
					if id.Session.BySetupCode() {
						чем = "код первой установки"
					}
					fmt.Fprint(sw, `<div class="connecting">Нужен ключ устройства: `+чем+` пускает только для этого. <a href="/login">Завести passkey</a></div>`)
					s.eng.Store().AddAccess(&store.AccessEntry{Who: id.Who, Method: r.Method, Path: p, Status: http.StatusUnauthorized, IP: clientIP(r), Note: msg})
				case wantsPage(r):
					// страница, открытая переходом, а не клиентом: сразу к
					// месту, где ключ заводится. Оболочка (`/`) сюда не
					// попадает — она открыта без входа и переадресации не
					// получает нарочно: служебный работник кладёт её в кэш
					// при установке, и страница входа под именем «/» ломала
					// бы открытие без сети. Оболочку уводит на вход первый
					// же её запрос экрана (ветка выше)
					s.eng.Store().AddAccess(&store.AccessEntry{Who: id.Who, Method: r.Method, Path: p, Status: http.StatusSeeOther, IP: clientIP(r), Note: msg})
					http.Redirect(sw, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				default:
					// приложения, провод, файлы по ссылке: честный отказ с
					// кодом, как был
					deny(http.StatusForbidden, msg)
				}
				return
			}
		case "token":
			scope := auth.ScopeFor(p)
			if !auth.HasScope(id.Token, scope) {
				deny(http.StatusForbidden, "у токена «"+id.Token.Name+"» нет права «"+scope+"»")
				return
			}
			s.eng.Store().TouchToken(id.Token.ID)
		}
		next.ServeHTTP(sw, r)
		if id.Kind == "token" || sw.status >= 400 {
			s.eng.Store().AddAccess(&store.AccessEntry{Who: id.Who, Method: r.Method, Path: p, Status: sw.status, Bytes: sw.bytes,
				Seconds: time.Since(started).Seconds(), IP: clientIP(r)})
		}
	})
}

// openPath — что открыто без входа помимо оболочки. Список короткий и
// нарочно один: каждая строка в нём — дверь, и держит его
// `TestOpenPathsAreListed`.
//   - здоровье и страница входа с её церемониями;
//   - вход агента по одноразовому коду;
//   - документ клиента для серверов входа OAuth (`engine.MCPClientDocPath`):
//     его скачивает чужой сервер, у которого нет ни сессии, ни токена; в
//     нём только имя, адрес и адрес возврата Сёмы, взятые из настроек.
func openPath(p string) bool {
	return p == "/api/health" || p == "/login" || p == "/api/agents/enroll" || p == engine.MCPClientDocPath ||
		strings.HasPrefix(p, "/api/auth/") ||
		// ссылка на файл открывается без входа: доступ даёт подпись
		(strings.HasPrefix(p, engine.SharePath) && len(p) > len(engine.SharePath))
}

// crossSite — запрос пришёл со страницы другого происхождения.
func crossSite(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return true
	}
	// браузер сам говорит, откуда запрос, и подделать это поле со
	// страницы нельзя: «same-origin» весомее пустого или обнулённого
	// Origin, который у WebKit появляется от политики referrer
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "same-origin") {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return origin == "null"
	}
	u, err := url.Parse(origin)
	if err != nil {
		return true
	}
	scheme := "http"
	if r.TLS != nil || (trustProxy.Load() && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return u.Scheme != scheme || !strings.EqualFold(u.Host, r.Host) || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != ""
}

// При DNS rebinding RemoteAddr локальный, а Host принадлежит чужому сайту.
func (s *Server) localHostAllowed(host string) bool {
	u, err := url.Parse("http://" + host)
	if err != nil || u.User != nil || u.Path != "" || u.Hostname() == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	// адрес числом подделать через чужой DNS нельзя: rebinding живёт на
	// именах. Поэтому 192.168.88.10:8765 из своей сети — всегда можно
	if h == "localhost" || net.ParseIP(h) != nil {
		return true
	}
	if strings.EqualFold(h, s.auth.RPID()) {
		return true
	}
	for _, name := range s.auth.LocalHosts() {
		if h == name {
			return true
		}
	}
	origin, err := url.Parse(s.auth.Origin())
	return err == nil && strings.EqualFold(h, origin.Hostname())
}

// ---- ручки входа ----

func (s *Server) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("GET /api/auth/state", s.handleAuthState)
	mux.HandleFunc("POST /api/auth/register/begin", s.handleRegisterBegin)
	mux.HandleFunc("POST /api/auth/register/finish", s.handleRegisterFinish)
	mux.HandleFunc("POST /api/auth/login/begin", s.handleLoginBegin)
	mux.HandleFunc("POST /api/auth/login/finish", s.handleLoginFinish)
	mux.HandleFunc("POST /api/auth/phrase", s.handlePhraseLogin)
	mux.HandleFunc("POST /api/auth/setup", s.handleSetupLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
}

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	id := s.auth.Identify(r)
	restricted := id.Kind == "session" && id.Session != nil && id.Session.Restricted
	writeJSON(w, map[string]any{
		"passkeys": s.eng.Store().CountPasskeys(), "local": auth.IsLocal(r), "localOpen": s.auth.LocalOpen(),
		"loggedIn": id.Kind == "session" || id.Kind == "local", "kind": id.Kind, "rpId": s.auth.RPID(), "origin": s.auth.Origin(),
		// заведена ли фраза, страница входа обязана знать: кнопка, которая
		// заведомо не сработает, хуже честного объяснения. Что вход фразой
		// у Сёмы есть, и так видно по самой странице — тайны здесь нет
		"canRegister": s.canRegister(r), "restricted": restricted, "phrase": s.auth.HasPhrase(),
		// код первой установки: действует ли он сейчас и открыта ли сессия им.
		// Страница входа без ключей просит код только тогда, когда он есть, —
		// иначе объясняет, откуда его взять. Сам код сюда не попадает никогда
		"setup": s.auth.SetupActive(), "bySetup": restricted && id.Session.BySetupCode(),
		// своей ли считается частная сеть: подсказка на странице входа говорит
		// правду о том, кого Сёма пускает без входа, а не одну на оба случая
		"localPrivate": auth.LocalPrivate.Load(),
	})
}

// canRegister — кто может завести ключ. Первый — с самой машины (петля, а
// частная сеть — только когда человек сам назвал её своей), из сессии по
// фразе или по коду первой установки; следующие — из открытой сессии.
// Сессия по коду пускает только к первому ключу: с ним код погас, и всё, что
// им открыто, тоже (open-source, решение 5).
func (s *Server) canRegister(r *http.Request) bool {
	id := s.auth.Identify(r)
	switch id.Kind {
	case "local":
		return true // с самой машины, пока ей доверяют без входа
	case "session":
		if id.Session.BySetupCode() {
			return s.eng.Store().CountPasskeys() == 0
		}
		return true // из сессии: по ключу или по фразе
	}
	return s.eng.Store().CountPasskeys() == 0 && auth.IsLocal(r)
}

func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if !s.canRegister(r) {
		http.Error(w, "первый ключ заводится с самой машины, кодом первой установки или контрольной фразой; следующие — из открытой сессии", http.StatusForbidden)
		return
	}
	opts, id, err := s.auth.BeginRegistration(r.URL.Query().Get("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ceremony": id, "options": opts})
}

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	// начало проверяло, кому можно заводить ключ; конец обязан проверить
	// то же — иначе церемония, начатая с машины, заканчивается откуда угодно
	if !s.canRegister(r) {
		http.Error(w, "первый ключ заводится с самой машины, кодом первой установки или фразой; следующие — из сессии", http.StatusForbidden)
		return
	}
	pid, _, err := s.auth.FinishRegistration(r.URL.Query().Get("ceremony"), r.URL.Query().Get("name"), r)
	if err != nil {
		s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: "REGISTER", Path: "/api/auth/register", Status: 400, IP: clientIP(r), Note: err.Error()})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token, err := s.auth.OpenSession(pid, r.UserAgent(), clientIP(r), "регистрация ключа")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	note := "новый passkey"
	s.dropRestrictedSession(r)
	// ключ есть — код первой установки гаснет, и сессии, открытые им, тоже
	if s.auth.EndSetup() {
		note += " · код первой установки погашен"
	}
	s.eng.Store().AddAccess(&store.AccessEntry{Who: fmt.Sprintf("session:%d", pid), Method: "REGISTER", Path: "/api/auth/register", Status: 200, IP: clientIP(r), Note: note})
	setSessionCookie(w, r, token)
	writeJSON(w, map[string]any{"ok": true, "passkey": pid})
}

// dropRestrictedSession — урезанная сессия (фраза снаружи) своё сделала, как
// только ключом открыта полная: cookie в браузере заменяется новой, а старая
// строка в базе жила бы дальше и позволяла бы заводить ключи тому, кто её
// унёс. Удаляется только урезанная: полную сессию, с которой пришли
// «завести ещё один ключ», трогать нельзя — она остаётся рабочей.
func (s *Server) dropRestrictedSession(r *http.Request) {
	id := s.auth.Identify(r)
	if id.Kind == "session" && id.Session != nil && id.Session.Restricted {
		_ = s.eng.Store().DeleteSession(id.Session.Hash)
	}
}

func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	opts, id, err := s.auth.BeginLogin()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ceremony": id, "options": opts})
}

func (s *Server) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	pid, _, err := s.auth.FinishLogin(r.URL.Query().Get("ceremony"), r)
	if err != nil {
		s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: "LOGIN", Path: "/api/auth/login", Status: 401, IP: clientIP(r), Note: err.Error()})
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	token, err := s.auth.OpenSession(pid, r.UserAgent(), clientIP(r), "вход по passkey")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.eng.Store().AddAccess(&store.AccessEntry{Who: fmt.Sprintf("session:%d", pid), Method: "LOGIN", Path: "/api/auth/login", Status: 200, IP: clientIP(r), Note: "вход по passkey"})
	s.dropRestrictedSession(r)
	setSessionCookie(w, r, token)
	writeJSON(w, map[string]any{"ok": true})
}

// handlePhraseLogin — вход контрольной фразой: единственный способ
// открыть Сёму с устройства, на котором ещё нет ключа. Из своей сети
// сессия полноценная, снаружи — урезанная (см. protect): фразу можно
// выманить поддельной страницей, а ключ с устройства не увозится.
func (s *Server) handlePhraseLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phrase string `json:"phrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "не разобрать запрос", http.StatusBadRequest)
		return
	}
	source := clientIP(r)
	res := s.auth.TryPhrase(source, body.Phrase)
	if !res.Locked.IsZero() {
		осталось := int(time.Until(res.Locked)/time.Minute) + 1
		msg := "перебор фразы: заперто ещё на " + plural(осталось, "минуту", "минуты", "минут") + " (до " + res.Locked.Format("15:04") + ")"
		s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: "PHRASE", Path: "/api/auth/phrase", Status: http.StatusTooManyRequests, IP: source, Note: msg})
		http.Error(w, msg, http.StatusTooManyRequests)
		return
	}
	if !res.OK {
		msg := "фраза не подходит"
		if res.Typo {
			msg = "в фразе опечатка: контрольная группа не сходится"
		}
		s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: "PHRASE", Path: "/api/auth/phrase", Status: http.StatusUnauthorized, IP: source, Note: msg})
		http.Error(w, msg, http.StatusUnauthorized)
		return
	}
	local := auth.IsLocal(r)
	note := "вход по фразе (из своей сети)"
	if !local {
		note = "вход по фразе (снаружи — только заведение ключа)"
	}
	token, err := s.auth.OpenSession(0, r.UserAgent(), source, note)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !local {
		_ = s.eng.Store().SetSessionRestricted(store.HashSecret(token), true)
	}
	// «session:0» в журнале читалось бы как «вход ключом номер ноль»:
	// у сессии по фразе ключа нет вовсе, и называть её надо своим именем
	s.eng.Store().AddAccess(&store.AccessEntry{Who: "phrase", Method: "PHRASE", Path: "/api/auth/phrase", Status: 200, IP: source, Note: note})
	setSessionCookie(w, r, token)
	writeJSON(w, map[string]any{"ok": true, "restricted": !local})
}

// handleSetupLogin — вход кодом первой установки: пока ключей нет, так
// заводит первый ключ тот, чей браузер приходит не по петле (Сёма в Docker).
// Сессия всегда урезанная — откуда бы ни пришли: код печатается в журнал, и
// дать ему больше, чем заведение ключа, значило бы сделать журнал ключом.
// В журнал доступа идут вход и отказы, но не код: ни в пути, ни в заметке.
func (s *Server) handleSetupLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "не разобрать запрос", http.StatusBadRequest)
		return
	}
	const path = "/api/auth/setup"
	source := clientIP(r)
	res := s.auth.TrySetupCode(source, body.Code)
	if !res.Locked.IsZero() {
		осталось := int(time.Until(res.Locked)/time.Minute) + 1
		msg := "перебор кода: заперто ещё на " + plural(осталось, "минуту", "минуты", "минут") + " (до " + res.Locked.Format("15:04") + ")"
		s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: "SETUP", Path: path, Status: http.StatusTooManyRequests, IP: source, Note: msg})
		http.Error(w, msg, http.StatusTooManyRequests)
		return
	}
	if !res.OK {
		msg := "код первой установки не подходит"
		if res.Gone {
			msg = "код первой установки больше не действует: ключ уже заведён или вышел срок"
		}
		s.eng.Store().AddAccess(&store.AccessEntry{Who: "anon", Method: "SETUP", Path: path, Status: http.StatusUnauthorized, IP: source, Note: msg})
		http.Error(w, msg, http.StatusUnauthorized)
		return
	}
	token, err := s.auth.OpenSession(0, r.UserAgent(), source, store.SetupSessionNote)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.eng.Store().SetSessionRestricted(store.HashSecret(token), true); err != nil {
		// неурезанная сессия по коду — дверь шире обещанной: лучше никакой
		_ = s.eng.Store().DeleteSession(store.HashSecret(token))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.eng.Store().AddAccess(&store.AccessEntry{Who: "setup", Method: "SETUP", Path: path, Status: http.StatusOK, IP: source, Note: store.SetupSessionNote})
	setSessionCookie(w, r, token)
	writeJSON(w, map[string]any{"ok": true, "restricted": true})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	// по https cookie помечается Secure: иначе она уйдёт и по http на то же
	// имя, где её видит сеть
	secure := r != nil && (r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
	http.SetCookie(w, &http.Cookie{Name: "sema_session", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(auth.SessionTTL.Seconds())})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	id := s.auth.Identify(r)
	if id.Session != nil {
		_ = s.eng.Store().DeleteSession(id.Session.Hash)
	}
	http.SetCookie(w, &http.Cookie{Name: "sema_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true})
}

// ---- страница входа ----

const loginPage = `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Сёма — вход</title>
<style>body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"SF Pro Text",sans-serif;background:#F7F6F4;color:#1D1D1F;display:flex;min-height:100vh;align-items:center;justify-content:center}
.card{width:360px;display:flex;flex-direction:column;gap:14px;align-items:center;text-align:center}
h1{font-size:22px;margin:0}.meta{font-size:12px;color:rgba(60,60,67,.6)}
button{width:100%;height:42px;border:0;border-radius:10px;background:#007AFF;color:#fff;font-size:15px;font-weight:600;cursor:pointer}
button.plain{background:#fff;color:#1D1D1F;border:1px solid rgba(0,0,0,.15)}
input{width:100%;box-sizing:border-box;height:36px;border:1px solid rgba(0,0,0,.15);border-radius:8px;padding:0 10px;font-size:14px}
.note{font-size:12px;color:#1D1D1F;background:rgba(255,188,46,.18);border-radius:8px;padding:10px 12px;line-height:1.5}
.err{font-size:12px;color:#C4271C}.ok{font-size:14px;color:#1E7A32;font-weight:600}</style></head><body>
<div class="card"><img src="/icon.svg" alt="" width="56" height="56"><h1>Сёма</h1><div class="meta" id="rp"></div>
<div id="login"><button id="go">Войти с passkey</button></div>
<div id="reg" style="display:none;width:100%;flex-direction:column;gap:8px"><input id="name" placeholder="Название ключа, например MacBook"><button id="mk" class="plain">Завести passkey</button></div>
<div id="hint" class="note" style="display:none;width:100%;box-sizing:border-box"></div>
<div id="phraseToggle" style="width:100%"><button class="plain" id="phraseShow" type="button">Нет ключа? Войти контрольной фразой</button></div>
<div id="phraseBox" style="display:none;width:100%;flex-direction:column;gap:8px">
<div class="note" id="phraseNote"></div>
<input id="phrase" placeholder="xxxx-xxxx-xxxx-xxxx-xxxx-xxxx-xxxx" autocapitalize="off" autocorrect="off" spellcheck="false">
<button class="plain" id="phraseGo" type="button">Войти фразой</button>
</div>
<div id="setupBox" style="display:none;width:100%;flex-direction:column;gap:8px">
<input id="setup" placeholder="setup-xxxx-xxxx-xxxx-xxxx" autocapitalize="off" autocorrect="off" spellcheck="false" autocomplete="off">
<button class="plain" id="setupGo" type="button">Войти кодом</button>
</div>
<div id="msg" class="meta"></div><div id="err" class="err"></div></div>
<script>
const q=new URLSearchParams(location.search);const want=q.get('register')==='1';
const b64=s=>Uint8Array.from(atob(s.replace(/-/g,'+').replace(/_/g,'/')+'='.repeat((4-s.length%4)%4)),c=>c.charCodeAt(0));
const u64=b=>btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
const свойПуть=п=>{if(typeof п!=='string'||!п.startsWith('/'))return false;try{return new URL(п,location.origin).origin===location.origin;}catch{return false;}};
const куда=()=>{const п=q.get('next');return свойПуть(п)?п:'';};
const say=(t,e)=>{document.getElementById(e?'err':'msg').textContent=t;if(!e)document.getElementById('err').textContent='';};
const hint=t=>{const h=document.getElementById('hint');h.textContent=t;h.style.display=t?'block':'none';};
async function j(u,o){const r=await fetch(u,o);if(!r.ok)throw new Error(await r.text());return r.json();}
function fixCreate(o){o.publicKey.challenge=b64(o.publicKey.challenge);o.publicKey.user.id=b64(o.publicKey.user.id);(o.publicKey.excludeCredentials||[]).forEach(c=>c.id=b64(c.id));return o;}
function fixGet(o){o.publicKey.challenge=b64(o.publicKey.challenge);(o.publicKey.allowCredentials||[]).forEach(c=>c.id=b64(c.id));return o;}
function pack(c){const r=c.response;const out={id:c.id,rawId:u64(c.rawId),type:c.type,response:{clientDataJSON:u64(r.clientDataJSON)}};
 if(r.attestationObject)out.response.attestationObject=u64(r.attestationObject);
 if(r.authenticatorData){out.response.authenticatorData=u64(r.authenticatorData);out.response.signature=u64(r.signature);if(r.userHandle)out.response.userHandle=u64(r.userHandle);}
 return JSON.stringify(out);}
// Safari отдаёт passkey только сразу по клику: параметры церемонии
// запрашиваем заранее, а по клику зовём ключ без походов на сервер.
let pre={login:null,register:null};
let preErr={login:'',register:''};
async function prefetch(kind){try{const u=kind==='login'?'/api/auth/login/begin':'/api/auth/register/begin';
 pre[kind]=await j(u,{method:'POST'});preErr[kind]='';}catch(e){pre[kind]=null;preErr[kind]=e.message;}}
function retry(kind){say('Готовим параметры…');prefetch(kind).then(()=>{if(pre[kind])say('Готово, нажмите ещё раз.');else say(preErr[kind]||'сервер не ответил',true);});}
function login(){const b=pre.login;if(!b){retry('login');return;}pre.login=null;hint('');say('Ждём ключ…');
 navigator.credentials.get(fixGet(b.options)).then(cred=>j('/api/auth/login/finish?ceremony='+b.ceremony,{method:'POST',headers:{'Content-Type':'application/json'},body:pack(cred)}))
 .then(()=>done('Вход выполнен.')).catch(e=>{say(e.message,true);prefetch('login');});}
function register(){const b=pre.register;const name=document.getElementById('name').value;if(!b){retry('register');return;}pre.register=null;say('Ждём ключ…');
 navigator.credentials.create(fixCreate(b.options)).then(cred=>j('/api/auth/register/finish?ceremony='+b.ceremony+'&name='+encodeURIComponent(name),{method:'POST',headers:{'Content-Type':'application/json'},body:pack(cred)}))
 .then(()=>done('Ключ заведён, вход выполнен.')).catch(e=>{
  // «The object is in an invalid state» человеку ничего не говорит, а
  // по сути это не поломка: хранитель отвечает «такой ключ у меня уже
  // есть» (1 октября 2026 у владельца он приехал на iPhone сам, из связки)
  if(e&&e.name==='InvalidStateError')ключУжеЕсть();else say(e.message,true);
  prefetch('register');});}
// Ключ Сёмы для этого имени сервер кладёт в список «такие уже есть», а
// синхронизированный ключ (связка iCloud, менеджер паролей) приходит на
// устройство сам, и второго хранитель не заведёт. Выход один — войти тем,
// что есть. Входа тем же нажатием не запускаем: Safari отдаёт ключ только
// по свежему касанию, и вход без него молча упал бы. Поэтому говорим, что
// случилось, и ставим кнопку входа перед глазами, параметры церемонии
// берём заранее — как перед любым другим касанием.
function ключУжеЕсть(){say('');const go=document.getElementById('go');
 if(!go){hint('На этом устройстве такой ключ Сёмы уже есть — заводить второй не нужно.');return;}
 hint('На этом устройстве уже есть ключ Сёмы — войдите им: нажмите «Войти ключом». Заводить второй не нужно.');
 if(!pre.login)prefetch('login');
 go.focus();}
// Что человек видит, когда вошёл фразой снаружи: сюда же его приводит
// клиент, когда экран не открылся (сессия по фразе экранов не отдаёт). Слова
// те же в обоих местах — это одно состояние, а не два. Дорог две, и какая
// нужна, страница знать не может: ключ Сёмы мог уже приехать на устройство
// сам (связка iCloud, менеджер паролей) — тогда им входят, а заводить второй
// хранитель откажется (1 октября 2026 именно так: InvalidStateError). Не
// приехал — заводят. Поэтому под урезанной сессией обе кнопки на месте.
const ключНужен='<div class="note"><b>Нужен ключ устройства (passkey).</b><br>Вы вошли контрольной фразой снаружи, а она пускает только для этого: письма и экраны ею не открываются. Если ключ Сёмы уже есть в вашей связке iCloud или в менеджере паролей — войдите им. Если нет — заведите: дальше Сёма будет пускать этим ключом, и доступ станет полным.</div>';
// Код первой установки — та же урезанная сессия, но ключей у Сёмы ещё нет
// вовсе: войти нечем, дорога одна — завести первый
const ключНуженКод='<div class="note"><b>Заведите первый ключ устройства (passkey).</b><br>Вы вошли кодом первой установки, а он пускает только для этого: письма и экраны им не открываются. Заведите ключ — дальше Сёма будет пускать им, доступ станет полным, а код погаснет.</div>';
// «Войти ключом» — только когда ключи у Сёмы вообще есть: без них вход
// заведомо отвечает «ключей ещё нет», и кнопка была бы ловушкой
function ключНуженВид(st){document.getElementById('login').innerHTML=(st.bySetup?ключНуженКод:ключНужен)+(st.passkeys>0?'<button id="go">Войти ключом</button>':'');
 document.getElementById('reg').style.display='flex';}
// Код первой установки — пока ключей нет, а страница открыта не с самой
// машины (в Docker браузер хозяина приходит из сети моста). Код Сёма печатает
// при старте вместе с адресом входа; после входа им — сразу форма ключа, как
// после фразы снаружи: экраны такая сессия не открывает
function кодБлок(st){const box=document.getElementById('setupBox'),поле=document.getElementById('setup'),кнопка=document.getElementById('setupGo');
 box.style.display='flex';
 const войти=async()=>{const v=поле.value.trim();if(!v){say('Введите код из журнала Сёмы целиком',true);return;}
  кнопка.disabled=true;say('Проверяем код…');
  try{await j('/api/auth/setup',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:v})});
   поле.value='';box.style.display='none';say('');
   ключНуженВид(Object.assign({},st,{bySetup:true}));
   await prefetch('register');
  }catch(e){say(e.message,true);}finally{кнопка.disabled=false;}};
 кнопка.onclick=войти;
 поле.addEventListener('keydown',e=>{if(e.key==='Enter')войти();});}
// Вход фразой — для устройства, на котором ключа ещё нет. Снаружи такая
// сессия даёт ровно одно: завести passkey. Поэтому после входа снаружи не
// уводим на экраны — человек получил бы отказ и решил, что фраза не
// сработала; вместо этого сразу показываем форму ключа.
function фразаБлок(st){const t=document.getElementById('phraseToggle'),box=document.getElementById('phraseBox'),поле=document.getElementById('phrase');
 if(!st.phrase||st.kind==='session'){t.style.display='none';return;}
 document.getElementById('phraseNote').textContent=st.local
  ?'Этот адрес Сёма считает своей сетью: фраза открывает его полностью.'
  :'Снаружи фраза даёт ровно одно — завести ключ на этом устройстве, и ничего больше. Фразу можно выманить поддельной страницей или подсмотреть через плечо, а ключ привязан к адресу и с устройства не увозится.';
 document.getElementById('phraseShow').onclick=()=>{t.style.display='none';box.style.display='flex';поле.focus();};
 const кнопка=document.getElementById('phraseGo');
 const войти=async()=>{const v=поле.value.trim();if(!v){say('Введите фразу целиком, группами по четыре знака',true);return;}
  кнопка.disabled=true;say('Проверяем фразу…');
  try{const r=await j('/api/auth/phrase',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({phrase:v})});
   поле.value='';
   if(!r.restricted){done('Вход по фразе выполнен.');return;}
   box.style.display='none';say('');
   ключНуженВид(st);
   // страница уже подключила кнопки при открытии, а эту дорисовали сейчас
   const go=document.getElementById('go');if(go){go.onclick=login;prefetch('login');}
   await prefetch('register');
  }catch(e){say(e.message,true);}finally{кнопка.disabled=false;}};
 кнопка.onclick=войти;
 поле.addEventListener('keydown',e=>{if(e.key==='Enter')войти();});}
function done(t){document.getElementById('login').innerHTML='<div class="ok">'+t+'</div>';document.getElementById('reg').style.display='none';
 say('Готово, открываю Сёму…');
 const назад=куда()||'/';setTimeout(()=>location.replace(назад),600);}
(async()=>{const st=await j('/api/auth/state');
 // passkey нельзя привязать к IP-адресу: если страница открыта по 127.0.0.1, уходим на адрес из настроек
 if(/^\d+\.\d+\.\d+\.\d+$/.test(location.hostname)&&st.origin){location.replace(st.origin.replace(/\/$/,'')+location.pathname+location.search);return;}
 document.getElementById('rp').textContent=st.rpId+' · вход по passkey';
 // раньше проверок про passkey: на чужом имени и без https ключ не
 // работает вовсе, а фраза из своей сети работает и там
 фразаБлок(st);
 // Passkey работает только на своём имени и только по защищённому
 // соединению (исключение — localhost). Кнопка, которая заведомо не
 // сработает, хуже честного объяснения: человек три раза нажмёт и решит,
 // что сломалась Сёма
 const своё = location.hostname === st.rpId || location.hostname.endsWith('.' + st.rpId);
 const защищено = window.isSecureContext;
 if (!своё || !защищено) {
  const беды = [];
  if (!своё) беды.push('ключи заведены для «' + st.rpId + '», а страница открыта на «' + location.hostname + '»');
  if (!защищено) беды.push('нет защищённого соединения: браузер разрешает passkey только по https (кроме localhost)');
  document.getElementById('login').innerHTML =
   '<div class="note"><b>Здесь войти по passkey нельзя.</b><br>' + беды.join('.<br>') + '.' +
   '<br><br>Откройте Сёму по адресу <b>' + (st.origin || '') + '</b>' +
   (защищено ? '' : ' или настройте https — тогда вход заработает и с телефона.') + '</div>';
  document.getElementById('reg').style.display = 'none';
  return;
 }
 if(st.localOpen&&!st.local){document.getElementById('login').insertAdjacentHTML('afterbegin','<div class="note">'+(st.localPrivate
  ?'Из частной сети Сёма пускает без входа, пока на «Вход и доступ» включено «Пускать без проверки личности»; этот адрес пришёл иначе — нужен passkey.'
  :'Без входа Сёма пускает только запросы с самой машины; этот адрес пришёл иначе — нужен passkey.')+'</div>');}
 const canReg=st.canRegister;
 if(st.kind==='session'&&st.restricted){
  // уводить такую сессию на экран нельзя: она их не открывает, и человек
  // увидел бы отказ вместо объяснения. Проверка первая: ключей может не
  // быть вовсе (первый заводят как раз фразой), и тогда общая подсказка
  // «ключей ещё нет» заслонила бы то, что нужно сделать сейчас
  ключНуженВид(st);
 }
 else if(st.passkeys===0){
  // откуда завести первый ключ: с самой машины — прямо здесь; иначе — кодом
  // первой установки, который Сёма напечатал при старте. Кода нет (истёк
  // срок) — говорим, как получить новый, а не рисуем поле, которое откажет
  const как=st.canRegister?' Заведите первый прямо здесь.'
   :st.setup?' Первый ключ заводится с самой машины (localhost) или кодом первой установки: Сёма напечатал его при старте вместе с адресом входа — в Docker это <b>docker logs</b>.'
   :' Первый ключ заводится с самой машины (localhost) или кодом первой установки, но сейчас кода нет: он печатается при старте, пока ключей нет, и живёт час. Перезапустите Сёму и возьмите новый код из его журнала.';
  document.getElementById('login').innerHTML='<div class="note">Ключей ещё нет.'+как+'</div>';
  if(!st.canRegister&&st.setup)кодБлок(st);
 }
 else if(st.kind==='session'){
  // уже вошли: страница нужна только чтобы завести ещё один ключ или выйти
  // пришли за экраном, а сессия уже есть — незачем показывать вход
  const назад=куда();if(назад){location.replace(назад);return;}
  document.getElementById('login').innerHTML='<div class="ok">Вы вошли.</div><button class="plain" id="more">Завести ещё один ключ</button><button class="plain" id="out">Выйти</button>';
  document.getElementById('more').onclick=()=>{document.getElementById('reg').style.display='flex';document.getElementById('more').style.display='none';};
  document.getElementById('out').onclick=async()=>{await fetch('/api/auth/logout',{method:'POST'});location.reload();};
 }
 else if(want){document.getElementById('msg').textContent='Завести ключ отсюда нельзя: нужна открытая сессия или включённое «С этой машины без входа» на экране «Вход и доступ».';}
 if(canReg&&(st.passkeys===0||st.kind!=='session')){document.getElementById('reg').style.display='flex';}
 const go=document.getElementById('go');if(go){go.onclick=login;prefetch('login');}const mk=document.getElementById('mk');if(mk){mk.onclick=register;if(canReg)prefetch('register');}})();
</script></body></html>`

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(loginPage))
}

// ---- экран «Доступ» ----

// Внутренние вкладки «Доступа». Ключи устройств, сессии, токены для
// программ, доверенная сеть и журнал обращений лежали одной лентой на
// семь экранов прокрутки телефона. Вход, токен и сеть — три разных
// решения, и опасное среди них ровно одно: кого пускать без входа.
//
// 2 октября 2026 ключи и сессии разъехались: «Ключи» — чем входят, «Кто
// внутри» — кто вошёл сейчас; три абзаца инструкции стали одной свёрнутой
// строкой «Как это устроено» у каждой вкладки. Только раскладка: вход и
// доступ работают как работали, адреса прежних вкладок открываются.
const (
	accessTabKeys    = ""        // Ключи устройств, фраза входа
	accessTabInside  = "inside"  // Кто внутри: действующие сессии
	accessTabTokens  = "tokens"  // Токены для программ
	accessTabNetwork = "network" // Доверенная сеть: кого пускать без входа
	accessTabLog     = "log"     // Кто стучался
)

// accessLogPage — сколько строк журнала показывается с первого раза.
const accessLogPage = 20

// Отборы журнала доступа.
const (
	accessFilterAll    = "all"
	accessFilterLogins = "logins"
	accessFilterDenied = "denied"
	accessFilterTokens = "tokens"
)

// accessFilters — чипы журнала по порядку: что считается входом, отказом и
// обращением по токену, решают данные журнала, а не вкус экрана.
var accessFilters = []struct {
	ID, Title string
	Match     func(*store.AccessEntry) bool
}{
	{accessFilterAll, "Все", func(*store.AccessEntry) bool { return true }},
	{accessFilterLogins, "Входы", func(e *store.AccessEntry) bool {
		return e.Method == "LOGIN" || e.Method == "REGISTER" || e.Method == "PHRASE"
	}},
	{accessFilterDenied, "Отказы", func(e *store.AccessEntry) bool { return e.Status >= 400 }},
	{accessFilterTokens, "Токены", func(e *store.AccessEntry) bool { return strings.HasPrefix(e.Who, "token:") }},
}

func (s *Server) buildAccess(doc *Document, tab, filter string, limit int) error {
	doc.Heading = sectionTitle[accessScreen]
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	st := s.eng.Store()

	keys, _ := st.Passkeys()
	sessions, _ := st.Sessions()
	entries, _ := st.AccessLog(200)
	doc.Subheading = accessByPhrase(s, keys)

	tokens, _ := st.Tokens()
	живых := 0
	for _, t := range tokens {
		if t.Revoked.IsZero() {
			живых++
		}
	}
	tabs := []innerTab{
		{ID: accessTabKeys, Name: "Ключи", Count: strconv.Itoa(len(keys))},
		{ID: accessTabInside, Name: "Кто внутри", Count: strconv.Itoa(len(sessions))},
		{ID: accessTabTokens, Name: "Токены", Count: strconv.Itoa(живых)},
		{ID: accessTabNetwork, Name: "Доверенная сеть"},
		{ID: accessTabLog, Name: "Кто стучался"},
	}
	// снаружи войти нечем — это не мелочь: ключ заводится только с самой
	// машины, и узнать об этом в поезде поздно
	if len(keys) == 0 {
		tabs[0].Count, tabs[0].Level = "нет", LevelWarn
	}
	if n := deniedCount(entries); n > 0 {
		tabs[4].Count, tabs[4].Level = strconv.Itoa(n)+" отбито", LevelWarn
	}
	setSubtabs(doc, accessScreen, tab, tabs)

	switch tab {
	case accessTabInside:
		s.accessInside(add, sessions, keys)
		return nil
	case accessTabTokens:
		s.programTokens(add, st, tokens, живых)
		return nil
	case accessTabNetwork:
		s.trustedNetwork(add, keys, entries)
		return nil
	case accessTabLog:
		s.accessJournal(doc, entries, filter, limit)
		return nil
	}

	doc.ToolbarActions = []Action{{Action: "$login", Target: "register", Label: "Завести ключ"}}

	var rows []Component
	for _, k := range keys {
		rows = append(rows, s.passkeyRow(k))
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Ключей устройств пока нет",
			"Ключ заводится на странице входа в браузере — его сохранит Bitwarden, связка iCloud или что у вас настроено. Первый ключ можно завести только с этой машины", "key"))
	}
	// «Завести ключ» — единственная кнопка шапки вкладки; второй такой же в
	// заголовке раздела больше нет
	add(Component{Type: "section", ID: "access-keys", Title: fmt.Sprintf("Ключи устройств · %d", len(keys)), Children: rows})

	s.loginPhrase(add, keys)
	howOrigin := s.originMigration(add, keys)
	// три абзаца инструкции — одна свёрнутая строка: пароля нет вовсе, как
	// набирается фраза и к чему привязаны ключи
	add(accessHow("access-keys-how",
		"Пароля у Сёмы нет вовсе — только ключ устройства. Первый заводится с самой машины, дальше — уже войдя. "+
			"Потеряли все — sema-server -reset-auth с самой машины.\n\n"+
			"Фраза набирается на странице входа кнопкой «Нет ключа? Войти контрольной фразой». Регистр, пробелы и дефисы не важны, "+
			"опечатка узнаётся по контрольной группе и отличается от неверной фразы. Каждая неудача видна в «Кто стучался».\n\n"+howOrigin))
	return nil
}

// accessHow — «Как это устроено»: справка вкладки одной свёрнутой строкой, а не
// абзацами между списками.
func accessHow(id, text string) Component {
	return Component{Type: "section", ID: id, Title: "Как это устроено", Collapsed: true,
		Children: []Component{{Type: "text", Style: "secondary", Text: text}}}
}

// passkeyRow — тонкая строка ключа: имя, три факта (когда заведён, когда входили
// последний раз, для какого имени сервера), «···» с удалением. Переименовать —
// полем в раскрытии, а не вопросом в окне; удаление — с подтверждением: ключ
// пропадает, и его сессии закрываются сразу.
func (s *Server) passkeyRow(k *store.Passkey) Component {
	id := strconv.FormatInt(k.ID, 10)
	last := "ещё не входили"
	if !k.LastUsed.IsZero() {
		last = k.LastUsed.Format("02.01 15:04")
	}
	facts := []Prop{{Label: "Заведён", Value: k.Created.Format("02.01.2006")}, {Label: "Последний вход", Value: last}}
	row := Component{Type: "row", Layout: "wide", ID: "passkey-" + id, Subtitle: k.Name,
		Expandable: true, Expand: "Название",
		Menu: []Action{{Action: "delete_passkey", Target: id, Label: "Удалить…", Style: "destructive",
			Confirm:   "Удалить passkey «" + k.Name + "»? Вход этим ключом перестанет работать, и его сессии закроются сразу.",
			ConfirmOK: "Удалить"}},
		Children: []Component{{Type: "form", ID: "passkey-form-" + id, Style: formRows, Fields: []Field{
			{Type: "text", Name: "value", Label: "Название ключа", Value: k.Name,
				Hint: "как вы зовёте это устройство: «MacBook», «iPhone»"},
		}, Submit: &Action{Action: "rename_passkey", Target: id, Label: "Переименовать"}}}}
	// для какого имени сервера заведён ключ: после переезда на домен по этой
	// строке видно, какие ключи ещё пускают, а какие остались на прежнем адресе.
	// Пусто у заведённых до того, как Сёма стал помнить
	switch {
	case k.RPID == "":
		facts = append(facts, Prop{Label: "Адрес", Value: "неизвестен"})
	case !strings.EqualFold(k.RPID, s.auth.RPID()):
		facts = append(facts, Prop{Label: "Адрес", Value: "«" + k.RPID + "», а вход теперь на «" + s.auth.RPID() + "»"})
		withLevel(&row, LevelWarn)
	default:
		facts = append(facts, Prop{Label: "Для", Value: "«" + k.RPID + "»"})
	}
	row.Facts = facts
	return row
}

// accessInside — «Кто внутри»: сессии тонкими строками с «Завершить»; «Выйти
// везде» — в «Опасном», с подтверждением. Нового действия нет: `logout_session` и
// `logout_all` те же, что были.
func (s *Server) accessInside(add func(Component), sessions []*store.Session, keys []*store.Passkey) {
	s.accessShelf(add, sessions)
	s.sessionsSection(add, sessions, keys)
	if len(sessions) > 0 {
		add(Component{Type: "section", ID: "access-danger", Title: "Опасное", Style: "danger", Children: []Component{{
			Type: "row", Layout: "wide", ID: "danger-logout-all", Subtitle: "Завершить все сессии",
			Caption: "Всем устройствам, включая этот браузер, придётся войти заново",
			Actions: []Action{{Action: "logout_all", Label: "Завершить все…", Style: "destructive",
				Confirm: "Завершить все сессии? Всем устройствам, включая этот браузер, придётся войти заново.", ConfirmOK: "Завершить все"}}}}})
	}
}

// accessJournal — «Кто стучался»: журнал последней вкладкой — чипы отбора с
// числами, первая порция в двадцать строк и «Показать ещё». Хроника, а не
// настройка: на первом экране раздела её нет, она приезжает только с вкладкой.
func (s *Server) accessJournal(doc *Document, entries []*store.AccessEntry, filter string, limit int) {
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	// у журнала есть полоса чипов, но нет своего поиска по строкам: верхнее поле
	// «ищи по настройкам» на телефоне ему не нужно
	doc.noPhoneSearch = true
	active, match := accessFilterAll, accessFilters[0].Match
	for _, f := range accessFilters {
		n := 0
		for _, e := range entries {
			if f.Match(e) {
				n++
			}
		}
		doc.Filters = append(doc.Filters, Filter{ID: f.ID, Title: fmt.Sprintf("%s · %d", f.Title, n)})
		if f.ID == filter {
			active, match = f.ID, f.Match
		}
	}
	doc.ActiveFilter = active
	s.accessForeign(add, entries)

	var picked []*store.AccessEntry
	for _, e := range entries {
		if match(e) {
			picked = append(picked, e)
		}
	}
	rows := accessLog(picked)
	total := len(rows)
	shown := total
	if page := matrixPage(limit, accessLogPage); total > page {
		shown = page
		rows = rows[:page]
	}
	switch {
	case len(rows) == 0 && active == accessFilterAll:
		rows = append(rows, emptyState("Никто ещё не стучался",
			"Сюда попадают входы, запросы по токенам и отказы — по одной строке на обращение; одинаковые сворачиваются в одну со счётчиком", "list.bullet"))
	case len(rows) == 0:
		rows = append(rows, emptyState("Под этот отбор ничего не подошло", "Выберите «Все» — там вся хроника за двое суток", "list.bullet"))
	}
	add(Component{Type: "section", ID: "access-journal", Title: "Кто стучался · за двое суток", Children: rows})
	if shown < total {
		add(moreRows(shown, total))
	}
	add(accessHow("access-log-how",
		"Одинаковые обращения свёрнуты в одну строку со счётчиком и временем: пятнадцать строк «GET /ui» подряд ничего не рассказывают. "+
			"Удачные обращения с этой машины сюда не пишутся вовсе — иначе журнал состоял бы из них одних."))
}

// loginPhrase — запасной вход с устройства, на котором ключа ещё нет:
// телефон в дороге, консоли нет. Фразу придумывает Сёма; человеку её
// показывают один раз, а хранится она медленным хэшем — из базы не узнать.
func (s *Server) loginPhrase(add func(Component), keys []*store.Passkey) {
	var rows []Component
	created, has := s.auth.PhraseCreated()
	if has {
		row := Component{Type: "row", Layout: "wide", ID: "фраза", Level: LevelOK,
			Subtitle: "Фраза входа заведена",
			Caption: "Из своей сети она открывает Сёму целиком. Снаружи — только заведение ключа на новом устройстве: " +
				"фразу можно выманить поддельной страницей, а ключ с устройства не увозится",
			Facts: []Prop{{Label: "Заведена", Value: created.Format("02.01.2006")}},
			Actions: []Action{
				{Action: "new_phrase", Label: "Сменить", Style: "primary",
					Confirm:   "Сменить фразу входа? Прежняя перестанет пускать сразу же, новая покажется один раз — запишите её, прежде чем закрыть экран.",
					ConfirmOK: "Сменить"},
				{Action: "revoke_phrase", Label: "Отозвать", Style: "destructive",
					Confirm:   "Отозвать фразу? Войти с нового устройства станет нечем: только с самой машины или sema-server -reset-auth с консоли.",
					ConfirmOK: "Отозвать"},
			}}
		rows = append(rows, row)
	} else {
		// «фразы нет» — это не пустое место, а состояние, в котором человек
		// снаружи заперт; узнать об этом в поезде поздно
		row := Component{Type: "row", Layout: "wide", ID: "фраза", Level: LevelWarn,
			Subtitle: "Фразы входа нет: с нового устройства войти нечем",
			Caption: "Ключ заводится только с самой машины или из открытой сессии. Если телефон в дороге, а ключа на нём нет, " +
				"без фразы остаётся консоль. Заведите её заранее — особенно перед сменой адреса входа",
			Actions: []Action{{Action: "new_phrase", Label: "Завести фразу", Style: "primary"}}}
		if len(keys) == 0 {
			row.Level = LevelAlert
		}
		rows = append(rows, row)
	}
	add(Component{Type: "section", Title: "Фраза входа · запасной путь", Children: rows})
	// свежая фраза видна те же три минуты, что и новый токен: экран
	// перерисовывается сам, и «ровно один раз» человек не успел бы прочесть
	if p, свежая := s.auth.LastPhrase(); свежая {
		add(Component{Type: "text", Icon: "key",
			Text: "Новая фраза показывается три минуты — запишите её сейчас, второго способа узнать нет:\n" + p})
	}
	// предел попыток — «Тонкая настройка» вкладки (settings_rare.go); действие
	// сохранения прежнее, поведение входа от переезда поля не меняется
	add(Component{Type: "form", ID: "phrase-limit", Title: "Предел попыток", Tier: tierRare, Style: formRows, Fields: []Field{
		s.settingField(auth.SettingPhraseLimit, auth.SettingPhraseLimit, "Неудач подряд до запирания",
			"Считается по адресу, откуда пришли; после предела вход запирается на минуту, пять, полчаса, два часа — "+
				"и говорит, когда откроется. Успешный вход обнуляет счёт. Предел действует и в своей сети: шлюза там нет", ""),
	}, Submit: &Action{Action: "save_auth", Label: "Сохранить"}})
}

// originMigration — ключи привязаны к имени сервера браузером, и перенести
// их нельзя ничем. Поэтому порядок переезда написан заранее, а расхождение
// «ключ заведён для одного имени, а вход теперь на другом» показывается,
// не дожидаясь, пока человек обнаружит его снаружи.
func (s *Server) originMigration(add func(Component), keys []*store.Passkey) string {
	rp := strings.ToLower(s.auth.RPID())
	чужих, неизвестных := 0, 0
	for _, k := range keys {
		switch {
		case k.RPID == "":
			неизвестных++
		case !strings.EqualFold(k.RPID, rp):
			чужих++
		}
	}
	порядок := "Порядок переезда: 1) завести фразу входа и записать её; 2) сменить адрес; 3) на новом адресе завести ключи заново; " +
		"4) только потом снять прежние."
	// спокойное состояние — абзац справки, а не отдельная строка на экране;
	// предупреждение о ключах с чужого адреса ниже остаётся видимым
	text := "Ключи привязаны к имени сервера «" + s.auth.RPID() + "»: при смене адреса входа они перестанут пускать, и перенести их нельзя ничем. " + порядок
	if неизвестных > 0 {
		text += " " + plural(неизвестных, "ключ", "ключа", "ключей") + " заведён до того, как Сёма начал запоминать адрес, — для какого имени, неизвестно."
	}
	if чужих == 0 {
		return text
	}
	row := Component{Type: "row", Layout: "wide", ID: "переезд", Level: LevelWarn,
		Subtitle: plural(чужих, "ключ заведён", "ключа заведены", "ключей заведены") + " для другого имени сервера",
		Caption:  "Вход теперь на «" + s.auth.RPID() + "», и эти ключи браузер к нему не применит. " + порядок,
		Actions:  []Action{{Action: "$login", Target: "register", Label: "Завести ключ", Style: "primary"}}}
	if !s.auth.HasPhrase() {
		// самый тяжёлый случай: ключи от прежнего имени, нового нет, фразы
		// нет — войти снаружи нечем вовсе
		row.Level = LevelAlert
		row.Caption = "Вход теперь на «" + s.auth.RPID() + "», эти ключи к нему не применятся, а фразы входа нет: " +
			"снаружи войти нечем вовсе. Заведите фразу и ключ на новом адресе, не закрывая нынешнюю сессию. " + порядок
	}
	add(Component{Type: "section", Title: "Адрес входа сменился", Children: []Component{row}})
	return text
}

// programTokens — ключи для программ без браузера: что выдано, кому и
// как этим пользовались.
func (s *Server) programTokens(add func(Component), st *store.Store, tokens []*store.APIToken, живых int) {
	var rows []Component
	week := time.Now().Add(-7 * 24 * time.Hour)
	for _, t := range tokens {
		id := strconv.FormatInt(t.ID, 10)
		var badges []Badge
		for _, sc := range t.Scopes {
			badges = append(badges, Badge{Text: scopeTitle(sc)})
		}
		req, denied, bytes := st.AccessStats("token:"+t.Name, week)
		meta := fmt.Sprintf("выдан %s · за неделю %s", t.Created.Format("02.01"), plural(req, "запрос", "запроса", "запросов"))
		if denied > 0 {
			meta += fmt.Sprintf(", отказов %d", denied)
		}
		if bytes > 0 {
			meta += " · " + humanBytes(bytes)
		}
		if !t.LastUsed.IsZero() {
			meta += " · последний " + t.LastUsed.Format("02.01 15:04")
		} else {
			meta += " · им ещё не пользовались"
		}
		row := Component{Type: "row", Layout: "wide", ID: "token-" + id, Subtitle: t.Name, Meta: meta, Badges: badges}
		if !t.Revoked.IsZero() {
			row.Style = "secondary"
			row.Badges = append(row.Badges, Badge{Text: "отозван " + t.Revoked.Format("02.01")})
		} else {
			row.Actions = []Action{{Action: "revoke_token", Target: id, Label: "Отозвать", Style: "destructive", Confirm: "Отозвать токен «" + t.Name + "»? Программы с ним получат 401.", ConfirmOK: "Отозвать"}}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Токенов пока нет",
			"Токен нужен программе, которая ходит к Сёме по сети без браузера. Claude Code и Codex обходятся без него — они ходят через жителя машины", "key"))
	}
	add(Component{Type: "section", ID: "access-tokens", Title: fmt.Sprintf("Токены для программ · %d", живых), Children: rows})
	// новый токен показывается недолго: экран перерисовывается сам, и
	// «ровно один раз» человек не успел бы скопировать
	s.mu.Lock()
	lastToken, fresh := s.lastToken, time.Since(s.lastTokenAt) < 3*time.Minute
	if !fresh {
		s.lastToken = ""
	}
	s.mu.Unlock()
	if lastToken != "" && fresh {
		add(Component{Type: "text", Icon: "key", Text: "Новый токен показывается три минуты, скопируйте:\n" + lastToken})
	}
	var fields []Field
	fields = append(fields, Field{Type: "text", Name: "name", Label: "Название", Hint: "например, claude-code"})
	for _, sc := range auth.Scopes {
		fields = append(fields, Field{Type: "toggle", Name: "scope_" + sc.ID, Label: sc.Title, Value: boolValue(sc.ID == "memory")})
	}
	add(Component{Type: "form", ID: "add-token", Title: "Новый токен", Fields: fields, Submit: &Action{Action: "add_token", Label: "Выдать", Style: "primary"}})
	add(accessHow("access-tokens-how",
		"Токен идёт в заголовке Authorization: Bearer; в базе хранится только хэш, показывается он один раз. Каждый запрос — строка в журнале «Кто стучался»; расход модели — на «Здоровье»."))
}

// trustedNetwork — кого Сёма пускает без входа и как отличает своих.
//
// Наверху итог, а не пересказ тумблеров: «своей считается и частная
// сеть: да» не говорит ничего, а «без входа доступна вся частная сеть,
// имена такие-то» говорит. Опасное стоит отдельным блоком со своей
// кнопкой: переключатель, выкидывающий хозяина из Сёмы, не должен
// уезжать вместе с «переименовать ключ» одним нажатием «Сохранить».
func (s *Server) trustedNetwork(add func(Component), keys []*store.Passkey, entries []*store.AccessEntry) {
	открыт, частная := s.auth.LocalOpen(), auth.LocalPrivate.Load()
	итог := Component{Type: "row", Layout: "wide", ID: "сеть-итог", Level: LevelOK}
	switch {
	case !открыт:
		итог.Subtitle = "Без входа не пускается никто"
		итог.Caption = "вход по ключу устройства нужен всем, включая эту машину"
	case частная:
		// зелёным это помечать нельзя, даже когда всё настроено так, как
		// человек хотел. «Работает как задумано» и «безопасно» — разные
		// утверждения, а доступ без проверки личности это состояние, о
		// котором надо помнить, а не галочка о порядке
		итог.Level = LevelWarn
		итог.Subtitle = "Доступ без проверки личности: эта машина и вся частная сеть"
		итог.Caption = "частной считается 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 и петля. " +
			"Сёма не спрашивает, кто пришёл: войдёт любой человек, телефон, программа или чужое устройство, " +
			"оказавшееся в этих сетях, — включая гостя по вашему Wi-Fi"
	default:
		итог.Level = LevelInfo
		итог.Subtitle = "Доступ без проверки личности: только сама машина"
		итог.Caption = "с самого сервера Сёма не спрашивает, кто пришёл; остальные, включая домашний мак, входят по ключу устройства"
	}
	if открыт && len(keys) == 0 {
		итог.Level = LevelWarn
		итог.Caption += " · ключей устройств нет: выключите «без входа» — и войти станет нечем"
	}
	итог.Facts = []Prop{{Label: "Разрешённые имена", Value: namesLine(s)}}
	// цифрами приходят всегда: подмена имени живёт на именах, а не на числах
	итог.Facts = append(итог.Facts, Prop{Label: "Адреса числом", Value: "разрешены всегда"})
	if n := deniedCount(entries); n > 0 {
		итог.Facts = append(итог.Facts, Prop{Label: "Отбито за двое суток", Value: plural(n, "попытка", "попытки", "попыток")})
	}
	итог.Actions = []Action{{Action: "$goto", Target: accessScreen + "~" + accessTabLog, Label: "Кто стучался"}}
	add(Component{Type: "section", Title: "Сейчас без входа пускаются", Children: []Component{итог}})
	add(accessHow("access-network-how",
		"Удачное обращение с этой машины в журнал не пишется — там видны только запросы по токенам и отказы. "+
			"Поэтому «в журнале пусто» значит «никого не отбивали», а не «никто не приходил»."))

	// имена, RP ID и адрес входа — редкие: их меняют при переезде на другой
	// адрес, а не каждый день; уходят в «Тонкую настройку» этой вкладки.
	// Тумблеры остаются наверху, действие сохранения общее — прежнее
	hostsField := s.settingField(auth.SettingLocalHosts, auth.SettingLocalHosts, "Имена сервера из своей сети",
		"Через запятую, кроме адреса входа: под чужим именем Сёма не отвечает даже своей сети (защита от подмены DNS); адреса числом разрешены всегда", "")
	hostsField.Value = strings.Join(s.auth.LocalHosts(), ", ") // нормализованный список, как показывалось и раньше
	originField := s.settingField(auth.SettingOrigin, auth.SettingOrigin, "Адрес страницы входа (origin)",
		"Как в браузере, со схемой и портом; меняется тем же порядком, что и имя сервера — ключи прежнего адреса пускать перестанут. Умолчание выводится из адреса, где Сёма слушает", "")
	originDefault := s.auth.OriginDefault() // пока адрес не назван, он выводится, а не пуст
	originField.Value, originField.Default = s.auth.Origin(), &originDefault
	rpField := s.settingField(auth.SettingRPID, auth.SettingRPID, "Имя сервера для passkey (RP ID)",
		"localhost на маке; на Pi — имя из Tailscale. Ключи привязаны к имени браузером, и перенести их нельзя ничем: "+
			"сначала заведите фразу входа, потом меняйте имя, потом заводите ключи заново и только потом снимайте прежние", "")
	add(Component{Type: "form", ID: "auth-settings", Title: "Как Сёма узнаёт своих", Style: formRows, Fields: []Field{
		rare(hostsField),
		{Type: "toggle", Name: auth.SettingTrustProxy, Label: "За своим прокси", Hint: "верить X-Forwarded-For: включайте только когда перед Сёмой стоит ваш прокси, иначе адрес подделывается заголовком", Value: boolValue(trustProxy.Load())},
		{Type: "toggle", Name: auth.SettingUserVerification, Label: "Требовать проверку владельца ключа",
			Hint: "включено по умолчанию: хранитель ключа спрашивает отпечаток, лицо или код устройства при каждом входе — " +
				"иначе найденный телефон войдёт без человека. Ключ, заведённый хранителем без такой проверки, входить перестанет; " +
				"если заперло — войдите контрольной фразой и выключите",
			Value: boolValue(s.auth.UserVerificationMode() == "required"), key: auth.SettingUserVerification},
		rare(rpField),
		rare(originField),
	}, Submit: &Action{Action: "save_auth", Label: "Сохранить"}})

	// свои кнопки и свой текст последствия: эти два тумблера
	// **отключают проверку личности** и действуют сразу.
	//
	// Здесь стояло «открывают Сёму людям, а не программам». Это неправда:
	// открытая сеть не отличает человека от программы и никого ни о чём
	// не спрашивает — в неё входит всё, что до неё дотянулось.
	последствие := fmt.Sprintf("Изменение подействует сразу и может выкинуть вас из Сёмы, если ключа устройства под рукой нет. Ключей заведено %d.", len(keys))
	if п := lastLogin(keys); п != "" {
		последствие += " Последний вход по ним — " + п + "."
	}
	openField := s.settingField(auth.SettingLocalOpen, auth.SettingLocalOpen, "Пускать без проверки личности",
		"включено — с этой машины (и, если ниже разрешено, из всей частной сети) Сёма не спрашивает, кто пришёл. "+
			"Выключите — ключ устройства понадобится всем, включая вас здесь. У новой установки выключено; для Pi за прокси так и надо", "")
	add(Component{Type: "form", ID: "auth-danger", Title: "Опасное · сохраняется отдельно", Fields: []Field{openField},
		Submit: &Action{Action: "save_auth_danger", Label: "Применить", Style: "destructive"}})
	add(s.privateNetRow(открыт, частная, len(keys)))
	add(Component{Type: "text", Style: "secondary", Text: последствие})
}

// privateNetRow — «Считать своей всю частную сеть»: строка с тумблером и
// вопросом, а не поле формы. Поле уходило бы вместе с полосой сохранения
// без единого слова, а включение отдаёт Сёму целой сети, и человек должен
// прочесть, кому именно, до того как это случилось (open-source, решение 5).
// Выключение тоже спрашивает: устройства из домашней сети разом останутся
// без входа, и это надо знать заранее.
func (s *Server) privateNetRow(открыт, частная bool, ключей int) Component {
	row := Component{Type: "row", Layout: "wide", ID: "auth-local-scope", Subtitle: "Считать своей всю частную сеть",
		Toggle: boolToggle(частная), key: auth.SettingLocalScope}
	toggle := Action{Action: "toggle_local_scope", Label: "toggle"}
	if частная {
		row.Level = LevelWarn
		row.Caption = "сейчас своей считается вся частная сеть: 10/8, 172.16/12, 192.168/16 — соседний компьютер, телефон гостя по Wi-Fi, контейнеры на этой машине"
		toggle.Confirm = "Своей останется только сама машина. Телефон и компьютеры из домашней сети будут входить по ключу устройства"
		if ключей == 0 {
			toggle.Confirm += " — а ключей пока нет ни одного: войти с них станет нечем, пока не заведёте ключ с самой машины или кодом первой установки."
		} else {
			toggle.Confirm += fmt.Sprintf(" (ключей заведено %d).", ключей)
		}
		toggle.ConfirmOK = "Только эта машина"
	} else {
		row.Caption = "сейчас своей считается только петля самого сервера; остальные входят по ключу устройства"
		кто := "любое устройство из 10.0.0.0/8, 172.16.0.0/12 и 192.168.0.0/16 — соседний компьютер, телефон гостя по Wi-Fi, " +
			"чужая программа и соседние контейнеры на этой машине"
		if открыт {
			toggle.Confirm = "Считать своей всю частную сеть? Тогда " + кто + " войдёт в Сёму без ключа и получит всё: " +
				"серверы и команды на них, секреты, почту, файлы и память."
		} else {
			toggle.Confirm = "Считать своей всю частную сеть? Тогда " + кто + " будет считаться самой машиной: " +
				"пока ключей нет, сможет завести первый, а включите «Пускать без проверки личности» — войдёт без ключа " +
				"и получит всё: серверы и команды на них, секреты, почту, файлы и память."
		}
		toggle.ConfirmOK = "Считать своей"
		// кнопка согласия — красная: это расширение доступа, а не настройка вида
		toggle.Style = "destructive"
	}
	row.Actions = []Action{toggle}
	return row
}

// deniedCount — сколько обращений Сёма отбил за то, что помнит журнал:
// чужой источник и неизвестное имя сервера. Это и есть попытки достучаться
// снаружи — удачные местные обращения в журнал не попадают вовсе.
func deniedCount(entries []*store.AccessEntry) int {
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Note, "чужой источник") || strings.HasPrefix(e.Note, "неизвестное имя сервера") {
			n++
		}
	}
	return n
}

// namesLine — имена, под которыми Сёма отвечает своей сети.
func namesLine(s *Server) string {
	names := s.auth.LocalHosts()
	if len(names) == 0 {
		return "только адрес входа"
	}
	return strings.Join(names, ", ")
}

// lastLogin — когда последний раз входили ключом устройства.
func lastLogin(keys []*store.Passkey) string {
	var last time.Time
	for _, k := range keys {
		if k.LastUsed.After(last) {
			last = k.LastUsed
		}
	}
	if last.IsZero() {
		return ""
	}
	return last.Format("02.01 15:04")
}

// accessByPhrase — строка под заголовком: чем входят и пускает ли Сёма
// свою сеть без входа.
func accessByPhrase(s *Server, keys []*store.Passkey) string {
	чем := "Ключей устройств ещё нет: войти снаружи нечем."
	if len(keys) > 0 {
		чем = "Вход по ключу устройства — пароля нет вовсе."
	}
	if !s.auth.LocalOpen() {
		return чем + " Вход нужен всем, включая эту машину."
	}
	if auth.LocalPrivate.Load() {
		return чем + " Из домашней сети Сёма пускает без входа: так решено вами, и это видно ниже."
	}
	return чем + " Без входа Сёма пускает только запросы с самой машины."
}

// accessShelf — «стоит посмотреть» на «Кто внутри»: забытые сессии. Они ничего
// не делают, но живут до своего срока; закрыть их можно по одной или всем
// разом в «Опасном» ниже.
func (s *Server) accessShelf(add func(Component), sessions []*store.Session) {
	живых, старых := 0, 0
	порог := time.Now().Add(-24 * time.Hour)
	for _, se := range sessions {
		if se.LastSeen.After(порог) {
			живых++
		} else {
			старых++
		}
	}
	if старых < 3 {
		return
	}
	add(Component{Type: "shelf", Title: "Стоит посмотреть", Meta: plural(1, "замечание", "замечания", "замечаний"), Children: []Component{{Type: "row",
		Subtitle: fmt.Sprintf("Открыто %s, работают %d", plural(len(sessions), "вход", "входа", "входов"), живых),
		Caption:  fmt.Sprintf("%s не подавали признаков жизни больше суток — это забытые вкладки; они ничего не делают, но живут до своего срока. Закрыть их можно по одной или всем разом в «Опасном» ниже", plural(старых, "вход", "входа", "входов"))}}})
}

// accessForeign — «стоит посмотреть» на «Кто стучался»: отбитые попытки с чужой
// страницы. Отбитая попытка — не ошибка Сёмы, но человек должен знать, что кто-то
// стучался, а не искать это в тридцати строках журнала.
func (s *Server) accessForeign(add func(Component), entries []*store.AccessEntry) {
	var kids []Component
	чужие, первый, last, адреса := 0, time.Time{}, time.Time{}, map[string]bool{}
	for _, e := range entries {
		if !strings.HasPrefix(e.Note, "чужой источник") {
			continue
		}
		чужие++
		адреса[e.IP] = true
		if первый.IsZero() || e.At.Before(первый) {
			первый = e.At
		}
		if e.At.After(last) {
			last = e.At
		}
	}
	if чужие > 0 {
		откуда := plural(len(адреса), "адреса", "адресов", "адресов")
		if len(адреса) == 1 {
			for ip := range адреса {
				откуда = "все с " + ip
			}
		} else {
			откуда = "с " + откуда
		}
		kids = append(kids, Component{Type: "row",
			Subtitle: plural(чужие, "раз кто-то пробовал", "раза кто-то пробовал", "раз кто-то пробовал") + " нажать кнопку в Сёме с чужой страницы",
			Caption: fmt.Sprintf("Все отбиты: запрос пришёл с другого сайта, Сёма такие не выполняет. Первый %s, последний %s, %s.",
				whenText(первый.Format(time.RFC3339)), whenText(last.Format(time.RFC3339)), откуда)})
	}
	if len(kids) == 0 {
		return
	}
	add(Component{Type: "shelf", Title: "Стоит посмотреть",
		Meta: plural(len(kids), "замечание", "замечания", "замечаний"), Children: kids})
}

// sessionsSection — кто сейчас внутри. Двенадцать строк, из которых
// десять — забытые вкладки одного и того же браузера, читаются как
// «двенадцать чужих устройств». Одинаковые сворачиваются в одну.
func (s *Server) sessionsSection(add func(Component), sessions []*store.Session, keys []*store.Passkey) {
	// чем вошли: ключом, фразой или ничем (с самой машины). У сессии по
	// фразе ключа нет, и «вход с этой машины» про неё было бы неправдой —
	// особенно про урезанную, которая пришла как раз не с этой машины
	имяВхода := func(se *store.Session) string {
		for _, k := range keys {
			if k.ID == se.PasskeyID {
				return "ключ «" + k.Name + "»"
			}
		}
		if strings.Contains(se.Note, "фраз") {
			if se.Restricted {
				return "вход по фразе — только заведение ключа"
			}
			return "вход по фразе"
		}
		return "вход с этой машины"
	}
	строка := func(se *store.Session) Component {
		// тонкая строка: устройство, чем вошли и откуда — одной фразой; когда
		// вошли, когда был последний запрос и до какого дня живёт — парами
		row := Component{Type: "row", Layout: "wide", ID: "session-" + se.Hash[:12],
			Subtitle: agentTitle(se.Agent),
			Caption:  имяВхода(se) + " · " + se.IP + " · действует до " + se.Expires.Format("02.01"),
			Facts: []Prop{{Label: "Вошли", Value: se.Created.Format("02.01 15:04")},
				{Label: "Последний запрос", Value: se.LastSeen.Format("02.01 15:04")}},
			Actions: []Action{{Action: "logout_session", Target: se.Hash, Label: "Завершить"}}}
		if se.Own {
			row.Badges = []Badge{{Text: "своё устройство", Color: "green"}}
			row.Actions = append(row.Actions, Action{Action: "unmark_own_device", Target: se.Hash, Label: "Снять отметку «моё»",
				Style: "destructive",
				Confirm: "Снять отметку «моё» с этого устройства? Снимок экрана, сохранённый на нём, станет недоступен — " +
					"но не сразу: достучаться до чужого браузера нечем, и он сотрётся там при ближайшем открытии Сёмы.",
				ConfirmOK: "Снять отметку"})
		} else {
			row.Actions = append(row.Actions, Action{Action: "mark_own_device", Target: se.Hash, Label: "Отметить своим"})
		}
		return row
	}
	порог := time.Now().Add(-24 * time.Hour)
	var rows []Component
	// свежие идут поимённо, забытые — кучками по устройству и ключу
	var порядок []string
	кучки := map[string][]*store.Session{}
	for _, se := range sessions {
		if se.LastSeen.After(порог) {
			rows = append(rows, строка(se))
			continue
		}
		k := agentTitle(se.Agent) + "|" + имяВхода(se)
		if _, before := кучки[k]; !before {
			порядок = append(порядок, k)
		}
		кучки[k] = append(кучки[k], se)
	}
	for _, k := range порядок {
		кучка := кучки[k]
		устройство, ключ, _ := strings.Cut(k, "|")
		if len(кучка) < 3 {
			for _, se := range кучка {
				rows = append(rows, строка(se))
			}
			continue
		}
		children := make([]Component, 0, len(кучка))
		от, до := кучка[0].Created, кучка[0].LastSeen
		for _, se := range кучка {
			children = append(children, строка(se))
			if se.Created.Before(от) {
				от = se.Created
			}
			if se.LastSeen.After(до) {
				до = se.LastSeen
			}
		}
		rows = append(rows, Component{Type: "rowgroup", ID: "sessions-" + k,
			Subtitle: устройство + " — ещё " + plural(len(кучка), "вход", "входа", "входов"),
			Caption: fmt.Sprintf("%s · с %s по %s · последний запрос %s", ключ,
				от.Format("02.01"), до.Format("02.01"), до.Format("02.01 15:04")),
			Children: children})
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Никто не вошёл",
			"С этой машины Сёма работает без входа, поэтому сессия и не заводится. Строка здесь появляется, когда входят ключом устройства", "person"))
	}
	add(Component{Type: "section", ID: "access-sessions", Title: fmt.Sprintf("Кто сейчас внутри · %s", plural(len(sessions), "вход", "входа", "входов")), Children: rows})
	свои := 0
	for _, se := range sessions {
		if se.Own {
			свои++
		}
	}
	// справка вкладки — одной свёрнутой строкой; «своё устройство» объяснено
	// там же: что с него хранится и почему снимок стирается не сразу
	own := "Ни одно устройство не отмечено своим — без сети экраны нигде не сохраняются. Отметить можно в «···» у входа выше."
	if свои > 0 {
		own = fmt.Sprintf("Снимок экрана без сети может лечь на %s. Сняли отметку — снимок стирается не сразу, "+
			"а при ближайшем открытии Сёмы на том устройстве: достучаться до чужого браузера нечем.",
			plural(свои, "устройство", "устройства", "устройств"))
	}
	add(accessHow("access-inside-how",
		"Здесь те, кто вошёл ключом или фразой: у каждого своя сессия, она живёт до своего срока и закрывается «Завершить». "+
			"С самой машины Сёма пускает без входа, поэтому сессия там не заводится.\n\n"+own))
}

// accessLog — обращения, свёрнутые по «кто, куда, с каким исходом».
// Порядок первого появления сохраняется: свежее сверху.
func accessLog(entries []*store.AccessEntry) []Component {
	type кучка struct {
		первый  *store.AccessEntry
		сколько int
		от, до  time.Time
		адреса  map[string]bool
		байты   int64
	}
	var порядок []string
	кучки := map[string]*кучка{}
	for _, e := range entries {
		k := fmt.Sprintf("%s|%s %s|%d|%s", e.Who, e.Method, e.Path, e.Status, e.Note)
		g, before := кучки[k]
		if !before {
			g = &кучка{первый: e, от: e.At, до: e.At, адреса: map[string]bool{}}
			кучки[k] = g
			порядок = append(порядок, k)
		}
		g.сколько++
		g.байты += e.Bytes
		g.адреса[e.IP] = true
		if e.At.Before(g.от) {
			g.от = e.At
		}
		if e.At.After(g.до) {
			g.до = e.At
		}
	}
	var out []Component
	for _, k := range порядок {
		g := кучки[k]
		e := g.первый
		row := Component{Type: "row", Layout: "wide", ID: "acc-" + strconv.FormatInt(e.ID, 10),
			Subtitle: requestTitle(e), Meta: requestByMeta(g.сколько, g.от, g.до, g.адреса, g.байты)}
		if e.Status >= 400 {
			withLevel(&row, LevelWarn)
			row.Badges = []Badge{{Text: denialCaption(e, g.сколько), Level: LevelWarn}}
		}
		if e.Note != "" {
			row.Caption = e.Note
		}
		out = append(out, row)
	}
	return out
}

// requestTitle — что человек в этой строке узнаёт: кто и зачем
// приходил. «GET /api/ui» — это адрес, а не событие.
func requestTitle(e *store.AccessEntry) string {
	что := e.Method + " " + e.Path
	switch {
	case strings.HasPrefix(e.Note, "чужой источник"):
		что = "Действие с чужой страницы — отбито"
	case strings.HasPrefix(e.Path, "/api/ui"), e.Path == "/ui":
		что = "Экраны"
	case e.Path == "/api/action", e.Path == "/do":
		что = "Действие"
	case strings.HasPrefix(e.Path, "/api/files"):
		что = "Файлы"
	}
	кто := e.Who
	switch {
	case кто == "" || кто == "anon":
		кто = "без входа"
	case кто == "phrase":
		кто = "вход по фразе"
	case strings.HasPrefix(кто, "token:"):
		кто = "токен «" + strings.TrimPrefix(кто, "token:") + "»"
	case strings.HasPrefix(кто, "session:"):
		кто = "вход " + strings.TrimPrefix(кто, "session:")
	}
	return что + " · " + кто
}

func requestByMeta(сколько int, от, до time.Time, адреса map[string]bool, байты int64) string {
	var части []string
	if сколько > 1 {
		части = append(части, fmt.Sprintf("%d раз с %s по %s", сколько, whenText(от.Format(time.RFC3339)), whenText(до.Format(time.RFC3339))))
	} else {
		части = append(части, whenText(до.Format(time.RFC3339)))
	}
	var ips []string
	for ip := range адреса {
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	sort.Strings(ips)
	if len(ips) == 1 {
		части = append(части, ips[0])
	} else if len(ips) > 1 {
		части = append(части, plural(len(ips), "адрес", "адреса", "адресов"))
	}
	if байты > 0 {
		части = append(части, humanBytes(байты))
	}
	return strings.Join(части, " · ")
}

func denialCaption(e *store.AccessEntry, сколько int) string {
	label := "отказ " + strconv.Itoa(e.Status)
	if сколько > 1 {
		label += " ×" + strconv.Itoa(сколько)
	}
	return label
}

func scopeTitle(id string) string {
	for _, sc := range auth.Scopes {
		if sc.ID == id {
			return sc.Title
		}
	}
	return id
}

func agentTitle(ua string) string {
	switch {
	case strings.Contains(ua, "Sema/"):
		return "Приложение Сёма"
	case strings.Contains(ua, "iPhone"):
		return "iPhone · браузер"
	case strings.Contains(ua, "Macintosh"):
		return "Mac · браузер"
	case ua == "":
		return "неизвестное устройство"
	}
	return clipRunes(ua, 40)
}

// Замкнутый список: новый обработчик не получает право «actions» сам.
// Модели, скрипты, SSH, секреты и настройки доступны только хозяину.
var limitedActions = map[string]bool{
	"confirm_spam": true, "not_spam": true, "refresh": true,
	"allow_sender": true, "whitelist_off": true, "restore_sender": true,
	"new_chat": true, "rename_chat": true, "stop_chat": true,
}

func (s *Server) allowScreen(w http.ResponseWriter, r *http.Request, screen string) bool {
	if strings.HasPrefix(screen, accessScreen) && !s.mayAdmin(r) {
		http.Error(w, "экран доступа открыт только хозяину", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) allowAction(w http.ResponseWriter, r *http.Request, req actionRequest) bool {
	if !limitedActions[req.Action] && !s.mayAdmin(r) {
		http.Error(w, "действие доступно хозяину или токену с правом «всё»", http.StatusForbidden)
		return false
	}
	return s.allowScreen(w, r, req.Screen)
}

// ownDevice — устройство отмечено своим (см. Document.OwnDevice): только
// у сессии, заведённой passkey, и только когда отметку поставили на
// «Доступе». У локального доступа и токена своей сессии в базе нет —
// молчание здесь читается как «нет», а не «пока не решили».
func (s *Server) ownDevice(r *http.Request) bool {
	id := s.auth.Identify(r)
	return id.Kind == "session" && id.Session != nil && id.Session.Own
}

// mayAdmin — можно ли этим запросом менять доступ: сессия человека и
// локальный запрос — да, токен — только с правом «всё».
func (s *Server) mayAdmin(r *http.Request) bool {
	id := s.auth.Identify(r)
	if id.Kind == "token" {
		return id.Token != nil && auth.HasScope(id.Token, "all")
	}
	return id.Kind == "session" || id.Kind == "local"
}

func (s *Server) accessActions(req actionRequest) (string, error) {
	st := s.eng.Store()
	switch req.Action {
	case "rename_passkey":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		return "", st.RenamePasskey(id, strings.TrimSpace(req.Params["value"]))
	case "delete_passkey":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		if err := st.DeletePasskey(id); err != nil {
			return "", err
		}
		// ключа нет — нет и его сессий: cookie не должна пережить отзыв
		return "", st.DeleteSessionsOfPasskey(id)
	case "logout_session":
		return "", st.DeleteSession(req.Target)
	case "logout_all":
		return "", st.DeleteAllSessions()
	case "mark_own_device":
		return "", st.SetSessionOwn(req.Target, true)
	case "unmark_own_device":
		// снимок на самом устройстве это не стирает: достучаться до
		// чужого браузера нечем, он пропадёт при ближайшем открытии
		// Сёмы там же (см. подпись у действия и design.md, решение 3)
		return "", st.SetSessionOwn(req.Target, false)
	case "add_token":
		name := strings.TrimSpace(req.Params["name"])
		if name == "" {
			return "", fmt.Errorf("нужно название токена")
		}
		var scopes []string
		for _, sc := range auth.Scopes {
			if req.Params["scope_"+sc.ID] == "true" {
				scopes = append(scopes, sc.ID)
			}
		}
		if len(scopes) == 0 {
			return "", fmt.Errorf("выберите хотя бы одно право")
		}
		value, err := st.AddToken(name, scopes)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.lastToken, s.lastTokenAt = value, time.Now()
		s.mu.Unlock()
		return "", nil
	case "revoke_token":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		return "", st.RevokeToken(id)
	// фразу придумывает Сёма: человек выбрал бы то, что помнит, а её
	// показывают один раз и хранят хэшем — придумывать нечего
	case "new_phrase":
		_, err := s.auth.SetPhrase()
		return "", err
	case "revoke_phrase":
		return "", s.auth.RevokePhrase()
	// «Считать своей всю частную сеть» — тумблер строки с вопросом
	// (privateNetRow). Флаг процесса перечитывается сразу: IsLocal спрашивает
	// его на каждый запрос
	case "toggle_local_scope":
		scope := store.ScopePrivate
		if st.SettingText(store.AuthLocalScope) == store.ScopePrivate {
			scope = store.ScopeLoopback
		}
		if err := st.SetSetting(auth.SettingLocalScope, scope); err != nil {
			return "", err
		}
		s.auth.LoadLocalScope()
		return "", nil
	// «save_auth_danger» — те же настройки, но тумблер «без проверки
	// личности» из блока «Опасное»: у него своя кнопка, чтобы «кого пускать
	// без входа» не уезжал вместе с именем сервера одним нажатием «Сохранить».
	// «Своя частная сеть» на экране — строка с вопросом (toggle_local_scope),
	// а параметр здесь остался ради скриптов и прежних форм.
	// Непришедшее поле здесь значит «не трогали», поэтому разбор общий
	case "save_auth", "save_auth_danger":
		if v := strings.TrimSpace(req.Params[auth.SettingRPID]); v != "" {
			_ = st.SetSetting(auth.SettingRPID, v)
		}
		if v := strings.TrimSpace(req.Params[auth.SettingOrigin]); v != "" {
			_ = st.SetSetting(auth.SettingOrigin, v)
		}
		if v, ok := req.Params[auth.SettingLocalOpen]; ok {
			_ = st.SetSetting(auth.SettingLocalOpen, boolValue(v == "true"))
		}
		if v, ok := req.Params[auth.SettingTrustProxy]; ok {
			_ = st.SetSetting(auth.SettingTrustProxy, boolValue(v == "true"))
			trustProxy.Store(v == "true")
		}
		if v, ok := req.Params[auth.SettingLocalScope]; ok {
			scope := "loopback"
			if v == "true" {
				scope = "private"
			}
			_ = st.SetSetting(auth.SettingLocalScope, scope)
			s.auth.LoadLocalScope()
		}
		if v, ok := req.Params[auth.SettingLocalHosts]; ok {
			_ = st.SetSetting(auth.SettingLocalHosts, strings.TrimSpace(v))
		}
		// предел попыток: ноль и пустое не принимаем — это означало бы
		// «запирать сразу» или «не запирать вовсе», а обе крайности вредны
		if v := strings.TrimSpace(req.Params[auth.SettingPhraseLimit]); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				_ = st.SetSetting(auth.SettingPhraseLimit, v)
			} else {
				return "", fmt.Errorf("предел попыток — целое число больше нуля")
			}
		}
		if v, ok := req.Params[auth.SettingUserVerification]; ok {
			mode := "preferred"
			if v == "true" {
				mode = "required"
			}
			_ = st.SetSetting(auth.SettingUserVerification, mode)
		}
		return "", nil
	}
	return "", errUnhandled
}

// webShell — адрес оболочки интерфейса: сами файлы клиента.
func webShell(p string) bool { return web.IsShell(p) }

// wantsPage — запрос от браузера, а не от приложения: тогда отказ
// показывается страницей входа, а не строкой JSON. htmx-запросы тоже
// сюда попадают: у них свой заголовок, и клиент по нему уводит на вход.
func wantsPage(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.Header.Get("HX-Request") != "" {
		return false // ответ подменит кусок страницы — редирект здесь не поможет
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// Unwrap и Hijack — рукопожатие WebSocket забирает соединение себе, а
// обёртка над ответом это скрывала: библиотека отвечала 501, как будто
// сервер не умеет провод вовсе.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	c, rw, err := h.Hijack()
	if err == nil {
		// сервер ставил на соединение сроки чтения и записи запроса; забранное
		// соединение их наследует, и провод умирал бы через ReadTimeout, а
		// потом через WriteTimeout — молча. Сроки провода ставит сама ручка
		_ = c.SetDeadline(time.Time{})
	}
	return c, rw, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
