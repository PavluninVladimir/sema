package engine

// Числовые приметы: номера документов и счетов узнаются контрольной
// суммой, а не соседним словом. Слово «ИНН» рядом с номером стоит не
// всегда, а голые двенадцать цифр примета по словам не ловит вовсе —
// именно так номер налогоплательщика уехал облачной модели 8 сентября.
//
// Контрольная сумма даёт точность почти без ложных срабатываний: двенадцать
// цифр, прошедшие проверку ИНН, — это ИНН, а не артикул. Поэтому такие
// детекторы можно включать без оглядки на шум, в отличие от расширения
// словаря подстрок.

import (
	"regexp"
	"strings"
)

// reDigits — подряд идущие цифры, между которыми человек мог поставить
// пробел или дефис: «7710 1401 5» и «771014015» — один и тот же номер.
var reDigits = regexp.MustCompile(`\d[\d \-]{7,30}\d`)

// Finding — что нашли в тексте и чем это опознано.
type Finding struct {
	Kind   string // инн | снилс | карта
	Reason string // для журнала и объяснения человеку
}

// nearbyWords — подсказки, которые должны стоять рядом с номером, чтобы
// считать его документом. Контрольная сумма — не доказательство: случайное
// десятизначное число проходит проверку ИНН примерно в одном случае из
// одиннадцати, а шестнадцатизначное — Луна в одном из десяти. В выводе
// серверов длинных чисел много, и без подсказки рядом детектор начнёт
// видеть паспорта в размерах файлов.
var nearbyWords = map[string][]string{
	"инн":   {"инн", "налог", "продавец", "поставщик", "огрн", "кпп", "чек", "счёт", "счет", "реквизит"},
	"снилс": {"снилс", "пенсион", "страхов"},
	"карта": {"карт", "card", "оплат", "платеж", "платёж", "visa", "mastercard", "мир ", "счёт", "счет"},
}

// hintNearby — есть ли рядом с находкой слово, которое подтверждает,
// что это номер документа, а не просто длинное число. Окно — 120 знаков
// в обе стороны: так делают взрослые системы (proximity window).
func hintNearby(text string, kind string, from, to int) bool {
	start := from - 120
	if start < 0 {
		start = 0
	}
	end := to + 120
	if end > len(text) {
		end = len(text)
	}
	window := strings.ToLower(text[start:end])
	for _, w := range nearbyWords[kind] {
		if strings.Contains(window, w) {
			return true
		}
	}
	return false
}

// DocumentNumbers — какие номера с контрольной суммой есть в тексте.
// Пустой список значит «не нашли», а не «их нет»: примета не всесильна.
func DocumentNumbers(text string) []Finding {
	var out []Finding
	seen := map[string]bool{}
	add := func(kind, why string, from, to int) {
		if seen[kind] {
			return
		}
		// двенадцатизначный ИНН человека стоит сам за себя: две
		// контрольные цифры дают одну случайность на сто двадцать один.
		// Всем остальным нужна подсказка рядом
		if kind != "инн12" && !hintNearby(text, kind, from, to) {
			return
		}
		seen[kind] = true
		if kind == "инн12" {
			seen["инн"] = true
		}
		out = append(out, Finding{Kind: strings.TrimSuffix(kind, "12"), Reason: why})
	}
	for _, m := range reDigits.FindAllStringIndex(text, 200) {
		chunk := text[m[0]:m[1]]
		c := onlyDigits(chunk)
		switch {
		case len(c) == 12 && inn(c):
			add("инн12", "двенадцатизначный номер прошёл контрольную сумму ИНН", m[0], m[1])
		case len(c) == 10 && inn(c):
			add("инн", "номер прошёл контрольную сумму ИНН и рядом стоит подсказка", m[0], m[1])
		case len(c) == 11 && snils(c):
			add("снилс", "номер прошёл контрольную сумму СНИЛС и рядом стоит подсказка", m[0], m[1])
		case len(c) >= 13 && len(c) <= 19 && luhn(c):
			add("карта", "номер прошёл проверку Луна и рядом стоит подсказка", m[0], m[1])
		}
	}
	return out
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// inn — контрольная сумма ИНН: десять знаков у организации, двенадцать у
// человека. Веса заданы порядком расчёта из приказа ФНС.
func inn(c string) bool {
	digit := func(i int) int { return int(c[i] - '0') }
	amount := func(weights []int) int {
		s := 0
		for i, v := range weights {
			s += v * digit(i)
		}
		return s % 11 % 10
	}
	switch len(c) {
	case 10:
		return amount([]int{2, 4, 10, 3, 5, 9, 4, 6, 8}) == digit(9)
	case 12:
		return amount([]int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8}) == digit(10) &&
			amount([]int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8}) == digit(11)
	}
	return false
}

// snils — одиннадцать знаков: девять номера и два контрольных.
func snils(c string) bool {
	if len(c) != 11 {
		return false
	}
	amount := 0
	for i := 0; i < 9; i++ {
		amount += int(c[i]-'0') * (9 - i)
	}
	control := 0
	switch {
	case amount < 100:
		control = amount
	case amount == 100 || amount == 101:
		control = 0
	default:
		control = amount % 101
		if control == 100 {
			control = 0
		}
	}
	has := int(c[9]-'0')*10 + int(c[10]-'0')
	return control == has
}

// luhn — проверка Луна: ею закрыты номера банковских карт.
func luhn(c string) bool {
	amount, second := 0, false
	for i := len(c) - 1; i >= 0; i-- {
		d := int(c[i] - '0')
		if second {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		amount += d
		second = !second
	}
	return amount%10 == 0
}

// секретныеСлова и чувствительныеСлова — приметы по словам. Отличие от
// нынешнего sentClass одно, но решающее: символа «@» здесь нет. Он стоит
// в адресе каждого письма, поэтому с ним «чувствительными» становятся все
// письма без исключения — класс, в который попадает всё, не различает.
var (
	secretWords = []string{
		"паспорт", "снилс", "инн ", "инн:", "карта ", "cvv", "iban", "договор №", "медицин", "диагноз",
		"водительск", "удостоверение", "свидетельство о рожд", "свидетельство о брак",
		"страховой полис", "полис омс", "полис дмс", "серия и номер",
	}
	sensitiveWords = []string{"счёт", "оплат", "сумма", "личн", "телефон", "адрес"}
)

// TextClass — класс содержимого и причина, по которой он такой.
// Причина нужна не для красоты: сегодня хранится только итог, и на вопрос
// «почему это секретное» ответить нечем — приходится пересчитывать по коду.
func TextClass(text string) (class, reason string) {
	if reMRZ.MatchString(text) {
		return "секретное", "машиночитаемая строка документа"
	}
	if n := DocumentNumbers(text); len(n) > 0 {
		return "секретное", n[0].Reason
	}
	low := strings.ToLower(text)
	for _, w := range secretWords {
		if strings.Contains(low, w) {
			return "секретное", "слово «" + strings.TrimSpace(w) + "»"
		}
	}
	for _, w := range sensitiveWords {
		if strings.Contains(low, w) {
			return "чувствительное", "слово «" + w + "»"
		}
	}
	return "обычное", ""
}
