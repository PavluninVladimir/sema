package config

import (
	"path/filepath"
	"testing"
)

// Каталог данных задают в трёх местах, и верхний источник обязан
// перекрывать нижние ПОЛНОСТЬЮ. Раньше при явном `db:` в файле база
// оставалась по старому пути, а человек ждал данные в новом — и находил
// пустую установку, решив, что всё потерялось.
func TestDataDirOverridesEntirely(t *testing.T) {
	c := &Config{Data: "/старый", DB: "/старый/своя-база.db"}
	c.Files.Dir = "/старый/files"

	c.SetDataDir("/новый", "аргумент -data")

	if c.Data != "/новый" {
		t.Fatalf("каталог: %q", c.Data)
	}
	if c.DB != filepath.Join("/новый", "sema.db") {
		t.Fatalf("база осталась по старому пути: %q — данные «пропадут»", c.DB)
	}
	if c.Files.Dir != filepath.Join("/новый", "files") {
		t.Fatalf("файлы остались по старому пути: %q", c.Files.Dir)
	}
	if c.DataFrom != "аргумент -data" {
		t.Fatalf("источник не записан: %q", c.DataFrom)
	}
}

// Пустой путь ничего не меняет: «не задано» — это не «сбросить в пустое».
func TestEmptyPathDoesNotReset(t *testing.T) {
	c := &Config{Data: "/данные", DB: "/данные/sema.db"}
	c.SetDataDir("", "переменная")
	if c.Data != "/данные" || c.DB != "/данные/sema.db" {
		t.Fatalf("пустое значение перетёрло настройки: %q %q", c.Data, c.DB)
	}
}

// Умолчание — режим разработки, а не цель установки: источник должен быть
// назван, чтобы человек видел, откуда взялся путь.
func TestDirSourceNamed(t *testing.T) {
	c := &Config{}
	if err := c.fill(); err != nil {
		t.Fatal(err)
	}
	if c.Data != "." || c.DataFrom != "умолчание" {
		t.Fatalf("умолчание: %q из %q", c.Data, c.DataFrom)
	}
	c2 := &Config{Data: "/из/файла"}
	if err := c2.fill(); err != nil {
		t.Fatal(err)
	}
	if c2.DataFrom != "файл запуска" {
		t.Fatalf("источник из файла: %q", c2.DataFrom)
	}
}
