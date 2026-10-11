package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Публикация в канал из модуля держит границы сама, а не полагается на
// добрую волю модуля: только каналы, только с правами от человека, не
// больше предела в сутки. Все отказы — до сети, клиента Telegram тут нет.
func TestPublishInChannelHoldsBounds(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	createTGAccounts(t, st)
	for _, c := range []*store.TGChat{
		{Account: testTGAccount, ID: 1, Title: "Технический форум", Kind: "channel"},
		{Account: testTGAccount, ID: 2, Title: "Мила", Kind: "user", Send: true},
		{Account: testTGAccount, ID: 3, Title: "Разрешённый", Kind: "channel", Send: true, CanPost: true},
	} {
		if err := st.SaveTGChat(c); err != nil {
			t.Fatal(err)
		}
	}
	post := func(channel, text string) error {
		_, err := e.toolTGPost(context.Background(), []any{channel, text})
		return err
	}
	waitDenial := func(what, channel, text, word string) {
		t.Helper()
		err := post(channel, text)
		if err == nil {
			t.Fatalf("%s: публикация прошла, а должна была получить отказ", what)
		}
		if !strings.Contains(err.Error(), word) {
			t.Errorf("%s: отказ без причины %q: %v", what, word, err)
		}
	}

	waitDenial("личный чат", "Мила", "новость", "не канал")
	waitDenial("чужой канал", "Нет такого", "новость", "нет среди")
	waitDenial("канал без прав", "Технический форум", "новость", "не разрешено")
	waitDenial("пустой пост", "Разрешённый", "  ", "пустой")
	waitDenial("длинный пост", "Разрешённый", strings.Repeat("я", 4097), "4096")

	// пост с картинкой: подпись к фото Telegram ограничивает 1024 знаками
	image, _, err := e.AddFile(context.Background(), "обложка.png", "image/png",
		[]byte("\x89PNG\r\n\x1a\nкартинка"), FileSource{Kind: "module", Ref: "тест"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	withImage := func(channel, text string, number int64) error {
		_, err := e.toolTGPost(context.Background(), []any{channel, text, map[string]any{"image": number}})
		return err
	}
	// длинный пост с картинкой идёт текстом с превью, а превью нужен
	// публичный адрес: без него — понятный отказ, а не молчаливая обрезка
	if err := withImage("Разрешённый", strings.Repeat("я", 1025), image.ID); err == nil || !strings.Contains(err.Error(), "публичного адреса") {
		t.Errorf("длинный пост с картинкой без адреса не получил отказ: %v", err)
	}
	// адрес есть — пост проходит все проверки длины и доходит до отправки
	// (здесь клиента Telegram нет, поэтому отказ уже не про длину)
	_ = st.AddFileSource(image.ID, "web", "https://v3.fal.media/обложка.png", "")
	if err := withImage("Разрешённый", strings.Repeat("я", 2000), image.ID); err == nil ||
		strings.Contains(err.Error(), "публичного адреса") || strings.Contains(err.Error(), "4096") {
		t.Errorf("длинный пост с картинкой и адресом не дошёл до отправки: %v", err)
	}
	if err := withImage("Разрешённый", strings.Repeat("я", 4097), image.ID); err == nil || !strings.Contains(err.Error(), "4096") {
		t.Errorf("пост длиннее 4096 знаков прошёл: %v", err)
	}
	if err := withImage("Разрешённый", "новость", 999999); err == nil || !strings.Contains(err.Error(), "нет среди файлов") {
		t.Errorf("несуществующая картинка прошла: %v", err)
	}
	text, _, _ := e.AddFile(context.Background(), "заметка.txt", "text/plain", []byte("просто текст"), FileSource{Kind: "module", Ref: "тест"}, nil)
	if err := withImage("Разрешённый", "новость", text.ID); err == nil || !strings.Contains(err.Error(), "только картинку") {
		t.Errorf("к посту приложен не-картинка: %v", err)
	}

	// предел в сутки: счёт уже набран — пост не уходит, и сеть не нужна
	channel, _ := st.TGChat(testTGAccount, 3)
	_ = st.SetSetting(tgCountKey(channel, time.Now()), "3")
	waitDenial("предел в сутки", "Разрешённый", "новость", "предел 3")
}

// Право «telegram» названо человеку словами и описано: без этого права
// модуль не получает инструмент публикации вовсе.
func TestGrantPublishDescribed(t *testing.T) {
	if primitives["telegram"] == "" || primitiveWhy["telegram"] == "" {
		t.Fatal("у права «telegram» нет названия или объяснения для человека")
	}
	for _, tool := range (&Engine{}).builtinToolsFor("тест", nil) {
		if tool.Name == "telegram.post" {
			if tool.Need != "telegram" {
				t.Fatalf("публикация доступна без права «telegram»: Need = %q", tool.Need)
			}
			return
		}
	}
	t.Fatal("инструмента telegram.post нет")
}
