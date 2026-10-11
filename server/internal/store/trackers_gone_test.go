package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// Схема трекеров, как она лежала в живой базе до 30 сентября 2026: копия,
// снятая до выката, и сама база при первом старте новой сборки несут эти
// таблицы.
const oldTrackersSchema = `
CREATE TABLE trackers (
	name TEXT PRIMARY KEY, module TEXT NOT NULL, rule TEXT NOT NULL DEFAULT '{}',
	on_start TEXT NOT NULL DEFAULT '', on_done TEXT NOT NULL DEFAULT '',
	every_m INTEGER NOT NULL DEFAULT 15, enabled INTEGER NOT NULL DEFAULT 0,
	last TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', created TEXT NOT NULL
);
CREATE TABLE tracker_tasks (
	tracker TEXT NOT NULL, ext_id TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'new', chat INTEGER NOT NULL DEFAULT 0,
	task INTEGER NOT NULL DEFAULT 0, result TEXT NOT NULL DEFAULT '', created TEXT NOT NULL,
	PRIMARY KEY (tracker, ext_id)
);`

func tableExists(t *testing.T, st *Store, name string) bool {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func openFresh(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Свежая база трекерных таблиц не заводит: возможность снята целиком.
func TestFreshDatabaseHasNoTrackerTables(t *testing.T) {
	st := openFresh(t)
	for _, n := range []string{"trackers", "tracker_tasks"} {
		if tableExists(t, st, n) {
			t.Errorf("свежая схема завела таблицу %s", n)
		}
	}
}

// Старая база: таблицы с данными, задача сердцебиения, работы очереди,
// настройки и задача с происхождением из трекера. После миграции трекеров
// нет, чужое цело, а повторный запуск ничего не ломает и не пишет в журнал
// настроек «снято» ещё раз.
func TestTrackersGoneCleansOldDatabaseAndRepeats(t *testing.T) {
	st := openFresh(t)
	if _, err := st.db.Exec(oldTrackersSchema); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO trackers (name, module, created) VALUES ('Planner', 'sing', '2026-09-01T00:00:00Z')`,
		`INSERT INTO tracker_tasks (tracker, ext_id, title, created) VALUES ('Planner', 'T-1', 'проба', '2026-09-01T00:00:00Z')`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	beat := &Task{Title: "Трекеры задач", Kind: "prompt", Schedule: "каждые 5 мин", System: "trackers"}
	from := &Task{Title: "Проверить память", Kind: "prompt", Schedule: "разово", Once: true,
		Enabled: true, Origin: "tracker:Planner", Ref: "T-1"}
	own := &Task{Title: "Своя задача", Kind: "prompt", Schedule: "разово", Once: true}
	for _, task := range []*Task{beat, from, own} {
		if err := st.CreateTask(task); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Enqueue("tracker_poll", "Planner", "", 4); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue("file_parse", "1", "", 4); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"tracker.legacy": "1", "queue.pause_ms.tracker_poll": "9000", "queue.pause_ms.file_parse": "700",
	} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.migrateTrackersGone(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"trackers", "tracker_tasks"} {
		if tableExists(t, st, n) {
			t.Errorf("таблица %s осталась", n)
		}
	}
	tasks, err := st.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]*Task{}
	for _, x := range tasks {
		byTitle[x.Title] = x
	}
	if byTitle["Трекеры задач"] != nil {
		t.Error("задача сердцебиения трекеров осталась")
	}
	if x := byTitle["Проверить память"]; x == nil || x.Origin != "" || x.Ref != "" {
		t.Errorf("задача из трекера не стала обычной: %+v", x)
	}
	if byTitle["Своя задача"] == nil {
		t.Error("чужая задача пропала")
	}
	var polls, others int
	_ = st.db.QueryRow(`SELECT count(*) FROM work_queue WHERE kind = 'tracker_poll'`).Scan(&polls)
	_ = st.db.QueryRow(`SELECT count(*) FROM work_queue WHERE kind <> 'tracker_poll'`).Scan(&others)
	if polls != 0 || others != 1 {
		t.Errorf("очередь: опросов трекеров %d (ждали 0), прочих работ %d (ждали 1)", polls, others)
	}
	for _, k := range []string{"tracker.legacy", "queue.pause_ms.tracker_poll"} {
		if v := st.Setting(k, "нет"); v != "нет" {
			t.Errorf("настройка %s осталась: %q", k, v)
		}
	}
	if v := st.Setting("queue.pause_ms.file_parse", ""); v != "700" {
		t.Errorf("чужой предел скорости задет: %q", v)
	}

	// повтор: ничего не ломает и не пишет в журнал настроек снова
	before := len(changesByKey(t, st, "tracker.legacy"))
	if err := st.migrateTrackersGone(); err != nil {
		t.Fatalf("повторный запуск: %v", err)
	}
	if after := len(changesByKey(t, st, "tracker.legacy")); after != before {
		t.Errorf("повторный запуск снова пишет в журнал настроек: было %d, стало %d", before, after)
	}
	if tasks2, _ := st.Tasks(); len(tasks2) != len(tasks) {
		t.Errorf("повторный запуск изменил число задач: %d → %d", len(tasks), len(tasks2))
	}
}

// Пустые таблицы (так и лежит живая база) и полное отсутствие таблиц
// сносятся без ошибок.
func TestTrackersGoneOnEmptyAndMissingTables(t *testing.T) {
	st := openFresh(t)
	if err := st.migrateTrackersGone(); err != nil { // таблиц уже нет
		t.Fatal(err)
	}
	if _, err := st.db.Exec(oldTrackersSchema); err != nil {
		t.Fatal(err)
	}
	if err := st.migrateTrackersGone(); err != nil { // таблицы пусты
		t.Fatal(err)
	}
	if tableExists(t, st, "trackers") || tableExists(t, st, "tracker_tasks") {
		t.Error("пустые таблицы не сняты")
	}
}

// Грабли `ALTER TABLE … RENAME`: если на таблицу трекеров кто-то ссылается,
// её не сносят, чтобы не оставить ссылку в пустоту; остальное снимается, а
// когда ссылка уйдёт, следующий запуск довершит.
func TestTrackersGoneKeepsReferencedTable(t *testing.T) {
	st := openFresh(t)
	if _, err := st.db.Exec(oldTrackersSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TABLE trackers_child (tracker TEXT REFERENCES "trackers"(name))`); err != nil {
		t.Fatal(err)
	}
	if err := st.migrateTrackersGone(); err != nil {
		t.Fatal(err)
	}
	if !tableExists(t, st, "trackers") {
		t.Fatal("таблица, на которую есть ссылка, снесена")
	}
	if tableExists(t, st, "tracker_tasks") {
		t.Error("свободная таблица не снята")
	}
	if _, err := st.db.Exec(`DROP TABLE trackers_child`); err != nil {
		t.Fatal(err)
	}
	if err := st.migrateTrackersGone(); err != nil {
		t.Fatal(err)
	}
	if tableExists(t, st, "trackers") {
		t.Error("после снятия ссылки таблица не снесена")
	}
	if strings.Contains(schemaText(t, st), "trackers") {
		t.Error("в схеме остались следы трекеров")
	}
}

func schemaText(t *testing.T, st *Store) string {
	t.Helper()
	rows, err := st.db.Query(`SELECT COALESCE(sql, '') FROM sqlite_master`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		b.WriteString(s + "\n")
	}
	return b.String()
}

// Сама проводка: старая база с трекерами при обычном открытии, как при
// старте сервера или проверке копии, приходит без них.
func TestOpenDropsTrackerTablesOfOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(oldTrackersSchema); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if tableExists(t, st, "trackers") || tableExists(t, st, "tracker_tasks") {
		t.Fatal("открытие старой базы оставило таблицы трекеров")
	}
	if err := st.Check(); err != nil {
		t.Fatalf("после миграции: %v", err)
	}
}
