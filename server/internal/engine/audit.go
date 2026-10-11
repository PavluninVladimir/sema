package engine

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Журнал отправленного: расход показывает цену, а этот журнал — содержимое.
// Ставка на облачные модели требует, чтобы человек мог посмотреть, что
// именно ушло наружу. Секреты и маски вырезаются до записи.

const (
	sentKeepKey  = "audit.keep_days"
	sentFullKey  = "audit.keep_full" // 1 — хранить полный текст, 0 — только выжимку
	sentPreviewN = 400
)

func (e *Engine) SentKeepDays() int {
	v, _ := strconv.Atoi(e.store.Setting(sentKeepKey, "30"))
	if v <= 0 {
		v = 30
	}
	return v
}

func (e *Engine) SentKeepFull() bool { return e.store.Setting(sentFullKey, "1") == "1" }

func (e *Engine) SetSentKeepFull(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(sentFullKey, v)
}

// recordSent пишет запись журнала по вызову модели.
func (e *Engine) recordSent(c llm.Call) {
	prompt := e.maskOutput(c.Prompt)
	answer := e.maskOutput(c.Answer)
	row := &store.SentRow{
		Consumer: c.Consumer, Model: c.Model, Outside: e.ModelIsRemote(c.Model), Kind: c.Kind,
		// недоверие пишется отдельно от адреса: проверять «секретное не
		// утекло» по `outside` нельзя — доверенная модель бывает удалённой,
		// а недоверенная стоять в соседнем контейнере
		Untrusted: e.ModelIsCloud(c.Model),
		Class:     e.classOfCall(c, prompt), TokensIn: c.Usage.In, TokensOut: c.Usage.Out, Seconds: c.Seconds,
		OK: c.Err == nil, Retry: c.Retryable, Sources: sentSources(c.Prompt), Preview: clip(prompt, sentPreviewN),
	}
	if e.SentKeepFull() {
		row.Prompt, row.Answer = prompt, answer
	}
	if c.Err != nil {
		// ошибка шлюза нередко несёт кусок запроса: маски нужны и здесь
		row.Err = clip(e.maskOutput(c.Err.Error()), 300)
	}
	_ = e.store.AddSent(row)
}

// sentClass — насколько чувствительно то, что ушло. Приметы у сущностей
// (письмо, файл) пока свои: здесь остался и символ «@», и окно в две
// тысячи знаков. Дверь считает иначе (`КлассТекста`), и разница нарочная —
// менять приметы у сущностей значит переразмечать всю базу.
func sentClass(text string) string {
	class, _ := sentClassWhy(text)
	return class
}

// sentClassWhy — то же самое, но говорит, какая примета сработала. Причина
// хранится вместе с классом: на вопрос «почему это секретное» раньше
// ответить было нечем — приходилось пересчитывать по коду.
func sentClassWhy(text string) (class, why string) {
	low := strings.ToLower(text)
	// машиночитаемая строка паспорта: латиница в два ряда по 44 знака с
	// решётками. Её tesseract вытаскивает даже там, где остальное каша —
	// у неё моноширинный шрифт и высокий контраст, и это самая надёжная
	// примета документа на фотографии
	if reMRZ.MatchString(text) {
		return store.ClassSecret, "машиночитаемая строка документа"
	}
	for _, w := range []string{
		"паспорт", "снилс", "инн ", "карта ", "cvv", "iban", "договор №", "медицин", "диагноз",
		"водительск", "удостоверение", "свидетельство о рожд", "свидетельство о брак",
		"страховой полис", "полис омс", "полис дмс", "серия и номер",
	} {
		if strings.Contains(low, w) {
			return store.ClassSecret, "слово «" + strings.TrimSpace(w) + "»"
		}
	}
	for _, w := range []string{"счёт", "оплат", "сумма", "@", "личн", "телефон", "адрес"} {
		if strings.Contains(low, w) {
			return store.ClassSensitive, "слово «" + strings.TrimSpace(w) + "»"
		}
	}
	return store.ClassPlain, ""
}

// reMRZ — строка машиночитаемой зоны документа: `P<RUSIVANOV<<IVAN<<<`.
// Хватает одной строки: подряд идущие решётки в латинице больше нигде не
// встречаются.
var reMRZ = regexp.MustCompile(`[A-Z0-9<]{6,}<<[A-Z0-9<]{6,}`)

var (
	reSrcMsg  = regexp.MustCompile(`(?i)письмо\s*#?(\d+)`)
	reSrcFile = regexp.MustCompile(`(?i)файл\s*#?(\d+)`)
	reSrcHost = regexp.MustCompile(`(?i)сервер[е]?\s+([A-Za-z0-9_.-]{2,40})`)
	reSrcMCP  = regexp.MustCompile(`MCP server «([^»]{1,40})»`)
)

// sentSources — что было в контексте: короткая строка для журнала.
func sentSources(prompt string) string {
	var parts []string
	if m := reSrcMsg.FindAllStringSubmatch(prompt, 3); len(m) > 0 {
		var ids []string
		for _, x := range m {
			ids = append(ids, x[1])
		}
		parts = append(parts, "письма "+strings.Join(ids, ", "))
	}
	if m := reSrcFile.FindAllStringSubmatch(prompt, 3); len(m) > 0 {
		var ids []string
		for _, x := range m {
			ids = append(ids, x[1])
		}
		parts = append(parts, "файлы "+strings.Join(ids, ", "))
	}
	if m := reSrcHost.FindStringSubmatch(prompt); m != nil {
		parts = append(parts, "сервер "+m[1])
	}
	if m := reSrcMCP.FindStringSubmatch(prompt); m != nil {
		parts = append(parts, "MCP "+m[1])
	}
	if strings.Contains(prompt, "Skill «") {
		parts = append(parts, "скиллы")
	}
	if strings.Contains(prompt, "Memory") || strings.Contains(prompt, "память") {
		parts = append(parts, "память")
	}
	return store.SentSourcesLine(parts...)
}

// SentPurgePass — уборка журнала по сроку; зовётся уборкой очереди.
func (e *Engine) SentPurgePass() int64 {
	return e.store.PurgeSent(time.Now().AddDate(0, 0, -e.SentKeepDays()).Format(time.RFC3339))
}

// SentToday — сводка за сутки для «Здоровья».
// SentToday — сводка отправок за сутки. Помнится десять секунд: её
// просят и карточки «Здоровья», и блок «что ушло» на том же экране, и
// точка в меню на каждом экране — а запрос идёт по всему журналу за сутки.
func (e *Engine) SentToday() store.SentStat {
	e.sentMu.Lock()
	defer e.sentMu.Unlock()
	if time.Since(e.sentWhen) < 10*time.Second {
		return e.sentSummary
	}
	e.sentSummary, e.sentWhen = e.store.SentStats(dayBack()), time.Now()
	return e.sentSummary
}

// SentClassesToday — сколько отправок каждого класса за сутки: счётчик у
// чипа «чувствительное» и «секретное». Без числа человек не знает, есть
// ли там что-то вообще, и жмёт наугад.
func (e *Engine) SentClassesToday() map[string]int {
	return e.store.SentClassCounts(dayBack())
}

func dayBack() string { return time.Now().Add(-24 * time.Hour).Format(time.RFC3339) }

// humanCount — короткое число для карточек: 12, 3.4 тыс., 1.2 млн.
func humanCount(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1000000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + " тыс."
	default:
		return strconv.FormatFloat(float64(n)/1000000, 'f', 1, 64) + " млн"
	}
}

// classOfCall — класс записи журнала. Если звавший назвал сущность, класс
// берётся у неё: она знает про себя больше, чем текст промпта. Иначе —
// по тексту, как раньше.
func (e *Engine) classOfCall(c llm.Call, prompt string) string {
	if c.Class != "" {
		return c.Class
	}
	if c.ClassKind != "" && c.ClassID > 0 {
		if m, ok := e.store.ClassMarkOf(c.ClassKind, c.ClassID); ok && m.Class != "" {
			return m.Class
		}
	}
	// той же меркой, что и дверь: если журнал считает класс своей
	// меркой, он разойдётся с тем, по чему принято решение, — так и
	// случилось 8 сентября
	return e.PromptClass(prompt)
}
