package engine

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Одно дело — одна запись, и закрывает её то же дело.
//
// 13 сентября 2026 в «Уведомлениях» висели три строки об одном и том же
// продлении сервера: Hosting присылает напоминание каждый день, а ключ
// записи считался по номеру письма (`mail:2324`), поэтому каждое новое
// письмо заводило новую запись вместо замены прежней. Ключ должен
// называть дело, а не письмо: отправитель плюс суть темы без чисел и
// дат — «пополнить баланс на 512,30 руб. для продления сервера RU2540»
// и «…на 512,30 руб. для продления сервера» это одно дело.
//
// И то же дело умеет себя закрывать: человек оплатил, провайдер прислал
// «платёж получен» — записи больше нечего ждать. Закрывает не любое
// письмо того же отправителя, а только то, которое говорит об оплате
// (`словаОплаты`), и только записи, заведённые раньше этого письма.

// ConsolidateMattersOnce — разовое сведение записей, заведённых до того, как
// ключ стал называть дело. Трёх строк об одном продлении новый код сам не
// склеит: у них уже разные ключи. Оставляем свежую, старые помечаем
// «заменена» — как сделало бы ядро, будь ключ делом с самого начала.
// Заодно закрываем то, что человек уже оплатил: письмо об оплате от того
// же отправителя пришло раньше, чем появился разбор.
func (e *Engine) ConsolidateMattersOnce() {
	const label = "notify.deals_merged"
	if e.store.Setting(label, "") != "" {
		return
	}
	defer func() { _ = e.store.SetSetting(label, "1") }()
	list, err := e.store.Notifications(true, 500)
	if err != nil {
		return
	}
	// порядок задаём сами: оставить надо запись о самом свежем письме, а
	// список отдаётся по уровню и времени правки
	sort.SliceStable(list, func(i, j int) bool {
		return notificationMessage(list[i]) > notificationMessage(list[j])
	})
	seen := map[string]bool{}
	consolidated, paid := 0, 0
	for _, rec := range list {
		id := notificationMessage(rec)
		if id == 0 {
			continue
		}
		m, err := e.store.Message(id)
		if err != nil {
			continue
		}
		matter := messageMatter(m.FromAddr, m.Subject)
		if matter == "" {
			continue
		}
		if seen[matter] {
			_ = e.store.SetNotificationState(rec.ID, store.NotifyReplaced, "то же дело уже есть записью свежее")
			consolidated++
			continue
		}
		seen[matter] = true
		// оплату могли подтвердить письмом до того, как разбор появился
		if e.paidAfter(m.FromAddr, rec.Created) {
			_ = e.store.SetNotificationState(rec.ID, store.NotifyDone, "платёж прошёл — отправитель подтвердил письмом")
			paid++
		}
	}
	if consolidated+paid > 0 {
		log.Printf("уведомления: свёл двойников дел %d, закрыл оплаченных %d", consolidated, paid)
	}
}

// ClosePaid — открытые почтовые записи, дело которых человек уже
// оплатил: отправитель подтвердил письмом позже, чем запись появилась.
// Зовётся задачей «Сроки» каждый час, а не разово: подтверждение приходит
// когда придёт, и ждать его должен Сёма, а не человек.
func (e *Engine) ClosePaid() int {
	list, err := e.store.Notifications(true, 500)
	if err != nil {
		return 0
	}
	n := 0
	for _, rec := range list {
		if rec.Source != "mail" {
			continue
		}
		id := notificationMessage(rec)
		if id == 0 {
			continue
		}
		m, err := e.store.Message(id)
		if err != nil || m.FromAddr == "" {
			continue
		}
		if !e.paidAfter(m.FromAddr, rec.Created) {
			continue
		}
		if err := e.store.SetNotificationState(rec.ID, store.NotifyDone,
			"платёж прошёл — отправитель подтвердил письмом"); err == nil {
			n++
		}
	}
	return n
}

// paidAfter — после этой минуты от отправителя приходило письмо об
// оплате. Смотрим архив, а не только новые письма: разбор появился
// позже, чем сама оплата.
func (e *Engine) paidAfter(from, since string) bool {
	if from == "" {
		return false
	}
	msgs, err := e.store.MessagesFromSender(from, 50)
	if err != nil {
		return false
	}
	threshold, _ := time.Parse(time.RFC3339, since)
	for _, m := range msgs {
		if !threshold.IsZero() && m.Date.Before(threshold) {
			continue
		}
		if messageAboutPayment(m.Subject, m.Snippet) {
			return true
		}
	}
	return false
}

// digitsAndDates — то, что меняется от письма к письму и делом не является.
var digitsAndDates = regexp.MustCompile(`[0-9]+([.,:/-][0-9]+)*`)

// paymentWords — приметы того, что дело закрыто самим отправителем.
// Не «оплатить» и не «к оплате»: это просьба, а не подтверждение.
var paymentWords = []string{
	"платёж получен", "платеж получен", "оплата получена", "оплата прошла",
	"успешно оплачен", "успешная оплата", "спасибо за оплату", "спасибо за платёж",
	"баланс пополнен", "счёт пополнен", "счет пополнен", "зачислен",
	// как пишут провайдеры на самом деле (живые письма 13 сентября 2026):
	// «Подтверждение оплаты» (Hosting), «Уведомление о зачислении платежа»
	// (ITSHOP), «Произведена оплата» (Pally)
	"подтверждение оплаты", "подтверждение платежа", "зачислении платежа",
	"зачисление платежа", "произведена оплата", "оплата принята", "платёж принят",
	"платеж принят", "оплата подтверждена", "поступила оплата", "поступил платёж",
	"payment received", "payment successful", "paid in full", "thank you for your payment",
	"payment confirmation", "payment confirmed",
}

// messageMatter — ключ дела: отправитель и суть темы. Пустая тема даёт
// пустой ключ — тогда зовущий оставляет ключ по письму.
func messageMatter(from, subject string) string {
	gist := topicGist(subject)
	addr := strings.ToLower(strings.TrimSpace(from))
	if gist == "" || addr == "" {
		return ""
	}
	return fmt.Sprintf("дело:%s:%s", addr, gist)
}

// topicGist — тема без чисел, дат, приставок «Re:»/«Fwd:» и лишних
// пробелов. Суммы и номера счетов меняются в каждом письме, а дело одно.
func topicGist(subject string) string {
	s := strings.ToLower(strings.TrimSpace(subject))
	for {
		cut := false
		for _, p := range []string{"re:", "fwd:", "fw:", "[spam]", "re[2]:", "автоответ:"} {
			if strings.HasPrefix(s, p) {
				s, cut = strings.TrimSpace(strings.TrimPrefix(s, p)), true
			}
		}
		if !cut {
			break
		}
	}
	s = digitsAndDates.ReplaceAllString(s, " ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsSpace(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// messageAboutPayment — письмо говорит, что платёж прошёл.
func messageAboutPayment(subject, text string) bool {
	s := strings.ToLower(subject + " " + text)
	for _, w := range paymentWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// closeSenderMatters — пришло письмо об оплате: открытые записи по
// делам того же отправителя закрываются сами. Возвращает, сколько
// закрыл: по этому числу видно в журнале задачи, что сработало.
func (e *Engine) closeSenderMatters(from, subject, text string) int {
	if !messageAboutPayment(subject, text) {
		return 0
	}
	addr := strings.ToLower(strings.TrimSpace(from))
	if addr == "" {
		return 0
	}
	list, err := e.store.Notifications(true, 500)
	if err != nil {
		return 0
	}
	prefix := "дело:" + addr + ":"
	n := 0
	for _, rec := range list {
		if !strings.HasPrefix(rec.Key, prefix) {
			continue
		}
		if err := e.store.SetNotificationState(rec.ID, store.NotifyDone,
			"платёж прошёл — отправитель подтвердил письмом"); err == nil {
			n++
		}
	}
	return n
}
