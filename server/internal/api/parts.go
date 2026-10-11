package api

// Части экрана: содержимое, которое приезжает при раскрытии.
//
// Свёрнутый раздел всё равно ехал к человеку целиком — свёрнутость была
// клиентской. Замер 12 сентября 2026: экран MAX в свёрнутом виде нёс
// 2 094 узла и 207 КБ, Telegram — 1 305 узлов и 138 КБ, и ни одного из
// этих узлов человек не видел, пока не раскрывал. Здесь раздел приезжает
// заголовком, а содержимое просит у сервера часть — по её адресу, с тем
// же пределом и поиском, что и экран.
//
// Реестр держится построением: раздел с Lazy, чьей части здесь нет,
// роняет тест на сборку экранов, а не экран у человека.

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// fragmentBuilder — строит содержимое одной части. Экран нужен тем частям,
// у которых адрес несёт хвост (ящик, сервер); поиск и предел — как у
// экрана, кнопка «ещё» внутри части удваивает предел ровно так же.
type fragmentBuilder func(s *Server, экран, поиск string, предел int) ([]Component, error)

// fragments — все, что умеет сервер. Одно место, а не разбросанные по
// экранам обработчики: видно, чего человеку не показывают сразу.
func (s *Server) fragments() map[string]fragmentBuilder {
	return map[string]fragmentBuilder{
		// чаты Telegram — матрица на самом экране (telegram_chats.go), частей у них
		// нет; у MAX список приезжает по раскрытию, но тоже матрицей
		"max-chats":        (*Server).maxChatsFragment,
		"memory-pending":   (*Server).pendingRecordsFragment,
		"health-slow":      (*Server).failuresFragment,
		"memory-rules":     (*Server).memoryRulesFragment,
		"memory-consumers": (*Server).memoryConsumersFragment,
		"tasks-off":        (*Server).disabledTasksFragment,
		// «Тонкая настройка» любого экрана: тело раздела, который собрал foldRare
		rareID: (*Server).rareFragment,
		// общий журнал вызовов серверов MCP: хроника приезжает по раскрытию
		mcpLogPart: (*Server).mcpLogFragment,
		// журнал всех агентов: тоже хроника, приезжает по раскрытию
		agentsLogPart: (*Server).agentsLogFragment,
	}
}

// buildPart — собрать часть: те же правила, что у экрана (одно действие в
// строке), но без сворачивания длинного — тут человек уже раскрыл.
func (s *Server) buildPart(screen, часть, search string, предел int) ([]Component, error) {
	сборщик, has := s.fragments()[часть]
	if !has {
		return nil, fmt.Errorf("части %q нет", часть)
	}
	if предел <= 0 || предел == defaultRows {
		предел = fragmentLimit
	}
	блоки, err := сборщик(s, screen, search, предел)
	if err != nil {
		return nil, err
	}
	tidyRows(блоки)
	return блоки, nil
}

// fragmentLimit — сколько строк часть отдаёт с первого раза. Двадцать:
// выбранное всегда сверху, остальное по «ещё» или поиском.
const fragmentLimit = 20

func (s *Server) writePart(w http.ResponseWriter, r *http.Request, screen, часть, search string, предел int) {
	блоки, err := s.buildPart(screen, часть, search, предел)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var бл []web.Block
	for _, к := range блоки {
		бл = append(бл, toBlock(к))
	}
	markup, err := web.RenderFragment(бл)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// у части нет меню: всё время — её сборка вместе с разметкой
	if t := timingOf(r); t != nil {
		t.build = time.Since(t.start)
		w.Header().Set(serverTimingHeader, t.header())
	}
	w.Header().Set(web.BuildHeader, web.Build())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	web.WriteMaybeGzip(w, r, []byte(markup))
}

// lazySections — какие части просят экраны: для теста на построение.
func lazySections(cs []Component) []string {
	var out []string
	var walk func([]Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			if c.Lazy != "" {
				out = append(out, c.Lazy)
			}
			walk(c.Children)
		}
	}
	walk(cs)
	sort.Strings(out)
	return out
}

// moreInFragment — кнопка «ещё» внутри части: клиент удвоит предел части, а
// не экрана. Подпись говорит, сколько показано и сколько всего.
func moreInFragment(показано, total int) Component {
	return Component{Type: "button", Actions: []Action{{Action: "$more",
		Label: fmt.Sprintf("Показать ещё · %d из %d", показано, total)}}}
}
