package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestWeChatSettingsAndQRPermissions(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen(wechatScreen, "", "", 0)
	if err != nil || doc.Heading != "WeChat" {
		t.Fatalf("settings unavailable: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, wechatQRPath, nil)
	req.RemoteAddr = "203.0.113.12:12345"
	reply := httptest.NewRecorder()
	s.handleWeChatQR(reply, req)
	if reply.Code != http.StatusForbidden {
		t.Fatalf("QR available to unauthenticated request: %d", reply.Code)
	}
	reply = httptest.NewRecorder()
	if s.allowScreen(reply, req, wechatScreen+"@test") || reply.Code != http.StatusForbidden {
		t.Fatalf("private screen accepted unauthenticated request: %d", reply.Code)
	}
	for _, action := range []string{"wechat_send_text", "wechat_send_file", "wechat_send_image"} {
		reply = httptest.NewRecorder()
		if s.allowAction(reply, req, actionRequest{Screen: "chats", Action: action}) || reply.Code != http.StatusForbidden {
			t.Fatalf("action %q available to unauthenticated request: %d", action, reply.Code)
		}
	}
	offline := &Document{offline: true}
	if err := s.buildWeChat(offline, wechatScreen+"@test", "", "", 0); err != nil || len(offline.Components) != 0 {
		t.Fatalf("settings index got WeChat content: %v", err)
	}
}

func TestWeChatSubscriptionsAndChatSettings(t *testing.T) {
	s, st := testServer(t)
	account := "synthetic"
	session := `{"UIN":1,"Username":"synthetic","DeviceID":"MDEyMzQ1Njc4OWFiY2RlZg==","SessionKey":"MDEyMzQ1Njc4OWFiY2RlZg==","ClientVersion":-227271398}`
	if err := st.SaveSecret(&store.Secret{Name: "wechat:account:oauth", Kind: store.SecretOAuth}, session); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	page := []store.WeChatContact{{Peer: "first", Title: "Рабочий чат"}, {Peer: "second", Title: "Другой чат"}}
	if err := st.CommitWeChatContacts(ctx, account, nil, []byte{1}, page); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen(wechatScreen, "all", "РАБОЧИЙ", 0)
	if err != nil {
		t.Fatal(err)
	}
	matrix := docByID(doc, "wechat-chats")
	if matrix == nil || len(matrix.Children) != 1 {
		t.Fatal("search should show only the matching chat")
	}
	row := matrix.Children[0]
	if len(row.Cells) != 7 || row.Cells[0].State != "off" || row.Cells[1].State != "off" ||
		row.Cells[2].State != "na" || row.Cells[3].State != "off" || row.Cells[4].State != "na" ||
		row.Cells[5].State != "off" || row.Cells[6].State != "na" || len(row.Menu) != 1 {
		t.Fatalf("initial rights or navigation unavailable: %+v", row)
	}
	if _, err := s.wechatActions(actionRequest{Screen: wechatScreen, Action: row.Cells[0].Action.Action, Target: "first"}); err != nil {
		t.Fatal(err)
	}
	doc, err = s.buildScreen(wechatScreen, "subscribed", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	matrix = docByID(doc, "wechat-chats")
	if matrix == nil || len(matrix.Children) != 1 || matrix.Children[0].Cells[0].State != "on" ||
		matrix.Children[0].Cells[2].State != "off" || matrix.Children[0].Cells[3].State != "off" || matrix.Children[0].Cells[6].State != "off" {
		t.Fatal("the subscription should be listed independently of send permission")
	}
	target := row.Menu[0].Target
	redirect, err := s.wechatActions(actionRequest{Screen: target, Action: "wechat_enable_send", Target: "first"})
	if err != nil || redirect != target {
		t.Fatalf("changing a chat right redirected away: %q, %v", redirect, err)
	}
	chat, err := st.WeChatChat(ctx, account, "first")
	if err != nil || !chat.Collect || !chat.CanSend {
		t.Fatalf("chat rights not saved: %+v, %v", chat, err)
	}
	detail, err := s.buildScreen(target, "", "", 0)
	if err != nil || detail.Heading != "Рабочий чат" {
		t.Fatalf("chat detail unavailable: %v", err)
	}
	for _, act := range []string{"wechat_enable_files", "wechat_enable_wait", "wechat_enable_reply", "wechat_enable_blocked"} {
		if _, err := s.wechatActions(actionRequest{Screen: target, Action: act, Target: "first"}); err != nil {
			t.Fatal(err)
		}
	}
	chat, err = st.WeChatChat(ctx, account, "first")
	if err != nil || !chat.Blocked || chat.Collect || chat.Files || chat.CanSend || chat.Wait || chat.Reply {
		t.Fatalf("blocked chat retained rights: %+v, %v", chat, err)
	}
	if _, err := s.wechatActions(actionRequest{Screen: target, Action: "wechat_enable_send", Target: "first"}); err == nil {
		t.Fatal("blocked chat accepted a direct request to enable sending")
	}
}
