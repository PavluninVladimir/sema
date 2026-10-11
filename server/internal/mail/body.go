package mail

import (
	"encoding/base64"
	"fmt"
	htmlpkg "html"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"golang.org/x/text/encoding/charmap"
)

// FetchBestTexts достаёт читаемые тексты писем: по BODYSTRUCTURE выбирается
// текстовая часть (text/plain, иначе text/html), раскодируются
// transfer-encoding (base64 / quoted-printable) и кодировка (utf-8,
// windows-1251, koi8-r), HTML очищается до текста.
// RichBody — обе версии письма: чистый текст и безопасный HTML.
type RichBody struct {
	Text string
	HTML string
}

func (s *Session) FetchRich(uids []uint32) (map[uint32]RichBody, error) {
	return s.FetchRichIn("INBOX", uids)
}

// FetchRichIn качает из папки текстовую и HTML-части каждого письма,
// декодирует transfer-encoding и кодировку, HTML санитизирует.
func (s *Session) FetchRichIn(mailbox string, uids []uint32) (map[uint32]RichBody, error) {
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return nil, err
	}
	set := make([]imap.UID, len(uids))
	for i, u := range uids {
		set[i] = imap.UID(u)
	}
	structs, err := s.c.Fetch(imap.UIDSetNum(set...), &imap.FetchOptions{
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, err
	}
	logDbg("bestparts: asked %d uids, got %d structs", len(uids), len(structs))

	type pick struct {
		path     []int
		encoding string
		charset  string
		found    bool
	}
	fetchPart := func(uid imap.UID, p pick) (string, bool) {
		section := &imap.FetchItemBodySection{Peek: true}
		if len(p.path) > 0 {
			section.Part = p.path
		} else {
			section.Specifier = imap.PartSpecifierText
		}
		section.Partial = &imap.SectionPartial{Offset: 0, Size: 512 << 10}
		fm, err := s.c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
			UID:         true,
			BodySection: []*imap.FetchItemBodySection{section},
		}).Collect()
		if err != nil || len(fm) == 0 {
			return "", false
		}
		raw, err := decodeBody(fm[0].FindBodySection(section), p.encoding)
		if err != nil {
			logDbg("part uid=%d: decode: %v", uid, err)
			return "", false
		}
		return decodeCharset(raw, p.charset), true
	}

	out := make(map[uint32]RichBody, len(structs))
	for _, m := range structs {
		if m.BodyStructure == nil {
			continue
		}
		var plain, html pick
		m.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
			sp, ok := part.(*imap.BodyStructureSinglePart)
			if !ok || sp.Filename() != "" || !strings.EqualFold(sp.Type, "text") {
				return true
			}
			charset := ""
			for k, v := range sp.Params {
				if strings.EqualFold(k, "charset") {
					charset = v
				}
			}
			p := pick{path: append([]int(nil), path...), encoding: strings.ToLower(sp.Encoding), charset: charset, found: true}
			if strings.EqualFold(sp.Subtype, "plain") && !plain.found {
				plain = p
			}
			if strings.EqualFold(sp.Subtype, "html") && !html.found {
				html = p
			}
			return true
		})
		var rb RichBody
		if html.found {
			if h, ok := fetchPart(m.UID, html); ok {
				// храним оригинал: чистка — дело показа, а ошибка в ней
				// не должна навсегда портить архивную копию
				rb.HTML = h
				rb.Text = tidyText(stripHTML(h), 8000)
			}
		}
		if plain.found {
			if t, ok := fetchPart(m.UID, plain); ok {
				text := tidyText(t, 8000)
				// Текстовую часть письма рассыльщик нередко собирает
				// наивным снятием тегов: в ней остаются куски служебных
				// блоков и набивка. Если после чистки букв в ней вдвое
				// меньше, чем в нашей выжимке из HTML, — своя честнее.
				if rb.Text == "" || lettersIn(text)*2 >= lettersIn(rb.Text) {
					rb.Text = text
				}
			}
		}
		if rb.Text != "" || rb.HTML != "" {
			out[uint32(m.UID)] = rb
		}
	}
	return out, nil
}

var (
	reScriptTag = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<script[^>]*/>`)
	reOnAttr    = regexp.MustCompile(`(?i)\son[a-z]+\s*=\s*("[^"]*"|'[^']*')`)
	// «(?:^|[^-\w])» — чтобы не пометить второй раз уже помеченное
	reNetSrc = regexp.MustCompile(`(?i)([^-\w])(src|srcset|background)(\s*=\s*["'])(https?:|//)`)
	reCSSURL = regexp.MustCompile(`(?i)(url\(\s*['"]?)(https?:|//)`)
)

// SanitizeHTML — тот же санитайзер для копий, сохранённых раньше
// (правила блокировки со временем становятся строже).
func SanitizeHTML(s string) string { return sanitizeHTML(s) }

var (
	// парные теги — с содержимым до своего закрытия, одиночные — сами по
	// себе: одна общая группа «до ближайшего </object>» съедала тело письма
	reExternalTag = regexp.MustCompile(`(?is)<iframe\b[^>]*>.*?</iframe\s*>|<object\b[^>]*>.*?</object\s*>|<(?:link|embed|base|iframe|object)\b[^>]*>`)
	reMetaRefresh = regexp.MustCompile(`(?is)<meta\b[^>]*http-equiv\s*=\s*["']?refresh[^>]*>`)
	reCSSImport   = regexp.MustCompile(`(?is)@import\b[^;]*;?`)
)

// sanitizeHTML готовит HTML к показу в приложении: без скриптов,
// обработчиков и сетевых загрузок (трекинг-пиксели не сработают).
func sanitizeHTML(s string) string {
	s = reScriptTag.ReplaceAllString(s, "")
	s = reOnAttr.ReplaceAllString(s, "")
	// <link>, @import и <meta refresh> тянут чужое и уводят со страницы
	// без ведома человека; <iframe>, <object>, <embed>, <base> — чужой
	// документ внутри письма. Их не блокируем, а убираем
	s = reExternalTag.ReplaceAllString(s, "")
	s = reMetaRefresh.ReplaceAllString(s, "")
	s = reCSSImport.ReplaceAllString(s, "")
	// картинки не загружаем, пока человек не попросит: иначе отправитель
	// узнает о прочтении по трекинг-пикселю
	s = reNetSrc.ReplaceAllString(s, `${1}data-blocked-$2$3$4`)
	s = reCSSURL.ReplaceAllString(s, `${1}x-blocked-$2`) // фоны в CSS — тоже
	if len(s) > 512<<10 {
		s = s[:512<<10]
	}
	return s
}

func decodeCharset(b []byte, charset string) string {
	var cm *charmap.Charmap
	switch strings.ToLower(charset) {
	case "windows-1251", "cp1251":
		cm = charmap.Windows1251
	case "koi8-r":
		cm = charmap.KOI8R
	case "iso-8859-1", "latin1":
		cm = charmap.ISO8859_1
	default:
		// без явной кодировки: валидный UTF-8 (допуская обрезанный последний
		// символ) оставляем как есть, иначе почти наверняка windows-1251
		trimmed := b
		for i := 0; i < 3 && len(trimmed) > 0 && !utf8.Valid(trimmed); i++ {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if utf8.Valid(trimmed) {
			return string(trimmed)
		}
		cm = charmap.Windows1251
	}
	out, err := cm.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
}

var (
	reStyleScript = regexp.MustCompile(`(?is)<(style|script)[^>]*>.*?</(style|script)>`)
	// Комментарий — не текст письма, а его содержимое до 10 сентября 2026
	// уезжало в архив: «<[^>]+>» видит в «<!--[if mso]>…<![endif]-->» два
	// отдельных «тега» и оставляет всё, что между ними. Так в тексте
	// письма ECCO оказалось «96» — из <o:PixelsPerInch>96</o:PixelsPerInch>
	// внутри условного комментария для Outlook.
	reComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	reCondTail = regexp.MustCompile(`(?i)<!\[endif\]-->|<!\[if[^\]]*\]>`)
	reAnyTag   = regexp.MustCompile(`(?s)<[^>]+>`)
	reEntity   = regexp.MustCompile(`&(nbsp|amp|lt|gt|quot|mdash|ndash|laquo|raquo|hellip|#\d+);`)
)

// TextFromHTML — та же выжимка текста из HTML, что делается при приёме
// письма: ею подчищается уже сохранённое, когда чистка стала строже.
func TextFromHTML(html string) string { return tidyText(stripHTML(html), 8000) }

// LetterCount — сколько в строке настоящих букв. По ним сравниваются две
// версии текста письма: длина обманывает — набивка длинная, а букв нет.
func LetterCount(s string) int { return lettersIn(s) }

// CleanText — чистка уже готового текста без разбора HTML: снимает
// набивку и невидимые знаки. Для писем, у которых HTML не сохранился.
func CleanText(text string) string { return tidyText(text, 8000) }

// stripHTML — HTML в текст. Сущности разворачиваем стандартной
// библиотекой, а не своим списком из десяти штук.
//
// Свой список делал две беды сразу. Незнакомая сущность оставалась в
// тексте как есть — «&zwnj;» триста раз подряд, потому что рассыльщики
// набивают ими преамбулу. А числовая превращалась в пробел, и
// «Пр&#1086;зрачность» становилось «Пр зрачность»: буква пропадала совсем,
// и это уже не косметика, а порча текста, по которому Сёма потом судит,
// ищет и отвечает.
//
// Невидимые знаки после разворота убирает tidyText — там же, где он их
// убирал всегда.
func stripHTML(s string) string {
	s = reStyleScript.ReplaceAllString(s, " ")
	// комментарии — раньше тегов: иначе «<[^>]+>» разберёт условный
	// комментарий на части и выпустит наружу его начинку
	s = reComment.ReplaceAllString(s, " ")
	s = reCondTail.ReplaceAllString(s, " ")
	s = strings.NewReplacer("</p>", "\n", "<br>", "\n", "<br/>", "\n", "<br />", "\n", "</div>", "\n", "</tr>", "\n").Replace(s)
	s = reAnyTag.ReplaceAllString(s, " ")
	// сущность бывает разорвана переносом строки письма: «Пр&#1 086;зрачность»
	s = reBrokenEntity.ReplaceAllStringFunc(s, func(m string) string {
		return "&#" + reNotDigits.ReplaceAllString(m, "") + ";"
	})
	s = htmlpkg.UnescapeString(s)
	// то, что было спрятано за сущностями («&lt;td&gt;»), — тоже разметка
	return reAnyTag.ReplaceAllString(s, " ")
}

// Невидимая набивка. Кроме нулевой ширины сюда входят знаки, которые
// рисуются пустотой: брайлевский пробел U+2800 и хангыльский филлер
// U+3164 — ими рассыльщики растягивают преамбулу письма, чтобы в списке
// почтового клиента она занимала больше места. Текстом это не является
// ни в каком письме, которое к Сёме приходит.
var reInvisible = regexp.MustCompile("[\u200b\u200c\u200d\u2060\ufeff\u00ad\u2800\u3164\u115f\u1160]+")

// Та же набивка, записанная сущностями: текстовую часть письма многие
// рассыльщики собирают наивным снятием тегов, и «&zwnj;» доезжает до нас
// буквами, а не знаком. Убираем только повторы — одиночный «&nbsp;» в
// письме про вёрстку законный.
var reEntityPad = regexp.MustCompile(`(?i)(&(?:zwnj|zwj|nbsp|shy|#8203|#8204|#8205|#847|#160|#65279);[ \t]*){2,}`)

func tidyText(s string, max int) string {
	s = reEntityPad.ReplaceAllString(s, " ")
	s = reInvisible.ReplaceAllString(s, "")
	s = reWSKeepNL.ReplaceAllString(s, " ")
	s = reManyNL.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	// режем по рунам, а не по байтам: у русского текста байт вдвое больше
	// букв, и обрезка по байтам делит букву пополам — в базу уезжает
	// битый UTF-8, а с ним и «0KHQv…» в списке писем
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// maybeDecodeBase64 спасает сниппеты писем, у которых всё тело закодировано
// base64 (без разбора структуры): если строка похожа на base64 — декодируем.
var reB64 = regexp.MustCompile(`^[A-Za-z0-9+/=\s]+$`)

func maybeDecodeBase64(s string) string {
	// Раньше требовалось, чтобы вся строка была base64: письмо, где за
	// телом идёт ещё хоть что-то (граница части, подпись), не
	// расшифровывалось вовсе — и в списке писем стояло
	// «0KPQstCw0LbQsNC10LzRi9C5…» вместо текста. Расшифровываем самое
	// длинное начало, похожее на base64, остальное оставляем как есть.
	head, tail := leadingB64(s)
	if len(head) < 80 {
		return s
	}
	clean := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, head)
	clean = clean[:len(clean)-len(clean)%4]
	dec, err := base64.StdEncoding.DecodeString(clean)
	if err != nil || len(dec) == 0 {
		return s
	}
	// Сниппет берётся из обрезанного тела, и обрез приходится куда попало —
	// в середину многобайтовой буквы. Один битый хвост не повод оставлять
	// в списке писем «0KHQv9Cw…»: отбрасываем неполную букву (не больше
	// трёх байт), остальное читается.
	for i := 0; i < 3 && len(dec) > 0 && !utf8.Valid(dec); i++ {
		dec = dec[:len(dec)-1]
	}
	if !utf8.Valid(dec) {
		return s
	}
	text := stripHTML(decodeCharset(dec, ""))
	if tail != "" {
		text += " " + tail
	}
	return text
}

// leadingB64 — начало строки из букв base64 и пробелов, и всё остальное.
func leadingB64(s string) (head, tail string) {
	i := 0
	for i < len(s) {
		c := s[i]
		isB64 := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '+' || c == '/' || c == '=' || c == ' ' || c == '\r' || c == '\n' || c == '\t'
		if !isB64 {
			break
		}
		i++
	}
	return s[:i], strings.TrimSpace(s[i:])
}

func logDbg(f string, a ...any) { log.Printf("[body] "+f, a...) }

// Locate ищет письмо по всему ящику: сначала в ожидаемых папках, потом в
// «Все письма», потом по остальным. Нужно потому, что человек волен
// перекладывать письма руками, а UID меняется при каждом переносе.
func (s *Session) Locate(messageID string, prefer ...string) (mailbox string, uid uint32, ok bool) {
	if messageID == "" {
		return "", 0, false
	}
	tried := map[string]bool{}
	try := func(box string) bool {
		if box == "" || tried[box] {
			return false
		}
		tried[box] = true
		if u, found := s.FindUIDByMessageID(box, messageID); found {
			mailbox, uid, ok = box, u, true
			return true
		}
		return false
	}
	for _, box := range prefer {
		if try(box) {
			return
		}
	}
	if try("INBOX") || try(s.junk) || try(s.allMail()) {
		return
	}
	boxes, err := s.c.List("", "*", nil).Collect()
	if err != nil {
		return "", 0, false
	}
	for _, b := range boxes {
		if isNoSelect(b.Attrs) {
			continue
		}
		if try(b.Mailbox) {
			return
		}
	}
	return "", 0, false
}

// allMail — папка со всей почтой (у Gmail «Все письма»).
func (s *Session) allMail() string {
	boxes, err := s.c.List("", "*", nil).Collect()
	if err != nil {
		return ""
	}
	for _, b := range boxes {
		for _, a := range b.Attrs {
			if a == imap.MailboxAttrAll || a == imap.MailboxAttrArchive {
				return b.Mailbox
			}
		}
	}
	return ""
}

func isNoSelect(attrs []imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == imap.MailboxAttrNoSelect || a == imap.MailboxAttrNonExistent {
			return true
		}
	}
	return false
}

// FindUIDByMessageID ищет письмо в папке по Message-ID (UID меняется при
// переносе между папками, Message-ID — нет).
func (s *Session) FindUIDByMessageID(mailbox, messageID string) (uint32, bool) {
	if messageID == "" {
		return 0, false
	}
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return 0, false
	}
	sr, err := s.c.UIDSearch(&imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}},
	}, nil).Wait()
	if err != nil {
		// сервер не умеет искать по заголовку — читаем конверты папки сами
		return s.findInEnvelopes(mailbox, messageID)
	}
	uids := sr.AllUIDs()
	if len(uids) == 0 {
		return 0, false
	}
	return uint32(uids[0]), true
}

// findInEnvelopes ищет письмо по индексу конвертов папки; индекс строится
// один раз на сеанс и папку (FETCH 1:* UID ENVELOPE), Message-ID сравниваются
// без угловых скобок — Яндекс отдаёт их без, Gmail с ними.
func (s *Session) findInEnvelopes(mailbox, messageID string) (uint32, bool) {
	if s.envIndex == nil {
		s.envIndex = map[string]map[string]uint32{}
	}
	idx, ok := s.envIndex[mailbox]
	if !ok {
		idx = map[string]uint32{}
		s.envIndex[mailbox] = idx
		sel, err := s.c.Select(mailbox, nil).Wait()
		if err != nil || sel.NumMessages == 0 {
			return 0, false
		}
		set := imap.SeqSet{imap.SeqRange{Start: 1, Stop: sel.NumMessages}}
		msgs, err := s.c.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true}).Collect()
		if err != nil {
			logDbg("конверты %s: %v", mailbox, err)
			return 0, false
		}
		for _, m := range msgs {
			if m.Envelope != nil && m.Envelope.MessageID != "" {
				idx[strings.Trim(m.Envelope.MessageID, "<> ")] = uint32(m.UID)
			}
		}
	}
	uid, found := idx[strings.Trim(messageID, "<> ")]
	return uid, found
}

// Sanitize чистит сохранённый сниппет: спасает base64-кашу старых записей.
func Sanitize(s string) string {
	return tidyText(stripHTML(maybeDecodeBase64(s)), 8000)
}

// ProbeMessageID — диагностика поиска: как сервер отвечает на SEARCH по
// Message-ID в разных формах и как выглядят Message-ID последних писем папки.
func (s *Session) ProbeMessageID(mailbox, messageID string) string {
	var b strings.Builder
	sel, err := s.c.Select(mailbox, nil).Wait()
	if err != nil {
		return "select: " + err.Error()
	}
	fmt.Fprintf(&b, "папка %s: %d писем\n", mailbox, sel.NumMessages)
	bare := strings.Trim(messageID, "<>")
	for _, v := range []string{bare, "<" + bare + ">"} {
		sr, err := s.c.UIDSearch(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: v}},
		}, nil).Wait()
		if err != nil {
			fmt.Fprintf(&b, "HEADER %q: ошибка %v\n", v, err)
			continue
		}
		fmt.Fprintf(&b, "HEADER %q: %v\n", v, sr.AllUIDs())
	}
	if sr, err := s.c.UIDSearch(&imap.SearchCriteria{Text: []string{bare}}, nil).Wait(); err == nil {
		fmt.Fprintf(&b, "TEXT: %v\n", sr.AllUIDs())
	} else {
		fmt.Fprintf(&b, "TEXT: ошибка %v\n", err)
	}
	if sel.NumMessages > 0 {
		from := sel.NumMessages - 2
		if sel.NumMessages < 3 {
			from = 1
		}
		set := imap.SeqSet{imap.SeqRange{Start: from, Stop: sel.NumMessages}}
		msgs, err := s.c.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true}).Collect()
		if err == nil {
			for _, m := range msgs {
				if m.Envelope != nil {
					fmt.Fprintf(&b, "uid %d: Message-ID=%q subject=%q\n", m.UID, m.Envelope.MessageID, m.Envelope.Subject)
				}
			}
		}
	}
	return b.String()
}
