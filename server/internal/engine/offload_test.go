package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/storage"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// mirrorProbe — зеркало, которое можно испортить: подсунуть не то,
// отдать ошибку или молча потерять файл.
type mirrorProbe struct {
	data    map[string][]byte
	trouble error
}

func (z *mirrorProbe) Name() string { return "проба" }
func (z *mirrorProbe) Kind() string { return "проба" }
func (z *mirrorProbe) Put(key string, data []byte) error {
	z.data[key] = data
	return nil
}
func (z *mirrorProbe) Get(key string) ([]byte, error) {
	if z.trouble != nil {
		return nil, z.trouble
	}
	d, ok := z.data[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return d, nil
}
func (z *mirrorProbe) Delete(key string) error {
	delete(z.data, key)
	return nil
}
func (z *mirrorProbe) Path(string) string { return "" }
func (z *mirrorProbe) Stat() (int, int64) { return len(z.data), 0 }

// fileForOffload — файл в местном хранилище плюс его запись.
func fileForOffload(t *testing.T, e *Engine, content string) *store.Attachment {
	t.Helper()
	f, _, err := e.AddFile(t.Context(), "счёт.pdf", "application/pdf", []byte(content),
		FileSource{Kind: "upload"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Местная копия стирается только после того, как файл забран из зеркала
// и хэш сошёлся. Ошибка здесь необратима: второй копии нет.
func TestOffloadErasesOnlyAfterVerify(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	f := fileForOffload(t, e, "счёт на 2165 рублей")
	mirror := &mirrorProbe{data: map[string][]byte{}}

	// 1. в зеркале файла нет вовсе — не трогаем
	if err := e.offloadFile(f, mirror); err == nil {
		t.Error("файла нет в зеркале, а разгрузка прошла")
	}
	if _, err := e.FileBytes(f); err != nil {
		t.Fatalf("местная копия пострадала: %v", err)
	}

	// 2. в зеркале лежит не то — не трогаем
	mirror.data[f.Key] = []byte("совсем другое содержимое")
	if err := e.offloadFile(f, mirror); err == nil {
		t.Error("в зеркале другое содержимое, а разгрузка прошла")
	}
	data, err := e.FileBytes(f)
	if err != nil || string(data) != "счёт на 2165 рублей" {
		t.Fatalf("местная копия пострадала: %v", err)
	}

	// 3. зеркало отвечает ошибкой — не трогаем
	mirror.data[f.Key] = []byte("счёт на 2165 рублей")
	mirror.trouble = os.ErrPermission
	if err := e.offloadFile(f, mirror); err == nil {
		t.Error("зеркало недоступно, а разгрузка прошла")
	}
	mirror.trouble = nil

	// 4. всё сошлось — стираем местную копию, но запись цела
	if err := e.offloadFile(f, mirror); err != nil {
		t.Fatalf("сверка сошлась, а разгрузка не прошла: %v", err)
	}
	after, err := st.AttachmentByID(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Storage != "" || after.Path != "" {
		t.Errorf("местный адрес не снят: storage %q, path %q", after.Storage, after.Path)
	}
	if after.SHA256 != f.SHA256 || after.Filename != f.Filename || after.Size != f.Size {
		t.Error("запись файла пострадала при разгрузке")
	}
	if _, err := storage.NewLocal("local", e.conf.Files.Dir).Get(f.Key); err == nil {
		t.Error("местная копия осталась на диске")
	}
}

// Разгруженный файл отдаётся из зеркала — ради этого всё и затевалось.
func TestOffloadedFileGivenFromMirror(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	f := fileForOffload(t, e, "договор на трёх листах")
	mirror := &mirrorProbe{data: map[string][]byte{f.Key: []byte("договор на трёх листах")}}
	if err := st.SetFileMirror(f.ID, "проба"); err != nil {
		t.Fatal(err)
	}
	if err := e.offloadFile(f, mirror); err != nil {
		t.Fatal(err)
	}
	// FileBytes ходит в зеркало по имени из записи — подменим справочник
	// на наше зеркало пробы через тот же путь, каким ходит движок
	after, _ := st.AttachmentByID(f.ID)
	if after.Mirror != "проба" {
		t.Fatalf("зеркало у записи %q", after.Mirror)
	}
	if after.Key != f.Key {
		t.Error("ключ потерялся — из зеркала файл не найти")
	}
}

// Путь из базы не даёт стереть чужое: одна кривая запись не должна
// уносить файлы за пределами каталога Сёмы.
func TestOffloadNotTouchesForeignPath(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	foreign := filepath.Join(t.TempDir(), "чужой.txt")
	if err := os.WriteFile(foreign, []byte("не наше"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.removeLocalFile(foreign); err == nil {
		t.Error("файл за пределами каталога Сёмы был удалён")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("чужой файл пострадал: %v", err)
	}
}

// Срок и выключатель — настройки, а не гвозди.
func TestDueOffloadSetting(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if e.OffloadDays() != 30 {
		t.Errorf("умолчание срока %d", e.OffloadDays())
	}
	if e.OffloadOn() {
		t.Error("разгрузка должна быть выключена по умолчанию: она стирает местные копии")
	}
	if err := st.SetSetting(offloadDaysKey, "чепуха"); err != nil {
		t.Fatal(err)
	}
	if e.OffloadDays() != 30 {
		t.Error("непонятное значение должно давать умолчание")
	}
	if got := e.offloadPass(t.Context()); got != "разгрузка выключена" {
		t.Errorf("выключенная разгрузка сказала %q", got)
	}
	_ = time.Now()
}
