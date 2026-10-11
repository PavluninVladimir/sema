// Package store — SQLite-хранилище (modernc.org/sqlite, без cgo,
// кросс-компилируется на Raspberry Pi одной командой GOARCH=arm64).
package store

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/secret"

	_ "modernc.org/sqlite"
	"regexp"
)

// Статусы письма в жизненном цикле Сёмы.
const (
	StatusPending   = "pending"    // LLM считает спамом, ждёт подтверждения человека
	StatusQueued    = "queued"     // подтверждено, ждёт фонового переноса в Junk
	StatusInboxOK   = "inbox_ok"   // проверено, не спам
	StatusHam       = "ham"        // человек сказал «не спам»
	StatusConfirmed = "confirmed"  // человек подтвердил спам → перенесено в Junk
	StatusAutoMoved = "auto_moved" // перенесено в Junk по выученному правилу
	StatusPurged    = "purged"     // удалено из Junk по истечении срока
	StatusArchived  = "archived"   // разобрано из папки ящика и уложено в архив
	// Папки, где решение принял почтовый сервер или человек, а не Сёма.
	// Отдельные состояния, а не «archived», потому что дайджест «Почты»
	// собирается по списку состояний: спам провайдера и корзина не должны
	// попадать в сводку «что пришло», но обязаны находиться поиском.
	StatusServerSpam  = "server_spam"  // лежало в папке спама почтового сервера
	StatusServerTrash = "server_trash" // лежало в корзине почтового сервера
)

type Message struct {
	ID         int64
	Account    string
	MessageID  string
	UID        uint32
	FromAddr   string
	FromName   string
	Subject    string
	Date       time.Time
	Snippet    string
	Confidence float64
	Reason     string
	Status     string
	MovedAt    *time.Time
	Direction  string // in — пришло, out — отправлено нами (папка «Отправленные»)

	// конверт: на что письмо отвечает, вся цепочка, куда отвечать, кому
	// адресовано и как отписаться. Хранятся строками через пробел —
	// сравниваются они целиком, а не по частям
	InReplyTo string
	Refs      string
	ReplyTo   string
	To        string
	Cc        string
	ListUnsub string
}

type Rule struct {
	ID     int64
	Kind   string // "sender" | "domain"
	Value  string
	Action string // "spam" | "ham"
}

type Store struct {
	// память сводок расхода: см. UsageBy
	usageMu    sync.Mutex
	usageCache map[string][]usageCacheEntry

	// база в обёртке: запросы попадают в счёт Server-Timing (sqltrace.go)
	db countedDB

	// версии ключей настроек: каждая запись поднимает счётчик своего ключа,
	// и кэш над большим списком (модули, модели) сверяется с числом, а не
	// перечитывает из базы строку в 100 КБ (perf-pass 2.1)
	verMu       sync.RWMutex
	settingVers map[string]uint64
	settingsGen uint64                 // растёт на любой записи настроек
	prefixCache map[string]prefixEntry // SettingsByPrefixShared

	// шифрование секретов: ключа может не быть, тогда всё работает
	// по-старому, а настройки честно об этом сообщают
	box         *secret.Box
	secretErr   string
	secretErrMu sync.Mutex

	// код первой установки: в памяти процесса и хэшем (setup_code.go)
	setup setupSlot
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: countedDB{db}}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenReadOnly — база только для чтения и без миграций: так снимается
// копия с работающей Сёмы, не трогая базу, которую держит сервер.
func OpenReadOnly(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	return &Store{db: countedDB{db}}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS messages (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id  TEXT UNIQUE NOT NULL,
	uid         INTEGER NOT NULL,
	from_addr   TEXT NOT NULL,
	from_name   TEXT NOT NULL DEFAULT '',
	subject     TEXT NOT NULL DEFAULT '',
	date        TIMESTAMP NOT NULL,
	snippet     TEXT NOT NULL DEFAULT '',
	confidence  REAL NOT NULL DEFAULT 0,
	reason      TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL,
	moved_at    TIMESTAMP,
	created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS rules (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	kind       TEXT NOT NULL,
	value      TEXT NOT NULL,
	action     TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(kind, value)
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS embeddings (
	message_id INTEGER PRIMARY KEY REFERENCES messages(id),
	vector     BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS attachments (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id INTEGER NOT NULL REFERENCES messages(id),
	filename   TEXT NOT NULL,
	mime       TEXT NOT NULL DEFAULT '',
	size       INTEGER NOT NULL DEFAULT 0,
	path       TEXT NOT NULL,
	extracted  TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT 'document',
	vendor     TEXT NOT NULL DEFAULT '',
	amount     REAL NOT NULL DEFAULT 0,
	currency   TEXT NOT NULL DEFAULT '',
	summary    TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS att_embeddings (
	attachment_id INTEGER PRIMARY KEY REFERENCES attachments(id),
	vector        BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS chats (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	title      TEXT NOT NULL DEFAULT 'Новый чат',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS chat_msgs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id    INTEGER NOT NULL REFERENCES chats(id),
	role       TEXT NOT NULL, -- user | assistant
	content    TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS tasks (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	title       TEXT NOT NULL,
	prompt      TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL DEFAULT 'prompt', -- prompt | poll
	schedule    TEXT NOT NULL,
	enabled     INTEGER NOT NULL DEFAULT 1,
	once        INTEGER NOT NULL DEFAULT 0,
	chat_id     INTEGER NOT NULL DEFAULT 0,
	last_run    TEXT NOT NULL DEFAULT '',
	last_result TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`)
	if err != nil {
		return err
	}
	// колонка-отметка «вложения этого письма уже просмотрены»
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN att_scanned INTEGER NOT NULL DEFAULT 0`)
	// имя почтового ящика, из которого пришло письмо
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN account TEXT NOT NULL DEFAULT ''`)
	// чеки и счета: найденные и во вложениях, и прямо в тексте письма
	// задачи: встроенные процессы живут в той же таблице, что и пользовательские
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN system TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN after_id INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN root INTEGER NOT NULL DEFAULT 0`)
	// кто поставил расписание: пусто — манифест модуля, «человек» — руками.
	// Без этого манифест молча возвращал свой час при каждом запуске
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN sched_by TEXT NOT NULL DEFAULT ''`)
	// откуда задача: пусто — человек внутри Сёмы. Колонки завели трекеры
	// задач (сняты 30 сентября 2026); сейчас их не пишет и не читает никто
	// кроме самой записи задачи
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN origin TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN ref TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN sort INTEGER NOT NULL DEFAULT 100`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN script_id INTEGER NOT NULL DEFAULT 0`)
	// у чата своя модель и свой уровень размышления
	_, _ = s.db.Exec(`ALTER TABLE chats ADD COLUMN model TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE chats ADD COLUMN effort TEXT NOT NULL DEFAULT ''`)
	// три уровня чата: контейнер → разговор → тред. Родитель, вид, якорь
	// (наше сообщение) и номер топика или поста в мессенджере
	_, _ = s.db.Exec(`ALTER TABLE chats ADD COLUMN parent_id INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE chats ADD COLUMN kind TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE chats ADD COLUMN anchor_msg_id INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE chats ADD COLUMN topic_id INTEGER NOT NULL DEFAULT 0`)
	// кто написал реплику собеседника: в личном чате это сам собеседник, а
	// в группе и топике — один из многих, и без имени переписка не читается
	_, _ = s.db.Exec(`ALTER TABLE chat_msgs ADD COLUMN author TEXT NOT NULL DEFAULT ''`)
	// откуда реплика пришла в мессенджере: «tg:<учётка>:<чат>:<номер>».
	// По этому адресу лента узнаёт, что сообщение удалили там, — иначе
	// удалённый пост так и висел бы в ленте канала у Сёмы
	_, _ = s.db.Exec(`ALTER TABLE chat_msgs ADD COLUMN ext_ref TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chat_msgs_ref ON chat_msgs(ext_ref) WHERE ext_ref != ''`)
	// одна тема — один разговор: слушатель и разбор истории идут разом, и
	// без ключа получалось два разговора, между которыми расходится лента
	_, _ = s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_topic ON chats(parent_id, topic_id) WHERE topic_id > 0`)
	// скрипты: то, чем агент расширяет себя сам
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS scripts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		title       TEXT NOT NULL,
		code        TEXT NOT NULL DEFAULT '',
		last_run    TEXT NOT NULL DEFAULT '',
		last_result TEXT NOT NULL DEFAULT '',
		last_ms     REAL NOT NULL DEFAULT 0,
		created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	// локальные копии писем: полный текст и HTML, переживают удаление на сервере
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS message_bodies (
		message_id INTEGER PRIMARY KEY REFERENCES messages(id),
		text       TEXT NOT NULL DEFAULT '',
		html       TEXT NOT NULL DEFAULT '',
		fetched_at TEXT NOT NULL DEFAULT ''
	)`)
	// индексы под запросы экранов (без них — полный скан messages)
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS idx_messages_status_date ON messages(status, date DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_subject ON messages(subject)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_account_uid ON messages(account, uid)`,
		// отправитель: «Отправители» соединяют правила с письмами по
		// from_addr, а без индекса каждое правило читало таблицу целиком
		// (204 правила на 1700 писем — 0,24 с на живой Сёме). Тем же
		// индексом идёт история отправителя: from_addr + свежие сверху
		`CREATE INDEX IF NOT EXISTS idx_messages_from_date ON messages(from_addr, date DESC)`,
		// переписка чата: пока сообщений сотни, скан незаметен, но
		// Telegram и MAX растят таблицу, а читается она при каждом открытии
		`CREATE INDEX IF NOT EXISTS idx_chat_msgs_chat ON chat_msgs(chat_id, id DESC)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// направление письма: исходящие читаются из «Отправленных» на месте
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN direction TEXT NOT NULL DEFAULT 'in'`)
	if err := s.repairBodies(); err != nil {
		return err
	}
	// журнал изменений настроек заводится раньше всех остальных таблиц,
	// какие заводит миграция: первичное заполнение справочников само пишет
	// настройки, и без таблицы эти правки пропали бы молча
	if err := s.migrateSettingsLog(); err != nil {
		return err
	}
	// список служебных отметок журнала: новые образцы доезжают и до базы, где
	// настройка уже сохранена (settings_log_view.go)
	s.seedLogMarks()
	// ключи и секреты плюс журнал обращений к ним
	if err := s.migrateVault(); err != nil {
		return err
	}
	// память: записи, граф, потребители, правила, журнал
	if err := s.migrateMemory(); err != nil {
		return err
	}
	// серверы, круги команд, ожидающие, журнал SSH
	if err := s.migrateSkills(); err != nil {
		return err
	}
	if err := s.migratePush(); err != nil {
		return err
	}
	if err := s.migrateNotifications(); err != nil {
		return err
	}
	if err := s.migrateQueue(); err != nil {
		return err
	}
	if err := s.migrateAgents(); err != nil {
		return err
	}
	if err := s.migrateTelegram(); err != nil {
		return err
	}
	if err := s.migrateMAX(); err != nil {
		return err
	}
	if err := s.migrateClasses(); err != nil {
		return err
	}
	if err := s.migrateMCP(); err != nil {
		return err
	}
	if err := s.migrateLearning(); err != nil {
		return err
	}
	if err := s.migrateAssignments(); err != nil {
		return err
	}
	if err := s.migrateShares(); err != nil {
		return err
	}
	if err := s.migrateNewsGone(); err != nil {
		return err
	}
	if err := s.migrateTrackersGone(); err != nil {
		return err
	}
	if err := s.migrateAudit(); err != nil {
		return err
	}
	if err := s.migrateHosts(); err != nil {
		return err
	}
	// правила доступа у каждого сервера свои: после серверов и прежних
	// общих кругов, из которых они раскладываются
	if err := s.migrateHostRules(); err != nil {
		return err
	}
	// файлы: источники, хранилища, письмо необязательно
	if err := s.migrateFiles(); err != nil {
		return err
	}
	// папки на диске как источник файлов
	if err := s.migrateFolders(); err != nil {
		return err
	}
	if err := s.migrateMirror(); err != nil {
		return err
	}
	// коды на картинках и страницах PDF
	if err := s.migrateFileCodes(); err != nil {
		return err
	}
	// журнал расхода и снимки процесса
	if err := s.migrateUsage(); err != nil {
		return err
	}
	if err := s.migrateBudgets(); err != nil {
		return err
	}
	if err := s.migrateAuth(); err != nil {
		return err
	}
	// умолчания входа — после таблиц ключей и сессий: по ним и по письмам
	// видно, свежая ли база (auth_defaults.go)
	if wrote := s.settleAuthDefaults(); wrote[AuthLocalOpen.Key] == "true" || wrote[AuthLocalScope.Key] == ScopePrivate {
		log.Printf("вход: в базе уже есть данные, а умолчания входа не записаны — записаны прежние (%s), "+
			"и Сёма пускает как до обновления; сменить — «Вход и доступ → Доверенная сеть»", authDefaultsLine(wrote))
	}
	// коллекции и события модулей
	if err := s.migrateDigest(); err != nil {
		return err
	}
	if err := s.migrateModuleData(); err != nil {
		return err
	}
	if err := s.migrateDanglingRefs(); err != nil {
		return err
	}
	s.migrateTranslations()
	s.migrateVendors()
	s.migrateMailSearch()
	s.migrateDeletions()
	s.migrateFindings()
	// Яндекс.Диск переехал с WebDAV на родное API 11 сентября 2026:
	// у шлюза нельзя спросить, что у него лежит, и он принимал файлы,
	// не отвечая. Вид записи переименован, настройки не трогаем — токен
	// человек называет сам на «Хранилищах»
	_, _ = s.db.Exec(`UPDATE storages SET kind = 'yadisk' WHERE kind = 'webdav'`)
	// заметка у внешнего действия: результат, который повтор берёт готовым
	_, _ = s.db.Exec(`ALTER TABLE outside_acts ADD COLUMN note TEXT NOT NULL DEFAULT ''`)
	return s.normalizeDates()
}

// repairBodies чинит архивные копии, повреждённые ранней версией чистки
// HTML: у картинок пропадали кавычка и схема (src=://host/...).
func (s *Store) repairBodies() error {
	res, err := s.db.Exec(`
UPDATE message_bodies SET html = REPLACE(html, '=://', '="https://')
WHERE html LIKE '%=://%'`)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("архив: починено копий писем %d", n)
	}
	return nil
}

// Exclusion — чего в этой сборке не бывает вовсе. Не-nil значит «сборка идёт в
// снимок на устройстве», и это главное: раньше признаком снимка была сама
// карта закрытых ящиков, а ClosedAccounts отдаёт nil, когда закрытых ящиков
// нет. В таком доме снимок собирался ровно как обычный показ, и письмо
// класса «секретное» уезжало на диск телефона беспрепятственно.
type Exclusion struct {
	// Ящики — закрытые для облачной модели: их письма, счётчики и файлы на
	// устройство не едут. Старые письма основного ящика лежат с пустым
	// именем account — оно исключается тем же способом (см. ClosedAccounts).
	Accounts map[string]bool
	// Секретное — вырезать письма и файлы класса «секретное». Пометка
	// человека и примета живут в content_class; класса нет — значит его не
	// считали, и такая запись остаётся: снимок это то же, что человек и так
	// видит на своём устройстве, а «чувствительное» в снимок разрешено.
	Secret bool
}

// SkipsAccount — этот ящик в сборку не входит целиком.
func (ex *Exclusion) SkipsAccount(account string) bool { return ex != nil && ex.Accounts[account] }

// Clause — хвост WHERE для запроса по таблице messages: «AND account NOT IN
// (…) AND id NOT IN (…секретные…)». nil — обычный показ, запрос не меняется.
func (ex *Exclusion) Clause() (string, []any) {
	if ex == nil {
		return "", nil
	}
	var sb strings.Builder
	var args []any
	if len(ex.Accounts) > 0 {
		names := make([]string, 0, len(ex.Accounts))
		for a := range ex.Accounts {
			names = append(names, a)
		}
		sort.Strings(names)
		ph := make([]string, len(names))
		for i, n := range names {
			ph[i] = "?"
			args = append(args, n)
		}
		sb.WriteString(" AND account NOT IN (" + strings.Join(ph, ",") + ")")
	}
	if ex.Secret {
		sb.WriteString(" AND id NOT IN (SELECT id FROM content_class WHERE kind = '" + ClassMsg + "' AND class = ?)")
		args = append(args, ClassSecret)
	}
	return sb.String(), args
}

// secretIDs — какие из этих записей помечены «секретное». Одним запросом:
// по письму на запрос выходило бы столько обращений, сколько строк на экране.
func (s *Store) secretIDs(kind string, ids []int64) map[int64]bool {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, kind, ClassSecret)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT id FROM content_class WHERE kind = ? AND class = ? AND id IN (?`+
		strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}

// FilterExcludedAttachments — вложения, которых не должно быть в снимке,
// вырезанные уже после выборки (offline-snapshot 2а.6), а не через SQL: у
// вложения без письма (message_id = 0 — загрузка, наблюдаемая папка, модуль)
// пустой account значит «у файла нет своего письма», а не «письмо старого
// закрытого ящика», и SQL-исключение по имени ящика срезало бы такие файлы
// тоже — закрой ящик с пустым именем, и все загруженные руками файлы
// пропали бы из снимка. Поэтому по ящику убираем только вложение с письмом.
//
// Класс «секретное» смотрится и у самого файла, и у его письма: паспорт
// приходит вложением к письму, чья тема ни о чём не говорит, и наоборот —
// у секретного письма бывает безобидное на вид вложение.
//
// о == nil — обычный показ, срез возвращается как есть.
func (s *Store) FilterExcludedAttachments(atts []*Attachment, ex *Exclusion) []*Attachment {
	if ex == nil || len(atts) == 0 {
		return atts
	}
	var secretFiles, secretMessages map[int64]bool
	if ex.Secret {
		files := make([]int64, 0, len(atts))
		messages := make([]int64, 0, len(atts))
		for _, a := range atts {
			files = append(files, a.ID)
			if a.MessageID > 0 {
				messages = append(messages, a.MessageID)
			}
		}
		secretFiles = s.secretIDs(ClassFile, files)
		secretMessages = s.secretIDs(ClassMsg, messages)
	}
	out := make([]*Attachment, 0, len(atts))
	for _, a := range atts {
		if a.MessageID > 0 && ex.Accounts[a.Account] {
			continue
		}
		if secretFiles[a.ID] || (a.MessageID > 0 && secretMessages[a.MessageID]) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// CountByStatus — счётчик без выгрузки строк (для бейджа в сайдбаре).
func (s *Store) CountByStatus(status string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE status = ?`, status).Scan(&n)
	return n, err
}

// CountByStatusAccount — то же для одного ящика (пустой = все). о — отсев
// этой сборки (снимок на устройстве); nil — не исключать никого, как раньше.
func (s *Store) CountByStatusAccount(status, account string, ex *Exclusion) (int, error) {
	clause, cargs := ex.Clause()
	if account == "" {
		if clause == "" {
			return s.CountByStatus(status)
		}
		var n int
		err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE status = ?`+clause,
			append([]any{status}, cargs...)...).Scan(&n)
		return n, err
	}
	if ex.SkipsAccount(account) {
		return 0, nil
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE status = ? AND account = ?`+clause,
		append([]any{status, account}, cargs...)...).Scan(&n)
	return n, err
}

// ByStatusAccount — письма статуса из одного ящика (пустой account = все).
// о — отсев этой сборки; nil — не исключать никого.
func (s *Store) ByStatusAccount(status, account string, ex *Exclusion, limit int) ([]*Message, error) {
	if account == "" {
		return s.ByStatus(status, ex, limit)
	}
	if ex.SkipsAccount(account) {
		return nil, nil
	}
	clause, cargs := ex.Clause()
	rows, err := s.db.Query(selectMsg+` WHERE status = ? AND account = ?`+clause+` ORDER BY date DESC LIMIT ?`,
		append(append([]any{status, account}, cargs...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MessagesWithoutBody — письма без локальной копии текста (для архиватора).
func (s *Store) MessagesWithoutBody(account string, limit int) ([]*Message, error) {
	rows, err := s.db.Query(selectMsg+`
WHERE id NOT IN (SELECT message_id FROM message_bodies)
  AND status <> ? AND (account = ? OR account = '')
ORDER BY id DESC LIMIT ?`, StatusPurged, account, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CountWithoutBody() (int, int, error) {
	var missing, total int
	err := s.db.QueryRow(`SELECT
	(SELECT COUNT(*) FROM messages WHERE status <> ? AND id NOT IN (SELECT message_id FROM message_bodies)),
	(SELECT COUNT(*) FROM messages)`, StatusPurged).Scan(&missing, &total)
	return missing, total, err
}

const (
	AttKindReceipt  = "receipt"
	AttKindDocument = "document"
)

type Attachment struct {
	ID        int64
	MessageID int64
	Filename  string
	MIME      string
	Size      int64
	Path      string
	Extracted string
	Kind      string
	Vendor    string
	Amount    float64
	Currency  string
	Summary   string
	SHA256    string // хэш содержимого: одинаковые файлы — одна запись
	Storage   string // имя хранилища; пусто — ещё не переехал, лежит по path
	Key       string // ключ в хранилище
	Mirror    string // зеркало, куда файл уже уложен (S3)
	VendorID  int64  // кто это на самом деле: справочник поставщиков
	DocDate   string // дата самого документа (не письма)
	DueDate   string // до какого числа платить
	ToPay     bool   // счёт к оплате, а не подтверждение платежа
	Source    string // основной источник: mail | upload | folder | module
	// подтягиваются join'ом для UI: откуда файл — отправитель, письмо, ящик
	FromAddr string
	FromName string
	Date     time.Time
	Subject  string
	Account  string
	Sources  []FileSource // все источники, заполняется отдельно
}

func (s *Store) InsertAttachment(a *Attachment) error {
	res, err := s.db.Exec(`
INSERT INTO attachments (message_id, filename, mime, size, path, extracted, kind, vendor, amount, currency, summary, sha256, storage, key, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.MessageID, a.Filename, a.MIME, a.Size, a.Path, a.Extracted, a.Kind,
		a.Vendor, a.Amount, a.Currency, a.Summary, a.SHA256, a.Storage, a.Key, time.Now().Format(time.RFC3339))
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	s.touch(ScreenFiles)
	return nil
}

// selectAtt — файл с полями письма, если письмо есть: у загруженного файла
// их нет, и это не ошибка. Дата файла без письма — дата записи.
const selectAtt = `
SELECT a.id, a.message_id, a.filename, a.mime, a.size, a.path, a.extracted,
       a.kind, a.vendor, a.amount, a.currency, a.summary, a.sha256, a.storage, a.key, a.mirror,
       a.vendor_id, a.doc_date, a.due_date, a.to_pay,
       COALESCE(m.from_addr, ''), COALESCE(m.from_name, ''), COALESCE(m.date, a.created_at), COALESCE(m.subject, ''), COALESCE(m.account, ''),
       COALESCE((SELECT kind FROM file_sources fs WHERE fs.file_id = a.id ORDER BY fs.id LIMIT 1), CASE WHEN a.message_id > 0 THEN 'mail' ELSE '' END)
FROM attachments a LEFT JOIN messages m ON m.id = a.message_id`

func scanAtt(r rowScanner) (*Attachment, error) {
	a := &Attachment{}
	var date string
	err := r.Scan(&a.ID, &a.MessageID, &a.Filename, &a.MIME, &a.Size, &a.Path, &a.Extracted,
		&a.Kind, &a.Vendor, &a.Amount, &a.Currency, &a.Summary, &a.SHA256, &a.Storage, &a.Key, &a.Mirror,
		&a.VendorID, &a.DocDate, &a.DueDate, &a.ToPay,
		&a.FromAddr, &a.FromName, &date, &a.Subject, &a.Account, &a.Source)
	if err != nil {
		return nil, err
	}
	a.Date = parseTime(date)
	return a, nil
}

// CountMessages — сколько писем в архиве.
func (s *Store) CountMessages() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n)
	return n, err
}

// MessageStats — сколько писем в архиве и за какой период.
func (s *Store) MessageStats() (total int, first, last string) {
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(date),''), COALESCE(MAX(date),'')
		FROM messages`).Scan(&total, &first, &last)
	return total, cutDate(first), cutDate(last)
}

// AttachmentStats — сколько файлов сохранено и у скольких есть текст.
func (s *Store) AttachmentStats() (total, withText int) {
	_ = s.db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN extracted <> '' THEN 1 ELSE 0 END)
		FROM attachments`).Scan(&total, &withText)
	return total, withText
}

func cutDate(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// ClearEmbeddings сбрасывает индекс: после смены модели старые векторы
// несравнимы с новыми, и считать похожесть по ним нельзя.
func (s *Store) ClearEmbeddings() error {
	if _, err := s.db.Exec(`DELETE FROM embeddings`); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM att_embeddings`)
	return err
}

// EmbeddingCounts — сколько писем уже с вектором и сколько ждут.
func (s *Store) EmbeddingCounts() (done, waiting int) {
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&done)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM messages m
		WHERE NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.message_id = m.id)`).Scan(&waiting)
	return done, waiting
}

// AttachmentsByName ищет вложения по куску имени файла — человек часто
// прямо называет файл в вопросе, и это надёжнее любой похожести.
func (s *Store) AttachmentsByName(name string, limit int) ([]*Attachment, error) {
	rows, err := s.db.Query(selectAtt+` WHERE a.filename LIKE ? LIMIT ?`, "%"+name+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AttachmentsWithoutText — вложения, из которых текст не достали: картинки
// без OCR, офисные файлы старых разборов. Их перечитывают, когда в системе
// появляется недостающий инструмент.
func (s *Store) AttachmentsWithoutText(limit int) ([]*Attachment, error) {
	rows, err := s.db.Query(selectAtt+` WHERE a.extracted = '' ORDER BY a.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountAttachmentsWithoutText — сколько файлов ждут распознавания.
func (s *Store) CountAttachmentsWithoutText() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE extracted = ''`).Scan(&n)
	return n
}

// UpdateAttachmentText сохраняет распознанный текст и итог разбора.
func (s *Store) UpdateAttachmentText(a *Attachment) error {
	_, err := s.db.Exec(`UPDATE attachments SET extracted = ?, kind = ?, vendor = ?,
		amount = ?, currency = ?, summary = ? WHERE id = ?`,
		a.Extracted, a.Kind, a.Vendor, a.Amount, a.Currency, a.Summary, a.ID)
	return err
}

// Attachments возвращает вложения, kind пустой — все.
func (s *Store) Attachments(kind string, limit int) ([]*Attachment, error) {
	q := selectAtt
	args := []any{}
	if kind != "" {
		q += ` WHERE a.kind = ?`
		args = append(args, kind)
	}
	// у файла без письма даты письма нет — иначе он уходит в самый конец
	q += ` ORDER BY COALESCE(m.date, a.created_at) DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) AttachmentsByIDs(ids []int64) ([]*Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := selectAtt + ` WHERE a.id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*Attachment{}
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		byID[a.ID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Attachment, 0, len(ids))
	for _, id := range ids {
		if a, ok := byID[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// UnscannedMessages — письма-не-спам, у которых ещё не искали вложения.
func (s *Store) UnscannedMessages(account string, limit int) ([]*Message, error) {
	// спам тоже: чеки и счета часто попадают именно туда
	rows, err := s.db.Query(selectMsg+
		` WHERE att_scanned = 0 AND status <> ? AND (account = ? OR account = '') ORDER BY date DESC LIMIT ?`,
		StatusPurged, account, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MarkAttScanned(msgID int64) error {
	_, err := s.db.Exec(`UPDATE messages SET att_scanned = 1 WHERE id = ?`, msgID)
	return err
}

func (s *Store) SaveAttEmbedding(attID int64, vec []byte) error {
	_, err := s.db.Exec(`
INSERT INTO att_embeddings (attachment_id, vector) VALUES (?, ?)
ON CONFLICT(attachment_id) DO UPDATE SET vector = excluded.vector`, attID, vec)
	return err
}

// AttEmbeddings — векторы всех вложений (id → vector).
func (s *Store) AttEmbeddings() ([]*EmbRow, error) {
	rows, err := s.db.Query(`SELECT attachment_id, '', vector FROM att_embeddings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EmbRow
	for rows.Next() {
		r := &EmbRow{}
		if err := rows.Scan(&r.MsgID, &r.Status, &r.Vector); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Даты храним строками RFC3339 в UTC — сравнимы лексикографически и
// однозначно читаются любым драйвером.
const timeFmt = time.RFC3339

func fmtTime(t time.Time) string { return t.UTC().Format(timeFmt) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

var timeFormats = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999 -0700 -0700", // старый формат драйвера (Go time.String)
	"2006-01-02 15:04:05 -0700 -0700",
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05",
}

func parseTime(s string) time.Time {
	for _, f := range timeFormats {
		if t, err := time.Parse(f, s); err == nil {
			// в базе время в UTC; наружу отдаём в поясе Сёмы (app.timezone)
			return t.Local()
		}
	}
	return time.Time{}
}

// normalizeDates приводит даты, записанные старыми версиями, к RFC3339.
func (s *Store) normalizeDates() error {
	rows, err := s.db.Query(`SELECT id, date, moved_at FROM messages WHERE date NOT LIKE '____-__-__T%'`)
	if err != nil {
		return err
	}
	type fix struct {
		id            int64
		date, movedAt string
	}
	var fixes []fix
	for rows.Next() {
		var f fix
		var moved sql.NullString
		if err := rows.Scan(&f.id, &f.date, &moved); err != nil {
			rows.Close()
			return err
		}
		f.movedAt = moved.String
		fixes = append(fixes, f)
	}
	rows.Close()
	for _, f := range fixes {
		var movedArg any
		if f.movedAt != "" {
			movedArg = fmtTime(parseTime(f.movedAt))
		}
		if _, err := s.db.Exec(`UPDATE messages SET date = ?, moved_at = ? WHERE id = ?`,
			fmtTime(parseTime(f.date)), movedArg, f.id); err != nil {
			return err
		}
	}
	return nil
}

// Thread — письма одной темы (нормализованная тема без Re:/Fwd:), старые сверху.
func (s *Store) Thread(normSubject string, limit int) ([]*Message, error) {
	variants := []any{normSubject,
		"Re: " + normSubject, "RE: " + normSubject, "re: " + normSubject,
		"Fwd: " + normSubject, "FW: " + normSubject, "Fw: " + normSubject}
	q := selectMsg + ` WHERE subject IN (?` + strings.Repeat(",?", len(variants)-1) + `) ORDER BY date ASC LIMIT ?`
	rows, err := s.db.Query(q, append(variants, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// KnownUIDs — UID всех писем, которые уже есть в базе (для глубокого разбора).
func (s *Store) KnownUIDs(account string) (map[uint32]bool, error) {
	rows, err := s.db.Query(`SELECT uid FROM messages WHERE account = ? OR account = ''`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint32]bool{}
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out[uid] = true
	}
	return out, rows.Err()
}

func (s *Store) KnownMessage(messageID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE message_id = ?`, messageID).Scan(&n)
	return n > 0, err
}

func (s *Store) InsertMessage(m *Message) error {
	res, err := s.db.Exec(`
INSERT INTO messages (account, message_id, uid, from_addr, from_name, subject, date, snippet, confidence, reason, status, moved_at, direction,
                      in_reply_to, refs, reply_to, to_addrs, cc_addrs, list_unsub)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(message_id) DO NOTHING`,
		m.Account, m.MessageID, m.UID, m.FromAddr, m.FromName, m.Subject, fmtTime(m.Date), m.Snippet,
		m.Confidence, m.Reason, m.Status, fmtTimePtr(m.MovedAt), orIn(m.Direction),
		m.InReplyTo, m.Refs, m.ReplyTo, m.To, m.Cc, m.ListUnsub)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	// в указатель поиска сразу: тело приедет позже и допишет запись
	s.IndexMessage(m.ID, m.Subject, m.FromAddr+" "+m.FromName, m.Snippet)
	s.touch(ScreenInbox, ScreenMailbox(m.Account))
	return nil
}

// UIDsByStatus — UID писем ящика в перечисленных статусах. Нужен разбору
// папок: письма на подтверждении спама трогать нельзя.
func (s *Store) UIDsByStatus(account string, statuses ...string) (map[uint32]bool, error) {
	if len(statuses) == 0 {
		return map[uint32]bool{}, nil
	}
	args := []any{account}
	for _, st := range statuses {
		args = append(args, st)
	}
	rows, err := s.db.Query(`SELECT uid FROM messages WHERE account = ? AND status IN (?`+
		strings.Repeat(",?", len(statuses)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint32]bool{}
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out[uid] = true
	}
	return out, rows.Err()
}

func (s *Store) SetStatus(id int64, status string, movedAt *time.Time) error {
	_, err := s.db.Exec(`UPDATE messages SET status = ?, moved_at = COALESCE(?, moved_at) WHERE id = ?`,
		status, fmtTimePtr(movedAt), id)
	if err == nil {
		s.touch(ScreenInbox)
	}
	return err
}

// SetStatusFull — статус вместе с суждением. Отдельно от SetStatus,
// потому что там статус меняет человек кнопкой, а здесь его меняет разбор
// и вместе с ним обязан записать, на чём основан: статус без причины и
// уверенности — это «Сёма решил, а почему, не скажет».
func (s *Store) SetStatusFull(id int64, status, reason string, confidence float64) error {
	_, err := s.db.Exec(`UPDATE messages SET status = ?, reason = ?, confidence = ? WHERE id = ?`,
		status, reason, confidence, id)
	if err == nil {
		s.touch(ScreenInbox)
	}
	return err
}

// ReceiptsFromSender — сколько чеков уже приходило от этого отправителя.
// Примета сильнее любых слов в теме: её не подделать рассылкой.
func (s *Store) ReceiptsFromSender(addr string) int {
	if strings.TrimSpace(addr) == "" {
		return 0
	}
	n := 0
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM attachments a JOIN messages m ON m.id = a.message_id
WHERE a.kind = ? AND m.from_addr = ?`, AttKindReceipt, strings.ToLower(addr)).Scan(&n)
	return n
}

// MessagesUnjudged — письма, уехавшие в архив мимо разбора ИЗ «Входящих».
// Свежие первыми: счёт этого месяца важнее прошлогоднего.
//
// Только INBOX, и это важно. Первый заход брал всё с причиной «папка: …» —
// то есть и письма, которые человек своими руками разложил по папкам
// «Счета», «Работа» и прочим. Их судить не надо: папка и есть решение
// человека, а пересуд означал шестнадцать старых рассылок, внезапно
// потребовавших решения. На живой Сёме так набралось 448 писем вместо 41.
func (s *Store) MessagesUnjudged(limit int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM messages
WHERE status = ? AND confidence = 0 AND reason = 'папка: INBOX'
ORDER BY date DESC LIMIT ?`, StatusArchived, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) Message(id int64) (*Message, error) {
	return scanOne(s.db.QueryRow(selectMsg+` WHERE id = ?`, id))
}

// ByStatus — письма статуса из всех ящиков. о — отсев этой сборки; nil — не
// исключать никого, как раньше.
func (s *Store) ByStatus(status string, ex *Exclusion, limit int) ([]*Message, error) {
	clause, cargs := ex.Clause()
	args := append([]any{status}, cargs...)
	rows, err := s.db.Query(selectMsg+` WHERE status = ?`+clause+` ORDER BY date DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MovedBefore — письма, перенесённые Семой в Junk раньше указанного момента
// (кандидаты на окончательное удаление).
func (s *Store) MovedBefore(t time.Time) ([]*Message, error) {
	rows, err := s.db.Query(selectMsg+
		` WHERE status IN (?, ?) AND moved_at IS NOT NULL AND moved_at < ?`,
		StatusConfirmed, StatusAutoMoved, fmtTime(t))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const selectMsg = `SELECT id, account, message_id, uid, from_addr, from_name, subject, date, snippet, confidence, reason, status, moved_at, direction, in_reply_to, refs, reply_to, to_addrs, cc_addrs, list_unsub FROM messages`

type rowScanner interface{ Scan(dest ...any) error }

func scanOne(r rowScanner) (*Message, error) {
	m := &Message{}
	var date string
	var movedAt sql.NullString
	err := r.Scan(&m.ID, &m.Account, &m.MessageID, &m.UID, &m.FromAddr, &m.FromName, &m.Subject,
		&date, &m.Snippet, &m.Confidence, &m.Reason, &m.Status, &movedAt, &m.Direction,
		&m.InReplyTo, &m.Refs, &m.ReplyTo, &m.To, &m.Cc, &m.ListUnsub)
	if err != nil {
		return nil, err
	}
	m.Date = parseTime(date)
	if movedAt.Valid && movedAt.String != "" {
		t := parseTime(movedAt.String)
		m.MovedAt = &t
	}
	return m, nil
}

func (s *Store) AddRule(kind, value, action string) error {
	_, err := s.db.Exec(`
INSERT INTO rules (kind, value, action) VALUES (?, ?, ?)
ON CONFLICT(kind, value) DO UPDATE SET action = excluded.action`, kind, value, action)
	return err
}

func (s *Store) Rules() ([]*Rule, error) {
	rows, err := s.db.Query(`SELECT id, kind, value, action FROM rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Rule
	for rows.Next() {
		r := &Rule{}
		if err := rows.Scan(&r.ID, &r.Kind, &r.Value, &r.Action); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) MessagesByIDs(ids []int64) ([]*Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := selectMsg + ` WHERE id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*Message{}
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		byID[m.ID] = m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// сохраняем порядок запрошенных id (у нас это порядок релевантности)
	out := make([]*Message, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Store) SaveEmbedding(msgID int64, vec []byte) error {
	_, err := s.db.Exec(`
INSERT INTO embeddings (message_id, vector) VALUES (?, ?)
ON CONFLICT(message_id) DO UPDATE SET vector = excluded.vector`, msgID, vec)
	return err
}

type EmbRow struct {
	MsgID   int64
	Status  string
	Vector  []byte
	Account string // ящик письма — чтобы не отдавать закрытые облачной модели
}

// Embeddings возвращает векторы писем; statuses пустой — все.
func (s *Store) Embeddings(statuses ...string) ([]*EmbRow, error) {
	q := `SELECT e.message_id, m.status, e.vector, m.account FROM embeddings e JOIN messages m ON m.id = e.message_id`
	var args []any
	if len(statuses) > 0 {
		q += ` WHERE m.status IN (?` + strings.Repeat(",?", len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EmbRow
	for rows.Next() {
		r := &EmbRow{}
		if err := rows.Scan(&r.MsgID, &r.Status, &r.Vector, &r.Account); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WithoutEmbedding — письма, которые ещё не векторизованы (для бэкфила).
func (s *Store) WithoutEmbedding(limit int) ([]*Message, error) {
	rows, err := s.db.Query(selectMsg+
		` WHERE id NOT IN (SELECT message_id FROM embeddings) ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Setting(key, def string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

// SettingsByPrefix — все настройки с префиксом ключа (реестры вроде chat.expires.*).
func (s *Store) SettingsByPrefix(prefix string) map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE key LIKE ?`, prefix+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[k] = v
		}
	}
	return out
}

func (s *Store) DeleteSettingKey(key string) {
	before := s.Setting(key, "")
	if _, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key); err != nil {
		return
	}
	s.bumpSetting(key)
	s.logSettingChange(SettingChange{Key: key, Before: before, Removed: true})
}

// SettingVersion — версия ключа в этом процессе: растёт на каждой записи
// через хранилище, читается без SQL. Кэш, разобранный при версии v, верен,
// пока версия v: читать её — до чтения значения, поднимать — после записи.
func (s *Store) SettingVersion(key string) uint64 {
	s.verMu.RLock()
	defer s.verMu.RUnlock()
	return s.settingVers[key]
}

func (s *Store) bumpSetting(key string) {
	s.verMu.Lock()
	if s.settingVers == nil {
		s.settingVers = map[string]uint64{}
	}
	s.settingVers[key]++
	s.settingsGen++
	s.verMu.Unlock()
}

type prefixEntry struct {
	gen uint64
	m   map[string]string
}

// SettingsByPrefixShared — то же, что SettingsByPrefix, но ответ держится,
// пока не было ни одной записи настроек: меню на каждом экране спрашивает
// отметки прочтения всех чатов (`chat.read.`), и раньше это был запрос на
// чат, дважды за экран (perf-pass 3.3). Словарь общий — только для чтения.
func (s *Store) SettingsByPrefixShared(prefix string) map[string]string {
	s.verMu.RLock()
	gen := s.settingsGen
	if e, ok := s.prefixCache[prefix]; ok && e.gen == gen {
		s.verMu.RUnlock()
		return e.m
	}
	s.verMu.RUnlock()
	m := s.SettingsByPrefix(prefix)
	s.verMu.Lock()
	if s.prefixCache == nil {
		s.prefixCache = map[string]prefixEntry{}
	}
	s.prefixCache[prefix] = prefixEntry{gen: gen, m: m}
	s.verMu.Unlock()
	return m
}

// SetSetting — единственный писатель таблицы настроек во всём дереве, и
// потому единственное место, где видно, что настройка изменилась. Журнал
// правок (`settings_log.go`) стоит здесь, а не в экранах и не в движке:
// там его пришлось бы просить у каждого нового пути, а забытая просьба —
// это молчащая история изменений, которая выглядит как «никто ничего не
// менял».
func (s *Store) SetSetting(key, value string) error {
	return s.setSetting(key, value, false)
}

// setSetting — общая дверь. `секрет` ставит тот, кто заведомо кладёт
// закрытое значение: без ключа шифрования `seal` отдаёт открытый текст, и
// по виду такое значение от обычной настройки не отличить.
func (s *Store) setSetting(key, value string, secret bool) error {
	before := s.Setting(key, "")
	_, err := s.db.Exec(`
INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return err
	}
	// версия растёт и на записи «того же самого»: два писателя одного
	// ключа иначе оставляли кэш с чужим значением (Y в кэше, X в базе) до
	// следующей записи — лишний разбор дешевле
	s.bumpSetting(key)
	if before == value {
		return nil // «записали то же самое» правкой не является
	}
	s.logSettingChange(SettingChange{Key: key, Before: before, After: value, secret: secret})
	return nil
}

// ---- Локальные копии тел писем ----

type Body struct {
	Text string
	HTML string
}

func (s *Store) SaveBody(msgID int64, b Body) error {
	_, err := s.db.Exec(`
INSERT INTO message_bodies (message_id, text, html, fetched_at) VALUES (?, ?, ?, ?)
ON CONFLICT(message_id) DO UPDATE SET text = excluded.text, html = excluded.html, fetched_at = excluded.fetched_at`,
		msgID, b.Text, b.HTML, fmtTime(time.Now()))
	if err == nil {
		// тело доехало — переписываем запись указателя целиком: до этого
		// письмо искалось только по теме и первым строкам
		if m, e := s.Message(msgID); e == nil {
			s.IndexMessage(m.ID, m.Subject, m.FromAddr+" "+m.FromName, b.Text)
			s.CleanSnippet(m, b.Text)
		}
	}
	return err
}

// CleanSnippet — превью письма из его готового текста.
//
// Сниппет собирается в момент, когда тела ещё нет: из первых четырёх
// килобайт сырого письма. У рассылки там не текст, а голова документа и
// таблица стилей, и в списке появляется «% 96 <![e» — так и было у письма
// ECCO 10 сентября 2026. Когда тело доехало, у нас есть источник получше,
// и держаться за прежний незачем.
//
// Заменяем только когда новое превью явно лучше: короткое или пустое
// начало текста не должно затирать сниппет, который хоть что-то говорил.
func (s *Store) CleanSnippet(m *Message, text string) {
	fresh := previewFromText(text)
	if fresh == "" {
		return
	}
	// короткое начало текста не должно затирать превью, которое хоть
	// что-то говорило, — но если прежнее было не текстом, а обломком
	// вёрстки, то говорило оно неправду, и длина тут не довод
	if len([]rune(fresh)) < len([]rune(m.Snippet))/2 && !looksLikeMarkup(m.Snippet) {
		return
	}
	if fresh == m.Snippet {
		return
	}
	_, _ = s.db.Exec(`UPDATE messages SET snippet = ? WHERE id = ?`, fresh, m.ID)
}

// looksLikeMarkup — прежнее превью собрано не из письма, а из его
// вёрстки: осталось имя свойства CSS, атрибут тега или голая скобка.
func looksLikeMarkup(s string) bool {
	return reMarkup.MatchString(s)
}

var reMarkup = regexp.MustCompile(`(?i)mso-[a-z-]+\s*:|border-collapse\s*:|[\w-]+="[^"]*"|<[a-z!/]|\{[^}]*:[^}]*\}?`)

// previewFromText — первые строки письма одной строкой.
func previewFromText(text string) string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
		if len(parts) >= 4 {
			break
		}
	}
	s := strings.Join(parts, " · ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300])
	}
	return strings.TrimSpace(s)
}

// SnippetsToRepair — письма, чьё превью собрано из сырого письма, а тело
// с тех пор доехало. Разовая уборка: у 1700 писем превью уже лежит.
func (s *Store) SnippetsToRepair(limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT b.message_id FROM message_bodies b
WHERE b.text <> '' ORDER BY b.message_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// BodiesWithHTML — письма, у которых сохранён исходный HTML, значит текст
// можно собрать заново по нынешним правилам чистки.
//
// Сначала здесь стоял поиск по приметам («&zwnj;», «endif», «mso-»), и
// он пропускал самое частое: из условного комментария в текст утекало
// одно голое число. У письма Яндекс ID это было «96» — ни одного слова,
// за которое можно зацепиться. Приметы искали следы, а надо сравнивать
// с тем, что получается сейчас.
func (s *Store) BodiesWithHTML(limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT message_id FROM message_bodies
WHERE html <> '' ORDER BY message_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FixBody — переписать текст письма, оставив оригинальный HTML.
// HTML — архивная копия, её не трогаем никогда: ошибка в чистке не
// должна портить то, из чего текст всегда можно собрать заново.
func (s *Store) FixBody(msgID int64, text string) error {
	_, err := s.db.Exec(`UPDATE message_bodies SET text = ? WHERE message_id = ?`, text, msgID)
	return err
}

func (s *Store) Bodies(msgIDs []int64) (map[int64]Body, error) {
	if len(msgIDs) == 0 {
		return map[int64]Body{}, nil
	}
	q := `SELECT message_id, text, html FROM message_bodies WHERE message_id IN (?` +
		strings.Repeat(",?", len(msgIDs)-1) + `)`
	args := make([]any, len(msgIDs))
	for i, id := range msgIDs {
		args[i] = id
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Body{}
	for rows.Next() {
		var id int64
		var b Body
		if err := rows.Scan(&id, &b.Text, &b.HTML); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

// AttachmentsOfMessage — вложения конкретного письма (для показа в цепочке).
// AttachmentByID — одно вложение с данными письма.
func (s *Store) AttachmentByID(id int64) (*Attachment, error) {
	return scanAtt(s.db.QueryRow(selectAtt+` WHERE a.id = ?`, id))
}

// AttachmentByPath — файл по пути на диске. В хранилище он лежит под
// хэшем, а человеку нужно настоящее имя вложения.
func (s *Store) AttachmentByPath(path string) (*Attachment, bool) {
	a, err := scanAtt(s.db.QueryRow(selectAtt+` WHERE a.path = ? OR a.key = ? OR a.sha256 = ?`, path, path, path))
	return a, err == nil
}

func (s *Store) AttachmentsOfMessage(msgID int64) ([]*Attachment, error) {
	rows, err := s.db.Query(selectAtt+` WHERE a.message_id = ?`, msgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- Отправители (правила + агрегаты по письмам) ----

type Sender struct {
	Addr     string
	Name     string
	Action   string // spam | ham
	Count    int
	LastDate time.Time
	SinceRaw string // когда появилось правило
}

// sqlSenders — запрос экрана «Отправители». Вынесен в константу, чтобы
// тест плана (idx_messages_from_date) судил ровно тот запрос, который
// уходит в базу, а не его копию.
const sqlSenders = `
SELECT r.value, r.action, r.created_at,
       COALESCE(COUNT(m.id), 0),
       COALESCE(MAX(m.date), ''),
       COALESCE(MAX(m.from_name), '')
FROM rules r LEFT JOIN messages m ON m.from_addr = r.value
WHERE r.kind = 'sender' AND r.action = ?
GROUP BY r.value, r.action, r.created_at
ORDER BY COALESCE(MAX(m.date), '') DESC`

// Senders — отправители с правилами: сколько писем, когда последнее.
func (s *Store) Senders(action string) ([]*Sender, error) {
	rows, err := s.db.Query(sqlSenders, action)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Sender
	for rows.Next() {
		sn := &Sender{}
		var last string
		if err := rows.Scan(&sn.Addr, &sn.Action, &sn.SinceRaw, &sn.Count, &last, &sn.Name); err != nil {
			return nil, err
		}
		if last != "" {
			sn.LastDate = parseTime(last)
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

func (s *Store) CountRules(action string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM rules WHERE kind = 'sender' AND action = ?`, action).Scan(&n)
	return n, err
}

// sqlMessagesFrom — тот же запрос, что MessagesFrom шлёт без исключений
// (excluded пуст): тем же индексом, что и «Отправители» (index_plan_test.go
// сверяет план именно по этой строке).
const sqlMessagesFrom = selectMsg + ` WHERE from_addr = ? ORDER BY date DESC LIMIT ?`

// MessagesFrom — история писем отправителя (свежие сверху). о — отсев этой
// сборки (снимок на устройстве); nil — не исключать никого, как раньше.
func (s *Store) MessagesFrom(addr string, ex *Exclusion, limit int) ([]*Message, error) {
	clause, cargs := ex.Clause()
	args := append([]any{addr}, cargs...)
	rows, err := s.db.Query(selectMsg+` WHERE from_addr = ?`+clause+` ORDER BY date DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRule(kind, value string) error {
	_, err := s.db.Exec(`DELETE FROM rules WHERE kind = ? AND value = ?`, kind, value)
	return err
}

// ---- Чеки и счета ----

// SyncHamRules — отправители писем, помеченных «не спам», должны быть в
// белом списке. Восстанавливает правила, потерянные из-за сбоев почты.
func (s *Store) SyncHamRules() (int, error) {
	res, err := s.db.Exec(`
INSERT INTO rules (kind, value, action)
SELECT 'sender', m.from_addr, 'ham' FROM messages m
WHERE m.status = ? AND m.from_addr <> ''
  AND NOT EXISTS (SELECT 1 FROM rules r WHERE r.kind = 'sender' AND r.value = m.from_addr)
GROUP BY m.from_addr`, StatusHam)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) CountAttScan() (done, total int, err error) {
	err = s.db.QueryRow(`SELECT
	(SELECT COUNT(*) FROM messages WHERE att_scanned = 1),
	(SELECT COUNT(*) FROM messages)`).Scan(&done, &total)
	return
}

// DeleteAttachment убирает запись о файле и его вектор.
func (s *Store) DeleteAttachment(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM att_embeddings WHERE attachment_id = ?`, id); err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM file_sources WHERE file_id = ?`, id)
	_, _ = s.db.Exec(`DELETE FROM file_codes WHERE file_id = ?`, id)
	_, err := s.db.Exec(`DELETE FROM attachments WHERE id = ?`, id)
	if err == nil {
		s.touch(ScreenFiles)
	}
	return err
}

// ClearHamStatus снимает пометку «не спам» с писем отправителя.
func (s *Store) ClearHamStatus(addr string) error {
	_, err := s.db.Exec(`UPDATE messages SET status = ? WHERE from_addr = ? AND status = ?`,
		StatusInboxOK, addr, StatusHam)
	if err == nil {
		s.touch(ScreenInbox)
	}
	return err
}

// ---- Скрипты ----

type Script struct {
	ID         int64
	Title      string
	Code       string
	LastRun    time.Time
	LastResult string
	LastMs     float64
}

func (s *Store) CreateScript(title, code string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO scripts (title, code) VALUES (?, ?)`, title, code)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SaveScript(id int64, title, code string) error {
	_, err := s.db.Exec(`UPDATE scripts SET title = ?, code = ? WHERE id = ?`, title, code, id)
	return err
}

func (s *Store) DeleteScript(id int64) error {
	_, err := s.db.Exec(`DELETE FROM scripts WHERE id = ?`, id)
	return err
}

func (s *Store) ScriptRan(id int64, result string, seconds float64) error {
	if r := []rune(result); len(r) > 4000 {
		result = string(r[:4000]) // по рунам, а не по байтам
	}
	_, err := s.db.Exec(`UPDATE scripts SET last_run = ?, last_result = ?, last_ms = ? WHERE id = ?`,
		fmtTime(time.Now()), result, seconds, id)
	return err
}

func (s *Store) Scripts() ([]*Script, error) {
	rows, err := s.db.Query(`SELECT id, title, code, last_run, last_result, last_ms FROM scripts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Script
	for rows.Next() {
		sc := &Script{}
		var lastRun string
		if err := rows.Scan(&sc.ID, &sc.Title, &sc.Code, &lastRun, &sc.LastResult, &sc.LastMs); err != nil {
			return nil, err
		}
		if lastRun != "" {
			sc.LastRun = parseTime(lastRun)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) Script(id int64) (*Script, error) {
	sc := &Script{}
	var lastRun string
	err := s.db.QueryRow(`SELECT id, title, code, last_run, last_result, last_ms FROM scripts WHERE id = ?`, id).
		Scan(&sc.ID, &sc.Title, &sc.Code, &lastRun, &sc.LastResult, &sc.LastMs)
	if err != nil {
		return nil, err
	}
	if lastRun != "" {
		sc.LastRun = parseTime(lastRun)
	}
	return sc, nil
}

// ClearMarksWithoutText — убрать пометки «смотрели и не нашли».
//
// Их ставит перечитывание, чтобы файл без текста не крутился в нём
// вечно. Кнопка «Перечитать файлы» снимает их все: инструмент мог
// появиться после того, как файл приняли, и тогда смотреть надо заново.
// Настоящий извлечённый текст пометка не трогает — только свои.
func (s *Store) ClearMarksWithoutText() (int64, error) {
	res, err := s.db.Exec(`UPDATE attachments SET extracted = ''
		WHERE extracted LIKE 'текста в файле не нашлось%'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ResetAttScan снимает отметку «вложения просмотрены» с писем ящика, у
// которых файлов не нашлось: воркер вложений пройдёт по ним ещё раз.
func (s *Store) ResetAttScan(account string) (int64, error) {
	res, err := s.db.Exec(`UPDATE messages SET att_scanned = 0 WHERE account = ? AND att_scanned = 1
		AND id NOT IN (SELECT message_id FROM attachments)`, account)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkAttachment — модуль помечает файл видом и разобранными полями; текст
// и путь остаются. Пустые поля не затирают уже известное.
func (s *Store) MarkAttachment(id int64, kind, vendor string, amount float64, currency, summary string) error {
	_, err := s.db.Exec(`UPDATE attachments SET
		kind = CASE WHEN ? != '' THEN ? ELSE kind END,
		vendor = CASE WHEN ? != '' THEN ? ELSE vendor END,
		amount = CASE WHEN ? > 0 THEN ? ELSE amount END,
		currency = CASE WHEN ? != '' THEN ? ELSE currency END,
		summary = CASE WHEN ? != '' THEN ? ELSE summary END
		WHERE id = ?`, kind, kind, vendor, vendor, amount, amount, currency, currency, summary, summary, id)
	if err == nil {
		s.touch(ScreenFiles)
	}
	return err
}

func orIn(d string) string {
	if d == "" {
		return "in"
	}
	return d
}

// MarkOutgoing помечает исходящими письма от адресов самих ящиков — на
// случай, если они попали в базу до появления направления.
func (s *Store) MarkOutgoing(addrs []string) {
	for _, a := range addrs {
		if a == "" {
			continue
		}
		_, _ = s.db.Exec(`UPDATE messages SET direction = 'out' WHERE direction = 'in' AND from_addr = ? AND (reason LIKE 'папка:%' AND status = ?)`, strings.ToLower(a), StatusArchived)
	}
}

// DirtySnippets — превью, в которых осталось устройство письма, а не его
// текст: границы частей, заголовки, правила CSS.
func (s *Store) DirtySnippets(limit int) (map[int64]string, error) {
	// к MIME и base64 добавились сущности и остатки разметки: «&nbsp;»,
	// «&#847;&zwnj;» и «<td style …>» в превью — это не текст письма
	rows, err := s.db.Query(`SELECT id, snippet FROM messages
		WHERE snippet LIKE '%Content-Type:%' OR snippet LIKE '%@viewport%'
		   OR snippet LIKE '%@media%' OR snippet LIKE '----%' OR snippet LIKE '%Content-Transfer-Encoding%'
		   OR snippet LIKE '%&nbsp;%' OR snippet LIKE '%&#%' OR snippet LIKE '%&zwnj;%'
		   OR snippet LIKE '%&lt;%' OR snippet LIKE '%<td%' OR snippet LIKE '%<div%' OR snippet LIKE '%<span%'
		   OR snippet LIKE '%/*%' OR snippet LIKE '%WordDocument%' OR snippet LIKE '%xmlns:%'
		   OR snippet LIKE '%' || char(10240) || '%' OR snippet LIKE '%' || char(12644) || '%'
		   OR snippet LIKE '%mso-%' OR snippet LIKE '%border-collapse%' OR snippet LIKE '%="%"%'
		   OR snippet GLOB '[A-Za-z0-9+/]*' AND snippet NOT GLOB '*[ ]*[а-яА-Я]*'
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var snip string
		if err := rows.Scan(&id, &snip); err != nil {
			return nil, err
		}
		out[id] = snip
	}
	return out, rows.Err()
}

// SetSnippet — превью письма после чистки. Само письмо не трогаем: в
// архиве Сёмы ничего не удаляется и не переписывается.
func (s *Store) SetSnippet(id int64, snippet string) error {
	_, err := s.db.Exec(`UPDATE messages SET snippet = ? WHERE id = ?`, snippet, id)
	return err
}

// ---- Чистое закрытие ----

const cleanKey = "server.clean_shutdown"

// CleanShutdown — прошлый запуск закрылся начисто? Первый запуск считается
// чистым: пугать пустую установку нечем.
func (s *Store) CleanShutdown() bool {
	return s.Setting(cleanKey, "1") == "1"
}

// MarkRunning — «работаю»: ставится сразу после старта. Если процесс убьют,
// признак так и останется — по нему следующий запуск и узнает о падении.
func (s *Store) MarkRunning() {
	_ = s.SetSetting(cleanKey, "0")
}

// MarkCleanShutdown — «закрыт чисто». Пишется последней транзакцией перед
// закрытием базы: раньше — соврёт, если упадём на выходе.
func (s *Store) MarkCleanShutdown() {
	_ = s.SetSetting(cleanKey, "1")
}

// QuickCheck — целостность базы без полного обхода: `quick_check`
// ловит то же, что и `integrity_check`, но не читает каждую страницу.
// Зовётся после нечистого закрытия: на аккуратный выход не рассчитываем.
func (s *Store) QuickCheck() (string, error) {
	var result string
	if err := s.db.QueryRow(`PRAGMA quick_check(5)`).Scan(&result); err != nil {
		return "", err
	}
	return result, nil
}

// CheckpointLog — checkpoint журнала предзаписи. Не успели — закрываемся
// всё равно: журнал дочитается при следующем открытии, это его работа.
func (s *Store) CheckpointLog(timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("журнал предзаписи не сведён за %s — дочитается при следующем открытии", timeout)
	}
}
