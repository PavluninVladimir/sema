package store

// Счёт запросов к базе для заголовка Server-Timing (perf-pass 1.1).
//
// Замер 3 октября 2026 показал: «Здоровье» строится 2,1 с, а запросы к базе
// на нём быстрые — время уходило в Go. Чтобы следующий такой разбор
// начинался с числа, а не с догадки, у ответа экрана есть часть «база»:
// сколько запросов сделал этот экран и сколько они заняли.
//
// Вызовы хранилища контекста запроса не получают (их сотни, и тянуть
// контекст через каждый ради счёта — переписать слой целиком), поэтому
// счёт привязан к горутине: обработчик экрана включает его на себе, и
// обёртка над базой прибавляет только запросы этой горутины. Фоновая
// работа (сердцебиение, очередь) в чужой счёт не попадает. Пока ни одна
// горутина не считает, обёртка стоит одно атомарное чтение.
//
// Время у списков занижено: Query и QueryRow меряются до первой строки
// (у modernc первый шаг идёт в самом запросе), а чтение остальных строк в
// rows.Next — уже нет. Число запросов верное; время «база» у экрана со
// списком в сотни строк — нижняя граница, а не итог.

import (
	"bytes"
	"database/sql"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SQLStats — запросы к базе одной горутины: сколько и сколько времени.
type SQLStats struct {
	N int
	D time.Duration
}

// SQLSlow — самый долгий запрос счёта (perf-pass 9.4). 5 октября 2026 корень
// «Настроек» тратил 90–120 мс в базе на 129 запросах, и какой из них дорогой,
// по коду было не понять: число запросов говорит «сколько», а не «какой».
// Здесь шаблон запроса — со знаками «?» вместо значений, их тут нет — и кто
// его позвал: метод хранилища и первый вызов снаружи.
type SQLSlow struct {
	D      time.Duration
	Query  string // шаблон, пробелы схлопнуты, не длиннее slowQueryRunes
	Caller string // «store.(*Store).CountMessages ← api.(*Server).connections»
}

const slowQueryRunes = 200

// SQLTrace — включённый счёт одной горутины.
type SQLTrace struct {
	id   uint64
	prev *SQLTrace // счёт, который был на горутине до этого (вложенный замер)
	n    atomic.Int64
	d    atomic.Int64
	// Keep — помнить и тексты запросов (только для приборов в тестах):
	// по числу не видно, какой запрос повторяется на каждый чат
	Keep    bool
	keptMu  sync.Mutex
	queries []string
	// самый долгий запрос: время читается без замка, запись — под ним
	slowD  atomic.Int64
	slowMu sync.Mutex
	slow   SQLSlow
}

// Slowest — самый долгий запрос счёта; пусто, если запросов не было.
func (t *SQLTrace) Slowest() SQLSlow {
	if t == nil {
		return SQLSlow{}
	}
	t.slowMu.Lock()
	defer t.slowMu.Unlock()
	return t.slow
}

// Queries — тексты запросов, если счёт их помнил (Keep).
func (t *SQLTrace) Queries() []string {
	t.keptMu.Lock()
	defer t.keptMu.Unlock()
	return append([]string(nil), t.queries...)
}

var (
	sqlTraced  atomic.Int32 // сколько горутин сейчас считают
	sqlTracers sync.Map     // номер горутины → *SQLTrace
)

// goroutineID — номер текущей горутины из первой строки её стека
// («goroutine 123 [running]:»). Около микросекунды; зовётся только пока
// кто-то считает.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	b := bytes.TrimPrefix(buf[:n], []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		b = b[:i]
	}
	id, _ := strconv.ParseUint(string(b), 10, 64)
	return id
}

// TraceSQL включает счёт запросов текущей горутины. Stop обязателен —
// иначе номер горутины остался бы в таблице после её конца. Вложенный
// замер на той же горутине встаёт поверх внешнего и на Stop отдаёт ему
// свой счёт: раньше он перезаписывал внешний, и после двух Stop счётчик
// считающих горутин оставался больше нуля навсегда.
func TraceSQL() *SQLTrace {
	t := &SQLTrace{id: goroutineID()}
	if v, ok := sqlTracers.Load(t.id); ok {
		t.prev = v.(*SQLTrace)
	} else {
		sqlTraced.Add(1)
	}
	sqlTracers.Store(t.id, t)
	return t
}

// Stats — счёт на сейчас.
func (t *SQLTrace) Stats() SQLStats {
	if t == nil {
		return SQLStats{}
	}
	return SQLStats{N: int(t.n.Load()), D: time.Duration(t.d.Load())}
}

// Stop выключает счёт; у вложенного — возвращает горутину внешнему.
func (t *SQLTrace) Stop() {
	if t == nil {
		return
	}
	if t.prev != nil {
		if sqlTracers.CompareAndSwap(t.id, t, t.prev) {
			t.prev.n.Add(t.n.Load())
			t.prev.d.Add(t.d.Load())
			if s := t.Slowest(); s.D > 0 {
				t.prev.noteSlow(s)
			}
		}
		return
	}
	if sqlTracers.CompareAndDelete(t.id, t) {
		sqlTraced.Add(-1)
	}
}

// traceOf — счёт текущей горутины, если она считает.
func traceOf() *SQLTrace {
	if sqlTraced.Load() == 0 {
		return nil
	}
	if v, ok := sqlTracers.Load(goroutineID()); ok {
		return v.(*SQLTrace)
	}
	return nil
}

func (t *SQLTrace) add(since time.Time, query string) {
	if t != nil {
		d := time.Since(since)
		t.n.Add(1)
		t.d.Add(int64(d))
		if int64(d) > t.slowD.Load() {
			t.noteSlow(SQLSlow{D: d, Query: clipQuery(query), Caller: sqlCaller()})
		}
		if t.Keep {
			t.keptMu.Lock()
			t.queries = append(t.queries, strings.Join(strings.Fields(query), " "))
			t.keptMu.Unlock()
		}
	}
}

// countedDB — база с тем же набором методов: Exec, Query и QueryRow
// попадают в счёт, остальное (Begin, Close, настройки пула) — как было.
// Запросы внутри транзакции не считаются: их немного, и все — в записи.
type countedDB struct{ *sql.DB }

func (c countedDB) Exec(query string, args ...any) (sql.Result, error) {
	t, t0 := traceOf(), time.Now()
	r, err := c.DB.Exec(query, args...)
	t.add(t0, query)
	return r, err
}

func (c countedDB) Query(query string, args ...any) (*sql.Rows, error) {
	t, t0 := traceOf(), time.Now()
	r, err := c.DB.Query(query, args...)
	t.add(t0, query)
	return r, err
}

func (c countedDB) QueryRow(query string, args ...any) *sql.Row {
	t, t0 := traceOf(), time.Now()
	r := c.DB.QueryRow(query, args...)
	t.add(t0, query)
	return r
}

// noteSlow — запомнить запрос, если он дольше прежнего самого долгого.
func (t *SQLTrace) noteSlow(s SQLSlow) {
	t.slowMu.Lock()
	defer t.slowMu.Unlock()
	if s.D > t.slow.D {
		t.slow = s
		t.slowD.Store(int64(s.D))
	}
}

func clipQuery(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > slowQueryRunes {
		q = string(r[:slowQueryRunes]) + "…"
	}
	return q
}

// sqlCaller — кто позвал запрос: метод хранилища, через который в него
// вошли (а не внутренний помощник вроде usageBy), и два вызова снаружи
// (обычно движок и экран). Обёртка над базой и database/sql пропускаются.
// Зовётся только когда запрос оказался самым долгим в счёте — то есть редко.
func sqlCaller() string {
	var pcs [32]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var chain []string
	entry := "" // последний кадр хранилища перед выходом из пакета
	for len(chain) < 3 {
		f, more := frames.Next()
		name := f.Function
		switch {
		case name == "" || strings.HasPrefix(name, "database/sql") || strings.Contains(name, "countedDB"):
		case strings.Contains(name, "/internal/store."):
			if len(chain) == 0 {
				entry = shortFunc(name)
			}
		default:
			if len(chain) == 0 && entry != "" {
				chain = append(chain, entry)
			}
			chain = append(chain, shortFunc(name))
		}
		if !more {
			break
		}
	}
	if len(chain) == 0 && entry != "" {
		chain = append(chain, entry)
	}
	return strings.Join(chain, " ← ")
}

// shortFunc — «github.com/…/internal/store.(*Store).X» → «store.(*Store).X».
func shortFunc(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}
