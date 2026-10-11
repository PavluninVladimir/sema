package store

// Отпечаток таблиц: по одному хэшу на таблицу. Нужен проверкам, которые обязаны
// доказать «ничего не записано»: сборка индекса настроек, например, обещает не
// менять ни одной строки в базе, и обещание проверяется сравнением отпечатков до
// и после, а не верой. Читает всё подряд — на живой базе не зовётся.

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"
)

// TablesDigest — хэш содержимого каждой таблицы базы (строки в порядке rowid).
func (s *Store) TablesDigest() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	rows.Close()
	sort.Strings(names)
	out := map[string]string{}
	for _, name := range names {
		sum, err := s.tableDigest(name)
		if err != nil {
			// виртуальные таблицы полнотекстового поиска читаются не всегда; такая
			// таблица — служебная копия, и отпечаток ей не нужен
			continue
		}
		out[name] = sum
	}
	return out, nil
}

func (s *Store) tableDigest(name string) (string, error) {
	rows, err := s.db.Query(`SELECT * FROM "` + name + `" ORDER BY rowid`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		for _, v := range vals {
			fmt.Fprintf(h, "%v\x1f", v)
		}
		fmt.Fprint(h, "\n")
	}
	if err := rows.Err(); err != nil && err != sql.ErrNoRows {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
