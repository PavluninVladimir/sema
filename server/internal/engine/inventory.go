package engine

// Опись: что вообще есть у Сёмы и откуда это взялось. Уходит в системную
// часть промпта — она маленькая и меняется редко. Сами данные туда не
// кладём: суммы и списки подставляются выборкой под конкретный вопрос,
// иначе промпт распухнет и начнёт устаревать.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

var (
	invMu   sync.Mutex
	invText string
	invAt   time.Time
)

// Inventory — краткая опись источников. Пересчитывается не чаще раза в минуту.
// machineLine — строка описи про свою машину: модель отвечает на «как
// твоё железо» без команды.
func machineLine() string {
	m := ReadMachine()
	if !m.HasTemp && m.Load1 == 0 {
		return ""
	}
	return "Машина Сёмы сейчас: " + m.String() + ".\n"
}

func (e *Engine) Inventory() string {
	invMu.Lock()
	defer invMu.Unlock()
	if time.Since(invAt) < time.Minute && invText != "" {
		return invText
	}
	invText, invAt = e.buildInventory(), time.Now()
	machine := machineLine()
	return machine + invText
}

func (e *Engine) buildInventory() string {
	st := e.store
	var b strings.Builder
	b.WriteString("\nWhat Sema has (sources of truth):\n")

	var boxes []string
	for _, a := range e.Accounts() {
		boxes = append(boxes, a.Username)
	}
	fmt.Fprintf(&b, "- mailboxes: %s\n", strings.Join(boxes, ", "))

	total, first, last := st.MessageStats()
	fmt.Fprintf(&b, "- emails in local archive: %d, from %s to %s (kept forever, even after deletion from the server)\n",
		total, first, last)

	pending, _ := st.CountByStatus(store.StatusPending)
	spam, _ := st.CountByStatus(store.StatusConfirmed)
	auto, _ := st.CountByStatus(store.StatusAutoMoved)
	ham, _ := st.CountByStatus(store.StatusHam)
	archived, _ := st.CountByStatus(store.StatusArchived)
	fmt.Fprintf(&b, "- email statuses: %d awaiting spam decision, %d confirmed spam, %d auto-moved, %d whitelisted, %d filed to Archive\n",
		pending, spam, auto, ham, archived)

	files, withText := st.AttachmentStats()
	fmt.Fprintf(&b, "- attached files: %d saved locally, %d with extracted text (PDF, office documents, images via OCR)\n",
		files, withText)

	// чеки — это сами файлы вида «чек»: получатель, сумма и дата уже разобраны
	if n, sum, first, last := st.ReceiptStats(); n > 0 {
		fmt.Fprintf(&b, "- receipts and invoices: %d, %.0f RUB in total, from %s to %s — vendor, amount and date are already parsed\n",
			n, sum, first, last)
	}

	if tasks, err := st.Tasks(); err == nil {
		fmt.Fprintf(&b, "- background tasks: %d (mail polling, archiving, attachments, receipts, folder filing, spam cleanup)\n", len(tasks))
	}
	if scripts, err := st.Scripts(); err == nil && len(scripts) > 0 {
		names := make([]string, 0, len(scripts))
		for _, s := range scripts {
			names = append(names, s.Title)
		}
		fmt.Fprintf(&b, "- agent scripts: %s\n", strings.Join(names, ", "))
	}
	// выписку в промпт давал модуль «Чеки»; он снят, и обещать её было бы
	// неправдой. Чек теперь — файл с суммой, и считать надо по файлам
	b.WriteString("A receipt is a file with an amount: fields vendor, amount, currency on the file record. " +
		"When the question is about amounts for a period, sum the relevant files yourself and say which ones you counted; " +
		"if the files in context do not cover the period, say so instead of guessing.\n")
	return b.String()
}
