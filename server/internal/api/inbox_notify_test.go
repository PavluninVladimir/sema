package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// На «Почте» полка «Ждут вас» говорит только про почту. 10 сентября 2026
// на почтовом экране первой строкой стояла команда `crontab -l 2>/dev/null`
// с сервера srv-bots: полка считала все открытые записи подряд.
func TestMailShelfWithoutForeign(t *testing.T) {
	s, st := testServer(t)
	// серверная: пришла из задачи присмотра, ждёт кнопки человека
	if _, err := st.PutNotification(&store.Notification{
		Key: "ssh:1", Source: "core", Level: "warn",
		Title: "srv-bots · crontab -l 2>/dev/null", Summary: "7 записей ждут решения",
	}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if полка := findShelf(doc); полка != nil {
		t.Errorf("полка «Ждут вас» есть, хотя по почте ничего не ждёт: %+v", полка)
	}
	if dump := dumpScreen(doc); strings.Contains(dump, "crontab") {
		t.Error("серверная команда попала на экран «Почта»")
	}

	// а почтовая — на месте, и уровень у полки её
	if _, err := st.PutNotification(&store.Notification{
		Key: "reminder:1", Source: "mail", Level: "alert",
		Title: "Проверить продление домена", Summary: "срок сегодня",
	}); err != nil {
		t.Fatal(err)
	}
	doc, err = s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	полка := findShelf(doc)
	if полка == nil {
		t.Fatal("почтовая запись ждёт, а полки нет")
	}
	if !strings.Contains(полка.Children[0].Subtitle, "продление домена") {
		t.Errorf("на полке не та запись: %q", полка.Children[0].Subtitle)
	}
	if полка.Level != "alert" {
		t.Errorf("уровень полки %q, а ждёт тревога", полка.Level)
	}
	// счёт по уровням, а не по источникам: источник тут всегда один
	if !strings.Contains(полка.Meta, "тревога: 1") {
		t.Errorf("подпись полки %q", полка.Meta)
	}
	if dump := dumpScreen(doc); strings.Contains(dump, "crontab") {
		t.Error("серверная команда всё ещё на экране «Почта»")
	}
}

// dumpScreen — весь документ строкой: ищем чужое не только в полке, но и
// где угодно ещё, куда оно могло просочиться.
func dumpScreen(doc *Document) string {
	b, _ := json.Marshal(doc)
	return string(b)
}

func findShelf(doc *Document) *Component {
	for i := range doc.Components {
		if doc.Components[i].Type == "shelf" && doc.Components[i].Title == "Ждут вас" {
			return &doc.Components[i]
		}
	}
	return nil
}
