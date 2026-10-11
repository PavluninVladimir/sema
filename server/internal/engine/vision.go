package engine

// Картинки Сёма читает в первую очередь моделью: она разбирает и фото чека
// под углом, и скриншот, где OCR путает цифры. Наш tesseract при этом тоже
// отрабатывает, и его текст уходит модели вместе с картинкой — пусть сама
// решит, чему верить. В RAG кладётся то, что она вернула; если модель
// картинки не умеет или недоступна — остаётся текст OCR.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"path/filepath"
)

const visionPrompt = `Ты видишь картинку из письма. Верни JSON без пояснений:
{"text": "весь текст с картинки построчно", "kind": "чек|документ|скриншот|фото|другое"}
Если это чек или счёт — обязательно сохрани продавца, дату, позиции и суммы.
Пиши ровно то, что видно; ничего не выдумывай.`

// imageText читает картинку. Возвращает текст и то, чем он получен.
//
// Порядок важен и он обратный прежнему: сначала свой OCR, по нему класс, и
// только потом модель. Раньше картинка уходила модели первой, а класс файла
// считался по тексту, который эта же модель и вернула, — то есть решение
// «можно ли показывать» принималось после того, как показали. На фотографии
// паспорта имя файла не говорит ничего, а машиночитаемая строка в выводе
// tesseract говорит всё.
func (e *Engine) imageText(ctx context.Context, path string, data []byte, mime string) (string, string) {
	ocr := clip(normalizeWS(e.ourOCR(ctx, path)), 20000)

	if len(data) == 0 {
		data, _ = os.ReadFile(path)
	}
	show, class := e.mayShowImage(path, ocr)
	// журналу «Что ушло» класс нужен от картинки: в промпте одна служебная
	// инструкция, и по её тексту скан паспорта записывался как «обычное»
	ctx = llm.WithClass(ctx, class)
	// большие картинки моделям не нужны и стоят дорого; а если модель уже
	// отказалась смотреть картинки, не долбимся в неё на каждом файле —
	// проверка инструментов в настройках сбрасывает этот запрет
	if e.useModelForImages() && len(data) > 0 && len(data) <= 8<<20 && e.VisionState() == "" && e.budgetCheck(ctx) == nil && show {
		prompt := visionPrompt
		if ocr != "" {
			prompt += "\n\nНаш OCR прочитал так — он часто ошибается, возьми из него только то,\n" +
				"что подтверждается картинкой:\n" + ocr
		}
		if p := e.provider(e.VisionModel()); p != nil {
			out, canSee, err := llm.SeeImage(ctx, p, prompt, data, imageMIME(path, mime))
			switch {
			case !canSee:
				e.setVisionState("модель этого провайдера не принимает картинки")
			case err != nil:
				log.Printf("vision %s: %v", path, err)
				e.setVisionState(shortErr(err.Error()))
			default:
				if text := visionText(out); text != "" {
					e.setVisionState("")
					return clip(text, 20000), "модель"
				}
			}
		}
	}
	if ocr != "" {
		return ocr, "OCR"
	}
	return "", ""
}

// visionText достаёт текст из ответа модели: ждём JSON, но модель может
// ответить и просто текстом — тогда берём его как есть.
func visionText(raw string) string {
	var v struct {
		Text string `json:"text"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(extractJSONBlock(raw)), &v); err == nil && strings.TrimSpace(v.Text) != "" {
		return normalizeWS(v.Text)
	}
	return normalizeWS(raw)
}

// imageMIME чинит тип, если почтовый клиент прислал octet-stream.
func imageMIME(path, mime string) string {
	if strings.HasPrefix(mime, "image/") {
		return mime
	}
	switch strings.ToLower(ext(path)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

const visionStateKey = "vision_state"

// setVisionState запоминает, чем закончилась попытка показать картинку —
// вместе с моделью, о которой речь: сменили модель, и старая жалоба уже
// ни при чём. Видно в настройках, вкладка «Инструменты».
func (e *Engine) setVisionState(problem string) {
	if problem == "" {
		_ = e.store.SetSetting(visionStateKey, "")
		return
	}
	_ = e.store.SetSetting(visionStateKey, e.VisionModel()+"|"+problem)
}

// VisionState — жалоба текущей модели; о прошлой молчим.
func (e *Engine) VisionState() string {
	model, problem, ok := strings.Cut(e.store.Setting(visionStateKey, ""), "|")
	if !ok || model != e.VisionModel() {
		return ""
	}
	return problem
}

// shortErr обрезает многословные ответы серверов моделей.
func shortErr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	return clip(s, 160)
}

// ResetVisionState снимает запрет на попытки показать картинку модели.
func (e *Engine) ResetVisionState() { e.setVisionState("") }

const noTextKey = "vision.no_text" // send | ask | hold

// NoTextMode — что делать с картинкой, на которой своё средство не нашло ни
// слова. Улик о содержимом нет, поэтому по умолчанию спрашиваем: молчание не
// разрешение. Настройка, а не гвоздь — у кого поток скриншотов, тому «send».
func (e *Engine) NoTextMode() string {
	switch e.store.Setting(noTextKey, "ask") {
	case "send":
		return "send"
	case "hold":
		return "hold"
	}
	return "ask"
}

func (e *Engine) SetNoTextMode(mode string) error {
	switch mode {
	case "send", "ask", "hold":
	default:
		return fmt.Errorf("режим бывает send, ask или hold")
	}
	return e.store.SetSetting(noTextKey, mode)
}

// mayShowImage — можно ли показать эту картинку модели. Класс считается по
// имени файла и тексту нашего OCR: другого знания о содержимом на этот момент
// нет и быть не может.
func (e *Engine) mayShowImage(path, ocr string) (bool, string) {
	class := sentClass(filepath.Base(path) + "\n" + ocr)
	if !e.ModelIsCloud(e.VisionModel()) {
		return true, class // домашней модели показываем всё: наружу ничего не уходит
	}
	if strings.TrimSpace(ocr) == "" {
		// решение человека по этому файлу сильнее общего режима
		switch e.ImageDecision(filepath.Base(path)) {
		case "показать":
			return true, class
		case "пропустить":
			return false, class
		}
		switch e.NoTextMode() {
		case "send":
			return true, class
		case "ask":
			e.askShowImage(path, "на картинке не нашлось текста")
		}
		return false, class
	}
	give, why := e.outsideCut(true, class, "")
	if !give {
		log.Printf("картинка %s модели не показана: %s", filepath.Base(path), why)
	}
	return give, class
}

// askShowImage — вопрос человеку тем же путём, что согласие по
// чувствительному: карточка в «Уведомлениях», решение за ним.
// ДополнитьВопросПроКартинку — вопрос задаётся из глубины извлечения
// текста, то есть ДО того, как файл записан в базу (AddFile сначала
// извлекает, потом вставляет). Поэтому в момент вопроса нет ни имени, ни
// номера файла — в записи стоял хэш, а картинка не показывалась вовсе
// (13 сентября 2026). Зовётся из AddFile сразу после вставки: имя и
// картинка дописываются в уже существующую запись.
func (e *Engine) AmendImageQuestion(a *store.Attachment) {
	if a == nil || a.SHA256 == "" {
		return
	}
	for _, key := range []string{"vision:" + a.SHA256, "vision:" + a.Path, "vision:" + a.Key} {
		rec, has := e.store.OpenNotificationByKey(key)
		if !has {
			continue
		}
		fresh := *rec
		fresh.Ref = fmt.Sprintf("file:%d", a.ID)
		// имя вместо хэша — и в итоге, и в поле
		hash := strings.TrimPrefix(key, "vision:")
		if a.Filename != "" {
			fresh.Summary = strings.Replace(rec.Summary, "«"+hash+"»", "«"+a.Filename+"»", 1)
		}
		fields := []store.NotifyField{{Label: "Файл", Value: a.Filename}}
		if a.Size > 0 {
			fields = append(fields, store.NotifyField{Label: "Размер", Value: fmt.Sprintf("%d КБ", (a.Size+1023)/1024)})
		}
		fresh.Fields = fields
		fresh.Actions = append([]store.NotifyAction{}, rec.Actions...)
		if _, err := e.Notify(&fresh); err != nil {
			log.Printf("вопрос про картинку %s: %v", a.Filename, err)
		}
		return
	}
}

func (e *Engine) askShowImage(path, why string) {
	name := filepath.Base(path)
	// в хранилище файл лежит под хэшем, и человеку показывалось
	// «Файл «5d8b5414556390d61ad1bbe6da71d4e…»» — имя ни о чём. Ищем
	// настоящее имя вложения по этому пути
	// Искали по полному пути — и не находили: в базе у файла лежит свой
	// путь в хранилище, а сюда приходит путь распакованной копии. Зато
	// базовое имя — это хэш содержимого, и по нему запись находится
	// сразу (AttachmentByPath сверяет и sha256). Из-за промаха человеку
	// восемнадцать раз подряд показали «Файл «bf62b2db…»» вместо
	// «qr.png» — решить по такому вопросу нельзя.
	show := attachmentName(e, name, path)
	// человек уже ответил про этот файл — второй раз не спрашиваем
	if e.ImageDecision(name) != "" {
		return
	}
	// Письмо, которое Сёма не разбирает, не стоит и вопроса. Спам и
	// корзину почтового сервера он читает на месте: не судит, не
	// переносит, моделям не показывает. Спрашивать, показывать ли
	// модели картинку из чужого спама, — значит просить решение,
	// которое ни на что не влияет.
	//
	// 11 сентября 2026 из-за этого вопросы посыпались очередью: разбор
	// спама принёс сотни писем с картинками-пикселями, и на каждую
	// Сёма спрашивал отдельно.
	if attachment, ok := e.store.AttachmentByPath(name); ok && attachment.MessageID > 0 {
		if m, err := e.store.Message(attachment.MessageID); err == nil && m != nil {
			if m.Status == store.StatusServerSpam || m.Status == store.StatusServerTrash {
				return
			}
		}
	}
	// Вопрос, на который не ответили пять раз подряд, шестой раз задавать
	// незачем: человек уже видит, что их много, и ответит разом на
	// «Инструментах». Предел — настройка: у каждого своё терпение.
	if open := e.howManyImageQuestionsWait(); open >= e.ImageQuestionsLimit() {
		return
	}
	key := "vision:" + name
	// вопрос задаётся один раз и ждёт: перечитывание файлов без текста
	// идёт по кругу, и без этой проверки один стикер, в котором OCR
	// ничего не нашёл, завёл четырнадцать тысяч записей за трое суток —
	// по одной каждые восемнадцать секунд. Лента становится непригодной
	// к чтению, а вместе с ней и все каналы доставки
	if _, has := e.store.OpenNotificationByKey(key); has {
		return
	}
	// вопрос без кнопок — не вопрос: закрыв запись, человек ничего не
	// решал, и следующий проход спрашивал снова. Решение хранится по
	// файлу и держится
	// Решать про картинку, не видя её, нельзя: в вопросе стоял хэш
	// содержимого («Файл «e26ddec8…»»), и человек отвечал наугад
	// (13 сентября 2026). Кладём в запись саму картинку рамкой и имя
	// файла полем, а рядом — ссылку открыть её целиком
	rec := &store.Notification{
		Key: key, Source: "core", Level: "info",
		Title:   "Показать картинку модели?",
		Summary: fmt.Sprintf("Файл «%s»: %s. Пока не показывал — решайте вы.", show, why),
		Fields:  []store.NotifyField{{Label: "Файл", Value: show}},
		Actions: []store.NotifyAction{
			{Kind: "core", Label: "Показать", Op: "vision_show", Ref: name},
			{Kind: "core", Label: "Не показывать", Op: "vision_skip", Ref: name},
		},
	}
	if a, ok := e.store.AttachmentByPath(name); ok && a.ID > 0 {
		rec.Ref = fmt.Sprintf("file:%d", a.ID)
		rec.Fields = append(rec.Fields,
			store.NotifyField{Label: "Размер", Value: fmt.Sprintf("%d КБ", (a.Size+1023)/1024)})
		rec.Actions = append(rec.Actions,
			store.NotifyAction{Kind: "goto", Label: "Открыть файл", Screen: "files"})
	}
	_, _ = e.Notify(rec)
}

// howManyImageQuestionsWait — сколько вопросов про картинки уже висит без
// ответа.
func (e *Engine) howManyImageQuestionsWait() int {
	list, err := e.store.Notifications(true, 500)
	if err != nil {
		return 0
	}
	n := 0
	for _, z := range list {
		if strings.HasPrefix(z.Key, "vision:") {
			n++
		}
	}
	return n
}

// ImageQuestionsLimit — сколько неотвеченных вопросов про картинки
// Сёма держит на экране, прежде чем перестать спрашивать.
func (e *Engine) ImageQuestionsLimit() int {
	v, err := strconv.Atoi(e.store.Setting("vision.max_ask", "5"))
	if err != nil || v < 1 {
		return 5
	}
	return v
}

// ПочинитьИменаКартинок// ПочинитьИменаКартинок — разовая уборка вопросов «Показать картинку
// модели?», в которых вместо имени файла стоит хэш.
//
// 11 сентября 2026 их накопилось восемнадцать: имя искалось по полному
// пути и не находилось, и человек видел «Файл «bf62b2db…»» вместо
// «qr.png». Решить по такому вопросу нельзя, а закрыть — значит
// согласиться вслепую. Ключ у записи тот же, поэтому исправленная
// заменяет прежнюю, а решение человека (если он уже решил) не трогаем.
func (e *Engine) FixImageNames() {
	// метка своя у каждого поколения починки: прежняя ставила только имя,
	// а картинку в запись не кладла вовсе
	const keySettings = "vision.names_fixed2"
	if e.store.Setting(keySettings, "") == "1" {
		return
	}
	list, err := e.store.Notifications(true, 2000)
	if err != nil {
		log.Printf("имена картинок: %v", err)
		return
	}
	fixed := 0
	for _, n := range list {
		fileName, has := strings.CutPrefix(n.Key, "vision:")
		if !has {
			continue
		}
		// файл ищем по хэшу, пути и ключу: в ключе записи лежит то, что
		// было под рукой у извлечения текста
		a, has := e.store.AttachmentByPath(fileName)
		if !has || a == nil || a.ID == 0 {
			continue // файла всё равно не нашли — оставляем как есть
		}
		e.AmendImageQuestion(a)
		fixed++
	}
	if fixed > 0 {
		log.Printf("вопросы про картинки: имя файла подставлено в %d", fixed)
	}
	_ = e.store.SetSetting(keySettings, "1")
}

// attachmentName — настоящее имя файла// имяВложения — настоящее имя файла: сначала по базовому имени (это
// хэш содержимого), потом по полному пути. Не нашли — возвращаем то, что
// есть: хэш в вопросе хуже имени, но лучше пустоты.
func attachmentName(e *Engine, name, path string) string {
	for _, key := range []string{name, path} {
		if key == "" {
			continue
		}
		if a, ok := e.store.AttachmentByPath(key); ok && a.Filename != "" {
			return a.Filename
		}
	}
	return name
}

// ImageDecision — что человек решил про этот файл: «показать»,
// «пропустить» или пусто, если ещё не решал.
func (e *Engine) ImageDecision(name string) string {
	return e.store.Setting("vision.decision."+name, "")
}

// DecideByImage — ответ человека из карточки. Он держится: иначе
// каждый новый проход спрашивает заново про тот же файл.
func (e *Engine) DecideByImage(name, decision string) error {
	if err := e.store.SetSetting("vision.decision."+name, decision); err != nil {
		return err
	}
	e.store.CloseNotificationByKey("vision:"+name, "done", "человек")
	return nil
}
