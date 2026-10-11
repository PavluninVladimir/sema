package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вопрос про картинку задаётся один раз и ждёт ответа. Перечитывание
// файлов без текста идёт по кругу, и без этого один стикер, в котором
// OCR ничего не нашёл, завёл четырнадцать тысяч записей за трое суток.
func TestAboutImageAskOnce(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	for i := 0; i < 5; i++ {
		e.askShowImage("/файлы/sticker.webp", "на картинке не нашлось текста")
	}
	list, err := e.store.Notifications(true, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, z := range list {
		if strings.HasPrefix(z.Key, "vision:") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("после пяти проходов записей о картинке: %d, ждали одну", n)
	}
}

// Ответ человека держится: вопрос без кнопок вопросом не был — закрыв
// запись, человек ничего не решал, и следующий проход спрашивал снова.
func TestDecisionByImageHeld(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	e.askShowImage("/файлы/sticker.webp", "на картинке не нашлось текста")
	list, _ := e.store.Notifications(true, 50)
	var rec *store.Notification
	for _, z := range list {
		if strings.HasPrefix(z.Key, "vision:") {
			rec = z
		}
	}
	if rec == nil {
		t.Fatal("вопроса нет")
	}
	if len(rec.Actions) != 2 {
		t.Fatalf("у вопроса %d кнопок, ждали две", len(rec.Actions))
	}
	// человек отвечает «не показывать»
	if err := e.NotifyDecide(context.Background(), rec.ID, 1, "Вадим"); err != nil {
		t.Fatal(err)
	}
	if e.ImageDecision("sticker.webp") != "пропустить" {
		t.Fatal("решение не сохранилось")
	}
	// и вопрос больше не задаётся
	e.askShowImage("/файлы/sticker.webp", "на картинке не нашлось текста")
	again, _ := e.store.Notifications(true, 50)
	for _, z := range again {
		if strings.HasPrefix(z.Key, "vision:") && z.State == store.NotifyOpen {
			t.Fatal("после ответа человека вопрос задан снова")
		}
	}
}
