package store

// Коды, найденные на картинке или странице PDF: QR чека ФНС, марка
// «Честного знака», штрих-код товара.
//
// Код лежит отдельно от извлечённого текста нарочно. Текст — то, что
// прочитали буквами, и в нём можно ошибиться: «55830.00» с фотографии
// становится «5583О». Код — то, что в картинку заложили машиной и что
// сходится по контрольной сумме. Смешать их в одно поле значило бы
// уравнять догадку и точное число.

import (
	"encoding/json"
	"strings"
	"time"
)

// Форматы, в которые разбирается значение кода. Неизвестное остаётся
// текстом: выдумывать формат хуже, чем честно сказать «просто значение».
const (
	CodeFNS       = "фнс"              // чек ФНС: t, s, fn, i, fp
	CodeMark      = "честный-знак"     // GS1 DataMatrix: GTIN плюс серийный номер
	CodeGTIN      = "gtin"             // EAN/UPC: номер товара
	CodeURL       = "адрес"            // http(s)
	CodeSecretURL = "адрес-с-секретом" // в адресе токен: значение скрыто
	CodeText      = "текст"            // что-то ещё
	CodeNone      = "нет"              // искали и не нашли — это тоже итог
)

// Codes — настоящие коды без отметки «искали и не нашли». Отметка нужна
// карточке файла, а модели, модулю и поиску — нет.
func Codes(list []FileCode) []FileCode {
	var out []FileCode
	for _, c := range list {
		if c.Format != CodeNone {
			out = append(out, c)
		}
	}
	return out
}

// NotFound — отметка о безуспешном поиске, если она есть.
func NotFound(list []FileCode) (FileCode, bool) {
	for _, c := range list {
		if c.Format == CodeNone {
			return c, true
		}
	}
	return FileCode{}, false
}

// FileCode — один код на файле.
type FileCode struct {
	ID        int64
	FileID    int64
	Symbology string // qr | datamatrix | aztec | ean13 | ean8 | upca | code128 …
	Value     string // значение как есть; у адреса с секретом — уже без секрета
	Format    string // один из Code* выше
	Fields    []CodeField
	Page      int // страница PDF, с которой код прочитан; 0 — картинка целиком
}

// CodeField — разобранное поле кода парой «подпись — значение»: порядок
// полей важен человеку, поэтому это список, а не карта.
type CodeField struct {
	Label string `json:"подпись"`
	Value string `json:"значение"`
}

// Field — значение поля по подписи; пусто, если такого поля нет.
func (c *FileCode) Field(label string) string {
	for _, f := range c.Fields {
		if f.Label == label {
			return f.Value
		}
	}
	return ""
}

func (s *Store) migrateFileCodes() error {
	// без REFERENCES на attachments нарочно: таблицу вложений уже
	// переименовывали, и SQLite тогда молча переправил чужие внешние
	// ключи, а после DROP ссылка осталась в пустоту (см. «Грабли»)
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS file_codes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			file_id    INTEGER NOT NULL,
			symbology  TEXT NOT NULL DEFAULT '',
			value      TEXT NOT NULL DEFAULT '',
			format     TEXT NOT NULL DEFAULT '',
			fields     TEXT NOT NULL DEFAULT '[]',
			page       INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_file_codes_file ON file_codes(file_id)`,
		`CREATE INDEX IF NOT EXISTS idx_file_codes_value ON file_codes(value)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// SaveFileCodes заменяет коды файла найденными сейчас. Замена, а не
// дописывание: файл перечитывают («Перечитать вложения», новый движок), и
// дописывание оставило бы по три одинаковых кода на одном чеке.
func (s *Store) SaveFileCodes(fileID int64, codes []FileCode) error {
	if fileID == 0 {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM file_codes WHERE file_id = ?`, fileID); err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	for _, c := range codes {
		raw, err := json.Marshal(c.Fields)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`INSERT INTO file_codes (file_id, symbology, value, format, fields, page, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, fileID, c.Symbology, c.Value, c.Format, string(raw), c.Page, now); err != nil {
			return err
		}
	}
	if len(codes) > 0 {
		s.touch(ScreenFiles)
	}
	return nil
}

// FileCodes — коды одного файла.
func (s *Store) FileCodes(fileID int64) []FileCode {
	rows, err := s.db.Query(`SELECT id, file_id, symbology, value, format, fields, page
		FROM file_codes WHERE file_id = ? ORDER BY page, id`, fileID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []FileCode
	for rows.Next() {
		if c, err := scanCode(rows); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// FileCodesFor — коды сразу многих файлов: список «Файлов» рисует капсулу
// у каждой строки, и запрос на строку превратил бы экран в N+1.
func (s *Store) FileCodesFor(ids []int64) map[int64][]FileCode {
	out := map[int64][]FileCode{}
	if len(ids) == 0 {
		return out
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT id, file_id, symbology, value, format, fields, page
		FROM file_codes WHERE file_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY page, id`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		if c, err := scanCode(rows); err == nil {
			out[c.FileID] = append(out[c.FileID], c)
		}
	}
	return out
}

type dbRow interface {
	Scan(dest ...any) error
}

func scanCode(r dbRow) (FileCode, error) {
	var c FileCode
	var fields string
	if err := r.Scan(&c.ID, &c.FileID, &c.Symbology, &c.Value, &c.Format, &fields, &c.Page); err != nil {
		return c, err
	}
	// поле не разобралось — код всё равно отдаём: значение важнее разбора
	_ = json.Unmarshal([]byte(fields), &c.Fields)
	return c, nil
}

// FileIDsByCode — файлы, у которых код совпадает с запросом. Запрос «ФД
// 4719» ищется по словам: одно слово — подпись поля, другое — значение.
// Совпасть должны все слова, иначе «ФД 4719» находил бы каждый чек, у
// которого вообще есть ФД.
func (s *Store) FileIDsByCode(query string, limit int) []int64 {
	words := strings.Fields(strings.TrimSpace(query))
	if len(words) == 0 || len(words) > 5 {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT DISTINCT file_id FROM file_codes WHERE format <> '` + CodeNone + `' AND `
	var args []any
	for i, w := range words {
		if i > 0 {
			q += " AND "
		}
		q += `(value LIKE ? OR fields LIKE ?)`
		args = append(args, "%"+w+"%", "%"+w+"%")
	}
	q += ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// CountFileCodes — сколько кодов и на скольких файлах: строка на
// «Инструментах», чтобы было видно, работает ли распознавание вообще.
func (s *Store) CountFileCodes() (codes, files int) {
	_ = s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT file_id) FROM file_codes WHERE format <> ?`,
		CodeNone).Scan(&codes, &files)
	return codes, files
}
