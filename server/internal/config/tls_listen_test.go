package config

import (
	"path/filepath"
	"testing"
)

// Защищённый порт по умолчанию слушает только петлю (open-source,
// решение 5): наружу его открывают явно — переменной или файлом запуска.
func TestTLSListenDefaultsToLoopback(t *testing.T) {
	t.Setenv("SEMA_TLS_LISTEN", "")
	c, err := Load(filepath.Join(t.TempDir(), "нет-такого.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.TLS.Listen != "127.0.0.1:8443" {
		t.Fatalf("защищённый порт по умолчанию %q, ждали 127.0.0.1:8443", c.TLS.Listen)
	}
	// в контейнере адрес задаётся явно — и он побеждает умолчание
	t.Setenv("SEMA_TLS_LISTEN", "0.0.0.0:8443")
	if c, err = Load(filepath.Join(t.TempDir(), "нет-такого.yaml")); err != nil || c.TLS.Listen != "0.0.0.0:8443" {
		t.Fatalf("SEMA_TLS_LISTEN не перекрыл умолчание: %q (%v)", c.TLS.Listen, err)
	}
}
