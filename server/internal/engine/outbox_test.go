package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// По умолчанию Сёма не отправляет ничего. Черновик отменяем, отправленное
// письмо — нет; ассистент, однажды отправивший не то и не тому, теряет
// доверие насовсем.
func TestByDefaultDraft(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if mode := e.SendMode(); mode != ModeDraft {
		t.Errorf("режим по умолчанию %q", mode)
	}
	if e.CanSendItself([]string{"кто@угодно.рф"}) {
		t.Error("в режиме по умолчанию Сёма собралась отправлять сама")
	}
}

// Режим «сама» без списка адресатов — это недонастроенность, а не
// разрешение писать всем.
func TestModeSelfWithoutListNoOne(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.SetSendMode(ModeSelf); err != nil {
		t.Fatal(err)
	}
	if e.CanSendItself([]string{"кто@угодно.рф"}) {
		t.Error("режим «сама» с пустым списком разрешил отправку")
	}
	if err := e.SetSendAutoTo("ivan@наша-контора.рф, @партнёр.рф"); err != nil {
		t.Fatal(err)
	}
	if !e.CanSendItself([]string{"ivan@наша-контора.рф"}) {
		t.Error("названный адресат не разрешён")
	}
	if !e.CanSendItself([]string{"anna@партнёр.рф"}) {
		t.Error("домен из списка не разрешён")
	}
	if e.CanSendItself([]string{"чужой@другой.рф"}) {
		t.Error("адресат вне списка разрешён")
	}
	// хоть один чужой в списке — письмо целиком ждёт человека
	if e.CanSendItself([]string{"ivan@наша-контора.рф", "чужой@другой.рф"}) {
		t.Error("письмо с чужим адресатом в копии ушло бы само")
	}
}

// Непонятный режим не принимается молча: молчаливое «оставлю как было»
// здесь означало бы, что человек думает одно, а Сёма делает другое.
func TestUnclearModeRejected(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.SetSendMode("как-нибудь"); err == nil {
		t.Error("непонятный режим принят")
	}
	if e.SendMode() != ModeDraft {
		t.Error("режим сбился после отказа")
	}
}

// Reply-To весомее From: рассылки нарочно ставят его, чтобы ответ попал
// живому человеку, а не в noreply.
func TestRecipientReplyFromEnvelope(t *testing.T) {
	m := &store.Message{FromAddr: "noreply@магазин.рф", ReplyTo: "support@магазин.рф"}
	if got := replyRecipients(m, "я@почта.рф"); len(got) != 1 || got[0] != "support@магазин.рф" {
		t.Errorf("адресаты: %v", got)
	}
	// без Reply-To — обычный отправитель
	m2 := &store.Message{FromAddr: "billing@энерго.рф"}
	if got := replyRecipients(m2, "я@почта.рф"); len(got) != 1 || got[0] != "billing@энерго.рф" {
		t.Errorf("адресаты: %v", got)
	}
	// себе Сёма не пишет
	m3 := &store.Message{FromAddr: "я@почта.рф"}
	if got := replyRecipients(m3, "Я@Почта.рф"); len(got) != 0 {
		t.Errorf("Сёма собралась ответить сама себе: %v", got)
	}
}

// Пустой ответ и письмо без обратного адреса — отказ, а не письмо в никуда.
func TestEmptyReplyRejected(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{MessageID: "<a@т>", Subject: "Счёт", FromAddr: "b@в.рф", Status: store.StatusInboxOK}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReplyToMessage(m.ID, "   ", false); err == nil {
		t.Error("пустой ответ принят")
	}
	without := &store.Message{MessageID: "<b@т>", Subject: "Без адреса", Status: store.StatusInboxOK}
	if err := st.InsertMessage(without); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReplyToMessage(without.ID, "ок", false); err == nil {
		t.Error("ответ на письмо без обратного адреса принят")
	} else if !strings.Contains(err.Error(), "некому") && !strings.Contains(err.Error(), "не настроен") {
		t.Errorf("непонятный отказ: %v", err)
	}
}

// Отправка без настроенного SMTP невозможна, и экран должен узнать об этом
// заранее, а не после нажатия.
func TestSendWithoutSMTPNotReady(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	ready, why := e.SendReady("проба")
	if ready {
		t.Error("отправка объявлена готовой без SMTP")
	}
	if why == "" {
		t.Error("не сказано, чего не хватает")
	}
}
