package store

import (
	"strings"
	"time"
)

// Ссылки на файлы (изменение file-share-links). Ссылка подписана ключом из
// хранилища секретов, а строка здесь — её выдача: кто, откуда, срок, отзыв,
// открытия. Подпись без строки не открывает ничего: отзыв проверяется после
// подписи.

type FileShare struct {
	ID       int64
	File     int64
	Expires  string // RFC3339
	Who      string // кто выдал: «вы», «модель · чат «…»», «модуль «…»»
	Origin   string // откуда: экран файла, чат, модуль
	Chat     int64  // чат, где выдана (карточка с «Отозвать»)
	Revoked  string // когда отозвана; пусто — действует
	Opens    int
	LastOpen string
	Created  string
}

// Active — ссылка ещё действует: не отозвана и срок не вышел.
func (f *FileShare) Active(now time.Time) bool {
	if f.Revoked != "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, f.Expires)
	return err == nil && now.Before(t)
}

func (s *Store) migrateShares() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS file_shares (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		file      INTEGER NOT NULL,
		expires   TEXT NOT NULL,
		who       TEXT NOT NULL DEFAULT '',
		origin    TEXT NOT NULL DEFAULT '',
		chat      INTEGER NOT NULL DEFAULT 0,
		revoked   TEXT NOT NULL DEFAULT '',
		opens     INTEGER NOT NULL DEFAULT 0,
		last_open TEXT NOT NULL DEFAULT '',
		created   TEXT NOT NULL
	)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_file_shares_file ON file_shares(file)`)
	return err
}

const shareCols = `id, file, expires, who, origin, chat, revoked, opens, last_open, created`

func scanShare(row interface{ Scan(...any) error }) (*FileShare, error) {
	var f FileShare
	if err := row.Scan(&f.ID, &f.File, &f.Expires, &f.Who, &f.Origin, &f.Chat, &f.Revoked, &f.Opens, &f.LastOpen, &f.Created); err != nil {
		return nil, err
	}
	return &f, nil
}

// AddFileShare — запись о выдаче ссылки.
func (s *Store) AddFileShare(f *FileShare) (int64, error) {
	f.Created = time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`INSERT INTO file_shares (file, expires, who, origin, chat, created) VALUES (?, ?, ?, ?, ?, ?)`,
		f.File, f.Expires, f.Who, f.Origin, f.Chat, f.Created)
	if err != nil {
		return 0, err
	}
	f.ID, err = res.LastInsertId()
	if err == nil {
		s.touch(ScreenFiles)
	}
	return f.ID, err
}

// FileShare — выдача по номеру.
func (s *Store) FileShare(id int64) (*FileShare, bool) {
	f, err := scanShare(s.db.QueryRow(`SELECT `+shareCols+` FROM file_shares WHERE id = ?`, id))
	return f, err == nil
}

// FileSharesOf — ссылки на файл, свежие первыми.
func (s *Store) FileSharesOf(file int64) ([]FileShare, error) {
	rows, err := s.db.Query(`SELECT `+shareCols+` FROM file_shares WHERE file = ? ORDER BY id DESC`, file)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileShare
	for rows.Next() {
		f, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// ActiveSharesFor — действующие ссылки на файлы списка, одним запросом:
// строка «Файлов» не спрашивает базу на каждый файл.
func (s *Store) ActiveSharesFor(files []int64) map[int64][]FileShare {
	out := map[int64][]FileShare{}
	if len(files) == 0 {
		return out
	}
	q := `SELECT ` + shareCols + ` FROM file_shares WHERE revoked = '' AND expires > ? AND file IN (?` + strings.Repeat(`, ?`, len(files)-1) + `) ORDER BY id DESC`
	args := []any{time.Now().UTC().Format(time.RFC3339)}
	for _, id := range files {
		args = append(args, id)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		if f, err := scanShare(rows); err == nil {
			out[f.File] = append(out[f.File], *f)
		}
	}
	return out
}

// RevokeFileShares — все действующие ссылки на файл.
func (s *Store) RevokeFileShares(file int64) error {
	_, err := s.db.Exec(`UPDATE file_shares SET revoked = ? WHERE file = ? AND revoked = ''`,
		time.Now().UTC().Format(time.RFC3339), file)
	if err == nil {
		s.touch(ScreenFiles)
	}
	return err
}

// RevokeFileShare — ссылка больше не открывает файл.
func (s *Store) RevokeFileShare(id int64) error {
	_, err := s.db.Exec(`UPDATE file_shares SET revoked = ? WHERE id = ? AND revoked = ''`,
		time.Now().UTC().Format(time.RFC3339), id)
	if err == nil {
		s.touch(ScreenFiles)
	}
	return err
}

// NoteFileShareOpen — ещё одно открытие.
func (s *Store) NoteFileShareOpen(id int64) {
	_, _ = s.db.Exec(`UPDATE file_shares SET opens = opens + 1, last_open = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
}
