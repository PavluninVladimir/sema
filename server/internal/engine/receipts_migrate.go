package engine

// Модуль «Чеки» снят 10 сентября 2026, как до него «Требует внимания»:
// он дублировал в своих коллекциях то, что у записи файла есть и так —
// вид, получатель, сумма, валюта, итог. Разбор переехал в ядро, а данные
// модуля — сюда.
//
// Перенос двусторонний, потому что чеки были двух родов. Двадцать четыре
// пришли вложением: их факты ложатся на сам файл, и «Файлы» показывают
// сумму там же, где показывали. Семьдесят три жили в теле письма — файла
// у них нет и не было, и просто выбросить их нельзя: тогда суммы в
// дайджесте, «Вернуть с чеками» и опись для модели разом обеднели бы
// втрое, а человек этого не просил.
//
// Поэтому такой чек становится файлом: короткая записка с получателем,
// суммой и итогом, источником — то самое письмо. Дальше он живёт как
// любой другой файл: попадает в поиск, в векторы, в суммы по письму, в
// счётчик отправителя. Чек и есть файл с суммой — отдельной сущности
// больше нет.

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const receiptsMigrationKey = "receipts.migrated"

// moduleReceipt — запись коллекции модуля. Полей больше, чем нам нужно;
// берём те, что переживут переезд.
type moduleReceipt struct {
	Date, Who, Category, Result, Currency, Account, Subject, From string
	Amount                                                        float64
	File, Message                                                 int64
}

// MigrateReceiptsModule — разовый переезд. Идемпотентен: метка в
// настройках, как у остальных переездов.
func (e *Engine) MigrateReceiptsModule(ctx context.Context) {
	if e.store.Setting(receiptsMigrationKey, "") == "1" {
		e.sweep()
		return
	}
	docs, err := e.store.FindModuleDocs("receipts", "receipts", store.DocQuery{Limit: 5000})
	if err != nil {
		log.Printf("чеки: не прочитать коллекцию модуля: %v", err)
		return
	}
	if len(docs) == 0 {
		// модуля не было или он пуст — переезжать нечего, но метку
		// ставим: иначе проверка будет ходить в базу при каждом старте
		_ = e.store.SetSetting(receiptsMigrationKey, "1")
		return
	}
	receipts := make([]moduleReceipt, 0, len(docs))
	for _, d := range docs {
		receipts = append(receipts, receiptFromDoc(d.Doc))
	}
	sort.SliceStable(receipts, func(i, j int) bool { return receipts[i].Date < receipts[j].Date })

	marked := e.markReceiptFiles(receipts)
	// модуль снимаем ДО того, как заводить файлы: пока он на месте, он
	// слышит «file.added» и на каждую нашу записку пишет себе новый чек —
	// переезд кормит сам себя. Данные при этом не трогаем: если записки
	// не лягут, переезд повторится при следующем старте и возьмёт их
	// оттуда же
	if err := e.clearModuleReceipts(); err != nil {
		log.Printf("чеки: модуль не снялся: %v", err)
		return
	}
	created, loss := e.createReceiptFiles(ctx, receipts)
	if loss > 0 {
		log.Printf("чеки: %d чеков не легли файлами — переезд отложен", loss)
		return
	}
	if err := e.store.DeleteModuleData("receipts"); err != nil {
		log.Printf("чеки: коллекции модуля не убрались: %v", err)
	}
	_ = e.store.SetSetting(receiptsMigrationKey, "1")
	log.Printf("чеки: перенесено %d, помечено файлов %d, заведено записок %d; модуль снят",
		len(receipts), marked, created)
}

// receiptFromDoc — запись модуля в наши поля. Числа приезжают из JSON как
// float64, но у старых записей встречались и строки.
func receiptFromDoc(d map[string]any) moduleReceipt {
	h := moduleReceipt{
		Date: strOf(d["date"]), Who: strOf(d["vendor"]), Category: strOf(d["category"]),
		Result: strOf(d["summary"]), Currency: strOf(d["currency"]), Account: strOf(d["account"]),
		Subject: strOf(d["subject"]), From: strOf(d["source"]),
	}
	if h.Currency == "" {
		h.Currency = "RUB"
	}
	switch v := d["amount"].(type) {
	case float64:
		h.Amount = v
	case string:
		h.Amount, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	h.File = toInt(d["file"])
	h.Message = toInt(d["msg"])
	return h
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

// markReceiptFiles — факты чека ложатся на сам файл. Номер файла
// записан у одного чека из девяноста семи: остальные вложения модуль
// пометил, а номер к себе не положил. Ищем файл по письму, а среди
// нескольких — по имени, с которого модуль начинал итог.
func (e *Engine) markReceiptFiles(receipts []moduleReceipt) int {
	marked := 0
	for _, h := range receipts {
		id := h.File
		if id == 0 && h.From == "attachment" && h.Message > 0 {
			id = e.receiptFile(h)
		}
		if id == 0 {
			continue
		}
		if err := e.store.MarkAttachment(id, store.AttKindReceipt, h.Who, h.Amount, h.Currency, h.Result); err != nil {
			log.Printf("чеки: файл %d не пометился: %v", id, err)
			continue
		}
		marked++
	}
	return marked
}

// receiptFile — какой файл письма и есть чек. Один — он и есть; несколько —
// тот, чьё имя модуль поставил в начало итога («счёт.pdf · оплата за…»).
func (e *Engine) receiptFile(h moduleReceipt) int64 {
	files, err := e.store.AttachmentsOfMessage(h.Message)
	if err != nil || len(files) == 0 {
		return 0
	}
	if len(files) == 1 {
		return files[0].ID
	}
	name, _, _ := strings.Cut(h.Result, " · ")
	name = strings.TrimSpace(name)
	for _, f := range files {
		if name != "" && strings.EqualFold(f.Filename, name) {
			return f.ID
		}
	}
	return 0
}

// createReceiptFiles — чек из тела письма становится файлом. Записка
// короткая: получатель, сумма, категория, итог и тема письма — по ней
// чек и ищут словами. Номер письма в тексте обязателен: без него две
// одинаковые покупки в один день слились бы в один файл, потому что
// AddFile отбрасывает повторы по содержимому.
func (e *Engine) createReceiptFiles(ctx context.Context, receipts []moduleReceipt) (created, loss int) {
	for _, h := range receipts {
		if h.From == "attachment" {
			continue // такой чек уже помечен на своём вложении
		}
		name := noteName(h)
		var b strings.Builder
		fmt.Fprintf(&b, "Чек · %s\n", field(h.Who))
		fmt.Fprintf(&b, "Сумма: %.2f %s\n", h.Amount, field(h.Currency))
		if h.Category != "" {
			fmt.Fprintf(&b, "Категория: %s\n", field(h.Category))
		}
		fmt.Fprintf(&b, "Дата: %s\n", receiptDay(h.Date))
		if h.Subject != "" {
			fmt.Fprintf(&b, "Письмо: %s\n", field(h.Subject))
		}
		if h.Result != "" {
			fmt.Fprintf(&b, "\n%s\n", field(h.Result))
		}
		fmt.Fprintf(&b, "\nИз письма #%d; чек перенесён из модуля «Чеки», снятого 10 сентября 2026.\n", h.Message)

		source := FileSource{Kind: "module", Ref: "receipts", Account: h.Account}
		var message *store.Message
		if h.Message > 0 {
			// источником ставим само письмо: тогда сумма видна в сводке
			// «Почты» у того письма, к которому относится
			if m, err := e.store.Message(h.Message); err == nil {
				message = m
				source = FileSource{Kind: "mail", Ref: strconv.FormatInt(h.Message, 10), Account: h.Account}
			}
		}
		f, _, err := e.AddFile(ctx, name, "text/plain", []byte(b.String()), source, message)
		if err != nil {
			log.Printf("чеки: записка «%s» не легла: %v", name, err)
			loss++
			continue
		}
		if err := e.store.MarkAttachment(f.ID, store.AttKindReceipt, h.Who, h.Amount, h.Currency, h.Result); err != nil {
			log.Printf("чеки: записка #%d не пометилась: %v", f.ID, err)
			loss++
			continue
		}
		created++
	}
	return created, loss
}

// noteName — «Чек · Умный ритейл · 2601 ₽ · 2026-05-27.txt»: в списке
// файлов такое имя читается без открытия.
func noteName(h moduleReceipt) string {
	who := field(h.Who)
	if who == "" {
		who = "без получателя"
	}
	who = strings.NewReplacer("/", "-", "\\", "-", ":", " ").Replace(who)
	return fmt.Sprintf("Чек · %s · %.0f %s · %s.txt", clip(who, 60), h.Amount, currencySign(h.Currency), receiptDay(h.Date))
}

func currencySign(v string) string {
	switch strings.ToUpper(v) {
	case "RUB", "":
		return "₽"
	case "USD":
		return "$"
	case "EUR":
		return "€"
	}
	return strings.ToUpper(v)
}

// clearModuleReceipts — модуль был встроенным, а встроенные RemoveModule не
// удаляет: он бережёт то, что приезжает вместе с Сёмой. Здесь бережь
// лишняя — receipts.js больше нет в сборке, и запись в базе осталась бы
// сиротой с кодом, который никто не обновит.
func (e *Engine) clearModuleReceipts() error {
	if err := e.dropModuleTasks("receipts"); err != nil {
		return err
	}
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	delete(all, "receipts")
	return e.saveModules(all)
}

// sweep — остатки коллекций снятого модуля. Первый переезд заводил
// файлы, пока модуль был ещё жив: он слышал «file.added» и на каждую
// записку писал себе новый чек, а те записи пережили удаление. Читать их
// уже некому, но пусть база будет честной.
func (e *Engine) sweep() {
	if _, ok := e.Module("receipts"); ok {
		return // модуль на месте — это не остатки
	}
	docs, _ := e.store.FindModuleDocs("receipts", "receipts", store.DocQuery{Limit: 1})
	if len(docs) == 0 {
		if cats, _ := e.store.FindModuleDocs("receipts", "categories", store.DocQuery{Limit: 1}); len(cats) == 0 {
			return
		}
	}
	if err := e.store.DeleteModuleData("receipts"); err != nil {
		log.Printf("чеки: остатки коллекций не убрались: %v", err)
		return
	}
	log.Printf("чеки: убраны остатки коллекций снятого модуля")
}

// field — значение для точки с запятой: разделитель внутри значения
// сломал бы столбцы, а кавычки в CSV читать глазами хуже.
func field(s string) string {
	s = strings.ReplaceAll(s, ";", ",")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// receiptDay — только день: время у чека ничего не значит, а столбец
// сужает.
func receiptDay(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("2006-01-02")
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func messageLink(id int64) string {
	if id <= 0 {
		return ""
	}
	return "письмо #" + strconv.FormatInt(id, 10)
}
