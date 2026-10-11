package backup

// Копия с самоосмотром. `sema -backup файл.tgz` снимает согласованный
// снимок базы, файлы и опись — версию, схему, sha256 каждого файла и
// счётчики. Ключ шифрования в копию не кладётся никогда. `sema -restore
// файл.tgz` не «как-нибудь открывает», а проверяет: хэши, integrity_check,
// foreign_key_check, миграции, пробная расшифровка, счётчики — и отвечает
// одной строкой: «проверено» или «не сошлось» с причиной.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/host"
	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/version"
)

// Inventory — что лежит в копии и как это проверить.
type Inventory struct {
	TakenAt    string              `json:"снята"`
	Version    string              `json:"версия"`
	Schema     map[string][]string `json:"схема"`
	Counts     map[string]int64    `json:"счётчики"`
	DB         string              `json:"база_sha256"`
	Files      map[string]string   `json:"файлы"`      // путь внутри files/ → sha256
	NoFiles    bool                `json:"без_файлов"` // режим «только база»
	MirrorOnly []string            `json:"только_в_зеркале,omitempty"`
	Host       string              `json:"хозяин"`
}

const dbInBackup = "sema.db"

func DBPath(conf *config.Config) string {
	if conf.DB != "" {
		return conf.DB
	}
	return filepath.Join(conf.Data, "sema.db")
}

func FilesPath(conf *config.Config) string {
	d := conf.Files.Dir
	if d == "" {
		d = "files"
	}
	if !filepath.IsAbs(d) {
		d = filepath.Join(conf.Data, d)
	}
	return d
}

// Make — копия с работающей Сёмы: замок не берём, база открывается
// второй связью, снимок делает VACUUM INTO.
func Make(conf *config.Config, dst string, noFiles bool) error {
	// живая база открывается только для чтения и без миграций: её держит
	// сервер, и второй мигрирующий процесс — та самая беда, от которой замок
	live, err := store.OpenReadOnly(DBPath(conf))
	if err != nil {
		return fmt.Errorf("база: %w", err)
	}
	tmp, err := os.MkdirTemp(conf.Data, ".backup-*")
	if err != nil {
		live.Close()
		return err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, dbInBackup)
	if err := live.SnapshotTo(snap); err != nil {
		live.Close()
		return fmt.Errorf("снимок базы: %w", err)
	}
	keys, mirrored, _ := live.AttachmentKeys()
	live.Close()
	// схема и счётчики — по снимку: между снимком и подсчётом на живой базе
	// успевает прийти письмо, и восстановление честно скажет «не сошлось»
	st, err := store.OpenReadOnly(snap)
	if err != nil {
		return err
	}
	defer st.Close()
	schema, err := st.Schema()
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	inv := Inventory{TakenAt: time.Now().Format(time.RFC3339), Version: version.String(), Schema: schema,
		Counts: st.Counts(), Files: map[string]string{}, NoFiles: noFiles, Host: hostname}
	if inv.DB, err = sha256File(snap); err != nil {
		return err
	}
	// файлы: что лежит у хозяина — в копию с хэшем; что только в зеркале —
	// названо поимённо, чтобы восстановление не искало их молча
	root := FilesPath(conf)
	for _, k := range keys {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(k))); err != nil && mirrored[k] {
			inv.MirrorOnly = append(inv.MirrorOnly, k)
		}
	}
	if !noFiles {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			sum, err := sha256File(p)
			if err == nil {
				inv.Files[filepath.ToSlash(rel)] = sum
			}
			return nil
		})
	}
	// архив
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(out, h))
	tw := tar.NewWriter(gz)
	describe, _ := json.MarshalIndent(inv, "", " ")
	if err := writeToTar(tw, "опись.json", describe); err != nil {
		return err
	}
	if err := fileToTar(tw, dbInBackup, snap); err != nil {
		return err
	}
	for rel := range inv.Files {
		if err := fileToTar(tw, "files/"+rel, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	// sha256 архива — рядом, отдельным файлом: внутрь себя его не положить
	sum := hex.EncodeToString(h.Sum(nil))
	if err := os.WriteFile(dst+".sha256", []byte(sum+"  "+filepath.Base(dst)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Println("копия без ключа неполна: ключ (или фраза восстановления) хранится отдельно от копии — без него секреты, пароли ящиков и ключи моделей не прочитаются")
	fmt.Printf("копия снята: %s · сборка %s · база %d записей писем, %d файлов в копии, %d только в зеркале · sha256 %s\n",
		dst, inv.Version, inv.Counts["messages"], len(inv.Files), len(inv.MirrorOnly), sum[:12])
	return nil
}

// Итог самоосмотра копии: что проверено и что не сошлось.
type Inspection struct {
	Inventory Inventory
	Checks    []string
	Troubles  []string
}

// Inspect — распаковать во временный каталог и проверить всё, что
// можно проверить без ключа; с ключом — ещё и пробную расшифровку.
func Inspect(path, key, tmpDir string) (*Inspection, string, error) {
	ins := &Inspection{}
	// 1. sha256 архива, если рядом лежит файл с суммой
	if b, err := os.ReadFile(path + ".sha256"); err == nil {
		want := strings.Fields(string(b))
		sum, err := sha256File(path)
		if err != nil {
			return nil, "", err
		}
		if len(want) > 0 && want[0] != sum {
			ins.Troubles = append(ins.Troubles, "sha256 архива не сходится с "+filepath.Base(path)+".sha256")
		} else {
			ins.Checks = append(ins.Checks, "sha256 архива")
		}
	} else {
		ins.Checks = append(ins.Checks, "sha256 архива не проверялся (нет файла "+filepath.Base(path)+".sha256)")
	}
	tmp, err := os.MkdirTemp(tmpDir, "sema-restore-*")
	if err != nil {
		return nil, "", err
	}
	if err := unpack(path, tmp); err != nil {
		os.RemoveAll(tmp)
		return nil, "", fmt.Errorf("архив: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(tmp, "опись.json"))
	if err != nil {
		os.RemoveAll(tmp)
		return nil, "", fmt.Errorf("в копии нет описи: %w", err)
	}
	if err := json.Unmarshal(b, &ins.Inventory); err != nil {
		os.RemoveAll(tmp)
		return nil, "", fmt.Errorf("опись не читается: %w", err)
	}
	// 2. хэши базы и файлов
	if sum, err := sha256File(filepath.Join(tmp, dbInBackup)); err != nil || sum != ins.Inventory.DB {
		ins.Troubles = append(ins.Troubles, "sha256 базы не сходится с описью")
	} else {
		ins.Checks = append(ins.Checks, "sha256 базы")
	}
	broken := 0
	for rel := range ins.Inventory.Files {
		// ключ из описи — тоже путь: «../x» из подделанной описи писал бы
		// мимо каталога файлов
		if clean := filepath.Clean(filepath.FromSlash(rel)); strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			ins.Troubles = append(ins.Troubles, "в описи путь наружу: "+rel)
			return ins, tmp, nil
		}
	}
	for rel, want := range ins.Inventory.Files {
		if sum, err := sha256File(filepath.Join(tmp, "files", filepath.FromSlash(rel))); err != nil || sum != want {
			broken++
		}
	}
	if broken > 0 {
		ins.Troubles = append(ins.Troubles, fmt.Sprintf("файлов с неверным sha256 или отсутствующих: %d", broken))
	} else if len(ins.Inventory.Files) > 0 {
		ins.Checks = append(ins.Checks, fmt.Sprintf("sha256 файлов · %d", len(ins.Inventory.Files)))
	}
	// 3–4. база: целостность, ссылки, схема не новее нашей, миграции
	base := filepath.Join(tmp, dbInBackup)
	if ours, err := store.Open(filepath.Join(tmp, "схема-этой-сборки.db")); err == nil {
		mine, _ := ours.Schema()
		ours.Close()
		if extra := store.SchemaNewerThan(ins.Inventory.Schema, mine); len(extra) > 0 {
			ins.Troubles = append(ins.Troubles, "копия снята более новой Сёмой ("+ins.Inventory.Version+"): эта сборка не знает "+strings.Join(extra, ", ")+" — обновите Сёму, а не копию")
			return ins, tmp, nil
		}
		ins.Checks = append(ins.Checks, "схема не новее этой сборки")
	}
	st, err := store.Open(base) // Open проводит миграции до нынешней версии
	if err != nil {
		ins.Troubles = append(ins.Troubles, "база не открывается или миграции не прошли: "+err.Error())
		return ins, tmp, nil
	}
	defer st.Close()
	ins.Checks = append(ins.Checks, "миграции")
	if err := st.Check(); err != nil {
		ins.Troubles = append(ins.Troubles, err.Error())
	} else {
		ins.Checks = append(ins.Checks, "integrity_check и foreign_key_check")
	}
	// 5. пробная расшифровка: любой секрет прочитался ключом — ключ тот
	if key != "" {
		if box, err := secret.NewFromEnv(key); err == nil && box != nil {
			st.UseSecretBox(box)
		}
		if ok, known := st.KeyMatches(); known && !ok {
			hint := ""
			if secret.LooksLikePhrase(key) {
				if _, perr := secret.ParsePhrase(key); perr != nil {
					hint = " (" + perr.Error() + ")"
				}
			}
			ins.Troubles = append(ins.Troubles, "секреты не расшифровываются этим ключом: копия от другой установки или фраза не та"+hint)
		} else if known {
			ins.Checks = append(ins.Checks, "пробная расшифровка")
		}
	}
	// 6. счётчики
	now := st.Counts()
	var diff []string
	for t, n := range ins.Inventory.Counts {
		if now[t] != n {
			diff = append(diff, fmt.Sprintf("%s %d≠%d", t, now[t], n))
		}
	}
	if len(diff) > 0 {
		ins.Troubles = append(ins.Troubles, "счётчики не сходятся с описью: "+strings.Join(diff, ", "))
	} else {
		ins.Checks = append(ins.Checks, "счётчики")
	}
	return ins, tmp, nil
}

// Restore — самоосмотр, и только при «проверено» копия ложится в
// каталог данных. Сёма при этом должна быть остановлена: берём её замок.
func Restore(conf *config.Config, path string) error {
	if err := os.MkdirAll(conf.Data, 0o755); err != nil {
		return err
	}
	ins, tmp, err := Inspect(path, os.Getenv(conf.KeyEnvName()), conf.Data)
	if tmp != "" {
		defer os.RemoveAll(tmp)
	}
	if err != nil {
		return err
	}
	if len(ins.Troubles) > 0 {
		fmt.Printf("не сошлось: %s\n", strings.Join(ins.Troubles, "; "))
		return fmt.Errorf("копия не прошла самоосмотр")
	}
	lock, err := host.Acquire(conf.Data)
	if err != nil {
		return fmt.Errorf("замок каталога не взялся (%v): либо Сёма работает — остановите её, либо нет прав на каталог %s", err, conf.Data)
	}
	defer lock.Release()
	// сначала файлы, потом база: если файлы не доехали, прежняя база на месте
	root := FilesPath(conf)
	for rel := range ins.Inventory.Files {
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(tmp, "files", filepath.FromSlash(rel)), dst); err != nil {
			return fmt.Errorf("файл %s не доехал, база не тронута: %w", rel, err)
		}
	}
	// база встаёт на место одним переименованием: полуписаной sema.db не бывает
	fresh := DBPath(conf) + ".new"
	if err := copyFile(filepath.Join(tmp, dbInBackup), fresh); err != nil {
		return err
	}
	stamp := time.Now().Format("2006-01-02-150405")
	if _, err := os.Stat(DBPath(conf)); err == nil {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Rename(DBPath(conf)+suffix, DBPath(conf)+".до-восстановления-"+stamp+suffix)
		}
	}
	if err := os.Rename(fresh, DBPath(conf)); err != nil {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Rename(DBPath(conf)+".до-восстановления-"+stamp+suffix, DBPath(conf)+suffix)
		}
		return fmt.Errorf("база не встала на место, прежняя возвращена: %w", err)
	}
	// после переезда ключ проверяется на месте
	if err := os.WriteFile(filepath.Join(conf.Data, "восстановлено.txt"), []byte("копия "+filepath.Base(path)+" · снята "+ins.Inventory.TakenAt+" сборкой "+ins.Inventory.Version+" на "+ins.Inventory.Host+"\n"), 0o644); err != nil {
		return err
	}
	tail := ""
	if n := len(ins.Inventory.MirrorOnly); n > 0 {
		tail = fmt.Sprintf(" · %d файлов есть только в зеркале S3 — без него они недоступны", n)
	}
	if ins.Inventory.NoFiles {
		tail += " · копия без файлов"
	}
	fmt.Printf("проверено: %s · записей писем %d · сборка копии %s%s\n", strings.Join(ins.Checks, ", "), ins.Inventory.Counts["messages"], ins.Inventory.Version, tail)
	return nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeToTar(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func fileToTar(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: fi.Size(), ModTime: fi.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func unpack(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// имя из архива не должно вывести за каталог распаковки
		clean := filepath.Clean(filepath.FromSlash(h.Name))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("подозрительный путь в архиве: %s", h.Name)
		}
		dst := filepath.Join(dst, clean)
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// RotateKey — `sema -rekey`: свежая копия обязательна, старый ключ
// проверяется по vault.check, перешифровка идёт на снимке базы, и только
// удавшийся снимок ложится на место — прервалось, прежний ключ действует.
func RotateKey(conf *config.Config, freshBackup string) (string, error) {
	fi, err := os.Stat(freshBackup)
	if err != nil {
		return "", fmt.Errorf("нужна свежая копия (-fresh-backup): %w", err)
	}
	if time.Since(fi.ModTime()) > time.Hour {
		return "", fmt.Errorf("копия %s старше часа — снимите новую: sema -backup", freshBackup)
	}
	if b, err := os.ReadFile(freshBackup + ".sha256"); err == nil {
		if sum, err := sha256File(freshBackup); err != nil || len(strings.Fields(string(b))) == 0 || strings.Fields(string(b))[0] != sum {
			return "", fmt.Errorf("sha256 копии не сходится с %s.sha256", filepath.Base(freshBackup))
		}
	}
	old, err := secret.NewFromEnv(os.Getenv(conf.KeyEnvName()))
	if err != nil || old == nil {
		return "", fmt.Errorf("нынешний ключ: %s пуст", conf.KeyEnvName())
	}
	lock, err := host.Acquire(conf.Data)
	if err != nil {
		return "", fmt.Errorf("замок каталога не взялся (%v): либо Сёма работает — остановите её, либо нет прав на каталог %s", err, conf.Data)
	}
	defer lock.Release()
	st, err := store.Open(DBPath(conf))
	if err != nil {
		return "", err
	}
	st.UseSecretBox(old)
	if ok, known := st.KeyMatches(); known && !ok {
		st.Close()
		return "", fmt.Errorf("нынешний ключ не подходит к базе: смена не начата")
	}
	tmp, err := os.MkdirTemp(conf.Data, ".rekey-*")
	if err != nil {
		st.Close()
		return "", err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, dbInBackup)
	if err := st.SnapshotTo(snap); err != nil {
		st.Close()
		return "", fmt.Errorf("снимок базы: %w", err)
	}
	st.Close()
	phrase, err := secret.NewPhrase()
	if err != nil {
		return "", err
	}
	freshKey, err := secret.New(phrase)
	if err != nil {
		return "", err
	}
	// перешифровка на снимке: живая база не трогается, пока всё не удалось
	snapDB, err := store.Open(snap)
	if err != nil {
		return "", err
	}
	snapDB.UseSecretBox(old)
	if err := snapDB.RotateKey(freshKey); err != nil {
		snapDB.Close()
		return "", fmt.Errorf("перешифровка не удалась, прежний ключ действует: %w", err)
	}
	if err := snapDB.SetSetting(store.VaultCheckKey, ""); err != nil {
		snapDB.Close()
		return "", err
	}
	if err := snapDB.EnsureKeyCheck(); err != nil {
		snapDB.Close()
		return "", err
	}
	if ok, _ := snapDB.KeyMatches(); !ok {
		snapDB.Close()
		return "", fmt.Errorf("новый ключ не проверился на снимке, прежний ключ действует")
	}
	snapDB.Close()
	// фраза печатается до замены базы: убитый посреди замены процесс не
	// должен оставить базу под фразой, которой никто не видел
	fmt.Printf("новая фраза восстановления — запишите её отдельно от копий, база сейчас встанет под неё:\n\n    %s\n\n", phrase)
	stamp := time.Now().Format("2006-01-02-150405")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Rename(DBPath(conf)+suffix, DBPath(conf)+".до-смены-ключа-"+stamp+suffix)
	}
	if err := os.Rename(snap, DBPath(conf)); err != nil {
		// снимок не встал на место — возвращаем прежнюю базу
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Rename(DBPath(conf)+".до-смены-ключа-"+stamp+suffix, DBPath(conf)+suffix)
		}
		return "", fmt.Errorf("база не заменилась, прежний ключ действует: %w", err)
	}
	fmt.Println("ключ сменён.")
	fmt.Printf("запускайте Сёму с %s=<эта фраза>. Прежняя база лежит рядом как %s; копии, снятые до смены, открываются прежней фразой.\n", conf.KeyEnvName(), filepath.Base(DBPath(conf))+".до-смены-ключа-"+stamp)
	return phrase, nil
}
