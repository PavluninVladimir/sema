package api

// Журнал настроек: вторая дверь — кто именно правил.
//
// Первая дверь (`store.SetSetting`) знает, ЧТО изменилось, и держится
// тем, что она единственный писатель таблицы. Эта отвечает, КТО, и
// держится тем же способом: действие человека попадает внутрь Сёмы ровно
// одной дорогой, и подпись стоит на ней, а не в местах, где настройка
// записывается.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func identityForTest(kind, name string) auth.Identity {
	id := auth.Identity{Kind: kind}
	if name != "" {
		id.Token = &store.APIToken{Name: name}
	}
	return id
}

func editForTest() store.SettingChange {
	return store.SettingChange{ID: 1, At: "2026-09-11T10:00:00+04:00",
		Key: "tg.api_hash", Who: "вы", From: "settings:tg · save_tg", Hidden: true}
}

func TestActionSignsEdits(t *testing.T) {
	файлы, _ := filepath.Glob("*.go")
	// «s.execute(» мимо обёртки — правка человека, которая ляжет в журнал
	// без имени и будет выглядеть как сделанная самим Сёмой
	re := regexp.MustCompile(`\bs\.execute\(`)
	var мимо []string
	for _, ф := range файлы {
		if strings.HasSuffix(ф, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(ф)
		if err != nil {
			t.Fatalf("%s: %v", ф, err)
		}
		text := string(raw)
		for i, строка := range strings.Split(text, "\n") {
			if !re.MatchString(строка) || strings.HasPrefix(strings.TrimSpace(строка), "//") {
				continue
			}
			if apiRowFunc(text, i) == "executeSigned" {
				continue
			}
			мимо = append(мимо, ф+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(строка))
		}
	}
	if len(мимо) > 0 {
		t.Fatalf("действие выполняется мимо s.executeSigned — его правки настроек лягут в журнал "+
			"без имени человека:\n%s", strings.Join(мимо, "\n"))
	}
	// и сама обёртка обязана подписывать: список мест вызова ничего не
	// стоит, если внутри перестали ставить подпись
	raw, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("server.go: %v", err)
	}
	body := apiFuncBody(string(raw), "executeSigned")
	switch {
	case body == "":
		t.Fatal("функции executeSigned в server.go нет: дверь переехала — перенесите и проверку")
	case !strings.Contains(body, "LastChange()"), !strings.Contains(body, "SignChanges("):
		t.Fatal("executeSigned больше не подписывает правки настроек: журнал начнёт врать «Сёма сам»")
	}
}

func TestWhoActsNamesHumanAndToken(t *testing.T) {
	for _, п := range []struct {
		вид, имя, ждём string
	}{
		{"session", "", "вы"},
		{"local", "", "вы · с этой машины"},
		{"token", "codex-hooks", "токен «codex-hooks»"},
		{"none", "", ""},
	} {
		if got := whoActs(identityForTest(п.вид, п.имя)); got != п.ждём {
			t.Fatalf("кто для %q: %q, ждали %q", п.вид, got, п.ждём)
		}
	}
}

func TestEditRowDoesNotShowSecret(t *testing.T) {
	s, _ := testServer(t)
	c := s.namedEditRow(s.settingsIndex(), editForTest(), false)
	for _, ф := range c.Facts {
		if strings.Contains(ф.Value, "sk-") {
			t.Fatalf("значение секрета попало на экран: %q", ф.Value)
		}
	}
	if len(c.Badges) == 0 {
		t.Fatal("скрытое значение должно быть подписано капсулой «секрет»: иначе человек решит, что параметр был пуст")
	}
}

// apiRowFunc и apiFuncBody — тот же грубый разбор, что в
// store/settings_door_test.go: объявление верхнего уровня начинается с
// «func» без отступа.
func apiRowFunc(text string, н int) string {
	строки := strings.Split(text, "\n")
	re := regexp.MustCompile(`^func(?:\s+\([^)]*\))?\s+([^\s(]+)\s*\(`)
	for i := н; i >= 0; i-- {
		if m := re.FindStringSubmatch(строки[i]); m != nil {
			return m[1]
		}
	}
	return ""
}

func apiFuncBody(text, name string) string {
	строки := strings.Split(text, "\n")
	re := regexp.MustCompile(`^func(?:\s+\([^)]*\))?\s+` + regexp.QuoteMeta(name) + `\s*\(`)
	for i, строка := range строки {
		if !re.MatchString(строка) {
			continue
		}
		for j := i + 1; j < len(строки); j++ {
			if строки[j] == "}" {
				return strings.Join(строки[i:j+1], "\n")
			}
		}
	}
	return ""
}
