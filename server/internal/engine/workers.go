package engine

import (
	"fmt"
	"sync"
	"time"
)

// Реестр фоновых процессов Сёмы: то же, что задачи по расписанию, только
// встроенные. Виден на экране «Задачи» — со своим расписанием, состоянием
// и выключателем.

const (
	WorkerPoll        = "poll"
	WorkerArchive     = "archive"
	WorkerReceipts    = "receipts" // ушёл в модуль «Чеки»; строка задачи удаляется при старте
	WorkerAttachments = "attachments"
	WorkerBackfill    = "backfill"
	WorkerPurge       = "purge"
	WorkerFolders     = "folders"
)

// mailTasks — встроенные задачи, которые работают с почтой: в UI они живут
// одной группой, их можно отключить, но не удалить.
var mailTasks = map[string]bool{
	WorkerPoll: true, WorkerArchive: true,
	WorkerAttachments: true, WorkerBackfill: true, WorkerPurge: true,
	WorkerFolders: true,
}

// IsMailTask — почтовая ли это встроенная задача.
func IsMailTask(system string) bool { return mailTasks[system] }

type SystemTask struct {
	ID       string
	Title    string
	Desc     string
	Schedule string
	Enabled  bool
	Running  bool
	LastRun  time.Time
	Progress string
}

type workerState struct {
	running bool
	lastRun time.Time
	note    string
}

type workers struct {
	mu    sync.Mutex
	state map[string]*workerState
}

func (w *workers) get(id string) *workerState {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == nil {
		w.state = map[string]*workerState{}
	}
	s := w.state[id]
	if s == nil {
		s = &workerState{}
		w.state[id] = s
	}
	return s
}

func (w *workers) begin(id string) {
	s := w.get(id)
	w.mu.Lock()
	s.running, s.lastRun = true, time.Now()
	w.mu.Unlock()
}

func (w *workers) end(id, note string) {
	s := w.get(id)
	w.mu.Lock()
	s.running = false
	if note != "" {
		s.note = note
	}
	w.mu.Unlock()
}

func (w *workers) snapshot(id string) workerState {
	s := w.get(id)
	w.mu.Lock()
	defer w.mu.Unlock()
	return *s
}

// WorkerEnabled — включён ли фоновый процесс (по умолчанию да).
func (e *Engine) WorkerEnabled(id string) bool {
	return e.store.Setting("worker."+id, "1") == "1"
}

func (e *Engine) SetWorkerEnabled(id string, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting("worker."+id, v)
}

// RunWorkerNow просит процесс отработать прямо сейчас.
func (e *Engine) RunWorkerNow(id string) {
	switch id {
	case WorkerPoll, WorkerPurge:
		e.TriggerPoll()
	default:
		select {
		case e.wakeWorker <- id:
		default:
		}
	}
}

// SystemTasks — состояние всех фоновых процессов для экрана «Задачи».
func (e *Engine) SystemTasks() []SystemTask {
	archDone, archTotal := e.ArchiveProgress()
	attDone, attTotal := e.attachmentProgress()
	bfDone, bfTotal := e.BackfillProgress()
	polling, pollNew := e.PollState()

	list := []SystemTask{
		{
			ID: WorkerPoll, Title: "Проверка почты",
			Desc:     "Забирает новые письма из всех ящиков, раскладывает спам и входящие",
			Schedule: fmt.Sprintf("каждые %d мин", int(e.conf.PollEvery().Minutes())),
			Running:  polling,
			Progress: fmt.Sprintf("разобрано новых писем: %d", pollNew),
		},
		{
			ID: WorkerArchive, Title: "Архив писем",
			Desc:     "Сохраняет текст и HTML каждого письма локально — навсегда",
			Schedule: "каждые 5 сек",
			Progress: fmt.Sprintf("%d из %d сохранено", archTotal-archDone, archTotal),
		},
		{
			ID: WorkerAttachments, Title: "Вложения из писем",
			Desc:     "Скачивает файлы к себе, включая письма из спама",
			Schedule: "каждые 10 сек",
			Progress: fmt.Sprintf("просмотрено %d из %d писем", attDone, attTotal),
		},
		{
			ID: WorkerBackfill, Title: "Глубокий разбор ящика",
			Desc:     "Разбирает всю историю переписки от свежих писем к старым",
			Schedule: "фоном",
			Progress: fmt.Sprintf("%d из %d", bfDone, bfTotal),
		},
		{
			ID: WorkerPulse, Title: "Пульс сервера",
			Desc:     "Снимок процесса раз в минуту: память, CPU, горутины, база, диск — для «Здоровья»",
			Schedule: "каждые 1 мин",
		},
		{
			ID: WorkerFileFolders, Title: "Папки с файлами",
			Desc:     "Раз в минуту забирает новые файлы из наблюдаемых папок — справочник на экране «Хранилища»",
			Schedule: "каждые 1 мин",
		},
		{
			ID: WorkerFilesMirror, Title: "Зеркало файлов",
			Desc:     "Раз в минуту докладывает новые файлы в S3-зеркало; локальная копия остаётся основной",
			Schedule: "каждые 1 мин",
		},
		{
			ID: WorkerBackup, Title: "Копия наружу",
			Desc:     "Ночью снимает копию базы, проверяет её тем же самоосмотром, что и восстановление, и выгружает на Яндекс.Диск. Старые копии убираются, самая свежая не трогается никогда",
			Schedule: "каждый день в 03:30",
		},
		{
			ID: WorkerFilesOffload, Title: "Разгрузка файлов",
			Desc:     "Файлы старше срока, уже лежащие в зеркале, перестают занимать место на этой машине — отдаются из зеркала. Перед удалением местной копии Сёма забирает файл из зеркала и сверяет хэш",
			Schedule: "каждые 10 мин",
		},
		{
			ID: WorkerFilesMigrate, Title: "Переезд файлов",
			Desc:     "Старые вложения получают хэш, ключ в хранилище и источник «письмо»; выключается сам, когда закончит",
			Schedule: "каждые 5 сек",
			Progress: filesMigrateNote(e),
		},
		{
			ID: WorkerModuleEvents, Title: "События модулей",
			Desc:     "Будит модули на новые письма и файлы с текстом",
			Schedule: "каждые 10 сек",
		},
		{
			ID: WorkerMemory, Title: "Сведение памяти",
			Desc:     "Кристаллы сессий, граф, слияние дублей, сроки — ролью «Память»",
			Schedule: "каждый час",
			Progress: memoryPassNote(e),
		},
		{
			ID: WorkerPurge, Title: "Очистка спама",
			Desc:     fmt.Sprintf("Удаляет с почтового сервера письма, пролежавшие в спаме %d дней; копии остаются у Сёмы", e.RetentionDays()),
			Schedule: "при проверке почты",
		},
	}
	for i := range list {
		st := e.workers.snapshot(list[i].ID)
		list[i].Enabled = e.WorkerEnabled(list[i].ID)
		list[i].LastRun = st.lastRun
		if !list[i].Running {
			list[i].Running = st.running
		}
		if st.note != "" {
			list[i].Progress = st.note
		}
	}
	return list
}

func (e *Engine) attachmentProgress() (done, total int) {
	d, t, err := e.store.CountAttScan()
	if err != nil {
		return 0, 0
	}
	return d, t
}

// runPass выполняет один проход процесса с отметками состояния.
func (e *Engine) runPass(id string, pass func() int) int {
	e.workers.begin(id)
	n := pass()
	e.workers.end(id, "")
	return n
}

// TaskBacklog — сколько работы у встроенной задачи осталось (0 — всё сделано).
func (e *Engine) TaskBacklog(system string) int {
	switch system {
	case WorkerArchive:
		missing, _ := e.ArchiveProgress()
		return missing
	case WorkerAttachments:
		done, total := e.attachmentProgress()
		return total - done
	case WorkerBackfill:
		done, total := e.BackfillProgress()
		return int(total - done)
	default:
		return 0 // задача по событию, а не по остатку работы
	}
}

// memoryPassNote — состояние сведения памяти для списка задач.
func memoryPassNote(e *Engine) string {
	if !e.MemoryModelSet() {
		return "роль «Память» без модели: только сроки и векторы"
	}
	if last := e.MemoryLastPass(); last != "" {
		return "последнее сведение " + last[11:16]
	}
	return "ещё не сводил"
}

func filesMigrateNote(e *Engine) string {
	left, total := e.FilesMigrateProgress()
	if left == 0 {
		return fmt.Sprintf("готово · файлов %d", total)
	}
	return fmt.Sprintf("осталось %d из %d", left, total)
}
