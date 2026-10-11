package engine

// Разгрузка файлов: то, что старше срока и уже лежит в зеркале, перестаёт
// занимать место на этой машине. Запись файла остаётся целиком — уходит
// только адрес на диске, и `FileBytes` отдаёт содержимое из зеркала.
//
// Правило одно и оно жёсткое: **сначала сверить, потом удалять**. Не
// «зеркало сказало, что положило», а «я забрал оттуда и хэш сошёлся».
// Ошибка здесь необратима: местная копия — единственная, и второго
// экземпляра письма с этим вложением уже нет.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/storage"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// WorkerFilesOffload — имя задачи разгрузки в общем списке.
const WorkerFilesOffload = "files_offload"

const (
	offloadDaysKey = "files.offload_days"
	offloadOnKey   = "files.offload"
	offloadBatch   = 10
)

// OffloadDays — со скольки дней файл считается залежавшимся. 0 — не
// разгружать: срок правится на «Хранилищах», а не в коде.
func (e *Engine) OffloadDays() int {
	v, err := strconv.Atoi(e.store.Setting(offloadDaysKey, "30"))
	if err != nil || v < 0 {
		return 30
	}
	return v
}

// OffloadOn — включена ли разгрузка. По умолчанию нет: она удаляет
// местные копии, и такое человек включает сам, посмотрев на зеркало.
func (e *Engine) OffloadOn() bool { return e.store.Setting(offloadOnKey, "0") == "1" }

// SetOffload — как и у копии, щёлкает и настройку, и задачу: два
// выключателя на одно дело молча спорили друг с другом.
func (e *Engine) SetOffload(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	if err := e.store.SetSetting(offloadOnKey, v); err != nil {
		return err
	}
	return e.enableTask(WorkerFilesOffload, on)
}

// OffloadTaskOn — включена ли сама задача разгрузки.
func (e *Engine) OffloadTaskOn() bool { return e.taskEnabled(WorkerFilesOffload) }

// offloadPass — проход разгрузки: задача «Разгрузка файлов».
func (e *Engine) offloadPass(ctx context.Context) string {
	if !e.OffloadOn() {
		return "разгрузка выключена"
	}
	days := e.OffloadDays()
	if days <= 0 {
		return "срок не задан — ничего не разгружаю"
	}
	mirror := e.MirrorStorage()
	if mirror == nil {
		return "зеркала нет: разгружать некуда"
	}
	far, err := e.backendByConf(mirror)
	if err != nil {
		e.store.NoteStorage(mirror.Name, "ошибка: "+err.Error())
		return "зеркало недоступно: " + err.Error()
	}
	edge := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	list, err := e.store.FilesToOffload(mirror.Name, edge, offloadBatch)
	if err != nil {
		return "ошибка: " + err.Error()
	}
	if len(list) == 0 {
		n, nBytes := e.store.OffloadedCount(mirror.Name)
		if n == 0 {
			return "разгружать нечего"
		}
		return fmt.Sprintf("разгружать нечего · на зеркале %s в %s", plural(n, "файл", "файла", "файлов"), humanBytes(nBytes))
	}
	free, denial := 0, 0
	var freed int64
	for _, a := range list {
		select {
		case <-ctx.Done():
			return "прервано"
		default:
		}
		if err := e.offloadFile(a, far); err != nil {
			log.Printf("разгрузка: файл %d (%s): %v", a.ID, a.Filename, err)
			denial++
			continue
		}
		free++
		freed += a.Size
	}
	result := fmt.Sprintf("разгружено %s, освободилось %s", plural(free, "файл", "файла", "файлов"), humanBytes(freed))
	if denial > 0 {
		result += fmt.Sprintf(" · не вышло у %d (см. журнал)", denial)
	}
	return result
}

// offloadFile — сверить и только потом стереть местную копию.
func (e *Engine) offloadFile(a *store.Attachment, far storage.Backend) error {
	if a.Key == "" {
		return fmt.Errorf("нет ключа: такой файл разгружать нечем")
	}
	// сверка настоящая: забираем из зеркала и считаем хэш. Проверять
	// размером мало — обрезанная заливка даст тот же размер только
	// случайно, а вот пустой или недописанный файл размером не поймать
	foreign, err := far.Get(a.Key)
	if err != nil {
		return fmt.Errorf("в зеркале не читается: %w", err)
	}
	if a.SHA256 != "" {
		if got := storage.SHA256(foreign); got != a.SHA256 {
			return fmt.Errorf("в зеркале лежит не то: хэш %s вместо %s", short8(got), short8(a.SHA256))
		}
	} else if int64(len(foreign)) != a.Size {
		// у старых записей хэша нет — тогда хотя бы размер
		return fmt.Errorf("в зеркале другой размер: %d вместо %d", len(foreign), a.Size)
	}
	// местная копия больше не нужна
	if a.Storage != "" {
		if own := e.storageByName(a.Storage); own != nil {
			if err := own.Delete(a.Key); err != nil {
				return fmt.Errorf("местную копию не убрать: %w", err)
			}
		}
	}
	if a.Path != "" {
		if err := e.removeLocalFile(a.Path); err != nil {
			return fmt.Errorf("старый путь не убрать: %w", err)
		}
	}
	return e.store.ClearFileLocal(a.ID)
}

// removeLocalFile — удаление с проверкой, что путь наш. Строка path
// приезжает из базы, а база — не гарантия: одна кривая запись не должна
// давать Сёме стереть что-нибудь за пределами своего каталога файлов.
func (e *Engine) removeLocalFile(path string) error {
	root, err := filepath.Abs(e.conf.Files.Dir)
	if err != nil || root == "" || root == "/" {
		return fmt.Errorf("каталог файлов не задан — ничего не удаляю")
	}
	full, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("путь вне каталога файлов: %s", path)
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
