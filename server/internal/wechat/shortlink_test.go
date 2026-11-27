package wechat

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestShortlinkRequestSplitsRecords(t *testing.T) {
	s := &mmtlsSession{accessKey: bytes.Repeat([]byte{0x42}, 32), ticket: mmtlsTicket{kind: 1, lifetime: 3600, nonce: make([]byte, 12), opaque: []byte{7}}}
	payload := bytes.Repeat([]byte{0xa5}, 200000)
	const host = "short.weixin.qq.com"
	const path = "/cgi-bin/micromsg-bin/newsync"
	hello, ext, wire, err := prepareShortExchange(s, host, path, payload, 123)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(wire)
	record, err := readMMTLSRecord(r)
	if err != nil || record.header[0] != 0x19 || !bytes.Equal(record.body, hello) {
		t.Fatal("bad early hello", err)
	}
	keys, err := transcriptKey(s.accessKey, "early data key expansion", hello, 28)
	if err != nil {
		t.Fatal(err)
	}
	var route []byte
	for seq := uint64(1); r.Len() > 0; seq++ {
		f, err := readMMTLSRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		p, err := openMMTLS(f, keys[:16], keys[16:], seq)
		if err != nil {
			t.Fatal(err)
		}
		if seq == 1 {
			if !bytes.Equal(p, ext) || f.header[0] != 0x19 {
				t.Fatal("bad extensions")
			}
		} else if r.Len() == 0 {
			if seq < 4 || f.header[0] != 0x15 || !bytes.Equal(p, []byte{0, 0, 0, 3, 0, 1, 1}) {
				t.Fatal("bad close")
			}
		} else {
			if f.header[0] != 0x17 {
				t.Fatal("bad data record")
			}
			route = append(route, p...)
		}
	}
	expected := append(mustSized(t, []byte(path)), mustSized(t, []byte(host))...)
	expected = binary.BigEndian.AppendUint32(expected, uint32(len(payload)))
	expected = append(expected, payload...)
	if !bytes.Equal(route, mustSection(t, expected)) {
		t.Fatal("route did not reassemble")
	}
}

func TestShortlinkReplyRequiresAuthenticatedClose(t *testing.T) {
	access := bytes.Repeat([]byte{1}, 32)
	hello, ext := []byte("client hello"), []byte("extensions")
	serverHello := append([]byte{2, 4, 0xf1, 0, 0xa8}, make([]byte, 32)...)
	serverHello = mustSection(t, append(serverHello, 0, 0, 0, 1, 0))
	transcript := append(append(bytes.Clone(hello), ext...), serverHello...)
	keys, err := transcriptKey(access, "handshake key expansion", transcript, 28)
	if err != nil {
		t.Fatal(err)
	}
	finish, err := finishedMAC(access, "server finished", transcript)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := mmtlsFrame(0x16, serverHello)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range []struct {
		typ  byte
		data []byte
	}{
		{0x16, mustSection(t, append([]byte{0x14}, mustSized(t, finish)...))},
		{0x17, []byte("one")},
		{0x17, []byte("two")},
		{0x15, []byte{0, 0, 0, 3, 0, 1, 1}},
	} {
		f, err := sealMMTLS(item.typ, item.data, keys[:16], keys[16:], uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		wire = append(wire, f...)
	}
	got, err := decodeShortReply(wire, access, hello, ext)
	if err != nil || string(got) != "onetwo" {
		t.Fatalf("reply %q: %v", got, err)
	}
	for _, bad := range [][]byte{wire[:len(wire)-28], append(bytes.Clone(wire), 0), append(bytes.Clone(wire[:len(wire)-1]), wire[len(wire)-1]^1)} {
		if _, err := decodeShortReply(bad, access, hello, ext); err == nil {
			t.Fatal("invalid close accepted")
		}
	}
}

func TestShortlinkRejectsUnexpectedDestinations(t *testing.T) {
	for _, host := range []string{"weixin.qq.com.example", "user@short.weixin.qq.com", "https://short.weixin.qq.com", "short.weixin.qq.com:80", "..weixin.qq.com"} {
		if validWeChatHost(host) {
			t.Errorf("accepted host %q", host)
		}
	}
	for _, path := range []string{"/cgi-bin/micromsg-bin/../newsync", "/cgi-bin/micromsg-bin/newsync?x=1", "/cgi-bin/micromsg-bin/", "/cgi-bin/micromsg-bin/a/b"} {
		if validShortRoute(path) {
			t.Errorf("accepted path %q", path)
		}
	}
}
