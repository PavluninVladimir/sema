package store

import (
	"fmt"
	"strings"
)

// migrateDanglingRefs — чинит ссылки на снесённую таблицу. Когда
// `attachments` переименовывалась в `attachments_v1`, SQLite переписал
// внешние ключи в других таблицах на новое имя; потом `attachments_v1`
// удалили, и `att_embeddings` осталась ссылаться в пустоту. Живая работа
// этого не замечала (проверка внешних ключей выключена), а самоосмотр копии
// честно падал на каждой строке. Таблица пересобирается с ссылкой на
// `attachments`, строки без владельца отбрасываются.
func (s *Store) migrateDanglingRefs() error {
	rows, err := s.db.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table' AND sql LIKE '%attachments_v1%'`)
	if err != nil {
		return err
	}
	type t struct{ name, sql string }
	var list []t
	for rows.Next() {
		var x t
		if err := rows.Scan(&x.name, &x.sql); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		if x.name == "attachments_v1" {
			continue
		}
		fresh := strings.Replace(x.sql, "attachments_v1", "attachments", -1)
		fresh = strings.Replace(fresh, `CREATE TABLE "`+x.name+`"`, `CREATE TABLE "`+x.name+`_v2"`, 1)
		fresh = strings.Replace(fresh, "CREATE TABLE "+x.name, "CREATE TABLE "+x.name+"_v2", 1)
		if !strings.Contains(fresh, x.name+"_v2") {
			return fmt.Errorf("не разобрал объявление таблицы %s", x.name)
		}
		filter := ""
		if strings.Contains(x.sql, "attachment_id") {
			filter = " WHERE attachment_id IN (SELECT id FROM attachments)"
		}
		// одной транзакцией: обрыв между DROP и RENAME оставил бы базу без
		// таблицы, а следующий старт завёл бы пустую
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, q := range []string{
			fresh,
			`INSERT INTO ` + x.name + `_v2 SELECT * FROM ` + x.name + filter,
			`DROP TABLE ` + x.name,
			`ALTER TABLE ` + x.name + `_v2 RENAME TO ` + x.name,
		} {
			if _, err := tx.Exec(q); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("пересборка %s: %w", x.name, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
