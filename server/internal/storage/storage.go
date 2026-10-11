// Package storage — где лежат байты файлов. Ядро знает только ключ и имя
// хранилища; клиент и модули получают файл через сервер и не знают путей.
// Сейчас реализовано локальное хранилище, интерфейс готов под S3-совместимое.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Backend — одно хранилище файлов.
type Backend interface {
	Name() string
	Kind() string
	// Put кладёт содержимое под ключ; повторный Put того же ключа — не ошибка.
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
	// Path — локальный путь, если хранилище умеет его дать (для внешних
	// инструментов вроде pdftotext); пусто — читайте через Get.
	Path(key string) string
	// Stat — сколько файлов и байт занято.
	Stat() (files int, bytes int64)
}

// Key — ключ по содержимому: две пары hex-символов каталогами, чтобы в одной
// папке не лежали тысячи файлов. Одинаковое содержимое — один ключ.
func Key(sha string) string {
	if len(sha) < 4 {
		return sha
	}
	return sha[:2] + "/" + sha[2:4] + "/" + sha
}

// SHA256 — хэш содержимого строкой.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Local — папка на диске сервера.
type Local struct {
	name string
	root string
}

func NewLocal(name, root string) *Local { return &Local{name: name, root: root} }

func (l *Local) Name() string { return l.name }
func (l *Local) Kind() string { return "local" }
func (l *Local) Root() string { return l.root }

func (l *Local) Path(key string) string {
	if strings.Contains(key, "..") {
		return ""
	}
	return filepath.Join(l.root, filepath.FromSlash(key))
}

func (l *Local) Put(key string, data []byte) error {
	p := l.Path(key)
	if p == "" {
		return fmt.Errorf("недопустимый ключ %q", key)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		return nil // то же содержимое уже лежит
	}
	tmp := p + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (l *Local) Get(key string) ([]byte, error) {
	p := l.Path(key)
	if p == "" {
		return nil, fmt.Errorf("недопустимый ключ %q", key)
	}
	return os.ReadFile(p)
}

func (l *Local) Delete(key string) error {
	p := l.Path(key)
	if p == "" {
		return nil
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *Local) Stat() (files int, bytes int64) {
	_ = filepath.WalkDir(l.root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return
}

// Copy — перенос содержимого между хранилищами (смена основного).
func Copy(from, to Backend, key string) error {
	data, err := from.Get(key)
	if err != nil {
		return err
	}
	return to.Put(key, data)
}

var _ = io.EOF
