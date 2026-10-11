package engine

import (
	"context"
	"log"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Архиватор: сразу после разбора докачивает письма целиком (текст + HTML)
// в локальную базу. Так копия есть задолго до того, как письмо удалят с
// сервера, и переживает любое удаление в почте.

const archiveBatch = 40

func (e *Engine) archivePass(ctx context.Context) int {
	saved := 0
	for _, acct := range e.Accounts() {
		select {
		case <-ctx.Done():
			return saved
		default:
		}
		saved += e.archiveAccount(acct)
	}
	return saved
}

func (e *Engine) archiveAccount(acct config.Mail) int {
	msgs, err := e.store.MessagesWithoutBody(acct.Name, archiveBatch)
	if err != nil || len(msgs) == 0 {
		return 0
	}
	// своё соединение, без общей блокировки: иначе архиватор простаивает,
	// пока разбор почты держит mu минутами
	sess, err := e.dialAccount(acct)
	if err != nil {
		log.Printf("archive dial %s: %v", acct.Name, err)
		return 0
	}
	defer sess.Close()

	// письмо ищем там, где оно реально лежит: человек мог переложить его
	// в свою папку, и UID при этом изменился
	byBox := map[string]map[uint32]int64{}
	for _, m := range msgs {
		prefer := []string{"INBOX"}
		switch m.Status {
		case store.StatusConfirmed, store.StatusAutoMoved, store.StatusQueued:
			prefer = []string{sess.JunkFolder(), "INBOX"}
		}
		box, uid, ok := sess.Locate(m.MessageID, prefer...)
		if !ok {
			continue
		}
		if byBox[box] == nil {
			byBox[box] = map[uint32]int64{}
		}
		byBox[box][uid] = m.ID
	}

	saved := 0
	save := func(mailbox string, byUID map[uint32]int64) {
		if len(byUID) == 0 {
			return
		}
		uids := make([]uint32, 0, len(byUID))
		for uid := range byUID {
			uids = append(uids, uid)
		}
		texts, err := sess.FetchRichIn(mailbox, uids)
		if err != nil {
			log.Printf("archive fetch %s: %v", mailbox, err)
			return
		}
		for uid, rb := range texts {
			if rb.Text == "" && rb.HTML == "" {
				continue
			}
			if err := e.store.SaveBody(byUID[uid], store.Body{Text: rb.Text, HTML: rb.HTML}); err != nil {
				log.Printf("archive save: %v", err)
				continue
			}
			saved++
		}
	}
	for box, ids := range byBox {
		save(box, ids)
	}

	// то, что не нашлось ни в одной папке, закрепляем хотя бы сниппетом,
	// иначе архиватор будет бесконечно возвращаться к этим письмам
	for _, m := range msgs {
		if bodies, err := e.store.Bodies([]int64{m.ID}); err == nil {
			if _, ok := bodies[m.ID]; ok {
				continue
			}
		}
		_ = e.store.SaveBody(m.ID, store.Body{Text: mail.Sanitize(m.Snippet)})
	}
	if saved > 0 {
		log.Printf("archive %s: сохранено писем %d", acct.Name, saved)
	}
	return saved
}

// ArchiveProgress — сколько писем ещё без локальной копии (для футера).
func (e *Engine) ArchiveProgress() (missing, total int) {
	m, t, err := e.store.CountWithoutBody()
	if err != nil {
		return 0, 0
	}
	return m, t
}
