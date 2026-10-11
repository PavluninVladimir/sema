package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

// Папки, где решение принял не Сёма, узнаются по имени и по метке IMAP.
//
// 10 сентября 2026 письмо «[Ticket#2026091028000669] Ограничение
// исходящей скорости на VPS» не нашлось поиском, потому что его не было
// в архиве вовсе: спам почтового сервера Сёма не читал никогда.
func TestServerFoldersRecognized(t *testing.T) {
	session := &Session{junk: "Спам"}
	boxes := []Mailbox{
		{Name: "INBOX"},
		{Name: "Archive"},
		{Name: "Спам"},
		{Name: "[Gmail]/Спам"},
		{Name: "Корзина"},
		{Name: "Удаленные"},
		{Name: "[Gmail]/Вся почта", Attrs: []imap.MailboxAttr{imap.MailboxAttrAll}},
		{Name: "Отправленные"},
		{Name: "pending"},
	}
	got := session.serverFoldersFrom(boxes, "Archive")
	want := map[string]string{
		"Спам":              FolderJunk,
		"[Gmail]/Спам":      FolderJunk,
		"Корзина":           FolderTrash,
		"Удаленные":         FolderTrash,
		"[Gmail]/Вся почта": FolderAll,
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%q: %q, а надо %q", name, got[name], kind)
		}
	}
	// «Входящие», архив и обычные папки решает Сёма — их здесь быть не должно
	for _, name := range []string{"INBOX", "Archive", "Отправленные", "pending"} {
		if v, ok := got[name]; ok {
			t.Errorf("%q попала в чужие решения как %q", name, v)
		}
	}
}

// Папка архива не считается «всей почтой», даже если у неё метка \All:
// туда Сёма складывает сам, и читать её как чужую было бы неправдой.
func TestArchiveIsNotForeign(t *testing.T) {
	session := &Session{junk: "Спам"}
	boxes := []Mailbox{{Name: "[Gmail]/Вся почта", Attrs: []imap.MailboxAttr{imap.MailboxAttrAll}}}
	if got := session.serverFoldersFrom(boxes, "[Gmail]/Вся почта"); len(got) != 0 {
		t.Errorf("папка архива объявлена чужой: %v", got)
	}
}
