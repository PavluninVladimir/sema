package api

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Свёртка бережёт место, а короткую группу беречь не от чего. 10 сентября
// 2026 на «Почте» стояло «Счета и чеки · 1 · свёрнуто» и строка «Ozon 1»
// вместо самого письма: человек видел счётчик там, где помещалось письмо.
func TestShortGroupDoesNotCollapse(t *testing.T) {
	s, st := testServer(t)
	// группу помечаем свёрнутой — как её пометил бы человек кнопкой
	группы, err := st.DigestGroups()
	if err != nil {
		t.Fatal(err)
	}
	var имяГруппы string
	for _, g := range группы {
		if g.Rules.Robots {
			g.Collapsed = true
			имяГруппы = g.Title
			if err := st.SaveDigestGroup(g); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if имяГруппы == "" {
		t.Skip("в справочнике нет группы роботов")
	}

	письмо := func(id, тема, от string) {
		t.Helper()
		m := &store.Message{MessageID: id, Subject: тема, FromAddr: от, FromName: "Рассылка",
			Date: time.Now(), Status: store.StatusInboxOK, Account: "проба"}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	письмо("<одно@т>", "Первое письмо рассылки", "noreply@магазин.рф")

	doc, err := s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	// свёрнутая группа узнаётся по перечню отправителей в заголовке:
	// строки-заглушки с кнопкой «Развернуть» больше нет — раздел
	// раскрывается нажатием на заголовок, как все свёртки
	if strings.Contains(dump, "Рассылка 1") {
		t.Error("группа из одного письма свёрнута в счётчик")
	}
	if !strings.Contains(dump, "Первое письмо рассылки") {
		t.Errorf("самого письма на экране нет:\n%s", clipTest(dump))
	}

	// а когда писем набралось — свёртка возвращается
	for i := 0; i < 5; i++ {
		письмо("<много"+string(rune('а'+i))+"@т>", "Ещё рассылка", "noreply@магазин.рф")
	}
	doc, err = s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	свёрнутый := dumpScreen(doc)
	if !strings.Contains(свёрнутый, "Рассылка 6") {
		t.Errorf("шесть писем не свернулись, хотя группа помечена свёрнутой:\n%s", clipTest(свёрнутый))
	}
}

// Порог — настройка, а не гвоздь: у одного «Уведомления» это три письма в
// день, у другого сорок.
func TestCollapseThresholdIsConfigurable(t *testing.T) {
	s, _ := testServer(t)
	if n := s.eng.DigestCollapseFrom(); n != 4 {
		t.Errorf("порог по умолчанию %d", n)
	}
	if err := s.eng.SetDigestCollapseFrom(10); err != nil {
		t.Fatal(err)
	}
	if n := s.eng.DigestCollapseFrom(); n != 10 {
		t.Errorf("порог не сохранился: %d", n)
	}
	if err := s.eng.SetDigestCollapseFrom(0); err == nil {
		t.Error("принят порог меньше одного письма")
	}
}
