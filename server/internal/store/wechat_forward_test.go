package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestWeChatForwardDeliveryAndRevocation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sema.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	account, peer, otherPeer := "wechat", "source@chatroom", "other@chatroom"
	first, second := WeChatForwardTarget{WeChatForwardTelegram, "telegram", 1}, WeChatForwardTarget{WeChatForwardTelegram, "telegram", 2}
	must(s.SaveTGAccount(&TGAccount{Phone: first.Account, Enabled: true, CanSend: true, State: "ok"}))
	must(s.SaveTGChat(&TGChat{Account: first.Account, ID: first.Chat, Title: "Алексей", Kind: "user", Send: true}))
	must(s.SaveTGChat(&TGChat{Account: second.Account, ID: second.Chat, Title: "Алексей", Kind: "user", Send: true}))
	must(s.CommitWeChatContacts(ctx, account, nil, []byte{1}, []WeChatContact{{Peer: peer}, {Peer: otherPeer}}))
	must(s.SetWeChatCollect(ctx, account, peer, true))
	must(s.SetWeChatCollect(ctx, account, otherPeer, true))

	enabledAt := time.Now().UTC().Truncate(time.Second)
	newAt := enabledAt.Add(time.Second)
	rule := WeChatForwardRule{Enabled: true, SourceLang: "Китайский", TargetLang: "Русский", Targets: []WeChatForwardTarget{first, second}}
	must(s.SaveWeChatForwardRule(ctx, account, peer, rule, enabledAt))
	body := []byte("你好")
	msgs := []WeChatMessage{
		{Peer: peer, ID: "old", Kind: 1, Body: body, At: enabledAt.Add(-time.Minute)},
		{Peer: peer, ID: "outgoing", Kind: 1, Body: body, At: newAt, Outgoing: true},
		{Peer: peer, ID: "image", Kind: 3, Body: body, At: newAt},
		{Peer: otherPeer, ID: "other", Kind: 1, Body: body, At: newAt},
		{Peer: peer, ID: "incoming", Kind: 1, Body: body, At: newAt},
	}
	must(s.CommitWeChatBatch(ctx, account, nil, []byte{1}, WeChatBatch{Messages: msgs}))
	must(s.CommitWeChatBatch(ctx, account, []byte{1}, []byte{2}, WeChatBatch{Messages: msgs}))
	m, targets, err := s.NextWeChatForward(ctx, account, newAt)
	must(err)
	if m.Body != string(body) || m.TargetLang != "Русский" || !slices.Equal(targets, rule.Targets) {
		t.Fatalf("unexpected message/destinations: %+v %+v", m, targets)
	}
	counts, err := s.WeChatForwardCounts(ctx, account, peer)
	must(err)
	if counts["pending"] != 2 {
		t.Fatalf("history, unrelated messages or duplicates were queued: %+v", counts)
	}

	started, err := s.BeginWeChatForward(ctx, m.ID, first)
	must(err)
	if !started {
		t.Fatal("first delivery did not start")
	}
	started, err = s.BeginWeChatForward(ctx, m.ID, first)
	must(err)
	if started {
		t.Fatal("delivery started twice")
	}
	must(s.Close())
	s, err = Open(path)
	must(err)
	m, targets, err = s.NextWeChatForward(ctx, account, newAt)
	must(err)
	if !slices.Equal(targets, []WeChatForwardTarget{second}) {
		t.Fatalf("interrupted delivery was requeued: %+v", targets)
	}
	started, err = s.BeginWeChatForward(ctx, m.ID, second)
	must(err)
	if !started {
		t.Fatal("pending second recipient could not continue after restart")
	}
	must(s.FinishWeChatForward(ctx, m.ID, second, true))
	counts, err = s.WeChatForwardCounts(ctx, account, peer)
	must(err)
	if counts["unknown"] != 1 || counts["sent"] != 1 {
		t.Fatalf("delivery states were lost: %+v", counts)
	}

	msgs = []WeChatMessage{{Peer: peer, ID: "second", Kind: 1, Body: body, At: newAt}}
	must(s.CommitWeChatBatch(ctx, account, []byte{2}, []byte{3}, WeChatBatch{Messages: msgs}))
	must(s.SetTGChatRight(first.Account, first.Chat, "send", false))
	must(s.SetTGChatRight(first.Account, first.Chat, "send", true))
	_, targets, err = s.NextWeChatForward(ctx, account, newAt)
	must(err)
	if !slices.Equal(targets, []WeChatForwardTarget{second}) {
		t.Fatalf("revoked delivery was restored: %+v", targets)
	}
	must(s.SetWeChatCollect(ctx, account, peer, false))
	must(s.SetWeChatCollect(ctx, account, peer, true))
	_, _, err = s.NextWeChatForward(ctx, account, newAt)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked source still has jobs: %v", err)
	}
	saved, err := s.WeChatForwardRule(ctx, account, peer)
	must(err)
	if saved.Enabled || !slices.Equal(saved.Targets, rule.Targets) {
		t.Fatalf("revoked rule should remain paused with its recipients: %+v", saved)
	}
}

func TestWeChatForwardMigratesTelegramAndDistinguishesMAX(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const account, peer = "owner", "chat@chatroom"
	at := time.Now().UTC().Truncate(time.Second)
	must(s.CommitWeChatContacts(ctx, account, nil, []byte{1}, []WeChatContact{{Peer: peer}}))
	must(s.SetWeChatCollect(ctx, account, peer, true))
	must(s.SaveTGAccount(&TGAccount{Phone: "+123", State: "ok", CanSend: true, Enabled: true}))
	must(s.SaveTGChat(&TGChat{Account: "+123", ID: 42, Title: "Телеграм", Kind: "group", Send: true}))
	must(s.SaveMAXAccount(&MAXAccount{Phone: "+123", State: "ok", CanSend: true}))
	must(s.SaveMAXChat(&MAXChat{ID: 42, Title: "Макс", Kind: "chat", Send: true}))
	msg := WeChatMessage{Peer: peer, ID: "legacy", Kind: 1, Body: []byte("你好"), At: at}
	must(s.CommitWeChatBatch(ctx, account, nil, []byte{1}, WeChatBatch{Messages: []WeChatMessage{msg}}))

	_, err := s.db.Exec(`
CREATE TABLE wechat_forward_targets(account TEXT, peer TEXT, tg_account TEXT, tg_chat INTEGER);
CREATE TABLE wechat_forward_deliveries(message INTEGER, tg_account TEXT, tg_chat INTEGER, state TEXT, attempts INTEGER, retry_at INTEGER);
INSERT INTO wechat_forward_rules VALUES('owner','chat@chatroom',1,'Китайский','Русский',0);
INSERT INTO wechat_forward_targets VALUES('owner','chat@chatroom','+123',42);
INSERT INTO wechat_forward_deliveries SELECT id,'+123',42,'sending',1,0 FROM wechat_messages;
`)
	must(err)
	must(s.migrateWeChatForward())
	must(s.migrateWeChatForward())
	tg := WeChatForwardTarget{WeChatForwardTelegram, "+123", 42}
	max := WeChatForwardTarget{WeChatForwardMAX, "+123", 42}
	rule, err := s.WeChatForwardRule(ctx, account, peer)
	must(err)
	if !rule.Enabled || rule.SourceLang != "Китайский" || !slices.Equal(rule.Targets, []WeChatForwardTarget{tg}) {
		t.Fatalf("old rule not migrated: %+v", rule)
	}
	counts, err := s.WeChatForwardCounts(ctx, account, peer)
	must(err)
	if counts["unknown"] != 1 {
		t.Fatalf("legacy delivery was lost or retried: %+v", counts)
	}

	rule.Targets = append(rule.Targets, max)
	must(s.SaveWeChatForwardRule(ctx, account, peer, rule, at))
	msg.ID, msg.At = "new", at.Add(time.Second)
	must(s.CommitWeChatBatch(ctx, account, []byte{1}, []byte{2}, WeChatBatch{Messages: []WeChatMessage{msg}}))
	_, targets, err := s.NextWeChatForward(ctx, account, msg.At)
	must(err)
	if !slices.Equal(targets, []WeChatForwardTarget{max, tg}) {
		t.Fatalf("different channels must keep independent deliveries: %+v", targets)
	}
	must(s.SetMAXChatRight(42, "send", false))
	must(s.SetMAXChatRight(42, "send", true))
	_, targets, err = s.NextWeChatForward(ctx, account, msg.At)
	must(err)
	if !slices.Equal(targets, []WeChatForwardTarget{tg}) {
		t.Fatalf("MAX revocation should only affect MAX: %+v", targets)
	}
}
