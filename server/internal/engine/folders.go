package engine

// Разбор всех папок ящика: Сёма проходит по каждой папке (кроме корзины,
// спама, черновиков и виртуальных срезов Gmail), забирает письма к себе в
// базу, помечает их на сервере прочитанными и переносит в папку архива.
// Идём пачками — между пачками IMAP свободен для переносов и опроса.
//
// Письма, ждущие решения человека по спаму, из «Входящих» не трогаем:
// иначе кнопки «В спам» и «Не спам» будут искать письмо там, где его нет.

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const folderBatch = 25

// archiveFolderKey — имя папки архива с прошлого прохода, для настроек.
const archiveFolderKey = "archive_folder"

// ArchiveFolderName — куда Сёма складывает разобранные письма.
func (e *Engine) ArchiveFolderName() string {
	return e.store.Setting(archiveFolderKey, "Archive")
}

// FoldersEnabled — включён ли разбор всех папок (это задача сердцебиения).
func (e *Engine) FoldersEnabled() bool {
	t, err := e.taskBySystem(WorkerFolders)
	return err == nil && t != nil && t.Enabled
}

func (e *Engine) SetFoldersEnabled(on bool) error {
	t, err := e.taskBySystem(WorkerFolders)
	if err != nil || t == nil {
		return err
	}
	return e.store.SetTaskEnabled(t.ID, on)
}

// включитьЗадачу / задачаВключена — общая дверь к тумблеру задачи
// сердцебиения. Нужна там, где у дела есть и настройка, и задача: без неё
// они расходятся, и человек видит «включено» у выключенного.
// ПараВыключателя — настройка, которую щёлкает человек, и задача
// сердцебиения, которая делает работу.
type TogglePair struct {
	Setting, Task, Name string
}

// TogglePairs — ВСЕ такие пары. Список открытый нарочно: по нему
// идёт сверка при старте, и по нему же тест проверяет, что новая пара не
// появилась в обход.
//
// Задача, у которой выключателя-настройки нет, в список не входит и не
// должна: там тумблер на экране щёлкает саму задачу, и разойтись нечему.
var TogglePairs = []TogglePair{
	{Setting: backupOnKey, Task: WorkerBackup, Name: "Копия наружу"},
	{Setting: offloadOnKey, Task: WorkerFilesOffload, Name: "Разгрузка файлов"},
	{Setting: mirrorOnKey, Task: WorkerFilesMirror, Name: "Зеркало файлов"},
}

// VerifyToggles — привести настройку и задачу к согласию.// СверитьВыключатели — привести настройку и задачу к согласию.
//
// 10 сентября 2026 у «Копии наружу» тумблер щёлкнул обе стороны, и с
// тех пор развестись они не могут. Но те, что уже разошлись, так и
// остались: настройка `backup.on` стояла «1», задача сердцебиения —
// выключенной, и копия не снималась ни разу. Человек включил, экран
// сказал «включено», и узнать правду было неоткуда — а резервной копии
// не существовало.
//
// Правим по настройке: её щёлкнул человек, это его решение. Каждое
// расхождение попадает в журнал — молчаливое исправление ничем не лучше
// молчаливой поломки.
func (e *Engine) VerifyToggles() {
	for _, p := range TogglePairs {
		want := e.store.Setting(p.Setting, "0") == "1"
		t, err := e.taskBySystem(p.Task)
		if err != nil || t == nil {
			continue
		}
		if t.Enabled == want {
			continue
		}
		if err := e.enableTask(p.Task, want); err != nil {
			log.Printf("ВНИМАНИЕ: «%s» — настройка и задача спорят, свести не вышло: %v", p.Name, err)
			continue
		}
		state := "включена"
		if !want {
			state = "выключена"
		}
		log.Printf("«%s»: настройка и задача разошлись — задача %s по настройке", p.Name, state)
	}
}

func (e *Engine) enableTask(system string, on bool) error {
	t, err := e.taskBySystem(system)
	if err != nil {
		return err
	}
	if t == nil {
		// молчать здесь нельзя: именно молчание и развело настройку с
		// задачей. Нет строки задачи — значит тумблер ничего не включил, и
		// человек должен это узнать сразу, а не через месяц без копий
		return fmt.Errorf("задачи %q нет в расписании — включать нечего", system)
	}
	if t.Enabled == on {
		return nil
	}
	return e.store.SetTaskEnabled(t.ID, on)
}

func (e *Engine) taskEnabled(system string) bool {
	t, err := e.taskBySystem(system)
	return err == nil && t != nil && t.Enabled
}

func (e *Engine) taskBySystem(system string) (*store.Task, error) {
	tasks, err := e.store.Tasks()
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.System == system {
			return t, nil
		}
	}
	return nil, nil
}

// FoldersProgress — разобрано всего и сколько ещё лежит в папках.
func (e *Engine) FoldersProgress() (done, left int64) {
	return e.foldersDone.Load(), e.foldersLeft.Load()
}

// foldersBatch разбирает одну пачку писем и возвращает, сколько и откуда.
func (e *Engine) foldersBatch(ctx context.Context) (int, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var firstErr error
	for _, acct := range e.Accounts() {
		n, box, err := e.foldersBatchAccount(ctx, acct)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if n > 0 {
			return n, box, err
		}
	}
	return 0, "", firstErr
}

func (e *Engine) foldersBatchAccount(ctx context.Context, acct config.Mail) (int, string, error) {
	sess, conf, err := e.dialConfigured(acct.Name)
	if err != nil {
		return 0, "", err
	}
	defer sess.Close()

	// «только читать» — ничего не переносим и не помечаем, только читаем;
	// то же для «Отправленных» любого ящика: исходящее остаётся на месте
	readOnly := conf.ReadOnly()
	archive := conf.Archive
	if archive == "" && !readOnly {
		if archive, err = sess.EnsureArchive(); err != nil {
			return 0, "", err
		}
		conf.Archive = archive
		_ = e.SaveMailConf(acct.Name, conf)
	}
	if archive != "" {
		_ = e.store.SetSetting(archiveFolderKey, archive)
	}
	sent := sess.SentBoxes()
	boxes, err := sess.SourceMailboxes(archive)
	if err != nil {
		return 0, "", err
	}
	// папки, где решение принял почтовый сервер или человек: спам,
	// корзина, «вся почта». Раньше Сёма их не читал вовсе, и письмо,
	// попавшее туда, для него не существовало. Читаются на месте
	server := map[string]string{}
	if e.ReadServerFolders() {
		server = sess.ServerFolders(archive)
		for name := range server {
			boxes = append(boxes, name)
		}
	}
	// папки, снятые человеком в настройках ящика, не трогаем
	kept := boxes[:0]
	for _, b := range boxes {
		if !conf.Skipped(b) {
			kept = append(kept, b)
		}
	}
	boxes = kept

	// письма на подтверждении спама остаются во «Входящих»
	hold, err := e.store.UIDsByStatus(acct.Name, store.StatusPending, store.StatusQueued)
	if err != nil {
		return 0, "", err
	}

	left := 0
	for _, box := range boxes {
		if n, err := sess.CountIn(box); err == nil {
			left += n
		}
	}
	e.foldersLeft.Store(int64(left))

	for _, box := range boxes {
		select {
		case <-ctx.Done():
			return 0, "", nil
		default:
		}
		uids, err := sess.UIDsIn(box)
		if err != nil {
			log.Printf("folders: %s: %v", box, err)
			continue
		}
		inPlace := readOnPlace(readOnly, sent[box], server[box])
		// папка, которую читаем на месте: письма никуда не уезжают, поэтому
		// помним, докуда дочитали — иначе каждый проход брал бы те же 25 UID
		cursorKey := "folders.cursor." + acct.Name + "." + box
		cursor := uint32(0)
		if inPlace {
			cursor = uint32(settingInt(e.store.Setting(cursorKey, "0")))
		}
		batch := make([]imap.UID, 0, folderBatch)
		if inPlace {
			// от старых к новым, только выше курсора
			var fresh []imap.UID
			for _, u := range uids {
				if uint32(u) > cursor {
					fresh = append(fresh, u)
				}
			}
			sort.Slice(fresh, func(i, j int) bool { return fresh[i] < fresh[j] })
			for _, u := range fresh {
				batch = append(batch, u)
				if len(batch) == folderBatch {
					break
				}
			}
		} else {
			for _, u := range uids {
				// UID уникален внутри папки, поэтому сверяемся только с «Входящими»
				if box == "INBOX" && hold[uint32(u)] {
					continue // ждёт решения человека — пусть лежит во «Входящих»
				}
				batch = append(batch, u)
				if len(batch) == folderBatch {
					break
				}
			}
		}
		if len(batch) == 0 {
			continue
		}

		msgs, err := sess.FetchIn(box, batch)
		if err != nil {
			return 0, box, err
		}
		direction := "in"
		if sent[box] {
			direction = "out"
		}
		status := folderStatus(server[box])
		for _, f := range msgs {
			if err := e.indexArchived(ctx, f, acct.Name, box, direction, status); err != nil {
				log.Printf("folders: сохранить %q: %v", f.Subject, err)
			}
		}
		if inPlace {
			maxUID := uint32(0)
			for _, u := range batch {
				if uint32(u) > maxUID {
					maxUID = uint32(u)
				}
			}
			_ = e.store.SetSetting(cursorKey, strconv.FormatUint(uint64(maxUID), 10))
			e.foldersDone.Add(int64(len(batch)))
			return len(batch), box, nil
		}
		// сначала прочитанные, потом перенос: после MOVE эти UID уже чужие
		if err := sess.MarkSeen(box, batch); err != nil {
			return 0, box, fmt.Errorf("пометить прочитанными в %s: %w", box, err)
		}
		if err := sess.MoveUIDs(box, batch, archive); err != nil {
			return 0, box, fmt.Errorf("перенести в %s: %w", archive, err)
		}
		e.foldersDone.Add(int64(len(batch)))
		if int64(len(batch)) <= e.foldersLeft.Load() {
			e.foldersLeft.Add(-int64(len(batch)))
		}
		return len(batch), box, nil
	}
	return 0, "", nil
}

// indexArchived кладёт письмо в базу Сёмы как разобранное: спам-разбор тут
// не нужен — папки человек уже разложил руками, нам важен архив и поиск.
// читатьНаМесте — трогать ли письма в этой папке. «На месте» значит:
// не помечать прочитанными и никуда не переносить, только прочесть.
// Так читаются отправленные, весь ящик в режиме «только читать» и — с
// 10 сентября 2026 — папки, где решение принял почтовый сервер или сам
// человек: спам провайдера, корзина, «вся почта». Спорить с этим
// решением Сёма не должен, он только перестаёт быть слепым.
func readOnPlace(readOnly, sent bool, kindServerFolders string) bool {
	return readOnly || sent || kindServerFolders != ""
}

// folderStatus — с каким состоянием письмо ложится в архив Сёмы.
// Спам провайдера и корзина получают свои состояния, а не «archived»:
// дайджест «Почты» собирается по списку состояний, и попади они в него
// — сводка «что пришло» утонула бы в чужом спаме.
func folderStatus(kindServerFolders string) string {
	switch kindServerFolders {
	case mail.FolderJunk:
		return store.StatusServerSpam
	case mail.FolderTrash:
		return store.StatusServerTrash
	}
	return store.StatusArchived
}

func (e *Engine) indexArchived(ctx context.Context, f *mail.Fetched, account, folder, direction, status string) error {
	known, err := e.store.KnownMessage(f.MessageID)
	if err != nil || known {
		return err
	}
	m := &store.Message{
		Account:   account,
		MessageID: f.MessageID,
		UID:       f.UID,
		FromAddr:  strings.ToLower(f.FromAddr),
		FromName:  f.FromName,
		Subject:   f.Subject,
		Date:      f.Date,
		Snippet:   f.Snippet,
		InReplyTo: strings.Join(f.InReplyTo, " "),
		Refs:      strings.Join(f.Refs, " "),
		ReplyTo:   f.ReplyTo,
		To:        strings.Join(f.To, " "),
		Cc:        strings.Join(f.Cc, " "),
		ListUnsub: f.ListUnsub,
		Status:    status,
		Reason:    "папка: " + folder,
		Direction: direction,
	}
	var vec []float32
	if e.embedder != nil {
		var eerr error
		vec, eerr = e.embedIfAllowed(ctx, embText(m.FromAddr, m.Subject, m.Snippet))
		if eerr != nil {
			log.Printf("embed %q: %v", m.Subject, eerr)
		}
	}
	if err := e.store.InsertMessage(m); err != nil {
		return err
	}
	e.emitModuleEvent(EventMessageAdded, m.ID)
	e.EnqueueTranslate(m.ID)
	if vec != nil {
		if err := e.store.SaveEmbedding(m.ID, embed.EncodeVec(vec)); err != nil {
			log.Printf("save embedding %d: %v", m.ID, err)
		}
	}
	if status == store.StatusServerSpam {
		e.learnFromServerSpam(m.FromAddr)
	}
	return nil
}

func settingInt(v string) int64 {
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}
