package engine

// Семафор «вам не ответили». Сёма не отвечает за человека и не говорит
// вместо него — она показывает, что разговор ждёт. Чем дольше молчание и
// чем больше сообщений подряд, тем громче уведомление: сначала спокойная
// запись, потом предупреждение, потом тревога.

import (
	"fmt"
	"strconv"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// WorkerTGWait — системное имя задачи «Ждут ответа в Telegram».
const WorkerTGWait = "tg_wait"

// tgWaitPass — проход по выбранным чатам: где собеседник написал, а
// ответа нет дольше порога, поднимается уведомление; ответили — запись
// закрывается сама.
func (e *Engine) tgWaitPass() error {
	list, err := e.store.TGChats("", true) // все учётки: молчание есть в каждой
	if err != nil {
		return err
	}
	warnAfter, alertAfter, many := e.tgWaitLimits()
	var peers []*store.TGChat
	for _, c := range list {
		peers = append(peers, e.tgWatchedPeers(c)...)
	}
	for _, c := range peers {
		if c.Blocked {
			continue
		}
		key := "tg-wait:" + tgRef(c)
		if e.store.Setting(tgWaitOffKey(c.Account, c.ID), "") == "1" {
			continue // в этом чате человек попросил не напоминать
		}
		count, since, last := e.store.TGWaiting(c.Account, c.ID, c.Topic)
		if count == 0 {
			e.CloseNotification(key, store.NotifyDone, "ответили") // семафор гаснет
			continue
		}
		// «не нужно» гасит семафор до следующего сообщения собеседника:
		// человек прочитал и решил не отвечать — напоминать про то же
		// самое второй раз незачем
		if e.store.Setting(tgWaitSkipKey(c.Account, c.ID), "") == waitPrint(count, since) {
			continue
		}
		waited := time.Since(since)
		if waited < warnAfter {
			continue
		}
		level := "info"
		switch {
		case waited >= alertAfter || count >= many*2:
			level = "alert"
		case count >= many:
			level = "warn"
		}
		semaChat := e.tgConvExisting(c)
		note := &store.Notification{
			Key: key, Source: "core", Level: level,
			Title:   "Ждёт ответа · " + tgWho(c),
			Summary: clip(last, 200),
			Fields: []store.NotifyField{
				{Label: "Сообщений без ответа", Value: strconv.Itoa(count)},
				{Label: "Молчание", Value: humanWait(waited)},
			},
		}
		// ссылка кнопки — с номером учётки: иначе «не напоминать» гасило бы
		// семафор у одноимённого чата соседней учётки
		id := c.Account + ":" + strconv.FormatInt(c.ID, 10)
		if semaChat > 0 {
			note.Actions = append(note.Actions,
				store.NotifyAction{Label: "Открыть переписку", Kind: "goto", Ref: "chat:" + strconv.FormatInt(semaChat, 10)})
		}
		// разбираться не обязательно: «не нужно» закрывает до следующего
		// сообщения, «не напоминать» — навсегда в этом чате
		note.Actions = append(note.Actions,
			store.NotifyAction{Label: "Не нужно", Kind: "core", Op: "tg_wait_skip", Ref: id},
			store.NotifyAction{Label: "Не напоминать в этом чате", Kind: "core", Op: "tg_wait_off", Ref: id})
		_, _ = e.Notify(note)
	}
	return nil
}

// humanWait — «2 часа 10 минут» вместо «2h10m0s».
func humanWait(d time.Duration) string {
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	h, rest := m/60, m%60
	if h < 24 {
		if rest == 0 {
			return fmt.Sprintf("%d ч", h)
		}
		return fmt.Sprintf("%d ч %d мин", h, rest)
	}
	return fmt.Sprintf("%d сут %d ч", h/24, h%24)
}

// Ключи настроек семафора: «не нужно» помнит, на чём остановились, а
// «не напоминать» просто выключает чат.
func tgWaitSkipKey(account string, chat int64) string {
	return fmt.Sprintf("tg.wait_skip.%s.%d", account, chat)
}
func tgWaitOffKey(account string, chat int64) string {
	return fmt.Sprintf("tg.wait_off.%s.%d", account, chat)
}

// tgWaitLimits — пороги семафора настройкой, а не числом в коде: у одного
// собеседника полчаса молчания — норма, у другого уже тревога. Через сколько
// молчание становится записью, через сколько тревогой и сколько сообщений
// подряд поднимают уровень.
func (e *Engine) tgWaitLimits() (warn, alert time.Duration, messages int) {
	return time.Duration(e.store.SettingInt(tgWaitWarnDef)) * time.Minute,
		time.Duration(e.store.SettingInt(tgWaitAlertDef)) * time.Minute,
		e.store.SettingInt(tgWaitMsgsDef)
}

// closeWaitNotices — погасить семафор чата и всех его тем: ключи у тем
// свои, и «не нужно» должно снимать их все.
func (e *Engine) closeWaitNotices(account string, chatID int64, state, why string) {
	e.CloseNotification(fmt.Sprintf("tg-wait:%s:%d", account, chatID), state, why)
	if c, ok := e.store.TGChat(account, chatID); ok {
		for _, p := range e.tgWatchedPeers(c) {
			e.CloseNotification("tg-wait:"+tgRef(p), state, why)
		}
	}
}

// waitPrint — отпечаток нынешнего ожидания: сколько сообщений ждут и с
// какого времени. Пришло новое — отпечаток другой, семафор загорится
// снова.
func waitPrint(count int, since time.Time) string {
	return fmt.Sprintf("%d/%d", count, since.Unix())
}

// TGWaitSkip — «не нужно»: погасить семафор до следующего сообщения.
func (e *Engine) TGWaitSkip(account string, chatID int64) error {
	count, since, _ := e.store.TGWaiting(account, chatID, 0)
	if err := e.store.SetSetting(tgWaitSkipKey(account, chatID), waitPrint(count, since)); err != nil {
		return err
	}
	e.closeWaitNotices(account, chatID, store.NotifyDismissed, "не нужно")
	return nil
}

// TGWaitOff — «не напоминать в этом чате»: семафор для него выключен,
// пока человек не включит его снова в настройках собеседника.
func (e *Engine) TGWaitOff(account string, chatID int64, off bool) error {
	v := ""
	if off {
		v = "1"
	}
	if err := e.store.SetSetting(tgWaitOffKey(account, chatID), v); err != nil {
		return err
	}
	if off {
		e.closeWaitNotices(account, chatID, store.NotifyDismissed, "не напоминать")
	}
	return nil
}

// TGWaitIsOff — выключен ли семафор в чате (для экрана настроек).
func (e *Engine) TGWaitIsOff(account string, chatID int64) bool {
	return e.store.Setting(tgWaitOffKey(account, chatID), "") == "1"
}
