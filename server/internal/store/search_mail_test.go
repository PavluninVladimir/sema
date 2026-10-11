package store

import (
	"path/filepath"
	"testing"
	"time"
)

func searchDB(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "поиск.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Письмо ищется словами — без эмбеддера, без модели, без сети. На Pi
// локальная модель засыпает, и смысловой поиск исчезает целиком; архив,
// в котором нельзя найти письмо, архивом не является.
func TestSearchMessagesByWords(t *testing.T) {
	st := searchDB(t)
	m := &Message{MessageID: "<счёт@т>", Subject: "Счет от АО \"Горэнерго\"",
		FromAddr: "billing@энерго.рф", FromName: "Горэнерго",
		Snippet: "Показания счётчика", Date: time.Now(), Status: StatusArchived}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	other := &Message{MessageID: "<другое@т>", Subject: "Стильные новинки осени",
		FromAddr: "shop@магазин.рф", Date: time.Now(), Status: StatusArchived}
	if err := st.InsertMessage(other); err != nil {
		t.Fatal(err)
	}

	list, err := st.SearchMessages("горэнерго", "", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != m.ID {
		t.Fatalf("по слову из темы нашлось %d писем", len(list))
	}
	// два слова — это «оба», как и ждёт человек
	if list, _ := st.SearchMessages("счет горэнерго", "", nil, 10); len(list) != 1 {
		t.Errorf("«счет горэнерго» нашло %d", len(list))
	}
	if list, _ := st.SearchMessages("горэнерго новинки", "", nil, 10); len(list) != 0 {
		t.Errorf("слова из разных писем не должны совпадать вместе: %d", len(list))
	}

	// тело приезжает позже темы — и письмо должно искаться по нему тоже
	if list, _ := st.SearchMessages("задолженность", "", nil, 10); len(list) != 0 {
		t.Fatal("слова из тела нашлись до того, как тело приехало")
	}
	if err := st.SaveBody(m.ID, Body{Text: "Задолженность на 1 сентября — 512,30 рубля"}); err != nil {
		t.Fatal(err)
	}
	list, _ = st.SearchMessages("задолженность", "", nil, 10)
	if len(list) != 1 {
		t.Errorf("после появления тела письмо по слову из него не нашлось: %d", len(list))
	}
	// и по теме оно всё ещё ищется: запись переписана, а не потеряна
	if list, _ := st.SearchMessages("горэнерго", "", nil, 10); len(list) != 1 {
		t.Error("после появления тела письмо перестало искаться по теме")
	}
}

// Набранное человеком не должно ронять поиск. Кавычка, скобка и слово NOT
// — обычные знаки в почтовом запросе, а для FTS5 это синтаксис.
func TestQueryNeverBreaksSearch(t *testing.T) {
	st := searchDB(t)
	m := &Message{MessageID: "<a@т>", Subject: "Счёт NOT оплачен", FromAddr: "a@б.рф",
		Date: time.Now(), Status: StatusArchived}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`"счёт`, `счёт)`, `NOT счёт`, `счёт OR`, `*`, `   `, `AND`, `счёт "`} {
		if _, err := st.SearchMessages(query, "", nil, 10); err != nil {
			t.Errorf("запрос %q уронил поиск: %v", query, err)
		}
	}
	// поиск по началу слова остаётся: это привычный приём
	if list, _ := st.SearchMessages("ульянов*", "", nil, 10); len(list) != 0 {
		t.Errorf("нашлось лишнее: %d", len(list))
	}
	if q := FTSQuery("ульянов*"); q != `"ульянов"*` {
		t.Errorf("запрос по началу слова собрался как %q", q)
	}
	if q := FTSQuery("  NOT  "); q != `"NOT"` {
		t.Errorf("служебное слово должно стать обычным: %q", q)
	}
}
