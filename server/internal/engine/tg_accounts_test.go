package engine

// Несколько учёток Telegram: чаты живут парой «учётка + номер», а
// слушатели — по одному на учётку, и падение одного не роняет соседние.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// testTGAccount — номер, под которым в проверках заводятся чаты.
const testTGAccount = "+70000000001"

// secondTGAccount — вторая учётка: рабочая рядом с личной.
const secondTGAccount = "+70000000002"

// createTGAccounts — учётки в базе. Без них чат некому принадлежать: номер
// чата сам по себе ничей.
func createTGAccounts(t testing.TB, st *store.Store, number ...string) {
	t.Helper()
	if len(number) == 0 {
		number = []string{testTGAccount}
	}
	for i, phone := range number {
		if err := st.SaveTGAccount(&store.TGAccount{
			Phone: phone, Title: "учётка " + phone, State: "ok", Enabled: true, Sort: i, CanSend: true,
		}); err != nil {
			t.Fatalf("учётка %s: %v", phone, err)
		}
	}
}

// Один и тот же номер чата в двух учётках — два разных разговора: права,
// архив и чат Сёмы у каждого свои. Раньше пара была «номер чата», и
// рабочий чат затёр бы личный молча.
func TestTwoAccountChatsNotConfused(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st, testTGAccount, secondTGAccount)

	private := &store.TGChat{Account: testTGAccount, ID: 7, Title: "Митя", Kind: "user", Collect: true, Class: store.ClassSecret}
	work := &store.TGChat{Account: secondTGAccount, ID: 7, Title: "Отдел кадров", Kind: "group", Class: store.ClassPlain}
	if err := st.SaveTGChat(private); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGChat(work); err != nil {
		t.Fatal(err)
	}

	got, ok := st.TGChat(testTGAccount, 7)
	if !ok || got.Title != "Митя" || !got.Collect || got.Class != store.ClassSecret {
		t.Fatalf("личный чат подменён: %+v", got)
	}
	got, ok = st.TGChat(secondTGAccount, 7)
	if !ok || got.Title != "Отдел кадров" || got.Collect {
		t.Fatalf("рабочий чат подменён: %+v", got)
	}
	// право выдаётся одному, а не обоим
	if err := st.SetTGChatRight(secondTGAccount, 7, "collect", true); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.TGChat(testTGAccount, 7); !c.Collect {
		t.Fatal("право личного чата снялось вместе с рабочим")
	}

	// архив: сообщение с тем же номером в обеих учётках — два сообщения
	for _, acc := range []string{testTGAccount, secondTGAccount} {
		fresh, err := st.AddTGMessage(&store.TGMessage{Account: acc, ChatID: 7, MsgID: 100, Text: "привет из " + acc})
		if err != nil || !fresh {
			t.Fatalf("сообщение учётки %s не легло: %v", acc, err)
		}
	}
	privateMessages, _ := st.TGMessages(testTGAccount, 7, 0, 10)
	if len(privateMessages) != 1 || !strings.Contains(privateMessages[0].Text, testTGAccount) {
		t.Fatalf("архив учёток слился: %+v", privateMessages)
	}

	// чат Сёмы у каждого свой
	privateChat, err := e.tgChatFor(private)
	if err != nil {
		t.Fatal(err)
	}
	workChat, err := e.tgChatFor(work)
	if err != nil {
		t.Fatal(err)
	}
	if privateChat == workChat {
		t.Fatal("разговоры двух учёток слились в один")
	}
	// и обратный путь ведёт туда же, откуда пришёл
	if peer, ok := e.tgPeerOfChat(workChat); !ok || peer.Account != secondTGAccount {
		t.Fatalf("обратный путь привёл в чужую учётку: %+v", peer)
	}

	// входящее в рабочую учётку не трогает личную
	e.onTGMessage(secondTGAccount, tg.Update{ChatID: 7, Kind: "group", MsgID: 101, Text: "совещание в 10", From: "Аня"})
	if msgs, _ := st.ChatMsgs(privateChat, 10); len(msgs) != 0 {
		t.Fatalf("сообщение рабочей учётки легло в личный разговор: %+v", msgs)
	}
	msgs, _ := st.ChatMsgs(workChat, 10)
	if len(msgs) != 1 || msgs[0].Content != "совещание в 10" {
		t.Fatalf("сообщение не легло в свой разговор: %+v", msgs)
	}

	// семафор «не напоминать» выключается у одного чата, а не у обоих
	if err := e.TGWaitOff(secondTGAccount, 7, true); err != nil {
		t.Fatal(err)
	}
	if e.TGWaitIsOff(testTGAccount, 7) {
		t.Fatal("«не напоминать» в рабочей учётке погасило семафор в личной")
	}
}

// Падение слушателя одной учётки не роняет остальные: надзиратель ловит
// панику, пишет, чья именно учётка упала, и поднимает её заново.
func TestListenerOneAccountNotFailsOther(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st, testTGAccount, secondTGAccount)

	var mu sync.Mutex
	falls, live := 0, 0
	secondAlive := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go e.tgSupervisor(ctx, 20*time.Millisecond, func(ctx context.Context, phone string) {
		if phone == testTGAccount {
			mu.Lock()
			falls++
			mu.Unlock()
			panic("чужой протокол сказал своё слово")
		}
		mu.Lock()
		live++
		res := live
		mu.Unlock()
		if res == 1 {
			close(secondAlive)
		}
		<-ctx.Done() // вторая учётка живёт, пока её не остановят
	})

	select {
	case <-secondAlive:
	case <-time.After(3 * time.Second):
		t.Fatal("вторая учётка не поднялась")
	}
	// первая должна падать и подниматься снова, вторая — держаться
	due := time.After(3 * time.Second)
	for {
		mu.Lock()
		n, j := falls, live
		mu.Unlock()
		if n >= 2 && j == 1 {
			break
		}
		select {
		case <-due:
			t.Fatalf("упавшая учётка не переподнялась или задела соседнюю: падений %d, живых %d", n, j)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
}

// Выключенную учётку надзиратель отпускает: собранное остаётся, новые
// сообщения не приходят.
func TestDisabledAccountNotListened(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st, testTGAccount)
	acc, _ := st.TGAccount(testTGAccount)
	acc.Enabled = false
	if err := st.SaveTGAccount(acc); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan string, 4)
	go e.tgSupervisor(ctx, 10*time.Millisecond, func(ctx context.Context, phone string) {
		called <- phone
		<-ctx.Done()
	})
	select {
	case phone := <-called:
		t.Fatalf("слушаем выключенную учётку %s", phone)
	case <-time.After(200 * time.Millisecond):
	}
	if n := e.TGListeners(); n != 0 {
		t.Fatalf("слушателей у выключенной учётки: %d", n)
	}
}

// Номер приводится к одному виду: «8 (999) 000-00-01» и «+79990000001» —
// одна учётка, а не две с одной сессией.
func TestNumberAccountInOneKind(t *testing.T) {
	if got := normalNumber("+7 (999) 000-00-01"); got != "+79990000001" {
		t.Errorf("номер: %q", got)
	}
	if normalNumber("не номер") != "" {
		t.Error("строка без цифр принята за номер")
	}
	if got := NumberDigits("+79990000001"); got != "79990000001" {
		t.Errorf("адрес экрана: %q", got)
	}
}
