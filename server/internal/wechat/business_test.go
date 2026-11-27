package wechat

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestBusinessReply(t *testing.T) {
	wire := []byte{0, 0, 0xff, 0xff, 0xff, 0xfe, 0, 0, 0, 0, 91, 92, 0x83, 1, 3, 3, 0, 0}
	binary.LittleEndian.PutUint16(wire, uint16(len(wire))<<2|2|2<<8|13<<12)
	wire = append(wire, 1, 2, 3)
	got, err := splitBusiness(wire)
	if err != nil || got.code != -2 || got.cgi != 131 || got.originalLength != 3 || got.preEncryptionLength != 3 ||
		got.encryption != 13 || got.compression != 2 || !bytes.Equal(got.cookie, []byte{91, 92}) || !bytes.Equal(got.payload, []byte{1, 2, 3}) {
		t.Fatalf("reply: %+v %v", got, err)
	}
	for n := 0; n < 18; n++ {
		if _, err := splitBusiness(wire[:n]); err == nil {
			t.Fatalf("truncated header accepted at %d", n)
		}
	}
}

func TestBusinessRequestHeader(t *testing.T) {
	got, err := packBusiness(packetPolicy{clientVersion: 42, uin: 7, cgi: 131, cookie: []byte{91, 92}, originalLength: 3, preEncryptionLength: 3, compression: 2, encryption: 13, sequence: 4}, []byte{1, 2, 3})
	want := []byte{0xbf, 0x62, 0xd2, 0, 0, 0, 42, 0, 0, 0, 7, 91, 92, 0x83, 1, 3, 3, 0, 0, 0, 0xff, 0, 0, 0, 0, 0, 4, 1, 2, 3}
	binary.LittleEndian.PutUint16(want[1:], uint16(len(want)-3)<<2|2|2<<8|13<<12)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("request: %x want %x: %v", got, want, err)
	}
}
