package store

import (
	"path/filepath"
	"testing"
)

// Повторяем историю живой базы: attachments переименовали в attachments_v1,
// завели новую и снесли старую — att_embeddings осталась ссылаться в пустоту.
func TestRefsToDroppedTableAreFixed(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, q := range []string{
		`INSERT INTO attachments (message_id, filename, mime, size, path) VALUES (0, 'a', 'text/plain', 1, '')`,
		`INSERT INTO att_embeddings (attachment_id, vector) VALUES (1, x'00')`,
		`ALTER TABLE attachments RENAME TO attachments_v1`,
		`CREATE TABLE attachments (id INTEGER PRIMARY KEY AUTOINCREMENT, message_id INTEGER, filename TEXT, mime TEXT, size INTEGER, path TEXT)`,
		`INSERT INTO attachments (id, message_id, filename, mime, size, path) SELECT id, message_id, filename, mime, size, path FROM attachments_v1`,
		`DROP TABLE attachments_v1`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var sql string
	_ = st.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'att_embeddings'`).Scan(&sql)
	if !contains(sql, "attachments_v1") {
		t.Skip("эта версия SQLite не переписывает ссылки при переименовании — чинить нечего")
	}
	if err := st.migrateDanglingRefs(); err != nil {
		t.Fatal(err)
	}
	_ = st.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'att_embeddings'`).Scan(&sql)
	if contains(sql, "attachments_v1") {
		t.Fatalf("ссылка осталась: %s", sql)
	}
	var n int
	_ = st.db.QueryRow(`SELECT count(*) FROM att_embeddings`).Scan(&n)
	if n != 1 {
		t.Fatalf("строки потерялись: %d", n)
	}
	if err := st.Check(); err != nil {
		t.Fatalf("после починки: %v", err)
	}
}

func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
