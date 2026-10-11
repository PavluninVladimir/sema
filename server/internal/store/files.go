package store

// Файлы — самостоятельные записи Сёмы: хэш содержимого, источники (письмо
// с ящиком, загрузка, папка, модуль) и расположение (хранилище и ключ).
// Письмо — необязательная ссылка. Физическая таблица по-прежнему называется
// attachments, но message_id может быть 0, а строки без письма — норма.

import (
	"fmt"
	"strings"
	"time"
)

// Виды источников файла.
const (
	SourceMail   = "mail"     // ref — id письма
	SourceUpload = "upload"   // загрузка через сервер (перетащили в окно)
	SourceFolder = "folder"   // ref — путь папки
	SourceModule = "module"   // ref — имя модуля
	SourceWeb    = "web"      // ref — адрес страницы: из ответа поиска, из ссылки в чате, по просьбе модуля
	SourceTG     = "telegram" // ref — «учётка/чат/сообщение», account — имя собеседника
	SourceMAX    = "max"      // то же для MAX: ref — «чат/сообщение»
)

type FileSource struct {
	Kind    string
	Ref     string
	Account string
	// Added — когда источник принёс это содержимое в последний раз: тот же
	// файл, принесённый снова, отмечается заново (`TouchFileSource`). У
	// страницы из интернета по этому времени решается, не пора ли скачать её
	// опять
	Added time.Time
}

func (s *Store) migrateFiles() error {
	for _, q := range []string{
		// письмо у файла необязательно: пересобираем таблицу без NOT NULL на message_id
		`CREATE TABLE IF NOT EXISTS files_new (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id INTEGER NOT NULL DEFAULT 0,
			filename   TEXT NOT NULL,
			mime       TEXT NOT NULL DEFAULT '',
			size       INTEGER NOT NULL DEFAULT 0,
			path       TEXT NOT NULL DEFAULT '',
			extracted  TEXT NOT NULL DEFAULT '',
			kind       TEXT NOT NULL DEFAULT 'document',
			vendor     TEXT NOT NULL DEFAULT '',
			amount     REAL NOT NULL DEFAULT 0,
			currency   TEXT NOT NULL DEFAULT '',
			summary    TEXT NOT NULL DEFAULT '',
			sha256     TEXT NOT NULL DEFAULT '',
			storage    TEXT NOT NULL DEFAULT '',
			key        TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS file_sources (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			file_id INTEGER NOT NULL,
			kind    TEXT NOT NULL,
			ref     TEXT NOT NULL DEFAULT '',
			account TEXT NOT NULL DEFAULT '',
			added   TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_file_sources_file ON file_sources(file_id)`,
		`CREATE TABLE IF NOT EXISTS storages (
			name       TEXT PRIMARY KEY,
			kind       TEXT NOT NULL,
			config     TEXT NOT NULL DEFAULT '{}',
			is_primary INTEGER NOT NULL DEFAULT 0,
			created    TEXT NOT NULL
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("файлы: %w", err)
		}
	}
	// один раз: старые вложения переезжают в новую таблицу с теми же id,
	// чтобы векторы att_embeddings и данные модулей остались верными
	if s.Setting("files_table_v2", "") != "1" {
		var hasOld int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='attachments'`).Scan(&hasOld)
		if hasOld > 0 {
			var cols int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('attachments') WHERE name='sha256'`).Scan(&cols)
			if cols == 0 { // старая форма таблицы
				if _, err := s.db.Exec(`INSERT INTO files_new (id, message_id, filename, mime, size, path, extracted, kind, vendor, amount, currency, summary, created_at)
					SELECT id, message_id, filename, mime, size, path, extracted, kind, vendor, amount, currency, summary, created_at FROM attachments`); err != nil {
					return fmt.Errorf("переезд вложений: %w", err)
				}
				if _, err := s.db.Exec(`INSERT INTO file_sources (file_id, kind, ref, account, added)
					SELECT a.id, 'mail', CAST(a.message_id AS TEXT), COALESCE(m.account, ''), COALESCE(m.date, '')
					FROM attachments a LEFT JOIN messages m ON m.id = a.message_id`); err != nil {
					return fmt.Errorf("источники вложений: %w", err)
				}
				if _, err := s.db.Exec(`ALTER TABLE attachments RENAME TO attachments_v1`); err != nil {
					return err
				}
			}
		}
		if _, err := s.db.Exec(`ALTER TABLE files_new RENAME TO attachments`); err != nil {
			return err
		}
		if err := s.SetSetting("files_table_v2", "1"); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS idx_att_message ON attachments(message_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_files_sha ON attachments(sha256) WHERE sha256 <> ''`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// остатки прошлого: старая форма таблицы вложений (переезд завершён и
	// сверен 3 сентября 2026), таблицы чеков и категорий ядра (переехали в
	// модуль «Чеки»), рабочая таблица переезда
	for _, q := range []string{
		`DROP TABLE IF EXISTS attachments_v1`,
		`DROP TABLE IF EXISTS receipts`,
		`DROP TABLE IF EXISTS categories`,
		`DROP TABLE IF EXISTS files_new`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("уборка: %w", err)
		}
	}
	// метки снесённых чеков убираются дверью, а не своим SQL: таблицу
	// настроек пишет ровно одно место, и оно ведёт журнал изменений
	for _, key := range []string{"categories_seeded", "receipts_merge_days"} {
		s.DeleteSettingKey(key)
	}
	// локальное хранилище — основное по умолчанию; это заполнение справочника
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM storages`).Scan(&n)
	if n == 0 {
		_, err := s.db.Exec(`INSERT INTO storages (name, kind, config, is_primary, created) VALUES ('local', 'local', '{}', 1, ?)`,
			time.Now().Format(time.RFC3339))
		return err
	}
	return nil
}

// FileBySHA — файл с таким содержимым уже есть?
func (s *Store) FileBySHA(sha string) (*Attachment, bool) {
	if sha == "" {
		return nil, false
	}
	a, err := scanAtt(s.db.QueryRow(selectAtt+` WHERE a.sha256 = ?`, sha))
	if err != nil {
		return nil, false
	}
	return a, true
}

// SetFileLocation — хэш, хранилище и ключ файла (после переезда или загрузки).
func (s *Store) SetFileLocation(id int64, sha, storage, key, path string) error {
	_, err := s.db.Exec(`UPDATE attachments SET sha256 = ?, storage = ?, key = ?, path = ? WHERE id = ?`, sha, storage, key, path, id)
	return err
}

// AddFileSource дописывает источник, если такого ещё нет.
func (s *Store) AddFileSource(fileID int64, kind, ref, account string) error {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM file_sources WHERE file_id = ? AND kind = ? AND ref = ?`, fileID, kind, ref).Scan(&n)
	if n > 0 {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO file_sources (file_id, kind, ref, account, added) VALUES (?, ?, ?, ?, ?)`,
		fileID, kind, ref, account, time.Now().Format(time.RFC3339))
	return err
}

// TouchFileSource — источник принёс то же содержимое ещё раз: отметка
// «когда» обновляется. Без этого страница, что не менялась, считалась бы
// вечно старой (файл-то один, и записан он давно) и качалась при каждом
// обращении. Источника нет — ничего не делает.
func (s *Store) TouchFileSource(fileID int64, kind, ref string) error {
	_, err := s.db.Exec(`UPDATE file_sources SET added = ? WHERE file_id = ? AND kind = ? AND ref = ?`,
		time.Now().Format(time.RFC3339), fileID, kind, ref)
	return err
}

// AgeFileSource — назначить источнику время, когда он принёс файл: проверка
// срока годности страницы не ждёт часов (так же `AgePlace` у памяти).
func (s *Store) AgeFileSource(fileID int64, kind, ref string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE file_sources SET added = ? WHERE file_id = ? AND kind = ? AND ref = ?`,
		at.Format(time.RFC3339), fileID, kind, ref)
	return err
}

// LatestFileByRef — файл, который источник с такой ссылкой принёс последним,
// и когда это было. Страница со временем меняется, и файлов у одного адреса
// бывает несколько; последний — тот, что принесли позже всех, а не с
// бо́льшим номером: страница, вернувшаяся к прежнему тексту, снова отмечает
// старый файл.
func (s *Store) LatestFileByRef(kind, ref string) (*Attachment, time.Time, bool) {
	rows, err := s.db.Query(`SELECT file_id, added FROM file_sources WHERE kind = ? AND ref = ? ORDER BY id`, kind, ref)
	if err != nil {
		return nil, time.Time{}, false
	}
	var best int64
	var at time.Time
	for rows.Next() {
		var id int64
		var added string
		if rows.Scan(&id, &added) != nil {
			continue
		}
		if t := parseTime(added); best == 0 || !t.Before(at) {
			best, at = id, t
		}
	}
	rows.Close()
	if best == 0 {
		return nil, time.Time{}, false
	}
	att, err := s.AttachmentByID(best)
	if err != nil {
		return nil, time.Time{}, false
	}
	return att, at, true
}

func (s *Store) FileSources(fileID int64) []FileSource {
	rows, err := s.db.Query(`SELECT kind, ref, account, added FROM file_sources WHERE file_id = ? ORDER BY id`, fileID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []FileSource
	for rows.Next() {
		var fs FileSource
		var added string
		if rows.Scan(&fs.Kind, &fs.Ref, &fs.Account, &added) == nil {
			fs.Added = parseTime(added)
			out = append(out, fs)
		}
	}
	return out
}

// FileSourcesFor — источники сразу для набора файлов.
func (s *Store) FileSourcesFor(ids []int64) map[int64][]FileSource {
	out := map[int64][]FileSource{}
	if len(ids) == 0 {
		return out
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT file_id, kind, ref, account, added FROM file_sources WHERE file_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY id`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var fs FileSource
		var added string
		if rows.Scan(&id, &fs.Kind, &fs.Ref, &fs.Account, &added) == nil {
			fs.Added = parseTime(added)
			out[id] = append(out[id], fs)
		}
	}
	return out
}

// FilesBySource — файлы с источником такого вида (upload, folder, module).
func (s *Store) FilesBySource(kind string, limit int) ([]*Attachment, error) {
	rows, err := s.db.Query(selectAtt+` WHERE a.id IN (SELECT file_id FROM file_sources WHERE kind = ?) ORDER BY a.id DESC LIMIT ?`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FilesWithoutLocation — старые вложения, у которых ещё нет хэша и ключа:
// их переносит задача «Переезд файлов».
func (s *Store) FilesWithoutLocation(limit int) ([]*Attachment, error) {
	rows, err := s.db.Query(selectAtt+` WHERE a.sha256 = '' ORDER BY a.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CountFilesWithoutLocation() (left, total int) {
	_ = s.db.QueryRow(`SELECT SUM(CASE WHEN sha256 = '' THEN 1 ELSE 0 END), COUNT(*) FROM attachments`).Scan(&left, &total)
	return
}

// MergeFileInto — файл с тем же содержимым уже есть: источники и векторы
// дубля переходят к оригиналу, дубль удаляется.
func (s *Store) MergeFileInto(dupID, keepID int64) error {
	if _, err := s.db.Exec(`UPDATE file_sources SET file_id = ? WHERE file_id = ?`, keepID, dupID); err != nil {
		return err
	}
	// один и тот же источник у слитого файла — одна строка
	_, _ = s.db.Exec(`DELETE FROM file_sources WHERE file_id = ? AND id NOT IN (SELECT MIN(id) FROM file_sources WHERE file_id = ? GROUP BY kind, ref)`, keepID, keepID)
	_, _ = s.db.Exec(`DELETE FROM att_embeddings WHERE attachment_id = ?`, dupID)
	_, err := s.db.Exec(`DELETE FROM attachments WHERE id = ?`, dupID)
	return err
}

// FileStats — сколько файлов, байт и источников какого вида.
func (s *Store) FileStats() (total int, bytes int64, bySource map[string]int) {
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM attachments`).Scan(&total, &bytes)
	bySource = map[string]int{}
	rows, err := s.db.Query(`SELECT kind, COUNT(DISTINCT file_id) FROM file_sources GROUP BY kind`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if rows.Scan(&k, &n) == nil {
			bySource[k] = n
		}
	}
	return
}

// ---- чеки ----
//
// Отдельной таблицы чеков у ядра нет: чек — это сам файл с видом
// AttKindReceipt и разобранными полями (получатель, сумма, валюта, итог).
// Письмо у файла необязательно, а связь с письмом бывает двух видов:
// прямая (attachments.message_id) и через источник «письмо» (file_sources,
// kind = mail, ref — id письма). Файл с одним содержимым живёт одной
// записью, поэтому у чека из двух писем в источниках стоят оба.

// FileReceipt — чек так, как его знает ядро: поля самого файла.
type FileReceipt struct {
	FileID   int64
	Vendor   string
	Amount   float64
	Currency string
	Summary  string
}

// receiptsByMessage — оба пути от файла к письму одним запросом.
// %s подставляет список мест для id писем, kind передаётся дважды.
const receiptsByMessage = `
SELECT msg, id, vendor, amount, currency, summary FROM (
	SELECT a.message_id AS msg, a.id AS id, a.vendor AS vendor, a.amount AS amount,
	       a.currency AS currency, a.summary AS summary
	FROM attachments a
	WHERE a.kind = ? AND a.message_id IN (%s)
	UNION
	SELECT CAST(fs.ref AS INTEGER), a.id, a.vendor, a.amount, a.currency, a.summary
	FROM attachments a JOIN file_sources fs ON fs.file_id = a.id AND fs.kind = 'mail'
	WHERE a.kind = ? AND CAST(fs.ref AS INTEGER) IN (%s)
)
ORDER BY CASE WHEN amount > 0 THEN 0 ELSE 1 END, id`

// ReceiptsForMessages — чек каждого письма из набора. У письма бывает
// несколько файлов-чеков; берётся первый, у которого разобрана сумма, —
// именно её показывает сводка «Почты».
func (s *Store) ReceiptsForMessages(ids []int64) (map[int64]*FileReceipt, error) {
	out := map[int64]*FileReceipt{}
	if len(ids) == 0 {
		return out, nil
	}
	places := "?" + strings.Repeat(",?", len(ids)-1)
	args := make([]any, 0, 2*len(ids)+2)
	for i := 0; i < 2; i++ { // список писем идёт в обе половины запроса
		args = append(args, AttKindReceipt)
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := s.db.Query(fmt.Sprintf(receiptsByMessage, places, places), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var msg int64
		r := &FileReceipt{}
		if err := rows.Scan(&msg, &r.FileID, &r.Vendor, &r.Amount, &r.Currency, &r.Summary); err != nil {
			return nil, err
		}
		if msg == 0 || out[msg] != nil {
			continue
		}
		out[msg] = r
	}
	return out, rows.Err()
}

// SendersWithReceipts — у кого из отправителей есть чеки и сколько.
// Считаются файлы-чеки, а не письма: у письма их может быть несколько.
func (s *Store) SendersWithReceipts() (map[string]int, error) {
	out := map[string]int{}
	rows, err := s.db.Query(`
SELECT from_addr, COUNT(DISTINCT file_id) FROM (
	SELECT m.from_addr AS from_addr, a.id AS file_id
	FROM attachments a JOIN messages m ON m.id = a.message_id
	WHERE a.kind = ?
	UNION
	SELECT m.from_addr, a.id
	FROM attachments a JOIN file_sources fs ON fs.file_id = a.id AND fs.kind = 'mail'
	JOIN messages m ON m.id = CAST(fs.ref AS INTEGER)
	WHERE a.kind = ?
)
WHERE from_addr <> ''
GROUP BY from_addr`, AttKindReceipt, AttKindReceipt)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var addr string
		var n int
		if err := rows.Scan(&addr, &n); err != nil {
			return out, err
		}
		out[strings.ToLower(addr)] += n
	}
	return out, rows.Err()
}

// ReceiptStats — сколько чеков, на какую сумму и за какой срок. Дата чека —
// дата письма, а у файла без письма — дата записи.
func (s *Store) ReceiptStats() (count int, total float64, first, last string) {
	_ = s.db.QueryRow(`
SELECT COUNT(*), COALESCE(SUM(a.amount), 0),
       COALESCE(MIN(COALESCE(NULLIF(m.date, ''), a.created_at)), ''),
       COALESCE(MAX(COALESCE(NULLIF(m.date, ''), a.created_at)), '')
FROM attachments a LEFT JOIN messages m ON m.id = a.message_id
WHERE a.kind = ?`, AttKindReceipt).Scan(&count, &total, &first, &last)
	return count, total, cutDate(first), cutDate(last)
}

// ---- хранилища ----

type StorageConf struct {
	Name    string
	Kind    string
	Config  string
	Primary bool
	Created time.Time
}

func (s *Store) Storages() ([]*StorageConf, error) {
	rows, err := s.db.Query(`SELECT name, kind, config, is_primary, created FROM storages ORDER BY is_primary DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StorageConf
	for rows.Next() {
		c := &StorageConf{}
		var p int
		var created string
		if err := rows.Scan(&c.Name, &c.Kind, &c.Config, &p, &created); err != nil {
			return nil, err
		}
		c.Primary = p == 1
		c.Created = parseTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) PrimaryStorage() string {
	var name string
	_ = s.db.QueryRow(`SELECT name FROM storages WHERE is_primary = 1`).Scan(&name)
	if name == "" {
		return "local"
	}
	return name
}

// ---- папки на диске ----

type FileFolder struct {
	ID          int64
	Path        string
	Enabled     bool
	DeleteAfter bool
	LastErr     string
	Taken       int
	Created     time.Time
}

func (s *Store) migrateFolders() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS file_folders (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			path         TEXT NOT NULL UNIQUE,
			enabled      INTEGER NOT NULL DEFAULT 1,
			delete_after INTEGER NOT NULL DEFAULT 0,
			last_err     TEXT NOT NULL DEFAULT '',
			taken        INTEGER NOT NULL DEFAULT 0,
			created      TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS folder_seen (
			path    TEXT PRIMARY KEY,
			size    INTEGER NOT NULL,
			mtime   TEXT NOT NULL,
			file_id INTEGER NOT NULL DEFAULT 0,
			seen    TEXT NOT NULL
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("папки с файлами: %w", err)
		}
	}
	// первичное заполнение: папка тома, выключена — человек включит, когда примонтирует
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM file_folders`).Scan(&n)
	if n == 0 && s.Setting("file_folders_seeded", "") != "1" {
		_, _ = s.db.Exec(`INSERT INTO file_folders (path, enabled, delete_after, created) VALUES ('/data/inbox', 0, 0, ?)`, time.Now().Format(time.RFC3339))
		_ = s.SetSetting("file_folders_seeded", "1")
	}
	return nil
}

func (s *Store) FileFolders() ([]*FileFolder, error) {
	rows, err := s.db.Query(`SELECT id, path, enabled, delete_after, last_err, taken, created FROM file_folders ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FileFolder
	for rows.Next() {
		f := &FileFolder{}
		var en, del int
		var created string
		if err := rows.Scan(&f.ID, &f.Path, &en, &del, &f.LastErr, &f.Taken, &created); err != nil {
			return nil, err
		}
		f.Enabled, f.DeleteAfter, f.Created = en == 1, del == 1, parseTime(created)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) AddFileFolder(path string, deleteAfter bool) error {
	_, err := s.db.Exec(`INSERT INTO file_folders (path, enabled, delete_after, created) VALUES (?, 1, ?, ?)
		ON CONFLICT(path) DO UPDATE SET enabled = 1, delete_after = excluded.delete_after`, path, b2i(deleteAfter), time.Now().Format(time.RFC3339))
	return err
}

func (s *Store) SetFileFolder(id int64, field string, on bool) error {
	switch field {
	case "enabled", "delete_after":
	default:
		return fmt.Errorf("поле %q", field)
	}
	_, err := s.db.Exec(`UPDATE file_folders SET `+field+` = ? WHERE id = ?`, b2i(on), id)
	return err
}

func (s *Store) NoteFileFolder(id int64, lastErr string, taken int) {
	_, _ = s.db.Exec(`UPDATE file_folders SET last_err = ?, taken = taken + ? WHERE id = ?`, lastErr, taken, id)
}

func (s *Store) DeleteFileFolder(id int64) error {
	_, err := s.db.Exec(`DELETE FROM file_folders WHERE id = ?`, id)
	return err
}

// FolderSeen — принимали ли этот файл в этом виде (путь, размер, время).
func (s *Store) FolderSeen(path string, size int64, mtime time.Time) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM folder_seen WHERE path = ? AND size = ? AND mtime = ?`, path, size, mtime.Format(time.RFC3339Nano)).Scan(&n)
	return n > 0
}

func (s *Store) MarkFolderSeen(path string, size int64, mtime time.Time, fileID int64) {
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO folder_seen (path, size, mtime, file_id, seen) VALUES (?, ?, ?, ?, ?)`,
		path, size, mtime.Format(time.RFC3339Nano), fileID, time.Now().Format(time.RFC3339))
}

// FilesByRef — файлы с источником такого вида и ссылки (например, загрузка в чат).
func (s *Store) FilesByRef(kind, ref string) ([]*Attachment, error) {
	rows, err := s.db.Query(selectAtt+` WHERE a.id IN (SELECT file_id FROM file_sources WHERE kind = ? AND ref = ?) ORDER BY a.id`, kind, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- хранилища и зеркало ----

func (s *Store) migrateMirror() error {
	_, _ = s.db.Exec(`ALTER TABLE attachments ADD COLUMN mirror TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE storages ADD COLUMN note TEXT NOT NULL DEFAULT ''`)
	return nil
}

// SetStorageConfig — переписать настройки заведённого хранилища. Дорогу
// для копий меняют здесь, а не заводя Диск второй раз: второе хранилище
// с тем же адресом это два зеркала на один Диск.
func (s *Store) SetStorageConfig(name, config string) error {
	res, err := s.db.Exec(`UPDATE storages SET config = ? WHERE name = ?`, config, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("хранилища %q нет", name)
	}
	return nil
}

func (s *Store) AddStorage(name, kind, config string) error {
	_, err := s.db.Exec(`INSERT INTO storages (name, kind, config, is_primary, created) VALUES (?, ?, ?, 0, ?)
		ON CONFLICT(name) DO UPDATE SET kind = excluded.kind, config = excluded.config`, name, kind, config, time.Now().Format(time.RFC3339))
	return err
}

func (s *Store) DeleteStorage(name string) error {
	if name == "local" {
		return fmt.Errorf("локальное хранилище не удаляется")
	}
	if _, err := s.db.Exec(`DELETE FROM storages WHERE name = ? AND is_primary = 0`, name); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE attachments SET mirror = '' WHERE mirror = ?`, name)
	return err
}

func (s *Store) NoteStorage(name, note string) {
	_, _ = s.db.Exec(`UPDATE storages SET note = ? WHERE name = ?`, note, name)
}

func (s *Store) StorageNote(name string) string {
	var n string
	_ = s.db.QueryRow(`SELECT note FROM storages WHERE name = ?`, name).Scan(&n)
	return n
}

// FilesToMirror — файлы с локальным расположением, ещё не уложенные в зеркало.
func (s *Store) FilesToMirror(mirror string, limit int) ([]*Attachment, error) {
	rows, err := s.db.Query(selectAtt+` WHERE a.storage <> '' AND a.key <> '' AND a.mirror <> ? ORDER BY a.id LIMIT ?`, mirror, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SetFileMirror(id int64, mirror string) error {
	_, err := s.db.Exec(`UPDATE attachments SET mirror = ? WHERE id = ?`, mirror, id)
	return err
}

// FilesToOffload — файлы, которые пора выгрузить с этой машины: старше
// срока, уже лежат в зеркале и ещё занимают место локально. Порядок — от
// старых к новым: место освобождается там, где оно залежалось дольше.
//
// Дата берётся у письма, а без письма — у самой записи: файл из папки или
// загрузки живёт своим возрастом.
func (s *Store) FilesToOffload(mirror, before string, limit int) ([]*Attachment, error) {
	rows, err := s.db.Query(selectAtt+`
		WHERE a.mirror = ? AND (a.storage <> '' OR a.path <> '')
		  AND COALESCE(NULLIF(m.date, ''), a.created_at) < ?
		ORDER BY COALESCE(NULLIF(m.date, ''), a.created_at)
		LIMIT ?`, mirror, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ClearFileLocal — местной копии больше нет: файл отдаётся из зеркала.
// Запись остаётся целиком, уходит только адрес на этой машине — иначе
// FileBytes продолжал бы искать по старому пути и молча отдавал ошибку.
func (s *Store) ClearFileLocal(id int64) error {
	_, err := s.db.Exec(`UPDATE attachments SET storage = '', path = '' WHERE id = ?`, id)
	return err
}

// OffloadedCount — сколько файлов уже отдаётся только из зеркала.
func (s *Store) OffloadedCount(mirror string) (n int, bytes int64) {
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size),0) FROM attachments
		WHERE mirror = ? AND storage = '' AND path = ''`, mirror).Scan(&n, &bytes)
	return
}

func (s *Store) MirrorProgress(mirror string) (done, total int) {
	_ = s.db.QueryRow(`SELECT SUM(CASE WHEN mirror = ? THEN 1 ELSE 0 END), COUNT(*) FROM attachments WHERE storage <> ''`, mirror).Scan(&done, &total)
	return
}

func (s *Store) FileMirror(id int64) string {
	var m string
	_ = s.db.QueryRow(`SELECT mirror FROM attachments WHERE id = ?`, id).Scan(&m)
	return m
}

// CountFiles — сколько всего файлов такого вида: без этого «показать
// ещё» не знает, сколько осталось, а человек не знает, что список не весь.
func (s *Store) CountFiles(kind, source string) int {
	var n int
	switch {
	case source != "":
		_ = s.db.QueryRow(`SELECT COUNT(DISTINCT a.id) FROM attachments a
			JOIN file_sources fs ON fs.file_id = a.id WHERE fs.kind = ?`, source).Scan(&n)
	case kind != "":
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE kind = ?`, kind).Scan(&n)
	default:
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&n)
	}
	return n
}

// SearchFiles — поиск по имени файла и извлечённому тексту. Ищет база, а
// не клиент: у клиента загружена только первая порция списка.
func (s *Store) SearchFiles(query string, limit int) ([]*Attachment, error) {
	if limit <= 0 {
		limit = 50
	}
	like := "%" + query + "%"
	q := selectAtt + ` WHERE a.filename LIKE ? OR a.extracted LIKE ?`
	args := []any{like, like}
	// по коду ищется отдельно: «ФД 4719» — это подпись поля и его
	// значение, а не подстрока текста, и LIKE по extracted такое не найдёт
	if ids := s.FileIDsByCode(query, limit); len(ids) > 0 {
		q += ` OR a.id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	q += ` ORDER BY COALESCE(m.date, a.created_at) DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
