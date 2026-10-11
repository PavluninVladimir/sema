package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Очередь фоновой работы. Всё, что Сёма делает не сразу, стоит здесь одной
// строкой: вид работы, объект, приоритет, попытки, «не раньше чем». Пара
// «вид и объект» уникальна среди незавершённых, поэтому одна и та же работа
// не попадает в очередь дважды.

type WorkItem struct {
	ID        int64
	Kind      string
	Ref       string
	Payload   string
	Priority  int
	State     string // waiting | running | done | held
	Attempts  int
	LastErr   string
	NotBefore string
	Lease     string // до какого времени работа занята исполнителем
	Created   string
	Updated   string
}

const (
	WorkWaiting = "waiting"
	WorkRunning = "running"
	WorkDone    = "done"
	WorkHeld    = "held" // застряло: попытки кончились, ждёт человека
)

func (s *Store) migrateQueue() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS work_queue (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			kind       TEXT NOT NULL,
			ref        TEXT NOT NULL DEFAULT '',
			payload    TEXT NOT NULL DEFAULT '',
			priority   INTEGER NOT NULL DEFAULT 5,
			state      TEXT NOT NULL DEFAULT 'waiting',
			attempts   INTEGER NOT NULL DEFAULT 0,
			last_err   TEXT NOT NULL DEFAULT '',
			not_before TEXT NOT NULL DEFAULT '',
			lease      TEXT NOT NULL DEFAULT '',
			created    TEXT NOT NULL,
			updated    TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_work_queue_one ON work_queue(kind, ref) WHERE state <> 'done'`,
		// откладываний тоже надо считать: причина, которая не проходит сама
		// («роли нужна доверенная модель»), иначе крутит работу вечно и
		// молча — очередь растёт, а человек об этом не знает
		`ALTER TABLE work_queue ADD COLUMN postponed INTEGER NOT NULL DEFAULT 0`,
		// след работы: что стало другим у объекта, когда работа кончилась.
		// По нему наблюдатель отличает сделанную работу от потраченной
		`ALTER TABLE work_queue ADD COLUMN trace TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_work_queue_take ON work_queue(state, priority DESC, id)`,
		// сводка очереди по видам на «Здоровье» и «Очереди» — без индекса
		// это полный скан на каждый показ
		`CREATE INDEX IF NOT EXISTS idx_work_queue_kind ON work_queue(kind, state)`,
		`CREATE INDEX IF NOT EXISTS idx_work_queue_trace ON work_queue(state, updated) WHERE trace <> ''`,
		// Внешние действия: отметка до и после, чтобы повтор после убийства
		// процесса не сделал дважды перенос письма или команду на сервере
		`CREATE TABLE IF NOT EXISTS outside_acts (
			key      TEXT PRIMARY KEY,
			state    TEXT NOT NULL DEFAULT 'doing',
			started  TEXT NOT NULL,
			finished TEXT NOT NULL DEFAULT '',
			note     TEXT NOT NULL DEFAULT ''
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue // миграция идёт на каждом старте
			}
			return fmt.Errorf("очередь: %w", err)
		}
	}
	return nil
}

const workCols = `id, kind, ref, payload, priority, state, attempts, last_err, not_before, lease, created, updated`

func scanWork(r rowScanner) (*WorkItem, error) {
	w := &WorkItem{}
	err := r.Scan(&w.ID, &w.Kind, &w.Ref, &w.Payload, &w.Priority, &w.State, &w.Attempts, &w.LastErr, &w.NotBefore, &w.Lease, &w.Created, &w.Updated)
	return w, err
}

// Enqueue ставит работу; та же работа уже в очереди — поднимаем приоритет и
// снимаем паузу, но второй строки не создаём.
func (s *Store) Enqueue(kind, ref, payload string, priority int) error {
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`UPDATE work_queue SET priority = MAX(priority, ?), not_before = '', updated = ?,
		state = CASE WHEN state = 'held' THEN 'held' ELSE 'waiting' END
		WHERE kind = ? AND ref = ? AND state <> 'done' AND state <> 'running'`, priority, now, kind, ref)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
	}
	_, err = s.db.Exec(`INSERT INTO work_queue (kind, ref, payload, priority, state, created, updated)
		VALUES (?, ?, ?, ?, 'waiting', ?, ?)`, kind, ref, payload, priority, now, now)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil // работа уже стоит: гонка постановки, не беда
	}
	if err == nil {
		s.touch(ScreenQueue)
	}
	return err
}

// TakeWork берёт работы в аренду: пока аренда не истекла, их никто не возьмёт.
func (s *Store) TakeWork(limit int, lease time.Duration) ([]*WorkItem, error) {
	now := time.Now()
	nowStr := now.Format(time.RFC3339)
	// у аренды точность до наносекунды, а не до секунды, как у остальных
	// времён: сравнение «срок вышел» должно быть точным, иначе работа
	// возвращается в очередь на секунду позже или раньше, чем обещано
	nowNano := now.Format(time.RFC3339Nano)
	// брошенные аренды (сервер перезапустили или убили) возвращаются в
	// очередь сами, без участия того, кто их брал: он мог уже не жить
	_, _ = s.db.Exec(`UPDATE work_queue SET state = 'waiting', lease = '' WHERE state = 'running' AND lease < ?`, nowNano)
	rows, err := s.db.Query(`SELECT `+workCols+` FROM work_queue
		WHERE state = 'waiting' AND (not_before = '' OR not_before <= ?)
		ORDER BY priority DESC, id LIMIT ?`, nowStr, limit)
	if err != nil {
		return nil, err
	}
	var out []*WorkItem
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, w)
	}
	rows.Close()
	until := now.Add(lease).Format(time.RFC3339Nano)
	// работа отдаётся только тому, кто её действительно забрал: без проверки
	// два одновременных прохода получали одну строку и делали работу дважды
	taken := out[:0]
	for _, w := range out {
		res, err := s.db.Exec(`UPDATE work_queue SET state = 'running', lease = ?, updated = ? WHERE id = ? AND state = 'waiting'`,
			until, nowStr, w.ID)
		if err != nil {
			return taken, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // забрал кто-то другой
		}
		taken = append(taken, w)
	}
	return taken, nil
}

// RememberTrace — отпечаток объекта на момент, когда работа кончилась.
//
// Лежит у самой работы, а не отдельной таблицей: «сколько работ подряд
// оставили один и тот же отпечаток» — это один сводный запрос по очереди,
// которая и так есть. Пусто — вид работы следа не объявил либо объект пропал;
// такие в счёт петель не идут.
func (s *Store) RememberTrace(id int64, trace string) error {
	_, err := s.db.Exec(`UPDATE work_queue SET trace = ? WHERE id = ?`, trace, id)
	return err
}

// RepeatsWithoutTrace — работы одного вида по одному объекту, оставившие один и
// тот же отпечаток.
//
// Возвращает только то, что перешло порог: вид, объект, сколько раз, с
// какого времени. Повтор со сменой отпечатка бедой не считается — работа
// делала работу.
func (s *Store) RepeatsWithoutTrace(threshold int, since time.Time) ([]Repeat, error) {
	rows, err := s.db.Query(`
SELECT kind, ref, COUNT(*) n, MIN(updated), MAX(updated)
FROM work_queue
WHERE state = 'done' AND trace <> '' AND updated >= ?
GROUP BY kind, ref, trace
HAVING n >= ?
ORDER BY n DESC`, since.Format(time.RFC3339), threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repeat
	for rows.Next() {
		var rep Repeat
		var first, last string
		if err := rows.Scan(&rep.Kind, &rep.Ref, &rep.Count, &first, &last); err != nil {
			return nil, err
		}
		rep.First, _ = time.Parse(time.RFC3339, first)
		rep.Last, _ = time.Parse(time.RFC3339, last)
		out = append(out, rep)
	}
	return out, rows.Err()
}

// Repeat — работа, которая делалась много раз и ничего не изменила.
type Repeat struct {
	Kind, Ref   string
	Count       int
	First, Last time.Time
}

func (s *Store) FinishWork(id int64) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE work_queue SET state = 'done', lease = '', last_err = '', updated = ? WHERE id = ?`, now, id)
	if err == nil {
		s.touch(ScreenQueue)
	}
	return err
}

// FailWork — попытка не удалась: пауза до notBefore, счётчик растёт; предел
// попыток переводит работу в «застряло».
func (s *Store) FailWork(id int64, errText string, notBefore time.Time, maxAttempts int) (held bool, err error) {
	now := time.Now().Format(time.RFC3339)
	w, ok := s.Work(id)
	if !ok {
		return false, fmt.Errorf("работы %d нет", id)
	}
	attempts := w.Attempts + 1
	state := WorkWaiting
	if attempts >= maxAttempts {
		state = WorkHeld
	}
	_, err = s.db.Exec(`UPDATE work_queue SET state = ?, attempts = ?, last_err = ?, not_before = ?, lease = '', updated = ? WHERE id = ?`,
		state, attempts, clipText(errText, 500), notBefore.Format(time.RFC3339), now, id)
	if err == nil {
		s.touch(ScreenQueue)
	}
	return state == WorkHeld, err
}

// PostponeWork — не вина работы: модель на передышке, бюджет исчерпан, час
// не тот. Попытка не тратится, но откладывание считается: причина, которая
// сама не пройдёт, должна однажды позвать человека, а не крутиться вечно.
// Возвращает, сколько раз работу уже откладывали.
func (s *Store) PostponeWork(id int64, notBefore time.Time, note string) error {
	_, err := s.PostponeWorkN(id, notBefore, note)
	return err
}

func (s *Store) PostponeWorkN(id int64, notBefore time.Time, note string) (int, error) {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE work_queue SET state = 'waiting', not_before = ?, last_err = ?, lease = '',
		postponed = postponed + 1, updated = ? WHERE id = ?`,
		notBefore.Format(time.RFC3339), clipText(note, 300), now, id)
	if err != nil {
		return 0, err
	}
	n := 0
	_ = s.db.QueryRow(`SELECT COALESCE(postponed, 0) FROM work_queue WHERE id = ?`, id).Scan(&n)
	return n, nil
}

// HoldWork — работа встала: причина не проходит сама, и её должен увидеть
// человек. От FailWork отличается тем, что попытки ни при чём.
func (s *Store) HoldWork(id int64, why string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE work_queue SET state = ?, last_err = ?, lease = '', updated = ? WHERE id = ?`,
		WorkHeld, clipText(why, 300), now, id)
	if err == nil {
		s.touch(ScreenQueue)
	}
	return err
}

func (s *Store) Work(id int64) (*WorkItem, bool) {
	w, err := scanWork(s.db.QueryRow(`SELECT `+workCols+` FROM work_queue WHERE id = ?`, id))
	return w, err == nil
}

// WorkList — работы одного состояния (для экрана и повторов).
func (s *Store) WorkList(kind, state string, limit int) ([]*WorkItem, error) {
	q := `SELECT ` + workCols + ` FROM work_queue WHERE state = ?`
	args := []any{state}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY priority DESC, id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WorkItem
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkStat — сводка по виду работы: сколько ждёт, идёт, застряло, сделано за час.
type WorkStat struct {
	Kind    string
	Waiting int
	Running int
	Held    int
	DoneHr  int
	Paused  int // ждут времени «не раньше»
}

func (s *Store) WorkStats() ([]WorkStat, error) {
	rows, err := s.db.Query(`SELECT kind,
		SUM(state = 'waiting' AND (not_before = '' OR not_before <= ?)),
		SUM(state = 'running'), SUM(state = 'held'),
		SUM(state = 'done' AND updated >= ?),
		SUM(state = 'waiting' AND not_before > ?)
		FROM work_queue GROUP BY kind ORDER BY kind`,
		time.Now().Format(time.RFC3339), time.Now().Add(-time.Hour).Format(time.RFC3339), time.Now().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkStat
	for rows.Next() {
		var st WorkStat
		var waiting, running, held, done, paused sql.NullInt64
		if err := rows.Scan(&st.Kind, &waiting, &running, &held, &done, &paused); err != nil {
			return nil, err
		}
		st.Waiting, st.Running, st.Held, st.DoneHr, st.Paused = int(waiting.Int64), int(running.Int64), int(held.Int64), int(done.Int64), int(paused.Int64)
		out = append(out, st)
	}
	return out, rows.Err()
}

// RetryHeld возвращает застрявшие работы в очередь: человек решил повторить.
func (s *Store) RetryHeld(kind string) (int64, error) {
	now := time.Now().Format(time.RFC3339)
	q := `UPDATE work_queue SET state = 'waiting', attempts = 0, not_before = '', updated = ? WHERE state = 'held'`
	args := []any{now}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DropWork — человек решил не делать эту работу.
func (s *Store) DropWork(kind string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM work_queue WHERE state = 'held' AND kind = ?`, kind)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BumpWork поднимает приоритет вида работы: «Ускорить» на экране.
func (s *Store) BumpWork(kind string) error {
	_, err := s.db.Exec(`UPDATE work_queue SET priority = 9, not_before = '', updated = ? WHERE kind = ? AND state = 'waiting'`,
		time.Now().Format(time.RFC3339), kind)
	return err
}

func (s *Store) PurgeWorkDone(before string) int64 {
	res, err := s.db.Exec(`DELETE FROM work_queue WHERE state = 'done' AND updated < ?`, before)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// HasWork — стоит ли уже такая работа (для сборщика).
func (s *Store) HasWork(kind, ref string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM work_queue WHERE kind = ? AND ref = ? AND state <> 'done'`, kind, ref).Scan(&n)
	return n > 0
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---- «Не более одного раза» для внешних действий ----

// Внешнее действие — перенос письма на почтовом сервере, команда на чужой
// машине, оплаченный вызов модели — повторять нельзя: очередь для того и
// повторяет работу, что внутренние работы безобидны, а эти нет.
//
// Приём простой и честный: перед действием пишем отметку «делаю, ключ
// такой-то», после — «сделано». Процесс убили между действием и записью
// результата — при повторе отметка «делаю» уже лежит, и повтор не идёт
// вслепую: он спрашивает, случилось ли снаружи, или отдаёт человеку.
// Гарантия здесь именно «не более одного раза», а не «ровно один раз» —
// последнего без участия чужой стороны не бывает.

// OutsideStarted — отметить начало внешнего действия. Второе значение —
// была ли отметка раньше: значит прошлый заход умер посередине.
func (s *Store) OutsideStarted(key string) (repeat bool, err error) {
	var prev string
	err = s.db.QueryRow(`SELECT state FROM outside_acts WHERE key = ?`, key).Scan(&prev)
	if err == nil {
		return true, nil
	}
	_, err = s.db.Exec(`INSERT INTO outside_acts (key, state, started, finished) VALUES (?, 'doing', ?, '')`,
		key, time.Now().Format(time.RFC3339Nano))
	return false, err
}

// OutsideDone — действие завершилось. Пишется сразу после того, как
// чужая сторона ответила, и до записи результата работы.
func (s *Store) OutsideDone(key string) error {
	_, err := s.db.Exec(`UPDATE outside_acts SET state = 'done', finished = ? WHERE key = ?`,
		time.Now().Format(time.RFC3339Nano), key)
	return err
}

// NoteForAction — что известно о внешнем действии: пусто (не начинали),
// «doing» (начали и не дописали) или «done».
// СделаноСнаружиСЗаметкой — «сделано» вместе с результатом: повтор после
// сбоя берёт результат отсюда и не делает работу второй раз.
// ЗаметкаКДействию — результат сохраняется, пока действие ещё «делаю»:
// повтор возьмёт его и не будет переспрашивать модель.
func (s *Store) NoteForAction(key, note string) error {
	_, err := s.db.Exec(`UPDATE outside_acts SET note = ? WHERE key = ?`, note, key)
	return err
}

func (s *Store) OutsideDoneWithNote(key, note string) error {
	_, err := s.db.Exec(`UPDATE outside_acts SET state = 'done', finished = ?, note = ? WHERE key = ?`,
		time.Now().Format(time.RFC3339Nano), note, key)
	return err
}

// OutsideNote — результат сделанного действия, пусто — не сделано.
func (s *Store) OutsideNote(key string) string {
	var st, note string
	if err := s.db.QueryRow(`SELECT state, note FROM outside_acts WHERE key = ?`, key).Scan(&st, &note); err != nil {
		return ""
	}
	return note
}

func (s *Store) OutsideState(key string) string {
	var st string
	if err := s.db.QueryRow(`SELECT state FROM outside_acts WHERE key = ?`, key).Scan(&st); err != nil {
		return ""
	}
	return st
}

// ForgetOutside — убрать отметку: человек разобрался и разрешил повтор.
func (s *Store) ForgetOutside(key string) error {
	_, err := s.db.Exec(`DELETE FROM outside_acts WHERE key = ?`, key)
	return err
}

// RowFingerprint — короткий отпечаток нескольких полей одной строки.
//
// Нужен наблюдателю за формой работы: он сравнивает отпечаток до работы и
// после, и совпадение значит, что работа ничего не изменила. Свободного SQL
// снаружи не принимаем — только имена таблицы, ключа и полей, — иначе запрос
// к базе расползётся по движку.
//
// Нет строки — нет и отпечатка: объект мог быть удалён, и это не то же
// самое, что «ничего не изменилось».
func (s *Store) RowFingerprint(table, key string, id any, fields []string) (string, bool) {
	if len(fields) == 0 || !safeName(table) || !safeName(key) {
		return "", false
	}
	for _, rep := range fields {
		if !safeName(rep) {
			return "", false
		}
	}
	// COALESCE, чтобы NULL и пустая строка давали одно и то же: разница
	// между ними ничего не говорит о том, сделала ли работа работу
	cols := make([]string, 0, len(fields))
	for _, rep := range fields {
		cols = append(cols, "COALESCE(CAST("+rep+" AS TEXT), '')")
	}
	var joined string
	err := s.db.QueryRow(`SELECT `+strings.Join(cols, `||char(31)||`)+
		` FROM `+table+` WHERE `+key+` = ?`, id).Scan(&joined)
	if err != nil {
		return "", false
	}
	h := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(h[:8]), true
}

// RowCount — сколько строк таблицы отвечают равенству по одному полю.
// Тоже для отпечатка: у работы, которая ничего не правит на месте, а
// добавляет записи, след — их число.
func (s *Store) RowCount(table, key string, id any) (int, bool) {
	if !safeName(table) || !safeName(key) {
		return 0, false
	}
	n := 0
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+key+` = ?`, id).Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}

// safeName — имя таблицы или столбца, а не кусок запроса. Имена приходят
// из кода, а не от человека, но подставлять их в SQL без проверки — привычка,
// которая однажды подставит и чужое.
func safeName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// HoldKind — снять с работы все незавершённые работы вида. Нужно
// наблюдателю: когда крутятся многие объекты одного вида, беда не в объекте,
// а в коде, и останавливать надо вид целиком.
//
// Работы не удаляются: «работу можно отложить, но не потерять». Состояние
// «held» — то же, в которое их кладёт исчерпанный предел попыток, и снимает
// его человек тем же способом.
func (s *Store) HoldKind(kind, why string) (int64, error) {
	res, err := s.db.Exec(`UPDATE work_queue SET state = ?, last_err = ?, lease = '', updated = ?
		WHERE kind = ? AND state <> 'done' AND state <> ?`,
		WorkHeld, clipText(why, 300), time.Now().Format(time.RFC3339), kind, WorkHeld)
	if err != nil {
		return 0, err
	}
	s.touch(ScreenQueue)
	n, _ := res.RowsAffected()
	return n, nil
}

// HoldRef — снять с работы один объект вида, остальные оставить.
// Один зациклившийся файл не должен запирать разбор остальных двухсот
// шестидесяти: этой ошибкой зеркало файлов уже отличилось.
func (s *Store) HoldRef(kind, ref, why string) (int64, error) {
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`UPDATE work_queue SET state = ?, last_err = ?, lease = '', updated = ?
		WHERE kind = ? AND ref = ? AND state <> 'done' AND state <> ?`,
		WorkHeld, clipText(why, 300), now, kind, ref, WorkHeld)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Незавершённой работы нет: петля успела всё доделать, и держать
		// нечего. Но объект вернётся в очередь через минуту — тот, кто его
		// туда ставит, о наблюдателе не знает. Поэтому кладём саму задержку
		// строкой: Enqueue видит «held» и не будит её.
		_, err = s.db.Exec(`INSERT INTO work_queue (kind, ref, payload, priority, state, last_err, created, updated)
			VALUES (?, ?, '', 5, ?, ?, ?, ?)`, kind, ref, WorkHeld, clipText(why, 300), now, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return 0, nil // уже задержан
			}
			return 0, err
		}
		n = 1
	}
	s.touch(ScreenQueue)
	return n, nil
}
