package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// Канал — одна лента, как в самом Telegram: пост ложится в чат канала,
// а комментарий к нему приходит в привязанную группу обсуждения и ложится
// в ту же ленту с подписью, к какому посту он, — а не в ленту группы и не
// в отдельный разговор. 17 сентября 2026 владелец: «не нужно отдельные
// разговоры, как в Telegram пусть будет».
func TestChannelOneFeed(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	createTGAccounts(t, st)
	ch := &store.TGChat{Account: testTGAccount, ID: 70, Title: "Новости", Kind: "channel", Collect: true, Access: 7}
	if err := st.SaveTGChat(ch); err != nil {
		t.Fatal(err)
	}
	// группа обсуждения: человек её тоже читает, но комментарии должны
	// уйти в ленту канала, а не в её ленту
	gr := &store.TGChat{Account: testTGAccount, ID: 71, Title: "Новости · чат", Kind: "group", Collect: true, Access: 8}
	if err := st.SaveTGChat(gr); err != nil {
		t.Fatal(err)
	}

	e.onTGMessage(testTGAccount, tg.Update{ChatID: 70, Kind: "channel", MsgID: 500, Text: "Вышла новая версия\nподробности", Date: time.Now()})
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 70, Kind: "channel", MsgID: 501, Text: "Второй пост", Date: time.Now()})

	root := e.tgRootOf(ch)
	rc, err := st.Chat(root)
	if err != nil || rc.Kind != store.ChatKindChannel || rc.IsContainer() {
		t.Fatalf("канал не лента: %+v", rc)
	}
	if kids, _ := st.ChatChildren(root); len(kids) != 0 {
		t.Fatalf("под каналом завелись разговоры: %+v", kids[0])
	}

	// привязку «копия поста в группе → пост» обычно спрашивает Telegram;
	// здесь кладём её руками — в сеть тест не ходит
	if err := st.SaveTGPostLink(&store.TGPostLink{Account: testTGAccount, Channel: 70, Post: 500, Group: 71, Access: 8, Root: 900}); err != nil {
		t.Fatal(err)
	}
	// копия поста в группе обсуждения второй раз не показывается
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 71, Kind: "group", MsgID: 900, Text: "Вышла новая версия", Date: time.Now()})
	// а комментарий — ответ на эту копию
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 71, Kind: "group", MsgID: 901, Text: "а что нового?", Thread: 900, From: "Митя", Date: time.Now()})

	msgs, _ := st.ChatMsgs(root, 10) // порядок хронологический
	if len(msgs) != 3 {
		t.Fatalf("в ленте канала не три сообщения: %d", len(msgs))
	}
	if msgs[0].Content != "Вышла новая версия\nподробности" || msgs[1].Content != "Второй пост" {
		t.Fatalf("посты не в ленте по порядку: %q, %q", msgs[0].Content, msgs[1].Content)
	}
	if c := msgs[2]; c.Author != "Митя" || c.Role != "peer" ||
		!strings.Contains(c.Content, "а что нового?") || !strings.Contains(c.Content, "«Вышла новая версия»") {
		t.Fatalf("комментарий не подписан своим постом: %+v", c)
	}
	if groupRoot := e.tgRootOf(gr); groupRoot != 0 {
		if gm, _ := st.ChatMsgs(groupRoot, 10); len(gm) != 0 {
			t.Fatalf("комментарии попали в ленту группы обсуждения: %+v", gm)
		}
	}
	// чат канала знает свой адрес: сам канал, без поста
	peer, ok := e.tgPeerOfChat(root)
	if !ok || peer.ID != 70 || peer.Topic != 0 {
		t.Fatalf("собеседник ленты не тот: %+v", peer)
	}
	// у поста своё название из первой строки — для автоответа под постом
	if eff := e.tgEffective(ch, 500); eff.TopicTitle != "Вышла новая версия" {
		t.Fatalf("название поста не из текста: %q", eff.TopicTitle)
	}
}

// Ответ под пост уходит комментарием в группу обсуждения, а человек,
// написавший в ленту канала, публикует пост — если у аккаунта есть право.
func TestReplyUnderPostGoesInGroupDiscussion(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	ch := &store.TGChat{Account: testTGAccount, ID: 70, Title: "Новости", Kind: "channel", Collect: true, Access: 7}
	_ = st.SaveTGChat(ch)
	post := e.tgEffective(ch, 500)
	// обсуждения ещё нет — писать некуда, и об этом говорится прямо
	if err := e.tgCanReply(post); err == nil || !strings.Contains(err.Error(), "обсуждения") {
		t.Fatalf("без группы обсуждения дали комментировать: %v", err)
	}
	if err := st.SaveTGPostLink(&store.TGPostLink{Account: testTGAccount, Channel: 70, Post: 500, Group: 71, Access: 8, Root: 900}); err != nil {
		t.Fatal(err)
	}
	if err := e.tgCanReply(post); err != nil {
		t.Fatalf("с группой обсуждения комментировать не дали: %v", err)
	}
	id, access, kind, topic := e.tgAddr(post)
	if id != 71 || access != 8 || kind != "group" || topic != 900 {
		t.Fatalf("ответ уходит не в группу обсуждения: %d/%d %s %d", id, access, kind, topic)
	}
	// в сам канал без права публиковать не пишется
	if err := e.tgCanReply(e.tgEffective(ch, 0)); err == nil {
		t.Fatal("дали писать прямо в канал без права публиковать")
	}
	_ = context.Background()
}

// Каналу не отвечают: семафор «ждёт ответа» на канале горел бы всегда —
// посты идут потоком, а ответа на них не бывает.
func TestChannelNotWaitsReply(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	ch := &store.TGChat{Account: testTGAccount, ID: 70, Title: "Новости", Kind: "channel", Collect: true}
	_ = st.SaveTGChat(ch)
	if peers := e.tgWatchedPeers(ch); len(peers) != 0 {
		t.Fatalf("канал попал в семафор «ждёт ответа»: %+v", peers)
	}
	dm := &store.TGChat{Account: testTGAccount, ID: 5, Title: "Вадим", Kind: "user", Collect: true}
	_ = st.SaveTGChat(dm)
	if peers := e.tgWatchedPeers(dm); len(peers) != 1 {
		t.Fatalf("личный чат выпал из семафора: %+v", peers)
	}
}

// Канал, разложенный раньше по разговорам «пост», собирается обратно в
// одну ленту: копии постов убираются (они в архиве и в Telegram), ветка
// с Сёмой и написанное не Telegram'ом переезжают к каналу, отметки
// истории снимаются — лента перечитается заново уже с картинками.
func TestConversationsPostsCollectedInFeed(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	ch := &store.TGChat{Account: testTGAccount, ID: 70, Title: "Новости", Kind: "channel", Collect: true}
	_ = st.SaveTGChat(ch)
	root, err := e.tgChatFor(ch)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetChatKind(root, store.ChatKindChannel)
	conv, err := st.CreateConversation(root, store.ChatKindPost, 36, "Пост · Свежее исследование")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddChatMsgFrom(conv, "peer", "", "Свежее исследование [photo-1.jpg]")
	_ = st.AddChatMsg(conv, "user", "Сёма, это наш лучший пост?")
	th, err := e.semaThreadFor(conv)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddChatMsg(th, "assistant", "Да, по охвату лучший")
	_ = st.SetSetting(tgHistoryKey(testTGAccount, 70, 0), "3")
	_ = st.SetSetting(tgHistoryKey(testTGAccount, 70, 36), "1")

	if err := e.tgChannelFeed(ch); err != nil {
		t.Fatal(err)
	}

	if _, err := st.Chat(conv); err == nil {
		t.Fatal("разговор поста остался")
	}
	msgs, _ := st.ChatMsgs(root, 10)
	if len(msgs) != 1 || msgs[0].Content != "Сёма, это наш лучший пост?" {
		t.Fatalf("в ленте не то: %+v", msgs)
	}
	nt, err := st.Chat(th)
	if err != nil || nt.Parent != root {
		t.Fatalf("ветка с Сёмой не переехала к каналу: %+v %v", nt, err)
	}
	if tm, _ := st.ChatMsgs(th, 5); len(tm) != 1 {
		t.Fatalf("переписка ветки потерялась: %+v", tm)
	}
	if st.Setting(tgHistoryKey(testTGAccount, 70, 0), "") != "" || st.Setting(tgHistoryKey(testTGAccount, 70, 36), "") != "" {
		t.Fatal("отметки истории не сняты — лента не перечитается")
	}
	if e.tgConvExisting(e.tgEffective(ch, 36)) != root {
		t.Fatal("комментарий к посту ищет не ленту канала")
	}
}

// Свой пост в своём канале ложится в ленту: в канале, где публикует
// владелец, всё исходящее — это и есть содержимое канала, и под ним живут
// комментарии. 17 сентября 2026 пост Сёмы вышел в «Технический форум», а
// чат канала в Сёме стоял пустым: исходящее отбрасывалось целиком. При этом
// свой пост — не входящее: ни уведомления, ни автоответа. Исходящее в
// личной переписке по-прежнему только в архив.
func TestOwnPostInChannelInFeed(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	createTGAccounts(t, st)
	ch := &store.TGChat{Account: testTGAccount, ID: 80, Title: "Технический форум", Kind: "channel", Collect: true, Access: 9, CanPost: true}
	if err := st.SaveTGChat(ch); err != nil {
		t.Fatal(err)
	}
	human := &store.TGChat{Account: testTGAccount, ID: 81, Title: "Мила", Kind: "user", Collect: true, Access: 10}
	if err := st.SaveTGChat(human); err != nil {
		t.Fatal(err)
	}
	notificationsTo := len(openTGNotifications(t, st))

	e.onTGMessage(testTGAccount, tg.Update{ChatID: 80, Kind: "channel", MsgID: 36, Out: true,
		Text: "Свежее исследование сессий с coding-агентами", Date: time.Now()})

	root := e.tgRootOf(ch)
	if msgs, _ := st.ChatMsgs(root, 5); len(msgs) != 1 || !strings.Contains(msgs[0].Content, "coding-агентами") {
		t.Fatalf("свой пост не лёг в ленту канала — чат канала остаётся пустым: %+v", msgs)
	}
	if n := len(openTGNotifications(t, st)); n != notificationsTo {
		t.Errorf("о собственном посте пришло уведомление: было %d, стало %d", notificationsTo, n)
	}

	// исходящее в личку — только архив, разговора и ленты не прибавляется
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 81, Kind: "user", MsgID: 7, Out: true, Text: "привет", Date: time.Now()})
	if chats, _ := st.Chats(); len(chats) > 0 {
		for _, c := range chats {
			if strings.Contains(c.Title, "Мила") {
				if msgs, _ := st.ChatMsgs(c.ID, 5); len(msgs) > 0 {
					t.Errorf("исходящее в личку попало в ленту: %+v", msgs)
				}
			}
		}
	}
}

func openTGNotifications(t *testing.T, st *store.Store) []*store.Notification {
	t.Helper()
	list, err := st.Notifications(true, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []*store.Notification
	for _, n := range list {
		if strings.HasPrefix(n.Key, "tg:") {
			out = append(out, n)
		}
	}
	return out
}

// Досбор привязок при старте: берутся посты канала без привязки к
// обсуждению, кроме тех, про которые Telegram ответил «обсуждения нет».
// Сорвавшаяся на «подождите» привязка (17 сентября 2026, пост 37) иначе
// не повторялась никогда.
func TestPostsWithoutBindingsToDiscussion(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	ch := &store.TGChat{Account: testTGAccount, ID: 70, Title: "Новости", Kind: "channel", Collect: true}
	_ = st.SaveTGChat(ch)
	for _, n := range []int{35, 36, 37} {
		_, _ = st.AddTGMessage(&store.TGMessage{Account: testTGAccount, ChatID: 70, MsgID: n, Topic: n, Text: "пост"})
	}
	_ = st.SaveTGPostLink(&store.TGPostLink{Account: testTGAccount, Channel: 70, Post: 36, Group: 71, Root: 900})
	_ = st.SetSetting(tgNoDiscussionKey(ch, 35), "1")

	got := e.tgPostsWithoutLink(ch)
	if len(got) != 1 || got[0] != 37 {
		t.Fatalf("к досбору не тот список: %v", got)
	}
}
