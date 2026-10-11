package engine

import (
	"strconv"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// Удалённое в Telegram пропадает из ленты Сёмы: пост канала по событию
// канала, личное сообщение — по событию без номера чата. Реплика, от
// которой открыта ветка, не удаляется, а становится пометкой. 17 сентября
// 2026 удалённый из канала пост так и висел в ленте у Сёмы.
func TestRemoteInTelegramDisappearsFromFeed(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	createTGAccounts(t, st)
	ch := &store.TGChat{Account: testTGAccount, ID: 80, Title: "Технический форум", Kind: "channel", Collect: true, Access: 9, CanPost: true}
	human := &store.TGChat{Account: testTGAccount, ID: 81, Title: "Мила", Kind: "user", Collect: true, Access: 10}
	_ = st.SaveTGChat(ch)
	_ = st.SaveTGChat(human)

	e.onTGMessage(testTGAccount, tg.Update{ChatID: 80, Kind: "channel", MsgID: 38, Out: true, Text: "мусорный пост", Date: time.Now()})
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 80, Kind: "channel", MsgID: 39, Out: true, Text: "хороший пост", Date: time.Now()})
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 81, Kind: "user", MsgID: 500, Text: "привет", From: "Мила", Date: time.Now()})
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 81, Kind: "user", MsgID: 501, Text: "как дела?", From: "Мила", Date: time.Now()})

	feed := e.tgRootOf(ch)
	dm := e.tgRootOf(human)
	// от второго сообщения Маруси открыта ветка — оно должно остаться пометкой
	msgs, _ := st.ChatMsgs(dm, 10)
	var anchor int64
	for _, m := range msgs {
		if m.Content == "как дела?" {
			anchor = m.ID
		}
	}
	if _, err := e.ThreadFromMsg(anchor); err != nil {
		t.Fatal(err)
	}
	// предел постов: оба поста посчитаны сегодня
	_ = st.SetSetting(tgCountKey(ch, time.Now()), "2")

	e.onTGMessage(testTGAccount, tg.Update{ChatID: 80, Kind: "channel", Deleted: []int{38}})
	e.onTGMessage(testTGAccount, tg.Update{Deleted: []int{500, 501}})

	if msgs, _ := st.ChatMsgs(feed, 10); len(msgs) != 1 || msgs[0].Content != "хороший пост" {
		t.Fatalf("удалённый пост остался в ленте канала: %+v", msgs)
	}
	msgs, _ = st.ChatMsgs(dm, 10)
	if len(msgs) != 1 || msgs[0].Content != tgDeletedNote || msgs[0].ID != anchor {
		t.Fatalf("личная переписка после удаления не та: %+v", msgs)
	}
	if n, _ := strconv.Atoi(st.Setting(tgCountKey(ch, time.Now()), "")); n != 1 {
		t.Errorf("удалённый сегодня пост всё ещё занимает место в пределе: %d", n)
	}
	// архив помнит сказанное
	if arch, _ := st.TGMessages(testTGAccount, 80, 38, 5); len(arch) != 1 {
		t.Error("удаление стёрло архив")
	}
}

// Ленты, собранные до адресов, привязываются к архиву по тексту — иначе
// удаление в Telegram не уберёт уже загруженные посты.
func TestOldFeedBoundToArchive(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	ch := &store.TGChat{Account: testTGAccount, ID: 80, Title: "Канал", Kind: "channel", Collect: true}
	_ = st.SaveTGChat(ch)
	root, err := e.tgChatFor(ch)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.AddTGMessage(&store.TGMessage{Account: testTGAccount, ChatID: 80, MsgID: 25, Topic: 25, Text: "старый пост"})
	_ = st.AddChatMsgFrom(root, "peer", "", "старый пост")
	// архив старого вида: картинка пометкой [имя], в ленте — чипом с номером
	_, _ = st.AddTGMessage(&store.TGMessage{Account: testTGAccount, ChatID: 80, MsgID: 26, Topic: 26, Text: "пост с фото [photo-1.jpg]"})
	_ = st.AddChatMsgFrom(root, "peer", "", "пост с фото «photo-1.jpg» (#437)")
	_ = st.AddChatMsgFrom(root, "peer", "", "пост, которого нет в архиве")

	e.tgBindFeedRefs(testTGAccount)
	e.onTGMessage(testTGAccount, tg.Update{ChatID: 80, Kind: "channel", Deleted: []int{25, 26}})

	msgs, _ := st.ChatMsgs(root, 10)
	if len(msgs) != 1 || msgs[0].Content != "пост, которого нет в архиве" {
		t.Fatalf("старый пост не привязался и не удалился: %+v", msgs)
	}
}
