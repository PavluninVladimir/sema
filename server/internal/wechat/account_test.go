package wechat

import (
	"bytes"
	"testing"
)

func TestInitialAccountRequiresConfirmedIdentityAndKeys(t *testing.T) {
	account, _ := appendBytes(nil, 1, []byte("test-account"))
	auth, _ := appendUint(nil, 1, 42)
	password := bytes.Repeat([]byte{6}, 16)
	wrapped, _ := packBuffer(password)
	auth, _ = appendBytes(auth, 3, wrapped)
	makeReply := func(mask uint64, auth []byte) []byte {
		b, _ := appendBytes(nil, 1, []byte{8, 0})
		b, _ = appendUint(b, 2, mask)
		b, _ = appendBytes(b, 3, auth)
		b, _ = appendBytes(b, 4, account)
		return b
	}
	state, err := initialAccount(makeReply(3, auth), []byte{1}, loginDevice{id: []byte("device")}, nil)
	if err != nil || state.UIN != 42 || state.Username != "test-account" || !bytes.Equal(state.SessionKey, password) {
		t.Fatalf("account decode failed: %v", err)
	}
	defer state.close()
	if _, err = initialAccount(makeReply(2, auth), nil, loginDevice{}, nil); err == nil {
		t.Fatal("initial account with no key update accepted")
	}
	if _, err = initialAccount(makeReply(3, []byte{8, 42}), nil, loginDevice{}, nil); err == nil {
		t.Fatal("initial account with missing keys accepted")
	}
}

func TestSyncCursorMergeAndReset(t *testing.T) {
	prior := []byte{8, 1, 18, 4, 8, 7, 16, 9}
	next := []byte{8, 2, 18, 4, 8, 7, 16, 3, 18, 4, 8, 8, 16, 12}
	got, err := mergeCursor(prior, next, false)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := readCursor(got)
	if err != nil || fields[7] != 9 || fields[8] != 12 {
		t.Fatalf("merged cursor: %v %v", fields, err)
	}
	got, err = mergeCursor(prior, next, true)
	if err != nil || !bytes.Equal(got, next) {
		t.Fatalf("reset: %x %v", got, err)
	}
	if _, err = mergeCursor(prior, []byte{0}, false); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}
