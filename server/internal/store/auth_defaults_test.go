package store

import (
	"path/filepath"
	"testing"
	"time"
)

// Разовая миграция умолчаний входа (auth_defaults.go): свежая база
// закрыта, обновлённая — как была, записанное не трогается.

func openAuthBase(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// oldVersion — так базу оставила прежняя версия: умолчания входа читались
// литералами и в таблицу не писались.
func oldVersion(t *testing.T, st *Store, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, err := st.db.Exec(`DELETE FROM settings WHERE key = ?`, key); err != nil {
			t.Fatal(err)
		}
	}
}

func authRows(st *Store) (open, scope string) {
	open, _ = st.settingRow(AuthLocalOpen.Key)
	scope, _ = st.settingRow(AuthLocalScope.Key)
	return
}

// Свежая установка: новые умолчания записаны явно — и ящик, заведённый
// потом, при следующем старте не превращает её в «обновлённую», которая
// открылась бы своей сети сама.
func TestAuthDefaultsFreshBase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	st := openAuthBase(t, path)
	if st.SettingFlag(AuthLocalOpen) || st.SettingText(AuthLocalScope) != ScopeLoopback {
		t.Fatalf("свежая база: без входа %v, своя сеть %q — ждали закрытую", st.SettingFlag(AuthLocalOpen), st.SettingText(AuthLocalScope))
	}
	if open, scope := authRows(st); open != "false" || scope != ScopeLoopback {
		t.Fatalf("свежей базе умолчания не записаны явно: %q, %q", open, scope)
	}
	// человек завёл ящик, пришли письма, завёл ключ — и Сёму перезапустили
	if err := st.SaveAccounts([]*Account{{Name: "ящик@пример.рф", Email: "ящик@пример.рф", Host: "imap.пример.рф", Port: 993}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPasskey("мак", "{}", "localhost"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st = openAuthBase(t, path)
	defer st.Close()
	if open, scope := authRows(st); open != "false" || scope != ScopeLoopback {
		t.Fatalf("после перезапуска с данными свежая установка открылась: %q, %q", open, scope)
	}
}

// Обновление работающего Сёмы: данные есть, ключей входа в таблице нет —
// записываются прежние «пускать» и «частная сеть своя». Любой из признаков
// данных достаточен сам по себе.
func TestAuthDefaultsUpgradeKeepsOldBehaviour(t *testing.T) {
	cases := map[string]func(*Store) error{
		"ящик": func(st *Store) error {
			return st.SaveAccounts([]*Account{{Name: "ящик@пример.рф", Email: "ящик@пример.рф", Host: "imap.пример.рф", Port: 993}})
		},
		"письмо": func(st *Store) error {
			return st.InsertMessage(&Message{MessageID: "<1@пример.рф>", UID: 1, FromAddr: "кто@пример.рф", Date: time.Now(), Status: StatusInboxOK})
		},
		"ключ устройства": func(st *Store) error {
			_, err := st.AddPasskey("мак", "{}", "localhost")
			return err
		},
		"сессия": func(st *Store) error {
			return st.AddSession("sema_s_проба", 0, "Safari", "127.0.0.1", "", time.Hour)
		},
		"токен": func(st *Store) error {
			_, err := st.AddToken("хук", []string{"memory"})
			return err
		},
	}
	for name, add := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "old.db")
			st := openAuthBase(t, path)
			if err := add(st); err != nil {
				t.Fatal(err)
			}
			oldVersion(t, st, AuthLocalOpen.Key, AuthLocalScope.Key)
			st.Close()
			st = openAuthBase(t, path)
			defer st.Close()
			if open, scope := authRows(st); open != "true" || scope != ScopePrivate {
				t.Fatalf("обновлённый Сёма с данными (%s) закрылся: %q, %q — ждали прежние true, private", name, open, scope)
			}
			if !st.SettingFlag(AuthLocalOpen) {
				t.Error("прежнее «пускать без входа» не читается")
			}
		})
	}
}

// Записанное значение миграция не трогает: у живого Сёмы владельца
// `auth.local_open = false` записано руками и таким остаётся. Ключи решаются по
// одному: незаписанная своя сеть получает прежнее значение.
func TestAuthDefaultsRecordedUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	st := openAuthBase(t, path)
	if _, err := st.AddPasskey("iPhone", "{}", "sema.local"); err != nil {
		t.Fatal(err)
	}
	oldVersion(t, st, AuthLocalScope.Key)
	if err := st.SetSetting(AuthLocalOpen.Key, "false"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st = openAuthBase(t, path)
	if open, scope := authRows(st); open != "false" || scope != ScopePrivate {
		t.Fatalf("записанное переписано или незаписанное не получило прежнего: %q, %q — ждали false, private", open, scope)
	}
	// оба записаны — ни одного прохода больше: человек выбрал петлю, а
	// данные в базе этого не отменяют
	if err := st.SetSetting(AuthLocalScope.Key, ScopeLoopback); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st = openAuthBase(t, path)
	defer st.Close()
	if open, scope := authRows(st); open != "false" || scope != ScopeLoopback {
		t.Fatalf("повторное открытие переписало выбор человека: %q, %q", open, scope)
	}
}

// Пустой список ящиков данными не считается: «[]» пишет json.Marshal, когда
// ящиков нет или их сняли все.
func TestAuthDefaultsEmptyAccountsAreNoData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	st := openAuthBase(t, path)
	if err := st.SaveAccounts(nil); err != nil {
		t.Fatal(err)
	}
	oldVersion(t, st, AuthLocalOpen.Key, AuthLocalScope.Key)
	st.Close()
	st = openAuthBase(t, path)
	defer st.Close()
	if open, scope := authRows(st); open != "false" || scope != ScopeLoopback {
		t.Fatalf("пустой список ящиков сочтён данными: %q, %q", open, scope)
	}
}
