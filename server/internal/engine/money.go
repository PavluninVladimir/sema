package engine

// Признак денег весомее приметы спама.
//
// На живой Сёме 10 сентября 2026: из 104 файлов-чеков двадцать шесть лежат
// в письмах со статусом «спам подтверждён», ещё три — в архиве. То есть
// счета и квитанции уехали в спам-корзину почтового сервера, откуда их
// через тридцать дней удалит сам сервер.
//
// Выученное правило «этот отправитель уже подтверждён как спамер» имеет
// уверенность 1.00 и переносит письмо само, без человека. Для рассылки
// магазина это правильно. Для «Счёт-квитанция за август» от того же
// магазина — нет: рассылка и счёт приходят с одного адреса.
//
// Правило простое: письмо, в котором виден счёт или чек, Сёма сам никуда
// не переносит. Оно становится решением человека — тем же «ждёт решения»,
// каким становится всё сомнительное. Человек по-прежнему может отправить
// его в спам кнопкой; Сёма — нет.
//
// Слова-приметы живут в настройках, а не в коде: у каждого свои
// поставщики и свой язык счетов.

import (
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	moneyWordsKey  = "mail.money_words"
	moneyGuardKey  = "mail.money_guard"
	moneyWordsSeed = "счёт, счет, квитанция, чек, к оплате, оплатить, оплата, задолженность, invoice, receipt, payment due, платёж, платеж, лицевой счёт, начислено, пени"
)

// MoneyGuard — бережём ли письма с признаком денег от самостоятельного
// переноса в спам. По умолчанию да: потерянный счёт дороже лишней строки
// на экране.
func (e *Engine) MoneyGuard() bool { return e.store.Setting(moneyGuardKey, "1") == "1" }

func (e *Engine) SetMoneyGuard(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(moneyGuardKey, v)
}

// MoneyWords — слова, по которым письмо похоже на счёт.
func (e *Engine) MoneyWords() []string {
	raw := e.store.SettingText(moneyWordsDef)
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(strings.ToLower(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (e *Engine) SetMoneyWords(list string) error {
	return e.store.SetSetting(moneyWordsKey, strings.TrimSpace(list))
}

// moneyInMessage — почему это письмо похоже на счёт. Пусто — не похоже.
// Строка возвращается человеческая: она попадёт в причину и объяснит, за
// что письмо задержано.
func (e *Engine) moneyInMessage(m *store.Message) string {
	if m == nil || !e.MoneyGuard() {
		return ""
	}
	// первая примета — история, а не слова: отправитель, от которого уже
	// приходили чеки, весомее любого списка. Её нельзя подделать темой
	if n := e.store.ReceiptsFromSender(m.FromAddr); n > 0 {
		return "от этого отправителя уже приходили чеки"
	}
	haystack := strings.ToLower(m.Subject + " " + m.Snippet)
	for _, word := range e.MoneyWords() {
		if strings.Contains(haystack, word) {
			return "в письме есть «" + word + "»"
		}
	}
	return ""
}

// holdForMoney — если письмо похоже на счёт, вернуть статус и
// причину, с которыми оно останется ждать человека. Второе значение —
// «придержали». Одна дверь на все пути самостоятельного переноса: два
// места с этой проверкой разошлись бы через месяц.
func (e *Engine) holdForMoney(m *store.Message, whatWas string) bool {
	sign := e.moneyInMessage(m)
	if sign == "" {
		return false
	}
	m.Status = store.StatusPending
	m.Confidence = 0
	m.Reason = whatWas + ", но " + sign + " — переносить не стал, решите сами"
	m.MovedAt = nil
	return true
}
