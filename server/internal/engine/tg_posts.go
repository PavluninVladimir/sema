package engine

// Каналы Telegram: канал — контейнер без своей ленты, каждый пост —
// разговор под ним, комментарии к посту — лента этого разговора.
//
// Комментариев в самом канале нет: Telegram пересылает пост в
// привязанную группу обсуждения, и отвечают уже там. Привязку «копия
// поста в группе → пост канала» спрашиваем один раз
// (messages.getDiscussionMessage) и запоминаем: без неё комментарий
// неотличим от обычного сообщения группы, а ходить за ней в сеть на
// каждый комментарий — лишний заход.
//
// Права и сам разговор остаются у канала, а физический адрес — куда
// отправлять и под каким чатом хранить — у группы обсуждения. Иначе
// номера сообщений двух разных чатов легли бы в архив под одним чатом,
// и комментарий молча затёр бы пост.

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// tgPostDays — насколько старый пост ещё заводит разговор. Настройка, а
// не число в коде: у одного канала пост живёт день, у другого
// обсуждение тянется месяц.
func (e *Engine) tgPostDays() int { return e.store.SettingInt(tgPostDaysDef) }

// tgPostTooOld — пост старше срока. Такой ложится в архив канала (поиск
// и память его видят), но разговором не становится: первое же включение
// канала завело бы сотни разговоров, которых никто не открывал.
func (e *Engine) tgPostTooOld(at time.Time) bool {
	if at.IsZero() {
		return false // времени нет — считаем свежим: молчание не повод выбрасывать пост
	}
	return time.Since(at) > time.Duration(e.tgPostDays())*24*time.Hour
}

// postTitle — как назвать пост человеку: первой строкой самого поста,
// а без текста — номером.
func postTitle(text string, post int) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	if line == "" {
		return postName(post)
	}
	return clip(line, 40)
}

// postName — имя поста, пока его текст неизвестен.
func postName(post int) string { return "Пост " + strconv.Itoa(post) }

// tgContainerKind — во что превращается корневой чат собеседника: группа
// с темами становится контейнером без своей ленты. Пусто — обычный чат с
// лентой, и канал тоже: владелец 17 сентября 2026 попросил канал «как в
// Telegram» — одной лентой постов, а не разговором на каждый пост.
func tgContainerKind(c *store.TGChat) string {
	if c.Forum {
		return store.ChatKindGroup
	}
	return ""
}

// tgPostTitleOf — первая строка поста из архива: подпись комментария и
// название поста в промпте автоответа.
func (e *Engine) tgPostTitleOf(c *store.TGChat, post int) string {
	if list, _ := e.store.TGMessages(c.Account, c.ID, post, 5); len(list) > 0 {
		for _, m := range list {
			if m.MsgID == post {
				return postTitle(m.Text, post)
			}
		}
	}
	return postName(post)
}

// tgCommentText — комментарий в ленте канала. Под постом в Telegram он
// виден сам собой, а в одной ленте без подписи не понять, к чему он.
func (e *Engine) tgCommentText(c *store.TGChat, post int, text string) string {
	return "↩ к посту «" + e.tgPostTitleOf(c, post) + "»\n" + text
}

// tgAddr — физический адрес разговора в Telegram: куда отправлять и в
// какой истории читать. Обычно это сам чат, а под постом канала —
// привязанная группа обсуждения и корень ветки комментариев.
func (e *Engine) tgAddr(c *store.TGChat) (id, access int64, kind string, topic int) {
	if c.Kind == "channel" && c.Topic > 0 {
		if l, ok := e.store.TGPostLink(c.Account, c.ID, c.Topic); ok {
			return l.Group, l.Access, "group", l.Root
		}
	}
	return c.ID, c.Access, c.Kind, c.Topic
}

// tgCanReply — есть ли вообще куда писать. В сам канал Сёма не пишет:
// это ведение канала, отдельное право и отдельный разговор. Ответ под
// постом уходит в группу обсуждения, и без неё комментировать негде.
func (e *Engine) tgCanReply(c *store.TGChat) error {
	if c.Kind != "channel" {
		return nil
	}
	if c.Topic <= 0 {
		// сам канал: пост уходит от вашего аккаунта, и только там, где у
		// него есть право публиковать — владелец или админ с постами
		if c.CanPost {
			return nil
		}
		return fmt.Errorf("в канале «%s» у вашего аккаунта нет права публиковать — Сёма туда не пишет", c.Title)
	}
	if _, ok := e.store.TGPostLink(c.Account, c.ID, c.Topic); !ok {
		return fmt.Errorf("у поста в «%s» нет группы обсуждения: комментировать негде", c.Title)
	}
	return nil
}

// tgRoute — чей это разговор и по каким правам. Обычно тот чат, куда
// сообщение пришло, но комментарий физически приходит в группу
// обсуждения, а принадлежит каналу: иначе комментарии сваливаются в
// общую ленту группы и пост от них не отличить. Третий ответ — «пропустить»:
// копию поста, которую Telegram сам кладёт в группу, второй раз
// показывать незачем.
func (e *Engine) tgRoute(account string, u tg.Update) (peer *store.TGChat, topic int, skip bool) {
	c, ok := e.store.TGChat(account, u.ChatID)
	if !ok {
		return nil, 0, false
	}
	if c.Kind == "group" && !c.Forum {
		if _, ok := e.store.TGPostByRoot(account, u.ChatID, u.MsgID); ok {
			return nil, 0, true // это копия поста: он уже лежит в своём разговоре
		}
		if u.Thread > 0 {
			if l, ok := e.store.TGPostByRoot(account, u.ChatID, u.Thread); ok {
				if ch, ok := e.store.TGChat(account, l.Channel); ok {
					return ch, l.Post, false
				}
			}
		}
	}
	if c.Kind == "channel" {
		// пост ложится в ленту канала, а номер поста остаётся темой
		// архива: под ним живут комментарии
		return c, u.MsgID, false
	}
	return c, tgTopicOf(c, u), false
}

// tgArchiveTopic — под какой темой хранить сообщение в архиве. Архив
// физический: чат, откуда сообщение пришло, и номер в этом чате. У
// комментария тема — корень ветки в группе обсуждения, а не номер
// поста: номера сообщений канала и группы в одном чате столкнулись бы,
// и комментарий затёр бы пост молча.
func tgArchiveTopic(u tg.Update, peer *store.TGChat, topic int) int {
	if peer != nil && peer.Kind == "channel" && u.ChatID != peer.ID {
		return u.Thread
	}
	return topic
}

// tgLinkPost — узнать, где живут комментарии к посту, и запомнить.
// Отдельным заходом: входящий пост ждать сети не должен. После привязки
// подтягиваются уже написанные комментарии — пост мог полежать час,
// пока Сёма его не увидел.
func (e *Engine) tgLinkPost(c *store.TGChat, post int, feed int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cl, acc, err := e.tgClientOf(c)
	if err != nil || acc.State != "ok" {
		return
	}
	e.tgComments(ctx, cl, c, post, feed)
}

// tgComments — привязка поста к обсуждению (если её ещё нет) и уже
// написанные комментарии в ленту канала. Одна отметка истории на пост:
// второй раз те же комментарии не ложатся.
func (e *Engine) tgComments(ctx context.Context, cl *tg.Client, c *store.TGChat, post int, feed int64) {
	if _, ok := e.store.TGPostLink(c.Account, c.ID, post); !ok {
		var (
			group, access int64
			root          int
			err           error
		)
		for attempt := 0; attempt < 3; attempt++ {
			group, access, root, err = cl.DiscussionRoot(ctx, c.ID, c.Access, post)
			pause, flood := tg.FloodWait(err)
			if !flood || pause > 2*time.Minute {
				break
			}
			// «подождите две секунды» — не «обсуждения нет»: 17 сентября
			// 2026 пост 37 так и остался без привязки к комментариям
			if !sleepCtx(ctx, pause+time.Second) {
				return
			}
		}
		if _, flood := tg.FloodWait(err); flood {
			log.Printf("telegram: привязку поста %d канала «%s» отложил: Telegram просит подождать", post, c.Title)
			return // отметки нет — попробуем при следующем запуске
		}
		if err != nil {
			// обсуждения у канала может не быть вовсе — это не поломка:
			// тогда пост остаётся без комментариев, и второй раз не спрашиваем
			log.Printf("telegram: у поста %d канала «%s» обсуждения нет: %v", post, c.Title, err)
			_ = e.store.SetSetting(tgNoDiscussionKey(c, post), "1")
			return
		}
		if err := e.store.SaveTGPostLink(&store.TGPostLink{
			Account: c.Account, Channel: c.ID, Post: post, Group: group, Access: access, Root: root,
		}); err != nil {
			log.Printf("telegram: привязка поста %d не сохранена: %v", post, err)
			return
		}
	}
	if err := e.tgOpenHistory(ctx, cl, e.tgEffective(c, post), feed); err != nil {
		log.Printf("telegram: комментарии к посту %d не прочитаны: %v", post, err)
	}
}

// tgNoDiscussionKey — отметка «у поста обсуждения нет, Telegram так и
// ответил»: без неё каждый запуск спрашивал бы заново про каждый пост.
func tgNoDiscussionKey(c *store.TGChat, post int) string {
	return fmt.Sprintf("tg.nodiscussion.%s.%d.%d", c.Account, c.ID, post)
}

// tgCommentsMissing — посты канала из архива, которые ещё не привязаны к
// обсуждению и про которые Telegram не ответил «обсуждения нет»: привязка
// могла сорваться на «подождите» или на перезапуске. Спрашиваем при
// старте слушателя, по одному.
func (e *Engine) tgCommentsMissing(c *store.TGChat) {
	if feed := e.tgRootOf(c); feed > 0 {
		e.tgCommentsLater(c, e.tgPostsWithoutLink(c), feed)
	}
}

// tgPostsWithoutLink — последние посты архива без привязки к обсуждению и
// без ответа «обсуждения нет», от старых к новым.
func (e *Engine) tgPostsWithoutLink(c *store.TGChat) []int {
	list, err := e.store.TGMessages(c.Account, c.ID, 0, 50)
	if err != nil {
		return nil
	}
	var posts []int
	for i := len(list) - 1; i >= 0; i-- { // от старых к новым
		m := list[i]
		if m.Topic != m.MsgID {
			continue
		}
		if _, ok := e.store.TGPostLink(c.Account, c.ID, m.MsgID); ok {
			continue
		}
		if e.store.Setting(tgNoDiscussionKey(c, m.MsgID), "") != "" {
			continue
		}
		posts = append(posts, m.MsgID)
	}
	return posts
}

// tgCommentsLater — комментарии к свежим постам одним заходом после
// истории канала: по одному посту за раз, а не тридцать запросов разом.
func (e *Engine) tgCommentsLater(c *store.TGChat, posts []int, feed int64) {
	if len(posts) == 0 {
		return
	}
	// один обход на канал: история канала и досбор при старте иначе
	// читали бы комментарии одного поста разом и клали их дважды
	key := fmt.Sprintf("%s.%d", c.Account, c.ID)
	if _, busy := e.tgCommentsBusy.LoadOrStore(key, true); busy {
		return
	}
	go func() {
		defer e.tgCommentsBusy.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cl, acc, err := e.tgClientOf(c)
		if err != nil || acc.State != "ok" {
			return
		}
		for _, p := range posts {
			if ctx.Err() != nil {
				return
			}
			e.tgComments(ctx, cl, c, p, feed)
		}
	}()
}

// tgChannelFeed — канал из разговоров по постам снова одной лентой.
// Разово на канал: посты-копии из разговоров удаляются (они лежат в
// архиве и в самом Telegram), написанное Сёмой и человеком переезжает в
// ленту, ветки — под канал, а история перечитывается заново — уже с
// картинками и с настоящим временем постов.
func (e *Engine) tgChannelFeed(c *store.TGChat) error {
	root := e.tgRootOf(c)
	if root == 0 {
		return nil
	}
	rc, err := e.store.Chat(root)
	if err != nil {
		return nil
	}
	kids, err := e.store.ChatChildren(root)
	if err != nil {
		return err
	}
	moved, dropped := 0, 0
	for _, k := range kids {
		if k.Kind != store.ChatKindPost {
			continue
		}
		// ветки разговора поста — под канал; «Сёма» у канала один
		sub, _ := e.store.ChatChildren(k.ID)
		for _, th := range sub {
			if th.Kind == store.ChatKindSema {
				if own, ok := e.store.ThreadOf(root, store.ChatKindSema, 0); ok {
					if _, err := e.store.MoveChatMsgs(th.ID, own); err != nil {
						return err
					}
					if err := e.store.DeleteChat(th.ID); err != nil {
						return err
					}
					continue
				}
			}
			if err := e.store.SetChatParent(th.ID, root); err != nil {
				return err
			}
		}
		msgs, err := e.store.ChatMsgs(k.ID, 100000)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			// копия поста или комментария: оригинал в архиве и в самом
			// Telegram. Копия, от которой человек открыл ветку, остаётся —
			// иначе ветка потеряет своё сообщение
			if m.Role == "peer" && !e.store.HasThreadFrom(m.ID) {
				if err := e.store.DeleteChatMsg(m.ID); err != nil {
					return err
				}
				dropped++
				continue
			}
			moved++
		}
		if _, err := e.store.MoveChatMsgs(k.ID, root); err != nil {
			return err
		}
		if err := e.store.DeleteChat(k.ID); err != nil {
			return err
		}
	}
	if rc.Kind != store.ChatKindChannel {
		if err := e.store.SetChatKind(root, store.ChatKindChannel); err != nil {
			return err
		}
	}
	// история канала и комментарии перечитываются: лента собирается заново
	e.store.DeleteSettingKey(tgHistoryKey(c.Account, c.ID, 0))
	for key := range e.store.SettingsByPrefix(tgHistoryKey(c.Account, c.ID, 0) + ".") {
		e.store.DeleteSettingKey(key)
	}
	log.Printf("telegram: канал «%s» снова одной лентой: копий постов убрано %d, переехало сообщений %d", c.Title, dropped, moved)
	return nil
}
