package tg

// Список диалогов на поддельном Telegram: он отвечает на getDialogs
// страницами по папкам и запоминает, что у него спрашивали. Так видно
// то, что на живом аккаунте молчало: канал в архиве и диалоги за
// первой сотней.

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// fakeTelegram — Invoker, который умеет только getDialogs.
type fakeTelegram struct {
	pages    map[int][]tg.MessagesDialogsClass // папка → страницы по порядку
	requests []tg.MessagesGetDialogsRequest
}

func (f *fakeTelegram) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	var buf bin.Buffer
	if err := input.Encode(&buf); err != nil {
		return err
	}
	var req tg.MessagesGetDialogsRequest
	if err := req.Decode(&buf); err != nil {
		return fmt.Errorf("ждали getDialogs: %w", err)
	}
	f.requests = append(f.requests, req)
	// какая по счёту страница этой папки
	n := 0
	for _, r := range f.requests[:len(f.requests)-1] {
		if r.FolderID == req.FolderID {
			n++
		}
	}
	var resp tg.MessagesDialogsClass = &tg.MessagesDialogsSlice{} // страницы кончились
	if pages := f.pages[req.FolderID]; n < len(pages) {
		resp = pages[n]
	}
	buf.Reset()
	box := tg.MessagesDialogsBox{Dialogs: resp}
	if err := box.Encode(&buf); err != nil {
		return err
	}
	return output.Decode(&buf)
}

// page — диалоги с их сущностями и последними сообщениями: без
// сущности итератор не соберёт смещение, без сообщения — дату.
func page(count int, peers ...tg.PeerClass) *tg.MessagesDialogsSlice {
	s := &tg.MessagesDialogsSlice{Count: count}
	for i, p := range peers {
		id := 100*len(peers) + i
		s.Dialogs = append(s.Dialogs, &tg.Dialog{Peer: p, TopMessage: id})
		s.Messages = append(s.Messages, &tg.Message{ID: id, Date: 1_700_000_000 - id, PeerID: p, Message: "…"})
	}
	return s
}

func TestArchivedChannelAndSecondHundredGetIntoList(t *testing.T) {
	photo := &tg.ChatPhotoEmpty{} // кодировщик требует фото у чата, пусть пустое
	channel := &tg.Channel{ID: 3, Title: "Новости", Username: "news", AccessHash: 33, Broadcast: true, Photo: photo, Creator: true}
	supergroup := &tg.Channel{ID: 4, Title: "Дом", AccessHash: 44, Megagroup: true, Forum: true, Photo: photo}
	person := &tg.User{ID: 5, FirstName: "Митя", AccessHash: 55}
	bot := &tg.User{ID: 6, FirstName: "Бот", Bot: true, AccessHash: 66}
	group := &tg.Chat{ID: 7, Title: "Старая группа", Photo: photo}
	// второй канал — за первой сотней основной папки
	distant := &tg.Channel{ID: 8, Title: "Далёкий канал", AccessHash: 88, Broadcast: true, Photo: photo}

	first := page(101, &tg.PeerUser{UserID: 5}, &tg.PeerUser{UserID: 6}, &tg.PeerChat{ChatID: 7}, &tg.PeerChannel{ChannelID: 4})
	first.Users = []tg.UserClass{person, bot}
	first.Chats = []tg.ChatClass{group, supergroup}
	second := page(101, &tg.PeerChannel{ChannelID: 8})
	second.Chats = []tg.ChatClass{distant}
	// архив: строка «Архив» в основной папке не собеседник, а сам канал
	// приходит только по folder_id = 1
	archive := page(1, &tg.PeerChannel{ChannelID: 3})
	archive.Chats = []tg.ChatClass{channel}

	f := &fakeTelegram{pages: map[int][]tg.MessagesDialogsClass{0: {first, second}, 1: {archive}}}
	got, err := allDialogs(context.Background(), tg.NewClient(f), 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[int64]string{}
	for _, c := range got {
		kinds[c.ID] = c.Kind
	}
	want := map[int64]string{5: "user", 7: "group", 4: "group", 8: "channel", 3: "channel"}
	for id, kind := range want {
		if kinds[id] != kind {
			t.Errorf("диалог %d: вид %q, ждали %q", id, kinds[id], kind)
		}
	}
	if _, ok := kinds[6]; ok {
		t.Error("бот попал в список собеседников")
	}
	if len(got) != len(want) {
		t.Errorf("в списке %d диалогов, ждали %d: %+v", len(got), len(want), got)
	}
	// канал — с ником и access_hash, иначе к нему не обратиться
	for _, c := range got {
		if c.ID == 3 && (c.Name != "news" || c.Access != 33) {
			t.Errorf("у канала потерялись ник или access_hash: %+v", c)
		}
		if c.ID == 4 && !c.Forum {
			t.Errorf("у супергруппы потерялся признак топиков: %+v", c)
		}
	}

	// что спрашивали: обе папки, по сотне, вторая страница со смещением
	// по последнему диалогу первой
	var folders []int
	for _, r := range f.requests {
		folders = append(folders, r.FolderID)
		if r.Limit != dialogsPage {
			t.Errorf("предел страницы %d, а Telegram отдаёт не больше %d", r.Limit, dialogsPage)
		}
	}
	if fmt.Sprint(folders) != "[0 0 0 1 1]" {
		t.Errorf("папки по запросам: %v, ждали основную трижды (две страницы и пустая) и архив дважды", folders)
	}
	secondReq := f.requests[1]
	if secondReq.OffsetID != first.Dialogs[3].(*tg.Dialog).TopMessage || secondReq.OffsetDate == 0 {
		t.Errorf("вторая страница без смещения по последнему сообщению: %+v", secondReq)
	}
	if p, ok := secondReq.OffsetPeer.(*tg.InputPeerChannel); !ok || p.ChannelID != 4 {
		t.Errorf("смещение не по последнему диалогу первой страницы: %T %+v", secondReq.OffsetPeer, secondReq.OffsetPeer)
	}
}

// Предохранитель: набрали сколько просили — дальше не листаем и в
// архив не ходим.
func TestDialogListIsNotEndless(t *testing.T) {
	var peers []tg.PeerClass
	var users []tg.UserClass
	for i := int64(1); i <= 3; i++ {
		peers = append(peers, &tg.PeerUser{UserID: i})
		users = append(users, &tg.User{ID: i, FirstName: fmt.Sprintf("Человек %d", i), AccessHash: i})
	}
	p := page(300, peers...)
	p.Users = users
	f := &fakeTelegram{pages: map[int][]tg.MessagesDialogsClass{0: {p, p, p}}}
	got, err := allDialogs(context.Background(), tg.NewClient(f), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("с пределом 2 набрали %d", len(got))
	}
	if len(f.requests) != 1 {
		t.Errorf("после предела Telegram спрашивали ещё: %d запросов", len(f.requests))
	}
}

// Канал и супергруппа у Telegram — один тип; различает их megagroup.
func TestChannelDiffersFromSupergroup(t *testing.T) {
	if got := chatOfChannel(&tg.Channel{ID: 1, Title: "Канал", Broadcast: true}); got.Kind != "channel" {
		t.Errorf("канал без megagroup стал %q", got.Kind)
	}
	if got := chatOfChannel(&tg.Channel{ID: 2, Title: "Группа", Megagroup: true}); got.Kind != "group" {
		t.Errorf("супергруппа стала %q", got.Kind)
	}
	if KindClass("channel") != "обычное" || KindClass("group") != "чувствительное" {
		t.Error("класс по умолчанию: канал — обычное, группа — чувствительное")
	}
}

// Право публиковать приходит от Telegram: владелец и админ с постами —
// да, подписчик — нет.
func TestChannelPublishRight(t *testing.T) {
	if !chatOfChannel(&tg.Channel{ID: 1, Title: "Мой", Broadcast: true, Creator: true}).CanPost {
		t.Error("владелец канала не может публиковать")
	}
	admin := &tg.Channel{ID: 2, Title: "Чужой, но админ", Broadcast: true}
	admin.SetAdminRights(tg.ChatAdminRights{PostMessages: true})
	if !chatOfChannel(admin).CanPost {
		t.Error("админ с правом постить не может публиковать")
	}
	if chatOfChannel(&tg.Channel{ID: 3, Title: "Подписка", Broadcast: true}).CanPost {
		t.Error("подписчик вдруг может публиковать")
	}
}
