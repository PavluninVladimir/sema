package mail

// Обход всех папок ящика: Сёма забирает письма к себе, помечает их
// прочитанными и складывает в папку архива на сервере.
// Виртуальные представления Gmail («Все письма», «Помеченные», «Важные»)
// не папки, а ярлыки-срезы: переносить из них нечего, иначе письмо
// потеряет пометку. Черновики и корзину со спамом тоже не трогаем.

import (
	"fmt"
	"strings"

	"github.com/emersion/go-imap/v2"
)

type Mailbox struct {
	Name  string
	Attrs []imap.MailboxAttr
}

func (m Mailbox) has(want imap.MailboxAttr) bool {
	for _, a := range m.Attrs {
		if a == want {
			return true
		}
	}
	return false
}

// Selectable — в папку можно зайти (в отличие от контейнера вроде «[Gmail]»).
func (m Mailbox) Selectable() bool { return !isNoSelect(m.Attrs) }

// SetJunk задаёт папку спама вручную: у ящика может не быть метки \Junk
// или человек хочет складывать спам в свою папку.
func (s *Session) SetJunk(name string) {
	if name != "" {
		s.junk = name
	}
}

// Mailboxes — все папки ящика.
func (s *Session) Mailboxes() ([]Mailbox, error) {
	boxes, err := s.c.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	out := make([]Mailbox, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, Mailbox{Name: b.Mailbox, Attrs: b.Attrs})
	}
	return out, nil
}

// ArchiveFolder ищет папку архива, ничего не создавая: сначала по атрибуту
// \Archive, потом по имени. Пустая строка — папки нет.
func (s *Session) ArchiveFolder() (string, error) {
	boxes, err := s.Mailboxes()
	if err != nil {
		return "", err
	}
	for _, b := range boxes {
		if b.has(imap.MailboxAttrArchive) {
			return b.Name, nil
		}
	}
	for _, b := range boxes {
		if b.has(imap.MailboxAttrAll) || isNoSelect(b.Attrs) {
			continue
		}
		name := strings.ToLower(b.Name)
		if strings.Contains(name, "archive") || strings.Contains(name, "архив") {
			return b.Name, nil
		}
	}
	return "", nil
}

// EnsureArchive возвращает папку архива, создавая «Archive», если её нет.
func (s *Session) EnsureArchive() (string, error) {
	if box, err := s.ArchiveFolder(); err != nil || box != "" {
		return box, err
	}
	const name = "Archive"
	if err := s.c.Create(name, nil).Wait(); err != nil {
		// папка могла появиться параллельно — это не ошибка
		if _, serr := s.c.Select(name, nil).Wait(); serr != nil {
			return "", fmt.Errorf("create %s: %w", name, err)
		}
	}
	_ = s.c.Subscribe(name).Wait()
	return name, nil
}

// SourceMailboxes — папки, которые нужно разобрать: всё, кроме корзины,
// спама, черновиков, виртуальных срезов, самого архива и «Входящих».
//
// Про «Входящие» отдельно, потому что это стоило Сёме недели разбора.
// Обход папок кладёт письмо в архив «как разобранное»: папки человек
// разложил руками, и судить их незачем. С «Входящими» это неправда —
// там лежит то, что ещё никто не смотрел. А ходит обход раз в двадцать
// секунд против пяти минут у разбора, и потому всегда успевал первым: с
// 2 сентября 2026 сорок одно письмо уехало в архив со статусом
// «archived» и уверенностью 0, включая «Счет от АО "Горэнерго"».
// Ни суда, ни спам-фильтра, ни строки во «Входящих» — молча.
//
// «Входящие» — территория разбора (`FetchInbox`), и он читает их у
// каждого ящика. Здесь их быть не должно ни в каком режиме.
func (s *Session) SourceMailboxes(archive string) ([]string, error) {
	boxes, err := s.Mailboxes()
	if err != nil {
		return nil, err
	}
	skipAttr := []imap.MailboxAttr{
		imap.MailboxAttrTrash, imap.MailboxAttrJunk, imap.MailboxAttrDrafts,
		imap.MailboxAttrAll, imap.MailboxAttrArchive,
		imap.MailboxAttrFlagged, imap.MailboxAttrImportant,
	}
	var out []string
	for _, b := range boxes {
		if isNoSelect(b.Attrs) || b.Name == archive || b.Name == s.junk || IsInbox(b.Name) {
			continue
		}
		skip := false
		for _, a := range skipAttr {
			if b.has(a) {
				skip = true
				break
			}
		}
		name := strings.ToLower(b.Name)
		if strings.Contains(name, "trash") || strings.Contains(name, "корзина") ||
			strings.Contains(name, "spam") || strings.Contains(name, "спам") {
			skip = true
		}
		if !skip {
			out = append(out, b.Name)
		}
	}
	return out, nil
}

// Виды папок, где решение принял не Сёма: спам разложил почтовый сервер,
// корзину и «всю почту» — человек и его правила.
const (
	FolderJunk  = "спам"
	FolderTrash = "корзина"
	FolderAll   = "вся почта"
)

// ServerFolders — папки, которые Сёма раньше не читал вовсе.
//
// 10 сентября 2026 письмо «[Ticket#2026091028000669] Ограничение
// исходящей скорости на VPS» не нашлось поиском, и причина оказалась не
// в поиске: письма не было в архиве. Спам почтового сервера не читался
// никогда, а в Gmail «Вся почта» попадает всё, что помечено «Пропустить
// входящие», — два места, где письмо есть, а Сёма о нём не знает.
//
// Эти папки читаются НА МЕСТЕ: письма не помечаются прочитанными и никуда
// не переносятся. Решение о них принял сервер или человек, и спорить с
// ним Сёма не должен — он только перестаёт быть слепым.
func (s *Session) ServerFolders(archive string) map[string]string {
	boxes, err := s.Mailboxes()
	if err != nil {
		return map[string]string{}
	}
	return s.serverFoldersFrom(boxes, archive)
}

// serverFoldersFrom — та же разборка, но по готовому списку: так её видно
// тестам без живого почтового сервера.
func (s *Session) serverFoldersFrom(boxes []Mailbox, archive string) map[string]string {
	out := map[string]string{}
	for _, b := range boxes {
		if isNoSelect(b.Attrs) || IsInbox(b.Name) {
			continue
		}
		name := strings.ToLower(b.Name)
		switch {
		case b.has(imap.MailboxAttrJunk) || b.Name == s.junk ||
			strings.Contains(name, "spam") || strings.Contains(name, "спам"):
			out[b.Name] = FolderJunk
		case b.has(imap.MailboxAttrTrash) ||
			strings.Contains(name, "trash") || strings.Contains(name, "корзина") ||
			strings.Contains(name, "удален") || strings.Contains(name, "удалён"):
			out[b.Name] = FolderTrash
		case b.has(imap.MailboxAttrAll) && b.Name != archive:
			// «Вся почта» Gmail: сюда уходит всё, что фильтр пометил
			// «Пропустить входящие». Дубли отсеются по Message-ID
			out[b.Name] = FolderAll
		}
	}
	return out
}

// IsInbox — та самая папка, которую читает разбор. Имя INBOX по
// стандарту IMAP не зависит от регистра, а у Gmail и Яндекса встречается
// с приставкой сервера.
func IsInbox(name string) bool {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "INBOX") {
		return true
	}
	// «INBOX/Входящие» — это уже подпапка, её разбирать надо; а вот
	// «[Gmail]/INBOX» и подобное — те же «Входящие»
	if i := strings.LastIndexAny(name, "/."); i >= 0 {
		return strings.EqualFold(strings.TrimSpace(name[i+1:]), "INBOX") && strings.HasPrefix(name, "[")
	}
	return false
}

// UIDsIn — все UID писем в папке, от новых к старым.
func (s *Session) UIDsIn(mailbox string) ([]imap.UID, error) {
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %s: %w", mailbox, err)
	}
	sr, err := s.c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", mailbox, err)
	}
	uids := sr.AllUIDs()
	for i, j := 0, len(uids)-1; i < j; i, j = i+1, j-1 {
		uids[i], uids[j] = uids[j], uids[i]
	}
	return uids, nil
}

// CountIn — сколько писем лежит в папке.
func (s *Session) CountIn(mailbox string) (int, error) {
	data, err := s.c.Select(mailbox, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("select %s: %w", mailbox, err)
	}
	return int(data.NumMessages), nil
}

// FetchIn скачивает конверты писем из указанной папки.
func (s *Session) FetchIn(mailbox string, uids []imap.UID) ([]*Fetched, error) {
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %s: %w", mailbox, err)
	}
	return s.FetchByUIDs(uids)
}

// MarkSeen помечает письма прочитанными.
func (s *Session) MarkSeen(mailbox string, uids []imap.UID) error {
	if len(uids) == 0 {
		return nil
	}
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", mailbox, err)
	}
	return s.c.Store(imap.UIDSetNum(uids...), &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagSeen},
	}, nil).Close()
}

// MoveUIDs переносит письма в другую папку.
func (s *Session) MoveUIDs(mailbox string, uids []imap.UID, dest string) error {
	if len(uids) == 0 || mailbox == dest {
		return nil
	}
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", mailbox, err)
	}
	_, err := s.c.Move(imap.UIDSetNum(uids...), dest).Wait()
	return err
}

// UnseenIn — сколько в папке непрочитанных писем (для диагностики).
func (s *Session) UnseenIn(mailbox string) int {
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return -1
	}
	sr, err := s.c.UIDSearch(&imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}, nil).Wait()
	if err != nil {
		return -1
	}
	return len(sr.AllUIDs())
}

// RawIn отдаёт письмо целиком из указанной папки — для диагностики, когда
// нужно посмотреть, из каких частей оно собрано.
func (s *Session) RawIn(mailbox string, uid uint32) (string, error) {
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return "", fmt.Errorf("select %s: %w", mailbox, err)
	}
	data, err := s.c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{}},
	}).Collect()
	if err != nil {
		return "", err
	}
	for _, msg := range data {
		for _, body := range msg.BodySection {
			if len(body.Bytes) > 0 {
				return string(body.Bytes), nil
			}
		}
	}
	return "", fmt.Errorf("письмо не отдалось")
}

// PartsIn перечисляет части письма так, как их видит Сёма: это ответ на
// вопрос «почему вложение не подхватилось».
func (s *Session) PartsIn(mailbox string, uid uint32) ([]string, error) {
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return nil, err
	}
	msgs, err := s.c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 || msgs[0].BodyStructure == nil {
		return nil, fmt.Errorf("письмо не отдало структуру")
	}
	var out []string
	msgs[0].BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		sp, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			out = append(out, fmt.Sprintf("%v · составная часть", path))
			return true
		}
		disp := "(нет)"
		if d := sp.Disposition(); d != nil {
			disp = d.Value + " " + fmt.Sprint(d.Params)
		}
		out = append(out, fmt.Sprintf("%v · %s/%s · имя=%q · disposition=%s · %d байт",
			path, sp.Type, sp.Subtype, sp.Filename(), disp, sp.Size))
		return true
	})
	return out, nil
}

// SentBoxes — папки с отправленными письмами: по метке \Sent или по имени.
// Их Сёма читает на месте и никуда не переносит: это исходящая переписка.
func (s *Session) SentBoxes() map[string]bool {
	out := map[string]bool{}
	boxes, err := s.Mailboxes()
	if err != nil {
		return out
	}
	for _, b := range boxes {
		name := strings.ToLower(b.Name)
		if b.has(imap.MailboxAttrSent) || strings.Contains(name, "sent") ||
			strings.Contains(name, "отправлен") || strings.Contains(name, "исходящ") {
			out[b.Name] = true
		}
	}
	return out
}
