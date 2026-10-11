package api

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// На экране письма сначала письмо, потом ответ: прежде сверху стояли
// форма ответа, «вернуться позже» и класс чувствительности, а само
// письмо было четвёртым блоком — человек открывал письмо и не видел
// письма (13 сентября 2026).
func TestOrderOnMessageScreen(t *testing.T) {
	s, st := testServer(t)
	m := &store.Message{MessageID: "<порядок@т>", Subject: "Счёт за сентябрь",
		FromAddr: "bill@пример.рф", FromName: "Хостинг", Date: time.Now(),
		Status: store.StatusInboxOK, Account: "проба"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBody(m.ID, store.Body{Text: "текст письма про счёт"}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("msg:"+itoa(m.ID), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// порядок блоков верхнего уровня
	var порядок []string
	for _, c := range doc.Components {
		switch {
		case c.ID == "тело-письма":
			порядок = append(порядок, "письмо")
		case strings.HasPrefix(c.ID, "ответ-"):
			порядок = append(порядок, "ответ")
		case c.ID == "про-письмо":
			порядок = append(порядок, "свойства")
		case strings.HasPrefix(c.Title, "Вложения"):
			порядок = append(порядок, "вложения")
		}
	}
	если := strings.Join(порядок, ",")
	if !strings.HasPrefix(если, "письмо") {
		t.Errorf("первым идёт не письмо: %s", если)
	}
	if i, j := strings.Index(если, "ответ"), strings.Index(если, "письмо"); i >= 0 && i < j {
		t.Errorf("форма ответа выше письма: %s", если)
	}
	if i, j := strings.Index(если, "свойства"), strings.Index(если, "ответ"); i >= 0 && j >= 0 && i < j {
		t.Errorf("свойства письма выше ответа: %s", если)
	}
	// свойства — под свёрткой, и в них то, что про письмо, а не письмо
	for _, c := range doc.Components {
		if c.ID != "про-письмо" {
			continue
		}
		if !c.Collapsed {
			t.Error("свойства письма раскрыты — они занимают экран вместо письма")
		}
		dump := dumpScreen(&Document{Components: c.Children})
		for _, что := range []string{"Напомнить", "Что с ним сделал Сёма"} {
			if !strings.Contains(dump, что) {
				t.Errorf("в свойствах нет «%s»", что)
			}
		}
	}
	// само письмо на экране есть
	if !strings.Contains(dumpScreen(doc), "текст письма про счёт") {
		t.Error("тела письма на экране нет")
	}
}
