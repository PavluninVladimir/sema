package auth

// Вход по контрольной фразе — способ войти с устройства, на котором ещё
// нет ключа: телефон в дороге, консоли нет, а шлюз наружу пускает
// только по HTTP. Фразу генерирует Сёма (internal/secret/phrase.go —
// формат уже выбран для фразы восстановления ключа шифрования, второй тут
// не заводим), хранится она медленным хэшем (argon2id): это единственный
// секрет, который человек носит в голове, а копия базы не должна стать
// поводом для офлайн-перебора.
//
// Предел попыток живёт здесь же, а не только у шлюза: из домашней
// сети шлюза нет вовсе, а 128 у ceremonies в auth.go — это про
// память процесса, не про перебор. Счёт — по источнику запроса (тот же
// clientIP, что и в журнале доступа), в памяти процесса: перезапуск
// сервера не в руках того, кто подбирает фразу, а персистентная таблица
// добавила бы ту же защиту вторым способом рядом с картой церемоний,
// которая уже решает эту задачу для WebAuthn.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

const (
	// SettingPhraseLimit — сколько неудач подряд с одного источника до
	// запирания. Порог, а не гвоздь в коде — правится настройкой.
	SettingPhraseLimit = "auth.phrase_limit"
	// Параметры argon2id — настройки, чтобы их можно было поднять на
	// более быстрой машине, не переписывая код. По умолчанию — лёгкий
	// профиль OWASP (t=2, m=19 МиБ, p=1): вход по фразе редкий, а Pi
	// небогата памятью (см. грабли про --cache-ram).
	settingPhraseTime    = "auth.phrase_argon_time"
	settingPhraseMemory  = "auth.phrase_argon_memory_kb"
	settingPhraseThreads = "auth.phrase_argon_threads"
)

// phraseLock — состояние перебора с одного источника.
type phraseLock struct {
	fails  int
	locked time.Time // не нулевое — заперто до этого момента
}

// phraseGuard — предел попыток по источнику запроса.
type phraseGuard struct {
	mu    sync.Mutex
	locks map[string]*phraseLock
}

// PhraseResult — исход попытки входа фразой.
type PhraseResult struct {
	OK     bool
	Typo   bool      // вид верный, а контрольная группа не сошлась — не то же самое, что «неверная фраза»
	Locked time.Time // не нулевое — заперто до этого момента; фраза не проверялась вовсе, если попытка началась запертой
}

// phraseParams — параметры argon2id из настроек.
func (a *Auth) phraseParams() (t uint32, m uint32, p uint8) {
	t = 2
	m = 19456
	p = 1
	if v, err := strconv.Atoi(a.st.Setting(settingPhraseTime, "")); err == nil && v > 0 {
		t = uint32(v)
	}
	if v, err := strconv.Atoi(a.st.Setting(settingPhraseMemory, "")); err == nil && v > 0 {
		m = uint32(v)
	}
	if v, err := strconv.Atoi(a.st.Setting(settingPhraseThreads, "")); err == nil && v > 0 && v < 256 {
		p = uint8(v)
	}
	return
}

// hashPhrase — новый хэш фразы: свой формат `argon2id$t$m$p$соль$хэш`,
// поле, а не библиотечная строка — тут только argon2id и один вид записи.
func hashPhrase(phrase string, t, m uint32, p uint8) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(phrase), salt, t, m, p, 32)
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s", t, m, p,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

// verifyPhraseHash — сравнение постоянным временем, параметры берутся из
// самого хэша: смена настройки не делает старую фразу нечитаемой.
func verifyPhraseHash(encoded, phrase string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false
	}
	t, err1 := strconv.Atoi(parts[1])
	m, err2 := strconv.Atoi(parts[2])
	p, err3 := strconv.Atoi(parts[3])
	salt, err4 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err5 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || t <= 0 || m <= 0 || p <= 0 || p > 255 {
		return false
	}
	got := argon2.IDKey([]byte(phrase), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1
}

// SetPhrase — новая фраза генерирует Сёма, а не человек; возвращается
// один раз — второго способа её узнать нет. Заменяет прежнюю целиком:
// прежняя перестаёт пускать с этого мгновения.
func (a *Auth) SetPhrase() (string, error) {
	raw, err := secret.NewPhrase()
	if err != nil {
		return "", err
	}
	t, m, p := a.phraseParams()
	hash, err := hashPhrase(raw, t, m, p)
	if err != nil {
		return "", err
	}
	if err := a.st.SetLoginPhrase(hash); err != nil {
		return "", err
	}
	a.rememberPhrase(raw)
	return raw, nil
}

// RevokePhrase — фраза больше не пускает никого.
func (a *Auth) RevokePhrase() error {
	a.forgetPhrase()
	return a.st.ClearLoginPhrase()
}

// HasPhrase — заведена ли фраза сейчас.
func (a *Auth) HasPhrase() bool {
	_, _, ok := a.st.LoginPhrase()
	return ok
}

// PhraseCreated — когда заведена нынешняя фраза.
func (a *Auth) PhraseCreated() (time.Time, bool) {
	_, created, ok := a.st.LoginPhrase()
	return created, ok
}

// rememberPhrase/LastPhrase — фраза видна недолго после создания: как и
// свежий токен на «Доступе», значение не хранится, а показывается один
// раз, пока не истекли те же три минуты.
func (a *Auth) rememberPhrase(p string) {
	a.mu.Lock()
	a.lastPhrase, a.lastPhraseAt = p, time.Now()
	a.mu.Unlock()
}

func (a *Auth) forgetPhrase() {
	a.mu.Lock()
	a.lastPhrase, a.lastPhraseAt = "", time.Time{}
	a.mu.Unlock()
}

// LastPhrase — свежесозданная фраза, если с её создания прошло меньше
// трёх минут.
func (a *Auth) LastPhrase() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastPhrase == "" || time.Since(a.lastPhraseAt) > 3*time.Minute {
		a.lastPhrase = ""
		return "", false
	}
	return a.lastPhrase, true
}

// PhraseLimit — попыток подряд с одного источника до запирания; предел
// правится на «Доступе», а не забит в коде.
func (a *Auth) PhraseLimit() int { return a.phraseLimit() }

// phraseLimit — попыток подряд с одного источника до запирания.
func (a *Auth) phraseLimit() int {
	return a.st.SettingInt(phraseLimitDef)
}

// phraseLockStep — растущий срок запирания: минута, пять, полчаса, два
// часа — тот же шаг, что уже держит очередь (engine/queue.go,
// retryDelay); второй вид растущей паузы в проекте заводить незачем.
func phraseLockStep(over int) time.Duration {
	switch {
	case over <= 1:
		return time.Minute
	case over == 2:
		return 5 * time.Minute
	case over == 3:
		return 30 * time.Minute
	default:
		return 2 * time.Hour
	}
}

// phraseAllowed — можно ли сейчас пробовать с этого источника; не
// пускает пока заперто, и не потребляет попытку заново — знакомая
// формулировка «когда откроется» важнее ещё одного щелчка счётчика.
func (a *Auth) phraseAllowed(source string) (bool, time.Time) {
	a.guard.mu.Lock()
	defer a.guard.mu.Unlock()
	l := a.guard.locks[source]
	if l == nil || l.locked.IsZero() {
		return true, time.Time{}
	}
	if time.Now().Before(l.locked) {
		return false, l.locked
	}
	return true, time.Time{}
}

// phraseFail — неудачная попытка: счёт растёт, после предела — запирание
// на растущий срок. Возвращает время открытия, если этой попыткой заперло.
func (a *Auth) phraseFail(source string) time.Time {
	limit := a.phraseLimit()
	a.guard.mu.Lock()
	defer a.guard.mu.Unlock()
	if a.guard.locks == nil {
		a.guard.locks = map[string]*phraseLock{}
	}
	l := a.guard.locks[source]
	if l == nil {
		l = &phraseLock{}
		a.guard.locks[source] = l
	}
	l.fails++
	if l.fails >= limit {
		l.locked = time.Now().Add(phraseLockStep(l.fails - limit + 1))
		return l.locked
	}
	return time.Time{}
}

// phraseSuccess — успешный вход обнуляет счёт: растущая пауза должна
// мерить нынешний перебор, а не всю историю (те же грабли, что у паузы
// переподключения агента).
func (a *Auth) phraseSuccess(source string) {
	a.guard.mu.Lock()
	delete(a.guard.locks, source)
	a.guard.mu.Unlock()
}

// TryPhrase — проверка фразы с указанного источника; сама считает
// неудачи и запирает. Заперто — фраза не проверяется вовсе, чтобы запертый
// перебор не тратил argon2 попусту.
func (a *Auth) TryPhrase(source, raw string) PhraseResult {
	if ok, until := a.phraseAllowed(source); !ok {
		return PhraseResult{Locked: until}
	}
	hash, _, has := a.st.LoginPhrase()
	if !has {
		until := a.phraseFail(source)
		return PhraseResult{Locked: until}
	}
	canon, err := secret.ParsePhrase(raw)
	if err != nil {
		typo := strings.Contains(err.Error(), "опечатка")
		until := a.phraseFail(source)
		return PhraseResult{Typo: typo, Locked: until}
	}
	if !verifyPhraseHash(hash, canon) {
		until := a.phraseFail(source)
		return PhraseResult{Locked: until}
	}
	a.phraseSuccess(source)
	return PhraseResult{OK: true}
}
