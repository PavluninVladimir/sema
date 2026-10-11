package engine

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// embText — каноничное текстовое представление письма для векторизации.
func embText(from, subject, snippet string) string {
	if len([]rune(snippet)) > 800 {
		snippet = snippet[:800]
	}
	return "From: " + from + "\nSubject: " + subject + "\n" + snippet
}

type neighbor struct {
	msgID  int64
	status string
	sim    float64
}

// nearest — top-k ближайших писем среди заданных статусов (пустой список — все).
func (e *Engine) nearest(vec []float32, k int, statuses ...string) ([]neighbor, error) {
	return e.nearestOpen(vec, k, nil, statuses...)
}

// nearestOpen — то же, но письма закрытых ящиков в выдачу не попадают.
func (e *Engine) nearestOpen(vec []float32, k int, closed map[string]bool, statuses ...string) ([]neighbor, error) {
	rows, err := e.store.Embeddings(statuses...)
	if err != nil {
		return nil, err
	}
	out := make([]neighbor, 0, len(rows))
	for _, r := range rows {
		if closed[r.Account] {
			continue
		}
		out = append(out, neighbor{
			msgID:  r.MsgID,
			status: r.Status,
			sim:    embed.Cosine(vec, embed.DecodeVec(r.Vector)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sim > out[j].sim })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// ragDecide пробует решить судьбу письма по похожести на размеченные человеком
// письма. Возвращает true, если решение принято без LLM.
func (e *Engine) ragDecide(sess mover, m *store.Message, vec []float32) bool {
	if vec == nil {
		return false
	}
	// только человеческая разметка: подтверждённый спам и белый список
	nn, err := e.nearest(vec, 1, store.StatusConfirmed, store.StatusHam)
	if err != nil || len(nn) == 0 || nn[0].sim < e.AutoThreshold() {
		return false
	}
	best := nn[0]
	switch best.status {
	case store.StatusConfirmed:
		if _, err := sess.MoveToJunkByID(m.MessageID, m.UID); err != nil {
			log.Printf("rag auto-move %q: %v", m.Subject, err)
			return false
		}
		now := time.Now()
		m.Status = store.StatusAutoMoved
		m.MovedAt = &now
		m.Confidence = best.sim
		m.Reason = fmt.Sprintf("похоже на подтверждённый вами спам (сходство %.0f%%)", best.sim*100)
	case store.StatusHam:
		m.Status = store.StatusInboxOK
		m.Confidence = 0
		m.Reason = fmt.Sprintf("похоже на письмо из белого списка (сходство %.0f%%)", best.sim*100)
	default:
		return false
	}
	return true
}

// ragExamples — размеченные примеры для промпта классификатора.
func (e *Engine) ragExamples(vec []float32) string {
	if vec == nil || e.RagExamples() == 0 {
		return ""
	}
	nn, err := e.nearest(vec, e.RagExamples(), store.StatusConfirmed, store.StatusHam, store.StatusAutoMoved)
	if err != nil || len(nn) == 0 {
		return ""
	}
	ids := make([]int64, len(nn))
	simByID := map[int64]neighbor{}
	for i, n := range nn {
		ids[i] = n.msgID
		simByID[n.msgID] = n
	}
	msgs, err := e.store.MessagesByIDs(ids)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nSimilar emails the user already labeled:\n")
	for _, m := range msgs {
		label := "SPAM"
		if m.Status == store.StatusHam {
			label = "NOT SPAM"
		}
		fmt.Fprintf(&b, "- [%s] From: %s Subject: %s\n", label, m.FromAddr, m.Subject)
	}
	return b.String()
}

// backfillEmbeddings векторизует письма, попавшие в базу до включения RAG.
func (e *Engine) backfillEmbeddings(ctx context.Context) {
	if e.embedder == nil {
		return
	}
	for {
		batch, err := e.store.WithoutEmbedding(16)
		if err != nil || len(batch) == 0 {
			return
		}
		texts := make([]string, len(batch))
		skip := make([]bool, len(batch))
		for i, m := range batch {
			// сетевому эмбеддеру секретное не отдаём: вектор считается по
			// теме и выжимке, то есть по самому письму
			if give, why := e.mayEmbed(m.ID); !give {
				skip[i] = true
				texts[i] = ""
				log.Printf("письмо %d не векторизую: %s", m.ID, why)
				continue
			}
			texts[i] = embText(m.FromAddr, m.Subject, m.Snippet)
		}
		vecs, err := e.embedder.Embed(ctx, texts)
		e.markVector(err)
		if err != nil {
			log.Printf("embed backfill: %v", err)
			return
		}
		for i, m := range batch {
			if skip[i] {
				continue
			}
			if err := e.store.SaveEmbedding(m.ID, embed.EncodeVec(vecs[i])); err != nil {
				log.Printf("save embedding %d: %v", m.ID, err)
				return
			}
		}
		log.Printf("embeddings: backfilled %d messages", len(batch))
	}
}

const askPrompt = `You are Sema, a personal email assistant. Answer the user's question based ONLY on these emails from their mailbox. Answer in Russian, briefly. If the emails don't contain the answer, say so.

%s

Question: %s`

// Ask — RAG-поиск по почте: вопрос → ближайшие письма → ответ LLM.
func (e *Engine) Ask(ctx context.Context, query string) (string, error) {
	if err := e.budgetCheck(ctx); err != nil {
		return "", err
	}
	if e.embedder == nil {
		return "", fmt.Errorf("RAG не настроен (секция embeddings в конфиге)")
	}
	// вопрос человека уходит сетевому эмбеддеру такой же строкой, как
	// письмо, — значит и проверяться должен так же. Раньше этот путь шёл
	// мимо: поиск по своему ИНН отдавал его наружу ещё до всякой модели
	vec, err := e.embedIfAllowed(ctx, query)
	if err != nil {
		return "", fmt.Errorf("векторизация вопроса: %w", err)
	}
	if vec == nil {
		return "", fmt.Errorf("в вопросе есть закрытое, а эмбеддер чужой: " +
			"пометьте его доверенным на «Инструментах» или поставьте свой")
	}
	closed := e.ClosedAccounts(e.ModelIsCloud(e.ActiveProvider()))
	nn, err := e.nearestOpen(vec, e.AskTopK(), closed)
	if err != nil {
		return "", err
	}
	if len(nn) == 0 {
		return "В базе пока нет писем — нечего искать.", nil
	}
	ids := make([]int64, len(nn))
	for i, n := range nn {
		ids[i] = n.msgID
	}
	msgs, err := e.store.MessagesByIDs(ids)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, m := range msgs {
		// по буквам, а не по байтам: русская выжимка иначе режется вдвое
		// раньше срока, да ещё и посреди буквы
		snippet := clip(m.Snippet, 500)
		fmt.Fprintf(&b, "Email %d (%s):\nFrom: %s (%s)\nSubject: %s\n%s\n\n",
			i+1, m.Date.Format("2006-01-02"), m.FromAddr, m.FromName, m.Subject, snippet)
	}
	// вложения (чеки, документы) — отдельным блоком контекста; личное
	// облачной модели не отдаём
	cloud := e.ModelIsCloud(e.ActiveProvider())
	files, secret, sensitive := e.attContext(query, vec, 4, closed, cloud)
	b.WriteString(files)
	b.WriteString(cutNotice(secret, sensitive, nil))
	b.WriteString(closedNotice(closed))
	provider := e.provider(e.ActiveProvider())
	if provider == nil {
		return "", fmt.Errorf("LLM-провайдер не настроен")
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	return provider.Complete(cctx, fmt.Sprintf(askPrompt, b.String(), query))
}

// mover — часть mail.Session, нужная RAG-решению (для тестов).
type mover interface {
	MoveToJunkByID(messageID string, fallbackUID uint32) (bool, error)
}

// saveDenial — что не ушло и почему. Пишется в «Что ушло» строкой с
// неудачей: человек смотрит туда, чтобы понять, что покидало дом, и
// невидимый отказ там хуже отказа вовсе — запись без вектора выглядела бы
// потерей, а не решением.
func (e *Engine) saveDenial(kind, why, text string) {
	log.Printf("%s не считаю: %s", kind, why)
	_ = e.store.AddSent(&store.SentRow{
		Consumer: "core:embed", Model: e.EmbedModel(),
		Outside: true, Kind: kind, Class: sentClass(text),
		OK: false, Err: "не отправлено: " + why,
		Preview: clip(text, 200),
	})
}

// mayEmbed — можно ли отдать это письмо эмбеддеру. Дома можно всё; наружу
// секретное не уходит и здесь, хотя дорога и выглядит служебной: вектор
// считается по теме и выжимке, то есть по содержимому письма.
func (e *Engine) mayEmbed(msgID int64) (bool, string) {
	if e.embedder == nil || e.EmbedTrusted() {
		return true, ""
	}
	m, err := e.store.Message(msgID)
	if err != nil {
		return true, ""
	}
	give, why := e.outsideCut(true, e.ClassOfMessage(m), m.FromAddr)
	if !give {
		e.saveDenial("вектор", why, m.Subject+"\n"+m.Snippet)
	}
	return give, why
}

// embedIfAllowed — вектор для текста, который ещё не стал записью в базе
// (новое письмо, файл, запись памяти). Класс считается по самому тексту:
// другого основания на этот момент нет. Дома считаем всё, наружу секретное
// не отдаём и возвращаем nil без ошибки — вектора просто не будет.
// Счёт неудач эмбеддера. Молчаливая поломка векторов стоила Сёме
// месяцев: адрес в настройке не применялся при старте, каждое письмо
// давало строку «lookup embed: no such host» в журнал — и всё. Поиск по
// смыслу не работал, а «Здоровье» было зелёным. Теперь неудачи считаются
// и видны карточкой.
func (e *Engine) markVector(err error) {
	if err == nil {
		e.vectorsInARow.Store(0)
		e.vectorsErr.Store("")
		return
	}
	e.vectorsInARow.Add(1)
	e.vectorsErr.Store(err.Error())
}

// VectorsNotBuilt — сколько попыток подряд не удалось и почему.
// Ноль значит «всё хорошо» или «ещё не пробовали».
func (e *Engine) VectorsNotBuilt() (int64, string) {
	inARow := e.vectorsInARow.Load()
	res, _ := e.vectorsErr.Load().(string)
	return inARow, res
}

func (e *Engine) embedIfAllowed(ctx context.Context, text string) ([]float32, error) {
	if e.embedder == nil {
		return nil, fmt.Errorf("RAG не настроен")
	}
	if !e.EmbedTrusted() {
		if give, why := e.outsideCut(true, sentClass(text), ""); !give {
			// отказ виден там же, где видно ушедшее: иначе запись без
			// вектора выглядит как потеря, а не как решение
			e.saveDenial("вектор", why, text)
			return nil, nil
		}
	}
	vector, err := e.embedder.EmbedOne(ctx, text)
	e.markVector(err)
	return vector, err
}
