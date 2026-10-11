package engine

// Разовая уборка в архиве писем.
//
// 10 сентября 2026 в тексте письма ECCO стояло «4 дня –15% … · 96 ·
// Успейте купить …», а дальше три сотни «&zwnj;». Разбор чинён в
// источнике (комментарии, набивка сущностями, знаки-пустышки), но
// полторы тысячи писем уже лежат в архиве с прежним мусором — а по
// этому тексту Сёма судит, ищет и отвечает.
//
// HTML не трогаем никогда: это архивная копия письма, из неё текст
// всегда можно собрать заново. Переписываем только выжимку.

import (
	"log"

	"github.com/PavluninVladimir/sema/server/internal/mail"
)

// Body — текст и HTML письма в пределах этой уборки.
type Body struct{ Text, HTML string }

const bodyRepairKey = "mail.bodies_clean"

// bodyRepairVersion растёт вместе со строгостью чистки: подняли —
// уборка проходит ещё раз по тем письмам, что подходят под приметы.
const bodyRepairVersion = "6"

func (e *Engine) repairBodies() {
	if e.store.Setting(bodyRepairKey, "") == bodyRepairVersion {
		return
	}
	ids, err := e.store.BodiesWithHTML(20000)
	if err != nil {
		log.Printf("тексты писем: %v", err)
		return
	}
	if len(ids) == 0 {
		_ = e.store.SetSetting(bodyRepairKey, bodyRepairVersion)
		return
	}
	// пачками: список «IN (?,?,…)» на полторы тысячи мест упирается в
	// предел числа переменных SQLite, и весь проход тогда молча
	// пропускает то, что в него не влезло
	bodies := make(map[int64]Body, len(ids))
	for len(ids) > 0 {
		batch := ids
		if len(batch) > 400 {
			batch = batch[:400]
		}
		ids = ids[len(batch):]
		part, err := e.store.Bodies(batch)
		if err != nil {
			log.Printf("тексты писем: %v", err)
			return
		}
		for id, b := range part {
			bodies[id] = Body{Text: b.Text, HTML: b.HTML}
		}
	}
	fixed, preview := 0, 0
	rebuild := func(id int64, text string) {
		// превью собрано из текста письма — пересобираем следом, иначе
		// в списке писем мусор останется, хотя сам текст уже чист.
		// Делаем это и для тел, которые не менялись: их текст мог быть
		// починен прошлым проходом, а превью тогда не тронули.
		m, err := e.store.Message(id)
		if err != nil || m == nil {
			return
		}
		was := m.Snippet
		e.store.CleanSnippet(m, text)
		if fresh, err := e.store.Message(id); err == nil && fresh != nil && fresh.Snippet != was {
			preview++
		}
	}
	for id, b := range bodies {
		if b.HTML == "" {
			if b.Text != "" {
				rebuild(id, b.Text)
			}
			continue
		}
		fresh := mail.TextFromHTML(b.HTML)
		// пусто после чистки — оставляем как было: письмо без текста
		// хуже письма с мусором, а из HTML он может и не доставаться
		if fresh == "" || fresh == b.Text {
			// текст уже такой, каким его собрал бы нынешний разбор, —
			// но превью могло остаться от прежнего
			if b.Text != "" {
				rebuild(id, b.Text)
			}
			continue
		}
		// У части писем сохранена текстовая часть от отправителя, и она
		// бывает лучше нашей выжимки — тогда переписывать нельзя.
		// Сравниваем по буквам, но с запасом в десятую часть: у письма
		// Альфа-Банка наша выжимка была короче на четырнадцать букв из
		// пятисот восьмидесяти, и строгое «не меньше» отвергало чистку
		// из-за разницы в два процента. Плохая выжимка теряет не
		// проценты, а большую часть текста.
		if mail.LetterCount(fresh)*10 < mail.LetterCount(b.Text)*9 {
			rebuild(id, b.Text)
			continue
		}
		if err := e.store.FixBody(id, fresh); err != nil {
			log.Printf("текст письма %d: %v", id, err)
			continue
		}
		fixed++
		rebuild(id, fresh)
	}
	log.Printf("тексты писем: взято %d, почищено %d, превью пересобрано %d", len(bodies), fixed, preview)
	_ = e.store.SetSetting(bodyRepairKey, bodyRepairVersion)
}
