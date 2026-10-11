package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func testConf(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	conf := &config.Config{KeyEnv: "SEMA_KEY"}
	conf.SetDataDir(dir, "тест")
	return conf
}

// Копия снимается, восстанавливается в другой каталог и проходит самоосмотр:
// база, файл с верным хэшем, счётчики, расшифровка секрета тем же ключом.
func TestBackupAndRestore(t *testing.T) {
	old, had := os.LookupEnv("SEMA_KEY")
	_ = os.Setenv("SEMA_KEY", "ключ-для-тестов")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEMA_KEY", old)
		} else {
			_ = os.Unsetenv("SEMA_KEY")
		}
	})
	src := testConf(t)
	st, err := store.Open(src.DB)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(os.Getenv("SEMA_KEY"))
	st.UseSecretBox(box)
	if err := st.SaveSecret(&store.Secret{Name: "проба", Kind: store.SecretToken}, "значение"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("проба.настройка", "да"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := os.MkdirAll(filepath.Join(src.Files.Dir, "ab"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.Files.Dir, "ab", "файл.txt"), []byte("вложение"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "копия.tgz")
	if err := Make(src, archive, false); err != nil {
		t.Fatalf("копия: %v", err)
	}
	if _, err := os.Stat(archive + ".sha256"); err != nil {
		t.Fatal("нет файла с sha256 архива")
	}

	dst := testConf(t)
	if err := Restore(dst, archive); err != nil {
		t.Fatalf("восстановление: %v", err)
	}
	st2, err := store.Open(dst.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	st2.UseSecretBox(box)
	if v, ok := st2.SecretValue("проба"); !ok || v != "значение" {
		t.Fatal("секрет после восстановления не читается тем же ключом")
	}
	if st2.Setting("проба.настройка", "") != "да" {
		t.Fatal("настройка не доехала")
	}
	if b, err := os.ReadFile(filepath.Join(dst.Files.Dir, "ab", "файл.txt")); err != nil || string(b) != "вложение" {
		t.Fatal("файл не доехал")
	}
}

// Испорченная копия не ложится в каталог: неверная сумма архива —
// «не сошлось», каталог данных остаётся пустым.
func TestCorruptedBackupNotRestored(t *testing.T) {
	src := testConf(t)
	st, err := store.Open(src.DB)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	archive := filepath.Join(t.TempDir(), "копия.tgz")
	if err := Make(src, archive, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive+".sha256", []byte("0000  копия.tgz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := testConf(t)
	if err := Restore(dst, archive); err == nil {
		t.Fatal("копия с неверной суммой восстановилась")
	}
	if _, err := os.Stat(dst.DB); err == nil {
		t.Fatal("база легла в каталог, хотя копия не прошла проверку")
	}
}

// Копия от более новой Сёмы отклоняется с объяснением, а не открывается
// «как-нибудь»: в описи есть колонка, которой эта сборка не знает.
func TestBackupFromNewerVersionRejected(t *testing.T) {
	src := testConf(t)
	st, err := store.Open(src.DB)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	archive := filepath.Join(t.TempDir(), "копия.tgz")
	if err := Make(src, archive, true); err != nil {
		t.Fatal(err)
	}
	// перепаковываем: та же база, опись с колонкой из будущего
	tmp := t.TempDir()
	if err := unpack(archive, tmp); err != nil {
		t.Fatal(err)
	}
	var inv Inventory
	b, _ := os.ReadFile(filepath.Join(tmp, "опись.json"))
	_ = json.Unmarshal(b, &inv)
	inv.Schema["messages"] = append(inv.Schema["messages"], "колонка_из_будущего")
	inv.Version = "v99"
	b, _ = json.MarshalIndent(inv, "", " ")
	newer := filepath.Join(t.TempDir(), "новая.tgz")
	f, _ := os.Create(newer)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = writeToTar(tw, "опись.json", b)
	_ = fileToTar(tw, dbInBackup, filepath.Join(tmp, dbInBackup))
	tw.Close()
	gz.Close()
	f.Close()
	ins, dir, err := Inspect(newer, "", t.TempDir())
	if dir != "" {
		defer os.RemoveAll(dir)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(ins.Troubles) == 0 || !strings.Contains(strings.Join(ins.Troubles, " "), "более новой") {
		t.Fatalf("копия из будущего не отклонена: %v", ins.Troubles)
	}
}

// архив с путём наружу не распаковывается
func TestArchiveWithPathOutsideRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "злой.tgz")
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = writeToTar(tw, "../../метка", []byte("x"))
	tw.Close()
	gz.Close()
	f.Close()
	if err := unpack(path, t.TempDir()); err == nil {
		t.Fatal("путь наружу прошёл")
	}
	_ = io.EOF
}

// Смена ключа: свежая копия обязательна, после смены секреты читаются
// новой фразой и не читаются старым ключом; прежняя база лежит рядом.
func TestKeyRotation(t *testing.T) {
	old, had := os.LookupEnv("SEMA_KEY")
	_ = os.Setenv("SEMA_KEY", "ключ-для-тестов")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEMA_KEY", old)
		} else {
			_ = os.Unsetenv("SEMA_KEY")
		}
	})
	conf := testConf(t)
	st, err := store.Open(conf.DB)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New("ключ-для-тестов")
	st.UseSecretBox(box)
	if err := st.SaveSecret(&store.Secret{Name: "проба", Kind: store.SecretToken}, "значение"); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureKeyCheck(); err != nil {
		t.Fatal(err)
	}
	st.Close()
	// без свежей копии — отказ
	if _, err := RotateKey(conf, filepath.Join(t.TempDir(), "нет.tgz")); err == nil {
		t.Fatal("смена без копии прошла")
	}
	archive := filepath.Join(t.TempDir(), "копия.tgz")
	if err := Make(conf, archive, true); err != nil {
		t.Fatal(err)
	}
	phrase, err := RotateKey(conf, archive)
	if err != nil {
		t.Fatalf("смена ключа: %v", err)
	}
	if _, err := secret.ParsePhrase(phrase); err != nil {
		t.Fatalf("напечатанная фраза не проходит проверку: %v", err)
	}
	st2, err := store.Open(conf.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	newBox, _ := secret.New(phrase)
	st2.UseSecretBox(newBox)
	if ok, known := st2.KeyMatches(); !known || !ok {
		t.Fatal("новая фраза не подходит к базе после смены")
	}
	if v, ok := st2.SecretValue("проба"); !ok || v != "значение" {
		t.Fatal("секрет не читается новой фразой")
	}
	st2.UseSecretBox(box)
	if ok, _ := st2.KeyMatches(); ok {
		t.Fatal("старый ключ всё ещё подходит")
	}
	m, _ := filepath.Glob(conf.DB + ".до-смены-ключа-*")
	if len(m) == 0 {
		t.Fatal("прежняя база не сохранена рядом")
	}
}
