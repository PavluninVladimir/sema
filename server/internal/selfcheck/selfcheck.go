// Пакет selfcheck — одно описание самоосмотра на две двери.
//
// Дверей у самоосмотра две, и они отвечают на разные вопросы. `-doctor`
// спрашивает «поднимется ли Сёма» и обязан работать, когда Сёма не
// поднимается: ни движка, ни моделей у него нет. Наблюдатель спрашивает
// «идёт ли работа» у живого сервера.
//
// Порознь эти двери разъезжаются молча. Сертификат, годный до вчера,
// `-doctor` называет бедой — но его запускают раз в месяц, а Сёма в это
// время работает и не знает. Ключ шифрования, переставший подходить,
// затирает секреты на живом сервере, а спрашивают о нём только при запуске.
// Поэтому проверка описывается один раз, и у неё есть признак «когда
// задавать»: обе двери перебирают один и тот же список.
//
// Здесь нет ни одной проверки, которой нужен движок: всё, что перечислено,
// смотрится своими силами по каталогу, файлам и базе. Это и позволяет одному
// описанию служить обеим дверям.
package selfcheck

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/host"
	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Когда задавать вопрос.
type When int

const (
	AtStartup    When = 1 << iota // только `-doctor`: поднимется ли
	WhileRunning                  // только наблюдатель: у живого сервера
	Always       = AtStartup | WhileRunning
)

// Итог одной проверки. «Беда» мешает работе, «внимание» — нет, но и «ок»
// тут неправда.
type Result struct {
	Name    string
	Line    string
	Advice  string
	Trouble bool
	Warn    bool
}

func (r Result) Level() string {
	switch {
	case r.Trouble:
		return "alert"
	case r.Warn:
		return "warn"
	}
	return "ok"
}

// Env — всё, чем проверка располагает. Движка здесь нет нарочно: проверка,
// которой нужен движок, второй двери недоступна, а значит это не общая
// проверка, а проверка наблюдателя.
type Env struct {
	Conf *config.Config
	DB   *store.Store // может быть nil: база не открылась или её ещё нет
	Key  string       // значение переменной с ключом шифрования
}

// Check — один вопрос к самому себе.
type Check struct {
	Name string
	When When
	// Реже — не задавать этот вопрос на ходу чаще, чем раз в столько.
	//
	// Ноль значит «каждый проход». Дорогие вопросы обязаны его называть:
	// проход наблюдателя идёт раз в пять минут, а `quick_check` по базе в
	// 188 МБ занимает на Pi почти полсекунды. Сторож, съедающий машину, и
	// есть та беда, которую он ищет; при запуске же спешить некуда и это
	// поле не смотрят.
	MinInterval time.Duration
	Look        func(Env) Result
}

// All — единственное описание. Добавленная сюда проверка немедленно видна
// обеим дверям: и `-doctor`, и наблюдателю.
func All() []Check {
	return []Check{
		{Name: "Каталог данных", When: AtStartup, Look: dataDir},
		{Name: "Замок", When: AtStartup, Look: lock},
		{Name: "Адрес", When: AtStartup, Look: addr},
		{Name: "Прошлое закрытие", When: AtStartup, Look: lastShutdown},
		{Name: "Целостность базы", When: Always, MinInterval: 24 * time.Hour, Look: integrity},
		{Name: "Ключ шифрования", When: Always, MinInterval: time.Hour, Look: encryptionKey},
		{Name: "Защищённое соединение", When: AtStartup, Look: secureConn},
		{Name: "Срок сертификата", When: Always, MinInterval: time.Hour, Look: certExpiry},
		{Name: "Место на диске", When: Always, Look: diskSpace},
		{Name: "Журнал", When: AtStartup, Look: logFile},
		{Name: "Инструменты разбора", When: Always, Look: tools},
	}
}

// For — проверки одной двери.
func For(when When) []Check {
	var out []Check
	for _, c := range All() {
		if c.When&when != 0 {
			out = append(out, c)
		}
	}
	return out
}

// Collect — задать все вопросы одной двери и вернуть ответы по порядку.
// Обе двери ходят сюда: разъехаться им негде.
func Collect(when When, env Env) []Result {
	var out []Result
	for _, c := range For(when) {
		r := c.Look(env)
		r.Name = c.Name
		out = append(out, r)
	}
	return out
}

// ---- сами проверки ----

func dataDir(env Env) Result {
	info, err := os.Stat(env.Conf.Data)
	switch {
	case err != nil:
		return Result{Line: env.Conf.Data + " — нет", Trouble: true,
			Advice: "создайте каталог или укажите другой: -data /путь"}
	case !info.IsDir():
		return Result{Line: env.Conf.Data + " — это файл, а не каталог", Trouble: true, Advice: "укажите каталог"}
	}
	probe := filepath.Join(env.Conf.Data, ".проба-записи")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return Result{Line: env.Conf.Data + " — нет прав на запись", Trouble: true,
			Advice: "дайте права пользователю, от которого работает Сёма"}
	}
	_ = os.Remove(probe)
	return Result{Line: env.Conf.Data + " — на месте, запись есть"}
}

// lock — занят ли каталог другим сервером. Каталога нет — проверять нечего,
// и говорить «занят» было бы неправдой: об этом уже сказано выше.
func lock(env Env) Result {
	if _, err := os.Stat(env.Conf.Data); err != nil {
		return Result{Line: "не проверял: сначала нужен каталог"}
	}
	l, err := host.Acquire(env.Conf.Data)
	if err != nil {
		holder := host.Owner(env.Conf.Data)
		line := "не взялся: " + err.Error()
		if holder.PID != 0 {
			line = "каталог занят: " + holder.String()
		}
		return Result{Line: line, Trouble: true,
			Advice: "один каталог — один сервер; остановите тот, что держит, или укажите другой каталог"}
	}
	l.Release()
	return Result{Line: "свободен — можно запускаться"}
}

func addr(env Env) Result {
	ln, err := net.Listen("tcp", env.Conf.Listen)
	if err != nil {
		return Result{Line: env.Conf.Listen + " занят", Trouble: true,
			Advice: "остановите того, кто держит порт, или задайте другой: -listen"}
	}
	_ = ln.Close()
	return Result{Line: env.Conf.Listen + " свободен"}
}

func lastShutdown(env Env) Result {
	if env.DB == nil {
		return Result{Line: "не проверял: базы нет"}
	}
	if !env.DB.CleanShutdown() {
		// это не «беда» — запуску не мешает; но и «ок» тут неправда
		return Result{Line: "неначисто — сервер убили или он упал", Warn: true,
			Advice: "проверьте очередь: незавершённые внешние действия ждут решения"}
	}
	return Result{Line: "начисто"}
}

func integrity(env Env) Result {
	if env.DB == nil {
		return Result{Line: "не проверял: базы нет"}
	}
	res, err := env.DB.QuickCheck()
	switch {
	case err != nil:
		return Result{Line: "проверить не вышло: " + err.Error(), Trouble: true}
	case res != "ok":
		return Result{Line: res, Trouble: true,
			Advice: "снимите копию и восстановите: sqlite3 sema.db \".recover\" | sqlite3 новая.db"}
	}
	return Result{Line: "в порядке"}
}

// encryptionKey — подходит ли нынешний ключ к уже сохранённым секретам.
//
// Вопрос живому серверу не менее важен, чем запускающемуся: чужой ключ не
// мешает работать, он молча затирает секреты — так уже пропал корень
// удостоверяющего центра.
func encryptionKey(env Env) Result {
	if env.DB == nil {
		return Result{Line: "не проверял: базы нет"}
	}
	advice := "без верного ключа секреты не читаются, а запись их затрёт — см. " + env.Conf.KeyEnvName()
	if env.Key == "" {
		return Result{Line: "переменная " + env.Conf.KeyEnvName() + " пуста — секреты не читаются и не пишутся",
			Trouble: true, Advice: advice}
	}
	if box, err := secret.NewFromEnv(env.Key); err == nil && box != nil {
		env.DB.UseSecretBox(box)
	}
	ok, known := env.DB.KeyMatches()
	switch {
	case !known:
		return Result{Line: "проверить нечем: ни проверочной записи, ни секретов — заведётся при первом запуске"}
	case !ok:
		hint := ""
		if _, perr := secret.ParsePhrase(env.Key); secret.LooksLikePhrase(env.Key) && perr != nil {
			hint = " · " + perr.Error()
		}
		return Result{Line: "не подходит: секреты этим ключом не читаются" + hint, Trouble: true, Advice: advice}
	}
	return Result{Line: "подходит"}
}

// secureConn — настроено ли https вообще. Вопрос запуска: это
// решение человека, а не беда, которая случилась сама. Наблюдателю его
// задавать нельзя — он повторял бы одно и то же каждые пять минут, пока
// человек не настроит, и научил бы не читать себя.
func secureConn(env Env) Result {
	if env.Conf.TLS.Cert == "" || env.Conf.TLS.Key == "" {
		return Result{Line: "не настроено", Warn: true,
			Advice: "без https браузер не даст ни войти по passkey с другого устройства, ни поставить значок на экран, ни слать пуши"}
	}
	if _, err := os.Stat(env.Conf.TLS.Cert); err != nil {
		return Result{Line: "сертификата нет: " + env.Conf.TLS.Cert, Trouble: true, Advice: "проверьте путь и права"}
	}
	return Result{Line: "настроено на " + env.Conf.TLS.Listen}
}

// certExpiry — и есть тот случай, ради которого заведены две двери.
// Срок кончается на работающем сервере, а спрашивал о нём только запуск: до
// следующего `-doctor` может пройти месяц, и всё это время человек с
// телефона просто не сможет войти.
func certExpiry(env Env) Result {
	if env.Conf.TLS.Cert == "" {
		return Result{Line: "нечему кончаться: https не настроено"}
	}
	until, err := CertExpiry(env.Conf.TLS.Cert)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Line: "сертификата нет: " + env.Conf.TLS.Cert, Trouble: true, Advice: "проверьте путь и права"}
		}
		return Result{Line: "сертификат не читается: " + err.Error(), Trouble: true}
	}
	line := "годен до " + until.Format("02.01.2006")
	switch left := time.Until(until); {
	case left <= 0:
		return Result{Line: line + " — кончился", Trouble: true,
			Advice: "выпустите новый: по истёкшему сертификату браузер не пустит вовсе"}
	case left < 14*24*time.Hour:
		return Result{Line: line, Warn: true, Advice: "сертификат скоро кончится — выпустите новый"}
	}
	return Result{Line: line}
}

// diskSpace — пульс пишет число каждую минуту, но порога у него нет.
func diskSpace(env Env) Result {
	free := host.DiskFree(env.Conf.Data)
	if free < 0 {
		return Result{Line: "не спросить"}
	}
	mb := free / (1 << 20)
	line := fmt.Sprintf("свободно %d МБ", mb)
	switch {
	case mb < 200:
		return Result{Line: line, Trouble: true,
			Advice: "база в WAL и копии не поместятся — освободите место немедленно"}
	case mb < 1000:
		return Result{Line: line, Warn: true, Advice: "копии и журнал скоро упрутся"}
	}
	return Result{Line: line}
}

func logFile(env Env) Result {
	info, err := os.Stat(filepath.Join(env.Conf.Data, "sema.log"))
	if err != nil {
		return Result{Line: "ещё не заводился"}
	}
	return Result{Line: fmt.Sprintf("%s/sema.log — %.1f МБ", env.Conf.Data, float64(info.Size())/(1<<20))}
}

// tools — их отсутствие не беда само по себе; бедой оно становится,
// когда настройка называет инструмент, которого нет. Про это спрашивает
// отдельная проверка наблюдателя, здесь — просто что есть на машине.
func tools(Env) Result {
	var have, miss []string
	for _, t := range []string{"pdftotext", "tesseract", "sqlite3"} {
		if InPath(t) {
			have = append(have, t)
		} else {
			miss = append(miss, t)
		}
	}
	line := "есть: " + strings.Join(have, ", ")
	if len(have) == 0 {
		line = "нет ни одного"
	}
	if len(miss) > 0 {
		line += " · нет: " + strings.Join(miss, ", ")
	}
	return Result{Line: line}
}

// InPath — лежит ли исполняемый файл в PATH.
func InPath(name string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// CertExpiry — до какого числа он годен. Читаем сами, без движка:
// осмотр обязан работать, когда Сёма не поднимается.
func CertExpiry(path string) (time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return time.Time{}, fmt.Errorf("это не сертификат")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}
