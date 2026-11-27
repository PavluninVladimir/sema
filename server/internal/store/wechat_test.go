package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWeChatMigratesExistingFileAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "from-old-wechat.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE wechat_chats (
		account TEXT NOT NULL, peer TEXT NOT NULL, last_at TEXT NOT NULL,
		collect INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(account,peer));
		INSERT INTO wechat_chats VALUES ('account','legacy','',1),('account','quiet','',0)`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for peer, want := range map[string]bool{"legacy": true, "quiet": false} {
		chat, err := s.WeChatChat(ctx, "account", peer)
		if err != nil || chat.Files != want || chat.Blocked || chat.Wait {
			_ = s.Close()
			t.Fatalf("migration for %s: %+v, %v", peer, chat, err)
		}
	}
	if err := s.SetWeChatFiles(ctx, "account", "legacy", false); err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	chat, err := s.WeChatChat(ctx, "account", "legacy")
	if err != nil || chat.Files || !chat.Collect {
		t.Fatalf("reopening restored revoked file rights: %+v, %v", chat, err)
	}
}

func TestWeChatFileAndWaitRights(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	account, peer := "account", "peer"
	if err := s.CommitWeChatContacts(ctx, account, nil, []byte{1}, []WeChatContact{{Peer: peer}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeChatCollect(ctx, account, peer, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeChatWait(ctx, account, peer, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	batch := WeChatBatch{Messages: []WeChatMessage{
		{Peer: peer, ID: "text", Kind: 1, Body: []byte("text"), At: now},
		{Peer: peer, ID: "image", Kind: 3, Body: []byte("descriptor"), At: now},
	}}
	if err := s.CommitWeChatBatch(ctx, account, nil, []byte{1}, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WeChatFileBody(ctx, account, peer, "image"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("file access during disabled collection: %v", err)
	}
	chat, err := s.WeChatChat(ctx, account, peer)
	if err != nil || chat.Count != 1 {
		t.Fatalf("attachment without permission was archived: %+v %v", chat, err)
	}
	waiting, err := s.WeChatWaitingChats(ctx, account)
	if err != nil || len(waiting) != 1 || waiting[0].Latest == 0 {
		t.Fatalf("waiting message missing: %+v %v", waiting, err)
	}
	if err := s.SetWeChatFiles(ctx, account, peer, true); err != nil {
		t.Fatal(err)
	}
	batch.Messages = []WeChatMessage{
		{Peer: peer, ID: "image2", Kind: 3, Body: []byte("descriptor"), At: now.Add(time.Second)},
		{Peer: peer, ID: "reply", Kind: 1, Outgoing: true, At: now.Add(time.Second)},
	}
	if err := s.CommitWeChatBatch(ctx, account, []byte{1}, []byte{2}, batch); err != nil {
		t.Fatal(err)
	}
	if body, err := s.WeChatFileBody(ctx, account, peer, "image2"); err != nil || len(body) == 0 {
		t.Fatalf("file with enabled permission unavailable: %v", err)
	}
	waiting, err = s.WeChatWaitingChats(ctx, account)
	if err != nil || len(waiting) != 1 || waiting[0].Latest != 0 {
		t.Fatalf("a reply did not clear the wait: %+v %v", waiting, err)
	}
}

func TestWeChatResumesAfterStoreReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wechat.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.CommitWeChatBatch(ctx, "account", nil, []byte{1}, WeChatBatch{Contacts: []WeChatContact{{Peer: "chat", Title: "Chat"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitWeChatContacts(ctx, "account", nil, []byte{9}, []WeChatContact{{Peer: "directory", Title: "Directory"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitWeChatContacts(ctx, "account", []byte{9}, []byte{10}, []WeChatContact{{Peer: "uncommitted", Title: "Uncommitted"}, {}}); err == nil {
		t.Fatal("malformed contact page accepted")
	}
	if err := s.SetWeChatCollect(ctx, "account", "chat", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeChatSend(ctx, "account", "chat", true); err != nil {
		t.Fatal(err)
	}
	msg := WeChatMessage{Peer: "chat", ID: "server:1", Kind: 1, Body: []byte("text"), At: time.Unix(100, 0)}
	if err := s.CommitWeChatBatch(ctx, "account", []byte{1}, []byte{2}, WeChatBatch{Messages: []WeChatMessage{msg}, EventModules: []string{"subscriber"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginWeChatSend(ctx, "account", "chat", 42, "test-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cursor, err := reopened.WeChatCursor(ctx, "account")
	if err != nil || !bytes.Equal(cursor, []byte{2}) {
		t.Fatalf("cursor %v: %v", cursor, err)
	}
	contacts, err := reopened.WeChatContactCursor(ctx, "account")
	if err != nil || !bytes.Equal(contacts, []byte{9}) {
		t.Fatalf("contact cursor %v: %v", contacts, err)
	}
	if rows, err := reopened.WeChatChats(ctx, "account", "uncommitted"); err != nil || len(rows) != 0 {
		t.Fatalf("failed page left contacts: %v, %v", rows, err)
	}
	chats, err := reopened.WeChatChats(ctx, "account", "Chat")
	if err != nil || len(chats) != 1 || chats[0].Title != "Chat" || !chats[0].Collect || !chats[0].CanSend {
		t.Fatalf("chats %+v: %v", chats, err)
	}
	if err := reopened.CommitWeChatBatch(ctx, "account", cursor, []byte{3}, WeChatBatch{Messages: []WeChatMessage{msg}, EventModules: []string{"subscriber"}}); err != nil {
		t.Fatal(err)
	}
	messages, err := reopened.WeChatText(ctx, "account", "chat")
	if err != nil || len(messages) != 1 || messages[0] != "text" {
		t.Fatalf("messages %v: %v", messages, err)
	}
	events, err := reopened.PendingModuleEvents(10)
	if err != nil || len(events) != 1 || events[0].Kind != EventWeChatMessageAdded || events[0].Module != "subscriber" {
		t.Fatalf("events %v: %v", events, err)
	}
	kind, at, err := reopened.WeChatEvent("account", events[0].RefID)
	if err != nil || kind != 1 || at != "1970-01-01T00:01:40Z" {
		t.Fatalf("event metadata %d %q: %v", kind, at, err)
	}
	if err := reopened.SetWeChatCollect(ctx, "account", "chat", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.WeChatEvent("account", events[0].RefID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("event available after revocation: %v", err)
	}
	if err := reopened.BeginWeChatSend(ctx, "account", "chat", 43, "test-key"); !errors.Is(err, ErrWeChatOperationStarted) {
		t.Fatal("pending delivery admitted again after reopening store")
	}
}

func TestWeChatCursorAndMessagesCommitTogether(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	msg := WeChatMessage{Peer: "chat", ID: "server:1", Body: []byte("text"), At: time.Unix(100, 0)}
	if err := st.CommitWeChatBatch(ctx, "account", nil, []byte{1}, WeChatBatch{Messages: []WeChatMessage{msg}}); err != nil {
		t.Fatal(err)
	}
	var count int
	countMessages := func(want int) {
		t.Helper()
		if err := st.db.QueryRow(`SELECT count(*) FROM wechat_messages`).Scan(&count); err != nil || count != want {
			t.Fatalf("count %d, want %d: %v", count, want, err)
		}
	}
	countMessages(0)
	if err := st.SetWeChatCollect(ctx, "account", "chat", true); err != nil {
		t.Fatal(err)
	}
	if err := st.CommitWeChatBatch(ctx, "account", []byte{1}, []byte{2}, WeChatBatch{Messages: []WeChatMessage{msg, msg}}); err != nil {
		t.Fatal(err)
	}
	countMessages(1)
	if err := st.CommitWeChatBatch(ctx, "account", []byte{1}, []byte{3}, WeChatBatch{}); !errors.Is(err, ErrWeChatCursorChanged) {
		t.Fatalf("stale cursor: %v", err)
	}
	msg.ID = "server:2"
	if err := st.CommitWeChatBatch(ctx, "account", []byte{2}, []byte{3}, WeChatBatch{Messages: []WeChatMessage{msg, {}}}); err == nil {
		t.Fatal("invalid message accepted")
	}
	countMessages(1)
	cursor, err := st.WeChatCursor(ctx, "account")
	if err != nil || !bytes.Equal(cursor, []byte{2}) {
		t.Fatalf("cursor %v: %v", cursor, err)
	}
}

func TestWeChatSendAdmissionAndResult(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.BeginWeChatSend(ctx, "account", "chat", 42, ""); !errors.Is(err, ErrWeChatSendDenied) {
		t.Fatalf("no grant: %v", err)
	}
	msg := WeChatMessage{Peer: "chat", ID: "a", At: time.Unix(100, 0)}
	if err := s.CommitWeChatBatch(ctx, "account", nil, []byte{1}, WeChatBatch{Messages: []WeChatMessage{msg}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeChatSend(ctx, "account", "chat", true); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginWeChatSend(ctx, "account", "chat", 42, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginWeChatSend(ctx, "account", "chat", 42, ""); err == nil {
		t.Fatal("duplicate delivery admitted")
	}
	if err := s.EndWeChatSend(ctx, "account", 42, "confirmed", "123"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndWeChatSend(ctx, "account", 42, "rejected", ""); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM wechat_outbox WHERE account='account' AND client_id=42`).Scan(&state); err != nil || state != "confirmed" {
		t.Fatalf("state %q: %v", state, err)
	}
	if err := s.SetWeChatSend(ctx, "account", "chat", false); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginWeChatSend(ctx, "account", "chat", 43, ""); !errors.Is(err, ErrWeChatSendDenied) {
		t.Fatalf("revocation: %v", err)
	}
}
