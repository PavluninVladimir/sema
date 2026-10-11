package engine

// Сводка сессии не бесконечная: длинная работа вытесняет собственное
// начало. Пять часов правок дают кристалл по последнему часу, а первые
// решения пропадают молча — это та же потеря, что и упавшая задача.
//
// Поэтому переполненная сводка не обрезается, а сворачивается: старая
// половина уходит в память отдельной записью, в сводке остаётся строка о
// том, что было раньше. Свёртка идёт очередью: она стоит вызова модели.

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// WorkSessionFold — свернуть переполненную сводку сессии.
const WorkSessionFold = "session_fold"

const (
	summaryKeep      = 8000                  // сколько знаков сводки держим
	summaryFoldKey   = "memory.summary_fold" // порог свёртки
	foldedMarkPrefix = "[ранее в этой сессии] "
)

// SummaryFoldAt — при какой длине сводки пора сворачивать.
func (e *Engine) SummaryFoldAt() int { return e.settingInt(summaryFoldKey, 6000) }

// noteAndFold дописывает заметку и, если сводка распухла, ставит свёртку
// в очередь.
func (e *Engine) noteAndFold(sid int64, text string) error {
	if err := e.store.AppendSessionSummary(sid, text, summaryKeep); err != nil {
		return err
	}
	s, err := e.store.MemorySession(sid)
	if err != nil {
		return nil
	}
	if len([]rune(s.Summary)) < e.SummaryFoldAt() {
		return nil
	}
	return e.store.Enqueue(WorkSessionFold, strconv.FormatInt(sid, 10), "", 4)
}

const foldPrompt = `Сожми начало рабочей сессии в 2–4 предложения по-русски: что делали и к чему пришли.
Только суть: решения, находки, что сломалось и как починили. Без воды и без пересказа реплик.

%s`

// handleSessionFold — работа очереди: свернуть начало сводки в память.
func (e *Engine) handleSessionFold(ctx context.Context, w *store.WorkItem) error {
	sid, err := strconv.ParseInt(w.Ref, 10, 64)
	if err != nil {
		return nil
	}
	s, err := e.store.MemorySession(sid)
	if err != nil {
		return nil // сессии больше нет — сворачивать нечего
	}
	runes := []rune(s.Summary)
	if len(runes) < e.SummaryFoldAt() {
		return nil // успели закрыть или свернуть раньше
	}
	half := len(runes) / 2
	head, tail := string(runes[:half]), string(runes[half:])
	// режем по строке, чтобы не рвать реплику посередине
	if i := strings.LastIndex(head, "\n"); i > 0 {
		tail, head = head[i+1:]+tail, head[:i]
	}
	// повтор после сбоя между записью в память и заменой сводки не должен
	// класть в память вторую свёртку: ключ по сессии и началу текста, а
	// готовая свёртка — заметкой у сделанного действия
	// ключ — сессия: сводка между попытками растёт (хуки дописывают, хвост
	// режется), и ключ по тексту плыл бы. Свёртки одной сессии идут по
	// очереди, ключ снимается после замены сводки
	key := fmt.Sprintf("fold:%d", sid)
	if _, err := e.store.OutsideStarted(key); err != nil {
		return err
	}
	short := e.store.OutsideNote(key)
	if short == "" {
		answer, _, err := e.askMemory(llm.WithConsumer(ctx, "core:memory"), fmt.Sprintf(foldPrompt, head))
		if err != nil {
			return err // очередь повторит: терять начало нельзя
		}
		short = strings.TrimSpace(answer)
		if short == "" {
			return fmt.Errorf("модель вернула пустую свёртку")
		}
		// ответ модели — единственное, что стоит денег и невоспроизводимо:
		// сохраняется до записи в память, чтобы повтор его не переспрашивал
		if err := e.store.NoteForAction(key, short); err != nil {
			return err
		}
	}
	consumer, cerr := e.store.MemoryConsumerByID(s.ConsumerID)
	if cerr == nil && consumer != nil && !e.sessionHasMemory(sid, short) {
		o := Origin{Consumer: consumer, SessionID: sid}
		if _, err := e.Remember(ctx, o, MemoryRecord{
			Kind: store.MemFact, Area: firstArea(consumer.Areas), Text: short,
			Model: e.Role(RoleMemory).Model,
		}); err != nil {
			return fmt.Errorf("свёртка сессии %d не записалась в память: %w", sid, err) // повторим, голову не терять
		}
	}
	folded := foldedMarkPrefix + short + "\n" + tail
	if err := e.store.SetSessionSummary(sid, folded); err != nil {
		return err
	}
	_ = e.store.ForgetOutside(key) // сводка заменена — следующая свёртка сессии пойдёт с чистого ключа
	log.Printf("память: сводка сессии %d свёрнута — начало ушло в память записью", sid)
	return nil
}

// sessionHasMemory — запись с этим текстом у сессии уже есть: повтор после
// сбоя между записью и заменой сводки не должен класть вторую.
func (e *Engine) sessionHasMemory(sid int64, text string) bool {
	list, err := e.store.MemoriesBySession(sid)
	if err != nil {
		return false
	}
	for _, m := range list {
		if strings.TrimSpace(m.Text) == strings.TrimSpace(text) {
			return true
		}
	}
	return false
}

// firstArea — куда класть свёртку: первая выданная область потребителя.
func firstArea(areas []string) string {
	if len(areas) > 0 {
		return areas[0]
	}
	return "код"
}

// adoptOrphanSessions пристраивает сессии, чей потребитель пропал. Такая
// сессия невидима для обходов: её не закрыть, не свернуть, не превратить
// в кристалл — и вся её сводка пропадает молча. Ищем живого потребителя
// того же агента; не нашли — закрываем и помечаем, чтобы не висела.
func (e *Engine) adoptOrphanSessions() int {
	list, err := e.store.OrphanSessions()
	if err != nil || len(list) == 0 {
		return 0
	}
	consumers, err := e.store.MemoryConsumers()
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range list {
		name := s.Agent
		if base, _, ok := strings.Cut(name, " · "); ok {
			name = base // «Claude Code · …/sema» → «Claude Code»
		}
		var to *store.MemoryConsumer
		for _, c := range consumers {
			if c.Name == name {
				to = c
				break
			}
		}
		if to == nil {
			if s.Ended == "" {
				_ = e.store.EndMemorySession(s.ID, s.Summary)
			}
			log.Printf("память: сессия %d осталась без потребителя «%s» — закрыта", s.ID, s.Agent)
			continue
		}
		if err := e.store.MoveSession(s.ID, to.ID); err != nil {
			continue
		}
		log.Printf("память: сессия %d переприкреплена к потребителю «%s»", s.ID, to.Name)
		n++
	}
	return n
}
