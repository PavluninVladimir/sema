package engine

// Файлы как сущность ядра: приём из любого источника, хранение по хэшу в
// основном хранилище, отдача байтов только через сервер, переезд старых
// вложений. Письмо у файла необязательно.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/storage"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const WorkerFilesMigrate = "files_migrate"

// FileSource — откуда пришёл файл.
type FileSource struct {
	Kind    string // mail | upload | folder | module
	Ref     string // id письма, путь папки, имя модуля
	Account string // ящик, если из почты
}

// storageByName — хранилище из справочника; реализовано локальное, остальные
// виды пока не поднимаются (придут с files-s3).
func (e *Engine) storageByName(name string) storage.Backend {
	if name == "" {
		name = e.store.PrimaryStorage()
	}
	if name == "local" {
		return storage.NewLocal("local", e.conf.Files.Dir)
	}
	confs, _ := e.store.Storages()
	for _, c := range confs {
		if c.Name != name {
			continue
		}
		switch c.Kind {
		case "local":
			return storage.NewLocal(c.Name, e.conf.Files.Dir)
		case "s3":
			b, err := e.s3Backend(c)
			if err != nil {
				return nil
			}
			return b
		}
	}
	return nil
}

// s3Backend — S3-хранилище из справочника: ключи по именам секретов,
// адрес через hostAlias (MinIO на маке виден из контейнера как хозяин).
func (e *Engine) s3Backend(c *store.StorageConf) (*storage.S3, error) {
	var conf storage.S3Config
	if err := json.Unmarshal([]byte(c.Config), &conf); err != nil {
		return nil, fmt.Errorf("настройки %s: %w", c.Name, err)
	}
	access, ok1 := e.coreSecret(conf.AccessSecret, "ядро · хранилище "+c.Name)
	secret, ok2 := e.coreSecret(conf.SecretSecret, "ядро · хранилище "+c.Name)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("нет секретов %q и %q в хранилище секретов", conf.AccessSecret, conf.SecretSecret)
	}
	scheme := "http://"
	if conf.SSL {
		scheme = "https://"
	}
	conf.Endpoint = strings.TrimPrefix(strings.TrimPrefix(hostAlias(scheme+conf.Endpoint), "https://"), "http://")
	return storage.NewS3(c.Name, conf, access, secret)
}

// YaDiskConf — настройки хранилища на Яндекс.Диске. Пароль лежит не
// здесь, а в хранилище секретов: тут только его имя, как у S3.
type YaDiskConf struct {
	// Токен — имя секрета с OAuth-токеном Диска, а не сам токен.
	// Единственная дверь: WebDAV снят 11 сентября 2026, потому что у
	// шлюза нельзя спросить, что у него лежит, — он принимал файл
	// целиком, сохранял и закрывал соединение без ответа.
	Token  string `json:"token_secret"`
	Folder string `json:"folder"` // куда класть внутри Диска
	// Логин и адрес остались от WebDAV и больше ни на что не влияют:
	// токен сам говорит, чей это Диск. Поля читаются, чтобы старая
	// запись справочника не рассыпалась, и не пишутся заново.
	Login    string `json:"login,omitempty"`
	Addr     string `json:"address,omitempty"`
	Password string `json:"password_secret,omitempty"`
}

// webdavBackend — хранилище на Диске по записи справочника.
func (e *Engine) yaDiskBackend(c *store.StorageConf) (*storage.YaDisk, error) {
	var conf YaDiskConf
	if err := json.Unmarshal([]byte(c.Config), &conf); err != nil {
		return nil, fmt.Errorf("настройки %s: %w", c.Name, err)
	}
	if strings.TrimSpace(conf.Token) == "" {
		return nil, fmt.Errorf("у хранилища %q не назван секрет с токеном Диска: задайте его на «Хранилищах» кнопкой «Токен для копий»", c.Name)
	}
	token, ok := e.coreSecret(conf.Token, "ядро · хранилище "+c.Name)
	if !ok {
		return nil, fmt.Errorf("нет секрета %q в хранилище секретов: заведите токен Диска на «Ключах и секретах»", conf.Token)
	}
	return storage.NewYaDisk(c.Name, token, conf.Folder), nil
}

// coreSecret — секрет с записью в журнал. Ядро читает ключи так же, как
// их читают модули: человек должен видеть каждое обращение. У S3 этой
// записи не было, и обращения к его ключам в журнале не появлялись.
func (e *Engine) coreSecret(name, who string) (string, bool) {
	if name == "" {
		return "", false
	}
	v, ok := e.store.SecretValue(name)
	note := "прочитан"
	if !ok {
		note = "не прочитан: нет такого секрета или он не открывается нынешним ключом"
	}
	e.store.LogSecret(store.SecretUse{Name: name, Who: who, Granted: ok, Note: note})
	return v, ok
}

// MirrorStorage — зеркало из справочника: первое не местное. Раньше здесь
// стояло «первое S3», и завести зеркало другого вида было нельзя, хотя
// весь остальной путь про вид не спрашивает.
func (e *Engine) MirrorStorage() *store.StorageConf {
	confs, _ := e.store.Storages()
	for _, c := range confs {
		if c.Kind != "" && c.Kind != "local" {
			return c
		}
	}
	return nil
}

// backendByConf — хранилище по записи справочника. Одна дверь для всех
// видов: зеркало, «Проверить» и разгрузка ходят сюда, а не строят S3
// руками, как было в трёх местах сразу.
func (e *Engine) backendByConf(c *store.StorageConf) (storage.Backend, error) {
	switch c.Kind {
	case "local":
		return storage.NewLocal(c.Name, e.conf.Files.Dir), nil
	case "s3":
		return e.s3Backend(c)
	case "yadisk":
		return e.yaDiskBackend(c)
	case "webdav":
		// прежнее имя вида: запись справочника переименовывает миграция,
		// но и без неё старая строка должна работать, а не падать
		return e.yaDiskBackend(c)
	}
	return nil, fmt.Errorf("хранилище вида %q Сёма не умеет", c.Kind)
}

// checkable — хранилище, которое умеет рассказать о себе одной строкой.
type checkable interface {
	Check() (string, error)
}

// FilesDir — папка локального хранилища на этой машине: где лежат файлы.
func (e *Engine) FilesDir() string { return e.conf.Files.Dir }

// CheckStorage — кнопка «Проверить»: соединение, bucket, запись и чтение.
func (e *Engine) CheckStorage(name string) (string, error) {
	confs, _ := e.store.Storages()
	for _, c := range confs {
		if c.Name != name {
			continue
		}
		if c.Kind == "local" {
			f, b := storage.NewLocal(c.Name, e.conf.Files.Dir).Stat()
			return fmt.Sprintf("папка %s · %d файлов · %s", e.conf.Files.Dir, f, humanBytes(b)), nil
		}
		b, err := e.backendByConf(c)
		if err != nil {
			e.store.NoteStorage(name, "ошибка: "+err.Error())
			return "", err
		}
		self, ok := b.(checkable)
		if !ok {
			return "", fmt.Errorf("хранилище %q не умеет рассказать о себе", name)
		}
		note, err := self.Check()
		if err != nil {
			e.store.NoteStorage(name, "ошибка: "+err.Error())
			return "", err
		}
		e.store.NoteStorage(name, note+" · "+time.Now().Format("02.01 15:04"))
		return note, nil
	}
	return "", fmt.Errorf("хранилище %q не найдено", name)
}

const WorkerFilesMirror = "files_mirror"

const mirrorOnKey = "mirror.on"

// MirrorOn — уезжают ли файлы в зеркало. Настройка рядом с задачей, как
// у копии: одним тумблером щёлкаются обе.
//
// До 11 сентября 2026 выключателя не было вовсе — задача «Зеркало
// файлов» жила только на «Задачах». Её выключили 3 сентября при
// переезде с MinIO, и месяц ни один файл никуда не уезжал, а на
// «Хранилищах» стояло честное «зеркало» без единого слова о том, что
// оно стоит. 260 файлов всё это время лежали в одном экземпляре на
// карточке Raspberry Pi.
func (e *Engine) MirrorOn() bool { return e.store.Setting(mirrorOnKey, "0") == "1" }

// MirrorTaskOn — включена ли сама задача: экран показывает расхождение.
func (e *Engine) MirrorTaskOn() bool { return e.taskEnabled(WorkerFilesMirror) }

func (e *Engine) SetMirrorOn(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	if err := e.store.SetSetting(mirrorOnKey, v); err != nil {
		return err
	}
	return e.enableTask(WorkerFilesMirror, on)
}

// MirrorPass — сколько файлов уже там и сколько всего.
func (e *Engine) MirrorPass() (fits, total int) {
	m := e.MirrorStorage()
	if m == nil {
		return 0, 0
	}
	return e.store.MirrorProgress(m.Name)
}

// mirrorPass — зеркало догоняет: новые файлы уезжают в S3 по 20 за проход.
func (e *Engine) mirrorPass(ctx context.Context) string {
	m := e.MirrorStorage()
	if m == nil {
		return "зеркала нет"
	}
	back, err := e.backendByConf(m)
	if err != nil {
		e.store.NoteStorage(m.Name, "ошибка: "+err.Error())
		return "ошибка: " + err.Error()
	}
	return e.mirrorBatch(ctx, m.Name, back)
}

// mirrorBatch — сам проход, отдельно от поиска хранилища: настоящий
// Диск в тесте не поднять, а проверять надо этот код, а не его копию в
// тесте.
func (e *Engine) mirrorBatch(ctx context.Context, name string, back storage.Backend) string {
	list, err := e.store.FilesToMirror(name, 20)
	if err != nil {
		return "ошибка: " + err.Error()
	}
	done, trouble := 0, 0
	firstTrouble := ""
	for _, a := range list {
		select {
		case <-ctx.Done():
			return "прервано"
		default:
		}
		data, err := e.FileBytes(a)
		if err != nil {
			continue
		}
		// Один упавший файл не останавливает остальные.
		//
		// 11 сентября 2026 зеркало догоняло 260 файлов и на 133-м
		// получило «dial tcp: i/o timeout» — узел загрузки не отозвался
		// на одно соединение. Проход обрывался целиком и начинался
		// заново с того же файла: разовая сетевая беда так ещё
		// вылечивается следующей минутой, а вот файл, который не
		// уходит никогда (слишком большой, пропал с диска), запер бы
		// зеркало навсегда — и все следующие файлы вместе с ним.
		if err := back.Put(a.Key, data); err != nil {
			trouble++
			if firstTrouble == "" {
				firstTrouble = shortErr(err.Error())
			}
			continue
		}
		_ = e.store.SetFileMirror(a.ID, name)
		done++
	}
	d, total := e.store.MirrorProgress(name)
	result := fmt.Sprintf("уложено %d · в зеркале %d из %d", done, d, total)
	if trouble > 0 {
		result += fmt.Sprintf(" · не вышло %d: %s", trouble, firstTrouble)
		// в заметке хранилища — то же самое: экран «Хранилища» должен
		// показывать беду, даже когда проход в целом удался
		e.store.NoteStorage(name, result)
	}
	return result
}

// AddFile — единая точка входа для файлов из любого источника: хэш,
// дедупликация, хранилище, извлечение текста, вектор, событие модулям.
// Известное содержимое не сохраняется второй раз — дописывается источник.
func (e *Engine) AddFile(ctx context.Context, name, mime string, data []byte, src FileSource, msg *store.Message) (*store.Attachment, bool, error) {
	return e.addFile(ctx, name, mime, data, src, msg, nil)
}

// knownText — текст файла, который вызывающий уже добыл сам: голосовое
// распознано на входе (у него своя политика — чей голос, какая модель), и
// второй раз при приёме файла его распознавать нельзя.
type knownText struct{ Text, Via string }

func (e *Engine) addFile(ctx context.Context, name, mime string, data []byte, src FileSource, msg *store.Message, known *knownText) (*store.Attachment, bool, error) {
	if int64(len(data)) > int64(e.FilesMaxMB())<<20 {
		return nil, false, fmt.Errorf("файл превышает предел %d МБ", e.FilesMaxMB())
	}
	if len(data) == 0 {
		return nil, false, fmt.Errorf("пустой файл")
	}
	sha := storage.SHA256(data)
	if existing, ok := e.store.FileBySHA(sha); ok {
		_ = e.store.AddFileSource(existing.ID, src.Kind, src.Ref, src.Account)
		// то же содержимое, но на этот раз текст есть, а раньше не было: голос,
		// не расслышанный при первой пересылке (нет модели, чужой чат), теперь
		// расслышан. Файл тот же — текст дописывается ему, а не теряется
		if known != nil && strings.TrimSpace(known.Text) != "" && !hasRealText(existing) {
			existing.Extracted = known.Text
			existing.Summary = "прочитано: " + known.Via
			if err := e.store.UpdateAttachmentText(existing); err == nil {
				e.fileGotText(ctx, existing)
			}
		}
		return existing, false, nil
	}
	back := e.storageByName("")
	if back == nil {
		return nil, false, fmt.Errorf("основное хранилище недоступно")
	}
	key := storage.Key(sha)
	if err := back.Put(key, data); err != nil {
		return nil, false, err
	}
	path := back.Path(key)
	if mime == "" {
		mime = guessMIME(name)
	}
	parseStart := time.Now()
	var text, via, note string
	var codes []store.FileCode
	switch {
	case known != nil:
		text, via = known.Text, known.Via
	case isAudio(name, mime):
		// записи из почты и папок не расшифровываются сами — только то, что
		// человек загрузил руками; остальное — по просьбе в чате
		text, via, note = e.audioFileText(ctx, name, mime, data, src)
	default:
		text, via, codes = e.extract(ctx, path, mime, data)
	}
	att := &store.Attachment{
		Filename: name, MIME: mime, Size: int64(len(data)), Path: path, Kind: store.AttKindDocument,
		Extracted: text, SHA256: sha, Storage: back.Name(), Key: key, Source: src.Kind,
	}
	if msg != nil {
		att.MessageID = msg.ID
	}
	switch {
	case att.Extracted != "":
		if via != "" {
			att.Summary = "прочитано: " + via
		}
	case note != "":
		att.Summary = note
	default:
		hint := path
		if isAudio(name, mime) {
			hint = name // у пути в хранилище расширения нет, а тип мог прийти пустым
		}
		att.Summary = extractHint(hint, mime)
	}
	if err := e.store.InsertAttachment(att); err != nil {
		return nil, false, err
	}
	// вопрос «показать ли картинку модели» родился при извлечении текста,
	// когда файла в базе ещё не было: дописываем в него имя и номер —
	// иначе в записи стоит хэш, а картинки не видно
	e.AmendImageQuestion(att)
	_ = e.store.AddFileSource(att.ID, src.Kind, src.Ref, src.Account)
	if searched := isImage(path, mime) || isPDF(path, mime); searched || len(codes) > 0 {
		elapsed := time.Since(parseStart).Seconds()
		if searched && len(codes) == 0 {
			// «искали и не нашли» — тоже итог, и он нужен в карточке:
			// иначе пустое место не отличить от «даже не смотрели»
			codes = []store.FileCode{markWithoutCodes(elapsed)}
		}
		if err := e.store.SaveFileCodes(att.ID, codes); err != nil {
			log.Printf("коды файла %d: %v", att.ID, err)
		}
		log.Printf("файл %d: %s", att.ID, codeLog(att.Filename, store.Codes(codes), elapsed))
		codes = store.Codes(codes)
	}
	// класс считаем сразу по добытому тексту: до этого о содержимом знало
	// только имя файла, а у фотографии оно не говорит ничего.
	// Коды в счёт не идут: в QR чека лежат ФН и ФП — это реквизиты, а не
	// личное, а слово «сумма» в разборе кода подняло бы класс на ровном
	// месте и закрыло бы файл от модели
	class, why := sentClassWhy(att.Filename + "\n" + clip(att.Extracted, 2000))
	e.raiseClass(store.ClassFile, att.ID, class, why)
	log.Printf("файл сохранён: %s (%s, %s)", att.Filename, src.Kind, att.Kind)
	if e.embedder != nil {
		from := ""
		if msg != nil {
			from = "\nFrom: " + msg.FromAddr
		}
		if vec, err := e.embedIfAllowed(ctx, "File: "+att.Filename+from+"\n"+att.Summary+"\n"+clip(att.Extracted, 800)); err == nil && vec != nil {
			_ = e.store.SaveAttEmbedding(att.ID, embed.EncodeVec(vec))
		}
	}
	// коды — такой же повод разбудить модули и разбор, как и текст: на
	// фотографии чека OCR иногда не находит ни слова, а QR ФНС читается
	if att.Extracted != "" || len(codes) > 0 {
		e.emitModuleEvent(EventFileAdded, att.ID)
		// разбор ядром идёт очередью: модель думает долго, а на Pi она
		// занимает процессор целиком — приём файла ждать этого не должен
		e.EnqueueFileParse(att.ID)
	}
	return att, true, nil
}

// FileBytes — содержимое файла для отдачи клиенту: из хранилища по ключу,
// а для ещё не переехавших — по старому пути.
func (e *Engine) FileBytes(a *store.Attachment) ([]byte, error) {
	if a.Storage != "" && a.Key != "" {
		if back := e.storageByName(a.Storage); back != nil {
			if data, err := back.Get(a.Key); err == nil {
				return data, nil
			}
		}
	}
	if a.Path != "" {
		if data, err := os.ReadFile(a.Path); err == nil {
			return data, nil
		}
	}
	// локальной копии нет — зеркало
	if a.Mirror != "" && a.Key != "" {
		if back := e.storageByName(a.Mirror); back != nil {
			return back.Get(a.Key)
		}
	}
	return nil, fmt.Errorf("файл %d: расположение неизвестно", a.ID)
}

// filesMigratePass — переезд старых вложений: хэш, ключ по хэшу, источник
// «письмо». Одинаковое содержимое сливается в одну запись.
func (e *Engine) filesMigratePass(ctx context.Context) string {
	list, err := e.store.FilesWithoutLocation(25)
	if err != nil {
		return "ошибка: " + err.Error()
	}
	if len(list) == 0 {
		left, total := e.store.CountFilesWithoutLocation()
		return fmt.Sprintf("готово · файлов %d, без расположения %d", total, left)
	}
	back := e.storageByName("")
	if back == nil {
		return "основное хранилище недоступно"
	}
	moved, merged, lost := 0, 0, 0
	for _, a := range list {
		select {
		case <-ctx.Done():
			return "прервано"
		default:
		}
		data, err := os.ReadFile(a.Path)
		if err != nil {
			// файла на диске нет — запись остаётся, но помечаем, чтобы не крутиться
			_ = e.store.SetFileLocation(a.ID, "missing:"+strconv.FormatInt(a.ID, 10), "", "", a.Path)
			lost++
			continue
		}
		sha := storage.SHA256(data)
		if other, ok := e.store.FileBySHA(sha); ok && other.ID != a.ID {
			_ = e.store.MergeFileInto(a.ID, other.ID)
			_ = os.Remove(a.Path)
			merged++
			continue
		}
		key := storage.Key(sha)
		if err := back.Put(key, data); err != nil {
			log.Printf("переезд файла %s: %v", a.Filename, err)
			continue
		}
		if err := e.store.SetFileLocation(a.ID, sha, back.Name(), key, back.Path(key)); err != nil {
			continue
		}
		if abs, _ := filepath.Abs(a.Path); abs != back.Path(key) {
			_ = os.Remove(a.Path)
		}
		moved++
	}
	left, total := e.store.CountFilesWithoutLocation()
	return fmt.Sprintf("перенесено %d, слито %d, потеряно %d · осталось %d из %d", moved, merged, lost, left, total)
}

// FilesMigrateProgress — сколько файлов ещё без расположения.
func (e *Engine) FilesMigrateProgress() (left, total int) { return e.store.CountFilesWithoutLocation() }

// StorageStat — что занято в хранилище.
func (e *Engine) StorageStat(name string) (files int, bytes int64, ok bool) {
	back := e.storageByName(name)
	if back == nil {
		return 0, 0, false
	}
	f, b := back.Stat()
	return f, b, true
}

// SourceTitle — подпись источника для капсул и карточек.
func SourceTitle(src store.FileSource) string {
	switch src.Kind {
	case store.SourceMail:
		if src.Account != "" {
			return "письмо · " + src.Account
		}
		return "письмо"
	case store.SourceUpload:
		switch {
		case strings.HasPrefix(src.Ref, "chat:"):
			return "загрузка · чат #" + strings.TrimPrefix(src.Ref, "chat:")
		case strings.HasPrefix(src.Ref, "script:"):
			return "скрипт #" + strings.TrimPrefix(src.Ref, "script:")
		}
		return "загрузка"
	case store.SourceFolder:
		return "папка " + src.Ref
	case store.SourceModule:
		return "модуль «" + src.Ref + "»"
	}
	return src.Kind
}

// GuessMIME — тип по расширению, когда источник его не сообщил.
func GuessMIME(name string) string { return guessMIME(name) }

func guessMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".txt", ".ics", ".csv":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".ogg", ".oga", ".opus":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	}
	return "application/octet-stream"
}

var _ = time.Now

const WorkerFileFolders = "files_folders"

const folderPassLimit = 50

// foldersPass — обход наблюдаемых папок: новые и изменившиеся файлы идут в
// AddFile с источником «папка». Файл, который ещё пишется (изменён меньше
// 10 секунд назад), ждёт следующего прохода.
func (e *Engine) foldersPass(ctx context.Context) string {
	folders, err := e.store.FileFolders()
	if err != nil {
		return "ошибка: " + err.Error()
	}
	total, taken := 0, 0
	for _, f := range folders {
		if !f.Enabled {
			continue
		}
		total++
		entries, err := os.ReadDir(f.Path)
		if err != nil {
			e.store.NoteFileFolder(f.ID, "не читается: "+err.Error(), 0)
			continue
		}
		got, lastErr := 0, ""
		for _, ent := range entries {
			if got >= folderPassLimit {
				break
			}
			select {
			case <-ctx.Done():
				return "прервано"
			default:
			}
			if ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
				continue
			}
			info, err := ent.Info()
			if err != nil || info.Size() == 0 || time.Since(info.ModTime()) < 10*time.Second {
				continue
			}
			full := filepath.Join(f.Path, ent.Name())
			if e.store.FolderSeen(full, info.Size(), info.ModTime()) {
				continue
			}
			data, err := os.ReadFile(full)
			if err != nil {
				lastErr = ent.Name() + ": " + err.Error()
				continue
			}
			att, _, err := e.AddFile(ctx, ent.Name(), "", data, FileSource{Kind: store.SourceFolder, Ref: f.Path}, nil)
			if err != nil {
				lastErr = ent.Name() + ": " + err.Error()
				continue
			}
			e.store.MarkFolderSeen(full, info.Size(), info.ModTime(), att.ID)
			got++
			if f.DeleteAfter {
				if err := os.Remove(full); err != nil {
					lastErr = ent.Name() + ": принят, но не удалён: " + err.Error()
				}
			}
		}
		e.store.NoteFileFolder(f.ID, lastErr, got)
		taken += got
	}
	if total == 0 {
		return "папок нет"
	}
	return fmt.Sprintf("папок %d · принято %d", total, taken)
}
