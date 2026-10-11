package mail

import "testing"

// «Входящие» разбирает разбор, а не обход папок. Обход кладёт письмо в
// архив как разобранное — для «Входящих» это значит «мимо суда, мимо
// спам-фильтра и молча».
func TestInboxIsNotASourceFolder(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"INBOX", true},
		{"inbox", true},
		{"Inbox", true},
		{" INBOX ", true},
		{"[Gmail]/INBOX", true},
		{"INBOX/Счета", false},  // подпапка — её человек завёл сам
		{"INBOX.Работа", false}, // разделитель Яндекса
		{"Входящие письма", false},
		{"Архив", false},
		{"Счета", false},
	}
	for _, c := range cases {
		if got := IsInbox(c.name); got != c.want {
			t.Errorf("IsInbox(%q) = %v, надо %v", c.name, got, c.want)
		}
	}
}
