package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// attachmentWorker постоянно вытягивает вложения из писем — своим
// соединением, не дожидаясь очередного опроса почты: файлы должны осесть
// локально до того, как письмо удалят с сервера.
func (e *Engine) scanAllAccounts(ctx context.Context) int {
	e.workers.begin(WorkerAttachments)
	defer func() { e.workers.end(WorkerAttachments, "") }()
	total := 0
	for _, acct := range e.Accounts() {
		sess, err := e.dialAccount(acct)
		if err != nil {
			log.Printf("attachments dial %s: %v", acct.Name, err)
			continue
		}
		total += e.scanAttachments(ctx, sess, acct.Name)
		sess.Close()
	}
	return total
}

// scanAttachments разбирает порцию писем: скачивает вложения, извлекает
// текст, распознаёт чеки и кладёт всё в локальную базу.
func (e *Engine) scanAttachments(ctx context.Context, sess *mail.Session, account string) int {
	msgs, err := e.store.UnscannedMessages(account, 20)
	if err != nil || len(msgs) == 0 {
		return 0
	}
	maxSize := int64(e.FilesMaxMB()) << 20
	for _, m := range msgs {
		prefer := []string{"INBOX"}
		switch m.Status {
		case store.StatusConfirmed, store.StatusAutoMoved, store.StatusQueued:
			prefer = []string{sess.JunkFolder(), "INBOX"}
		}
		mailbox, uid, found := sess.Locate(m.MessageID, prefer...)
		if !found {
			_ = e.store.MarkAttScanned(m.ID) // письма нет в почте — искать нечего
			continue
		}
		atts, err := sess.FetchAttachmentsIn(mailbox, uid, maxSize)
		if err != nil {
			log.Printf("attachments of %q: %v", m.Subject, err)
			continue // не помечаем — попробуем в следующий опрос
		}
		for _, a := range atts {
			if err := e.processAttachment(ctx, m, a); err != nil {
				log.Printf("attachment %q: %v", a.Filename, err)
			}
		}
		_ = e.store.MarkAttScanned(m.ID)
	}
	return len(msgs)
}

func (e *Engine) processAttachment(ctx context.Context, m *store.Message, a *mail.Attachment) error {
	account := m.Account
	if account == "" {
		account = e.conf.Mail.Name
	}
	_, _, err := e.AddFile(ctx, a.Filename, a.MIME, a.Data,
		FileSource{Kind: store.SourceMail, Ref: strconv.FormatInt(m.ID, 10), Account: account}, m)
	return err
}

var havePdftotext = func() bool {
	_, err := exec.LookPath("pdftotext")
	return err == nil
}()

// extractText достаёт текст из вложения: PDF через pdftotext, картинки —
// через OCR, офисные документы Сёма разбирает сама, текстовые форматы
// читает напрямую. Чем именно — видно в настройках, вкладка «Система».
func extractText(ctx context.Context, path, mime string, data []byte) string {
	switch {
	case mime == "application/pdf" || strings.HasSuffix(strings.ToLower(path), ".pdf"):
		if !havePdftotext {
			return ""
		}
		out, err := extractCommand(ctx, "pdftotext", "-layout", "-q", path, "-")
		if err != nil {
			return ""
		}
		return clip(normalizeWS(string(out)), 20000)
	case isImage(path, mime):
		return "" // картинки читает e.extract: сначала модель, потом OCR
	case isOffice(path, mime):
		return clip(normalizeWS(officeText(data)), 20000)
	case isHTML(path, mime):
		return clip(normalizeWS(htmlText(data)), 20000)
	case isPlainText(path, mime):
		return clip(normalizeWS(string(data)), 20000)
	default:
		return ""
	}
}

var (
	reWS     = regexp.MustCompile(`[ \t]+`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
	reUnsafe = regexp.MustCompile(`[^\wа-яА-ЯёЁ.\-]+`)
)

func normalizeWS(s string) string {
	s = reWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(reBlank.ReplaceAllString(s, "\n\n"))
}

func safeName(name string) string {
	name = filepath.Base(name)
	return reUnsafe.ReplaceAllString(name, "_")
}

// clip режет по символам: в русском тексте байт вдвое больше букв, и счёт
// по байтам незаметно обрезал документы посередине нужного места.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func extractJSONBlock(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

// similarAttachments — ближайшие по смыслу файлы к вектору вопроса, не
// больше k, из открытых ящиков.
func (e *Engine) similarAttachments(vec []float32, k int, closed map[string]bool) []*store.Attachment {
	rows, err := e.store.AttEmbeddings()
	if err != nil || len(rows) == 0 {
		return nil
	}
	type cand struct {
		id  int64
		sim float64
	}
	cands := make([]cand, 0, len(rows))
	for _, r := range rows {
		cands = append(cands, cand{r.MsgID, embed.Cosine(vec, embed.DecodeVec(r.Vector))})
	}
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].sim > cands[i].sim {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	// берём с запасом: часть кандидатов может оказаться из закрытых ящиков
	if len(cands) > k*4 {
		cands = cands[:k*4]
	}
	ids := make([]int64, len(cands))
	for i, c := range cands {
		ids[i] = c.id
	}
	atts, err := e.store.AttachmentsByIDs(ids)
	if err != nil {
		return nil
	}
	atts = openAttachments(atts, closed)
	if len(atts) > k {
		atts = atts[:k]
	}
	return atts
}

// attContext — контекст из вложений для «Спросить Сёму»: ближайшие по
// смыслу файлы (чеки — с суммами) и файлы, названные в вопросе.
func (e *Engine) attContext(query string, vec []float32, k int, closed map[string]bool, cloud bool) (text string, secret, sensitive int) {
	var atts []*store.Attachment
	// похожие по смыслу — только когда есть вектор вопроса; названные в
	// вопросе файлы — всегда, вектор им не нужен
	if vec != nil {
		atts = e.similarAttachments(vec, k, closed)
	}
	// файлы, названные в самом вопросе, идут первыми и всегда
	atts = append(openAttachments(e.namedFiles(query, atts), closed), atts...)
	if len(atts) == 0 {
		return "", 0, 0
	}
	// личное облачной модели не показываем
	var open []*store.Attachment
	for _, a := range atts {
		if give, why := e.outsideCut(cloud, e.ClassOfFile(a), a.FromAddr); !give {
			if why == "секретное" {
				secret++
			} else {
				sensitive++
			}
			continue
		}
		open = append(open, a)
	}
	atts = open
	if len(atts) == 0 {
		return "", secret, sensitive
	}
	ids := make([]int64, len(atts))
	for i, a := range atts {
		ids[i] = a.ID
	}
	codes := e.store.FileCodesFor(ids)
	var b strings.Builder
	b.WriteString("\nAttached files from the mailbox:\n")
	for _, a := range atts {
		// без номеров: модель должна называть файл по имени, а не «File 1»
		fmt.Fprintf(&b, "File «%s» (from %s, %s)", a.Filename, a.FromAddr, a.Date.Format("2006-01-02"))
		if a.Kind == store.AttKindReceipt {
			fmt.Fprintf(&b, " [RECEIPT: %s, %.2f %s]", a.Vendor, a.Amount, a.Currency)
		}
		b.WriteString("\n" + a.Summary + "\n" + excerpt(a.Extracted, query, 2000) + "\n")
		// коды отдельной строкой, а не вперемешку с текстом: это точные
		// числа, и модель должна видеть, что они добыты не чтением букв
		if line := CodesForModel(codes[a.ID]); line != "" {
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	return b.String(), secret, sensitive
}

// DeleteAttachment удаляет локальную копию файла: сам файл с диска и
// запись о нём. Письмо и его текст остаются в архиве Сёмы.
func (e *Engine) DeleteAttachment(id int64) error {
	atts, err := e.store.AttachmentsByIDs([]int64{id})
	if err != nil {
		return err
	}
	if len(atts) == 0 {
		return nil
	}
	if err := os.Remove(atts[0].Path); err != nil && !os.IsNotExist(err) {
		log.Printf("удаление файла %s: %v", atts[0].Path, err)
	}
	return e.store.DeleteAttachment(id)
}

// openAttachments отбрасывает файлы из ящиков, закрытых для модели.
func openAttachments(atts []*store.Attachment, closed map[string]bool) []*store.Attachment {
	if len(closed) == 0 {
		return atts
	}
	var out []*store.Attachment
	for _, a := range atts {
		if !closed[a.Account] {
			out = append(out, a)
		}
	}
	return out
}

// extractHint объясняет в карточке файла, почему текст не извлечён и что
// для этого поставить — то же самое перечислено в настройках.
func extractHint(path, mime string) string {
	switch {
	case isImage(path, mime) && !haveTesseract:
		return "картинку не прочитали: модель не видит картинки, OCR не установлен " +
			"(brew install tesseract tesseract-lang)"
	case isImage(path, mime):
		return "картинка распознана, но текста на ней не нашлось"
	case isAudio(path, mime):
		return "аудиозапись: речь не расшифровывалась — Сёма расшифровывает загруженное вами и то, о чём попросите в чате; " +
			"записи из почты и папок сам не берёт (деньги и чужие голоса)"
	case strings.HasSuffix(strings.ToLower(path), ".pdf") && !havePdftotext:
		return "PDF не прочитан — нужен pdftotext: brew install poppler"
	default:
		return "текст не извлечён (" + mime + ")"
	}
}

// extract — единая точка разбора вложения: картинки идут к модели (наш OCR
// отправляется ей же подсказкой), остальное читается на месте. Третьим
// возвращаются коды: на картинке они читаются тем же проходом, и просить
// их отдельно значило бы второй раз развернуть ту же фотографию.
func (e *Engine) extract(ctx context.Context, path, mime string, data []byte) (text, via string, codes []store.FileCode) {
	if isImage(path, mime) {
		text, via = e.imageText(ctx, path, data, mime)
		return text, via, e.ImageCodes(ctx, path, data, 0)
	}
	text = extractText(ctx, path, mime, data)
	// PDF без текстового слоя (скан с телефона, фото счёта в PDF): pdftotext
	// молчит — рисуем страницы и читаем их как картинки
	if isPDF(path, mime) && len(strings.Fields(text)) < 3 {
		if t, v, c := e.pdfPagesText(ctx, path); strings.TrimSpace(t) != "" || len(c) > 0 {
			return t, v, c
		}
	}
	return text, "", nil
}

func isPDF(path, mime string) bool {
	return strings.Contains(mime, "pdf") || strings.ToLower(filepath.Ext(path)) == ".pdf"
}

var havePdftoppm = func() bool {
	_, err := exec.LookPath("pdftoppm")
	return err == nil
}()

const pdfOCRPages = 5 // страниц на файл: чеки и счета короткие, договоры читать целиком дорого

// pdfPagesText растеризует первые страницы PDF (pdftoppm из poppler) и
// читает каждую как картинку: модель зрения или OCR — как настроено, а
// заодно коды. Страницы рисуются один раз на оба дела: на Pi растеризация
// листа дороже всего остального вместе взятого.
func (e *Engine) pdfPagesText(ctx context.Context, path string) (string, string, []store.FileCode) {
	if !havePdftoppm {
		return "", "", nil
	}
	dir, err := os.MkdirTemp("", "sema-pdf-")
	if err != nil {
		return "", "", nil
	}
	defer os.RemoveAll(dir)
	prefix := filepath.Join(dir, "page")
	// PDF задаёт размер страницы сам: ограничиваем сторону картинки,
	// чтобы огромный лист не породил гигабайты растровых данных.
	if _, err := extractCommand(ctx, "pdftoppm", "-scale-to", "2400", "-png", "-l", strconv.Itoa(pdfOCRPages), path, prefix); err != nil {
		log.Printf("pdftoppm %s: %v", filepath.Base(path), err)
		return "", "", nil
	}
	pages, _ := filepath.Glob(prefix + "*.png")
	sort.Strings(pages)
	var parts []string
	var codes []store.FileCode
	// предел времени на коды — общий на весь файл, а не на страницу:
	// иначе пятистраничный скан искал бы коды в пять раз дольше обещанного
	due := time.Now().Add(e.CodesLimit())
	via := ""
	for n, pg := range pages {
		data, err := os.ReadFile(pg)
		if err != nil {
			continue
		}
		t, v := e.imageText(ctx, pg, data, "image/png")
		if strings.TrimSpace(t) != "" {
			parts = append(parts, strings.TrimSpace(t))
		}
		if v != "" {
			via = v
		}
		codes = append(codes, e.imageCodesUntil(ctx, pg, data, n+1, due)...)
	}
	if len(parts) == 0 {
		return "", "", codes
	}
	if via == "" {
		via = "OCR"
	}
	return strings.Join(parts, "\n\n"), via + " по страницам PDF", codes
}

// excerpt выбирает из документа то место, о котором спрашивают: начало файла
// плюс окно вокруг строк, где встречаются слова вопроса. Раньше в контекст
// всегда уходило только начало, и таблицы в середине документа модель не
// видела — отвечала «не извлечено», хотя текст давно был в базе.
func excerpt(text, query string, limit int) string {
	lines := strings.Split(text, "\n")
	if len([]rune(text)) <= limit {
		return text
	}
	words := queryWords(query)
	best, bestScore := 0, 0
	for i, line := range lines {
		score := 0
		low := strings.ToLower(line)
		for _, w := range words {
			if strings.Contains(low, w) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	head := clip(text, limit/2)
	if bestScore == 0 {
		return head
	}
	from := best - 4
	if from < 0 {
		from = 0
	}
	to := best + 12
	if to > len(lines) {
		to = len(lines)
	}
	window := clip(strings.Join(lines[from:to], "\n"), limit/2)
	if strings.Contains(head, window) {
		return head
	}
	return head + "\n…\n" + window
}

// queryWords — значимые слова вопроса (короткие предлоги только мешают).
func queryWords(query string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, ".,:;!?()«»\"'")
		if len([]rune(w)) >= 5 {
			out = append(out, w)
		}
	}
	return out
}

// reFileName — имя файла прямо в тексте вопроса.
var reFileName = regexp.MustCompile(`(?i)[^\s"'(<>]+(?:[ ][^\s"'(<>]+){0,4}\.(pdf|docx?|xlsx?|pptx?|png|jpe?g|csv|txt|zip)`)

// namedFiles достаёт вложения, названные в вопросе по имени: похожесть
// векторов на длинных документах ошибается, а прямое имя — нет.
func (e *Engine) namedFiles(query string, already []*store.Attachment) []*store.Attachment {
	var out []*store.Attachment
	seen := map[int64]bool{}
	for _, a := range already {
		seen[a.ID] = true
	}
	for _, name := range reFileName.FindAllString(query, 3) {
		found, err := e.store.AttachmentsByName(strings.TrimSpace(name), 2)
		if err != nil {
			continue
		}
		for _, a := range found {
			if !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, a)
			}
		}
	}
	// файл назван номером: «#271» — так его подписывает чат при вложении
	for _, m := range reFileRef.FindAllStringSubmatch(query, 5) {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		if id == 0 || seen[id] {
			continue
		}
		if a, err := e.store.AttachmentByID(id); err == nil {
			seen[a.ID] = true
			out = append(out, a)
		}
	}
	return out
}

var reFileRef = regexp.MustCompile(`#(\d{1,9})\b`)

// AttachFileToChat — файл прислали в чат: сообщение с номером файла, по
// которому он попадает в контекст ответа, и чип для клиента.
func (e *Engine) AttachFileToChat(chatID, fileID int64) error {
	a, err := e.store.AttachmentByID(fileID)
	if err != nil {
		return fmt.Errorf("файл не найден")
	}
	_ = e.store.AddFileSource(a.ID, store.SourceUpload, "chat:"+strconv.FormatInt(chatID, 10), "")
	// в чате собеседника файл прикрепляют, чтобы его отправить: он уходит
	// человеку и остаётся в переписке, а не только в архиве Сёмы
	if sent, err := e.sendFileToPeer(chatID, a); sent {
		if err != nil {
			return err
		}
		return nil
	}
	return e.store.AddChatMsg(chatID, "user", fmt.Sprintf("Прикрепил файл «%s» (#%d)", a.Filename, a.ID))
}

// sendFileToPeer — отправить файл собеседнику канала. Первое значение
// false: чат не про переписку с человеком, файл просто ложится в архив.
func (e *Engine) sendFileToPeer(chatID int64, a *store.Attachment) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	raw, err := e.FileBytes(a)
	if err != nil {
		return true, err
	}
	line := fmt.Sprintf("Отправил файл «%s» (#%d)", a.Filename, a.ID)

	if peer, ok := e.tgPeerOfChat(chatID); ok {
		cl, acc, err := e.tgClientOf(peer)
		if err != nil {
			return true, err
		}
		if !acc.CanSend {
			return true, fmt.Errorf("у учётки %s отправка от вашего имени выключена — включите её в «Настройки → Telegram»", acc.Name())
		}
		if peer.Blocked {
			return true, fmt.Errorf("чат «%s» заблокирован: Сёма его не трогает", peer.Title)
		}
		if err := cl.SendFile(ctx, peer.ID, peer.Access, peer.Kind, peer.Topic, a.Filename, a.MIME, raw, ""); err != nil {
			return true, err
		}
		log.Printf("telegram: файл «%s» ушёл в «%s» (%d КБ)", a.Filename, peer.Title, len(raw)/1024)
		return true, e.store.AddChatMsg(chatID, "user", line)
	}

	if peer, ok := e.MAXPeerOfChat(chatID); ok {
		cl, acc, err := e.maxClient()
		if err != nil {
			return true, err
		}
		defer cl.Close()
		if !acc.CanSend {
			return true, fmt.Errorf("отправка от вашего имени выключена — включите её в «Настройки → MAX»")
		}
		if peer.Blocked {
			return true, fmt.Errorf("чат «%s» заблокирован: Сёма его не трогает", peer.Title)
		}
		if _, err := cl.Login(ctx); err != nil {
			return true, err
		}
		if err := cl.SendFile(ctx, peer.ID, a.Filename, raw, ""); err != nil {
			return true, err
		}
		log.Printf("max: файл «%s» ушёл в «%s» (%d КБ)", a.Filename, peer.Title, len(raw)/1024)
		return true, e.store.AddChatMsg(chatID, "user", line)
	}
	return false, nil
}

// chatFileRefs — номера файлов, присланных в этот чат: они всегда в контексте.
func (e *Engine) chatFileRefs(chatID int64) string {
	files, err := e.store.FilesByRef(store.SourceUpload, "chat:"+strconv.FormatInt(chatID, 10))
	if err != nil || len(files) == 0 {
		return ""
	}
	var refs []string
	for _, f := range files {
		refs = append(refs, "#"+strconv.FormatInt(f.ID, 10))
	}
	return strings.Join(refs, " ")
}
