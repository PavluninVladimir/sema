package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// Группа с топиками: сообщение с номером темы ложится в разговор этой
// темы под контейнером, а не в общую ленту группы; контейнер ленты не
// имеет, тред «Сёма» заводится под темой.
func TestTopicLandsInOwnConversation(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	createTGAccounts(t, st)
	c := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Дом", Kind: "group", Collect: true, Forum: true}
	if err := st.SaveTGChat(c); err != nil {
		t.Fatal(err)
	}
	_ = st.SaveTGTopic(testTGAccount, c.ID, 7, "Заявки")

	e.onTGMessage(testTGAccount, tg.Update{ChatID: 9, Kind: "group", MsgID: 100, Text: "батарея не греет", Topic: 7})
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 9, Kind: "group", MsgID: 101, Text: "приду в семь"}) // без темы — «Общий»

	root := e.tgRootOf(c)
	rc, err := st.Chat(root)
	if err != nil || !rc.IsContainer() {
		t.Fatalf("корень группы с топиками не стал контейнером: %+v", rc)
	}
	if msgs, _ := st.ChatMsgs(root, 5); len(msgs) != 0 {
		t.Fatalf("в контейнер легли сообщения: %+v", msgs)
	}
	conv, ok := st.ConversationByTopic(root, 7)
	if !ok {
		t.Fatal("разговор темы не заведён")
	}
	cc, _ := st.Chat(conv)
	if cc.Kind != store.ChatKindTopic || cc.Title != "Заявки" || cc.Topic != 7 {
		t.Fatalf("разговор темы не тот: %+v", cc)
	}
	msgs, _ := st.ChatMsgs(conv, 5)
	if len(msgs) != 1 || msgs[0].Role != "peer" || msgs[0].Content != "батарея не греет" {
		t.Fatalf("в разговоре темы не то: %+v", msgs)
	}
	general, ok := st.ConversationByTopic(root, tg.GeneralTopic)
	if !ok || general == conv {
		t.Fatalf("«Общий» не стал своим разговором: %d", general)
	}
	// разговор темы знает адрес: группа плюс тема
	peer, ok := e.tgPeerOfChat(conv)
	if !ok || peer.ID != 9 || peer.Topic != 7 || peer.TopicTitle != "Заявки" {
		t.Fatalf("собеседник темы не тот: %+v", peer)
	}
	// тред «Сёма» — под темой, а не под контейнером
	th := e.chatForOutput(conv)
	tc, _ := st.Chat(th)
	if th == conv || tc.Kind != store.ChatKindSema || tc.Parent != conv {
		t.Fatalf("тред темы не тот: %+v", tc)
	}
	if e.chatForOutput(root) != root {
		t.Fatal("под контейнером завёлся тред")
	}
	if _, err := e.SendMessageTo(root, "привет"); err == nil {
		t.Fatal("в контейнер дали написать")
	}
	// удаление контейнера уносит темы и их ветки
	if err := e.DeleteChatTree(root); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Chat(th); err == nil {
		t.Fatal("ветка темы пережила удаление контейнера")
	}
}

// Признак «группа с топиками» доезжает из Telegram в справочник и
// переживает обновление списка: без него топиков не появляется вовсе.
func TestFlagTopicsSaved(t *testing.T) {
	_, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	row := tgRowOf(testTGAccount, tg.Chat{ID: 9, Title: "Дом", Kind: "group", Access: 42, Forum: true})
	if !row.Forum || row.Access != 42 {
		t.Fatalf("строка справочника потеряла поля: %+v", row)
	}
	if err := st.SaveTGChat(row); err != nil {
		t.Fatal(err)
	}
	_ = st.SetTGChatRight(testTGAccount, 9, "collect", true)
	if err := st.SaveTGChat(tgRowOf(testTGAccount, tg.Chat{ID: 9, Title: "Дом", Kind: "group", Access: 42, Forum: true})); err != nil {
		t.Fatal(err)
	}
	got, ok := st.TGChat(testTGAccount, 9)
	if !ok || !got.Forum || !got.Collect {
		t.Fatalf("после обновления списка: %+v", got)
	}
}

// Ответ уходит в тот же топик: черновик из треда темы несёт номер темы,
// и отправка идёт с ним.
func TestReplyGoesToSameTopic(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	createTGAccounts(t, st)
	c := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Дом", Kind: "group", Collect: true, Forum: true}
	_ = st.SaveTGChat(c)
	_ = st.SaveTGTopic(testTGAccount, c.ID, 7, "Заявки")
	conv, err := e.tgConvFor(c, 7)
	if err != nil {
		t.Fatal(err)
	}
	thread := e.chatForOutput(conv)
	_, cards := e.runChatTG(context.Background(), thread, "Напишу так:\n```sema-tg:\nМастер придёт завтра\n```")
	if len(cards) != 1 || cards[0].TG == nil || cards[0].TG.Status != "pending" {
		t.Fatalf("черновик не тот: %+v", cards)
	}
	o, ok := st.TGOutboxByID(cards[0].TG.ID)
	if !ok || o.ChatID != 9 || o.Topic != 7 {
		t.Fatalf("черновик без темы: %+v", o)
	}
	if !strings.Contains(cards[0].TG.Who, "Заявки") {
		t.Fatalf("адресат без темы: %q", cards[0].TG.Who)
	}
	// промпт треда темы говорит модели, где она
	if p := e.tgChatPrompt(thread); !strings.Contains(p, "Заявки") {
		t.Fatalf("промпт не знает тему:\n%s", p)
	}
}

// В группе видно, кто написал: имя автора едет с сообщением, ложится в
// архив и в переписку, а промпт автоответа подписывает реплики автором,
// а не именем группы.
func TestAuthorReplyInGroup(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	createTGAccounts(t, st)
	c := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Дом", Kind: "group", Collect: true, Forum: true}
	_ = st.SaveTGChat(c)
	_ = st.SaveTGTopic(testTGAccount, c.ID, 7, "Заявки")

	e.onTGMessage(testTGAccount, tg.Update{ChatID: 9, Kind: "group", MsgID: 100, Text: "батарея не греет", Topic: 7, From: "Митя"})
	conv, _ := st.ConversationByTopic(e.tgRootOf(c), 7)
	msgs, _ := st.ChatMsgs(conv, 5)
	if len(msgs) != 1 || msgs[0].Author != "Митя" {
		t.Fatalf("автор не доехал до переписки: %+v", msgs)
	}
	arch, _ := st.TGMessages(testTGAccount, 9, 7, 5)
	if len(arch) != 1 || arch[0].Author != "Митя" {
		t.Fatalf("автор не доехал до архива: %+v", arch)
	}
}

// Право темы умеет и расширять: у группы чтение выключено, у темы
// включено — сообщения этой темы приходят, остальные нет. И уведомление
// о такой теме ведёт в её разговор, а не в контейнер без ленты.
func TestGrantTopicExtends(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	createTGAccounts(t, st)
	c := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Дом", Kind: "group", Forum: true} // читать у группы выключено
	_ = st.SaveTGChat(c)
	_ = st.SaveTGTopic(testTGAccount, c.ID, 7, "Заявки")
	if err := e.TGSetTopicRight(testTGAccount, 9, 7, "collect", 1); err != nil {
		t.Fatal(err)
	}
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 9, Kind: "group", MsgID: 100, Text: "батарея не греет", Topic: 7, From: "Митя"})
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 9, Kind: "group", MsgID: 101, Text: "оффтоп", Topic: 3})

	root := e.tgRootOf(c)
	conv, ok := st.ConversationByTopic(root, 7)
	if !ok {
		t.Fatal("тема со своим правом не завела разговор")
	}
	if _, ok := st.ConversationByTopic(root, 3); ok {
		t.Fatal("тема без права завела разговор")
	}
	// «Разобраться» у уведомления о теме ведёт в разговор темы
	if got := e.peerChatOfNotice("tg:9/7"); got != conv {
		t.Fatalf("уведомление темы ведёт в %d, а разговор темы %d", got, conv)
	}
	if got := e.peerChatOfNotice("tg-wait:9/7"); got != conv {
		t.Fatalf("семафор темы ведёт в %d", got)
	}
}

// Отправка в форум без темы уходит в «Общий», значит и права спрашивать
// надо у «Общего»: иначе запрет темы обходится через имя группы.
func TestSendWithoutTopicAsksCommon(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	c := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Дом", Kind: "group", Collect: true, Reply: true, Forum: true}
	_ = st.SaveTGChat(c)
	_ = st.SaveTGTopic(testTGAccount, c.ID, 1, "General")
	if err := e.TGSetTopicRight(testTGAccount, 9, 1, "blocked", 1); err != nil {
		t.Fatal(err)
	}
	err := e.tgSendChecked(context.Background(), testTGAccount, 9, 0, "привет", true)
	if err == nil || !strings.Contains(err.Error(), "заблокирован") {
		t.Fatalf("запрет «Общего» обошли: %v", err)
	}
}

// Переписка, собранная до появления топиков, не пропадает: контейнер
// ленты не показывает, поэтому она переезжает в разговор «Общего».
func TestConversationToTopicsNotDisappears(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	c := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Дом", Kind: "group", Collect: true}
	_ = st.SaveTGChat(c)
	root, err := e.tgChatFor(c)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddChatMsg(root, "peer", "старое сообщение группы")

	c.Forum = true
	_ = st.SaveTGChat(c)
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 9, Kind: "group", MsgID: 100, Text: "в теме", Topic: 7})

	if msgs, _ := st.ChatMsgs(root, 10); len(msgs) != 0 {
		t.Fatalf("в контейнере остались сообщения: %+v", msgs)
	}
	general, ok := st.ConversationByTopic(root, 1)
	if !ok {
		t.Fatal("разговор «Общего» не заведён")
	}
	msgs, _ := st.ChatMsgs(general, 10)
	if len(msgs) != 1 || msgs[0].Content != "старое сообщение группы" {
		t.Fatalf("прежняя переписка потерялась: %+v", msgs)
	}
}

// «Отвечать только в одном топике»: право группы выключено, у одной темы
// включено — Сёма отвечает там и молчит в остальных; заблокированная тема
// не заводит разговор.
func TestGrantOnlyInOneTopic(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	createTGAccounts(t, st)
	c := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Дом", Kind: "group", Collect: true, Forum: true}
	_ = st.SaveTGChat(c)
	if err := e.TGSetTopicRight(testTGAccount, 9, 7, "reply", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.TGSetTopicRight(testTGAccount, 9, 3, "blocked", 1); err != nil {
		t.Fatal(err)
	}
	if !e.tgEffective(c, 7).Reply || e.tgEffective(c, 1).Reply {
		t.Fatal("право темы не переопределило право группы")
	}
	err := e.tgSendChecked(context.Background(), testTGAccount, 9, tg.GeneralTopic, "ответ", false)
	if err == nil || !strings.Contains(err.Error(), "писать не разрешено") {
		t.Fatalf("в «Общий» дали писать без права: %v", err)
	}
	err = e.tgSendChecked(context.Background(), testTGAccount, 9, 7, "ответ", false)
	if err != nil && strings.Contains(err.Error(), "писать не разрешено") {
		t.Fatalf("в тему с правом писать не дали: %v", err)
	}
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 9, Kind: "group", MsgID: 5, Text: "секрет", Topic: 3})
	if root := e.tgRootOf(c); root != 0 {
		if _, ok := st.ConversationByTopic(root, 3); ok {
			t.Fatal("заблокированная тема завела разговор")
		}
	}
	// «как у группы» снимает переопределение
	_ = e.TGSetTopicRight(testTGAccount, 9, 7, "reply", store.TopicInherit)
	if e.tgEffective(c, 7).Reply {
		t.Fatal("переопределение не снялось")
	}
}
