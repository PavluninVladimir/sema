package engine

// Разговор с хозяином в Telegram вживую: «прочитано», «печатает», ответ,
// пишущийся на глазах. Telegram подменён записью вызовов с метками времени
// (Engine.tgVia), модель — поддельной, пишущей кусочками с паузами: так
// видно порядок и ритм, а не только итог.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/tgerr"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// tgCall — одно обращение к Telegram.
type tgCall struct {
	at   time.Time
	kind string // read | typing | typing-off | send | edit | draft
	id   int
	text string
}

// fakeTG — подменённый Telegram: помнит все вызовы по порядку и умеет
// отвечать отказами по заказу.
type fakeTG struct {
	mu    sync.Mutex
	calls []tgCall
	next  int

	editErr  func(n int) error // n — какая по счёту правка; nil — принимаем
	draftErr error
	edits    int
}

func (f *fakeTG) add(kind string, id int, text string) {
	f.calls = append(f.calls, tgCall{at: time.Now(), kind: kind, id: id, text: text})
}

func (f *fakeTG) SendText(_ context.Context, _ tg.Peer, text string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.add("send", 100+f.next, text)
	return 100 + f.next, nil
}

func (f *fakeTG) EditText(_ context.Context, _ tg.Peer, id int, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits++
	f.add("edit", id, text)
	if f.editErr != nil {
		if err := f.editErr(f.edits); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeTG) Typing(_ context.Context, _ tg.Peer, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.add("typing", 0, "")
	} else {
		f.add("typing-off", 0, "")
	}
	return nil
}

func (f *fakeTG) LiveDraft(_ context.Context, _ tg.Peer, _ int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.add("draft", 0, text)
	return f.draftErr
}

func (f *fakeTG) MarkRead(_ context.Context, _ tg.Peer, maxID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.add("read", maxID, "")
	return nil
}

func (f *fakeTG) snapshot() []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tgCall(nil), f.calls...)
}

func kinds(calls []tgCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.kind)
	}
	return out
}

// onScreen — что видит хозяин в чате после всех вызовов: сообщения по
// номерам, каждое с последним текстом.
func onScreen(calls []tgCall) []string {
	texts := map[int]string{}
	for _, c := range calls {
		if c.kind == "send" || c.kind == "edit" {
			texts[c.id] = c.text
		}
	}
	ids := make([]int, 0, len(texts))
	for id := range texts {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, texts[id])
	}
	return out
}

// streamModel — модель, пишущая ответ кусочками с паузами. turns — кусочки
// каждого вызова по порядку (второй вызов — второй ход после скриптов).
type streamModel struct {
	mu    sync.Mutex
	turns [][]string
	pause time.Duration
	err   error
	calls int
}

func (m *streamModel) Name() string { return "тестовая" }

func (m *streamModel) Complete(ctx context.Context, prompt string) (string, error) {
	return m.CompleteStream(ctx, prompt, nil)
}

func (m *streamModel) CompleteStream(ctx context.Context, prompt string, on func(string)) (string, error) {
	m.mu.Lock()
	i := m.calls
	m.calls++
	m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	if i >= len(m.turns) {
		i = len(m.turns) - 1
	}
	var b strings.Builder
	for _, d := range m.turns[i] {
		select {
		case <-ctx.Done():
			return b.String(), ctx.Err()
		case <-time.After(m.pause):
		}
		b.WriteString(d)
		if on != nil {
			on(d)
		}
	}
	llm.Report(ctx, "тестовая", "text", prompt, b.String(), llm.Usage{In: 10, Out: 5, Exact: true}, time.Now(), nil)
	return b.String(), nil
}

// talkSetup — движок с админ-чатом, подменённым Telegram и быстрыми часами.
type talkSetup struct {
	e    *Engine
	st   *store.Store
	fake *fakeTG
	chat *store.TGChat
	conv int64

	mu    sync.Mutex
	msgID int // номер последнего сообщения хозяина: 41, 42, …
}

func newTalk(t *testing.T, model llm.Provider) *talkSetup {
	t.Helper()
	e, st := newTestEngine(t, "модель", model)
	createTGAccounts(t, st)
	if err := e.SaveTGCreds("12345", "abcdef"); err != nil {
		t.Fatal(err)
	}
	chat := &store.TGChat{Account: testTGAccount, ID: 5, Title: "Вадим", Kind: "user", Access: 1, Admin: true, Collect: true}
	if err := st.SaveTGChat(chat); err != nil {
		t.Fatal(err)
	}
	fake := &fakeTG{}
	e.tgVia = fake
	e.tgPulse = 30 * time.Millisecond
	_ = st.SetSetting("tg.stream_ms", "100")
	_ = st.SetSetting("tg.stream_first", "10")
	conv, err := e.tgConvFor(chat, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &talkSetup{e: e, st: st, fake: fake, chat: chat, conv: conv, msgID: 41}
}

// ask — хозяин пишет в админ-чат: сообщение проходит тем же путём, что и
// настоящее, от приёма обновления до итога. Номер у каждого свой, иначе
// повтор считался бы дублем.
func (s *talkSetup) ask(t *testing.T, text string) error {
	t.Helper()
	s.mu.Lock()
	s.msgID++
	id := s.msgID
	s.mu.Unlock()
	s.e.onTGMessage(testTGAccount, tg.Update{ChatID: s.chat.ID, Kind: "user", MsgID: id, Text: text, From: "Вадим", Date: time.Now()})
	return nil
}

// words — ответ кусочками по слову, как пишет модель.
func words(text string) []string {
	parts := strings.SplitAfter(text, " ")
	return parts
}

// outboxRows — сколько записей лежит в журнале отправленного и что в них.
func (s *talkSetup) outboxRows() []*store.TGOutbox {
	var out []*store.TGOutbox
	for id := int64(1); id < 20; id++ {
		if o, ok := s.st.TGOutboxByID(id); ok {
			out = append(out, o)
		}
	}
	return out
}

// sentInChat — сколько раз итог лёг в переписку Сёмы строкой «ушло».
func (s *talkSetup) sentInChat(t *testing.T) []string {
	t.Helper()
	msgs, err := s.st.ChatMsgs(s.conv, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		if m.Role == "sent" {
			out = append(out, m.Content)
		}
	}
	return out
}

// Хозяин видит: сначала «прочитано», сразу за ним «печатает», и только
// потом появляется ответ. «Печатает» повторяется, пока модель медленная, а
// в конце гаснет. Ответ правится не чаще stream_ms и кончается итоговым
// текстом; в журнал и в чат итог ложится один раз.
func TestTGAdminReadTypingThenAnswerOnEyes(t *testing.T) {
	answer := "Сегодня на сервере всё спокойно: диск занят на треть, память свободна, службы работают."
	s := newTalk(t, &streamModel{turns: [][]string{words(answer)}, pause: 60 * time.Millisecond})

	if err := s.ask(t, "как дела на сервере?"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	t.Logf("обращения: %v", kinds(calls))
	if len(calls) < 4 {
		t.Fatalf("слишком мало обращений: %v", kinds(calls))
	}
	if calls[0].kind != "read" || calls[0].id != 42 {
		t.Fatalf("первым должно быть «прочитано» до сообщения 42, а было %v", calls[0])
	}
	if calls[1].kind != "typing" {
		t.Fatalf("вторым — «печатает», а было %v", calls[1])
	}
	firstSend := -1
	typing := 0
	for i, c := range calls {
		if c.kind == "send" && firstSend < 0 {
			firstSend = i
		}
		if c.kind == "typing" {
			typing++
		}
	}
	if firstSend < 2 {
		t.Fatalf("ответ ушёл раньше «печатает»: %v", kinds(calls))
	}
	if typing < 3 {
		t.Fatalf("«печатает» не повторялось, пока модель писала: %d раз", typing)
	}
	if last := calls[len(calls)-1]; last.kind != "typing-off" {
		t.Fatalf("статус не погашен в конце: %v", kinds(calls))
	}
	// после отбоя «печатает» уже не подаётся
	for i, c := range calls {
		if c.kind == "typing-off" && i != len(calls)-1 {
			t.Fatalf("после отбоя обращения не кончились: %v", kinds(calls))
		}
	}

	// ответ пришёл по частям: сначала отправка, потом правки, и не чаще stream_ms
	var stream []tgCall
	for _, c := range calls {
		if c.kind == "send" || c.kind == "edit" {
			stream = append(stream, c)
		}
	}
	if stream[0].kind != "send" || len(stream) < 3 {
		t.Fatalf("ответ не писался на глазах: %v", kinds(stream))
	}
	for i := 1; i < len(stream); i++ {
		if gap := stream[i].at.Sub(stream[i-1].at); gap < 90*time.Millisecond {
			t.Fatalf("правки идут чаще stream_ms: %s между %d и %d", gap, i-1, i)
		}
	}
	for _, c := range stream[:len(stream)-1] {
		if !strings.HasPrefix(answer, strings.TrimSpace(c.text)) {
			t.Fatalf("на глазах появилось то, чего в ответе нет: %q", c.text)
		}
	}
	if screen := onScreen(calls); len(screen) != 1 || screen[0] != answer {
		t.Fatalf("на экране хозяина: %q, ждали один итог", screen)
	}

	// журнал: итог один раз, черновые правки туда не попали
	rows := s.outboxRows()
	if len(rows) != 1 || rows[0].Text != answer || rows[0].State != "sent" {
		t.Fatalf("журнал отправленного: %+v", rows)
	}
	if got := s.sentInChat(t); len(got) != 1 || got[0] != answer {
		t.Fatalf("в переписке Сёмы итог лёг так: %q", got)
	}
	// архив хранит итог, а не первые слова
	arch, _ := s.st.TGMessages(testTGAccount, 5, 0, 10)
	var mine []string
	for _, m := range arch {
		if m.Out {
			mine = append(mine, m.Text)
		}
	}
	if len(arch) != 2 || len(mine) != 1 || mine[0] != answer {
		t.Fatalf("в архиве должны быть вопрос хозяина и итог Сёмы: %d записей, исходящие %q", len(arch), mine)
	}
}

// Служебное ограждение до хозяина не доходит ни в частичном тексте, ни в
// итоге: код скрипта и команды ему не для чтения. Второй ход после
// скрипта пишется заново, и текст первого хода в итоге не остаётся.
func TestTGAdminHidesFencesAcrossTurns(t *testing.T) {
	model := &streamModel{pause: 40 * time.Millisecond, turns: [][]string{
		{"Сейчас посчитаю ", "сумму для вас.\n", "``", "`sema:Сумма\n", "return 2 + 2;\n", "```", "\nЖдите."},
		{"Получилось ", "четыре, ", "всё готово."},
	}}
	s := newTalk(t, model)
	if err := s.ask(t, "сколько будет два плюс два?"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	for _, c := range calls {
		t.Logf("%s %d %q", c.kind, c.id, c.text)
	}
	for _, c := range calls {
		if strings.Contains(c.text, "```") || strings.Contains(c.text, "sema:") || strings.Contains(c.text, "return 2") {
			t.Fatalf("ограждение попало в Telegram: %v %q", c.kind, c.text)
		}
	}
	screen := onScreen(calls)
	if len(screen) != 1 || screen[0] != "Получилось четыре, всё готово." {
		t.Fatalf("итог на экране: %q", screen)
	}
	// первый ход всё же был показан на глазах: «Сейчас посчитаю…»
	sawFirst := false
	for _, c := range calls {
		if strings.HasPrefix(c.text, "Сейчас посчитаю") {
			sawFirst = true
		}
		if strings.Contains(c.text, "Сейчас посчитаю") && strings.Contains(c.text, "Получилось") {
			t.Fatalf("текст первого хода склеился со вторым: %q", c.text)
		}
	}
	if !sawFirst {
		t.Fatalf("первый ход не показан: %v", onScreen(calls))
	}
}

func TestVisiblePartialCutsFences(t *testing.T) {
	cases := []struct {
		name    string
		partial string
		skip    int
		want    string
	}{
		{"обычный текст", "Привет, хозяин", 0, "Привет, хозяин"},
		{"метка говорящего", "Sema: Привет", 0, "Привет"},
		{"ограждение целиком", "Смотрю.\n```sema-ssh:хост\nuptime\n```\nдальше", 0, "Смотрю."},
		{"ограждение скрипта", "Считаю ```sema:Сумма\nreturn 1", 0, "Считаю"},
		{"начало ограждения на конце", "Считаю.\n``", 0, "Считаю."},
		{"почти ограждение", "Считаю.\n```sem", 0, "Считаю."},
		{"чужое ограждение остаётся", "Пример:\n```json\n{}\n```\nвсё.", 0, "Пример:\n```json\n{}\n```\nвсё."},
		{"закрытие чужого ограждения на конце ждёт следующего кусочка", "Пример:\n```json\n{}\n```", 0, "Пример:\n```json\n{}"},
		{"прежний ход отрезан", "Смотрю.Получилось", len("Смотрю."), "Получилось"},
		{"новый ход ещё пуст", "Смотрю.", len("Смотрю."), ""},
		{"пусто", "", 0, ""},
	}
	for _, c := range cases {
		if got := visiblePartial(c.partial, c.skip); got != c.want {
			t.Errorf("%s: %q, ждали %q", c.name, got, c.want)
		}
	}
}

// Ответ длиннее одного сообщения: первое правится до первой части, остальное
// досылается новыми сообщениями, и ни одно не длиннее предела Telegram.
func TestTGAdminLongAnswerSplits(t *testing.T) {
	var paras []string
	for i := 0; i < 12; i++ {
		paras = append(paras, fmt.Sprintf("Абзац %d. ", i)+strings.Repeat("слово ", 90))
	}
	answer := strings.TrimSpace(strings.Join(paras, "\n\n"))
	if len(utf16.Encode([]rune(answer))) <= tg.MessageLimit {
		t.Fatal("проверка не про то: ответ короткий")
	}
	// кусочки по абзацу: модель «пишет» быстро, сообщение правится на ходу
	chunks := strings.SplitAfter(answer, "\n\n")
	s := newTalk(t, &streamModel{turns: [][]string{chunks}, pause: 30 * time.Millisecond})
	if err := s.ask(t, "расскажи всё"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	screen := onScreen(calls)
	if len(screen) < 2 {
		t.Fatalf("длинный итог не разделён: %d сообщений", len(screen))
	}
	for i, m := range screen {
		if n := len(utf16.Encode([]rune(m))); n > tg.MessageLimit {
			t.Fatalf("сообщение %d длиной %d — Telegram не примет", i, n)
		}
	}
	if joined := strings.Join(screen, "\n\n"); strings.Join(strings.Fields(joined), " ") != strings.Join(strings.Fields(answer), " ") {
		t.Fatalf("после склейки ответ не тот, что был")
	}
	// журнал и переписка — про целый ответ, один раз
	if rows := s.outboxRows(); len(rows) != 1 || rows[0].Text != answer {
		t.Fatalf("журнал: %d записей", len(rows))
	}
	if got := s.sentInChat(t); len(got) != 1 || got[0] != answer {
		t.Fatalf("в переписке итог лёг %d раз", len(got))
	}
}

// tg.stream = off — как раньше: одно сообщение, когда ответ готов. «Прочитано»
// и «печатает» при этом остаются.
func TestTGAdminStreamOffIsOneMessage(t *testing.T) {
	answer := "Готово, проверил: всё работает как надо."
	s := newTalk(t, &streamModel{turns: [][]string{words(answer)}, pause: 40 * time.Millisecond})
	_ = s.st.SetSetting("tg.stream", "off")
	if err := s.ask(t, "проверь"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	sends, edits := 0, 0
	for _, c := range calls {
		switch c.kind {
		case "send":
			sends++
		case "edit":
			edits++
		}
	}
	if sends != 1 || edits != 0 {
		t.Fatalf("ждали одно сообщение без правок: %v", kinds(calls))
	}
	if calls[0].kind != "read" || calls[1].kind != "typing" {
		t.Fatalf("порядок: %v", kinds(calls))
	}
	if screen := onScreen(calls); len(screen) != 1 || screen[0] != answer {
		t.Fatalf("на экране: %q", screen)
	}
	if calls[len(calls)-1].kind != "typing-off" {
		t.Fatalf("статус не погашен: %v", kinds(calls))
	}
}

// Выключатели «прочитано» и «печатает» действуют: выключенное не вызывается.
func TestTGAdminReadAndTypingSwitches(t *testing.T) {
	s := newTalk(t, &streamModel{turns: [][]string{{"Короткий ответ хозяину, без лишнего."}}, pause: 10 * time.Millisecond})
	_ = s.st.SetSetting("tg.read_mark", "0")
	_ = s.st.SetSetting("tg.typing", "0")
	if err := s.ask(t, "привет"); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.fake.snapshot() {
		if c.kind == "read" || c.kind == "typing" || c.kind == "typing-off" {
			t.Fatalf("выключенное вызвано: %v", kinds(s.fake.snapshot()))
		}
	}
	if screen := onScreen(s.fake.snapshot()); len(screen) != 1 {
		t.Fatalf("ответ не дошёл: %q", screen)
	}
}

// FLOOD_WAIT на правке ответ не роняет: Сёма выдерживает срок и
// продолжает, итог доходит целиком.
func TestTGAdminFloodWaitDoesNotDropAnswer(t *testing.T) {
	answer := "Подробный ответ на ваш вопрос: сначала одно, потом другое, и в конце третье."
	s := newTalk(t, &streamModel{turns: [][]string{words(answer)}, pause: 60 * time.Millisecond})
	// вторая правка, а с ней и итоговая, упираются в ожидание
	s.fake.editErr = func(n int) error {
		if n == 2 {
			return tgerr.New(420, "FLOOD_WAIT_1")
		}
		return nil
	}
	started := time.Now()
	if err := s.ask(t, "расскажи"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	if screen := onScreen(calls); len(screen) != 1 || screen[0] != answer {
		t.Fatalf("итог не дошёл: %q", screen)
	}
	// срок выдержан: после отказа следующее обращение не раньше, чем через секунду
	var failed time.Time
	edits := 0
	for _, c := range calls {
		if c.kind != "edit" {
			continue
		}
		edits++
		if edits == 2 {
			failed = c.at
		}
		if edits == 3 {
			if gap := c.at.Sub(failed); gap < 900*time.Millisecond {
				t.Fatalf("срок FLOOD_WAIT не выдержан: %s", gap)
			}
		}
	}
	if edits < 3 {
		t.Fatalf("после отказа правок не было: %v (ответ шёл %s)", kinds(calls), time.Since(started))
	}
	if rows := s.outboxRows(); len(rows) != 1 || rows[0].State != "sent" {
		t.Fatalf("журнал: %+v", rows)
	}
}

// Ошибка модели: «печатает» погашен, а хозяин получает строку об ошибке,
// а не тишину после «печатает…».
func TestTGAdminModelErrorTellsOwner(t *testing.T) {
	s := newTalk(t, &streamModel{err: errors.New("HTTP 500: модель упала")})
	if err := s.ask(t, "привет"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	screen := onScreen(calls)
	if len(screen) != 1 || !strings.HasPrefix(screen[0], "Не получилось ответить") {
		t.Fatalf("хозяин получил: %q", screen)
	}
	if calls[len(calls)-1].kind != "typing-off" {
		t.Fatalf("статус не погашен: %v", kinds(calls))
	}
	if rows := s.outboxRows(); len(rows) != 1 || rows[0].State != "sent" {
		t.Fatalf("журнал: %+v", rows)
	}
}

// Живой черновик: Telegram принял — на глазах идёт он, а итог уходит обычным
// сообщением один раз; «печатает» на время черновика не перебивает его.
func TestTGAdminDraftAccepted(t *testing.T) {
	answer := "Черновик Telegram показывает текст, пока он пишется, и потом сменяется итогом."
	s := newTalk(t, &streamModel{turns: [][]string{words(answer)}, pause: 60 * time.Millisecond})
	_ = s.st.SetSetting("tg.stream", "draft")
	if err := s.ask(t, "покажи черновик"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	drafts, sends, edits := 0, 0, 0
	for _, c := range calls {
		switch c.kind {
		case "draft":
			drafts++
			if strings.Contains(c.text, "```") {
				t.Fatalf("в черновик попало ограждение: %q", c.text)
			}
		case "send":
			sends++
		case "edit":
			edits++
		}
	}
	if drafts < 2 || sends != 1 || edits != 0 {
		t.Fatalf("черновик %d, отправок %d, правок %d: %v", drafts, sends, edits, kinds(calls))
	}
	if screen := onScreen(calls); len(screen) != 1 || screen[0] != answer {
		t.Fatalf("итог: %q", screen)
	}
	if s.st.Setting("tg.draft_off."+testTGAccount, "") != "" {
		t.Fatal("принятый черновик записан как отказ")
	}
}

// Telegram отказал в черновике: Сёма переходит на правки сообщения, запоминает
// отказ на этой учётке и следующий ответ даже не пробует черновик.
func TestTGAdminDraftRefusedFallsBackToEdits(t *testing.T) {
	answer := "Аккаунт человека черновик не принимает, поэтому ответ идёт правками сообщения."
	model := &streamModel{turns: [][]string{words(answer)}, pause: 60 * time.Millisecond}
	s := newTalk(t, model)
	_ = s.st.SetSetting("tg.stream", "draft")
	s.fake.draftErr = tgerr.New(400, "SEND_MESSAGE_TYPE_INVALID")
	if err := s.ask(t, "первый"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	drafts, edits := 0, 0
	for _, c := range calls {
		switch c.kind {
		case "draft":
			drafts++
		case "edit":
			edits++
		}
	}
	if drafts != 1 || edits < 1 {
		t.Fatalf("после отказа ждали правки: черновиков %d, правок %d (%v)", drafts, edits, kinds(calls))
	}
	if screen := onScreen(calls); len(screen) != 1 || screen[0] != answer {
		t.Fatalf("итог: %q", screen)
	}
	if s.st.Setting("tg.draft_off."+testTGAccount, "") == "" {
		t.Fatal("отказ не запомнен")
	}
	before := len(calls)
	if err := s.ask(t, "второй"); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.fake.snapshot()[before:] {
		if c.kind == "draft" {
			t.Fatal("черновик пробовали снова после запомненного отказа")
		}
	}
}

// Ответа текстом нет, а черновик уже показан — он не остаётся обрывком:
// его заменяет короткая строка, и она же ложится в журнал и в переписку.
func TestTGAdminCardsOnlyReplacesDraft(t *testing.T) {
	model := &streamModel{pause: 40 * time.Millisecond, turns: [][]string{
		{"Сейчас посмотрю, что там ", "на сервере творится.\n", "```sema:Проба\nreturn 1;\n```\n"},
		{"   "},
	}}
	s := newTalk(t, model)
	if err := s.ask(t, "посмотри"); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	screen := onScreen(calls)
	if len(screen) != 1 || screen[0] != tgCardsOnly {
		t.Fatalf("на экране: %q", screen)
	}
	if rows := s.outboxRows(); len(rows) != 1 || rows[0].Text != tgCardsOnly {
		t.Fatalf("журнал: %+v", rows)
	}
}

// Два вопроса подряд: ответы идут по очереди, а не вперемешку, и «печатает»
// не гаснет между ними.
func TestTGAdminAnswersInOrder(t *testing.T) {
	s := newTalk(t, &streamModel{turns: [][]string{{"Ответ хозяину на вопрос, целиком."}}, pause: 50 * time.Millisecond})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.ask(t, fmt.Sprintf("вопрос %d", i))
		}(i)
		time.Sleep(20 * time.Millisecond)
	}
	wg.Wait()
	calls := s.fake.snapshot()
	if screen := onScreen(calls); len(screen) != 2 {
		t.Fatalf("ждали два ответа, а на экране: %q", screen)
	}
	// «печатает» гаснет один раз — когда ответов не осталось
	off := 0
	for _, c := range calls {
		if c.kind == "typing-off" {
			off++
		}
	}
	if off != 1 {
		t.Fatalf("статус гасился %d раз: %v", off, kinds(calls))
	}
	if rows := s.outboxRows(); len(rows) != 2 {
		t.Fatalf("в журнале %d записей", len(rows))
	}
}

// Автоответ собеседнику: «печатает» ставится, когда ответ действительно
// готовится, а «прочитано» — только по отдельной настройке
// tg.read_mark_reply (по умолчанию выключена: отметка снимает непрочитанное у
// владельца на телефоне). При отказе (предел в час) собеседник не видит
// ничего.
func TestTGAutoReplyTypesAndReadsOnlyByOwnSetting(t *testing.T) {
	s := newTalk(t, &fakeProvider{answer: "Передам Вадиму, спасибо."})
	peer := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Митя", Kind: "user", Access: 2, Collect: true, Reply: true}
	if err := s.st.SaveTGChat(peer); err != nil {
		t.Fatal(err)
	}
	conv, err := s.e.tgConvFor(peer, 0)
	if err != nil {
		t.Fatal(err)
	}

	// по умолчанию: «печатает» есть, «прочитано» нет — даже при включённом
	// tg.read_mark, который про разговор с Сёмой
	if err := s.e.tgAutoReply(context.Background(), peer, "Привет! Ты на связи?", conv, 77); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	for _, c := range calls {
		if c.kind == "read" {
			t.Fatalf("по умолчанию «прочитано» в автоответе не ставится: %v", kinds(calls))
		}
	}
	if len(calls) < 3 || calls[0].kind != "typing" {
		t.Fatalf("порядок: %v", kinds(calls))
	}
	if screen := onScreen(calls); len(screen) != 1 || screen[0] != "Передам Вадиму, спасибо." {
		t.Fatalf("собеседник получил: %q", screen)
	}
	if calls[len(calls)-1].kind != "typing-off" {
		t.Fatalf("статус не погашен: %v", kinds(calls))
	}

	// tg.read_mark_reply=1: «прочитано» до сообщения собеседника, затем «печатает»
	_ = s.st.SetSetting("tg.read_mark_reply", "1")
	before := len(calls)
	if err := s.e.tgAutoReply(context.Background(), peer, "А ещё вопрос", conv, 78); err != nil {
		t.Fatal(err)
	}
	next := s.fake.snapshot()[before:]
	if len(next) < 3 || next[0].kind != "read" || next[0].id != 78 || next[1].kind != "typing" {
		t.Fatalf("с включённой настройкой порядок: %v", kinds(next))
	}

	// предел в час исчерпан: молчим, и ничего не «читаем» и не «печатаем» —
	// даже с включённой отметкой
	_ = s.st.SetSetting("tg.reply_max_hour", "1")
	before = len(s.fake.snapshot())
	if err := s.e.tgAutoReply(context.Background(), peer, "А сейчас?", conv, 79); err == nil {
		t.Fatal("предел не сработал")
	}
	if extra := s.fake.snapshot()[before:]; len(extra) != 0 {
		t.Fatalf("Сёма промолчал, но собеседник видел статус: %v", kinds(extra))
	}
}

// Отметка автоответа и отметка админ-чата — разные настройки: выключенная
// tg.read_mark не отнимает у автоответа его отдельную.
func TestTGReadMarksAreIndependent(t *testing.T) {
	s := newTalk(t, &fakeProvider{answer: "Хорошо."})
	_ = s.st.SetSetting("tg.read_mark", "0")
	_ = s.st.SetSetting("tg.read_mark_reply", "1")
	peer := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Митя", Kind: "user", Access: 2, Collect: true, Reply: true}
	_ = s.st.SaveTGChat(peer)
	conv, _ := s.e.tgConvFor(peer, 0)
	if err := s.e.tgAutoReply(context.Background(), peer, "привет", conv, 5); err != nil {
		t.Fatal(err)
	}
	if calls := s.fake.snapshot(); len(calls) == 0 || calls[0].kind != "read" {
		t.Fatalf("автоответ с tg.read_mark_reply=1 не отметил: %v", kinds(calls))
	}
	// а вопрос хозяину при tg.read_mark=0 не отмечается
	s2 := newTalk(t, &streamModel{turns: [][]string{{"Короткий ответ хозяину, без лишнего."}}, pause: 10 * time.Millisecond})
	_ = s2.st.SetSetting("tg.read_mark", "0")
	_ = s2.ask(t, "привет")
	for _, c := range s2.fake.snapshot() {
		if c.kind == "read" {
			t.Fatal("админ-чат отметил при выключенном tg.read_mark")
		}
	}
}

// Отправка у учётки выключена — в автоответе нет ни «прочитано», ни
// «печатает»: это тоже действия от вашего имени.
func TestTGAutoReplyNoPresenceWhenSendOff(t *testing.T) {
	s := newTalk(t, &fakeProvider{answer: "ответ"})
	acc, _ := s.st.TGAccount(testTGAccount)
	acc.CanSend = false
	if err := s.st.SaveTGAccount(acc); err != nil {
		t.Fatal(err)
	}
	peer := &store.TGChat{Account: testTGAccount, ID: 9, Title: "Митя", Kind: "user", Access: 2, Collect: true, Reply: true}
	_ = s.st.SaveTGChat(peer)
	conv, _ := s.e.tgConvFor(peer, 0)
	if err := s.e.tgAutoReply(context.Background(), peer, "привет", conv, 5); err != nil {
		t.Fatal(err)
	}
	if calls := s.fake.snapshot(); len(calls) != 0 {
		t.Fatalf("при выключенной отправке в Telegram обращались: %v", kinds(calls))
	}
}

// Итоговая правка подчиняется тому же ритму, что и остальные: если только что
// правили, она ждёт своего часа, а не идёт впритык.
func TestTGStreamFinalEditKeepsCadence(t *testing.T) {
	s := newTalk(t, &streamModel{turns: [][]string{{"нечего"}}})
	talk := s.e.tgTalkWith(s.chat, true)
	st := talk.newStream(s.conv)
	st.msgID, st.shown = 101, "Начало ответа"
	st.next = time.Now().Add(150 * time.Millisecond)
	started := time.Now()
	if err := st.finish(context.Background(), "Начало ответа и его конец."); err != nil {
		t.Fatal(err)
	}
	calls := s.fake.snapshot()
	if len(calls) != 1 || calls[0].kind != "edit" || calls[0].text != "Начало ответа и его конец." {
		t.Fatalf("обращения: %v", kinds(calls))
	}
	if waited := calls[0].at.Sub(started); waited < 140*time.Millisecond {
		t.Fatalf("итоговая правка не выждала ритм: %s", waited)
	}
}
