package engine

// Удалённое в Telegram пропадает из ленты Сёмы. 17 сентября 2026 владелец
// удалил из канала неудачный пост, а в ленте канала у Сёмы он так и висел:
// лента не знала, какому сообщению Telegram соответствует её реплика.
// Теперь у реплики есть адрес (chat_msgs.ext_ref), слушатель ловит
// удаления, и реплика уходит. Архив сообщений (tg_messages) не трогается:
// память и поиск помнят, что было сказано.

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// tgMsgRef — адрес сообщения Telegram для ленты: учётка, физический чат
// (у комментария — группа обсуждения) и номер.
func tgMsgRef(account string, chat int64, msg int) string {
	return fmt.Sprintf("tg:%s:%d:%d", account, chat, msg)
}

// tgDeletedNote — чем становится реплика, от которой открыта ветка.
const tgDeletedNote = "🗑 Сообщение удалено в Telegram"

// onTGDeleted — в Telegram удалили сообщения. У канала и супергруппы
// номер чата известен; в личном чате и обычной группе Telegram его не
// называет, номера там общие на учётку — ищем по таким чатам учётки.
func (e *Engine) onTGDeleted(account string, u tg.Update) {
	var refs []string
	if u.ChatID != 0 {
		for _, n := range u.Deleted {
			refs = append(refs, tgMsgRef(account, u.ChatID, n))
		}
		e.tgUncountDeletedPosts(account, u.ChatID, u.Deleted)
	} else {
		chats, _ := e.store.TGChats(account, true)
		for _, c := range chats {
			if c.Kind != "user" && !(c.Kind == "group" && c.Access == 0) {
				continue // канал и супергруппа приходят своим событием с номером чата
			}
			for _, n := range u.Deleted {
				refs = append(refs, tgMsgRef(account, c.ID, n))
			}
		}
	}
	n, err := e.store.DeleteChatMsgsByRef(refs, tgDeletedNote)
	if err != nil {
		log.Printf("telegram: удалённые сообщения не убраны из ленты: %v", err)
		return
	}
	if n > 0 {
		log.Printf("telegram: удалено в Telegram — убрано из ленты Сёмы: %d", n)
	}
}

// tgUncountDeletedPosts — свой пост, опубликованный сегодня и удалённый,
// не занимает место в дневном пределе постов: его в канале больше нет.
func (e *Engine) tgUncountDeletedPosts(account string, chat int64, msgs []int) {
	c, ok := e.store.TGChat(account, chat)
	if !ok || c.Kind != "channel" {
		return
	}
	key := tgCountKey(c, time.Now())
	was, _ := strconv.Atoi(e.store.Setting(key, "0"))
	today := time.Now().Format("2006-01-02")
	for _, n := range msgs {
		if was <= 0 {
			break
		}
		list, _ := e.store.TGMessages(account, chat, n, 5)
		for _, m := range list {
			if m.MsgID == n && m.Out && strings.HasPrefix(parseTimeSafe(m.At).Local().Format(time.RFC3339), today) {
				was--
				break
			}
		}
	}
	_ = e.store.SetSetting(key, strconv.Itoa(was))
}

var (
	// «[photo-1.jpg]» и ««photo-1.jpg» (#437)» — одна и та же картинка в
	// архиве старого и нового вида
	reMarkFile = regexp.MustCompile(`\s*(\[[^\]\n]+\]|«[^»\n]+» \(#\d+\))\s*$`)
	// подпись комментария в ленте канала
	reToPost = regexp.MustCompile(`^↩ к посту «[^\n]*»\n`)
)

// textForVerify — текст без пометок файла и подписи комментария: так
// реплика ленты и запись архива совпадают, даже если писались по-разному.
func textForVerify(s string) string {
	s = reToPost.ReplaceAllString(strings.TrimSpace(s), "")
	return strings.TrimSpace(reMarkFile.ReplaceAllString(s, ""))
}

// tgBindFeedRefs — разово: реплики лент, собранных до адресов, узнают свои
// сообщения по совпадению текста с архивом. Не нашлось — реплика остаётся
// как есть, только удаление в Telegram её не уберёт.
func (e *Engine) tgBindFeedRefs(account string) {
	label := "tg.refs.v2." + account
	if e.store.Setting(label, "") != "" {
		return
	}
	chats, err := e.store.TGChats(account, true)
	if err != nil {
		return
	}
	bound := 0
	for _, c := range chats {
		root := e.tgRootOf(c)
		if root == 0 {
			continue
		}
		feed := []int64{root}
		if kids, err := e.store.ChatChildren(root); err == nil {
			for _, k := range kids {
				if !k.IsThread() {
					feed = append(feed, k.ID)
				}
			}
		}
		archive, _ := e.store.TGMessages(account, c.ID, 0, 1000)
		byText := map[string][]*store.TGMessage{}
		for i := len(archive) - 1; i >= 0; i-- { // от старых к новым
			m := archive[i]
			key := textForVerify(m.Text)
			byText[key] = append(byText[key], m)
		}
		for _, feed := range feed {
			msgs, _ := e.store.ChatMsgsWithoutRef(feed)
			for _, m := range msgs {
				key := textForVerify(m.Content)
				candidates := byText[key]
				if len(candidates) == 0 {
					continue
				}
				a := candidates[0]
				byText[key] = candidates[1:]
				if e.store.SetChatMsgRef(m.ID, tgMsgRef(account, a.ChatID, a.MsgID)) == nil {
					bound++
				}
			}
		}
	}
	log.Printf("telegram: ленты учётки %s привязаны к сообщениям: %d", account, bound)
	_ = e.store.SetSetting(label, strconv.Itoa(bound))
}
