package engine

// Сведение памяти — задача сердцебиения. Раз в час: удалить истёкшее,
// досчитать векторы, закрыть замолчавшие сессии чата, сделать кристаллы
// из завершённых сессий, извлечь граф из новых записей, слить дубли от
// разных агентов. Всё, что требует модели, идёт через роль «Память»; без
// модели остаются только сроки и векторы.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const WorkerMemory = "memory"

// memoryPass — один проход сведения. Возвращает строку для журнала задачи.
func (e *Engine) memoryPass(ctx context.Context) string {
	var parts []string
	stage := time.Now()
	step := func(name string) {
		if d := time.Since(stage); d > 5*time.Second {
			log.Printf("память: шаг %s занял %.0f с", name, d.Seconds())
		}
		stage = time.Now()
	}
	defer func() { step("хвост") }()
	_ = e.store.SkipEmptySessions()
	step("пустые сессии")
	if n := e.dropExpiredMemories(); n > 0 {
		parts = append(parts, fmt.Sprintf("истекло %d", n))
	}
	step("сроки")
	// координаты записей сверяет житель машины: символ мог уехать или
	// исчезнуть, а знание при этом остаётся верным
	e.verifyPlacesWithCode(ctx)
	e.parseDoubtfulPlaces()
	step("места в коде")
	if n := e.embedMissing(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("векторов %d", n))
	}
	step("векторы")
	if n := e.closeIdleChatSessions(); n > 0 {
		parts = append(parts, fmt.Sprintf("сессий чата закрыто %d", n))
	}
	step("сессии чатов")
	// сессия без потребителя невидима для всех обходов: пристраиваем
	if n := e.adoptOrphanSessions(); n > 0 {
		parts = append(parts, fmt.Sprintf("сессий пристроено %d", n))
	}
	step("осиротевшие сессии")
	// сессия чата, которого больше нет, не закроется обходом чатов и
	// висит вечно: закрываем по тишине так же, как сессии агентов
	if n := e.closeOrphanChatSessions(); n > 0 {
		parts = append(parts, fmt.Sprintf("сессий пропавших чатов закрыто %d", n))
	}
	step("сессии пропавших чатов")
	// сессия агента закрывается по тишине: хук закрытия приходит не
	// всегда (агент упал, окно закрыли иначе, кончился лимит), а
	// несведённая сводка — потерянное знание
	idleFor := time.Duration(e.settingInt("memory.session_idle_h", 2)) * time.Hour
	if idle, err := e.store.IdleOpenSessions(time.Now().Add(-idleFor).Format(time.RFC3339)); err == nil && len(idle) > 0 {
		for _, s := range idle {
			_ = e.store.EndMemorySession(s.ID, s.Summary)
			log.Printf("память: сессия %d (%s) замолчала — закрыта", s.ID, s.Agent)
		}
		parts = append(parts, fmt.Sprintf("сессий агентов закрыто %d", len(idle)))
	}
	step("сессии агентов")
	if !e.MemoryModelSet() {
		parts = append(parts, "роль «Память» без модели: без сжатия и графа")
		_ = e.store.SetSetting(memLastPass, time.Now().Format(time.RFC3339))
		return strings.Join(parts, " · ")
	}
	step("до кристаллов")
	if n := e.crystallize(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("кристаллов %d", n))
	}
	step("кристаллы")
	if n := e.extractGraph(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("граф из %d записей", n))
	}
	step("граф")
	if n := e.mergeDuplicates(); n > 0 {
		parts = append(parts, fmt.Sprintf("слито дублей %d", n))
	}
	_ = e.store.SetSetting(memLastPass, time.Now().Format(time.RFC3339))
	if len(parts) == 0 {
		return "нечего сводить"
	}
	return strings.Join(parts, " · ")
}

// RunMemoryPassNow — «Свести сейчас» с экрана.
func (e *Engine) RunMemoryPassNow() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	log.Printf("память: сведение по запросу — %s", e.memoryPass(ctx))
}

// MemoryModelSet — назначена ли модель роли «Память».
func (e *Engine) MemoryModelSet() bool {
	m := e.Role(RoleMemory).Model
	return m != "" && m != roleNoneName && e.provider(m) != nil
}

// MemoryLastPass — когда сведение шло последний раз.
func (e *Engine) MemoryLastPass() string { return e.store.Setting(memLastPass, "") }

func (e *Engine) dropExpiredMemories() int {
	list, err := e.store.ExpiredMemories()
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range list {
		if e.store.DeleteMemory(m.ID) == nil {
			n++
		}
	}
	return n
}

func (e *Engine) embedMissing(ctx context.Context) int {
	if e.embedder == nil {
		return 0
	}
	list, err := e.store.MemoriesWithoutVector(50)
	if err != nil {
		return 0
	}
	for _, m := range list {
		e.embedMemory(ctx, m)
	}
	return len(list)
}

// closeIdleChatSessions завершает сессии чатов, в которых давно тишина:
// сводка — хвост разговора, из неё потом родится кристалл.
func (e *Engine) closeIdleChatSessions() int {
	n := 0
	chats, err := e.store.Chats()
	if err != nil {
		return 0
	}
	for _, c := range chats {
		key := chatSessionKey(c.ID)
		var sid int64
		if _, err := fmt.Sscanf(e.store.Setting(key, ""), "%d", &sid); err != nil || sid == 0 {
			continue
		}
		msgs, err := e.store.ChatMsgs(c.ID, 24)
		if err != nil || len(msgs) == 0 {
			continue
		}
		if time.Since(msgs[len(msgs)-1].At) < 90*time.Minute {
			continue
		}
		if e.endChatSession(c.ID) {
			n++
		}
	}
	return n
}

// endChatSession закрывает сессию памяти чата сводкой из хвоста разговора:
// из неё потом родится кристалл. Сессии ещё не было — заводим её тут же:
// разговор не должен пропасть вместе с чатом.
func (e *Engine) endChatSession(chatID int64) bool {
	key := chatSessionKey(chatID)
	var sid int64
	if _, err := fmt.Sscanf(e.store.Setting(key, ""), "%d", &sid); err != nil || sid == 0 {
		o := e.chatOrigin(chatID)
		if o.Consumer == nil {
			return false
		}
		id, err := e.store.StartMemorySession(o.Consumer.ID, o.Agent, e.Role(RoleChat).Model)
		if err != nil {
			log.Printf("память: сессия для чата %d не завелась: %v", chatID, err)
			return false
		}
		sid = id
	}
	msgs, err := e.store.ChatMsgs(chatID, 24)
	if err != nil || len(msgs) == 0 {
		return false
	}
	var b strings.Builder
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		role := "User"
		if m.Role == "assistant" {
			role = "Sema"
		}
		fmt.Fprintf(&b, "%s: %s\n", role, clip(m.Content, 600))
	}
	_ = e.store.EndMemorySession(sid, clip(b.String(), 6000))
	_ = e.store.SetSetting(key, "")
	return true
}

// crystalize делает из завершённых сессий кристаллы, факты, уроки и граф —
// одним обращением к модели на сессию.
// crystalWaitsTrust — отметка сессии, чей кристалл ждёт доверенной модели.
const crystalWaitsTrust = -2

func (e *Engine) crystallize(ctx context.Context) int {
	// сессии, отложенные «до доверенной модели», возвращаются в очередь,
	// когда такая модель появилась
	if e.homeModel() != "" {
		_ = e.store.ResumeSessionsWaitingTrust()
	}
	sessions, err := e.store.SessionsWithoutCrystal(5)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range sessions {
		c, err := e.store.MemoryConsumerByID(s.ConsumerID)
		if err != nil || len(c.Areas) == 0 {
			_ = e.store.SetSessionCrystal(s.ID, -1) // некому выдать — пропускаем навсегда
			continue
		}
		var areas []string
		for _, a := range c.Areas {
			if ar, ok := e.store.MemoryArea(a); ok && !ar.Forget {
				areas = append(areas, a)
			}
		}
		prompt := "You distill an agent session into durable memory. Reply with JSON only:\n" +
			`{"area": "<one of areas>", "crystal": "<60-150 tokens, Russian, what was done and concluded>", ` +
			`"facts": [{"area": "<area>", "text": "<lasting fact, Russian, one sentence>"}], ` +
			`"lessons": [{"area": "<area>", "text": "<rule worth keeping, Russian>"}], ` +
			`"graph": [["<entity>", "<relation>", "<entity>"]]}` + "\n" +
			"Areas available: " + strings.Join(areas, ", ") + ". Keep only what will matter in a later session; " +
			"no secrets, no file contents, no chit-chat. Empty arrays are fine. " +
			"If nothing happened (no mail found, greetings only, connectivity checks, a scheduled task with an empty result) — return crystal as an empty string and empty arrays. " +
			"Lessons are only about THIS person's own setup, habits or systems, never generic advice like «always check» or «close topics».\n\nSession of " + s.Agent + ":\n" + s.Summary
		answer, by, err := e.askMemory(ctx, prompt)
		if err != nil {
			log.Printf("память: кристалл сессии %d: %v", s.ID, err)
			if err == errNoTrusted {
				// секретное без доверенной модели: сессия ждёт, а не держит
				// остальных — иначе одна такая блокировала бы кристаллы навсегда
				_ = e.store.SetSessionCrystal(s.ID, crystalWaitsTrust)
				continue
			}
			return n
		}
		var out struct {
			Area    string      `json:"area"`
			Crystal string      `json:"crystal"`
			Facts   []areaText  `json:"facts"`
			Lessons []areaText  `json:"lessons"`
			Graph   [][3]string `json:"graph"`
		}
		if err := json.Unmarshal([]byte(extractJSON(answer)), &out); err != nil || strings.TrimSpace(out.Crystal) == "" || emptyOutcome(out.Crystal) {
			// пустая сессия — не кристалл: «писем нет» и «связь проверена»
			// в памяти никому не пригодятся
			log.Printf("память: сессия %d без итога — кристалла нет", s.ID)
			_ = e.store.SetSessionCrystal(s.ID, -1)
			continue
		}
		if !hasString(areas, out.Area) {
			out.Area = areas[0]
		}
		origin := Origin{Consumer: c, SessionID: s.ID, Agent: s.Agent}
		cr, err := e.Remember(ctx, origin, MemoryRecord{Kind: store.MemCrystal, Area: out.Area, Text: out.Crystal, Model: by, Graph: out.Graph})
		if err != nil {
			log.Printf("память: кристалл не сохранён: %v", err)
			_ = e.store.SetSessionCrystal(s.ID, -1)
			continue
		}
		_ = e.store.SetSessionCrystal(s.ID, cr.ID)
		for _, f := range out.Facts {
			if emptyOutcome(f.Text) {
				continue
			}
			_, _ = e.Remember(ctx, origin, MemoryRecord{Kind: store.MemFact, Area: pickArea(f.Area, areas), Text: f.Text, Model: by})
		}
		for _, l := range out.Lessons {
			if emptyOutcome(l.Text) || genericLesson(l.Text) {
				continue
			}
			_, _ = e.Remember(ctx, origin, MemoryRecord{Kind: store.MemLesson, Area: pickArea(l.Area, areas), Text: l.Text, Model: by})
		}
		n++
	}
	return n
}

type areaText struct {
	Area string `json:"area"`
	Text string `json:"text"`
}

func pickArea(want string, areas []string) string {
	if hasString(areas, want) {
		return want
	}
	return areas[0]
}

// extractGraph вынимает сущности и связи из записей, у которых графа ещё нет.
func (e *Engine) extractGraph(ctx context.Context) int {
	list, err := e.store.MemoriesNotGraphed(10)
	if err != nil || len(list) == 0 {
		return 0
	}
	types, _ := e.store.MemoryTypes("edge")
	var rels []string
	for _, t := range types {
		rels = append(rels, t[0])
	}
	n := 0
	for _, m := range list {
		prompt := "Extract entities and relations from the note. Reply with JSON only: " +
			`[["<entity>", "<relation>", "<entity>"], ...]` + " — 0 to 6 triples, entity names short and in the note's language, " +
			"relations preferably from: " + strings.Join(rels, ", ") + ". No secrets.\n\nNote: " + m.Text
		answer, _, err := e.askMemory(ctx, prompt)
		if err != nil {
			return n
		}
		var triples [][3]string
		_ = json.Unmarshal([]byte(extractJSON(answer)), &triples)
		e.saveGraph(m, triples)
		if !m.Graphed { // пусто тоже результат: второй раз не спрашиваем
			m.Graphed = true
			_ = e.store.UpdateMemory(m)
		}
		n++
	}
	return n
}

// mergeDuplicates сливает очень близкие записи одной области и вида: одна
// запись с несколькими источниками вместо двух.
func (e *Engine) mergeDuplicates() int {
	vectors, err := e.store.MemoryVectors()
	if err != nil || len(vectors) < 2 {
		return 0
	}
	ids := make([]int64, 0, len(vectors))
	decoded := map[int64][]float32{}
	for id, raw := range vectors {
		ids = append(ids, id)
		decoded[id] = embed.DecodeVec(raw)
	}
	merged := map[int64]bool{}
	n := 0
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := ids[i], ids[j]
			if merged[a] || merged[b] || embed.Cosine(decoded[a], decoded[b]) < 0.93 {
				continue
			}
			ma, err1 := e.store.MemoryByID(a)
			mb, err2 := e.store.MemoryByID(b)
			if err1 != nil || err2 != nil || ma.Area != mb.Area || ma.Kind != mb.Kind ||
				(ma.Kind != store.MemFact && ma.Kind != store.MemLesson) {
				continue
			}
			keep, drop := ma, mb
			if mb.Weight > ma.Weight || (mb.Weight == ma.Weight && mb.Created > ma.Created) {
				keep, drop = mb, ma
			}
			keep.Weight += drop.Weight
			for _, s := range drop.Sources {
				if !hasString(keep.Sources, s) {
					keep.Sources = append(keep.Sources, s)
				}
			}
			keep.Confirmed = keep.Confirmed || drop.Confirmed
			if keep.Kind == store.MemFact && keep.Weight >= e.MemorySetting(memLessonThreshold) {
				keep.Kind = store.MemLesson
				keep.Expires = ""
			}
			if err := e.store.UpdateMemory(keep); err != nil {
				continue
			}
			// координаты дубля переезжают к оставшейся записи: иначе
			// слияние молча стирает знание о месте в коде
			_ = e.store.MovePlaces(drop.ID, keep.ID)
			_ = e.store.MarkReplaced(drop.ID, keep.ID)
			merged[drop.ID] = true
			n++
		}
	}
	return n
}

// extractJSON достаёт первый JSON-объект или массив из ответа модели:
// вокруг бывают пояснения и ограждения кода.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = fenceRe.ReplaceAllString(s, "")
	for _, open := range []string{"{", "["} {
		if head, _ := splitBalanced(s, open); head != "" {
			return head
		}
	}
	return s
}

var fenceRe = regexp.MustCompile("(?s)```[a-z]*\n?|```")

// splitBalanced — как splitManifest, но и для массивов.
func splitBalanced(raw, open string) (head, rest string) {
	if open == "{" {
		return splitManifest(raw)
	}
	start := strings.Index(raw, "[")
	if start < 0 {
		return "", raw
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth == 0 {
				return raw[start : i+1], raw[i+1:]
			}
		}
	}
	return "", raw
}

// closeOrphanChatSessions закрывает сессии чатов, которых уже нет.
// Обход чатов такую сессию не видит — чата нет, — и она висит открытой
// навсегда, а вместе с ней и её сводка.
func (e *Engine) closeOrphanChatSessions() int {
	idleFor := time.Duration(e.settingInt("memory.session_idle_h", 2)) * time.Hour
	before := time.Now().Add(-idleFor).Format(time.RFC3339)
	list, err := e.store.IdleOpenChatSessions(before)
	if err != nil {
		return 0
	}
	alive := map[int64]bool{}
	if chats, err := e.store.Chats(); err == nil {
		for _, c := range chats {
			var sid int64
			if _, err := fmt.Sscanf(e.store.Setting(chatSessionKey(c.ID), ""), "%d", &sid); err == nil {
				alive[sid] = true
			}
		}
	}
	n := 0
	for _, s := range list {
		if alive[s.ID] {
			continue // живой чат: его закроет обход чатов со сводкой
		}
		if err := e.store.EndMemorySession(s.ID, s.Summary); err == nil {
			log.Printf("память: сессия %d осталась без чата — закрыта", s.ID)
			n++
		}
	}
	return n
}
