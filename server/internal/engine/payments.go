package engine

// Платежи: что Сёма умеет такого, чего не умеет почтовый ящик.
//
// Ящик показывает письма. Сёма видит документы внутри них и знает про них
// три вещи, которых письмо не говорит: кто поставщик (справочник сводит
// написания), когда платить (`due_date`) и оплачено ли (счёт против чека).
// Из этих трёх выходит то, ради чего ассистент и нужен:
//
//   «Ближайший платёж: Горэнерго, 512,30 ₽, до 12 сентября»
//   «Счёт от 1 сентября на 3 480 ₽ — чека за него так и нет»
//
// Про второе отдельно. Сёма НЕ утверждает, что человек не заплатил: чек
// мог прийти бумагой, платёж — из банка без письма. Она говорит ровно то,
// что видит: счёт есть, документа об оплате в её архиве нет. Разница между
// «ты не заплатил» и «я не вижу подтверждения» — это разница между
// ассистентом, которому верят, и тем, которого выключают.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	// сколько дней после срока ждать чек, прежде чем сказать о его
	// отсутствии. Оплата и чек не приходят в один час
	receiptWaitKey = "payments.receipt_wait_days"
	paymentsKey    = "payments.on"
)

// PaymentsOn — следит ли Сёма за платежами.
func (e *Engine) PaymentsOn() bool { return e.store.Setting(paymentsKey, "1") == "1" }

func (e *Engine) SetPayments(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(paymentsKey, v)
}

// ReceiptWaitDays — сколько дней после срока ждать чек.
func (e *Engine) ReceiptWaitDays() int {
	v, err := strconv.Atoi(e.store.Setting(receiptWaitKey, "3"))
	if err != nil || v < 0 {
		return 3
	}
	return v
}

func (e *Engine) SetReceiptWaitDays(n int) error {
	if n < 0 {
		return fmt.Errorf("срок не бывает отрицательным")
	}
	return e.store.SetSetting(receiptWaitKey, strconv.Itoa(n))
}

// NearestPayment — что платить следующим. Пусто — платить нечего, и это
// тоже ответ: строку «ничего не ждёт» показывать не надо. excluded — ящики,
// закрытые для этой сборки (снимок на устройстве, offline-snapshot 2а.6):
// счёт из такого ящика ближайшим платежом не станет; nil — обычный показ,
// как было всегда.
func (e *Engine) NearestPayment(o *store.Exclusion) (*store.Attachment, string) {
	if !e.PaymentsOn() {
		return nil, ""
	}
	invoices, err := e.store.BillsDue(time.Now().Format("2006-01-02"), 20)
	if err != nil || len(invoices) == 0 {
		return nil, ""
	}
	invoices = e.store.FilterExcludedAttachments(invoices, o)
	for _, count := range invoices {
		if e.receiptFound(count) {
			continue // за этот счёт документ об оплате есть
		}
		return count, paymentLine(count, e.fileProviderName(count))
	}
	return nil, ""
}

// paymentLine — «Горэнерго · 512,30 ₽ · до 12 сентября».
func paymentLine(a *store.Attachment, who string) string {
	parts := []string{who}
	if a.Amount > 0 {
		parts = append(parts, money(a.Amount, a.Currency))
	}
	if t, err := time.Parse("2006-01-02", a.DueDate); err == nil {
		days := int(time.Until(t).Hours() / 24)
		switch {
		case days < 0:
			parts = append(parts, fmt.Sprintf("срок был %s — %s назад", t.Format("2 января"), plural(-days, "день", "дня", "дней")))
		case days == 0:
			parts = append(parts, "срок сегодня")
		default:
			parts = append(parts, fmt.Sprintf("до %s · %s", t.Format("2 января"), plural(days, "день", "дня", "дней")))
		}
	}
	return strings.Join(parts, " · ")
}

func money(amount float64, currency string) string {
	sign := currencySign(currency)
	return fmt.Sprintf("%s %s", amountLine(amount), sign)
}

func amountLine(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimSuffix(s, ".00")
	return strings.Replace(s, ".", ",", 1)
}

// fileProviderName — как звать того, кто прислал документ: имя из
// справочника, а нет его — сырое имя из бумаги.
func (e *Engine) fileProviderName(a *store.Attachment) string {
	if a.VendorID > 0 {
		if v, ok := e.store.VendorByID(a.VendorID); ok {
			return v.Name
		}
	}
	if a.Vendor != "" {
		return a.Vendor
	}
	return "без поставщика"
}

// receiptFound — есть ли в архиве документ об оплате этого счёта. Ищем у того
// же поставщика, после даты счёта, на ту же сумму.
//
// «Та же сумма» — с допуском: банк округляет, комиссия добавляется, а чек
// иногда выписан на копейку иначе. Допуск — настройка, а не гвоздь.
func (e *Engine) receiptFound(count *store.Attachment) bool {
	if count.Amount <= 0 {
		// без суммы сверять нечего: любой чек того же поставщика сойдёт за
		// оплату, и Сёма замолчит про настоящую просрочку
		return false
	}
	from := count.DocDate
	if from == "" {
		from = count.Date.Format("2006-01-02")
	}
	receipts, err := e.store.ReceiptsAfter(count.VendorID, count.Vendor, from, 50)
	if err != nil {
		return false
	}
	access := math.Max(count.Amount*0.02, 1)
	for _, h := range receipts {
		if h.ID == count.ID {
			continue
		}
		if math.Abs(h.Amount-count.Amount) <= access {
			return true
		}
	}
	return false
}

// InvoicesWithoutReceipt — счета, у которых срок прошёл, а документа об оплате в
// архиве нет. Отсюда рождается уведомление; сам текст осторожен нарочно.
func (e *Engine) InvoicesWithoutReceipt(now time.Time) []*store.Attachment {
	if !e.PaymentsOn() {
		return nil
	}
	edge := now.AddDate(0, 0, -e.ReceiptWaitDays()).Format("2006-01-02")
	invoices, err := e.store.BillsOverdue(edge, 50)
	if err != nil {
		return nil
	}
	var out []*store.Attachment
	for _, count := range invoices {
		if !e.receiptFound(count) {
			out = append(out, count)
		}
	}
	return out
}

// paymentsPass — часть задачи «Сроки»: сказать о счетах без чека.
// Уведомление одно на счёт (ключ по файлу), уровень «внимание»: это не
// тревога — Сёма не знает, оплачен ли счёт, она знает только, что не
// видела подтверждения.
func (e *Engine) paymentsPass(now time.Time) int {
	said := 0
	for _, count := range e.InvoicesWithoutReceipt(now) {
		who := e.fileProviderName(count)
		result := "Сёма не видела документа об оплате — возможно, чек просто не пришёл письмом"
		fields := []store.NotifyField{
			{Label: "Поставщик", Value: who},
			{Label: "Сумма", Value: money(count.Amount, count.Currency)},
		}
		if t, err := time.Parse("2006-01-02", count.DueDate); err == nil {
			fields = append(fields, store.NotifyField{Label: "Срок был", Value: t.Format("2 января 2006"), Level: "warn"})
		}
		if _, err := e.Notify(&store.Notification{
			Key: "bill:" + strconv.FormatInt(count.ID, 10), Source: "mail", Level: "warn",
			Title:   fmt.Sprintf("Счёт %s на %s — чека нет", who, money(count.Amount, count.Currency)),
			Summary: result, Fields: fields,
			Actions: []store.NotifyAction{
				{Label: "Открыть счёт", Kind: "goto", Screen: "files"},
				{Label: "Оплачен", Kind: "done"},
			},
		}); err == nil {
			said++
		}
	}
	return said
}

// ---- счёт, который не пришёл ----
//
// Почтовый ящик умеет показывать письма. Заметить, что письмо НЕ пришло,
// он не умеет вовсе — а это ровно тот случай, когда человек попадает на
// пени: счёт за электричество приходит третьего числа каждый месяц, в этом
// месяце не пришёл, и вспоминают об этом, когда приходит долг.
//
// Правила здесь строже, чем везде: Сёма говорит о пропаже только когда
// уверена в самом ритме. Три письма — не ритм, а совпадение; четыре с
// разбросом в две недели — тоже. Ошибка тут дорога не деньгами, а
// доверием: ассистент, который раз в месяц зря говорит «счёт не пришёл»,
// перестаёт быть ассистентом.

const (
	rhythmMin        = 4 // сколько счетов подряд нужно, чтобы говорить о ритме
	rhythmSpreadDays = 6 // допустимый разброс дня месяца, дней
	rhythmLag        = 5 // на сколько дней позже обычного пора беспокоиться
)

// MissingInvoice — поставщик, чей счёт обычно уже был бы, а его нет.
type MissingInvoice struct {
	Provider   string
	ID         int64
	UsuallyDay int    // около какого числа месяца приходит
	Last       string // когда был последний счёт
	Invoices   int    // на скольких счетах построен ритм
}

// MissingInvoices — у кого ритм есть, а счёта в этом периоде нет.
func (e *Engine) MissingInvoices(now time.Time) []MissingInvoice {
	if !e.PaymentsOn() {
		return nil
	}
	list, err := e.store.Vendors()
	if err != nil {
		return nil
	}
	var out []MissingInvoice
	for _, v := range list {
		if v.Files < rhythmMin {
			continue
		}
		files, err := e.store.FilesOfVendor(v.ID, 60)
		if err != nil {
			continue
		}
		if p, ok := providerRhythm(v, files, now); ok {
			out = append(out, p)
		}
	}
	return out
}

// providerRhythm — есть ли у поставщика месячный ритм и пропущен ли он.
// Отдельная функция без базы: её можно проверить тестом на любых датах, а
// без такой проверки правило «уверены в ритме» держалось бы на честном
// слове.
func providerRhythm(v *store.Vendor, files []*store.Attachment, now time.Time) (MissingInvoice, bool) {
	var days []int
	var dates []time.Time
	for _, f := range files {
		if !f.ToPay {
			continue // ритм строим по счетам, а не по чекам
		}
		d := documentDate(f)
		if d.IsZero() {
			continue
		}
		dates = append(dates, d)
		days = append(days, d.Day())
	}
	if len(dates) < rhythmMin {
		return MissingInvoice{}, false
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })

	// ритм месячный? Считаем по промежуткам между соседними счетами
	for i := 1; i < len(dates); i++ {
		days := int(dates[i].Sub(dates[i-1]).Hours() / 24)
		if days < 20 || days > 45 {
			return MissingInvoice{}, false // не месяц — не наш случай
		}
	}
	// день месяца устойчив?
	minD, maxD := days[0], days[0]
	for _, d := range days {
		if d < minD {
			minD = d
		}
		if d > maxD {
			maxD = d
		}
	}
	if maxD-minD > rhythmSpreadDays {
		return MissingInvoice{}, false
	}

	last := dates[len(dates)-1]
	usually := maxD // берём худший день: говорить раньше срока хуже, чем позже
	// когда счёт ждали в этом периоде
	waited := time.Date(now.Year(), now.Month(), usually, 0, 0, 0, 0, now.Location())
	if usually > daysInMonth(now) {
		waited = time.Date(now.Year(), now.Month(), daysInMonth(now), 0, 0, 0, 0, now.Location())
	}
	if now.Before(waited.AddDate(0, 0, rhythmLag)) {
		return MissingInvoice{}, false // ещё рано беспокоиться
	}
	if !last.Before(waited) {
		return MissingInvoice{}, false // счёт этого периода уже пришёл
	}
	// поставщик мог просто закончиться: если счёта нет два периода подряд,
	// это не пропажа, а конец отношений, и напоминать о нём каждый месяц —
	// то же самое, что не напоминать вовсе
	if now.Sub(last) > 70*24*time.Hour {
		return MissingInvoice{}, false
	}
	return MissingInvoice{
		Provider: v.Name, ID: v.ID, UsuallyDay: usually,
		Last: last.Format("2 января 2006"), Invoices: len(dates),
	}, true
}

func documentDate(f *store.Attachment) time.Time {
	if f.DocDate != "" {
		if t, err := time.Parse("2006-01-02", f.DocDate); err == nil {
			return t
		}
	}
	return f.Date
}

func daysInMonth(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// missingBillsPass — часть задачи «Сроки»: сказать о счёте, который обычно
// уже пришёл бы. Уровень «внимание» и формулировка с «обычно»: Сёма
// говорит о своём наблюдении, а не о чужом долге.
func (e *Engine) missingBillsPass(now time.Time) int {
	said := 0
	for _, p := range e.MissingInvoices(now) {
		if _, err := e.Notify(&store.Notification{
			Key: fmt.Sprintf("nobill:%d:%s", p.ID, now.Format("2006-01")), Source: "mail", Level: "warn",
			Title:   "Счёт от «" + p.Provider + "» в этом месяце не приходил",
			Summary: fmt.Sprintf("обычно приходит около %d числа; последний был %s", p.UsuallyDay, p.Last),
			Fields: []store.NotifyField{
				{Label: "Обычно", Value: fmt.Sprintf("около %d числа", p.UsuallyDay)},
				{Label: "Последний счёт", Value: p.Last},
				{Label: "Ритм по", Value: plural(p.Invoices, "счёту", "счетам", "счетам")},
			},
			Actions: []store.NotifyAction{
				{Label: "Открыть поставщика", Kind: "goto", Screen: "vendors@" + strconv.FormatInt(p.ID, 10)},
				{Label: "Разобрался", Kind: "done"},
			},
		}); err == nil {
			said++
		}
	}
	return said
}
