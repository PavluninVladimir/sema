package store

// Поиск по почте.
//
// До сих пор его не было вовсе: письмо можно было найти либо глазами в
// дайджесте за окно, либо через чат — по смысловой похожести. Второе
// зависит от эмбеддера, а он на Raspberry Pi засыпает вместе с локальной
// моделью, и тогда поиск исчезал целиком. Архив из 1700 писем, в котором
// нельзя найти письмо, — это не архив.
//
// Здесь обычный полнотекстовый поиск SQLite (FTS5, тот же токенизатор, что
// у памяти). Он не понимает смысла — и не должен: «Горэнерго» ищется
// по слову, а не по вектору, и работает, когда все модели спят.
//
// Тело письма приезжает отдельной работой позже темы, поэтому запись в
// указателе обновляется дважды: при появлении письма и при появлении тела.

import (
	"strings"
	"unicode"
)

func (s *Store) migrateMailSearch() {
	_, _ = s.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		subject, sender, body, tokenize = 'unicode61 remove_diacritics 2')`)
}

// IndexMessage — положить письмо в указатель. Зовётся и при появлении
// письма, и когда доехало тело: rowid — это id письма, поэтому повторная
// запись заменяет прежнюю, а не плодит вторую.
func (s *Store) IndexMessage(id int64, subject, sender, body string) {
	if id <= 0 {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM messages_fts WHERE rowid = ?`, id)
	_, _ = s.db.Exec(`INSERT INTO messages_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)`,
		id, subject, sender, clipRunes(body, 20000))
}

// SearchMessages — письма по словам. Свежие первыми: в почте почти всегда
// ищут недавнее, а совпадение по FTS у писем одинаково хорошее. о — отсев
// этой сборки (снимок на устройстве); nil — не исключать никого, как раньше.
func (s *Store) SearchMessages(query, account string, ex *Exclusion, limit int) ([]*Message, error) {
	q := FTSQuery(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	sql := selectMsg + ` WHERE id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`
	args := []any{q}
	if account != "" {
		if ex.SkipsAccount(account) {
			return nil, nil
		}
		sql += ` AND account = ?`
		args = append(args, account)
	}
	if clause, cargs := ex.Clause(); clause != "" {
		sql += clause
		args = append(args, cargs...)
	}
	sql += ` ORDER BY date DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MessagesToIndex — письма, которых ещё нет в указателе.
func (s *Store) MessagesToIndex(limit int) ([]*Message, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(selectMsg+`
WHERE id NOT IN (SELECT rowid FROM messages_fts) ORDER BY date DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// FTSQuery — человеческий запрос в выражение FTS5.
//
// Отдавать введённое напрямую нельзя: кавычка, звёздочка или слово NOT
// превращают запрос в синтаксическую ошибку, и поиск отвечает отказом на
// то, что человек просто набрал. Поэтому берём только слова, каждое
// заключаем в кавычки и склеиваем через AND — «счёт горэнерго»
// значит «оба слова», как этого и ждут.
//
// Звёздочку в конце слова оставляем: «ульянов*» — привычный способ искать
// по началу слова, и он единственный, который здесь пропущен нарочно.
func FTSQuery(query string) string {
	var words []string
	for _, w := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '*' && r != '@' && r != '.'
	}) {
		star := strings.HasSuffix(w, "*")
		w = strings.Trim(w, "*")
		if w == "" {
			continue
		}
		// кавычки внутри слова сломали бы выражение — их и не бывает в
		// словах, но проверка дешевле разбирательства
		w = strings.ReplaceAll(w, `"`, "")
		if w == "" {
			continue
		}
		if star {
			words = append(words, `"`+w+`"*`)
			continue
		}
		words = append(words, `"`+w+`"`)
	}
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, " AND ")
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
