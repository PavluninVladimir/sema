package engine

// Разбор входящих файлов ядром. Раньше этим занимался встроенный модуль
// «Чеки»: по событию `file.added` он спрашивал модель, кто получатель,
// какая сумма и о чём документ. Модуль уезжает, а поля у самой записи файла
// (`kind`, `vendor`, `amount`, `currency`, `summary`) остались с тех пор,
// когда чеки жили в ядре, — их и заполняем.
//
// Работа идёт очередью, а не сразу: модель думает долго, а на Raspberry Pi
// она занимает процессор целиком. Файл, который не разобрался, ничего не
// теряет: он остаётся с тем, что было (вид `document`), а строка в журнале
// объясняет, почему.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	// WorkFileParse — вид работы очереди: один файл, один вопрос модели.
	WorkFileParse = "file_parse"

	filesParseKey = "files.parse" // 1 — ядро разбирает входящие файлы

	// сколько текста файла показываем модели: договор на сорок страниц не
	// делает разбор точнее, а локальную модель кладёт целиком
	fileParseChars = 3000
)

// FilesParseOn — разбирает ли Сёма входящие файлы. По умолчанию да:
// без разбора «Файлы» остаются списком имён без сумм и описаний.
func (e *Engine) FilesParseOn() bool { return e.store.Setting(filesParseKey, "1") == "1" }

// SetFilesParse — тумблер «Разбирать файлы» на экране «Хранилища».
func (e *Engine) SetFilesParse(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(filesParseKey, v)
}

// EnqueueFileParse — поставить файл в очередь на разбор. Зовётся там же, где
// ядро выпускает `file.added`: у файла появился текст, значит его есть чем
// разбирать.
func (e *Engine) EnqueueFileParse(id int64) {
	if id == 0 || !e.FilesParseOn() {
		return
	}
	_ = e.store.Enqueue(WorkFileParse, strconv.FormatInt(id, 10), "", 5)
}

// fileParsePrompt — короткий вопрос и короткий ответ. Развёрнутых рассуждений
// здесь не нужно: `безРазмышлений` в конце — та же строка, что у памяти, и по
// той же причине (локальная qwen3 тратила весь предел роли на «думание»).
const fileParsePrompt = "Разбери файл и ответь ТОЛЬКО JSON, без пояснений и без ограждений:\n" +
	`{"kind": "receipt|document", "vendor": "кто выдал или прислал", "amount": 0, "currency": "RUB", "doc_date": "", "due_date": "", "to_pay": false, "summary": "о чём файл, одной строкой по-русски"}` + "\n" +
	"receipt — чек, квитанция, счёт на оплату или подтверждение платежа. Реклама с ценами, скидками и кэшбэком чеком не считается.\n" +
	"document — всё остальное; тогда amount 0 и currency пустая.\n" +
	"amount — итоговая сумма (Итого / К оплате / Сумма) числом, без пробелов.\n" +
	"doc_date — дата самого документа в виде ГГГГ-ММ-ДД. Не дата письма и не сегодня.\n" +
	"due_date — до какого числа платить («оплатите до», «срок оплаты», «至»), тоже ГГГГ-ММ-ДД.\n" +
	"to_pay — true, если это счёт К ОПЛАТЕ (ещё не оплачен); false, если это чек, квитанция об оплате или подтверждение платежа.\n" +
	"vendor и summary — по-русски, коротко. Не знаешь — оставь пустым. Даты не выдумывай: нет в документе — пустая строка.\n\n"

// fileFacts — то, что ядро хочет узнать о файле. Сумма приходит и числом, и
// строкой («1 234,50»): модель пишет как умеет, а разбор не должен из-за
// этого терять и остальные поля.
type fileFacts struct {
	Kind     string `json:"kind"`
	Vendor   string `json:"vendor"`
	Amount   any    `json:"amount"`
	Currency string `json:"currency"`
	DocDate  string `json:"doc_date"`
	DueDate  string `json:"due_date"`
	ToPay    bool   `json:"to_pay"`
	Summary  string `json:"summary"`
}

// handleFileParse — одна работа очереди: заполнить поля одного файла.
func (e *Engine) handleFileParse(ctx context.Context, w *store.WorkItem) error {
	if !e.FilesParseOn() {
		return nil // человек выключил разбор: работа сделана тем, что не сделана
	}
	id, err := strconv.ParseInt(w.Ref, 10, 64)
	if err != nil {
		return fmt.Errorf("непонятная ссылка %q", w.Ref)
	}
	a, err := e.store.AttachmentByID(id)
	if err != nil {
		return nil // файла больше нет: разбирать нечего
	}
	codes := e.FileCodes(a.ID)
	// чек ФНС — это не догадка, а реквизиты налоговой: сумма, дата и
	// номера ставятся сразу, до всякой модели. Даже если модель потом не
	// ответит вовсе, чек уже записан с точной суммой
	receipt := ReceiptFromCodes(codes)
	if receipt != nil {
		if err := e.receiptByQR(a, receipt); err != nil {
			return err
		}
	}
	text := strings.TrimSpace(a.Extracted)
	if text == "" && receipt == nil {
		return nil // ни текста, ни кодов: разбирать нечего, ждать нечего
	}
	if until, resting := e.roleCooldown(RoleModules); resting && !e.CloudHelpOn() {
		return postpone(time.Until(until)+time.Minute, "роль «Модули» на передышке")
	}
	cloud, _ := e.needsCloudHelp(WorkFileParse)

	// закрытый ящик и класс «секретное» — два разных запрета, и оба ведут в
	// одну дверь: `askRoleClosed` сам уведёт текст доверенной модели, а не
	// найдёт её — честно откажет, и работа подождёт человека
	closed := e.fileClosedForCloud(a) || e.ClassOfFile(a) == store.ClassSecret
	answer, _, err := e.askRoleClosed(ctx, RoleModules, e.fileParseQuestion(a, text, codes, receipt), cloud, closed)
	if err != nil {
		switch {
		case errors.Is(err, errNoTrusted):
			return postpone(6*time.Hour, "закрытый файл, а доверенной модели нет")
		case isBusyErr(err):
			return postpone(10*time.Minute, clip(err.Error(), 120))
		}
		return err // очередь сама даст растущую паузу и остановится на пределе попыток
	}
	var f fileFacts
	if err := json.Unmarshal([]byte(extractJSON(answer)), &f); err != nil {
		// файл остаётся с тем, что было: вид `document`, прежняя подпись.
		// Второй раз спрашивать ту же модель тем же вопросом незачем
		log.Printf("разбор файла %d (%s): ответ модели не разобран — %s", a.ID, a.Filename, clip(answer, 120))
		return nil
	}
	kind, vendor, amount, currency, summary := fileFactsClean(f)
	if kind == "" && vendor == "" && amount == 0 && summary == "" {
		log.Printf("разбор файла %d (%s): модель ничего не назвала", a.ID, a.Filename)
		return nil
	}
	if receipt != nil {
		// код важнее ответа модели: он и есть та самая точность, ради
		// которой его читали. У модели берём только продавца и описание
		kind, amount, currency = store.AttKindReceipt, receipt.Amount, "RUB"
		f.DocDate, f.ToPay = receipt.When.ISO(), false
	}
	if err := e.store.MarkAttachment(a.ID, kind, vendor, amount, currency, summary); err != nil {
		return err
	}
	// даты документа — отдельно от сумм: «когда счёт» и «когда платить»
	// это разные вопросы, и оба не совпадают с датой письма
	docDue, due := date(f.DocDate), date(f.DueDate)
	if docDue != "" || due != "" || f.ToPay {
		if err := e.store.SetFileDates(a.ID, docDue, due, f.ToPay && kind == store.AttKindReceipt); err != nil {
			return err
		}
	}
	// кто это на самом деле: сырое имя остаётся на файле, а справочник
	// сводит написания одного поставщика
	if fresh, err := e.store.AttachmentByID(a.ID); err == nil {
		if _, err := e.FileProvider(fresh); err != nil {
			log.Printf("разбор файла %d: поставщик не записан: %v", a.ID, err)
		}
	}
	log.Printf("разобран файл %d (%s): %s · %s · %.2f %s", a.ID, a.Filename, kind, vendor, amount, currency)
	return nil
}

// receiptByQR — записать чек по коду ФНС, не спрашивая модель. Продавец и
// описание пока пустые: их принесёт модель следующим шагом, а сумма и
// дата уже верные, и файл не пропадёт из «Чеков», даже если модель молчит.
func (e *Engine) receiptByQR(a *store.Attachment, receipt *FNSReceipt) error {
	vendor, summary := a.Vendor, a.Summary
	if strings.HasPrefix(summary, "прочитано:") || strings.HasPrefix(summary, "текст не извлечён") ||
		strings.HasPrefix(summary, "картинк") {
		summary = "" // это подпись о том, чем читали, а не о чём документ
	}
	if err := e.store.MarkAttachment(a.ID, store.AttKindReceipt, vendor, receipt.Amount, "RUB", summary); err != nil {
		return err
	}
	if err := e.store.SetFileDates(a.ID, receipt.When.ISO(), "", false); err != nil {
		return err
	}
	log.Printf("файл %d (%s): чек по QR ФНС — %.2f RUB от %s, ФД %s", a.ID, a.Filename, receipt.Amount, receipt.When.Text(), receipt.FD)
	return nil
}

// fileParseQuestion — что показываем модели: имя, тип, письмо (если файл из
// почты), начало текста и коды с картинки.
func (e *Engine) fileParseQuestion(a *store.Attachment, text string, codes []store.FileCode, receipt *FNSReceipt) string {
	var b strings.Builder
	b.WriteString(fileParsePrompt)
	if receipt != nil {
		// сумму и дату не спрашиваем вовсе: спросишь — модель ответит, и
		// её ответ придётся отбрасывать, потратив на него токены
		b.WriteString("У этого файла есть QR-код чека ФНС: сумма и дата уже известны точно, " +
			"их не называй — верни kind \"receipt\", а от себя только vendor и summary.\n\n")
	}
	b.WriteString("Файл: " + a.Filename + " · " + a.MIME + "\n")
	if a.MessageID > 0 {
		if a.FromAddr != "" {
			b.WriteString("Из письма от: " + a.FromAddr + "\n")
		}
		if a.Subject != "" {
			b.WriteString("Тема письма: " + a.Subject + "\n")
		}
	}
	if line := CodesForModel(codes); line != "" {
		b.WriteString(line + "\n")
	}
	b.WriteString("---\n" + clip(text, fileParseChars) + "\n---\n")
	b.WriteString(withoutThinking)
	return b.String()
}

// fileFactsClean — привести ответ модели к тому, что кладётся в базу. Вид
// признаём только известный: `kind` попадает в выборки экранов, и «счёт»,
// «invoice» или «чек?» из ответа модели превратили бы их в решето.
func fileFactsClean(f fileFacts) (kind, vendor string, amount float64, currency, summary string) {
	switch strings.ToLower(strings.TrimSpace(f.Kind)) {
	case store.AttKindReceipt, "чек", "receipt/invoice", "invoice":
		kind = store.AttKindReceipt
	default:
		kind = store.AttKindDocument
	}
	vendor = clip(strings.TrimSpace(f.Vendor), 120)
	summary = clip(strings.TrimSpace(f.Summary), 300)
	amount = numberFromReply(f.Amount)
	currency = strings.ToUpper(clip(strings.TrimSpace(f.Currency), 8))
	if kind != store.AttKindReceipt {
		// не чек — сумма и валюта не наши: у рекламной рассылки «итого» тоже
		// найдётся, и в «Файлах» она встала бы рядом с настоящими тратами
		amount, currency = 0, ""
	}
	if amount > 0 && currency == "" {
		currency = "RUB"
	}
	if amount <= 0 {
		currency = ""
	}
	return kind, vendor, amount, currency, summary
}

// date — ответ модели в дату. Модели пишут «12.09.2026», «2026-09-12» и
// «12 сентября 2026»; принимаем то, что разбирается наверняка, остальное
// отбрасываем. Выдуманный срок оплаты хуже, чем его отсутствие: по нему
// Сёма поднимет тревогу не в тот день и человек перестанет ей верить.
func date(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, form := range []string{"2006-01-02", "02.01.2006", "02/01/2006", "2006/01/02", time.RFC3339} {
		if t, err := time.Parse(form, s); err == nil {
			// дата за пределами разумного — это не дата, а мусор разбора
			if t.Year() < 2000 || t.Year() > time.Now().Year()+5 {
				return ""
			}
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// numberFromReply — сумма из ответа модели: числом, строкой «1 234,50» или
// «1234.50 RUB». Не разобралось — ноль, и это честнее выдуманной суммы.
func numberFromReply(v any) float64 {
	switch x := v.(type) {
	case float64:
		if x > 0 {
			return x
		}
	case string:
		s := strings.Map(func(r rune) rune {
			switch {
			case r >= '0' && r <= '9':
				return r
			case r == ',' || r == '.':
				return '.'
			}
			return -1 // пробелы, неразрывные пробелы, знаки валют — прочь
		}, x)
		// «1.234.50» — разделитель тысяч точкой: последняя точка дробная
		if i := strings.LastIndex(s, "."); i >= 0 {
			s = strings.ReplaceAll(s[:i], ".", "") + s[i:]
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
			return f
		}
	}
	return 0
}

// fileClosedForCloud — файл из закрытого ящика. Закрытость ящика — решение
// человека, а не примета в тексте: дверь должна узнать о ней до разбора.
func (e *Engine) fileClosedForCloud(a *store.Attachment) bool {
	fromMail := false
	for _, src := range e.store.FileSources(a.ID) {
		if src.Kind != store.SourceMail {
			continue
		}
		fromMail = true
		if e.mailboxClosedForCloud(e.accountOr(src.Account)) {
			return true
		}
	}
	// источников ещё нет (файл только что вставлен) — смотрим письмо записи
	if !fromMail && a.MessageID > 0 {
		return e.mailboxClosedForCloud(e.accountOr(a.Account))
	}
	return false
}

// accountOr — имя ящика: у писем, попавших в базу до появления имён, оно
// пустое, и тогда это основной ящик.
func (e *Engine) accountOr(name string) string {
	if name == "" {
		return e.conf.Mail.Name
	}
	return name
}
