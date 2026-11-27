package wechat

import "testing"

func TestTextSendReplyCorrelation(t *testing.T) {
	destination := "test-peer"
	dest, _ := appendBytes(nil, 1, []byte(destination))
	item, _ := appendUint(nil, 1, 0)
	item, _ = appendBytes(item, 2, dest)
	for n := uint32(3); n <= 7; n++ {
		item, _ = appendUint(item, n, 42)
	}
	item, _ = appendUint(item, 8, ^uint64(0))
	reply, _ := appendBytes(nil, 1, []byte{8, 0})
	reply, _ = appendUint(reply, 2, 1)
	reply, _ = appendBytes(reply, 3, item)
	got, err := decodeTextSend(reply, destination, 42)
	if err != nil || got.serverID != "18446744073709551615" {
		t.Fatalf("reply: %+v %v", got, err)
	}
	if _, err = decodeTextSend(reply, destination, 43); err == nil {
		t.Fatal("unrelated client ID accepted")
	}
	if _, err = decodeTextSend(reply, "other-peer", 42); err == nil {
		t.Fatal("unrelated destination accepted")
	}
	if _, err = decodeTextSend(reply[:len(reply)-1], destination, 42); err == nil {
		t.Fatal("truncated reply accepted")
	}
}
