package store

// Данные модулей: коллекции документов JSON, изолированные по модулю, и
// очередь событий, на которые модули просыпаются. Поиск и сумма считаются
// в базе через json_extract — в песочницу документы не выгружаются без
// нужды.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ModuleDoc struct {
	ID      string
	Doc     map[string]any
	Created string
	Updated string
}

// DocQuery — что искать: равенство полей, период по полю даты, порядок, предел.
type DocQuery struct {
	Where     map[string]any
	DateField string // поле с датой RFC3339 для since/until (по умолчанию created)
	Since     string
	Until     string
	OrderBy   string
	Desc      bool
	Limit     int
}

type ModuleEvent struct {
	ID       int64
	Module   string
	Kind     string // message | attachment
	RefID    int64
	Created  string
	Attempts int
}

func (s *Store) migrateModuleData() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS module_data (
			module     TEXT NOT NULL,
			collection TEXT NOT NULL,
			id         TEXT NOT NULL,
			doc        TEXT NOT NULL,
			created    TEXT NOT NULL,
			updated    TEXT NOT NULL,
			PRIMARY KEY (module, collection, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_module_data_coll ON module_data(module, collection, created DESC)`,
		`CREATE TABLE IF NOT EXISTS module_events (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			module   TEXT NOT NULL,
			kind     TEXT NOT NULL,
			ref_id   INTEGER NOT NULL,
			created  TEXT NOT NULL,
			done     TEXT NOT NULL DEFAULT '',
			error    TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_module_events_pending ON module_events(done, id)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("данные модулей: %w", err)
		}
	}
	return nil
}

// ---- коллекции ----

func (s *Store) PutModuleDoc(module, coll, id string, doc map[string]any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	_, err = s.db.Exec(`INSERT INTO module_data (module, collection, id, doc, created, updated) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(module, collection, id) DO UPDATE SET doc = excluded.doc, updated = excluded.updated`,
		module, coll, id, string(raw), now, now)
	return err
}

func (s *Store) GetModuleDoc(module, coll, id string) (*ModuleDoc, bool) {
	var raw string
	d := &ModuleDoc{ID: id}
	err := s.db.QueryRow(`SELECT doc, created, updated FROM module_data WHERE module = ? AND collection = ? AND id = ?`,
		module, coll, id).Scan(&raw, &d.Created, &d.Updated)
	if err != nil {
		return nil, false
	}
	_ = json.Unmarshal([]byte(raw), &d.Doc)
	return d, true
}

func (s *Store) DeleteModuleDoc(module, coll, id string) error {
	_, err := s.db.Exec(`DELETE FROM module_data WHERE module = ? AND collection = ? AND id = ?`, module, coll, id)
	return err
}

// DeleteModuleData стирает все коллекции модуля — только вместе с модулем.
func (s *Store) DeleteModuleData(module string) error {
	_, err := s.db.Exec(`DELETE FROM module_data WHERE module = ?`, module)
	return err
}

// docField — путь к полю документа для SQL; имя поля проверяется, чтобы не
// собирать SQL из чужих строк.
func docField(name string) (string, error) {
	for _, r := range name {
		if !(r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё') {
			return "", fmt.Errorf("недопустимое имя поля %q", name)
		}
	}
	return "json_extract(doc, '$." + name + "')", nil
}

func (s *Store) docWhere(module, coll string, q DocQuery) (string, []any, error) {
	where := `module = ? AND collection = ?`
	args := []any{module, coll}
	for k, v := range q.Where {
		f, err := docField(k)
		if err != nil {
			return "", nil, err
		}
		where += ` AND ` + f + ` = ?`
		args = append(args, v)
	}
	dateField := "created"
	if q.DateField != "" {
		f, err := docField(q.DateField)
		if err != nil {
			return "", nil, err
		}
		dateField = f
	}
	if q.Since != "" {
		where += ` AND ` + dateField + ` >= ?`
		args = append(args, q.Since)
	}
	if q.Until != "" {
		where += ` AND ` + dateField + ` <= ?`
		args = append(args, q.Until)
	}
	return where, args, nil
}

// FindModuleDocs — поиск по коллекции.
func (s *Store) FindModuleDocs(module, coll string, q DocQuery) ([]*ModuleDoc, error) {
	where, args, err := s.docWhere(module, coll, q)
	if err != nil {
		return nil, err
	}
	order := "created"
	if q.OrderBy != "" {
		if order, err = docField(q.OrderBy); err != nil {
			return nil, err
		}
	}
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, doc, created, updated FROM module_data WHERE `+where+` ORDER BY `+order+` `+dir+` LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ModuleDoc
	for rows.Next() {
		d := &ModuleDoc{}
		var raw string
		if err := rows.Scan(&d.ID, &raw, &d.Created, &d.Updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &d.Doc)
		out = append(out, d)
	}
	return out, rows.Err()
}

// SumModuleDocs — сумма числового поля по выборке, считается в базе.
func (s *Store) SumModuleDocs(module, coll, field string, q DocQuery) (float64, int, error) {
	where, args, err := s.docWhere(module, coll, q)
	if err != nil {
		return 0, 0, err
	}
	f, err := docField(field)
	if err != nil {
		return 0, 0, err
	}
	var sum float64
	var n int
	err = s.db.QueryRow(`SELECT COALESCE(SUM(`+f+`), 0), COUNT(*) FROM module_data WHERE `+where, args...).Scan(&sum, &n)
	return sum, n, err
}

// CountModuleDocs — сколько документов в коллекциях модуля, по коллекциям.
func (s *Store) CountModuleDocs(module string) map[string]int {
	out := map[string]int{}
	rows, err := s.db.Query(`SELECT collection, COUNT(*) FROM module_data WHERE module = ? GROUP BY collection`, module)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var n int
		if rows.Scan(&c, &n) == nil {
			out[c] = n
		}
	}
	return out
}

// ---- события ----

func (s *Store) AddModuleEvent(module, kind string, refID int64) error {
	_, err := s.db.Exec(`INSERT INTO module_events (module, kind, ref_id, created) VALUES (?, ?, ?, ?)`,
		module, kind, refID, time.Now().Format(time.RFC3339))
	return err
}

func (s *Store) PendingModuleEvents(limit int) ([]*ModuleEvent, error) {
	rows, err := s.db.Query(`SELECT id, module, kind, ref_id, created, attempts FROM module_events
		WHERE done = '' AND attempts < 3 ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ModuleEvent
	for rows.Next() {
		ev := &ModuleEvent{}
		if err := rows.Scan(&ev.ID, &ev.Module, &ev.Kind, &ev.RefID, &ev.Created, &ev.Attempts); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// FinishModuleEvent — событие обработано или упало; после третьей попытки
// оно остаётся в журнале с ошибкой и больше не пробуется.
func (s *Store) FinishModuleEvent(id int64, errText string) {
	if errText == "" {
		_, _ = s.db.Exec(`UPDATE module_events SET done = ?, error = '' WHERE id = ?`, time.Now().Format(time.RFC3339), id)
		return
	}
	_, _ = s.db.Exec(`UPDATE module_events SET attempts = attempts + 1, error = ?,
		done = CASE WHEN attempts + 1 >= 3 THEN ? ELSE '' END WHERE id = ?`, strings.TrimSpace(errText), time.Now().Format(time.RFC3339), id)
	_, _ = s.db.Exec(`DELETE FROM module_events WHERE done != '' AND id NOT IN (SELECT id FROM module_events WHERE done != '' ORDER BY id DESC LIMIT 1000)`)
}

// ModuleEventStats — сколько событий обработано и сколько упало у модуля.
func (s *Store) ModuleEventStats(module string) (done, failed, pending int) {
	_ = s.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN done != '' AND error = '' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN done != '' AND error != '' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN done = '' THEN 1 ELSE 0 END), 0)
		FROM module_events WHERE module = ?`, module).Scan(&done, &failed, &pending)
	return
}

func (s *Store) LastModuleEventError(module string) string {
	var e string
	_ = s.db.QueryRow(`SELECT error FROM module_events WHERE module = ? AND error != '' ORDER BY id DESC LIMIT 1`, module).Scan(&e)
	return e
}

// PendingEventAge — сколько событий ждёт и возраст самого старого.
func (s *Store) PendingEventAge() (int, time.Duration) {
	var n int
	var oldest string
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(created), '') FROM module_events WHERE done = '' AND attempts < 3`).Scan(&n, &oldest)
	if n == 0 || oldest == "" {
		return 0, 0
	}
	t, err := time.Parse(time.RFC3339, oldest)
	if err != nil {
		return n, 0
	}
	return n, time.Since(t)
}
