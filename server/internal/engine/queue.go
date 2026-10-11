package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Очередь фоновой работы. Один воркер берёт работы по одной, между ними
// дышит, и не начинает проход, если машина занята: на Raspberry Pi это
// разница между «работает» и «сто процентов процессора». Повторы с растущей
// паузой, застрявшее видно человеку, ничего не теряется.

const (
	WorkerQueue     = "queue"
	WorkerReconcile = "queue_reconcile"

	// виды работы
	WorkMemoryReview = "memory_review"

	queueBatchKey      = "queue.batch"          // сколько работ за проход
	queuePauseKey      = "queue.pause_ms"       // пауза между работами
	queueTriesKey      = "queue.max_tries"      // попыток до «застряло»
	queueLoadKey       = "queue.max_load"       // средняя нагрузка, выше которой пропускаем проход
	queueKeepKey       = "queue.keep_days"      // сколько хранить сделанное
	queuePauseLocalKey = "queue.pause_local_ms" // пауза, когда работу делает локальная модель
	queueCloudKey      = "queue.cloud_help"     // 1 — звать облачную запасную, когда локальная не тянет
	queueCloudAfterKey = "queue.cloud_after"    // с какого числа ждущих работ звать облачную
	queueLeaseMins     = 15                     // аренда работы
)

// errPostpone — работа не виновата: модель на передышке, бюджет исчерпан.
type errPostpone struct {
	until time.Time
	why   string
}

func (e *errPostpone) Error() string { return e.why }

func postpone(d time.Duration, why string) error {
	return &errPostpone{until: time.Now().Add(d), why: why}
}

type workHandler func(ctx context.Context, w *store.WorkItem) error

func (e *Engine) queueHandlers() map[string]workHandler {
	return map[string]workHandler{
		WorkMemoryReview: e.handleMemoryReview,
		WorkFileParse:    e.handleFileParse,
		WorkSkillLearn:   e.handleSkillLearn,
		WorkSessionFold:  e.handleSessionFold,
		WorkTranslate:    e.handleTranslate,
		WorkMsgJudge:     e.handleMsgJudge,
	}
}

// QueueKinds — понятные имена видов работы для экрана.
var QueueKinds = map[string]string{
	WorkMemoryReview: "Разбор памяти",
	WorkFileParse:    "Разбор файлов",
	WorkSkillLearn:   "Учусь на работе",
	WorkSessionFold:  "Свёртка сводки сессии",
	WorkTranslate:    "Перевод писем",
	WorkMsgJudge:     "Разбор писем задним числом",
}

func (e *Engine) queueInt(key, def string) int {
	v, _ := strconv.Atoi(e.store.Setting(key, def))
	if v <= 0 {
		v, _ = strconv.Atoi(def)
	}
	return v
}

func (e *Engine) QueueBatch() int    { return e.queueInt(queueBatchKey, "5") }
func (e *Engine) QueuePauseMs() int  { return e.queueInt(queuePauseKey, "500") }
func (e *Engine) QueueMaxTries() int { return e.queueInt(queueTriesKey, "5") }
func (e *Engine) QueueKeepDays() int { return e.queueInt(queueKeepKey, "7") }

// CloudHelpOn — разрешена ли помощь облачной запасной, когда локальная не
// успевает разбирать очередь.
func (e *Engine) CloudHelpOn() bool { return e.store.Setting(queueCloudKey, "1") == "1" }

func (e *Engine) SetCloudHelp(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(queueCloudKey, v)
}

// CloudHelpAfter — сколько работ должно накопиться, чтобы позвать облачную.
func (e *Engine) CloudHelpAfter() int { return e.queueInt(queueCloudAfterKey, "30") }

// needsCloudHelp — локальная не справляется: очередь этого вида длиннее
// порога или её модель ушла на передышку от отказов.
func (e *Engine) needsCloudHelp(kind string) (bool, string) {
	if !e.CloudHelpOn() {
		return false, ""
	}
	role, ok := queueRoles[kind]
	if !ok {
		return false, ""
	}
	if !e.ModelOnThisMachine(e.Role(role).Model) {
		return false, "" // основная и так не на этой машине: ей не тесно
	}
	if _, resting := e.roleCooldown(role); resting {
		return true, "локальная модель на передышке"
	}
	stats, err := e.store.WorkStats()
	if err != nil {
		return false, ""
	}
	for _, st := range stats {
		if st.Kind != kind {
			continue
		}
		if n := st.Waiting + st.Paused; n >= e.CloudHelpAfter() {
			return true, fmt.Sprintf("в очереди %d работ — локальная не успевает", n)
		}
	}
	return false, ""
}

// QueuePauseLocalMs — пауза между работами, когда думает локальная модель:
// она занимает процессор целиком, и на Raspberry Pi это заметно.
func (e *Engine) QueuePauseLocalMs() int { return e.queueInt(queuePauseLocalKey, "3000") }

// pauseFor — сколько дышать после работы этого вида.
func (e *Engine) pauseFor(kind string) time.Duration {
	ms := e.QueuePauseMs()
	if role, ok := queueRoles[kind]; ok && e.ModelOnThisMachine(e.Role(role).Model) {
		ms = e.QueuePauseLocalMs()
	}
	// свой предел для вида работы важнее общего: разбор памяти и учёба на
	// работах стоят по-разному
	if v, err := strconv.Atoi(e.store.Setting(queuePauseKey+"."+kind, "")); err == nil && v > 0 {
		ms = v
	}
	return time.Duration(ms) * time.Millisecond
}

// SetQueuePauseFor — предел скорости для одного вида работы; 0 — убрать.
func (e *Engine) SetQueuePauseFor(kind string, ms int) error {
	if ms <= 0 {
		return e.store.SetSetting(queuePauseKey+"."+kind, "")
	}
	return e.store.SetSetting(queuePauseKey+"."+kind, strconv.Itoa(ms))
}

// QueuePauseFor — какой предел стоит у вида работы (0 — общий).
func (e *Engine) QueuePauseFor(kind string) int {
	v, _ := strconv.Atoi(e.store.Setting(queuePauseKey+"."+kind, ""))
	return v
}

// queueRoles — чья модель делает работу: по ней видно, локальная она или нет.
var queueRoles = map[string]string{
	WorkMemoryReview: RoleMemory,
	WorkFileParse:    RoleModules,
	WorkSkillLearn:   RoleChat,
	WorkSessionFold:  RoleMemory,
	WorkTranslate:    RoleTranslate,
	WorkMsgJudge:     RoleFilter,
}

// QueueMaxLoad — предел средней нагрузки на ядро; 0 — не смотреть.
func (e *Engine) QueueMaxLoad() float64 {
	raw := e.store.Setting(queueLoadKey, "1.5")
	v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", "."), 64)
	if err != nil || v < 0 {
		return 1.5
	}
	return v
}

func (e *Engine) QueuePaused() bool { return e.store.Setting("queue.paused", "") == "1" }

func (e *Engine) SetQueuePaused(on bool) error {
	v := ""
	if on {
		v = "1"
	}
	return e.store.SetSetting("queue.paused", v)
}

// MemoryFree — сколько мегабайт машина может выдать прямо сейчас
// (MemAvailable). На маке файла нет, и проверка пропускается.
//
// Нагрузка на ядро — не единственная мера тесноты. На Pi с восемью
// гигабайтами локальная модель держит четыре с половиной, эмбеддер ещё
// гигабайт, и подкачка кончается раньше, чем нагрузка дорастает до
// предела: в таком состоянии очередь продолжала подкладывать работу, а
// машина уже не считала, а перекладывала страницы.
func MemoryFree() (int, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, false
		}
		return kb / 1024, true
	}
	return 0, false
}

// QueueMinFreeMB — сколько мегабайт должно оставаться свободными, чтобы
// очередь взялась за проход. 0 — не смотреть.
func (e *Engine) QueueMinFreeMB() int {
	v, err := strconv.Atoi(e.store.Setting("queue.min_free_mb", "400"))
	if err != nil || v < 0 {
		return 400
	}
	return v
}

// LoadPerCPU — средняя нагрузка за минуту на ядро; на macOS файла нет и
// проверка пропускается.
func LoadPerCPU() (float64, bool) { return loadPerCPU() }

func loadPerCPU() (float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	cpus := runtimeCPUs()
	if cpus < 1 {
		cpus = 1
	}
	return load / float64(cpus), true
}

// queuePass — проход очереди: работы по одной, с паузой между ними.
func (e *Engine) queuePass(ctx context.Context) string {
	if e.QueuePaused() {
		return "очередь на паузе"
	}
	if limit := e.QueueMaxLoad(); limit > 0 {
		if load, ok := loadPerCPU(); ok && load > limit {
			return fmt.Sprintf("машина занята: нагрузка %.2f на ядро — проход пропущен", load)
		}
	}
	// теснота бывает не только по процессору: на Pi локальная модель
	// держит четыре с половиной гигабайта из восьми, и подкачка
	// кончается раньше, чем нагрузка дорастает до предела
	if needFree := e.QueueMinFreeMB(); needFree > 0 {
		if free, ok := MemoryFree(); ok && free < needFree {
			return fmt.Sprintf("машине тесно: свободно %d МБ из нужных %d — проход пропущен", free, needFree)
		}
	}
	items, err := e.store.TakeWork(e.QueueBatch(), queueLeaseMins*time.Minute)
	if err != nil {
		return "очередь: " + err.Error()
	}
	if len(items) == 0 {
		if n := e.store.PurgeWorkDone(time.Now().AddDate(0, 0, -e.QueueKeepDays()).Format(time.RFC3339)); n > 0 {
			return fmt.Sprintf("работы нет · убрано старого %d", n)
		}
		return "работы нет"
	}
	ctx = llm.WithRetryable(ctx) // неудача внутри очереди — повтор, а не отказ
	handlers := e.queueHandlers()
	done, failed, held, later := 0, 0, 0, 0
	lost := 0 // состояние работы не записалось: она может повториться
	maxLoad := e.QueueMaxLoad()
	minFree := e.QueueMinFreeMB()
	for i, w := range items {
		// нагрузка проверяется и между работами: локальная модель греет
		// процессор, и лучше отложить остаток, чем занять машину целиком
		if i > 0 && minFree > 0 {
			if free, ok := MemoryFree(); ok && free < minFree {
				for _, rest := range items[i:] {
					_ = e.store.PostponeWork(rest.ID, time.Now().Add(2*time.Minute), fmt.Sprintf("машине тесно: свободно %d МБ", free))
					later++
				}
				break
			}
		}
		if i > 0 && maxLoad > 0 {
			if load, ok := loadPerCPU(); ok && load > maxLoad {
				for _, rest := range items[i:] {
					_ = e.store.PostponeWork(rest.ID, time.Now().Add(2*time.Minute), fmt.Sprintf("машина занята: нагрузка %.2f", load))
					later++
				}
				break
			}
		}
		pause := e.pauseFor(w.Kind)
		if i > 0 && pause > 0 {
			select { // между работами дышим: процессор не наш одного
			case <-ctx.Done():
				// в ожидание возвращается ВЕСЬ остаток пачки: иначе взятые
				// работы висят «в работе» до конца аренды и делаются позже,
				// чем могли бы
				for _, rest := range items[i:] {
					_ = e.store.PostponeWork(rest.ID, time.Now(), "проход прерван")
				}
				return fmt.Sprintf("сделано %d · прервано, остаток вернулся в очередь", done)
			case <-time.After(pause):
			}
		}
		h, ok := handlers[w.Kind]
		if !ok {
			_ = e.store.PostponeWork(w.ID, time.Now().Add(time.Hour), "некому делать: вид "+w.Kind)
			later++
			continue
		}
		err := h(ctx, w)
		var pp *errPostpone
		switch {
		case err == nil:
			// запись состояния — часть работы: не записалось, значит
			// работа будет сделана второй раз, и это надо видеть
			// след: что стало другим у объекта. По нему наблюдатель
			// отличает сделанную работу от потраченной — см. work_trace.go
			if trace, has := e.Trace(w.Kind, w.Ref); has {
				if terr := e.store.RememberTrace(w.ID, trace); terr != nil {
					log.Printf("очередь: след работы %d не записался: %v", w.ID, terr)
				}
			}
			if ferr := e.store.FinishWork(w.ID); ferr != nil {
				log.Printf("очередь: работа %d сделана, но состояние не записалось: %v", w.ID, ferr)
				lost++
				break
			}
			done++
		case errors.As(err, &pp):
			n, perr := e.store.PostponeWorkN(w.ID, pp.until, pp.why)
			if perr != nil {
				log.Printf("очередь: работа %d не отложилась: %v", w.ID, perr)
				lost++
				break
			}
			// причина, которая не проходит сама, однажды перестаёт быть
			// «временной»: работа встаёт и зовёт человека, а не крутится в
			// очереди месяцами, растя счётчик и молча
			if n >= e.postponeLimit() {
				_ = e.store.HoldWork(w.ID, "не проходит само: "+pp.why)
				e.tellStuckReason(w.Kind, pp.why)
				held++
				break
			}
			later++
		case llm.IsNetworkErr(err):
			// связи нет — работа не виновата. Попытку не тратим: иначе ночь
			// без Wi-Fi отправляет всю очередь в «застряло» с уведомлением
			// на каждую, а утром, когда сеть вернётся, чинить нечего
			if perr := e.store.PostponeWork(w.ID, time.Now().Add(e.NetRetryDelay()), "нет связи: "+clip(err.Error(), 120)); perr != nil {
				log.Printf("очередь: работа %d не отложилась: %v", w.ID, perr)
				lost++
				break
			}
			later++
		default:
			wasHeld, ferr := e.store.FailWork(w.ID, err.Error(), time.Now().Add(retryDelay(w.Attempts+1)), e.QueueMaxTries())
			if ferr != nil {
				log.Printf("очередь: неудача работы %d не записалась: %v", w.ID, ferr)
				lost++
				break
			}
			if wasHeld {
				held++
				e.notifyHeldWork(w, err)
			} else {
				failed++
			}
		}
	}
	out := fmt.Sprintf("сделано %d · ошибок %d · застряло %d · отложено %d", done, failed, held, later)
	if lost > 0 {
		// молчать нельзя: такая работа повторится, и человек должен это
		// видеть на экране очереди, а не гадать
		out += fmt.Sprintf(" · состояние не записалось у %d", lost)
	}
	return out
}

// retryDelay — пауза перед следующей попыткой: минута, пять, полчаса, два часа.
func retryDelay(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return time.Minute
	case attempt == 2:
		return 5 * time.Minute
	case attempt == 3:
		return 30 * time.Minute
	default:
		return 2 * time.Hour
	}
}

// postponeLimit — сколько раз работу можно отложить по одной и той же
// «временной» причине, прежде чем считать её постоянной. Шесть — это час
// при десятиминутном шаге: временное за час проходит, постоянное нет.
func (e *Engine) postponeLimit() int { return e.settingInt("queue.postpone_limit", 6) }

// tellStuckReason — причина, которая сама не пройдёт: работа встала, и
// человек должен узнать, что именно чинить. Одна запись на вид работы:
// шестьдесят одинаковых уведомлений — это не забота, а шум.
func (e *Engine) tellStuckReason(kind, why string) {
	title := QueueKinds[kind]
	if title == "" {
		title = kind
	}
	held, _ := e.store.WorkList(kind, store.WorkHeld, 200)
	_, _ = e.Notify(&store.Notification{
		Key: "queue-stuck:" + kind, Source: "core", Level: "warn",
		Title:   "Очередь · " + title + ": работа встала",
		Summary: clip(why, 200),
		Fields: []store.NotifyField{
			{Label: "Причина", Value: clip(why, 300), Level: "warn"},
			{Label: "Встало работ", Value: strconv.Itoa(len(held))},
			{Label: "Что дальше", Value: "причина сама не пройдёт: почините её и нажмите «Повторить застрявшие» на экране «Очередь»"},
		},
		Actions: []store.NotifyAction{
			{Label: "Открыть очередь", Kind: "goto", Screen: "queue", Style: "primary"},
			{Label: "Понял", Kind: "done"},
		},
	})
}

// notifyHeldWork — работа застряла: человек видит её в «Уведомлениях».
func (e *Engine) notifyHeldWork(w *store.WorkItem, err error) {
	title := QueueKinds[w.Kind]
	if title == "" {
		title = w.Kind
	}
	held, _ := e.store.WorkList(w.Kind, store.WorkHeld, 50)
	_, _ = e.Notify(&store.Notification{
		Key: "queue:" + w.Kind, Source: "core", Level: "warn",
		Title:   "Очередь · " + title + ": работа застряла",
		Summary: fmt.Sprintf("не далась после %d попыток · всего застряло %d", w.Attempts+1, len(held)),
		Fields: []store.NotifyField{
			{Label: "Объект", Value: w.Ref},
			{Label: "Ошибка", Value: clip(err.Error(), 300), Level: "warn"},
			{Label: "Что дальше", Value: "остальная очередь идёт своим чередом; можно повторить или пропустить"},
		},
		Actions: []store.NotifyAction{
			{Label: "Открыть очередь", Kind: "goto", Screen: "queue", Style: "primary"},
			{Label: "Понял", Kind: "done"},
		},
	})
}

// ---- сборщик ----

// reconcilePass — страховка: объекты, которым нужна работа, но её нет в
// очереди. Очередь не единственный источник правды, потерять нельзя.
func (e *Engine) reconcilePass() string {
	added := 0
	if e.MemoryReviewOn() {
		list, err := e.store.MemoriesForReview(200)
		if err == nil {
			for _, m := range list {
				ref := strconv.FormatInt(m.ID, 10)
				if e.store.HasWork(WorkMemoryReview, ref) {
					continue
				}
				if err := e.store.Enqueue(WorkMemoryReview, ref, "", 4); err == nil {
					added++
				}
			}
		}
	}
	added += e.EnqueueTranslateBacklog(200)
	// указатель поиска по письмам: 1700 писем легли в базу до того, как он
	// появился, и без досыпки не искались бы вовсе
	added += e.seedCursor(300)
	if added == 0 {
		return "очередь полна работы или пуста — добавлять нечего"
	}
	return fmt.Sprintf("поставлено в очередь: %d", added)
}

// EnqueueAllMemoryReview — «Разобрать сейчас»: вся очередь разбора сразу и
// с высоким приоритетом; воркер всё равно идёт по одной.
func (e *Engine) EnqueueAllMemoryReview() int {
	list, err := e.store.MemoriesForReview(500)
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range list {
		if err := e.store.Enqueue(WorkMemoryReview, strconv.FormatInt(m.ID, 10), "", 8); err == nil {
			n++
		}
	}
	return n
}

// EnqueueMemoryReview — постановка при рождении записи.
func (e *Engine) EnqueueMemoryReview(id int64, priority int) {
	_ = e.store.Enqueue(WorkMemoryReview, strconv.FormatInt(id, 10), "", priority)
}

// handleMemoryReview — одна запись памяти: подтвердить, переписать, забыть.
func (e *Engine) handleMemoryReview(ctx context.Context, w *store.WorkItem) error {
	id, err := strconv.ParseInt(w.Ref, 10, 64)
	if err != nil {
		return fmt.Errorf("непонятная ссылка %q", w.Ref)
	}
	m, err := e.store.MemoryByID(id)
	if err != nil {
		return nil // записи больше нет: работа сделана сама собой
	}
	if m.Confirmed {
		_ = e.store.SetMemoryReviewed(m.ID)
		return nil
	}
	area, ok := e.store.MemoryArea(m.Area)
	if !ok || area.Confirm == store.ConfirmAsk {
		_ = e.store.SetMemoryReviewed(m.ID) // решает человек
		return nil
	}
	if until, resting := e.roleCooldown(RoleMemory); resting && !e.CloudHelpOn() {
		return postpone(time.Until(until)+time.Minute, "роль «Память» на передышке")
	} else if resting {
		_ = until
	}
	cloud, why := e.needsCloudHelp(WorkMemoryReview)
	if cloud && why != "" && e.store.Setting("queue.cloud_note", "") != why {
		_ = e.store.SetSetting("queue.cloud_note", why)
		log.Printf("очередь: подключаю облачную запасную — %s", why)
	}
	v, err := e.askVerdict(ctx, m, cloud)
	if err != nil {
		if isBusyErr(err) {
			return postpone(10*time.Minute, clip(err.Error(), 120))
		}
		return err
	}
	e.applyVerdict(ctx, m, v)
	return nil
}

func isBusyErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "передышка") || strings.Contains(s, "бюджет") ||
		strings.Contains(s, "не подключена") || strings.Contains(s, "закрыта наружу")
}

func runtimeCPUs() int { return runtime.NumCPU() }

// StopSoftSec — сколько ждать, пока начатое доделается при остановке.
// Пять секунд, зашитые в код, обрывали действие посреди похода в IMAP или
// на сервер: работа оставалась без записи результата. В compose рядом стоит
// `stop_grace_period` — он должен быть заметно больше, иначе Docker убьёт
// раньше, чем Сёма закроется.
func (e *Engine) StopSoftSec() int {
	return e.settingInt("stop.soft_s", 30)
}

// FillCursor — довести указатель поиска до конца, а не «по 300 за
// час». Зовётся при старте отдельной ниткой: 1700 писем — это 1700 простых
// вставок, они укладываются в секунды даже на Raspberry Pi.
//
// Без этого поиск выглядит сломанным ровно так, как выглядел 10 сентября
// 2026: поле есть, запрос принимается, ответ — «ничего не нашлось». Новая
// возможность, которая молчит первый час, ничем не отличается от
// отсутствующей.
func (e *Engine) FillCursor() {
	total := 0
	for pass := 0; pass < 40; pass++ {
		n := e.seedCursor(500)
		if n == 0 {
			break
		}
		total += n
	}
	if total > 0 {
		log.Printf("поиск по письмам: в указатель добавлено %d писем", total)
	}
	e.fixSnippets()
}

// fixSnippets — разовая уборка превью. Собранные из сырого письма
// превью («% 96 <![e») пересобираются из готового текста; тот, что и так
// хорош, не трогается.
func (e *Engine) fixSnippets() {
	if e.store.Setting("mail.snippets_repaired", "") == "1" {
		return
	}
	ids, err := e.store.SnippetsToRepair(3000)
	if err != nil || len(ids) == 0 {
		return
	}
	bodies, _ := e.store.Bodies(ids)
	edited := 0
	for _, id := range ids {
		m, err := e.store.Message(id)
		if err != nil {
			continue
		}
		was := m.Snippet
		e.store.CleanSnippet(m, bodies[id].Text)
		if fresh, err := e.store.Message(id); err == nil && fresh.Snippet != was {
			edited++
		}
	}
	_ = e.store.SetSetting("mail.snippets_repaired", "1")
	if edited > 0 {
		log.Printf("превью писем: пересобрано %d из текста письма", edited)
	}
}

// seedCursor — письма, которых нет в полнотекстовом указателе.
// Не через очередь: это не вызов модели, а один SQL на письмо, и растянуть
// его на часы значило бы держать поиск полуслепым те же часы.
func (e *Engine) seedCursor(limit int) int {
	list, err := e.store.MessagesToIndex(limit)
	if err != nil || len(list) == 0 {
		return 0
	}
	ids := make([]int64, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	bodies, _ := e.store.Bodies(ids)
	for _, m := range list {
		text := m.Snippet
		if b, ok := bodies[m.ID]; ok && b.Text != "" {
			text = b.Text
		}
		e.store.IndexMessage(m.ID, m.Subject, m.FromAddr+" "+m.FromName, text)
	}
	return len(list)
}
