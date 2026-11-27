package wechat

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func mustSection(t *testing.T, b []byte) []byte {
	t.Helper()
	v, e := handshakeSection(b)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func mustSized(t *testing.T, b []byte) []byte {
	t.Helper()
	v, e := handshakeSized(b)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestServerHello(t *testing.T) {
	point := append([]byte{4}, make([]byte, 64)...)
	share := append([]byte{0, 0x11, 0, 0, 0, 5}, mustSized(t, point)...)
	cert := []byte{0, 0x13, 0, 0, 0, 1, 0, 0, 0, 5}
	ext := append([]byte{2}, mustSection(t, share)...)
	ext = append(ext, mustSection(t, cert)...)
	body := []byte{2, 0x04, 0xf1, 0xc0, 0x2b}
	body = append(body, make([]byte, 32)...)
	body = append(body, mustSection(t, ext)...)
	valid := mustSection(t, body)
	got, e := parseServerHello(valid)
	if e != nil || got.keyID != 5 || got.version != 5 || !bytes.Equal(got.point, point) {
		t.Fatalf("hello: %#v %v", got, e)
	}
	for n := 0; n < len(valid); n++ {
		if _, e := parseServerHello(valid[:n]); e == nil {
			t.Fatalf("accepted truncated %d", n)
		}
	}
	duplicated := append([]byte{3}, mustSection(t, share)...)
	duplicated = append(duplicated, mustSection(t, share)...)
	duplicated = append(duplicated, mustSection(t, cert)...)
	body = body[:37]
	body = append(body, mustSection(t, duplicated)...)
	if _, e := parseServerHello(mustSection(t, body)); e == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestMMTLSTickets(t *testing.T) {
	ticket := []byte{1, 0, 0, 0, 30, 0, 0, 0, 0, 0, 7}
	ticket = append(ticket, mustSized(t, make([]byte, 12))...)
	ticket = append(ticket, mustSized(t, []byte{9, 8, 7})...)
	valid := mustSection(t, append([]byte{4, 1}, mustSection(t, ticket)...))
	got, e := parseMMTLSTickets(valid)
	if e != nil || len(got) != 1 || got[0].lifetime != 30 || got[0].reserved != 7 || !bytes.Equal(got[0].opaque, []byte{9, 8, 7}) {
		t.Fatalf("ticket: %#v %v", got, e)
	}
	for n := 0; n < len(valid); n++ {
		if _, e := parseMMTLSTickets(valid[:n]); e == nil {
			t.Fatalf("accepted truncated %d", n)
		}
	}
	bad := bytes.Clone(valid)
	binary.BigEndian.PutUint32(bad[:4], uint32(len(bad)-5))
	if _, e := parseMMTLSTickets(bad); e == nil {
		t.Fatal("bad length accepted")
	}
}

func TestSignedHandshakePayload(t *testing.T) {
	body := append([]byte{0x0f}, mustSized(t, []byte{1, 2, 3})...)
	wire := mustSection(t, body)
	got, e := signedHandshakePayload(wire, 0x0f)
	if e != nil || !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("signed payload: %x %v", got, e)
	}
	if _, e := signedHandshakePayload(wire, 0x14); e == nil {
		t.Fatal("wrong handshake type accepted")
	}
	if _, e := signedHandshakePayload(append(wire, 1), 0x0f); e == nil {
		t.Fatal("trailing bytes accepted")
	}
}
