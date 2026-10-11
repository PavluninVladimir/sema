package engine

// Голосовые в Telegram: вход. Telegram подменён записью вызовов (fakeTG),
// скачивание — заданными байтами (Engine.tgFetch), OpenAI — поддельным
// сервером (ears). Проверяется путь целиком, от приёма обновления до строки
// в переписке, а не отдельная функция.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// askedModel — модель чата, которая помнит, о чём её спросили.
type askedModel struct {
	mu     sync.Mutex
	answer string
	calls  int
	last   string
}

func (m *askedModel) Name() string { return "тестовая" }

func (m *askedModel) Complete(ctx context.Context, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.last = prompt
	return m.answer, nil
}

func (m *askedModel) CompleteStream(ctx context.Context, prompt string, on func(string)) (string, error) {
	out, err := m.Complete(ctx, prompt)
	if on != nil {
		on(out)
	}
	return out, err
}

func (m *askedModel) seen() (int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.last
}

func voiceUpdate(chat int64, id int, seconds int) tg.Update {
	return tg.Update{ChatID: chat, Kind: "user", MsgID: id, From: "Митя", Date: time.Now(),
		File: &tg.File{Name: "voice-" + string(rune('0'+id%10)) + ".ogg", MIME: "audio/ogg", Voice: true, Duration: seconds}}
}

// threadTexts — что лежит в переписке Сёмы (все реплики подряд).
func threadTexts(t *testing.T, st *store.Store, chat int64) []string {
	t.Helper()
	msgs, err := st.ChatMsgs(chat, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role+": "+m.Content)
	}
	return out
}

func joined(list []string) string { return strings.Join(list, "\n") }

// Голосовое в админ-чате: «прочитано», «печатает» пока идёт распознавание,
// дальше это вопрос Сёме, как набранный текст. В переписке — «🎤 <текст>»,
// ответ уходит текстом, со стримингом.
func TestTGAdminVoiceBecomesQuestion(t *testing.T) {
	model := &askedModel{answer: "Сегодня пришло три письма, все разобраны."}
	s := newTalk(t, model)
	e := s.e
	e.installUsage()
	f := newEars(t, "какие письма пришли сегодня")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "$0.003 / мин")
	setEarsRole(t, e, "слух")
	e.tgFetch = func(ctx context.Context, c *store.TGChat, file *tg.File) ([]byte, error) {
		return speechOgg(4), nil
	}

	e.onTGMessage(testTGAccount, voiceUpdate(5, 42, 4))

	calls := s.fake.snapshot()
	t.Logf("обращения: %v", kinds(calls))
	if len(calls) < 3 || calls[0].kind != "read" || calls[0].id != 42 {
		t.Fatalf("первым должно быть «прочитано» до 42: %v", kinds(calls))
	}
	if calls[1].kind != "typing" {
		t.Fatalf("пока идёт распознавание, хозяин видит «печатает»: %v", kinds(calls))
	}
	if f.count() != 1 {
		t.Fatalf("OpenAI получил %d записей", f.count())
	}
	n, prompt := model.seen()
	if n == 0 || !strings.Contains(prompt, "🎤 какие письма пришли сегодня") {
		t.Fatalf("Сёме ушёл не вопрос хозяина: вызовов %d", n)
	}
	got := joined(threadTexts(t, s.st, e.chatForOutput(s.conv)))
	if !strings.Contains(got, "user: 🎤 какие письма пришли сегодня") {
		t.Fatalf("в переписке Сёмы нет «🎤 …»:\n%s", got)
	}
	// ответ — текстом, как на набранное
	if screen := onScreen(calls); len(screen) != 1 || screen[0] != model.answer {
		t.Fatalf("на экране хозяина: %q", screen)
	}
	if last := calls[len(calls)-1]; last.kind != "typing-off" {
		t.Fatalf("статус не погашен: %v", kinds(calls))
	}
	// архив хранит расшифровку, а не «[voice-2.ogg]»
	arch, _ := s.st.TGMessages(testTGAccount, 5, 0, 10)
	var incoming []string
	for _, m := range arch {
		if !m.Out {
			incoming = append(incoming, m.Text)
		}
	}
	if len(incoming) != 1 || incoming[0] != "🎤 какие письма пришли сегодня" {
		t.Fatalf("в архиве: %q", incoming)
	}
	// это голос владельца: деньги записаны на Telegram, «Что ушло» видит секунды
	sent, _ := s.st.SentList(false, "", "", 20)
	for _, r := range sent {
		if r.Kind == "transcribe" {
			if r.Preview != "аудио 4 с" || r.Consumer != "core:telegram" {
				t.Fatalf("«Что ушло»: %+v", r)
			}
			return
		}
	}
	t.Fatal("в «Что ушло» нет записи о распознавании")
}

// Роль не назначена — голосовое хозяина не превращается в вопрос: он узнаёт
// честно и сразу, а Сёма ничего не сочиняет.
func TestTGAdminVoiceNotHeard(t *testing.T) {
	model := &askedModel{answer: "не должен был отвечать"}
	s := newTalk(t, model)
	s.e.tgFetch = func(ctx context.Context, c *store.TGChat, file *tg.File) ([]byte, error) {
		return speechOgg(4), nil
	}
	s.e.onTGMessage(testTGAccount, voiceUpdate(5, 42, 4))

	if n, _ := model.seen(); n != 0 {
		t.Fatalf("Сёме задали вопрос из нераспознанного голосового (%d вызовов)", n)
	}
	screen := onScreen(s.fake.snapshot())
	if len(screen) != 1 || screen[0] != "Не разобрал голосовое: не выбрана модель" {
		t.Fatalf("хозяину сказали: %q", screen)
	}
	got := joined(threadTexts(t, s.st, s.e.chatForOutput(s.conv)))
	if !strings.Contains(got, "user: 🎤 голосовое, не распознано: не выбрана модель") {
		t.Fatalf("в переписке Сёмы:\n%s", got)
	}
	if sent := s.sentInChat(t); len(sent) != 1 || !strings.HasPrefix(sent[0], "Не разобрал голосовое") {
		t.Fatalf("видно ли в чате, что ушло хозяину: %q", sent)
	}
}

// Провайдер упал — причина в строке, и вопроса нет.
func TestTGAdminVoiceProviderDown(t *testing.T) {
	model := &askedModel{answer: "нет"}
	s := newTalk(t, model)
	f := newEars(t, "")
	f.status = 500
	connectEars(t, s.e, f, "слух", "gpt-4o-mini-transcribe", false, "")
	setEarsRole(t, s.e, "слух")
	s.e.tgFetch = func(ctx context.Context, c *store.TGChat, file *tg.File) ([]byte, error) { return speechOgg(4), nil }
	s.e.onTGMessage(testTGAccount, voiceUpdate(5, 42, 4))
	if n, _ := model.seen(); n != 0 {
		t.Fatal("вопрос задан при упавшем распознавании")
	}
	screen := onScreen(s.fake.snapshot())
	if len(screen) != 1 || !strings.HasPrefix(screen[0], "Не разобрал голосовое: ") || !strings.Contains(screen[0], "HTTP 500") {
		t.Fatalf("хозяину сказали: %q", screen)
	}
}

// foreignChat — обычный собеседник: не админ-чат.
func foreignChat(t *testing.T, s *talkSetup, reply bool) *store.TGChat {
	t.Helper()
	c := &store.TGChat{Account: testTGAccount, ID: 7, Title: "Митя", Kind: "user", Access: 2,
		Collect: true, Files: true, Reply: reply, Class: store.ClassSensitive}
	if err := s.st.SaveTGChat(c); err != nil {
		t.Fatal(err)
	}
	return c
}

// Голосовое из чужого чата при speech.outside=own облачной модели не уходит:
// в переписке строка «голосовое, не распознано», автоответа нет — отвечать
// на то, чего не слышал, нечем. Разрешили все голосовые — уходит, текст ложится
// в архив и ленту рядом с файлом, а автоответ видит расшифровку.
func TestTGForeignVoiceFollowsOutsidePolicy(t *testing.T) {
	model := &askedModel{answer: "Передам Вадиму."}
	s := newTalk(t, model)
	e := s.e
	f := newEars(t, "перезвони мне вечером")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "")
	setEarsRole(t, e, "слух")
	c := foreignChat(t, s, true)
	e.tgFetch = func(ctx context.Context, c *store.TGChat, file *tg.File) ([]byte, error) { return speechOgg(6), nil }

	e.onTGMessage(testTGAccount, voiceUpdate(7, 51, 6))

	if f.count() != 0 {
		t.Fatalf("чужое голосовое ушло облаку (%d записей)", f.count())
	}
	if n, _ := model.seen(); n != 0 {
		t.Fatal("на нераспознанное голосовое ответили автоответом")
	}
	want := "🎤 голосовое, не распознано: голосовые из чужих чатов не уходят облачной модели"
	arch, _ := s.st.TGMessages(testTGAccount, 7, 0, 10)
	if len(arch) != 1 || !strings.HasPrefix(arch[0].Text, want) {
		t.Fatalf("в архиве: %+v", arch)
	}
	conv, err := e.tgConvFor(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := joined(threadTexts(t, s.st, conv)); !strings.Contains(got, "peer: "+want) {
		t.Fatalf("в ленте разговора:\n%s", got)
	}

	// хозяин разрешил все голосовые: то же самое голосовое теперь слышно
	if err := e.SetSpeechOutside("all"); err != nil {
		t.Fatal(err)
	}
	e.onTGMessage(testTGAccount, voiceUpdate(7, 52, 6))
	if f.count() != 1 {
		t.Fatalf("при all запись не ушла: %d", f.count())
	}
	arch, _ = s.st.TGMessages(testTGAccount, 7, 0, 10)
	var heard string
	for _, m := range arch {
		if strings.HasPrefix(m.Text, "🎤 перезвони мне вечером") {
			heard = m.Text
		}
	}
	if heard == "" {
		t.Fatalf("расшифровка не легла в архив: %+v", arch)
	}
	// рядом с текстом — файл: запись лежит в «Файлах» вместе с расшифровкой,
	// поэтому при приёме её не платили второй раз
	if !strings.Contains(heard, "(#") {
		t.Fatalf("файл не приложен: %q", heard)
	}
	if f.count() != 1 {
		t.Fatalf("запись расшифровали дважды: %d", f.count())
	}
	files, _ := s.st.FilesByRef(store.SourceTG, testTGAccount+"/7/52")
	if len(files) != 1 || files[0].Extracted != "перезвони мне вечером" || !strings.Contains(files[0].Summary, "распознавание речи") {
		for _, f := range files {
			t.Logf("файл: %q извлечено %q сводка %q", f.Filename, f.Extracted, f.Summary)
		}
		t.Fatalf("файл голосового: %d записей", len(files))
	}
	n, prompt := model.seen()
	if n != 1 || !strings.Contains(prompt, "🎤 перезвони мне вечером") {
		t.Fatalf("автоответ не видит расшифровку: вызовов %d", n)
	}
}

// Доверенной модели (своему Whisper) чужие голоса можно всегда.
func TestTGForeignVoiceHeardByTrustedModel(t *testing.T) {
	s := newTalk(t, &askedModel{answer: "ок"})
	f := newEars(t, "своя расшифровка")
	connectEars(t, s.e, f, "дома", "whisper-large-v3", true, "")
	setEarsRole(t, s.e, "дома")
	foreignChat(t, s, false)
	s.e.tgFetch = func(ctx context.Context, c *store.TGChat, file *tg.File) ([]byte, error) { return speechOgg(3), nil }
	s.e.onTGMessage(testTGAccount, voiceUpdate(7, 61, 3))
	arch, _ := s.st.TGMessages(testTGAccount, 7, 0, 10)
	if len(arch) != 1 || !strings.HasPrefix(arch[0].Text, "🎤 своя расшифровка") {
		t.Fatalf("в архиве: %+v", arch)
	}
}

// История и исходящие голосовые не расшифровываются: это деньги за каждую
// запись, о которой никто не просил. Музыка с тем же типом файла голосовой
// не считается.
func TestTGHistoryAndOutgoingVoiceAreNotHeard(t *testing.T) {
	s := newTalk(t, &askedModel{answer: "ок"})
	f := newEars(t, "не должен звучать")
	connectEars(t, s.e, f, "слух", "gpt-4o-mini-transcribe", false, "")
	setEarsRole(t, s.e, "слух")
	c := foreignChat(t, s, false)
	fetched := 0
	s.e.tgFetch = func(ctx context.Context, c *store.TGChat, file *tg.File) ([]byte, error) {
		fetched++
		return speechOgg(3), nil
	}
	ctx := context.Background()

	u := voiceUpdate(7, 71, 12)
	if got := s.e.tgTextWithFile(ctx, c, u, false); got != "[голосовое, 12 с]" {
		t.Fatalf("история: %q", got)
	}
	// своё исходящее голосовое (с телефона) — тоже без расшифровки
	u.Out = true
	if got := s.e.tgTextWithFile(ctx, c, u, true); got != "[голосовое, 12 с]" {
		t.Fatalf("исходящее: %q", got)
	}
	s.e.onTGMessage(testTGAccount, u)
	if f.count() != 0 || fetched != 0 {
		t.Fatalf("исходящее скачано (%d) или отправлено провайдеру (%d)", fetched, f.count())
	}
	arch, _ := s.st.TGMessages(testTGAccount, 7, 0, 10)
	if len(arch) != 1 || arch[0].Text != "[голосовое, 12 с]" {
		t.Fatalf("в архиве: %+v", arch)
	}
	// музыка: audio/ogg, но не голосовое
	music := voiceUpdate(7, 72, 200)
	music.File.Voice = false
	music.File.Name = "song.ogg"
	s.e.onTGMessage(testTGAccount, music)
	if f.count() != 0 {
		t.Fatal("музыка отправлена на распознавание")
	}
}
