package wechat

import (
	"bytes"
	"encoding/binary"
	"image"
	"net"
	"testing"
)

func TestContactPageAndCursor(t *testing.T) {
	pair, _ := appendUint(nil, 1, 11)
	pair, _ = appendUint(pair, 2, ^uint64(0))
	cursor, _ := appendBytes(nil, 1, pair)
	base, _ := appendUint(nil, 1, 0)
	response, _ := appendBytes(nil, 1, base)
	response, _ = appendUint(response, 2, contactSelector)
	response, _ = appendBytes(response, 3, cursor)
	peer, _ := appendBytes(nil, 1, []byte("peer"))
	contact, _ := appendBytes(nil, 1, peer)
	command, _ := appendUint(nil, 1, 2)
	command, _ = appendBytes(command, 2, contact)
	response, _ = appendBytes(response, 4, command)
	page, err := decodeContactPage(response, nil)
	if err != nil || !page.More || !bytes.Equal(page.Cursor, cursor) || len(page.Contacts) != 1 || page.Contacts[0].Peer != "peer" {
		t.Fatalf("contact page %+v: %v", page, err)
	}
	if _, err := decodeContactPage(response, cursor); err == nil {
		t.Fatal("accepted continuation with unchanged cursor")
	}
	if err := validateContactCursor(append(append([]byte{}, cursor...), cursor...)); err == nil {
		t.Fatal("accepted duplicate keys")
	}
}

func TestLANChallengeFraming(t *testing.T) {
	request, err := makeLANRequest(loginDevice{id: []byte("synthetic"), kind: "Linux", version: 1}, []byte("qr"), []net.IP{net.ParseIP("192.168.1.10")}, 8050)
	if err != nil {
		t.Fatal(err)
	}
	f, err := decodeFields(request)
	if err != nil || len(f) != 4 || string(f[2].data) != "qr" || !bytes.Contains(f[1].data, []byte("192.168.1.10")) {
		t.Fatalf("LAN request: %v", err)
	}
	base, _ := appendUint(nil, 1, 0)
	reply, _ := appendBytes(nil, 1, base)
	reply, _ = appendBytes(reply, 2, []byte("synthetic"))
	frame, err := decodeLANFrame(reply)
	if err != nil || len(frame) < 4 || int(binary.BigEndian.Uint32(frame[:4])) != len(frame) || string(frame[4:]) != `{"scene":"xwechat","token":"synthetic"}` {
		t.Fatalf("LAN frame %q: %v", frame, err)
	}
}

func TestImageCDNAndCorrelatedResponse(t *testing.T) {
	thumbnail, err := resizeJPEG(image.NewRGBA(image.Rect(0, 0, 640, 480)), 320, 75)
	if err != nil || thumbnail.width != 320 || thumbnail.height != 240 {
		t.Fatalf("thumbnail %dx%d: %v", thumbnail.width, thumbnail.height, err)
	}
	key := make([]byte, 16)
	f := cdnFields{}
	if err := imageCDNFields(f, key, 640, 480, thumbnail, thumbnail); err != nil {
		t.Fatal(err)
	}
	plain, err := cdnCrypt(f["thumbdata"], key, true)
	if err != nil || !bytes.Equal(plain, thumbnail.jpeg) || adlerZero([]byte("A")) != 0x00410041 {
		t.Fatalf("encrypted thumbnail: %v", err)
	}
	base, _ := appendUint(nil, 1, 0)
	reply, _ := appendBytes(nil, 1, base)
	for _, n := range []uint32{2, 6, 7, 8, 9, 10} {
		reply, _ = appendUint(reply, n, 42)
	}
	for i, s := range []string{"id", "owner", "peer"} {
		wrapped, _ := appendBytes(nil, 1, []byte(s))
		reply, _ = appendBytes(reply, uint32(i+3), wrapped)
	}
	got, err := decodeImageSend(reply, "id", "owner", "peer")
	if err != nil || got.ServerID != "42" || got.Rejected {
		t.Fatalf("image send reply %+v: %v", got, err)
	}
	if _, err := decodeImageSend(reply, "id", "owner", "other"); err == nil {
		t.Fatal("accepted image reply for another peer")
	}
}
