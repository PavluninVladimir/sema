package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Пароль приложения берётся из «Ключей и секретов» списком, а не
// набирается руками: имя, которого нет, молчит до «Проверить» — на
// экране всё выглядит заполненным, а хранилище не работает.
func TestStoragePasswordPickedFromSecrets(t *testing.T) {
	s, st := testServer(t)

	// пока секретов нет — поле честно говорит, где их заводят
	empty := s.secretField("password_secret", "Пароль приложения", "зачем")
	if empty.Type != "text" || len(empty.Options) != 0 {
		t.Errorf("без секретов ждали строку с объяснением: %+v", empty)
	}

	for _, name := range []string{"yadisk-pass", "s3-key"} {
		if err := st.SaveSecret(&store.Secret{Name: name, Kind: store.SecretPassword}, "тайна"); err != nil {
			t.Fatal(err)
		}
	}
	поле := s.secretField("password_secret", "Пароль приложения", "зачем")
	if поле.Type != "picker" {
		t.Fatalf("ждали выбор из списка, а не %q", поле.Type)
	}
	// «—» плюс два секрета: у поля обязано быть положение «не выбран»,
	// иначе секрет назначается самим фактом открытия экрана
	if len(поле.Options) != 3 || поле.Options[0] != "—" {
		t.Errorf("список секретов: %v", поле.Options)
	}
	if поле.Value != "—" {
		t.Errorf("по умолчанию выбран %q, а должно быть «не выбран»", поле.Value)
	}
	// а когда секрет уже назначен, поле показывает именно его: иначе
	// человек, сохранив форму, молча меняет пароль на чужой
	выбрано := s.secretFieldWithPick("password_secret", "Пароль", "s3-key", "зачем")
	if выбрано.Value != "s3-key" {
		t.Errorf("выбранный секрет не показан: %q", выбрано.Value)
	}
	// значение секрета в форму не попадает и попасть не может
	for _, о := range поле.Options {
		if о == "тайна" {
			t.Fatal("в описании экрана оказалось значение секрета")
		}
	}
}

// На экранах «Хранилищ» значений секретов нет ни в каком виде — ни в описании
// экрана, ни в разметке, — а имена есть: выбирать ключ приходится по имени, и
// ссылка на сам секрет ведёт в «Ключи и секреты».
func TestStoragesScreenWithoutSecretValues(t *testing.T) {
	s, st := testServer(t)
	const value = "очень-тайное-значение"
	for _, name := range []string{"yadisk-pass", "s3-secret"} {
		if err := st.SaveSecret(&store.Secret{Name: name, Kind: store.SecretPassword}, value+"-"+name); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddStorage("s3-проба", "s3", `{"endpoint":"s3.example.org","bucket":"b","ssl":true,"access_secret":"yadisk-pass","secret_secret":"s3-secret"}`); err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, screen := range []string{storagesScreen, storagesScreen + "~" + storTabSources,
		entityAddr(storagesScreen, storNew, ""), entityAddr(storagesScreen, storNew, storNewDisk), storageAddr("s3-проба")} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatal(err)
		}
		всё := dumpScreen(doc) + string(markup)
		if len(всё) == 0 {
			t.Fatalf("%s: экран пуст", screen)
		}
		if containsSecret(всё, value) {
			t.Errorf("%s: значение секрета попало в описание или разметку экрана", screen)
		}
		if containsSecret(всё, "yadisk-pass") {
			named[screen] = true
		}
	}
	if !named[entityAddr(storagesScreen, storNew, "")] || !named[storageAddr("s3-проба")] {
		t.Errorf("имени секрета нет там, где его выбирают и где он назван: %v", named)
	}
	// на экране хранилища у ключа есть ссылка на сам секрет
	doc, err := s.buildScreen(storageAddr("s3-проба"), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(doc, "storage-key-0")
	if row == nil || len(row.Actions) != 1 || row.Actions[0].Target != engine.SecretScreen("yadisk-pass", "") {
		t.Errorf("у ключа доступа нет ссылки на секрет: %+v", row)
	}
}

func containsSecret(стог, игла string) bool { return strings.Contains(стог, игла) }
