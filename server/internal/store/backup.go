package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Копия и самоосмотр: то, что нужно `sema -backup` и `sema -restore` и
// чего нельзя сделать снаружи базы.

// SnapshotTo — согласованная копия базы в файл на работающей Сёме:
// журнал предзаписи сводится, затем VACUUM INTO пишет одним снимком.
func (s *Store) SnapshotTo(path string) error {
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

// Schema — таблицы и их колонки: по ним видно, что копия снята более
// новой версией, чем та, что её открывает.
func (s *Store) Schema() (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '%_fts%'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string][]string{}
	for _, t := range tables {
		cols, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, t))
		if err != nil {
			return nil, err
		}
		for cols.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt sql.NullString
			if err := cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				cols.Close()
				return nil, err
			}
			out[t] = append(out[t], name)
		}
		cols.Close()
		sort.Strings(out[t])
	}
	return out, nil
}

// legacyColumns — колонки, которые остались в старых базах от снесённых
// возможностей: SQLite их не убирает, а свежая схема уже не создаёт. Копия
// с ними — не «от более новой Сёмы». Проверено на живой базе 7 сентября
// 2026: `messages.receipt_scanned` (чеки переехали в модуль 3 сентября).
var legacyColumns = map[string]bool{
	"messages.receipt_scanned": true,
}

// legacyTables — таблицы снесённых возможностей: миграция (`migrateNewsGone`,
// `migrateTrackersGone`) убирает их из живой базы, а копия, снятая до
// выката, их ещё несёт. Такая копия — не «от более новой Сёмы»: при
// восстановлении миграции снимут остаток сами.
var legacyTables = map[string]bool{
	"search_seen":   true, // новости по темам, сняты 13 сентября 2026
	"search_topics": true,
	"trackers":      true, // трекеры задач, сняты 30 сентября 2026
	"tracker_tasks": true,
}

// SchemaNewerThan — что в копии есть такого, чего эта сборка не знает.
func SchemaNewerThan(backup, mine map[string][]string) []string {
	var extra []string
	for t, cols := range backup {
		known := map[string]bool{}
		for _, c := range mine[t] {
			known[c] = true
		}
		if _, ok := mine[t]; !ok {
			if !legacyTables[t] {
				extra = append(extra, "таблица "+t)
			}
			continue
		}
		for _, c := range cols {
			if !known[c] && !legacyColumns[t+"."+c] {
				extra = append(extra, t+"."+c)
			}
		}
	}
	sort.Strings(extra)
	return extra
}

// countedTables — предметные записи, чьи счётчики идут в опись.
var countedTables = []string{"messages", "attachments", "chats", "chat_msgs", "memories", "tasks", "scripts", "secrets", "notifications", "hosts", "passkeys"}

// Counts — сколько предметных записей в базе.
func (s *Store) Counts() map[string]int64 {
	out := map[string]int64{}
	for _, t := range countedTables {
		var n int64
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + t).Scan(&n); err == nil {
			out[t] = n
		}
	}
	return out
}

// Check — целостность и ссылки: integrity_check и foreign_key_check.
func (s *Store) Check() error {
	var result string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check: %s", result)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := map[string]int{}
	var order []string
	for rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err == nil {
			k := table + "→" + parent
			if counts[k] == 0 {
				order = append(order, k)
			}
			counts[k]++
		}
	}
	if len(order) > 0 {
		var troubles []string
		for _, k := range order {
			troubles = append(troubles, fmt.Sprintf("%s (строк %d)", k, counts[k]))
		}
		return fmt.Errorf("foreign_key_check: %s", strings.Join(troubles, ", "))
	}
	return nil
}

// AttachmentKeys — ключи файлов в хранилище: по ним копия понимает,
// какие файлы лежат у хозяина, а какие только в зеркале.
func (s *Store) AttachmentKeys() (keys []string, mirrored map[string]bool, err error) {
	rows, err := s.db.Query(`SELECT key, mirror FROM attachments WHERE key <> ''`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	mirrored = map[string]bool{}
	for rows.Next() {
		var k, m string
		if err := rows.Scan(&k, &m); err != nil {
			return nil, nil, err
		}
		keys = append(keys, k)
		if m != "" {
			mirrored[k] = true
		}
	}
	return keys, mirrored, nil
}

// MessagesWithReason — сколько писем стоят с этой причиной: «Здоровье»
// показывает, сколько работ ждёт доверенной модели.
func (s *Store) MessagesWithReason(reason string) int {
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM messages WHERE reason = ?`, reason).Scan(&n)
	return n
}
