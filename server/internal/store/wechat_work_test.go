package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestWeChatAutomationQueueAndRevocation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const acc = "owner"
	if err := s.EnsureWeChatOwnerChat(ctx, acc); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitWeChatContacts(ctx, acc, nil, []byte{1}, []WeChatContact{{Peer: "friend"}, {Peer: "group@chatroom"}}); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"friend", "group@chatroom", "filehelper"} {
		if err := s.SetWeChatCollect(ctx, acc, peer, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetWeChatAutomation(ctx, acc, "friend", WeChatAdminMode, true); err == nil {
		t.Fatal("admin granted to another person")
	}
	if err := s.SetWeChatAutomation(ctx, acc, "group@chatroom", WeChatReplyMode, true); err == nil {
		t.Fatal("reply granted to a group")
	}
	if err := s.SetWeChatAutomation(ctx, acc, "friend", WeChatReplyMode, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeChatAutomation(ctx, acc, "filehelper", WeChatAdminMode, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	friend := WeChatMessage{Peer: "friend", Sender: "friend", ID: "first", Kind: 1, At: now, Body: []byte("hello")}
	fake := WeChatMessage{Peer: "filehelper", Sender: "other", ID: "fake", Kind: 1, Outgoing: true, At: now, Body: []byte("/sema статус")}
	ordinary := WeChatMessage{Peer: "filehelper", Sender: acc, ID: "ordinary", Kind: 1, Outgoing: true, At: now, Body: []byte("a private note")}
	owner := WeChatMessage{Peer: "filehelper", Sender: acc, ID: "owner", Kind: 1, Outgoing: true, At: now, Body: []byte("/sema статус")}
	batch := WeChatBatch{Messages: []WeChatMessage{friend, friend, fake, ordinary, owner}}
	if err := s.CommitWeChatBatch(ctx, acc, nil, []byte{1}, batch); err != nil {
		t.Fatal(err)
	}
	reply, err := s.ClaimWeChatWork(ctx, acc, now)
	if err != nil || reply.Mode != WeChatReplyMode || reply.Peer != "friend" {
		t.Fatalf("reply job: %+v %v", reply, err)
	}
	if err := s.BeginWeChatSend(ctx, acc, "friend", 101, ""); !errors.Is(err, ErrWeChatSendDenied) {
		t.Fatalf("reply permit allowed arbitrary send: %v", err)
	}
	if err := s.SetWeChatAutomation(ctx, acc, "friend", WeChatReplyMode, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeChatAutomation(ctx, acc, "friend", WeChatReplyMode, true); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginWeChatWorkSend(ctx, reply, 101, "reply-1"); !errors.Is(err, ErrWeChatSendDenied) {
		t.Fatalf("revoked reply allowed send: %v", err)
	}
	admin, err := s.ClaimWeChatWork(ctx, acc, now)
	if err != nil || admin.Mode != WeChatAdminMode || admin.Body != string(owner.Body) {
		t.Fatalf("admin job: %+v %v", admin, err)
	}
	if _, err := s.ClaimWeChatWork(ctx, acc, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate or unrelated event queued: %v", err)
	}
	if err := s.AddWeChatAdminRequest(ctx, admin, "fixture", "uptime"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimWeChatAdminRequest(ctx, "other", admin.Message); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other account claimed admin command: %v", err)
	}
	host, cmd, err := s.ClaimWeChatAdminRequest(ctx, acc, admin.Message)
	if err != nil || host != "fixture" || cmd != "uptime" {
		t.Fatalf("approved command: %q %q %v", host, cmd, err)
	}
	if _, _, err := s.ClaimWeChatAdminRequest(ctx, acc, admin.Message); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("double approval: %v", err)
	}
}
