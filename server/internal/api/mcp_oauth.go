package api

// Возврат с входа OAuth на чужой сервер MCP (`engine/mcp_oauth.go`). Сервер
// входа отправляет сюда браузер человека с `code` и `state`; ручка стоит под
// тем же `protect`, что и всё остальное, — её зовёт браузер владельца со
// своей сессией, а чужой браузер без сессии до неё не доходит (человека
// без сессии `protect` уводит на вход, и после него он возвращается сюда же
// с теми же параметрами).

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// requestOrigin — адрес Сёмы, с которого пришёл запрос: схема и хост так, как
// их набрал человек. Именно он, а не `auth.origin`: сессия человека лежит
// в cookie того адреса, где он сейчас, и вернуть его нужно туда же — на
// sema.local:8443 это одна сессия, на sema.example.org другая, а адрес
// входа мог быть вообще выведен из порта (`localhost`) и человеку ни к чему.
//
// Схему сервер за прокси угадать не может (TLS заканчивается у чужого
// nginx, а `X-Forwarded-Proto` верят только при включённом доверии к
// прокси). Тогда её называет человек: если хост запроса совпал с хостом
// `auth.origin`, берётся схема оттуда.
//
// Хосту из запроса доверять безопасно: запрос дошёл под сессией (её cookie
// привязана к этому хосту) или из своей сети под проверкой имён
// (`localHostAllowed`), а код, попавший не туда, без verifier бесполезен
// (PKCE) — verifier остаётся в памяти Сёмы.
func (s *Server) requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || (trustProxy.Load() && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	u, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return s.auth.Origin()
	}
	if declared, err := url.Parse(s.auth.Origin()); err == nil && strings.EqualFold(declared.Host, u.Host) &&
		(declared.Scheme == "http" || declared.Scheme == "https") {
		return declared.Scheme + "://" + declared.Host
	}
	return u.Scheme + "://" + u.Host
}

// handleMCPCallback — `GET /mcp/oauth/callback?code=…&state=…`.
func (s *Server) handleMCPCallback(w http.ResponseWriter, r *http.Request) {
	// завершить вход может тот, кто вправе менять интеграции: сессия
	// человека, свой запрос без входа или токен с правом «всё»
	if !s.mayAdmin(r) {
		http.Error(w, "вход на сервер MCP завершает только хозяин: сессия или токен с правом «всё»", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	who := s.auth.Identify(r).Who
	// обмен кода и вычитка инструментов не должны обрываться оттого, что
	// человек закрыл вкладку: код одноразовый, второго не будет
	server, err := s.eng.MCPLoginFinish(context.WithoutCancel(r.Context()), q.Get("state"), q.Get("code"), q.Get("error"), q.Get("error_description"), who)
	if err != nil && server == "" {
		// что за сервер, неизвестно: показать итог в строке негде
		status := http.StatusBadRequest
		if !errors.Is(err, engine.ErrMCPState) {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error()+"\n\nОткройте «Настройки → Интеграции» и нажмите «Войти» ещё раз.", status)
		return
	}
	// итог — на экране сервера (вошли, инструментов столько-то, или причина
	// отказа): человека ведут к нему. Адрес возврата и документ клиента от
	// этого не меняются — это адрес экрана, а не OAuth
	http.Redirect(w, r, "/#screen="+url.QueryEscape(entityAddr(mcpScreen, server, "")), http.StatusSeeOther)
}

// handleMCPClientDoc — `GET /mcp/oauth/client.json`: документ клиента (Client
// ID Metadata Document), по адресу которого сервер входа узнаёт Сёму.
//
// Ручка **открыта без входа** (`openPath`): документ скачивает чужой сервер,
// у него нет ни сессии, ни токена. Поэтому она делает ровно одно: отдаёт то,
// что собрано из настроек (`Engine.MCPClientDocument`). Ничего из запроса —
// ни параметров, ни заголовков, ни имени хоста — в ответ не попадает, и
// вытянуть через неё что-либо ещё нельзя. Нет публичного https-адреса —
// нет и документа: 404, а не документ с внутренним адресом.
func (s *Server) handleMCPClientDoc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "документ клиента только читается", http.StatusMethodNotAllowed)
		return
	}
	doc, ok := s.eng.MCPClientDocument()
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	// документ публичный и меняется только правкой настроек: сервер входа
	// вправе держать его в кэше (черновик так и советует), а `no-store` от
	// `protect` заставило бы его скачивать документ на каждый запрос
	h.Set("Cache-Control", "public, max-age=300")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(doc)
}
