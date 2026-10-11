package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вопрос «показать ли картинку модели» рождается при извлечении текста,
// то есть до того, как файл записан в базу: в записи стоял хэш, а
// картинки не показывалось вовсе (13 сентября 2026). После вставки файла
// запись дополняется именем и номером.
func TestQuestionAboutImageAmendedAfterRecord(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	hash := "5a9fbe9d4d736e24b64203b393fd2abb1c1a21b5fde29ff2b52f14499ae57187"
	// так задаёт вопрос извлечение текста: файла в базе ещё нет
	if _, err := st.PutNotification(&store.Notification{
		Key: "vision:" + hash, Source: "core", Level: "info",
		Title:   "Показать картинку модели?",
		Summary: "Файл «" + hash + "»: на картинке не нашлось текста. Пока не показывал — решайте вы.",
		Fields:  []store.NotifyField{{Label: "Файл", Value: hash}},
		Actions: []store.NotifyAction{
			{Kind: "core", Label: "Показать", Op: "vision_show", Ref: hash},
			{Kind: "core", Label: "Не показывать", Op: "vision_skip", Ref: hash},
		}}); err != nil {
		t.Fatal(err)
	}
	a := &store.Attachment{Filename: "qr.png", MIME: "image/png", Size: 4096,
		SHA256: hash, Path: "/data/files/5a/9f/" + hash, Key: "5a/9f/" + hash, Storage: "local"}
	if err := st.InsertAttachment(a); err != nil {
		t.Fatal(err)
	}
	e.AmendImageQuestion(a)
	rec, has := st.OpenNotificationByKey("vision:" + hash)
	if !has {
		t.Fatal("запись пропала")
	}
	if !strings.Contains(rec.Ref, "file:") {
		t.Errorf("в записи нет номера файла: Ref=%q — картинку показать нечем", rec.Ref)
	}
	if !strings.Contains(rec.Summary, "qr.png") {
		t.Errorf("в итоге остался хэш вместо имени: %q", rec.Summary)
	}
	var name, size string
	for _, f := range rec.Fields {
		switch f.Label {
		case "Файл":
			name = f.Value
		case "Размер":
			size = f.Value
		}
	}
	if name != "qr.png" {
		t.Errorf("поле «Файл» = %q", name)
	}
	if size == "" {
		t.Error("не сказан размер файла")
	}
	// кнопки решения остались: без них вопрос перестал бы быть вопросом
	if len(rec.Actions) < 2 {
		t.Errorf("кнопки решения потеряны: %+v", rec.Actions)
	}
}
