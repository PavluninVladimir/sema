package api

import (
	"strconv"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Разговоры в меню — деревом в три уровня: группа → контейнер (группа с
// топиками) → тема, и ветка рядом с темой, когда открыта она сама. Раньше
// это дерево жило второй панелью рядом с меню и дублировало его.
func TestChatListInThreeLevels(t *testing.T) {
	s, st := testServer(t)
	// главный разговор с Сёмой — самый старый: он первым в меню и не
	// должен забирать себе ветку темы, хотя та тоже ветка «Сёма»
	я, _ := st.CreateChat("Telegram · Вадим")
	главный, _ := st.CreateThread(я, store.ChatKindSema, 0, "Сёма · Вадим")
	root, _ := st.CreateChat("Telegram · Дом")
	_ = st.SetChatKind(root, store.ChatKindGroup)
	topic, _ := st.CreateConversation(root, store.ChatKindTopic, 7, "Заявки")
	_ = st.AddChatMsg(topic, "peer", "батарея не греет")
	thread, _ := st.CreateThread(topic, store.ChatKindSema, 0, "Сёма · Заявки")
	_ = st.AddChatMsg(thread, "assistant", "вызову мастера")
	other, _ := st.CreateChat("Про отпуск")
	_ = st.AddChatMsg(other, "user", "поедем")
	s.markChatRead(thread)

	id := func(n int64) string { return "chat:" + strconv.FormatInt(n, 10) }
	var найти func(children []Screen, addr string, глубина int) (*Screen, int)
	найти = func(children []Screen, addr string, глубина int) (*Screen, int) {
		for i := range children {
			if children[i].ID == addr {
				return &children[i], глубина
			}
			if п, г := найти(children[i].Children, addr, глубина+1); п != nil {
				return п, г
			}
		}
		return nil, 0
	}
	меню := func(screen string) []Screen {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Screens) == 0 || doc.Screens[0].ID != "ask" {
			t.Fatalf("первый пункт меню не «Чаты»")
		}
		return doc.Screens[0].Children
	}

	children := меню(id(topic))
	if len(children) == 0 || children[0].ID != id(главный) {
		t.Fatalf("первым под «Чатами» не главный разговор с Сёмой: %+v", children)
	}
	дом, г := найти(children, id(root), 0)
	if дом == nil || г != 1 || !дом.Open || !дом.NoSelf {
		t.Fatalf("контейнер «Дом» не раскрытым узлом в группе: %+v, глубина %d", дом, г)
	}
	if тема, г := найти(children, id(topic), 0); тема == nil || г != 2 {
		t.Fatalf("тема не под контейнером: %+v, глубина %d", тема, г)
	}
	// открыта тема, а не ветка: ветки в меню нет — меню не растёт от
	// каждого перехода
	if в, _ := найти(children, id(thread), 0); в != nil {
		t.Fatal("ветка видна, хотя открыта тема, а не она")
	}

	// открыта ветка — она видна рядом с темой, иначе нечего подсветить
	children = меню(id(thread))
	if в, г := найти(children, id(thread), 0); в == nil || г != 2 {
		t.Fatalf("открытая ветка не рядом с темой: %+v, глубина %d", в, г)
	}

	// в чужом разговоре контейнер всё равно раскрыт: раскрытие «где
	// стоишь» двигало меню при переходах между разговорами
	children = меню(id(other))
	if дом, _ := найти(children, id(root), 0); дом == nil || !дом.Open {
		t.Fatalf("контейнер свернулся в чужом разговоре: %+v", дом)
	}
	// в топике подпись пузыря — автор, а не название темы: в группе
	// пишут многие, и «General» над каждой репликой — неправда. Подпись
	// стоит над пузырём (кто сказал), время — под ним
	_ = st.AddChatMsgFrom(topic, "peer", "Митя", "батарея не греет")
	_ = st.AddChatMsg(topic, "peer", "без автора")
	doc, _ := s.buildScreen(id(topic), "", "", 0)
	var кто []string
	for _, comp := range doc.Components {
		if comp.Type == "bubble" && comp.Role == "peer" {
			кто = append(кто, comp.Title)
		}
	}
	if len(кто) < 2 {
		t.Fatalf("пузырей собеседника меньше двух: %+v", кто)
	}
	if кто[len(кто)-2] != "Митя" {
		t.Fatalf("подпись без автора: %q", кто[len(кто)-2])
	}
	if кто[len(кто)-1] != "" {
		t.Fatalf("реплика без автора всё-таки подписана: %q", кто[len(кто)-1])
	}
	// ветку разговора удаляют из её шапки: списка с кнопками удаления
	// рядом больше нет
	doc, _ = s.buildScreen(id(thread), "", "", 0)
	удалить := false
	for _, a := range doc.ToolbarMenu {
		if a.Action == "delete_chat" {
			удалить = true
		}
	}
	if !удалить {
		t.Fatal("ветку нечем удалить")
	}
	// экран контейнера — список разговоров, без поля ввода
	doc, err := s.buildScreen(id(root), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Composer != nil {
		t.Fatal("у контейнера появилось поле ввода")
	}
	// имя темы — жирная строка списка (Subtitle), а не мелкая надстрока:
	// раньше старшинство читалось наоборот
	found := false
	for _, comp := range doc.Components {
		for _, стр := range comp.Children {
			if стр.Subtitle == "Заявки" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("на экране контейнера нет разговора темы: %+v", doc.Components)
	}
}
