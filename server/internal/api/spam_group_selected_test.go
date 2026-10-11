package api

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Отмеченная строка на «Почте» бывает группой отправителя, а не письмом:
// её id — «спам-<адрес>». «В спам выбранные» с такой отметкой падал в
// strconv.ParseInt прямо на экран (13 сентября 2026). Группа в выборе
// значит «все ждущие письма этого отправителя».
func TestGroupInPickExpandsToMessages(t *testing.T) {
	s, st := testServer(t)
	for i, тема := range []string{"Раз", "Два", "Три"} {
		m := &store.Message{MessageID: "g" + тема, Subject: тема, FromAddr: "business-info@360.yandex.ru", FromName: "Яндекс 360",
			Date: time.Now().Add(-time.Duration(i) * time.Minute), Status: store.StatusPending, Confidence: 0.95,
			Reason: "фишинг", Account: "проба"}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	чужое := &store.Message{MessageID: "x", Subject: "Чужое", FromAddr: "other@пример.рф", Date: time.Now(),
		Status: store.StatusPending, Confidence: 0.9, Reason: "реклама", Account: "проба"}
	if err := st.InsertMessage(чужое); err != nil {
		t.Fatal(err)
	}

	ids, err := s.messagesByTarget("спам-business-info@360.yandex.ru")
	if err != nil {
		t.Fatalf("группа не раскрылась: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("в группе %d писем, ждали 3", len(ids))
	}
	// смесь: группа плюс номер письма
	ids, err = s.messagesByTarget("спам-business-info@360.yandex.ru, " + strconv.FormatInt(чужое.ID, 10))
	if err != nil || len(ids) != 4 {
		t.Fatalf("смесь группы и письма: %d, %v", len(ids), err)
	}
	// не письмо и не группа — отказ словами, а не strconv
	if _, err := s.messagesByTarget("что-то"); err == nil || strings.Contains(err.Error(), "strconv") {
		t.Fatalf("ошибка не объясняет себя: %v", err)
	}
	// и само действие проходит
	if _, err := s.execute(actionRequest{Action: "not_spam", Target: "спам-business-info@360.yandex.ru"}); err != nil {
		t.Fatalf("not_spam по группе: %v", err)
	}
	if n, _ := st.CountByStatusAccount(store.StatusPending, "проба", nil); n != 1 {
		t.Fatalf("после «не спам» по группе ждущих осталось %d, ждали 1 (чужое)", n)
	}
}
