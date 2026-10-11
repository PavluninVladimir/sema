package engine

// Чистка почтового сервера по дням.
//
// Правило проекта неизменно: письма из архива Сёмы не удаляются никогда.
// Здесь речь только о почтовом сервере — у Сёмы копия остаётся навсегда,
// и именно поэтому чистить вообще можно.
//
// Два срока и две причины. Спам живёт меньше: он уже осуждён, и держать
// его на сервере незачем — 15 дней по умолчанию. Обычные письма живут
// дольше и стираются только с разрешения человека: это его переписка, и
// «срок хранения» не должен однажды утром съесть её молча.
//
// Чистим ПО ДНЯМ, а не папку целиком. У человека в спаме бывают тысячи
// писем; проход «стереть всё» либо упрётся в предел времени задачи
// посередине, либо так нагрузит почтовый сервер, что тот начнёт
// отказывать, а половина дня окажется стёртой, половина нет. День за
// проход виден в журнале, останавливается на любом шаге и всегда закрыт
// целиком.
//
// И ни одно письмо не стирается, пока его текст не лежит у Сёмы:
// archiveBeforePurge сначала докачивает тело, и только потом удаление.

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	spamPolicy = "спам"
	duePolicy  = "срок хранения"
)

// SpamTTL — общий срок: умолчание для ящиков, которые своего не назвали.
func (e *Engine) SpamTTL() int { return e.RetentionDays() }

// SpamTTLForAccount — срок этого ящика. Ноль значит «как у всех».
//
// Поле junk_days у ящика было с самого начала, и всё это время его
// никто не читал: чистка смотрела только в общую настройку. То есть
// человек задавал срок ящику, а работал общий — молча.
func (e *Engine) SpamTTLForAccount(account string) int {
	if c := e.MailConf(account); c != nil && c.JunkDays > 0 {
		return c.JunkDays
	}
	return e.SpamTTL()
}

// RestTTLForAccount — срок обычных писем этого ящика.
func (e *Engine) RestTTLForAccount(account string) int {
	if c := e.MailConf(account); c != nil && c.KeepDays > 0 {
		return c.KeepDays
	}
	return e.RestTTL()
}

// PurgeRestForAccount — стирать ли обычные письма этого ящика.
// Не решено (nil) — как у всех.
func (e *Engine) PurgeRestForAccount(account string) bool {
	if c := e.MailConf(account); c != nil && c.PurgeOther != nil {
		return *c.PurgeOther
	}
	return e.PurgeRest()
}

// PurgeRest — стирать ли с сервера обычные письма по сроку.
// Выключено, пока человек не согласился: это его переписка.
func (e *Engine) PurgeRest() bool {
	return e.store.Setting("mail.purge_other", "") == "1"
}

func (e *Engine) SetPurgeRest(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting("mail.purge_other", v)
}

// RestTTL — через сколько дней обычное письмо стирается с сервера.
func (e *Engine) RestTTL() int {
	if v, err := strconv.Atoi(e.store.Setting("mail.keep_days", "")); err == nil && v > 0 {
		return v
	}
	return 30
}

func (e *Engine) SetRestTTL(v int) error {
	if v < 1 || v > 3650 {
		return fmt.Errorf("срок хранения — от 1 до 3650 дней")
	}
	return e.store.SetSetting("mail.keep_days", strconv.Itoa(v))
}

// dueStatuses — письма, до которых дотягивается «срок хранения».
//
// Спам сюда не входит: у него свой, более короткий срок. Не входят и
// письма, ждущие решения человека (pending, queued) — стереть то, о чём
// ещё не спросили, значит решить за него. И не входит уже стёртое.
var dueStatuses = []string{
	store.StatusInboxOK, store.StatusHam, store.StatusArchived,
	store.StatusServerSpam, store.StatusServerTrash,
}

// DailyPurge — один проход: самый старый день спама и, если человек
// разрешил, самый старый день обычных писем. Возвращает, сколько стёрто.
func (e *Engine) DailyPurge(sess *mail.Session, account string) (int, string) {
	erased := 0
	var report string

	// Срок считается от ДАТЫ ПИСЬМА, а не от того, когда Сёма унёс его
	// в спам.
	//
	// Считали от переноса, и 1 сентября 2026 массовый разбор задним
	// числом поставил 1112 старым письмам один и тот же moved_at. Из-за
	// этого в спаме на почтовом сервере лежала почта за апрель:
	// настройка говорила «стирать спам через 15 дней», а письмо от 28
	// апреля собиралось уехать 16 сентября — и все 1112 разом, одним
	// днём, ровно тем комом, которого чистка по дням и должна избегать.
	//
	// «Через 15 дней» человек читает как «письмо старше 15 дней», и это
	// единственное чтение, которое не удивляет. Цена решения названа
	// прямо: у старого письма, осуждённого сегодня, запаса на «вернуть»
	// нет — оно стирается в ближайший проход.
	day, messages, err := e.spamUnderDue(account)
	if err != nil {
		log.Printf("чистка спама: %v", err)
	} else if len(messages) > 0 {
		n := e.eraseDay(sess, account, sess.JunkFolder(), messages, spamPolicy)
		erased += n
		report = fmt.Sprintf("спам за %s: %d", day, n)
	}

	if !e.PurgeRestForAccount(account) {
		return erased, report
	}
	due := time.Now().AddDate(0, 0, -e.RestTTLForAccount(account))
	day, messages, err = e.store.OldestDay(account, "date", dueStatuses, due)
	if err != nil {
		log.Printf("чистка по сроку: %v", err)
		return erased, report
	}
	if len(messages) == 0 {
		return erased, report
	}
	n := e.eraseDay(sess, account, "", messages, duePolicy)
	erased += n
	if report != "" {
		report += " · "
	}
	report += fmt.Sprintf("по сроку за %s: %d", day, n)
	return erased, report
}

// spamUnderDue — самый старый день спама, которому пора уехать с
// почтового сервера, и письма этого дня.
//
// Отдельной функцией, потому что проверять надо именно ВЫБОР: по какому
// времени считается срок. Тест, который сам зовёт хранилище с нужной
// колонкой, судит свой пересказ, а не этот код, и промолчит, если здесь
// однажды снова окажется «moved_at».
func (e *Engine) spamUnderDue(account string) (string, []*store.Message, error) {
	due := time.Now().AddDate(0, 0, -e.SpamTTLForAccount(account))
	return e.store.OldestDay(account, "date",
		[]string{store.StatusConfirmed, store.StatusAutoMoved}, due)
}

// eraseDay — стереть письма одного дня. Папка пустая означает «искать
// письмо по всем папкам»: обычное письмо могло уехать куда угодно, в том
// числе в архив на сервере.
func (e *Engine) eraseDay(sess *mail.Session, account, folder string, messages []*store.Message, policy string) int {
	n := 0
	for _, m := range messages {
		// без копии у Сёмы не стираем ничего и никогда
		if err := e.archiveBeforePurge(sess, m); err != nil {
			log.Printf("копия %s: %v — удаление отложено", m.MessageID, err)
			continue
		}
		where := folder
		if where == "" {
			box, _, ok := sess.Locate(m.MessageID)
			if !ok {
				// письма на сервере уже нет: записываем как стёртое,
				// иначе каждый проход будет искать его заново
				_ = e.store.LogDeletion(m.ID, account, "нет на сервере", policy)
				continue
			}
			where = box
		}
		erased, err := sess.DeleteFromFolder(where, m.MessageID)
		if err != nil {
			log.Printf("удаление %s из %q: %v", m.MessageID, where, err)
			continue
		}
		if !erased {
			where = "нет на сервере"
		}
		if err := e.store.LogDeletion(m.ID, account, where, policy); err != nil {
			log.Printf("журнал удаления %s: %v", m.MessageID, err)
			continue
		}
		if policy == spamPolicy {
			// прежнее состояние спама сохраняем: «purged» и значит
			// «подтверждённый спам, стёртый с сервера»
			if err := e.store.SetStatus(m.ID, store.StatusPurged, nil); err != nil {
				log.Printf("состояние %d: %v", m.ID, err)
			}
		}
		n++
	}
	return n
}

// MessagesUnderTTL — сколько писем уже старше общего срока хранения.
//
// Число нужно там, где человек решает, включать ли стирание обычной
// переписки. «Стирать письма старше 30 дней» звучит безобидно, пока не
// сказано, что под это уже подпадает полторы тысячи писем и они уедут с
// почтового сервера в ближайшую ночь.
func (e *Engine) MessagesUnderTTL() int {
	due := time.Now().AddDate(0, 0, -e.RestTTL())
	n, err := e.store.CountUnderTTL(dueStatuses, due)
	if err != nil {
		return 0
	}
	return n
}
