package store

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

func vaultStore(t *testing.T) *Store {
	t.Helper()
	st := testStore(t)
	box, err := secret.New("ключ-для-проверок-хранилища")
	if err != nil || box == nil {
		t.Fatalf("ключ: %v", err)
	}
	st.UseSecretBox(box)
	return st
}

// «Кто завёл» у записей, лежавших до этого признака, определяется по заметке,
// которую пишут человеческие пути создания, а не по приставке имени: приставок
// у ядра много, и новая не должна делать секрет «вашим».
func TestSecretCreatedByMigrationUsesNotes(t *testing.T) {
	st := vaultStore(t)
	for _, sec := range []Secret{
		{Name: "мой-токен", Kind: SecretToken, Note: SecretNoteAdded},
		{Name: "ssh:мой", Kind: SecretSSH, Note: SecretNoteGenerated},
		{Name: "ssh:чужой", Kind: SecretSSH, Note: SecretNoteImported},
		{Name: "model:OpenAI", Kind: SecretToken, Note: "ключ провайдера моделей · OpenAI"},
		{Name: "agents:root-key", Kind: SecretSSH, Note: "корень для агентов · закрытый ключ, наружу не выдаётся"},
		// имя похоже на ваше, а заметка служебная: решает заметка
		{Name: "ssh:выглядит-вашим", Kind: SecretSSH, Note: "ключ Сёмы для агентов, наружу не выдаётся"},
		// а у вашего имя может начинаться как служебное
		{Name: "model:мой-токен", Kind: SecretToken, Note: SecretNoteAdded},
		// заметки нет вовсе: у человеческих путей она есть всегда
		{Name: "без-заметки", Kind: SecretOther},
	} {
		sec := sec
		if err := st.SaveSecret(&sec, "значение"); err != nil {
			t.Fatal(err)
		}
	}
	// как будто признака ещё не было: старые записи лежат с пустым
	if _, err := st.db.Exec(`UPDATE secrets SET created_by = ''`); err != nil {
		t.Fatal(err)
	}
	if err := st.fillSecretCreatedBy(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"мой-токен": SecretByHuman, "ssh:мой": SecretByHuman, "ssh:чужой": SecretByHuman, "model:мой-токен": SecretByHuman,
		"model:OpenAI": SecretByCore, "agents:root-key": SecretByCore, "ssh:выглядит-вашим": SecretByCore, "без-заметки": SecretByCore,
	}
	for name, by := range want {
		sec, ok := st.Secret(name)
		if !ok || sec.CreatedBy != by {
			t.Errorf("%s: завёл %q, а ждали %q", name, sec.CreatedBy, by)
		}
		if sec.Service() != (by == SecretByCore) {
			t.Errorf("%s: служебный %v при %q", name, sec.Service(), by)
		}
	}
	// второй проход ничего не меняет: решённое не пересчитывается
	if _, err := st.db.Exec(`UPDATE secrets SET note = 'любая' WHERE name = 'мой-токен'`); err != nil {
		t.Fatal(err)
	}
	if err := st.fillSecretCreatedBy(); err != nil {
		t.Fatal(err)
	}
	if sec, _ := st.Secret("мой-токен"); sec.CreatedBy != SecretByHuman {
		t.Errorf("уже решённая запись пересчитана: %q", sec.CreatedBy)
	}
}

// Кто не назвался — тот ядро; кто назвался человеком — человек, и замена значения
// и перезапись ядром этого не меняют: первый остаётся первым.
func TestSecretCreatedByFromPathAndKeptOnReplace(t *testing.T) {
	st := vaultStore(t)
	if err := st.SaveSecret(&Secret{Name: "ядро", Kind: SecretToken, Note: "ключ провайдера моделей · X"}, "один"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSecret(&Secret{Name: "ваш", Kind: SecretToken, Note: SecretNoteAdded, CreatedBy: SecretByHuman}, "один"); err != nil {
		t.Fatal(err)
	}
	if sec, _ := st.Secret("ядро"); !sec.Service() || sec.CreatedBy != SecretByCore {
		t.Errorf("не назвавшийся не стал ядром: %+v", sec)
	}
	if sec, _ := st.Secret("ваш"); sec.Service() {
		t.Errorf("названный человеком стал служебным: %+v", sec)
	}
	if err := st.ReplaceSecretValue("ваш", "два"); err != nil {
		t.Fatal(err)
	}
	// ядро перезаписало ваше имя, не называясь: завёл по-прежнему человек
	if err := st.SaveSecret(&Secret{Name: "ваш", Kind: SecretToken}, "три"); err != nil {
		t.Fatal(err)
	}
	if sec, _ := st.Secret("ваш"); sec.CreatedBy != SecretByHuman {
		t.Errorf("перезапись сменила того, кто завёл: %q", sec.CreatedBy)
	}
	if v, _ := st.SecretValue("ваш"); v != "три" {
		t.Errorf("значение %q", v)
	}
}

// Журнал секретов различает чтения, отказы и правки; правки не вытесняются
// частыми чтениями, а чтения — правками, и значения в нём нет.
func TestSecretLogFiltersAndKeepsChanges(t *testing.T) {
	st := vaultStore(t)
	st.LogSecret(SecretUse{Name: "k", Who: "вы", Granted: true, Change: true, Note: "заведён"})
	for i := 0; i < keepSecretLog+30; i++ {
		st.LogSecret(SecretUse{Name: "k", Who: "ядро", Granted: true})
	}
	st.LogSecret(SecretUse{Name: "k", Who: "модуль", Granted: false, Note: "право не выдано"})
	if n := st.CountSecretLogFiltered("k", SecretLogChanges); n != 1 {
		t.Errorf("правок %d: частые чтения вытеснили запись «заведён»", n)
	}
	// обращения (чтения и отказы) хранятся вместе, двести на секрет
	if n := st.CountSecretLogFiltered("k", SecretLogReads); n != keepSecretLog-1 {
		t.Errorf("чтений %d, а хранится %d вместе с отказом", n, keepSecretLog)
	}
	if n := st.CountSecretLogFiltered("k", SecretLogDenied); n != 1 {
		t.Errorf("отказов %d", n)
	}
	// «последнее обращение» и «отказы» не считают правки
	if u := st.LastSecretUse("k"); u == nil || u.Change || u.Granted {
		t.Errorf("последнее обращение: %+v", u)
	}
	if n, _ := st.SecretDenials("k"); n != 1 {
		t.Errorf("отказов у секрета %d", n)
	}
	list, _ := st.SecretLogFiltered("k", SecretLogChanges, 10)
	if len(list) != 1 || !list[0].Change || strings.TrimSpace(list[0].Note) != "заведён" {
		t.Errorf("правки: %+v", list)
	}
}
