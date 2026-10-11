package store

// Подписки устройств на пуши. Одна запись — одно устройство-браузер: у
// каждого своя конечная точка у службы пушей и своя пара ключей.
//
// Канал `web` общий (включён ли, источники, порог и тихие часы по
// умолчанию), а у устройства свои три вещи: имя, порог и тихие часы.
// Пустое значение значит «как у канала»: старая подписка ведёт себя ровно
// так же, как вела себя без этих полей.

import (
	"fmt"
	"strings"
	"time"
)

type PushSubscription struct {
	ID       int64
	Endpoint string
	P256dh   string
	Auth     string
	// Agent — имя, которое Сёма вывел из устройства при подписке («iPhone ·
	// Сёма с экрана «Домой»»). У подписок, заведённых до появления имён,
	// здесь кусок строки браузера — его показывают через PushDeviceName
	Agent string
	// Title — имя, которое дал человек. Пусто — показывается Agent. Живёт
	// отдельно, чтобы повторная подписка не затирала выбранное имя
	Title string
	// MinLevel и Quiet — свои порог и тихие часы устройства. Пусто — как у
	// канала; в Quiet «-» значит «своих часов нет и канальные не действуют»
	MinLevel string
	Quiet    string
	// VAPIDKey — под каким нашим открытым ключом устройство подписалось.
	// Ключ сменился или потерян — подписка мертва, и это надо сказать вслух,
	// а не молча получать отказы
	VAPIDKey string
	Created  string
	LastOK   string
	LastErr  string
	// LastStatus — что ответила служба пушей в последний раз (201, 403…);
	// ErrAt — когда случилась последняя неудача. Без них строка «не удалось»
	// не говорила, давно ли и что именно сказала служба
	LastStatus int
	ErrAt      string
}

func (s *Store) migratePush() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS push_subscriptions (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			endpoint  TEXT NOT NULL UNIQUE,
			p256dh    TEXT NOT NULL,
			auth      TEXT NOT NULL,
			agent     TEXT NOT NULL DEFAULT '',
			vapid_key TEXT NOT NULL DEFAULT '',
			created   TEXT NOT NULL,
			last_ok   TEXT NOT NULL DEFAULT '',
			last_err  TEXT NOT NULL DEFAULT ''
		)`,
		// запись «ждёт конца тихих часов»: что устройству не отправили из-за
		// них и надо отправить потом. Отдельная таблица, а не отметка в
		// `notifications.delivered`: та считает запись доставленной, и
		// повтор для других каналов (Mac) тогда никогда бы не наступил
		`CREATE TABLE IF NOT EXISTS push_pending (
			device  INTEGER NOT NULL,
			notif   INTEGER NOT NULL,
			created TEXT NOT NULL,
			PRIMARY KEY (device, notif)
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("пуши: %w", err)
		}
	}
	// колонки, появившиеся позже: ошибка «уже есть» — нормальный исход
	for _, q := range []string{
		`ALTER TABLE push_subscriptions ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE push_subscriptions ADD COLUMN min_level TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE push_subscriptions ADD COLUMN quiet TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE push_subscriptions ADD COLUMN last_status INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE push_subscriptions ADD COLUMN err_at TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = s.db.Exec(q)
	}
	return nil
}

// SavePushSubscription — подписка устройства. Та же конечная точка —
// та же запись: браузер переподписывается, и двойников быть не должно.
// Имя, порог и тихие часы, выбранные человеком, при этом не трогаются.
func (s *Store) SavePushSubscription(p *PushSubscription) error {
	if p.Endpoint == "" || p.P256dh == "" || p.Auth == "" {
		return fmt.Errorf("подписка неполная: нужны endpoint и оба ключа")
	}
	_, err := s.db.Exec(`INSERT INTO push_subscriptions (endpoint, p256dh, auth, agent, vapid_key, created)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth,
			agent = excluded.agent, vapid_key = excluded.vapid_key, last_err = '', err_at = ''`,
		p.Endpoint, p.P256dh, p.Auth, p.Agent, p.VAPIDKey, time.Now().Format(time.RFC3339))
	if err != nil {
		return err
	}
	// номер нужен тому, кто подписывал: по нему устройство узнаёт себя в списке
	return s.db.QueryRow(`SELECT id FROM push_subscriptions WHERE endpoint = ?`, p.Endpoint).Scan(&p.ID)
}

func (s *Store) DeletePushSubscription(endpoint string) error {
	var id int64
	if s.db.QueryRow(`SELECT id FROM push_subscriptions WHERE endpoint = ?`, endpoint).Scan(&id) == nil {
		_, _ = s.db.Exec(`DELETE FROM push_pending WHERE device = ?`, id)
	}
	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

const pushCols = `id, endpoint, p256dh, auth, agent, title, min_level, quiet, vapid_key, created, last_ok, last_err, last_status, err_at`

func scanPush(sc interface{ Scan(...any) error }) (*PushSubscription, error) {
	p := &PushSubscription{}
	err := sc.Scan(&p.ID, &p.Endpoint, &p.P256dh, &p.Auth, &p.Agent, &p.Title, &p.MinLevel, &p.Quiet,
		&p.VAPIDKey, &p.Created, &p.LastOK, &p.LastErr, &p.LastStatus, &p.ErrAt)
	return p, err
}

func (s *Store) PushSubscriptions() ([]*PushSubscription, error) {
	rows, err := s.db.Query(`SELECT ` + pushCols + ` FROM push_subscriptions ORDER BY created, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PushSubscription
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PushSubscriptionByID — устройство по номеру.
func (s *Store) PushSubscriptionByID(id int64) (*PushSubscription, bool) {
	p, err := scanPush(s.db.QueryRow(`SELECT `+pushCols+` FROM push_subscriptions WHERE id = ?`, id))
	return p, err == nil
}

// PushSubscriptionByEndpoint — устройство по адресу у службы пушей: браузер
// знает свой адрес, а не наш номер.
func (s *Store) PushSubscriptionByEndpoint(endpoint string) (*PushSubscription, bool) {
	p, err := scanPush(s.db.QueryRow(`SELECT `+pushCols+` FROM push_subscriptions WHERE endpoint = ?`, endpoint))
	return p, err == nil
}

// SetPushTitle — имя устройства, которое дал человек. Пусто — вернуться к
// имени, выведенному из устройства.
func (s *Store) SetPushTitle(id int64, title string) error {
	_, err := s.db.Exec(`UPDATE push_subscriptions SET title = ? WHERE id = ?`, strings.TrimSpace(title), id)
	return err
}

// SetPushLevel — свой порог устройства: info, warn, alert или пусто («как у
// канала»).
func (s *Store) SetPushLevel(id int64, level string) error {
	if level != "" {
		if _, ok := notifyLevels[level]; !ok {
			return fmt.Errorf("уровень %q неизвестен", level)
		}
	}
	_, err := s.db.Exec(`UPDATE push_subscriptions SET min_level = ? WHERE id = ?`, level, id)
	return err
}

// SetPushQuiet — свои тихие часы устройства: «23:00-8:00», «-» (без тихих
// часов вовсе) или пусто («как у канала»). Разбирает и проверяет вызывающий.
func (s *Store) SetPushQuiet(id int64, quiet string) error {
	_, err := s.db.Exec(`UPDATE push_subscriptions SET quiet = ? WHERE id = ?`, quiet, id)
	return err
}

// MarkPushDelivery — исход отправки на устройство: по нему человек видит,
// живо ли оно, а не гадает. status — ответ службы пушей (0 — до службы не
// дошли).
func (s *Store) MarkPushDelivery(endpoint, errText string, status int) {
	now := time.Now().Format(time.RFC3339)
	if errText == "" {
		_, _ = s.db.Exec(`UPDATE push_subscriptions SET last_ok = ?, last_err = '', err_at = '', last_status = ? WHERE endpoint = ?`,
			now, status, endpoint)
		return
	}
	_, _ = s.db.Exec(`UPDATE push_subscriptions SET last_err = ?, err_at = ?, last_status = ? WHERE endpoint = ?`,
		errText, now, status, endpoint)
}

// DeferPush — устройству эту запись отправить не успели: тихие часы. Повтор
// сделает проход после их конца.
func (s *Store) DeferPush(device, notif int64) {
	_, _ = s.db.Exec(`INSERT OR IGNORE INTO push_pending (device, notif, created) VALUES (?,?,?)`,
		device, notif, time.Now().Format(time.RFC3339))
}

// DeferredPush — записи, ждущие устройство, от старых к новым.
func (s *Store) DeferredPush(device int64) []int64 {
	rows, err := s.db.Query(`SELECT notif FROM push_pending WHERE device = ? ORDER BY notif`, device)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// ForgetDeferredPush — снять ожидание: отправлено, запись закрыта или
// устарела.
func (s *Store) ForgetDeferredPush(device int64, notifs ...int64) {
	for _, id := range notifs {
		_, _ = s.db.Exec(`DELETE FROM push_pending WHERE device = ? AND notif = ?`, device, id)
	}
}
