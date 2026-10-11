package tg

import (
	"testing"

	"github.com/gotd/td/tg"
)

// Тема сообщения: у форума она приходит в заголовке ответа, и перепутать
// её с обычным ответом нельзя — иначе реплика уедет не в тот разговор.
func TestMessageTopic(t *testing.T) {
	header := func(fill func(*tg.MessageReplyHeader)) *tg.Message {
		h := &tg.MessageReplyHeader{}
		fill(h)
		m := &tg.Message{ID: 10}
		m.SetReplyTo(h)
		return m
	}
	cases := []struct {
		name string
		msg  *tg.Message
		want int
	}{
		{"без ответа", &tg.Message{ID: 10}, 0},
		{"обычный ответ", header(func(h *tg.MessageReplyHeader) { h.SetReplyToMsgID(5) }), 0},
		{"тема с корнем", header(func(h *tg.MessageReplyHeader) {
			h.ForumTopic = true
			h.SetReplyToMsgID(5)
			h.SetReplyToTopID(7)
		}), 7},
		{"ответ самому корню темы", header(func(h *tg.MessageReplyHeader) {
			h.ForumTopic = true
			h.SetReplyToMsgID(7)
		}), 7},
	}
	for _, c := range cases {
		if got := topicOf(c.msg); got != c.want {
			t.Errorf("%s: тема %d, ждали %d", c.name, got, c.want)
		}
	}
}

// Имя автора берётся из списка людей того же ответа: без него в группе
// не видно, кто написал.
func TestAuthorFromReply(t *testing.T) {
	m := &tg.Message{ID: 10, Message: "привет"}
	m.SetFromID(&tg.PeerUser{UserID: 42})
	u := updateOf(m, map[int64]string{42: "Митя"})
	if u.From != "Митя" {
		t.Fatalf("автор: %q", u.From)
	}
	if got := updateOf(m, nil).From; got != "" {
		t.Fatalf("автор без списка людей выдуман: %q", got)
	}
}
