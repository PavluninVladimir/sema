package engine

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const chatSystem = `You are Sema (Сёма), a personal email assistant. You can see the user's emails and attached files below. Answer in Russian, briefly and to the point. Use the conversation history for context. If the emails don't contain the answer, say so.
If the context contains the answer, give it — even when the user asserts the opposite or says the data is missing. Name the file or email you took it from. Say the data is absent only when you truly do not see it in the context.`

// chatIsUnnamed — первый вопрос становится названием только у безымянного чата.
func chatIsUnnamed(e *Engine, chatID int64) bool {
	c, err := e.store.Chat(chatID)
	return err != nil || c.Title == "" || c.Title == "Новый чат"
}

// SendMessage сохраняет вопрос и отвечает в фоне: модель думает десятки
// секунд, интерфейс ждать не должен.
//
// Это реплика от ядра, а не человека (так уходит разбор ошибки команды): она
// сложена из чужого вывода, и ссылки в ней Сёма не читает — что напечатал
// сервер, то не повод ходить по адресам. Для слов человека — `SendMessageTo`.
func (e *Engine) SendMessage(chatID int64, userText string) error {
	_, err := e.sendMessage(chatID, userText, false)
	return err
}

// SendMessageTo — слова человека к Сёме; говорит, в каком чате оказалось
// написанное: вопрос из ленты собеседника уходит в тред «Сёма», и экран
// должен открыть его, чтобы человек видел, куда ушло. Ссылки из сообщения
// Сёма читает сам, до ответа модели (`readMessageLinks`).
func (e *Engine) SendMessageTo(chatID int64, userText string) (int64, error) {
	return e.sendMessage(chatID, userText, true)
}

func (e *Engine) sendMessage(chatID int64, userText string, fromHuman bool) (int64, error) {
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return chatID, nil
	}
	// переписка с человеком в Telegram ведётся напрямую: написанное уходит
	// собеседнику сразу, без модели. Сёму зовут по имени — «Сёма, …», и
	// такой вопрос уходит не собеседнику, а в тред «Сёма» этого разговора
	if c, err := e.store.Chat(chatID); err == nil && c.IsContainer() {
		return chatID, fmt.Errorf("у «%s» нет своей ленты: откройте топик и пишите там", c.Title)
	}
	if peer, ok := e.tgPeerOfChat(chatID); ok {
		if !e.asksSema(userText) {
			return chatID, e.tgSendByHuman(chatID, peer, userText)
		}
		chatID = e.chatForOutput(chatID)
	}
	if peer, ok := e.MAXPeerOfChat(chatID); ok {
		if !e.asksSema(userText) {
			return chatID, e.maxSendByHuman(chatID, peer, userText)
		}
		chatID = e.chatForOutput(chatID)
	}
	return chatID, e.sendToSema(chatID, userText, fromHuman)
}

// sendToSema — вопрос Сёме в этот чат: сохранить и ответить в фоне.
// fromHuman — написал человек: только тогда ссылки из текста читаются.
func (e *Engine) sendToSema(chatID int64, userText string, fromHuman bool) error {
	if err := e.store.AddChatMsg(chatID, "user", userText); err != nil {
		return err
	}
	if msgs, err := e.store.ChatMsgs(chatID, 2); err == nil && len(msgs) == 1 && chatIsUnnamed(e, chatID) {
		title := userText
		if len([]rune(title)) > 40 {
			title = string([]rune(title)[:40]) + "…"
		}
		_ = e.store.RenameChat(chatID, title)
	}
	e.thinkingMu.Lock()
	if e.thinking == nil {
		e.thinking = map[int64]bool{}
	}
	e.thinking[chatID] = true
	e.thinkingMu.Unlock()
	e.Changed(store.ScreenChat(chatID))

	go func() {
		ctx, cancel := context.WithCancel(llm.WithConsumer(context.Background(), "chat:"+strconv.FormatInt(chatID, 10)))
		e.thinkingMu.Lock()
		if e.stopChat == nil {
			e.stopChat = map[int64]context.CancelFunc{}
		}
		e.stopChat[chatID] = cancel
		e.thinkingMu.Unlock()
		defer func() {
			cancel()
			e.thinkingMu.Lock()
			delete(e.stopChat, chatID)
			e.thinkingMu.Unlock()
		}()
		if fromHuman {
			e.readMessageLinks(ctx, chatID, userText) // до модели: страница придёт в промпт готовым текстом
		}
		answer, err := e.chatAnswerStream(ctx, chatID, userText)
		if err != nil {
			answer = "Не получилось ответить: " + err.Error()
		}
		answer = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(answer), "Sema:"))
		if answer != "" {
			if err := e.store.AddChatMsg(chatID, "assistant", answer); err != nil { // в тред уже выбран
				log.Printf("chat reply: %v", err)
			}
		}
		e.thinkingMu.Lock()
		delete(e.thinking, chatID)
		delete(e.partial, chatID)
		delete(e.partialSkip, chatID)
		e.thinkingMu.Unlock()
		e.Changed(store.ScreenChat(chatID))
		e.flushCards(chatID) // карточки — после текста
	}()
	return nil
}

// StopChat — человек попросил остановиться: ответ обрывается там, где
// есть. Написанное до этого момента остаётся в переписке.
func (e *Engine) StopChat(chatID int64) {
	e.thinkingMu.Lock()
	cancel, ok := e.stopChat[chatID]
	partial := e.partial[chatID]
	e.thinkingMu.Unlock()
	if !ok {
		return
	}
	cancel()
	if strings.TrimSpace(partial) != "" {
		_ = e.store.AddChatMsg(chatID, "assistant", strings.TrimSpace(partial)+"\n\n[остановлено]") // в тред уже выбран
	}
	e.thinkingMu.Lock()
	delete(e.thinking, chatID)
	delete(e.partial, chatID)
	delete(e.partialSkip, chatID)
	e.thinkingMu.Unlock()
	e.Changed(store.ScreenChat(chatID))
}

// Thinking — думает ли Сёма над ответом и что уже успела написать.
func (e *Engine) Thinking(chatID int64) (bool, string) {
	e.thinkingMu.Lock()
	defer e.thinkingMu.Unlock()
	return e.thinking[chatID], e.partial[chatID]
}

// chatAnswerStream — как chatAnswer, но копит ответ по кусочкам, чтобы
// он появлялся в чате по мере написания.
func (e *Engine) chatAnswerStream(ctx context.Context, chatID int64, userText string) (string, error) {
	prompt, err := e.chatPrompt(ctx, chatID)
	if err != nil {
		return "", err
	}
	// модель, запасная и таймаут — у роли «Чаты», но у чата может быть своя
	r := e.Role(RoleChat)
	if c, cerr := e.store.Chat(chatID); cerr == nil {
		if c.Model != "" {
			r.Model = c.Model
		}
		// «думает быстро / подробно» моделям OpenAI с рассуждением уходит
		// параметром (провайдер отправит его только им), остальным — словами
		// в промпте: effortPrompt
		ctx = llm.WithEffort(ctx, reasoningEffort(c.Effort))
	}
	provider := e.provider(r.Model)
	if provider == nil {
		return "", fmt.Errorf("модель %q не подключена", r.Model)
	}
	timeout := time.Duration(r.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// наружу не уходит: контекст собран через outsideCut и ClosedAccounts
	answer, err := llm.Stream(cctx, provider, prompt, e.streamInto(chatID))
	if err == nil {
		e.noteRole(RoleChat, r.Model)
		// модель снова отвечает — снять передышку и уведомление, как это
		// делает AskRole. Без этого «Роль «Чаты»: модель не отвечает»
		// висело до вечера, хотя модель весь день отвечала в чате
		e.clearRoleFails(RoleChat)
		return e.finishWithScripts(ctx, chatID, provider, prompt, answer, timeout), nil
	}
	log.Printf("роль %q: модель %s не справилась: %v", RoleChat, r.Model, err)
	e.noteRoleFail(RoleChat, r.Model, err)
	if r.Spare == "" || r.Spare == roleNoneName {
		return "", humanModelErr(err)
	}
	spare := e.provider(r.Spare)
	if spare == nil {
		return "", humanModelErr(err)
	}
	sctx, scancel := context.WithTimeout(ctx, timeout)
	defer scancel()
	// наружу не уходит: контекст собран по худшей из двух моделей (chatCloud)
	answer, serr := llm.Stream(sctx, spare, prompt, e.streamInto(chatID))
	if serr != nil {
		return "", humanModelErr(serr)
	}
	e.noteRole(RoleChat, r.Spare+" (запасная)")
	return e.finishWithScripts(ctx, chatID, spare, prompt, answer, timeout), nil
}

// chatCloud — можно ли контексту чата закрытое: считается по худшей из
// двух моделей роли. Отказ доверенной основной уводил тот же промпт
// облачной запасной — с закрытыми ящиками и секретным внутри.
func (e *Engine) chatCloud(model string) bool {
	if e.ModelIsCloud(model) {
		return true
	}
	r := e.Role(RoleChat)
	return r.Spare != "" && r.Spare != roleNoneName && e.ModelIsCloud(r.Spare)
}

// streamInto — куда складывать кусочки ответа, пока он пишется.
func (e *Engine) streamInto(chatID int64) func(string) {
	return func(delta string) {
		e.thinkingMu.Lock()
		if e.partial == nil {
			e.partial = map[int64]string{}
		}
		e.partial[chatID] += delta
		e.thinkingMu.Unlock()
		e.Changed(store.ScreenChat(chatID))
	}
}

// ChatReply — синхронный вариант (используют задачи по расписанию).
func (e *Engine) ChatReply(ctx context.Context, chatID int64, userText string) error {
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return nil
	}
	if err := e.store.AddChatMsg(chatID, "user", userText); err != nil {
		return err
	}
	// первый вопрос становится названием чата
	if msgs, err := e.store.ChatMsgs(chatID, 2); err == nil && len(msgs) == 1 && chatIsUnnamed(e, chatID) {
		title := userText
		if len([]rune(title)) > 40 {
			title = string([]rune(title)[:40]) + "…"
		}
		_ = e.store.RenameChat(chatID, title)
	}

	// тот же путь, что и у обычного чата: скрипты, команды, таймеры
	// выполняются, а не остаются текстом в переписке
	e.thinkingMu.Lock()
	if e.thinking == nil {
		e.thinking = map[int64]bool{}
	}
	e.thinking[chatID] = true
	e.thinkingMu.Unlock()
	e.Changed(store.ScreenChat(chatID))
	// слова хозяина (админ-чат Telegram, задача, которую он завёл): ссылки
	// читает ядро до модели, как и в обычном чате
	e.readMessageLinks(ctx, chatID, userText)
	answer, err := e.chatAnswerStream(ctx, chatID, userText)
	if err != nil {
		answer = "Не получилось ответить: " + err.Error()
	}
	answer = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(answer), "Sema:"))
	e.thinkingMu.Lock()
	delete(e.thinking, chatID)
	delete(e.partial, chatID)
	delete(e.partialSkip, chatID)
	e.thinkingMu.Unlock()
	e.Changed(store.ScreenChat(chatID))
	if answer != "" {
		if err := e.store.AddChatMsg(chatID, "assistant", answer); err != nil { // в тред уже выбран
			return err
		}
	}
	e.flushCards(chatID)
	return nil
}

func (e *Engine) chatAnswer(ctx context.Context, chatID int64, userText string) (string, error) {
	prompt, err := e.chatPrompt(ctx, chatID)
	if err != nil {
		return "", err
	}
	// роль знает свою модель, запасную и таймаут
	// наружу не уходит: контекст собран через outsideCut и ClosedAccounts, секретное вырезано
	answer, by, err := e.AskRole(ctx, RoleChat, prompt)
	if err != nil {
		return "", err
	}
	log.Printf("ответ в чате %d получен моделью %s", chatID, by)
	return answer, nil
}

// Пределы на реплику в промпте — в рунах. Старые реплики режутся, чтобы
// длинная переписка не съедала контекст; последняя не режется почти
// никогда: это вопрос, на который отвечаем.
const (
	historyLimit = 1500
	lastMsgLimit = 12000
)

// lastUserIndex — номер последней реплики человека в переписке; нет ни
// одной — последняя строка, как раньше.
func lastUserIndex(history []*store.ChatMsg) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return i
		}
	}
	return len(history) - 1
}

// clipHistory — реплика в промпт: предел по рунам, обрез назван словами,
// чтобы модель не считала обрывок целым сообщением.
func clipHistory(content string, limit int) string {
	r := []rune(content)
	if len(r) <= limit {
		return content
	}
	return string(r[:limit]) + "\n… (сообщение обрезано: было " + strconv.Itoa(len(r)) + " знаков)"
}

// chatPrompt собирает системную часть, RAG-контекст и историю диалога.
func (e *Engine) chatPrompt(ctx context.Context, chatID int64) (string, error) {
	history, err := e.store.ChatMsgs(chatID, 12)
	if err != nil {
		return "", err
	}
	// результат закрытого сервера MCP облачной модели не идёт ни в историю, ни
	// в поисковый запрос, собранный из хвоста разговора
	mcpCloud := e.chatCloudOf(chatID)
	closedMCP := e.mcpClosed(mcpCloud)
	// последняя в переписке карточка отказа по аргументам — просьба исправить
	// вызов; читается до того, как карточки в истории станут строками для модели
	mcpRetry := e.mcpRetryNote(chatID, history, closedMCP)
	resultLimit := e.MCPChatLimits().Result
	for _, m := range history {
		if m.Role == "tool" {
			m.Content = mcpCardForModel(hideClosedMCP(m.Content, closedMCP), resultLimit)
		}
	}
	// для поиска берём не только последнюю реплику: на «а в каком файле?»
	// сам по себе вопрос ничего не находит, нужен хвост разговора
	userText := retrievalQuery(history)
	// файлы, присланные в этот чат, — всегда в контексте
	if refs := e.chatFileRefs(chatID); refs != "" {
		userText += " " + refs
	}
	// разговор заведён от письма — тема письма в запросе, чтобы оно само
	// оказалось в контексте, а не «где-то рядом»
	if refs := e.chatMsgRefs(chatID); refs != "" {
		userText += " " + refs
	}

	var b strings.Builder
	b.WriteString(chatSystem + "\n")
	b.WriteString(assignHint())
	if e.ChatScriptsEnabled() {
		b.WriteString(e.chatScriptsPrompt())
		b.WriteString(e.installedModulesPrompt(userText))
	}
	if c, err := e.store.Chat(chatID); err == nil {
		b.WriteString(effortPrompt(c.Effort))
	}
	if m, _ := e.ChatSettings(chatID); true {
		b.WriteString(e.memoryPrompt(m))
	}
	// скиллы говорят, что делать, блок серверов — чем
	if sp, used := e.skillsPrompt(userText); sp != "" {
		b.WriteString(sp)
		if len(used) > 0 {
			log.Printf("чат %d: подмешаны скиллы %s", chatID, strings.Join(used, ", "))
			e.noteSkillsUsed(chatID, used)
		}
	}
	b.WriteString(e.sshPrompt())
	// серверы MCP: опись всегда, полный список инструментов — когда вопрос о том,
	// что на сервере хранится. Закрытое облачной модели вырезается здесь же
	b.WriteString(e.mcpChatPrompt(ctx, chatID, userText, mcpCloud))
	b.WriteString(e.tgPrompt())
	b.WriteString(e.tgSendPrompt())
	b.WriteString(e.tgChatPrompt(chatID))
	b.WriteString(e.searchPromptHint())
	b.WriteString(e.Inventory())
	// память раньше писем: если вопрос уже решали, в промпт идут кристалл и
	// факты, а письма и чеки не собираются заново — разница в журнале
	model, _ := e.ChatSettings(chatID)
	memBlock, recalled, answered := e.memoryContext(ctx, chatID, userText, model)
	b.WriteString(memBlock)
	// модули знают своё: их вклад идёт сразу за памятью
	if modBlock, _ := e.modulesContext(ctx, userText, e.chatCloud(model)); modBlock != "" {
		b.WriteString(modBlock)
	}
	// закрытые ящики: их письма, файлы и чеки облачной модели не показываем
	closed := e.ClosedAccounts(e.chatCloud(model))
	// выписка чеков за период теперь приходит из контекста модуля «Чеки»
	receipts := ""
	letters := e.ragContext(ctx, userText, closed, e.chatCloud(model))
	saved, note := 0, ""
	if answered {
		saved = tokensOf(letters)
		letters = ""
		note = "письма не собирались"
		for _, m := range recalled.Items {
			if m.Kind == store.MemCrystal {
				saved += tokensOf(receipts)
				receipts, note = "", "письма и чеки не собирались"
				break
			}
		}
	}
	if len(recalled.Items) > 0 || len(recalled.Graph) > 0 || len(recalled.Closed) > 0 {
		e.LogRecall(e.chatOrigin(chatID), recalled, saved, note)
	}
	// сводка по чекам идёт раньше писем: на вопрос «сколько за август»
	// отвечают именно разобранные суммы, а не похожие письма
	b.WriteString(receipts)
	b.WriteString(letters)
	b.WriteString(closedNotice(closed))
	// страницы по ссылкам из вопроса: их прочитало ядро, а не скрипт модели
	b.WriteString(e.linkPagesPrompt(history, e.chatCloud(model)))

	// заметка сработавшего таймера — модели, а не в переписку: в чате уже есть карточка
	e.pendingMu.Lock()
	timerNote := e.chatNotes[chatID]
	delete(e.chatNotes, chatID)
	e.pendingMu.Unlock()
	if timerNote != "" {
		b.WriteString("\nTimer fired. Your note: " + timerNote + "\nDo the check now (commands as sema-ssh blocks) and continue; the user did not write anything.\n")
	}
	b.WriteString(e.threadContext(chatID))
	// поручение этого чата — в каждом ответе: на «как дела?» модель
	// отвечает по шагам, а в ход ядра получает указание сделать шаг
	b.WriteString(e.assignmentContext(ctx, chatID))
	b.WriteString("\nConversation:\n")
	// щедрый предел — последней реплике человека, а не последней строке
	// переписки: 11 октября 2026 ссылка в задании на 6500 знаков дала
	// заметку «страницу прочитать не удалось» после вопроса, и само задание
	// ушло модели обрезанным до 1500 знаков
	lastUser := lastUserIndex(history)
	for i, m := range history {
		role := "User"
		switch m.Role {
		case "assistant":
			role = "Sema"
		case "note":
			role = "System" // служебная вводная: не человек, но и не Сёма
		}
		// предел по рунам, а не по байтам: 1500 байт — это 750 русских
		// букв, и длинная схема обрывалась на середине. Последняя реплика
		// — тот самый вопрос, на который отвечаем: её не режем почти
		// никогда, а обрез называем словами, чтобы модель не додумывала
		limit := historyLimit
		if i == lastUser {
			limit = lastMsgLimit
		}
		fmt.Fprintf(&b, "%s: %s\n", role, clipHistory(m.Content, limit))
	}
	b.WriteString(mcpRetry)
	b.WriteString("Sema:")
	return b.String(), nil
}

// RAGContext — тот же контекст, что уходит активной модели; открыт для проверки.
func (e *Engine) RAGContext(ctx context.Context, query string) string {
	return e.RAGContextFor(ctx, query, e.ActiveProvider())
}

// RAGContextFor — контекст глазами конкретной модели: облачная не видит
// закрытые ящики, и это можно проверить, не имея её ключа.
func (e *Engine) RAGContextFor(ctx context.Context, query, model string) string {
	closed := e.ClosedAccounts(e.chatCloud(model))
	modBlock, _ := e.modulesContext(ctx, query, e.chatCloud(model))
	return e.Inventory() + modBlock + e.ragContext(ctx, query, closed, e.chatCloud(model)) + closedNotice(closed)
}

// ragContext — блок контекста из писем и файлов, ближайших к запросу.
func (e *Engine) ragContext(ctx context.Context, query string, closed map[string]bool, cloud bool) string {
	// вектор вопроса — лучшее, что есть, но не условие: файл, названный в
	// вопросе по имени или номеру, идёт в контекст и без эмбеддера. Иначе
	// присланный в чат чек пропадал, стоило LM Studio на маке уснуть, а
	// модель честно отвечала «содержимое мне не передано»
	var vec []float32
	if e.embedder != nil {
		if v, err := e.embedIfAllowed(ctx, query); err == nil {
			vec = v
		}
	}
	var b strings.Builder
	secret, sensitive := 0, 0
	if vec == nil {
		files, fs, fsens := e.attContext(query, nil, 4, closed, cloud)
		b.WriteString(files)
		b.WriteString(cutNotice(fs, fsens, nil))
		return b.String()
	}
	var senders []string
	nn, err := e.nearestOpen(vec, e.AskTopK(), closed)
	if err == nil && len(nn) > 0 {
		ids := make([]int64, len(nn))
		for i, n := range nn {
			ids[i] = n.msgID
		}
		if msgs, err := e.store.MessagesByIDs(ids); err == nil {
			var lines []string
			for _, m := range msgs {
				// личное облачной модели не показываем: класс письма
				// считается один раз и правится человеком
				if give, why := e.outsideCut(cloud, e.ClassOfMessage(m), m.FromAddr); !give {
					if why == "секретное" {
						secret++
					} else {
						sensitive++
						e.notifyConsent(m.FromAddr)
					}
					senders = append(senders, m.FromAddr)
					continue
				}
				lines = append(lines, fmt.Sprintf("Email %d (%s): From %s — %s\n%s\n",
					len(lines)+1, m.Date.Format("2006-01-02"), m.FromAddr, m.Subject, clip(m.Snippet, 400)))
			}
			if len(lines) > 0 {
				b.WriteString("\nRelevant emails from the mailbox:\n")
				for _, l := range lines {
					b.WriteString(l)
				}
			}
		}
	}
	files, fs, fsens := e.attContext(query, vec, 4, closed, cloud)
	b.WriteString(files)
	secret += fs
	sensitive += fsens
	b.WriteString(cutNotice(secret, sensitive, uniqStrings(senders)))
	return b.String()
}

// uniqStrings — имена без повторов, порядок сохраняется.
func uniqStrings(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// TaskChat — чат, привязанный к задаче (создаётся при первом запуске).
func (e *Engine) TaskChat(t *store.Task) (int64, error) {
	if t.ChatID != 0 {
		if _, err := e.store.Chat(t.ChatID); err == nil {
			return t.ChatID, nil
		}
	}
	id, err := e.store.CreateChat("Задача: " + t.Title)
	if err != nil {
		return 0, err
	}
	return id, e.store.SetTaskChat(t.ID, id)
}

// retrievalQuery — текст, по которому ищем письма и файлы: последний вопрос
// плюс то, о чём говорили только что.
func retrievalQuery(history []*store.ChatMsg) string {
	var parts []string
	users := 0
	for i := len(history) - 1; i >= 0 && len(parts) < 3; i-- {
		m := history[i]
		if m.Role == "user" {
			if users == 0 || len([]rune(m.Content)) > 25 {
				parts = append(parts, clip(m.Content, 300))
			}
			users++
			continue
		}
		if users == 1 && len(parts) < 2 {
			parts = append(parts, clip(m.Content, 200)) // ответ Сёмы тоже задаёт тему
		}
	}
	return strings.Join(parts, "\n")
}

// Уровни размышления. Единого способа просить модель думать дольше нет:
// у локальных и облачных он разный, поэтому просим словами — это работает
// везде и ничего не ломает, если модель такого не умеет.
const (
	EffortFast   = "думает быстро"
	EffortNormal = "думает обычно"
	EffortDeep   = "думает подробно"
)

func EffortLevels() []string { return []string{EffortFast, EffortNormal, EffortDeep} }

func effortPrompt(level string) string {
	switch level {
	case EffortFast:
		return "\nAnswer briefly and directly. Do not reason out loud. /no_think\n"
	case EffortDeep:
		return "\nThink it through step by step before answering: check the context carefully, " +
			"compare the sources and only then give the answer. /think\n"
	default:
		return ""
	}
}

// reasoningEffort — уровень размышления чата как reasoning_effort у OpenAI.
// «Обычно» — пусто: модель решает сама, лишний параметр не нужен.
func reasoningEffort(level string) string {
	switch level {
	case EffortFast:
		return "low"
	case EffortDeep:
		return "high"
	}
	return ""
}

// ChatSettings — модель и уровень конкретного чата для интерфейса.
func (e *Engine) ChatSettings(chatID int64) (model, effort string) {
	c, err := e.store.Chat(chatID)
	if err != nil {
		return e.Role(RoleChat).Model, EffortNormal
	}
	model, effort = c.Model, c.Effort
	if model == "" {
		model = e.Role(RoleChat).Model
	}
	if effort == "" {
		effort = EffortNormal
	}
	return model, effort
}

// finishWithScripts исполняет блоки ответа модели и, пока ей есть что
// вернуть, даёт следующий ход — шаги читающих вызовов (chat_steps.go).
// Блоки разбираются одинаково на каждом ходу. Следующий ход случается, когда
// есть результаты для модели, ходы ещё остались (`chats.read_steps`) и — со
// второго хода — ничего не ждёт кнопки человека: первый ход по своим
// результатам модель получает всегда, как было и до шагов. Ответ, который
// обещает действие без единого блока, один раз возвращается модели.
func (e *Engine) finishWithScripts(ctx context.Context, chatID int64, p llm.Provider,
	prompt, answer string, timeout time.Duration) string {

	log.Printf("чат %d: ответ модели %d символов", chatID, len([]rune(answer)))
	ctx, turn := withChatTurn(ctx)
	steps := e.chatReadSteps()
	transcript := prompt
	reminded := false
	// outward — в этом ответе было дело снаружи или то, что ждёт кнопки:
	// после него слова «создана», «на подтверждении» законны. Любой блок
	// делом не считается: скрипт поиска файла не заводит задачу
	outward := false
	prevRaw := ""
	for step := 1; ; step++ {
		raw := answer
		turn.pending = 0
		// ход дословно повторяет прежний — модель ходит по кругу: блоки не
		// исполняются второй раз, человеку уходит текст без них
		if step > 1 && strings.TrimSpace(raw) == strings.TrimSpace(prevRaw) {
			log.Printf("чат %d: модель повторила прежний ход — шаги закончены", chatID)
			return trimSpeakerPrefix(stripBlocks(raw))
		}
		prevRaw = raw
		// правила и модули модель предлагает, а не применяет: подтверждает человек;
		// память пишется по режиму области — карточка остаётся в переписке
		// сравнение без краёв: findRules обрезает пробелы всегда, и хвостовой
		// перевод строки живой модели иначе сходил бы за предложение
		proposals := strings.TrimSpace(answer)
		answer = e.findRules(chatID, answer)
		answer = e.findSkillBlocks(chatID, answer)
		if strings.TrimSpace(answer) != proposals {
			outward = true // правило или скилл ждут кнопки человека
		}
		answer = e.findTimers(chatID, answer)
		// план и итоги шагов поручения — до «готово»: последний шаг
		// закрывает поручение сам, а sema-done закрывает его досрочно
		beforeAssign := strings.TrimSpace(answer)
		answer = e.findAssignBlocks(ctx, chatID, answer)
		if strings.TrimSpace(answer) != beforeAssign {
			outward = true // поручение заводит задачу в трекере само
		}
		answer = e.findDone(ctx, chatID, answer)
		answer = e.findMemoryBlocks(ctx, chatID, answer)
		// модуль Сёма часто присылает не первым ходом, а после команд: 19
		// сентября 2026 правка модуля «Серверы под присмотром» во втором ходе
		// осталась сырым кодом в переписке, — поэтому разбор на каждом ходу
		if withoutModule, proposed := e.findModule(answer); proposed != nil {
			if proposed.Err != "" {
				if strings.TrimSpace(withoutModule) == "" {
					return "Собрал модуль, но он не разобрался: " + proposed.Err +
						" Попросите переписать — или скажите, что поправить."
				}
				return trimSpeakerPrefix(strings.TrimSpace(withoutModule + "\n\nМодуль не встал: " + proposed.Err))
			}
			outward = true
			previous, was := e.Module(proposed.Module.Name)
			e.proposeModule(chatID, proposed)
			// модуль поручения просит секреты, которых нет: поручение ждёт
			e.assignNeedsSecrets(ctx, chatID, proposed.Module)
			if strings.TrimSpace(withoutModule) == "" {
				// ответ из одного блока: сказать правду о том, что случилось, —
				// обновлённый включённый модуль не «установлен выключенным»
				switch {
				case was && previous.Enabled:
					return "Обновил модуль «" + proposed.Module.Title + "» — он работает, изменения в карточке выше."
				case was:
					return "Обновил модуль «" + proposed.Module.Title + "» — он остаётся выключенным, включите на экране «Модули»."
				}
				return "Предложил модуль «" + proposed.Module.Title +
					"» — он установлен выключенным, посмотрите права на экране «Модули» и включите, если подходит."
			}
			// остальные блоки того же ответа выполняются как обычно: 17
			// сентября 2026 модуль ставился, а скрипты и поиск рядом с ним
			// молча оставались сырым кодом в переписке
			answer = withoutModule
		}
		// карточки этого хода; `=`, а не `:=`: иначе ответ следующего хода лёг
		// бы в тень переменной цикла, и цикл разбирал бы первый ответ снова
		var sshCards, searchCards, mcpCards, tgCards, cards []ToolCard
		// команды на серверах: чтение выполняется сразу, остальное ждёт человека
		answer, sshCards = e.runChatSSH(ctx, chatID, answer)
		// поиск в интернете: ответ поисковой модели — данные для следующего хода
		answer, searchCards = e.runChatSearch(ctx, chatID, answer)
		// инструменты чужих серверов MCP: читающее — сразу, меняющее ждёт кнопки
		answer, mcpCards = e.runChatMCP(ctx, chatID, answer)
		// сообщение в Telegram: предлагает Сёма, отправляет человек кнопкой
		answer, tgCards = e.runChatTG(ctx, chatID, answer)
		clean, cards := e.runChatScripts(ctx, chatID, answer)
		if step > 1 {
			clean = trimSpeakerPrefix(clean)
		}
		// делом считается то, что ждёт кнопки (счётчик хода, Telegram), и
		// выполненный изменяющий вызов MCP. Прочитанный список и отказ — нет:
		// «посмотрел список проектов» не делает правдой «создал задачу»
		if turn.pending > 0 || len(tgCards) > 0 {
			outward = true
		}
		for _, c := range mcpCards {
			if c.MCP != nil && c.MCP.Status == "executed" && !mcpReadingTool(c.MCP.Tool) {
				outward = true
			}
		}
		for _, c := range cards {
			if outwardCard(c) {
				outward = true
			}
		}

		results := len(cards)+len(sshCards)+len(searchCards)+len(mcpCards) > 0
		waits := turn.pending > 0 || len(tgCards) > 0
		left := steps - step
		if !results || left <= 0 || (step > 1 && waits) {
			// обещание без действия: «Хорошо, сделаем» без блока человеку не
			// уходит, модель один раз получает напоминание (4 октября 2026 так
			// остался заглушкой модуль «Дневник»)
			markers := e.promiseMarkers()
			// «сделано» без дела: «отправлено на подтверждение» в ответе, где
			// не было ни одного блока, — значит, не было ничего (4 октября
			// 2026 так «завёлся» баг в Planner). После настоящих вызовов в
			// этом же ответе слова «создана» законны и не проверяются
			// Проверка «сделано» не смотрит, были ли в этом ходе блоки: 4
			// октября 2026 модель нашла скриптом фото и в том же ответе
			// написала, что баг ушёл на подтверждение
			claim := !outward && isPromise(clean, e.claimMarkers()) && e.answersHuman(chatID)
			if (!hasBlocks(raw) && isPromise(clean, markers)) || claim {
				if !reminded && left > 0 {
					reminded = true
					note := promiseReminder
					if claim {
						note = claimReminder
						log.Printf("чат %d: ответ говорит «сделано» без блока — напоминаю модели", chatID)
					} else {
						log.Printf("чат %d: ответ обещает действие без блока — напоминаю модели", chatID)
					}
					e.setStepPartial(chatID, "")
					transcript += "\nSema: " + clean + note
					next, err := e.chatNextTurn(ctx, chatID, p, transcript, timeout)
					if err == nil {
						answer = trimSpeakerPrefix(next)
						continue
					}
					log.Printf("чат %d: напоминание об обещанном: %v", chatID, err)
				} else if reminded {
					log.Printf("чат %d: обещанное не сделано и после напоминания", chatID)
				}
			}
			if strings.TrimSpace(clean) == "" && len(tgCards) > 0 {
				return "Сообщение в Telegram ждёт вашей кнопки — карточка ниже."
			}
			return clean
		}

		// частичный ответ до блока кода в чате уже не нужен: он будет заменён
		e.setStepPartial(chatID, clean)
		var res strings.Builder
		if len(cards) > 0 {
			res.WriteString(scriptResultsPrompt(cards))
		}
		if len(sshCards) > 0 {
			res.WriteString(sshResultsPrompt(sshCards))
		}
		if len(searchCards) > 0 {
			res.WriteString(searchResultsPrompt(searchCards))
		}
		if len(mcpCards) > 0 {
			res.WriteString(e.mcpResultsPrompt(chatID, mcpCards))
		}
		res.WriteString(stepsNote(left - 1))
		fallback := func() string {
			if clean != "" {
				return clean
			}
			if len(cards) > 0 {
				return "Скрипт выполнен: " + cards[0].Result
			}
			if len(mcpCards) > 0 && len(sshCards) == 0 && len(searchCards) == 0 && len(tgCards) == 0 {
				return "Вызов выполнен, результат в карточке выше."
			}
			return "Команда выполнена, вывод в карточке выше."
		}
		// каждый ход стоит денег: бюджет проверяется перед каждым
		if err := e.budgetCheck(ctx); err != nil {
			log.Printf("чат %d: следующий ход не сделан: %v", chatID, err)
			return fallback()
		}
		// наружу не уходит: промпт первого хода плюс результаты после масок секретов
		transcript += "\nSema: " + clean + "\n" + res.String() + "\nSema:"
		next, err := e.chatNextTurn(ctx, chatID, p, transcript, timeout)
		if err != nil {
			log.Printf("ответ по результатам: %v", err)
			return fallback()
		}
		answer = trimSpeakerPrefix(next)
	}
}

// chatNextTurn — следующий ход модели в том же ответе, с потоком кусочков.
func (e *Engine) chatNextTurn(ctx context.Context, chatID int64, p llm.Provider, transcript string, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return llm.Stream(cctx, p, transcript, e.streamInto(chatID))
}

// setStepPartial — что уже видно из ответа перед следующим ходом: новый ход
// пишется поверх, и снаружи (Telegram) прежнего хода уже не видно.
func (e *Engine) setStepPartial(chatID int64, clean string) {
	e.thinkingMu.Lock()
	if e.partial == nil {
		e.partial = map[int64]string{} // модель без потока кусочков карту не завела
	}
	e.partial[chatID] = clean
	if e.partialSkip == nil {
		e.partialSkip = map[int64]int{}
	}
	e.partialSkip[chatID] = len(clean)
	e.thinkingMu.Unlock()
	e.Changed(store.ScreenChat(chatID))
}

// trimSpeakerPrefix убирает «Sema:» и «Сёма:» в начале: модель иногда
// повторяет метку говорящего из промпта.
func trimSpeakerPrefix(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"Sema:", "Сема:", "Сёма:"} {
		s = strings.TrimSpace(strings.TrimPrefix(s, p))
	}
	return s
}
