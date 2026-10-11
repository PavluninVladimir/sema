package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Публикация в Telegram-канал из модуля. Ядро умело отправить пост в канал
// (TGSend с правом «Публиковать в канал»), а инструмента у модулей не
// было: модуль мог найти и написать новость, но опубликовать её — нет.
//
// Границы, которые держит сам инструмент, а не добрая воля модуля:
//   - только каналы: писать людям этим инструментом нельзя;
//   - только туда, где человек в «Настройки → Telegram» включил и «Писать»,
//     и «Публиковать в канал», а у учётки — отправку от своего имени;
//   - не больше tg.posts_per_day постов в сутки на канал: сломанный модуль
//     не должен завалить подписчиков.

// tgLimit — сколько постов в сутки модуль может опубликовать в канал.
func (e *Engine) tgLimit() int { return e.settingInt("tg.posts_per_day", 3) }

func tgCountKey(c *store.TGChat, day time.Time) string {
	return fmt.Sprintf("tg.posted.%s.%d.%s", c.Account, c.ID, day.Format("2006-01-02"))
}

// toolTGPost — sema.telegram.post(channel, text, {image}). С картинкой пост
// уходит фото с подписью, а подпись Telegram ограничивает 1024 знаками.
func (e *Engine) toolTGPost(ctx context.Context, a []any) (any, error) {
	name := strings.TrimSpace(script.Str(a, 0))
	text := strings.TrimSpace(script.Str(a, 1))
	var image *store.Attachment
	if number := intOpt(script.Opts(a, 2), "image", 0); number > 0 {
		file, err := e.store.AttachmentByID(int64(number))
		if err != nil {
			return nil, fmt.Errorf("картинки #%d нет среди файлов", number)
		}
		image = file
	}
	if text == "" {
		return nil, fmt.Errorf("пустой пост: публиковать нечего")
	}
	length := utf8.RuneCountInString(text)
	if length > 4096 {
		return nil, fmt.Errorf("пост длиной %d знаков: Telegram принимает не больше 4096", length)
	}
	// длинный пост с картинкой уходит текстом с превью над ним, а превью
	// Telegram берёт по публичному адресу — без адреса только фото с подписью
	addr := ""
	if image != nil && length > tgPhotoCaption {
		if addr = e.ImageAddr(image.ID); addr == "" {
			return nil, fmt.Errorf("пост с картинкой длиной %d знаков, а у картинки нет публичного адреса для превью: сократите до %d", length, tgPhotoCaption)
		}
	}
	channel, err := e.channelByName(name)
	if err != nil {
		return nil, err
	}
	limit := e.tgLimit()
	key := tgCountKey(channel, time.Now())
	was, _ := strconv.Atoi(e.store.Setting(key, "0"))
	if was >= limit {
		return nil, fmt.Errorf("в «%s» сегодня уже %d постов — предел %d в сутки (tg.posts_per_day)", channel.Title, was, limit)
	}
	switch {
	case addr != "":
		if err := e.TGSendLongWithImage(ctx, channel.Account, channel.ID, 0, addr, text); err != nil {
			return nil, err
		}
	case image != nil:
		if err := e.TGSendPhoto(ctx, channel.Account, channel.ID, 0, image, text); err != nil {
			return nil, err
		}
	default:
		if err := e.TGSend(ctx, channel.Account, channel.ID, 0, text); err != nil {
			return nil, err
		}
	}
	_ = e.store.SetSetting(key, strconv.Itoa(was+1))
	return map[string]any{"channel": channel.Title, "today": was + 1, "limit": limit}, nil
}

// channelByName — канал по названию среди всех учёток. Только каналы: личный
// чат или группа с тем же именем инструменту не подходят.
func (e *Engine) channelByName(name string) (*store.TGChat, error) {
	if name == "" {
		return nil, fmt.Errorf("не названо, в какой канал публиковать")
	}
	account, err := e.store.TGAccounts()
	if err != nil {
		return nil, err
	}
	for _, u := range account {
		chats, err := e.store.TGChats(u.Phone, false)
		if err != nil {
			continue
		}
		for _, c := range chats {
			if strings.EqualFold(strings.TrimSpace(c.Title), name) {
				if c.Kind != "channel" {
					return nil, fmt.Errorf("«%s» — не канал: этим инструментом публикуют только в каналы, людям так не пишут", c.Title)
				}
				return c, nil
			}
		}
	}
	return nil, fmt.Errorf("канала «%s» нет среди ваших каналов Telegram", name)
}
