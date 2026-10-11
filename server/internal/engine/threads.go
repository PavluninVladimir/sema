package engine

// Три уровня чата: контейнер → разговор → тред. Разговор — там, где идёт
// переписка с людьми (личный чат мессенджера, топик, пост); в его ленте
// только то, что собеседники видели или увидят. Всё, чего они не видят —
// ответы Сёмы, карточки, черновики, заметки — ложится в тред «Сёма» этого
// разговора. Тред — тот же чат с родителем, поэтому умеет всё, что чат.
//
// Правило одно, и точка входа одна: chatForOutput. Мест записи в чат
// полсотни, и править их по одному значило бы пропустить три.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// conversationOf — разговор, к которому относится чат: сам чат, а для
// треда — его родитель.
func (e *Engine) conversationOf(chatID int64) (*store.Chat, bool) {
	c, err := e.store.Chat(chatID)
	if err != nil {
		return nil, false
	}
	if c.IsThread() && c.Parent > 0 {
		if p, err := e.store.Chat(c.Parent); err == nil {
			return p, true
		}
	}
	return c, true
}

// isMessengerChat — разговор привязан к собеседнику в мессенджере.
func (e *Engine) isMessengerChat(chatID int64) bool {
	if _, ok := e.tgPeerOfChat(chatID); ok {
		return true
	}
	_, ok := e.MAXPeerOfChat(chatID)
	return ok
}

// laneOf — лента разговора: то, что видит собеседник. Для треда — лента
// родителя, для остальных — сам чат.
func (e *Engine) laneOf(chatID int64) int64 {
	if c, ok := e.conversationOf(chatID); ok {
		return c.ID
	}
	return chatID
}

// chatForOutput — куда класть слова и дела Сёмы. Для разговора из
// мессенджера — его тред «Сёма» (заводится при первом обращении), для
// треда и обычного чата — сам чат.
func (e *Engine) chatForOutput(chatID int64) int64 {
	if !e.isMessengerChat(chatID) {
		return chatID
	}
	if c, err := e.store.Chat(chatID); err == nil && c.IsContainer() {
		return chatID // у контейнера ленты нет — и треда под ним тоже
	}
	if id, err := e.semaThreadFor(chatID); err == nil {
		return id
	}
	return chatID
}

// semaThreadFor — тред «Сёма» разговора, один на разговор.
func (e *Engine) semaThreadFor(conv int64) (int64, error) {
	if id, ok := e.store.ThreadOf(conv, store.ChatKindSema, 0); ok {
		return id, nil
	}
	c, err := e.store.Chat(conv)
	if err != nil {
		return 0, err
	}
	title := "Сёма · " + strings.TrimPrefix(strings.TrimPrefix(c.Title, "Telegram · "), "MAX · ")
	return e.store.CreateThread(conv, store.ChatKindSema, 0, title)
}

// ThreadFromMsg — тред по сообщению: один якорь — один тред. Вводная с
// текстом якоря ложится заметкой: Сёма видит, о чём речь, даже если
// родительский чат ушёл далеко вперёд.
func (e *Engine) ThreadFromMsg(msgID int64) (int64, error) {
	m, err := e.store.ChatMsg(msgID)
	if err != nil {
		return 0, fmt.Errorf("сообщения нет")
	}
	parent, err := e.store.Chat(m.ChatID)
	if err != nil {
		return 0, err
	}
	if parent.IsThread() {
		// глубже треда уровней нет: ветка от ветки уходит к тому же разговору
		if id, ok := e.store.ThreadOf(parent.Parent, store.ChatKindMsg, msgID); ok {
			return id, nil
		}
		parent, err = e.store.Chat(parent.Parent)
		if err != nil {
			return 0, err
		}
	}
	if id, ok := e.store.ThreadOf(parent.ID, store.ChatKindMsg, msgID); ok {
		return id, nil
	}
	title := "По сообщению · " + clip(strings.TrimSpace(m.Content), 40)
	id, err := e.store.CreateThread(parent.ID, store.ChatKindMsg, msgID, title)
	if err != nil {
		return 0, err
	}
	who := "Собеседник"
	switch m.Role {
	case "user", "sent":
		who = "Вы"
	case "assistant":
		who = "Сёма"
	}
	_ = e.store.AddChatMsg(id, "note", "Ветка по сообщению из чата «"+parent.Title+"» ("+m.At.Format("02.01 15:04")+"), "+who+":\n"+m.Content) // в тред уже выбран
	return id, nil
}

// endChatTree — сводка в память по всему дереву: сам чат, его разговоры
// и ветки. В ленте разговора одни реплики собеседника, а разговор с
// моделью лежит в ветке — её сводка памяти и нужна.
func (e *Engine) endChatTree(chatID int64) {
	kids, _ := e.store.ChatChildren(chatID)
	for _, k := range kids {
		e.endChatTree(k.ID)
	}
	e.endChatSession(chatID)
}

// DeleteChatTree — чат с ветками: сначала сводка каждого в память, потом
// удаление. Терять разговор нельзя, ветку — тоже.
func (e *Engine) DeleteChatTree(chatID int64) error {
	kids, _ := e.store.ChatChildren(chatID)
	for _, k := range kids {
		if err := e.DeleteChatTree(k.ID); err != nil { // у контейнера — разговоры с их ветками
			return err
		}
	}
	e.endChatSession(chatID)
	return e.store.DeleteChat(chatID)
}

// endChatSession по дереву нужен и при закрытии переписки: см. endChatTree.

// tgPeerForContext — собеседник в Telegram для чата или его треда:
// модели в треде нужно знать, с кем разговор, а отправка из треда
// собеседнику идёт только черновиком.
func (e *Engine) tgPeerForContext(chatID int64) (*store.TGChat, bool) {
	if c, ok := e.tgPeerOfChat(chatID); ok {
		return c, true
	}
	if conv, ok := e.conversationOf(chatID); ok && conv.ID != chatID {
		return e.tgPeerOfChat(conv.ID)
	}
	return nil, false
}

// maxPeerForContext — то же для MAX.
func (e *Engine) maxPeerForContext(chatID int64) (*store.MAXChat, bool) {
	if c, ok := e.MAXPeerOfChat(chatID); ok {
		return c, true
	}
	if conv, ok := e.conversationOf(chatID); ok && conv.ID != chatID {
		return e.MAXPeerOfChat(conv.ID)
	}
	return nil, false
}

// threadTailKey — сколько реплик родителя видит Сёма в треде; правится на
// «Работе Сёмы».
const threadTailKey = "chats.thread_tail"

// ThreadTail — сколько реплик родителя видит Сёма в треде. Ноль и мусор —
// «не задан», то есть умолчание реестра.
func (e *Engine) ThreadTail() int { return e.store.SettingInt(threadTailDef) }

// threadContext — что Сёма видит в треде сверх самой ветки: якорь и хвост
// родительского разговора, подписанные, чтобы модель не путала ветку с
// родителем.
func (e *Engine) threadContext(chatID int64) string {
	c, err := e.store.Chat(chatID)
	if err != nil || !c.IsThread() || c.Parent == 0 {
		return ""
	}
	parent, err := e.store.Chat(c.Parent)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nThis is a thread (branch) under the chat «" + parent.Title + "». ")
	if c.Kind == store.ChatKindSema {
		b.WriteString("The parent lane is the conversation with a person; everything you say here stays in the thread and is NOT sent to them unless the human sends it.\n")
	} else {
		b.WriteString("The thread was opened about one message (the anchor). Answer about the anchor unless asked otherwise.\n")
	}
	if c.Anchor > 0 {
		if m, err := e.store.ChatMsg(c.Anchor); err == nil {
			b.WriteString("Anchor message (" + m.Role + ", " + m.At.Format("2006-01-02 15:04") + "): " + clipHistory(m.Content, lastMsgLimit) + "\n")
		}
	}
	tail, err := e.store.ChatMsgs(parent.ID, e.ThreadTail())
	if err == nil && len(tail) > 0 {
		b.WriteString("Recent messages in the parent chat (for context only):\n")
		for _, m := range tail {
			if m.Role == "tool" {
				continue
			}
			b.WriteString("  [" + m.Role + " " + m.At.Format("01-02 15:04") + "] " + clipHistory(m.Content, historyLimit) + "\n")
		}
	}
	return b.String()
}

// ThreadTitle — подпись треда для экрана: чей он и о чём.
func (e *Engine) ThreadTitle(c *store.Chat) string {
	if c == nil || !c.IsThread() {
		return ""
	}
	parent, err := e.store.Chat(c.Parent)
	if err != nil {
		return "ветка"
	}
	if c.Kind == store.ChatKindMsg {
		return "ветка по сообщению из чата «" + parent.Title + "»"
	}
	return "ветка разговора «" + parent.Title + "»"
}

// ParentChat — родитель треда для перехода наверх.
func (e *Engine) ParentChat(c *store.Chat) string {
	if c == nil || c.Parent == 0 {
		return ""
	}
	return "chat:" + strconv.FormatInt(c.Parent, 10)
}
