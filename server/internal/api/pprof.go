package api

// Профилировщик Go (perf-pass 1.3). Замер 3 октября 2026 разбирали без
// него: «Здоровье» строилось 2,1 с, и где именно, искали чтением кода.
//
// Дверь узкая: выключен по умолчанию (`debug.pprof`, «Работа Сёмы → Экран
// и связь → Тонкая настройка»), а включённый отвечает только сессии,
// запросу с этой машины и токену с правом «всё». Токен «экраны» до сюда
// не доходит вовсе: `ScopeFor` относит путь к «всё», и `protect` отказывает
// с записью в журнале доступа. Выключенный — «не найдено», как будто его
// нет: о двери, которая закрыта, незачем рассказывать.

import (
	"net/http"
	"net/http/pprof"
	"strings"
)

const pprofPath = "/debug/pprof/"

func (s *Server) handlePprof(w http.ResponseWriter, r *http.Request) {
	if !s.eng.PprofOn() {
		http.NotFound(w, r)
		return
	}
	if !s.mayAdmin(r) {
		http.Error(w, "нужно право «всё»", http.StatusForbidden)
		return
	}
	// срок записи профилю продлевает сам pprof (WriteTimeout + seconds)
	switch strings.TrimPrefix(r.URL.Path, pprofPath) {
	case "cmdline":
		pprof.Cmdline(w, r)
	case "profile":
		pprof.Profile(w, r)
	case "symbol":
		pprof.Symbol(w, r)
	case "trace":
		pprof.Trace(w, r)
	default:
		pprof.Index(w, r)
	}
}
