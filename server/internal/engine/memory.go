package engine

// Память Сёмы: одна на чат, модули и внешних агентов. Запись проходит
// guardrails на входе (маски секретов, режим области, размер), выдача —
// guardrails на выходе (выданные области, «наружу» для облачной модели,
// лимиты на промпт). Всё, что можно настроить, лежит в базе; здесь только
// первичное заполнение.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const RoleMemory = "Память"

// Ключи настроек памяти: пороги и лимиты, правятся на экране.
const (
	memLimitLessons    = "memory.limit_lessons"
	memLimitFacts      = "memory.limit_facts"
	memLimitEpisodes   = "memory.limit_episodes"
	memLessonThreshold = "memory.lesson_threshold"
	memObserveTTL      = "memory.observe_ttl_days"
	memGraphDepth      = "memory.graph_depth"
	memMaxChars        = "memory.max_chars"
	memSuggestions     = "memory.suggestions.json"
	memLastPass        = "memory.last_pass"
	// как часто сверять координату записи с кодом: символ переживает
	// переносы, но не переименование, а спрашивать жителя машины о каждой
	// привязке каждый проход незачем
	memPlaceCheckDays = "code.place_check_days"
)

// MemorySetting — числовая настройка памяти со значением по умолчанию.
func (e *Engine) MemorySetting(key string) int {
	def := map[string]int{
		memLimitLessons: 10, memLimitFacts: 8, memLimitEpisodes: 3,
		memLessonThreshold: 2, memObserveTTL: 7, memGraphDepth: 2, memMaxChars: 1500,
	}[key]
	var v int
	if _, err := fmt.Sscanf(e.store.Setting(key, ""), "%d", &v); err == nil && v > 0 {
		return v
	}
	return def
}

func (e *Engine) SetMemorySetting(key string, v int) error {
	return e.store.SetSetting(key, fmt.Sprint(v))
}

// ensureMemorySeed — первичное заполнение справочников: области, маски
// секретов, типы узлов и связей, потребитель «Сёма-чат» со всеми областями.
func (e *Engine) ensureMemorySeed() {
	areas, _ := e.store.MemoryAreas()
	if len(areas) == 0 {
		for _, a := range []*store.MemoryArea{
			{Name: "личное", Title: "Личное", Confirm: store.ConfirmAsk, TTLDays: 0},
			{Name: "финансы", Title: "Финансы", Confirm: store.ConfirmAuto, TTLDays: 90},
			{Name: "код", Title: "Код", Confirm: store.ConfirmAuto, TTLDays: 180},
			{Name: "серверы", Title: "Серверы", Confirm: store.ConfirmAuto, TTLDays: 30},
		} {
			_ = e.store.SaveMemoryArea(a)
		}
		areas, _ = e.store.MemoryAreas()
	}
	if masks, _ := e.store.MemoryRules("mask"); len(masks) == 0 {
		for _, m := range []string{"enc:v1:", "sk-", "glpat-", "ghp_", "xoxb-", "-----BEGIN", "password=", "пароль=", "Bearer "} {
			_ = e.store.AddMemoryRule("mask", m, "первичный набор")
		}
	}
	if types, _ := e.store.MemoryTypes("node"); len(types) == 0 {
		for _, t := range [][2]string{{"сущность", "Сущность"}, {"человек", "Человек"}, {"организация", "Организация"},
			{"поставщик", "Поставщик"}, {"счёт", "Счёт"}, {"документ", "Документ"}, {"сумма", "Сумма"},
			{"место", "Место"}, {"сервер", "Сервер"}, {"проект", "Проект"}, {"решение", "Решение"}, {"правило", "Правило"}} {
			_ = e.store.SaveMemoryType("node", t[0], t[1])
		}
		for _, t := range [][2]string{{"относится", "относится к"}, {"поставщик", "поставщик"}, {"счёт", "счёт"},
			{"документ", "документ"}, {"часть", "часть"}, {"зависит", "зависит от"}, {"решение", "решение"}, {"правило", "правило"}} {
			_ = e.store.SaveMemoryType("edge", t[0], t[1])
		}
	}
	// чат Сёмы — потребитель со всеми областями: это ассистент самого человека
	if c, err := e.store.MemoryConsumer(store.ConsumerChat, "Сёма", ""); err == nil && len(c.Areas) == 0 {
		var names []string
		for _, a := range areas {
			names = append(names, a.Name)
		}
		_ = e.store.SetConsumerAreas(c.ID, names)
	}
}

// ChatConsumer — потребитель «Сёма-чат».
func (e *Engine) ChatConsumer() *store.MemoryConsumer {
	c, err := e.store.MemoryConsumer(store.ConsumerChat, "Сёма", "")
	if err != nil {
		return &store.MemoryConsumer{Kind: store.ConsumerChat, Name: "Сёма"}
	}
	return c
}

// MemoryRecord — что просят запомнить.
type MemoryRecord struct {
	Kind string
	Area string
	Text string
	// Место — к какому куску кода относится запись. Необязательно: память
	// живёт и без привязки. Нужна там, где знание про конкретное место —
	// грабли, уроки из работ, требования спеки. Символ важнее строки:
	// строки съезжают от любой правки, имя функции переживает переносы.
	Place     *store.Place
	Confirmed bool        // подтверждено человеком: не ждёт режима «спросить»
	Graph     [][3]string // связи «узел — отношение — узел», извлечённые вместе с записью
	Model     string      // какой моделью извлечено (пусто — человек или код)
}

// Origin — происхождение записи: кто и в какой сессии.
type Origin struct {
	Consumer  *store.MemoryConsumer
	SessionID int64
	Agent     string // как назвать источник человеку: «чат «Коммуналка»», «задача «Проверка дисков»»
}

// errMemoryDenied — один и тот же отказ для невыданной и несуществующей
// области: потребитель не должен узнавать, какие области есть.
func errMemoryDenied(area string) error {
	return fmt.Errorf("область памяти %q не выдана — выдать можно на экране «Память»", area)
}

// Remember проводит запись через guardrails на входе и сохраняет её.
func (e *Engine) Remember(ctx context.Context, o Origin, rec MemoryRecord) (*store.Memory, error) {
	rec.Text = strings.TrimSpace(rec.Text)
	rec.Area = strings.TrimSpace(rec.Area)
	if rec.Kind == "" {
		rec.Kind = store.MemFact
	}
	logDeny := func(action, detail string) {
		e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: o.SessionID,
			Area: rec.Area, Action: action, Detail: detail, Granted: false})
	}
	area, ok := e.store.MemoryArea(rec.Area)
	if !ok || !o.Consumer.Has(rec.Area) {
		logDeny("deny", "запись: область не выдана")
		return nil, errMemoryDenied(rec.Area)
	}
	if area.Forget {
		logDeny("reject", "область «не запоминать»")
		return nil, fmt.Errorf("область %q помечена «не запоминать»", rec.Area)
	}
	if rec.Text == "" {
		return nil, fmt.Errorf("пустая запись")
	}
	if mask := e.secretMask(rec.Text); mask != "" {
		logDeny("reject", "маска секрета "+mask+" · значение не сохранено")
		return nil, fmt.Errorf("запись похожа на секрет (%s) и не сохраняется", mask)
	}
	if n := len([]rune(rec.Text)); n > e.MemorySetting(memMaxChars) {
		logDeny("reject", fmt.Sprintf("запись длиннее %d символов", e.MemorySetting(memMaxChars)))
		return nil, fmt.Errorf("запись слишком длинная: %d символов, предел %d", n, e.MemorySetting(memMaxChars))
	}
	if area.Confirm == store.ConfirmNever && !rec.Confirmed {
		logDeny("reject", "область в режиме «никогда»")
		return nil, fmt.Errorf("область %q не принимает записи без человека", rec.Area)
	}

	// повтор уже известного — не дубль, а подтверждение: вес растёт, факт
	// может стать уроком
	if rec.Kind == store.MemFact || rec.Kind == store.MemLesson {
		if same := e.sameMemory(ctx, rec.Area, rec.Text); same != nil {
			return e.reinforce(o, same, rec)
		}
	}

	m := &store.Memory{
		Kind: rec.Kind, Area: rec.Area, Text: rec.Text, SessionID: o.SessionID,
		Agent: o.Agent, Model: rec.Model, Confirmed: rec.Confirmed, Sources: []string{o.Agent},
	}
	if area.Confirm == store.ConfirmAsk && !rec.Confirmed {
		m.Status = store.MemPending
	}
	switch {
	case rec.Kind == store.MemObservation:
		m.Expires = time.Now().AddDate(0, 0, e.MemorySetting(memObserveTTL)).Format(time.RFC3339)
	case area.TTLDays > 0 && !rec.Confirmed && rec.Kind != store.MemLesson:
		m.Expires = time.Now().AddDate(0, 0, area.TTLDays).Format(time.RFC3339)
	}
	id, err := e.store.SaveMemory(m)
	if err != nil {
		return nil, err
	}
	// координата ставится после сохранения: без записи привязывать не к чему
	if rec.Place != nil {
		place := *rec.Place
		place.MemoryID = id
		if err := e.store.BindToPlace(place); err != nil {
			log.Printf("память: привязка записи %d к месту не вышла: %v", id, err)
		}
	}
	if m.Status == store.MemOK {
		e.embedMemory(ctx, m)
		e.saveGraph(m, rec.Graph)
		if !m.Confirmed {
			e.EnqueueMemoryReview(m.ID, 4) // разбор — в очередь, а не сканом
		}
	}
	e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: o.SessionID, Area: rec.Area,
		Action: "remember", Detail: rec.Kind + ": " + clip(rec.Text, 120) + statusNote(m.Status), Granted: true, Tokens: tokensOf(rec.Text)})
	_ = id
	return m, nil
}

func statusNote(status string) string {
	if status == store.MemPending {
		return " · ждёт подтверждения"
	}
	return ""
}

// reinforce — повтор известного факта: вес растёт, источник добавляется,
// по порогу факт становится уроком.
func (e *Engine) reinforce(o Origin, m *store.Memory, rec MemoryRecord) (*store.Memory, error) {
	m.Weight++
	if !hasString(m.Sources, o.Agent) {
		m.Sources = append(m.Sources, o.Agent)
	}
	if rec.Confirmed {
		m.Confirmed = true
		m.Expires = ""
	}
	note := "повтор: вес " + fmt.Sprint(m.Weight)
	if m.Kind == store.MemFact && (m.Weight >= e.MemorySetting(memLessonThreshold) || m.Confirmed) {
		m.Kind = store.MemLesson
		m.Expires = ""
		note += " → урок"
	}
	if err := e.store.UpdateMemory(m); err != nil {
		return nil, err
	}
	e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: o.SessionID, Area: m.Area,
		Action: "remember", Detail: note + ": " + clip(m.Text, 100), Granted: true})
	return m, nil
}

// sameMemory ищет ту же мысль в области: совпадение по нормализованному
// тексту или очень близкий вектор.
func (e *Engine) sameMemory(ctx context.Context, area, text string) *store.Memory {
	norm := normalizeText(text)
	list, _ := e.store.Memories(area, "", 500)
	for _, m := range list {
		if m.Status == store.MemOK && (m.Kind == store.MemFact || m.Kind == store.MemLesson) && normalizeText(m.Text) == norm {
			return m
		}
	}
	if e.embedder == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	vec, err := e.embedIfAllowed(cctx, text)
	if err != nil || vec == nil {
		return nil // проверка не пустила — искать по вектору нечем
	}
	vectors, _ := e.store.MemoryVectors()
	for _, m := range list {
		if raw, ok := vectors[m.ID]; ok && (m.Kind == store.MemFact || m.Kind == store.MemLesson) &&
			embed.Cosine(vec, embed.DecodeVec(raw)) >= 0.93 {
			return m
		}
	}
	return nil
}

func normalizeText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r == '.' || r == ',' || r == '—' || r == '-' || r == ':' || r == ';' || r == '«' || r == '»' || r == '"' {
			return ' '
		}
		return r
	}, s)), " ")
}

func hasString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// secretMask — какая маска сработала (пусто — никакая).
func (e *Engine) secretMask(text string) string {
	masks, _ := e.store.MemoryRules("mask")
	for _, m := range masks {
		if maskHits(text, m) {
			return m
		}
	}
	return ""
}

// maskHits — встречается ли маска в тексте. Маска, начинающаяся с буквы
// или цифры, — приставка токена («sk-», «ghp_», «Bearer ») и сравнивается
// с начала слова: «sk-» внутри «--disk-usage» не секрет, а ключ
// «sk-abc…» после пробела, кавычки или в начале строки — секрет. Иначе
// маска из памяти закрывала бы любой текст со случайным совпадением
// внутри слова, и встроенный скилл «Linux-сервер» не правился вовсе.
// Маска со знака («enc:v1:», «-----BEGIN») ищется как есть.
func maskHits(text, mask string) bool {
	if mask == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(mask)
	if !unicode.IsLetter(first) && !unicode.IsDigit(first) {
		return strings.Contains(text, mask)
	}
	from := 0
	for {
		i := strings.Index(text[from:], mask)
		if i < 0 {
			return false
		}
		at := from + i
		if at == 0 {
			return true
		}
		prev, _ := utf8.DecodeLastRuneInString(text[:at])
		if !unicode.IsLetter(prev) && !unicode.IsDigit(prev) {
			return true
		}
		from = at + len(mask)
	}
}

// embedMemory считает вектор записи; без модели эмбеддингов запись живёт
// только в словарном поиске.
func (e *Engine) embedMemory(ctx context.Context, m *store.Memory) {
	if e.embedder == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if vec, err := e.embedIfAllowed(cctx, m.Text); err == nil && vec != nil {
		_ = e.store.SaveMemoryVector(m.ID, embed.EncodeVec(vec))
	}
}

// saveGraph кладёт связи записи в граф. Узел получает область записи.
func (e *Engine) saveGraph(m *store.Memory, edges [][3]string) {
	for _, tr := range edges {
		a, rel, b := strings.TrimSpace(tr[0]), strings.TrimSpace(tr[1]), strings.TrimSpace(tr[2])
		if a == "" || b == "" {
			continue
		}
		if rel == "" {
			rel = "относится"
		}
		from, err := e.store.EnsureMemoryNode("сущность", a, m.Area)
		if err != nil {
			continue
		}
		to, err := e.store.EnsureMemoryNode("сущность", b, m.Area)
		if err != nil {
			continue
		}
		_ = e.store.AddMemoryEdge(from, to, rel, m.ID)
	}
	if len(edges) > 0 {
		m.Graphed = true
		_ = e.store.UpdateMemory(m)
	}
}

// ConfirmMemory — человек подтвердил ожидающую запись (или исправил её).
func (e *Engine) ConfirmMemory(ctx context.Context, id int64, text string) error {
	m, err := e.store.MemoryByID(id)
	if err != nil {
		return err
	}
	if text = strings.TrimSpace(text); text != "" && text != m.Text {
		// исправление — новая версия с источником «человек»
		nm := &store.Memory{Kind: m.Kind, Area: m.Area, Text: text, Agent: "человек", Confirmed: true,
			Supersedes: m.ID, Sources: []string{"человек"}, Weight: m.Weight}
		// места читаются до удаления: удаление записи уносит и привязки
		place := e.store.MemoryPlaces(m.ID)
		if m.Status == store.MemPending {
			_ = e.store.DeleteMemory(m.ID)
			nm.Supersedes = 0
		}
		if _, err := e.store.SaveMemory(nm); err != nil {
			return err
		}
		// исправленная человеком запись остаётся про то же место в коде
		for _, pl := range place {
			pl.MemoryID = nm.ID
			_ = e.store.BindToPlace(pl)
		}
		e.embedMemory(ctx, nm)
		e.store.LogMemory(store.MemoryLogEntry{Consumer: "человек", Area: m.Area, Action: "remember",
			Detail: "исправлено: " + clip(text, 100), Granted: true})
		return nil
	}
	m.Status, m.Confirmed, m.Expires = store.MemOK, true, ""
	if err := e.store.UpdateMemory(m); err != nil {
		return err
	}
	e.embedMemory(ctx, m)
	e.store.LogMemory(store.MemoryLogEntry{Consumer: "человек", Area: m.Area, Action: "remember",
		Detail: "подтверждено: " + clip(m.Text, 100), Granted: true})
	return nil
}

// PromoteMemory закрепляет запись уроком.
func (e *Engine) PromoteMemory(id int64) error {
	m, err := e.store.MemoryByID(id)
	if err != nil {
		return err
	}
	m.Kind, m.Confirmed, m.Expires, m.Status = store.MemLesson, true, "", store.MemOK
	return e.store.UpdateMemory(m)
}

// ForgetMemory удаляет запись: человек решил, что помнить это не нужно.
// DecideAllPending — все ждущие записи разом: принять или забыть. Тринадцать
// кнопок по одной — это не решение, а работа.
func (e *Engine) DecideAllPending(ctx context.Context, accept bool) (int, error) {
	list, err := e.store.PendingMemories()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range list {
		if accept {
			err = e.ConfirmMemory(ctx, m.ID, "")
		} else {
			err = e.ForgetMemory(m.ID)
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (e *Engine) ForgetMemory(id int64) error {
	m, err := e.store.MemoryByID(id)
	if err != nil {
		return err
	}
	e.store.LogMemory(store.MemoryLogEntry{Consumer: "человек", Area: m.Area, Action: "forget",
		Detail: clip(m.Text, 100), Granted: true})
	return e.store.DeleteMemory(id)
}

// ---- выдача ----

// Recalled — что память отдала на вопрос.
type Recalled struct {
	Items  []*store.Memory
	Graph  []string // строки «A —связь→ B»
	Closed []string // области, закрытые для этой модели
	Text   string   // готовый блок для промпта
	Tokens int
}

// Recall проводит поиск через guardrails на выходе: выданные области,
// «наружу» для облачной модели, лимиты по видам.
func (e *Engine) Recall(ctx context.Context, o Origin, model, query string) Recalled {
	var out Recalled
	cloud := e.ModelIsCloud(model)
	var allowed []string
	for _, name := range o.Consumer.Areas {
		a, ok := e.store.MemoryArea(name)
		if !ok {
			continue
		}
		if cloud && !a.Outbound {
			out.Closed = append(out.Closed, name)
			continue
		}
		allowed = append(allowed, name)
	}
	if len(allowed) == 0 {
		if len(o.Consumer.Areas) == 0 {
			e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: o.SessionID,
				Action: "deny", Detail: "вспомнить: области не выданы", Granted: false})
		}
		return out
	}
	allow := map[string]bool{}
	for _, a := range allowed {
		allow[a] = true
	}

	score := map[int64]float64{}
	byID := map[int64]*store.Memory{}
	secretCut := 0
	take := func(m *store.Memory, s float64) {
		if m == nil || !allow[m.Area] || m.Status != store.MemOK {
			return
		}
		// область может быть открыта наружу, но отдельная запись — нет:
		// паспорт и медицина не уходят облачной модели ни из какой области
		if cloud && sentClass(m.Text) == store.ClassSecret {
			secretCut++
			return
		}
		byID[m.ID] = m
		score[m.ID] += s
	}
	defer func() {
		if secretCut > 0 {
			e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: o.SessionID,
				Action: "deny", Granted: false,
				Detail: fmt.Sprintf("вспомнить: %d записей с личным не отданы облачной модели", secretCut)})
		}
	}()
	// уроки области — всегда кандидаты, они короткие и старшие
	for _, a := range allowed {
		lessons, _ := e.store.Memories(a, store.MemLesson, e.MemorySetting(memLimitLessons)*2)
		for _, m := range lessons {
			take(m, 0.3+float64(m.Weight)*0.02)
		}
	}
	// слова
	if hits, ranks, _ := e.store.MemoriesByFTS(query, 30); len(hits) > 0 {
		best := ranks[0]
		for i, m := range hits {
			r := 1.0
			if best != 0 {
				r = ranks[i] / best // bm25 отрицательный: ближе к best — лучше
			}
			take(m, 0.4*r)
		}
	}
	// вектор
	if e.embedder != nil {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		vec, err := e.embedIfAllowed(cctx, query)
		cancel()
		if err == nil && vec != nil {
			vectors, _ := e.store.MemoryVectors()
			type cand struct {
				id  int64
				cos float64
			}
			var cands []cand
			for id, raw := range vectors {
				if c := embed.Cosine(vec, embed.DecodeVec(raw)); c > 0.45 {
					cands = append(cands, cand{id, c})
				}
			}
			sort.Slice(cands, func(i, j int) bool { return cands[i].cos > cands[j].cos })
			for i, c := range cands {
				if i >= 30 {
					break
				}
				m := byID[c.id]
				if m == nil {
					m, _ = e.store.MemoryByID(c.id)
				}
				take(m, 0.5*c.cos)
			}
		}
	}
	// граф вокруг сущностей, названных в вопросе
	if nodes, _ := e.store.FindMemoryNodes(query); len(nodes) > 0 {
		var ids []int64
		for _, n := range nodes {
			ids = append(ids, n.ID)
		}
		lines, memIDs := e.graphAround(ids, e.MemorySetting(memGraphDepth), allow)
		out.Graph = lines
		for _, id := range memIDs {
			if m, err := e.store.MemoryByID(id); err == nil {
				take(m, 0.2)
			}
		}
	}

	// отбор по видам в пределах лимитов
	var lessons, facts, episodes []*store.Memory
	for id, m := range byID {
		_ = id
		switch m.Kind {
		case store.MemLesson:
			lessons = append(lessons, m)
		case store.MemFact:
			facts = append(facts, m)
		default:
			episodes = append(episodes, m)
		}
	}
	bySc := func(list []*store.Memory) {
		sort.Slice(list, func(i, j int) bool { return score[list[i].ID] > score[list[j].ID] })
	}
	bySc(lessons)
	bySc(facts)
	bySc(episodes)
	out.Items = append(out.Items, head(lessons, e.MemorySetting(memLimitLessons))...)
	out.Items = append(out.Items, head(facts, e.MemorySetting(memLimitFacts))...)
	out.Items = append(out.Items, head(episodes, e.MemorySetting(memLimitEpisodes))...)

	out.Text = e.memoryBlock(out)
	out.Tokens = tokensOf(out.Text)
	return out
}

func head(list []*store.Memory, n int) []*store.Memory {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// memoryBlock — блок памяти для промпта. Текущие версии старше любого
// повторного контекста: об этом модели сказано прямо.
func (e *Engine) memoryBlock(r Recalled) string {
	if len(r.Items) == 0 && len(r.Graph) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nMemory — what Sema already established earlier (current versions; they take precedence over older context). " +
		"If the user brings new data, say you can recount from the sources.\n")
	for _, m := range r.Items {
		kind := map[string]string{store.MemLesson: "урок", store.MemFact: "факт", store.MemCrystal: "сводка"}[m.Kind]
		if kind == "" {
			kind = m.Kind
		}
		fmt.Fprintf(&b, "- [%s · %s · %s] %s\n", kind, m.Area, shortDate(m.Created), m.Text)
	}
	for _, g := range r.Graph {
		b.WriteString("- [связь] " + g + "\n")
	}
	if len(r.Closed) > 0 {
		b.WriteString("Some memory areas are closed for this model (" + strings.Join(r.Closed, ", ") +
			"); if the answer may depend on them, say that part of memory is closed for this model.\n")
	}
	return b.String()
}

func shortDate(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.Local().Format("02.01")
}

// graphAround — обход графа на глубину depth от узлов; связь между
// областями видна, только если выданы обе.
func (e *Engine) graphAround(start []int64, depth int, allow map[string]bool) (lines []string, memIDs []int64) {
	nodes, _ := e.store.MemoryNodes()
	edges, _ := e.store.MemoryEdges()
	byID := map[int64]*store.MemoryNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	seen := map[int64]bool{}
	frontier := start
	for _, id := range start {
		seen[id] = true
	}
	edgeSeen := map[int64]bool{}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []int64
		for _, ed := range edges {
			var other int64
			switch {
			case seen[ed.From] && !edgeSeen[ed.ID]:
				other = ed.To
			case seen[ed.To] && !edgeSeen[ed.ID]:
				other = ed.From
			default:
				continue
			}
			a, b := byID[ed.From], byID[ed.To]
			if a == nil || b == nil || !allow[a.Area] || !allow[b.Area] {
				continue
			}
			edgeSeen[ed.ID] = true
			lines = append(lines, a.Name+" —"+ed.Relation+"→ "+b.Name)
			if ed.MemoryID > 0 {
				memIDs = append(memIDs, ed.MemoryID)
			}
			if !seen[other] {
				seen[other] = true
				next = append(next, other)
			}
			if len(lines) >= 24 {
				return lines, memIDs
			}
		}
		frontier = next
	}
	return lines, memIDs
}

// LogRecall пишет обращение в журнал: сколько токенов ушло из памяти и
// сколько сбережено против контекста, который иначе собрали бы заново.
func (e *Engine) LogRecall(o Origin, r Recalled, saved int, note string) {
	detail := fmt.Sprintf("вспомнить: %d записей, %d связей", len(r.Items), len(r.Graph))
	if note != "" {
		detail += " · " + note
	}
	if len(r.Closed) > 0 {
		detail += " · закрыто: " + strings.Join(r.Closed, ", ")
	}
	e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: o.SessionID,
		Area: strings.Join(o.Consumer.Areas, ","), Action: "recall", Detail: detail,
		Granted: len(r.Items) > 0 || len(r.Graph) > 0, Tokens: r.Tokens, Saved: saved})
}

// tokensOf — грубая оценка токенов: для кириллицы около трёх символов.
func tokensOf(s string) int { return len([]rune(s)) / 3 }

// ModelIsCloud — уходит ли запрос к этой модели наружу. Локальная — та,
// чей адрес на этой машине или в домашней сети; подписка через CLI вендора
// — облако.
func (e *Engine) ModelIsCloud(model string) bool {
	if model == "" {
		return false // модели нет: код модуля работает внутри Сёмы, наружу ничего не уходит
	}
	for _, c := range e.userModels() {
		if c.Name != model {
			continue
		}
		if c.Type == "cli" {
			return true
		}
		// «облачная» здесь значит «ей нельзя закрытое»: это доверие,
		// объявленное человеком, а не адрес — адрес ничего не гарантирует
		return !c.Trusted
	}
	return true // неизвестная модель — считаем облаком: закрытое не отдаём
}

var localNetRe = regexp.MustCompile(`^172\.(1[6-9]|2\d|3[01])\.`)

// ---- предложения областей ----

type AreaSuggestion struct {
	Name string `json:"name"`
	Why  string `json:"why"`
	At   string `json:"at"`
}

func (e *Engine) AreaSuggestions() []AreaSuggestion {
	var out []AreaSuggestion
	_ = json.Unmarshal([]byte(e.store.Setting(memSuggestions, "[]")), &out)
	return out
}

// SuggestArea запоминает предложение Сёмы завести область; заводит человек.
func (e *Engine) SuggestArea(name, why string) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return
	}
	if _, ok := e.store.MemoryArea(name); ok {
		return
	}
	list := e.AreaSuggestions()
	for _, s := range list {
		if s.Name == name {
			return
		}
	}
	list = append(list, AreaSuggestion{Name: name, Why: why, At: time.Now().Format(time.RFC3339)})
	raw, _ := json.Marshal(list)
	_ = e.store.SetSetting(memSuggestions, string(raw))
}

func (e *Engine) DropAreaSuggestion(name string) {
	var keep []AreaSuggestion
	for _, s := range e.AreaSuggestions() {
		if s.Name != name {
			keep = append(keep, s)
		}
	}
	raw, _ := json.Marshal(keep)
	_ = e.store.SetSetting(memSuggestions, string(raw))
}

// CreateArea заводит область и выдаёт её чату Сёмы.
func (e *Engine) CreateArea(name, title string) error {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return fmt.Errorf("у области должно быть имя")
	}
	if title == "" {
		title = name
	}
	if err := e.store.SaveMemoryArea(&store.MemoryArea{Name: name, Title: title, Confirm: store.ConfirmAuto, TTLDays: 90}); err != nil {
		return err
	}
	e.DropAreaSuggestion(name)
	c := e.ChatConsumer()
	if !c.Has(name) {
		return e.store.SetConsumerAreas(c.ID, append(c.Areas, name))
	}
	return nil
}

// GrantArea выдаёт или отзывает область у потребителя.
func (e *Engine) GrantArea(consumerID int64, area string, on bool) error {
	c, err := e.store.MemoryConsumerByID(consumerID)
	if err != nil {
		return err
	}
	if _, ok := e.store.MemoryArea(area); !ok && on {
		return fmt.Errorf("области %q нет", area)
	}
	areas := without(c.Areas, area)
	if on {
		areas = append(areas, area)
		sort.Strings(areas)
	}
	if err := e.store.SetConsumerAreas(c.ID, areas); err != nil {
		return err
	}
	// у модуля выданные области живут и в манифесте: иначе следующий запуск
	// вернул бы старый набор
	if c.Kind == store.ConsumerModule {
		all, err := e.modulesMap()
		if err == nil {
			for _, m := range all {
				if m.Title == c.Name {
					m.GrantedMemory = areas
					_ = e.saveModules(all)
				}
			}
		}
	}
	log.Printf("память: %s — области %v", c.Label(), areas)
	return nil
}

// AreasOf — сколько записей в области и последнее обращение к ней.
func (e *Engine) MemoryCounts() (map[string]int, map[string]int) { return e.store.CountMemories() }

// GraphNodesLimit — сколько узлов графа памяти показывать разом. Больше
// человек всё равно не разглядит, а раскладка считает силы для каждой
// пары: на пятистах узлах окно перестаёт отзываться. 0 — без предела.
func (e *Engine) GraphNodesLimit() int {
	n, err := strconv.Atoi(strings.TrimSpace(e.store.Setting("memory.graph_nodes", "200")))
	if err != nil || n < 0 {
		return 200
	}
	return n
}

// CodeKnowledge — что мы знаем про место в коде. Агент разработки видит,
// что в коде **сейчас**; это отвечает на другой вопрос — что здесь уже
// ломалось, каким решением держится и о чём договаривались.
//
// Права те же, что у обычного припоминания: чужой агент видит только
// выданные ему области, и облачной модели закрытое не уходит.
func (e *Engine) CodeKnowledge(ctx context.Context, agent, path, file, symbol string) string {
	return e.PlacesKnowledge(ctx, agent, path, "", []store.PlaceQuery{{File: file, Symbol: symbol}})
}

// PlacesKnowledge — то же по списку мест карточки: папка, файл, символы.
// Один запрос на карточку, а не по запросу на символ. Порядок — точное
// раньше общего: символ → файл → папка, внутри уровня подтверждённое и
// свежее раньше. Репозиторий — от git-корня, которое назвал житель
// машины; пусто — по хвосту рабочей папки, как раньше.
func (e *Engine) PlacesKnowledge(ctx context.Context, agent, path, repo string, places []store.PlaceQuery) string {
	o := e.ExternalOrigin(agent, path)
	if o.Consumer == nil || len(o.Consumer.Areas) == 0 {
		return ""
	}
	if repo == "" {
		repo = repoByPath(path)
	}
	list, err := e.store.PlacesKnowledge(repo, places, 40)
	if err != nil || len(list) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Что известно про %s:\n", placesTitle(places))
	shown := 0
	for _, k := range list {
		if !allowedArea(o.Consumer.Areas, k.Area) {
			continue
		}
		kind := map[string]string{store.MemLesson: "урок", store.MemFact: "факт",
			store.MemCrystal: "итог", store.MemObservation: "наблюдение"}[k.Kind]
		fmt.Fprintf(&b, "- [%s] %s\n", kind, clip(k.Text, 400))
		pl := k.Via
		// кто это утверждает: требование спеки, урок из работы и абзац
		// инструкций весят по-разному, и человек должен видеть разницу
		if pl.Source != "" {
			fmt.Fprintf(&b, "  источник: %s\n", pl.Source)
		}
		switch {
		case k.Level == store.LevelDir:
			fmt.Fprintf(&b, "  (про папку %s/)\n", pl.Dir)
		case k.Level == store.LevelSymbol && pl.File != "" && !queriesFile(places, pl.File):
			// место могло переехать: сказать об этом честнее, чем молчать
			fmt.Fprintf(&b, "  (записано про %s в %s — место могло переехать)\n", pl.Symbol, pl.File)
		}
		if pl.Doubtful {
			switch {
			case strings.HasPrefix(pl.Source, PrincipleSource):
				b.WriteString("  (абзаца в CLAUDE.md больше нет — устарело)\n")
			case pl.Symbol != "":
				fmt.Fprintf(&b, "  (символа %s в коде уже нет — знание может относиться к другому месту)\n", pl.Symbol)
			case pl.Dir != "":
				fmt.Fprintf(&b, "  (папки %s/ больше нет — место могло переехать)\n", pl.Dir)
			}
		}
		shown++
	}
	if shown == 0 {
		return ""
	}
	return b.String()
}

// placesTitle — о чём спрашивали, одной строкой: символ и файл, файл,
// папка; у оглавления с сорока символами — файл и число символов.
func placesTitle(places []store.PlaceQuery) string {
	var file, dir string
	symbols := 0
	for _, q := range places {
		if q.Symbol != "" {
			symbols++
		}
		if q.File != "" && file == "" {
			file = q.File
		}
		if q.Dir != "" && dir == "" {
			dir = q.Dir
		}
	}
	switch {
	case symbols == 1 && file != "":
		for _, q := range places {
			if q.Symbol != "" {
				return q.Symbol + " (" + file + ")"
			}
		}
	case symbols > 1 && file != "":
		return fmt.Sprintf("%s (%s)", file, pluralRu(symbols, "символ", "символа", "символов"))
	case file != "":
		return file
	case symbols == 1:
		for _, q := range places {
			if q.Symbol != "" {
				return q.Symbol
			}
		}
	case dir != "":
		return dir + "/"
	}
	return "это место"
}

func queriesFile(places []store.PlaceQuery, file string) bool {
	for _, q := range places {
		if q.File == file {
			return true
		}
	}
	return false
}

// repoByPath — имя репозитория по рабочей папке агента: «server», «app»,
// «agent». Пусто — знание ищется по всем.
func repoByPath(path string) string {
	for _, name := range []string{"server", "app", "agent"} {
		if strings.HasSuffix(strings.TrimRight(path, "/"), "/"+name) {
			return name
		}
	}
	return ""
}

// allowedArea — выдана ли область этому потребителю.
func allowedArea(granted []string, area string) bool {
	for _, a := range granted {
		if a == area || a == "*" {
			return true
		}
	}
	return false
}

// ---- Связи с остальными источниками знания ----

// BindWithPlace — привязать уже существующую запись памяти к месту в коде
// от имени источника. Пользуются самообучение (урок из работы), разбор
// чатов (где это место обсуждали) и человек с экрана.
func (e *Engine) BindWithPlace(memoryID int64, repo, file, symbol, source string) error {
	return e.store.BindToPlace(store.Place{
		MemoryID: memoryID, Repo: repo, File: file, Symbol: symbol, Source: source,
	})
}

// rePlaceInText — какие места в коде названы в тексте. Ищем то, что похоже
// на путь к файлу Go и на `имя.go:строка`. Догадка нарочно узкая: лучше не
// привязать, чем привязать не туда — неверная координата хуже отсутствия.
var rePlaceInText = regexp.MustCompile(`\b([\w./-]+\.(?:go|js|swift|md))(?::(\d+))?\b`)

func placesFromText(text string) []store.Place {
	var out []store.Place
	was := map[string]bool{}
	for _, m := range rePlaceInText.FindAllStringSubmatch(text, 5) {
		file := m[1]
		if was[file] {
			continue
		}
		was[file] = true
		place := store.Place{File: file}
		if m[2] != "" {
			if n, err := strconv.Atoi(m[2]); err == nil {
				place.Line = n
			}
		}
		// репозиторий угадывается по первому куску пути: server/…, app/…
		if i := strings.Index(file, "/"); i > 0 {
			switch file[:i] {
			case "server", "app", "agent":
				place.Repo, place.File = file[:i], file[i+1:]
			}
		}
		out = append(out, place)
	}
	return out
}

// BindByText — если в тексте записи названы файлы, привязать её к
// ним. Возвращает, сколько привязок сделано. Пусто — обычное дело: не в
// каждом уроке есть координата, и выдумывать её нельзя.
func (e *Engine) BindByText(memoryID int64, text, source string) int {
	n := 0
	for _, m := range placesFromText(text) {
		m.MemoryID, m.Source = memoryID, source
		if err := e.store.BindToPlace(m); err == nil {
			n++
		}
	}
	return n
}

// codeKnowledgeCap — предел приложения к карточке, в знаках. Как у скиллов
// в промпте: знание полезно, пока не вытесняет сам код. Настройка, а не
// гвоздь — у кого модель с большим окном, тот поднимет.
const codeKnowledgeCap = "code.knowledge_chars"

func (e *Engine) CodeKnowledgeLimit() int { return e.settingInt(codeKnowledgeCap, 1500) }

// AttachToPlace — накопленное знание одной строкой для карточки кода.
// Агент отвечает, что в коде **сейчас**; это добавляет, что здесь уже
// ломалось. Обрезается по пределу с честной пометкой: молча урезанное
// знание хуже, чем названный остаток.
func (e *Engine) AttachToPlace(ctx context.Context, agent, path, file, symbol string) string {
	return e.AttachToPlaces(ctx, agent, path, "", []store.PlaceQuery{{File: file, Symbol: symbol}})
}

// AttachToPlaces — приложение по списку мест карточки, в пределе.
func (e *Engine) AttachToPlaces(ctx context.Context, agent, path, repo string, places []store.PlaceQuery) string {
	text := e.PlacesKnowledge(ctx, agent, path, repo, places)
	if text == "" {
		return ""
	}
	limit := e.CodeKnowledgeLimit()
	if len([]rune(text)) <= limit {
		return text
	}
	// режем по строкам, а не посередине фразы: обрывок урока хуже, чем его
	// отсутствие — по нему делают неверный вывод
	var b strings.Builder
	left := 0
	for i, line := range strings.Split(text, "\n") {
		if len([]rune(b.String()))+len([]rune(line)) > limit-80 && i > 0 {
			left++
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if left > 0 {
		fmt.Fprintf(&b, "…и ещё %s — спросите code_knowledge, если нужно всё\n",
			pluralRu(left, "запись", "записи", "записей"))
	}
	return b.String()
}

// pluralRu — русское склонение по числу.
func pluralRu(n int, one, few, many string) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod10 == 1 && mod100 != 11:
		return fmt.Sprintf("%d %s", n, one)
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return fmt.Sprintf("%d %s", n, few)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---- Разметка знания по месту от разных источников ----

// BindChatWithPlace — разговор, в котором это место разбирали. Зовётся при
// закрытии чата: если в переписке названы файлы, знание о них должно
// находиться и потом.
func (e *Engine) BindChatWithPlace(chatID int64, memoryID int64, text string) int {
	title := "чат"
	if c, err := e.store.Chat(chatID); err == nil && c.Title != "" {
		title = "чат «" + c.Title + "»"
	}
	return e.BindByText(memoryID, text, title)
}

// SuggestPitfall — разметка граблей из `CLAUDE.md`. Сервер живёт в
// контейнере и файлов репозитория не видит, поэтому текст приносит тот,
// кто их читает: агент разработки или человек. Записи ложатся как
// предложения — принимает человек, потому что грабли из общего файла
// касаются всех, а не одной работы.
//
// Возвращает, сколько предложено.
func (e *Engine) SuggestPitfall(ctx context.Context, o Origin, pitfall []string) int {
	n := 0
	for _, text := range pitfall {
		text = strings.TrimSpace(text)
		if len([]rune(text)) < 40 {
			continue // однострочник без места ничего не объясняет
		}
		place := placesFromText(text)
		if len(place) == 0 {
			continue // грабля без координаты — обычная запись, не наше дело
		}
		m, err := e.Remember(ctx, o, MemoryRecord{
			Kind: store.MemLesson, Area: o.Consumer.Areas[0],
			Text: clip(text, 600),
		})
		if err != nil || m == nil {
			continue
		}
		if e.BindByText(m.ID, text, "грабли из CLAUDE.md") > 0 {
			n++
		}
	}
	return n
}
