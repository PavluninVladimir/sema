package store

// Код первой установки — место хранения (сам код, его вид и проверка — в
// `internal/auth/setup.go`).
//
// Код живёт только в памяти процесса и только хэшем: в базу он не попадает,
// поэтому перезапуск убивает его сам, а копия базы не уносит ничего, чем можно
// войти. Место — у хранилища, а не у входа: обычный и защищённый порт — два
// сервера с двумя объектами входа, а код, выданный при старте, обязан
// приниматься на обоих. Хранилище у них одно.

import (
	"sync"
	"time"
)

// SetupSessionNote — подпись сессии, открытой кодом первой установки. По ней
// такая сессия и узнаётся: она пускает только к заведению первого ключа, а с
// первым ключом закрывается (`DropSetupSessions`).
const SetupSessionNote = "вход по коду первой установки (только заведение ключа)"

// setupSlot — нынешний код: хэш и срок. Пусто — кода нет.
type setupSlot struct {
	mu    sync.Mutex
	hash  string
	until time.Time
}

// PutSetupCode — запомнить код этого запуска (хэшем) и его срок.
func (s *Store) PutSetupCode(hash string, until time.Time) {
	s.setup.mu.Lock()
	s.setup.hash, s.setup.until = hash, until
	s.setup.mu.Unlock()
}

// SetupCode — хэш нынешнего кода и срок; ok = false — кода нет.
func (s *Store) SetupCode() (hash string, until time.Time, ok bool) {
	s.setup.mu.Lock()
	defer s.setup.mu.Unlock()
	return s.setup.hash, s.setup.until, s.setup.hash != ""
}

// DropSetupCode — код больше не действует.
func (s *Store) DropSetupCode() {
	s.setup.mu.Lock()
	s.setup.hash, s.setup.until = "", time.Time{}
	s.setup.mu.Unlock()
}

// BySetupCode — сессия открыта кодом первой установки.
func (se *Session) BySetupCode() bool {
	return se != nil && se.Restricted && se.Note == SetupSessionNote
}

// DropSetupSessions — закрыть все сессии, открытые кодом первой установки:
// первый ключ заведён, и пускать к заведению ключей им больше незачем.
func (s *Store) DropSetupSessions() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE restricted = 1 AND note = ?`, SetupSessionNote)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
