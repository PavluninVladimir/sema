package store

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// Каждое место записи, которое меняет экран, говорит об этом: новое место
// без touch рядом с Exec — экран, который перестал обновляться молча.
func TestEveryWriteSignals(t *testing.T) {
	s := testStore(t)
	var got []string
	s.OnChange(func(screens ...string) { got = append(got, screens...) })
	defer s.OnChange(nil)
	expect := func(what string, want ...string) {
		t.Helper()
		sort.Strings(got)
		for _, w := range want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: нет сигнала %q, были %v", what, w, got)
			}
		}
		got = nil
	}
	chat, err := s.CreateChat("проба")
	if err != nil {
		t.Fatal(err)
	}
	expect("CreateChat", ScreenChats)
	_ = s.AddChatMsg(chat, "user", "привет")
	expect("AddChatMsg", ScreenChat(chat), ScreenChats)
	_ = s.RenameChat(chat, "новое")
	expect("RenameChat", ScreenChats, ScreenChat(chat))
	id, _ := s.PutNotification(&Notification{Key: "к", Source: "core", Level: "info", Title: "Проба"})
	expect("PutNotification", ScreenNotifications)
	_ = s.SetNotificationState(id, NotifyDone, "проба")
	expect("SetNotificationState", ScreenNotifications)
	s.CloseNotificationByKey("нет-такого", NotifyDone, "проба")
	if len(got) != 0 {
		t.Errorf("закрытие ничего не закрыло, а сигнал ушёл: %v", got)
	}
	_ = s.InsertMessage(&Message{Account: "ящик", MessageID: "<m@x>", Subject: "п", Date: time.Now(), Status: StatusInboxOK})
	expect("InsertMessage", ScreenInbox, ScreenMailbox("ящик"))
	task := &Task{Title: "з", Kind: "prompt", Schedule: "каждый день 09:00"}
	_ = s.CreateTask(task)
	expect("CreateTask", ScreenTasks)
	_ = s.TaskRan(task.ID, "ок")
	expect("TaskRan", ScreenTasks)
	_ = s.Enqueue("проба", "1", "", 0)
	expect("Enqueue", ScreenQueue)
	_ = s.DeleteChat(chat)
	expect("DeleteChat", ScreenChats)
	if strings.TrimSpace(ScreenChat(7)) != "chat:7" {
		t.Fatal("имя экрана чата")
	}
}
