package engine

// Память в чате: блок из памяти идёт раньше писем и чеков; если память
// ответила, письма не собираются заново — и разница считается в журнале.
// Запомнить что-то модель просит блоком в ответе, а сохраняется это по
// режиму области: сразу, после подтверждения или никогда.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// rememberBlock — «запомни это в область»:
//
//	```sema-remember:финансы
//	Счёт за воду приходит дважды — считать один раз
//	граф: квартира на Садовой | вода | л/с XXXX
//	```
var rememberBlock = regexp.MustCompile("(?s)```sema-remember:([^\n]*)\n(.*?)```")

// areaBlock — предложение завести область: ```sema-area:дача\nпочему```
var areaBlock = regexp.MustCompile("(?s)```sema-area:([^\n]*)\n(.*?)```")

// MemoryCard — карточка памяти в переписке.
type MemoryCard struct {
	ID     int64  `json:"id,omitempty"`
	Area   string `json:"area"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // saved | pending | rejected | suggest
	Note   string `json:"note,omitempty"`
}

func chatSessionKey(chatID int64) string { return fmt.Sprintf("memory.chat_session.%d", chatID) }

// chatOrigin — происхождение записей из чата: потребитель «Сёма-чат» и
// открытая сессия этого чата.
func (e *Engine) chatOrigin(chatID int64) Origin {
	c := e.ChatConsumer()
	title := "чат"
	if ch, err := e.store.Chat(chatID); err == nil && ch.Title != "" {
		title = "чат «" + ch.Title + "»"
	}
	var sid int64
	if _, err := fmt.Sscanf(e.store.Setting(chatSessionKey(chatID), ""), "%d", &sid); err == nil && sid > 0 {
		if s, err := e.store.MemorySession(sid); err == nil && s.Ended == "" {
			return Origin{Consumer: c, SessionID: sid, Agent: title}
		}
	}
	model, _ := e.ChatSettings(chatID)
	sid, err := e.store.StartMemorySession(c.ID, title, model)
	if err == nil {
		_ = e.store.SetSetting(chatSessionKey(chatID), fmt.Sprint(sid))
	}
	return Origin{Consumer: c, SessionID: sid, Agent: title}
}

// memoryContext — блок памяти для промпта и решение, нужен ли ещё поиск по
// письмам: если память ответила кристаллом или фактами, письма не собираем.
func (e *Engine) memoryContext(ctx context.Context, chatID int64, query, model string) (block string, r Recalled, answered bool) {
	o := e.chatOrigin(chatID)
	r = e.Recall(ctx, o, model, query)
	for _, m := range r.Items {
		if m.Kind == store.MemCrystal || m.Kind == store.MemFact {
			answered = true
		}
	}
	return r.Text, r, answered
}

// memoryPrompt — договор с моделью о блоках памяти. Области перечислены с
// режимами: модель должна знать, что «личное» спросит человека.
func (e *Engine) memoryPrompt(model string) string {
	c := e.ChatConsumer()
	cloud := e.ModelIsCloud(model)
	var areas []string
	for _, name := range c.Areas {
		a, ok := e.store.MemoryArea(name)
		if !ok || a.Forget {
			continue
		}
		mode := map[string]string{store.ConfirmAuto: "saved at once", store.ConfirmAsk: "asks the user", store.ConfirmNever: "human only"}[a.Confirm]
		if cloud && !a.Outbound {
			mode += ", closed for this model"
		}
		areas = append(areas, name+" ("+mode+")")
	}
	if len(areas) == 0 {
		return ""
	}
	return "\nMemory: when the user states something lasting (a fact about their life or setup, a decision, a rule) " +
		"or a conclusion worth keeping was reached, write a block ```sema-remember:<area>\\n<one sentence in Russian>``` " +
		"(optionally add lines `граф: A | relation | B`). Areas: " + strings.Join(areas, "; ") + ". " +
		"If the topic fits no area, propose one with ```sema-area:<short name>\\n<why>```. Never put secrets, passwords or tokens in memory. " +
		"Write these blocks only when it is really worth remembering.\n"
}

// findMemoryBlocks вынимает блоки памяти из ответа: записи сохраняются по
// режиму области, каждая оставляет карточку в переписке.
func (e *Engine) findMemoryBlocks(ctx context.Context, chatID int64, answer string) string {
	o := e.chatOrigin(chatID)
	model, _ := e.ChatSettings(chatID)
	for _, m := range rememberBlock.FindAllStringSubmatch(answer, 4) {
		area := strings.TrimSpace(strings.ToLower(m[1]))
		text, graph := splitRememberBody(m[2])
		if text == "" {
			continue
		}
		card := ToolCard{Title: "Запомнить · " + area, Code: text, Memory: &MemoryCard{Area: area, Kind: store.MemFact}}
		rec, err := e.Remember(ctx, o, MemoryRecord{Kind: store.MemFact, Area: area, Text: text, Graph: graph, Model: model})
		switch {
		case err != nil:
			card.Memory.Status = "rejected"
			card.Result = err.Error()
		case rec.Status == store.MemPending:
			card.Memory.Status, card.Memory.ID = "pending", rec.ID
			card.Result = "область в режиме «спросить» — сохраню после вашего решения"
		default:
			card.Memory.Status, card.Memory.ID, card.Memory.Kind = "saved", rec.ID, rec.Kind
			card.Result = "записано · " + o.Agent
			if rec.Weight > 1 {
				card.Result = fmt.Sprintf("уже знал — повтор %d, %s", rec.Weight, map[bool]string{true: "теперь урок", false: "вес вырос"}[rec.Kind == store.MemLesson])
			}
		}
		_ = e.saveToolCard(chatID, card)
	}
	answer = strings.TrimSpace(rememberBlock.ReplaceAllString(answer, ""))
	for _, m := range areaBlock.FindAllStringSubmatch(answer, 2) {
		name := strings.TrimSpace(strings.ToLower(m[1]))
		why := strings.TrimSpace(m[2])
		if name == "" {
			continue
		}
		e.SuggestArea(name, why)
		_ = e.saveToolCard(chatID, ToolCard{Title: "Завести область «" + name + "»?", Code: why,
			Result: "до вашего решения записи по этой теме не сохраняются", Memory: &MemoryCard{Area: name, Status: "suggest"}})
	}
	return strings.TrimSpace(areaBlock.ReplaceAllString(answer, ""))
}

// splitRememberBody отделяет текст записи от строк «граф: A | связь | B».
func splitRememberBody(body string) (text string, graph [][3]string) {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		l := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(strings.ToLower(l), "граф:"); ok {
			parts := strings.Split(rest, "|")
			if len(parts) == 3 {
				graph = append(graph, [3]string{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])})
			}
			continue
		}
		if l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, " "), graph
}

// AcceptMemoryCard — человек нажал «Запомнить» на карточке ожидающей записи,
// возможно переложив её в другую область.
func (e *Engine) AcceptMemoryCard(ctx context.Context, id int64, area string) error {
	m, err := e.store.MemoryByID(id)
	if err != nil {
		return err
	}
	if area != "" && area != m.Area {
		if _, ok := e.store.MemoryArea(area); !ok {
			return fmt.Errorf("области %q нет", area)
		}
		m.Area = area
		if err := e.store.UpdateMemory(m); err != nil {
			return err
		}
	}
	return e.ConfirmMemory(ctx, id, "")
}
