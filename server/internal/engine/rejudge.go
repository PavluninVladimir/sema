package engine

// Досуд: письма, уехавшие в архив мимо разбора.
//
// Обход папок кладёт письмо в архив «как разобранное» — папки человек
// разложил руками, и судить их незачем. Беда была в том, что в список
// папок попадали и «Входящие», а ходит обход раз в двадцать секунд против
// пяти минут у разбора. С 2 сентября 2026 сорок одно письмо уехало в архив
// со статусом «archived», уверенностью 0 и причиной «папка: INBOX» —
// включая «Счет от АО "Горэнерго"». Ни суда, ни спам-фильтра, ни
// строки во «Входящих».
//
// Дорогу починили (`mail.ВходящиеЛи`), но письма остались неразобранными.
// Здесь — разбор задним числом. Правило одно: **ничего не переносим**.
// Письмо уже лежит в архиве, суд задним числом даёт только суждение;
// спам-вердикт становится «ждёт решения», и переносит его человек кнопкой,
// как всегда.

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// WorkMsgJudge — вид работы очереди: одно письмо, один суд.
const WorkMsgJudge = "msg_judge"

const preCourtKey = "mail.rejudge_done"

// EnqueueJudge — поставить письмо на разбор задним числом.
func (e *Engine) EnqueueJudge(id int64) {
	if id == 0 {
		return
	}
	_ = e.store.Enqueue(WorkMsgJudge, strconv.FormatInt(id, 10), "", 4)
}

// RejudgeUnjudged — разовый проход: поставить в очередь всё, что уехало в
// архив мимо суда. Идемпотентен по метке, как остальные переезды; сам
// разбор идёт очередью, по одному письму с паузой.
func (e *Engine) RejudgeUnjudged() int {
	if e.store.Setting(preCourtKey, "") == "1" {
		return 0
	}
	list, err := e.store.MessagesUnjudged(500)
	if err != nil {
		log.Printf("досуд: не выбрать письма: %v", err)
		return 0
	}
	for _, id := range list {
		e.EnqueueJudge(id)
	}
	_ = e.store.SetSetting(preCourtKey, "1")
	if len(list) > 0 {
		log.Printf("досуд: поставлено в очередь %d писем, уехавших в архив мимо разбора", len(list))
	}
	return len(list)
}

// handleMsgJudge — одна работа очереди: рассудить одно письмо.
func (e *Engine) handleMsgJudge(ctx context.Context, w *store.WorkItem) error {
	id, err := strconv.ParseInt(w.Ref, 10, 64)
	if err != nil {
		return fmt.Errorf("непонятная ссылка %q", w.Ref)
	}
	m, err := e.store.Message(id)
	if err != nil {
		return nil // письма больше нет
	}
	if !withoutCourt(m) {
		return nil // кто-то уже рассудил: второй раз незачем
	}
	if until, resting := e.roleCooldown(RoleFilter); resting {
		return postpone(time.Until(until)+time.Minute, "роль «Фильтр писем» на передышке")
	}
	// судим той же дверью, что и свежую почту: отдельный путь к модели
	// разошёлся бы с основным через месяц
	was := m.Status
	vec, _ := e.embedIfAllowed(ctx, embText(m.FromAddr, m.Subject, m.Snippet))
	e.classify(ctx, e.filterModel(m), m, e.ragExamples(vec))
	if m.Status == was || m.Reason == "" {
		return nil
	}
	// письмо уже в архиве на сервере — переносов не делаем ни одного.
	// «Не спам» так и остаётся в архиве: доставать его обратно во
	// «Входящие» значило бы шуметь задним числом о том, что человек и так
	// прочитал. «Спам» становится решением человека, а не Сёмы
	status := store.StatusArchived
	if m.Status == store.StatusPending {
		status = store.StatusPending
	}
	reason := m.Reason + " · разобрано задним числом"
	if err := e.store.SetStatusFull(id, status, reason, m.Confidence); err != nil {
		return err
	}
	log.Printf("досуд: письмо %d (%q) — %s (%.0f%%)", id, clip(m.Subject, 60), status, m.Confidence*100)
	// правила уведомлений теперь видят разобранное письмо: срок оплаты из
	// счёта, который уехал в архив молча, — это и есть главная потеря
	go e.applyMailRules(context.Background(), id)
	return nil
}

// withoutCourt — письмо, которое никто не судил: обход папок забрал его из
// «Входящих» раньше разбора. Именно из «Входящих», а не из любой папки:
// письмо, которое человек сам положил в «Счета», уже рассужено — им же.
func withoutCourt(m *store.Message) bool {
	return m.Status == store.StatusArchived && m.Confidence == 0 &&
		m.Reason == "папка: INBOX"
}

// CancelWidePreCourt — разовая уборка за собой.
//
// 10 сентября 2026 первый досуд взял письма из всех папок, а не только из
// «Входящих», и успел рассудить тридцать штук: шестнадцать из них встали
// «ждут решения», хотя человек давно разложил их руками. Переносов не
// было — досуд их не делает, — но на экране это шум, которого человек не
// просил.
//
// Возвращаем как было: в архив, без уверенности и с прежней причиной. Что
// из них пришло из «Входящих», уже не различить, поэтому исправленный
// досуд разберёт их заново — теперь по правильному отбору.
func (e *Engine) CancelWidePreCourt() int {
	if e.store.Setting("mail.rejudge_wide_undone", "") == "1" {
		return 0
	}
	// сперва снимаем из очереди то, что поставлено по ошибке: иначе
	// исправленный отбор будет соревноваться с прежним списком
	if removed, err := e.store.DropAllWork(WorkMsgJudge); err == nil && removed > 0 {
		log.Printf("отмена широкого досуда: снято из очереди %d работ", removed)
	}
	n, err := e.store.UndoRejudge()
	if err != nil {
		log.Printf("отмена широкого досуда: %v", err)
		return 0
	}
	_ = e.store.SetSetting("mail.rejudge_wide_undone", "1")
	// метку самого досуда снимаем: он пройдёт заново по узкому отбору
	_ = e.store.SetSetting(preCourtKey, "")
	if n > 0 {
		log.Printf("отмена широкого досуда: возвращено %d писем", n)
	}
	return n
}

// FixServiceReason — разовая правка текста, попавшего в графу
// «Почему» у тридцати писем при уборке за широким досудом.
func (e *Engine) FixServiceReason() {
	if e.store.Setting("mail.reason_note_fixed", "") == "1" {
		return
	}
	n, err := e.store.FixRepairNote()
	if err != nil {
		log.Printf("правка причины: %v", err)
		return
	}
	_ = e.store.SetSetting("mail.reason_note_fixed", "1")
	if n > 0 {
		log.Printf("правка причины: переписано %d писем", n)
	}
}
