package engine

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Публикация в канал MAX из модуля — пара к telegram.post: владелец ведёт
// «Технический форум» в обоих мессенджерах и попросил дублировать посты
// (17 сентября 2026). Границы те же и держит сам инструмент:
//   - только каналы MAX: людям и группам этим инструментом не пишут;
//   - только туда, где в «Настройки → MAX» у канала включено «Писать», а у
//     аккаунта — отправка от вашего имени;
//   - не больше max.posts_per_day постов в сутки на канал.

// maxPostMAX — длина поста MAX. Точного предела протокол не называет;
// держим тот же порядок, что у Telegram, чтобы один текст шёл в оба.
const maxPostMAX = 4000

func (e *Engine) maxLimit() int { return e.settingInt("max.posts_per_day", 3) }

func maxCountKey(c *store.MAXChat, day time.Time) string {
	return fmt.Sprintf("max.posted.%d.%s", c.ID, day.Format("2006-01-02"))
}

// toolMAXPost — sema.max.post(channel, text, {image}). Картинка уходит
// фотографией вместе с текстом одним сообщением; фото не приняли — тем
// же сообщением файлом, чтобы пост не пропал из-за обложки.
func (e *Engine) toolMAXPost(ctx context.Context, a []any) (any, error) {
	name := strings.TrimSpace(script.Str(a, 0))
	text := strings.TrimSpace(script.Str(a, 1))
	if text == "" {
		return nil, fmt.Errorf("пустой пост: публиковать нечего")
	}
	if n := utf8.RuneCountInString(text); n > maxPostMAX {
		return nil, fmt.Errorf("пост длиной %d знаков: в MAX публикуем не больше %d", n, maxPostMAX)
	}
	var image *store.Attachment
	if number := intOpt(script.Opts(a, 2), "image", 0); number > 0 {
		file, err := e.store.AttachmentByID(int64(number))
		if err != nil {
			return nil, fmt.Errorf("картинки #%d нет среди файлов", number)
		}
		image = file
	}
	channel, err := e.maxChannelByName(name)
	if err != nil {
		return nil, err
	}
	if channel.Blocked {
		return nil, fmt.Errorf("канал «%s» в MAX заблокирован: Сёма его не трогает", channel.Title)
	}
	if !channel.Send {
		return nil, fmt.Errorf("в «Настройки → MAX» у канала «%s» не включено «Писать»", channel.Title)
	}
	limit := e.maxLimit()
	key := maxCountKey(channel, time.Now())
	was, _ := strconv.Atoi(e.store.Setting(key, "0"))
	if was >= limit {
		return nil, fmt.Errorf("в «%s» (MAX) сегодня уже %d постов — предел %d в сутки (max.posts_per_day)", channel.Title, was, limit)
	}
	var bytes []byte
	if image != nil {
		if bytes, err = e.FileBytes(image); err != nil {
			return nil, fmt.Errorf("картинка #%d не читается: %v", image.ID, err)
		}
	}
	cl, acc, err := e.maxClient()
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	if !acc.CanSend {
		return nil, fmt.Errorf("отправка от вашего имени в MAX выключена — включите её в «Настройки → MAX»")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := cl.Login(ctx); err != nil {
		return nil, err
	}
	as := "текстом"
	switch {
	case image == nil:
		err = cl.Send(ctx, channel.ID, text)
	default:
		as = "с фото"
		if err = cl.SendPhoto(ctx, channel.ID, image.Filename, bytes, text); err != nil {
			log.Printf("max: фото в «%s» не приняли (%v) — отправляю картинку файлом", channel.Title, err)
			as = "с картинкой файлом"
			err = cl.SendFile(ctx, channel.ID, image.Filename, bytes, text)
		}
	}
	if err != nil {
		return nil, err
	}
	_ = e.store.SetSetting(key, strconv.Itoa(was+1))
	log.Printf("max: пост в «%s» %s (%d знаков)", channel.Title, as, utf8.RuneCountInString(text))
	return map[string]any{"channel": channel.Title, "today": was + 1, "limit": limit, "how": as}, nil
}

// maxChannelByName — канал MAX по названию. Только каналы.
func (e *Engine) maxChannelByName(name string) (*store.MAXChat, error) {
	if name == "" {
		return nil, fmt.Errorf("не названо, в какой канал MAX публиковать")
	}
	chats, err := e.store.MAXChats(false)
	if err != nil {
		return nil, err
	}
	for _, c := range chats {
		if strings.EqualFold(strings.TrimSpace(c.Title), name) {
			if c.Kind != "channel" {
				return nil, fmt.Errorf("«%s» в MAX — не канал: этим инструментом публикуют только в каналы", c.Title)
			}
			return c, nil
		}
	}
	return nil, fmt.Errorf("канала «%s» нет среди ваших чатов MAX — обновите список в «Настройки → MAX»", name)
}
