// Package script — JavaScript внутри Сёмы. Движок goja: чистый Go, без cgo,
// поэтому сервер по-прежнему собирается под Raspberry Pi одной командой.
// Скрипт видит только то, что мы явно положили в объект sema: писем, файлов,
// сети и модели он касается через наши функции, всё остальное недоступно.
package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// Tool — один инструмент песочницы. Раньше это была жёсткая структура на
// десять полей, и любая новая возможность требовала правки ядра; теперь
// набор собирается реестром, который пополняют модули.
//
// Name — путь в объекте sema: "messages", "http.get", "servers.logs".
// Need — примитив ядра, без которого инструмент не выдаётся ("mail", "http"…).
type Tool struct {
	Name string
	Desc string
	Need string
	Args string // как вызывать — для подсказки модели
	Fn   func(args []any) (any, error)
}

type Result struct {
	Value    string   // что вернул скрипт
	Log      []string // console.log
	Duration time.Duration
	Err      error
	ErrLine  int // строка, на которой всё сломалось (0 — неизвестно)
	ErrText  string
	ErrCode  string // сама строка кода
	// ToolErr — упал инструмент ядра (модель, сеть), а не код скрипта;
	// Interrupted — остановлен по времени или отмене. Ни то, ни другое не
	// говорит, что код сломан.
	ToolErr     bool
	Interrupted bool
}

const toolErrMark = "[tool] "

const defaultTimeout = 30 * time.Second

// Run выполняет скрипт с ограничением по времени: зависший цикл прерывается.
// Скрипту видны только переданные инструменты — всё остальное недоступно.
func Run(ctx context.Context, code string, tools []Tool, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	started := time.Now()
	vm := goja.New()
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())

	res := Result{}
	logLine := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, stringify(vm, a))
		}
		res.Log = append(res.Log, strings.Join(parts, " "))
		return goja.Undefined()
	}
	console := vm.NewObject()
	_ = console.Set("log", logLine)
	_ = console.Set("error", logLine)
	_ = console.Set("info", logLine)
	_ = vm.Set("console", console)

	sema := vm.NewObject()
	for _, t := range tools {
		bindTool(vm, sema, t)
	}
	// человеческие даты нужны всем и ничего не открывают наружу
	_ = sema.Set("date", func(spec string) goja.Value {
		return vm.ToValue(parseDate(spec).Format(time.RFC3339))
	})
	// период из вопроса («за август», «прошлый месяц») — {since, until, label} или null
	_ = sema.Set("period", func(text string) goja.Value {
		from, to, label := ParsePeriod(text)
		if label == "" {
			return goja.Null()
		}
		return vm.ToValue(map[string]any{"since": from.Format(time.RFC3339), "until": to.Format(time.RFC3339), "label": label})
	})
	_ = vm.Set("sema", sema)

	// прерывание по таймауту и по отмене контекста
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
		case <-ctx.Done():
			vm.Interrupt("остановлено")
		case <-time.After(timeout):
			vm.Interrupt(fmt.Sprintf("скрипт работал дольше %s и был остановлен", timeout))
		}
	}()

	// код оборачиваем в функцию, чтобы работал return на верхнем уровне;
	// из-за обёртки номера строк сдвигаются на одну — учитываем это
	wrapped := "(function(){\n" + code + "\n})()"

	// сначала синтаксис: ошибка приходит с номером строки до запуска
	if _, err := goja.Compile("script.js", wrapped, false); err != nil {
		res.Duration = time.Since(started)
		res.Err = err
		res.ErrLine, res.ErrText = errPosition(err.Error(), 1)
		res.ErrCode = codeLine(code, res.ErrLine)
		return res
	}

	value, err := vm.RunString(wrapped)
	res.Duration = time.Since(started)
	if err != nil {
		res.Err = err
		res.ErrLine, res.ErrText = errPosition(err.Error(), 1)
		res.ErrCode = codeLine(code, res.ErrLine)
		if strings.Contains(err.Error(), toolErrMark) {
			res.ToolErr = true
			res.ErrText = strings.Replace(res.ErrText, toolErrMark, "", 1)
		}
		var interrupted *goja.InterruptedError
		res.Interrupted = errors.As(err, &interrupted)
		return res
	}
	if value != nil && !goja.IsUndefined(value) && !goja.IsNull(value) {
		res.Value = stringify(vm, value)
	}
	return res
}

// bindTool кладёт инструмент в объект sema по его имени: "http.get"
// становится sema.http.get. Ошибка инструмента прерывает скрипт так же,
// как обычное исключение, — с понятным текстом.
func bindTool(vm *goja.Runtime, sema *goja.Object, t Tool) {
	target := sema
	parts := strings.Split(t.Name, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := target.Get(p).(*goja.Object)
		if !ok || next == nil {
			next = vm.NewObject()
			_ = target.Set(p, next)
		}
		target = next
	}
	leaf := parts[len(parts)-1]
	_ = target.Set(leaf, func(call goja.FunctionCall) goja.Value {
		args := make([]any, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			if goja.IsUndefined(a) || goja.IsNull(a) {
				args = append(args, nil)
				continue
			}
			args = append(args, a.Export())
		}
		out, err := t.Fn(args)
		if err != nil {
			panic(vm.ToValue(toolErrMark + t.Name + ": " + err.Error()))
		}
		if out == nil {
			return goja.Undefined()
		}
		return vm.ToValue(out)
	})
}

// Аргументы приходят из JavaScript как есть, поэтому разбираем их мягко:
// пропущенный аргумент — не ошибка, а значение по умолчанию.
func Str(args []any, i int) string {
	if i >= len(args) || args[i] == nil {
		return ""
	}
	if s, ok := args[i].(string); ok {
		return s
	}
	// объект или массив — JSON, а не «map[...]» из Go: скрипт сохраняет
	// объект в store.set и ждёт назад то, что сможет разобрать
	switch args[i].(type) {
	case map[string]any, []any:
		if raw, err := json.Marshal(args[i]); err == nil {
			return string(raw)
		}
	}
	return fmt.Sprint(args[i])
}

func Opts(args []any, i int) map[string]any {
	if i >= len(args) || args[i] == nil {
		return map[string]any{}
	}
	if m, ok := args[i].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func stringify(vm *goja.Runtime, v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	switch exported := v.Export().(type) {
	case string:
		return exported
	case map[string]any, []any, []map[string]any:
		if raw, err := json.Marshal(exported); err == nil {
			return string(raw)
		}
	}
	return v.String()
}

// ParseDate — человеческие описания дат для ядра (уведомления, напоминания).
func ParseDate(spec string) time.Time { return parseDate(spec) }

// ParseFutureDate — срок в будущем словами («через 30 минут», «в понедельник»,
// «на выходных», «2026-10-05 10:00»); false — срок не понят. В отличие от
// ParseDate, непонятое не превращается в «месяц назад».
func ParseFutureDate(spec string) (time.Time, bool) {
	return parseFutureDate(strings.ToLower(strings.TrimSpace(spec)), time.Now())
}

// parseDate понимает человеческие описания дат.
func parseDate(spec string) time.Time {
	now := time.Now()
	s := strings.ToLower(strings.TrimSpace(spec))
	if t, ok := parseFutureDate(s, now); ok {
		return t
	}
	switch {
	case strings.Contains(s, "начало месяца"):
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	case strings.Contains(s, "начало недели"):
		offset := (int(now.Weekday()) + 6) % 7
		d := now.AddDate(0, 0, -offset)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
	case strings.Contains(s, "сегодня"):
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case strings.Contains(s, "вчера"):
		d := now.AddDate(0, 0, -1)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
	default:
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			switch {
			case strings.Contains(s, "час"):
				return now.Add(-time.Duration(n) * time.Hour)
			case strings.Contains(s, "недел"):
				return now.AddDate(0, 0, -7*n)
			case strings.Contains(s, "месяц"):
				return now.AddDate(0, -n, 0)
			default:
				return now.AddDate(0, 0, -n)
			}
		}
		if t, err := time.Parse(time.RFC3339, spec); err == nil {
			return t
		}
		return now.AddDate(0, 0, -30)
	}
}

var weekdayForms = []struct {
	Stem string
	Day  time.Weekday
}{
	{"понедельник", time.Monday}, {"вторник", time.Tuesday}, {"сред", time.Wednesday}, {"четверг", time.Thursday},
	{"пятниц", time.Friday}, {"суббот", time.Saturday}, {"воскресень", time.Sunday},
}

var monthForms = []string{"янва", "февра", "мар", "апре", "ма", "июн", "июл", "авгус", "сентябр", "октябр", "ноябр", "декабр"}

// parseFutureDate — сроки для напоминаний: «завтра», «в пятницу», «через 3
// дня», «10 сентября», «утром» — время 09:00, если не сказано иное.
// regexpAfterDay — «после 15-го», «после 15 числа».
var regexpAfterDay = regexp.MustCompile(`после\s+(\d{1,2})(?:-?го|-?ого|\s+числа)?`)

func parseFutureDate(s string, now time.Time) (time.Time, bool) {
	minute := 0
	at := func(d time.Time, h int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), h, minute, 0, 0, now.Location())
	}
	hour := 9
	if strings.Contains(s, "вечер") {
		hour = 18
	}
	if strings.Contains(s, "днём") || strings.Contains(s, "днем") || strings.Contains(s, "в обед") {
		hour = 13
	}
	// «в 16:30» — час и минуты; раньше минуты терялись и напоминание
	// на 16:30 вставало на 16:00
	explicitTime := false
	if m := regexpHour.FindStringSubmatch(s); m != nil {
		if h, err := strconv.Atoi(m[1]); err == nil && h >= 0 && h < 24 {
			hour, explicitTime = h, true
			if m[2] != "" {
				if mm, err := strconv.Atoi(m[2]); err == nil && mm >= 0 && mm < 60 {
					minute = mm
				}
			}
		}
	}
	// точное время, как его пишет модель: «2026-10-05 10:00»
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, true
		}
	}
	switch {
	case strings.Contains(s, "сегодня"):
		t := at(now, hour)
		if t.Before(now) && !explicitTime {
			t = at(now, 18) // «сегодня» без времени, а утро прошло — вечером
		}
		return t, true
	case strings.Contains(s, "послезавтра"):
		return at(now.AddDate(0, 0, 2), hour), true
	case strings.Contains(s, "завтра"):
		return at(now.AddDate(0, 0, 1), hour), true
	case strings.HasPrefix(s, "через "):
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(s, "через "), "%d", &n); err != nil {
			n = 1
			if strings.Contains(s, "пар") { // «через пару часов»
				n = 2
			}
		}
		switch {
		case strings.Contains(s, "полчаса"):
			return now.Add(30 * time.Minute), true
		case strings.Contains(s, "минут"):
			// раньше минут разбор не знал вовсе: «через 30 минут» уходило в
			// ветку дней и становилось «через 30 дней»
			return now.Add(time.Duration(n) * time.Minute), true
		case strings.Contains(s, "час"):
			return now.Add(time.Duration(n) * time.Hour), true
		case strings.Contains(s, "недел"):
			return at(now.AddDate(0, 0, 7*n), hour), true
		case strings.Contains(s, "месяц"):
			return at(now.AddDate(0, n, 0), hour), true
		default:
			return at(now.AddDate(0, 0, n), hour), true
		}
	}
	// «на выходных» — ближайшая суббота, «на следующей неделе» — понедельник
	if strings.Contains(s, "выходн") {
		days := (int(time.Saturday) - int(now.Weekday()) + 7) % 7
		if days == 0 && at(now, 10).Before(now) {
			days = 7
		}
		if !explicitTime {
			hour = 10
		}
		return at(now.AddDate(0, 0, days), hour), true
	}
	if strings.Contains(s, "следующ") && strings.Contains(s, "недел") {
		days := (int(time.Monday) - int(now.Weekday()) + 7) % 7
		if days == 0 {
			days = 7
		}
		return at(now.AddDate(0, 0, days), hour), true
	}
	// «после 15-го», «после 15 числа» — шестнадцатое этого месяца или следующего
	if m := regexpAfterDay.FindStringSubmatch(s); m != nil {
		if d, err := strconv.Atoi(m[1]); err == nil && d >= 1 && d <= 31 {
			t := at(time.Date(now.Year(), now.Month(), d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1), hour)
			if !t.After(now) {
				t = at(time.Date(now.Year(), now.Month()+1, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1), hour)
			}
			return t, true
		}
	}
	for _, w := range weekdayForms {
		if strings.Contains(s, w.Stem) {
			days := (int(w.Day) - int(now.Weekday()) + 7) % 7
			if days == 0 {
				days = 7
			}
			return at(now.AddDate(0, 0, days), hour), true
		}
	}
	// «10 сентября», «10.09», «2026-09-10»
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return at(t, hour), true
	}
	var day int
	if _, err := fmt.Sscanf(s, "%d", &day); err == nil && day >= 1 && day <= 31 {
		for i, m := range monthForms {
			if strings.Contains(s, " "+m) {
				t := time.Date(now.Year(), time.Month(i+1), day, hour, 0, 0, 0, now.Location())
				if t.Before(now) && !strings.Contains(s, strconv.Itoa(now.Year())) {
					t = t.AddDate(1, 0, 0)
				}
				return t, true
			}
		}
		var mo int
		if _, err := fmt.Sscanf(s, "%d.%d", &day, &mo); err == nil && mo >= 1 && mo <= 12 {
			t := time.Date(now.Year(), time.Month(mo), day, hour, 0, 0, 0, now.Location())
			if t.Before(now) {
				t = t.AddDate(1, 0, 0)
			}
			return t, true
		}
	}
	if explicitTime {
		t := at(now, hour)
		if t.Before(now) {
			t = at(now.AddDate(0, 0, 1), hour)
		}
		return t, true
	}
	// «вечером», «утром», «днём» без дня — сегодня, а прошло — завтра
	if strings.Contains(s, "вечер") || strings.Contains(s, "утр") || hour == 13 {
		if strings.Contains(s, "утр") {
			hour = 9
		}
		t := at(now, hour)
		if !t.After(now) {
			t = at(now.AddDate(0, 0, 1), hour)
		}
		return t, true
	}
	return time.Time{}, false
}

var regexpHour = regexp.MustCompile(`в (\d{1,2})(?::(\d{2}))?`)

// «script.js: Line 4:1», «at script.js:4:1», «(4:1)» — форматы разные
var rePos = regexp.MustCompile(`(?i)(?:line\s+|:|\()(\d+):(\d+)\)?`)

// errPosition достаёт номер строки из сообщения движка.
// shift — поправка на обёртку, в которую мы кладём код.
func errPosition(msg string, shift int) (line int, text string) {
	text = strings.TrimSpace(strings.SplitN(msg, "\n", 2)[0])
	if m := rePos.FindStringSubmatch(msg); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			line = n - shift
		}
	}
	// убираем служебные хвосты вида "at script.js:3:5"
	text = strings.TrimSpace(regexp.MustCompile(`\s+at .*$`).ReplaceAllString(text, ""))
	// служебное имя файла и позицию из текста убираем — они уже в полях
	text = strings.TrimSpace(regexp.MustCompile(`(?i)script\.js:?\s*(line\s+\d+:\d+)?`).ReplaceAllString(text, ""))
	return line, strings.TrimPrefix(text, ": ")
}

// codeLine возвращает саму строку кода — её показываем рядом с ошибкой.
func codeLine(code string, line int) string {
	if line <= 0 {
		return ""
	}
	lines := strings.Split(code, "\n")
	if line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}
