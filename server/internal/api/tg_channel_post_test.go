package api

import (
	"strings"
	"testing"
)

// «Писать в канал»: право «Писать первым» у канала — публикация, и оно есть
// только там, где аккаунт владелец или админ с постами; у остального — клетка
// «na» с причиной, а действие отказывает с объяснением.
func TestPostToChannelOnlyWhereGranted(t *testing.T) {
	s, st := telegramWithChannels(t, 2)
	doc, err := s.buildScreen(tgScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	m := tgMatrixOf(t, doc)
	own := tgCell(t, m, "tg-101", "Писать первым")
	if own.State != "off" || own.Action.Action != "tg_chat_right" || !strings.Contains(own.Title, "публиковать в канал") {
		t.Errorf("у канала владельца нет права «публиковать в канал»: %+v", own)
	}
	other := tgCell(t, m, "tg-102", "Писать первым")
	if other.State != "na" || other.Action.Action != "" || !strings.Contains(other.Title, "Публиковать нельзя") {
		t.Errorf("у канала без права нет «Публиковать нельзя»: %+v", other)
	}
	var бейджи []string
	for _, b := range findByID(m.Children, "tg-101").Badges {
		бейджи = append(бейджи, b.Text)
	}
	if !strings.Contains(strings.Join(бейджи, ","), "вы админ") {
		t.Error("у канала владельца нет капсулы «вы админ»")
	}
	if _, err := s.execute(actionRequest{Action: "tg_chat_right", Target: telegramTarget(101) + "~send"}); err != nil {
		t.Errorf("владельцу не дали включить публикацию: %v", err)
	}
	if c, _ := st.TGChat(telegramAccount, 101); !c.Send {
		t.Error("право send у канала владельца не включилось")
	}
	if _, err := s.execute(actionRequest{Action: "tg_chat_right", Target: telegramTarget(102) + "~send"}); err == nil ||
		!strings.Contains(err.Error(), "нет права публиковать") {
		t.Errorf("подписчику разрешили публиковать: %v", err)
	}
}
