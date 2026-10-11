package auth

// Код первой установки (open-source, решение 5; спека access-control,
// «Код первой установки»).
//
// У новой установки без проверки личности не пускается никто, и первый ключ
// заводится с самой машины — по петле. Но в Docker браузер хозяина приходит в
// контейнер из сети моста, а не по петле, и без другой двери человек со свежей
// установкой не завёл бы ключ вовсе. Дверь — одноразовый код: пока ключей нет,
// Сёма при каждом старте создаёт его и печатает в стандартный вывод вместе с
// адресом входа (его видно в `docker logs` и в журнале службы). Код открывает
// сессию, которой хватает только на заведение ключа, — тот же урезанный вид,
// что у фразы снаружи.
//
// Код перестаёт действовать после первого ключа, по сроку и при перезапуске
// (он живёт только в памяти процесса — store/setup_code.go). На него действует
// тот же предел попыток, что на фразу, и счёт у них общий: перебор не
// становится вдвое дешевле от того, что дверей две.
//
// Почему не фраза, напечатанная при первом старте: фраза живёт долго, и её
// след в журнале остался бы действующим ключом. Код после первого ключа не
// открывает ничего, поэтому печатать его в журнал можно — маска журнала
// пропускает его только в строке выдачи (journal.Log.Issue).
//
// Срок — константа, а не настройка: до первого ключа ни один экран не
// открывается, и настройку срока было бы негде поправить. Истёк — перезапуск
// печатает новый.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	// SetupCodeTTL — сколько живёт код первой установки
	SetupCodeTTL = time.Hour
	// SetupCodePrefix — приставка кода. По ней маска журнала узнаёт код в
	// любой строке, кроме строки выдачи; набирать её необязательно
	SetupCodePrefix = "setup-"
	// setupAlphabet — тот же алфавит без похожих знаков, что у фраз
	// (internal/secret/phrase.go): код переписывают из журнала руками
	setupAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	// setupLen — знаков в коде без приставки: 16 знаков из 31 — около 79 бит,
	// перебор при пределе попыток и часе срока недостижим
	setupLen = 16
)

// IssueSetupCode — новый код первой установки на этот запуск, если ключей ещё
// нет. Возвращает сам код (он нигде больше не хранится — только хэш) и срок.
// Ключи есть — кода нет, ok = false.
func IssueSetupCode(st *store.Store) (code string, until time.Time, ok bool) {
	return issueSetupCode(st, SetupCodeTTL)
}

func issueSetupCode(st *store.Store, ttl time.Duration) (string, time.Time, bool) {
	if st.CountPasskeys() > 0 {
		st.DropSetupCode()
		return "", time.Time{}, false
	}
	body, err := randomSetupBody()
	if err != nil {
		return "", time.Time{}, false
	}
	until := time.Now().Add(ttl)
	st.PutSetupCode(setupHash(body), until)
	return formatSetupCode(body), until, true
}

// randomSetupBody — знаки кода без смещения: байт, не делящийся на алфавит
// нацело, отбрасывается, а не сворачивается по модулю.
func randomSetupBody() (string, error) {
	limit := 256 - 256%len(setupAlphabet)
	var sb strings.Builder
	buf := make([]byte, 32)
	for sb.Len() < setupLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit || sb.Len() == setupLen {
				continue
			}
			sb.WriteByte(setupAlphabet[int(b)%len(setupAlphabet)])
		}
	}
	return sb.String(), nil
}

// formatSetupCode — «setup-xxxx-xxxx-xxxx-xxxx»: группами по четыре, как фраза.
func formatSetupCode(body string) string {
	var parts []string
	for i := 0; i < len(body); i += 4 {
		parts = append(parts, body[i:i+4])
	}
	return SetupCodePrefix + strings.Join(parts, "-")
}

// normalizeSetupCode — набранное человеком к виду для сравнения: регистр,
// пробелы, дефисы и приставка не важны. Не тот вид — пустая строка.
func normalizeSetupCode(raw string) string {
	s := strings.NewReplacer("-", "", " ", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(raw)))
	// приставка снимается по длине, а не по началу: тело кода само может
	// начинаться с тех же букв
	if word := strings.TrimSuffix(SetupCodePrefix, "-"); len(s) == len(word)+setupLen && strings.HasPrefix(s, word) {
		s = s[len(word):]
	}
	if len(s) != setupLen {
		return ""
	}
	for _, c := range s {
		if !strings.ContainsRune(setupAlphabet, c) {
			return ""
		}
	}
	return s
}

// SetupCodeForms — как код может всплыть в чужой строке: целиком, без
// приставки и одной строкой без дефисов. Журнал прячет все три (Hide), а не
// только тот вид, в каком код выдан.
func SetupCodeForms(code string) []string {
	body := normalizeSetupCode(code)
	if body == "" {
		return nil
	}
	full := formatSetupCode(body)
	return []string{full, strings.TrimPrefix(full, SetupCodePrefix), body}
}

func setupHash(body string) string {
	sum := sha256.Sum256([]byte("sema-setup:" + body))
	return hex.EncodeToString(sum[:])
}

// SetupLine — строка выдачи для журнала и стандартного вывода: код, срок и
// адрес входа. Человек читает её в `docker logs`, поэтому в ней всё, что
// нужно сделать, а не только сам код.
func SetupLine(code string, until time.Time, origin string) string {
	return fmt.Sprintf("вход: ключей ещё нет — код первой установки %s (действует до %s и до перезапуска). "+
		"Откройте %s/login, введите код и заведите passkey; с первым ключом код погаснет",
		code, until.Format("15:04"), strings.TrimSuffix(origin, "/"))
}

// SetupActive — есть ли сейчас действующий код: выдан, не истёк, ключей нет.
func (a *Auth) SetupActive() bool {
	hash, until, ok := a.st.SetupCode()
	return ok && hash != "" && time.Now().Before(until) && a.st.CountPasskeys() == 0
}

// SetupResult — исход попытки входа кодом первой установки.
type SetupResult struct {
	OK bool
	// Gone — код был, но больше не действует: заведён первый ключ или вышел
	// срок. Человеку это говорится иначе, чем «код не подходит»
	Gone bool
	// Locked — заперто до этого момента (общий с фразой предел попыток)
	Locked time.Time
}

// TrySetupCode — проверка кода с указанного источника. Неудачи считаются
// вместе с неудачами фразы и запирают тем же растущим сроком; заперто — код
// не проверяется вовсе.
func (a *Auth) TrySetupCode(source, raw string) SetupResult {
	if ok, until := a.phraseAllowed(source); !ok {
		return SetupResult{Locked: until}
	}
	hash, until, has := a.st.SetupCode()
	gone := false
	if has && (a.st.CountPasskeys() > 0 || !time.Now().Before(until)) {
		// с первым ключом или по сроку код гаснет насовсем: следующий даст
		// только перезапуск, и то лишь пока ключей нет
		a.st.DropSetupCode()
		has, gone = false, true
	}
	body := normalizeSetupCode(raw)
	if !has || body == "" || subtle.ConstantTimeCompare([]byte(setupHash(body)), []byte(hash)) != 1 {
		return SetupResult{Gone: gone, Locked: a.phraseFail(source)}
	}
	a.phraseSuccess(source)
	return SetupResult{OK: true}
}

// EndSetup — заведён ключ: код первой установки гаснет, и сессии, открытые
// им, закрываются — пускать к заведению ключей им больше незачем. Возвращает,
// было ли что гасить.
func (a *Auth) EndSetup() bool {
	_, _, had := a.st.SetupCode()
	a.st.DropSetupCode()
	n, _ := a.st.DropSetupSessions()
	return had || n > 0
}
