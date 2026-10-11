package engine

// Проверка 6: самоосмотр у живого сервера.
//
// Вопросы здесь не свои — те же самые, что задаёт `-doctor`. Описание одно
// (`internal/selfcheck`), а дверей две, и это не удобство, а необходимость:
// `-doctor` запускают, когда что-то уже сломалось, а сертификат, ключ и
// место на диске портятся на работающей Сёме. Сертификат, годный до вчера,
// осмотр назовёт бедой — через месяц, когда его в следующий раз запустят.
//
// Проверка ходит только по файлам и своей базе: ни моделей, ни сети.

import (
	"os"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/selfcheck"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const watchDoctorKey = "watch.doctor" // задавать ли вопросы запуска на ходу

// WatchDoctor — спрашивать ли самоосмотр у живого сервера.
func (e *Engine) WatchDoctor() bool { return e.store.Setting(watchDoctorKey, "1") == "1" }

func (e *Engine) SetWatchDoctor(on bool) error {
	return e.store.SetSetting(watchDoctorKey, boolSetting(on))
}

// selfCheckEnv — что наблюдатель может дать общим проверкам. База та же,
// живая: проверки этого пакета только читают.
func (e *Engine) selfCheckEnv() selfcheck.Env {
	return selfcheck.Env{Conf: e.conf, DB: e.store, Key: os.Getenv(e.conf.KeyEnvName())}
}

// checkSelfCheck — те же вопросы, что у `-doctor`, заданные на ходу.
//
// Дорогой вопрос задаётся не каждый проход, а раз в свой срок: проход идёт
// раз в пять минут, а `quick_check` по базе в 188 МБ занимает на Pi почти
// полсекунды. Сторож, съедающий машину, и есть та беда, которую он ищет.
func (e *Engine) checkSelfCheck() int {
	if !e.WatchDoctor() {
		return 0
	}
	env := e.selfCheckEnv()
	found := 0
	for _, p := range selfcheck.For(selfcheck.WhileRunning) {
		if !e.dueAsk(p) {
			continue
		}
		i := p.Look(env)
		i.Name = p.Name
		key := "осмотр:" + i.Name
		if !i.Trouble && !i.Warn {
			e.closeIfWas(key, i.Line)
			continue
		}
		level := store.FindingWarn
		if i.Trouble {
			level = store.FindingAlert
		}
		numbers := i.Line
		if i.Advice != "" {
			numbers += " · " + i.Advice
		}
		e.sayAboutFinding(store.Finding{
			Key: key, Kind: "самоосмотр", Ref: i.Name, Level: level,
			Summary: lowerFirst(i.Name) + ": " + i.Line,
			Numbers: numbers,
			// самоосмотр ничего не останавливает: он говорит о состоянии
			// машины, а не о работе, которая идёт впустую
			Measure: store.MeasureAlert, Screen: lookWhere(i.Name),
		})
		found++
	}
	return found
}

// dueAsk — не задавали ли этот вопрос слишком недавно. Метка пишется
// только у вопросов со сроком: у остальных её незачем и трогать, а лишняя
// запись в настройки каждые пять минут заливает журнал изменений.
func (e *Engine) dueAsk(p selfcheck.Check) bool {
	if p.MinInterval <= 0 {
		return true
	}
	key := "watch.doctor.last." + p.Name
	when, _ := time.Parse(time.RFC3339, e.store.Setting(key, ""))
	if time.Since(when) < p.MinInterval {
		return false
	}
	_ = e.store.SetSetting(key, time.Now().Format(time.RFC3339))
	return true
}

// lowerFirst — «Срок сертификата» → «срок сертификата». По рунам:
// кириллическая буква в два байта, и срез по байту разрубил бы её пополам.
func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToLower(string(r[0])) + string(r[1:])
}

// lookWhere — у находки всегда есть действие, и оно ведёт туда, где чинят.
func lookWhere(what string) string {
	switch what {
	case "Срок сертификата", "Защищённое соединение":
		return "settings:access~network"
	case "Ключ шифрования":
		// ключ шифрования живёт на вкладке «Шифрование» «Ключей и секретов»
		return SecretScreen("", secretTabCrypt)
	case "Инструменты разбора":
		return "settings:tools"
	}
	return "health"
}
