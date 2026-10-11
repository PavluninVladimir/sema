// Package mail — работа с почтовым ящиком по IMAP (emersion/go-imap v2).
// Соединение открывается на один сеанс работы и закрывается — так проще
// переживать обрывы сети и рестарты сервера.
package mail

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

// TokenSource выдаёт живой OAuth access-токен (nil — вход по паролю).
type TokenSource func(ctx context.Context) (string, error)

type Fetched struct {
	UID       uint32
	MessageID string
	FromAddr  string
	FromName  string
	Subject   string
	Date      time.Time
	Snippet   string

	// конверт письма. До 10 сентября 2026 Сёма его выбрасывала: в базу
	// ехали только отправитель, тема и дата. Из-за этого «есть ли наш
	// ответ» проверялось совпадением темы — то есть угадыванием, а цепочка
	// писем собиралась по той же теме и путала разные разговоры.
	InReplyTo []string // на какое письмо это ответ
	Refs      []string // вся цепочка (заголовок References)
	ReplyTo   string   // куда отвечать, если не отправителю
	To        []string // кому — по нему видно, письмо лично вам или в рассылку
	Cc        []string
	ListUnsub string // List-Unsubscribe: как отписаться, если это рассылка
}

type Session struct {
	c    *imapclient.Client
	conf config.Mail
	junk string // реальное имя папки спама на сервере
	// envIndex — Message-ID → UID по папкам для серверов, у которых SEARCH
	// по заголовку не работает (Яндекс отвечает «Backend error»)
	envIndex map[string]map[string]uint32
}

func Dial(conf config.Mail, tokens TokenSource) (*Session, error) {
	addr := fmt.Sprintf("%s:%d", conf.IMAPHost, conf.IMAPPort)
	c, err := imapclient.DialTLS(addr, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	if tokens != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		token, err := tokens(ctx)
		cancel()
		if err == nil {
			err = c.Authenticate(&xoauth2{user: conf.Username, token: token})
		}
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("oauth login %s: %w", conf.Username, err)
		}
	} else if err := c.Login(conf.Username, conf.IMAPPassword()).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("login %s: %w", conf.Username, err)
	}
	s := &Session{c: c, conf: conf}
	s.junk = s.detectJunk()
	return s, nil
}

// detectJunk ищет папку спама по IMAP-атрибуту \Junk (SPECIAL-USE) —
// имя зависит от хостинга и языка аккаунта ("[Gmail]/Спам", "Spam",
// "INBOX.Junk"...). Конфиг остаётся запасным вариантом.
func (s *Session) detectJunk() string {
	boxes, err := s.c.List("", "*", nil).Collect()
	if err == nil {
		for _, b := range boxes {
			for _, a := range b.Attrs {
				if a == imap.MailboxAttrJunk {
					return b.Mailbox
				}
			}
		}
	}
	return s.conf.JunkFolder
}

// JunkFolder — имя папки спама, которое реально используется в сеансе.
func (s *Session) JunkFolder() string { return s.junk }

// xoauth2 — SASL-механизм XOAUTH2 (Gmail); в go-sasl его нет, но он тривиален.
type xoauth2 struct{ user, token string }

func (x *xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

// При ошибке Gmail присылает challenge с JSON-описанием; по протоколу
// нужно ответить пустой строкой, после чего сервер вернёт NO.
func (x *xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }

var _ sasl.Client = (*xoauth2)(nil)

func (s *Session) Close() {
	_ = s.c.Logout().Wait()
	_ = s.c.Close()
}

// FetchInbox возвращает письма из INBOX за последние fetch_window_days.
func (s *Session) FetchInbox() ([]*Fetched, error) {
	if _, err := s.c.Select("INBOX", nil).Wait(); err != nil {
		return nil, fmt.Errorf("select INBOX: %w", err)
	}
	// отрицательное окно — «за всё время»: без ограничения по дате
	criteria := &imap.SearchCriteria{}
	if s.conf.FetchWindowDays > 0 {
		criteria.Since = time.Now().AddDate(0, 0, -s.conf.FetchWindowDays)
	}
	sr, err := s.c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return s.FetchByUIDs(sr.AllUIDs())
}

// AllInboxUIDs — все UID писем в INBOX, от новых к старым.
func (s *Session) AllInboxUIDs() ([]imap.UID, error) {
	if _, err := s.c.Select("INBOX", nil).Wait(); err != nil {
		return nil, fmt.Errorf("select INBOX: %w", err)
	}
	sr, err := s.c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search all: %w", err)
	}
	uids := sr.AllUIDs()
	// UID растут со временем — разворачиваем, чтобы идти от свежих
	for i, j := 0, len(uids)-1; i < j; i, j = i+1, j-1 {
		uids[i], uids[j] = uids[j], uids[i]
	}
	return uids, nil
}

// FetchByUIDs скачивает конверты и фрагменты тел; крупные наборы бьются на
// пачки по 50 — на больших FETCH Gmail отвечает "NO System Error".
func (s *Session) FetchByUIDs(uids []imap.UID) ([]*Fetched, error) {
	var out []*Fetched
	for len(uids) > 0 {
		chunk := uids
		if len(chunk) > 50 {
			chunk = chunk[:50]
		}
		uids = uids[len(chunk):]

		section := &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierText,
			Partial:   &imap.SectionPartial{Offset: 0, Size: 4096},
			Peek:      true,
		}
		// заголовки, которых нет в конверте, берём той же выборкой: второй
		// заход к серверу за двумя строчками стоил бы дороже самих строчек
		header := &imap.FetchItemBodySection{
			Specifier:    imap.PartSpecifierHeader,
			HeaderFields: []string{"References", "List-Unsubscribe"},
			Peek:         true,
		}
		msgs, err := s.c.Fetch(imap.UIDSetNum(chunk...), &imap.FetchOptions{
			UID:         true,
			Envelope:    true,
			BodySection: []*imap.FetchItemBodySection{section, header},
		}).Collect()
		if err != nil {
			return out, fmt.Errorf("fetch: %w", err)
		}
		for _, m := range msgs {
			if m.Envelope == nil {
				continue
			}
			f := &Fetched{
				UID:       uint32(m.UID),
				MessageID: m.Envelope.MessageID,
				Subject:   m.Envelope.Subject,
				Date:      m.Envelope.Date,
				Snippet:   cleanSnippet(string(m.FindBodySection(section))),
			}
			if len(m.Envelope.From) > 0 {
				f.FromAddr = m.Envelope.From[0].Addr()
				f.FromName = m.Envelope.From[0].Name
			}
			f.InReplyTo = cleanIDs(m.Envelope.InReplyTo)
			f.To = addrList(m.Envelope.To)
			f.Cc = addrList(m.Envelope.Cc)
			if r := addrList(m.Envelope.ReplyTo); len(r) > 0 {
				f.ReplyTo = r[0]
			}
			f.Refs, f.ListUnsub = parseHeader(string(m.FindBodySection(header)))
			if f.MessageID == "" {
				// без Message-ID дедупликация невозможна — синтезируем стабильный ключ
				f.MessageID = fmt.Sprintf("<sema-%d-%s>", f.UID, f.FromAddr)
			}
			out = append(out, f)
		}
	}
	return out, nil
}

// addrList — список адресов конверта строками. Имена отбрасываем: в базе
// нужен адрес, а имя у каждого письма своё и всё равно ненадёжно.
func addrList(list []imap.Address) []string {
	var out []string
	for _, a := range list {
		if s := strings.ToLower(strings.TrimSpace(a.Addr())); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cleanIDs — Message-ID без угловых скобок и пустых значений. Яндекс
// отдаёт их без скобок, Gmail — со скобками; сравнивать надо одинаково.
func cleanIDs(list []string) []string {
	var out []string
	for _, s := range list {
		for _, part := range strings.Fields(s) {
			part = strings.Trim(strings.TrimSpace(part), "<>")
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// parseHeader — References и List-Unsubscribe из куска заголовков.
// Разбираем руками, а не почтовым разборщиком: здесь всего две строки, а
// заголовок может приехать оборванным на середине.
func parseHeader(raw string) (refs []string, unsub string) {
	var field, value string
	flush := func() {
		switch strings.ToLower(field) {
		case "references":
			refs = append(refs, cleanIDs([]string{value})...)
		case "list-unsubscribe":
			unsub = strings.TrimSpace(value)
		}
		field, value = "", ""
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// продолжение длинного заголовка начинается с пробела
		if line[0] == ' ' || line[0] == '\t' {
			value += " " + strings.TrimSpace(line)
			continue
		}
		flush()
		if i := strings.Index(line, ":"); i > 0 {
			field, value = line[:i], strings.TrimSpace(line[i+1:])
		}
	}
	flush()
	return refs, unsub
}

// FetchFullText скачивает полный текст письма (для просмотра цепочки).
func (s *Session) FetchFullText(uid uint32) (string, error) {
	m, err := s.FetchFullTexts([]uint32{uid})
	if err != nil {
		return "", err
	}
	return m[uid], nil
}

// FetchFullTexts скачивает полные тексты нескольких писем одним сеансом
// (вся цепочка разворачивается на месте без дозагрузок).
func (s *Session) FetchFullTexts(uids []uint32) (map[uint32]string, error) {
	if _, err := s.c.Select("INBOX", nil).Wait(); err != nil {
		return nil, err
	}
	section := &imap.FetchItemBodySection{
		Specifier: imap.PartSpecifierText,
		Partial:   &imap.SectionPartial{Offset: 0, Size: 256 << 10},
		Peek:      true,
	}
	set := make([]imap.UID, len(uids))
	for i, u := range uids {
		set[i] = imap.UID(u)
	}
	msgs, err := s.c.Fetch(imap.UIDSetNum(set...), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil, err
	}
	out := make(map[uint32]string, len(msgs))
	for _, m := range msgs {
		out[uint32(m.UID)] = cleanBody(string(m.FindBodySection(section)))
	}
	return out, nil
}

// cleanBody — как cleanSnippet, но сохраняет абзацы и не режет длину так жёстко.
func cleanBody(raw string) string {
	s := maybeDecodeBase64(raw)
	s = reTags.ReplaceAllString(s, " ")
	s = reQP.ReplaceAllString(s, " ")
	s = reWSKeepNL.ReplaceAllString(s, " ")
	s = reManyNL.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	if len(s) > 8000 {
		s = s[:8000] + "…"
	}
	return s
}

var (
	// неразрывный пробел — тоже пробел: рассыльщики набивают им преамбулу
	// («&nbsp;&zwnj;» по три сотни подряд), и без него схлопывать нечего
	reWSKeepNL = regexp.MustCompile("[ \t\u00a0\u2007\u202f]+")
	reManyNL   = regexp.MustCompile(`\s*\n\s*\n[\s\n]*`)
)

// MoveToJunk переносит письмо из INBOX в папку спама.
func (s *Session) MoveToJunk(uid uint32) error {
	if _, err := s.c.Select("INBOX", nil).Wait(); err != nil {
		return err
	}
	_, err := s.c.Move(imap.UIDSetNum(imap.UID(uid)), s.junk).Wait()
	return err
}

// MoveToJunkByID переносит письмо, находя его по Message-ID: UID письма
// меняется при любом переносе между папками, и MOVE по устаревшему UID
// IMAP выполняет «успешно», ничего не сделав.
// Возвращает: перенесено ли, и новый UID письма в INBOX (0 — не найдено).
func (s *Session) MoveToJunkByID(messageID string, fallbackUID uint32) (bool, error) {
	box := "INBOX"
	uid, ok := s.FindUIDByMessageID(box, messageID)
	if !ok {
		// во «Входящих» письма нет — возможно, оно уже в спаме
		if _, inJunk := s.FindUIDByMessageID(s.junk, messageID); inJunk {
			return true, nil
		}
		// либо лежит в другой папке: разбор папок уносит письма в архив,
		// да и человек волен переложить письмо руками
		if found, u, located := s.Locate(messageID); located {
			box, uid, ok = found, u, true
		}
	}
	if !ok {
		if fallbackUID == 0 {
			return false, nil
		}
		uid = fallbackUID
	}
	if _, err := s.c.Select(box, nil).Wait(); err != nil {
		return false, err
	}
	if _, err := s.c.Move(imap.UIDSetNum(imap.UID(uid)), s.junk).Wait(); err != nil {
		return false, err
	}
	// проверяем, что письмо действительно уехало
	if _, still := s.FindUIDByMessageID(box, messageID); still {
		return false, fmt.Errorf("письмо осталось в папке %q", box)
	}
	return true, nil
}

// MoveFromJunk возвращает письмо из папки спама во «Входящие».
func (s *Session) MoveFromJunk(messageID string) (bool, error) {
	uid, ok := s.FindUIDByMessageID(s.junk, messageID)
	if !ok {
		return false, nil
	}
	if _, err := s.c.Select(s.junk, nil).Wait(); err != nil {
		return false, err
	}
	if _, err := s.c.Move(imap.UIDSetNum(imap.UID(uid)), "INBOX").Wait(); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteFromJunk окончательно удаляет письмо из Junk по Message-ID.
// Возвращает false, если письмо там уже не найдено (удалили руками).
// DeleteFromJunk — удалить письмо из папки спама почтового сервера.
// У Сёмы копия остаётся навсегда: удаление касается только сервера.
func (s *Session) DeleteFromJunk(messageID string) (bool, error) {
	return s.DeleteFromFolder(s.junk, messageID)
}

// DeleteFromFolder — то же для любой папки.
//
// Письмо ищется через FindUIDByMessageID, а не прямым «UID SEARCH HEADER
// Message-ID»: Яндекс на такой поиск отвечает «NO [UNAVAILABLE] Backend
// error», и удаление у него не находило ни одного письма — молча, как
// будто чистить нечего. Та же грабля уже ломала Locate и воркер
// вложений.
func (s *Session) DeleteFromFolder(folder, messageID string) (bool, error) {
	if folder == "" {
		return false, fmt.Errorf("папка не названа")
	}
	if _, err := s.c.Select(folder, nil).Wait(); err != nil {
		return false, err
	}
	uid, ok := s.FindUIDByMessageID(folder, messageID)
	if !ok {
		return false, nil
	}
	uids := []imap.UID{imap.UID(uid)}
	set := imap.UIDSetNum(uids...)
	if err := s.c.Store(set, &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagDeleted},
	}, nil).Close(); err != nil {
		return false, err
	}
	if err := s.c.Expunge().Close(); err != nil {
		return false, err
	}
	return true, nil
}

var (
	reTags   = regexp.MustCompile(`(?s)<(style|script)[^>]*>.*?</(style|script)>|<[^>]+>`)
	reQP     = regexp.MustCompile(`=[0-9A-F]{2}|=\r?\n`)
	reSpaces = regexp.MustCompile(`\s+`)
	// граница части письма: «--boundary_123» или «----=_NextPart_…»
	reBoundary   = regexp.MustCompile(`^-{2,}[A-Za-z0-9'()+_,./:=?-]{4,}-{0,2}$`)
	reHeaderLine = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{1,40}:\s`)
	// Заголовки и границы частей, когда переносов строк уже нет: превью
	// сохранялось склеенным в одну строку, и построчная чистка его не
	// берёт. Значение заголовка — до следующего заголовка или конца.
	reInlineMIME = regexp.MustCompile(`(?is)-{2,}[A-Za-z0-9._=/+-]{6,}|` +
		`\b(?:Content-Type|Content-Transfer-Encoding|Content-Disposition|Content-ID|Content-Length|MIME-Version)\s*:\s*[^\s;]*(?:\s*;\s*[a-z-]+\s*=\s*(?:"[^"]*"|[^\s;]+))*`)
	// не-цифры внутри разорванной сущности
	reNotDigits = regexp.MustCompile(`[^0-9]`)
	// числовая сущность, разбитая переносом: «&#1 086;», «& #1077;»
	reBrokenEntity = regexp.MustCompile(`&\s*#\s*([0-9][0-9\s]{0,8})\s*;`)
	// правила CSS, оставшиеся без тега <style>: «@viewport {…}», «body {…}»
	reCSSLeft = regexp.MustCompile(`(?s)(@[a-z-]+\s*\{[^}]*\}|[.#:@a-zA-Z][^{}<>\n]{0,80}\{[^{}]{0,400}\})`)
	// комментарии CSS и их обрывки: рассыльщики режут стиль на куски, и в
	// превью приезжает «/* /* /* These are our tablet…». Закрытые убираем
	// целиком, незакрытый — вместе с хвостом строки
	reCSSComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reCSSTail    = regexp.MustCompile(`(?s)/\*.*$`)
	// правило, у которого нет закрывающей скобки: «@media screen and
	// (max-width: 799px) { .display-td…» — тоже мусор до конца строки
	reAtTail = regexp.MustCompile(`(?s)@[a-z-]+[^{]{0,200}\{.*$`)
	// остатки условной разметки Office: <w:WordDocument>, xmlns:w и то,
	// что от них осталось после снятия тегов
	reOffice = regexp.MustCompile(`(?is)<[/]?[a-z]+:[^>]*>|xmlns:[a-z]+\b[^\s]*|\bNormal\b(?:\s+MicrosoftInternetExplorer\d*)?`)
	// класс Outlook без точки: точку съела чистка тегов, осталось
	// «ExternalClass, ExternalClas [ [ [» — это тоже вёрстка
	reOutlook = regexp.MustCompile(`(?i)[.#]?\bExternalClass\w*\b`)
	// обломок стиля без скобок: «… 96 @media (prefers-colo» или
	// «.ExternalClass, .ExternalClass p, @med». Правило ищем только там,
	// где @ стоит отдельным словом: в «info@shop.example» его трогать нельзя
	reCSSFrag = regexp.MustCompile(`(?s)(^|\s)(@[a-z-]{2,}|[.#][A-Za-z][\w-]*\s*[,{])`)

	// Обломок вёрстки без единого правила в скобках: незакрытое «{…»
	// таблицы стилей и хвост атрибутов тега, у которого само имя уже
	// снято как «<table». Ни то, ни другое не текст письма; превью,
	// собранное из такого, показывать незачем — останется тема.
	reMarkup = regexp.MustCompile(`(?i)(^\s*\{|\bmso-[a-z-]+\s*:|\bborder-collapse\s*:|^\s*(?:[\w-]+\s+)?[\w-]+="[^"]*"(?:\s+[\w-]+="[^"]*"){1,})`)
	// незакрытый тег в конце превью: «<w:WordDocument "» — закрывающей
	// скобки нет, и обычная чистка тегов его не видит
	reOpenTag = regexp.MustCompile(`(?s)<[a-zA-Z/][^>]*$`)
	// хвост условной разметки Office после снятия тегов: «= table , 96»,
	// «table , 0», «96 x 96» — числа и слова вёрстки без единого русского
	// или латинского слова длиннее трёх букв в начале превью
	reOfficeTail = regexp.MustCompile(`(?i)^[\s=,;:0-9]*(?:table|tr|td|div|span|font|width|height|border|cellpadding|cellspacing|x)?[\s=,;:0-9]*`)
)

// cleanSnippet грубо превращает кусок MIME-тела в читаемый текст для LLM:
// режем HTML, артефакты quoted-printable и лишние пробелы.
func cleanSnippet(raw string) string {
	s := maybeDecodeBase64(raw)
	// комментарии — до тегов, иначе «<[^>]+>» разберёт условный
	// комментарий на части и выпустит наружу его начинку
	s = reComment.ReplaceAllString(s, " ")
	s = reCondTail.ReplaceAllString(s, " ")
	s = stripMIME(s)                          // границы частей и заголовки — не текст письма
	s = reInlineMIME.ReplaceAllString(s, " ") // то же в уже склеенной строке
	s = reTags.ReplaceAllString(s, " ")
	s = reOffice.ReplaceAllString(s, " ") // <w:WordDocument> и его обломки
	s = reOpenTag.ReplaceAllString(s, " ")
	// правила CSS бывают вложенными (@media { .wrap { … } }) — чистим,
	// пока чистится, но не больше трёх проходов
	for i := 0; i < 3; i++ {
		next := reCSSLeft.ReplaceAllString(s, " ")
		if next == s {
			break
		}
		s = next
	}
	s = reQP.ReplaceAllString(s, " ")
	// комментарии стиля и незакрытые правила: сначала закрытые, потом
	// хвосты. Порядок важен — незакрытый «/*» съел бы весь текст письма,
	// если бы шёл первым
	s = reCSSComment.ReplaceAllString(s, " ")
	s = reAtTail.ReplaceAllString(s, " ")
	s = reCSSTail.ReplaceAllString(s, " ")
	// начало превью из обломков вёрстки срезаем один раз: дальше идёт
	// уже текст письма, и трогать его нельзя
	if m := reOfficeTail.FindString(s); m != "" && len(m) < len(s) {
		s = s[len(m):]
	}
	// сущности HTML: в превью уезжало «&nbsp;&#847;&zwnj;» вместо текста.
	// Разворачиваем их и выкидываем невидимые знаки, которыми рассыльщики
	// разбивают буквы, чтобы обмануть фильтры
	// сущности бывают разорваны переносом строки письма: «Пр&#1 086;зрачность»
	// — сначала склеиваем их, иначе разворот не сработает
	s = reBrokenEntity.ReplaceAllStringFunc(s, func(m string) string {
		digits := reNotDigits.ReplaceAllString(m, "")
		return "&#" + digits + ";"
	})
	s = html.UnescapeString(s)
	s = reInvisible.ReplaceAllString(s, "")
	// то, что осталось от разметки после разворота сущностей (&lt;td …&gt;)
	s = reTags.ReplaceAllString(s, " ")
	s = reSpaces.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	// обломок стиля без скобок режем вместе с хвостом: то, что до него, —
	// это и есть текст письма
	if m := reOutlook.FindStringIndex(s); m != nil {
		s = strings.TrimSpace(s[:m[0]])
	}
	if m := reCSSFrag.FindStringIndex(s); m != nil {
		s = strings.TrimSpace(s[:m[0]])
	}
	// превью без единой буквы — это не текст, а знаки препинания, оставшиеся
	// от вёрстки: «! ? ! , , . . ? , + . , , , =». Показывать такое незачем
	if !hasLetters(s) {
		return ""
	}
	// буквы есть, но это имена свойств CSS и атрибутов тега — тоже не текст
	if reMarkup.MatchString(s) {
		return ""
	}
	if len(s) > 1500 {
		s = s[:1500]
	}
	return s
}

// lettersIn — есть ли в строке хоть одна буква. Знаки препинания и цифры
// за текст не считаются.
// буквВСтроке — сколько в строке настоящих букв. По ним и сравниваем две
// версии текста письма: длина обманывает — набивка длинная, а букв в ней нет.
func lettersIn(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

func hasLetters(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// CleanSnippet — та же чистка снаружи пакета: ею подчищаются уже
// сохранённые превью.
func CleanSnippet(raw string) string { return cleanSnippet(raw) }

// stripMIME убирает то, что относится к устройству письма, а не к его
// смыслу: строки-границы частей и заголовки этих частей. Иначе в превью
// (и в контекст модели, и в вектор) уезжает «----boundary_… Content-Type:
// text/html; charset=utf-8» вместо текста.
func stripMIME(s string) string {
	if !strings.Contains(s, "Content-Type:") && !strings.Contains(s, "--") {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inHeaders := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case reBoundary.MatchString(t):
			inHeaders = true // дальше идут заголовки части
			continue
		case inHeaders && t == "":
			inHeaders = false // пустая строка — конец заголовков
			continue
		case inHeaders && reHeaderLine.MatchString(t):
			continue
		case inHeaders && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			continue // продолжение заголовка
		}
		inHeaders = false
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
