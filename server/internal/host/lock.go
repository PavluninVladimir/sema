// Пакет host — то, что зависит от машины, а не от предметной области:
// каталог данных, замок на нём, признаки среды. Ничего из `engine` и
// `store` здесь быть не должно: нужен их тип — значит место не здесь.
package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lock — единственность экземпляра на каталоге данных.
//
// Два сервера на одном томе — это повреждённая база, а не «второй сервер»:
// SQLite в WAL держит блокировки, но миграции и очередь на это не
// рассчитаны. Замок берётся средствами операционной системы и держится
// дескриптором всё время жизни процесса: убитый процесс (`docker kill`)
// отпускает его сам, и логика «забрать у мёртвого» не нужна — именно она и
// была бы источником ошибок.
type Lock struct {
	file *os.File
	path string
}

// Acquire — занять каталог данных. Занят другим — ошибка с тем, кто держит.
func Acquire(dir string) (*Lock, error) {
	path := filepath.Join(dir, "sema.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("замок %s: %w", path, err)
	}
	if err := acquireOS(f); err != nil {
		holder := Owner(dir)
		_ = f.Close()
		if holder.PID != 0 {
			return nil, fmt.Errorf("каталог данных %s уже занят: %s. "+
				"Один каталог — один сервер; для второго укажите другой каталог", dir, holder)
		}
		return nil, fmt.Errorf("каталог данных %s уже занят другим сервером Сёмы", dir)
	}
	// диагностическая запись: по ней ничего не решается, она только
	// объясняет человеку, кто занял каталог
	rec, _ := json.Marshal(Holder{
		PID: os.Getpid(), Host: machineName(), TakenAt: time.Now().Format(time.RFC3339),
		InContainer: inContainer(),
	})
	_ = f.Truncate(0)
	if _, err := f.WriteAt(rec, 0); err != nil {
		// запись не удалась — не беда: сам замок уже наш
		_ = err
	}
	return &Lock{file: f, path: path}, nil
}

// Release — снять замок. Зовётся последним, после закрытия базы.
func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = releaseOS(l.file)
	_ = l.file.Close()
	l.file = nil
}

// Holder — кто занял каталог. Только для показа человеку.
type Holder struct {
	PID         int    `json:"pid"`
	Host        string `json:"host"`
	TakenAt     string `json:"taken"`
	InContainer bool   `json:"in_container"`
}

func (h Holder) String() string {
	where := "на " + h.Host
	if h.InContainer {
		where = "в контейнере на " + h.Host
	}
	return fmt.Sprintf("процесс %d %s, с %s", h.PID, where, h.TakenAt)
}

// Owner — кто держит каталог по диагностической записи. Пусто — записи
// нет или она испорчена; это не ошибка, замок работает и без неё.
func Owner(dir string) Holder {
	raw, err := os.ReadFile(filepath.Join(dir, "sema.lock"))
	if err != nil {
		return Holder{}
	}
	var h Holder
	_ = json.Unmarshal(raw, &h)
	return h
}

func machineName() string {
	h, err := os.Hostname()
	if err != nil {
		return "неизвестно"
	}
	return h
}

// InContainer — грубая примета, только для показа: файл, которого на
// обычной машине не бывает.
// ВКонтейнере — Сёма живёт в докере: порт на хозяине держит проброс.
func InContainer() bool { return inContainer() }

func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}
