package store

// Сигнал «экран устарел». Хранилище — единственное место, где меняется
// то, что показывают экраны, поэтому и сигнал идёт отсюда: одна точка
// вместо десятка в движке, и новое место записи не забудет сказать о
// себе, если зовёт touch рядом с Exec.
//
// Кто слушает и что делает с сигналом — не дело хранилища: оно зовёт
// переданную функцию и не ждёт её.

import (
	"strconv"
	"sync"
)

var (
	changeMu sync.RWMutex
	onChange func(screens ...string)
)

// OnChange — кому сообщать об изменениях. Один слушатель на процесс:
// движок один, а в проверках хранилищ бывает несколько, и последний
// заявившийся перекрывает предыдущего.
func (s *Store) OnChange(fn func(screens ...string)) {
	changeMu.Lock()
	onChange = fn
	changeMu.Unlock()
}

// touch — экраны изменились. Слушателя нет — тишина: хранилище работает
// и без движка (копии, осмотр, проверки).
func (s *Store) touch(screens ...string) {
	changeMu.RLock()
	fn := onChange
	changeMu.RUnlock()
	if fn != nil {
		fn(screens...)
	}
}

// Имена экранов, которые меняет хранилище. Совпадают с адресами экранов
// клиента: сигнал говорит, что перечитать, а не что случилось.
const (
	ScreenNotifications = "notifications"
	ScreenInbox         = "inbox"
	ScreenTasks         = "tasks"
	ScreenChats         = "chats"
	ScreenQueue         = "queue"
	ScreenFiles         = "files"
	// ScreenIntegrations — «Интеграции»: вход на сервер MCP идёт в другой
	// вкладке браузера, и первая должна увидеть итог сама
	ScreenIntegrations = "settings:integrations"
	// ScreenServers — «Серверы»: правила и команды меняются и из чата
	ScreenServers = "settings:servers"
)

// ScreenChat — экран переписки по номеру чата.
func ScreenChat(id int64) string { return "chat:" + strconv.FormatInt(id, 10) }

// ScreenMailbox — «Почта» одного ящика.
func ScreenMailbox(account string) string { return ScreenInbox + "@" + account }
