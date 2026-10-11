package store

// Переезд с одной учётки Telegram на список. Проверяется не обещание, а
// старая база: таблицы приводятся к прежнему виду руками, и миграция
// гоняется по ним — иначе тест проверял бы только свежую схему, где
// переезжать нечему.

import (
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

const oldPhone = "+79990000000"

// oldTGTables — схема до нескольких учёток: чаты по номеру, темы и
// привязки постов без учётки, сообщения с парой «чат + номер».
func oldTGTables(t *testing.T, st *Store) {
	t.Helper()
	for _, q := range []string{
		`DROP TABLE IF EXISTS tg_chats`,
		`DROP TABLE IF EXISTS tg_topics`,
		`DROP TABLE IF EXISTS tg_post_links`,
		`DROP TABLE IF EXISTS tg_messages`,
		`DROP TABLE IF EXISTS tg_outbox`,
		`DROP INDEX IF EXISTS idx_tg_msg`,
		`CREATE TABLE tg_chats (
			id INTEGER PRIMARY KEY, title TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '',
			collect INTEGER NOT NULL DEFAULT 0, class TEXT NOT NULL DEFAULT '', access INTEGER NOT NULL DEFAULT 0,
			cursor INTEGER NOT NULL DEFAULT 0, count INTEGER NOT NULL DEFAULT 0, updated TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '', reply INTEGER NOT NULL DEFAULT 0, send INTEGER NOT NULL DEFAULT 0,
			files INTEGER NOT NULL DEFAULT 0, blocked INTEGER NOT NULL DEFAULT 0, admin INTEGER NOT NULL DEFAULT 0,
			forum INTEGER NOT NULL DEFAULT 0, can_post INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE tg_topics (
			chat_id INTEGER NOT NULL, topic_id INTEGER NOT NULL, title TEXT NOT NULL DEFAULT '',
			collect INTEGER NOT NULL DEFAULT -1, reply INTEGER NOT NULL DEFAULT -1, send INTEGER NOT NULL DEFAULT -1,
			files INTEGER NOT NULL DEFAULT -1, blocked INTEGER NOT NULL DEFAULT -1, updated TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (chat_id, topic_id))`,
		`CREATE TABLE tg_post_links (
			channel_id INTEGER NOT NULL, post_id INTEGER NOT NULL, group_id INTEGER NOT NULL,
			group_access INTEGER NOT NULL DEFAULT 0, root_id INTEGER NOT NULL, updated TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (channel_id, post_id))`,
		`CREATE TABLE tg_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT, chat_id INTEGER NOT NULL, msg_id INTEGER NOT NULL,
			text TEXT NOT NULL DEFAULT '', out INTEGER NOT NULL DEFAULT 0, at TEXT NOT NULL,
			topic INTEGER NOT NULL DEFAULT 0, author TEXT NOT NULL DEFAULT '')`,
		`CREATE UNIQUE INDEX idx_tg_msg ON tg_messages(chat_id, msg_id)`,
		`CREATE TABLE tg_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT, chat_id INTEGER NOT NULL, sema_chat INTEGER NOT NULL DEFAULT 0,
			who TEXT NOT NULL DEFAULT '', text TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'waiting',
			note TEXT NOT NULL DEFAULT '', created TEXT NOT NULL, topic INTEGER NOT NULL DEFAULT 0)`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("старая схема (%s): %v", q, err)
		}
	}
}

// storeWithKey — база с ключом шифрования: сессия Telegram без него
// не пишется вовсе, а проверять надо именно её сохранность.
func storeWithKey(t *testing.T, phrase string) *Store {
	t.Helper()
	st := testStore(t)
	box, err := secret.New(phrase)
	if err != nil {
		t.Fatal(err)
	}
	st.UseSecretBox(box)
	return st
}

func TestMultiAccountMigrationLosesNothing(t *testing.T) {
	st := storeWithKey(t, "ключ-для-проверки-переезда")
	oldTGTables(t, st)

	// учётка со своей сессией: она лежит в tg_account и переезд её не
	// касается вовсе — но убедиться в этом надо
	if err := st.SaveTGAccount(&TGAccount{Phone: oldPhone, State: "ok", Session: "живая-сессия",
		Every: 7, Batch: 42, CanSend: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339)
	if _, err := st.db.Exec(`INSERT INTO tg_chats (id, title, kind, collect, class, reply, forum, updated)
		VALUES (7, 'Митя', 'user', 1, 'секретное', 1, 0, ?), (9, 'Дом', 'group', 1, '', 0, 1, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO tg_topics (chat_id, topic_id, title, collect) VALUES (9, 3, 'Заявки', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO tg_post_links (channel_id, post_id, group_id, group_access, root_id)
		VALUES (70, 500, 71, 8, 900)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO tg_messages (chat_id, msg_id, text, out, at) VALUES (7, 100, 'привет', 0, ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO tg_outbox (chat_id, text, state, created) VALUES (7, 'черновик', 'waiting', ?)`, now); err != nil {
		t.Fatal(err)
	}
	// адреса старого вида: чат Сёмы, метка истории, «не напоминать»
	for key, value := range map[string]string{
		"tg.chat.7":     "15",
		"tg.history.7":  "30",
		"tg.wait_off.9": "1",
		"tg.quiet.9/3":  "2026-09-01T10:00:00Z",
		"tg.history":    "30", // общий предел: его трогать нельзя
		"tg.help_word":  "сёма",
		"tg.code_hash":  "хэш-кода",
	} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	st.DeleteSettingKey("tg.keys_moved")
	if _, err := st.db.Exec(`INSERT INTO notifications (key, source, title, created, updated)
		VALUES ('tg-wait:9/3', 'core', 'Ждёт ответа', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}

	if err := st.migrateTelegram(); err != nil {
		t.Fatalf("переезд: %v", err)
	}

	// сессия цела и читается тем же ключом
	acc, ok := st.TGAccount(oldPhone)
	if !ok || acc.Session != "живая-сессия" {
		t.Fatalf("сессия не пережила переезд: %+v", acc)
	}
	if acc.Every != 7 || acc.Batch != 42 || !acc.CanSend || !acc.Enabled {
		t.Fatalf("пределы учётки потерялись: %+v", acc)
	}

	// чаты достались нынешней учётке вместе с правами
	chats, err := st.TGChats(oldPhone, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 {
		t.Fatalf("чаты не переехали: %+v", chats)
	}
	c, ok := st.TGChat(oldPhone, 7)
	if !ok || !c.Collect || !c.Reply || c.Class != "секретное" || c.Account != oldPhone {
		t.Fatalf("права чата потерялись: %+v", c)
	}
	if topics, _ := st.TGTopics(oldPhone, 9); len(topics) != 1 || topics[0].Title != "Заявки" || topics[0].Collect != 1 {
		t.Fatalf("темы не переехали: %+v", topics)
	}
	if _, ok := st.TGPostLink(oldPhone, 70, 500); !ok {
		t.Fatal("привязка поста не переехала")
	}
	if _, ok := st.TGPostByRoot(oldPhone, 71, 900); !ok {
		t.Fatal("привязка по корню ветки не переехала")
	}
	msgs, _ := st.TGMessages(oldPhone, 7, 0, 10)
	if len(msgs) != 1 || msgs[0].Text != "привет" || msgs[0].Account != oldPhone {
		t.Fatalf("архив не переехал: %+v", msgs)
	}
	if n := st.CountTGDrafts(0); n != 0 {
		_ = n // черновик без чата Сёмы в счёт не идёт: проверяем ниже прямым чтением
	}
	var outAcc string
	if err := st.db.QueryRow(`SELECT account FROM tg_outbox LIMIT 1`).Scan(&outAcc); err != nil || outAcc != oldPhone {
		t.Fatalf("исходящие не переехали: %q %v", outAcc, err)
	}

	// адреса: чат Сёмы и метки нашли номер учётки, общие пределы остались
	if got := st.Setting("tg.chat."+oldPhone+".7", ""); got != "15" {
		t.Errorf("адрес чата Сёмы не переехал: %q", got)
	}
	if st.Setting("tg.chat.7", "") != "" {
		t.Error("старый адрес чата Сёмы остался — разговор нашёлся бы дважды")
	}
	if got := st.Setting("tg.history."+oldPhone+".7", ""); got != "30" {
		t.Errorf("метка истории не переехала: %q", got)
	}
	if got := st.Setting("tg.history", ""); got != "30" {
		t.Errorf("общий предел «сколько истории» уехал вместе с адресами: %q", got)
	}
	if got := st.Setting("tg.help_word", ""); got != "сёма" {
		t.Errorf("слово-вызов потерялось: %q", got)
	}
	if got := st.Setting("tg.wait_off."+oldPhone+".9", ""); got != "1" {
		t.Errorf("«не напоминать» не переехало: %q", got)
	}
	// у «замолчал» разделитель свой — двоеточие, как в tgRef: ключ,
	// собранный иначе, это просто другой ключ
	if got := st.Setting("tg.quiet."+oldPhone+":9/3", ""); got == "" {
		t.Error("метка «Сёма замолчал» не переехала")
	}
	if got := st.Setting("tg.code_hash."+oldPhone, ""); got != "хэш-кода" {
		t.Errorf("хэш кода входа не переехал: %q", got)
	}

	// ключ уведомления получил номер учётки: иначе открытая запись висела
	// бы вечно — закрыть её новым ключом уже нечем
	var key string
	if err := st.db.QueryRow(`SELECT key FROM notifications WHERE key LIKE 'tg-wait:%'`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "tg-wait:"+oldPhone+":9/3" {
		t.Errorf("ключ уведомления не переехал: %q", key)
	}

	// повтор ничего не ломает: миграция идёт на каждом старте
	if err := st.migrateTelegram(); err != nil {
		t.Fatalf("повторный переезд: %v", err)
	}
	if c, ok := st.TGChat(oldPhone, 7); !ok || !c.Collect {
		t.Fatalf("повторный переезд потерял чат: %+v", c)
	}

	// и с этого момента пара «учётка + номер» — это ключ: тот же номер в
	// соседней учётке живёт своей жизнью
	if err := st.SaveTGChat(&TGChat{Account: "+70000000002", ID: 7, Title: "Рабочий", Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.TGChat(oldPhone, 7); c.Title != "Митя" {
		t.Fatalf("чат соседней учётки затёр личный: %+v", c)
	}
}

// Сессию, которую нынешний ключ не читает, перезаписывать нельзя: сервер
// с чужим SEMA_KEY иначе молча стёр бы вход.
func TestForeignKeyNeverWipesTelegramSession(t *testing.T) {
	st := storeWithKey(t, "настоящий-ключ-достаточной-длины")
	if err := st.SaveTGAccount(&TGAccount{Phone: oldPhone, State: "ok", Session: "живая-сессия"}); err != nil {
		t.Fatal(err)
	}
	// тот же приём, что у секретов: подменяем ключ и пробуем записать
	foreign, err := secret.New("чужой-ключ-достаточной-длины")
	if err != nil {
		t.Fatal(err)
	}
	prev := st.box
	st.box = foreign
	errSave := st.SaveTGAccount(&TGAccount{Phone: oldPhone, State: "ok", Session: "чужая-сессия"})
	st.box = prev
	if errSave == nil {
		t.Fatal("чужой ключ перезаписал сессию Telegram")
	}
	acc, ok := st.TGAccount(oldPhone)
	if !ok || acc.Session != "живая-сессия" {
		t.Fatalf("сессия испортилась: %+v", acc)
	}
}
