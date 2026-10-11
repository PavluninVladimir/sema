package engine

// Ошибки инструментов MCP: суть первой строкой, подсказка про формат — на
// будущее.
//
// 30 сентября 2026 (чат 103, `mcp_log` 46–47) владелец попросил поставить срок
// задаче Planner «на завтра». Модель послала `deadline: "2026-10-01"` — как
// сказано в схеме («ISO 8601 date string»). Сервер отказал: дата нужна со
// временем и поясом. Ошибка прямо называла формат, но до модели она дошла
// обрезанной («инструмент вернул ошибку: { "code": … "message": [ "V»): суть
// лежит глубоко в JSON (`details.raw.message[0]`), а клиент отрезал первые
// двести знаков. Сёма ответил «не получилось» и не попытался исправить вызов.
//
// Что теперь. Суть вынимается из ответа сервера и стоит первой строкой —
// в карточке для человека, во втором ходе модели, в продолжении после кнопки;
// сырой ответ идёт следом, обрезанный по рунам с названным остатком. Отказ по
// аргументам запоминается у сервера как подсказка к инструменту и живёт в
// промпте, пока не истечёт `mcp.tool_hint_days` или человек её не забудет.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/mcp"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Настройки: пороги правятся на «Интеграциях», в коде только первичные значения.
const (
	mcpErrorCharsKey    = "mcp.error_chars"     // предел сути ошибки в первой строке
	mcpToolHintDaysKey  = "mcp.tool_hint_days"  // сколько дней подсказка про формат идёт в промпт; 0 — не идёт
	mcpToolHintCharsKey = "mcp.tool_hint_chars" // сколько знаков сути хранит подсказка
	mcpArgRetriesKey    = "mcp.arg_retries"     // сколько раз модель исправляет вызов после отказа по аргументам
)

// mcpFailArgs — вид отказа: сервер не принял аргументы вызова (проверка, формат).
const mcpFailArgs = "args"

// mcpFail — ошибка вызова так, как её видят человек и модель.
type mcpFail struct {
	Kind string // mcpFailArgs — отказ по аргументам; пусто — прочее (сеть, вход, сама работа)
	// FromServer — текст написал сам сервер (ошибка инструмента, HTTP-статус,
	// JSON-RPC), а не Сёма: это данные сервера, и закрытому модели серверу они
	// идут под теми же правилами, что и его ответы.
	FromServer bool
	Gist       string // суть без имени сервера: то, что сервер сказал человеческими словами
	Line       string // первая строка целиком: «<сервер> отклонил аргументы: <суть> (<код>, HTTP <статус>)»
	Raw        string // ответ как пришёл, если в нём было больше, чем в первой строке
}

// Text — первая строка, а под ней сырой ответ по рунам с названным остатком.
// Предел касается только сырого ответа: суть не обрезается по этому пределу.
func (f mcpFail) Text(rawLimit int) string {
	if f.Raw == "" {
		return f.Line
	}
	return f.Line + "\n" + clipNamed(f.Raw, rawLimit)
}

// firstFilledLine — первая непустая строка текста.
func firstFilledLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// clipKeepingFirstLine — текст в предел знаков, но первая строка (суть)
// остаётся целой при любом пределе: обрезается только то, что под ней.
func clipKeepingFirstLine(s string, limit int) string {
	if limit <= 0 || len([]rune(s)) <= limit {
		return s
	}
	head, rest, found := strings.Cut(s, "\n")
	if !found {
		return s // одна строка — это и есть суть, её не режем
	}
	room := limit - len([]rune(head)) - 1
	if room <= 0 {
		return head + fmt.Sprintf("\n… (сырой ответ не показан: %d знаков, предел `mcp.chat_result_chars`)", len([]rune(rest)))
	}
	return head + "\n" + clipNamed(rest, room)
}

// ---- разбор ответа сервера ----

// mcpErrInfo — что нашлось в ответе сервера.
type mcpErrInfo struct {
	Gist       string // человеческие строки самого глубокого уровня
	Code       string // `code` строкой: INVALID_FIELD
	Status     int    // HTTP-статус, если сервер его назвал
	NumCode    int    // числовой `code` (JSON-RPC: -32602)
	Structured bool   // ответ был JSON
}

// mcpHumanKeys — ключи, под которыми сервер кладёт слова для человека.
var mcpHumanKeys = map[string]bool{
	"message": true, "messages": true, "msg": true, "error": true, "errors": true,
	"error_description": true, "detail": true, "details": true, "reason": true,
	"description": true, "title": true, "hint": true, "cause": true,
}

// mcpFieldKeys — ключи, которыми проверка называет виновное поле; строка
// «deadline: must be …» полезнее, чем «must be …».
var mcpFieldKeys = []string{"field", "path", "param", "parameter", "property", "instancePath"}

var mcpStatusKeys = map[string]bool{
	"status": true, "statuscode": true, "status_code": true, "httpstatus": true, "http_status": true,
}

// mcpGenericGist — слова, которые ничего не говорят: пока есть другие, в суть
// не идут («Bad Request» рядом с настоящим сообщением проверки).
var mcpGenericGist = func() map[string]bool {
	m := map[string]bool{"error": true, "failed": true, "failure": true, "unknown error": true,
		"invalid params": true, "invalid request": true, "validation failed": true, "validation error": true}
	for code := 400; code < 600; code++ {
		if t := http.StatusText(code); t != "" {
			m[strings.ToLower(t)] = true
		}
	}
	return m
}()

type gistItem struct {
	depth int
	text  string
}

type numItem struct {
	depth int
	value int
}

type codeItem struct {
	depth int
	value string
}

// mcpErrWalk — обход JSON ответа. Порядок ключей — по алфавиту: карта в Go
// обходится как придётся, а суть не должна зависеть от того, как выпал жребий.
type mcpErrWalk struct {
	gists    []gistItem
	statuses []numItem
	numCodes []numItem
	codes    []codeItem
}

func (w *mcpErrWalk) walk(v any, depth int, human bool) {
	switch x := v.(type) {
	case string:
		if human {
			if s := strings.TrimSpace(x); s != "" {
				w.gists = append(w.gists, gistItem{depth, s})
			}
		}
	case []any:
		for _, it := range x {
			w.walk(it, depth, human)
		}
	case map[string]any:
		label := fieldLabel(x)
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val, lk := x[k], strings.ToLower(k)
			switch t := val.(type) {
			case string:
				switch {
				case lk == "code" || lk == "error_code" || lk == "errorcode":
					if c := strings.TrimSpace(t); c != "" && len(c) <= 64 && !strings.ContainsAny(c, " \t\n") {
						w.codes = append(w.codes, codeItem{depth, c})
					}
				case mcpStatusKeys[lk]:
					if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil && n >= 100 && n <= 599 {
						w.statuses = append(w.statuses, numItem{depth, n})
					}
				case mcpHumanKeys[lk]:
					if s := strings.TrimSpace(t); s != "" {
						if label != "" && (lk == "message" || lk == "msg" || lk == "reason") {
							s = label + ": " + s
						}
						w.gists = append(w.gists, gistItem{depth, s})
					}
				}
			case float64:
				n := int(t)
				switch {
				case mcpStatusKeys[lk] && n >= 100 && n <= 599:
					w.statuses = append(w.statuses, numItem{depth, n})
				case lk == "code" || lk == "error_code" || lk == "errorcode":
					w.numCodes = append(w.numCodes, numItem{depth, n})
				}
			case []any:
				w.walk(t, depth, mcpHumanKeys[lk]) // список уровень не меняет
			case map[string]any:
				w.walk(t, depth+1, mcpHumanKeys[lk])
			}
		}
	}
}

// fieldLabel — имя виновного поля из соседних ключей проверки: `field`,
// `path`, `loc` (у Pydantic это список: body.deadline).
func fieldLabel(m map[string]any) string {
	for _, k := range mcpFieldKeys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if loc, ok := m["loc"].([]any); ok {
		var parts []string
		for _, p := range loc {
			switch t := p.(type) {
			case string:
				parts = append(parts, t)
			case float64:
				parts = append(parts, strconv.Itoa(int(t)))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ".")
		}
	}
	return ""
}

// parseMCPErrorText — суть, код и статус из текста ошибки. Текст, не
// разбирающийся как JSON, суть и есть: берётся его первая строка.
func parseMCPErrorText(text string) mcpErrInfo {
	text = strings.TrimSpace(text)
	var root any
	dec := func(s string) bool { return json.Unmarshal([]byte(s), &root) == nil }
	ok := (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")) && dec(text)
	if !ok {
		// «Error: {…}» — часть серверов приписывает слово перед JSON
		if i := strings.IndexAny(text, "{["); i > 0 && i < 60 {
			ok = dec(text[i:])
		}
	}
	if !ok {
		return mcpErrInfo{Gist: firstFilledLine(text)}
	}
	w := &mcpErrWalk{}
	w.walk(root, 0, true)
	info := mcpErrInfo{Structured: true}
	// самая глубокая группа человеческих строк: наверху стоит обёртка
	// («REST request failed with HTTP 400»), настоящий отказ — внутри. Общие
	// слова («Bad Request») в счёт не идут, пока есть другие
	pick := func(skipGeneric bool) []gistItem {
		best, out := -1, []gistItem(nil)
		for _, g := range w.gists {
			if skipGeneric && mcpGenericGist[strings.ToLower(strings.TrimRight(g.text, ". "))] {
				continue
			}
			switch {
			case g.depth > best:
				best, out = g.depth, []gistItem{g}
			case g.depth == best:
				out = append(out, g)
			}
		}
		return out
	}
	items := pick(true)
	if len(items) == 0 {
		items = pick(false)
	}
	seen := map[string]bool{}
	var parts []string
	for _, g := range items {
		if !seen[g.text] && len(parts) < 5 {
			seen[g.text] = true
			parts = append(parts, g.text)
		}
	}
	info.Gist = strings.Join(parts, "; ")
	shallowest := func(list []numItem) int {
		best, val := 1<<30, 0
		for _, n := range list {
			if n.depth < best {
				best, val = n.depth, n.value
			}
		}
		return val
	}
	info.Status = shallowest(w.statuses)
	info.NumCode = shallowest(w.numCodes)
	if info.Status == 0 && info.NumCode >= 400 && info.NumCode <= 599 {
		info.Status = info.NumCode // Google-стиль: {"error": {"code": 400, …}}
	}
	best := 1 << 30
	for _, c := range w.codes {
		if c.depth < best {
			best, info.Code = c.depth, c.value
		}
	}
	return info
}

// reServerStatus — в тексте назван статус 5xx.
var reServerStatus = regexp.MustCompile(`(?i)\b(?:http|status(?: code)?)[ :=]*5\d\d\b`)

// mcpArgsRejected — это отказ по аргументам, а не сеть, вход или работа
// самого инструмента. Только такой отказ становится подсказкой к
// инструменту: «сервер требует другого формата» верно и завтра, а «сервер
// упал» — нет.
//
// Порядок такой. Известный статус решает первым: 5xx, 401/403/404/408/429 —
// нет, 400/422 — да. Без статуса смотрят код (`INVALID_*`, `VALIDATION_*`) и
// слова сообщения («must be», «invalid», «expected»); слова про вход, права,
// «не найдено», перегрузку исключают.
func mcpArgsRejected(status, numCode int, code, text string) bool {
	switch {
	case status >= 500:
		return false
	case status == 401 || status == 403 || status == 404 || status == 405 || status == 408 ||
		status == 409 || status == 410 || status == 423 || status == 429:
		return false
	case status == 400 || status == 422:
		return true
	case numCode == -32602:
		return true
	}
	if reServerStatus.MatchString(text) {
		return false // «Request failed with status code 502» — беда сервера, а не формата
	}
	low, lc := strings.ToLower(text), strings.ToLower(code)
	for _, no := range []string{"auth", "forbidden", "permission", "not_found", "notfound", "rate", "timeout", "unavailable", "internal"} {
		if strings.Contains(lc, no) {
			return false
		}
	}
	for _, yes := range []string{"invalid", "validation", "bad_request", "badrequest", "unprocessable", "schema"} {
		if strings.HasPrefix(lc, yes) || strings.Contains(lc, "_"+yes) {
			return true
		}
	}
	for _, no := range []string{"unauthorized", "unauthenticated", "forbidden", "permission denied", "not found",
		"rate limit", "too many requests", "timed out", "timeout", "unavailable", "internal server error",
		"api key", "invalid token", "token expired", "authentication", "invalid session", "session expired",
		"credentials", "not logged in", "login required"} {
		if strings.Contains(low, no) {
			return false
		}
	}
	for _, yes := range []string{"must be", "should be", "must not", "must have", "invalid", "expected",
		"is required", "field required", "required property", "validation", "not a valid", "unexpected"} {
		if strings.Contains(low, yes) {
			return true
		}
	}
	return false
}

// ---- описание ошибки ----

// describeMCPError — ошибка вызова инструмента: суть, вид и сырой ответ.
// Настоящую работу делают типизированные ошибки клиента (`mcp.ToolError`,
// `mcp.HTTPError`, `mcp.RPCError`); всё остальное — сеть, вход, время — идёт
// как есть: у него нет «сути внутри JSON».
func (e *Engine) describeMCPError(m *store.MCPServer, err error) mcpFail {
	if err == nil {
		return mcpFail{}
	}
	var (
		te     *mcp.ToolError
		he     *mcp.HTTPError
		re     *mcp.RPCError
		text   string
		status int
		rpc    int
		how    string
	)
	switch {
	case errors.As(err, &te):
		text, how = te.Text, "tool"
	case errors.As(err, &he):
		text, status, how = he.Body, he.Status, "http"
	case errors.As(err, &re):
		text, rpc, how = re.Message, re.Code, "rpc"
	default:
		msg := e.maskOutput(err.Error())
		return mcpFail{Gist: msg, Line: msg}
	}
	text = e.maskOutput(text) // секрет в тексте ошибки не должен дойти ни до карточки, ни до модели
	info := parseMCPErrorText(text)
	if info.Status == 0 {
		info.Status = status
	}
	if rpc != 0 && info.NumCode == 0 {
		info.NumCode = rpc
	}
	f := mcpFail{FromServer: true}
	if mcpArgsRejected(info.Status, info.NumCode, info.Code, info.Gist+" "+text) {
		f.Kind = mcpFailArgs
	}
	gist := clipGist(info.Gist, e.store.SettingInt(mcpErrorCharsDef))
	f.Gist = gist
	var tags []string
	if info.Code != "" {
		tags = append(tags, info.Code)
	}
	if info.Status != 0 {
		tags = append(tags, "HTTP "+strconv.Itoa(info.Status))
	} else if info.NumCode != 0 && how == "rpc" {
		tags = append(tags, "код "+strconv.Itoa(info.NumCode))
	}
	tail := ""
	if len(tags) > 0 {
		tail = " (" + strings.Join(tags, ", ") + ")"
	}
	name := m.Name
	switch {
	case f.Kind == mcpFailArgs && gist != "":
		f.Line = name + " отклонил аргументы: " + gist + tail
	case f.Kind == mcpFailArgs:
		f.Line = name + " отклонил аргументы" + tail
	case how == "http" && gist != "":
		f.Line = fmt.Sprintf("%s ответил HTTP %d: %s", name, info.Status, gist)
		if info.Code != "" {
			f.Line += " (" + info.Code + ")"
		}
	case how == "http":
		f.Line = fmt.Sprintf("%s ответил HTTP %d", name, info.Status)
	case gist != "":
		f.Line = name + " вернул ошибку: " + gist + tail
	default:
		f.Line = name + " вернул ошибку" + tail
	}
	if strings.TrimSpace(text) != strings.TrimSpace(info.Gist) {
		f.Raw = strings.TrimSpace(text)
	}
	return f
}

// clipGist — суть в предел знаков: по рунам, остаток назван.
func clipGist(s string, limit int) string {
	r := []rune(s)
	if limit <= 0 || len(r) <= limit {
		return s
	}
	return string(r[:limit]) + fmt.Sprintf(" … (ещё %d знаков, целиком в ответе ниже)", len(r)-limit)
}

// ---- подсказка к инструменту ----

// noteToolHint запоминает отказ по аргументам у сервера. Сеть, 5xx, отказ по
// кругу и вход — не особенность инструмента и сюда не попадают.
func (e *Engine) noteToolHint(m *store.MCPServer, tool string, f mcpFail) {
	if f.Kind != mcpFailArgs || strings.TrimSpace(f.Gist) == "" || m.Agent != "" {
		return
	}
	limit := e.store.SettingInt(mcpToolHintCharsDef)
	gist := f.Gist
	if r := []rune(gist); len(r) > limit {
		gist = string(r[:limit]) + fmt.Sprintf(" … (ещё %d знаков)", len(r)-limit)
	}
	if err := e.store.SetMCPToolHint(m.Name, tool, gist, time.Now()); err != nil {
		// подсказка — удобство, а не условие работы: вызов от этого не падает
		log.Printf("подсказка к инструменту %s.%s не записана: %v", m.Name, tool, err)
	}
}

// mcpHintDays — сколько дней подсказка живёт; 0 — подсказки в промпт не идут.
func (e *Engine) mcpHintDays() int { return e.store.SettingInt(mcpToolHintDaysDef) }

// mcpFreshHints — подсказки сервера, у которых срок не вышел: инструмент → суть.
func (e *Engine) mcpFreshHints(m *store.MCPServer) map[string]string {
	days := e.mcpHintDays()
	if days <= 0 || len(m.ToolHints) == 0 {
		return nil
	}
	out := map[string]string{}
	for tool, h := range m.ToolHints {
		at, err := time.Parse(time.RFC3339, h.At)
		if err != nil || time.Since(at) > time.Duration(days)*24*time.Hour {
			continue
		}
		out[tool] = h.Gist
	}
	return out
}

// mcpHintLine — строка подсказки под сигнатурой инструмента в промпте.
func mcpHintLine(gist string) string {
	return "      ⚠ сервер требует: " + strings.Join(strings.Fields(gist), " ") + "\n"
}
