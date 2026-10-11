package store

// Журнал расхода: каждый вызов модели и каждый запуск кода — строка с
// потребителем, моделью, токенами, секундами и результатом. Снимки процесса —
// отдельная таблица. Пороги и хранение — настройки, не константы.

import (
	"fmt"
	"strings"
	"time"
)

type UsageRow struct {
	ID        int64
	At        time.Time
	Consumer  string // core:poll | module:receipts | script:12 | chat:47 | mcp:claude-code
	Kind      string // llm | run
	Model     string
	Role      string
	TokensIn  int
	TokensOut int
	// Cached — сколько входа пришло из кэша провайдера. Держим отдельно:
	// по нему видно, окупается ли длинный неизменный кусок промпта, и
	// без него счёт денег завышен там, где кэш работает
	Cached    int
	Estimated bool
	Seconds   float64
	OK        bool
	Note      string
	Cost      float64 // в валюте модели; 0 — бесплатно или неизвестно
	Currency  string
	// Retry — неудачу переделает очередь: это повтор, а не отказ.
	Retry bool
}

type UsageSum struct {
	Key       string // потребитель или модель
	Calls     int
	Fails     int
	TokensIn  int
	TokensOut int
	// Cached — сколько входа пришло из кэша провайдера. Держим отдельно:
	// по нему видно, окупается ли длинный неизменный кусок промпта, и
	// без него счёт денег завышен там, где кэш работает
	Cached    int
	Estimated bool // хоть одна строка — оценка
	Seconds   float64
	Cost      float64
	Currency  string
	LastAt    time.Time
}

type ProcessStat struct {
	At         time.Time
	RSS        int64
	CPUPct     float64
	Goroutines int
	DBBytes    int64
	WALBytes   int64
	FilesBytes int64
	DiskFree   int64
	Uptime     int64
}

func (s *Store) migrateUsage() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS usage (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			at         TEXT NOT NULL,
			consumer   TEXT NOT NULL,
			kind       TEXT NOT NULL,
			model      TEXT NOT NULL DEFAULT '',
			role       TEXT NOT NULL DEFAULT '',
			tokens_in  INTEGER NOT NULL DEFAULT 0,
			tokens_out INTEGER NOT NULL DEFAULT 0,
			estimated  INTEGER NOT NULL DEFAULT 0,
			seconds    REAL NOT NULL DEFAULT 0,
			ok         INTEGER NOT NULL DEFAULT 1,
			note       TEXT NOT NULL DEFAULT '',
			cost       REAL NOT NULL DEFAULT 0,
			currency   TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_at ON usage(at)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_consumer ON usage(consumer, at)`,
		`CREATE TABLE IF NOT EXISTS process_stats (
			at          TEXT PRIMARY KEY,
			rss         INTEGER NOT NULL DEFAULT 0,
			cpu_pct     REAL NOT NULL DEFAULT 0,
			goroutines  INTEGER NOT NULL DEFAULT 0,
			db_bytes    INTEGER NOT NULL DEFAULT 0,
			wal_bytes   INTEGER NOT NULL DEFAULT 0,
			files_bytes INTEGER NOT NULL DEFAULT 0,
			disk_free   INTEGER NOT NULL DEFAULT 0,
			uptime      INTEGER NOT NULL DEFAULT 0
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("журнал расхода: %w", err)
		}
	}
	// повтор против отказа: колонка появилась позже
	_, _ = s.db.Exec(`ALTER TABLE usage ADD COLUMN retry INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE usage ADD COLUMN cached INTEGER NOT NULL DEFAULT 0`)
	return nil
}

func (s *Store) AddUsage(r *UsageRow) error {
	if r.At.IsZero() {
		r.At = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO usage (at, consumer, kind, model, role, tokens_in, tokens_out, estimated, seconds, ok, note, cost, currency, retry, cached)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.At.Format(time.RFC3339), r.Consumer, r.Kind, r.Model, r.Role, r.TokensIn, r.TokensOut, b2i(r.Estimated), r.Seconds, b2i(r.OK), r.Note, r.Cost, r.Currency, b2i(r.Retry), r.Cached)
	return err
}

// UsageBy — сводка за период по потребителю ("consumer") или модели ("model").
// UsageBy — расход по потребителям или моделям с момента since.
//
// Сводка дорогая: на живой Сёме за сутки в журнал ложится около 30 тысяч
// вызовов, и проход по ним стоил на Pi 70–160 мс — на «Задачах» и «Здоровье»
// при каждом открытии. Кэш был, но с ключом «миг начала окна с точностью до
// секунды» почти не попадал (5 октября 2026).
//
// Теперь ответ ищется по ближайшему началу окна в пределах usageStale: у
// окна «за сутки» начало сдвигается на секунды между запросами, у «с
// полуночи» оно одно и то же — оба находят свою запись. Ошибка от близкого
// начала — вызовы на краю окна за эти минуты, на сводке за сутки не видны;
// окна разной длины («сутки», «неделя», «с полуночи») расходятся на часы и не
// путаются. Свежим ответ считается usageFresh; старше — экран получает
// прежние цифры сразу, а пересчёт с началом из этого запроса идёт в фоне,
// один на запись; дальше usageStale — считается на месте.
func (s *Store) UsageBy(field string, since time.Time, kind string) ([]*UsageSum, error) {
	key := field + "|" + kind
	s.usageMu.Lock()
	i := s.nearUsage(key, since)
	if i >= 0 {
		cached := s.usageCache[key][i]
		age := time.Since(cached.at)
		if age < usageFresh {
			s.usageMu.Unlock()
			return cached.sums, nil
		}
		if age < usageStale {
			if !cached.refreshing {
				s.usageCache[key][i].refreshing = true
				go s.refreshUsage(key, cached.since, field, since, kind)
			}
			s.usageMu.Unlock()
			return cached.sums, nil
		}
	}
	s.usageMu.Unlock()
	sums, err := s.usageBy(field, since, kind)
	if err != nil {
		return nil, err
	}
	s.keepUsage(key, since, since, sums)
	return sums, nil
}

const (
	usageFresh = 15 * time.Second
	usageStale = 5 * time.Minute
)

// nearUsage — запись с ближайшим к since началом окна, не дальше usageStale;
// -1 — такой нет. Под usageMu.
func (s *Store) nearUsage(key string, since time.Time) int {
	best, bestD := -1, usageStale+time.Nanosecond
	for i, c := range s.usageCache[key] {
		d := c.since.Sub(since)
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// refreshUsage — пересчёт в фоне с началом из того запроса, что его позвал.
// Не вышло (база закрыта, занята) — прежние цифры остаются, следующий запрос
// попробует снова.
func (s *Store) refreshUsage(key string, was time.Time, field string, since time.Time, kind string) {
	sums, err := s.usageBy(field, since, kind)
	if err != nil {
		s.usageMu.Lock()
		for i := range s.usageCache[key] {
			if s.usageCache[key][i].since.Equal(was) {
				s.usageCache[key][i].refreshing = false
			}
		}
		s.usageMu.Unlock()
		return
	}
	s.keepUsage(key, was, since, sums)
}

// keepUsage — запомнить ответ: запись с началом was заменяется (у фонового
// пересчёта начало сдвинулось), давно не спрошенные уходят — память не растёт.
func (s *Store) keepUsage(key string, was, since time.Time, sums []*UsageSum) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usageCache == nil {
		s.usageCache = map[string][]usageCacheEntry{}
	}
	var kept []usageCacheEntry
	for _, c := range s.usageCache[key] {
		if time.Since(c.at) <= usageStale && !c.since.Equal(was) && !c.since.Equal(since) {
			kept = append(kept, c)
		}
	}
	s.usageCache[key] = append(kept, usageCacheEntry{since: since, sums: sums, at: time.Now()})
}

type usageCacheEntry struct {
	since      time.Time // начало окна, для которого посчитано
	sums       []*UsageSum
	at         time.Time
	refreshing bool // пересчёт уже идёт в фоне
}

func (s *Store) usageBy(field string, since time.Time, kind string) ([]*UsageSum, error) {
	if field != "consumer" && field != "model" {
		return nil, fmt.Errorf("группировка %q", field)
	}
	q := `SELECT ` + field + `, COUNT(*), SUM(CASE WHEN ok = 0 THEN 1 ELSE 0 END), SUM(tokens_in), SUM(tokens_out),
		MAX(estimated), SUM(seconds), SUM(cost), MAX(currency), MAX(at), COALESCE(SUM(cached), 0) FROM usage WHERE at >= ?`
	args := []any{since.Format(time.RFC3339)}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if field == "model" {
		q += ` AND model <> ''`
	}
	// «GROUP BY +consumer», а не «GROUP BY consumer»: одинарный плюс ничего
	// не меняет в значении, но снимает у SQLite соблазн взять индекс по
	// потребителю ради готового порядка групп. Соблазн дорогой: при «GROUP BY
	// consumer» план был «SCAN usage USING INDEX idx_usage_consumer» — проход
	// по всему журналу, все 150 тыс. строк, сколько бы узким ни было окно.
	// С плюсом берётся idx_usage_at и читаются только строки за срок:
	// 13,3 мс против 0,86 мс на живом объёме. Порядок групп всё равно
	// строится временным деревом — он задан ORDER BY по сумме токенов.
	q += ` GROUP BY +` + field + ` ORDER BY SUM(tokens_in) + SUM(tokens_out) DESC, COUNT(*) DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UsageSum
	for rows.Next() {
		u := &UsageSum{}
		var est int
		var last string
		if err := rows.Scan(&u.Key, &u.Calls, &u.Fails, &u.TokensIn, &u.TokensOut, &est, &u.Seconds, &u.Cost, &u.Currency, &last, &u.Cached); err != nil {
			return nil, err
		}
		u.Estimated = est == 1
		u.LastAt = parseTime(last)
		out = append(out, u)
	}
	return out, rows.Err()
}

// sqlUsageFor — сводка потребителя и всех, кто под ним, двумя поисками по
// idx_usage_consumer(consumer, at).
//
// Было «WHERE at >= ? AND (consumer = ? OR consumer LIKE ?)»: индекс по
// потребителю SQLite при таком условии не берёт вовсе и читает все строки
// за срок — на живой базе это 24 тыс. строк и 7 мс на каждую капсулу
// расхода. Приставка ищется диапазоном: ':' — 0x3A, ';' — 0x3B, значит
// [«вид:», «вид;») это ровно те имена, что начинаются с «вид:», ни одного
// лишнего. Разница с LIKE одна: LIKE в SQLite не различает регистр латиницы,
// а диапазон различает — виды потребителей пишет код («module», «script»,
// «chat», «core», «task»), и все они в нижнем регистре.
const sqlUsageFor = `SELECT COALESCE(SUM(cnt),0), COALESCE(SUM(fails),0), COALESCE(SUM(tin),0), COALESCE(SUM(tout),0),
		COALESCE(MAX(est),0), COALESCE(SUM(secs),0), COALESCE(SUM(money),0), COALESCE(MAX(cur),''), COALESCE(MAX(lastat),'')
	FROM (
		SELECT COUNT(*) cnt, SUM(CASE WHEN ok = 0 THEN 1 ELSE 0 END) fails, SUM(tokens_in) tin, SUM(tokens_out) tout,
			MAX(estimated) est, SUM(seconds) secs, SUM(cost) money, MAX(currency) cur, MAX(at) lastat
			FROM usage WHERE consumer = ? AND at >= ?
		UNION ALL
		SELECT COUNT(*), SUM(CASE WHEN ok = 0 THEN 1 ELSE 0 END), SUM(tokens_in), SUM(tokens_out),
			MAX(estimated), SUM(seconds), SUM(cost), MAX(currency), MAX(at)
			FROM usage WHERE consumer >= ? AND consumer < ? AND at >= ?
	)`

// UsageFor — сводка одного потребителя (или всех с таким префиксом) за период.
func (s *Store) UsageFor(consumerPrefix string, since time.Time) *UsageSum {
	u := &UsageSum{Key: consumerPrefix}
	var est int
	var last string
	from := since.Format(time.RFC3339)
	_ = s.db.QueryRow(sqlUsageFor, consumerPrefix, from, consumerPrefix+":", consumerPrefix+";", from).
		Scan(&u.Calls, &u.Fails, &u.TokensIn, &u.TokensOut, &est, &u.Seconds, &u.Cost, &u.Currency, &last)
	u.Estimated = est == 1
	u.LastAt = parseTime(last)
	return u
}

// UsageProblems — отказы и медленные вызовы за период, новые сверху.
func (s *Store) UsageProblems(since time.Time, slowSeconds float64, limit int) ([]*UsageRow, error) {
	rows, err := s.db.Query(`SELECT id, at, consumer, kind, model, role, tokens_in, tokens_out, estimated, seconds, ok, note, cost, currency
		FROM usage WHERE at >= ? AND (ok = 0 OR seconds >= ?) ORDER BY id DESC LIMIT ?`,
		since.Format(time.RFC3339), slowSeconds, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UsageRow
	for rows.Next() {
		r := &UsageRow{}
		var at string
		var est, ok int
		if err := rows.Scan(&r.ID, &at, &r.Consumer, &r.Kind, &r.Model, &r.Role, &r.TokensIn, &r.TokensOut, &est, &r.Seconds, &ok, &r.Note, &r.Cost, &r.Currency); err != nil {
			return nil, err
		}
		r.At, r.Estimated, r.OK = parseTime(at), est == 1, ok == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageOfConsumer — история вызовов одного потребителя: скрипта, модуля,
// задачи. «Последний запуск» отвечает, что было сейчас, а история — что
// было до этого: скрипт, который перестал работать месяц назад, по одному
// последнему запуску выглядит живым.
func (s *Store) UsageOfConsumer(consumer string, limit int) ([]*UsageRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, at, consumer, kind, model, role, tokens_in, tokens_out, estimated, seconds, ok, note, cost, currency
		FROM usage WHERE consumer = ? ORDER BY id DESC LIMIT ?`, consumer, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UsageRow
	for rows.Next() {
		r := &UsageRow{}
		var at string
		var est, ok int
		if err := rows.Scan(&r.ID, &at, &r.Consumer, &r.Kind, &r.Model, &r.Role, &r.TokensIn, &r.TokensOut, &est, &r.Seconds, &ok, &r.Note, &r.Cost, &r.Currency); err != nil {
			return nil, err
		}
		r.At, r.Estimated, r.OK = parseTime(at), est == 1, ok == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// ModelFailRate — доля отказов модели за период (для порогов).
// ModelCallStat — вызовы за период: сколько всего, сколько повторов (их
// переделает очередь) и сколько настоящих отказов.
func (s *Store) ModelCallStat(since time.Time) (calls, retries, fails int) {
	_ = s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(ok = 0 AND retry = 1), 0),
		COALESCE(SUM(ok = 0 AND retry = 0), 0)
		FROM usage WHERE kind = 'llm' AND at >= ?`, since.Format(time.RFC3339)).Scan(&calls, &retries, &fails)
	return calls, retries, fails
}

func (s *Store) ModelFailRate(since time.Time) (calls, fails int) {
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN ok = 0 THEN 1 ELSE 0 END),0) FROM usage WHERE kind = 'llm' AND at >= ?`,
		since.Format(time.RFC3339)).Scan(&calls, &fails)
	return
}

// PruneUsage удаляет строки старше срока хранения. Сроков два, и это не
// прихоть: строки «run» — отметки о каждом проходе фонового воркера, их за
// сутки набегает под двадцать тысяч, а строк «llm» — полторы тысячи. Держать
// их одинаково долго значит хранить триста мегабайт отметок ради журнала
// вызовов модели. Записи «llm» трогает только первый срок: по ним считаются
// деньги, и потерять их нельзя.
func (s *Store) PruneUsage(keep, keepRuns time.Duration) {
	now := time.Now()
	_, _ = s.db.Exec(`DELETE FROM usage WHERE kind <> 'run' AND at < ?`, now.Add(-keep).Format(time.RFC3339))
	if keepRuns <= 0 {
		keepRuns = keep
	}
	_, _ = s.db.Exec(`DELETE FROM usage WHERE kind = 'run' AND at < ?`, now.Add(-keepRuns).Format(time.RFC3339))
}

// ---- процесс ----

func (s *Store) AddProcessStat(p *ProcessStat) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO process_stats (at, rss, cpu_pct, goroutines, db_bytes, wal_bytes, files_bytes, disk_free, uptime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.At.Format(time.RFC3339), p.RSS, p.CPUPct, p.Goroutines, p.DBBytes, p.WALBytes, p.FilesBytes, p.DiskFree, p.Uptime)
	return err
}

func (s *Store) ProcessStats(since time.Time) ([]*ProcessStat, error) {
	rows, err := s.db.Query(`SELECT at, rss, cpu_pct, goroutines, db_bytes, wal_bytes, files_bytes, disk_free, uptime FROM process_stats WHERE at >= ? ORDER BY at`,
		since.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProcessStat
	for rows.Next() {
		p := &ProcessStat{}
		var at string
		if err := rows.Scan(&at, &p.RSS, &p.CPUPct, &p.Goroutines, &p.DBBytes, &p.WALBytes, &p.FilesBytes, &p.DiskFree, &p.Uptime); err != nil {
			return nil, err
		}
		p.At = parseTime(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// RollupProcessStats: старше суток — только снимки на начало часа; старше месяца — ничего.
func (s *Store) RollupProcessStats() {
	dayAgo := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	_, _ = s.db.Exec(`DELETE FROM process_stats WHERE at < ? AND substr(at, 15, 2) <> '00'`, dayAgo)
	_, _ = s.db.Exec(`DELETE FROM process_stats WHERE at < ?`, time.Now().Add(-30*24*time.Hour).Format(time.RFC3339))
}

var _ = strings.TrimSpace

// Checkpoint сбрасывает WAL в базу: у долгоживущего соединения журнал иначе
// растёт до размера самой базы.
func (s *Store) Checkpoint() {
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
}

// ---- бюджеты ----

type Budget struct {
	ID       int64
	Consumer string // точное имя (module:receipts) или вид (module, script, chat, core)
	Kind     string // tokens | money
	Period   string // day | week | month
	Limit    float64
	Action   string // warn | pause
	Created  time.Time
}

func (s *Store) migrateBudgets() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS usage_budgets (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		consumer TEXT NOT NULL,
		kind     TEXT NOT NULL,
		period   TEXT NOT NULL,
		lim      REAL NOT NULL,
		action   TEXT NOT NULL DEFAULT 'warn',
		created  TEXT NOT NULL
	)`)
	return err
}

func (s *Store) Budgets() ([]*Budget, error) {
	rows, err := s.db.Query(`SELECT id, consumer, kind, period, lim, action, created FROM usage_budgets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Budget
	for rows.Next() {
		b := &Budget{}
		var created string
		if err := rows.Scan(&b.ID, &b.Consumer, &b.Kind, &b.Period, &b.Limit, &b.Action, &created); err != nil {
			return nil, err
		}
		b.Created = parseTime(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) AddBudget(b *Budget) error {
	_, err := s.db.Exec(`INSERT INTO usage_budgets (consumer, kind, period, lim, action, created) VALUES (?, ?, ?, ?, ?, ?)`,
		b.Consumer, b.Kind, b.Period, b.Limit, b.Action, time.Now().Format(time.RFC3339))
	return err
}

func (s *Store) SetBudgetLimit(id int64, limit float64) error {
	_, err := s.db.Exec(`UPDATE usage_budgets SET lim = ? WHERE id = ?`, limit, id)
	return err
}

func (s *Store) DeleteBudget(id int64) error {
	_, err := s.db.Exec(`DELETE FROM usage_budgets WHERE id = ?`, id)
	return err
}

// UsageConsumers — все потребители, встречавшиеся в журнале (для формы бюджета).
// UsageConsumers — все потребители, когда-либо бывшие в журнале.
//
// Не DISTINCT: он читает покрывающий индекс целиком — 214 тысяч строк на
// живой базе ради трёх десятков имён, пятая часть времени сборки
// «Здоровья». Прыжками по индексу: следующий потребитель за нынешним —
// один поиск, а не скан.
func (s *Store) UsageConsumers() []string {
	var out []string
	last := ""
	for {
		var name string
		err := s.db.QueryRow(`SELECT consumer FROM usage WHERE consumer > ? ORDER BY consumer LIMIT 1`, last).Scan(&name)
		if err != nil {
			return out
		}
		out = append(out, name)
		last = name
	}
}

func (s *Store) usageConsumersScan() []string {
	rows, err := s.db.Query(`SELECT DISTINCT consumer FROM usage ORDER BY consumer`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// PriceFor — сколько денег потратил потребитель за отрезок времени.
//
// Наблюдателю нужна цена петли: повтор, не стоивший денег, — беда, но не
// такая, чтобы рисковать остановкой настоящей работы; повтор, тративший
// токены, стоил 6,5 $ за двое суток.
func (s *Store) PriceFor(consumer string, since, until time.Time) float64 {
	var price float64
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(cost), 0) FROM usage
		WHERE consumer = ? AND at >= ? AND at <= ?`,
		consumer, since.Format(time.RFC3339), until.Add(time.Minute).Format(time.RFC3339)).Scan(&price)
	return price
}
