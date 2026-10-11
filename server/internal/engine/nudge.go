package engine

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Пинатель чатов. Работа, начатая в чате команды, должна доходить до конца
// без человека: если Сёма замолчал, а вопрос не закрыт и никто ничего не
// ждёт — ядро даёт ему внутренний ход «продолжай». Модель либо делает
// следующий шаг (команда, таймер), либо закрывает вопрос блоком
// ```sema-done:<итог>```, либо говорит, чего ждёт от человека.

const (
	WorkerChatNudge = "chat_nudge"
	nudgeAfterKey   = "chats.nudge_after_min"
	nudgeMaxKey     = "chats.nudge_max"
)

var doneBlock = regexp.MustCompile("(?s)```sema-done:?([^\n]*)\n?(.*?)```")

// DoneCard — карточка «вопрос закрыт» в переписке.
type DoneCard struct {
	Summary string `json:"summary"`
}

func (e *Engine) nudgeAfter() time.Duration {
	return time.Duration(e.store.SettingInt(nudgeAfterDef)) * time.Minute
}

func (e *Engine) NudgeAfterMin() int { return int(e.nudgeAfter().Minutes()) }

// NudgeMax — сколько раз Сёма пинает чат. 0 законен: «не пинать».
func (e *Engine) NudgeMax() int { return e.store.SettingInt(nudgeMaxDef) }

// workChats — чаты, за которыми ядро следит: те, что оно завело само под
// команду или разбор (у них есть срок жизни).
func (e *Engine) workChats() []int64 {
	var out []int64
	for key := range e.store.SettingsByPrefix("chat.expires.") {
		if id, err := strconv.ParseInt(strings.TrimPrefix(key, "chat.expires."), 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// waitingForHuman — в чате висит команда на подтверждение: пинать нельзя,
// решение за человеком.
func (e *Engine) waitingForHuman(chatID int64) bool {
	list, err := e.store.SSHPendingList()
	if err != nil {
		return false
	}
	for _, p := range list {
		if p.ChatID == chatID {
			return true
		}
	}
	// и вызов чужого сервера MCP, ждущий кнопки
	if calls, err := e.store.MCPPendingList(); err == nil {
		for _, p := range calls {
			if p.ChatID == chatID {
				return true
			}
		}
	}
	return false
}

// timerPending — в чате тикает таймер: ждём его, а не пинаем.
func (e *Engine) timerPending(chatID int64) bool {
	tasks, err := e.store.Tasks()
	if err != nil {
		return false
	}
	system := chatTimerPrefix + strconv.FormatInt(chatID, 10)
	for _, t := range tasks {
		if t.System == system && t.Enabled {
			return true
		}
	}
	return false
}

const nudgeNote = "Никто не писал: это внутренний ход. " +
	"Если работа не закончена — сделай следующий шаг сам: команда блоком sema-ssh:<сервер>, ожидание — блоком sema-timer:<срок>. " +
	"Если всё сделано — ответь блоком ```sema-done:<итог одной строкой>``` и больше ничего. " +
	"Если ждёшь решения человека (команда на подтверждении) — одной строкой скажи, чего именно ждёшь, и не повторяй уже сказанное."

// answerNote — пинок в переписке, где Сёме разрешено отвечать вместо
// человека: собеседник ждёт, а ответа нет (модель промолчала, связь
// рвалась, сервер перезапускали).
const answerNote = "Собеседник написал и ждёт ответа, а ответа нет. " +
	"Тебе разрешено отвечать вместо человека в этом чате: ответь одним сообщением блоком ```sema-tg``` — оно уйдёт сразу. " +
	"Если ответить нечего или нужен человек — скажи это одной строкой и больше ничего."

// answerAfter — сколько ждать, прежде чем пинать Сёму ответить. Настройка:
// в живом разговоре три минуты молчания уже заметны, в рабочем чате нет.
func (e *Engine) answerAfter() time.Duration {
	v, _ := strconv.Atoi(e.store.Setting("chats.answer_after_min", "3"))
	if v <= 0 {
		v = 3
	}
	return time.Duration(v) * time.Minute
}

// peerChatsToAnswer — переписки, где собеседник ждёт ответа, а Сёме
// разрешено отвечать вместо человека. Пинатель доводит до ответа то, что
// не довёл автоответ: он срабатывает на сообщение, а сообщение могло
// прийти, пока сервер перезапускали.
func (e *Engine) peerChatsToAnswer() []int64 {
	list, err := e.store.TGChats("", true)
	if err != nil {
		return nil
	}
	var out []int64
	for _, root := range list {
		// у группы с топиками каждая тема отвечает за себя: молчание в
		// одной не лечится ответом в другой
		for _, c := range e.tgWatchedPeers(root) {
			if c.Blocked || !c.Reply {
				continue
			}
			count, since, _ := e.store.TGWaiting(c.Account, c.ID, c.Topic)
			if count == 0 || time.Since(since) < e.answerAfter() {
				continue
			}
			// предел автоответов в час — тот же, что у автоответа: пинок не
			// повод устроить переписку двух ботов
			if n := e.store.CountTGSentSince(c.Account, c.ID, c.Topic, time.Now().Add(-time.Hour).Format(time.RFC3339)); n >= e.TGReplyMaxHour() {
				continue
			}
			if chat := e.tgConvExisting(c); chat > 0 {
				out = append(out, chat)
			}
		}
	}
	return out
}

// nudgePass — задача «Пинать чаты»: доводит начатое до конца.
func (e *Engine) nudgePass() string {
	nudged, done := 0, 0
	healed := e.HealPendingNotices() // кнопки, потерянные при закрытии по ключу
	answer := map[int64]bool{}
	for _, id := range e.peerChatsToAnswer() {
		answer[id] = true
	}
	chats := append(e.workChats(), e.peerChatsToAnswer()...)
	for _, chatID := range chats {
		if e.store.Setting(fmt.Sprintf("chat.done.%d", chatID), "") == "1" {
			done++
			continue
		}
		if thinking, _ := e.Thinking(chatID); thinking {
			continue
		}
		msgs, err := e.store.ChatMsgs(chatID, 1)
		if err != nil || len(msgs) == 0 {
			continue
		}
		last := msgs[len(msgs)-1]
		if answer[chatID] {
			// в переписке ждут ответа: пинаем, пока последнее слово за
			// собеседником, а не за нами
			if last.Role != "peer" || time.Since(last.At) < e.answerAfter() {
				continue
			}
		} else if last.Role == "user" || time.Since(last.At) < e.nudgeAfter() {
			continue
		}
		if e.waitingForHuman(chatID) || e.timerPending(chatID) {
			continue
		}
		key := fmt.Sprintf("chat.nudges.%d", chatID)
		note := nudgeNote
		if answer[chatID] {
			// счёт пинков ведётся по последнему сообщению собеседника:
			// новое сообщение — новый повод ответить
			key = fmt.Sprintf("chat.answer_nudge.%d", chatID)
			note = answerNote
			if e.store.Setting(key, "") == strconv.FormatInt(last.ID, 10) {
				continue // на это сообщение уже пинали
			}
			_ = e.store.SetSetting(key, strconv.FormatInt(last.ID, 10))
		} else {
			n, _ := strconv.Atoi(e.store.Setting(key, "0"))
			if n >= e.NudgeMax() {
				continue
			}
			_ = e.store.SetSetting(key, strconv.Itoa(n+1))
		}
		e.pendingMu.Lock()
		if e.chatNotes == nil {
			e.chatNotes = map[int64]string{}
		}
		e.chatNotes[chatID] = note
		e.pendingMu.Unlock()
		if answer[chatID] {
			log.Printf("пинатель: чат %d — собеседник ждёт ответа", chatID)
		} else {
			log.Printf("пинатель: чат %d, внутренний ход", chatID)
		}
		e.continueChat(chatID)
		nudged++
	}
	if nudged == 0 && done == 0 && healed == 0 {
		return "все чаты на месте"
	}
	return fmt.Sprintf("пнули: %d · закрыто: %d · кнопок вернули: %d", nudged, done, healed)
}

// findDone — модель говорит, что вопрос закрыт: карточка в чат, итог в
// уведомление, чат больше не пинается и скоро уйдёт в память.
func (e *Engine) findDone(ctx context.Context, chatID int64, answer string) string {
	m := doneBlock.FindStringSubmatch(answer)
	if m == nil {
		return answer
	}
	summary := strings.TrimSpace(m[1] + " " + m[2])
	if summary == "" {
		summary = "работа закончена"
	}
	// в чате идёт поручение — «готово» закрывает его, а не чат
	if a, ok := e.store.OpenAssignmentOf(chatID); ok {
		e.closeAssignment(ctx, a, summary)
		return strings.TrimSpace(doneBlock.ReplaceAllString(answer, ""))
	}
	_ = e.saveToolCard(chatID, ToolCard{Title: "Вопрос закрыт", Result: summary, Done: &DoneCard{Summary: summary}})
	_ = e.store.SetSetting(fmt.Sprintf("chat.done.%d", chatID), "1")
	// чат живёт ещё час: прочитать итог; потом сводка уйдёт в память. Только
	// чат, у которого срок уже был (чат команды, уведомления): ветке «Сёма»
	// админ-чата срок стёр бы всю переписку через час
	if e.store.Setting(fmt.Sprintf("chat.expires.%d", chatID), "") != "" {
		_ = e.store.SetSetting(fmt.Sprintf("chat.expires.%d", chatID), time.Now().Add(time.Hour).Format(time.RFC3339))
	}
	if nid, _ := strconv.ParseInt(e.store.Setting(fmt.Sprintf("chat.notify.%d", chatID), ""), 10, 64); nid > 0 {
		if n, ok := e.store.Notification(nid); ok && n.State == store.NotifyOpen {
			_ = e.store.SetNotificationResult(nid, store.NotifyField{Label: "Итог", Value: summary, Level: "ok"},
				summary, "info", store.NotifyDone, "Сёма · работа закончена")
		}
	}
	log.Printf("чат %d: вопрос закрыт — %s", chatID, clip(summary, 80))
	// из законченной работы рождается урок, а иногда и скилл — на согласование
	e.learnFromChat(chatID, summary)
	return strings.TrimSpace(doneBlock.ReplaceAllString(answer, ""))
}
