package engine

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Ответ найден» должно означать ответ именно на это письмо. Раньше это
// проверялось совпадением темы, и «Re: Счёт» из чужого разговора закрывало
// напоминание по другому счёту.
func TestReplyFoundByEnvelope(t *testing.T) {
	_, st := newTestEngine(t, "", nil)
	message := &store.Message{MessageID: "исходное@пример.рф", Subject: "Счёт",
		FromAddr: "billing@пример.рф", Status: store.StatusInboxOK, Account: "я@почта.рф"}
	if err := st.InsertMessage(message); err != nil {
		t.Fatal(err)
	}
	// чужой ответ с той же темой — не наш ответ на это письмо
	foreign := &store.Message{MessageID: "чужой@пример.рф", Subject: "Re: Счёт",
		FromAddr: "я@почта.рф", Direction: "out", InReplyTo: "другое@пример.рф"}
	if err := st.InsertMessage(foreign); err != nil {
		t.Fatal(err)
	}
	has, err := st.ReplyTo("исходное@пример.рф")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("чужой ответ с той же темой засчитан за наш")
	}

	// а вот это — настоящий ответ
	our := &store.Message{MessageID: "наш@пример.рф", Subject: "Re: Счёт",
		FromAddr: "я@почта.рф", Direction: "out", InReplyTo: "исходное@пример.рф"}
	if err := st.InsertMessage(our); err != nil {
		t.Fatal(err)
	}
	if has, _ := st.ReplyTo("исходное@пример.рф"); !has {
		t.Error("прямой ответ не найден")
	}

	// и ответ где-то в середине цепочки — тоже ответ
	_, st2 := newTestEngine(t, "", nil)
	chain := &store.Message{MessageID: "поздний@пример.рф", FromAddr: "я@почта.рф",
		Direction: "out", Refs: "первое@пример.рф исходное@пример.рф"}
	if err := st2.InsertMessage(chain); err != nil {
		t.Fatal(err)
	}
	if has, _ := st2.ReplyTo("<исходное@пример.рф>"); !has {
		t.Error("ответ из середины цепочки не найден (или скобки не сняты)")
	}

	// входящее письмо ответом не считается: отвечали не мы
	_, st3 := newTestEngine(t, "", nil)
	incoming := &store.Message{MessageID: "вход@пример.рф", FromAddr: "чужой@пример.рф",
		Direction: "in", InReplyTo: "исходное@пример.рф"}
	if err := st3.InsertMessage(incoming); err != nil {
		t.Fatal(err)
	}
	if has, _ := st3.ReplyTo("исходное@пример.рф"); has {
		t.Error("чужое входящее письмо засчитано за наш ответ")
	}
}
