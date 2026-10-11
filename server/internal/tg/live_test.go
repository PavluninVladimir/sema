package tg

// Разговор вживую на поддельном Telegram: он записывает, что у него
// спросили, и отвечает как настоящий. Так видна форма запросов —
// «прочитано» до нужного номера, «печатает» с темой, правка тем же
// номером, — а не только то, что вызов «не упал».

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// recorder — Invoker, который помнит запросы и отвечает по заказу.
type recorder struct {
	requests []bin.Encoder
	failWith error
}

func (r *recorder) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	r.requests = append(r.requests, input)
	if r.failWith != nil {
		return r.failWith
	}
	var buf bin.Buffer
	var reply bin.Encoder
	switch input.(type) {
	case *tg.MessagesSendMessageRequest:
		reply = &tg.UpdatesBox{Updates: &tg.UpdateShortSentMessage{ID: 77, Out: true}}
	case *tg.MessagesEditMessageRequest:
		reply = &tg.UpdatesBox{Updates: &tg.Updates{}}
	case *tg.MessagesSetTypingRequest:
		reply = &tg.BoolBox{Bool: &tg.BoolTrue{}}
	case *tg.MessagesReadHistoryRequest:
		reply = &tg.MessagesAffectedMessages{}
	case *tg.ChannelsReadHistoryRequest:
		reply = &tg.BoolBox{Bool: &tg.BoolTrue{}}
	default:
		return fmt.Errorf("поддельный Telegram такого не знает: %T", input)
	}
	if err := reply.Encode(&buf); err != nil {
		return err
	}
	return output.Decode(&buf)
}

func TestSendReturnsMessageID(t *testing.T) {
	r := &recorder{}
	api := tg.NewClient(r)
	id, err := sendText(context.Background(), api, Peer{ID: 5, Access: 9, Kind: "user"}, 1234, "привет")
	if err != nil || id != 77 {
		t.Fatalf("номер сообщения %d, ошибка %v", id, err)
	}
	req := r.requests[0].(*tg.MessagesSendMessageRequest)
	if req.RandomID != 1234 || req.Message != "привет" {
		t.Fatalf("запрос: %+v", req)
	}
	if _, has := req.GetReplyTo(); has {
		t.Fatal("без темы у сообщения появился адрес ответа")
	}
	// в тему — адресом темы, а не «ответом»
	if _, err := sendText(context.Background(), api, Peer{ID: 5, Access: 9, Kind: "group", Topic: 7}, 1, "в тему"); err != nil {
		t.Fatal(err)
	}
	reply, has := r.requests[1].(*tg.MessagesSendMessageRequest).GetReplyTo()
	in, ok := reply.(*tg.InputReplyToMessage)
	if !has || !ok || in.TopMsgID != 7 || in.ReplyToMsgID != 7 {
		t.Fatalf("адрес темы: %+v", reply)
	}
}

func TestEditKeepsMessageID(t *testing.T) {
	r := &recorder{}
	api := tg.NewClient(r)
	if err := editText(context.Background(), api, Peer{ID: 5, Access: 9, Kind: "user"}, 77, "поправил"); err != nil {
		t.Fatal(err)
	}
	req := r.requests[0].(*tg.MessagesEditMessageRequest)
	if req.ID != 77 || req.Message != "поправил" {
		t.Fatalf("запрос: %+v", req)
	}
	// то же самое — не беда
	r.failWith = tgerr.New(400, "MESSAGE_NOT_MODIFIED")
	if err := editText(context.Background(), api, Peer{ID: 5, Kind: "user"}, 77, "поправил"); err != nil {
		t.Fatalf("правка тем же текстом стала ошибкой: %v", err)
	}
	// удалённое сообщение — ошибка, и по ней видно, что его больше нет
	r.failWith = tgerr.New(400, "MESSAGE_ID_INVALID")
	err := editText(context.Background(), api, Peer{ID: 5, Kind: "user"}, 77, "поправил")
	if err == nil || !Gone(err) || !Answered(err) {
		t.Fatalf("пропавшее сообщение не узнано: %v", err)
	}
}

func TestTypingAndDraftActions(t *testing.T) {
	r := &recorder{}
	api := tg.NewClient(r)
	p := Peer{ID: 5, Access: 9, Kind: "group", Topic: 7}
	ctx := context.Background()
	if err := setAction(ctx, api, p, &tg.SendMessageTypingAction{}); err != nil {
		t.Fatal(err)
	}
	if err := setAction(ctx, api, p, &tg.SendMessageCancelAction{}); err != nil {
		t.Fatal(err)
	}
	draft := &tg.SendMessageTextDraftAction{RandomID: 5, Text: tg.TextWithEntities{Text: "пишу"}}
	if err := setAction(ctx, api, Peer{ID: 5, Kind: "user"}, draft); err != nil {
		t.Fatal(err)
	}
	first := r.requests[0].(*tg.MessagesSetTypingRequest)
	if _, ok := first.Action.(*tg.SendMessageTypingAction); !ok {
		t.Fatalf("действие: %T", first.Action)
	}
	if top, has := first.GetTopMsgID(); !has || top != 7 {
		t.Fatalf("тема у «печатает»: %d %v", top, has)
	}
	if _, ok := r.requests[1].(*tg.MessagesSetTypingRequest).Action.(*tg.SendMessageCancelAction); !ok {
		t.Fatal("отбой не отбой")
	}
	last := r.requests[2].(*tg.MessagesSetTypingRequest)
	d, ok := last.Action.(*tg.SendMessageTextDraftAction)
	if !ok || d.RandomID != 5 || d.Text.Text != "пишу" {
		t.Fatalf("черновик: %+v", last.Action)
	}
	if _, has := last.GetTopMsgID(); has {
		t.Fatal("у личного чата появилась тема")
	}
}

func TestMarkReadUpToMessage(t *testing.T) {
	r := &recorder{}
	api := tg.NewClient(r)
	ctx := context.Background()
	if err := markRead(ctx, api, Peer{ID: 5, Access: 9, Kind: "user"}, 42); err != nil {
		t.Fatal(err)
	}
	if req := r.requests[0].(*tg.MessagesReadHistoryRequest); req.MaxID != 42 {
		t.Fatalf("до какого сообщения: %d", req.MaxID)
	}
	if err := markRead(ctx, api, Peer{ID: 6, Access: 3, Kind: "channel"}, 43); err != nil {
		t.Fatal(err)
	}
	req := r.requests[1].(*tg.ChannelsReadHistoryRequest)
	ch, ok := req.Channel.(*tg.InputChannel)
	if req.MaxID != 43 || !ok || ch.ChannelID != 6 || ch.AccessHash != 3 {
		t.Fatalf("канал: %+v", req)
	}
}

// Телеграм-кабельная арифметика: предел сообщения считается в единицах
// UTF-16, и эмодзи вне базовой плоскости — две единицы.
func TestSplitTextByLimit(t *testing.T) {
	if got := SplitText("   ", 10); got != nil {
		t.Fatalf("пустое дало куски: %q", got)
	}
	if got := SplitText("коротко", 4096); len(got) != 1 || got[0] != "коротко" {
		t.Fatalf("короткое: %q", got)
	}
	long := strings.Repeat("слово ", 300)
	parts := SplitText(long, 100)
	if len(parts) < 15 {
		t.Fatalf("длинное не разрезано: %d частей", len(parts))
	}
	for i, p := range parts {
		if n := len(utf16.Encode([]rune(p))); n > 100 || strings.TrimSpace(p) != p {
			t.Fatalf("часть %d: длина %d, %q", i, n, p)
		}
	}
	if strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != strings.Join(strings.Fields(long), " ") {
		t.Fatal("после склейки слова не те")
	}
	// режем по абзацу, если он есть в окне
	text := strings.Repeat("а", 60) + "\n\n" + strings.Repeat("б", 60)
	if got := SplitText(text, 100); len(got) != 2 || got[0] != strings.Repeat("а", 60) {
		t.Fatalf("по абзацу: %q", got)
	}
	// эмодзи — две единицы: сорок штук не помещаются в предел 60
	emoji := strings.Repeat("😀", 40)
	for _, p := range SplitText(emoji, 60) {
		if n := len(utf16.Encode([]rune(p))); n > 60 {
			t.Fatalf("эмодзи посчитаны знаками: %d единиц", n)
		}
	}
	// ни одна руна не разрезана посередине
	for _, p := range SplitText(strings.Repeat("ёж", 200), 50) {
		if strings.ContainsRune(p, '�') {
			t.Fatalf("руна разрезана: %q", p)
		}
	}
	if got := FirstChunk(strings.Repeat("я", 5000)); len(utf16.Encode([]rune(got))) > MessageLimit {
		t.Fatal("первый кусок не помещается в сообщение")
	}
}

// Голосовое узнаётся по атрибуту, а не по типу файла: музыка с тем же
// audio/ogg голосовым не считается и Сёмой не расшифровывается.
func TestFileOfVoiceAndMusic(t *testing.T) {
	doc := func(id int64, attrs ...tg.DocumentAttributeClass) *tg.Message {
		m := &tg.Message{}
		m.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
			ID: id, MimeType: "audio/ogg", Size: 999, Attributes: attrs}})
		return m
	}
	v := fileOf(doc(5, &tg.DocumentAttributeAudio{Voice: true, Duration: 12}))
	if v == nil || !v.Voice || v.Duration != 12 || v.Name != "voice-5.ogg" || v.MIME != "audio/ogg" {
		t.Fatalf("голосовое: %+v", v)
	}
	m := fileOf(doc(6, &tg.DocumentAttributeAudio{Duration: 200, Title: "песня"},
		&tg.DocumentAttributeFilename{FileName: "song.ogg"}))
	if m == nil || m.Voice || m.Name != "song.ogg" {
		t.Fatalf("музыка: %+v", m)
	}
}
