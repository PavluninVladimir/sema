package engine

// Часы пик у провайдера. У DeepSeek дневной тариф вдвое дороже ночного, и
// без учёта этого счёт врёт вдвое. Расписание держим настройкой, а не в
// коде: провайдер меняет его когда захочет, а зашитые часы начнут врать
// молча — и это худший вид ошибки в деньгах.
//
// Строка расписания: «пн-пт 01:00-04:00,06:00-10:00 UTC».
// Без пояса считаем UTC: провайдеры называют часы именно так.

import (
	"strconv"
	"strings"
	"time"
)

type peakWindow struct {
	days           [7]bool // 0 — воскресенье, как в time.Weekday
	fromMin, toMin int     // минуты от полуночи
}

var shortDays = map[string]time.Weekday{
	"вс": time.Sunday, "пн": time.Monday, "вт": time.Tuesday, "ср": time.Wednesday,
	"чт": time.Thursday, "пт": time.Friday, "сб": time.Saturday,
}

// parsePeak — расписание в окна. Не разобралось — пусто: лучше считать
// всё по обычной цене, чем по выдуманному расписанию.
func parsePeak(s string) []peakWindow {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return nil
	}
	inUTC := strings.Contains(s, "utc")
	_ = inUTC // пояс всегда UTC; строка нужна только человеку
	s = strings.ReplaceAll(s, "utc", "")
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return nil
	}
	days, ok := parseDays(fields[0])
	if !ok {
		return nil
	}
	var out []peakWindow
	for _, chunk := range strings.Split(strings.Join(fields[1:], ""), ",") {
		from, to, ok := parseHours(chunk)
		if !ok {
			continue
		}
		out = append(out, peakWindow{days: days, fromMin: from, toMin: to})
	}
	return out
}

func parseDays(s string) ([7]bool, bool) {
	var days [7]bool
	cases := strings.Split(s, ",")
	has := false
	for _, chunk := range cases {
		if from, to, ok := strings.Cut(chunk, "-"); ok {
			a, ok1 := shortDays[strings.TrimSpace(from)]
			b, ok2 := shortDays[strings.TrimSpace(to)]
			if !ok1 || !ok2 {
				continue
			}
			for d := a; ; d = (d + 1) % 7 {
				days[d], has = true, true
				if d == b {
					break
				}
			}
			continue
		}
		if d, ok := shortDays[strings.TrimSpace(chunk)]; ok {
			days[d], has = true, true
		}
	}
	return days, has
}

func parseHours(s string) (from, to int, ok bool) {
	a, b, has := strings.Cut(strings.TrimSpace(s), "-")
	if !has {
		return 0, 0, false
	}
	from, ok1 := minutes(a)
	to, ok2 := minutes(b)
	return from, to, ok1 && ok2
}

func minutes(s string) (int, bool) {
	s = strings.TrimSpace(s)
	h, m, has := strings.Cut(s, ":")
	hours, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || hours < 0 || hours > 24 {
		return 0, false
	}
	mins := 0
	if has {
		mins, err = strconv.Atoi(strings.TrimSpace(m))
		if err != nil || mins < 0 || mins > 59 {
			return 0, false
		}
	}
	return hours*60 + mins, true
}

// inPeak — попадает ли момент в расписание. Время берётся в UTC: провайдеры
// называют часы пика именно так, и переводить их в местный пояс значит
// ошибиться на смене летнего времени.
func inPeak(schedule string, when time.Time) bool {
	window := parsePeak(schedule)
	if len(window) == 0 {
		return false
	}
	u := when.UTC()
	mins := u.Hour()*60 + u.Minute()
	for _, o := range window {
		if !o.days[int(u.Weekday())] {
			continue
		}
		// окно через полночь («22:00-02:00») считается как два куска
		if o.fromMin <= o.toMin {
			if mins >= o.fromMin && mins < o.toMin {
				return true
			}
			continue
		}
		if mins >= o.fromMin || mins < o.toMin {
			return true
		}
	}
	return false
}
