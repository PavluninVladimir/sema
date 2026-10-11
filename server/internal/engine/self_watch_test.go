package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// retry — то, что делает очередь: взять работу, записать след, закрыть.
func retry(t *testing.T, st *store.Store, kind, ref, trace string, once int) {
	t.Helper()
	for i := 0; i < once; i++ {
		if err := st.Enqueue(kind, ref, "", 5); err != nil {
			t.Fatal(err)
		}
		batch, err := st.TakeWork(1, time.Minute)
		if err != nil || len(batch) == 0 {
			t.Fatalf("работа не взялась: %v", err)
		}
		if err := st.RememberTrace(batch[0].ID, trace); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishWork(batch[0].ID); err != nil {
			t.Fatal(err)
		}
	}
}

// Дешёвая петля — тревога, работа идёт. Останавливать посреди ночи то, что
// не стоит денег, значит рисковать настоящей работой ради экономии
// процессорного времени.
func TestCheapLoopOnlyAlert(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := e.SetWatchStops(true); err != nil {
		t.Fatal(err)
	}
	retry(t, st, WorkFileParse, "41", "один-и-тот-же", 6)

	if found := e.checkRetries(); found == 0 {
		t.Fatal("петля не найдена")
	}
	findings, err := st.Findings(true, 10)
	if err != nil {
		t.Fatal(err)
	}
	var n *store.Finding
	for _, f := range findings {
		if strings.Contains(f.Key, "41") {
			n = f
		}
	}
	if n == nil {
		t.Fatal("находки про объект нет")
	}
	if n.Measure != store.MeasureAlert {
		t.Errorf("дешёвая петля остановлена (%s), а должна была только сказать", n.Measure)
	}
	// и работа по объекту не задержана
	if held := howManyDelayed(t, st, WorkFileParse, "41"); held > 0 {
		t.Errorf("дешёвая петля остановила работу: задержано %d", held)
	}
}

// Дорогая петля останавливается сразу — и только по своему объекту.
// Остальные объекты того же вида обязаны работать: один зациклившийся файл
// не запирает разбор остальных.
func TestExpensiveLoopStopsOnlyObject(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := e.SetWatchStops(true); err != nil {
		t.Fatal(err)
	}
	retry(t, st, WorkFileParse, "413", "не-меняется", 6)
	// петля стоила денег
	saveUsage(t, st, "core:queue", 6.5)
	// а рядом обычная работа того же вида ждёт своей очереди
	if err := st.Enqueue(WorkFileParse, "500", "", 5); err != nil {
		t.Fatal(err)
	}

	if found := e.checkRetries(); found == 0 {
		t.Fatal("дорогая петля не найдена")
	}
	if held := howManyDelayed(t, st, WorkFileParse, "413"); held == 0 {
		t.Error("дорогая петля не остановлена")
	}
	if held := howManyDelayed(t, st, WorkFileParse, "500"); held > 0 {
		t.Error("вместе с петлёй остановлен соседний объект — так зеркало уже ломалось")
	}
}

// Пока человек не разрешил останавливать, наблюдатель только говорит.
func TestWithoutPermissionNotStops(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if e.WatchStops() {
		t.Fatal("останавливать разрешено по умолчанию — право должно даваться осознанно")
	}
	retry(t, st, WorkFileParse, "77", "не-меняется", 6)
	saveUsage(t, st, "core:queue", 3)
	e.checkRetries()
	if held := howManyDelayed(t, st, WorkFileParse, "77"); held > 0 {
		t.Error("наблюдатель остановил работу без разрешения")
	}
}

// Включённая задача с расписанием, которого планировщик не понимает, — то,
// из-за чего «Копия наружу» не снялась ни разу за всё своё существование.
func TestTaskWithUnclearScheduleFound(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.CreateTask(&store.Task{Title: "Копия наружу",
		System: "backup-проба", Schedule: "по вторникам как получится", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if found := e.checkDeclared(); found == 0 {
		t.Fatal("задача с непонятным расписанием не найдена")
	}
	findings, _ := st.Findings(true, 20)
	found := false
	for _, n := range findings {
		if strings.Contains(n.Summary, "Копия наружу") && strings.Contains(n.Summary, "не запустится никогда") {
			found = true
			if n.Level != store.FindingAlert {
				t.Errorf("уровень находки %q — задача не работает вовсе", n.Level)
			}
		}
	}
	if !found {
		t.Error("находка не называет задачу и не говорит, что она не запустится")
	}
}

// Одна и та же беда не должна плодить записи: 120 одинаковых обрывов MAX за
// тринадцать часов никто не заметил именно потому, что они были одинаковы.
func TestRepeatFindingNotBreedsRecord(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	retry(t, st, WorkFileParse, "9", "не-меняется", 6)
	e.checkRetries()
	e.checkRetries()
	e.checkRetries()
	findings, _ := st.Findings(true, 50)
	n := 0
	for _, f := range findings {
		if strings.Contains(f.Key, ":9") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("одна беда дала %d записей", n)
	}
}

func howManyDelayed(t *testing.T, st *store.Store, kind, ref string) int {
	t.Helper()
	hold := 0
	list, err := st.WorkList(kind, "held", 50)
	if err != nil {
		return 0
	}
	for _, w := range list {
		if w.Ref == ref {
			hold++
		}
	}
	return hold
}

func saveUsage(t *testing.T, st *store.Store, consumer string, price float64) {
	t.Helper()
	if err := st.AddUsage(&store.UsageRow{At: time.Now(), Consumer: consumer,
		Kind: "llm", Model: "тест", TokensIn: 100, TokensOut: 100, OK: true, Cost: price}); err != nil {
		t.Fatal(err)
	}
}

// Задержанный объект не должен возвращаться в работу сам: тот, кто ставит
// его в очередь, о наблюдателе не знает и поставит снова через минуту.
func TestDelayedObjectNotEntersInQueueAgain(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := e.SetWatchStops(true); err != nil {
		t.Fatal(err)
	}
	retry(t, st, WorkFileParse, "413", "не-меняется", 6)
	saveUsage(t, st, "core:queue", 6.5)
	e.checkRetries()

	// тот, кто ставит работу, пробует снова — и не должен разбудить объект
	if err := st.Enqueue(WorkFileParse, "413", "", 5); err != nil {
		t.Fatal(err)
	}
	batch, err := st.TakeWork(10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range batch {
		if w.Kind == WorkFileParse && w.Ref == "413" {
			t.Fatal("задержанный объект снова взят в работу — пауза ничего не держит")
		}
	}
}

// Наблюдатель не должен находить самого себя. На первом проходе он нашёл:
// `last_run` пишется после прохода, и, сидя внутри собственного запуска, он
// объявил, что ни разу не запускался. Правило распространяется и на него —
// это оказалось не предположением, а находкой.
func TestWatcherNotFindsSelf(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.CreateTask(&store.Task{Title: "Наблюдатель",
		System: WorkerSelfWatch, Schedule: "каждые 5 мин", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	task, err := st.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	var own *store.Task
	for _, x := range task {
		if x.System == WorkerSelfWatch {
			own = x
		}
	}
	if own == nil {
		t.Fatal("задачи наблюдателя нет")
	}
	// он идёт прямо сейчас — ровно так и было на живом сервере
	e.setTaskRunning(own.ID, true)
	defer e.setTaskRunning(own.ID, false)

	e.checkDeclared()
	findings, _ := st.Findings(true, 20)
	for _, n := range findings {
		if strings.Contains(n.Summary, "Наблюдатель") {
			t.Errorf("наблюдатель нашёл самого себя: %s", n.Summary)
		}
	}
}

// Устаревшая находка должна закрываться сама, когда беда прошла. Первая
// редакция «не находить себя» пропускала задачу целиком — и наблюдатель
// перестал закрывать собственную же запись: она висела открытой, пока он
// бодро отчитывался «найдено 0».
func TestFindingClosesWhenTroublePassed(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.CreateTask(&store.Task{Title: "Проба",
		System: "проба-задача", Schedule: "каждые 5 мин", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if found := e.checkDeclared(); found == 0 {
		t.Fatal("задача, ни разу не запускавшаяся, не найдена")
	}
	// задача отработала
	task, _ := st.Tasks()
	for _, x := range task {
		if x.System == "проба-задача" {
			if err := st.TaskRan(x.ID, "готово"); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.checkDeclared()
	open, _ := st.Findings(true, 20)
	for _, n := range open {
		if strings.Contains(n.Summary, "Проба") {
			t.Error("находка осталась открытой, хотя задача пошла")
		}
	}
}

// Уровни в норме при открытой находке зелёного состояния не дают.
//
// Это и есть весь смысл наблюдателя: все восемь молчаливых поломок прошли
// мимо «Здоровья» именно потому, что каждый отдельный уровень был в норме.
// Находка обязана красить экран сама.
func TestFindingPaintsHealth(t *testing.T) {
	e, st := newTestEngine(t, "", nil)

	_, level := e.HealthCards()
	if level != "ok" {
		t.Fatalf("на пустой базе состояние должно быть зелёным, а оно %q", level)
	}

	if _, err := st.SaveFinding(store.Finding{
		Key: "проба:петля", Kind: "повтор без следа", Ref: "file:7",
		Level: store.FindingAlert, Summary: "работа повторяется впустую",
		Measure: store.MeasureAlert, Screen: "queue",
	}); err != nil {
		t.Fatal(err)
	}
	// запись мимо движка: наблюдатель сбрасывает память карточек сам, а
	// прямая запись в хранилище — нет
	e.ResetHealth()
	cards, level := e.HealthCards()
	var has bool
	for _, c := range cards {
		if c.Key == "watch" {
			has = true
		}
	}
	if !has {
		t.Fatal("на «Здоровье» нет карточки находки")
	}
	if level == "ok" {
		t.Fatal("открытая находка оставила состояние зелёным")
	}

	if err := st.CloseFinding("проба:петля", "проверено"); err != nil {
		t.Fatal(err)
	}
	e.ResetHealth()
	if _, level := e.HealthCards(); level != "ok" {
		t.Fatalf("после закрытия находки состояние должно вернуться: %q", level)
	}
}

// На нормальной работе наблюдатель молчит.
//
// Это не приятная мелочь, а условие его существования. Сторож, который
// каждый проход находит «беду», перестаёт читаться через неделю — ровно так
// прошли мимо сто двадцать одинаковых строк MAX за ночь. Поэтому проход по
// здоровой Сёме обязан заканчиваться нулём: почта опрашивается, пульс идёт,
// сердцебиение тикает — находок нет.
func TestOnNormalWorkSilent(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	for _, z := range []*store.Task{
		{Title: "Почта", System: WorkerPoll, Schedule: "каждые 5 мин", Enabled: true},
		{Title: "Пульс сервера", System: WorkerPulse, Schedule: "каждые 1 мин", Enabled: true},
		{Title: "Наблюдатель", System: WorkerSelfWatch, Schedule: "каждые 5 мин", Enabled: true},
	} {
		if err := st.CreateTask(z); err != nil {
			t.Fatal(err)
		}
	}
	// задачи уже работали: иначе «ни разу не сработало» — честная находка
	task, err := st.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, z := range task {
		if z.Enabled {
			_ = st.TaskRan(z.ID, "проход")
		}
	}

	result := e.selfWatchPass(context.Background())
	findings, _ := st.Findings(true, 20)
	for _, n := range findings {
		t.Logf("лишняя находка: [%s] %s · %s", n.Kind, n.Summary, n.Numbers)
	}
	if !strings.Contains(result, "найдено 0") || len(findings) != 0 {
		t.Fatalf("на здоровой Сёме наблюдатель что-то нашёл: %s", result)
	}
}

// Находка о молчащей задаче закрывается, когда задачи больше нет: модуль
// убрал «Пробный пост» с расписанием «вручную», а тревога о нём висела на
// «Здоровье» до вечера (17 сентября 2026).
func TestFindingAboutRemovedTaskCloses(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	task := &store.Task{Title: "Пробный пост", System: "module:проба:preview", Schedule: "вручную", Enabled: true}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if found := e.checkDeclared(); found == 0 {
		t.Fatal("задача с расписанием «вручную» не найдена")
	}
	tasks, _ := st.Tasks()
	for _, z := range tasks {
		if z.System == "module:проба:preview" {
			if err := st.DeleteTask(z.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.checkDeclared()
	if open, _ := st.OpenFindings(); open != 0 {
		t.Fatalf("находка об убранной задаче осталась открытой: %d", open)
	}
}
