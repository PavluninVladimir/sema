package engine

// Наблюдатель: Сёма смотрит на форму собственной работы.
//
// Уровни меряет «Здоровье», готовность к запуску — самоосмотр. Здесь третий
// вопрос, которого не задавал никто: идёт ли работа куда-нибудь.
//
// 11–12 сентября 2026 в Сёме нашлось восемь тихих отказов, и семь из них не
// дали ни одной ошибки. Разбор одного QR-кода крутился двое суток и стоил
// 9,2 млн токенов; перечитывание тридцати восьми картинок шло неделями;
// суточные расписания не понимались планировщиком с рождения задач, и «Копия
// наружу» не снялась по расписанию ни разу; зеркало файлов стояло
// выключенным месяц. Все восемь нашёл человек, задавая вопросы базе руками.
//
// Здесь эти вопросы задаёт себе сам Сёма. Не «механизм поиска аномалий»
// вообще, а пять конкретных вопросов, каждый из которых уже поймал настоящую
// беду:
//
//	1. повтор без следа        — работа идёт и ничего не меняет
//	2. пауза в потолке         — растущая пауза стала состоянием
//	3. счётчик не растёт       — «сделано из всего» стоит на месте
//	4. объявлено != наблюдается— включённое не происходит
//	5. две записи спорят       — свой итог против своего же хранилища
//
// Наблюдатель не чинит найденное и не зовёт для разбора модель: он только
// смотрит, называет числами и, если работа стоит денег, останавливает её.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	WorkerSelfWatch = "self_watch"

	watchOnKey     = "watch.on"        // сама проверка
	watchStopKey   = "watch.stop"      // можно ли останавливать работу
	watchRepeatKey = "watch.repeat"    // сколько повторов без следа считать петлёй
	watchWindowKey = "watch.window_h"  // за какое окно считать
	watchManyKey   = "watch.many"      // сколько объектов одного вида — уже вид виноват
	watchKeepKey   = "watch.keep_days" // сколько держать закрытые находки
)

// WatchOn — наблюдает ли Сёма за собой. По умолчанию да: наблюдение ничего
// не меняет, оно только смотрит.
func (e *Engine) WatchOn() bool { return e.store.Setting(watchOnKey, "1") == "1" }

func (e *Engine) SetWatchOn(on bool) error {
	return e.store.SetSetting(watchOnKey, boolSetting(on))
}

// WatchStops — можно ли наблюдателю останавливать работу.
//
// По умолчанию нет, как у ночной смены с её холостым режимом, и по той же
// причине: право останавливать даётся человеком осознанно, когда видно, на
// чём оно срабатывает. Первые сутки — только тревоги.
func (e *Engine) WatchStops() bool { return e.store.Setting(watchStopKey, "0") == "1" }

func (e *Engine) SetWatchStops(on bool) error {
	return e.store.SetSetting(watchStopKey, boolSetting(on))
}

func boolSetting(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// Пороги — настройки, а не числа в коде: у разбора файлов и у перевода
// писем «слишком часто» разное.
func (e *Engine) watchRepeat() int { return e.settingInt(watchRepeatKey, 5) }
func (e *Engine) watchWindow() int { return e.settingInt(watchWindowKey, 6) }

// WatchRepeat и WatchWindow — те же пороги для экрана.
func (e *Engine) WatchRepeat() int { return e.watchRepeat() }
func (e *Engine) WatchWindow() int { return e.watchWindow() }

// SetWatchLimits — пороги правит человек на экране, а не мы в коде.
func (e *Engine) SetWatchLimits(retries, window, keep int) error {
	if retries >= 2 {
		if err := e.store.SetSetting(watchRepeatKey, strconv.Itoa(retries)); err != nil {
			return err
		}
	}
	if window >= 1 {
		if err := e.store.SetSetting(watchWindowKey, strconv.Itoa(window)); err != nil {
			return err
		}
	}
	if keep >= 7 {
		if err := e.store.SetSetting(watchKeepKey, strconv.Itoa(keep)); err != nil {
			return err
		}
	}
	return nil
}

// TaskResult — итог последнего прохода задачи, для экрана.
func (e *Engine) TaskResult(system string) string {
	t, err := e.taskBySystem(system)
	if err != nil || t == nil {
		return ""
	}
	return t.LastResult
}
func (e *Engine) watchMany() int     { return e.settingInt(watchManyKey, 3) }
func (e *Engine) WatchKeepDays() int { return e.settingInt(watchKeepKey, 90) }

// selfWatchPass — проход наблюдателя. Строка для журнала задачи.
func (e *Engine) selfWatchPass(ctx context.Context) string {
	if !e.WatchOn() {
		return "наблюдение выключено"
	}
	start := time.Now()
	// Собственная цена прохода — часть работы, а не «потом посмотрим»:
	// сторож, который сам съедает Pi, и есть та беда, которую он ищет.
	// Поэтому дорогая проверка называется в итоге по имени: «за 1,01 с» без
	// имени я уже читал полдня, гадая, какая из шести столько думает.
	found, heavy := 0, []string{}
	measure := func(name string, check func() int) {
		t := time.Now()
		found += check()
		if howMany := time.Since(t); howMany > 100*time.Millisecond {
			heavy = append(heavy, fmt.Sprintf("%s %s", name, howMany.Round(time.Millisecond)))
		}
	}
	measure("повторы", e.checkRetries)
	measure("объявленное", e.checkDeclared)
	measure("счётчики", e.checkCounters)
	measure("паузы", e.checkPauses)
	measure("спор", func() int { return e.checkDispute(ctx) })
	measure("самоосмотр", e.checkSelfCheck)

	result := fmt.Sprintf("проверок 6 · найдено %d · за %s", found,
		time.Since(start).Round(time.Millisecond))
	if len(heavy) > 0 {
		result += " · дольше всего: " + strings.Join(heavy, ", ")
	}
	if !e.WatchStops() {
		result += " · останавливать не разрешено, только тревоги"
	}
	return result
}

// ---- проверка 1: повтор без следа ----

// checkRetries — работа, которая идёт и ничего не меняет.
//
// Цена решает, тревога это или остановка: повтор, не стоящий денег, — беда,
// но не такая, чтобы будить человека ночью и рисковать остановкой настоящей
// работы; повтор, тративший токены, — 6,5 $ за двое суток.
//
// Пауза вешается на объект, а не на вид работы. Один зациклившийся файл не
// должен запирать разбор остальных: это ровно та ошибка, которую пришлось
// чинить в зеркале файлов 12 сентября, где один сетевой обрыв обрывал весь
// проход и начинал его заново с того же файла.
func (e *Engine) checkRetries() int {
	window := time.Duration(e.watchWindow()) * time.Hour
	retries, err := e.store.RepeatsWithoutTrace(e.watchRepeat(), time.Now().Add(-window))
	if err != nil || len(retries) == 0 {
		return 0
	}
	// много объектов одного вида — беда не в объекте, а в коде
	byKinds := map[string]int{}
	for _, p := range retries {
		byKinds[p.Kind]++
	}
	found := 0
	kindsStop := map[string]bool{}
	for kind, howMany := range byKinds {
		if howMany >= e.watchMany() {
			kindsStop[kind] = true
			f := store.Finding{
				Key: "повтор:вид:" + kind, Kind: "повтор без следа", Ref: kind,
				Level: store.FindingAlert,
				Summary: fmt.Sprintf("«%s» крутится на %s и ничего не меняет",
					kindName(kind), plural(howMany, "объекте", "объектах", "объектах")),
				Numbers: fmt.Sprintf("объектов %d, окно %d ч", howMany, e.watchWindow()),
				Measure: store.MeasureAlert,
				Screen:  "queue",
			}
			if e.WatchStops() {
				f.Measure = store.MeasurePauseKind
				if n, err := e.store.HoldKind(kind, "наблюдатель: работа идёт и ничего не меняет"); err == nil && n > 0 {
					f.Summary += " · вид работы остановлен: дело не в объекте"
				}
			}
			e.sayAboutFinding(f)
			found++
		}
	}
	for _, p := range retries {
		if kindsStop[p.Kind] {
			continue // про этот вид уже сказано целиком
		}
		price := e.retryPrice(p)
		f := store.Finding{
			Key: "повтор:" + p.Kind + ":" + p.Ref, Kind: "повтор без следа",
			Ref:   p.Kind + ":" + p.Ref,
			Level: store.FindingWarn,
			Summary: fmt.Sprintf("«%s» повторяется над одним объектом и ничего не меняет",
				kindName(p.Kind)),
			Numbers: fmt.Sprintf("%s за %s%s", plural(p.Count, "работа", "работы", "работ"),
				humanDuration(p.Last.Sub(p.First)), priceLine(price)),
			Measure: store.MeasureAlert,
			Screen:  "queue",
		}
		// дорогая петля останавливается сразу, дешёвая — только тревога
		if price > 0 {
			f.Level = store.FindingAlert
			if e.WatchStops() {
				f.Measure = store.MeasurePauseRef
				if n, err := e.store.HoldRef(p.Kind, p.Ref, "наблюдатель: повтор без следа"); err == nil && n > 0 {
					f.Summary += " · объект снят с работы, остальные идут"
				}
			}
		}
		e.sayAboutFinding(f)
		found++
	}
	return found
}

// retryPrice — сколько денег стоила эта работа. Ноль значит «моделей не
// звала»: такую петлю останавливать среди ночи незачем.
func (e *Engine) retryPrice(p store.Repeat) float64 {
	return e.store.PriceFor("core:queue", p.First, p.Last)
}

func priceLine(c float64) string {
	if c <= 0 {
		return ""
	}
	return fmt.Sprintf(", потрачено $%.2f", c)
}

func kindName(kind string) string {
	if name, ok := QueueKinds[kind]; ok {
		return name
	}
	return kind
}

// ---- проверка 4: объявленное против наблюдаемого ----

// checkDeclared — включённое не происходит.
//
// Самая дешёвая из пяти и самая обидная: «Копия наружу» стояла включённой с
// красивым расписанием «каждый день в 03:30», а планировщик такой строки не
// понимал и молча не запускал задачу ни разу за всё её существование.
func (e *Engine) checkDeclared() int {
	task, err := e.store.Tasks()
	if err != nil {
		return 0
	}
	now := time.Now()
	found := 0
	verified := map[string]bool{}
	// задачу могли удалить или выключить: её находка о молчании больше ни
	// к чему не относится и закрывается. Цикл ниже смотрит только живые
	// задачи, и 17 сентября 2026 находка о давно убранной задаче модуля
	// «Пробный пост» висела тревогой на «Здоровье»
	defer func() {
		open, err := e.store.Findings(true, 500)
		if err != nil {
			return
		}
		for _, n := range open {
			if strings.HasPrefix(n.Key, "молчит:") && !verified[n.Key] {
				e.closeIfWas(n.Key, "задачи больше нет или она выключена")
			}
		}
	}()
	for _, t := range task {
		if t.Root || !t.Enabled || t.AfterID != 0 {
			continue
		}

		key := "молчит:" + t.System
		if t.System == "" {
			key = "молчит:задача:" + strconv.FormatInt(t.ID, 10)
		}
		verified[key] = true
		// расписание, которого планировщик не понимает
		if _, err := nextRun(t.Schedule, t.LastRun, now); err != nil {
			e.sayAboutFinding(store.Finding{
				Key: key, Kind: "объявлено, но не происходит", Ref: t.Title,
				Level:   store.FindingAlert,
				Summary: fmt.Sprintf("задача «%s» включена, но её расписание планировщику непонятно — она не запустится никогда", t.Title),
				Numbers: fmt.Sprintf("расписание %q", t.Schedule),
				Measure: store.MeasureAlert, Screen: "tasks",
			})
			found++
			continue
		}
		// Расписание понятно, а запусков не было вовсе.
		//
		// Задача, которая работает прямо сейчас, своего запуска ещё не
		// записала: `last_run` ставится после прохода. На первом же проходе
		// наблюдатель нашёл так самого себя и объявил, что он ни разу не
		// запускался, — сидя внутри собственного запуска. Правило
		// распространяется и на него, и это оказалось не предположением.
		//
		// Пропускаем только этот приговор, а не задачу целиком: пропустив
		// её целиком, наблюдатель перестал закрывать собственную же
		// устаревшую находку — и она висела открытой, пока он бодро
		// отчитывался «найдено 0».
		if t.LastRun.IsZero() && !e.taskRunning(t.ID) {
			e.sayAboutFinding(store.Finding{
				Key: key, Kind: "объявлено, но не происходит", Ref: t.Title,
				Level:   store.FindingWarn,
				Summary: fmt.Sprintf("задача «%s» включена и ни разу не запускалась", t.Title),
				Numbers: fmt.Sprintf("расписание %q", t.Schedule),
				Measure: store.MeasureAlert, Screen: "tasks",
			})
			found++
			continue
		}
		e.closeIfWas(key, "задача пошла")
	}
	return found
}

// sayAboutFinding — записать и, если находка новая, сказать человеку.
// Повтор той же находки человека не будит: 120 одинаковых обрывов MAX за
// тринадцать часов никто не заметил именно потому, что они были одинаковы.
func (e *Engine) sayAboutFinding(n store.Finding) {
	fresh, err := e.store.SaveFinding(n)
	if err != nil || !fresh {
		return
	}
	// точка в меню помнит карточки десять секунд, а находка обязана
	// покрасить её сразу — ради этого она и есть
	e.ResetHealth()
	_, _ = e.Notify(&store.Notification{
		Key: "watch:" + n.Key, Source: "core", Level: n.Level,
		Title:   "Сёма нашёл у себя: " + clip(n.Summary, 120),
		Summary: n.Numbers,
		Actions: []store.NotifyAction{{Kind: "goto", Screen: n.Screen, Label: "Посмотреть"}},
	})
}

func (e *Engine) closeIfWas(key, why string) {
	_ = e.store.CloseFinding(key, why)
	e.CloseNotification("watch:"+key, store.NotifyDone, why)
	e.ResetHealth()
}
