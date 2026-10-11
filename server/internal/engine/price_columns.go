package engine

// Столбцы таблицы цен: по шапке узнаём, какое число за что.
//
// Число, что стоит в строке рядом с именем, ещё не цена выхода: у OpenAI и
// Anthropic между «Cached input» и «Output» лежит столбец «Cache writes»
// («5m writes», «1h writes»), и модель, читая строку без шапки, берёт запись
// в кэш за выход. Порядок «кэш ≤ вход ≤ выход» такую ошибку не ловит: запись
// в кэш дороже входа в 1.25 раза и дешевле выхода. Поэтому код читает шапку
// сам: столбец получает роль по подписи, и число ответа обязано стоять
// именно в столбце своей роли. Шапки нет, а чисел в строке больше трёх —
// строка не подтверждена: угадывать нельзя.

import (
	"regexp"
	"strings"
)

// colRole — что лежит в столбце.
type colRole int

const (
	roleOther      colRole = iota // имя, категория, «Use case», «Training»
	roleInput                     // вход
	roleCached                    // вход из кэша (чтение)
	roleCacheWrite                // запись в кэш: ни вход, ни выход
	roleOutput                    // выход
	roleCost                      // оценка «за минуту звука»
)

func (r colRole) String() string {
	switch r {
	case roleInput:
		return "вход"
	case roleCached:
		return "вход из кэша"
	case roleCacheWrite:
		return "запись в кэш"
	case roleOutput:
		return "выход"
	case roleCost:
		return "цена за минуту"
	}
	return "прочее"
}

var reWrites = regexp.MustCompile(`\bwrites?\b|запис`)

// cellRole — роль столбца по подписи в шапке. Порядок проверок важен:
// «Cache writes» содержит и «cache», а «Output / cost» — и «cost».
func cellRole(cell string) colRole {
	s := strings.ToLower(strings.TrimSpace(cell))
	switch {
	case s == "":
		return roleOther
	case reWrites.MatchString(s):
		return roleCacheWrite
	case strings.Contains(s, "output") || strings.Contains(s, "completion"):
		return roleOutput
	case strings.Contains(s, "miss"):
		return roleInput // «cache miss» — вход, которого в кэше не нашлось
	case strings.Contains(s, "cach") || strings.Contains(s, "hit") ||
		strings.Contains(s, "read") || strings.Contains(s, "refresh") || strings.Contains(s, "кэш"):
		return roleCached
	case strings.Contains(s, "input") || strings.Contains(s, "prompt"):
		return roleInput
	case strings.Contains(s, "cost") || strings.Contains(s, "per minute") || strings.Contains(s, "/ min"):
		return roleCost
	}
	return roleOther
}

func headerRoles(cells []string) []colRole {
	out := make([]colRole, len(cells))
	for i, c := range cells {
		out[i] = cellRole(c)
	}
	return out
}

// isHeaderCells — строка похожа на шапку таблицы цен: два и больше столбцов
// с ролью, среди них вход или выход. Строка данных так не выглядит: в ней
// числа, а не подписи.
func isHeaderCells(cells []string) bool {
	if len(cells) < 3 {
		return false
	}
	// подписи строк у Google («Input price · Not available · $2.00») — данные:
	// в шапке денег нет
	for _, c := range cells {
		if strings.ContainsAny(c, "$€₽") {
			return false
		}
	}
	roles, seen, key := headerRoles(cells), 0, false
	for i, r := range roles {
		if r == roleOther || hasDigitsOnly(cells[i]) {
			continue
		}
		seen++
		key = key || r == roleInput || r == roleOutput
	}
	return seen >= 2 && key
}

func hasDigitsOnly(s string) bool {
	s = strings.TrimSpace(strings.TrimLeft(s, "$€₽"))
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != ',' {
			return false
		}
	}
	return true
}

// firstGroup — шапка до первого повтора роли: «Input, Cached, Cache writes,
// Output, Input, …» (короткий и длинный контекст) даёт первую четвёрку.
// Короткий контекст идёт первым, а строки, что лежат в свойствах острова, —
// только он.
func firstGroup(cells []string) []string {
	seen := map[colRole]bool{}
	for i, c := range cells {
		r := cellRole(c)
		if r == roleOther {
			continue
		}
		if seen[r] {
			return cells[:i]
		}
		seen[r] = true
	}
	return cells
}

// columnMap — где в строке какая роль: индекс ячейки первого столбца этой
// роли. Строка короче шапки ровно на одну ячейку, когда в шапке есть запись
// в кэш, — это строка модели без такого столбца (у gpt-5.4-mini три числа:
// вход, кэш, выход): выбрасываем запись в кэш из шапки и сверяем остальное.
// Любое другое расхождение — nil: не сходится, не угадываем.
func columnMap(header []string, rowLen int) map[colRole]int {
	roles := headerRoles(header)
	switch {
	case len(roles) == rowLen:
	case len(roles) == rowLen+1:
		w := -1
		for i, r := range roles {
			if r == roleCacheWrite {
				w = i
				break
			}
		}
		if w < 0 {
			return nil
		}
		roles = append(append([]colRole{}, roles[:w]...), roles[w+1:]...)
	default:
		return nil
	}
	m := map[colRole]int{}
	for i, r := range roles {
		if r == roleOther {
			continue
		}
		if _, dup := m[r]; !dup {
			m[r] = i
		}
	}
	return m
}

// cellNum — число в ячейке: «$0.50», «0.5», «$0.003 / minute». «-» и «Free» —
// числа нет.
func cellNum(cell string) (float64, bool) {
	n := numbersIn(cell)
	if len(n) == 0 {
		return 0, false
	}
	return n[0], true
}
