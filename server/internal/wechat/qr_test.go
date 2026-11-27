package wechat

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestQRRequests(t *testing.T) {
	device := loginDevice{id: []byte("device"), kind: "test", version: 42}
	request, err := makeQRRequest(device, "Сёма")
	if err != nil {
		t.Fatal(err)
	}
	fields, err := decodeFields(request)
	if err != nil {
		t.Fatal(err)
	}
	name, err := dataField(fields, 4)
	if err != nil || string(name) != "Сёма" {
		t.Fatalf("name %q %v", name, err)
	}
	wrapped, err := dataField(fields, 2)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := unpackBuffer(wrapped)
	if err != nil || len(nonce) != 16 {
		t.Fatalf("nonce: %d %v", len(nonce), err)
	}
	request, err = makePollRequest(device, []byte("id"), time.UnixMilli((1<<32)+123456))
	if err != nil {
		t.Fatal(err)
	}
	fields, err = decodeFields(request)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := scalarField(fields, 4)
	if err != nil || ts != 123 {
		t.Fatalf("timestamp: %d %v", ts, err)
	}
}

func TestLoginPollStates(t *testing.T) {
	key := bytes.Repeat([]byte{6}, 16)
	ok, _ := appendBytes(nil, 1, []byte{8, 0})
	rejected, _ := appendBytes(nil, 1, []byte{8, 7})
	if poll, err := decodeLoginPoll(ok, key); err != nil || poll.status != nil {
		t.Fatalf("waiting: %+v %v", poll, err)
	}
	var svc *serviceError
	if _, err := decodeLoginPoll(rejected, key); !errors.As(err, &svc) || svc.code != 7 {
		t.Fatalf("rejected: %v", err)
	}
	packet := func(notify []byte) []byte {
		enc, err := legacyCBC(notify, key, false)
		if err != nil {
			t.Fatal(err)
		}
		buf, _ := packBuffer(enc)
		inner, _ := appendBytes(nil, 1, buf)
		inner, _ = appendUint(inner, 2, 1)
		b, _ := appendBytes(bytes.Clone(ok), 3, inner)
		return b
	}
	approved := []byte{16, 2, 26, 1, 1, 34, 1, 2}
	poll, err := decodeLoginPoll(packet(approved), key)
	if err != nil || poll.status == nil || *poll.status != 2 || !bytes.Equal(poll.credential4, []byte{2}) || poll.lanChallenge {
		t.Fatalf("approved: %+v %v", poll, err)
	}
	if _, err = decodeLoginPoll(packet([]byte{16, 2}), key); err == nil {
		t.Fatal("accepted approval without credentials")
	}
	risk, _ := packBuffer([]byte{8, 11})
	notify, _ := appendBytes([]byte{16, 2}, 17, risk)
	poll, err = decodeLoginPoll(packet(notify), key)
	if err != nil || !poll.lanChallenge {
		t.Fatalf("LAN challenge: %+v %v", poll, err)
	}
}
