package store

import (
	"strings"
	"testing"
	"time"
)

// Превью собирается до того, как приедет тело, — из первых килобайт сырого
// письма. У рассылки там голова документа и таблица стилей, и в списке
// появляется «% 96 <![e». Когда тело доехало, источник получше есть.
func TestPreviewRebuiltFromText(t *testing.T) {
	st := searchDB(t)
	m := &Message{MessageID: "<ecco@т>", Subject: "4 дня –15% на обувь",
		FromAddr: "news@shop.example", FromName: "ECCO", Snippet: "% 96 <![e",
		Date: time.Now(), Status: StatusArchived}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	text := "4 дня –15% на классическую обувь и сумки\n\nУспейте купить, пока выгода действует\nСкидка действует до воскресенья"
	if err := st.SaveBody(m.ID, Body{Text: text}); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.Message(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fresh.Snippet, "<![") || strings.Contains(fresh.Snippet, "% 96") {
		t.Errorf("превью осталось обломком вёрстки: %q", fresh.Snippet)
	}
	if !strings.Contains(fresh.Snippet, "Успейте купить") {
		t.Errorf("превью не из текста письма: %q", fresh.Snippet)
	}
}

// Плохое превью не заменяется ещё худшим: пустой или совсем короткий текст
// не должен затирать то, что хоть что-то говорило.
func TestBadPreviewDoesNotOverwriteGood(t *testing.T) {
	st := searchDB(t)
	m := &Message{MessageID: "<a@т>", Subject: "Счёт", FromAddr: "b@в.рф",
		Snippet: "Счёт-квитанция на оплату за август, к оплате 512,30 рубля",
		Date:    time.Now(), Status: StatusArchived}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	before := m.Snippet
	if err := st.SaveBody(m.ID, Body{Text: "  \n \n "}); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := st.Message(m.ID); fresh.Snippet != before {
		t.Errorf("пустой текст затёр превью: %q", fresh.Snippet)
	}
	if err := st.SaveBody(m.ID, Body{Text: "ок"}); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := st.Message(m.ID); fresh.Snippet != before {
		t.Errorf("двухбуквенный текст затёр превью: %q", fresh.Snippet)
	}
}
