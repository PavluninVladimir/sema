package store

// Память: одна таблица записей с видом, областью и происхождением, граф
// сущностей, потребители с выданными областями, правила и журнал. Ничего
// не разнесено по агентам: запись хранится один раз, а видят её те, кому
// выдана её область.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"time"
)

// Виды записей памяти.
const (
	MemObservation = "observation" // сырое событие сессии, живёт недолго
	MemFact        = "fact"        // утверждение с цепочкой версий
	MemLesson      = "lesson"      // факт, закрепившийся повторением
	MemCrystal     = "crystal"     // сводка сессии
)

// Режимы записи в область.
const (
	ConfirmAuto  = "auto"
	ConfirmAsk   = "ask"
	ConfirmNever = "never"
)

// Состояния записи.
const (
	MemPending = "pending" // ждёт подтверждения человека
	MemOK      = "ok"
)

type MemoryArea struct {
	Name     string
	Title    string
	Outbound bool   // можно отдавать облачной модели
	Confirm  string // auto | ask | never
	TTLDays  int    // сколько живут эпизоды (кристаллы и факты без подтверждения)
	Forget   bool   // «не запоминать»: записи в область отвергаются
	Created  string
}

// Потребитель памяти: чат Сёмы, модуль, внешний агент по MCP.
const (
	ConsumerChat   = "chat"
	ConsumerModule = "module"
	ConsumerMCP    = "mcp"
)

type MemoryConsumer struct {
	ID      int64
	Kind    string
	Name    string
	Path    string // рабочая папка внешнего агента
	Areas   []string
	Seen    string // последнее обращение
	Created string
}

func (c *MemoryConsumer) Has(area string) bool {
	for _, a := range c.Areas {
		if a == area {
			return true
		}
	}
	return false
}

// Label — как назвать потребителя человеку.
func (c *MemoryConsumer) Label() string {
	switch c.Kind {
	case ConsumerChat:
		return "Сёма-чат"
	case ConsumerModule:
		return "модуль «" + c.Name + "»"
	default:
		if c.Path != "" {
			return c.Name + " · " + c.Path
		}
		return c.Name
	}
}

type Memory struct {
	ID         int64
	Kind       string
	Area       string
	Text       string
	Status     string
	SessionID  int64
	Agent      string // как назвать источник человеку
	Model      string // какая модель извлекла (пусто — человек или код)
	Created    string
	Supersedes int64 // какую версию заменила
	Confirmed  bool  // подтверждено человеком
	Weight     int   // сколько раз повторилась
	Sources    []string
	Expires    string
	Graphed    bool // сущности уже извлечены
}

type MemorySession struct {
	ID         int64
	ConsumerID int64
	Agent      string
	Model      string
	Started    string
	Ended      string
	Summary    string
	Crystal    int64
}

type MemoryNode struct {
	ID   int64
	Type string
	Name string
	Area string
}

type MemoryEdge struct {
	ID       int64
	From     int64
	To       int64
	Relation string
	MemoryID int64
}

type MemoryLogEntry struct {
	ID        int64
	Consumer  string
	SessionID int64
	Area      string
	Action    string // recall | remember | deny | reject | observe
	Detail    string
	Granted   bool
	Tokens    int // сколько токенов ушло из памяти
	Saved     int // сколько токенов сбережено против повторного контекста
	At        string
}

func (s *Store) migrateMemory() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS memory_areas (
			name     TEXT PRIMARY KEY,
			title    TEXT NOT NULL,
			outbound INTEGER NOT NULL DEFAULT 0,
			confirm  TEXT NOT NULL DEFAULT 'auto',
			ttl_days INTEGER NOT NULL DEFAULT 90,
			forget   INTEGER NOT NULL DEFAULT 0,
			created  TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS memory_consumers (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			kind    TEXT NOT NULL,
			name    TEXT NOT NULL,
			path    TEXT NOT NULL DEFAULT '',
			areas   TEXT NOT NULL DEFAULT '[]',
			seen    TEXT NOT NULL DEFAULT '',
			created TEXT NOT NULL,
			UNIQUE(kind, name, path)
		)`,
		`CREATE TABLE IF NOT EXISTS memory_sessions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			consumer_id INTEGER NOT NULL,
			agent       TEXT NOT NULL,
			model       TEXT NOT NULL DEFAULT '',
			started     TEXT NOT NULL,
			ended       TEXT NOT NULL DEFAULT '',
			summary     TEXT NOT NULL DEFAULT '',
			crystal     INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS memories (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			kind       TEXT NOT NULL,
			area       TEXT NOT NULL,
			text       TEXT NOT NULL,
			status     TEXT NOT NULL DEFAULT 'ok',
			session_id INTEGER NOT NULL DEFAULT 0,
			agent      TEXT NOT NULL,
			model      TEXT NOT NULL DEFAULT '',
			created    TEXT NOT NULL,
			supersedes INTEGER NOT NULL DEFAULT 0,
			confirmed  INTEGER NOT NULL DEFAULT 0,
			weight     INTEGER NOT NULL DEFAULT 1,
			sources    TEXT NOT NULL DEFAULT '[]',
			expires    TEXT NOT NULL DEFAULT '',
			graphed    INTEGER NOT NULL DEFAULT 0,
			replaced   INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_area ON memories(area, status, replaced)`,
		// Координата: к какому месту в коде относится запись. Необязательна —
		// память живёт и без неё, а привязка нужна там, где знание про
		// конкретный файл или символ: грабли, уроки, требования
		`CREATE TABLE IF NOT EXISTS memory_places (
			memory_id INTEGER NOT NULL,
			repo      TEXT NOT NULL DEFAULT '',
			dir       TEXT NOT NULL DEFAULT '',
			file      TEXT NOT NULL DEFAULT '',
			symbol    TEXT NOT NULL DEFAULT '',
			line      INTEGER NOT NULL DEFAULT 0,
			added     TEXT NOT NULL,
			source    TEXT NOT NULL DEFAULT '',
			doubtful  INTEGER NOT NULL DEFAULT 0,
			checked   TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (memory_id, repo, dir, file, symbol)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_places_file ON memory_places(repo, file)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_places_symbol ON memory_places(symbol)`,
		// индекс по папке — только после migratePlaceDir: на старой базе
		// колонки dir ещё нет, и «CREATE INDEX» здесь ронял старт
		// (выкат 7 октября 2026, откат за минуту)
		`CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(text, tokenize = 'unicode61 remove_diacritics 2')`,
		`CREATE TABLE IF NOT EXISTS memory_vectors (
			memory_id INTEGER PRIMARY KEY,
			vector    BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS memory_nodes (
			id   INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			area TEXT NOT NULL,
			name_key TEXT NOT NULL DEFAULT '',
			UNIQUE(type, name)
		)`,
		`CREATE TABLE IF NOT EXISTS memory_edges (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			from_id   INTEGER NOT NULL,
			to_id     INTEGER NOT NULL,
			relation  TEXT NOT NULL,
			memory_id INTEGER NOT NULL DEFAULT 0,
			UNIQUE(from_id, to_id, relation)
		)`,
		`CREATE TABLE IF NOT EXISTS memory_types (
			kind  TEXT NOT NULL,
			name  TEXT NOT NULL,
			title TEXT NOT NULL,
			PRIMARY KEY(kind, name)
		)`,
		`CREATE TABLE IF NOT EXISTS memory_rules (
			id    INTEGER PRIMARY KEY AUTOINCREMENT,
			kind  TEXT NOT NULL,
			value TEXT NOT NULL,
			note  TEXT NOT NULL DEFAULT '',
			UNIQUE(kind, value)
		)`,
		`CREATE TABLE IF NOT EXISTS memory_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			consumer   TEXT NOT NULL,
			session_id INTEGER NOT NULL DEFAULT 0,
			area       TEXT NOT NULL DEFAULT '',
			action     TEXT NOT NULL,
			detail     TEXT NOT NULL DEFAULT '',
			granted    INTEGER NOT NULL,
			tokens     INTEGER NOT NULL DEFAULT 0,
			saved      INTEGER NOT NULL DEFAULT 0,
			at         TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_log_at ON memory_log(at DESC)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("память: %w", err)
		}
	}
	// колонки, добавленные позже: на свежей базе они уже созданы выше,
	// на старой их надо дописать — «уже есть» здесь не ошибка
	for _, q := range []string{
		`ALTER TABLE memory_places ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE memory_places ADD COLUMN doubtful INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE memory_places ADD COLUMN checked TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("память: %w", err)
		}
	}
	if err := s.migratePlaceDir(); err != nil {
		return fmt.Errorf("память: папка в привязках: %w", err)
	}
	// когда сессия последний раз подавала признаки жизни: по этому закрываем замолчавшие
	_, _ = s.db.Exec(`ALTER TABLE memory_sessions ADD COLUMN updated TEXT NOT NULL DEFAULT ''`)
	// «разобрано Сёмой»: колонка появилась позже, старой базе дописываем
	_, _ = s.db.Exec(`ALTER TABLE memories ADD COLUMN reviewed INTEGER NOT NULL DEFAULT 0`)
	// имя узла без регистра: «pi» и «Pi» — один узел (5 октября 2026)
	if _, err := s.db.Exec(`ALTER TABLE memory_nodes ADD COLUMN name_key TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("память: %w", err)
	}
	if err := s.foldMemoryNodes(); err != nil {
		return fmt.Errorf("память: слияние узлов: %w", err)
	}
	// координаты забытых записей: до 9 сентября 2026 удаление записи не
	// уносило привязку, и в базе копились места без знания. Проверка идёт
	// при каждом старте — если она снова что-то находит, значит удаление
	// опять роняет привязку, и это должно быть видно, а не молча чиниться
	if res, err := s.db.Exec(`DELETE FROM memory_places WHERE NOT EXISTS
		(SELECT 1 FROM memories m WHERE m.id = memory_places.memory_id)`); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("память: убрано привязок без записи: %d", n)
		}
	}
	if res, err := s.db.Exec(`UPDATE memory_sessions SET crystal = -1 WHERE crystal > 0 AND NOT EXISTS
		(SELECT 1 FROM memories m WHERE m.id = memory_sessions.crystal)`); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("память: сессий с забытым кристаллом: %d", n)
		}
	}
	return nil
}

// MemoryConsumerByPrefix — потребитель того же агента, чья папка — сам путь
// или его предок: подпапки проекта не должны плодить потребителей.
func (s *Store) MemoryConsumerByPrefix(kind, name, path string) (*MemoryConsumer, bool) {
	rows, err := s.db.Query(`SELECT id, kind, name, path, areas, seen, created FROM memory_consumers WHERE kind = ? AND name = ? AND path != ''`, kind, name)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	var best *MemoryConsumer
	for rows.Next() {
		c, err := scanConsumer(rows)
		if err != nil {
			continue
		}
		p := strings.TrimRight(c.Path, "/")
		if path == p || strings.HasPrefix(path, p+"/") {
			if best == nil || len(p) > len(strings.TrimRight(best.Path, "/")) {
				best = c
			}
		}
	}
	return best, best != nil
}

// MergeMemoryConsumers переносит сессии и удаляет лишнего потребителя.
func (s *Store) MergeMemoryConsumers(from, to int64) error {
	if _, err := s.db.Exec(`UPDATE memory_sessions SET consumer_id = ? WHERE consumer_id = ?`, to, from); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM memory_consumers WHERE id = ?`, from)
	return err
}

// TouchMemorySession / AppendSessionSummary — сессия жива, сводка растёт.
func (s *Store) AppendSessionSummary(id int64, text string, keep int) error {
	m, err := s.MemorySession(id)
	if err != nil {
		return err
	}
	sum := strings.TrimSpace(m.Summary + "\n" + text)
	if r := []rune(sum); len(r) > keep {
		sum = string(r[len(r)-keep:])
	}
	_, err = s.db.Exec(`UPDATE memory_sessions SET summary = ?, updated = ? WHERE id = ?`, sum, time.Now().Format(time.RFC3339), id)
	return err
}

// SetSessionSummary заменяет сводку целиком: так свёртка кладёт на место
// распухшего начала одну строку о том, что было раньше.
func (s *Store) SetSessionSummary(id int64, text string) error {
	_, err := s.db.Exec(`UPDATE memory_sessions SET summary = ?, updated = ? WHERE id = ?`,
		text, time.Now().Format(time.RFC3339), id)
	return err
}

// OpenMemorySession — незакрытая сессия потребителя (nil, если нет).
func (s *Store) OpenMemorySession(consumerID int64) *MemorySession {
	m := &MemorySession{}
	err := s.db.QueryRow(`SELECT id, consumer_id, agent, model, started, ended, summary, crystal FROM memory_sessions
		WHERE consumer_id = ? AND ended = '' ORDER BY id DESC LIMIT 1`, consumerID).
		Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal)
	if err != nil {
		return nil
	}
	return m
}

// IdleOpenSessions — открытые сессии внешних агентов без признаков жизни
// дольше указанного срока.
func (s *Store) IdleOpenSessions(before string) ([]*MemorySession, error) {
	rows, err := s.db.Query(`SELECT s.id, s.consumer_id, s.agent, s.model, s.started, s.ended, s.summary, s.crystal
		FROM memory_sessions s JOIN memory_consumers c ON c.id = s.consumer_id
		WHERE s.ended = '' AND c.kind = 'mcp' AND COALESCE(NULLIF(s.updated, ''), s.started) < ?`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemorySession
	for rows.Next() {
		m := &MemorySession{}
		if err := rows.Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// IdleOpenChatSessions — открытые сессии чатов без признаков жизни
// дольше срока. Чат мог быть удалён: тогда обход чатов их не видит.
func (s *Store) IdleOpenChatSessions(before string) ([]*MemorySession, error) {
	rows, err := s.db.Query(`SELECT s.id, s.consumer_id, s.agent, s.model, s.started, s.ended, s.summary, s.crystal
		FROM memory_sessions s JOIN memory_consumers c ON c.id = s.consumer_id
		WHERE s.ended = '' AND c.kind = 'chat' AND COALESCE(NULLIF(s.updated, ''), s.started) < ?`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemorySession
	for rows.Next() {
		m := &MemorySession{}
		if err := rows.Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MemorySessions — последние сессии всех потребителей.
func (s *Store) MemorySessions(limit int) ([]*MemorySession, error) {
	rows, err := s.db.Query(`SELECT id, consumer_id, agent, model, started, ended, summary, crystal FROM memory_sessions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemorySession
	for rows.Next() {
		m := &MemorySession{}
		if err := rows.Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MemoriesBySession — что родилось из сессии.
func (s *Store) MemoriesBySession(id int64) ([]*Memory, error) {
	return s.queryMemories(`SELECT `+memoryCols+` FROM memories WHERE session_id = ? ORDER BY CASE kind WHEN 'crystal' THEN 0 WHEN 'lesson' THEN 1 WHEN 'fact' THEN 2 ELSE 3 END, id`, id)
}

// SkipEmptySessions помечает закрытые сессии без сводки как «без
// кристалла»: сводить нечего, и ждать сведения им незачем.
func (s *Store) SkipEmptySessions() int64 {
	res, err := s.db.Exec(`UPDATE memory_sessions SET crystal = -1 WHERE ended != '' AND TRIM(summary) = '' AND crystal = 0`)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

func (s *Store) DeleteMemorySession(id int64) error {
	_, err := s.db.Exec(`DELETE FROM memory_sessions WHERE id = ?`, id)
	return err
}

// ---- области ----

func (s *Store) MemoryAreas() ([]*MemoryArea, error) {
	rows, err := s.db.Query(`SELECT name, title, outbound, confirm, ttl_days, forget, created FROM memory_areas ORDER BY created`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryArea
	for rows.Next() {
		a := &MemoryArea{}
		var ob, fg int
		if err := rows.Scan(&a.Name, &a.Title, &ob, &a.Confirm, &a.TTLDays, &fg, &a.Created); err != nil {
			return nil, err
		}
		a.Outbound, a.Forget = ob == 1, fg == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) MemoryArea(name string) (*MemoryArea, bool) {
	a := &MemoryArea{}
	var ob, fg int
	err := s.db.QueryRow(`SELECT name, title, outbound, confirm, ttl_days, forget, created FROM memory_areas WHERE name = ?`, name).
		Scan(&a.Name, &a.Title, &ob, &a.Confirm, &a.TTLDays, &fg, &a.Created)
	a.Outbound, a.Forget = ob == 1, fg == 1
	return a, err == nil
}

func (s *Store) SaveMemoryArea(a *MemoryArea) error {
	if a.Created == "" {
		a.Created = time.Now().Format(time.RFC3339)
	}
	if a.Confirm == "" {
		a.Confirm = ConfirmAuto
	}
	_, err := s.db.Exec(`INSERT INTO memory_areas (name, title, outbound, confirm, ttl_days, forget, created)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET title = excluded.title, outbound = excluded.outbound,
		confirm = excluded.confirm, ttl_days = excluded.ttl_days, forget = excluded.forget`,
		a.Name, a.Title, b2i(a.Outbound), a.Confirm, a.TTLDays, b2i(a.Forget), a.Created)
	return err
}

// DeleteMemoryArea удаляет область вместе с записями и узлами: удаление
// области — осознанное действие с подтверждением на экране.
func (s *Store) DeleteMemoryArea(name string) error {
	ids, _ := s.memoryIDsByArea(name)
	for _, id := range ids {
		if err := s.DeleteMemory(id); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM memory_edges WHERE from_id IN (SELECT id FROM memory_nodes WHERE area = ?)
		OR to_id IN (SELECT id FROM memory_nodes WHERE area = ?)`, name, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM memory_nodes WHERE area = ?`, name); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM memory_areas WHERE name = ?`, name)
	return err
}

func (s *Store) memoryIDsByArea(area string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM memories WHERE area = ?`, area)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- потребители ----

func (s *Store) MemoryConsumers() ([]*MemoryConsumer, error) {
	rows, err := s.db.Query(`SELECT id, kind, name, path, areas, seen, created FROM memory_consumers ORDER BY kind, name, path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryConsumer
	for rows.Next() {
		c, err := scanConsumer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanConsumer(r rowScanner) (*MemoryConsumer, error) {
	c := &MemoryConsumer{}
	var areas string
	if err := r.Scan(&c.ID, &c.Kind, &c.Name, &c.Path, &areas, &c.Seen, &c.Created); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(areas), &c.Areas)
	return c, nil
}

func (s *Store) MemoryConsumerByID(id int64) (*MemoryConsumer, error) {
	return scanConsumer(s.db.QueryRow(`SELECT id, kind, name, path, areas, seen, created FROM memory_consumers WHERE id = ?`, id))
}

// MemoryConsumer находит или заводит потребителя. Новый внешний агент
// появляется с пустым набором областей — до выдачи человеком ему всё
// закрыто.
func (s *Store) MemoryConsumer(kind, name, path string) (*MemoryConsumer, error) {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT INTO memory_consumers (kind, name, path, areas, seen, created) VALUES (?, ?, ?, '[]', ?, ?)
		ON CONFLICT(kind, name, path) DO UPDATE SET seen = excluded.seen`, kind, name, path, now, now)
	if err != nil {
		return nil, err
	}
	return scanConsumer(s.db.QueryRow(`SELECT id, kind, name, path, areas, seen, created FROM memory_consumers
		WHERE kind = ? AND name = ? AND path = ?`, kind, name, path))
}

func (s *Store) SetConsumerAreas(id int64, areas []string) error {
	if areas == nil {
		areas = []string{}
	}
	raw, _ := json.Marshal(areas)
	_, err := s.db.Exec(`UPDATE memory_consumers SET areas = ? WHERE id = ?`, string(raw), id)
	return err
}

func (s *Store) DeleteMemoryConsumer(id int64) error {
	_, err := s.db.Exec(`DELETE FROM memory_consumers WHERE id = ?`, id)
	return err
}

// ---- сессии ----

func (s *Store) StartMemorySession(consumerID int64, agent, model string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO memory_sessions (consumer_id, agent, model, started) VALUES (?, ?, ?, ?)`,
		consumerID, agent, model, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) EndMemorySession(id int64, summary string) error {
	_, err := s.db.Exec(`UPDATE memory_sessions SET ended = ?, summary = ? WHERE id = ?`,
		time.Now().Format(time.RFC3339), summary, id)
	return err
}

func (s *Store) MemorySession(id int64) (*MemorySession, error) {
	m := &MemorySession{}
	err := s.db.QueryRow(`SELECT id, consumer_id, agent, model, started, ended, summary, crystal FROM memory_sessions WHERE id = ?`, id).
		Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal)
	return m, err
}

// LastMemorySession — последняя сессия потребителя (nil, если не было).
func (s *Store) LastMemorySession(consumerID int64) *MemorySession {
	m := &MemorySession{}
	err := s.db.QueryRow(`SELECT id, consumer_id, agent, model, started, ended, summary, crystal FROM memory_sessions
		WHERE consumer_id = ? ORDER BY id DESC LIMIT 1`, consumerID).
		Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal)
	if err != nil {
		return nil
	}
	return m
}

// LastCrystalSession — последняя сессия потребителя, у которой есть
// кристалл (текущая открытая его ещё не имеет).
func (s *Store) LastCrystalSession(consumerID int64) *MemorySession {
	m := &MemorySession{}
	err := s.db.QueryRow(`SELECT id, consumer_id, agent, model, started, ended, summary, crystal FROM memory_sessions
		WHERE consumer_id = ? AND crystal > 0 ORDER BY id DESC LIMIT 1`, consumerID).
		Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal)
	if err != nil {
		return nil
	}
	return m
}

// SessionsWithoutCrystal — завершённые сессии со сводкой, из которых
// кристалл ещё не сделан.
func (s *Store) SessionsWithoutCrystal(limit int) ([]*MemorySession, error) {
	rows, err := s.db.Query(`SELECT id, consumer_id, agent, model, started, ended, summary, crystal FROM memory_sessions
		WHERE ended != '' AND crystal = 0 AND summary != '' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemorySession
	for rows.Next() {
		m := &MemorySession{}
		if err := rows.Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ResumeSessionsWaitingTrust — сессии, отложенные до доверенной модели
// (crystal = -2), снова в очередь на кристалл.
func (s *Store) ResumeSessionsWaitingTrust() error {
	_, err := s.db.Exec(`UPDATE memory_sessions SET crystal = 0 WHERE crystal = -2`)
	return err
}

func (s *Store) SetSessionCrystal(sessionID, memoryID int64) error {
	_, err := s.db.Exec(`UPDATE memory_sessions SET crystal = ? WHERE id = ?`, memoryID, sessionID)
	return err
}

// ---- записи ----

const memoryCols = `id, kind, area, text, status, session_id, agent, model, created, supersedes, confirmed, weight, sources, expires, graphed`

func scanMemory(r rowScanner) (*Memory, error) {
	m := &Memory{}
	var conf, graphed int
	var sources string
	if err := r.Scan(&m.ID, &m.Kind, &m.Area, &m.Text, &m.Status, &m.SessionID, &m.Agent, &m.Model, &m.Created,
		&m.Supersedes, &conf, &m.Weight, &sources, &m.Expires, &graphed); err != nil {
		return nil, err
	}
	m.Confirmed, m.Graphed = conf == 1, graphed == 1
	_ = json.Unmarshal([]byte(sources), &m.Sources)
	return m, nil
}

func (s *Store) queryMemories(q string, args ...any) ([]*Memory, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveMemory добавляет запись. Если она заменяет старую (Supersedes),
// старая помечается заменённой и уходит из выдачи, но остаётся в истории.
func (s *Store) SaveMemory(m *Memory) (int64, error) {
	if m.Created == "" {
		m.Created = time.Now().Format(time.RFC3339)
	}
	if m.Status == "" {
		m.Status = MemOK
	}
	if m.Weight == 0 {
		m.Weight = 1
	}
	if m.Sources == nil {
		m.Sources = []string{m.Agent}
	}
	sources, _ := json.Marshal(m.Sources)
	res, err := s.db.Exec(`INSERT INTO memories (kind, area, text, status, session_id, agent, model, created, supersedes, confirmed, weight, sources, expires, graphed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Kind, m.Area, m.Text, m.Status, m.SessionID, m.Agent, m.Model, m.Created, m.Supersedes, b2i(m.Confirmed), m.Weight, string(sources), m.Expires, b2i(m.Graphed))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	m.ID = id
	_, _ = s.db.Exec(`INSERT INTO memories_fts (rowid, text) VALUES (?, ?)`, id, m.Text)
	if m.Supersedes > 0 {
		_, _ = s.db.Exec(`UPDATE memories SET replaced = 1 WHERE id = ?`, m.Supersedes)
	}
	return id, nil
}

func (s *Store) UpdateMemory(m *Memory) error {
	sources, _ := json.Marshal(m.Sources)
	_, err := s.db.Exec(`UPDATE memories SET kind = ?, area = ?, text = ?, status = ?, confirmed = ?, weight = ?, sources = ?, expires = ?, graphed = ?
		WHERE id = ?`, m.Kind, m.Area, m.Text, m.Status, b2i(m.Confirmed), m.Weight, string(sources), m.Expires, b2i(m.Graphed), m.ID)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM memories_fts WHERE rowid = ?`, m.ID)
	_, _ = s.db.Exec(`INSERT INTO memories_fts (rowid, text) VALUES (?, ?)`, m.ID, m.Text)
	return nil
}

func (s *Store) MemoryByID(id int64) (*Memory, error) {
	return scanMemory(s.db.QueryRow(`SELECT `+memoryCols+` FROM memories WHERE id = ?`, id))
}

// MemoriesByIDs — записи по номерам пачкой; ненайденных в ответе нет. Экран
// узла графа брал запись-источник каждой связи отдельным запросом — у узла с
// сотней связей это были сотни запросов на экран (5 октября 2026).
func (s *Store) MemoriesByIDs(ids []int64) (map[int64]*Memory, error) {
	out := make(map[int64]*Memory, len(ids))
	const chunk = 500 // предел параметров у SQLite не дальше
	for len(ids) > 0 {
		part := ids
		if len(part) > chunk {
			part = part[:chunk]
		}
		ids = ids[len(part):]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT `+memoryCols+` FROM memories WHERE id IN (?`+strings.Repeat(`, ?`, len(part)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			m, err := scanMemory(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[m.ID] = m
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) DeleteMemory(id int64) error {
	for _, q := range []string{
		`DELETE FROM memories_fts WHERE rowid = ?`,
		`DELETE FROM memory_vectors WHERE memory_id = ?`,
		`DELETE FROM memory_edges WHERE memory_id = ?`,
		// координата уходит вместе с записью: привязка без записи
		// ничего не значит и только копится в базе
		`DELETE FROM memory_places WHERE memory_id = ?`,
		// сессия помнит свой кристалл номером: забытый кристалл делает
		// её «сведённой» с ссылкой в пустоту — честнее сказать
		// «без кристалла», чем показывать капсулу без текста
		`UPDATE memory_sessions SET crystal = -1 WHERE crystal = ?`,
		`DELETE FROM memories WHERE id = ?`,
	} {
		if _, err := s.db.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

// MarkReplaced убирает запись из выдачи в пользу другой (слияние дублей).
func (s *Store) MarkReplaced(id, by int64) error {
	_, err := s.db.Exec(`UPDATE memories SET replaced = 1, supersedes = CASE WHEN supersedes = 0 THEN ? ELSE supersedes END WHERE id = ?`, by, id)
	return err
}

// Memories — актуальные записи (без заменённых) области или всех областей.
func (s *Store) Memories(area, kind string, limit int) ([]*Memory, error) {
	q := `SELECT ` + memoryCols + ` FROM memories WHERE replaced = 0`
	var args []any
	if area != "" {
		q += ` AND area = ?`
		args = append(args, area)
	}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY CASE kind WHEN 'lesson' THEN 0 WHEN 'fact' THEN 1 WHEN 'crystal' THEN 2 ELSE 3 END, id DESC LIMIT ?`
	args = append(args, limit)
	return s.queryMemories(q, args...)
}

// MemoryHistory — цепочка версий записи, от новой к старым.
func (s *Store) MemoryHistory(id int64) ([]*Memory, error) {
	var out []*Memory
	for id > 0 && len(out) < 20 {
		m, err := s.MemoryByID(id)
		if err != nil {
			break
		}
		out = append(out, m)
		id = m.Supersedes
	}
	return out, nil
}

// MemoriesByFTS — поиск по словам (BM25); rank меньше — лучше.
func (s *Store) MemoriesByFTS(query string, limit int) ([]*Memory, []float64, error) {
	terms := ftsQuery(query)
	if terms == "" {
		return nil, nil, nil
	}
	rows, err := s.db.Query(`SELECT m.id, m.kind, m.area, m.text, m.status, m.session_id, m.agent, m.model, m.created,
		m.supersedes, m.confirmed, m.weight, m.sources, m.expires, m.graphed, bm25(memories_fts) AS rank
		FROM memories_fts f JOIN memories m ON m.id = f.rowid
		WHERE memories_fts MATCH ? AND m.replaced = 0 AND m.status = 'ok' ORDER BY rank LIMIT ?`, terms, limit)
	if err != nil {
		return nil, nil, nil // неудачный запрос FTS — не повод ломать ответ
	}
	defer rows.Close()
	var out []*Memory
	var ranks []float64
	for rows.Next() {
		m := &Memory{}
		var conf, graphed int
		var sources string
		var rank float64
		if err := rows.Scan(&m.ID, &m.Kind, &m.Area, &m.Text, &m.Status, &m.SessionID, &m.Agent, &m.Model, &m.Created,
			&m.Supersedes, &conf, &m.Weight, &sources, &m.Expires, &graphed, &rank); err != nil {
			return nil, nil, err
		}
		m.Confirmed, m.Graphed = conf == 1, graphed == 1
		_ = json.Unmarshal([]byte(sources), &m.Sources)
		out = append(out, m)
		ranks = append(ranks, rank)
	}
	return out, ranks, rows.Err()
}

// ftsQuery превращает вопрос человека в запрос FTS: слова через OR, без
// служебных символов, короткие слова отбрасываются.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'а' && r <= 'я' || r == 'ё')
	}) {
		if len([]rune(w)) < 3 {
			continue
		}
		terms = append(terms, `"`+w+`"`)
		if len(terms) >= 12 {
			break
		}
	}
	return strings.Join(terms, " OR ")
}

// MemoryVectors — все векторы актуальных записей для брутфорс-косинуса.
func (s *Store) MemoryVectors() (map[int64][]byte, error) {
	rows, err := s.db.Query(`SELECT v.memory_id, v.vector FROM memory_vectors v JOIN memories m ON m.id = v.memory_id
		WHERE m.replaced = 0 AND m.status = 'ok'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]byte{}
	for rows.Next() {
		var id int64
		var v []byte
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

func (s *Store) SaveMemoryVector(id int64, vec []byte) error {
	_, err := s.db.Exec(`INSERT INTO memory_vectors (memory_id, vector) VALUES (?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET vector = excluded.vector`, id, vec)
	return err
}

// MemoriesForReview — что Сёма ещё не разбирал сам: неподтверждённые записи
// с текстом, кроме наблюдений (те живут коротко и уходят по сроку).
func (s *Store) MemoriesForReview(limit int) ([]*Memory, error) {
	return s.queryMemories(`SELECT `+memoryCols+` FROM memories WHERE replaced = 0 AND status = 'ok'
		AND confirmed = 0 AND reviewed = 0 AND kind IN ('fact', 'lesson', 'crystal') ORDER BY id LIMIT ?`, limit)
}

// SetMemoryReviewed отмечает запись разобранной: второй раз не спрашиваем.
func (s *Store) SetMemoryReviewed(id int64) error {
	_, err := s.db.Exec(`UPDATE memories SET reviewed = 1 WHERE id = ?`, id)
	return err
}

// MemoriesWithoutVector / MemoriesNotGraphed — очередь фоновых задач памяти.
func (s *Store) MemoriesWithoutVector(limit int) ([]*Memory, error) {
	return s.queryMemories(`SELECT `+memoryCols+` FROM memories m WHERE m.replaced = 0 AND m.status = 'ok'
		AND NOT EXISTS (SELECT 1 FROM memory_vectors v WHERE v.memory_id = m.id) ORDER BY id LIMIT ?`, limit)
}

func (s *Store) MemoriesNotGraphed(limit int) ([]*Memory, error) {
	return s.queryMemories(`SELECT `+memoryCols+` FROM memories WHERE replaced = 0 AND status = 'ok' AND graphed = 0
		AND kind IN ('fact', 'lesson', 'crystal') ORDER BY id LIMIT ?`, limit)
}

// ExpiredMemories — записи, у которых вышел срок.
func (s *Store) ExpiredMemories() ([]*Memory, error) {
	return s.queryMemories(`SELECT `+memoryCols+` FROM memories WHERE expires != '' AND expires < ? AND confirmed = 0`,
		time.Now().Format(time.RFC3339))
}

// PendingMemories — записи, ждущие подтверждения человека.
func (s *Store) PendingMemories() ([]*Memory, error) {
	return s.queryMemories(`SELECT ` + memoryCols + ` FROM memories WHERE status = 'pending' ORDER BY id DESC`)
}

// CountMemories — сколько актуальных записей по областям и видам.
func (s *Store) CountMemories() (byArea map[string]int, byKind map[string]int) {
	byArea, byKind = map[string]int{}, map[string]int{}
	rows, err := s.db.Query(`SELECT area, kind, COUNT(*) FROM memories WHERE replaced = 0 AND status = 'ok' GROUP BY area, kind`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var area, kind string
		var n int
		if rows.Scan(&area, &kind, &n) == nil {
			byArea[area] += n
			byKind[kind] += n
		}
	}
	return
}

// ---- граф ----

// MemoryNodeByName — узел по типу и точному имени.
func (s *Store) MemoryNodeByName(typ, name string) (*MemoryNode, bool) {
	n := &MemoryNode{}
	err := s.db.QueryRow(`SELECT id, type, name, area FROM memory_nodes WHERE type = ? AND name = ?`, typ, name).
		Scan(&n.ID, &n.Type, &n.Name, &n.Area)
	return n, err == nil
}

// MemoryNodesByKey — узлы с тем же именем без учёта регистра, во всех
// областях: человек ввёл имя, а какой это узел — решать ему, если их несколько.
func (s *Store) MemoryNodesByKey(typ, name string) ([]*MemoryNode, error) {
	rows, err := s.db.Query(`SELECT id, type, name, area FROM memory_nodes WHERE type = ? AND name_key = ? ORDER BY id`,
		typ, memoryNameKey(name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryNode
	for rows.Next() {
		n := &MemoryNode{}
		if err := rows.Scan(&n.ID, &n.Type, &n.Name, &n.Area); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// memoryNameKey — имя узла для сравнения: без регистра и крайних пробелов.
// Регистр снимает Go, а не SQLite: lower() в SQLite кириллицу не знает.
func memoryNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// foldMemoryNodes — ключ имени у старых узлов и разовое слияние узлов одной
// области, различающихся только регистром («pi» и «Pi», «sema» и «Sema»):
// остаётся тот, у кого больше связей, при равенстве — заведённый раньше;
// связи второго переходят к нему, каждое слияние — строкой в журнале памяти.
// Узлы разных областей не сливаются: связи «личного» ушли бы к узлу «кода» и
// стали бы видны тем, кому выдан только «код». Повторный запуск ничего не
// находит и ничего не делает.
func (s *Store) foldMemoryNodes() error {
	rows, err := s.db.Query(`SELECT id, name FROM memory_nodes WHERE name_key = ''`)
	if err != nil {
		return err
	}
	type pending struct {
		id  int64
		key string
	}
	var keys []pending
	for rows.Next() {
		var p pending
		var name string
		if err := rows.Scan(&p.id, &name); err != nil {
			rows.Close()
			return err
		}
		p.key = memoryNameKey(name)
		keys = append(keys, p)
	}
	rows.Close()
	if len(keys) > 0 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, p := range keys {
			if _, err := tx.Exec(`UPDATE memory_nodes SET name_key = ? WHERE id = ?`, p.key, p.id); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_nodes_key ON memory_nodes(type, name_key)`); err != nil {
		return err
	}

	type group struct{ typ, key, area string }
	var groups []group
	grows, err := s.db.Query(`SELECT type, name_key, area FROM memory_nodes GROUP BY type, name_key, area HAVING COUNT(*) > 1`)
	if err != nil {
		return err
	}
	for grows.Next() {
		var g group
		if err := grows.Scan(&g.typ, &g.key, &g.area); err != nil {
			grows.Close()
			return err
		}
		groups = append(groups, g)
	}
	grows.Close()
	merged := 0
	for _, g := range groups {
		type member struct {
			id         int64
			name, area string
			deg        int
		}
		var members []member
		mrows, err := s.db.Query(`SELECT n.id, n.name, n.area,
				(SELECT COUNT(*) FROM memory_edges e WHERE e.from_id = n.id OR e.to_id = n.id) AS deg
			FROM memory_nodes n WHERE n.type = ? AND n.name_key = ? AND n.area = ? ORDER BY deg DESC, n.id ASC`, g.typ, g.key, g.area)
		if err != nil {
			return err
		}
		for mrows.Next() {
			var m member
			if err := mrows.Scan(&m.id, &m.name, &m.area, &m.deg); err != nil {
				mrows.Close()
				return err
			}
			members = append(members, m)
		}
		mrows.Close()
		if len(members) < 2 {
			continue
		}
		keep := members[0]
		for _, m := range members[1:] {
			if err := s.MergeMemoryNodes(m.id, keep.id); err != nil {
				return err
			}
			merged++
			s.LogMemory(MemoryLogEntry{Consumer: "Сёма · граф памяти", Area: keep.area, Action: "merge", Granted: true,
				Detail: fmt.Sprintf("узел «%s» слит с «%s»: имена различались только регистром", m.name, keep.name)})
		}
	}
	if merged > 0 {
		log.Printf("память: слито узлов, отличавшихся только регистром: %d", merged)
	}
	return nil
}

func (s *Store) MemoryNode(id int64) (*MemoryNode, error) {
	n := &MemoryNode{}
	err := s.db.QueryRow(`SELECT id, type, name, area FROM memory_nodes WHERE id = ?`, id).Scan(&n.ID, &n.Type, &n.Name, &n.Area)
	return n, err
}

// EnsureMemoryNode находит узел по типу и имени или заводит его. То же имя
// как есть — тот же узел, в какой бы области он ни был (имя в типе
// уникально, так было всегда). То же имя в другом регистре — тот же узел, но
// только в своей области: граф различал регистр, и одна сущность
// разъезжалась на «pi» и «Pi»; а склейка через границу областей открыла бы
// личные связи потребителю, которому выдана только другая область.
func (s *Store) EnsureMemoryNode(typ, name, area string) (int64, error) {
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM memory_nodes WHERE type = ? AND name = ?`, typ, name).Scan(&id); err == nil {
		return id, nil
	}
	if err := s.db.QueryRow(`SELECT id FROM memory_nodes WHERE type = ? AND name_key = ? AND area = ? ORDER BY id LIMIT 1`,
		typ, memoryNameKey(name), area).Scan(&id); err == nil {
		return id, nil
	}
	res, err := s.db.Exec(`INSERT INTO memory_nodes (type, name, area, name_key) VALUES (?, ?, ?, ?)`, typ, name, area, memoryNameKey(name))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) AddMemoryEdge(from, to int64, relation string, memoryID int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO memory_edges (from_id, to_id, relation, memory_id) VALUES (?, ?, ?, ?)`,
		from, to, relation, memoryID)
	return err
}

func (s *Store) MemoryNodes() ([]*MemoryNode, error) {
	rows, err := s.db.Query(`SELECT id, type, name, area FROM memory_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryNode
	for rows.Next() {
		n := &MemoryNode{}
		if err := rows.Scan(&n.ID, &n.Type, &n.Name, &n.Area); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) MemoryEdges() ([]*MemoryEdge, error) {
	rows, err := s.db.Query(`SELECT id, from_id, to_id, relation, memory_id FROM memory_edges ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryEdge
	for rows.Next() {
		e := &MemoryEdge{}
		if err := rows.Scan(&e.ID, &e.From, &e.To, &e.Relation, &e.MemoryID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FindMemoryNodes — узлы, чьё имя встречается в тексте (для обхода от
// сущностей, названных в вопросе).
func (s *Store) FindMemoryNodes(text string) ([]*MemoryNode, error) {
	all, err := s.MemoryNodes()
	if err != nil {
		return nil, err
	}
	low := strings.ToLower(text)
	var out []*MemoryNode
	for _, n := range all {
		name := strings.ToLower(n.Name)
		if len([]rune(name)) >= 3 && strings.Contains(low, name) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *Store) DeleteMemoryNode(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM memory_edges WHERE from_id = ? OR to_id = ?`, id, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM memory_nodes WHERE id = ?`, id)
	return err
}

func (s *Store) MoveMemoryNode(id int64, area string) error {
	_, err := s.db.Exec(`UPDATE memory_nodes SET area = ? WHERE id = ?`, area, id)
	return err
}

// MergeMemoryNodes переносит связи узла from на узел to и удаляет from.
// MergeMemoryNodes — влить узел from в to: связи переходят, from удаляется.
func (s *Store) MergeMemoryNodes(from, to int64) error {
	// «слить с самим собой» удалило бы узел со всеми связями: после поиска
	// без регистра такое имя ввести легко
	if from == to {
		return fmt.Errorf("это тот же самый узел")
	}
	// связь между двумя сливаемыми узлами стала бы петлёй «узел — сам с
	// собой»: утверждения в ней нет. Прежние петли победителя не трогаем
	if _, err := s.db.Exec(`DELETE FROM memory_edges WHERE (from_id = ? AND to_id = ?) OR (from_id = ? AND to_id = ?)`,
		from, to, to, from); err != nil {
		return err
	}
	if err := s.keepEdgeSources(from, to); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE OR IGNORE memory_edges SET from_id = ? WHERE from_id = ?`, to, from); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE OR IGNORE memory_edges SET to_id = ? WHERE to_id = ?`, to, from); err != nil {
		return err
	}
	return s.DeleteMemoryNode(from)
}

// keepEdgeSources — связь, которая у обоих узлов одна и та же, остаётся у
// победителя, а связь проигравшего удаляется вместе с ним. Если у
// победителя записи-источника нет, а у проигравшего есть — переносим её:
// иначе связь осталась бы утверждением без автора.
func (s *Store) keepEdgeSources(from, to int64) error {
	rows, err := s.db.Query(`SELECT from_id, to_id, relation, memory_id FROM memory_edges
		WHERE (from_id = ? OR to_id = ?) AND memory_id <> 0`, from, from)
	if err != nil {
		return err
	}
	type edge struct {
		from, to, mem int64
		rel           string
	}
	var list []edge
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.from, &e.to, &e.rel, &e.mem); err != nil {
			rows.Close()
			return err
		}
		list = append(list, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range list {
		nf, nt := e.from, e.to
		if nf == from {
			nf = to
		}
		if nt == from {
			nt = to
		}
		if _, err := s.db.Exec(`UPDATE memory_edges SET memory_id = ? WHERE from_id = ? AND to_id = ? AND relation = ? AND memory_id = 0`,
			e.mem, nf, nt, e.rel); err != nil {
			return err
		}
	}
	return nil
}

// ---- справочники ----

func (s *Store) MemoryTypes(kind string) ([][2]string, error) {
	rows, err := s.db.Query(`SELECT name, title FROM memory_types WHERE kind = ? ORDER BY rowid`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var n, t string
		if err := rows.Scan(&n, &t); err != nil {
			return nil, err
		}
		out = append(out, [2]string{n, t})
	}
	return out, rows.Err()
}

func (s *Store) SaveMemoryType(kind, name, title string) error {
	_, err := s.db.Exec(`INSERT INTO memory_types (kind, name, title) VALUES (?, ?, ?)
		ON CONFLICT(kind, name) DO UPDATE SET title = excluded.title`, kind, name, title)
	return err
}

func (s *Store) MemoryRules(kind string) ([]string, error) {
	rows, err := s.db.Query(`SELECT value FROM memory_rules WHERE kind = ? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) AddMemoryRule(kind, value, note string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO memory_rules (kind, value, note) VALUES (?, ?, ?)`, kind, value, note)
	return err
}

func (s *Store) DeleteMemoryRule(kind, value string) error {
	_, err := s.db.Exec(`DELETE FROM memory_rules WHERE kind = ? AND value = ?`, kind, value)
	return err
}

// ---- журнал ----

func (s *Store) LogMemory(e MemoryLogEntry) {
	if e.At == "" {
		e.At = time.Now().Format(time.RFC3339)
	}
	_, _ = s.db.Exec(`INSERT INTO memory_log (consumer, session_id, area, action, detail, granted, tokens, saved, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.Consumer, e.SessionID, e.Area, e.Action, e.Detail, b2i(e.Granted), e.Tokens, e.Saved, e.At)
	_, _ = s.db.Exec(`DELETE FROM memory_log WHERE id NOT IN (SELECT id FROM memory_log ORDER BY id DESC LIMIT 2000)`)
}

func (s *Store) MemoryLog(limit int) ([]MemoryLogEntry, error) {
	rows, err := s.db.Query(`SELECT id, consumer, session_id, area, action, detail, granted, tokens, saved, at
		FROM memory_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryLogEntry
	for rows.Next() {
		var e MemoryLogEntry
		var g int
		if err := rows.Scan(&e.ID, &e.Consumer, &e.SessionID, &e.Area, &e.Action, &e.Detail, &g, &e.Tokens, &e.Saved, &e.At); err != nil {
			return nil, err
		}
		e.Granted = g == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// MemoryLogTotals — сколько токенов ушло из памяти и сколько сбережено с
// указанного момента.
// MemoryLogUse — кто и как пользовался памятью за срок: обращений, выдано
// токенов, сбережено. Measured — есть ли у потребителя мера экономии
// вообще: у чата Сёмы память заменяет письма, у внешних агентов
// считается только сводка прошлой сессии на старте.
type MemoryLogUse struct {
	Consumer string
	Calls    int
	Tokens   int
	Saved    int
	Measured bool
}

func (s *Store) MemoryLogByConsumer(since string) []MemoryLogUse {
	rows, err := s.db.Query(`SELECT consumer, COUNT(*), COALESCE(SUM(tokens),0), COALESCE(SUM(saved),0),
		SUM(CASE WHEN consumer LIKE 'Сёма-чат%' OR saved > 0 OR detail LIKE '%не собирались%' OR detail LIKE '%вместо перечитывания%' THEN 1 ELSE 0 END)
		FROM memory_log WHERE action = 'recall' AND at >= ? GROUP BY consumer ORDER BY 3 DESC`, since)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []MemoryLogUse
	for rows.Next() {
		var u MemoryLogUse
		var measured int
		if err := rows.Scan(&u.Consumer, &u.Calls, &u.Tokens, &u.Saved, &measured); err == nil {
			u.Measured = measured > 0
			out = append(out, u)
		}
	}
	return out
}

func (s *Store) MemoryLogTotals(since string) (tokens, saved, denied int) {
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(tokens),0), COALESCE(SUM(saved),0),
		COALESCE(SUM(CASE WHEN granted = 0 THEN 1 ELSE 0 END),0) FROM memory_log WHERE at >= ?`, since).
		Scan(&tokens, &saved, &denied)
	return
}

// CountMemoriesSince — сколько фактов и уроков появилось с момента: для
// строки «Сёма сделал за окно».
func (s *Store) CountMemoriesSince(since time.Time) map[string]int {
	out := map[string]int{}
	rows, err := s.db.Query(`SELECT kind, COUNT(*) FROM memories WHERE created >= ? AND replaced = 0 GROUP BY kind`, since.Format(time.RFC3339))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		if rows.Scan(&kind, &n) == nil {
			out[kind] = n
		}
	}
	return out
}

// OrphanSessions — сессии, чей потребитель пропал (удалили или слили).
// Такие невидимы для всех обходов и висят вечно вместе со своей сводкой.
func (s *Store) OrphanSessions() ([]*MemorySession, error) {
	rows, err := s.db.Query(`SELECT s.id, s.consumer_id, s.agent, s.model, s.started, s.ended, s.summary, s.crystal
		FROM memory_sessions s LEFT JOIN memory_consumers c ON c.id = s.consumer_id
		WHERE c.id IS NULL ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemorySession
	for rows.Next() {
		m := &MemorySession{}
		if err := rows.Scan(&m.ID, &m.ConsumerID, &m.Agent, &m.Model, &m.Started, &m.Ended, &m.Summary, &m.Crystal); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MoveSession переприкрепляет сессию к другому потребителю.
func (s *Store) MoveSession(id, consumerID int64) error {
	_, err := s.db.Exec(`UPDATE memory_sessions SET consumer_id = ? WHERE id = ?`, consumerID, id)
	return err
}

// CountMemoryGraph — размер графа без самого графа: экрану нужно только
// число, а узлы весят десятки килобайт.
func (s *Store) CountMemoryGraph() (nodes, edges int) {
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM memory_nodes`).Scan(&nodes)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM memory_edges`).Scan(&edges)
	return nodes, edges
}

// ---- Координата: к какому месту в коде относится запись ----

// Place — где живёт знание. Символ важнее строки: строки съезжают от
// любой правки, а имя функции переживает и переносы, и переименования
// файлов. Строка хранится подсказкой, не опорой.
type Place struct {
	MemoryID int64
	Repo     string
	// Dir — папка или пакет: четвёртый уровень привязки, самый общий.
	// Принцип пакета экранов приходит к любому файлу внутри него, после
	// того, что привязано к самому файлу и символу
	Dir    string
	File   string
	Symbol string
	Line   int
	Added  string
	// Источник — откуда пришла связь: «урок», «требование <изменение>»,
	// «скилл», «чат <номер>». Человеку важно, кто это утверждает: урок из
	// работы и требование спеки весят по-разному.
	Source string
	// Сомнительна — символа по этой координате больше нет. Заметка не
	// теряется: место могло переехать, а знание остаться верным.
	Doubtful bool
	Verified string
}

// BindToPlace — связать запись памяти с местом в коде. Повтор той же
// привязки не двоится: ключ — запись плюс место.
func (s *Store) BindToPlace(place Place) error {
	place.Dir = cleanDir(place.Dir)
	if place.MemoryID == 0 || (place.File == "" && place.Symbol == "" && place.Dir == "") {
		return fmt.Errorf("привязка без места: нужен файл, символ или папка")
	}
	_, err := s.db.Exec(`INSERT INTO memory_places (memory_id, repo, dir, file, symbol, line, added, source)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(memory_id, repo, dir, file, symbol) DO UPDATE SET
			line = excluded.line, source = excluded.source, doubtful = 0`,
		place.MemoryID, place.Repo, place.Dir, place.File, place.Symbol, place.Line, time.Now().Format(time.RFC3339), place.Source)
	return err
}

// cleanDir — папка без ведущих «./» и хвостовых «/»: «internal/api/»,
// «./internal/api» и «internal/api» — одно место.
func cleanDir(dir string) string {
	dir = strings.TrimSpace(dir)
	dir = strings.TrimPrefix(dir, "./")
	return strings.Trim(dir, "/")
}

// Where — место словами для человека и журнала.
func (p Place) Where() string {
	switch {
	case p.Symbol != "" && p.File != "":
		return p.Symbol + " (" + p.File + ")"
	case p.Symbol != "":
		return p.Symbol
	case p.File != "":
		return p.File
	case p.Dir != "":
		return p.Dir + "/"
	}
	return ""
}

// MovePlaces — отдать координаты новой записи. Знание переживает и
// переписывание понятнее, и слияние дублей, и правку человеком, а место в
// коде у него одно: без переноса урок остаётся жив, но перестаёт отвечать
// на вопрос «что здесь уже ломалось». У старой записи привязка остаётся —
// это её история, а не потеря.
func (s *Store) MovePlaces(fromMemory, toMemory int64) error {
	if fromMemory == 0 || toMemory == 0 || fromMemory == toMemory {
		return nil
	}
	for _, place := range s.MemoryPlaces(fromMemory) {
		place.MemoryID = toMemory
		if err := s.BindToPlace(place); err != nil {
			return err
		}
	}
	return nil
}

// MemoryPlaces — куда привязана запись.
func (s *Store) MemoryPlaces(memoryID int64) []Place {
	rows, err := s.db.Query(`SELECT memory_id, repo, dir, file, symbol, line, added, source, doubtful, checked
		FROM memory_places WHERE memory_id = ? ORDER BY symbol <> '' DESC, file <> '' DESC, dir`, memoryID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanPlaces(rows)
}

// scanPlaces — строки мест в список; битая строка пропускается, а не
// роняет весь список.
func scanPlaces(rows *sql.Rows) []Place {
	var out []Place
	for rows.Next() {
		var place Place
		var doubtful int
		if rows.Scan(&place.MemoryID, &place.Repo, &place.Dir, &place.File, &place.Symbol, &place.Line, &place.Added,
			&place.Source, &doubtful, &place.Verified) == nil {
			place.Doubtful = doubtful == 1
			out = append(out, place)
		}
	}
	return out
}

// MarkDoubtful — символа по этой координате больше нет. Заметку не
// теряем: место могло переехать, а знание остаться верным. Решение —
// человеку или разбору памяти.
func (s *Store) MarkDoubtful(memoryID int64, symbol string, doubtful bool) error {
	v := 0
	if doubtful {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE memory_places SET doubtful = ?, checked = ?
		WHERE memory_id = ? AND symbol = ?`, v, time.Now().Format(time.RFC3339), memoryID, symbol)
	return err
}

// MarkDirDoubtful — папки по этой координате больше нет (или снова есть).
// Та же мысль, что у символа: место могло переехать, знание осталось.
func (s *Store) MarkDirDoubtful(memoryID int64, dir string, doubtful bool) error {
	v := 0
	if doubtful {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE memory_places SET doubtful = ?, checked = ?
		WHERE memory_id = ? AND dir = ? AND file = '' AND symbol = ''`,
		v, time.Now().Format(time.RFC3339), memoryID, cleanDir(dir))
	return err
}

// PlacesToVerify — координаты, которые давно не сверяли с кодом. Уже
// сомнительные не берём: их судьбу решает разбор по сроку.
func (s *Store) PlacesToVerify(olderThan time.Time, limit int) []Place {
	if limit <= 0 {
		limit = 10
	}
	// символ и папка сверяются с кодом; привязка только к файлу — нет:
	// файл переезжает чаще, чем символ, и его отсутствие мало что значит
	rows, err := s.db.Query(`SELECT p.memory_id, p.repo, p.dir, p.file, p.symbol, p.line, p.added, p.source, p.doubtful, p.checked
		FROM memory_places p JOIN memories m ON m.id = p.memory_id
		WHERE p.doubtful = 0 AND (p.symbol <> '' OR (p.dir <> '' AND p.file = '')) AND m.replaced = 0
		  AND (p.checked = '' OR p.checked < ?)
		ORDER BY p.checked LIMIT ?`, olderThan.Format(time.RFC3339), limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanPlaces(rows)
}

// ConsumerRoots — рабочие папки агентов разработки: по ним житель
// машины ищет файл, на который показывает координата. Сама Сёма файлов
// репозитория не видит — она в контейнере.
func (s *Store) ConsumerRoots(limit int) []string {
	rows, err := s.db.Query(`SELECT DISTINCT path FROM memory_consumers
		WHERE kind = 'mcp' AND path <> '' ORDER BY seen DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// DoubtfulPlaces — привязки к местам, которых больше нет. Разбор памяти
// предлагает их человеку: забыть заметку или поправить координату.
func (s *Store) DoubtfulPlaces(limit int) []Place {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT memory_id, repo, dir, file, symbol, line, added, source, doubtful, checked
		FROM memory_places WHERE doubtful = 1 ORDER BY checked DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanPlaces(rows)
}

// UnbindFromPlace — снять координату. Само знание остаётся: устарела
// привязка, а не урок.
func (s *Store) UnbindFromPlace(memoryID int64, repo, file, symbol string) error {
	_, err := s.db.Exec(`DELETE FROM memory_places
		WHERE memory_id = ? AND repo = ? AND file = ? AND symbol = ?`, memoryID, repo, file, symbol)
	return err
}

// UnbindPlace — снять ровно эту привязку, с папкой в ключе.
func (s *Store) UnbindPlace(p Place) error {
	_, err := s.db.Exec(`DELETE FROM memory_places
		WHERE memory_id = ? AND repo = ? AND dir = ? AND file = ? AND symbol = ?`,
		p.MemoryID, p.Repo, cleanDir(p.Dir), p.File, p.Symbol)
	return err
}

// PlaceKnowledge — что мы знаем про это место в коде. Ищется и по файлу, и
// по символу: знание про функцию переживает переезд файла, а знание про
// файл остаётся, даже если функцию переименовали.
func (s *Store) PlaceKnowledge(repo, file, symbol string, limit int) ([]*Memory, error) {
	known, err := s.PlacesKnowledge(repo, []PlaceQuery{{File: file, Symbol: symbol}}, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Memory, 0, len(known))
	for _, k := range known {
		out = append(out, k.Memory)
	}
	return out, nil
}

// PlaceQuery — место, про которое спрашивают: папка, файл, символ в
// файле. Карточка оглавления называет сразу все свои символы — один
// запрос на карточку, а не сорок.
type PlaceQuery struct {
	Dir    string `json:"dir,omitempty"`
	File   string `json:"file,omitempty"`
	Symbol string `json:"symbol,omitempty"`
}

// Уровни привязки: точное раньше общего.
const (
	LevelSymbol = 0
	LevelFile   = 1
	LevelDir    = 2
)

// PlaceKnown — запись памяти и то, каким местом она сюда попала.
type PlaceKnown struct {
	*Memory
	Level int   // LevelSymbol, LevelFile, LevelDir
	Via   Place // привязка, по которой нашлось
}

// PlacesKnowledge — что известно про набор мест одним запросом. Порядок:
// символ → файл → папка, внутри уровня — подтверждённое и свежее раньше.
// Папка ищется до ближайшей заведённой: у файла `internal/api/inbox.go`
// при привязках к `internal/api` и `internal` приходит только первая —
// иначе принцип про `internal/` ехал бы к каждому файлу сервера.
// Запись, попавшая по двум местам, считается один раз, по лучшему.
func (s *Store) PlacesKnowledge(repo string, places []PlaceQuery, limit int) ([]PlaceKnown, error) {
	if limit <= 0 {
		limit = 10
	}
	var files, symbols []string
	dirs := map[string]bool{}
	for _, q := range places {
		if q.File != "" {
			files = append(files, q.File)
			dirs[cleanDir(path.Dir(q.File))] = true
		}
		if q.Symbol != "" {
			symbols = append(symbols, q.Symbol)
		}
		if d := cleanDir(q.Dir); d != "" {
			dirs[d] = true
		}
	}
	nearest, err := s.nearestDirs(repo, dirs)
	if err != nil {
		return nil, err
	}
	cols := "m." + strings.ReplaceAll(memoryCols, ", ", ", m.")
	var out []PlaceKnown
	seen := map[int64]bool{}
	ask := func(level int, column string, values []string) error {
		if len(values) == 0 {
			return nil
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
		args := make([]any, 0, len(values)+3)
		for _, v := range values {
			args = append(args, v)
		}
		args = append(args, repo, repo)
		rows, err := s.db.Query(`SELECT `+cols+`, p.memory_id, p.repo, p.dir, p.file, p.symbol, p.line, p.added, p.source, p.doubtful, p.checked
			FROM memories m JOIN memory_places p ON p.memory_id = m.id
			WHERE m.replaced = 0 AND m.status = 'ok' AND p.`+column+` IN (`+marks+`)
			  AND (? = '' OR p.repo = '' OR p.repo = ?)
			ORDER BY m.confirmed DESC, m.id DESC`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Memory
			var pl Place
			var conf, graphed, doubtful int
			var sources string
			if err := rows.Scan(&m.ID, &m.Kind, &m.Area, &m.Text, &m.Status, &m.SessionID, &m.Agent, &m.Model, &m.Created,
				&m.Supersedes, &conf, &m.Weight, &sources, &m.Expires, &graphed,
				&pl.MemoryID, &pl.Repo, &pl.Dir, &pl.File, &pl.Symbol, &pl.Line, &pl.Added, &pl.Source, &doubtful, &pl.Verified); err != nil {
				return err
			}
			if seen[m.ID] {
				continue
			}
			// символ ищется в паре с файлом, когда файл назван: одноимённая
			// функция другого файла — чужое знание
			if level == LevelSymbol && pl.File != "" && len(files) > 0 && !hasString(files, pl.File) && !hasString(symbols, pl.Symbol) {
				continue
			}
			seen[m.ID] = true
			m.Confirmed, m.Graphed, pl.Doubtful = conf == 1, graphed == 1, doubtful == 1
			_ = json.Unmarshal([]byte(sources), &m.Sources)
			mm := m
			out = append(out, PlaceKnown{Memory: &mm, Level: level, Via: pl})
		}
		return rows.Err()
	}
	if err := ask(LevelSymbol, "symbol", symbols); err != nil {
		return nil, err
	}
	if err := ask(LevelFile, "file", files); err != nil {
		return nil, err
	}
	if err := ask(LevelDir, "dir", nearest); err != nil {
		return nil, err
	}
	// привязка к папке приходит только без файла и символа: строка с
	// файлом уже пришла своим уровнем
	kept := out[:0]
	for _, k := range out {
		if k.Level == LevelDir && (k.Via.File != "" || k.Via.Symbol != "") {
			delete(seen, k.ID)
			continue
		}
		kept = append(kept, k)
	}
	out = kept
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// nearestDirs — для каждой спрошенной папки ближайшая заведённая: сама
// она или ближайший родитель, у которого есть привязки.
func (s *Store) nearestDirs(repo string, dirs map[string]bool) ([]string, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT DISTINCT dir FROM memory_places
		WHERE dir <> '' AND file = '' AND symbol = '' AND (? = '' OR repo = '' OR repo = ?)`, repo, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bound := map[string]bool{}
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil {
			bound[d] = true
		}
	}
	var out []string
	picked := map[string]bool{}
	for d := range dirs {
		for cur := d; cur != "" && cur != "."; cur = parentDir(cur) {
			if bound[cur] {
				if !picked[cur] {
					picked[cur] = true
					out = append(out, cur)
				}
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// parentDir — родитель относительной папки; у верхней — пусто.
func parentDir(dir string) string {
	i := strings.LastIndex(dir, "/")
	if i < 0 {
		return ""
	}
	return dir[:i]
}

// hasString — есть ли строка в списке.
func hasString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// AgePlace — только для тестов: сдвинуть время проверки назад,
// чтобы проверить снятие протухшей координаты, не ожидая месяцами.
func (s *Store) AgePlace(memoryID int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE memory_places SET checked = ? WHERE memory_id = ?`,
		at.Format(time.RFC3339), memoryID)
	return err
}

// migratePlaceDir — папка как четвёртый уровень привязки (7 октября 2026).
// Колонка входит в ключ, а ключ у SQLite не меняется, поэтому таблица
// пересобирается: новая рядом, перенос строк, замена. На таблицу никто
// не ссылается через REFERENCES, переименование безопасно.
func (s *Store) migratePlaceDir() error {
	rows, err := s.db.Query(`PRAGMA table_info(memory_places)`)
	if err != nil {
		return err
	}
	hasDir := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var def sql.NullString
		if rows.Scan(&cid, &name, &typ, &notnull, &def, &pk) == nil && name == "dir" {
			hasDir = true
		}
	}
	rows.Close()
	if hasDir {
		_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_places_dir ON memory_places(repo, dir)`)
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE memory_places_new (
			memory_id INTEGER NOT NULL,
			repo      TEXT NOT NULL DEFAULT '',
			dir       TEXT NOT NULL DEFAULT '',
			file      TEXT NOT NULL DEFAULT '',
			symbol    TEXT NOT NULL DEFAULT '',
			line      INTEGER NOT NULL DEFAULT 0,
			added     TEXT NOT NULL,
			source    TEXT NOT NULL DEFAULT '',
			doubtful  INTEGER NOT NULL DEFAULT 0,
			checked   TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (memory_id, repo, dir, file, symbol)
		)`,
		`INSERT INTO memory_places_new (memory_id, repo, dir, file, symbol, line, added, source, doubtful, checked)
			SELECT memory_id, repo, '', file, symbol, line, added, source, doubtful, checked FROM memory_places`,
		`DROP TABLE memory_places`,
		`ALTER TABLE memory_places_new RENAME TO memory_places`,
		`CREATE INDEX IF NOT EXISTS idx_memory_places_file ON memory_places(repo, file)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_places_symbol ON memory_places(symbol)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_places_dir ON memory_places(repo, dir)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PlacesBySource — привязки с таким источником или его приставкой: по
// «CLAUDE.md · » находятся все принципы проекта, по полному источнику —
// привязки одного абзаца. Только живые записи.
func (s *Store) PlacesBySource(prefix string) []Place {
	rows, err := s.db.Query(`SELECT p.memory_id, p.repo, p.dir, p.file, p.symbol, p.line, p.added, p.source, p.doubtful, p.checked
		FROM memory_places p JOIN memories m ON m.id = p.memory_id
		WHERE m.replaced = 0 AND p.source LIKE ? || '%' ORDER BY p.memory_id, p.dir, p.file`, prefix)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanPlaces(rows)
}

// MarkPlacesDoubtful — все привязки записи разом: абзац исчез из
// инструкций, и каждое его место устарело.
func (s *Store) MarkPlacesDoubtful(memoryID int64, doubtful bool) error {
	v := 0
	if doubtful {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE memory_places SET doubtful = ?, checked = ? WHERE memory_id = ?`,
		v, time.Now().Format(time.RFC3339), memoryID)
	return err
}
