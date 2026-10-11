package api

// Предзагрузка экрана при наведении (perf-pass 7). Клиент заранее просит
// только экраны, чьё открытие ничего не меняет: чат ставит «прочитано»,
// «Почта» сдвигает окно «с прошлого захода», «Здоровье» с выбранным
// периодом запоминает его, модульный экран исполняет код модуля. Список
// разрешённых — здесь, клиент получает его атрибутом экрана; чего в нём
// нет, то не подгружается: молчание не разрешение. Держит
// TestPrefetchableScreensWriteNothing — открывает каждый такой экран и
// сверяет отпечаток базы.

import (
	"net/http"
	"strings"
)

// prefetchPrefixes — приставки адресов экранов без побочных действий.
// «modules» — список и экраны кода модулей (modules@…), но не «module:…»
// (экран модуля исполняет его код).
var prefetchPrefixes = []string{
	"files", "tasks", "scripts", "script:", "modules", "memory", "health", "queue",
	notifyScreen, "settings", vendorsScreen, "digest:groups", "categories",
}

// prefetchable — можно ли подгружать экран заранее. Тот же разбор, что у
// клиента: адрес равен приставке или начинается с неё.
func prefetchable(addr string) bool {
	if addr == "" || strings.Contains(addr, "&") {
		return false
	}
	for _, p := range prefetchPrefixes {
		if addr == p || strings.HasPrefix(addr, p) {
			return true
		}
	}
	return false
}

// noPrefetch — подогрева не будет: клиент откроет экран обычным запросом.
func noPrefetch(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
