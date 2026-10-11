package tg

// Клиент Telegram от вашего имени: бот видит только то, что написали ему, а
// нужны ваши чаты. MTProto на чистом Go (gotd), без cgo — иначе ломается
// кросс-компиляция проекта.
//
// Сессия отдаётся наружу одной строкой и хранится шифрованной: значит ключи
// и сессия живут там же, где остальные секреты, а не в файле рядом с базой.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// FloodWait — Telegram просит подождать: сколько. Это не отказ, а
// «не сейчас», и путать одно с другим нельзя.
func FloodWait(err error) (time.Duration, bool) { return tgerr.AsFloodWait(err) }

// Creds — ключи приложения с my.telegram.org.
type Creds struct {
	AppID   int
	AppHash string
}

// memSession — хранилище сессии в памяти: строку кладёт и забирает Сёма, а
// шифрование делает хранилище секретов.
type memSession struct {
	mu   sync.Mutex
	data []byte
}

func (m *memSession) LoadSession(_ context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.data) == 0 {
		return nil, session.ErrNotFound
	}
	out := make([]byte, len(m.data))
	copy(out, m.data)
	return out, nil
}

func (m *memSession) StoreSession(_ context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append([]byte(nil), data...)
	return nil
}

func (m *memSession) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return string(m.data)
}

// Client — соединение с Telegram под одной сессией.
type Client struct {
	creds   Creds
	storage *memSession

	// живое соединение слушателя (см. live.go): пока оно есть, разговор с
	// хозяином — «прочитано», «печатает», ответ и его правки — идёт по нему
	liveMu sync.Mutex
	live   *telegram.Client
}

func New(creds Creds, sessionData string) *Client {
	c := &Client{creds: creds, storage: &memSession{}}
	if sessionData != "" {
		c.storage.data = []byte(sessionData)
	}
	return c
}

// Session — строка сессии для хранения: после входа она меняется, и её
// нужно сохранить обратно шифрованной.
func (c *Client) Session() string { return c.storage.String() }

// with — одно соединение на операцию: держать постоянное подключение из
// сервера, который перезапускается, смысла нет.
func (c *Client) with(ctx context.Context, fn func(ctx context.Context, cl *telegram.Client) error) error {
	if c.creds.AppID == 0 || c.creds.AppHash == "" {
		return fmt.Errorf("не заданы api_id и api_hash: возьмите их на my.telegram.org")
	}
	cl := telegram.NewClient(c.creds.AppID, c.creds.AppHash, telegram.Options{SessionStorage: c.storage})
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return cl.Run(cctx, func(ctx context.Context) error { return fn(ctx, cl) })
}

// SendCode просит Telegram прислать код. Возвращает хэш кода — он нужен,
// чтобы этот же код подтвердить.
func (c *Client) SendCode(ctx context.Context, phone string) (string, error) {
	var hash string
	err := c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		sent, err := cl.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
		if err != nil {
			return err
		}
		code, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("Telegram ответил не тем: %T", sent)
		}
		hash = code.PhoneCodeHash
		return nil
	})
	return hash, err
}

// SignIn подтверждает код. Ошибка про пароль означает второй фактор.
func (c *Client) SignIn(ctx context.Context, phone, hash, code string) error {
	return c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		_, err := cl.Auth().SignIn(ctx, phone, code, hash)
		return err
	})
}

// NeedPassword — включён второй фактор, нужен пароль.
func NeedPassword(err error) bool { return errors.Is(err, auth.ErrPasswordAuthNeeded) }

func (c *Client) SignInPassword(ctx context.Context, password string) error {
	return c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		_, err := cl.Auth().Password(ctx, password)
		return err
	})
}

// Ready — есть ли рабочая сессия; возвращает, кем Сёма представляется.
func (c *Client) Ready(ctx context.Context) (string, error) {
	var who string
	err := c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		self, err := cl.Self(ctx)
		if err != nil {
			return err
		}
		who = strings.TrimSpace(self.FirstName + " " + self.LastName)
		if who == "" {
			who = self.Username
		}
		return nil
	})
	return who, err
}

// Chat — строка списка чатов.
type Chat struct {
	ID     int64
	Title  string
	Name   string // @username: по нему собеседника называют и человек, и модель
	Kind   string // user | group | channel
	Access int64  // access_hash: без него к чату не обратиться
	Forum  bool   // группа с топиками: переписка разложена по темам
	// CanPost — аккаунт может публиковать в этом канале: владелец или
	// админ с правом постить. Без него «писать в канал» не о чем говорить
	CanPost bool
}

// Topic — топик группы: номер и название. Номер — это id первого
// сообщения темы; у «Общего» он всегда 1.
type Topic struct {
	ID    int
	Title string
}

// GeneralTopic — номер топика «Общий» у группы с темами.
const GeneralTopic = 1

// Topics — темы группы с топиками. Зовётся при обновлении списка чатов;
// у обычных групп тем нет, и спрашивать их незачем.
func (c *Client) Topics(ctx context.Context, chatID, access int64) ([]Topic, error) {
	var out []Topic
	err := c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		res, err := cl.API().MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{
			Peer: peerOf(chatID, access, "group"), Limit: 100,
		})
		if err != nil {
			return err
		}
		for _, t := range res.Topics {
			if v, ok := t.(*tg.ForumTopic); ok {
				out = append(out, Topic{ID: v.ID, Title: v.Title})
			}
		}
		return nil
	})
	return out, err
}

// dialogsPage — сколько диалогов Telegram отдаёт за один запрос. Больше
// он не даёт: предел молча режется до ста, и «300» в запросе означало
// «первая сотня» — всё, что стояло ниже, в список не попадало.
const dialogsPage = 100

// dialogFolders — папки Telegram, из которых читается список: основная
// (0) и архив (1). Без folder_id Telegram отдаёт только основную, а
// архив — одной строкой «Архив» без его чатов; в архив же люди убирают
// как раз каналы, и на живом аккаунте каналов в списке не было ни одного.
var dialogFolders = []int{0, 1}

// errEnough — предохранитель сработал: диалогов набрано столько, сколько
// просили, дальше не листаем.
var errEnough = errors.New("достаточно")

// Chats — список диалогов целиком: постранично, из основной папки и из
// архива. max — предохранитель на общее число (0 — без предела): один
// заход не должен листать тысячи диалогов чужого аккаунта.
func (c *Client) Chats(ctx context.Context, max int) ([]Chat, error) {
	var out []Chat
	err := c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		var err error
		out, err = allDialogs(ctx, tg.NewClient(cl), max)
		return err
	})
	return out, err
}

// allDialogs — обход диалогов по папкам и страницам. Отдельно от
// соединения: так его можно проверить на поддельном Telegram, а не
// рассуждением. Смещение страницы (дата, номер и собеседник последнего
// сообщения) ведёт итератор gotd.
func allDialogs(ctx context.Context, api *tg.Client, max int) ([]Chat, error) {
	seen := map[int64]bool{}
	var out []Chat
	for _, folder := range dialogFolders {
		q := dialogs.NewQueryBuilder(api).GetDialogs().BatchSize(dialogsPage).FolderID(folder)
		err := q.ForEach(ctx, func(_ context.Context, el dialogs.Elem) error {
			ch, ok := chatOfDialog(el)
			if !ok || seen[ch.ID] {
				return nil
			}
			seen[ch.ID] = true
			out = append(out, ch)
			if max > 0 && len(out) >= max {
				return errEnough
			}
			return nil
		})
		if errors.Is(err, errEnough) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("папка %d: %w", folder, err)
		}
	}
	return out, nil
}

// chatOfDialog — собеседник одного диалога. Идём от диалогов, а не от
// списка людей в ответе: там лежат и авторы последних сообщений групп,
// с которыми у человека никакой переписки нет. Строка «Архив»
// (dialogFolder) — не собеседник.
func chatOfDialog(el dialogs.Elem) (Chat, bool) {
	d, ok := el.Dialog.(*tg.Dialog)
	if !ok {
		return Chat{}, false
	}
	switch p := d.Peer.(type) {
	case *tg.PeerUser:
		if u, ok := el.Entities.User(p.UserID); ok {
			return chatOfUser(u)
		}
	case *tg.PeerChat:
		if ch, ok := el.Entities.Chat(p.ChatID); ok {
			return Chat{ID: ch.ID, Title: ch.Title, Kind: "group"}, true
		}
	case *tg.PeerChannel:
		if ch, ok := el.Entities.Channel(p.ChannelID); ok {
			return chatOfChannel(ch), true
		}
	}
	return Chat{}, false
}

// chatOfChannel — у Telegram и супергруппа, и канал приходят одним
// типом channel; различает их признак megagroup.
func chatOfChannel(v *tg.Channel) Chat {
	kind := "channel"
	if v.Megagroup {
		kind = "group"
	}
	rights, _ := v.GetAdminRights()
	return Chat{ID: v.ID, Title: v.Title, Name: v.Username, Kind: kind, Access: v.AccessHash, Forum: v.Forum,
		CanPost: v.Creator || rights.PostMessages}
}

// chatOfUser — личный чат; боты не нужны, безымянный пропускается.
func chatOfUser(v *tg.User) (Chat, bool) {
	if v.Bot {
		return Chat{}, false
	}
	title := userName(v)
	if title == "" {
		return Chat{}, false
	}
	return Chat{ID: v.ID, Title: title, Name: v.Username, Kind: "user", Access: v.AccessHash}, true
}

// KindClass — какой класс чувствительности ставить чату по умолчанию:
// личное — чувствительное, каналы — обычное.
func KindClass(kind string) string {
	if kind == "channel" {
		return "обычное"
	}
	return "чувствительное"
}

// Send — отправить сообщение в чат от имени человека. Право на это даёт
// человек в настройках чата: клиент здесь ничего не решает, он только
// умеет отправить. topic — номер темы у группы с топиками: сообщение
// уходит ответом на корень темы, так Telegram и кладёт его в топик;
// 0 и «Общий» — без адреса.
//
// Соединение своё, новое: эхо отправленного приходит слушателю как обычное
// обновление, и архив пополняется само. Разговору с хозяином, где нужен
// номер отправленного сообщения и скорость, служит SendText.
func (c *Client) Send(ctx context.Context, chatID, access int64, kind string, topic int, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("пустое сообщение")
	}
	rid := randomID()
	return c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		_, err := sendText(ctx, cl.API(), Peer{ID: chatID, Access: access, Kind: kind, Topic: topic}, rid, text)
		return err
	})
}

// SendWithImagePreview — длинный пост с картинкой: текст до 4096 знаков и
// над ним крупное превью картинки по публичному адресу. Фото с подписью так
// не отправить — подпись к фото Telegram ограничивает 1024 знаками, — а
// превью страницы идёт в обычном текстовом сообщении, и предел у него тот
// же, что у текста. Картинку Telegram забирает сам по адресу.
func (c *Client) SendWithImagePreview(ctx context.Context, chatID, access int64, kind string, topic int, text, imageURL string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("пустое сообщение")
	}
	if !strings.HasPrefix(imageURL, "https://") {
		return fmt.Errorf("у картинки нет публичного адреса https — превью не показать")
	}
	return c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		api := tg.NewClient(cl)
		// Telegram собирает превью по адресу сам и не сразу: адрес, который
		// он ещё не видел, отправка встречает WEBPAGE_NOT_FOUND. 17 сентября
		// 2026 так не ушла статья в канал — картинка fal открывалась, а
		// Telegram её ещё не забрал. Поэтому сначала просим превью и ждём,
		// пока оно перестанет быть «в работе»
		waitPreview(ctx, api, imageURL)
		req := &tg.MessagesSendMediaRequest{
			Peer:        peerOf(chatID, access, kind),
			Media:       &tg.InputMediaWebPage{URL: imageURL, ForceLargeMedia: true},
			Message:     text,
			RandomID:    randomID(),
			InvertMedia: true, // картинка над текстом, как у обложки поста
		}
		if topic > GeneralTopic {
			req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: topic, TopMsgID: topic})
		}
		_, err := api.MessagesSendMedia(ctx, req)
		if err != nil && strings.Contains(err.Error(), "WEBPAGE_NOT_FOUND") {
			// превью могло дозреть между проверкой и отправкой — ещё раз
			waitPreview(ctx, api, imageURL)
			req.RandomID = randomID()
			_, err = api.MessagesSendMedia(ctx, req)
		}
		return err
	})
}

// waitPreview — попросить у Telegram превью адреса и подождать, пока он
// его соберёт: до ~45 секунд, по запросу раз в три секунды.
func waitPreview(ctx context.Context, api *tg.Client, url string) {
	for i := 0; i < 15; i++ {
		res, err := api.MessagesGetWebPagePreview(ctx, &tg.MessagesGetWebPagePreviewRequest{Message: url})
		if err == nil && previewReady(res.Media) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// previewReady — Telegram отдал собранную страницу, а не «ещё собираю»
// и не пустоту.
func previewReady(media tg.MessageMediaClass) bool {
	m, ok := media.(*tg.MessageMediaWebPage)
	if !ok {
		return false
	}
	_, ready := m.Webpage.(*tg.WebPage)
	return ready
}

// randomID — номер сообщения для Telegram: свой у каждой отправки, иначе
// повтор считается тем же сообщением.
func randomID() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UnixNano()
	}
	return int64(binary.LittleEndian.Uint64(b[:]))
}

// SendFile — файл или картинка собеседнику от вашего имени. Картинка
// уходит фотографией (её видно прямо в переписке), остальное —
// документом с исходным именем.
func (c *Client) SendFile(ctx context.Context, chatID, access int64, kind string, topic int, name, mime string, raw []byte, caption string) error {
	if len(raw) == 0 {
		return fmt.Errorf("пустой файл")
	}
	return c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		api := tg.NewClient(cl)
		up, err := uploader.NewUploader(api).FromBytes(ctx, name, raw)
		if err != nil {
			return err
		}
		var opts []message.StyledTextOption
		if strings.TrimSpace(caption) != "" {
			opts = append(opts, styling.Plain(caption))
		}
		var media message.MediaOption
		if strings.HasPrefix(mime, "image/") && !strings.Contains(mime, "svg") {
			media = message.UploadedPhoto(up, opts...)
		} else {
			media = message.File(up, opts...).Filename(name).MIME(mime)
		}
		sender := message.NewSender(api).To(peerOf(chatID, access, kind))
		if topic > GeneralTopic {
			_, err = sender.Reply(topic).Media(ctx, media)
			return err
		}
		_, err = sender.Media(ctx, media)
		return err
	})
}

// Update — новое сообщение, пришедшее по живому соединению.
type Update struct {
	ChatID int64
	Kind   string // user | group | channel
	MsgID  int
	Text   string
	Out    bool   // это мы написали, а не нам
	File   *File  // вложение, если оно есть
	Topic  int    // тема в группе с топиками: 0 — не тема (или «Общий»)
	From   string // кто написал: в группе и топике пишут многие
	// когда сообщение написано: пост канала старше срока разговором не
	// заводится, а читается как история
	Date time.Time
	// корень ветки комментариев: комментарий под постом канала приходит
	// в привязанную группу обсуждения ответом на копию поста. 0 — не
	// ответ; у темы форума здесь тоже 0, её разбирает Topic
	Thread int
	// Deleted — сообщения удалили: номера удалённых. У канала и
	// супергруппы ChatID — их номер; в личном чате и обычной группе
	// Telegram чат не называет (номера там общие на всю учётку), и ChatID 0
	Deleted []int
}

// File — вложение сообщения: имя, тип и адрес, по которому его забрать.
type File struct {
	Name     string
	MIME     string
	Size     int64
	Location tg.InputFileLocationClass
	// Voice — голосовое сообщение (запись с микрофона), а не музыка и не
	// файл: у него атрибут audio с флагом voice. Duration — его секунды, как
	// их назвал отправитель.
	Voice    bool
	Duration int
}

// Listen держит соединение и отдаёт новые сообщения по мере их прихода.
// Telegram присылает обновления сам — опрос раз в минуту был обходом
// того, что клиент поднимался на каждый запрос и не жил между ними.
// Возвращается, когда обрывается связь или отменён контекст.
func (c *Client) Listen(ctx context.Context, onUpdate func(Update)) error {
	if c.creds.AppID == 0 || c.creds.AppHash == "" {
		return fmt.Errorf("не заданы api_id и api_hash")
	}
	d := tg.NewUpdateDispatcher()
	names := func(e tg.Entities) map[int64]string {
		out := map[int64]string{}
		for id, u := range e.Users {
			out[id] = userName(u)
		}
		return out
	}
	d.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		if m, ok := u.Message.(*tg.Message); ok {
			onUpdate(updateOf(m, names(e)))
		}
		return nil
	})
	d.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		if m, ok := u.Message.(*tg.Message); ok {
			onUpdate(updateOf(m, names(e)))
		}
		return nil
	})
	// удаления: пост удалили в канале — он должен пропасть и из ленты Сёмы
	d.OnDeleteChannelMessages(func(ctx context.Context, e tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		if len(u.Messages) > 0 {
			onUpdate(Update{ChatID: u.ChannelID, Kind: "channel", Deleted: u.Messages})
		}
		return nil
	})
	d.OnDeleteMessages(func(ctx context.Context, e tg.Entities, u *tg.UpdateDeleteMessages) error {
		if len(u.Messages) > 0 {
			onUpdate(Update{Deleted: u.Messages})
		}
		return nil
	})
	cl := telegram.NewClient(c.creds.AppID, c.creds.AppHash, telegram.Options{
		SessionStorage: c.storage,
		UpdateHandler:  d,
	})
	return cl.Run(ctx, func(ctx context.Context) error {
		if status, err := cl.Auth().Status(ctx); err != nil || !status.Authorized {
			return fmt.Errorf("вход в Telegram не выполнен")
		}
		c.setLive(cl) // отсюда исходящее идёт по этому же соединению
		defer c.dropLive(cl)
		<-ctx.Done() // держим соединение, пока не попросят выйти
		return ctx.Err()
	})
}

// userName — как назвать человека: имя с фамилией, иначе ник.
func userName(u *tg.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	return name
}

// namesOf — имена людей из ответа Telegram: сообщение несёт номер автора,
// а имя лежит рядом, в списке пользователей.
func namesOf(users []tg.UserClass) map[int64]string {
	out := map[int64]string{}
	for _, u := range users {
		if v, ok := u.(*tg.User); ok {
			out[v.ID] = userName(v)
		}
	}
	return out
}

// updateOf — из сообщения Telegram в то, что нужно Сёме. names — имена
// людей из того же ответа; без них автор остаётся неизвестным, и это
// честнее, чем подписать реплику именем чата.
func updateOf(m *tg.Message, names map[int64]string) Update {
	u := Update{MsgID: m.ID, Text: m.Message, Out: m.Out, File: fileOf(m),
		Topic: topicOf(m), Thread: threadOf(m), Date: time.Unix(int64(m.Date), 0)}
	if from, ok := m.GetFromID(); ok {
		if p, ok := from.(*tg.PeerUser); ok {
			u.From = names[p.UserID]
		}
	}
	switch p := m.PeerID.(type) {
	case *tg.PeerUser:
		u.ChatID, u.Kind = p.UserID, "user"
	case *tg.PeerChat:
		u.ChatID, u.Kind = p.ChatID, "group"
	case *tg.PeerChannel:
		u.ChatID, u.Kind = p.ChannelID, "channel"
	}
	return u
}

// topicOf — тема сообщения в группе с топиками. У сообщения в теме
// заголовок ответа помечен forum_topic: корень темы — reply_to_top_id,
// а если сообщение отвечает прямо корню — reply_to_msg_id. В «Общем»
// пометки нет, и тема здесь 0: кто группа — форум, знает Сёма, не письмо.
func topicOf(m *tg.Message) int {
	h, ok := m.GetReplyTo()
	if !ok {
		return 0
	}
	r, ok := h.(*tg.MessageReplyHeader)
	if !ok || !r.ForumTopic {
		return 0
	}
	if top, ok := r.GetReplyToTopID(); ok && top > 0 {
		return top
	}
	if id, ok := r.GetReplyToMsgID(); ok {
		return id
	}
	return 0
}

// threadOf — корень ветки комментариев под постом канала. Telegram сам
// пересылает пост в привязанную группу обсуждения, и комментарии идут
// ответами на эту копию: reply_to_top_id у ответа на ответ, иначе
// reply_to_msg_id. Тема форума помечена forum_topic и разбирается в
// topicOf — иначе комментарий и тема были бы неотличимы.
func threadOf(m *tg.Message) int {
	h, ok := m.GetReplyTo()
	if !ok {
		return 0
	}
	r, ok := h.(*tg.MessageReplyHeader)
	if !ok || r.ForumTopic {
		return 0
	}
	if top, ok := r.GetReplyToTopID(); ok && top > 0 {
		return top
	}
	if id, ok := r.GetReplyToMsgID(); ok {
		return id
	}
	return 0
}

// DiscussionRoot — где живут комментарии к посту канала. Комментариев в
// самом канале нет: Telegram пересылает пост в привязанную группу
// обсуждения, и отвечают уже там. Возвращает группу и номер копии поста
// в ней — по ним комментарий узнаётся, а ответ уходит под тот же пост.
// Спрашивается один раз на пост: ходить в сеть на каждый комментарий
// незачем.
func (c *Client) DiscussionRoot(ctx context.Context, chatID, access int64, post int) (groupID, groupAccess int64, root int, err error) {
	err = c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		res, err := cl.API().MessagesGetDiscussionMessage(ctx, &tg.MessagesGetDiscussionMessageRequest{
			Peer: peerOf(chatID, access, "channel"), MsgID: post,
		})
		if err != nil {
			return err
		}
		for _, mc := range res.Messages {
			m, ok := mc.(*tg.Message)
			if !ok {
				continue
			}
			p, ok := m.PeerID.(*tg.PeerChannel)
			if !ok {
				continue
			}
			groupID, root = p.ChannelID, m.ID
			break
		}
		if groupID == 0 || root == 0 {
			return fmt.Errorf("у поста нет группы обсуждения")
		}
		for _, ch := range res.Chats {
			if v, ok := ch.(*tg.Channel); ok && v.ID == groupID {
				groupAccess = v.AccessHash
			}
		}
		return nil
	})
	return groupID, groupAccess, root, err
}

// fileOf — вложение сообщения, если оно есть и его можно забрать.
// Фотографии берём в самом большом размере: Сёма читает с них текст.
func fileOf(m *tg.Message) *File {
	media, ok := m.GetMedia()
	if !ok {
		return nil
	}
	switch v := media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := v.Document.AsNotEmpty()
		if !ok {
			return nil
		}
		f := &File{MIME: doc.MimeType, Size: doc.Size, Location: &tg.InputDocumentFileLocation{
			ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference,
		}}
		for _, a := range doc.Attributes {
			switch attr := a.(type) {
			case *tg.DocumentAttributeFilename:
				f.Name = attr.FileName
			case *tg.DocumentAttributeAudio:
				f.Voice, f.Duration = attr.Voice, attr.Duration
			}
		}
		if f.Name == "" {
			f.Name = fmt.Sprintf("telegram-%d", doc.ID)
			if f.Voice {
				// у голосового имени нет вовсе; расширение нужно и файлам
				// Сёмы, и распознаванию — по нему провайдер узнаёт формат
				f.Name = fmt.Sprintf("voice-%d.ogg", doc.ID)
			}
		}
		return f
	case *tg.MessageMediaPhoto:
		photo, ok := v.Photo.AsNotEmpty()
		if !ok {
			return nil
		}
		size := ""
		for _, s := range photo.Sizes {
			if ps, ok := s.(*tg.PhotoSize); ok {
				size = ps.Type // последний — самый крупный
			}
		}
		if size == "" {
			return nil
		}
		return &File{
			Name: fmt.Sprintf("photo-%d.jpg", photo.ID), MIME: "image/jpeg",
			Location: &tg.InputPhotoFileLocation{
				ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: size,
			},
		}
	}
	return nil
}

// Download — забрать вложение. Байты возвращаются целиком: файлы Сёмы и
// так проходят через память при разборе.
func (c *Client) Download(ctx context.Context, loc tg.InputFileLocationClass, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	err := c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		w := &limitedBuf{buf: &buf, left: limit}
		_, err := downloader.NewDownloader().Download(tg.NewClient(cl), loc).Stream(ctx, w)
		if err != nil && !errors.Is(err, errTooBig) {
			return err
		}
		return nil
	})
	return buf.Bytes(), err
}

var errTooBig = errors.New("вложение больше предела")

// limitedBuf — не тянем в память бесконечность: предел задаёт настройка.
type limitedBuf struct {
	buf  *bytes.Buffer
	left int64
}

func (w *limitedBuf) Write(p []byte) (int, error) {
	if w.left <= 0 {
		return 0, errTooBig
	}
	if int64(len(p)) > w.left {
		p = p[:w.left]
	}
	n, err := w.buf.Write(p)
	w.left -= int64(n)
	return n, err
}

// peerOf — собеседник в том виде, в каком его понимает MTProto.
func peerOf(chatID, access int64, kind string) tg.InputPeerClass {
	switch kind {
	case "user":
		return &tg.InputPeerUser{UserID: chatID, AccessHash: access}
	case "channel", "group":
		return &tg.InputPeerChannel{ChannelID: chatID, AccessHash: access}
	default:
		return &tg.InputPeerChat{ChatID: chatID}
	}
}

// History — прошлая переписка с собеседником, от старых к новым.
// Нужна, когда человек только что открыл чат: разговор должен быть виден
// целиком, а не начинаться со следующего входящего сообщения.
// topic > 0 — история одной темы группы с топиками: у темы это
// «ответы» на её корень (messages.getReplies), у «Общего» — общая
// история без сообщений других тем.
func (c *Client) History(ctx context.Context, chatID, access int64, kind string, topic, limit int) ([]Update, error) {
	if limit <= 0 {
		limit = 30
	}
	var out []Update
	err := c.with(ctx, func(ctx context.Context, cl *telegram.Client) error {
		offset := 0
		for pass := 0; pass < historyPasses; pass++ {
			page, next, err := c.historyPage(ctx, cl, chatID, access, kind, topic, limit, offset)
			if err != nil {
				return err
			}
			out = append(page, out...) // страницы идут от новых к старым
			if len(out) >= limit || next == 0 {
				break
			}
			offset = next
		}
		if len(out) > limit {
			out = out[len(out)-limit:]
		}
		return nil
	})
	return out, err
}

// historyPasses — сколько страниц истории перебирать. В «Общем» топике
// живого форума последние тридцать сообщений — это чужие темы, и с одной
// страницы в разговор не попадает почти ничего.
const historyPasses = 5

// historyPage — страница истории; возвращает сообщения и номер, с
// которого продолжать (0 — дальше ничего).
func (c *Client) historyPage(ctx context.Context, cl *telegram.Client, chatID, access int64, kind string, topic, limit, offset int) ([]Update, int, error) {
	var out []Update
	next := 0
	err := func() error {
		var res tg.MessagesMessagesClass
		var err error
		if topic > GeneralTopic {
			res, err = cl.API().MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
				Peer: peerOf(chatID, access, kind), MsgID: topic, Limit: limit, OffsetID: offset,
			})
		} else {
			res, err = cl.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer: peerOf(chatID, access, kind), Limit: limit, OffsetID: offset,
			})
		}
		if err != nil {
			return err
		}
		var msgs []tg.MessageClass
		var names map[int64]string
		switch m := res.(type) {
		case *tg.MessagesMessages:
			msgs, names = m.Messages, namesOf(m.Users)
		case *tg.MessagesMessagesSlice:
			msgs, names = m.Messages, namesOf(m.Users)
		case *tg.MessagesChannelMessages:
			msgs, names = m.Messages, namesOf(m.Users)
		default:
			return fmt.Errorf("Telegram ответил не тем: %T", res)
		}
		// Telegram отдаёт от новых к старым — переписка читается наоборот
		for i := len(msgs) - 1; i >= 0; i-- {
			m, ok := msgs[i].(*tg.Message)
			if !ok {
				continue
			}
			if next == 0 || m.ID < next {
				next = m.ID // самое старое на странице: с него продолжаем
			}
			u := updateOf(m, names)
			if topic == GeneralTopic && u.Topic > GeneralTopic {
				continue // общая история несёт и другие темы — они не «Общий»
			}
			out = append(out, u)
		}
		if len(msgs) == 0 {
			next = 0
		}
		return nil
	}()
	return out, next, err
}
