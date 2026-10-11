package mail

import (
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// Собранное письмо должна прочитать стандартная библиотека: если его не
// разбирает Go, его не разберёт и почтовый сервер.
func TestMessageReadsBack(t *testing.T) {
	raw, err := Build(Message{
		From: "owner@example.org", FromName: "Вадим",
		To:      []string{"billing@энерго.рф"},
		Subject: "Счёт за август", Text: "Здравствуйте!\nСчёт получил, оплачу до пятницы.",
		Date: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("письмо не разбирается: %v\n%s", err, raw)
	}
	// тема по-русски: получатель не должен видеть кракозябры
	subject, err := (&mime.WordDecoder{}).DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Счёт за август" {
		t.Errorf("тема расшифровалась как %q", subject)
	}
	from, err := mail.ParseAddress(m.Header.Get("From"))
	if err != nil {
		t.Fatalf("From не разбирается: %v", err)
	}
	if from.Name != "Вадим" || from.Address != "owner@example.org" {
		t.Errorf("From: %+v", from)
	}
	// тело: раскодируем и сверим
	body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "оплачу до пятницы") {
		t.Errorf("тело потерялось: %q", body)
	}
	if !strings.Contains(string(body), "Здравствуйте!") {
		t.Errorf("начало тела потерялось: %q", body)
	}
	// каждая строка укладывается в предел SMTP
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Errorf("строка длиной %d — сервер вправе её не принять", len(line))
		}
	}
}

// Ответ без In-Reply-To и References — это новое письмо с похожей темой:
// у собеседника оно встанет отдельной веткой, и разговор развалится.
func TestReplyKeepsThread(t *testing.T) {
	orig := &Fetched{
		MessageID: "<первое@энерго.рф>",
		Subject:   "Счёт за август",
		Refs:      []string{"нулевое@энерго.рф"},
	}
	raw, err := Build(Message{
		From: "owner@example.org", To: []string{"billing@энерго.рф"},
		Subject: "Счёт за август", Text: "Оплачу", ReplyTo: orig,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Header.Get("In-Reply-To"); got != "<первое@энерго.рф>" {
		t.Errorf("In-Reply-To: %q", got)
	}
	refs := m.Header.Get("References")
	if !strings.Contains(refs, "нулевое@энерго.рф") || !strings.Contains(refs, "первое@энерго.рф") {
		t.Errorf("References: %q", refs)
	}
	// порядок важен: исходное письмо последнее
	if strings.Index(refs, "нулевое") > strings.Index(refs, "первое") {
		t.Errorf("порядок цепочки перевёрнут: %q", refs)
	}
	subject, _ := (&mime.WordDecoder{}).DecodeHeader(m.Header.Get("Subject"))
	if subject != "Re: Счёт за август" {
		t.Errorf("тема ответа: %q", subject)
	}
}

// «Re: Re: Re: Счёт» — верный признак письма, собранного механически.
func TestRePrefixDoesNotPileUp(t *testing.T) {
	for _, subject := range []string{"Re: Счёт", "RE: Счёт", "Ответ: Счёт"} {
		raw, err := Build(Message{From: "a@б.рф", To: []string{"в@г.рф"},
			Subject: subject, Text: "ок", InReplyTo: "исходное@б.рф"})
		if err != nil {
			t.Fatal(err)
		}
		m, _ := mail.ReadMessage(strings.NewReader(string(raw)))
		got, _ := (&mime.WordDecoder{}).DecodeHeader(m.Header.Get("Subject"))
		if strings.Count(strings.ToLower(got), "re:") > 1 || strings.Contains(got, "Ответ: Ответ") {
			t.Errorf("тема %q превратилась в %q", subject, got)
		}
	}
}

// Пустой адресат или пустой отправитель — отказ, а не письмо в никуда.
func TestMessageWithoutRecipientIsNotBuilt(t *testing.T) {
	if _, err := Build(Message{From: "a@б.рф", Text: "ок"}); err == nil {
		t.Error("собралось письмо без получателя")
	}
	if _, err := Build(Message{To: []string{"в@г.рф"}, Text: "ок"}); err == nil {
		t.Error("собралось письмо без отправителя")
	}
}

// Пробел в конце строки съедает первый же сервер по дороге — а вместе с
// ним и смысл, если это было форматирование.
func TestTrailingSpaceIsEncoded(t *testing.T) {
	raw, err := Build(Message{From: "a@б.рф", To: []string{"в@г.рф"},
		Subject: "Проба", Text: "строка с хвостом   \nвторая"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := mail.ReadMessage(strings.NewReader(string(raw)))
	body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "строка с хвостом   \r\n") {
		t.Errorf("хвостовой пробел потерян: %q", body)
	}
}
