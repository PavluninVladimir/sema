package api

// Подсветка строки после перехода.
//
// Поиск по настройкам и кнопка замечания «открыть саму настройку» ведут не на
// экран вообще, а к строке: экран открывается, раздел со строкой раскрывается,
// строка встаёт в середину окна и вспыхивает. Какая строка — знает тот, кто
// строит переход, а сам переход — обычный `$goto` с адресом экрана. Метка
// строки едет в этом адресе после знака «^»: `settings:servers@srv-vpn~auto^f:…`.
// Сервер снимает её первым делом (`splitHighlight` в `buildScreenForPhone`) и
// кладёт в `Document.Highlight` — так клиент узнаёт её только из ответа на
// переход, а адрес экрана (то, что он запомнит и будет перечитывать опросом)
// остаётся чистым. Подсветка поэтому не возвращается при перерисовке.
//
// Метка — не произвольная строка, а ссылка на элемент разметки: приставка
// говорит, чей это идентификатор.

import (
	"net/http"
	"strings"
)

const (
	// highlightSep — отделяет метку строки от адреса экрана. В адресах экранов
	// его не бывает (имена серверов, ящиков, моделей, секретов — слова, адреса
	// и пути); на всякий случай метку разбирает только то, что похоже на метку
	highlightSep = "^"

	hlFieldPrefix   = "f:" // поле формы: «f:<id формы>-<имя поля>» либо «f:<имя поля>»
	hlRowPrefix     = "r:" // строка: «r:<ID компонента>»
	hlSectionPrefix = "s:" // раздел: «s:<ID компонента>»
)

// hlField — метка поля формы. Совпадает с идентификатором, на который смотрит
// подпись поля (`AnchorID` в разметке): у формы с ID он «<форма>-<поле>».
func hlField(formID, name string) string {
	if formID != "" {
		return hlFieldPrefix + formID + "-" + name
	}
	return hlFieldPrefix + name
}

// hlRow — метка строки; у строки без ID метки нет.
func hlRow(id string) string {
	if id == "" {
		return ""
	}
	return hlRowPrefix + id
}

// hlSection — метка раздела; у раздела без ID метки нет.
func hlSection(id string) string {
	if id == "" {
		return ""
	}
	return hlSectionPrefix + id
}

// withHighlight — адрес перехода с меткой строки. Пустая метка — тот же адрес.
func withHighlight(addr, mark string) string {
	if mark == "" {
		return addr
	}
	return addr + highlightSep + mark
}

// splitHighlight — адрес экрана и метка строки из адреса перехода. Хвост после
// последнего «^», не похожий на метку, частью адреса остаётся.
func splitHighlight(screen string) (addr, mark string) {
	i := strings.LastIndex(screen, highlightSep)
	if i < 0 {
		return screen, ""
	}
	tail := screen[i+len(highlightSep):]
	for _, prefix := range []string{hlFieldPrefix, hlRowPrefix, hlSectionPrefix} {
		if strings.HasPrefix(tail, prefix) && len(tail) > len(prefix) {
			return screen[:i], tail
		}
	}
	return screen, ""
}

// ---- кто смотрит ----

// screenOptions — что сборка экрана знает о том, кто на него смотрит.
type screenOptions struct {
	// guest — смотрит тот, кому нельзя видеть вход и доступ (токен без права
	// «всё»): поиск по настройкам не показывает ему эти настройки
	guest bool
	// timing — счёт Server-Timing запроса: сборка отмечает, когда готово меню
	timing *screenTiming
	// prefetch — подогрев при наведении: экран собирается с Document.offline
	prefetch bool
}

// screenOpt — параметр сборки экрана. Необязательный: сборка без параметров —
// для хозяина, а обработчики запросов обязаны передать `s.viewerOpts(r)`;
// держит `TestSearchHidesAccessFromGuests`.
type screenOpt func(*screenOptions)

func asGuest() screenOpt { return func(o *screenOptions) { o.guest = true } }

// asPrefetch — сборка для подогрева: ничего наружу (Document.offline).
func asPrefetch() screenOpt { return func(o *screenOptions) { o.prefetch = true } }

// viewerOpts — параметры сборки для того, кто прислал запрос.
func (s *Server) viewerOpts(r *http.Request) []screenOpt {
	var opts []screenOpt
	if t := timingOf(r); t != nil {
		opts = append(opts, timedBy(t))
	}
	if s.mayAdmin(r) {
		return opts
	}
	return append(opts, asGuest())
}
