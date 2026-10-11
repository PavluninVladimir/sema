package engine

import (
	"testing"
	"time"
)

// Планировщик обязан понимать те расписания, которыми Сёма сам заводит
// свои задачи. 12 сентября 2026 он не понимал ни одного суточного:
// задачи сеются строкой «каждый день в 03:30», а разбор принимал только
// голую пару «03:30». taskDue молча возвращал false, и «Копия наружу» не
// снялась ни разу за всё своё существование — на экране при этом стояло
// «включена» и красивое расписание. Ни ошибки, ни строки в журнале.
//
// Список берётся из самих встроенных задач, а не выписывается руками:
// выписанный разошёлся бы с кодом ровно так же, как разошлись разбор и
// посев.
func TestScheduleBuiltinTasksUnderstood(t *testing.T) {
	now := time.Now()
	long := now.Add(-72 * time.Hour)
	for _, z := range builtinTasks {
		if z.Schedule == "" || z.Root {
			continue
		}
		if _, err := nextRun(z.Schedule, long, now); err != nil {
			t.Errorf("задача «%s»: расписание %q планировщику непонятно — она не запустится никогда",
				z.Title, z.Schedule)
		}
	}
}

// И наоборот: то, что планировщик понимает, должно доживать до запуска.
// Суточная задача, не запускавшаяся со вчера, сегодня после своего часа
// обязана быть пора.
func TestDailyTaskAfterOwnHourDue(t *testing.T) {
	loc := time.FixedZone("+04", 4*3600)
	yesterday := time.Date(2026, 9, 11, 15, 46, 0, 0, loc)
	for _, when := range []string{"каждый день в 03:30", "03:30"} {
		to := time.Date(2026, 9, 12, 3, 29, 0, 0, loc)
		after := time.Date(2026, 9, 12, 3, 31, 0, 0, loc)
		if due, err := nextRun(when, yesterday, to); err != nil || due {
			t.Errorf("%q: до своего часа уже пора (%v, %v)", when, due, err)
		}
		if due, err := nextRun(when, yesterday, after); err != nil || !due {
			t.Errorf("%q: после своего часа не пора (%v, %v)", when, due, err)
		}
	}
}
