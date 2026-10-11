package engine

// Журнал расхода и здоровье: кто сколько съел (модели, код), снимки
// процесса, пороги жёлтого и красного. Провайдеры моделей сообщают о каждом
// вызове через llm.OnCall; запуски кода записывают разборщик событий,
// задачи и песочница. Модулям и скриптам делать ничего не нужно.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/host"
	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const WorkerPulse = "pulse"

// Пороги здоровья — настройки: ключ, подпись, умолчания жёлтого и красного.
type HealthThreshold struct {
	Key, Title, Hint, Unit string
	Yellow, Red            float64
	LowerIsBad             bool // для «свободно на диске»: плохо, когда меньше
}

var healthThresholds = []HealthThreshold{
	{Key: "health.disk_free_gb", Title: "Свободно на диске", Hint: "жёлтая и красная точка, когда меньше", Unit: "ГБ", Yellow: 5, Red: 1, LowerIsBad: true},
	{Key: "health.fail_pct", Title: "Отказы модели за час", Hint: "доля вызовов с ошибкой", Unit: "%", Yellow: 5, Red: 20},
	{Key: "health.queue_min", Title: "Очередь событий ждёт", Hint: "самое старое необработанное событие", Unit: "мин", Yellow: 2, Red: 15},
	{Key: "health.poll_min", Title: "Почта не проверялась", Hint: "с последнего удачного опроса", Unit: "мин", Yellow: 30, Red: 180},
	{Key: "health.rss_mb", Title: "Память процесса", Hint: "сервер на Pi с 8 ГБ", Unit: "МБ", Yellow: 1500, Red: 3000},
	{Key: "health.temp_c", Title: "Температура процессора", Hint: "машина, где работает Сёма; Pi без кулера троттлит от 80", Unit: "°C", Yellow: 70, Red: 80},
}

const (
	HealthSlowKey = "health.slow_sec" // медленная операция — в ленту
	HealthKeepKey = "health.keep_days"
	// HealthKeepRunsKey — свой срок для отметок о запусках кода. Их пишет
	// каждый проход фонового воркера: на живой Сёме 150 тыс. строк «run»
	// против 1,9 тыс. строк «llm». Денег по ним не считают, а место они
	// занимают всё; девяносто дней таких отметок — это два миллиона строк.
	HealthKeepRunsKey = "health.keep_run_days"
	healthSlowDef     = 30.0
	healthKeepDays    = 90
	healthKeepRunDays = 14
)

func (e *Engine) HealthThresholds() []HealthThreshold {
	out := make([]HealthThreshold, len(healthThresholds))
	for i, t := range healthThresholds {
		out[i] = t
		if v, err := strconv.ParseFloat(e.store.Setting(t.Key+".yellow", ""), 64); err == nil {
			out[i].Yellow = v
		}
		if v, err := strconv.ParseFloat(e.store.Setting(t.Key+".red", ""), 64); err == nil {
			out[i].Red = v
		}
	}
	return out
}

func (e *Engine) SlowSeconds() float64 {
	if v, err := strconv.ParseFloat(e.store.Setting(HealthSlowKey, ""), 64); err == nil && v > 0 {
		return v
	}
	return healthSlowDef
}

func (e *Engine) UsageKeepDays() int {
	if v, err := strconv.Atoi(e.store.Setting(HealthKeepKey, "")); err == nil && v > 0 {
		return v
	}
	return healthKeepDays
}

// UsageKeepRunDays — сколько держим отметки о запусках кода. Отдельно от
// журнала вызовов модели: см. HealthKeepRunsKey.
func (e *Engine) UsageKeepRunDays() int {
	if v, err := strconv.Atoi(e.store.Setting(HealthKeepRunsKey, "")); err == nil && v > 0 {
		return v
	}
	return healthKeepRunDays
}

// installUsage ставит колбэк учёта провайдерам моделей.
func (e *Engine) installUsage() {
	llm.OnCall = func(c llm.Call) {
		consumer := c.Consumer
		if consumer == "" {
			consumer = "core"
		}
		row := &store.UsageRow{
			Consumer: consumer, Kind: "llm", Model: c.Model, Role: c.Kind,
			TokensIn: c.Usage.In, TokensOut: c.Usage.Out, Estimated: !c.Usage.Exact,
			Seconds: c.Seconds, OK: c.Err == nil,
		}
		if c.Err != nil {
			row.Note = clip(c.Err.Error(), 300)
		}
		noteNetwork(c.Err) // сетевая беда — состояние, а не ошибка вызова
		row.Retry = c.Retryable
		row.Cached = c.Usage.Cached
		// цена от провайдера точнее нашей: у Perplexity в неё входит и плата
		// за поиск, которой в прайсе за миллион токенов нет вовсе
		if c.Usage.Cost > 0 {
			row.Cost, row.Currency = c.Usage.Cost, c.Usage.Currency
			row.Note = withNote(row.Note, "цена от провайдера")
		} else if c.Usage.AudioSec > 0 && c.Err == nil {
			// звук считается минутами, а не токенами; нет цены — так и
			// пишем, а не молчаливый ноль
			var priced bool
			row.Cost, row.Currency, priced = e.costOfAudio(c.Model, c.Usage)
			row.Note = withNote(row.Note, audioNote(c.Usage.AudioSec, priced))
		} else {
			row.Cost, row.Currency = e.costOfUsage(c.Model, c.Usage)
		}
		if err := e.store.AddUsage(row); err != nil {
			// журнал не должен ломать работу: только лог
			fmt.Fprintln(os.Stderr, "журнал расхода:", err)
		}
		e.recordSent(c) // что именно ушло модели
	}
}

// recordRun — запуск кода: событие модуля, задача, скрипт.
func (e *Engine) recordRun(consumer string, seconds float64, ok bool, note string) {
	if consumer == "" {
		consumer = "core"
	}
	_ = e.store.AddUsage(&store.UsageRow{Consumer: consumer, Kind: "run", Seconds: seconds, OK: ok, Note: clip(note, 300)})
}

// costOfUsage — деньги с учётом кэша входа. У DeepSeek вход, попавший в
// кэш, дешевле обычного примерно в тридцать раз: без этого счёт завышен
// настолько же. Цена кэша — третье число в прайсе
// («$0.22 / $0.66 / $0.007 / 1M»); нет его — считаем весь вход обычным,
// то есть по-старому и в сторону завышения, а не занижения.
func (e *Engine) costOfUsage(model string, u llm.Usage) (float64, string) {
	if !e.ModelIsRemote(model) {
		return 0, ""
	}
	for _, c := range e.userModels() {
		if c.Name != model {
			continue
		}
		price := c.Price
		// в часы пик действует своя цена: у DeepSeek дневной тариф вдвое
		// дороже ночного. Расписание — настройка модели, а не код
		if c.PricePeak != "" && inPeak(c.Peak, time.Now()) {
			price = c.PricePeak
		}
		if _, _, perMinute := parsePerMinute(price); perMinute {
			return 0, "" // модель речи: цена за минуту звука, токенами не считается (costOfAudio)
		}
		in, out, cache, cur := parsePrices3(price)
		if in <= 0 && out <= 0 {
			return 0, ""
		}
		fresh := u.In - u.Cached
		if fresh < 0 || cache <= 0 {
			fresh, u.Cached = u.In, 0
		}
		cost := float64(fresh)/1_000_000*in + float64(u.Cached)/1_000_000*cache +
			float64(u.Out)/1_000_000*out
		return cost, cur
	}
	return 0, ""
}

// parsePrices3 — вход, выход и кэшированный вход. Две первые цены как
// раньше; третья необязательна.
func parsePrices3(s string) (in, out, cache float64, cur string) {
	in, out, cur = parsePrices(s)
	parts := strings.Split(s, "/")
	if len(parts) >= 4 {
		if v, _ := parsePrice(parts[2]); v > 0 {
			cache = v
		}
	}
	return in, out, cache, cur
}

// costOf — деньги по цене модели из настроек. Цен две: за миллион входных и
// за миллион выходных токенов («$0.55 / $2.20 / 1M»). Выход почти везде
// дороже входа в несколько раз, и общая цена на оба вида давала заметную
// ошибку в обе стороны. Одно число в настройке по-прежнему принимается —
// тогда оно применяется к обоим видам, как раньше. Локальные — бесплатно.
func (e *Engine) costOf(model string, in, out int) (float64, string) {
	if !e.ModelIsRemote(model) {
		return 0, ""
	}
	for _, c := range e.userModels() {
		if c.Name != model {
			continue
		}
		priceIn, priceOut, cur := parsePrices(c.Price)
		if priceIn <= 0 && priceOut <= 0 {
			return 0, ""
		}
		return float64(in)/1_000_000*priceIn + float64(out)/1_000_000*priceOut, cur
	}
	return 0, ""
}

// parsePrices — цены входа и выхода из строки настройки. «$0.55 / 1M» —
// одна цена на оба вида (как было). «$0.55 / $2.20 / 1M» — вход и выход.
func parsePrices(s string) (in, out float64, cur string) {
	in, cur = parsePrice(s)
	parts := strings.Split(s, "/")
	if len(parts) >= 3 {
		if v, _ := parsePrice(parts[1]); v > 0 {
			return in, v, cur
		}
	}
	return in, in, cur
}

// withNote — приписка к заметке журнала, не затирая уже написанное.
func withNote(was, what string) string {
	if was == "" {
		return what
	}
	return was + " · " + what
}

// parsePrice — «$0.55 / 1M», «0.55», «₽120/1M» → число за миллион и валюта.
func parsePrice(s string) (float64, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ""
	}
	cur := "$"
	switch {
	case strings.Contains(s, "₽"):
		cur = "₽"
	case strings.Contains(s, "€"):
		cur = "€"
	}
	num := strings.Builder{}
	for _, r := range s {
		if r >= '0' && r <= '9' || r == '.' {
			num.WriteRune(r)
		} else if r == ',' {
			num.WriteRune('.')
		} else if num.Len() > 0 {
			break
		}
	}
	v, err := strconv.ParseFloat(num.String(), 64)
	if err != nil {
		return 0, ""
	}
	return v, cur
}

// TaskConsumer — подпись расхода для задачи: воркер ядра, модуль, скрипт, своя.
func TaskConsumer(t *store.Task) string {
	if mod, _ := ModuleOfTask(t.System); mod != "" {
		return "module:" + mod
	}
	if t.System != "" {
		return "core:" + t.System
	}
	if t.ScriptID != 0 {
		return "script:" + strconv.FormatInt(t.ScriptID, 10)
	}
	return "task:" + strconv.FormatInt(t.ID, 10)
}

// ---- процесс ----

var (
	processStarted = time.Now()
	lastCPU        time.Duration
	lastCPUAt      time.Time
)

// Снимок процесса переехал в `internal/host`: спрашивать у операционной
// системы память, процессор и место на диске — не дело пакета про
// предметную область, и из-за этого `engine` не собирался под Windows.
func cpuTime() time.Duration     { return host.CPUTime() }
func rssBytes() int64            { return host.ProcessRSS() }
func diskFree(path string) int64 { return host.DiskFree(path) }
func fileSize(p string) int64    { return host.FileSize(p) }

// pulsePass — снимок процесса раз в минуту плюс уборка журнала.
func (e *Engine) pulsePass(ctx context.Context) string {
	now := time.Now()
	cpu := cpuTime()
	pct := 0.0
	if !lastCPUAt.IsZero() && now.Sub(lastCPUAt) > 0 {
		pct = float64(cpu-lastCPU) / float64(now.Sub(lastCPUAt)) * 100
	}
	lastCPU, lastCPUAt = cpu, now
	files, fbytes, _ := e.StorageStat("")
	p := &store.ProcessStat{
		At: now, RSS: rssBytes(), CPUPct: pct, Goroutines: runtime.NumGoroutine(),
		DBBytes: fileSize(e.conf.DB), WALBytes: fileSize(e.conf.DB + "-wal"), FilesBytes: fbytes,
		DiskFree: diskFree(e.conf.Data), Uptime: int64(now.Sub(processStarted).Seconds()),
	}
	_ = e.store.AddProcessStat(p)
	if now.Minute() == 0 {
		e.store.Checkpoint() // WAL рос до размера базы — раз в час сбрасываем
		e.store.RollupProcessStats()
		e.store.PruneUsage(time.Duration(e.UsageKeepDays())*24*time.Hour,
			time.Duration(e.UsageKeepRunDays())*24*time.Hour)
		e.SettingsLogPurgePass()
		// закрытые находки: открытые не трогаем никогда, сколько бы им ни
		// было лет — незакрытая находка это незаконченное дело
		e.store.RemoveOldFindings(e.WatchKeepDays())
	}
	_ = ctx
	return fmt.Sprintf("память %s · CPU %.0f%% · горутин %d · база %s · файлов %d · свободно %s",
		humanBytes(p.RSS), pct, p.Goroutines, humanBytes(p.DBBytes), files, humanBytes(p.DiskFree))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f ГБ", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f МБ", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n>>10)
	}
	return fmt.Sprintf("%d Б", n)
}

// ---- состояние ----

// briefErr — из «Post "http://embed:8080/v1/embeddings": dial tcp
// …: connect: connection refused» человеку нужен хвост, а не весь адрес.
func briefErr(s string) string {
	if s == "" {
		return "причина не названа"
	}
	if i := strings.LastIndex(s, ": "); i > 0 && len(s)-i < 60 {
		return strings.TrimSpace(s[i+2:])
	}
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

type HealthCard struct {
	Key, Title, Value, Meta, Level string // level: ok | warn | bad
}

// HealthCards — карточки состояния и общий уровень для точки в меню.
//
// Считаются не чаще раза в десять секунд. Точка в меню стоит на каждом
// экране, и без памяти карточки собирались заново на каждый запрос —
// семь запросов к базе ради одного цвета; на «Здоровье» они же считались
// второй раз для самого экрана. Пульс пишет раз в минуту, десять секунд
// давности здесь не видны никому.
func (e *Engine) HealthCards() (cards []HealthCard, level string) {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	if time.Since(e.healthWhen) < 10*time.Second && e.healthCardsCache != nil {
		return append([]HealthCard(nil), e.healthCardsCache...), e.healthLevel
	}
	cards, level = e.healthCards()
	e.healthCardsCache, e.healthLevel, e.healthWhen = cards, level, time.Now()
	return append([]HealthCard(nil), cards...), level
}

// ResetHealth — забыть посчитанное: после действия человека
// («Это его новое место», подтверждение привязки) точка обязана
// измениться сразу, а не через десять секунд.
func (e *Engine) ResetHealth() {
	e.healthMu.Lock()
	e.healthWhen = time.Time{}
	e.healthMu.Unlock()
}

func (e *Engine) healthCards() (cards []HealthCard, level string) {
	th := map[string]HealthThreshold{}
	for _, t := range e.HealthThresholds() {
		th[t.Key] = t
	}
	worst := "ok"
	bump := func(l string) {
		if l == "bad" || (l == "warn" && worst == "ok") {
			worst = l
		}
	}
	grade := func(t HealthThreshold, v float64) string {
		if t.LowerIsBad {
			switch {
			case v <= t.Red:
				return "bad"
			case v <= t.Yellow:
				return "warn"
			}
			return "ok"
		}
		switch {
		case v >= t.Red:
			return "bad"
		case v >= t.Yellow:
			return "warn"
		}
		return "ok"
	}
	// сервер
	var last *store.ProcessStat
	if list, err := e.store.ProcessStats(time.Now().Add(-3 * time.Minute)); err == nil && len(list) > 0 {
		last = list[len(list)-1]
	}
	if last != nil {
		l := grade(th["health.rss_mb"], float64(last.RSS)/float64(1<<20))
		bump(l)
		cards = append(cards, HealthCard{Key: "server", Title: "Сервер", Value: humanDuration(time.Duration(last.Uptime) * time.Second),
			Meta: fmt.Sprintf("аптайм · %s памяти · %d горутин · CPU %.0f%%", humanBytes(last.RSS), last.Goroutines, last.CPUPct), Level: l})
	} else {
		cards = append(cards, HealthCard{Key: "server", Title: "Сервер", Value: humanDuration(time.Since(processStarted)), Meta: "аптайм · первый снимок через минуту", Level: "ok"})
	}
	// машина: температура и нагрузка хозяйского железа видны и из контейнера
	if m := ReadMachine(); m.HasTemp || m.Load1 > 0 {
		l := "ok"
		if m.HasTemp {
			l = grade(th["health.temp_c"], m.TempC)
		}
		bump(l)
		value := fmt.Sprintf("нагрузка %.2f", m.Load1)
		if m.HasTemp {
			value = fmt.Sprintf("%.0f °C", m.TempC)
		}
		cards = append(cards, HealthCard{Key: "machine", Title: "Машина", Value: value,
			Meta: m.String() + " · полный контроль — сервер «хозяин» на «Серверах»", Level: l})
	}
	// модели: повтор и отказ — разные вещи. Повтор переделает очередь, и
	// краснеть из-за него не нужно; отказ значит, что работу никто не подберёт
	calls, retries, fails := e.store.ModelCallStat(time.Now().Add(-time.Hour))
	pct := 0.0
	if calls > 0 {
		pct = float64(fails) / float64(calls) * 100
	}
	// доля считается только на разумном числе вызовов: при пяти вызовах в
	// час один отказ — это двадцать процентов и красный экран на ровном
	// месте. Пока вызовов мало, смотрим на сами отказы
	l := "ok"
	minCalls := e.settingInt("health.fail_min_calls", 20)
	switch {
	case calls >= minCalls:
		l = grade(th["health.fail_pct"], pct)
	case fails >= 3:
		l = "warn"
	}
	bump(l)
	meta := fmt.Sprintf("отказов %d · %.1f%%", fails, pct)
	if calls < minCalls {
		meta = fmt.Sprintf("отказов %d из %d · доля считается от %d вызовов", fails, calls, minCalls)
	}
	if retries > 0 {
		meta += fmt.Sprintf(" · повторов %d", retries)
	}
	cards = append(cards, HealthCard{Key: "models", Title: "Модели · час", Value: fmt.Sprintf("%d вызовов", calls),
		Meta: meta, Level: l})
	// Сёма нашёл что-то в себе. Карточка стоит здесь, а не только на своём
	// экране, ровно по той причине, ради которой наблюдатель и заводился:
	// все восемь молчаливых поломок прошли мимо «Здоровья» именно потому,
	// что уровни были в норме. Открытая находка зелёного состояния не даёт.
	if open, level := e.store.OpenFindings(); open > 0 {
		l := "warn"
		if level == store.FindingAlert {
			l = "bad"
		}
		bump(l)
		cards = append(cards, HealthCard{Key: "watch", Title: "Сёма нашёл в себе",
			Value: plural(open, "находка", "находки", "находок"),
			Meta: "работа идёт, а ничего не меняется · уровни при этом в норме — " +
				"именно так прошли мимо все прежние поломки", Level: l})
	}
	// поднята из копии: внешний мир ждёт подтверждения человека
	if note := e.bindingStatus(); note != "" {
		bump("bad")
		cards = append(cards, HealthCard{Key: "binding", Title: "Нужна привязка", Value: "почта и папки стоят",
			Meta: note + " · проверьте ящики, папки и адреса служб, остановите оригинал — и подтвердите ниже", Level: "bad"})
	}
	// закрытое ждёт доверенной модели: без неё письма закрытых ящиков
	// остаются без разметки, и человек должен видеть это здесь, а не
	// гадать, почему «Почта» молчит
	if n := e.store.MessagesWithReason(reasonNoTrusted); n > 0 {
		bump("warn")
		cards = append(cards, HealthCard{Key: "trust", Title: "Ждут доверенной модели", Value: fmt.Sprintf("%d писем", n),
			Meta: "закрытые ящики без разметки · назначьте доверенную модель на «Моделях»", Level: "warn"})
	}
	// векторы не строятся: поиск по смыслу и память тихо перестают
	// работать, а без карточки об этом узнаёшь только из журнала
	if inARow, why := e.VectorsNotBuilt(); inARow >= 5 {
		bump("bad")
		cards = append(cards, HealthCard{Key: "embed", Title: "Векторы не строятся",
			Value: fmt.Sprintf("%d подряд", inARow),
			Meta: "поиск по смыслу и память работают вслепую · " + briefErr(why) +
				" · адрес эмбеддера — на «Инструментах»", Level: "bad"})
	}
	// что ушло наружу за сутки
	if st := e.SentToday(); st.Calls > 0 {
		l = "ok"
		if st.Sensitive > 0 {
			l = "warn"
		}
		bump(l)
		cards = append(cards, HealthCard{Key: "sent", Title: "Ушло наружу · сутки",
			Value: fmt.Sprintf("%d отправок", st.Outside),
			Meta:  fmt.Sprintf("чувствительных %d · %s токенов · всего вызовов %d", st.Sensitive, humanCount(st.TokensOut), st.Calls), Level: l})
	}
	// очередь событий
	pending, oldest := e.store.PendingEventAge()
	l = "ok"
	if pending > 0 {
		l = grade(th["health.queue_min"], oldest.Minutes())
	}
	bump(l)
	cards = append(cards, HealthCard{Key: "events", Title: "События", Value: strconv.Itoa(pending) + " в очереди",
		Meta: fmt.Sprintf("самое старое %s · %s", humanDuration(oldest), e.eventsStatShort()), Level: l})
	// очередь фоновой работы: сколько ждёт и не застряло ли
	if stats, err := e.store.WorkStats(); err == nil {
		waiting, held, doneHr, running := 0, 0, 0, 0
		for _, st := range stats {
			waiting += st.Waiting + st.Paused
			held += st.Held
			doneHr += st.DoneHr
			running += st.Running
		}
		if waiting+held+doneHr+running > 0 {
			l = "ok"
			switch {
			case held > 0:
				l = "bad"
			case waiting > 50:
				l = "warn"
			}
			bump(l)
			value := strconv.Itoa(waiting) + " ждут"
			if waiting == 0 && held == 0 {
				value = "пусто"
			}
			meta := fmt.Sprintf("сделано за час %d · застряло %d", doneHr, held)
			if waiting > 0 && doneHr > 0 {
				meta += fmt.Sprintf(" · ~%d мин", waiting*60/doneHr)
			}
			if e.QueuePaused() {
				meta += " · на паузе"
			}
			cards = append(cards, HealthCard{Key: "queue", Title: "Очередь", Value: value, Meta: meta, Level: l})
		}
	}
	// почта
	pollAge := e.lastPollAge()
	l = "ok"
	if pollAge > 0 {
		l = grade(th["health.poll_min"], pollAge.Minutes())
	}
	bump(l)
	cards = append(cards, HealthCard{Key: "mail", Title: "Почта", Value: humanDuration(pollAge) + " назад",
		Meta: "последняя проверка · порог тишины из настроек", Level: l})
	// бюджеты
	if bl := e.BudgetLevel(); bl != "ok" {
		bump(bl)
		cards = append(cards, HealthCard{Key: "budgets", Title: "Бюджеты", Value: budgetLevelText(bl), Meta: "лимиты расхода — раздел «Бюджеты» ниже", Level: bl})
	}
	// диск и база
	if last != nil {
		l = grade(th["health.disk_free_gb"], float64(last.DiskFree)/float64(1<<30))
		bump(l)
		cards = append(cards, HealthCard{Key: "disk", Title: "База и диск", Value: humanBytes(last.DBBytes),
			Meta: fmt.Sprintf("файлы %s · свободно %s", humanBytes(last.FilesBytes), humanBytes(last.DiskFree)), Level: l})
	}
	return cards, worst
}

func budgetLevelText(l string) string {
	if l == "bad" {
		return "превышен"
	}
	return "почти исчерпан"
}

func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0 с"
	case d < time.Minute:
		return fmt.Sprintf("%.0f с", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.0f мин", d.Minutes())
	case d < 48*time.Hour:
		return fmt.Sprintf("%.0f ч %.0f мин", float64(int(d.Hours())), d.Minutes()-60*float64(int(d.Hours())))
	}
	return fmt.Sprintf("%d дн. %d ч", int(d.Hours())/24, int(d.Hours())%24)
}

func (e *Engine) eventsStatShort() string {
	done, failed := 0, 0
	for _, m := range e.Modules() {
		d, f, _ := e.store.ModuleEventStats(m.Name)
		done += d
		failed += f
	}
	return fmt.Sprintf("обработано %d · упало %d", done, failed)
}

// lastPollAge — сколько прошло с последнего удачного опроса почты.
func (e *Engine) lastPollAge() time.Duration {
	t, err := e.taskBySystem(WorkerPoll)
	if err != nil || t == nil || t.LastRun.IsZero() {
		return 0
	}
	return time.Since(t.LastRun)
}

// ---- бюджеты ----

// BudgetState — бюджет и расход к нему за его период.
type BudgetState struct {
	Budget   *store.Budget
	Spent    float64
	Pct      int
	Exceeded bool
	Since    time.Time
}

func budgetSince(period string) time.Time {
	now := time.Now()
	switch period {
	case "week":
		return now.Add(-7 * 24 * time.Hour)
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// budgetMatches — подходит ли бюджет потребителю: точное имя или вид.
func budgetMatches(b *store.Budget, consumer string) bool {
	if b.Consumer == consumer {
		return true
	}
	kind, _, _ := strings.Cut(consumer, ":")
	if kind == "task" {
		kind = "script"
	}
	return b.Consumer == kind
}

// BudgetStates — все бюджеты с расходом (для экрана и точки в меню).
func (e *Engine) BudgetStates() []BudgetState {
	budgets, err := e.store.Budgets()
	if err != nil {
		return nil
	}
	var out []BudgetState
	for _, b := range budgets {
		out = append(out, e.budgetState(b))
	}
	return out
}

func (e *Engine) budgetState(b *store.Budget) BudgetState {
	since := budgetSince(b.Period)
	var u *store.UsageSum
	if strings.Contains(b.Consumer, ":") {
		u = e.store.UsageFor(b.Consumer, since)
	} else {
		// вид потребителя: сумма по всем с таким префиксом
		u = e.store.UsageFor(b.Consumer, since)
		if b.Consumer == "script" {
			t := e.store.UsageFor("task", since)
			u.TokensIn += t.TokensIn
			u.TokensOut += t.TokensOut
			u.Cost += t.Cost
		}
	}
	spent := float64(u.TokensIn + u.TokensOut)
	if b.Kind == "money" {
		spent = u.Cost
	}
	st := BudgetState{Budget: b, Spent: spent, Since: since}
	if b.Limit > 0 {
		st.Pct = int(spent / b.Limit * 100)
		st.Exceeded = spent >= b.Limit
	}
	return st
}

// BudgetFor — самый напряжённый бюджет потребителя, для капсул.
func (e *Engine) BudgetFor(consumer string) *BudgetState {
	return BudgetPick(e.BudgetStates(), consumer)
}

// BudgetPick — то же самое, но по уже посчитанным состояниям. Экран со
// списком строк считает бюджеты один раз и спрашивает их для каждой
// строки: раньше каждая капсула звала BudgetStates заново, а тот на
// каждый бюджет ходил в журнал расхода — три запроса на строку.
func BudgetPick(states []BudgetState, consumer string) *BudgetState {
	var best *BudgetState
	for i := range states {
		if !budgetMatches(states[i].Budget, consumer) {
			continue
		}
		s := states[i]
		if best == nil || s.Pct > best.Pct {
			best = &s
		}
	}
	return best
}

// budgetCheck — перед вызовом модели: превышенный бюджет отклоняет вызов,
// пишет отказ в журнал и, если так задано, ставит потребителя на паузу.
func (e *Engine) budgetCheck(ctx context.Context) error {
	consumer := llm.ConsumerOf(ctx)
	if consumer == "" {
		consumer = "core"
	}
	for _, st := range e.BudgetStates() {
		if !budgetMatches(st.Budget, consumer) || !st.Exceeded {
			continue
		}
		b := st.Budget
		what := fmt.Sprintf("бюджет исчерпан: %s · %s за %s", e.budgetWho(b.Consumer), budgetLimitText(b), budgetPeriodText(b.Period))
		e.recordRun(consumer, 0, false, what)
		if b.Action == "pause" {
			e.pauseConsumer(consumer, what)
		}
		if _, open := e.store.OpenNotificationByKey("budget:" + consumer); !open {
			e.notifyBudget(consumer, what, b.Action == "pause")
		}
		return fmt.Errorf("%s", what)
	}
	return nil
}

// pauseConsumer — модуль выключается с причиной, задача выключается; чат
// просто получает отказ.
func (e *Engine) pauseConsumer(consumer, why string) {
	kind, id, _ := strings.Cut(consumer, ":")
	switch kind {
	case "module":
		if m, ok := e.Module(id); ok && m.Enabled {
			e.markBroken(id, why)
		}
	case "task", "script":
		tasks, err := e.store.Tasks()
		if err != nil {
			return
		}
		for _, t := range tasks {
			if TaskConsumer(t) == consumer && t.Enabled && !t.Root {
				_ = e.store.SetTaskEnabled(t.ID, false)
				_ = e.store.TaskRan(t.ID, "на паузе: "+why)
			}
		}
	}
}

func (e *Engine) budgetWho(consumer string) string {
	if kind, id, ok := strings.Cut(consumer, ":"); ok && kind == "module" {
		if m, ok := e.Module(id); ok {
			return "модуль «" + m.Title + "»"
		}
	}
	switch consumer {
	case "module":
		return "все модули"
	case "script":
		return "все скрипты и задачи"
	case "chat":
		return "все чаты"
	case "core":
		return "ядро"
	}
	return consumer
}

func budgetLimitText(b *store.Budget) string {
	if b.Kind == "money" {
		return fmt.Sprintf("%.2f $", b.Limit)
	}
	return fmt.Sprintf("%.0f токенов", b.Limit)
}

func budgetPeriodText(p string) string {
	switch p {
	case "week":
		return "неделю"
	case "month":
		return "месяц"
	}
	return "день"
}

// BudgetLevel — уровень для точки в меню: warn от 80 %, bad при превышении.
func (e *Engine) BudgetLevel() string {
	level := "ok"
	for _, st := range e.BudgetStates() {
		switch {
		case st.Exceeded:
			return "bad"
		case st.Pct >= 80:
			level = "warn"
		}
	}
	return level
}
