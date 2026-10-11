package engine

// Аудиофайл как файл Сёмы и голосовое MAX: запись получает текст, как
// картинка — текст OCR, но только там, где человек сам её принёс. Записи из
// почты и папок сами не расшифровываются никогда.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/max"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func hearingEngine(t *testing.T, price string) (*Engine, *store.Store, *ears) {
	t.Helper()
	e, st := newTestEngine(t, "", nil)
	e.installUsage()
	f := newEars(t, "текст записи")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, price)
	setEarsRole(t, e, "слух")
	return e, st, f
}

// Загруженная человеком запись читается сама; такая же из почты, из папки и
// от модуля — нет: деньги и чужие голоса, массово этого не делают.
func TestOnlyUploadedAudioIsHeardAutomatically(t *testing.T) {
	e, _, f := hearingEngine(t, "$0.003 / мин")
	ctx := context.Background()
	audio := func(n float64) []byte { return speechOgg(n) }

	up, _, err := e.AddFile(ctx, "заметка.ogg", "", audio(8), FileSource{Kind: store.SourceUpload, Ref: "chat:1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if up.Extracted != "текст записи" || !strings.Contains(up.Summary, "прочитано: распознавание речи · слух") {
		t.Fatalf("загруженная запись: текст %q, сводка %q", up.Extracted, up.Summary)
	}
	if f.count() != 1 {
		t.Fatalf("загрузка: %d обращений", f.count())
	}

	// то же по источникам, которым верить нельзя: почта, папка, модуль, TG-файл
	for i, kind := range []string{store.SourceMail, store.SourceFolder, store.SourceModule, store.SourceTG} {
		a, _, err := e.AddFile(ctx, "чужая.ogg", "audio/ogg", audio(float64(10+i)), FileSource{Kind: kind, Ref: "x"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if a.Extracted != "" || !strings.Contains(a.Summary, "из почты и папок сам не берёт") {
			t.Fatalf("%s: текст %q, сводка %q", kind, a.Extracted, a.Summary)
		}
	}
	if f.count() != 1 {
		t.Fatalf("записи из чужих источников ушли провайдеру: %d обращений", f.count())
	}

	// speech.files=off гасит и загрузку
	if err := e.SetSpeechFiles("off"); err != nil {
		t.Fatal(err)
	}
	off, _, err := e.AddFile(ctx, "ещё.ogg", "audio/ogg", audio(20), FileSource{Kind: store.SourceUpload}, nil)
	if err != nil || off.Extracted != "" || f.count() != 1 {
		t.Fatalf("при off: текст %q, обращений %d, %v", off.Extracted, f.count(), err)
	}
	if err := e.SetSpeechFiles("что-то"); err == nil {
		t.Fatal("принято несуществующее значение")
	}
}

// Не вышло — в карточке файла сказано, почему, а не «текст не извлечён».
func TestUploadedAudioWithoutModelExplainsWhy(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	a, _, err := e.AddFile(context.Background(), "диктофон.mp3", "audio/mpeg", speechOgg(5),
		FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Extracted != "" || a.Summary != "аудио не распознано: не выбрана модель" {
		t.Fatalf("сводка: %q", a.Summary)
	}
}

// Перечитывание файлов «без текста» аудио не расшифровывает: оно ходит по
// всей базе разом, а это была бы массовая расшифровка. Пометка «смотрели»
// при этом не мешает расшифровать запись по просьбе.
func TestRescanDoesNotHearAudio(t *testing.T) {
	e, _, f := hearingEngine(t, "")
	ctx := context.Background()
	a, _, err := e.AddFile(ctx, "из-почты.ogg", "audio/ogg", speechOgg(7), FileSource{Kind: store.SourceMail, Ref: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.RescanAttachments(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if f.count() != 0 {
		t.Fatalf("перечитывание расшифровывало запись: %d обращений", f.count())
	}
	got, _ := e.store.AttachmentByID(a.ID)
	if !strings.HasPrefix(got.Extracted, "текста в файле не нашлось") || !strings.Contains(got.Extracted, "по просьбе в чате") {
		t.Fatalf("пометка перечитывания: %q", got.Extracted)
	}
	if hasRealText(got) {
		t.Fatal("пометка принята за настоящий текст")
	}
	// по просьбе — расшифровывается, пометка настоящим текстом не считается
	text, err := e.TranscribeFile(ctx, a.ID, "chat:1")
	if err != nil || text != "текст записи" || f.count() != 1 {
		t.Fatalf("по просьбе: %q, %v, обращений %d", text, err, f.count())
	}
}

// «Расшифруй запись» в чате: `sema.files.transcribe(id)`. Только из чата;
// уже расшифрованное второй раз не оплачивается; чужая запись из почты
// расшифровывается, потому что просит сам владелец.
func TestTranscribeOnRequestFromChat(t *testing.T) {
	e, st, f := hearingEngine(t, "$0.003 / мин")
	ctx := context.Background()
	a, _, err := e.AddFile(ctx, "звонок.ogg", "audio/ogg", speechOgg(45), FileSource{Kind: store.SourceMail, Ref: "9"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// модуль с правом «files» массово расшифровывать почту не может
	if _, err := e.toolTranscribe(ctx, "module:чеки", a.ID); err == nil || !strings.Contains(err.Error(), "только по просьбе владельца в чате") {
		t.Fatalf("модуль расшифровал запись: %v", err)
	}
	if f.count() != 0 {
		t.Fatal("отказ модулю не остановил обращение")
	}

	out, err := e.toolTranscribe(ctx, "chat:3", a.ID)
	if err != nil || out != "текст записи" {
		t.Fatalf("по просьбе: %v, %v", out, err)
	}
	got, _ := st.AttachmentByID(a.ID)
	if got.Extracted != "текст записи" || !strings.Contains(got.Summary, "по просьбе") {
		t.Fatalf("файл после расшифровки: %q / %q", got.Extracted, got.Summary)
	}
	// второй раз бесплатно
	if _, err := e.toolTranscribe(ctx, "chat:3", a.ID); err != nil || f.count() != 1 {
		t.Fatalf("повтор: %v, обращений %d", err, f.count())
	}
	// расход записан на чат, полминуты «$0.003 / мин»
	var chat *store.UsageRow
	for _, r := range usageAll(st) {
		if r.Consumer == "chat:3" && r.Role == "transcribe" {
			chat = r
		}
	}
	if chat == nil || chat.Cost < 0.00224 || chat.Cost > 0.00226 {
		t.Fatalf("расход чата: %+v", chat)
	}
	// не запись — понятный отказ
	txt, _, err := e.AddFile(ctx, "текст.txt", "text/plain", []byte("просто текст"), FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.toolTranscribe(ctx, "chat:3", txt.ID); err == nil || !strings.Contains(err.Error(), "не аудиозапись") {
		t.Fatalf("текстовый файл: %v", err)
	}
	if _, err := e.toolTranscribe(ctx, "chat:3", 99999); err == nil {
		t.Fatal("несуществующий файл")
	}

	// инструмент виден модели в описании песочницы
	var found bool
	for _, tool := range e.builtinToolsFor("chat:3", nil) {
		if tool.Name == "files.transcribe" {
			found = tool.Need == "files" && strings.Contains(tool.Desc, "попросил")
		}
	}
	if !found {
		t.Fatal("files.transcribe не зарегистрирован с правом files и описанием")
	}
}

// ---- MAX ----

// maxVoiceServer — отдаёт запись по адресу вложения и считает обращения.
func maxVoiceServer(t *testing.T, contentType string, body []byte) (url string, hits func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/голос", func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// Голосовое MAX распознаётся: скачивается по адресу вложения и читается той же
// ролью, что в Telegram. В MAX нет админ-чата — любой голос чужой, и облачной
// модели он уходит только при speech.outside=all. В истории не расшифровывается.
func TestMAXVoiceIsHeard(t *testing.T) {
	e, st, f := hearingEngine(t, "")
	f.text = "созвонимся в пять"
	url, downloads := maxVoiceServer(t, "audio/ogg", speechOgg(5))
	c := &store.MAXChat{ID: 9, Title: "Коллега", Collect: true, Files: true}
	u := max.Update{ChatID: 9, MsgID: 700, Files: []max.Attach{{Kind: "AUDIO", URL: url}}}
	ctx := context.Background()

	// по умолчанию (own) чужой голос облачной модели не уходит
	got := e.maxWithFiles(ctx, c, u, c.Files, true)
	if !strings.HasPrefix(got, "🎤 голосовое, не распознано: голосовые из чужих чатов не уходят облачной модели") {
		t.Fatalf("при own: %q", got)
	}
	if f.count() != 0 {
		t.Fatalf("чужое голосовое MAX ушло облаку: %d", f.count())
	}

	// разрешили все голосовые: слышно, рядом файл
	if err := e.SetSpeechOutside("all"); err != nil {
		t.Fatal(err)
	}
	u.MsgID = 701
	got = e.maxWithFiles(ctx, c, u, c.Files, true)
	if !strings.HasPrefix(got, "🎤 созвонимся в пять «voice-701.ogg» (#") {
		t.Fatalf("при all: %q", got)
	}
	if f.count() != 1 {
		t.Fatalf("обращений %d", f.count())
	}
	files, _ := st.FilesByRef(store.SourceMAX, "9/701")
	if len(files) != 1 || files[0].Extracted != "созвонимся в пять" {
		t.Fatalf("файл голосового MAX: %+v", files)
	}
	f.mu.Lock()
	if f.models[0] != "gpt-4o-mini-transcribe" || f.langs[0] != "ru" {
		t.Errorf("запрос: %v %v", f.models, f.langs)
	}
	f.mu.Unlock()

	// без права «Файлы» текст остаётся, файла нет
	c2 := &store.MAXChat{ID: 10, Title: "Без файлов", Collect: true}
	u2 := max.Update{ChatID: 10, MsgID: 702, Files: []max.Attach{{Kind: "AUDIO", URL: url}}}
	if got := e.maxWithFiles(ctx, c2, u2, false, true); got != "🎤 созвонимся в пять" {
		t.Fatalf("без файлов: %q", got)
	}

	// история — без расшифровки и без скачивания
	before, calls := downloads(), f.count()
	if got := e.maxWithFiles(ctx, c, u, false, false); got != "[голосовое сообщение]" {
		t.Fatalf("история: %q", got)
	}
	if downloads() != before || f.count() != calls {
		t.Fatal("историю скачали или расшифровали")
	}
	// своё исходящее голосовое — тоже
	out := u
	out.Out = true
	if got := e.maxWithFiles(ctx, c, out, false, true); got != "[голосовое сообщение]" {
		t.Fatalf("исходящее: %q", got)
	}
}

func TestMAXVoiceEdgeCases(t *testing.T) {
	e, _, f := hearingEngine(t, "")
	if err := e.SetSpeechOutside("all"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := &store.MAXChat{ID: 9, Title: "Коллега", Collect: true}

	// у голосового нет адреса: как раньше, «[голосовое сообщение]»
	u := max.Update{ChatID: 9, MsgID: 1, Files: []max.Attach{{Kind: "AUDIO", Keys: []string{"_type", "token"}}}}
	if got := e.maxWithFiles(ctx, c, u, true, true); got != "[голосовое сообщение]" {
		t.Fatalf("без адреса: %q", got)
	}
	// вид не назван, и по типу это картинка — звуком не считается
	url, _ := maxVoiceServer(t, "image/png", []byte("PNG"))
	u = max.Update{ChatID: 9, MsgID: 2, Files: []max.Attach{{Kind: "UNSUPPORTED", URL: url}}}
	got := e.maxWithFiles(ctx, c, u, false, true)
	if !strings.Contains(got, "не распознано: вложение не звук") || f.count() != 0 {
		t.Fatalf("не звук: %q, обращений %d", got, f.count())
	}
	// а фотография — не голосовое вообще и идёт как раньше
	if (max.Attach{Kind: "PHOTO", URL: url}).Voice() {
		t.Fatal("фотография принята за голосовое")
	}
	// адрес не отвечает — строка с причиной, а не тишина
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "нет", 404) }))
	t.Cleanup(dead.Close)
	u = max.Update{ChatID: 9, MsgID: 3, Files: []max.Attach{{Kind: "AUDIO", URL: dead.URL}}}
	if got := e.maxWithFiles(ctx, c, u, false, true); !strings.Contains(got, "не распознано: запись не скачалась") {
		t.Fatalf("недоступный адрес: %q", got)
	}
}
