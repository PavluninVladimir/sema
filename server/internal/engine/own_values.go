package engine

// Свои значения: ИНН, СНИЛС, номера документов, счета, телефоны владельца.
//
// Приметы по словам ловят «ИНН 7710140679», но голые двенадцать цифр в
// реплике не ловит никто: ни слова-подсказки рядом, ни сущности с классом
// в контексте. Ровно так номер уехал облачной модели 8 сентября. Список
// своих значений закрывает этот случай точным совпадением — полнота у него
// почти единица, а ложных срабатываний почти нет: чужой номер сюда не
// попадает по построению.
//
// Сами номера не хранятся нигде. В хранилище секретов лежат солёные хэши:
// список личных номеров — сам по себе секрет, а без соли его можно было бы
// перебрать словарём всех ИНН за вечер. Соль — отдельный секрет, заводится
// один раз и не меняется: сменить её значит потерять весь список.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	saltSecret      = "класс:соль"
	ownValuesSecret = "класс:свои-значения"
	// minDigits — короче этого значение не берём: шестизначное число
	// встречается в любом счёте и в номере дома, и список своих значений
	// из таких чисел начал бы закрывать наружу всё подряд
	minDigits = 6
)

// OwnValue — одна строка списка. Ни самого номера, ни его хвоста здесь
// нет: экран показывает только вид и дату, потому что экран видно с плеча.
type OwnValue struct {
	Hash string // первые знаки солёного хэша: по ним строку удаляют
	Kind string // ИНН, СНИЛС, карта, телефон, номер
	At   string
}

// salt — общая соль списка; заводится при первом добавлении.
func (e *Engine) salt() (string, error) {
	if v, ok := e.store.SecretValue(saltSecret); ok && v != "" {
		return v, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	v := base64.RawStdEncoding.EncodeToString(b)
	if err := e.store.SaveSecret(&store.Secret{
		Name: saltSecret, Kind: store.SecretOther,
		Note: "соль для списка своих значений; сменить её — потерять список",
	}, v); err != nil {
		return "", err
	}
	return v, nil
}

// valueHash — солёный отпечаток нормализованного значения.
func valueHash(salt, norm string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + norm))
	return hex.EncodeToString(sum[:])[:32]
}

// onlyChars — нормализация: пробелы, дефисы и скобки между цифрами
// человек ставит как придётся, а «7710 1401 5» и «771014015» — один номер.
func onlyChars(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'а' && r <= 'я') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// valueKind — как назвать номер человеку. Спрашивать вид отдельным полем
// незачем: контрольная сумма отвечает на этот вопрос точнее, чем человек,
// который добавляет номер в спешке.
func valueKind(c string) string {
	switch {
	case len(c) == 10 && inn(c), len(c) == 12 && inn(c):
		return "ИНН"
	case len(c) == 11 && snils(c):
		return "СНИЛС"
	case len(c) >= 13 && len(c) <= 19 && luhn(c):
		return "карта"
	case len(c) == 11 && (c[0] == '7' || c[0] == '8'), len(c) == 10:
		return "телефон"
	case len(c) == 20:
		return "счёт"
	}
	return "номер"
}

// OwnValues — список для экрана: вид и дата, без самих номеров.
func (e *Engine) OwnValues() []OwnValue {
	v, ok := e.store.SecretValue(ownValuesSecret)
	if !ok || v == "" {
		return nil
	}
	var out []OwnValue
	for _, line := range strings.Split(v, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 || parts[0] == "" {
			continue
		}
		out = append(out, OwnValue{Hash: parts[0], Kind: parts[1], At: parts[2]})
	}
	return out
}

// AddOwnValue — положить номер в список. Возвращает, чем его
// опознали: человек должен видеть, что Сёма понял его правильно.
func (e *Engine) AddOwnValue(value string) (string, error) {
	norm := onlyChars(value)
	c := onlyDigits(norm)
	if len(c) < minDigits || len(c) != len(norm) {
		return "", fmt.Errorf("своё значение — это номер: ИНН, СНИЛС, карта, счёт, телефон, "+
			"не короче %d цифр и без букв", minDigits)
	}
	salt, err := e.salt()
	if err != nil {
		return "", err
	}
	hash := valueHash(salt, norm)
	kind := valueKind(c)
	has := e.OwnValues()
	for _, z := range has {
		if z.Hash == hash {
			return z.Kind, nil // уже в списке: повтор не ошибка
		}
	}
	has = append(has, OwnValue{Hash: hash, Kind: kind, At: time.Now().Format(time.RFC3339)})
	return kind, e.saveOwn(has)
}

// RemoveOwnValue — удалить строку по отпечатку.
func (e *Engine) RemoveOwnValue(hash string) error {
	var keep []OwnValue
	for _, z := range e.OwnValues() {
		if z.Hash != hash {
			keep = append(keep, z)
		}
	}
	return e.saveOwn(keep)
}

func (e *Engine) saveOwn(list []OwnValue) error {
	var line []string
	for _, z := range list {
		line = append(line, z.Hash+"\t"+z.Kind+"\t"+z.At)
	}
	text := strings.Join(line, "\n")
	if text == "" {
		// пустой секрет хранилище не принимает, да и хранить нечего
		return e.store.DeleteSecret(ownValuesSecret)
	}
	return e.store.SaveSecret(&store.Secret{
		Name: ownValuesSecret, Kind: store.SecretOther,
		Note: "солёные хэши личных номеров; самих номеров здесь нет",
	}, text)
}

// hasOwnValue — стоит ли в тексте номер из списка. Второе значение —
// вид, чтобы причина в журнале была человеческой.
//
// Перебирать все подстроки нельзя, поэтому смотрим только на цепочки цифр
// (между ними человек ставит пробелы и дефисы) и сверяем каждую цепочку
// целиком, а также её начало и конец нужной длины: телефон пишут и как
// «+7 916 …», и как «916 …», и в тексте это одна цепочка из одиннадцати
// цифр против десяти в списке.
func (e *Engine) hasOwnValue(text string) (string, bool) {
	list := e.OwnValues()
	if len(list) == 0 || text == "" {
		return "", false
	}
	salt, ok := e.store.SecretValue(saltSecret)
	if !ok || salt == "" {
		return "", false
	}
	kinds := map[string]string{}
	for _, z := range list {
		kinds[z.Hash] = z.Kind
	}
	for _, m := range reChainDigits.FindAllStringIndex(text, 500) {
		c := onlyDigits(text[m[0]:m[1]])
		// длины номеров в списке нам неизвестны — там одни хэши. Поэтому
		// перебираем все осмысленные: их полтора десятка, а не миллион
		for n := minDigits; n <= 20 && n <= len(c); n++ {
			for _, k := range []string{c[:n], c[len(c)-n:]} {
				if kind, has := kinds[valueHash(salt, k)]; has {
					return kind, true
				}
			}
		}
	}
	return "", false
}

// reChainDigits — подряд идущие цифры, между которыми человек мог
// поставить пробел, дефис или скобку. Своя, а не `reЦифры` из примет:
// та начинается с девяти знаков, а своим значением бывает и шестизначный
// номер документа.
var reChainDigits = regexp.MustCompile(`\d[\d \-()]{4,30}\d`)
