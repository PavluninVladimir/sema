package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestWeChatForwardFormAndMenu(t *testing.T) {
	s, st := testServer(t)
	const session = `{"UIN":1,"Username":"synthetic","DeviceID":"MDEyMzQ1Njc4OWFiY2RlZg==","SessionKey":"MDEyMzQ1Njc4OWFiY2RlZg==","ClientVersion":-227271398}`
	ctx := context.Background()
	account, peer := "synthetic", "source@chatroom"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SaveSecret(&store.Secret{Name: "wechat:account:oauth", Kind: store.SecretOAuth}, session))
	must(st.CommitWeChatContacts(ctx, account, nil, []byte{1}, []store.WeChatContact{
		{Peer: peer, Title: "Группа WeChat"}, {Peer: "other", Title: "Другая группа"},
	}))
	must(st.SetWeChatCollect(ctx, account, peer, true))
	must(st.SaveTGAccount(&store.TGAccount{Phone: "+1000", Title: "Личный", State: "ok", CanSend: true, Enabled: true}))
	must(st.SaveTGChat(&store.TGChat{Account: "+1000", ID: 123, Title: "Закупки", Kind: "group", Send: true}))
	must(st.SaveTGChat(&store.TGChat{Account: "+1000", ID: 456, Title: "Другой получатель", Kind: "user", Reply: true}))
	must(st.SaveTGChat(&store.TGChat{Account: "+1000", ID: 789, Title: "Чужой канал", Kind: "channel", Send: true}))
	must(st.SaveMAXAccount(&store.MAXAccount{Phone: "+1000", State: "ok", CanSend: true}))
	must(st.SaveMAXChat(&store.MAXChat{ID: 123, Title: "Семья", Kind: "chat", Send: true}))
	target := store.WeChatForwardTarget{Channel: store.WeChatForwardTelegram, Account: "+1000", Chat: 123}
	maxTarget := store.WeChatForwardTarget{Channel: store.WeChatForwardMAX, Account: "+1000", Chat: 123}
	want := []store.WeChatForwardTarget{maxTarget, target}

	screen := wechatChatScreen(peer)
	doc, err := s.buildScreen(screen, "", "", 0)
	must(err)
	menu := s.wechatMenu()
	if menu == nil || len(menu.Children) != 1 || menu.Children[0].ID != screen {
		t.Fatalf("subscribed chat missing from navigation: %+v", menu)
	}
	guest, err := s.buildScreenForPhone(wechatScreen, "", "", 0, false, false, asGuest())
	must(err)
	for _, parent := range guest.Screens {
		if slices.ContainsFunc(parent.Children, func(node Screen) bool { return node.ID == wechatMenuID }) {
			t.Fatal("WeChat subscriptions shown in a guest menu")
		}
	}
	form := docByID(doc, "wechat-forward")
	if form == nil || form.Submit.Action != "wechat_save_forward" || len(form.Fields) != 5 ||
		form.Fields[3].Name != recipientField(target) || form.Fields[4].Name != recipientField(maxTarget) {
		t.Fatalf("forward form unavailable or lists disallowed recipients: %+v", form)
	}
	values := url.Values{
		"screen": {screen}, "action": {"wechat_save_forward"}, "target": {peer},
		"source_language": {"Китайский"}, "target_language": {"Русский"},
		"enabled": {"true"}, recipientField(target): {"true"}, recipientField(maxTarget): {"true"},
		"toggles": {"enabled," + recipientField(target) + "," + recipientField(maxTarget)},
	}
	save := func() {
		req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(values.Encode()))
		req.RemoteAddr = "127.0.0.1:34567"
		req.Host = "127.0.0.1:8765"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		resp := httptest.NewRecorder()
		s.Handler().ServeHTTP(resp, req)
		if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "Пересылка сообщений") {
			t.Fatalf("web form returned %d: %s", resp.Code, resp.Body.String())
		}
	}
	save()
	rule, err := st.WeChatForwardRule(ctx, account, peer)
	must(err)
	if !rule.Enabled || rule.SourceLang != "Китайский" || rule.TargetLang != "Русский" || !slices.Equal(rule.Targets, want) {
		t.Fatalf("unexpected saved rule: %+v", rule)
	}
	values.Del("enabled")
	save()
	rule, err = st.WeChatForwardRule(ctx, account, peer)
	must(err)
	if rule.Enabled || !slices.Equal(rule.Targets, want) {
		t.Fatalf("pause lost the configured recipient: %+v", rule)
	}
}
