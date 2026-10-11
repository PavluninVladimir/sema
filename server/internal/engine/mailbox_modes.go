package engine

// Назначение и режим ящика. Режим «только читать» проверяют воркеры: без
// фильтра спама, переносов, пометок, очистки и разбора папок — письма и файлы
// при этом всё равно приходят в архив и поиск. Признак «наружу» проверяет
// сборка контекста: письма, файлы и чеки закрытого ящика не уходят облачной
// модели, а сама модель узнаёт, что такие ящики есть.

import (
	"fmt"
	"sort"
	"strings"
)

// MailboxReadOnly — запрещено ли менять что-либо на сервере этого ящика.
func (e *Engine) MailboxReadOnly(account string) bool {
	return e.MailConf(account).ReadOnly()
}

// mailboxClosedForCloud — закрыт ли ящик для облачной модели.
func (e *Engine) mailboxClosedForCloud(account string) bool {
	return !e.MailConf(account).OutboundOn()
}

// ClosedAccounts — ящики, которые нельзя отдавать этой модели: для
// локальной модели закрытых нет.
func (e *Engine) ClosedAccounts(cloud bool) map[string]bool {
	if !cloud {
		return nil
	}
	out := map[string]bool{}
	for _, a := range e.Accounts() {
		if e.mailboxClosedForCloud(a.Name) {
			out[a.Name] = true
		}
	}
	// старые записи основного ящика лежат с пустым именем: без этой строки
	// закрытый основной ящик утекал в контекст облачной модели через письма
	// и вложения, сохранённые до появления имён ящиков
	if out[e.conf.Mail.Name] || (e.conf.Mail.Name == "" && len(out) > 0 && e.mailboxClosedForCloud("")) {
		out[""] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CloudModelsHint — имена облачных моделей для подсказок в настройках:
// «в контекст DeepSeek», а если облачных нет — просто «облачной модели».
func (e *Engine) CloudModelsHint() string {
	var names []string
	for _, m := range e.Models() {
		if e.ModelIsCloud(m.Name) {
			names = append(names, m.Name)
		}
	}
	if len(names) == 0 {
		return "облачной модели"
	}
	sort.Strings(names)
	// подпись, а не перечень: у одного ключа моделей бывает полсотни, и
	// строка «в контекст DeepseekFlash, DeepseekPro, gpt-5.6-luna, …»
	// занимала весь экран, ничего не объясняя
	switch {
	case len(names) <= 3:
		return strings.Join(names, ", ")
	default:
		return fmt.Sprintf("%s и ещё %d облачным", strings.Join(names[:2], ", "), len(names)-2)
	}
}

// closedNotice — строка промпта: модель должна знать, что часть почты ей
// не показана, и назвать ящик, а не сказать «письма нет».
func closedNotice(closed map[string]bool) string {
	if len(closed) == 0 {
		return ""
	}
	names := make([]string, 0, len(closed))
	for n := range closed {
		names = append(names, n)
	}
	sort.Strings(names)
	return "\nMailboxes closed for this model (their emails, files and receipts are NOT in the context): " +
		strings.Join(names, ", ") + ". If the user asks about mail from these mailboxes, say in Russian that the mailbox " +
		"is closed for this model («ящик закрыт для этой модели: содержимое не отправлялось») and that a trusted model can be assigned on the Models screen («назначьте доверенную модель на экране Модели»).\n"
}
