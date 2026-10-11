package auth

import (
	"path/filepath"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ключи в пакете входа и в реестре — одни и те же строки: расходятся — и
// экран читает одну настройку, а вход другую.
func TestSettingKeysMatchRegistry(t *testing.T) {
	for key, def := range map[string]string{
		SettingPhraseLimit: phraseLimitDef.Key, SettingRPID: rpIDDef.Key,
		SettingOrigin: originDef.Key, SettingLocalHosts: localHostsDef.Key,
		SettingLocalOpen: localOpenDef.Key, SettingLocalScope: localScopeDef.Key,
	} {
		if key != def {
			t.Errorf("ключ %q в пакете входа, а в реестре %q", key, def)
		}
	}
}

// Пакет входа на пустой базе читает то, что записано в реестре: умолчание
// у каждой настройки входа одно, и вход его не переписывает числом рядом.
func TestSettingDefaultsAreReadByAuth(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st)
	a.SetListen("127.0.0.1:9001")
	if got := a.PhraseLimit(); got != phraseLimitDef.DefaultInt() {
		t.Errorf("предел попыток %d, умолчание %d", got, phraseLimitDef.DefaultInt())
	}
	if got := a.RPID(); got != rpIDDef.Default {
		t.Errorf("RP ID %q, умолчание %q", got, rpIDDef.Default)
	}
	if got := a.LocalHosts(); len(got) != 0 || localHostsDef.Default != "" {
		t.Errorf("имена из своей сети %v, умолчание %q", got, localHostsDef.Default)
	}
	// у адреса входа умолчание выводится: пока не назван, это адрес по порту прослушивания
	if got := a.Origin(); got != DefaultOrigin("127.0.0.1:9001") || got != a.OriginDefault() || originDef.Default != "" {
		t.Errorf("адрес входа %q, выводимое умолчание %q", got, a.OriginDefault())
	}
	if err := st.SetSetting(SettingPhraseLimit, "9"); err != nil || a.PhraseLimit() != 9 {
		t.Errorf("предел попыток после записи: %d (%v)", a.PhraseLimit(), err)
	}
	// свежая установка закрыта: без проверки личности не пускается никто, а
	// «этой машиной» считается только петля (open-source, решение 5)
	if a.LocalOpen() || localOpenDef.DefaultFlag() {
		t.Errorf("свежая установка пускает без входа: %v (умолчание %q)", a.LocalOpen(), localOpenDef.Default)
	}
	prev := LocalPrivate.Load()
	defer LocalPrivate.Store(prev)
	a.LoadLocalScope()
	if LocalPrivate.Load() || localScopeDef.Default != store.ScopeLoopback {
		t.Errorf("свежая установка считает своей частную сеть (умолчание %q)", localScopeDef.Default)
	}
	// непонятная запись — не разрешение: читается умолчанием, а не «да»
	if err := st.SetSetting(SettingLocalOpen, "мусор"); err != nil || a.LocalOpen() {
		t.Errorf("непонятная запись «без входа» читается как «да» (%v)", err)
	}
	if err := st.SetSetting(SettingLocalScope, "мусор"); err != nil {
		t.Fatal(err)
	}
	if a.LoadLocalScope(); LocalPrivate.Load() {
		t.Error("непонятная запись своей сети читается как «private»")
	}
}
