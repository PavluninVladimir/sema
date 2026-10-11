package auth

// Вход по фразе: она подходит, опечатка отличается от неверной фразы,
// смена отменяет прежнюю, а подбор упирается в предел и запирается.
// Проверяем настоящий путь — тот же TryPhrase, который зовёт ручка входа.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func semaWithPhrase(t *testing.T) (*Auth, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	old, had := os.LookupEnv("SEMA_KEY")
	_ = os.Setenv("SEMA_KEY", "ключ-для-тестов")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEMA_KEY", old)
			return
		}
		_ = os.Unsetenv("SEMA_KEY")
	})
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// argon2 нарочно медленный; в тесте нам важен не его вес, а поведение
	for k, v := range map[string]string{
		settingPhraseTime: "1", settingPhraseMemory: "1024", settingPhraseThreads: "1",
	} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return New(st), st
}

func TestPhraseFitsAndRotationRevokesPrevious(t *testing.T) {
	a, _ := semaWithPhrase(t)
	if a.HasPhrase() {
		t.Fatal("фраза есть до того, как её завели")
	}
	phrase, err := a.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	if !a.HasPhrase() {
		t.Fatal("фраза заведена, а HasPhrase молчит")
	}
	if res := a.TryPhrase("203.0.113.10", phrase); !res.OK {
		t.Fatalf("своя же фраза не подошла: %+v", res)
	}
	// регистр, пробелы и дефисы не важны — так обещано человеку
	withSpaces := strings.ToUpper(strings.ReplaceAll(phrase, "-", " "))
	if res := a.TryPhrase("203.0.113.10", withSpaces); !res.OK {
		t.Fatalf("фраза в другом виде не подошла: %+v", res)
	}
	// опечатка: вид верный, контрольная группа не сходится
	typo := []byte(phrase)
	for i := range typo {
		if typo[i] != '-' {
			if typo[i] == 'a' {
				typo[i] = 'b'
			} else {
				typo[i] = 'a'
			}
			break
		}
	}
	res := a.TryPhrase("203.0.113.10", string(typo))
	if res.OK || !res.Typo {
		t.Fatalf("опечатка не отличена от неверной фразы: %+v", res)
	}
	// другая настоящая фраза: вид верный, контрольная группа сходится, а
	// фраза не та — это не опечатка
	other, err := secret.NewPhrase()
	if err != nil {
		t.Fatal(err)
	}
	if res := a.TryPhrase("203.0.113.10", other); res.OK || res.Typo {
		t.Fatalf("чужая фраза принята или названа опечаткой: %+v", res)
	}
	// смена делает прежнюю негодной с того же мгновения
	rotated, err := a.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	if rotated == phrase {
		t.Fatal("новая фраза совпала с прежней")
	}
	if res := a.TryPhrase("203.0.113.10", phrase); res.OK {
		t.Fatal("прежняя фраза пускает после смены")
	}
	if res := a.TryPhrase("203.0.113.10", rotated); !res.OK {
		t.Fatalf("новая фраза не подошла: %+v", res)
	}
	// отзыв: не пускает никого, включая правильную фразу
	if err := a.RevokePhrase(); err != nil {
		t.Fatal(err)
	}
	if res := a.TryPhrase("203.0.113.10", rotated); res.OK {
		t.Fatal("отозванная фраза пускает")
	}
}

// Перебор упирается в предел: после исчерпания вход запирается, и верная
// фраза до срока не пускает. Иначе фразу подбирают за вечер.
func TestBruteForceHitsLimit(t *testing.T) {
	a, st := semaWithPhrase(t)
	if err := st.SetSetting(SettingPhraseLimit, "3"); err != nil {
		t.Fatal(err)
	}
	phrase, err := a.SetPhrase()
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := secret.NewPhrase()
	if err != nil {
		t.Fatal(err)
	}
	const source = "203.0.113.77"
	for i := 1; i <= 2; i++ {
		if res := a.TryPhrase(source, foreign); res.OK || !res.Locked.IsZero() {
			t.Fatalf("попытка %d: %+v — рано заперло или пустило", i, res)
		}
	}
	res := a.TryPhrase(source, foreign)
	if res.OK || res.Locked.IsZero() {
		t.Fatalf("третья неудача не заперла вход: %+v", res)
	}
	// верная фраза до срока не пускает, и срок назван — человеку надо
	// знать, когда откроется
	res = a.TryPhrase(source, phrase)
	if res.OK {
		t.Fatal("после запирания верная фраза пустила: предел не работает")
	}
	if res.Locked.IsZero() {
		t.Fatal("заперто, а срок не назван")
	}
	// у соседнего источника свой счёт: предел не должен запирать всех
	if res := a.TryPhrase("198.51.100.5", phrase); !res.OK {
		t.Fatalf("запертый источник запер и другого: %+v", res)
	}
	// успех обнуляет счёт: растущая пауза должна где-то обнуляться, иначе
	// она мерит всю историю, а не нынешний перебор
	const otherSource = "198.51.100.9"
	if res := a.TryPhrase(otherSource, foreign); res.OK {
		t.Fatal("чужая фраза пустила")
	}
	if res := a.TryPhrase(otherSource, phrase); !res.OK {
		t.Fatalf("верная фраза не подошла: %+v", res)
	}
	for i := 1; i <= 2; i++ {
		if res := a.TryPhrase(otherSource, foreign); !res.Locked.IsZero() {
			t.Fatalf("счёт не обнулился после успеха: заперло на %d-й неудаче", i)
		}
	}
}

// Значение по умолчанию у проверки владельца ключа сменилось на required,
// и это сужение доступа названо в журнале при старте. Обратный путь
// обязан оставаться: хранитель ключа без биометрии спрашивает код
// устройства, но если его нет вовсе — человек не должен оказаться заперт.
func TestUserVerificationRequiredButReversible(t *testing.T) {
	a, st := semaWithPhrase(t)
	if a.UserVerificationMode() != "required" {
		t.Fatalf("по умолчанию не required, а %q", a.UserVerificationMode())
	}
	if n := UserVerificationNotice(""); !strings.Contains(n, "required") || !strings.Contains(n, "фраз") {
		t.Fatalf("смена значения не названа или не сказано, чем войти: %q", n)
	}
	if err := st.SetSetting(SettingUserVerification, "preferred"); err != nil {
		t.Fatal(err)
	}
	if a.UserVerificationMode() != "preferred" {
		t.Fatal("человек вернул preferred, а Сёма всё равно требует")
	}
	if n := UserVerificationNotice("preferred"); !strings.Contains(n, "preferred") {
		t.Fatalf("возврат к preferred не назван в журнале: %q", n)
	}
	if n := UserVerificationNotice("required"); n != "" {
		t.Fatalf("required вручную — говорить нечего, а сказано: %q", n)
	}
}
