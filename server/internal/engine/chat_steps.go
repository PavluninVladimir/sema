package engine

// Шаги чата: «найти, потом сделать» за одно обращение человека.
//
// 4 октября 2026 Сёма в админ-чате Telegram пять сообщений подряд отвечал
// «Проверю список проектов» и ничего не делал. `project_list` у Planner
// вернул 16 тысяч знаков, модель видела первые 6000, нужный проект был за
// обрезкой — и она звала тот же список снова. А читающий вызов второго хода
// выполнялся, но его результат модель уже не видела: третьего хода не было.
//
// Отсюда три правила: длинный список приходит модели выжимкой (номер и
// название каждой записи), тот же вызов дважды не исполняется, а читающие
// вызовы идут шагами до предела `chats.read_steps`. Четвёртое — про ответ
// без дела: «Хорошо, сделаем» без единого блока не уходит человеку, модель
// один раз получает напоминание.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	chatReadStepsKey      = "chats.read_steps"      // ходов модели за одно обращение, пока она зовёт читающие инструменты
	chatPromiseMarkersKey = "chats.promise_markers" // слова, которыми модель обещает действие
	chatClaimMarkersKey   = "chats.claim_markers"   // слова, которыми модель говорит, что действие уже сделано
	mcpChatListFieldsKey  = "mcp.chat_list_fields"  // поля записи в выжимке длинного списка для модели
)

// Умолчания справочников — первичное заполнение, правится на экранах.
const (
	mcpListFieldsDefault = "id, title, name, projectId, parent, state, checked"
	// 4 октября 2026 в 11:11 «Изучу клиент и документацию, затем соберу
	// модуль» ушло человеку: первый засев знал только «проверю» и родню
	promiseMarkersDefault = "проверю, сделаю, создам, заведу, поищу, займусь, сейчас посмотрю, сейчас сделаю, сейчас проверю, " +
		"изучу, соберу, напишу, допишу, подготовлю, реализую, разберусь, приступаю, приступлю, начну, оформлю, " +
		"добавлю, настрою, исправлю, доделаю, сделаем, соберём, напишем, подготовим, займёмся, изучим, проверим"
)

func (e *Engine) chatReadSteps() int { return e.store.SettingInt(chatReadStepsDef) }

// ChatReadSteps — сколько ходов модель получает за одно обращение (экран
// «Работа Сёмы → Чаты и ветки» показывает то же, что читает движок).
func (e *Engine) ChatReadSteps() int { return e.chatReadSteps() }

// PromiseMarkers — слова обещания строкой, как их правит человек.
func (e *Engine) PromiseMarkers() string { return strings.Join(e.promiseMarkers(), ", ") }

func (e *Engine) mcpListFields() []string {
	return splitCommaList(e.store.SettingText(mcpChatListFieldsDef))
}

// claimMarkersDefault — «сделано» без дела: 4 октября 2026 модель ответила
// «Создание бага в Planner отправлено на подтверждение» без единого блока
// — задачи не было, кнопки тоже.
//
// «…» в примете — разрыв до нескольких слов: «отправил … на подтверждение»
// ловит и «отправил в Planner на подтверждение» — так модель обошла
// примету в тот же вечер.
const claimMarkersDefault = "отправлено … на подтверждение, отправил … на подтверждение, ждёт вашей кнопки, ждёт подтверждения, " +
	"создал … задачу, завёл … задачу, задача … создана, задача … заведена, баг … заведён, создал … баг, завёл … баг, добавил … задачу"

func (e *Engine) claimMarkers() []string {
	return splitCommaList(e.store.SettingText(chatClaimMarkersDef))
}

// ClaimMarkers — слова «уже сделано» строкой, как их правит человек.
func (e *Engine) ClaimMarkers() string { return strings.Join(e.claimMarkers(), ", ") }

func (e *Engine) promiseMarkers() []string {
	return splitCommaList(e.store.SettingText(chatPromiseMarkersDef))
}

// splitCommaList — справочник строкой «а, б, в» в список без пустых.
func splitCommaList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- состояние одного обращения ----

// chatTurnState — что известно о вызовах в пределах одного обращения
// человека: сколько вызовов ждёт кнопки на текущем ходу и какие вызовы уже
// были (ключ → текст результата для модели).
type chatTurnState struct {
	pending int
	calls   map[string]string
}

type chatTurnKey struct{}

func withChatTurn(ctx context.Context) (context.Context, *chatTurnState) {
	st := &chatTurnState{calls: map[string]string{}}
	return context.WithValue(ctx, chatTurnKey{}, st), st
}

func chatTurn(ctx context.Context) *chatTurnState {
	st, _ := ctx.Value(chatTurnKey{}).(*chatTurnState)
	return st
}

// notePending — вызов на этом ходу ждёт кнопки человека.
func notePending(ctx context.Context) {
	if st := chatTurn(ctx); st != nil {
		st.pending++
	}
}

// callKey — вызов инструмента одной строкой: сервер, инструмент и аргументы
// в одном порядке (json.Marshal сортирует ключи карты).
func callKey(server, tool string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	if len(args) == 0 {
		raw = nil
	}
	return strings.ToLower(server) + "\x00" + tool + "\x00" + string(raw)
}

// ---- выжимка длинного списка ----

// mcpPageArgs — аргументы порции, которые принимает инструмент: по ним
// модель просит следующую часть списка, а не тот же список снова.
func (e *Engine) mcpPageArgs(server, tool string) []string {
	m, ok := e.store.MCPServer(server)
	if !ok {
		return nil
	}
	for _, t := range m.ToolInfo {
		if t.Name != tool || len(t.Schema) == 0 {
			continue
		}
		var sc struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(t.Schema, &sc) != nil {
			return nil
		}
		var out []string
		for _, name := range []string{"offset", "maxCount", "limit", "page", "pageSize", "cursor"} {
			if _, ok := sc.Properties[name]; ok {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}

// mcpModelText — что модель увидит из ответа инструмента: длинный список —
// выжимкой, остальное — как раньше, обрезкой с названным остатком.
func (e *Engine) mcpModelText(server, tool, text string, limit int) string {
	if short, ok := compactMCPList(text, e.mcpListFields(), limit, e.mcpPageArgs(server, tool)); ok {
		return short
	}
	return clipNamed(text, limit)
}

// compactMCPList — выжимка списка записей для модели. Список — JSON-массив
// объектов или объект, в котором такой массив есть (берётся самый длинный);
// перед JSON бывает строка вроде «Showing all 43 project items. Items:», она
// остаётся заголовком. Не список или короче предела — false: текст идёт как
// раньше.
func compactMCPList(text string, fields []string, limit int, pageArgs []string) (string, bool) {
	if len([]rune(text)) <= limit || len(fields) == 0 {
		return "", false
	}
	start := strings.IndexAny(text, "[{")
	if start < 0 {
		return "", false
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(text[start:]))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	items, extra := listIn(v)
	if len(items) == 0 {
		return "", false
	}
	var b strings.Builder
	if head := strings.TrimSpace(text[:start]); head != "" {
		b.WriteString(clip(head, 200) + "\n")
	}
	for _, line := range extra {
		b.WriteString(line + "\n")
	}
	fmt.Fprintf(&b, "Список сжат для тебя: всего %d записей, у каждой — только поля %s. Полный ответ — в карточке у человека; подробности о записи — тем же сервером, инструментом *_get по её id.",
		len(items), strings.Join(fields, ", "))
	if len(pageArgs) > 0 {
		fmt.Fprintf(&b, " Следующая порция — тот же инструмент с аргументами %s.", strings.Join(pageArgs, "/"))
	}
	b.WriteString("\n")
	used := len([]rune(b.String()))
	shown := 0
	for _, it := range items {
		line := "- " + recordLine(it, fields) + "\n"
		n := len([]rune(line))
		if used+n > limit && shown > 0 {
			break
		}
		b.WriteString(line)
		used += n
		shown++
	}
	if rest := len(items) - shown; rest > 0 {
		fmt.Fprintf(&b, "… ещё %d записей не поместились: попроси следующую порцию или уточни запрос.\n", rest)
	}
	return b.String(), true
}

// listIn — записи списка и простые поля обёртки (итог, «обрезано»). Массив
// без объектов списком записей не считается.
func listIn(v any) ([]map[string]any, []string) {
	switch x := v.(type) {
	case []any:
		return objects(x), nil
	case map[string]any:
		var best []map[string]any
		var extra []string
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch inner := x[k].(type) {
			case []any:
				if objs := objects(inner); len(objs) > len(best) {
					best = objs
				}
			case string, json.Number, bool:
				extra = append(extra, fmt.Sprintf("%s: %v", k, clip(fmt.Sprint(inner), 80)))
			}
		}
		if len(best) == 0 {
			return nil, nil
		}
		return best, extra
	}
	return nil, nil
}

func objects(list []any) []map[string]any {
	var out []map[string]any
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// recordLine — запись одной строкой по полям выжимки; нет ни одного — первые
// три простых поля, чтобы запись не превратилась в пустую строку.
func recordLine(m map[string]any, fields []string) string {
	var parts []string
	for _, f := range fields {
		if v, ok := m[f]; ok && v != nil {
			if s := scalar(v); s != "" {
				parts = append(parts, f+": "+s)
			}
		}
	}
	if len(parts) == 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s := scalar(m[k]); s != "" {
				parts = append(parts, k+": "+s)
				if len(parts) == 3 {
					break
				}
			}
		}
	}
	return strings.Join(parts, " · ")
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return clip(strings.TrimSpace(x), 120)
	case json.Number, bool:
		return fmt.Sprint(x)
	}
	return ""
}

// ---- обещание без действия ----

// isPromise — объявляет ли текст действие словом из справочника. Сравнение
// без регистра и без различия «е» и «ё», по началу слова. «…» (или «...»)
// в примете — разрыв: между частями до markerGap знаков.
func isPromise(text string, markers []string) bool {
	low := normYo(strings.ToLower(text))
	for _, m := range markers {
		m = normYo(strings.ToLower(strings.TrimSpace(m)))
		if m == "" {
			continue
		}
		if strings.Contains(m, "…") || strings.Contains(m, "...") {
			if re := gapMarker(m); re != nil && re.MatchString(low) {
				return true
			}
			continue
		}
		for i := 0; ; {
			j := strings.Index(low[i:], m)
			if j < 0 {
				break
			}
			at := i + j
			if at == 0 || !isLetterBefore(low[:at]) {
				return true
			}
			i = at + len(m)
		}
	}
	return false
}

func normYo(s string) string { return strings.ReplaceAll(s, "ё", "е") }

// markerGap — сколько знаков может стоять в разрыве приметы: «в Planner»,
// «в проекте «Сёма»» — а не через предложение.
const markerGap = 40

// gapMarker — примета с разрывами как выражение: части по порядку, первая —
// с начала слова, между частями до markerGap знаков. Примета из одних
// разрывов («…» опечаткой в справочнике) приметой не считается: иначе она
// ловила бы любой ответ.
func gapMarker(m string) *regexp.Regexp {
	m = strings.ReplaceAll(m, "...", "…")
	var parts []string
	for _, p := range strings.Split(m, "…") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, regexp.QuoteMeta(p))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return regexp.MustCompile(`(?s)(^|[^\p{L}])` + strings.Join(parts, `.{0,`+strconv.Itoa(markerGap)+`}?`))
}

// mcpReadingTool — инструмент по имени читает: те же образцы, что были у
// прежнего круга чтения (`task_list`, `get_my_context`, по границам слов).
func mcpReadingTool(tool string) bool {
	return legacyClassifyMCP(mcpSeed.read, nil, tool) == PolicyAllow
}

// outwardCard — карточка хода, за которой стоит дело снаружи или кнопка
// человека: вызов MCP, команда, Telegram — или скрипт, который их зовёт.
// Скрипт, который только искал файл, «отправил на подтверждение» не
// оправдывает: 4 октября 2026 так и было — нашёл фото и сказал, что
// задача ушла на кнопку.
func outwardCard(c ToolCard) bool {
	if c.Err != "" {
		return false // упал — до вызова мог и не дойти
	}
	for _, api := range []string{"sema.mcp", "sema.ssh", "sema.telegram", "sema.max"} {
		if strings.Contains(c.Code, api) {
			return true
		}
	}
	return false
}

func isLetterBefore(s string) bool {
	r := []rune(s)
	return len(r) > 0 && unicode.IsLetter(r[len(r)-1])
}

// hasBlocks — есть ли в ответе модели хоть один блок для ядра.
func hasBlocks(answer string) bool { return strings.Contains(answer, "```sema") }

var anyBlock = regexp.MustCompile("(?s)```sema[^\n]*\n.*?```")

// stripBlocks — текст ответа без блоков: то, что видит человек, когда блоки
// не исполняются (повтор того же хода).
func stripBlocks(answer string) string {
	return strings.TrimSpace(anyBlock.ReplaceAllString(answer, ""))
}

// promiseReminder — ход, которым ядро напоминает модели об обещанном.
const promiseReminder = "\n(Note from Sema's core, not from the user: your previous answer promised an action but contains no block, " +
	"so nothing was done. Do the action now with the right block, or say plainly what is missing. Do not promise again. " +
	"To study a page (code on GitHub, documentation), write a script that reads it with sema.web.page(url) — its result comes back to you; " +
	"to build or change a module, write the sema-module block now, even as a first working version.)\nSema:"

// answersHuman — ответ идёт на сообщение человека, а не продолжает работу
// после кнопки, таймера или пинка: там «задача заведена» — правда, вызов
// выполнила кнопка, просто в этом ответе блока нет.
func (e *Engine) answersHuman(chatID int64) bool {
	msgs, err := e.store.ChatMsgs(chatID, 1)
	return err == nil && len(msgs) == 1 && msgs[0].Role == "user"
}

// claimReminder — ход, которым ядро говорит модели, что заявленное ею
// действие не случилось.
const claimReminder = "\n(Note from Sema's core, not from the user: your previous answer says an action was done or sent for confirmation, " +
	"but this answer contains no block — nothing happened. Do the action now with the right block, " +
	"or tell the user plainly that it was not done.)\nSema:"

// stepsNote — сколько ещё модель может звать читающие инструменты в этом
// ответе. На последнем ходу — прежнее правило: результаты покажут карточки.
func stepsNote(left int) string {
	if left > 0 {
		return fmt.Sprintf("You may call more read-only tools in this answer; their results come back to you (up to %d more turns). "+
			"Changes still wait for the user's button.\n", left)
	}
	return "Results of any further calls in this answer are shown to the user as cards, not returned to you.\n"
}
