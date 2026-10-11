package store

import (
	"fmt"
	"log"
	"regexp"
	"strings"
)

// Трекеры задач сняты 30 сентября 2026 по решению владельца: справочник
// `trackers`, взятые задачи `tracker_tasks`, задача сердцебиения «Трекеры
// задач» и вид очереди `tracker_poll`. Миграция убирает их следы из живой
// базы; вход в чужие серверы MCP (`mcp_servers`) остаётся — это другое.
//
// Разовая и повторяемая: на пустых и уже чистых таблицах ничего не делает.
// Свежая схема трекерных таблиц не создаёт.
func (s *Store) migrateTrackersGone() error {
	// Задачи, взятые из трекера, не пропадают молча: работа по ним могла
	// идти, у каждой есть чат. Они становятся обычными — без происхождения и
	// внешнего номера — и остаются на «Задачах».
	res, err := s.db.Exec(`UPDATE tasks SET origin = '', ref = '' WHERE origin LIKE 'tracker:%'`)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("трекеры сняты: задач с происхождением «трекер» — %d, теперь они обычные и остались на «Задачах»", n)
	}
	for _, q := range []string{
		`DELETE FROM tasks WHERE system = 'trackers'`,
		`DELETE FROM work_queue WHERE kind = 'tracker_poll'`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// настройки — через дверь, чтобы правка легла в журнал изменений; и
	// только существующие, иначе каждый старт писал бы в журнал «снято»
	var gone []string
	for k := range s.SettingsByPrefix("tracker.") {
		gone = append(gone, k)
	}
	const pauseKey = "queue.pause_ms.tracker_poll" // свой предел скорости у вида работы
	if _, ok := s.SettingsByPrefix(pauseKey)[pauseKey]; ok {
		gone = append(gone, pauseKey)
	}
	for _, k := range gone {
		s.DeleteSettingKey(k)
	}
	return s.dropTablesIfUnreferenced("tracker_tasks", "trackers")
}

// dropTablesIfUnreferenced — снести таблицы снятой возможности, но только
// те, на которые не ссылается ни одна другая. Причина — грабли переименования:
// `ALTER TABLE … RENAME` молча переписывает чужие `REFERENCES`, и после
// `DROP` ссылка остаётся в пустоту (`migrateDanglingRefs`). Ссылающаяся
// таблица — это чужие данные: пусть таблица лежит и молча ждёт человека,
// чем база уйдёт в «не найдено» на первой же записи. Следующий старт
// проверит снова.
func (s *Store) dropTablesIfUnreferenced(names ...string) error {
	rows, err := s.db.Query(`SELECT name, sql FROM sqlite_master WHERE sql LIKE '%REFERENCES%'`)
	if err != nil {
		return err
	}
	type decl struct{ name, sql string }
	var all []decl
	for rows.Next() {
		var d decl
		if err := rows.Scan(&d.name, &d.sql); err != nil {
			rows.Close()
			return err
		}
		all = append(all, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	dropping := map[string]bool{}
	for _, n := range names {
		dropping[n] = true
	}
	for _, n := range names {
		// имя в кавычках любого вида тоже ссылка
		ref := regexp.MustCompile("(?i)references\\s*[\"'`\\[]?" + regexp.QuoteMeta(n) + `\b`)
		var by []string
		for _, d := range all {
			if !dropping[d.name] && ref.MatchString(d.sql) {
				by = append(by, d.name)
			}
		}
		if len(by) > 0 {
			log.Printf("трекеры сняты: таблицу %s не сношу — на неё ссылается %s", n, strings.Join(by, ", "))
			continue
		}
		if _, err := s.db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, n)); err != nil {
			return err
		}
	}
	return nil
}
