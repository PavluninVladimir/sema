package store

// Доступ: passkey (WebAuthn), сессии, токены для API, журнал доступа.
// Токены хранятся хэшем: значение показывается один раз при выдаче.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type Passkey struct {
	ID         int64
	Name       string
	Credential string // JSON webauthn.Credential
	Created    time.Time
	LastUsed   time.Time
	// RPID — имя сервера, для которого ключ заведён. Пусто у ключей,
	// заведённых до переезда адреса: тогда «неизвестно», а не «нынешний».
	RPID string
}

type Session struct {
	ID        string // сам токен сессии (в базе — хэш)
	Hash      string
	PasskeyID int64
	Created   time.Time
	LastSeen  time.Time
	Expires   time.Time
	Agent     string
	IP        string
	Note      string
	// Own — устройство отмечено «своим»: только на нём клиент имеет
	// право сохранять снимок экрана (offline-snapshot, решение 3).
	// Отметка живёт здесь, а не в браузере, поэтому видна на «Доступе»
	// у любого устройства, снимается оттуда же и уходит вместе с
	// отзывом сессии — строка удаляется целиком.
	Own bool
	// Restricted — сессия открыта контрольной фразой из недоверенной сети:
	// снаружи фраза не должна давать больше, чем ключ, привязанный к
	// адресу и не увозимый с устройства. Признак ставится один раз при
	// входе (та же IsLocal, что и везде) и живёт с сессией, а не
	// проверяется заново на каждый запрос — «своя сеть» на момент входа,
	// а не на момент каждого обращения.
	Restricted bool
}

type APIToken struct {
	ID       int64
	Name     string
	Hash     string
	Scopes   []string
	Created  time.Time
	LastUsed time.Time
	Revoked  time.Time
	Uses     int
}

type AccessEntry struct {
	ID      int64
	At      time.Time
	Who     string // token:<имя> | session:<passkey> | anon
	Method  string
	Path    string
	Status  int
	Bytes   int64
	Seconds float64
	IP      string
	Note    string
}

func (s *Store) migrateAuth() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS passkeys (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL,
			credential TEXT NOT NULL,
			created    TEXT NOT NULL,
			last_used  TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			hash       TEXT PRIMARY KEY,
			passkey_id INTEGER NOT NULL DEFAULT 0,
			created    TEXT NOT NULL,
			last_seen  TEXT NOT NULL,
			expires    TEXT NOT NULL,
			agent      TEXT NOT NULL DEFAULT '',
			ip         TEXT NOT NULL DEFAULT '',
			note       TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS api_tokens (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			name      TEXT NOT NULL,
			hash      TEXT NOT NULL UNIQUE,
			scopes    TEXT NOT NULL DEFAULT '',
			created   TEXT NOT NULL,
			last_used TEXT NOT NULL DEFAULT '',
			revoked   TEXT NOT NULL DEFAULT '',
			uses      INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS access_log (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			at      TEXT NOT NULL,
			who     TEXT NOT NULL,
			method  TEXT NOT NULL,
			path    TEXT NOT NULL,
			status  INTEGER NOT NULL,
			bytes   INTEGER NOT NULL DEFAULT 0,
			seconds REAL NOT NULL DEFAULT 0,
			ip      TEXT NOT NULL DEFAULT '',
			note    TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_access_log_at ON access_log(at)`,
		// одна фраза разом: строка с id=1 заменяется целиком при смене,
		// прежняя не хранится нигде и не восстанавливается
		`CREATE TABLE IF NOT EXISTS login_phrase (
			id      INTEGER PRIMARY KEY CHECK (id = 1),
			hash    TEXT NOT NULL,
			created TEXT NOT NULL
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("доступ: %w", err)
		}
	}
	// «своё устройство» появилось позже сессий: у старых строк его нет,
	// и ноль здесь значит «не отмечали», а не «отметили чужим»
	_, _ = s.db.Exec(`ALTER TABLE sessions ADD COLUMN own INTEGER NOT NULL DEFAULT 0`)
	// «урезанная» появилась вместе с входом по фразе: у старых сессий,
	// открытых ключом, ноль здесь и означает полный доступ, как и раньше
	_, _ = s.db.Exec(`ALTER TABLE sessions ADD COLUMN restricted INTEGER NOT NULL DEFAULT 0`)
	// адрес, для которого заведён ключ: браузер привязывает passkey к имени
	// сервера, и после переезда на домен прежние ключи не пускают. У старых
	// строк здесь пусто — «неизвестно, для какого», а не «для нынешнего»:
	// врать про это хуже, чем молчать
	_, _ = s.db.Exec(`ALTER TABLE passkeys ADD COLUMN rp_id TEXT NOT NULL DEFAULT ''`)
	return nil
}

// NewSecret — случайная строка для сессии или токена: префикс плюс 32 байта.
func NewSecret(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func HashSecret(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// ---- passkey ----

func (s *Store) Passkeys() ([]*Passkey, error) {
	rows, err := s.db.Query(`SELECT id, name, credential, created, last_used, rp_id FROM passkeys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Passkey
	for rows.Next() {
		p := &Passkey{}
		var created, used string
		if err := rows.Scan(&p.ID, &p.Name, &p.Credential, &created, &used, &p.RPID); err != nil {
			return nil, err
		}
		p.Created, p.LastUsed = parseTime(created), parseTime(used)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CountPasskeys() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM passkeys`).Scan(&n)
	return n
}

// AddPasskey — новый ключ устройства. rpID запоминается вместе с ключом:
// после переезда на домен по нему видно, какие ключи ещё пускают, а какие
// остались на прежнем адресе.
func (s *Store) AddPasskey(name, credential, rpID string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO passkeys (name, credential, created, rp_id) VALUES (?, ?, ?, ?)`, name, credential, time.Now().Format(time.RFC3339), rpID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePasskeyCredential(id int64, credential string) {
	_, _ = s.db.Exec(`UPDATE passkeys SET credential = ?, last_used = ? WHERE id = ?`, credential, time.Now().Format(time.RFC3339), id)
}

func (s *Store) RenamePasskey(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE passkeys SET name = ? WHERE id = ?`, name, id)
	return err
}

func (s *Store) DeletePasskey(id int64) error {
	_, err := s.db.Exec(`DELETE FROM passkeys WHERE id = ?`, id)
	return err
}

// ---- сессии ----

func (s *Store) AddSession(token string, passkeyID int64, agent, ip, note string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.Exec(`INSERT INTO sessions (hash, passkey_id, created, last_seen, expires, agent, ip, note) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		HashSecret(token), passkeyID, now.Format(time.RFC3339), now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339), agent, ip, note)
	return err
}

// SessionByToken — живая сессия по токену; продлевает срок при обращении.
func (s *Store) SessionByToken(token string, ttl time.Duration) (*Session, bool) {
	h := HashSecret(token)
	se := &Session{Hash: h}
	var created, seen, expires string
	var own, restricted int
	if err := s.db.QueryRow(`SELECT passkey_id, created, last_seen, expires, agent, ip, note, own, restricted FROM sessions WHERE hash = ?`, h).
		Scan(&se.PasskeyID, &created, &seen, &expires, &se.Agent, &se.IP, &se.Note, &own, &restricted); err != nil {
		return nil, false
	}
	se.Own, se.Restricted = own != 0, restricted != 0
	se.Created, se.LastSeen, se.Expires = parseTime(created), parseTime(seen), parseTime(expires)
	if time.Now().After(se.Expires) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE hash = ?`, h)
		return nil, false
	}
	if time.Since(se.LastSeen) > time.Minute {
		now := time.Now()
		_, _ = s.db.Exec(`UPDATE sessions SET last_seen = ?, expires = ? WHERE hash = ?`, now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339), h)
	}
	return se, true
}

// SessionActive проверяет пропуск без продления: фоновый провод сам по
// себе не должен превращать сессию в бессрочную.
func (s *Store) SessionActive(hash string) bool {
	var expires string
	err := s.db.QueryRow(`SELECT expires FROM sessions WHERE hash = ?`, hash).Scan(&expires)
	return err == nil && time.Now().Before(parseTime(expires))
}

func (s *Store) Sessions() ([]*Session, error) {
	rows, err := s.db.Query(`SELECT hash, passkey_id, created, last_seen, expires, agent, ip, note, own, restricted FROM sessions WHERE expires > ? ORDER BY last_seen DESC`,
		time.Now().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		se := &Session{}
		var created, seen, expires string
		var own, restricted int
		if err := rows.Scan(&se.Hash, &se.PasskeyID, &created, &seen, &expires, &se.Agent, &se.IP, &se.Note, &own, &restricted); err != nil {
			return nil, err
		}
		se.Own, se.Restricted = own != 0, restricted != 0
		se.Created, se.LastSeen, se.Expires = parseTime(created), parseTime(seen), parseTime(expires)
		out = append(out, se)
	}
	return out, rows.Err()
}

// SetSessionRestricted — сессия открыта фразой из недоверенной сети:
// снаружи из неё можно только завести passkey. Ставится сразу после
// открытия сессии (см. handlePhraseLogin), а не решается на каждый
// запрос заново.
func (s *Store) SetSessionRestricted(hash string, restricted bool) error {
	_, err := s.db.Exec(`UPDATE sessions SET restricted = ? WHERE hash = ?`, boolInt(restricted), hash)
	return err
}

// SetSessionOwn — поставить или снять отметку «своё устройство» у сессии.
// Живёт у сессии, а не в браузере: тогда её видно на «Доступе» рядом с
// остальными сессиями, и она снимается оттуда же, включая потерянный
// телефон, до которого иначе не достучаться.
func (s *Store) SetSessionOwn(hash string, own bool) error {
	res, err := s.db.Exec(`UPDATE sessions SET own = ? WHERE hash = ?`, boolInt(own), hash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("сессия не найдена")
	}
	return nil
}

// DeleteSessionsOfPasskey — удалён ключ, умирают и все сессии, открытые
// им: иначе украденная cookie переживает отзыв ключа.
func (s *Store) DeleteSessionsOfPasskey(passkeyID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE passkey_id = ?`, passkeyID)
	return err
}

func (s *Store) DeleteSession(hash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE hash = ?`, hash)
	return err
}

func (s *Store) DeleteAllSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions`)
	return err
}

// ---- токены ----

func (s *Store) AddToken(name string, scopes []string) (string, error) {
	value := NewSecret("sema_")
	_, err := s.db.Exec(`INSERT INTO api_tokens (name, hash, scopes, created) VALUES (?, ?, ?, ?)`,
		name, HashSecret(value), strings.Join(scopes, ","), time.Now().Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return value, nil
}

func (s *Store) TokenByValue(value string) (*APIToken, bool) {
	t := &APIToken{}
	var scopes, created, used, revoked string
	if err := s.db.QueryRow(`SELECT id, name, hash, scopes, created, last_used, revoked, uses FROM api_tokens WHERE hash = ?`, HashSecret(value)).
		Scan(&t.ID, &t.Name, &t.Hash, &scopes, &created, &used, &revoked, &t.Uses); err != nil {
		return nil, false
	}
	t.Scopes = splitScopes(scopes)
	t.Created, t.LastUsed, t.Revoked = parseTime(created), parseTime(used), parseTime(revoked)
	return t, true
}

func (s *Store) TouchToken(id int64) {
	_, _ = s.db.Exec(`UPDATE api_tokens SET last_used = ?, uses = uses + 1 WHERE id = ?`, time.Now().Format(time.RFC3339), id)
}

func (s *Store) Tokens() ([]*APIToken, error) {
	rows, err := s.db.Query(`SELECT id, name, hash, scopes, created, last_used, revoked, uses FROM api_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t := &APIToken{}
		var scopes, created, used, revoked string
		if err := rows.Scan(&t.ID, &t.Name, &t.Hash, &scopes, &created, &used, &revoked, &t.Uses); err != nil {
			return nil, err
		}
		t.Scopes = splitScopes(scopes)
		t.Created, t.LastUsed, t.Revoked = parseTime(created), parseTime(used), parseTime(revoked)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevokeToken(id int64) error {
	_, err := s.db.Exec(`UPDATE api_tokens SET revoked = ? WHERE id = ?`, time.Now().Format(time.RFC3339), id)
	return err
}

func splitScopes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- журнал ----

func (s *Store) AddAccess(e *AccessEntry) {
	_, _ = s.db.Exec(`INSERT INTO access_log (at, who, method, path, status, bytes, seconds, ip, note) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Format(time.RFC3339), e.Who, e.Method, e.Path, e.Status, e.Bytes, e.Seconds, e.IP, e.Note)
}

func (s *Store) AccessLog(limit int) ([]*AccessEntry, error) {
	rows, err := s.db.Query(`SELECT id, at, who, method, path, status, bytes, seconds, ip, note FROM access_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AccessEntry
	for rows.Next() {
		e := &AccessEntry{}
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Who, &e.Method, &e.Path, &e.Status, &e.Bytes, &e.Seconds, &e.IP, &e.Note); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AccessStats — запросов и отказов у потребителя за период.
func (s *Store) AccessStats(who string, since time.Time) (requests, denied int, bytes int64) {
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END),0), COALESCE(SUM(bytes),0) FROM access_log WHERE who = ? AND at >= ?`,
		who, since.Format(time.RFC3339)).Scan(&requests, &denied, &bytes)
	return
}

func (s *Store) PruneAccessLog(keep time.Duration) {
	_, _ = s.db.Exec(`DELETE FROM access_log WHERE at < ?`, time.Now().Add(-keep).Format(time.RFC3339))
}

// ResetAuth — запасной путь: все ключи и сессии долой, токены остаются.
func (s *Store) ResetAuth() error {
	if _, err := s.db.Exec(`DELETE FROM passkeys`); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM sessions`)
	return err
}

// ---- фраза входа ----
//
// Одна активна разом: смена заменяет строку целиком, прежняя фраза
// перестаёт пускать с этого мгновения. Само значение фразы здесь не
// живёт никогда — только медленный хэш; восстановить фразу из base
// невозможно, это осознанное свойство, а не недоделка.

// SetLoginPhrase — новый хэш фразы взамен прежнего.
func (s *Store) SetLoginPhrase(hash string) error {
	_, err := s.db.Exec(`
INSERT INTO login_phrase (id, hash, created) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET hash = excluded.hash, created = excluded.created`,
		hash, time.Now().Format(time.RFC3339))
	return err
}

// LoginPhrase — хэш и когда заведена; ok=false — фразы ещё нет.
func (s *Store) LoginPhrase() (hash string, created time.Time, ok bool) {
	var c string
	if err := s.db.QueryRow(`SELECT hash, created FROM login_phrase WHERE id = 1`).Scan(&hash, &c); err != nil {
		return "", time.Time{}, false
	}
	return hash, parseTime(c), true
}

// ClearLoginPhrase — отозвать: фраза больше не пускает никого.
func (s *Store) ClearLoginPhrase() error {
	_, err := s.db.Exec(`DELETE FROM login_phrase WHERE id = 1`)
	return err
}
