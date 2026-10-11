package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// разовая проба: что в живой базе есть такого, чего нет в свежей схеме
func TestSchemaDiffProbe(t *testing.T) {
	dir := os.Getenv("SEMA_BACKUP_DIR")
	if dir == "" {
		t.Skip("SEMA_BACKUP_DIR не задан")
	}
	b, err := os.ReadFile(filepath.Join(dir, "опись.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ins Inventory
	_ = json.Unmarshal(b, &ins)
	fresh, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	mine, _ := fresh.Schema()
	t.Logf("лишнее в живой базе: %s", strings.Join(store.SchemaNewerThan(ins.Schema, mine), ", "))
}
