package engine

// Топики Telegram: группа с темами — контейнер без своей ленты, каждая
// тема — разговор под ним. Права выданы группе, тема их переопределяет
// только там, где человек решил иначе («как у группы» — по умолчанию).
// Ответ уходит в ту тему, откуда пришёл вопрос: адрес разговора — это
// группа плюс номер темы.

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

// topicName — название темы, пока Telegram не сказал своё.
func topicName(topic int) string {
	if topic == tg.GeneralTopic {
		return "Общий"
	}
	return "Топик " + strconv.Itoa(topic)
}

// tgTopicOf — тема входящего сообщения: у группы без топиков её нет, у
// форума сообщение без пометки лежит в «Общем».
func tgTopicOf(c *store.TGChat, u tg.Update) int {
	if c == nil || !c.Forum {
		return 0
	}
	if u.Topic > 0 {
		return u.Topic
	}
	return tg.GeneralTopic
}

// tgEffective — права темы поверх прав группы: копия чата с адресом темы
// и переопределёнными правами. Для чата без тем — сам чат.
func (e *Engine) tgEffective(c *store.TGChat, topic int) *store.TGChat {
	if c == nil || topic <= 0 {
		return c
	}
	if c.Kind == "channel" {
		// пост канала: адрес у поста свой (комментарии), а права остаются
		// у канала. Своих прав у поста нет нарочно: канал даёт сотни
		// постов, и тумблер на каждый был бы списком, который никто не читает
		eff := *c
		eff.Topic = topic
		eff.TopicTitle = e.tgPostTitleOf(c, topic)
		return &eff
	}
	if !c.Forum {
		return c
	}
	eff := *c
	eff.Topic = topic
	eff.TopicTitle = topicName(topic)
	t, ok := e.store.TGTopic(c.Account, c.ID, topic)
	if !ok {
		return &eff
	}
	if t.Title != "" {
		eff.TopicTitle = t.Title
	}
	apply := func(dst *bool, v int) {
		switch v {
		case 0:
			*dst = false
		case 1:
			*dst = true
		}
	}
	apply(&eff.Collect, t.Collect)
	apply(&eff.Reply, t.Reply)
	apply(&eff.Send, t.Send)
	apply(&eff.Files, t.Files)
	apply(&eff.Blocked, t.Blocked)
	return &eff
}

// tgRef — адрес собеседника для ключей уведомлений: учётка, чат и тема.
// Номер учётки здесь обязателен: чат 7 личной и чат 7 рабочей — разные
// разговоры, а ключ у них был бы один, и «ответили» в одном гасило бы
// семафор в другом.
func tgRef(c *store.TGChat) string {
	if c.Topic > 0 {
		return fmt.Sprintf("%s:%d/%d", c.Account, c.ID, c.Topic)
	}
	return c.Account + ":" + strconv.FormatInt(c.ID, 10)
}

// tgFromLinks — обратное: учётка, чат и тема из «+7999:7/3». Старая форма
// без учётки («7/3») понимается тоже и достаётся первой учётке: записи,
// заведённые до появления второй, не должны потерять кнопки.
func (e *Engine) tgFromLinks(ref string) (account string, id int64, topic int, ok bool) {
	if left, right, has := strings.Cut(ref, ":"); has {
		account, ref = left, right
	}
	if left, right, has := strings.Cut(ref, "/"); has {
		n, err := strconv.Atoi(right)
		if err != nil {
			return "", 0, 0, false
		}
		ref, topic = left, n
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return "", 0, 0, false
	}
	if account == "" {
		a, has := e.store.TGAccountFirst()
		if !has {
			return "", 0, 0, false
		}
		account = a.Phone
	}
	return account, id, topic, true
}

// tgWho — как назвать собеседника человеку: «Дом › Заявки» у темы.
func tgWho(c *store.TGChat) string {
	if c.Topic > 0 && c.TopicTitle != "" {
		return c.Title + " › " + c.TopicTitle
	}
	return c.Title
}

// tgConvFor — разговор Сёмы для собеседника и темы. У группы с топиками
// корневой чат становится контейнером, а тема — разговором под ним;
// заводится при первом сообщении, как чаты собеседников. Канал — одна
// лента, как в самом Telegram: посты и комментарии к ним.
func (e *Engine) tgConvFor(c *store.TGChat, topic int) (int64, error) {
	root, err := e.tgChatFor(c)
	if err != nil {
		return root, err
	}
	if c.Kind == "channel" {
		if rc, err := e.store.Chat(root); err == nil && rc.Kind != store.ChatKindChannel {
			_ = e.store.SetChatKind(root, store.ChatKindChannel) // значок канала, а не человека
		}
		return root, nil
	}
	if tgContainerKind(c) == "" || topic <= 0 {
		return root, nil
	}
	if rc, err := e.store.Chat(root); err == nil && !rc.IsContainer() {
		// сначала вид, потом переезд: разговор «Общего» заводится тем же
		// tgConvFor, и с обратным порядком он звал бы переезд по кругу
		if err := e.store.SetChatKind(root, tgContainerKind(c)); err != nil {
			return 0, err
		}
		// у контейнера ленты нет: прежняя общая переписка уехала бы с
		// экрана насовсем, поэтому она переезжает в отдельный разговор
		if err := e.moveLaneAside(c, root); err != nil {
			return 0, err
		}
	}
	if id, ok := e.store.ConversationByTopic(root, int64(topic)); ok {
		return id, nil
	}
	title := topicName(topic)
	if t, ok := e.store.TGTopic(c.Account, c.ID, topic); ok && t.Title != "" {
		title = t.Title
	} else if !ok {
		// тема появилась раньше, чем список: права по умолчанию как у группы
		if err := e.store.SaveTGTopic(c.Account, c.ID, topic, title); err != nil {
			return 0, err
		}
	}
	id, err := e.store.CreateConversation(root, store.ChatKindTopic, int64(topic), title)
	if err != nil {
		return 0, err
	}
	log.Printf("telegram: завёл разговор «%s › %s»", c.Title, title)
	if title == topicName(topic) && topic != tg.GeneralTopic {
		// тему завели в Telegram после нашего последнего захода: название
		// спрашиваем отдельно, чтобы в списке не стоял «Топик 41235»
		go e.tgNameTopic(c, topic, id)
	}
	return id, nil
}

// tgWatchedPeers — адреса, за которыми Сёма следит в этом чате: сам чат,
// а у группы с топиками — по одному на тему с правами темы. Семафор
// «ждёт ответа» и пинатель считают по разговору, а не по всей группе.
func (e *Engine) tgWatchedPeers(c *store.TGChat) []*store.TGChat {
	if c == nil || !c.Forum {
		// каналу не отвечают: посты идут потоком, ответа на них не
		// бывает, и семафор «ждёт ответа» горел бы на канале всегда
		if c != nil && !c.Blocked && c.Collect && c.Kind != "channel" {
			return []*store.TGChat{c}
		}
		return nil
	}
	topics, _ := e.store.TGTopics(c.Account, c.ID)
	var out []*store.TGChat
	for _, t := range topics {
		eff := e.tgEffective(c, t.ID)
		if eff.Blocked || !eff.Collect {
			continue
		}
		out = append(out, eff)
	}
	return out
}

// tgConvExisting — разговор Сёмы для этого адреса, если он уже заведён.
// Заводить здесь нечего: пустой разговор темы никому не нужен.
func (e *Engine) tgConvExisting(c *store.TGChat) int64 {
	root := e.tgRootOf(c)
	if root == 0 || c.Topic <= 0 || c.Kind == "channel" {
		return root // у канала одна лента: и пост, и комментарии лежат в ней
	}
	if id, ok := e.store.ConversationByTopic(root, int64(c.Topic)); ok {
		return id
	}
	return 0
}

// moveLaneAside — переписка, собранная до того, как чат стал
// контейнером, уезжает в отдельный разговор: терять её нельзя, а
// показать в контейнере негде: у группы это «Общий».
func (e *Engine) moveLaneAside(c *store.TGChat, root int64) error {
	msgs, err := e.store.ChatMsgs(root, 1)
	if err != nil || len(msgs) == 0 {
		return err
	}
	where := "«Общий»"
	aside, err := e.tgConvFor(c, tg.GeneralTopic)
	if err != nil {
		return err
	}
	n, err := e.store.MoveChatMsgs(root, aside)
	if err != nil {
		return err
	}
	log.Printf("telegram: прежняя лента «%s» (%d сообщений) уехала в %s", c.Title, n, where)
	return nil
}

// tgNameTopic — спросить у Telegram название незнакомой темы и назвать
// разговор по-человечески. Отдельным заходом: входящее сообщение ждать
// сети не должно.
func (e *Engine) tgNameTopic(c *store.TGChat, topic int, conv int64) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cl, acc, err := e.tgClientOf(c)
	if err != nil || acc.State != "ok" {
		return
	}
	if err := e.tgRefreshTopics(ctx, cl, c); err != nil {
		log.Printf("telegram: название темы %d не узнали: %v", topic, err)
		return
	}
	if t, ok := e.store.TGTopic(c.Account, c.ID, topic); ok && t.Title != "" && t.Title != topicName(topic) {
		_ = e.store.RenameChat(conv, t.Title)
	}
}

// tgRefreshTopics — темы группы из Telegram: названия обновляются,
// права человека остаются. Тема, которой больше нет, не удаляется —
// её разговор и права никуда не деваются.
func (e *Engine) tgRefreshTopics(ctx context.Context, cl *tg.Client, c *store.TGChat) error {
	if !c.Forum {
		return nil
	}
	list, err := cl.Topics(ctx, c.ID, c.Access)
	if err != nil {
		return err
	}
	for _, t := range list {
		if err := e.store.SaveTGTopic(c.Account, c.ID, t.ID, t.Title); err != nil {
			return err
		}
		if id, ok := e.store.ConversationByTopic(e.tgRootOf(c), int64(t.ID)); ok {
			if ch, err := e.store.Chat(id); err == nil && ch.Title != t.Title && t.Title != "" {
				_ = e.store.RenameChat(id, t.Title) // тему переименовали в Telegram
			}
		}
	}
	return nil
}

// tgRootOf — корневой чат собеседника, если он уже заведён; 0 — нет.
func (e *Engine) tgRootOf(c *store.TGChat) int64 {
	id, _ := strconv.ParseInt(e.store.Setting(tgChatKey(c.Account, c.ID), ""), 10, 64)
	return id
}

// TGEffective — права темы поверх прав группы, для экранов.
func (e *Engine) TGEffective(c *store.TGChat, topic int) *store.TGChat {
	return e.tgEffective(c, topic)
}

// TGTopicsOf — темы группы с их правами для экранов.
func (e *Engine) TGTopicsOf(account string, chatID int64) []*store.TGTopic {
	list, _ := e.store.TGTopics(account, chatID)
	return list
}

// TGSetTopicRight — право темы: явное «да»/«нет» или «как у группы».
func (e *Engine) TGSetTopicRight(account string, chatID int64, topic int, right string, v int) error {
	if _, ok := e.store.TGTopic(account, chatID, topic); !ok {
		if err := e.store.SaveTGTopic(account, chatID, topic, topicName(topic)); err != nil {
			return err
		}
	}
	return e.store.SetTGTopicRight(account, chatID, topic, right, v)
}
