package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

// Telegram как источник: учётки, выбранные чаты и курсоры. Сессия хранится
// шифрованной тем же ключом, что и секреты, и наружу не выдаётся.
//
// Учёток несколько: личная и рабочая живут рядом, у каждой свои чаты,
// права и пределы обращений. **Номер чата уникален только внутри
// учётки**: один и тот же id встречается у разных аккаунтов, а один и тот
// же собеседник в двух учётках — это два разных разговора. Поэтому
// идентичность везде составная — «учётка + номер», и первичные ключи
// таблиц устроены так же. Иначе чат рабочей учётки молча затирал бы
// личный (та же грабля, что была с постами каналов и группами
// обсуждения, см. tgAddr).

// Что хранилище подставляет учётке, у которой пределы обращений не названы:
// как часто заходить (минуты) и сколько сообщений брать за заход. Экран
// «Ответы и пределы» берёт умолчание поля отсюда, а не вписывает рядом.
const (
	TGEveryDefault = 10
	TGBatchDefault = 200
)

type TGAccount struct {
	Phone   string
	Title   string // как человек её зовёт: «личная», «рабочая»
	State   string // new | code | password | ok | fail
	Note    string
	Session string // строка сессии, всегда шифрованная
	Every   int    // как часто заходить, минуты
	Batch   int    // сколько сообщений за заход
	CanSend bool   // разрешено ли отправлять от вашего имени
	Enabled bool   // слушать эту учётку
	Sort    int    // порядок в списке
	Created string
	Last    string
}

// Имя учётки для человека: название, если оно есть, иначе номер.
func (a *TGAccount) Name() string {
	if a == nil {
		return ""
	}
	if t := strings.TrimSpace(a.Title); t != "" {
		return t
	}
	return a.Phone
}

// TGChat — чат или канал в выборе.
type TGChat struct {
	Account string // номер учётки, в которой живёт этот чат
	ID      int64
	Title   string
	Kind    string // user | group | channel
	Name    string // @username: по нему человек и модель называют собеседника
	Collect bool   // читать переписку
	Reply   bool   // отвечать, когда пишут вам
	Send    bool   // писать первым
	Files   bool   // забирать вложения
	Blocked bool   // не трогать вовсе: ни читать, ни отвечать
	Admin   bool   // из этого чата Сёмой управляют, как из его собственного чата
	Class   string // класс чувствительности по умолчанию
	Access  int64  // access_hash: без него к чату не обратиться
	Forum   bool   // группа с топиками: переписка разложена по темам
	CanPost bool   // канал: аккаунт — владелец или админ с правом публиковать
	Cursor  int    // id последнего разобранного сообщения
	Count   int
	Updated string
	// тема группы с топиками, к которой относится разговор: не колонка, а
	// адрес — заполняется, когда чат Сёмы привязан к одной теме
	Topic      int
	TopicTitle string
}

// TGTopic — тема группы с топиками и её права. Право -1 значит «как у
// группы»: переопределяет человек, и только то, что решил сам.
type TGTopic struct {
	Account string
	ChatID  int64
	ID      int
	Title   string
	Collect int
	Reply   int
	Send    int
	Files   int
	Blocked int
	Updated string
}

// TopicInherit — право темы не задано: действует право группы.
const TopicInherit = -1

// TGTopics — темы группы, по названию.
func (s *Store) TGTopics(account string, chatID int64) ([]*TGTopic, error) {
	rows, err := s.db.Query(`SELECT account, chat_id, topic_id, title, collect, reply, send, files, blocked, updated
		FROM tg_topics WHERE account = ? AND chat_id = ? ORDER BY topic_id`, account, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TGTopic
	for rows.Next() {
		t := &TGTopic{}
		if err := rows.Scan(&t.Account, &t.ChatID, &t.ID, &t.Title, &t.Collect, &t.Reply, &t.Send, &t.Files, &t.Blocked, &t.Updated); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TGTopic(account string, chatID int64, topic int) (*TGTopic, bool) {
	t := &TGTopic{}
	err := s.db.QueryRow(`SELECT account, chat_id, topic_id, title, collect, reply, send, files, blocked, updated
		FROM tg_topics WHERE account = ? AND chat_id = ? AND topic_id = ?`, account, chatID, topic).
		Scan(&t.Account, &t.ChatID, &t.ID, &t.Title, &t.Collect, &t.Reply, &t.Send, &t.Files, &t.Blocked, &t.Updated)
	return t, err == nil
}

// TGTopicCollect — хоть у одной темы группы чтение включено своим правом.
// Нужно входящим: право группы может быть выключено, а тема — открыта.
func (s *Store) TGTopicCollect(account string, chatID int64) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tg_topics WHERE account = ? AND chat_id = ? AND collect = 1`,
		account, chatID).Scan(&n)
	return n > 0
}

// SaveTGTopic — тема из Telegram: название обновляется, права человека
// остаются, как у чатов.
func (s *Store) SaveTGTopic(account string, chatID int64, topic int, title string) error {
	_, err := s.db.Exec(`INSERT INTO tg_topics (account, chat_id, topic_id, title, updated) VALUES (?,?,?,?,?)
		ON CONFLICT(account, chat_id, topic_id) DO UPDATE SET title = excluded.title, updated = excluded.updated`,
		account, chatID, topic, title, time.Now().Format(time.RFC3339))
	return err
}

// SetTGTopicRight — право темы: 1, 0 или TopicInherit («как у группы»).
func (s *Store) SetTGTopicRight(account string, chatID int64, topic int, right string, v int) error {
	col := map[string]string{"collect": "collect", "reply": "reply", "send": "send", "files": "files", "blocked": "blocked"}[right]
	if col == "" {
		return fmt.Errorf("неизвестное право %q", right)
	}
	if v != 0 && v != 1 {
		v = TopicInherit
	}
	_, err := s.db.Exec(`UPDATE tg_topics SET `+col+` = ?, updated = ? WHERE account = ? AND chat_id = ? AND topic_id = ?`,
		v, time.Now().Format(time.RFC3339), account, chatID, topic)
	return err
}

// TGPostLink — пост канала и его комментарии. Пост живёт в канале, а
// комментарии — в привязанной группе обсуждения, ответами на копию
// поста (Root). Права и разговор остаются у канала, адрес отправки и
// архива — у группы.
type TGPostLink struct {
	Account string
	Channel int64
	Post    int
	Group   int64
	Access  int64 // access_hash группы: без него к ней не обратиться
	Root    int   // номер копии поста в группе обсуждения
}

// SaveTGPostLink — запомнить привязку. Повтор перезаписывает: у группы
// мог смениться access_hash.
func (s *Store) SaveTGPostLink(l *TGPostLink) error {
	_, err := s.db.Exec(`INSERT INTO tg_post_links (account, channel_id, post_id, group_id, group_access, root_id, updated)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(account, channel_id, post_id) DO UPDATE SET group_id = excluded.group_id,
			group_access = excluded.group_access, root_id = excluded.root_id, updated = excluded.updated`,
		l.Account, l.Channel, l.Post, l.Group, l.Access, l.Root, time.Now().Format(time.RFC3339))
	return err
}

// TGPostLink — привязка по посту: куда уходит ответ под этот пост.
func (s *Store) TGPostLink(account string, channel int64, post int) (*TGPostLink, bool) {
	l := &TGPostLink{}
	err := s.db.QueryRow(`SELECT account, channel_id, post_id, group_id, group_access, root_id
		FROM tg_post_links WHERE account = ? AND channel_id = ? AND post_id = ?`, account, channel, post).
		Scan(&l.Account, &l.Channel, &l.Post, &l.Group, &l.Access, &l.Root)
	return l, err == nil
}

// TGPostByRoot — привязка по корню ветки в группе обсуждения: так
// входящий комментарий узнаёт свой пост.
func (s *Store) TGPostByRoot(account string, group int64, root int) (*TGPostLink, bool) {
	l := &TGPostLink{}
	err := s.db.QueryRow(`SELECT account, channel_id, post_id, group_id, group_access, root_id
		FROM tg_post_links WHERE account = ? AND group_id = ? AND root_id = ?`, account, group, root).
		Scan(&l.Account, &l.Channel, &l.Post, &l.Group, &l.Access, &l.Root)
	return l, err == nil
}

func (s *Store) migrateTelegram() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS tg_account (
			phone    TEXT PRIMARY KEY,
			state    TEXT NOT NULL DEFAULT 'new',
			note     TEXT NOT NULL DEFAULT '',
			session  TEXT NOT NULL DEFAULT '',
			every_m  INTEGER NOT NULL DEFAULT 10,
			batch    INTEGER NOT NULL DEFAULT 200,
			can_send INTEGER NOT NULL DEFAULT 0,
			created  TEXT NOT NULL,
			last     TEXT NOT NULL DEFAULT ''
		)`,
		// учёток несколько: у каждой своё название, свой тумблер и своё
		// место в списке. Включена по умолчанию — иначе обновление
		// выключило бы единственную рабочую учётку молча
		`ALTER TABLE tg_account ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tg_account ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE tg_account ADD COLUMN sort INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS tg_chats (
			account TEXT NOT NULL DEFAULT '',
			id      INTEGER NOT NULL,
			title   TEXT NOT NULL DEFAULT '',
			kind    TEXT NOT NULL DEFAULT '',
			collect INTEGER NOT NULL DEFAULT 0,
			class   TEXT NOT NULL DEFAULT '',
			access  INTEGER NOT NULL DEFAULT 0,
			cursor  INTEGER NOT NULL DEFAULT 0,
			count   INTEGER NOT NULL DEFAULT 0,
			updated TEXT NOT NULL DEFAULT '',
			name    TEXT NOT NULL DEFAULT '',
			reply   INTEGER NOT NULL DEFAULT 0,
			send    INTEGER NOT NULL DEFAULT 0,
			files   INTEGER NOT NULL DEFAULT 0,
			blocked INTEGER NOT NULL DEFAULT 0,
			admin   INTEGER NOT NULL DEFAULT 0,
			forum   INTEGER NOT NULL DEFAULT 0,
			can_post INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (account, id)
		)`,
		// права по каждому собеседнику отдельно: читать, отвечать, писать
		// первым, забирать файлы — и «не трогать вовсе». На свежей базе
		// колонки уже есть, на старой досыпаются здесь
		`ALTER TABLE tg_chats ADD COLUMN name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tg_chats ADD COLUMN reply INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tg_chats ADD COLUMN send INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tg_chats ADD COLUMN files INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tg_chats ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tg_chats ADD COLUMN admin INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tg_chats ADD COLUMN forum INTEGER NOT NULL DEFAULT 0`,
		// канал, где аккаунт может публиковать: приходит от Telegram при
		// каждом обновлении списка, а не решается человеком
		`ALTER TABLE tg_chats ADD COLUMN can_post INTEGER NOT NULL DEFAULT 0`,
		// темы группы с топиками: права переопределяются по каждой, -1 —
		// как у группы
		`CREATE TABLE IF NOT EXISTS tg_topics (
			account  TEXT NOT NULL DEFAULT '',
			chat_id  INTEGER NOT NULL,
			topic_id INTEGER NOT NULL,
			title    TEXT NOT NULL DEFAULT '',
			collect  INTEGER NOT NULL DEFAULT -1,
			reply    INTEGER NOT NULL DEFAULT -1,
			send     INTEGER NOT NULL DEFAULT -1,
			files    INTEGER NOT NULL DEFAULT -1,
			blocked  INTEGER NOT NULL DEFAULT -1,
			updated  TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (account, chat_id, topic_id)
		)`,
		// привязка поста канала к его комментариям: они живут не в
		// канале, а в группе обсуждения, куда Telegram пересылает копию
		// поста. Без записанной привязки комментарий неотличим от
		// обычного сообщения группы, а спрашивать её у Telegram на
		// каждый комментарий — лишний заход в сеть
		`CREATE TABLE IF NOT EXISTS tg_post_links (
			account      TEXT NOT NULL DEFAULT '',
			channel_id   INTEGER NOT NULL,
			post_id      INTEGER NOT NULL,
			group_id     INTEGER NOT NULL,
			group_access INTEGER NOT NULL DEFAULT 0,
			root_id      INTEGER NOT NULL,
			updated      TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (account, channel_id, post_id)
		)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue // миграция идёт на каждом старте
			}
			return fmt.Errorf("telegram: %w", err)
		}
	}
	if err := s.migrateTGOutbox(); err != nil {
		return err
	}
	if err := s.migrateTGMessages(); err != nil {
		return err
	}
	if err := s.migrateTGMultiAccount(); err != nil {
		return err
	}
	// индекс по учётке и корню ветки — только когда колонка уже есть:
	// на старой базе он завёлся бы раньше переезда и уронил бы миграцию
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_tg_post_root ON tg_post_links(account, group_id, root_id)`)
	return err
}

// hasColumn — есть ли у таблицы такая колонка. По ней и понимаем,
// нужен ли переезд: отдельная метка в настройках соврала бы, если базу
// восстановили из копии до переезда.
func (s *Store) hasColumn(table, column string) bool {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && name == column {
			return true
		}
	}
	return false
}

// migrateTGMultiAccount — переезд с «одной учётки» на список. Всё, что
// было собрано, достаётся нынешней учётке: её номер проставляется в чаты,
// темы, привязки постов, сообщения и исходящие, а ключи настроек и
// уведомлений получают номер в адресе. Сессия при этом не трогается
// вовсе — она лежит в tg_account, который не перестраивается.
func (s *Store) migrateTGMultiAccount() error {
	if s.hasColumn("tg_chats", "account") {
		return nil
	}
	var phone string
	// строк в tg_account могло не быть вовсе (Telegram не подключали):
	// тогда переезжать нечему, но таблицы всё равно надо привести к
	// нынешнему виду
	_ = s.db.QueryRow(`SELECT phone FROM tg_account ORDER BY rowid LIMIT 1`).Scan(&phone)

	// ключи настроек — своей меткой и своей дверью: таблицу настроек
	// пишут только SetSetting и DeleteSettingKey, иначе правка не попадёт
	// в журнал изменений. Метка отдельная, чтобы переезд ключей и
	// перестройка таблиц повторялись независимо, если сервер упал между
	if phone != "" && s.Setting("tg.keys_moved", "") == "" {
		if err := s.migrateTGKeys(phone); err != nil {
			return err
		}
		if err := s.SetSetting("tg.keys_moved", phone); err != nil {
			return err
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// перестройка таблиц: первичный ключ в SQLite не меняется ALTER'ом.
	// Перед переименованием стоит посмотреть sqlite_master на REFERENCES
	// к этим таблицам — RENAME переписывает чужие внешние ключи; на
	// tg_* сегодня не ссылается никто
	for _, step := range []struct{ create, move, oldTable, newTable string }{
		{
			create: `CREATE TABLE tg_chats_2 (
				account TEXT NOT NULL DEFAULT '',
				id      INTEGER NOT NULL,
				title   TEXT NOT NULL DEFAULT '',
				kind    TEXT NOT NULL DEFAULT '',
				collect INTEGER NOT NULL DEFAULT 0,
				class   TEXT NOT NULL DEFAULT '',
				access  INTEGER NOT NULL DEFAULT 0,
				cursor  INTEGER NOT NULL DEFAULT 0,
				count   INTEGER NOT NULL DEFAULT 0,
				updated TEXT NOT NULL DEFAULT '',
				name    TEXT NOT NULL DEFAULT '',
				reply   INTEGER NOT NULL DEFAULT 0,
				send    INTEGER NOT NULL DEFAULT 0,
				files   INTEGER NOT NULL DEFAULT 0,
				blocked INTEGER NOT NULL DEFAULT 0,
				admin   INTEGER NOT NULL DEFAULT 0,
				forum   INTEGER NOT NULL DEFAULT 0,
				can_post INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (account, id)
			)`,
			move: `INSERT INTO tg_chats_2 (account, id, title, kind, collect, class, access, cursor, count, updated,
				name, reply, send, files, blocked, admin, forum, can_post)
				SELECT ?, id, title, kind, collect, class, access, cursor, count, updated,
					name, reply, send, files, blocked, admin, forum, can_post FROM tg_chats`,
			oldTable: "tg_chats", newTable: "tg_chats_2",
		},
		{
			create: `CREATE TABLE tg_topics_2 (
				account  TEXT NOT NULL DEFAULT '',
				chat_id  INTEGER NOT NULL,
				topic_id INTEGER NOT NULL,
				title    TEXT NOT NULL DEFAULT '',
				collect  INTEGER NOT NULL DEFAULT -1,
				reply    INTEGER NOT NULL DEFAULT -1,
				send     INTEGER NOT NULL DEFAULT -1,
				files    INTEGER NOT NULL DEFAULT -1,
				blocked  INTEGER NOT NULL DEFAULT -1,
				updated  TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (account, chat_id, topic_id)
			)`,
			move: `INSERT INTO tg_topics_2 (account, chat_id, topic_id, title, collect, reply, send, files, blocked, updated)
				SELECT ?, chat_id, topic_id, title, collect, reply, send, files, blocked, updated FROM tg_topics`,
			oldTable: "tg_topics", newTable: "tg_topics_2",
		},
		{
			create: `CREATE TABLE tg_post_links_2 (
				account      TEXT NOT NULL DEFAULT '',
				channel_id   INTEGER NOT NULL,
				post_id      INTEGER NOT NULL,
				group_id     INTEGER NOT NULL,
				group_access INTEGER NOT NULL DEFAULT 0,
				root_id      INTEGER NOT NULL,
				updated      TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (account, channel_id, post_id)
			)`,
			move: `INSERT INTO tg_post_links_2 (account, channel_id, post_id, group_id, group_access, root_id, updated)
				SELECT ?, channel_id, post_id, group_id, group_access, root_id, updated FROM tg_post_links`,
			oldTable: "tg_post_links", newTable: "tg_post_links_2",
		},
	} {
		if _, err := tx.Exec(step.create); err != nil {
			return fmt.Errorf("telegram: учётки в %s: %w", step.oldTable, err)
		}
		if _, err := tx.Exec(step.move, phone); err != nil {
			return fmt.Errorf("telegram: перенос %s: %w", step.oldTable, err)
		}
		if _, err := tx.Exec(`DROP TABLE ` + step.oldTable); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE ` + step.newTable + ` RENAME TO ` + step.oldTable); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_tg_post_root ON tg_post_links(account, group_id, root_id)`); err != nil {
		return err
	}

	// сообщения и исходящие: там первичный ключ свой, хватает колонки и
	// нового уникального индекса
	for _, q := range []string{
		`ALTER TABLE tg_messages ADD COLUMN account TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tg_outbox ADD COLUMN account TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE tg_messages SET account = ?`, phone); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE tg_outbox SET account = ?`, phone); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_tg_msg`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tg_msg ON tg_messages(account, chat_id, msg_id)`); err != nil {
		return err
	}

	// ключи уведомлений: «tg-wait:7/3» → «tg-wait:+7999…:7/3». Иначе
	// открытая запись осталась бы висеть навсегда — закрыть её новым
	// ключом уже нечем
	if phone != "" {
		for _, prefix := range []string{"tg:", "tg-wait:", "tg-auto:", "tg-quiet:"} {
			if _, err := tx.Exec(`UPDATE notifications SET key = ? || substr(key, ?) WHERE key LIKE ?`,
				prefix+phone+":", len(prefix)+1, prefix+"%"); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// migrateTGKeys — адреса, в которых стоял голый номер чата, получают
// номер учётки. Без этого чат 7 личной учётки и чат 7 рабочей смотрели бы
// в один и тот же разговор Сёмы, а «не напоминать» в одной гасило бы
// семафор в другой.
//
// Переезд идёт через дверь настроек (SetSetting/DeleteSettingKey), а не
// одним UPDATE: таблицу настроек пишут только они, иначе правка не
// попадёт в журнал изменений и человек не увидит, что у него переехали
// адреса.
func (s *Store) migrateTGKeys(phone string) error {
	// «tg.chat.7» → «tg.chat.+7999….7». Общие пределы («tg.history»,
	// «tg.help_word») выглядят так же, поэтому берём только те, у
	// которых после точки стоит номер чата. Разделитель повторяет тот,
	// которым ключ собирается в коде: у «tg.quiet» это двоеточие (там
	// стоит tgRef целиком), у остальных — точка. Разойтись им нельзя:
	// ключ, собранный иначе, — это просто другой ключ
	for _, kind := range []struct{ prefix, sep string }{
		{"tg.chat.", "."}, {"tg.history.", "."},
		{"tg.wait_off.", "."}, {"tg.wait_skip.", "."},
		{"tg.quiet.", ":"},
	} {
		for key, value := range s.SettingsByPrefix(kind.prefix) {
			tail := strings.TrimPrefix(key, kind.prefix)
			if tail == "" || tail[0] < '0' || tail[0] > '9' {
				continue // это общий предел, а не адрес чата
			}
			if err := s.SetSetting(kind.prefix+phone+kind.sep+tail, value); err != nil {
				return err
			}
			s.DeleteSettingKey(key)
		}
	}
	// хэш кода входа: он всегда про одну учётку
	if hash := s.Setting("tg.code_hash", ""); hash != "" {
		if err := s.SetSetting("tg.code_hash."+phone, hash); err != nil {
			return err
		}
	}
	s.DeleteSettingKey("tg.code_hash")
	return nil
}

// TGAccounts — все учётки по порядку. Список, а не «та единственная»:
// личная и рабочая живут рядом.
func (s *Store) TGAccounts() ([]*TGAccount, error) {
	rows, err := s.db.Query(`SELECT phone, title, state, note, session, every_m, batch, can_send, enabled, sort, created, last
		FROM tg_account ORDER BY sort, created, phone`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TGAccount
	for rows.Next() {
		a := &TGAccount{}
		var send, enabled int
		if err := rows.Scan(&a.Phone, &a.Title, &a.State, &a.Note, &a.Session, &a.Every, &a.Batch,
			&send, &enabled, &a.Sort, &a.Created, &a.Last); err != nil {
			return nil, err
		}
		a.CanSend, a.Enabled = send == 1, enabled == 1
		a.Session = s.open(a.Session) // расшифровка на выходе
		out = append(out, a)
	}
	return out, rows.Err()
}

// TGAccount — учётка по номеру телефона.
func (s *Store) TGAccount(phone string) (*TGAccount, bool) {
	list, err := s.TGAccounts()
	if err != nil {
		return nil, false
	}
	for _, a := range list {
		if a.Phone == phone {
			return a, true
		}
	}
	return nil, false
}

// TGAccountFirst — первая учётка по порядку: на ней открывается экран, и
// она же имеется в виду, когда учётка одна.
func (s *Store) TGAccountFirst() (*TGAccount, bool) {
	list, err := s.TGAccounts()
	if err != nil || len(list) == 0 {
		return nil, false
	}
	return list[0], true
}

func (s *Store) SaveTGAccount(a *TGAccount) error {
	return s.saveTGAccountRow(a, true)
}

// saveTGAccountRow — запись учётки. `беречьСессию` выключается только
// при смене ключа шифрования: там лежащая сессия нарочно зашифрована
// прежним ключом, и проверка «читается ли она нынешним» запретила бы
// саму перешифровку.
func (s *Store) saveTGAccountRow(a *TGAccount, keepSession bool) error {
	a.Phone = strings.TrimSpace(a.Phone)
	if a.Phone == "" {
		return fmt.Errorf("нужен номер телефона")
	}
	if a.Every <= 0 {
		a.Every = TGEveryDefault
	}
	if a.Batch <= 0 {
		a.Batch = TGBatchDefault
	}
	if a.Created == "" {
		a.Created = time.Now().Format(time.RFC3339)
	}
	// сессия — как секрет: без ключа шифрования не пишем вовсе
	sealed := a.Session
	if sealed != "" {
		if s.box == nil {
			return fmt.Errorf("нет ключа шифрования: сессию Telegram открытой не храню")
		}
		sealed = s.seal(sealed)
	}
	// лежащая сессия, которую нынешний ключ не читает, не перезаписывается:
	// сервер с чужим SEMA_KEY иначе молча стёр бы вход — так уже случалось
	// с корнем агентов
	var prev string
	if err := s.db.QueryRow(`SELECT session FROM tg_account WHERE phone = ?`, a.Phone).Scan(&prev); keepSession && err == nil &&
		secret.Encrypted(prev) && s.open(prev) == "" {
		return fmt.Errorf("у учётки %s уже есть сессия, но она не читается этим ключом — "+
			"перезапись отклонена: проверьте SEMA_KEY, с которым запущен сервер", a.Phone)
	}
	send, enabled := 0, 0
	if a.CanSend {
		send = 1
	}
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`INSERT INTO tg_account (phone, title, state, note, session, every_m, batch, can_send, enabled, sort, created, last)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(phone) DO UPDATE SET title = excluded.title, state = excluded.state, note = excluded.note,
			session = excluded.session, every_m = excluded.every_m, batch = excluded.batch,
			can_send = excluded.can_send, enabled = excluded.enabled, sort = excluded.sort, last = excluded.last`,
		a.Phone, a.Title, a.State, a.Note, sealed, a.Every, a.Batch, send, enabled, a.Sort, a.Created, a.Last)
	return err
}

func (s *Store) DeleteTGAccount(phone string) error {
	_, err := s.db.Exec(`DELETE FROM tg_account WHERE phone = ?`, phone)
	return err
}

// TGChats — чаты учётки. Пустой номер — чаты всех учёток разом: так
// собираются опись для модели и поиск собеседника по имени.
func (s *Store) TGChats(account string, onlyCollected bool) ([]*TGChat, error) {
	q := `SELECT account, id, title, kind, name, collect, reply, send, files, blocked, admin, class, access, forum, can_post, cursor, count, updated FROM tg_chats`
	var where []string
	var args []any
	if account != "" {
		where = append(where, `account = ?`)
		args = append(args, account)
	}
	if onlyCollected {
		where = append(where, `collect = 1`)
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY collect DESC, title`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TGChat
	for rows.Next() {
		c := &TGChat{}
		var collect, reply, send, files, blocked, admin, forum, canPost int
		if err := rows.Scan(&c.Account, &c.ID, &c.Title, &c.Kind, &c.Name, &collect, &reply, &send, &files, &blocked, &admin,
			&c.Class, &c.Access, &forum, &canPost, &c.Cursor, &c.Count, &c.Updated); err != nil {
			return nil, err
		}
		c.Collect, c.Reply, c.Send, c.Files, c.Blocked, c.Admin = collect == 1, reply == 1, send == 1, files == 1, blocked == 1, admin == 1
		c.Forum, c.CanPost = forum == 1, canPost == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// TGChat — чат учётки по номеру. Пара «учётка + номер» и есть
// идентичность: тот же номер у другой учётки — другой чат.
func (s *Store) TGChat(account string, id int64) (*TGChat, bool) {
	list, err := s.TGChats(account, false)
	if err != nil {
		return nil, false
	}
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return nil, false
}

// SaveTGChat — запись чата из списка. Выбор человека не затирается при
// обновлении списка: collect и class сохраняются, если чат уже знаком.
func (s *Store) SaveTGChat(c *TGChat) error {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	// Права человека при обновлении списка не затираются: из Telegram
	// приходят имя, счётчик и access_hash, а кому отвечать и у кого брать
	// файлы решает человек.
	_, err := s.db.Exec(`INSERT INTO tg_chats (account, id, title, kind, name, collect, reply, send, files, blocked, admin, class, access, forum, can_post, cursor, count, updated)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(account, id) DO UPDATE SET title = excluded.title, kind = excluded.kind, name = excluded.name,
			access = excluded.access, forum = excluded.forum, can_post = excluded.can_post, cursor = excluded.cursor, count = excluded.count, updated = excluded.updated`,
		c.Account, c.ID, c.Title, c.Kind, c.Name, b(c.Collect), b(c.Reply), b(c.Send), b(c.Files), b(c.Blocked), b(c.Admin),
		c.Class, c.Access, b(c.Forum), b(c.CanPost), c.Cursor, c.Count, time.Now().Format(time.RFC3339))
	return err
}

// SetTGChatRight — переключить одно право чата. Права выдаёт человек, и
// каждое отдельно: «читать» не значит «отвечать», а «отвечать» не значит
// «писать первым».
func (s *Store) SetTGChatRight(account string, id int64, right string, on bool) error {
	col := map[string]string{
		"collect": "collect", "reply": "reply", "send": "send",
		"files": "files", "blocked": "blocked", "admin": "admin",
	}[right]
	if col == "" {
		return fmt.Errorf("неизвестное право %q", right)
	}
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE tg_chats SET `+col+` = ?, updated = ? WHERE account = ? AND id = ?`,
		v, time.Now().Format(time.RFC3339), account, id)
	return err
}

func (s *Store) SetTGChatCollect(account string, id int64, on bool, class string) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE tg_chats SET collect = ?, class = ? WHERE account = ? AND id = ?`, v, class, account, id)
	return err
}

// SetTGChatClass — класс чувствительности чата решает человек: у канала
// по умолчанию «обычное», но закрытый канал он вправе поднять до
// «чувствительного», и тогда облачная модель постов не увидит.
func (s *Store) SetTGChatClass(account string, id int64, class string) error {
	if class != ClassPlain && class != ClassSensitive && class != ClassSecret {
		return fmt.Errorf("неизвестный класс %q", class)
	}
	_, err := s.db.Exec(`UPDATE tg_chats SET class = ?, updated = ? WHERE account = ? AND id = ?`,
		class, time.Now().Format(time.RFC3339), account, id)
	return err
}

func (s *Store) SetTGChatCursor(account string, id int64, cursor, added int) error {
	_, err := s.db.Exec(`UPDATE tg_chats SET cursor = ?, count = count + ?, updated = ? WHERE account = ? AND id = ?`,
		cursor, added, time.Now().Format(time.RFC3339), account, id)
	return err
}

// TGOutbox — предложенное сообщение, которое ждёт кнопки человека. Текст
// живёт здесь, а не в описании экрана: отправляется то, что предложила
// Сёма, а не то, что вернулось из клиента.
type TGOutbox struct {
	ID       int64
	Account  string
	ChatID   int64
	Topic    int   // тема группы с топиками: туда и уйдёт
	SemaChat int64 // чат с Сёмой, где предложено: туда же уходит подтверждение
	Who      string
	Text     string
	State    string // waiting | sent | rejected
	Note     string
	Created  string
}

func (s *Store) migrateTGOutbox() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS tg_outbox (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		account   TEXT NOT NULL DEFAULT '',
		chat_id   INTEGER NOT NULL,
		sema_chat INTEGER NOT NULL DEFAULT 0,
		who     TEXT NOT NULL DEFAULT '',
		text    TEXT NOT NULL,
		state   TEXT NOT NULL DEFAULT 'waiting',
		note    TEXT NOT NULL DEFAULT '',
		created TEXT NOT NULL
	)`)
	if err != nil {
		return err
	}
	// таблица могла появиться раньше поля: миграция идёт на каждом старте
	for _, q := range []string{
		`ALTER TABLE tg_outbox ADD COLUMN sema_chat INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tg_outbox ADD COLUMN topic INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := s.db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

func (s *Store) AddTGOutbox(o *TGOutbox) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO tg_outbox (account, chat_id, topic, sema_chat, who, text, state, created) VALUES (?,?,?,?,?,?,'waiting',?)`,
		o.Account, o.ChatID, o.Topic, o.SemaChat, o.Who, o.Text, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) TGOutboxByID(id int64) (*TGOutbox, bool) {
	o := &TGOutbox{}
	err := s.db.QueryRow(`SELECT id, account, chat_id, topic, sema_chat, who, text, state, note, created FROM tg_outbox WHERE id = ?`, id).
		Scan(&o.ID, &o.Account, &o.ChatID, &o.Topic, &o.SemaChat, &o.Who, &o.Text, &o.State, &o.Note, &o.Created)
	return o, err == nil
}

// EditTGDraft — поправить текст черновика, не отправляя его. Сёма
// предлагает текст, но последнее слово за человеком: раньше выбор был
// «отправить как есть» или «отклонить и начать заново» из-за одного
// неверного слова. Правится только ждущий черновик: отправленное назад
// не вернёшь.
func (s *Store) EditTGDraft(id int64, text string) error {
	res, err := s.db.Exec(`UPDATE tg_outbox SET text = ? WHERE id = ? AND state = 'waiting'`, text, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("черновик %d уже решён — править нечего", id)
	}
	s.touch(ScreenChats)
	return nil
}

func (s *Store) DecideTGOutbox(id int64, state, note string) error {
	_, err := s.db.Exec(`UPDATE tg_outbox SET state = ?, note = ? WHERE id = ? AND state = 'waiting'`, state, note, id)
	return err
}

// TGMessage — сообщение из Telegram. Хранится отдельно от писем: у него
// своя идентичность (учётка, чат и номер сообщения) и свои права.
type TGMessage struct {
	ID      int64
	Account string
	ChatID  int64
	MsgID   int
	Topic   int    // тема группы с топиками; 0 — не тема или «Общий»
	Author  string // кто написал: в группе пишут многие
	Text    string
	Out     bool
	At      string
}

func (s *Store) migrateTGMessages() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS tg_messages (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			account TEXT NOT NULL DEFAULT '',
			chat_id INTEGER NOT NULL,
			msg_id  INTEGER NOT NULL,
			text    TEXT NOT NULL DEFAULT '',
			out     INTEGER NOT NULL DEFAULT 0,
			at      TEXT NOT NULL
		)`,
		`ALTER TABLE tg_messages ADD COLUMN topic INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tg_messages ADD COLUMN author TEXT NOT NULL DEFAULT ''`,
		// уникальность составная: у разных учёток номера чатов свои, и
		// «чат + номер» без учётки терял бы сообщение соседней
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_tg_msg ON tg_messages(account, chat_id, msg_id)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("telegram-сообщения: %w", err)
		}
	}
	return nil
}

// AddTGMessage — сообщение в архив. Второй раз то же сообщение не
// добавляется: у учётки, чата и номера уникальная тройка.
func (s *Store) AddTGMessage(m *TGMessage) (bool, error) {
	out := 0
	if m.Out {
		out = 1
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO tg_messages (account, chat_id, msg_id, topic, author, text, out, at)
		VALUES (?,?,?,?,?,?,?,?)`, m.Account, m.ChatID, m.MsgID, m.Topic, m.Author, m.Text, out, time.Now().Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_, _ = s.db.Exec(`UPDATE tg_chats SET count = count + 1, cursor = ?, updated = ? WHERE account = ? AND id = ?`,
			m.MsgID, time.Now().Format(time.RFC3339), m.Account, m.ChatID)
	}
	return n > 0, nil
}

// SetTGMessageText — заменить текст своего сообщения в архиве. Ответ хозяину
// пишется правками: первое слово ложится в архив сразу, а остаться там
// должен итог. Чужих сообщений не трогает: правка чужого текста —
// отдельный разговор, и архив у неё не спрашивает.
func (s *Store) SetTGMessageText(account string, chatID int64, msgID int, text string) error {
	_, err := s.db.Exec(`UPDATE tg_messages SET text = ? WHERE account = ? AND chat_id = ? AND msg_id = ? AND out = 1`,
		text, account, chatID, msgID)
	return err
}

// TGMessages — последние сообщения чата, свежие первыми. topic > 0 —
// только одна тема группы с топиками: автоответ в теме не должен видеть
// соседние.
func (s *Store) TGMessages(account string, chatID int64, topic, limit int) ([]*TGMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, account, chat_id, msg_id, topic, COALESCE(author,''), text, out, at FROM tg_messages WHERE account = ? AND chat_id = ?`
	args := []any{account, chatID}
	if topic > 0 {
		q += ` AND topic = ?`
		args = append(args, topic)
	}
	args = append(args, limit)
	rows, err := s.db.Query(q+` ORDER BY msg_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*TGMessage
	for rows.Next() {
		m := &TGMessage{}
		var out int
		if err := rows.Scan(&m.ID, &m.Account, &m.ChatID, &m.MsgID, &m.Topic, &m.Author, &m.Text, &out, &m.At); err != nil {
			return nil, err
		}
		m.Out = out == 1
		list = append(list, m)
	}
	return list, rows.Err()
}

// CountTGSentSince — сколько сообщений Сёма отправил в этот чат с
// указанного момента: предохранитель от переписки самой с собой.
// Считаются только автоответы: предел бережёт от переписки двух ботов, а
// не от вашего разговора. Сообщения, отправленные вами и по вашей просьбе,
// раньше тоже попадали в счёт — и Сёма замолкал посреди беседы.
func (s *Store) CountTGSentSince(account string, chatID int64, topic int, since string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tg_outbox
		WHERE account = ? AND chat_id = ? AND topic = ? AND state = 'sent' AND created >= ? AND note LIKE 'автоответ%'`,
		account, chatID, topic, since).Scan(&n)
	return n
}

// TGWaiting — сколько сообщений собеседника ждут ответа и когда пришло
// первое из них. Считается от последнего исходящего: ответили — счёт
// обнулился. Нужно семафору «вам не ответили».
// В группе с топиками счёт идёт по теме: ответ в одной теме не значит,
// что ответили в другой.
func (s *Store) TGWaiting(account string, chatID int64, topic int) (count int, since time.Time, last string) {
	where, args := "", []any{account, chatID}
	if topic > 0 {
		where = " AND topic = ?"
		args = append(args, topic)
	}
	var lastOut string
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(at), '') FROM tg_messages WHERE account = ? AND chat_id = ? AND out = 1`+where,
		args...).Scan(&lastOut)
	rows, err := s.db.Query(`SELECT at, text FROM tg_messages
		WHERE account = ? AND chat_id = ? AND out = 0`+where+` AND at > ? ORDER BY at`, append(args, lastOut)...)
	if err != nil {
		return 0, time.Time{}, ""
	}
	defer rows.Close()
	for rows.Next() {
		var at, text string
		if rows.Scan(&at, &text) != nil {
			continue
		}
		if count == 0 {
			since = parseTime(at)
		}
		count++
		if strings.TrimSpace(text) != "" {
			last = text
		}
	}
	return count, since, last
}

// CountTGDrafts — сколько предложенных сообщений ждут кнопки в этом чате
// Сёмы. По ним над полем ввода видно, что разговор чего-то ждёт.
// SupersedeTGDrafts — остальные ждущие черновики разговора закрываются,
// когда один из них ушёл.
func (s *Store) SupersedeTGDrafts(semaChat, exceptID int64, note string) error {
	if semaChat == 0 {
		return nil
	}
	_, err := s.db.Exec(`UPDATE tg_outbox SET state = 'rejected', note = ? WHERE sema_chat = ? AND id <> ? AND state = 'waiting'`, note, semaChat, exceptID)
	return err
}

// TGDraftCounts — черновики, ждущие кнопки, по всем чатам одним запросом:
// меню и список чатов спрашивали их по запросу на чат на каждом экране
// (perf-pass 3.4). Чата без черновиков в словаре нет.
func (s *Store) TGDraftCounts() map[int64]int {
	out := map[int64]int{}
	rows, err := s.db.Query(`SELECT sema_chat, COUNT(*) FROM tg_outbox WHERE state = 'waiting' GROUP BY sema_chat`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var chat int64
		var n int
		if rows.Scan(&chat, &n) == nil {
			out[chat] = n
		}
	}
	return out
}

func (s *Store) CountTGDrafts(semaChat int64) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tg_outbox WHERE sema_chat = ? AND state = 'waiting'`,
		semaChat).Scan(&n)
	return n
}
