package engine

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func shareFix(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting("auth.origin", "https://sema.example"); err != nil {
		t.Fatal(err)
	}
	return e, st
}

func addPlainFile(t *testing.T, e *Engine, name, mime, text string) *store.Attachment {
	t.Helper()
	a, _, err := e.AddFile(context.Background(), name, mime, []byte(text), FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func tokenOf(url string) string { return url[strings.LastIndex(url, "/")+1:] }

// Ссылка открывает файл до срока; подделка, чужой срок и отзыв — нет.
func TestShareOpenTamperRevoke(t *testing.T) {
	e, st := shareFix(t)
	a := addPlainFile(t, e, "скриншот.png", "image/png", "картинка бага")
	sh, err := e.ShareFile(ShareRequest{File: a.ID, Who: "модель"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sh.URL, "https://sema.example/share/") {
		t.Fatalf("адрес ссылки: %s", sh.URL)
	}
	if d := time.Until(sh.Expires); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("срок по умолчанию: %v", d)
	}
	tok := tokenOf(sh.URL)
	got, row, err := e.OpenShare(tok)
	if err != nil || got.ID != a.ID {
		t.Fatalf("ссылка не открыла файл: %v", err)
	}
	if f, _ := st.FileShare(row.ID); f.Opens != 1 {
		t.Errorf("открытий %d", f.Opens)
	}
	parts := strings.Split(tok, ".")
	for name, bad := range map[string]string{
		"подпись": parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2])),
		"срок":    parts[0] + "." + "9999999999" + "." + parts[2],
		"номер":   "999." + parts[1] + "." + parts[2],
		"мусор":   "abc",
		"пустой":  "",
	} {
		if _, _, err := e.OpenShare(bad); err == nil {
			t.Errorf("%s: подделка открыла файл", name)
		}
	}
	// адрес на экране вычисляется заново и совпадает с выданным
	if f, _ := st.FileShare(row.ID); e.ShareURL(f) != sh.URL {
		t.Errorf("адрес на экране %s, выдан %s", e.ShareURL(f), sh.URL)
	}
	if err := e.RevokeShare(row.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.OpenShare(tok); err == nil {
		t.Fatal("отозванная ссылка открыла файл")
	}
}

// Секретное ссылкой не уходит; чувствительное — только по действию
// человека; класс, поднятый после выдачи, закрывает ссылку.
func TestShareRespectsClass(t *testing.T) {
	e, _ := shareFix(t)
	secret := addPlainFile(t, e, "паспорт.png", "image/png", "скан")
	if err := e.MarkClass(store.ClassFile, secret.ID, store.ClassSecret, "вы"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ShareFile(ShareRequest{File: secret.ID, ByHuman: true}); err == nil || !strings.Contains(err.Error(), "секретное") {
		t.Errorf("секретное: %v", err)
	}
	sens, _, err := e.AddFile(context.Background(), "справка.png", "image/png", []byte("справка"),
		FileSource{Kind: store.SourceWeb, Ref: "https://example.org/справка.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.MarkClass(store.ClassFile, sens.ID, store.ClassSensitive, "вы"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ShareFile(ShareRequest{File: sens.ID}); err == nil || !strings.Contains(err.Error(), "только человек") {
		t.Errorf("чувствительное от модели: %v", err)
	}
	if _, err := e.ShareFromScreen(context.Background(), sens.ID); err != nil {
		t.Errorf("чувствительное от человека: %v", err)
	}
	// свой файл владельца (загрузка) — модель может и чувствительный
	own := addPlainFile(t, e, "скрин.png", "image/png", "почта owner@example.com")
	if err := e.MarkClass(store.ClassFile, own.ID, store.ClassSensitive, "вы"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ShareFile(ShareRequest{File: own.ID}); err != nil {
		t.Errorf("свой чувствительный файл модели: %v", err)
	}
	plain := addPlainFile(t, e, "схема.png", "image/png", "схема")
	sh, err := e.ShareFile(ShareRequest{File: plain.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.MarkClass(store.ClassFile, plain.ID, store.ClassSecret, "вы"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.OpenShare(tokenOf(sh.URL)); err == nil {
		t.Error("файл, ставший секретным после выдачи, открылся по ссылке")
	}
}

// Модель выдаёт ссылку скриптом в чате: адрес приходит в ответ, в чате —
// карточка ссылки.
func TestShareToolFromChat(t *testing.T) {
	e, st := shareFix(t)
	a := addPlainFile(t, e, "баг.png", "image/png", "картинка")
	chat, err := st.CreateChat("Баги")
	if err != nil {
		t.Fatal(err)
	}
	ctx := llm.WithConsumer(context.Background(), "chat:"+strconv.FormatInt(chat, 10))
	res := e.RunScript(ctx, "return sema.files.share("+strconv.FormatInt(a.ID, 10)+", {days: 3}).url;")
	if res.Err != nil || !strings.Contains(res.Value, "https://sema.example/share/") {
		t.Fatalf("инструмент: %v %s %s", res.Err, res.ErrText, res.Value)
	}
	e.flushCards(chat)
	msgs, _ := st.ChatMsgs(chat, 20)
	found := false
	for _, m := range msgs {
		if c, ok := ParseToolCard(m.Content); ok && c.Share != nil && c.Share.File == a.ID {
			found = true
		}
	}
	if !found {
		t.Error("карточки ссылки в чате нет")
	}
	if !strings.Contains(e.ToolsHint(), "sema.files.share") {
		t.Error("модель не знает про sema.files.share")
	}
}

// Скриншот, который владелец прислал Сёме в админ-чат Telegram, модель может
// выдать ссылкой сама, даже «чувствительный»; из чужого чата — нет.
func TestShareOwnersTelegramFile(t *testing.T) {
	e, st := shareFix(t)
	for _, c := range []*store.TGChat{
		{Account: "+7900", ID: 5, Title: "Вадим", Kind: "user", Admin: true},
		{Account: "+7900", ID: 6, Title: "Сосед", Kind: "user"},
	} {
		if err := st.SaveTGChat(c); err != nil {
			t.Fatal(err)
		}
	}
	add := func(ref string) *store.Attachment {
		a, _, err := e.AddFile(context.Background(), "photo-"+ref+".jpg", "image/jpeg", []byte("скрин "+ref),
			FileSource{Kind: store.SourceTG, Ref: ref}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.MarkClass(store.ClassFile, a.ID, store.ClassSensitive, ""); err != nil {
			t.Fatal(err)
		}
		return a
	}
	if _, err := e.ShareFile(ShareRequest{File: add("+7900/5/341").ID}); err != nil {
		t.Errorf("скриншот из админ-чата: %v", err)
	}
	if _, err := e.ShareFile(ShareRequest{File: add("+7900/6/12").ID}); err == nil {
		t.Error("чувствительный файл из чужого чата выдан моделью")
	}
}
