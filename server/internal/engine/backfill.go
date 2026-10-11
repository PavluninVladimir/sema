package engine

import (
	"context"

	"github.com/emersion/go-imap/v2"
)

// Глубокий разбор всего ящика: фоновый воркер идёт от новых писем к старым
// пачками, между пачками отпуская IMAP другим задачам (переносы, опрос).
// Прогресс виден в UI; после рестарта продолжает с места остановки —
// уже разобранные письма отсекаются по Message-ID.

const backfillBatch = 25

// BackfillProgress — done/total для UI (0/0 — выключен или ещё не считал).
func (e *Engine) BackfillProgress() (done, total int64) {
	return e.backfillDone.Load(), e.backfillTotal.Load()
}

func (e *Engine) backfillBatch(ctx context.Context) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	sess, err := e.dialAccount(e.conf.Mail)
	if err != nil {
		return 0, err
	}
	defer sess.Close()

	uids, err := sess.AllInboxUIDs()
	if err != nil {
		return 0, err
	}
	e.backfillTotal.Store(int64(len(uids)))

	known, err := e.store.KnownUIDs(e.conf.Mail.Name)
	if err != nil {
		return 0, err
	}
	// «разобрано» — сколько писем ящика уже известно Сёме, а не сколько
	// писем в базе вообще (часть их давно уехала в спам)
	seen := 0
	for _, uid := range uids {
		if known[uint32(uid)] {
			seen++
		}
	}
	e.backfillDone.Store(int64(seen))

	batch := make([]imap.UID, 0, backfillBatch)
	for _, uid := range uids {
		if !known[uint32(uid)] {
			batch = append(batch, uid)
			if len(batch) == backfillBatch {
				break
			}
		}
	}
	if len(batch) == 0 {
		return 0, nil
	}

	msgs, err := sess.FetchByUIDs(batch)
	if err != nil {
		return 0, err
	}
	rules, err := e.store.Rules()
	if err != nil {
		return 0, err
	}
	for _, f := range msgs {
		select {
		case <-ctx.Done():
			return len(msgs), nil
		default:
		}
		if err := e.processFetched(ctx, sess, f, rules, e.conf.Mail.Name, e.MailboxReadOnly(e.conf.Mail.Name)); err != nil {
			return 0, err
		}
		e.backfillDone.Add(1)
	}
	return len(msgs), nil
}
