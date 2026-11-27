package wechat

import (
	"bytes"
	"testing"
)

func TestFileDescriptorRoundTrip(t *testing.T) {
	f := uploadedFile{name: "имя & файл.txt", id: "remote", md5: "d41d8cd98f00b204e9800998ecf8427e", size: 5, key: bytes.Repeat([]byte{2}, 16)}
	state := &accountState{UIN: 42, Username: "owner", DeviceID: []byte("device"), DeviceKind: "test", ClientVersion: 42}
	request, err := fileSendRequest(state, "peer", "task", 100, f)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := decodeFields(request)
	if err != nil {
		t.Fatal(err)
	}
	app, err := dataField(fields, 2)
	if err != nil {
		t.Fatal(err)
	}
	fields, err = decodeFields(app)
	if err != nil {
		t.Fatal(err)
	}
	xml, err := dataField(fields, 6)
	if err != nil {
		t.Fatal(err)
	}
	info, descriptor, err := decodeFileCarrier(append([]byte("sender:\n"), xml...))
	if err != nil || info.Name != f.name || descriptor.id != f.id || !bytes.Equal(descriptor.key, f.key) {
		t.Fatalf("descriptor round trip: %v", err)
	}
	if _, err = InspectFile(bytes.Replace(xml, []byte("<title>"), []byte("<title>../"), 1)); err == nil {
		t.Fatal("unsafe file name accepted")
	}
}
