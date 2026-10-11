package engine

// Составные настройки, которые лежат в таблице одной строкой JSON (`models.json`,
// `roles.json`, `providers.json`, `modules.json`), журнал описывает человеческими
// словами по таблице полей из `store/settings_log_note.go`. Структуры живут здесь,
// а таблица — там: новое поле без строки показывалось бы человеку как «поле «x»»
// или молчало бы, и ловить это приходится тестом, а не компилятором.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestNoteKnowsEveryEngineField(t *testing.T) {
	for _, c := range []struct {
		key string
		typ reflect.Type
	}{
		{"models.json", reflect.TypeOf(ModelConf{})},
		{"roles.json", reflect.TypeOf(Role{})},
		{"providers.json", reflect.TypeOf(ProviderConf{})},
		{"modules.json", reflect.TypeOf(Module{})},
	} {
		for i := 0; i < c.typ.NumField(); i++ {
			f := c.typ.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if !store.NoteKnows(c.key, name) {
				t.Errorf("%s: поле %s (json %q) не описано в noteSpecs — добавьте строку в store/settings_log_note.go", c.key, f.Name, name)
			}
		}
	}
}

// Каждая составная настройка из таблицы журнала — настоящий ключ, под которым
// движок хранит список: опечатка в ключе молча лишила бы описания правок.
func TestNoteKeysAreRealKeys(t *testing.T) {
	real := map[string]bool{modelsKey: true, rolesKey: true, providersKey: true, modulesKey: true, "mailconf.json": true, "accounts.json": true}
	for _, key := range store.NoteKeys() {
		if !real[key] {
			t.Errorf("%s: такого ключа в коде нет", key)
		}
	}
}
