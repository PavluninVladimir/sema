package auth

// Ключ заводится браузером под конкретное имя сервера (RP ID), и чужие
// ключи в церемонии не просто бесполезны, а вредны. 28 сентября 2026
// переезд на внешний домен встал намертво именно из-за этого: имя стало
// `sema.example.org`, а три прежних ключа от `sema.local` продолжали
// уезжать в список «такие уже есть». Хранитель, у которого они лежат (их
// завели на этой же машине), отвечал «уже заведён», а браузер отдавал
// безликое «operation either timed out or was not allowed» — по нему
// причину не найти ни в журнале Сёмы, ни где-либо ещё.
//
// Поэтому правило разное в две стороны, и тест держит обе:
//   - «уже есть» при заведении — только свои: ошибка здесь запирает дверь;
//   - «предложи из этих» на входе — свои и старые безымянные: ошибка здесь
//     лишь заведёт лишний ключ.

import (
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func ключ(t *testing.T, a *Auth, name, rpID string, id byte) {
	t.Helper()
	raw, err := json.Marshal(webauthn.Credential{ID: []byte{id}, PublicKey: []byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddPasskey(name, string(raw), rpID); err != nil {
		t.Fatalf("ключ %s: %v", name, err)
	}
}

func TestCeremonyTakesOnlyKeysOfThisRP(t *testing.T) {
	a, st := semaWithPhrase(t)
	if err := st.SetSetting(SettingRPID, "sema.example.org"); err != nil {
		t.Fatal(err)
	}
	ключ(t, a, "свой", "sema.example.org", 1)
	ключ(t, a, "чужой", "sema.local", 2)
	ключ(t, a, "старый", "", 3)

	// заведение: чужой и безымянный не идут в «уже есть»
	рег := a.ownerCeremony(false)
	if len(рег.creds) != 1 || рег.creds[0].ID[0] != 1 {
		var было []string
		for _, c := range рег.creds {
			было = append(было, string(rune('0'+c.ID[0])))
		}
		t.Fatalf("при заведении в церемонию ушло %d ключей %v, а должен только свой: "+
			"чужой ключ в списке «уже есть» заставляет хранителя отказать", len(рег.creds), было)
	}

	// вход: свой и старый безымянный, чужой — нет
	вход := a.ownerCeremony(true)
	if len(вход.creds) != 2 {
		t.Fatalf("на входе предложено %d ключей, а должно два: свой и старый без имени", len(вход.creds))
	}
	for _, c := range вход.creds {
		if c.ID[0] == 2 {
			t.Fatal("на входе предложен ключ от другого имени сервера: хранитель его не найдёт, и вход упадёт")
		}
	}

	// без отбора по-прежнему видно все три — это нужно там, где важен
	// сам факт «ключи есть» (например, чтобы не предлагать заводить первый)
	if all := a.loadOwner(); len(all.creds) != 3 {
		t.Fatalf("loadOwner отдал %d ключей вместо трёх: счёт ключей не должен зависеть от имени", len(all.creds))
	}
}

// Имя сменили, а своих ключей под него ещё нет — вход обязан сказать это
// словами, а не молча предложить чужие и получить отказ от хранителя.
func TestLoginWithoutKeysForThisRPSaysSo(t *testing.T) {
	a, st := semaWithPhrase(t)
	if err := st.SetSetting(SettingRPID, "sema.example.org"); err != nil {
		t.Fatal(err)
	}
	ключ(t, a, "чужой", "sema.local", 2)

	_, _, err := a.BeginLogin()
	if err == nil {
		t.Fatal("вход собрался на чужих ключах: браузер получит список, в котором нет ни одного подходящего")
	}
	if !contains(err.Error(), "sema.example.org") {
		t.Fatalf("в отказе не названо имя, под которое нет ключей: %q", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
