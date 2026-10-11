package engine

// Разовая починка старых реплик собеседника. До появления роли «peer»
// сообщения из Telegram ложились ролью «user» с именем внутри текста
// («Вадим: Привет») — и рисовались справа синим, будто это писали вы.
// Чинится один раз по метке, как снимки писем.

import (
	"log"
	"regexp"
	"strconv"
	"strings"
)

const chatPeerMark = "chats.peer_fixed"

// chatNoteMark — разовый перевод старых служебных вводных в роль «note».
const chatNoteMark = "chats.notes_fixed"

// noteStarts — по чему узнаётся служебная вводная, записанная до
// появления роли: это внутренний ход Сёмы, а не реплика человека.
var noteStarts = []string{"Никто не писал: это внутренний ход",
	"Продолжай сам:", "Уведомление ·", "Разобраться с уведомлением"}

// fixNoteMessages — старые вводные показывались вашей репликой, синим
// пузырём справа. Роль правится один раз, текст не трогаем.
func (e *Engine) fixNoteMessages() {
	if e.store.Setting(chatNoteMark, "") == "1" {
		return
	}
	chats, err := e.store.Chats()
	if err != nil {
		return
	}
	fixed := 0
	for _, c := range chats {
		msgs, err := e.store.ChatMsgs(c.ID, 300)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if m.Role != "user" {
				continue
			}
			for _, start := range noteStarts {
				if !strings.Contains(m.Content, start) {
					continue
				}
				if e.store.SetChatMsgRole(m.ID, "note", m.Content) == nil {
					fixed++
				}
				break
			}
		}
	}
	_ = e.store.SetSetting(chatNoteMark, "1")
	if fixed > 0 {
		log.Printf("чаты: служебных вводных помечено: %d", fixed)
	}
}

// rePeerLine — «Имя: текст» в начале сообщения.
var rePeerLine = regexp.MustCompile(`^([^:\n]{2,40}):\s+`)

// fixPeerMessages — привести старые сообщения каналов к роли «peer».
func (e *Engine) fixPeerMessages() {
	if e.store.Setting(chatPeerMark, "") == "1" {
		return
	}
	fixed := 0
	for key, v := range e.store.SettingsByPrefix("tg.chat.") {
		_ = key
		chatID, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		chat, err := e.store.Chat(chatID)
		if err != nil {
			continue
		}
		who := strings.TrimPrefix(chat.Title, "Telegram · ")
		msgs, err := e.store.ChatMsgs(chatID, 500)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if m.Role != "user" {
				continue
			}
			match := rePeerLine.FindStringSubmatch(m.Content)
			if match == nil || !strings.EqualFold(strings.TrimSpace(match[1]), who) {
				continue
			}
			text := strings.TrimSpace(m.Content[len(match[0]):])
			if text == "" {
				continue
			}
			if err := e.store.SetChatMsgRole(m.ID, "peer", text); err == nil {
				fixed++
			}
		}
	}
	_ = e.store.SetSetting(chatPeerMark, "1")
	if fixed > 0 {
		log.Printf("чаты: старых реплик собеседника поправлено: %d", fixed)
	}
}
