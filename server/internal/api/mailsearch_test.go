package api

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Поиск на «Почте» отвечает на «где то письмо», а не «что пришло за окно».
// Это разные вопросы, и смешивать их значит врать в обоих.
func TestMailSearchIsOwnScreen(t *testing.T) {
	s, st := testServer(t)
	m := &store.Message{MessageID: "<счёт@т>", Subject: "Счет от АО «Горэнерго»",
		FromAddr: "billing@энерго.рф", FromName: "Горэнерго",
		Date: time.Now().AddDate(0, -6, 0), Status: store.StatusConfirmed}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("inbox", "", "горэнерго", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "Горэнерго") {
		t.Fatalf("письмо не нашлось:\n%s", clipTest(dump))
	}
	// письмо полугодовой давности и в спаме — сводка его не показала бы
	// никогда, а поиск обязан и показать, и сказать, где оно лежит
	if !strings.Contains(dump, "в спаме") {
		t.Error("не сказано, где лежит найденное письмо")
	}
	if strings.Contains(dump, "Спам за окно") {
		t.Error("поиск подмешал сводку: это ответ на другой вопрос")
	}

	// пустой поиск возвращает обычную сводку
	doc, err = s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dumpScreen(doc), "Поиск по письмам") {
		t.Error("без запроса экран стал поиском")
	}
}

// Ненайденное объясняет себя: человек должен понять, почему пусто и что
// поиск вообще умеет.
func TestEmptySearchIsExplained(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("inbox", "", "такогонетнигде", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	for _, надо := range []string{"Ничего не нашлось", "словами", "ульянов*"} {
		if !strings.Contains(dump, надо) {
			t.Errorf("в объяснении нет %q", надо)
		}
	}
}
