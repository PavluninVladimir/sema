package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Канал Telegram — одна лента, как в самом Telegram: шапка говорит, что
// написанное уйдёт постом, а у поста с картинкой картинка стоит под
// текстом, без служебного «photo-….jpg (#437)» в конце. 17 сентября 2026
// в ленте «Технического фэншуя» так и стояло имя файла вместо картинки.
func TestChannelFeedWithImage(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveTGAccount(&store.TGAccount{Phone: telegramAccount, State: "ok", Enabled: true, Every: 1, Batch: 50}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGChat(&store.TGChat{Account: telegramAccount, ID: 2, Title: "Технический форум", Kind: "channel", Collect: true, CanPost: true}); err != nil {
		t.Fatal(err)
	}
	chatID, _ := s.eng.TGOpenChat(context.Background(), telegramAccount, 2) // входа нет — чат всё равно заводится
	if chatID == 0 {
		t.Fatal("чат канала не заведён")
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
	att, _, err := s.eng.AddFile(context.Background(), "photo-1.jpg", "image/png", png,
		engine.FileSource{Kind: store.SourceTG, Ref: "проба"}, nil)
	if err != nil || att == nil {
		t.Fatalf("файл не лёг: %v", err)
	}
	if err := st.AddChatMsgFrom(chatID, "peer", "", fmt.Sprintf("Свежее исследование\n\nhttps://arxiv.org «photo-1.jpg» (#%d)", att.ID)); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen(fmt.Sprintf("chat:%d", chatID), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Subheading, "постом") || doc.Composer == nil || !strings.Contains(doc.Composer.Placeholder, "Новый пост") {
		t.Errorf("не сказано, что написанное уйдёт постом: %q / %+v", doc.Subheading, doc.Composer)
	}
	var пузырь *Component
	var найти func(list []Component)
	найти = func(list []Component) {
		for i := range list {
			if c := &list[i]; c.Type == "bubble" && strings.Contains(c.Text, "Свежее исследование") {
				пузырь = c
			}
			найти(list[i].Children)
		}
	}
	найти(doc.Components)
	if пузырь == nil {
		t.Fatal("поста в ленте нет")
	}
	if len(пузырь.Files) != 1 || !пузырь.Files[0].Image {
		t.Fatalf("картинка поста не показана: %+v", пузырь.Files)
	}
	if strings.Contains(пузырь.Text, "photo-1.jpg") || !strings.HasSuffix(пузырь.Text, "https://arxiv.org") {
		t.Errorf("в тексте поста служебная пометка файла: %q", пузырь.Text)
	}
}
