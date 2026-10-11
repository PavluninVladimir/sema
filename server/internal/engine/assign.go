package engine

// Поручения: работа «до готовности» (изменение work-until-done).
//
// 4 октября 2026 владелец попросил из Telegram модуль, который ходит в
// электронный дневник и собирает оценки. Такая работа за одно обращение не
// делается: найти клиент, вынести адрес в настройки, пароли в секреты,
// написать код, запустить, прочитать ошибку, поправить. Сёма отвечал «изучу
// и соберу» — и на этом всё: «потом» у него не существовало.
//
// Теперь модель на такую просьбу присылает план блоком ```sema-plan```, ядро
// заводит поручение (store.Assignment) и само даёт модели ход по каждому
// шагу задачей «Поручения» — тем же путём, что пинатель: заметка ядра и
// ответ модели в ветку чата. Шаг кончается блоком ```sema-step:done|wait|fail```;
// без него ход — попытка, после assign.step_turns попыток поручение ждёт
// человека. Изменяющее по-прежнему ждёт кнопки, а пока она висит, ход не
// начинается.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	WorkerAssign = "assignments"

	assignConfirmKey       = "assign.confirm"             // начинать поручение только кнопкой «Начать»
	assignMaxStepsKey      = "assign.max_steps"           // шагов в плане не больше
	assignStepTurnsKey     = "assign.step_turns"          // ходов на шаг без его итога, потом — человеку
	assignDaysKey          = "assign.days"                // сколько дней поручение идёт само
	assignTrackerKey       = "assign.tracker"             // сервер MCP трекера: auto, имя или пусто
	assignTrackerProjKey   = "assign.tracker_project"     // проект трекера для новых задач
	assignTrackerCreateKey = "assign.tracker_create_tool" // инструмент создания задачи
	assignTrackerDoneKey   = "assign.tracker_done_tool"   // инструмент «задача сделана»
	assignTrackerCancelKey = "assign.tracker_cancel_tool" // инструмент «задача отменена»

	assignTrackerAuto = "auto"
)

var (
	planBlock = regexp.MustCompile("(?s)```sema-plan[^\n]*\n(.*?)```")
	stepBlock = regexp.MustCompile("(?s)```sema-step:?[ \t]*([^\\s`]*)[ \t]*([^\n`]*)\n?(.*?)```")
	stepLine  = regexp.MustCompile(`^\s*(?:\d+[.)]|[-*•])\s+(.+)$`)
	trackerID = regexp.MustCompile(`"id"\s*:\s*"([^"]+)"`)
)

// AssignCard — карточка поручения в переписке. Рисуется по базе: в
// сообщении лежит только номер, поэтому карточка всегда показывает нынешний
// ход, а не тот, что был при её появлении.
type AssignCard struct {
	ID      int64    `json:"id"`
	Kind    string   `json:"kind,omitempty"` // "" — план; "secret" — нужен секрет; "done" — закрыто
	Secrets []string `json:"secrets,omitempty"`
}

// assignTurnState — ход дало ядро, а не человек: итог шага уходит в
// Telegram, а ход без итога считается попыткой.
type assignTurnState struct {
	id      int64
	stepped bool
}

type assignTurnKey struct{}

func assignTurnOf(ctx context.Context) *assignTurnState {
	st, _ := ctx.Value(assignTurnKey{}).(*assignTurnState)
	return st
}

// AssignLimits — настройки поручений так, как их читает движок; экран
// показывает то же самое.
type AssignLimits struct {
	Confirm              bool
	MaxSteps, StepTurns  int
	Days                 int
	Tracker, TrackerProj string
	CreateTool, DoneTool string
	CancelTool           string
}

func (e *Engine) AssignLimits() AssignLimits {
	return AssignLimits{
		Confirm:     e.store.SettingFlag(assignConfirmDef),
		MaxSteps:    e.store.SettingInt(assignMaxStepsDef),
		StepTurns:   e.store.SettingInt(assignStepTurnsDef),
		Days:        e.store.SettingInt(assignDaysDef),
		Tracker:     e.store.SettingText(assignTrackerDef),
		TrackerProj: e.store.SettingText(assignTrackerProjDef),
		CreateTool:  e.store.SettingText(assignTrackerCreateDef),
		DoneTool:    e.store.SettingText(assignTrackerDoneDef),
		CancelTool:  e.store.SettingText(assignTrackerCancelDef),
	}
}

// ---- план ----

type assignPlan struct {
	goal, project, task string
	steps               []string
}

// parsePlan — блок плана: «цель: …», пронумерованные шаги, необязательные
// «проект: …» (проект трекера) и «задача: …» (уже заведённая задача).
func parsePlan(body string) assignPlan {
	var p assignPlan
	var loose []string
	for _, raw := range strings.Split(body, "\n") {
		// жирный и курсив Markdown модель ставит где придётся: «**Цель:** …»
		line := strings.NewReplacer("**", "", "__", "").Replace(raw)
		line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "*_"))
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if v, ok := cutLabel(line, low, "цель", "goal"); ok {
			p.goal = v
			continue
		}
		if v, ok := cutLabel(line, low, "проект", "project"); ok {
			p.project = v
			continue
		}
		if v, ok := cutLabel(line, low, "задача", "task"); ok {
			// 11 октября 2026 модель написала «задача: 8» — номер своего же
			// прошлого поручения, не трекера; зеркало в трекере не завелось,
			// а «сделано» ушло бы на несуществующий номер
			if !numericOnly(v) {
				p.task = v
			}
			continue
		}
		if m := stepLine.FindStringSubmatch(line); m != nil {
			p.steps = append(p.steps, strings.TrimSpace(strings.Trim(m[1], "*_ ")))
			continue
		}
		loose = append(loose, line)
	}
	if p.goal == "" && len(loose) > 0 {
		p.goal = loose[0]
	}
	if p.goal == "" && len(p.steps) > 0 {
		p.goal = p.steps[0]
	}
	return p
}

func cutLabel(line, low string, names ...string) (string, bool) {
	for _, n := range names {
		if strings.HasPrefix(low, n+":") {
			return strings.TrimSpace(line[len(n)+1:]), true
		}
	}
	return "", false
}

// findAssignBlocks — блоки поручения в ответе модели: план заводит или
// правит поручение, итог шага двигает его. Возвращает ответ без блоков.
func (e *Engine) findAssignBlocks(ctx context.Context, chatID int64, answer string) string {
	if m := planBlock.FindStringSubmatch(answer); m != nil {
		e.takePlan(ctx, chatID, m[1])
		answer = planBlock.ReplaceAllString(answer, "")
	}
	calls := hasToolCalls(answer)
	for _, m := range stepBlock.FindAllStringSubmatch(answer, -1) {
		// «жду» рядом со своими же вызовами — это ожидание их результата, а
		// не человека: результат вернётся модели в этом же ответе. 4 октября
		// 2026 модель позвала task_list и тут же написала «жду» — и хозяину в
		// Telegram ушёл вопрос «нужен результат поиска задачи»
		if k := stepKind(m[1]); calls && (k == "wait" || k == "fail") {
			log.Printf("чат %d: «%s» рядом с вызовами инструментов — ждём их результата, а не человека", chatID, k)
			continue
		}
		e.takeStep(ctx, chatID, m[1], strings.TrimSpace(m[2]+" "+m[3]))
	}
	return strings.TrimSpace(stepBlock.ReplaceAllString(answer, ""))
}

func (e *Engine) takePlan(ctx context.Context, chatID int64, body string) {
	p := parsePlan(body)
	if len(p.steps) == 0 {
		log.Printf("чат %d: план без шагов — поручение не заведено", chatID)
		return
	}
	if max := e.store.SettingInt(assignMaxStepsDef); len(p.steps) > max {
		p.steps = p.steps[:max]
	}
	if a, ok := e.store.OpenAssignmentOf(chatID); ok {
		// новый план в том же чате — правка: сделанное остаётся, несделанное
		// заменяется. Человек поправил — снова в работу
		if err := e.store.ReplaceAssignSteps(a.ID, p.steps); err != nil {
			log.Printf("поручение %d: новый план: %v", a.ID, err)
			return
		}
		if p.goal != "" {
			a.Goal = p.goal
		}
		a.StepTries = 0
		if a.State == store.AssignWaiting {
			a.State, a.Question, a.WaitSince, a.WaitSecrets = store.AssignActive, "", "", ""
		}
		_ = e.store.SaveAssignment(a)
		_ = e.saveToolCard(chatID, ToolCard{Title: "Поручение · план поправлен", Assign: &AssignCard{ID: a.ID}})
		log.Printf("поручение %d: план поправлен, шагов %d", a.ID, len(p.steps))
		return
	}
	a := &store.Assignment{Chat: chatID, Goal: p.goal, TrackerTask: p.task, TrackerProject: p.project, State: store.AssignActive}
	if e.store.SettingFlag(assignConfirmDef) {
		a.State = store.AssignDraft
	}
	if _, err := e.store.AddAssignment(a, p.steps); err != nil {
		log.Printf("чат %d: поручение не заведено: %v", chatID, err)
		return
	}
	if full, ok := e.store.Assignment(a.ID); ok {
		a = full // шаги — для заметки задачи в трекере
	}
	_ = e.saveToolCard(chatID, ToolCard{Title: "Поручение", Assign: &AssignCard{ID: a.ID}})
	log.Printf("чат %d: заведено поручение %d «%s», шагов %d", chatID, a.ID, clip(a.Goal, 60), len(p.steps))
	// хозяин в Telegram карточки не видит — план уходит туда строками
	var b strings.Builder
	fmt.Fprintf(&b, "Поручение: %s\n", a.Goal)
	for i, st := range p.steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, st)
	}
	if a.State == store.AssignDraft {
		b.WriteString("Начну, когда нажмёте «Начать» в Сёме.")
	} else {
		b.WriteString("Иду по шагам сам и пишу сюда, что сделано.")
	}
	e.assignTell(ctx, chatID, b.String(), true)
	if a.State == store.AssignActive {
		e.assignTrackerStart(ctx, a)
	}
}

// takeStep — итог текущего шага: сделан, ждёт человека или не удался.
func (e *Engine) takeStep(ctx context.Context, chatID int64, kind, text string) {
	a, ok := e.store.OpenAssignmentOf(chatID)
	if !ok {
		return
	}
	// остановленное человеком (и ждущее «Начать») шагов не отмечает: 4
	// октября 2026 на паузе модель отметила последний шаг ответом на побочный
	// вопрос — и закрыла поручение с недоделанным модулем
	if a.State == store.AssignPaused || a.State == store.AssignDraft {
		log.Printf("поручение %d на паузе: отметка шага «%s» не принята", a.ID, clip(text, 80))
		return
	}
	if st := assignTurnOf(ctx); st != nil {
		st.stepped = true
	}
	cur := a.Current()
	if cur == nil {
		e.closeAssignment(ctx, a, text)
		return
	}
	done, total := a.Progress()
	switch stepKind(kind) {
	case "go":
		// человек ответил на вопрос — работа идёт дальше со следующего хода
		if a.State == store.AssignWaiting {
			a.State, a.Question, a.WaitSince, a.WaitSecrets, a.StepTries, a.WaitMsg, a.WakeAt = store.AssignActive, "", "", "", 0, 0, ""
			a.Since = time.Now().UTC().Format(time.RFC3339)
			_ = e.store.SaveAssignment(a)
			log.Printf("поручение %d снова в работе: человек ответил", a.ID)
		}
	case "later":
		// человек отложил на любой срок — поручение ждёт молча и само
		// возвращается в работу в названное время
		e.assignLater(a, text)
	case "wait":
		if text == "" {
			text = "нужен ваш ответ по шагу «" + cur.Text + "»"
		}
		e.assignWait(ctx, a, text, "")
	case "fail":
		a.StepTries++
		log.Printf("поручение %d: шаг %d не удался (%d): %s", a.ID, cur.N, a.StepTries, clip(text, 120))
		if a.StepTries >= e.store.SettingInt(assignStepTurnsDef) {
			e.assignWait(ctx, a, fmt.Sprintf("Шаг %d «%s» не выходит: %s. Что делать?", cur.N, cur.Text, firstNonEmpty(text, "причина не названа")), "")
			return
		}
		_ = e.store.SaveAssignment(a)
	default:
		if text == "" {
			text = "сделано"
		}
		if err := e.store.FinishAssignStep(a.ID, cur.N, store.StepDone, text); err != nil {
			log.Printf("поручение %d: шаг %d: %v", a.ID, cur.N, err)
			return
		}
		a.StepTries = 0
		_ = e.store.SaveAssignment(a)
		e.assignTell(ctx, chatID, fmt.Sprintf("Шаг %d из %d: %s", done+1, total, text), false)
		if next, ok := e.store.Assignment(a.ID); ok && next.Current() == nil {
			e.closeAssignment(ctx, next, text)
		}
	}
}

// hasToolCalls — в ответе есть вызовы, чьи результаты вернутся модели:
// команды, поиск, серверы MCP, скрипты.
func hasToolCalls(answer string) bool {
	for _, mark := range []string{"```sema-mcp", "```sema-ssh", "```sema-search"} {
		if strings.Contains(answer, mark) {
			return true
		}
	}
	return scriptBlock.MatchString(answer)
}

func stepKind(kind string) string {
	switch strings.ToLower(strings.Trim(kind, ":")) {
	case "go", "resume", "continue", "продолжить", "дальше":
		return "go"
	case "later", "snooze", "потом", "позже", "отложить", "отложено":
		return "later"
	case "wait", "ждёт", "жду", "вопрос", "ask":
		return "wait"
	case "fail", "failed", "error", "ошибка", "неудача":
		return "fail"
	}
	return "done"
}

// assignLater — человек отложил поручение: срок из блока (точное время,
// которое пишет модель, или слова «через час», «на выходных»), до него —
// тишина, в срок — снова в работе. Срок не понят — завтра в 10:00, а не
// «вечно ждёт».
func (e *Engine) assignLater(a *store.Assignment, text string) {
	when, note := text, ""
	for _, sep := range []string{" — ", " - ", " – ", ";"} {
		if i := strings.Index(text, sep); i > 0 {
			when, note = strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+len(sep):])
			break
		}
	}
	at, ok := script.ParseFutureDate(when)
	if !ok {
		at, ok = script.ParseFutureDate(text)
	}
	if !ok || !at.After(time.Now()) {
		t := time.Now().AddDate(0, 0, 1)
		at = time.Date(t.Year(), t.Month(), t.Day(), 10, 0, 0, 0, time.Local)
	}
	a.State, a.WakeAt = store.AssignWaiting, at.Format(time.RFC3339)
	a.Question = firstNonEmpty(note, "отложено человеком")
	a.WaitSince = time.Now().UTC().Format(time.RFC3339)
	a.WaitMsg = e.store.LastChatMsgID()
	_ = e.store.SaveAssignment(a)
	log.Printf("поручение %d отложено до %s: %s", a.ID, at.Format("02.01 15:04"), clip(a.Question, 80))
}

// assignWait — поручение ждёт человека: вопрос в чат и в Telegram.
func (e *Engine) assignWait(ctx context.Context, a *store.Assignment, question, secrets string) {
	a.State, a.Question, a.WaitSecrets, a.WakeAt = store.AssignWaiting, question, secrets, ""
	a.WaitSince = time.Now().UTC().Format(time.RFC3339)
	a.WaitMsg = e.store.LastChatMsgID()
	_ = e.store.SaveAssignment(a)
	log.Printf("поручение %d ждёт человека: %s", a.ID, clip(question, 120))
	// вопрос из хода ядра — в Telegram сам; из ответа человеку он и так
	// стоит в тексте ответа
	e.assignTell(ctx, a.Chat, "Нужен ваш ответ: "+question, false)
}

// closeAssignment — поручение закрыто: несделанные шаги пропущены, итог в
// чат и в Telegram, задача в трекере отмечена.
func (e *Engine) closeAssignment(ctx context.Context, a *store.Assignment, summary string) {
	if !a.Open() {
		return
	}
	for _, st := range a.Steps {
		if st.State == store.StepTodo {
			_ = e.store.FinishAssignStep(a.ID, st.N, store.StepSkipped, "")
		}
	}
	if summary = strings.TrimSpace(summary); summary == "" {
		summary = "поручение выполнено"
	}
	a.State, a.Summary, a.Question, a.WaitSecrets = store.AssignDone, summary, "", ""
	a.Closed = time.Now().UTC().Format(time.RFC3339)
	_ = e.store.SaveAssignment(a)
	_ = e.saveToolCard(a.Chat, ToolCard{Title: "Поручение выполнено", Result: summary, Assign: &AssignCard{ID: a.ID, Kind: "done"}})
	log.Printf("поручение %d закрыто: %s", a.ID, clip(summary, 120))
	e.assignTell(ctx, a.Chat, "Поручение «"+a.Goal+"» выполнено: "+summary, false)
	e.assignTrackerDone(ctx, a)
}

// ---- что видит модель ----

// assignHint — как просить о большой работе: план, а не обещание.
func assignHint() string {
	return "Work that needs more than one answer (an integration module, several steps with a check on live data) " +
		"is never promised for later. Reply with a plan block instead:\n" +
		"```sema-plan\nцель: <one line>\n1. <step you can do yourself with blocks>\n2. <next step>\n```\n" +
		"If the conversation already shows the id of a task tracker task for this work, add a line `задача: <its id>` — Sema then uses it instead of creating another one; " +
		"never search the tracker for it and never make that a step: Sema mirrors the assignment to the tracker itself. " +
		"Sema then works through the steps itself, one turn per step, and reports to the user. " +
		"Steps are actions with blocks (search, a script with sema.web.page, sema-module, sema-mcp); " +
		"a step that needs the user is a question. Passwords never go into the chat: a module gets them as named secrets.\n"
}

// assignmentContext — поручение этого чата в промпте: цель, шаги, что
// сейчас. В ход ядра — ещё и указание сделать шаг сейчас.
func (e *Engine) assignmentContext(ctx context.Context, chatID int64) string {
	a, ok := e.store.OpenAssignmentOf(chatID)
	if !ok {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nAssignment #%d in this chat: «%s» (%s).\nSteps:\n", a.ID, a.Goal, assignStateWord(a.State))
	cur := a.Current()
	for _, st := range a.Steps {
		mark := "todo"
		switch {
		case st.State == store.StepDone:
			mark = "done"
		case st.State == store.StepSkipped:
			mark = "skipped"
		case cur != nil && st.N == cur.N:
			mark = "now"
		}
		line := fmt.Sprintf("%d. [%s] %s", st.N, mark, st.Text)
		if st.Result != "" {
			line += " — " + clip(st.Result, 200)
		}
		b.WriteString(line + "\n")
	}
	if a.State == store.AssignWaiting && a.Question != "" {
		b.WriteString("The assignment waits for the user: " + a.Question + "\n")
		if assignTurnOf(ctx) == nil {
			fmt.Fprintf(&b, "If the user's message gives what was asked and the work can go on now, end your answer with ```sema-step:go```. "+
				"If the user postpones it to any time — «завтра», «через час», «через 30 минут», «вечером», «в понедельник», «на выходных», «после 15-го» and so on — "+
				"end with ```sema-step:later <YYYY-MM-DD HH:MM> — <what we wait for>``` giving the exact local time (now it is %s); "+
				"Sema stays silent until then and comes back to the assignment itself. "+
				"If the user writes about something else, just answer that. In both cases do not ask the question again.\n",
				time.Now().Format("2006-01-02 15:04, Monday"))
			return b.String()
		}
	}
	if assignTurnOf(ctx) != nil && cur != nil {
		fmt.Fprintf(&b, "Internal turn: nobody wrote. Do step %d now, with blocks. ", cur.N)
		if a.StepTries > 0 {
			fmt.Fprintf(&b, "Earlier turns on this step (%d) ended without its result. ", a.StepTries)
		}
		b.WriteString("Results of the tools you call come back to you within this same answer — never write sema-step:wait for them. " +
			"Finish the answer with exactly one block: ```sema-step:done <result in one line>``` when the step is done, " +
			"```sema-step:wait <question to the user>``` only when you need the user, ```sema-step:fail <reason>``` when it failed. " +
			"A new sema-plan block changes the remaining steps; ```sema-done:<result>``` closes the whole assignment.\n")
	} else if a.State == store.AssignPaused || a.State == store.AssignDraft {
		b.WriteString("The assignment is not running: answer the user, do not mark steps and do not close it.\n")
	} else {
		b.WriteString("If the user's message answers your question or changes the plan, act on it. ")
		if cur != nil {
			fmt.Fprintf(&b, "Write ```sema-step:done <result>``` only if this very answer finished and checked step %d «%s» itself; "+
				"answering a side question or a small fix is not that step — then write no step block.\n", cur.N, cur.Text)
		} else {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func assignStateWord(state string) string {
	switch state {
	case store.AssignDraft:
		return "waits for «Start»"
	case store.AssignPaused:
		return "paused by the user"
	case store.AssignWaiting:
		return "waits for the user"
	}
	return "in progress"
}

// ---- ход по шагам ----

// assignPass — задача «Поручения»: каждому идущему поручению — ход по
// текущему шагу, ждущему — проверка, не ответил ли человек.
func (e *Engine) assignPass(ctx context.Context) string {
	list, err := e.store.Assignments(0, store.AssignActive, store.AssignWaiting)
	if err != nil {
		return "поручения не прочитаны: " + err.Error()
	}
	if len(list) == 0 {
		return "поручений нет"
	}
	started, waiting, resumed := 0, 0, 0
	days := e.store.SettingInt(assignDaysDef)
	for i := range list {
		a := &list[i]
		if a.State == store.AssignWaiting {
			if !e.assignResumable(a) {
				waiting++
				continue
			}
			a.State, a.Question, a.WaitSince, a.WaitSecrets, a.StepTries, a.WaitMsg, a.WakeAt = store.AssignActive, "", "", "", 0, 0, ""
			// человек ответил — срок «идёт само» считается заново, иначе
			// поручение старше срока спрашивало бы «продолжать?» по кругу
			a.Since = time.Now().UTC().Format(time.RFC3339)
			_ = e.store.SaveAssignment(a)
			resumed++
			log.Printf("поручение %d снова в работе", a.ID)
		}
		if t, err := time.Parse(time.RFC3339, firstNonEmpty(a.Since, a.Created)); err == nil && time.Since(t) > time.Duration(days)*24*time.Hour {
			e.assignWait(ctx, a, fmt.Sprintf("Поручение идёт дольше %d дн. Продолжать? Ответьте здесь — или отмените его в Сёме.", days), "")
			waiting++
			continue
		}
		if a.Current() == nil {
			e.closeAssignment(ctx, a, "")
			continue
		}
		if thinking, _ := e.Thinking(a.Chat); thinking {
			continue
		}
		// кнопка человека или тикающий таймер — ход подождёт
		if e.waitingForHuman(a.Chat) || e.timerPending(a.Chat) {
			continue
		}
		if e.assignTurn(a) {
			started++
		}
	}
	return fmt.Sprintf("ходов: %d · ждут человека: %d · вернулись в работу: %d", started, waiting, resumed)
}

// assignResumable — ждущее поручение можно продолжать: заведены секреты,
// которых ждали. Ответ человека сам по себе работу не возвращает: 4 октября
// 2026 любое сообщение в ветке (про другой баг, «завтра сделаю», «хорошо»)
// будило поручение, и оно снова слало тот же вопрос. Вернуть его в работу
// решает модель, отвечая человеку, — блоком ```sema-step:go```.
func (e *Engine) assignResumable(a *store.Assignment) bool {
	if a.WakeAt != "" {
		if t, err := time.Parse(time.RFC3339, a.WakeAt); err == nil && !time.Now().Before(t) {
			return true // настал срок, на который человек отложил
		}
	}
	if names := splitCommaList(a.WaitSecrets); len(names) > 0 {
		all := true
		for _, n := range names {
			if _, ok := e.store.Secret(n); !ok {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// assignTurn — ход модели по текущему шагу, как ход пинателя, но со своим
// указанием в промпте и разбором итога. false — чат уже занят.
func (e *Engine) assignTurn(a *store.Assignment) bool {
	chatID := a.Chat
	e.thinkingMu.Lock()
	if e.thinking == nil {
		e.thinking = map[int64]bool{}
	}
	if e.thinking[chatID] {
		e.thinkingMu.Unlock()
		return false
	}
	e.thinking[chatID] = true
	e.thinkingMu.Unlock()
	a.Turns++
	a.LastTurn = time.Now().UTC().Format(time.RFC3339)
	_ = e.store.SaveAssignment(a)
	e.Changed(store.ScreenChat(chatID))
	id := a.ID
	run := func() {
		st := &assignTurnState{id: id}
		ctx := context.WithValue(llm.WithConsumer(context.Background(), "assign:"+strconv.FormatInt(id, 10)), assignTurnKey{}, st)
		answer, err := e.chatAnswerStream(ctx, chatID, "")
		if err != nil {
			answer = "Не получилось сделать шаг: " + err.Error()
		}
		answer = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(answer), "Sema:"))
		if answer != "" {
			if err := e.store.AddChatMsg(chatID, "assistant", answer); err != nil { // в тред уже выбран
				log.Printf("поручение %d: ответ: %v", id, err)
			}
		}
		e.thinkingMu.Lock()
		delete(e.thinking, chatID)
		delete(e.partial, chatID)
		e.thinkingMu.Unlock()
		e.Changed(store.ScreenChat(chatID))
		e.flushCards(chatID)
		e.afterAssignTurn(ctx, id, st, err)
	}
	if e.assignSync {
		run()
	} else {
		go run()
	}
	return true
}

// afterAssignTurn — ход кончился без итога шага: это попытка; попыток
// больше предела — поручение ждёт человека с вопросом.
func (e *Engine) afterAssignTurn(ctx context.Context, id int64, st *assignTurnState, err error) {
	a, ok := e.store.Assignment(id)
	if !ok || a.State != store.AssignActive || st.stepped {
		return
	}
	cur := a.Current()
	if cur == nil {
		return
	}
	a.StepTries++
	limit := e.store.SettingInt(assignStepTurnsDef)
	if a.StepTries >= limit {
		why := fmt.Sprintf("Шаг %d «%s» не сдвинулся за %d ход(а).", cur.N, cur.Text, a.StepTries)
		if err != nil {
			why += " Последняя ошибка: " + clip(err.Error(), 200) + "."
		}
		e.assignWait(ctx, a, why+" Подскажите, что мешает, или поправьте план.", "")
		return
	}
	_ = e.store.SaveAssignment(a)
}

// ---- секреты модуля ----

// assignNeedsSecrets — модуль, поставленный в чате с поручением, просит
// секреты, которых нет: поручение ждёт, человеку — карточка с переходом на
// добавление секрета. Пароль в чат не просим никогда.
func (e *Engine) assignNeedsSecrets(ctx context.Context, chatID int64, m *Module) {
	a, ok := e.store.OpenAssignmentOf(chatID)
	if !ok || m == nil {
		return
	}
	var missing []string
	for _, n := range m.Secrets {
		if _, ok := e.store.Secret(n); !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return
	}
	_ = e.saveToolCard(chatID, ToolCard{Title: "Нужен секрет", Assign: &AssignCard{ID: a.ID, Kind: "secret", Secrets: missing}})
	q := fmt.Sprintf("Модулю «%s» нужны секреты: %s. Заведите их в Сёме («Настройки → Ключи и секреты»), "+
		"пароль сюда не присылайте — работа продолжится сама.", m.Title, strings.Join(missing, ", "))
	e.assignWait(ctx, a, q, strings.Join(missing, ","))
	// вопрос ушёл в чат карточкой, а в Telegram — строкой только из хода
	// ядра; из ответа человеку модель его и так назовёт
}

// SecretAddScreen — экран добавления секрета с вписанным именем.
func SecretAddScreen(name string) string { return SecretScreen("new", "name:"+name) }

// ---- Telegram ----

// assignTell — строка о ходе поручения хозяину в Telegram, если поручение
// идёт в ветке админ-чата. Подробности остаются в ветке. Из ответа человеку
// (force=false) не шлём: ответ и так уходит в Telegram целиком.
func (e *Engine) assignTell(ctx context.Context, chatID int64, text string, force bool) {
	if !force && assignTurnOf(ctx) == nil {
		return
	}
	lane := e.laneOf(chatID)
	if lane == chatID {
		return // не ветка разговора — Telegram тут ни при чём
	}
	peer, ok := e.tgPeerOfChat(lane)
	if !ok || !peer.Admin {
		return
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	// тот же путь, что у ответа хозяину: по соединению слушателя, а своё
	// сообщение, отправленное по нему, эхом не приходит — в архив его
	// кладёт отправивший; слушателя нет — прежней отправкой
	out, err := e.store.AddTGOutbox(&store.TGOutbox{Account: peer.Account, ChatID: peer.ID, Topic: peer.Topic,
		SemaChat: lane, Who: peer.Title, Text: text})
	if err != nil {
		log.Printf("поручение: журнал отправленного: %v", err)
		return
	}
	if talk := e.tgTalkWith(peer, true); talk != nil {
		var id int
		if id, err = talk.tr.SendText(sctx, talk.peer, text); err == nil {
			talk.archive(id, text)
		}
	} else {
		err = e.TGSendApproved(sctx, peer.Account, peer.ID, peer.Topic, text)
	}
	if err != nil {
		_ = e.store.DecideTGOutbox(out, "rejected", err.Error())
		log.Printf("поручение: в Telegram не ушло: %v", err)
		return
	}
	_ = e.store.DecideTGOutbox(out, "sent", "поручение")
	_ = e.store.AddChatMsg(lane, "sent", text) // видно, что ушло хозяину
}

// ---- трекер ----

// assignTrackerServer — сервер MCP, где поручение заводит задачу: из
// настройки, а «auto» — первый открытый чату сервер с инструментом создания.
func (e *Engine) assignTrackerServer() string {
	want := strings.TrimSpace(e.store.SettingText(assignTrackerDef))
	tool := strings.TrimSpace(e.store.SettingText(assignTrackerCreateDef))
	if want == "" || tool == "" {
		return ""
	}
	for _, m := range e.mcpChatServers() {
		if want != assignTrackerAuto {
			if strings.EqualFold(m.Name, want) {
				return m.Name
			}
			continue
		}
		if !m.Chat {
			continue
		}
		for _, t := range m.ToolInfo {
			if t.Name == tool {
				return m.Name
			}
		}
	}
	return ""
}

// assignTrackerStart — задача в трекере при старте. Вызов без чата: если он
// ждёт кнопки, шаги поручения не стоят (ядро не ходит, пока в чате ждёт
// кнопка), а номер задачи придёт после кнопки.
func (e *Engine) assignTrackerStart(ctx context.Context, a *store.Assignment) {
	server := e.assignTrackerServer()
	if server == "" {
		return
	}
	a.Tracker = server
	if a.TrackerTask != "" {
		_ = e.store.SaveAssignment(a)
		return
	}
	args := map[string]any{"title": a.Goal, "note": assignPlanText(a)}
	if proj := firstNonEmpty(a.TrackerProject, e.store.SettingText(assignTrackerProjDef)); strings.TrimSpace(proj) != "" {
		args["projectId"] = strings.TrimSpace(proj)
	}
	res := e.RequestMCPFrom(ctx, MCPOrigin{Who: fmt.Sprintf("поручение #%d", a.ID)}, server,
		e.store.SettingText(assignTrackerCreateDef), args)
	switch {
	case res.PendingID > 0:
		a.TrackerPending = res.PendingID
	case res.Err != "":
		log.Printf("поручение %d: задача в трекере не заведена: %s", a.ID, res.Err)
	default:
		a.TrackerTask = trackerTaskID(res.Text)
	}
	_ = e.store.SaveAssignment(a)
}

// assignTrackerDone — задача в трекере сделана. Номера ещё нет (создание
// ждёт кнопки) — отметим после неё.
func (e *Engine) assignTrackerDone(ctx context.Context, a *store.Assignment) {
	if a.Tracker == "" || a.TrackerTask == "" {
		return
	}
	res := e.RequestMCPFrom(ctx, MCPOrigin{Who: fmt.Sprintf("поручение #%d", a.ID)}, a.Tracker,
		e.store.SettingText(assignTrackerDoneDef), map[string]any{"id": a.TrackerTask})
	if res.PendingID > 0 {
		a.TrackerPending = res.PendingID
		_ = e.store.SaveAssignment(a)
	} else if res.Err != "" {
		log.Printf("поручение %d: задача в трекере не отмечена: %s", a.ID, res.Err)
	}
}

// assignTrackerApproved — человек нажал кнопку у вызова трекера: номер
// созданной задачи забираем из ответа, а если поручение тем временем
// закрылось — отмечаем задачу сделанной.
func (e *Engine) assignTrackerApproved(ctx context.Context, pending int64, text string) {
	a, ok := e.store.AssignmentByTrackerPending(pending)
	if !ok {
		return
	}
	a.TrackerPending = 0
	created := a.TrackerTask == ""
	if created {
		a.TrackerTask = trackerTaskID(text)
	}
	_ = e.store.SaveAssignment(a)
	if created && !a.Open() {
		e.assignTrackerDone(ctx, a)
	}
}

// assignTrackerCancel — поручение отменено, и зеркало в трекере тоже
// отменяется, а не висит открытым: 11 октября 2026 два снятых поручения
// оставили в Planner незакрытые задачи. Инструмент пустой — трекер
// отмены не знает, ничего не шлём.
func (e *Engine) assignTrackerCancel(ctx context.Context, a *store.Assignment) {
	tool := strings.TrimSpace(e.store.SettingText(assignTrackerCancelDef))
	if a.Tracker == "" || a.TrackerTask == "" || tool == "" {
		return
	}
	res := e.RequestMCPFrom(ctx, MCPOrigin{Who: fmt.Sprintf("поручение #%d", a.ID)}, a.Tracker,
		tool, map[string]any{"id": a.TrackerTask})
	if res.PendingID > 0 {
		a.TrackerPending = res.PendingID
		_ = e.store.SaveAssignment(a)
	} else if res.Err != "" {
		log.Printf("поручение %d: задача в трекере не отменена: %s", a.ID, res.Err)
	}
}

// numericOnly — строка из одних цифр: так выглядит номер поручения Сёмы,
// а не задачи трекера.
func numericOnly(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func trackerTaskID(text string) string {
	if m := trackerID.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// assignPlanText — план строками для заметки задачи в трекере.
func assignPlanText(a *store.Assignment) string {
	var b strings.Builder
	for _, st := range a.Steps {
		fmt.Fprintf(&b, "%d. %s\n", st.N, st.Text)
	}
	return strings.TrimSpace(b.String())
}

// ---- действия человека ----

// reopenAssignment — закрытое поручение снова в работе: шаг, который его
// закрыл, и пропущенные — несделанные, отметка задачи в трекере, ждущая
// кнопки, отклоняется (её просило само поручение).
func (e *Engine) reopenAssignment(ctx context.Context, a *store.Assignment) (string, error) {
	if a.Open() {
		return "Поручение и так идёт.", nil
	}
	last := 0
	for _, st := range a.Steps {
		if st.State == store.StepDone && st.N > last {
			last = st.N
		}
	}
	for _, st := range a.Steps {
		if st.State == store.StepSkipped || (st.State == store.StepDone && st.N == last && a.State == store.AssignDone) {
			_ = e.store.ReopenAssignStep(a.ID, st.N)
		}
	}
	if a.TrackerPending > 0 {
		if p, ok := e.store.MCPPendingByID(a.TrackerPending); ok && p.State == "waiting" &&
			p.Tool == e.store.SettingText(assignTrackerDoneDef) {
			_, _ = e.ApproveMCP(ctx, a.TrackerPending, false)
		}
		a.TrackerPending = 0
	}
	a.State, a.Summary, a.Closed, a.StepTries = store.AssignActive, "", "", 0
	a.Since = time.Now().UTC().Format(time.RFC3339)
	_ = e.store.SaveAssignment(a)
	_ = e.saveToolCard(a.Chat, ToolCard{Title: "Поручение снова в работе", Assign: &AssignCard{ID: a.ID}})
	log.Printf("поручение %d возвращено в работу", a.ID)
	return "Поручение снова в работе.", nil
}

// AssignmentAction — «Начать», «Остановить», «Продолжить», «Отменить».
func (e *Engine) AssignmentAction(ctx context.Context, id int64, act string) (string, error) {
	a, ok := e.store.Assignment(id)
	if !ok {
		return "", fmt.Errorf("поручения %d нет", id)
	}
	if act == "reopen" {
		return e.reopenAssignment(ctx, a)
	}
	if !a.Open() {
		return "", fmt.Errorf("поручение уже закрыто")
	}
	switch act {
	case "start":
		if a.State != store.AssignDraft {
			return "Поручение уже идёт.", nil
		}
		a.State = store.AssignActive
		_ = e.store.SaveAssignment(a)
		e.assignTrackerStart(ctx, a)
		return "Поручение начато: Сёма пойдёт по шагам сам.", nil
	case "pause":
		a.State = store.AssignPaused
		_ = e.store.SaveAssignment(a)
		return "Поручение остановлено.", nil
	case "resume":
		a.State, a.Question, a.WaitSince, a.WaitSecrets, a.StepTries = store.AssignActive, "", "", "", 0
		a.Since = time.Now().UTC().Format(time.RFC3339)
		_ = e.store.SaveAssignment(a)
		return "Поручение продолжается.", nil
	case "cancel":
		a.State = store.AssignCancelled
		a.Closed = time.Now().UTC().Format(time.RFC3339)
		_ = e.store.SaveAssignment(a)
		e.assignTrackerCancel(ctx, a)
		_ = e.saveToolCard(a.Chat, ToolCard{Title: "Поручение отменено", Result: a.Goal, Assign: &AssignCard{ID: a.ID, Kind: "done"}})
		return "Поручение отменено.", nil
	}
	return "", fmt.Errorf("неизвестное действие %q", act)
}
