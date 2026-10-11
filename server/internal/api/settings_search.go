package api

// Поиск по настройкам: находит настройку, а не раздел.
//
// Сверху — совпавшие разделы и сущности (сервер, ящик, модель, секрет), ниже —
// сами настройки: путь словами, название, текущее значение. Нажатие ведёт к
// строке: экран открывается, «Тонкая настройка» или свёрнутый раздел
// раскрываются, строка встаёт в середину окна и вспыхивает
// (`Document.Highlight`, см. settings_highlight.go).
//
// Слои совпадения — от точного к дальнему: название настройки, её подсказка, путь
// (раздел, сущность, вкладка), слова раздела и заголовки формы, ключ в реестре.
// Синонимы разделов (`synonyms`) остаются вторым слоем: человек ищет «пуши», а
// раздел называется «Уведомления», и настройка внутри раздела находится по слову,
// которым зовут раздел, — хоть в её названии его нет.
//
// Сравнение — по рунам, без регистра и без различия «е» и «ё»: SQLite с
// кириллицей не дружит, и поиск живёт в памяти.

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// searchSubheading — строка под заголовком результатов: чем ищется.
const searchSubheading = "ищется по названию настройки, её подсказке, разделу и словам, которыми её обычно называют"

// maxEntityRows — сколько сущностей показывается над настройками: сто
// семьдесят моделей по слову «gpt» занимали бы экран, а нужная находится
// уточнением запроса.
const maxEntityRows = 8

// normText — текст для сравнения: строчные буквы, «е» вместо «ё».
func normText(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "ё", "е")
}

// queryWords — слова запроса: по пробелам, без кавычек и знаков на краях.
func queryWords(req string) []string {
	var out []string
	for _, w := range strings.Fields(normText(req)) {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// hasWordPrefix — слово начинает название или одно из его слов.
func hasWordPrefix(title, w string) bool {
	return strings.HasPrefix(title, w) || strings.Contains(title, " "+w)
}

// scoreEntry — насколько запись отвечает запросу; ноль — не отвечает вовсе:
// каждое слово обязано найтись хоть где-то.
func scoreEntry(e *indexEntry, words []string) int {
	total := 0
	for _, w := range words {
		switch {
		case hasWordPrefix(e.normTitle, w):
			total += 12
		case strings.Contains(e.normTitle, w):
			total += 8
		case strings.Contains(e.normHint, w):
			total += 3
		case strings.Contains(e.norm, w):
			total += 2
		default:
			return 0
		}
	}
	return total
}

// find — записи по запросу: отдельно сущности, отдельно настройки, лучшие
// первыми. Гостю (токен без права «всё») настройки входа не отдаются.
func (idx *settingsIndex) find(words []string, guest bool) (entities, settings []*indexEntry) {
	if idx == nil {
		return nil, nil // индекс собирается: находятся только разделы (perf-pass 4.3)
	}
	type hit struct {
		e     *indexEntry
		score int
	}
	var ents, sets []hit
	for _, e := range idx.Entries {
		if guest && e.Admin {
			continue
		}
		sc := scoreEntry(e, words)
		if sc == 0 {
			continue
		}
		if e.Kind == entryEntity {
			ents = append(ents, hit{e, sc})
		} else {
			sets = append(sets, hit{e, sc})
		}
	}
	for _, list := range [][]hit{ents, sets} {
		list := list
		sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	}
	for _, h := range ents {
		entities = append(entities, h.e)
	}
	for _, h := range sets {
		settings = append(settings, h.e)
	}
	return entities, settings
}

// maxSameSetting — сколько раз показывается «одинаковая настройка у каждой
// сущности» (цена у ста семидесяти трёх моделей, окно у пяти серверов): сто
// строк с одной и той же подписью читать нечем, а нужная сущность находится
// словом из её имени.
const maxSameSetting = 5

// groupedHit — запись результата и сколько таких же настроек у других сущностей
// не показано.
type groupedHit struct {
	e    *indexEntry
	more int
}

// groupSettings — результаты без повторов: настройка, что есть у каждой сущности
// раздела, показывается не больше `maxSameSetting` раз, остальные считаются.
func groupSettings(settings []*indexEntry) []*groupedHit {
	var out []*groupedHit
	shown := map[string]int{}
	last := map[string]*groupedHit{}
	for _, e := range settings {
		if e.Group != "" {
			if shown[e.Group] >= maxSameSetting {
				last[e.Group].more++
				continue
			}
			shown[e.Group]++
		}
		h := &groupedHit{e: e}
		out = append(out, h)
		if e.Group != "" {
			last[e.Group] = h
		}
	}
	return out
}

// entryValue — текущее значение записи для показа. У настройки реестра читается
// заново тем же путём, что читает движок: индекс живёт несколько минут, а значение
// человек только что мог поменять.
func (s *Server) entryValue(e *indexEntry) string {
	if e.Control == "secure" || e.Control == "code" || store.NameSmellsSecret(e.Key) {
		return ""
	}
	// заново читается то, что показывается так, как лежит: число, тумблер, строка.
	// Список и сегменты показывают подпись варианта, а в таблице лежит его ключ
	// («own» — «Только мои»): такое значение остаётся, как его собрал экран
	if e.Key != "" && liveControl(e.Control) {
		if def, ok := store.LookupSetting(e.Key); ok {
			return formatValue(e.Control, e.Unit, s.eng.Store().SettingShown(def))
		}
	}
	return e.Value
}

// liveControl — поле показывает значение настройки так, как оно лежит.
func liveControl(control string) bool {
	switch control {
	case "number", "toggle", "text":
		return true
	}
	return false
}

// settingResult — строка результата: путь над названием, значение под ним.
func (s *Server) settingResult(i int, e *indexEntry) Component {
	// значение-список (слова через запятую) бывает длиннее самой подсказки:
	// в подписи — его начало, целиком — на экране настройки
	caption := clipRunes(s.entryValue(e), 60)
	// подсказка в результате — первое, что она говорит, до знака конца фразы или
	// девяноста знаков: строка в три строки читается хуже макета
	if hint := firstSentence(e.Hint, 90); hint != "" {
		if caption != "" {
			caption += " · "
		}
		caption += hint
	}
	c := Component{Type: "row", Layout: "wide", ID: "найдено-" + strconv.Itoa(i),
		Title: strings.Join(e.Path, " → "), Subtitle: e.Title, Caption: caption,
		Actions: []Action{{Action: "$goto", Target: withHighlight(e.Addr, e.Anchor), Label: "Открыть"}}}
	if e.Fine {
		c.Badges = []Badge{{Text: "тонкая настройка"}}
	}
	return c
}

// firstSentence — подсказка до конца первой фразы, но не длиннее limit знаков.
func firstSentence(hint string, limit int) string {
	hint = strings.TrimSpace(hint)
	if i := strings.Index(hint, ". "); i > 0 && i < limit {
		hint = hint[:i]
	}
	return clipWords(hint, limit)
}

// entityResult — строка сущности: куда её положить и что это.
func entityResult(i int, e *indexEntry) Component {
	return Component{Type: "row", Layout: "wide", ID: "сущность-" + strconv.Itoa(i),
		Title: strings.Join(e.Path, " → "), Subtitle: e.Title, Caption: e.Hint,
		Actions: []Action{{Action: "$goto", Target: e.Addr, Label: "Открыть"}}}
}

// searchBlocks — результаты поиска по настройкам: разделы и сущности, затем
// настройки. Пусто нигде не бывает: ничего не нашлось — объяснение, а не белый
// экран. limit — сколько настроек показать; остальные приезжают «Показать ещё».
func (s *Server) searchBlocks(doc *Document, req string, limit int) []Component {
	words := queryWords(req)
	if len(words) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = defaultRows
	}
	sections := s.foundInSettings(doc.sections, req, doc.guest)
	idx := s.settingsIndex()
	entities, settings := idx.find(words, doc.guest)

	var out []Component
	if idx == nil {
		// индекс собирается в фоне (perf-pass 4.3): разделы находятся и без него
		out = append(out, Component{Type: "text", Style: "secondary", ID: indexBuildingID,
			Text: "Индекс настроек собирается: пока находятся только разделы, отдельные настройки найдутся через несколько секунд"})
	}
	top := sections
	shown := entities
	if len(shown) > maxEntityRows {
		shown = shown[:maxEntityRows]
	}
	for i, e := range shown {
		top = append(top, entityResult(i, e))
	}
	if len(entities) > len(shown) {
		top = append(top, Component{Type: "text", Style: "secondary",
			Text: "И ещё " + plural(len(entities)-len(shown), "сущность", "сущности", "сущностей") + " — уточните запрос"})
	}
	if len(top) > 0 {
		out = append(out, Component{Type: "section", ID: "found-sections", Expanded: true,
			Title: "Нашлось · разделы и сущности · " + strconv.Itoa(len(sections)+len(entities)), Children: top})
	}
	if grouped := groupSettings(settings); len(grouped) > 0 {
		page := grouped
		if len(page) > limit {
			page = page[:limit]
		}
		rows := make([]Component, 0, len(page))
		for i, h := range page {
			row := s.settingResult(i, h.e)
			if h.more > 0 {
				row.Badges = append(row.Badges, Badge{Text: "такая же у ещё " + strconv.Itoa(h.more), Level: LevelInfo})
			}
			rows = append(rows, row)
		}
		// Expanded: результатов бывает больше двадцати, а раздел длиннее двадцати строк
		// сворачивается (collapseLong) — свёрнутые результаты прятали бы то, что искали
		out = append(out, Component{Type: "section", ID: "found-settings", Expanded: true,
			Title: "Нашлось · настройки · " + strconv.Itoa(len(grouped)), Children: rows})
		if len(grouped) > len(page) {
			out = append(out, Component{Type: "button", Actions: []Action{{Action: "$more", Label: "Показать ещё"}}})
		}
	}
	if len(out) == 0 {
		return []Component{emptyState("Ничего не нашлось",
			"Ищется по названию настройки, её подсказке, разделу и словам, которыми её обычно называют: «окно», «пуши», «ключи», «sudo», «цена». "+
				"Сотрите запрос — вернутся все разделы.", "search")}
	}
	return out
}
