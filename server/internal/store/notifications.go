package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Уведомления — сущность ядра: всё, что ждёт человека, одной формой.
// Ключ (источник + объект) уникален среди открытых: новая запись с тем же
// ключом заменяет прежнюю, прежняя остаётся в истории.

type NotifyField struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Level string `json:"level,omitempty"` // ok | info | warn | alert
}

// NotifyAction — кнопка решения: команда на сервере, переход, действие
// модуля, «сделано», «отклонить», операция ядра.
type NotifyAction struct {
	Label  string `json:"label"`
	Kind   string `json:"kind"` // ssh | goto | module | done | dismiss | core
	Host   string `json:"host,omitempty"`
	Cmd    string `json:"cmd,omitempty"`
	Screen string `json:"screen,omitempty"`
	Module string `json:"module,omitempty"`
	Fn     string `json:"fn,omitempty"`
	Arg    string `json:"arg,omitempty"`
	Op     string `json:"op,omitempty"`  // core: approve_ssh | reject_ssh | rule_ask | rule_allow | rule_dismiss | grant_module | decide_skill | accept_fp | reject_fp
	Ref    string `json:"ref,omitempty"` // core: id или имя
	Style  string `json:"style,omitempty"`
}

type Notification struct {
	ID        int64
	Key       string
	Source    string // core | mail | module:<имя>
	Title     string
	Level     string // ok | info | warn | alert
	Summary   string
	Fields    []NotifyField
	Actions   []NotifyAction
	Due       string // срок RFC3339, если есть
	RemindAt  string // когда поднять в alert
	State     string // open | done | dismissed | replaced
	DecidedBy string
	DecidedAt string
	Ref       string // связанный объект: msg:<id>, ssh:<id>, skill:<id>…
	Created   string
	Updated   string
	Delivered string // JSON: канал → время
}

const (
	NotifyOpen      = "open"
	NotifyDone      = "done"
	NotifyDismissed = "dismissed"
	NotifyReplaced  = "replaced"
)

var notifyLevels = map[string]int{"ok": 0, "info": 1, "warn": 2, "alert": 3}

// LevelRank — порядок уровней для сортировки и порогов.
func LevelRank(level string) int { return notifyLevels[level] }

// NotifyRule — «письмо → уведомление»: условие, вид, срок, режим.
type NotifyRule struct {
	ID      string
	Title   string
	Cond    string // group:<группа> | sender:<адрес> | words:<слова> | ics
	About   string
	Kind    string // due | noreply | ics | none
	Days    int
	Mode    string // now | suggest
	Sort    int
	Enabled bool
}

// NotifyChannel — куда доставлять кроме экрана.
type NotifyChannel struct {
	ID        int64
	Kind      string // mac | mail | module:<имя>
	Title     string
	Enabled   bool
	MinLevel  string
	Sources   string // через запятую: servers, mail, requests, reminders; пусто — все
	QuietFrom string // «23:00»
	QuietTo   string // «8:00»
	Config    string // JSON вида
	Created   string
}

func (s *Store) migrateNotifications() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS notifications (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			key        TEXT NOT NULL,
			source     TEXT NOT NULL,
			title      TEXT NOT NULL,
			level      TEXT NOT NULL DEFAULT 'info',
			summary    TEXT NOT NULL DEFAULT '',
			fields     TEXT NOT NULL DEFAULT '[]',
			actions    TEXT NOT NULL DEFAULT '[]',
			due        TEXT NOT NULL DEFAULT '',
			remind_at  TEXT NOT NULL DEFAULT '',
			state      TEXT NOT NULL DEFAULT 'open',
			decided_by TEXT NOT NULL DEFAULT '',
			decided_at TEXT NOT NULL DEFAULT '',
			ref        TEXT NOT NULL DEFAULT '',
			created    TEXT NOT NULL,
			updated    TEXT NOT NULL,
			delivered  TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notifications_state ON notifications(state, level, updated DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_notifications_key ON notifications(key, state)`,
		`CREATE TABLE IF NOT EXISTS notify_rules (
			id      TEXT PRIMARY KEY,
			title   TEXT NOT NULL,
			cond    TEXT NOT NULL DEFAULT '',
			about   TEXT NOT NULL DEFAULT '',
			kind    TEXT NOT NULL DEFAULT 'due',
			days    INTEGER NOT NULL DEFAULT 0,
			mode    TEXT NOT NULL DEFAULT 'now',
			sort    INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS notify_channels (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			kind       TEXT NOT NULL,
			title      TEXT NOT NULL,
			enabled    INTEGER NOT NULL DEFAULT 1,
			min_level  TEXT NOT NULL DEFAULT 'warn',
			sources    TEXT NOT NULL DEFAULT '',
			quiet_from TEXT NOT NULL DEFAULT '',
			quiet_to   TEXT NOT NULL DEFAULT '',
			config     TEXT NOT NULL DEFAULT '{}',
			created    TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS notify_log (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			notif   INTEGER NOT NULL,
			channel TEXT NOT NULL,
			at      TEXT NOT NULL,
			err     TEXT NOT NULL DEFAULT ''
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("уведомления: %w", err)
		}
	}
	return nil
}

const notifCols = `id, key, source, title, level, summary, fields, actions, due, remind_at, state, decided_by, decided_at, ref, created, updated, delivered`

func scanNotification(r rowScanner) (*Notification, error) {
	n := &Notification{}
	var fields, actions string
	err := r.Scan(&n.ID, &n.Key, &n.Source, &n.Title, &n.Level, &n.Summary, &fields, &actions, &n.Due, &n.RemindAt,
		&n.State, &n.DecidedBy, &n.DecidedAt, &n.Ref, &n.Created, &n.Updated, &n.Delivered)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(fields), &n.Fields)
	_ = json.Unmarshal([]byte(actions), &n.Actions)
	return n, nil
}

// PutNotification кладёт уведомление; открытая запись с тем же ключом
// переводится в replaced. Возвращает id новой записи.
func (s *Store) PutNotification(n *Notification) (int64, error) {
	if strings.TrimSpace(n.Title) == "" {
		return 0, fmt.Errorf("у уведомления нужен заголовок")
	}
	if _, ok := notifyLevels[n.Level]; !ok {
		n.Level = "info"
	}
	if n.State == "" {
		n.State = NotifyOpen
	}
	now := time.Now().Format(time.RFC3339)
	if n.Created == "" {
		n.Created = now
	}
	n.Updated = now
	fields, _ := json.Marshal(n.Fields)
	if n.Fields == nil {
		fields = []byte("[]")
	}
	actions, _ := json.Marshal(n.Actions)
	if n.Actions == nil {
		actions = []byte("[]")
	}
	if n.Delivered == "" {
		n.Delivered = "{}"
	}
	if n.Key != "" {
		_, _ = s.db.Exec(`UPDATE notifications SET state = ?, updated = ? WHERE key = ? AND state = 'open'`, NotifyReplaced, now, n.Key)
	}
	res, err := s.db.Exec(`INSERT INTO notifications (key, source, title, level, summary, fields, actions, due, remind_at, state, decided_by, decided_at, ref, created, updated, delivered)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.Key, n.Source, n.Title, n.Level, n.Summary, string(fields), string(actions), n.Due, n.RemindAt, n.State, n.DecidedBy, n.DecidedAt, n.Ref, n.Created, n.Updated, n.Delivered)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	n.ID = id
	s.touch(ScreenNotifications)
	return id, nil
}

func (s *Store) Notification(id int64) (*Notification, bool) {
	n, err := scanNotification(s.db.QueryRow(`SELECT `+notifCols+` FROM notifications WHERE id = ?`, id))
	return n, err == nil
}

// OpenNotificationByKey — открытая запись с ключом, если есть.
func (s *Store) OpenNotificationByKey(key string) (*Notification, bool) {
	n, err := scanNotification(s.db.QueryRow(`SELECT `+notifCols+` FROM notifications WHERE key = ? AND state = 'open' ORDER BY id DESC LIMIT 1`, key))
	return n, err == nil
}

// Notifications — открытые (state=open) или закрытые (state<>open) записи,
// по убыванию уровня и времени.
// TopOpenNotifyLevel — старший уровень среди открытых записей одной строкой:
// пункт меню спрашивал его на каждом экране, загружая до трёхсот записей
// целиком, с полями и действиями (perf-pass 3.4). Порядок — тот же, что у
// Notifications.
func (s *Store) TopOpenNotifyLevel() string {
	var level string
	_ = s.db.QueryRow(`SELECT level FROM notifications WHERE state = 'open'
		ORDER BY CASE level WHEN 'alert' THEN 3 WHEN 'warn' THEN 2 WHEN 'info' THEN 1 ELSE 0 END DESC LIMIT 1`).Scan(&level)
	return level
}

func (s *Store) Notifications(open bool, limit int) ([]*Notification, error) {
	cond := `state = 'open'`
	if !open {
		cond = `state <> 'open'`
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT `+notifCols+` FROM notifications WHERE `+cond+`
		ORDER BY CASE level WHEN 'alert' THEN 3 WHEN 'warn' THEN 2 WHEN 'info' THEN 1 ELSE 0 END DESC, updated DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NotificationsSince — лента для каналов клиента: открытые записи с id больше
// заданного и уровнем не ниже порога.
func (s *Store) NotificationsSince(afterID int64, minLevel string, limit int) ([]*Notification, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+notifCols+` FROM notifications WHERE id > ? AND state = 'open' ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		if LevelRank(n.Level) >= LevelRank(minLevel) {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

func (s *Store) LastNotificationID() int64 {
	var id sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(id) FROM notifications`).Scan(&id)
	return id.Int64
}

// CountOpenNotifications — сколько открытых от уровня и выше. Отложенное
// не считается: человек уже сказал «не сейчас», и счётчик, который от
// этого не гаснет, перестаёт что-либо значить. Срок наступит — задача
// «Сроки» поднимет запись, и она вернётся в счёт.
func (s *Store) CountOpenNotifications(minLevel string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE state = 'open' AND
		(remind_at = '' OR remind_at <= ?) AND
		CASE level WHEN 'alert' THEN 3 WHEN 'warn' THEN 2 WHEN 'info' THEN 1 ELSE 0 END >= ?`,
		time.Now().Format(time.RFC3339), LevelRank(minLevel)).Scan(&n)
	return n
}

// SnoozeNotification — «не сейчас, вернись потом». Срок напоминания
// переносится, а тревога опускается до предупреждения: задача «Сроки»
// поднимает обратно только то, что ниже alert, иначе отложенное не
// вернулось бы никогда. Итог записи не переписывается — он говорит, что
// случилось, а не что человек с этим решил.
func (s *Store) SnoozeNotification(id int64, until string) error {
	_, err := s.db.Exec(`UPDATE notifications SET remind_at = ?, state = 'open',
		level = CASE level WHEN 'alert' THEN 'warn' ELSE level END, updated = ? WHERE id = ?`,
		until, time.Now().Format(time.RFC3339), id)
	if err == nil {
		s.touch(ScreenNotifications)
	}
	return err
}

// SetNotificationState закрывает или возвращает запись.
func (s *Store) SetNotificationState(id int64, state, by string) error {
	now := time.Now().Format(time.RFC3339)
	decidedAt := now
	if state == NotifyOpen {
		by, decidedAt = "", ""
	}
	_, err := s.db.Exec(`UPDATE notifications SET state = ?, decided_by = ?, decided_at = ?, updated = ? WHERE id = ?`, state, by, decidedAt, now, id)
	if err == nil {
		s.touch(ScreenNotifications)
	}
	return err
}

// CloseNotificationByKey — решение пришло из старого места («Серверы»,
// «Модули», карточка чата): открытая запись с ключом закрывается.
func (s *Store) CloseNotificationByKey(key, state, by string) {
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`UPDATE notifications SET state = ?, decided_by = ?, decided_at = ?, updated = ? WHERE key = ? AND state = 'open'`, state, by, now, now, key)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.touch(ScreenNotifications)
	}
}

// UpdateNotificationFields — поднять уровень, сменить срок, отметить доставку.
func (s *Store) UpdateNotificationLevel(id int64, level, summary string) error {
	_, err := s.db.Exec(`UPDATE notifications SET level = ?, summary = CASE WHEN ? = '' THEN summary ELSE ? END, updated = ? WHERE id = ?`,
		level, summary, summary, time.Now().Format(time.RFC3339), id)
	if err == nil {
		s.touch(ScreenNotifications)
	}
	return err
}

// SetNotificationResult — итог действия: поле «Результат», строка-итог,
// уровень и состояние; при ошибке запись остаётся открытой.
func (s *Store) SetNotificationResult(id int64, field NotifyField, summary, level, state, by string) error {
	n, ok := s.Notification(id)
	if !ok {
		return fmt.Errorf("уведомления %d нет", id)
	}
	var fields []NotifyField
	for _, f := range n.Fields {
		if f.Label != field.Label {
			fields = append(fields, f)
		}
	}
	fields = append(fields, field)
	raw, _ := json.Marshal(fields)
	now := time.Now().Format(time.RFC3339)
	decidedAt := ""
	if state != NotifyOpen {
		decidedAt = now
	} else {
		by = ""
	}
	_, err := s.db.Exec(`UPDATE notifications SET fields = ?, summary = ?, level = ?, state = ?, decided_by = ?, decided_at = ?, updated = ? WHERE id = ?`,
		string(raw), summary, level, state, by, decidedAt, now, id)
	if err == nil {
		s.touch(ScreenNotifications)
	}
	return err
}

// AddNotificationAction — дописать кнопку (например, «Открыть чат»), если такой ещё нет.
func (s *Store) AddNotificationAction(id int64, a NotifyAction) error {
	n, ok := s.Notification(id)
	if !ok {
		return fmt.Errorf("уведомления %d нет", id)
	}
	for _, x := range n.Actions {
		if x.Kind == a.Kind && x.Screen == a.Screen && x.Label == a.Label {
			return nil
		}
	}
	raw, _ := json.Marshal(append(n.Actions, a))
	_, err := s.db.Exec(`UPDATE notifications SET actions = ? WHERE id = ?`, string(raw), id)
	if err == nil {
		s.touch(ScreenNotifications)
	}
	return err
}

func (s *Store) SetNotificationDue(id int64, due, remindAt string) error {
	_, err := s.db.Exec(`UPDATE notifications SET due = ?, remind_at = ?, state = 'open', decided_by = '', decided_at = '', updated = ? WHERE id = ?`,
		due, remindAt, time.Now().Format(time.RFC3339), id)
	if err == nil {
		s.touch(ScreenNotifications)
	}
	return err
}

// DueNotifications — открытые с наступившим временем напоминания и уровнем ниже alert.
func (s *Store) DueNotifications(now string) ([]*Notification, error) {
	rows, err := s.db.Query(`SELECT `+notifCols+` FROM notifications WHERE state = 'open' AND remind_at <> '' AND remind_at <= ? AND level <> 'alert' ORDER BY remind_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkDelivered — отметка о доставке. Время в карте `delivered` ставится
// только за успех: «доставлено» должно означать «человек это увидел».
// Раньше туда писалась и неудача («подписанных устройств нет», отказ
// службы пушей), и пустая карта переставала отвечать на вопрос «дошло ли
// хоть куда-нибудь» — молчание выглядело как доставка. Причина отказа
// по-прежнему ложится в журнал канала.
func (s *Store) MarkDelivered(id int64, channel string, errText string) {
	n, ok := s.Notification(id)
	if !ok {
		return
	}
	now := time.Now().Format(time.RFC3339)
	if errText == "" {
		delivered := n.DeliveredTo()
		delivered[channel] = now
		raw, _ := json.Marshal(delivered)
		_, _ = s.db.Exec(`UPDATE notifications SET delivered = ? WHERE id = ?`, string(raw), id)
		s.touch(ScreenNotifications)
	}
	_, _ = s.db.Exec(`INSERT INTO notify_log (notif, channel, at, err) VALUES (?, ?, ?, ?)`, id, channel, now, errText)
}

// DeliveredTo — каналы, до которых запись дошла, и когда.
func (n *Notification) DeliveredTo() map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(n.Delivered) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(n.Delivered), &out)
	return out
}

// Undelivered — запись не ушла ни одним каналом. Не «ошибка доставки», а
// «человек об этом не узнал»: ассистент, который молчит, должен сказать об
// этом сам.
func (n *Notification) Undelivered() bool { return len(n.DeliveredTo()) == 0 }

const rankCase = `CASE level WHEN 'alert' THEN 3 WHEN 'warn' THEN 2 WHEN 'info' THEN 1 ELSE 0 END`

// UndeliveredSince — сколько записей уровня minLevel и выше за период
// каналы должны были доставить (total) и сколько не дошло ни одним
// (undelivered). Порог приходит снаружи: то, что каналы не берут нарочно,
// недоставкой не считается.
func (s *Store) UndeliveredSince(since, minLevel string) (undelivered, total int) {
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN delivered IN ('', '{}') THEN 1 ELSE 0 END), 0)
		FROM notifications WHERE created >= ? AND level <> 'ok' AND `+rankCase+` >= ?`,
		since, LevelRank(minLevel)).Scan(&total, &undelivered)
	return undelivered, total
}

// UndeliveredOpen — открытые записи уровня minLevel и выше, которые не
// дошли ни одним каналом и не старше срока. По ним идёт повтор доставки:
// тихие часы кончаются, житель машины просыпается, канал включают — а
// прежняя посылка была одноразовой. Отложенное человеком не берём: он уже
// сказал «не сейчас».
func (s *Store) UndeliveredOpen(since, minLevel string, limit int) ([]*Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	now := time.Now().Format(time.RFC3339)
	rows, err := s.db.Query(`SELECT `+notifCols+` FROM notifications WHERE state = 'open'
		AND delivered IN ('', '{}') AND created >= ? AND `+rankCase+` >= ?
		AND (remind_at = '' OR remind_at <= ?) ORDER BY id LIMIT ?`, since, LevelRank(minLevel), now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NotifyFails — последняя причина отказа доставки по каждой из записей,
// одним запросом: строка «не доставлено» на экране должна называть
// причину, а не просто сообщать о молчании.
func (s *Store) NotifyFails(ids []int64) map[int64]string {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out
	}
	if len(ids) > 300 {
		ids = ids[:300]
	}
	holes := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		holes[i], args[i] = "?", id
	}
	rows, err := s.db.Query(`SELECT notif, channel, err FROM notify_log
		WHERE err <> '' AND notif IN (`+strings.Join(holes, ",")+`) ORDER BY id`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var channel, e string
		if rows.Scan(&id, &channel, &e) == nil {
			out[id] = channel + ": " + e
		}
	}
	return out
}

// CountClosedNotifications — сколько записей в истории. Считаем в базе, а
// не по длине выборки: выборка ограничена, и «История 1000» врала.
func (s *Store) CountClosedNotifications() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE state <> 'open'`).Scan(&n)
	return n
}

// PurgeNotifications убирает закрытые старше срока.
func (s *Store) PurgeNotifications(before string) int64 {
	res, err := s.db.Exec(`DELETE FROM notifications WHERE state <> 'open' AND updated < ?`, before)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// ---- правила ----

func (s *Store) NotifyRules() ([]*NotifyRule, error) {
	rows, err := s.db.Query(`SELECT id, title, cond, about, kind, days, mode, sort, enabled FROM notify_rules ORDER BY sort, title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NotifyRule
	for rows.Next() {
		r := &NotifyRule{}
		var enabled int
		if err := rows.Scan(&r.ID, &r.Title, &r.Cond, &r.About, &r.Kind, &r.Days, &r.Mode, &r.Sort, &enabled); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) NotifyRule(id string) (*NotifyRule, bool) {
	r := &NotifyRule{}
	var enabled int
	err := s.db.QueryRow(`SELECT id, title, cond, about, kind, days, mode, sort, enabled FROM notify_rules WHERE id = ?`, id).
		Scan(&r.ID, &r.Title, &r.Cond, &r.About, &r.Kind, &r.Days, &r.Mode, &r.Sort, &enabled)
	r.Enabled = enabled != 0
	return r, err == nil
}

func (s *Store) SaveNotifyRule(r *NotifyRule) error {
	if strings.TrimSpace(r.ID) == "" {
		r.ID = fmt.Sprintf("rule-%d", time.Now().UnixNano())
	}
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`INSERT INTO notify_rules (id, title, cond, about, kind, days, mode, sort, enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET title = excluded.title, cond = excluded.cond, about = excluded.about, kind = excluded.kind,
		days = excluded.days, mode = excluded.mode, sort = excluded.sort, enabled = excluded.enabled`,
		r.ID, r.Title, r.Cond, r.About, r.Kind, r.Days, r.Mode, r.Sort, enabled)
	return err
}

func (s *Store) DeleteNotifyRule(id string) error {
	_, err := s.db.Exec(`DELETE FROM notify_rules WHERE id = ?`, id)
	return err
}

// ---- каналы ----

func (s *Store) NotifyChannels() ([]*NotifyChannel, error) {
	rows, err := s.db.Query(`SELECT id, kind, title, enabled, min_level, sources, quiet_from, quiet_to, config, created FROM notify_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NotifyChannel
	for rows.Next() {
		c := &NotifyChannel{}
		var enabled int
		if err := rows.Scan(&c.ID, &c.Kind, &c.Title, &enabled, &c.MinLevel, &c.Sources, &c.QuietFrom, &c.QuietTo, &c.Config, &c.Created); err != nil {
			return nil, err
		}
		c.Enabled = enabled != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) NotifyChannel(id int64) (*NotifyChannel, bool) {
	list, err := s.NotifyChannels()
	if err != nil {
		return nil, false
	}
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return nil, false
}

func (s *Store) SaveNotifyChannel(c *NotifyChannel) error {
	if c.Created == "" {
		c.Created = time.Now().Format(time.RFC3339)
	}
	if c.Config == "" {
		c.Config = "{}"
	}
	if _, ok := notifyLevels[c.MinLevel]; !ok {
		c.MinLevel = "warn"
	}
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	if c.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO notify_channels (kind, title, enabled, min_level, sources, quiet_from, quiet_to, config, created) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Kind, c.Title, enabled, c.MinLevel, c.Sources, c.QuietFrom, c.QuietTo, c.Config, c.Created)
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.db.Exec(`UPDATE notify_channels SET kind = ?, title = ?, enabled = ?, min_level = ?, sources = ?, quiet_from = ?, quiet_to = ?, config = ? WHERE id = ?`,
		c.Kind, c.Title, enabled, c.MinLevel, c.Sources, c.QuietFrom, c.QuietTo, c.Config, c.ID)
	return err
}

func (s *Store) DeleteNotifyChannel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM notify_channels WHERE id = ?`, id)
	return err
}

// NotifyLog — последние доставки по каналу.
func (s *Store) NotifyLog(channel string, limit int) []string {
	rows, err := s.db.Query(`SELECT at, err FROM notify_log WHERE channel = ? ORDER BY id DESC LIMIT ?`, channel, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var at, e string
		if rows.Scan(&at, &e) == nil {
			if e != "" {
				out = append(out, at+" · "+e)
			} else {
				out = append(out, at)
			}
		}
	}
	return out
}
