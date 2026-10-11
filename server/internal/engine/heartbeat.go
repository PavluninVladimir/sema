package engine

import (
	"context"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/llm"
	"log"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Сердцебиение — корень всей фоновой работы Сёмы. Каждую секунду оно
// проходит по списку задач и решает, чья очередь: по времени, по интервалу
// или «после такой-то задачи». Сама эта задача не редактируется и не
// выключается — как пульс.

const heartbeatEvery = time.Second

// Встроенные задачи заводятся один раз и дальше живут как обычные:
// их можно переименовать, поменять расписание и поставить в очередь.
var builtinTasks = []store.Task{
	{Title: "Сердцебиение", System: "heartbeat", Root: true, Enabled: true, Sort: 0,
		Schedule: "каждую секунду",
		Prompt:   "Пульс Сёмы: будит остальные задачи и решает, чья очередь"},
	{Title: "Проверка почты", System: WorkerPoll, Enabled: true, Sort: 10,
		Schedule: "каждые 5 мин",
		Prompt:   "Забирает новые письма из всех ящиков, раскладывает спам и входящие"},
	{Title: "Архив писем", System: WorkerArchive, Enabled: true, Sort: 20,
		Schedule: "каждые 10 сек",
		Prompt:   "Сохраняет текст и HTML каждого письма локально — навсегда"},
	{Title: "Вложения из писем", System: WorkerAttachments, Enabled: true, Sort: 30,
		Schedule: "каждые 15 сек",
		Prompt:   "Скачивает файлы к себе, включая письма из спама"},
	{Title: "Очередь", System: WorkerQueue, Enabled: true, Sort: 3,
		Schedule: "каждые 30 сек",
		Prompt:   "Фоновая работа по одной: разбор памяти и прочее. Между работами пауза, при высокой нагрузке проход пропускается"},
	{Title: "Сборщик очереди", System: WorkerReconcile, Enabled: true, Sort: 8,
		Schedule: "каждый час",
		Prompt:   "Ищет работу, которой нет в очереди: страховка, чтобы ничего не потерялось"},
	{Title: "Пинать чаты", System: WorkerChatNudge, Enabled: true, Sort: 7,
		Schedule: "каждые 5 мин",
		Prompt:   "Незаконченная работа в чатах команд: Сёма получает внутренний ход и доводит дело до конца или говорит, чего ждёт от человека"},
	{Title: "Поручения", System: WorkerAssign, Enabled: true, Sort: 7,
		Schedule: "каждые 2 мин",
		Prompt:   "Работа до готовности: идущему поручению — ход по текущему шагу, ждущему — проверка, не ответил ли человек"},
	{Title: "Сроки", System: WorkerNotifyDue, Enabled: true, Sort: 6,
		Schedule: "каждый час",
		Prompt:   "Наступившие сроки уведомлений поднимаются в тревогу, письма без ответа сверяются с архивом, старые закрытые убираются"},
	{Title: "Остаток на счетах", System: WorkerBalance, Enabled: true, Sort: 74,
		Schedule: "каждые 30 мин",
		Prompt: "Спрашивает у провайдеров, которые умеют это назвать, сколько осталось на счёте. " +
			"«Здоровье» показывает уже известное вместе со временем вопроса, а не ждёт сети в момент открытия"},
	{Title: "Пульс сервера", System: WorkerPulse, Enabled: true, Sort: 5,
		Schedule: "каждые 1 мин",
		Prompt:   "Снимок процесса: память, CPU, база, диск — для экрана «Здоровье»"},
	{Title: "Папки с файлами", System: WorkerFileFolders, Enabled: true, Sort: 34,
		Schedule: "каждые 1 мин",
		Prompt:   "Забирает новые файлы из наблюдаемых папок (экран «Хранилища»)"},
	// выключена, как и «Копия наружу»: зеркало увозит файлы наружу, и
	// это решение человека. Раньше стояла включённой — и выглядела
	// работающей, хотя зеркала могло не быть вовсе
	{Title: "Зеркало файлов", System: WorkerFilesMirror, Enabled: false, Sort: 36,
		Schedule: "каждые 1 мин",
		Prompt:   "Докладывает копию каждого файла в зеркало из справочника хранилищ (экран «Хранилища»)"},
	// наблюдатель за формой собственной работы: раз в пять минут, только
	// сводные запросы. Дорогая петля не должна прожить больше часа
	{Title: "Наблюдатель", System: WorkerSelfWatch, Enabled: true, Sort: 37,
		Schedule: "каждые 5 мин",
		Prompt:   "Смотрит, не идёт ли работа впустую: повтор без следа, включённое, которое не происходит"},
	// копия наружу: ночью, когда канал свободен и никто не работает
	{Title: "Копия наружу", System: WorkerBackup, Enabled: false, Sort: 38,
		Schedule: "каждый день в 03:30",
		Prompt:   "Снимает копию, проверяет её самоосмотром и выгружает на Яндекс.Диск; старые убирает"},
	// разгрузка идёт следом за зеркалом и по умолчанию выключена: она
	// стирает местные копии, и такое человек включает сам
	{Title: "Разгрузка файлов", System: WorkerFilesOffload, Enabled: false, Sort: 37,
		Schedule: "каждые 10 мин",
		Prompt:   "Файлы старше срока, уже лежащие в зеркале, перестают занимать место на этой машине; отдаются из зеркала"},
	{Title: "Переезд файлов", System: WorkerFilesMigrate, Enabled: true, Sort: 35,
		Schedule: "каждые 5 сек",
		Prompt:   "Считает хэш старых вложений, переносит их в хранилище по хэшу, записывает источник «письмо»; выключается сам, когда закончит"},
	{Title: "Глубокий разбор ящика", System: WorkerBackfill, Enabled: true, Sort: 50,
		Schedule: "каждые 20 сек",
		Prompt:   "Разбирает всю историю переписки от свежих писем к старым"},
	{Title: "Разбор всех папок", System: WorkerFolders, Enabled: true, Sort: 55,
		Schedule: "каждые 20 сек",
		Prompt:   "Обходит все папки ящика, помечает письма прочитанными и складывает их в «Archive» на сервере (корзину, спам и черновики не трогает)"},
	{Title: "Очистка спама", System: WorkerPurge, Enabled: true, Sort: 60,
		Schedule: "каждые 30 мин",
		Prompt:   "Удаляет с почтового сервера письма, пролежавшие в спаме положенный срок"},
	{Title: "События модулей", System: WorkerModuleEvents, Enabled: true, Sort: 65,
		Schedule: "каждые 10 сек",
		Prompt:   "Будит модули на новые письма и файлы с текстом: очередь событий, повтор при ошибке"},
	{Title: "Ночная смена", System: WorkerNightShift, Enabled: false, Sort: 79,
		Schedule: "каждые 5 мин",
		Prompt: "Сервер, отданный Сёме целиком, в своё окно обслуживается им самим: осмотр читающими командами из скилла, " +
			"наряд работ, каждый шаг через суд двух моделей и по одному, с пробой и возвратом. " +
			"Утром одна запись в «Уведомлениях»; проба не сошлась — тревога сразу и остановка сервера"},
	{Title: "Цены у провайдеров", System: WorkerPrices, Enabled: false, Sort: 76,
		Schedule: "каждый день в 07:00",
		Prompt: "Берёт цены у каждого провайдера с подключёнными моделями по его источнику (страница цен или JSON) и, если что-то " +
			"новое или изменилось, кладёт в «Уведомления» одну карточку на провайдера. " +
			"Сама ничего не меняет: неверно разобранная цена хуже отсутствующей, поэтому принимает человек. " +
			"Кнопка «Проверить цены» и проверка при подключении провайдера работают и без этой задачи"},
	{Title: "Проверка новой версии", System: WorkerUpdate, Enabled: false, Sort: 78,
		Schedule: updateSchedule(),
		Prompt: "Раз в сутки спрашивает у заданного адреса, нет ли новой версии Сёмы; наружу уходит только номер нынешней сборки. " +
			"Новая версия приходит карточкой в «Уведомления», сама ничего не обновляет. Адрес — на «Здоровье»"},
	{Title: "Ждут ответа в Telegram", System: WorkerTGWait, Enabled: true, Sort: 77,
		Schedule: "каждые 5 мин",
		Prompt:   "Смотрит, где собеседник написал, а ответа нет: поднимает уведомление и повышает уровень, пока молчание тянется. Сама за человека не отвечает"},
	{Title: "Сведение памяти", System: WorkerMemory, Enabled: true, Sort: 70,
		Schedule: "каждый час",
		Prompt:   "Закрывает замолчавшие сессии, делает кристаллы, извлекает граф, сливает дубли и удаляет истёкшее — ролью «Память»"},
}

func (e *Engine) ensureBuiltinTasks() {
	// воркеры, ушедшие в модули: их строки задач больше не нужны
	if tasks, err := e.store.Tasks(); err == nil {
		for _, t := range tasks {
			if t.System == WorkerReceipts {
				_ = e.store.DeleteTask(t.ID)
			}
		}
	}
	for i := range builtinTasks {
		t := builtinTasks[i]
		if err := e.store.EnsureSystemTask(&t); err != nil {
			log.Printf("встроенная задача %q: %v", t.Title, err)
		}
		if t.Root {
			// у корня текст всегда соответствует реальному такту
			_ = e.store.SyncRootTask(t.Title, t.Schedule, t.Prompt)
		}
	}
}

// heartbeat — единственный планировщик: будит задачи, чьё время пришло.
func (e *Engine) heartbeat(ctx context.Context) {
	e.ensureBuiltinTasks()
	tick := time.NewTicker(heartbeatEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.beats.Add(1)
			e.lastBeat.Store(time.Now().UnixMilli())
			e.dispatch(ctx)
		}
	}
}

func (e *Engine) dispatch(ctx context.Context) {
	tasks, err := e.store.Tasks()
	if err != nil {
		return
	}
	byID := map[int64]*store.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	now := time.Now()
	for _, t := range tasks {
		if t.Root || !t.Enabled || e.taskRunning(t.ID) {
			continue
		}
		if !e.taskDue(t, byID, now) {
			continue
		}
		go e.runTask(ctx, t)
	}
}

// taskDue — пора ли запускать: «после другой задачи» или по расписанию.
func (e *Engine) taskDue(t *store.Task, byID map[int64]*store.Task, now time.Time) bool {
	if t.AfterID != 0 {
		dep := byID[t.AfterID]
		if dep == nil || dep.LastRun.IsZero() {
			return false
		}
		return dep.LastRun.After(t.LastRun) // предыдущая отработала после нас
	}
	due, err := nextRun(t.Schedule, t.LastRun, now)
	return err == nil && due
}

func (e *Engine) taskRunning(id int64) bool {
	e.runningMu.Lock()
	defer e.runningMu.Unlock()
	return e.running[id]
}

func (e *Engine) setTaskRunning(id int64, on bool) {
	e.runningMu.Lock()
	defer e.runningMu.Unlock()
	if e.running == nil {
		e.running = map[int64]bool{}
	}
	if on {
		e.running[id] = true
	} else {
		delete(e.running, id)
	}
}

// runTask выполняет задачу — встроенную или пользовательскую.
func (e *Engine) runTask(ctx context.Context, t *store.Task) {
	if e.taskRunning(t.ID) {
		return
	}
	if e.heldByBinding(t) {
		_ = e.store.TaskRan(t.ID, "ждёт привязки: Сёма поднят из копии, подтвердите привязку на «Здоровье»")
		return
	}
	e.setTaskRunning(t.ID, true)
	defer e.setTaskRunning(t.ID, false)
	// расход задачи — на её имя: воркер ядра, модуль, скрипт или своя задача
	consumer := TaskConsumer(t)
	ctx = llm.WithConsumer(ctx, consumer)
	started := time.Now()
	defer func() {
		if t.System == WorkerModuleEvents || t.System == WorkerPulse || t.System == "heartbeat" {
			return // сами пишут своё или слишком часты, чтобы засорять журнал
		}
		if t.System == "" || strings.HasPrefix(t.System, "module:") {
			return // скрипты и модули записаны там, где исполняются
		}
		if e.lastResultOK(t.ID) {
			e.recordRun(consumer, time.Since(started).Seconds(), true, "")
		} else {
			e.recordRun(consumer, time.Since(started).Seconds(), false, e.lastResultText(t.ID))
		}
	}()

	// ненастроенная система — не поломка: задача ждёт ящика и модели
	if t.System != "" && t.System != "heartbeat" {
		if mail, model := e.Configured(); !mail || !model {
			what := "ящика"
			if mail {
				what = "модели"
			}
			_ = e.store.TaskRan(t.ID, "ждёт настройки: не хватает "+what)
			return
		}
	}

	// таймер чата: будим чат заметкой, Сёма продолжает сам
	if strings.HasPrefix(t.System, chatTimerPrefix) {
		_ = e.store.TaskRan(t.ID, e.fireChatTimer(t))
		return
	}

	// задача модуля: выполняем его код с его правами
	if mod, run := ModuleOfTask(t.System); mod != "" {
		call := ""
		if run != "" {
			call = run + "()"
		}
		res, err := e.RunModuleFor(ctx, mod, call, "задача «"+t.Title+"»")
		// запуск задачи модуля — в журнал расхода на имя модуля, как и события
		switch {
		case err != nil:
			_ = e.store.TaskRan(t.ID, "ошибка: "+err.Error())
			e.recordRun(consumer, time.Since(started).Seconds(), false, t.Title+": "+err.Error())
		case res.Err != nil:
			_ = e.store.TaskRan(t.ID, "ошибка модуля: "+res.ErrText)
			e.recordRun(consumer, time.Since(started).Seconds(), false, t.Title+": "+res.ErrText)
		default:
			e.recordRun(consumer, time.Since(started).Seconds(), true, t.Title)
			out := res.Value
			if out == "" {
				out = strings.Join(res.Log, " · ")
			}
			if out == "" {
				out = "выполнено"
			}
			_ = e.store.TaskRan(t.ID, out)
		}
		return
	}

	result := ""
	switch t.System {
	case WorkerPoll:
		e.poll(ctx)
		_, n := e.PollState()
		result = fmt.Sprintf("новых писем: %d", n)
	case WorkerArchive:
		n := e.archivePass(ctx)
		missing, total := e.ArchiveProgress()
		result = fmt.Sprintf("сохранено %d · в архиве %d из %d", n, total-missing, total)
	case WorkerAttachments:
		n := e.scanAllAccounts(ctx)
		done, total := e.attachmentProgress()
		result = fmt.Sprintf("разобрано %d · просмотрено %d из %d", n, done, total)
		// старые файлы, из которых текст не достали: инструмент мог появиться
		if again, err := e.RescanAttachments(ctx, 10); err == nil && again > 0 {
			result += fmt.Sprintf(" · перечитано %d", again)
		}
	case WorkerBackfill:
		n, err := e.backfillBatch(ctx)
		done, total := e.BackfillProgress()
		result = fmt.Sprintf("разобрано %d · %d из %d", n, done, total)
		if err != nil {
			result = "ошибка: " + err.Error()
		}
	case WorkerFolders:
		n, box, err := e.foldersBatch(ctx)
		done, left := e.FoldersProgress()
		result = fmt.Sprintf("уложено в архив %d · всего %d · осталось %d", n, done, left)
		if box != "" {
			result = fmt.Sprintf("папка «%s»: %d · всего %d · осталось %d", box, n, done, left)
		}
		if err != nil {
			result = "ошибка: " + err.Error()
		}
	case WorkerPurge:
		n := e.purgeAll(ctx)
		result = fmt.Sprintf("удалено из почты: %d", n)
	case WorkerMemory:
		result = e.memoryPass(ctx)
	case WorkerTGWait:
		if err := e.tgWaitPass(); err != nil {
			result = "не сошлось: " + err.Error()
		} else {
			result = "проверено"
		}
	case WorkerPrices:
		result = e.pricesPass(ctx)
	case WorkerUpdate:
		result = e.updatePass(ctx)
	case WorkerNightShift:
		result = e.nightShiftPass(ctx)
	case WorkerModuleEvents:
		result = e.moduleEventsPass(ctx)
	case WorkerPulse:
		result = e.pulsePass(ctx)
	case WorkerBalance:
		result = e.RefreshBalances(ctx)
	case WorkerNotifyDue:
		result = e.notifyDuePass(ctx)
	case WorkerChatNudge:
		result = e.nudgePass()
	case WorkerAssign:
		result = e.assignPass(ctx)
	case WorkerQueue:
		result = e.queuePass(ctx)
	case WorkerReconcile:
		result = e.reconcilePass()
	case WorkerFileFolders:
		result = e.foldersPass(ctx)
	case WorkerFilesMirror:
		result = e.mirrorPass(ctx)
	case WorkerFilesOffload:
		result = e.offloadPass(ctx)
	case WorkerSelfWatch:
		result = e.selfWatchPass(ctx)
	case WorkerBackup:
		result = e.backupPass(ctx)
	case WorkerFilesMigrate:
		result = e.filesMigratePass(ctx)
		if left, _ := e.FilesMigrateProgress(); left == 0 {
			_ = e.store.SetTaskEnabled(t.ID, false) // переезд закончен — задача выключается сама
		}
	default:
		if t.ScriptID != 0 {
			res, err := e.RunScriptByID(ctx, t.ScriptID)
			switch {
			case err != nil:
				result = "ошибка: " + err.Error()
			case res.Err != nil:
				result = "ошибка: " + res.Err.Error()
			default:
				result = res.Value
				if result == "" {
					result = strings.Join(res.Log, " · ")
				}
			}
			break
		}
		e.runUserTask(ctx, t)
		return
	}
	_ = e.store.TaskRan(t.ID, result)
}

// runUserTask — задача, которую завёл человек: спрашиваем модель в её чате.
func (e *Engine) runUserTask(ctx context.Context, t *store.Task) {
	log.Printf("task run: %q", t.Title)
	result := ""
	chatID, err := e.TaskChat(t)
	if err != nil {
		result = "ошибка: " + err.Error()
	} else {
		prompt := t.Prompt
		if prompt == "" {
			prompt = t.Title
		}
		if err := e.ChatReply(ctx, chatID, prompt); err != nil {
			result = "ошибка: " + err.Error()
		} else if msgs, err := e.store.ChatMsgs(chatID, 1); err == nil && len(msgs) > 0 {
			result = msgs[0].Content
		}
	}
	_ = e.store.TaskRan(t.ID, result)
	if t.Once {
		_ = e.store.SetTaskEnabled(t.ID, false)
	}
}

// RunTaskByID — «Запустить сейчас» из интерфейса.
func (e *Engine) RunTaskByID(ctx context.Context, id int64) {
	t, err := e.store.Task(id)
	if err != nil || t.Root {
		return
	}
	go e.runTask(ctx, t)
}

// Beats — сколько ударов сделало сердце и сколько прошло с последнего.
func (e *Engine) Beats() (int64, time.Duration) {
	ms := e.lastBeat.Load()
	if ms == 0 {
		return e.beats.Load(), 0
	}
	return e.beats.Load(), time.Since(time.UnixMilli(ms))
}

// TaskRunning — выполняется ли задача прямо сейчас (для UI).
func (e *Engine) TaskRunning(id int64) bool { return e.taskRunning(id) }

// ParseAfter разбирает «после Архив писем» → id задачи.
func (e *Engine) ParseAfter(text string) (int64, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, "", nil
	}
	low := strings.ToLower(text)
	if !strings.HasPrefix(low, "после") {
		return 0, text, nil // обычное расписание
	}
	name := strings.TrimSpace(strings.TrimPrefix(low, "после"))
	name = strings.Trim(name, " «»\"':")
	tasks, err := e.store.Tasks()
	if err != nil {
		return 0, "", err
	}
	for _, t := range tasks {
		if strings.Contains(strings.ToLower(t.Title), name) && !t.Root {
			return t.ID, "после «" + t.Title + "»", nil
		}
	}
	return 0, "", fmt.Errorf("не нашёл задачу «%s»", name)
}

// RescheduleTask меняет расписание задачи, понимая «после «Другая задача»».
func (e *Engine) RescheduleTask(id int64, when string) error {
	when = strings.TrimSpace(when)
	if when == "" {
		return nil
	}
	afterID, schedule, err := e.ParseAfter(when)
	if err != nil {
		return err
	}
	if afterID == id {
		return fmt.Errorf("задача не может ждать саму себя")
	}
	if afterID == 0 {
		if _, err := nextRun(schedule, time.Time{}, time.Now()); err != nil {
			return fmt.Errorf("не понял расписание %q — примеры: «каждые 30 сек», «каждые 5 мин», «9:00», «после «Архив писем»»", when)
		}
	}
	return e.store.SetTaskSchedule(id, schedule, afterID)
}

// ResetSystemTasks возвращает встроенные задачи с обычным расписанием:
// удалённые создаются заново, изменённым возвращается расписание.
func (e *Engine) ResetSystemTasks() error {
	existing, err := e.store.Tasks()
	if err != nil {
		return err
	}
	bySystem := map[string]*store.Task{}
	for _, t := range existing {
		if t.System != "" {
			bySystem[t.System] = t
		}
	}
	for i := range builtinTasks {
		def := builtinTasks[i]
		cur, ok := bySystem[def.System]
		if !ok {
			if err := e.store.CreateTask(&def); err != nil {
				return err
			}
			continue
		}
		if cur.Root {
			continue
		}
		if err := e.store.SetTaskTitle(cur.ID, def.Title); err != nil {
			return err
		}
		if err := e.store.SetTaskPrompt(cur.ID, def.Prompt); err != nil {
			return err
		}
		if err := e.store.SetTaskSchedule(cur.ID, def.Schedule, 0); err != nil {
			return err
		}
		if err := e.store.SetTaskEnabled(cur.ID, true); err != nil {
			return err
		}
	}
	return nil
}

// CreateScriptTask заводит задачу, которая запускает скрипт по расписанию.
func (e *Engine) CreateScriptTask(scriptID int64, title, when string) error {
	afterID, schedule, err := e.ParseAfter(when)
	if err != nil {
		return err
	}
	if afterID == 0 {
		if _, err := nextRun(schedule, time.Time{}, time.Now()); err != nil {
			return fmt.Errorf("не понял расписание %q", when)
		}
	}
	return e.store.CreateTask(&store.Task{
		Title:    title,
		Prompt:   "Скрипт «" + title + "»",
		Kind:     "script",
		Schedule: schedule,
		AfterID:  afterID,
		ScriptID: scriptID,
		Enabled:  true,
		Sort:     100,
	})
}

// lastResultOK/lastResultText — результат задачи после выполнения: строка
// с «ошибка» — отказ. Читается из базы, потому что задача пишет её сама.
func (e *Engine) lastResultOK(id int64) bool {
	return !strings.HasPrefix(e.lastResultText(id), "ошибка")
}

func (e *Engine) lastResultText(id int64) string {
	t, err := e.store.Task(id)
	if err != nil || t == nil {
		return ""
	}
	return t.LastResult
}
