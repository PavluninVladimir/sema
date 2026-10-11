package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const planДневник = "Сделаю это поручением.\n```sema-plan\nцель: модуль электронного дневника\n1. найти клиент и описание входа\n2. написать модуль с настройками\n```"

func (f *mcpChatFix) assignment(t *testing.T) *store.Assignment {
	t.Helper()
	list, err := f.st.Assignments(0)
	if err != nil || len(list) == 0 {
		t.Fatalf("поручений нет: %v", err)
	}
	a, _ := f.st.Assignment(list[0].ID)
	return a
}

func (f *mcpChatFix) pass(t *testing.T) string {
	t.Helper()
	f.e.assignSync = true
	return f.e.assignPass(context.Background())
}

func stepStates(a *store.Assignment) string {
	var out []string
	for _, st := range a.Steps {
		out = append(out, fmt.Sprintf("%d:%s", st.N, st.State))
	}
	return strings.Join(out, " ")
}

// План → шаги → закрытие: человек попросил один раз, дальше Сёма идёт сам.
func TestAssignPlanStepsClose(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник,
		"Нашёл клиент.\n```sema-step:done клиент example/school-api, вход формой\n```",
		"Модуль стоит выключенным.\n```sema-step:done модуль «Дневник» поставлен\n```")
	f.ask(t, "напиши модуль, который ходит в дневник и собирает оценки")

	a := f.assignment(t)
	if a.State != store.AssignActive || a.Goal != "модуль электронного дневника" || len(a.Steps) != 2 {
		t.Fatalf("поручение после плана: %+v", a)
	}
	if strings.Contains(f.lastAnswer(t), "sema-plan") {
		t.Error("блок плана остался в ответе")
	}
	f.pass(t)
	if a = f.assignment(t); stepStates(a) != "1:done 2:todo" || a.Steps[0].Result != "клиент example/school-api, вход формой" {
		t.Fatalf("после первого хода: %s %+v", stepStates(a), a.Steps)
	}
	f.pass(t)
	a = f.assignment(t)
	if a.State != store.AssignDone || stepStates(a) != "1:done 2:done" || a.Summary != "модуль «Дневник» поставлен" {
		t.Fatalf("после второго хода: %s %+v", stepStates(a), a)
	}
	if got := len(f.model.Prompts()); got != 3 {
		t.Errorf("модель звали %d раз, ждали три: план и два шага", got)
	}
	// ход ядра шёл с указанием сделать шаг сейчас
	if p := f.model.Prompts()[1]; !strings.Contains(p, "Internal turn") || !strings.Contains(p, "Do step 1 now") {
		t.Error("ход шага без указания сделать шаг")
	}
	// закрытое больше не ходит
	if res := f.pass(t); !strings.Contains(res, "поручений нет") {
		t.Errorf("после закрытия: %s", res)
	}
}

// Ход без итога шага — попытка; попыток больше предела — поручение ждёт
// человека, а ответ человека возвращает его в работу.
func TestAssignStuckStepWaitsForHuman(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник, "Думаю над первым шагом.", "Всё ещё думаю.",
		"Понял, продолжаю.\n```sema-step:go\n```", "Нашёл.\n```sema-step:done клиент найден\n```")
	if err := f.st.SetSetting(assignStepTurnsKey, "2"); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "напиши модуль для дневника")
	f.pass(t)
	if a := f.assignment(t); a.State != store.AssignActive || a.StepTries != 1 {
		t.Fatalf("после первой пустой попытки: %+v", a)
	}
	f.pass(t)
	a := f.assignment(t)
	if a.State != store.AssignWaiting || !strings.Contains(a.Question, "не сдвинулся за 2") {
		t.Fatalf("после второй попытки поручение не ждёт человека: %+v", a)
	}
	// ждёт — значит ходов нет
	before := len(f.model.Prompts())
	f.pass(t)
	if len(f.model.Prompts()) != before {
		t.Fatal("ждущее поручение получило ход")
	}
	// человек ответил — снова в работе, и ход идёт
	f.ask(t, "клиент есть на GitHub, example/school-api")
	f.pass(t)
	if a := f.assignment(t); stepStates(a) != "1:done 2:todo" || a.State != store.AssignActive {
		t.Fatalf("после ответа человека: %s %+v", stepStates(a), a)
	}
}

// Пока в чате ждёт кнопки изменяющий вызов, хода нет.
func TestAssignWaitsForButton(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник,
		"Заведу задачу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Дневник\"}}\n```",
		"Этот ход случиться не должен.")
	f.ask(t, "напиши модуль для дневника")
	f.pass(t)
	statuses := []string{}
	for _, c := range f.cards(t) {
		statuses = append(statuses, c.MCP.Tool+":"+c.MCP.Status)
	}
	if strings.Join(statuses, " ") != "task_create:pending" {
		t.Fatalf("карточки: %v", statuses)
	}
	before := len(f.model.Prompts())
	f.pass(t)
	if len(f.model.Prompts()) != before {
		t.Fatal("ход начался, хотя в чате ждёт кнопка")
	}
}

// Новый план в том же чате: сделанное остаётся, несделанное заменяется.
func TestAssignReplanKeepsDone(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник,
		"Нашёл.\n```sema-step:done клиент найден\n```",
		"Поправил план.\n```sema-plan\nцель: модуль дневника для двух детей\n1. настройки детей\n2. проба на одном ребёнке\n3. расписание\n```")
	f.ask(t, "напиши модуль для дневника")
	f.pass(t)
	f.ask(t, "детей двое, сделай отдельно для каждого")
	a := f.assignment(t)
	if a.Goal != "модуль дневника для двух детей" || stepStates(a) != "1:done 2:todo 3:todo 4:todo" || a.Steps[1].Text != "настройки детей" {
		t.Fatalf("после нового плана: %s %+v", stepStates(a), a.Steps)
	}
	if n, _ := f.st.Assignments(0); len(n) != 1 {
		t.Errorf("новый план завёл второе поручение: %d", len(n))
	}
}

// «Готово» в чате с поручением закрывает поручение и не ставит чату срок
// жизни: ветке «Сёма» срок стёр бы переписку через час.
func TestDoneClosesAssignmentNotChat(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник, "Всё сделано.\n```sema-done:модуль поставлен и проверен\n```")
	f.ask(t, "напиши модуль для дневника")
	f.ask(t, "как дела?")
	a := f.assignment(t)
	if a.State != store.AssignDone || a.Summary != "модуль поставлен и проверен" || stepStates(a) != "1:skipped 2:skipped" {
		t.Fatalf("поручение после «готово»: %s %+v", stepStates(a), a)
	}
	if v := f.st.Setting(fmt.Sprintf("chat.expires.%d", f.chatID), ""); v != "" {
		t.Errorf("чату поставлен срок жизни %s", v)
	}
}

// И без поручения «готово» не ставит срок чату, у которого его не было.
func TestDoneDoesNotExpireOrdinaryChat(t *testing.T) {
	f := newMCPChatFix(t, true, "Готово.\n```sema-done:вопрос решён\n```")
	f.ask(t, "реши вопрос")
	if v := f.st.Setting(fmt.Sprintf("chat.expires.%d", f.chatID), ""); v != "" {
		t.Errorf("обычному чату поставлен срок жизни %s", v)
	}
}

// Модуль поручения просит секрет, которого нет: поручение ждёт, карточка
// ведёт на добавление секрета, а заведённый секрет возвращает работу.
func TestAssignWaitsForModuleSecret(t *testing.T) {
	module := "Модуль готов.\n```sema-module:dnevnik\n```json\n{\"name\": \"dnevnik\", \"title\": \"Дневник\", \"secrets\": [\"school:pupil\"]}\n```\n" +
		"```javascript\nfunction run() { return sema.secret('school:pupil') ? 1 : 0; }\n```\n```"
	f := newMCPChatFix(t, true, planДневник, module, "Проверил вход.\n```sema-step:done вход работает\n```")
	f.ask(t, "напиши модуль для дневника")
	f.pass(t)
	a := f.assignment(t)
	if a.State != store.AssignWaiting || a.WaitSecrets != "school:pupil" {
		t.Fatalf("поручение не ждёт секрет: %+v", a)
	}
	var card *AssignCard
	msgs, _ := f.st.ChatMsgs(f.chatID, 100)
	for _, m := range msgs {
		if c, ok := ParseToolCard(m.Content); ok && c.Assign != nil && c.Assign.Kind == "secret" {
			card = c.Assign
		}
	}
	if card == nil || len(card.Secrets) != 1 || card.Secrets[0] != "school:pupil" {
		t.Fatalf("карточки «нужен секрет» нет: %+v", card)
	}
	if got := SecretAddScreen("school:pupil"); got != "settings:secrets@new~name:school:pupil" {
		t.Errorf("переход на добавление: %s", got)
	}
	before := len(f.model.Prompts())
	f.pass(t)
	if len(f.model.Prompts()) != before {
		t.Fatal("ход начался без секрета")
	}
	if err := f.e.AddSecret("school:pupil", store.SecretToken, "пароль-понарошку"); err != nil {
		t.Fatal(err)
	}
	f.pass(t)
	if a := f.assignment(t); a.State != store.AssignActive || stepStates(a) != "1:done 2:todo" {
		t.Fatalf("секрет заведён, а работа не пошла: %s %+v", stepStates(a), a)
	}
}

// Трекер: создание задачи ждёт кнопки и не держит шаги; номер задачи
// приходит после кнопки, при закрытии задача отмечается сделанной.
func TestAssignTrackerMirror(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник,
		"Нашёл.\n```sema-step:done клиент найден\n```",
		"Готово.\n```sema-step:done модуль поставлен\n```")
	// у поддельного Planner нет task_complete: «сделано» — архивом задачи
	if err := f.st.SetSetting(assignTrackerDoneKey, "task_archive"); err != nil {
		t.Fatal(err)
	}
	f.srv.CallReply = func(name string, _ map[string]any) string {
		if name == "task_create" {
			return `{"id":"T-42","title":"модуль электронного дневника"}`
		}
		return `{"ok":true}`
	}
	f.ask(t, "напиши модуль для дневника")
	a := f.assignment(t)
	if a.Tracker != "planner" || a.TrackerPending == 0 || a.TrackerTask != "" {
		t.Fatalf("задача в трекере при старте: %+v", a)
	}
	// кнопка трекера не держит ход: вызов не привязан к чату
	f.pass(t)
	if a = f.assignment(t); stepStates(a) != "1:done 2:todo" {
		t.Fatalf("ждущий вызов трекера остановил шаги: %s", stepStates(a))
	}
	if _, err := f.e.ApproveMCP(context.Background(), a.TrackerPending, true); err != nil {
		t.Fatal(err)
	}
	if a = f.assignment(t); a.TrackerTask != "T-42" || a.TrackerPending != 0 {
		t.Fatalf("номер задачи после кнопки: %+v", a)
	}
	f.pass(t)
	a = f.assignment(t)
	if a.State != store.AssignDone || a.TrackerPending == 0 {
		t.Fatalf("при закрытии отметка задачи не попросилась: %+v", a)
	}
	p, ok := f.st.MCPPendingByID(a.TrackerPending)
	if !ok || p.Tool != "task_archive" || !strings.Contains(p.Args, "T-42") || p.ChatID != 0 {
		t.Fatalf("вызов закрытия задачи: %+v", p)
	}
}

// Без трекера — без зеркала, и поручение работает так же.
func TestAssignWithoutTracker(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник, "Нашёл.\n```sema-step:done клиент найден\n```")
	if err := f.st.SetSetting(assignTrackerKey, ""); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "напиши модуль для дневника")
	if a := f.assignment(t); a.Tracker != "" || a.TrackerPending != 0 {
		t.Fatalf("трекер выключен, а задача заводится: %+v", a)
	}
	f.pass(t)
	if a := f.assignment(t); stepStates(a) != "1:done 2:todo" {
		t.Fatalf("без трекера шаги не идут: %s", stepStates(a))
	}
}

// Запуск по кнопке: поручение ждёт «Начать», ходов до неё нет.
func TestAssignConfirmStart(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник, "Нашёл.\n```sema-step:done клиент найден\n```")
	if err := f.st.SetSetting(assignConfirmKey, "1"); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "напиши модуль для дневника")
	a := f.assignment(t)
	if a.State != store.AssignDraft {
		t.Fatalf("без кнопки поручение не ждёт: %+v", a)
	}
	before := len(f.model.Prompts())
	f.pass(t)
	if len(f.model.Prompts()) != before {
		t.Fatal("ход до «Начать»")
	}
	if _, err := f.e.AssignmentAction(context.Background(), a.ID, "start"); err != nil {
		t.Fatal(err)
	}
	f.pass(t)
	if a := f.assignment(t); stepStates(a) != "1:done 2:todo" {
		t.Fatalf("после «Начать»: %s", stepStates(a))
	}
	if _, err := f.e.AssignmentAction(context.Background(), a.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if a := f.assignment(t); a.State != store.AssignCancelled {
		t.Fatalf("после «Отменить»: %+v", a)
	}
}

// Поручение из админ-чата Telegram: план и строка каждого шага уходят
// хозяину в Telegram, подробности остаются в ветке.
func TestAssignTellsTelegram(t *testing.T) {
	model := &scriptedModel{answers: []string{planДневник,
		"Нашёл клиент, вот разбор.\n```sema-step:done клиент example/school-api\n```"}}
	s := newTalk(t, model)
	if err := s.ask(t, "напиши модуль для дневника"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.st.Assignments(0)
	if len(list) != 1 {
		t.Fatalf("поручение из Telegram не заведено: %d", len(list))
	}
	s.e.assignSync = true
	s.e.assignPass(context.Background())
	var sent []string
	for _, c := range s.fake.snapshot() {
		if c.kind == "send" || c.kind == "edit" {
			sent = append(sent, c.text)
		}
	}
	all := strings.Join(sent, "\n---\n")
	for _, want := range []string{"Поручение: модуль электронного дневника", "1. найти клиент", "Шаг 1 из 2: клиент example/school-api"} {
		if !strings.Contains(all, want) {
			t.Errorf("в Telegram нет %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "вот разбор") {
		t.Error("подробности хода ушли в Telegram, а должна была уйти одна строка шага")
	}
}

func TestParsePlan(t *testing.T) {
	p := parsePlan("**Цель:** модуль дневника\nпроект: P-1\nзадача: T-9\n1. найти клиент\n2) написать код\n- проба на живом\n")
	if p.goal != "модуль дневника" || p.project != "P-1" || p.task != "T-9" ||
		strings.Join(p.steps, "|") != "найти клиент|написать код|проба на живом" {
		t.Fatalf("разбор плана: %+v", p)
	}
	if q := parsePlan("1. первый\n2. второй"); q.goal != "первый" || len(q.steps) != 2 {
		t.Fatalf("план без цели: %+v", q)
	}
	// «задача: 8» — номер поручения Сёмы, а не задачи трекера
	if r := parsePlan("цель: модуль\nзадача: 8\n1. шаг"); r.task != "" {
		t.Fatalf("номер поручения принят за задачу трекера: %q", r.task)
	}
}

// Просьба про модуль для дневника подмешивает скилл «Модуль-интеграция»
// целиком: план поручения идёт по его порядку.
func TestModuleIntegrationSkillMixedIn(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	text, used := e.skillsPrompt("напиши модуль, который ходит в электронный дневник и собирает оценки")
	found := false
	for _, n := range used {
		if n == "module-integration" {
			found = true
		}
	}
	if !found || !strings.Contains(text, "Пароль в чат не проси никогда") {
		t.Fatalf("скилл не подмешан: %v", used)
	}
}

// Поручение старше срока ждёт человека, а его ответ даёт новый срок: без
// этого оно спрашивало бы «продолжать?» на каждом проходе.
func TestAssignDeadlineAskedOnce(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник, "Понял, продолжаю.\n```sema-step:go\n```", "Нашёл.\n```sema-step:done клиент найден\n```")
	f.ask(t, "напиши модуль для дневника")
	a := f.assignment(t)
	a.Since = "2026-01-01T00:00:00Z"
	if err := f.st.SaveAssignment(a); err != nil {
		t.Fatal(err)
	}
	f.pass(t)
	if a = f.assignment(t); a.State != store.AssignWaiting || !strings.Contains(a.Question, "Продолжать?") {
		t.Fatalf("старое поручение не спросило: %+v", a)
	}
	f.ask(t, "да, продолжай")
	f.pass(t)
	if a = f.assignment(t); a.State != store.AssignActive || stepStates(a) != "1:done 2:todo" {
		t.Fatalf("после «продолжай»: %s %+v", stepStates(a), a)
	}
}

// Модель знает, как модулю завести окно настроек: образец экрана и
// сохранения в подсказке о модулях, без несуществующего права «data».
func TestModulePromptHasSettingsScreenSample(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	p := e.chatScriptsPrompt()
	for _, want := range []string{"settingsScreen", "module_action", "~saveSettings", "sema.data.put('settings', 'main'"} {
		if !strings.Contains(p, want) {
			t.Errorf("в подсказке о модулях нет %q", want)
		}
	}
	if strings.Contains(p, "store, data,") || strings.Contains(p, "needs ui and data") {
		t.Error("подсказка просит право «data», которого нет")
	}
}

// На паузе отметки шагов не принимаются: 4 октября 2026 модель ответом на
// побочный вопрос отметила последний шаг и закрыла недоделанное поручение.
func TestPausedAssignmentIgnoresSteps(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник, "Вынес школу в настройки.\n```sema-step:done школа в настройках\n```")
	f.ask(t, "напиши модуль для дневника")
	a := f.assignment(t)
	if _, err := f.e.AssignmentAction(context.Background(), a.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "вынеси школу в настройки")
	if a = f.assignment(t); a.State != store.AssignPaused || stepStates(a) != "1:todo 2:todo" {
		t.Fatalf("на паузе шаг отмечен: %s %+v", stepStates(a), a)
	}
	if p := f.model.Prompts()[1]; !strings.Contains(p, "do not mark steps") {
		t.Error("модель не знает, что поручение на паузе")
	}
}

// В ответе человеку «сделано» — только про сам текущий шаг, названный.
func TestHumanTurnStepRuleNamesTheStep(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник, "Ответ.")
	f.ask(t, "напиши модуль для дневника")
	f.ask(t, "как дела?")
	p := f.model.Prompts()[1]
	if !strings.Contains(p, "only if this very answer finished and checked step 1 «найти клиент и описание входа»") {
		t.Errorf("правило отметки шага без названного шага:\n%s", p[strings.LastIndex(p, "Assignment #"):])
	}
}

// «Вернуть в работу»: шаг, закрывший поручение, снова несделан, отметка
// задачи в трекере, ждущая кнопки, отклонена.
func TestReopenAssignment(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник,
		"Нашёл.\n```sema-step:done клиент найден\n```",
		"Готово.\n```sema-step:done модуль поставлен\n```")
	if err := f.st.SetSetting(assignTrackerDoneKey, "task_archive"); err != nil {
		t.Fatal(err)
	}
	f.srv.CallReply = func(name string, _ map[string]any) string { return `{"id":"T-7"}` }
	f.ask(t, "напиши модуль для дневника")
	a := f.assignment(t)
	// план — в заметке задачи трекера
	if p, ok := f.st.MCPPendingByID(a.TrackerPending); !ok || !strings.Contains(p.Args, "1. найти клиент") {
		t.Fatalf("заметка задачи в трекере без плана: %+v", p)
	}
	if _, err := f.e.ApproveMCP(context.Background(), a.TrackerPending, true); err != nil {
		t.Fatal(err)
	}
	f.pass(t)
	f.pass(t)
	a = f.assignment(t)
	done := a.TrackerPending
	if a.State != store.AssignDone || done == 0 {
		t.Fatalf("поручение не закрылось: %+v", a)
	}
	if _, err := f.e.AssignmentAction(context.Background(), a.ID, "reopen"); err != nil {
		t.Fatal(err)
	}
	a = f.assignment(t)
	if a.State != store.AssignActive || stepStates(a) != "1:done 2:todo" || a.TrackerPending != 0 || a.Summary != "" {
		t.Fatalf("после «Вернуть в работу»: %s %+v", stepStates(a), a)
	}
	if p, _ := f.st.MCPPendingByID(done); p.State != "rejected" {
		t.Errorf("отметка задачи в трекере не отклонена: %s", p.State)
	}
}

// «Жду» рядом со своими вызовами — ожидание их результата, а не человека:
// поручение не встаёт, результат возвращается модели, шаг закрывается.
func TestStepWaitNextToToolCallsIsNotAQuestion(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник,
		"Смотрю задачи.\n```sema-mcp:planner\n{\"tool\": \"task_list_today\"}\n```\n```sema-step:wait нужен результат поиска\n```",
		"Нашёл всё, что нужно.\n```sema-step:done клиент и задачи сверены\n```")
	f.ask(t, "напиши модуль для дневника")
	f.pass(t)
	a := f.assignment(t)
	if a.State != store.AssignActive || a.Question != "" || stepStates(a) != "1:done 2:todo" {
		t.Fatalf("«жду» рядом с вызовом остановило поручение: %s %+v", stepStates(a), a)
	}
	if p := f.model.Prompts()[1]; !strings.Contains(p, "never write sema-step:wait for them") {
		t.Error("указание хода не говорит, что результаты вызовов вернутся сами")
	}
}

// Ждущее поручение не просыпается от любого сообщения в чате: «завтра
// сделаю» и разговор о другом его не будят, и вопрос не повторяется. Будит
// только ответ, после которого модель сама пишет «продолжить».
func TestWaitingAssignmentNotWokenByAnyMessage(t *testing.T) {
	f := newMCPChatFix(t, true, planДневник,
		"Нужен пароль.\n```sema-step:wait заведите пароль и нажмите проверку\n```",
		"Хорошо, жду.",
		"Тег создан.",
		"Спасибо, продолжаю.\n```sema-step:go\n```",
		"Вошёл.\n```sema-step:done вход работает\n```")
	f.ask(t, "напиши модуль для дневника")
	f.pass(t)
	if a := f.assignment(t); a.State != store.AssignWaiting {
		t.Fatalf("поручение не ждёт: %+v", a)
	}
	f.ask(t, "завтра восстановлю пароль и сделаю")
	f.ask(t, "заведи тег «мобильная версия»")
	before := len(f.model.Prompts())
	f.pass(t)
	if a := f.assignment(t); a.State != store.AssignWaiting || len(f.model.Prompts()) != before {
		t.Fatalf("поручение проснулось от разговора: %s, ходов %d → %d", a.State, before, len(f.model.Prompts()))
	}
	if p := f.model.Prompts()[2]; !strings.Contains(p, "do not ask the question again") {
		t.Error("модель не знает, что вопрос повторять не нужно")
	}
	f.ask(t, "пароль восстановил, проверка прошла")
	if a := f.assignment(t); a.State != store.AssignActive {
		t.Fatalf("после «продолжить» поручение не в работе: %+v", a)
	}
	f.pass(t)
	if a := f.assignment(t); stepStates(a) != "1:done 2:todo" {
		t.Fatalf("после ответа человека шаг не сделан: %s", stepStates(a))
	}
}

// Человек отложил на любой срок: модель пишет точное время, поручение
// молчит до него и в срок само возвращается в работу.
func TestAssignLaterWakesAtTime(t *testing.T) {
	when := time.Now().Add(90 * time.Minute).Format("2006-01-02 15:04")
	f := newMCPChatFix(t, true, planДневник,
		"Нужен пароль.\n```sema-step:wait заведите пароль\n```",
		"Хорошо, вернусь к этому.\n```sema-step:later "+when+" — восстановит пароль\n```",
		"Пароль на месте.\n```sema-step:done вход работает\n```")
	f.ask(t, "напиши модуль для дневника")
	f.pass(t)
	f.ask(t, "через полтора часа восстановлю пароль")
	a := f.assignment(t)
	wake, err := time.Parse(time.RFC3339, a.WakeAt)
	if a.State != store.AssignWaiting || err != nil || wake.Local().Format("2006-01-02 15:04") != when || a.Question != "восстановит пароль" {
		t.Fatalf("отсрочка: %+v", a)
	}
	if p := f.model.Prompts()[2]; !strings.Contains(p, "sema-step:later <YYYY-MM-DD HH:MM>") || !strings.Contains(p, time.Now().Format("2006-01-02")) {
		t.Error("модель не знает, как отложить, или не видит сегодняшней даты")
	}
	before := len(f.model.Prompts())
	f.pass(t)
	if len(f.model.Prompts()) != before {
		t.Fatal("отложенное поручение получило ход до срока")
	}
	a.WakeAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if err := f.st.SaveAssignment(a); err != nil {
		t.Fatal(err)
	}
	f.pass(t)
	if a := f.assignment(t); a.State != store.AssignActive || a.WakeAt != "" || stepStates(a) != "1:done 2:todo" {
		t.Fatalf("в срок поручение не вернулось в работу: %s %+v", stepStates(a), a)
	}
}
