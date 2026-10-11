package engine

// Прежняя защита наблюдаемых папок: «удалять после приёма» стирает из папки только
// тот файл, который принят без ошибки. Файл, который Сёма принять не смог, остаётся
// там, где лежал, — иначе единственная копия пропала бы. Экран «Хранилища» говорит
// об этом словами до включения; здесь — что движок так и делает.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFolderDeleteAfterKeepsFileThatWasNotAccepted(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.conf.Files.MaxSizeMB = 1
	dir := t.TempDir()
	old := time.Now().Add(-time.Minute) // файл, который ещё пишется (моложе десяти секунд), Сёма не берёт
	put := func(name string, size int) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := put("принять.txt", 100)
	tooBig := put("слишком-большой.bin", 2<<20)
	if err := st.AddFileFolder(dir, true); err != nil {
		t.Fatal(err)
	}
	e.foldersPass(context.Background())

	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Errorf("принятый файл остался в папке с «удалять после приёма»: %v", err)
	}
	if _, err := os.Stat(tooBig); err != nil {
		t.Errorf("файл, который не приняли, стёрт из папки: %v", err)
	}
	folders, _ := st.FileFolders()
	if len(folders) == 0 || !strings.Contains(folders[len(folders)-1].LastErr, "превышает предел") {
		t.Errorf("отказ приёма не записан в папку: %+v", folders)
	}
	// без «удалять после приёма» файл остаётся и после приёма
	keep := t.TempDir()
	stay := filepath.Join(keep, "останется.txt")
	if err := os.WriteFile(stay, []byte("остаться"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stay, old, old); err != nil {
		t.Fatal(err)
	}
	if err := st.AddFileFolder(keep, false); err != nil {
		t.Fatal(err)
	}
	e.foldersPass(context.Background())
	if _, err := os.Stat(stay); err != nil {
		t.Errorf("файл стёрт из папки без «удалять после приёма»: %v", err)
	}
}
