package wechat

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestMMTLSIndependentServerRecord(t *testing.T) {
	shared := unhex(t, "005543894af3d00ed7d740abdbd75c96b06877b787db5f70eea78b90a8d7c00a")
	secret := sha256.Sum256(shared)
	keys, err := deriveMMTLSTraffic(secret[:], "handshake key expansion", []byte("synthetic client hellosynthetic server hello"))
	if err != nil {
		t.Fatal(err)
	}
	wire := unhex(t, "16f10400314bbd4e0fa2f28903e143496cf3f42dd756b3e792e8359afb180b5bfaff820fcb03742b63ba60e98e9e3ac32f99f851f031")
	record, err := parseMMTLSRecord(wire)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := openMMTLS(record, keys.serverKey, keys.serverIV, 1)
	if err != nil || string(plain) != "synthetic server signature record" {
		t.Fatalf("record %q: %v", plain, err)
	}
	if _, err = openMMTLS(record, keys.serverKey, keys.serverIV, 2); err == nil {
		t.Fatal("wrong sequence accepted")
	}
	record.body[len(record.body)-1] ^= 1
	if _, err = openMMTLS(record, keys.serverKey, keys.serverIV, 1); err == nil {
		t.Fatal("tampering accepted")
	}
}

func TestMMTLSFraming(t *testing.T) {
	key := bytes.Repeat([]byte{2}, 16)
	iv := bytes.Repeat([]byte{3}, 12)
	plain := []byte("hello")
	wire, err := sealMMTLS(0x17, plain, key, iv, 9)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := parseMMTLSRecord(wire)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openMMTLS(rec, key, iv, 9)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %v", err)
	}
	for _, bad := range [][]byte{nil, wire[:4], wire[:len(wire)-1], append(bytes.Clone(wire), 1), {0x17, 0, 0, 0, 0}} {
		if _, err := parseMMTLSRecord(bad); err == nil {
			t.Fatalf("invalid record accepted %x", bad)
		}
	}
	if _, err := sealMMTLS(0x17, make([]byte, mmtlsMaxPlain+1), key, iv, 0); err == nil {
		t.Fatal("large record accepted")
	}
}
