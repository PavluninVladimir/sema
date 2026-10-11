package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// ---- Чаты ----

type Chat struct {
	ID      int64
	Title   string
	LastMsg string    // для списка чатов
	LastAt  time.Time // когда пришло последнее сообщение: по нему порядок
	Model   string    // своя модель чата; пусто — модель роли «Чаты»
	Effort  string    // уровень размышления: быстро | обычно | подробно
	// три уровня: контейнер (группа с топиками, канал) без ленты →
	// разговор с людьми → тред с Сёмой. Тред — это тот же чат с родителем
	Parent int64  // чат-родитель; 0 у корневых
	Kind   string // "" | dm | group | channel | topic | post | sema | msg
	Anchor int64  // сообщение-якорь у треда по сообщению
	Topic  int64  // номер топика или поста в мессенджере
}

// Виды чатов.
const (
	ChatKindGroup   = "group"   // группа с топиками — контейнер без ленты
	ChatKindChannel = "channel" // канал — одна лента постов и комментариев, как в Telegram
	ChatKindTopic   = "topic"   // топик группы — разговор под контейнером
	ChatKindPost    = "post"    // пост канала отдельным разговором — снято, остаётся для переезда
	ChatKindSema    = "sema"    // тред «Сёма» — ветка Сёмы под разговором, один на разговор
	ChatKindMsg     = "msg"     // тред по сообщению
)

// IsThread — ветка с Сёмой под другим чатом.
func (c *Chat) IsThread() bool { return c.Kind == ChatKindSema || c.Kind == ChatKindMsg }

// IsContainer — группа с топиками: своей ленты нет, только список
// разговоров под ней. Канал контейнером больше не бывает — у него лента.
func (c *Chat) IsContainer() bool { return c.Kind == ChatKindGroup }

// CreateConversation — разговор под контейнером: топик группы или пост
// канала. Модель и усилие — от контейнера. Второй разговор на ту же тему
// не заводится: слушатель Telegram и разбор истории идут разом, а
// разошедшиеся ленты потом не свести (уникальный ключ в базе).
func (s *Store) CreateConversation(parent int64, kind string, topic int64, title string) (int64, error) {
	var model, effort string
	_ = s.db.QueryRow(`SELECT COALESCE(model,''), COALESCE(effort,'') FROM chats WHERE id = ?`, parent).Scan(&model, &effort)
	res, err := s.db.Exec(`INSERT INTO chats (title, model, effort, parent_id, kind, topic_id) VALUES (?, ?, ?, ?, ?, ?)`,
		title, model, effort, parent, kind, topic)
	if err != nil {
		if id, ok := s.ConversationByTopic(parent, topic); ok {
			return id, nil // успел другой: разговор уже есть
		}
		return 0, err
	}
	id, err := res.LastInsertId()
	if err == nil {
		s.touch(ScreenChats, ScreenChat(parent))
	}
	return id, err
}

// ConversationByTopic — разговор контейнера по номеру темы или поста.
func (s *Store) ConversationByTopic(parent int64, topic int64) (int64, bool) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM chats WHERE parent_id = ? AND topic_id = ? AND kind IN (?, ?) ORDER BY id LIMIT 1`,
		parent, topic, ChatKindTopic, ChatKindPost).Scan(&id)
	return id, err == nil
}

// MoveChatMsgs — перенести переписку из одного чата в другой. Нужно, когда
// у группы появились топики: прежняя общая лента не должна пропасть с
// экрана, она уезжает в разговор «Общего».
func (s *Store) MoveChatMsgs(from, to int64) (int64, error) {
	res, err := s.db.Exec(`UPDATE chat_msgs SET chat_id = ? WHERE chat_id = ?`, to, from)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.touch(ScreenChats, ScreenChat(from), ScreenChat(to))
	}
	return n, nil
}

// SetChatKind — вид чата: личный чат становится контейнером, когда у
// группы включают топики.
func (s *Store) SetChatKind(id int64, kind string) error {
	_, err := s.db.Exec(`UPDATE chats SET kind = ? WHERE id = ?`, kind, id)
	if err == nil {
		s.touch(ScreenChats, ScreenChat(id))
	}
	return err
}

// CreateThread — тред под чатом: модель и усилие наследуются от родителя.
func (s *Store) CreateThread(parent int64, kind string, anchor int64, title string) (int64, error) {
	var model, effort string
	_ = s.db.QueryRow(`SELECT COALESCE(model,''), COALESCE(effort,'') FROM chats WHERE id = ?`, parent).Scan(&model, &effort)
	res, err := s.db.Exec(`INSERT INTO chats (title, model, effort, parent_id, kind, anchor_msg_id) VALUES (?, ?, ?, ?, ?, ?)`,
		title, model, effort, parent, kind, anchor)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err == nil {
		s.touch(ScreenChats, ScreenChat(parent))
	}
	return id, err
}

// ThreadOf — тред данного вида под чатом (для «Сёма» — единственный),
// а при anchor > 0 — тред по этому сообщению.
func (s *Store) ThreadOf(parent int64, kind string, anchor int64) (int64, bool) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM chats WHERE parent_id = ? AND kind = ? AND anchor_msg_id = ? ORDER BY id LIMIT 1`,
		parent, kind, anchor).Scan(&id)
	return id, err == nil
}

// ChatChildren — дети чата: разговоры контейнера или треды разговора.
func (s *Store) ChatChildren(parent int64) ([]*Chat, error) {
	all, err := s.Chats()
	if err != nil {
		return nil, err
	}
	var out []*Chat
	for _, c := range all {
		if c.Parent == parent {
			out = append(out, c)
		}
	}
	return out, nil
}

// SetChatModel и SetChatEffort — настройки конкретного чата.
func (s *Store) SetChatModel(id int64, model string) error {
	_, err := s.db.Exec(`UPDATE chats SET model = ? WHERE id = ?`, model, id)
	return err
}

func (s *Store) SetChatEffort(id int64, effort string) error {
	_, err := s.db.Exec(`UPDATE chats SET effort = ? WHERE id = ?`, effort, id)
	return err
}

type ChatMsg struct {
	ID      int64
	ChatID  int64
	Role    string // user | assistant
	Author  string // кто написал реплику собеседника: имя в группе или топике
	Content string
	At      time.Time
}

func (s *Store) CreateChat(title string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO chats (title) VALUES (?)`, title)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err == nil {
		s.touch(ScreenChats)
	}
	return id, err
}

func (s *Store) RenameChat(id int64, title string) error {
	_, err := s.db.Exec(`UPDATE chats SET title = ? WHERE id = ?`, title, id)
	if err == nil {
		s.touch(ScreenChats, ScreenChat(id))
	}
	return err
}

func (s *Store) DeleteChat(id int64) error {
	// дети уходят вместе с родителем: тред без разговора и разговор без
	// контейнера — сироты, которых никто не найдёт
	if kids, err := s.ChatChildren(id); err == nil {
		for _, k := range kids {
			if err := s.DeleteChat(k.ID); err != nil {
				return err
			}
		}
	}
	if _, err := s.db.Exec(`DELETE FROM chat_msgs WHERE chat_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM chats WHERE id = ?`, id)
	if err == nil {
		s.touch(ScreenChats, ScreenChat(id))
	}
	return err
}

func (s *Store) Chats() ([]*Chat, error) {
	// порядок — по последнему сообщению, а не по заведению чата: живая
	// переписка должна стоять сверху, даже если чат старый
	rows, err := s.db.Query(`
SELECT c.id, c.title,
       COALESCE((SELECT content FROM chat_msgs m WHERE m.chat_id = c.id ORDER BY m.id DESC LIMIT 1), ''),
       COALESCE((SELECT created_at FROM chat_msgs m WHERE m.chat_id = c.id ORDER BY m.id DESC LIMIT 1), ''),
       COALESCE(c.parent_id, 0), COALESCE(c.kind, ''), COALESCE(c.anchor_msg_id, 0), COALESCE(c.topic_id, 0)
FROM chats c ORDER BY 4 DESC, c.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Chat
	for rows.Next() {
		c := &Chat{}
		var at string
		if err := rows.Scan(&c.ID, &c.Title, &c.LastMsg, &at, &c.Parent, &c.Kind, &c.Anchor, &c.Topic); err != nil {
			return nil, err
		}
		c.LastAt = parseTime(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Chat(id int64) (*Chat, error) {
	c := &Chat{}
	err := s.db.QueryRow(`SELECT id, title, COALESCE(model,''), COALESCE(effort,''), COALESCE(parent_id,0), COALESCE(kind,''), COALESCE(anchor_msg_id,0), COALESCE(topic_id,0) FROM chats WHERE id = ?`,
		id).Scan(&c.ID, &c.Title, &c.Model, &c.Effort, &c.Parent, &c.Kind, &c.Anchor, &c.Topic)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) AddChatMsg(chatID int64, role, content string) error {
	return s.AddChatMsgFrom(chatID, role, "", content)
}

// AddChatMsgFrom — то же с автором: в группе и топике пишут разные люди,
// и «кто это сказал» — часть сообщения, а не догадка по чату.
func (s *Store) AddChatMsgFrom(chatID int64, role, author, content string) error {
	_, err := s.db.Exec(`INSERT INTO chat_msgs (chat_id, role, author, content) VALUES (?, ?, ?, ?)`,
		chatID, role, author, content)
	if err == nil {
		s.touch(ScreenChat(chatID), ScreenChats)
	}
	return err
}

// AddChatMsgFromAt — то же с временем самого сообщения: история из
// мессенджера ложится с датой, когда сообщение написали, а не когда Сёма
// его прочитал, — иначе у тридцати постов канала одно и то же время.
func (s *Store) AddChatMsgFromAt(chatID int64, role, author, content string, at time.Time) error {
	return s.AddChatMsgExt(chatID, role, author, content, at, "")
}

// AddChatMsgExt — то же с адресом исходного сообщения в мессенджере
// (ext_ref): по нему реплика уходит из ленты, когда её удалили там.
// Пустое время — сейчас.
func (s *Store) AddChatMsgExt(chatID int64, role, author, content string, at time.Time, ref string) error {
	var err error
	if at.IsZero() {
		_, err = s.db.Exec(`INSERT INTO chat_msgs (chat_id, role, author, content, ext_ref) VALUES (?, ?, ?, ?, ?)`,
			chatID, role, author, content, ref)
	} else {
		_, err = s.db.Exec(`INSERT INTO chat_msgs (chat_id, role, author, content, created_at, ext_ref) VALUES (?, ?, ?, ?, ?, ?)`,
			chatID, role, author, content, at.UTC().Format("2006-01-02 15:04:05"), ref)
	}
	if err == nil {
		s.touch(ScreenChat(chatID), ScreenChats)
	}
	return err
}

// SetChatMsgRef — привязать уже лежащую реплику к сообщению мессенджера.
func (s *Store) SetChatMsgRef(id int64, ref string) error {
	_, err := s.db.Exec(`UPDATE chat_msgs SET ext_ref = ? WHERE id = ?`, ref, id)
	return err
}

// ChatMsgsWithoutRef — реплики чата, которые ещё не знают своего адреса в
// мессенджере: для разовой привязки лент, собранных раньше.
func (s *Store) ChatMsgsWithoutRef(chatID int64) ([]*ChatMsg, error) {
	rows, err := s.db.Query(`SELECT id, chat_id, role, COALESCE(author,''), content, created_at FROM chat_msgs
		WHERE chat_id = ? AND ext_ref = '' AND role IN ('peer', 'user') ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ChatMsg
	for rows.Next() {
		m := &ChatMsg{}
		var at string
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Author, &m.Content, &at); err != nil {
			return nil, err
		}
		m.At = parseTime(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteChatMsgsByRef — сообщение удалили в мессенджере: реплика уходит из
// ленты. Если от неё открыта ветка с Сёмой, текст заменяется пометкой —
// ветка не должна остаться без своего сообщения. Возвращает, сколько
// реплик убрано или помечено.
func (s *Store) DeleteChatMsgsByRef(refs []string, note string) (int, error) {
	n := 0
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		rows, err := s.db.Query(`SELECT id, chat_id FROM chat_msgs WHERE ext_ref = ?`, ref)
		if err != nil {
			return n, err
		}
		type replyRow struct{ id, chat int64 }
		var list []replyRow
		for rows.Next() {
			var r replyRow
			if err := rows.Scan(&r.id, &r.chat); err == nil {
				list = append(list, r)
			}
		}
		rows.Close()
		for _, r := range list {
			if s.HasThreadFrom(r.id) {
				_, err = s.db.Exec(`UPDATE chat_msgs SET content = ?, ext_ref = '' WHERE id = ?`, note, r.id)
			} else {
				_, err = s.db.Exec(`DELETE FROM chat_msgs WHERE id = ?`, r.id)
			}
			if err != nil {
				return n, err
			}
			n++
			s.touch(ScreenChat(r.chat), ScreenChats)
		}
	}
	return n, nil
}

// DeleteChatMsg — убрать одно сообщение переписки.
func (s *Store) DeleteChatMsg(id int64) error {
	var chatID int64
	_ = s.db.QueryRow(`SELECT chat_id FROM chat_msgs WHERE id = ?`, id).Scan(&chatID)
	_, err := s.db.Exec(`DELETE FROM chat_msgs WHERE id = ?`, id)
	if err == nil && chatID > 0 {
		s.touch(ScreenChat(chatID), ScreenChats)
	}
	return err
}

// HasThreadFrom — открыта ли от сообщения ветка.
func (s *Store) HasThreadFrom(msgID int64) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM chats WHERE anchor_msg_id = ?`, msgID).Scan(&n)
	return n > 0
}

// SetChatParent — перевесить чат под другого родителя: ветки разговора,
// которого больше нет, переезжают к тому, что его заменил.
func (s *Store) SetChatParent(id, parent int64) error {
	_, err := s.db.Exec(`UPDATE chats SET parent_id = ? WHERE id = ?`, parent, id)
	if err == nil {
		s.touch(ScreenChats, ScreenChat(id), ScreenChat(parent))
	}
	return err
}

// ReplaceChatMsg — переписать содержимое сообщения. Нужно карточкам: у
// одной команды одна карточка, которая меняет состояние, а не три штуки
// «предложено», «выполнено», «код 0» подряд.
// ChatMsg — одно сообщение переписки по номеру: карточку правят на месте,
// а не переписывают целиком.
func (s *Store) ChatMsg(id int64) (*ChatMsg, error) {
	m := &ChatMsg{}
	var at string
	err := s.db.QueryRow(`SELECT id, chat_id, role, COALESCE(author,''), content, created_at FROM chat_msgs WHERE id = ?`, id).
		Scan(&m.ID, &m.ChatID, &m.Role, &m.Author, &m.Content, &at)
	if err != nil {
		return nil, err
	}
	m.At = parseTime(at)
	return m, nil
}

func (s *Store) ReplaceChatMsg(id int64, content string) error {
	var chatID int64
	_ = s.db.QueryRow(`SELECT chat_id FROM chat_msgs WHERE id = ?`, id).Scan(&chatID)
	_, err := s.db.Exec(`UPDATE chat_msgs SET content = ? WHERE id = ?`, content, id)
	if err == nil && chatID > 0 {
		s.touch(ScreenChat(chatID))
	}
	return err
}

// SetChatMsgRole — поправить роль и текст сообщения. Нужно разовой
// починке старых реплик собеседника: имя жило внутри текста.
func (s *Store) SetChatMsgRole(id int64, role, content string) error {
	_, err := s.db.Exec(`UPDATE chat_msgs SET role = ?, content = ? WHERE id = ?`, role, content, id)
	return err
}

// sqlChatMsgs — лента чата: хвост переписки, свежие сверху. Индекс
// idx_chat_msgs_chat(chat_id, id DESC) держит её без скана таблицы.
const sqlChatMsgs = `
SELECT id, chat_id, role, COALESCE(author,''), content, created_at FROM chat_msgs
WHERE chat_id = ? ORDER BY id DESC LIMIT ?`

func (s *Store) ChatMsgs(chatID int64, limit int) ([]*ChatMsg, error) {
	rows, err := s.db.Query(sqlChatMsgs, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ChatMsg
	for rows.Next() {
		m := &ChatMsg{}
		var at string
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Author, &m.Content, &at); err != nil {
			return nil, err
		}
		m.At = parseTime(at)
		out = append(out, m)
	}
	// разворачиваем в хронологический порядок
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// ---- Задачи по расписанию ----

type Task struct {
	ID         int64
	Title      string
	Prompt     string
	Kind       string // prompt | poll
	System     string // непустое — встроенный процесс (archive, receipts…)
	Schedule   string // «каждые 5 сек», «9:00», «после:<id>»
	AfterID    int64  // запускать после этой задачи
	Root       bool   // сердцебиение: не меняется и не выключается
	Sort       int
	ScriptID   int64
	Enabled    bool
	Once       bool
	ChatID     int64
	LastRun    time.Time
	LastResult string
	// Origin и Ref — откуда задача взялась и её внешний номер. Пусто —
	// поставил человек внутри Сёмы. Поля остались от трекеров задач (сняты
	// 30 сентября 2026): сейчас задачу с происхождением никто не заводит.
	Origin string
	Ref    string
	// SchedBy — кто поставил нынешнее расписание. Пусто — манифест
	// модуля или умолчание, «человек» — его поменяли руками, и манифест
	// его больше не перебивает.
	SchedBy string
}

func (s *Store) CreateTask(t *Task) error {
	res, err := s.db.Exec(`
INSERT INTO tasks (title, prompt, kind, schedule, enabled, once, chat_id, system, after_id, root, sort, script_id, origin, ref)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Title, t.Prompt, t.Kind, t.Schedule, boolInt(t.Enabled), boolInt(t.Once), t.ChatID,
		t.System, t.AfterID, boolInt(t.Root), t.Sort, t.ScriptID, t.Origin, t.Ref)
	if err != nil {
		return err
	}
	t.ID, _ = res.LastInsertId()
	s.touch(ScreenTasks)
	return nil
}

func (s *Store) Tasks() ([]*Task, error) {
	rows, err := s.db.Query(`
SELECT id, title, prompt, kind, schedule, enabled, once, chat_id, last_run, last_result,
       system, after_id, root, sort, script_id, origin, ref, sched_by
FROM tasks ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t := &Task{}
		var enabled, once int
		var lastRun string
		var root int
		if err := rows.Scan(&t.ID, &t.Title, &t.Prompt, &t.Kind, &t.Schedule,
			&enabled, &once, &t.ChatID, &lastRun, &t.LastResult,
			&t.System, &t.AfterID, &root, &t.Sort, &t.ScriptID, &t.Origin, &t.Ref, &t.SchedBy); err != nil {
			return nil, err
		}
		t.Enabled, t.Once, t.Root = enabled == 1, once == 1, root == 1
		if lastRun != "" {
			t.LastRun = parseTime(lastRun)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Task(id int64) (*Task, error) {
	t := &Task{}
	var enabled, once int
	var lastRun string
	var root int
	err := s.db.QueryRow(`
SELECT id, title, prompt, kind, schedule, enabled, once, chat_id, last_run, last_result,
       system, after_id, root, sort, script_id, origin, ref, sched_by
FROM tasks WHERE id = ?`, id).Scan(&t.ID, &t.Title, &t.Prompt, &t.Kind, &t.Schedule,
		&enabled, &once, &t.ChatID, &lastRun, &t.LastResult,
		&t.System, &t.AfterID, &root, &t.Sort, &t.ScriptID, &t.Origin, &t.Ref, &t.SchedBy)
	if err != nil {
		return nil, err
	}
	t.Enabled, t.Once, t.Root = enabled == 1, once == 1, root == 1
	if lastRun != "" {
		t.LastRun = parseTime(lastRun)
	}
	return t, nil
}

func (s *Store) SetTaskEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE tasks SET enabled = ? WHERE id = ?`, boolInt(enabled), id)
	if err == nil {
		s.touch(ScreenTasks)
	}
	return err
}

func (s *Store) SetTaskChat(id, chatID int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET chat_id = ? WHERE id = ?`, chatID, id)
	return err
}

// TasksByScript — задачи, которые запускают этот скрипт: только номер и
// название, этого хватает, чтобы назвать их человеку. По ним видно, можно
// ли удалять скрипт и что уйдёт вместе с задачей.
func (s *Store) TasksByScript(scriptID int64) ([]*Task, error) {
	if scriptID == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id, title FROM tasks WHERE script_id = ? ORDER BY id`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t := &Task{ScriptID: scriptID}
		if err := rows.Scan(&t.ID, &t.Title); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTask(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	if err == nil {
		s.touch(ScreenTasks)
	}
	return err
}

func (s *Store) TaskRan(id int64, result string) error {
	if r := []rune(result); len(r) > 4000 {
		result = string(r[:4000]) // по рунам: кириллица иначе режется вдвое раньше
	}
	_, err := s.db.Exec(`UPDATE tasks SET last_run = ?, last_result = ? WHERE id = ?`,
		fmtTime(time.Now()), result, id)
	if err == nil {
		s.touch(ScreenTasks)
	}
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- Дополнительные почтовые ящики (добавленные из UI) ----

type Account struct {
	Name     string `json:"name"`  // отображаемое имя = email
	Email    string `json:"email"` // логин IMAP
	Password string `json:"password"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

const accountsKey = "accounts.json"

// AccountSecretName — под каким именем пароль ящика лежит в хранилище.
func AccountSecretName(email string) string { return "mail:" + email }

// Accounts — ящики, заведённые из интерфейса. Пароль берётся из
// хранилища секретов; в самой настройке его нет.
func (s *Store) Accounts() ([]*Account, error) {
	raw := s.Setting(accountsKey, "[]")
	var out []*Account
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	for _, a := range out {
		if a.Password == "" {
			if v, ok := s.SecretValue(AccountSecretName(a.Email)); ok {
				a.Password = v
			}
		}
	}
	return out, nil
}

// SaveAccounts — ящики в настройку, пароли в хранилище секретов.
//
// До 11 сентября 2026 пароль ящика лежал в `settings` строкой, как его
// набрали: настройку видно в любой выгрузке базы и в любой копии, а
// правило проекта — шифровать всё ключом из SEMA_KEY. Рядом, в
// `mailconf.json`, ключ Google всё это время лежал правильно
// (`enc:v1:`) — то есть ящик, добавленный из интерфейса, шёл другой
// дорогой, где шифрования не было.
//
// Если хранилище пароль не приняло (нет ключа шифрования), настройку не
// трогаем вовсе: лучше оставить как было, чем потерять пароль от почты.
func (s *Store) SaveAccounts(list []*Account) error {
	clean := make([]*Account, 0, len(list))
	for _, a := range list {
		dup := *a
		if dup.Password != "" {
			err := s.SaveSecret(&Secret{
				Name: AccountSecretName(dup.Email), Kind: "пароль",
				Note: "пароль приложения для ящика " + dup.Email,
			}, dup.Password)
			if err != nil {
				return fmt.Errorf("пароль ящика %s в хранилище: %w", dup.Email, err)
			}
			dup.Password = ""
		}
		clean = append(clean, &dup)
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return s.SetSetting(accountsKey, string(raw))
}

// MigrateAccountPasswords — разовый переезд уже лежащих открытых паролей
// в хранилище секретов. Возвращает, сколько перенёс.
func (s *Store) MigrateAccountPasswords() (int, error) {
	raw := s.Setting(accountsKey, "[]")
	var list []*Account
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return 0, err
	}
	need := 0
	for _, a := range list {
		if a.Password != "" {
			need++
		}
	}
	if need == 0 {
		return 0, nil
	}
	// SaveAccounts сам положит пароли в хранилище и вычистит настройку;
	// если хранилище откажет, настройка останется прежней
	if err := s.SaveAccounts(list); err != nil {
		return 0, err
	}
	return need, nil
}

// ---- Редактирование задач ----

func (s *Store) SetTaskTitle(id int64, title string) error {
	_, err := s.db.Exec(`UPDATE tasks SET title = ? WHERE id = ? AND root = 0`, title, id)
	return err
}

// SetTaskSchedule — расписание задачи. `by` говорит, кто его поставил:
// «человек» защищает расписание от манифеста модуля.
//
// До 10 сентября 2026 расписание задачи модуля было целиком за
// манифестом: человек менял час на экране «Задачи», а следующий запуск
// сервера молча возвращал прежний. Из-за этого «Утренняя проверка
// серверов» упорно оставалась на 8:00, хотя окно самостоятельной работы
// стояло на 02:00–04:00.
func (s *Store) SetTaskSchedule(id int64, schedule string, afterID int64) error {
	return s.setTaskSchedule(id, schedule, afterID, "человек")
}

// SetTaskScheduleFromManifest — расписание из манифеста модуля. Не трогает
// то, что человек поставил руками.
func (s *Store) SetTaskScheduleFromManifest(id int64, schedule string, afterID int64) error {
	var by string
	if err := s.db.QueryRow(`SELECT sched_by FROM tasks WHERE id = ?`, id).Scan(&by); err == nil && by == "человек" {
		return nil
	}
	return s.setTaskSchedule(id, schedule, afterID, "")
}

func (s *Store) setTaskSchedule(id int64, schedule string, afterID int64, by string) error {
	_, err := s.db.Exec(`UPDATE tasks SET schedule = ?, after_id = ?, sched_by = ? WHERE id = ? AND root = 0`,
		schedule, afterID, by, id)
	return err
}

func (s *Store) SetTaskPrompt(id int64, prompt string) error {
	_, err := s.db.Exec(`UPDATE tasks SET prompt = ? WHERE id = ? AND root = 0`, prompt, id)
	return err
}

// EnsureSystemTask заводит встроенную задачу, если её ещё нет.
func (s *Store) EnsureSystemTask(t *Task) error {
	var n int
	key := t.System
	if t.Root {
		key = "root"
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE system = ? OR (root = 1 AND ? = 'root')`,
		t.System, key).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return s.CreateTask(t)
}

// SyncRootTask обновляет текст корневой задачи (её обычные правки запрещены).
func (s *Store) SyncRootTask(title, schedule, prompt string) error {
	_, err := s.db.Exec(`UPDATE tasks SET title = ?, schedule = ?, prompt = ? WHERE root = 1`,
		title, schedule, prompt)
	return err
}
