package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func wechatWaitKey(account, peer string) string {
	return fmt.Sprintf("wechat-wait:%x", sha256.Sum256([]byte(account+"\x00"+peer)))
}

func (e *Engine) wechatWaitPass(ctx context.Context, account string) error {
	chats, err := e.store.WeChatWaitingChats(ctx, account)
	if err != nil {
		return err
	}
	const warn, alert = 30 * time.Minute, 2 * time.Hour
	for _, chat := range chats {
		key := wechatWaitKey(account, chat.Peer)
		if chat.Latest == 0 {
			e.CloseNotification(key, store.NotifyDone, "ответили")
			continue
		}
		since, err := time.Parse(time.RFC3339, chat.Since)
		if err != nil || time.Since(since) < warn {
			continue
		}
		level := "info"
		if time.Since(since) >= alert {
			level = "alert"
		}
		if chat.Latest == chat.Notified {
			if n, ok := e.store.OpenNotificationByKey(key); ok && n.Level != level {
				if err := e.store.UpdateNotificationLevel(n.ID, level, ""); err != nil {
					return err
				}
			}
			continue
		}
		// Напоминание видно в Сёме. Имя, идентификатор и текст чата
		// остаются на закрытом экране WeChat.
		_, err = e.store.PutNotification(&store.Notification{
			Key: key, Source: "core", Level: level, Title: "Ждёт ответа · WeChat",
			Summary: "В одной из подписок есть сообщения без ответа",
			Actions: []store.NotifyAction{{Label: "Открыть WeChat", Kind: "goto", Ref: "settings:wechat"}},
		})
		if err != nil {
			return err
		}
		if err := e.store.MarkWeChatWaitNotified(ctx, account, chat.Peer, chat.Latest); err != nil {
			return err
		}
	}
	return nil
}
