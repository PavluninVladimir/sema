package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Сколько писем удалено» человеку приходилось спрашивать: журнал
// удалений велся с самого начала, но не показывался нигде (13 сентября
// 2026). Число — на «Почте», что именно — на «Папках и хранении».
func TestPurgedFromServerVisible(t *testing.T) {
	s, st := testServer(t)
	m := &store.Message{MessageID: "<стёртое@т>", Subject: "Рассылка про скидки",
		FromAddr: "news@магазин.рф", Status: store.StatusPurged, Account: "проба"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := st.LogDeletion(m.ID, "проба", "Spam", "спам"); err != nil {
		t.Fatal(err)
	}
	// на «Почте» — число и переход
	doc, err := s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	почта := dumpScreen(doc)
	if !strings.Contains(почта, "С почтового сервера стёрто за сутки: 1") {
		t.Errorf("на «Почте» не сказано, сколько стёрто:\n%s", clipTest(почта))
	}
	if !strings.Contains(почта, "архива Сёмы письма не удаляются") {
		t.Errorf("не сказано, что у Сёмы копия остаётся:\n%s", clipTest(почта))
	}
	// «Какие письма» ведёт на список писем, а не в настройки: кнопка
	// водила на «Папки и хранение», и человек искал список среди настроек
	if !strings.Contains(почта, purgedScreen) {
		t.Errorf("кнопка не ведёт на список стёртых писем:\n%s", clipTest(почта))
	}
	// сам список: письма с темой, отправителем, правилом и папкой
	doc, err = s.buildScreen(purgedScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	список := dumpScreen(doc)
	for _, что := range []string{"Рассылка про скидки", "news@магазин.рф", "спам", "Spam", "проба"} {
		if !strings.Contains(список, что) {
			t.Errorf("в списке нет «%s»:\n%s", что, clipTest(список))
		}
	}
	if !strings.Contains(список, "msg:") {
		t.Error("из списка нельзя открыть само письмо")
	}
	// отбор по правилу работает
	doc, err = s.buildScreen(purgedScreen, "срок хранения", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dumpScreen(doc), "Рассылка про скидки") {
		t.Error("отбор по правилу «срок хранения» показал письмо, стёртое как спам")
	}
	// и поиск по отправителю
	doc, err = s.buildScreen(purgedScreen, "", "магазин", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dumpScreen(doc), "Рассылка про скидки") {
		t.Error("поиск по отправителю письмо не нашёл")
	}
	// в настройках — числа и переход, а не сам список: у ящика на «Хранении»,
	// у всех ящиков — на «Общем»
	for _, screen := range []string{entityAddr(mailScreen, "проба", mailTabKeep), entityAddr(mailScreen, "", mailTabCommon)} {
		doc, err = s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		хранение := dumpScreen(doc)
		if !strings.Contains(хранение, "Стёрто с почтового сервера") {
			t.Fatalf("%s: раздела про стёртое нет:\n%s", screen, clipTest(хранение))
		}
		if !strings.Contains(хранение, purgedScreen) {
			t.Errorf("%s: из настроек нет перехода к списку писем", screen)
		}
		if strings.Contains(хранение, "Рассылка про скидки") {
			t.Errorf("%s: в настройках снова весь список писем — он должен жить своим экраном", screen)
		}
	}
}
