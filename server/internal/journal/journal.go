// Пакет journal — журнал сервера: строки одного вида, уровни, ротация и
// последняя защита от утечки секретов.
//
// Маски у Сёмы уже есть в двух местах: `engine.maskOutput` прячет значения
// из хранилища в выводе моделей и команд. Здесь — третье, и оно другого
// рода: сюда всё равно попадёт то, о чём никто не подумал, — заголовок
// `Authorization` в тексте ошибки, ключ в строке подключения, сессия
// Telegram в дампе. Поэтому запрет здесь работает по виду значения, а не
// по списку известных секретов: неизвестный секрет тоже не должен попасть
// в файл, который человек потом пришлёт в переписку.
package journal

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Log — файл с ротацией. Пишется одной строкой на запись:
// `2026-09-06 18:20:31 инфо  ядро  текст`.
type Log struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	size     int64
	limit    int64
	keep     int
	toScreen io.Writer
	secrets  []string
	disabled bool
}

// Open — журнал в каталоге данных. `предел` — сколько мегабайт до
// поворота, `хранить` — сколько файлов держать.
func Open(dir string, limit, keep int, toScreen io.Writer) (*Log, error) {
	if limit <= 0 {
		limit = 10
	}
	if keep <= 0 {
		keep = 5
	}
	path := filepath.Join(dir, "sema.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{path: path, file: f, limit: int64(limit) << 20, keep: keep, toScreen: toScreen}
	if info, err := f.Stat(); err == nil {
		l.size = info.Size()
	}
	return l, nil
}

// Hide — значения, которые нельзя писать никогда: сам ключ шифрования,
// пароли ящиков, токены. Их знает вызывающий; неизвестное ловят приметы.
func (l *Log) Hide(values ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range values {
		if len([]rune(v)) >= 6 { // короткое прятать бессмысленно и вредно
			l.secrets = append(l.secrets, v)
		}
	}
}

// Write — чтобы журнал можно было отдать стандартному `log`.
func (l *Log) Write(p []byte) (int, error) {
	line := Mask(string(p), l.secretsCopy())
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.toScreen != nil {
		_, _ = io.WriteString(l.toScreen, line)
	}
	if l.disabled || l.file == nil {
		return len(p), nil
	}
	n, err := io.WriteString(l.file, line)
	l.size += int64(n)
	if l.size >= l.limit {
		l.rotate()
	}
	return len(p), err
}

// Issue — строка выдачи одноразового значения: кода первой установки. Само
// значение проходит в журнал ровно здесь — его надо прочитать в `docker logs`,
// — а дальше прячется в каждой строке: и как известное (Hide), и по виду
// (примета «код установки»). Остальные секреты в строке выдачи прячутся как
// обычно. На экран строка идёт в `screen` (стандартный вывод), а не туда, куда
// пишет журнал: там её ищут.
func (l *Log) Issue(screen io.Writer, text, value string) {
	const slot = "\x00выдача\x00"
	line := time.Now().Format("2006/01/02 15:04:05 ") + strings.TrimRight(text, "\n") + "\n"
	if value != "" {
		line = strings.ReplaceAll(line, value, slot)
	}
	line = strings.ReplaceAll(Mask(line, l.secretsCopy()), slot, value)
	l.mu.Lock()
	if screen != nil {
		_, _ = io.WriteString(screen, line)
	}
	if !l.disabled && l.file != nil {
		n, _ := io.WriteString(l.file, line)
		l.size += int64(n)
		if l.size >= l.limit {
			l.rotate()
		}
	}
	l.mu.Unlock()
	if value != "" {
		l.Hide(value)
	}
}

func (l *Log) secretsCopy() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.secrets))
	copy(out, l.secrets)
	return out
}

// rotate — переименовать нынешний файл и начать новый. Зовётся под
// замком.
func (l *Log) rotate() {
	_ = l.file.Close()
	for i := l.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1))
	}
	_ = os.Rename(l.path, l.path+".1")
	_ = os.Remove(fmt.Sprintf("%s.%d", l.path, l.keep+1))
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.disabled = true // писать некуда: остаёмся с экраном
		return
	}
	l.file, l.size = f, 0
}

// Close — при остановке.
func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// Приметы секретов: то, что не должно попасть в файл ни на каком уровне.
// Ловим по виду значения — неизвестный секрет тоже секрет.
var signs = []struct {
	name string
	re   *regexp.Regexp
}{
	{"ключ", regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"токен", regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._\-/+=]{8,}`)},
	// имя может быть в кавычках (JSON), значение тоже: `"password": "…"`
	{"заголовок", regexp.MustCompile(`(?i)\b(authorization|cookie|set-cookie|x-api-key)"?\s*[:=]\s*"?[^\s",}]+`)},
	// access_token / refresh_token — ответ сервера входа OAuth и наша запись
	// в хранилище: `\btoken` внутри `access_token` границы слова не видит
	// (подчёркивание — часть слова), поэтому названы отдельно; code_verifier
	// и client_secret — те самые значения, которые ни в какую строку не идут
	{"пароль", regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token|api_hash|session|(?:access|refresh|id)[_-]?token|client[_-]?secret|code[_-]?verifier)"?\s*[:=]\s*"?[^\s",}]{4,}`)},
	{"сессия", regexp.MustCompile(`\bsema_(s_)?[A-Za-z0-9_\-]{16,}`)},
	{"адрес с паролем", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s:/@]+:[^\s@]+@`)},
	{"шифр", regexp.MustCompile(`\benc:v1:[A-Za-z0-9+/=]{8,}`)},
	// Контрольная фраза — вход с нового устройства и запасная копия ключа
	// шифрования. Вид у неё свой: семь групп по четыре знака алфавита без
	// похожих букв (internal/secret/phrase.go). Ни одна строка журнала её
	// не печатает нарочно, но журнал человек присылает в переписку, и
	// именно здесь ловится то, о чём никто не подумал
	{"фраза", regexp.MustCompile(`(?i)\b[a-hj-km-np-z2-9]{4}(?:-[a-hj-km-np-z2-9]{4}){6}\b`)},
	// Код первой установки (internal/auth/setup.go): «setup-» и четыре группы
	// того же алфавита. Он печатается нарочно — но ровно одной строкой выдачи
	// (Issue); в любой другой строке, где он всплывёт, его не будет
	{"код установки", regexp.MustCompile(`(?i)\bsetup-[a-hj-km-np-z2-9]{4}(?:-[a-hj-km-np-z2-9]{4}){3}\b`)},
}

// Mask — известные значения и приметы. Отдельная функция, чтобы
// её мог позвать кто угодно и чтобы её было чем проверить.
func Mask(text string, known []string) string {
	for _, s := range known {
		if s != "" {
			text = strings.ReplaceAll(text, s, "···скрыто···")
		}
	}
	for _, s := range signs {
		text = s.re.ReplaceAllString(text, "···"+s.name+" скрыт···")
	}
	return text
}

// Line — как выглядит запись журнала: время, уровень, источник, текст.
func Line(level, source, text string) string {
	return fmt.Sprintf("%s %-5s %-8s %s\n", time.Now().Format("2006-01-02 15:04:05"), level, source, text)
}
