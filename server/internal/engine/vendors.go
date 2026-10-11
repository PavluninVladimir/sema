package engine

// Кто прислал документ. Одно и то же имя в бумагах пишется по-разному, и
// пока Сёма считает написания, а не поставщиков, любая сумма по поставщику
// неверна: «Горэнерго» на живой Сёме стоит тремя строками.
//
// Сводим только то, что сводится наверняка: два написания дают один ключ
// после снятия организационной шелухи («ООО», «АО», «ПАО», кавычки,
// «ОП Городское отделение»). «Т Плюс ПАО» и «ТП ПАО» одним ключом не
// станут — и правильно: догадаться, что это одно и то же, нельзя, а
// ошибиться значит сложить чужие деньги. Такое сводит человек кнопкой на
// экране «Поставщики», и его решение потом сильнее нашего.

import (
	"regexp"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// формыОрганизаций — приставки и хвосты, которые ничего не говорят о том,
// кто это. Живут в настройках: у каждого свой круг поставщиков и своя
// бюрократия.
const formKey = "vendors.forms"

const formSeed = "ооо, оао, зао, пао, ао, ип, нко, ltd, llc, inc, gmbh, pbc, оп, филиал, городское отделение, обособленное подразделение"

var notLetters = regexp.MustCompile(`[^\p{L}\p{Nd}]+`)

// VendorForms — что считаем шелухой при сведении написаний.
func (e *Engine) VendorForms() []string {
	return formFromLine(e.store.Setting(formKey, formSeed))
}

func formFromLine(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(strings.ToLower(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (e *Engine) SetVendorForms(list string) error {
	return e.store.SetSetting(formKey, strings.TrimSpace(list))
}

// providerKey — написание в ключ. «АО "Горэнерго"», «АО
// Горэнерго» и «ОП Городское отделение АО Горэнерго» дают
// «горэнерго».
func providerKey(raw string, form []string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	// длинные формы снимаем до разбиения на слова: «городское отделение» —
	// два слова, и по одному они не ловятся
	for _, f := range form {
		if strings.Contains(f, " ") {
			s = strings.ReplaceAll(s, f, " ")
		}
	}
	words := notLetters.Split(s, -1)
	short := map[string]bool{}
	for _, f := range form {
		if !strings.Contains(f, " ") {
			short[f] = true
		}
	}
	var clean []string
	for _, w := range words {
		if w == "" || short[w] {
			continue
		}
		clean = append(clean, w)
	}
	if len(clean) == 0 {
		// имя целиком состояло из шелухи — тогда шелуха и есть имя
		return strings.Join(notLetters.Split(s, -1), " ")
	}
	return strings.Join(clean, " ")
}

// FileProvider — привязать файл к поставщику справочника. Сырое имя в
// записи не меняется: так написано в документе.
//
// Первое написание заводит поставщика и даёт ему своё имя — оно хотя бы
// человеческое. Дальше все написания с тем же ключом приезжают к нему.
func (e *Engine) FileProvider(a *store.Attachment) (int64, error) {
	raw := strings.TrimSpace(a.Vendor)
	if raw == "" {
		return 0, nil
	}
	key := providerKey(raw, e.VendorForms())
	if key == "" {
		return 0, nil
	}
	if id, ok := e.store.VendorByAlias(key); ok {
		// написание уже знакомо: связь только с файлом
		if err := e.store.LinkAlias(key, raw, id, "сам"); err != nil {
			return 0, err
		}
		return id, e.store.SetFileVendor(a.ID, id)
	}
	id, err := e.store.EnsureVendor(providerName(raw))
	if err != nil {
		return 0, err
	}
	if err := e.store.LinkAlias(key, raw, id, "сам"); err != nil {
		return 0, err
	}
	return id, e.store.SetFileVendor(a.ID, id)
}

// providerName — как назвать поставщика в списке. Кавычки и форма
// организации из имени убираются, а слова остаются как в документе:
// «ООО "УМНЫЙ РИТЕЙЛ"» → «УМНЫЙ РИТЕЙЛ». Придумывать регистр за документ
// не берёмся — человек переименует, если захочет.
func providerName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.NewReplacer(`"`, " ", "«", " ", "»", " ", "'", " ").Replace(s)
	fields := strings.Fields(s)
	var out []string
	for _, w := range fields {
		l := strings.ToLower(w)
		if l == "ооо" || l == "оао" || l == "зао" || l == "пао" || l == "ао" || l == "ип" {
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return strings.TrimSpace(raw)
	}
	return clip(strings.Join(out, " "), 120)
}

// ConsolidateProviders — разовый проход по уже разобранным документам. До
// него справочник пуст, а чеки лежат с сырыми именами: 104 файла на живой
// Сёме, из них двенадцать — один Горэнерго под тремя вывесками.
func (e *Engine) ConsolidateProviders() int {
	if e.store.Setting("vendors.seeded", "") == "1" {
		return 0
	}
	files, err := e.store.FilesWithVendorText(2000)
	if err != nil {
		return 0
	}
	n := 0
	for _, a := range files {
		if id, err := e.FileProvider(a); err == nil && id > 0 {
			n++
		}
	}
	_ = e.store.SetSetting("vendors.seeded", "1")
	return n
}
