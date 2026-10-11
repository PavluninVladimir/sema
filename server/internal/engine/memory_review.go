package engine

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Разбор памяти моделью. Кнопки «Подтвердить · Исправить · Забыть» на экране
// остаются человеку, но нажимать их не обязательно: раз в проход сведения
// Сёма сам смотрит неподтверждённые записи и решает — оставить навсегда,
// переписать понятнее или забыть как мусор. Ничего не пропадает молча:
// каждое решение видно в журнале памяти, а «забыть» только для записей
// без подтверждения человека.

const (
	memReviewKey      = "memory.review"       // 1 — разбирать самому
	memReviewBatchKey = "memory.review_batch" // сколько записей за проход
)

func (e *Engine) MemoryReviewOn() bool { return e.store.Setting(memReviewKey, "1") == "1" }

func (e *Engine) SetMemoryReview(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(memReviewKey, v)
}

func (e *Engine) memoryReviewBatch() int {
	v, _ := strconv.Atoi(e.store.Setting(memReviewBatchKey, "20"))
	if v <= 0 {
		v = 20
	}
	return v
}

const reviewPrompt = "Ты сам ведёшь свою память и решаешь судьбу записи. " +
	"Ответь ТОЛЬКО JSON: {\"verdict\": \"keep|fix|forget\", \"text\": \"новый текст, если fix\", \"why\": \"причина одной строкой, по-русски\"}.\n" +
	"keep — запись пригодится в будущих разговорах, оставить навсегда.\n" +
	"fix — мысль верная, но написана мутно или слишком длинно: перепиши одним предложением, по-русски, без воды.\n" +
	"forget — сиюминутное, дубль, пересказ разговора, обрывок команды или то, что не понадобится: забыть.\n" +
	"Секретов и содержимого файлов в тексте быть не должно.\n\n"

type memVerdict struct {
	Verdict string `json:"verdict"`
	Text    string `json:"text"`
	Why     string `json:"why"`
}

// reviewMemories — разбор пачкой: остался для кнопки «Разобрать сейчас»,
// обычно работу ведёт очередь (WorkMemoryReview).
func (e *Engine) reviewMemories(ctx context.Context) int {
	if !e.MemoryReviewOn() {
		return 0
	}
	// сначала — привязки к местам, которых больше нет: заметку не теряем,
	// но и делать вид, что она про живой код, нельзя. Сверка с кодом идёт
	// в сведении (задача «Память»): работа очереди разбирает одну запись,
	// а спрашивать жителя машины о координатах имеет смысл пачкой
	e.parseDoubtfulPlaces()

	list, err := e.store.MemoriesForReview(e.memoryReviewBatch())
	if err != nil || len(list) == 0 {
		return 0
	}
	n := 0
	for _, m := range list {
		area, ok := e.store.MemoryArea(m.Area)
		if !ok || area.Confirm == store.ConfirmAsk {
			_ = e.store.SetMemoryReviewed(m.ID)
			continue
		}
		v, err := e.askVerdict(ctx, m, false)
		if err != nil {
			return n // модель недоступна: записи ждут следующего прохода
		}
		e.applyVerdict(ctx, m, v)
		n++
	}
	return n
}

// askVerdict — что модель решила про одну запись. Область, закрытая наружу,
// не уходит облачной модели ни основной, ни запасной: разбор подождёт, а
// обещание «наружу закрыто» останется обещанием.
func (e *Engine) askVerdict(ctx context.Context, m *store.Memory, allowSpare bool) (memVerdict, error) {
	closed := false
	if area, ok := e.store.MemoryArea(m.Area); ok && !area.Outbound {
		// закрытая область — не повод стоять вечно: разбор идёт доверенной
		// модели, как и всё закрытое. Нет ни одной доверенной — вот тогда
		// работа честно ждёт человека
		closed = true
		if allowSpare && e.ModelIsCloud(e.Role(RoleMemory).Spare) {
			allowSpare = false // облачная запасная такой области не положена
		}
	}
	prompt := reviewPrompt + "Область: " + m.Area + " · вид: " + m.Kind + " · источник: " + m.Agent +
		" · создана: " + shortDate(m.Created) + "\nЗапись: " + m.Text
	answer, _, err := e.askRoleClosed(ctx, RoleMemory, prompt, allowSpare, closed)
	if err != nil {
		return memVerdict{}, err
	}
	var v memVerdict
	if json.Unmarshal([]byte(extractJSON(answer)), &v) != nil || v.Verdict == "" {
		_ = e.store.SetMemoryReviewed(m.ID) // не разобрались — не мучаем повторно
		return memVerdict{Verdict: "keep", Why: "ответ модели не разобран"}, nil
	}
	return v, nil
}

func (e *Engine) applyVerdict(ctx context.Context, m *store.Memory, v memVerdict) {
	detail := clip(m.Text, 80)
	switch strings.ToLower(strings.TrimSpace(v.Verdict)) {
	case "forget":
		if m.Confirmed {
			break // подтверждённое человеком Сёма не забывает
		}
		e.store.LogMemory(store.MemoryLogEntry{Consumer: "Сёма · разбор памяти", Area: m.Area, Action: "forget",
			Detail: detail + " · " + clip(v.Why, 80), Granted: true})
		_ = e.store.DeleteMemory(m.ID)
		log.Printf("память: забыто по разбору — %s (%s)", detail, clip(v.Why, 60))
		return
	case "fix":
		text := strings.TrimSpace(v.Text)
		if text == "" || text == m.Text {
			break
		}
		nm := &store.Memory{Kind: m.Kind, Area: m.Area, Text: text, Agent: "Сёма · разбор памяти", Confirmed: true,
			Supersedes: m.ID, Sources: append(m.Sources, "разбор"), Weight: m.Weight}
		id, err := e.store.SaveMemory(nm)
		if err != nil {
			return
		}
		nm.ID = id
		// переписанный урок остаётся про то же место в коде
		_ = e.store.MovePlaces(m.ID, id)
		_ = e.store.SetMemoryReviewed(id)
		e.embedMemory(ctx, nm)
		e.store.LogMemory(store.MemoryLogEntry{Consumer: "Сёма · разбор памяти", Area: m.Area, Action: "remember",
			Detail: "переписано: " + clip(text, 80) + " · " + clip(v.Why, 60), Granted: true, Tokens: tokensOf(text)})
		return
	}
	// keep и всё непонятое: запись остаётся, живёт без срока
	m.Confirmed = true
	m.Expires = ""
	_ = e.store.UpdateMemory(m)
	_ = e.store.SetMemoryReviewed(m.ID)
	e.store.LogMemory(store.MemoryLogEntry{Consumer: "Сёма · разбор памяти", Area: m.Area, Action: "observe",
		Detail: "оставлено: " + detail + " · " + clip(v.Why, 60), Granted: true})
}

// MemoryReviewStat — сколько записей ждут разбора: для экрана «Память».
func (e *Engine) MemoryReviewStat() (waiting int, last string) {
	list, err := e.store.MemoriesForReview(100)
	if err != nil {
		return 0, ""
	}
	return len(list), e.store.Setting("memory.review_last", "")
}

func (e *Engine) noteReviewRun(n int) {
	if n > 0 {
		_ = e.store.SetSetting("memory.review_last", time.Now().Format(time.RFC3339))
	}
}

// verifyPlacesWithCode — жив ли ещё символ, на который показывает запись.
// Спрашивает житель машины: файлы репозитория видит только он, Сёма живёт
// в контейнере и о коде знает лишь то, что ей рассказали.
//
// Ответ «не знаю» ничего не меняет. Языкового сервера может не быть,
// ноутбук может спать, папка — переехать; снять координату по молчанию
// значило бы стереть разметку памяти на первой же неудачной проверке.
func (e *Engine) verifyPlacesWithCode(ctx context.Context) {
	place := e.store.PlacesToVerify(time.Now().AddDate(0, 0, -e.settingInt(memPlaceCheckDays, 7)), 10)
	if len(place) == 0 {
		return
	}
	agent := e.agentWithCode()
	if agent == "" {
		return // некому спросить: житель машины не на связи
	}
	roots := e.store.ConsumerRoots(10)
	if len(roots) == 0 {
		return
	}
	for _, m := range place {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		args := map[string]any{"корни": roots, "файл": placePath(m), "символ": m.Symbol}
		if m.Symbol == "" && m.Dir != "" {
			// привязка к папке: спрашиваем про папку, символа тут нет
			args = map[string]any{"корни": roots, "папка": placeDir(m)}
		}
		raw, err := e.AskAgent(cctx, agent, "код", "код.место", args)
		cancel()
		if err != nil {
			return // провод оборвался — вернёмся к этому следующим проходом
		}
		var reply struct {
			Есть  bool  `json:"есть"`
			Знаем bool  `json:"знаем"`
			Папка *bool `json:"папка"`
		}
		if json.Unmarshal(raw, &reply) != nil || !reply.Знаем {
			continue
		}
		if m.Symbol == "" && m.Dir != "" {
			// житель прежней сборки папок не понимает и ответил бы про
			// файл «нет»: без поля «папка» в ответе — это «не знаю»
			if reply.Папка == nil {
				continue
			}
			if err := e.store.MarkDirDoubtful(m.MemoryID, m.Dir, !reply.Есть); err != nil {
				continue
			}
			if !reply.Есть {
				log.Printf("память: папки %s больше нет — координата записи %d помечена «место могло переехать»",
					placeDir(m), m.MemoryID)
			}
			continue
		}
		if err := e.store.MarkDoubtful(m.MemoryID, m.Symbol, !reply.Есть); err != nil {
			continue
		}
		if !reply.Есть {
			log.Printf("память: символа %s в %s больше нет — координата записи %d помечена сомнительной",
				m.Symbol, m.File, m.MemoryID)
		}
	}
}

// placeDir — папка привязки с репозиторием впереди, как у placePath.
func placeDir(m store.Place) string {
	if m.Repo == "" || strings.HasPrefix(m.Dir, m.Repo+"/") {
		return m.Dir
	}
	return m.Repo + "/" + m.Dir
}

// placePath — координата хранится по-разному: у старых записей путь от
// корня репозитория, у новых — от корня зонтика. Репозиторий приписываем,
// когда его в пути ещё нет, а искать файл всё равно будет житель машины.
func placePath(m store.Place) string {
	if m.Repo == "" || strings.HasPrefix(m.File, m.Repo+"/") {
		return m.File
	}
	return m.Repo + "/" + m.File
}

// agentWithCode — первый житель машины на связи, которому разрешён «код».
func (e *Engine) agentWithCode() string {
	list, err := e.store.Agents()
	if err != nil {
		return ""
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for _, a := range list {
		if e.AgentAllowed(a.Name, "код") && e.AgentOnline(a.Name) {
			return a.Name
		}
	}
	return ""
}

// parseDoubtfulPlaces — привязка указывает на символ, которого нет.
// Заметку не выбрасываем: место могло переехать, а знание остаться верным.
// Просроченную привязку снимаем — знание остаётся, координата уходит.
func (e *Engine) parseDoubtfulPlaces() {
	due := e.settingInt("code.place_ttl_days", 90)
	if due <= 0 {
		return
	}
	threshold := time.Now().AddDate(0, 0, -due)
	for _, m := range e.store.DoubtfulPlaces(20) {
		verified, err := time.Parse(time.RFC3339, m.Verified)
		if err != nil || verified.After(threshold) {
			continue
		}
		// координата протухла: снимаем её, запись живёт дальше без места
		if err := e.store.UnbindPlace(m); err != nil {
			continue
		}
		log.Printf("память: запись %d отвязана от %s — места нет уже %d дней, само знание сохранено",
			m.MemoryID, m.Where(), due)
	}
}
