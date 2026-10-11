package engine

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// Файл, названный в вопросе номером, попадает в контекст и без
// эмбеддера: присланный в чат чек пропадал, стоило LM Studio на маке
// уснуть, а модель честно отвечала «содержимое мне не передано».
func TestNamedFileWithoutEmbedder(t *testing.T) {
	e, _ := newTestEngine(t, "модель", &fakeProvider{answer: "ладно"})
	if e.embedder != nil {
		t.Skip("проверка про работу без эмбеддера")
	}
	ctx := context.Background()
	att, _, err := e.AddFile(ctx, "чек.txt", "text/plain",
		[]byte("КАССОВЫЙ ЧЕК ИТОГ 55830.00 ЗН ККТ 0142100022007459"),
		FileSource{Kind: "upload", Ref: "chat:1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := e.RAGContextFor(ctx, "что в файле «чек.txt» (#"+strconv.FormatInt(att.ID, 10)+")?", "модель")
	if !strings.Contains(got, "55830") {
		t.Fatalf("текст названного файла не попал в контекст:\n%s", got)
	}
}
