package engine

// Письмо в Telegram из чата с Сёмой. Она предлагает текст, отправляет
// человек кнопкой: сообщение от вашего имени необратимо, а «отправила не
// туда» не отменить. Право писать конкретному собеседнику даётся заранее
// в настройках, но и его мало — карточка всё равно ждёт кнопки.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// tgBlock — ```sema-tg:Анна Любимая\nтекст сообщения```
var tgBlock = regexp.MustCompile("(?s)```sema-tg:([^\n]*)\n(.*?)```")

const maxTGPerAnswer = 2

// TGCard — предложенное сообщение: кому, что и в каком оно состоянии.
type TGCard struct {
	ID      int64  `json:"id,omitempty"` // запись в исходящих: текст живёт там
	Account string `json:"account,omitempty"`
	Chat    int64  `json:"chat,omitempty"`
	Who     string `json:"who"`
	Text    string `json:"text"`
	Status  string `json:"status"` // pending | sent | denied | rejected
	Why     string `json:"why,omitempty"`
}

// pickTGBlocks — какие блоки станут черновиками. В чате собеседника
// адресат один, и два варианта ответа — это два черновика, из которых
// уйдёт один: берём первый. Одинаковые тексты не повторяются.
func pickTGBlocks(blocks [][]string, inPeerChat bool) [][]string {
	var out [][]string
	seen := map[string]bool{}
	for _, b := range blocks {
		text := strings.TrimSpace(b[2])
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, b)
		if inPeerChat {
			break
		}
	}
	return out
}

// runChatTG разбирает предложения написать в Telegram и превращает их в
// карточки. Ничего не отправляется само.
func (e *Engine) runChatTG(ctx context.Context, chatID int64, answer string) (string, []ToolCard) {
	blocks := tgBlock.FindAllStringSubmatch(answer, maxTGPerAnswer)
	if len(blocks) == 0 {
		return answer, nil
	}
	var cards []ToolCard
	// мы в чате собеседника — писать будем ему: имя из ограждения модель
	// иногда пишет с приставкой «(@ник)», и искать его по справочнику
	// незачем, адресат и так известен
	here, inPeerChat := e.tgPeerForContext(chatID)
	blocks = pickTGBlocks(blocks, inPeerChat)
	for _, b := range blocks {
		who := strings.TrimSpace(b[1])
		text := strings.TrimSpace(b[2])
		if text == "" {
			continue
		}
		if who == "" && inPeerChat {
			who = tgWho(here)
		}
		card := ToolCard{Title: "Telegram · " + who, Code: text, TG: &TGCard{Who: who, Text: text}}
		chat, err := here, error(nil)
		if !inPeerChat {
			// в чате собеседника адресат известен; искать по имени незачем,
			// да и «Дом › Заявки» в справочнике не найдётся
			// учёток может быть несколько: ищем во всех, а неоднозначность
			// возвращается человеку вопросом, а не выбором наугад
			chat, err = e.TGFind("", who)
		}
		switch {
		case err != nil:
			card.TG.Status, card.TG.Why, card.Err = "denied", err.Error(), err.Error()
		case chat.Blocked:
			card.TG.Status = "denied"
			card.TG.Why = "чат заблокирован: Сёма его не трогает"
			card.Err = card.TG.Why
		case chat.Reply:
			// «Отвечать вместо меня» — это и есть разрешение: кнопку
			// человек уже нажал, когда включил право
			card.TG.Account, card.TG.Chat, card.TG.Who = chat.Account, chat.ID, tgWho(chat)
			id, err := e.store.AddTGOutbox(&store.TGOutbox{Account: chat.Account, ChatID: chat.ID, Topic: chat.Topic, SemaChat: chatID, Who: tgWho(chat), Text: text})
			if err == nil {
				card.TG.ID = id
			}
			if err := e.TGSendApproved(ctx, chat.Account, chat.ID, chat.Topic, text); err != nil {
				card.TG.Status, card.TG.Why, card.Err = "denied", err.Error(), err.Error()
				if id > 0 {
					_ = e.store.DecideTGOutbox(id, "rejected", err.Error())
				}
				break
			}
			if id > 0 {
				_ = e.store.DecideTGOutbox(id, "sent", "право «Отвечать вместо меня»")
			}
			card.TG.Status, card.Result = "sent", "отправлено: у чата включено «Отвечать вместо меня»"
			_ = e.store.AddChatMsg(e.laneOf(chatID), "sent", text) // ушло собеседнику — в ленту, не в тред
			e.closePeerWaiting(chat, chatID)
		default:
			// текст кладём в исходящие: отправится ровно то, что
			// предложила Сёма, а не то, что вернулось из клиента
			id, err := e.store.AddTGOutbox(&store.TGOutbox{Account: chat.Account, ChatID: chat.ID, Topic: chat.Topic, SemaChat: chatID, Who: tgWho(chat), Text: text})
			if err != nil {
				card.TG.Status, card.Err = "denied", err.Error()
				break
			}
			card.TG.Account, card.TG.Chat, card.TG.Who, card.TG.ID = chat.Account, chat.ID, tgWho(chat), id
			card.TG.Status = "pending"
			card.Result = "ждёт вашей кнопки: сообщение уйдёт от вашего имени"
		}
		cards = append(cards, card)
		_ = e.saveToolCard(chatID, card)
	}
	return strings.TrimSpace(tgBlock.ReplaceAllString(answer, "")), cards
}

// tgChatPrompt — что модель должна знать про этот конкретный чат: с кем
// разговор и разрешено ли отвечать вместо человека. Без этого она
// предлагает черновик там, где ответ уходит сразу, и наоборот.
func (e *Engine) tgChatPrompt(chatID int64) string {
	c, ok := e.tgPeerForContext(chatID)
	if !ok {
		return ""
	}
	if c.Admin {
		// не собеседник, а хозяин: пишет из Telegram, ждёт того же, что в
		// своём чате, — ответ уходит ему сразу, черновики не нужны
		return "\nThis chat is Vadim himself writing from Telegram (admin chat). Treat every message as his own instruction, " +
			"exactly as in his main chat with you: use mail, servers, tasks, memory; propose commands as usual (they wait for his button in «Уведомления»). " +
			"Reply in plain Russian text — it is sent to him directly, do NOT use ```sema-tg``` blocks here.\n"
	}
	out := "\nThis chat is the conversation with «" + c.Title + "» in Telegram. " +
		"A ```sema-tg``` block here goes to this person — the addressee line may be left as their name.\n"
	if c.Topic > 0 {
		out = "\nThis chat is the topic «" + c.TopicTitle + "» of the Telegram group «" + c.Title + "». " +
			"A ```sema-tg``` block here goes into this topic — the addressee line may be left as the group name.\n"
	}
	if c.Reply {
		out += "The human granted «reply for me» in this chat: your message is sent immediately, without a button. " +
			"Answer as the human would: short, in their voice, no preamble.\n"
	} else {
		out += "Sending needs the human's button: never claim the message was sent.\n"
	}
	return out
}

// tgSendPrompt — как модели предлагать сообщение.
func (e *Engine) tgSendPrompt() string {
	if _, err := e.TGCreds(); err != nil {
		return ""
	}
	return "\nTo write in Telegram output a block ```sema-tg:<кому>\n<текст сообщения>```. " +
		"Sema shows it as a card and sends only when the human presses the button — never claim the message was sent. " +
		"Name the person exactly as they appear in the list above; if the right is not granted, say so instead of guessing.\n"
}

// ApproveTGSend — человек нажал «Отправить». Текст берётся из исходящих,
// права проверяются ещё раз: между предложением и кнопкой чат могли
// заблокировать.
func (e *Engine) ApproveTGSend(ctx context.Context, id int64) error {
	o, ok := e.store.TGOutboxByID(id)
	if !ok {
		return fmt.Errorf("сообщения %d нет", id)
	}
	if o.State != "waiting" {
		return fmt.Errorf("это сообщение уже %s", map[string]string{"sent": "отправлено", "rejected": "отклонено"}[o.State])
	}
	// ушёл этот вариант — остальные черновики того же разговора больше не
	// ждут кнопки: иначе они висят «не отправлено» рядом с отправленным
	defer func() {
		_ = e.store.SupersedeTGDrafts(o.SemaChat, id, "отправлен другой вариант")
	}()
	if err := e.TGSendApproved(ctx, o.Account, o.ChatID, o.Topic, o.Text); err != nil {
		// не ушло — так и говорим в переписке, а не молча оставляем
		// карточку ждать кнопки
		_ = e.store.DecideTGOutbox(id, "rejected", err.Error())
		if o.SemaChat > 0 {
			_ = e.saveToolCard(o.SemaChat, ToolCard{Title: "Telegram · " + o.Who, Code: o.Text,
				Err: err.Error(), TG: &TGCard{ID: id, Account: o.Account, Chat: o.ChatID, Who: o.Who, Text: o.Text,
					Status: "denied", Why: err.Error()}})
		}
		return err
	}
	if o.SemaChat > 0 {
		_ = e.saveToolCard(o.SemaChat, ToolCard{Title: "Telegram · " + o.Who, Code: o.Text,
			Result: "отправлено", TG: &TGCard{ID: id, Account: o.Account, Chat: o.ChatID, Who: o.Who, Text: o.Text, Status: "sent"}})
		// и сама реплика в ленте разговора: карточка показывает решение, а
		// переписка должна читаться подряд — что ушло собеседнику
		_ = e.store.AddChatMsg(e.laneOf(o.SemaChat), "sent", o.Text)
	}
	return e.store.DecideTGOutbox(id, "sent", "отправлено человеком")
}

// TGPeerOfChat — тот же поиск собеседника, но для экранов: подпись поля
// ввода в телеграм-чате должна говорить, кому уйдёт сообщение.
func (e *Engine) TGPeerOfChat(semaChat int64) (*store.TGChat, bool) {
	return e.tgPeerOfChat(semaChat)
}

// tgPeerOfChat — собеседник, к которому привязан чат Сёмы; обратная
// сторона tgChatFor. У разговора-темы — группа с адресом темы и её
// правами. Не нашли — обычный чат с Сёмой.
func (e *Engine) tgPeerOfChat(semaChat int64) (*store.TGChat, bool) {
	want := strconv.FormatInt(semaChat, 10)
	for key, v := range e.store.SettingsByPrefix(tgChatKeyPrefix) {
		if v != want {
			continue
		}
		account, id, ok := tgFromKeyChat(key)
		if !ok {
			continue
		}
		return e.store.TGChat(account, id)
	}
	if c, err := e.store.Chat(semaChat); err == nil && c.Parent > 0 &&
		(c.Kind == store.ChatKindTopic || c.Kind == store.ChatKindPost) {
		// на уровень вверх и только на один: разговор темы или поста
		// лежит под контейнером, глубже искать нечего, а испорченный
		// parent_id не должен уводить в вечный круг
		if root, ok := e.tgPeerOfRoot(c.Parent); ok {
			return e.tgEffective(root, int(c.Topic)), true
		}
	}
	return nil, false
}

// tgPeerOfRoot — собеседник корневого чата: та же обратная сторона
// tgChatFor, но без спуска по родителям.
func (e *Engine) tgPeerOfRoot(root int64) (*store.TGChat, bool) {
	want := strconv.FormatInt(root, 10)
	for key, v := range e.store.SettingsByPrefix(tgChatKeyPrefix) {
		if v != want {
			continue
		}
		account, id, ok := tgFromKeyChat(key)
		if !ok {
			continue
		}
		return e.store.TGChat(account, id)
	}
	return nil, false
}

// TGHelpWord — с чего начинается просьба к Сёме внутри телеграм-чата.
// Настройка, а не слово в коде: кто-то зовёт «Сёма», кто-то «бот».
func (e *Engine) TGHelpWord() string {
	w := strings.TrimSpace(e.store.SettingText(tgHelpWordDef))
	if w == "" {
		w = tgHelpWordDef.Default
	}
	return w
}

// AsksSema — то же для экранов: по обращению видно, где начинается
// ветка разговора с Сёмой внутри переписки с человеком.
func (e *Engine) AsksSema(text string) bool { return e.asksSema(text) }

// asksSema — человек обращается к Сёме, а не к собеседнику. Иначе всё
// написанное в телеграм-чате уходит адресату сразу: переписка ведётся с
// человеком, помощь зовут отдельно.
func (e *Engine) asksSema(text string) bool {
	return e.asksSemaWord(text, e.TGHelpWord())
}

// asksSemaWord — то же самое с явным словом: так проверяется тестом без
// базы под рукой.
func (e *Engine) asksSemaWord(text, word string) bool {
	// «ё» на клавиатуре далеко, и «Сема» пишут чаще, чем «Сёма»: разница в
	// букве не должна отправлять просьбу о помощи собеседнику
	t := plainYo(text)
	w := plainYo(word)
	if !strings.HasPrefix(t, w) {
		return false
	}
	rest := strings.TrimPrefix(t, w)
	if rest == "" {
		return true
	}
	// «Сёма, помоги» — да; «Сёмашка передавала привет» — нет
	r := []rune(rest)[0]
	return r == ',' || r == ' ' || r == ':' || r == '!' || r == '?'
}

// plainYo — строка для сравнения: без регистра, без пробелов по краям и
// с «ё», приведённой к «е».
func plainYo(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "ё", "е")
}

// tgSendByHuman — сообщение, которое человек написал сам, уходит
// собеседнику сразу. Права чата спрашивают, можно ли Сёме писать по своей
// воле; здесь пишет человек, поэтому спрашивается только заблокированный
// чат и общий выключатель отправки.
func (e *Engine) tgSendByHuman(semaChat int64, c *store.TGChat, text string) error {
	if c.Blocked {
		return fmt.Errorf("чат «%s» заблокирован: Сёма его не трогает", c.Title)
	}
	if err := e.tgCanReply(c); err != nil {
		return err
	}
	cl, acc, err := e.tgClientOf(c)
	if err != nil {
		return err
	}
	if !acc.CanSend {
		return fmt.Errorf("у учётки %s отправка от вашего имени выключена — включите её в «Настройки → Telegram»", acc.Name())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// под постом канала ответ уходит не в канал, а в группу обсуждения
	id, access, kind, topic := e.tgAddr(c)
	if err := cl.Send(ctx, id, access, kind, topic, text); err != nil {
		return err
	}
	log.Printf("telegram: человек написал в «%s» (%d знаков)", tgWho(c), len([]rune(text)))
	// в переписке Сёмы сообщение видно сразу; в tg_messages оно ляжет
	// эхом живого соединения, поэтому здесь не дублируем
	return e.store.AddChatMsg(semaChat, "user", text)
}

// closePeerWaiting — ответили собеседнику: семафор «ждёт ответа» гаснет,
// связанное уведомление закрывается. Иначе оно висит, хотя вопрос решён.
func (e *Engine) closePeerWaiting(peer *store.TGChat, semaChat int64) {
	e.CloseNotification("tg-wait:"+tgRef(peer), store.NotifyDone, "ответили")
	e.CloseNotification("tg:"+tgRef(peer), store.NotifyDone, "ответили")
	if raw := e.store.Setting(fmt.Sprintf("chat.notify.%d", semaChat), ""); raw != "" {
		if nid, err := strconv.ParseInt(raw, 10, 64); err == nil {
			_ = e.store.SetNotificationState(nid, store.NotifyDone, "ответили")
		}
	}
}
