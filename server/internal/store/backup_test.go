package store

import "testing"

// Остаток снесённой возможности в старой базе — не признак более новой
// Сёмы; а неизвестная колонка — признак.
func TestSchemaWithLeftoversIsNotNewer(t *testing.T) {
	mine := map[string][]string{"messages": {"id", "subject"}}
	old := map[string][]string{"messages": {"id", "subject", "receipt_scanned"}}
	if extra := SchemaNewerThan(old, mine); len(extra) != 0 {
		t.Fatalf("остаток принят за новую схему: %v", extra)
	}
	future := map[string][]string{"messages": {"id", "subject", "колонка_из_будущего"}, "новая_таблица": {"id"}}
	if extra := SchemaNewerThan(future, mine); len(extra) != 2 {
		t.Fatalf("новая схема не замечена: %v", extra)
	}
}

// Копия, снятая до выката снятия трекеров и новостей, несёт их таблицы:
// это остаток, а не признак более новой Сёмы.
func TestBackupWithRemovedFeatureTablesIsNotNewer(t *testing.T) {
	mine := map[string][]string{"messages": {"id"}}
	old := map[string][]string{
		"messages": {"id"}, "trackers": {"name"}, "tracker_tasks": {"tracker"},
		"search_seen": {"url"}, "search_topics": {"id"},
	}
	if extra := SchemaNewerThan(old, mine); len(extra) != 0 {
		t.Fatalf("остаток снятой возможности принят за новую схему: %v", extra)
	}
	// а таблица, которой сборка не знает и которая не из снятых, — по-прежнему новая
	old["новая_таблица"] = []string{"id"}
	if extra := SchemaNewerThan(old, mine); len(extra) != 1 {
		t.Fatalf("новая таблица не замечена: %v", extra)
	}
}
