package engine

import "testing"

// Контрольные суммы проверяются на числах, посчитанных здесь же: брать
// настоящие чужие номера в тест нельзя, а выдуманные без контрольной
// суммы ничего не проверяют.
func buildINN12(base string) string {
	digit := func(s string, i int) int { return int(s[i] - '0') }
	amount := func(s string, weights []int) int {
		x := 0
		for i, v := range weights {
			x += v * digit(s, i)
		}
		return x % 11 % 10
	}
	check11 := amount(base, []int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8})
	base11 := base + string(rune('0'+check11))
	check12 := amount(base11, []int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8})
	return base11 + string(rune('0'+check12))
}

func buildSNILS(base string) string {
	amount := 0
	for i := 0; i < 9; i++ {
		amount += int(base[i]-'0') * (9 - i)
	}
	k := 0
	switch {
	case amount < 100:
		k = amount
	case amount == 100 || amount == 101:
		k = 0
	default:
		k = amount % 101
		if k == 100 {
			k = 0
		}
	}
	return base + string(rune('0'+k/10)) + string(rune('0'+k%10))
}

func TestNumberDetectedCheckSum(t *testing.T) {
	number := buildINN12("1651066469")
	if len(number) != 12 || !inn(number) {
		t.Fatalf("собранный ИНН не проходит свою же проверку: %s", number)
	}
	// тот самый случай: номер в тексте разговора, слова «ИНН» рядом нет
	text := "продавец Хакимова, номер " + number + ", дата 06.04.26"
	n := DocumentNumbers(text)
	if len(n) == 0 || n[0].Kind != "инн" {
		t.Fatalf("номер без слова-подсказки не найден: %+v", n)
	}
	// он же с пробелами, как пишут люди
	withSpaces := number[:4] + " " + number[4:8] + " " + number[8:]
	if len(DocumentNumbers("счёт "+withSpaces)) == 0 {
		t.Error("номер с пробелами не найден")
	}
	// испорченная контрольная сумма — не номер
	corruption := number[:11] + string(rune('0'+(int(number[11]-'0')+1)%10))
	if len(DocumentNumbers("код "+corruption)) != 0 {
		t.Error("число с неверной контрольной суммой принято за ИНН")
	}
}

func TestSNILSAndCard(t *testing.T) {
	s := buildSNILS("112233445")
	if !snils(s) {
		t.Fatalf("собранный СНИЛС не проходит проверку: %s", s)
	}
	if len(DocumentNumbers("страховой "+s)) == 0 {
		t.Error("СНИЛС не найден")
	}
	// карта: контрольная цифра по Луну
	if !luhn("4111111111111111") {
		t.Error("известный тестовый номер карты не прошёл Луна")
	}
	if luhn("4111111111111112") {
		t.Error("испорченный номер карты принят")
	}
}

// Обычный текст не должен порождать находок: длинные числа встречаются
// в счетах, артикулах и датах, и ложная тревога здесь дороже пропуска.
func TestPlainTextWithoutFindings(t *testing.T) {
	cases := []string{
		"заказ 1234567890123 доставлен 09.09.2026",
		"телефон +7 903 555 77 77, звоните",
		"сумма 12 345 678 рублей по договору 2026-09-08",
	}
	for _, s := range cases {
		if n := DocumentNumbers(s); len(n) > 0 {
			t.Errorf("ложная находка в %q: %+v", s, n)
		}
	}
}

// Контрольная сумма — не доказательство: случайное десятизначное число
// проходит проверку ИНН примерно в одном случае из одиннадцати. В выводе
// серверов длинных чисел много, и без подсказки рядом детектор начинает
// видеть документы в размерах файлов и номерах процессов.
func TestLongNumberWithoutHintNotDocument(t *testing.T) {
	ten := ""
	for i := 0; i < 100000 && ten == ""; i++ {
		candidate := buildINN10(i)
		if inn(candidate) {
			ten = candidate
		}
	}
	if ten == "" {
		t.Skip("не собрал десятизначный ИНН")
	}
	output := "df: /dev/root 15230984 " + ten + " 8123456 66% /"
	if n := DocumentNumbers(output); len(n) > 0 {
		t.Errorf("в выводе df нашёлся документ: %+v", n)
	}
	// то же число рядом со словом-подсказкой — уже документ
	if n := DocumentNumbers("ИНН поставщика " + ten); len(n) == 0 {
		t.Error("номер рядом с подсказкой не найден")
	}
	// двенадцатизначный ИНН человека стоит сам за себя: две контрольные
	// цифры дают одну случайность на сто двадцать один
	twelve := buildINN12("1651066469")
	if n := DocumentNumbers("продавец, номер " + twelve); len(n) == 0 {
		t.Error("двенадцатизначный ИНН должен находиться и без подсказки")
	}
}

func buildINN10(seed int) string {
	base := ""
	x := 1000000 + seed
	for i := 0; i < 9; i++ {
		base += string(rune('0' + (x/pow10(i))%10))
	}
	amount := 0
	for i, v := range []int{2, 4, 10, 3, 5, 9, 4, 6, 8} {
		amount += v * int(base[i]-'0')
	}
	return base + string(rune('0'+amount%11%10))
}

func pow10(n int) int {
	r := 1
	for i := 0; i < n; i++ {
		r *= 10
	}
	return r
}
