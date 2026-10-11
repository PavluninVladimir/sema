package store

import (
	"database/sql"
	"strings"
	"time"
)

// Поручения: работа, которую Сёма доводит до конца сам, шаг за шагом. 4
// октября 2026 модуль «Дневник» остался заглушкой — за одно обращение
// интеграцию не написать, а «потом» у Сёмы не существовало. Поручение —
// запись, а не настроение чата: цель, шаги с итогами, ожидание человека и
// задача во внешнем трекере живут в базе и переживают перезапуск.

type Assignment struct {
	ID             int64
	Chat           int64  // чат, где просили и где идёт работа
	Goal           string // цель одной строкой
	State          string // AssignActive, …
	StepTries      int    // ходов на текущем шаге без его итога
	Question       string // что спросили у человека, пока ждём
	WaitSince      string // с какого времени ждём человека
	WaitMsg        int64  // последнее сообщение в переписке, когда начали ждать: ответ — то, что новее
	WaitSecrets    string // каких секретов ждём, через запятую
	WakeAt         string // человек отложил: в это время поручение само вернётся в работу
	Summary        string // итог закрытого
	Tracker        string // сервер MCP трекера задач
	TrackerTask    string // номер задачи в трекере
	TrackerPending int64  // вызов создания или закрытия, ждущий кнопки
	TrackerProject string // проект трекера, названный в плане
	Turns          int    // ходов ядра всего
	Since          string // с какого времени поручение идёт само: срок считается от него, ответ человека сдвигает
	LastTurn       string
	Created        string
	Updated        string
	Closed         string
	Steps          []AssignStep
}

type AssignStep struct {
	N      int
	Text   string
	State  string // StepTodo | StepDone | StepSkipped
	Result string
	DoneAt string
}

const (
	AssignDraft     = "draft"     // ждёт «Начать» (настройка запуска — кнопкой)
	AssignActive    = "active"    // идёт
	AssignPaused    = "paused"    // остановлено человеком
	AssignWaiting   = "waiting"   // ждёт человека: вопрос или секрет
	AssignDone      = "done"      // закрыто итогом
	AssignCancelled = "cancelled" // отменено человеком

	StepTodo    = "todo"
	StepDone    = "done"
	StepSkipped = "skipped"
)

// Open — поручение ещё не закончено: его шаги и вопросы имеют смысл.
func (a *Assignment) Open() bool {
	switch a.State {
	case AssignDraft, AssignActive, AssignPaused, AssignWaiting:
		return true
	}
	return false
}

// Current — шаг, который делается сейчас; nil — несделанных нет.
func (a *Assignment) Current() *AssignStep {
	for i := range a.Steps {
		if a.Steps[i].State == StepTodo {
			return &a.Steps[i]
		}
	}
	return nil
}

// Progress — сколько шагов закрыто и сколько всего.
func (a *Assignment) Progress() (done, total int) {
	for _, st := range a.Steps {
		if st.State != StepTodo {
			done++
		}
	}
	return done, len(a.Steps)
}

func (s *Store) migrateAssignments() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS assignments (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			chat            INTEGER NOT NULL,
			goal            TEXT NOT NULL,
			state           TEXT NOT NULL DEFAULT 'active',
			step_tries      INTEGER NOT NULL DEFAULT 0,
			question        TEXT NOT NULL DEFAULT '',
			wait_since      TEXT NOT NULL DEFAULT '',
			wait_msg        INTEGER NOT NULL DEFAULT 0,
			wait_secrets    TEXT NOT NULL DEFAULT '',
			wake_at         TEXT NOT NULL DEFAULT '',
			summary         TEXT NOT NULL DEFAULT '',
			tracker         TEXT NOT NULL DEFAULT '',
			tracker_task    TEXT NOT NULL DEFAULT '',
			tracker_pending INTEGER NOT NULL DEFAULT 0,
			tracker_project TEXT NOT NULL DEFAULT '',
			turns           INTEGER NOT NULL DEFAULT 0,
			since           TEXT NOT NULL DEFAULT '',
			last_turn       TEXT NOT NULL DEFAULT '',
			created         TEXT NOT NULL,
			updated         TEXT NOT NULL,
			closed          TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_assignments_chat ON assignments(chat, state)`,
		`CREATE TABLE IF NOT EXISTS assignment_steps (
			assignment INTEGER NOT NULL,
			n          INTEGER NOT NULL,
			text       TEXT NOT NULL,
			state      TEXT NOT NULL DEFAULT 'todo',
			result     TEXT NOT NULL DEFAULT '',
			done_at    TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (assignment, n)
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// столбцы, которых не было у первой таблицы (живая база с 4 октября 2026)
	_, _ = s.db.Exec(`ALTER TABLE assignments ADD COLUMN wake_at TEXT NOT NULL DEFAULT ''`)
	return nil
}

const assignCols = `id, chat, goal, state, step_tries, question, wait_since, wait_msg, wait_secrets, wake_at, summary,
	tracker, tracker_task, tracker_pending, tracker_project, turns, since, last_turn, created, updated, closed`

func scanAssignment(row interface{ Scan(...any) error }) (*Assignment, error) {
	var a Assignment
	err := row.Scan(&a.ID, &a.Chat, &a.Goal, &a.State, &a.StepTries, &a.Question, &a.WaitSince, &a.WaitMsg, &a.WaitSecrets, &a.WakeAt,
		&a.Summary, &a.Tracker, &a.TrackerTask, &a.TrackerPending, &a.TrackerProject, &a.Turns, &a.Since, &a.LastTurn,
		&a.Created, &a.Updated, &a.Closed)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) assignSteps(id int64) ([]AssignStep, error) {
	rows, err := s.db.Query(`SELECT n, text, state, result, done_at FROM assignment_steps
		WHERE assignment = ? ORDER BY n`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AssignStep
	for rows.Next() {
		var st AssignStep
		if err := rows.Scan(&st.N, &st.Text, &st.State, &st.Result, &st.DoneAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// AddAssignment — новое поручение с шагами по порядку.
func (s *Store) AddAssignment(a *Assignment, steps []string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if a.State == "" {
		a.State = AssignActive
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`INSERT INTO assignments (chat, goal, state, tracker_task, tracker_project, since, created, updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, a.Chat, a.Goal, a.State, a.TrackerTask, a.TrackerProject, now, now, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for i, text := range steps {
		if _, err := tx.Exec(`INSERT INTO assignment_steps (assignment, n, text) VALUES (?, ?, ?)`, id, i+1, text); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	a.ID, a.Since, a.Created, a.Updated = id, now, now, now
	s.touch(ScreenChat(a.Chat), ScreenTasks)
	return id, nil
}

// Assignment — поручение со всеми шагами.
func (s *Store) Assignment(id int64) (*Assignment, bool) {
	a, err := scanAssignment(s.db.QueryRow(`SELECT `+assignCols+` FROM assignments WHERE id = ?`, id))
	if err != nil {
		return nil, false
	}
	if a.Steps, err = s.assignSteps(id); err != nil {
		return nil, false
	}
	return a, true
}

// OpenAssignmentOf — незаконченное поручение чата (последнее, если вдруг их
// несколько).
func (s *Store) OpenAssignmentOf(chat int64) (*Assignment, bool) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM assignments WHERE chat = ? AND state IN (?, ?, ?, ?)
		ORDER BY id DESC LIMIT 1`, chat, AssignDraft, AssignActive, AssignPaused, AssignWaiting).Scan(&id)
	if err != nil {
		return nil, false
	}
	return s.Assignment(id)
}

// Assignments — поручения в названных состояниях (пусто — все), свежие
// первыми, не больше limit.
func (s *Store) Assignments(limit int, states ...string) ([]Assignment, error) {
	q := `SELECT ` + assignCols + ` FROM assignments`
	var args []any
	if len(states) > 0 {
		q += ` WHERE state IN (?` + strings.Repeat(`, ?`, len(states)-1) + `)`
		for _, st := range states {
			args = append(args, st)
		}
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var out []Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Steps, err = s.assignSteps(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AssignmentByTrackerPending — поручение, чей вызов трекера ждёт кнопки.
func (s *Store) AssignmentByTrackerPending(pending int64) (*Assignment, bool) {
	if pending <= 0 {
		return nil, false
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM assignments WHERE tracker_pending = ?`, pending).Scan(&id); err != nil {
		return nil, false
	}
	return s.Assignment(id)
}

// SaveAssignment — записывает поля поручения (шаги — своими методами).
func (s *Store) SaveAssignment(a *Assignment) error {
	a.Updated = time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE assignments SET goal = ?, state = ?, step_tries = ?, question = ?, wait_since = ?,
		wait_msg = ?, wait_secrets = ?, wake_at = ?, summary = ?, tracker = ?, tracker_task = ?, tracker_pending = ?, tracker_project = ?,
		turns = ?, since = ?, last_turn = ?, updated = ?, closed = ? WHERE id = ?`,
		a.Goal, a.State, a.StepTries, a.Question, a.WaitSince, a.WaitMsg, a.WaitSecrets, a.WakeAt, a.Summary, a.Tracker, a.TrackerTask,
		a.TrackerPending, a.TrackerProject, a.Turns, a.Since, a.LastTurn, a.Updated, a.Closed, a.ID)
	if err == nil {
		s.touch(ScreenChat(a.Chat), ScreenTasks)
	}
	return err
}

// FinishAssignStep — шаг закрыт: сделан или пропущен, с итогом.
func (s *Store) FinishAssignStep(id int64, n int, state, result string) error {
	res, err := s.db.Exec(`UPDATE assignment_steps SET state = ?, result = ?, done_at = ? WHERE assignment = ? AND n = ?`,
		state, result, time.Now().UTC().Format(time.RFC3339), id, n)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return sql.ErrNoRows
	}
	s.touchAssignment(id)
	return nil
}

// ReopenAssignStep — шаг снова не сделан: поручение вернули в работу.
func (s *Store) ReopenAssignStep(id int64, n int) error {
	if _, err := s.db.Exec(`UPDATE assignment_steps SET state = ?, result = '', done_at = '' WHERE assignment = ? AND n = ?`,
		StepTodo, id, n); err != nil {
		return err
	}
	s.touchAssignment(id)
	return nil
}

// ReplaceAssignSteps — новый план для несделанного: закрытые шаги остаются
// со своими номерами, несделанные заменяются новыми следом за ними.
func (s *Store) ReplaceAssignSteps(id int64, steps []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM assignment_steps WHERE assignment = ? AND state = ?`, id, StepTodo); err != nil {
		return err
	}
	var last int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(n), 0) FROM assignment_steps WHERE assignment = ?`, id).Scan(&last); err != nil {
		return err
	}
	for i, text := range steps {
		if _, err := tx.Exec(`INSERT INTO assignment_steps (assignment, n, text) VALUES (?, ?, ?)`, id, last+i+1, text); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.touchAssignment(id)
	return nil
}

// LastChatMsgID — номер самого свежего сообщения во всей переписке: от
// него поручение отсчитывает «человек ответил после вопроса». Время для
// этого не годится — оно в базе с точностью до секунды.
func (s *Store) LastChatMsgID() int64 {
	var id int64
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM chat_msgs`).Scan(&id)
	return id
}

func (s *Store) touchAssignment(id int64) {
	var chat int64
	if s.db.QueryRow(`SELECT chat FROM assignments WHERE id = ?`, id).Scan(&chat) == nil {
		s.touch(ScreenChat(chat), ScreenTasks)
	}
}
