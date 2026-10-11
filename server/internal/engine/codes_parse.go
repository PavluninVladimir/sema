package engine

// Разбор значения кода в поля. Известное разбираем, неизвестное оставляем
// значением: выдуманный разбор хуже честного «просто текст» — по нему
// потом считают деньги.

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// parseValue — формат и поля кода; здесь же маски секретов, которые
// человек завёл в памяти: адрес с чужим токеном не должен уехать модели
// только потому, что он приехал картинкой, а не текстом.
func (e *Engine) parseValue(k store.FileCode) store.FileCode {
	format, fields := parseCode(k.Symbology, k.Value)
	// маска памяти сильнее разбора, но только там, где значение —
	// свободный текст: в GTIN и реквизитах чека маске взяться неоткуда,
	// а совпади она там случайно — мы потеряли бы точное число
	if (format == store.CodeURL || format == store.CodeText) && e.secretMask(k.Value) != "" {
		format, fields = store.CodeSecretURL, fieldAddrWithSecret(k.Value)
	}
	k.Format, k.Fields = format, fields
	if format == store.CodeSecretURL {
		k.Value = withoutSecret(k.Value)
	}
	return k
}

// parseCode — чистый разбор без движка: по нему идёт табличный тест.
func parseCode(kind, value string) (string, []store.CodeField) {
	value = strings.TrimSpace(value)
	if value == "" {
		return store.CodeText, nil
	}
	if fields, ok := parseReceiptFNS(value); ok {
		return store.CodeFNS, fields
	}
	if fields, ok := parseBrand(value); ok {
		return store.CodeMark, fields
	}
	if fields, ok := parseItem(kind, value); ok {
		return store.CodeGTIN, fields
	}
	if addr, ok := parseAddr(value); ok {
		if secretInAddr(addr) {
			return store.CodeSecretURL, fieldAddrWithSecret(value)
		}
		return store.CodeURL, []store.CodeField{{Label: "Адрес", Value: value}}
	}
	return store.CodeText, nil
}

// ---- чек ФНС ----

// calcKinds — что значит `n` в QR чека. Словарь короткий и закреплён
// приказом ФНС, поэтому он здесь, а не в справочнике базы.
var calcKinds = map[string]string{
	"1": "приход",
	"2": "возврат прихода",
	"3": "расход",
	"4": "возврат расхода",
}

// FNSReceipt — реквизиты чека так, как их знает налоговая.
type FNSReceipt struct {
	When   timeParts
	Amount float64
	FN     string // номер фискального накопителя
	FD     string // номер фискального документа
	FP     string // фискальный признак
	Kind   string // приход, возврат прихода…
}

// ReceiptDetails — строка `t=20260406T1847&s=55830.00&fn=…&i=…&fp=…&n=1`.
// Требуем все пять реквизитов: без них это не чек, а похожая строка, и
// принять её значило бы завести чек на пустом месте.
func ReceiptDetails(value string) (*FNSReceipt, bool) {
	if !strings.Contains(value, "t=") || !strings.Contains(value, "fn=") {
		return nil, false
	}
	q, err := url.ParseQuery(value)
	if err != nil {
		return nil, false
	}
	t, s, fn := q.Get("t"), q.Get("s"), q.Get("fn")
	i, fp := q.Get("i"), q.Get("fp")
	if t == "" || s == "" || fn == "" || i == "" || fp == "" {
		return nil, false
	}
	when, ok := receiptTime(t)
	if !ok {
		return nil, false
	}
	amount, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil || amount <= 0 {
		return nil, false
	}
	return &FNSReceipt{When: when, Amount: amount, FN: fn, FD: i, FP: fp, Kind: calcKinds[q.Get("n")]}, true
}

func parseReceiptFNS(value string) ([]store.CodeField, bool) {
	receipt, ok := ReceiptDetails(value)
	if !ok {
		return nil, false
	}
	fields := []store.CodeField{
		{Label: "Дата", Value: receipt.When.Text()},
		{Label: "Сумма", Value: fmt.Sprintf("%.2f", receipt.Amount)},
		{Label: "ФН", Value: receipt.FN},
		{Label: "ФД", Value: receipt.FD},
		{Label: "ФП", Value: receipt.FP},
	}
	if receipt.Kind != "" {
		fields = append(fields, store.CodeField{Label: "Вид расчёта", Value: receipt.Kind})
	}
	return fields, true
}

// receiptTime — `20260406T1847` или `20260406T184700`.
func receiptTime(t string) (timeParts, bool) {
	digits := strings.ReplaceAll(strings.ToUpper(t), "T", "")
	if len(digits) != 12 && len(digits) != 14 {
		return timeParts{}, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return timeParts{}, false
		}
	}
	year, _ := strconv.Atoi(digits[0:4])
	month, _ := strconv.Atoi(digits[4:6])
	day, _ := strconv.Atoi(digits[6:8])
	hour, _ := strconv.Atoi(digits[8:10])
	mins, _ := strconv.Atoi(digits[10:12])
	if year < 2000 || month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || mins > 59 {
		return timeParts{}, false
	}
	return timeParts{Year: year, Month: month, Day: day, Hour: hour, Min: mins}, true
}

// timeParts — дата и время чека без часового пояса: касса пишет своё
// местное время, и приводить его к какому-нибудь поясу было бы домыслом.
// Поэтому это не time.Time: у той всегда есть пояс, и однажды кто-нибудь
// позвал бы .Local() и сдвинул чек на три часа.
type timeParts struct {
	Year, Month, Day, Hour, Min int
}

// Text — как показываем человеку.
func (v timeParts) Text() string {
	return fmt.Sprintf("%02d.%02d.%04d %02d:%02d", v.Day, v.Month, v.Year, v.Hour, v.Min)
}

// ISO — дата документа для базы: там даты хранятся строками.
func (v timeParts) ISO() string {
	return fmt.Sprintf("%04d-%02d-%02d", v.Year, v.Month, v.Day)
}

// ---- марка «Честного знака» ----

// fixedAI — идентификаторы применения GS1 с известной длиной
// данных: после них разделителя нет и быть не должно.
var fixedAI = map[string]int{
	"00": 18, "01": 14, "02": 14,
	"11": 6, "12": 6, "13": 6, "15": 6, "16": 6, "17": 6,
	"20": 2,
}

// signatureAI — как называется поле человеку. Чего здесь нет, то остаётся в
// сыром значении: категории товаров кладут в марку разные хвосты, и
// угадывать их назначение — не наше дело.
var signatureAI = map[string]string{
	"01": "GTIN",
	"21": "Серийный номер",
	"17": "Годен до",
	"10": "Партия",
	"91": "Ключ проверки",
	"92": "Код проверки",
	"93": "Код проверки",
}

const gs = "\x1d" // FNC1: gozxing отдаёт разделитель групп именно так

// parseBrand — GS1 DataMatrix: признаём по идентификаторам применения,
// а не по длине. Марка обязана нести 01 (GTIN) и 21 (серийный номер) —
// иначе это какой-то другой GS1, и звать его маркой было бы неправдой.
func parseBrand(value string) ([]store.CodeField, bool) {
	pairs := parseGS1(value)
	if pairs == nil {
		return nil, false
	}
	gtin, series := "", ""
	var fields []store.CodeField
	for _, p := range pairs {
		switch p.AI {
		case "01":
			gtin = p.Value
		case "21":
			series = p.Value
		}
		signature := signatureAI[p.AI]
		if signature == "" {
			continue // неизвестный хвост живёт в сыром значении
		}
		fields = append(fields, store.CodeField{Label: signature, Value: p.Value})
	}
	if gtin == "" || series == "" {
		return nil, false
	}
	if flag := weightedItem(gtin); flag != "" {
		fields = append(fields, store.CodeField{Label: "Признак", Value: flag})
	}
	return fields, true
}

type pairGS1 struct {
	AI    string
	Value string
}

// parseGS1 — строка из пар «идентификатор + данные». Данные
// переменной длины кончаются разделителем FNC1 или концом строки.
func parseGS1(value string) []pairGS1 {
	s := strings.TrimPrefix(value, gs)
	var out []pairGS1
	for len(s) >= 2 {
		ai := s[:2]
		if !allDigits(ai) {
			return nil
		}
		s = s[2:]
		if n, ok := fixedAI[ai]; ok {
			if len(s) < n {
				return nil
			}
			out = append(out, pairGS1{AI: ai, Value: s[:n]})
			s = strings.TrimPrefix(s[n:], gs)
			continue
		}
		data := s
		if i := strings.Index(s, gs); i >= 0 {
			data, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		if data == "" {
			return nil
		}
		out = append(out, pairGS1{AI: ai, Value: data})
	}
	if s != "" || len(out) == 0 {
		return nil
	}
	return out
}

// ---- штрих-код товара ----

// parseItem — EAN-13/8, UPC. Контрольная сумма обязательна: полосатый
// текст иногда читается как код, и без проверки в чеках завелись бы
// товары, которых никто не покупал.
func parseItem(kind, value string) ([]store.CodeField, bool) {
	switch kind {
	case "ean13", "ean8", "upca", "upce", "ean":
	default:
		return nil, false
	}
	if !allDigits(value) || (len(value) != 8 && len(value) != 12 && len(value) != 13) {
		return nil, false
	}
	if !checkSumMatched(value) {
		return nil, false
	}
	fields := []store.CodeField{{Label: "GTIN", Value: value}}
	if flag := weightedItem(value); flag != "" {
		fields = append(fields, store.CodeField{Label: "Признак", Value: flag})
	}
	return fields, true
}

// weightedItem — префикс 2 у EAN-13 означает внутренний код магазина
// (взвешенный товар): такой номер ничего не говорит о товаре вне этой
// сети, и человек должен видеть, что искать его в справочниках незачем.
func weightedItem(gtin string) string {
	if len(gtin) == 13 && strings.HasPrefix(gtin, "2") {
		return "внутренний код магазина (весовой товар)"
	}
	if len(gtin) == 14 && strings.HasPrefix(gtin, "02") {
		return "внутренний код магазина (весовой товар)"
	}
	return ""
}

// checkSumMatched — контрольная цифра GTIN (EAN-8/12/13/14).
func checkSumMatched(digits string) bool {
	n := len(digits)
	amount := 0
	for i := 0; i < n-1; i++ {
		d := int(digits[i] - '0')
		// вес чередуется от конца: правый разряд перед контрольным — 3
		if (n-2-i)%2 == 0 {
			amount += d * 3
		} else {
			amount += d
		}
	}
	control := (10 - amount%10) % 10
	return control == int(digits[n-1]-'0')
}

// ---- адрес ----

func parseAddr(value string) (*url.URL, bool) {
	low := strings.ToLower(value)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return nil, false
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return nil, false
	}
	return u, true
}

// secretParams — по каким именам параметр считается секретом.
var secretParams = []string{"token", "secret", "password", "passwd", "pass", "key",
	"apikey", "api_key", "access_token", "auth", "sign", "signature", "sig", "code", "otp"}

// secretInAddr — есть ли в ссылке то, чем можно воспользоваться вместо
// человека. Судим по имени параметра: имя мы видим, а содержимое чужого
// токена от обычного числа не отличить.
func secretInAddr(u *url.URL) bool {
	q := u.Query()
	for name := range q {
		bottom := strings.ToLower(name)
		for _, s := range secretParams {
			if bottom == s || strings.HasSuffix(bottom, "_"+s) || strings.HasSuffix(bottom, "-"+s) {
				return true
			}
		}
	}
	return false
}

// fieldAddrWithSecret — что показываем вместо адреса.
func fieldAddrWithSecret(value string) []store.CodeField {
	if u, err := url.Parse(value); err == nil && u.Host != "" {
		return []store.CodeField{
			{Label: "Адрес", Value: "адрес с секретом"},
			{Label: "Куда ведёт", Value: u.Host},
		}
	}
	// не адрес, а просто текст, в котором сработала маска памяти
	return []store.CodeField{{Label: "Значение", Value: "в коде секрет — не показываю"}}
}

// withoutSecret — адрес без строки параметров: хранить её негде, а показать
// один раз значит показать навсегда.
func withoutSecret(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return "в коде секрет"
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String() + "?…"
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
