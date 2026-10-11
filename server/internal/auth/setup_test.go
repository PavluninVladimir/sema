package auth

// Код первой установки: подходит, пока ключей нет; гаснет с первым ключом, по
// сроку и при перезапуске; подбор упирается в общий с фразой предел.
// Проверяется настоящий путь — тот же TrySetupCode, который зовёт ручка входа.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/journal"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestSetupCodeFitsUntilFirstKey(t *testing.T) {
	a, st := semaWithPhrase(t)
	code, until, ok := IssueSetupCode(st)
	if !ok || code == "" {
		t.Fatal("ключей нет, а кода первой установки нет")
	}
	if !regexp.MustCompile(`^setup-[a-hj-km-np-z2-9]{4}(-[a-hj-km-np-z2-9]{4}){3}$`).MatchString(code) {
		t.Fatalf("вид кода %q: ждали setup- и четыре группы алфавита без похожих знаков", code)
	}
	if d := time.Until(until); d <= 0 || d > SetupCodeTTL {
		t.Errorf("срок кода %v", d)
	}
	if !a.SetupActive() {
		t.Fatal("код выдан, а SetupActive молчит")
	}
	// набирают как удобно: без приставки, заглавными, пробелами вместо дефисов
	body := strings.TrimPrefix(code, SetupCodePrefix)
	for _, typed := range []string{code, body, strings.ToUpper(code), strings.ReplaceAll(body, "-", " ")} {
		if res := a.TrySetupCode("203.0.113.10", typed); !res.OK {
			t.Fatalf("свой же код %q не подошёл: %+v", typed, res)
		}
	}
	чужой := code[:len(code)-1] + "2"
	if strings.HasSuffix(code, "2") {
		чужой = code[:len(code)-1] + "3"
	}
	if res := a.TrySetupCode("203.0.113.10", чужой); res.OK {
		t.Fatal("чужой код подошёл")
	}
	// первый ключ заведён — код больше не открывает ничего, и это «погас», а
	// не «не подходит»
	if _, err := st.AddPasskey("мак", "{}", "localhost"); err != nil {
		t.Fatal(err)
	}
	if res := a.TrySetupCode("203.0.113.11", code); res.OK || !res.Gone {
		t.Fatalf("код принят после первого ключа: %+v", res)
	}
	if a.SetupActive() {
		t.Error("после первого ключа код всё ещё действует")
	}
	// и новый старт кода не выдаёт: ключи есть
	if _, _, ok := IssueSetupCode(st); ok {
		t.Error("при заведённых ключах выдан новый код")
	}
}

func TestSetupCodeDiesByTimeAndRestart(t *testing.T) {
	a, st := semaWithPhrase(t)
	code, _, ok := issueSetupCode(st, -time.Second)
	if !ok {
		t.Fatal("код не выдан")
	}
	if res := a.TrySetupCode("203.0.113.10", code); res.OK || !res.Gone {
		t.Fatalf("просроченный код принят: %+v", res)
	}
	// перезапуск: та же база, новый процесс — код жил только в памяти прежнего
	path := filepath.Join(t.TempDir(), "sema.db")
	before, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ = IssueSetupCode(before)
	if res := New(before).TrySetupCode("203.0.113.10", code); !res.OK {
		t.Fatalf("до перезапуска код не подошёл: %+v", res)
	}
	_ = before.Close()
	after, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if res := New(after).TrySetupCode("203.0.113.10", code); res.OK {
		t.Fatal("код пережил перезапуск")
	}
}

// Предел попыток общий с фразой: неудачи фразы и кода с одного источника
// складываются, а запертый вход не принимает и верный код.
func TestSetupCodeSharesPhraseLimit(t *testing.T) {
	a, st := semaWithPhrase(t)
	if err := st.SetSetting(SettingPhraseLimit, "3"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := IssueSetupCode(st)
	const src = "198.51.100.7"
	a.TryPhrase(src, "неверная фраза")
	a.TryPhrase(src, "и ещё одна")
	res := a.TrySetupCode(src, "setup-aaaa-bbbb-cccc-dddd")
	if res.Locked.IsZero() {
		t.Fatalf("две неудачи фразы и одна кода при пределе 3 не заперли: %+v", res)
	}
	if res := a.TrySetupCode(src, code); res.OK || res.Locked.IsZero() {
		t.Fatalf("запертый вход принял верный код: %+v", res)
	}
	// другой источник не заперт: предел — по источнику
	if res := a.TrySetupCode("198.51.100.8", code); !res.OK {
		t.Fatalf("соседний источник заперт чужим перебором: %+v", res)
	}
}

// С первым ключом гаснет и код, и сессии, открытые им.
func TestEndSetupDropsCodeAndSessions(t *testing.T) {
	a, st := semaWithPhrase(t)
	IssueSetupCode(st)
	token, err := a.OpenSession(0, "Safari", "172.17.0.1", store.SetupSessionNote)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionRestricted(store.HashSecret(token), true); err != nil {
		t.Fatal(err)
	}
	phraseToken, err := a.OpenSession(0, "Safari", "203.0.113.5", "вход по фразе (снаружи — только заведение ключа)")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionRestricted(store.HashSecret(phraseToken), true); err != nil {
		t.Fatal(err)
	}
	if se, ok := st.SessionByToken(token, SessionTTL); !ok || !se.BySetupCode() {
		t.Fatal("сессия по коду не узнаётся")
	}
	if !a.EndSetup() {
		t.Fatal("гасить было что, а EndSetup говорит «нечего»")
	}
	if _, _, ok := st.SetupCode(); ok {
		t.Error("код остался после первого ключа")
	}
	if _, ok := st.SessionByToken(token, SessionTTL); ok {
		t.Error("сессия по коду пережила первый ключ")
	}
	if _, ok := st.SessionByToken(phraseToken, SessionTTL); !ok {
		t.Error("заодно закрылась сессия по фразе — её закрывает только её собственный ключ")
	}
}

// Код в журнале узнаётся по виду: в любой строке, кроме строки выдачи, его нет.
// Вид кода живёт здесь, а примета — в журнале; расходятся — тест падает.
func TestSetupCodeMaskedByJournal(t *testing.T) {
	_, st := semaWithPhrase(t)
	code, _, _ := IssueSetupCode(st)
	for _, line := range []string{"ввёл " + code, "запрос: {\"code\":\"" + code + "\"}"} {
		if got := journal.Mask(line, nil); strings.Contains(got, code) {
			t.Errorf("код прошёл маску журнала: %s", got)
		}
	}
	for _, form := range SetupCodeForms(code) {
		if got := journal.Mask("строка с "+form, SetupCodeForms(code)); strings.Contains(got, form) {
			t.Errorf("вид кода %q не спрятан известным значением: %s", form, got)
		}
	}
}
