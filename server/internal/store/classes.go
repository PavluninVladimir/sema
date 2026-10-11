package store

import (
	"strings"
	"time"
)

// Класс чувствительности у письма и файла. Хранится отдельной таблицей, а не
// колонкой: класс появляется у части записей, ставится и Сёмой, и человеком,
// и должен переживать переразбор.

const (
	ClassMsg  = "msg"
	ClassFile = "file"
)

// ClassMark — что и кем помечено.
type ClassMark struct {
	Kind  string
	ID    int64
	Class string
	By    string // пусто — Сёма по приметам, иначе имя человека
	At    string
	// Why — какая примета сработала: слово, номер с контрольной суммой,
	// машиночитаемая строка. Раньше хранился только итог, и на вопрос
	// «почему это секретное» ответить было нечем — приходилось
	// пересчитывать по коду. Причина нужна и человеку, который решает,
	// снимать ли пометку, и нам, когда примета врёт.
	Why string
}

func (s *Store) migrateClasses() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS content_class (
			kind  TEXT NOT NULL,
			id    INTEGER NOT NULL,
			class TEXT NOT NULL,
			by    TEXT NOT NULL DEFAULT '',
			at    TEXT NOT NULL,
			PRIMARY KEY (kind, id)
		)`,
		// согласие человека отдавать чувствительное наружу: по отправителю
		// или по одной записи. Отозвать можно тем же действием.
		`CREATE TABLE IF NOT EXISTS class_consent (
			scope   TEXT NOT NULL,
			key     TEXT NOT NULL,
			allowed INTEGER NOT NULL DEFAULT 0,
			by      TEXT NOT NULL DEFAULT '',
			at      TEXT NOT NULL,
			PRIMARY KEY (scope, key)
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// причина появилась позже самой таблицы: у старых строк её нет, и это
	// честно — мы правда не знаем, почему они такие
	_, _ = s.db.Exec(`ALTER TABLE content_class ADD COLUMN why TEXT NOT NULL DEFAULT ''`)
	return nil
}

func (s *Store) ContentClass(kind string, id int64) (string, bool) {
	var class string
	err := s.db.QueryRow(`SELECT class FROM content_class WHERE kind = ? AND id = ?`, kind, id).Scan(&class)
	return class, err == nil
}

// SetContentClass — записать класс. why — примета, по которой он такой;
// у пометки человека она пуста: человек и есть причина.
func (s *Store) SetContentClass(kind string, id int64, class, by, why string) error {
	_, err := s.db.Exec(`INSERT INTO content_class (kind, id, class, by, at, why) VALUES (?,?,?,?,?,?)
		ON CONFLICT(kind, id) DO UPDATE SET class = excluded.class, by = excluded.by, at = excluded.at, why = excluded.why`,
		kind, id, class, by, time.Now().Format(time.RFC3339), why)
	return err
}

// ClassMarkOf — класс и кто его поставил.
func (s *Store) ClassMarkOf(kind string, id int64) (ClassMark, bool) {
	m := ClassMark{Kind: kind, ID: id}
	err := s.db.QueryRow(`SELECT class, by, at, why FROM content_class WHERE kind = ? AND id = ?`, kind, id).
		Scan(&m.Class, &m.By, &m.At, &m.Why)
	return m, err == nil
}

func (s *Store) CountByClass(kind, class string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM content_class WHERE kind = ? AND class = ?`, kind, class).Scan(&n)
	return n
}

// Consent — разрешил ли человек отдавать наружу чувствительное этого
// отправителя (или эту запись). Второе значение — было ли решение вообще.
func (s *Store) Consent(scope, key string) (allowed bool, decided bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	var v int
	if err := s.db.QueryRow(`SELECT allowed FROM class_consent WHERE scope = ? AND key = ?`, scope, key).Scan(&v); err != nil {
		return false, false
	}
	return v == 1, true
}

func (s *Store) SetConsent(scope, key string, allow bool, by string) error {
	v := 0
	if allow {
		v = 1
	}
	_, err := s.db.Exec(`INSERT INTO class_consent (scope, key, allowed, by, at) VALUES (?,?,?,?,?)
		ON CONFLICT(scope, key) DO UPDATE SET allowed = excluded.allowed, by = excluded.by, at = excluded.at`,
		scope, strings.ToLower(strings.TrimSpace(key)), v, by, time.Now().Format(time.RFC3339))
	return err
}

// Consents — все решения человека: экран должен показывать, кому разрешено.
func (s *Store) Consents() ([]ClassMark, error) {
	rows, err := s.db.Query(`SELECT scope, key, allowed, by, at FROM class_consent ORDER BY at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClassMark
	for rows.Next() {
		var scope, key, by, at string
		var allowed int
		if err := rows.Scan(&scope, &key, &allowed, &by, &at); err != nil {
			return nil, err
		}
		class := "разрешено"
		if allowed == 0 {
			class = "запрещено"
		}
		out = append(out, ClassMark{Kind: scope, Class: class, By: key + " · " + by, At: at})
	}
	return out, rows.Err()
}
