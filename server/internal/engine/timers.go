package engine

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Таймеры чата: модель пишет ```sema-timer:10 мин\nчто проверить``` —
// в чате появляется карточка, а через срок сердцебиение будит этот чат:
// заметка кладётся сообщением, и Сёма продолжает сам (команды, второй ход).
// Таймер — разовая задача с системой chat_timer:<чат>, живёт на «Задачах».

var timerBlock = regexp.MustCompile("(?s)```sema-timer:([^\n]*)\n(.*?)```")

const chatTimerPrefix = "chat_timer:"

// TimerCard — карточка таймера в переписке.
type TimerCard struct {
	At     string `json:"at"`     // когда сработает, RFC3339
	Note   string `json:"note"`   // что сделать
	Status string `json:"status"` // set | fired
	TaskID int64  `json:"task_id,omitempty"`
}

// timerMaxDelay — предел здравого смысла для таймера из переписки: дальше
// это уже задача с расписанием, а не «напомни через».
const timerMaxDelay = 30 * 24 * time.Hour

// parseTimerDuration — «10 мин», «1 ч», «через 2 часа», «90 секунд».
func parseTimerDuration(s string) (time.Duration, bool) {
	s = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "через")))
	m := regexp.MustCompile(`(\d+)\s*([a-zа-яё]*)`).FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	if n <= 0 {
		return 0, false
	}
	unit := m[2]
	var d time.Duration
	switch {
	case strings.HasPrefix(unit, "сек"), strings.HasPrefix(unit, "s"):
		d = time.Duration(n) * time.Second
	case strings.HasPrefix(unit, "ч"), strings.HasPrefix(unit, "h"):
		d = time.Duration(n) * time.Hour
	case strings.HasPrefix(unit, "д"), strings.HasPrefix(unit, "d"):
		d = time.Duration(n) * 24 * time.Hour
	default: // минуты по умолчанию
		d = time.Duration(n) * time.Minute
	}
	// «через 10000000000 мин» переполняет счётчик и даёт срок в прошлом:
	// таймер срабатывал бы сразу. Всё за пределом здравого смысла отклоняем.
	if d <= 0 || d > timerMaxDelay {
		return 0, false
	}
	return d, true
}

// findTimers вынимает таймеры из ответа модели: ставит задачу, кладёт карточку.
func (e *Engine) findTimers(chatID int64, answer string) string {
	found := timerBlock.FindAllStringSubmatch(answer, 3)
	if len(found) > 0 {
		log.Printf("чат %d: таймеров в ответе %d", chatID, len(found))
	}
	for _, m := range found {
		d, ok := parseTimerDuration(m[1])
		note := strings.TrimSpace(m[2])
		if !ok {
			_ = e.saveToolCard(chatID, ToolCard{Title: "Таймер", Err: "не понял срок «" + strings.TrimSpace(m[1]) + "» — нужно «10 мин», «1 ч»"})
			continue
		}
		if note == "" {
			note = "проверить, что изменилось"
		}
		if _, err := e.CreateChatTimer(chatID, d, note); err != nil {
			_ = e.saveToolCard(chatID, ToolCard{Title: "Таймер", Err: err.Error()})
		}
	}
	return strings.TrimSpace(timerBlock.ReplaceAllString(answer, ""))
}

// CreateChatTimer — разовая задача-будильник для чата; сердцебиение тикает
// раз в секунду, но расписание «дд.мм.гггг чч:мм» — минутное, поэтому
// срок короче минуты округляется до минуты.
func (e *Engine) CreateChatTimer(chatID int64, d time.Duration, note string) (int64, error) {
	if d < time.Minute {
		d = time.Minute
	}
	at := time.Now().Add(d)
	task := &store.Task{
		Title: "Таймер · " + timerDuration(d) + " · " + clip(note, 60), Prompt: note,
		System: chatTimerPrefix + strconv.FormatInt(chatID, 10), Schedule: at.Format("02.01.2006 15:04"),
		Enabled: true, Once: true, ChatID: chatID, Sort: 900,
	}
	if err := e.store.CreateTask(task); err != nil {
		return 0, err
	}
	card := ToolCard{Title: "Таймер · " + timerDuration(d), Result: "сработает в " + at.Format("15:04") + " · " + note,
		Timer: &TimerCard{At: at.Format(time.RFC3339), Note: note, Status: "set", TaskID: task.ID}}
	_ = e.saveToolCard(chatID, card)
	return task.ID, nil
}

// fireChatTimer — срок вышел: заметка в чат карточкой и сообщением, Сёма
// продолжает сам; задача удаляется, чат команды живёт дальше.
func (e *Engine) fireChatTimer(t *store.Task) string {
	chatID, _ := strconv.ParseInt(strings.TrimPrefix(t.System, chatTimerPrefix), 10, 64)
	if chatID == 0 {
		return "чат не найден"
	}
	if _, err := e.store.Chat(chatID); err != nil {
		_ = e.store.DeleteTask(t.ID)
		return "чат уже удалён"
	}
	// одна карточка, без дублирующего сообщения: заметка уходит модели через
	// промпт, а в чате остаётся карточка и ответ Сёмы
	_ = e.saveToolCard(chatID, ToolCard{Title: "Таймер сработал", Result: t.Prompt,
		Timer: &TimerCard{At: time.Now().Format(time.RFC3339), Note: t.Prompt, Status: "fired"}})
	if e.store.Setting(fmt.Sprintf("chat.expires.%d", chatID), "") != "" {
		e.touchChatExpiry(chatID)
	}
	e.pendingMu.Lock()
	if e.chatNotes == nil {
		e.chatNotes = map[int64]string{}
	}
	e.chatNotes[chatID] = t.Prompt
	e.pendingMu.Unlock()
	e.continueChat(chatID)
	_ = e.store.DeleteTask(t.ID)
	log.Printf("таймер чата %d сработал: %s", chatID, clip(t.Prompt, 60))
	return "сработал: " + clip(t.Prompt, 80)
}

func timerDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 24*time.Hour:
		if d%time.Hour == 0 {
			return fmt.Sprintf("%d ч", int(d.Hours()))
		}
		return fmt.Sprintf("%d ч %d мин", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d д", int(d.Hours()/24))
	}
}
