package tg

// Разговор с хозяином вживую: «прочитано», «печатает», ответ, который
// пишется на глазах, — всё это десятки коротких обращений подряд, и
// открывать под каждое новое соединение MTProto (рукопожатие, ключи, вход)
// значит терять секунды там, где человек смотрит на экран и ждёт. Слушатель
// (Listen) соединение и так держит, поэтому пока оно живо, эти обращения
// идут по нему; нет слушателя — прежнее «одно соединение на операцию».
//
// Безопасность по параллельности проверена по исходникам gotd v0.161:
// telegram.Client.Invoke берёт соединение под замком (invokeConn) и
// делит его с обработчиками обновлений; обработчик каждого кадра —
// отдельная горутина (mtproto/read.go), так что долгий обработчик (ответ
// модели занимает минуты) чтение ответов на наши вызовы не задерживает.

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// MessageLimit — сколько знаков Telegram принимает в одном сообщении
// (считается в единицах UTF-16, как считает он сам).
const MessageLimit = 4096

// Peer — куда обращаться: собеседник и, если это тема группы, её номер.
type Peer struct {
	ID     int64
	Access int64
	Kind   string // user | group | channel
	Topic  int    // 0 и «Общий» — без адреса темы
}

func (c *Client) setLive(cl *telegram.Client) {
	c.liveMu.Lock()
	c.live = cl
	c.liveMu.Unlock()
}

// dropLive убирает соединение только если оно всё ещё то самое: слушатель,
// переподключившись, мог уже положить новое.
func (c *Client) dropLive(cl *telegram.Client) {
	c.liveMu.Lock()
	if c.live == cl {
		c.live = nil
	}
	c.liveMu.Unlock()
}

func (c *Client) liveClient() *telegram.Client {
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	return c.live
}

// Alive — слушатель держит соединение прямо сейчас.
func (c *Client) Alive() bool { return c.liveClient() != nil }

// callTimeout — сколько ждать одного обращения к Telegram.
const callTimeout = 2 * time.Minute

// use выполняет обращение по живому соединению, а без него — по новому.
// Соединение слушателя могло оборваться посреди вызова (он как раз
// переподключается): тогда вызов повторяется один раз на новом, поэтому
// fn обязана переживать повтор — у отправки номер random_id выбирается
// снаружи, и Telegram отличает повтор от второго сообщения. Настоящий
// ответ Telegram (ошибка RPC, FLOOD_WAIT) повтором не лечится и уходит
// вызывающему как есть.
func (c *Client) use(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error {
	if cl := c.liveClient(); cl != nil {
		lctx, cancel := context.WithTimeout(ctx, callTimeout)
		err := fn(lctx, cl.API())
		cancel()
		if err == nil || ctx.Err() != nil {
			return err
		}
		if _, answered := tgerr.As(err); answered {
			return err
		}
	}
	return c.with(ctx, func(ctx context.Context, cl *telegram.Client) error { return fn(ctx, cl.API()) })
}

// ---- вызовы, которым не нужен клиент целиком: их проверяют на поддельном
// Telegram, а не рассуждением ----

// sendText — отправка текста; rid — номер random_id, выбранный снаружи.
func sendText(ctx context.Context, api *tg.Client, p Peer, rid int64, text string) (int, error) {
	req := &tg.MessagesSendMessageRequest{Peer: peerOf(p.ID, p.Access, p.Kind), Message: text, RandomID: rid}
	if p.Topic > GeneralTopic {
		// в тему сообщение кладёт не «ответ», а адрес темы: билдер gotd
		// поля top_msg_id не отдаёт, поэтому запрос собирается руками —
		// иначе сообщение выглядит ответом на служебную строку темы, а
		// при удалённом корне уезжает в «Общий»
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: p.Topic, TopMsgID: p.Topic})
	}
	return unpack.MessageID(api.MessagesSendMessage(ctx, req))
}

// editText — правка своего сообщения. «Не изменено» — не беда: Telegram
// отвечает так на правку тем же текстом, а нам нужно ровно это состояние.
func editText(ctx context.Context, api *tg.Client, p Peer, msgID int, text string) error {
	_, err := api.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
		Peer: peerOf(p.ID, p.Access, p.Kind), ID: msgID, Message: text,
	})
	if tgerr.Is(err, "MESSAGE_NOT_MODIFIED") {
		return nil
	}
	return err
}

// setAction — «печатает» и прочие статусы у собеседника.
func setAction(ctx context.Context, api *tg.Client, p Peer, action tg.SendMessageActionClass) error {
	req := &tg.MessagesSetTypingRequest{Peer: peerOf(p.ID, p.Access, p.Kind), Action: action}
	if p.Topic > GeneralTopic {
		req.SetTopMsgID(p.Topic)
	}
	_, err := api.MessagesSetTyping(ctx, req)
	return err
}

// markRead — «прочитано» до сообщения включительно: у собеседника две галочки.
func markRead(ctx context.Context, api *tg.Client, p Peer, maxID int) error {
	switch p.Kind {
	case "channel", "group":
		_, err := api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: p.ID, AccessHash: p.Access}, MaxID: maxID,
		})
		return err
	}
	_, err := api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: peerOf(p.ID, p.Access, p.Kind), MaxID: maxID})
	return err
}

// ---- методы клиента: по живому соединению, а без него по новому ----

// SendText — отправить и вернуть номер сообщения: по нему его потом правят
// и кладут в архив. Своё сообщение, отправленное по соединению слушателя,
// Telegram ему эхом не присылает (обновление возвращается в ответе на
// вызов), поэтому в архив такое сообщение кладёт тот, кто отправил.
func (c *Client) SendText(ctx context.Context, p Peer, text string) (int, error) {
	if strings.TrimSpace(text) == "" {
		return 0, fmt.Errorf("пустое сообщение")
	}
	rid := randomID()
	id := 0
	err := c.use(ctx, func(ctx context.Context, api *tg.Client) error {
		var err error
		id, err = sendText(ctx, api, p, rid, text)
		return err
	})
	return id, err
}

// EditText — заменить текст своего сообщения.
func (c *Client) EditText(ctx context.Context, p Peer, msgID int, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("пустое сообщение")
	}
	return c.use(ctx, func(ctx context.Context, api *tg.Client) error { return editText(ctx, api, p, msgID, text) })
}

// Typing — «печатает…» у собеседника; on == false снимает статус. Telegram
// гасит его сам секунд через пять, поэтому пока идёт работа, вызов повторяют.
func (c *Client) Typing(ctx context.Context, p Peer, on bool) error {
	var action tg.SendMessageActionClass = &tg.SendMessageCancelAction{}
	if on {
		action = &tg.SendMessageTypingAction{}
	}
	return c.use(ctx, func(ctx context.Context, api *tg.Client) error { return setAction(ctx, api, p, action) })
}

// LiveDraft — живой черновик (sendMessageTextDraftAction): собеседник видит
// текст, который ещё пишется, и итоговое сообщение потом просто заменяет
// его. В описании gotd это «для ботов», а принимает ли Telegram такое от
// аккаунта человека, известно только ему: отказ — обычная ошибка RPC, и
// вызывающий переходит на правки сообщения. draftID один на весь черновик.
func (c *Client) LiveDraft(ctx context.Context, p Peer, draftID int64, text string) error {
	action := &tg.SendMessageTextDraftAction{RandomID: draftID, Text: tg.TextWithEntities{Text: text}}
	return c.use(ctx, func(ctx context.Context, api *tg.Client) error { return setAction(ctx, api, p, action) })
}

// MarkRead — отметить прочитанным до этого сообщения включительно.
func (c *Client) MarkRead(ctx context.Context, p Peer, maxID int) error {
	return c.use(ctx, func(ctx context.Context, api *tg.Client) error { return markRead(ctx, api, p, maxID) })
}

// SplitText — разрезать длинный текст на сообщения не длиннее limit
// (единиц UTF-16): по абзацу, иначе по строке, иначе по пробелу, иначе
// где придётся. Пустой текст — ни одного куска; пробелы по краям кусков
// срезаются, а склейка кусков через пробел возвращает исходное.
func SplitText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 {
		limit = MessageLimit
	}
	var out []string
	for text != "" {
		if utf16Len(text) <= limit {
			out = append(out, text)
			break
		}
		cut := cutPoint(text, limit)
		out = append(out, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	return out
}

// utf16Len — длина строки так, как её считает Telegram.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// FirstChunk — начало текста, которое умещается в одно сообщение.
func FirstChunk(text string) string {
	if utf16Len(text) <= MessageLimit {
		return text
	}
	return text[:cutPoint(text, MessageLimit)]
}

// cutPoint — байтовое смещение, до которого умещается limit единиц UTF-16,
// подвинутое к ближайшей границе абзаца, строки или слова: режем там, где
// читателю не больно, но не отступая дальше половины окна — иначе кусок
// получится куцым.
func cutPoint(text string, limit int) int {
	units, end := 0, len(text)
	for i, r := range text {
		w := len(utf16.Encode([]rune{r}))
		if units+w > limit {
			end = i
			break
		}
		units += w
	}
	window := text[:end]
	floor := len(window) / 2
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(window, sep); i >= floor && i > 0 {
			return i + len(sep)
		}
	}
	return end
}

// Answered — Telegram ответил отказом на сам вызов (ошибка RPC), а не
// связь оборвалась по дороге: первое лечится решением, второе повтором.
func Answered(err error) bool {
	_, ok := tgerr.As(err)
	return ok
}

// Gone — сообщения, которое хотели поправить, больше нет или править его
// уже нельзя (удалили, вышел срок правки).
func Gone(err error) bool {
	return tgerr.Is(err, "MESSAGE_ID_INVALID", "MESSAGE_EDIT_TIME_EXPIRED", "MESSAGE_AUTHOR_REQUIRED")
}
