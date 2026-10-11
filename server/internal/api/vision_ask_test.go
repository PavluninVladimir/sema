package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Решать «показывать ли картинку модели», не видя картинки, нельзя: в
// вопросе стоял хэш содержимого (13 сентября 2026). В записи — имя файла
// и сама картинка рамкой.
func TestImageQuestionShowsIt(t *testing.T) {
	s, st := testServer(t)
	a := &store.Attachment{Filename: "qr.png", Path: "ab/cd/хэш", Size: 4096, MIME: "image/png"}
	if err := st.InsertAttachment(a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutNotification(&store.Notification{
		Key: "vision:ab/cd/хэш", Source: "core", Level: "info",
		Title:   "Показать картинку модели?",
		Summary: "Файл «qr.png»: на картинке не нашлось текста",
		Ref:     fmt.Sprintf("file:%d", a.ID),
		Fields:  []store.NotifyField{{Label: "Файл", Value: "qr.png"}},
		Actions: []store.NotifyAction{
			{Kind: "core", Label: "Показать", Op: "vision_show", Ref: "ab/cd/хэш"},
			{Kind: "core", Label: "Не показывать", Op: "vision_skip", Ref: "ab/cd/хэш"},
		}}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen(notifyScreen, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var строка *Component
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if strings.Contains(c.Subtitle, "Показать картинку") {
				строка = c
			}
		})
	}
	if строка == nil {
		t.Fatal("записи про картинку нет на экране")
	}
	// картинкой, а не рамкой: файл отдаётся с запретом встраивания, и в
	// рамке человек видел «сайт не позволяет установить соединение»
	if строка.Image != fmt.Sprintf("/api/files/%d", a.ID) {
		t.Errorf("картинка не показана: Image=%q", строка.Image)
	}
	if строка.Frame != "" {
		t.Errorf("картинка показана рамкой — в рамке её не видно: Frame=%q", строка.Frame)
	}
	if !строка.Expanded {
		t.Error("картинка спрятана под раскрытие — человек её не увидит")
	}
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "qr.png") {
		t.Errorf("в записи нет имени файла:\n%s", dump)
	}
}

// walkthrough — по компоненту и его детям.
func walkthrough(c *Component, fn func(*Component)) {
	fn(c)
	for i := range c.Children {
		walkthrough(&c.Children[i], fn)
	}
}
