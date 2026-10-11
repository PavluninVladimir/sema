package engine

import (
	"github.com/PavluninVladimir/sema/server/internal/store"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Состояние «нужна привязка». Копия, поднятая на другой машине, — это та же
// Сёма с теми же ящиками, папками и адресами служб: если она сразу начнёт
// забирать почту и файлы, оригинал и копия станут отбирать письма друг у
// друга. Поэтому после `sema -restore` наблюдатели папок, приём почты и
// переносы не запускаются, пока человек не подтвердит привязку на
// «Здоровье»; интерфейс, чаты и память работают.

// bindingMarker — файл, который оставляет восстановление; его содержимое —
// что за копия. Подтверждение записывает то же содержимое в настройку.
const (
	bindingMarker = "восстановлено.txt"
	bindingAckKey = "restore.ack"
)

// bindingStatus — текст маркера, если привязка ещё не подтверждена.
func (e *Engine) bindingStatus() string {
	b, err := os.ReadFile(filepath.Join(e.conf.Data, bindingMarker))
	if err != nil {
		return ""
	}
	marker := strings.TrimSpace(string(b))
	if marker == "" || e.store.Setting(bindingAckKey, "") == marker {
		return ""
	}
	return marker
}

// NeedsBinding — Сёма поднят из копии и ждёт подтверждения, что это его
// новое место (или что оригинал остановлен).
func (e *Engine) NeedsBinding() bool { return e.bindingStatus() != "" }

// BindingNote — что именно восстановлено, для экрана.
func (e *Engine) BindingNote() string { return e.bindingStatus() }

// ConfirmBinding — человек проверил ящики, папки и адреса: работа с
// внешним миром разрешена.
func (e *Engine) ConfirmBinding() error {
	defer e.ResetHealth()
	marker := e.bindingStatus()
	if marker == "" {
		return nil
	}
	if err := e.store.SetSetting(bindingAckKey, marker); err != nil {
		return err
	}
	log.Printf("привязка подтверждена: %s", marker)
	return nil
}

// bindingAllowed — задачи, которым можно и без привязки: они не выходят
// из каталога данных. Всё остальное — приём почты и файлов, переносы,
// зеркала, модули, скрипты, цены, проверка версии —
// стоит: перечислять разрешённое надёжнее, чем запрещённое, — новая
// задача по умолчанию стоит.
var bindingAllowed = map[string]bool{
	WorkerPulse: true, WorkerMemory: true, WorkerQueue: true, WorkerReconcile: true,
	WorkerNotifyDue: true, WorkerChatNudge: true, WorkerFilesMigrate: true, "heartbeat": true,
}

func (e *Engine) heldByBinding(t *store.Task) bool {
	if !e.NeedsBinding() {
		return false
	}
	if t.System == "" || strings.HasPrefix(t.System, "module:") || t.ScriptID != 0 {
		return true
	}
	return !bindingAllowed[t.System]
}
