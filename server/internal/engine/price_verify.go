package engine

// Разбор страницы цен моделью и проверка её ответа кодом.
//
// Модель читает страницу любой вёрстки — это то, чего не умели регулярки, —
// но модели верить нельзя: она может взять соседнюю ячейку, цену другого
// тарифа или выдумать число. Поэтому ответ проходит проверку, которая от
// модели не зависит: каждое число ответа обязано буквально стоять на
// странице рядом с именем модели, имя обязано быть на странице, а модель
// нашей — найтись по имени, а не по слову модели. Не прошло — отброшено и
// посчитано, в предложение не попадает.

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// flexNum — число, которое модель написала как захотела: числом, строкой
// «$0.10» или прочерком. Непонятное — «нет числа», а не ошибка разбора
// всего ответа: одна кривая строка не должна хоронить остальные.
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	s = strings.NewReplacer("$", "", ",", "", " ", "", "€", "", "USD", "").Replace(s)
	if v, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		*f = flexNum(v)
		return nil
	}
	*f = 0
	return nil
}

// priceAnswer — строка ответа модели.
type priceAnswer struct {
	Model     string  `json:"model"`     // наше имя, если модель его узнала; проверке не служит
	PageName  string  `json:"page_name"` // как модель названа на странице
	Input     flexNum `json:"input"`
	Cached    flexNum `json:"cached"`
	Output    flexNum `json:"output"`
	PerMinute flexNum `json:"per_minute"`
	PeakIn    flexNum `json:"peak_input"`
	PeakCache flexNum `json:"peak_cached"`
	PeakOut   flexNum `json:"peak_output"`
	PeakHours string  `json:"peak_hours"`
	Note      string  `json:"note"`
}

func (a priceAnswer) name() string {
	if n := strings.TrimSpace(a.PageName); n != "" {
		return n
	}
	return strings.TrimSpace(a.Model)
}

// foundPrice — цена, прошедшая проверку и привязанная к нашей модели.
type foundPrice struct {
	Model    string // наше имя модели
	PageName string
	Price    string // «$0.1 / $0.5 / $0.01 / 1M» или «$0.003 / мин»
	Peak     string // цена в часы пик, если страница её называет
	Hours    string // часы пик
	Note     string
}

// priceStats — что случилось с ответом по дороге; идёт в итог проверки.
type priceStats struct {
	Asked      int      // строк в ответе модели
	Dropped    int      // не прошли проверку страницей
	DroppedWhy []string // до трёх примеров: что именно не сошлось
	Unmatched  int      // строки, для которых нет нашей модели
	// UnmatchedNames — их имена со страницы, до пяти: если ни одна наша модель
	// не нашлась, человеку нужно видеть, как страница их называет, чтобы
	// связать («Имя в прайсе»)
	UnmatchedNames []string
	Free           int // бесплатные: цена нулевая, считать нечего
	NoPrice        int // строки ответа без цены
}

func (st *priceStats) drop(why string) {
	st.Dropped++
	if len(st.DroppedWhy) < 3 {
		st.DroppedWhy = append(st.DroppedWhy, why)
	}
}

// parsePriceAnswer — массив строк из ответа модели: вокруг него бывают
// пояснения, ограждения кода и объект-обёртка.
func parsePriceAnswer(answer string) ([]priceAnswer, error) {
	s := fenceRe.ReplaceAllString(strings.TrimSpace(answer), "")
	head, _ := splitBalanced(s, "[")
	if head == "" {
		return nil, fmt.Errorf("в ответе модели нет списка цен")
	}
	var list []priceAnswer
	if err := json.Unmarshal([]byte(head), &list); err != nil {
		return nil, fmt.Errorf("ответ модели не разобран: %v", err)
	}
	return list, nil
}

// pricePrompt — задание модели. Числа она обязана брать со страницы как
// есть; что делать с чужой вёрсткой, сказано правилами, а не примерами
// одной страницы.
func pricePrompt(p ProviderConf, text string, models []ModelConf) string {
	var ours []string
	for _, c := range models {
		name := c.Model
		if name == "" {
			name = c.Name
		}
		if c.PriceName != "" {
			name += " (в прайсе: " + c.PriceName + ")"
		}
		ours = append(ours, name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Ты разбираешь страницу цен провайдера моделей «%s» и отвечаешь только JSON.\n", p.Name)
	fmt.Fprintf(&b, "Ниже её текст (адрес %s): ячейки таблиц разделены табуляцией, строки — переводом строки.\n\n", p.PriceURL)
	b.WriteString("Нас интересуют только эти модели (так они названы у нас; на странице имя бывает другим — " +
		"в ответе пиши имя со страницы):\n")
	b.WriteString(strings.Join(ours, ", "))
	b.WriteString("\n\nВерни JSON-массив, по объекту на модель, у которой на странице названа цена:\n")
	b.WriteString(`[{"model":"<наше имя, если узнал>","page_name":"<имя ровно как на странице, без сносок и скобок>",` +
		`"input":0.1,"cached":0.01,"output":0.5,"per_minute":null,` +
		`"peak_input":null,"peak_cached":null,"peak_output":null,"peak_hours":"","note":""}]` + "\n\n")
	b.WriteString(`Правила:
1. Цены в долларах за 1 миллион токенов. Числа — ровно как на странице: без пересчёта, без округления, без вычислений. Нет числа на странице — нет строки и нет поля.
2. Только обычный тариф. Пакетный (Batch), гибкий (Flex), приоритетный (Priority, Fast), региональный, цены дообученных моделей (Fine-tuning, Training) — не бери.
3. cached — цена входа из кэша («Cached input», «Cache hits», «Cache read», «Context caching»). Цена записи в кэш («Cache writes», «5m writes», «1h writes») — это не кэш и не вход, её не бери. Если в строке чисел на одно меньше, чем в заголовке, то у модели нет столбца записи в кэш: три числа — вход, кэш, выход; четыре — вход, кэш, запись в кэш, выход. Цены кэша нет — null.
4. Если цена зависит от длины контекста, бери цену для короткого, а длинный контекст назови в note («длинный контекст: вход 4, выход 15»).
5. Если у модели разные цены на текст, аудио и картинки — бери текст, остальное опиши в note. Модель, у которой на странице только цены аудио или картинок, пропусти.
6. Распознавание речи, что оплачивается за минуту звука, — в per_minute (доллары за минуту), input и output — null.
7. Эмбеддинги, картинки, видео, реалтайм, озвучка, инструменты и всё, где нет цены за токен или за минуту, — пропусти.
8. Часы пик (у DeepSeek): peak_input, peak_cached, peak_output — цены в пик; peak_hours — часы в виде «пн-пт 01:00-04:00,06:00-10:00 UTC», как они названы на странице. Пика нет — null и пустая строка.
9. Ничего не выдумывай. Не уверен — пропусти строку. Отвечай одним массивом JSON, без пояснений.

Текст страницы:
`)
	b.WriteString(text)
	return b.String()
}

// ---- проверка по тексту страницы ----

var (
	// сноска «(1)» рядом с именем: цифра в ней — не цена
	reFootnote = regexp.MustCompile(`[\(\[]\d{1,2}[\)\]]`)
	reNumber   = regexp.MustCompile(`\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?`)
)

// priceWindow — сколько знаков после имени модели считаются «рядом» в
// вёрстке, где цены идут отдельными строками под именем (Anthropic, Google),
// и сколько — в вёрстке по столбцам, где имена в шапке, а цены ниже
// (DeepSeek). Строка таблицы, где имя и цены в одной строке, окна не
// требует: рядом — это та же строка.
const (
	priceNearChars = 700
	priceWideChars = 3000
)

// numbersIn — числа в куске текста; сноски «(1)» не в счёт.
func numbersIn(s string) []float64 {
	s = reFootnote.ReplaceAllString(s, " ")
	var out []float64
	for _, m := range reNumber.FindAllString(s, -1) {
		if v, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func containsNum(have []float64, want float64) bool {
	for _, v := range have {
		if math.Abs(v-want) <= 1e-9*math.Max(1, math.Abs(want)) {
			return true
		}
	}
	return false
}

func containsAll(have, want []float64) bool {
	for _, w := range want {
		if !containsNum(have, w) {
			return false
		}
	}
	return true
}

// pageIndex — страница, подготовленная к проверкам: строчными буквами, по
// строкам с разделами и с позициями всех имён из ответа модели, чтобы «рядом с
// именем» обрывалось на следующем имени, а не тянулось до чужих цен.
type pageIndex struct {
	orig   string   // как на странице
	text   string   // строчными латинскими: длина та же, позиции общие
	marks  []int    // начала всех вхождений всех имён, по возрастанию
	lines  []string // строки страницы
	starts []int    // где каждая строка начинается в text
	sect   []string // раздел каждой строки; может быть пуст
	skip   []string // подстроки заголовков, чьи разделы не берём (строчные)
}

// asciiLower — только A-Z: обычное ToLower меняет длину строки у
// некоторых знаков, и позиции в копии разошлись бы с оригиналом.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func newPageIndex(doc priceDoc, names, skip []string) *pageIndex {
	ix := &pageIndex{orig: doc.Text, text: asciiLower(doc.Text), sect: doc.Sections}
	for _, w := range skip {
		if w = asciiLower(strings.TrimSpace(w)); w != "" {
			ix.skip = append(ix.skip, w)
		}
	}
	pos := 0
	for _, l := range strings.Split(doc.Text, "\n") {
		ix.lines = append(ix.lines, l)
		ix.starts = append(ix.starts, pos)
		pos += len(l) + 1
	}
	seen := map[string]bool{}
	for _, n := range names {
		n = asciiLower(strings.TrimSpace(n))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		for _, at := range ix.occurrences(n) {
			ix.marks = append(ix.marks, at)
		}
	}
	sort.Ints(ix.marks)
	return ix
}

// lineOf — номер строки, в которой лежит позиция.
func (ix *pageIndex) lineOf(pos int) int {
	return max(sort.SearchInts(ix.starts, pos+1)-1, 0)
}

func (ix *pageIndex) sectionOf(line int) string {
	if line < len(ix.sect) {
		return ix.sect[line]
	}
	return ""
}

// denied — раздел не про обычный вызов модели: дообучение, пакетный тариф,
// инструменты. Совпадение по заголовку любого уровня в цепочке: «Finetuning»
// закрывает и всё, что лежит под ним.
func (ix *pageIndex) denied(section string) bool {
	low := asciiLower(section)
	for _, w := range ix.skip {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// occurrences — где имя стоит на странице целым словом: «gpt-5» не живёт
// внутри «gpt-5.4» и «gpt-5-mini», а «claude sonnet 4» — внутри «claude
// sonnet 4.5». Справа за именем может идти прописная буква: на странице
// Anthropic описание модели приклеено к её названию.
func (ix *pageIndex) occurrences(name string) []int {
	var out []int
	from := 0
	for {
		i := strings.Index(ix.text[from:], name)
		if i < 0 {
			return out
		}
		at := from + i
		end := at + len(name)
		from = at + 1
		if r, _ := utf8.DecodeLastRuneInString(ix.text[:at]); at > 0 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_') {
			continue
		}
		if end < len(ix.orig) {
			r, w := utf8.DecodeRuneInString(ix.orig[end:])
			next, _ := utf8.DecodeRuneInString(ix.orig[end+w:])
			switch {
			case unicode.IsDigit(r) || r == '-' || r == '_' || unicode.IsLower(r):
				continue
			case r == '.' && unicode.IsDigit(next):
				continue
			}
		}
		out = append(out, at)
	}
}

// nextMark — начало ближайшего вхождения любого имени правее позиции.
func (ix *pageIndex) nextMark(after int) int {
	i := sort.SearchInts(ix.marks, after+1)
	if i < len(ix.marks) {
		return ix.marks[i]
	}
	return len(ix.text)
}

// priceClaim — что модель утверждает о строке: числа по ролям.
type priceClaim struct {
	in, cached, out, perMin float64
}

// pageHit — итог сверки одного имени со страницей.
type pageHit struct {
	ok      bool
	section string // раздел строки, которая решила
	why     string // почему нет
}

// Три вёрстки страницы: строка таблицы, имена в шапке столбцов и имя
// отдельной строкой с ценами под ним.
type layout int

const (
	layoutRow layout = iota
	layoutColumns
	layoutNear
)

// onPage — стоят ли числа ответа на странице рядом с именем, и в том ли они
// столбце. Вёрстки:
//   - строка таблицы: имя и цены в одной строке. Шапку столбцов ищем над
//     таблицей: число обязано стоять в столбце своей роли, а не просто в
//     строке (запись в кэш — не выход). Шапки нет, а чисел больше трёх —
//     строка не подтверждена;
//   - имена в шапке столбцов (в строке с именем есть ещё имя): цены ниже,
//     ищем в широком окне после имени;
//   - имя отдельной строкой, цены под ним: если над таблицей есть шапка —
//     значения под именем сверяются с ней по порядку, иначе окно до
//     следующего имени, но не длиннее priceNearChars.
//
// Разделы, что не про обычный вызов модели (дообучение, пакетный тариф,
// инструменты), не читаются вовсе. Решает первое вхождение имени в разрешённом
// разделе, рядом с которым есть строка цен — столько чисел, сколько мы
// проверяем. Имя стоит на странице и в меню, и в сноске, и в таблице, но прозу
// с одной цифрой строкой цен не считаем. А вторую, третью таблицу с тем же
// именем не ищем: у провайдеров обычный тариф идёт первым, а пакетный, гибкий
// и приоритетный — следом, и цена пакетного тарифа, названная моделью как
// обычная, на странице есть, но не первая.
func (ix *pageIndex) onPage(name string, c priceClaim, want []float64) pageHit {
	name = asciiLower(strings.TrimSpace(name))
	if name == "" {
		return pageHit{why: "нет имени"}
	}
	need := max(len(want), 1)
	deniedSeen := false
	for _, at := range ix.occurrences(name) {
		li := ix.lineOf(at)
		section := ix.sectionOf(li)
		if ix.denied(section) {
			deniedSeen = true
			continue
		}
		end := at + len(name)
		lineStart := ix.starts[li]
		lineEnd := len(ix.text)
		if i := strings.Index(ix.text[end:], "\n"); i >= 0 {
			lineEnd = end + i
		}
		var window string
		var kind layout
		switch {
		case ix.otherMarkIn(lineStart, lineEnd, at):
			// в строке несколько имён — это шапка столбцов, цены ниже. Первой
			// проверкой: цифры в чужом имени («v4») иначе сошли бы за цены строки
			stop := ix.cutAtDenied(li, end, min(end+priceWideChars, len(ix.text)))
			window, kind = ix.text[end:stop], layoutColumns
		case len(numbersIn(ix.text[end:lineEnd])) > 0:
			window, kind = ix.text[end:lineEnd], layoutRow
		default:
			// окно не заходит в закрытый раздел: у Google под именем модели
			// идут обычный тариф и, следом, пакетный — со своим заголовком
			stop := ix.cutAtDenied(li, end, min(ix.nextMark(at), end+priceNearChars, len(ix.text)))
			window, kind = ix.text[end:max(stop, end)], layoutNear
		}
		nums := numbersIn(window)
		if len(nums) < need {
			continue // проза или подпись, а не строка цен
		}
		hit := pageHit{section: section, why: "числа нет на странице рядом с именем"}
		switch kind {
		case layoutRow:
			hit.ok, hit.why = ix.checkRow(li, c, want, nums)
		case layoutNear:
			hit.ok, hit.why = ix.checkNear(li, c, want, nums)
		default:
			hit.ok = containsAll(nums, want)
		}
		if hit.ok {
			hit.why = ""
		}
		return hit
	}
	if deniedSeen {
		return pageHit{why: "цены стоят только в разделах не про обычный вызов модели (дообучение, пакетный тариф, инструменты)"}
	}
	return pageHit{why: "числа нет на странице рядом с именем"}
}

// cutAtDenied — где обрывается окно, если ниже начинается закрытый раздел.
func (ix *pageIndex) cutAtDenied(li, from, to int) int {
	for j := li + 1; j < len(ix.lines) && j < len(ix.sect); j++ {
		if ix.starts[j] >= to {
			break
		}
		if ix.denied(ix.sect[j]) {
			return max(ix.starts[j], from)
		}
	}
	return to
}

// otherMarkIn — есть ли в строке другое имя, кроме вхождения на позиции at.
func (ix *pageIndex) otherMarkIn(from, to, at int) bool {
	for _, m := range ix.marks {
		if m >= from && m < to && m != at {
			return true
		}
	}
	return false
}

// headerFor — шапка таблицы над строкой: ближайшая выше строка-шапка из того же
// раздела или из объемлющего (шапка стоит под заголовком таблицы, а строки —
// под подзаголовком). Дальше ста двадцати строк не ищем: это уже другая
// таблица.
func (ix *pageIndex) headerFor(li int) []string {
	own := ix.sectionOf(li)
	for j := li - 1; j >= 0 && li-j <= 120; j-- {
		if !strings.HasPrefix(own, ix.sectionOf(j)) {
			continue // раздел не объемлющий: чужая таблица
		}
		if cells := trimCells(ix.lines[j]); isHeaderCells(cells) {
			return cells
		}
	}
	return nil
}

func trimCells(line string) []string {
	cells := strings.Split(line, "\t")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	for len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// checkRow — строка таблицы: числа ответа сверяются со столбцами шапки.
func (ix *pageIndex) checkRow(li int, c priceClaim, want, nums []float64) (bool, string) {
	cells := trimCells(ix.lines[li])
	header := ix.headerFor(li)
	var cm map[colRole]int
	if header != nil {
		cm = columnMap(header, len(cells))
	}
	if cm == nil {
		// шапки нет или строка с ней не сходится: больше трёх чисел — не
		// угадываем, какое за что; до трёх — вход, кэш, выход не спутать
		if len(nums) > 3 {
			why := "шапка столбцов не найдена"
			if header != nil {
				why = fmt.Sprintf("строка не сходится с шапкой таблицы (%d ячеек, в шапке %d)", len(cells), len(header))
			}
			return false, fmt.Sprintf("в строке %d чисел, а %s — не угадываю, какое число за что", len(nums), why)
		}
		return containsAll(nums, want), "числа нет на странице рядом с именем"
	}
	values := map[colRole][]float64{}
	for r, i := range cm {
		if i < len(cells) {
			values[r] = numbersIn(cells[i])
		}
	}
	return claimMatches(c, values, func(role colRole) string {
		if i, ok := cm[role]; ok && i < len(cells) {
			return cells[i]
		}
		return ""
	})
}

// checkNear — имя отдельной строкой, значения под ним по одному на строку.
// Есть шапка (Anthropic: «Input, Output, 5m writes, 1h writes, Hits and
// refreshes») — значения сверяются с ней по порядку; нет её (Google подписывает
// каждую строку словами) — обычная проверка окном.
func (ix *pageIndex) checkNear(li int, c priceClaim, want, nums []float64) (bool, string) {
	header := ix.headerFor(li)
	if header == nil {
		return containsAll(nums, want), "числа нет на странице рядом с именем"
	}
	roles := headerRoles(header)
	var data []colRole
	for _, r := range roles {
		if r != roleOther {
			data = append(data, r)
		}
	}
	values := map[colRole][]float64{}
	texts := map[colRole]string{}
	for k, r := range data {
		j := li + 1 + k
		if j >= len(ix.lines) || !looksValue(ix.lines[j]) {
			return false, fmt.Sprintf("под именем меньше значений, чем столбцов в шапке (%d) — не угадываю, какое за что", len(data))
		}
		if _, dup := texts[r]; !dup {
			values[r] = numbersIn(ix.lines[j])
			texts[r] = ix.lines[j]
		}
	}
	return claimMatches(c, values, func(role colRole) string { return texts[role] })
}

// looksValue — строка похожа на ячейку с ценой: «$3 / MTok», «/ MTok» (цены
// нет), «-». Описание модели или следующее имя ею не будет.
func looksValue(l string) bool {
	if l == "" || strings.Contains(l, "\t") || len([]rune(l)) > 40 {
		return false
	}
	return strings.ContainsAny(l, "0123456789/-–")
}

// claimMatches — числа ответа против столбцов: вход в столбце «вход», выход в
// столбце «выход», кэш в столбце «вход из кэша». Число, что стоит в столбце
// записи в кэш, выходом не станет, даже если равно чужому.
func claimMatches(c priceClaim, values map[colRole][]float64, text func(colRole) string) (bool, string) {
	check := func(role colRole, want float64) string {
		got := values[role]
		if len(got) == 0 {
			return fmt.Sprintf("в столбце «%s» у строки числа нет, а модель назвала %s", role, numStr(want))
		}
		if !containsNum(got[:1], want) {
			return fmt.Sprintf("в столбце «%s» стоит %s, а модель назвала %s — другой столбец", role, numStr(got[0]), numStr(want))
		}
		return ""
	}
	if c.perMin > 0 {
		// минута звука: в столбце оценки или в любой ячейке про минуты
		for _, v := range values[roleCost] {
			if containsNum([]float64{v}, c.perMin) {
				return true, ""
			}
		}
		for role := range values {
			if strings.Contains(strings.ToLower(text(role)), "min") && containsNum(values[role], c.perMin) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("цены за минуту %s нет в столбце оценки", numStr(c.perMin))
	}
	if c.in > 0 {
		if why := check(roleInput, c.in); why != "" {
			return false, why
		}
	}
	if c.out > 0 {
		if why := check(roleOutput, c.out); why != "" {
			return false, why
		}
	}
	if c.cached > 0 {
		if why := check(roleCached, c.cached); why != "" {
			return false, why
		}
	}
	return true, ""
}

// ---- разбор ответа ----

// Здравый смысл цен: то, что не сходится, значит, модель взяла не тот
// столбец. Кэш дешевле входа, выход не дешевле входа, потолок от опечатки
// на порядки.
const priceCeiling = 5000.0

// answerNumbers — числа, которые обязаны стоять на странице, и строки цены.
// Пустая цена — не цена (per_minute без токенов и токены без минуты — оба
// законны).
func (a priceAnswer) numbers() (want []float64, price, peak string, why string) {
	in, out, cache, per := float64(a.Input), float64(a.Output), float64(a.Cached), float64(a.PerMinute)
	for _, v := range []float64{in, out, cache, per, float64(a.PeakIn), float64(a.PeakOut), float64(a.PeakCache)} {
		if v < 0 || v > priceCeiling {
			return nil, "", "", "число вне разумного"
		}
	}
	switch {
	case per > 0:
		return []float64{per}, fmt.Sprintf("$%s / мин", numStr(per)), "", ""
	case in == 0 && out == 0:
		return nil, "", "", "" // строка без цены: пропуск, а не ошибка
	case in <= 0 || out <= 0:
		return nil, "", "", "нет цены входа или выхода"
	case out < in:
		return nil, "", "", "выход дешевле входа — вероятно, взят не тот столбец"
	case cache > in:
		return nil, "", "", "кэш дороже входа — вероятно, взят не тот столбец"
	}
	want = []float64{in, out}
	price = tokenPrice(in, out, cache)
	if cache > 0 {
		want = append(want, cache)
	}
	if a.PeakIn > 0 && a.PeakOut > 0 {
		pin, pout, pcache := float64(a.PeakIn), float64(a.PeakOut), float64(a.PeakCache)
		if pout < pin || pcache > pin {
			return nil, "", "", "цены в пик не сходятся"
		}
		peak = tokenPrice(pin, pout, pcache)
		want = append(want, pin, pout)
		if pcache > 0 {
			want = append(want, pcache)
		}
	}
	return want, price, peak, ""
}

// claim — числа ответа по ролям столбцов.
func (a priceAnswer) claim() priceClaim {
	return priceClaim{in: float64(a.Input), cached: float64(a.Cached), out: float64(a.Output), perMin: float64(a.PerMinute)}
}

// tokenPrice — цена в формате Сёмы: «$0.55 / $2.20 / $0.11 / 1M».
func tokenPrice(in, out, cache float64) string {
	if cache > 0 {
		return fmt.Sprintf("$%s / $%s / $%s / 1M", numStr(in), numStr(out), numStr(cache))
	}
	return fmt.Sprintf("$%s / $%s / 1M", numStr(in), numStr(out))
}

// numStr — число без хвоста нулей и без экспоненты.
func numStr(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// verifyAnswers — что из ответа модели подтверждено страницей. Возвращает
// подтверждённые строки; остальное считает в st.
func verifyAnswers(doc priceDoc, skip []string, answers []priceAnswer, st *priceStats) []verifiedPrice {
	names := make([]string, 0, len(answers))
	for _, a := range answers {
		names = append(names, a.name())
	}
	ix := newPageIndex(doc, names, skip)
	var out []verifiedPrice
	for _, a := range answers {
		name := a.name()
		if name == "" {
			st.drop("строка без имени модели")
			continue
		}
		want, price, peak, why := a.numbers()
		if why != "" {
			st.drop(name + ": " + why)
			continue
		}
		if price == "" {
			st.NoPrice++ // модель назвала имя, а цены не привела: пропуск
			continue
		}
		hit := ix.onPage(name, a.claim(), want)
		if !hit.ok {
			st.drop(name + ": " + hit.why)
			continue
		}
		v := verifiedPrice{PageName: name, Price: price, Section: hit.section, Note: strings.TrimSpace(a.Note)}
		if peak != "" {
			// часы пик — тоже слова модели: годятся, если наш же разбор их
			// понимает и каждое время стоит на странице
			if hours := strings.TrimSpace(a.PeakHours); hours != "" && len(parsePeak(hours)) > 0 && hoursOnPage(ix.text, hours) {
				v.Peak, v.Hours = peak, hours
			} else {
				v.Note = joinNote(v.Note, "цены в пик на странице есть, но часы не подтверждены — пик не взят")
			}
		}
		out = append(out, v)
	}
	return out
}

// verifiedPrice — строка, подтверждённая страницей, ещё без нашей модели.
type verifiedPrice struct {
	PageName string
	Price    string
	Peak     string
	Hours    string
	Note     string
	Section  string // раздел страницы, где строка найдена: дата сильнее базовой только внутри одного
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

var reClock = regexp.MustCompile(`\d{1,2}:\d{2}`)

// hoursOnPage — каждое «ЧЧ:ММ» из часов пик стоит на странице.
func hoursOnPage(text, hours string) bool {
	clocks := reClock.FindAllString(hours, -1)
	if len(clocks) == 0 {
		return false
	}
	for _, c := range clocks {
		if !strings.Contains(text, c) && !strings.Contains(text, "0"+c) {
			return false
		}
	}
	return true
}

// ---- привязка к нашим моделям ----

var (
	reParen = regexp.MustCompile(`\([^)]*\)`)
	// хвост-дата в имени модели: gpt-5.4-2026-03-05, claude-3-5-haiku-20241022, gpt-3.5-turbo-0125
	reDateTail = regexp.MustCompile(`[-_](\d{4}-\d{2}-\d{2}|\d{8}|\d{4})$`)
)

// priceKey — имя модели как множество слов: «Claude Haiku 3.5» и
// «claude-3-5-haiku» — одна модель, а «gpt-5» и «gpt-5-mini» — разные.
//
// Слова сравниваются как множество, а числа версии — по порядку: иначе
// «gpt-5.4» и «gpt-4.5» — одна модель.
func priceKey(name string) string {
	name = strings.ToLower(reParen.ReplaceAllString(name, " "))
	toks := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var words, nums []string
	for _, t := range toks {
		if strings.IndexFunc(t, unicode.IsLetter) >= 0 {
			words = append(words, t)
		} else {
			nums = append(nums, t)
		}
	}
	sort.Strings(words)
	return strings.Join(words, " ") + "|" + strings.Join(nums, " ")
}

// priceNames — как нашу модель могут звать на странице. Первым идёт то, что
// связал человек, потом имя в API, потом наше; после них — те же имена без
// даты в хвосте: у страницы одна строка на «gpt-5.4», а в API её
// закрепляют датой.
func priceNames(c ModelConf) (exact, dateless []string) {
	add := func(list *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*list = append(*list, s)
		}
	}
	base := func(s string) string {
		if i := strings.LastIndex(s, "/"); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	for _, s := range []string{c.PriceName, base(c.Model), c.Name} {
		add(&exact, s)
	}
	for _, s := range []string{base(c.Model), c.Name} {
		if t := reDateTail.ReplaceAllString(s, ""); t != s {
			add(&dateless, t)
		}
	}
	return exact, dateless
}

// bindPrices — подтверждённые строки → наши модели. Имя со страницы
// сопоставляется с моделью по её именам (`priceNames`), а не по тому, что
// написала модель ответа: её догадка «это наш gpt-5.4» проверке не служит.
// Строка, к которой нет нашей модели, считается, но не предлагается.
//
// Отдельная строка с датой («gpt-4o-2024-05-13») сильнее цены базовой модели
// («gpt-4o»), но только если обе лежат в одном разделе страницы. Дата в имени
// встречается и у дообученных моделей, и у пакетного тарифа: там та же
// строка значит другую цену. Связка человека («имя в прайсе») сильнее всего.
func bindPrices(found []verifiedPrice, models []ModelConf, st *priceStats) []foundPrice {
	byKey := map[string]*verifiedPrice{}
	for i, v := range found {
		k := priceKey(v.PageName)
		if byKey[k] == nil {
			byKey[k] = &found[i]
		}
	}
	used := map[string]bool{}
	first := func(list []string) (*verifiedPrice, string) {
		for _, n := range list {
			if v := byKey[priceKey(n)]; v != nil {
				return v, priceKey(n)
			}
		}
		return nil, ""
	}
	var out []foundPrice
	for _, c := range models {
		exact, dateless := priceNames(c)
		hit, hitKey := first(exact)
		base, baseKey := first(dateless)
		switch {
		case hit == nil:
			hit, hitKey = base, baseKey
		case base != nil && hit.Section != base.Section && priceKey(c.PriceName) != hitKey:
			// строка с датой лежит в другом разделе, чем базовая: это не её
			// уточнение, а другая цена
			hit, hitKey = base, baseKey
		}
		if hit == nil {
			continue
		}
		used[hitKey] = true
		out = append(out, foundPrice{Model: c.Name, PageName: hit.PageName, Price: hit.Price,
			Peak: hit.Peak, Hours: hit.Hours, Note: hit.Note})
	}
	for _, v := range found {
		if k := priceKey(v.PageName); !used[k] {
			used[k] = true // одно имя — один раз
			st.Unmatched++
			if len(st.UnmatchedNames) < 5 {
				st.UnmatchedNames = append(st.UnmatchedNames, v.PageName)
			}
		}
	}
	return out
}

// crossCheckTable — вёрстка по столбцам (DeepSeek): числа шапки и цен не
// привязаны к имени построчно, и модель могла отдать цены соседнему столбцу —
// числа на странице есть, а стоят не там. Поэтому таблицу читает ещё и
// строгий разбор регулярками, независимо от модели: где он сошёлся и назвал
// эту модель, цены обязаны совпасть. Не сошлись — отброшено: одна из двух
// ошибается, а ошибку в деньгах предлагать нельзя.
func crossCheckTable(raw string, found []verifiedPrice, st *priceStats) []verifiedPrice {
	table := priceFromCells(cells(raw))
	if len(table) == 0 {
		return found
	}
	byKey := map[string]string{}
	for name, line := range table {
		byKey[priceKey(name)] = line
	}
	var out []verifiedPrice
	for _, v := range found {
		if line, ok := byKey[priceKey(v.PageName)]; ok {
			main, peak, _ := splitPeak(line)
			if !linePricesMatch(v.Price, main) || (v.Peak != "" && peak != "" && !linePricesMatch(v.Peak, peak)) {
				st.drop(v.PageName + ": расходится с независимым разбором таблицы")
				continue
			}
		}
		out = append(out, v)
	}
	return out
}
