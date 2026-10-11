package store

// Находки самонаблюдения: что Сёма заметил у себя самого.
//
// Отдельно от уведомлений нарочно. Уведомление — это дело, которое ждёт
// человека, и оно закрывается, когда человек его сделал. Находка — состояние
// Сёмы: она держится, пока держится сама беда, и её надо уметь показать
// списком «что проверено и что найдено», даже когда человек ничего делать не
// собирается.
//
// Ключ повторяет устройство уведомлений: та же находка приходит снова и
// заменяет прежнюю открытую, а не ложится второй строкой. Иначе за неделю
// экран превратится в ленту одинаковых записей и человек перестанет её
// читать — ровно так 120 одинаковых обрывов MAX за тринадцать часов не
// заметил никто.

import (
	"strings"
	"time"
)

// Уровни находки. «Ок» здесь нет: находка — это то, что не так.
const (
	FindingAlert = "alert" // работа остановлена
	FindingWarn  = "warn"  // работа идёт, но так быть не должно
)

// Что сделал наблюдатель, найдя беду.
const (
	MeasureAlert     = "тревога"       // только сказал
	MeasurePauseRef  = "пауза объекту" // объект больше не ставится в работу
	MeasurePauseKind = "пауза виду"    // остановлен вид работы целиком
)

// Finding — одна беда, замеченная наблюдателем.
type Finding struct {
	ID       int64
	Key      string // вид проверки плюс объект: та же беда заменяет прежнюю
	Kind     string // какая из проверок нашла
	Ref      string // к чему относится: файл, задача, хранилище
	Level    string
	Summary  string // одной строкой: что именно увидено
	Numbers  string // чем измерено: «1187 работ за 2 дня, 9,2 млн токенов»
	Measure  string
	Screen   string // экран, где это чинят
	Decision string // что решил человек; пусто — ещё не решал
	Open     bool
	First    time.Time // когда увидено впервые
	Last     time.Time
}

func (s *Store) migrateFindings() {
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS self_findings (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		key      TEXT NOT NULL,
		kind     TEXT NOT NULL,
		object   TEXT NOT NULL DEFAULT '',
		level    TEXT NOT NULL DEFAULT 'warn',
		what     TEXT NOT NULL DEFAULT '',
		numbers  TEXT NOT NULL DEFAULT '',
		measure  TEXT NOT NULL DEFAULT '',
		goto_    TEXT NOT NULL DEFAULT '',
		decision TEXT NOT NULL DEFAULT '',
		open     INTEGER NOT NULL DEFAULT 1,
		first_at TEXT NOT NULL,
		last_at  TEXT NOT NULL
	)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_findings_open ON self_findings(open, level, last_at DESC)`)
	_, _ = s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_findings_key ON self_findings(key) WHERE open = 1`)
}

// SaveFinding — увидели беду. Та же беда по ключу обновляет открытую
// запись и двигает «последнюю», а первую не трогает: важно, сколько времени
// это тянется, а не когда сказали в последний раз.
//
// Возвращает true, если находка новая: только о новой стоит будить человека.
func (s *Store) SaveFinding(finding Finding) (bool, error) {
	if strings.TrimSpace(finding.Key) == "" {
		return false, nil
	}
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`UPDATE self_findings
		SET level = ?, what = ?, numbers = ?, measure = ?, goto_ = ?, last_at = ?
		WHERE key = ? AND open = 1`,
		finding.Level, finding.Summary, finding.Numbers, finding.Measure, finding.Screen, now, finding.Key)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil
	}
	_, err = s.db.Exec(`INSERT INTO self_findings
		(key, kind, object, level, what, numbers, measure, goto_, open, first_at, last_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		finding.Key, finding.Kind, finding.Ref, finding.Level, finding.Summary, finding.Numbers, finding.Measure, finding.Screen, now, now)
	return err == nil, err
}

// CloseFinding — беда прошла или человек решил. Запись остаётся: по ней
// видно, что это уже случалось, и сколько раз.
func (s *Store) CloseFinding(key, decision string) error {
	_, err := s.db.Exec(`UPDATE self_findings SET open = 0, decision = ?, last_at = ?
		WHERE key = ? AND open = 1`, decision, time.Now().Format(time.RFC3339), key)
	return err
}

// Findings — что видно на экране. Открытые сверху, свежие раньше.
func (s *Store) Findings(openOnly bool, limit int) ([]*Finding, error) {
	if limit <= 0 {
		limit = 50
	}
	where := ""
	if openOnly {
		where = " WHERE open = 1"
	}
	rows, err := s.db.Query(`SELECT id, key, kind, object, level, what, numbers, measure,
		goto_, decision, open, first_at, last_at FROM self_findings`+where+
		` ORDER BY open DESC, last_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Finding
	for rows.Next() {
		finding := &Finding{}
		var openFlag int
		var first, last string
		if err := rows.Scan(&finding.ID, &finding.Key, &finding.Kind, &finding.Ref, &finding.Level, &finding.Summary,
			&finding.Numbers, &finding.Measure, &finding.Screen, &finding.Decision, &openFlag, &first, &last); err != nil {
			return nil, err
		}
		finding.Open = openFlag == 1
		finding.First, _ = time.Parse(time.RFC3339, first)
		finding.Last, _ = time.Parse(time.RFC3339, last)
		out = append(out, finding)
	}
	return out, rows.Err()
}

// OpenFindings — сколько сейчас и какая самая тяжёлая. Нужно «Здоровью»:
// уровни в норме при открытой находке не дают зелёного состояния.
func (s *Store) OpenFindings() (n int, level string) {
	rows, err := s.db.Query(`SELECT level, COUNT(*) FROM self_findings WHERE open = 1 GROUP BY level`)
	if err != nil {
		return 0, ""
	}
	defer rows.Close()
	for rows.Next() {
		var l string
		var c int
		if rows.Scan(&l, &c) != nil {
			continue
		}
		n += c
		if l == FindingAlert || level == "" {
			level = l
		}
	}
	return n, level
}

// RemoveOldFindings — закрытые старше срока. Открытые не трогаем никогда:
// беда, которая тянется полгода, тем и важна, что тянется полгода.
func (s *Store) RemoveOldFindings(days int) int64 {
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM self_findings WHERE open = 0 AND last_at < ?`, cutoff)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}
