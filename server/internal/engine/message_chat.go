package engine

import (
	"fmt"
	"strconv"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Разобраться» с письма. У уведомления такая кнопка есть с самого
// начала, а у письма её не было: человек читал письмо, у него возникал
// вопрос — и он шёл в общий чат Сёмы пересказывать своими словами то,
// что Сёма и так видит. Теперь разговор заводится от самого письма и
// один на письмо: второе нажатие открывает тот же чат.

const (
	keyMsgChat        = "msg.chat.%d"  // письмо → чат
	keyChatMsg        = "chat.msg.%d"  // чат → письмо
	keyTopicInRequest = "chat.msgref." // подсказка поиску: тема письма
)

// MessageChatID — заведён ли уже разговор об этом письме.
func (e *Engine) MessageChatID(msgID int64) int64 {
	id, _ := strconv.ParseInt(e.store.Setting(fmt.Sprintf(keyMsgChat, msgID), ""), 10, 64)
	if id > 0 {
		if _, err := e.store.Chat(id); err == nil {
			return id
		}
	}
	return 0
}

// MessageChat — завести (или открыть) разговор о письме.
func (e *Engine) MessageChat(msgID int64) (int64, error) {
	m, err := e.store.Message(msgID)
	if err != nil {
		return 0, fmt.Errorf("письма %d нет", msgID)
	}
	if id := e.MessageChatID(msgID); id > 0 {
		return id, nil
	}
	subject := subjectOrEmpty(m.Subject)
	id, err := e.store.CreateChat("Письмо · " + clip(subject, 40))
	if err != nil {
		return 0, err
	}
	_ = e.store.SetSetting(fmt.Sprintf(keyMsgChat, msgID), strconv.FormatInt(id, 10))
	_ = e.store.SetSetting(fmt.Sprintf(keyChatMsg, id), strconv.FormatInt(msgID, 10))
	// тема письма уходит подсказкой в поиск контекста: письмо — самый
	// близкий сосед собственной темы, и оно окажется в контексте само,
	// пройдя обычную проверку класса. Класть текст письма в реплику
	// нельзя: реплики идут модели как есть, мимо двери
	_ = e.store.SetSetting(keyTopicInRequest+strconv.FormatInt(id, 10), subject+" "+m.FromAddr)

	intro := "Разбираемся с письмом #" + strconv.FormatInt(msgID, 10) + "."
	if e.ClassOfMessage(m) == store.ClassSecret {
		// секретное не называем даже темой: реплика уйдёт той модели,
		// какая назначена роли, а решение про класс принимается дверью
		// позже — здесь его ещё никто не проверял
		intro += " Письмо помечено секретным: его текст ты увидишь, только если отвечаешь доверенной моделью."
	} else {
		intro += " Тема: «" + subject + "», от " + m.FromAddr + "."
	}
	intro += "\n\nСамо письмо ты видишь в контексте. Продолжай сам: ответь по существу, " +
		"а закончишь — блоком sema-done:<итог>."
	if err := e.store.AddChatMsg(e.chatForOutput(id), "note", intro); err != nil {
		return id, err
	}
	e.continueChat(id)
	return id, nil
}

// chatMsgRefs — подсказка поиску контекста у разговора о письме.
func (e *Engine) chatMsgRefs(chatID int64) string {
	return e.store.Setting(keyTopicInRequest+strconv.FormatInt(chatID, 10), "")
}

// subjectOrEmpty — тема письма или честная замена: «(без темы)» лучше
// пустой строки в названии чата.
func subjectOrEmpty(s string) string {
	if s == "" {
		return "без темы"
	}
	return s
}
