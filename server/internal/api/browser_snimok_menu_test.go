package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Снимок меню с деревом разговоров — чтобы вёрстку проверять глазами, а не
// на словах. Запуск:
//
//	SEMA_BROWSER=1 SEMA_SHOT=/tmp/меню.png go test ./internal/api -run СнимокМеню
//
// Данные — как на живой машине: разговор с Сёмой, Telegram с группой-темой,
// MAX и задача.
func TestBrowserSnapshotMenu(t *testing.T) {
	файл := os.Getenv("SEMA_SHOT")
	if os.Getenv("SEMA_BROWSER") == "" || файл == "" {
		t.Skip("снимок: SEMA_BROWSER=1 SEMA_SHOT=/путь.png go test ./internal/api -run СнимокМеню")
	}
	s, st := testServer(t)
	я, _ := st.CreateChat("Telegram · Вадим")
	главный, _ := st.CreateThread(я, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(главный, "assistant", "готово")
	_ = st.AddChatMsg(я, "peer", "итог")
	дом, _ := st.CreateChat("Telegram · Анна | Закупки из Китая")
	_ = st.SetChatKind(дом, store.ChatKindGroup)
	тема, _ := st.CreateConversation(дом, store.ChatKindTopic, 1, "General")
	_ = st.AddChatMsg(тема, "peer", "привет")
	for _, name := range []string{"Telegram · Анна Ласточкина", "Telegram · Мила Лучшая подруга", "Telegram · Технический форум"} {
		ч, _ := st.CreateChat(name)
		_ = st.AddChatMsg(ч, "peer", "сообщение")
	}
	семья, _ := st.CreateChat("MAX · семья Ивановых")
	_ = st.AddChatMsg(семья, "peer", "ужин")
	задача, _ := st.CreateChat("Задача: Собери новости и важное из писем")
	_ = st.AddChatMsg(задача, "assistant", "сводка")
	мила := int64(0)
	chats, _ := st.Chats()
	for _, c := range chats {
		if strings.Contains(c.Title, "Мила") {
			мила = c.ID
		}
	}

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()

	addr := "chat:" + strconv.FormatInt(мила, 10)
	// SEMA_SHOT_SCREEN — другой экран, например «inbox»: у «Почты» свои дети
	if э := os.Getenv("SEMA_SHOT_SCREEN"); э != "" {
		addr = э
	}
	// тема: SEMA_SHOT_THEME=dark снимает тёмную
	схема := "light"
	if os.Getenv("SEMA_SHOT_THEME") == "dark" {
		схема = "dark"
	}
	var снимок []byte
	if err := chromedp.Run(ctx,
		emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: схема}}),
		chromedp.EmulateViewport(1290, 1100, chromedp.EmulateScale(2)),
		chromedp.Navigate(srv.URL+"/#screen="+addr)); err != nil {
		t.Fatalf("открыть: %v", err)
	}
	waitScreen(t, ctx, addr)
	if err := chromedp.Run(ctx,
		chromedp.Sleep(600*time.Millisecond),
		chromedp.Screenshot(`#menu`, &снимок, chromedp.NodeVisible, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("снимок: %v", err)
	}
	if err := os.WriteFile(файл, снимок, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("снимок меню: %s", файл)
}
