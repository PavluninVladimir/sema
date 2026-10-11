package store

// Проверка построения, а не обещания.
//
// У Сёмы уже была «единственная дверь» `mayGoOutside`, которую не звал ни
// один рабочий путь, и тест засчитывал за проверку комментарий рядом с
// ней. Год все были уверены, что дыра закрыта. Поэтому здесь проверяется
// не намерение, а устройство: сколько во всём дереве мест, которые пишут
// таблицу настроек. Их должно быть ровно столько, сколько мы завели, — и
// каждое обязано вести журнал.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// settingsDoor — функции хранилища, которым позволено менять таблицу
// `settings`. Обе пишут журнал правок.
var settingsDoor = map[string]bool{"setSetting": true, "DeleteSettingKey": true}

func TestOnlyDoorWritesSettings(t *testing.T) {
	root := ".."
	// SQL, меняющий таблицу настроек. Миграции заводят её саму (CREATE) —
	// это не правка значения и журналу не нужно.
	re := regexp.MustCompile(`(?i)(INSERT\s+INTO\s+settings|UPDATE\s+settings|DELETE\s+FROM\s+settings)\b`)
	var outsiders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		text := string(raw)
		for i, line := range strings.Split(text, "\n") {
			if !re.MatchString(line) {
				continue
			}
			if settingsDoor[funcAtLine(text, i)] {
				continue
			}
			outsiders = append(outsiders, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход исходников: %v", err)
	}
	if len(outsiders) > 0 {
		t.Fatalf("таблицу настроек пишут мимо двери — эти правки не попадут в журнал изменений "+
			"(store/settings_log.go). Пишите через SetSetting/SetSecretSetting/DeleteSettingKey:\n%s",
			strings.Join(outsiders, "\n"))
	}
}

// TestDoorKeepsLog — вторая половина того же: дверь на месте, но
// перестала записывать. Одного списка мест мало — молчащая дверь
// выглядит точно так же, как работающая.
func TestDoorKeepsLog(t *testing.T) {
	raw, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("store.go: %v", err)
	}
	text := string(raw)
	for name := range settingsDoor {
		body := funcBody(text, name)
		if body == "" {
			t.Fatalf("функции %s в store.go больше нет: дверь переехала — перенесите и проверку", name)
		}
		if !strings.Contains(body, "logSettingChange(") {
			t.Fatalf("%s меняет настройку и не пишет журнал правок", name)
		}
	}
}

// funcAtLine — имя функции, внутри которой стоит строка номер `н`.
// Разбор грубый (объявление верхнего уровня начинается с «func» без
// отступа), но для этой проверки достаточный.
func funcAtLine(text string, n int) string {
	lines := strings.Split(text, "\n")
	re := regexp.MustCompile(`^func(?:\s+\([^)]*\))?\s+([^\s(]+)\s*\(`)
	for i := n; i >= 0; i-- {
		if m := re.FindStringSubmatch(lines[i]); m != nil {
			return m[1]
		}
	}
	return ""
}

// funcBody — текст функции верхнего уровня от объявления до закрывающей
// скобки в нулевой колонке.
func funcBody(text, name string) string {
	lines := strings.Split(text, "\n")
	re := regexp.MustCompile(`^func(?:\s+\([^)]*\))?\s+` + regexp.QuoteMeta(name) + `\s*\(`)
	for i, line := range lines {
		if !re.MatchString(line) {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if lines[j] == "}" {
				return strings.Join(lines[i:j+1], "\n")
			}
		}
	}
	return ""
}
